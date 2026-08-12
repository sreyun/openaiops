package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeCICDStatus(t *testing.T) {
	cases := map[string]string{
		"success": CICDStatusSuccess, "passed": CICDStatusSuccess,
		"failed": CICDStatusFailed, "failure": CICDStatusFailed, "timed_out": CICDStatusFailed,
		"canceled": CICDStatusCanceled, "cancelled": CICDStatusCanceled,
		"running": CICDStatusRunning, "in_progress": CICDStatusRunning,
		"created": CICDStatusPending, "queued": CICDStatusPending, "manual": CICDStatusPending,
		"skipped": CICDStatusSkipped,
		"":        CICDStatusUnknown, "something-else": CICDStatusUnknown,
	}
	for raw, want := range cases {
		if got := normalizeCICDStatus(CICDProviderGitLab, raw); got != want {
			t.Errorf("normalizeCICDStatus(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestGithubRunStatusUsesConclusionWhenCompleted(t *testing.T) {
	if got := githubRunStatus("completed", "failure"); got != CICDStatusFailed {
		t.Errorf("completed/failure = %q, want failed", got)
	}
	if got := githubRunStatus("completed", "success"); got != CICDStatusSuccess {
		t.Errorf("completed/success = %q, want success", got)
	}
	// While in flight the conclusion is empty and must not win.
	if got := githubRunStatus("in_progress", ""); got != CICDStatusRunning {
		t.Errorf("in_progress = %q, want running", got)
	}
	if got := githubRunStatus("queued", ""); got != CICDStatusPending {
		t.Errorf("queued = %q, want pending", got)
	}
}

func TestCICDAPIRootSelfHosted(t *testing.T) {
	cases := []struct {
		name string
		conn CICDConnection
		want string
	}{
		{"self-hosted gitlab gains /api/v4",
			CICDConnection{Provider: CICDProviderGitLab, BaseURL: "https://gitlab.corp.example"},
			"https://gitlab.corp.example/api/v4"},
		{"trailing slash trimmed",
			CICDConnection{Provider: CICDProviderGitLab, BaseURL: "https://gitlab.corp.example/"},
			"https://gitlab.corp.example/api/v4"},
		{"explicit api path preserved",
			CICDConnection{Provider: CICDProviderGitLab, BaseURL: "https://gitlab.corp.example/api/v4"},
			"https://gitlab.corp.example/api/v4"},
		{"gitlab saas default",
			CICDConnection{Provider: CICDProviderGitLab},
			"https://gitlab.com/api/v4"},
		{"github saas stays bare",
			CICDConnection{Provider: CICDProviderGitHub},
			"https://api.github.com"},
		{"github enterprise gains /api/v3",
			CICDConnection{Provider: CICDProviderGitHub, BaseURL: "https://ghe.corp.example"},
			"https://ghe.corp.example/api/v3"},
		{"gitee default",
			CICDConnection{Provider: CICDProviderGitee},
			"https://gitee.com/api/v5"},
	}
	for _, tc := range cases {
		if got := cicdAPIRoot(tc.conn); got != tc.want {
			t.Errorf("%s: cicdAPIRoot = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCICDOwnerRepoHandlesNestedGroups(t *testing.T) {
	owner, repo := cicdOwnerRepo("group/sub/app")
	if owner != "group/sub" || repo != "app" {
		t.Fatalf("nested = %q/%q, want group/sub/app", owner, repo)
	}
	owner, repo = cicdOwnerRepo("/owner/repo/")
	if owner != "owner" || repo != "repo" {
		t.Fatalf("trimmed = %q/%q, want owner/repo", owner, repo)
	}
}

func TestGiteePipelinePathTemplating(t *testing.T) {
	c := CICDConnection{Provider: CICDProviderGitee, Project: "acme/app"}
	if got := giteePipelinePath(c); got != "/repos/acme/app/gitee_go/pipelines" {
		t.Fatalf("default = %q", got)
	}
	c.PipelinePath = "enterprises/acme/gitee_go/pipelines?repo={repo}"
	if got := giteePipelinePath(c); got != "/enterprises/acme/gitee_go/pipelines?repo=app" {
		t.Fatalf("override = %q", got)
	}
}

func TestValidateCICDConnection(t *testing.T) {
	cases := []struct {
		name    string
		in      CICDConnection
		wantErr bool
	}{
		{"ok gitlab self-hosted nested group",
			CICDConnection{Name: "n", Provider: "gitlab", Project: "group/sub/app", BaseURL: "https://g.corp"}, false},
		{"missing name", CICDConnection{Provider: "gitlab", Project: "a/b"}, true},
		{"bad provider", CICDConnection{Name: "n", Provider: "svn", Project: "a/b"}, true},
		{"missing project", CICDConnection{Name: "n", Provider: "gitlab"}, true},
		{"base url without scheme",
			CICDConnection{Name: "n", Provider: "gitlab", Project: "a/b", BaseURL: "gitlab.corp"}, true},
		{"github must be owner/repo",
			CICDConnection{Name: "n", Provider: "github", Project: "a/b/c"}, true},
		{"ca cert and skip-tls are mutually exclusive",
			CICDConnection{Name: "n", Provider: "gitlab", Project: "a/b",
				CACert: "-----BEGIN CERTIFICATE-----x", InsecureSkipTLS: true}, true},
		{"ca cert must be PEM",
			CICDConnection{Name: "n", Provider: "gitlab", Project: "a/b", CACert: "not-a-cert"}, true},
	}
	for _, tc := range cases {
		in := tc.in
		msg := validateCICDConnection(&in)
		if (msg != "") != tc.wantErr {
			t.Errorf("%s: got %q, wantErr=%v", tc.name, msg, tc.wantErr)
		}
	}
}

// TestListCICDRunsGitLab exercises the adapter against a stub instance, covering
// the self-hosted base URL, the PRIVATE-TOKEN header and field mapping.
func TestListCICDRunsGitLab(t *testing.T) {
	var gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.RequestURI
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": 42, "iid": 7, "status": "failed", "ref": "main",
			"sha": "abcdef1234567890", "web_url": "https://g/x/-/pipelines/42",
			"created_at": "2024-05-01T10:00:00Z", "updated_at": "2024-05-01T10:05:00Z",
			"duration": 300, "user": map[string]any{"username": "alice"},
		}})
	}))
	defer srv.Close()

	s := &Server{}
	c := CICDConnection{
		ID: "c1", Provider: CICDProviderGitLab, BaseURL: srv.URL,
		Project: "group/sub/app", Token: "tok",
	}
	runs, err := s.ListCICDRuns(context.Background(), c, 10)
	if err != nil {
		t.Fatalf("ListCICDRuns: %v", err)
	}
	if gotToken != "tok" {
		t.Errorf("PRIVATE-TOKEN = %q", gotToken)
	}
	// Nested group paths must be URL-escaped into a single path segment.
	// GitLab requires the project path as ONE url-encoded segment.
	if !strings.Contains(gotPath, "group%2Fsub%2Fapp") {
		t.Errorf("project not escaped on the wire: %s", gotPath)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs", len(runs))
	}
	r := runs[0]
	if r.ID != "42" || r.Number != 7 || r.Status != CICDStatusFailed || r.Actor != "alice" {
		t.Errorf("mapped run = %+v", r)
	}
	if r.CreatedAt == 0 || r.DurationSec != 300 {
		t.Errorf("time/duration not mapped: %+v", r)
	}
}

