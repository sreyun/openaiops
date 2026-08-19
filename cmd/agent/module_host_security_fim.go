package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	fimMaxFiles     = 80
	fimMaxHashBytes = 2 << 20 // 2 MiB
	fimMaxTextBytes = 64 << 10
	fimMaxDiffLines = 200
	fimMaxDiffBytes = 48 << 10
)

type hostSecFileInv struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
	Mtime  int64  `json:"mtime,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Kind   string `json:"kind,omitempty"` // config|exec|auth|other
}

type hostSecTextDiff struct {
	Path      string `json:"path"`
	OldSHA    string `json:"old_sha,omitempty"`
	NewSHA    string `json:"new_sha,omitempty"`
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

var fimSecretLineRe = regexp.MustCompile(`(?i)(password|secret|token|api[_-]?key|BEGIN\s+\w+\s+PRIVATE)`)

// fimStateDirHint is set from agent main (directory of state_file) so FIM text
// cache lands in a writable location instead of next to a read-only binary.
var fimStateDirHint string

func setFIMStateDir(stateFile string) {
	stateFile = strings.TrimSpace(stateFile)
	if stateFile == "" {
		fimStateDirHint = ""
		return
	}
	abs, err := filepath.Abs(stateFile)
	if err != nil {
		fimStateDirHint = filepath.Dir(stateFile)
		return
	}
	fimStateDirHint = filepath.Dir(abs)
}

func collectFIMInventory(enableDiff bool) (inv []hostSecFileInv, diffs []hostSecTextDiff) {
	paths := fimMonitorPaths()
	seen := map[string]bool{}
	cacheDir := ""
	if enableDiff {
		cacheDir = fimTextCacheDir()
	}

	for _, p := range paths {
		if len(inv) >= fimMaxFiles {
			break
		}
		p = filepath.Clean(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		h, ok := hashFileLimited(p, fimMaxHashBytes)
		if !ok {
			continue
		}
		fi, err := os.Stat(p)
		mtime := int64(0)
		if err == nil {
			mtime = fi.ModTime().Unix()
		}
		item := hostSecFileInv{
			Path: h.Path, SHA256: h.SHA256, Size: h.Size, Mode: h.Mode,
			Mtime: mtime, Kind: fimPathKind(p),
		}
		inv = append(inv, item)

		if !enableDiff || cacheDir == "" || !fimAllowContentDiff(p) || h.Size > fimMaxTextBytes {
			continue
		}
		if d, ok := fimMaybeTextDiff(cacheDir, p, h.SHA256); ok {
			diffs = append(diffs, d)
		}
	}
	return inv, diffs
}

func fimMonitorPaths() []string {
	var paths []string
	switch runtime.GOOS {
	case "windows":
		paths = append(paths,
			`C:\Windows\System32\drivers\etc\hosts`,
			`C:\Windows\System32\drivers\etc\hosts.ics`,
			`C:\Windows\System32\drivers\etc\lmhosts.sam`,
			`C:\Windows\System32\drivers\etc\networks`,
			`C:\Windows\System32\drivers\etc\protocol`,
			`C:\Windows\System32\drivers\etc\services`,
		)
		// Common autorun / profile hooks (best-effort; missing paths are skipped).
		if windir := os.Getenv("WINDIR"); windir != "" {
			paths = append(paths,
				filepath.Join(windir, "System32", "GroupPolicy", "Machine", "Scripts", "Startup"),
				filepath.Join(windir, "System32", "GroupPolicy", "Machine", "Scripts", "Shutdown"),
			)
		}
		// Startup / scheduled-task readable snippets (best-effort, capped later).
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			paths = append(paths, filepath.Join(appdata, "Microsoft", "Windows", "Start Menu", "Programs", "Startup"))
		}
		if prog := os.Getenv("ProgramData"); prog != "" {
			paths = append(paths, filepath.Join(prog, "Microsoft", "Windows", "Start Menu", "Programs", "StartUp"))
		}
	default:
		home, _ := os.UserHomeDir()
		base := []string{
			"/etc/passwd", "/etc/shadow", "/etc/group", "/etc/sudoers",
			"/etc/ssh/sshd_config", "/etc/crontab", "/etc/hosts", "/etc/resolv.conf",
			"/etc/rc.local", "/etc/nsswitch.conf",
		}
		paths = append(paths, base...)
		if home != "" {
			paths = append(paths, filepath.Join(home, ".ssh", "authorized_keys"))
		}
		// cron.d entries (depth 1)
		if entries, err := os.ReadDir("/etc/cron.d"); err == nil {
			n := 0
			for _, e := range entries {
				if e.IsDir() || n >= 20 {
					continue
				}
				paths = append(paths, filepath.Join("/etc/cron.d", e.Name()))
				n++
			}
		}
		// Sample executables under /usr/local/bin and /opt (cap)
		paths = append(paths, fimSampleExecs("/usr/local/bin", 15)...)
		paths = append(paths, fimSampleExecs("/opt", 10)...)
	}
	// Expand directories to contained files (Startup folders on Windows).
	var out []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				continue
			}
			n := 0
			for _, e := range entries {
				if e.IsDir() || n >= 10 {
					continue
				}
				out = append(out, filepath.Join(p, e.Name()))
				n++
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

func fimSampleExecs(dir string, limit int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if len(out) >= limit {
			break
		}
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		mode := fi.Mode()
		if runtime.GOOS != "windows" && mode&0o111 == 0 {
			continue
		}
		if fi.Size() > fimMaxHashBytes {
			continue
		}
		out = append(out, p)
	}
	return out
}

func fimPathKind(p string) string {
	base := strings.ToLower(filepath.Base(p))
	switch {
	case strings.Contains(base, "authorized_keys"), base == "shadow", base == "sudoers":
		return "auth"
	case strings.HasSuffix(base, ".exe"), strings.HasSuffix(base, ".bin"),
		(runtime.GOOS != "windows" && fileExecutable(p)):
		return "exec"
	case base == "sshd_config", base == "hosts", base == "crontab",
		base == "resolv.conf", base == "passwd", base == "group", base == "rc.local":
		return "config"
	default:
		if strings.Contains(p, "cron.d") || strings.Contains(strings.ToLower(p), "startup") {
			return "config"
		}
		return "other"
	}
}

func fileExecutable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	return fi.Mode()&0o111 != 0
}

func fimTextCacheDir() string {
	var candidates []string
	if fimStateDirHint != "" {
		candidates = append(candidates, filepath.Join(fimStateDirHint, "fim_text_cache"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "fim_text_cache"))
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidates = append(candidates, filepath.Join(wd, "fim_text_cache"))
	}
	if ud, err := os.UserCacheDir(); err == nil && ud != "" {
		candidates = append(candidates, filepath.Join(ud, "aiops-agent", "fim_text_cache"))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "aiops-agent-fim"))

	for _, d := range candidates {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o750); err != nil {
			continue
		}
		probe := filepath.Join(d, ".write_probe")
		if err := os.WriteFile(probe, []byte("1"), 0o600); err != nil {
			continue
		}
		_ = os.Remove(probe)
		return d
	}
	return filepath.Join(os.TempDir(), "aiops-agent-fim")
}

func fimCacheKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:16])
}

func fimIsTextual(raw []byte) bool {
	if len(raw) == 0 {
		return true
	}
	// Reject obvious binaries (NUL) and high control-byte density.
	nul, ctrl := 0, 0
	for _, b := range raw {
		if b == 0 {
			nul++
		} else if b < 0x09 || (b > 0x0d && b < 0x20) {
			ctrl++
		}
	}
	if nul > 0 {
		return false
	}
	return ctrl*10 <= len(raw) // ≤10% control bytes
}

// fimSeedTextCache snapshots a content-audit file the first time it is seen, so
// the NEXT modification can be diffed. Existing snapshots are left alone —
// overwriting them here would erase the "before" side of the current change.
func fimSeedTextCache(cacheDir, path, sha string) {
	if cacheDir == "" || sha == "" {
		return
	}
	metaPath := filepath.Join(cacheDir, fimCacheKey(path)+".sha")
	if _, err := os.Stat(metaPath); err == nil {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil || int64(len(raw)) > fimMaxTextBytes || !fimIsTextual(raw) {
		return
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return
	}
	_ = os.WriteFile(metaPath, []byte(sha+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(cacheDir, fimCacheKey(path)+".txt"), raw, 0o600)
}

func fimMaybeTextDiff(cacheDir, path, newSHA string) (hostSecTextDiff, bool) {
	raw, err := os.ReadFile(path)
	if err != nil || int64(len(raw)) > fimMaxTextBytes || !fimIsTextual(raw) {
		return hostSecTextDiff{}, false
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return hostSecTextDiff{}, false
	}
	key := fimCacheKey(path)
	metaPath := filepath.Join(cacheDir, key+".sha")
	bodyPath := filepath.Join(cacheDir, key+".txt")
	oldSHA := ""
	if b, err := os.ReadFile(metaPath); err == nil {
		oldSHA = strings.TrimSpace(string(b))
	}
	// Always refresh cache after read so next scan has a baseline snapshot.
	defer func() {
		_ = os.WriteFile(metaPath, []byte(newSHA+"\n"), 0o600)
		_ = os.WriteFile(bodyPath, raw, 0o600)
	}()
	if oldSHA == "" || strings.EqualFold(oldSHA, newSHA) {
		return hostSecTextDiff{}, false
	}
	oldBody, _ := os.ReadFile(bodyPath)
	if !fimIsTextual(oldBody) {
		return hostSecTextDiff{}, false
	}
	diffText, trunc := fimUnifiedDiff(path, string(oldBody), string(raw))
	diffText = fimRedactDiff(diffText)
	if len(diffText) > fimMaxDiffBytes {
		diffText = diffText[:fimMaxDiffBytes]
		trunc = true
	}
	if strings.TrimSpace(diffText) == "" {
		return hostSecTextDiff{}, false
	}
	return hostSecTextDiff{
		Path: path, OldSHA: oldSHA, NewSHA: newSHA,
		Diff: diffText, Truncated: trunc,
	}, true
}

func fimUnifiedDiff(path, oldS, newS string) (string, bool) {
	oldLines := strings.Split(strings.ReplaceAll(oldS, "\r\n", "\n"), "\n")
	newLines := strings.Split(strings.ReplaceAll(newS, "\r\n", "\n"), "\n")
	// Strip trailing empty line artifact from Split on empty.
	if len(oldLines) == 1 && oldLines[0] == "" {
		oldLines = nil
	}
	if len(newLines) == 1 && newLines[0] == "" {
		newLines = nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", filepath.Base(path), filepath.Base(path))
	// Simple LCS-free line diff: mark removed then added (good enough for config files).
	// Use Myers-lite: walk with greedy match.
	i, j := 0, 0
	linesOut := 0
	truncated := false
	for i < len(oldLines) || j < len(newLines) {
		if linesOut >= fimMaxDiffLines || b.Len() >= fimMaxDiffBytes {
			truncated = true
			break
		}
		if i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j] {
			fmt.Fprintf(&b, " %s\n", oldLines[i])
			i++
			j++
			linesOut++
			continue
		}
		// Look ahead for resync within 8 lines.
		resync := -1
		for di := 0; di <= 8 && i+di < len(oldLines); di++ {
			for dj := 0; dj <= 8 && j+dj < len(newLines); dj++ {
				if oldLines[i+di] == newLines[j+dj] {
					resync = di*100 + dj
					goto found
				}
			}
		}
	found:
		if resync >= 0 {
			di, dj := resync/100, resync%100
			for k := 0; k < di && i < len(oldLines); k++ {
				if linesOut >= fimMaxDiffLines {
					truncated = true
					break
				}
				fmt.Fprintf(&b, "-%s\n", oldLines[i])
				i++
				linesOut++
			}
			for k := 0; k < dj && j < len(newLines); k++ {
				if linesOut >= fimMaxDiffLines {
					truncated = true
					break
				}
				fmt.Fprintf(&b, "+%s\n", newLines[j])
				j++
				linesOut++
			}
			continue
		}
		if i < len(oldLines) {
			fmt.Fprintf(&b, "-%s\n", oldLines[i])
			i++
			linesOut++
			continue
		}
		if j < len(newLines) {
			fmt.Fprintf(&b, "+%s\n", newLines[j])
			j++
			linesOut++
		}
	}
	return b.String(), truncated
}

func fimRedactDiff(diff string) string {
	var b strings.Builder
	for _, ln := range strings.Split(diff, "\n") {
		if len(ln) > 0 && (ln[0] == '+' || ln[0] == '-' || ln[0] == ' ') {
			body := ln[1:]
			if fimSecretLineRe.MatchString(body) {
				b.WriteByte(ln[0])
				b.WriteString("***REDACTED***\n")
				continue
			}
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
