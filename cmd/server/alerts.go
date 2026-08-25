package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Thresholds define when a base metric is warning / critical. A production
// version would load these per-host / per-rule and require a sustained
// duration before firing. Plugin-generated findings arrive as Events instead
// and are surfaced separately from these threshold alerts.
//
// Three preset profiles are available (v5.4.1):
//   - ConservativeThresholds() — sensitive, for production-critical systems
//   - StandardThresholds()    — recommended default, balanced noise/sensitivity
//   - RelaxedThresholds()     — low-noise, for dev/staging environments
type Thresholds struct {
	CPUWarn, CPUCrit float64
	MemWarn, MemCrit float64
	// MemFreeFloorGB 是内存告警的「并且」条件：使用率过线，但剩余内存仍不低于这个数
	// （GB）时不告警。512G 的机器到 90% 还剩 50G，这时候叫醒值班的人是纯噪声；小机器
	// 上 90% 只剩 800M 才是真事故——同一个百分比在不同规格的机器上根本不是同一件事。
	// 0 = 不启用（保持纯百分比判定，与历史行为一致）。
	MemFreeFloorGB           float64
	DiskWarn, DiskCrit       float64
	DiskIOWarn, DiskIOCrit   float64
	IOPSWarn, IOPSCrit       float64
	GPUWarn, GPUCrit         float64 // GPU 核心算力使用率 %
	GPUTempWarn, GPUTempCrit float64 // GPU 温度 °C
	GPUMemWarn, GPUMemCrit   float64 // GPU 显存占用率 %
	LoadWarn, LoadCrit       float64 // 按 CPU 核心数倍率
	ProcWarn                 float64 // 进程数突增/突降比例阈值
	ConnWarn, ConnCrit       float64 // 主机连接数（TCP+UDP 总数）
	OfflineAfter             time.Duration
	// ---- 拨测监控阈值（Ping / TCP / HTTP / 进程）----
	CheckPingLossWarn, CheckPingLossCrit       float64 // 丢包率 %
	CheckPingLatencyWarn, CheckPingLatencyCrit float64 // 平均延迟 ms
	CheckTCPTimeoutWarn, CheckTCPTimeoutCrit   float64 // 连接超时 ms
	CheckHTTPRespWarn, CheckHTTPRespCrit       float64 // 响应时间 ms
	CheckHTTPStatusWarn, CheckHTTPStatusCrit   int     // 非 2xx 次数
	CheckProcFailWarn, CheckProcFailCrit       int     // 进程存活失败次数
	CheckUDPTimeoutWarn, CheckUDPTimeoutCrit   float64 // UDP 探测超时 ms
	CheckDNSTimeoutWarn, CheckDNSTimeoutCrit   float64 // DNS 解析超时 ms
	// ---- API 业务监控阈值 ----
	APIAvailWarn, APIAvailCrit           float64 // 可用率 %（低于阈值告警）
	APIAvgRespWarn, APIAvgRespCrit       float64 // 平均响应 ms
	APIP95RespWarn, APIP95RespCrit       float64 // P95 响应 ms
	APIThroughputWarn, APIThroughputCrit float64 // 吞吐量 req/s（低于阈值告警）
	// ---- 编排定时任务阈值 ----
	TaskFailWarn, TaskFailCrit       int     // 失败次数
	TaskTimeoutWarn, TaskTimeoutCrit float64 // 超时时长 s
	// ---- 端口转发监控阈值 ----
	ForwardConnWarn, ForwardConnCrit int     // 活跃连接数
	ForwardBwWarn, ForwardBwCrit     float64 // 带宽使用率 %
	ForwardErrWarn, ForwardErrCrit   float64 // 错误率 %
	ForwardLatWarn, ForwardLatCrit   float64 // 平均延迟 ms
	// ---- SNMP 网络设备阈值 ----
	SNMPIfUtilWarn, SNMPIfUtilCrit float64 // 接口带宽利用率 %
	SNMPIfErrWarn, SNMPIfErrCrit   float64 // 接口错误+丢包率 pps
	// ---- NetFlow 流量异常阈值 ----
	NetFlowSurgeRatio   float64 // 突增判定倍数（当前 bps 超基线的倍数），默认 3.0
	NetFlowSurgeMinMbps float64 // 突增最小流量地板（Mbps），低于此值不告警（避免小流量噪声），默认 1
	NetFlowDropWarn     float64 // 采集器单窗口丢包告警阈值（包），默认 100
}

