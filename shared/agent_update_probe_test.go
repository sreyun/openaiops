package shared

import (
	"strings"
	"testing"
)

// 这段 PowerShell 会被拼进 Go 原始字符串字面量，并且其中一处还落在 fmt.Sprintf 的
// 格式串里。出现反引号会提前终止字面量（编译不过），出现 % 则会被当成格式动词，
// 悄悄把脚本改成别的东西——后者不报错，只会在真实主机上生成一份坏脚本。
func TestWindowsVersionProbePSIsSpliceSafe(t *testing.T) {
	if strings.Contains(WindowsVersionProbePS, "`") {
		t.Fatal("探针文本不得包含反引号：会终止 Go 原始字符串字面量")
	}
	if strings.Contains(WindowsVersionProbePS, "%") {
		t.Fatal("探针文本不得包含 %：它会被 fmt.Sprintf 当成格式动词")
	}
}

// 探针的两半缺一不可：Invoke-VersionProbe 负责拿到结果，Test-ProbeRunnable 负责判定。
// 曾经的缺陷正是判定那一半——退出码读不出来（$null）时按失败处理，把一个已经跑起来
// 并打印了版本号的二进制判成「不可运行」。
func TestWindowsVersionProbePSKeepsBothHalves(t *testing.T) {
	for _, want := range []string{
		"function Invoke-VersionProbe",
		"function Test-ProbeRunnable",
		"New-Object Diagnostics.Process",
	} {
		if !strings.Contains(WindowsVersionProbePS, want) {
			t.Fatalf("探针缺少 %q", want)
		}
	}
	// 退出码必须来自我们自己 Start 的 Process 对象。Start-Process -PassThru 交回来的
	// 对象在部分主机上读 .ExitCode 会抛异常，而 PowerShell 把它吞成 $null。
	for i, line := range strings.Split(WindowsVersionProbePS, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") { // 注释里写着当年为什么错，那是要留着的
			continue
		}
		if strings.Contains(code, "Start-Process") {
			t.Fatalf("第 %d 行仍在用 Start-Process 取退出码：%s", i+1, code)
		}
	}
	// 判定必须显式区分「退出码读不出来」与「退出码非 0」。
	if !strings.Contains(WindowsVersionProbePS, "$null -ne $ProbeResult.ExitCode") {
		t.Fatal("Test-ProbeRunnable 必须先判断退出码是否可读，再比较是否为 0")
	}
}

// 脚本要下发到目标主机并按 ASCII 校验（cmd/server 的 TestWindowsUpdateHelperIsASCII 与
// cmd/agent 的同名测试）。在这里再拦一道，是为了让「中文写进了脚本」在改动它的那一刻
// 就红，而不是等两个下游包的测试各红一次。
func TestWindowsVersionProbePSIsASCII(t *testing.T) {
	for i, r := range WindowsVersionProbePS {
		if r > 127 {
			t.Fatalf("探针第 %d 字节处出现非 ASCII 字符 %q：中文说明请留在 Go 注释里", i, r)
		}
	}
}
