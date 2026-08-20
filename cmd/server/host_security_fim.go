package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	fimMaxStoredInv  = 80
	fimMaxStoredDiff = 48 << 10
	// Full-scope FIM legitimately produces many changes; keep enough to be useful
	// while bounding the persisted scan record.
	fimMaxStoredChg = 500
	// Only security-relevant paths become individual findings; the rest are
	// summarized in one aggregate finding so the list stays triageable.
	fimMaxIndividualFindings = 60
)

func clampInt(v, def, lo, hi int) int {
	if v <= 0 {
		v = def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sanitizeFIMPathList trims operator-supplied path/glob lists for agent args.
func sanitizeFIMPathList(in []string, max int) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		// Commas and newlines are the arg-list separators; a value containing
		// them would silently split into bogus entries on the agent.
		if p == "" || len(p) > 512 || strings.ContainsAny(p, ",\n\r;\x00") {
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= max {
			break
		}
	}
	return out
}

// applyFIMScanArgs pushes the FIM scope/limits to the agent module.
func applyFIMScanArgs(args map[string]string, cfg HostSecurityConfig) {
	cfg = cfg.withDefaults()
	args["fim_scope"] = cfg.FIMScope
	args["fim_max_files"] = strconv.Itoa(cfg.FIMMaxFiles)
	args["fim_max_changes"] = strconv.Itoa(cfg.FIMMaxChanges)
	args["fim_budget_sec"] = strconv.Itoa(cfg.FIMBudgetSec)
	if v := strings.Join(sanitizeFIMPathList(cfg.FIMRoots, 32), ","); v != "" {
		args["fim_roots"] = v
	}
	if v := strings.Join(sanitizeFIMPathList(cfg.FIMExcludes, 200), ","); v != "" {
		args["fim_excludes"] = v
	}
	if v := strings.Join(sanitizeFIMPathList(cfg.FIMContentPaths, 200), ","); v != "" {
		args["fim_content_paths"] = v
	}
}

// convertAgentFileChanges adopts deltas computed by a full-scope FIM agent.
func convertAgentFileChanges(in []hsAgentFileChange, contentDiff bool) []HostFileChange {
	if len(in) == 0 {
		return nil
	}
	out := make([]HostFileChange, 0, len(in))
	seen := map[string]bool{}
	for _, c := range in {
		p := fimNormalizePath(c.Path)
		if p == "" || seen[p] {
			continue
		}
		switch c.Change {
		case "added", "removed", "modified":
		default:
			continue
		}
		seen[p] = true
		ch := HostFileChange{
			Path: p, Change: c.Change, Reason: c.Reason, Kind: c.Kind,
			OldSHA:  strings.ToLower(strings.TrimSpace(c.OldSHA)),
			NewSHA:  strings.ToLower(strings.TrimSpace(c.NewSHA)),
			OldSize: c.OldSize, NewSize: c.NewSize,
			OldMtime: c.OldMtime, NewMtime: c.NewMtime,
			OldMode: c.OldMode, NewMode: c.NewMode,
			Truncated: c.Truncated,
		}
		if contentDiff {
			ch.Diff = c.Diff
		}
		out = append(out, ch)
	}
	sortHostFileChanges(out)
	return sanitizeFileChanges(out)
}

func convertAgentFIMStats(in *hsAgentFIMStats) *HostFIMStats {
	if in == nil {
		return nil
	}
	mode := "full"
	if strings.EqualFold(in.Mode, "sensitive") {
		mode = "sensitive"
	}
	roots := in.Roots
	if len(roots) > 16 {
		roots = roots[:16]
	}
	return &HostFIMStats{
		Mode: mode, Baseline: in.Baseline, Roots: roots,
		Files: in.Files, Dirs: in.Dirs,
		Added: in.Added, Removed: in.Removed, Modified: in.Modified,
		Reported: in.Reported, Skipped: in.Skipped,
		LimitHit: in.LimitHit, BudgetHit: in.BudgetHit, Truncated: in.Truncated,
		DurationMS: in.DurationMS, ContentPaths: in.ContentPaths,
		Error: truncateRun(in.Error, 200),
	}
}

// fimNormalizePath keeps agent-reported paths stable across server GOOS.
// Never use filepath.Clean here: a Windows server would turn "/etc/hosts" into "\etc\hosts".
func fimNormalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimRight(p, "/")
	}
	return p
}

func trimHostFileInventory(inv []hsAgentFileInv) []HostFileHash {
	if len(inv) == 0 {
		return nil
	}
	out := make([]HostFileHash, 0, len(inv))
	seen := map[string]bool{}
	for _, it := range inv {
		p := fimNormalizePath(it.Path)
		if p == "" || it.SHA256 == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, HostFileHash{
			Path: p, SHA256: strings.ToLower(strings.TrimSpace(it.SHA256)),
			Size: it.Size, Mtime: it.Mtime, Kind: it.Kind,
		})
		if len(out) >= fimMaxStoredInv {
			break
		}
	}
	return out
}

