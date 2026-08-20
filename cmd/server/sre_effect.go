package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SREEffectReport aggregates ops effect KPIs (MTTR/MTTA, noise, change failure, AI adoption).
type SREEffectReport struct {
	GeneratedAt int64 `json:"generated_at"`
	WindowDays  int   `json:"window_days"`

	// Incidents / MTTR / MTTA
	IncidentCount   int     `json:"incident_count"`
	ResolvedCount   int     `json:"resolved_count"`
	AckedCount      int     `json:"acked_count"`
	MTTRP50Sec      int64   `json:"mttr_p50_sec"`
	MTTRP75Sec      int64   `json:"mttr_p75_sec"`
	MTTRP90Sec      int64   `json:"mttr_p90_sec"`
	MTTAP50Sec      int64   `json:"mtta_p50_sec"`
	MTTAP75Sec      int64   `json:"mtta_p75_sec"`
	ClosedLoopCount int     `json:"closed_loop_count"`
	ClosedLoopRate  float64 `json:"closed_loop_rate"`

	// Alert noise
	AlertNoiseRatio  float64 `json:"alert_noise_ratio"` // (reopen_keys + flap_keys) / resolved
	AlertReopenKeys  int     `json:"alert_reopen_keys"`
	AlertFlapKeys    int     `json:"alert_flap_keys"`
	AlertNoiseDetail string  `json:"alert_noise_detail,omitempty"`

	// Change / DORA CFR
	ChangeCount          int     `json:"change_count"`
	ChangeFailedCount    int     `json:"change_failed_count"`
	ChangeFailureRate    float64 `json:"change_failure_rate"`
	ChangeLeadTimeP75Sec int64   `json:"change_lead_time_p75_sec"`

	// AI adoption / verify
	AIRunCount       int     `json:"ai_run_count"`
	AIFeedbackCount  int     `json:"ai_feedback_count"`
	AIHelpfulCount   int     `json:"ai_helpful_count"`
	AIAdoptionRate   float64 `json:"ai_adoption_rate"`
	VerifyPassRate   float64 `json:"verify_pass_rate"`
	VerifySampleSize int     `json:"verify_sample_size"`
	AIFallbackCount  int     `json:"ai_fallback_count"`
	AIToolTurnRuns   int     `json:"ai_tool_turn_runs"`

	// Golden-set eval (他证): from ai_eval_runs, not self-verification.
	EvalRunCount    int     `json:"eval_run_count"`
	EvalPassedCount int     `json:"eval_passed_count"`
	EvalPassRate    float64 `json:"eval_pass_rate"`
	EvalSetVersion  string  `json:"eval_set_version"`
	EvalModel       string  `json:"eval_model"`
	EvalMode        string  `json:"eval_mode"`

	// Learning assets (private Skills / memory compounding)
	SkillHitRuns            int     `json:"skill_hit_runs"`
	MemoryHitRuns           int     `json:"memory_hit_runs"`
	SkillAssistedVerifyRate float64 `json:"skill_assisted_verify_rate"`
	SkillDraftCount         int     `json:"skill_draft_count"`
	SkillActiveCount        int     `json:"skill_active_count"`
	SkillDraftActiveRatio   float64 `json:"skill_draft_active_ratio"` // draft / (draft+active)
	MemoryVerifiedCount     int     `json:"memory_verified_count"`
	MemoryTotalCount        int     `json:"memory_total_count"`

	Notes []string `json:"notes,omitempty"`
}

