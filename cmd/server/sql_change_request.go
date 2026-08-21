package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"aiops-monitor/cmd/server/sqltoolkit"
)

const sqlChangeApprovalTTL = 30 * time.Minute

type SQLChangeRequest struct {
	ID           string         `json:"id"`
	ConnectionID string         `json:"connection_id"`
	Connection   string         `json:"connection_name,omitempty"`
	Environment  string         `json:"environment"`
	Kind         string         `json:"kind,omitempty"` // ddl|kill
	SQL          string         `json:"sql"`
	Reason       string         `json:"reason,omitempty"`
	Status       string         `json:"status"`
	Proposer     string         `json:"proposer"`
	Approver     string         `json:"approver,omitempty"`
	Executor     string         `json:"executor,omitempty"`
	ChangeID     int64          `json:"change_id,omitempty"` // linked generic ChangeRecord
	CreatedAt    int64          `json:"created_at"`
	ApprovedAt   int64          `json:"approved_at,omitempty"`
	ExpiresAt    int64          `json:"expires_at,omitempty"`
	ExecutedAt   int64          `json:"executed_at,omitempty"`
	Error        string         `json:"error,omitempty"`
	Result       map[string]any `json:"result,omitempty"`
}

type sqlChangeRequestManager struct {
	mu    sync.Mutex
	items map[string]*SQLChangeRequest
	ttl   time.Duration
}

func newSQLChangeRequestManager() *sqlChangeRequestManager {
	return &sqlChangeRequestManager{items: make(map[string]*SQLChangeRequest), ttl: sqlChangeApprovalTTL}
}

func sqlConnectionEnv(c MySQLConnection) string {
	env := strings.ToLower(strings.TrimSpace(c.Env))
	if env == "" {
		return "prod"
	}
	return env
}

func cloneSQLChangeRequest(in *SQLChangeRequest) SQLChangeRequest {
	out := *in
	if in.Result != nil {
		out.Result = make(map[string]any, len(in.Result))
		for k, v := range in.Result {
			out.Result[k] = v
		}
	}
	return out
}

func (m *sqlChangeRequestManager) Create(c MySQLConnection, sqlText, reason, proposer, kind string, now time.Time) SQLChangeRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "ddl"
	}
	cr := &SQLChangeRequest{
		ID:           termID(),
		ConnectionID: c.ID,
		Connection:   c.Name,
		Environment:  sqlConnectionEnv(c),
		Kind:         kind,
		SQL:          strings.TrimSpace(sqlText),
		Reason:       strings.TrimSpace(reason),
		Status:       "pending",
		Proposer:     proposer,
		CreatedAt:    now.Unix(),
	}
	m.items[cr.ID] = cr
	return cloneSQLChangeRequest(cr)
}