// DefaultThresholds returns the Standard profile (recommended defaults).
// This is the alias used by the config store for new installations.
func DefaultThresholds() Thresholds {
	return StandardThresholds()
}

// ConservativeThresholds returns sensitive thresholds for production-critical
// systems where early warning is preferred over reducing noise.
func ConservativeThresholds() Thresholds {
	return Thresholds{
		CPUWarn: 70, CPUCrit: 85,
		MemWarn: 75, MemCrit: 90,
		DiskWarn: 75, DiskCrit: 85,
		DiskIOWarn: 70, DiskIOCrit: 85,
		IOPSWarn: 20000, IOPSCrit: 50000,
		GPUWarn: 70, GPUCrit: 85,
		GPUTempWarn: 80, GPUTempCrit: 90,
		GPUMemWarn: 85, GPUMemCrit: 95,
		LoadWarn: 2.0, LoadCrit: 4.0,
		ProcWarn: 0.3,
		ConnWarn: 2000, ConnCrit: 5000,
		OfflineAfter:      30 * time.Second,
		CheckPingLossWarn: 5, CheckPingLossCrit: 15,
		CheckPingLatencyWarn: 50, CheckPingLatencyCrit: 200,
		CheckTCPTimeoutWarn: 500, CheckTCPTimeoutCrit: 2000,
		CheckHTTPRespWarn: 500, CheckHTTPRespCrit: 2000,
		CheckHTTPStatusWarn: 1, CheckHTTPStatusCrit: 3,
		CheckProcFailWarn: 1, CheckProcFailCrit: 2,
		CheckUDPTimeoutWarn: 500, CheckUDPTimeoutCrit: 2000,
		CheckDNSTimeoutWarn: 200, CheckDNSTimeoutCrit: 1000,
		APIAvailWarn: 99.5, APIAvailCrit: 98.0,
		APIAvgRespWarn: 300, APIAvgRespCrit: 1000,
		APIP95RespWarn: 500, APIP95RespCrit: 2000,
		APIThroughputWarn: 200, APIThroughputCrit: 50,
		TaskFailWarn: 1, TaskFailCrit: 3,
		TaskTimeoutWarn: 30, TaskTimeoutCrit: 120,
		ForwardConnWarn: 150, ForwardConnCrit: 230,
		ForwardBwWarn: 70, ForwardBwCrit: 90,
		ForwardErrWarn: 3, ForwardErrCrit: 10,
		ForwardLatWarn: 500, ForwardLatCrit: 3000,
		SNMPIfUtilWarn: 70, SNMPIfUtilCrit: 85,
		SNMPIfErrWarn: 0.5, SNMPIfErrCrit: 5,
		NetFlowSurgeRatio: 2.0, NetFlowSurgeMinMbps: 0.5, NetFlowDropWarn: 50,
	}
}

