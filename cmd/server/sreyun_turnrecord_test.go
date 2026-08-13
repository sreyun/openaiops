package main

import (
	"strings"
	"testing"
)

// Native Function Calling returns the calls in a structured field and usually
// leaves content empty. Feeding that straight back produced an empty assistant
// message — rejected outright by Anthropic and by several OpenAI-compatible
// gateways, which broke the loop on the second turn — and also dropped any
// record of what the model had just called.
func TestAssistantTurnRecordNeverEmpty(t *testing.T) {
	got := assistantTurnRecord("", []toolCall{
		{Name: "query_metrics", Args: map[string]any{"host_id": "h1", "metric": "cpu"}},
	})
	if strings.TrimSpace(got) == "" {
		t.Fatal("assistant turn record must not be empty")
	}
	if !strings.Contains(got, "query_metrics") {
		t.Errorf("tool name missing from turn record: %q", got)
	}
	if !strings.Contains(got, "h1") {
		t.Errorf("tool args missing from turn record: %q", got)
	}
}

// Internal engine keys ride along in the arg map; they must never be written
// into the conversation context (nor marshalled — _call_ctx is a Context).
func TestAssistantTurnRecordStripsInternalKeys(t *testing.T) {
	args := map[string]any{"host_id": "h1"}
	h := newSreyunCore(&Server{})
	h.stampCallContext(args, t.Context())
	h.newCallCitations(args)
	args[argKeyScopeUser] = "alice"

	got := assistantTurnRecord("正在检查", []toolCall{{Name: "query_metrics", Args: args}})
	for _, leak := range []string{argKeyCallCtx, argKeyCiteBuf, argKeyScopeUser, "alice"} {
		if strings.Contains(got, leak) {
			t.Errorf("internal key %q leaked into context: %q", leak, got)
		}
	}
	if !strings.Contains(got, "正在检查") {
		t.Errorf("model text dropped: %q", got)
	}
}

// The same stripping guards the outbound path to third-party MCP servers.
func TestSanitizeOutboundArgsDropsInternals(t *testing.T) {
	args := map[string]any{
		"query":         "SELECT 1",
		argKeyScopeUser: "alice",
		argKeyCallCtx:   t.Context(),
	}
	out := sanitizeOutboundArgs(args)
	if _, ok := out["query"]; !ok {
		t.Error("caller args must survive sanitization")
	}
	if len(out) != 1 {
		t.Errorf("internal keys leaked to external MCP server: %+v", out)
	}
}
