package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ============================================================================
// MCP Server —— 把本平台的【只读】运维工具暴露为标准 Model Context Protocol，供外部 Agent
// （Claude Desktop、Cursor 等）连接调用。
//
// 传输：
//   - POST /api/v1/mcp  JSON-RPC（application/json 或 text/event-stream，Streamable HTTP）
//   - GET  /api/v1/mcp  SSE 长连接（兼容探测 / 服务端推送保活）
//
// 鉴权：Bearer Token；支持主令牌与 scoped token。默认关闭。
// ============================================================================

var mcpReadonlyTools = map[string]bool{
	"query_metrics": true, "search_logs": true, "list_alerts": true,
	"search_similar_cases": true, "search_knowledge": true, "list_datasources": true, "query_datasource": true,
	"list_recent_changes": true, "check_host_health": true, "list_hosts": true,
	"query_hardware": true, "query_hardware_events": true, "query_hardware_history": true,
	"query_hardware_changes": true, "query_netflow": true, "query_hyperv": true,
	"query_snmp": true, "query_interface_traffic": true, "query_traps": true,
	"query_netflow_flows": true,
	"query_containers": true, "query_k8s": true, "locate_resource": true,
	"render_chart": true, "query_metric_range": true, "query_promql_range": true,
	"show_instant_stat": true, "analyze_metric_trend": true, "forecast_metric": true,
	"list_dashboards": true, "get_dashboard": true,
	"list_dashboard_panels": true, "query_dashboard_panel": true,
	"list_ui_views": true, "navigate_ui": true,
	"query_security_posture": true,
	// SRE / AI 只读研判（无写操作）
	"get_duty_context": true, "diagnose_incident": true, "run_assist_task": true,
	"run_diagnostic": true, "analyze_dashboard": true,
}

type jsonRPCReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func rawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

func mcpTokenFingerprint(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:8])
}

func mcpNewSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func mcpWantsSSE(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/event-stream")
}

func writeMCPSSEData(w http.ResponseWriter, flusher http.Flusher, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", raw)
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.AIConfig()
	if !cfg.MCPEnabled || (strings.TrimSpace(cfg.MCPToken) == "" && strings.TrimSpace(cfg.MCPScopedTokensJSON) == "") {
		http.Error(w, "MCP server disabled", http.StatusNotFound)
		return
	}
	tok := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	ok, scopes, tokName := resolveMCPAuth(cfg, tok)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleMCPGetSSE(w, r, scopes, tokName)
		return
	case http.MethodDelete:
		// Streamable HTTP：客户端结束会话；本服务无服务端会话态，直接 200。
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodPost:
		// continue
	default:
		http.Error(w, "method not allowed (use GET/POST/DELETE)", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	limit := cfg.MCPRateLimitPerMin
	if limit <= 0 {
		limit = 60
	}
	if s.aiGov != nil {
		if ok, used, lim := s.aiGov.checkAndIncrMCPRate(mcpTokenFingerprint(tok)+":"+tokName, limit); !ok {
			w.Header().Set("Retry-After", "60")
			s.autoDefendOnMCPAbuse(tokName)
			http.Error(w, "MCP rate limit exceeded ("+itoa(used)+"/"+itoa(lim)+" per min)", http.StatusTooManyRequests)
			return
		}
	}
	var req jsonRPCReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeMCPError(w, r, nil, -32700, "parse error")
		return
	}
	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if sessionID == "" && req.Method == "initialize" {
		sessionID = mcpNewSessionID()
	}
	if sessionID != "" {
		w.Header().Set("Mcp-Session-Id", sessionID)
	}

	switch req.Method {
	case "initialize":
		protocol := "2025-06-18"
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion != "" {
			protocol = p.ProtocolVersion
		}
		s.writeMCPResult(w, r, req.ID, map[string]any{
			"protocolVersion": protocol,
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"subscribe": false, "listChanged": false},
				"prompts":   map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name": "aiops-monitor", "version": appVersion,
				"token": tokName, "scopes": scopes,
			},
			"instructions": "AIOps 只读运维 MCP：指标/日志/告警/硬件/流量/K8s/看板/值班与诊断研判。禁止写操作。",
		})
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		s.writeMCPResult(w, r, req.ID, map[string]any{})
	case "tools/list":
		s.writeMCPResult(w, r, req.ID, map[string]any{"tools": s.mcpToolList(scopes)})
	case "tools/call":
		s.mcpToolCall(w, r, req, scopes, tokName)
	case "resources/list":
		s.writeMCPResult(w, r, req.ID, map[string]any{"resources": s.mcpResourceList()})
	case "resources/templates/list":
		s.writeMCPResult(w, r, req.ID, map[string]any{"resourceTemplates": []any{}})
	case "resources/read":
		s.mcpResourceRead(w, r, req)
	case "prompts/list":
		s.writeMCPResult(w, r, req.ID, map[string]any{"prompts": mcpPromptList()})
	case "prompts/get":
		s.mcpPromptGet(w, r, req)
	default:
		s.writeMCPError(w, r, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (s *Server) handleMCPGetSSE(w http.ResponseWriter, r *http.Request, scopes []string, tokName string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if sessionID == "" {
		sessionID = mcpNewSessionID()
	}
	w.Header().Set("Mcp-Session-Id", sessionID)
	w.WriteHeader(http.StatusOK)

	// 兼容部分客户端：声明本端点即消息通道（Streamable HTTP 同源）。
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", r.URL.Path)
	flusher.Flush()
	_ = scopes
	_ = tokName

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping %d\n\n", time.Now().Unix())
			flusher.Flush()
		}
	}
}

