//go:build linux

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Linux always-on daemon: a root systemd service runs metrics/terminal/forward,
// and supervises a remote-desktop worker spawned into the *active graphical
// session* (X11/Wayland) with that session's DISPLAY/XAUTHORITY environment.
//
// Running as root lets the worker attach to any logged-in user's X server (given
// XAUTHORITY) and keeps the persisted host_id readable, so the browser's session
// (keyed by the root agent's host_id) matches the worker's desktop/wait.

const agentServiceName = "aiops-agent"

const systemdUnitPath = "/etc/systemd/system/aiops-agent.service"

// legacyAgentServiceNames includes the historical --install-service name
// (aiops-monitor-agent). Install/upgrade always writes aiops-agent; heal/update
// still detect and purge both. Do NOT include aiops-relay.
var legacyAgentServiceNames = []string{
	"aiops-agent",
	"aiops-monitor-agent",
}

func installAgentService(exePath, cfgPath string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限，请用 sudo 运行 --install-service")
	}
	// Clear leftover drop-ins/duplicate units so a prior hardened install cannot
	// stick — but NEVER `systemctl stop` the unit we are currently running under.
	//
	// stop 会终止整个 unit cgroup：当 --install-service 是被自动升级助手（Agent 的
	// 子进程）调用时，那一 stop 会连调用方一起 SIGKILL，后面的 daemon-reload /
	// enable / restart 全部落空——服务停在"已停止"再没人拉起，机器永久离线。
	// 正确做法：先写单元 + enable，最后用一次 `systemctl restart`（stop+start 是
	// systemd 的同一个 job，即使调用方被杀，start 半程照样完成）。
	purgeAgentUnitDropIns()
	purgeConflictingAgentUnits()
	termShell := "/bin/bash"
	if _, err := os.Stat(termShell); err != nil {
		termShell = "/bin/sh"
	}
	home := "/root"
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		home = "/var/tmp"
	}
	// Remote interactive shell must have full write access (incl. /etc) and
	// sudo/setuid helpers — explicitly disable systemd sandboxing.
	unit := fmt.Sprintf(`[Unit]
Description=AIOps Agent (metrics + remote desktop)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s --service --config %s
Restart=always
RestartSec=5
User=root
Environment=SHELL=%s
Environment=HOME=%s
Environment=USER=root
Environment=LOGNAME=root
# Only signal the agent PID. mixed/control-group SIGKILLs every child in this
# cgroup on stop/restart — including Java/xjar started from the remote
# terminal or a playbook, which is how a routine Agent bounce takes down
# business processes.
KillMode=process
LimitNOFILE=65536
ProtectHome=false
ProtectSystem=false
PrivateTmp=false
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
`, exePath, cfgPath, termShell, home)

	if err := os.WriteFile(systemdUnitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("写入 systemd 单元失败: %w", err)
	}
	if err := runSvcCmd("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload 失败: %w", err)
	}
	if err := runSvcCmd("systemctl", "enable", agentServiceName); err != nil {
		return fmt.Errorf("systemctl enable 失败: %w", err)
	}
	if err := runSvcCmd("systemctl", "restart", agentServiceName); err != nil {
		return fmt.Errorf("systemctl restart 失败: %w", err)
	}
	// Last step on purpose: retiring the unit we are running under kills this
	// very process (cgroup teardown), so nothing may depend on returning here.
	retireSelfLegacyUnit()
	return nil
}

// currentSystemdUnit returns the *.service this process belongs to, or "" when
// it is not running under systemd. One definition only: the update helper needs
// the same answer to decide which unit to restart (see pickLinuxAgentUnit).
func currentSystemdUnit() string { return selfSystemdUnit() }

// purgeAgentUnitDropIns removes *.service.d overrides for every known agent unit
// without touching the running service. Drop-ins are what re-apply
// ProtectSystem=strict after a heal, so they must go on every (re)install.
func purgeAgentUnitDropIns() {
	for _, name := range legacyAgentServiceNames {
		for _, base := range []string{
			"/etc/systemd/system",
			"/lib/systemd/system",
			"/usr/lib/systemd/system",
			"/run/systemd/system",
		} {
			_ = os.RemoveAll(filepath.Join(base, name+".service.d"))
		}
	}
}

// purgeConflictingAgentUnits removes duplicate/legacy agent units so only the
// canonical one survives — skipping the unit this process is running under
// (retired last, see retireSelfLegacyUnit) and the unit about to be written.
func purgeConflictingAgentUnits() {
	self := currentSystemdUnit()
	for _, name := range legacyAgentServiceNames {
		if name == agentServiceName || name == self {
			continue
		}
		removeAgentUnit(name, true)
	}
}

// retireSelfLegacyUnit disables and removes the legacy unit this process runs
// under, once the canonical unit is already installed and started.
func retireSelfLegacyUnit() {
	self := currentSystemdUnit()
	if self == "" || self == agentServiceName {
		return
	}
	known := false
	for _, name := range legacyAgentServiceNames {
		if name == self {
			known = true
			break
		}
	}
	if !known {
		return
	}
	removeAgentUnit(self, true)
	_ = runSvcCmd("systemctl", "daemon-reload")
}

