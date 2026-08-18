package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"aiops-monitor/shared"
	"time"
	"unicode/utf16"
)

// testPinSHA stands in for the server-computed digest of a dist artifact.
// Written as four repeats so its length is 64 by construction — a 65th nibble
// would make sanitizeSHA256Hex reject it and quietly turn every assertion here
// into "unpinned", which is exactly the state under test.
const testPinSHA = "0123456789abcdef" + "0123456789abcdef" + "0123456789abcdef" + "0123456789abcdef"

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

	// Always measure WITH a pinned digest: that is the production shape (the
	// artifact is on disk next to the server) and the longer of the two.
	for _, tc := range []struct{ server, bin string }{
		{"https://monitoring.some-quite-long-corporate-domain.example.com:8529", "aiops-agent-windows-amd64-win2012.exe"},
		{"http://10.0.0.5:8529", "aiops-agent.exe"},
	} {
		cmd := legacyWindowsAgentUpdateScript(tc.server, tc.bin, testPinSHA)
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
	ps := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example:8529", "aiops-agent.exe", testPinSHA))
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
	inline := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe", testPinSHA))
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
	// Detach ladder: WMI create → SYSTEM scheduled task → launcher .cmd.
	//
	// schtasks.exe replaced the ScheduledTasks cmdlets on purpose: that module does
	// not exist on Server 2008 R2 / 2012 (leaving those hosts with WMI as their only
	// path), and importing it writes a CLIXML progress blob to stderr that buries the
	// one line of this script anybody reads.
	for _, want := range []string{"Win32_Process", "schtasks.exe", "/Create", "/Run", "AIOpsAgentLegacyUpdate"} {
		if !strings.Contains(inline, want) {
			t.Fatalf("bootstrap missing detach mechanism %q", want)
		}
	}
	if strings.Contains(inline, "Register-ScheduledTask") || strings.Contains(inline, "New-ScheduledTaskAction") {
		t.Fatal("ScheduledTasks cmdlets are back: they are absent on 2008R2/2012 and their module import pollutes stderr with CLIXML")
	}
	// WMI must stay ahead of the scheduled task: task scheduler policy defaults
	// (DisallowStartIfOnBatteries) make /Run a silent no-op on battery-backed hosts,
	// and Win32_Process.Create has no such layer.
	if wmiAt, taskAt := strings.Index(inline, "Win32_Process"), strings.Index(inline, "/Create"); wmiAt < 0 || taskAt < 0 || wmiAt > taskAt {
		t.Fatal("WMI must be tried before the scheduled task")
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
	// left hanging would make the /Run a silent no-op.
	inline := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe", testPinSHA))
	if !strings.Contains(inline, "/End") || !strings.Contains(inline, "$T") {
		t.Fatal("bootstrap must end a stale instance of its own task before starting a new one")
	}
	endAt := strings.Index(inline, "/End")
	startAt := strings.Index(inline, "/Run /TN")
	if startAt < 0 || endAt > startAt {
		t.Fatal("the stale-instance /End must come before the /Run, not after")
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
		sh := legacyUnixAgentUpdateScript("https://mon.example", "aiops-agent-linux-amd64", testPinSHA, darwin)
		if !strings.Contains(sh, "--desktop-worker") {
			t.Fatalf("legacy unix script (darwin=%v) must exclude the desktop worker from liveness checks", darwin)
		}
	}
}

func TestLegacyUnixAgentUpdateScriptPrefersInstallService(t *testing.T) {
	sh := legacyUnixAgentUpdateScript("http://mon.example:8529", "aiops-agent-linux-amd64", testPinSHA, false)
	if !strings.Contains(sh, "--install-service") {
		t.Fatal("linux legacy restart missing --install-service")
	}
	if !strings.Contains(sh, "--config") {
		t.Fatal("linux legacy restart missing --config")
	}
}

func TestLegacyUnixAgentUpdateScriptPinsKillModeBeforeRestart(t *testing.T) {
	sh := legacyUnixAgentUpdateScript("https://mon.example", "aiops-agent-linux-amd64", testPinSHA, false)
	sedKill := strings.Index(sh, `s/^KillMode=.*/KillMode=process/`)
	grepKill := strings.Index(sh, `grep -q "^KillMode=process"`)
	startCall := strings.Index(sh, "start_units; then")
	if sedKill < 0 || grepKill < 0 || startCall < 0 {
		t.Fatal("legacy linux helper must pin KillMode=process before restart")
	}
	if !(sedKill < grepKill && grepKill < startCall) {
		t.Fatal("KillMode=process must be written before start_units runs")
	}
}

// windowsUpdateScriptsUnderTest returns every generated Windows PowerShell body,
// decoded where it ships base64 — assert on the decoded text, or checks silently
// pass against base64 noise.
func windowsUpdateScriptsUnderTest(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"bootstrap": decodeLegacyWindowsPS(t,
			legacyWindowsAgentUpdateScript("https://mon.example:8529", "aiops-agent.exe", testPinSHA)),
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

// ---------------------------------------------------------------------------
// TLS / certificate handling in the legacy (server-generated) update path.
//
// 现网面板是 HTTPS + 域名，而兜底路径的下载不走 Agent 的 Go HTTP 客户端——它走主机上的
// PowerShell / curl，因此完全用不上 Agent 配置里的 ca_cert 与 tls_skip_verify。老 Windows
// 的根证书库里没有 ISRG Root X1 一类的新根，私有 CA、TLS 审计代理同理：模块路径能升级，
// 兜底脚本却在第一步下载就失败，而兜底恰恰是模块路径坏掉时唯一的逃生口。
//
// 解法不是"关掉校验"，而是把完整性凭证挪到带外：产物摘要由服务端算好、写进脚本，经
// Agent 已鉴权已验证证书的 exec 通道下发。有了带外摘要，降级重试才不会把机群交出去。
// ---------------------------------------------------------------------------

func TestSanitizeSHA256Hex(t *testing.T) {
	if got := sanitizeSHA256Hex("  " + strings.ToUpper(testPinSHA) + "\n"); got != testPinSHA {
		t.Fatalf("well-formed digest not normalized: %q", got)
	}
	if len(testPinSHA) != 64 {
		t.Fatalf("testPinSHA is %d chars, not a SHA-256 digest", len(testPinSHA))
	}
	for _, bad := range []string{
		"", "abc", testPinSHA + "0", strings.Repeat("z", 64),
		// A digest is interpolated into a single-quoted PowerShell literal and a
		// %q shell string; anything that is not pure hex must become "unpinned".
		"'; iex 'calc", strings.Repeat("a", 63) + "$",
	} {
		if got := sanitizeSHA256Hex(bad); got != "" {
			t.Fatalf("sanitizeSHA256Hex(%q) = %q, want empty", bad, got)
		}
	}
}

// The pin has to reach the helper, or the helper can only trust the checksum it
// downloads over the very connection under suspicion.
func TestLegacyScriptsCarryTheServerPinnedDigest(t *testing.T) {
	inline := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe", testPinSHA))
	if !strings.Contains(inline, "$D='"+testPinSHA+"'") {
		t.Fatal("bootstrap does not carry the server-computed artifact digest")
	}
	if !strings.Contains(inline, `-Sha "'+$D+'"`) {
		t.Fatal("bootstrap does not hand the digest to the helper as -Sha")
	}
	// No digest available (artifact not on this server's disk) must degrade to
	// "unpinned", never to a value the helper can pin to and always fail.
	unpinned := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe", ""))
	if !strings.Contains(unpinned, "$D=''") {
		t.Fatal("missing digest must render as an empty pin")
	}

	sh := legacyUnixAgentUpdateScript("https://mon.example", "aiops-agent-linux-amd64", testPinSHA, false)
	if !strings.Contains(sh, `PINNED="`+testPinSHA+`"`) {
		t.Fatal("unix script does not carry the server-computed artifact digest")
	}
	if !strings.Contains(sh, `EXPECTED="$PINNED"`) {
		t.Fatal("unix script must prefer the pinned digest over the downloaded .sha256")
	}
}

// TLS 1.2 is not in the default SecurityProtocol of .NET < 4.7, and the Tls12
// enum member does not exist on 4.0 — hence the numeric flags. Each flag needs
// its own try: TLS 1.3 (12288) throws on < 4.8 and would otherwise discard 3072.
func TestWindowsUpdateScriptsEnableModernTLS(t *testing.T) {
	for name, script := range windowsUpdateScriptsUnderTest(t) {
		for _, want := range []string{"3072", "12288", "SecurityProtocol"} {
			if !strings.Contains(script, want) {
				t.Errorf("%s does not enable modern TLS (%q missing)", name, want)
			}
		}
	}
}

// The unvalidated retry is the dangerous half of the fix: the payload is a
// binary that runs as LocalSystem on every host in the fleet. It is allowed
// ONLY behind the out-of-band pin, and never for the .sha256 fallback, which
// travels the same connection as the binary it is supposed to vouch for.
func TestWindowsUpdateHelperRelaxesTLSOnlyBehindThePin(t *testing.T) {
	ps := windowsUpdateHelperScript()
	if !strings.Contains(ps, "function Get-Payload") {
		t.Fatal("helper must funnel downloads through Get-Payload")
	}
	relax := strings.Index(ps, "ServerCertificateValidationCallback = { $true }")
	if relax < 0 {
		t.Fatal("helper has no certificate-validation fallback at all")
	}
	guard := strings.Index(ps, "if(-not $Pin){")
	if guard < 0 || guard > relax {
		t.Fatal("the unvalidated retry must sit behind the 'no pin -> throw' guard")
	}
	if restore := strings.Index(ps, "ServerCertificateValidationCallback = $prev"); restore < relax {
		t.Fatal("helper must restore the previous validation callback in finally")
	}
	// A malformed -Sha must degrade to unpinned rather than pin the download to
	// something no artifact can match.
	if !strings.Contains(ps, `[^0-9a-fA-F]`) || !strings.Contains(ps, "$Pin.Length -ne 64") {
		t.Fatal("helper must validate the -Sha argument before treating it as a pin")
	}
	// The .sha256 fallback must stay on a plain, validated WebClient call.
	sumAt := strings.Index(ps, `DownloadString("$Server/dl/$Bin.sha256")`)
	if sumAt < 0 {
		t.Fatal("helper lost the unpinned checksum fallback")
	}
	if strings.Contains(ps[sumAt:], "ServerCertificateValidationCallback") {
		t.Fatal("the .sha256 fallback must never relax certificate validation")
	}
	// $Sha is now a parameter; the SHA256 object it used to name would silently
	// clobber the pin.
	if strings.Contains(ps, "$Sha=[Security.Cryptography.SHA256]::Create()") {
		t.Fatal("the hasher variable still shadows the -Sha parameter")
	}
}

// The bootstrap only ever downloads the helper script, whose digest ($H) is
// baked in by the server and checked right after — so it may retry without
// validation unconditionally. It must still verify the digest afterwards.
func TestWindowsBootstrapFallsBackOnCertFailureButKeepsThePinCheck(t *testing.T) {
	inline := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe", testPinSHA))
	dl := strings.Index(inline, "ServerCertificateValidationCallback")
	if dl < 0 {
		t.Fatal("bootstrap cannot recover from an untrusted certificate chain")
	}
	check := strings.Index(inline, "update helper sha256 mismatch")
	if check < 0 || check < dl {
		t.Fatal("the helper digest must still be verified after the relaxed download")
	}
}

func TestUnixLegacyScriptRelaxesTLSOnlyBehindThePin(t *testing.T) {
	sh := legacyUnixAgentUpdateScript("https://mon.example", "aiops-agent-linux-amd64", testPinSHA, false)
	if !strings.Contains(sh, "curl -fSLk") || !strings.Contains(sh, "--no-check-certificate") {
		t.Fatal("unix script has no fallback for an untrusted certificate chain")
	}
	if !strings.Contains(sh, `if [ "$allow_insecure" != "1" ]; then return 1; fi`) {
		t.Fatal("the insecure retry must be gated on the caller passing allow_insecure")
	}
	// Binary: gated on the pin. Checksum: never.
	if !strings.Contains(sh, `fetch "$SERVER/dl/$BIN" "$NEW" "$ALLOW"`) {
		t.Fatal("binary download must pass the pin-derived ALLOW flag")
	}
	if !strings.Contains(sh, `fetch "$SERVER/dl/$BIN.sha256" ".aiops-agent.sha256" 0`) {
		t.Fatal("the .sha256 fallback must be fetched with the insecure retry disabled")
	}
	if !strings.Contains(sh, `if [ -n "$PINNED" ]; then ALLOW=1; fi`) {
		t.Fatal("ALLOW must be derived from the pin")
	}
	// `set -e` kills the script on any failing AND-OR list, which would skip the
	// fallback entirely — the retries must be written as `if`.
	for i, line := range strings.Split(sh, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "curl ") && strings.Contains(trimmed, "&& return") {
			t.Errorf("line %d: `cmd && return` under set -e aborts before the fallback: %s", i+1, trimmed)
		}
	}
}

// The unix script is generated text that nothing else ever parses before it runs
// as root on a production host. Hand it to /bin/sh -n so a structural mistake
// (an unbalanced if/fi, a stray quote from a future edit) fails here instead of
// halfway through a fleet upgrade.
func TestLegacyUnixAgentUpdateScriptParses(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no /bin/sh on this platform")
	}
	for _, tc := range []struct {
		name   string
		darwin bool
		pin    string
	}{
		{"linux-pinned", false, testPinSHA},
		{"linux-unpinned", false, ""},
		{"darwin-pinned", true, testPinSHA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := legacyUnixAgentUpdateScript("https://mon.example", "aiops-agent-linux-amd64", tc.pin, tc.darwin)
			cmd := exec.Command(sh, "-n")
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generated script is not valid POSIX sh: %v\n%s", err, out)
			}
		})
	}
}

