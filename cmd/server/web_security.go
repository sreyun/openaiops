package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const webSecMaxScans = 200

// WebScanTarget is a Nuclei scan target with optional schedule and auth.
type WebScanTarget struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	BaseURL         string   `json:"base_url"`
	Enabled         bool     `json:"enabled"`
	AuthType        string   `json:"auth_type,omitempty"` // none|basic|bearer|cookie|header|form|header_body
	AuthUser        string   `json:"auth_user,omitempty"`
	AuthPass        string   `json:"auth_pass,omitempty"`
	AuthHeader      string   `json:"auth_header,omitempty"` // multi-line "Name: Value"
	AuthBody        string   `json:"auth_body,omitempty"`   // login / warmup request body
	AuthLoginURL    string   `json:"auth_login_url,omitempty"`
	AuthMethod      string   `json:"auth_method,omitempty"` // GET|POST|PUT
	AuthContentType string   `json:"auth_content_type,omitempty"`
	Include         []string `json:"include,omitempty"`
	Exclude         []string `json:"exclude,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	Templates       []string `json:"templates,omitempty"`
	// ScanURLs are additional absolute URLs (e.g. from OpenAPI import) passed as Nuclei -u.
	ScanURLs     []string          `json:"scan_urls,omitempty"`
	Schedule     *PlaybookSchedule `json:"schedule,omitempty"`
	AllowPrivate bool              `json:"allow_private,omitempty"` // per-target; still needs global allow
	LastScanAt   int64             `json:"last_scan_at,omitempty"`
	CreatedAt    int64             `json:"created_at,omitempty"`
	UpdatedAt    int64             `json:"updated_at,omitempty"`
}

// WebSecurityConfig controls Nuclei path, limits, and persisted targets.
type WebSecurityConfig struct {
	NucleiPath      string `json:"nuclei_path,omitempty"`
	TemplatesDir    string `json:"templates_dir,omitempty"`
	Severity        string `json:"severity,omitempty"` // critical,high,medium,low,info
	RateLimit       int    `json:"rate_limit,omitempty"`
	Concurrency     int    `json:"concurrency,omitempty"`
	TimeoutSec      int    `json:"timeout_sec,omitempty"`
	AllowPrivate    bool   `json:"allow_private"`
	UpdateTemplates bool   `json:"update_templates"`
	ScanConcurrency int    `json:"scan_concurrency,omitempty"`
	// Built-in checks default ON; opt out with disable_builtin_checks.
	DisableBuiltin bool            `json:"disable_builtin_checks,omitempty"`
	DefaultsGen    int             `json:"defaults_gen,omitempty"` // bumps one-time shipped-default migrations
	AutoAISummary  bool            `json:"auto_ai_summary,omitempty"`
	Targets        []WebScanTarget `json:"targets,omitempty"`
}

// webSecDefaultsGen is bumped when shipped defaults change and existing installs
// should be soft-migrated once (exact old values only).
const webSecDefaultsGen = 2

func (c WebSecurityConfig) withDefaults() WebSecurityConfig {
	if c.NucleiPath == "" {
		c.NucleiPath = "nuclei"
	}
	if c.Severity == "" {
		c.Severity = "critical,high,medium,low,info"
	}
	// Defaults tuned for real Nuclei workloads (CVE/misconfig packs are large).
	// Previously timeout=300s / nuclei -c=10 / scan_concurrency=1 caused frequent
	// "超时" and serial queues that felt like hangs.
	if c.RateLimit <= 0 {
		c.RateLimit = 120
	}
	if c.RateLimit > 500 {
		c.RateLimit = 500
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 25
	}
	if c.Concurrency > 100 {
		c.Concurrency = 100
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 900
	}
	if c.TimeoutSec < 60 {
		c.TimeoutSec = 60
	}
	if c.TimeoutSec > 7200 {
		c.TimeoutSec = 7200
	}
	if c.ScanConcurrency <= 0 {
		c.ScanConcurrency = 3
	}
	if c.ScanConcurrency > 8 {
		c.ScanConcurrency = 8
	}
	return c
}

func (cs *ConfigStore) WebSecurity() WebSecurityConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.cfg.WebSecurity.withDefaults()
}

func (cs *ConfigStore) SecurityFeeds() SecurityFeedConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.cfg.SecurityFeeds.withDefaults()
}

func (cs *ConfigStore) SetSecurityFeeds(c SecurityFeedConfig) error {
	c = c.withDefaults()
	if c.ProxyURL != "" {
		if _, err := feedHTTPClient(c); err != nil {
			return err
		}
	}
	if c.MirrorPrefix != "" {
		u, err := url.Parse(c.MirrorPrefix)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("加速镜像地址无效，需形如 https://ghfast.top/")
		}
	}
	// Unknown IDs would silently disable a source after a catalog rename.
	for _, id := range c.Sources {
		if _, ok := feedSourceByID(id); !ok {
			return fmt.Errorf("未知情报源：%s", truncateRun(id, 60))
		}
	}
	cs.mu.Lock()
	cs.cfg.SecurityFeeds = c
	cs.mu.Unlock()
	return cs.save()
}

// markFeedAutoRun records the scheduler's last pass so a restart loop cannot
// re-trigger a multi-hundred-megabyte download on every boot.
func (cs *ConfigStore) markFeedAutoRun(ts int64) {
	cs.mu.Lock()
	cs.cfg.SecurityFeeds.LastAutoRunSec = ts
	cs.mu.Unlock()
	_ = cs.save()
}

// migrateWebSecurityDefaultsOnce rewrites previously shipped defaults that caused
// widespread timeouts / serial queues. Runs at most once per install (DefaultsGen).
func (cs *ConfigStore) migrateWebSecurityDefaultsOnce() bool {
	cs.mu.Lock()
	c := cs.cfg.WebSecurity
	if c.DefaultsGen >= webSecDefaultsGen {
		cs.mu.Unlock()
		return false
	}
	changed := false
	if c.TimeoutSec == 0 || c.TimeoutSec == 300 {
		c.TimeoutSec = 900
		changed = true
	}
	if c.Concurrency == 0 || c.Concurrency == 10 {
		c.Concurrency = 25
		changed = true
	}
	if c.RateLimit == 0 || c.RateLimit == 50 {
		c.RateLimit = 120
		changed = true
	}
	if c.ScanConcurrency == 0 || c.ScanConcurrency == 1 {
		c.ScanConcurrency = 3
		changed = true
	}
	c.DefaultsGen = webSecDefaultsGen
	cs.cfg.WebSecurity = c
	cs.mu.Unlock()
	if changed {
		_ = cs.save()
		slog.Info("web security defaults migrated",
			"timeout_sec", c.TimeoutSec, "concurrency", c.Concurrency,
			"rate_limit", c.RateLimit, "scan_concurrency", c.ScanConcurrency)
	} else {
		_ = cs.save() // persist DefaultsGen even when values already customized
	}
	return changed
}

func (cs *ConfigStore) SetWebSecurity(c WebSecurityConfig) error {
	cs.mu.Lock()
	c = c.withDefaults()
	if c.DefaultsGen < webSecDefaultsGen {
		c.DefaultsGen = webSecDefaultsGen
	}
	for i := range c.Targets {
		if err := sanitizeWebTarget(&c.Targets[i], c.AllowPrivate); err != nil {
			cs.mu.Unlock()
			return err
		}
	}
	cs.cfg.WebSecurity = c
	cs.mu.Unlock()
	return cs.save()
}

func (cs *ConfigStore) UpsertWebTarget(t WebScanTarget) (WebScanTarget, error) {
	cs.mu.Lock()
	cfg := cs.cfg.WebSecurity.withDefaults()
	if err := sanitizeWebTarget(&t, cfg.AllowPrivate); err != nil {
		cs.mu.Unlock()
		return t, err
	}
	now := time.Now().Unix()
	if t.ID == "" {
		t.ID = "wt-" + randomHex(8)
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	found := false
	for i, x := range cfg.Targets {
		if x.ID == t.ID {
			if isMaskedSecret(t.AuthPass) {
				t.AuthPass = x.AuthPass
			}
			if isMaskedSecret(t.AuthHeader) || strings.Contains(t.AuthHeader, "********") {
				t.AuthHeader = x.AuthHeader
			}
			if isMaskedSecret(t.AuthBody) || strings.Contains(t.AuthBody, "********") {
				t.AuthBody = x.AuthBody
			}
			t.CreatedAt = x.CreatedAt
			t.LastScanAt = x.LastScanAt
			cfg.Targets[i] = t
			found = true
			break
		}
	}
	if err := validateWebTargetAuth(t); err != nil {
		cs.mu.Unlock()
		return t, err
	}
	if !found {
		cfg.Targets = append(cfg.Targets, t)
	}
	cs.cfg.WebSecurity = cfg
	cs.mu.Unlock()
	return t, cs.save()
}

func (cs *ConfigStore) DeleteWebTarget(id string) bool {
	cs.mu.Lock()
	cfg := cs.cfg.WebSecurity
	n := make([]WebScanTarget, 0, len(cfg.Targets))
	ok := false
	for _, t := range cfg.Targets {
		if t.ID == id {
			ok = true
			continue
		}
		n = append(n, t)
	}
	if !ok {
		cs.mu.Unlock()
		return false
	}
	cfg.Targets = n
	cs.cfg.WebSecurity = cfg
	cs.mu.Unlock()
	_ = cs.save()
	return true
}

func (cs *ConfigStore) GetWebTarget(id string) (WebScanTarget, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	for _, t := range cs.cfg.WebSecurity.Targets {
		if t.ID == id {
			return t, true
		}
	}
	return WebScanTarget{}, false
}

// UpdateWebTargetScanURLs merges or replaces ScanURLs under the config lock
// so concurrent target edits cannot drop unrelated fields.
func (cs *ConfigStore) UpdateWebTargetScanURLs(id string, urls []string, replace bool) (WebScanTarget, error) {
	cs.mu.Lock()
	cfg := cs.cfg.WebSecurity.withDefaults()
	idx := -1
	for i := range cfg.Targets {
		if cfg.Targets[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		cs.mu.Unlock()
		return WebScanTarget{}, fmt.Errorf("target not found")
	}
	t := cfg.Targets[idx]
	if replace {
		t.ScanURLs = append([]string{}, urls...)
	} else {
		t.ScanURLs = mergeUniqueURLs(t.ScanURLs, urls, 80)
	}
	if err := sanitizeWebTarget(&t, cfg.AllowPrivate); err != nil {
		cs.mu.Unlock()
		return WebScanTarget{}, err
	}
	t.UpdatedAt = time.Now().Unix()
	cfg.Targets[idx] = t
	cs.cfg.WebSecurity = cfg
	cs.mu.Unlock()
	if err := cs.save(); err != nil {
		return WebScanTarget{}, err
	}
	return t, nil
}

func (cs *ConfigStore) touchWebTargetScan(id string, at int64) {
	cs.mu.Lock()
	found := false
	for i, t := range cs.cfg.WebSecurity.Targets {
		if t.ID == id {
			cs.cfg.WebSecurity.Targets[i].LastScanAt = at
			found = true
			break
		}
	}
	cs.mu.Unlock()
	if found {
		_ = cs.save()
	}
}

var webTagWhitelist = map[string]bool{
	"cves": true, "misconfig": true, "exposures": true, "default-logins": true,
	"vulnerabilities": true, "technologies": true, "panel": true, "xss": true,
	"sqli": true, "lfi": true, "rce": true, "token-spray": true, "iot": true,
	"network": true, "dns": true, "fuzz": true, "osint": true, "misc": true,
}

func sanitizeWebTarget(t *WebScanTarget, globalAllowPrivate bool) error {
	t.Name = strings.TrimSpace(t.Name)
	t.BaseURL = strings.TrimSpace(t.BaseURL)
	if t.Name == "" {
		return fmt.Errorf("name required")
	}
	u, err := url.Parse(t.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("base_url must be http(s) URL")
	}
	// Private/reserved targets require the admin global flag — per-target
	// allow_private alone must not bypass SSRF guards (operators can CRUD targets).
	if t.AllowPrivate && !globalAllowPrivate {
		return fmt.Errorf("「允许私网」需管理员先在引擎配置中开启全局开关")
	}
	if err := assertURLAllowed(t.BaseURL, globalAllowPrivate); err != nil {
		return err
	}
	cleanURLs := make([]string, 0, len(t.ScanURLs))
	seenU := map[string]bool{}
	for _, raw := range t.ScanURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if err := assertURLAllowed(raw, globalAllowPrivate); err != nil {
			return fmt.Errorf("scan_urls: %w", err)
		}
		k := strings.ToLower(raw)
		if seenU[k] {
			continue
		}
		seenU[k] = true
		cleanURLs = append(cleanURLs, raw)
		if len(cleanURLs) >= 80 {
			break
		}
	}
	t.ScanURLs = cleanURLs
	t.AuthType = strings.ToLower(strings.TrimSpace(t.AuthType))
	if t.AuthType == "" {
		t.AuthType = "none"
	}
	switch t.AuthType {
	case "none", "basic", "bearer", "cookie", "header", "form", "login", "header_body":
	default:
		return fmt.Errorf("不支持的鉴权类型：%s", t.AuthType)
	}
	if t.AuthType == "login" {
		t.AuthType = "form"
	}
	t.AuthLoginURL = strings.TrimSpace(t.AuthLoginURL)
	t.AuthMethod = strings.ToUpper(strings.TrimSpace(t.AuthMethod))
	if t.AuthMethod == "" {
		t.AuthMethod = "POST"
	}
	switch t.AuthMethod {
	case "GET", "POST", "PUT", "PATCH":
	default:
		return fmt.Errorf("鉴权请求方法无效")
	}
	t.AuthContentType = strings.TrimSpace(t.AuthContentType)
	t.AuthUser = strings.TrimSpace(t.AuthUser)
	t.AuthHeader = strings.TrimSpace(t.AuthHeader)
	t.AuthBody = strings.TrimSpace(t.AuthBody)
	switch t.AuthType {
	case "basic":
		if t.AuthUser == "" {
			return fmt.Errorf("Basic 鉴权需填写用户名")
		}
	case "bearer":
		if isMaskedSecret(t.AuthPass) && strings.TrimSpace(t.AuthHeader) == "" {
			// may be filled from existing secret on upsert
		}
	case "form", "header_body":
		if t.AuthLoginURL == "" {
			return fmt.Errorf("表单/预认证需填写登录或预认证 URL")
		}
	}
	var tags []string
	for _, tag := range t.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if !webTagWhitelist[tag] {
			return fmt.Errorf("tag not allowed: %s", tag)
		}
		tags = append(tags, tag)
	}
	t.Tags = tags
	if t.Schedule != nil {
		if err := sanitizeSchedule(t.Schedule); err != nil {
			return err
		}
		// Web scans are heavier than host checks; floor interval at 15 minutes.
		if t.Schedule.Enabled && t.Schedule.Kind == "interval" && t.Schedule.IntervalMin < 15 {
			t.Schedule.IntervalMin = 15
		}
	}
	return nil
}

func ipBlockedWhenPrivateDenied(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() || ip.IsUnspecified()
}

func assertURLAllowed(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("仅允许 http/https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if !allowPrivate {
		if ip := net.ParseIP(host); ip != nil {
			if ipBlockedWhenPrivateDenied(ip) {
				return fmt.Errorf("禁止扫描私网/保留地址（需管理员开启「允许私网」）")
			}
		} else {
			// Fail closed: unresolved hosts must not bypass SSRF guards.
			addrs, err := net.LookupIP(host)
			if err != nil {
				return fmt.Errorf("无法解析目标主机（已拒绝）：%v", err)
			}
			if len(addrs) == 0 {
				return fmt.Errorf("目标主机无解析结果（已拒绝）")
			}
			for _, ip := range addrs {
				if ipBlockedWhenPrivateDenied(ip) {
					return fmt.Errorf("目标解析到私网地址（需管理员开启「允许私网」）")
				}
			}
		}
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WebFinding is one Nuclei match.
type WebFinding struct {
	TemplateID       string   `json:"template_id"`
	Name             string   `json:"name"`
	MatcherName      string   `json:"matcher_name,omitempty"` // e.g. x-frame-options for missing-headers
	Severity         string   `json:"severity"`
	URL              string   `json:"url"`
	MatchedAt        string   `json:"matched_at,omitempty"`
	Description      string   `json:"description,omitempty"`
	Remediation      string   `json:"remediation,omitempty"`
	CurlCommand      string   `json:"curl_command,omitempty"`
	Type             string   `json:"type,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	ExtractedResults []string `json:"extracted_results,omitempty"`
	Status           string   `json:"status,omitempty"` // open|ack|false_positive|resolved
	StatusNote       string   `json:"status_note,omitempty"`
	// OWASP / Compliance make a finding audit-ready without manual mapping.
	OWASP      string          `json:"owasp,omitempty"`
	Compliance []ComplianceRef `json:"compliance,omitempty"`
}

