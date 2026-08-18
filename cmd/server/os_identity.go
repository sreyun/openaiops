package main

import (
	"strings"
)

// hostDistroProfile is derived from Host.OS (GOOS) + Host.Platform (pretty name)
// so playbook targeting works across Rocky/Kylin/openEuler/EulerOS/Aliyun/Debian
// and Windows/macOS version lines without requiring a new Agent field.
type hostDistroProfile struct {
	GOOS     string // linux|windows|darwin
	ID       string // rocky|kylin|openeuler|euleros|alinux|windows|darwin|…
	Family   string // rhel|debian|kylin|uos|linux|windows|darwin|…
	Version  string // major / year: 9|10|11|22|24|2019|2022|2025|…
	Platform string // original pretty string
}

func hostDistro(h *Host) hostDistroProfile {
	if h == nil {
		return hostDistroProfile{}
	}
	p := hostDistroProfile{
		GOOS:     strings.ToLower(strings.TrimSpace(h.OS)),
		Platform: strings.TrimSpace(h.Platform),
	}
	if p.GOOS == "macos" || p.GOOS == "osx" || p.GOOS == "mac" {
		p.GOOS = "darwin"
	}
	blob := strings.ToLower(p.Platform + " " + p.GOOS)
	switch {
	case p.GOOS == "windows":
		p.GOOS, p.Family, p.ID = "windows", "windows", "windows"
		p.Version = normalizeWindowsVersion(p.Platform)
	case p.GOOS == "darwin":
		p.GOOS, p.Family, p.ID = "darwin", "darwin", "darwin"
		p.Version = normalizeHostDistroMajor(p.Platform)
	case p.GOOS == "linux":
		// Trust GOOS: do not reclassify via Platform text (e.g. WSL / Wine strings
		// containing "windows" must stay linux for playbook targeting).
		p.ID, p.Family = classifyHostDistro(blob)
		p.Version = normalizeHostDistroMajor(p.Platform)
	default:
		// Legacy / unknown GOOS — fall back to Platform heuristics.
		switch {
		case strings.Contains(blob, "windows"):
			p.GOOS, p.Family, p.ID = "windows", "windows", "windows"
			p.Version = normalizeWindowsVersion(p.Platform)
		case strings.Contains(blob, "macos") || strings.Contains(blob, "darwin"):
			p.GOOS, p.Family, p.ID = "darwin", "darwin", "darwin"
			p.Version = normalizeHostDistroMajor(p.Platform)
		default:
			if p.GOOS == "" {
				p.GOOS = "linux"
			}
			p.ID, p.Family = classifyHostDistro(blob)
			p.Version = normalizeHostDistroMajor(p.Platform)
		}
	}
	return p
}

func classifyHostDistro(blob string) (id, family string) {
	switch {
	case strings.Contains(blob, "kylin") || strings.Contains(blob, "neokylin") || strings.Contains(blob, "kylinos"):
		return "kylin", "kylin"
	case strings.Contains(blob, "uos") || strings.Contains(blob, "deepin"):
		return "uos", "uos"
	case strings.Contains(blob, "rocky"):
		return "rocky", "rhel"
	case strings.Contains(blob, "alma"):
		return "almalinux", "rhel"
	case strings.Contains(blob, "centos"):
		return "centos", "rhel"
	case strings.Contains(blob, "red hat") || strings.Contains(blob, "rhel") || strings.Contains(blob, "redhat"):
		return "rhel", "rhel"
	// openEuler before EulerOS: "openeuler" must not be classified as euleros.
	case strings.Contains(blob, "openeuler"):
		return "openeuler", "rhel"
	case strings.Contains(blob, "euleros") || strings.Contains(blob, "euler os"):
		return "euleros", "rhel"
	case strings.Contains(blob, "alibaba cloud linux") || strings.Contains(blob, "alibabacloudlinux") ||
		strings.Contains(blob, "alinux") ||
		(strings.Contains(blob, "alibaba") && strings.Contains(blob, "linux")):
		return "alinux", "rhel"
	case strings.Contains(blob, "anolis"):
		return "anolis", "rhel"
	case strings.Contains(blob, "amazon linux") || strings.Contains(blob, "amzn"):
		return "amzn", "rhel"
	case strings.Contains(blob, "fedora"):
		return "fedora", "rhel"
	case strings.Contains(blob, "ubuntu"):
		return "ubuntu", "debian"
	case strings.Contains(blob, "debian"):
		return "debian", "debian"
	case strings.Contains(blob, "suse") || strings.Contains(blob, "sles"):
		return "suse", "suse"
	case strings.Contains(blob, "alpine"):
		return "alpine", "alpine"
	case strings.Contains(blob, "arch"):
		return "arch", "arch"
	default:
		if strings.Contains(blob, "linux") {
			return "linux", "linux"
		}
		return "", "linux"
	}
}

