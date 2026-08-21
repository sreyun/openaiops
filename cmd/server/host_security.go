package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	hostSecMaxScans   = 200
	hostSecMaxPkgsOSV = 120
	defaultOSVURL     = "https://api.osv.dev/v1/querybatch"

	defaultFIMMaxFiles   = 150000
	defaultFIMMaxChanges = 500
	defaultFIMBudgetSec  = 90
)

// HostSecurityConfig controls scheduled host scans and OSV matching.
type HostSecurityConfig struct {
	Enabled       bool              `json:"enabled"`
	Schedule      *PlaybookSchedule `json:"schedule,omitempty"`
	HostIDs       []string          `json:"host_ids,omitempty"` // empty = all online
	OSVURL        string            `json:"osv_url,omitempty"`
	EnableClamAV  bool              `json:"enable_clamav"` // kept for API/UI; see clamAVEnabled()
	DisableClamAV bool              `json:"disable_clamav,omitempty"`
	// FIMEnabled / FIMContentDiff are API mirrors; defaults ON via Disable* opt-out.
	FIMEnabled            bool `json:"fim_enabled"`
	FIMContentDiff        bool `json:"fim_content_diff"`
	DisableFIM            bool `json:"disable_fim,omitempty"`
	DisableFIMContentDiff bool `json:"disable_fim_content_diff,omitempty"`
	// FIMScope selects "full" (walk every directory, metadata-only add/modify/delete)
	// or "sensitive" (legacy allowlist with server-side inventory diff).
	FIMScope        string   `json:"fim_scope,omitempty"`
	FIMRoots        []string `json:"fim_roots,omitempty"`
	FIMExcludes     []string `json:"fim_excludes,omitempty"`
	FIMContentPaths []string `json:"fim_content_paths,omitempty"` // content-audit whitelist (globs)
	FIMMaxFiles     int      `json:"fim_max_files,omitempty"`
	FIMMaxChanges   int      `json:"fim_max_changes,omitempty"`
	FIMBudgetSec    int      `json:"fim_budget_sec,omitempty"`
	TimeoutSec      int      `json:"timeout_sec,omitempty"`
	// AutoAISummary runs host_security_diagnosis after completed scans (non-blocking).
	AutoAISummary bool `json:"auto_ai_summary,omitempty"`
}

func (c HostSecurityConfig) withDefaults() HostSecurityConfig {
	if c.OSVURL == "" {
		c.OSVURL = defaultOSVURL
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 180
	}
	c.FIMScope = normalizeFIMScope(c.FIMScope)
	if c.FIMMaxFiles <= 0 {
		c.FIMMaxFiles = defaultFIMMaxFiles
	}
	if c.FIMMaxChanges <= 0 {
		c.FIMMaxChanges = defaultFIMMaxChanges
	}
	if c.FIMBudgetSec <= 0 {
		c.FIMBudgetSec = defaultFIMBudgetSec
	}
	return c
}

// normalizeFIMScope defaults to full-filesystem monitoring.
func normalizeFIMScope(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "sensitive") {
		return "sensitive"
	}
	return "full"
}

// clamAVEnabled defaults ON. Opt out with disable_clamav=true (preferred) or
// enable_clamav=false when disable_clamav is unset and config was explicitly saved.
func (c HostSecurityConfig) clamAVEnabled() bool { return !c.DisableClamAV }

// fimEnabled defaults ON. Opt out with disable_fim=true.
func (c HostSecurityConfig) fimEnabled() bool {
	return !c.DisableFIM
}

// fimContentDiffEnabled defaults ON. Opt out with disable_fim_content_diff=true.
func (c HostSecurityConfig) fimContentDiffEnabled() bool {
	return !c.DisableFIMContentDiff
}

func (cs *ConfigStore) HostSecurity() HostSecurityConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.cfg.HostSecurity.withDefaults()
}

func (cs *ConfigStore) SetHostSecurity(c HostSecurityConfig) error {
	cs.mu.Lock()
	if c.Schedule != nil {
		if err := sanitizeSchedule(c.Schedule); err != nil {
			cs.mu.Unlock()
			return err
		}
	}
	if c.OSVURL == "" {
		c.OSVURL = defaultOSVURL
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 180
	}
	c.FIMScope = normalizeFIMScope(c.FIMScope)
	c.FIMRoots = sanitizeFIMPathList(c.FIMRoots, 32)
	c.FIMExcludes = sanitizeFIMPathList(c.FIMExcludes, 200)
	c.FIMContentPaths = sanitizeFIMPathList(c.FIMContentPaths, 200)
	c.FIMMaxFiles = clampInt(c.FIMMaxFiles, defaultFIMMaxFiles, 1000, 2000000)
	c.FIMMaxChanges = clampInt(c.FIMMaxChanges, defaultFIMMaxChanges, 10, 5000)
	c.FIMBudgetSec = clampInt(c.FIMBudgetSec, defaultFIMBudgetSec, 5, 900)
	cs.cfg.HostSecurity = c
	cs.mu.Unlock()
	return cs.save()
}

func (cs *ConfigStore) securityDataDir() string {
	cs.mu.RLock()
	p := cs.path
	cs.mu.RUnlock()
	dir := filepath.Join(filepath.Dir(p), "security")
	_ = os.MkdirAll(dir, 0o750)
	return dir
}

// --- Agent report shape (subset) ---

type hsAgentPkg struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Ecosystem string `json:"ecosystem,omitempty"`
}

