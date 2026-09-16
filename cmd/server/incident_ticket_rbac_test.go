package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// e37759f closed GET/create/escalate/diagnose host RBAC for incidents and forward
// rules, but left ack/resolve/comment, ticket CRUD, and remediation approve open.
// A folder-scoped operator could still mutate out-of-scope incidents (and approve
// playbook remediations that execute on those hosts).
func TestIncidentMutateAndTicketRemediationRespectHostScope(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	im := newIncidentManager()
	tm := newTicketManager()
	rm := newRemediationManager(cfg)
	s := &Server{
		cfg: cfg, store: store, auth: NewAuth(cfg),
		incidents: im, tickets: tm, remediation: rm,
		oncall: newOnCallManager(), messages: newMessageHub(),
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
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	tok := s.auth.issueSession("scoped")
	withSession := func(req *http.Request) *http.Request {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		return req
	}

	incA := im.CreateManual("a-down", "critical", "host-a", "alpha", "admin")
	incB := im.CreateManual("b-down", "critical", "host-b", "beta", "admin")

	call := func(method, path, body string, id int64, h func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := withSession(httptest.NewRequest(method, path, strings.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", itoa64(id))
		h(rr, req)
		return rr
	}

	// Out-of-scope incident: ack / resolve / comment must 403
	if rr := call(http.MethodPost, "/api/v1/incidents/x/ack", `{}`, incB.ID, s.handleAckIncident); rr.Code != http.StatusForbidden {
		t.Fatalf("ack out-of-scope: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := call(http.MethodPost, "/api/v1/incidents/x/resolve", `{}`, incB.ID, s.handleResolveIncident); rr.Code != http.StatusForbidden {
		t.Fatalf("resolve out-of-scope: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := call(http.MethodPost, "/api/v1/incidents/x/comment", `{"text":"x"}`, incB.ID, s.handleCommentIncident); rr.Code != http.StatusForbidden {
		t.Fatalf("comment out-of-scope: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	if got, _ := im.Get(incB.ID); got.Status == "resolved" || got.Status == "acknowledged" {
		t.Fatalf("out-of-scope incident mutated: status=%q", got.Status)
	}

	// In-scope still works
	if rr := call(http.MethodPost, "/api/v1/incidents/x/ack", `{}`, incA.ID, s.handleAckIncident); rr.Code != http.StatusOK {
		t.Fatalf("ack in-scope: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	// Ticket bound to out-of-scope incident: list hides it; get/update/create-link blocked
	tkB, err := tm.Create(Ticket{
		Title: "fix-b", IncidentID: incB.ID, Kind: "incident",
		Links: []OpsLink{incidentOpsLink(incB.ID, "caused_by"), hostOpsLink("host-b", "beta")},
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	tkA, err := tm.Create(Ticket{
		Title: "fix-a", IncidentID: incA.ID, Kind: "incident",
		Links: []OpsLink{incidentOpsLink(incA.ID, "caused_by"), hostOpsLink("host-a", "alpha")},
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.handleListTickets(rr, withSession(httptest.NewRequest(http.MethodGet, "/api/v1/tickets", nil)))
	if rr.Code != http.StatusOK {
		t.Fatalf("list tickets: %d %s", rr.Code, rr.Body.String())
	}
	var listed []TicketListRow
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, row := range listed {
		if row.ID == tkB.ID {
			t.Fatalf("list leaked out-of-scope ticket #%d", tkB.ID)
		}
	}
	foundA := false
	for _, row := range listed {
		if row.ID == tkA.ID {
			foundA = true
		}
	}
	if !foundA {
		t.Fatal("in-scope ticket missing from list")
	}

	if rr := call(http.MethodGet, "/api/v1/tickets/x", ``, tkB.ID, s.handleGetTicket); rr.Code != http.StatusForbidden {
		t.Fatalf("get out-of-scope ticket: want 403 got %d", rr.Code)
	}
	if rr := call(http.MethodPost, "/api/v1/tickets/x", `{"status":"resolved"}`, tkB.ID, s.handleUpdateTicket); rr.Code != http.StatusForbidden {
		t.Fatalf("resolve out-of-scope ticket: want 403 got %d", rr.Code)
	}
	if got, _ := im.Get(incB.ID); got.Status == "resolved" {
		t.Fatal("ticket side-door resolved out-of-scope incident")
	}

	// Creating a ticket that points at out-of-scope incident must fail
	rr = httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/v1/tickets", strings.NewReader(
		`{"title":"steal","incident_id":`+itoa64(incB.ID)+`}`)))
	req.Header.Set("Content-Type", "application/json")
	s.handleCreateTicket(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("create ticket→out-of-scope incident: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}

	// Remediation approve on out-of-scope host must 403 before launch
	run, err := rm.ProposeManual(Playbook{ID: "pb1", Name: "restart"}, "host-b", "beta", incB.ID, "fix-out", "admin")
	if err != nil {
		// ProposeManual may need a real playbook id in manager — seed via direct run insert
		rm.mu.Lock()
		rm.nextID++
		run = RemediationRun{
			ID: rm.nextID, HostID: "host-b", Hostname: "beta",
			Status: "pending_approval", PlaybookID: "pb1", PlaybookName: "restart",
			IncidentID: incB.ID, CreatedAt: 1,
		}
		rm.runs = append(rm.runs, run)
		rm.mu.Unlock()
	}
	if rr := call(http.MethodPost, "/api/v1/remediation/runs/x/approve", `{}`, run.ID, s.handleApproveRemediation); rr.Code != http.StatusForbidden {
		t.Fatalf("approve out-of-scope remediation: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rm.Runs(); len(got) > 0 {
		for _, r := range got {
			if r.ID == run.ID && r.Status == "running" {
				t.Fatal("out-of-scope remediation was approved/launched")
			}
		}
	}

	// GET diagnose-chat history also gated
	if rr := call(http.MethodGet, "/api/v1/incidents/x/diagnose-chat", ``, incB.ID, s.handleGetDiagnosisChatHistory); rr.Code != http.StatusForbidden {
		t.Fatalf("diagnose-chat GET out-of-scope: want 403 got %d", rr.Code)
	}
}
