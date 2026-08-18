//go:build !linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// SecurityModule stub for non-Linux platforms.
type SecurityModule struct {
	Name    string
	Status  string
	Details string
}

// OSDistInfo stub for non-Linux platforms.
type OSDistInfo struct {
	ID         string
	Name       string
	Version    string
	IDLike     string
	PrettyName string
}

// detectSecurityEnv probes platform-specific security modules.
// Windows: Defender, Controlled Folder Access, Firewall.
// macOS: SIP, TCC (Full Disk Access).
func detectSecurityEnv() ([]SecurityModule, bool) {
	switch runtime.GOOS {
	case "windows":
		return detectWindowsSecurity(), false
	case "darwin":
		return detectMacSecurity(), false
	default:
		return nil, false
	}
}

// getOSDist returns OS info for non-Linux platforms.
func getOSDist() OSDistInfo {
	return OSDistInfo{
		ID:   runtime.GOOS,
		Name: runtime.GOOS,
	}
}

// setKysecMode is a no-op on non-Linux platforms.
func setKysecMode(mode string, _ time.Duration) error {
	return fmt.Errorf("security mode switching is only supported on Linux")
}

// securityFixCommands returns empty on non-Linux platforms.
func securityFixCommands(modules []SecurityModule) []string { return nil }

// checkProcAccess is a no-op on non-Linux platforms.
func checkProcAccess() map[string]error {
	return nil
}

// isPermissionError stub for non-Linux.
func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "access is denied")
}

// ---------- Windows security detection ----------

func detectWindowsSecurity() []SecurityModule {
	var modules []SecurityModule

	// 1. Windows Defender real-time protection
	if m := detectWindowsDefender(); m != nil {
		modules = append(modules, *m)
	}
	// 2. Controlled Folder Access
	if m := detectControlledFolderAccess(); m != nil {
		modules = append(modules, *m)
	}
	// 3. Windows Firewall
	if m := detectWindowsFirewall(); m != nil {
		modules = append(modules, *m)
	}

	return modules
}

func detectWindowsDefender() *SecurityModule {
	// Check registry for real-time protection status
	// HKLM\SOFTWARE\Microsoft\Windows Defender\Real-Time Protection\DisableRealtimeMonitoring
	out, err := exec.Command("reg", "query",
		`HKLM\SOFTWARE\Microsoft\Windows Defender\Real-Time Protection`,
		"/v", "DisableRealtimeMonitoring").Output()
	if err != nil {
		// Registry key may not exist if Defender is managed by GPO or third-party AV
		return &SecurityModule{Name: "defender", Status: "unknown",
			Details: "无法读取 Defender 注册表状态（可能由组策略或第三方杀毒软件管理）"}
	}
	outStr := string(out)
	if strings.Contains(outStr, "0x1") {
		return &SecurityModule{Name: "defender", Status: "disabled",
			Details: "Windows Defender 实时保护已关闭"}
	}
	return &SecurityModule{Name: "defender", Status: "enforcing",
		Details: "Windows Defender 实时保护已启用，可能拦截 Agent 文件操作"}
}

func detectControlledFolderAccess() *SecurityModule {
	// Controlled Folder Access blocks unauthorized writes to protected folders
	out, err := exec.Command("reg", "query",
		`HKLM\SOFTWARE\Microsoft\Windows Defender\Windows Defender Exploit Guard\Controlled Folder Access`,
		"/v", "EnableControlledFolderAccess").Output()
	if err != nil {
		return nil // Feature not configured
	}
	outStr := string(out)
	if strings.Contains(outStr, "0x1") {
		return &SecurityModule{Name: "controlled_folder_access", Status: "enforcing",
			Details: "受控文件夹访问已启用，Agent 写入受保护目录可能被拦截"}
	}
	return nil
}

func detectWindowsFirewall() *SecurityModule {
	// Check if Windows Firewall is active and might block outbound connections
	out, err := exec.Command("netsh", "advfirewall", "show", "allprofiles", "state").Output()
	if err != nil {
		return nil
	}
	outStr := string(out)
	if strings.Contains(outStr, "ON") {
		return &SecurityModule{Name: "windows_firewall", Status: "enforcing",
			Details: "Windows 防火墙已启用，请确认 Agent 出站连接未被拦截"}
	}
	return nil
}

// ---------- macOS security detection ----------

func detectMacSecurity() []SecurityModule {
	var modules []SecurityModule

	// 1. SIP (System Integrity Protection)
	if m := detectSIP(); m != nil {
		modules = append(modules, *m)
	}
	// 2. TCC (Full Disk Access)
	if m := detectTCC(); m != nil {
		modules = append(modules, *m)
	}

	return modules
}

func detectSIP() *SecurityModule {
	out, err := exec.Command("csrutil", "status").Output()
	if err != nil {
		return &SecurityModule{Name: "sip", Status: "unknown",
			Details: "无法检测 SIP 状态"}
	}
	outStr := strings.TrimSpace(string(out))
	if strings.Contains(outStr, "enabled") {
		return &SecurityModule{Name: "sip", Status: "enforcing",
			Details: "SIP 已启用：" + outStr}
	}
	return &SecurityModule{Name: "sip", Status: "disabled",
		Details: "SIP 已关闭：" + outStr}
}

func detectTCC() *SecurityModule {
	// Check if the Agent has Full Disk Access by probing TCC database readability
	tccPath := "/Library/Application Support/com.apple.TCC"
	if _, err := os.Stat(tccPath); err != nil {
		if os.IsPermission(err) {
			return &SecurityModule{Name: "tcc", Status: "enforcing",
				Details: "TCC 限制：Agent 可能缺少完全磁盘访问权限，部分系统路径不可读"}
		}
		return nil
	}
	return &SecurityModule{Name: "tcc", Status: "permissive",
		Details: "TCC 数据库可访问"}
}
