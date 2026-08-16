package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// insertAICallEvent appends one AI call observation permanently (survives restart).
// The base table and its partitioned twin are written in one transaction so a
// partial failure never leaves the two copies divergent (this is a billing-
// adjacent write path).
func (p *pgStore) insertAICallEvent(st aiCallStat) {
	if err := p.appendAICallEventDual(context.Background(), st); err != nil {
		slog.Warn("PG AI call append failed", "operation", "append", "error_class", auditAppendErrorClass(err))
	}
}

func (p *pgStore) appendAICallEventDual(ctx context.Context, st aiCallStat) error {
	if st.Ts <= 0 {
		st.Ts = time.Now().Unix()
	}
	return p.withPgTx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `
INSERT INTO ai_call_events(
  ts, task, model, actor, latency_ms, ok, error,
  memory_hits, skill_hits, reply_chars, approx_tokens,
  prompt_tokens, completion_tokens, cost_estimate, usage_source, prompt_version, route_reason
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
RETURNING id`,
			st.Ts, nullStr(st.Task), nullStr(st.Model), nullStr(st.Actor),
			st.LatencyMs, st.OK, nullStr(st.Error),
			st.MemHits, st.SkillHits, st.ReplyChars, st.ApproxTokens,
			st.PromptTokens, st.CompletionTokens, st.CostEstimate, nullStr(st.UsageSource), nullStr(st.PromptVersion), nullStr(st.RouteReason)).Scan(&id)
		if err != nil {
			return fmt.Errorf("insert legacy AI call mirror: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO ai_call_events_p(
  id, ts, task, model, actor, latency_ms, ok, error,
  memory_hits, skill_hits, reply_chars, approx_tokens,
  prompt_tokens, completion_tokens, cost_estimate, usage_source, prompt_version, route_reason
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			id, st.Ts, nullStr(st.Task), nullStr(st.Model), nullStr(st.Actor),
			st.LatencyMs, st.OK, nullStr(st.Error),
			st.MemHits, st.SkillHits, st.ReplyChars, st.ApproxTokens,
			st.PromptTokens, st.CompletionTokens, st.CostEstimate, nullStr(st.UsageSource), nullStr(st.PromptVersion), nullStr(st.RouteReason))
		if err != nil {
			return fmt.Errorf("insert partition AI call mirror: %w", err)
		}
		return nil
	})
}

// insertAIFeedbackEvent persists a privacy-minimized quality signal. sourceHash
// is one-way metadata; raw prompts and replies stay out of observability tables.
func (p *pgStore) insertAIFeedbackEvent(task, actor, action, sourceHash string) bool {
	if p == nil || p.db == nil {
		return false
	}
	_, err := p.db.Exec(`
INSERT INTO ai_feedback_events(ts, task, actor, action, source_hash)
VALUES ($1,$2,$3,$4,$5)`,
		time.Now().Unix(), task, actor, action, sourceHash)
	if err != nil {
		slog.Warn("PG 写入 AI 反馈事件失败", "err", err)
		return false
	}
	return true
}

func nullStr(s string) string { return s }

