package main

import (
	"strings"
	"testing"
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
