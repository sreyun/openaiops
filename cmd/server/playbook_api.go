package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// modulePrefix 标识一条「内置模块调用」封套命令，Agent 端识别后按系统执行对应模块。
const modulePrefix = "__AIOPS_MODULE__"

// playbookHostVars 预置一台主机的内置变量（供 {{名}} 引用与 when 条件求值）。
// 除 GOOS 外还暴露 Platform / 发行版 ID / 主版本，便于 Rocky 9/10、麒麟 V10/V11 条件分支。
func playbookHostVars(h *Host) map[string]string {
	d := hostDistro(h)
	os := strings.ToLower(h.OS)
	if os == "macos" || os == "osx" || os == "mac" {
		os = "darwin"
	}
	return map[string]string{
		"host_id":        h.ID,
		"hostname":       h.Hostname,
		"ip":             h.IP,
		"os":             os,
		"arch":           strings.ToLower(strings.TrimSpace(h.Arch)),
		"platform":       h.Platform,
		"os_family":      d.Family,
		"distro":         d.ID,
		"distro_id":      d.ID,
		"distro_version": d.Version,
		"category":       h.Category,
	}
}

var pbVarRE = regexp.MustCompile(`\{\{\s*([a-zA-Z_]\w*)\s*\}\}`)

// substitutePlaybookVars 把 {{ 变量 }} 替换为 vars 中的值（未知变量替为空串）。
func substitutePlaybookVars(s string, vars map[string]string) string {
	return pbVarRE.ReplaceAllStringFunc(s, func(m string) string {
		return vars[pbVarRE.FindStringSubmatch(m)[1]]
	})
}

// evalPlaybookWhen 求值 when 条件。支持：
//
//	a == b / a != b（macos 与 darwin 视为同一 OS）
//	a contains b（子串，大小写不敏感）
//	a >= b / <= / > / <（两侧均可解析为数字时按数值比较）
//	否则按真值（空 / false / 0 / no / off = 假）
func evalPlaybookWhen(when string, vars map[string]string) bool {
	when = strings.TrimSpace(substitutePlaybookVars(when, vars))
	if when == "" {
		return false
	}
	lower := strings.ToLower(when)
	for _, op := range []string{"contains", ">=", "<=", "==", "!=", ">", "<"} {
		if i := strings.Index(lower, op); i >= 0 {
			// Re-slice on original to preserve values; op length from lower match.
			left := strings.TrimSpace(when[:i])
			right := strings.TrimSpace(when[i+len(op):])
			switch op {
			case "contains":
				return strings.Contains(strings.ToLower(left), strings.ToLower(right))
			case "==":
				return whenValuesEqual(left, right)
			case "!=":
				return !whenValuesEqual(left, right)
			case ">=", "<=", ">", "<":
				lf, lerr := strconv.ParseFloat(left, 64)
				rf, rerr := strconv.ParseFloat(right, 64)
				if lerr != nil || rerr != nil {
					return false
				}
				switch op {
				case ">=":
					return lf >= rf
				case "<=":
					return lf <= rf
				case ">":
					return lf > rf
				case "<":
					return lf < rf
				}
			}
		}
	}
	switch strings.ToLower(when) {
	case "", "false", "0", "no", "off":
		return false
	}
	return true
}

func whenValuesEqual(a, b string) bool {
	if strings.TrimSpace(a) == strings.TrimSpace(b) {
		return true
	}
	return normalizeWhenToken(a) == normalizeWhenToken(b)
}

func normalizeWhenToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "macos", "osx", "mac":
		return "darwin"
	case "rockylinux":
		return "rocky"
	case "kylinos", "neokylin":
		return "kylin"
	case "redhat":
		return "rhel"
	case "almalinux":
		return "alma"
	case "euler":
		return "euleros"
	case "alibaba", "alibabacloudlinux":
		return "alinux"
	case "amazon", "amazonlinux":
		return "amzn"
	}
	return s
}

// resolvePlaybookCommand 决定某步在一台主机上实际执行的命令：
// 模块 > 分系统覆盖 > 默认命令，最后做 {{变量}} 替换。
func resolvePlaybookCommand(step PlaybookStep, h *Host, vars map[string]string) string {
	if step.Module != "" {
		return buildModuleCommand(step.Module, step.Args, vars)
	}
	cmd := step.Command
	switch normalizeWhenToken(h.OS) {
	case "windows":
		if strings.TrimSpace(step.CommandWin) != "" {
			cmd = step.CommandWin
		}
	case "darwin":
		if strings.TrimSpace(step.CommandMac) != "" {
			cmd = step.CommandMac
		}
	}
	return substitutePlaybookVars(cmd, vars)
}

