package main

// CI/CD integration — GitLab CI, GitHub Actions and Gitee Go.
//
// The console needs one normalized view of "what pipelines are running and which
// ones broke", plus enough control (retry / cancel / trigger) to close the loop
// from a failed build to a fix, and a bridge into the incident + AI diagnosis
// path so a red pipeline can be triaged like any other signal.
//
// Each provider gets an adapter that maps its REST shape onto CICDRun/CICDJob.
// Tokens are sealed with the same encryptSecret helper used for data sources and
// are never returned to the browser in clear text.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Provider identifiers.
const (
	CICDProviderGitLab = "gitlab"
	CICDProviderGitHub = "github"
	CICDProviderGitee  = "gitee"
)

// Normalized run states. Provider-specific values are folded into these so the
// UI, alerting and AI prompts only ever deal with one vocabulary.
const (
	CICDStatusRunning  = "running"
	CICDStatusSuccess  = "success"
	CICDStatusFailed   = "failed"
	CICDStatusCanceled = "canceled"
	CICDStatusPending  = "pending"
	CICDStatusSkipped  = "skipped"
	CICDStatusUnknown  = "unknown"
)

// CICDConnection is one configured repository/pipeline source.
type CICDConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// BaseURL points at a self-hosted instance ("https://gitlab.corp.example").
	// Empty means the provider's SaaS endpoint.
	BaseURL string `json:"base_url,omitempty"`
	// Project is "group/subgroup/repo" (GitLab) or "owner/repo" (GitHub, Gitee).
	Project string `json:"project"`
	// Token is a personal/project access token, encrypted at rest and masked on read.
	Token string `json:"token,omitempty"`
	// Ref is the default branch used when triggering a pipeline.
	Ref string `json:"ref,omitempty"`
	// PipelinePath overrides the provider's pipeline list path. Gitee Go does not
	// publish a stable REST contract the way GitLab/GitHub do, so operators can
	// point this at whatever their Gitee edition exposes. `{owner}`, `{repo}` and
	// `{project}` are substituted. Empty uses the adapter default.
	PipelinePath string `json:"pipeline_path,omitempty"`
	// CACert is a PEM bundle for a self-hosted instance signed by an internal CA.
	CACert string `json:"ca_cert,omitempty"`
	// InsecureSkipTLS disables certificate verification. Self-hosted GitLab behind
	// a self-signed cert is the common case; keep it an explicit operator opt-in.
	InsecureSkipTLS bool `json:"insecure_skip_tls,omitempty"`
	// WatchFailures raises a platform alert (and optionally an incident) when a
	// run on this connection transitions into a failed state.
	WatchFailures bool `json:"watch_failures,omitempty"`
	// AutoIncident opens an SRE incident for a failed run so the AI diagnosis and
	// remediation loop can pick it up.
	AutoIncident bool   `json:"auto_incident,omitempty"`
	Enabled      bool   `json:"enabled"`
	CreatedAt    int64  `json:"created_at"`
	LastError    string `json:"last_error,omitempty"`
	LastSyncAt   int64  `json:"last_sync_at,omitempty"`
}

// CICDRun is one pipeline execution, normalized across providers.
type CICDRun struct {
	ConnectionID string `json:"connection_id"`
	Provider     string `json:"provider"`
	Project      string `json:"project"`
	ID           string `json:"id"`
	Number       int64  `json:"number,omitempty"`
	Name         string `json:"name,omitempty"`
	Status       string `json:"status"`
	RawStatus    string `json:"raw_status,omitempty"`
	Ref          string `json:"ref,omitempty"`
	SHA          string `json:"sha,omitempty"`
	Actor        string `json:"actor,omitempty"`
	WebURL       string `json:"web_url,omitempty"`
	CreatedAt    int64  `json:"created_at,omitempty"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
	DurationSec  int64  `json:"duration_sec,omitempty"`
}

// CICDJob is one stage/step inside a run.
type CICDJob struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Stage       string `json:"stage,omitempty"`
	Status      string `json:"status"`
	RawStatus   string `json:"raw_status,omitempty"`
	WebURL      string `json:"web_url,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	FinishedAt  int64  `json:"finished_at,omitempty"`
	DurationSec int64  `json:"duration_sec,omitempty"`
	FailureNote string `json:"failure_note,omitempty"`
}