// WebScanResult is one Nuclei run.
type WebScanResult struct {
	ID           string            `json:"id"`
	Label        string            `json:"label,omitempty"` // human-readable batch title
	Seq          int               `json:"seq,omitempty"`
	TargetID     string            `json:"target_id"`
	TargetName   string            `json:"target_name,omitempty"`
	BaseURL      string            `json:"base_url"`
	StartedAt    int64             `json:"started_at"`
	FinishedAt   int64             `json:"finished_at,omitempty"`
	Status       string            `json:"status"`
	Error        string            `json:"error,omitempty"`
	Findings     []WebFinding      `json:"findings"`
	Summary      map[string]int    `json:"summary"`
	Operator     string            `json:"operator,omitempty"`
	Trigger      string            `json:"trigger,omitempty"`
	Report       *ScanReport       `json:"report,omitempty"`
	BaselineDiff *ScanBaselineDiff `json:"baseline_diff,omitempty"`
	AISummary    string            `json:"ai_summary,omitempty"`
	AISummaryAt  int64             `json:"ai_summary_at,omitempty"`
	OWASP        map[string]int    `json:"owasp,omitempty"`      // OWASP Top 10 category → count
	Compliance   map[string]int    `json:"compliance,omitempty"` // framework → failing controls
	Engines      []string          `json:"engines,omitempty"`    // builtin|nuclei
	EngineNote   string            `json:"engine_note,omitempty"`
}

// ScanReport is a structured professional report for export / AI.
type ScanReport struct {
	Title       string         `json:"title"`
	GeneratedAt int64          `json:"generated_at"`
	Target      string         `json:"target"`
	Executive   string         `json:"executive"`
	RiskCounts  map[string]int `json:"risk_counts"`
	Findings    []WebFinding   `json:"findings"`
	Remediation []string       `json:"remediation,omitempty"`
}

type webScanManager struct {
	mu      sync.Mutex
	scans   []*WebScanResult
	lastRun map[string]int64
	dir     string
	seq     int

	// Dynamic scan slots: config ScanConcurrency can change at runtime.
	// A fixed buffered channel created at startup used to ignore later config saves.
	concMu   sync.Mutex
	concCond *sync.Cond
	maxConc  int
	active   int
}

func newWebScanManager(dir string, concurrency int) *webScanManager {
	m := &webScanManager{
		scans:   make([]*WebScanResult, 0, 32),
		lastRun: map[string]int64{},
		dir:     dir,
		maxConc: clampScanConcurrency(concurrency),
	}
	m.concCond = sync.NewCond(&m.concMu)
	m.load()
	for _, sc := range m.scans {
		if sc != nil && sc.Seq > m.seq {
			m.seq = sc.Seq
		}
	}
	if m.seq < len(m.scans) {
		m.seq = len(m.scans)
	}
	return m
}

