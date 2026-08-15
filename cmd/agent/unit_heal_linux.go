//go:build linux

package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ensureLinuxAgentUnitPrivileges rewrites systemd units that still sandbox the
// agent or run it as a non-root user. Those configs make the remote terminal
// look "read-only" (vim E45 on /etc/*, ProtectHome blocking $HOME, etc.).
// Interactive shells also nsenter into PID 1's mount ns (term_nsenter_linux.go)
// so a leftover sandbox cannot keep /etc RO even before the unit restart lands.
//
// Opt out of User=root escalation with an empty file:
//
//	/etc/aiops-agent/allow-nonroot
func ensureLinuxAgentUnitPrivileges(cfgPath string) {
	allowNonRoot := false
	if _, err := os.Stat("/etc/aiops-agent/allow-nonroot"); err == nil {
		allowNonRoot = true
	}

	if os.Geteuid() != 0 {
		slog.Warn("Agent 以非 root 运行：远程终端无法直接写入 /etc 等系统路径",
			"uid", os.Geteuid(), "diag", termPrivilegeDiag(),
			"hint", "curl … | sudo bash  # 或显式 AIOPS_USER=root")
		if !allowNonRoot {
			trySudoInstallService(cfgPath)
		}
		return
	}

	// ProtectSystem=strict makes /etc RO inside the service mount ns — unit rewrite
	// and drop-in purge must run in PID 1's mount ns (same as auto-upgrade helper).
	if !etcWritable() && !sameMountNS(os.Getpid(), 1) {
		if tryHealViaNsenter(cfgPath) {
			return
		}
	}

	// Drop-ins commonly re-apply ProtectSystem=strict after a main-unit heal.
	for _, name := range []string{"aiops-agent", "aiops-monitor-agent"} {
		purgeUnitDropIns(name)
	}

	healed := false
	needRestart := false
	for _, name := range []string{"aiops-agent", "aiops-monitor-agent"} {
		path := "/etc/systemd/system/" + name + ".service"
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		needFile := linuxUnitNeedsPrivilegeHeal(string(body), allowNonRoot)
		needKill := linuxUnitNeedsKillModeHeal(string(body))
		needEff := systemdEffectiveNeedsHeal(name, allowNonRoot)
		if !needFile && !needKill && !needEff {
			continue
		}
		next, changed := healLinuxUnitBody(string(body), allowNonRoot)
		if !changed && needEff {
			// File already looks unlocked but effective props are still sandboxed
			// (stale drop-in / masked fragment). Force a clean rewrite keeping ExecStart.
			next = forceCleanUnitBody(string(body), allowNonRoot)
			changed = true
		}
		if !changed {
			continue
		}
		if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
			slog.Warn("重写 systemd unit 失败", "unit", name, "err", err)
			continue
		}
		purgeUnitDropIns(name)
		slog.Info("已重写 Agent systemd unit（解除沙箱 / 恢复 root 终端权限 / KillMode=process）",
			"unit", name, "file_heal", needFile, "kill_mode_heal", needKill, "effective_heal", needEff)
		healed = true
		if needFile || needEff {
			needRestart = true
		}
	}

	// Even when the unit file looks fine, a still-running sandboxed process keeps
	// a RO mount namespace until restart. Only bounce when we actually rewrote the
	// unit (or purged drop-ins via a successful write path). Restarting while /etc
	// is still RO and unlock failed just loops without progress — interactive
	// shells rely on nsenter instead.
	//
	// KillMode-only rewrites must NOT restart: mixed/control-group would SIGKILL
	// every Java/xjar child in this cgroup on the way down. daemon-reload is
	// enough — systemd reads KillMode when the next stop job starts.
	if !healed {
		if !allowNonRoot && !etcWritable() {
			slog.Warn("root 下 /etc 仍不可写且未能重写 unit；跳过盲目重启，依赖远程终端 nsenter",
				"diag", termPrivilegeDiag())
		}
		return
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	if !needRestart {
		slog.Info("已将 Agent unit 改为 KillMode=process 并 daemon-reload（不重启，以免误杀终端拉起的业务进程）")
		return
	}
	go func() {
		time.Sleep(800 * time.Millisecond)
		_ = exec.Command("systemctl", "restart", detectLinuxAgentUnit()).Run()
	}()
}

func purgeUnitDropIns(name string) {
	for _, base := range []string{
		"/etc/systemd/system",
		"/run/systemd/system",
		"/lib/systemd/system",
		"/usr/lib/systemd/system",
	} {
		_ = os.RemoveAll(filepath.Join(base, name+".service.d"))
	}
}

