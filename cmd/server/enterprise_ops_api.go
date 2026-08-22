package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// startOnCallForIncident assigns current on-call and opens an escalation page.
func (s *Server) startOnCallForIncident(inc Incident) {
	pol, ok := s.cfg.DefaultEscalationPolicy()
	if !ok {
		// still try assign from first schedule
		schs := s.cfg.OnCallSchedules()
		if len(schs) == 0 {
			return
		}
		user := resolveOnCallUser(schs[0], time.Now())
		if user != "" {
			s.incidents.SetAssignee(inc.ID, user, "oncall")
			s.store.MarkDirty()
		}
		return
	}
	schedID := ""
	var users []string
	if len(pol.Steps) > 0 {
		t := pol.Steps[0].Target
		if len(t.Users) > 0 {
			users = append(users, t.Users...)
		}
		if t.ScheduleID != "" {
			if sch, ok := s.cfg.FindOnCallSchedule(t.ScheduleID); ok {
				schedID = sch.ID
				users = append(users, resolveLayerUsers(sch, t.Layer, time.Now())...)
			}
		}
	}
	if schedID == "" {
		schs := s.cfg.OnCallSchedules()
		if len(schs) > 0 {
			schedID = schs[0].ID
			if len(users) == 0 {
				users = resolveLayerUsers(schs[0], 0, time.Now())
			}
		}
	}
	if len(users) > 0 {
		s.incidents.SetAssignee(inc.ID, users[0], "oncall")
	}
	page := s.oncall.Start(inc, pol, schedID, users)
	// Immediate step-0 notify via message center + configured channels.
	s.messages.push("oncall", inc.Severity, "On-call 通知："+inc.Title,
		fmt.Sprintf("值班：%s · 策略：%s · page #%d", strings.Join(users, ","), pol.Name, page.ID),
		"sre", strconv.FormatInt(inc.ID, 10))
	if s.notifier != nil && len(pol.Steps) > 0 && len(pol.Steps[0].Channels) > 0 {
		body := fmt.Sprintf("事件 #%d · %s\n值班：%s\n策略：%s",
			inc.ID, inc.Title, strings.Join(users, ", "), pol.Name)
		// Prefer on-call contact overrides from account profiles when present.
		s.notifier.PushAdhoc(inc.Severity, "On-call："+inc.Title, body, pol.Steps[0].Channels)
	}
	s.store.MarkDirty()
}

func (s *Server) startOnCallEscalationLoop() {
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			s.tickOnCallEscalation()
		}
	}()
}

func (s *Server) tickOnCallEscalation() {
	now := time.Now().Unix()
	for _, page := range s.oncall.DuePages(now) {
		pol, ok := s.cfg.FindEscalationPolicy(page.PolicyID)
		if !ok || !pol.Enabled {
			s.oncall.Advance(page.ID, page.Step, nil, 0, true)
			continue
		}
		next := page.Step + 1
		if next >= len(pol.Steps) {
			s.oncall.Advance(page.ID, page.Step, nil, 0, true)
			s.messages.push("oncall", "critical", "On-call 升级已穷尽",
				fmt.Sprintf("事件 #%d 已走完整升级阶梯仍未确认", page.IncidentID),
				"sre", strconv.FormatInt(page.IncidentID, 10))
			continue
		}
		step := pol.Steps[next]
		users := append([]string(nil), step.Target.Users...)
		if step.Target.ScheduleID != "" {
			if sch, ok := s.cfg.FindOnCallSchedule(step.Target.ScheduleID); ok {
				users = append(users, resolveLayerUsers(sch, step.Target.Layer, time.Now())...)
			}
		}
		nextAt := now + int64(step.AfterSec)
		if step.AfterSec <= 0 && next+1 < len(pol.Steps) {
			nextAt = now + int64(pol.Steps[next+1].AfterSec)
		}
		s.oncall.Advance(page.ID, next, users, nextAt, false)
		s.messages.push("oncall", "warning",
			fmt.Sprintf("On-call 升级至第 %d 级", next+1),
			fmt.Sprintf("事件 #%d · 通知：%s", page.IncidentID, strings.Join(users, ",")),
			"sre", strconv.FormatInt(page.IncidentID, 10))
		// Best-effort channel push using notifier's custom text path if available
		if s.notifier != nil && len(step.Channels) > 0 {
			s.notifier.PushAdhoc("warning",
				fmt.Sprintf("On-call 升级：事件 #%d", page.IncidentID),
				strings.Join(users, ", "), step.Channels)
		}
	}
}

