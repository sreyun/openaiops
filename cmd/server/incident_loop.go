package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// diagnosisGateResult summarizes the latest AI diagnosis for propose gating.
type diagnosisGateResult struct {
	HasDiagnosis bool   `json:"has_diagnosis"`
	Citations    int    `json:"citations"`
	LowConf      bool   `json:"low_confidence"`
	OK           bool   `json:"ok"`
	Reason       string `json:"reason,omitempty"`
	Text         string `json:"text,omitempty"`
	RunHint      string `json:"run_hint,omitempty"`
}

func latestDiagnosisGate(inc Incident) diagnosisGateResult {
	var ev *IncidentEvent
	for i := len(inc.Timeline) - 1; i >= 0; i-- {
		if inc.Timeline[i].Kind == "ai_diagnosis" {
			ev = &inc.Timeline[i]
			break
		}
	}
	if ev == nil {
		return diagnosisGateResult{OK: false, Reason: "尚无 AI 诊断，请先执行诊断"}
	}
	out := diagnosisGateResult{
		HasDiagnosis: true,
		Citations:    len(ev.Citations),
		Text:         ev.Text,
		OK:           true,
	}
	low := strings.Contains(ev.Text, "置信度") && (strings.Contains(ev.Text, "低") || strings.Contains(ev.Text, "low"))
	out.LowConf = low
	if !diagnosisEvidenceOK(ev.Citations) {
		out.OK = false
		out.Reason = "诊断证据不足：需指标/告警/日志/SQL 等实质性引用（仅主机心跳不够）"
	} else if low {
		out.OK = false
		out.Reason = "诊断置信度为低，需人工复核或强制继续"
	}
	return out
}

func ensureLoop(inc *Incident) *IncidentLoopState {
	if inc.Loop == nil {
		inc.Loop = &IncidentLoopState{Stage: "idle"}
	}
	if inc.Loop.Stage == "" {
		inc.Loop.Stage = "idle"
	}
	return inc.Loop
}

func (s *Server) handleGetIncidentLoop(w http.ResponseWriter, r *http.Request) {
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
	gate := latestDiagnosisGate(inc)
	loop := ensureLoop(&inc)
	if gate.HasDiagnosis && (loop.Stage == "idle" || loop.Stage == "") {
		loop.Stage = "diagnosed"
		loop.DiagnosedAt = time.Now().Unix()
		inc, _ = s.incidents.SetLoop(id, *loop)
		loop = ensureLoop(&inc)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"incident_id": id,
		"loop":        loop,
		"gate":        gate,
	})
}

func (s *Server) handleIncidentLoopAction(w http.ResponseWriter, r *http.Request) {
	id, ok := sreParseID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	action := strings.ToLower(strings.TrimSpace(r.PathValue("action")))
	inc, found := s.incidents.Get(id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "incident.not_found")})
		return
	}
	if !s.requireIncidentAccess(w, r, inc.HostID) {
		return
	}
	actor := s.actorName(r)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	force := false
	if body != nil {
		if v, ok := body["force"].(bool); ok {
			force = v
		}
	}
	if force && !s.requireLoopForceAdmin(w, r, force) {
		return
	}

	switch action {
	case "dry-run", "dry_run":
		s.loopDryRun(w, r, inc, actor, force)
	case "propose":
		s.loopPropose(w, r, inc, actor, force, body)
	case "approve":
		s.loopApprove(w, r, inc, actor)
	case "verify":
		s.loopVerify(w, r, inc, actor)
	case "promote":
		s.loopPromote(w, r, inc, actor, force)
	case "demo", "run-demo":
		s.loopRunDemo(w, r, inc, actor)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未知动作: " + action})
	}
}

