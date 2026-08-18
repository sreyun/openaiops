package main

import (
	"os"
	"runtime"
	"strings"
)

// linuxDistro describes the running Linux distribution for inspect / playbook modules.
// Rocky 9/10, Kylin V10/V11 (Server=RPM, Desktop=Deb), RHEL clones and Debian family
// are all normalized here so callers do not re-parse /etc/os-release ad hoc.
type linuxDistro struct {
	ID      string // rocky, kylin, rhel, ubuntu, …
	IDLike  string
	Version string // major: "9", "10", "11"
	Pretty  string
	Family  string // rhel|debian|kylin|uos|suse|arch|alpine|linux
	Pkg     string // rpm|deb|apk|zypper|pacman|""
}

func detectLinuxDistro() linuxDistro {
	id, idLike, ver, pretty := readOSRelease()
	d := linuxDistro{
		ID:      strings.ToLower(strings.TrimSpace(id)),
		IDLike:  strings.ToLower(strings.TrimSpace(idLike)),
		Version: normalizeDistroMajor(ver),
		Pretty:  strings.TrimSpace(pretty),
	}
	if d.Pretty == "" {
		if d.ID != "" {
			d.Pretty = d.ID
			if d.Version != "" {
				d.Pretty += " " + d.Version
			}
		} else {
			d.Pretty = "Linux"
		}
	}
	// Enrich Kylin from legacy release file when os-release is sparse.
	if fileExists("/etc/kylin-release") {
		if raw, err := os.ReadFile("/etc/kylin-release"); err == nil {
			line := strings.TrimSpace(string(raw))
			if line != "" {
				if d.Pretty == "" || d.Pretty == "Linux" || d.ID == "kylin" && !strings.Contains(strings.ToLower(d.Pretty), "kylin") {
					d.Pretty = line
				}
				if d.Version == "" {
					d.Version = normalizeDistroMajor(line)
				}
			}
		}
		if d.ID == "" {
			d.ID = "kylin"
		}
	}
	blob := d.ID + " " + d.IDLike + " " + strings.ToLower(d.Pretty)
	d.Family = classifyLinuxFamily(blob, d.ID)
	d.Pkg = classifyLinuxPkg(blob, d.Family)
	// Prefer VERSION_ID from pretty when still empty (e.g. "Rocky Linux 9.4").
	if d.Version == "" {
		d.Version = normalizeDistroMajor(d.Pretty)
	}
	return d
}

func classifyLinuxFamily(blob, id string) string {
	switch {
	case strings.Contains(blob, "kylin") || strings.Contains(blob, "neokylin") || id == "kylinos":
		return "kylin"
	case strings.Contains(blob, "uos") || strings.Contains(blob, "deepin"):
		return "uos"
	case id == "rocky" || strings.Contains(blob, "rocky"):
		return "rhel" // Rocky is a RHEL rebuild; keep family for package/update paths
	case strings.Contains(blob, "rhel") || strings.Contains(blob, "centos") ||
		strings.Contains(blob, "alma") || strings.Contains(blob, "fedora") ||
		strings.Contains(blob, "openeuler") || strings.Contains(blob, "euleros") ||
		strings.Contains(blob, "euler os") || strings.Contains(blob, "anolis") ||
		strings.Contains(blob, "alinux") || strings.Contains(blob, "alibaba cloud linux") ||
		strings.Contains(blob, "alibabacloudlinux") || strings.Contains(blob, "amzn") ||
		strings.Contains(blob, "amazon linux") || strings.Contains(blob, "oracle") ||
		id == "rhel" || id == "centos" || id == "almalinux" || id == "euleros" ||
		id == "alinux" || id == "openeuler":
		return "rhel"
	case strings.Contains(blob, "debian") || strings.Contains(blob, "ubuntu"):
		return "debian"
	case strings.Contains(blob, "suse") || strings.Contains(blob, "sles") || strings.Contains(blob, "opensuse"):
		return "suse"
	case strings.Contains(blob, "arch"):
		return "arch"
	case strings.Contains(blob, "alpine"):
		return "alpine"
	default:
		return "linux"
	}
}