// StandardThresholds returns the recommended balanced thresholds for most
// deployments. This is the new default since v5.4.1.
func StandardThresholds() Thresholds {
	return Thresholds{
		CPUWarn: 80, CPUCrit: 95,
		MemWarn: 85, MemCrit: 95,
		DiskWarn: 80, DiskCrit: 90,
		DiskIOWarn: 80, DiskIOCrit: 95,
		IOPSWarn: 50000, IOPSCrit: 100000,
		GPUWarn: 80, GPUCrit: 95,
		GPUTempWarn: 85, GPUTempCrit: 95,
		GPUMemWarn: 90, GPUMemCrit: 97,
		LoadWarn: 4.0, LoadCrit: 8.0,
		ProcWarn: 0.5,
		ConnWarn: 5000, ConnCrit: 10000,
		OfflineAfter:      60 * time.Second,
		CheckPingLossWarn: 10, CheckPingLossCrit: 30,
		CheckPingLatencyWarn: 100, CheckPingLatencyCrit: 500,
		CheckTCPTimeoutWarn: 1000, CheckTCPTimeoutCrit: 5000,
		CheckHTTPRespWarn: 1000, CheckHTTPRespCrit: 5000,
		CheckHTTPStatusWarn: 1, CheckHTTPStatusCrit: 5,
		CheckProcFailWarn: 1, CheckProcFailCrit: 3,
		CheckUDPTimeoutWarn: 1000, CheckUDPTimeoutCrit: 5000,
		CheckDNSTimeoutWarn: 500, CheckDNSTimeoutCrit: 2000,
		APIAvailWarn: 99.0, APIAvailCrit: 95.0,
		APIAvgRespWarn: 500, APIAvgRespCrit: 2000,
		APIP95RespWarn: 1000, APIP95RespCrit: 5000,
		APIThroughputWarn: 100, APIThroughputCrit: 10,
		TaskFailWarn: 1, TaskFailCrit: 5,
		TaskTimeoutWarn: 60, TaskTimeoutCrit: 300,
		ForwardConnWarn: 200, ForwardConnCrit: 280,
		ForwardBwWarn: 80, ForwardBwCrit: 95,
		ForwardErrWarn: 5, ForwardErrCrit: 15,
		ForwardLatWarn: 1000, ForwardLatCrit: 5000,
		SNMPIfUtilWarn: 80, SNMPIfUtilCrit: 95,
		SNMPIfErrWarn: 1, SNMPIfErrCrit: 10,
		NetFlowSurgeRatio: 3.0, NetFlowSurgeMinMbps: 1.0, NetFlowDropWarn: 100,
	}
}

// RelaxedThresholds returns low-noise thresholds suitable for dev/staging
// environments where alert fatigue should be minimized.
func RelaxedThresholds() Thresholds {
	return Thresholds{
		CPUWarn: 90, CPUCrit: 98,
		MemWarn: 90, MemCrit: 98,
		DiskWarn: 90, DiskCrit: 97,
		DiskIOWarn: 90, DiskIOCrit: 98,
		IOPSWarn: 100000, IOPSCrit: 200000,
		GPUWarn: 90, GPUCrit: 98,
		GPUTempWarn: 90, GPUTempCrit: 98,
		GPUMemWarn: 95, GPUMemCrit: 99,
		LoadWarn: 6.0, LoadCrit: 12.0,
		ProcWarn: 0.8,
		ConnWarn: 10000, ConnCrit: 20000,
		OfflineAfter:      120 * time.Second,
		CheckPingLossWarn: 20, CheckPingLossCrit: 50,
		CheckPingLatencyWarn: 300, CheckPingLatencyCrit: 1000,
		CheckTCPTimeoutWarn: 3000, CheckTCPTimeoutCrit: 10000,
		CheckHTTPRespWarn: 3000, CheckHTTPRespCrit: 10000,
		CheckHTTPStatusWarn: 3, CheckHTTPStatusCrit: 10,
		CheckProcFailWarn: 3, CheckProcFailCrit: 5,
		CheckUDPTimeoutWarn: 3000, CheckUDPTimeoutCrit: 10000,
		CheckDNSTimeoutWarn: 1000, CheckDNSTimeoutCrit: 5000,
		APIAvailWarn: 95.0, APIAvailCrit: 90.0,
		APIAvgRespWarn: 1000, APIAvgRespCrit: 5000,
		APIP95RespWarn: 2000, APIP95RespCrit: 10000,
		APIThroughputWarn: 50, APIThroughputCrit: 5,
		TaskFailWarn: 3, TaskFailCrit: 10,
		TaskTimeoutWarn: 120, TaskTimeoutCrit: 600,
		ForwardConnWarn: 260, ForwardConnCrit: 300,
		ForwardBwWarn: 90, ForwardBwCrit: 98,
		ForwardErrWarn: 10, ForwardErrCrit: 25,
		ForwardLatWarn: 3000, ForwardLatCrit: 10000,
		SNMPIfUtilWarn: 90, SNMPIfUtilCrit: 98,
		SNMPIfErrWarn: 5, SNMPIfErrCrit: 20,
		NetFlowSurgeRatio: 5.0, NetFlowSurgeMinMbps: 2.0, NetFlowDropWarn: 200,
	}
}