func (s *Server) loopDryRun(w http.ResponseWriter, r *http.Request, inc Incident, actor string, force bool) {
	loop := ensureLoop(&inc)
	gate := latestDiagnosisGate(inc)
	notes := []string{}
	ok := true
	if strings.TrimSpace(inc.HostID) == "" {
		ok = false
		notes = append(notes, "事件未关联主机")
	} else if s.store != nil {
		if h, found := s.store.GetHost(inc.HostID); !found || h == nil {
			ok = false
			notes = append(notes, "主机不存在: "+inc.HostID)
		} else {
			age := time.Now().Unix() - h.LastSeen
			if age > 300 {
				notes = append(notes, fmt.Sprintf("主机心跳偏旧（%ds 前），执行风险升高", age))
			} else {
				notes = append(notes, "主机在线检查通过")
			}
		}
	}
	if s.cfg != nil {
		if win, frozen := s.cfg.activeFreezeWindow(inc.HostID, inc.Type, time.Now().Unix()); frozen {
			notes = append(notes, "处于冻结窗「"+win.Name+"」——修复须审批")
		}
	}
	if !gate.HasDiagnosis {
		notes = append(notes, "尚无 AI 诊断（建议先诊断）")
	} else if !gate.OK {
		notes = append(notes, "诊断闸门: "+gate.Reason)
		if !force {
			// dry-run still allowed but marked not fully ready for propose
			notes = append(notes, "propose 将被拦截，除非 force=true")
		}
	} else {
		notes = append(notes, fmt.Sprintf("诊断引用 %d 条，闸门通过", gate.Citations))
	}
	loop.DryRunOK = ok
	loop.DryRunAt = time.Now().Unix()
	loop.DryRunNotes = notes
	if ok {
		loop.Stage = "dry_run_ok"
	}
	loop.Force = force
	inc, _ = s.incidents.SetLoop(inc.ID, *loop)
	s.incidents.AddEvent(inc.ID, "note", actor, "闭环 dry-run："+strings.Join(notes, "；"))
	if s.store != nil {
		s.store.MarkDirty()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "loop": inc.Loop, "gate": gate, "notes": notes})
}

func (s *Server) loopPropose(w http.ResponseWriter, r *http.Request, inc Incident, actor string, force bool, body map[string]any) {
	gate := latestDiagnosisGate(inc)
	loop := ensureLoop(&inc)
	if !gate.OK && !force {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": gate.Reason,
			"gate":  gate,
			"hint":  "设置 force=true 可强制继续",
		})
		return
	}
	if !loop.DryRunOK && !force {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "请先执行 dry-run 通过主机/冻结窗检查（或 force=true）",
		})
		return
	}
	if strings.TrimSpace(inc.HostID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "事件未关联主机"})
		return
	}

	// Optional emergency change instead of playbook.
	mode, _ := body["mode"].(string)
	if strings.EqualFold(mode, "emergency_change") {
		rec, err := s.changes.Upsert(ChangeRecord{
			Title:   "应急变更 · 事件 #" + strconv.FormatInt(inc.ID, 10),
			Summary: inc.Title, Kind: "emergency", Risk: "high",
			Status: ChangePendingApproval, HostIDs: []string{inc.HostID},
			LinkedIncidentIDs: []int64{inc.ID},
			Links:             mergeOpsLinks(inc.Links, incidentOpsLink(inc.ID, "caused_by")),
		}, actor)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		loop.ChangeID = rec.ID
		loop.Stage = "proposed"
		loop.Force = force
		inc, _ = s.incidents.SetLoop(inc.ID, *loop)
		s.incidents.AddLinks(inc.ID, []OpsLink{changeOpsLink(rec.ID)}, actor, fmt.Sprintf("闭环提案：应急变更 #%d", rec.ID))
		if s.store != nil {
			s.store.MarkDirty()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "change": rec, "loop": inc.Loop, "gate": gate})
		return
	}

	pbID, _ := body["existing_playbook_id"].(string)
	title, _ := body["title"].(string)
	var pb Playbook
	if strings.TrimSpace(pbID) != "" {
		var okPB bool
		pb, okPB = s.playbooks.Get(strings.TrimSpace(pbID))
		if !okPB {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "指定剧本不存在"})
			return
		}
	} else {
		// Minimal safe draft from diagnosis text.
		pb = Playbook{
			Name: "[提案] " + trimLine(inc.Title, 40),
			Steps: []PlaybookStep{
				{
					Name: "inspect", Target: "host:" + inc.HostID, TimeoutSec: 60,
					Command: "echo 'loop propose placeholder — replace with real fix steps'",
				},
			},
		}
		if raw, ok := body["playbook"]; ok {
			b, _ := json.Marshal(raw)
			_ = json.Unmarshal(b, &pb)
			pb.ID = ""
			if strings.TrimSpace(pb.Name) == "" {
				pb.Name = "[提案] " + trimLine(inc.Title, 40)
			}
			for i := range pb.Steps {
				pb.Steps[i].Target = "host:" + inc.HostID
			}
		}
		if len(pb.Steps) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "剧本缺少步骤"})
			return
		}
		saved, err := s.playbooks.Upsert(pb)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		pb = saved
	}
	if title == "" {
		title = "闭环修复提案 · " + trimLine(inc.Title, 48)
	}
	run, err := s.remediation.ProposeManual(pb, inc.HostID, inc.Hostname, inc.ID, title, actor)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	loop.RemediationRunID = run.ID
	loop.Stage = "proposed"
	loop.Force = force
	inc, _ = s.incidents.SetLoop(inc.ID, *loop)
	s.incidents.AddEvent(inc.ID, "note", actor, fmt.Sprintf("闭环提案：修复运行 #%d（剧本 %s）", run.ID, pb.Name))
	if s.store != nil {
		s.store.MarkDirty()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": run, "playbook_id": pb.ID, "loop": inc.Loop, "gate": gate})
}

