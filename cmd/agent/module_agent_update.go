package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// agentUpdateRunMu serializes self-update attempts inside this process.
//
// 多服务端下这把锁是必须的：每个面板各自持有 in-flight 冷却表，两块面板完全可能在
// 同一时刻下发 agent_update；exec 通道又是「每个 target 一条 goroutine」，于是两次
// 升级会并发跑同一条 download → verify → replace 流水线。共用暂存文件时，A 校验完
// SHA-256 到 rename 之间的窗口里 B 正在把同一个文件截断重写，换上去的就是半截二进制
// （只剩 .bak + 看门狗兜底）；Windows 侧还会互相覆盖 helper 脚本、注销对方的计划任务。
//
// 用 TryLock 而不是 Lock：第二块面板应当立刻被告知「已有升级在跑」，而不是排在一次
// 以重启收尾的替换后面——那条命令醒来时进程早已不是它下发的那个了。
var agentUpdateRunMu sync.Mutex

// moduleAgentUpdate downloads the matching platform binary from the server /dl/
// tree, verifies SHA-256, stages it beside the running executable, then asks the
// OS service manager to restart so the new binary is picked up.
//
// Args:
//
//	server   – base URL (required), e.g. http://mon.example:8529
//	force    – "1" to replace even when versions match
//	version  – expected target version (optional; skip when equal unless force)
//	rollback – "1" to restore aiops-agent(.exe).bak instead of downloading
func moduleAgentUpdate(args map[string]string, allowedBases []string) ([]byte, int) {
	if !agentUpdateRunMu.TryLock() {
		// Keep the "agent_update:" prefix — the server uses it to tell a real module
		// failure from "module unknown" (which would trigger the legacy script path).
		return []byte("agent_update: another self-update is already running on this host (skipped)"), 1
	}
	defer agentUpdateRunMu.Unlock()

	if truthyArg(args["rollback"]) {
		return moduleAgentRollback()
	}
	server := strings.TrimRight(strings.TrimSpace(args["server"]), "/")
	if server == "" && len(allowedBases) > 0 {
		server = allowedBases[0]
	}
	if server == "" {
		return []byte("agent_update: missing server URL"), 1
	}
	if err := validateUpdateServerURL(server, allowedBases); err != nil {
		return []byte("agent_update: " + err.Error()), 1
	}
	if strings.HasPrefix(strings.ToLower(server), "http://") {
		// Not fatal (plenty of air-gapped LAN deployments run plaintext), but the
		// operator should know the binary + its checksum share one unauthenticated
		// channel. Same reasoning applies to tls_skip_verify: see docs/ci-gate.md.
		fmt.Fprintf(os.Stderr, "agent_update: WARNING downloading over plaintext http from %s — binary and checksum share an unauthenticated channel\n", server)
	}
	force := truthyArg(args["force"])
	wantVer := strings.TrimSpace(args["version"])
	if !force && wantVer != "" && normalizeAgentVer(wantVer) == normalizeAgentVer(agentVersion()) {
		return []byte(fmt.Sprintf("agent_update: already at %s (skip)", agentVersion())), 0
	}

	binCandidates := agentUpdateBinCandidates(runtime.GOOS, runtime.GOARCH, args["bin"])
	if len(binCandidates) == 0 {
		return []byte("agent_update: unsupported platform"), 1
	}
	exe, err := os.Executable()
	if err != nil {
		return []byte("agent_update: resolve executable: " + err.Error()), 1
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	sweepStaleAgentStaging(dir)
	staging := agentStagingPath(dir, "new")
	bak := exe + ".bak"

	client := newUpdateHTTPClient(10 * time.Minute)
	var binName string
	var expected string
	var lastDL error
	for _, cand := range binCandidates {
		dlURL := server + "/dl/" + cand
		sumURL := dlURL + ".sha256"
		if err := downloadFileHTTP(client, dlURL, staging); err != nil {
			lastDL = err
			_ = os.Remove(staging)
			continue
		}
		sum, err := fetchRemoteSHA256(client, sumURL)
		if err != nil {
			lastDL = err
			_ = os.Remove(staging)
			continue
		}
		actual, err := fileSHA256Hex(staging)
		if err != nil {
			lastDL = err
			_ = os.Remove(staging)
			continue
		}
		if !strings.EqualFold(sum, actual) {
			lastDL = fmt.Errorf("SHA-256 mismatch for %s (want %s got %s)", cand, sum, actual)
			_ = os.Remove(staging)
			continue
		}
		_ = os.Chmod(staging, 0o755)
		if err := peCompatibleWithHost(staging); err != nil {
			lastDL = err
			_ = os.Remove(staging)
			continue
		}
		if err := agentBinaryVersionProbe(staging); err != nil {
			lastDL = fmt.Errorf("%s not runnable: %w", cand, err)
			_ = os.Remove(staging)
			continue
		}
		binName, expected = cand, actual
		break
	}
	if binName == "" {
		_ = os.Remove(staging)
		if lastDL == nil {
			lastDL = fmt.Errorf("no candidate binary")
		}
		return []byte("agent_update: download failed: " + lastDL.Error()), 1
	}

	// Backup current binary. On Windows the running PE is often locked for
	// reading — leave .bak to the restart helper and do not delete an existing
	// good backup before a successful copy.
	if runtime.GOOS != "windows" {
		tmpBak := bak + ".tmp"
		if err := copyFile(exe, tmpBak); err != nil {
			fmt.Fprintf(os.Stderr, "agent_update: backup warning: %v\n", err)
			_ = os.Remove(tmpBak)
		} else {
			_ = os.Remove(bak)
			if err := os.Rename(tmpBak, bak); err != nil {
				_ = copyFile(tmpBak, bak)
				_ = os.Remove(tmpBak)
			}
		}
	}

	cfgPath := resolveAgentConfigBesideExe(dir)
	if err := agentReplaceAndRestart(exe, staging, cfgPath); err != nil {
		_ = os.Remove(staging)
		return []byte("agent_update: " + err.Error()), 1
	}
	msg := fmt.Sprintf("agent_update: staged %s sha256=%s from=%s → restart scheduled (was %s",
		binName, expected[:12], server, agentVersion())
	if wantVer != "" {
		msg += fmt.Sprintf(", target %s", wantVer)
	}
	msg += ")"
	if runtime.GOOS == "windows" {
		msg += " [windows: scheduled-task/breakaway helper]"
	}
	return []byte(msg), 0
}

func moduleAgentRollback() ([]byte, int) {
	exe, err := os.Executable()
	if err != nil {
		return []byte("agent_update rollback: " + err.Error()), 1
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	bak := exe + ".bak"
	if st, err := os.Stat(bak); err != nil || st.IsDir() {
		return []byte("agent_update rollback: no .bak beside current binary"), 1
	}
	staging := agentStagingPath(filepath.Dir(exe), "rollback")
	if err := copyFile(bak, staging); err != nil {
		return []byte("agent_update rollback: " + err.Error()), 1
	}
	_ = os.Chmod(staging, 0o755)
	cfgPath := resolveAgentConfigBesideExe(filepath.Dir(exe))
	if err := agentReplaceAndRestart(exe, staging, cfgPath); err != nil {
		_ = os.Remove(staging)
		return []byte("agent_update rollback: " + err.Error()), 1
	}
	return []byte("agent_update: rollback to .bak scheduled"), 0
}

// resolveAgentConfigBesideExe returns an absolute config path next to the agent
// binary when present. Update restart helpers must pass this explicitly —
// Windows services start with CWD=System32, and a bare relaunch without
// --config falls back to localhost and breaks terminal/desktop.
func resolveAgentConfigBesideExe(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	for _, name := range []string{"config.yaml", "config.yml", "config.json"} {
		p := filepath.Join(dir, name)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return ""
}

// agentStagingPath builds a per-process staging name (".aiops-agent.new.<pid>").
// agentUpdateRunMu already serializes goroutines inside one agent; the pid keeps a
// SECOND agent process (a manual foreground run alongside the service, or the
// installer re-launching while a self-update is in flight) from truncating the
// file this process has already checksummed.
func agentStagingPath(dir, kind string) string {
	return filepath.Join(dir, fmt.Sprintf(".aiops-agent.%s.%d%s", kind, os.Getpid(), exeSuffix()))
}

// sweepStaleAgentStaging removes staging leftovers from runs that died before
// their own cleanup (killed mid-download, host rebooted during a swap). Without
// this, per-pid names would accumulate one dead multi-MB file per failed attempt
// next to the binary. Anything younger than an hour may belong to a live run.
func sweepStaleAgentStaging(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-1 * time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, ".aiops-agent.new.") &&
			!strings.HasPrefix(name, ".aiops-agent.rollback.") &&
			!strings.HasPrefix(name, ".aiops-agent.replace.") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

func truthyArg(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func normalizeAgentVer(v string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "v")
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// agentDistBinaryName maps GOOS/GOARCH to the /dl/ artifact name served by the server.
func agentDistBinaryName(goos, goarch string) (string, error) {
	cands := agentUpdateBinCandidates(goos, goarch, "")
	if len(cands) == 0 {
		return "", fmt.Errorf("unsupported platform %s/%s", goos, goarch)
	}
	return cands[0], nil
}

// agentUpdateBinCandidates lists /dl names to try (server may ship either Windows alias).
// On Server 2012 / Win8 kernels ONLY the Go 1.20 win2012 artifact is offered —
// never fall back to modern aiops-agent.exe (it exits immediately on NT 6.2/6.3).
func agentUpdateBinCandidates(goos, goarch, preferred string) []string {
	goos = strings.ToLower(strings.TrimSpace(goos))
	goarch = strings.ToLower(strings.TrimSpace(goarch))
	switch goarch {
	case "x86_64", "x64", "":
		goarch = "amd64"
	case "aarch64":
		goarch = "arm64"
	case "loongarch64":
		goarch = "loong64"
	case "i386", "i686", "x86":
		goarch = "386"
	case "armv7l", "armv7", "armv6l", "armhf":
		goarch = "arm"
	}
	const win2012 = "aiops-agent-windows-amd64-win2012.exe"
	legacyWin := goos == "windows" && goarch == "amd64" && windowsNeedsLegacyAgentBuild()

	var primary string
	switch goos {
	case "linux":
		switch goarch {
		case "amd64", "arm64", "loong64", "riscv64", "386", "arm":
			primary = "aiops-agent-linux-" + goarch
		}
	case "darwin":
		switch goarch {
		case "amd64":
			primary = "aiops-agent-darwin-amd64"
		case "arm64":
			primary = "aiops-agent-darwin-arm64"
		}
	case "windows":
		switch goarch {
		case "amd64":
			if legacyWin {
				primary = win2012
			} else {
				primary = "aiops-agent.exe"
			}
		case "arm64":
			primary = "aiops-agent-windows-arm64.exe"
		}
	}
	if primary == "" {
		return nil
	}

	pref := strings.TrimSpace(preferred)
	if pref != "" {
		pref = filepath.Base(pref)
	}
	if pref == "." || strings.Contains(pref, "..") || strings.ContainsAny(pref, `/\`) {
		pref = ""
	}

	// Legacy Windows: ignore preferred modern PE names — they cannot run here.
	if legacyWin {
		if pref != "" && pref != win2012 {
			pref = ""
		}
		return []string{win2012}
	}

	// The server guesses "legacy Windows" from the reported OS string ("...2012...")
	// while we know the actual kernel. On a modern kernel its win2012 hint would
	// pin us to the Go 1.20 build line — drop it here; the alias loop below still
	// keeps win2012 as the last-resort candidate.
	if pref == win2012 && primary != win2012 {
		pref = ""
	}

	out := make([]string, 0, 4)
	if pref != "" {
		out = append(out, pref)
	}
	if pref != primary {
		out = append(out, primary)
	}
	if goos == "windows" && goarch == "amd64" {
		for _, alt := range []string{
			"aiops-agent.exe",
			"aiops-agent-windows-amd64.exe",
			win2012, // last resort on modern Windows (runs, but prefer current build)
		} {
			dup := false
			for _, e := range out {
				if e == alt {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, alt)
			}
		}
	}
	return out
}

// agentBinaryVersionProbe runs `<bin> --version` to ensure the staged binary
// actually starts on this host before it replaces a working agent (catches
// Go≥1.21 PEs on Server 2012, and wrong-arch / truncated downloads everywhere).
// AIOPS_UPDATE_PROBE=1 forces the callee onto its silent fast path.
func agentBinaryVersionProbe(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = append(os.Environ(), "AIOPS_UPDATE_PROBE=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	if runtime.GOOS == "windows" || probeFailureIsFatal(err) {
		return fmt.Errorf("%s", msg)
	}
	// Hardened hosts (SELinux/AppArmor/noexec staging dir) can deny exec of a
	// freshly written file even though the binary itself is fine. Do not block the
	// upgrade on an inconclusive probe — the restart watchdog plus .bak rollback
	// remains the real safety net on Unix.
	fmt.Fprintf(os.Stderr, "agent_update: version probe inconclusive (%s); continuing\n", msg)
	return nil
}

// probeFailureIsFatal is true only when the staged binary demonstrably cannot
// run here: a wrong-arch / truncated download (ENOEXEC), or a clean non-zero
// exit from a process that did start.
func probeFailureIsFatal(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "exec format error") ||
		strings.Contains(low, "cannot execute binary file") ||
		strings.Contains(low, "bad executable")
}

func validateUpdateServerURL(server string, allowedBases []string) error {
	u, err := url.Parse(server)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid server URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("server URL scheme must be http or https")
	}
	if len(allowedBases) == 0 {
		// Standalone module tests / unexpected path: still block obvious SSRF targets.
		if host, _, err := net.SplitHostPort(u.Host); err == nil {
			if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
				// loopback is OK for local ops; link-local is not a typical monitor server
				if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
					return fmt.Errorf("server URL host not allowed")
				}
			}
		}
		return nil
	}
	want := strings.ToLower(strings.TrimRight(server, "/"))
	for _, b := range allowedBases {
		base := strings.ToLower(strings.TrimRight(strings.TrimSpace(b), "/"))
		if base == "" {
			continue
		}
		if want == base {
			return nil
		}
		// Allow same host with/without trailing path differences.
		bu, err := url.Parse(base)
		if err != nil {
			continue
		}
		if strings.EqualFold(u.Host, bu.Host) {
			// http↔https after redirect / TLS upgrade: still the same monitor.
			// A downgrade is not: the update payload is a root-privileged binary
			// whose only integrity proof (the .sha256) travels the same channel,
			// so plaintext would let anyone on-path own the fleet. Upgrades to
			// https are always fine.
			if scheme == "http" && strings.EqualFold(bu.Scheme, "https") {
				return fmt.Errorf("refusing plaintext http update from %s (configured target is https)", u.Host)
			}
			return nil
		}
	}
	return fmt.Errorf("server URL not in configured report targets")
}

func newUpdateHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     reportTransport,
		CheckRedirect: preserveAgentRedirect,
	}
}

func downloadFileHTTP(client *http.Client, rawURL, dest string) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	_ = os.Remove(dest)
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Sync()
}

func fetchRemoteSHA256(client *http.Client, rawURL string) (string, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty checksum body")
	}
	sum := strings.ToLower(fields[0])
	if len(sum) != 64 {
		return "", fmt.Errorf("invalid sha256 length")
	}
	for _, c := range sum {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("invalid sha256 hex")
		}
	}
	return sum, nil
}

func fileSHA256Hex(path string) (string, error) {
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	_ = os.Remove(dst)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
