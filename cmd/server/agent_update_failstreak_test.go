package main

import (
	"strings"
	"testing"
)

// 归一化要抹掉的是「每次都不一样、但不影响是不是同一个问题」的部分。server11 那五条
// 日志除了时间戳和 pid 逐字相同，如果指纹留着它们，五次失败就会被当成五个不同的问题，
// 计数永远到不了阈值——熔断也就永远不会触发。
func TestAgentUpdateFailFingerprintCollapsesNoise(t *testing.T) {
	a := agentUpdateFailFingerprint(
		"[2026-08-18T09:02:53.0009618+08:00] helper start pid=15276; update failed: staging not runnable (exit=): v0.19.100")
	b := agentUpdateFailFingerprint(
		"[2026-08-18T08:56:51.1616129+08:00] helper start pid=5416; update failed: staging not runnable (exit=): v0.19.100")
	if a != b {
		t.Fatalf("同一原因的两次失败指纹不同：\n%q\n%q", a, b)
	}
	// 换了一版仍是同一句话 —— 仍算同一个问题（跨版本由 TargetVer 单独把关）。
	c := agentUpdateFailFingerprint("update failed: staging not runnable (exit=): v0.19.101")
	if agentUpdateFailFingerprint("update failed: staging not runnable (exit=): v0.19.100") != c {
		t.Fatal("只有版本号不同的同一句话应当算同一个指纹")
	}
	// 真正不同的原因必须区分得开，否则熔断会把「换了个毛病」也一起挡住。
	d := agentUpdateFailFingerprint("SHA-256 mismatch (want abcdef01 got 12345678)")
	if d == a {
		t.Fatal("不同原因的指纹不应相同")
	}
	if strings.TrimSpace(agentUpdateFailFingerprint("   ")) != "" {
		t.Fatal("空消息不应产生指纹")
	}
}

// 抬头必须**只发一次**：此后每 6 分钟再喊一遍，只会变成新的噪音，而噪音正是这套机制
// 要消灭的东西。而闸门则要持续生效，直到原因变了、版本变了或人工修好。
func TestAgentUpdateFailStreakRaisesOnceThenGates(t *testing.T) {
	srv, _ := newTestServer(t)
	m := srv.agentUpdates
	const host = "h-streak"
	msg := "restart scheduled but agent_version still behind " + appVersion +
		" | host evidence: update failed: staging not runnable (exit=): v0.19.100"

	for i := 1; i < agentUpdateFailStreakLimit; i++ {
		if _, raised := m.noteFailure(host, "v0.19.98", appVersion, msg); raised {
			t.Fatalf("第 %d 次就抬头了，阈值是 %d", i, agentUpdateFailStreakLimit)
		}
		if blocked, _ := srv.agentUpdateFailStreakGate(host); blocked {
			t.Fatalf("第 %d 次就熔断了，阈值是 %d", i, agentUpdateFailStreakLimit)
		}
	}
	st, raised := m.noteFailure(host, "v0.19.98", appVersion, msg)
	if !raised {
		t.Fatalf("第 %d 次应当抬头，得到 count=%d", agentUpdateFailStreakLimit, st.Count)
	}
	blocked, detail := srv.agentUpdateFailStreakGate(host)
	if !blocked {
		t.Fatal("到达阈值后自动升级必须停下来")
	}
	// 「为什么停」必须带原文，否则又回到「没升上去，谁也说不清原因」。
	if !strings.Contains(detail, "staging not runnable") {
		t.Fatalf("跳过原因里必须带失败原文，得到：%s", detail)
	}
	if _, raisedAgain := m.noteFailure(host, "v0.19.98", appVersion, msg); raisedAgain {
		t.Fatal("同一串失败只能抬头一次")
	}
}

// 原因变了就是另一个问题，值得重试——计数必须归零，否则一次偶发失败会让此后任何
// 新问题都直接撞上熔断。
func TestAgentUpdateFailStreakResetsOnDifferentCause(t *testing.T) {
	srv, _ := newTestServer(t)
	m := srv.agentUpdates
	const host = "h-reset"
	for i := 0; i < agentUpdateFailStreakLimit; i++ {
		m.noteFailure(host, "v0.19.98", appVersion, "staging not runnable (exit=): v0.19.100")
	}
	if blocked, _ := srv.agentUpdateFailStreakGate(host); !blocked {
		t.Fatal("同因失败到阈值应当熔断")
	}
	st, _ := m.noteFailure(host, "v0.19.98", appVersion, "SHA-256 mismatch (want aa got bb)")
	if st.Count != 1 {
		t.Fatalf("换了原因应当从 1 开始重新计数，得到 %d", st.Count)
	}
	if blocked, _ := srv.agentUpdateFailStreakGate(host); blocked {
		t.Fatal("换了原因之后不应仍被熔断挡住")
	}
}

// 熔断绝不能变成永久拉黑：人工升好了（版本追上）或发了新版（目标版本变了），
// 两者任一都要自动解除，否则一台已经修好的机器会永远挂在「已暂停自动升级」上。
func TestAgentUpdateFailStreakNeverSticksForever(t *testing.T) {
	srv, _ := newTestServer(t)
	m := srv.agentUpdates
	const host = "h-clear"
	for i := 0; i < agentUpdateFailStreakLimit; i++ {
		m.noteFailure(host, "v0.19.98", appVersion, "staging not runnable (exit=): v0.19.100")
	}
	if blocked, _ := srv.agentUpdateFailStreakGate(host); !blocked {
		t.Fatal("前置条件：应当已熔断")
	}
	m.clearFailStreak(host)
	if blocked, _ := srv.agentUpdateFailStreakGate(host); blocked {
		t.Fatal("版本追上后清除计数，熔断必须解除")
	}

	// 目标版本变了 = 发了新版，可能就修好了，必须自动放行。
	const host2 = "h-newver"
	for i := 0; i < agentUpdateFailStreakLimit; i++ {
		m.noteFailure(host2, "v0.19.98", "v0.0.1-old", "staging not runnable (exit=): v0.19.100")
	}
	if blocked, _ := srv.agentUpdateFailStreakGate(host2); blocked {
		t.Fatal("目标版本已经变了，熔断不应跨版本粘住")
	}
	if _, ok := m.failStreak(host2); ok {
		t.Fatal("跨版本放行时应当把陈旧的计数一并清掉")
	}
}
