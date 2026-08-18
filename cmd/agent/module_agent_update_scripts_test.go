package main

import (
	"fmt"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// These tests deliberately carry NO build tag. The self-update restart helpers
// are shell/PowerShell text that no CI runner ever executes (release.yml is
// ubuntu-only), so their invariants have to be asserted from any platform —
// otherwise the Windows and macOS upgrade paths ship completely unverified.

func TestBuildWindowsUpdateHelperScriptPrefersServiceConfig(t *testing.T) {
	script := buildWindowsUpdateHelperScript(
		`C:\Program Files\AIOps Agent\aiops-agent.exe`,
		`C:\Program Files\AIOps Agent\.aiops-agent.new.exe`,
		`C:\Program Files\AIOps Agent\config.yaml`,
		`C:\Users\Public\aiops-agent-update.log`,
		`C:\Program Files\AIOps Agent\aiops-agent-update.result`,
		`C:\Users\Public\aiops-agent-update.result`,
	)
	for _, want := range []string{
		"--install-service",
		"--config",
		"AiopsMonitorAgent",
		"WorkingDirectory",
		"agent failed to restart after binary replace",
		"Restart-AgentUserMode",
		"start-agent.vbs",
		"schtasks.exe",
		"hasService=",
		"AIOpsAgentSelfUpdate",
		"helper start pid=",
		"sc.exe",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("helper script missing %q", want)
		}
	}
	// Legacy bug: bare Start-Process $exe without config broke terminal/desktop.
	if strings.Contains(script, "Start-Process $exe -WindowStyle Hidden") {
		t.Fatal("helper still has bare Start-Process without --config")
	}
	if !strings.Contains(script, "refusing bare Start-Process") {
		t.Fatal("helper must refuse config-less restart")
	}
	if !strings.Contains(script, "staging --version") {
		t.Fatal("helper must probe staging binary before swap")
	}
	if !strings.Contains(script, "Move-Item attempt") {
		t.Fatal("helper must retry Move-Item under AV locks")
	}
	if !strings.Contains(script, "Copy-Item fallback") {
		t.Fatal("helper must fall back to Copy-Item when Move-Item is locked")
	}
	// Pre-swap failures must not restore a leftover .bak over a still-good PE.
	if !strings.Contains(script, "$swapped = $false") || !strings.Contains(script, "$swapped = $true") {
		t.Fatal("helper must track $swapped around Move-Item")
	}
	if !strings.Contains(script, "$swapped -or -not (Test-Path -LiteralPath $exe)") {
		t.Fatal("helper must restore .bak only after swap or when exe is missing")
	}
	if strings.Contains(script, "elseif ((Test-Path -LiteralPath $bak))") {
		t.Fatal("helper must not unconditionally restore .bak whenever it exists")
	}
	if !strings.Contains(script, "aiops-agent-windows-amd64-win2012") {
		t.Fatal("helper must stop/check Win2012 process name")
	}
	// Must not kill the update helper itself when stopping agent processes.
	if !strings.Contains(script, "aiops-agent-update-helper") {
		t.Fatal("helper must exclude update-helper from process kill list")
	}
}

