package main

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestRunCmdEscapedEcho(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "echo ok")
	} else {
		cmd = exec.Command("echo", "ok")
	}
	out, err := runCmdEscaped(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("got %q", out)
	}
}

// TestBuildCmdExeCmdLine pins the shape of the command line handed to cmd.exe.
//
// Go 的 os/exec 会用 CRT 约定给参数加引号、把内嵌的 " 转义成 \"，而 cmd.exe 不认这套
// （Go 自己的文档就把 cmd.exe 列为例外）。Agent 给每条 exec/剧本命令都套了
// `set "PATH=…" & "…\chcp.com" 65001 …` 这个前缀，于是它到达 cmd.exe 时变成
// `set \"PATH=…`——定义了一个名叫 \"PATH 的变量，chcp 也因为命令名以 \" 开头而失败。
// 两处失败都被静默吞掉（chcp 输出重定向到 nul，且刻意用 & 而非 && 串接），所以这段
// 代码要修的 LocalSystem 瘦 PATH 问题，在任何一台 Windows 上都从未真正修过。
// 这个测试只能在 Linux CI 上跑，所以断言的是我们自己拼出来的那行字符串。
func TestBuildCmdExeCmdLine(t *testing.T) {
	const exe = `C:\Windows\System32\cmd.exe`
	inner := `set "PATH=C:\Windows\System32;%PATH%" & "C:\Windows\System32\chcp.com" 65001 >nul 2>nul & echo hi`
	got := buildCmdExeCmdLine(exe, inner)

	if want := `"` + exe + `" /c ` + inner; got != want {
		t.Fatalf("command line mangled\n got: %s\nwant: %s", got, want)
	}
	// The remainder after /c must reach cmd verbatim: no CRT-style escaping.
	if strings.Contains(got, `\"`) {
		t.Fatalf("command line contains CRT-escaped quotes, which cmd.exe reads literally: %s", got)
	}
	// It must NOT start with a quote right after /c — cmd strips the first and
	// last quote of the line in that case, which would eat our real quoting.
	rest := got[strings.Index(got, " /c ")+len(" /c "):]
	if strings.HasPrefix(rest, `"`) {
		t.Fatalf("payload starts with a quote; cmd.exe would strip it: %s", rest)
	}
}