// A failed update must not take down a healthy agent.
//
// Most failures in the helper happen BEFORE the service is touched: unreachable
// server, untrusted certificate, checksum mismatch, staging binary that will not
// run. The agent is still up in all of them. Restarting unconditionally means
// every failed attempt stops a working service and reinstalls it — and when that
// reinstall fails (no admin rights, locked SCM) a harmless "could not download"
// becomes an outage. Restart only when the swap happened or the agent is down.
func TestWindowsUpdateHelperLeavesHealthyAgentAloneOnPreSwapFailure(t *testing.T) {
	ps := windowsUpdateHelperScript()
	const guard = "if($swapped -or -not (Test-Running)){"
	guardAt := strings.Index(ps, guard)
	if guardAt < 0 {
		t.Fatal("failure path must gate the restart on 'we swapped' or 'agent is down'")
	}
	// ...and there must be no unconditional Restart-Agent left in the catch block.
	catchAt := strings.Index(ps, "} catch {\n  Write-Log (\"update failed: \"")
	if catchAt < 0 {
		t.Fatal("could not locate the helper's failure handler")
	}
	if guardAt < catchAt {
		t.Fatal("the guard must live in the failure handler, not before it")
	}
	// The restart call must sit *directly* under the guard. Checking the line in
	// isolation cannot tell guarded from unguarded — it is the same text either way.
	if !strings.Contains(ps, guard+"\n      [void](Restart-Agent)\n") {
		t.Fatal("the restart call is not the guarded branch's body")
	}
	if strings.Count(ps[catchAt:], "[void](Restart-Agent)") != 1 {
		t.Fatal("an unguarded Restart-Agent remains on the failure path")
	}
}

