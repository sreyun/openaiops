package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Change windows + change records (ops change management).
// ============================================================================

type ChangeWindow struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Start      int64    `json:"start"`
	End        int64    `json:"end"`
	HostIDs    []string `json:"host_ids,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Freeze     bool     `json:"freeze"` // block unapproved auto-remediation
	Note       string   `json:"note,omitempty"`
	UpdatedAt  int64    `json:"updated_at,omitempty"`
	// Metadata for SLO / release linkage
	SLOIDs     []string `json:"slo_ids,omitempty"`
	Version    string   `json:"version,omitempty"`
	ServiceIDs []string `json:"service_ids,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
	// Recurrence: when Recur is set, absolute Start/End are optional anchors;
	// active window is computed from clock time each day/week.
	// Recur: "" | "daily" | "weekly"
	Recur         string `json:"recur,omitempty"`
	RecurStartHM  string `json:"recur_start_hm,omitempty"` // "22:00"
	RecurEndHM    string `json:"recur_end_hm,omitempty"`   // "06:00" (may cross midnight)
	RecurWeekdays []int  `json:"recur_weekdays,omitempty"` // 0=Sun … 6=Sat; empty weekly = all days
}

// ChangeRecord statuses (new machine). Legacy "planned" maps to draft/approved on read.
const (
	ChangeDraft           = "draft"
	ChangePendingApproval = "pending_approval"
	ChangeApproved        = "approved"
	ChangeScheduled       = "scheduled"
	ChangeInProgress      = "in_progress"
	ChangeCompleted       = "completed"
	ChangeRolledBack      = "rolled_back"
	ChangeRejected        = "rejected"
	ChangeCancelled       = "cancelled"
)

var changeStatuses = map[string]bool{
	ChangeDraft: true, ChangePendingApproval: true, ChangeApproved: true,
	ChangeScheduled: true, ChangeInProgress: true, ChangeCompleted: true,
	ChangeRolledBack: true, ChangeRejected: true, ChangeCancelled: true,
	// legacy
	"planned": true,
}

type ChangeRecord struct {
	ID                int64     `json:"id"`
	Title             string    `json:"title"`
	Summary           string    `json:"summary,omitempty"`
	Kind              string    `json:"kind"`   // deploy|config|infra|emergency|sql|other
	Status            string    `json:"status"` // draft|pending_approval|approved|scheduled|in_progress|completed|rolled_back|rejected|cancelled
	Risk              string    `json:"risk"`   // low|medium|high
	Plan              string    `json:"plan,omitempty"`
	RollbackPlan      string    `json:"rollback_plan,omitempty"`
	TestPlan          string    `json:"test_plan,omitempty"`
	HostIDs           []string  `json:"host_ids,omitempty"`
	Services          []string  `json:"services,omitempty"`
	WindowID          string    `json:"window_id,omitempty"`
	TicketIDs         []int64   `json:"ticket_ids,omitempty"`
	SQLChangeIDs      []string  `json:"sql_change_ids,omitempty"`
	Links             []OpsLink `json:"links,omitempty"`
	StartedAt         int64     `json:"started_at"`
	EndedAt           int64     `json:"ended_at,omitempty"`
	ApprovedAt        int64     `json:"approved_at,omitempty"`
	ExecutedAt        int64     `json:"executed_at,omitempty"`
	Author            string    `json:"author,omitempty"`
	Approver          string    `json:"approver,omitempty"`
	ExternalRef       string    `json:"external_ref,omitempty"`
	LinkedIncidentIDs []int64   `json:"linked_incident_ids,omitempty"`
	CreatedAt         int64     `json:"created_at"`
	UpdatedAt         int64     `json:"updated_at"`
}

func normalizeChangeStatus(st string) string {
	st = strings.ToLower(strings.TrimSpace(st))
	switch st {
	case "planned":
		return ChangeDraft
	case ChangeDraft, ChangePendingApproval, ChangeApproved, ChangeScheduled,
		ChangeInProgress, ChangeCompleted, ChangeRolledBack, ChangeRejected, ChangeCancelled:
		return st
	default:
		if st == "" {
			return ChangeDraft
		}
		return st
	}
}

