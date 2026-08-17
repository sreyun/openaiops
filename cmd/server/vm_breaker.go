package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// vmCircuitBreaker trips after consecutive failures; half-opens after cool-down.
type vmCircuitBreaker struct {
	mu            sync.Mutex
	failures      int
	openUntil     time.Time
	threshold     int
	coolDown      time.Duration
	halfOpenProbe bool
}

// state reports the breaker in words, for the diagnostics endpoint. 断路器是
// 「曲线突然只剩内存」的三种原因之一，而它此前在外部完全不可见。
func (b *vmCircuitBreaker) state() string {
	if b == nil {
		return "disabled"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.openUntil.IsZero() && time.Now().Before(b.openUntil) {
		return "open"
	}
	if b.halfOpenProbe {
		return "half-open"
	}
	if b.failures > 0 {
		return "closed(degraded)"
	}
	return "closed"
}

func newVMCircuitBreaker() *vmCircuitBreaker {
	th := 5
	if v := strings.TrimSpace(os.Getenv("AIOPS_VM_BREAKER_THRESHOLD")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			th = n
		}
	}
	cd := 30 * time.Second
	if v := strings.TrimSpace(os.Getenv("AIOPS_VM_BREAKER_COOLDOWN_SEC")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cd = time.Duration(n) * time.Second
		}
	}
	return &vmCircuitBreaker{threshold: th, coolDown: cd}
}

// allow implements a real half-open: after the cool-down exactly ONE caller gets
// through to probe, and everyone else keeps failing fast until that probe
// reports back.
//
// 原实现在冷却期满时直接把 openUntil 清零就放行，于是 halfOpenProbe 这个字段从来没被读过，
// "半开"实际是"全开"：VM 真的挂掉或变慢时，每过 30 秒就有一整批并发请求同时放出去，各自
// 卡满 15 秒查询超时才回退到内存——用户看到的就是「图有时候要转很久才出来」。单探针放行
// 之后，故障期间除了那一个探针，其余请求立刻拿到熔断错误并瞬间走内存回退。
func (b *vmCircuitBreaker) allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Now().Before(b.openUntil) {
		return false
	}
	if !b.openUntil.IsZero() {
		// Cool-down elapsed. openUntil stays set until the probe resolves, so a
		// second caller arriving now still fails fast.
		if b.halfOpenProbe {
			return false
		}
		b.halfOpenProbe = true
		return true
	}
	return true
}

func (b *vmCircuitBreaker) success() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.failures = 0
	b.halfOpenProbe = false
	b.openUntil = time.Time{}
	b.mu.Unlock()
}

func (b *vmCircuitBreaker) failure() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.halfOpenProbe {
		// The probe failed: straight back to open for another cool-down. Counting
		// up to the threshold again would let a full burst through first.
		b.halfOpenProbe = false
		b.openUntil = time.Now().Add(b.coolDown)
		b.failures = 0
		b.mu.Unlock()
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = time.Now().Add(b.coolDown)
		b.failures = 0
	}
	b.mu.Unlock()
}

func vmQueryTimeout() time.Duration {
	sec := 15
	if v := strings.TrimSpace(os.Getenv("AIOPS_VM_QUERY_TIMEOUT_SEC")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sec = n
		}
	}
	return time.Duration(sec) * time.Second
}

// doVMRequest runs an INGEST request under the write breaker.
//
// 不要把查询也走这里：写入失败会打开熔断器，而共用熔断器意味着写入一抖动，全部读图
// 请求跟着被拒 30 秒。查询请走 doVMQuery。
func (v *vmWriter) doVMRequest(req *http.Request) (*http.Response, error) {
	return v.doVMWithBreaker(req, v.writeBreaker())
}

// doVMQuery runs a READ request (query / query_range / export / label values)
// under the read breaker, so ingest hiccups can never blank the charts.
func (v *vmWriter) doVMQuery(req *http.Request) (*http.Response, error) {
	return v.doVMWithBreaker(req, v.queryBreaker())
}

// writeBreaker / queryBreaker never lazily construct: a nil-check-then-assign on
// a shared field is a data race (ingest flush and chart reads call this
// concurrently), and two racing constructions would silently split the state.
// newVMWriter always populates both; a zero-value vmWriter simply runs unguarded.
func (v *vmWriter) writeBreaker() *vmCircuitBreaker { return v.breaker }
func (v *vmWriter) queryBreaker() *vmCircuitBreaker { return v.readBreaker }

func (v *vmWriter) doVMWithBreaker(req *http.Request, b *vmCircuitBreaker) (*http.Response, error) {
	if v == nil {
		return nil, context.Canceled
	}
	if !b.allow() {
		return nil, errVMCircuitOpen
	}
	// cancel 必须跟随 **Body 的生命周期**，不能跟随本函数的返回。
	//
	// 这里原本是 `defer cancel()`：函数一返回就取消 context，而响应体是**调用方**在之后
	// 才读的（http.Client.Do 只等到响应头，正文是流式的）。于是——
	//   * 小响应侥幸成功：正文已经落在传输层缓冲里，一次读完，来不及受影响；
	//   * 大响应必然失败：主机曲线那条 query_range 要拉约 50 个指标的整段 matrix，
	//     读到一半 context 已被取消，报 "read body: context canceled"。
	// 而写入路径只看状态码、正文读失败被忽略，所以写入看起来一切正常。
	//
	// 表现出来就是：VictoriaMetrics 健康、写入成功、count() 查得到，唯独主机曲线永远
	// 拿不到数据 → 静默退回内存环。内存环的 5 分钟层有 30 天，进程活着时完全看不出来，
	// 直到一次重启才暴露成「曲线只剩重启之后的数据」。
	ctx, cancel := context.WithTimeout(req.Context(), vmQueryTimeout())
	req = req.WithContext(ctx)
	resp, err := v.httpc.Do(req)
	if err != nil {
		cancel()
		b.failure()
		return nil, err
	}
	// 所有调用方都会 defer resp.Body.Close()，因此把 cancel 挂在 Close 上既能保证
	// context 一定被释放（不泄漏），又不会在正文读完之前把它掐断。
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	if resp.StatusCode >= 500 {
		b.failure()
		return resp, nil
	}
	b.success()
	return resp, nil
}

type vmCircuitOpenError struct{}

func (vmCircuitOpenError) Error() string { return "victoria metrics circuit open" }

var errVMCircuitOpen = vmCircuitOpenError{}

// cancelOnCloseBody releases the request context when the body is closed rather
// than when the helper returns. 见 doVMWithBreaker 里的说明。
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelOnCloseBody) Close() error {
	err := c.ReadCloser.Close()
	c.once.Do(c.cancel)
	return err
}
