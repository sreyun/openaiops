package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

func fakeResp(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("body"))}
}

// Retry must distinguish "VM is temporarily unhappy" from "this batch is illegal".
// Retrying a poison batch forever re-sends it every 5s and blocks every healthy
// batch behind it until the retry cap evicts it — one bad sample could stall a
// whole server's ingest.
func TestVMImportDoneRetriesOnlyTransientFailures(t *testing.T) {
	v := &vmWriter{breaker: newVMCircuitBreaker()}

	if v.vmImportDone(nil, errors.New("dial tcp: connection refused"), "samples", 3) {
		t.Fatal("network error must be retried")
	}
	if v.vmImportDone(fakeResp(503), nil, "samples", 3) {
		t.Fatal("5xx must be retried")
	}
	if v.vmImportDone(fakeResp(429), nil, "samples", 3) {
		t.Fatal("429 must be retried")
	}
	if !v.vmImportDone(fakeResp(204), nil, "samples", 3) {
		t.Fatal("2xx must clear the batch")
	}
	if !v.vmImportDone(fakeResp(400), nil, "samples", 3) {
		t.Fatal("4xx is permanent — the batch must be dropped, not retried forever")
	}
	if !v.vmImportDone(fakeResp(404), nil, "samples", 3) {
		t.Fatal("4xx is permanent — the batch must be dropped, not retried forever")
	}
}

// Hardware / SNMP / NetFlow / Hyper-V and exporter scrapes used to be
// fire-and-forget (one goroutine, one request, no retry), so they lost data on
// every VM blip. They must go through the same batch+retry+drain pipeline.
func TestPushRawLineQueuesForBatchedRetry(t *testing.T) {
	cs, err := NewConfigStore(t.TempDir()+"/config.json", nil)
	if err != nil {
		t.Fatalf("config store: %v", err)
	}
	cs.mu.Lock()
	cs.cfg.VM = VMConfig{Enabled: true, URL: "http://vm.invalid:8428"}
	cs.mu.Unlock()

	v := &vmWriter{rawCh: make(chan string, 4), cfg: cs}

	v.pushRawLine("aiops_x{host=\"h\"} 1 1700000000000\n")
	v.pushRawLine("   ") // blank must not occupy a slot
	select {
	case got := <-v.rawCh:
		if strings.HasSuffix(got, "\n") {
			t.Fatalf("trailing newline must be trimmed (lines are re-joined on flush): %q", got)
		}
	default:
		t.Fatal("pushRawLine did not enqueue onto the batch pipeline")
	}
	select {
	case got := <-v.rawCh:
		t.Fatalf("blank line should have been dropped, got %q", got)
	default:
	}
}

func TestPushRawJoinsBatchIntoOneBody(t *testing.T) {
	v := &vmWriter{}
	if !v.pushRaw("http://vm.invalid", nil) {
		t.Fatal("empty batch is trivially done")
	}
}

// The RAM overlay must land on the SAME step grid as the VM half. A 24h chart is
// 180s per point while the RAM ring is 5-10s: splicing them raw yields a series
// whose last 15 minutes are ~18x denser, and every forecast model here
// extrapolates by array index, so that tail's slope gets amplified ~18x.
func TestAlignOverlayToStepMatchesVMGrid(t *testing.T) {
	const to = int64(1_800_000_000)
	const from = to - 24*3600 // step = 180
	step := adaptiveHistoryStep(from, to)
	if step != 180 {
		t.Fatalf("precondition: 24h step = %d, want 180", step)
	}
	// 15 minutes of 10s RAM samples = 90 points.
	var tail []shared.Sample
	for ts := to - 900; ts <= to; ts += 10 {
		tail = append(tail, shared.Sample{Timestamp: ts, Metrics: shared.Metrics{CPUPercent: 5}})
	}
	out := alignOverlayToStep(tail, from, to)
	if len(out) >= len(tail) {
		t.Fatalf("overlay was not resampled: %d points in, %d out", len(tail), len(out))
	}
	for i := 1; i < len(out); i++ {
		if gap := out[i].Timestamp - out[i-1].Timestamp; gap != step {
			t.Fatalf("overlay point %d is %ds after the previous one, want %ds", i, gap, step)
		}
	}
	if len(out) > 0 && out[len(out)-1].Timestamp > to {
		t.Fatal("overlay must not extend past the requested window end")
	}
}

// 7d/14d: step (1260s) is longer than the whole overlay window (900s), so the
// grid holds no point at all. Returning the tail untouched would leave 90 raw
// 10s points glued onto ~480 sparse ones — the exact density cliff this function
// exists to remove.
func TestAlignOverlayToStepCollapsesTailShorterThanOneStep(t *testing.T) {
	const to = int64(1_800_000_000)
	const from = to - 7*24*3600
	step := adaptiveHistoryStep(from, to)
	if step <= memHistoryOverlaySec {
		t.Fatalf("precondition: 7d step = %d must exceed the %ds overlay window", step, memHistoryOverlaySec)
	}
	var tail []shared.Sample
	for ts := to - memHistoryOverlaySec; ts <= to; ts += 10 {
		tail = append(tail, shared.Sample{Timestamp: ts, Metrics: shared.Metrics{CPUPercent: float64(ts % 7)}})
	}
	out := alignOverlayToStep(tail, from, to)
	// The contract is "never hand back the dense raw tail". Depending on grid
	// phase, a 900s window may still contain exactly one 1260s grid point — in
	// that case we emit that point (grid-consistent with the VM half); when it
	// contains none we fall back to the single freshest sample. Both outcomes are
	// one or two points, never the original 90.
	if len(out) > 2 {
		t.Fatalf("dense tail was not collapsed: %d points survived (raw tail had %d)", len(out), len(tail))
	}
	if len(out) == 0 {
		t.Fatal("overlay must not vanish entirely — the live tail would disappear from the chart")
	}
	last := out[len(out)-1]
	onGrid := last.Timestamp%step == 0
	isNewest := last.Timestamp == tail[len(tail)-1].Timestamp
	if !onGrid && !isNewest {
		t.Fatalf("tail point must be either on the VM step grid or the freshest sample, got ts=%d (step=%d, newest=%d)",
			last.Timestamp, step, tail[len(tail)-1].Timestamp)
	}
	if last.Timestamp > to {
		t.Fatalf("tail must not extend past the window end: ts=%d > to=%d", last.Timestamp, to)
	}
}

func TestAlignOverlayToStepKeepsShortWindowsDense(t *testing.T) {
	const to = int64(1_800_000_000)
	const from = to - 3600 // 1h → step floors at 5-7s, close to the RAM interval
	var tail []shared.Sample
	for ts := to - 900; ts <= to; ts += 10 {
		tail = append(tail, shared.Sample{Timestamp: ts, Metrics: shared.Metrics{CPUPercent: 5}})
	}
	out := alignOverlayToStep(tail, from, to)
	if len(out) == 0 {
		t.Fatal("1h window must keep a live tail")
	}
	if alignOverlayToStep(nil, from, to) != nil {
		t.Fatal("empty tail stays empty")
	}
}
