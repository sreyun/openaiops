package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 一台机器的服务 ImagePath 长这样：
//
//	"C:\Program Files\AIOps Agent\aiops-agent.exe" --service --config "C:\Program"
//
// 升级助手用 Start-Process -ArgumentList @('--install-service','--config',$Cfg) 拉起
// Agent，而 Start-Process 把数组用空格拼接、不加任何引号，于是默认安装路径在空格处被
// 切断，Agent 又把这个残缺路径原样写回 ImagePath。此后每次启动都读不到配置、退回
// localhost:8529：服务状态正常、进程活着、二进制是最新的，而主机永远离线。
//
// 下面两条守的是「读」这一侧——就算 ImagePath 已经被写坏，Agent 也要能自己认回配置。

func TestAgentConfigCandidatesLookBesideTheExecutable(t *testing.T) {
	got := agentConfigCandidates()
	if len(got) < 4 {
		t.Fatalf("候选里必须同时有工作目录与 exe 目录两组: %v", got)
	}
	// 前三个仍是相对名：工作目录优先，老部署的行为不能变。
	for i, want := range []string{"config.yaml", "config.yml", "config.json"} {
		if got[i] != want {
			t.Fatalf("候选 %d 应为 %q，实际 %q（工作目录必须先于 exe 目录）", i, want, got[i])
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no os.Executable on this platform")
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	var abs int
	for _, c := range got[3:] {
		if !filepath.IsAbs(c) {
			t.Fatalf("exe 目录那一组必须是绝对路径: %q", c)
		}
		if filepath.Dir(c) != dir {
			t.Fatalf("候选 %q 不在 exe 目录 %q 下", c, dir)
		}
		abs++
	}
	if abs != 3 {
		t.Fatalf("exe 目录下应有 3 个候选，实际 %d", abs)
	}
}

// 配置一个字节都没读到时，运行日志不能锚到工作目录——Windows 服务的工作目录是
// System32，那等于把唯一写着原因的那条记录丢进没人会去看的地方。
func TestAgentLogBaseDirFallsBackToExeDirWhenNoConfigWasRead(t *testing.T) {
	dir := exeDir()
	if dir == "" {
		t.Skip("no os.Executable on this platform")
	}
	if got := agentLogBaseDir("config.yaml", false); got != dir {
		t.Fatalf("读不到配置时日志应写在 exe 目录 %q，实际 %q", dir, got)
	}
	// 读到了配置就仍然写在它旁边——这条路径的行为一个字都不能变。
	cfgDir := t.TempDir()
	cfg := filepath.Join(cfgDir, "config.yaml")
	if got := agentLogBaseDir(cfg, true); got != cfgDir {
		t.Fatalf("读到配置时日志应写在配置目录 %q，实际 %q", cfgDir, got)
	}
}

// 助手脚本里每一处 --config 都必须带引号，否则 'C:\Program Files\...' 会在空格处断开。
// 同一份不变量在 cmd/server 侧也有一条（两条升级路径都会写 ImagePath，守一边不够）。
func TestWindowsHelperQuotesTheConfigPathInStartProcess(t *testing.T) {
	script := buildWindowsUpdateHelperScript(
		`C:\Program Files\AIOps Agent\aiops-agent.exe`,
		`C:\Program Files\AIOps Agent\.aiops-agent.new.42.exe`,
		`C:\Program Files\AIOps Agent\config.yaml`,
		`C:\ProgramData\aiops-agent-update\aiops-agent-update.log`,
		`C:\Program Files\AIOps Agent\aiops-agent-update.result`,
		`C:\ProgramData\aiops-agent-update\aiops-agent-update.result`,
	)
	assertStartProcessQuotesConfig(t, script)
}

func assertStartProcessQuotesConfig(t *testing.T, script string) {
	t.Helper()
	seen := 0
	for _, line := range strings.Split(script, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") || !strings.Contains(code, "-ArgumentList") {
			continue
		}
		if !strings.Contains(code, "'--config'") {
			continue
		}
		seen++
		if !strings.Contains(code, `('"'+$Cfg+'"')`) {
			t.Fatalf("Start-Process 的 --config 没有加引号，带空格的安装路径会被截断:\n%s", code)
		}
	}
	if seen == 0 {
		t.Fatal("没有找到任何传 --config 的 Start-Process —— 断言失效了，去确认脚本结构")
	}
}