func (s *Server) loopApprove(w http.ResponseWriter, r *http.Request, inc Incident, actor string) {
	loop := ensureLoop(&inc)
	if loop.RemediationRunID > 0 {
		if err := s.remediation.Approve(loop.RemediationRunID, actor); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		loop.Stage = "approved"
		inc, _ = s.incidents.SetLoop(inc.ID, *loop)
		s.incidents.AddEvent(inc.ID, "note", actor, fmt.Sprintf("闭环批准修复运行 #%d", loop.RemediationRunID))
		if s.store != nil {
			s.store.MarkDirty()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "loop": inc.Loop, "remediation_run_id": loop.RemediationRunID})
		return
	}
	if loop.ChangeID > 0 {
		_, freeze := s.cfg.activeFreezeForHosts([]string{inc.HostID}, time.Now().Unix())
		out, err := s.changes.Transition(loop.ChangeID, "approve", actor, freeze)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		loop.Stage = "approved"
		inc, _ = s.incidents.SetLoop(inc.ID, *loop)
		if s.store != nil {
			s.store.MarkDirty()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "loop": inc.Loop, "change": out})
		return
	}
	writeJSON(w, http.StatusConflict, map[string]string{"error": "无待批准的修复运行或变更"})
}

func (s *Server) loopVerify(w http.ResponseWriter, r *http.Request, inc Incident, actor string) {
	loop := ensureLoop(&inc)
	notes := []string{}
	ok := true
	hostOK, alertQuiet, hostSignalOK, remOK, serviceQuiet := true, true, true, true, true

	if strings.TrimSpace(inc.HostID) != "" && s.store != nil {
		if h, found := s.store.GetHost(inc.HostID); !found || h == nil {
			hostOK = false
			ok = false
			notes = append(notes, "主机不存在，无法回验")
		} else if time.Now().Unix()-h.LastSeen > 180 {
			hostOK = false
			ok = false
			notes = append(notes, "主机仍离线或心跳超时")
		} else {
			notes = append(notes, "主机在线")
		}
	}
	if inc.Key != "" && s.notifier != nil {
		still := false
		for _, a := range s.notifier.ActiveAlerts() {
			if alertKey(a) == inc.Key {
				still = true
				break
			}
		}
		if still {
			alertQuiet = false
			ok = false
			notes = append(notes, "关联告警仍在 firing")
		} else {
			notes = append(notes, "关联告警已恢复或不存在")
		}
	}
	if sigOK, sigNotes := s.loopVerifyHostSignals(inc); true {
		notes = append(notes, sigNotes...)
		if !sigOK {
			hostSignalOK = false
			ok = false
		}
	}
	// Remediation run evidence
	if loop.RemediationRunID > 0 {
		if run, found := s.findRemediationRun(loop.RemediationRunID); found {
			switch run.Status {
			case "success":
				notes = append(notes, "修复运行 #"+strconv.FormatInt(run.ID, 10)+" 成功")
			case "failed", "rejected":
				remOK = false
				ok = false
				notes = append(notes, "修复运行 #"+strconv.FormatInt(run.ID, 10)+" 状态="+run.Status)
			case "pending_approval", "running":
				remOK = false
				ok = false
				notes = append(notes, "修复运行尚未完成（"+run.Status+"）")
			default:
				notes = append(notes, "修复运行状态="+run.Status)
			}
		} else {
			notes = append(notes, "关联修复运行未在内存中找到（可能已落 PG）")
		}
	}
	// Service quiet: open incidents on same host should not have grown for linked services
	if s.cfg != nil && strings.TrimSpace(inc.HostID) != "" {
		for _, svc := range s.cfg.BusinessServices() {
			hit := false
			for _, h := range svc.HostIDs {
				if h == inc.HostID {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			imp := s.computeServiceImpact(svc)
			for _, oi := range imp.OpenIncidents {
				oid, _ := oi["id"].(int64)
				if oid == 0 {
					if f, ok2 := oi["id"].(float64); ok2 {
						oid = int64(f)
					}
				}
				if oid != inc.ID && oid > 0 {
					// Other open incidents on the service — soft warn, fail if critical sibling
					if sev, _ := oi["severity"].(string); strings.EqualFold(sev, "critical") {
						serviceQuiet = false
						ok = false
						notes = append(notes, "业务服务「"+svc.Name+"」仍有其他危急未决事件")
						break
					}
				}
			}
			if !serviceQuiet {
				break
			}
			notes = append(notes, "业务服务「"+svc.Name+"」影响面无新增危急事件")
			break
		}
	}
	if inc.Status == "resolved" {
		notes = append(notes, "事件已解决")
	}
	v := ok
	loop.VerifyOK = &v
	loop.VerifyAt = time.Now().Unix()
	loop.VerifyNotes = notes
	if ok {
		loop.Stage = "verified"
	}
	inc, _ = s.incidents.SetLoop(inc.ID, *loop)
	s.incidents.AddEvent(inc.ID, "note", actor, "闭环回验："+strings.Join(notes, "；"))

	verifyPayload, _ := json.Marshal(map[string]any{
		"ok": ok, "notes": notes, "citations_present": latestDiagnosisGate(inc).Citations > 0,
		"incident_id": inc.ID, "at": loop.VerifyAt,
		"host_ok": hostOK, "alert_quiet": alertQuiet, "host_signal_ok": hostSignalOK,
		"remediation_ok": remOK, "service_quiet": serviceQuiet,
	})
	if loop.RunID != "" {
		s.patchAIRunVerify(loop.RunID, verifyPayload, ok)
	} else {
		s.persistAIRun(AIRun{
			ID:   "loop-verify-" + strconv.FormatInt(inc.ID, 10) + "-" + strconv.FormatInt(loop.VerifyAt, 10),
			Kind: "diagnose", Task: "loop_verify", Actor: actor, IncidentID: inc.ID,
			OK: ok, VerifyJSON: verifyPayload, Answer: strings.Join(notes, "; "),
		})
	}
	// Learning: verify_ok → mark verified + reinforce + write verified resolution card.
	if ok {
		src := "incident:" + strconv.FormatInt(inc.ID, 10)
		if s.pg != nil {
			s.pg.markMemoryVerifiedBySource("diagnosis", src)
			s.pg.markMemoryVerifiedBySource("resolution", src)
		}
		s.reinforceMemoryBySource("diagnosis", src, reinforceResolved)
		s.reinforceMemoryBySource("resolution", src, reinforceResolved)
		gate := latestDiagnosisGate(inc)
		corpus := strings.TrimSpace(gate.Text)
		if corpus == "" {
			corpus = inc.Title
		}
		card := fmt.Sprintf("【回验通过结案】%s\n主机：%s\n回验：%s\n诊断摘要：%s",
			inc.Title, firstNonEmptyOrDash(inc.Hostname, inc.HostID), strings.Join(notes, "；"), trimLine(corpus, 1200))
		s.rememberFromIncident(inc, "resolution", card, true)
	}
	if s.store != nil {
		s.store.MarkDirty()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": ok, "loop": inc.Loop, "notes": notes,
		"checks": map[string]bool{
			"host_ok": hostOK, "alert_quiet": alertQuiet, "host_signal_ok": hostSignalOK,
			"remediation_ok": remOK, "service_quiet": serviceQuiet,
		},
	})
}

func (s *Server) loopPromote(w http.ResponseWriter, r *http.Request, inc Incident, actor string, force bool) {
	loop := ensureLoop(&inc)
	if loop.VerifyOK == nil || !*loop.VerifyOK {
		if !force {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "回验未通过，不能沉淀 Skill（需 verify_ok 或管理员 force）"})
			return
		}
		if !s.actorIsAdminName(actor) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "未回验通过时仅管理员可强制沉淀"})
			return
		}
	}
	gate := latestDiagnosisGate(inc)
	corpus := gate.Text
	if corpus == "" {
		corpus = inc.Title + "\n" + strings.Join(loop.VerifyNotes, "\n")
	}
	status := "active"
	if loop.VerifyOK == nil || !*loop.VerifyOK {
		status = "draft"
	}
	created, updated, err := s.promoteTextToSkillSyncStatus("loop_verify", fmt.Sprintf("incident:%d", inc.ID), corpus, status)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	loop.SkillPromoted = true
	loop.Stage = "promoted"
	inc, _ = s.incidents.SetLoop(inc.ID, *loop)
	s.incidents.AddEvent(inc.ID, "note", actor, fmt.Sprintf("闭环沉淀 Skill（status=%s created=%v updated=%v）", status, created, updated))
	if s.store != nil {
		s.store.MarkDirty()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": created, "updated": updated, "status": status, "loop": inc.Loop})
}