// removeAgentUnit disables one unit and deletes its files. stop=true also stops
// it — only safe for units the caller does not live in.
func removeAgentUnit(name string, stop bool) {
	if stop {
		_ = runSvcCmd("systemctl", "stop", name)
	}
	_ = runSvcCmd("systemctl", "disable", name)
	_ = runSvcCmd("systemctl", "reset-failed", name)
	for _, base := range []string{
		"/etc/systemd/system",
		"/lib/systemd/system",
		"/usr/lib/systemd/system",
		"/run/systemd/system",
	} {
		_ = os.Remove(filepath.Join(base, name+".service"))
		_ = os.RemoveAll(filepath.Join(base, name+".service.d"))
	}
	_ = os.Remove(filepath.Join("/etc/systemd/system/multi-user.target.wants", name+".service"))
}

func uninstallAgentService() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限，请用 sudo 运行 --uninstall-service")
	}
	purgeAgentSystemdUnits()
	_ = runSvcCmd("systemctl", "daemon-reload")
	return nil
}

// purgeAgentSystemdUnits stops/disables every known agent unit and removes both
// the unit file and *.service.d drop-in directories (systemctl edit leftovers).
func purgeAgentSystemdUnits() {
	for _, name := range legacyAgentServiceNames {
		_ = runSvcCmd("systemctl", "stop", name)
		_ = runSvcCmd("systemctl", "disable", name)
		_ = runSvcCmd("systemctl", "reset-failed", name)
		for _, base := range []string{
			"/etc/systemd/system",
			"/lib/systemd/system",
			"/usr/lib/systemd/system",
			"/run/systemd/system",
		} {
			_ = os.Remove(filepath.Join(base, name+".service"))
			_ = os.RemoveAll(filepath.Join(base, name+".service.d"))
		}
		_ = os.Remove(filepath.Join("/etc/systemd/system/multi-user.target.wants", name+".service"))
	}
}

// runAgentAsService is the entry point invoked by systemd (--service). Metrics
// run in-process as root; the desktop channel is delegated to a per-session
// worker so it inherits the logged-in user's graphical environment.
func runAgentAsService(agent *Agent, cfgPath string) error {
	agent.desktopDisabled = true
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法定位可执行文件: %w", err)
	}

	go agent.Run(ctx)
	slog.Info("Agent systemd 守护进程已启动(root)", "config", cfgPath)
	superviseLinuxDesktopWorker(ctx, exe, cfgPath)
	return nil
}

// runDesktopWorker runs ONLY the remote-desktop channel. It expects the active
// session's DISPLAY/XAUTHORITY (and Wayland vars) to already be set in its env
// by the supervisor.
func runDesktopWorker(agent *Agent) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	agent.RunDesktopOnly(ctx)
	return nil
}

// ---- supervisor ---------------------------------------------------------

type linuxDesktopSession struct {
	uid            int
	user           string
	display        string
	xauthority     string
	waylandDisplay string
	xdgRuntimeDir  string
	dbusAddr       string
}

// key identifies a session so we respawn only when it actually changes.
func (s *linuxDesktopSession) key() string {
	return fmt.Sprintf("%d|%s|%s", s.uid, s.display, s.waylandDisplay)
}