// resolvePlaybookRollback returns an explicit rollback command for the host OS.
// There is no inferred rollback: operators must define a deterministic inverse
// for each step that is safe to compensate.
func resolvePlaybookRollback(step PlaybookStep, h *Host, vars map[string]string) string {
	cmd := step.Rollback
	switch normalizeWhenToken(h.OS) {
	case "windows":
		if strings.TrimSpace(step.RollbackWin) != "" {
			cmd = step.RollbackWin
		}
	case "darwin":
		if strings.TrimSpace(step.RollbackMac) != "" {
			cmd = step.RollbackMac
		}
	}
	return substitutePlaybookVars(cmd, vars)
}

// buildModuleCommand 把模块调用编码成 Agent 可识别的封套命令：
//
//	__AIOPS_MODULE__ {"module":"...","args":{...}}
//
// 复用现有 exec 通道与退出码机制，Agent 端按系统执行内置模块。
func buildModuleCommand(module string, args map[string]string, vars map[string]string) string {
	sub := make(map[string]string, len(args))
	for k, v := range args {
		sub[k] = substitutePlaybookVars(v, vars)
	}
	payload, _ := json.Marshal(map[string]any{"module": module, "args": sub})
	return modulePrefix + " " + string(payload)
}

// -----------------------------------------------------------------------
// Playbook (automation) handlers
// -----------------------------------------------------------------------

func (s *Server) handleListPlaybooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.playbooks.List())
}

func (s *Server) handleUpsertPlaybook(w http.ResponseWriter, r *http.Request) {
	var p Playbook
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	saved, err := s.playbooks.Upsert(p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rev := 0
	if s.pg != nil {
		if n, e := s.pg.savePlaybookRevision(saved, s.actorName(r)); e == nil {
			rev = n
		}
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.save_playbook", saved.Name)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": saved.ID, "rev": rev})
}

func (s *Server) handleDeletePlaybook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.playbooks.Delete(id)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.delete_playbook", id)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleExecutePlaybook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pb, ok := s.playbooks.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "playbook.not_found")})
		return
	}
	preflight := s.buildPlaybookPreflight(pb)
	if !preflight.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "剧本确定性预检未通过", "preflight": preflight})
		return
	}
	if preflight.RequiresApproval && !strings.EqualFold(r.Header.Get("X-AIOps-Risk-Accepted"), "true") {
		errMsg := "高风险剧本需要显式确认"
		if preflight.FreezeActive {
			errMsg = "变更冻结期内执行剧本需要显式确认"
			if preflight.FreezeWindow != nil && strings.TrimSpace(preflight.FreezeWindow.Name) != "" {
				errMsg = "变更冻结「" + strings.TrimSpace(preflight.FreezeWindow.Name) + "」期内执行剧本需要显式确认"
			}
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": errMsg, "preflight": preflight,
		})
		return
	}
	targetList := s.filterHostsForUser(r, s.onlinePlaybookTargets(pb))
	if len(targetList) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "playbook.no_target")})
		return
	}
	exec := s.playbooks.StartExecution(pb, s.actorName(r), targetList)
	s.persistPlaybookExecution(exec.ID)
	// Run each step on each host sequentially via the agent reverse terminal channel
	go s.runPlaybookExecution(pb, exec, targetList)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.execute_playbook", pb.Name, len(targetList))})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "execution_id": exec.ID})
}

type playbookPreflightStep struct {
	Name           string `json:"name"`
	Target         string `json:"target"`
	OnlineTargets  int    `json:"online_targets"`
	OfflineTargets int    `json:"offline_targets"`
	Risk           string `json:"risk"` // read_only | command | change
	HasRollback    bool   `json:"has_rollback"`
}

type playbookPreflight struct {
	Valid            bool                    `json:"valid"`
	RiskLevel        string                  `json:"risk_level"` // low | medium | high
	RequiresApproval bool                    `json:"requires_approval"`
	OnlineTargets    int                     `json:"online_targets"`
	OfflineTargets   int                     `json:"offline_targets"`
	MaxParallel      int                     `json:"max_parallel"`
	AutoRollback     bool                    `json:"auto_rollback"`
	Warnings         []string                `json:"warnings"`
	Steps            []playbookPreflightStep `json:"steps"`
	FreezeActive     bool                    `json:"freeze_active,omitempty"`
	FreezeWindow     *ChangeWindow           `json:"freeze_window,omitempty"`
}

// handlePlaybookPreflight provides a deterministic, non-AI execution plan:
// target reachability, policy risk, concurrency and rollback coverage.
func (s *Server) handlePlaybookPreflight(w http.ResponseWriter, r *http.Request) {
	pb, ok := s.playbooks.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "playbook.not_found")})
		return
	}
	writeJSON(w, http.StatusOK, s.buildPlaybookPreflight(pb))
}