// tryHealViaNsenter re-runs --install-service (or a minimal unlock) in the host
// mount namespace so sandboxed agents can still fix their own unit after upgrade.
func tryHealViaNsenter(cfgPath string) bool {
	nsenter, err := exec.LookPath("nsenter")
	if err != nil {
		return false
	}
	if _, err := os.Stat("/proc/1/ns/mnt"); err != nil {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	cfgPath = strings.TrimSpace(cfgPath)
	if cfgPath != "" {
		cmd := exec.Command(nsenter, "-t", "1", "-m", "-u", "-i", "-n", "--",
			exe, "--install-service", "--config", cfgPath)
		if out, err := cmd.CombinedOutput(); err == nil {
			slog.Info("已通过 nsenter --install-service 解锁 Agent unit（脱离 ProtectSystem）")
			os.Exit(0)
		} else {
			slog.Warn("nsenter --install-service 失败，尝试原地解锁 unit",
				"err", err, "out", truncBytes(out, 300))
		}
	}
	// Minimal unlock: purge drop-ins + force Protect*=false on unit files.
	// Keep AmbientCapabilities (SNI/packet) while stripping CapabilityBoundingSet.
	script := `for u in aiops-agent aiops-monitor-agent; do
  rm -rf /etc/systemd/system/${u}.service.d /run/systemd/system/${u}.service.d \
         /lib/systemd/system/${u}.service.d /usr/lib/systemd/system/${u}.service.d 2>/dev/null || true
  f=/etc/systemd/system/${u}.service
  [ -f "$f" ] || continue
  sed -i -e 's/^User=.*/User=root/' -e 's/^Group=.*/Group=root/' \
    -e 's/^ProtectHome=.*/ProtectHome=false/' -e 's/^ProtectSystem=.*/ProtectSystem=false/' \
    -e 's/^PrivateTmp=.*/PrivateTmp=false/' -e 's/^NoNewPrivileges=.*/NoNewPrivileges=false/' \
    -e 's|^Environment=HOME=.*|Environment=HOME=/root|' \
    -e 's|^Environment=USER=.*|Environment=USER=root|' \
    -e 's|^Environment=LOGNAME=.*|Environment=LOGNAME=root|' \
    -e '/^CapabilityBoundingSet=/d' -e '/^ReadWritePaths=/d' -e '/^ReadOnlyPaths=/d' \
    -e '/^InaccessiblePaths=/d' -e '/^TemporaryFileSystem=/d' "$f" 2>/dev/null || true
  grep -q '^ProtectSystem=false' "$f" || echo 'ProtectSystem=false' >> "$f"
  grep -q '^ProtectHome=false' "$f" || echo 'ProtectHome=false' >> "$f"
  grep -q '^User=root' "$f" || echo 'User=root' >> "$f"
  grep -q '^PrivateTmp=false' "$f" || echo 'PrivateTmp=false' >> "$f"
  grep -q '^NoNewPrivileges=false' "$f" || echo 'NoNewPrivileges=false' >> "$f"
done
systemctl daemon-reload
systemctl restart aiops-agent 2>/dev/null || systemctl restart aiops-monitor-agent 2>/dev/null || true`
	cmd := exec.Command(nsenter, "-t", "1", "-m", "--", "sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Warn("nsenter unit unlock 失败", "err", err, "out", truncBytes(out, 300))
		return false
	}
	slog.Info("已通过 nsenter 解锁 Agent unit，进程即将退出并由 systemd 拉起")
	os.Exit(0)
	return true
}

func trySudoInstallService(cfgPath string) {
	cfgPath = strings.TrimSpace(cfgPath)
	if cfgPath == "" {
		return
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		slog.Warn("无法 passwordless sudo 提权重装 Agent；请用 root 重跑安装命令")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command("sudo", "-n", exe, "--install-service", "--config", cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Warn("sudo --install-service 失败", "err", err, "out", truncBytes(out, 400))
		return
	}
	slog.Info("已通过 sudo 重装为 root 服务，当前非 root 进程退出")
	os.Exit(0)
}

func forceCleanUnitBody(body string, allowNonRoot bool) string {
	exe, cfg := "/opt/aiops-agent/aiops-agent", "/opt/aiops-agent/config.yaml"
	hasServiceFlag := false
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "ExecStart=") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(ln, "ExecStart="))
		if len(fields) == 0 {
			continue
		}
		exe = fields[0]
		for i := 1; i < len(fields); i++ {
			if fields[i] == "--service" {
				hasServiceFlag = true
			}
			if fields[i] == "--config" && i+1 < len(fields) {
				cfg = fields[i+1]
			}
		}
	}
	termShell := "/bin/bash"
	if _, err := os.Stat(termShell); err != nil {
		termShell = "/bin/sh"
	}
	user, group, home := "root", "root", "/root"
	if allowNonRoot {
		// Keep existing User/Group/HOME when operator opted out of root.
		for _, ln := range strings.Split(body, "\n") {
			trim := strings.TrimSpace(ln)
			switch {
			case strings.HasPrefix(trim, "User="):
				user = strings.TrimSpace(strings.TrimPrefix(trim, "User="))
			case strings.HasPrefix(trim, "Group="):
				group = strings.TrimSpace(strings.TrimPrefix(trim, "Group="))
			case strings.HasPrefix(trim, "Environment=HOME="):
				home = strings.TrimSpace(strings.TrimPrefix(trim, "Environment=HOME="))
			}
		}
	}
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		home = "/var/tmp"
	}
	execLine := exe
	if hasServiceFlag {
		execLine = fmt.Sprintf("%s --service --config %s", exe, cfg)
	} else {
		execLine = fmt.Sprintf("%s --config %s", exe, cfg)
	}
	wd := filepath.Dir(exe)
	return fmt.Sprintf(`[Unit]
Description=AIOps Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
Environment=SHELL=%s
Environment=HOME=%s
Environment=USER=%s
Environment=LOGNAME=%s
ExecStart=%s
Restart=always
RestartSec=5
KillMode=process
LimitNOFILE=65536
ProtectHome=false
ProtectSystem=false
PrivateTmp=false
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
`, user, group, wd, termShell, home, user, user, execLine)
}

