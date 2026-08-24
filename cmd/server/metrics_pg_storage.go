package main

// PostgreSQL 存储膨胀的指标出口。
//
// 膨胀这件事此前**只有 CLI 看得见**（`aiops-server -pg-report`）。也就是说：一套
// 交付出去的平台，pg-data 从几百 MB 涨到 9 GB 的整个过程，客户的 Prometheus 里
// 一个信号也没有，运维第一次知道是磁盘写满、PG 拒绝写入的时候——而这时候要做的
// `-pg-reclaim` 恰恰需要磁盘上有空间来重写表。信号必须比故障早。
//
// 于是把 -pg-report 已经算好的那几个量搬到 /metrics 上，运维可以直接配一条
// `aiops_pg_reclaimable_bytes > 2e9` 的告警，在维护窗口里安排一次回收。
//
// 代价控制：那条统计查询要扫 pg_class/pg_stats，不能跟着 15 秒一次的抓取跑，
// 所以按 10 分钟缓存——膨胀是以天计的量，10 分钟的滞后没有意义上的损失。

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	pgStorageMetricsTTL = 10 * time.Minute
	// 每张表两条 series，取体积最大的 N 张——剩下的都是几百 KB 的小表，
	// 全量导出只会把客户的 Prometheus 撑成高基数。
	pgStorageMetricsTopN = 15
)

type pgStorageSnapshot struct {
	dbBytes          int64
	tables           []pgTableStat
	reclaimableBytes int64
	candidates       int
	takenAt          time.Time
	err              error
}

var (
	pgStorageMu         sync.Mutex
	pgStorageSnap       pgStorageSnapshot
	pgStorageRefreshing bool
)

// pgStorageProbeTimeout 是那条统计查询的硬超时。没有它，PG 一卡住，
// 抓取协程就永久挂在这里——而 /metrics 恰恰是运维用来判断"平台还活着吗"的入口。
const pgStorageProbeTimeout = 8 * time.Second

// pgStorageMetrics 返回缓存的存储快照；过期时**在后台**刷新，绝不阻塞本次抓取。
//
// 刻意不让抓取等查询：Prometheus 的抓取超时通常只有 10 秒，而这条查询在一个正在
// 挣扎的 PG 上可以慢得多。宁可这一轮少几条 series（下一轮就有了），也不能让
// "PG 慢"变成"整个 /metrics 抓不到"——那会把平台自身的所有指标一起弄丢，
// 正好在最需要它们的时候。
func (s *Server) pgStorageMetrics(now time.Time) pgStorageSnapshot {
	if s == nil || s.pg == nil || s.pg.db == nil {
		return pgStorageSnapshot{}
	}
	pgStorageMu.Lock()
	snap := pgStorageSnap
	stale := snap.takenAt.IsZero() || now.Sub(snap.takenAt) >= pgStorageMetricsTTL
	if stale && !pgStorageRefreshing {
		pgStorageRefreshing = true
		go s.refreshPGStorageSnapshot()
	}
	pgStorageMu.Unlock()
	return snap
}

// refreshPGStorageSnapshot 在后台跑一次统计查询并替换快照。
func (s *Server) refreshPGStorageSnapshot() {
	defer func() {
		pgStorageMu.Lock()
		pgStorageRefreshing = false
		pgStorageMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), pgStorageProbeTimeout)
	defer cancel()

	snap := pgStorageSnapshot{takenAt: time.Now()}
	stats, err := s.pg.tableStatsContext(ctx)
	if err != nil {
		snap.err = err
		slog.Warn("采集 PostgreSQL 存储指标失败", "err", err)
	} else {
		snap.tables = stats
		if n, err := s.pg.databaseSizeContext(ctx); err == nil {
			snap.dbBytes = n
		}
		for _, c := range reclaimCandidates(stats) {
			snap.candidates++
			snap.reclaimableBytes += c.estReclaimBytes()
		}
	}
	pgStorageMu.Lock()
	pgStorageSnap = snap
	pgStorageMu.Unlock()
}

// writePGStorageMetrics 把快照渲染成 Prometheus 文本。
func writePGStorageMetrics(b *strings.Builder, snap pgStorageSnapshot) {
	if snap.takenAt.IsZero() {
		return
	}
	fmt.Fprintf(b, "# HELP aiops_pg_metrics_error 1 when the last PostgreSQL storage probe failed\n# TYPE aiops_pg_metrics_error gauge\n")
	errVal := 0.0
	if snap.err != nil {
		errVal = 1
	}
	writeMetricLine(b, "aiops_pg_metrics_error", errVal)
	if snap.err != nil {
		return
	}

	fmt.Fprintf(b, "# HELP aiops_pg_database_bytes Total size of the AIOps PostgreSQL database\n# TYPE aiops_pg_database_bytes gauge\n")
	writeMetricLine(b, "aiops_pg_database_bytes", float64(snap.dbBytes))

	fmt.Fprintf(b, "# HELP aiops_pg_reclaimable_bytes Bytes a one-time `aiops-server -pg-reclaim` is expected to return to the filesystem\n# TYPE aiops_pg_reclaimable_bytes gauge\n")
	writeMetricLine(b, "aiops_pg_reclaimable_bytes", float64(snap.reclaimableBytes))

	fmt.Fprintf(b, "# HELP aiops_pg_reclaim_candidate_tables Tables bloated enough to be worth a VACUUM FULL\n# TYPE aiops_pg_reclaim_candidate_tables gauge\n")
	writeMetricLine(b, "aiops_pg_reclaim_candidate_tables", float64(snap.candidates))

	top := append([]pgTableStat(nil), snap.tables...)
	sort.Slice(top, func(i, j int) bool { return top[i].TotalBytes > top[j].TotalBytes })
	if len(top) > pgStorageMetricsTopN {
		top = top[:pgStorageMetricsTopN]
	}
	if len(top) == 0 {
		return
	}
	fmt.Fprintf(b, "# HELP aiops_pg_table_bytes Total size (heap+indexes+TOAST) of the largest tables\n# TYPE aiops_pg_table_bytes gauge\n")
	for _, t := range top {
		writeMetricLine(b, "aiops_pg_table_bytes", float64(t.TotalBytes), "table", t.Name)
	}
	fmt.Fprintf(b, "# HELP aiops_pg_table_bloat_bytes Estimated dead space in the main heap of the largest tables\n# TYPE aiops_pg_table_bloat_bytes gauge\n")
	for _, t := range top {
		writeMetricLine(b, "aiops_pg_table_bloat_bytes", float64(t.bloatBytes()), "table", t.Name)
	}
	fmt.Fprintf(b, "# HELP aiops_pg_table_dead_tuples Dead row versions awaiting vacuum in the largest tables\n# TYPE aiops_pg_table_dead_tuples gauge\n")
	for _, t := range top {
		writeMetricLine(b, "aiops_pg_table_dead_tuples", float64(t.DeadTuples), "table", t.Name)
	}
}
