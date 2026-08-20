package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newMCPTestServer(t *testing.T, ai AIConfig) *Server {
	t.Helper()
	cfg := newTestConfigStore(t)
	if err := cfg.SetAIConfig(ai); err != nil {
		t.Fatalf("SetAIConfig: %v", err)
	}
	s := &Server{cfg: cfg, aiGov: newAIGovHub()}
	s.sreyun = newSreyunCore(s)
	return s
}

func mcpPOST(t *testing.T, s *Server, token, body, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rr := httptest.NewRecorder()
	s.handleMCP(rr, req)
	return rr
}

func TestHandleMCPInitializeAndToolsList(t *testing.T) {
	s := newMCPTestServer(t, AIConfig{
		MCPEnabled: true,
		MCPToken:   "mcp-primary-token-32chars!!!!",
	})
	rr := mcpPOST(t, s, "mcp-primary-token-32chars!!!!",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`,
		"application/json")
	if rr.Code != 200 {
		t.Fatalf("initialize status=%d body=%s", rr.Code, rr.Body.String())
	}
	if sid := rr.Header().Get("Mcp-Session-Id"); sid == "" {
		t.Fatal("missing Mcp-Session-Id on initialize")
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil || env.Error != nil {
		t.Fatalf("initialize decode: %v err=%v body=%s", err, env.Error, rr.Body.String())
	}
	if env.Result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocol=%v", env.Result["protocolVersion"])
	}

	rr = mcpPOST(t, s, "mcp-primary-token-32chars!!!!",
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "")
	if rr.Code != 200 {
		t.Fatalf("tools/list status=%d", rr.Code)
	}
	var list struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Result.Tools {
		if n, _ := tool["name"].(string); n != "" {
			names[n] = true
		}
	}
	for _, need := range []string{"query_metrics", "get_duty_context", "diagnose_incident", "run_assist_task", "analyze_dashboard", "list_hosts", "query_containers"} {
		if !names[need] {
			t.Fatalf("tools/list missing %s (got %d tools)", need, len(names))
		}
	}
	if names["propose_skill"] || names["remember_preference"] {
		t.Fatal("write preference tools must not appear in MCP tools/list")
	}
}

func TestHandleMCPUnauthorizedAndDisabled(t *testing.T) {
	s := newMCPTestServer(t, AIConfig{MCPEnabled: false, MCPToken: "x"})
	rr := mcpPOST(t, s, "x", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled want 404 got %d", rr.Code)
	}
	s = newMCPTestServer(t, AIConfig{MCPEnabled: true, MCPToken: "secret-token"})
	rr = mcpPOST(t, s, "wrong", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad token want 401 got %d", rr.Code)
	}
}

func TestHandleMCPScopedToolCallDenied(t *testing.T) {
	s := newMCPTestServer(t, AIConfig{
		MCPEnabled:          true,
		MCPToken:            "primary-token",
		MCPScopedTokensJSON: `[{"name":"logs","token":"logs-only-token","scopes":["logs"]}]`,
	})
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query_metrics","arguments":{}}}`
	rr := mcpPOST(t, s, "logs-only-token", body, "")
	if rr.Code != 200 {
		t.Fatalf("status=%d", rr.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	errObj, _ := env["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("expected RPC error for out-of-scope tool, body=%s", rr.Body.String())
	}
}

func TestHandleMCPAcceptSSE(t *testing.T) {
	s := newMCPTestServer(t, AIConfig{MCPEnabled: true, MCPToken: "sse-token-xxxxxxxx"})
	rr := mcpPOST(t, s, "sse-token-xxxxxxxx",
		`{"jsonrpc":"2.0","id":9,"method":"ping"}`,
		"application/json, text/event-stream")
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("want SSE content-type, got %q body=%s", ct, rr.Body.String())
	}
	raw := rr.Body.String()
	if !strings.Contains(raw, "event: message") || !strings.Contains(raw, `"result"`) {
		t.Fatalf("SSE payload unexpected: %s", raw)
	}
}

func TestHandleMCPGetSSEHeaders(t *testing.T) {
	s := newMCPTestServer(t, AIConfig{MCPEnabled: true, MCPToken: "get-sse-token"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/mcp", nil)
	req.Header.Set("Authorization", "Bearer get-sse-token")
	req.Header.Set("Accept", "text/event-stream")
	rr := httptest.NewRecorder()
	// SSE 处理器会一直写到 ctx 取消，所以它必须跑在另一个 goroutine 里；而这个测试要
	// 一边等 "event: endpoint" 出现一边取消。httptest.ResponseRecorder **没有任何加锁**，
	// 直接边写边读 rr.Body 是数据竞争（-race 下必然失败）。用带锁的包装把两侧隔开：
	// 生产路径没有这个问题——真实的 ResponseWriter 只被请求自己的 goroutine 写。
	lw := &lockedRecorder{rec: rr}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleMCP(lw, req)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(lw.body(), "event: endpoint") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GET SSE handler did not exit after cancel")
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("want SSE content-type, got %q", rr.Header().Get("Content-Type"))
	}
	if rr.Header().Get("Mcp-Session-Id") == "" {
		t.Fatal("missing Mcp-Session-Id")
	}
	if !strings.Contains(lw.body(), "event: endpoint") {
		t.Fatalf("missing endpoint event: %s", lw.body())
	}
}

// lockedRecorder serialises writes from the handler goroutine against reads from
// the test goroutine. Only needed for the streaming (SSE) handlers.
type lockedRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func (l *lockedRecorder) Header() http.Header { return l.rec.Header() }

func (l *lockedRecorder) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Write(b)
}

func (l *lockedRecorder) WriteHeader(code int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.WriteHeader(code)
}

func (l *lockedRecorder) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.Flush()
}

func (l *lockedRecorder) body() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Body.String()
}

func TestHandleMCPPromptsAndResources(t *testing.T) {
	s := newMCPTestServer(t, AIConfig{MCPEnabled: true, MCPToken: "res-token"})
	rr := mcpPOST(t, s, "res-token", `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`, "")
	var prompts struct {
		Result struct {
			Prompts []map[string]any `json:"prompts"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &prompts); err != nil || len(prompts.Result.Prompts) < 1 {
		t.Fatalf("prompts/list: %v body=%s", err, rr.Body.String())
	}
	rr = mcpPOST(t, s, "res-token", `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`, "")
	var res struct {
		Result struct {
			Resources []map[string]any `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil || len(res.Result.Resources) < 1 {
		t.Fatalf("resources/list: %v body=%s", err, rr.Body.String())
	}
}