func sanitizeFileChanges(changes []HostFileChange) []HostFileChange {
	if len(changes) == 0 {
		return nil
	}
	if len(changes) > fimMaxStoredChg {
		changes = changes[:fimMaxStoredChg]
	}
	out := make([]HostFileChange, 0, len(changes))
	for _, ch := range changes {
		ch.Path = fimNormalizePath(ch.Path)
		if ch.Path == "" {
			continue
		}
		switch ch.Change {
		case "added", "removed", "modified":
		default:
			continue
		}
		if len(ch.Diff) > fimMaxStoredDiff {
			ch.Diff = ch.Diff[:fimMaxStoredDiff]
			ch.Truncated = true
		}
		out = append(out, ch)
	}
	return out
}

func indexHostFileInventory(inv []HostFileHash) map[string]HostFileHash {
	m := make(map[string]HostFileHash, len(inv))
	for _, it := range inv {
		m[it.Path] = it
	}
	return m
}

func indexAgentTextDiffs(diffs []hsAgentTextDiff) map[string]hsAgentTextDiff {
	m := make(map[string]hsAgentTextDiff, len(diffs))
	for _, d := range diffs {
		p := fimNormalizePath(d.Path)
		if p == "" {
			continue
		}
		if len(d.Diff) > fimMaxStoredDiff {
			d.Diff = d.Diff[:fimMaxStoredDiff]
			d.Truncated = true
		}
		m[p] = d
	}
	return m
}

// pickFIMInventoryToStore chooses which inventory to persist on a completed scan.
// Never replace a nonempty baseline with an empty cur (would force perpetual re-baseline).
func pickFIMInventoryToStore(cur, livePrev, hostPrev []HostFileHash, fimOn bool) []HostFileHash {
	if len(cur) > 0 {
		return cur
	}
	if !fimOn {
		if len(livePrev) > 0 {
			return livePrev
		}
		return hostPrev
	}
	if len(livePrev) > 0 {
		return livePrev
	}
	if len(hostPrev) > 0 {
		return hostPrev
	}
	return cur
}

// diffHostFileInventory compares current inventory to previous baseline.
// When prev is empty/nil, returns nil changes and baselineEstablished=true (first scan).
func diffHostFileInventory(prev, cur []HostFileHash, textDiffs []hsAgentTextDiff) (changes []HostFileChange, baselineEstablished bool) {
	if len(prev) == 0 {
		if len(cur) > 0 {
			return nil, true
		}
		return nil, false
	}
	if len(cur) == 0 {
		// Old agent or FIM disabled mid-flight: no noisy removed spam.
		return nil, false
	}
	prevMap := indexHostFileInventory(prev)
	curMap := indexHostFileInventory(cur)
	diffMap := indexAgentTextDiffs(textDiffs)

	for path, c := range curMap {
		p, ok := prevMap[path]
		if !ok {
			changes = append(changes, HostFileChange{
				Path: path, Change: "added", NewSHA: c.SHA256, NewMtime: c.Mtime,
			})
			continue
		}
		if !strings.EqualFold(p.SHA256, c.SHA256) {
			ch := HostFileChange{
				Path: path, Change: "modified",
				OldSHA: p.SHA256, NewSHA: c.SHA256,
				OldMtime: p.Mtime, NewMtime: c.Mtime,
			}
			if d, ok := diffMap[path]; ok {
				ch.Diff = d.Diff
				ch.Truncated = d.Truncated
				if ch.OldSHA == "" {
					ch.OldSHA = d.OldSHA
				}
			}
			changes = append(changes, ch)
		}
	}
	for path, p := range prevMap {
		if _, ok := curMap[path]; !ok {
			changes = append(changes, HostFileChange{
				Path: path, Change: "removed", OldSHA: p.SHA256, OldMtime: p.Mtime,
			})
		}
	}
	// Stable-ish order: critical paths first, then path.
	sortHostFileChanges(changes)
	return sanitizeFileChanges(changes), false
}