func clampScanConcurrency(n int) int {
	if n < 1 {
		return 1
	}
	if n > 8 {
		return 8
	}
	return n
}

func (m *webScanManager) setScanConcurrency(n int) {
	m.concMu.Lock()
	m.maxConc = clampScanConcurrency(n)
	m.concCond.Broadcast()
	m.concMu.Unlock()
}

func (m *webScanManager) acquireScanSlot() {
	m.concMu.Lock()
	for m.active >= m.maxConc {
		m.concCond.Wait()
	}
	m.active++
	m.concMu.Unlock()
}

func (m *webScanManager) releaseScanSlot() {
	m.concMu.Lock()
	if m.active > 0 {
		m.active--
	}
	m.concCond.Signal()
	m.concMu.Unlock()
}

// allocScanMeta builds a short readable id + label, e.g.
// id=ws-003-0725-1806-a3f1  label=芒果系统 · #003 · 07-25 18:06
func (m *webScanManager) allocScanMeta(name string) (id, label string, seq int) {
	m.mu.Lock()
	m.seq++
	seq = m.seq
	m.mu.Unlock()
	now := time.Now()
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名目标"
	}
	id = fmt.Sprintf("ws-%03d-%s-%s", seq, now.Format("0102-1504"), randomHex(2))
	label = fmt.Sprintf("%s · #%03d · %s", name, seq, now.Format("01-02 15:04"))
	return id, label, seq
}

func (m *webScanManager) path() string {
	return filepath.Join(m.dir, "web_scans.json")
}

func (m *webScanManager) load() {
	b, err := os.ReadFile(m.path())
	if err != nil {
		return
	}
	var list []*WebScanResult
	if json.Unmarshal(b, &list) != nil {
		return
	}
	now := time.Now().Unix()
	dirty := false
	for _, sc := range list {
		if sc != nil && sc.Status == "running" {
			sc.Status = "failed"
			sc.Error = "服务重启，扫描中断"
			if sc.FinishedAt == 0 {
				sc.FinishedAt = now
			}
			dirty = true
		}
	}
	m.scans = list
	if dirty {
		m.saveLocked()
	}
}

func (m *webScanManager) saveLocked() {
	if m.dir == "" {
		return
	}
	_ = os.MkdirAll(m.dir, 0o750)
	b, err := json.Marshal(m.scans)
	if err != nil {
		return
	}
	tmp := m.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, m.path())
}

// finishIfRunning applies a completion under lock only when the scan is still
// running — cancels/timeouts win over a stale worker finishing later.
func (m *webScanManager) finishIfRunning(id string, apply func(live *WebScanResult)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.scans {
		if s == nil || s.ID != id {
			continue
		}
		if s.Status != "running" {
			return false
		}
		apply(s)
		m.saveLocked()
		return true
	}
	return false
}

// trimLocked keeps running scans and the newest history within webSecMaxScans.
func (m *webScanManager) trimLocked() {
	if len(m.scans) <= webSecMaxScans {
		return
	}
	var running, rest []*WebScanResult
	for _, sc := range m.scans {
		if sc != nil && sc.Status == "running" {
			running = append(running, sc)
		} else if sc != nil {
			rest = append(rest, sc)
		}
	}
	budget := webSecMaxScans - len(running)
	if budget < 0 {
		budget = 0
	}
	if len(rest) > budget {
		rest = rest[:budget]
	}
	m.scans = append(running, rest...)
}

func (m *webScanManager) list(limit int) []*WebScanResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > len(m.scans) {
		limit = len(m.scans)
	}
	out := make([]*WebScanResult, 0, limit)
	for i := 0; i < limit; i++ {
		if m.scans[i] == nil {
			continue
		}
		out = append(out, summarizeWebScanForList(m.scans[i]))
	}
	return out
}

func summarizeWebScanForList(s *WebScanResult) *WebScanResult {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Findings = nil
	if s.Summary != nil {
		cp.Summary = make(map[string]int, len(s.Summary))
		for k, v := range s.Summary {
			cp.Summary[k] = v
		}
	}
	return &cp
}

func (m *webScanManager) get(id string) *WebScanResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.scans {
		if s != nil && s.ID == id {
			cp := *s
			return &cp
		}
	}
	return nil
}

func (m *webScanManager) hasRunning(targetID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapStuckLocked(0)
	for _, s := range m.scans {
		if s != nil && s.TargetID == targetID && s.Status == "running" {
			return true
		}
	}
	return false
}

func (m *webScanManager) reapStuck(timeoutSec int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reapStuckLocked(timeoutSec)
}

func (m *webScanManager) reapStuckLocked(timeoutSec int) int {
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	grace := int64(120)
	limit := int64(timeoutSec) + grace
	now := time.Now().Unix()
	n := 0
	for _, sc := range m.scans {
		if sc == nil || sc.Status != "running" {
			continue
		}
		if sc.StartedAt > 0 && now-sc.StartedAt > limit {
			sc.Status = "failed"
			sc.Error = fmt.Sprintf("扫描超时中断（超过 %ds）", limit)
			sc.FinishedAt = now
			n++
			// 扫描超时的形态是「安全页面上这台机器一直没有新结果」——看起来像没扫，
			// 其实是每次都跑到一半被掐掉。反复超时说明预算或目标规模不对，属于要人
			// 处理的一类；进归口后连续 3 次同因就会开事件。
			reportFault("scan", "web_security_timeout", "warning", "",
				fmt.Sprintf("Web 漏洞扫描「%s」超时中断（超过 %ds），本次结果作废", firstNonEmptyOrDash(sc.TargetName, sc.TargetID), limit), "")
		}
	}
	if n > 0 {
		m.saveLocked()
	}
	return n
}

func (m *webScanManager) cancelScan(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sc := range m.scans {
		if sc == nil || sc.ID != id {
			continue
		}
		if sc.Status != "running" {
			return false
		}
		sc.Status = "failed"
		sc.Error = "已取消"
		sc.FinishedAt = time.Now().Unix()
		m.saveLocked()
		return true
	}
	return false
}

// nucleiJSONL is a minimal subset of Nuclei -jsonl output.
type nucleiJSONL struct {
	TemplateID string `json:"template-id"`
	Info       struct {
		Name        string   `json:"name"`
		Severity    string   `json:"severity"`
		Description string   `json:"description"`
		Remediation string   `json:"remediation"`
		Tags        []string `json:"tags"`
	} `json:"info"`
	Type             string   `json:"type"`
	Host             string   `json:"host"`
	MatchedAt        string   `json:"matched-at"`
	MatcherName      string   `json:"matcher-name"`
	ExtractedResults []string `json:"extracted-results"`
	CurlCommand      string   `json:"curl-command"`
}

func parseNucleiJSONL(r io.Reader) []WebFinding {
	var out []WebFinding
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	skipped := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var n nucleiJSONL
		if json.Unmarshal([]byte(line), &n) != nil {
			skipped++
			continue
		}
		sev := strings.ToLower(n.Info.Severity)
		if sev == "" {
			sev = "info"
		}
		url := n.MatchedAt
		if url == "" {
			url = n.Host
		}
		matcher := strings.TrimSpace(n.MatcherName)
		out = append(out, WebFinding{
			TemplateID:       n.TemplateID,
			Name:             findingDisplayName(n.Info.Name, matcher),
			MatcherName:      matcher,
			Severity:         sev,
			URL:              url,
			MatchedAt:        n.MatchedAt,
			Description:      n.Info.Description,
			Remediation:      coalesceRemediation(n.Info.Remediation, n.Info.Name, sev, matcher, n.TemplateID),
			CurlCommand:      redactCurlCommand(n.CurlCommand),
			Type:             n.Type,
			Tags:             append([]string(nil), n.Info.Tags...),
			ExtractedResults: append([]string(nil), n.ExtractedResults...),
		})
	}
	if err := sc.Err(); err != nil {
		slog.Warn("nuclei jsonl scanner error", "err", err, "parsed", len(out), "skipped", skipped)
	} else if skipped > 0 {
		slog.Warn("nuclei jsonl skipped invalid lines", "skipped", skipped, "parsed", len(out))
	}
	return out
}

// findingDisplayName keeps multi-matcher templates distinguishable in the UI
// (e.g. http-missing-security-headers emits one event per missing header).
func findingDisplayName(infoName, matcher string) string {
	infoName = strings.TrimSpace(infoName)
	matcher = strings.TrimSpace(matcher)
	if matcher == "" {
		return infoName
	}
	if infoName == "" {
		return matcher
	}
	return infoName + " [" + matcher + "]"
}

func redactCurlCommand(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Strip common secret-bearing header / form fragments from Nuclei curl PoC.
	type pair struct {
		re   *regexp.Regexp
		repl string
	}
	rules := []pair{
		{regexp.MustCompile(`(?i)(Authorization:\s*)([^'"]+)`), "${1}********"},
		{regexp.MustCompile(`(?i)(Cookie:\s*)([^'"]+)`), "${1}********"},
		{regexp.MustCompile(`(?i)(password=)([^&\s'"]+)`), "${1}********"},
		{regexp.MustCompile(`(?i)(passwd=)([^&\s'"]+)`), "${1}********"},
		{regexp.MustCompile(`(?i)(token=)([^&\s'"]+)`), "${1}********"},
		{regexp.MustCompile(`(?i)(api[_-]?key=)([^&\s'"]+)`), "${1}********"},
	}
	out := s
	for _, r := range rules {
		out = r.re.ReplaceAllString(out, r.repl)
	}
	return out
}

