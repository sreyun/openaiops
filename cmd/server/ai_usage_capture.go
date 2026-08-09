package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Per-call usage slots avoid global mutex cross-talk under concurrent AI calls.
type aiUsageSlot struct {
	prompt, completion int
	set                bool
}

type aiUsageCtxKey struct{}

var (
	aiUsageSeq   uint64
	aiUsageSlots sync.Map // uint64 -> *aiUsageSlot
	// legacy fallback for call paths that do not bind a slot
	aiUsageMu   sync.Mutex
	aiUsageLast struct {
		prompt, completion int
		set                bool
	}
)

func withAIUsageSlot(ctx context.Context) (context.Context, uint64) {
	if ctx == nil {
		ctx = context.Background()
	}
	id := atomic.AddUint64(&aiUsageSeq, 1)
	aiUsageSlots.Store(id, &aiUsageSlot{})
	return context.WithValue(ctx, aiUsageCtxKey{}, id), id
}

// bindAIUsageSlot reuses an existing live slot on ctx when present so nested LLM
// helpers (streamChat / aiChatV*) do not create a child slot that is deleted
// before the outer recordAICallActor can takeCapturedAIUsageCtx. owned=true means
// the caller created the slot and must endAIUsageSlot it.
func bindAIUsageSlot(ctx context.Context) (context.Context, uint64, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id, ok := ctx.Value(aiUsageCtxKey{}).(uint64); ok && id != 0 {
		if _, live := aiUsageSlots.Load(id); live {
			return ctx, id, false
		}
	}
	ctx2, id := withAIUsageSlot(ctx)
	return ctx2, id, true
}

func endAIUsageSlot(id uint64) {
	aiUsageSlots.Delete(id)
}

func captureAIUsage(promptTok, completionTok int) {
	captureAIUsageCtx(context.Background(), promptTok, completionTok)
}

func captureAIUsageCtx(ctx context.Context, promptTok, completionTok int) {
	if promptTok <= 0 && completionTok <= 0 {
		return
	}
	if ctx != nil {
		if id, ok := ctx.Value(aiUsageCtxKey{}).(uint64); ok {
			if v, ok2 := aiUsageSlots.Load(id); ok2 {
				slot := v.(*aiUsageSlot)
				slot.prompt = promptTok
				slot.completion = completionTok
				slot.set = true
				return
			}
		}
	}
	aiUsageMu.Lock()
	aiUsageLast.prompt = promptTok
	aiUsageLast.completion = completionTok
	aiUsageLast.set = true
	aiUsageMu.Unlock()
}

func takeCapturedAIUsageCtx(ctx context.Context) (promptTok, completionTok int, ok bool) {
	if ctx != nil {
		if id, ok2 := ctx.Value(aiUsageCtxKey{}).(uint64); ok2 {
			if v, loaded := aiUsageSlots.Load(id); loaded {
				slot := v.(*aiUsageSlot)
				if !slot.set {
					return 0, 0, false
				}
				promptTok, completionTok = slot.prompt, slot.completion
				slot.set = false
				slot.prompt, slot.completion = 0, 0
				return promptTok, completionTok, true
			}
		}
	}
	aiUsageMu.Lock()
	defer aiUsageMu.Unlock()
	if !aiUsageLast.set {
		return 0, 0, false
	}
	promptTok, completionTok = aiUsageLast.prompt, aiUsageLast.completion
	aiUsageLast.set = false
	aiUsageLast.prompt, aiUsageLast.completion = 0, 0
	return promptTok, completionTok, true
}

// captureAIUsageFromJSON extracts usage from a chat/completions JSON object or SSE data payload.
func captureAIUsageFromJSON(raw []byte) {
	captureAIUsageFromJSONCtx(context.Background(), raw)
}

func captureAIUsageFromJSONCtx(ctx context.Context, raw []byte) {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	p, c := tokenUsage(v)
	captureAIUsageCtx(ctx, p, c)
}

type aiModelAgg struct {
	Count        int     `json:"count"`
	Fail         int     `json:"fail"`
	Tokens       int64   `json:"tokens"`  // 精确 token（provider usage 捕获）
	ApproxTokens int64   `json:"approx_tokens"` // 估算 token（字符粗估）
	Cost         float64 `json:"cost"`
	AvgMs        int64   `json:"avg_ms"`
}

