package main

import (
	"fmt"
	"strings"

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
	return strings.Join(hostHistoryMetricNames, "|")
}

func hostHistoryRangeExpr(hostID string) string {
	return fmt.Sprintf(`{__name__=~"%s",host=%q,path!~"%s"}`, hostHistoryNameRE(), hostID, ephemeralDiskPathRE)
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
