package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

func (h *SreyunCore) registerSecurityTools() {
	h.tools["query_security_posture"] = SreyunTool{
		Name: "query_security_posture",
		Description: "查询平台安全态势：开放漏洞数、主机/Web 扫描状态、近期登录失败与防御事件摘要。" +
			"用户问「现在安全吗」「有没有被扫/被撞库」时优先调用。",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Execute:    h.execQuerySecurityPosture,
	}
	h.tools["defend_security_event"] = SreyunTool{
		Name: "defend_security_event",
		Description: "对安全事件执行防御调度：记录审计、沉淀记忆、可选创建事件单，并打开对应安全界面。" +
			"kind 示例：login_bruteforce / vuln_scan / web_attack / mcp_abuse / content_leak / generic。" +
			"不会执行破坏性操作；主要用于感知、留痕与引导处置。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":          map[string]string{"type": "string", "description": "事件类型"},
				"source":        map[string]string{"type": "string", "description": "来源 IP / 主机 / Token 名"},
				"target":        map[string]string{"type": "string", "description": "目标账户 / URL / 主机"},
				"summary":       map[string]string{"type": "string", "description": "事件摘要"},
				"create_ticket": map[string]any{"type": "boolean", "description": "是否创建 SRE 事件单，默认 true"},
			},
			"required": []string{"kind", "summary"},
		},
		Execute: h.execDefendSecurityEvent,
	}
}

func (h *SreyunCore) execQuerySecurityPosture(args map[string]any) (string, error) {
	counts := h.s.countOpenSecurityFindingsDetail()
	hostCfg := h.s.cfg.HostSecurity()
	webCfg := h.s.cfg.WebSecurity()
	hostRunning, hostStuck := h.s.hostSec.scanActivity(hostCfg.TimeoutSec)
	webRunning, webStuck := h.s.webSec.scanActivity(webCfg.TimeoutSec)

	recent := h.s.listRecentSecurityDefense(8)
	data := map[string]any{
		"open_critical": counts.Critical,
		"open_high":     counts.High,
		"host": map[string]any{
			"open_critical": counts.HostCritical,
			"open_high":     counts.HostHigh,
			"scan_running":  hostRunning,
			"scan_stuck":    hostStuck,
			"enabled":       hostCfg.Enabled,
		},
		"web": map[string]any{
			"open_critical": counts.WebCritical,
			"open_high":     counts.WebHigh,
			"scan_running":  webRunning,
			"scan_stuck":    webStuck,
			"targets":       len(webCfg.Targets),
		},
		"recent_defense": recent,
		"auto_defend":    h.s.cfg.AIConfig().AutoDefendEnabled,
	}
	sum := fmt.Sprintf("开放漏洞 critical=%d high=%d；主机扫描运行中=%d，Web 扫描运行中=%d",
		counts.Critical, counts.High, hostRunning, webRunning)
	actions := []map[string]any{
		navigateViewAction("security-overview", "打开安全总览", "安全总览"),
	}
	if counts.HostCritical+counts.HostHigh > 0 {
		actions = append(actions, navigateViewAction("host-security", "打开主机安全", "主机安全"))
	}
	if counts.WebCritical+counts.WebHigh > 0 {
		actions = append(actions, navigateViewAction("web-security", "打开 Web 扫描", "Web 扫描"))
	}
	return capabilityJSON(capabilityResult{OK: true, Summary: sum, Data: data, UIActions: actions}), nil
}

func (h *SreyunCore) execDefendSecurityEvent(args map[string]any) (string, error) {
	kind, _ := args["kind"].(string)
	source, _ := args["source"].(string)
	target, _ := args["target"].(string)
	summary, _ := args["summary"].(string)
	createTicket := true
	if v, ok := args["create_ticket"].(bool); ok {
		createTicket = v
	}
	res := h.s.recordSecurityDefense(securityDefenseInput{
		Kind:         strings.TrimSpace(kind),
		Source:       strings.TrimSpace(source),
		Target:       strings.TrimSpace(target),
		Summary:      strings.TrimSpace(summary),
		CreateTicket: createTicket,
		Actor:        "ai-chat",
	})
	if res.Error != "" {
		return capabilityJSON(capabilityResult{OK: false, Error: res.Error}), nil
	}
	return capabilityJSON(capabilityResult{
		OK:        true,
		Summary:   res.Summary,
		Data:      res.Data,
		UIActions: res.UIActions,
	}), nil
}

// ---- Server-side auto defense ----

type securityDefenseInput struct {
	Kind         string
	Source       string
	Target       string
	Summary      string
	CreateTicket bool
	Actor        string
	Force        bool // bypass debounce
}

type securityDefenseResult struct {
	Summary   string
	Data      map[string]any
	UIActions []map[string]any
	Error     string
	Skipped   bool
}

type securityDefenseEvent struct {
	Kind     string `json:"kind"`
	Source   string `json:"source,omitempty"`
	Target   string `json:"target,omitempty"`
	Summary  string `json:"summary"`
	Incident int64  `json:"incident_id,omitempty"`
	Ts       int64  `json:"ts"`
}

