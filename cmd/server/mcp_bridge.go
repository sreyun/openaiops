package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// registerExternalMCPTools bridges enabled MCP clients into the Sreyun tool map.
// Removes previously registered ext_* tools, then re-adds from manager cache.
//
// 这条路径在运行时被 handleSetAIConfig / handleSyncMCPClient 触发，与正在进行的
// AI 会话并发。工具表的增删必须在 toolsMu 下完成再原子发布；而 mgr.Reload 会做网络
// 探测，绝不能持锁做，否则一次保存配置就会把所有会话卡住。
func (h *SreyunCore) registerExternalMCPTools() {
	if h == nil || h.s == nil {
		return
	}
	mgr := h.s.mcpClients

	// —— 锁外：网络 I/O ——
	var bridged []mcpBridgedTool
	if mgr != nil {
		_ = mgr.Reload(h.s.cfg.AIConfig().MCPClientsJSON)
		bridged = mgr.ListBridgedTools()
	}

	// —— 锁内：纯内存改表 ——
	h.mutateTools(func(tools map[string]SreyunTool) {
		for name := range tools {
			if strings.HasPrefix(name, "ext_") || name == "call_external_mcp" || name == "list_external_mcp_tools" {
				delete(tools, name)
			}
		}
		if mgr == nil {
			return
		}
		for _, b := range bridged {
			bt := b // capture
			schema := bt.Tool.InputSchema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			desc := bt.Tool.Description
			if desc == "" {
				desc = "外部 MCP 工具 " + bt.Tool.Name
			}
			desc = fmt.Sprintf("[外部MCP:%s] %s", bt.ServerName, desc)
			tools[bt.BridgeName] = SreyunTool{
				Name:        bt.BridgeName,
				Description: desc,
				Parameters:  schema,
				Execute: func(args map[string]any) (string, error) {
					// 继承本轮会话上下文：客户端断开时外部调用也随之取消，不再空转 45s。
					ctx, cancel := context.WithTimeout(h.callContext(args), 45*time.Second)
					defer cancel()
					if h.s.aiGov != nil {
						h.s.aiGov.recordTool(aiToolAuditEntry{
							Actor: "mcp-client:" + bt.ServerID, Tool: bt.BridgeName, Action: "tools/call", Approved: true,
							Detail: "remote=" + bt.Tool.Name,
						})
					}
					// 剥离引擎内部键，避免把面板用户名等信息透给第三方 MCP 服务端。
					return mgr.Call(ctx, bt.ServerID, bt.Tool.Name, sanitizeOutboundArgs(args))
				},
			}
		}
		h.registerExternalMCPMetaToolsLocked(tools)
	})
}

// registerExternalMCPMetaToolsLocked adds the discovery / explicit-call tools.
// Caller holds toolsMu.
func (h *SreyunCore) registerExternalMCPMetaToolsLocked(tools map[string]SreyunTool) {
	// Meta tools for discovery / explicit call when many servers exist
	tools["list_external_mcp_tools"] = SreyunTool{
		Name:        "list_external_mcp_tools",
		Description: "列出已启用的外部 MCP Client 及其可用工具（只读）。排查外部系统或创建看板前可先调用确认工具名。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: h.execListExternalMCPTools,
	}
	tools["call_external_mcp"] = SreyunTool{
		Name: "call_external_mcp",
		Description: "调用指定外部 MCP Client 上的工具（只读策略仍生效）。" +
			"参数：server_id（list_external_mcp_tools 返回的 id）、tool（远端工具名）、args（JSON 对象）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"server_id": map[string]string{"type": "string", "description": "MCP Client id"},
				"tool":      map[string]string{"type": "string", "description": "远端工具名"},
				"args":      map[string]any{"type": "object", "description": "工具参数对象"},
			},
			"required": []string{"server_id", "tool"},
		},
		Execute: h.execCallExternalMCP,
	}
}

func (h *SreyunCore) reloadExternalMCPTools() {
	h.registerExternalMCPTools()
}

func (h *SreyunCore) execListExternalMCPTools(_ map[string]any) (string, error) {
	if h.s == nil || h.s.mcpClients == nil || !h.s.mcpClients.HasEnabled() {
		return "当前未启用任何外部 MCP Client。请在「AI 设置 → 集成 → MCP Clients」添加并启用。", nil
	}
	cfgs, _ := parseMCPClientsJSON(h.s.cfg.AIConfig().MCPClientsJSON)
	var b strings.Builder
	n := 0
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		n++
		fmt.Fprintf(&b, "- id=%s name=%s url=%s tools=%d\n", c.ID, c.Name, c.URL, len(c.SyncedTools))
		for _, t := range c.SyncedTools {
			flag := ""
			if t.Blocked {
				flag = " [blocked]"
			}
			fmt.Fprintf(&b, "    · %s%s — %s\n", t.Name, flag, truncateRunes(t.Description, 120))
			fmt.Fprintf(&b, "      bridge=%s\n", externalMCPToolName(c.ID, t.Name))
		}
	}
	if n == 0 {
		return "无已启用的 MCP Client。", nil
	}
	return b.String(), nil
}

