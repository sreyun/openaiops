package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Threat-intel feed subsystem.
//
// Every detection library the product ships — Nuclei templates, DBMS error
// signatures, payload/POC corpora — is fetched through one pipeline so that a
// single place owns the things that actually break in customer networks:
// egress proxies, GitHub being unreachable from mainland China, archive
// extraction safety, and update runs that outlive an HTTP request.
//
// Updates never run inside a request handler. The old design downloaded a
// ~600 MB template tree synchronously in POST /engine/refresh, which reliably
// timed out at the browser or reverse proxy and left a half-written tree behind.

type FeedKind string

const (
	// FeedKindNuclei feeds the Nuclei engine directly — these change what gets scanned.
	FeedKindNuclei FeedKind = "nuclei"
	// FeedKindSignature is parsed by the built-in engine (e.g. DBMS error regexes).
	FeedKindSignature FeedKind = "signature"
	// FeedKindKnowledge is reference material: payload corpora, POC indexes,
	// remediation text. It enriches findings but does not itself detect.
	FeedKindKnowledge FeedKind = "knowledge"
)

// FeedSource is one catalog entry. Sources are code-defined (not user-supplied
// URLs) so an operator cannot turn the updater into an SSRF primitive.
type FeedSource struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    FeedKind `json:"kind"`
	Repo    string   `json:"repo"`          // GitHub owner/name
	Ref     string   `json:"ref,omitempty"` // branch or tag; empty = latest release
	Subdir  string   `json:"subdir,omitempty"`
	Include []string `json:"include,omitempty"` // lower-case filename suffixes to keep
	// RefFallback is the default branch to use when the releases API is blocked
	// or the resolved tag no longer has an archive. Repos differ ("main" vs
	// "master"), and guessing wrong turns every update into a 404.
	RefFallback string `json:"ref_fallback,omitempty"`
	Desc        string `json:"desc"`
	License     string `json:"license,omitempty"`
	DefaultOn   bool   `json:"default_on"`
	MaxFiles    int    `json:"max_files,omitempty"`
	MaxBytes    int64  `json:"max_bytes,omitempty"`
}

// refCandidates lists the refs to try in order. A single 404 on a stale tag
// should not strand an air-gapped-ish install on an empty template tree.
func (s FeedSource) refCandidates(resolved string) []string {
	out := make([]string, 0, 3)
	add := func(r string) {
		r = strings.TrimSpace(r)
		if r == "" {
			return
		}
		for _, e := range out {
			if e == r {
				return
			}
		}
		out = append(out, r)
	}
	add(resolved)
	add(s.Ref)
	add(s.RefFallback)
	add("main")
	add("master")
	return out
}

