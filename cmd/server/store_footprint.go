package main

import (
	"log/slog"
	"unsafe"

	"aiops-monitor/shared"
)

// historyRingBytesPerHost 返回一台主机的三层历史环在内存里占多少字节。
//
// 只算 shared.Sample 本体（344 B/点）。raw 层每个点还挂着 Disks/Conns/GPUs 三个切片，
// 实际占用会更高——所以这个数是**下界**，报给运维时要说清楚。
func historyRingBytesPerHost() int {
	return int(unsafe.Sizeof(shared.Sample{})) * (histRawMax + hist1mMax + hist5mMax)
}

// logHistoryRingBudget 启动时把内存态历史环的容量账打出来。
//
// 为什么值得单独一行日志：500 台机群下这一项就要 2 GB 以上，而它超标的表现是 OOM 或
// 长时间 GC 停顿——现场只会看到"平台卡"，没有任何线索指向历史环。把每台主机的成本、
// 当前机群的总量、以及可下调的环境变量一次说清，运维才有得选。
func (s *Store) hostCount() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.hosts)
}

func logHistoryRingBudget(hosts int) {
	per := historyRingBytesPerHost()
	slog.Info("内存态历史环容量预算",
		"每台主机", humanBytes(int64(per)),
		"当前主机数", hosts,
		"合计下界", humanBytes(int64(per)*int64(max(hosts, 1))),
		"层深", []int{histRawMax, hist1mMax, hist5mMax},
		"可调", "AIOPS_HIST_RAW_MAX / AIOPS_HIST_1M_MAX / AIOPS_HIST_5M_MAX",
		"note", "持久历史在 VictoriaMetrics；下调只影响 VM 不可用时的回看深度与图表右端实时叠加窗口",
	)
}
