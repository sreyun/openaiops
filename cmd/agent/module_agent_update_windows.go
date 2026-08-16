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

// windowsHelperAliveWindow is how long we wait for a launched helper to prove it
// is running (it writes "helper start" as its very first statement). One shared
// value for BOTH launchers: a cold powershell.exe on an older or AV-scanned host
// routinely needs several seconds before it reaches the first line of a 13 KB
// script, and the scheduled-task path — which pays the highest cold-start cost —
// used to get only half the window the breakaway path got.
const windowsHelperAliveWindow = 12 * time.Second

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
	// 三个文件都必须先清掉，尤其是 resultPath。
	//
	// waitWindowsUpdateHelperAlive 判定「助手已经跑起来」的依据就是这三个文件里出现
	// helper start / running / ok / fail 之一。resultPath 原来不在清理列表里，而助手
	// 成功时会往它写 "ok <时间>" 且从不删除——于是**任何一台成功升级过一次的机器**，
	// 之后每一次升级都会在第一次轮询就读到上一轮留下的 "ok "，立刻认为助手已启动。
	// 计划任务被电池策略挡掉、breakaway 落进不可脱离的 Job、脚本被拦下，全都被这行
	// 陈旧记录掩盖成功，模块照常回报 "restart scheduled"，主机却纹丝不动。
	// 这正是「第一次能升、以后再也升不动」的 Windows 主机的成因。
	// 删之前先把上一轮的证据留下来，随本次模块输出回传给服务端（见 updateDiagnostics）。
	var prev []string
	if t := tailFileForDiagnostics(resultPath, 512); t != "" {
		prev = append(prev, "result: "+t)
	} else if t := tailFileForDiagnostics(altResult, 512); t != "" {
		prev = append(prev, "result: "+t)
	}
	if t := tailFileForDiagnostics(logPath, 2048); t != "" {
		prev = append(prev, "log tail:\n"+t)
	}
	setUpdateDiagnostics(strings.Join(prev, "\n"))

	_ = os.Remove(logPath)
	_ = os.Remove(altResult)
	_ = os.Remove(resultPath)

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
	//
	// 这里必须给满 windowsHelperAliveWindow，而且**看到活迹象就绝不能删任务**。
	// 原来的写法是「等 6 秒，没动静就 schtasks /Delete /F」，两处都错：
	//   - 6 秒是本文件里唯一没跟上的窗口。下面 breakaway 那条路早就因为「老机器上光是
	//     powershell.exe 冷启动就要好几秒」放宽到 12 秒，而计划任务这条恰恰是**冷启动
	//     最慢**的一条（调度器另起进程、无父进程缓存、杀毒软件要扫一遍 13KB 脚本）。
	//   - `/Delete /F` 会**连正在运行的实例一起终止**。于是慢一点的机器上，主路径刚把
	//     PowerShell 拉起来就被自己删掉，只能退到更弱的 breakaway；而 breakaway 在
	//     不允许脱离的 Job 里同样起不来，最后整台机器报「helper 没起来」。
	// 现在：看到 marker 就直接成功返回（任务留着，助手自己会在 finally 里清理）；
	// 只有一整个窗口内毫无动静——即确认它一行都没跑——才删任务，此时删除是安全的，
	// 而且必须删，否则它晚点才触发会和 breakaway 起来的第二个助手抢同一次换版。
	if scheduled {
		if waitWindowsUpdateHelperAlive(logPath, altResult, resultPath, windowsHelperAliveWindow) {
			return nil
		}
		_ = exec.Command(filepath.Join(windowsSystemRoot(), "System32", "schtasks.exe"),
			"/Delete", "/TN", "AIOpsAgentSelfUpdate", "/F").Run()
	}

	var startErrs []string
	if err := startWindowsBreakaway(ps, psArgs, workDir); err != nil {
		startErrs = append(startErrs, "breakaway: "+err.Error())
		if err2 := startWindowsCmdStart(ps, helper, workDir); err2 != nil {
			return fmt.Errorf("start update helper: schtasks/breakaway/cmd all failed: %v / %v", err, err2)
		}
	}
	// 「进程创建成功」≠「助手真的跑起来了」。计划任务被电池策略挡掉、breakaway 落进
	// 一个不允许脱离的 Job、脚本被 AppLocker/受限执行策略拦下——这些都会让 Start 返回
	// 成功而助手一行都没执行。原来这里把探测结果直接丢弃并 return nil，模块于是向服务端
	// 回报「restart scheduled」，服务端把主机挂进 pending_verify，白等满 5 分钟的校验窗
	// 才轮到 legacy 脚本救援。返回错误可以让服务端**在同一次任务里立刻**改走服务端下发的
	// legacy 脚本（shouldLegacyAgentUpdateFallback 认得 "start update helper" 这个前缀），
	// 而那条路径的正文由服务端生成，正是为修这类 Agent 侧缺陷准备的。
	//
	// windowsHelperAliveWindow 而不是 4 秒：老机器上光是 powershell.exe 冷启动就要好几秒，
	// 窗口太短会把正常运行的助手误判成没起来，白白多跑一次 legacy 脚本。助手一进正文就先写
	// "helper start" 与 "running"，这个窗口足够覆盖冷启动。
	if !waitWindowsUpdateHelperAlive(logPath, altResult, resultPath, windowsHelperAliveWindow) {
		detail := "scheduled task did not run and no helper process appeared"
		if len(startErrs) > 0 {
			detail += " (" + strings.Join(startErrs, "; ") + ")"
		}
		return fmt.Errorf("start update helper: %s; see %s", detail, logPath)
	}
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

// scheduleWindowsUpdateTask registers and starts the one-shot helper task.
//
// -Settings 不是可选项，省略它等于接受 New-ScheduledTaskSettingsSet 的默认值，
// 其中三条默认值会让 Windows 升级**无声失败**，而且失败点在我们的日志之外：
//
//   - DisallowStartIfOnBatteries=True：主机靠电池供电时，Start-ScheduledTask 照常
//     返回成功，任务却被调度器直接判定为「不运行」（结果码 0x41325）。Windows 10/11
//     笔记本、平板，以及任何向来宾暴露电池的虚拟机全部中招——升级请求发出去了，
//     助手一次都没跑过，服务端只看到「restart scheduled」然后版本永远不动。
//   - StopIfGoingOnBatteries=True：升级途中拔掉电源，调度器会在**停服务与重启服务
//     之间**把助手杀掉，主机就停在「服务已停止」，比不升级更糟。
//   - MultipleInstances=IgnoreNew：上一轮吊死的实例还挂着「运行中」时，后续每一次
//     Start-ScheduledTask 都静默无操作，这台机器从此永久卡在旧版本。
//
// StopExisting + 事前 /End 是对第三条的双保险；ExecutionTimeLimit 让万一再吊死的
// 实例 30 分钟后自己了结，而不是占着任务槽 72 小时（默认值）。
func scheduleWindowsUpdateTask(ps string, psArgs []string) error {
	argLine := make([]string, 0, len(psArgs))
	for _, a := range psArgs {
		argLine = append(argLine, quoteWinArg(a))
	}
	psSchedule := buildWindowsUpdateTaskScript(windowsSelfUpdateTaskName, ps, strings.Join(argLine, " "))

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