func coalesceRemediation(nuclei, name, sev, matcher, templateID string) string {
	matcher = strings.TrimSpace(matcher)
	templateID = strings.TrimSpace(templateID)
	nuclei = strings.TrimSpace(nuclei)
	if nuclei != "" {
		// Prefix matcher so multi-matcher templates with a shared Nuclei tip stay distinct.
		if matcher != "" && !strings.Contains(strings.ToLower(nuclei), strings.ToLower(matcher)) {
			return fmt.Sprintf("[%s] %s", matcher, nuclei)
		}
		return nuclei
	}
	// Nuclei's missing-headers template has empty remediation; generate per-header tips.
	if (templateID == "http-missing-security-headers" || strings.Contains(strings.ToLower(name), "missing security header")) && matcher != "" {
		return fmt.Sprintf("在 HTTP 响应中配置安全头「%s」（网关/反向代理或应用统一下发），并验证非跳转响应中生效。", matcher)
	}
	if matcher != "" {
		return fmt.Sprintf("按模板「%s」匹配项「%s」(%s) 加固：升级组件、关闭暴露面或加强访问控制。", name, matcher, sev)
	}
	return fmt.Sprintf("按模板「%s」(%s) 的官方修复建议加固：升级组件、关闭暴露面或加强访问控制。", name, sev)
}

func buildWebScanReport(target WebScanTarget, findings []WebFinding) *ScanReport {
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	exec := fmt.Sprintf("对「%s」(%s) 完成 Web 漏洞扫描，共 %d 条命中（危急 %d / 高危 %d / 中危 %d / 低危 %d / 信息 %d）。",
		target.Name, target.BaseURL, len(findings),
		counts["critical"], counts["high"], counts["medium"], counts["low"], counts["info"])
	switch {
	case counts["critical"] > 0:
		exec += "存在严重漏洞，请立即处置。"
	case counts["high"] > 0:
		exec += "存在高危问题，建议优先修复。"
	case len(findings) == 0:
		exec = fmt.Sprintf("对「%s」(%s) 扫描完成，未发现匹配当前模板集的问题（不代表绝对无风险；可扩大模板范围或启用信息级检测）。",
			target.Name, target.BaseURL)
	default:
		exec += "请按严重度分批修复，并保留本报告作为审计留存。"
	}
	tips := uniqueRemediationTips(findings, 15)
	sort.Slice(findings, func(i, j int) bool {
		return sevRank(findings[i].Severity) > sevRank(findings[j].Severity)
	})
	return &ScanReport{
		Title:       "Web 漏洞扫描报告 — " + target.Name,
		GeneratedAt: time.Now().Unix(),
		Target:      target.BaseURL,
		Executive:   exec,
		RiskCounts:  counts,
		Findings:    findings,
		Remediation: tips,
	}
}

// uniqueRemediationTips collapses identical advice from multi-matcher templates
// (e.g. 10× generic "HTTP Missing Security Headers" tips → distinct / once).
func uniqueRemediationTips(findings []WebFinding, limit int) []string {
	if limit <= 0 {
		limit = 15
	}
	seenLine := map[string]bool{}
	seenBody := map[string]bool{}
	var tips []string
	for _, f := range findings {
		tip := strings.TrimSpace(f.Remediation)
		if tip == "" {
			continue
		}
		label := strings.TrimSpace(f.Name)
		line := tip
		if label != "" {
			line = label + ": " + tip
		}
		lineKey := strings.ToLower(line)
		bodyKey := strings.ToLower(tip)
		if seenLine[lineKey] || seenBody[bodyKey] {
			continue
		}
		seenLine[lineKey] = true
		seenBody[bodyKey] = true
		tips = append(tips, line)
		if len(tips) >= limit {
			break
		}
	}
	return tips
}

func sevRank(s string) int {
	switch strings.ToLower(s) {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	default:
		return 1
	}
}

func (s *Server) beginWebScan(targetID, operator, trigger string) *WebScanResult {
	t, ok := s.cfg.GetWebTarget(targetID)
	if !ok {
		return &WebScanResult{
			ID: "ws-err", Label: "目标不存在", TargetID: targetID, Status: "failed",
			Error: "未找到扫描目标", Findings: []WebFinding{}, Summary: map[string]int{},
		}
	}
	// Atomic check+insert under one lock to avoid duplicate in-flight scans.
	s.webSec.mu.Lock()
	for _, sc := range s.webSec.scans {
		if sc != nil && sc.TargetID == targetID && sc.Status == "running" {
			s.webSec.mu.Unlock()
			return &WebScanResult{
				ID: "ws-busy", Label: t.Name + " · 进行中", TargetID: targetID, Status: "failed",
				Error: "该目标已有扫描进行中，请稍后再试", Findings: []WebFinding{}, Summary: map[string]int{},
			}
		}
	}
	running := 0
	for _, sc := range s.webSec.scans {
		if sc != nil && sc.Status == "running" {
			running++
		}
	}
	if running >= 12 {
		s.webSec.mu.Unlock()
		return &WebScanResult{
			ID: "ws-queue", Label: "队列已满", TargetID: targetID, Status: "failed",
			Error: "扫描队列已满（最多 12 个进行中），请稍后再试", Findings: []WebFinding{}, Summary: map[string]int{},
		}
	}
	s.webSec.seq++
	seq := s.webSec.seq
	now := time.Now()
	name := strings.TrimSpace(t.Name)
	if name == "" {
		name = "未命名目标"
	}
	id := fmt.Sprintf("ws-%03d-%s-%s", seq, now.Format("0102-1504"), randomHex(2))
	label := fmt.Sprintf("%s · #%03d · %s", name, seq, now.Format("01-02 15:04"))
	scan := &WebScanResult{
		ID:         id,
		Label:      label,
		Seq:        seq,
		TargetID:   t.ID,
		TargetName: t.Name,
		BaseURL:    t.BaseURL,
		StartedAt:  now.Unix(),
		Status:     "running",
		Operator:   operator,
		Trigger:    trigger,
		Findings:   []WebFinding{},
		Summary:    map[string]int{},
	}
	s.webSec.scans = append([]*WebScanResult{scan}, s.webSec.scans...)
	s.webSec.trimLocked()
	s.webSec.saveLocked()
	s.webSec.mu.Unlock()
	return scan
}

func (s *Server) completeWebScan(scanID string) {
	scan := s.webSec.get(scanID)
	if scan == nil || scan.Status != "running" {
		return
	}
	cfg := s.cfg.WebSecurity()
	t, ok := s.cfg.GetWebTarget(scan.TargetID)
	if !ok {
		_ = s.webSec.finishIfRunning(scanID, func(live *WebScanResult) {
			live.Status = "failed"
			live.Error = "target not found"
			live.FinishedAt = time.Now().Unix()
		})
		return
	}

	s.webSec.acquireScanSlot()
	defer s.webSec.releaseScanSlot()

	// Re-check after waiting for a slot — cancel/reap may have won.
	if cur := s.webSec.get(scanID); cur == nil || cur.Status != "running" {
		return
	}

	if err := assertURLAllowed(t.BaseURL, cfg.AllowPrivate); err != nil {
		_ = s.webSec.finishIfRunning(scanID, func(live *WebScanResult) {
			live.Status = "failed"
			live.Error = zhWebSecErr(err.Error())
			live.FinishedAt = time.Now().Unix()
		})
		return
	}
	for _, u := range t.ScanURLs {
		if err := assertURLAllowed(u, cfg.AllowPrivate); err != nil {
			_ = s.webSec.finishIfRunning(scanID, func(live *WebScanResult) {
				live.Status = "failed"
				live.Error = zhWebSecErr("scan_urls: " + err.Error())
				live.FinishedAt = time.Now().Unix()
			})
			return
		}
	}

	findings, engines, engineNote, err := s.runWebEngines(cfg, t)
	finished := time.Now().Unix()

	var prevFindings []WebFinding
	var prevID string
	s.webSec.mu.Lock()
	for _, sc := range s.webSec.scans { // newest-first
		if sc == nil || sc.TargetID != t.ID || sc.Status != "completed" || sc.ID == scanID {
			continue
		}
		prevFindings = sc.Findings
		prevID = sc.ID
		break
	}
	s.webSec.mu.Unlock()
	baseDiff := diffWebFindings(prevFindings, findings, prevID)

	applied := s.webSec.finishIfRunning(scanID, func(live *WebScanResult) {
		live.FinishedAt = finished
		live.Summary = map[string]int{}
		live.Engines = engines
		live.EngineNote = engineNote
		if len(findings) > 0 {
			live.Findings = findings
			for _, f := range findings {
				live.Summary[f.Severity]++
			}
			live.Report = buildWebScanReport(t, findings)
			live.OWASP = summarizeWebOWASP(findings)
			live.Compliance = summarizeWebCompliance(findings)
		}
		live.BaselineDiff = baseDiff
		if err != nil {
			live.Status = "failed"
			live.Error = zhWebSecErr(err.Error())
			return
		}
		live.Status = "completed"
		live.Error = ""
	})
	if applied && err == nil {
		s.cfg.touchWebTargetScan(t.ID, finished)
		if done := s.webSec.get(scanID); done != nil {
			s.notifyWebSecurityScanCompleted(done)
			s.maybeWebSecurityAISummary(done)
		}
	}
}

// runWebScan synchronous path for scheduler.
func (s *Server) runWebScan(targetID, operator, trigger string) *WebScanResult {
	scan := s.beginWebScan(targetID, operator, trigger)
	if scan.Status == "failed" {
		return scan
	}
	s.completeWebScan(scan.ID)
	if done := s.webSec.get(scan.ID); done != nil {
		return done
	}
	return scan
}

// webTagTemplateDirs maps UI whitelist tags → one or more nuclei-templates subdirs.
// Prefer directory scoping over -tags alone (avoids "no templates provided").
var webTagTemplateDirs = map[string][]string{
	"cves":            {"http/cves"},
	"misconfig":       {"http/misconfiguration", "ssl", "http/miscellaneous"},
	"exposures":       {"http/exposures", "http/exposed-panels"},
	"default-logins":  {"http/default-logins"},
	"vulnerabilities": {"http/vulnerabilities"},
	"technologies":    {"http/technologies"},
	"panel":           {"http/exposed-panels"},
	// Specialty tags share http/vulnerabilities; when used alone we still scan that
	// dir, but buildNucleiTemplateArgs adds -tags to narrow the set.
	"xss":     {"http/vulnerabilities"},
	"sqli":    {"http/vulnerabilities"},
	"lfi":     {"http/vulnerabilities"},
	"rce":     {"http/vulnerabilities"},
	"iot":     {"http/iot"},
	"network": {"network"},
	"dns":     {"dns"},
	"osint":   {"http/osint"},
	"misc":    {"http/miscellaneous"},
}

