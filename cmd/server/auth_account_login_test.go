package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticateAccountLoginPrefersUsernameOverPhone(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	adminSalt := genToken()[:16]
	peerSalt := genToken()[:16]
	phoneUser := "13800138000"
	cfg.cfg.Users = []AccountConfig{
		{
			Username: phoneUser, Role: RoleAdmin,
			Salt: adminSalt, Hash: hashPassword("AdminPass1!", adminSalt),
		},
		{
			Username: "peer", Role: RoleViewer, Phone: phoneUser,
			Salt: peerSalt, Hash: hashPassword("PeerPass1!", peerSalt),
		},
	}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, auth: NewAuth(cfg), store: NewStore()}

	body := `{"username":"13800138000","password":"AdminPass1!"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(body))
	s.handleLogin(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin username login status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["ok"] != true {
		t.Fatalf("want ok session for phone-shaped username, got %v", resp)
	}
	// Cookie session must belong to the username account, not the phone peer.
	me := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	for _, c := range w.Result().Cookies() {
		me.AddCookie(c)
	}
	acc, ok := s.currentUser(me)
	if !ok || acc.Username != phoneUser {
		t.Fatalf("session user=%q ok=%v, want %q", acc.Username, ok, phoneUser)
	}
}

func TestSetUserProfileRejectsPhoneMatchingOtherUsername(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	salt := genToken()[:16]
	cfg.cfg.Users = []AccountConfig{
		{Username: "13800138000", Role: RoleAdmin, Salt: salt, Hash: hashPassword("AdminPass1!", salt)},
		{Username: "peer", Role: RoleViewer, Salt: salt, Hash: hashPassword("PeerPass1!", salt)},
	}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	err = cfg.SetUserProfile("peer", "Peer", "", "13800138000")
	if err == nil {
		t.Fatal("expected phone/username conflict")
	}
	if !strings.Contains(err.Error(), Tz("user.phone_username_conflict")) &&
		!strings.Contains(err.Error(), "phone_username_conflict") &&
		!strings.Contains(err.Error(), "冲突") &&
		!strings.Contains(err.Error(), "衝突") &&
		!strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSetUserProfileRejectsDuplicatePhone(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	salt := genToken()[:16]
	cfg.cfg.Users = []AccountConfig{
		{Username: "alice", Role: RoleOperator, Phone: "13900139000", Salt: salt, Hash: hashPassword("AlicePass1!", salt)},
		{Username: "bob", Role: RoleViewer, Salt: salt, Hash: hashPassword("BobPass1!", salt)},
	}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetUserProfile("bob", "Bob", "", "139-0013-9000"); err == nil {
		t.Fatal("expected duplicate phone rejection")
	}
}

func TestAuthenticateSMSLoginRejectsAmbiguousPhone(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	salt := genToken()[:16]
	phone := "13700137000"
	cfg.cfg.Users = []AccountConfig{
		{Username: "a1", Role: RoleAdmin, Phone: phone, Salt: salt, Hash: hashPassword("A1Passw0rd!", salt)},
		{Username: "a2", Role: RoleOperator, Phone: phone, Salt: salt, Hash: hashPassword("A2Passw0rd!", salt)},
	}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, auth: NewAuth(cfg), store: NewStore()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/login", nil)
	_, ok := s.authenticateSMSLogin(w, r, phone, "123456", "127.0.0.1")
	if ok {
		t.Fatal("ambiguous phone must not authenticate")
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