// ---------------------------------------------------------------------------
// ConfigStore CRUD (persisted in ServerConfig.CICDConnections)
// ---------------------------------------------------------------------------

func (cs *ConfigStore) ListCICDConnections() []CICDConnection {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return append([]CICDConnection{}, cs.cfg.CICDConnections...)
}

func (cs *ConfigStore) GetCICDConnection(id string) (CICDConnection, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, c := range cs.cfg.CICDConnections {
		if c.ID == id {
			return c, true
		}
	}
	return CICDConnection{}, false
}

func (cs *ConfigStore) AddCICDConnection(c CICDConnection) (CICDConnection, error) {
	cs.mu.Lock()
	if c.ID == "" {
		c.ID = termID()[:8]
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = time.Now().Unix()
	}
	c.Token = encryptSecret(c.Token)
	cs.cfg.CICDConnections = append(cs.cfg.CICDConnections, c)
	cs.mu.Unlock()
	return c, cs.save()
}

func (cs *ConfigStore) UpdateCICDConnection(id string, updated CICDConnection) error {
	cs.mu.Lock()
	found := false
	for i, c := range cs.cfg.CICDConnections {
		if c.ID != id {
			continue
		}
		updated.ID = c.ID
		updated.CreatedAt = c.CreatedAt
		// A blank or masked token means "keep what is stored" — the browser never
		// receives the real value, so it cannot echo it back.
		if updated.Token == "" || strings.Contains(updated.Token, "****") {
			updated.Token = c.Token
		} else {
			updated.Token = encryptSecret(updated.Token)
		}
		updated.LastError = c.LastError
		updated.LastSyncAt = c.LastSyncAt
		cs.cfg.CICDConnections[i] = updated
		found = true
		break
	}
	cs.mu.Unlock()
	if !found {
		return fmt.Errorf("cicd connection not found")
	}
	return cs.save()
}

func (cs *ConfigStore) DeleteCICDConnection(id string) error {
	cs.mu.Lock()
	var kept []CICDConnection
	for _, c := range cs.cfg.CICDConnections {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	cs.cfg.CICDConnections = kept
	cs.mu.Unlock()
	return cs.save()
}

// SetCICDSyncResult records the outcome of the latest poll for the status column.
func (cs *ConfigStore) SetCICDSyncResult(id string, errMsg string) {
	cs.mu.Lock()
	for i, c := range cs.cfg.CICDConnections {
		if c.ID == id {
			cs.cfg.CICDConnections[i].LastError = errMsg
			cs.cfg.CICDConnections[i].LastSyncAt = time.Now().Unix()
			break
		}
	}
	cs.mu.Unlock()
	_ = cs.save()
}

// ---------------------------------------------------------------------------
// Provider plumbing
// ---------------------------------------------------------------------------

func cicdDefaultBase(provider string) string {
	switch provider {
	case CICDProviderGitHub:
		return "https://api.github.com"
	case CICDProviderGitee:
		return "https://gitee.com/api/v5"
	default:
		return "https://gitlab.com"
	}
}

// cicdAPIRoot resolves the API root for a connection, normalizing self-hosted
// base URLs (GitLab wants /api/v4 appended, GitHub Enterprise wants /api/v3).
func cicdAPIRoot(c CICDConnection) string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = cicdDefaultBase(c.Provider)
	}
	switch c.Provider {
	case CICDProviderGitLab:
		if !strings.Contains(base, "/api/v") {
			base += "/api/v4"
		}
	case CICDProviderGitHub:
		// api.github.com is already the root; GHE installs live under /api/v3.
		if !strings.Contains(base, "api.github.com") && !strings.Contains(base, "/api/v") {
			base += "/api/v3"
		}
	case CICDProviderGitee:
		if !strings.Contains(base, "/api/v") {
			base += "/api/v5"
		}
	}
	return base
}

func cicdOwnerRepo(project string) (owner, repo string) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(project), "/"), "/")
	if len(parts) < 2 {
		return "", strings.TrimSpace(project)
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
}

