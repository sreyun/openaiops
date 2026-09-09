package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Live playbook runs register a cancel func so operators can stop long-running
// fleets without issuing kill scripts on hosts (pending work is dropped; in-flight
// agent exec sessions are closed so agents cancel CommandContext).
var (
	pbRunMu     sync.Mutex
	pbRunCancel = map[int64]context.CancelFunc{}
)

func (s *Server) registerPlaybookRun(execID int64) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	pbRunMu.Lock()
	if old, ok := pbRunCancel[execID]; ok {
		old()
	}
	pbRunCancel[execID] = cancel
	pbRunMu.Unlock()
	return ctx
}

func (s *Server) unregisterPlaybookRun(execID int64) {
	pbRunMu.Lock()
	delete(pbRunCancel, execID)
	pbRunMu.Unlock()
}

// signalPlaybookCancel cancels the run context (if any) and closes tagged exec sessions.
// Returns whether a live runner was signalled and how many sessions were aborted.
func (s *Server) signalPlaybookCancel(execID int64) (signalled bool, sessions int) {
	pbRunMu.Lock()
	cancel, ok := pbRunCancel[execID]
	pbRunMu.Unlock()
	if ok && cancel != nil {
		cancel()
		signalled = true
	}
	if s != nil && s.term != nil {
		sessions = s.term.abortSessionsByExecID(execID)
	}
	return signalled, sessions
}

// abortPlaybookExec stops a live run (context + sessions) and marks the execution cancelled.
// Used by AI diagnostic timeouts so work does not continue after the tool returns.
func (s *Server) abortPlaybookExec(execID int64) {
	if execID <= 0 || s == nil || s.playbooks == nil {
		return
	}
	s.signalPlaybookCancel(execID)
	s.playbooks.CancelUnfinishedHosts(execID)
	s.playbooks.FinishExecution(execID, "cancelled")
	s.persistPlaybookExecution(execID)
}

// handleCancelPlaybookExecution thoroughly stops a running (or pending) execution.
// POST /api/v1/playbooks/executions/by-id/{id}/cancel
func (s *Server) handleCancelPlaybookExecution(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	exec, ok := s.resolvePlaybookExecution(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "playbook.exec_not_found")})
		return
	}
	switch exec.Status {
	case "cancelled":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "cancelled", "already": true})
		return
	case "completed", "failed", "partial", "rejected":
		writeJSON(w, http.StatusConflict, map[string]string{"error": "该执行已结束，无法停止"})
		return
	case "running", "pending_approval":
		// proceed
	default:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前状态不可停止: " + exec.Status})
		return
	}

	signalled, nSess := s.signalPlaybookCancel(id)
	s.playbooks.CancelUnfinishedHosts(id)
	s.playbooks.FinishExecution(id, "cancelled")
	s.playbooks.clearSchedBusy(exec.PlaybookID)
	s.persistPlaybookExecution(id)
	if s.inspect != nil {
		s.inspect.finishPlaybookBatch(playbookInspectBatchID(id))
	}
	actor := s.actorName(r)
	s.store.AddLog(LogEntry{
		Kind: KindOperation, Level: "warning", Actor: actor, IP: s.clientIP(r),
		Message: fmt.Sprintf("手动停止剧本执行「%s」(id=%d，会话中止 %d，live=%v)", exec.PlaybookName, id, nSess, signalled),
	})
	slog.Info("playbook execution cancelled", "id", id, "playbook", exec.PlaybookName, "sessions", nSess, "signalled", signalled, "actor", actor)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "status": "cancelled",
		"sessions_aborted": nSess, "runner_signalled": signalled,
	})
}

// CancelUnfinishedHosts marks pending/running hosts (and their open steps) as cancelled.
func (pm *playbookManager) CancelUnfinishedHosts(execID int64) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for i := range pm.executions {
		if pm.executions[i].ID != execID {
			continue
		}
		for hid, hr := range pm.executions[i].HostResults {
			st := strings.TrimSpace(hr.Status)
			if st == "success" || st == "failed" || st == "timeout" || st == "skipped" || st == "cancelled" {
				// Still cancel a dangling "running" step on an otherwise finished host.
				changed := false
				for j := range hr.Steps {
					if hr.Steps[j].Status == "running" || hr.Steps[j].Status == "" {
						hr.Steps[j].Status = "cancelled"
						if strings.TrimSpace(hr.Steps[j].Output) == "" {
							hr.Steps[j].Output = "（剧本已手动停止）"
						}
						changed = true
					}
				}
				if changed {
					pm.executions[i].HostResults[hid] = hr
				}
				continue
			}
			hr.Status = "cancelled"
			hr.Reason = "cancelled"
			for j := range hr.Steps {
				if hr.Steps[j].Status == "running" || hr.Steps[j].Status == "pending" || hr.Steps[j].Status == "" {
					hr.Steps[j].Status = "cancelled"
					if strings.TrimSpace(hr.Steps[j].Output) == "" {
						hr.Steps[j].Output = "（剧本已手动停止）"
					}
				}
			}
			if len(hr.Steps) == 0 {
				hr.Steps = []StepResult{{Name: "（未开始）", Status: "cancelled", Output: "（剧本已手动停止，未向主机下发后续任务）"}}
			}
			pm.executions[i].HostResults[hid] = hr
		}
		return
	}
}

// ensureExecution upserts an execution into the in-memory ring (for cancel /
// approve / reject of PG-only rows that fell out of the soft-capped history).
func (pm *playbookManager) ensureExecution(e PlaybookExecution) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for i := range pm.executions {
		if pm.executions[i].ID == e.ID {
			pm.executions[i] = e
			return
		}
	}
	pm.executions = append(pm.executions, e)
	if e.ID >= pm.nextExecID {
		pm.nextExecID = e.ID + 1
	}
	pm.trimExecutionsLocked()
}
