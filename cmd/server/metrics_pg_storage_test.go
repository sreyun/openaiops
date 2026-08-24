package main

import (
	"strings"
	"testing"
	"time"
)

func TestWritePGStorageMetricsEmptyWhenNeverProbed(t *testing.T) {
	var b strings.Builder
	writePGStorageMetrics(&b, pgStorageSnapshot{})
	if b.Len() != 0 {
		t.Fatalf("没有 PG 时不应输出任何 series，得到:\n%s", b.String())
	}
}

// 探针失败时必须留下一条 aiops_pg_metrics_error=1，而不是"什么都不输出"——
// 后者在 Prometheus 里和"一切正常"长得一模一样。
func TestWritePGStorageMetricsReportsProbeFailure(t *testing.T) {
	var b strings.Builder
	writePGStorageMetrics(&b, pgStorageSnapshot{takenAt: time.Now(), err: errProbe{}})
	out := b.String()
	if !strings.Contains(out, "aiops_pg_metrics_error 1") {
		t.Fatalf("缺少失败标记:\n%s", out)
	}
	if strings.Contains(out, "aiops_pg_database_bytes") {
		t.Fatalf("探针失败时不应输出体积数字（会被当成真值）:\n%s", out)
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
	})
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
