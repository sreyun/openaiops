package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestSreyunToolMapConcurrentAccess proves the tool registry survives a runtime
// re-registration (triggered by the AI-settings / MCP-client save handlers)
// racing against in-flight tool lookups from concurrent chat turns.
//
// Before the fix these ran against a bare map[string]SreyunTool. Reproduced
// against the pre-fix tree, this exact shape aborts the test binary with
// "fatal error: concurrent map writes" — in production that is the whole
// server process dying because someone saved the AI settings page.
func TestSreyunToolMapConcurrentAccess(t *testing.T) {
	h := newSreyunCore(&Server{mcpClients: newMCPClientManager(), cfg: newTestConfigStore(t)})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: mirror runLoop (lookup) and injectTools (full snapshot).
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = h.lookupTool("query_metrics")
				_ = h.toolNames()
				_ = h.nativeToolDefs()
			}
		}()
	}

	// Writers: mirror handleSetAIConfig / handleSyncMCPClient.
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				h.reloadExternalMCPTools()
			}
		}()
	}

	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()

	// Core tools must still be intact after the churn.
	for _, name := range []string{"query_metrics", "search_logs", "list_alerts"} {
		if _, ok := h.lookupTool(name); !ok {
			t.Fatalf("core tool %q lost after concurrent re-registration", name)
		}
	}
}

// TestSreyunPerCallContextIsolation proves one chat turn's cancellation cannot
// abort a different turn's host command.
//
// h.ctx used to be a single shared field assigned by every Chat() call, so the
// most recent caller's context governed every in-flight tool on the box.
func TestSreyunPerCallContextIsolation(t *testing.T) {
	h := newSreyunCore(&Server{})

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())

	argsA := map[string]any{}
	argsB := map[string]any{}
	h.stampCallContext(argsA, ctxA)
	h.stampCallContext(argsB, ctxB)

	// B disconnects. A must be unaffected.
	cancelB()

	if err := h.callContext(argsA).Err(); err != nil {
		t.Fatalf("turn A context was cancelled by turn B: %v", err)
	}
	if err := h.callContext(argsB).Err(); err == nil {
		t.Fatal("turn B context should be cancelled")
	}
}

// TestSreyunCitationsArePerCall proves WeKnora citations recorded by one turn
// are not attributed to (or wiped by) a concurrent turn.
func TestSreyunCitationsArePerCall(t *testing.T) {
	h := newSreyunCore(&Server{})

	argsA := map[string]any{}
	argsB := map[string]any{}
	bufA := h.newCallCitations(argsA)
	bufB := h.newCallCitations(argsB)

	h.addCitation(argsA, RAGCitation{Kind: "weknora", Title: "doc-A"})
	h.addCitation(argsB, RAGCitation{Kind: "weknora", Title: "doc-B1"})
	h.addCitation(argsB, RAGCitation{Kind: "weknora", Title: "doc-B2"})

	if got := bufA.snapshot(); len(got) != 1 || got[0].Title != "doc-A" {
		t.Fatalf("turn A citations contaminated: %+v", got)
	}
	if got := bufB.snapshot(); len(got) != 2 {
		t.Fatalf("turn B expected 2 citations, got %+v", got)
	}
}
