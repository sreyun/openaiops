package main

import (
	"strings"
	"testing"
)

// 现网原样抓到的一行（server11，2026-08-17 16:38）。PowerShell 把进度记录序列化成
// CLIXML 写进 stderr，Agent 的 exec 通道 stdout/stderr 合流，于是几千字节 XML 和唯一
// 有用的那一行挤在同一条 message 里——而 message 只留 500 字。
const realWorldCLIXML = "#< CLIXML\n" +
	"legacy agent update ok helper=aabbccddeeff via=task log=C:\\ProgramData\\aiops-agent-update\\aiops-agent-update.log\n" +
	`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
	`<Obj S="progress" RefId="0"><TN RefId="0"><T>System.Management.Automation.PSCustomObject</T><T>System.Object</T></TN>` +
	`<MS><I64 N="SourceId">1</I64><PR N="Record"><AV>Preparing modules for first use.</AV><AI>0</AI><Nil /><PI>-1</PI>` +
	`<PC>-1</PC><T>Completed</T><SR>-1</SR><SD> </SD></PR></MS></Obj></Objs>`

func TestSanitizeKeepsTheOnlyUsefulLine(t *testing.T) {
	got := sanitizePowerShellOutput(realWorldCLIXML)
	if strings.Contains(got, "CLIXML") || strings.Contains(got, "<Objs") || strings.Contains(got, "Preparing modules") {
		t.Fatalf("CLIXML noise survived:\n%s", got)
	}
	if !strings.Contains(got, "legacy agent update ok helper=aabbccddeeff via=task") {
		t.Fatalf("the line the operator actually needs was dropped:\n%s", got)
	}
	// The whole point is fitting the console's message budget.
	if len(got) > 200 {
		t.Fatalf("sanitized output is still %d bytes:\n%s", len(got), got)
	}
	// The server keys pending_verify off this exact phrase; sanitizing must not
	// break that contract.
	if !strings.Contains(strings.ToLower(got), "legacy agent update ok") {
		t.Fatal("the pending_verify marker phrase must survive sanitizing")
	}
}

// 丢掉整坨 XML 很容易，但**真正的错误信息就藏在同一坨里**。删噪声不能连证据一起删。
func TestSanitizeRecoversErrorRecordsFromCLIXML(t *testing.T) {
	in := "#< CLIXML\n" +
		`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
		`<S S="Error">Register-ScheduledTask : Access is denied._x000D__x000A_At line:1 char:1</S>` +
		`<S S="Warning">The task is already registered</S>` +
		`<S S="Verbose">loading module</S>` +
		`</Objs>`
	got := sanitizePowerShellOutput(in)
	if !strings.Contains(got, "Access is denied") {
		t.Fatalf("the error record was thrown away with the noise:\n%s", got)
	}
	if !strings.Contains(got, "ps error:") || !strings.Contains(got, "ps warning:") {
		t.Fatalf("records lost their kind:\n%s", got)
	}
	if strings.Contains(got, "_x000D_") || strings.Contains(got, "<S ") {
		t.Fatalf("CLIXML escaping/markup leaked through:\n%s", got)
	}
	if strings.Contains(got, "loading module") {
		t.Fatalf("verbose records are noise and must not be kept:\n%s", got)
	}
}

// message 是被截断保存的，所以现网存量里必然存在「XML 开了头没有收尾」的样本。
func TestSanitizeHandlesTruncatedCLIXML(t *testing.T) {
	in := "legacy agent update ok via=wmi\n#< CLIXML\n" +
		`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04"><Obj S="progress" RefId="0"><TN RefId="0"><T>System.Man`
	got := sanitizePowerShellOutput(in)
	if strings.Contains(got, "<Objs") || strings.Contains(got, "System.Man") {
		t.Fatalf("truncated blob survived:\n%s", got)
	}
	if !strings.Contains(got, "legacy agent update ok via=wmi") {
		t.Fatalf("stdout ahead of the truncated blob was eaten:\n%s", got)
	}
}

// 绝大多数输出（Linux 脚本、模块 JSON）里根本没有 CLIXML，一个字节都不该动。
func TestSanitizeLeavesOrdinaryOutputAlone(t *testing.T) {
	for _, in := range []string{
		"legacy agent update ok sha=deadbeef\n",
		"agent_update: restart scheduled\n",
		"",
		"a line with a <tag> and an & ampersand",
	} {
		if got := sanitizePowerShellOutput(in); got != in {
			t.Fatalf("non-CLIXML output was rewritten:\n in: %q\nout: %q", in, got)
		}
	}
}
