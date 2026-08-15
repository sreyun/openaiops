//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	// CREATE_BREAKAWAY_FROM_JOB — critical: SCM / service Job Objects kill
	// children when the service stops. Without breakaway the update helper dies
	// with the agent and the binary never swaps (classic "Windows no auto-update").
	// createNoWindow / createBreakawayJob also declared in desktop_session_windows.go.
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// agentReplaceAndRestart cannot overwrite a running Windows PE. Stage stays as
// .new; a detached helper (prefer SYSTEM scheduled task, else breakaway process)
// stops the service/process, swaps files, and brings the agent back via
// --install-service or user-mode VBS/schtasks/--config.
func agentReplaceAndRestart(exe, staging, cfgPath string) error {
	ensureWindowsProcessPath()
	dir := filepath.Dir(exe)
	if strings.TrimSpace(cfgPath) == "" {
		cfgPath = resolveAgentConfigBesideExe(dir)
	}

	// Prefer SYSTEM-readable, space-free work dir. Per-user TEMP with spaces
	// breaks Scheduled Task -Argument quoting; SYSTEM often cannot read it.
	workDir := windowsUpdateWorkDir()
	_ = os.MkdirAll(workDir, 0o755)
	helper := filepath.Join(workDir, "aiops-agent-update-helper.ps1")
	logPath := filepath.Join(workDir, "aiops-agent-update.log")
	resultPath := filepath.Join(dir, "aiops-agent-update.result")
	altResult := filepath.Join(workDir, "aiops-agent-update.result")
	_ = os.Remove(logPath)
	_ = os.Remove(altResult)

	script := buildWindowsUpdateHelperScript(exe, staging, cfgPath, logPath, resultPath, altResult)
	if err := os.WriteFile(helper, []byte(script), 0o644); err != nil {
		return fmt.Errorf("write helper: %w", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "aiops-agent-update-helper.ps1"), []byte(script), 0o644)

	ps := windowsPowerShellPath()
	// -Command & 'path' survives spaces better than -File for Task Scheduler.
	psArgs := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", "& '" + strings.ReplaceAll(helper, "'", "''") + "'"}

	scheduled := false
	if err := scheduleWindowsUpdateTask(ps, psArgs); err == nil {
		scheduled = true
		_ = os.WriteFile(altResult, []byte("scheduled "+time.Now().Format(time.RFC3339)), 0o644)
	}
	// Task registration ≠ helper running. Verify, else fall through to breakaway.
	if scheduled && waitWindowsUpdateHelperAlive(logPath, altResult, resultPath, 6*time.Second) {
		return nil
	}
	if scheduled {
		_ = exec.Command(filepath.Join(windowsSystemRoot(), "System32", "schtasks.exe"),
			"/Delete", "/TN", "AIOpsAgentSelfUpdate", "/F").Run()
	}

	if err := startWindowsBreakaway(ps, psArgs, workDir); err != nil {
		if err2 := startWindowsCmdStart(ps, helper, workDir); err2 != nil {
			return fmt.Errorf("start update helper: schtasks/breakaway/cmd all failed: %v / %v", err, err2)
		}
	}
	_ = waitWindowsUpdateHelperAlive(logPath, altResult, resultPath, 4*time.Second)
	return nil
}

func windowsUpdateWorkDir() string {
	for _, d := range []string{
		filepath.Join(os.Getenv("ProgramData"), "aiops-agent-update"),
		filepath.Join(windowsSystemRoot(), "Temp", "aiops-agent-update"),
		filepath.Join(os.TempDir(), "aiops-agent-update"),
	} {
		if strings.TrimSpace(d) == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			continue
		}
		probe := filepath.Join(d, ".w")
		if err := os.WriteFile(probe, []byte("1"), 0o644); err != nil {
			continue
		}
		_ = os.Remove(probe)
		return d
	}
	return filepath.Join(os.TempDir(), "aiops-agent-update")
}

// helperProgressMarker reports whether a helper log/result file already proves
// the helper is running. These are the first things the helper writes.
func helperProgressMarker(body string) bool {
	return strings.Contains(body, "helper start") || strings.HasPrefix(body, "running") ||
		strings.HasPrefix(body, "ok ") || strings.HasPrefix(body, "fail ")
}