func systemdEffectiveNeedsHeal(unit string, allowNonRoot bool) bool {
	out, err := exec.Command("systemctl", "show", unit+".service",
		"-p", "User", "-p", "ProtectHome", "-p", "ProtectSystem",
		"-p", "PrivateTmp", "-p", "NoNewPrivileges", "-p", "LoadState").Output()
	if err != nil {
		// Retry without .service suffix (some hosts accept either).
		out, err = exec.Command("systemctl", "show", unit,
			"-p", "User", "-p", "ProtectHome", "-p", "ProtectSystem",
			"-p", "PrivateTmp", "-p", "NoNewPrivileges", "-p", "LoadState").Output()
		if err != nil {
			return false
		}
	}
	props := map[string]string{}
	for _, ln := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(ln, "="); ok {
			props[k] = strings.TrimSpace(v)
		}
	}
	if props["LoadState"] != "" && props["LoadState"] != "loaded" {
		return false
	}
	if !allowNonRoot {
		u := props["User"]
		if u != "" && u != "root" && u != "0" {
			return true
		}
	}
	truthy := func(v string) bool {
		switch strings.ToLower(v) {
		case "", "no", "false", "0", "none":
			return false
		default:
			return true
		}
	}
	if truthy(props["ProtectHome"]) || truthy(props["ProtectSystem"]) ||
		truthy(props["PrivateTmp"]) || truthy(props["NoNewPrivileges"]) {
		return true
	}
	return false
}

func healLinuxUnitBody(body string, allowNonRoot bool) (string, bool) {
	if !linuxUnitNeedsPrivilegeHeal(body, allowNonRoot) && !linuxUnitNeedsKillModeHeal(body) {
		return body, false
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines)+8)
	var (
		hasProtectHome   bool
		hasProtectSystem bool
		hasPrivateTmp    bool
		hasNNP           bool
		hasUser          bool
		hasGroup         bool
		hasHOME          bool
		hasUSER          bool
		hasSHELL         bool
		hasKillMode      bool
		inService        bool
	)
	skipPrefixes := []string{
		"CapabilityBoundingSet=",
		"ReadWritePaths=",
		"ReadOnlyPaths=",
		"InaccessiblePaths=",
		"TemporaryFileSystem=",
		"ProtectKernelTunables=",
		"ProtectKernelModules=",
		"ProtectControlGroups=",
		"RestrictSUIDSGID=",
		"SystemCallFilter=",
		"MemoryDenyWriteExecute=",
		"LockPersonality=",
		"RestrictRealtime=",
		"PrivateDevices=",
		"PrivateUsers=",
		"RootDirectory=",
		"RootImage=",
		"MountAPIVFS=",
		"ProtectProc=",
		"ProcSubset=",
	}
	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		if trim == "[Service]" {
			inService = true
			out = append(out, ln)
			continue
		}
		if strings.HasPrefix(trim, "[") && trim != "[Service]" {
			if inService {
				out = appendUnlockDirectives(out, hasProtectHome, hasProtectSystem, hasPrivateTmp, hasNNP, hasUser, hasGroup, hasHOME, hasUSER, hasSHELL, hasKillMode, allowNonRoot)
				inService = false
			}
			out = append(out, ln)
			continue
		}
		if !inService {
			out = append(out, ln)
			continue
		}
		skip := false
		for _, p := range skipPrefixes {
			if strings.HasPrefix(trim, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		switch {
		case strings.HasPrefix(trim, "User="):
			hasUser = true
			if allowNonRoot {
				out = append(out, ln)
			} else {
				out = append(out, "User=root")
			}
		case strings.HasPrefix(trim, "Group="):
			hasGroup = true
			if allowNonRoot {
				out = append(out, ln)
			} else {
				out = append(out, "Group=root")
			}
		case strings.HasPrefix(trim, "ProtectHome="):
			hasProtectHome = true
			out = append(out, "ProtectHome=false")
		case strings.HasPrefix(trim, "ProtectSystem="):
			hasProtectSystem = true
			out = append(out, "ProtectSystem=false")
		case strings.HasPrefix(trim, "PrivateTmp="):
			hasPrivateTmp = true
			out = append(out, "PrivateTmp=false")
		case strings.HasPrefix(trim, "NoNewPrivileges="):
			hasNNP = true
			out = append(out, "NoNewPrivileges=false")
		case strings.HasPrefix(trim, "Environment=HOME="):
			hasHOME = true
			if allowNonRoot {
				out = append(out, ln)
			} else {
				out = append(out, "Environment=HOME=/root")
			}
		case strings.HasPrefix(trim, "Environment=USER="):
			hasUSER = true
			if allowNonRoot {
				out = append(out, ln)
			} else {
				out = append(out, "Environment=USER=root")
			}
		case strings.HasPrefix(trim, "Environment=LOGNAME="):
			if allowNonRoot {
				out = append(out, ln)
			} else {
				out = append(out, "Environment=LOGNAME=root")
			}
		case strings.HasPrefix(trim, "Environment=SHELL="):
			hasSHELL = true
			out = append(out, ln)
		case strings.HasPrefix(trim, "KillMode="):
			hasKillMode = true
			out = append(out, "KillMode=process")
		default:
			out = append(out, ln)
		}
	}
	if inService {
		out = appendUnlockDirectives(out, hasProtectHome, hasProtectSystem, hasPrivateTmp, hasNNP, hasUser, hasGroup, hasHOME, hasUSER, hasSHELL, hasKillMode, allowNonRoot)
	}
	next := strings.Join(out, "\n")
	if !strings.HasSuffix(next, "\n") {
		next += "\n"
	}
	return next, next != body
}

