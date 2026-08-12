package main

// CI/CD HTTP surface.
//
// Read paths are viewer+; anything that changes a pipeline (trigger / retry /
// cancel) or a stored token is gated in routeAllowed. Every mutating call is
// written to the audit log, because triggering a deploy from the console is an
// action an auditor will want attributed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func cicdCtx(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// resolveCICDConnection loads the connection named by ?connection_id= / body field.
func (s *Server) resolveCICDConnection(id string) (CICDConnection, bool) {
	return s.cfg.GetCICDConnection(strings.TrimSpace(id))
}

// validateCICDConnection normalizes and checks a submitted connection.
func validateCICDConnection(c *CICDConnection) string {
	c.Name = strings.TrimSpace(c.Name)
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.Project = strings.Trim(strings.TrimSpace(c.Project), "/")
	c.Ref = strings.TrimSpace(c.Ref)
	c.PipelinePath = strings.TrimSpace(c.PipelinePath)
	c.CACert = strings.TrimSpace(c.CACert)

	if c.Name == "" {
		return "名称必填"
	}
	switch c.Provider {
	case CICDProviderGitLab, CICDProviderGitHub, CICDProviderGitee:
	default:
		return "提供方仅支持 gitlab / github / gitee"
	}
	if c.Project == "" {
		return "项目路径必填（GitLab: group/repo；GitHub/Gitee: owner/repo）"
	}
	if c.BaseURL != "" && !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		return "实例地址需以 http:// 或 https:// 开头"
	}
	// GitHub/Gitee SaaS paths are owner/repo; GitLab allows nested groups.
	if c.Provider != CICDProviderGitLab && strings.Count(c.Project, "/") != 1 {
		return "项目路径格式应为 owner/repo"
	}
	if c.InsecureSkipTLS && c.CACert != "" {
		return "已提供 CA 证书时不应同时勾选跳过 TLS 校验"
	}
	if c.CACert != "" && !strings.Contains(c.CACert, "BEGIN CERTIFICATE") {
		return "CA 证书需为 PEM 格式"
	}
	return ""
}

// GET /api/v1/cicd/connections
func (s *Server) handleCICDConnectionList(w http.ResponseWriter, r *http.Request) {
	list := s.cfg.ListCICDConnections()
	out := make([]CICDConnection, len(list))
	for i, c := range list {
		out[i] = maskCICDConnection(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

// POST /api/v1/cicd/connections
func (s *Server) handleCICDConnectionCreate(w http.ResponseWriter, r *http.Request) {
	var c CICDConnection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if msg := validateCICDConnection(&c); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	saved, err := s.cfg.AddCICDConnection(c)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info",
		Message: "接入 CI/CD 连接（" + saved.Provider + "）：" + saved.Name + " · " + saved.Project})
	writeJSON(w, http.StatusOK, maskCICDConnection(saved))
}

// PUT /api/v1/cicd/connections/{id}
func (s *Server) handleCICDConnectionUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c CICDConnection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if msg := validateCICDConnection(&c); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if err := s.cfg.UpdateCICDConnection(id, c); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info",
		Message: "更新 CI/CD 连接：" + c.Name})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /api/v1/cicd/connections/{id}
func (s *Server) handleCICDConnectionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.cfg.DeleteCICDConnection(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Message: "删除 CI/CD 连接：" + id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/v1/cicd/connections/test — validate credentials before saving.
// Accepts either a full connection body or {"id":"…"} for a stored one.
func (s *Server) handleCICDConnectionTest(w http.ResponseWriter, r *http.Request) {
	var c CICDConnection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	// Editing an existing connection sends a masked token — fall back to the stored one.
	if stored, ok := s.resolveCICDConnection(c.ID); ok {
		if c.Token == "" || strings.Contains(c.Token, "****") {
			c.Token = stored.Token
		} else {
			c.Token = encryptSecret(c.Token)
		}
		if c.Provider == "" {
			c = stored
		}
	} else if c.Token != "" {
		c.Token = encryptSecret(c.Token)
	}
	if msg := validateCICDConnection(&c); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	ctx, cancel := cicdCtx(r, 25*time.Second)
	defer cancel()
	runs, err := s.ListCICDRuns(ctx, c, 3)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "runs": len(runs),
		"message": fmt.Sprintf("连接成功，读到 %d 条最近流水线记录", len(runs)),
	})
}