// aiCallStatsFromPG aggregates durable AI call events for the stats dashboard.
func (p *pgStore) aiCallStatsFromPG(sinceTs int64, recentLimit int) map[string]any {
	out := map[string]any{
		"total": 0, "fail": 0, "avg_latency_ms": 0, "fail_rate": 0.0,
		"approx_tokens_total": 0, "cost_total": 0.0,
		"by_task": map[string]aiTaskAgg{}, "recent": []aiCallStat{},
		"persisted": true, "since_ts": sinceTs,
	}
	if p == nil || p.db == nil {
		out["persisted"] = false
		return out
	}
	var total, fail, sumLat, sumTok, sumExactTok sql.NullInt64
	var sumCost sql.NullFloat64
	err := p.db.QueryRow(`
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN NOT ok THEN 1 ELSE 0 END),0),
       COALESCE(SUM(latency_ms),0),
       COALESCE(SUM(CASE WHEN usage_source<>'exact' THEN approx_tokens ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN usage_source='exact' THEN prompt_tokens+completion_tokens ELSE 0 END),0),
       COALESCE(SUM(cost_estimate),0)
FROM ai_call_events_p WHERE ts >= $1`, sinceTs).Scan(&total, &fail, &sumLat, &sumTok, &sumExactTok, &sumCost)
	if err != nil {
		slog.Warn("PG 查询 AI 调用统计失败", "err", err)
		out["persisted"] = false
		return out
	}
	t := total.Int64
	f := fail.Int64
	out["total"] = t
	out["fail"] = f
	out["approx_tokens_total"] = sumTok.Int64
	out["exact_tokens_total"] = sumExactTok.Int64
	out["cost_total"] = sumCost.Float64
	if t > 0 {
		out["avg_latency_ms"] = sumLat.Int64 / t
		out["fail_rate"] = float64(f) / float64(t)
	}

	byTask := map[string]aiTaskAgg{}
	rows, err := p.db.Query(`
SELECT task,
       COUNT(*),
       COALESCE(SUM(CASE WHEN NOT ok THEN 1 ELSE 0 END),0),
       COALESCE(AVG(latency_ms),0)
FROM ai_call_events_p WHERE ts >= $1
GROUP BY task ORDER BY COUNT(*) DESC`, sinceTs)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var task string
			var cnt, fl int
			var avg float64
			if rows.Scan(&task, &cnt, &fl, &avg) != nil {
				continue
			}
			if task == "" {
				task = "(unknown)"
			}
			byTask[task] = aiTaskAgg{Count: cnt, Fail: fl, AvgMs: int64(avg)}
		}
	}
	out["by_task"] = byTask

	if recentLimit <= 0 {
		recentLimit = 30
	}
	rrows, err := p.db.Query(`
SELECT ts, task, model, actor, latency_ms, ok, COALESCE(error,''),
       memory_hits, skill_hits, reply_chars, approx_tokens,
       prompt_tokens, completion_tokens, cost_estimate
FROM ai_call_events_p WHERE ts >= $1
ORDER BY id DESC LIMIT $2`, sinceTs, recentLimit)
	if err == nil {
		defer rrows.Close()
		recent := make([]aiCallStat, 0, recentLimit)
		for rrows.Next() {
			var st aiCallStat
			if rrows.Scan(&st.Ts, &st.Task, &st.Model, &st.Actor, &st.LatencyMs, &st.OK, &st.Error,
				&st.MemHits, &st.SkillHits, &st.ReplyChars, &st.ApproxTokens,
				&st.PromptTokens, &st.CompletionTokens, &st.CostEstimate) != nil {
				continue
			}
			recent = append(recent, st)
		}
		out["recent"] = recent
	}
	return out
}

