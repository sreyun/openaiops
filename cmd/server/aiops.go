package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// AI layer — automated inspection + agent-style incident diagnosis.
//
// The LLM is a PLUGGABLE ENHANCEMENT ("埋点"): configure any OpenAI-compatible
// chat/completions endpoint and the inspector/diagnoser use it; leave it off and
// a built-in heuristic engine produces the same structured output, so the whole
// feature works out of the box with zero external dependencies.
// ============================================================================

// AIConfig configures the optional AI provider.
type AIConfig struct {
	Enabled            bool   `json:"enabled"`
	Endpoint           string `json:"endpoint"` // e.g. https://api.openai.com/v1/chat/completions
	APIKey             string `json:"api_key,omitempty"`
	Model              string `json:"model"`                // e.g. gpt-4o-mini / a local model name
	InspectIntervalMin int    `json:"inspect_interval_min"` // 0 = default 30
	MaxTokens          int    `json:"max_tokens,omitempty"` // 单次输出 token 上限（0 = 默认 4096）
	// 嵌入（向量化 / RAG）配置——与对话模型解耦，可指向任意 OpenAI 兼容 /embeddings 服务，
	// 不再绑定阿里百炼。留空时回退到主 Endpoint / API Key。
	EmbedEndpoint   string `json:"embed_endpoint,omitempty"`   // 嵌入端点，留空=复用主 Endpoint
	EmbedAPIKey     string `json:"embed_api_key,omitempty"`    // 嵌入 Key，留空=复用主 API Key
	EmbedModel      string `json:"embed_model,omitempty"`      // 嵌入模型，如 text-embedding-3-small / text-embedding-v2
	EmbedDimensions int    `json:"embed_dimensions,omitempty"` // 目标维度，默认 1536（须与 pgvector 列一致）
	// 可选重排（rerank）：配置后 RAG 先按向量过取候选，再用 rerank 模型精排 Top-K，显著提升召回
	// 相关性。留空=不启用（行为不变）。兼容 Jina / Cohere / 百炼 / SiliconFlow 等 OpenAI 风格
	// /rerank；Endpoint / Key 留空时依次回退到嵌入配置、主配置。
	RerankEndpoint string `json:"rerank_endpoint,omitempty"`
	RerankModel    string `json:"rerank_model,omitempty"`
	RerankAPIKey   string `json:"rerank_api_key,omitempty"`
	// 自我校验：诊断生成后追加一遍【独立】审校（对照证据复核结论、纠偏、给复核可信度）。
	// 默认关闭——多一次 LLM 调用；对高价值诊断建议开启。
	SelfVerify bool `json:"self_verify,omitempty"`
	// Mixture-of-Agents：额外参与「集成研判」的模型名（逗号分隔）。配置后【诊断】走多模型并行
	// 提案 → 聚合模型综合成一份更稳的结论。默认空=关闭（成本可控）。这些模型复用主 Endpoint / Key。
	MoAModels string `json:"moa_models,omitempty"`
	// FallbackModels：主模型失败时按序切换的备用模型（逗号分隔，复用主 Endpoint/Key）。
	FallbackModels string `json:"fallback_models,omitempty"`
	// CheapModel：摘要/轻量任务优先模型；空=用 Model。
	CheapModel string `json:"cheap_model,omitempty"`
	// TaskModelsJSON：任务→模型映射，例 {"chart_analysis":"qwen-plus","promql":"qwen-turbo"}。
	TaskModelsJSON string `json:"task_models_json,omitempty"`
	// ActiveExperimentID：可选 A/B 实验 id（配合 ai_experiments 表）。
	ActiveExperimentID string `json:"active_experiment_id,omitempty"`
	// MCP Server：把本平台的只读运维工具（指标/日志/告警/硬件/流量等）暴露为标准 MCP，供外部
	// Agent（如 Nous Sreyun Agent）连接调用。默认关闭；开启需设置 Bearer Token（客户端用它鉴权）。
	MCPEnabled bool   `json:"mcp_enabled,omitempty"`
	MCPToken   string `json:"mcp_token,omitempty"`
	// MCPScopedTokensJSON：多 Token 作用域，例 [{"name":"metrics","token":"xxx","scopes":["metrics","alerts"]}]
	// scopes: metrics|logs|sql|hardware|infra|knowledge|all
	MCPScopedTokensJSON string `json:"mcp_scoped_tokens_json,omitempty"`
	// MCPClientsJSON：外部 MCP Server 客户端列表（本平台作为 Client 去连别人）。
	// 例 [{"id":"jira","name":"Jira","enabled":true,"url":"https://…/mcp","headers":{"Authorization":"Bearer …"},"timeout_sec":30}]
	MCPClientsJSON string `json:"mcp_clients_json,omitempty"`
	// WeKnora：外部文档知识库（腾讯开源 RAG）。本平台不建文档入库，仅通过 API URL + API Key
	// 调用 knowledge-search，供 search_knowledge 工具在诊断/对话时检索手册类知识。
	WeKnoraEnabled          bool   `json:"weknora_enabled,omitempty"`
	WeKnoraURL              string `json:"weknora_url,omitempty"`                // 如 http://weknora:8080 或 …/api/v1
	WeKnoraAPIKey           string `json:"weknora_api_key,omitempty"`            // X-API-Key
	WeKnoraKnowledgeBaseIDs string `json:"weknora_knowledge_base_ids,omitempty"` // 逗号分隔；可空=自动枚举全部可见库再检索
	// DisablePublicChatMemory：开启后对话/助手回复不再写入公共向量记忆（敏感场景）；结案/诊断/采纳沉淀不受影响。
	DisablePublicChatMemory bool `json:"disable_public_chat_memory,omitempty"`
	// AllowUnverifiedAIOutputLearning：显式允许把尚未被人工采纳的普通对话输出写入 RAG。
	// 默认关闭，防止提示注入或模型幻觉污染长期记忆；推荐依赖采纳/反馈/执行结果学习。
	AllowUnverifiedAIOutputLearning bool `json:"allow_unverified_ai_output_learning,omitempty"`
	// Sreyun Agent 配置（对外 JSON 字段一律使用 ai_*，禁止 hermes 字样）
	SreyunEnabled         bool `json:"ai_agent_enabled,omitempty"`    // 启用自主 Agent
	SreyunAutoApprove     bool `json:"ai_auto_approve,omitempty"`     // 低风险操作自动执行
	SreyunTerminalEnabled bool `json:"ai_terminal_enabled,omitempty"` // AI 终端只读巡检权限
	// 成本估算单价（每 100 万 token）；用于 AI 调用观测与历史组合曲线。0=不估算费用（仍记 token）。
	InputPricePer1M  float64 `json:"input_price_per_1m,omitempty"`
	OutputPricePer1M float64 `json:"output_price_per_1m,omitempty"`
	CostCurrency     string  `json:"cost_currency,omitempty"` // CNY | USD …
	// ModelPricingJSON：按模型覆盖成本单价，{"gpt-4o-mini":{"input_per_1m":0.15,"output_per_1m":0.6}}
	ModelPricingJSON string `json:"model_pricing_json,omitempty"`
	// MaxCostPerQueryCNY：单次 AI 调用成本护栏（元）；0=不限制
	MaxCostPerQueryCNY float64 `json:"max_cost_per_query_cny,omitempty"`
	// ---- AI 治理（商业级最小集）----
	// DailyQuotaPerUser：每用户每日 AI 调用上限（Assist/Chat/Sreyun/Diagnose 等）；0=不限制。
	DailyQuotaPerUser int `json:"daily_quota_per_user,omitempty"`
	// QuotaExemptTasks：逗号分隔任务名（如 distill,embed_test），这些入口不计入日配额；默认空=全部计入。
	QuotaExemptTasks string `json:"quota_exempt_tasks,omitempty"`
	// MCPRateLimitPerMin：MCP Bearer 每分钟调用上限；0=默认 60。
	MCPRateLimitPerMin int `json:"mcp_rate_limit_per_min,omitempty"`
	// WriteToolsRequireApproval：写工具是否强制 approval_id。
	// 与 ai_auto_approve 组合：仅当 auto_approve=true 且本开关=false 时可跳过审批（实验室模式）；
	// 默认/生产请保持本开关为 true（前端默认勾选）。
	WriteToolsRequireApproval bool `json:"write_tools_require_approval,omitempty"`
	// RedactSensitiveFields：对提示词/响应做轻量脱敏后再落审计或展示。
	RedactSensitiveFields bool `json:"redact_sensitive_fields,omitempty"`
	// PromptOverridesDir：部署级提示词覆盖目录。存在时同名 .md（如 assist-logql.md）
	// 优先于内嵌模板，私有化客户可改提示词而无需重编译。空=仅用内嵌。
	PromptOverridesDir string `json:"prompt_overrides_dir,omitempty"`
	// ---- AI 语音（可选云端 STT/TTS；留空则客户端继续用系统/浏览器语音）----
	// SpeechEndpoint：OpenAI 兼容音频根路径（如 https://api.openai.com/v1）；留空=复用主 Endpoint 的 /v1 根。
	SpeechEndpoint string `json:"speech_endpoint,omitempty"`
	SpeechAPIKey   string `json:"speech_api_key,omitempty"`   // 留空=复用主 API Key
	SpeechSTTModel string `json:"speech_stt_model,omitempty"` // 如 whisper-1 / gpt-4o-mini-transcribe
	SpeechTTSModel string `json:"speech_tts_model,omitempty"` // 如 tts-1 / gpt-4o-mini-tts
	SpeechTTSVoice string `json:"speech_tts_voice,omitempty"` // alloy / nova / shimmer …
	// SpeechPreferCloud：开启后 Web/移动端优先走云端语音（需配置 STT 或 TTS 模型）。
	SpeechPreferCloud bool `json:"speech_prefer_cloud,omitempty"`
	// AutoDefendEnabled：登录撞库 / MCP 滥用等安全事件自动留痕、沉淀记忆并可选创建事件单。
	AutoDefendEnabled bool `json:"auto_defend_enabled,omitempty"`
	// SelfEvolveEnabled：每日维护循环额外执行技能提炼后的成长日记与自我优化总结。
	SelfEvolveEnabled bool `json:"self_evolve_enabled,omitempty"`
}

