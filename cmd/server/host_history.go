package main

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// hostHistoryMetricNames is the allowlist of metric names host-history charts
// read from VictoriaMetrics. Must stay ≥50 (host gauges + derived + GPU +
// per-volume disk + conn + hardware/netflow aggregates). Never use aiops_.* —
// that pulls every docker overlay / k8s PVC path as its own series.
var hostHistoryMetricNames = []string{
	"aiops_cpu_percent", "aiops_cpu_cores", "aiops_cpu_idle_percent",
	"aiops_mem_percent", "aiops_mem_used_bytes", "aiops_mem_total_bytes", "aiops_mem_free_bytes", "aiops_mem_free_percent",
	"aiops_swap_percent", "aiops_swap_used_bytes", "aiops_swap_total_bytes", "aiops_swap_free_bytes",
	"aiops_disk_percent", "aiops_disk_used_bytes", "aiops_disk_total_bytes", "aiops_disk_free_bytes", "aiops_disk_free_percent",
	"aiops_disk_io_util_percent", "aiops_disk_read_rate", "aiops_disk_write_rate", "aiops_disk_read_iops", "aiops_disk_write_iops",
	"aiops_uptime_seconds", "aiops_proc_count",
	"aiops_net_sent_rate", "aiops_net_recv_rate", "aiops_net_total_rate", "aiops_net_conns",
	"aiops_load1", "aiops_load5", "aiops_load15", "aiops_load1_per_core", "aiops_load5_per_core", "aiops_load15_per_core",
	"aiops_api_avail_percent", "aiops_api_avg_resp_ms", "aiops_api_p95_resp_ms", "aiops_api_throughput_rps",
	"aiops_task_fail_count", "aiops_task_timeout_sec",
	"aiops_gpus_count", "aiops_mounts_count",
	"aiops_gpu_util_percent", "aiops_gpu_temp_c", "aiops_gpu_mem_percent",
	"aiops_gpu_mem_used_bytes", "aiops_gpu_mem_free_bytes", "aiops_gpu_mem_total_bytes",
	"aiops_disk_vol_percent", "aiops_disk_vol_used_bytes", "aiops_disk_vol_total_bytes",
	"aiops_net_conn_count",
	"aiops_hardware_health_score", "aiops_hardware_power_watts",
	"aiops_netflow_total_bytes", "aiops_netflow_total_packets", "aiops_netflow_flows", "aiops_netflow_dropped",
}

// ephemeralDiskPathRE drops overlay/kubelet PVC churn from disk_vol series.
// Series without a path label (scalars) still match a negative path regex.
const ephemeralDiskPathRE = `.*(overlay2|/docker/|/kubelet/pods|containerd).*`

func hostHistoryNameRE() string {
	return hostHistoryNameREOf(nil)
}

func hostHistoryNameREOf(names []string) string {
	if len(names) == 0 {
		names = hostHistoryMetricNames
	}
	return strings.Join(names, "|")
}

func hostHistoryRangeExpr(hostID string) string {
	return hostHistoryRangeExprNames(hostID, nil)
}

func hostHistoryRangeExprNames(hostID string, names []string) string {
	return fmt.Sprintf(`{__name__=~"%s",host=%q,path!~"%s"}`, hostHistoryNameREOf(names), hostID, ephemeralDiskPathRE)
}

func queryHistoryAllowsExportFallback(span int64) bool {
	return span > 0 && span <= 24*3600
}

const memHistoryOverlaySec int64 = 15 * 60

func spliceHistory(base, overlay []shared.Sample) []shared.Sample {
	if len(overlay) == 0 {
		return base
	}
	if len(base) == 0 {
		return overlay
	}
	cut := overlay[0].Timestamp
	out := make([]shared.Sample, 0, len(base)+len(overlay))
	for _, s := range base {
		if s.Timestamp < cut {
			out = append(out, s)
		}
	}
	return append(out, overlay...)
}

func recentHistoryTail(samples []shared.Sample, maxAgeSec int64) []shared.Sample {
	if len(samples) == 0 || maxAgeSec <= 0 {
		return nil
	}
	cut := samples[len(samples)-1].Timestamp - maxAgeSec
	i := 0
	for i < len(samples) && samples[i].Timestamp < cut {
		i++
	}
	if i == 0 {
		return samples
	}
	if i >= len(samples) {
		return nil
	}
	return samples[i:]
}

// forecastFitLookback is extra history pulled from VictoriaMetrics so a short
// visible window (1h chart, 3–5 min dashboard panel) still has enough points
// to fit a forecast. Caps at 7d.
func forecastFitLookback(rangeSec int64) int64 {
	if rangeSec < 0 {
		rangeSec = 0
	}
	lookback := rangeSec * 3
	if lookback < 3600 {
		lookback = 3600
	}
	if lookback > 7*24*3600 {
		lookback = 7 * 24 * 3600
	}
	return lookback
}