func (s *Server) buildPlaybookPreflight(pb Playbook) playbookPreflight {
	out := playbookPreflight{
		Valid: true, RiskLevel: "low", MaxParallel: pb.Strategy.MaxParallel,
		AutoRollback: pb.Strategy.AutoRollback, Warnings: []string{},
	}
	if out.MaxParallel <= 0 {
		out.MaxParallel = playbookMaxParallel
	}
	if err := validatePlaybookCommands(pb.Steps, s.cfg.CmdPolicy()); err != nil {
		out.Valid = false
		out.Warnings = append(out.Warnings, err.Error())
	}
	if err := validatePlaybookVariables(pb.Steps); err != nil {
		out.Valid = false
		out.Warnings = append(out.Warnings, err.Error())
	}

	allHosts := s.store.ListHosts()
	offlineSec := int64(s.cfg.Thresholds().OfflineAfter.Seconds())
	now := time.Now().Unix()
	onlineHosts := make([]*Host, 0, len(allHosts))
	for _, h := range allHosts {
		if now-h.LastSeen <= offlineSec {
			onlineHosts = append(onlineHosts, h)
		}
	}
	onlineUnion := map[string]bool{}
	offlineUnion := map[string]bool{}
	for _, st := range pb.Steps {
		allMatched := s.playbooks.ResolveTargets(st.Target, allHosts)
		onlineMatched := s.playbooks.ResolveTargets(st.Target, onlineHosts)
		onlineSet := map[string]bool{}
		for _, h := range onlineMatched {
			onlineSet[h.ID] = true
			onlineUnion[h.ID] = true
		}
		for _, h := range allMatched {
			if !onlineSet[h.ID] {
				offlineUnion[h.ID] = true
			}
		}
		risk := "read_only"
		if st.Module == "" {
			risk = "command"
			if out.RiskLevel == "low" {
				out.RiskLevel = "medium"
			}
		} else if meta, ok := knownPlaybookModules[st.Module]; !ok || !meta.ReadOnly {
			risk = "change"
			out.RiskLevel = "high"
		}
		for _, cmd := range []string{st.Command, st.CommandWin, st.CommandMac} {
			if strings.TrimSpace(cmd) == "" {
				continue
			}
			_, force, _ := evaluatePlaybookCommand(cmd, s.cfg.CmdPolicy())
			if force {
				risk = "change"
				out.RiskLevel = "high"
			}
		}
		hasRollback := strings.TrimSpace(st.Rollback) != "" ||
			strings.TrimSpace(st.RollbackWin) != "" || strings.TrimSpace(st.RollbackMac) != ""
		if pb.Strategy.AutoRollback && risk == "change" && !hasRollback {
			out.Warnings = append(out.Warnings, fmt.Sprintf("变更步骤 %q 未配置显式回滚", st.Name))
		}
		out.Steps = append(out.Steps, playbookPreflightStep{
			Name: st.Name, Target: st.Target, OnlineTargets: len(onlineMatched),
			OfflineTargets: len(allMatched) - len(onlineMatched), Risk: risk, HasRollback: hasRollback,
		})
	}
	out.OnlineTargets = len(onlineUnion)
	out.OfflineTargets = len(offlineUnion)
	out.RequiresApproval = out.RiskLevel == "high" || playbookNeedsForcedApproval(pb.Steps, s.cfg.CmdPolicy())
	// Change freeze: any online target under an active freeze window requires explicit ack.
	if s.cfg != nil {
		nowFreeze := time.Now().Unix()
		for id := range onlineUnion {
			cat := ""
			for _, h := range allHosts {
				if h.ID == id {
					cat = h.Category
					break
				}
			}
			if w, ok := s.cfg.activeFreezeWindow(id, cat, nowFreeze); ok {
				out.FreezeActive = true
				cp := w
				out.FreezeWindow = &cp
				out.RequiresApproval = true
				name := strings.TrimSpace(w.Name)
				if name == "" {
					name = w.ID
				}
				out.Warnings = append(out.Warnings, "变更冻结中："+name+"（禁止未确认直跑）")
				break
			}
		}
	}
	if out.OnlineTargets == 0 {
		out.Valid = false
		out.Warnings = append(out.Warnings, "当前没有可执行的在线目标")
	}
	if out.OfflineTargets > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d 台离线目标将在执行时跳过", out.OfflineTargets))
	}
	return out
}

// onlinePlaybookTargets resolves the unique set of ONLINE target hosts across all
// of a playbook's steps. Offline hosts have no reachable agent, so including them
// would always fail — they are filtered out up front.
func (s *Server) onlinePlaybookTargets(pb Playbook) []*Host {
	offlineSec := int64(s.cfg.Thresholds().OfflineAfter.Seconds())
	nowUnix := time.Now().Unix()
	hosts := make([]*Host, 0)
	for _, h := range s.store.ListHosts() {
		if nowUnix-h.LastSeen <= offlineSec {
			hosts = append(hosts, h)
		}
	}
	targetSet := map[string]*Host{}
	for _, step := range pb.Steps {
		for _, h := range s.playbooks.ResolveTargets(step.Target, hosts) {
			targetSet[h.ID] = h
		}
	}
	targetList := make([]*Host, 0, len(targetSet))
	for _, h := range targetSet {
		targetList = append(targetList, h)
	}
	return targetList
}

