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

func detectLinuxAgentUnit() string {
	// Prefer the canonical one-liner / current --install-service name.
	for _, u := range []string{"aiops-agent", "aiops-monitor-agent"} {
		out, err := exec.Command("systemctl", "is-active", u).CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			return u
		}
	}
	if _, err := os.Stat("/etc/systemd/system/aiops-agent.service"); err == nil {
		return "aiops-agent"
	}
	if _, err := os.Stat("/etc/systemd/system/aiops-monitor-agent.service"); err == nil {
		return "aiops-monitor-agent"
	}
	return "aiops-agent"
}

// windowsPowerShellPath is a stub for non-Windows builds (CIM helpers compile
// everywhere but only run under GOOS=windows switches).
func windowsPowerShellPath() string { return "powershell" }