// feedCatalog is the shipped source list. Each entry states honestly whether it
// is executable detection content or reference material, because "we integrate
// tool X" means very different things for a template tree than for a POC repo.
var feedCatalog = []FeedSource{
	{
		ID: "nuclei-templates", Name: "Nuclei 模板库", Kind: FeedKindNuclei,
		Repo: "projectdiscovery/nuclei-templates", RefFallback: "main",
		Desc:    "ProjectDiscovery 官方模板库，CVE/错误配置/暴露面/默认口令等，直接驱动 Nuclei 扫描引擎。",
		License: "MIT", DefaultOn: true,
		Include:  []string{".yaml", ".yml"},
		MaxFiles: 120000, MaxBytes: 900 << 20,
	},
	{
		ID: "sqlmap-signatures", Name: "sqlmap 注入特征库", Kind: FeedKindSignature,
		Repo: "sqlmapproject/sqlmap", Ref: "master", Subdir: "data/xml",
		Desc:    "sqlmap 的 DBMS 报错指纹与注入载荷定义；内置引擎据此实现报错型 SQL 注入检测，不依赖 Nuclei。",
		License: "GPL-2.0", DefaultOn: true,
		Include:  []string{".xml"},
		MaxFiles: 4000, MaxBytes: 32 << 20,
	},
	{
		ID: "zap-rules", Name: "OWASP ZAP 规则知识库", Kind: FeedKindKnowledge,
		Repo: "zaproxy/zap-extensions", Ref: "main", Subdir: "addOns/ascanrules/src/main/resources",
		Desc:    "ZAP 主动扫描规则的名称、说明、修复建议与参考链接，用于为命中项补充权威处置文案。",
		License: "Apache-2.0", DefaultOn: false,
		Include:  []string{".properties"},
		MaxFiles: 2000, MaxBytes: 24 << 20,
	},
	{
		ID: "payloads-all-the-things", Name: "PayloadsAllTheThings", Kind: FeedKindKnowledge,
		Repo: "swisskyrepo/PayloadsAllTheThings", Ref: "master",
		Desc:    "各类漏洞的利用载荷与绕过技巧合集，按技术分类建立索引，作为研判与复现参考。",
		License: "MIT", DefaultOn: false,
		Include:  []string{".md"},
		MaxFiles: 8000, MaxBytes: 96 << 20,
	},
	{
		ID: "vulhub", Name: "Vulhub 漏洞环境索引", Kind: FeedKindKnowledge,
		Repo: "vulhub/vulhub", Ref: "master",
		Desc:    "CVE 到可复现漏洞环境的映射；用于给 CVE 类命中项附上复现路径。",
		License: "MIT", DefaultOn: false,
		Include:  []string{".md", ".yml"},
		MaxFiles: 12000, MaxBytes: 64 << 20,
	},
	{
		ID: "yaklang-poc", Name: "Yaklang 安全能力文档", Kind: FeedKindKnowledge,
		Repo: "yaklang/yakit", Ref: "master",
		Desc:    "Yakit/Yaklang 的能力与用法文档，作为人工深度验证的参考。注意：Yakit 为独立 GUI 工具，本平台不直接执行其脚本。",
		License: "AGPL-3.0", DefaultOn: false,
		Include:  []string{".md"},
		MaxFiles: 4000, MaxBytes: 32 << 20,
	},
}

func feedSourceByID(id string) (FeedSource, bool) {
	for _, s := range feedCatalog {
		if strings.EqualFold(s.ID, id) {
			return s, true
		}
	}
	return FeedSource{}, false
}

// SecurityFeedConfig holds the operator-tunable transport settings. These are
// the knobs that decide whether an update succeeds at all behind a corporate
// egress policy or from a network where GitHub is slow.
type SecurityFeedConfig struct {
	Sources []string `json:"sources,omitempty"` // enabled IDs; nil = catalog defaults
	// ProxyURL is an explicit http/https/socks5 proxy. Empty falls back to the
	// process HTTP_PROXY/HTTPS_PROXY environment.
	ProxyURL string `json:"proxy_url,omitempty"`
	// MirrorPrefix is prepended to github.com URLs, e.g. "https://ghfast.top/".
	// Needed wherever raw GitHub is throttled or blocked.
	MirrorPrefix   string `json:"mirror_prefix,omitempty"`
	TimeoutSec     int    `json:"timeout_sec,omitempty"`
	InsecureTLS    bool   `json:"insecure_tls,omitempty"` // for TLS-intercepting proxies
	AutoUpdate     bool   `json:"auto_update,omitempty"`
	IntervalHours  int    `json:"interval_hours,omitempty"`
	LastAutoRunSec int64  `json:"last_auto_run_sec,omitempty"`
}

func (c SecurityFeedConfig) withDefaults() SecurityFeedConfig {
	c.TimeoutSec = clampInt(c.TimeoutSec, 1800, 120, 7200)
	c.IntervalHours = clampInt(c.IntervalHours, 24, 1, 720)
	c.ProxyURL = strings.TrimSpace(c.ProxyURL)
	c.MirrorPrefix = strings.TrimSpace(c.MirrorPrefix)
	if c.MirrorPrefix != "" && !strings.HasSuffix(c.MirrorPrefix, "/") {
		c.MirrorPrefix += "/"
	}
	return c
}

// enabledSources resolves the configured IDs against the catalog, falling back
// to the shipped defaults when the operator has never touched the settings.
func (c SecurityFeedConfig) enabledSources() []FeedSource {
	var out []FeedSource
	if c.Sources == nil {
		for _, s := range feedCatalog {
			if s.DefaultOn {
				out = append(out, s)
			}
		}
		return out
	}
	for _, id := range c.Sources {
		if s, ok := feedSourceByID(id); ok {
			out = append(out, s)
		}
	}
	return out
}