type hsAgentFinding struct {
	Level   string `json:"level"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Suggest string `json:"suggest,omitempty"`
}

type hsAgentMalware struct {
	ClamAV    string           `json:"clamav"`
	Version   string           `json:"version,omitempty"`
	Scanned   int              `json:"scanned"`
	Infected  []string         `json:"infected,omitempty"`
	Findings  []hsAgentFinding `json:"findings,omitempty"`
	DBAgeDays int              `json:"db_age_days,omitempty"`
	DBUpdated int64            `json:"db_updated,omitempty"`
}

type hsAgentFirewall struct {
	Status string `json:"status"`
	Engine string `json:"engine,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type hsAgentFileInv struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
	Mtime  int64  `json:"mtime,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

type hsAgentTextDiff struct {
	Path      string `json:"path"`
	OldSHA    string `json:"old_sha,omitempty"`
	NewSHA    string `json:"new_sha,omitempty"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// hsAgentFileChange is a delta computed by a full-scope FIM agent.
type hsAgentFileChange struct {
	Path      string `json:"path"`
	Change    string `json:"change"`
	Reason    string `json:"reason,omitempty"`
	Kind      string `json:"kind,omitempty"`
	OldSHA    string `json:"old_sha,omitempty"`
	NewSHA    string `json:"new_sha,omitempty"`
	OldSize   int64  `json:"old_size,omitempty"`
	NewSize   int64  `json:"new_size,omitempty"`
	OldMtime  int64  `json:"old_mtime,omitempty"`
	NewMtime  int64  `json:"new_mtime,omitempty"`
	OldMode   string `json:"old_mode,omitempty"`
	NewMode   string `json:"new_mode,omitempty"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type hsAgentFIMStats struct {
	Mode         string   `json:"mode"`
	Baseline     bool     `json:"baseline,omitempty"`
	Roots        []string `json:"roots,omitempty"`
	Files        int      `json:"files"`
	Dirs         int      `json:"dirs"`
	Added        int      `json:"added"`
	Removed      int      `json:"removed"`
	Modified     int      `json:"modified"`
	Reported     int      `json:"reported"`
	Hashed       int      `json:"hashed,omitempty"`
	Skipped      int      `json:"skipped,omitempty"`
	LimitHit     bool     `json:"limit_hit,omitempty"`
	BudgetHit    bool     `json:"budget_hit,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	DurationMS   int64    `json:"duration_ms"`
	ContentPaths int      `json:"content_paths,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type hsAgentReport struct {
	CollectedAt   int64               `json:"collected_at"`
	Hostname      string              `json:"hostname"`
	OS            string              `json:"os"`
	Arch          string              `json:"arch"`
	Kernel        string              `json:"kernel,omitempty"`
	Distro        string              `json:"distro,omitempty"`
	PkgMgr        string              `json:"pkg_mgr,omitempty"`
	Packages      []hsAgentPkg        `json:"packages"`
	Listeners     []string            `json:"listeners"`
	Processes     []string            `json:"processes"`
	Hardening     []hsAgentFinding    `json:"hardening"`
	IOC           []hsAgentFinding    `json:"ioc"`
	Malware       hsAgentMalware      `json:"malware"`
	Firewall      hsAgentFirewall     `json:"firewall"`
	FileInventory []hsAgentFileInv    `json:"file_inventory,omitempty"`
	FileTextDiffs []hsAgentTextDiff   `json:"file_text_diffs,omitempty"`
	FileChanges   []hsAgentFileChange `json:"file_changes,omitempty"`
	FIMStats      *hsAgentFIMStats    `json:"fim_stats,omitempty"`
	Meta          map[string]any      `json:"meta,omitempty"`
}

// HostFileHash is a trimmed inventory entry stored with a scan.
type HostFileHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
	Mtime  int64  `json:"mtime,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

// HostFileChange is a FIM delta: either agent-computed (full scope) or derived
// server-side from the inventory of a sensitive-scope agent.
type HostFileChange struct {
	Path      string `json:"path"`
	Change    string `json:"change"`           // added|removed|modified
	Reason    string `json:"reason,omitempty"` // content|size|mtime|mode
	Kind      string `json:"kind,omitempty"`
	OldSHA    string `json:"old_sha,omitempty"`
	NewSHA    string `json:"new_sha,omitempty"`
	OldSize   int64  `json:"old_size,omitempty"`
	NewSize   int64  `json:"new_size,omitempty"`
	OldMtime  int64  `json:"old_mtime,omitempty"`
	NewMtime  int64  `json:"new_mtime,omitempty"`
	OldMode   string `json:"old_mode,omitempty"`
	NewMode   string `json:"new_mode,omitempty"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// HostFIMStats records what the agent actually walked, so the UI can state the
// real coverage instead of implying a full filesystem was inspected.
type HostFIMStats struct {
	Mode         string   `json:"mode"` // full|sensitive
	Baseline     bool     `json:"baseline,omitempty"`
	Roots        []string `json:"roots,omitempty"`
	Files        int      `json:"files"`
	Dirs         int      `json:"dirs"`
	Added        int      `json:"added"`
	Removed      int      `json:"removed"`
	Modified     int      `json:"modified"`
	Reported     int      `json:"reported"`
	Skipped      int      `json:"skipped,omitempty"`
	LimitHit     bool     `json:"limit_hit,omitempty"`
	BudgetHit    bool     `json:"budget_hit,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	DurationMS   int64    `json:"duration_ms,omitempty"`
	ContentPaths int      `json:"content_paths,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// HostFinding is a normalized finding after server-side enrichment.
type HostFinding struct {
	Level      string `json:"level"`
	Category   string `json:"category"` // hardening|malware|ioc|cve|port|fim
	ID         string `json:"id"`
	Title      string `json:"title"`
	Detail     string `json:"detail,omitempty"`
	Suggest    string `json:"suggest,omitempty"`
	Package    string `json:"package,omitempty"`
	Version    string `json:"version,omitempty"`
	FixedIn    string `json:"fixed_in,omitempty"`
	CVE        string `json:"cve,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Status     string `json:"status,omitempty"` // open|ack|false_positive|resolved
	StatusNote string `json:"status_note,omitempty"`
	// Compliance maps the finding to the controls it violates (CIS / 等保2.0 /
	// PCI-DSS / ISO27001) so scan output is usable as audit evidence.
	Compliance []ComplianceRef `json:"compliance,omitempty"`
}

// HostScanResult is one completed (or failed) host security scan.
type HostScanResult struct {
	ID             string            `json:"id"`
	Label          string            `json:"label,omitempty"`
	Seq            int               `json:"seq,omitempty"`
	HostID         string            `json:"host_id"`
	Hostname       string            `json:"hostname,omitempty"`
	StartedAt      int64             `json:"started_at"`
	FinishedAt     int64             `json:"finished_at,omitempty"`
	Status         string            `json:"status"` // running|completed|failed
	Error          string            `json:"error,omitempty"`
	Score          int               `json:"score"` // 0–100
	Risk           string            `json:"risk"`  // critical|high|medium|low|info
	ClamAV         string            `json:"clamav,omitempty"`
	ClamAVDBAge    int               `json:"clamav_db_age_days,omitempty"`
	Firewall       string            `json:"firewall,omitempty"`        // on|off|partial|unknown
	FirewallEngine string            `json:"firewall_engine,omitempty"` // ufw|firewalld|macos|windows|...
	FirewallDetail string            `json:"firewall_detail,omitempty"`
	OS             string            `json:"os,omitempty"`
	Distro         string            `json:"distro,omitempty"`
	PkgCount       int               `json:"pkg_count"`
	CVECount       int               `json:"cve_count"`
	PortCount      int               `json:"port_count"`
	RiskyPortCount int               `json:"risky_port_count"`
	OpenPorts      []HostOpenPort    `json:"open_ports,omitempty"`
	PortSample     []int             `json:"port_sample,omitempty"` // compact list for tables
	Findings       []HostFinding     `json:"findings"`
	Summary        map[string]int    `json:"summary"` // level → count
	Remediation    []string          `json:"remediation,omitempty"`
	Operator       string            `json:"operator,omitempty"`
	Trigger        string            `json:"trigger,omitempty"` // manual|schedule
	BaselineDiff   *ScanBaselineDiff `json:"baseline_diff,omitempty"`
	FileInventory  []HostFileHash    `json:"file_inventory,omitempty"`
	FileChanges    []HostFileChange  `json:"file_changes,omitempty"`
	FIMBaseline    bool              `json:"fim_baseline_established,omitempty"`
	FIMStats       *HostFIMStats     `json:"fim_stats,omitempty"`
	Compliance     map[string]int    `json:"compliance,omitempty"` // framework → failing controls
	AISummary      string            `json:"ai_summary,omitempty"`
	AISummaryAt    int64             `json:"ai_summary_at,omitempty"`
}

type hostSecurityManager struct {
	mu         sync.Mutex
	scans      []*HostScanResult
	lastByHost map[string]*HostScanResult
	lastRun    map[string]int64 // schedule key → unix
	dir        string
	seq        int
}

func newHostSecurityManager(dir string) *hostSecurityManager {
	m := &hostSecurityManager{
		scans:      make([]*HostScanResult, 0, 32),
		lastByHost: map[string]*HostScanResult{},
		lastRun:    map[string]int64{},
		dir:        dir,
	}
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

func (m *hostSecurityManager) path() string {
	return filepath.Join(m.dir, "host_scans.json")
}

func (m *hostSecurityManager) load() {
	b, err := os.ReadFile(m.path())
	if err != nil {
		return
	}
	var list []*HostScanResult
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
	var newestFinished int64
	for _, s := range list {
		if s == nil || s.HostID == "" {
			continue
		}
		// Same preference as rememberLastLocked: completed beats newer failed,
		// so a flaky retry after restart cannot wipe the last good posture.
		m.rememberLastLocked(s)
		if s.FinishedAt > newestFinished && (s.Status == "completed" || s.Status == "failed") {
			newestFinished = s.FinishedAt
		}
	}
	// Seed schedule lastRun so interval jobs don't re-fire immediately after restart.
	if newestFinished > 0 {
		m.lastRun["host"] = newestFinished
	}
	if dirty {
		m.saveLocked()
	}
}

func (m *hostSecurityManager) saveLocked() {
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

// finishIfRunning applies completion only while the scan is still running.
func (m *hostSecurityManager) finishIfRunning(id string, apply func(live *HostScanResult)) bool {
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
		m.rememberLastLocked(s)
		m.saveLocked()
		return true
	}
	return false
}

// rememberLastLocked prefers the latest completed scan over a newer failed one
// so a flaky retry does not wipe the last good posture from the host summary.
func (m *hostSecurityManager) rememberLastLocked(scan *HostScanResult) {
	if scan == nil || scan.HostID == "" {
		return
	}
	switch scan.Status {
	case "completed":
		m.lastByHost[scan.HostID] = scan
	case "failed":
		prev := m.lastByHost[scan.HostID]
		if prev == nil || prev.Status != "completed" {
			m.lastByHost[scan.HostID] = scan
		}
	}
}

func (m *hostSecurityManager) list(limit int) []*HostScanResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > len(m.scans) {
		limit = len(m.scans)
	}
	out := make([]*HostScanResult, 0, limit)
	for _, s := range m.scans[:limit] {
		if s == nil {
			continue
		}
		out = append(out, summarizeHostScanForList(s))
	}
	return out
}

// summarizeHostScanForList drops bulky fields (findings / FIM inventory / ports)
// so poll+history tables stay fast; detail GET still returns the full scan.
func summarizeHostScanForList(s *HostScanResult) *HostScanResult {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Findings = nil
	cp.OpenPorts = nil
	cp.FileInventory = nil
	cp.FileChanges = nil
	cp.Remediation = nil
	cp.BaselineDiff = nil
	cp.AISummary = ""
	if s.PortSample != nil {
		cp.PortSample = append([]int(nil), s.PortSample...)
	}
	if s.Summary != nil {
		cp.Summary = make(map[string]int, len(s.Summary))
		for k, v := range s.Summary {
			cp.Summary[k] = v
		}
	}
	if s.Compliance != nil {
		cp.Compliance = make(map[string]int, len(s.Compliance))
		for k, v := range s.Compliance {
			cp.Compliance[k] = v
		}
	}
	if s.FIMStats != nil {
		st := *s.FIMStats
		cp.FIMStats = &st
	}
	return &cp
}

func (m *hostSecurityManager) get(id string) *HostScanResult {
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

func (m *hostSecurityManager) summary() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, 0, len(m.lastByHost))
	for _, s := range m.lastByHost {
		if s == nil {
			continue
		}
		out = append(out, map[string]any{
			"host_id":          s.HostID,
			"hostname":         s.Hostname,
			"score":            s.Score,
			"risk":             s.Risk,
			"clamav":           s.ClamAV,
			"firewall":         s.Firewall,
			"firewall_engine":  s.FirewallEngine,
			"firewall_detail":  s.FirewallDetail,
			"cve_count":        s.CVECount,
			"pkg_count":        s.PkgCount,
			"port_count":       s.PortCount,
			"risky_port_count": s.RiskyPortCount,
			"port_sample":      s.PortSample,
			"os":               s.OS,
			"distro":           s.Distro,
			"finished_at":      s.FinishedAt,
			"status":           s.Status,
			"scan_id":          s.ID,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		si, _ := out[i]["score"].(int)
		sj, _ := out[j]["score"].(int)
		return si < sj
	})
	return out
}

// --- OSV ---

type osvQuery struct {
	Package *osvPkg `json:"package,omitempty"`
	Version string  `json:"version,omitempty"`
}

type osvPkg struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type osvBatchReq struct {
	Queries []osvQuery `json:"queries"`
}

type osvVuln struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	DatabaseSpecific map[string]any `json:"database_specific"`
}

type osvBatchResult struct {
	Results []struct {
		Vulns []osvVuln `json:"vulns"`
	} `json:"results"`
}

func mapPkgEcosystem(pkg hsAgentPkg, distro, pkgMgr string) (eco, name string) {
	name = strings.TrimSpace(pkg.Name)
	if name == "" {
		return "", ""
	}
	if e := strings.TrimSpace(pkg.Ecosystem); e != "" {
		return e, name
	}
	switch strings.ToLower(pkgMgr) {
	case "apk":
		return "Alpine", name
	case "rpm":
		d := strings.ToLower(distro)
		switch {
		case strings.Contains(d, "fedora"):
			return "Fedora", name
		case strings.Contains(d, "rocky"):
			return "Rocky Linux", name
		case strings.Contains(d, "alma"):
			return "AlmaLinux", name
		case strings.Contains(d, "kylin") || strings.Contains(d, "neokylin"):
			return "Kylin", name
		case strings.Contains(d, "openeuler"):
			return "openEuler", name
		case strings.Contains(d, "euleros") || strings.Contains(d, "euler os"):
			return "openEuler", name // EulerOS shares openEuler/RHEL OSV lineage
		case strings.Contains(d, "alinux") || strings.Contains(d, "alibaba"):
			return "Red Hat", name
		case strings.Contains(d, "centos"):
			return "Red Hat", name // CentOS stream maps closest to RHEL OSV
		default:
			return "Red Hat", name
		}
	case "brew":
		return "Homebrew", name
	case "winget", "choco":
		return "", "" // OSV coverage limited; skip
	default:
		// dpkg / apt
		d := strings.ToLower(distro)
		if strings.Contains(d, "ubuntu") {
			return "Ubuntu", name
		}
		return "Debian", name
	}
}

func queryOSVBatch(ctx context.Context, url string, pkgs []hsAgentPkg, distro, pkgMgr string) ([]HostFinding, error) {
	if url == "" {
		url = defaultOSVURL
	}
	queries := make([]osvQuery, 0, len(pkgs))
	idxMap := make([]int, 0, len(pkgs))
	for i, p := range pkgs {
		eco, name := mapPkgEcosystem(p, distro, pkgMgr)
		ver := strings.TrimSpace(p.Version)
		if eco == "" || name == "" || ver == "" {
			continue
		}
		queries = append(queries, osvQuery{Package: &osvPkg{Name: name, Ecosystem: eco}, Version: ver})
		idxMap = append(idxMap, i)
		if len(queries) >= hostSecMaxPkgsOSV {
			break
		}
	}
	if len(queries) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(osvBatchReq{Queries: queries})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// SSRF: OSV URL is configurable — block cloud metadata / link-local.
	client := newGuardedHTTPClient(45 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("osv http %d: %s", resp.StatusCode, truncateRun(string(raw), 200))
	}
	var batch osvBatchResult
	if err := json.Unmarshal(raw, &batch); err != nil {
		return nil, err
	}
	var findings []HostFinding
	for ri, r := range batch.Results {
		if ri >= len(idxMap) {
			break
		}
		p := pkgs[idxMap[ri]]
		for _, v := range r.Vulns {
			sev := osvSeverity(v)
			level := severityToLevel(sev)
			cve := v.ID
			for _, alias := range osvAliases(v) {
				if strings.HasPrefix(alias, "CVE-") {
					cve = alias
					break
				}
			}
			title := v.Summary
			if title == "" {
				title = cve
			}
			findings = append(findings, HostFinding{
				Level:    level,
				Category: "cve",
				ID:       v.ID + "@" + p.Name, // package-scoped so same CVE on different pkgs is distinct
				Title:    title,
				Detail:   fmt.Sprintf("%s %s — %s", p.Name, p.Version, cve),
				Suggest:  pkgUpgradeSuggest(pkgMgr, p.Name),
				Package:  p.Name,
				Version:  p.Version,
				CVE:      cve,
				Severity: sev,
			})
		}
	}
	return findings, nil
}

func osvAliases(v osvVuln) []string {
	if v.DatabaseSpecific == nil {
		return nil
	}
	raw, ok := v.DatabaseSpecific["cve_id"]
	if !ok {
		return nil
	}
	switch t := raw.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func osvSeverity(v osvVuln) string {
	if v.DatabaseSpecific != nil {
		if s, ok := v.DatabaseSpecific["severity"].(string); ok && s != "" {
			return strings.ToUpper(s)
		}
	}
	for _, s := range v.Severity {
		sc := strings.ToUpper(s.Score)
		if strings.Contains(sc, "CRITICAL") {
			return "CRITICAL"
		}
		if strings.Contains(sc, "HIGH") {
			return "HIGH"
		}
		if strings.Contains(sc, "MEDIUM") || strings.Contains(sc, "MODERATE") {
			return "MEDIUM"
		}
		if strings.Contains(sc, "LOW") {
			return "LOW"
		}
		// CVSS numeric
		var f float64
		if _, err := fmt.Sscanf(s.Score, "%f", &f); err == nil {
			if f >= 9 {
				return "CRITICAL"
			}
			if f >= 7 {
				return "HIGH"
			}
			if f >= 4 {
				return "MEDIUM"
			}
			return "LOW"
		}
	}
	return "MEDIUM"
}

func severityToLevel(sev string) string {
	switch strings.ToUpper(sev) {
	case "CRITICAL":
		return "crit"
	case "HIGH":
		return "high"
	case "MEDIUM", "MODERATE":
		return "medium"
	case "LOW":
		return "low"
	default:
		return "medium"
	}
}

func pkgUpgradeSuggest(pkgMgr, name string) string {
	switch strings.ToLower(pkgMgr) {
	case "apk":
		return "apk upgrade " + name
	case "rpm":
		return "dnf upgrade " + name + "  # or yum update " + name
	case "brew":
		return "brew upgrade " + name
	default:
		return "apt-get install --only-upgrade " + name
	}
}

func scoreHostFindings(findings []HostFinding) (score int, risk string, summary map[string]int) {
	summary = map[string]int{}
	deduct := 0
	for _, f := range findings {
		summary[f.Level]++
		if f.Category == "fim" {
			summary["fim"]++
		}
		switch f.Level {
		case "crit":
			deduct += 25
		case "high":
			deduct += 12
		case "medium":
			deduct += 5
		case "low":
			deduct += 2
		}
	}
	score = 100 - deduct
	if score < 0 {
		score = 0
	}
	switch {
	case summary["crit"] > 0:
		risk = "critical"
	case summary["high"] > 0:
		risk = "high"
	case summary["medium"] > 0:
		risk = "medium"
	case summary["low"] > 0:
		risk = "low"
	default:
		risk = "info"
	}
	return score, risk, summary
}

func buildRemediation(rep hsAgentReport, findings []HostFinding) []string {
	seen := map[string]bool{}
	var tips []string
	add := func(tip string) {
		tip = strings.TrimSpace(tip)
		if tip == "" {
			return
		}
		key := strings.ToLower(tip)
		if seen[key] {
			return
		}
		seen[key] = true
		tips = append(tips, tip)
	}
	switch rep.Malware.ClamAV {
	case "unavailable":
		switch strings.ToLower(rep.OS) {
		case "darwin":
			add("macOS：brew install clamav && sudo freshclam，然后重启 Agent 再扫描")
		case "windows":
			add("Windows：安装 ClamAV 并确保 clamscan 在 PATH 中，然后重启 Agent")
		default:
			add("Linux：安装 ClamAV（apt/yum/apk install clamav）并执行 freshclam 更新病毒库")
		}
	case "error":
		add("ClamAV 已安装但病毒库异常：请在目标主机执行 sudo freshclam 后重试")
	}
	if len(rep.Malware.Infected) > 0 {
		add("立即隔离并处置 ClamAV 命中文件，复核启动项与 crontab")
	}
	switch strings.ToLower(rep.Firewall.Status) {
	case "off":
		add("启用系统防火墙并按业务最小化放行入站端口")
	case "partial":
		add("系统防火墙部分配置文件未开启，请统一开启域/专用/公用配置")
	}
	cveSeen := 0
	for _, f := range findings {
		if f.Category == "cve" && f.Suggest != "" && cveSeen < 8 {
			line := f.Suggest
			if f.CVE != "" {
				line += "  # " + f.CVE
			}
			if f.Package != "" {
				line += " (" + f.Package + ")"
			}
			add(line)
			cveSeen++
		}
	}
	for _, f := range findings {
		if f.Category == "hardening" && f.Suggest != "" && len(tips) < 20 {
			add(f.Suggest)
		}
	}
	portTips := 0
	for _, f := range findings {
		if f.Category == "port" && f.Suggest != "" && portTips < 6 && len(tips) < 24 {
			add(f.Suggest + "  # " + f.Title)
			portTips++
		}
	}
	fimTips := 0
	for _, f := range findings {
		if f.Category == "fim" && f.Suggest != "" && fimTips < 6 && len(tips) < 28 {
			add(f.Suggest + "  # " + f.Title)
			fimTips++
		}
	}
	return tips
}

func normalizeAgentFindings(rep hsAgentReport, cves []HostFinding) []HostFinding {
	var out []HostFinding
	for _, f := range rep.Hardening {
		id := strings.TrimSpace(f.ID)
		if id != "" && f.Detail != "" && (id == "world_writable" || strings.HasSuffix(id, "_writable")) {
			id = id + "." + shortDiscHash(f.Detail)
		}
		out = append(out, HostFinding{
			Level: f.Level, Category: "hardening", ID: id, Title: f.Title,
			Detail: f.Detail, Suggest: f.Suggest,
		})
	}
	for _, f := range rep.IOC {
		id := strings.TrimSpace(f.ID)
		if id != "" && f.Detail != "" {
			id = id + "." + shortDiscHash(f.Detail)
		}
		title := f.Title
		if f.Detail != "" && !strings.Contains(title, f.Detail) {
			title = title + " — " + truncateRun(f.Detail, 80)
		}
		out = append(out, HostFinding{
			Level: f.Level, Category: "ioc", ID: id, Title: title,
			Detail: f.Detail, Suggest: f.Suggest,
		})
	}
	seenInfected := map[string]bool{}
	for _, f := range rep.Malware.Findings {
		id := strings.TrimSpace(f.ID)
		if id == "" {
			id = "malware"
		}
		if f.Detail != "" {
			id = id + "." + shortDiscHash(f.Detail)
		}
		title := f.Title
		if f.Detail != "" && strings.EqualFold(title, "ClamAV 命中") {
			title = "ClamAV 命中 — " + truncateRun(f.Detail, 80)
		}
		out = append(out, HostFinding{
			Level: f.Level, Category: "malware", ID: id, Title: title,
			Detail: f.Detail, Suggest: f.Suggest,
		})
		if f.Detail != "" {
			seenInfected[f.Detail] = true
		}
	}
	for _, path := range rep.Malware.Infected {
		if seenInfected[path] {
			continue
		}
		out = append(out, HostFinding{
			Level: "crit", Category: "malware", ID: "clamav.infected." + shortDiscHash(path),
			Title: "ClamAV 命中 — " + truncateRun(path, 80), Detail: path,
			Suggest: "隔离并删除/检疫该文件，排查横向移动痕迹",
		})
	}
	out = append(out, cves...)
	return out
}

// mergeAndCapFindings keeps high-severity and port findings when capping for UI/storage.
func mergeAndCapFindings(base, ports []HostFinding, limit int) []HostFinding {
	if limit <= 0 {
		limit = 400
	}
	all := append([]HostFinding(nil), base...)
	all = append(all, ports...)
	if len(all) <= limit {
		return all
	}
	sort.SliceStable(all, func(i, j int) bool {
		return sevRank(hostLevelToSev(all[i].Level)) > sevRank(hostLevelToSev(all[j].Level))
	})
	return all[:limit]
}

func hostLevelToSev(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "crit", "critical":
		return "critical"
	case "high":
		return "high"
	case "medium", "med", "warn", "warning":
		return "medium"
	case "low":
		return "low"
	default:
		return "info"
	}
}

func (m *hostSecurityManager) hasRunning(hostID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapStuckLocked(0)
	for _, s := range m.scans {
		if s != nil && s.HostID == hostID && s.Status == "running" {
			return true
		}
	}
	return false
}

// reapStuck fails scans that have been running longer than timeoutSec+grace.
// timeoutSec<=0 defaults to 180s; grace defaults to 60s.
func (m *hostSecurityManager) reapStuck(timeoutSec int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reapStuckLocked(timeoutSec)
}

func (m *hostSecurityManager) reapStuckLocked(timeoutSec int) int {
	if timeoutSec <= 0 {
		timeoutSec = 180
	}
	grace := int64(60)
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
			reportFault("scan", "host_security_timeout", "warning", sc.HostID,
				fmt.Sprintf("主机安全扫描「%s」超时中断（超过 %ds），本次结果作废", firstNonEmptyOrDash(sc.Hostname, sc.HostID), limit), "")
		}
	}
	if n > 0 {
		m.saveLocked()
	}
	return n
}

