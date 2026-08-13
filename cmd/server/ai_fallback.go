package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// AgentLoopMeta captures Hermes-style loop observability for ai_runs.meta_json.
type AgentLoopMeta struct {
	ToolTurns     int              `json:"tool_turns,omitempty"`
	Tools         []string         `json:"tools,omitempty"`
	FallbackModel string           `json:"fallback_model,omitempty"`
	MaxTurnsHit   bool             `json:"max_turns_hit,omitempty"`
	Citations     int              `json:"citations,omitempty"`
	SelfVerify    bool             `json:"self_verify,omitempty"`
	LiveEvidence  int              `json:"live_evidence,omitempty"`
	Actions       []map[string]any `json:"actions,omitempty"` // UI action cards collected from capability tools
	RoutedModel   string           `json:"routed_model,omitempty"`
	ExperimentID  string           `json:"experiment_id,omitempty"`
	Variant       string           `json:"variant,omitempty"`
	// ToolCites 是本轮工具（当前为 search_knowledge/WeKnora）产生的文档引用。
	// 按轮返回而非挂在引擎单例上，避免并发会话互相覆盖引用来源。不外发，仅供计数与记忆沉淀。
	ToolCites []RAGCitation `json:"-"`
}

func fallbackModelList(cfg AIConfig) []string {
	var out []string
	seen := map[string]bool{strings.TrimSpace(cfg.Model): true}
	for _, m := range strings.Split(cfg.FallbackModels, ",") {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// aiChatVWithFallback retries aiChatV across FallbackModels (Claude/Hermes-style resilience).
func aiChatVWithFallback(ctx context.Context, cfg AIConfig, messages []map[string]string, images []chatImage, tools []map[string]any, onFallback func(model string)) (reply string, calls []nativeToolCall, usedModel string, err error) {
	usedModel = cfg.Model
	reply, calls, err = aiChatV(ctx, cfg, messages, images, tools)
	if err == nil {
		return reply, calls, usedModel, nil
	}
	firstErr := err
	for _, model := range fallbackModelList(cfg) {
		retry := cfg
		retry.Model = model
		if onFallback != nil {
			onFallback(model)
		}
		reply, calls, err = aiChatV(ctx, retry, messages, images, tools)
		if err == nil {
			return reply, calls, model, nil
		}
	}
	if firstErr != nil {
		return "", nil, usedModel, firstErr
	}
	return "", nil, usedModel, err
}

// aiChatVStreamWithFallback mirrors streaming FC path with model fallback.
func aiChatVStreamWithFallback(ctx context.Context, cfg AIConfig, messages []map[string]string, images []chatImage, tools []map[string]any,
	onDelta func(string), onReasoning func(string), opts aiCallOpts, onFallback func(model string),
) (reply string, calls []nativeToolCall, usedModel string, err error) {
	usedModel = cfg.Model
	reply, calls, err = aiChatVStreamOpts(ctx, cfg, messages, images, tools, onDelta, onReasoning, opts)
	if err == nil {
		return reply, calls, usedModel, nil
	}
	firstErr := err
	for _, model := range fallbackModelList(cfg) {
		retry := cfg
		retry.Model = model
		if onFallback != nil {
			onFallback(model)
		}
		reply, calls, err = aiChatVStreamOpts(ctx, retry, messages, images, tools, onDelta, onReasoning, opts)
		if err == nil {
			return reply, calls, model, nil
		}
	}
	if firstErr != nil {
		return "", nil, usedModel, firstErr
	}
	return "", nil, usedModel, err
}

func emitFallbackSSE(w http.ResponseWriter, model string) {
	if w == nil || model == "" {
		return
	}
	fmt.Fprintf(w, "data: {\"meta\":{\"fallback_model\":%s}}\n\n", jsonString(model))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func agentMetaJSON(m AgentLoopMeta) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
