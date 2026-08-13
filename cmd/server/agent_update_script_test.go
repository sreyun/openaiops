package main

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestLegacyWindowsAgentUpdateScriptUsesInstallService(t *testing.T) {
	cmd := legacyWindowsAgentUpdateScript("http://mon.example:8529", "aiops-agent.exe")
	const marker = "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand "
	idx := strings.Index(cmd, marker)
	if idx < 0 {
		t.Fatalf("expected absolute powershell.exe EncodedCommand, got: %s", cmd[:min(120, len(cmd))])
	}
	if !strings.Contains(cmd, `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`) {
		t.Fatal("legacy windows update must use absolute powershell path")
	}
	raw, err := base64.StdEncoding.DecodeString(cmd[idx+len(marker):])
	if err != nil {
		t.Fatal(err)
	}
	u16 := make([]uint16, len(raw)/2)
	for i := range u16 {
		u16[i] = uint16(raw[i*2]) | uint16(raw[i*2+1])<<8
	}
	ps := string(utf16.Decode(u16))
	for _, want := range []string{"--install-service", "--config", "WorkingDirectory", "aiops-agent-windows-amd64-win2012", "hasSvc", "start-agent.vbs"} {
		if !strings.Contains(ps, want) {
			t.Fatalf("legacy windows script missing %q", want)
		}
	}
	if strings.Contains(ps, "Start-Process $Exe -WindowStyle Hidden") {
		t.Fatal("legacy script still starts agent without --config")
	}
}

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

// The legacy path runs through the agent's exec channel, i.e. as a child inside
// the agent's service Job Object. Stopping the service inline kills this very
// process mid-swap — the exact failure the module path avoids with
// schtasks/CREATE_BREAKAWAY_FROM_JOB. It must hand off to a detached helper.
func TestLegacyWindowsAgentUpdateDetachesBeforeStoppingService(t *testing.T) {
	ps := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe"))

	// Split the generated script into the part that executes inside the agent's
	// job object and the here-string body that only ever runs detached.
	pre, rest, found := strings.Cut(ps, "$body=@'")
	if !found {
		t.Fatal("legacy script must embed the stop/swap/restart body as a here-string helper")
	}
	helper, post, found := strings.Cut(rest, "\n'@")
	if !found {
		t.Fatal("here-string helper is not terminated")
	}
	inline := pre + post
	for _, forbidden := range []string{"sc.exe", "Stop-Service", "Stop-Process", "Move-Item", "Copy-Item -Force -LiteralPath $New"} {
		if strings.Contains(inline, forbidden) {
			t.Fatalf("inline (in-job) part must not %q — it would kill itself mid-swap", forbidden)
		}
	}
	for _, want := range []string{"Stop-Service", "Move-Item", "--install-service", "rolling back to .bak"} {
		if !strings.Contains(helper, want) {
			t.Fatalf("detached helper missing %q", want)
		}
	}
	// Detach ladder: SYSTEM scheduled task → WMI create → cmd start /b.
	for _, want := range []string{"Register-ScheduledTask", "AIOpsAgentLegacyUpdate", "Win32_Process", "start \"\" /b"} {
		if !strings.Contains(inline, want) {
			t.Fatalf("legacy script missing detach mechanism %q", want)
		}
	}
	// The server keys pending_verify off this exact phrase.
	if !strings.Contains(inline, "legacy agent update ok sha=") {
		t.Fatal("legacy script must keep the 'legacy agent update ok sha=' success marker")
	}
}

// Windows PowerShell 5.1 turns native stderr captured via 2>&1 into terminating
// errors under $ErrorActionPreference='Stop'; the agent prints a startup warning,
// so the pre-swap probe aborted the whole update.
func TestLegacyWindowsAgentUpdateNeverMergesNativeStderr(t *testing.T) {
	ps := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe"))
	for i, line := range strings.Split(ps, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, "2>&1") {
			t.Fatalf("line %d merges native stderr under EAP=Stop: %s", i+1, line)
		}
	}
	if !strings.Contains(ps, "function Invoke-Native") {
		t.Fatal("legacy script must route native commands through Invoke-Native")
	}
	if !strings.Contains(ps, "staging not runnable (exit=") {
		t.Fatal("staging probe must be judged by exit code")
	}
}

// A leftover --desktop-worker is the same binary with the same process name; if
// it counted as a live agent, a failed swap would never roll back.
func TestLegacyWindowsAgentUpdateIgnoresDesktopWorker(t *testing.T) {
	ps := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe"))
	if !strings.Contains(ps, "--desktop-worker") {
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
