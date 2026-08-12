package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aiops-monitor/shared"
)

func scopedOperatorServer(t *testing.T, allowedHost string, folders []HostFolderNode, assign map[string]string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), desk: newDeskManager(), forward: newForwardManager(cfg)}
	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	_, _ = store.UpsertAuthenticated(shared.Report{
		HostID: "host-a", Hostname: "alpha", Fingerprint: "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Metrics: shared.Metrics{CPUPercent: 10},
	}, "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, _ = store.UpsertAuthenticated(shared.Report{
		HostID: "host-b", Hostname: "beta", Fingerprint: "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Metrics: shared.Metrics{CPUPercent: 20},
	}, "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	cfg.cfg.HostFolders = folders
	cfg.cfg.HostFolderAssign = assign
	salt := genToken()[:16]
	op := AccountConfig{
		Username: "scoped", DisplayName: "Scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
	}
	if allowedHost != "" {
		op.AllowedHostIDs = []string{allowedHost}
	}
	for _, f := range folders {
		if f.Name == "allowed" {
			op.AllowedFolderIDs = []string{f.ID}
		}
	}
	cfg.cfg.Users = append(cfg.cfg.Users, op)
	_ = cfg.save()
	return s, s.auth.issueSession("scoped")
}

func withSessionCookie(req *http.Request, tok string) *http.Request {
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	return req
}

func TestHostFolderTreeWriteAdminOnly(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/host-folders", nil)
	if s.routeAllowed(req, RoleOperator) {
		t.Fatal("operator must not rewrite host-folder tree")
	}
	if !s.routeAllowed(req, RoleAdmin) {
		t.Fatal("admin must rewrite host-folder tree")
	}
	if !s.routeAllowed(httptest.NewRequest(http.MethodGet, "/api/v1/host-folders", nil), RoleViewer) {
		t.Fatal("viewer may GET host-folders")
	}
}

func TestHostFolderPutCannotEscalateFolderScope(t *testing.T) {
	// Even if an older build allowed operator PUT, nesting a secret folder under
	// an allowed one must not expand access. Tree writes are now admin-only;
	// verify route gate + that descendant grants would have expanded without it.
	folders := []HostFolderNode{
		{ID: "hf-allowed", Name: "allowed"},
		{ID: "hf-secret", Name: "secret"},
	}
	s, tok := scopedOperatorServer(t, "", folders, map[string]string{
		"host-a": "hf-allowed",
		"host-b": "hf-secret",
	})
	u, _ := s.cfg.UserByName("scoped")
	if s.userCanAccessHost(u, "host-b") {
		t.Fatal("host-b must start out of scope")
	}
	// Simulate the attack payload nesting secret under allowed.
	attack := []HostFolderNode{{
		ID: "hf-allowed", Name: "allowed",
		Children: []HostFolderNode{{ID: "hf-secret", Name: "secret"}},
	}}
	body, _ := json.Marshal(map[string]any{"folders": attack})
	rr := httptest.NewRecorder()
	req := withSessionCookie(httptest.NewRequest(http.MethodPut, "/api/v1/host-folders", bytes.NewReader(body)), tok)
	req.Header.Set("Content-Type", "application/json")
	mw := s.authMiddleware(http.HandlerFunc(s.handlePutHostFolders))
	mw.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("scoped PUT host-folders: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	if s.userCanAccessHost(u, "host-b") {
		t.Fatal("host-b must remain out of scope after rejected PUT")
	}
}