func syncChangeLinkIndexes(r *ChangeRecord) {
	for _, id := range r.LinkedIncidentIDs {
		if id > 0 {
			r.Links = mergeOpsLinks(r.Links, incidentOpsLink(id, "related"))
		}
	}
	for _, id := range r.TicketIDs {
		if id > 0 {
			r.Links = mergeOpsLinks(r.Links, ticketOpsLink(id))
		}
	}
	for _, id := range r.SQLChangeIDs {
		if id != "" {
			r.Links = mergeOpsLinks(r.Links, sqlChangeOpsLink(id))
		}
	}
	for _, h := range r.HostIDs {
		if h != "" {
			r.Links = mergeOpsLinks(r.Links, OpsLink{Type: "host", ID: h, Role: "affects"})
		}
	}
	for _, l := range r.Links {
		switch l.Type {
		case "incident":
			if id := parseOpsLinkInt(l.ID); id > 0 {
				r.LinkedIncidentIDs = appendUniqueInt64(r.LinkedIncidentIDs, id)
			}
		case "ticket":
			if id := parseOpsLinkInt(l.ID); id > 0 {
				r.TicketIDs = appendUniqueInt64(r.TicketIDs, id)
			}
		case "sql_change":
			r.SQLChangeIDs = appendUniqueString(r.SQLChangeIDs, l.ID)
		case "host":
			r.HostIDs = appendUniqueString(r.HostIDs, l.ID)
		}
	}
}

func appendUniqueInt64(in []int64, v int64) []int64 {
	for _, x := range in {
		if x == v {
			return in
		}
	}
	return append(in, v)
}

func appendUniqueString(in []string, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return in
	}
	for _, x := range in {
		if x == v {
			return in
		}
	}
	return append(in, v)
}

type changeManager struct {
	mu      sync.Mutex
	records []ChangeRecord
	nextID  int64
}

func newChangeManager() *changeManager {
	return &changeManager{nextID: 0}
}

func (m *changeManager) Export() []ChangeRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ChangeRecord, len(m.records))
	copy(out, m.records)
	return out
}

func (m *changeManager) Import(list []ChangeRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append([]ChangeRecord(nil), list...)
	var maxID int64
	for i := range m.records {
		m.records[i].Status = normalizeChangeStatus(m.records[i].Status)
		syncChangeLinkIndexes(&m.records[i])
		if m.records[i].ID > maxID {
			maxID = m.records[i].ID
		}
	}
	if maxID >= m.nextID {
		m.nextID = maxID
	}
}

func (m *changeManager) List() []ChangeRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ChangeRecord, len(m.records))
	for i, r := range m.records {
		r.Status = normalizeChangeStatus(r.Status)
		out[i] = r
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

// ChangeListRow is the list projection of a ChangeRecord.
//
// The three free-text plans (execution / rollback / test) are what make a change
// record big — they are runbooks, not labels — and no list column renders them.
// Change records are append-only like tickets, so shipping every plan on every
// list load only gets more expensive. Detail comes from GET /api/v1/changes/{id};
// the has_* flags let a row show that a plan exists without carrying it.
type ChangeListRow struct {
	ChangeRecord
	HasPlan         bool `json:"has_plan"`
	HasRollbackPlan bool `json:"has_rollback_plan"`
	HasTestPlan     bool `json:"has_test_plan"`
}

func changeListRows(list []ChangeRecord) []ChangeListRow {
	out := make([]ChangeListRow, 0, len(list))
	for _, c := range list {
		row := ChangeListRow{
			HasPlan:         strings.TrimSpace(c.Plan) != "",
			HasRollbackPlan: strings.TrimSpace(c.RollbackPlan) != "",
			HasTestPlan:     strings.TrimSpace(c.TestPlan) != "",
		}
		// c is a per-iteration copy; blanking here does not touch the manager.
		c.Plan, c.RollbackPlan, c.TestPlan = "", "", ""
		row.ChangeRecord = c
		out = append(out, row)
	}
	return out
}

func (m *changeManager) Get(id int64) (ChangeRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.records {
		if r.ID == id {
			r.Status = normalizeChangeStatus(r.Status)
			return r, true
		}
	}
	return ChangeRecord{}, false
}

func (m *changeManager) findLocked(id int64) *ChangeRecord {
	for i := range m.records {
		if m.records[i].ID == id {
			return &m.records[i]
		}
	}
	return nil
}

func (m *changeManager) Upsert(in ChangeRecord, actor string) (ChangeRecord, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return ChangeRecord{}, fmt.Errorf("变更标题不能为空")
	}
	if in.Kind == "" {
		in.Kind = "other"
	}
	in.Status = normalizeChangeStatus(in.Status)
	if !changeStatuses[in.Status] && in.Status != "planned" {
		in.Status = ChangeDraft
	}
	in.Status = normalizeChangeStatus(in.Status)
	if in.Risk == "" {
		in.Risk = "medium"
	}
	in.Links = mergeOpsLinks(nil, in.Links...)
	syncChangeLinkIndexes(&in)
	now := time.Now().Unix()
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.ID == 0 {
		m.nextID++
		in.ID = m.nextID
		in.CreatedAt = now
		if in.Author == "" {
			in.Author = actor
		}
		if in.StartedAt == 0 {
			in.StartedAt = now
		}
		if in.Status == "" {
			in.Status = ChangeDraft
		}
		in.UpdatedAt = now
		m.records = append(m.records, in)
		return in, nil
	}
	for i := range m.records {
		if m.records[i].ID == in.ID {
			prev := m.records[i]
			in.CreatedAt = prev.CreatedAt
			if in.Author == "" {
				in.Author = prev.Author
			}
			if in.ApprovedAt == 0 {
				in.ApprovedAt = prev.ApprovedAt
			}
			if in.ExecutedAt == 0 {
				in.ExecutedAt = prev.ExecutedAt
			}
			if in.Approver == "" {
				in.Approver = prev.Approver
			}
			// UI/API partial updates often omit association slices; preserve unless explicitly provided.
			if len(in.Links) == 0 {
				in.Links = prev.Links
			} else {
				in.Links = mergeOpsLinks(prev.Links, in.Links...)
			}
			if len(in.SQLChangeIDs) == 0 {
				in.SQLChangeIDs = prev.SQLChangeIDs
			}
			if len(in.TicketIDs) == 0 {
				in.TicketIDs = prev.TicketIDs
			}
			if len(in.LinkedIncidentIDs) == 0 {
				in.LinkedIncidentIDs = prev.LinkedIncidentIDs
			}
			syncChangeLinkIndexes(&in)
			in.UpdatedAt = now
			m.records[i] = in
			return in, nil
		}
	}
	return ChangeRecord{}, fmt.Errorf("变更不存在")
}