// webSpecialtyTags are Nuclei tags that should filter http/vulnerabilities when
// the broad "vulnerabilities" pack is NOT also selected (avoids scanning the
// whole vuln tree three times under deep profile — dirs are deduped, but without
// -tags the specialty picks were uselessly identical to the full pack).
var webSpecialtyTags = map[string]bool{
	"xss": true, "sqli": true, "lfi": true, "rce": true,
}

// webTemplatePackMeta is the commercial UI catalog of template packs.
var webTemplatePackMeta = []struct {
	ID, Name, Path string
}{
	{"misconfig", "错误配置", "http/misconfiguration"},
	{"ssl", "SSL/TLS", "ssl"},
	{"exposures", "信息暴露", "http/exposures"},
	{"panel", "管理面板", "http/exposed-panels"},
	{"cves", "CVE 漏洞", "http/cves"},
	{"logins", "默认口令", "http/default-logins"},
	{"vuln", "通用漏洞", "http/vulnerabilities"},
	{"tech", "技术指纹", "http/technologies"},
}

type WebTemplatePack struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Count int    `json:"count"`
}

type WebEngineStatus struct {
	Ready           bool              `json:"ready"`
	NucleiPath      string            `json:"nuclei_path"`
	NucleiVersion   string            `json:"nuclei_version,omitempty"`
	TemplatesDir    string            `json:"templates_dir"`
	TemplateCount   int               `json:"template_count"`
	UpdatedAt       int64             `json:"updated_at,omitempty"`
	Packs           []WebTemplatePack `json:"packs"`
	Severity        string            `json:"severity"`
	AllowPrivate    bool              `json:"allow_private"`
	RateLimit       int               `json:"rate_limit"`
	TimeoutSec      int               `json:"timeout_sec"`
	Concurrency     int               `json:"concurrency"`
	ScanConcurrency int               `json:"scan_concurrency"`
	Message         string            `json:"message,omitempty"`
}

var (
	webEngineCacheMu sync.Mutex
	webEngineCacheAt time.Time
	webEngineCached  WebEngineStatus
)

func countYAMLTemplates(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			n++
		}
		return nil
	})
	return n
}

func nucleiVersionString(bin string) string {
	if bin == "" {
		bin = "nuclei"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-version").CombinedOutput()
	if err != nil {
		return ""
	}
	text := stripANSI(string(out))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		if strings.Contains(low, "nuclei engine version") || strings.Contains(low, "version:") {
			line = strings.TrimPrefix(line, "[INF]")
			line = strings.TrimSpace(line)
			return truncateRun(line, 80)
		}
	}
	return ""
}

func (s *Server) collectWebEngineStatus(force bool) WebEngineStatus {
	webEngineCacheMu.Lock()
	defer webEngineCacheMu.Unlock()
	if !force && time.Since(webEngineCacheAt) < 45*time.Second && webEngineCached.TemplatesDir != "" {
		return webEngineCached
	}
	cfg := s.cfg.WebSecurity()
	dir := s.resolveNucleiTemplatesDir(cfg)
	st := WebEngineStatus{
		NucleiPath:      cfg.NucleiPath,
		TemplatesDir:    dir,
		Severity:        cfg.Severity,
		AllowPrivate:    cfg.AllowPrivate,
		RateLimit:       cfg.RateLimit,
		TimeoutSec:      cfg.TimeoutSec,
		Concurrency:     cfg.Concurrency,
		ScanConcurrency: cfg.ScanConcurrency,
		Packs:           make([]WebTemplatePack, 0, len(webTemplatePackMeta)),
	}
	st.NucleiVersion = nucleiVersionString(cfg.NucleiPath)
	st.Ready = nucleiTemplatesReady(dir)
	if st.Ready {
		st.TemplateCount = countYAMLTemplates(dir)
		if info, err := os.Stat(dir); err == nil {
			st.UpdatedAt = info.ModTime().Unix()
		}
		st.Message = "模板库已就绪，可发起扫描"
	} else {
		st.Message = "模板库未就绪：首次扫描或点击「更新模板」将自动下载"
	}
	for _, p := range webTemplatePackMeta {
		pack := WebTemplatePack{ID: p.ID, Name: p.Name, Path: p.Path}
		full := filepath.Join(dir, filepath.FromSlash(p.Path))
		if st2, err := os.Stat(full); err == nil && st2.IsDir() {
			pack.Count = countYAMLTemplates(full)
		}
		st.Packs = append(st.Packs, pack)
	}
	webEngineCached = st
	webEngineCacheAt = time.Now()
	return st
}

func (s *Server) resolveNucleiTemplatesDir(cfg WebSecurityConfig) string {
	if d := strings.TrimSpace(cfg.TemplatesDir); d != "" {
		return d
	}
	// A tree installed by the feed updater wins: it is the one the operator can
	// see, refresh and version from the UI. The legacy path stays as fallback so
	// existing installs keep scanning until their first feed update.
	if d := s.nucleiFeedTemplatesDir(); d != "" {
		return d
	}
	return filepath.Join(s.cfg.securityDataDir(), "nuclei-templates")
}

// ensureNucleiTemplatesVia prepares templates using the feed pipeline (proxy +
// mirror aware, pure Go) and falls back to the nuclei CLI only if that fails.
// Returns the directory that ended up usable.
func (s *Server) ensureNucleiTemplatesVia(bin string, timeout time.Duration) (string, error) {
	cfg := s.cfg.WebSecurity()
	dir := s.resolveNucleiTemplatesDir(cfg)
	if nucleiTemplatesReady(dir) {
		return dir, nil
	}
	if s.feeds != nil && strings.TrimSpace(cfg.TemplatesDir) == "" {
		src, ok := feedSourceByID("nuclei-templates")
		if ok {
			feedCfg := s.cfg.SecurityFeeds()
			if feedCfg.TimeoutSec <= 0 {
				feedCfg.TimeoutSec = int(timeout / time.Second)
			}
			client, err := feedHTTPClient(feedCfg)
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				st := s.feeds.updateSource(ctx, client, feedCfg, src)
				cancel()
				s.feeds.mu.Lock()
				s.feeds.states[src.ID] = st
				s.feeds.saveStatesLocked()
				s.feeds.mu.Unlock()
				if st.Error == "" {
					if d := s.nucleiFeedTemplatesDir(); d != "" {
						return d, nil
					}
				}
				slog.Warn("feed template install failed, falling back to nuclei CLI", "err", st.Error)
			}
		}
	}
	if err := ensureNucleiTemplates(s.cfg.SecurityFeeds(), bin, dir, timeout); err != nil {
		return dir, err
	}
	return dir, nil
}

func nucleiTemplatesReady(dir string) bool {
	if dir == "" {
		return false
	}
	// Prefer http/ tree; also accept any yaml under the root.
	httpDir := filepath.Join(dir, "http")
	if st, err := os.Stat(httpDir); err == nil && st.IsDir() {
		n := 0
		_ = filepath.WalkDir(httpDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(d.Name()), ".yaml") || strings.HasSuffix(strings.ToLower(d.Name()), ".yml") {
				n++
				if n >= 20 {
					return io.EOF
				}
			}
			return nil
		})
		return n >= 20
	}
	return false
}

func ensureNucleiTemplates(feedCfg SecurityFeedConfig, bin, dir string, timeout time.Duration) error {
	if bin == "" {
		bin = "nuclei"
	}
	if nucleiTemplatesReady(dir) {
		return nil
	}
	if timeout <= 0 {
		timeout = 12 * time.Minute
	}
	home, _ := os.UserHomeDir()
	homeTpl := filepath.Join(home, "nuclei-templates")
	// Nuclei v3.3.x often ignores -update-template-dir; install to $HOME then publish
	// into the persisted data dir used by scans.
	if !nucleiTemplatesReady(homeTpl) {
		if err := downloadNucleiTemplatesHome(feedCfg, bin, homeTpl, timeout); err != nil {
			return err
		}
	}
	if !nucleiTemplatesReady(homeTpl) {
		return fmt.Errorf("Nuclei 模板下载后仍不可用（%s）。请检查外网访问 GitHub，或手动把 nuclei-templates 放到该路径", homeTpl)
	}
	if filepath.Clean(dir) == filepath.Clean(homeTpl) {
		return nil
	}
	if err := publishNucleiTemplates(homeTpl, dir); err != nil {
		return err
	}
	if !nucleiTemplatesReady(dir) {
		return fmt.Errorf("无法准备持久化模板目录：%s", dir)
	}
	slog.Info("nuclei templates ready", "dir", dir, "source", homeTpl)
	return nil
}

func nucleiTemplatesConfigPath(home string) string {
	return filepath.Join(home, ".config", "nuclei", ".templates-config.json")
}

func downloadNucleiTemplatesHome(feedCfg SecurityFeedConfig, bin, homeTpl string, timeout time.Duration) error {
	home, _ := os.UserHomeDir()
	if homeTpl == "" {
		homeTpl = filepath.Join(home, "nuclei-templates")
	}
	// Stale config claiming "installed" while the directory is empty makes
	// `nuclei -update-templates` exit 0 without downloading anything.
	_ = os.Remove(nucleiTemplatesConfigPath(home))
	_ = os.RemoveAll(homeTpl)

	runUpdate := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "-update-templates", "-disable-update-check")
		out, err := cmd.CombinedOutput()
		msg := strings.TrimSpace(stripANSI(string(out)))
		if err != nil {
			return fmt.Errorf("%s", truncateRun(msg+" "+err.Error(), 280))
		}
		return nil
	}
	if err := runUpdate(); err != nil {
		slog.Warn("nuclei -update-templates failed, trying GitHub archive", "err", err)
	}
	if nucleiTemplatesReady(homeTpl) {
		return nil
	}
	// Retry once after clearing config again.
	_ = os.Remove(nucleiTemplatesConfigPath(home))
	_ = os.RemoveAll(homeTpl)
	_ = runUpdate()
	if nucleiTemplatesReady(homeTpl) {
		return nil
	}
	if err := installNucleiTemplatesFromGitHub(feedCfg, homeTpl, timeout); err != nil {
		return fmt.Errorf("下载 Nuclei 模板失败：%s", err.Error())
	}
	return nil
}

