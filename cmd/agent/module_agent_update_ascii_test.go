package main

import (
	"strings"
	"testing"
)

// TestWindowsAgentScriptsAreASCII mirrors the server-side guard
// (TestWindowsUpdateHelperIsASCII) for the scripts the AGENT generates.
//
// 这两段脚本由 Agent 以 UTF-8、不带 BOM 写进 .ps1，而 Windows PowerShell 5.1 读
// 无 BOM 的脚本用系统 ANSI 代码页——中文 Windows 上是 GBK。UTF-8 的中文字节在 GBK
// 下是非法序列，能不能解析取决于 MultiByteToWideChar 的替换行为；就算侥幸解析成功，
// 助手日志里的中文也会变成乱码，而换版发生在 Agent 被杀之后的独立进程里，那份日志
// 是唯一的失败证据。中文说明留在 Go 注释里。
func TestWindowsAgentScriptsAreASCII(t *testing.T) {
	scripts := map[string]string{
		"helper": buildWindowsUpdateHelperScript(
			`C:\Program Files\AIOps Agent\aiops-agent.exe`,
			`C:\Program Files\AIOps Agent\.aiops-agent.new.1234`,
			`C:\Program Files\AIOps Agent\config.yaml`,
			`C:\ProgramData\aiops-agent-update\aiops-agent-update.log`,
			`C:\Program Files\AIOps Agent\aiops-agent-update.result`,
			`C:\ProgramData\aiops-agent-update\aiops-agent-update.result`),
		"task": buildWindowsUpdateTaskScript(windowsSelfUpdateTaskName,
			`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
			`-NoProfile -Command "& 'C:\ProgramData\x.ps1'"`),
	}
	for name, script := range scripts {
		for i, r := range script {
			if r > 127 {
				line := 1 + strings.Count(script[:i], "\n")
				t.Errorf("%s script: non-ASCII %q (U+%04X) on line %d — keep the Chinese commentary in the Go source, not in the generated .ps1",
					name, r, r, line)
			}
		}
	}
}
