package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"
)

// decodeLegacyWindowsPS extracts the PowerShell body from the -EncodedCommand.
func decodeLegacyWindowsPS(t *testing.T, cmd string) string {
	t.Helper()
	const marker = "-EncodedCommand "
	idx := strings.Index(cmd, marker)
	if idx < 0 {
		t.Fatal("no -EncodedCommand in legacy windows command")
	}
	raw, err := base64.StdEncoding.DecodeString(cmd[idx+len(marker):])
	if err != nil {
		t.Fatal(err)
	}
	u16 := make([]uint16, len(raw)/2)
	for i := range u16 {
		u16[i] = uint16(raw[i*2]) | uint16(raw[i*2+1])<<8
	}
	return string(utf16.Decode(u16))
}

// TestLegacyWindowsUpdateCommandFitsWindowsShellLimit is the regression guard for
// the defect that made Windows fleet updates impossible.
//
// Agent 在 Windows 上执行 exec 命令的方式是 `cmd.exe /c "<整条命令>"`
// （cmd/agent/terminal.go:runShellCommandCtx），cmd.exe 的命令行硬上限是 8191
// 字符，CreateProcessW 的上限是 32767。这条命令曾把整段 PowerShell 内联成
// -EncodedCommand，长度 37,171 字符：既超 cmd.exe 上限 4.5 倍，也超
// CreateProcessW 上限，于是每一次 Windows 兜底升级/救援都在进程创建阶段直接失败。
// 内容层面的测试再多也发现不了这件事——只有长度能。
func TestLegacyWindowsUpdateCommandFitsWindowsShellLimit(t *testing.T) {
	// Worst-case wrapper the agent prepends: PATH repair + chcp, see
	// runShellCommandCtx. Measured against a long SystemRoot for headroom.
	const agentWrapperOverhead = 260
	const cmdExeHardLimit = 8191

	for _, tc := range []struct{ server, bin string }{
		{"https://monitoring.some-quite-long-corporate-domain.example.com:8529", "aiops-agent-windows-amd64-win2012.exe"},
		{"http://10.0.0.5:8529", "aiops-agent.exe"},
	} {
		cmd := legacyWindowsAgentUpdateScript(tc.server, tc.bin)
		total := len(cmd) + agentWrapperOverhead
		if total > windowsUpdateBootstrapMaxLen {
			t.Errorf("windows update command is %d chars (+%d wrapper = %d), budget is %d.\n"+
				"Do NOT raise the budget: cmd.exe truncates at %d and CreateProcessW fails past 32767.\n"+
				"Move the new logic into windowsUpdateHelperPS (served over /dl/) instead of inlining it.",
				len(cmd), agentWrapperOverhead, total, windowsUpdateBootstrapMaxLen, cmdExeHardLimit)
		}
		if total > cmdExeHardLimit {
			t.Fatalf("windows update command (%d chars) cannot be executed by cmd.exe at all", total)
		}
	}
}

// The bootstrap pins the helper digest. If the two ever drift apart, every
// Windows update fails the integrity check on the host with no way to notice
// here — so assert they are derived from the same bytes.
func TestWindowsUpdateBootstrapPinsHelperSHA256(t *testing.T) {
	ps := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example:8529", "aiops-agent.exe"))
	want := windowsUpdateHelperSHA256()
	if !strings.Contains(ps, "$H='"+want+"'") {
		t.Fatalf("bootstrap does not pin the served helper digest %s", want)
	}
	sum := sha256.Sum256([]byte(windowsUpdateHelperScript()))
	if hex.EncodeToString(sum[:]) != want {
		t.Fatal("windowsUpdateHelperSHA256 does not describe windowsUpdateHelperScript")
	}
	if !strings.Contains(ps, windowsUpdateHelperPath) {
		t.Fatalf("bootstrap must fetch the helper from %s", windowsUpdateHelperPath)
	}
}

// Windows PowerShell 5.1 reads a BOM-less .ps1 using the system ANSI code page.
// The bootstrap prepends its own assignment header (and the BOM), so any
// non-ASCII byte in the helper body would be mis-decoded on GBK/Latin-1 hosts.
func TestWindowsUpdateHelperIsASCII(t *testing.T) {
	for i, r := range windowsUpdateHelperScript() {
		if r > 127 {
			t.Fatalf("non-ASCII rune %q at offset %d: keep Chinese commentary in the Go source, not in the served script", r, i)
		}
	}
}

