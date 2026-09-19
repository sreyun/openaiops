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
)

func playbookExecScopedServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{
		cfg:       cfg,
		store:     store,
		auth:      NewAuth(cfg),
		playbooks: newPlaybookManager(cfg),
	}
	ha := store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	hb := store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	now := time.Now().Unix()
	ha.LastSeen, hb.LastSeen = now, now

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

func TestPlaybookExecutionListGetCancelRespectHostScope(t *testing.T) {
	s, tok := playbookExecScopedServer(t)
	pb, err := s.playbooks.Upsert(Playbook{
		Name: "diag",
		Steps: []PlaybookStep{{
			Name: "uname", Command: "uname -a", Target: "all", TimeoutSec: 30,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ha, _ := s.store.GetHost("host-a")
	hb, _ := s.store.GetHost("host-b")
	local := s.playbooks.StartExecution(pb, "admin", []*Host{ha})
	foreign := s.playbooks.StartExecution(pb, "admin", []*Host{hb})
	s.playbooks.UpdateHostResult(foreign.ID, "host-b", HostExecResult{
		Hostname: "beta", Status: "success",
		Output: "SECRET_FROM_HOST_B",
		Steps:  []StepResult{{Name: "uname", Status: "success", Output: "SECRET_FROM_HOST_B"}},
	})
	s.playbooks.SetExecutionStatus(foreign.ID, "running", false)

	withSession := func(req *http.Request) *http.Request {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		return req
	}

	rr := httptest.NewRecorder()
	s.handleListExecutions(rr, withSession(httptest.NewRequest(http.MethodGet, "/api/v1/playbooks/executions", nil)))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}
	var listed []PlaybookExecution
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, e := range listed {
		if e.ID == foreign.ID {
			t.Fatalf("list leaked foreign execution %d", foreign.ID)
		}
		if _, ok := e.HostResults["host-b"]; ok {
			t.Fatal("list leaked host-b results")
		}
	}
	foundLocal := false
	for _, e := range listed {
		if e.ID == local.ID {
			foundLocal = true
		}
	}
	if !foundLocal {
		t.Fatal("in-scope execution missing from list")
	}

	rr = httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/playbooks/executions/by-id/"+strconv.FormatInt(foreign.ID, 10), nil))
	req.SetPathValue("id", strconv.FormatInt(foreign.ID, 10))
	s.handleGetExecution(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("get foreign: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "SECRET_FROM_HOST_B") {
		t.Fatal("get must not leak foreign output even on error body")
	}

	rr = httptest.NewRecorder()
	req = withSession(httptest.NewRequest(http.MethodPost, "/api/v1/playbooks/executions/by-id/"+strconv.FormatInt(foreign.ID, 10)+"/cancel", nil))
	req.SetPathValue("id", strconv.FormatInt(foreign.ID, 10))
	s.handleCancelPlaybookExecution(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cancel foreign: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	got, ok := s.playbooks.GetExecution(foreign.ID)
	if !ok || got.Status != "running" {
		t.Fatalf("foreign exec should stay running, ok=%v status=%q", ok, got.Status)
	}
}
