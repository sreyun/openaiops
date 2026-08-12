package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type errAuditMirrorConflict struct {
	Kind  string
	Value string
}

func (e *errAuditMirrorConflict) Error() string {
	return fmt.Sprintf("audit mirror conflict: %s=%s", e.Kind, e.Value)
}

type auditMirrorStats struct {
	LegacyRows          int64
	PartitionRows       int64
	UniqueHashes        int64
	RemovedDuplicates   int64
	BackfilledLegacy    int64
	BackfilledPartition int64
}

// applyPGPoolSettings configures the sql.DB pool from env (defaults match historical values).
func applyPGPoolSettings(db interface {
	SetMaxOpenConns(int)
	SetMaxIdleConns(int)
	SetConnMaxLifetime(time.Duration)
	SetConnMaxIdleTime(time.Duration)
}) {
	maxOpen := envIntDefault("AIOPS_PG_MAX_OPEN", 200)
	maxIdle := envIntDefault("AIOPS_PG_MAX_IDLE", 50)
	lifeMin := envIntDefault("AIOPS_PG_CONN_LIFE_MIN", 30)
	idleMin := envIntDefault("AIOPS_PG_CONN_IDLE_MIN", 5)
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(time.Duration(lifeMin) * time.Minute)
	db.SetConnMaxIdleTime(time.Duration(idleMin) * time.Minute)
}

