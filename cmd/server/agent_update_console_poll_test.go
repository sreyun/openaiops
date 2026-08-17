package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// 出厂控制台轮询 Agent 升级任务的预算，必须覆盖服务端**整条阶梯**：
//
//	module 助手 verify（agentUpdateVerifyWindow）
//	  → legacy 救援 exec（最长 agentUpdateTimeoutSec）
//	  → 救援 verify（再一个 agentUpdateVerifyWindow）
//
// 两边此前各写各的：服务端把 agentUpdateJobFinalizeWindow 按整条链路算到 22 分钟，
// 前端却停在 6.5 分钟。差额全部砸在 Windows 上——Windows 主机无条件进 pending_verify
// （换版发生在 Agent 被杀之后的独立进程里，只有版本号追上才算数），于是每一次
// Windows 升级都会在服务端还没给出结论、甚至救援脚本正跑到一半时被前端判成「轮询
// 超时」并弹红字；Linux 主机通常直接 success、根本不进 verify，几秒收摊，所以同一个
// 上限只咬 Windows。「Windows Agent 升不上去」的观感，有很大一部分就是这么来的。
//
// 这条测试把前端预算和服务端窗口钉在一起：以后谁改了阶梯长度，忘了改前端就会红。
func TestConsolePollBudgetCoversServerVerifyLadder(t *testing.T) {
	const path = "web/js/agent-update.js"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(b)

	ticks := mustMatchInt(t, src, `AGENT_UPDATE_POLL_TICKS\s*=\s*(\d+)`, "AGENT_UPDATE_POLL_TICKS")
	intervalMs := mustMatchInt(t, src, `AGENT_UPDATE_TIMER\s*=\s*setInterval\((?s:.*?)\}\s*,\s*(\d+)\s*\)`, "poll interval")

	budget := time.Duration(ticks) * time.Duration(intervalMs) * time.Millisecond
	if budget < agentUpdateJobFinalizeWindow {
		t.Fatalf("控制台轮询预算 %v 短于服务端阶梯 %v：Windows 主机会在服务端给出结论之前"+
			"就被判成「轮询超时」（ticks=%d interval=%dms）",
			budget, agentUpdateJobFinalizeWindow, ticks, intervalMs)
	}
}

// 失败详情里真正有用的部分全在消息**结尾**：服务端拼上去的 " | host evidence: …" 与
// Agent 捎回来的「上一轮升级助手留下的记录」。此前前端把消息压成一行再切到 160 字，
// 恰好只留下开头那句谁都看得懂但什么也没说的英文——证据一路运到操作台，最后一步被
// 切掉了。这条测试禁止那种截断回来。
func TestConsoleJobDetailDoesNotTruncateHostMessage(t *testing.T) {
	const path = "web/js/agent-update.js"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(b)
	// 只看渲染详情的那一段，避免把别处无关的 slice 也算进来。
	re := regexp.MustCompile(`(?s)function showAgentUpdateJobDetail\(\)\s*\{.*?\n\}`)
	body := re.FindString(src)
	if body == "" {
		t.Fatal("showAgentUpdateJobDetail 不见了；本测试需要跟着改")
	}
	if regexp.MustCompile(`h\.message[^\n]*slice\(`).MatchString(body) {
		t.Fatalf("失败详情又开始截断 h.message，会把唯一解释原因的证据切掉：\n%s", body)
	}
}

// 设置页的「最近的升级任务」是另一处会看到同一条 message 的地方，同样不许截断。
func TestSettingsJobListDoesNotTruncateHostMessage(t *testing.T) {
	const path = "web/js/settings.js"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?s)async function loadAgentAutoUpdateJobs\(\)\s*\{.*?\n\}`)
	body := re.FindString(string(b))
	if body == "" {
		t.Fatal("loadAgentAutoUpdateJobs 不见了；本测试需要跟着改")
	}
	if regexp.MustCompile(`h\.message[^\n]*slice\(`).MatchString(body) {
		t.Fatalf("最近任务列表又开始截断 h.message：\n%s", body)
	}
}

func mustMatchInt(t *testing.T, src, pattern, what string) int {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(src)
	if len(m) < 2 {
		t.Fatalf("在 agent-update.js 里找不到 %s（正则 %s）", what, pattern)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		t.Fatalf("%s 解析失败：%q", what, m[1])
	}
	return n
}
