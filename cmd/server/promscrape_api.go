package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// ---- 指标抓取目标 HTTP 端点 ----

// handleScrapeTargets 返回所有抓取目标 + 实时抓取状态（供看板）。
func (s *Server) handleScrapeTargets(w http.ResponseWriter, r *http.Request) {
	targets := s.cfg.ScrapeTargets()
	st := s.scrapes.snapshot()
	out := make([]map[string]any, 0, len(targets))
	for _, t := range targets {
		m := map[string]any{
			"id": t.ID, "name": t.Name, "url": t.URL, "interval_sec": t.IntervalSec,
			"timeout_sec": t.TimeoutSec, "enabled": t.Enabled, "labels": t.Labels, "headers": t.Headers,
			"created_at": t.CreatedAt,
			"ok":         false, "samples": 0, "latency_ms": 0.0, "msg": "", "checked_at": int64(0),
		}
		if s2, ok := st[t.ID]; ok {
			m["ok"], m["samples"], m["latency_ms"], m["msg"], m["checked_at"] = s2.OK, s2.Samples, s2.LatencyMs, s2.Msg, s2.CheckedAt
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": out})
}

func (s *Server) handleUpsertScrapeTarget(w http.ResponseWriter, r *http.Request) {
	var t ScrapeTarget
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	t.Name = strings.TrimSpace(t.Name)
	t.URL = strings.TrimSpace(t.URL)
	if t.Name == "" || t.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "名称与 URL 不能为空"})
		return
	}
	if !strings.HasPrefix(t.URL, "http://") && !strings.HasPrefix(t.URL, "https://") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "URL 需以 http:// 或 https:// 开头"})
		return
	}
	if t.IntervalSec < 5 {
		t.IntervalSec = 30
	}
	if t.TimeoutSec <= 0 {
		t.TimeoutSec = 10
	} else if t.TimeoutSec > 60 {
		t.TimeoutSec = 60
	}
	saved, err := s.cfg.UpsertScrapeTarget(t)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.scrapes.runNow(saved.ID)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Message: "保存指标抓取目标：" + saved.Name})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": saved.ID})
}

func (s *Server) handleDeleteScrapeTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.cfg.DeleteScrapeTarget(id)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Message: "删除指标抓取目标：" + id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRunScrapeTarget(w http.ResponseWriter, r *http.Request) {
	s.scrapes.runNow(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- 路径 B：Prometheus remote_write 接收端点 ----

// handleGetPromWrite 返回 remote_write 接收配置（令牌脱敏）+ 推送路径提示。
func (s *Server) handleGetPromWrite(w http.ResponseWriter, r *http.Request) {
	masked := ""
	if s.cfg.Get().PromWriteToken != "" {
		masked = "****"
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": masked, "path": "/api/v1/prom/write"})
}

// handleSetPromWriteToken 设置/清除 remote_write 令牌（空=禁用接收；脱敏值=保持不变）。
func (s *Server) handleSetPromWriteToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	tok := strings.TrimSpace(req.Token)
	if strings.Contains(tok, "****") { // 脱敏回显值，不改
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := s.cfg.SetPromWriteToken(tok); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Message: "更新 remote_write 接收令牌"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePromRemoteWrite 接收 Prometheus remote_write（Bearer 令牌鉴权），反向代理到 VM 的
// /api/v1/write，由 VM 解 protobuf+snappy 落库——零解码依赖，任何 exporter/telegraf/categraf/
// OTel Collector 都能往这推。此端点在 auth 白名单里（自带令牌鉴权，不走会话）。
func (s *Server) handlePromRemoteWrite(w http.ResponseWriter, r *http.Request) {
	tok := s.cfg.Get().PromWriteToken
	if tok == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "remote_write 接收未启用"})
		return
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "令牌无效"})
		return
	}
	vmc := s.cfg.VMConfig()
	if !vmc.Enabled || vmc.URL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "VictoriaMetrics 未启用"})
		return
	}
	// 直接把请求体**流式**转给 VM，不再先 io.ReadAll 到内存。
	//
	// 这条端点既免限流（apiRateLimitSkip）又是高频写入：原来每个并发请求都可能
	// 在堆上按住 32 MB，几个推送方同时刷一批就足以把一台 2 GB 的控制面推到 OOM，
	// 而这些字节我们一个也不解析——纯粹是搬运。
	const promWriteMaxBytes = 32 << 20
	if r.ContentLength > promWriteMaxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "remote_write 请求体超过 32MB"})
		return
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(vmc.URL, "/")+"/api/v1/write",
		io.LimitReader(r.Body, promWriteMaxBytes))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// 已知长度时原样带上，避免无谓地转成 chunked（部分反代对 chunked 上传更挑剔）；
	// 上游本来就是 chunked（-1）时保持 chunked。
	req.ContentLength = r.ContentLength
	// 透传 remote_write 关键头，VM 据此解 snappy + protobuf
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	req.Header.Set("Content-Encoding", r.Header.Get("Content-Encoding"))
	if v := r.Header.Get("X-Prometheus-Remote-Write-Version"); v != "" {
		req.Header.Set("X-Prometheus-Remote-Write-Version", v)
	}
	resp, err := s.vm.httpc.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "转发 VM 失败：" + err.Error()})
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	w.WriteHeader(resp.StatusCode) // 透传 VM 状态（成功通常 204 No Content）
}