// installNucleiTemplatesFromGitHub fetches the newest nuclei-templates release
// straight over HTTP. Used when `nuclei -update-templates` exits successfully
// but leaves an empty tree.
//
// This is pure Go on purpose: the previous curl+tar version failed on any host
// without those binaries (every Windows install) and ignored the configured
// proxy, which is exactly the combination that produced "update timed out" with
// an empty template library.
func installNucleiTemplatesFromGitHub(feedCfg SecurityFeedConfig, dir string, timeout time.Duration) error {
	src, ok := feedSourceByID("nuclei-templates")
	if !ok {
		return fmt.Errorf("模板源未在目录中定义")
	}
	feedCfg = feedCfg.withDefaults()
	feedCfg.TimeoutSec = int(timeout / time.Second)
	client, err := feedHTTPClient(feedCfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	resolved := resolveLatestRef(ctx, client, feedCfg, src.Repo, src.RefFallback)
	staging := dir + ".staging"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	tag := ""
	var lastErr error
	for _, ref := range src.refCandidates(resolved) {
		tag = ref
		_, _, lastErr = fetchFeedArchive(ctx, client, feedCfg, src, ref, staging, nopFeedProgress{})
		if lastErr == nil {
			break
		}
		if !errors.Is(lastErr, errFeedRefMissing) {
			break
		}
		_ = os.RemoveAll(staging)
		if err := os.MkdirAll(staging, 0o750); err != nil {
			return err
		}
	}
	if lastErr != nil {
		if errors.Is(lastErr, errFeedRefMissing) {
			return fmt.Errorf("未找到可用的模板包版本（已尝试 %s）", strings.Join(src.refCandidates(resolved), "/"))
		}
		return fmt.Errorf("下载 Nuclei 模板失败：%s", lastErr.Error())
	}
	if !nucleiTemplatesReady(staging) {
		return fmt.Errorf("模板包校验失败：解压结果中没有可用的 http/ 模板")
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return err
	}
	old := dir + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dir); err == nil {
		_ = os.Rename(dir, old)
	}
	if err := os.Rename(staging, dir); err != nil {
		_ = os.Rename(old, dir)
		return fmt.Errorf("发布模板目录失败：%w", err)
	}
	_ = os.RemoveAll(old)
	slog.Info("nuclei templates installed from GitHub", "tag", tag, "dir", dir)
	return nil
}

// nucleiBinaryPresent reports whether the configured engine is actually
// runnable — either resolvable on PATH or an existing executable file.
func nucleiBinaryPresent(bin string) bool {
	if bin == "" {
		bin = "nuclei"
	}
	if _, err := exec.LookPath(bin); err == nil {
		return true
	}
	st, err := os.Stat(bin)
	return err == nil && !st.IsDir()
}

func publishNucleiTemplates(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("创建模板目录失败：%w", err)
	}
	staging := dst + ".staging"
	_ = os.RemoveAll(staging)
	// `cp -a` is the fast path on Unix; Windows has no cp, and symlinks there
	// need privileges the service usually lacks, so fall back to a Go copy.
	copyErr := ""
	if runtime.GOOS != "windows" {
		if out, err := exec.Command("cp", "-a", src, staging).CombinedOutput(); err != nil {
			_ = os.RemoveAll(staging)
			copyErr = string(out) + " " + err.Error()
		}
	}
	if _, err := os.Stat(staging); err != nil {
		if err := copyTreeInto(src, staging); err != nil {
			_ = os.RemoveAll(staging)
			if err2 := os.Symlink(src, dst); err2 != nil {
				return fmt.Errorf("复制模板失败：%s", truncateRun(copyErr+" "+err.Error()+" "+err2.Error(), 240))
			}
			slog.Info("nuclei templates linked", "from", src, "to", dst)
			return nil
		}
	}
	_ = os.RemoveAll(dst)
	if err := os.Rename(staging, dst); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("切换模板目录失败：%w", err)
	}
	return nil
}

// copyTreeInto copies a directory tree with plain Go I/O. Symlinks are skipped
// rather than followed, so a hostile template archive cannot write outside dst.
func copyTreeInto(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o750)
		case !d.Type().IsRegular():
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// constrainPathUnderRoot resolves p under root and rejects path escape / abs paths outside.
// On Windows, filepath.IsAbs("/etc/passwd") is false, so a leading "/" must still be
// treated as absolute — otherwise Join(root, "/etc/passwd") lands under root and
// falsely allows a path traversal.
func constrainPathUnderRoot(p, root string) (string, bool) {
	p = strings.TrimSpace(p)
	root = strings.TrimSpace(root)
	if p == "" || root == "" || strings.Contains(p, "..") {
		return "", false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	slashP := filepath.ToSlash(p)
	absLike := filepath.IsAbs(p) || strings.HasPrefix(slashP, "/") ||
		(len(p) >= 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')))
	var full string
	if absLike {
		full = filepath.Clean(filepath.FromSlash(slashP))
	} else {
		full = filepath.Join(rootAbs, filepath.FromSlash(p))
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", false
	}
	sep := string(os.PathSeparator)
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+sep) {
		return "", false
	}
	return fullAbs, true
}

func buildNucleiTemplateArgs(tplRoot string, t WebScanTarget) []string {
	var args []string
	seen := map[string]bool{}
	addDir := func(sub string) {
		p := filepath.Join(tplRoot, filepath.FromSlash(sub))
		if seen[p] {
			return
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			args = append(args, "-t", p)
			seen[p] = true
		}
	}
	for _, tpl := range t.Templates {
		if full, ok := constrainPathUnderRoot(tpl, tplRoot); ok {
			args = append(args, "-t", full)
		}
	}
	if len(t.Templates) > 0 {
		return args
	}
	if len(t.Tags) > 0 {
		hasVulnPack := false
		var specialty []string
		for _, tag := range t.Tags {
			if tag == "vulnerabilities" {
				hasVulnPack = true
			}
			if webSpecialtyTags[tag] {
				specialty = append(specialty, tag)
			}
			for _, sub := range webTagTemplateDirs[tag] {
				addDir(sub)
			}
		}
		// When only xss/sqli/rce (no full vulnerabilities pack), narrow with -tags
		// so Nuclei does not execute every template under http/vulnerabilities.
		if !hasVulnPack && len(specialty) > 0 {
			args = append(args, "-tags", strings.Join(specialty, ","))
		}
		if len(args) > 0 {
			return args
		}
		return []string{"-t", tplRoot, "-tags", strings.Join(t.Tags, ",")}
	}
	for _, sub := range []string{
		"http/misconfiguration", "ssl", "http/exposures", "http/vulnerabilities",
		"http/cves", "http/default-logins", "http/exposed-panels",
	} {
		addDir(sub)
	}
	if len(args) == 0 {
		args = append(args, "-t", tplRoot)
	}
	return args
}

// runWebEngines runs the built-in checks and Nuclei, merging both result sets.
// A Nuclei failure degrades the scan instead of voiding it: the built-in engine
// still covers TLS / headers / cookies / CORS / exposure, and returning those
// beats reporting nothing because a template download failed.
func (s *Server) runWebEngines(cfg WebSecurityConfig, t WebScanTarget) (findings []WebFinding, engines []string, note string, err error) {
	if cfg.builtinEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		authHeaders, authErr := resolveWebAuthHeaders(t, cfg.AllowPrivate)
		if authErr != nil {
			note = "内置检测未鉴权运行：" + authErr.Error()
		}
		findings = append(findings, runBuiltinWebChecks(ctx, t, cfg.AllowPrivate, authHeaders)...)
		cancel()
		engines = append(engines, "builtin")
	}

	nucFindings, nucErr := s.execNuclei(cfg, t)
	if nucErr == nil {
		findings = append(findings, nucFindings...)
		engines = append(engines, "nuclei")
	} else {
		findings = append(findings, nucFindings...)
		if !cfg.builtinEnabled() {
			return nil, engines, note, nucErr
		}
		findings = append(findings, WebFinding{
			TemplateID:  "builtin/engine-degraded",
			Name:        "Nuclei 引擎不可用，本次仅执行内置检测",
			Severity:    "info",
			URL:         t.BaseURL,
			Type:        "http",
			Tags:        []string{"builtin", "engine"},
			Description: zhWebSecErr(nucErr.Error()),
			Remediation: "修复 Nuclei 引擎/模板后重新扫描，以覆盖 CVE、默认口令与注入类模板检测",
		})
		if note != "" {
			note += "；"
		}
		note += "Nuclei 未执行：" + zhWebSecErr(nucErr.Error())
	}
	findings = annotateWebFindings(dedupeWebFindings(findings))
	return findings, engines, note, nil
}

