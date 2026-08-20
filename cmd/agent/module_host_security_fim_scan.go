package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Full-scope FIM: walk every directory and record add/modify/delete of any file
// using metadata only (size / mtime / mode). File CONTENT is never captured for
// the broad scope — only paths on the content-audit whitelist are hashed and
// diffed, so an operator gets fleet-wide change visibility without the agent
// shipping arbitrary file bodies off the host.
//
// The baseline lives on the AGENT (a gzip line file next to agent_state.json),
// not on the server: a whole-filesystem inventory is millions of entries and
// cannot be round-tripped through the scan report. The agent therefore computes
// the delta locally and reports only the changes.

const (
	fimDefaultMaxFiles   = 150000
	fimDefaultMaxChanges = 500
	fimDefaultBudget     = 90 * time.Second
	fimBaselineFile      = "fim_baseline.gz"
	// v2 stores mtime in nanoseconds; a v1 file is discarded and re-baselined
	// rather than producing a wave of bogus "mtime changed" deltas.
	fimBaselineHeader = "#aiops-fim v2"
)

// fimEntry is one baseline record. Mtime is UnixNano so same-second rewrites of
// equal-length files are still detected. SHA is only set for content-audit paths.
type fimEntry struct {
	Size  int64
	Mtime int64
	Mode  string
	SHA   string
}

func (e fimEntry) mtimeSec() int64 { return e.Mtime / int64(time.Second) }

