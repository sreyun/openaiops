package main

import (
	"strings"
	"testing"
	"time"

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

func TestVmNamesForMetricKeysSlim(t *testing.T) {
	if got := vmNamesForMetricKeys(nil); got != nil {
		t.Fatalf("empty keys should mean full allowlist (nil): %v", got)
	}
	got := vmNamesForMetricKeys([]string{"cpu", "memory"})
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "aiops_cpu_percent") || !strings.Contains(joined, "aiops_mem_percent") {
		t.Fatalf("missing core gauges: %v", got)
	}
	if strings.Contains(joined, "gpu_util") || strings.Contains(joined, "netflow") {
		t.Fatalf("slim cpu/mem query must not pull GPU/netflow: %v", got)
	}
	expr := hostHistoryRangeExprNames("h1", got)
	if strings.Contains(expr, "aiops_.*") {
		t.Fatal("subset expr still unbounded")
	}
	if !strings.Contains(expr, `host="h1"`) || !strings.Contains(expr, "path!~") {
		t.Fatalf("subset expr missing host/path filter: %s", expr)
	}
}

func TestForecastFitLookback(t *testing.T) {
	if got := forecastFitLookback(300); got != 3600 {
		t.Fatalf("short window lookback=%d want 1h", got)
	}
	if got := forecastFitLookback(2 * 3600); got != 6*3600 {
		t.Fatalf("2h window lookback=%d want 6h", got)
	}
	if got := forecastFitLookback(10 * 24 * 3600); got != 7*24*3600 {
		t.Fatalf("cap lookback=%d want 7d", got)
	}
}

func TestPrependHistoryPoints(t *testing.T) {
	pts := [][2]float64{{200, 50}, {300, 60}}
	extra := []shared.Sample{
		{Timestamp: 100, Metrics: shared.Metrics{CPUPercent: 10}},
		{Timestamp: 150, Metrics: shared.Metrics{CPUPercent: 20}},
		{Timestamp: 250, Metrics: shared.Metrics{CPUPercent: 99}}, // inside visible window — skip
	}
	got := prependHistoryPoints(pts, extra, "cpu_percent")
	if len(got) != 4 || got[0][0] != 100 || got[0][1] != 10 || got[2][0] != 200 {
		t.Fatalf("%v", got)
	}
}

func TestLoadDurableHostHistoryFallsBackToRAM(t *testing.T) {
	st := NewStore()
	st.RegisterHost("h1", "node-1", "fp-aaa")
	rep := newTestReport("h1", "node-1", "fp-aaa", 42)
	if _, ok := st.UpsertAuthenticated(rep, "fp-aaa"); !ok {
		t.Fatal("upsert failed")
	}
	srv := &Server{store: st}
	now := time.Now().Unix()
	samples, ok := srv.loadDurableHostHistory("h1", now-3600, now, vmNamesForMetricKeys([]string{"cpu"}))
	if !ok {
		t.Fatal("host should exist")
	}
	if len(samples) == 0 {
		t.Fatal("expected RAM fallback samples")
	}
	if _, ok := srv.loadDurableHostHistory("ghost", now-3600, now, nil); ok {
		t.Fatal("missing host")
	}
}

func TestFormatHostTrendLine(t *testing.T) {
	samples := []shared.Sample{
		{Timestamp: 1, Metrics: shared.Metrics{CPUPercent: 10, MemPercent: 20, DiskPercent: 30, Load1: 1}},
		{Timestamp: 2, Metrics: shared.Metrics{CPUPercent: 20, MemPercent: 30, DiskPercent: 40, Load1: 2}},
		{Timestamp: 3, Metrics: shared.Metrics{CPUPercent: 30, MemPercent: 40, DiskPercent: 50, Load1: 3}},
	}
	got := formatHostTrendLine(samples, 6)
	if !strings.Contains(got, "近6h趋势") || !strings.Contains(got, "CPU") || !strings.Contains(got, "3点") {
		t.Fatalf("%s", got)
	}
	if formatHostTrendLine(nil, 6) != "" {
		t.Fatal("empty samples")
	}
}
