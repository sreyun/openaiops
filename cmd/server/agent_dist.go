package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// agentDistArtifact describes one downloadable agent binary under dist/.
type agentDistArtifact struct {
	Name    string `json:"name"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
	Path    string `json:"-"`
}

var agentDistCatalog = []struct {
	Name   string
	GOOS   string
	GOARCH string
}{
	{"aiops-agent-linux-amd64", "linux", "amd64"},
	{"aiops-agent-linux-arm64", "linux", "arm64"},
	// 国产化 / 嵌入式：麒麟-龙芯、开源 RISC-V、老旧 32 位 x86 与 armv7 单板。
	// 没有对应产物时 agentDistResolveForHost 会给出 no_artifact 跳过原因，
	// 自动升级只是不可用，不会静默失败。
	{"aiops-agent-linux-loong64", "linux", "loong64"},
	{"aiops-agent-linux-riscv64", "linux", "riscv64"},
	{"aiops-agent-linux-386", "linux", "386"},
	{"aiops-agent-linux-arm", "linux", "arm"},
	{"aiops-agent-darwin-amd64", "darwin", "amd64"},
	{"aiops-agent-darwin-arm64", "darwin", "arm64"},
	{"aiops-agent.exe", "windows", "amd64"},
	{"aiops-agent-windows-amd64-win2012.exe", "windows", "amd64"}, // Go 1.20 build for Server 2012/R2
	{"aiops-agent-windows-arm64.exe", "windows", "arm64"},
}

func agentDistBinaryName(goos, goarch string) (string, error) {
	goos, goarch = normalizeGOOSArch(goos, goarch)
	if goos == "" {
		return "", fmt.Errorf("unsupported platform %s/%s", goos, goarch)
	}
	for _, a := range agentDistCatalog {
		if a.GOOS == goos && a.GOARCH == goarch {
			return a.Name, nil
		}
	}
	return "", fmt.Errorf("unsupported platform %s/%s", goos, goarch)
}

// normalizeGOOSArch maps host-reported OS/arch strings to Go GOOS/GOARCH.
func normalizeGOOSArch(goos, goarch string) (string, string) {
	goos = strings.ToLower(strings.TrimSpace(goos))
	goarch = strings.ToLower(strings.TrimSpace(goarch))
	switch {
	case goos == "macos" || goos == "osx" || goos == "mac" || strings.Contains(goos, "darwin"):
		goos = "darwin"
	case goos == "windows" || strings.Contains(goos, "windows"):
		goos = "windows"
	case goos == "linux" || strings.Contains(goos, "linux"):
		goos = "linux"
	}
	switch goarch {
	case "x86_64", "x64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	case "i386", "i686", "x86":
		goarch = "386"
	case "loongarch64", "loong64":
		goarch = "loong64"
	case "riscv64", "riscv":
		goarch = "riscv64"
	case "armv7l", "armv7", "armv6l", "armhf", "arm":
		goarch = "arm"
	case "":
		// Empty arch is common on older Windows reports — default to amd64.
		if goos == "windows" || goos == "linux" || goos == "darwin" {
			goarch = "amd64"
		}
	}
	return goos, goarch
}

// agentDistCandidates returns preferred download names for a platform (first existing wins).
func agentDistCandidates(goos, goarch string) []string {
	goos, goarch = normalizeGOOSArch(goos, goarch)
	primary, err := agentDistBinaryName(goos, goarch)
	if err != nil {
		return nil
	}
	out := []string{primary}
	// Release CI also publishes the descriptive Windows name; some deployments
	// only copy one of the two into dist/.
	if goos == "windows" && goarch == "amd64" {
		for _, alt := range []string{"aiops-agent.exe", "aiops-agent-windows-amd64.exe", "aiops-agent-windows-amd64-win2012.exe"} {
			if alt != primary {
				out = append(out, alt)
			}
		}
	}
	return out
}

func (s *Server) listAgentDistManifest() []agentDistArtifact {
	out := make([]agentDistArtifact, 0, len(agentDistCatalog))
	if s.distDir == "" {
		return out
	}
	ver := strings.TrimSpace(appVersion)
	for _, a := range agentDistCatalog {
		p := filepath.Join(s.distDir, a.Name)
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		sum, err := cachedFileSHA256(p, fi)
		if err != nil {
			continue
		}
		out = append(out, agentDistArtifact{
			Name: a.Name, GOOS: a.GOOS, GOARCH: a.GOARCH,
			Size: fi.Size(), SHA256: sum, Version: ver, Path: p,
		})
	}
	return out
}

type shaCacheEntry struct {
	size    int64
	modTime int64
	sum     string
}

var (
	shaCacheMu sync.Mutex
	shaCache   = map[string]shaCacheEntry{}
)

func cachedFileSHA256(path string, fi os.FileInfo) (string, error) {
	mod := fi.ModTime().UnixNano()
	size := fi.Size()
	shaCacheMu.Lock()
	if e, ok := shaCache[path]; ok && e.size == size && e.modTime == mod {
		sum := e.sum
		shaCacheMu.Unlock()
		return sum, nil
	}
	shaCacheMu.Unlock()
	sum, err := fileSHA256HexServer(path)
	if err != nil {
		return "", err
	}
	shaCacheMu.Lock()
	shaCache[path] = shaCacheEntry{size: size, modTime: mod, sum: sum}
	shaCacheMu.Unlock()
	return sum, nil
}

func (s *Server) agentDistHas(goos, goarch string) bool {
	_, ok := s.agentDistResolve(goos, goarch)
	return ok
}

// agentDistResolve returns the on-disk /dl filename for goos/goarch.
func (s *Server) agentDistResolve(goos, goarch string) (string, bool) {
	return s.agentDistResolveNames(agentDistCandidates(goos, goarch))
}

// agentDistResolveForHost prefers the Server 2012/R2 (Go 1.20) artifact when the
// host identity indicates a pre–Windows 10 kernel. Legacy hosts NEVER fall back
// to modern aiops-agent.exe (Go ≥1.21 exits immediately on NT 6.2/6.3).
func (s *Server) agentDistResolveForHost(h *Host) (string, bool) {
	if hostNeedsWin2012Agent(h) {
		const win2012 = "aiops-agent-windows-amd64-win2012.exe"
		if s == nil || s.distDir == "" {
			return "", false
		}
		if _, err := os.Stat(filepath.Join(s.distDir, win2012)); err != nil {
			return "", false
		}
		return win2012, true
	}
	return s.agentDistResolveNames(agentDistCandidatesForHost(h))
}

func (s *Server) agentDistResolveNames(names []string) (string, bool) {
	if s == nil || s.distDir == "" {
		return "", false
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(s.distDir, name)); err == nil {
			return name, true
		}
	}
	return "", false
}

// hostNeedsWin2012Agent reports whether fleet updates must ship the Go 1.20
// win2012 binary (modern Go ≥1.21 exits on Server 2012/R2 and Win8/8.1).
func hostNeedsWin2012Agent(h *Host) bool {
	if h == nil {
		return false
	}
	blob := strings.ToLower(h.OS + " " + h.Platform)
	if strings.Contains(blob, "2012") {
		return true
	}
	if strings.Contains(blob, "windows 8") {
		return true
	}
	if strings.Contains(blob, "build 9200") || strings.Contains(blob, "build 9600") {
		return true
	}
	return false
}

// agentDistCandidatesForHost returns download candidates. For legacy Windows
// hosts the list is ONLY the win2012 artifact (no modern PE fallback).
func agentDistCandidatesForHost(h *Host) []string {
	goos, goarch := hostGOOSArch(h)
	cands := agentDistCandidates(goos, goarch)
	if !hostNeedsWin2012Agent(h) || goos != "windows" || goarch != "amd64" {
		return cands
	}
	const win2012 = "aiops-agent-windows-amd64-win2012.exe"
	return []string{win2012}
}

func fileSHA256HexServer(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func normalizeAgentVer(v string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "v")
}

// isComparableAgentVer reports whether v looks like a release version we can order.
func isComparableAgentVer(v string) bool {
	v = normalizeAgentVer(v)
	if v == "" || v == "aiops" || v == "dev" {
		return false
	}
	return unicode.IsDigit(rune(v[0]))
}

// compareAgentVer returns -1 if a<b, 0 if equal, 1 if a>b (numeric dotted parts).
func compareAgentVer(a, b string) int {
	a, b = normalizeAgentVer(a), normalizeAgentVer(b)
	ap := strings.Split(a, ".")
	bp := strings.Split(b, ".")
	n := len(ap)
	if len(bp) > n {
		n = len(bp)
	}
	for i := 0; i < n; i++ {
		ai, bi := 0, 0
		if i < len(ap) {
			ai = verNumericPrefix(ap[i])
		}
		if i < len(bp) {
			bi = verNumericPrefix(bp[i])
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	// Equal numeric parts: a pre-release ranks below the plain release (semver
	// §11), so an agent sitting on v1.2.3-rc1 is still behind v1.2.3 — without
	// this it would compare equal and never receive the GA build.
	apre, bpre := agentVerPreRelease(a), agentVerPreRelease(b)
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	case apre < bpre:
		return -1
	default:
		return 1
	}
}

// agentVerPreRelease returns the semver pre-release tail ("rc1" for 1.2.3-rc1),
// or "" for a plain release.
func agentVerPreRelease(v string) string {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return v[i+1:]
	}
	return ""
}

func verNumericPrefix(s string) int {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}

// agentVersionBehind is true only when current is strictly older than target.
// Empty current (legacy agent) counts as behind. Uncomparable targets (AIOps/dev)
// never trigger updates. Newer agents are never treated as behind (no downgrade).
func agentVersionBehind(current, target string) bool {
	target = normalizeAgentVer(target)
	if !isComparableAgentVer(target) {
		return false
	}
	current = normalizeAgentVer(current)
	if current == "" {
		return true
	}
	if !isComparableAgentVer(current) {
		// Agent still reports "dev"/placeholder — push once to a real release.
		return true
	}
	return compareAgentVer(current, target) < 0
}

func hostSupportsAgentUpdateModule(h *Host) bool {
	// Agents that report agent_version were built with the self-update module.
	return h != nil && strings.TrimSpace(h.AgentVersion) != ""
}

// agentPublicBaseURL is the URL agents use to download /dl/. Prefer PublicURL;
// fall back to empty (caller must supply from HTTP request).
func (s *Server) agentPublicBaseURL() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(s.cfg.PublicURL()), "/")
}

func hostGOOSArch(h *Host) (goos, goarch string) {
	if h == nil {
		return "", ""
	}
	goos = strings.TrimSpace(h.OS)
	goarch = strings.TrimSpace(h.Arch)
	// Some agents put a pretty OS string in OS and leave Arch empty — also try Platform.
	if goos == "" {
		goos = strings.TrimSpace(h.Platform)
	}
	return normalizeGOOSArch(goos, goarch)
}