var (
	secDefendMu   sync.Mutex
	secDefendLast = map[string]int64{} // key -> unix
	secDefendRing []securityDefenseEvent
)

func (s *Server) autoDefendEnabled() bool {
	return s.cfg.AIConfig().AutoDefendEnabled
}

// autoDefendOnLoginLockout fires when an IP crosses the login failure threshold.
func (s *Server) autoDefendOnLoginLockout(ip, username string) {
	if !s.autoDefendEnabled() {
		return
	}
	go s.recordSecurityDefense(securityDefenseInput{
		Kind:         "login_bruteforce",
		Source:       ip,
		Target:       username,
		Summary:      fmt.Sprintf("登录暴力破解锁定：IP=%s 账户=%s（已触发限流）", ip, username),
		CreateTicket: true,
		Actor:        "auto-defend",
	})
}

// autoDefendOnMCPAbuse fires when MCP bearer exceeds rate limit.
func (s *Server) autoDefendOnMCPAbuse(tokenName string) {
	if !s.autoDefendEnabled() {
		return
	}
	go s.recordSecurityDefense(securityDefenseInput{
		Kind:         "mcp_abuse",
		Source:       tokenName,
		Summary:      fmt.Sprintf("MCP 调用超限疑似滥用：token=%s", tokenName),
		CreateTicket: true,
		Actor:        "auto-defend",
	})
}

func (s *Server) recordSecurityDefense(in securityDefenseInput) securityDefenseResult {
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Kind == "" {
		in.Kind = "generic"
	}
	if in.Summary == "" {
		return securityDefenseResult{Error: "summary 必填"}
	}
	if in.Actor == "" {
		in.Actor = "system"
	}
	key := in.Kind + "|" + in.Source + "|" + in.Target
	now := time.Now().Unix()
	secDefendMu.Lock()
	if !in.Force {
		if last, ok := secDefendLast[key]; ok && now-last < 300 {
			secDefendMu.Unlock()
			return securityDefenseResult{
				Skipped: true,
				Summary: "同类防御事件 5 分钟内已处理，已去重跳过",
				Data:    map[string]any{"dedup": true, "kind": in.Kind},
			}
		}
	}
	secDefendLast[key] = now
	secDefendMu.Unlock()

	level := "warning"
	sev := "warning"
	view := "security-overview"
	switch in.Kind {
	case "login_bruteforce":
		level, sev, view = "critical", "critical", "security-overview"
	case "vuln_scan", "web_attack":
		level, sev, view = "critical", "high", "web-security"
	case "mcp_abuse":
		level, sev, view = "warning", "warning", "ai-tool-audit"
	case "content_leak":
		level, sev, view = "warning", "warning", "content-audit"
	case "host_vuln":
		level, sev, view = "critical", "high", "host-security"
	}

	s.store.AddLog(LogEntry{
		Kind: KindSystem, Level: level, Actor: in.Actor, IP: in.Source,
		Message: "安全自动防御：" + in.Summary,
	})

	var incidentID int64
	if in.CreateTicket && s.incidents != nil {
		title := "[安全防御] " + trimLine(in.Summary, 80)
		inc := s.incidents.CreateManual(title, sev, "", "", in.Actor)
		incidentID = inc.ID
		s.incidents.AddEvent(inc.ID, "note", in.Actor, fmt.Sprintf("kind=%s source=%s target=%s", in.Kind, in.Source, in.Target))
		s.store.MarkDirty()
	}

	mem := fmt.Sprintf("【安全防御】kind=%s source=%s target=%s detail=%s", in.Kind, in.Source, in.Target, in.Summary)
	s.rememberAI("security", "auto-defend:"+in.Kind, mem)

	ev := securityDefenseEvent{
		Kind: in.Kind, Source: in.Source, Target: in.Target, Summary: in.Summary,
		Incident: incidentID, Ts: now,
	}
	secDefendMu.Lock()
	secDefendRing = append([]securityDefenseEvent{ev}, secDefendRing...)
	if len(secDefendRing) > 50 {
		secDefendRing = secDefendRing[:50]
	}
	secDefendMu.Unlock()

	actions := []map[string]any{navigateViewAction(view, "打开相关安全页", viewTitle(view))}
	if incidentID > 0 {
		actions = append(actions, navigateViewAction("sre", "打开 SRE 事件", "SRE 中枢"))
	}
	sum := "已记录安全防御事件"
	if incidentID > 0 {
		sum += fmt.Sprintf("，事件单 #%d", incidentID)
	}
	return securityDefenseResult{
		Summary:   sum,
		Data:      map[string]any{"kind": in.Kind, "incident_id": incidentID, "view": view},
		UIActions: actions,
	}
}

func (s *Server) listRecentSecurityDefense(n int) []securityDefenseEvent {
	if n <= 0 {
		n = 8
	}
	secDefendMu.Lock()
	defer secDefendMu.Unlock()
	if len(secDefendRing) < n {
		n = len(secDefendRing)
	}
	out := make([]securityDefenseEvent, n)
	copy(out, secDefendRing[:n])
	return out
}

func viewTitle(view string) string {
	if v, ok := resolveUIView(view); ok {
		return v.Title
	}
	return view
}
