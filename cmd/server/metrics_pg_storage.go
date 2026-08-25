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
	// 失败后的重试间隔，远短于 TTL。
	//
	// 这条曾经是缺的，代价很具体：探针的 8 秒超时在**服务端刚启动**时极易撞穿——
	// 那几秒里连接池正被建表/迁移占满，一条平时 150ms 的统计查询要排十几秒才轮得上。
	// 一旦首刷失败，按 TTL 缓存就意味着接下来整整 10 分钟，客户的 Prometheus 里
	// 存储指标是**空的**。膨胀告警在平台每次重启后失明 10 分钟，不可接受。
	pgStorageRetryAfter = 30 * time.Second
	// 每张表两条 series，取体积最大的 N 张——剩下的都是几百 KB 的小表，
	// 全量导出只会把客户的 Prometheus 撑成高基数。
	pgStorageMetricsTopN = 15
)

type pgStorageSnapshot struct {
	dbBytes          int64
	tables           []pgTableStat
	reclaimableBytes int64
	candidates       int
	// takenAt 是最近一次**成功**采集的时刻；零值表示从未成功过。
	takenAt time.Time
	// err 是最近一次采集尝试的错误（成功则为 nil），erredAt 是它发生的时刻。
	// 与 takenAt 分开记：失败时上面那几个字段仍然是上一次的好数据。
	err     error
	erredAt time.Time
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
	if pgStorageRefreshDue(snap, now) && !pgStorageRefreshing {
		pgStorageRefreshing = true
		go s.refreshPGStorageSnapshot()
	}
	pgStorageMu.Unlock()
	return snap
}

// pgStorageRefreshDue 判断该不该再跑一次探针。失败走短重试，成功走长 TTL。
func pgStorageRefreshDue(snap pgStorageSnapshot, now time.Time) bool {
	if snap.err != nil {
		return now.Sub(snap.erredAt) >= pgStorageRetryAfter
	}
	if snap.takenAt.IsZero() {
		return true
	}
	return now.Sub(snap.takenAt) >= pgStorageMetricsTTL
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

	// 以上一份快照为底稿：这次失败了，上一次的好数字也要继续对外供着。
	pgStorageMu.Lock()
	snap := pgStorageSnap
	pgStorageMu.Unlock()

	stats, err := s.pg.tableStatsContext(ctx)
	if err != nil {
		snap.err, snap.erredAt = err, time.Now()
		slog.Warn("采集 PostgreSQL 存储指标失败（沿用上一份快照，稍后重试）",
			"err", err, "retry_after", pgStorageRetryAfter, "has_previous", !snap.takenAt.IsZero())
	} else {
		snap = pgStorageSnapshot{takenAt: time.Now(), tables: stats}
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
//
// 探针失败时**依然输出上一份好数据**，另配 aiops_pg_metrics_age_seconds 说明它有多旧。
// 反过来做（出错就整段不输出）等于让存储指标在 PG 最不健康的时候消失，而膨胀是以天
// 计的量：一份几分钟前的真值，永远比一个空洞更有用。运维要的是
// `aiops_pg_metrics_age_seconds > 1800` 这样一条告警，不是一段没有数据的时间轴。
func writePGStorageMetrics(b *strings.Builder, snap pgStorageSnapshot, now time.Time) {
	if snap.takenAt.IsZero() && snap.err == nil {
		return // 从未探测过（没有 PG，或刚启动第一轮还没回来）
	}
	fmt.Fprintf(b, "# HELP aiops_pg_metrics_error 1 when the last PostgreSQL storage probe failed\n# TYPE aiops_pg_metrics_error gauge\n")
	errVal := 0.0
	if snap.err != nil {
		errVal = 1
	}
	writeMetricLine(b, "aiops_pg_metrics_error", errVal)
	if snap.takenAt.IsZero() {
		return // 失败，且还从没成功过——没有任何可信数字可供
	}

	fmt.Fprintf(b, "# HELP aiops_pg_metrics_age_seconds Age of the last successful PostgreSQL storage probe\n# TYPE aiops_pg_metrics_age_seconds gauge\n")
	age := now.Sub(snap.takenAt).Seconds()
	if age < 0 {
		age = 0
	}
	writeMetricLine(b, "aiops_pg_metrics_age_seconds", age)

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