func (p *pgStore) aiFeedbackStatsFromPG(sinceTs int64) map[string]any {
	out := map[string]any{
		"feedback_total": 0, "feedback_applied": 0, "feedback_helpful": 0,
		"feedback_unhelpful": 0, "feedback_positive_rate": 0.0, "feedback_apply_rate": 0.0,
		"feedback_by_task": map[string]aiFeedbackAgg{}, "feedback_persisted": true,
	}
	if p == nil || p.db == nil {
		out["feedback_persisted"] = false
		return out
	}
	var a aiFeedbackAgg
	err := p.db.QueryRow(`
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN action='applied' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN action='helpful' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN action='unhelpful' THEN 1 ELSE 0 END),0)
FROM ai_feedback_events WHERE ts >= $1`, sinceTs).
		Scan(&a.Total, &a.Applied, &a.Helpful, &a.Unhelpful)
	if err != nil {
		slog.Warn("PG 查询 AI 反馈统计失败", "err", err)
		out["feedback_persisted"] = false
		return out
	}
	positiveRate, applyRate := feedbackRates(a)
	out["feedback_total"] = a.Total
	out["feedback_applied"] = a.Applied
	out["feedback_helpful"] = a.Helpful
	out["feedback_unhelpful"] = a.Unhelpful
	out["feedback_positive_rate"] = positiveRate
	out["feedback_apply_rate"] = applyRate

	byTask := map[string]aiFeedbackAgg{}
	rows, err := p.db.Query(`
SELECT task, COUNT(*),
       COALESCE(SUM(CASE WHEN action='applied' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN action='helpful' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN action='unhelpful' THEN 1 ELSE 0 END),0)
FROM ai_feedback_events WHERE ts >= $1
GROUP BY task ORDER BY COUNT(*) DESC`, sinceTs)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var task string
			var row aiFeedbackAgg
			if rows.Scan(&task, &row.Total, &row.Applied, &row.Helpful, &row.Unhelpful) != nil {
				continue
			}
			if task == "" {
				task = "(unknown)"
			}
			byTask[task] = row
		}
	}
	out["feedback_by_task"] = byTask
	return out
}

