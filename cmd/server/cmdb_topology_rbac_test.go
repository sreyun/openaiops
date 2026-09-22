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

func TestUpsertBusinessServiceDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := cfg.UpsertBusinessService(BusinessService{Name: "checkout", HostIDs: []string{"h1"}})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("UpsertBusinessService deadlocked (held write lock across save RLock)")
	}
	// A subsequent config read must also complete — proves the mutex was released.
	if n := len(cfg.BusinessServices()); n != 1 {
		t.Fatalf("BusinessServices len=%d want 1", n)
	}
}

func TestCMDBTopologyAPIMonCheckHostScope(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	checks := newCheckRunner(cfg, store, nil, "")
	apimon := newAPIRunner(checks, cfg, store, nil, newVMWriter(cfg))
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), incidents: newIncidentManager(), checks: checks, apimon: apimon}

	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	_, _ = store.UpsertAuthenticated(shared.Report{
		HostID: "host-a", Hostname: "alpha", Fingerprint: "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Metrics: shared.Metrics{CPUPercent: 10},
	}, "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, _ = store.UpsertAuthenticated(shared.Report{
		HostID: "host-b", Hostname: "beta", Fingerprint: "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Metrics: shared.Metrics{CPUPercent: 90, ProcessNames: []string{"sshd", "nginx"}},
	}, "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	salt := genToken()[:16]
	op := AccountConfig{
		Username: "scoped", DisplayName: "Scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
		AllowedHostIDs: []string{"host-a"},
	}
	cfg.cfg.Users = append(cfg.cfg.Users, op)
	_ = cfg.save()

	tok := s.auth.issueSession("scoped")
	withSession := func(req *http.Request) *http.Request {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		return req
	}
	post := func(path, body string, h func(http.ResponseWriter, *http.Request), pathVals map[string]string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := withSession(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range pathVals {
			req.SetPathValue(k, v)
		}
		h(rr, req)
		return rr
	}

	// 1) Business service cannot bind foreign hosts.
	rr := post("/api/v1/services", `{"name":"leak","host_ids":["host-b"]}`, s.handleUpsertBusinessService, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("service upsert foreign host: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	rr = post("/api/v1/services", `{"name":"ok","host_ids":["host-a"]}`, s.handleUpsertBusinessService, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("service upsert own host: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	// Pre-seed a foreign-bound service as admin would, then scoped impact must 403.
	foreignSvc, err := cfg.UpsertBusinessService(BusinessService{Name: "foreign", HostIDs: []string{"host-b"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.incidents.CreateManual("b-down", "critical", "host-b", "beta", "ops")
	rr = httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodGet, "/api/v1/services/"+foreignSvc.ID+"/impact", nil))
	req.SetPathValue("id", foreignSvc.ID)
	s.handleBusinessServiceImpact(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("service impact foreign: want 403 got %d", rr.Code)
	}

	// 2) Topology edge to foreign host rejected.
	rr = post("/api/v1/topology/edges", `{"from":"host:host-a","to":"host:host-b","kind":"depends_on"}`, s.handleUpsertTopologyEdge, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("topology edge foreign: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	rr = post("/api/v1/topology/edges", `{"from":"host:host-a","to":"svc:web","kind":"runs_on"}`, s.handleUpsertTopologyEdge, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("topology edge svc-only: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	// RCA must not disclose foreign same-category / related hosts.
	_ = cfg.SetCategory("host-a", "prod")
	_ = cfg.SetCategory("host-b", "prod")
	rr = httptest.NewRecorder()
	req = withSession(httptest.NewRequest(http.MethodGet, "/api/v1/topology/rca?host_id=host-a", nil))
	s.handleTopologyRCA(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rca: want 200 got %d", rr.Code)
	}
	var rca TopologyRCA
	if err := json.Unmarshal(rr.Body.Bytes(), &rca); err != nil {
		t.Fatal(err)
	}
	for _, h := range rca.RelatedHosts {
		if h.HostID == "host-b" {
			t.Fatalf("rca leaked foreign related host via same_category")
		}
	}
	for _, inc := range rca.OpenIncidents {
		if inc.HostID == "host-b" {
			t.Fatalf("rca leaked foreign incident")
		}
	}

	// 3) APIMon cannot bind foreign hosts or enable distributed probes.
	rr = post("/api/v1/apimon/systems", `{"name":"sys","host_ids":["host-b"],"endpoints":[{"name":"h","url":"http://127.0.0.1/"}]}`, s.handleUpsertAPISystem, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("apimon foreign host: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	rr = post("/api/v1/apimon/systems", `{"name":"dist","endpoints":[{"name":"h","url":"http://evil/","distributed":true}]}`, s.handleUpsertAPISystem, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("apimon distributed: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	rr = post("/api/v1/apimon/systems", `{"name":"ok","host_ids":["host-a"],"endpoints":[{"name":"h","url":"http://127.0.0.1/"}]}`, s.handleUpsertAPISystem, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("apimon own host: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}

	// Pre-seed foreign HostIDs system; hosts endpoint must 403.
	foreignSys, err := cfg.UpsertAPISystem(APISystem{
		Name: "foreign-sys", HostIDs: []string{"host-b"},
		Endpoints: []APIEndpoint{{Name: "h", URL: "http://127.0.0.1/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	req = withSession(httptest.NewRequest(http.MethodGet, "/api/v1/apimon/systems/"+foreignSys.ID+"/hosts", nil))
	req.SetPathValue("id", foreignSys.ID)
	s.handleAPISystemHosts(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("apimon hosts foreign: want 403 got %d", rr.Code)
	}

	// 4) Process check cannot target foreign host.
	rr = post("/api/v1/checks", `{"name":"p","type":"process","target":"host-b/sshd"}`, s.handleUpsertCheck, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("process check foreign: want 403 got %d body=%s", rr.Code, rr.Body.String())
	}
	rr = post("/api/v1/checks", `{"name":"p","type":"process","target":"host-a/sshd"}`, s.handleUpsertCheck, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("process check own: want 200 got %d body=%s", rr.Code, rr.Body.String())
	}
}
