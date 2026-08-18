package main

// Phase 2 · 明文 HTTP 内容审计（服务端接收 + 查询）。agent 抓明文 HTTP 请求(method/path/Host/
// body 前缀)上报，落 PG 审计库。⚠ 高敏感：body 可能含用户发给大模型的 prompt(PII)。仅当 agent
// 显式开启 content_audit 时才有数据；服务端只做接收/存储/查询，保留期由 cleanupContentAudit 控制。

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"aiops-monitor/shared"
)

// handleAgentContentAudit 接收 agent 上报的内容审计事件（指纹校验，与其它 agent ingest 一致）。
func (s *Server) handleAgentContentAudit(w http.ResponseWriter, r *http.Request) {
	var rep shared.ContentAuditReport
	if !decodeContentAuditReport(w, r, &rep) {
		return
	}
	if rep.HostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id required"})
		return
	}
	fp := r.Header.Get("X-Agent-Fingerprint")
	if fp == "" {
		fp = r.URL.Query().Get("fp")
	}
	if !s.forwardFingerprintOKByHost(rep.HostID, fp) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "fingerprint mismatch"})
		return
	}
	if len(rep.Events) > 256 {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too many content audit events; max 256"})
		return
	}
	for i := range rep.Events {
		normalizeContentAuditEvent(&rep.Events[i], rep.Timestamp)
	}
	s.persistContentAuditReport(rep)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGatewayContentAudit is the preferred HTTPS/LLM path: an application
// gateway or SDK emits structured events after TLS termination. It is public
// only at the session layer and requires a dedicated long random Bearer token.
func (s *Server) handleGatewayContentAudit(w http.ResponseWriter, r *http.Request) {
	expected := strings.TrimSpace(os.Getenv("AIOPS_CONTENT_AUDIT_INGEST_TOKEN"))
	if len(expected) < 24 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "gateway audit ingest is not configured"})
		return
	}
	provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		if shouldAlertContent("gateway-ingest-auth|"+s.clientIP(r), time.Now().Unix()) {
			s.store.AddLog(LogEntry{
				Kind: KindSystem, Level: "warning", Actor: "LLM Gateway Audit", IP: s.clientIP(r),
				Message: "LLM Gateway 内容审计摄入鉴权失败",
			})
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid audit ingest token"})
		return
	}
	var rep shared.ContentAuditReport
	if !decodeContentAuditReport(w, r, &rep) {
		return
	}
	if !validGatewayAuditHostID(rep.HostID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid host_id"})
		return
	}
	if len(rep.Events) == 0 || len(rep.Events) > 256 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "events must contain 1..256 items"})
		return
	}
	for i := range rep.Events {
		ev := &rep.Events[i]
		ev.CaptureBackend = "gateway"
		if strings.TrimSpace(ev.BodyMode) == "" {
			ev.BodyMode = "metadata" // fail closed unless the integration is explicit
		}
		normalizeContentAuditEvent(ev, rep.Timestamp)
	}
	s.persistContentAuditReport(rep)
	s.store.AddLog(LogEntry{
		Kind: KindSystem, Level: "info", Actor: "LLM Gateway Audit", IP: s.clientIP(r), Host: rep.HostID,
		Message: fmt.Sprintf("接收 LLM Gateway 结构化内容审计：host=%s events=%d", rep.HostID, len(rep.Events)),
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "events": len(rep.Events)})
}

func decodeContentAuditReport(w http.ResponseWriter, r *http.Request, rep *shared.ContentAuditReport) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(rep); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "content audit report too large"})
			return false
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "content audit report too large"})
			return false
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid trailing json"})
		return false
	}
	return true
}

func validGatewayAuditHostID(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for _, ch := range v {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':') {
			return false
		}
	}
	return true
}

