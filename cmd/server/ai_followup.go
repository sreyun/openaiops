package main

// AI 对话闭环：把「只能被读」的 AI 结论一键转成运维动作。
//
// 在这之前，AI 给出结论后要据此建工单 / 提自愈 / 关联变更，只能人工离开对话、
// 换个页面、把结论重新敲一遍——链路在「决策」这一段断了。这里补上最后一跳。
//
// 反投毒（与 docs/ci-gate.md 里 /ai/assist/feedback 的规则同源）：动作按钮只回传
// 服务端签发的 run_id，正文一律用 AIRun 里的服务端原文，**绝不采信客户端传来的
// 结论文本**。否则任何人都能借「AI 说的」把任意内容写进工单和事件时间线。
// 客户端能影响的只有标题这类短标签，且长度与内容都在服务端收敛。
//
// 审批：这里只做「加法」写入（建工单 / 记时间线 / 关联变更），全部可审计、可撤回，
// 不新增任何绕过 approval_id 的执行旁路。真正会动生产的自愈走 propose_remediation，
// 它是**纯导航**动作——把人送到既有的修复提案对话框，最终仍落 pending_approval
// 由人批准，见 handleIncidentProposeRemediation。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// 闭环动作类型。与 opsAllowedUI 的白名单成对存在，改一处要改两处。
const (
	aiActCreateTicket       = "create_ticket"
	aiActAddIncidentNote    = "add_incident_note"
	aiActLinkChange         = "link_change"
	aiActProposeRemediation = "propose_remediation"
)

// aiFollowupWriteActions 是会真正写库的那几个；propose_remediation 不在其中（纯导航）。
var aiFollowupWriteActions = map[string]bool{
	aiActCreateTicket:    true,
	aiActAddIncidentNote: true,
	aiActLinkChange:      true,
}

// aiFollowupMinRunes 是挂动作区的最短回答长度。太短的回答（寒暄、一句确认）挂上
// 「建工单」只会变成噪音，反而让真正该转成动作的结论淹没在按钮里。
const aiFollowupMinRunes = 80

// aiFollowupActions 组装一条回答下方的动作区。
//
// runID 为空（模型没产出可追溯的 run）或 actor 只读时返回 nil——只读角色点了也会被
// 路由层挡下，不如不给按钮。事件相关的三个动作只在会话确实挂着事件时才出现。
func aiFollowupActions(runID, reply string, incidentID int64, canOperate bool) []map[string]any {
	if !canOperate || strings.TrimSpace(runID) == "" {
		return nil
	}
	if len([]rune(strings.TrimSpace(reply))) < aiFollowupMinRunes {
		return nil
	}
	base := func(typ, label string) map[string]any {
		act := map[string]any{"type": typ, "label": label, "run_id": runID}
		if incidentID > 0 {
			act["incident_id"] = incidentID
		}
		return act
	}
	actions := []map[string]any{base(aiActCreateTicket, "建工单")}
	if incidentID > 0 {
		actions = append(actions,
			base(aiActAddIncidentNote, "记入事件时间线"),
			base(aiActProposeRemediation, "提自愈"),
			base(aiActLinkChange, "关联变更"),
		)
	}
	return filterUIActions(actions)
}