// loopRunDemo is a one-click Year-1 sales/acceptance demo:
// seed diagnosis evidence if missing → dry-run → propose → approve → verify → promote.
// Admin-only; uses force where gates would otherwise block a greenfield incident.
func (s *Server) loopRunDemo(w http.ResponseWriter, r *http.Request, inc Incident, actor string) {
	if !s.actorIsAdmin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "一键闭环 Demo 仅管理员可用"})
		return
	}
	if strings.TrimSpace(inc.HostID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "事件未关联主机：请先编辑事件绑定主机，或新建带 host_id 的演示事件"})
		return
	}

	results := map[string]any{}
	gate := latestDiagnosisGate(inc)
	if !gate.OK {
		s.incidents.AddEventWithCitations(inc.ID, "ai_diagnosis", "demo",
			"【Demo】主机负载偏高，建议检查热点进程并确认变更窗。置信度：高",
			[]RAGCitation{
				{Kind: "metric", Source: "metrics", Title: "CPU", Summary: "cpu_percent=92"},
				{Kind: "alert", Source: "alert", Title: "CPU 告警", Summary: "cpu_high firing"},
			})
		inc, _ = s.incidents.Get(inc.ID)
		results["seed_diagnosis"] = true
	}

	runStep := func(name string, fn func(http.ResponseWriter)) map[string]any {
		rec := newDemoRecorder()
		fn(rec)
		out := map[string]any{"status": rec.code}
		var payload any
		if json.Unmarshal(rec.body, &payload) == nil {
			out["body"] = payload
		} else {
			out["raw"] = string(rec.body)
		}
		results[name] = out
		inc, _ = s.incidents.Get(inc.ID)
		return out
	}

	runStep("dry-run", func(rw http.ResponseWriter) { s.loopDryRun(rw, r, inc, actor, true) })
	runStep("propose", func(rw http.ResponseWriter) {
		s.loopPropose(rw, r, inc, actor, true, map[string]any{
			"title": "Demo 闭环修复提案 · " + trimLine(inc.Title, 40),
		})
	})
	runStep("approve", func(rw http.ResponseWriter) { s.loopApprove(rw, r, inc, actor) })
	runStep("verify", func(rw http.ResponseWriter) { s.loopVerify(rw, r, inc, actor) })
	runStep("promote", func(rw http.ResponseWriter) { s.loopPromote(rw, r, inc, actor, true) })

	s.incidents.AddEvent(inc.ID, "note", actor, "一键闭环 Demo 已执行（dry-run→propose→approve→verify→promote）")
	if s.store != nil {
		s.store.MarkDirty()
	}
	inc, _ = s.incidents.Get(inc.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "loop": inc.Loop, "results": results,
		"hint": "销售/验收一键演示完成；详见 docs/engineering/year1-acceptance.md 与 scripts/demo-year1-loop.sh",
	})
}

