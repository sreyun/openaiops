package main

import (
	"strings"
	"testing"
	"time"
)

// 自身故障最典型的形态就是同一件事每分钟发生一次。逐条落库只会把真正的信息淹掉——
// 而「淹掉」正是 Windows 升级那件事的全部经过：五条一模一样的记录，没有一条说过
// 「这已经是第五次了」。
func TestPlatformFaultAggregatesByFingerprint(t *testing.T) {
	srv, _ := newTestServer(t)
	for i := 0; i < 5; i++ {
		srv.reportPlatformFault("loop", "loop_panic", "warning", "",
			"后台循环「alerts」发生 panic：runtime error: index out of range [3] pid=1"+string(rune('0'+i)), "stack…")
	}
	list := srv.faults.snapshot(0)
	if len(list) != 1 {
		t.Fatalf("同一原因应聚合成一条，得到 %d 条：%+v", len(list), list)
	}
	if list[0].Count != 5 {
		t.Fatalf("聚合计数应为 5，得到 %d", list[0].Count)
	}
	// 不同原因必须分开，否则「换了个毛病」会被混进同一条里看不见。
	srv.reportPlatformFault("loop", "loop_panic", "warning", "", "后台循环「vmwrite」发生 panic：nil map write", "stack…")
	if n := len(srv.faults.snapshot(0)); n != 2 {
		t.Fatalf("不同原因应各占一条，得到 %d", n)
	}
}

// 不是所有故障都值得开事件：偶发一次的写库超时不该叫醒任何人，而反复发生的必须进闭环。
func TestPlatformFaultRaisesIncidentAtThreshold(t *testing.T) {
	srv, _ := newTestServer(t)
	for i := 1; i < platformFaultIncidentThreshold; i++ {
		srv.reportPlatformFault("vm", "queue_full", "warning", "", "VictoriaMetrics 写入队列已满", "")
	}
	if n := srv.incidents.OpenCount(); n != 0 {
		t.Fatalf("未到阈值不应开事件，已开 %d 个", n)
	}
	srv.reportPlatformFault("vm", "queue_full", "warning", "", "VictoriaMetrics 写入队列已满", "")
	waitFor(t, 3*time.Second, func() bool { return srv.incidents.OpenCount() == 1 })

	list := srv.faults.snapshot(0)
	if len(list) != 1 || list[0].IncidentID == 0 {
		t.Fatalf("开完事件后必须回填 incident_id：%+v", list)
	}
	// 再来一百次也只能是同一个事件，否则告警风暴换了个地方继续。
	for i := 0; i < 100; i++ {
		srv.reportPlatformFault("vm", "queue_full", "warning", "", "VictoriaMetrics 写入队列已满", "")
	}
	time.Sleep(200 * time.Millisecond)
	if n := srv.incidents.OpenCount(); n != 1 {
		t.Fatalf("同一条故障只应有一个事件，得到 %d", n)
	}
}

// critical 第一次就要开：审计链断掉、常驻循环 panic 这类故障，没有任何人会来告诉你，
// 等它「攒够三次」就已经晚了。
func TestPlatformFaultCriticalRaisesImmediately(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.reportPlatformFault("pg", "audit_append_failed", "critical", "", "审计链追加失败", "connection refused")
	waitFor(t, 3*time.Second, func() bool { return srv.incidents.OpenCount() == 1 })
	incs := srv.incidents.List()
	if len(incs) == 0 || !strings.Contains(incs[0].Title, "平台自身故障") {
		t.Fatalf("事件标题应表明这是平台自身故障：%+v", incs)
	}
	// 证据必须进时间线：AI 诊断读的就是它，证据不进去等于没采集。
	found := false
	for _, ev := range incs[0].Timeline {
		if strings.Contains(ev.Text, "connection refused") && strings.Contains(ev.Text, "【证据】") {
			found = true
		}
	}
	if !found {
		t.Fatalf("证据没有挂到事件时间线上：%+v", incs[0].Timeline)
	}
}

// 一条每天出现一次的偶发故障，攒上一周也不该假装成「连续失败」。
func TestPlatformFaultStreakResetsAfterGap(t *testing.T) {
	srv, _ := newTestServer(t)
	const msg = "偶发：PG 写告警历史超时"
	srv.reportPlatformFault("pg", "alert_history", "warning", "", msg, "")
	key := platformFaultKey("pg", "alert_history", "", msg)
	srv.faults.mu.Lock()
	srv.faults.faults[key].LastAt = time.Now().Unix() - platformFaultStreakGapSec - 1
	srv.faults.mu.Unlock()

	srv.reportPlatformFault("pg", "alert_history", "warning", "", msg, "")
	list := srv.faults.snapshot(0)
	if len(list) != 1 || list[0].Count != 1 {
		t.Fatalf("隔太久再出现应当从头计数，得到 %+v", list)
	}
}

// panic 是平台自身最严重的一类故障，此前它只有一行 slog。接进归口之后，它必须像
// 主机故障一样开事件、带堆栈。
func TestSupervisedPanicBecomesPlatformFault(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.bindPlatformFaultSinks()
	t.Cleanup(func() { onPlatformPanic = nil; platformFaultSink = nil })

	runSupervised("test-loop", func() { panic("boom") })
	waitFor(t, 3*time.Second, func() bool { return len(srv.faults.snapshot(0)) == 1 })

	f := srv.faults.snapshot(0)[0]
	if f.Component != "loop" || f.Level != "critical" {
		t.Fatalf("panic 应按 loop/critical 上报，得到 %+v", f)
	}
	if !strings.Contains(f.Message, "test-loop") || !strings.Contains(f.Message, "boom") {
		t.Fatalf("上报消息里必须能看出是哪条循环、panic 了什么：%q", f.Message)
	}
	if !strings.Contains(f.Evidence, "runSupervised") {
		t.Fatalf("堆栈必须作为证据带上：%q", trimLine(f.Evidence, 200))
	}
	waitFor(t, 3*time.Second, func() bool { return srv.incidents.OpenCount() == 1 })
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
