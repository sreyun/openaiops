package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// TestGetDiagnosisChatHistoryRequiresHostAccess ensures GET on the same path as
// POST /incidents/{id}/diagnose-chat enforces requireIncidentAccess — history can
// contain terminal snippets and host context from out-of-scope incidents.
func TestGetDiagnosisChatHistoryRequiresHostAccess(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{
		cfg: cfg, store: store, auth: NewAuth(cfg),
		incidents: newIncidentManager(),
		logs:      newLogStore(),
	}
	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	salt := genToken()[:16]
	op := AccountConfig{
		Username: "scoped", DisplayName: "Scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
		AllowedHostIDs: []string{"host-a"},
	}
	cfg.cfg.Users = append(cfg.cfg.Users, op)
	_ = cfg.save()

	incIn := s.incidents.CreateManual("in-scope", "warning", "host-a", "alpha", "admin")
	incOut := s.incidents.CreateManual("out-of-scope", "critical", "host-b", "beta", "admin")

	tok := s.auth.issueSession("scoped")
	withSession := func(req *http.Request) *http.Request {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		return req
	}

	rr := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/incidents/"+strconv.FormatInt(incIn.ID, 10)+"/diagnose-chat", nil))
	req.SetPathValue("id", strconv.FormatInt(incIn.ID, 10))
	s.handleGetDiagnosisChatHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("in-scope diagnose-chat GET: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	req = withSession(httptest.NewRequest(http.MethodGet, "/api/v1/incidents/"+strconv.FormatInt(incOut.ID, 10)+"/diagnose-chat", nil))
	req.SetPathValue("id", strconv.FormatInt(incOut.ID, 10))
	s.handleGetDiagnosisChatHistory(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope diagnose-chat GET: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestLogDiagnoseRespectsHostVisibility ensures POST /logs/diagnose without
// host_id does not feed platform-wide error rings to scoped operators.
func TestLogDiagnoseRespectsHostVisibility(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	logs := newLogStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), logs: logs, ai: newAIManager(cfg)}

	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	now := time.Now().Unix()
	logs.ingest("host-a", "alpha", []shared.LogLine{
		{Ts: now, Level: "error", Message: "alpha-only-error-line"},
	})
	logs.ingest("host-b", "beta", []shared.LogLine{
		{Ts: now, Level: "error", Message: "beta-secret-error-line"},
	})

	salt := genToken()[:16]
	op := AccountConfig{
		Username: "scoped", DisplayName: "Scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
		AllowedHostIDs: []string{"host-a"},
	}
	cfg.cfg.Users = append(cfg.cfg.Users, op)
	_ = cfg.save()

	tok := s.auth.issueSession("scoped")
	body := strings.NewReader(`{"since_min":30}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs/diagnose", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	rr := httptest.NewRecorder()
	s.handleLogDiagnose(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("log diagnose: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if strings.Contains(out, "beta-secret-error-line") {
		t.Fatalf("scoped log diagnose leaked out-of-scope host errors: %s", out)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, out)
	}
}

// TestRecentErrorsFilteredByAllow locks the allow-callback contract used by log diagnose.
func TestRecentErrorsFilteredByAllow(t *testing.T) {
	ls := newLogStore()
	now := time.Now().Unix()
	ls.ingest("host-a", "alpha", []shared.LogLine{{Ts: now, Level: "error", Message: "a"}})
	ls.ingest("host-b", "beta", []shared.LogLine{{Ts: now, Level: "error", Message: "b"}})
	allow := func(id string) bool { return id == "host-a" }
	got := ls.recentErrorsFiltered(now-10, 50, allow)
	if len(got) != 1 || got[0].HostID != "host-a" {
		t.Fatalf("filtered errors: %+v", got)
	}
	if n := ls.errorCountFiltered(now-10, allow); n != 1 {
		t.Fatalf("filtered count=%d want 1", n)
	}
}