func (s *Server) writeMCPResult(w http.ResponseWriter, r *http.Request, id json.RawMessage, result any) {
	payload := map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "result": result}
	if mcpWantsSSE(r) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		writeMCPSSEData(w, flusher, payload)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) writeMCPError(w http.ResponseWriter, r *http.Request, id json.RawMessage, code int, msg string) {
	payload := map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "error": map[string]any{"code": code, "message": msg}}
	if mcpWantsSSE(r) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		writeMCPSSEData(w, flusher, payload)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) mcpToolList(scopes []string) []map[string]any {
	out := []map[string]any{}
	if s.sreyun == nil {
		return out
	}
	// 走已发布快照：外部 MCP 工具可能正被 AI 设置保存流程重新注册，直接 range 裸 map 会
	// 触发 Go 运行时的 concurrent map iteration and map write 致命错误。
	snap := s.sreyun.snapshot()
	for _, name := range snap.names {
		t := snap.byName[name]
		if !mcpToolAllowedByScopes(name, scopes) {
			continue
		}
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{"name": name, "description": t.Description, "inputSchema": schema})
	}
	return out
}

func (s *Server) mcpToolCall(w http.ResponseWriter, r *http.Request, req jsonRPCReq, scopes []string, tokName string) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeMCPError(w, r, req.ID, -32602, "invalid params")
		return
	}
	if !mcpToolAllowedByScopes(p.Name, scopes) || s.sreyun == nil {
		s.writeMCPError(w, r, req.ID, -32602, "unknown, not-exposed, or out-of-scope tool: "+p.Name)
		return
	}
	tool, ok := s.sreyun.lookupTool(p.Name)
	if !ok {
		s.writeMCPError(w, r, req.ID, -32602, "unknown tool: "+p.Name)
		return
	}
	if s.aiGov != nil {
		s.aiGov.recordTool(aiToolAuditEntry{
			Actor: "mcp:" + tokName, Tool: p.Name, Action: "tools/call", Approved: true,
			Detail: "scopes=" + strings.Join(scopes, ","),
		})
	}
	result, err := tool.Execute(p.Arguments)
	if err != nil {
		s.writeMCPResult(w, r, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "工具执行失败：" + err.Error()}},
			"isError": true,
		})
		return
	}
	// 尽量附带 structuredContent，便于 Agent 解析 JSON 结果
	out := map[string]any{
		"content": []map[string]any{{"type": "text", "text": result}},
	}
	var asJSON any
	if json.Unmarshal([]byte(result), &asJSON) == nil {
		out["structuredContent"] = asJSON
	}
	s.writeMCPResult(w, r, req.ID, out)
}

