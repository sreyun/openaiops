//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
)

// 中继网关在回源校验不上云端时会有意继续发本地缓存（对断网内网这正是它存在的意义），
// 而缓存里的二进制与 .sha256 是同一代、彼此自洽的。于是升级链路每一步都"成功"：下载
// 完整、校验通过、探针跑得起来、重启也排上了——只有上报的版本纹丝不动。服务端的
// stale_artifact 闸门只看得见自己的 dist，看不见中继缓存，所以这一判必须落在 Agent 侧。

func TestAgentUpdateArtifactIsStale(t *testing.T) {
	cases := []struct {
		name         string
		probed, want string
		stale        bool
	}{
		{"中继发来上一版", "v0.19.98", "v0.19.100", true},
		{"版本一致", "v1.1.5", "v1.1.5", false},
		{"前缀与大小写不算差异", "V1.1.5", "1.1.5", false},
		{"探针没判出版本就不判", "", "v1.1.5", false},
		{"没有目标版本就不判", "v1.1.5", "", false},
		{"两头都没有", "", "", false},
	}
	for _, c := range cases {
		if got := agentUpdateArtifactIsStale(c.probed, c.want); got != c.stale {
			t.Errorf("%s: agentUpdateArtifactIsStale(%q,%q)=%v，期望 %v", c.name, c.probed, c.want, got, c.stale)
		}
	}
}

func TestParseProbeVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v0.19.100\n", "v0.19.100"},
		{"1.1.5", "1.1.5"},
		// 受限主机上偶尔有一行 loader/审计告警混进 stderr，版本号仍应被认出来。
		{"audit: denied /tmp exec\nv1.1.5\n", "v1.1.5"},
		// 判不出来必须是空串：空表示"没判定"，会让调用方放行，而不是判死一次好升级。
		{"", ""},
		{"dev\n", ""},
		{"Access is denied.\n", ""},
	}
	for _, c := range cases {
		if got := parseProbeVersion(c.in); got != c.want {
			t.Errorf("parseProbeVersion(%q)=%q，期望 %q", c.in, got, c.want)
		}
	}
}

// 网关机装的是 aiops-relay.service。换版后去重启 aiops-agent 等于谁都没重启：新二进制
// 静静躺在原地，主机上报的版本永远追不上，服务端只能等 pending_verify 超时再软重试。
func TestPickLinuxAgentUnitPrefersTheUnitWeRunUnder(t *testing.T) {
	always := func(string) bool { return true }
	never := func(string) bool { return false }

	// 网关机同时装了普通 Agent：is-active aiops-agent 会痛快地回答 active，
	// 回答的却是另一个进程。
	if got := pickLinuxAgentUnit("aiops-relay", always, always); got != "aiops-relay" {
		t.Fatalf("自身 unit 必须优先，实际 %q", got)
	}
	// 不是我们认识的 unit（被人套进别的 service 里跑）时不能盲信，退回探测。
	if got := pickLinuxAgentUnit("cron", func(u string) bool { return u == "aiops-monitor-agent" }, never); got != "aiops-monitor-agent" {
		t.Fatalf("陌生 unit 应退回探测，实际 %q", got)
	}
	// 不在 systemd 下（容器 / nohup）：按 is-active → unit 文件 → 兜底 的顺序。
	if got := pickLinuxAgentUnit("", never, func(u string) bool { return u == "aiops-relay" }); got != "aiops-relay" {
		t.Fatalf("应按 unit 文件存在挑到 aiops-relay，实际 %q", got)
	}
	if got := pickLinuxAgentUnit("", never, never); got != "aiops-agent" {
		t.Fatalf("兜底必须是 aiops-agent，实际 %q", got)
	}
}

func TestSystemdUnitFromCgroup(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0::/system.slice/aiops-relay.service\n", "aiops-relay"},
		{"1:name=systemd:/system.slice/aiops-agent.service\n", "aiops-agent"},
		{"0::/user.slice/user-0.slice/session-3.scope\n", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := systemdUnitFromCgroup(c.in); got != c.want {
			t.Errorf("systemdUnitFromCgroup(%q)=%q，期望 %q", c.in, got, c.want)
		}
	}
}

// 中继模式以前是 runRelay(...) + return：内网每一台 agent 都吊在这台机器上，而它恰恰是
// 面板上唯一看不见的一台。这条守着"网关也上报"这件事不被下一次重构悄悄改回去。
func TestRelayModeKeepsReporting(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	const anchor = "\tif cfg.Relay {\n"
	i := strings.Index(string(src), anchor)
	if i < 0 {
		t.Fatalf("main.go 里找不到 relay 分支（%q）", anchor)
	}
	rest := string(src)[i+len(anchor):]
	end := strings.Index(rest, "\n\t}\n")
	if end < 0 {
		t.Fatal("relay 分支没有找到收尾的大括号")
	}
	block := rest[:end]
	if !strings.Contains(block, "go runRelay(") {
		t.Errorf("中继必须在 goroutine 里起，主流程要继续走到采集与上报：\n%s", block)
	}
	if strings.Contains(block, "\n\t\treturn") {
		t.Errorf("relay 分支里不能 return——那会让网关机从主机列表里消失：\n%s", block)
	}
}