// Alert is a single fired threshold condition on base metrics.
type Alert struct {
	HostID    string  `json:"host_id"`
	Hostname  string  `json:"hostname"`
	IP        string  `json:"ip"`
	Level     string  `json:"level"`           // warning | critical
	Type      string  `json:"type"`            // cpu | memory | disk | diskio | iops | offline | check | load | gpu | proc | conn | hardware | hyperv | api | task | forward | snmp | trap | netflow
	Scope     string  `json:"scope,omitempty"` // sub-target (e.g. disk path) for per-item dedup
	Group     string  `json:"group,omitempty"` // 完整分组路径（"IDC机房 / 华东 / 数据库"），由 decorateAlertGroups 填充
	Since     int64   `json:"since,omitempty"` // unix time the condition first fired (for duration display)
	Message   string  `json:"message"`
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"`
	Status    string  `json:"status,omitempty"` // acknowledged | silenced | "" (active)
}

// memFreeAboveFloor 报告「使用率虽然过线，但剩余内存还在地板之上」——这种情况按配置
// 不算告警。floorGB<=0 表示没配这条附加条件，一律返回 false（等于不改变原行为）。
// total==0（采集还没上来）时同样返回 false：宁可按老规则报一次，也不要因为一次缺数据
// 把真实的内存告警吞掉。
func memFreeAboveFloor(total, used uint64, floorGB float64) bool {
	if floorGB <= 0 || total == 0 || used > total {
		return false
	}
	return float64(total-used) >= floorGB*1024*1024*1024
}

func classify(v, warn, crit float64) string {
	switch {
	case v >= crit:
		return "critical"
	case v >= warn:
		return "warning"
	default:
		return ""
	}
}

// classifyLow returns the alert level for "lower is worse" metrics (availability,
// throughput) where the value dropping below the threshold is the concern.
func classifyLow(v, warn, crit float64) string {
	switch {
	case v <= crit:
		return "critical"
	case v <= warn:
		return "warning"
	default:
		return ""
	}
}