func (s *Server) mcpResourceList() []map[string]any {
	return []map[string]any{
		{"uri": "aiops://overview", "name": "平台总览", "description": "在线主机/告警/事件摘要", "mimeType": "application/json"},
		{"uri": "aiops://duty", "name": "值班态势", "description": "值班晨报上下文", "mimeType": "application/json"},
	}
}

func (s *Server) mcpResourceRead(w http.ResponseWriter, r *http.Request, req jsonRPCReq) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.URI) == "" {
		s.writeMCPError(w, r, req.ID, -32602, "invalid params: uri required")
		return
	}
	text := ""
	switch strings.TrimSpace(p.URI) {
	case "aiops://overview":
		if s.sreyun != nil {
			if t, ok := s.sreyun.lookupTool("list_hosts"); ok {
				text, _ = t.Execute(map[string]any{"limit": 50})
			}
		}
		if text == "" {
			text = `{"hint":"call list_hosts / list_alerts via tools"}`
		}
	case "aiops://duty":
		if s.sreyun != nil {
			if t, ok := s.sreyun.lookupTool("get_duty_context"); ok {
				text, _ = t.Execute(map[string]any{})
			}
		}
		if text == "" {
			text = `{"hint":"get_duty_context unavailable"}`
		}
	default:
		s.writeMCPError(w, r, req.ID, -32002, "resource not found: "+p.URI)
		return
	}
	s.writeMCPResult(w, r, req.ID, map[string]any{
		"contents": []map[string]any{{
			"uri": p.URI, "mimeType": "application/json", "text": text,
		}},
	})
}

func mcpPromptList() []map[string]any {
	return []map[string]any{
		{"name": "duty_brief", "description": "生成值班晨报：风险优先级、待办与建议处置", "arguments": []map[string]any{}},
		{"name": "alert_triage", "description": "告警分诊：按主机/级别汇总并给出处置顺序", "arguments": []map[string]any{
			{"name": "host_id", "description": "可选主机 ID", "required": false},
		}},
		{"name": "k8s_investigate", "description": "K8s 排查：定位异常 Pod/资源并给出只读诊断步骤", "arguments": []map[string]any{
			{"name": "cluster_id", "description": "集群 ID", "required": false},
		}},
	}
}

func (s *Server) mcpPromptGet(w http.ResponseWriter, r *http.Request, req jsonRPCReq) {
	var p struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.Name) == "" {
		s.writeMCPError(w, r, req.ID, -32602, "invalid params: name required")
		return
	}
	var text string
	switch p.Name {
	case "duty_brief":
		text = "请调用 get_duty_context 拉取值班态势，再输出简洁晨报：风险优先级、待办、建议处置。"
	case "alert_triage":
		text = "请调用 list_alerts（必要时 check_host_health / query_metrics），按严重级别与主机汇总，给出处置顺序。"
		if hid := strings.TrimSpace(p.Arguments["host_id"]); hid != "" {
			text += " 重点主机 host_id=" + hid + "。"
		}
	case "k8s_investigate":
		text = "请用 query_k8s / locate_resource 只读排查集群异常，给出根因假设与下一步只读检查；禁止扩缩容或重启。"
		if cid := strings.TrimSpace(p.Arguments["cluster_id"]); cid != "" {
			text += " 集群 cluster_id=" + cid + "。"
		}
	default:
		s.writeMCPError(w, r, req.ID, -32602, "unknown prompt: "+p.Name)
		return
	}
	s.writeMCPResult(w, r, req.ID, map[string]any{
		"description": p.Name,
		"messages": []map[string]any{
			{"role": "user", "content": map[string]any{"type": "text", "text": text}},
		},
	})
}
