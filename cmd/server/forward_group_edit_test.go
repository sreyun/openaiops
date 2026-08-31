package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// Group edit used to delete every rule then recreate while (1) hardcoding
// remoteTarget="" and (2) skipping out-of-range ports with continue — so a
// successful-looking edit permanently wiped jump targets and could drop ports.
func TestForwardGroupEditPreservesRemoteTargetAndRejectsOverflow(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), forward: newForwardManager(cfg)}
	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, _ = store.UpsertAuthenticated(shared.Report{
		HostID: "host-a", Hostname: "alpha", Fingerprint: "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	salt := genToken()[:16]
	cfg.cfg.Users = []AccountConfig{{
		Username: "admin", DisplayName: "Admin", Role: RoleAdmin,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
	}}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	tok := s.auth.issueSession("admin")

	gid := "g-edit1"
	jump := "192.168.30.220:3306"
	var ids []string
	for _, p := range []int{65530, 65531, 65532} {
		rule, err := s.forward.createRule("host-a", "alpha", p, 0, "127.0.0.1", "tcp", gid, "admin", jump, false, nil)
		if err != nil {
			t.Fatalf("create %d: %v", p, err)
		}
		ids = append(ids, rule.id)
	}
	t.Cleanup(func() {
		for _, id := range s.forward.groupRuleIDs(gid) {
			s.forward.removeRule(id)
		}
		for _, id := range ids {
			s.forward.removeRule(id)
		}
	})

	callEdit := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/forward/group/"+gid+"/edit", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		req.SetPathValue("gid", gid)
		s.handleForwardGroupEdit(rr, req)
		return rr
	}

	// Overflow shift must be rejected up front — rules stay intact.
	rr := callEdit(`{"host_id":"host-a","target_port":65534}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("overflow edit: want 400 got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := s.forward.groupRuleIDs(gid); len(got) != 3 {
		t.Fatalf("overflow must not delete rules: got %d", len(got))
	}
	for _, id := range s.forward.groupRuleIDs(gid) {
		r := s.forward.getRule(id)
		if r == nil || r.remoteTarget != jump {
			t.Fatalf("overflow path must keep remote_target %q, rule=%+v", jump, r)
		}
	}

	// In-range host-only edit must preserve jump target on every recreated rule.
	rr = callEdit(`{"host_id":"host-a","target_port":65530}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("host edit: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Edited int           `json:"edited"`
		Rules  []forwardInfo `json:"rules"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Edited != 3 || len(resp.Rules) != 3 {
		t.Fatalf("edited=%d rules=%d want 3", resp.Edited, len(resp.Rules))
	}
	for _, info := range resp.Rules {
		if info.RemoteTarget != jump {
			t.Fatalf("remote_target wiped on group edit: %+v", info)
		}
	}
	persisted := cfg.ListForwardRules()
	kept := 0
	for _, pr := range persisted {
		if pr.GroupID != gid {
			continue
		}
		kept++
		if pr.RemoteTarget != jump {
			t.Fatalf("persisted remote_target wiped: %+v", pr)
		}
	}
	if kept != 3 {
		t.Fatalf("persisted group size %d want 3", kept)
	}
}
