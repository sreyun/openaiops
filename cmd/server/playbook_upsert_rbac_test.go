package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// Scheduled playbooks fire without a user session. Manual execute filters via
// filterHostsForUser, but upsert previously accepted foreign targets — so a
// scoped operator could plant schedule.enabled + host:<foreign> (or "all") and
// the scheduler would auto-run read-only modules on unauthorized agents.
func TestPlaybookUpsertRespectsHostScope(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), playbooks: newPlaybookManager(cfg)}

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

	post := func(body string) int {
		rr := httptest.NewRecorder()
		req := withSession(httptest.NewRequest(http.MethodPost, "/api/v1/playbooks", strings.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		s.handleUpsertPlaybook(rr, req)
		return rr.Code
	}

	foreign := `{"name":"recon-b","steps":[{"name":"facts","module":"gather_facts","target":"host:host-b","timeout_sec":60}],"schedule":{"enabled":true,"kind":"interval","interval_min":1}}`
	if code := post(foreign); code != http.StatusForbidden {
		t.Fatalf("upsert foreign host target: want 403 got %d", code)
	}
	if n := len(cfg.Playbooks()); n != 0 {
		t.Fatalf("foreign playbook was persisted: %d", n)
	}

	fleet := `{"name":"recon-all","steps":[{"name":"facts","module":"gather_facts","target":"all","timeout_sec":60}],"schedule":{"enabled":true,"kind":"interval","interval_min":1}}`
	if code := post(fleet); code != http.StatusForbidden {
		t.Fatalf("upsert all target: want 403 got %d", code)
	}

	okBody := `{"name":"recon-a","steps":[{"name":"facts","module":"gather_facts","target":"host:host-a","timeout_sec":60}],"schedule":{"enabled":true,"kind":"interval","interval_min":1}}`
	if code := post(okBody); code != http.StatusOK {
		t.Fatalf("upsert in-scope host: want 200 got %d", code)
	}
	list := cfg.Playbooks()
	if len(list) != 1 || list[0].Steps[0].Target != "host:host-a" {
		t.Fatalf("expected one in-scope playbook, got %+v", list)
	}

	// Packs embed target=all — scoped import must not plant fleet playbooks.
	rr := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/v1/playbooks/packs/import", strings.NewReader(`{"id":"inspect"}`)))
	req.Header.Set("Content-Type", "application/json")
	s.handleImportPlaybookPacks(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("import pack: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
}
