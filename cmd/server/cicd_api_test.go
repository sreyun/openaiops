package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cicdTestServer builds a real Server with a throwaway ConfigStore so the CI/CD
// handlers run against genuine persistence + masking logic.
func cicdTestServer(t *testing.T) *Server {
	t.Helper()
	store := NewStore()
	cfg := newTestConfigStore(t)
	return NewServer(store, cfg, NewNotifier(store, cfg), t.TempDir(), "127.0.0.1:0")
}

func cicdDo(t *testing.T, h http.HandlerFunc, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

// TestCICDConnectionCRUDMasksToken walks create → list → update → delete through
// the real handlers and asserts the access token never leaves the server.
func TestCICDConnectionCRUDMasksToken(t *testing.T) {
	s := cicdTestServer(t)
	const secret = "glpat-SUPERSECRETTOKEN123"

	rr := cicdDo(t, s.handleCICDConnectionCreate, http.MethodPost, "/api/v1/cicd/connections", map[string]any{
		"name": "corp gitlab", "provider": "gitlab",
		"base_url": "https://gitlab.corp.example", "project": "infra/platform/api",
		"token": secret, "ref": "main", "enabled": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Fatal("create response leaked the raw token")
	}
	var created CICDConnection
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create returned no id")
	}

	// The stored value must be sealed, not plaintext.
	stored, ok := s.cfg.GetCICDConnection(created.ID)
	if !ok {
		t.Fatal("connection not persisted")
	}
	if stored.Token == "" {
		t.Fatal("token not persisted")
	}
	if decryptSecret(stored.Token) != secret {
		t.Fatal("stored token does not round-trip through decryptSecret")
	}

	rr = cicdDo(t, s.handleCICDConnectionList, http.MethodGet, "/api/v1/cicd/connections", nil)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), secret) {
		t.Fatalf("list leaked token or failed: %d %s", rr.Code, rr.Body)
	}

	// Updating with the masked token must keep the stored secret.
	upd := httptest.NewRequest(http.MethodPut, "/api/v1/cicd/connections/"+created.ID,
		strings.NewReader(`{"name":"renamed","provider":"gitlab","project":"infra/platform/api","token":"glpa****n123","enabled":true}`))
	upd.SetPathValue("id", created.ID)
	rr = httptest.NewRecorder()
	s.handleCICDConnectionUpdate(rr, upd)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rr.Code, rr.Body)
	}
	after, _ := s.cfg.GetCICDConnection(created.ID)
	if decryptSecret(after.Token) != secret {
		t.Fatal("masked update overwrote the stored token")
	}
	if after.Name != "renamed" {
		t.Fatalf("update did not apply: %+v", after)
	}

	del := httptest.NewRequest(http.MethodDelete, "/api/v1/cicd/connections/"+created.ID, nil)
	del.SetPathValue("id", created.ID)
	rr = httptest.NewRecorder()
	s.handleCICDConnectionDelete(rr, del)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status=%d", rr.Code)
	}
	if _, ok := s.cfg.GetCICDConnection(created.ID); ok {
		t.Fatal("connection still present after delete")
	}
}