// UnmarshalJSON accepts both ai_* (preferred) and legacy hermes_* keys so upgrades
// do not drop Agent settings; marshaling always emits ai_* only.
func (c *AIConfig) UnmarshalJSON(b []byte) error {
	type alias AIConfig
	var plain alias
	if err := json.Unmarshal(b, &plain); err != nil {
		return err
	}
	*c = AIConfig(plain)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil
	}
	pickBool := func(keys ...string) (bool, bool) {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				var b bool
				if json.Unmarshal(v, &b) == nil {
					return b, true
				}
			}
		}
		return false, false
	}
	if v, ok := pickBool("ai_agent_enabled", "hermes_enabled"); ok {
		c.SreyunEnabled = v
	}
	if v, ok := pickBool("ai_auto_approve", "hermes_auto_approve"); ok {
		c.SreyunAutoApprove = v
	}
	if v, ok := pickBool("ai_terminal_enabled", "hermes_terminal_enabled"); ok {
		c.SreyunTerminalEnabled = v
	}
	return nil
}

// embedReady reports whether embedding (RAG write/retrieve) can run.
// Uses dedicated embed credentials when set; otherwise falls back to chat endpoint/key.
func embedReady(cfg AIConfig) bool {
	if !cfg.Enabled {
		return false
	}
	key := strings.TrimSpace(cfg.EmbedAPIKey)
	if key == "" {
		key = strings.TrimSpace(cfg.APIKey)
	}
	ep := strings.TrimSpace(cfg.EmbedEndpoint)
	if ep == "" {
		ep = strings.TrimSpace(cfg.Endpoint)
	}
	return key != "" && ep != ""
}

func quotaTaskExempt(cfg AIConfig, task string) bool {
	task = strings.ToLower(strings.TrimSpace(task))
	if task == "" || strings.TrimSpace(cfg.QuotaExemptTasks) == "" {
		return false
	}
	for _, p := range strings.Split(cfg.QuotaExemptTasks, ",") {
		if strings.ToLower(strings.TrimSpace(p)) == task {
			return true
		}
	}
	return false
}

// aiProviderType classifies the AI endpoint so the request/response format can be
// chosen automatically.
type aiProviderType int

const (
	aiProvOpenAI    aiProviderType = iota // OpenAI 兼容 chat/completions（默认，覆盖 OpenAI/DeepSeek/Ollama/百炼兼容模式/本地模型等）
	aiProvAnthropic                       // Anthropic Messages API（Claude，直连 api.anthropic.com 或百炼 /apps/anthropic）
)

// isBailianEndpoint reports whether the endpoint targets Alibaba Bailian / Model Studio / MaaS.
func isBailianEndpoint(endpoint string) bool {
	ep := strings.ToLower(endpoint)
	return strings.Contains(ep, "dashscope.aliyuncs.com") ||
		strings.Contains(ep, "maas.aliyuncs.com") ||
		strings.Contains(ep, "dashscope-intl.aliyuncs.com")
}

// normalizeEndpoint auto-corrects common endpoint mistakes and classifies the
// provider type so the caller can pick the right request/response format.
//
// 分类规则（仅两种类型）：
//   - 端点含 "anthropic"（直连 api.anthropic.com 或百炼 /apps/anthropic）→ aiProvAnthropic
//   - 其余一律 → aiProvOpenAI（OpenAI 兼容 chat/completions，含百炼兼容模式/DeepSeek/Ollama…）
//
// 对于非 Anthropic 端点，若未包含 /chat/completions，自动补齐（兼容自定义端点）。
func normalizeEndpoint(endpoint string) (string, aiProviderType) {
	ep := strings.TrimRight(endpoint, "/")
	// Anthropic Messages API（Claude）：端点按用户填写原样使用（预设已填好），不追加 /chat/completions。
	if strings.Contains(ep, "anthropic") {
		return ep, aiProvAnthropic
	}
	// 其余全部按 OpenAI 兼容处理。若未含 /chat/completions 则自动补齐，兼容任意自定义端点。
	if !strings.HasSuffix(ep, "/chat/completions") && !strings.Contains(ep, "/chat/completions?") {
		ep += "/chat/completions"
	}
	return ep, aiProvOpenAI
}

// providerHTTPErrorMsg 把 provider 返回的非 200 状态转成用户友好的中文说明。
// 关键：/chat/completions 在端点正确时，404 通常是【模型不存在/非对话模型】而非端点错误，
// 所以 404 优先指向模型（此前一律说"端点不存在"会误导用户）。
func providerHTTPErrorMsg(status int, body string, cfg AIConfig) string {
	body = trimLine(strings.TrimSpace(body), 220)
	suffix := ""
	if body != "" {
		suffix = "\n原始响应：" + body
	}
	switch status {
	case http.StatusNotFound: // 404
		msg := fmt.Sprintf("HTTP 404：模型不存在或该端点不支持此模型。请确认【模型名】%q 是否为该服务商有效的“对话(chat)”模型（嵌入/语音/图像等模型不能用于对话），或换用其它模型；并确认 Endpoint 正确。", cfg.Model)
		if isBailianEndpoint(cfg.Endpoint) {
			msg += "\n百炼 OpenAI 兼容 Endpoint 应为 https://dashscope.aliyuncs.com/compatible-mode/v1"
		}
		return msg + suffix
	case http.StatusUnauthorized, http.StatusForbidden: // 401/403
		lb := strings.ToLower(body)
		if strings.Contains(lb, "access denied") || strings.Contains(lb, "accessdenied") ||
			strings.Contains(lb, "not activated") || strings.Contains(lb, "未开通") || strings.Contains(lb, "no permission") {
			return fmt.Sprintf("HTTP %d：模型 %q 未开通 / 未授权（Model.AccessDenied）。该模型在你的账号下没有访问权限，请到服务商控制台开通此模型，或改用已开通的模型（百炼一般默认可用 qwen-plus / qwen-turbo / qwen-max）。", status, cfg.Model) + suffix
		}
		return fmt.Sprintf("HTTP %d：认证失败，请检查 API Key 是否正确、是否有权调用模型 %q。", status, cfg.Model) + suffix
	case http.StatusBadRequest: // 400
		if body != "" {
			return "HTTP 400：请求参数错误 — " + body
		}
		return fmt.Sprintf("HTTP 400：请求参数错误，请检查模型名称是否正确（当前：%s）", cfg.Model)
	default:
		if body != "" {
			return fmt.Sprintf("HTTP %d：%s", status, body)
		}
		return fmt.Sprintf("HTTP %d：服务端返回异常状态码", status)
	}
}

// aiChat calls an OpenAI-compatible, Bailian-native, or Anthropic-compatible
// chat/completions endpoint with a full message list (multi-turn, stdlib only).
// On a non-200 it surfaces a snippet of the provider's error body so the caller
// (e.g. the config test) can show WHY it failed.
// chatImage 是要发给多模态(视觉)模型的一张图片：MIME 类型 + base64 数据（不含 data: 前缀）。
type chatImage struct {
	MIME string
	Data string
}

// buildRequestMessages 把纯文本消息转成请求消息；若带图片，则把图片附到「最后一条非工具结果的
// user 消息」（即用户本轮提问），并按 provider 生成多模态 content 数组。无图片时与原来完全一致。
func buildRequestMessages(messages []map[string]string, images []chatImage, prov aiProviderType) []map[string]any {
	anchor := -1
	if len(images) > 0 {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i]["role"] == "user" && !strings.HasPrefix(messages[i]["content"], "工具执行结果") {
				anchor = i
				break
			}
		}
	}
	out := make([]map[string]any, 0, len(messages))
	for i, m := range messages {
		if i == anchor {
			out = append(out, map[string]any{"role": m["role"], "content": multimodalContent(m["content"], images, prov)})
		} else {
			out = append(out, map[string]any{"role": m["role"], "content": m["content"]})
		}
	}
	return out
}

