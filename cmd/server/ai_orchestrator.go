package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// AI Orchestrator（P2）：任务路由策略、统一调用日志、运行时统计。
// 不替代 Hermes 工具循环；先覆盖 /ai/assist 与共享观测点。
// ============================================================================

// aiTaskPolicy 描述某一 AI 任务的记忆种类与调用选项。
type aiTaskPolicy struct {
	MemKind        string
	DisableThink   bool
	EnableThink    bool          // 显式开启深度思考（看板等质量任务）
	ThinkingBudget int           // 思考 token 上限；0=不传（由 applyThinkingKnobs 默认）
	MaxTokens      int           // 输出 token 上限；0=默认策略
	Timeout        time.Duration // 0 = 用 streamChat 默认 120s
	RememberKind   string
	RememberSource string
	AutoRemember   bool // 仅用于已验证的确定性结果；普通模型回答必须经人工反馈后再学习
}

// assistTaskPolicy 按 task 返回编排策略（路由 + 思考开关 + 超时）。
func assistTaskPolicy(task string) aiTaskPolicy {
	p := aiTaskPolicy{MemKind: "chat", RememberKind: "assist", RememberSource: "assist:" + task}
	switch task {
	case "audit_diagnosis", "result_diagnosis", "chart_analysis", "forecast_analysis", "snmp_diagnosis", "trap_diagnosis",
		"hardware_diagnosis", "hyperv_diagnosis", "netflow_diagnosis", "checks_diagnosis",
		"forward_diagnosis", "apimon_diagnosis", "content_audit_diagnosis",
		"host_security_diagnosis", "web_vuln_diagnosis",
		"inspect_diagnosis", "java_diagnosis", "inspect_remediation", "host_inspect_analysis",
		"host_security_remediation", "web_vuln_remediation",
		"host_security_finding", "web_vuln_finding",
		"hyperv_ops_plan", "container_ops_plan", "k8s_ops_plan", "sql_remediation",
		"dashboard_analysis", "dashboard_optimize",
		"sql_audit", "sql_optimize":
		p.MemKind = "diagnosis"
	}
	switch task {
	case "sql_beautify", "sql_audit", "sql_optimize", "sql_remediation",
		// 巡检报告动辄上万字符，且要求分五/六段结构化输出；默认 120s 会把长报告
		// 的诊断截在半路，用户看到的是一段没写完的结论。
		"inspect_diagnosis", "java_diagnosis", "inspect_remediation",
		"hyperv_ops_plan", "container_ops_plan", "k8s_ops_plan",
		"host_security_remediation", "web_vuln_remediation",
		"host_security_finding", "web_vuln_finding":
		p.Timeout = 90 * time.Second
	case "dashboard_optimize":
		// 开启思考但严格限预算：过长思维链会占满超时/输出额度，最终 JSON 出不来。
		// MaxTokens 16k，避免大型看板优化 JSON 被截断导致「应用失败」。
		p.EnableThink = true
		p.DisableThink = false
		p.ThinkingBudget = 256
		p.MaxTokens = 16384
		p.Timeout = 240 * time.Second
	case "dashboard_prompt_optimize":
		p.EnableThink = true
		p.DisableThink = false
		p.ThinkingBudget = 128
		p.MaxTokens = 2048
		p.Timeout = 90 * time.Second
	case "dashboard_analysis":
		p.EnableThink = true
		p.DisableThink = false
		p.ThinkingBudget = 384
		p.MaxTokens = 4096
		p.Timeout = 120 * time.Second
	case "logql", "promql", "playbook", "remediation_rule", "remediation_proposal":
		p.Timeout = 90 * time.Second
	}
	return p
}