// GET /api/v1/cicd/runs?connection_id=&limit=&status=
// Without connection_id it aggregates every enabled connection.
func (s *Server) handleCICDRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 30
	}
	statusFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))

	var targets []CICDConnection
	if id := strings.TrimSpace(r.URL.Query().Get("connection_id")); id != "" {
		c, ok := s.resolveCICDConnection(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "连接不存在"})
			return
		}
		targets = []CICDConnection{c}
	} else {
		for _, c := range s.cfg.ListCICDConnections() {
			if c.Enabled {
				targets = append(targets, c)
			}
		}
	}

	ctx, cancel := cicdCtx(r, 30*time.Second)
	defer cancel()

	runs, errs := s.FetchCICDRunsParallel(ctx, targets, limit)
	for _, c := range targets {
		s.cfg.SetCICDSyncResult(c.ID, errs[c.ID])
	}
	if statusFilter != "" && statusFilter != "all" {
		filtered := runs[:0]
		for _, run := range runs {
			if run.Status == statusFilter {
				filtered = append(filtered, run)
			}
		}
		runs = filtered
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].CreatedAt > runs[j].CreatedAt })
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "errors": errs})
}

// GET /api/v1/cicd/overview — KPI cards for the page header.
func (s *Server) handleCICDOverview(w http.ResponseWriter, r *http.Request) {
	conns := s.cfg.ListCICDConnections()
	enabled := 0
	for _, c := range conns {
		if c.Enabled {
			enabled++
		}
	}
	ctx, cancel := cicdCtx(r, 30*time.Second)
	defer cancel()

	var active []CICDConnection
	for _, c := range conns {
		if c.Enabled {
			active = append(active, c)
		}
	}
	// Same limit as the runs list so both endpoints share one cache entry.
	runs, errs := s.FetchCICDRunsParallel(ctx, active, 30)
	for _, c := range active {
		s.cfg.SetCICDSyncResult(c.ID, errs[c.ID])
	}

	var running, failed, success, total int
	var durSum, durN int64
	for _, run := range runs {
		total++
		switch run.Status {
		case CICDStatusRunning, CICDStatusPending:
			running++
		case CICDStatusFailed:
			failed++
		case CICDStatusSuccess:
			success++
		}
		if run.DurationSec > 0 {
			durSum += run.DurationSec
			durN++
		}
	}
	successRate := 0.0
	if success+failed > 0 {
		successRate = float64(success) * 100 / float64(success+failed)
	}
	avgDur := int64(0)
	if durN > 0 {
		avgDur = durSum / durN
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connections":     len(conns),
		"enabled":         enabled,
		"total_runs":      total,
		"running":         running,
		"failed":          failed,
		"success":         success,
		"success_rate":    successRate,
		"avg_duration_ms": avgDur * 1000,
	})
}