// runScheduler is the timed-trigger loop: every tick it fires any playbooks whose
// schedule is due. It runs for the life of the process.
func (s *Server) runScheduler(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for now := range t.C {
		for _, pb := range s.playbooks.dueSchedules(now) {
			s.fireScheduledPlaybook(pb)
		}
	}
}

// fireScheduledPlaybook runs one scheduled execution, clearing the in-flight guard
// when it finishes so the next occurrence can fire.
//
// High-risk / change-module playbooks never auto-execute: they enter
// pending_approval (same bar as manual X-AIOps-Risk-Accepted) so schedule cannot
// bypass the human gate that handleExecutePlaybook enforces.
func (s *Server) fireScheduledPlaybook(pb Playbook) {
	hosts := s.onlinePlaybookTargets(pb)
	if len(hosts) == 0 {
		s.playbooks.clearSchedBusy(pb.ID)
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: "scheduler", Message: Tz("log.sched_no_target", pb.Name)})
		return
	}
	// Scheduled runs have no human ack token — skip entirely during freeze.
	if s.cfg != nil {
		now := time.Now().Unix()
		for _, h := range hosts {
			if w, ok := s.cfg.activeFreezeWindow(h.ID, h.Category, now); ok {
				s.playbooks.clearSchedBusy(pb.ID)
				name := strings.TrimSpace(w.Name)
				if name == "" {
					name = w.ID
				}
				s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: "scheduler",
					Message: fmt.Sprintf("定时剧本「%s」因变更冻结「%s」跳过", pb.Name, name)})
				return
			}
		}
	}
	preflight := s.buildPlaybookPreflight(pb)
	if !preflight.Valid {
		s.playbooks.clearSchedBusy(pb.ID)
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: "scheduler",
			Message: fmt.Sprintf("定时剧本「%s」预检未通过，已跳过", pb.Name)})
		return
	}
	// Default: non-readonly / high-risk scheduled runs require approval (cannot silent-fire).
	if preflight.RequiresApproval {
		note := "高风险/变更类定时剧本需人工审批后方可执行"
		if preflight.FreezeActive {
			note = "变更冻结期内定时剧本需人工审批"
		}
		exec := s.playbooks.StartPendingExecution(pb, Tz("playbook.scheduler_actor"), hosts, note)
		s.persistPlaybookExecution(exec.ID)
		// Keep schedBusy until approve/reject so the next tick cannot pile up
		// duplicate pending_approval executions for the same playbook.
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: "scheduler",
			Message: fmt.Sprintf("定时剧本「%s」待审批（execution=%d，目标 %d 台）", pb.Name, exec.ID, len(hosts))})
		s.notifyPlaybookPendingApproval(pb, exec)
		return
	}
	exec := s.playbooks.StartScheduledExecution(pb, Tz("playbook.scheduler_actor"), hosts)
	s.persistPlaybookExecution(exec.ID)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: "scheduler", Message: Tz("log.sched_fire", pb.Name, len(hosts))})
	go func() {
		s.runPlaybookExecution(pb, exec, hosts)
		s.playbooks.clearSchedBusy(pb.ID)
	}()
}

func (s *Server) notifyPlaybookPendingApproval(pb Playbook, exec *PlaybookExecution) {
	if s == nil || exec == nil {
		return
	}
	msg := fmt.Sprintf("定时剧本「%s」等待审批（execution=%d）。请在编排执行历史中批准或拒绝。", pb.Name, exec.ID)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: "scheduler", Message: msg})
	s.store.AddAlertRecord(AlertRecord{
		Key:     fmt.Sprintf("playbook_pending:%d", exec.ID),
		Type:    "playbook_pending_approval",
		Level:   "warning",
		Message: msg,
		FiredAt: time.Now().Unix(),
	})
	if s.notifier != nil {
		s.notifier.PushAdhoc("warning", "剧本待审批", msg, nil)
	}
}