// hostSecFileChange is an agent-computed FIM delta.
type hostSecFileChange struct {
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

// hostSecFIMStats describes the walk so the UI can explain coverage honestly.
type hostSecFIMStats struct {
	Mode         string   `json:"mode"` // full|sensitive
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

type fimOptions struct {
	Scope        string
	Roots        []string
	Excludes     []string
	ContentPaths []string
	MaxFiles     int
	MaxChanges   int
	Budget       time.Duration
	ContentDiff  bool
}

// fimParseOptions builds walk options from module args pushed by the server.
func fimParseOptions(args map[string]string) fimOptions {
	o := fimOptions{
		Scope:       strings.ToLower(strings.TrimSpace(args["fim_scope"])),
		MaxFiles:    fimDefaultMaxFiles,
		MaxChanges:  fimDefaultMaxChanges,
		Budget:      fimDefaultBudget,
		ContentDiff: true,
	}
	if o.Scope != "sensitive" {
		o.Scope = "full"
	}
	if v := strings.ToLower(strings.TrimSpace(args["fim_diff"])); v == "0" || v == "false" || v == "off" {
		o.ContentDiff = false
	}
	o.Roots = fimSplitList(args["fim_roots"])
	o.Excludes = fimSplitList(args["fim_excludes"])
	o.ContentPaths = fimSplitList(args["fim_content_paths"])
	if n, err := strconv.Atoi(strings.TrimSpace(args["fim_max_files"])); err == nil && n > 0 {
		o.MaxFiles = min(n, 2000000)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(args["fim_max_changes"])); err == nil && n > 0 {
		o.MaxChanges = min(n, 5000)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(args["fim_budget_sec"])); err == nil && n > 0 {
		o.Budget = time.Duration(min(n, 900)) * time.Second
	}
	return o
}

func fimSplitList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// fimNormPath is the canonical on-wire path form: forward slashes, no trailing slash.
func fimNormPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

func fimMatchKey(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// fimDefaultRoots returns every local filesystem root worth walking.
func fimDefaultRoots() []string {
	if runtime.GOOS != "windows" {
		return []string{"/"}
	}
	var roots []string
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + ":\\"
		fi, err := os.Stat(root)
		if err != nil || !fi.IsDir() {
			continue
		}
		roots = append(roots, root)
	}
	if len(roots) == 0 {
		if sd := os.Getenv("SystemDrive"); sd != "" {
			roots = append(roots, sd+"\\")
		} else {
			roots = append(roots, "C:\\")
		}
	}
	return roots
}

// fimDefaultExcludes skips pseudo filesystems, package/build caches and
// high-churn spool dirs. Without these a full walk is both slow and pure noise.
func fimDefaultExcludes() []string {
	common := []string{
		"/proc", "/sys", "/dev", "/run", "/lost+found",
		"/var/lib/docker", "/var/lib/containerd", "/var/lib/kubelet", "/var/lib/libvirt",
		"/var/cache", "/var/spool", "/var/log", "/var/tmp/systemd-private",
		"/snap", "/var/lib/snapd", "/var/lib/flatpak",
		"/swapfile", "/swap.img",
	}
	switch runtime.GOOS {
	case "darwin":
		return append(common,
			"/System/Volumes/Data/private/var/folders", "/private/var/folders",
			"/private/var/vm", "/Volumes", "/.Spotlight-V100", "/.fseventsd",
			"/Library/Caches", "/System/Library/Caches",
		)
	case "windows":
		var out []string
		add := func(p string) {
			if strings.TrimSpace(p) != "" {
				out = append(out, p)
			}
		}
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		add(filepath.Join(win, "WinSxS"))
		add(filepath.Join(win, "Temp"))
		add(filepath.Join(win, "SoftwareDistribution"))
		add(filepath.Join(win, "Installer"))
		add(filepath.Join(win, "servicing"))
		add(filepath.Join(win, "Prefetch"))
		add(filepath.Join(win, "Logs"))
		add(filepath.Join(win, "CSC"))
		if pd := os.Getenv("ProgramData"); pd != "" {
			add(filepath.Join(pd, "Package Cache"))
			add(filepath.Join(pd, "Microsoft", "Windows Defender"))
			add(filepath.Join(pd, "Microsoft", "Search"))
			add(filepath.Join(pd, "USOPrivate"))
			add(filepath.Join(pd, "USOShared"))
		}
		sd := os.Getenv("SystemDrive")
		if sd == "" {
			sd = "C:"
		}
		add(sd + `\$Recycle.Bin`)
		add(sd + `\System Volume Information`)
		add(sd + `\pagefile.sys`)
		add(sd + `\hiberfil.sys`)
		add(sd + `\swapfile.sys`)
		return out
	default:
		return common
	}
}

// fimDefaultNameExcludes drops dependency/build trees anywhere in the tree.
func fimDefaultNameExcludes() []string {
	return []string{
		"node_modules", ".git", ".svn", ".hg", "__pycache__",
		".cache", ".npm", ".yarn", ".pnpm-store", ".gradle", ".m2",
		".terraform", ".venv", "site-packages", ".pytest_cache", ".mypy_cache",
	}
}

// fimRemoteMountExcludes adds network / virtual mount points so a full walk
// never traverses NFS/CIFS/overlay trees (slow, and not this host's state).
func fimRemoteMountExcludes() []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	raw, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil
	}
	skip := map[string]bool{
		"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "fuse": true,
		"fuseblk": true, "fuse.sshfs": true, "fuse.glusterfs": true, "ceph": true,
		"overlay": true, "squashfs": true, "tmpfs": true, "devtmpfs": true,
		"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "debugfs": true,
		"tracefs": true, "configfs": true, "securityfs": true, "pstore": true,
		"bpf": true, "mqueue": true, "hugetlbfs": true, "autofs": true, "binfmt_misc": true,
		"rpc_pipefs": true, "nsfs": true, "efivarfs": true,
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		mount, fstype := f[1], f[2]
		if mount == "/" {
			continue
		}
		if skip[fstype] || strings.HasPrefix(fstype, "fuse.") {
			out = append(out, unescapeMountPath(mount))
		}
	}
	return out
}