func (c SecurityFeedConfig) sourceEnabled(id string) bool {
	for _, s := range c.enabledSources() {
		if s.ID == id {
			return true
		}
	}
	return false
}

// FeedState is the persisted result of the last update for one source.
type FeedState struct {
	ID         string `json:"id"`
	Ref        string `json:"ref,omitempty"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
	Files      int    `json:"files,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
	Source     string `json:"source,omitempty"` // resolved download URL (mirror applied)
}

// FeedJob is the live progress of an update run. The UI polls this instead of
// holding a request open for the length of a multi-hundred-megabyte download.
type FeedJob struct {
	ID        string      `json:"id"`
	Running   bool        `json:"running"`
	StartedAt int64       `json:"started_at"`
	EndedAt   int64       `json:"ended_at,omitempty"`
	Total     int         `json:"total"`
	Done      int         `json:"done"`
	Current   string      `json:"current,omitempty"`
	Phase     string      `json:"phase,omitempty"` // resolve|download|extract|publish|done
	Log       []string    `json:"log,omitempty"`
	Results   []FeedState `json:"results,omitempty"`
	Error     string      `json:"error,omitempty"`
	Actor     string      `json:"actor,omitempty"`
}

const feedMaxLogLines = 60

type feedManager struct {
	mu      sync.Mutex
	dir     string // {securityDataDir}/feeds
	states  map[string]FeedState
	job     *FeedJob
	cancel  context.CancelFunc
	loaded  bool
	statePS string // states.json path
	// onUpdated lets consumers (e.g. the sqlmap signature set) pick up freshly
	// installed content without a restart. Called after every finished run.
	onUpdated func()
}

func newFeedManager(dir string) *feedManager {
	m := &feedManager{
		dir:     dir,
		states:  map[string]FeedState{},
		statePS: filepath.Join(dir, "states.json"),
	}
	m.load()
	return m
}

func (m *feedManager) load() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded {
		return
	}
	m.loaded = true
	raw, err := os.ReadFile(m.statePS)
	if err != nil {
		return
	}
	var list []FeedState
	if json.Unmarshal(raw, &list) != nil {
		return
	}
	for _, st := range list {
		m.states[st.ID] = st
	}
}

// saveStatesLocked persists atomically: a torn states.json would make every
// source look never-updated and trigger a full re-download.
func (m *feedManager) saveStatesLocked() {
	list := make([]FeedState, 0, len(m.states))
	for _, st := range m.states {
		list = append(list, st)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(m.dir, 0o750); err != nil {
		return
	}
	tmp := m.statePS + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, m.statePS)
	}
}

func (m *feedManager) stateOf(id string) FeedState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[id]
}

func (m *feedManager) currentJob() *FeedJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	cp := *m.job
	cp.Log = append([]string(nil), m.job.Log...)
	cp.Results = append([]FeedState(nil), m.job.Results...)
	return &cp
}

func (m *feedManager) jobRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.job != nil && m.job.Running
}

func (m *feedManager) logf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return
	}
	m.job.Log = append(m.job.Log, line)
	if len(m.job.Log) > feedMaxLogLines {
		m.job.Log = m.job.Log[len(m.job.Log)-feedMaxLogLines:]
	}
}

func (m *feedManager) setPhase(phase, current string, done int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return
	}
	m.job.Phase = phase
	m.job.Current = current
	if done >= 0 {
		m.job.Done = done
	}
}

// cancelJob stops an in-flight update. Half-written trees are discarded by the
// staging/rename scheme, so cancelling never corrupts the live library.
func (m *feedManager) cancelJob() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil || !m.job.Running || m.cancel == nil {
		return false
	}
	m.cancel()
	return true
}

func (m *feedManager) sourceDir(id string) string {
	return filepath.Join(m.dir, id)
}

// --- HTTP transport ---