func normalizeHostDistroMajor(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	// Kylin V10/V11 only — avoid stealing majors from "… v10 …" noise in other distros.
	if strings.Contains(lower, "kylin") || strings.HasPrefix(strings.TrimLeft(lower, " \t"), "v") {
		for _, maj := range []string{"11", "10", "9", "8"} {
			if strings.Contains(lower, "v"+maj) {
				return maj
			}
		}
	}
	// "Rocky Linux 9.4" / "openEuler 22.03 LTS" / "macOS 15.2" / "9.3 (Blue Onyx)"
	for _, tok := range strings.Fields(lower) {
		tok = strings.TrimLeft(tok, "v(")
		n := ""
		for _, r := range tok {
			if r >= '0' && r <= '9' {
				n += string(r)
			} else if n != "" {
				break
			} else {
				break
			}
		}
		if n != "" {
			return n
		}
	}
	return ""
}

// normalizeWindowsVersion maps Platform captions to a stable major token:
// desktop 10/11 or Server year 2012/2016/2019/2022/2025.
func normalizeWindowsVersion(platform string) string {
	lower := strings.ToLower(platform)
	for _, yr := range []string{"2025", "2022", "2019", "2016", "2012"} {
		if strings.Contains(lower, "server") && strings.Contains(lower, yr) {
			return yr
		}
	}
	if strings.Contains(lower, "windows 11") || strings.Contains(lower, "windows11") {
		return "11"
	}
	if strings.Contains(lower, "windows 10") || strings.Contains(lower, "windows10") {
		return "10"
	}
	// Fallback: "Windows 11 (Build 26100)" / bare year tokens.
	return normalizeHostDistroMajor(platform)
}

// distroVersionMatches reports whether want (selector side) matches have (host).
// "22" matches "22" / "22.03"; "2.5" matches "2.5"; empty want always matches.
func distroVersionMatches(want, have string) bool {
	want = strings.TrimSpace(strings.ToLower(want))
	have = strings.TrimSpace(strings.ToLower(have))
	if want == "" {
		return true
	}
	if have == "" {
		return false
	}
	if want == have {
		return true
	}
	if normalizeHostDistroMajor(have) == want || have == want {
		return true
	}
	if strings.HasPrefix(have, want+".") || strings.HasPrefix(want, have+".") {
		return true
	}
	return false
}

// matchHostSystemSelector reports whether host matches system:<token>.
// Supports linux/windows/macos/darwin, distro aliases, and optional
// version suffix: system:rocky:9, system:openeuler:22, system:windows:2022,
// system:macos:15.
func matchHostSystemSelector(h *Host, sys string) bool {
	sys = strings.ToLower(strings.TrimSpace(sys))
	if sys == "" || h == nil {
		return false
	}
	base := sys
	wantVer := ""
	if i := strings.Index(sys, ":"); i >= 0 {
		base = sys[:i]
		wantVer = sys[i+1:]
	}
	p := hostDistro(h)
	ok := false
	switch base {
	case "linux":
		ok = p.GOOS == "linux"
	case "windows":
		ok = p.GOOS == "windows"
	case "macos", "darwin", "osx", "mac":
		ok = p.GOOS == "darwin"
	case "rocky", "rockylinux":
		ok = p.ID == "rocky" || strings.Contains(strings.ToLower(p.Platform), "rocky")
	case "kylin", "neokylin", "kylinos":
		ok = p.ID == "kylin" || p.Family == "kylin"
	case "rhel", "redhat":
		ok = p.Family == "rhel" || p.ID == "rhel"
	case "centos":
		ok = p.ID == "centos"
	case "alma", "almalinux":
		ok = p.ID == "almalinux"
	case "debian":
		ok = p.Family == "debian" || p.ID == "debian"
	case "ubuntu":
		ok = p.ID == "ubuntu"
	case "uos":
		ok = p.ID == "uos" || p.Family == "uos"
	case "openeuler":
		ok = p.ID == "openeuler"
	case "euleros", "euler":
		ok = p.ID == "euleros"
	case "alinux", "alibaba", "alibabacloudlinux":
		ok = p.ID == "alinux"
	case "anolis":
		ok = p.ID == "anolis"
	case "amzn", "amazon", "amazonlinux":
		ok = p.ID == "amzn"
	case "fedora":
		ok = p.ID == "fedora"
	default:
		os := strings.ToLower(h.OS)
		ok = os == base ||
			(base == "macos" && os == "darwin") ||
			p.ID == base || p.Family == base ||
			strings.Contains(strings.ToLower(p.Platform), base)
	}
	if !ok {
		return false
	}
	if wantVer == "" {
		return true
	}
	if distroVersionMatches(wantVer, p.Version) {
		return true
	}
	// Allow matching against full platform string (e.g. openEuler 22.03, EulerOS 2.5).
	return strings.Contains(strings.ToLower(p.Platform), wantVer)
}

// knownPlaybookSystemBase lists accepted system: selector bases (version suffix optional).
func knownPlaybookSystemBase(base string) bool {
	switch strings.ToLower(strings.TrimSpace(base)) {
	case "linux", "windows", "macos", "darwin", "osx", "mac",
		"rocky", "rockylinux", "kylin", "neokylin", "kylinos",
		"rhel", "redhat", "centos", "alma", "almalinux",
		"ubuntu", "debian", "uos",
		"openeuler", "euleros", "euler",
		"alinux", "alibaba", "alibabacloudlinux",
		"anolis", "amzn", "amazon", "amazonlinux", "fedora":
		return true
	default:
		return false
	}
}