// cancelScan marks a running scan as failed/cancelled. Returns false if not found or not running.
func (m *hostSecurityManager) cancelScan(id string) bool {
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

func (s *Server) beginHostSecurityScan(hostID, operator, trigger string) *HostScanResult {
	hostID = strings.TrimSpace(hostID)
	h := s.hostByID(hostID)
	hostname := hostID
	if h == nil {
		return &HostScanResult{
			ID: "hs-err", Label: "主机不存在", HostID: hostID, Status: "failed",
			Error: "未找到主机", Findings: []HostFinding{}, Summary: map[string]int{},
		}
	}
	hostname = h.Hostname
	if hostname == "" {
		hostname = hostID
	}
	offlineSec := int64(s.cfg.Thresholds().OfflineAfter.Seconds())
	if offlineSec <= 0 {
		offlineSec = 180
	}
	if time.Now().Unix()-h.LastSeen > offlineSec {
		return &HostScanResult{
			ID: "hs-offline", Label: hostname + " · 离线", HostID: hostID, Hostname: hostname,
			Status: "failed", Error: "主机离线，无法启动扫描", Findings: []HostFinding{}, Summary: map[string]int{},
		}
	}
	// Atomic check+insert under one lock to avoid duplicate in-flight scans.
	s.hostSec.mu.Lock()
	s.hostSec.reapStuckLocked(0)
	for _, sc := range s.hostSec.scans {
		if sc != nil && sc.HostID == hostID && sc.Status == "running" {
			s.hostSec.mu.Unlock()
			return &HostScanResult{
				ID: "hs-busy", Label: hostname + " · 进行中", HostID: hostID, Hostname: hostname,
				Status: "failed", Error: "该主机已有扫描进行中，请稍后再试", Findings: []HostFinding{}, Summary: map[string]int{},
			}
		}
	}
	running := 0
	for _, sc := range s.hostSec.scans {
		if sc != nil && sc.Status == "running" {
			running++
		}
	}
	if running >= 12 {
		s.hostSec.mu.Unlock()
		return &HostScanResult{
			ID: "hs-queue", Label: "队列已满", HostID: hostID, Hostname: hostname,
			Status: "failed", Error: "扫描队列已满（最多 12 个进行中），请稍后再试",
			Findings: []HostFinding{}, Summary: map[string]int{},
		}
	}
	s.hostSec.seq++
	seq := s.hostSec.seq
	now := time.Now()
	id := fmt.Sprintf("hs-%03d-%s-%s", seq, now.Format("0102-1504"), randomHex(2))
	label := fmt.Sprintf("%s · #%03d · %s", hostname, seq, now.Format("01-02 15:04"))
	scan := &HostScanResult{
		ID:        id,
		Label:     label,
		Seq:       seq,
		HostID:    hostID,
		Hostname:  hostname,
		StartedAt: now.Unix(),
		Status:    "running",
		Operator:  operator,
		Trigger:   trigger,
		Findings:  []HostFinding{},
		Summary:   map[string]int{},
		Score:     100,
		Risk:      "info",
	}
	s.hostSec.scans = append([]*HostScanResult{scan}, s.hostSec.scans...)
	if len(s.hostSec.scans) > hostSecMaxScans {
		s.hostSec.scans = s.hostSec.scans[:hostSecMaxScans]
	}
	s.hostSec.saveLocked()
	s.hostSec.mu.Unlock()
	return scan
}

func (s *Server) finishHostSecurityScans(ids []string) {
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(1)
		// safeGo：扫描要解析 Agent 上报的一大堆外部数据，一次 panic 不该带走整个进程。
		// wg.Done / 信号量都在 fn 里，recover 之后 defer 照常执行，不会卡住 wg.Wait。
		safeGo("host-security-scan", func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.completeHostSecurityScan(id)
		})
	}
	wg.Wait()
}

