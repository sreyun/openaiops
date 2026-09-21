package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// Scoped operators must not delete foreign hosts / Hyper-V inventories or wipe
// foreign folder assignments via bulk tree ops.

func scopedMutateFixture(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), hv: newHypervStore()}

	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	for _, h := range []struct{ id, name, fp string }{
		{"host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	} {
		_, _ = store.UpsertAuthenticated(shared.Report{
			HostID: h.id, Hostname: h.name, Fingerprint: h.fp,
			Metrics: shared.Metrics{CPUPercent: 1},
		}, h.fp)
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
	return s, s.auth.issueSession("scoped")
}

func withScopedSession(tok string, req *http.Request) *http.Request {
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	return req
}

func TestCleanupDuplicatesRespectsHostScope(t *testing.T) {
	s, tok := scopedMutateFixture(t)
	now := time.Now().Unix()
	// Same fingerprint as host-b: stale offline twin outside the caller's scope.
	s.store.mu.Lock()
	s.store.hosts["host-b-old"] = &Host{
		ID: "host-b-old", Hostname: "beta", Fingerprint: "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		LastSeen: now - 86400,
	}
	// Also a stale twin of host-a that the scoped operator SHOULD be allowed to clean.
	s.store.hosts["host-a-old"] = &Host{
		ID: "host-a-old", Hostname: "alpha", Fingerprint: "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LastSeen: now - 86400,
	}
	s.store.mu.Unlock()

	// Grant the old-a id so cleanup can see it as in-scope; foreign old-b stays denied.
	for i := range s.cfg.cfg.Users {
		if s.cfg.cfg.Users[i].Username == "scoped" {
			s.cfg.cfg.Users[i].AllowedHostIDs = []string{"host-a", "host-a-old"}
		}
	}

	rr := httptest.NewRecorder()
	req := withScopedSession(tok, httptest.NewRequest(http.MethodPost, "/api/v1/hosts/duplicates/cleanup", nil))
	s.handleCleanupDuplicates(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cleanup: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Deleted []string `json:"deleted"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, id := range body.Deleted {
		if id == "host-b-old" || id == "host-b" {
			t.Fatalf("deleted out-of-scope host %q: %v", id, body.Deleted)
		}
	}
	if _, ok := s.store.GetHost("host-b-old"); !ok {
		t.Fatal("foreign stale duplicate host-b-old was deleted")
	}
	if _, ok := s.store.GetHost("host-a-old"); ok {
		t.Fatal("in-scope stale duplicate host-a-old should have been cleaned")
	}
	if _, ok := s.store.GetHost("host-b"); !ok {
		t.Fatal("current foreign host-b must survive")
	}
}

func TestDeleteHyperVRespectsHostScope(t *testing.T) {
	s, tok := scopedMutateFixture(t)
	s.hv.put("host-b", "beta", "", []shared.HyperVGuest{{ID: "g1", Name: "vm1", State: "Running"}}, "", 1, 1)

	rr := httptest.NewRecorder()
	req := withScopedSession(tok, httptest.NewRequest(http.MethodDelete, "/api/v1/hyperv/host-b", nil))
	req.SetPathValue("hostID", "host-b")
	s.handleDeleteHyperV(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("delete foreign hyperv: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	if total, _ := s.hv.hostMemOf("host-b"); total == 0 {
		// hostMemOf returns 0 when missing; guests presence is better via listing internals.
		t.Fatal("foreign Hyper-V inventory was wiped despite 403")
	}

	rr = httptest.NewRecorder()
	req = withScopedSession(tok, httptest.NewRequest(http.MethodDelete, "/api/v1/hyperv/host-a", nil))
	req.SetPathValue("hostID", "host-a")
	s.handleDeleteHyperV(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete own hyperv: want 200 got %d", rr.Code)
	}
}

func TestPutHostFoldersRespectsHostScope(t *testing.T) {
	s, tok := scopedMutateFixture(t)
	folders := []HostFolderNode{
		{ID: "fold-a", Name: "A"},
		{ID: "fold-b", Name: "B"},
	}
	if err := s.cfg.setHostFoldersTree(folders); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.assignHostFolder("host-a", "fold-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.assignHostFolder("host-b", "fold-b"); err != nil {
		t.Fatal(err)
	}

	// Empty tree would drop every assignment, including foreign host-b.
	rr := httptest.NewRecorder()
	req := withScopedSession(tok, httptest.NewRequest(http.MethodPut, "/api/v1/host-folders",
		strings.NewReader(`{"folders":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	s.handlePutHostFolders(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("empty tree put: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	_, assign := s.cfg.hostFoldersSnapshot()
	if assign["host-b"] != "fold-b" {
		t.Fatalf("foreign assignment wiped: %v", assign)
	}
	if assign["host-a"] != "fold-a" {
		t.Fatalf("own assignment changed on rejected put: %v", assign)
	}
}

func TestDeleteHostFolderRespectsHostScope(t *testing.T) {
	s, tok := scopedMutateFixture(t)
	folders := []HostFolderNode{
		{ID: "fold-shared", Name: "Shared"},
	}
	if err := s.cfg.setHostFoldersTree(folders); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.assignHostFolder("host-a", "fold-shared"); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.assignHostFolder("host-b", "fold-shared"); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := withScopedSession(tok, httptest.NewRequest(http.MethodDelete, "/api/v1/host-folders/fold-shared", nil))
	req.SetPathValue("id", "fold-shared")
	s.handleDeleteHostFolder(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("delete folder with foreign hosts: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	_, assign := s.cfg.hostFoldersSnapshot()
	if assign["host-b"] != "fold-shared" || assign["host-a"] != "fold-shared" {
		t.Fatalf("assignments changed on rejected delete: %v", assign)
	}
}