func (s *Server) execNuclei(cfg WebSecurityConfig, t WebScanTarget) ([]WebFinding, error) {
	bin := cfg.NucleiPath
	if bin == "" {
		bin = "nuclei"
	}
	if !nucleiBinaryPresent(bin) {
		return nil, fmt.Errorf("未找到 Nuclei 引擎（%s）。请确认镜像已内置 nuclei，或在引擎配置中设置正确路径", bin)
	}
	tplDir, err := s.ensureNucleiTemplatesVia(bin, 12*time.Minute)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSec)*time.Second)
	defer cancel()

	authHeaders, authErr := resolveWebAuthHeaders(t, cfg.AllowPrivate)
	if authErr != nil {
		return nil, fmt.Errorf("鉴权准备失败：%w", authErr)
	}
	args := buildNucleiRunArgs(cfg, t, tplDir, authHeaders)

	findings, errMsg, waitErr := runNucleiOnce(ctx, bin, args)
	if ctx.Err() == context.DeadlineExceeded {
		return findings, fmt.Errorf("Nuclei 扫描超时（%d 秒）。可在引擎配置增大超时，或减少标签/模板范围", cfg.TimeoutSec)
	}
	needTpl := waitErr != nil && len(findings) == 0 &&
		(strings.Contains(strings.ToLower(errMsg), "no templates") ||
			strings.Contains(errMsg, "Could not run nuclei"))
	if needTpl {
		slog.Info("nuclei reported no templates, forcing reinstall then retry", "dir", tplDir)
		home, _ := os.UserHomeDir()
		_ = os.Remove(nucleiTemplatesConfigPath(home))
		_ = os.RemoveAll(filepath.Join(home, "nuclei-templates"))
		// Hide persisted tree so ensure() reinstalls; restore on failure.
		if nucleiTemplatesReady(tplDir) {
			_ = os.Rename(tplDir, tplDir+".bak")
		}
		if upErr := ensureNucleiTemplates(s.cfg.SecurityFeeds(), bin, tplDir, 12*time.Minute); upErr != nil {
			if !nucleiTemplatesReady(tplDir) {
				_ = os.Rename(tplDir+".bak", tplDir)
			} else {
				_ = os.RemoveAll(tplDir + ".bak")
			}
			return nil, upErr
		}
		_ = os.RemoveAll(tplDir + ".bak")
		ctx2, cancel2 := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSec)*time.Second)
		defer cancel2()
		args = buildNucleiRunArgs(cfg, t, tplDir, authHeaders)
		findings, errMsg, waitErr = runNucleiOnce(ctx2, bin, args)
	}
	if waitErr != nil && len(findings) == 0 {
		return nil, fmt.Errorf("%s", humanizeNucleiErr(errMsg, waitErr))
	}
	return findings, nil
}

// buildNucleiRunArgs assembles Nuclei CLI flags for a single-target scan.
func buildNucleiRunArgs(cfg WebSecurityConfig, t WebScanTarget, tplDir string, authHeaders []string) []string {
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 25
	}
	rateLimit := cfg.RateLimit
	if rateLimit <= 0 {
		rateLimit = 120
	}
	bulk := concurrency
	if bulk < 25 {
		bulk = 25
	}
	if bulk > 50 {
		bulk = 50
	}
	severity := cfg.Severity
	if severity == "" {
		severity = "critical,high,medium,low,info"
	}
	args := []string{
		"-jsonl",
		"-silent",
		"-severity", severity,
		"-rate-limit", fmt.Sprintf("%d", rateLimit),
		"-c", fmt.Sprintf("%d", concurrency),
		"-bulk-size", fmt.Sprintf("%d", bulk),
		"-timeout", "10",
		"-retries", "1",
		"-max-host-error", "40",
		"-disable-update-check",
		"-nh",       // skip httpx probing — target URL is already known
		"-omit-raw", // shrink JSONL IO on the hot path
	}
	seenU := map[string]bool{}
	addU := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		k := strings.ToLower(u)
		if seenU[k] {
			return
		}
		seenU[k] = true
		args = append(args, "-u", u)
	}
	addU(t.BaseURL)
	for _, u := range t.ScanURLs {
		addU(u)
		if len(seenU) >= 80 {
			break
		}
	}
	if len(seenU) == 0 {
		args = append(args, "-u", t.BaseURL)
	}
	args = append(args, buildNucleiTemplateArgs(tplDir, t)...)
	for _, p := range t.Include {
		if full, ok := constrainPathUnderRoot(p, tplDir); ok {
			args = append(args, "-include-path", full)
		}
	}
	for _, p := range t.Exclude {
		if full, ok := constrainPathUnderRoot(p, tplDir); ok {
			args = append(args, "-exclude-path", full)
		}
	}
	for _, h := range authHeaders {
		args = append(args, "-H", h)
	}
	return args
}

func runNucleiOnce(ctx context.Context, bin string, args []string) ([]WebFinding, string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err.Error(), err
	}
	findings := parseNucleiJSONL(stdout)
	waitErr := cmd.Wait()
	return findings, stderr.String(), waitErr
}

func humanizeNucleiErr(stderr string, waitErr error) string {
	msg := strings.TrimSpace(stripANSI(stderr))
	if msg == "" && waitErr != nil {
		msg = waitErr.Error()
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "no templates"):
		return "未匹配到可用扫描模板。请检查目标标签是否过窄，或在引擎配置中重新下载模板"
	case strings.Contains(low, "could not run nuclei"):
		return "Nuclei 无法启动扫描：" + truncateRun(msg, 200)
	case strings.Contains(low, "no such file"):
		return "Nuclei 模板路径无效，请检查引擎配置中的模板目录"
	default:
		return "Nuclei 扫描失败：" + truncateRun(msg, 240)
	}
}

func zhWebSecErr(s string) string {
	s = strings.TrimSpace(stripANSI(s))
	if s == "" {
		return "扫描失败"
	}
	low := strings.ToLower(s)
	switch {
	case strings.Contains(s, "模板"), strings.Contains(s, "Nuclei"), strings.Contains(s, "私网"),
		strings.Contains(s, "未找到"), strings.Contains(s, "超时"), strings.Contains(s, "禁止"):
		return s
	case strings.Contains(low, "no templates"), strings.Contains(low, "could not run nuclei"):
		return humanizeNucleiErr(s, nil)
	case strings.Contains(low, "private"), strings.Contains(low, "allow_private"):
		return "目标地址为私网/保留地址，需管理员在引擎配置中开启「允许私网」"
	case strings.Contains(low, "target not found"):
		return "未找到扫描目标"
	default:
		return s
	}
}

func basicAuthValue(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func (s *Server) startWebSecurityScheduler() {
	// superviseLoop 而不是裸 goroutine：这条循环处理的是**外部来的扫描结果**，
	// 一次空指针/越界不该把整个服务端带走；更要紧的是——即便有 recover，
	// 循环退出也意味着**Web 扫描从此再也不会被调度**，而且没有任何人会发现。
	// superviseLoop 记堆栈、上报平台自身故障，然后把循环重新拉起来。
	go superviseLoop("web-security-scheduler", func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			cfg := s.cfg.WebSecurity().withDefaults()
			if n := s.webSec.reapStuck(cfg.TimeoutSec); n > 0 {
				slog.Info("web security watchdog reaped stuck scans", "count", n)
			}
			s.tickWebSecuritySchedule()
		}
	})
	go s.maybeUpdateNucleiTemplates()
}

func (s *Server) handleWebScanCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.webSec.cancelScan(id) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "扫描不存在或不在运行中"})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: "取消 Web 安全扫描 " + id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) maybeUpdateNucleiTemplates() {
	cfg := s.cfg.WebSecurity()
	bin := cfg.NucleiPath
	if bin == "" {
		bin = "nuclei"
	}
	// Without the engine the templates are dead weight: skip the boot-time
	// download so deployments (and tests) that never install nuclei don't pay
	// for it. execNuclei still prepares templates lazily on the first scan.
	if !nucleiBinaryPresent(bin) {
		return
	}
	tplDir := s.resolveNucleiTemplatesDir(cfg)
	if !nucleiTemplatesReady(tplDir) {
		got, err := s.ensureNucleiTemplatesVia(bin, 12*time.Minute)
		if err != nil {
			slog.Warn("nuclei templates prepare failed", "dir", tplDir, "err", err)
			return
		}
		slog.Info("nuclei templates ready", "dir", got)
		return
	}
	// Already have a usable tree. Optional startup refresh is incremental only —
	// never wipe the persisted directory on boot (avoids empty-tree outages).
	if cfg.UpdateTemplates {
		home, _ := os.UserHomeDir()
		homeTpl := filepath.Join(home, "nuclei-templates")
		if !nucleiTemplatesReady(homeTpl) {
			_ = publishNucleiTemplates(tplDir, homeTpl)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		cmd := exec.CommandContext(ctx, bin, "-update-templates", "-disable-update-check")
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			slog.Warn("nuclei incremental template update failed", "err", truncateRun(stripANSI(string(out))+" "+err.Error(), 200))
		} else if nucleiTemplatesReady(homeTpl) {
			if pubErr := publishNucleiTemplates(homeTpl, tplDir); pubErr != nil {
				slog.Warn("publish updated templates failed", "err", pubErr)
			} else {
				slog.Info("nuclei templates refreshed", "dir", tplDir)
				return
			}
		}
	}
	slog.Info("nuclei templates ready", "dir", tplDir)
}

func (s *Server) tickWebSecuritySchedule() {
	cfg := s.cfg.WebSecurity()
	now := time.Now()
	for _, t := range cfg.Targets {
		if !t.Enabled || t.Schedule == nil || !t.Schedule.Enabled {
			continue
		}
		if s.webSec.hasRunning(t.ID) {
			continue
		}
		if !webTargetScheduleDue(t, s.webSec, now) {
			continue
		}
		tid := t.ID
		go s.runWebScan(tid, "scheduler", "schedule")
	}
}