func sortHostFileChanges(changes []HostFileChange) {
	rank := func(c HostFileChange) int {
		switch fimPathSeverity(c.Path) {
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
	sort.SliceStable(changes, func(i, j int) bool {
		ri, rj := rank(changes[i]), rank(changes[j])
		if ri != rj {
			return ri < rj
		}
		return changes[i].Path < changes[j].Path
	})
}

func fimBaseName(path string) string {
	p := fimNormalizePath(path)
	if i := strings.LastIndex(p, "/"); i >= 0 && i+1 < len(p) {
		return p[i+1:]
	}
	return p
}

// fimPathSeverity ranks a changed path. With full-scope monitoring most paths
// are ordinary application churn ("low"); only these buckets become findings.
func fimPathSeverity(path string) string {
	lower := strings.ToLower(fimNormalizePath(path))
	base := fimBaseName(lower)
	switch base {
	case "shadow", "gshadow", "sshd_config", "authorized_keys", "sudoers":
		return "high"
	case "passwd", "group", "crontab", "hosts", "resolv.conf", "rc.local", "fstab",
		"nsswitch.conf", "login.defs", "sysctl.conf":
		return "medium"
	}
	for _, frag := range []string{
		"/.ssh/", "/sudoers.d/", "/cron.d/", "/cron.daily/", "/cron.hourly/",
		"/pam.d/", "/systemd/system/", "/startup/", "/system32/drivers/etc/",
	} {
		if strings.Contains(lower, frag) {
			return "high"
		}
	}
	for _, frag := range []string{
		"/usr/local/bin/", "/usr/local/sbin/", "/usr/bin/", "/usr/sbin/", "/sbin/", "/bin/",
		"/boot/", "/system32/", "/syswow64/",
	} {
		if strings.Contains(lower, frag) {
			return "medium"
		}
	}
	if strings.HasPrefix(lower, "/etc/") {
		return "medium"
	}
	return "low"
}

func fimFindingsFromChanges(changes []HostFileChange) []HostFinding {
	var out []HostFinding
	quiet := map[string]int{"added": 0, "removed": 0, "modified": 0}
	quietTotal := 0
	for _, ch := range changes {
		level := fimPathSeverity(ch.Path)
		// A full-scope walk sees ordinary application churn. Only security-relevant
		// paths deserve their own finding; everything else rolls into one summary
		// so the findings list stays triageable instead of thousands of rows.
		if level == "low" || len(out) >= fimMaxIndividualFindings {
			quiet[ch.Change]++
			quietTotal++
			continue
		}
		// Critical auth files that change → bump.
		base := strings.ToLower(fimBaseName(ch.Path))
		if (base == "shadow" || base == "authorized_keys" || base == "sudoers") && ch.Change != "added" {
			if level == "high" {
				level = "crit"
			}
		}
		title := "文件完整性变更"
		switch ch.Change {
		case "added":
			title = "监控文件新增"
		case "removed":
			title = "监控文件删除"
		case "modified":
			title = "监控文件内容变更"
		}
		out = append(out, HostFinding{
			Level: level, Category: "fim",
			ID:     "fim." + ch.Change + "." + shortDiscHash(ch.Path),
			Title:  title + " — " + fimBaseName(ch.Path),
			Detail: fimChangeDetail(ch),
			Suggest: func() string {
				if ch.Diff != "" {
					return "已附带脱敏内容差异，请核对变更行是否符合变更单"
				}
				return "核对是否为预期运维变更；非预期则回滚并排查入侵痕迹"
			}(),
		})
	}
	if quietTotal > 0 {
		out = append(out, HostFinding{
			Level: "info", Category: "fim", ID: "fim.summary",
			Title: "文件系统变更汇总（非敏感路径）",
			Detail: fmt.Sprintf("新增 %d，修改 %d，删除 %d（共 %d 项，仅记录路径与元数据，不采集文件内容）",
				quiet["added"], quiet["modified"], quiet["removed"], quietTotal),
			Suggest: "在「文件变更」列表中核对是否包含非预期目录；如需内容级审计，请把路径加入内容审计白名单",
		})
	}
	return out
}

// fimChangeDetail renders what actually differs, preferring content hashes and
// falling back to the metadata that triggered the delta.
func fimChangeDetail(ch HostFileChange) string {
	var parts []string
	switch {
	case ch.NewSHA != "" || ch.OldSHA != "":
		switch ch.Change {
		case "modified":
			parts = append(parts, "sha "+shortSHA(ch.OldSHA)+" → "+shortSHA(ch.NewSHA))
		case "added":
			parts = append(parts, "sha "+shortSHA(ch.NewSHA))
		case "removed":
			parts = append(parts, "sha "+shortSHA(ch.OldSHA))
		}
	case ch.Change == "modified" && ch.OldSize != ch.NewSize:
		parts = append(parts, fmt.Sprintf("size %d → %d", ch.OldSize, ch.NewSize))
	}
	if ch.OldMode != "" && ch.NewMode != "" && ch.OldMode != ch.NewMode {
		parts = append(parts, "mode "+ch.OldMode+" → "+ch.NewMode)
	}
	if ch.Reason != "" {
		parts = append(parts, "reason="+ch.Reason)
	}
	if len(parts) == 0 {
		return ch.Path
	}
	return ch.Path + " (" + strings.Join(parts, ", ") + ")"
}

func shortSHA(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
