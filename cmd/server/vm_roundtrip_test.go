package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// TestVMHostHistoryRoundTrip is the answer to "数据到底有没有落到 VM，读的时候到底
// 走没走 VM" — asked with a real HTTP server on both ends instead of by reading code.
//
// 它同时钉住三件事：
//  1. Agent 上报进来的样本，确实以 Prometheus 文本格式 POST 到了 VM 的
//     /api/v1/import/prometheus，且带着 host 标签和毫秒时间戳；
//  2. 主机曲线接口读的确实是 VM 的 /api/v1/query_range，不是内存；
//  3. VM 有数据时 provenance 是 vm+ram，VM 空/挂时才是 ram-fallback。
//
// 第 3 条是这一整类问题的判据：现象「重启后只剩重启之后的数据」等价于读到了
// ram-fallback —— 内存里的 5 分钟环有 30 天，进程活着时会把 VM 为空这件事完全掩盖。
func TestVMHostHistoryRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var imported []string
	var queried []string
	// The fake VM replays whatever the writer imported, as a query_range matrix.
	var replayValue float64 = 42.5
	replayTs := time.Now().Add(-2 * time.Hour).Unix()

	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/import/prometheus"):
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			imported = append(imported, string(b))
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/api/v1/query_range"):
			mu.Lock()
			queried = append(queried, r.URL.Query().Get("query"))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "matrix",
					"result": []map[string]any{{
						"metric": map[string]string{"__name__": "aiops_cpu_percent", "host": "h-1"},
						"values": [][]any{{float64(replayTs), "42.5"}},
					}},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer vm.Close()

	cfg, err := NewConfigStore(filepath.Join(t.TempDir(), "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.mu.Lock()
	cfg.cfg.VM = VMConfig{Enabled: true, URL: vm.URL}
	cfg.mu.Unlock()

	w := newVMWriter(cfg)
	if !w.enabled() {
		t.Fatal("VM writer must be enabled when VMConfig carries Enabled+URL")
	}

	// ---- write half -------------------------------------------------------
	go w.run()
	defer w.shutdown(3 * time.Second)
	sampleTs := time.Now().Add(-90 * time.Minute).Unix()
	w.enqueue("h-1", "web-01", "prod", sampleTs, shared.Metrics{CPUPercent: replayValue, MemPercent: 61})

	deadline := time.Now().Add(20 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		mu.Lock()
		if len(imported) > 0 {
			body = strings.Join(imported, "\n")
		}
		mu.Unlock()
		if body != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if body == "" {
		t.Fatal("nothing reached VictoriaMetrics: the enqueue → pump → import path never wrote a sample")
	}
	if !strings.Contains(body, `aiops_cpu_percent{host="h-1"`) {
		t.Fatalf("import payload lacks the host-labelled CPU series:\n%s", vmFirstLines(body, 5))
	}
	if !strings.Contains(body, "42.5") {
		t.Fatalf("import payload lost the value:\n%s", vmFirstLines(body, 5))
	}
	// Prometheus text wants MILLISECOND timestamps; seconds would land the point
	// in 1970 and the chart would silently never show it.
	if !strings.Contains(body, itoa64(sampleTs*1000)) {
		t.Fatalf("import payload must carry ms timestamps, want %d:\n%s", sampleTs*1000, vmFirstLines(body, 5))
	}

	// ---- read half --------------------------------------------------------
	srv := &Server{store: NewStore(), cfg: cfg, vm: w}
	srv.store.RegisterHost("h-1", "web-01", "fp-11111111111111111111111111111111")

	from, to := replayTs-3600, time.Now().Unix()
	out, source, ok := srv.loadDurableHostHistorySource("h-1", from, to, []string{"aiops_cpu_percent"})
	if !ok {
		t.Fatal("host history must resolve for a known host")
	}
	if source != historySourceVM {
		t.Fatalf("source = %q, want %q — the chart is not reading VictoriaMetrics", source, historySourceVM)
	}
	mu.Lock()
	q := strings.Join(queried, " | ")
	mu.Unlock()
	if !strings.Contains(q, `host="h-1"`) {
		t.Fatalf("query_range was not filtered by host: %s", q)
	}
	found := false
	for _, s := range out {
		if s.CPUPercent == replayValue {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("VM sample never made it back into the chart payload (%d samples)", len(out))
	}
}

// When VM answers nothing, the read must degrade to RAM *and say so* — that label
// is the only way an operator can tell "this host has no data" from "the durable
// store is empty and everything you see will vanish on restart".
func TestVMEmptyDegradesToRamFallbackWithProvenance(t *testing.T) {
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "matrix", "result": []any{}},
		})
	}))
	defer vm.Close()

	cfg, err := NewConfigStore(filepath.Join(t.TempDir(), "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.mu.Lock()
	cfg.cfg.VM = VMConfig{Enabled: true, URL: vm.URL}
	cfg.mu.Unlock()

	srv := &Server{store: NewStore(), cfg: cfg, vm: newVMWriter(cfg)}
	srv.store.RegisterHost("h-1", "web-01", "fp-11111111111111111111111111111111")

	_, source, ok := srv.loadDurableHostHistorySource("h-1", time.Now().Unix()-86400, time.Now().Unix(), nil)
	if !ok {
		t.Fatal("known host must resolve")
	}
	if source != historySourceFallback {
		t.Fatalf("source = %q, want %q", source, historySourceFallback)
	}
}

func vmFirstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}