func envIntDefault(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// ensureTSPartitions creates monthly range partitions for a parent table
// partitioned by BIGINT unix ts. Parent must already exist.
func (p *pgStore) ensureTSPartitions(parent string, monthsAhead int) {
	if p == nil || p.db == nil || parent == "" {
		return
	}
	if monthsAhead < 1 {
		monthsAhead = 2
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	for i := 0; i < monthsAhead+2; i++ {
		mStart := start.AddDate(0, i, 0)
		mEnd := mStart.AddDate(0, 1, 0)
		name := fmt.Sprintf("%s_%04d%02d", parent, mStart.Year(), int(mStart.Month()))
		if !isSafePartitionName(name, parent) {
			continue
		}
		fromTS, toTS := mStart.Unix(), mEnd.Unix()
		ddl := fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%d) TO (%d)`,
			quoteIdent(name), quoteIdent(parent), fromTS, toTS)
		if _, err := p.db.Exec(ddl); err != nil {
			slog.Debug("ensure partition", "table", name, "err", err)
		}
	}
}

func isSafePartitionName(name, parent string) bool {
	if !strings.HasPrefix(name, parent+"_") {
		return false
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

// migrateDualTrackPartitions creates partitioned twin tables for audit/events/ai_call.
func (p *pgStore) migrateDualTrackPartitions() error {
	if p == nil || p.db == nil {
		return sql.ErrConnDone
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS audit_log_p (
			id BIGSERIAL,
			ts BIGINT NOT NULL,
			data JSONB NOT NULL,
			content_hash TEXT NOT NULL DEFAULT '',
			prev_hash TEXT NOT NULL DEFAULT '',
			chain_seq BIGINT NOT NULL DEFAULT 0,
			chain_version SMALLINT NOT NULL DEFAULT 1,
			chain_key_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (id, ts)
		) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS audit_log_p_default PARTITION OF audit_log_p DEFAULT`,
		`CREATE INDEX IF NOT EXISTS audit_log_p_ts ON audit_log_p(ts DESC)`,
		`CREATE TABLE IF NOT EXISTS events_p (
			id BIGSERIAL,
			ts BIGINT NOT NULL,
			data JSONB NOT NULL,
			PRIMARY KEY (id, ts)
		) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS events_p_default PARTITION OF events_p DEFAULT`,
		`CREATE INDEX IF NOT EXISTS events_p_ts ON events_p(ts DESC)`,
		`CREATE TABLE IF NOT EXISTS ai_call_events_p (
			id BIGSERIAL,
			ts BIGINT NOT NULL,
			task TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			actor TEXT NOT NULL DEFAULT '',
			latency_ms BIGINT NOT NULL DEFAULT 0,
			ok BOOLEAN NOT NULL DEFAULT TRUE,
			error TEXT DEFAULT '',
			memory_hits INT DEFAULT 0,
			skill_hits INT DEFAULT 0,
			reply_chars INT DEFAULT 0,
			approx_tokens INT DEFAULT 0,
			prompt_tokens INT DEFAULT 0,
			completion_tokens INT DEFAULT 0,
			cost_estimate DOUBLE PRECISION DEFAULT 0,
			PRIMARY KEY (id, ts)
		) PARTITION BY RANGE (ts)`,
		`CREATE TABLE IF NOT EXISTS ai_call_events_p_default PARTITION OF ai_call_events_p DEFAULT`,
		`CREATE INDEX IF NOT EXISTS ai_call_events_p_ts ON ai_call_events_p(ts DESC)`,
		// hash-chain columns on legacy audit_log
		`ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS content_hash TEXT DEFAULT ''`,
		`ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS prev_hash TEXT DEFAULT ''`,
		`ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS chain_seq BIGINT DEFAULT 0`,
		`ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS chain_version SMALLINT NOT NULL DEFAULT 1`,
		`ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS chain_key_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE audit_log_p ADD COLUMN IF NOT EXISTS chain_version SMALLINT NOT NULL DEFAULT 1`,
		`ALTER TABLE audit_log_p ADD COLUMN IF NOT EXISTS chain_key_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS audit_log_content_hash ON audit_log(content_hash)`,
		`CREATE INDEX IF NOT EXISTS audit_log_chain_seq ON audit_log(chain_seq)`,
		`CREATE INDEX IF NOT EXISTS audit_log_p_content_hash ON audit_log_p(content_hash)`,
		`CREATE INDEX IF NOT EXISTS audit_log_p_chain_seq ON audit_log_p(chain_seq)`,
	}
	for _, s := range stmts {
		if _, err := p.db.Exec(s); err != nil {
			return fmt.Errorf("dual-track partition migrate: %w", err)
		}
	}
	var stats auditMirrorStats
	if err := p.withPgTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		stats, err = reconcileAuditMirrors(context.Background(), tx)
		if err != nil {
			return err
		}
		for _, stmt := range []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS audit_log_content_hash_uq
ON audit_log(content_hash) WHERE content_hash <> ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS audit_log_p_content_hash_ts_uq
ON audit_log_p(content_hash, ts) WHERE content_hash <> ''`,
		} {
			if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
				return fmt.Errorf("guard reconciled audit mirrors: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	slog.Info("audit mirrors reconciled",
		"legacy_rows", stats.LegacyRows,
		"partition_rows", stats.PartitionRows,
		"unique_hashes", stats.UniqueHashes,
		"removed_duplicates", stats.RemovedDuplicates,
		"backfilled_legacy", stats.BackfilledLegacy,
		"backfilled_partition", stats.BackfilledPartition)
	p.ensureTSPartitions("audit_log_p", 3)
	p.ensureTSPartitions("events_p", 3)
	p.ensureTSPartitions("ai_call_events_p", 3)
	return nil
}

func reconcileAuditMirrors(ctx context.Context, tx *sql.Tx) (auditMirrorStats, error) {
	var stats auditMirrorStats
	if tx == nil {
		return stats, sql.ErrTxDone
	}
	var contentHash string
	err := tx.QueryRowContext(ctx, `
SELECT content_hash
FROM (
  SELECT content_hash, ts, data, prev_hash, chain_seq, chain_version, chain_key_id FROM audit_log
  UNION ALL
  SELECT content_hash, ts, data, prev_hash, chain_seq, chain_version, chain_key_id FROM audit_log_p
) u
WHERE content_hash <> ''
GROUP BY content_hash
HAVING COUNT(DISTINCT ROW(ts, data, prev_hash, chain_seq, chain_version, chain_key_id)) > 1
LIMIT 1`).Scan(&contentHash)
	if err == nil {
		return stats, &errAuditMirrorConflict{Kind: "content_hash", Value: contentHash}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return stats, fmt.Errorf("probe audit content conflicts: %w", err)
	}

	var chainSeq int64
	err = tx.QueryRowContext(ctx, `
SELECT chain_seq
FROM (
  SELECT chain_seq, content_hash FROM audit_log WHERE content_hash <> ''
  UNION ALL
  SELECT chain_seq, content_hash FROM audit_log_p WHERE content_hash <> ''
) u
GROUP BY chain_seq
HAVING COUNT(DISTINCT content_hash) > 1
LIMIT 1`).Scan(&chainSeq)
	if err == nil {
		return stats, &errAuditMirrorConflict{Kind: "chain_seq", Value: strconv.FormatInt(chainSeq, 10)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return stats, fmt.Errorf("probe audit sequence conflicts: %w", err)
	}

	res, err := tx.ExecContext(ctx, `
WITH ranked AS (
  SELECT tableoid, ctid,
         ROW_NUMBER() OVER (PARTITION BY content_hash ORDER BY tableoid, ctid) AS copy_no
  FROM audit_log_p
  WHERE content_hash <> ''
)
DELETE FROM audit_log_p p
USING ranked r
WHERE p.tableoid = r.tableoid AND p.ctid = r.ctid AND r.copy_no > 1`)
	if err != nil {
		return stats, fmt.Errorf("remove exact audit mirrors: %w", err)
	}
	stats.RemovedDuplicates, err = res.RowsAffected()
	if err != nil {
		return stats, fmt.Errorf("count removed audit mirrors: %w", err)
	}

	res, err = tx.ExecContext(ctx, `
INSERT INTO audit_log (
  ts, data, content_hash, prev_hash, chain_seq, chain_version, chain_key_id
)
SELECT p.ts, p.data, p.content_hash, p.prev_hash, p.chain_seq, p.chain_version, p.chain_key_id
FROM audit_log_p p
WHERE p.content_hash <> ''
  AND NOT EXISTS (SELECT 1 FROM audit_log a WHERE a.content_hash = p.content_hash)
ORDER BY p.chain_seq, p.id`)
	if err != nil {
		return stats, fmt.Errorf("backfill legacy audit mirror: %w", err)
	}
	stats.BackfilledLegacy, err = res.RowsAffected()
	if err != nil {
		return stats, fmt.Errorf("count legacy audit backfill: %w", err)
	}

	res, err = tx.ExecContext(ctx, `
INSERT INTO audit_log_p (
  ts, data, content_hash, prev_hash, chain_seq, chain_version, chain_key_id
)
SELECT a.ts, a.data, a.content_hash, a.prev_hash, a.chain_seq, a.chain_version, a.chain_key_id
FROM audit_log a
WHERE a.content_hash <> ''
  AND NOT EXISTS (SELECT 1 FROM audit_log_p p WHERE p.content_hash = a.content_hash)
ORDER BY a.chain_seq, a.id`)
	if err != nil {
		return stats, fmt.Errorf("backfill partition audit mirror: %w", err)
	}
	stats.BackfilledPartition, err = res.RowsAffected()
	if err != nil {
		return stats, fmt.Errorf("count partition audit backfill: %w", err)
	}

	for _, stmt := range []string{
		`SELECT setval(pg_get_serial_sequence('audit_log','id'),
  COALESCE((SELECT MAX(id) FROM audit_log), 1),
  EXISTS(SELECT 1 FROM audit_log))`,
		`SELECT setval(pg_get_serial_sequence('audit_log_p','id'),
  COALESCE((SELECT MAX(id) FROM audit_log_p), 1),
  EXISTS(SELECT 1 FROM audit_log_p))`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return stats, fmt.Errorf("align audit identity sequence: %w", err)
		}
	}

	err = tx.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM audit_log) AS legacy_rows,
  (SELECT COUNT(*) FROM audit_log_p) AS partition_rows,
  (SELECT COUNT(DISTINCT content_hash)
     FROM (
       SELECT content_hash FROM audit_log WHERE content_hash <> ''
       UNION ALL
       SELECT content_hash FROM audit_log_p WHERE content_hash <> ''
     ) hashes) AS unique_hashes`).Scan(&stats.LegacyRows, &stats.PartitionRows, &stats.UniqueHashes)
	if err != nil {
		return stats, fmt.Errorf("count reconciled audit mirrors: %w", err)
	}
	return stats, nil
}

