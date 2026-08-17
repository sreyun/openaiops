package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ============================================================================
// MCP Client —— 本平台作为 Client 连接外部 MCP Server（Streamable HTTP）。
// 将外部 tools/list 桥接为 SreyunTool，供 Hermes 对话 / 看板 / 诊断调用。
// ============================================================================

const (
	mcpClientProtocolVersion = "2025-06-18"
	mcpClientMaxBodyBytes    = 4 << 20 // 4 MiB
	mcpClientDefaultTimeout  = 30
)

// MCPClientConfig is one external MCP Server entry stored in AIConfig.MCPClientsJSON.
type MCPClientConfig struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Enabled       bool              `json:"enabled"`
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers,omitempty"`
	TimeoutSec    int               `json:"timeout_sec,omitempty"`
	ToolAllowlist []string          `json:"tool_allowlist,omitempty"`
	ToolBlocklist []string          `json:"tool_blocklist,omitempty"`
	SyncedTools   []MCPSyncedTool   `json:"synced_tools,omitempty"`
	LastSyncUnix  int64             `json:"last_sync_unix,omitempty"`
	LastSyncError string            `json:"last_sync_error,omitempty"`
}

// MCPSyncedTool is a cached tools/list entry for UI preview.
type MCPSyncedTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Blocked     bool   `json:"blocked,omitempty"`
}

var reDangerousMCPTool = regexp.MustCompile(`(?i)(write|delete|drop|exec|execute|shell|bash|rm\b|kill|truncate|update|insert|create|apply|deploy|mutate|grant|revoke)`)

func parseMCPClientsJSON(raw string) ([]MCPClientConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var list []MCPClientConfig
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("mcp_clients_json 非法: %w", err)
	}
	for i := range list {
		normalizeMCPClient(&list[i])
	}
	return list, nil
}

func normalizeMCPClient(c *MCPClientConfig) {
	if c == nil {
		return
	}
	c.ID = sanitizeMCPClientID(c.ID)
	c.Name = strings.TrimSpace(c.Name)
	c.URL = strings.TrimSpace(c.URL)
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = mcpClientDefaultTimeout
	}
	if c.TimeoutSec > 300 {
		c.TimeoutSec = 300
	}
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	if c.ID == "" {
		c.ID = genMCPClientID(c.Name, c.URL)
	}
	if c.Name == "" {
		c.Name = c.ID
	}
}

func sanitizeMCPClientID(id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	var b strings.Builder
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

func genMCPClientID(name, url string) string {
	_ = url
	base := sanitizeMCPClientID(name)
	if base == "" {
		base = "mcp"
	}
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return base + "_" + hex.EncodeToString(buf[:])
}

func encodeMCPClientsJSON(list []MCPClientConfig) string {
	if len(list) == 0 {
		return ""
	}
	b, err := json.Marshal(list)
	if err != nil {
		return ""
	}
	return string(b)
}

// maskMCPClientsJSONForAPI redacts Authorization / token-like header values for browser GET.
func maskMCPClientsJSONForAPI(raw string) string {
	list, err := parseMCPClientsJSON(raw)
	if err != nil || len(list) == 0 {
		if strings.TrimSpace(raw) != "" {
			return "****"
		}
		return ""
	}
	for i := range list {
		for k, v := range list[i].Headers {
			if v == "" {
				continue
			}
			if headerLooksSecret(k) || strings.Contains(v, "****") {
				list[i].Headers[k] = "****"
			}
		}
	}
	return encodeMCPClientsJSON(list)
}

func headerLooksSecret(k string) bool {
	low := strings.ToLower(strings.TrimSpace(k))
	return low == "authorization" || low == "x-api-key" || low == "api-key" ||
		strings.Contains(low, "token") || strings.Contains(low, "secret") || strings.Contains(low, "password")
}

// mergeMCPClientsJSON merges inbound (possibly masked) clients with saved secrets.
func mergeMCPClientsJSON(incoming, saved string) string {
	incoming = strings.TrimSpace(incoming)
	if incoming == "" {
		return ""
	}
	if strings.Contains(incoming, "****") && !strings.HasPrefix(strings.TrimSpace(incoming), "[") {
		return saved
	}
	inList, err := parseMCPClientsJSON(incoming)
	if err != nil {
		if strings.TrimSpace(saved) != "" {
			return saved
		}
		return incoming
	}
	savedList, _ := parseMCPClientsJSON(saved)
	savedByID := map[string]MCPClientConfig{}
	for _, c := range savedList {
		savedByID[c.ID] = c
	}
	for i := range inList {
		old, ok := savedByID[inList[i].ID]
		if !ok {
			continue
		}
		if inList[i].Headers == nil {
			inList[i].Headers = map[string]string{}
		}
		for k, v := range inList[i].Headers {
			if v == "" || strings.Contains(v, "****") {
				if ov, ok2 := old.Headers[k]; ok2 && ov != "" {
					inList[i].Headers[k] = ov
				}
			}
		}
		if len(inList[i].SyncedTools) == 0 && len(old.SyncedTools) > 0 {
			inList[i].SyncedTools = old.SyncedTools
			inList[i].LastSyncUnix = old.LastSyncUnix
			inList[i].LastSyncError = old.LastSyncError
		}
	}
	return encodeMCPClientsJSON(inList)
}

func mcpToolAllowedByClientPolicy(toolName string, c MCPClientConfig) bool {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return false
	}
	low := strings.ToLower(name)
	for _, b := range c.ToolBlocklist {
		if strings.EqualFold(strings.TrimSpace(b), name) {
			return false
		}
	}
	if len(c.ToolAllowlist) > 0 {
		for _, a := range c.ToolAllowlist {
			if strings.EqualFold(strings.TrimSpace(a), name) {
				return true
			}
		}
		return false
	}
	if reDangerousMCPTool.MatchString(low) {
		return false
	}
	return true
}