// feedHTTPClient builds the download client. Unlike newGuardedHTTPClient this
// one is deliberately allowed to reach the public internet through a proxy;
// the destinations come from the code-defined catalog, not from user input.
func feedHTTPClient(cfg SecurityFeedConfig) (*http.Client, error) {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		// Large archives stream for minutes; the client Timeout below bounds the
		// whole run, so don't also cap the response header wait too tightly.
		ResponseHeaderTimeout: 90 * time.Second,
	}
	if cfg.ProxyURL != "" {
		u, err := url.Parse(cfg.ProxyURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("代理地址无效：%s", cfg.ProxyURL)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("代理协议不支持：%s（仅 http/https/socks5）", u.Scheme)
		}
		tr.Proxy = http.ProxyURL(u)
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}
	if cfg.InsecureTLS {
		// Opt-in escape hatch for TLS-intercepting corporate proxies. The
		// payload is still integrity-checked by gzip/tar parsing and the
		// per-source size and file-count caps.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 - operator opt-in
	}
	return &http.Client{
		Transport: tr,
		Timeout:   time.Duration(cfg.TimeoutSec) * time.Second,
	}, nil
}

// feedURL applies the configured mirror to a github.com URL. Mirrors like
// ghfast.top / ghproxy expect the full original URL appended to their prefix.
func feedURL(cfg SecurityFeedConfig, raw string) string {
	if cfg.MirrorPrefix == "" {
		return raw
	}
	if !strings.HasPrefix(raw, "https://github.com/") && !strings.HasPrefix(raw, "https://api.github.com/") {
		return raw
	}
	return cfg.MirrorPrefix + raw
}