func TestAgentUpdateHelperScriptHandlerServesPinnedBody(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleAgentUpdateHelperScript(rec, httptest.NewRequest(http.MethodGet, "/dl/aiops-agent-update.ps1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	sum := sha256.Sum256(rec.Body.Bytes())
	if hex.EncodeToString(sum[:]) != windowsUpdateHelperSHA256() {
		t.Fatal("served helper bytes do not match the digest pinned in the bootstrap")
	}

	// Conditional GET must not answer 304 for a stale digest, or a host that
	// cached a previous release's helper would keep failing the pin check.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dl/aiops-agent-update.ps1", nil)
	req.Header.Set("If-None-Match", `"0000000000000000000000000000000000000000000000000000000000000000"`)
	s.handleAgentUpdateHelperScript(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stale ETag must re-serve the body, got %d", rec.Code)
	}
}

func TestWindowsUpdateHelperUsesInstallService(t *testing.T) {
	ps := windowsUpdateHelperScript()
	for _, want := range []string{"--install-service", "--config", "WorkingDirectory", "hasSvc", "start-agent.vbs"} {
		if !strings.Contains(ps, want) {
			t.Fatalf("windows update helper missing %q", want)
		}
	}
	if strings.Contains(ps, "Start-Process $Exe -WindowStyle Hidden") {
		t.Fatal("helper still starts agent without --config")
	}
	// Path guessing alone cannot find non-standard installs, and under a SYSTEM
	// scheduled task LOCALAPPDATA points at systemprofile.
	if !strings.Contains(ps, "Win32_Service") || !strings.Contains(ps, "PathName") {
		t.Fatal("helper must resolve the agent binary from the service ImagePath")
	}
}

// The bootstrap runs through the agent's exec channel, i.e. as a child inside
// the agent's service Job Object. Stopping the service inline kills this very
// process mid-swap — the exact failure the module path avoids with
// schtasks/CREATE_BREAKAWAY_FROM_JOB. It must only ever hand off.
func TestLegacyWindowsBootstrapDetachesBeforeTouchingTheAgent(t *testing.T) {
	inline := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe"))
	for _, forbidden := range []string{"sc.exe", "Stop-Service", "Stop-Process", "Move-Item", "--install-service"} {
		if strings.Contains(inline, forbidden) {
			t.Fatalf("inline (in-job) bootstrap must not %q — it would kill itself mid-swap", forbidden)
		}
	}
	for _, want := range []string{"Stop-Service", "Move-Item", "--install-service", "rolling back to .bak"} {
		if !strings.Contains(windowsUpdateHelperScript(), want) {
			t.Fatalf("detached helper missing %q", want)
		}
	}
	// Detach ladder: SYSTEM scheduled task → WMI create → cmd start /b.
	for _, want := range []string{"Register-ScheduledTask", "AIOpsAgentLegacyUpdate", "Win32_Process", "start \"\" /b"} {
		if !strings.Contains(inline, want) {
			t.Fatalf("bootstrap missing detach mechanism %q", want)
		}
	}
	// The server keys pending_verify off this exact phrase.
	if !strings.Contains(inline, "legacy agent update ok") {
		t.Fatal("bootstrap must keep the 'legacy agent update ok' success marker")
	}
}

// The helper runs AS the AIOpsAgentLegacyUpdate scheduled task, and
// `schtasks /End /TN <task>` terminates that task's whole process tree. A call
// to it from inside the helper is therefore suicide — and it sat immediately
// before the service stop and the binary swap, so every rescue died there with
// the log ending at "staging --version" and the host still on the old version.
// Clearing a stale instance is the BOOTSTRAP's job, before the task is started.
func TestWindowsUpdateHelperNeverEndsItsOwnScheduledTask(t *testing.T) {
	const ownTask = "AIOpsAgentLegacyUpdate"
	for i, line := range strings.Split(windowsUpdateHelperScript(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, ownTask) {
			continue
		}
		for _, kill := range []string{"/End", "Stop-ScheduledTask", "Unregister-ScheduledTask"} {
			if strings.Contains(trimmed, kill) {
				t.Fatalf("helper line %d terminates the task it is itself running under (%s): %s", i+1, kill, trimmed)
			}
		}
	}
	// Scheduled tasks default to MultipleInstances=IgnoreNew, so a previous run
	// left hanging would make Start-ScheduledTask a silent no-op.
	inline := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe"))
	if !strings.Contains(inline, "/End") || !strings.Contains(inline, "$T") {
		t.Fatal("bootstrap must end a stale instance of its own task before starting a new one")
	}
	endAt := strings.Index(inline, "/End")
	startAt := strings.Index(inline, "Start-ScheduledTask")
	if startAt < 0 || endAt > startAt {
		t.Fatal("the stale-instance /End must come before Start-ScheduledTask, not after")
	}
}

// After the swap the service is stopped and the host is offline until something
// starts it again. A registered service already carries
// '--service --config <abs>' in its ImagePath, so plain start always works —
// gating the only start path on "a config file sits beside the exe" left hosts
// with a brand-new binary they never ran.
func TestWindowsUpdateHelperStartsExistingServiceWithoutConfig(t *testing.T) {
	ps := windowsUpdateHelperScript()
	const guard = "if(-not $ok -and $svcs.Count -gt 0){"
	guardAt := strings.Index(ps, guard)
	if guardAt < 0 {
		t.Fatal("helper must try to start an already-registered service even when no config is known")
	}
	// The plain service start must live under that config-independent guard, not
	// nested inside the "install-service with a config" branch above it.
	startAt := strings.Index(ps, `@('start',$n)`)
	if startAt < 0 || startAt < guardAt {
		t.Fatal("sc.exe start must sit under the config-independent guard")
	}
	if cfgGate := strings.Index(ps, "if($hasSvc -and $Cfg){"); cfgGate < 0 || cfgGate > guardAt {
		t.Fatal("expected the install-service branch to precede the unconditional service start")
	}
	// ...and it must know about installs whose config is not beside the binary.
	if !strings.Contains(ps, "function Get-ConfigFromCommandLine") || !strings.Contains(ps, `'--config\s+`) {
		t.Fatal("helper must read --config out of the service ImagePath, not only guess beside the exe")
	}
}

// Windows PowerShell 5.1 turns native stderr captured via 2>&1 into terminating
// errors under $ErrorActionPreference='Stop'; the agent prints a startup warning,
// so the pre-swap probe aborted the whole update.
func TestWindowsUpdateScriptsNeverMergeNativeStderr(t *testing.T) {
	for name, script := range windowsUpdateScriptsUnderTest(t) {
		for i, line := range strings.Split(script, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if strings.Contains(line, "2>&1") {
				t.Errorf("%s line %d merges native stderr under EAP=Stop: %s", name, i+1, line)
			}
		}
	}
	ps := windowsUpdateHelperScript()
	if !strings.Contains(ps, "function Invoke-Native") {
		t.Fatal("helper must route native commands through Invoke-Native")
	}
	if !strings.Contains(ps, "staging not runnable (exit=") {
		t.Fatal("staging probe must be judged by exit code")
	}
}

// A leftover --desktop-worker is the same binary with the same process name; if
// it counted as a live agent, a failed swap would never roll back.
func TestWindowsUpdateHelperIgnoresDesktopWorker(t *testing.T) {
	if !strings.Contains(windowsUpdateHelperScript(), "--desktop-worker") {
		t.Fatal("Test-Running must exclude the desktop worker")
	}
}

func TestLegacyUnixAgentUpdateScriptIgnoresDesktopWorker(t *testing.T) {
	for _, darwin := range []bool{false, true} {
		sh := legacyUnixAgentUpdateScript("https://mon.example", "aiops-agent-linux-amd64", darwin)
		if !strings.Contains(sh, "--desktop-worker") {
			t.Fatalf("legacy unix script (darwin=%v) must exclude the desktop worker from liveness checks", darwin)
		}
	}
}

func TestLegacyUnixAgentUpdateScriptPrefersInstallService(t *testing.T) {
	sh := legacyUnixAgentUpdateScript("http://mon.example:8529", "aiops-agent-linux-amd64", false)
	if !strings.Contains(sh, "--install-service") {
		t.Fatal("linux legacy restart missing --install-service")
	}
	if !strings.Contains(sh, "--config") {
		t.Fatal("linux legacy restart missing --config")
	}
}

// windowsUpdateScriptsUnderTest returns every generated Windows PowerShell body,
// decoded where it ships base64 — assert on the decoded text, or checks silently
// pass against base64 noise.
func windowsUpdateScriptsUnderTest(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"bootstrap": decodeLegacyWindowsPS(t,
			legacyWindowsAgentUpdateScript("https://mon.example:8529", "aiops-agent.exe")),
		"windowsUpdateHelperPS": windowsUpdateHelperScript(),
	}
}

// psAutomaticVarParamsSrv mirrors the agent-side guard: a param() that reuses a
// PowerShell automatic variable name is silently clobbered to empty on every
// call, so "& $File @Args" degenerates into running the target with no arguments.
// 这条缺陷曾同时命中 module helper 和这里的 legacy 兜底脚本 —— 两条路径一起坏掉，
// Windows 机群的自动升级因此没有任何逃生口。
func psAutomaticVarParamsSrv(script string) []string {
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

func TestWindowsUpdateScriptsNeverDeclareAutomaticVarAsParam(t *testing.T) {
	for name, script := range windowsUpdateScriptsUnderTest(t) {
		if hits := psAutomaticVarParamsSrv(script); len(hits) > 0 {
			t.Errorf("%s: PowerShell automatic variables used as parameter names (clobbered to empty on every call):\n  %s",
				name, strings.Join(hits, "\n  "))
		}
	}
}

func TestWindowsUpdateHelperBoundsVersionProbe(t *testing.T) {
	ps := windowsUpdateHelperScript()
	if !strings.Contains(ps, "Invoke-VersionProbe") {
		t.Error("--version must go through the bounded Invoke-VersionProbe")
	}
	for i, line := range strings.Split(ps, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "Invoke-Native") && strings.Contains(trimmed, "--version") {
			t.Errorf("line %d runs --version through the unbounded Invoke-Native: %s", i+1, trimmed)
		}
	}
}