func appendUnlockDirectives(out []string, hasProtectHome, hasProtectSystem, hasPrivateTmp, hasNNP, hasUser, hasGroup, hasHOME, hasUSER, hasSHELL, hasKillMode, allowNonRoot bool) []string {
	if !hasUser && !allowNonRoot {
		out = append(out, "User=root")
	}
	if !hasGroup && !allowNonRoot {
		out = append(out, "Group=root")
	}
	if !hasSHELL {
		sh := "/bin/bash"
		if _, err := os.Stat(sh); err != nil {
			sh = "/bin/sh"
		}
		out = append(out, "Environment=SHELL="+sh)
	}
	if !hasHOME && !allowNonRoot {
		out = append(out, "Environment=HOME=/root")
	}
	if !hasUSER && !allowNonRoot {
		out = append(out, "Environment=USER=root", "Environment=LOGNAME=root")
	}
	if !hasProtectHome {
		out = append(out, "ProtectHome=false")
	}
	if !hasProtectSystem {
		out = append(out, "ProtectSystem=false")
	}
	if !hasPrivateTmp {
		out = append(out, "PrivateTmp=false")
	}
	if !hasNNP {
		out = append(out, "NoNewPrivileges=false")
	}
	if !hasKillMode {
		out = append(out, "KillMode=process")
	}
	return out
}

// linuxUnitNeedsKillModeHeal is true when stop/restart would still tear down
// the whole cgroup (default control-group, or the old mixed setting).
func linuxUnitNeedsKillModeHeal(body string) bool {
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "KillMode=") {
			return !strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(ln, "KillMode=")), "process")
		}
	}
	return true
}

func linuxUnitNeedsPrivilegeHeal(body string, allowNonRoot bool) bool {
	checks := []string{
		"ProtectHome=true", "ProtectHome=read-only", "ProtectHome=yes",
		"ProtectSystem=strict", "ProtectSystem=full", "ProtectSystem=true", "ProtectSystem=yes",
		"PrivateTmp=true", "PrivateTmp=yes", "NoNewPrivileges=true", "NoNewPrivileges=yes",
		"CapabilityBoundingSet=", "ReadWritePaths=", "ReadOnlyPaths=",
		"InaccessiblePaths=", "TemporaryFileSystem=",
		"PrivateDevices=", "PrivateUsers=", "RootDirectory=",
	}
	for _, c := range checks {
		if strings.Contains(body, c) {
			return true
		}
	}
	if !strings.Contains(body, "ProtectHome=false") || !strings.Contains(body, "ProtectSystem=false") {
		return true
	}
	if !allowNonRoot {
		for _, ln := range strings.Split(body, "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "User=") {
				u := strings.TrimSpace(strings.TrimPrefix(ln, "User="))
				if u != "" && u != "root" && u != "0" {
					return true
				}
			}
		}
	}
	return false
}