// Transition applies a workflow action: submit|approve|reject|start|complete|rollback|cancel|schedule.
// breakGlass skips author≠approver SoD (admin only; enforced by HTTP layer).
func (m *changeManager) Transition(id int64, action, actor string, freezeActive bool) (ChangeRecord, error) {
	return m.TransitionSoD(id, action, actor, freezeActive, false)
}

func (m *changeManager) TransitionSoD(id int64, action, actor string, freezeActive, breakGlass bool) (ChangeRecord, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.findLocked(id)
	if r == nil {
		return ChangeRecord{}, fmt.Errorf("变更不存在")
	}
	if err := changeSoDAllows(*r, action, actor, breakGlass); err != nil {
		return ChangeRecord{}, err
	}
	cur := normalizeChangeStatus(r.Status)
	now := time.Now().Unix()
	next := ""
	switch action {
	case "submit":
		if cur != ChangeDraft && cur != ChangeRejected {
			return ChangeRecord{}, fmt.Errorf("仅 draft/rejected 可提交审批")
		}
		next = ChangePendingApproval
	case "approve":
		if cur != ChangePendingApproval && cur != ChangeDraft {
			return ChangeRecord{}, fmt.Errorf("仅 pending_approval/draft 可批准")
		}
		next = ChangeApproved
		r.Approver = actor
		r.ApprovedAt = now
	case "reject":
		if cur != ChangePendingApproval && cur != ChangeDraft {
			return ChangeRecord{}, fmt.Errorf("仅 pending_approval/draft 可驳回")
		}
		next = ChangeRejected
		r.Approver = actor
	case "schedule":
		if cur != ChangeApproved {
			return ChangeRecord{}, fmt.Errorf("仅 approved 可排期")
		}
		next = ChangeScheduled
	case "start":
		if cur != ChangeApproved && cur != ChangeScheduled && cur != ChangeDraft {
			return ChangeRecord{}, fmt.Errorf("当前状态不可开始执行")
		}
		needsApproval := freezeActive && (r.Risk == "high" || r.Kind == "emergency")
		if needsApproval && cur != ChangeApproved && cur != ChangeScheduled {
			return ChangeRecord{}, fmt.Errorf("高风险/应急变更处于冻结窗内，须先审批再执行")
		}
		next = ChangeInProgress
		r.ExecutedAt = now
		if r.StartedAt == 0 {
			r.StartedAt = now
		}
	case "complete":
		if cur != ChangeInProgress {
			return ChangeRecord{}, fmt.Errorf("仅 in_progress 可完成")
		}
		next = ChangeCompleted
		r.EndedAt = now
	case "rollback":
		if cur != ChangeInProgress && cur != ChangeCompleted {
			return ChangeRecord{}, fmt.Errorf("仅 in_progress/completed 可回滚")
		}
		next = ChangeRolledBack
		r.EndedAt = now
	case "cancel":
		if cur == ChangeCompleted || cur == ChangeRolledBack {
			return ChangeRecord{}, fmt.Errorf("终态不可取消")
		}
		next = ChangeCancelled
		r.EndedAt = now
	default:
		return ChangeRecord{}, fmt.Errorf("未知动作: %s", action)
	}
	r.Status = next
	r.UpdatedAt = now
	return *r, nil
}

