package main

import (
	"net/http"
	"sync"
	"time"
)

// ============================================================================
// VictoriaMetrics 读写健康诊断
//
// 起因：主机曲线上的降级提示只能说到「持久化时序库没有返回数据（查询失败 / 熔断 /
// 该窗口确实为空）」——三种原因混在一句话里，而它们的处置完全不同：
//
//   * 查询失败 → VM 地址不通 / 超时 / 返回非 200，要查网络与 VM 进程；
//   * 熔断     → 读路径被断路器挡住，说明**之前**已经连续失败，要看断路器为什么开；
//   * 窗口为空 → VM 活着、也答了，但那段时间确实没有点 —— 问题在**写入侧**，
//                和读路径完全无关。
//
// 前两种查读，第三种查写，方向相反。把它们混成一句话，等于把人推回猜测。
// 这里做两件事：把原因拆开逐级上报，以及把**写入侧**的健康（最近一次成功写入是什么
// 时候、最近一次失败是什么、断路器什么状态）暴露出来——「到底写进去没有」必须能一眼
// 看到，而不是靠推理。
// ============================================================================

// history fallback reasons, reported via the X-AIOps-History-Reason header.
const (
	historyReasonNone       = ""             // VM answered with data
	historyReasonDisabled   = "vm_disabled"  // VM not configured at all
	historyReasonBreaker    = "read_breaker" // read circuit breaker is open
	historyReasonQueryError = "query_error"  // transport error / non-200 / bad body
	historyReasonEmpty      = "empty_window" // VM answered fine, the window has no points
)

// vmDiag records the last outcome of each direction. Kept tiny and mutex-guarded:
// it is written on every flush and read only by humans.
type vmDiag struct {
	mu sync.Mutex

	lastWriteOKAt   int64
	lastWriteErr    string
	lastWriteErrAt  int64
	writtenBatches  uint64
	lastReadOKAt    int64
	lastReadErr     string
	lastReadErrAt   int64
	lastReadEmptyAt int64
}

func (d *vmDiag) writeOK() {
	d.mu.Lock()
	d.lastWriteOKAt = time.Now().Unix()
	d.writtenBatches++
	d.mu.Unlock()
}

func (d *vmDiag) writeErr(err string) {
	if err == "" {
		return
	}
	d.mu.Lock()
	d.lastWriteErr, d.lastWriteErrAt = truncateRun(err, 300), time.Now().Unix()
	d.mu.Unlock()
}

func (d *vmDiag) readOK() {
	d.mu.Lock()
	d.lastReadOKAt = time.Now().Unix()
	d.mu.Unlock()
}

func (d *vmDiag) readErr(err string) {
	if err == "" {
		return
	}
	d.mu.Lock()
	d.lastReadErr, d.lastReadErrAt = truncateRun(err, 300), time.Now().Unix()
	d.mu.Unlock()
}

func (d *vmDiag) readEmpty() {
	d.mu.Lock()
	d.lastReadEmptyAt = time.Now().Unix()
	d.mu.Unlock()
}

func (d *vmDiag) snapshot() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return map[string]any{
		"last_write_ok_at":   d.lastWriteOKAt,
		"last_write_err":     d.lastWriteErr,
		"last_write_err_at":  d.lastWriteErrAt,
		"written_batches":    d.writtenBatches,
		"last_read_ok_at":    d.lastReadOKAt,
		"last_read_err":      d.lastReadErr,
		"last_read_err_at":   d.lastReadErrAt,
		"last_read_empty_at": d.lastReadEmptyAt,
	}
}

