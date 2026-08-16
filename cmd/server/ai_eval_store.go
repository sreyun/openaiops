package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"
)

// persistEvalRun 把一次评测汇总落库 ai_eval_runs（供周报/对账引用「他证通过率」）。
func (p *pgStore) persistEvalRun(s evalRunSummary) {
	if p == nil || p.db == nil {
		return
	}
	detail, _ := json.Marshal(s)
	_, err := p.db.Exec(`
INSERT INTO ai_eval_runs(
  id, ts, model, mode, eval_set_version, case_count, passed_count,
  pass_rate, root_cause_hit_rate, action_accept_rate, verify_agreement, detail
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		s.RunID, time.Now().Unix(), s.Model, s.Mode, s.EvalSetVersion, s.CaseCount, s.PassedCount,
		s.PassRate, s.RootCauseHitRate, s.ActionAcceptRate, s.VerifyAgreement, detail)
	if err != nil {
		slog.Warn("PG 写入 AI 评测结果失败", "err", err)
	}
}

// latestEvalRun 返回最近一次评测汇总（若存在）。空 RunID 表示从未跑过评测。
func (p *pgStore) latestEvalRun() (evalRunSummary, error) {
	var s evalRunSummary
	if p == nil || p.db == nil {
		return s, nil
	}
	var detail []byte
	err := p.db.QueryRow(`
SELECT id, ts, model, mode, eval_set_version, case_count, passed_count,
       pass_rate, root_cause_hit_rate, action_accept_rate, verify_agreement, detail
FROM ai_eval_runs ORDER BY ts DESC LIMIT 1`).
		Scan(&s.RunID, &s.Ts, &s.Model, &s.Mode, &s.EvalSetVersion, &s.CaseCount, &s.PassedCount,
			&s.PassRate, &s.RootCauseHitRate, &s.ActionAcceptRate, &s.VerifyAgreement, &detail)
	if err != nil {
		if err == sql.ErrNoRows {
			return s, nil
		}
		return s, err
	}
	return s, nil
}

// onlineEvalLLM 是真实 provider 评测的 LLM 调用：复用 aiComplete（非流式，含 usage 槽位）。
// 它把 evalCase 的 task 作为诊断任务分派，但固定走诊断 prompt——评测只看「根因命中」而非
// 具体任务路由，保证评测与任务无关、可跨模型对比。
func (s *Server) onlineEvalLLM(cfg AIConfig) evalLLMFunc {
	return func(_ context.Context, system, user string) (string, error) {
		return aiComplete(cfg, system, user)
	}
}

// runWeeklyEval 每周一与周报一起跑在线评测（走 cheap_model 控制成本），结果落库。
// 由 runDutyReportLoop 的周一分支调用。返回最新汇总。
func (s *Server) runWeeklyEval() (evalRunSummary, error) {
	cfg := s.cfg.AIConfig()
	model := cfg.Model
	if cfg.CheapModel != "" {
		model = cfg.CheapModel
	}
	sum, err := runEvalSet(context.Background(), model, "online", s.onlineEvalLLM(cfg))
	if err != nil {
		return sum, err
	}
	if s.pg != nil {
		s.pg.persistEvalRun(sum)
		slog.Info("ai.eval online", "run", sum.RunID, "pass_rate", sum.PassRate,
			"cases", sum.CaseCount, "model", model)
	}
	return sum, nil
}
