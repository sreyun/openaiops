package main

import (
	"testing"
	"time"

	"aiops-monitor/shared"
)

func TestVMHistoryCacheKeyIsStableWithinItsTTLBucket(t *testing.T) {
	// A relative window slides every second (from=now-24h, to=now). Without
	// bucketing BOTH ends the key changes on every poll and the hit rate is 0 —
	// which is the whole point of the cache.
	const step = int64(180) // 24h window
	base := int64(1_700_000_000)
	bucket := int64(vmHistoryCacheTTL(step) / time.Second)

	k1 := vmHistoryCacheKey("h1", base-86400, base, step, nil)
	k2 := vmHistoryCacheKey("h1", base-86400+1, base+1, step, nil)
	if k1 != k2 {
		t.Fatalf("key changed within one bucket:\n %s\n %s", k1, k2)
	}
	k3 := vmHistoryCacheKey("h1", base-86400+bucket, base+bucket, step, nil)
	if k1 == k3 {
		t.Fatal("key did not roll over at the bucket boundary — entries would never refresh")
	}
	if vmHistoryCacheKey("h1", base-86400, base, step, nil) == vmHistoryCacheKey("h2", base-86400, base, step, nil) {
		t.Fatal("key must separate hosts")
	}
	if vmHistoryCacheKey("h1", base-86400, base, step, nil) ==
		vmHistoryCacheKey("h1", base-86400, base, step, []string{"aiops_cpu_percent"}) {
		t.Fatal("key must separate metric-name subsets, or a narrow AI read would poison the chart read")
	}
}

// The TTL must stay well under the RAM overlay window: the overlay is what keeps
// a cached VM result from showing a stale tail.
func TestVMHistoryCacheTTLStaysUnderTheRAMOverlay(t *testing.T) {
	for _, step := range []int64{1, 5, 60, 180, 1260, 3600, 100000} {
		ttl := vmHistoryCacheTTL(step)
		if ttl < vmHistoryCacheMinTTL {
			t.Errorf("step %d: ttl %v below the floor", step, ttl)
		}
		if ttl > vmHistoryCacheMaxTTL {
			t.Errorf("step %d: ttl %v above the ceiling", step, ttl)
		}
		if ttl >= time.Duration(memHistoryOverlaySec)*time.Second {
			t.Errorf("step %d: ttl %v is not covered by the %ds RAM overlay — the chart tail could go stale",
				step, ttl, memHistoryOverlaySec)
		}
	}
}