func (m *changeManager) Link(id int64, add []OpsLink, removeType, removeID, removeRole string) (ChangeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.findLocked(id)
	if r == nil {
		return ChangeRecord{}, fmt.Errorf("变更不存在")
	}
	if removeType != "" && removeID != "" {
		r.Links = removeOpsLink(r.Links, removeType, removeID, removeRole)
	}
	if len(add) > 0 {
		r.Links = mergeOpsLinks(r.Links, add...)
	}
	syncChangeLinkIndexes(r)
	r.UpdatedAt = time.Now().Unix()
	return *r, nil
}

func (m *changeManager) LinkIncident(changeID, incidentID int64) (ChangeRecord, error) {
	return m.Link(changeID, []OpsLink{incidentOpsLink(incidentID, "related")}, "", "", "")
}

// UpsertFromSQLChange creates or updates a ChangeRecord linked to a SQL change ticket.
func (m *changeManager) UpsertFromSQLChange(sqlID, title, summary, kind, risk, author string, status string) (ChangeRecord, error) {
	if kind == "" {
		kind = "sql"
	}
	if risk == "" {
		risk = "medium"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().Unix()
	for i := range m.records {
		r := &m.records[i]
		for _, sid := range r.SQLChangeIDs {
			if sid == sqlID {
				if title != "" {
					r.Title = title
				}
				if summary != "" {
					r.Summary = summary
				}
				if status != "" {
					r.Status = normalizeChangeStatus(status)
				}
				r.UpdatedAt = now
				syncChangeLinkIndexes(r)
				return *r, nil
			}
		}
		for _, l := range r.Links {
			if l.Type == "sql_change" && l.ID == sqlID {
				if title != "" {
					r.Title = title
				}
				if summary != "" {
					r.Summary = summary
				}
				if status != "" {
					r.Status = normalizeChangeStatus(status)
				}
				r.UpdatedAt = now
				syncChangeLinkIndexes(r)
				return *r, nil
			}
		}
	}
	m.nextID++
	rec := ChangeRecord{
		ID: m.nextID, Title: title, Summary: summary, Kind: kind, Risk: risk,
		Status: normalizeChangeStatus(firstNonEmptyOrDash(status, ChangePendingApproval)),
		Author: author, SQLChangeIDs: []string{sqlID},
		Links:     []OpsLink{sqlChangeOpsLink(sqlID)},
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	syncChangeLinkIndexes(&rec)
	m.records = append(m.records, rec)
	return rec, nil
}

func (m *changeManager) RelatedToHosts(hostIDs []string, since int64) []ChangeRecord {
	want := map[string]bool{}
	for _, h := range hostIDs {
		if h != "" {
			want[h] = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ChangeRecord
	for _, r := range m.records {
		if r.StartedAt < since && (r.EndedAt == 0 || r.EndedAt < since) {
			continue
		}
		if len(want) == 0 {
			out = append(out, r)
			continue
		}
		hit := false
		for _, h := range r.HostIDs {
			if want[h] {
				hit = true
				break
			}
		}
		if !hit {
			for _, l := range r.Links {
				if l.Type == "host" && want[l.ID] {
					hit = true
					break
				}
			}
		}
		if hit {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func (cs *ConfigStore) ChangeWindows() []ChangeWindow {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]ChangeWindow, len(cs.cfg.ChangeWindows))
	copy(out, cs.cfg.ChangeWindows)
	return out
}

func (cs *ConfigStore) UpsertChangeWindow(w ChangeWindow) (ChangeWindow, error) {
	w.Name = strings.TrimSpace(w.Name)
	if w.Name == "" {
		return ChangeWindow{}, fmt.Errorf("变更窗名称不能为空")
	}
	w.Recur = strings.ToLower(strings.TrimSpace(w.Recur))
	if w.Recur != "" && w.Recur != "daily" && w.Recur != "weekly" {
		return ChangeWindow{}, fmt.Errorf("recur 仅支持 daily/weekly")
	}
	if w.Recur != "" {
		if parseHM(w.RecurStartHM) < 0 || parseHM(w.RecurEndHM) < 0 {
			return ChangeWindow{}, fmt.Errorf("循环变更窗需填写 recur_start_hm / recur_end_hm（HH:MM）")
		}
	} else if w.End > 0 && w.End < w.Start {
		return ChangeWindow{}, fmt.Errorf("结束时间必须晚于开始时间")
	}
	w.UpdatedAt = time.Now().Unix()
	cs.mu.Lock()
	if w.ID == "" {
		w.ID = genToken()[:8]
		cs.cfg.ChangeWindows = append(cs.cfg.ChangeWindows, w)
	} else {
		found := false
		for i := range cs.cfg.ChangeWindows {
			if cs.cfg.ChangeWindows[i].ID == w.ID {
				cs.cfg.ChangeWindows[i] = w
				found = true
				break
			}
		}
		if !found {
			cs.cfg.ChangeWindows = append(cs.cfg.ChangeWindows, w)
		}
	}
	cs.mu.Unlock()
	return w, cs.save()
}

func (cs *ConfigStore) DeleteChangeWindow(id string) error {
	cs.mu.Lock()
	kept := cs.cfg.ChangeWindows[:0]
	for _, w := range cs.cfg.ChangeWindows {
		if w.ID != id {
			kept = append(kept, w)
		}
	}
	cs.cfg.ChangeWindows = kept
	cs.mu.Unlock()
	return cs.save()
}

// activeFreezeWindow reports whether auto-remediation should be frozen for host.
func (cs *ConfigStore) activeFreezeWindow(hostID, category string, now int64) (ChangeWindow, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, w := range cs.cfg.ChangeWindows {
		if !w.Freeze {
			continue
		}
		if !changeWindowActiveAt(w, now) {
			continue
		}
		if len(w.HostIDs) == 0 && len(w.Categories) == 0 {
			return w, true
		}
		for _, h := range w.HostIDs {
			if h == hostID {
				return w, true
			}
		}
		for _, c := range w.Categories {
			if c != "" && c == category {
				return w, true
			}
		}
	}
	return ChangeWindow{}, false
}

func parseHM(s string) int {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return -1
	}
	h, m := 0, 0
	for _, ch := range parts[0] {
		if ch < '0' || ch > '9' {
			return -1
		}
		h = h*10 + int(ch-'0')
	}
	for _, ch := range parts[1] {
		if ch < '0' || ch > '9' {
			return -1
		}
		m = m*10 + int(ch-'0')
	}
	if h > 23 || m > 59 {
		return -1
	}
	return h*60 + m
}

func changeWindowActiveAt(w ChangeWindow, now int64) bool {
	if strings.TrimSpace(w.Recur) == "" {
		if now < w.Start {
			return false
		}
		if w.End > 0 && now > w.End {
			return false
		}
		return true
	}
	t := time.Unix(now, 0).In(time.Local)
	startMin := parseHM(w.RecurStartHM)
	endMin := parseHM(w.RecurEndHM)
	if startMin < 0 || endMin < 0 {
		return false
	}
	if strings.EqualFold(w.Recur, "weekly") && len(w.RecurWeekdays) > 0 {
		wd := int(t.Weekday())
		hit := false
		for _, d := range w.RecurWeekdays {
			if d == wd {
				hit = true
				break
			}
		}
		// Overnight weekly windows: also allow previous weekday when before end.
		if !hit && endMin <= startMin {
			prev := (wd + 6) % 7
			for _, d := range w.RecurWeekdays {
				if d == prev {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
			// Only the overnight tail of previous day's window.
			cur := t.Hour()*60 + t.Minute()
			return cur < endMin
		}
		if !hit {
			return false
		}
	}
	cur := t.Hour()*60 + t.Minute()
	if endMin > startMin {
		return cur >= startMin && cur < endMin
	}
	// Cross midnight: active if >= start OR < end.
	return cur >= startMin || cur < endMin
}

// activeFreezeForHosts is true if any freeze window covers the given hosts (or is global).
func (cs *ConfigStore) activeFreezeForHosts(hostIDs []string, now int64) (ChangeWindow, bool) {
	if len(hostIDs) == 0 {
		return cs.activeFreezeWindow("", "", now)
	}
	for _, h := range hostIDs {
		if w, ok := cs.activeFreezeWindow(h, "", now); ok {
			return w, true
		}
	}
	return ChangeWindow{}, false
}