// backfillPartitionTwins reconciles event and AI-call multiplicity after all
// versioned columns exist. It never treats two equal business events as one.
func (p *pgStore) backfillPartitionTwins(ctx context.Context) error {
	return p.withPgTx(ctx, func(tx *sql.Tx) error {
		if err := alignTwinIdentitySequences(ctx, tx); err != nil {
			return err
		}
		stmts := []struct {
			name string
			sql  string
		}{
			{name: "events legacy to partition", sql: `
WITH legacy_ranked AS (
  SELECT ts, data,
         ROW_NUMBER() OVER (PARTITION BY ts, data ORDER BY id) AS copy_no
  FROM events
), partition_ranked AS (
  SELECT ts, data,
         ROW_NUMBER() OVER (PARTITION BY ts, data ORDER BY id) AS copy_no
  FROM events_p
)
INSERT INTO events_p(ts, data)
SELECT l.ts, l.data
FROM legacy_ranked l
WHERE NOT EXISTS (
  SELECT 1 FROM partition_ranked p
  WHERE p.ts = l.ts AND p.data = l.data AND p.copy_no = l.copy_no
)`},
			{name: "events partition to legacy", sql: `
WITH partition_ranked AS (
  SELECT ts, data,
         ROW_NUMBER() OVER (PARTITION BY ts, data ORDER BY id) AS copy_no
  FROM events_p
), legacy_ranked AS (
  SELECT ts, data,
         ROW_NUMBER() OVER (PARTITION BY ts, data ORDER BY id) AS copy_no
  FROM events
)
INSERT INTO events(ts, data)
SELECT p.ts, p.data
FROM partition_ranked p
WHERE NOT EXISTS (
  SELECT 1 FROM legacy_ranked l
  WHERE l.ts = p.ts AND l.data = p.data AND l.copy_no = p.copy_no
)`},
			{name: "AI calls legacy to partition", sql: `
WITH legacy_ranked AS (
  SELECT ts, task, model, actor, latency_ms, ok, error,
         memory_hits, skill_hits, reply_chars, approx_tokens,
         prompt_tokens, completion_tokens, cost_estimate,
         usage_source, prompt_version, route_reason,
         ROW_NUMBER() OVER (
           PARTITION BY ts, task, model, actor, latency_ms, ok, error,
                        memory_hits, skill_hits, reply_chars, approx_tokens,
                        prompt_tokens, completion_tokens, cost_estimate,
                        usage_source, prompt_version, route_reason
           ORDER BY id
         ) AS copy_no
  FROM ai_call_events
), partition_ranked AS (
  SELECT ts, task, model, actor, latency_ms, ok, error,
         memory_hits, skill_hits, reply_chars, approx_tokens,
         prompt_tokens, completion_tokens, cost_estimate,
         usage_source, prompt_version, route_reason,
         ROW_NUMBER() OVER (
           PARTITION BY ts, task, model, actor, latency_ms, ok, error,
                        memory_hits, skill_hits, reply_chars, approx_tokens,
                        prompt_tokens, completion_tokens, cost_estimate,
                        usage_source, prompt_version, route_reason
           ORDER BY id
         ) AS copy_no
  FROM ai_call_events_p
)
INSERT INTO ai_call_events_p(
  ts, task, model, actor, latency_ms, ok, error,
  memory_hits, skill_hits, reply_chars, approx_tokens,
  prompt_tokens, completion_tokens, cost_estimate,
  usage_source, prompt_version, route_reason
)
SELECT l.ts, l.task, l.model, l.actor, l.latency_ms, l.ok, l.error,
       l.memory_hits, l.skill_hits, l.reply_chars, l.approx_tokens,
       l.prompt_tokens, l.completion_tokens, l.cost_estimate,
       l.usage_source, l.prompt_version, l.route_reason
FROM legacy_ranked l
WHERE NOT EXISTS (
  SELECT 1 FROM partition_ranked p
  WHERE ROW(
    p.ts, p.task, p.model, p.actor, p.latency_ms, p.ok, p.error,
    p.memory_hits, p.skill_hits, p.reply_chars, p.approx_tokens,
    p.prompt_tokens, p.completion_tokens, p.cost_estimate,
    p.usage_source, p.prompt_version, p.route_reason
  ) IS NOT DISTINCT FROM ROW(
    l.ts, l.task, l.model, l.actor, l.latency_ms, l.ok, l.error,
    l.memory_hits, l.skill_hits, l.reply_chars, l.approx_tokens,
    l.prompt_tokens, l.completion_tokens, l.cost_estimate,
    l.usage_source, l.prompt_version, l.route_reason
  ) AND p.copy_no = l.copy_no
)`},
			{name: "AI calls partition to legacy", sql: `
WITH partition_ranked AS (
  SELECT ts, task, model, actor, latency_ms, ok, error,
         memory_hits, skill_hits, reply_chars, approx_tokens,
         prompt_tokens, completion_tokens, cost_estimate,
         usage_source, prompt_version, route_reason,
         ROW_NUMBER() OVER (
           PARTITION BY ts, task, model, actor, latency_ms, ok, error,
                        memory_hits, skill_hits, reply_chars, approx_tokens,
                        prompt_tokens, completion_tokens, cost_estimate,
                        usage_source, prompt_version, route_reason
           ORDER BY id
         ) AS copy_no
  FROM ai_call_events_p
), legacy_ranked AS (
  SELECT ts, task, model, actor, latency_ms, ok, error,
         memory_hits, skill_hits, reply_chars, approx_tokens,
         prompt_tokens, completion_tokens, cost_estimate,
         usage_source, prompt_version, route_reason,
         ROW_NUMBER() OVER (
           PARTITION BY ts, task, model, actor, latency_ms, ok, error,
                        memory_hits, skill_hits, reply_chars, approx_tokens,
                        prompt_tokens, completion_tokens, cost_estimate,
                        usage_source, prompt_version, route_reason
           ORDER BY id
         ) AS copy_no
  FROM ai_call_events
)
INSERT INTO ai_call_events(
  ts, task, model, actor, latency_ms, ok, error,
  memory_hits, skill_hits, reply_chars, approx_tokens,
  prompt_tokens, completion_tokens, cost_estimate,
  usage_source, prompt_version, route_reason
)
SELECT p.ts, p.task, p.model, p.actor, p.latency_ms, p.ok, p.error,
       p.memory_hits, p.skill_hits, p.reply_chars, p.approx_tokens,
       p.prompt_tokens, p.completion_tokens, p.cost_estimate,
       p.usage_source, p.prompt_version, p.route_reason
FROM partition_ranked p
WHERE NOT EXISTS (
  SELECT 1 FROM legacy_ranked l
  WHERE ROW(
    l.ts, l.task, l.model, l.actor, l.latency_ms, l.ok, l.error,
    l.memory_hits, l.skill_hits, l.reply_chars, l.approx_tokens,
    l.prompt_tokens, l.completion_tokens, l.cost_estimate,
    l.usage_source, l.prompt_version, l.route_reason
  ) IS NOT DISTINCT FROM ROW(
    p.ts, p.task, p.model, p.actor, p.latency_ms, p.ok, p.error,
    p.memory_hits, p.skill_hits, p.reply_chars, p.approx_tokens,
    p.prompt_tokens, p.completion_tokens, p.cost_estimate,
    p.usage_source, p.prompt_version, p.route_reason
  ) AND l.copy_no = p.copy_no
)`},
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt.sql); err != nil {
				return fmt.Errorf("backfill %s: %w", stmt.name, err)
			}
		}
		return alignTwinIdentitySequences(ctx, tx)
	})
}

