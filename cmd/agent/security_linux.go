//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// SecurityModule identifies the active MAC (Mandatory Access Control) system
// on the host. Multiple modules can coexist (e.g. kysec + SELinux on Kylin).
type SecurityModule struct {
	Name    string // "kysec", "selinux", "apparmor", "firewalld", "none"
	Status  string // "enforcing", "permissive", "disabled", "unknown"
	Details string // human-readable details for diagnostics
}

// OSDistInfo holds the detected Linux distribution identity.
type OSDistInfo struct {
	ID        string // e.g. "kylin", "uos", "centos"
	Name      string // e.g. "Kylin Linux Advanced Server"
	Version   string // e.g. "V10"
	IDLike    string // e.g. "centos rhel fedora"
	PrettyName string
}

// securityEnv caches the detected security environment so it's only probed
// once at startup (the OS security config doesn't change at runtime).
var (
	secEnvOnce   sync.Once
	secEnvResult []SecurityModule
	secIsKylin   bool
	secOSDist    OSDistInfo
	// maintenanceMode tracks whether the agent has temporarily relaxed security.
	maintenanceTimer *time.Timer
)

// detectSecurityEnv probes the host for active security modules and returns
// a slice of detected modules. It also sets secIsKylin if the OS is Kylin.
// This is called once at Agent startup; results are cached.
func detectSecurityEnv() ([]SecurityModule, bool) {
	secEnvOnce.Do(func() {
		secOSDist = detectOSDist()
		secIsKylin = isKylinFamily(secOSDist)
		secEnvResult = probeSecurityModules()
	})
	return secEnvResult, secIsKylin
}

// getOSDist returns the cached OS distribution info.
func getOSDist() OSDistInfo {
	if secOSDist.ID == "" {
		secOSDist = detectOSDist()
	}
	return secOSDist
}

// detectOSDist reads /etc/os-release and returns structured distro info.
// Covers all major Linux distributions including Chinese domestic ones.
func detectOSDist() OSDistInfo {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return OSDistInfo{ID: "unknown", Name: "Linux"}
	}
	defer f.Close()
	var info OSDistInfo
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, "\"'")
		switch k {
		case "ID":
			info.ID = strings.ToLower(v)
		case "NAME":
			info.Name = v
		case "VERSION_ID":
			info.Version = v
		case "ID_LIKE":
			info.IDLike = strings.ToLower(v)
		case "PRETTY_NAME":
			info.PrettyName = v
		}
	}
	if info.ID == "" {
		info.ID = "unknown"
	}
	if info.Name == "" {
		info.Name = "Linux"
	}
	return info
}

// isKylinFamily checks if the distro is any Kylin variant.
func isKylinFamily(d OSDistInfo) bool {
	for _, s := range []string{d.ID, d.IDLike, d.Name} {
		if strings.Contains(strings.ToLower(s), "kylin") {
			return true
		}
	}
	return false
}

// probeSecurityModules checks for all known Linux security modules.
func probeSecurityModules() []SecurityModule {
	var modules []SecurityModule

	// 1. kysec (Kylin Security Enhanced Control)
	if m := detectKysec(); m != nil {
		modules = append(modules, *m)
	}
	// 2. SELinux
	if m := detectSELinux(); m != nil {
		modules = append(modules, *m)
	}
	// 3. AppArmor
	if m := detectAppArmor(); m != nil {
		modules = append(modules, *m)
	}
	// 4. firewalld
	if m := detectFirewalld(); m != nil {
		modules = append(modules, *m)
	}

	return modules
}

// detectKysec checks for Kylin's kysec security module.
// kysec paths vary by version:
//   - /proc/sys/kernel/kysec/enable (V10)
//   - /etc/kysec/ directory (V10/V11)
//   - `sestatus` command output mentioning kysec
func detectKysec() *SecurityModule {
	// Method 1: check /proc/sys/kernel/kysec
	if b, err := os.ReadFile("/proc/sys/kernel/kysec/enable"); err == nil {
		val := strings.TrimSpace(string(b))
		status := "unknown"
		switch val {
		case "1":
			status = "enforcing"
		case "0":
			status = "disabled"
		}
		return &SecurityModule{Name: "kysec", Status: status, Details: "/proc/sys/kernel/kysec/enable=" + val}
	}

	// Method 2: check /etc/kysec directory
	if info, err := os.Stat("/etc/kysec"); err == nil && info.IsDir() {
		status := "unknown"
		details := "/etc/kysec directory exists"
		if out, err := exec.Command("sestatus").Output(); err == nil {
			outStr := string(out)
			if strings.Contains(outStr, "kysec") {
				if strings.Contains(outStr, "enabled") || strings.Contains(outStr, "enforcing") {
					status = "enforcing"
				} else if strings.Contains(outStr, "permissive") {
					status = "permissive"
				} else {
					status = "disabled"
				}
				details = strings.TrimSpace(outStr)
			}
		}
		return &SecurityModule{Name: "kysec", Status: status, Details: details}
	}

	// Method 3: check via getenforce for kysec-patched SELinux
	if out, err := exec.Command("getenforce").Output(); err == nil {
		val := strings.TrimSpace(string(out))
		lower := strings.ToLower(val)
		if strings.Contains(lower, "kysec") || strings.Contains(lower, "enforcing") {
			if secIsKylin {
				return &SecurityModule{Name: "kysec", Status: mapEnforceStatus(val), Details: "getenforce=" + val}
			}
		}
	}

	return nil
}