// multimodalContent 生成「文本 + 图片」的 content 数组（OpenAI 用 image_url，Anthropic 用 image/source）。
func multimodalContent(text string, images []chatImage, prov aiProviderType) []map[string]any {
	parts := make([]map[string]any, 0, len(images)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		if img.Data == "" {
			continue
		}
		mime := img.MIME
		if mime == "" {
			mime = "image/png"
		}
		if prov == aiProvAnthropic {
			parts = append(parts, map[string]any{
				"type":   "image",
				"source": map[string]any{"type": "base64", "media_type": mime, "data": img.Data},
			})
		} else {
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": "data:" + mime + ";base64," + img.Data},
			})
		}
	}
	return parts
}

func aiChat(cfg AIConfig, messages []map[string]string) (string, error) {
	text, _, err := aiChatV(context.Background(), cfg, messages, nil, nil)
	return text, err
}

// aiCallOpts 控制单次 LLM 调用的行为覆盖。
// AI-First：看板任务开启思考（EnableThinking），但必须用 ThinkingBudget 卡住思维链长度，
// 否则模型会把超时/输出额度耗在「想」上，最终 JSON/正文出不来。
// 切勿对要求 enable_thinking=true 的网关硬传 false（会直接 HTTP 400）。
type aiCallOpts struct {
	DisableThinking bool          // 尽量关闭思考（仅提示词；API 不再传 enable_thinking=false）
	EnableThinking  bool          // 显式开启思考（专业 BI 生成/优化）
	ThinkingBudget  int           // 思考 token 上限（Qwen/DashScope thinking_budget）；0=不传
	MaxTokens       int           // 本请求输出上限；0 则回退 cfg.MaxTokens / 默认策略
	Timeout         time.Duration // 请求超时，0 = 默认 120s
}

// thinkingModelOrGateway reports whether this model/endpoint is likely to run
// extended chain-of-thought by default (Qwen3 / QwQ / DeepSeek-R1 / Bailian…).
func thinkingModelOrGateway(cfg AIConfig) bool {
	m := strings.ToLower(cfg.Model)
	ep := strings.ToLower(cfg.Endpoint)
	if isBailianEndpoint(cfg.Endpoint) || strings.Contains(ep, "dashscope") ||
		strings.Contains(ep, "siliconflow") || strings.Contains(ep, "maas.aliyuncs") {
		return true
	}
	for _, k := range []string{"qwen3", "qwen-3", "qwen3.", "qwq", "thinking", "deepseek-r1", "reasoner", "-r1", "o1-", "o3-"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

// applyThinkingKnobs 按任务偏好注入思考开关与预算。
// 重要：部分百炼/Qwen 网关规定 enable_thinking 只能为 true；传 false 会 400。
// EnableThinking 时显式 true，并尽量带 thinking_budget，避免思维链耗尽超时/输出额度。
func applyThinkingKnobs(reqBody map[string]any, cfg AIConfig, prov aiProviderType, opts aiCallOpts) {
	if prov == aiProvAnthropic || !thinkingModelOrGateway(cfg) {
		return
	}
	if opts.EnableThinking {
		reqBody["enable_thinking"] = true
		budget := opts.ThinkingBudget
		if budget <= 0 {
			// 看板等结构化任务的安全默认：够用且不会拖死生成
			budget = 512
		}
		if budget > 0 {
			reqBody["thinking_budget"] = budget
		}
		return
	}
	// DisableThinking 或默认：省略 enable_thinking 字段（兼容「仅允许 true」与「不识别该字段」两类网关）
}

// applyOutputTokenLimit 设置本请求 max_tokens：优先 opts，其次全局配置，再按 provider 默认。
func applyOutputTokenLimit(reqBody map[string]any, cfg AIConfig, prov aiProviderType, opts aiCallOpts) {
	if opts.MaxTokens > 0 {
		reqBody["max_tokens"] = opts.MaxTokens
		return
	}
	if cfg.MaxTokens > 0 {
		reqBody["max_tokens"] = cfg.MaxTokens
		return
	}
	if prov == aiProvAnthropic {
		reqBody["max_tokens"] = 8192
		return
	}
	// 看板结构化任务未显式设 MaxTokens 时，OpenAI 兼容不强制；其余保持不传。
	delete(reqBody, "max_tokens")
}

// thinkingParamForcedTrueError 识别「enable_thinking 只能为 True」类 400，便于自动重试。
func thinkingParamForcedTrueError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if !strings.Contains(s, "enable_thinking") {
		return false
	}
	return strings.Contains(s, "restricted to true") ||
		strings.Contains(s, "must be true") ||
		(strings.Contains(s, "only") && strings.Contains(s, "true")) ||
		strings.Contains(s, "restricted to true")
}

// withNoThinkHint prepends a hard constraint so models that ignore API knobs
// still skip chain-of-thought (and appends /no_think for Qwen-style templates).
func withNoThinkHint(messages []map[string]string, cfg AIConfig) []map[string]string {
	out := make([]map[string]string, len(messages))
	copy(out, messages)
	const ban = "【重要】本任务禁止深度思考与思维链输出，不要逐步推理，直接给出最终答案。"
	for i := range out {
		if out[i]["role"] == "system" {
			out[i] = map[string]string{"role": "system", "content": ban + "\n" + out[i]["content"]}
			break
		}
	}
	if thinkingModelOrGateway(cfg) {
		for i := len(out) - 1; i >= 0; i-- {
			if out[i]["role"] == "user" {
				c := out[i]["content"]
				if !strings.Contains(c, "/no_think") {
					out[i] = map[string]string{"role": "user", "content": c + "\n/no_think"}
				}
				break
			}
		}
	}
	return out
}

// nativeToolCall represents a tool call parsed from the LLM's native function calling response.
// P3-1: 使用 LLM 原生 Function Calling 替代文本解析，更可靠地提取工具调用。
type nativeToolCall struct {
	ID   string         // 工具调用 ID（OpenAI 格式），用于将结果关联回特定调用
	Name string         // 工具名称
	Args map[string]any // 工具参数
}

// aiChatV 在 aiChat 基础上支持传入 ctx（客户端中止 / 超时控制）+ 给用户消息附带图片（多模态/视觉）。
// P3-1: 新增 tools 参数，当非 nil 且为 OpenAI 兼容 Provider 时，使用原生 Function Calling。
// 返回 (文本回复, 原生工具调用列表, error)。无工具调用时 toolCalls 为 nil。
func aiChatV(ctx context.Context, cfg AIConfig, messages []map[string]string, images []chatImage, tools []map[string]any) (string, []nativeToolCall, error) {
	return aiChatVOpts(ctx, cfg, messages, images, tools, aiCallOpts{})
}

func aiChatVOpts(ctx context.Context, cfg AIConfig, messages []map[string]string, images []chatImage, tools []map[string]any, opts aiCallOpts) (string, []nativeToolCall, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var slotID uint64
	var owned bool
	ctx, slotID, owned = bindAIUsageSlot(ctx)
	if owned {
		defer endAIUsageSlot(slotID)
	}
	if cfg.Endpoint == "" || cfg.Model == "" {
		return "", nil, fmt.Errorf("AI Endpoint 或模型名未配置，请先在「AI 设置」中填写并保存")
	}
	// EnableThinking 优先：质量任务允许深度思考；仅在未启用时才注入「少思考」提示。
	if opts.DisableThinking && !opts.EnableThinking {
		messages = withNoThinkHint(messages, cfg)
	}

	ep, prov := normalizeEndpoint(cfg.Endpoint)

	var reqBody map[string]any
	var extraHeaders map[string]string

	reqMsgs := buildRequestMessages(messages, images, prov) // 无图片时等价于原 messages
	switch prov {
	case aiProvAnthropic:
		// Anthropic Messages API format. system prompt is a top-level field, not a message role.
		var sys string
		var userMsgs []map[string]any
		for _, m := range reqMsgs {
			if m["role"] == "system" && sys == "" {
				if s, ok := m["content"].(string); ok {
					sys = s
				}
			} else {
				userMsgs = append(userMsgs, m)
			}
		}
		reqBody = map[string]any{
			"model":      cfg.Model,
			"max_tokens": 4096,
			"messages":   userMsgs,
		}
		if sys != "" {
			reqBody["system"] = sys
		}
		extraHeaders = map[string]string{
			"x-api-key":         cfg.APIKey,
			"anthropic-version": "2023-06-01",
		}
	default: // aiProvOpenAI
		reqBody = map[string]any{
			"model":       cfg.Model,
			"messages":    reqMsgs,
			"temperature": 0.2,
			"stream":      false,
		}
		// P3-1: 当传入工具定义时，使用 OpenAI 原生 Function Calling
		if len(tools) > 0 {
			reqBody["tools"] = tools
			reqBody["tool_choice"] = "auto"
		}
	}
	applyThinkingKnobs(reqBody, cfg, prov, opts)
	applyOutputTokenLimit(reqBody, cfg, prov, opts)

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	b, _ := json.Marshal(reqBody)
	// 请求级 ctx：既受客户端「终止」影响、又设超时上限（模型慢时不至于过早断开）
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, ep, bytes.NewReader(b))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if prov == aiProvAnthropic {
		// Anthropic uses x-api-key header instead of Authorization: Bearer
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
	} else if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client := newGuardedHTTPClient(timeout + 5*time.Second) // SSRF：用户可配 AI Endpoint，拦元数据/链路本地
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil { // 用户主动终止
			return "", nil, fmt.Errorf("已终止")
		}
		if reqCtx.Err() == context.DeadlineExceeded || strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "Client.Timeout") {
			sec := int(timeout.Seconds())
			if sec <= 0 {
				sec = 120
			}
			return "", nil, fmt.Errorf("AI 响应超时（>%d 秒）。深度思考模型耗时更长，可重试、适当增大等待，或检查 Endpoint / 网络。", sec)
		}
		return "", nil, fmt.Errorf("网络请求失败：%v（请检查 Endpoint 与网络）", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
		return "", nil, fmt.Errorf("%s", providerHTTPErrorMsg(resp.StatusCode, string(body), cfg))
	}

	// Parse response according to provider type.
	switch prov {
	case aiProvAnthropic:
		// Anthropic response: {"content":[{"type":"text","text":"..."}],"role":"assistant",...}
		var out struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Role string `json:"role"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", nil, fmt.Errorf("解析 Anthropic 响应失败：%v", err)
		}
		// Collect text blocks from content array
		var texts []string
		for _, c := range out.Content {
			if c.Type == "text" && c.Text != "" {
				texts = append(texts, c.Text)
			}
		}
		if len(texts) == 0 {
			return "", nil, fmt.Errorf("Anthropic API 返回空结果")
		}
		return strings.TrimSpace(strings.Join(texts, "\n")), nil, nil

	default: // aiProvOpenAI
		// P3-1: 扩展响应解析，支持原生 Function Calling 的 tool_calls 字段
		var out struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"` // JSON 字符串
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", nil, fmt.Errorf("解析 AI 响应失败：%v", err)
		}
		if out.Usage != nil {
			captureAIUsageCtx(ctx, out.Usage.PromptTokens, out.Usage.CompletionTokens)
		}
		if len(out.Choices) == 0 {
			return "", nil, fmt.Errorf("AI 服务返回空结果")
		}
		content := strings.TrimSpace(out.Choices[0].Message.Content)
		// 解析原生 tool_calls
		var calls []nativeToolCall
		for _, tc := range out.Choices[0].Message.ToolCalls {
			var args map[string]any
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			calls = append(calls, nativeToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: args,
			})
		}
		return content, calls, nil
	}
}

