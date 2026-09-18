package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// Desktop/terminal session list + replay (+ observe) previously ignored AllowedHostIDs
// while live open/WS already called requireHostAccess — a scoped operator could
// enumerate foreign host_id/session meta and pull screen/shell recordings by id.
func TestDesktopTerminalSessionsRespectHostScope(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{
		cfg: cfg, store: store, auth: NewAuth(cfg),
		desk: newDeskManager(), term: newTermManager(),
	}
	s.desk.setRecDir(filepath.Join(dir, "desk"))
	s.term.recDir = filepath.Join(dir, "term")

	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	for _, h := range []struct{ id, name, fp string }{
		{"host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	} {
		_, _ = store.UpsertAuthenticated(shared.Report{HostID: h.id, Hostname: h.name, Fingerprint: h.fp}, h.fp)
	}

	salt := genToken()[:16]
	reqTermPW := false
	cfg.cfg.RequireTerminalPassword = &reqTermPW
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

	deskA := s.desk.create("host-a", "alpha", "op", "10.0.0.1", "zh")
	deskB := s.desk.create("host-b", "beta", "op", "10.0.0.2", "zh")
	deskA.recordFrame("meta", []byte(`{"w":1}`))
	deskB.recordFrame("meta", []byte(`{"w":2}`))

	termA := s.term.create("host-a", "alpha", "op")
	termB := s.term.create("host-b", "beta", "op")
	termA.ip = "10.0.0.1"
	termB.ip = "10.0.0.2"
	termA.recordFrame("out", []byte("a-out"))
	termB.recordFrame("out", []byte("b-out"))

	// List desktop: only host-a
	{
		req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/desktop/sessions", nil))
		rr := httptest.NewRecorder()
		s.handleListDesktopSessions(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("desktop list status=%d body=%s", rr.Code, rr.Body.String())
		}
		var list []deskSessionInfo
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].HostID != "host-a" || list[0].ID != deskA.id {
			t.Fatalf("desktop list want only host-a session, got %+v", list)
		}
	}

	// Replay foreign desktop → 403
	{
		req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/desktop/sessions/"+deskB.id+"/replay", nil))
		req.SetPathValue("id", deskB.id)
		rr := httptest.NewRecorder()
		s.handleDesktopReplay(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("desktop replay foreign want 403, got %d body=%s", rr.Code, rr.Body.String())
		}
	}

	// Replay allowed desktop → 200
	{
		req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/desktop/sessions/"+deskA.id+"/replay", nil))
		req.SetPathValue("id", deskA.id)
		rr := httptest.NewRecorder()
		s.handleDesktopReplay(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("desktop replay allowed want 200, got %d body=%s", rr.Code, rr.Body.String())
		}
	}

	// List terminal: only host-a
	{
		req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/terminal/sessions", nil))
		rr := httptest.NewRecorder()
		s.handleListTerminalSessions(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("terminal list status=%d body=%s", rr.Code, rr.Body.String())
		}
		var list []termSessionInfo
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].HostID != "host-a" || list[0].ID != termA.id {
			t.Fatalf("terminal list want only host-a session, got %+v", list)
		}
	}

	// Terminal replay: secondary verify not required when policy is off and no
	// password is set; host-scope still blocks foreign sessions.
	{
		req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/terminal/sessions/"+termB.id+"/replay", nil))
		req.SetPathValue("id", termB.id)
		rr := httptest.NewRecorder()
		s.handleTerminalReplay(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("terminal replay foreign want 403, got %d body=%s", rr.Code, rr.Body.String())
		}
	}
	{
		req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/terminal/sessions/"+termA.id+"/replay", nil))
		req.SetPathValue("id", termA.id)
		rr := httptest.NewRecorder()
		s.handleTerminalReplay(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("terminal replay allowed want 200, got %d body=%s", rr.Code, rr.Body.String())
		}
	}

	// Observe foreign live session → 403 before WS upgrade
	{
		req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/terminal/sessions/"+termB.id+"/observe", nil))
		req.SetPathValue("id", termB.id)
		rr := httptest.NewRecorder()
		s.handleTerminalObserve(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("terminal observe foreign want 403, got %d body=%s", rr.Code, rr.Body.String())
		}
	}
}

// handleHosts used to pass the RBAC-filtered host list into first-time folder
// migration, so a scoped user's first GET /hosts permanently dropped other
// hosts' category folders from HostFolderAssign.
func TestHandleHostsMigratesFromAllHostsNotJustVisible(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh install: HostFolders never initialized.
	cfg.cfg.HostFolders = nil
	cfg.cfg.HostFolderAssign = nil

	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg)}
	_ = store.RegisterHost("h1", "db-01", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("h2", "web-01", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	_ = store.RegisterHost("h3", "cache-01", "fp-cccccccccccccccccccccccccccccccc")
	for _, h := range []struct{ id, name, cat, fp string }{
		{"h1", "db-01", "数据库", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"h2", "web-01", "Web", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{"h3", "cache-01", "缓存", "fp-cccccccccccccccccccccccccccccccc"},
	} {
		_, _ = store.UpsertAuthenticated(shared.Report{
			HostID: h.id, Hostname: h.name, Fingerprint: h.fp, Category: h.cat,
		}, h.fp)
	}

	salt := genToken()[:16]
	cfg.cfg.Users = append(cfg.cfg.Users, AccountConfig{
		Username: "scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
		AllowedHostIDs: []string{"h1"},
	})
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	tok := s.auth.issueSession("scoped")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	rr := httptest.NewRecorder()
	s.handleHosts(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	folders, assign := cfg.hostFoldersSnapshot()
	if len(folders) != 3 {
		t.Fatalf("migration should build 3 category folders from full fleet, got %d: %+v", len(folders), folders)
	}
	for _, id := range []string{"h1", "h2", "h3"} {
		if assign[id] == "" {
			t.Errorf("%s missing folder assignment after scoped GET /hosts", id)
		}
	}
	// Response itself remains scoped.
	var views []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("response should list only visible host, got %d", len(views))
	}
}

func TestDeskSessionHostIDFromArchive(t *testing.T) {
	m := newDeskManager()
	dir := t.TempDir()
	m.setRecDir(dir)
	s := m.create("host-z", "zeta", "op", "1.1.1.1", "en")
	s.recordFrame("jpeg", []byte{1, 2, 3})
	id := s.id
	m.remove(id)
	// Allow archive goroutine to flush.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hid, ok := m.sessionHostID(id); ok && hid == "host-z" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hid, ok := m.sessionHostID(id); !ok || hid != "host-z" {
		t.Fatalf("sessionHostID after archive = (%q,%v), want host-z", hid, ok)
	}
}
