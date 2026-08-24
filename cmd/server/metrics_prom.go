package main

// 平台自身的 Prometheus 指标出口。
//
// 一个监控产品说自己不可观测，销售现场很难解释——客户的 Prometheus 接不进来，
// 我们自己的 SRE 也监控不了这套控制面。这里暴露的是**运维接得住的那几个量**：
// 主机在线率、告警数、VictoriaMetrics 熔断与丢样、PG 刷写延迟与连接池、
// 授权余量、进程自身（goroutine/内存/运行时长）。
//
// 鉴权：配置了 AIOPS_METRICS_TOKEN 就按 Bearer/?token= 校验（Prometheus 拿不到
// 会话 Cookie）；没配置则退回会话鉴权，绝不匿名放行——这些数字里有主机规模、
// 告警面和授权信息，属于客户资产。

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

var (
	metricProcessStart = time.Now()

	// pgFlush 的观测量（包级原子，不往 Server 结构体上加字段）。
	pgFlushCount    atomic.Uint64
	pgFlushHeavyCnt atomic.Uint64
	pgFlushLastMs   atomic.Int64
	pgFlushMaxMs    atomic.Int64
	pgFlushLastTS   atomic.Int64
)

// observePGFlush 由 pgFlush 在每轮刷写结束时调用。刷写延迟是「PG 撑不撑得住」
// 最早的信号：它一旦从几十毫秒涨到几秒，接下来就是内存里堆积和退出时丢数据。
func observePGFlush(start time.Time, heavy bool) {
	ms := time.Since(start).Milliseconds()
	pgFlushCount.Add(1)
	if heavy {
		pgFlushHeavyCnt.Add(1)
	}
	pgFlushLastMs.Store(ms)
	pgFlushLastTS.Store(time.Now().Unix())
	for {
		old := pgFlushMaxMs.Load()
		if ms <= old || pgFlushMaxMs.CompareAndSwap(old, ms) {
			return
		}
	}
}