// handleVMDiagnostics answers "写进去了吗 / 读得到吗" in one request.
//
// 它刻意**现场探一次 VM**，而不是只回报缓存的状态：用户看这个接口的时刻，正是需要
// 知道「此时此刻」的时候。probe 里那条 count() 是判断「VM 里到底有没有主机指标」的
// 最短路径——为空就说明问题在写入侧，再去看 last_write_err 与断路器。
func (s *Server) handleVMDiagnostics(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.vm == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vm writer unavailable"})
		return
	}
	c := s.cfg.VMConfig()
	out := map[string]any{
		"enabled":       c.Enabled,
		"url":           c.URL,
		"write_breaker": s.vm.writeBreaker().state(),
		"read_breaker":  s.vm.queryBreaker().state(),
		"dropped":       s.vm.dropped.Load(),
	}
	for k, v := range s.vm.diag.snapshot() {
		out[k] = v
	}
	if c.Enabled && c.URL != "" {
		series, ok := s.vm.vmInstantScalar(`count(aiops_cpu_percent)`)
		out["probe_series_count"] = series
		out["probe_ok"] = ok
		depth, depthLabel := s.vm.probeRetentionDepth()
		out["oldest_data_age_sec"] = depth
		out["oldest_data_label"] = depthLabel
		switch {
		case !ok:
			out["verdict"] = "VictoriaMetrics 查询不可用 —— 看 last_read_err 与 read_breaker"
		case series == 0:
			out["verdict"] = "VictoriaMetrics 可达，但里面没有任何主机 CPU 指标 —— 问题在写入侧或数据已被清空"
		case depth <= 0:
			out["verdict"] = "VictoriaMetrics 只有『刚刚』的数据：连 1 小时前都查不到。" +
				"写入正常说明链路是通的，那么历史是**被清掉的**——检查 VM 数据目录是否在发版时随部署目录一起被删（compose 默认挂 ./vm-data）"
		default:
			out["verdict"] = "VictoriaMetrics 最早的数据在 " + depthLabel + " 之前；比这更早的窗口查不到属正常"
		}
	} else {
		out["verdict"] = "未启用 VictoriaMetrics"
	}
	writeJSON(w, http.StatusOK, out)
}

// vmInstantScalar runs an instant PromQL query and returns the first sample value.
// ok=false means the query itself failed (unreachable / breaker / non-200);
// ok=true with 0 means VM answered and nothing matched — a very different verdict.
func (v *vmWriter) vmInstantScalar(promql string) (float64, bool) {
	series, ok := v.vmQueryVectorAt(promql, time.Now().Unix())
	if !ok {
		return 0, false
	}
	for _, s := range series {
		return s.Value, true
	}
	return 0, true
}

// historyReasonHint turns a fallback reason into operator-facing guidance.
func historyReasonHint(reason string) string {
	switch reason {
	case historyReasonBreaker:
		return "读路径断路器已打开（此前连续查询失败）"
	case historyReasonQueryError:
		return "查询 VictoriaMetrics 失败（不可达 / 超时 / 非 200）"
	case historyReasonEmpty:
		return "VictoriaMetrics 已应答，但该时间窗内没有数据 —— 问题在写入侧"
	case historyReasonDisabled:
		return "未启用 VictoriaMetrics"
	}
	return ""
}

// probeRetentionDepth answers the one question that separates「没写进去」from
// 「写进去了但被删了」：**VictoriaMetrics 里最早的数据是什么时候**。
//
// 写入正常 + 读取正常 + 序列数不为零，却依然只看得到重启之后的曲线——这三者同时成立时，
// 唯一自洽的解释就是库里真的只剩下重启之后的点。与其让人去猜，不如直接在几个时间刻度上
// 各探一次：返回「最早还能查到数据的那个刻度」。
//
// 用 count() 在**过去某一时刻**求值（VM 的 /api/v1/query 支持 time=），而不是 range 查询：
// 一次请求一个标量，最便宜，也不受 step / 抽样的影响。
func (v *vmWriter) probeRetentionDepth() (int64, string) {
	type mark struct {
		sec   int64
		label string
	}
	marks := []mark{
		{30 * 86400, "30 天"},
		{7 * 86400, "7 天"},
		{86400, "24 小时"},
		{6 * 3600, "6 小时"},
		{3600, "1 小时"},
	}
	now := time.Now().Unix()
	for _, m := range marks {
		series, ok := v.vmQueryVectorAt(`count(aiops_cpu_percent)`, now-m.sec)
		if !ok {
			continue
		}
		for _, s := range series {
			if s.Value > 0 {
				return m.sec, m.label
			}
		}
	}
	return 0, "1 小时以内"
}