func TestListCICDRunsGitHubFoldsConclusion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{{
			"id": 9, "run_number": 3, "name": "CI", "status": "completed", "conclusion": "failure",
			"head_branch": "main", "head_sha": "deadbeef", "html_url": "https://gh/run/9",
			"created_at": "2024-05-01T10:00:00Z", "run_started_at": "2024-05-01T10:00:00Z",
			"updated_at": "2024-05-01T10:02:00Z", "actor": map[string]any{"login": "bob"},
		}}})
	}))
	defer srv.Close()

	s := &Server{}
	c := CICDConnection{ID: "c2", Provider: CICDProviderGitHub, BaseURL: srv.URL, Project: "acme/app", Token: "tok"}
	runs, err := s.ListCICDRuns(context.Background(), c, 10)
	if err != nil {
		t.Fatalf("ListCICDRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != CICDStatusFailed {
		t.Fatalf("got %+v", runs)
	}
	if runs[0].DurationSec != 120 {
		t.Errorf("duration = %d, want 120", runs[0].DurationSec)
	}
}

func TestListCICDRunsGiteeAcceptsWrappedPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Gitee authenticates via query parameter, not a header.
		if r.URL.Query().Get("access_token") != "tok" {
			t.Errorf("access_token missing: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{
			"id": 5, "sequence": 12, "name": "build", "state": "success", "branch": "master",
			"commit_id": "cafebabe", "created_at": "2024-05-01T10:00:00Z",
		}}})
	}))
	defer srv.Close()

	s := &Server{}
	c := CICDConnection{ID: "c3", Provider: CICDProviderGitee, BaseURL: srv.URL, Project: "acme/app", Token: "tok"}
	runs, err := s.ListCICDRuns(context.Background(), c, 10)
	if err != nil {
		t.Fatalf("ListCICDRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs", len(runs))
	}
	if runs[0].Status != CICDStatusSuccess || runs[0].Ref != "master" || runs[0].SHA != "cafebabe" {
		t.Errorf("gitee fallback fields not mapped: %+v", runs[0])
	}
	if runs[0].Number != 12 {
		t.Errorf("sequence → number = %d", runs[0].Number)
	}
}

func TestCICDRequestSurfacesAuthAndNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.RequestURI, "/api/v4/projects/a%2Fb/pipelines"):
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := &Server{}
	c := CICDConnection{Provider: CICDProviderGitLab, BaseURL: srv.URL, Project: "a/b", Token: "bad"}
	_, err := s.ListCICDRuns(context.Background(), c, 5)
	if err == nil || !strings.Contains(err.Error(), "令牌无效") {
		t.Fatalf("want auth error, got %v", err)
	}

	c.Provider = CICDProviderGitee
	_, err = s.ListCICDRuns(context.Background(), c, 5)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want 404 hint, got %v", err)
	}
}

func TestCICDRunCacheCollapsesDuplicateFetches(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "status": "success"}})
	}))
	defer srv.Close()

	s := &Server{}
	c := CICDConnection{ID: "cache-test", Provider: CICDProviderGitLab, BaseURL: srv.URL, Project: "a/b"}
	InvalidateCICDCache(c.ID)
	for i := 0; i < 3; i++ {
		if _, err := s.CachedCICDRuns(context.Background(), c, 10); err != nil {
			t.Fatalf("CachedCICDRuns: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("upstream called %d times, want 1 (cache miss only)", calls)
	}
	InvalidateCICDCache(c.ID)
	if _, err := s.CachedCICDRuns(context.Background(), c, 10); err != nil {
		t.Fatalf("after invalidate: %v", err)
	}
	if calls != 2 {
		t.Fatalf("invalidate did not force a refetch (calls=%d)", calls)
	}
}

func TestMaskCICDConnectionHidesToken(t *testing.T) {
	c := CICDConnection{Token: "glpat-abcdefghijklmnop"}
	masked := maskCICDConnection(c)
	if strings.Contains(masked.Token, "efghijkl") {
		t.Fatalf("token leaked: %q", masked.Token)
	}
	if masked.Token == "" {
		t.Fatal("masked token should stay non-empty so the UI can show a placeholder")
	}
}