// aiUsageHistoryPoint is one bucket for cost/token composite charts.
type aiUsageHistoryPoint struct {
	Timestamp    int64   `json:"timestamp"`
	Calls        int64   `json:"calls"`
	Fail         int64   `json:"fail"`
	Tokens       int64   `json:"tokens"`        // 精确 token
	ApproxTokens int64   `json:"approx_tokens"` // 估算 token
	Cost         float64 `json:"cost"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
}

// queryAIUsageHistory returns time-bucketed series for composite cost charts.
func (p *pgStore) queryAIUsageHistory(fromTs, toTs int64, bucket string) []aiUsageHistoryPoint {
	if p == nil || p.db == nil {
		return nil
	}
	trunc := "hour"
	step := int64(3600)
	switch strings.ToLower(bucket) {
	case "day", "d":
		trunc = "day"
		step = 86400
	case "hour", "h", "":
		trunc = "hour"
		step = 3600
	}
	_ = step
	q := `
SELECT EXTRACT(EPOCH FROM date_trunc('` + trunc + `', to_timestamp(ts)))::bigint AS bucket,
       COUNT(*),
       COALESCE(SUM(CASE WHEN NOT ok THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN usage_source='exact' THEN prompt_tokens+completion_tokens ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN usage_source<>'exact' THEN approx_tokens ELSE 0 END),0),
       COALESCE(SUM(cost_estimate),0),
       COALESCE(AVG(latency_ms),0)::bigint
FROM ai_call_events_p
WHERE ts >= $1 AND ts <= $2
GROUP BY 1 ORDER BY 1`
	rows, err := p.db.Query(q, fromTs, toTs)
	if err != nil {
		slog.Warn("PG 查询 AI 用量历史失败", "err", err)
		return nil
	}
	defer rows.Close()
	var out []aiUsageHistoryPoint
	for rows.Next() {
		var pt aiUsageHistoryPoint
		if rows.Scan(&pt.Timestamp, &pt.Calls, &pt.Fail, &pt.Tokens, &pt.ApproxTokens, &pt.Cost, &pt.AvgLatencyMs) != nil {
			continue
		}
		out = append(out, pt)
	}
	return out
}

type aiUserUsageRow struct {
	Actor        string  `json:"actor"`
	Calls        int64   `json:"calls"`
	Fail         int64   `json:"fail"`
	Tokens       int64   `json:"tokens"`        // 精确 token
	ApproxTokens int64   `json:"approx_tokens"` // 估算 token
	Cost         float64 `json:"cost"`
	AvgMs        int64   `json:"avg_latency_ms"`
}

func (p *pgStore) queryAIUsageByUser(fromTs, toTs int64, limit int) []aiUserUsageRow {
	if p == nil || p.db == nil {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := p.db.Query(`
SELECT COALESCE(NULLIF(TRIM(actor),''),'(system)') AS actor,
       COUNT(*),
       COALESCE(SUM(CASE WHEN NOT ok THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN usage_source='exact' THEN prompt_tokens+completion_tokens ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN usage_source<>'exact' THEN approx_tokens ELSE 0 END),0),
       COALESCE(SUM(cost_estimate),0),
       COALESCE(AVG(latency_ms),0)::bigint
FROM ai_call_events_p
WHERE ts >= $1 AND ts <= $2
GROUP BY 1 ORDER BY SUM(approx_tokens) DESC NULLS LAST LIMIT $3`, fromTs, toTs, limit)
	if err != nil {
		slog.Warn("PG 查询 AI 用户用量失败", "err", err)
		return nil
	}
	defer rows.Close()
	var out []aiUserUsageRow
	for rows.Next() {
		var r aiUserUsageRow
		if rows.Scan(&r.Actor, &r.Calls, &r.Fail, &r.Tokens, &r.ApproxTokens, &r.Cost, &r.AvgMs) != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (p *pgStore) cleanupAICallEvents(retainDays int) {
	if retainDays <= 0 {
		retainDays = 365
	}
	months := retainDays/30 + 1
	if months < 2 {
		months = 2
	}
	p.cleanupOldTSPartitions("ai_call_events_p", months)
	cut := time.Now().AddDate(0, 0, -retainDays).Unix()
	_, _ = p.db.Exec(`DELETE FROM ai_call_events WHERE ts > 0 AND ts < $1`, cut)
	_, _ = p.db.Exec(`DELETE FROM ai_feedback_events WHERE ts > 0 AND ts < $1`, cut)
}

// estimateAICost returns cost in configured currency for approx completion tokens.
// When prompt/completion split is unknown, treat ApproxTokens as completion-side
// (reply-based estimate) and apply output price; optionally blend with input price.
func estimateAICost(cfg AIConfig, promptTok, completionTok, approxTok int) float64 {
	inP := cfg.InputPricePer1M
	outP := cfg.OutputPricePer1M
	if inP <= 0 && outP <= 0 {
		return 0
	}
	if promptTok <= 0 && completionTok <= 0 {
		completionTok = approxTok
	}
	cost := float64(promptTok)*inP/1e6 + float64(completionTok)*outP/1e6
	if cost <= 0 && approxTok > 0 {
		// blended fallback when only one side priced
		p := outP
		if p <= 0 {
			p = inP
		}
		cost = float64(approxTok) * p / 1e6
	}
	return cost
}

// aiBillingReconcileRow is one day of exact-vs-estimated cost/token comparison.
type aiBillingReconcileRow struct {
	Day          string  `json:"day"`
	ExactTokens  int64   `json:"exact_tokens"`
	ApproxTokens int64   `json:"approx_tokens"`
	ExactCost    float64 `json:"exact_cost"`
	ApproxCost   float64 `json:"approx_cost"`
	// DiffRate is (approx-exact)/exact; negative when exact exceeds estimate.
	DiffRate float64 `json:"diff_rate"`
}

// billingReconcileFromPG returns per-day exact vs estimated token/cost so a
// customer can verify our ledger reconciles with the provider's bill. This is
// the "可审计账本" surface for paid deployments.
func (p *pgStore) billingReconcileFromPG(fromTs, toTs int64) []aiBillingReconcileRow {
	if p == nil || p.db == nil {
		return nil
	}
	rows, err := p.db.Query(`
SELECT to_char(to_timestamp(ts), 'YYYY-MM-DD') AS day,
       COALESCE(SUM(CASE WHEN usage_source='exact' THEN prompt_tokens+completion_tokens ELSE 0 END),0) AS exact_tok,
       COALESCE(SUM(CASE WHEN usage_source<>'exact' THEN approx_tokens ELSE 0 END),0) AS approx_tok,
       COALESCE(SUM(CASE WHEN usage_source='exact' THEN cost_estimate ELSE 0 END),0) AS exact_cost,
       COALESCE(SUM(CASE WHEN usage_source<>'exact' THEN cost_estimate ELSE 0 END),0) AS approx_cost
FROM ai_call_events_p
WHERE ts >= $1 AND ts <= $2
GROUP BY 1 ORDER BY 1`, fromTs, toTs)
	if err != nil {
		slog.Warn("PG 查询 AI 账单对账失败", "err", err)
		return nil
	}
	defer rows.Close()
	var out []aiBillingReconcileRow
	for rows.Next() {
		var r aiBillingReconcileRow
		if rows.Scan(&r.Day, &r.ExactTokens, &r.ApproxTokens, &r.ExactCost, &r.ApproxCost) != nil {
			continue
		}
		if r.ExactCost != 0 {
			r.DiffRate = (r.ApproxCost - r.ExactCost) / r.ExactCost
		}
		out = append(out, r)
	}
	return out
}

// handleAIBillingReconcile GET /api/v1/admin/ai/billing-reconcile?from=&to=
// Admin-only via routeAllowed prefix (see auth.go routeAllowed /api/v1/admin/).
func (s *Server) handleAIBillingReconcile(w http.ResponseWriter, r *http.Request) {
	fromTs, toTs := parseTimeRangeQuery(r, 30*24*time.Hour)
	var rows []aiBillingReconcileRow
	if s.pg != nil {
		rows = s.pg.billingReconcileFromPG(fromTs, toTs)
	}
	if rows == nil {
		rows = []aiBillingReconcileRow{}
	}
	var sumExactTok, sumApproxTok int64
	var sumExactCost, sumApproxCost float64
	for _, r := range rows {
		sumExactTok += r.ExactTokens
		sumApproxTok += r.ApproxTokens
		sumExactCost += r.ExactCost
		sumApproxCost += r.ApproxCost
	}
	diffRate := 0.0
	if sumExactCost != 0 {
		diffRate = (sumApproxCost - sumExactCost) / sumExactCost
	}
	cfg := s.cfg.AIConfig()
	cur := cfg.CostCurrency
	if cur == "" {
		cur = "CNY"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": fromTs, "to": toTs,
		"cost_currency": cur,
		"persisted":     s.pg != nil,
		"days":          rows,
		"total": map[string]any{
			"exact_tokens":  sumExactTok,
			"approx_tokens": sumApproxTok,
			"exact_cost":    sumExactCost,
			"approx_cost":   sumApproxCost,
			"diff_rate":     diffRate,
		},
	})
}

// handleAIStats returns durable AI observability aggregates (PG) when available.
// Query: days (default 30). Falls back to process-local hub if PG unavailable.
func (s *Server) handleAIStats(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	if days > 3650 {
		days = 3650
	}
	since := time.Now().AddDate(0, 0, -days).Unix()
	if s.pg != nil {
		out := s.pg.aiCallStatsFromPG(since, 30)
		for k, v := range s.pg.aiFeedbackStatsFromPG(since) {
			out[k] = v
		}
		out["days"] = days
		out["by_model"] = s.pg.aiCallByModelFromPG(since)
		out["by_task_cost"] = s.pg.aiCallByTaskCostFromPG(since)
		out["by_route_reason"] = s.pg.aiCallByRouteReasonFromPG(since)
		cfg := s.cfg.AIConfig()
		out["cost_currency"] = cfg.CostCurrency
		if out["cost_currency"] == "" {
			out["cost_currency"] = "CNY"
		}
		costTotal, _ := out["cost_total"].(float64)
		out["tco"] = map[string]any{
			"total_cost":     costTotal,
			"daily_avg_cost": costTotal / float64(days),
			"days":           days,
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	snap := s.aiStats.snapshot()
	snap["persisted"] = false
	snap["days"] = days
	writeJSON(w, http.StatusOK, snap)
}

// handleAIUsageHistory GET /api/v1/ai/usage/history?from=&to=&bucket=hour|day
func (s *Server) handleAIUsageHistory(w http.ResponseWriter, r *http.Request) {
	fromTs, toTs := parseTimeRangeQuery(r, 7*24*time.Hour)
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		span := toTs - fromTs
		if span > 14*86400 {
			bucket = "day"
		} else {
			bucket = "hour"
		}
	}
	var pts []aiUsageHistoryPoint
	if s.pg != nil {
		pts = s.pg.queryAIUsageHistory(fromTs, toTs, bucket)
	}
	if pts == nil {
		pts = []aiUsageHistoryPoint{}
	}
	cfg := s.cfg.AIConfig()
	cur := cfg.CostCurrency
	if cur == "" {
		cur = "CNY"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": fromTs, "to": toTs, "bucket": bucket,
		"cost_currency": cur,
		"points":        pts,
		"persisted":     s.pg != nil,
	})
}

// handleAIUsageByUser GET /api/v1/ai/usage/by-user?from=&to=&limit=
func (s *Server) handleAIUsageByUser(w http.ResponseWriter, r *http.Request) {
	fromTs, toTs := parseTimeRangeQuery(r, 30*24*time.Hour)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var rows []aiUserUsageRow
	if s.pg != nil {
		rows = s.pg.queryAIUsageByUser(fromTs, toTs, limit)
	}
	if rows == nil {
		rows = []aiUserUsageRow{}
	}
	cfg := s.cfg.AIConfig()
	cur := cfg.CostCurrency
	if cur == "" {
		cur = "CNY"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": fromTs, "to": toTs,
		"cost_currency": cur,
		"users":         rows,
		"persisted":     s.pg != nil,
	})
}

func parseTimeRangeQuery(r *http.Request, defaultSpan time.Duration) (fromTs, toTs int64) {
	now := time.Now().Unix()
	toTs = now
	fromTs = now - int64(defaultSpan.Seconds())
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			toTs = n
		}
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			fromTs = n
		}
	}
	if fromTs > toTs {
		fromTs, toTs = toTs, fromTs
	}
	return fromTs, toTs
}

// --- terminal command history (query audit_log permanently) ---

type termCommandRow struct {
	Timestamp int64  `json:"timestamp"`
	Actor     string `json:"actor"`
	IP        string `json:"ip,omitempty"`
	Host      string `json:"host,omitempty"`
	Message   string `json:"message"`
}

func (p *pgStore) queryTerminalCommands(fromTs, toTs int64, host, actor, q string, limit, offset int) ([]termCommandRow, int) {
	if p == nil || p.db == nil {
		return nil, 0
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	args := []any{fromTs, toTs}
	where := `ts >= $1 AND ts <= $2 AND data->>'kind' = 'terminal'`
	n := 3
	if host != "" {
		where += ` AND data->>'host' ILIKE $` + strconv.Itoa(n)
		args = append(args, "%"+host+"%")
		n++
	}
	if actor != "" {
		where += ` AND data->>'actor' ILIKE $` + strconv.Itoa(n)
		args = append(args, "%"+actor+"%")
		n++
	}
	if q != "" {
		where += ` AND data->>'message' ILIKE $` + strconv.Itoa(n)
		args = append(args, "%"+q+"%")
		n++
	}
	var total int
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM audit_log_p WHERE `+where, args...).Scan(&total)

	args = append(args, limit, offset)
	rows, err := p.db.Query(`
SELECT COALESCE((data->>'timestamp')::bigint, ts),
       COALESCE(data->>'actor',''),
       COALESCE(data->>'ip',''),
       COALESCE(data->>'host',''),
       COALESCE(data->>'message','')
FROM audit_log_p WHERE `+where+`
ORDER BY ts DESC LIMIT $`+strconv.Itoa(n)+` OFFSET $`+strconv.Itoa(n+1), args...)
	if err != nil {
		slog.Warn("PG 查询终端命令审计失败", "err", err)
		return nil, total
	}
	defer rows.Close()
	var out []termCommandRow
	for rows.Next() {
		var r termCommandRow
		if rows.Scan(&r.Timestamp, &r.Actor, &r.IP, &r.Host, &r.Message) != nil {
			continue
		}
		out = append(out, r)
	}
	return out, total
}

