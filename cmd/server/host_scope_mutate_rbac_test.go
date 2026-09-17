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

func scopedOperatorServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{
		cfg:          cfg,
		store:        store,
		auth:         NewAuth(cfg),
		playbooks:    newPlaybookManager(cfg),
		agentUpdates: newAgentUpdateManager(),
		secFindings:  newSecurityFindingManager(filepath.Join(dir, "sec-findings")),
	}
	ha := store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	hb := store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	now := time.Now().Unix()
	ha.LastSeen, hb.LastSeen = now, now
	ha.OS, hb.OS = "linux", "linux"

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

func TestPlaybookApproveRejectRespectHostScope(t *testing.T) {
	s, tok := scopedOperatorServer(t)
	pb, err := s.playbooks.Upsert(Playbook{
		Name: "restart-foreign",
		Steps: []PlaybookStep{{
			Name: "restart", Module: "service", Target: "all", TimeoutSec: 30,
			Args: map[string]string{"name": "nginx", "state": "restarted"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := s.store.GetHost("host-b")
	exec := s.playbooks.StartPendingExecution(pb, "scheduler", []*Host{hb}, "test")
	idStr := strconv.FormatInt(exec.ID, 10)

	withSession := func(req *http.Request) *http.Request {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		return req
	}

	rr := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/api/v1/playbooks/executions/"+idStr+"/approve", nil))
	req.SetPathValue("id", idStr)
	s.handleApprovePlaybookExecution(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("approve foreign pending exec: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	got, ok := s.playbooks.GetExecution(exec.ID)
	if !ok || got.Status != "pending_approval" {
		t.Fatalf("execution should stay pending_approval, got ok=%v status=%q", ok, got.Status)
	}

	rr = httptest.NewRecorder()
	req = withSession(httptest.NewRequest(http.MethodPost, "/api/v1/playbooks/executions/"+idStr+"/reject", nil))
	req.SetPathValue("id", idStr)
	s.handleRejectPlaybookExecution(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("reject foreign pending exec: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAgentUpdateStartRespectsHostScope(t *testing.T) {
	s, tok := scopedOperatorServer(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/update",
		strings.NewReader(`{"confirm":true,"host_ids":["host-b"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	s.handleAgentUpdateStart(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("OTA push to out-of-scope host: want 400 got %d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/agents/update",
		strings.NewReader(`{"confirm":true,"all":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	s.handleAgentUpdateStart(rr, req)
	if rr.Code != http.StatusAccepted && rr.Code != http.StatusForbidden {
		t.Fatalf("all:true for scoped user: unexpected %d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Code == http.StatusAccepted {
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		// Snapshot shape varies; ensure host-b was not queued by inspecting jobs.
		jobs := s.agentUpdates.list(10)
		for _, j := range jobs {
			for _, h := range j.Hosts {
				if h.HostID == "host-b" {
					t.Fatalf("all:true queued out-of-scope host-b in job %s", j.ID)
				}
			}
		}
	}
}

func TestSecurityFindingUpdateRespectsHostKey(t *testing.T) {
	s, tok := scopedOperatorServer(t)
	key := hostFindingKey("host-b", HostFinding{ID: "cve-1", Category: "cve", Title: "x", Detail: "d"})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/security/findings/state",
		strings.NewReader(`{"key":"`+key+`","scope":"host","status":"resolved"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	s.handleUpdateSecurityFindingState(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("resolve foreign finding via key only: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
}
