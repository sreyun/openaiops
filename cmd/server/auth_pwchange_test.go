package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPasswordChangeSessionBlocksAPI(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	auth := NewAuth(cfg)
	srv := &Server{cfg: cfg, store: store, auth: auth}

	// Issue a password-change-only session as the default admin would get.
	tok := auth.issuePasswordChangeSession("admin")
	mw := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	// Hosts API must be forbidden.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("hosts with pw-change session: got %d, want 403", rr.Code)
	}

	// /api/v1/me must still work (SPA reads must_change_password).
	req = httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	rr = httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("me with pw-change session: got %d, want 200", rr.Code)
	}
}

func TestCompleteLoginDefaultAdminIssuesPwChangeSession(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	auth := NewAuth(cfg)
	srv := &Server{cfg: cfg, store: store, auth: auth}

	acc, ok := cfg.UserByName("admin")
	if !ok {
		t.Fatal("default admin missing")
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{}`))
	srv.completeLogin(rr, req, acc, "admin", "", "127.0.0.1")
	if rr.Code != http.StatusOK {
		t.Fatalf("login status %d body %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["must_change_password"] != true {
		t.Fatalf("expected must_change_password=true, got %#v", resp)
	}
	cookie := rr.Result().Cookies()
	var sess string
	for _, c := range cookie {
		if c.Name == sessionCookie {
			sess = c.Value
		}
	}
	if sess == "" {
		t.Fatal("missing session cookie")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	req2.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess})
	if !auth.isPasswordChangeOnly(req2) {
		t.Fatal("session must be password-change-only")
	}
}

func TestAPIRateLimitSkipAgentPaths(t *testing.T) {
	if !apiRateLimitSkip("/api/v1/agent/report") {
		t.Fatal("agent report must skip global rate limit")
	}
	if apiRateLimitSkip("/api/v1/hosts") {
		t.Fatal("hosts must be rate-limited")
	}
}

// Forced password-change sessions may call POST /password. That path must not
// mint a full session when MFARequired is on and the user has no TOTP yet —
// otherwise global MFA enrollment is skipped entirely.
func TestPasswordChangeHonorsMFARequired(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetMFARequired(true); err != nil {
		t.Fatal(err)
	}
	salt := genToken()[:16]
	oldPass := "TempPass1!"
	newPass := "StrongPass1!"
	cfg.cfg.Users = []AccountConfig{{
		Username:           "ops1",
		Role:               RoleOperator,
		Salt:               salt,
		Hash:               hashPassword(oldPass, salt),
		MustChangePassword: true,
	}}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}

	auth := NewAuth(cfg)
	srv := &Server{cfg: cfg, store: NewStore(), auth: auth}
	tok := auth.issuePasswordChangeSession("ops1")

	body := `{"old":"` + oldPass + `","new":"` + newPass + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	rr := httptest.NewRecorder()
	srv.handleSetPassword(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["require_mfa_setup"] != true {
		t.Fatalf("expected require_mfa_setup=true, got %#v", resp)
	}
	var newTok string
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			newTok = c.Value
		}
	}
	if newTok == "" {
		t.Fatal("missing session cookie after password change")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	req2.AddCookie(&http.Cookie{Name: sessionCookie, Value: newTok})
	if !auth.isRestricted(req2) {
		t.Fatal("session must remain MFA-enrollment restricted when MFARequired")
	}
	if auth.isPasswordChangeOnly(req2) {
		t.Fatal("password-change-only flag should be cleared")
	}

	mw := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr2 := httptest.NewRecorder()
	mw.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Fatalf("hosts via post-password session: got %d, want 403", rr2.Code)
	}
}

func TestFinishSSOLoginHonorsMFARequired(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetMFARequired(true); err != nil {
		t.Fatal(err)
	}
	salt := genToken()[:16]
	cfg.cfg.Users = append(cfg.cfg.Users, AccountConfig{
		Username: "sso1", Role: RoleOperator, Salt: salt, Hash: hashPassword("UnusedPass1!", salt),
	})
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	auth := NewAuth(cfg)
	srv := &Server{cfg: cfg, store: NewStore(), auth: auth}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/feishu/callback", nil)
	srv.finishSSOLogin(rr, req, "sso1", "feishu")
	if rr.Code != http.StatusFound {
		t.Fatalf("status=%d", rr.Code)
	}
	if loc := rr.Result().Header.Get("Location"); !strings.Contains(loc, "require_mfa_setup=1") {
		t.Fatalf("expected require_mfa_setup redirect, got %q", loc)
	}
	var sess string
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			sess = c.Value
		}
	}
	if sess == "" {
		t.Fatal("missing session cookie")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	req2.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess})
	if !auth.isRestricted(req2) {
		t.Fatal("SSO session must be restricted under MFARequired")
	}
}
