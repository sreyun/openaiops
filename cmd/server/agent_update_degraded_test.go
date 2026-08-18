package main

import (
	"strings"
	"testing"
)

// 换版成功与服务起来是两件事。助手把差集写成 result 文件里的 degraded 一行，服务端必须
// 读得懂它——否则一台「二进制新了但服务没起来」的主机在操作台上是绿的，直到它下一次
// 重启后彻底掉线。

func TestDegradedSwapVerdictReadsHelperResult(t *testing.T) {
	out := "aiops-agent-update.result >> degraded 2026-08-18T09:12:00.0000000-07:00 version=0.19.100 reason=service-not-running"
	got := degradedSwapVerdict(out, "0.19.100")
	if got == "" {
		t.Fatal("助手写下的 degraded 判决没有被读出来")
	}
	if want := "reason=service-not-running"; !strings.Contains(got, want) {
		t.Fatalf("判决原文必须原样带回，缺少 %q: %s", want, got)
	}
}

// result 文件是累积的：上一轮的 degraded 还躺在里面。拿它给这一轮定罪，等于让一台
// 已经修好的主机永远判不了成功。
func TestDegradedSwapVerdictIgnoresAnEarlierRound(t *testing.T) {
	out := "aiops-agent-update.result >> degraded 2026-08-01T00:00:00.0000000-07:00 version=0.19.98 reason=service-not-running"
	if got := degradedSwapVerdict(out, "0.19.100"); got != "" {
		t.Fatalf("上一轮留下的 degraded 不能给这一轮定罪: %s", got)
	}
}

// PowerShell 5.1 的 -Encoding UTF8 带 BOM，而取证命令还在正文前面加了文件名前缀。
// BOM 在正文开头，也就是**前缀之后**——只在行首剥一次是剥不掉的。
func TestDegradedSwapVerdictSurvivesBOMAfterTheFilePrefix(t *testing.T) {
	out := "aiops-agent-legacy-update.result >> " + utf8BOM + "degraded 2026-08-18T09:12:00Z version=v0.19.100 reason=service-not-running"
	if got := degradedSwapVerdict(out, "0.19.100"); got == "" {
		t.Fatal("BOM 挡住了 degraded 判定")
	}
	// 前导 v 两边都可能出现，不能因为一个 v 就判不出来。
	if got := degradedSwapVerdict(out, "v0.19.100"); got == "" {
		t.Fatal("目标版本带 v 时判不出来")
	}
}

// 成功就是成功：ok 一行绝不能被读成 degraded，否则每一次正常升级都会被判失败。
func TestDegradedSwapVerdictLeavesASuccessfulSwapAlone(t *testing.T) {
	out := "aiops-agent-update.result >> ok 2026-08-18T09:12:00Z version=0.19.100"
	if got := degradedSwapVerdict(out, "0.19.100"); got != "" {
		t.Fatalf("一次正常升级被判成 degraded: %s", got)
	}
	if got := degradedSwapVerdict("", "0.19.100"); got != "" {
		t.Fatalf("读不到 result 时必须按成功处理（不确定不等于失败）: %s", got)
	}
	if got := degradedSwapVerdict(out, ""); got != "" {
		t.Fatal("版本号未知时无法核对新旧，必须按成功处理")
	}
}

// 取证命令要在**每台成功升级的 Windows 主机**上跑一次，所以它必须便宜：只读 result 的
// 最后一行，不碰日志。以及必须走 -EncodedCommand——现网老 Agent 会弄坏带双引号的命令。
func TestWindowsUpdateResultCommandStaysCheapAndQuoteFree(t *testing.T) {
	cmd := windowsUpdateResultCommand()
	if !strings.Contains(cmd, "-EncodedCommand") {
		t.Fatal("必须走 -EncodedCommand，否则老 Agent 上命令会被转义弄坏")
	}
	for _, ch := range []string{`"`, "'"} {
		if strings.Contains(cmd, ch) {
			t.Fatalf("命令行里不得出现引号: %s", cmd)
		}
	}
	if strings.Contains(cmd, "aiops-agent-update.log") {
		t.Fatal("这条是每次成功都跑的探针，不该去拉日志正文")
	}
}
