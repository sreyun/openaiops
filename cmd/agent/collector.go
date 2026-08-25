package main

import (
	"log/slog"

	"aiops-monitor/shared"
)

// runSafe 执行 fn 并捕获 panic，避免任一采集协程的单点 panic 打崩整个 agent（Go 里任一
// 未捕获 panic 会杀死整个进程 → 被 keepalive 反复重启，表现为"安装后不稳定/频繁重启"）。
// 用法：在【每轮迭代体】里调用（如 for range ticker.C { runSafe("xxx", work) }），
// 这样单次崩溃被吞掉、循环继续，而不是整个采集器退出。解析不可信网络输入的采集器尤须如此。
func runSafe(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("采集协程 panic 已恢复（不影响其它采集）", "collector", name, "panic", r)
		}
	}()
	fn()
}

// Collector gathers base system metrics natively. Implementations are
// platform-specific and selected at build time via build tags:
//   - collector_linux.go   (procfs + syscall)
//   - collector_windows.go (Win32 API via syscall.NewLazyDLL)
//   - collector_darwin.go  (sysctl + system tools)
//   - collector_other.go   (stub; base metrics come from a core plugin such as
//     plugins/core_metrics.py via psutil)
type Collector interface {
	Collect() (shared.Metrics, error)
	Supported() bool
	Name() string
}

// ---- rounding + rate helpers shared by every native collector ----

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// rate computes a per-second rate from two monotonic counters. It returns 0
// on counter wrap (cur < prev) or a non-positive interval, so a freshly primed
// collector never emits a bogus spike.
func rate(cur, prev uint64, elapsed float64) float64 {
	if cur < prev || elapsed <= 0 {
		return 0
	}
	return float64(cur-prev) / elapsed
}

// maxProcessNames 是每次上报携带的去重进程名上限（三个平台共用）。
//
// 原来是 256 且**静默截断**：跑容器的宿主机上不同进程名轻易超过 256 个，被截掉的
// 那些名字对应的进程监控就会全部误报「进程未运行」。名字只是去重后的短字符串，
// 1024 个也不过十几 KB；服务端只在 Latest 上保留一份，历史环不存。
const maxProcessNames = 1024