// Evaluate computes the current threshold-alert set from host state.
func Evaluate(hosts []*Host, t Thresholds) []Alert {
	now := time.Now().Unix()
	offlineSec := int64(t.OfflineAfter.Seconds())
	var alerts []Alert

	for _, h := range hosts {
		if now-h.LastSeen > offlineSec {
			alerts = append(alerts, Alert{
				HostID:    h.ID,
				Hostname:  h.Hostname,
				IP:        h.IP,
				Level:     "critical",
				Type:      "offline",
				Message:   Tz("alert.offline", h.Hostname, h.IP, now-h.LastSeen),
				Value:     float64(now - h.LastSeen),
				Timestamp: now,
			})
			continue
		}
		if h.Latest == nil {
			continue
		}
		m := h.Latest
		if lv := classify(m.CPUPercent, t.CPUWarn, t.CPUCrit); lv != "" {
			alerts = append(alerts, Alert{
				HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "cpu",
				Message: Tz("alert.cpu_high", m.CPUPercent, 100-m.CPUPercent),
				Value:   m.CPUPercent, Timestamp: now,
			})
		}
		if lv := classify(m.MemPercent, t.MemWarn, t.MemCrit); lv != "" && !memFreeAboveFloor(m.MemTotal, m.MemUsed, t.MemFreeFloorGB) {
			alerts = append(alerts, Alert{
				HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "memory",
				Message: Tz("alert.mem_high", m.MemPercent, fmtBytes(m.MemTotal-m.MemUsed)),
				Value:   m.MemPercent, Timestamp: now,
			})
		}
		if len(m.Disks) > 0 {
			for _, d := range m.Disks {
				if lv := classify(d.Percent, t.DiskWarn, t.DiskCrit); lv != "" {
					alerts = append(alerts, Alert{
						HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "disk", Scope: d.Path,
						Message: Tz("alert.disk_path_high", d.Path, d.Percent, fmtBytes(d.Total-d.Used)),
						Value:   d.Percent, Timestamp: now,
					})
				}
			}
		} else if lv := classify(m.DiskPercent, t.DiskWarn, t.DiskCrit); lv != "" {
			alerts = append(alerts, Alert{
				HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "disk",
				Message: Tz("alert.disk_high", m.DiskPercent, fmtBytes(m.DiskTotal-m.DiskUsed)),
				Value:   m.DiskPercent, Timestamp: now,
			})
		}
		// System load alert (5-min load exceeding core count × threshold)
		if m.CPUCores > 0 {
			loadWarn := float64(m.CPUCores) * t.LoadWarn
			loadCrit := float64(m.CPUCores) * t.LoadCrit
			if m.Load5 >= loadWarn {
				lv := "warning"
				if m.Load5 >= loadCrit {
					lv = "critical"
				}
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "load",
					Message: Tz("alert.load_high", m.Load5, m.CPUCores, loadWarn),
					Value:   m.Load5, Timestamp: now,
				})
			}
		}
		// GPU alerts (configurable): 核心算力使用率 / 温度 / 显存占用率。三项用不同 Scope
		// （name、name/temp、name/mem），否则 alertKey=host/gpu/name 冲突会互相覆盖。
		for _, g := range m.GPUs {
			if lv := classify(g.UtilPercent, t.GPUWarn, t.GPUCrit); lv != "" {
				tempStr := ""
				if g.Temp > 0 {
					tempStr = Tz("alert.gpu_temp", int(g.Temp))
				}
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "gpu", Scope: g.Name,
					Message: Tz("alert.gpu_high", g.Name, g.UtilPercent, tempStr),
					Value:   g.UtilPercent, Timestamp: now,
				})
			}
			if g.Temp > 0 && t.GPUTempWarn > 0 {
				if lv := classify(g.Temp, t.GPUTempWarn, t.GPUTempCrit); lv != "" {
					alerts = append(alerts, Alert{
						HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "gpu", Scope: g.Name + "/temp",
						Message: Tz("alert.gpu_temp_high", g.Name, int(g.Temp)),
						Value:   g.Temp, Timestamp: now,
					})
				}
			}
			if g.MemPercent > 0 && t.GPUMemWarn > 0 {
				if lv := classify(g.MemPercent, t.GPUMemWarn, t.GPUMemCrit); lv != "" {
					alerts = append(alerts, Alert{
						HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "gpu", Scope: g.Name + "/mem",
						Message: Tz("alert.gpu_mem_high", g.Name, g.MemPercent, fmtBytes(g.MemUsed), fmtBytes(g.MemTotal)),
						Value:   g.MemPercent, Timestamp: now,
					})
				}
			}
		}
		// Disk IO alert (>80% warning, >90% critical)
		if m.DiskIOUtilPercent > 0 {
			if lv := classify(m.DiskIOUtilPercent, t.DiskIOWarn, t.DiskIOCrit); lv != "" {
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "diskio",
					Message: Tz("alert.diskio_high", m.DiskIOUtilPercent,
						fmtRateBytes(m.DiskReadRate), fmtRateBytes(m.DiskWriteRate)),
					Value: m.DiskIOUtilPercent, Timestamp: now,
				})
			}
		}
		// IOPS alert (>10000 warning, >20000 critical)
		totalIOPS := m.DiskReadIOPS + m.DiskWriteIOPS
		if totalIOPS > 0 {
			if lv := classify(totalIOPS, t.IOPSWarn, t.IOPSCrit); lv != "" {
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "iops",
					Message: Tz("alert.iops_high", totalIOPS, m.DiskReadIOPS, m.DiskWriteIOPS),
					Value:   totalIOPS, Timestamp: now,
				})
			}
		}
		// Process count anomaly: compare current proc count vs 1h baseline
		// 基线由 store 在 1m 层追加时预先算好（见 Host.procBase1h），这里不再遍历 hist1m。
		if m.ProcCount > 0 && t.ProcWarn > 0 && h.procBase1h > 0 {
			baseline := h.procBase1h
			{
				change := math.Abs(float64(m.ProcCount)-baseline) / baseline
				if change >= t.ProcWarn {
					dir := "increase"
					if float64(m.ProcCount) < baseline {
						dir = "decrease"
					}
					alerts = append(alerts, Alert{
						HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: "warning", Type: "proc",
						Message: Tz("alert.proc_anomaly", m.ProcCount, baseline, change*100, dir),
						Value:   change * 100, Timestamp: now,
					})
				}
			}
		}
		// Host connection count (TCP+UDP total): catches连接泄漏 / TIME_WAIT 风暴 / fd 耗尽。
		// 优先用新采集的分状态计数求和，回退到旧的 established 标量（兼容未升级 Agent）。
		if t.ConnWarn > 0 {
			totalConns := m.NetConns
			if len(m.Conns) > 0 {
				totalConns = 0
				for _, c := range m.Conns {
					totalConns += c.Count
				}
			}
			if totalConns > 0 {
				if lv := classify(float64(totalConns), t.ConnWarn, t.ConnCrit); lv != "" {
					alerts = append(alerts, Alert{
						HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "conn",
						Message: Tz("alert.conn_high", totalConns),
						Value:   float64(totalConns), Timestamp: now,
					})
				}
			}
		}
	}

	// ---- API 业务监控告警（每个主机可独立上报 API 指标）----
	for _, h := range hosts {
		if h.Latest == nil {
			continue
		}
		m := h.Latest
		// 接口可用率（低于阈值告警）
		if m.APIAvailPercent > 0 {
			if lv := classifyLow(m.APIAvailPercent, t.APIAvailWarn, t.APIAvailCrit); lv != "" {
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "api",
					Scope:   "availability",
					Message: Tz("alert.api_avail_low", m.APIAvailPercent),
					Value:   m.APIAvailPercent, Timestamp: now,
				})
			}
		}
		// 平均响应时间
		if m.APIAvgRespMs > 0 {
			if lv := classify(m.APIAvgRespMs, t.APIAvgRespWarn, t.APIAvgRespCrit); lv != "" {
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "api",
					Scope:   "avg_resp",
					Message: Tz("alert.api_avg_resp_high", m.APIAvgRespMs),
					Value:   m.APIAvgRespMs, Timestamp: now,
				})
			}
		}
		// P95 响应时间
		if m.APIP95RespMs > 0 {
			if lv := classify(m.APIP95RespMs, t.APIP95RespWarn, t.APIP95RespCrit); lv != "" {
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "api",
					Scope:   "p95_resp",
					Message: Tz("alert.api_p95_resp_high", m.APIP95RespMs),
					Value:   m.APIP95RespMs, Timestamp: now,
				})
			}
		}
		// 吞吐量（低于阈值告警）
		if m.APIThroughputRPS > 0 {
			if lv := classifyLow(m.APIThroughputRPS, t.APIThroughputWarn, t.APIThroughputCrit); lv != "" {
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "api",
					Scope:   "throughput",
					Message: Tz("alert.api_throughput_low", m.APIThroughputRPS),
					Value:   m.APIThroughputRPS, Timestamp: now,
				})
			}
		}
	}

	// ---- 编排定时任务告警 ----
	for _, h := range hosts {
		if h.Latest == nil {
			continue
		}
		m := h.Latest
		// 执行失败次数
		if m.TaskFailCount > 0 && t.TaskFailWarn > 0 {
			fc := float64(m.TaskFailCount)
			if lv := classify(fc, float64(t.TaskFailWarn), float64(t.TaskFailCrit)); lv != "" {
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "task",
					Scope:   "fail_count",
					Message: Tz("alert.task_fail", m.TaskFailCount),
					Value:   fc, Timestamp: now,
				})
			}
		}
		// 超时时长
		if m.TaskTimeoutSec > 0 {
			if lv := classify(m.TaskTimeoutSec, t.TaskTimeoutWarn, t.TaskTimeoutCrit); lv != "" {
				alerts = append(alerts, Alert{
					HostID: h.ID, Hostname: h.Hostname, IP: h.IP, Level: lv, Type: "task",
					Scope:   "timeout",
					Message: Tz("alert.task_timeout", m.TaskTimeoutSec),
					Value:   m.TaskTimeoutSec, Timestamp: now,
				})
			}
		}
	}

	sort.SliceStable(alerts, func(i, j int) bool {
		if alerts[i].Level != alerts[j].Level {
			return alerts[i].Level == "critical"
		}
		return alerts[i].Hostname < alerts[j].Hostname
	})
	return alerts
}

