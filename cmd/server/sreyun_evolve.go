package main

import (
	"fmt"
	"strings"
	"time"
)

func (h *SreyunCore) registerEvolveTools() {
	h.tools["run_self_maintenance"] = SreyunTool{
		Name: "run_self_maintenance",
		Description: "触发 AI 自我维护：记忆衰减/清理/容量裁剪、技能提炼、成长日记总结。" +
			"用户说「自我优化」「整理记忆」「进化一下」时调用。只做只读维护与学习沉淀，不改生产配置。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"distill":       map[string]any{"type": "boolean", "description": "是否提炼技能，默认 true"},
				"journal":       map[string]any{"type": "boolean", "description": "是否写成长日记，默认 true"},
				"lookback_days": map[string]any{"type": "integer", "description": "技能提炼回溯天数，默认 14"},
			},
		},
		Execute: h.execRunSelfMaintenance,
	}
	h.tools["summarize_growth"] = SreyunTool{
		Name:        "summarize_growth",
		Description: "基于近期记忆与技能统计，生成一段自我成长/进化摘要（可写入向量记忆）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"persist": map[string]any{"type": "boolean", "description": "是否写入记忆，默认 true"},
			},
		},
		Execute: h.execSummarizeGrowth,
	}
}

func (h *SreyunCore) execRunSelfMaintenance(args map[string]any) (string, error) {
	distill := true
	journal := true
	if v, ok := args["distill"].(bool); ok {
		distill = v
	}
	if v, ok := args["journal"].(bool); ok {
		journal = v
	}
	lookback := 14
	switch v := args["lookback_days"].(type) {
	case float64:
		lookback = int(v)
	case int:
		lookback = v
	}
	res := h.s.runSelfEvolution(selfEvolveOpts{Distill: distill, Journal: journal, LookbackDays: lookback, Actor: "ai-chat"})
	return capabilityJSON(capabilityResult{
		OK:      true,
		Summary: res.Summary,
		Data:    res.Data,
		Answer:  res.Journal,
	}), nil
}

func (h *SreyunCore) execSummarizeGrowth(args map[string]any) (string, error) {
	persist := true
	if v, ok := args["persist"].(bool); ok {
		persist = v
	}
	text, stats := h.s.buildGrowthJournal(persist)
	return capabilityJSON(capabilityResult{
		OK:      true,
		Summary: "已生成自我成长摘要",
		Answer:  text,
		Data:    stats,
	}), nil
}

type selfEvolveOpts struct {
	Distill      bool
	Journal      bool
	LookbackDays int
	Actor        string
}

type selfEvolveResult struct {
	Summary string
	Journal string
	Data    map[string]any
}

func (s *Server) runSelfEvolution(opts selfEvolveOpts) selfEvolveResult {
	if opts.LookbackDays <= 0 {
		opts.LookbackDays = 14
	}
	if opts.Actor == "" {
		opts.Actor = "self-evolve"
	}
	data := map[string]any{}
	parts := []string{}

	if s.pg != nil {
		s.pg.decayOldMemories()
		s.pg.cleanupExpiredMemories()
		s.pg.capMemoriesByKind(2000)
		if n := s.pg.archiveStaleSkills(); n > 0 {
			data["archived_skills"] = n
			parts = append(parts, fmt.Sprintf("归档技能 %d", n))
		}
		parts = append(parts, "记忆衰减/清理/裁剪完成")
		data["memory_kinds"] = s.pg.memoryKindStats()
	} else {
		parts = append(parts, "PG 不可用，跳过记忆维护")
	}

	if opts.Distill {
		n, err := s.distillSkills(opts.LookbackDays)
		if err != nil {
			data["distill_error"] = err.Error()
			parts = append(parts, "技能提炼跳过："+err.Error())
		} else {
			data["skills_distilled"] = n
			parts = append(parts, fmt.Sprintf("新提炼技能 %d", n))
		}
	}

	journal := ""
	if opts.Journal {
		journal, _ = s.buildGrowthJournal(true)
		data["journal_chars"] = len([]rune(journal))
		parts = append(parts, "已写入成长日记")
	}

	sum := strings.Join(parts, "；")
	s.store.AddLog(LogEntry{Kind: KindSystem, Level: "info", Actor: opts.Actor, Message: "AI 自我维护：" + sum})
	return selfEvolveResult{Summary: sum, Journal: journal, Data: data}
}

func (s *Server) buildGrowthJournal(persist bool) (string, map[string]any) {
	stats := map[string]any{"ts": time.Now().Unix()}
	var corpus strings.Builder
	corpus.WriteString("日期：" + time.Now().Format("2006-01-02") + "\n")
	if s.pg != nil {
		kinds := s.pg.memoryKindStats()
		stats["memory_kinds"] = kinds
		fmt.Fprintf(&corpus, "记忆分布：%v\n", kinds)
		verified := s.pg.countVerifiedMemories()
		stats["verified_memories"] = verified
		fmt.Fprintf(&corpus, "已验证记忆：%d\n", verified)
		mems := s.pg.memoriesForDistill(time.Now().Add(-7*24*time.Hour).Unix(), 12)
		for i, m := range mems {
			fmt.Fprintf(&corpus, "- [%s] %s\n", m.Kind, trimLine(m.Content, 160))
			if i >= 11 {
				break
			}
		}
	}
	cfg := s.cfg.AIConfig()
	text := ""
	if cfg.Enabled && cfg.Endpoint != "" && cfg.Model != "" {
		sys := "你是 AIOps 助手的自我进化模块。根据下列记忆统计与片段，用简洁中文写一段「今日成长日记」：" +
			"1) 学到了什么 2) 仍薄弱的能力 3) 下一步自我优化方向。不超过 220 字，不要标题。"
		out, err := aiComplete(cfg, sys, corpus.String())
		if err == nil {
			text = strings.TrimSpace(out)
		}
	}
	if text == "" {
		text = fmt.Sprintf("自我维护完成（%s）。记忆与技能已按生命周期策略衰减/裁剪；继续结合结案与安全防御事件迭代经验。",
			time.Now().Format("2006-01-02 15:04"))
	}
	if persist {
		s.rememberAI("growth", "self-evolve:"+time.Now().Format("20060102"), text)
	}
	stats["journal"] = text
	return text, stats
}

// maybeRunScheduledSelfEvolve is called from the daily maintenance ticker when enabled.
func (s *Server) maybeRunScheduledSelfEvolve() {
	_ = s.runSelfEvolution(selfEvolveOpts{Distill: true, Journal: true, LookbackDays: 14, Actor: "self-evolve-cron"})
}