func (m *sqlChangeRequestManager) List(now time.Time) []SQLChangeRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SQLChangeRequest, 0, len(m.items))
	for _, cr := range m.items {
		m.expireLocked(cr, now)
		out = append(out, cloneSQLChangeRequest(cr))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// RenameActor rewrites proposer/approver/executor usernames so SoD checks stay
// valid after an account rename (username is the only stable actor key today).
func (m *sqlChangeRequestManager) RenameActor(oldName, newName string) {
	if m == nil || oldName == "" || newName == "" || oldName == newName {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cr := range m.items {
		if cr.Proposer == oldName {
			cr.Proposer = newName
		}
		if cr.Approver == oldName {
			cr.Approver = newName
		}
		if cr.Executor == oldName {
			cr.Executor = newName
		}
	}
}

func (m *sqlChangeRequestManager) SetChangeID(id string, changeID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cr := m.items[id]; cr != nil {
		cr.ChangeID = changeID
	}
}

func (m *sqlChangeRequestManager) Get(id string) (SQLChangeRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cr, ok := m.items[id]
	if !ok || cr == nil {
		return SQLChangeRequest{}, false
	}
	m.expireLocked(cr, time.Now())
	return cloneSQLChangeRequest(cr), true
}

func (m *sqlChangeRequestManager) Approve(id, approver string, now time.Time) (SQLChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cr, ok := m.items[id]
	if !ok {
		return SQLChangeRequest{}, fmt.Errorf("change request not found")
	}
	if cr.Status != "pending" {
		return SQLChangeRequest{}, fmt.Errorf("change request is %s", cr.Status)
	}
	cr.Status = "approved"
	cr.Approver = approver
	cr.ApprovedAt = now.Unix()
	cr.ExpiresAt = now.Add(m.ttl).Unix()
	return cloneSQLChangeRequest(cr), nil
}

func (m *sqlChangeRequestManager) Reject(id, approver string, now time.Time) (SQLChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cr, ok := m.items[id]
	if !ok {
		return SQLChangeRequest{}, fmt.Errorf("change request not found")
	}
	m.expireLocked(cr, now)
	if cr.Status != "pending" && cr.Status != "approved" {
		return SQLChangeRequest{}, fmt.Errorf("change request is %s", cr.Status)
	}
	cr.Status = "rejected"
	cr.Approver = approver
	cr.ExpiresAt = 0
	return cloneSQLChangeRequest(cr), nil
}

// BeginExecute atomically consumes an approval before database I/O. A failed
// database execution remains consumed, preventing unsafe retries with one ticket.
func (m *sqlChangeRequestManager) BeginExecute(id, connectionID, sqlText, executor string, now time.Time) (SQLChangeRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cr, ok := m.items[id]
	if !ok {
		return SQLChangeRequest{}, fmt.Errorf("change request not found")
	}
	m.expireLocked(cr, now)
	if cr.Status != "approved" {
		return SQLChangeRequest{}, fmt.Errorf("change request is %s", cr.Status)
	}
	if cr.ConnectionID != connectionID || cr.SQL != strings.TrimSpace(sqlText) {
		return SQLChangeRequest{}, fmt.Errorf("ticket connection or SQL does not match")
	}
	cr.Status = "executing"
	cr.Executor = executor
	return cloneSQLChangeRequest(cr), nil
}

func (m *sqlChangeRequestManager) FinishExecute(id string, result map[string]any, execErr error, now time.Time) SQLChangeRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cr := m.items[id]
	if cr == nil {
		return SQLChangeRequest{}
	}
	cr.ExecutedAt = now.Unix()
	if execErr != nil {
		cr.Status = "failed"
		cr.Error = execErr.Error()
	} else {
		cr.Status = "executed"
		cr.Result = result
	}
	return cloneSQLChangeRequest(cr)
}

func (m *sqlChangeRequestManager) expireLocked(cr *SQLChangeRequest, now time.Time) {
	if cr.Status == "approved" && cr.ExpiresAt > 0 && now.Unix() >= cr.ExpiresAt {
		cr.Status = "expired"
	}
}

func (s *Server) auditSQLChange(r *http.Request, action string, cr SQLChangeRequest, level string) {
	detail, _ := json.Marshal(map[string]any{
		"action": action, "ticket_id": cr.ID, "connection_id": cr.ConnectionID,
		"environment": cr.Environment, "status": cr.Status, "sql": truncateRunes(cr.SQL, 200),
	})
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: level, Actor: s.actorName(r), IP: s.clientIP(r),
		Message: "sql_change_request " + string(detail)})
}

func (s *Server) handleCreateSQLChangeRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionID string `json:"connection_id"`
		SQL          string `json:"sql"`
		Reason       string `json:"reason"`
		Kind         string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	c, ok := s.cfg.GetMySQLConnection(strings.TrimSpace(req.ConnectionID))
	if !ok || !c.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "connection not found or disabled"})
		return
	}
	sqlText := strings.TrimSpace(req.SQL)
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		if _, ok := parseKillSQL(sqlText); ok {
			kind = "kill"
		} else {
			kind = "ddl"
		}
	}
	switch kind {
	case "kill":
		if _, ok := parseKillSQL(sqlText); !ok || len(sqlText) > 128 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "KILL 变更单格式：KILL <process_id>"})
			return
		}
	case "ddl":
		if driverOf(c) == "postgres" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "PostgreSQL 只读：不支持 DDL 变更单（可用 KILL 终止会话）"})
			return
		}
		if sqlText == "" || len(sqlText) > 16<<10 || !sqltoolkit.IsAllowedIndexDDL(sqlText) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "仅允许 16KB 内的 CREATE/ALTER 索引类 DDL"})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind 仅支持 ddl 或 kill"})
		return
	}
	if len(req.Reason) > 2000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reason 过长"})
		return
	}
	cr := s.sqlChanges.Create(c, sqlText, req.Reason, s.actorName(r), kind, time.Now())
	rec, err := s.bridgeSQLChangeToRecord(cr, ChangePendingApproval)
	if err == nil && rec.ID > 0 {
		s.sqlChanges.SetChangeID(cr.ID, rec.ID)
		cr.ChangeID = rec.ID
		s.store.MarkDirty()
	}
	s.auditSQLChange(r, "create", cr, "warning")
	writeJSON(w, http.StatusCreated, cr)
}

