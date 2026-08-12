package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHandleGetConfigMasksCICDTokens ensures viewer-readable GET /api/v1/config
// never returns SCM PATs (dedicated /cicd/connections already masks; the generic
// config endpoint must match).
func TestHandleGetConfigMasksCICDTokens(t *testing.T) {
	cfg := newTestConfigStore(t)
	const secret = "glpat-CICDCONFIGLEAKTOKEN99"
	cfg.mu.Lock()
	cfg.cfg.CICDConnections = []CICDConnection{{
		ID: "c1", Name: "gitlab", Provider: CICDProviderGitLab,
		Project: "acme/app", Token: encryptSecret(secret), Enabled: true,
	}}
	cfg.mu.Unlock()

	srv := &Server{cfg: cfg}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	w := httptest.NewRecorder()
	srv.handleGetConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("CICD token leaked unmasked in /api/v1/config")
	}
	// Live config must still hold a decryptable token (mask must copy first).
	stored, ok := cfg.GetCICDConnection("c1")
	if !ok || decryptSecret(stored.Token) != secret {
		t.Fatalf("masking corrupted stored token: ok=%v token=%q", ok, stored.Token)
	}
	var got ServerConfig
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.CICDConnections) != 1 || !strings.Contains(got.CICDConnections[0].Token, "****") {
		t.Fatalf("expected masked cicd token, got %+v", got.CICDConnections)
	}
}

// TestConfigSetPreservesCICDConnections locks the alert-settings Set path so it
// cannot wipe SCM integrations when the form omits cicd_connections.
func TestConfigSetPreservesCICDConnections(t *testing.T) {
	cfg := newTestConfigStore(t)
	const secret = "ghp_PRESERVEONSETTOKEN1234"
	if _, err := cfg.AddCICDConnection(CICDConnection{
		Name: "gh", Provider: CICDProviderGitHub, Project: "acme/app",
		Token: secret, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	before := cfg.ListCICDConnections()
	if len(before) != 1 {
		t.Fatalf("setup: %+v", before)
	}

	in := cfg.Get()
	in.CICDConnections = nil // simulate alert form POST without the field
	in.Feishu.Enabled = true
	if err := cfg.Set(in); err != nil {
		t.Fatal(err)
	}
	after := cfg.ListCICDConnections()
	if len(after) != 1 {
		t.Fatalf("Set wiped CICD connections: %+v", after)
	}
	if decryptSecret(after[0].Token) != secret {
		t.Fatalf("token lost after Set: %q", after[0].Token)
	}
}

// TestSetCICDSyncResultUpdatesMemory pins the poll-status contract after we
// stopped persisting on every viewer GET (which raced admin config writes).
func TestSetCICDSyncResultUpdatesMemory(t *testing.T) {
	cfg := newTestConfigStore(t)
	saved, err := cfg.AddCICDConnection(CICDConnection{
		Name: "g", Provider: CICDProviderGitLab, Project: "a/b",
		BaseURL: "https://gitlab.example", Token: "t", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetCICDSyncResult(saved.ID, "boom")
	got, ok := cfg.GetCICDConnection(saved.ID)
	if !ok || got.LastError != "boom" || got.LastSyncAt == 0 {
		t.Fatalf("memory update missing: %+v", got)
	}
}

func TestCICDHTTPClientBlocksMetadataSSRF(t *testing.T) {
	c := CICDConnection{
		Provider: CICDProviderGitLab,
		BaseURL:  "http://169.254.169.254",
		Project:  "a/b",
		Token:    "t",
	}
	client, err := cicdHTTPClient(c, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected SSRF dial rejection for cloud metadata")
	}
	if !strings.Contains(err.Error(), "SSRF") && !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("unexpected error: %v", err)
	}
}
