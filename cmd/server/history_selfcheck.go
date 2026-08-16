package main

import (
	"log/slog"
	"time"
)

// ============================================================================
// 持久化历史自检 —— 让「重启后曲线只剩重启之后」这件事在发生的那一刻就被看见。
//
// 为什么需要它：主机曲线的持久层是 VictoriaMetrics，内存环只是活进程里的一层
// 缓存。但那层缓存并不小 —— hist5m 是 8640 个 5 分钟点，**整整 30 天**。于是只要
// 进程不重启，即便 VM 里一个点都没有，页面上的曲线看起来依然是完整的；直到某次
// 发版重启，内存清空，用户才第一次看到「历史没了」。
//
// 这就是这个现象反复出现、又反复"修不好"的结构原因：
//   * 症状出现在重启后，所以每次都被当成「重启丢数据」去查写入路径；
//   * 而真正的丢失可能在更早、且在 Go 代码之外 —— VM 的数据目录在发版时被清掉
//     （compose 默认是 ./vm-data 这个**部署目录内的绑定挂载**，一次 git clean -xfd、
//     一次目录重建、一次 docker compose down -v 就没了）、或者写入长期失败；
//   * 内存环那 30 天的假象把两者都掩盖了，等到症状暴露时，现场已经过去很久。
//
// 自检把这层假象戳破：启动后等一小会儿，拿一台**在本进程启动之前就存在**的主机，
// 走**生产同一条读路径**去 VM 取一段重启前的历史。取不到就说明持久层是空的，
// 立刻按错误级别落日志 + 推送到通知中心。它不修数据，但它让人在丢失当天就知道，
// 而不是在下一次发版之后靠猜。
// ============================================================================

const (
	// 等 VM 起来、Agent 完成首轮上报再查；compose 里 server 只 depends_on
	// service_started，VM 在数据量大时要先合并分片才对外服务。
	durableHistorySelfCheckDelay = 3 * time.Minute
	// 探针主机至少要在启动前 2 小时就存在，这样「查不到」才排除得掉「本来就没跑够」。
	durableHistoryProbeAge = 2 * time.Hour
	// 探测窗口的右端离启动时刻留 30 分钟，避开重启前后那段本来就可能缺口的时间。
	durableHistoryProbeGap = 30 * time.Minute
)

// verifyDurableHistoryAfterStart runs once per process; see the file comment.
func (s *Server) verifyDurableHistoryAfterStart(bootAt int64) {
	if s == nil || s.store == nil || s.vm == nil || !s.vm.enabled() {
		return
	}
	time.Sleep(durableHistorySelfCheckDelay)

	probe := s.durableHistoryProbeHost(bootAt)
	if probe == nil {
		// 全新部署或全是新纳管主机：没有「重启前的历史」可言，不该报警。
		return
	}
	from := bootAt - int64(durableHistoryProbeAge/time.Second)
	to := bootAt - int64(durableHistoryProbeGap/time.Second)
	samples, ok := s.vm.queryHistoryFilter(probe.ID, from, to, []string{"aiops_cpu_percent"})
	if ok && len(samples) > 0 {
		slog.Info("持久化历史自检通过：VictoriaMetrics 保留了本次启动之前的样本",
			"host", shortID(probe.ID), "samples", len(samples), "window_hours", (to-from)/3600)
		return
	}

	title := "时序历史缺失：VictoriaMetrics 里没有本次重启之前的数据"
	body := "主机 " + probe.Hostname + " 在本次启动前已纳管超过 " +
		durableHistoryProbeAge.String() + "，但 VictoriaMetrics 在启动前的窗口里返回 0 个样本。" +
		"主机曲线因此只会显示重启之后的数据。\n" +
		"内存里的 5 分钟历史有 30 天，进程不重启时会把这个问题完全掩盖，所以请在今天排查：\n" +
		"1) VM 数据目录是否在发版时被清掉 —— compose 默认挂的是部署目录内的 ./vm-data，" +
		"git clean -xfd、重建目录、docker compose down -v 都会连它一起删；\n" +
		"2) VM 写入是否长期失败 —— 查服务端日志里 VictoriaMetrics 相关的告警；\n" +
		"3) 主机曲线接口的 X-AIOps-History-Source 响应头是否为 ram-fallback。"
	slog.Error(title, "host", shortID(probe.ID), "hostname", probe.Hostname,
		"probe_from", from, "probe_to", to, "vm_query_ok", ok, "samples", len(samples))
	if s.messages != nil {
		s.messages.push("system", "critical", title, body, "hosts", probe.ID)
	}
	if s.store != nil {
		s.store.AddLog(LogEntry{
			Kind: KindSystem, Level: "error", Actor: "history-selfcheck",
			Host: probe.Hostname, Message: title,
		})
	}
}

// durableHistoryProbeHost picks the host most likely to prove the point: the one
// managed longest before this process started.
func (s *Server) durableHistoryProbeHost(bootAt int64) *Host {
	var best *Host
	for _, h := range s.store.ListHosts() {
		if h == nil || h.FirstSeen <= 0 {
			continue
		}
		if bootAt-h.FirstSeen < int64(durableHistoryProbeAge/time.Second) {
			continue
		}
		if best == nil || h.FirstSeen < best.FirstSeen {
			best = h
		}
	}
	return best
}