func TestDesktopSessionHostScopeRBAC(t *testing.T) {
	s, tok := scopedOperatorServer(t, "host-a", nil, nil)
	live := s.desk.create("host-b", "beta", "admin", "127.0.0.1", "zh")
	live.recordFrame("jpeg", []byte{0xff, 0xd8, 0xff})
	s.desk.archiveSession(live)

	rr := httptest.NewRecorder()
	req := withSessionCookie(httptest.NewRequest(http.MethodGet, "/api/v1/desktop/sessions", nil), tok)
	s.handleListDesktopSessions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d", rr.Code)
	}
	var listed []deskSessionInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, sess := range listed {
		if sess.HostID == "host-b" {
			t.Fatal("scoped operator must not list out-of-scope desktop sessions")
		}
	}

	rr = httptest.NewRecorder()
	req = withSessionCookie(httptest.NewRequest(http.MethodGet, "/api/v1/desktop/sessions/"+live.id+"/replay", nil), tok)
	req.SetPathValue("id", live.id)
	s.handleDesktopReplay(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("replay out-of-scope: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestForwardDeleteHostScopeRBAC(t *testing.T) {
	s, tok := scopedOperatorServer(t, "host-a", nil, nil)
	rule, err := s.forward.createRule("host-b", "beta", 3306, 13306, "127.0.0.1", "tcp", "", "admin", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := withSessionCookie(httptest.NewRequest(http.MethodDelete, "/api/v1/forward/"+rule.id, nil), tok)
	req.SetPathValue("id", rule.id)
	s.handleForwardDelete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("delete out-of-scope forward: want 403 got %d", rr.Code)
	}
	if s.forward.getRule(rule.id) == nil {
		t.Fatal("out-of-scope forward must not be deleted")
	}
}

func TestHardwareHostScopeRBAC(t *testing.T) {
	s, tok := scopedOperatorServer(t, "host-a", nil, nil)
	rr := httptest.NewRecorder()
	req := withSessionCookie(httptest.NewRequest(http.MethodGet, "/api/v1/hardware/health?host=host-b", nil), tok)
	s.handleHardwareHealth(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("hardware health out-of-scope: want 403 got %d", rr.Code)
	}
}

func TestGetHostFoldersHidesOutOfScopeAssign(t *testing.T) {
	folders := []HostFolderNode{
		{ID: "hf-a", Name: "a"},
		{ID: "hf-b", Name: "b"},
	}
	s, tok := scopedOperatorServer(t, "host-a", folders, map[string]string{
		"host-a": "hf-a",
		"host-b": "hf-b",
	})
	rr := httptest.NewRecorder()
	req := withSessionCookie(httptest.NewRequest(http.MethodGet, "/api/v1/host-folders", nil), tok)
	s.handleGetHostFolders(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var body struct {
		Assign map[string]string `json:"assign"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body.Assign["host-b"]; ok {
		t.Fatalf("assign leaked host-b: %v", body.Assign)
	}
	if body.Assign["host-a"] != "hf-a" {
		t.Fatalf("host-a assign=%q", body.Assign["host-a"])
	}
}

func TestResourceSearchHostScopeRBAC(t *testing.T) {
	s, tok := scopedOperatorServer(t, "host-a", nil, nil)
	rr := httptest.NewRecorder()
	req := withSessionCookie(httptest.NewRequest(http.MethodGet, "/api/v1/resources/search?q=host", nil), tok)
	s.handleResourceSearch(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var body struct {
		Results []ResourceSearchResult `json:"results"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, res := range body.Results {
		if res.HostID == "host-b" || strings.Contains(res.Ref, "host-b") {
			t.Fatalf("search leaked host-b: %+v", res)
		}
	}
}

func TestCleanupDuplicatesHostScopeRBAC(t *testing.T) {
	s, tok := scopedOperatorServer(t, "host-a", nil, nil)
	// Two hosts same fingerprint: host-b is stale offline duplicate of a third identity.
	_ = s.store.RegisterHost("host-b2", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	h, _ := s.store.GetHost("host-b")
	h.LastSeen = time.Now().Unix() - 86400
	h2, _ := s.store.GetHost("host-b2")
	h2.LastSeen = time.Now().Unix()

	rr := httptest.NewRecorder()
	req := withSessionCookie(httptest.NewRequest(http.MethodPost, "/api/v1/hosts/duplicates/cleanup", nil), tok)
	s.handleCleanupDuplicates(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := s.store.GetHost("host-b"); !ok {
		t.Fatal("scoped cleanup must not delete out-of-scope stale host-b")
	}
}