// streamToolChunk 解析 OpenAI 兼容流式响应里的 choices[0].delta，同时覆盖 content（正文增量）
// 与 tool_calls（原生 Function Calling 增量）。tool_calls 以 index 分片到达，需按 index 累积。
type streamToolChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"` // 推理模型思维链（DeepSeek-R1/QwQ/Qwen3-thinking）
			Reasoning        string `json:"reasoning"`         // 部分网关字段名
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// aiChatVStream 是面向 OpenAI 兼容 Provider 的「流式 + 原生 Function Calling」调用：
//   - content 增量通过 onDelta 逐字回调，供上层实时下发前端，实现真正的逐字流式；
//   - tool_calls 增量按 index 累积（id/name 取首个非空、arguments 分片拼接），流结束后解析为
//     nativeToolCall 列表返回。
//
// 原生 FC 下 content 与 tool_calls 天然分离，直接透传 content 不会把工具 JSON 泄漏给用户。
// 仅支持 OpenAI 兼容端点；Anthropic 走非流式 aiChatV（其流式 tool-use 帧格式不同，成本高）。
// ctx 用于客户端断开/超时时中止在途请求。
func aiChatVStream(ctx context.Context, cfg AIConfig, messages []map[string]string, images []chatImage, tools []map[string]any, onDelta, onReasoning func(string)) (string, []nativeToolCall, error) {
	return aiChatVStreamOpts(ctx, cfg, messages, images, tools, onDelta, onReasoning, aiCallOpts{})
}

func aiChatVStreamOpts(ctx context.Context, cfg AIConfig, messages []map[string]string, images []chatImage, tools []map[string]any, onDelta, onReasoning func(string), opts aiCallOpts) (string, []nativeToolCall, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var slotID uint64
	var owned bool
	ctx, slotID, owned = bindAIUsageSlot(ctx)
	if owned {
		defer endAIUsageSlot(slotID)
	}
	if cfg.Endpoint == "" || cfg.Model == "" {
		return "", nil, fmt.Errorf("AI Endpoint 或模型名未配置")
	}
	ep, prov := normalizeEndpoint(cfg.Endpoint)
	if prov == aiProvAnthropic { // 兜底：不应走到这里，直接回退非流式
		return aiChatVOpts(ctx, cfg, messages, images, tools, opts)
	}
	if opts.DisableThinking && !opts.EnableThinking {
		messages = withNoThinkHint(messages, cfg)
	}

	reqBody := map[string]any{
		"model":          cfg.Model,
		"messages":       buildRequestMessages(messages, images, prov), // 无图片时等价原 messages
		"temperature":    0.2,
		"stream":         true,
		"stream_options": openAIStreamUsageOptions(),
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = "auto"
	}
	applyThinkingKnobs(reqBody, cfg, prov, opts)
	applyOutputTokenLimit(reqBody, cfg, prov, opts)

	b, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(b))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client := newGuardedHTTPClient(125 * time.Second) // SSRF：用户可配 AI Endpoint，拦元数据/链路本地
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, fmt.Errorf("已终止")
		}
		return "", nil, fmt.Errorf("网络请求失败：%v（请检查 Endpoint 与网络）", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
		return "", nil, fmt.Errorf("%s", providerHTTPErrorMsg(resp.StatusCode, string(body), cfg))
	}

	var content strings.Builder
	// 按 index 累积分片到达的 tool_calls；order 保留首次出现顺序，保证多工具调用顺序稳定。
	type toolAccumulator struct {
		id, name string
		args     strings.Builder
	}
	toolAcc := map[int]*toolAccumulator{}
	var order []int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 4*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil { // 客户端断开：中止读取，释放 provider 连接
			return content.String(), nil, ctx.Err()
		}
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk streamToolChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			captureAIUsageCtx(ctx, chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if r := d.ReasoningContent; r != "" && onReasoning != nil {
			onReasoning(r) // 思维链走独立通道，不计入 content / 最终答案
		} else if d.Reasoning != "" && onReasoning != nil {
			onReasoning(d.Reasoning)
		}
		if d.Content != "" {
			content.WriteString(d.Content)
			if onDelta != nil {
				onDelta(d.Content)
			}
		}
		for _, tc := range d.ToolCalls {
			a := toolAcc[tc.Index]
			if a == nil {
				a = &toolAccumulator{}
				toolAcc[tc.Index] = a
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				a.id = tc.ID
			}
			if tc.Function.Name != "" {
				a.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				a.args.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		// 已有部分产出则算部分成功；完全无产出才报错
		if content.Len() == 0 && len(order) == 0 {
			return "", nil, fmt.Errorf("读取流式响应失败：%v", err)
		}
	}

	var calls []nativeToolCall
	for _, idx := range order {
		a := toolAcc[idx]
		if a.name == "" {
			continue
		}
		var args map[string]any
		if s := strings.TrimSpace(a.args.String()); s != "" {
			_ = json.Unmarshal([]byte(s), &args)
		}
		calls = append(calls, nativeToolCall{ID: a.id, Name: a.name, Args: args})
	}
	return content.String(), calls, nil
}

// aiComplete is the single-turn (system + user) convenience wrapper around aiChat.
func aiComplete(cfg AIConfig, system, user string) (string, error) {
	return aiCompleteOpts(cfg, system, user, aiCallOpts{})
}

// aiCompleteOpts is aiComplete with per-call overrides (thinking / timeout).
// 若网关强制 enable_thinking=true，自动以 EnableThinking 重试一次（AI-First：优先成功）。
func aiCompleteOpts(cfg AIConfig, system, user string, opts aiCallOpts) (string, error) {
	msgs := []map[string]string{
		{"role": "system", "content": system},
		{"role": "user", "content": user},
	}
	text, _, err := aiChatVOpts(context.Background(), cfg, msgs, nil, nil, opts)
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
		slog.Info("ai retry with enable_thinking=true", "model", cfg.Model, "budget", retry.ThinkingBudget, "err", err.Error())
		text, _, err = aiChatVOpts(context.Background(), cfg, msgs, nil, nil, retry)
	}
	return text, err
}

// ---- 上下文压缩：长对话历史 → 「AI 摘要 + 最近若干轮 verbatim」，替代硬截断 ----
//
// 硬截断（只保留最近 N 轮）会丢掉早期关键上下文（如最初的现象、已确认结论、约束）。这里改为：
// 超预算时把较旧轮次交给廉价模型摘成要点，保留最近轮次原文；摘要可增量缓存（见 SreyunSession），
// 只对「新变旧」的那段做增量摘要，避免每轮全量重算。

