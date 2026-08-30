package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegisterSuppressDoesNotBurnInstallTokenUses: after DeleteHost, RegisterHost
// is suppressed for ~60s. Returning 200 + ConsumeInstallTokenUse in that window
// made agents mark themselves registered and burn MaxUses on every report-cycle
// re-register — exhausting the fleet token while the host stayed deleted.
func TestRegisterSuppressDoesNotBurnInstallTokenUses(t *testing.T) {
	dir := t.TempDir()
	cs := &ConfigStore{path: filepath.Join(dir, "cfg.json"), cfg: ServerConfig{
		InstallToken:        "tok-suppress-audit-0123456789",
		InstallTokenMaxUses: 3,
	}}
	store := NewStore()
	store.RegisterHost("h-del", "node-1", "fp-del")
	s := &Server{cfg: cs, store: store}

	if !store.DeleteHost("h-del") {
		t.Fatal("delete failed")
	}

	body := `{"host_id":"h-del","hostname":"node-1","token":"tok-suppress-audit-0123456789","fingerprint":"fp-del"}`
	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/register", strings.NewReader(body))
		s.handleRegister(rr, req)
		if rr.Code != http.StatusConflict {
			t.Fatalf("attempt %d: want 409 during suppress, got %d body=%s", i, rr.Code, rr.Body.String())
		}
		if got := len(store.ListHosts()); got != 0 {
			t.Fatalf("attempt %d: host should stay deleted during suppress, got %d hosts", i, got)
		}
	}
	cs.mu.RLock()
	got := cs.cfg.InstallTokenUseCount
	cs.mu.RUnlock()
	if got != 0 {
		t.Fatalf("useCount=%d want 0 (suppress must not burn token uses)", got)
	}
	if !cs.ValidInstallToken("tok-suppress-audit-0123456789") {
		t.Fatal("token must still be valid after suppressed register attempts")
	}

	// After the suppress window, registration should succeed and consume one use.
	store.mu.Lock()
	store.deleted["h-del"] = 0 // expire suppress (unix 0 → age >> deleteSuppressSec)
	store.mu.Unlock()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/register", strings.NewReader(body))
	s.handleRegister(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("post-suppress register: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["status"] != "ok" {
		t.Fatalf("want status ok, got %v", resp)
	}
	if _, ok := store.GetHost("h-del"); !ok {
		t.Fatal("host should be enrolled after suppress expires")
	}
	cs.mu.RLock()
	got = cs.cfg.InstallTokenUseCount
	cs.mu.RUnlock()
	if got != 1 {
		t.Fatalf("useCount=%d want 1 after successful re-enroll", got)
	}
}