// The helper stops the service before swapping the PE, so whatever it does next
// is the only thing standing between the host and permanent silence. A service
// that is already registered carries "--service --config <abs>" in its
// ImagePath, so plain start is always correct — but the start loop used to be
// nested inside "a config file sits beside the exe", and the user-mode fallback
// below it refuses outright without a config. Installs configured with an
// absolute --config elsewhere therefore ended the update offline, holding a
// brand-new binary they had never run.
func TestWindowsHelperStartsRegisteredServiceWithoutConfig(t *testing.T) {
	script := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\.new.exe`,
		"", `C:\log`, `C:\a\r`, `C:\r2`)
	startAt := strings.Index(script, `@('start',$name)`)
	if startAt < 0 {
		t.Fatal("helper must start an already-registered service")
	}
	gate := strings.Index(script, "if ($hasSvc -and $Cfg -and (Test-Path -LiteralPath $Cfg)) {")
	if gate < 0 {
		t.Fatal("expected the install-service branch to stay gated on a usable config")
	}
	if startAt < gate {
		t.Fatal("unexpected script layout: service start precedes the install-service branch")
	}
	// The start loop must iterate the collected service list at function scope,
	// i.e. outside the config-gated branch.
	loop := strings.Index(script, "foreach ($name in $svcs) {")
	if loop < 0 || loop < gate {
		t.Fatal("service start loop must sit after — and outside — the config-gated branch")
	}
	// Restart-AgentService returns 'service'/'usermode'/'failed' rather than a
	// boolean, so the tail is a guarded call instead of "return (Restart-...".
	// What matters here is unchanged: user mode is reached only after the loop.
	userMode := strings.Index(script, "Restart-AgentUserMode -Exe $Exe")
	if userMode < 0 || userMode < loop {
		t.Fatal("user-mode fallback must be the last resort, after the service start attempt")
	}
	if !strings.Contains(script, "return 'usermode'") {
		t.Fatal("a user-mode start must be reported as its own outcome, not as success")
	}
}

// Windows PowerShell 5.1 converts native-command stderr captured via `2>&1`
// into NativeCommandError records, which $ErrorActionPreference='Stop' promotes
// to a terminating error. The agent prints a config warning on startup, so the
// pre-swap `--version` probe threw on every host and the binary was never
// swapped. Native calls must discard stderr and be judged by exit code.
func TestWindowsHelperNeverMergesNativeStderr(t *testing.T) {
	script := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\.new.exe`,
		`C:\a\config.yaml`, `C:\log`, `C:\a\r`, `C:\r2`)
	for i, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue // the comment explaining this rule may name the operator
		}
		if strings.Contains(line, "2>&1") {
			t.Fatalf("line %d merges native stderr into the success stream under EAP=Stop: %s", i+1, line)
		}
	}
	if !strings.Contains(script, "function Invoke-Native") {
		t.Fatal("helper must route native commands through Invoke-Native")
	}
	if !strings.Contains(script, "$ErrorActionPreference = 'Continue'") {
		t.Fatal("Invoke-Native must relax EAP around the native call")
	}
	if !strings.Contains(script, "staging binary not runnable before swap (exit=") {
		t.Fatal("staging probe must be judged by exit code, not by stderr presence")
	}
}