func externalMCPToolName(serverID, toolName string) string {
	sid := sanitizeMCPClientID(serverID)
	tn := sanitizeMCPClientID(toolName)
	if sid == "" {
		sid = "ext"
	}
	if tn == "" {
		tn = "tool"
	}
	return "ext_" + sid + "_" + tn
}

// ---- HTTP JSON-RPC client ----

type mcpHTTPClient struct {
	cfg        MCPClientConfig
	httpClient *http.Client
	sessionID  string
}

func newMCPHTTPClient(c MCPClientConfig) *mcpHTTPClient {
	to := time.Duration(c.TimeoutSec) * time.Second
	if to <= 0 {
		to = mcpClientDefaultTimeout * time.Second
	}
	return &mcpHTTPClient{
		cfg: c,
		// 必须用 guarded 客户端：这条出站的目标 URL 由用户在「AI 设置 → MCP 客户端」里
		// 填写，而 /ai/mcp-clients/test 会把响应回显给调用方——裸客户端在这里就是一个
		// SSRF 读取原语，可以探内网、可以读云元数据（169.254.169.254）。
		// docs/ci-gate.md 的硬性规定：「AI / Embed / Models / WeKnora 一律走
		// newGuardedHTTPClient」。同仓的飞书/钉钉 webhook（同样用户可配）一直是走
		// guarded 的，唯独这条漏了。
		httpClient: newGuardedHTTPClient(to + 5*time.Second),
	}
}

func (c *mcpHTTPClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	isNotify := strings.HasPrefix(method, "notifications/")
	id := time.Now().UnixNano()
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if !isNotify {
		reqBody["id"] = id
	}
	if params != nil {
		reqBody["params"] = params
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("MCP-Protocol-Version", mcpClientProtocolVersion)
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	for k, v := range c.cfg.Headers {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		httpReq.Header.Set(k, v)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	limited := io.LimitReader(resp.Body, mcpClientMaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > mcpClientMaxBodyBytes {
		return nil, fmt.Errorf("MCP 响应过大（>%d bytes）", mcpClientMaxBodyBytes)
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 240 {
			msg = msg[:240] + "…"
		}
		return nil, fmt.Errorf("MCP HTTP %d: %s", resp.StatusCode, msg)
	}
	if isNotify || len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	var payload []byte
	if strings.Contains(ct, "text/event-stream") {
		payload, err = extractJSONFromSSE(body)
		if err != nil {
			return nil, err
		}
	} else {
		payload = bytes.TrimSpace(body)
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return nil, fmt.Errorf("解析 MCP 响应失败: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("MCP error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	return rpc.Result, nil
}

func extractJSONFromSSE(body []byte) ([]byte, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), mcpClientMaxBodyBytes)
	var dataLines []string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if line == "" && len(dataLines) > 0 {
			break
		}
	}
	if len(dataLines) == 0 {
		trim := bytes.TrimSpace(body)
		if len(trim) > 0 && trim[0] == '{' {
			return trim, nil
		}
		return nil, fmt.Errorf("SSE 响应中无 data 帧")
	}
	return []byte(strings.Join(dataLines, "\n")), sc.Err()
}

func (c *mcpHTTPClient) initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": mcpClientProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "aiops-monitor",
			"version": "1.0",
		},
	})
	if err != nil {
		return err
	}
	_, _ = c.call(ctx, "notifications/initialized", map[string]any{})
	return nil
}

type mcpToolDesc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (c *mcpHTTPClient) listTools(ctx context.Context) ([]mcpToolDesc, error) {
	res, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Tools []mcpToolDesc `json:"tools"`
	}
	if err := json.Unmarshal(res, &parsed); err != nil {
		return nil, fmt.Errorf("tools/list 解析失败: %w", err)
	}
	return parsed.Tools, nil
}

