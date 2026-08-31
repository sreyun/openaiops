package main

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// 这些用例跑真实 PostgreSQL（AIOPS_TEST_PG_DSN），因为它们断言的正是「有没有真的发生
// 一次行重写」——这件事只有数据库自己知道。判据用 xmin：PG 的 MVCC 里每次 UPDATE 都会
// 写一个新版本并带上新的事务号，所以 xmin 不变 ⇔ 这一轮一个死元组都没产生。用行数或
// 内容去断言是测不出来的：跳过写和重写成相同内容，从外面看结果一模一样。
func openIntegrationPGStore(t *testing.T) *pgStore {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("AIOPS_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("AIOPS_TEST_PG_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	if err := adminDB.PingContext(ctx); err != nil {
		t.Fatalf("connect to integration PostgreSQL: %v", err)
	}
	schema := "writecache_test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+quoteIdent(schema)); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_, _ = adminDB.ExecContext(dropCtx, `DROP SCHEMA IF EXISTS `+quoteIdent(schema)+` CASCADE`)
	})
	scopedDSN, err := postgresDSNWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	// Keep public on the search path *behind* the isolated schema: new tables still
	// land in the isolated one (it is first), but extension-provided types such as
	// pgvector's `vector` — installed once, in public — stay resolvable. Without
	// this the bootstrap DDL fails with `type "vector" does not exist`.
	scopedDSN = strings.Replace(scopedDSN, "search_path="+schema, "search_path="+schema+",public", 1)
	// openPGStore runs the full bootstrap + versioned migrations, so this also
	// exercises migration v13 against a real server.
	ps, err := openPGStore(scopedDSN)
	if err != nil {
		t.Fatalf("openPGStore against isolated schema: %v", err)
	}
	t.Cleanup(ps.close)
	return ps
}

func hostRowXmin(t *testing.T, ps *pgStore, id string) string {
	t.Helper()
	var xmin string
	if err := ps.db.QueryRow(`SELECT xmin::text FROM hosts WHERE id=$1`, id).Scan(&xmin); err != nil {
		t.Fatalf("read xmin for %s: %v", id, err)
	}
	return xmin
}

func sampleHost(id string, cpu float64, lastSeen int64) *Host {
	return &Host{
		ID: id, Hostname: "web-" + id, OS: "linux", Platform: "Debian 12", Arch: "amd64",
		IP: "10.0.0.5", Category: "prod", AgentVersion: "0.19.68",
		FirstSeen: 1786000000, LastSeen: lastSeen,
		Latest: &shared.Sample{Timestamp: lastSeen, Metrics: shared.Metrics{CPUPercent: cpu, MemPercent: 40}},
	}
}