// handleTerminalCommands GET /api/v1/terminal/commands?from=&to=&host=&actor=&q=&limit=&offset=
func (s *Server) handleTerminalCommands(w http.ResponseWriter, r *http.Request) {
	fromTs, toTs := parseTimeRangeQuery(r, 30*24*time.Hour)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	actor := strings.TrimSpace(r.URL.Query().Get("actor"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var items []termCommandRow
	var total int
	if s.pg != nil {
		items, total = s.pg.queryTerminalCommands(fromTs, toTs, host, actor, q, limit, offset)
	}
	if items == nil {
		items = []termCommandRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": fromTs, "to": toTs,
		"total": total, "limit": limit, "offset": offset,
		"items": items, "persisted": s.pg != nil,
	})
}

// --- playbook / remediation permanent execution tables ---

func (p *pgStore) getPlaybookExecution(id int64) (PlaybookExecution, bool) {
	if p == nil || p.db == nil || id == 0 {
		return PlaybookExecution{}, false
	}
	var raw []byte
	err := p.db.QueryRow(`SELECT data FROM playbook_executions WHERE id=$1`, id).Scan(&raw)
	if err != nil {
		return PlaybookExecution{}, false
	}
	var e PlaybookExecution
	if json.Unmarshal(raw, &e) != nil {
		return PlaybookExecution{}, false
	}
	return e, true
}

func (p *pgStore) upsertPlaybookExecution(e PlaybookExecution) {
	if p == nil || p.db == nil || e.ID == 0 {
		return
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	ts := e.StartTime
	if ts == 0 {
		ts = time.Now().Unix()
	}
	_, err = p.db.Exec(`
INSERT INTO playbook_executions(id, ts, playbook_id, status, data)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (id) DO UPDATE SET ts=EXCLUDED.ts, playbook_id=EXCLUDED.playbook_id,
  status=EXCLUDED.status, data=EXCLUDED.data`,
		e.ID, ts, e.PlaybookID, e.Status, raw)
	if err != nil {
		slog.Warn("PG 写入剧本执行记录失败", "err", err)
	}
}

func (p *pgStore) listPlaybookExecutions(limit int) []PlaybookExecution {
	if p == nil || p.db == nil {
		return nil
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := p.db.Query(`SELECT data FROM playbook_executions ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []PlaybookExecution
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var e PlaybookExecution
		if json.Unmarshal(raw, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func (p *pgStore) upsertRemediationRun(run RemediationRun) {
	if p == nil || p.db == nil || run.ID == 0 {
		return
	}
	raw, err := json.Marshal(run)
	if err != nil {
		return
	}
	ts := run.CreatedAt
	if ts == 0 {
		ts = time.Now().Unix()
	}
	_, err = p.db.Exec(`
INSERT INTO remediation_runs(id, ts, rule_id, status, data)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (id) DO UPDATE SET ts=EXCLUDED.ts, rule_id=EXCLUDED.rule_id,
  status=EXCLUDED.status, data=EXCLUDED.data`,
		run.ID, ts, run.RuleID, run.Status, raw)
	if err != nil {
		slog.Warn("PG 写入自愈执行记录失败", "err", err)
	}
}

func (p *pgStore) listRemediationRuns(limit int) []RemediationRun {
	if p == nil || p.db == nil {
		return nil
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, err := p.db.Query(`SELECT data FROM remediation_runs ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RemediationRun
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var run RemediationRun
		if json.Unmarshal(raw, &run) == nil {
			out = append(out, run)
		}
	}
	return out
}
