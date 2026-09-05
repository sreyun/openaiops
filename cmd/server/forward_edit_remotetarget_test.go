package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aiops-monitor/shared"
)

// The classic single-rule edit UI sends host/ports/whitelist only — no remote_target.
// Omitting the field must not clear an existing jump target (that was a silent data-loss
// path: edit any jump rule → traffic falls back to agent localhost).
func TestForwardEditOmitsRemoteTargetPreservesJump(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), forward: newForwardManager(cfg)}
	fp := "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_ = store.RegisterHost("host-a", "alpha", fp)
	_, _ = store.UpsertAuthenticated(shared.Report{HostID: "host-a", Hostname: "alpha", Fingerprint: fp}, fp)

	salt := genToken()[:16]
	cfg.cfg.Users = []AccountConfig{{
		Username: "admin", DisplayName: "Admin", Role: RoleAdmin,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
	}}

	rule, err := s.forward.createRule("host-a", "alpha", 3306, 0, "127.0.0.1", "tcp", "", "admin", "192.168.30.220:3306", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rule.remoteTarget != "192.168.30.220:3306" {
		t.Fatalf("setup remoteTarget = %q", rule.remoteTarget)
	}

	body, _ := json.Marshal(map[string]any{
		"host_id": "host-a", "target_port": 3307, "local_port": rule.localPort,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/forward/"+rule.id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", rule.id)
	w := httptest.NewRecorder()
	// Authenticate as admin via session cookie path used by tests elsewhere.
	tok := s.auth.issueSession("admin")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})

	s.handleForwardEdit(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	got := s.forward.getRule(rule.id)
	if got == nil {
		t.Fatal("rule missing after edit")
	}
	if got.remoteTarget != "192.168.30.220:3306" {
		t.Fatalf("omit remote_target wiped jump: got %q", got.remoteTarget)
	}
	if got.targetPort != 3307 {
		t.Fatalf("target_port not updated: %d", got.targetPort)
	}
}

func TestForwardEditExplicitEmptyClearsJump(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), forward: newForwardManager(cfg)}
	fp := "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_ = store.RegisterHost("host-b", "beta", fp)
	_, _ = store.UpsertAuthenticated(shared.Report{HostID: "host-b", Hostname: "beta", Fingerprint: fp}, fp)

	salt := genToken()[:16]
	cfg.cfg.Users = []AccountConfig{{
		Username: "admin", DisplayName: "Admin", Role: RoleAdmin,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
	}}

	rule, err := s.forward.createRule("host-b", "beta", 5432, 0, "127.0.0.1", "tcp", "", "admin", "10.0.0.5:5432", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	body, _ := json.Marshal(map[string]any{
		"host_id": "host-b", "target_port": 5432, "local_port": rule.localPort,
		"remote_target": empty,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/forward/"+rule.id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", rule.id)
	tok := s.auth.issueSession("admin")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	s.handleForwardEdit(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	got := s.forward.getRule(rule.id)
	if got == nil || got.remoteTarget != "" {
		t.Fatalf("explicit empty should clear jump, got %#v", got)
	}
}