// unescapeMountPath decodes the octal escapes /proc/mounts uses for spaces etc.
func unescapeMountPath(p string) string {
	if !strings.Contains(p, `\`) {
		return p
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+3 < len(p) {
			if v, err := strconv.ParseUint(p[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

// fimExcluder decides whether a path is out of scope.
type fimExcluder struct {
	// prefixes 是自动排除项（默认表 + tmpfs/网络挂载）。
	prefixes []string
	// userPrefixes 是运维自己写的排除项，优先级最高，任何情况下都生效。
	userPrefixes []string
	// allow 是运维点名要扫的子树；落在里面的路径不受 prefixes 影响。
	allow []string
	names map[string]bool
}

// newFIMExcluder builds the skip set.
//
// `explicitRoots` 是运维**点名要扫**的目录（fimOptions.Roots）。自动排除项对它们不生效：
// 默认排除表和"跳过 tmpfs/overlay/网络挂载"这类规则，是给默认整盘扫（root = "/"）兜底的
// ——一个 ramdisk 或容器 overlay 目录不值得进基线。但运维显式写下这个路径时，同一条规则
// 会让扫描**一个文件都不走且不报错**，界面上只剩一句"0 个文件"，没人能从中看出是被
// 挂载类型挡掉的。点名即授权：这时以运维的配置为准。
//
// `extra`（运维自己写的排除项）永远生效——那是他明确要排除的东西。
func newFIMExcluder(extra []string, explicitRoots []string) *fimExcluder {
	e := &fimExcluder{names: map[string]bool{}}
	for _, n := range fimDefaultNameExcludes() {
		e.names[strings.ToLower(n)] = true
	}
	// 只有"点名的子树"才豁免自动排除；root 是 "/" 等于整盘扫，豁免它会把
	// /proc、/sys 一起拖进来。
	for _, r := range explicitRoots {
		key := fimMatchKey(fimNormPath(strings.TrimSpace(r)))
		if key == "" || key == "/" {
			continue
		}
		e.allow = append(e.allow, key)
	}
	add := func(list []string, dst *[]string) {
		for _, p := range list {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// A bare name (no separator) excludes that directory name anywhere.
			if !strings.ContainsAny(p, `/\`) {
				e.names[strings.ToLower(p)] = true
				continue
			}
			*dst = append(*dst, fimMatchKey(fimNormPath(p)))
		}
	}
	add(fimDefaultExcludes(), &e.prefixes)
	add(fimRemoteMountExcludes(), &e.prefixes)
	add(extra, &e.userPrefixes)
	sort.Strings(e.prefixes)
	sort.Strings(e.userPrefixes)
	sort.Strings(e.allow)
	return e
}

func underAny(key string, list []string) bool {
	for _, p := range list {
		if key == p || strings.HasPrefix(key, p+"/") {
			return true
		}
	}
	return false
}

func (e *fimExcluder) skipName(name string) bool {
	return e.names[strings.ToLower(name)]
}

func (e *fimExcluder) skipPath(norm string) bool {
	key := fimMatchKey(norm)
	// 运维自己写的排除项最优先——那是他明确要排除的东西。
	if underAny(key, e.userPrefixes) {
		return true
	}
	// 点名要扫的子树豁免自动排除项（否则一个 tmpfs 上的目录会被静默跳成 0 个文件）。
	if underAny(key, e.allow) {
		return false
	}
	return underAny(key, e.prefixes)
}

// --- content-audit whitelist ---

// fimDefaultContentPatterns lists paths whose CONTENT changes may be diffed.
// Everything else is metadata-only, by design. Both OS families are listed
// unconditionally — the shapes never collide, and keeping the set
// platform-independent makes the policy testable anywhere.
func fimDefaultContentPatterns() []string {
	return []string{
		"C:/Windows/System32/drivers/etc/*",
		"*/Start Menu/Programs/Startup/*",
		"*/Start Menu/Programs/StartUp/*",
		"C:/Windows/System32/GroupPolicy/*",
		"C:/Windows/System32/GroupPolicy/*/*",
		"C:/Windows/System32/GroupPolicy/*/*/*",
		"C:/Windows/System32/GroupPolicy/*/*/*/*",
		"/etc/passwd", "/etc/group", "/etc/sudoers", "/etc/sudoers.d/*",
		"/etc/ssh/sshd_config", "/etc/ssh/sshd_config.d/*", "/etc/ssh/ssh_config",
		"/etc/hosts", "/etc/hosts.allow", "/etc/hosts.deny",
		"/etc/resolv.conf", "/etc/nsswitch.conf", "/etc/fstab", "/etc/rc.local",
		"/etc/crontab", "/etc/cron.d/*", "/etc/cron.daily/*", "/etc/cron.hourly/*",
		"/etc/cron.weekly/*", "/etc/cron.monthly/*",
		"/etc/pam.d/*", "/etc/security/*.conf", "/etc/login.defs",
		"/etc/sysctl.conf", "/etc/sysctl.d/*",
		"/etc/profile", "/etc/profile.d/*", "/etc/environment",
		"/etc/bashrc", "/etc/bash.bashrc",
		"/etc/systemd/system/*.service", "/etc/systemd/system/*.timer",
		"/etc/nginx/*.conf", "/etc/nginx/conf.d/*", "/etc/nginx/sites-enabled/*",
		"/etc/httpd/conf/*", "/etc/apache2/*.conf", "/etc/apache2/sites-enabled/*",
	}
}

// fimContentDenied blocks secrets from ever being diffed, whatever the whitelist says.
func fimContentDenied(norm string) bool {
	base := strings.ToLower(fimBase(norm))
	lower := strings.ToLower(norm)
	switch base {
	case "shadow", "gshadow", "shadow-", "gshadow-", ".env", "authorized_keys", "known_hosts":
		return true
	}
	for _, suf := range []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".ovpn"} {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	for _, frag := range []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "private", "secret", "credential"} {
		if strings.Contains(base, frag) {
			return true
		}
	}
	return strings.Contains(lower, "/.ssh/") || strings.Contains(lower, "/ssl/private/")
}

func fimBase(norm string) string {
	if i := strings.LastIndex(norm, "/"); i >= 0 && i+1 < len(norm) {
		return norm[i+1:]
	}
	return norm
}

// fimContentAllowed reports whether norm may be content-diffed under patterns.
func fimContentAllowed(norm string, patterns []string) bool {
	if fimContentDenied(norm) {
		return false
	}
	key := fimMatchKey(norm)
	for _, pat := range patterns {
		p := fimMatchKey(fimNormPath(pat))
		if p == "" {
			continue
		}
		if p == key {
			return true
		}
		// path.Match keeps '/'-only separator semantics on every host OS
		// (filepath.Match would treat '\' as an escape on Windows).
		if ok, err := path.Match(p, key); err == nil && ok {
			return true
		}
		// A directory pattern covers everything under it.
		if strings.HasSuffix(p, "/*") && strings.HasPrefix(key, strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

func fimEffectiveContentPatterns(extra []string) []string {
	out := append([]string(nil), fimDefaultContentPatterns()...)
	for _, p := range extra {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// fimAllowContentDiff keeps the legacy default-policy entry point (sensitive scope).
func fimAllowContentDiff(p string) bool {
	return fimContentAllowed(fimNormPath(p), fimDefaultContentPatterns())
}

// --- baseline persistence ---

func fimDataDir() string { return fimTextCacheDir() }

func fimBaselinePath() string {
	return filepath.Join(fimDataDir(), fimBaselineFile)
}

func fimLoadBaseline(path string) (map[string]fimEntry, bool) {
	f, err := os.Open(path)
	if err != nil {
		return map[string]fimEntry{}, false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return map[string]fimEntry{}, false
	}
	defer zr.Close()

	out := make(map[string]fimEntry, 4096)
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if strings.TrimSpace(line) != fimBaselineHeader {
				return map[string]fimEntry{}, false
			}
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		mtime, _ := strconv.ParseInt(f[2], 10, 64)
		e := fimEntry{Size: size, Mtime: mtime, Mode: f[3]}
		if len(f) >= 5 {
			e.SHA = f[4]
		}
		out[f[0]] = e
	}
	if err := sc.Err(); err != nil {
		return map[string]fimEntry{}, false
	}
	return out, true
}

func fimSaveBaseline(path string, cur map[string]fimEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	bw := bufio.NewWriterSize(zw, 256<<10)
	writeErr := func() error {
		if _, err := bw.WriteString(fimBaselineHeader + "\n"); err != nil {
			return err
		}
		for p, e := range cur {
			if _, err := fmt.Fprintf(bw, "%s\t%d\t%d\t%s\t%s\n", p, e.Size, e.Mtime, e.Mode, e.SHA); err != nil {
				return err
			}
		}
		return bw.Flush()
	}()
	if writeErr == nil {
		writeErr = zw.Close()
	} else {
		_ = zw.Close()
	}
	if cerr := f.Close(); writeErr == nil {
		writeErr = cerr
	}
	if writeErr != nil {
		_ = os.Remove(tmp)
		return writeErr
	}
	return os.Rename(tmp, path)
}

// --- walk + diff ---

func fimModeString(m fs.FileMode) string {
	return fmt.Sprintf("%04o", uint32(m.Perm()))
}

// collectFIMChanges walks every in-scope directory, diffs against the local
// baseline and returns metadata-only changes plus content diffs for whitelisted
// paths. The baseline is rewritten so the next scan reports only new deltas.
func collectFIMChanges(opts fimOptions) ([]hostSecFileChange, hostSecFIMStats) {
	start := time.Now()
	stats := hostSecFIMStats{Mode: "full"}

	roots := opts.Roots
	if len(roots) == 0 {
		roots = fimDefaultRoots()
	}
	excl := newFIMExcluder(opts.Excludes, opts.Roots)
	patterns := fimEffectiveContentPatterns(opts.ContentPaths)
	stats.ContentPaths = len(patterns)
	cacheDir := ""
	if opts.ContentDiff {
		cacheDir = fimTextCacheDir()
	}

	basePath := fimBaselinePath()
	prev, hadBaseline := fimLoadBaseline(basePath)
	// Exclude our own state dir so the baseline file never reports itself.
	excl.prefixes = append(excl.prefixes, fimMatchKey(fimNormPath(fimDataDir())))

	cur := make(map[string]fimEntry, len(prev)+1024)
	deadline := start.Add(opts.Budget)
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = fimDefaultMaxFiles
	}

	for _, root := range roots {
		if stats.LimitHit || stats.BudgetHit {
			break
		}
		norm := fimNormPath(root)
		stats.Roots = append(stats.Roots, norm)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				stats.Skipped++
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if stats.Files >= maxFiles {
				stats.LimitHit = true
				return filepath.SkipAll
			}
			if stats.Files&1023 == 0 && time.Now().After(deadline) {
				stats.BudgetHit = true
				return filepath.SkipAll
			}
			np := fimNormPath(p)
			if d.IsDir() {
				if excl.skipName(d.Name()) || excl.skipPath(np) {
					return fs.SkipDir
				}
				stats.Dirs++
				return nil
			}
			// Regular files only: symlinks, sockets, devices and FIFOs are not
			// content-bearing and their metadata churns for unrelated reasons.
			if !d.Type().IsRegular() {
				return nil
			}
			if excl.skipName(d.Name()) || excl.skipPath(np) {
				return nil
			}
			// Tabs/newlines would corrupt the baseline line format; skip (vanishingly rare).
			if strings.ContainsAny(np, "\t\n\r") {
				stats.Skipped++
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				stats.Skipped++
				return nil
			}
			e := fimEntry{Size: fi.Size(), Mtime: fi.ModTime().UnixNano(), Mode: fimModeString(fi.Mode())}
			// Content-audit whitelist: hash so modification is content-truth, not mtime noise.
			if fimContentAllowed(np, patterns) && fi.Size() <= fimMaxHashBytes {
				if h, ok := hashFileLimited(p, fimMaxHashBytes); ok {
					e.SHA = h.SHA256
					stats.Hashed++
					fimSeedTextCache(cacheDir, np, h.SHA256)
				}
			}
			cur[np] = e
			stats.Files++
			return nil
		})
	}

	if !hadBaseline {
		stats.Baseline = true
		stats.DurationMS = time.Since(start).Milliseconds()
		if err := fimSaveBaseline(basePath, cur); err != nil {
			stats.Error = "baseline save failed: " + err.Error()
		}
		return nil, stats
	}
	// A walk that got cut short must not delete the baseline's knowledge of the
	// unvisited tail, otherwise the next scan reports thousands of fake deletes.
	partial := stats.LimitHit || stats.BudgetHit

	maxChanges := opts.MaxChanges
	if maxChanges <= 0 {
		maxChanges = fimDefaultMaxChanges
	}
	changes := fimDiffBaseline(prev, cur, partial, patterns, opts.ContentDiff, maxChanges, &stats)
	stats.DurationMS = time.Since(start).Milliseconds()

	merged := cur
	if partial {
		merged = make(map[string]fimEntry, len(prev)+len(cur))
		for p, e := range prev {
			merged[p] = e
		}
		for p, e := range cur {
			merged[p] = e
		}
	}
	if err := fimSaveBaseline(basePath, merged); err != nil {
		stats.Error = "baseline save failed: " + err.Error()
	}
	return changes, stats
}

func fimDiffBaseline(prev, cur map[string]fimEntry, partial bool, patterns []string, contentDiff bool, maxChanges int, stats *hostSecFIMStats) []hostSecFileChange {
	var changes []hostSecFileChange
	for p, c := range cur {
		o, ok := prev[p]
		if !ok {
			stats.Added++
			changes = append(changes, hostSecFileChange{
				Path: p, Change: "added", Reason: "added", Kind: fimPathKind(p),
				NewSHA: c.SHA, NewSize: c.Size, NewMtime: c.mtimeSec(), NewMode: c.Mode,
			})
			continue
		}
		reason := fimChangeReason(o, c)
		if reason == "" {
			continue
		}
		stats.Modified++
		changes = append(changes, hostSecFileChange{
			Path: p, Change: "modified", Reason: reason, Kind: fimPathKind(p),
			OldSHA: o.SHA, NewSHA: c.SHA,
			OldSize: o.Size, NewSize: c.Size,
			OldMtime: o.mtimeSec(), NewMtime: c.mtimeSec(),
			OldMode: o.Mode, NewMode: c.Mode,
		})
	}
	if !partial {
		for p, o := range prev {
			if _, ok := cur[p]; ok {
				continue
			}
			stats.Removed++
			changes = append(changes, hostSecFileChange{
				Path: p, Change: "removed", Reason: "removed", Kind: fimPathKind(p),
				OldSHA: o.SHA, OldSize: o.Size, OldMtime: o.mtimeSec(), OldMode: o.Mode,
			})
		}
	}

	fimSortChanges(changes)
	if maxChanges > 0 && len(changes) > maxChanges {
		changes = changes[:maxChanges]
		stats.Truncated = true
	}
	stats.Reported = len(changes)

	if contentDiff {
		cacheDir := fimTextCacheDir()
		for i := range changes {
			ch := &changes[i]
			if ch.Change != "modified" || ch.NewSHA == "" || ch.NewSHA == ch.OldSHA {
				continue
			}
			if !fimContentAllowed(ch.Path, patterns) {
				continue
			}
			if d, ok := fimMaybeTextDiff(cacheDir, ch.Path, ch.NewSHA); ok {
				ch.Diff = d.Diff
				ch.Truncated = ch.Truncated || d.Truncated
				if ch.OldSHA == "" {
					ch.OldSHA = d.OldSHA
				}
			}
		}
	}
	return changes
}

// fimChangeReason names what differs, preferring content truth when hashed.
func fimChangeReason(o, c fimEntry) string {
	if o.SHA != "" && c.SHA != "" {
		if !strings.EqualFold(o.SHA, c.SHA) {
			return "content"
		}
		if o.Mode != c.Mode {
			return "mode"
		}
		return ""
	}
	switch {
	case o.Size != c.Size:
		return "size"
	case o.Mtime != c.Mtime:
		return "mtime"
	case o.Mode != c.Mode:
		return "mode"
	}
	return ""
}

// fimSortChanges puts security-relevant paths first so the capped report keeps
// the entries an analyst actually needs.
func fimSortChanges(changes []hostSecFileChange) {
	sort.SliceStable(changes, func(i, j int) bool {
		ri, rj := fimChangeRank(changes[i]), fimChangeRank(changes[j])
		if ri != rj {
			return ri < rj
		}
		return changes[i].Path < changes[j].Path
	})
}

func fimChangeRank(c hostSecFileChange) int {
	switch fimAgentPathSeverity(c.Path) {
	case "crit":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	default:
		return 3
	}
}

// fimAgentPathSeverity mirrors the server ranking so capping keeps the same
// entries the server would rank highest.
func fimAgentPathSeverity(p string) string {
	norm := strings.ToLower(fimNormPath(p))
	base := fimBase(norm)
	switch base {
	case "shadow", "gshadow", "sudoers", "authorized_keys", "sshd_config":
		return "crit"
	case "passwd", "group", "crontab", "fstab", "hosts", "rc.local", "sudo":
		return "high"
	}
	switch {
	case strings.Contains(norm, "/.ssh/"),
		strings.Contains(norm, "/sudoers.d/"),
		strings.Contains(norm, "/cron.d/"),
		strings.Contains(norm, "/pam.d/"),
		strings.Contains(norm, "/systemd/system/"),
		strings.Contains(norm, "/startup/"),
		strings.Contains(norm, "/system32/drivers/etc/"):
		return "high"
	case strings.HasPrefix(norm, "/etc/"),
		strings.HasPrefix(norm, "/boot/"),
		strings.Contains(norm, "/usr/local/bin/"),
		strings.Contains(norm, "/usr/local/sbin/"),
		strings.Contains(norm, "/usr/bin/"),
		strings.Contains(norm, "/usr/sbin/"),
		strings.Contains(norm, "/sbin/"),
		strings.Contains(norm, "/system32/"):
		return "medium"
	}
	return "low"
}