func (s *Server) handleApprovePlaybookExecution(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	exec, ok := s.playbooks.GetExecution(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "execution not found"})
		return
	}
	if exec.Status != "pending_approval" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "仅待审批的执行可批准"})
		return
	}
	pb, ok := s.playbooks.Get(exec.PlaybookID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "playbook.not_found")})
		return
	}
	hosts := s.onlinePlaybookTargets(pb)
	if len(hosts) == 0 {
		s.playbooks.FinishExecution(id, "failed")
		s.persistPlaybookExecution(id)
		s.playbooks.clearSchedBusy(pb.ID)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "playbook.no_target")})
		return
	}
	actor := s.actorName(r)
	s.playbooks.SetExecutionStatus(id, "running", false)
	// Refresh host results for currently online targets.
	for _, h := range hosts {
		s.playbooks.UpdateHostResult(id, h.ID, HostExecResult{Hostname: h.Hostname, Status: "pending"})
	}
	s.persistPlaybookExecution(id)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: actor, IP: s.clientIP(r),
		Message: fmt.Sprintf("批准定时剧本执行「%s」(execution=%d)", pb.Name, id)})
	go func() {
		fresh, _ := s.playbooks.GetExecution(id)
		s.runPlaybookExecution(pb, &fresh, hosts)
		s.playbooks.clearSchedBusy(pb.ID)
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "execution_id": id})
}

func (s *Server) handleRejectPlaybookExecution(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	exec, ok := s.playbooks.GetExecution(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "execution not found"})
		return
	}
	if exec.Status != "pending_approval" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "仅待审批的执行可拒绝"})
		return
	}
	s.playbooks.FinishExecution(id, "rejected")
	s.persistPlaybookExecution(id)
	s.playbooks.clearSchedBusy(exec.PlaybookID)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: fmt.Sprintf("拒绝定时剧本执行「%s」(execution=%d)", exec.PlaybookName, id)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// execPickupTimeout bounds how long a summoned agent has to attach before we
// declare a no-pickup. Covers a full long-poll cycle, busy-agent queueing, and
// WAN/VPN jitter — 40s was too aggressive and caused false no-pickup retries.
// 变量而非常量：waitAgentPickup 的分支要在测试里跑完整条路径，90s 的等待没法测。
var execPickupTimeout = 90 * time.Second

const (
	// playbookMaxAttempts is the total number of tries per step per host: 1 initial
	// + retries. Only infrastructure-class failures (no-pickup/timeout/abnormal) are
	// retried; a genuine non-zero command exit is never retried.
	playbookMaxAttempts = 3
	// playbookRetryBackoff is multiplied by the attempt number for a linear backoff
	// between retries, giving a briefly-unreachable agent time to recover.
	playbookRetryBackoff = 2 * time.Second
	// playbookMaxParallel caps how many hosts run concurrently so a large fleet
	// doesn't get summoned in one thundering herd.
	playbookMaxParallel = 30
)

