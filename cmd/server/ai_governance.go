package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// AI governance fields live on AIConfig (see aiops.go). This file implements
// per-user daily quota tracking and AI write-tool audit trail.

type aiQuotaDay struct {
	Day   string // YYYY-MM-DD UTC
	Count int
}

type aiToolAuditEntry struct {
	Timestamp  int64  `json:"timestamp"`
	Actor      string `json:"actor"`
	Tool       string `json:"tool"`
	Action     string `json:"action"`
	HostID     string `json:"host_id,omitempty"`
	Approved   bool   `json:"approved"`
	Blocked    bool   `json:"blocked"`
	Detail     string `json:"detail,omitempty"`
	IncidentID int64  `json:"incident_id,omitempty"`
}

type aiGovHub struct {
	mu        sync.Mutex
	quota     map[string]aiQuotaDay // username → day count
	mcpQuota  map[string]aiQuotaDay // MCP token fingerprint → per-minute count
	approvals map[string]writeApproval
	tools     []aiToolAuditEntry
	toolCap   int
	path      string
	onRecord  func(aiToolAuditEntry) // optional SIEM/export hook
	pg        *pgStore               // durable dual-write when set
}

func newAIGovHub() *aiGovHub {
	return &aiGovHub{
		quota:     map[string]aiQuotaDay{},
		mcpQuota:  map[string]aiQuotaDay{},
		approvals: map[string]writeApproval{},
		toolCap:   500,
	}
}

func (h *aiGovHub) load(path string) {
	if h == nil || path == "" {
		return
	}
	h.mu.Lock()
	h.path = path
	h.mu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var entries []aiToolAuditEntry
	if json.Unmarshal(b, &entries) != nil {
		return
	}
	h.mu.Lock()
	h.tools = entries
	if len(h.tools) > h.toolCap {
		h.tools = h.tools[:h.toolCap]
	}
	h.mu.Unlock()
}

func (h *aiGovHub) saveLocked() {
	if h.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(h.path), 0o750)
	b, err := json.MarshalIndent(h.tools, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(h.path, b, 0o640)
}

func (h *aiGovHub) checkAndIncrQuota(user string, limit int) (ok bool, used, lim int) {
	if limit <= 0 {
		return true, 0, 0
	}
	if user == "" {
		user = "anonymous"
	}
	day := time.Now().UTC().Format("2006-01-02")
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.quota[user]
	if cur.Day != day {
		cur = aiQuotaDay{Day: day, Count: 0}
	}
	if cur.Count >= limit {
		h.quota[user] = cur
		return false, cur.Count, limit
	}
	cur.Count++
	h.quota[user] = cur
	return true, cur.Count, limit
}

func (h *aiGovHub) recordTool(e aiToolAuditEntry) {
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().Unix()
	}
	h.mu.Lock()
	h.tools = append([]aiToolAuditEntry{e}, h.tools...)
	if len(h.tools) > h.toolCap {
		h.tools = h.tools[:h.toolCap]
	}
	h.saveLocked()
	hook := h.onRecord
	h.mu.Unlock()
	if hook != nil {
		hook(e)
	}
}

func (h *aiGovHub) listTools(limit int) []aiToolAuditEntry {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.tools) < limit {
		limit = len(h.tools)
	}
	out := make([]aiToolAuditEntry, limit)
	copy(out, h.tools[:limit])
	return out
}