// waitWindowsUpdateHelperAlive polls the helper's own log/result files, which is
// nearly free, and falls back to a process probe ONCE at the end of the window.
//
// 进程探测要拉起一个 powershell.exe 去枚举全机进程（Get-CimInstance Win32_Process）：
// 在老机器上光是 PowerShell 启动就要几百毫秒到数秒。原来它写在 200ms 的轮询循环里，
// 于是这个「等 6 秒」实际变成了「连开若干个 PowerShell、每个都全量枚举进程」，而且因为
// 每次探测都阻塞，文件里已经写出的 "helper start" 反而要等好几秒才被看见——恰好发生在
// 主机即将被换二进制、最不该浪费时间和内存的时刻。文件轮询已经能覆盖绝大多数情况，
// 进程探测只作为最后一次兜底。
func waitWindowsUpdateHelperAlive(logPath, altResult, resultPath string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	paths := []string{logPath, resultPath, altResult}
	for time.Now().Before(deadline) {
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil || len(b) == 0 {
				continue
			}
			if helperProgressMarker(string(b)) {
				return true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return windowsUpdateHelperProcessAlive()
}

// windowsUpdateHelperProcessAlive asks Windows whether a helper process exists.
// Expensive (spawns PowerShell, enumerates every process) — call it once, not in
// a loop.
func windowsUpdateHelperProcessAlive() bool {
	out, err := exec.Command(windowsPowerShellPath(), "-NoProfile", "-NonInteractive", "-Command",
		`Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.CommandLine -match 'aiops-agent-update-helper\.ps1' } | Select-Object -First 1 -ExpandProperty ProcessId`).CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func scheduleWindowsUpdateTask(ps string, psArgs []string) error {
	// Prefer Register-ScheduledTask (locale-safe dates) over schtasks /SD.
	// SYSTEM + Highest when elevated; fall back to current-user task.
	argLine := make([]string, 0, len(psArgs))
	for _, a := range psArgs {
		argLine = append(argLine, quoteWinArg(a))
	}
	arguments := strings.Join(argLine, " ")
	task := "AIOpsAgentSelfUpdate"
	psSchedule := fmt.Sprintf(`
$ErrorActionPreference='Stop'
$task='%s'
$exe='%s'
$arg='%s'
Unregister-ScheduledTask -TaskName $task -Confirm:$false -ErrorAction SilentlyContinue | Out-Null
$action = New-ScheduledTaskAction -Execute $exe -Argument $arg
# Far-future ONCE trigger satisfies older Windows; we only Start once (no double /Run).
$trigger = New-ScheduledTaskTrigger -Once -At ((Get-Date).AddYears(10))
$ok = $false
try {
  $prin = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
  Register-ScheduledTask -TaskName $task -Action $action -Trigger $trigger -Principal $prin -Force | Out-Null
  $ok = $true
} catch {
  try {
    Register-ScheduledTask -TaskName $task -Action $action -Trigger $trigger -Force | Out-Null
    $ok = $true
  } catch {
    throw $_
  }
}
if ($ok) {
  Start-ScheduledTask -TaskName $task -ErrorAction Stop
}
`, psSingleQuote(task), psSingleQuote(ps), psSingleQuote(arguments))

	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", psSchedule)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schedule task: %v (%s)", err, truncBytes(out, 300))
	}
	return nil
}

func startWindowsBreakaway(ps string, args []string, dir string) error {
	cmd := exec.Command(ps, args...)
	cmd.Dir = dir
	cmd.Env = enrichWindowsShellEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createBreakawayJob | detachedProcess | createNewProcessGroup | createNoWindow,
		HideWindow:    true,
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("breakaway start (%s): %w", ps, err)
	}
	// Detach from Go's wait; process continues independently.
	go func() { _ = cmd.Process.Release() }()
	return nil
}

func startWindowsCmdStart(ps, helper, dir string) error {
	cmdExe := windowsCmdPath()
	// start "" /b launches a new process not tied to our console/job as tightly.
	line := fmt.Sprintf(`start "" /b "%s" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%s"`, ps, helper)
	cmd := exec.Command(cmdExe, "/c", line)
	cmd.Dir = dir
	cmd.Env = enrichWindowsShellEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createBreakawayJob | createNewProcessGroup | createNoWindow,
		HideWindow:    true,
	}
	return cmd.Start()
}

// windowsPowerShellPath returns an absolute powershell.exe path. LocalSystem
// services often lack System32\WindowsPowerShell on PATH, so bare "powershell"
// fails with "executable file not found in %PATH%".
func windowsPowerShellPath() string {
	root := windowsSystemRoot()
	candidates := []string{
		filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		filepath.Join(root, "SysWOW64", "WindowsPowerShell", "v1.0", "powershell.exe"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath("powershell.exe"); err == nil {
		return p
	}
	if p, err := exec.LookPath("powershell"); err == nil {
		return p
	}
	return candidates[0]
}