func (h *SreyunCore) execCallExternalMCP(args map[string]any) (string, error) {
	serverID, _ := args["server_id"].(string)
	tool, _ := args["tool"].(string)
	serverID = strings.TrimSpace(serverID)
	tool = strings.TrimSpace(tool)
	if serverID == "" || tool == "" {
		return "", fmt.Errorf("server_id 与 tool 必填")
	}
	var toolArgs map[string]any
	switch v := args["args"].(type) {
	case map[string]any:
		toolArgs = v
	default:
		toolArgs = map[string]any{}
	}
	if h.s == nil || h.s.mcpClients == nil {
		return "", fmt.Errorf("MCP Client 管理器未初始化")
	}
	ctx, cancel := context.WithTimeout(h.callContext(args), 45*time.Second)
	defer cancel()
	if h.s.aiGov != nil {
		h.s.aiGov.recordTool(aiToolAuditEntry{
			Actor: "mcp-client:" + serverID, Tool: "call_external_mcp", Action: "tools/call", Approved: true,
			Detail: "remote=" + tool,
		})
	}
	return h.s.mcpClients.Call(ctx, serverID, tool, sanitizeOutboundArgs(toolArgs))
}

// mcpPrefetchMaxTurnsKey limits runLoop turns for diagnosis/assist MCP prefetch.
type mcpPrefetchMaxTurnsKey struct{}

func (s *Server) formatExternalMCPInventory() string {
	if s == nil || s.mcpClients == nil || !s.mcpClients.HasEnabled() {
		return ""
	}
	cfgs, err := parseMCPClientsJSON(s.cfg.AIConfig().MCPClientsJSON)
	if err != nil || len(cfgs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【外部 MCP Client 可用工具】\n")
	n := 0
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		n++
		fmt.Fprintf(&b, "- %s (id=%s)\n", c.Name, c.ID)
		shown := 0
		for _, t := range c.SyncedTools {
			if t.Blocked {
				continue
			}
			fmt.Fprintf(&b, "  · %s → bridge `%s`\n", t.Name, externalMCPToolName(c.ID, t.Name))
			shown++
			if shown >= 24 {
				b.WriteString("  · …\n")
				break
			}
		}
		if shown == 0 {
			b.WriteString("  ·（尚未同步工具，可先 list_external_mcp_tools）\n")
		}
	}
	if n == 0 {
		return ""
	}
	b.WriteString("需要外部系统事实时优先调用上述 bridge 名或 call_external_mcp。\n")
	return b.String()
}

// prefetchExternalMCPForDiagnosis runs a short tool-capable gather when MCP Clients are enabled.
func (s *Server) prefetchExternalMCPForDiagnosis(query, actor string) string {
	inv := s.formatExternalMCPInventory()
	if inv == "" || s.sreyun == nil {
		return inv
	}
	cfg := s.cfg.AIConfig()
	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		return inv
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return inv
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, mcpPrefetchMaxTurnsKey{}, 3)
	sys := "你是外部事实收集助手。只允许调用 list_external_mcp_tools、call_external_mcp 以及以 ext_ 开头的外部 MCP 工具。" +
		"最多用 2～3 次工具调用，收集与用户问题相关的外部数据后，用简洁中文摘要（要点列表）。禁止写操作、禁止创建看板、禁止编造。"
	msgs := []map[string]string{
		{"role": "system", "content": sys + inv},
		{"role": "user", "content": "请针对以下诊断/分析问题拉取必要的外部 MCP 数据并摘要：\n" + query},
	}
	reply, _, err := s.sreyun.runLoop(ctx, cfg, msgs, nil, false, nil, actor)
	if err != nil || strings.TrimSpace(reply) == "" {
		return inv
	}
	out := inv + "\n\n【外部 MCP 预取摘要】\n" + strings.TrimSpace(reply)
	if len([]rune(out)) > 6000 {
		r := []rune(out)
		out = string(r[:6000]) + "…"
	}
	return out
}

func assistTaskWantsExternalMCP(task string) bool {
	t := strings.ToLower(strings.TrimSpace(task))
	switch {
	case strings.Contains(t, "diagnos"), strings.Contains(t, "chart"), strings.Contains(t, "dashboard"),
		strings.Contains(t, "forecast"), strings.Contains(t, "incident"), strings.Contains(t, "root"),
		strings.Contains(t, "slow"), strings.Contains(t, "security"), strings.Contains(t, "sre"):
		return true
	default:
		return false
	}
}