func (s *Server) persistContentAuditReport(rep shared.ContentAuditReport) {
	// DLP 扫描：逐条查敏感数据(内置密钥/身份证/凭据 + 用户关键词)，命中即打标签 + 告警(去重防风暴)。
	// policy_decision=deny|block：服务端强制抬升严重度、剥离正文、并创建 Incident（去重）。
	hostname := rep.HostID
	if h := s.hostByID(rep.HostID); h != nil {
		hostname = h.Hostname
	}
	cfg := s.cfg.Get()
	labels := make([]string, len(rep.Events))
	for i := range rep.Events {
		enrichLLMAuditEvent(&rep.Events[i])
		ev := &rep.Events[i]
		enforceContentAuditPolicy(ev)
		hits := scanSensitive(ev.Body+"\n"+ev.RespBody, cfg.ContentAuditSensitiveKeywords)
		for _, label := range ev.RedactionLabels {
			hits = append(hits, "端侧脱敏:"+label)
		}
		hits = append(hits, ev.RiskLabels...)
		if isContentPolicyBlocked(ev.PolicyDecision) {
			hits = append(hits, "策略拦截:"+ev.PolicyDecision)
		}
		hits = uniqueAuditLabels(hits)
		if len(hits) == 0 && !isContentPolicyBlocked(ev.PolicyDecision) {
			continue
		}
		labels[i] = strings.Join(hits, ", ")
		dest := ev.Host
		if dest == "" {
			dest = ev.DstIP
		}
		dest += ev.Path
		lvl := sensitiveSeverity(hits)
		if isContentPolicyBlocked(ev.PolicyDecision) {
			lvl = "critical"
		}
		a := Alert{
			HostID: rep.HostID, Hostname: hostname, IP: ev.SrcIP,
			Level: lvl, Type: "content_audit",
			Scope:     ev.SrcIP + "→" + dest,
			Message:   Tz("alert.content_sensitive", ev.SrcIP, dest, labels[i]),
			Timestamp: ev.Ts,
		}
		if isContentPolicyBlocked(ev.PolicyDecision) {
			a.Message = fmt.Sprintf("内容策略%s：%s → %s（%s）", strings.ToUpper(ev.PolicyDecision), ev.SrcIP, dest, labels[i])
		}
		s.store.AddLog(LogEntry{Kind: KindSystem, Level: lvl, Actor: "内容审计DLP", Host: hostname, Message: a.Message})
		if cfg.AlertsEnabled && shouldAlertContent(a.HostID+"|"+a.Scope+"|"+labels[i]+"|"+ev.PolicyDecision, ev.Ts) {
			s.notifier.pushChannels(cfg, a, true)
			if lvl == "critical" && s.incidents != nil {
				s.incidents.OnAlertTransition(a, alertKey(a), true) // 密钥外泄 / 策略拦截转 Incident
			}
		}
	}
	if s.pg != nil && len(rep.Events) > 0 {
		s.pg.insertContentAudit(rep.HostID, rep.Events, labels)
		slog.Info("内容审计已存储", "host_id", rep.HostID, "events", len(rep.Events))
	}
}

func isContentPolicyBlocked(decision string) bool {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "deny", "denied", "block", "blocked", "reject", "rejected",
		"forbid", "forbidden", "drop", "quarantine":
		return true
	default:
		return false
	}
}

// enforceContentAuditPolicy strips retained bodies for deny/block decisions so
// persisted audit rows cannot re-expose blocked prompt/completion content.
func enforceContentAuditPolicy(ev *shared.ContentAuditEvent) {
	if ev == nil || !isContentPolicyBlocked(ev.PolicyDecision) {
		return
	}
	ev.PolicyDecision = strings.ToLower(strings.TrimSpace(ev.PolicyDecision))
	if ev.PolicyDecision == "blocked" || ev.PolicyDecision == "rejected" {
		ev.PolicyDecision = "block"
	}
	if ev.Body != "" || ev.RespBody != "" {
		ev.BodyMode = "redacted"
		ev.Body = "[blocked by policy]"
		ev.RespBody = "[blocked by policy]"
	}
	ev.RiskLabels = append(ev.RiskLabels, "policy:"+ev.PolicyDecision)
	ev.RiskLabels = normalizeAuditLabels(ev.RiskLabels)
}