func (c *mcpHTTPClient) callTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	res, err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError           bool `json:"isError"`
		StructuredContent any  `json:"structuredContent"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return string(res), nil
	}
	var texts []string
	for _, ctn := range out.Content {
		if ctn.Text != "" {
			texts = append(texts, ctn.Text)
		}
	}
	text := strings.Join(texts, "\n")
	if text == "" && out.StructuredContent != nil {
		b, _ := json.MarshalIndent(out.StructuredContent, "", "  ")
		text = string(b)
	}
	if text == "" {
		text = string(res)
	}
	if out.IsError {
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}

// TestAndListTools connects, initializes, and returns tools (does not persist).
func TestAndListTools(ctx context.Context, c MCPClientConfig) ([]MCPSyncedTool, error) {
	normalizeMCPClient(&c)
	if c.URL == "" {
		return nil, fmt.Errorf("请填写 MCP Server URL")
	}
	cli := newMCPHTTPClient(c)
	if err := cli.initialize(ctx); err != nil {
		return nil, err
	}
	tools, err := cli.listTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MCPSyncedTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, MCPSyncedTool{
			Name:        t.Name,
			Description: t.Description,
			Blocked:     !mcpToolAllowedByClientPolicy(t.Name, c),
		})
	}
	return out, nil
}

// SyncMCPClient refreshes tools/list for one client config and returns updated config.
func SyncMCPClient(ctx context.Context, c MCPClientConfig) (MCPClientConfig, error) {
	normalizeMCPClient(&c)
	tools, err := TestAndListTools(ctx, c)
	c.LastSyncUnix = time.Now().Unix()
	if err != nil {
		c.LastSyncError = err.Error()
		return c, err
	}
	c.LastSyncError = ""
	c.SyncedTools = tools
	return c, nil
}

// ---- Manager + Sreyun bridge ----

type mcpClientRuntime struct {
	cfg   MCPClientConfig
	tools []mcpToolDesc
	cli   *mcpHTTPClient
}

// MCPClientManager holds enabled external MCP clients and their tool caches.
type MCPClientManager struct {
	mu   sync.RWMutex
	list []*mcpClientRuntime
}

func newMCPClientManager() *MCPClientManager {
	return &MCPClientManager{}
}

func (m *MCPClientManager) Reload(rawJSON string) error {
	cfgs, err := parseMCPClientsJSON(rawJSON)
	if err != nil {
		return err
	}
	var runtimes []*mcpClientRuntime
	for _, c := range cfgs {
		if !c.Enabled || strings.TrimSpace(c.URL) == "" {
			continue
		}
		rt := &mcpClientRuntime{cfg: c, cli: newMCPHTTPClient(c)}
		for _, st := range c.SyncedTools {
			if st.Blocked || !mcpToolAllowedByClientPolicy(st.Name, c) {
				continue
			}
			rt.tools = append(rt.tools, mcpToolDesc{Name: st.Name, Description: st.Description})
		}
		runtimes = append(runtimes, rt)
	}
	m.mu.Lock()
	m.list = runtimes
	m.mu.Unlock()
	return nil
}

func (m *MCPClientManager) EnabledCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.list)
}

type mcpBridgedTool struct {
	ServerID   string
	ServerName string
	Tool       mcpToolDesc
	BridgeName string
}

func (m *MCPClientManager) ListBridgedTools() []mcpBridgedTool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []mcpBridgedTool
	for _, rt := range m.list {
		for _, t := range rt.tools {
			if !mcpToolAllowedByClientPolicy(t.Name, rt.cfg) {
				continue
			}
			out = append(out, mcpBridgedTool{
				ServerID: rt.cfg.ID, ServerName: rt.cfg.Name, Tool: t,
				BridgeName: externalMCPToolName(rt.cfg.ID, t.Name),
			})
		}
	}
	return out
}

func (m *MCPClientManager) ensureTools(ctx context.Context, rt *mcpClientRuntime) error {
	if len(rt.tools) > 0 {
		return nil
	}
	if err := rt.cli.initialize(ctx); err != nil {
		return err
	}
	tools, err := rt.cli.listTools(ctx)
	if err != nil {
		return err
	}
	filtered := make([]mcpToolDesc, 0, len(tools))
	for _, t := range tools {
		if mcpToolAllowedByClientPolicy(t.Name, rt.cfg) {
			if t.InputSchema == nil {
				t.InputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			filtered = append(filtered, t)
		}
	}
	rt.tools = filtered
	return nil
}

func (m *MCPClientManager) Call(ctx context.Context, serverID, toolName string, args map[string]any) (string, error) {
	m.mu.RLock()
	var rt *mcpClientRuntime
	for _, x := range m.list {
		if x.cfg.ID == serverID {
			rt = x
			break
		}
	}
	m.mu.RUnlock()
	if rt == nil {
		return "", fmt.Errorf("未找到已启用的 MCP Client: %s", serverID)
	}
	if !mcpToolAllowedByClientPolicy(toolName, rt.cfg) {
		return "", fmt.Errorf("工具 %s 被策略拦截（危险名默认拒绝，请加入 allowlist）", toolName)
	}
	if err := m.ensureTools(ctx, rt); err != nil {
		return "", err
	}
	return rt.cli.callTool(ctx, toolName, args)
}

func (m *MCPClientManager) HasEnabled() bool {
	return m != nil && m.EnabledCount() > 0
}
