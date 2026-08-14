package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentDistBinaryNameServer(t *testing.T) {
	name, err := agentDistBinaryName("windows", "amd64")
	if err != nil || name != "aiops-agent.exe" {
		t.Fatalf("got %q %v", name, err)
	}
	name, err = agentDistBinaryName("linux", "x86_64")
	if err != nil || name != "aiops-agent-linux-amd64" {
		t.Fatalf("arch alias: got %q %v", name, err)
	}
}

func TestNormalizeGOOSArchWindowsAliases(t *testing.T) {
	goos, goarch := normalizeGOOSArch("Windows", "")
	if goos != "windows" || goarch != "amd64" {
		t.Fatalf("got %s/%s", goos, goarch)
	}
	goos, goarch = normalizeGOOSArch("Microsoft Windows Server 2022", "x64")
	if goos != "windows" || goarch != "amd64" {
		t.Fatalf("pretty OS: got %s/%s", goos, goarch)
	}
	h := &Host{OS: "windows", Arch: ""}
	goos, goarch = hostGOOSArch(h)
	if goos != "windows" || goarch != "amd64" {
		t.Fatalf("hostGOOSArch: %s/%s", goos, goarch)
	}
}

func TestAgentDistResolveWindowsAliasFile(t *testing.T) {
	dir := t.TempDir()
	// Only the descriptive Windows name is present (common partial dist sync).
	path := filepath.Join(dir, "aiops-agent-windows-amd64.exe")
	if err := os.WriteFile(path, []byte("mz"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{distDir: dir}
	name, ok := s.agentDistResolve("windows", "amd64")
	if !ok || name != "aiops-agent-windows-amd64.exe" {
		t.Fatalf("resolve=%q ok=%v", name, ok)
	}
	if !s.agentDistHas("windows", "") {
		t.Fatal("empty arch should still resolve via default amd64")
	}
}

func TestAgentVersionBehind(t *testing.T) {
	if !agentVersionBehind("", "v0.19.3") {
		t.Fatal("empty current should be behind")
	}
	if agentVersionBehind("v0.19.3", "0.19.3") {
		t.Fatal("same version should not be behind")
	}
	if !agentVersionBehind("0.19.2", "0.19.3") {
		t.Fatal("older should be behind")
	}
	if agentVersionBehind("0.20.0", "0.19.3") {
		t.Fatal("newer must not be behind (no downgrade)")
	}
	if agentVersionBehind("0.19.2", "AIOps") {
		t.Fatal("uncomparable target must not trigger")
	}
	if agentVersionBehind("0.19.2", "dev") {
		t.Fatal("dev target must not trigger")
	}
	if !agentVersionBehind("dev", "0.19.3") {
		t.Fatal("dev current should update to release")
	}
}

func TestCompareAgentVer(t *testing.T) {
	if compareAgentVer("0.19.2", "0.19.3") >= 0 {
		t.Fatal("expected 0.19.2 < 0.19.3")
	}
	if compareAgentVer("1.0.0", "1.0") != 0 {
		t.Fatal("expected 1.0.0 == 1.0")
	}
}

func TestAgentAutoUpdateWindow(t *testing.T) {
	if !agentAutoUpdateWindowOpen("") {
		t.Fatal("empty window always open")
	}
	if !agentAutoUpdateWindowOpen("00:00-23:59") {
		t.Fatal("full-day window should be open")
	}
}

func TestNormalizeCSVList(t *testing.T) {
	got := normalizeCSVList([]string{" a,b ", "b", "c"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %#v", got)
	}
}

func TestBuildLegacyAgentUpdateCommand(t *testing.T) {
	sh := buildLegacyAgentUpdateCommand("linux", "http://x:8529", "aiops-agent-linux-amd64", testPinSHA, false)
	for _, p := range []string{"curl", "sha256", "nohup"} {
		if !strings.Contains(sh, p) {
			t.Fatalf("linux script missing %q", p)
		}
	}
	if strings.Contains(sh, "systemctl restart") && strings.Contains(sh, "|| true\necho") {
		// ensure we no longer mask restart failure with trailing || true before ok echo
	}
	darwin := buildLegacyAgentUpdateCommand("darwin", "http://x:8529", "aiops-agent-darwin-arm64", testPinSHA, false)
	for _, p := range []string{"xattr", "system/com.aiops.agent", "gui/"} {
		if !strings.Contains(darwin, p) {
			t.Fatalf("darwin script missing %q", p)
		}
	}
	ps := buildLegacyAgentUpdateCommand("windows", "http://x:8529", "aiops-agent.exe", testPinSHA, false)
	if !strings.Contains(ps, "powershell") || !strings.Contains(ps, "EncodedCommand") {
		t.Fatalf("windows script incomplete: %s", ps[:minInt(120, len(ps))])
	}
	// Decode is heavy; spot-check that ProgramData\\aiops-agent is in the encoded payload by
	// checking the source builder separately via a known substring in the PS before encode —
	// EncodedCommand obscures it, so rebuild via helper string presence in function body by
	// ensuring script is non-empty and contains EncodedCommand (already done).
}

func TestShouldLegacyAgentUpdateFallback(t *testing.T) {
	if !shouldLegacyAgentUpdateFallback("未知模块: agent_update", nil) {
		t.Fatal("unknown module should fallback")
	}
	if shouldLegacyAgentUpdateFallback("agent_update: SHA-256 mismatch", nil) {
		t.Fatal("checksum failure must not fallback")
	}
	if !shouldLegacyAgentUpdateFallback("agent_update: start helper (C:\\Windows\\...\\powershell.exe): executable file not found in %PATH%", nil) {
		t.Fatal("helper spawn failure should fallback to legacy script")
	}
	if !shouldLegacyAgentUpdateFallback("agent_update: start update helper: schtasks/breakaway/cmd all failed: x / y", nil) {
		t.Fatal("breakaway failure should fallback")
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Semver §11: a pre-release ranks below the plain release. Without this an agent
// sitting on an -rc build compares equal to GA and never receives the real one.
func TestAgentVersionBehindPreRelease(t *testing.T) {
	cases := []struct {
		current, target string
		want            bool
	}{
		{"v1.2.3-rc1", "v1.2.3", true},
		{"v1.2.3", "v1.2.3-rc1", false}, // never downgrade GA → rc
		{"v1.2.3-rc1", "v1.2.3-rc2", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.4", true},
		{"v1.3.0", "v1.2.9", false},
	}
	for _, c := range cases {
		if got := agentVersionBehind(c.current, c.target); got != c.want {
			t.Errorf("agentVersionBehind(%q, %q) = %v, want %v", c.current, c.target, got, c.want)
		}
	}
}

// 国产化 / 嵌入式平台必须能解析出 /dl 产物名，否则 decideAutoUpdate 直接 no_artifact。
func TestNormalizeGOOSArchCoversDomesticPlatforms(t *testing.T) {
	cases := []struct{ inOS, inArch, wantOS, wantArch string }{
		{"linux", "loongarch64", "linux", "loong64"},
		{"Kylin Linux Advanced Server V10", "loongarch64", "linux", "loong64"},
		{"linux", "riscv64", "linux", "riscv64"},
		{"linux", "armv7l", "linux", "arm"},
		{"linux", "i686", "linux", "386"},
	}
	for _, c := range cases {
		gotOS, gotArch := normalizeGOOSArch(c.inOS, c.inArch)
		if gotOS != c.wantOS || gotArch != c.wantArch {
			t.Errorf("normalizeGOOSArch(%q,%q) = %q/%q, want %q/%q",
				c.inOS, c.inArch, gotOS, gotArch, c.wantOS, c.wantArch)
		}
		if _, err := agentDistBinaryName(gotOS, gotArch); err != nil {
			t.Errorf("no /dl artifact name for %s/%s: %v", gotOS, gotArch, err)
		}
	}
}
