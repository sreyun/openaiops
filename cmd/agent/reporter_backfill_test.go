package main

import (
	"testing"
	"time"

	"aiops-monitor/shared"
)

// spool a sample at a given unix time.
func spoolAt(t *serverTarget, ts int64, cpu float64) {
	t.spoolBackfill(shared.BackfillSample{Ts: ts, Metrics: shared.Metrics{CPUPercent: cpu}})
}

func TestBackfillKeepsRecentHourAtFullResolution(t *testing.T) {
	tg := &serverTarget{}
	now := int64(1_800_000_000)
	// 10s interval for 30 minutes → every sample must survive.
	n := 0
	for ts := now - 1800; ts <= now; ts += 10 {
		spoolAt(tg, ts, 1)
		n++
	}
	if got := tg.backfillPending(); got != n {
		t.Fatalf("recent window must not be thinned: got %d want %d", got, n)
	}
}

func TestBackfillThinsOlderSamplesAndDropsBeyondSevenDays(t *testing.T) {
	tg := &serverTarget{}
	now := int64(1_800_000_000)
	// 8 days of 10s samples would be ~69k entries at full resolution.
	for ts := now - 8*24*3600; ts <= now; ts += 10 {
		spoolAt(tg, ts, 1)
	}
	got := tg.backfillPending()
	if got == 0 {
		t.Fatal("buffer must not be empty")
	}
	if got > agentBackfillMaxSamples {
		t.Fatalf("hard cap breached: %d > %d", got, agentBackfillMaxSamples)
	}
	// 分级保留的期望条数：1h 全留 + 23h@60s + 6d@600s，允许一定裕量。
	if got > 3200 {
		t.Fatalf("tiered retention did not thin enough: %d entries (memory blowup on the monitored host)", got)
	}

	tg.backfillMu.Lock()
	items := append([]shared.BackfillSample(nil), tg.backfill...)
	tg.backfillMu.Unlock()

	maxAge := int64(agentBackfillMaxAge / time.Second)
	if age := now - items[0].Ts; age > maxAge {
		t.Fatalf("sample older than the 7-day cap survived: age=%ds", age)
	}
	// Ordering must stay ascending — the server replays them as history.
	for i := 1; i < len(items); i++ {
		if items[i].Ts < items[i-1].Ts {
			t.Fatalf("buffer lost time ordering at %d", i)
		}
	}
	// Spacing rules per tier.
	full := int64(agentBackfillFullWindow / time.Second)
	mid := int64(agentBackfillMidWindow / time.Second)
	for i := 1; i < len(items); i++ {
		age := now - items[i].Ts
		gap := items[i].Ts - items[i-1].Ts
		switch {
		case age <= full:
			// no constraint
		case age <= mid:
			if gap < int64(agentBackfillMidSpacing/time.Second) && now-items[i-1].Ts > full {
				t.Fatalf("1h-24h tier kept samples %ds apart", gap)
			}
		default:
			if gap < int64(agentBackfillOldSpacing/time.Second) {
				t.Fatalf("24h-7d tier kept samples %ds apart", gap)
			}
		}
	}
}

func TestBackfillTakeAndReturnPreserveOrder(t *testing.T) {
	tg := &serverTarget{}
	now := int64(1_800_000_000)
	for i := 0; i < 150; i++ {
		spoolAt(tg, now-int64(150-i)*10, float64(i))
	}
	batch := tg.takeBackfill()
	if len(batch) != agentBackfillPerBatch {
		t.Fatalf("batch size: got %d want %d", len(batch), agentBackfillPerBatch)
	}
	if batch[0].Metrics.CPUPercent != 0 {
		t.Fatalf("oldest must go first, got cpu=%v", batch[0].Metrics.CPUPercent)
	}
	remaining := tg.backfillPending()
	tg.returnBackfill(batch)
	if got := tg.backfillPending(); got != remaining+len(batch) {
		t.Fatalf("return lost samples: got %d want %d", got, remaining+len(batch))
	}
	tg.backfillMu.Lock()
	first := tg.backfill[0]
	tg.backfillMu.Unlock()
	if first.Metrics.CPUPercent != 0 {
		t.Fatalf("returned batch must go back to the FRONT, got cpu=%v", first.Metrics.CPUPercent)
	}
}

// Process lists are dropped server-side for history anyway; keeping them would
// inflate the buffer (and every backfill POST) by an order of magnitude.
func TestBackfillDropsProcessNames(t *testing.T) {
	tg := &serverTarget{}
	tg.spoolBackfill(shared.BackfillSample{
		Ts:      1_800_000_000,
		Metrics: shared.Metrics{ProcessNames: []string{"a", "b", "c"}},
	})
	tg.backfillMu.Lock()
	defer tg.backfillMu.Unlock()
	if len(tg.backfill) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(tg.backfill))
	}
	if tg.backfill[0].Metrics.ProcessNames != nil {
		t.Fatal("process names must be stripped before buffering")
	}
}