// detectSELinux checks for SELinux via /sys/fs/selinux/enforce.
// enforce=0 means permissive (policy loaded but not blocking), not "disabled".
func detectSELinux() *SecurityModule {
	b, err := os.ReadFile("/sys/fs/selinux/enforce")
	if err != nil {
		return nil
	}
	val := strings.TrimSpace(string(b))
	status := "permissive"
	if val == "1" {
		status = "enforcing"
	}
	return &SecurityModule{Name: "selinux", Status: status, Details: "/sys/fs/selinux/enforce=" + val}
}

// detectAppArmor checks for AppArmor via /sys/module/apparmor.
func detectAppArmor() *SecurityModule {
	if _, err := os.Stat("/sys/module/apparmor"); err != nil {
		return nil
	}
	status := "unknown"
	if b, err := os.ReadFile("/sys/module/apparmor/parameters/enabled"); err == nil {
		val := strings.TrimSpace(string(b))
		if val == "Y" || val == "1" {
			status = "enforcing"
		} else {
			status = "disabled"
		}
	}
	return &SecurityModule{Name: "apparmor", Status: status, Details: "/sys/module/apparmor loaded"}
}

// detectFirewalld checks whether firewalld is running and may block agent traffic.
func detectFirewalld() *SecurityModule {
	out, err := exec.Command("firewall-cmd", "--state").Output()
	if err != nil {
		return nil // firewalld not installed or not running
	}
	state := strings.TrimSpace(string(out))
	if state != "running" {
		return nil
	}
	// firewalld is active — check if it might block agent outbound traffic
	return &SecurityModule{Name: "firewalld", Status: "enforcing",
		Details: "firewalld running; verify agent outbound ports are allowed"}
}

func mapEnforceStatus(val string) string {
	lower := strings.ToLower(val)
	switch {
	case strings.Contains(lower, "enforcing"):
		return "enforcing"
	case strings.Contains(lower, "permissive"):
		return "permissive"
	case strings.Contains(lower, "disabled"):
		return "disabled"
	default:
		return "unknown"
	}
}

// isPermissionError returns true if the error is an os.ErrPermission or
// contains "permission denied" in its message (covers syscall-level EPERM/EACCES).
func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "operation not permitted")
}

// checkProcAccess tests whether the Agent can read standard procfs paths.
// Returns a map of path -> error for any path that fails. Used at startup
// to give actionable diagnostics on Kylin/SELinux systems.
func checkProcAccess() map[string]error {
	paths := []string{
		"/proc/stat",
		"/proc/meminfo",
		"/proc/net/dev",
		"/proc/loadavg",
		"/proc/uptime",
		"/proc/diskstats",
		"/proc/mounts",
	}
	results := map[string]error{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			results[p] = err
			continue
		}
		f.Close()
	}
	// Test /proc/[pid]/comm for a known PID (self)
	if b, err := os.ReadFile("/proc/self/comm"); err != nil {
		results["/proc/self/comm"] = err
		_ = b
	}
	return results
}

// setKysecMode switches the kysec security module to the given mode.
// Supported modes: "permissive" (relax), "enforcing" (restore), "auto" (print steps only).
// When mode is "permissive", a timer is set to auto-restore after the given duration
// (default 2h) to prevent permanent security relaxation.
func setKysecMode(mode string, autoRestoreAfter time.Duration) error {
	if autoRestoreAfter <= 0 {
		autoRestoreAfter = 2 * time.Hour
	}

	switch mode {
	case "permissive":
		return activatePermissive(autoRestoreAfter)

	case "enforcing":
		return activateEnforcing()

	case "auto":
		// Dry-run: print what would be done
		fmt.Fprintln(os.Stderr, "[AIOps] 安全模块维护模式操作指引：")
		fmt.Fprintln(os.Stderr, "  方式1: sudo kysec_set -m permissive          # 麒麟 kysec 切换宽容模式")
		fmt.Fprintln(os.Stderr, "  方式2: sudo setenforce 0                     # SELinux 切换宽容模式")
		fmt.Fprintln(os.Stderr, "  方式3: sudo kysec_adm -a /path/to/aiops-agent # 添加 Agent 白名单")
		fmt.Fprintln(os.Stderr, "  恢复: sudo kysec_set -m enforcing            # 恢复强制模式")
		return nil

	default:
		return fmt.Errorf("unknown security mode: %s (use permissive/enforcing/auto)", mode)
	}
}

