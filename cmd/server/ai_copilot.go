package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handleAICopilotContext aggregates on-call / incident / skill hints for the
// On-call Copilot workspace (Wave 3 minimal surface).
// GET /api/v1/ai/copilot/context
func (s *Server) handleAICopilotContext(w http.ResponseWriter, r *http.Request) {
	duty, notable := s.buildDutyReportContext()

	var open []map[string]any
	// 这段上下文会直接进 AI 对话：不过滤就等于把范围外主机的事件标题、主机名喂给
	// 只该看到自己那几台机器的人。
	for _, inc := range s.filterIncidentsForUser(r, s.incidents.List()) {
		if inc.Status == "resolved" {
			continue
		}
		open = append(open, map[string]any{
			"id": inc.ID, "title": inc.Title, "severity": inc.Severity,
			"type": inc.Type, "host": firstNonEmptyOrDash(inc.Hostname, inc.HostID),
			"status": inc.Status, "created_at": inc.CreatedAt,
		})
		if len(open) >= 12 {
			break
		}
	}

	var pendingRem []map[string]any
	for _, run := range s.remediation.Runs() {
		if run.Status != "pending_approval" {
			continue
		}
		pendingRem = append(pendingRem, map[string]any{
			"rule": run.RuleName, "playbook": run.PlaybookName,
			"host": run.Hostname, "alert": run.AlertType,
		})
		if len(pendingRem) >= 8 {
			break
		}
	}

	suggestions := []string{
		"用 AI 诊断最高优先级未决事件",
		"核对待审批自动修复是否可执行",
		"导入行业知识包（MySQL/PG/K8s/网络）补充技能",
		"对关键 SQL/PromQL 用 Assist 生成并只读验证",
	}
	if !notable && len(open) == 0 {
		suggestions = []string{"态势平稳：回顾昨日 AI 巡检与慢 SQL", "检查 MCP/数据源连通性", "更新值班通讯录与升级策略"}
	}

	var skillHints []map[string]any
	if s.pg != nil && len(open) > 0 {
		q := fmt.Sprintf("%s %s", open[0]["title"], open[0]["type"])
		if _, names, hits, _ := s.retrieveSkillsDetailed(q, 3); hits > 0 {
			for _, n := range names {
				skillHints = append(skillHints, map[string]any{"name": n})
			}
		}
	}

	packs, _ := listEmbeddedSkillPacks()

	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at":        time.Now().Unix(),
		"notable":             notable,
		"duty_context":        duty,
		"open_incidents":      open,
		"pending_remediation": pendingRem,
		"skill_hints":         skillHints,
		"suggestions":         suggestions,
		"skill_packs":         packs,
		"assist_hint":         "可对未决事件一键打开诊断，或用 Assist task=generic 生成值班摘要",
	})
}

// streamChatWithFallback retries stream with FallbackModels on hard failure.
// Returns the model that produced the reply (primary or fallback).
func (s *Server) streamChatWithFallback(ctx context.Context, w http.ResponseWriter, cfg AIConfig, messages []map[string]string, images []chatImage, sendDone bool, opts aiCallOpts) (string, string, error) {
	used := cfg.Model
	reply, err := streamChatInnerOpts(ctx, w, cfg, messages, images, false, opts)
	if err == nil && strings.TrimSpace(reply) != "" {
		if sendDone {
			fmt.Fprint(w, "data: [DONE]\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		return reply, used, nil
	}
	for _, model := range fallbackModelList(cfg) {
		retry := cfg
		retry.Model = model
		fmt.Fprintf(w, "data: {\"meta\":{\"fallback_model\":%s}}\n\n", jsonString(model))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		reply2, err2 := streamChatInnerOpts(ctx, w, retry, messages, images, false, opts)
		if err2 == nil && strings.TrimSpace(reply2) != "" {
			if sendDone {
				fmt.Fprint(w, "data: [DONE]\n\n")
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			return reply2, model, nil
		}
		err = err2
	}
	if sendDone {
		if err != nil {
			fmt.Fprintf(w, "data: {\"error\":%s}\n\n", jsonString(err.Error()))
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	return reply, used, err
}