// aiCallStat 单次 AI 调用观测样本（内存环形 + PG 永久落库）。
type aiCallStat struct {
	Ts               int64   `json:"ts"`
	Task             string  `json:"task"`
	Model            string  `json:"model"`
	Actor            string  `json:"actor,omitempty"`
	LatencyMs        int64   `json:"latency_ms"`
	OK               bool    `json:"ok"`
	Error            string  `json:"error,omitempty"`
	MemHits          int     `json:"memory_hits"`
	SkillHits        int     `json:"skill_hits"`
	ReplyChars       int     `json:"reply_chars"`
	ApproxTokens     int     `json:"approx_tokens"`               // 按字符粗估，非 Provider 精确账单
	PromptTokens     int     `json:"prompt_tokens,omitempty"`     // Provider usage（若有）
	CompletionTokens int     `json:"completion_tokens,omitempty"` // Provider usage（若有）
	CostEstimate     float64 `json:"cost_estimate,omitempty"`     // 按配置单价估算
	// UsageSource 标记 token 来源：exact=Provider usage 捕获，estimated=字符粗估。
	// 计费只认 exact；estimated 仅供估算口径。
	UsageSource string `json:"usage_source,omitempty"`
	// PromptVersion 是本次调用使用的系统提示词模板版本指纹（见 prompt_render.go）。
	// 用于复盘「哪个提示词版本产出了这个回答/成本」。
	PromptVersion string `json:"prompt_version,omitempty"`
	// RouteReason 是本次调用的模型路由决策原因：task_models | cheap_model | primary | fallback。
	// 由 recordAICallActor 在落库时按 (task, usedModel) 推断（见 inferRouteReason），
	// 使「这次为什么选这个模型」在账本中可审计——智能路由决策可追溯的基础。
	RouteReason string `json:"route_reason,omitempty"`
}

type aiTaskAgg struct {
	Count int   `json:"count"`
	Fail  int   `json:"fail"`
	AvgMs int64 `json:"avg_ms"`
	sumMs int64
}

type aiFeedbackAgg struct {
	Total     int64 `json:"total"`
	Applied   int64 `json:"applied"`
	Helpful   int64 `json:"helpful"`
	Unhelpful int64 `json:"unhelpful"`
}

type aiStatsHub struct {
	mu             sync.Mutex
	recent         []aiCallStat
	cap            int
	total          int64
	fail           int64
	sumLatency     int64
	sumTokens      int64
	feedback       aiFeedbackAgg
	feedbackByTask map[string]aiFeedbackAgg
}

func newAIStatsHub() *aiStatsHub {
	return &aiStatsHub{
		cap:            200,
		recent:         make([]aiCallStat, 0, 64),
		feedbackByTask: make(map[string]aiFeedbackAgg),
	}
}

func (h *aiStatsHub) record(st aiCallStat) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.total++
	h.sumLatency += st.LatencyMs
	h.sumTokens += int64(st.ApproxTokens)
	if !st.OK {
		h.fail++
	}
	h.recent = append(h.recent, st)
	if len(h.recent) > h.cap {
		h.recent = h.recent[len(h.recent)-h.cap:]
	}
}

// recordFeedback records only the human quality signal. It intentionally does
// not retain prompt/answer content in telemetry.
func (h *aiStatsHub) recordFeedback(task, action string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	update := func(a *aiFeedbackAgg) {
		a.Total++
		switch action {
		case "applied":
			a.Applied++
		case "helpful":
			a.Helpful++
		case "unhelpful":
			a.Unhelpful++
		}
	}
	update(&h.feedback)
	a := h.feedbackByTask[task]
	update(&a)
	h.feedbackByTask[task] = a
}

func feedbackRates(a aiFeedbackAgg) (positiveRate, applyRate float64) {
	if a.Total == 0 {
		return 0, 0
	}
	return float64(a.Applied+a.Helpful) / float64(a.Total),
		float64(a.Applied) / float64(a.Total)
}