// runPlaybookExecution runs playbook steps on all target hosts in parallel
// (bounded by playbookMaxParallel). Each host gets a one-shot terminal session
// per step; infrastructure-class failures are retried automatically.
func (s *Server) runPlaybookExecution(pb Playbook, exec *PlaybookExecution, hosts []*Host) {
	// Defense in depth: re-check command policy at execution time (Upsert already validates).
	if err := validatePlaybookCommands(pb.Steps, s.cfg.CmdPolicy()); err != nil {
		s.playbooks.FinishExecution(exec.ID, "failed")
		s.persistPlaybookExecution(exec.ID)
		slog.Warn("playbook blocked by cmd policy", "exec", exec.ID, "err", err)
		return
	}
	ctx := s.registerPlaybookRun(exec.ID)
	defer s.unregisterPlaybookRun(exec.ID)

	var wg sync.WaitGroup
	maxParallel := pb.Strategy.MaxParallel
	if maxParallel <= 0 {
		maxParallel = playbookMaxParallel
	}
	if maxParallel > 100 {
		maxParallel = 100
	}
	// Heavy modules (deep inspect / security scan) open large agent sessions —
	// align with hostInspectConcLimit to avoid thundering herd + UI/DB stalls.
	if playbookHasHeavySteps(pb.Steps) && maxParallel > hostInspectConcLimit {
		maxParallel = hostInspectConcLimit
	}
	sem := make(chan struct{}, maxParallel) // per-playbook bounded fleet concurrency
	for _, h := range hosts {
		wg.Add(1)
		go func(h *Host) {
			defer wg.Done()
			if ctx.Err() != nil {
				s.playbooks.UpdateHostResult(exec.ID, h.ID, HostExecResult{
					Hostname: h.Hostname, Status: "cancelled", Reason: "cancelled",
					Steps: []StepResult{{Name: "（未开始）", Status: "cancelled", Output: "（剧本已手动停止，未向主机下发任务）"}},
				})
				return
			}
			select {
			case <-ctx.Done():
				s.playbooks.UpdateHostResult(exec.ID, h.ID, HostExecResult{
					Hostname: h.Hostname, Status: "cancelled", Reason: "cancelled",
					Steps: []StepResult{{Name: "（未开始）", Status: "cancelled", Output: "（剧本已手动停止，未向主机下发任务）"}},
				})
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				s.playbooks.UpdateHostResult(exec.ID, h.ID, HostExecResult{
					Hostname: h.Hostname, Status: "cancelled", Reason: "cancelled",
					Steps: []StepResult{{Name: "（未开始）", Status: "cancelled", Output: "（剧本已手动停止，未向主机下发任务）"}},
				})
				return
			}
			result := HostExecResult{Hostname: h.Hostname, Status: "running"}
			// Progressive status: leave「等待中」as soon as the host goroutine starts.
			s.playbooks.UpdateHostResult(exec.ID, h.ID, result)
			s.persistPlaybookExecutionDebounced(exec.ID, false)
			vars := playbookHostVars(h) // 变量存储：预置主机 facts，register 逐步累加
			type rollbackAction struct {
				step PlaybookStep
				cmd  string
			}
			var rollbacks []rollbackAction
			pushProgress := func() {
				s.playbooks.UpdateHostResult(exec.ID, h.ID, result)
				s.persistPlaybookExecutionDebounced(exec.ID, false)
			}
			for _, step := range pb.Steps {
				if ctx.Err() != nil {
					result.Status = "cancelled"
					result.Reason = "cancelled"
					result.Steps = append(result.Steps, StepResult{
						Name: step.Name, Status: "cancelled", Output: "（剧本已手动停止）",
					})
					pushProgress()
					break
				}
				sr := StepResult{Name: step.Name, Status: "running"}
				start := time.Now()
				// when 条件：不满足则跳过本步
				if step.When != "" && !evalPlaybookWhen(step.When, vars) {
					sr.Status = "skipped"
					sr.Output = "（when 条件不满足，已跳过）"
					sr.Duration = time.Since(start).Milliseconds()
					if result.Reason == "" {
						result.Reason = "skipped_when"
					}
					result.Steps = append(result.Steps, sr)
					pushProgress()
					continue
				}
				// 解析最终命令：模块 > 分系统覆盖 > 默认，并做 {{变量}} 替换
				cmd := resolvePlaybookCommand(step, h, vars)
				if strings.TrimSpace(cmd) == "" {
					sr.Status = "skipped"
					sr.Output = "（本系统无对应命令，已跳过）"
					sr.Duration = time.Since(start).Milliseconds()
					result.Steps = append(result.Steps, sr)
					pushProgress()
					continue
				}
				// Retry infrastructure-class failures (agent didn't pick up,
				// timeout, abnormal end) — the usual cause of "some nodes fail"
				// in large batches. A genuine non-zero command exit is NOT retried.
				var output string
				var kind execKind
				var err error
				maxAttempts := step.MaxAttempts
				if maxAttempts <= 0 {
					maxAttempts = playbookMaxAttempts
				}
				if maxAttempts > 6 {
					maxAttempts = 6
				}
				retryDelay := step.RetryDelaySec
				if retryDelay <= 0 {
					retryDelay = int(playbookRetryBackoff / time.Second)
				}
				attemptsUsed := 0
				outCap := 512 * 1024
				if strings.TrimSpace(step.Module) == "host_inspect" {
					outCap = hostInspectOutCap
				}
				cancelled := false
				for attempt := 1; attempt <= maxAttempts; attempt++ {
					if ctx.Err() != nil {
						kind = execCancelled
						err = fmt.Errorf("%s", "剧本已停止")
						output = "（剧本已手动停止）"
						cancelled = true
						break
					}
					attemptsUsed = attempt
					output, kind, err = s.execCommandOnHostCtx(ctx, exec.ID, h, cmd, step.TimeoutSec, outCap)
					if kind == execCancelled || ctx.Err() != nil {
						cancelled = true
						break
					}
					if err == nil {
						if attempt > 1 {
							output += "\n" + Tz("playbook.retry_recovered", attempt)
						}
						break
					}
					if !kind.retryable() && !(step.RetryOnExit && kind == execExit) {
						break // real command failure — retrying is pointless
					}
					if attempt < maxAttempts {
						select {
						case <-ctx.Done():
							cancelled = true
							kind = execCancelled
							err = fmt.Errorf("%s", "剧本已停止")
							output = "（剧本已手动停止）"
						case <-time.After(time.Duration(attempt*retryDelay) * time.Second):
						}
						if cancelled {
							break
						}
						continue
					}
					output += "\n" + Tz("playbook.attempts_failed", attempt)
				}
				sr.Duration = time.Since(start).Milliseconds()
				sr.Attempts = attemptsUsed
				if cancelled {
					sr.Status = "cancelled"
					sr.Output = truncatePlaybookStoreOutput(step.Module, output)
					result.Status = "cancelled"
					result.Reason = "cancelled"
					result.Steps = append(result.Steps, sr)
					pushProgress()
					break
				}
				// ignore_exit：仅「命令跑完但退出码非零」可被忽略（no-pickup/超时等基础设施失败不忽略）
				failed := err != nil
				if failed && step.IgnoreExit && kind == execExit {
					failed = false
					err = nil
					output += "\n（已忽略非零退出码）"
				}
				// register：把本步输出存入变量，供后续步骤 {{名}} 引用
				if step.Register != "" {
					vars[step.Register] = strings.TrimSpace(output)
				}
				if failed {
					sr.Status = "failed"
					sr.Output = truncatePlaybookStoreOutput(step.Module, output+"\n[error] "+err.Error())
					result.Status = "failed"
					result.Reason = execKindReason(kind)
					// Avoid duplicating multi‑MB stdout on the host-level Output field.
					if len(sr.Output) < playbookOutputPreview {
						result.Output += sr.Output + "\n"
					}
					result.Steps = append(result.Steps, sr)
					pushProgress()
					if !step.ContinueErr {
						break
					}
				} else {
					sr.Status = "success"
					fullOut := output
					if strings.TrimSpace(step.Module) == "host_inspect" {
						s.ingestPlaybookHostInspect(pb.Name, exec.ID, h, exec.Operator, fullOut, sr.Duration)
					}
					sr.Output = truncatePlaybookStoreOutput(step.Module, fullOut)
					if len(sr.Output) < playbookOutputPreview {
						result.Output += sr.Output + "\n"
					}
					result.Steps = append(result.Steps, sr)
					pushProgress()
					if rb := strings.TrimSpace(resolvePlaybookRollback(step, h, vars)); rb != "" {
						rollbacks = append(rollbacks, rollbackAction{step: step, cmd: rb})
					}
				}
			}
			// Do not auto-rollback after operator cancel — that would add host load.
			if result.Status == "failed" && pb.Strategy.AutoRollback && len(rollbacks) > 0 && ctx.Err() == nil {
				for i := len(rollbacks) - 1; i >= 0; i-- {
					if ctx.Err() != nil {
						break
					}
					action := rollbacks[i]
					start := time.Now()
					var out string
					var rbKind execKind
					var rbErr error
					rbAttempts := 0
					maxAttempts := action.step.MaxAttempts
					if maxAttempts <= 0 {
						maxAttempts = playbookMaxAttempts
					}
					for attempt := 1; attempt <= maxAttempts; attempt++ {
						if ctx.Err() != nil {
							break
						}
						rbAttempts = attempt
						out, rbKind, rbErr = s.execCommandOnHostCtx(ctx, exec.ID, h, action.cmd, action.step.TimeoutSec, 512*1024)
						if rbErr == nil || !rbKind.retryable() || attempt == maxAttempts {
							break
						}
						delay := action.step.RetryDelaySec
						if delay <= 0 {
							delay = int(playbookRetryBackoff / time.Second)
						}
						select {
						case <-ctx.Done():
						case <-time.After(time.Duration(attempt*delay) * time.Second):
						}
					}
					rb := StepResult{
						Name: "回滚 · " + action.step.Name, Status: "rollback_success",
						Output: out, Duration: time.Since(start).Milliseconds(), Attempts: rbAttempts, Rollback: true,
					}
					if rbErr != nil {
						rb.Status = "rollback_failed"
						rb.Output = strings.TrimSpace(out + "\n[error] " + rbErr.Error())
					}
					result.Steps = append(result.Steps, rb)
					result.Output += rb.Output + "\n"
				}
			}
			if result.Status != "failed" && result.Status != "cancelled" {
				result.Status = "success"
			}
			s.playbooks.UpdateHostResult(exec.ID, h.ID, result)
			s.persistPlaybookExecutionDebounced(exec.ID, true)
		}(h)
	}
	wg.Wait()
	if s.inspect != nil {
		s.inspect.finishPlaybookBatch(playbookInspectBatchID(exec.ID))
	}
	// Re-read host results (local exec snapshot is stale after UpdateHostResult).
	fresh, _ := s.playbooks.GetExecution(exec.ID)
	if fresh.Status == "cancelled" || ctx.Err() != nil {
		// Cancel handler may have already finished; ensure unfinished hosts are marked.
		s.playbooks.CancelUnfinishedHosts(exec.ID)
		s.playbooks.FinishExecution(exec.ID, "cancelled")
		s.persistPlaybookExecutionDebounced(exec.ID, true)
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: exec.Operator, Message: Tz("log.playbook_done", pb.Name, "cancelled")})
		return
	}
	okN, failN := 0, 0
	for _, r := range fresh.HostResults {
		if r.Status == "success" {
			okN++
		} else {
			failN++
		}
	}
	status := "completed"
	if failN > 0 && okN > 0 {
		status = "partial"
	} else if failN > 0 {
		status = "failed"
	}
	s.playbooks.FinishExecution(exec.ID, status)
	s.persistPlaybookExecutionDebounced(exec.ID, true)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: exec.Operator, Message: Tz("log.playbook_done", pb.Name, status)})
	// 学习闭环 B：把执行结果沉淀为经验记忆，全成功则强化——让后续「AI 生成剧本 / 事件诊断」
	// 复用被现实验证有效的自动化做法。异步、尽力而为。
	s.rememberPlaybookOutcome(pb, &fresh, status)
}