// A leftover --desktop-worker is the same binary with the same process name in
// its own session; treating it as a live agent would fake a successful upgrade
// and suppress the .bak rollback.
func TestUpdateHelpersIgnoreDesktopWorkerProcess(t *testing.T) {
	win := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\.new.exe`, `C:\a\config.yaml`, `C:\l`, `C:\r`, `C:\r2`)
	if !strings.Contains(win, "--desktop-worker") {
		t.Fatal("windows Test-AgentRunning must exclude the desktop worker")
	}
	for name, script := range map[string]string{
		"linux":  buildLinuxAgentRestartScript("/opt/aiops-agent/aiops-agent", "/opt/aiops-agent", "/opt/aiops-agent/config.yaml", "aiops-agent"),
		"darwin": buildDarwinAgentRestartScript("/opt/aiops-agent/aiops-agent", "/opt/aiops-agent", "/opt/aiops-agent/config.yaml"),
	} {
		if !strings.Contains(script, "agent_proc_alive()") {
			t.Fatalf("%s helper must define agent_proc_alive", name)
		}
		if !strings.Contains(script, "*--desktop-worker*) continue ;;") {
			t.Fatalf("%s helper must skip --desktop-worker processes when probing liveness", name)
		}
		if strings.Contains(script, "agent_alive() {\n  pgrep -x aiops-agent") {
			t.Fatalf("%s helper must not treat a bare pgrep hit as a healthy agent", name)
		}
	}
}

func TestBuildLinuxAgentRestartScript(t *testing.T) {
	script := buildLinuxAgentRestartScript("/opt/aiops-agent/aiops-agent", "/opt/aiops-agent",
		"/opt/aiops-agent/config.yaml", "aiops-agent")
	for _, want := range []string{
		"escape_cgroup",               // must leave the agent's cgroup before restarting
		"nsenter -t 1 -m -u -i -n --", // escape ProtectSystem mount namespace
		"systemctl restart",           // one queued restart job
		"ProtectSystem=false",         // unit unlock
		"rolling back to $BAK",        // watchdog rollback
		"/var/log/aiops-agent-update.log",
		"--install-service",
		"wait_alive",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("linux restart script missing %q", want)
		}
	}
	// `systemctl stop` would tear down the helper's own cgroup mid-swap.
	if strings.Contains(script, "systemctl stop") {
		t.Fatal("linux restart script must never stop the unit (it would kill the helper)")
	}
	if !strings.Contains(script, "EXE='/opt/aiops-agent/aiops-agent'") {
		t.Fatalf("linux restart script did not shell-quote EXE:\n%s", script)
	}
}

// First upgrade onto a still-mixed unit must rewrite KillMode=process and
// daemon-reload BEFORE systemctl restart, otherwise the stop job still
// SIGKILLs the whole cgroup (Java started from the remote terminal).
func TestBuildLinuxAgentRestartScriptPinsKillModeBeforeRestart(t *testing.T) {
	script := buildLinuxAgentRestartScript("/opt/aiops-agent/aiops-agent", "/opt/aiops-agent",
		"/opt/aiops-agent/config.yaml", "aiops-agent")
	unlock := strings.Index(script, "UNLOCK_SH=")
	sedKill := strings.Index(script, `s/^KillMode=.*/KillMode=process/`)
	grepKill := strings.Index(script, `grep -q "^KillMode=process"`)
	restart := strings.Index(script, "systemctl restart")
	if unlock < 0 || sedKill < 0 || grepKill < 0 || restart < 0 {
		t.Fatal("linux restart script must pin KillMode=process inside UNLOCK_SH")
	}
	if !(unlock < sedKill && sedKill < grepKill && grepKill < restart) {
		t.Fatal("KillMode=process must be written and daemon-reloaded before systemctl restart")
	}
}

func TestBuildDarwinAgentRestartScript(t *testing.T) {
	script := buildDarwinAgentRestartScript("/opt/aiops-agent/aiops-agent", "/opt/aiops-agent",
		"/opt/aiops-agent/config.yaml")
	for _, want := range []string{
		"launchctl kickstart -k",
		"system/com.aiops.monitor.agent", // --install-service label
		"system/com.aiops.agent",         // one-click install.sh root LaunchDaemon
		"gui/$UIDN/com.aiops.agent",      // one-click install.sh per-user LaunchAgent
		"com.apple.quarantine",
		"rolling back to $BAK",
		"state = running",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("darwin restart script missing %q", want)
		}
	}
	if !strings.Contains(script, "EXE='/opt/aiops-agent/aiops-agent'") {
		t.Fatalf("darwin restart script did not shell-quote EXE:\n%s", script)
	}
}

// Paths with quotes/spaces must not be able to break out of the generated
// scripts — these strings are interpolated straight into sh / PowerShell.
func TestUpdateScriptQuotingIsInjectionSafe(t *testing.T) {
	evil := `/opt/a'; rm -rf /; echo '`
	script := buildLinuxAgentRestartScript(evil, "/opt", "", "aiops-agent")
	if strings.Contains(script, "rm -rf /; echo") && !strings.Contains(script, `'"'"'`) {
		t.Fatal("shellQuote failed to neutralise embedded single quotes")
	}
	if shellQuote("") != "''" {
		t.Fatal("empty shellQuote must still produce a quoted empty string")
	}
	if psSingleQuote(`a'b`) != `a''b` {
		t.Fatalf("psSingleQuote: %q", psSingleQuote(`a'b`))
	}
	ps := buildWindowsUpdateHelperScript(`C:\a'b\aiops-agent.exe`, `C:\n`, `C:\c`, `C:\l`, `C:\r`, `C:\r2`)
	if !strings.Contains(ps, `C:\a''b\aiops-agent.exe`) {
		t.Fatal("windows helper must double single quotes in interpolated paths")
	}
}

func TestQuoteWinArg(t *testing.T) {
	if quoteWinArg(`C:\a b\c.exe`) != `"C:\a b\c.exe"` {
		t.Fatal(quoteWinArg(`C:\a b\c.exe`))
	}
	if quoteWinArg(`simple`) != `simple` {
		t.Fatal(quoteWinArg(`simple`))
	}
	if quoteWinArg(``) != `""` {
		t.Fatal("empty arg must quote to \"\"")
	}
}