func activatePermissive(autoRestore time.Duration) error {
	// Try kysec first (Kylin)
	if _, err := os.Stat("/etc/kysec"); err == nil {
		if out, err := exec.Command("kysec_set", "-m", "permissive").CombinedOutput(); err != nil {
			return fmt.Errorf("kysec_set -m permissive failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
		scheduleAutoRestore(autoRestore)
		return nil
	}
	// Try /proc/sys/kernel/kysec
	if _, err := os.Stat("/proc/sys/kernel/kysec/enable"); err == nil {
		if err := os.WriteFile("/proc/sys/kernel/kysec/enable", []byte("0"), 0644); err != nil {
			return fmt.Errorf("write /proc/sys/kernel/kysec/enable=0 failed: %w", err)
		}
		scheduleAutoRestore(autoRestore)
		return nil
	}
	// Fallback: setenforce 0 (SELinux / kysec-patched SELinux)
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err == nil {
		if out, err := exec.Command("setenforce", "0").CombinedOutput(); err != nil {
			return fmt.Errorf("setenforce 0 failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
		scheduleAutoRestore(autoRestore)
		return nil
	}
	return fmt.Errorf("未检测到可切换的安全模块（kysec/SELinux）")
}

func activateEnforcing() error {
	// Stop any pending auto-restore timer
	if maintenanceTimer != nil {
		maintenanceTimer.Stop()
		maintenanceTimer = nil
	}

	if _, err := os.Stat("/etc/kysec"); err == nil {
		if out, err := exec.Command("kysec_set", "-m", "enforcing").CombinedOutput(); err != nil {
			return fmt.Errorf("kysec_set -m enforcing failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil
	}
	if _, err := os.Stat("/proc/sys/kernel/kysec/enable"); err == nil {
		if err := os.WriteFile("/proc/sys/kernel/kysec/enable", []byte("1"), 0644); err != nil {
			return fmt.Errorf("write /proc/sys/kernel/kysec/enable=1 failed: %w", err)
		}
		return nil
	}
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err == nil {
		if out, err := exec.Command("setenforce", "1").CombinedOutput(); err != nil {
			return fmt.Errorf("setenforce 1 failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
		return nil
	}
	return fmt.Errorf("未检测到可切换的安全模块（kysec/SELinux）")
}

func scheduleAutoRestore(after time.Duration) {
	if maintenanceTimer != nil {
		maintenanceTimer.Stop()
	}
	maintenanceTimer = time.AfterFunc(after, func() {
		if err := activateEnforcing(); err != nil {
			fmt.Fprintf(os.Stderr, "[AIOps] 安全模块自动恢复失败: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "[AIOps] 安全维护模式已到期，安全模块已自动恢复为 enforcing 模式")
		}
	})
}

// securityFixCommands returns platform-specific fix commands based on detected modules.
func securityFixCommands(modules []SecurityModule) []string {
	var cmds []string
	for _, m := range modules {
		if m.Status != "enforcing" {
			continue
		}
		switch m.Name {
		case "kysec":
			cmds = append(cmds,
				"sudo kysec_set -m permissive          # 临时切换宽容模式",
				"sudo kysec_adm -a /path/to/aiops-agent # 添加 Agent 白名单(推荐)",
			)
		case "selinux":
			cmds = append(cmds,
				"sudo setenforce 0                      # 临时切换 SELinux 为 permissive",
				"sudo ausearch -m avc -ts recent | grep aiops-agent   # 查看拒绝日志",
				"# 长期方案：按 AVC 拒绝日志编写本地 SELinux 策略模块（本发行版不附带预置 .pp）",
			)
		case "apparmor":
			cmds = append(cmds,
				"sudo aa-complain /path/to/aiops-agent   # 切换为 complain 模式",
			)
		case "firewalld":
			cmds = append(cmds,
				"# Agent 主动出站连服务端；若出站被限制，放行到监控中心的 TCP 端口（默认 8529）",
				"sudo firewall-cmd --permanent --add-rich-rule='rule family=ipv4 destination address=<server-ip> port port=8529 protocol=tcp accept' && sudo firewall-cmd --reload",
			)
		}
	}
	return cmds
}
