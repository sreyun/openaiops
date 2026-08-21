package main

// 闭环的最后一段：让 AI 知道自己说对了没有。
//
// 在这之前，一条 AI 结论的「是否有用」只有一个来源——人在回答下面点的那个赞
// （/ai/assist/feedback）。点赞是主观的、稀疏的，而且多数人根本不点。于是记忆库里
// 大量条目永远停在 verified=false，检索时和没验证过的猜测混在一起。
//
// 这里接的是两个**客观**信号，它们不需要任何人表态：
//
//   1. 由 AI 结论建出的工单被解决了  → 那条结论确实推动了问题的解决；
//   2. 自愈剧本跑完后回看，告警真的消失了（remediation.Verify == "cleared"）
//      → 这套处置在现实里管用；反过来 still_firing 就是「跑完了但没修好」，
//        必须让它在检索里下沉，否则下次还会被推荐同一套无效动作。
//
// 两条路径都只写**服务端原文**（AIRun.Answer / 运行记录本身），与
// docs/ci-gate.md 里 /ai/assist/feedback 的反投毒规则同源：客户端能触发回验，
// 但决定不了被写进记忆的内容。

import (
	"fmt"
	"log/slog"
	"strings"
)

// aiRunMemorySource 是 AI 结论在记忆库里的来源键。run id 是服务端签发的不透明串，
// 因此这个键天然唯一，可以直接用 markMemoryVerifiedBySource 精确命中。
func aiRunMemorySource(runID string) string {
	return "ai_run:" + strings.TrimSpace(runID)
}

// recordAIFollowupAdoption 记录「这条结论被人真的拿去做事了」。
//
// 和 /ai/assist/feedback 的 applied 是同一种信号，区别在于不需要人再点一次——把结论
// 转成工单本身就是最强的采纳表态。刻意**不**在这里把回答写进 knowledge 记忆：采纳只
// 说明有人信它，还不说明它对；入库留给 learnFromAIFollowupTicket 那条经现实回验的路径。
func (s *Server) recordAIFollowupAdoption(run AIRun, actor string) {
	if s == nil || strings.TrimSpace(run.ID) == "" {
		return
	}
	task := firstNonEmptyOrDash(run.Task, run.Kind, "sreyun")
	s.aiStats.recordFeedback(task, "applied")
	if text := strings.TrimSpace(run.Input + " " + run.Answer); text != "" {
		s.reinforceMemory("knowledge", text, reinforceApplied)
		s.reinforceSkill(text, reinforceApplied)
	}
	if s.pg == nil {
		return
	}
	s.pg.markAIRunFeedback(run.ID, "applied")
	src := "run:" + task + ":" + run.ID + ":" + run.ContentHash
	go s.pg.insertAIFeedbackEvent(task, actor, "applied", src)
}

// learnFromAIFollowupTicket 在「由 AI 结论建出的工单」被解决/关闭时，把那条结论
// 作为**已验证**经验写入记忆库。
//
// 刻意不在建工单的那一刻写：那时候还没有任何证据说明结论是对的，提前入库只会把
// 未经验证的推测混进 RAG。等到工单真的被解决再写，记忆库里就只有被现实确认过的条目。
func (s *Server) learnFromAIFollowupTicket(tk Ticket, outcome string) {
	if s == nil || s.pg == nil {
		return
	}
	runID := strings.TrimSpace(tk.AIRunID)
	if runID == "" {
		return
	}
	run, ok := s.lookupAIRun(runID)
	if !ok {
		return // run 已过期：没有服务端原文就没有可入库的内容，宁可不写
	}
	answer := strings.TrimSpace(run.Answer)
	if answer == "" {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【AI 结论经现实验证】\n工单 #%d：%s（%s）\n", tk.ID, trimLine(tk.Title, 120), outcome)
	if tk.IncidentID > 0 {
		fmt.Fprintf(&b, "关联事件：#%d\n", tk.IncidentID)
	}
	b.WriteString("\nAI 结论：\n")
	b.WriteString(answer)

	opts := memoryWriteOpts{Verified: true}
	if tk.IncidentID > 0 {
		if inc, found := s.incidents.Get(tk.IncidentID); found {
			opts.ServiceID, opts.Category = s.memoryScopeFromIncident(inc)
		}
	}
	src := aiRunMemorySource(runID)
	s.rememberAIScoped("resolution", src, b.String(), opts)
	// 同一条 run 可能早已以别的形式进过记忆（诊断卡、结案卡）：一并标记为已验证，
	// 否则同一件事会有一半条目验证过、一半没有。
	s.pg.markMemoryVerifiedBySource("diagnosis", src)
	s.pg.markMemoryVerifiedBySource("resolution", src)
	s.reinforceMemoryBySource("resolution", src, reinforceResolved)
	slog.Info("学习闭环·AI 结论经工单回验", "ticket", tk.ID, "run", runID, "outcome", outcome)
}

// learnFromRemediationVerify 把自愈回验的客观结论回流成记忆信号。
//
// 注意正负两侧都要接：只强化成功的话，一条「跑得完但修不好」的剧本会因为
// rememberPlaybookOutcome 里的执行成功而留在检索前排，永远得不到纠正。
func (s *Server) learnFromRemediationVerify(run RemediationRun) {
	if s == nil || s.pg == nil {
		return
	}
	pbSrc := "playbook:" + strings.TrimSpace(run.PlaybookID)
	incSrc := ""
	if run.IncidentID > 0 {
		incSrc = fmt.Sprintf("incident:%d", run.IncidentID)
	}
	switch run.Verify {
	case "cleared":
		if strings.TrimSpace(run.PlaybookID) != "" {
			text := fmt.Sprintf(
				"【自愈经回验有效】剧本：%s\n主机：%s\n告警：%s（%s）\n结果：剧本执行后回看，该告警已消除。",
				run.PlaybookName, run.Hostname, run.AlertKey, run.AlertType)
			opts := memoryWriteOpts{Verified: true}
			if s.store != nil && strings.TrimSpace(run.HostID) != "" {
				if h, ok := s.store.GetHost(run.HostID); ok && h != nil {
					opts.Category = h.Category
				}
			}
			s.rememberAIScoped("experience", pbSrc, text, opts)
			// 之前 rememberPlaybookOutcome 写的那条「执行成功」经验用的是同一个 source，
			// 这里把它一并升格：执行成功是过程，告警消除才是结果。
			s.pg.markMemoryVerifiedBySource("experience", pbSrc)
			s.reinforceMemoryBySource("experience", pbSrc, reinforceResolved)
		}
		if incSrc != "" {
			s.pg.markMemoryVerifiedBySource("diagnosis", incSrc)
			s.reinforceMemoryBySource("diagnosis", incSrc, reinforceResolved)
		}
		slog.Info("学习闭环·自愈回验有效", "run", run.ID, "playbook", run.PlaybookID, "incident", run.IncidentID)
	case "still_firing":
		if strings.TrimSpace(run.PlaybookID) != "" {
			s.reinforceMemoryBySource("experience", pbSrc, penalizeUnhelpful)
		}
		if incSrc != "" {
			s.reinforceMemoryBySource("diagnosis", incSrc, penalizeUnhelpful)
		}
		slog.Info("学习闭环·自愈回验未生效", "run", run.ID, "playbook", run.PlaybookID, "incident", run.IncidentID)
	}
}