// summaryMsg 把摘要包装成一条注入消息。
func summaryMsg(summary string) map[string]string {
	return map[string]string{"role": "user", "content": "【历史对话摘要】\n" + summary + "\n（以上为早期对话的要点摘要，请据此保持连贯）"}
}

// summarizeTurns 把一段旧对话（可基于已有摘要增量）压成要点摘要。失败则回退旧摘要。
func summarizeTurns(cfg AIConfig, priorSummary string, msgs []map[string]string) string {
	var b strings.Builder
	if priorSummary != "" {
		b.WriteString("已有摘要：\n" + priorSummary + "\n\n需要并入的新增对话：\n")
	}
	for _, m := range msgs {
		if c := strings.TrimSpace(m["content"]); c != "" {
			b.WriteString(m["role"] + "：" + trimLine(c, 500) + "\n")
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return priorSummary
	}
	sys := "你是对话摘要器。把下面的运维对话压缩成简洁要点摘要，保留关键事实、已确认结论、约束、" +
		"待办与决定，丢弃寒暄与冗余；若给了已有摘要，则在其基础上合并更新。只输出摘要正文，控制在 400 字内。"
	out, err := aiComplete(cfg, sys, b.String())
	if err != nil || strings.TrimSpace(out) == "" {
		return priorSummary
	}
	return strings.TrimSpace(out)
}

// compactHistory 是【无 LLM】的历史压缩：保留最早一轮（通常含最初现象/约束）+ 最近 keepRecentTurns
// 轮，省略中间轮次。用于无会话缓存的入口（/ai/chat、diagnose-chat）——那里 compressHistory 会因
// 每次都从 priorCount=0 起算而【每轮】同步做一次整段 LLM 摘要（成本/首字延迟翻倍）。带会话缓存的
// Sreyun 主对话仍用 compressHistory 做增量 AI 摘要（只对新变旧的段落摘要一次，成本低）。
func compactHistory(history []map[string]string, keepRecentTurns int) []map[string]string {
	if keepRecentTurns < 2 {
		keepRecentTurns = 2
	}
	keep := keepRecentTurns * 2 // 每轮 user+assistant = 2 条
	if len(history) <= keep+2 {
		return history
	}
	out := make([]map[string]string, 0, keep+3)
	out = append(out, history[:2]...) // 最早一轮
	out = append(out, map[string]string{"role": "user", "content": "（为控制长度，中间若干轮对话已省略）"})
	out = append(out, history[len(history)-keep:]...)
	return out
}

// compressHistory 返回压缩后的历史消息列表 + 新摘要 + 新的「已被摘要覆盖的消息数」。
// history 为 user/assistant 交替的全量历史；keepRecentTurns 为保留原文的最近轮数。
// priorSummary/priorCount 为已缓存的摘要与其覆盖的消息数（无缓存传 "" / 0，即无状态调用）。
func compressHistory(cfg AIConfig, history []map[string]string, keepRecentTurns int, priorSummary string, priorCount int) ([]map[string]string, string, int) {
	if keepRecentTurns < 2 {
		keepRecentTurns = 2
	}
	keep := keepRecentTurns * 2 // 每轮 user+assistant = 2 条
	if len(history) <= keep {
		// 历史不长，无需压缩；若此前已有摘要（更早的历史被删过）则保留在最前
		if priorSummary != "" {
			return append([]map[string]string{summaryMsg(priorSummary)}, history...), priorSummary, priorCount
		}
		return history, priorSummary, priorCount
	}
	cutoff := len(history) - keep
	newSummary, newCount := priorSummary, priorCount
	if cutoff > priorCount { // 有「新变旧」的段落需要增量并入摘要
		newSummary = summarizeTurns(cfg, priorSummary, history[priorCount:cutoff])
		newCount = cutoff
	}
	recent := history[cutoff:]
	out := make([]map[string]string, 0, len(recent)+1)
	if newSummary != "" {
		out = append(out, summaryMsg(newSummary))
	}
	out = append(out, recent...)
	return out, newSummary, newCount
}

// openAIStreamUsageOptions asks OpenAI-compatible gateways to emit a final
// usage chunk on SSE streams. Without this, most providers omit usage and the
// exact-token ledger stays empty on the dominant streaming path.
func openAIStreamUsageOptions() map[string]any {
	return map[string]any{"include_usage": true}
}

// streamChat calls the AI provider with streaming enabled (SSE) and writes each
// streamChat streams an AI chat response via SSE. When images are non-nil,
// the last user message is converted to multimodal content format so vision
// models can analyze uploaded screenshots.
// For providers that do not support streaming (Anthropic), it falls back to
// aiChat and sends the whole reply as a single SSE event.
// The caller must set the proper headers (Content-Type: text/event-stream,
// Cache-Control: no-cache, Connection: keep-alive) before calling this function.
// ctx 用于客户端断开时取消到 LLM provider 的在途请求，防止资源泄漏。
func streamChatInner(ctx context.Context, w http.ResponseWriter, cfg AIConfig, messages []map[string]string, images []chatImage, sendDone bool) (string, error) {
	return streamChatInnerOpts(ctx, w, cfg, messages, images, sendDone, aiCallOpts{})
}

func streamChatInnerOpts(ctx context.Context, w http.ResponseWriter, cfg AIConfig, messages []map[string]string, images []chatImage, sendDone bool, opts aiCallOpts) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var slotID uint64
	var owned bool
	ctx, slotID, owned = bindAIUsageSlot(ctx)
	if owned {
		defer endAIUsageSlot(slotID)
	}
	if cfg.Endpoint == "" || cfg.Model == "" {
		fmt.Fprintf(w, "data: {\"error\":\"AI 未配置\"}\n\n")
		return "", nil
	}
	if opts.DisableThinking && !opts.EnableThinking {
		messages = withNoThinkHint(messages, cfg)
	}

	ep, prov := normalizeEndpoint(cfg.Endpoint)

	// Anthropic-compatible endpoints don't support SSE streaming in the same way;
	// fall back to a single-chunk response.
	if prov == aiProvAnthropic {
		reply, _, err := aiChatVOpts(ctx, cfg, messages, nil, nil, opts) // 透传 ctx：客户端断开时可取消到 Anthropic 的在途请求
		if err != nil {
			fmt.Fprintf(w, "data: {\"error\":%s}\n\n", jsonString(err.Error()))
			return "", nil
		}
		fmt.Fprintf(w, "data: {\"delta\":%s}\n\n", jsonString(reply))
		if sendDone {
			fmt.Fprintf(w, "data: [DONE]\n\n")
		}
		return reply, nil
	}

	// 流式仅走 OpenAI 兼容（Anthropic 已在上方非流式处理并返回）。
	// 当携带图片时，使用 buildRequestMessages 将最后一条 user 消息转为多模态 content 数组
	var reqMessages any = messages
	if len(images) > 0 {
		reqMessages = buildRequestMessages(messages, images, prov)
	}
	reqBody := map[string]any{
		"model":          cfg.Model,
		"messages":       reqMessages,
		"temperature":    0.2,
		"stream":         true,
		"stream_options": openAIStreamUsageOptions(),
	}
	applyThinkingKnobs(reqBody, cfg, prov, opts)
	applyOutputTokenLimit(reqBody, cfg, prov, opts)

	b, _ := json.Marshal(reqBody)
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, ep, bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":%s}\n\n", jsonString(err.Error()))
		return "", nil
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := newGuardedHTTPClient(timeout + 5*time.Second) // SSRF：用户可配 AI Endpoint，拦元数据/链路本地
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(w, "data: {\"error\":%s}\n\n", jsonString(err.Error()))
		return "", nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
		fmt.Fprintf(w, "data: {\"error\":%s}\n\n", jsonString(providerHTTPErrorMsg(resp.StatusCode, string(body), cfg)))
		return "", nil
	}

	// Parse SSE stream line by line, accumulating the full reply
	var fullReply strings.Builder
	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 4*1024), 1024*1024)
	for scanner.Scan() {
		if reqCtx.Err() != nil { // 客户端已断开或超时：中止读取 LLM 流
			return fullReply.String(), reqCtx.Err()
		}
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			if sendDone {
				fmt.Fprintf(w, "data: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			}
			return fullReply.String(), nil
		}
		// 分别提取正文增量与思维链增量：思维链走独立 {"reasoning":...} 帧，前端收进折叠区。
		delta, reasoning := parseStreamDelta(data, prov)
		// 捕获 SSE 携带的 provider usage（若网关下发了 usage 字段），写入当前 ctx 槽位。
		captureAIUsageFromJSONCtx(ctx, []byte(data))
		if reasoning != "" {
			fmt.Fprintf(w, "data: {\"reasoning\":%s}\n\n", jsonString(reasoning))
			if flusher != nil {
				flusher.Flush()
			}
		}
		if delta == "" {
			continue
		}
		fullReply.WriteString(delta)
		fmt.Fprintf(w, "data: {\"delta\":%s}\n\n", jsonString(delta))
		if flusher != nil {
			flusher.Flush()
		}
	}
	// If scanner ended without [DONE], send a done marker
	if sendDone {
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
	return fullReply.String(), nil
}

// streamChat 保持原签名（自动发送结尾 [DONE]），既有调用方无需改动。
func streamChat(ctx context.Context, w http.ResponseWriter, cfg AIConfig, messages []map[string]string, images []chatImage) (string, error) {
	return streamChatInner(ctx, w, cfg, messages, images, true)
}

// streamChatOpts is streamChat with per-call overrides (disable thinking for dashboard JSON tasks).
func streamChatOpts(ctx context.Context, w http.ResponseWriter, cfg AIConfig, messages []map[string]string, images []chatImage, opts aiCallOpts) (string, error) {
	return streamChatInnerOpts(ctx, w, cfg, messages, images, true, opts)
}

// streamChatNoDone 与 streamChat 相同但【不发】结尾 [DONE]——用于把多段流式内容（诊断正文 +
// 自我校验 / MoA 聚合）拼成一条 SSE 响应，最后由调用方统一发一次 [DONE]。
func streamChatNoDone(ctx context.Context, w http.ResponseWriter, cfg AIConfig, messages []map[string]string, images []chatImage) (string, error) {
	return streamChatInner(ctx, w, cfg, messages, images, false)
}

// streamSelfVerify 是「自我校验」的独立第二遍：对照 evidence 复核 answer，把审校意见【流式续写】
// 到 w（不发 [DONE]，由调用方统一收尾），返回审校全文。借鉴 Sreyun Agent 的 background review。
func streamSelfVerify(ctx context.Context, w http.ResponseWriter, cfg AIConfig, evidence, answer string) string {
	if flusher, ok := w.(http.Flusher); ok {
		fmt.Fprintf(w, "data: {\"delta\":%s}\n\n", jsonString("\n\n---\n🔎 **自我校验**（对照证据独立复核）\n\n"))
		flusher.Flush()
	}
	sysV := "你是严谨的运维审校员。请对照【证据】逐条核对下面的【诊断结论】：指出哪些判断有证据支撑、" +
		"哪些缺乏证据或属过度推断、有无自相矛盾；如有问题请直接修正，并给出复核后的可信度（高/中/低）" +
		"与仍需补充的信息。简洁分点，只依据给定证据，不臆造。\n\n【证据】\n" + trimLine(evidence, 4000)
	out, _ := streamChatNoDone(ctx, w, cfg, []map[string]string{
		{"role": "system", "content": sysV},
		{"role": "user", "content": "【诊断结论】\n" + answer},
	}, nil)
	return out
}

// streamDeltaChunk 是 SSE 流式响应的 JSON 解析目标类型。
// 提取为包级类型以便复用，编译器可优化栈分配，减少 GC 压力（P2-5 优化）。
// reasoning_content / reasoning：推理模型（DeepSeek-R1 / QwQ / Qwen3-thinking 等）把思维链
// 放在独立字段，正文答案仍在 content——分开解析后前端可把 CoT 收进「思考过程」折叠区，
// 既消除首字前的长时间静默，又不与最终答案混排。
type streamDeltaChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Output struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	} `json:"output"`
	// Usage 由部分 OpenAI 兼容网关在 SSE 中携带（如 DeepSeek / 通义 / Qwen 等）。
	// 捕获它以便写入精确 token 账本，避免纯字符粗估。
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// parseStreamDelta 从单个 SSE chunk 提取「正文增量」与「思维链增量」。二者可同时为空，
// 也可各自非空（推理阶段只有 reasoning，作答阶段只有 content）。
func parseStreamDelta(data string, prov aiProviderType) (content, reasoning string) {
	var chunk streamDeltaChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return "", ""
	}
	if len(chunk.Choices) > 0 {
		d := chunk.Choices[0].Delta
		content = d.Content
		reasoning = d.ReasoningContent
		if reasoning == "" {
			reasoning = d.Reasoning
		}
		// 少数实现把内容放在 message.content（非 delta）——仅当 delta 为空时兜底
		if content == "" && chunk.Choices[0].Message.Content != "" {
			content = chunk.Choices[0].Message.Content
		}
		if content != "" || reasoning != "" {
			return content, reasoning
		}
	}
	// Bailian native format: output.choices[0].message.content
	if len(chunk.Output.Choices) > 0 && chunk.Output.Choices[0].Message.Content != "" {
		return chunk.Output.Choices[0].Message.Content, ""
	}
	return "", ""
}

