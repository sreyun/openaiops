package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 同一个 limit 的并发请求必须合流成**一次**数据库校验：这条查询要扫两张审计全表，
// 十个人同时开安全页就跑十遍的话，数据库会被自己人打垮。
func TestAuditVerifyGateCollapsesConcurrentCalls(t *testing.T) {
	g := &auditVerifyGate{}

	var runs int32
	release := make(chan struct{})
	run := func(ctx context.Context, limit int) (auditChainVerifyResult, error) {
		atomic.AddInt32(&runs, 1)
		<-release
		return auditChainVerifyResult{Status: "healthy", Checked: limit}, nil
	}

	const callers = 8
	var wg sync.WaitGroup
	results := make([]auditChainVerifyResult, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = g.run(context.Background(), 200, run)
		}(i)
	}
	// 等大家都排到闸门后面再放行，否则可能是"先后串行跑完"而不是真的合流。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		running := g.running
		g.mu.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("并发 %d 次请求跑了 %d 遍全表校验，应当只跑 1 遍", callers, got)
	}
	for i, r := range results {
		if r.Status != "healthy" || r.Checked != 200 {
			t.Fatalf("合流的第 %d 个调用没拿到结果：%+v", i, r)
		}
	}
}

// 参数不同又撞上正在跑的：直接告诉调用方"忙"，而不是排队再跑一遍全表。
func TestAuditVerifyGateRejectsDifferentLimitWhileBusy(t *testing.T) {
	g := &auditVerifyGate{}

	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_, _ = g.run(context.Background(), 200, func(context.Context, int) (auditChainVerifyResult, error) {
			close(started)
			<-release
			return auditChainVerifyResult{Status: "healthy"}, nil
		})
	}()
	<-started
	_, err := g.run(context.Background(), 500, func(context.Context, int) (auditChainVerifyResult, error) {
		t.Error("不该在已有校验在跑时又发起一次")
		return auditChainVerifyResult{}, nil
	})
	if !errors.Is(err, errAuditVerifyBusy) {
		t.Fatalf("应当返回 busy，实际 %v", err)
	}
	close(release)
}

// 成功结果进缓存（重复点击不再打数据库），失败结果不进——数据库刚恢复不该再干等 30 秒。
func TestAuditVerifyGateCachesSuccessOnly(t *testing.T) {
	g := &auditVerifyGate{}

	var runs int
	ok := func(context.Context, int) (auditChainVerifyResult, error) {
		runs++
		return auditChainVerifyResult{Status: "healthy"}, nil
	}
	for i := 0; i < 3; i++ {
		if _, err := g.run(context.Background(), 200, ok); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 1 {
		t.Fatalf("成功结果没有被缓存：跑了 %d 遍", runs)
	}

	boom := errors.New("db down")
	var failRuns int
	fail := func(context.Context, int) (auditChainVerifyResult, error) {
		failRuns++
		return auditChainVerifyResult{}, boom
	}
	for i := 0; i < 3; i++ {
		if _, err := g.run(context.Background(), 300, fail); !errors.Is(err, boom) {
			t.Fatalf("第 %d 次没有把错误透出来：%v", i, err)
		}
	}
	if failRuns != 3 {
		t.Fatalf("失败结果被缓存了：只跑了 %d 遍", failRuns)
	}
}

// 缓存键是 limit，别让 1..5000 的任意取值把缓存撑成一张只增不减的表。
func TestAuditVerifyGatePrunesExpiredCache(t *testing.T) {
	g := &auditVerifyGate{}

	stale := time.Now().Add(-2 * auditVerifyCacheTTL)
	g.mu.Lock()
	g.cache = map[int]auditVerifyOutcome{}
	for i := 1; i <= 50; i++ {
		g.cache[i] = auditVerifyOutcome{at: stale}
	}
	g.mu.Unlock()

	if _, err := g.run(context.Background(), 999, func(context.Context, int) (auditChainVerifyResult, error) {
		return auditChainVerifyResult{Status: "healthy"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	n := len(g.cache)
	g.mu.Unlock()
	if n != 1 {
		t.Fatalf("过期项没有被清掉：缓存里还有 %d 条", n)
	}
}