func execKindReason(k execKind) string {
	switch k {
	case execNoPickup:
		return "no_pickup"
	case execTimeout:
		return "timeout"
	case execExit:
		return "exit"
	case execAbnormal:
		return "error"
	case execCancelled:
		return "cancelled"
	default:
		return "error"
	}
}

// execKind classifies a single command run so the batch runner can decide
// whether a failure is worth retrying. A non-zero exit code is a genuine command
// failure (retrying is pointless); a timeout / no-pickup / abnormal end is an
// infrastructure hiccup (a retry often recovers it — the root cause of the
// "some nodes fail" complaint in large batches).
type execKind int

const (
	execOK        execKind = iota // ran, exit 0
	execExit                      // ran, non-zero exit — NOT retryable
	execTimeout                   // timed out with partial output — retryable
	execNoPickup                  // timed out with NO output: agent never picked up — retryable
	execAbnormal                  // session ended without an exit marker — retryable
	execCancelled                 // playbook cancelled by operator — NOT retryable
)

// retryable reports whether a failure of this kind is an infrastructure issue
// worth re-attempting (as opposed to a real non-zero command exit).
func (k execKind) retryable() bool {
	return k == execTimeout || k == execNoPickup || k == execAbnormal
}

// execCommandOnHost runs a single command on a host via the Agent reverse terminal
// channel. It creates a one-shot exec session, summons the agent, and streams the
// combined output until the process exits (tx EOF → session done) or the timer
// fires. The outcome is classified via parseExecOutput.
func (s *Server) execCommandOnHost(h *Host, command string, timeoutSec int) (string, execKind, error) {
	return s.execCommandOnHostCtx(context.Background(), 0, h, command, timeoutSec, 512*1024)
}

