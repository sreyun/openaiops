package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestVMQueryBodySurvivesHelperReturn pins the root cause of「主机曲线重启后只剩
// 重启之后的数据」.
//
// doVMWithBreaker 原本是 `ctx, cancel := context.WithTimeout(...); defer cancel()`，
// 然后把**尚未读取正文**的 *http.Response 返回给调用方。http.Client.Do 只等到响应头，
// 正文是流式的——于是函数一返回，context 就被取消，调用方再读正文就拿到
// "context canceled"。
//
// 小响应侥幸能过（正文已在传输层缓冲里），大响应必然失败。主机曲线那条 query_range 要
// 拉约 50 个指标的整段 matrix，属于后者；而写入路径只看状态码，所以写入一直"正常"。
// 净结果：VictoriaMetrics 健康、写入成功、count() 查得到，唯独曲线永远读不到 →
// 静默退回内存环，而内存环有 30 天，直到重启才暴露。
//
// 这个测试用「先发响应头、隔一会儿再发正文」的假 VM 复现那条时序。
func TestVMQueryBodySurvivesHelperReturn(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "matrix",
			"result": []map[string]any{{
				"metric": map[string]string{"__name__": "aiops_cpu_percent", "host": "h-1"},
				"values": [][]any{{float64(time.Now().Add(-time.Hour).Unix()), "42.5"}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// 先把响应头刷出去，让 Do() 立刻返回，helper 随之返回——正文稍后才来。
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	cfg, err := NewConfigStore(filepath.Join(t.TempDir(), "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.mu.Lock()
	cfg.cfg.VM = VMConfig{Enabled: true, URL: srv.URL}
	cfg.mu.Unlock()

	v := newVMWriter(cfg)
	to := time.Now().Unix()
	series, ok := v.vmQueryRangeSeries(`{__name__=~"aiops_cpu_percent",host="h-1"}`, to-3600, to, 60)
	if !ok {
		t.Fatal("query failed — the response body was cut off before the caller could read it " +
			"(context cancelled when the helper returned)")
	}
	if len(series) != 1 || len(series[0].Points) != 1 {
		t.Fatalf("want 1 series with 1 point, got %#v", series)
	}
	if got := series[0].Points[0][1]; got != 42.5 {
		t.Fatalf("value = %v, want 42.5", got)
	}
	if _, msg := v.diag.lastReadErrSince(0); strings.Contains(msg, "context canceled") {
		t.Fatalf("read still reports a cancelled context: %s", msg)
	}
}