func metricsTokenOK(r *http.Request) bool {
	want := strings.TrimSpace(os.Getenv("AIOPS_METRICS_TOKEN"))
	if want == "" {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if got == "" {
		got = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func breakerStateCode(s string) int {
	switch s {
	case "open":
		return 2
	case "half-open", "half_open":
		return 1
	default:
		return 0
	}
}

func licenseStateCode(s string) int {
	switch s {
	case "active":
		return 0
	case "over_quota":
		return 1
	case "grace":
		return 2
	case "expired":
		return 3
	case "invalid":
		return 4
	default: // unlicensed
		return 5
	}
}

// handleMetrics GET /metrics —— Prometheus 文本格式。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !metricsTokenOK(r) {
		// 没配令牌就退回会话鉴权（浏览器里点开也能看）。
		if s.auth == nil || s.auth.userForRequest(r) == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="aiops-metrics"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "metrics requires a session or AIOPS_METRICS_TOKEN bearer token",
			})
			return
		}
	}

	var b strings.Builder
	g := func(name, help string, val float64, labels ...string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		writeMetricLine(&b, name, val, labels...)
	}
	c := func(name, help string, val float64, labels ...string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
		writeMetricLine(&b, name, val, labels...)
	}

	g("aiops_build_info", "Build info; value is always 1", 1, "version", appVersion)
	g("aiops_process_uptime_seconds", "Seconds since this server process started", time.Since(metricProcessStart).Seconds())
	g("aiops_goroutines", "Number of goroutines", float64(runtime.NumGoroutine()))
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	g("aiops_memory_alloc_bytes", "Heap bytes currently allocated", float64(ms.Alloc))
	g("aiops_memory_sys_bytes", "Bytes obtained from the OS", float64(ms.Sys))

	// 主机与在线率
	hosts := s.store.ListHosts()
	th := s.cfg.Thresholds()
	offlineAfter := int64(th.OfflineAfter.Seconds())
	now := time.Now().Unix()
	online := 0
	for _, h := range hosts {
		if now-h.LastSeen <= offlineAfter {
			online++
		}
	}
	g("aiops_hosts_total", "Registered hosts", float64(len(hosts)))
	g("aiops_hosts_online", "Hosts seen within the offline threshold", float64(online))
	ratio := 1.0
	if len(hosts) > 0 {
		ratio = float64(online) / float64(len(hosts))
	}
	g("aiops_agent_online_ratio", "Online host ratio (0..1)", ratio)

	// 告警面（内存态评估，与控制台概览同源）
	alerts := append(append(Evaluate(hosts, th), s.checks.DownAlerts()...), EvaluateForward(s.forward.Snapshot(), th)...)
	crit, warn := 0, 0
	for _, a := range alerts {
		if a.Level == "critical" {
			crit++
		} else {
			warn++
		}
	}
	fmt.Fprintf(&b, "# HELP aiops_alerts_active Active alerts by level\n# TYPE aiops_alerts_active gauge\n")
	writeMetricLine(&b, "aiops_alerts_active", float64(crit), "level", "critical")
	writeMetricLine(&b, "aiops_alerts_active", float64(warn), "level", "warning")

	// VictoriaMetrics：熔断状态 + 丢样 + 三条入队通道的水位
	if s.vm != nil {
		fmt.Fprintf(&b, "# HELP aiops_vm_breaker_state VictoriaMetrics circuit breaker (0=closed 1=half-open 2=open)\n# TYPE aiops_vm_breaker_state gauge\n")
		writeMetricLine(&b, "aiops_vm_breaker_state", float64(breakerStateCode(s.vm.writeBreaker().state())), "breaker", "write")
		writeMetricLine(&b, "aiops_vm_breaker_state", float64(breakerStateCode(s.vm.queryBreaker().state())), "breaker", "read")
		c("aiops_vm_dropped_samples_total", "Samples dropped because the ingest queue was full", float64(s.vm.dropped.Load()))
		fmt.Fprintf(&b, "# HELP aiops_vm_queue_depth Pending samples per ingest queue\n# TYPE aiops_vm_queue_depth gauge\n")
		writeMetricLine(&b, "aiops_vm_queue_depth", float64(len(s.vm.ch)), "queue", "host")
		writeMetricLine(&b, "aiops_vm_queue_depth", float64(len(s.vm.checkCh)), "queue", "check")
		writeMetricLine(&b, "aiops_vm_queue_depth", float64(len(s.vm.apiCh)), "queue", "api")
		writeMetricLine(&b, "aiops_vm_queue_depth", float64(len(s.vm.rawCh)), "queue", "raw")
		fmt.Fprintf(&b, "# HELP aiops_vm_queue_capacity Capacity of each ingest queue\n# TYPE aiops_vm_queue_capacity gauge\n")
		writeMetricLine(&b, "aiops_vm_queue_capacity", float64(cap(s.vm.ch)), "queue", "host")
		writeMetricLine(&b, "aiops_vm_queue_capacity", float64(cap(s.vm.checkCh)), "queue", "check")
		writeMetricLine(&b, "aiops_vm_queue_capacity", float64(cap(s.vm.apiCh)), "queue", "api")
		writeMetricLine(&b, "aiops_vm_queue_capacity", float64(cap(s.vm.rawCh)), "queue", "raw")
	}

	// PG：刷写延迟 + 连接池
	c("aiops_pg_flush_total", "pgFlush rounds since start", float64(pgFlushCount.Load()))
	c("aiops_pg_flush_heavy_total", "Heavy pgFlush rounds since start", float64(pgFlushHeavyCnt.Load()))
	g("aiops_pg_flush_duration_seconds", "Duration of the last pgFlush", float64(pgFlushLastMs.Load())/1000)
	g("aiops_pg_flush_duration_max_seconds", "Slowest pgFlush since start", float64(pgFlushMaxMs.Load())/1000)
	g("aiops_pg_flush_timestamp_seconds", "Unix time of the last pgFlush", float64(pgFlushLastTS.Load()))
	if s.pg != nil && s.pg.db != nil {
		st := s.pg.db.Stats()
		g("aiops_pg_connections_open", "Open PostgreSQL connections", float64(st.OpenConnections))
		g("aiops_pg_connections_in_use", "In-use PostgreSQL connections", float64(st.InUse))
		g("aiops_pg_connections_idle", "Idle PostgreSQL connections", float64(st.Idle))
		c("aiops_pg_wait_total", "Times a query waited for a free connection", float64(st.WaitCount))
	}

	// 授权余量：签发方与客户的 SRE 都需要在到期前看见它
	lic := s.licenseStatus()
	g("aiops_license_state", "License state (0=active 1=over_quota 2=grace 3=expired 4=invalid 5=unlicensed)",
		float64(licenseStateCode(lic.State)), "state", lic.State)
	g("aiops_license_days_left", "Days until license expiry (negative = expired)", float64(lic.DaysLeft))
	g("aiops_license_hosts_used", "Hosts counted against the license", float64(lic.UsedHosts))
	g("aiops_license_hosts_max", "Licensed host limit (0 = unlimited)", float64(lic.MaxHosts))
	if lic.ReadOnly {
		g("aiops_license_read_only", "1 when writes are degraded to read-only", 1)
	} else {
		g("aiops_license_read_only", "1 when writes are degraded to read-only", 0)
	}

	// 事件面（未关闭的 SRE 事件）
	openIncidents := 0
	for _, inc := range s.incidents.Export() {
		if inc.Status != "resolved" && inc.Status != "closed" {
			openIncidents++
		}
	}
	g("aiops_incidents_open", "Open SRE incidents", float64(openIncidents))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

// writeMetricLine 输出一行样本；标签值按 Prometheus 文本格式转义，
// 版本号里带引号或反斜杠时不至于把整份输出弄成非法格式。
func writeMetricLine(b *strings.Builder, name string, val float64, labels ...string) {
	b.WriteString(name)
	if len(labels) >= 2 {
		b.WriteString("{")
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(labels[i])
			b.WriteString(`="`)
			b.WriteString(escapeMetricLabel(labels[i+1]))
			b.WriteString(`"`)
		}
		b.WriteString("}")
	}
	fmt.Fprintf(b, " %g\n", val)
}

func escapeMetricLabel(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}