// cicdHTTPClient builds a client that trusts the connection's CA bundle. Self-hosted
// GitLab/Gitee instances routinely sit behind an internal CA, so verification has to
// be configurable without falling back to "skip everything" by default.
func cicdHTTPClient(c CICDConnection, timeout time.Duration) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.InsecureSkipTLS {
		tlsCfg.InsecureSkipVerify = true // #nosec G402 - explicit operator opt-in
	}
	if pem := strings.TrimSpace(c.CACert); pem != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			return nil, fmt.Errorf("无效的 CA 证书 PEM")
		}
		tlsCfg.RootCAs = pool
	}
	tr := &http.Transport{
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: 10 * time.Second,
		Proxy:               http.ProxyFromEnvironment,
	}
	return &http.Client{Timeout: timeout, Transport: tr}, nil
}

// cicdRequest performs an authenticated call and decodes JSON into out.
func (s *Server) cicdRequest(ctx context.Context, c CICDConnection, method, path string, body io.Reader, out any) error {
	root := cicdAPIRoot(c)
	full := root + path
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return err
	}
	token := decryptSecret(c.Token)
	switch c.Provider {
	case CICDProviderGitLab:
		if token != "" {
			req.Header.Set("PRIVATE-TOKEN", token)
		}
	case CICDProviderGitHub:
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	case CICDProviderGitee:
		// Gitee v5 authenticates with an access_token query parameter.
		if token != "" {
			sep := "?"
			if strings.Contains(full, "?") {
				sep = "&"
			}
			full += sep + "access_token=" + url.QueryEscape(token)
			req, err = http.NewRequestWithContext(ctx, method, full, body)
			if err != nil {
				return err
			}
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept-Encoding", "identity")

	client, err := cicdHTTPClient(c, 20*time.Second)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: 接口不存在或项目/流水线路径不正确 (404 %s)", c.Provider, path)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%s: 令牌无效或权限不足 (%d)", c.Provider, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("%s: HTTP %d %s", c.Provider, resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// ---------------------------------------------------------------------------
// Status normalization
// ---------------------------------------------------------------------------

func normalizeCICDStatus(provider, raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "success", "successful", "passed", "completed_success":
		return CICDStatusSuccess
	case "failed", "failure", "error", "timed_out":
		return CICDStatusFailed
	case "canceled", "cancelled", "canceling":
		return CICDStatusCanceled
	case "running", "in_progress", "pending_running":
		return CICDStatusRunning
	case "pending", "created", "waiting_for_resource", "preparing", "scheduled", "queued", "manual", "waiting":
		return CICDStatusPending
	case "skipped", "neutral":
		return CICDStatusSkipped
	case "":
		return CICDStatusUnknown
	}
	return CICDStatusUnknown
}

// githubRunStatus folds GitHub's split status/conclusion pair into one value.
func githubRunStatus(status, conclusion string) string {
	if strings.EqualFold(status, "completed") {
		return normalizeCICDStatus(CICDProviderGitHub, conclusion)
	}
	return normalizeCICDStatus(CICDProviderGitHub, status)
}

func parseCICDTime(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Adapters: list runs
// ---------------------------------------------------------------------------

type gitlabPipeline struct {
	ID        int64  `json:"id"`
	IID       int64  `json:"iid"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
	SHA       string `json:"sha"`
	WebURL    string `json:"web_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Name      string `json:"name"`
	User      struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Duration int64 `json:"duration"`
}

type githubRun struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	RunNumber    int64  `json:"run_number"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HeadBranch   string `json:"head_branch"`
	HeadSHA      string `json:"head_sha"`
	HTMLURL      string `json:"html_url"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	RunStartedAt string `json:"run_started_at"`
	Actor        struct {
		Login string `json:"login"`
	} `json:"actor"`
}

// giteePipeline covers the shape Gitee Go returns for a pipeline record. Field
// names vary between editions, so the decoder accepts several spellings.
type giteePipeline struct {
	ID        json.Number `json:"id"`
	Sequence  json.Number `json:"sequence"`
	Name      string      `json:"name"`
	Status    string      `json:"status"`
	State     string      `json:"state"`
	Ref       string      `json:"ref"`
	Branch    string      `json:"branch"`
	SHA       string      `json:"sha"`
	CommitID  string      `json:"commit_id"`
	URL       string      `json:"url"`
	HTMLURL   string      `json:"html_url"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
	Creator   struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	} `json:"creator"`
}

func (g giteePipeline) statusText() string {
	if g.Status != "" {
		return g.Status
	}
	return g.State
}

func (g giteePipeline) refText() string {
	if g.Ref != "" {
		return g.Ref
	}
	return g.Branch
}

func (g giteePipeline) shaText() string {
	if g.SHA != "" {
		return g.SHA
	}
	return g.CommitID
}

func (g giteePipeline) webURL() string {
	if g.HTMLURL != "" {
		return g.HTMLURL
	}
	return g.URL
}

// giteePipelinePath resolves the (configurable) Gitee Go pipeline list path.
func giteePipelinePath(c CICDConnection) string {
	owner, repo := cicdOwnerRepo(c.Project)
	tpl := strings.TrimSpace(c.PipelinePath)
	if tpl == "" {
		tpl = "/repos/{owner}/{repo}/gitee_go/pipelines"
	}
	if !strings.HasPrefix(tpl, "/") {
		tpl = "/" + tpl
	}
	rep := strings.NewReplacer(
		"{owner}", url.PathEscape(owner),
		"{repo}", url.PathEscape(repo),
		"{project}", url.PathEscape(c.Project),
	)
	return rep.Replace(tpl)
}

// ---------------------------------------------------------------------------
// Run cache
// ---------------------------------------------------------------------------

// The console loads /cicd/overview and /cicd/runs together, and the runs list
// polls. Without a shared cache that is 2 upstream calls per connection every
// refresh — enough to hit GitLab/GitHub rate limits on a busy fleet. A short TTL
// keeps the UI live while collapsing duplicate fetches.
const cicdRunCacheTTL = 20 * time.Second

type cicdRunCacheEntry struct {
	runs    []CICDRun
	err     error
	fetched time.Time
}

var (
	cicdRunCacheMu sync.Mutex
	cicdRunCache   = map[string]cicdRunCacheEntry{}
)

func cicdCacheKey(c CICDConnection, limit int) string {
	return fmt.Sprintf("%s|%d", c.ID, limit)
}

// InvalidateCICDCache drops cached runs for a connection after a mutating action
// so the next poll reflects the retry/cancel/trigger immediately.
func InvalidateCICDCache(connID string) {
	cicdRunCacheMu.Lock()
	for k := range cicdRunCache {
		if strings.HasPrefix(k, connID+"|") {
			delete(cicdRunCache, k)
		}
	}
	cicdRunCacheMu.Unlock()
}

// CachedCICDRuns returns runs from cache when fresh, otherwise fetches.
func (s *Server) CachedCICDRuns(ctx context.Context, c CICDConnection, limit int) ([]CICDRun, error) {
	key := cicdCacheKey(c, limit)
	cicdRunCacheMu.Lock()
	if e, ok := cicdRunCache[key]; ok && time.Since(e.fetched) < cicdRunCacheTTL {
		cicdRunCacheMu.Unlock()
		return e.runs, e.err
	}
	cicdRunCacheMu.Unlock()

	runs, err := s.ListCICDRuns(ctx, c, limit)

	cicdRunCacheMu.Lock()
	cicdRunCache[key] = cicdRunCacheEntry{runs: runs, err: err, fetched: time.Now()}
	// Bound the map: connections are few, but deletes never happened otherwise.
	if len(cicdRunCache) > 256 {
		for k, e := range cicdRunCache {
			if time.Since(e.fetched) > cicdRunCacheTTL {
				delete(cicdRunCache, k)
			}
		}
	}
	cicdRunCacheMu.Unlock()
	return runs, err
}

// FetchCICDRunsParallel queries every connection concurrently. One slow or dead
// instance must not serialize behind the others — a self-hosted GitLab that is
// down would otherwise stall the whole page for the full request timeout.
func (s *Server) FetchCICDRunsParallel(ctx context.Context, conns []CICDConnection, limit int) ([]CICDRun, map[string]string) {
	type result struct {
		id   string
		runs []CICDRun
		err  error
	}
	out := make([]result, len(conns))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6) // bound fan-out against the SCM APIs
	for i, c := range conns {
		wg.Add(1)
		go func(i int, c CICDConnection) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			runs, err := s.CachedCICDRuns(ctx, c, limit)
			out[i] = result{id: c.ID, runs: runs, err: err}
		}(i, c)
	}
	wg.Wait()

	all := []CICDRun{}
	errs := map[string]string{}
	for _, r := range out {
		if r.err != nil {
			errs[r.id] = r.err.Error()
			continue
		}
		all = append(all, r.runs...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt > all[j].CreatedAt })
	return all, errs
}

// ListCICDRuns fetches recent runs for a connection, newest first.
func (s *Server) ListCICDRuns(ctx context.Context, c CICDConnection, limit int) ([]CICDRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	owner, repo := cicdOwnerRepo(c.Project)
	out := []CICDRun{}

	switch c.Provider {
	case CICDProviderGitLab:
		var raw []gitlabPipeline
		path := fmt.Sprintf("/projects/%s/pipelines?per_page=%d&order_by=id&sort=desc",
			url.PathEscape(strings.Trim(c.Project, "/")), limit)
		if err := s.cicdRequest(ctx, c, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		for _, p := range raw {
			actor := p.User.Username
			if actor == "" {
				actor = p.User.Name
			}
			out = append(out, CICDRun{
				ConnectionID: c.ID, Provider: c.Provider, Project: c.Project,
				ID: fmt.Sprint(p.ID), Number: p.IID, Name: p.Name,
				Status: normalizeCICDStatus(c.Provider, p.Status), RawStatus: p.Status,
				Ref: p.Ref, SHA: p.SHA, Actor: actor, WebURL: p.WebURL,
				CreatedAt: parseCICDTime(p.CreatedAt), UpdatedAt: parseCICDTime(p.UpdatedAt),
				DurationSec: p.Duration,
			})
		}

	case CICDProviderGitHub:
		var raw struct {
			WorkflowRuns []githubRun `json:"workflow_runs"`
		}
		path := fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=%d",
			url.PathEscape(owner), url.PathEscape(repo), limit)
		if err := s.cicdRequest(ctx, c, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		for _, p := range raw.WorkflowRuns {
			started := parseCICDTime(p.RunStartedAt)
			updated := parseCICDTime(p.UpdatedAt)
			var dur int64
			if started > 0 && updated > started {
				dur = updated - started
			}
			out = append(out, CICDRun{
				ConnectionID: c.ID, Provider: c.Provider, Project: c.Project,
				ID: fmt.Sprint(p.ID), Number: p.RunNumber, Name: p.Name,
				Status:    githubRunStatus(p.Status, p.Conclusion),
				RawStatus: strings.TrimSpace(p.Status + "/" + p.Conclusion),
				Ref:       p.HeadBranch, SHA: p.HeadSHA, Actor: p.Actor.Login, WebURL: p.HTMLURL,
				CreatedAt: parseCICDTime(p.CreatedAt), UpdatedAt: updated, DurationSec: dur,
			})
		}

	case CICDProviderGitee:
		// Gitee returns either a bare array or {data:[…]} depending on edition.
		var probe json.RawMessage
		if err := s.cicdRequest(ctx, c, http.MethodGet, giteePipelinePath(c), nil, &probe); err != nil {
			return nil, err
		}
		var raw []giteePipeline
		if err := json.Unmarshal(probe, &raw); err != nil {
			var wrapped struct {
				Data []giteePipeline `json:"data"`
				List []giteePipeline `json:"list"`
			}
			if err2 := json.Unmarshal(probe, &wrapped); err2 != nil {
				return nil, fmt.Errorf("gitee: 无法解析流水线响应，请在连接里调整「流水线接口路径」")
			}
			raw = wrapped.Data
			if len(raw) == 0 {
				raw = wrapped.List
			}
		}
		for _, p := range raw {
			actor := p.Creator.Login
			if actor == "" {
				actor = p.Creator.Name
			}
			num, _ := p.Sequence.Int64()
			out = append(out, CICDRun{
				ConnectionID: c.ID, Provider: c.Provider, Project: c.Project,
				ID: p.ID.String(), Number: num, Name: p.Name,
				Status: normalizeCICDStatus(c.Provider, p.statusText()), RawStatus: p.statusText(),
				Ref: p.refText(), SHA: p.shaText(), Actor: actor, WebURL: p.webURL(),
				CreatedAt: parseCICDTime(p.CreatedAt), UpdatedAt: parseCICDTime(p.UpdatedAt),
			})
		}

	default:
		return nil, fmt.Errorf("unsupported provider %q", c.Provider)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ListCICDJobs returns the stages/steps of a single run.
func (s *Server) ListCICDJobs(ctx context.Context, c CICDConnection, runID string) ([]CICDJob, error) {
	owner, repo := cicdOwnerRepo(c.Project)
	out := []CICDJob{}

	switch c.Provider {
	case CICDProviderGitLab:
		var raw []struct {
			ID         int64   `json:"id"`
			Name       string  `json:"name"`
			Stage      string  `json:"stage"`
			Status     string  `json:"status"`
			WebURL     string  `json:"web_url"`
			StartedAt  string  `json:"started_at"`
			FinishedAt string  `json:"finished_at"`
			Duration   float64 `json:"duration"`
			FailureRsn string  `json:"failure_reason"`
		}
		path := fmt.Sprintf("/projects/%s/pipelines/%s/jobs?per_page=100",
			url.PathEscape(strings.Trim(c.Project, "/")), url.PathEscape(runID))
		if err := s.cicdRequest(ctx, c, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		for _, j := range raw {
			out = append(out, CICDJob{
				ID: fmt.Sprint(j.ID), Name: j.Name, Stage: j.Stage,
				Status: normalizeCICDStatus(c.Provider, j.Status), RawStatus: j.Status,
				WebURL: j.WebURL, StartedAt: parseCICDTime(j.StartedAt), FinishedAt: parseCICDTime(j.FinishedAt),
				DurationSec: int64(j.Duration), FailureNote: j.FailureRsn,
			})
		}

	case CICDProviderGitHub:
		var raw struct {
			Jobs []struct {
				ID          int64  `json:"id"`
				Name        string `json:"name"`
				Status      string `json:"status"`
				Conclusion  string `json:"conclusion"`
				HTMLURL     string `json:"html_url"`
				StartedAt   string `json:"started_at"`
				CompletedAt string `json:"completed_at"`
			} `json:"jobs"`
		}
		path := fmt.Sprintf("/repos/%s/%s/actions/runs/%s/jobs?per_page=100",
			url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(runID))
		if err := s.cicdRequest(ctx, c, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		for _, j := range raw.Jobs {
			st, fi := parseCICDTime(j.StartedAt), parseCICDTime(j.CompletedAt)
			var dur int64
			if st > 0 && fi > st {
				dur = fi - st
			}
			out = append(out, CICDJob{
				ID: fmt.Sprint(j.ID), Name: j.Name,
				Status:    githubRunStatus(j.Status, j.Conclusion),
				RawStatus: strings.TrimSpace(j.Status + "/" + j.Conclusion),
				WebURL:    j.HTMLURL, StartedAt: st, FinishedAt: fi, DurationSec: dur,
			})
		}

	case CICDProviderGitee:
		// Stage detail is edition-specific; the run list already carries status,
		// so return empty rather than guessing at a path that may not exist.
		return out, nil

	default:
		return nil, fmt.Errorf("unsupported provider %q", c.Provider)
	}
	return out, nil
}

// TriggerCICDRun starts a new pipeline on the given ref.
func (s *Server) TriggerCICDRun(ctx context.Context, c CICDConnection, ref, workflow string) error {
	owner, repo := cicdOwnerRepo(c.Project)
	if ref == "" {
		ref = c.Ref
	}
	if ref == "" {
		ref = "main"
	}
	switch c.Provider {
	case CICDProviderGitLab:
		path := fmt.Sprintf("/projects/%s/pipeline?ref=%s",
			url.PathEscape(strings.Trim(c.Project, "/")), url.QueryEscape(ref))
		return s.cicdRequest(ctx, c, http.MethodPost, path, nil, nil)
	case CICDProviderGitHub:
		if strings.TrimSpace(workflow) == "" {
			return fmt.Errorf("github: 触发需要指定 workflow 文件名（如 ci.yml）")
		}
		body := strings.NewReader(fmt.Sprintf(`{"ref":%q}`, ref))
		path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/dispatches",
			url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(workflow))
		return s.cicdRequest(ctx, c, http.MethodPost, path, body, nil)
	case CICDProviderGitee:
		return fmt.Errorf("gitee: 当前版本不支持从控制台触发流水线，请在 Gitee Go 页面操作")
	}
	return fmt.Errorf("unsupported provider %q", c.Provider)
}

// RetryCICDRun re-runs a pipeline (GitLab retry / GitHub rerun).
func (s *Server) RetryCICDRun(ctx context.Context, c CICDConnection, runID string) error {
	owner, repo := cicdOwnerRepo(c.Project)
	switch c.Provider {
	case CICDProviderGitLab:
		path := fmt.Sprintf("/projects/%s/pipelines/%s/retry",
			url.PathEscape(strings.Trim(c.Project, "/")), url.PathEscape(runID))
		return s.cicdRequest(ctx, c, http.MethodPost, path, nil, nil)
	case CICDProviderGitHub:
		path := fmt.Sprintf("/repos/%s/%s/actions/runs/%s/rerun",
			url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(runID))
		return s.cicdRequest(ctx, c, http.MethodPost, path, nil, nil)
	case CICDProviderGitee:
		return fmt.Errorf("gitee: 当前版本不支持从控制台重跑流水线")
	}
	return fmt.Errorf("unsupported provider %q", c.Provider)
}

// CancelCICDRun stops an in-flight pipeline.
func (s *Server) CancelCICDRun(ctx context.Context, c CICDConnection, runID string) error {
	owner, repo := cicdOwnerRepo(c.Project)
	switch c.Provider {
	case CICDProviderGitLab:
		path := fmt.Sprintf("/projects/%s/pipelines/%s/cancel",
			url.PathEscape(strings.Trim(c.Project, "/")), url.PathEscape(runID))
		return s.cicdRequest(ctx, c, http.MethodPost, path, nil, nil)
	case CICDProviderGitHub:
		path := fmt.Sprintf("/repos/%s/%s/actions/runs/%s/cancel",
			url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(runID))
		return s.cicdRequest(ctx, c, http.MethodPost, path, nil, nil)
	case CICDProviderGitee:
		return fmt.Errorf("gitee: 当前版本不支持从控制台取消流水线")
	}
	return fmt.Errorf("unsupported provider %q", c.Provider)
}

// FetchCICDJobLog returns the tail of a failed job's log, used as AI evidence.
func (s *Server) FetchCICDJobLog(ctx context.Context, c CICDConnection, jobID string, maxBytes int) (string, error) {
	if maxBytes <= 0 || maxBytes > 512*1024 {
		maxBytes = 64 * 1024
	}
	owner, repo := cicdOwnerRepo(c.Project)
	var path string
	switch c.Provider {
	case CICDProviderGitLab:
		path = fmt.Sprintf("/projects/%s/jobs/%s/trace",
			url.PathEscape(strings.Trim(c.Project, "/")), url.PathEscape(jobID))
	case CICDProviderGitHub:
		path = fmt.Sprintf("/repos/%s/%s/actions/jobs/%s/logs",
			url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(jobID))
	default:
		return "", fmt.Errorf("%s: 暂不支持拉取任务日志", c.Provider)
	}

	root := cicdAPIRoot(c)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+path, nil)
	if err != nil {
		return "", err
	}
	token := decryptSecret(c.Token)
	if c.Provider == CICDProviderGitLab {
		req.Header.Set("PRIVATE-TOKEN", token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	client, err := cicdHTTPClient(c, 30*time.Second)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s: HTTP %d", c.Provider, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)*4))
	if err != nil {
		return "", err
	}
	text := string(raw)
	// The tail is where failures live; keep the last maxBytes worth of runes.
	if len(text) > maxBytes {
		text = "…（已截断前段）\n" + text[len(text)-maxBytes:]
	}
	return text, nil
}

// maskCICDConnection strips the token before the record reaches the browser.
func maskCICDConnection(c CICDConnection) CICDConnection {
	if c.Token != "" {
		c.Token = maskSecret(decryptSecret(c.Token))
	}
	return c
}