func normalizeContentAuditEvent(ev *shared.ContentAuditEvent, reportTs int64) {
	if ev == nil {
		return
	}
	now := time.Now().Unix()
	if reportTs <= 0 || reportTs > now+300 || reportTs < now-86400 {
		reportTs = now
	}
	if ev.Ts <= 0 || ev.Ts > now+300 || ev.Ts < now-86400 {
		ev.Ts = reportTs
	}
	if net.ParseIP(ev.SrcIP) == nil {
		ev.SrcIP = ""
	}
	if net.ParseIP(ev.DstIP) == nil {
		ev.DstIP = ""
	}
	ev.Method = truncateAuditField(strings.ToUpper(strings.TrimSpace(ev.Method)), 16)
	switch strings.ToLower(strings.TrimSpace(ev.Protocol)) {
	case "http", "tls", "gateway":
		ev.Protocol = strings.ToLower(strings.TrimSpace(ev.Protocol))
	default:
		switch {
		case strings.EqualFold(ev.CaptureBackend, "gateway"):
			ev.Protocol = "gateway"
		case ev.Method != "":
			ev.Protocol = "http"
		default:
			ev.Protocol = "unknown"
		}
	}
	ev.Host = truncateAuditField(strings.TrimSpace(ev.Host), 512)
	ev.Path = truncateAuditField(ev.Path, 4096)
	ev.CType = truncateAuditField(ev.CType, 256)
	ev.RespCType = truncateAuditField(ev.RespCType, 256)
	ev.Body = truncateAuditField(ev.Body, 64<<10)
	ev.RespBody = truncateAuditField(ev.RespBody, 1<<20)
	visibleReqBody, visibleRespBody := ev.Body, ev.RespBody

	switch strings.ToLower(strings.TrimSpace(ev.CaptureBackend)) {
	case "native", "tshark", "gateway", "sdk":
		ev.CaptureBackend = strings.ToLower(strings.TrimSpace(ev.CaptureBackend))
	default:
		ev.CaptureBackend = "legacy"
	}
	switch strings.ToLower(strings.TrimSpace(ev.BodyMode)) {
	case "metadata", "redacted", "full":
		ev.BodyMode = strings.ToLower(strings.TrimSpace(ev.BodyMode))
	default:
		ev.BodyMode = "legacy"
	}
	if ev.Protocol == "tls" {
		ev.BodyMode = "metadata"
	}
	if ev.ReqBytes < 0 || ev.ReqBytes > 2<<20 {
		ev.ReqBytes = 0
	}
	if ev.RespBytes < 0 || ev.RespBytes > 2<<20 {
		ev.RespBytes = 0
	}
	if ev.ReqBytes == 0 {
		ev.ReqBytes = len([]byte(visibleReqBody))
	}
	if ev.RespBytes == 0 {
		ev.RespBytes = len([]byte(visibleRespBody))
	}
	if !validAuditSHA256(ev.ReqSHA256) {
		ev.ReqSHA256 = ""
	}
	if !validAuditSHA256(ev.RespSHA256) {
		ev.RespSHA256 = ""
	}
	if ev.ReqSHA256 == "" && visibleReqBody != "" {
		ev.ReqSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(visibleReqBody)))
	}
	if ev.RespSHA256 == "" && visibleRespBody != "" {
		ev.RespSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(visibleRespBody)))
	}
	if ev.BodyMode == "metadata" {
		ev.Body, ev.RespBody = "", ""
	}
	if ev.RedactionCount < 0 || ev.RedactionCount > 100000 {
		ev.RedactionCount = 0
	}
	ev.RedactionLabels = normalizeAuditLabels(ev.RedactionLabels)
	ev.PrincipalID = truncateAuditField(strings.TrimSpace(ev.PrincipalID), 256)
	ev.ApplicationID = truncateAuditField(strings.TrimSpace(ev.ApplicationID), 256)
	ev.EventID = truncateAuditField(strings.TrimSpace(ev.EventID), 256)
	ev.RequestID = truncateAuditField(strings.TrimSpace(ev.RequestID), 256)
	ev.TraceID = truncateAuditField(strings.TrimSpace(ev.TraceID), 256)
	ev.LLMProvider = truncateAuditField(strings.ToLower(strings.TrimSpace(ev.LLMProvider)), 128)
	ev.LLMModel = truncateAuditField(strings.TrimSpace(ev.LLMModel), 256)
	ev.LLMOperation = truncateAuditField(strings.ToLower(strings.TrimSpace(ev.LLMOperation)), 128)
	for _, n := range []*int{&ev.InputTokens, &ev.OutputTokens, &ev.ToolCalls} {
		if *n < 0 || *n > 1_000_000_000 {
			*n = 0
		}
	}
	if ev.LatencyMS < 0 || ev.LatencyMS > 86_400_000 {
		ev.LatencyMS = 0
	}
	ev.PolicyDecision = truncateAuditField(strings.ToLower(strings.TrimSpace(ev.PolicyDecision)), 64)
	ev.RiskLabels = normalizeAuditLabels(ev.RiskLabels)
	if ev.EventID == "" && ev.CaptureBackend == "gateway" && ev.RequestID != "" {
		ev.EventID = ev.RequestID
	}
}