// emitAIFollowupActions 把动作区作为 SSE action 帧发给前端，格式与工具产出的
// _ui_actions 完全一致，前端因此不需要第二套解析路径。
func (s *Server) emitAIFollowupActions(w http.ResponseWriter, r *http.Request, runID, reply string, incidentID int64) {
	canOperate := false
	if u, ok := s.currentUser(r); ok {
		canOperate = roleRank(u.Role) >= roleRank(RoleOperator)
	}
	for _, act := range aiFollowupActions(runID, reply, incidentID, canOperate) {
		b, err := json.Marshal(act)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "data: {\"action\":%s}\n\n", b)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// aiFollowupProvenance 是写进工单/时间线正文的来源标记，让「这段话是谁说的」
// 在离开对话之后仍然查得到。
func aiFollowupProvenance(runID string) string {
	return "\n\n———\n来源：AI 运维助手（run " + runID + "）"
}

// handleAIFollowup 执行一个闭环写动作。
// POST /api/v1/ai/followup  {run_id, action, incident_id?, change_id?, title?}
func (s *Server) handleAIFollowup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID      string `json:"run_id"`
		Action     string `json:"action"`
		IncidentID int64  `json:"incident_id,omitempty"`
		ChangeID   int64  `json:"change_id,omitempty"`
		Title      string `json:"title,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if !aiFollowupWriteActions[action] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未知的 AI 闭环动作"})
		return
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 run_id"})
		return
	}
	// 服务端原文是唯一可信来源：run 查不到就没有可落库的结论，直接拒绝。
	run, found := s.lookupAIRun(runID)
	if !found || strings.TrimSpace(run.Answer) == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "AI 结论已过期或不存在，请重新生成后再操作",
		})
		return
	}
	answer := strings.TrimSpace(run.Answer)
	actor := s.actorName(r)
	// 标题允许前端给，但只当短标签用：截断并去掉换行，正文永远来自服务端。
	title := trimLine(strings.TrimSpace(req.Title), 120)

	switch action {
	case aiActCreateTicket:
		if title == "" {
			title = "AI 结论 · " + trimLine(firstNonEmpty(run.Task, "运维分析"), 40)
		}
		tk, err := s.tickets.Create(Ticket{
			Title:       title,
			Description: answer + aiFollowupProvenance(runID),
			Kind:        "task",
			Priority:    "p3",
			Source:      "ai",
			IncidentID:  req.IncidentID,
		}, actor)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// 记下这张工单出自哪条 AI 结论。工单最终被解决，是这条结论「确实管用」的
		// 客观回验信号——比人点一次「有用」强得多，见 learnFromAIFollowupTicket。
		s.tickets.ApplyPostCreate(tk.ID, func(t *Ticket) { t.AIRunID = runID })
		tk = s.finalizeNewTicket(tk) // re-reads the stored ticket, so AIRunID rides along
		s.store.MarkDirty()
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: actor, IP: s.clientIP(r),
			Message: fmt.Sprintf("AI 结论转工单 #%d「%s」（run %s）", tk.ID, tk.Title, runID)})
		s.messages.push("ticket", "info", "新工单："+tk.Title,
			fmt.Sprintf("由 AI 结论转入 · 优先级 %s · 状态 %s", tk.Priority, tk.Status), "sre", strconv.FormatInt(tk.ID, 10))
		s.recordAIFollowupAdoption(run, actor)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ticket": tk})

	case aiActAddIncidentNote:
		if req.IncidentID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 incident_id"})
			return
		}
		if _, ok := s.incidents.Get(req.IncidentID); !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "事件不存在"})
			return
		}
		s.incidents.AddEvent(req.IncidentID, "ai_analysis", actor, answer+aiFollowupProvenance(runID))
		s.store.MarkDirty()
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: actor, IP: s.clientIP(r),
			Message: fmt.Sprintf("AI 结论记入事件#%d 时间线（run %s）", req.IncidentID, runID)})
		s.recordAIFollowupAdoption(run, actor)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "incident_id": req.IncidentID})

	case aiActLinkChange:
		if req.IncidentID <= 0 || req.ChangeID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 incident_id 或 change_id"})
			return
		}
		if _, ok := s.incidents.Get(req.IncidentID); !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "事件不存在"})
			return
		}
		rec, err := s.changes.LinkIncident(req.ChangeID, req.IncidentID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.incidents.AddLinks(req.IncidentID, []OpsLink{changeOpsLink(req.ChangeID)}, actor, "AI 结论关联")
		// 关联本身不带上下文，把 AI 的判断依据一并留在时间线上，事后才说得清为什么关联。
		s.incidents.AddEvent(req.IncidentID, "change_correlation", actor,
			fmt.Sprintf("关联变更 #%d：%s%s", req.ChangeID, trimLine(rec.Title, 80), aiFollowupProvenance(runID)))
		s.store.MarkDirty()
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: actor, IP: s.clientIP(r),
			Message: fmt.Sprintf("AI 结论关联变更 #%d ↔ 事件#%d（run %s）", req.ChangeID, req.IncidentID, runID)})
		s.recordAIFollowupAdoption(run, actor)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "change": rec})
	}
}
