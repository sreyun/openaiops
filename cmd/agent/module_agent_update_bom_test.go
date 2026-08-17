package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 助手写 result 文件用的是 PowerShell 5.1 的 -Encoding UTF8，也就是**带 BOM** 的
// UTF-8。判定「助手起来了没有」如果不先剥 BOM，"running"/"ok "/"fail " 这三个标记在
// 任何一台真实 Windows 主机上都永远匹配不到——整套判定退化成只认日志里的
// "helper start"，而那份日志恰恰是最容易写不进去的东西。
func TestHelperProgressMarkerSurvivesUTF8BOM(t *testing.T) {
	for _, body := range []string{
		"running 2026-08-16T20:00:00.000+08:00",
		"ok 2026-08-16T20:03:11.000+08:00 version=v1.1.60",
		"fail staging missing: C:\\Program Files\\aiops\\.aiops-agent.new.123.exe",
		"[2026-08-16T20:00:00.0000000+08:00] helper start pid=4242",
	} {
		if !helperProgressMarker(body) {
			t.Fatalf("plain body should count as progress: %q", body)
		}
		if !helperProgressMarker(utf8BOM + body) {
			t.Fatalf("BOM-prefixed body must count as progress too: %q", body)
		}
	}
	for _, body := range []string{
		"",
		utf8BOM,
		// Go 自己在登记计划任务之后写的占位，绝不能被当成「助手已启动」——
		// 那正是「第一次能升、以后再也升不动」的老毛病。
		"scheduled 2026-08-16T20:00:00+08:00",
		utf8BOM + "scheduled 2026-08-16T20:00:00+08:00",
	} {
		if helperProgressMarker(body) {
			t.Fatalf("body must NOT count as helper progress: %q", body)
		}
	}
}

// 证据是捎回操作台给人读的，开头挂一串 BOM 乱码等于把它废掉一半。
func TestTailFileForDiagnosticsStripsBOM(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "aiops-agent-update.result")
	if err := os.WriteFile(p, []byte(utf8BOM+"fail Move-Item failed after retries\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := tailFileForDiagnostics(p, 512)
	if strings.HasPrefix(got, utf8BOM) {
		t.Fatalf("BOM leaked into operator-facing evidence: %q", got)
	}
	if got != "fail Move-Item failed after retries" {
		t.Fatalf("unexpected tail: %q", got)
	}
	if tailFileForDiagnostics(filepath.Join(dir, "nope"), 512) != "" {
		t.Fatal("missing file must yield an empty tail, not an error string")
	}
}

// 助手的 catch 块此前无条件把 $cfg 重新赋成模块传进来的值，而 try 块里刚刚「在 exe
// 旁边找到的配置」就这样被丢掉。配置不在 exe 旁边的安装（--install-service 把绝对
// 路径写进 ImagePath，这完全合法）一旦升级失败，回滚路径就会因为「没有配置」拒绝
// 启动 Agent——主机被自己的回滚打成离线，比不升级更糟。
func TestWindowsHelperCatchDoesNotDiscardDiscoveredConfig(t *testing.T) {
	script := buildWindowsUpdateHelperScript(
		`C:\Program Files\aiops\aiops-agent.exe`,
		`C:\Program Files\aiops\.aiops-agent.new.42.exe`,
		"", // 模块没解析出配置：try 块要靠 exe 旁边的探测补上
		`C:\ProgramData\aiops-agent-update\aiops-agent-update.log`,
		`C:\Program Files\aiops\aiops-agent-update.result`,
		`C:\ProgramData\aiops-agent-update\aiops-agent-update.result`,
	)
	for _, bad := range []string{"\n    $cfg = '", "\n    $exe = '"} {
		if strings.Contains(script, bad) {
			t.Fatalf("catch 块又开始无条件覆盖变量（%q），会丢掉 try 里探测到的配置:\n%s", bad, script)
		}
	}
	for _, want := range []string{"if (-not $cfg) { $cfg = ", "if (-not $exe) { $exe = "} {
		if !strings.Contains(script, want) {
			t.Fatalf("catch 块缺少条件赋值 %q:\n%s", want, script)
		}
	}
}