func TestVMHistoryCacheGetPutAndExpiry(t *testing.T) {
	c := newVMHistoryCache()
	samples := []shared.Sample{
		{Timestamp: 1, Metrics: shared.Metrics{CPUPercent: 10}},
		{Timestamp: 2, Metrics: shared.Metrics{CPUPercent: 20}},
	}
	c.put("k", samples, time.Minute)

	got, ok := c.get("k")
	if !ok || len(got) != 2 || got[1].CPUPercent != 20 {
		t.Fatalf("cache miss or wrong payload: ok=%v got=%#v", ok, got)
	}
	// Callers must not be able to reach into the cached slice.
	got[0].CPUPercent = 999
	again, _ := c.get("k")
	if again[0].CPUPercent != 10 {
		t.Fatal("cache handed out an aliased slice; a caller mutated the stored entry")
	}

	c.put("expired", samples, time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	if _, ok := c.get("expired"); ok {
		t.Fatal("expired entry was served")
	}

	if _, ok := c.get("never-written"); ok {
		t.Fatal("unknown key reported a hit")
	}
	// Zero-length results must not be cached: that would pin an empty chart for a
	// whole TTL after a transient VM failure.
	c.put("empty", nil, time.Minute)
	if _, ok := c.get("empty"); ok {
		t.Fatal("an empty result was cached")
	}
}

func TestVMHistoryCacheableRejectsIncompleteWindow(t *testing.T) {
	from, to := int64(1_700_000_000), int64(1_700_000_000+24*3600)
	full := make([]shared.Sample, 48)
	span := to - from
	for i := range full {
		full[i].Timestamp = from + span*int64(i)/int64(len(full)-1)
	}
	if !vmHistoryCacheable(full, from, to) {
		t.Fatal("a full 24h series must be cacheable")
	}
	// VM just came back: two minutes of new samples at the end of a 24h window.
	tail := []shared.Sample{
		{Timestamp: to - 120},
		{Timestamp: to - 60},
		{Timestamp: to},
	}
	if vmHistoryCacheable(tail, from, to) {
		t.Fatal("a 2-minute tail must not be cached as 24h history")
	}
	if vmHistoryCacheable(nil, from, to) {
		t.Fatal("empty series must not be cached")
	}
}

func TestVMWriterShutdownStopsRun(t *testing.T) {
	v := newVMWriter(&ConfigStore{})
	go v.run()
	v.shutdown(2 * time.Second)
	select {
	case <-v.stopped:
	default:
		t.Fatal("run did not exit after shutdown")
	}
	// Second call must not panic on a closed stopCh.
	v.shutdown(time.Second)
}

func TestVMHistoryCacheEvictsWhenFull(t *testing.T) {
	c := newVMHistoryCache()
	samples := []shared.Sample{{Timestamp: 1}}
	for i := 0; i < vmHistoryCacheMaxEntries+50; i++ {
		c.put(string(rune('a'+i%26))+string(rune('a'+i/26)), samples, time.Minute)
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > vmHistoryCacheMaxEntries {
		t.Fatalf("cache grew to %d entries, cap is %d", n, vmHistoryCacheMaxEntries)
	}
}

// Ingest failures must never blank the charts. A single shared breaker is how a
// bad label from one agent (or a brief VM overload on the write path) took every
// host chart offline for the whole cool-down.
func TestVMWriterKeepsReadAndWriteBreakersSeparate(t *testing.T) {
	v := newVMWriter(nil)
	if v.writeBreaker() == nil || v.queryBreaker() == nil {
		t.Fatal("both breakers must be constructed up front (lazy init on a shared field is a data race)")
	}
	if v.writeBreaker() == v.queryBreaker() {
		t.Fatal("read and write share one breaker: an ingest hiccup would refuse every chart query")
	}
	for i := 0; i < 50; i++ {
		v.writeBreaker().failure()
	}
	if v.writeBreaker().allow() {
		t.Fatal("write breaker did not open after repeated failures")
	}
	if !v.queryBreaker().allow() {
		t.Fatal("write failures opened the read breaker — charts would go dark on an ingest problem")
	}
}

// After the cool-down exactly one probe may go through. Letting the whole
// backlog out at once means every one of them burns the full query timeout
// before falling back — the "charts take forever" half of the symptom.
func TestVMCircuitBreakerHalfOpenAdmitsOneProbe(t *testing.T) {
	b := &vmCircuitBreaker{threshold: 2, coolDown: 10 * time.Millisecond}
	b.failure()
	b.failure()
	if b.allow() {
		t.Fatal("breaker should be open right after tripping")
	}
	time.Sleep(20 * time.Millisecond)
	if !b.allow() {
		t.Fatal("cool-down elapsed but no probe was admitted")
	}
	if b.allow() {
		t.Fatal("a second caller got through while the probe was still outstanding")
	}
	// A failed probe goes straight back to open, without re-counting to threshold.
	b.failure()
	if b.allow() {
		t.Fatal("failed probe did not re-open the breaker")
	}
	// A successful probe closes it for everyone.
	time.Sleep(20 * time.Millisecond)
	if !b.allow() {
		t.Fatal("second cool-down did not admit a probe")
	}
	b.success()
	if !b.allow() || !b.allow() {
		t.Fatal("successful probe did not close the breaker")
	}
}
