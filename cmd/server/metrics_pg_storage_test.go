package main

import (
	"strings"
	"testing"
	"time"
)

func TestWritePGStorageMetricsEmptyWhenNeverProbed(t *testing.T) {
	var b strings.Builder
	writePGStorageMetrics(&b, pgStorageSnapshot{}, time.Now())
	if b.Len() != 0 {
		t.Fatalf("没有 PG 时不应输出任何 series，得到:\n%s", b.String())
	}
}

// 探针失败时必须留下一条 aiops_pg_metrics_error=1，而不是"什么都不输出"——
// 后者在 Prometheus 里和"一切正常"长得一模一样。
// 首次就失败（还没有过任何一次成功）时，只给标记，不能凭空造出体积数字。
func TestWritePGStorageMetricsReportsProbeFailure(t *testing.T) {
	var b strings.Builder
	writePGStorageMetrics(&b, pgStorageSnapshot{erredAt: time.Now(), err: errProbe{}}, time.Now())
	out := b.String()
	if !strings.Contains(out, "aiops_pg_metrics_error 1") {
		t.Fatalf("缺少失败标记:\n%s", out)
	}
	if strings.Contains(out, "aiops_pg_database_bytes") {
		t.Fatalf("从未成功过时不应输出体积数字（会被当成真值）:\n%s", out)
	}
}

// 探针失败但**曾经成功过**：上一份好数据要继续供着，另配 age 说明它有多旧。
// 出错就整段不输出，等于让存储指标在 PG 最不健康的时候消失。
func TestWritePGStorageMetricsKeepsLastGoodOnFailure(t *testing.T) {
	now := time.Now()
	var b strings.Builder
	writePGStorageMetrics(&b, pgStorageSnapshot{
		takenAt: now.Add(-4 * time.Minute), dbBytes: 12345, reclaimableBytes: 999,
		err: errProbe{}, erredAt: now,
	}, now)
	out := b.String()
	if !strings.Contains(out, "aiops_pg_metrics_error 1") {
		t.Fatalf("缺少失败标记:\n%s", out)
	}
	if !strings.Contains(out, "aiops_pg_database_bytes 12345") {
		t.Fatalf("失败时丢掉了上一份好数据:\n%s", out)
	}
	if !strings.Contains(out, "aiops_pg_metrics_age_seconds 240") {
		t.Fatalf("缺少新鲜度（应为 240 秒）:\n%s", out)
	}
}

// 失败快照按 TTL 缓存，会让平台每次重启后存储指标失明 10 分钟——
// 因为启动那几秒连接池被建表占满，8 秒探针超时极易撞穿。失败必须走短重试。
func TestPGStorageFailedProbeRetriesQuickly(t *testing.T) {
	now := time.Now()
	failed := pgStorageSnapshot{err: errProbe{}, erredAt: now}
	if pgStorageRefreshDue(failed, now.Add(pgStorageRetryAfter-time.Second)) {
		t.Error("重试间隔未到就重跑，会把一个挣扎中的 PG 打得更狠")
	}
	if !pgStorageRefreshDue(failed, now.Add(pgStorageRetryAfter+time.Second)) {
		t.Errorf("失败后应在 %v 内重试，而不是等满 %v 的 TTL", pgStorageRetryAfter, pgStorageMetricsTTL)
	}
	// 成功的快照仍然走长 TTL：那条查询要扫 pg_class/pg_stats，不能跟着抓取跑。
	ok := pgStorageSnapshot{takenAt: now}
	if pgStorageRefreshDue(ok, now.Add(pgStorageMetricsTTL-time.Second)) {
		t.Error("成功快照不应提前失效")
	}
	if !pgStorageRefreshDue(ok, now.Add(pgStorageMetricsTTL+time.Second)) {
		t.Error("成功快照过了 TTL 应刷新")
	}
	if !pgStorageRefreshDue(pgStorageSnapshot{}, now) {
		t.Error("从未探测过时应立即探测")
	}
}

func TestWritePGStorageMetricsTopNAndValues(t *testing.T) {
	stats := make([]pgTableStat, 0, pgStorageMetricsTopN+5)
	for i := 0; i < pgStorageMetricsTopN+5; i++ {
		stats = append(stats, pgTableStat{
			Name:       "t" + string(rune('a'+i)),
			TotalBytes: int64(1000 - i),
			HeapBytes:  int64(900 - i),
			DeadTuples: int64(i),
		})
	}
	var b strings.Builder
	writePGStorageMetrics(&b, pgStorageSnapshot{
		takenAt: time.Now(), dbBytes: 12345, tables: stats,
		reclaimableBytes: 999, candidates: 2,
	}, time.Now())
	out := b.String()
	if !strings.Contains(out, "aiops_pg_database_bytes 12345") {
		t.Errorf("库体积缺失:\n%s", out)
	}
	if !strings.Contains(out, "aiops_pg_reclaimable_bytes 999") {
		t.Errorf("可回收字节缺失:\n%s", out)
	}
	if !strings.Contains(out, "aiops_pg_reclaim_candidate_tables 2") {
		t.Errorf("候选表数缺失:\n%s", out)
	}
	if n := strings.Count(out, "aiops_pg_table_bytes{"); n != pgStorageMetricsTopN {
		t.Errorf("表级 series 应被截到 %d 条，实际 %d 条", pgStorageMetricsTopN, n)
	}
	// 截断必须按体积取最大的那一批，而不是查询返回的前 N 条。
	if strings.Contains(out, `table="t`+string(rune('a'+pgStorageMetricsTopN+4))+`"`) {
		t.Errorf("最小的表不该进入 topN:\n%s", out)
	}
}

type errProbe struct{}

func (errProbe) Error() string { return "probe failed" }

// 抓取绝不能被 PG 拖住：/metrics 是运维判断"平台还活着吗"的入口，
// 让它跟着一个正在挣扎的 PG 一起卡死，等于在最需要指标的时候把指标全弄丢。
func TestPGStorageMetricsNeverBlocksScrape(t *testing.T) {
	pgStorageMu.Lock()
	pgStorageSnap = pgStorageSnapshot{}
	pgStorageRefreshing = true // 假装后台刷新正在进行且很慢
	pgStorageMu.Unlock()
	t.Cleanup(func() {
		pgStorageMu.Lock()
		pgStorageSnap, pgStorageRefreshing = pgStorageSnapshot{}, false
		pgStorageMu.Unlock()
	})

	s := &Server{}
	done := make(chan pgStorageSnapshot, 1)
	go func() { done <- s.pgStorageMetrics(time.Now()) }()
	select {
	case snap := <-done:
		if !snap.takenAt.IsZero() {
			t.Fatalf("没有 PG 时应返回空快照，得到 %+v", snap)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pgStorageMetrics 阻塞了抓取")
	}
}