// bridgeSQLChangeToRecord upserts a generic ChangeRecord for a SQL DDL/KILL ticket.
func (s *Server) bridgeSQLChangeToRecord(cr SQLChangeRequest, status string) (ChangeRecord, error) {
	if s.changes == nil {
		return ChangeRecord{}, fmt.Errorf("changes unavailable")
	}
	title := "SQL " + strings.ToUpper(firstNonEmptyOrDash(cr.Kind, "ddl")) + " · " + firstNonEmptyOrDash(cr.Connection, cr.ConnectionID)
	summary := firstNonEmptyOrDash(cr.Reason, truncateRunes(cr.SQL, 120))
	risk := "medium"
	if cr.Environment == "prod" || cr.Kind == "kill" {
		risk = "high"
	}
	return s.changes.UpsertFromSQLChange(cr.ID, title, summary, "sql", risk, cr.Proposer, status)
}

func (s *Server) syncSQLChangeRecordStatus(cr SQLChangeRequest, actor string) {
	if s.changes == nil || cr.ID == "" {
		return
	}
	rec, err := s.bridgeSQLChangeToRecord(cr, "")
	if err != nil || rec.ID == 0 {
		return
	}
	s.sqlChanges.SetChangeID(cr.ID, rec.ID)
	st := normalizeChangeStatus(rec.Status)
	switch cr.Status {
	// approved / rejected 是终态：迁移的副作用就是全部目的，迁移后的状态没有下一步会读，
	// 所以不再回写 st（只有下面 executed 那条会继续链式迁移，才需要跟踪状态）。
	case "approved":
		if st == ChangePendingApproval || st == ChangeDraft {
			_, _ = s.changes.Transition(rec.ID, "approve", actor, false)
		}
	case "rejected":
		if st == ChangePendingApproval || st == ChangeDraft {
			_, _ = s.changes.Transition(rec.ID, "reject", actor, false)
		}
	case "executed", "done", "completed":
		if st == ChangePendingApproval || st == ChangeDraft {
			if out, terr := s.changes.Transition(rec.ID, "approve", firstNonEmpty(cr.Approver, actor), false); terr == nil {
				st = normalizeChangeStatus(out.Status)
			} else if got, ok := s.changes.Get(rec.ID); ok {
				st = normalizeChangeStatus(got.Status)
			}
		}
		if st == ChangeApproved || st == ChangeScheduled {
			if out, terr := s.changes.Transition(rec.ID, "start", actor, false); terr == nil {
				st = normalizeChangeStatus(out.Status)
			} else if got, ok := s.changes.Get(rec.ID); ok {
				st = normalizeChangeStatus(got.Status)
			}
		}
		if st == ChangeInProgress {
			_, _ = s.changes.Transition(rec.ID, "complete", actor, false)
		}
	}
	if s.store != nil {
		s.store.MarkDirty()
	}
}

func (s *Server) handleListSQLChangeRequests(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"change_requests": s.sqlChanges.List(time.Now())})
}

func (s *Server) handleApproveSQLChangeRequest(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	approver := s.actorName(r)
	// Separation of duties: proposer cannot approve their own ticket.
	for _, existing := range s.sqlChanges.List(time.Now()) {
		if existing.ID == id && strings.EqualFold(strings.TrimSpace(existing.Proposer), strings.TrimSpace(approver)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "不能审批自己提交的变更单（职责分离）"})
			return
		}
	}
	cr, err := s.sqlChanges.Approve(id, approver, time.Now())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.syncSQLChangeRecordStatus(cr, approver)
	if cr2, ok := s.sqlChanges.Get(cr.ID); ok {
		cr = cr2
	}
	s.auditSQLChange(r, "approve", cr, "warning")
	writeJSON(w, http.StatusOK, cr)
}