// jsonString marshals a string as a JSON string (with quotes).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// streamChatFiltered wraps streamChat with sensitive content filtering on the
// accumulated reply. The streaming deltas are sent unfiltered (the frontend
// applies its own display filter), but the returned full text is filtered.
// 注意：用 streamChatNoDone（不发 [DONE]），以便调用方（AI 连通性测试）在正文之后还能再发一帧
// {"result":...}（延迟/Provider 等元数据），最后由调用方统一发一次 [DONE]。若这里发 [DONE]，
// 前端 readSSEStream 命中即 return，后续 result 帧会被丢弃。
func streamChatFiltered(ctx context.Context, w http.ResponseWriter, cfg AIConfig, messages []map[string]string) (string, error) {
	reply, err := streamChatNoDone(ctx, w, cfg, messages, nil)
	if err != nil {
		return "", err
	}
	return reply, nil
}

// filterRegex 预编译正则，避免每次调用重复编译（P2-1 优化）。
var (
	reCodeFenceJSON = regexp.MustCompile("(?s)```[a-z]*\\s*\\{.*?```")
	reCodeFenceAny  = regexp.MustCompile("(?s)```[^`]*```")
	reToolCallJSON  = regexp.MustCompile("(?s)\\{\\s*\"tool_calls\".*?\\}\\s*$")
	reAPIKey        = regexp.MustCompile("\\b(sk-[a-zA-Z0-9_-]{20,})\\b")
	reSecretKV      = regexp.MustCompile("\\b(api_key|apikey|secret|password|token)\\s*[:=]\\s*['\"]?[^\\s'\"]+['\"]?")
)

// filterSensitiveContent strips JSON blocks, code fences, and sensitive patterns
// from AI-generated text. Used by the frontend; also available for backend use.
func filterSensitiveContent(text string) string {
	text = reCodeFenceJSON.ReplaceAllString(text, "[已过滤代码块]")
	text = reCodeFenceAny.ReplaceAllString(text, "[已过滤代码块]")
	text = reToolCallJSON.ReplaceAllString(text, "")
	text = reAPIKey.ReplaceAllString(text, "[已隐藏密钥]")
	text = reSecretKV.ReplaceAllString(text, "$1=[已隐藏]")
	return strings.TrimSpace(text)
}

// InspectionFinding is one item on an inspection report.
type InspectionFinding struct {
	Severity string `json:"severity"` // critical|warning|info
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
}

// InspectionReport is one automated (or on-demand) health inspection.
type InspectionReport struct {
	ID         int64               `json:"id"`
	Ts         int64               `json:"ts"`
	Trigger    string              `json:"trigger"`               // scheduled|manual
	Source     string              `json:"source"`                // ai|heuristic
	Model      string              `json:"model,omitempty"`       // AI model used, or 启发式规则
	Context    string              `json:"context,omitempty"`     // human-readable "what was inspected"
	DurationMs int64               `json:"duration_ms,omitempty"` // how long this round took
	Summary    string              `json:"summary"`
	Findings   []InspectionFinding `json:"findings"`
}

// inspectionContext is the snapshot the AI/heuristic engine reasons over.
type inspectionContext struct {
	OnlineHosts   int
	OfflineHosts  []string
	FiringAlerts  []Alert
	BreachingSLOs []SLOStatus
	RecentErrors  []StoredLog
	ErrorCount    int
	WarnCount     int
	HighUsage     []string
}

const inspectionReportCap = 60

type aiManager struct {
	mu      sync.Mutex
	cfg     *ConfigStore
	reports []InspectionReport
	nextID  int64
	// injected data sources (set during wiring)
	snapshot    func() inspectionContext
	diagContext func(inc Incident) string
	onReport    func(rep InspectionReport) // notify hook: surface findings as messages
}

func newAIManager(cfg *ConfigStore) *aiManager { return &aiManager{cfg: cfg, nextID: 1} }