// The job may not be declared done while a host is still walking the verify
// ladder — the UI stops polling at "done", so a late success/failed lands in a
// job nobody is watching and the operator is left with "finished, but this host
// never upgraded".
func TestAgentUpdateJobFinalizeWindowCoversTheVerifyLadder(t *testing.T) {
	ladder := agentUpdateVerifyWindow + // module helper verify
		agentUpdateTimeoutSec*time.Second + // legacy rescue exec
		agentUpdateVerifyWindow // rescue verify
	if agentUpdateJobFinalizeWindow <= ladder {
		t.Fatalf("finalize window %v does not cover the %v verify ladder: a job would be marked done "+
			"while hosts are still pending_verify", agentUpdateJobFinalizeWindow, ladder)
	}
}

// 换版前的探针曾经把「退出码读不出来」当成「二进制不可运行」。现场日志
// （server11，v0.19.98 → v0.19.100，连续五次）证明了后果：
//
//	downloaded aiops-agent.exe sha=<与服务端 pin 一致>
//	update failed: staging not runnable (exit=): v0.19.100
//
// 括号里是空的，冒号后面是探针自己读回来的版本号——它跑起来了、版本也对，却在
// 换版之前被自己挡下，每 6 分钟重来一次，永远升不上去。
func TestLegacyHelperAcceptsProbeWithUnreadableExitCode(t *testing.T) {
	ps := windowsUpdateHelperScript()
	if !strings.Contains(ps, "Test-ProbeRunnable $probe") {
		t.Fatal("换版前的判定必须走 Test-ProbeRunnable，而不是直接比较退出码")
	}
	if strings.Contains(ps, "$probe.ExitCode -ne 0") {
		t.Fatal("退出码读不出来时是 $null，直接与 0 比较会把可用的二进制判死")
	}
}

// 这段探针在服务端救援脚本与 Agent 模块助手里各用一次。曾经是两份逐字副本，于是
// 同一个缺陷同时长在两条升级路径上：模块助手判死之后退到 legacy 救援，救援用同样
// 的判据再判死一次。唯一定义处在 shared，谁再抄一份这条测试就红。
func TestLegacyHelperUsesSharedVersionProbe(t *testing.T) {
	if !strings.Contains(windowsUpdateHelperScript(), shared.WindowsVersionProbePS) {
		t.Fatal("救援脚本必须内嵌 shared.WindowsVersionProbePS，不要另抄一份")
	}
}