func (h *aiStatsHub) snapshot() map[string]any {
	if h == nil {
		return map[string]any{
			"total": 0, "fail": 0, "avg_latency_ms": 0, "fail_rate": 0,
			"approx_tokens_total": 0, "by_task": map[string]aiTaskAgg{}, "recent": []aiCallStat{},
			"feedback_total": 0, "feedback_applied": 0, "feedback_helpful": 0,
			"feedback_unhelpful": 0, "feedback_positive_rate": 0.0, "feedback_apply_rate": 0.0,
			"feedback_by_task": map[string]aiFeedbackAgg{},
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	avg := int64(0)
	if h.total > 0 {
		avg = h.sumLatency / h.total
	}
	failRate := 0.0
	if h.total > 0 {
		failRate = float64(h.fail) / float64(h.total)
	}
	byTask := map[string]*aiTaskAgg{}
	for _, r := range h.recent {
		m := byTask[r.Task]
		if m == nil {
			m = &aiTaskAgg{}
			byTask[r.Task] = m
		}
		m.Count++
		m.sumMs += r.LatencyMs
		if !r.OK {
			m.Fail++
		}
	}
	outByTask := map[string]aiTaskAgg{}
	for k, m := range byTask {
		if m.Count > 0 {
			m.AvgMs = m.sumMs / int64(m.Count)
		}
		outByTask[k] = aiTaskAgg{Count: m.Count, Fail: m.Fail, AvgMs: m.AvgMs}
	}
	recent := make([]aiCallStat, len(h.recent))
	copy(recent, h.recent)
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	if len(recent) > 30 {
		recent = recent[:30]
	}
	feedbackByTask := make(map[string]aiFeedbackAgg, len(h.feedbackByTask))
	for k, v := range h.feedbackByTask {
		feedbackByTask[k] = v
	}
	positiveRate, applyRate := feedbackRates(h.feedback)
	return map[string]any{
		"total":                  h.total,
		"fail":                   h.fail,
		"avg_latency_ms":         avg,
		"fail_rate":              failRate,
		"approx_tokens_total":    h.sumTokens,
		"by_task":                outByTask,
		"recent":                 recent,
		"feedback_total":         h.feedback.Total,
		"feedback_applied":       h.feedback.Applied,
		"feedback_helpful":       h.feedback.Helpful,
		"feedback_unhelpful":     h.feedback.Unhelpful,
		"feedback_positive_rate": positiveRate,
		"feedback_apply_rate":    applyRate,
		"feedback_by_task":       feedbackByTask,
	}
}

// recordAICallActor 同上，附带操作者（用于成本/用户分析）。
func (s *Server) recordAICallActor(ctx context.Context, task, model, actor string, latencyMs int64, ok bool, errStr string, memHits, skillHits int, reply string) {
	if s == nil || s.aiStats == nil {
		return
	}
	approx := estimateTokens(reply)
	promptTok, completionTok := 0, approx
	usageSource := "estimated"
	if p, c, got := takeCapturedAIUsageCtx(ctx); got {
		promptTok, completionTok = p, c
		if promptTok+completionTok > 0 {
			approx = promptTok + completionTok
			usageSource = "exact"
		}
	}
	cfg := AIConfig{}
	if s.cfg != nil {
		cfg = s.cfg.AIConfig()
	}
	st := aiCallStat{
		Ts: time.Now().Unix(), Task: task, Model: model, Actor: actor,
		LatencyMs: latencyMs, OK: ok, Error: trimLine(errStr, 200),
		MemHits: memHits, SkillHits: skillHits,
		ReplyChars: len([]rune(reply)), ApproxTokens: approx,
		PromptTokens: promptTok, CompletionTokens: completionTok,
		CostEstimate:  estimateAICost(cfg, promptTok, completionTok, approx),
		UsageSource:   usageSource,
		PromptVersion: promptVersionFor("assist-" + strings.TrimPrefix(task, "assist:")),
		RouteReason:   inferRouteReason(cfg, task, model),
	}
	s.aiStats.record(st)
	// AI 调用失败进自身故障归口。这里是**所有** AI 路径的公共出口（assist / Hermes /
	// 自动诊断 / 自动巡检），接一处即可全覆盖。
	//
	// 为什么值得接：AI 失败的形态是「问了没反应」——按钮转一下没结果、自动诊断悄悄
	// 什么都不写。用户看不出是模型配置错了、额度用完了还是网关挂了，而这三种的处理
	// 方式完全不同。指纹按 task + 错误原文归并，同一个错连续 3 次就开事件带原文。
	if !ok && strings.TrimSpace(errStr) != "" {
		reportFault("ai", "call_failed", "warning", "",
			"AI 调用失败（task="+task+"，model="+model+"）："+trimLine(errStr, 400)+
				"；此期间依赖 AI 的诊断/分析/助手会静默无结果", "")
	}
	if s.pg != nil {
		go s.pg.insertAICallEvent(st)
	}
	slog.Info("ai.call",
		"task", task, "model", model, "actor", actor, "latency_ms", latencyMs,
		"ok", ok, "memory_hits", memHits, "skill_hits", skillHits,
		"prompt_tokens", promptTok, "completion_tokens", completionTok,
		"approx_tokens", approx, "cost", st.CostEstimate, "err", errStr)
}

// streamOrchestratedAssist：assist 统一编排 —— RAG 注入、策略应用、流式调用、统计与记忆沉淀。
// datasourceID 可选：用于 promql/logql/pgsql 生成后的只读验证；doVerify=false 跳过探针。
func (s *Server) streamOrchestratedAssist(ctx context.Context, w http.ResponseWriter, cfg AIConfig, task, userMsg, contextText string, history []map[string]string, actor, datasourceID string, doVerify bool) string {
	policy := assistTaskPolicy(task)
	primaryModel := cfg.Model
	routedModel, _ := resolveModelForTask(cfg, task)
	cfg = applyRoutedModel(cfg, task)
	if routedModel == "" {
		routedModel = cfg.Model
	}
	safeCtx := sanitizeAssistContext(contextText)
	expID, variant := s.pickAssistExperiment(cfg, task, actor)
	cfg = s.applyExperimentVariantOn(cfg, expID, variant)
	if cfg.Model != "" && cfg.Model != routedModel {
		routedModel = cfg.Model
	}
	// 提示词装配走共享流水线（ai_prompt_shared.go）：安全条款、任务模板、PG 热加载
	// 模板、记忆与技能检索的顺序在那里只写一遍，两个 AI 入口不再各抄一份。
	ragQ := strings.TrimSpace(userMsg + " " + contextText)
	parts := s.buildAssistPrompt(cfg, assistPromptReq{
		Task: task, Actor: actor, RAGQuery: ragQ,
		ExperimentSuffix: experimentPromptSuffix(s, expID, variant),
		WithExternalMCP:  true,
	})
	sys := parts.System
	memHits, skillHits, skillNames := parts.MemHits, parts.SkillHits, parts.SkillNames
	cites := parts.Citations
	writeRAGMetaFull(w, memHits, skillHits, parts.Degraded, skillNames, cites)

	if strings.TrimSpace(userMsg) == "" {
		userMsg = "请根据上述上下文进行分析并给出结论。"
	}
	msgs := []map[string]string{{"role": "system", "content": sys}}
	if n := len(history); n > 0 {
		start := 0
		if n > 20 {
			start = n - 20
		}
		for _, h := range history[start:] {
			role, content := h["role"], strings.TrimSpace(h["content"])
			if (role == "user" || role == "assistant") && content != "" {
				msgs = append(msgs, map[string]string{"role": role, "content": content})
			}
		}
	}
	userPayload := userMsg
	if safeCtx != "" {
		userPayload = safeCtx + "\n\n【用户请求】\n" + userMsg
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": userPayload})
	if routedModel != "" && routedModel != primaryModel {
		payload := map[string]any{"meta": map[string]any{"routed_model": routedModel}}
		if expID != "" {
			payload["meta"].(map[string]any)["experiment_id"] = expID
			payload["meta"].(map[string]any)["variant"] = variant
		}
		if b, mErr := json.Marshal(payload); mErr == nil {
			fmt.Fprintf(w, "data: %s\n\n", b)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	} else if expID != "" {
		if b, mErr := json.Marshal(map[string]any{"meta": map[string]any{
			"experiment_id": expID, "variant": variant, "routed_model": routedModel,
		}}); mErr == nil {
			fmt.Fprintf(w, "data: %s\n\n", b)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}

	opts := aiCallOpts{
		DisableThinking: policy.DisableThink,
		EnableThinking:  policy.EnableThink,
		ThinkingBudget:  policy.ThinkingBudget,
		MaxTokens:       policy.MaxTokens,
		Timeout:         policy.Timeout,
	}
	start := time.Now()
	// 不发 [DONE]，以便在流末追加 assist_id / verify meta，再由本函数统一收尾。
	var reply, usedModel string
	var err error
	usedMoA := isHighRiskAssistTask(task) && len(moaModelList(cfg)) > 1
	if usedMoA {
		reply = aiChatMoAStream(ctx, w, cfg, msgs)
		usedModel = cfg.Model
		for _, m := range moaModelList(cfg) {
			if m == cfg.Model {
				continue
			}
			s.recordAICallActor(ctx, task+":moa", m, actor, 0, true, "", 0, 0, "")
		}
	} else {
		reply, usedModel, err = s.streamChatWithFallback(ctx, w, cfg, msgs, nil, false, opts)
		if err != nil && thinkingParamForcedTrueError(err) && !opts.EnableThinking {
			retry := opts
			retry.EnableThinking = true
			retry.DisableThinking = false
			if retry.ThinkingBudget <= 0 {
				retry.ThinkingBudget = 512
			}
			if retry.Timeout < 180*time.Second {
				retry.Timeout = 180 * time.Second
			}
			slog.Info("assist retry with enable_thinking=true", "task", task, "model", cfg.Model, "budget", retry.ThinkingBudget)
			start = time.Now()
			reply, usedModel, err = s.streamChatWithFallback(ctx, w, cfg, msgs, nil, false, retry)
		}
	}
	llmSelfVerify := false
	if err == nil && cfg.SelfVerify && isHighRiskAssistTask(task) && strings.TrimSpace(reply) != "" {
		vStart := time.Now()
		vtxt := streamSelfVerify(ctx, w, cfg, safeCtx, reply)
		llmSelfVerify = strings.TrimSpace(vtxt) != ""
		s.recordAICallActor(ctx, task+":verify", cfg.Model, actor, time.Since(vStart).Milliseconds(), true, "", 0, 0, vtxt)
		if llmSelfVerify {
			reply = reply + "\n\n" + vtxt
		}
	}
	if err == nil {
		reply, _ = sanitizeAssistActionReply(task, reply)
	}
	latency := time.Since(start).Milliseconds()
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	if usedModel == "" {
		usedModel = cfg.Model
	}
	s.recordAICallActor(ctx, task, usedModel, actor, latency, err == nil, errStr, memHits, skillHits, reply)

	assistID := ""
	var verify *assistVerifyResult
	if doVerify {
		switch strings.ToLower(strings.TrimSpace(task)) {
		case "promql", "logql", "pgsql", "sqlql":
			if strings.TrimSpace(reply) != "" {
				v := s.verifyAssistQuery(task, reply, contextText, datasourceID)
				verify = &v
			}
		}
	}
	if strings.TrimSpace(reply) != "" {
		assistID = newOpaqueID("run_")
		fb := ""
		if usedModel != cfg.Model {
			fb = usedModel
		}
		s.persistAIRun(AIRun{
			ID: assistID, Kind: "assist", Task: task, Actor: actor, Model: usedModel,
			Input: userMsg, Answer: reply, OK: err == nil, LatencyMs: latency,
			MemHits: memHits, SkillHits: skillHits, DataSourceID: datasourceID,
			VerifyJSON: verifyJSONBytes(verify),
			MetaJSON: agentMetaJSON(AgentLoopMeta{
				FallbackModel: fb,
				Citations:     len(cites),
				SelfVerify:    llmSelfVerify,
				RoutedModel:   routedModel,
				ExperimentID:  expID,
				Variant:       variant,
			}),
		})
	}
	if assistID != "" || verify != nil || llmSelfVerify || usedMoA {
		payload := map[string]any{}
		if assistID != "" {
			payload["assist_id"] = assistID
			payload["run_id"] = assistID
		}
		if verify != nil {
			payload["verify"] = verify
			payload["query_verified"] = true
		}
		payload["self_verify"] = llmSelfVerify
		payload["moa"] = usedMoA
		payload["memory_hits"] = memHits
		payload["skill_hits"] = skillHits
		payload["citations"] = len(cites)
		if len(skillNames) > 0 {
			payload["skills"] = skillNames
		}
		if routedModel != "" {
			payload["routed_model"] = routedModel
		}
		if expID != "" {
			payload["experiment_id"] = expID
			payload["variant"] = variant
		}
		if primaryModel != "" && usedModel != primaryModel {
			payload["primary_model"] = primaryModel
		}
		if b, mErr := json.Marshal(map[string]any{"meta": payload}); mErr == nil {
			fmt.Fprintf(w, "data: %s\n\n", b)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	if policy.AutoRemember && strings.TrimSpace(reply) != "" {
		rememberOK := true
		if policy.RememberKind == "chat" || policy.RememberKind == "assist" {
			rememberOK = s.shouldRememberPublicChat()
		}
		if rememberOK {
			go s.rememberAI(policy.RememberKind, policy.RememberSource,
				fmt.Sprintf("【AI 辅助·%s】\n%s\n\n【AI】\n%s", task, userMsg, reply))
		}
	}
	return reply
}