// vmNamesForMetricKeys maps AI/chart metric keys (cpu, mem_percent, …) onto the
// host-history allowlist. Empty keys → nil (caller uses the full ≥50-name set).
func vmNamesForMetricKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	allow := make(map[string]bool, len(hostHistoryMetricNames))
	for _, n := range hostHistoryMetricNames {
		allow[n] = true
	}
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if n == "" || seen[n] || !allow[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	addFamily := func(names ...string) {
		for _, n := range names {
			add(n)
		}
	}
	for _, raw := range keys {
		k := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "aiops_")
		if k == "" {
			continue
		}
		switch {
		case k == "cpu" || k == "cpu_percent":
			addFamily("aiops_cpu_percent", "aiops_cpu_cores", "aiops_cpu_idle_percent")
		case k == "memory" || k == "mem" || k == "ram" || k == "mem_percent":
			addFamily("aiops_mem_percent", "aiops_mem_used_bytes", "aiops_mem_total_bytes", "aiops_mem_free_bytes", "aiops_mem_free_percent")
		case k == "swap" || k == "swap_percent":
			addFamily("aiops_swap_percent", "aiops_swap_used_bytes", "aiops_swap_total_bytes", "aiops_swap_free_bytes")
		case k == "disk" || k == "disk_percent":
			addFamily("aiops_disk_percent", "aiops_disk_used_bytes", "aiops_disk_total_bytes", "aiops_disk_free_bytes", "aiops_disk_free_percent",
				"aiops_disk_vol_percent", "aiops_disk_vol_used_bytes", "aiops_disk_vol_total_bytes")
		case strings.HasPrefix(k, "disk_vol") || strings.HasPrefix(k, "disk_/"):
			addFamily("aiops_disk_vol_percent", "aiops_disk_vol_used_bytes", "aiops_disk_vol_total_bytes")
		case k == "io":
			addFamily("aiops_disk_io_util_percent", "aiops_disk_read_rate", "aiops_disk_write_rate", "aiops_disk_read_iops", "aiops_disk_write_iops")
		case k == "network" || k == "net":
			addFamily("aiops_net_sent_rate", "aiops_net_recv_rate", "aiops_net_total_rate", "aiops_net_conns", "aiops_net_conn_count")
		case k == "load":
			addFamily("aiops_load1", "aiops_load5", "aiops_load15", "aiops_load1_per_core", "aiops_load5_per_core", "aiops_load15_per_core", "aiops_proc_count")
		case strings.HasPrefix(k, "gpu"):
			addFamily("aiops_gpu_util_percent", "aiops_gpu_temp_c", "aiops_gpu_mem_percent",
				"aiops_gpu_mem_used_bytes", "aiops_gpu_mem_free_bytes", "aiops_gpu_mem_total_bytes", "aiops_gpus_count")
		default:
			add("aiops_" + k)
		}
	}
	return out
}

// sampleGauge reads one host-chart / AI metric key from a joined Sample.
func sampleGauge(s shared.Sample, key string) (float64, bool) {
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(key)), "aiops_") {
	case "cpu", "cpu_percent":
		return s.CPUPercent, true
	case "cpu_cores":
		return float64(s.CPUCores), true
	case "memory", "mem", "mem_percent":
		return s.MemPercent, true
	case "mem_used_bytes":
		return float64(s.MemUsed), true
	case "mem_total_bytes":
		return float64(s.MemTotal), true
	case "swap", "swap_percent":
		return s.SwapPercent, true
	case "disk", "disk_percent":
		return s.DiskPercent, true
	case "disk_used_bytes":
		return float64(s.DiskUsed), true
	case "disk_total_bytes":
		return float64(s.DiskTotal), true
	case "disk_io_util_percent":
		return s.DiskIOUtilPercent, true
	case "disk_read_rate":
		return s.DiskReadRate, true
	case "disk_write_rate":
		return s.DiskWriteRate, true
	case "disk_read_iops":
		return s.DiskReadIOPS, true
	case "disk_write_iops":
		return s.DiskWriteIOPS, true
	case "uptime_seconds":
		return float64(s.Uptime), true
	case "proc_count":
		return float64(s.ProcCount), true
	case "net_sent_rate":
		return s.NetSentRate, true
	case "net_recv_rate":
		return s.NetRecvRate, true
	case "net_total_rate":
		return s.NetSentRate + s.NetRecvRate, true
	case "net_conns":
		return float64(s.NetConns), true
	case "network":
		return (s.NetRecvRate + s.NetSentRate) / 1048576, true
	case "io":
		return (s.DiskReadRate + s.DiskWriteRate) / 1048576, true
	case "load", "load1":
		return s.Load1, true
	case "load5":
		return s.Load5, true
	case "load15":
		return s.Load15, true
	case "api_avail_percent":
		return s.APIAvailPercent, true
	case "api_avg_resp_ms":
		return s.APIAvgRespMs, true
	case "api_p95_resp_ms":
		return s.APIP95RespMs, true
	case "api_throughput_rps":
		return s.APIThroughputRPS, true
	case "task_fail_count":
		return float64(s.TaskFailCount), true
	case "task_timeout_sec":
		return s.TaskTimeoutSec, true
	default:
		return 0, false
	}
}