func truncateAuditField(s string, maxBytes int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	return strings.ToValidUTF8(s, "")
}

func validAuditSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, ch := range s {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}

func normalizeAuditLabels(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		label := strings.ToLower(strings.TrimSpace(raw))
		if label == "" || len(label) > 64 || seen[label] {
			continue
		}
		valid := true
		for _, ch := range label {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-') {
				valid = false
				break
			}
		}
		if valid {
			seen[label] = true
			out = append(out, label)
		}
		if len(out) >= 32 {
			break
		}
	}
	return out
}

func uniqueAuditLabels(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, label := range in {
		label = strings.TrimSpace(label)
		if label != "" && !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	return out
}

// handleContentAuditHosts 只返回有内容审计记录的主机（供前端过滤掉无数据主机）。
func (s *Server) handleContentAuditHosts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"hosts": []any{}})
		return
	}
	hosts, err := s.pg.getContentAuditHosts()
	if err != nil {
		slog.Warn("查询有内容审计的主机失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	if u, ok := s.currentUser(r); ok && u.hostScopeRestricted() && roleRank(u.Role) < roleRank(RoleAdmin) {
		filtered := make([]map[string]any, 0, len(hosts))
		for _, h := range hosts {
			hid, _ := h["host_id"].(string)
			if hid != "" && s.userCanAccessHost(u, hid) {
				filtered = append(filtered, h)
			}
		}
		hosts = filtered
	}
	s.annotateHostNames(hosts)
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts})
}

// handleContentAudit 前端查询内容审计记录（最新在前，支持 filter）。
func (s *Server) handleContentAudit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	hostID := r.URL.Query().Get("host")
	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
		return
	}
	limit := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	evs, err := s.pg.getContentAudit(hostID, r.URL.Query().Get("filter"), limit)
	if err != nil {
		slog.Warn("查询内容审计失败", "host", hostID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	for _, ev := range evs {
		annotateLLMAuditEvent(ev)
	}
	// Bodies may contain prompts/PII — only admins receive raw body fields.
	if u, ok := s.currentUser(r); !ok || roleRank(u.Role) < roleRank(RoleAdmin) {
		for _, ev := range evs {
			if ev == nil {
				continue
			}
			if body, _ := ev["body"].(string); body != "" {
				ev["body"] = "[redacted]"
			}
			if body, _ := ev["resp_body"].(string); body != "" {
				ev["resp_body"] = "[redacted]"
			}
			if mode, _ := ev["body_mode"].(string); mode == "full" || mode == "legacy" {
				ev["body_mode"] = "redacted"
			}
		}
	}
	s.store.AddLog(LogEntry{
		Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r), Host: hostID,
		Message: fmt.Sprintf("查看内容审计：host=%s filter=%q records=%d", hostID, r.URL.Query().Get("filter"), len(evs)),
	})
	writeJSON(w, http.StatusOK, map[string]any{"events": evs})
}
