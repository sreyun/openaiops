package main

import (
	"math"
	"os"
	"strings"
	"testing"
)

// End-to-end check of the bloat estimate against a real PostgreSQL, because the
// whole point of the estimate is to be trustworthy enough to justify taking an
// ACCESS EXCLUSIVE lock on a production table.
//
// Shape of the test mirrors the situation operators actually hit: bulk-delete
// most of a table, run a plain VACUUM (what autovacuum does), and observe that
// n_dead_tup drops to zero while the heap file stays at its high-water mark.
// The dead-tuple rule reports "nothing to reclaim" there; the estimate must not.
//
// Requires AIOPS_TEST_PG_DSN, like the other PG-gated suites.
func TestBloatEstimateMatchesRealVacuumFull(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AIOPS_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("AIOPS_TEST_PG_DSN 未设置，跳过真实 PG 膨胀估算校验")
	}
	ps, err := openPGStore(dsn)
	if err != nil {
		t.Fatalf("连接 PostgreSQL 失败: %v", err)
	}
	defer ps.close()

	const tbl = "aiops_bloat_estimate_probe"
	drop := func() { _, _ = ps.db.Exec(`DROP TABLE IF EXISTS ` + tbl) }
	drop()
	defer drop()

	mustExec := func(q string) {
		t.Helper()
		if _, err := ps.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`CREATE TABLE ` + tbl + ` (id bigserial primary key, payload text)`)
	mustExec(`INSERT INTO ` + tbl + `(payload) SELECT repeat('x',180) FROM generate_series(1,60000)`)
	// Delete 90%, then a plain VACUUM: space becomes reusable, the file does not shrink.
	mustExec(`DELETE FROM ` + tbl + ` WHERE id % 10 <> 0`)
	mustExec(`VACUUM ANALYZE ` + tbl)

	stat := findTableStat(t, ps, tbl)
	if stat.DeadTuples > stat.LiveTuples/10 {
		t.Skipf("autovacuum 尚未收敛（dead=%d live=%d），本次不做估算校验", stat.DeadTuples, stat.LiveTuples)
	}

	// The regression this guards: dead tuples say "clean", the file says otherwise.
	if got := stat.deadRatio(); got >= 0.20 {
		t.Fatalf("前提不成立：死元组占比 %.2f，本用例要验证的是 vacuum 之后的存量膨胀", got)
	}
	predicted := stat.bloatBytes()
	if predicted <= 0 {
		t.Fatal("VACUUM 之后仍有大量空洞，膨胀估算却报 0 —— 正是此前「清理了空间没降」的盲区")
	}

	beforeHeap := stat.HeapBytes
	mustExec(`VACUUM (FULL, ANALYZE) ` + tbl)
	afterHeap := findTableStat(t, ps, tbl).HeapBytes
	actual := beforeHeap - afterHeap
	if actual <= 0 {
		t.Fatalf("VACUUM FULL 未缩小堆文件（%d → %d）", beforeHeap, afterHeap)
	}

	// Accurate to a few percent, and biased low: over-promising reclaim would send
	// operators to lock tables for space that is not actually there.
	diff := float64(predicted-actual) / float64(actual)
	t.Logf("预估可回收 %d B，实际回收 %d B，偏差 %+.2f%%", predicted, actual, diff*100)
	if math.Abs(diff) > 0.10 {
		t.Errorf("膨胀估算偏差 %+.1f%% 超出 ±10%%（预估 %d B / 实际 %d B）", diff*100, predicted, actual)
	}
	if predicted > actual {
		if over := float64(predicted-actual) / float64(actual); over > 0.02 {
			t.Errorf("估算高报 %.1f%%：应偏保守，避免为不存在的空间去承担表锁", over*100)
		}
	}
}

func findTableStat(t *testing.T, ps *pgStore, name string) pgTableStat {
	t.Helper()
	stats, err := ps.tableStats()
	if err != nil {
		t.Fatalf("tableStats: %v", err)
	}
	for _, s := range stats {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("tableStats 未返回表 %s", name)
	return pgTableStat{}
}