// demoRecorder is a minimal ResponseWriter for composing loop steps without httptest.
type demoRecorder struct {
	code int
	hdr  http.Header
	body []byte
}

func newDemoRecorder() *demoRecorder {
	return &demoRecorder{code: http.StatusOK, hdr: make(http.Header)}
}
func (d *demoRecorder) Header() http.Header { return d.hdr }
func (d *demoRecorder) Write(b []byte) (int, error) {
	d.body = append(d.body, b...)
	return len(b), nil
}
func (d *demoRecorder) WriteHeader(statusCode int) { d.code = statusCode }

func (s *Server) patchAIRunVerify(runID string, verify json.RawMessage, ok bool) {
	if s == nil || strings.TrimSpace(runID) == "" {
		return
	}
	run, found := s.lookupAIRun(runID)
	if !found {
		run = AIRun{ID: runID, Kind: "diagnose", Task: "diagnose"}
	}
	run.VerifyJSON = verify
	run.OK = ok
	s.persistAIRun(run)
}

// diagnosisGateAllowsPropose is used by remediation-propose handler.
func (s *Server) diagnosisGateAllowsPropose(inc Incident, force bool) (diagnosisGateResult, error) {
	gate := latestDiagnosisGate(inc)
	if gate.OK || force {
		return gate, nil
	}
	return gate, fmt.Errorf("%s", gate.Reason)
}