func classifyLinuxPkg(blob, family string) string {
	// Kylin Desktop (Ubuntu-based) vs Server (RHEL/openEuler-based) share ID=kylin.
	// Prefer ID_LIKE / pretty hints, then installed managers.
	switch family {
	case "debian", "uos":
		return "deb"
	case "suse":
		return "zypper"
	case "arch":
		return "pacman"
	case "alpine":
		return "apk"
	case "rhel":
		return "rpm"
	case "kylin":
		if strings.Contains(blob, "debian") || strings.Contains(blob, "ubuntu") {
			return "deb"
		}
		if strings.Contains(blob, "rhel") || strings.Contains(blob, "centos") ||
			strings.Contains(blob, "fedora") || strings.Contains(blob, "openeuler") ||
			strings.Contains(blob, "euler") {
			return "rpm"
		}
		// Binary probe as last resort (Desktop usually has apt; Server has dnf/yum).
		if have("apt-get") && !have("dnf") && !have("yum") {
			return "deb"
		}
		if have("dnf") || have("yum") || have("rpm") {
			return "rpm"
		}
		if have("apt-get") {
			return "deb"
		}
		return "rpm"
	default:
		if have("apt-get") {
			return "deb"
		}
		if have("dnf") || have("yum") || have("rpm") {
			return "rpm"
		}
		if have("apk") {
			return "apk"
		}
		if have("zypper") {
			return "zypper"
		}
		if have("pacman") {
			return "pacman"
		}
		return ""
	}
}

// normalizeDistroMajor extracts a major version suitable for Rocky 9/10, Kylin V10/V11,
// openEuler 22.03/24.03, EulerOS 2.x/3.x, Alibaba Cloud Linux 2/3/4, Debian 10–13.
// Accepts "9.4", "10.0", "22.03", "V10", "v11", "V10 (Sword)", pretty names, etc.
func normalizeDistroMajor(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	// Prefer explicit V10 / V11 tokens for Kylin (VERSION_ID="V10" or pretty with V11).
	// Limit the V-token scan to Kylin / leading-V inputs so unrelated text containing
	// "v10" substrings cannot steal openEuler/Debian majors.
	if strings.Contains(lower, "kylin") || strings.HasPrefix(strings.TrimLeft(lower, " \t"), "v") {
		for _, maj := range []string{"11", "10", "9", "8"} {
			if strings.Contains(lower, "v"+maj) {
				return maj
			}
		}
	}
	// Strip leading V/v then take leading digits (VERSION_ID="V10" / "9.3" / "22.03").
	s = strings.TrimLeft(s, "Vv")
	var b strings.Builder
	for i, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
			continue
		}
		if i == 0 {
			continue
		}
		break
	}
	out := b.String()
	if out == "" {
		// Scan pretty names for first digit run after space (e.g. "Rocky Linux 9.4").
		for _, tok := range strings.Fields(strings.ToLower(raw)) {
			tok = strings.TrimLeft(tok, "v")
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
	}
	return out
}

// linuxPkgManagerCmd picks the preferred package manager for this distro.
// Rocky 9/10 and Kylin Server → dnf/yum; Kylin Desktop / Debian → apt-get.
// Distro identity beats "whichever binary happens to exist" (avoids apt-get on
// a host that also ships a stub apt for compatibility).
func linuxPkgManagerCmd() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	d := detectLinuxDistro()
	switch d.Pkg {
	case "deb":
		if have("apt-get") {
			return "apt-get"
		}
		if have("apt") {
			return "apt"
		}
	case "rpm":
		if have("dnf") {
			return "dnf"
		}
		if have("yum") {
			return "yum"
		}
	case "apk":
		if have("apk") {
			return "apk"
		}
	case "zypper":
		if have("zypper") {
			return "zypper"
		}
	case "pacman":
		if have("pacman") {
			return "pacman"
		}
	}
	// Fallback probe order: RPM-family first for Rocky/RHEL clones, then deb.
	for _, c := range []string{"dnf", "yum", "apt-get", "apk", "zypper", "pacman"} {
		if have(c) {
			return c
		}
	}
	return ""
}