// AI 出网脱敏的模式表。
//
// 这些正则的**目的不是做完整 DLP**，而是把"一旦泄漏就直接出事"的那几类挡在出网请求
// 之外：密钥、口令、邮箱、手机号、长摘要。顺序有讲究——先按 key=value 整体替换值，
// 再扫裸露的密钥前缀，否则 `api_key=sk-xxx` 会被后一条先啃掉一半。
var (
	// key=value / key: value 形式的凭据。值可能带引号，也可能裸奔到下一个分隔符。
	reRedactSecretKV = regexp.MustCompile(`(?i)\b(api[_-]?key|secret[_-]?key|secret|password|passwd|pwd|token|access[_-]?key|private[_-]?key|authorization)\b(\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;"'&]+)`)
	// 裸露的密钥字面量：OpenAI/Anthropic 风格 sk-、GitHub ghp_、AWS AKIA、Slack xox*。
	reRedactAPIKey = regexp.MustCompile(`\b(?:sk|rk|pk)-[A-Za-z0-9_\-]{16,}\b|\bgh[pousr]_[A-Za-z0-9]{20,}\b|\bAKIA[0-9A-Z]{16}\b|\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)
	reRedactEmail  = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	// 中国大陆手机号（可带 +86 / 86 前缀）。函数注释一直写着"脱敏手机号"，实现里却
	// 从来没有这一条——写进合规说明的能力必须真的存在。
	reRedactPhoneCN = regexp.MustCompile(`(?:\+?86[- ]?)?\b1[3-9]\d{9}\b`)
	// 长十六进制串：会话令牌、哈希、指纹。24 位以下留着——主机短 ID 与端口号还要给
	// 模型当上下文用，全抹掉等于把问题本身也抹掉了。
	reRedactLongHex = regexp.MustCompile(`\b[0-9a-fA-F]{24,}\b`)
)

// redactAIText 在「AI 设置 → 敏感字段脱敏」打开时，对送往模型的提示词与回显文本做脱敏。
//
// 旧实现把整段文本里的每一个 `@` 换成 `[at]`（于是 `image@sha256:`、Java 注解、
// `user@host` 形式的命令全被改坏），并把任何超过 12 位的十六进制串打星；
// 而函数注释承诺的"邮箱/手机号/令牌"里，手机号一条都没做，最该挡的
// `sk-…` 密钥与 `password=…` 也没有。这是写进交付说明的合规能力，
// 说到的必须做到，没做的不能写在注释里。
func redactAIText(s string, enabled bool) string {
	if !enabled || s == "" {
		return s
	}
	out := reRedactSecretKV.ReplaceAllString(s, "${1}${2}***")
	out = reRedactAPIKey.ReplaceAllString(out, "***")
	out = reRedactEmail.ReplaceAllString(out, "***@***")
	out = reRedactPhoneCN.ReplaceAllString(out, "***")
	out = reRedactLongHex.ReplaceAllString(out, "***")
	return out
}

func (s *Server) handleListAIToolAudit(w http.ResponseWriter, r *http.Request) {
	if s.pg != nil {
		if list := s.pg.listAIToolAudit(100); len(list) > 0 {
			writeJSON(w, http.StatusOK, list)
			return
		}
	}
	if s.aiGov == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.aiGov.listTools(100))
}

// aiGovAllowRequestTask enforces daily quota unless task is listed in QuotaExemptTasks.
func (s *Server) aiGovAllowRequestTask(r *http.Request, task string) (bool, string) {
	if s == nil || s.cfg == nil || s.aiGov == nil {
		return true, ""
	}
	cfg := s.cfg.AIConfig()
	if quotaTaskExempt(cfg, task) {
		return true, ""
	}
	if cfg.DailyQuotaPerUser <= 0 {
		return true, ""
	}
	user := s.actorName(r)
	ok, used, lim := s.aiGov.checkAndIncrQuota(user, cfg.DailyQuotaPerUser)
	if !ok {
		return false, "AI 日配额已用尽（" + itoa(used) + "/" + itoa(lim) + "），请明日再试或联系管理员提高配额"
	}
	return true, ""
}

// handleIssueAIWriteApproval issues a short-lived per-action approval token for write tools.
// POST /api/v1/ai/write-approval  {tool, args_hash?, ttl_sec?}
func (s *Server) handleIssueAIWriteApproval(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var req struct {
		Tool     string `json:"tool"`
		ArgsHash string `json:"args_hash,omitempty"`
		TTLSec   int    `json:"ttl_sec,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	req.Tool = strings.TrimSpace(req.Tool)
	if req.Tool == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tool 必填"})
		return
	}
	req.ArgsHash = strings.TrimSpace(req.ArgsHash)
	if req.ArgsHash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "args_hash 必填（写操作审批须绑定参数摘要，禁止空哈希万能令牌）"})
		return
	}
	a := s.aiGov.issueWriteApproval(s.actorName(r), req.Tool, req.ArgsHash, req.TTLSec)
	if s.aiGov != nil {
		s.aiGov.recordTool(aiToolAuditEntry{
			Actor: s.actorName(r), Tool: req.Tool, Action: "issue_approval",
			Approved: true, Detail: "approval_id=" + a.ID + " args_hash=" + req.ArgsHash,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"approval_id": a.ID, "tool": a.Tool, "args_hash": a.ArgsHash,
		"expires_at": a.ExpiresAt, "ttl_sec": a.ExpiresAt - a.CreatedAt,
	})
}

// exportAIToolAuditEntry forwards AI write-tool actions to the audit-export pipeline.
func (s *Server) exportAIToolAuditEntry(e aiToolAuditEntry) {
	if s == nil {
		return
	}
	level := "info"
	if e.Blocked {
		level = "warning"
	}
	action := e.Action
	if action == "" {
		action = e.Tool
	}
	msg := "AI工具审计 " + e.Tool + " " + action
	if e.Blocked {
		msg += " [blocked]"
	} else if e.Approved {
		msg += " [approved]"
	}
	if e.Detail != "" {
		msg += ": " + truncateRun(e.Detail, 200)
	}
	entry := LogEntry{
		Timestamp: e.Timestamp,
		Kind:      KindOperation,
		Level:     level,
		Actor:     e.Actor,
		Host:      e.HostID,
		Message:   msg,
	}
	s.exportAuditEntry(entry)
}