func TestCICDConnectionCreateRejectsInvalid(t *testing.T) {
	s := cicdTestServer(t)
	rr := cicdDo(t, s.handleCICDConnectionCreate, http.MethodPost, "/api/v1/cicd/connections", map[string]any{
		"name": "x", "provider": "subversion", "project": "a/b",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad provider accepted: %d %s", rr.Code, rr.Body)
	}
}

// Route registration is covered by frontend/scripts/check-api-contract.mjs, which
// parses every mux pattern out of the Go source and matches it against the calls
// the console makes (it went 499 → 512 routes when these landed). Server.Routes()
// returns a middleware-wrapped http.Handler, so it cannot be introspected here.

// TestCICDRouteRBAC pins the permission model: reads viewer+, pipeline control
// operator+, and connection CRUD (which holds SCM tokens) admin-only.
func TestCICDRouteRBAC(t *testing.T) {
	s := cicdTestServer(t)
	cases := []struct {
		method, path string
		role         string
		want         bool
	}{
		{http.MethodGet, "/api/v1/cicd/runs", RoleViewer, true},
		{http.MethodGet, "/api/v1/cicd/overview", RoleViewer, true},
		{http.MethodGet, "/api/v1/cicd/connections", RoleViewer, true},

		{http.MethodPost, "/api/v1/cicd/trigger", RoleViewer, false},
		{http.MethodPost, "/api/v1/cicd/trigger", RoleOperator, true},
		{http.MethodPost, "/api/v1/cicd/runs/1/retry", RoleOperator, true},
		{http.MethodPost, "/api/v1/cicd/runs/1/cancel", RoleOperator, true},
		{http.MethodPost, "/api/v1/cicd/runs/1/diagnose", RoleOperator, true},

		// Tokens for the SCM live here — operators must not read/write them.
		{http.MethodPost, "/api/v1/cicd/connections", RoleOperator, false},
		{http.MethodPost, "/api/v1/cicd/connections", RoleAdmin, true},
		{http.MethodPut, "/api/v1/cicd/connections/x", RoleOperator, false},
		{http.MethodPut, "/api/v1/cicd/connections/x", RoleAdmin, true},
		{http.MethodDelete, "/api/v1/cicd/connections/x", RoleOperator, false},
		{http.MethodDelete, "/api/v1/cicd/connections/x", RoleAdmin, true},
		{http.MethodPost, "/api/v1/cicd/connections/test", RoleOperator, false},
		{http.MethodPost, "/api/v1/cicd/connections/test", RoleAdmin, true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if got := s.routeAllowed(req, tc.role); got != tc.want {
			t.Errorf("%s %s as %s = %v, want %v", tc.method, tc.path, tc.role, got, tc.want)
		}
	}
}

func TestCICDRunsUnknownConnection404(t *testing.T) {
	s := cicdTestServer(t)
	rr := cicdDo(t, s.handleCICDRuns, http.MethodGet, "/api/v1/cicd/runs?connection_id=nope", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
}

// With no connections configured the aggregate endpoints must degrade to empty
// results rather than erroring — the page renders its empty state from these.
func TestCICDAggregatesEmptyWithoutConnections(t *testing.T) {
	s := cicdTestServer(t)

	rr := cicdDo(t, s.handleCICDRuns, http.MethodGet, "/api/v1/cicd/runs", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("runs status=%d", rr.Code)
	}
	var runsResp struct {
		Runs []CICDRun `json:"runs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &runsResp); err != nil {
		t.Fatalf("runs decode: %v", err)
	}
	if len(runsResp.Runs) != 0 {
		t.Fatalf("expected no runs, got %d", len(runsResp.Runs))
	}

	rr = cicdDo(t, s.handleCICDOverview, http.MethodGet, "/api/v1/cicd/overview", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("overview status=%d", rr.Code)
	}
	var ov map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &ov); err != nil {
		t.Fatalf("overview decode: %v", err)
	}
	for _, k := range []string{"connections", "enabled", "running", "failed", "success_rate"} {
		if _, ok := ov[k]; !ok {
			t.Errorf("overview missing %q — the KPI cards read it", k)
		}
	}
}

// The runs endpoint must report a broken connection per-connection instead of
// failing the whole page: one dead self-hosted GitLab cannot blank the console.
func TestCICDRunsReportsPerConnectionErrors(t *testing.T) {
	s := cicdTestServer(t)
	saved, err := s.cfg.AddCICDConnection(CICDConnection{
		Name: "dead", Provider: CICDProviderGitLab,
		// Reserved TEST-NET-1 address: guaranteed not to answer.
		BaseURL: "http://192.0.2.1:9", Project: "a/b", Enabled: true,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := cicdDo(t, s.handleCICDRuns, http.MethodGet, "/api/v1/cicd/runs", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (degraded, not failed)", rr.Code)
	}
	var resp struct {
		Runs   []CICDRun         `json:"runs"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Errors[saved.ID] == "" {
		t.Fatalf("expected an error entry for the dead connection, got %+v", resp.Errors)
	}
	// And the failure is recorded for the connections table's status column.
	after, _ := s.cfg.GetCICDConnection(saved.ID)
	if after.LastError == "" {
		t.Error("LastError not recorded on the connection")
	}
}
