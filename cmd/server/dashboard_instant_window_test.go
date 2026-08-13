package main

import (
	"testing"
	"time"
)

// Instant panels (gauge / pie / bar / histogram / radar / sankey / stat) used to
// ignore the dashboard time picker entirely: $__range was hardcoded to 3600s and
// the query always evaluated at wall-clock now. Picking 1h vs 14d produced an
// identical chart — the reported "点击时间跨度没有实际生效".
func TestInstantQueryWindowExpandsRangeVar(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name      string
		from, to  int64
		wantRange int64
	}{
		{"1h", now - 3600, now, 3600},
		{"6h", now - 6*3600, now, 6 * 3600},
		{"7d", now - 7*24*3600, now, 7 * 24 * 3600},
		{"14d", now - 14*24*3600, now, 14 * 24 * 3600},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, step, rangeSec := instantQueryWindow(c.from, c.to)
			if rangeSec != c.wantRange {
				t.Errorf("$__range = %ds, want %ds", rangeSec, c.wantRange)
			}
			if step < 5 || step > 300 {
				t.Errorf("$__interval %ds outside [5,300]", step)
			}
		})
	}
}

// A window ending now must stay a live query (evalAt 0) so dashboards keep
// showing the freshest sample; only a genuinely historical window pins the
// evaluation instant to the end of that window.
func TestInstantQueryWindowEvalInstant(t *testing.T) {
	now := time.Now().Unix()

	if at, _, _ := instantQueryWindow(now-3600, now); at != 0 {
		t.Errorf("live window should evaluate at now, got at=%d", at)
	}
	// Frontend aligns `to` down onto the step grid; that must not read as history.
	if at, _, _ := instantQueryWindow(now-3600-45, now-45); at != 0 {
		t.Errorf("step-aligned live window should evaluate at now, got at=%d", at)
	}
	past := now - 7*24*3600
	if at, _, _ := instantQueryWindow(past-3600, past); at != past {
		t.Errorf("historical window should evaluate at its end %d, got %d", past, at)
	}
}

// No window from the client keeps the previous 1h / 60s behaviour.
func TestInstantQueryWindowDefaults(t *testing.T) {
	at, step, rangeSec := instantQueryWindow(0, 0)
	if at != 0 || step != 60 || rangeSec != 3600 {
		t.Errorf("defaults = (%d,%d,%d), want (0,60,3600)", at, step, rangeSec)
	}
	// Inverted / nonsense windows fall back rather than producing a negative range.
	if _, _, r := instantQueryWindow(100, 50); r != 3600 {
		t.Errorf("inverted window range = %d, want 3600", r)
	}
}

// substituteVars is what actually renders $__range into the PromQL sent upstream.
func TestSubstituteVarsUsesSelectedRange(t *testing.T) {
	expr := "topk(5, avg_over_time(aiops_cpu_percent[$__range]))"
	_, step, rangeSec := instantQueryWindow(time.Now().Unix()-24*3600, time.Now().Unix())
	got := substituteVars(expr, nil, step, rangeSec)
	if want := "24h"; got != "topk(5, avg_over_time(aiops_cpu_percent["+want+"]))" {
		t.Errorf("substituted expr = %q, want $__range → %s", got, want)
	}
}

// Instant query used to call validatePanelQueryReq(..., withRange=false), so a
// from=1 / to=now body could expand $__range across decades and DoS VictoriaMetrics.
func TestInstantQueryReqRejectsUnboundedRange(t *testing.T) {
	now := time.Now().Unix()
	req := panelQueryReq{Expr: `avg_over_time(aiops_cpu_percent[$__range])`, From: 1, To: now}
	if err := validatePanelQueryReq(&req, true, false); err == nil {
		t.Fatal("expected rejection of multi-year instant $__range window")
	}
	req = panelQueryReq{Expr: `avg_over_time(aiops_cpu_percent[$__range])`, From: now - 7*24*3600, To: now}
	if err := validatePanelQueryReq(&req, true, false); err != nil {
		t.Fatalf("7d instant window should be accepted: %v", err)
	}
}