// GET /api/v1/cicd/runs/{id}/jobs?connection_id=
func (s *Server) handleCICDRunJobs(w http.ResponseWriter, r *http.Request) {
	c, ok := s.resolveCICDConnection(r.URL.Query().Get("connection_id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "连接不存在"})
		return
	}
	ctx, cancel := cicdCtx(r, 25*time.Second)
	defer cancel()
	jobs, err := s.ListCICDJobs(ctx, c, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// GET /api/v1/cicd/jobs/{id}/log?connection_id=
func (s *Server) handleCICDJobLog(w http.ResponseWriter, r *http.Request) {
	c, ok := s.resolveCICDConnection(r.URL.Query().Get("connection_id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "连接不存在"})
		return
	}
	ctx, cancel := cicdCtx(r, 35*time.Second)
	defer cancel()
	text, err := s.FetchCICDJobLog(ctx, c, r.PathValue("id"), 64*1024)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": text})
}

type cicdRunAction struct {
	ConnectionID string `json:"connection_id"`
	Ref          string `json:"ref,omitempty"`
	Workflow     string `json:"workflow,omitempty"`
}

// POST /api/v1/cicd/runs/{id}/retry
func (s *Server) handleCICDRunRetry(w http.ResponseWriter, r *http.Request) {
	var in cicdRunAction
	_ = json.NewDecoder(r.Body).Decode(&in)
	c, ok := s.resolveCICDConnection(in.ConnectionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "连接不存在"})
		return
	}
	runID := r.PathValue("id")
	ctx, cancel := cicdCtx(r, 25*time.Second)
	defer cancel()
	if err := s.RetryCICDRun(ctx, c, runID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	InvalidateCICDCache(c.ID)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning",
		Message: "重跑流水线：" + c.Name + " · " + c.Project + " #" + runID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/v1/cicd/runs/{id}/cancel
func (s *Server) handleCICDRunCancel(w http.ResponseWriter, r *http.Request) {
	var in cicdRunAction
	_ = json.NewDecoder(r.Body).Decode(&in)
	c, ok := s.resolveCICDConnection(in.ConnectionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "连接不存在"})
		return
	}
	runID := r.PathValue("id")
	ctx, cancel := cicdCtx(r, 25*time.Second)
	defer cancel()
	if err := s.CancelCICDRun(ctx, c, runID); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	InvalidateCICDCache(c.ID)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning",
		Message: "取消流水线：" + c.Name + " · " + c.Project + " #" + runID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /api/v1/cicd/trigger
func (s *Server) handleCICDTrigger(w http.ResponseWriter, r *http.Request) {
	var in cicdRunAction
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	c, ok := s.resolveCICDConnection(in.ConnectionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "连接不存在"})
		return
	}
	ctx, cancel := cicdCtx(r, 25*time.Second)
	defer cancel()
	if err := s.TriggerCICDRun(ctx, c, in.Ref, in.Workflow); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	InvalidateCICDCache(c.ID)
	ref := in.Ref
	if ref == "" {
		ref = c.Ref
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning",
		Message: "触发流水线：" + c.Name + " · " + c.Project + " @" + ref})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// AI closed loop: failed run → evidence → incident → diagnosis
// ---------------------------------------------------------------------------

// buildCICDEvidence assembles the prompt context for a failed run: run metadata,
// the failing stages, and the tail of the first failing job's log.
func (s *Server) buildCICDEvidence(ctx context.Context, c CICDConnection, run CICDRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【流水线】%s · %s（%s）\n", c.Name, c.Project, c.Provider)
	fmt.Fprintf(&b, "运行 #%v 状态=%s 分支=%s 提交=%s 触发人=%s\n",
		firstNonEmptyAny(run.Number, run.ID), run.Status, run.Ref, shortSHA(run.SHA), run.Actor)
	if run.WebURL != "" {
		fmt.Fprintf(&b, "链接：%s\n", run.WebURL)
	}
	jobs, err := s.ListCICDJobs(ctx, c, run.ID)
	if err != nil {
		fmt.Fprintf(&b, "（阶段明细获取失败：%v）\n", err)
		return b.String()
	}
	var failing []CICDJob
	for _, j := range jobs {
		if j.Status == CICDStatusFailed {
			failing = append(failing, j)
		}
	}
	if len(jobs) > 0 {
		b.WriteString("\n【阶段】\n")
		for _, j := range jobs {
			line := fmt.Sprintf("- %s", j.Name)
			if j.Stage != "" {
				line += " [" + j.Stage + "]"
			}
			line += " → " + j.Status
			if j.DurationSec > 0 {
				line += fmt.Sprintf(" (%ds)", j.DurationSec)
			}
			if j.FailureNote != "" {
				line += " · " + j.FailureNote
			}
			b.WriteString(line + "\n")
		}
	}
	if len(failing) > 0 {
		// Only the first failure matters — later stages usually cascade from it.
		logText, logErr := s.FetchCICDJobLog(ctx, c, failing[0].ID, 12*1024)
		b.WriteString("\n【失败任务日志（尾部）】" + failing[0].Name + "\n")
		if logErr != nil {
			fmt.Fprintf(&b, "（日志获取失败：%v）\n", logErr)
		} else {
			b.WriteString(logText + "\n")
		}
	}
	return b.String()
}

func firstNonEmptyAny(n int64, fallback string) any {
	if n > 0 {
		return n
	}
	return fallback
}

// POST /api/v1/cicd/runs/{id}/diagnose {connection_id, open_incident}
//
// Pulls the failure evidence, optionally opens an SRE incident so the existing
// diagnose → verify → remediate loop owns it, and returns the evidence so the
// console can hand it straight to the AI assistant.
func (s *Server) handleCICDRunDiagnose(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ConnectionID  string `json:"connection_id"`
		OpenIncident  bool   `json:"open_incident"`
		RunStatusHint string `json:"status,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	c, ok := s.resolveCICDConnection(in.ConnectionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "连接不存在"})
		return
	}
	runID := r.PathValue("id")

	ctx, cancel := cicdCtx(r, 40*time.Second)
	defer cancel()

	// Locate the run in the recent list so the evidence carries real metadata.
	run := CICDRun{ConnectionID: c.ID, Provider: c.Provider, Project: c.Project, ID: runID, Status: in.RunStatusHint}
	if list, err := s.CachedCICDRuns(ctx, c, 50); err == nil {
		for _, candidate := range list {
			if candidate.ID == runID {
				run = candidate
				break
			}
		}
	}

	evidence := s.buildCICDEvidence(ctx, c, run)

	resp := map[string]any{"evidence": evidence, "run": run}
	if in.OpenIncident {
		title := fmt.Sprintf("流水线失败：%s · %s #%v", c.Name, run.Ref, firstNonEmptyAny(run.Number, run.ID))
		key := fmt.Sprintf("cicd/%s/%s", c.ID, runID)
		incID, created := s.incidents.raise(key, title, "warning", "CI/CD", "", c.Project, "pipeline")
		if incID > 0 {
			s.incidents.AddEvent(incID, "note", "CI/CD", evidence)
			resp["incident_id"] = incID
			resp["incident_created"] = created
		}
		s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning",
			Message: "流水线失败转事件：" + c.Project + " #" + runID})
	}
	// Feed the failure into the AI memory store so later RAG lookups can reuse it.
	go s.rememberAI("cicd", c.ID+"/"+runID, evidence)
	writeJSON(w, http.StatusOK, resp)
}