// heuristicInspect turns a snapshot into a summary + structured findings without
// any LLM — the reliable baseline the AI narrative sits on top of.
func heuristicInspect(ctx inspectionContext) (string, []InspectionFinding) {
	var f []InspectionFinding
	for _, hn := range ctx.OfflineHosts {
		f = append(f, InspectionFinding{"critical", "主机离线：" + hn, "该主机已失联，请检查网络连通与 Agent 进程。"})
	}
	for _, a := range ctx.FiringAlerts {
		sev := "warning"
		if a.Level == "critical" {
			sev = "critical"
		}
		f = append(f, InspectionFinding{sev, a.Hostname + " · " + a.Message, ""})
	}
	for _, s := range ctx.BreachingSLOs {
		f = append(f, InspectionFinding{"warning", "SLO 未达标：" + s.Name,
			fmt.Sprintf("SLI %.2f%% < 目标 %.2f%%，错误预算剩余 %.0f%%。", s.SLI, s.Target, s.ErrorBudget)})
	}
	for _, hu := range ctx.HighUsage {
		f = append(f, InspectionFinding{"warning", "资源高位：" + hu, ""})
	}
	if ctx.ErrorCount > 0 || ctx.WarnCount > 0 {
		sev := "info"
		if ctx.ErrorCount >= 50 {
			sev = "critical"
		} else if ctx.ErrorCount >= 10 {
			sev = "warning"
		}
		f = append(f, InspectionFinding{sev,
			fmt.Sprintf("近 30 分钟日志：error %d 条 · warn %d 条", ctx.ErrorCount, ctx.WarnCount),
			"可在「日志检索」按级别 + 主机定位错误起始时间与来源服务。"})
	}
	summary := fmt.Sprintf("在线 %d 台 · 离线 %d 台 · firing 告警 %d 条 · SLO 超标 %d 项 · 近 30 分钟 error %d/warn %d 条。",
		ctx.OnlineHosts, len(ctx.OfflineHosts), len(ctx.FiringAlerts), len(ctx.BreachingSLOs), ctx.ErrorCount, ctx.WarnCount)
	if len(f) == 0 {
		summary = "系统健康：本轮巡检未发现异常。"
	}
	return summary, f
}

// buildInspectionPrompt renders the snapshot as text for the LLM.
func buildInspectionPrompt(ctx inspectionContext) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("在线主机 %d 台。\n", ctx.OnlineHosts))
	if len(ctx.OfflineHosts) > 0 {
		b.WriteString("离线主机：" + strings.Join(ctx.OfflineHosts, "、") + "\n")
	}
	if len(ctx.FiringAlerts) > 0 {
		b.WriteString("正在触发的告警：\n")
		for _, a := range ctx.FiringAlerts {
			b.WriteString(fmt.Sprintf("  - [%s] %s · %s\n", a.Level, a.Hostname, a.Message))
		}
	}
	if len(ctx.BreachingSLOs) > 0 {
		b.WriteString("未达标 SLO：\n")
		for _, s := range ctx.BreachingSLOs {
			b.WriteString(fmt.Sprintf("  - %s: SLI %.2f%% / 目标 %.2f%%\n", s.Name, s.SLI, s.Target))
		}
	}
	if len(ctx.HighUsage) > 0 {
		b.WriteString("资源高位：" + strings.Join(ctx.HighUsage, "、") + "\n")
	}
	if len(ctx.RecentErrors) > 0 {
		b.WriteString(fmt.Sprintf("近期错误日志（%d 条，节选）：\n", ctx.ErrorCount))
		for i, e := range ctx.RecentErrors {
			if i >= 15 {
				break
			}
			b.WriteString("  - " + e.Hostname + ": " + trimLine(e.Message, 160) + "\n")
		}
	}
	if ctx.WarnCount > 0 {
		b.WriteString(fmt.Sprintf("近 30 分钟告警(warn)级日志 %d 条（可作为错误的前兆信号）。\n", ctx.WarnCount))
	}
	if b.Len() == 0 {
		b.WriteString("无异常指标。")
	}
	return b.String()
}

func trimLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		// s[:n] 按字节截断可能切断多字节 UTF-8 字符（如中文 3 字节），
		// 产生无效字节序列导致 PostgreSQL 报 22021。用 ToValidUTF8 清洗。
		return strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}

// RunInspection performs one inspection (AI-enhanced when configured, heuristic
// otherwise) and stores the report.
func (m *aiManager) RunInspection(trigger string) InspectionReport {
	start := time.Now()
	ctx := inspectionContext{}
	if m.snapshot != nil {
		ctx = m.snapshot()
	}
	// Human-readable description of exactly what this round looked at — surfaced in
	// the report so operators can see the AI/heuristic actually ran over real data.
	inspectCtx := fmt.Sprintf("巡检范围：在线主机 %d 台 · 离线 %d 台 · firing 告警 %d 条 · SLO %d 项 · 近 30 分钟 error %d/warn %d 条 · 资源高位 %d 项。",
		ctx.OnlineHosts, len(ctx.OfflineHosts), len(ctx.FiringAlerts), len(ctx.BreachingSLOs), ctx.ErrorCount, ctx.WarnCount, len(ctx.HighUsage))
	summary, findings := heuristicInspect(ctx)
	source, model := "heuristic", "启发式规则"
	if cfg := m.cfg.AIConfig(); cfg.Enabled {
		sys := "你是资深 SRE 专家。根据以下系统巡检快照，用简洁中文给出整体健康研判、风险优先级与处置建议，控制在 200 字内。"
		if out, err := aiComplete(cfg, sys, buildInspectionPrompt(ctx)); err == nil && out != "" {
			summary = out
			source = "ai"
			model = cfg.Model
		}
	}
	rep := InspectionReport{
		Trigger: trigger, Source: source, Model: model, Context: inspectCtx,
		DurationMs: time.Since(start).Milliseconds(),
		Summary:    summary, Findings: findings, Ts: time.Now().Unix(),
	}
	m.mu.Lock()
	m.nextID++
	rep.ID = m.nextID
	m.reports = append(m.reports, rep)
	if len(m.reports) > inspectionReportCap {
		m.reports = m.reports[len(m.reports)-inspectionReportCap:]
	}
	m.mu.Unlock()
	if m.onReport != nil {
		m.onReport(rep)
	}
	return rep
}

// Reports returns inspection history newest-first.
func (m *aiManager) Reports() []InspectionReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]InspectionReport, len(m.reports))
	copy(out, m.reports)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// exportReports returns inspection history in chronological (storage) order for
// PG persistence.
func (m *aiManager) exportReports() []InspectionReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]InspectionReport, len(m.reports))
	copy(out, m.reports)
	return out
}

// importReports restores inspection history from PG on startup, resuming the ID
// sequence from the highest persisted ID so new reports never collide.
func (m *aiManager) importReports(reps []InspectionReport) {
	if len(reps) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(reps) > inspectionReportCap {
		reps = reps[len(reps)-inspectionReportCap:]
	}
	m.reports = reps
	for _, r := range reps {
		if r.ID > m.nextID {
			m.nextID = r.ID
		}
	}
}

// runInspectionLoop is the scheduled inspector.
func (m *aiManager) runInspectionLoop() {
	for {
		iv := 30
		if c := m.cfg.AIConfig(); c.InspectIntervalMin > 0 {
			iv = c.InspectIntervalMin
		}
		time.Sleep(time.Duration(iv) * time.Minute)
		m.RunInspection("scheduled")
	}
}

// diagHint returns a rule-of-thumb remediation direction per alert type.
func diagHint(typ string) string {
	m := map[string]string{
		"cpu":     "定位高 CPU 进程（top / pidstat）；排查失控进程或流量突增；必要时限流或扩容。",
		"memory":  "查看内存占用 TOP（free / ps aux --sort=-%mem）；排查内存泄漏或缓存膨胀；必要时重启相关服务或扩容。",
		"disk":    "定位大文件/目录（du -sh *）；清理日志与临时文件；排查写入是否激增；必要时扩容。",
		"diskio":  "定位高 IO 进程（iotop）；排查慢查询或大批量写入；考虑限速或迁移。",
		"iops":    "排查高频小 IO 来源（数据库/日志刷盘）；评估合并写入或换用更高 IOPS 存储。",
		"load":    "结合 CPU/IO/进程数判断瓶颈；定位 D 状态阻塞进程；排查下游依赖是否卡顿。",
		"offline": "检查主机网络连通与 Agent 进程是否存活；确认是否宕机或正在重启。",
		"gpu":     "查看 GPU 占用与温度（nvidia-smi）；排查失控训练/推理任务。",
		"proc":    "对比进程基线，定位异常拉起或退出的服务，检查是否 OOM/崩溃重启。",
	}
	if v, ok := m[typ]; ok {
		return v
	}
	return "结合指标趋势与错误日志定位异常起始时间，缩小到具体服务/进程后再处置。"
}

// heuristicDiagnose produces a rule-based diagnosis when no AI is configured.
func heuristicDiagnose(inc Incident, ctx string) string {
	var b strings.Builder
	b.WriteString("【启发式诊断 · 基于规则】\n\n根因方向：\n")
	b.WriteString(diagHint(inc.Type) + "\n")
	if ctx != "" {
		b.WriteString("\n采集到的上下文：\n" + ctx)
	}
	b.WriteString("\n提示：在「AI 巡检」页配置 AI Provider 后，可获得智能体级别的根因研判与处置编排。")
	return b.String()
}

// Diagnose returns a diagnosis for an incident (AI when configured, heuristic
// otherwise). The caller appends the result to the incident timeline.
func (m *aiManager) Diagnose(inc Incident) (string, string) {
	ctx := ""
	if m.diagContext != nil {
		ctx = m.diagContext(inc)
	}
	if cfg := m.cfg.AIConfig(); cfg.Enabled {
		sys := "你是资深 SRE 值班工程师。根据事件与主机上下文，给出：1) 按可能性排序的根因假设；2) 具体可执行的处置步骤。简洁中文，分点。"
		if out, err := aiComplete(cfg, sys, ctx); err == nil && out != "" {
			return out, "ai"
		}
	}
	return heuristicDiagnose(inc, ctx), "heuristic"
}