// parseExecOutput splits the agent's exec result into clean output text and an
// error derived from the trailing "[AIOPS_EXIT]<code>" marker.
func parseExecOutput(output []byte, timedOut bool) (string, execKind, error) {
	s := string(output)
	if idx := strings.LastIndex(s, "[AIOPS_EXIT]"); idx >= 0 {
		code := 0
		fmt.Sscanf(strings.TrimSpace(s[idx+len("[AIOPS_EXIT]"):]), "%d", &code)
		body := strings.TrimRight(s[:idx], "\r\n")
		if code != 0 {
			return body, execExit, fmt.Errorf("%s", Tz("playbook.exit_code", code))
		}
		return body, execOK, nil
	}
	body := strings.TrimRight(s, "\r\n")
	if timedOut {
		// No exit marker + timed out. Empty output means the agent never picked
		// up the summoned session (a pure infrastructure miss, highly retryable);
		// partial output means the command was running but exceeded the timeout.
		if strings.TrimSpace(body) == "" {
			return body, execNoPickup, fmt.Errorf("%s", Tz("playbook.no_pickup"))
		}
		return body, execTimeout, fmt.Errorf("%s", Tz("playbook.timeout"))
	}
	return body, execAbnormal, fmt.Errorf("%s", Tz("playbook.abnormal"))
}

func (s *Server) handleListExecutions(w http.ResponseWriter, r *http.Request) {
	var list []PlaybookExecution
	if s.pg != nil {
		if pgList := s.pg.listPlaybookExecutions(200); len(pgList) > 0 {
			list = pgList
		}
	}
	if len(list) == 0 {
		list = s.playbooks.ExecutionHistory()
	}
	out := make([]PlaybookExecution, 0, len(list))
	for _, e := range list {
		out = append(out, summarizePlaybookExecution(e))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetExecution(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	exec, ok := s.playbooks.GetExecution(id)
	if !ok && s.pg != nil {
		if e, found := s.pg.getPlaybookExecution(id); found {
			exec, ok = e, true
		}
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "playbook.exec_not_found")})
		return
	}
	view := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("view")))
	compact := r.URL.Query().Get("compact") == "1" || view == "compact"
	if compact {
		writeJSON(w, http.StatusOK, compactPlaybookExecution(exec, playbookOutputPreview))
		return
	}
	writeJSON(w, http.StatusOK, exec)
}

func (s *Server) persistPlaybookExecution(id int64) {
	if s == nil || s.pg == nil {
		return
	}
	if e, ok := s.playbooks.GetExecution(id); ok {
		s.pg.upsertPlaybookExecution(e)
	}
}
