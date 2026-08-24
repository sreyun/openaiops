package main

import (
	"strconv"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// checkSlowDegradation 挂在**每一次 Agent 上报**之后，是全平台最高频的路径。
// 它每次都要整份复制最多 240 条采样、建三份数组、跑三次外推；500 台机群每秒要跑
// 17 遍。而它检测的东西按定义就是慢的——限频到 5 分钟一次，结论不变，代价降一个数量级。
func TestSlowDegradationThrottlePerHost(t *testing.T) {
	now := time.Now().Unix()
	host := "host-throttle-a"
	slowDegradeMu.Lock()
	delete(slowDegradeLast, host)
	slowDegradeMu.Unlock()

	if !shouldRunSlowDegradation(host, now) {
		t.Fatal("第一次必须放行")
	}
	if shouldRunSlowDegradation(host, now+1) {
		t.Fatal("1 秒后不该再跑（上报间隔通常是 30 秒，这里代表连续上报）")
	}
	if shouldRunSlowDegradation(host, now+slowDegradeMinInterval-1) {
		t.Fatal("未到最小间隔就不该跑")
	}
	if !shouldRunSlowDegradation(host, now+slowDegradeMinInterval) {
		t.Fatal("到了最小间隔必须放行")
	}
}

// 限频是**按主机**的：一台机器刚跑过，不能把别的机器一起挡住。
func TestSlowDegradationThrottleIsPerHost(t *testing.T) {
	now := time.Now().Unix()
	a, b := "host-throttle-b1", "host-throttle-b2"
	slowDegradeMu.Lock()
	delete(slowDegradeLast, a)
	delete(slowDegradeLast, b)
	slowDegradeMu.Unlock()

	if !shouldRunSlowDegradation(a, now) {
		t.Fatal("A 第一次应放行")
	}
	if !shouldRunSlowDegradation(b, now) {
		t.Fatal("B 不该被 A 的限频挡住")
	}
	if shouldRunSlowDegradation(a, now) {
		t.Fatal("A 立刻再来一次不该放行")
	}
}

// 自动升级总开关关着时，上报路径不该再做任何事——尤其不该往 skipReasons 里写
// 500 条"总开关未开启"，那会把真正需要人处理的原因（缺产物、下载基址没配）挤出表外，
// 而且每次写入都要 O(500) 扫一遍整张表去淘汰最老的一条。
func TestMaybeAutoUpdateHostNoopWhenDisabled(t *testing.T) {
	srv, _ := newTestServer(t)
	// 出厂默认是开启的，这里显式关掉——要验的正是"关掉之后上报路径什么都不做"。
	if err := srv.cfg.SetAgentAutoUpdatePolicy(false, "", nil, nil); err != nil {
		t.Fatalf("关闭自动升级失败: %v", err)
	}
	const fp = "fp-auto-off-0000000000000000000000"
	_ = srv.store.RegisterHost("host-auto-off", "node-off", fp)
	if _, ok := srv.store.UpsertAuthenticated(shared.Report{
		HostID: "host-auto-off", Hostname: "node-off", AgentVersion: "v0.0.1", Fingerprint: fp,
	}, fp); !ok {
		t.Fatal("测试主机没有登记成功")
	}

	srv.maybeAutoUpdateHost("host-auto-off")

	if n := len(srv.agentUpdates.skipSnapshot(srv)); n != 0 {
		t.Fatalf("总开关关着时不该产生跳过记录，实际 %d 条", n)
	}

	// 反向确认：开关打开后这条路径确实还在工作（别把优化写成"永远不跑"）。
	if err := srv.cfg.SetAgentAutoUpdatePolicy(true, "", nil, nil); err != nil {
		t.Fatalf("重新开启自动升级失败: %v", err)
	}
	srv.maybeAutoUpdateHost("host-auto-off")
	if len(srv.agentUpdates.skipSnapshot(srv)) == 0 {
		t.Fatal("开关打开后应当照常走闸门链并记录跳过原因")
	}
}

// 运维路径指纹表原本只增不减：键是自由文本拼出来的，取值空间没有上限，
// 跑几个月就在内存里堆一大片，而且不出现在任何指标或页面上——没人会发现。
func TestOpsPatternMapIsBounded(t *testing.T) {
	growthHub.mu.Lock()
	growthHub.patterns = map[string]*opsPatternHit{}
	growthHub.mu.Unlock()

	now := time.Now().Unix()
	growthHub.mu.Lock()
	// 一条早就过期的，和一条刚刚出现过的。
	growthHub.patterns["actor|stale"] = &opsPatternHit{Key: "stale", Last: now - opsPatternTTLSec - 1}
	growthHub.patterns["actor|fresh"] = &opsPatternHit{Key: "fresh", Last: now}
	growthHub.pruneLocked(now)
	_, staleKept := growthHub.patterns["actor|stale"]
	_, freshKept := growthHub.patterns["actor|fresh"]
	growthHub.mu.Unlock()

	if staleKept {
		t.Error("超过 TTL 的指纹应被丢弃")
	}
	if !freshKept {
		t.Error("最近出现过的指纹不该被丢")
	}
}

func TestOpsPatternMapHonoursHardCap(t *testing.T) {
	now := time.Now().Unix()
	growthHub.mu.Lock()
	growthHub.patterns = map[string]*opsPatternHit{}
	// 全部都在 TTL 内，只能靠硬上限收：Last 越小越该先被丢。
	for i := 0; i < opsPatternMaxKeys+200; i++ {
		k := "actor|fp-" + strconv.Itoa(i)
		growthHub.patterns[k] = &opsPatternHit{Key: k, Last: now - int64(i)}
	}
	growthHub.pruneLocked(now)
	size := len(growthHub.patterns)
	_, newestKept := growthHub.patterns["actor|fp-0"]
	_, oldestKept := growthHub.patterns["actor|fp-"+strconv.Itoa(opsPatternMaxKeys+199)]
	growthHub.patterns = map[string]*opsPatternHit{}
	growthHub.mu.Unlock()

	if size > opsPatternMaxKeys {
		t.Fatalf("清理后仍有 %d 条，超过上限 %d", size, opsPatternMaxKeys)
	}
	if !newestKept {
		t.Error("最近出现的那条不该被丢")
	}
	if oldestKept {
		t.Error("最老的那条应该先被丢")
	}
}
