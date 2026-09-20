package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

func scopedChangeTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{
		cfg: cfg, store: store, auth: NewAuth(cfg),
		changes: newChangeManager(), incidents: newIncidentManager(),
		tickets: newTicketManager(), messages: newMessageHub(),
		assistStore: newAssistStore(),
	}
	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	for _, h := range []struct{ id, name, fp string }{
		{"host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	} {
		_, _ = store.UpsertAuthenticated(shared.Report{HostID: h.id, Hostname: h.name, Fingerprint: h.fp}, h.fp)
	}
	salt := genToken()[:16]
	cfg.cfg.Users = append(cfg.cfg.Users, AccountConfig{
		Username: "scoped", DisplayName: "Scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
		AllowedHostIDs: []string{"host-a"},
	})
	tok := s.auth.issueSession("scoped")
	return s, tok
}

func withScopedSession(req *http.Request, tok string) *http.Request {
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	return req
}

// Scoped operators must not forge approved change records for out-of-scope hosts
// (remoteGateCheck treats approved/in_progress/scheduled as freeze unlock).
func TestChangeUpsertRespectsHostScope(t *testing.T) {
	s, tok := scopedChangeTestServer(t)

	body := `{"title":"unlock-b","host_ids":["host-b"],"status":"approved","risk":"high"}`
	req := withScopedSession(httptest.NewRequest(http.MethodPost, "/api/v1/changes", strings.NewReader(body)), tok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handleUpsertChange(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("foreign host upsert: got %d want 403 body=%s", rr.Code, rr.Body.String())
	}
	if n := len(s.changes.List()); n != 0 {
		t.Fatalf("no change should be stored, got %d", n)
	}

	body = `{"title":"ok-a","host_ids":["host-a"],"status":"draft"}`
	req = withScopedSession(httptest.NewRequest(http.MethodPost, "/api/v1/changes", strings.NewReader(body)), tok)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	s.handleUpsertChange(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("in-scope upsert: got %d want 200 body=%s", rr.Code, rr.Body.String())
	}
}

func TestChangeListAndApproveRespectHostScope(t *testing.T) {
	s, tok := scopedChangeTestServer(t)
	foreign, err := s.changes.Upsert(ChangeRecord{
		Title: "foreign", HostIDs: []string{"host-b"}, Status: ChangeApproved,
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := s.changes.Upsert(ChangeRecord{
		Title: "mine", HostIDs: []string{"host-a"}, Status: ChangeDraft,
	}, "scoped")
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.handleListChanges(rr, withScopedSession(httptest.NewRequest(http.MethodGet, "/api/v1/changes", nil), tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	var rows []ChangeListRow
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		for _, hid := range row.HostIDs {
			if hid == "host-b" {
				t.Fatalf("list leaked foreign change #%d", row.ID)
			}
		}
	}

	req := withScopedSession(httptest.NewRequest(http.MethodPost, "/api/v1/changes/x/approve", nil), tok)
	req.SetPathValue("id", strconv.FormatInt(foreign.ID, 10))
	rr = httptest.NewRecorder()
	s.handleChangeApprove(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("approve foreign: got %d want 403 body=%s", rr.Code, rr.Body.String())
	}

	req = withScopedSession(httptest.NewRequest(http.MethodGet, "/api/v1/changes/x", nil), tok)
	req.SetPathValue("id", strconv.FormatInt(mine.ID, 10))
	rr = httptest.NewRecorder()
	s.handleGetChange(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get mine: got %d want 200 body=%s", rr.Code, rr.Body.String())
	}
}

func TestChangeWindowGlobalRequiresAdmin(t *testing.T) {
	s, tok := scopedChangeTestServer(t)
	body := `{"name":"global-freeze","freeze":true,"start":1,"end":9999999999}`
	req := withScopedSession(httptest.NewRequest(http.MethodPost, "/api/v1/changes/windows", strings.NewReader(body)), tok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handleUpsertChangeWindow(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("global window: got %d want 403 body=%s", rr.Code, rr.Body.String())
	}

	body = `{"name":"b-freeze","freeze":true,"host_ids":["host-b"],"start":1,"end":9999999999}`
	req = withScopedSession(httptest.NewRequest(http.MethodPost, "/api/v1/changes/windows", strings.NewReader(body)), tok)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	s.handleUpsertChangeWindow(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("foreign window: got %d want 403 body=%s", rr.Code, rr.Body.String())
	}

	// Categories OR-match every host in that category; host-id-only scoped ops must not set them.
	body = `{"name":"cat-freeze","freeze":true,"categories":["db"],"start":1,"end":9999999999}`
	req = withScopedSession(httptest.NewRequest(http.MethodPost, "/api/v1/changes/windows", strings.NewReader(body)), tok)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	s.handleUpsertChangeWindow(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("category window for host-id scope: got %d want 403 body=%s", rr.Code, rr.Body.String())
	}
}

func TestAIFollowupRespectsHostScope(t *testing.T) {
	s, tok := scopedChangeTestServer(t)
	incB := s.incidents.CreateManual("foreign", "critical", "host-b", "beta", "admin")
	before, _ := s.incidents.Get(incB.ID)
	beforeN := len(before.Timeline)
	s.persistAIRun(AIRun{
		ID: "run_scope_1", Kind: "sreyun", Task: "sreyun",
		Answer: strings.Repeat("根因分析与处置建议。", 20), Actor: "scoped",
	})

	post := func(body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := withScopedSession(httptest.NewRequest(http.MethodPost, "/api/v1/ai/followup", bytes.NewReader(b)), tok)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.handleAIFollowup(rr, req)
		return rr
	}

	rr := post(map[string]any{
		"run_id": "run_scope_1", "action": aiActAddIncidentNote, "incident_id": incB.ID,
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("add note foreign: got %d want 403 body=%s", rr.Code, rr.Body.String())
	}
	if got, _ := s.incidents.Get(incB.ID); len(got.Timeline) != beforeN {
		t.Fatalf("timeline should be unchanged, before=%d after=%d", beforeN, len(got.Timeline))
	}

	rr = post(map[string]any{
		"run_id": "run_scope_1", "action": aiActCreateTicket, "incident_id": incB.ID, "title": "x",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("ticket foreign incident: got %d want 403 body=%s", rr.Code, rr.Body.String())
	}
	if n := len(s.tickets.List("")); n != 0 {
		t.Fatalf("no ticket should be created, got %d", n)
	}
}