func (s *Server) handleRejectSQLChangeRequest(w http.ResponseWriter, r *http.Request) {
	cr, err := s.sqlChanges.Reject(strings.TrimSpace(r.PathValue("id")), s.actorName(r), time.Now())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.syncSQLChangeRecordStatus(cr, s.actorName(r))
	if cr2, ok := s.sqlChanges.Get(cr.ID); ok {
		cr = cr2
	}
	s.auditSQLChange(r, "reject", cr, "warning")
	writeJSON(w, http.StatusOK, cr)
}

func (s *Server) executeSQLChangeRequest(w http.ResponseWriter, r *http.Request, ticketID, expectedConnectionID, expectedSQL string, timeoutSec int, verifySQL string) {
	now := time.Now()
	list := s.sqlChanges.List(now)
	var ticket *SQLChangeRequest
	for i := range list {
		if list[i].ID == ticketID {
			ticket = &list[i]
			break
		}
	}
	if ticket == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "change request not found"})
		return
	}
	executor := s.actorName(r)
	// Separation of duties: proposer cannot execute their own ticket either.
	if strings.EqualFold(strings.TrimSpace(ticket.Proposer), strings.TrimSpace(executor)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "不能执行自己提交的变更单（职责分离）"})
		return
	}
	if expectedConnectionID != "" && (ticket.ConnectionID != expectedConnectionID || ticket.SQL != strings.TrimSpace(expectedSQL)) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "ticket connection or SQL does not match"})
		return
	}
	c, ok := s.cfg.GetMySQLConnection(ticket.ConnectionID)
	if !ok || !c.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "connection not found or disabled"})
		return
	}
	if win, frozen := s.cfg.activeFreezeWindow("", "sql", now.Unix()); frozen && ticket.Kind != "kill" {
		// Kill may still be needed during freeze to unblock production; DDL stays frozen.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("当前处于 SQL 变更冻结窗「%s」，禁止执行 DDL 变更单", win.Name),
		})
		return
	}
	cr, err := s.sqlChanges.BeginExecute(ticketID, c.ID, ticket.SQL, executor, now)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.auditSQLChange(r, "execute_start", cr, "warning")
	var (
		result  map[string]any
		execErr error
	)
	if cr.Kind == "kill" {
		pid, ok := parseKillSQL(cr.SQL)
		if !ok {
			execErr = fmt.Errorf("invalid KILL statement")
		} else if driverOf(c) == "postgres" {
			execErr = pgKillSession(c, pid)
			if execErr == nil {
				result = map[string]any{"killed": pid, "via": "pg_terminate_backend"}
			}
		} else {
			execErr = mysqlKillSession(c, pid)
			if execErr == nil {
				result = map[string]any{"killed": pid}
			}
		}
	} else {
		if driverOf(c) == "postgres" {
			execErr = fmt.Errorf("PostgreSQL 不支持 DDL 变更单")
		} else {
			result, execErr = s.execDDLWithExplain(c, cr.SQL, verifySQL, timeoutSec)
		}
	}
	cr = s.sqlChanges.FinishExecute(ticketID, result, execErr, time.Now())
	if cr.Status == "executed" {
		s.syncSQLChangeRecordStatus(cr, executor)
		if cr2, ok := s.sqlChanges.Get(cr.ID); ok {
			cr = cr2
		}
	}
	if execErr != nil {
		s.auditSQLChange(r, "execute_failed", cr, "error")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": execErr.Error()})
		return
	}
	s.auditSQLChange(r, "execute_success", cr, "warning")
	writeJSON(w, http.StatusOK, cr)
}

func (s *Server) handleExecuteSQLChangeRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TimeoutSec int    `json:"timeout_sec"`
		VerifySQL  string `json:"verify_sql"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.executeSQLChangeRequest(w, r, strings.TrimSpace(r.PathValue("id")), "", "", req.TimeoutSec, req.VerifySQL)
}