func (p *pgStore) aiCallByModelFromPG(sinceTs int64) map[string]aiModelAgg {
	out := map[string]aiModelAgg{}
	if p == nil || p.db == nil {
		return out
	}
	rows, err := p.db.Query(`
SELECT COALESCE(NULLIF(model,''),'(unknown)'),
       COUNT(*),
       COALESCE(SUM(CASE WHEN NOT ok THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN usage_source='exact' THEN prompt_tokens+completion_tokens ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN usage_source<>'exact' THEN approx_tokens ELSE 0 END),0),
       COALESCE(SUM(cost_estimate),0),
       COALESCE(SUM(latency_ms),0)
FROM ai_call_events_p WHERE ts >= $1
GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 40`, sinceTs)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var model string
		var cnt, fl int
		var tokens, approxTokens, latencySum int64
		var costF float64
		if rows.Scan(&model, &cnt, &fl, &tokens, &approxTokens, &costF, &latencySum) != nil {
			continue
		}
		avg := int64(0)
		if cnt > 0 {
			avg = latencySum / int64(cnt)
		}
		out[model] = aiModelAgg{Count: cnt, Fail: fl, Tokens: tokens, ApproxTokens: approxTokens, Cost: costF, AvgMs: avg}
	}
	return out
}

type aiTaskCostAgg struct {
	Count int     `json:"count"`
	Fail  int     `json:"fail"`
	Cost  float64 `json:"cost"`
	Tokens int64  `json:"tokens"`
	AvgMs int64   `json:"avg_ms"`
}

func (p *pgStore) aiCallByTaskCostFromPG(sinceTs int64) map[string]aiTaskCostAgg {
	out := map[string]aiTaskCostAgg{}
	if p == nil || p.db == nil {
		return out
	}
	rows, err := p.db.Query(`
SELECT COALESCE(NULLIF(task,''),'(unknown)'),
       COUNT(*),
       COALESCE(SUM(CASE WHEN NOT ok THEN 1 ELSE 0 END),0),
       COALESCE(SUM(cost_estimate),0),
       COALESCE(SUM(CASE WHEN usage_source='exact' THEN prompt_tokens+completion_tokens ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN usage_source<>'exact' THEN approx_tokens ELSE 0 END),0),
       COALESCE(SUM(latency_ms),0)
FROM ai_call_events_p WHERE ts >= $1
GROUP BY 1 ORDER BY SUM(cost_estimate) DESC NULLS LAST LIMIT 40`, sinceTs)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var task string
		var cnt, fl int
		var costF float64
		var tokens, approxTokens, latSum int64
		if rows.Scan(&task, &cnt, &fl, &costF, &tokens, &approxTokens, &latSum) != nil {
			continue
		}
		avg := int64(0)
		if cnt > 0 {
			avg = latSum / int64(cnt)
		}
		out[task] = aiTaskCostAgg{Count: cnt, Fail: fl, Cost: costF, Tokens: tokens, AvgMs: avg}
	}
	return out
}

// aiRouteReasonAgg 按模型路由原因（task_models/cheap_model/primary/fallback）聚合调用与成本。
// 这是「智能路由决策可审计」的核心视图：能看出多少调用走了 cheap、多少走了 fallback、
// 各自花了多少钱——验证路由是否如配置所愿工作。
type aiRouteReasonAgg struct {
	Count int     `json:"count"`
	Fail  int     `json:"fail"`
	Cost  float64 `json:"cost"`
	AvgMs int64   `json:"avg_ms"`
}

func (p *pgStore) aiCallByRouteReasonFromPG(sinceTs int64) map[string]aiRouteReasonAgg {
	out := map[string]aiRouteReasonAgg{}
	if p == nil || p.db == nil {
		return out
	}
	rows, err := p.db.Query(`
SELECT COALESCE(NULLIF(route_reason,''),'(none)'),
       COUNT(*),
       COALESCE(SUM(CASE WHEN NOT ok THEN 1 ELSE 0 END),0),
       COALESCE(SUM(cost_estimate),0),
       COALESCE(AVG(latency_ms),0)::bigint
FROM ai_call_events_p WHERE ts >= $1
GROUP BY 1 ORDER BY COUNT(*) DESC`, sinceTs)
	if err != nil {
		slog.Warn("PG ?? AI route reason", "err", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		var cnt, fl int
		var costF float64
		var avg int64
		if rows.Scan(&r, &cnt, &fl, &costF, &avg) != nil {
			continue
		}
		out[r] = aiRouteReasonAgg{Count: cnt, Fail: fl, Cost: costF, AvgMs: avg}
	}
	return out
}