// resolveLatestRef asks GitHub for the newest release tag. A pinned fallback
// keeps updates working when the API is blocked or rate-limited — being one
// release behind beats shipping a years-old pinned tree.
func resolveLatestRef(ctx context.Context, client *http.Client, cfg SecurityFeedConfig, repo, fallback string) string {
	api := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL(cfg, api), nil)
	if err != nil {
		return fallback
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AIOps-Monitor-FeedUpdater/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fallback
	}
	var out struct {
		TagName string `json:"tag_name"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) != nil {
		return fallback
	}
	if tag := strings.TrimSpace(out.TagName); tag != "" {
		return tag
	}
	return fallback
}

// --- archive download & extraction ---

type extractStats struct {
	Files int
	Bytes int64
}

// feedSafeJoin rejects entries that would escape the destination directory.
// Archive path traversal ("../../etc/cron.d/x") is the classic way a poisoned
// mirror turns an update into remote code execution.
//
// Traversal is rejected outright rather than normalised away: an archive that
// contains "../" is not something we want to silently relocate and keep.
func feedSafeJoin(dst, name string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", false
		}
	}
	clean := path.Clean(name)
	if clean == "" || clean == "." {
		return "", false
	}
	full := filepath.Join(dst, filepath.FromSlash(clean))
	rel, err := filepath.Rel(dst, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}

// feedStripRoot removes the "<repo>-<ref>/" wrapper GitHub adds to archives.
func feedStripRoot(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	if i := strings.Index(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return ""
}

func feedWantFile(src FeedSource, rel string) bool {
	if src.Subdir != "" {
		prefix := strings.Trim(src.Subdir, "/") + "/"
		if !strings.HasPrefix(rel, prefix) {
			return false
		}
	}
	if len(src.Include) == 0 {
		return true
	}
	low := strings.ToLower(rel)
	for _, suf := range src.Include {
		if strings.HasSuffix(low, suf) {
			return true
		}
	}
	return false
}

func feedWriteEntry(dst, rel string, r io.Reader, mode os.FileMode) (int64, error) {
	full, ok := feedSafeJoin(dst, rel)
	if !ok {
		return 0, nil // silently skip traversal attempts
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return 0, err
	}
	if mode == 0 {
		mode = 0o640
	}
	// Never honour the archive's executable bits: detection content is data.
	f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	cerr := f.Close()
	if err != nil {
		return n, err
	}
	return n, cerr
}

// extractTarGz streams a GitHub tarball into dst, keeping only wanted files.
func extractTarGz(r io.Reader, dst string, src FeedSource) (extractStats, error) {
	var st extractStats
	gz, err := gzip.NewReader(r)
	if err != nil {
		return st, errNotGzip
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return st, fmt.Errorf("解压中断：%w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		rel := feedStripRoot(h.Name)
		if rel == "" || !feedWantFile(src, rel) {
			continue
		}
		if src.MaxFiles > 0 && st.Files >= src.MaxFiles {
			return st, fmt.Errorf("文件数超过上限 %d，已停止（源可能非预期）", src.MaxFiles)
		}
		n, err := feedWriteEntry(dst, rel, tr, os.FileMode(h.Mode))
		if err != nil {
			return st, err
		}
		if n > 0 {
			st.Files++
			st.Bytes += n
		}
		if src.MaxBytes > 0 && st.Bytes > src.MaxBytes {
			return st, fmt.Errorf("解压体积超过上限 %d MB，已停止", src.MaxBytes>>20)
		}
	}
	return st, nil
}

// errNotGzip marks a body that did not parse as gzip. Some acceleration
// mirrors answer with an HTML interstitial or a zip; retrying as a zipball
// recovers the update instead of failing the whole run.
var errNotGzip = errors.New("response is not a gzip archive")

// extractZip is the fallback for hosts whose mirror serves zipballs.
func extractZip(path string, dst string, src FeedSource) (extractStats, error) {
	var st extractStats
	zr, err := zip.OpenReader(path)
	if err != nil {
		return st, fmt.Errorf("解压 zip 失败：%w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rel := feedStripRoot(f.Name)
		if rel == "" || !feedWantFile(src, rel) {
			continue
		}
		if src.MaxFiles > 0 && st.Files >= src.MaxFiles {
			return st, fmt.Errorf("文件数超过上限 %d，已停止", src.MaxFiles)
		}
		rc, err := f.Open()
		if err != nil {
			return st, err
		}
		n, err := feedWriteEntry(dst, rel, rc, 0)
		_ = rc.Close()
		if err != nil {
			return st, err
		}
		if n > 0 {
			st.Files++
			st.Bytes += n
		}
		if src.MaxBytes > 0 && st.Bytes > src.MaxBytes {
			return st, fmt.Errorf("解压体积超过上限 %d MB，已停止", src.MaxBytes>>20)
		}
	}
	return st, nil
}

// errFeedRefMissing means the archive for that ref does not exist, which is the
// one failure worth retrying with a different ref.
var errFeedRefMissing = errors.New("archive not found for ref")

// feedProgress reports an update's progress. *feedManager implements it for
// UI-driven runs; nopFeedProgress covers the boot-time template install where
// there is no job to attach to.
type feedProgress interface {
	logf(format string, args ...any)
	setPhase(phase, current string, done int)
}

type nopFeedProgress struct{}

func (nopFeedProgress) logf(string, ...any)          {}
func (nopFeedProgress) setPhase(string, string, int) {}

// fetchFeedArchive pulls one ref's archive into dst. It prefers the tarball and
// retries as a zipball when the body is not gzip, which is what acceleration
// mirrors return when they inject an interstitial or re-pack the download.
// Returns the extraction stats and the URL actually used.
func fetchFeedArchive(ctx context.Context, client *http.Client, cfg SecurityFeedConfig, src FeedSource, ref, dst string, m feedProgress) (extractStats, string, error) {
	base := "https://github.com/" + src.Repo + "/archive/refs/" + refPathSegment(ref)
	tarURL := feedURL(cfg, base+".tar.gz")

	m.setPhase("download", src.Name, -1)
	resp, err := feedGet(ctx, client, tarURL)
	if err != nil {
		return extractStats{}, tarURL, errors.New(feedNetError(cfg, err))
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return extractStats{}, tarURL, errFeedRefMissing
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return extractStats{}, tarURL, fmt.Errorf("下载返回 HTTP %d（%s）", resp.StatusCode, truncateRun(tarURL, 120))
	}
	m.setPhase("extract", src.Name, -1)
	stats, err := extractTarGz(resp.Body, dst, src)
	_ = resp.Body.Close()
	if err == nil {
		return stats, tarURL, nil
	}
	if !errors.Is(err, errNotGzip) {
		return extractStats{}, tarURL, err
	}

	zipURL := feedURL(cfg, base+".zip")
	m.logf("%s：tar.gz 不可用，改用 zip 包重试", src.Name)
	_ = os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return extractStats{}, zipURL, err
	}
	m.setPhase("download", src.Name, -1)
	zresp, err := feedGet(ctx, client, zipURL)
	if err != nil {
		return extractStats{}, zipURL, errors.New(feedNetError(cfg, err))
	}
	defer zresp.Body.Close()
	if zresp.StatusCode != http.StatusOK {
		return extractStats{}, zipURL, fmt.Errorf("下载失败（tar.gz 非法，zip 返回 HTTP %d）；镜像地址可能不可用", zresp.StatusCode)
	}
	// zip needs random access, so it has to land on disk first.
	tmp, err := os.CreateTemp("", "aiops-feed-*.zip")
	if err != nil {
		return extractStats{}, zipURL, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	limit := src.MaxBytes
	if limit <= 0 {
		limit = 1 << 30
	}
	written, cerr := io.Copy(tmp, io.LimitReader(zresp.Body, limit+1))
	_ = tmp.Close()
	if cerr != nil {
		return extractStats{}, zipURL, errors.New(feedNetError(cfg, cerr))
	}
	if written > limit {
		return extractStats{}, zipURL, fmt.Errorf("下载体积超过上限 %d MB，已停止", limit>>20)
	}
	m.setPhase("extract", src.Name, -1)
	stats, err = extractZip(tmpName, dst, src)
	if err != nil {
		return extractStats{}, zipURL, err
	}
	return stats, zipURL, nil
}

func feedGet(ctx context.Context, client *http.Client, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "AIOps-Monitor-FeedUpdater/1.0")
	return client.Do(req)
}

// updateSource downloads one source into a staging dir and swaps it in only
// after a successful extraction, so a failed or cancelled run always leaves the
// previously working library in place.
func (m *feedManager) updateSource(ctx context.Context, client *http.Client, cfg SecurityFeedConfig, src FeedSource) FeedState {
	started := time.Now()
	st := FeedState{ID: src.ID}

	resolved := src.Ref
	if resolved == "" {
		m.setPhase("resolve", src.Name, -1)
		resolved = resolveLatestRef(ctx, client, cfg, src.Repo, src.RefFallback)
		if resolved != "" {
			m.logf("%s：解析到版本 %s", src.Name, resolved)
		}
	}

	tmpRoot := m.sourceDir(src.ID) + ".staging"
	_ = os.RemoveAll(tmpRoot)
	if err := os.MkdirAll(tmpRoot, 0o750); err != nil {
		st.Error = err.Error()
		return st
	}
	defer os.RemoveAll(tmpRoot)

	var stats extractStats
	var lastErr string
	got := false
	for _, ref := range src.refCandidates(resolved) {
		if ctx.Err() != nil {
			st.Error = "已取消"
			return st
		}
		st.Ref = ref
		s, dl, err := fetchFeedArchive(ctx, client, cfg, src, ref, tmpRoot, m)
		st.Source = dl
		if err == nil {
			stats, got = s, true
			break
		}
		lastErr = err.Error()
		// Only a missing archive is worth trying the next ref for; a proxy or
		// TLS failure will fail identically for every candidate.
		if !errors.Is(err, errFeedRefMissing) {
			break
		}
		m.logf("%s：ref %s 不存在，尝试下一个", src.Name, ref)
		_ = os.RemoveAll(tmpRoot)
		if err := os.MkdirAll(tmpRoot, 0o750); err != nil {
			st.Error = err.Error()
			return st
		}
	}
	if !got {
		st.Error = lastErr
		if st.Error == "" {
			st.Error = "下载失败"
		}
		return st
	}
	if stats.Files == 0 {
		st.Error = "解压后没有匹配文件，源目录或过滤条件可能已变更"
		return st
	}

	m.setPhase("publish", src.Name, -1)
	live := m.sourceDir(src.ID)
	old := live + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(live); err == nil {
		if err := os.Rename(live, old); err != nil {
			st.Error = "切换目录失败：" + err.Error()
			return st
		}
	}
	if err := os.Rename(tmpRoot, live); err != nil {
		_ = os.Rename(old, live) // put the working tree back
		st.Error = "发布目录失败：" + err.Error()
		return st
	}
	_ = os.RemoveAll(old)

	st.Files = stats.Files
	st.Bytes = stats.Bytes
	st.UpdatedAt = time.Now().Unix()
	st.DurationMS = time.Since(started).Milliseconds()
	return st
}

// refPathSegment maps a ref to the archive path GitHub expects: tags live under
// refs/tags, everything else is treated as a branch head.
func refPathSegment(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "master"
	}
	if strings.HasPrefix(ref, "v") || strings.Contains(ref, ".") {
		return "tags/" + ref
	}
	return "heads/" + ref
}

// feedNetError turns a transport failure into advice, because "context deadline
// exceeded" tells an operator nothing about the proxy they need to configure.
func feedNetError(cfg SecurityFeedConfig, err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		return "已取消"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		if cfg.ProxyURL == "" && cfg.MirrorPrefix == "" {
			return "下载超时：直连 GitHub 不通。请在「情报源」中配置代理或加速镜像后重试。"
		}
		return "下载超时：请检查代理/镜像是否可用，或调大超时时间。"
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "server misbehaving"):
		return "域名解析失败：请检查服务端 DNS 或改用加速镜像。"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "proxyconnect"):
		return "无法连接到代理：" + truncateRun(cfg.ProxyURL, 80)
	case strings.Contains(msg, "certificate"):
		return "TLS 证书校验失败：若使用了解密型代理，可临时勾选「跳过证书校验」。"
	}
	return truncateRun(msg, 200)
}

// runUpdate executes an update pass over the given sources. It is the single
// entry point for both manual refresh and the scheduler.
func (m *feedManager) runUpdate(cfg SecurityFeedConfig, sources []FeedSource, actor string) (*FeedJob, error) {
	cfg = cfg.withDefaults()
	if len(sources) == 0 {
		return nil, errors.New("没有启用任何情报源")
	}
	client, err := feedHTTPClient(cfg)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.job != nil && m.job.Running {
		m.mu.Unlock()
		return nil, errors.New("已有更新任务在运行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSec)*time.Second)
	job := &FeedJob{
		ID:        fmt.Sprintf("feed-%d", time.Now().UnixNano()),
		Running:   true,
		StartedAt: time.Now().Unix(),
		Total:     len(sources),
		Phase:     "resolve",
		Actor:     actor,
	}
	m.job = job
	m.cancel = cancel
	m.mu.Unlock()

	// 情报源同步解析的是第三方下载下来的内容：坏包不该把进程带走，
	// 而 defer cancel() 在 recover 之后仍会执行，任务状态不会悬着。
	safeGo("security-feed-sync", func() {
		defer cancel()
		for i, src := range sources {
			if ctx.Err() != nil {
				m.logf("已取消，跳过剩余 %d 个源", len(sources)-i)
				break
			}
			m.logf("开始更新 %s", src.Name)
			st := m.updateSource(ctx, client, cfg, src)
			m.mu.Lock()
			m.states[src.ID] = st
			m.saveStatesLocked()
			if m.job != nil {
				m.job.Results = append(m.job.Results, st)
				m.job.Done = i + 1
			}
			m.mu.Unlock()
			if st.Error != "" {
				m.logf("%s 失败：%s", src.Name, st.Error)
				slog.Warn("security feed update failed", "source", src.ID, "err", st.Error)
			} else {
				m.logf("%s 完成：%d 个文件 / %.1f MB / %.1fs",
					src.Name, st.Files, float64(st.Bytes)/(1<<20), float64(st.DurationMS)/1000)
			}
		}
		m.mu.Lock()
		if m.job != nil {
			m.job.Running = false
			m.job.Phase = "done"
			m.job.Current = ""
			m.job.EndedAt = time.Now().Unix()
			if ctx.Err() != nil {
				m.job.Error = "任务已取消或超时"
			}
		}
		m.cancel = nil
		cb := m.onUpdated
		m.mu.Unlock()
		if cb != nil {
			cb()
		}
	})

	return m.currentJob(), nil
}