func (s *Server) completeHostSecurityScan(scanID string) {
	scan := s.hostSec.get(scanID)
	if scan == nil || scan.Status != "running" {
		return
	}
	hostID := scan.HostID
	cfg := s.cfg.HostSecurity()
	args := map[string]string{}
	if !cfg.clamAVEnabled() {
		args["clamav"] = "0"
	}
	if !cfg.fimEnabled() {
		args["fim"] = "0"
	} else {
		applyFIMScanArgs(args, cfg)
	}
	if !cfg.fimContentDiffEnabled() {
		args["fim_diff"] = "0"
	}
	out, err := s.runAgentModule(hostID, "host_security_scan", args, cfg.TimeoutSec)
	finished := time.Now().Unix()
	if err != nil {
		errMsg := err.Error()
		if strings.TrimSpace(out) != "" {
			errMsg += ": " + truncateRun(out, 300)
		}
		_ = s.hostSec.finishIfRunning(scanID, func(live *HostScanResult) {
			live.FinishedAt = finished
			live.Status = "failed"
			live.Error = errMsg
		})
		return
	}
	var rep hsAgentReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		_ = s.hostSec.finishIfRunning(scanID, func(live *HostScanResult) {
			live.FinishedAt = finished
			live.Status = "failed"
			live.Error = "invalid agent report: " + err.Error()
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	cves, osvErr := queryOSVBatch(ctx, cfg.OSVURL, rep.Packages, rep.Distro, rep.PkgMgr)
	var osvFinding *HostFinding
	if osvErr != nil {
		slog.Warn("host security OSV query failed", "host", hostID, "err", osvErr)
		osvFinding = &HostFinding{
			Level: "low", Category: "cve", ID: "osv.unavailable",
			Title: "OSV CVE 匹配失败", Detail: osvErr.Error(),
			Suggest: "检查服务端出网或配置 osv_url / 代理",
		}
	}
	ports := parseListenPorts(rep.Listeners)
	base := normalizeAgentFindings(rep, cves)
	if osvFinding != nil {
		base = append([]HostFinding{*osvFinding}, base...)
	}

	curInv := trimHostFileInventory(rep.FileInventory)
	var prevFindings []HostFinding
	var prevID string
	var prevInv []HostFileHash
	s.hostSec.mu.Lock()
	// Prefer lastByHost when it is a prior completed scan; otherwise search history
	// (covers restart edge cases where lastByHost briefly held a failed scan).
	if prev := s.hostSec.lastByHost[hostID]; prev != nil && prev.Status == "completed" && prev.ID != scanID {
		prevFindings = append([]HostFinding(nil), prev.Findings...)
		prevID = prev.ID
		prevInv = append([]HostFileHash(nil), prev.FileInventory...)
	} else {
		for _, sc := range s.hostSec.scans {
			if sc == nil || sc.HostID != hostID || sc.Status != "completed" || sc.ID == scanID {
				continue
			}
			prevFindings = append([]HostFinding(nil), sc.Findings...)
			prevID = sc.ID
			prevInv = append([]HostFileHash(nil), sc.FileInventory...)
			break
		}
	}
	s.hostSec.mu.Unlock()

	var fileChanges []HostFileChange
	fimBaseline := false
	fimStats := convertAgentFIMStats(rep.FIMStats)
	// A full-scope agent owns its baseline and ships deltas; only legacy
	// sensitive-scope agents need the server to diff an inventory.
	agentOwnsFIM := fimStats != nil && fimStats.Mode == "full"
	if cfg.fimEnabled() {
		if agentOwnsFIM {
			fileChanges = convertAgentFileChanges(rep.FileChanges, cfg.fimContentDiffEnabled())
			fimBaseline = fimStats.Baseline
			curInv = nil
		} else {
			textDiffs := rep.FileTextDiffs
			if !cfg.fimContentDiffEnabled() {
				textDiffs = nil
			}
			fileChanges, fimBaseline = diffHostFileInventory(prevInv, curInv, textDiffs)
		}
		if len(fileChanges) > 0 {
			base = append(base, fimFindingsFromChanges(fileChanges)...)
		}
	}

	findings := annotateCompliance(mergeAndCapFindings(base, portRiskFindings(ports), 400))
	compliance := summarizeCompliance(findings)
	cveCount := 0
	for _, f := range findings {
		if f.Category == "cve" && f.ID != "osv.unavailable" {
			cveCount++
		}
	}
	portCount, riskyCount, portSample := summarizePorts(ports)
	score, risk, summary := scoreHostFindings(findings)
	tips := buildRemediation(rep, findings)
	baseDiff := diffHostFindings(prevFindings, findings, prevID)

	applied := s.hostSec.finishIfRunning(scanID, func(live *HostScanResult) {
		live.FinishedAt = finished
		if rep.Hostname != "" {
			live.Hostname = rep.Hostname
		}
		live.OS = rep.OS
		live.Distro = rep.Distro
		live.ClamAV = rep.Malware.ClamAV
		live.ClamAVDBAge = rep.Malware.DBAgeDays
		live.Firewall = strings.ToLower(strings.TrimSpace(rep.Firewall.Status))
		if live.Firewall == "" {
			live.Firewall = "unknown"
		}
		live.FirewallEngine = rep.Firewall.Engine
		live.FirewallDetail = truncateRun(rep.Firewall.Detail, 240)
		live.PkgCount = len(rep.Packages)
		live.CVECount = cveCount
		live.OpenPorts = ports
		live.PortCount = portCount
		live.RiskyPortCount = riskyCount
		live.PortSample = portSample
		live.Findings = findings
		live.Compliance = compliance
		live.Score = score
		live.Risk = risk
		live.Summary = summary
		live.Remediation = tips
		live.BaselineDiff = baseDiff
		if agentOwnsFIM {
			// Nothing to keep: the filesystem-wide baseline lives on the agent.
			live.FileInventory = nil
		} else {
			live.FileInventory = pickFIMInventoryToStore(curInv, live.FileInventory, prevInv, cfg.fimEnabled())
		}
		live.FileChanges = fileChanges
		live.FIMBaseline = fimBaseline
		live.FIMStats = fimStats
		live.Status = "completed"
		live.Error = ""
	})
	if applied {
		if done := s.hostSec.get(scanID); done != nil {
			s.notifyHostSecurityScanCompleted(done)
			s.maybeHostSecurityAISummary(done)
		}
	}
}

// runHostSecurityScan is used by the scheduler (synchronous worker path).
func (s *Server) runHostSecurityScan(hostID, operator, trigger string) *HostScanResult {
	scan := s.beginHostSecurityScan(hostID, operator, trigger)
	s.completeHostSecurityScan(scan.ID)
	return s.hostSec.get(scan.ID)
}

func (s *Server) startHostSecurityScheduler() {
	// 同 Web 扫描调度：panic 之后必须**把循环重新拉起来**，否则主机安全扫描会静默停摆。
	go superviseLoop("host-security-scheduler", func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			cfg := s.cfg.HostSecurity().withDefaults()
			if n := s.hostSec.reapStuck(cfg.TimeoutSec); n > 0 {
				slog.Info("host security watchdog reaped stuck scans", "count", n)
			}
			s.tickHostSecuritySchedule()
		}
	})
}

