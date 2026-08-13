package main

import (
	"fmt"
	"log/slog"
	"time"
)

// schemaMigration is one numbered, idempotent-by-version DDL step.
type schemaMigration struct {
	Version int
	Name    string
	SQL     string
}

// enterpriseOpsMigrations covers On-call pages, change records, and backup metadata.
// Bootstrap tables remain in migrateBootstrap's CREATE IF NOT EXISTS block (v0 compatible).
var enterpriseOpsMigrations = []schemaMigration{
	{
		Version: 1,
		Name:    "schema_migrations_bootstrap_marker",
		SQL:     `-- no-op: ensures version bookkeeping exists after table create`,
	},
	{
		Version: 2,
		Name:    "oncall_pages",
		SQL: `
CREATE TABLE IF NOT EXISTS oncall_pages (
	id         BIGSERIAL PRIMARY KEY,
	incident_id BIGINT NOT NULL,
	status     TEXT NOT NULL,
	created_at BIGINT NOT NULL,
	data       JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS oncall_pages_status ON oncall_pages(status);
CREATE INDEX IF NOT EXISTS oncall_pages_incident ON oncall_pages(incident_id);
`,
	},
	{
		Version: 3,
		Name:    "change_records",
		SQL: `
CREATE TABLE IF NOT EXISTS change_records (
	id         BIGSERIAL PRIMARY KEY,
	status     TEXT NOT NULL,
	started_at BIGINT NOT NULL,
	data       JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS change_records_started ON change_records(started_at DESC);
CREATE INDEX IF NOT EXISTS change_records_status ON change_records(status);
`,
	},
	{
		Version: 4,
		Name:    "backup_meta",
		SQL: `
CREATE TABLE IF NOT EXISTS backup_meta (
	id          TEXT PRIMARY KEY,
	created_at  BIGINT NOT NULL,
	size_bytes  BIGINT NOT NULL DEFAULT 0,
	sha256      TEXT NOT NULL DEFAULT '',
	operator    TEXT NOT NULL DEFAULT '',
	path        TEXT NOT NULL,
	note        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS backup_meta_created ON backup_meta(created_at DESC);
`,
	},
	{
		Version: 5,
		Name:    "partition_backfill_marker",
		SQL:     `-- backfill of audit_log/events/ai_call_events into *_p is done in migrateDualTrackPartitions`,
	},
	{
		Version: 6,
		Name:    "optimistic_lock_version_ai_runs",
		SQL:     `ALTER TABLE ai_runs ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 1;`,
	},
	{
		Version: 7,
		Name:    "optimistic_lock_version_incidents",
		SQL:     `ALTER TABLE incidents ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 1;`,
	},
	{
		Version: 8,
		Name:    "hosts_updated_at",
		SQL:     `ALTER TABLE hosts ADD COLUMN IF NOT EXISTS updated_at BIGINT NOT NULL DEFAULT 0;`,
	},
	{
		Version: 9,
		Name:    "ai_call_events_usage_source",
		SQL: `
ALTER TABLE ai_call_events ADD COLUMN IF NOT EXISTS usage_source TEXT NOT NULL DEFAULT 'estimated';
ALTER TABLE ai_call_events_p ADD COLUMN IF NOT EXISTS usage_source TEXT NOT NULL DEFAULT 'estimated';
`,
	},
	{
		Version: 10,
		Name:    "ai_eval_runs",
		SQL: `
CREATE TABLE IF NOT EXISTS ai_eval_runs (
	id              TEXT PRIMARY KEY,
	ts              BIGINT NOT NULL,
	model           TEXT NOT NULL DEFAULT '',
	mode            TEXT NOT NULL DEFAULT '',   -- offline | online
	eval_set_version TEXT NOT NULL DEFAULT '',
	case_count      INT NOT NULL DEFAULT 0,
	passed_count    INT NOT NULL DEFAULT 0,
	pass_rate       DOUBLE PRECISION NOT NULL DEFAULT 0,
	root_cause_hit_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
	action_accept_rate  DOUBLE PRECISION NOT NULL DEFAULT 0,
	verify_agreement    DOUBLE PRECISION NOT NULL DEFAULT 0,
	detail          JSONB
);
CREATE INDEX IF NOT EXISTS ai_eval_runs_ts ON ai_eval_runs(ts DESC);
`,
	},
	{
		Version: 11,
		Name:    "ai_call_events_prompt_version",
		SQL: `
ALTER TABLE ai_call_events ADD COLUMN IF NOT EXISTS prompt_version TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_call_events_p ADD COLUMN IF NOT EXISTS prompt_version TEXT NOT NULL DEFAULT '';
`,
	},
	{
		Version: 12,
		Name:    "ai_call_events_route_reason",
		SQL: `
ALTER TABLE ai_call_events ADD COLUMN IF NOT EXISTS route_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_call_events_p ADD COLUMN IF NOT EXISTS route_reason TEXT NOT NULL DEFAULT '';
`,
	},
	{
		Version: 13,
		Name:    "autovacuum_tuning_write_heavy_tables",
		// 这些表被周期刷写反复 UPDATE，是「行数很少、死元组很多」的典型形态。PG 默认的
		// autovacuum_vacuum_scale_factor=0.2 是按**表的行数**算阈值的：hosts 只有几百行，
		// 一次刷写就能产生几百个死元组，但相对阈值同样只有几十——真正的问题不是触发不了，
		// 而是每一轮刷写都在制造新的死元组和 WAL，堆文件在 autovacuum 追上之前先膨胀起来，
		// 而 PG 只会把空间标记为「可复用」，不会把文件还给操作系统。
		//
		// 把 scale_factor 压到 0.01 并设一个小的绝对阈值，让 autovacuum 更贴身地跟着写入
		// 跑，堆就能稳定在一个小的高水位上，而不是一路涨到几 GB。kv_state 的大 blob 存在
		// TOAST 副表里，必须单独设 toast.* 参数，否则主表干净、TOAST 继续膨胀。
		//
		// 这只是「以后不再涨」；已经积下来的膨胀要靠 -pg-reclaim 做一次性回收（VACUUM FULL
		// 会持 ACCESS EXCLUSIVE 锁，故意不放在启动路径上自动执行）。
		// to_regclass(quote_ident(t)) 走 search_path 解析，不写死 public —— 部署可以把
		// 整套表放在自定义 schema 里（集成测试就是这么隔离的），写死 public 会让这条迁移
		// 悄悄什么都不做，却把自己记成「已应用」，从此再也不会重跑。
		SQL: `
DO $$
DECLARE t TEXT; reg regclass;
BEGIN
  FOREACH t IN ARRAY ARRAY['hosts','kv_state','incidents','tickets','oncall_pages','change_records',
                           'hardware_snapshot','snmp_snapshot','container_inventory','hyperv_inventory']
  LOOP
    reg := to_regclass(quote_ident(t));
    IF reg IS NOT NULL THEN
      EXECUTE format('ALTER TABLE %s SET ('
        || 'autovacuum_vacuum_scale_factor = 0.01, autovacuum_vacuum_threshold = 50, '
        || 'autovacuum_analyze_scale_factor = 0.05, autovacuum_vacuum_cost_delay = 0)', reg);
      EXECUTE format('ALTER TABLE %s SET ('
        || 'toast.autovacuum_vacuum_scale_factor = 0.01, toast.autovacuum_vacuum_threshold = 50)', reg);
    END IF;
  END LOOP;
END $$;
`,
	},
}

// runVersionedMigrations applies numbered schema steps after the bootstrap IF NOT EXISTS schema.
// Failures are fatal to the caller (openPGStore) so we never run half-migrated.
func (p *pgStore) runVersionedMigrations() error {
	if _, err := p.db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INT PRIMARY KEY,
	name       TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := p.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, m := range enterpriseOpsMigrations {
		if applied[m.Version] {
			continue
		}
		tx, err := p.db.Begin()
		if err != nil {
			return err
		}
		if m.SQL != "" {
			if _, err := tx.Exec(m.SQL); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration v%d %s: %w", m.Version, m.Name, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES($1,$2,$3)`,
			m.Version, m.Name, time.Now()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration v%d: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		slog.Info("schema migration applied", "version", m.Version, "name", m.Name)
	}
	return nil
}
