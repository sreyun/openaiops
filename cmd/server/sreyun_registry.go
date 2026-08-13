package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// ============================================================================
// Sreyun 工具注册表 —— 写时复制快照
//
// SreyunCore 是进程级单例（handlers.go 里 s.sreyun = newSreyunCore(s)），被所有
// AI 会话、MCP 服务端、诊断/巡检旁路并发共享。而外部 MCP Client 的桥接工具会在
// 运行时被重新注册：handleSetAIConfig / handleSyncMCPClient 保存配置时调用
// reloadExternalMCPTools，对同一张 map 做 delete + 赋值。
//
// 裸 map 上「一边写、一边有会话在 h.tools[name] 查表 / range 出工具定义」是 Go
// 运行时会直接 fatal 掉整个进程的 concurrent map read and map write —— 不是数据
// 错乱，是服务端当场退出。
//
// 因此：h.tools 只作为「写入暂存区」，任何变更后调用 publishTools() 生成一份不可
// 变快照原子发布；所有读路径只看快照，零锁、零竞争。快照同时预算好排序后的工具名、
// 文本注入提示词与原生 Function Calling 定义——这些原本各自散落的缓存字段有同样的
// 竞争问题，一并收敛到快照里，也省掉了每轮重建。
// ============================================================================

// toolSnapshot is an immutable view of the tool registry.
type toolSnapshot struct {
	byName map[string]SreyunTool
	names  []string
	prompt string           // 文本注入模式的工具说明（Anthropic / 回退路径）
	native []map[string]any // 原生 Function Calling 工具定义
}

// publishTools rebuilds and atomically publishes the snapshot.
// Callers must hold toolsMu (or be in single-threaded construction).
func (h *SreyunCore) publishTools() {
	names := make([]string, 0, len(h.tools))
	for name := range h.tools {
		names = append(names, name)
	}
	// 排序保证注入顺序稳定，利于 Provider 侧 prompt 缓存命中与结果可复现。
	sort.Strings(names)

	byName := make(map[string]SreyunTool, len(h.tools))
	native := make([]map[string]any, 0, len(names))
	for _, name := range names {
		t := h.tools[name]
		byName[name] = t
		native = append(native, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	toolsJSON, _ := json.Marshal(native)
	prompt := "\n\n你可以使用以下工具来获取信息或执行操作。当需要调用工具时，请用以下 JSON 格式回复：\n" +
		"```json\n{\"tool_calls\":[{\"name\":\"工具名\",\"args\":{参数}}]}\n```\n\n可用工具定义：\n" + string(toolsJSON)

	h.published.Store(&toolSnapshot{byName: byName, names: names, prompt: prompt, native: native})
}

// snapshot returns the current published registry, never nil.
func (h *SreyunCore) snapshot() *toolSnapshot {
	if snap := h.published.Load(); snap != nil {
		return snap
	}
	return &toolSnapshot{byName: map[string]SreyunTool{}}
}

// lookupTool resolves a tool by name from the published snapshot.
func (h *SreyunCore) lookupTool(name string) (SreyunTool, bool) {
	t, ok := h.snapshot().byName[name]
	return t, ok
}

// toolNames returns the sorted tool names. The slice is shared and read-only.
func (h *SreyunCore) toolNames() []string { return h.snapshot().names }

// toolCount reports how many tools are currently registered.
func (h *SreyunCore) toolCount() int { return len(h.snapshot().byName) }

// nativeToolDefs returns the OpenAI-style function definitions (read-only).
func (h *SreyunCore) nativeToolDefs() []map[string]any { return h.snapshot().native }

// toolPrompt returns the text-injection tool description block.
func (h *SreyunCore) toolPrompt() string { return h.snapshot().prompt }

// eachTool iterates the published snapshot. Safe against concurrent re-registration.
func (h *SreyunCore) eachTool(fn func(name string, t SreyunTool)) {
	snap := h.snapshot()
	for _, name := range snap.names {
		fn(name, snap.byName[name])
	}
}

// mutateTools applies fn to the staging map under the write lock, then republishes.
func (h *SreyunCore) mutateTools(fn func(tools map[string]SreyunTool)) {
	h.toolsMu.Lock()
	defer h.toolsMu.Unlock()
	fn(h.tools)
	h.publishTools()
}

// ============================================================================
// 每轮调用的私有上下文 —— 通过工具参数携带
//
// 此前 SreyunCore 上挂着一个 h.ctx 字段，每次 Chat() 直接覆盖它，而 execDiagnostic /
// execPythonAction 又在【工具执行 goroutine】里读它。单例 + 并发会话下这既是数据竞争，
// 也是真实的串话：A 会话正在目标机上跑诊断命令，B 会话一开始就把 h.ctx 换成了自己的，
// B 关掉页面就会掐断 A 的命令；反过来 A 断开时自己的命令反而杀不掉。
//
// 上下文本就是「每次调用」的东西，所以随工具参数一起下发。参数 map 已经在用
// _scope_actor 传调用者身份，这里沿用同一约定，键名统一以 _ 开头标记为内部字段。
// ============================================================================

// internalArgPrefix marks tool-arg keys injected by the engine itself.
const internalArgPrefix = "_"

const (
	argKeyCallCtx   = "_call_ctx"
	argKeyCiteBuf   = "_cite_buf"
	argKeyScopeUser = "_scope_actor"
)

// stampCallContext attaches the turn's context to a tool-call argument set.
func (h *SreyunCore) stampCallContext(args map[string]any, ctx context.Context) {
	if args == nil || ctx == nil {
		return
	}
	args[argKeyCallCtx] = ctx
}

// callContext recovers the turn's context, falling back to Background so tools
// invoked outside a chat loop (MCP server, prefetch) still work.
func (h *SreyunCore) callContext(args map[string]any) context.Context {
	if args != nil {
		if ctx, ok := args[argKeyCallCtx].(context.Context); ok && ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

// newCallCitations creates a per-turn citation buffer and attaches it to args.
func (h *SreyunCore) newCallCitations(args map[string]any) *citationBuf {
	buf := &citationBuf{}
	if args != nil {
		args[argKeyCiteBuf] = buf
	}
	return buf
}

// carryCallMeta copies engine-injected keys from one arg set onto the next tool call.
func carryCallMeta(dst, src map[string]any) {
	if dst == nil || src == nil {
		return
	}
	for _, k := range []string{argKeyCallCtx, argKeyCiteBuf, argKeyScopeUser} {
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
}

// addCitation records a retrieval citation against the calling turn only.
func (h *SreyunCore) addCitation(args map[string]any, items ...RAGCitation) {
	if args == nil {
		return
	}
	if buf, ok := args[argKeyCiteBuf].(*citationBuf); ok && buf != nil {
		buf.add(items...)
	}
}

// sanitizeOutboundArgs strips engine-internal keys before arguments leave the
// process. Bridged ext_* tools forward the raw arg map to a third-party MCP
// server, so without this the dashboard username (_scope_actor) is disclosed to
// that server — and a context.Context value would fail json.Marshal outright,
// breaking every external MCP call.
func sanitizeOutboundArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if strings.HasPrefix(k, internalArgPrefix) {
			continue
		}
		out[k] = v
	}
	return out
}
