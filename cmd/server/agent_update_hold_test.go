package main

import (
	"testing"
	"time"
)

func newHoldTestServer() *Server {
	return &Server{agentUpdates: newAgentUpdateManager()}
}

func inFlightStamp(t *testing.T, m *agentUpdateManager, hostID string) (int64, bool) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	ts, ok := m.inFlight[hostID]
	return ts, ok
}

func setInFlightStamp(m *agentUpdateManager, hostID string, ts int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight[hostID] = ts
}

// 一台主机的完整升级阶梯（exec 最长 600s ×3 → 5 分钟 verify → legacy 救援 exec →
// 再 5 分钟 verify）远长于 agentUpdateSoftRetrySec(360s)，而 in-flight 时间戳只在入队
// 和进入 pending_verify 时各打一次。不按住它，软重试会在升级还没跑完时再下发一次
// ——Windows 上就是两个助手抢同一次换版：一个刚停服务，另一个正在覆盖同一个 PE。
func TestHoldHostInFlightBlocksSoftRetryWhileWorking(t *testing.T) {
	prev := agentUpdateHoldInterval
	agentUpdateHoldInterval = 5 * time.Millisecond
	t.Cleanup(func() { agentUpdateHoldInterval = prev })

	s := newHoldTestServer()
	const host = "h-win-01"
	// 时间戳已经比软重试窗口更老：不刷新的话，下一次入队就会被放行。
	stale := time.Now().Unix() - agentUpdateSoftRetrySec - 40
	setInFlightStamp(s.agentUpdates, host, stale)

	release := s.holdHostInFlight(host)
	time.Sleep(80 * time.Millisecond) // 足够跑过好几个心跳
	if s.agentUpdates.tryMarkInFlightOrSoftRetry(host, true) {
		t.Fatal("升级仍在进行中却被软重试放行了：同一台主机会并发跑两次换版")
	}
	if ts, ok := inFlightStamp(t, s.agentUpdates, host); !ok || ts <= stale {
		t.Fatalf("hold 没有刷新 in-flight 时间戳：ts=%d stale=%d ok=%v", ts, stale, ok)
	}

	release()
	// 松手之后，同样陈旧的时间戳必须重新允许软重试——否则修好并发反而变成拒绝升级。
	setInFlightStamp(s.agentUpdates, host, stale)
	time.Sleep(30 * time.Millisecond)
	if !s.agentUpdates.tryMarkInFlightOrSoftRetry(host, true) {
		t.Fatal("hold 释放后仍在挡软重试：主机会被白白冻结")
	}
}

// 调用方常常先 clearInFlight 再 release（defer 的顺序天然如此）。一次晚到的心跳如果
// 能凭空把条目写回去，就把并发缺陷换成了「主机被冻结 30 分钟不给升级」的新缺陷。
func TestHoldNeverResurrectsAReleasedHost(t *testing.T) {
	prev := agentUpdateHoldInterval
	agentUpdateHoldInterval = 5 * time.Millisecond
	t.Cleanup(func() { agentUpdateHoldInterval = prev })

	s := newHoldTestServer()
	const host = "h-win-02"
	setInFlightStamp(s.agentUpdates, host, time.Now().Unix())
	release := s.holdHostInFlight(host)

	s.agentUpdates.clearInFlight(host)
	time.Sleep(60 * time.Millisecond) // 心跳仍在跑
	if _, ok := inFlightStamp(t, s.agentUpdates, host); ok {
		t.Fatal("心跳把已经放行的主机重新标成 in-flight，会冻结它整个硬冷却期")
	}
	release()
	if _, ok := inFlightStamp(t, s.agentUpdates, host); ok {
		t.Fatal("release 之后依然出现了 in-flight 条目")
	}
}

// release 会被 defer 调用，也可能因为提前返回被调用两次；重复调用不得 panic 或死锁。
func TestHoldReleaseIsIdempotent(t *testing.T) {
	s := newHoldTestServer()
	release := s.holdHostInFlight("h-win-03")
	done := make(chan struct{})
	go func() {
		release()
		release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("重复 release 卡住了")
	}
}

// 没有 hostID / 没有管理器时也必须给出一个可安全调用的 release。
func TestHoldHostInFlightDegradesSafely(t *testing.T) {
	s := newHoldTestServer()
	s.holdHostInFlight("")()
	var nilSrv *Server
	nilSrv.holdHostInFlight("h")()
	(&Server{}).holdHostInFlight("h")()
}