func (s *Server) appendChangeCorrelation(inc Incident) {
	if inc.HostID == "" || s.changes == nil {
		return
	}
	since := time.Now().Add(-7 * 24 * time.Hour).Unix()
	rels := s.changes.RelatedToHosts([]string{inc.HostID}, since)
	if len(rels) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("关联变更：")
	for i, c := range rels {
		if i >= 5 {
			break
		}
		if i > 0 {
			b.WriteString("；")
		}
		fmt.Fprintf(&b, "#%d %s (%s)", c.ID, c.Title, c.Kind)
		_, _ = s.changes.LinkIncident(c.ID, inc.ID)
	}
	s.incidents.AddEvent(inc.ID, "change_correlation", "system", b.String())
	s.store.MarkDirty()
}

// --- On-call HTTP ---

func (s *Server) handleOnCallWho(w http.ResponseWriter, r *http.Request) {
	at := time.Now()
	if v := r.URL.Query().Get("at"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			at = time.Unix(n, 0)
		}
	}
	schs := s.cfg.OnCallSchedules()
	type layerView struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
		Current string   `json:"current"`
	}
	out := []map[string]any{}
	for _, sch := range schs {
		layers := []layerView{}
		for i, l := range sch.Layers {
			cur := ""
			if i == 0 {
				cur = resolveOnCallUser(sch, at)
			} else if len(l.Members) > 0 {
				cur = l.Members[0]
			}
			layers = append(layers, layerView{Name: l.Name, Members: l.Members, Current: cur})
		}
		primary := ""
		if len(layers) > 0 {
			primary = layers[0].Current
		}
		out = append(out, map[string]any{"id": sch.ID, "name": sch.Name, "primary": primary, "layers": layers})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListOnCallSchedules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.OnCallSchedules())
}
func (s *Server) handleUpsertOnCallSchedule(w http.ResponseWriter, r *http.Request) {
	var in OnCallSchedule
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	out, err := s.cfg.UpsertOnCallSchedule(in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleDeleteOnCallSchedule(w http.ResponseWriter, r *http.Request) {
	_ = s.cfg.DeleteOnCallSchedule(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListEscalationPolicies(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.EscalationPolicies())
}
func (s *Server) handleUpsertEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	var in EscalationPolicy
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	out, err := s.cfg.UpsertEscalationPolicy(in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleDeleteEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	_ = s.cfg.DeleteEscalationPolicy(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListOnCallPages(w http.ResponseWriter, r *http.Request) {
	openOnly := r.URL.Query().Get("open") == "1"
	writeJSON(w, http.StatusOK, s.oncall.List(openOnly))
}

// --- Changes HTTP ---

func (s *Server) handleListChangeWindows(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.ChangeWindows())
}
func (s *Server) handleUpsertChangeWindow(w http.ResponseWriter, r *http.Request) {
	var in ChangeWindow
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	out, err := s.cfg.UpsertChangeWindow(in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleDeleteChangeWindow(w http.ResponseWriter, r *http.Request) {
	_ = s.cfg.DeleteChangeWindow(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListChanges(w http.ResponseWriter, r *http.Request) {
	rows := changeListRows(s.changes.List())
	limit, offset, paged := parsePageLimitOffset(r, 50, 500)
	if !paged {
		writeJSON(w, http.StatusOK, rows)
		return
	}
	total := len(rows)
	offset = normalizeListOffsetForTotal(offset, total)
	end := offset + limit
	if end > total {
		end = total
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": rows[offset:end], "total": total, "limit": limit, "offset": offset,
	})
}
func (s *Server) handleUpsertChange(w http.ResponseWriter, r *http.Request) {
	var in ChangeRecord
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	in.Status = normalizeChangeStatus(in.Status)
	// Prevent bypassing the approval gate by writing status=in_progress directly.
	if in.Status == ChangeInProgress {
		hosts := in.HostIDs
		if in.ID > 0 {
			if prev, ok := s.changes.Get(in.ID); ok {
				if len(hosts) == 0 {
					hosts = prev.HostIDs
				}
				if in.Risk == "" {
					in.Risk = prev.Risk
				}
				if in.Kind == "" {
					in.Kind = prev.Kind
				}
				prevSt := normalizeChangeStatus(prev.Status)
				_, freeze := s.cfg.activeFreezeForHosts(hosts, time.Now().Unix())
				needs := freeze && (in.Risk == "high" || in.Kind == "emergency")
				if needs && prevSt != ChangeApproved && prevSt != ChangeScheduled && prevSt != ChangeInProgress {
					writeJSON(w, http.StatusBadRequest, map[string]string{
						"error": "高风险/应急变更处于冻结窗内，须先审批再执行（请用 /approve 再 /start）",
					})
					return
				}
			}
		} else {
			_, freeze := s.cfg.activeFreezeForHosts(hosts, time.Now().Unix())
			if freeze && (in.Risk == "high" || in.Kind == "emergency") {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "高风险/应急变更处于冻结窗内，不能直接创建为执行中，请先提交审批",
				})
				return
			}
		}
	}
	out, err := s.changes.Upsert(in, s.actorName(r))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.store.MarkDirty()
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleGetChange(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rec, ok := s.changes.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}
func (s *Server) handleLinkChangeIncident(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		IncidentID int64 `json:"incident_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.IncidentID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "incident_id required"})
		return
	}
	rec, err := s.changes.LinkIncident(id, in.IncidentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.incidents.AddLinks(in.IncidentID, []OpsLink{changeOpsLink(id)}, s.actorName(r), "")
	s.store.MarkDirty()
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleChangeLink(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Add    []OpsLink `json:"add"`
		Remove *OpsLink  `json:"remove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	rmType, rmID, rmRole := "", "", ""
	if in.Remove != nil {
		rmType, rmID, rmRole = in.Remove.Type, in.Remove.ID, in.Remove.Role
	}
	rec, err := s.changes.Link(id, in.Add, rmType, rmID, rmRole)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.store.MarkDirty()
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleChangeAction(w http.ResponseWriter, r *http.Request, action string) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rec, ok := s.changes.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	_, freeze := s.cfg.activeFreezeForHosts(rec.HostIDs, time.Now().Unix())
	actor := s.actorName(r)
	breakGlass := s.actorIsAdmin(r) && (r.URL.Query().Get("break_glass") == "1" || r.Header.Get("X-Break-Glass") == "1")
	out, err := s.changes.TransitionSoD(id, action, actor, freeze, breakGlass)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if breakGlass && (action == "approve" || action == "start") && s.store != nil {
		s.store.AddLog(LogEntry{
			Kind: KindOperation, Level: "warning", Actor: actor,
			Message: "变更 SoD break-glass #" + strconv.FormatInt(id, 10) + " " + action,
		})
	}
	s.store.MarkDirty()
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleChangeSubmit(w http.ResponseWriter, r *http.Request) {
	s.handleChangeAction(w, r, "submit")
}
func (s *Server) handleChangeApprove(w http.ResponseWriter, r *http.Request) {
	s.handleChangeAction(w, r, "approve")
}
func (s *Server) handleChangeReject(w http.ResponseWriter, r *http.Request) {
	s.handleChangeAction(w, r, "reject")
}
func (s *Server) handleChangeStart(w http.ResponseWriter, r *http.Request) {
	s.handleChangeAction(w, r, "start")
}
func (s *Server) handleChangeComplete(w http.ResponseWriter, r *http.Request) {
	s.handleChangeAction(w, r, "complete")
}
func (s *Server) handleChangeRollback(w http.ResponseWriter, r *http.Request) {
	s.handleChangeAction(w, r, "rollback")
}
func (s *Server) handleChangeCancel(w http.ResponseWriter, r *http.Request) {
	s.handleChangeAction(w, r, "cancel")
}
func (s *Server) handleChangeSchedule(w http.ResponseWriter, r *http.Request) {
	s.handleChangeAction(w, r, "schedule")
}

func (s *Server) handleIncidentRelatedChanges(w http.ResponseWriter, r *http.Request) {
	id, ok := sreParseID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	inc, found := s.incidents.Get(id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "incident.not_found")})
		return
	}
	if !s.requireIncidentAccess(w, r, inc.HostID) {
		return
	}
	since := time.Now().Add(-14 * 24 * time.Hour).Unix()
	writeJSON(w, http.StatusOK, s.changes.RelatedToHosts([]string{inc.HostID}, since))
}

func (s *Server) handleAssignIncident(w http.ResponseWriter, r *http.Request) {
	id, ok := sreParseID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	var in struct {
		Assignee string `json:"assignee"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	inc, found := s.incidents.SetAssignee(id, in.Assignee, s.actorName(r))
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "incident.not_found")})
		return
	}
	s.store.MarkDirty()
	writeJSON(w, http.StatusOK, inc)
}