func superviseLinuxDesktopWorker(ctx context.Context, exe, cfgPath string) {
	reapLeftoverDesktopWorkers(exe)
	var worker *bgProc
	var curKey string
	stopWorker := func() {
		if worker != nil {
			worker.kill()
			worker = nil
			curKey = ""
		}
	}
	defer stopWorker()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		sess, ok := detectLinuxDesktopSession()
		if ok {
			if worker == nil || !worker.alive() || sess.key() != curKey {
				stopWorker()
				w, err := spawnLinuxDesktopWorker(exe, cfgPath, sess)
				if err != nil {
					slog.Warn("启动 Linux 桌面 worker 失败", "err", err, "user", sess.user, "display", sess.display)
				} else {
					worker = w
					curKey = sess.key()
					slog.Info("已启动图形会话桌面 worker",
						"user", sess.user, "display", sess.display,
						"wayland", sess.waylandDisplay != "")
				}
			}
		} else {
			stopWorker()
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func spawnLinuxDesktopWorker(exe, cfgPath string, s *linuxDesktopSession) (*bgProc, error) {
	args := []string{"--desktop-worker"}
	if cfgPath != "" {
		args = append(args, "--config", cfgPath)
	}
	cmd := exec.Command(exe, args...)
	env := os.Environ()
	setEnvKV(&env, "DISPLAY", s.display)
	setEnvKV(&env, "XAUTHORITY", s.xauthority)
	if s.waylandDisplay != "" {
		setEnvKV(&env, "WAYLAND_DISPLAY", s.waylandDisplay)
	}
	if s.xdgRuntimeDir != "" {
		setEnvKV(&env, "XDG_RUNTIME_DIR", s.xdgRuntimeDir)
	}
	if s.dbusAddr != "" {
		setEnvKV(&env, "DBUS_SESSION_BUS_ADDRESS", s.dbusAddr)
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return newBgProc(cmd), nil
}

// reapLeftoverDesktopWorkers SIGTERMs host --desktop-worker leftovers from a
// previous Agent that died before stopWorker ran (KillMode=process leaves them).
// User GUI apps are not workers and are not touched.
func reapLeftoverDesktopWorkers(selfExe string) {
	self := selfExe
	if resolved, err := filepath.EvalSymlinks(selfExe); err == nil && resolved != "" {
		self = resolved
	}
	selfBase := filepath.Base(self)
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	myPid := os.Getpid()
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || pid == myPid {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || !strings.Contains(string(raw), "--desktop-worker") {
			continue
		}
		exe, err := os.Readlink("/proc/" + e.Name() + "/exe")
		if err != nil {
			continue
		}
		exe = strings.TrimSuffix(exe, " (deleted)")
		if exe != self && filepath.Base(exe) != selfBase {
			continue
		}
		slog.Info("回收残留桌面 worker", "pid", pid, "exe", exe)
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}

// detectLinuxDesktopSession finds the active local graphical session and lifts
// its graphical environment (DISPLAY/XAUTHORITY/…) directly from a live process
// owned by that user — the most reliable cross-distro method.
func detectLinuxDesktopSession() (*linuxDesktopSession, bool) {
	uid, uname, display := activeGraphicalUser()
	if uid < 0 {
		return nil, false
	}
	env := liftGraphicalEnv(uid)
	if display == "" {
		display = env["DISPLAY"]
	}
	wayland := env["WAYLAND_DISPLAY"]
	if display == "" && wayland == "" {
		return nil, false
	}
	xauth := env["XAUTHORITY"]
	xdg := env["XDG_RUNTIME_DIR"]
	if xdg == "" {
		xdg = fmt.Sprintf("/run/user/%d", uid)
	}
	if xauth == "" {
		// Common fallback: the user's home cookie.
		if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && u.HomeDir != "" {
			cand := u.HomeDir + "/.Xauthority"
			if _, e := os.Stat(cand); e == nil {
				xauth = cand
			}
		}
	}
	return &linuxDesktopSession{
		uid: uid, user: uname, display: display, xauthority: xauth,
		waylandDisplay: wayland, xdgRuntimeDir: xdg, dbusAddr: env["DBUS_SESSION_BUS_ADDRESS"],
	}, true
}

// activeGraphicalUser returns (uid, name, display) of the active local seat's
// graphical session via loginctl, falling back to `who`.
func activeGraphicalUser() (int, string, string) {
	if out, err := exec.Command("loginctl", "list-sessions", "--no-legend").Output(); err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			fields := strings.Fields(ln)
			if len(fields) == 0 {
				continue
			}
			props := loginctlSession(fields[0])
			if props["Active"] != "yes" || props["Remote"] == "yes" {
				continue
			}
			if t := props["Type"]; t != "x11" && t != "wayland" {
				continue
			}
			uid, err := strconv.Atoi(props["User"])
			if err != nil {
				continue
			}
			return uid, props["Name"], props["Display"]
		}
	}
	if out, err := exec.Command("who").Output(); err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			if !strings.Contains(ln, "(:") && !strings.Contains(ln, " :0") {
				continue
			}
			fields := strings.Fields(ln)
			if len(fields) == 0 {
				continue
			}
			if u, err := user.Lookup(fields[0]); err == nil {
				if uid, err := strconv.Atoi(u.Uid); err == nil {
					return uid, fields[0], ""
				}
			}
		}
	}
	return -1, "", ""
}

func loginctlSession(sid string) map[string]string {
	props := map[string]string{}
	out, err := exec.Command("loginctl", "show-session", sid,
		"-p", "Active", "-p", "Type", "-p", "Remote", "-p", "User", "-p", "Name", "-p", "Display").Output()
	if err != nil {
		return props
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(ln, "="); ok {
			props[k] = strings.TrimSpace(v)
		}
	}
	return props
}

// liftGraphicalEnv scans /proc for a process owned by uid whose environment
// contains a display, and returns the graphical env vars it needs.
func liftGraphicalEnv(uid int) map[string]string {
	res := map[string]string{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return res
	}
	want := []string{"DISPLAY", "XAUTHORITY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}
	var fallback map[string]string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fi, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			continue
		}
		env := parseEnviron(data)
		if env["DISPLAY"] == "" && env["WAYLAND_DISPLAY"] == "" {
			continue
		}
		picked := map[string]string{}
		for _, k := range want {
			if v := env[k]; v != "" {
				picked[k] = v
			}
		}
		// Prefer a process that also carries XAUTHORITY (needed for x11grab as root).
		if picked["XAUTHORITY"] != "" {
			return picked
		}
		if fallback == nil {
			fallback = picked
		}
	}
	if fallback != nil {
		return fallback
	}
	return res
}

func parseEnviron(data []byte) map[string]string {
	env := map[string]string{}
	for _, kv := range strings.Split(string(data), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}
