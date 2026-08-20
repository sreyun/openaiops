package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---- custom checks ----

func (s *Server) handleGetChecks(w http.ResponseWriter, r *http.Request) {
	checks := s.cfg.Checks()
	st := s.checks.snapshot()
	out := make([]map[string]any, 0, len(checks)+1)

	// Built-in self health-check is always first
	selfEntry := map[string]any{
		"id": selfCheckID, "name": SelfCheckName(), "type": "http",
		"target":       "http://127.0.0.1:" + portFromAddr(s.checks.selfAddr) + "/healthz",
		"interval_sec": 30, "level": "critical", "enabled": true,
		"ok": true, "message": "", "checked_at": int64(0), "latency_ms": 0.0,
		"builtin": true,
	}
	if s2, ok := st[selfCheckID]; ok {
		selfEntry["ok"], selfEntry["message"], selfEntry["checked_at"], selfEntry["latency_ms"] = s2.OK, s2.Message, s2.CheckedAt, s2.LatencyMs
	}
	out = append(out, selfEntry)

	for _, c := range checks {
		m := map[string]any{
			"id": c.ID, "name": c.Name, "type": c.Type, "target": c.Target,
			"interval_sec": c.IntervalSec, "level": c.Level, "enabled": c.Enabled,
			"timeout_sec": c.TimeoutSec,
			"ok":          true, "message": "", "checked_at": int64(0), "latency_ms": 0.0,
			"status_code": 0, "cert_days": -1, "loss_pct": -1.0,
		}
		if s2, ok := st[c.ID]; ok {
			m["ok"], m["message"], m["checked_at"], m["latency_ms"] = s2.OK, s2.Message, s2.CheckedAt, s2.LatencyMs
			m["status_code"], m["cert_days"], m["loss_pct"] = s2.StatusCode, s2.CertDays, s2.LossPct
		}
		// HTTP 高级模式配置（供编辑表单回填）
		m["advanced"] = c.Advanced
		if c.Advanced {
			m["method"], m["headers"], m["body"] = c.Method, c.Headers, c.Body
			m["expect_status"], m["expect_keyword"], m["keyword_is_regex"] = c.ExpectStatus, c.ExpectKeyword, c.KeywordIsRegex
			m["json_path"], m["json_expect"], m["cert_warn_days"] = c.JSONPath, c.JSONExpect, c.CertWarnDays
		}
		if c.Type == "dns" { // DNS 拨测配置回填（记录类型 + 期望值），否则编辑时会丢失
			m["dns_type"], m["expect_keyword"] = c.DNSType, c.ExpectKeyword
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUpsertCheck(w http.ResponseWriter, r *http.Request) {
	var c CustomCheck
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	c.Target = strings.TrimSpace(c.Target)
	if c.Name == "" || c.Target == "" || (c.Type != "http" && c.Type != "tcp" && c.Type != "ping" && c.Type != "udp" && c.Type != "process" && c.Type != "dns") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "check_api.invalid_params")})
		return
	}
	if c.IntervalSec < 5 {
		c.IntervalSec = 30
	}
	if c.Level != "warning" && c.Level != "critical" {
		c.Level = "critical"
	}
	saved, err := s.cfg.UpsertCheck(c)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.checks.runNow(saved.ID)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Message: Tz("log.save_check", saved.Name)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": saved.ID})
}

// handleRunCheck triggers one immediate probe of a check (fire-and-forget).
func (s *Server) handleRunCheck(w http.ResponseWriter, r *http.Request) {
	s.checks.runNow(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCheckHistory returns a check's recorded trend series (latency / status /
// loss over time) for the history-curve view.
func (s *Server) handleCheckHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// 优先从 VM 读取（持久化，服务重启后历史仍在）；VM 无数据时回落到本次会话的内存环。
	var pts []CheckPoint
	to := time.Now().Unix()
	from := to - 24*3600 // 默认最近 24h
	if m := r.URL.Query().Get("since_min"); m != "" {
		if v, _ := strconv.Atoi(m); v > 0 {
			from = to - int64(v)*60
		}
	}
	if fs := r.URL.Query().Get("from"); fs != "" {
		if v, err := strconv.ParseInt(fs, 10, 64); err == nil && v > 0 {
			from = v
		}
	}
	if ts := r.URL.Query().Get("to"); ts != "" {
		if v, err := strconv.ParseInt(ts, 10, 64); err == nil && v > 0 {
			to = v
		}
	}
	if s.vm != nil && s.vm.enabled() {
		pts = s.vm.queryCheckHistory(id, from, to)
	}
	if len(pts) == 0 {
		pts = s.checks.HistoryOf(id)
	}
	// Client-side filter for in-memory fallback so custom ranges still clip.
	if len(pts) > 0 && (r.URL.Query().Get("from") != "" || r.URL.Query().Get("since_min") != "") {
		clipped := pts[:0]
		for _, p := range pts {
			if p.Ts >= from && p.Ts <= to {
				clipped = append(clipped, p)
			}
		}
		pts = clipped
	}
	if pts == nil {
		pts = []CheckPoint{}
	}
	writeJSON(w, http.StatusOK, pts)
}

func (s *Server) handleDeleteCheck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.cfg.DeleteCheck(id)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Message: Tz("log.delete_check", id)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
