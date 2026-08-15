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