// EvaluateForward generates threshold-based alerts from forwarding metrics.
func EvaluateForward(snap ForwardSnapshot, t Thresholds) []Alert {
	now := time.Now().Unix()
	var alerts []Alert

	// 活跃连接数
	if t.ForwardConnWarn > 0 {
		fc := float64(snap.ActiveSessions)
		if lv := classify(fc, float64(t.ForwardConnWarn), float64(t.ForwardConnCrit)); lv != "" {
			alerts = append(alerts, Alert{
				Hostname: "Forward", Level: lv, Type: "forward", Scope: "connections",
				Message: Tz("alert.forward_conn", snap.ActiveSessions, snap.MaxSessions),
				Value:   fc, Timestamp: now,
			})
		}
	}
	// 带宽使用率（相对于最大带宽 100MB/s = 100e6 B/s）
	const maxBwBps = 100e6
	if snap.BandwidthBps > 0 && t.ForwardBwWarn > 0 {
		bwPct := snap.BandwidthBps / maxBwBps * 100
		if lv := classify(bwPct, t.ForwardBwWarn, t.ForwardBwCrit); lv != "" {
			alerts = append(alerts, Alert{
				Hostname: "Forward", Level: lv, Type: "forward", Scope: "bandwidth",
				Message: Tz("alert.forward_bw", bwPct, snap.BandwidthBps/1e6),
				Value:   bwPct, Timestamp: now,
			})
		}
	}
	// 错误率
	if snap.TotalSessions > 0 && t.ForwardErrWarn > 0 {
		errPct := float64(snap.Errors) / float64(snap.TotalSessions) * 100
		if lv := classify(errPct, t.ForwardErrWarn, t.ForwardErrCrit); lv != "" {
			alerts = append(alerts, Alert{
				Hostname: "Forward", Level: lv, Type: "forward", Scope: "errors",
				Message: Tz("alert.forward_err", errPct, snap.Errors),
				Value:   errPct, Timestamp: now,
			})
		}
	}
	// 平均延迟
	if snap.AvgLatencyMs > 0 && t.ForwardLatWarn > 0 {
		if lv := classify(snap.AvgLatencyMs, t.ForwardLatWarn, t.ForwardLatCrit); lv != "" {
			alerts = append(alerts, Alert{
				Hostname: "Forward", Level: lv, Type: "forward", Scope: "latency",
				Message: Tz("alert.forward_lat", snap.AvgLatencyMs),
				Value:   snap.AvgLatencyMs, Timestamp: now,
			})
		}
	}
	return alerts
}

// fmtBytes renders a byte count as a human-readable amount for alert messages.
func fmtBytes(b uint64) string {
	const gb, mb = 1 << 30, 1 << 20
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1fG", float64(b)/gb)
	case b >= mb:
		return fmt.Sprintf("%.0fM", float64(b)/mb)
	default:
		return fmt.Sprintf("%dK", b/1024)
	}
}

// fmtRateBytes renders a bytes/sec rate as human-readable (e.g. "12.3 MB/s").
func fmtRateBytes(bps float64) string {
	switch {
	case bps >= 1e9:
		return fmt.Sprintf("%.1f GB/s", bps/1e9)
	case bps >= 1e6:
		return fmt.Sprintf("%.1f MB/s", bps/1e6)
	case bps >= 1e3:
		return fmt.Sprintf("%.0f KB/s", bps/1e3)
	default:
		return fmt.Sprintf("%.0f B/s", bps)
	}
}