// embedDim 是向量存储列（pgvector）的固定维度。嵌入模型必须输出该维度，否则入库失败。
const embedDim = 1536

// embedCache 缓存嵌入向量，避免对相同文本重复调用 embedText API。
// 容量有限，TTL 30 秒，适合对话场景下同一用户的连续请求复用。
type embedCacheEntry struct {
	vec    []float64
	expiry time.Time
}

var (
	embCacheMu  sync.Mutex
	embCache    = make(map[string]embedCacheEntry)
	embCacheTTL = 30 * time.Second
	embCacheCap = 50
)

func embedCacheGet(text string) []float64 {
	embCacheMu.Lock()
	defer embCacheMu.Unlock()
	if e, ok := embCache[text]; ok && time.Now().Before(e.expiry) {
		return e.vec
	}
	return nil
}

func embedCacheSet(text string, vec []float64) {
	embCacheMu.Lock()
	defer embCacheMu.Unlock()
	if len(embCache) >= embCacheCap {
		now := time.Now()
		for k, e := range embCache {
			if now.After(e.expiry) {
				delete(embCache, k)
			}
		}
		if len(embCache) >= embCacheCap {
			// 仍满则随机淘汰一半
			i := 0
			for k := range embCache {
				delete(embCache, k)
				i++
				if i >= embCacheCap/2 {
					break
				}
			}
		}
	}
	embCache[text] = embedCacheEntry{vec: vec, expiry: time.Now().Add(embCacheTTL)}
}

// embedText 把文本转成向量。与对话模型解耦：默认走「OpenAI 兼容 /embeddings」，兼容
// OpenAI / 本地(Ollama/LocalAI/Xinference) / 百炼兼容模式 等任意服务；仅当用户未配置嵌入且
// 主端点为百炼原生时，沿用旧的百炼 DashScope 原生 text-embedding-v2（向后兼容）。
// 任何错误返回 nil，调用方优雅降级（不阻塞主流程）。
func embedText(cfg AIConfig, text string) []float64 {
	if !cfg.Enabled {
		return nil
	}
	if text = strings.TrimSpace(text); text == "" {
		return nil
	}
	if len([]rune(text)) > 8000 { // 控制输入长度，避免超模型上限（~8000字符 ≈ 4000 token）
		text = string([]rune(text)[:8000])
	}
	// 嵌入缓存命中检查
	if cached := embedCacheGet(text); cached != nil {
		return cached
	}
	ep := strings.TrimSpace(cfg.EmbedEndpoint)
	key := strings.TrimSpace(cfg.EmbedAPIKey)
	if key == "" {
		key = cfg.APIKey
	}
	model := strings.TrimSpace(cfg.EmbedModel)
	if key == "" {
		return nil
	}
	var emb []float64
	// 未配置自定义嵌入 + 主端点是百炼原生 → 沿用旧的百炼原生 v2（向后兼容既有用户）
	if ep == "" && model == "" && isBailianEndpoint(cfg.Endpoint) {
		emb = embedBailianNative(key, text)
	} else {
		if ep == "" {
			ep = cfg.Endpoint // 复用主端点 base
		}
		if model == "" {
			return nil // 通用模式必须指定嵌入模型
		}
		emb = embedOpenAICompat(ep, key, model, cfg.EmbedDimensions, text)
	}
	if emb != nil {
		embedCacheSet(text, emb)
	}
	return emb
}

// rerankConfig 解析 rerank 端点/密钥/模型；未配置 RerankModel 即视为未启用（ok=false）。
// Endpoint / Key 依次回退：rerank 专用 → 嵌入 → 主配置。
func rerankConfig(cfg AIConfig) (ep, key, model string, ok bool) {
	model = strings.TrimSpace(cfg.RerankModel)
	if model == "" {
		return "", "", "", false
	}
	ep = strings.TrimSpace(cfg.RerankEndpoint)
	if ep == "" {
		ep = strings.TrimSpace(cfg.EmbedEndpoint)
	}
	if ep == "" {
		ep = strings.TrimSpace(cfg.Endpoint)
	}
	key = strings.TrimSpace(cfg.RerankAPIKey)
	if key == "" {
		key = strings.TrimSpace(cfg.EmbedAPIKey)
	}
	if key == "" {
		key = strings.TrimSpace(cfg.APIKey)
	}
	if ep == "" || key == "" {
		return "", "", "", false
	}
	return ep, key, model, true
}

// rerankEndpointURL 把 base / 对话 / 嵌入端点规整为 OpenAI 风格的 /rerank 完整地址。
func rerankEndpointURL(ep string) string {
	ep = strings.TrimRight(strings.TrimSpace(ep), "/")
	switch {
	case strings.HasSuffix(ep, "/rerank"):
		return ep
	case strings.HasSuffix(ep, "/chat/completions"):
		return strings.TrimSuffix(ep, "/chat/completions") + "/rerank"
	case strings.HasSuffix(ep, "/embeddings"):
		return strings.TrimSuffix(ep, "/embeddings") + "/rerank"
	default:
		return ep + "/rerank"
	}
}

// rerankDocuments 调用 OpenAI 风格 /rerank 接口，对候选文档按与 query 的相关性重排，返回
// 按相关性降序的原始下标（已裁剪到 topN）。未配置 / 出错 / 空结果时返回 nil，调用方回退到
// 原向量顺序——rerank 是纯增强，绝不因其失败而降低可用性。
func rerankDocuments(cfg AIConfig, query string, docs []string, topN int) []int {
	ep, key, model, ok := rerankConfig(cfg)
	if !ok || strings.TrimSpace(query) == "" || len(docs) == 0 {
		return nil
	}
	reqBody := map[string]any{"model": model, "query": query, "documents": docs}
	if topN > 0 {
		reqBody["top_n"] = topN
	}
	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequest(http.MethodPost, rerankEndpointURL(ep), bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := newGuardedHTTPClient(20 * time.Second).Do(req) // SSRF：rerank 端点用户可配，拦元数据/链路本地
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	// 兼容 Jina/Cohere/SiliconFlow：{"results":[{"index":int,"relevance_score":float},...]}（已按分降序）
	var out struct {
		Results []struct {
			Index int `json:"index"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Results) == 0 {
		return nil
	}
	idx := make([]int, 0, len(out.Results))
	seen := make(map[int]bool, len(out.Results))
	for _, r := range out.Results {
		if r.Index >= 0 && r.Index < len(docs) && !seen[r.Index] {
			idx = append(idx, r.Index)
			seen[r.Index] = true
		}
	}
	return idx
}

// embedEndpointURL 把用户填的 base / 对话端点规整为 OpenAI 兼容的 /embeddings 完整地址。
func embedEndpointURL(ep string) string {
	ep = strings.TrimRight(strings.TrimSpace(ep), "/")
	if strings.HasSuffix(ep, "/embeddings") {
		return ep
	}
	if strings.HasSuffix(ep, "/chat/completions") { // 用户填了对话端点：替换尾段
		return strings.TrimSuffix(ep, "/chat/completions") + "/embeddings"
	}
	return ep + "/embeddings"
}

// embedOpenAICompat 调用 OpenAI 兼容的 /embeddings 接口。dim>0 时请求指定维度
// （OpenAI text-embedding-3-* 支持；其它服务忽略并返回原生维度）。
func embedOpenAICompat(ep, key, model string, dim int, text string) []float64 {
	reqBody := map[string]any{"model": model, "input": text}
	if dim > 0 {
		reqBody["dimensions"] = dim
	}
	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequest(http.MethodPost, embedEndpointURL(ep), bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := newGuardedHTTPClient(30 * time.Second).Do(req) // SSRF：嵌入端点用户可配，拦元数据/链路本地
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("嵌入接口返回非 200（检查嵌入 Endpoint / Key / 模型）", "status", resp.StatusCode, "model", model)
		return nil
	}
	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Data) == 0 {
		return nil
	}
	emb := result.Data[0].Embedding
	if len(emb) != embedDim { // 维度须与 pgvector 列一致，否则入库失败——告警并跳过
		slog.Warn("嵌入维度与存储列不一致，已跳过（请选输出 1536 维的模型，或用支持 dimensions 的模型）",
			"got", len(emb), "want", embedDim, "model", model)
		return nil
	}
	return emb
}

// embedBailianNative 是旧的百炼 DashScope 原生 text-embedding-v2 路径（向后兼容既有配置）。
func embedBailianNative(key, text string) []float64 {
	reqBody := map[string]any{
		"model":      "text-embedding-v2",
		"input":      map[string]any{"texts": []string{text}},
		"parameters": map[string]string{"text_type": "query"},
	}
	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequest(http.MethodPost,
		"https://dashscope.aliyuncs.com/api/v1/services/embeddings/text-embedding/text-embedding",
		bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := newGuardedHTTPClient(30 * time.Second).Do(req) // SSRF：百炼固定端点仍守卫
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var result struct {
		Output struct {
			Embeddings []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"embeddings"`
		} `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Output.Embeddings) == 0 {
		return nil
	}
	return result.Output.Embeddings[0].Embedding
}