func TestArgsRequestVersion(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--version"}, true},
		{[]string{"-version"}, true},
		{[]string{"--version=true"}, true},
		{[]string{"--version=false"}, false},
		{[]string{"--config", "/etc/a.yaml", "--version"}, true},
		{[]string{"--config", "--version"}, false}, // path literally named --version
		{[]string{"--service"}, false},
		{[]string{}, false},
		{[]string{"--", "--version"}, false},
		{[]string{"--install-service", "--config", "/a.yaml"}, false},
	}
	for _, c := range cases {
		if got := argsRequestVersion(c.args); got != c.want {
			t.Errorf("argsRequestVersion(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// psAutomaticVarParams reports every param() declaration in a PowerShell script
// that reuses the name of an automatic variable.
//
// 这不是风格问题，是一个已经把整个 Windows 机群打瘫过的缺陷：PowerShell 会在每次
// 调用时用「未绑定实参」重新赋值自动变量 $Args，把同名声明参数覆盖成空数组，于是
//
//	function Invoke-Native { param([string]$File,[string[]]$Args) ... & $File @Args }
//	Invoke-Native $new @('--version')
//
// 实际执行的是不带任何参数的 "& $new" —— 把刚下载的 Agent 当守护进程前台拉起，
// 管道永不结束，升级助手在换二进制之前就永久吊死。位置绑定和 -Args 命名绑定都中招。
func psAutomaticVarParams(script string) []string {
	// PowerShell automatic variables that are writable, so declaring them as a
	// parameter silently binds-then-clobbers instead of raising an error.
	auto := []string{"Args", "Input", "This", "PSItem", "Matches", "Error", "Host", "Foreach", "Switch"}
	var hits []string
	for i, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "param(") {
			continue
		}
		for _, name := range auto {
			for _, form := range []string{"$" + name + ",", "$" + name + ")", "$" + name + " ", "$" + name + "="} {
				if strings.Contains(trimmed+" ", form) {
					hits = append(hits, fmt.Sprintf("line %d: $%s — %s", i+1, name, trimmed))
					break
				}
			}
		}
	}
	return hits
}

func TestWindowsHelperNeverDeclaresAutomaticVarAsParam(t *testing.T) {
	script := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\.new.exe`,
		`C:\a\config.yaml`, `C:\log`, `C:\a\r`, `C:\r2`)
	if hits := psAutomaticVarParams(script); len(hits) > 0 {
		t.Fatalf("PowerShell automatic variables used as parameter names (they are clobbered to empty on every call):\n  %s",
			strings.Join(hits, "\n  "))
	}
}

// TestWindowsHelperBoundsVersionProbe pins the second half of the same incident:
// the probe's input is a binary that may not exit at all, so it must never be run
// through an unbounded pipeline capture.
func TestWindowsHelperBoundsVersionProbe(t *testing.T) {
	script := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\.new.exe`,
		`C:\a\config.yaml`, `C:\log`, `C:\a\r`, `C:\r2`)
	if !strings.Contains(script, "function Invoke-VersionProbe") {
		t.Fatal("helper must define a bounded Invoke-VersionProbe")
	}
	if !strings.Contains(script, "WaitForExit($TimeoutSec * 1000)") {
		t.Fatal("Invoke-VersionProbe must wait with a timeout, not block forever")
	}
	if !strings.Contains(script, "$p.Kill()") {
		t.Fatal("Invoke-VersionProbe must kill a binary that ignores --version")
	}
	for i, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "Invoke-Native") && strings.Contains(trimmed, "--version") {
			t.Fatalf("line %d runs --version through the unbounded Invoke-Native: %s", i+1, trimmed)
		}
	}
}

