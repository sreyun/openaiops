package main

import (
	"regexp"
	"strings"
	"testing"
)

// 现网症状（server11，v0.19.93 → v0.19.96）：
//
//	pending_verify | script | #< CLIXML \n legacy agent update ok helper=-nop -noni - log=C:\ProgramData\... \n <Objs …>
//
// 五分钟后转成 failed：「restart scheduled but agent_version still behind v0.19.96」。
// 引导脚本报了 ok，但主机的版本一直没动——因为「拉起助手」的三条路都可能**成功地
// 什么也没做**：Win32_Process.Create 返回 0 只代表创建了进程；计划任务被电池策略挡下
// 时 /Run 一样返回成功；cmd start 拉起就走。ok 因此只证明了"我发过命令"。
//
// 所以引导脚本必须等助手自己写下 result 标记，等不到就换下一条路，三条都等不到就**当场
// 失败**，而不是打印 ok 再让服务端等五分钟去猜。
func TestBootstrapWaitsForTheHelperToActuallyComeUp(t *testing.T) {
	inline := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe", testPinSHA))

	if !strings.Contains(inline, "$Mk=") || !strings.Contains(inline, "Test-Path $Mk") {
		t.Fatal("bootstrap must poll for the helper's result marker; without it 'ok' only means 'a spawn API returned'")
	}
	// A marker left over from an earlier round would make the very first check pass.
	if !strings.Contains(inline, "rm $F,$Mk") {
		t.Fatal("the stale result marker must be removed before spawning, or the check passes on the previous run's file")
	}
	failAt := strings.Index(inline, `if(-not $k){throw`)
	okAt := strings.Index(inline, "legacy agent update ok")
	if failAt < 0 {
		t.Fatal("bootstrap must fail loudly when no spawn path brought the helper up")
	}
	if okAt < 0 || failAt > okAt {
		t.Fatal("the success line must come after the 'did it actually start' check, not before")
	}
	// Which path worked is the first question every Windows update post-mortem asks.
	if !strings.Contains(inline, "via=$k") {
		t.Fatal("the success line must report which spawn path brought the helper up")
	}
	for _, tag := range []string{"'wmi'", "'task'", "'cmd'"} {
		if !strings.Contains(inline, tag) {
			t.Fatalf("spawn ladder is missing the %s rung", tag)
		}
	}
}

// 助手侧的对偶约束：引导脚本等的那个标记，助手必须在**任何可能失败的动作之前**写下。
// 否则「找不到 Agent 安装位置」这类失败会被引导脚本读成「助手没起来」，然后再拉起两次
// 同样注定失败的助手，最后报一个完全指错方向的原因。
func TestHelperWritesItsMarkerBeforeAnythingCanFail(t *testing.T) {
	ps := windowsUpdateHelperScript()
	first := strings.Index(ps, "Write-Result")
	resolve := strings.Index(ps, "$Exe = Resolve-AgentExe")
	if first < 0 || resolve < 0 {
		t.Fatal("helper lost Write-Result or the exe resolution")
	}
	marker := strings.Index(ps, `Write-Result ("running `)
	if marker < 0 || marker > resolve {
		t.Fatal("the 'running' marker must be written before the agent install is resolved — that step can fail")
	}
	if !strings.Contains(ps, "Write-Result 'fail agent exe not found") {
		t.Fatal("a failed exe resolution must leave a result behind; silence is indistinguishable from 'never started'")
	}
	// Two helpers swapping the same locked PE is worse than no update at all, and
	// the bootstrap's 12s marker window can time out on a merely slow host.
	if !strings.Contains(ps, "Global\\AIOpsAgentUpdateHelper") || !strings.Contains(ps, "WaitOne(0)") {
		t.Fatal("helper must hold a global single-instance lock")
	}
	if !strings.Contains(ps, "AbandonedMutexException") {
		t.Fatal("a helper killed mid-swap leaves the mutex abandoned; that case must still be allowed to retry")
	}
	// Stopping the agent kills the exec channel the bootstrap is still writing on.
	if !strings.Contains(ps, "$quiet = 20 - ") {
		t.Fatal("helper must hold off the service stop until the bootstrap has had its marker window")
	}
	if q, stop := strings.Index(ps, "$quiet = 20 - "), strings.Index(ps, `@('stop',$name)`); stop < 0 || q > stop {
		t.Fatal("the quiet period must come before the service stop, or it protects nothing")
	}
}

