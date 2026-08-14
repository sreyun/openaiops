package main

import (
	"strings"
	"testing"

	"aiops-monitor/shared"
)

func TestHostHistoryRangeExprIsBounded(t *testing.T) {
	if n := len(hostHistoryMetricNames); n < 50 {
		t.Fatalf("host history allowlist is %d names, need ≥50", n)
	}
	seen := map[string]bool{}
	for _, name := range hostHistoryMetricNames {
		if seen[name] {
			t.Fatalf("duplicate metric name %q", name)
		}
		seen[name] = true
	}
	expr := hostHistoryRangeExpr("h1")
	if strings.Contains(expr, "aiops_.*") {
		t.Fatalf("unbounded aiops_.* selector times out on overlay/PVC cardinality: %s", expr)
	}
	for _, need := range []string{"cpu_percent", "gpu_util_percent", "disk_vol_percent", "net_conn_count", `host="h1"`, "path!~"} {
		if !strings.Contains(expr, need) {
			t.Fatalf("expr missing %q: %s", need, expr)
		}
	}
}

func TestSpliceHistoryPrefersRecentMemory(t *testing.T) {
	base := []shared.Sample{
		{Timestamp: 100, Metrics: shared.Metrics{CPUPercent: 1}},
		{Timestamp: 200, Metrics: shared.Metrics{CPUPercent: 2}},
		{Timestamp: 300, Metrics: shared.Metrics{CPUPercent: 3}},
	}
	recent := []shared.Sample{
		{Timestamp: 250, Metrics: shared.Metrics{CPUPercent: 9, DiskPercent: 40}},
		{Timestamp: 300, Metrics: shared.Metrics{CPUPercent: 8, DiskPercent: 41}},
	}
	out := spliceHistory(base, recent)
	if len(out) != 4 {
		t.Fatalf("len=%d want 4: %+v", len(out), out)
	}
	if out[0].Timestamp != 100 || out[1].Timestamp != 200 {
		t.Fatalf("kept prefix: %+v", out)
	}
	if out[2].CPUPercent != 9 || out[3].CPUPercent != 8 {
		t.Fatalf("memory tail should win: %+v", out[2:])
	}
}

func TestSpliceHistoryEmptySides(t *testing.T) {
	one := []shared.Sample{{Timestamp: 1, Metrics: shared.Metrics{CPUPercent: 4}}}
	if got := spliceHistory(nil, one); len(got) != 1 || got[0].CPUPercent != 4 {
		t.Fatalf("nil base: %+v", got)
	}
	if got := spliceHistory(one, nil); len(got) != 1 || got[0].CPUPercent != 4 {
		t.Fatalf("nil overlay: %+v", got)
	}
}

func TestRecentHistoryTail(t *testing.T) {
	in := []shared.Sample{
		{Timestamp: 1000, Metrics: shared.Metrics{CPUPercent: 1}},
		{Timestamp: 1500, Metrics: shared.Metrics{CPUPercent: 2}},
		{Timestamp: 2000, Metrics: shared.Metrics{CPUPercent: 3}},
	}
	got := recentHistoryTail(in, 600)
	if len(got) != 2 || got[0].Timestamp != 1500 || got[1].CPUPercent != 3 {
		t.Fatalf("tail: %+v", got)
	}
	if got := recentHistoryTail(nil, 60); got != nil {
		t.Fatalf("empty: %+v", got)
	}
}

func TestQueryHistoryExportFallbackWindow(t *testing.T) {
	if !queryHistoryAllowsExportFallback(6 * 3600) {
		t.Fatal("6h must fall back to /export when query_range fails")
	}
	if !queryHistoryAllowsExportFallback(24 * 3600) {
		t.Fatal("24h must fall back to /export when query_range fails")
	}
	if queryHistoryAllowsExportFallback(14 * 24 * 3600) {
		t.Fatal("14d export is unbounded; rely on bounded query_range")
	}
}