// TestSaveHostsFastPassSkipsMetricChurn is the core claim of the fast/slow split:
// a host whose only change is its metrics must not cost a row rewrite on the 15s
// flush, but must still be persisted by the slow one.
func TestSaveHostsFastPassSkipsMetricChurn(t *testing.T) {
	ps := openIntegrationPGStore(t)

	h := sampleHost("h1", 10, 1786600000)
	if err := ps.saveHosts([]*Host{h}, true); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	base := hostRowXmin(t, ps, "h1")

	// Metric churn only — five fast flushes must produce zero writes.
	for i := 1; i <= 5; i++ {
		churn := sampleHost("h1", 10+float64(i)*7, 1786600000+int64(i)*15)
		if err := ps.saveHosts([]*Host{churn}, false); err != nil {
			t.Fatalf("fast flush %d: %v", i, err)
		}
	}
	if got := hostRowXmin(t, ps, "h1"); got != base {
		t.Fatalf("fast flush rewrote the row despite metric-only change (xmin %s → %s)", base, got)
	}

	// The slow flush is what persists the metrics — and it must actually store them.
	latest := sampleHost("h1", 93.5, 1786600100)
	if err := ps.saveHosts([]*Host{latest}, true); err != nil {
		t.Fatalf("slow flush: %v", err)
	}
	afterSlow := hostRowXmin(t, ps, "h1")
	if afterSlow == base {
		t.Fatal("slow flush must persist the metric snapshot (xmin unchanged)")
	}
	loaded, err := ps.loadHosts()
	if err != nil {
		t.Fatalf("loadHosts: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Latest == nil || loaded[0].Latest.CPUPercent != 93.5 {
		t.Fatalf("slow flush did not persist the latest sample: %+v", loaded)
	}

	// A second slow flush with identical content must still be a no-op.
	if err := ps.saveHosts([]*Host{latest}, true); err != nil {
		t.Fatalf("idempotent slow flush: %v", err)
	}
	if got := hostRowXmin(t, ps, "h1"); got != afterSlow {
		t.Fatalf("unchanged content was rewritten anyway (xmin %s → %s)", afterSlow, got)
	}
}

// TestSaveHostsFastPassStillWritesIdentityChanges pins the other half: the fast
// pass must not become a black hole for things that are NOT metrics.
func TestSaveHostsFastPassStillWritesIdentityChanges(t *testing.T) {
	ps := openIntegrationPGStore(t)

	h := sampleHost("h2", 10, 1786600000)
	if err := ps.saveHosts([]*Host{h}, true); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	base := hostRowXmin(t, ps, "h2")

	// An agent upgrade bumps AgentVersion — the fleet-update UI reads this back
	// after a restart, so it must not wait up to 5 minutes for the slow flush.
	upgraded := sampleHost("h2", 55, 1786600030)
	upgraded.AgentVersion = "0.19.69"
	if err := ps.saveHosts([]*Host{upgraded}, false); err != nil {
		t.Fatalf("fast flush with identity change: %v", err)
	}
	if got := hostRowXmin(t, ps, "h2"); got == base {
		t.Fatal("fast flush must write when agent_version changed")
	}
	loaded, err := ps.loadHosts()
	if err != nil {
		t.Fatalf("loadHosts: %v", err)
	}
	if len(loaded) != 1 || loaded[0].AgentVersion != "0.19.69" {
		t.Fatalf("identity change not persisted: %+v", loaded[0])
	}
}

// TestSaveHostsMirrorsDeletes covers the behaviour the old "DELETE 全表 + 重插"
// provided implicitly, including hosts removed while the process was down.
func TestSaveHostsMirrorsDeletes(t *testing.T) {
	ps := openIntegrationPGStore(t)

	a, b := sampleHost("ha", 10, 1786600000), sampleHost("hb", 20, 1786600000)
	if err := ps.saveHosts([]*Host{a, b}, true); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	if err := ps.saveHosts([]*Host{a}, true); err != nil {
		t.Fatalf("save after removal: %v", err)
	}
	loaded, err := ps.loadHosts()
	if err != nil {
		t.Fatalf("loadHosts: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "ha" {
		t.Fatalf("removed host was not mirrored away: %+v", loaded)
	}

	// Simulate a restart: re-seed the write cache, then reload hosts the way
	// BindPG does. An empty in-memory set must NOT wipe PG — that path is how a
	// failed/ignored loadHosts used to delete the fleet on the first flush.
	ps.wc = newPGWriteCache()
	if err := ps.saveHosts(nil, true); err == nil {
		t.Fatal("empty memory + non-empty PG must refuse mirror-delete")
	}
	loaded, err = ps.loadHosts()
	if err != nil {
		t.Fatalf("loadHosts after refused wipe: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "ha" {
		t.Fatalf("hosts were wiped despite safety latch: %+v", loaded)
	}
	// Proper restart path: load into memory, then flush — row survives.
	ps.wc = newPGWriteCache()
	if err := ps.saveHosts(loaded, true); err != nil {
		t.Fatalf("post-restart save with loaded hosts: %v", err)
	}
	loaded, err = ps.loadHosts()
	if err != nil {
		t.Fatalf("loadHosts after restart: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "ha" {
		t.Fatalf("host lost across restart simulation: %+v", loaded)
	}
}

// TestAutovacuumTuningMigrationApplied proves migration v13 actually reached the
// tables — a DO block that silently matches nothing would still record itself as
// applied and never run again.
func TestAutovacuumTuningMigrationApplied(t *testing.T) {
	ps := openIntegrationPGStore(t)

	for _, table := range []string{"hosts", "kv_state", "incidents"} {
		var opts, toastOpts sql.NullString
		// PG 把 toast.* 参数存在 **TOAST 副表** 的 reloptions 里，不是主表的——查主表
		// 永远看不到它们。kv_state 的大 blob 正是躺在 TOAST 里，这一半设置漏了的话，
		// 主表干净而 TOAST 继续膨胀，问题只解决了一半。
		err := ps.db.QueryRow(`
			SELECT array_to_string(c.reloptions, ','), array_to_string(t.reloptions, ',')
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_class t ON t.oid = c.reltoastrelid
			WHERE c.relname = $1 AND n.nspname = current_schema()`, table).Scan(&opts, &toastOpts)
		if err != nil {
			t.Fatalf("read reloptions for %s: %v", table, err)
		}
		if !opts.Valid || !strings.Contains(opts.String, "autovacuum_vacuum_scale_factor=0.01") {
			t.Errorf("%s did not get the autovacuum tuning (reloptions=%q)", table, opts.String)
		}
		if !toastOpts.Valid || !strings.Contains(toastOpts.String, "autovacuum_vacuum_scale_factor=0.01") {
			t.Errorf("%s TOAST table did not get the autovacuum tuning (toast reloptions=%q)", table, toastOpts.String)
		}
	}
}

// TestPGMaintenanceQueries keeps the storage diagnostic honest: its SQL only ever
// runs from an operator subcommand, so nothing else would catch a typo in it.
func TestPGMaintenanceQueries(t *testing.T) {
	ps := openIntegrationPGStore(t)

	if err := ps.saveHosts([]*Host{sampleHost("h1", 10, 1786600000)}, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	size, err := ps.databaseSize()
	if err != nil {
		t.Fatalf("databaseSize: %v", err)
	}
	if size <= 0 {
		t.Fatalf("databaseSize returned %d", size)
	}
	stats, err := ps.tableStats()
	if err != nil {
		t.Fatalf("tableStats: %v", err)
	}
	var found bool
	for _, s := range stats {
		if s.Name == "hosts" {
			found = true
			if s.deadRatio() < 0 || s.deadRatio() > 1 {
				t.Fatalf("deadRatio out of range: %v", s.deadRatio())
			}
		}
	}
	if !found {
		t.Fatal("tableStats did not report the hosts table")
	}
	// A compact freshly-written table must never be picked for a VACUUM FULL —
	// that would take an ACCESS EXCLUSIVE lock for nothing.
	for _, c := range reclaimCandidates(stats) {
		if c.Name == "hosts" {
			t.Fatal("a small, freshly written hosts table must not be a reclaim candidate")
		}
	}
}