// PowerShell 的变量名不区分大小写，而这两段脚本为了塞进 cmd.exe 的命令行预算全用短名。
// 历史事故：`$a` 存助手摘要、`$A` 存启动参数，是同一个变量，于是成功那行打出来的
// `helper=` 是参数串的前 12 个字符而不是摘要——唯一能证明"下发的是哪一份助手"的字段变成
// 了噪声。函数参数同样会遮蔽外层同名变量（`function Sp($m,$b)` 里的 `$m` 会盖住 `$Mk`
// 之外任何叫 `$M` 的东西）。这条测试把「同一个名字只许有一种拼法」变成机器可查的规则。
// stripPSComments drops whole-line PowerShell comments. Prose quotes variables
// freely ("& $new ran the downloaded agent"), and a comment cannot shadow
// anything, so scanning them would only produce false alarms.
func stripPSComments(body string) string {
	lines := strings.Split(body, "\n")
	out := lines[:0]
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func TestPowerShellScriptsHaveNoCaseColludingVariables(t *testing.T) {
	varRe := regexp.MustCompile(`\$(?:script:|global:|local:|private:|env:)?([A-Za-z_][A-Za-z0-9_]*)`)
	for _, tc := range []struct{ name, body string }{
		{"bootstrap", decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe", testPinSHA))},
		{"helper", windowsUpdateHelperScript()},
		{"evidence", decodeLegacyWindowsPS(t, windowsUpdateEvidenceCommand())},
	} {
		spellings := map[string]map[string]bool{}
		for _, m := range varRe.FindAllStringSubmatch(stripPSComments(tc.body), -1) {
			name := m[1]
			low := strings.ToLower(name)
			if spellings[low] == nil {
				spellings[low] = map[string]bool{}
			}
			spellings[low][name] = true
		}
		for low, forms := range spellings {
			if len(forms) > 1 {
				var list []string
				for f := range forms {
					list = append(list, "$"+f)
				}
				t.Errorf("%s: %q is written %d different ways (%s) — PowerShell treats them as ONE variable, "+
					"so the later assignment silently destroys the earlier value",
					tc.name, low, len(forms), strings.Join(list, " "))
			}
		}
	}
}

// 现网 2026-08-17 23:15，server11 的取证输出里，目录中只剩三个脚本文件：
//
//	aiops-agent-update-helper.ps1  13850B  23:15:18   ← Agent 侧 module 助手（后写的）
//	aiops-agent-update.cmd           278B  23:09:38
//	aiops-agent-update.ps1         20169B  23:09:38   ← 服务端 legacy 助手
//	（计划任务 AIOpsAgentLegacyUpdate：Last Run 23:09:51，Last Result: 1）
//
// 助手在 23:09 跑过并且失败了（Last Result=1），失败原因写进了 .result/.log；23:15 软重试
// 改走 module 路径，而 Agent 侧每次开工都会 os.Remove 掉 aiops-agent-update.{log,result}
// （清陈旧标记，对它自己完全正确）——两条路共用同一组文件名，于是**唯一记着为什么失败的
// 那两个文件被下一次尝试擦掉了**，只留下一个 "Last Result: 1"。
//
// 两条路必须各写各的文件。Agent 侧那组名字已经烧进现网的老 Agent 里改不动，所以让服务端
// 这一侧（引导脚本与助手都由服务端生成、同版本下发）改名。
func TestUpdateHelperFileNamesDoNotCollide(t *testing.T) {
	helper := windowsUpdateHelperScript()
	inline := decodeLegacyWindowsPS(t, legacyWindowsAgentUpdateScript("https://mon.example", "aiops-agent.exe", testPinSHA))

	// Agent 侧 module 路径写死的那组名字（cmd/agent/module_agent_update_windows.go）。
	for _, agentOwned := range []string{"'aiops-agent-update.log'", "'aiops-agent-update.result'"} {
		if strings.Contains(helper, agentOwned) {
			t.Errorf("服务端助手仍在用 Agent 侧的文件名 %s：下一次 module 升级会把它的失败原因删掉", agentOwned)
		}
	}
	for _, own := range []string{"aiops-agent-legacy-update.log", "aiops-agent-legacy-update.result"} {
		if !strings.Contains(helper, own) {
			t.Errorf("服务端助手没有使用自己的文件名 %s", own)
		}
	}
	// 引导脚本等的标记必须与助手写的是同一个文件，否则握手永远等不到。
	if !strings.Contains(inline, "aiops-agent-legacy-update") {
		t.Fatal("引导脚本的 result 标记没跟着改名，握手会永远超时")
	}
	if strings.Contains(inline, `$Mk="$W\$N.result"`) {
		t.Fatal("引导脚本仍在等 Agent 侧那个会被删掉的标记文件")
	}
	// 取证要同时读两组，否则改名之后反而看不见 legacy 的证据。
	ev := decodeLegacyWindowsPS(t, windowsUpdateEvidenceCommand())
	for _, want := range []string{"aiops-agent-update.log", "aiops-agent-legacy-update.log"} {
		if !strings.Contains(ev, want) {
			t.Errorf("取证命令没有读 %s", want)
		}
	}
}
