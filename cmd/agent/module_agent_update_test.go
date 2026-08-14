package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResolveAgentConfigBesideExe(t *testing.T) {
	dir := t.TempDir()
	if got := resolveAgentConfigBesideExe(dir); got != "" {
		t.Fatalf("empty dir → %q", got)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("server: http://x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveAgentConfigBesideExe(dir)
	want, _ := filepath.Abs(cfg)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// The restart helpers relaunch the new binary with --config. Guessing
// "config.yaml beside the exe" is wrong whenever the service was installed with
// an absolute --config pointing elsewhere: the helper then finds nothing, and
// its user-mode path refuses to start an agent without a config at all.
func TestAgentUpdateConfigPathPrefersTheLiveConfig(t *testing.T) {
	prev := agentActiveConfigPath
	t.Cleanup(func() { agentActiveConfigPath = prev })

	exeDir := t.TempDir()
	beside := filepath.Join(exeDir, "config.yaml")
	if err := os.WriteFile(beside, []byte("server: http://x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "aiops.yaml")
	if err := os.WriteFile(elsewhere, []byte("server: http://y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	agentActiveConfigPath = elsewhere
	if got := agentUpdateConfigPath(exeDir); got != elsewhere {
		t.Fatalf("live config ignored: got %q want %q", got, elsewhere)
	}

	// A stale/removed live path must not shadow a perfectly good local config.
	agentActiveConfigPath = filepath.Join(t.TempDir(), "gone.yaml")
	want, _ := filepath.Abs(beside)
	if got := agentUpdateConfigPath(exeDir); got != want {
		t.Fatalf("missing live config did not fall back: got %q want %q", got, want)
	}

	agentActiveConfigPath = ""
	if got := agentUpdateConfigPath(exeDir); got != want {
		t.Fatalf("unset live config did not fall back: got %q want %q", got, want)
	}
}

func TestAgentDistBinaryName(t *testing.T) {
	cases := []struct{ goos, goarch, want string }{
		{"linux", "amd64", "aiops-agent-linux-amd64"},
		{"linux", "arm64", "aiops-agent-linux-arm64"},
		// 国产化 / 嵌入式：uname -m 的写法与 GOARCH 不同，必须先归一化再取产物名，
		// 否则这些平台会落到 "unsupported platform" 而永远收不到自动升级。
		{"linux", "loongarch64", "aiops-agent-linux-loong64"},
		{"linux", "loong64", "aiops-agent-linux-loong64"},
		{"linux", "riscv64", "aiops-agent-linux-riscv64"},
		{"linux", "i686", "aiops-agent-linux-386"},
		{"linux", "386", "aiops-agent-linux-386"},
		{"linux", "armv7l", "aiops-agent-linux-arm"},
		{"darwin", "arm64", "aiops-agent-darwin-arm64"},
		{"windows", "amd64", "aiops-agent.exe"},
		{"windows", "arm64", "aiops-agent-windows-arm64.exe"},
	}
	for _, c := range cases {
		got, err := agentDistBinaryName(c.goos, c.goarch)
		if err != nil || got != c.want {
			t.Fatalf("%s/%s → %q (%v), want %q", c.goos, c.goarch, got, err, c.want)
		}
	}
	for _, c := range []struct{ goos, goarch string }{
		{"linux", "mips64le"}, {"freebsd", "amd64"}, {"darwin", "386"},
	} {
		if got, err := agentDistBinaryName(c.goos, c.goarch); err == nil {
			t.Fatalf("expected error for %s/%s, got %q", c.goos, c.goarch, got)
		}
	}
}

func TestAgentUpdateBinCandidatesWindowsAliases(t *testing.T) {
	if windowsNeedsLegacyAgentBuild() {
		cands := agentUpdateBinCandidates("windows", "amd64", "aiops-agent.exe")
		if len(cands) != 1 || cands[0] != "aiops-agent-windows-amd64-win2012.exe" {
			t.Fatalf("legacy host must ignore modern preferred: %v", cands)
		}
		return
	}
	cands := agentUpdateBinCandidates("windows", "amd64", "aiops-agent-windows-amd64.exe")
	if len(cands) < 2 {
		t.Fatalf("cands=%v", cands)
	}
	if cands[0] != "aiops-agent-windows-amd64.exe" {
		t.Fatalf("preferred first: %v", cands)
	}
	foundExe := false
	for _, c := range cands {
		if c == "aiops-agent.exe" {
			foundExe = true
		}
	}
	if !foundExe {
		t.Fatalf("missing aiops-agent.exe alias: %v", cands)
	}
}

func TestAgentUpdateBinCandidatesLegacyIgnoresModernPreferred(t *testing.T) {
	// Simulate preferred modern PE from an outdated server on a host that needs win2012.
	// windowsNeedsLegacyAgentBuild is OS-dependent; when false, exercise the same
	// filtering by checking the public helper used for legacy-only lists.
	cands := agentUpdateBinCandidates("windows", "amd64", "aiops-agent.exe")
	if windowsNeedsLegacyAgentBuild() {
		if len(cands) != 1 || cands[0] != "aiops-agent-windows-amd64-win2012.exe" {
			t.Fatalf("legacy: %v", cands)
		}
		return
	}
	// On modern Windows, preferred modern name stays first.
	if cands[0] != "aiops-agent.exe" {
		t.Fatalf("modern preferred first: %v", cands)
	}
}

func TestAgentUpdateBinCandidatesEmptyPreferred(t *testing.T) {
	cands := agentUpdateBinCandidates("linux", "amd64", "")
	if len(cands) != 1 || cands[0] != "aiops-agent-linux-amd64" {
		t.Fatalf("empty preferred: %v", cands)
	}
	// filepath.Base("") is "."; must not become a download candidate.
	cands = agentUpdateBinCandidates("linux", "amd64", "   ")
	if len(cands) != 1 || cands[0] != "aiops-agent-linux-amd64" {
		t.Fatalf("whitespace preferred: %v", cands)
	}
}

func TestNormalizeAgentVer(t *testing.T) {
	if normalizeAgentVer("v0.19.3") != "0.19.3" {
		t.Fatal(normalizeAgentVer("v0.19.3"))
	}
}

func TestValidateUpdateServerURL(t *testing.T) {
	if err := validateUpdateServerURL("http://mon.example:8529", []string{"http://mon.example:8529"}); err != nil {
		t.Fatal(err)
	}
	if err := validateUpdateServerURL("https://mon.example:8529", []string{"http://mon.example:8529"}); err != nil {
		t.Fatal("http↔https same host must be allowed:", err)
	}
	if err := validateUpdateServerURL("http://evil.example", []string{"http://mon.example:8529"}); err == nil {
		t.Fatal("expected reject")
	}
	if err := validateUpdateServerURL("ftp://mon.example", nil); err == nil {
		t.Fatal("expected scheme reject")
	}
	// The update payload is a root-privileged binary whose only integrity proof
	// (the .sha256) travels the same channel, so a TLS→plaintext downgrade would
	// hand anyone on-path the whole fleet.
	if err := validateUpdateServerURL("http://mon.example:8529", []string{"https://mon.example:8529"}); err == nil {
		t.Fatal("https→http downgrade must be rejected")
	}
	// A plaintext-only deployment (common on isolated LANs) still works.
	if err := validateUpdateServerURL("http://mon.example:8529", []string{"http://mon.example:8529", "https://other.example"}); err != nil {
		t.Fatal("plaintext-configured target must still be allowed:", err)
	}
}

// The server infers "legacy Windows" from the reported OS string ("...2012...")
// while the agent knows the real kernel version. On a modern kernel the server's
// win2012 hint would otherwise pin the host to the Go 1.20 build line forever.
func TestAgentUpdateBinCandidatesDemotesWin2012HintOnModernKernel(t *testing.T) {
	const win2012 = "aiops-agent-windows-amd64-win2012.exe"
	got := agentUpdateBinCandidates("windows", "amd64", win2012)
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	if got[0] == win2012 && !windowsNeedsLegacyAgentBuild() {
		t.Fatalf("modern kernel must not prefer the win2012 artifact: %v", got)
	}
	found := false
	for _, c := range got {
		if c == win2012 {
			found = true
		}
	}
	if !found {
		t.Fatalf("win2012 must remain a last-resort candidate: %v", got)
	}
}

// 多服务端场景：两块面板各自持有 in-flight 冷却表，可以在同一时刻下发 agent_update。
// 第二次必须立刻退出，而不是与第一次并发跑同一条 download→verify→replace 流水线。
func TestModuleAgentUpdateRejectsConcurrentRun(t *testing.T) {
	agentUpdateRunMu.Lock() // 模拟「另一次升级正在跑」
	defer agentUpdateRunMu.Unlock()

	out, code := moduleAgentUpdate(map[string]string{"server": "http://127.0.0.1:1"}, nil)
	if code == 0 {
		t.Fatalf("并发调用应返回非零退出码，得 %d (%s)", code, out)
	}
	if !strings.Contains(string(out), "already running") {
		t.Fatalf("输出应说明已有升级在跑，得 %q", out)
	}
	// 服务端靠 "agent_update:" 前缀区分「模块自身失败」与「不认识该模块」——
	// 丢了前缀就会误触发 legacy 安装脚本回退路径。
	if !strings.HasPrefix(string(out), "agent_update:") {
		t.Fatalf("输出必须带 agent_update: 前缀，否则触发 legacy 脚本回退，得 %q", out)
	}
}

// 锁释放后必须能再次进入（否则一次并发拒绝就永久堵死自升级）。
func TestModuleAgentUpdateLockReleasedAfterRun(t *testing.T) {
	if !agentUpdateRunMu.TryLock() {
		t.Fatal("锁在测试开始时应是空闲的")
	}
	agentUpdateRunMu.Unlock()
	// server 不在 allowedBases 内 → 立刻失败返回，但必须走完 defer Unlock。
	moduleAgentUpdate(map[string]string{"server": "http://127.0.0.1:1"}, []string{"http://other.invalid"})
	if !agentUpdateRunMu.TryLock() {
		t.Fatal("moduleAgentUpdate 返回后锁未释放")
	}
	agentUpdateRunMu.Unlock()
}

// 暂存文件名带 pid：另一个 agent 进程（手工前台跑 / 安装器重启）不能截断
// 本进程已经校验过 SHA-256 的那份文件。
func TestAgentStagingPathIsPerProcess(t *testing.T) {
	dir := t.TempDir()
	p := agentStagingPath(dir, "new")
	if filepath.Dir(p) != dir {
		t.Fatalf("暂存路径应落在 %q，得 %q", dir, p)
	}
	base := filepath.Base(p)
	if !strings.HasPrefix(base, ".aiops-agent.new.") {
		t.Fatalf("暂存文件名前缀不对: %q", base)
	}
	if !strings.Contains(base, strconv.Itoa(os.Getpid())) {
		t.Fatalf("暂存文件名应含 pid: %q", base)
	}
	if agentStagingPath(dir, "rollback") == p {
		t.Fatal("rollback 与 new 不能共用同一个暂存文件")
	}
}

// 残留清理：进程被杀 / 主机在替换途中重启会留下暂存文件，per-pid 命名下会一次
// 失败攒一个多 MB 的死文件。超过 1 小时的才清，免得误删其它进程正在下载的那份。
func TestSweepStaleAgentStaging(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, ".aiops-agent.new.999999")
	fresh := filepath.Join(dir, ".aiops-agent.new.999998")
	keep := filepath.Join(dir, "aiops-agent")
	for _, p := range []string{old, fresh, keep} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keep, past, past); err != nil {
		t.Fatal(err)
	}

	sweepStaleAgentStaging(dir)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("陈旧暂存文件应被清理")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("1 小时内的暂存文件可能属于在跑的升级，不能删")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("非暂存文件不得被清理")
	}
}