func alignTwinIdentitySequences(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"events", "events_p", "ai_call_events", "ai_call_events_p"} {
		stmt := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s','id'),
  COALESCE((SELECT MAX(id) FROM %s), 1),
  EXISTS(SELECT 1 FROM %s))`, table, table, table)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("align %s identity sequence: %w", table, err)
		}
	}
	return nil
}

// cleanupOldTSPartitions drops monthly child partitions older than retainMonths.
func (p *pgStore) cleanupOldTSPartitions(parent string, retainMonths int) {
	if p == nil || p.db == nil || parent == "" {
		return
	}
	if retainMonths <= 0 {
		retainMonths = 12
	}
	p.ensureTSPartitions(parent, 3)
	cut := time.Now().UTC().AddDate(0, -retainMonths, 0)
	cutYM := cut.Year()*100 + int(cut.Month())
	rows, err := p.db.Query(`
SELECT c.relname FROM pg_class c
JOIN pg_inherits i ON i.inhrelid = c.oid
JOIN pg_class par ON par.oid = i.inhparent
WHERE par.relname = $1 AND c.relkind = 'r'`, parent)
	if err != nil {
		return
	}
	defer rows.Close()
	prefix := parent + "_"
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil || !isSafePartitionName(name, parent) {
			continue
		}
		if strings.HasSuffix(name, "_default") {
			continue
		}
		ymStr := strings.TrimPrefix(name, prefix)
		ym, err := strconv.Atoi(ymStr)
		if err != nil || ym < 197001 || ym >= cutYM {
			continue
		}
		ddl := fmt.Sprintf(`DROP TABLE IF EXISTS %s`, quoteIdent(name))
		if _, err := p.db.Exec(ddl); err != nil {
			slog.Warn("drop old partition", "table", name, "err", err)
		} else {
			slog.Info("dropped old partition", "table", name)
		}
	}
}