func prependHistoryPoints(pts [][2]float64, extra []shared.Sample, key string) [][2]float64 {
	if len(extra) == 0 || len(pts) == 0 {
		return pts
	}
	cut := pts[0][0]
	prefix := make([][2]float64, 0, len(extra))
	for _, s := range extra {
		if float64(s.Timestamp) >= cut {
			continue
		}
		if v, ok := sampleGauge(s, key); ok {
			prefix = append(prefix, [2]float64{float64(s.Timestamp), v})
		}
	}
	if len(prefix) == 0 {
		return pts
	}
	return append(prefix, pts...)
}

// loadDurableHostHistory is the single host time-series read path:
// VictoriaMetrics first, last 15 minutes of RAM as a nested-inventory overlay,
// RAM-only if VM is empty/down. names=nil uses the full ≥50-name allowlist.
func (s *Server) loadDurableHostHistory(hostID string, from, to int64, names []string) (samples []shared.Sample, hostOK bool) {
	out, _, ok := s.loadDurableHostHistorySource(hostID, from, to, names)
	return out, ok
}

// historySource labels where a host-history response actually came from, so a
// silent degradation is visible instead of just looking like "the chart shrank".
const (
	historySourceRAM      = "ram"          // VM disabled — RAM ring is the only store
	historySourceVM       = "vm+ram"       // normal: VM history + live RAM tail
	historySourceFallback = "ram-fallback" // VM enabled but the read failed
)

// loadDurableHostHistorySource is loadDurableHostHistory plus the provenance of
// the result.
//
// VM 读失败时这里会退回内存环，而内存只有 raw 1200 / 1m 2880 / 5m 8640 个点：24h、7d 这类
// 窗口会突然只剩很短一段，前端画出来就是「仅覆盖 x/y」甚至空图。以前这一步完全静默——
// 接口照样 200，运维看到的只有"曲线一会有一会没有"，无从判断是 VM 挂了、熔断了，还是本来
// 就没数据。返回 source 让调用方能把它放进响应头并打日志。
func (s *Server) loadDurableHostHistorySource(hostID string, from, to int64, names []string) (samples []shared.Sample, source string, hostOK bool) {
	if s == nil || s.store == nil || strings.TrimSpace(hostID) == "" {
		return nil, historySourceRAM, false
	}
	mem, hostOK := s.store.GetHistory(hostID, from, to)
	if !hostOK {
		return nil, historySourceRAM, false
	}
	if !s.vm.enabled() {
		return mem, historySourceRAM, true
	}
	vm, ok := s.vm.queryHistoryFilter(hostID, from, to, names)
	if ok && len(vm) > 0 {
		return spliceHistory(vm, recentHistoryTail(mem, memHistoryOverlaySec)), historySourceVM, true
	}
	return mem, historySourceFallback, true
}

// historyFallbackWarnEvery throttles the degraded-read warning. Chart polls are
// frequent and a VM outage hits every one of them; logging each would bury the
// signal in its own noise.
const historyFallbackWarnEvery = 60 * time.Second

var (
	historyFallbackWarnMu sync.Mutex
	historyFallbackWarnAt time.Time
)

func (s *Server) warnHistoryFallback(hostID string, from, to int64) {
	historyFallbackWarnMu.Lock()
	now := time.Now()
	if now.Sub(historyFallbackWarnAt) < historyFallbackWarnEvery {
		historyFallbackWarnMu.Unlock()
		return
	}
	historyFallbackWarnAt = now
	historyFallbackWarnMu.Unlock()
	slog.Warn("主机历史读取回退到内存环：VictoriaMetrics 未返回数据（查询失败/熔断/该窗口无数据）。"+
		"内存只保留 raw 1200 / 1m 2880 / 5m 8640 点，长窗口会明显缩水",
		"host", shortID(hostID), "window_sec", to-from)
}

func formatHostTrendLine(samples []shared.Sample, hours int) string {
	if len(samples) < 2 {
		return ""
	}
	third := len(samples) / 3
	if third < 1 {
		third = 1
	}
	avg := func(ss []shared.Sample, get func(shared.Sample) float64) float64 {
		var sum float64
		for _, x := range ss {
			sum += get(x)
		}
		return sum / float64(len(ss))
	}
	early, late := samples[:third], samples[len(samples)-third:]
	return fmt.Sprintf("近%dh趋势：CPU %.1f→%.1f · Mem %.1f→%.1f · Disk %.1f→%.1f · Load1 %.2f→%.2f（%d点）",
		hours,
		avg(early, func(x shared.Sample) float64 { return x.CPUPercent }),
		avg(late, func(x shared.Sample) float64 { return x.CPUPercent }),
		avg(early, func(x shared.Sample) float64 { return x.MemPercent }),
		avg(late, func(x shared.Sample) float64 { return x.MemPercent }),
		avg(early, func(x shared.Sample) float64 { return x.DiskPercent }),
		avg(late, func(x shared.Sample) float64 { return x.DiskPercent }),
		avg(early, func(x shared.Sample) float64 { return x.Load1 }),
		avg(late, func(x shared.Sample) float64 { return x.Load1 }),
		len(samples))
}
