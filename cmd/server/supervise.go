package main

import (
	"log/slog"
	"runtime/debug"
	"time"
)

// ============================================================================
// 常驻后台循环的看护
//
// Go 的规则很硬：**任何一个 goroutine 里未捕获的 panic 会直接终止整个进程**。而这个
// 服务端在启动时拉起十几条常驻循环——告警评估、拨测、API 探测、指标抓取、PromQL 规则、
// 剧本调度、SLO 评估、AI 巡检、值班日报、VM 写入泵、Agent 自动升级扫描、CI/CD 看护。
// 它们全都在处理**外部来的、不可信的数据**：Agent 上报的字段、用户配的 PromQL 与剧本、
// 第三方 CI 的 JSON、VM 的响应。其中任意一条上出现一次空指针、一次越界、一次类型断言
// 失败，整台监控服务端就没了——而它恰恰是所有人用来发现故障的那个东西。
//
// 全仓 141 处 goroutine 启动点只有 7 处 recover，且没有统一封装。这里补上：
//
//   - superviseLoop：给常驻循环用。panic 后记录堆栈并**重启**它。监控循环挂掉不该
//     是永久的——重启一次通常就能跨过那条坏数据，而进程活着比这一轮采集更重要。
//   - safeGo：给一次性后台任务用。panic 只吞掉这一次，不牵连进程。
//
// 刻意不做的事：不吞掉 panic 就当无事发生。每次都按 error 级别记录函数名与完整堆栈，
// 否则这层看护就变成了「把 bug 藏起来」，比崩溃更糟。
// ============================================================================

// superviseRestartDelay 是循环崩溃后重启前的等待。
// 太短会在持续 panic 时变成刷屏 + 空转；太长又会让监控出现长时间盲区。
var superviseRestartDelay = 5 * time.Second

// onPlatformPanic 把 panic 送进平台自身故障归口（self_fault.go），由 Server 启动时装配。
//
// 为什么要有这个钩子：这两个函数是包级的，拿不到 *Server；而 panic 恰恰是平台自身最
// 严重的一类故障——一条常驻循环反复崩溃意味着某个监控能力已经静默失效了，此前它只有
// 一行 slog.Error。日志是给已经知道要查什么的人看的；不知道该查什么的时候，日志等于
// 不存在。接进归口之后，它会像主机故障一样开事件、被 AI 诊断、进闭环。
//
// 用变量而不是接口：这条路径要能在任何 goroutine 里被调用，且绝不能因为自身出错再引发
// 一次 panic（调用点都做了 nil 判断）。
var onPlatformPanic func(name, kind string, r any, stack string)

func reportPanic(name, kind string, r any, stack string) {
	if onPlatformPanic == nil {
		return
	}
	// 钩子自己 panic 不能把「保住进程」这件事反过来搞砸。
	defer func() { _ = recover() }()
	onPlatformPanic(name, kind, r, stack)
}

// superviseLoop runs fn forever, restarting it after a panic.
func superviseLoop(name string, fn func()) {
	for {
		if done := runSupervised(name, fn); done {
			return
		}
		time.Sleep(superviseRestartDelay)
		slog.Warn("后台循环已重启", "loop", name)
	}
}

// runSupervised runs fn once; returns true when it returned normally (i.e. the
// loop decided to stop) and false when it died of a panic.
func runSupervised(name string, fn func()) (returnedNormally bool) {
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			slog.Error("后台循环 panic（进程已保住，将重启该循环）",
				"loop", name, "panic", r, "stack", stack)
			reportPanic(name, "loop_panic", r, stack)
			returnedNormally = false
		}
	}()
	fn()
	return true
}

// safeGo runs a one-shot background task whose panic must not take the process
// with it. 用于「每个事件一个 goroutine」这类路径：单次失败只丢这一次。
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				slog.Error("后台任务 panic（已隔离，不影响进程）",
					"task", name, "panic", r, "stack", stack)
				reportPanic(name, "task_panic", r, stack)
			}
		}()
		fn()
	}()
}