func percentileSorted(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func changeIsFailed(c ChangeRecord, incidents []Incident) bool {
	st := normalizeChangeStatus(c.Status)
	if st == ChangeRolledBack {
		return true
	}
	if st != ChangeCompleted && st != ChangeInProgress {
		return false
	}
	if len(c.LinkedIncidentIDs) == 0 {
		return false
	}
	start := c.StartedAt
	if start <= 0 {
		start = c.CreatedAt
	}
	if start <= 0 {
		return false
	}
	windowEnd := start + 24*3600
	idSet := map[int64]bool{}
	for _, id := range c.LinkedIncidentIDs {
		idSet[id] = true
	}
	for _, inc := range incidents {
		if !idSet[inc.ID] {
			continue
		}
		if inc.CreatedAt >= start && inc.CreatedAt <= windowEnd {
			return true
		}
	}
	return false
}

func (s *Server) computeSREEffect(windowDays int) SREEffectReport {
	if windowDays <= 0 {
		windowDays = 14
	}
	since := time.Now().Add(-time.Duration(windowDays) * 24 * time.Hour).Unix()
	rep := SREEffectReport{GeneratedAt: time.Now().Unix(), WindowDays: windowDays}

	var mttrs, mttas []int64
	keyResolvedN := map[string]int{}
	keyOpen := map[string]int{}
	var allIncidents []Incident
	if s.incidents != nil {
		allIncidents = s.incidents.List()
		for _, inc := range allIncidents {
			if inc.CreatedAt < since {
				continue
			}
			rep.IncidentCount++
			if inc.AckedAt > 0 && inc.AckedAt >= inc.CreatedAt {
				rep.AckedCount++
				mttas = append(mttas, inc.AckedAt-inc.CreatedAt)
			}
			if inc.Status == "resolved" && inc.ResolvedAt > 0 {
				rep.ResolvedCount++
				mttrs = append(mttrs, inc.ResolvedAt-inc.CreatedAt)
				if inc.Key != "" {
					keyResolvedN[inc.Key]++
				}
			}
			if inc.Loop != nil && inc.Loop.VerifyOK != nil && *inc.Loop.VerifyOK {
				rep.ClosedLoopCount++
			}
			if inc.Key != "" && inc.Status != "resolved" {
				keyOpen[inc.Key]++
			}
		}
	}
	sort.Slice(mttrs, func(i, j int) bool { return mttrs[i] < mttrs[j] })
	sort.Slice(mttas, func(i, j int) bool { return mttas[i] < mttas[j] })
	rep.MTTRP50Sec = percentileSorted(mttrs, 0.50)
	rep.MTTRP75Sec = percentileSorted(mttrs, 0.75)
	rep.MTTRP90Sec = percentileSorted(mttrs, 0.90)
	rep.MTTAP50Sec = percentileSorted(mttas, 0.50)
	rep.MTTAP75Sec = percentileSorted(mttas, 0.75)
	if rep.IncidentCount > 0 {
		rep.ClosedLoopRate = float64(rep.ClosedLoopCount) / float64(rep.IncidentCount)
	}

	reopenKeys, flapKeys := 0, 0
	for k, n := range keyResolvedN {
		if n >= 2 {
			flapKeys++
		}
		if keyOpen[k] > 0 {
			reopenKeys++
		}
	}
	rep.AlertReopenKeys = reopenKeys
	rep.AlertFlapKeys = flapKeys
	if rep.ResolvedCount > 0 {
		rep.AlertNoiseRatio = float64(reopenKeys+flapKeys) / float64(rep.ResolvedCount)
	}
	rep.AlertNoiseDetail = "reopen_keys+flap_keys / resolved"

	var leadTimes []int64
	if s.changes != nil {
		for _, c := range s.changes.List() {
			ts := c.StartedAt
			if ts <= 0 {
				ts = c.CreatedAt
			}
			if ts < since {
				continue
			}
			st := normalizeChangeStatus(c.Status)
			if st != ChangeCompleted && st != ChangeRolledBack && st != ChangeInProgress {
				continue
			}
			rep.ChangeCount++
			if changeIsFailed(c, allIncidents) {
				rep.ChangeFailedCount++
			}
			end := c.EndedAt
			if end <= 0 {
				end = c.ExecutedAt
			}
			if end <= 0 {
				end = c.UpdatedAt
			}
			created := c.CreatedAt
			if created <= 0 {
				created = c.StartedAt
			}
			if end > created && created > 0 {
				leadTimes = append(leadTimes, end-created)
			}
		}
	}
	if rep.ChangeCount > 0 {
		rep.ChangeFailureRate = float64(rep.ChangeFailedCount) / float64(rep.ChangeCount)
	}
	sort.Slice(leadTimes, func(i, j int) bool { return leadTimes[i] < leadTimes[j] })
	rep.ChangeLeadTimeP75Sec = percentileSorted(leadTimes, 0.75)

	feedbackN, helpfulN := 0, 0
	verifyN, verifyOK := 0, 0
	if s.pg != nil {
		list, err := s.pg.listAIRunsSince(since, 2000)
		if err != nil {
			// fallback to recent list
			list, _ = s.pg.listAIRuns("", 500)
		}
		for _, run := range list {
			if run.CreatedAt < since {
				continue
			}
			rep.AIRunCount++
			fb := strings.ToLower(strings.TrimSpace(run.Feedback))
			if fb != "" {
				feedbackN++
				if fb == "helpful" || fb == "applied" {
					helpfulN++
				}
			}
			if len(run.VerifyJSON) > 0 && string(run.VerifyJSON) != "null" {
				verifyN++
				var m map[string]any
				if json.Unmarshal(run.VerifyJSON, &m) == nil {
					if v, ok := m["ok"].(bool); ok && v {
						verifyOK++
					}
				}
			}
			if run.SkillHits > 0 {
				rep.SkillHitRuns++
			}
			if run.MemHits > 0 {
				rep.MemoryHitRuns++
			}
			if len(run.MetaJSON) > 0 && string(run.MetaJSON) != "null" {
				var meta AgentLoopMeta
				if json.Unmarshal(run.MetaJSON, &meta) == nil {
					if meta.FallbackModel != "" {
						rep.AIFallbackCount++
					}
					if meta.ToolTurns > 0 {
						rep.AIToolTurnRuns++
					}
				}
			}
		}
		skillAssistOK := 0
		skillAssistN := 0
		for _, run := range list {
			if run.CreatedAt < since || run.SkillHits <= 0 {
				continue
			}
			if len(run.VerifyJSON) == 0 || string(run.VerifyJSON) == "null" {
				continue
			}
			skillAssistN++
			var m map[string]any
			if json.Unmarshal(run.VerifyJSON, &m) == nil {
				if v, ok := m["ok"].(bool); ok && v {
					skillAssistOK++
				}
			}
		}
		if skillAssistN > 0 {
			rep.SkillAssistedVerifyRate = float64(skillAssistOK) / float64(skillAssistN)
		}
		rep.SkillDraftCount = s.pg.countSkillsByStatus("draft")
		rep.SkillActiveCount = s.pg.countSkillsByStatus("active")
		if den := rep.SkillDraftCount + rep.SkillActiveCount; den > 0 {
			rep.SkillDraftActiveRatio = float64(rep.SkillDraftCount) / float64(den)
		}
		rep.MemoryVerifiedCount = s.pg.countVerifiedMemories()
		if st := s.pg.memoryKindStats(); len(st) > 0 {
			for _, n := range st {
				rep.MemoryTotalCount += n
			}
		}
	} else {
		rep.Notes = append(rep.Notes, "无 PostgreSQL：AI Runs / 验证率仅部分可用")
	}
	rep.AIFeedbackCount = feedbackN
	rep.AIHelpfulCount = helpfulN
	if feedbackN > 0 {
		rep.AIAdoptionRate = float64(helpfulN) / float64(feedbackN)
	}
	rep.VerifySampleSize = verifyN
	if verifyN > 0 {
		rep.VerifyPassRate = float64(verifyOK) / float64(verifyN)
	}
	// 黄金集评测通过率（他证）：来自最近一次 ai_eval_runs，供周报引用可审计数字。
	if s.pg != nil {
		if ev, err := s.pg.latestEvalRun(); err == nil && ev.RunID != "" {
			rep.EvalRunCount = ev.CaseCount
			rep.EvalPassedCount = ev.PassedCount
			rep.EvalPassRate = ev.PassRate
			rep.EvalSetVersion = ev.EvalSetVersion
			rep.EvalModel = ev.Model
			rep.EvalMode = ev.Mode
		}
	}
	return rep
}

func (s *Server) handleSREEffect(w http.ResponseWriter, r *http.Request) {
	days := 14
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}
	writeJSON(w, http.StatusOK, s.computeSREEffect(days))
}