// A failed update must not take down a healthy agent. Nearly every failure in
// this helper happens before the service is touched (staging missing, staging
// --version fails), and there the agent is still up. Restarting anyway means a
// failed attempt stops a working service and reinstalls it — and when that
// reinstall fails (no admin rights, locked SCM) "download failed" turns into
// "host offline".
func TestWindowsHelperLeavesHealthyAgentAloneOnPreSwapFailure(t *testing.T) {
	script := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\.new.exe`,
		`C:\a\config.yaml`, `C:\log`, `C:\a\r`, `C:\r2`)
	const guard = "if ($swapped -or -not (Test-AgentRunning)) {"
	guardAt := strings.Index(script, guard)
	if guardAt < 0 {
		t.Fatal("failure path must gate the restart on 'we swapped' or 'agent is down'")
	}
	catchAt := strings.Index(script, `Write-Log ("update failed: "`)
	if catchAt < 0 {
		t.Fatal("could not locate the helper's failure handler")
	}
	if guardAt < catchAt {
		t.Fatal("the guard must live in the failure handler, not before it")
	}
	// The restart call must sit *directly* under the guard. Checking the line in
	// isolation cannot tell guarded from unguarded — it is the same text either way.
	// The call now keeps its tri-state result (and logs it) instead of discarding
	// it with [void]; the guarding it must sit under is what this test protects.
	guarded := guard + "\n      $rbMode = Restart-AgentService -Exe $exe -Cfg $cfg -Dir $dir\n"
	if !strings.Contains(script, guarded) {
		t.Fatal("the restart call is not the guarded branch's body")
	}
	if strings.Count(script, "Restart-AgentService -Exe $exe -Cfg $cfg -Dir $dir") !=
		strings.Count(script, guarded)+1 {
		// +1 for the success path, which restarts on purpose.
		t.Fatal("an unguarded Restart-AgentService remains on the failure path")
	}
}

// 模块助手与服务端救援脚本共用同一段探针。它曾经把「退出码读不出来」当成「不可运行」，
// 于是两条升级路径会用同一个错判先后把同一次升级判死两遍——见
// docs/superpowers/plans/2026-08-18-windows-agent-update-version-probe.md。
func TestWindowsHelperUsesSharedVersionProbe(t *testing.T) {
	script := buildWindowsUpdateHelperScript(
		`C:\Program Files\AIOps Agent\aiops-agent.exe`,
		`C:\Program Files\AIOps Agent\.aiops-agent.update.exe`,
		`C:\Program Files\AIOps Agent\config.yaml`,
		`C:\ProgramData\aiops-agent-update\aiops-agent-update.log`,
		`C:\ProgramData\aiops-agent-update\aiops-agent-update.result`,
		"",
	)
	if !strings.Contains(script, shared.WindowsVersionProbePS) {
		t.Fatal("模块助手必须内嵌 shared.WindowsVersionProbePS，不要另抄一份")
	}
	if !strings.Contains(script, "Test-ProbeRunnable $probe") {
		t.Fatal("换版前的判定必须走 Test-ProbeRunnable")
	}
	if strings.Contains(script, "$probe.ExitCode -ne 0") {
		t.Fatal("退出码读不出来时是 $null，直接与 0 比较会把可用的二进制判死")
	}
}

// ---- 换版后的成败判据 ----
//
// 这三个测试守的是同一次线上事故：一次发版后全部 Windows Agent 离线，盘上是新
// 二进制、服务却是 Stopped，而控制台记的是「升级成功」。三个缺陷叠出来的：
// 用退出码判 install-service 的成败、用户态兜底被当成成功、成功就不回滚。

// install-service 的成败绝不能看 Start-Process -PassThru 的退出码：在拦截/包装
// 进程创建的主机上（EDR）ExitCode 读出来是 $null，"-eq 0" 对一次完全成功的执行
// 也为假。这和当初误判探针的是同一个缺陷（见 shared.WindowsVersionProbePS）。
func TestWindowsUpdateNeverJudgesInstallServiceByExitCode(t *testing.T) {
	script := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\new.exe`,
		`C:\a\config.yaml`, `C:\a\u.log`, `C:\a\u.result`, `C:\b\u.result`)
	for _, forbidden := range []string{
		"$p -and $p.ExitCode -eq 0",
		"$p.ExitCode -eq 0",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("助手仍在用退出码判定 install-service 的成败: %q", forbidden)
		}
	}
	if !strings.Contains(script, "advisory only") {
		t.Fatal("退出码应降级为日志线索，并在脚本里写明它只是 advisory")
	}
	// 判据必须落在可观测的事实上：服务进入 Running。
	if !strings.Contains(script, "Wait-ServiceState $name 'Running' 45") {
		t.Fatal("install-service 之后必须以服务状态作为判据")
	}
}

// 用户态兜底把二进制拉起来了，但没有服务：进程随会话结束而死、重启后无人拉起。
// 这不能记成升级成功，否则机群会在控制台一片绿的情况下悄悄掉光。
func TestWindowsUpdateReportsUserModeStartAsDegraded(t *testing.T) {
	script := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\new.exe`,
		`C:\a\config.yaml`, `C:\a\u.log`, `C:\a\u.result`, `C:\b\u.result`)
	for _, want := range []string{
		"return 'usermode'",
		"return 'service'",
		"return 'failed'",
		"reason=service-not-running",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("助手缺少三态重启结果 %q", want)
		}
	}
	deg := strings.Index(script, `Write-Result ("degraded `)
	ok := strings.Index(script, `Write-Result ("ok `)
	if deg < 0 || ok < 0 {
		t.Fatal("助手必须能分别写出 degraded 与 ok 两种结果")
	}
	if deg > ok {
		t.Fatal("degraded 分支必须在 ok 之前判定，否则用户态启动仍会被记成成功")
	}
}

// 三态返回值是字符串。PowerShell 里任何非空字符串都为真，所以
// "if (-not (Restart-AgentService ...))" 会把 'failed' 读成成功——必须显式比较。
func TestRestartAgentServiceResultIsComparedAsString(t *testing.T) {
	script := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\new.exe`,
		`C:\a\config.yaml`, `C:\a\u.log`, `C:\a\u.result`, `C:\b\u.result`)
	if strings.Contains(script, "-not (Restart-AgentService") {
		t.Fatal("三态结果被当布尔用：'failed' 是非空字符串，会被判成成功")
	}
	if !strings.Contains(script, "$restartMode -eq 'failed'") {
		t.Fatal("必须显式比较 'failed' 才触发回滚")
	}
}