func webTargetScheduleDue(t WebScanTarget, m *webScanManager, now time.Time) bool {
	sc := t.Schedule
	if sc == nil || !sc.Enabled {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := t.ID
	last := m.lastRun[key]
	// Seed from persisted LastScanAt so restart does not immediately re-fire intervals.
	if last == 0 && t.LastScanAt > 0 {
		last = t.LastScanAt
		m.lastRun[key] = last
	}
	switch sc.Kind {
	case "interval":
		min := sc.IntervalMin
		if min < 15 {
			min = 15
		}
		if last > 0 && now.Unix()-last < int64(min)*60 {
			return false
		}
		m.lastRun[key] = now.Unix()
		return true
	case "daily":
		mins, ok := parseHHMM(sc.At)
		if !ok || now.Hour()*60+now.Minute() != mins {
			return false
		}
		day := now.Format("2006-01-02")
		dayKey := key + ":" + day
		if m.lastRun[dayKey] > 0 {
			return false
		}
		// If last completed scan was within this minute window today, skip.
		if t.LastScanAt > 0 {
			lastT := time.Unix(t.LastScanAt, 0).In(now.Location())
			if lastT.Format("2006-01-02") == day && lastT.Hour()*60+lastT.Minute() == mins {
				m.lastRun[dayKey] = t.LastScanAt
				return false
			}
		}
		m.lastRun[dayKey] = now.Unix()
		return true
	case "weekly":
		mins, ok := parseHHMM(sc.At)
		if !ok || int(now.Weekday()) != sc.Weekday || now.Hour()*60+now.Minute() != mins {
			return false
		}
		wk := now.Format("2006-W02")
		wkKey := key + ":" + wk
		if m.lastRun[wkKey] > 0 {
			return false
		}
		if t.LastScanAt > 0 {
			lastT := time.Unix(t.LastScanAt, 0).In(now.Location())
			if lastT.Format("2006-W02") == wk && int(lastT.Weekday()) == sc.Weekday &&
				lastT.Hour()*60+lastT.Minute() == mins {
				m.lastRun[wkKey] = t.LastScanAt
				return false
			}
		}
		m.lastRun[wkKey] = now.Unix()
		return true
	}
	return false
}

func isMaskedSecret(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.Contains(s, "****")
}

func validateWebTargetAuth(t WebScanTarget) error {
	switch t.AuthType {
	case "basic":
		if t.AuthUser == "" || t.AuthPass == "" {
			return fmt.Errorf("Basic 鉴权需填写用户名和密码")
		}
	case "bearer":
		if strings.TrimSpace(t.AuthPass) == "" && !strings.Contains(strings.ToLower(t.AuthHeader), "authorization:") {
			return fmt.Errorf("Bearer 鉴权需填写 Token，或在请求头中提供 Authorization")
		}
	case "cookie", "header":
		if strings.TrimSpace(t.AuthHeader) == "" {
			return fmt.Errorf("请填写鉴权请求头（可多行 Name: Value）")
		}
	case "form":
		if t.AuthUser == "" || t.AuthPass == "" {
			return fmt.Errorf("表单登录需填写用户名和密码")
		}
		if strings.TrimSpace(t.AuthLoginURL) == "" {
			return fmt.Errorf("表单登录需填写登录 URL")
		}
	case "header_body":
		if strings.TrimSpace(t.AuthLoginURL) == "" {
			return fmt.Errorf("预认证需填写请求 URL")
		}
		if strings.TrimSpace(t.AuthHeader) == "" && strings.TrimSpace(t.AuthBody) == "" {
			return fmt.Errorf("预认证需至少提供请求头或请求体")
		}
	}
	return nil
}

func parseHeaderLines(raw string) []string {
	var out []string
	for _, ln := range strings.Split(raw, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || !strings.Contains(ln, ":") {
			continue
		}
		out = append(out, ln)
	}
	return out
}

func resolveAuthLoginURL(t WebScanTarget) (string, error) {
	raw := strings.TrimSpace(t.AuthLoginURL)
	if raw == "" {
		return "", fmt.Errorf("缺少登录/预认证 URL")
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw, nil
	}
	base, err := url.Parse(t.BaseURL)
	if err != nil {
		return "", err
	}
	rel, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(rel).String(), nil
}

func substituteAuthPlaceholders(s, user, pass string) string {
	repl := strings.NewReplacer(
		"{{username}}", user,
		"{{password}}", pass,
		"{{user}}", user,
		"{{pass}}", pass,
		"{{USER}}", user,
		"{{PASS}}", pass,
	)
	return repl.Replace(s)
}

// resolveWebAuthHeaders prepares Nuclei -H headers, optionally performing a
// login/warmup HTTP request to capture session cookies.
func resolveWebAuthHeaders(t WebScanTarget, allowPrivate bool) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(t.AuthType)) {
	case "", "none":
		return nil, nil
	case "basic":
		if t.AuthUser == "" {
			return nil, fmt.Errorf("缺少用户名")
		}
		return []string{"Authorization: Basic " + basicAuthValue(t.AuthUser, t.AuthPass)}, nil
	case "bearer":
		tok := strings.TrimSpace(t.AuthPass)
		if tok == "" {
			for _, h := range parseHeaderLines(t.AuthHeader) {
				if i := strings.Index(h, ":"); i >= 0 && strings.EqualFold(strings.TrimSpace(h[:i]), "Authorization") {
					return []string{h}, nil
				}
			}
			return nil, fmt.Errorf("缺少 Bearer Token")
		}
		tok = strings.TrimSpace(strings.TrimPrefix(tok, "Bearer"))
		return []string{"Authorization: Bearer " + tok}, nil
	case "cookie", "header":
		hs := parseHeaderLines(t.AuthHeader)
		if len(hs) == 0 {
			return nil, fmt.Errorf("缺少请求头")
		}
		return hs, nil
	case "form", "header_body":
		return webAuthWarmup(t, allowPrivate)
	default:
		return nil, fmt.Errorf("未知鉴权类型")
	}
}

func webAuthWarmup(t WebScanTarget, allowPrivate bool) ([]string, error) {
	loginURL, err := resolveAuthLoginURL(t)
	if err != nil {
		return nil, err
	}
	if err := assertURLAllowed(loginURL, allowPrivate); err != nil {
		return nil, fmt.Errorf("登录地址：%w", err)
	}
	body := substituteAuthPlaceholders(t.AuthBody, t.AuthUser, t.AuthPass)
	if t.AuthType == "form" && body == "" && t.AuthUser != "" {
		body = url.Values{
			"username": {t.AuthUser},
			"password": {t.AuthPass},
			"user":     {t.AuthUser},
			"pass":     {t.AuthPass},
		}.Encode()
	}
	method := t.AuthMethod
	if method == "" {
		method = "POST"
	}
	var rdr io.Reader
	if method != "GET" && body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, loginURL, rdr)
	if err != nil {
		return nil, err
	}
	ct := t.AuthContentType
	if ct == "" {
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			ct = "application/json"
		} else {
			ct = "application/x-www-form-urlencoded"
		}
	}
	if method != "GET" && body != "" {
		req.Header.Set("Content-Type", ct)
	}
	for _, h := range parseHeaderLines(substituteAuthPlaceholders(t.AuthHeader, t.AuthUser, t.AuthPass)) {
		if i := strings.Index(h, ":"); i > 0 {
			req.Header.Set(strings.TrimSpace(h[:i]), strings.TrimSpace(h[i+1:]))
		}
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 45 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("登录重定向过多")
			}
			if req.URL != nil {
				if err := assertURLAllowed(req.URL.String(), allowPrivate); err != nil {
					return fmt.Errorf("登录重定向被拒绝：%w", err)
				}
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("预认证请求失败：%w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	var headers []string
	// Static headers (except Cookie — session cookie comes from jar).
	for _, h := range parseHeaderLines(substituteAuthPlaceholders(t.AuthHeader, t.AuthUser, t.AuthPass)) {
		name := h
		if i := strings.Index(h, ":"); i > 0 {
			name = strings.TrimSpace(h[:i])
		}
		if strings.EqualFold(name, "Cookie") {
			continue
		}
		headers = append(headers, h)
	}
	u, _ := url.Parse(t.BaseURL)
	if u != nil {
		if cookies := jar.Cookies(u); len(cookies) > 0 {
			parts := make([]string, 0, len(cookies))
			for _, c := range cookies {
				parts = append(parts, c.Name+"="+c.Value)
			}
			headers = append(headers, "Cookie: "+strings.Join(parts, "; "))
		} else if loginU, err := url.Parse(loginURL); err == nil {
			if cookies := jar.Cookies(loginU); len(cookies) > 0 {
				parts := make([]string, 0, len(cookies))
				for _, c := range cookies {
					parts = append(parts, c.Name+"="+c.Value)
				}
				headers = append(headers, "Cookie: "+strings.Join(parts, "; "))
			}
		}
	}
	if t.AuthType == "form" && !headerLinesHas(headers, "Cookie") && !headerLinesHas(headers, "Authorization") {
		return nil, fmt.Errorf("登录后未获得 Cookie/Token（HTTP %d）。请检查登录 URL、请求体占位符 {{username}}/{{password}} 或改用请求头鉴权", resp.StatusCode)
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("预认证未产生可用鉴权头（HTTP %d）", resp.StatusCode)
	}
	return headers, nil
}

func headerLinesHas(headers []string, name string) bool {
	for _, h := range headers {
		if i := strings.Index(h, ":"); i > 0 && strings.EqualFold(strings.TrimSpace(h[:i]), name) {
			return true
		}
	}
	return false
}

// Mask secrets for API responses.
func maskWebTarget(t WebScanTarget) WebScanTarget {
	if t.AuthPass != "" {
		t.AuthPass = "********"
	}
	if t.AuthHeader != "" {
		t.AuthHeader = maskAuthHeader(t.AuthHeader)
	}
	if t.AuthBody != "" {
		t.AuthBody = maskAuthBody(t.AuthBody)
	}
	return t
}

func maskAuthHeader(h string) string {
	var lines []string
	for _, ln := range strings.Split(h, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if i := strings.Index(ln, ":"); i >= 0 {
			lines = append(lines, strings.TrimSpace(ln[:i])+": ********")
		} else {
			lines = append(lines, "********")
		}
	}
	return strings.Join(lines, "\n")
}

func maskAuthBody(body string) string {
	b := strings.TrimSpace(body)
	if b == "" {
		return b
	}
	// Keep structure hint for editors; hide values.
	if strings.HasPrefix(b, "{") {
		return `{"_masked":true,"hint":"********"}`
	}
	return "********"
}