func (s *Server) handleHostSecurityScanCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.hostSec.cancelScan(id) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "扫描不存在或不在运行中"})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: "取消主机安全扫描 " + id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) tickHostSecuritySchedule() {
	cfg := s.cfg.HostSecurity()
	if !cfg.Enabled || cfg.Schedule == nil || !cfg.Schedule.Enabled {
		return
	}
	now := time.Now()
	if !hostSecScheduleDue(cfg.Schedule, s.hostSec, now) {
		return
	}
	ids := cfg.HostIDs
	if len(ids) == 0 {
		for _, h := range s.store.ListHosts() {
			offlineSec := int64(s.cfg.Thresholds().OfflineAfter.Seconds())
			if time.Now().Unix()-h.LastSeen > offlineSec {
				continue
			}
			ids = append(ids, h.ID)
		}
	}
	sem := make(chan struct{}, 3)
	for _, id := range ids {
		hid := id
		if s.hostSec.hasRunning(hid) {
			continue
		}
		sem <- struct{}{}
		safeGo("host-security-scan-scheduled", func() {
			defer func() { <-sem }()
			s.runHostSecurityScan(hid, "scheduler", "schedule")
		})
	}
}

func hostSecScheduleDue(sc *PlaybookSchedule, m *hostSecurityManager, now time.Time) bool {
	if sc == nil || !sc.Enabled {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := "host"
	last := m.lastRun[key]
	// Seed from newest completed/failed scan if never scheduled in this process.
	if last == 0 {
		for _, s := range m.lastByHost {
			if s != nil && s.FinishedAt > last {
				last = s.FinishedAt
			}
		}
		if last > 0 {
			m.lastRun[key] = last
		}
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
		if m.lastRun[key+":"+day] > 0 {
			return false
		}
		m.lastRun[key+":"+day] = now.Unix()
		return true
	case "weekly":
		mins, ok := parseHHMM(sc.At)
		if !ok || int(now.Weekday()) != sc.Weekday || now.Hour()*60+now.Minute() != mins {
			return false
		}
		wk := now.Format("2006-W02")
		if m.lastRun[key+":"+wk] > 0 {
			return false
		}
		m.lastRun[key+":"+wk] = now.Unix()
		return true
	}
	return false
}
