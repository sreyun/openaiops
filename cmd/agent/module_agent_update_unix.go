//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// agentReplaceAndRestart replaces the running binary with staging (Linux allows
// renaming over a busy executable) then schedules a detached service restart.
func agentReplaceAndRestart(exe, staging, cfgPath string) error {
	// Prefer atomic rename on the same filesystem; never write-through a live path
	// via copyFile (ETXTBSY / partial write risk).
	if err := os.Rename(staging, exe); err != nil {
		sameDirStaging := agentStagingPath(filepath.Dir(exe), "replace")
		if staging != sameDirStaging {
			if err2 := copyFile(staging, sameDirStaging); err2 != nil {
				return fmt.Errorf("replace binary: stage copy: %v (rename: %v)", err2, err)
			}
			_ = os.Remove(staging)
			staging = sameDirStaging
		}
		if err2 := os.Rename(staging, exe); err2 != nil {
			return fmt.Errorf("replace binary: %v / %v", err, err2)
		}
	}
	_ = os.Chmod(exe, 0o755)
	if strings.TrimSpace(cfgPath) == "" {
		cfgPath = resolveAgentConfigBesideExe(filepath.Dir(exe))
	}
	return scheduleAgentRestart(exe, cfgPath)
}

func scheduleAgentRestart(exe, cfgPath string) error {
	dir := filepath.Dir(exe)
	switch runtime.GOOS {
	case "linux":
		// Critical: the agent (and this helper, if spawned without nsenter) may run
		// inside a systemd ProtectSystem mount namespace where /etc is read-only.
		// Fresh curl|bash install works because it runs outside that ns; auto-upgrade
		// must nsenter into PID 1 before --install-service / unit rewrite.
		return startDetachedShell(buildLinuxAgentRestartScript(exe, dir, cfgPath, detectLinuxAgentUnit()))
	case "darwin":
		return startDetachedShell(buildDarwinAgentRestartScript(exe, dir, cfgPath))
	default:
		return fmt.Errorf("restart not supported on %s", runtime.GOOS)
	}
}

// startDetachedShell runs the restart helper so that neither the service
// manager nor the agent's own exit can take it down mid-swap.
//
// systemd-run 是首选：助手落进**独立的临时 unit（自己的 cgroup）**，`systemctl
// stop/restart aiops-agent` 再也波及不到它。没有 systemd-run 时退回 setsid +
// 脚本内的 cgroup 逃逸（见 cgroupEscapeSh），Windows 侧走 DETACHED_PROCESS。
func startDetachedShell(script string) error {
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		if err := startViaSystemdRun(script); err == nil {
			return nil
		}
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start restart helper: %w", err)
	}
	return nil
}

// startViaSystemdRun launches the helper as a transient systemd unit, i.e. in a
// cgroup of its own. Returns an error when systemd-run is unavailable or the
// transient unit could not be started, so the caller can fall back.
func startViaSystemdRun(script string) error {
	bin, err := exec.LookPath("systemd-run")
	if err != nil {
		return err
	}
	unit := fmt.Sprintf("aiops-agent-update-%d", os.Getpid())
	cmd := exec.Command(bin,
		"--quiet", "--collect",
		"--unit="+unit,
		"--description=AIOps Agent self-update helper",
		"--property=Type=oneshot",
		"--property=KillMode=process",
		"--property=TimeoutStartSec=600",
		"/bin/sh", "-c", script,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	// systemd-run itself returns as soon as the transient unit is queued.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemd-run: %w", err)
	}
	return nil
}

// knownAgentUnits are the systemd units this binary can legitimately be running
// under. aiops-relay is the gateway install (install-relay.sh): same binary,
// same self-update path, different unit — restarting "aiops-agent" on a gateway
// restarts nothing, so the new binary is staged and never activated, and the
// panel sees the version refuse to move for reasons no log explains.
var knownAgentUnits = []string{"aiops-agent", "aiops-monitor-agent", "aiops-relay"}

func detectLinuxAgentUnit() string {
	return pickLinuxAgentUnit(selfSystemdUnit(), func(u string) bool {
		out, err := exec.Command("systemctl", "is-active", u).CombinedOutput()
		return err == nil && strings.TrimSpace(string(out)) == "active"
	}, func(u string) bool {
		_, err := os.Stat("/etc/systemd/system/" + u + ".service")
		return err == nil
	})
}

// pickLinuxAgentUnit chooses the unit to restart after a swap.
//
// self 优先于探测：一台机器完全可能同时装着 aiops-agent 与 aiops-relay（网关机也想被
// 监控），此时 `systemctl is-active aiops-agent` 会痛快地回答 active——回答的却是另一个
// 进程。要重启的只能是**我们自己所在的那个 unit**。
func pickLinuxAgentUnit(self string, isActive, unitFileExists func(string) bool) string {
	if self != "" {
		for _, u := range knownAgentUnits {
			if self == u {
				return u
			}
		}
	}
	for _, u := range knownAgentUnits {
		if isActive(u) {
			return u
		}
	}
	for _, u := range knownAgentUnits {
		if unitFileExists(u) {
			return u
		}
	}
	return "aiops-agent"
}

// selfSystemdUnit returns the *.service this process belongs to, or "" when it
// is not running under systemd. Parsed from /proc/self/cgroup, which carries the
// unit path for both cgroup v1 (name=systemd hierarchy) and v2 (unified).
func selfSystemdUnit() string {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return systemdUnitFromCgroup(string(b))
}

func systemdUnitFromCgroup(text string) string {
	for _, line := range strings.Split(text, "\n") {
		// "0::/system.slice/aiops-agent.service" | "1:name=systemd:/system.slice/…"
		idx := strings.LastIndex(line, "/")
		if idx < 0 {
			continue
		}
		leaf := strings.TrimSpace(line[idx+1:])
		if strings.HasSuffix(leaf, ".service") {
			return strings.TrimSuffix(leaf, ".service")
		}
	}
	return ""
}

// windowsPowerShellPath is a stub for non-Windows builds (CIM helpers compile
// everywhere but only run under GOOS=windows switches).
func windowsPowerShellPath() string { return "powershell" }
