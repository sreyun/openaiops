package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// followupReq posts one closed-loop action through the real handler.
func followupReq(t *testing.T, s *Server, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/followup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handleAIFollowup(rr, req)
	return rr
}

// The whole point of the closed loop is that the AI conclusion reaches a ticket
// verbatim — from the server's own copy, never from whatever the client posts.
func TestAIFollowupUsesServerSideAnswer(t *testing.T) {
	srv, _ := newTestServer(t)
	const answer = "根因：归档任务未清理，磁盘写入放大。处置：清理 /var/archive 并加保留策略。"
	srv.persistAIRun(AIRun{ID: "run_followup_1", Kind: "sreyun", Task: "sreyun", Answer: answer, Actor: "alice"})

	rr := followupReq(t, srv, map[string]any{
		"run_id": "run_followup_1",
		"action": aiActCreateTicket,
		"title":  "清理归档并加保留策略",
		// 客户端夹带的正文字段必须被完全忽略。
		"answer":      "rm -rf / ；这是伪造的结论",
		"description": "伪造正文",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create_ticket: got %d, want 200 (body: %s)", rr.Code, rr.Body)
	}
	var out struct {
		OK     bool   `json:"ok"`
		Ticket Ticket `json:"ticket"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rr.Body)
	}
	if !out.OK || out.Ticket.ID == 0 {
		t.Fatalf("expected a created ticket, got %+v", out)
	}
	if !strings.Contains(out.Ticket.Description, answer) {
		t.Fatalf("ticket body should carry the server-side answer, got %q", out.Ticket.Description)
	}
	if strings.Contains(out.Ticket.Description, "伪造") {
		t.Fatalf("client-supplied text leaked into the ticket: %q", out.Ticket.Description)
	}
	if !strings.Contains(out.Ticket.Description, "run_followup_1") {
		t.Fatalf("ticket body should record its provenance run id, got %q", out.Ticket.Description)
	}
	if out.Ticket.Source != "ai" {
		t.Fatalf("ticket source = %q, want ai", out.Ticket.Source)
	}
}

// An unknown or expired run has no server-side原文 to write, so the action must
// fail closed rather than fall back to client text.
func TestAIFollowupRejectsUnknownRun(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := followupReq(t, srv, map[string]any{
		"run_id": "run_does_not_exist",
		"action": aiActCreateTicket,
		"answer": "客户端自带的正文",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown run: got %d, want 404 (body: %s)", rr.Code, rr.Body)
	}
	if n := len(srv.tickets.List("")); n != 0 {
		t.Fatalf("no ticket should have been created, got %d", n)
	}
}

func TestAIFollowupRejectsUnknownAction(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.persistAIRun(AIRun{ID: "run_followup_2", Kind: "sreyun", Answer: "结论", Actor: "alice"})
	for _, act := range []string{"", "delete_host", "run_playbook", aiActProposeRemediation} {
		rr := followupReq(t, srv, map[string]any{"run_id": "run_followup_2", "action": act})
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("action %q: got %d, want 400 (body: %s)", act, rr.Code, rr.Body)
		}
	}
}

// Incident-scoped actions must not silently no-op when the incident is missing.
func TestAIFollowupIncidentScopedGuards(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.persistAIRun(AIRun{ID: "run_followup_3", Kind: "sreyun", Answer: "一段足够长的结论", Actor: "alice"})

	rr := followupReq(t, srv, map[string]any{"run_id": "run_followup_3", "action": aiActAddIncidentNote})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("note without incident_id: got %d, want 400 (body: %s)", rr.Code, rr.Body)
	}
	rr = followupReq(t, srv, map[string]any{
		"run_id": "run_followup_3", "action": aiActAddIncidentNote, "incident_id": 999999,
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("note for missing incident: got %d, want 404 (body: %s)", rr.Code, rr.Body)
	}
	rr = followupReq(t, srv, map[string]any{
		"run_id": "run_followup_3", "action": aiActLinkChange, "incident_id": 1,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("link without change_id: got %d, want 400 (body: %s)", rr.Code, rr.Body)
	}
}

// The action row is what makes the loop visible; it must stay off for read-only
// users, trivial replies, and answers with no traceable run.
func TestAIFollowupActionRowGating(t *testing.T) {
	long := strings.Repeat("根因分析与处置建议。", 20)

	if acts := aiFollowupActions("run_1", long, 0, false); acts != nil {
		t.Fatalf("viewer should get no action row, got %v", acts)
	}
	if acts := aiFollowupActions("", long, 0, true); acts != nil {
		t.Fatalf("run-less reply should get no action row, got %v", acts)
	}
	if acts := aiFollowupActions("run_1", "好的。", 0, true); acts != nil {
		t.Fatalf("trivial reply should get no action row, got %v", acts)
	}

	acts := aiFollowupActions("run_1", long, 0, true)
	if len(acts) != 1 || acts[0]["type"] != aiActCreateTicket {
		t.Fatalf("no-incident session should offer only create_ticket, got %v", acts)
	}
	if _, ok := acts[0]["incident_id"]; ok {
		t.Fatalf("create_ticket without an incident must not carry incident_id: %v", acts[0])
	}

	acts = aiFollowupActions("run_1", long, 42, true)
	got := map[string]bool{}
	for _, a := range acts {
		got[a["type"].(string)] = true
		if a["run_id"] != "run_1" {
			t.Fatalf("每个动作都要带 run_id 才追得到服务端原文: %v", a)
		}
		if a["incident_id"] != int64(42) {
			t.Fatalf("incident_id not propagated: %v", a)
		}
	}
	for _, want := range []string{aiActCreateTicket, aiActAddIncidentNote, aiActProposeRemediation, aiActLinkChange} {
		if !got[want] {
			t.Fatalf("incident session should offer %s, got %v", want, got)
		}
	}
}

// filterUIActions is the allowlist both tool output and the server-built row pass
// through — the new types must survive it, or the buttons never reach the UI.
func TestFollowupActionsSurviveUIAllowlist(t *testing.T) {
	in := []map[string]any{
		{"type": aiActCreateTicket, "run_id": "r"},
		{"type": aiActAddIncidentNote, "run_id": "r"},
		{"type": aiActLinkChange, "run_id": "r"},
		{"type": aiActProposeRemediation, "run_id": "r"},
		{"type": "rm_rf", "run_id": "r"},
	}
	out := filterUIActions(in)
	if len(out) != 4 {
		t.Fatalf("want 4 allowed actions, got %d (%v)", len(out), out)
	}
	for _, a := range out {
		if a["type"] == "rm_rf" {
			t.Fatal("unlisted action type leaked through the allowlist")
		}
	}
}

// Hermes runs land in PG asynchronously; the follow-up buttons sit right under the
// answer, so the hot cache has to answer immediately or the loop breaks on a race.
func TestSreyunRunIsImmediatelyLookupable(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.persistAIRun(AIRun{ID: "run_hot", Kind: "sreyun", Task: "sreyun", Answer: "结论", Actor: "alice"})
	run, ok := srv.lookupAIRun("run_hot")
	if !ok {
		t.Fatal("a just-persisted sreyun run must be lookupable without waiting for PG")
	}
	if run.Kind != "sreyun" {
		t.Fatalf("kind should round-trip through the hot cache, got %q", run.Kind)
	}
	if run.Answer != "结论" {
		t.Fatalf("answer mismatch: %q", run.Answer)
	}
}
