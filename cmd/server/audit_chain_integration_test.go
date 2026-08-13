package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendAuditChainedConcurrentPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AIOPS_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("AIOPS_TEST_PG_DSN is not set")
	}
	t.Setenv("AIOPS_SECRET_KEY", "audit-concurrency-test-passphrase")
	t.Setenv("AIOPS_SECRET_KEY_ID", "integration-test")
	t.Setenv("AIOPS_SECRET_KEYS_PREV", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	if err := adminDB.PingContext(ctx); err != nil {
		t.Fatalf("connect to integration PostgreSQL: %v", err)
	}

	schema := "audit_chain_test_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+quoteIdent(schema)); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	scopedDSN, err := postgresDSNWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	scopedDB, err := sql.Open("postgres", scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	scopedDB.SetMaxOpenConns(10)
	t.Cleanup(func() {
		_ = scopedDB.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = adminDB.ExecContext(dropCtx, `DROP SCHEMA IF EXISTS `+quoteIdent(schema)+` CASCADE`)
	})
	if err := scopedDB.PingContext(ctx); err != nil {
		t.Fatalf("connect with isolated search path: %v", err)
	}

	for _, ddl := range []string{
		`CREATE TABLE audit_log (
			id BIGSERIAL PRIMARY KEY,
			ts BIGINT NOT NULL,
			data JSONB NOT NULL,
			content_hash TEXT NOT NULL DEFAULT '',
			prev_hash TEXT NOT NULL DEFAULT '',
			chain_seq BIGINT NOT NULL DEFAULT 0,
			chain_version SMALLINT NOT NULL DEFAULT 1,
			chain_key_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE audit_log_p (
			id BIGINT PRIMARY KEY,
			ts BIGINT NOT NULL,
			data JSONB NOT NULL,
			content_hash TEXT NOT NULL DEFAULT '',
			prev_hash TEXT NOT NULL DEFAULT '',
			chain_seq BIGINT NOT NULL DEFAULT 0,
			chain_version SMALLINT NOT NULL DEFAULT 1,
			chain_key_id TEXT NOT NULL DEFAULT ''
		)`,
	} {
		if _, err := scopedDB.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("create isolated audit table: %v", err)
		}
	}

	store := &pgStore{db: scopedDB}
	const appenders = 8
	start := make(chan struct{})
	seqs := make(chan int64, appenders)
	errs := make(chan error, appenders)
	var wg sync.WaitGroup
	for i := 0; i < appenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			seq, err := store.appendAuditChained(ctx, LogEntry{
				Timestamp: 1786284000 + int64(i),
				Kind:      "operation",
				Level:     "info",
				Actor:     "integration-test",
				Message:   fmt.Sprintf("concurrent append %d", i),
			})
			if err != nil {
				errs <- err
				return
			}
			seqs <- seq
		}(i)
	}
	close(start)
	wg.Wait()
	close(seqs)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	}

	gotSeqs := make([]int, 0, appenders)
	for seq := range seqs {
		gotSeqs = append(gotSeqs, int(seq))
	}
	sort.Ints(gotSeqs)
	for i, seq := range gotSeqs {
		if want := i + 1; seq != want {
			t.Fatalf("committed sequences = %v, want 1-%d", gotSeqs, appenders)
		}
	}

	var legacyRows, partitionRows, matchingTwins, distinctSeqs int
	err = scopedDB.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM audit_log),
  (SELECT COUNT(*) FROM audit_log_p),
  (SELECT COUNT(*) FROM audit_log a JOIN audit_log_p p USING (id)
   WHERE a.ts = p.ts
     AND a.data = p.data
     AND a.content_hash = p.content_hash
     AND a.prev_hash = p.prev_hash
     AND a.chain_seq = p.chain_seq
     AND a.chain_version = p.chain_version
     AND a.chain_key_id = p.chain_key_id),
  (SELECT COUNT(DISTINCT chain_seq) FROM audit_log_p)`).Scan(
		&legacyRows, &partitionRows, &matchingTwins, &distinctSeqs,
	)
	if err != nil {
		t.Fatal(err)
	}
	if legacyRows != appenders || partitionRows != appenders || matchingTwins != appenders || distinctSeqs != appenders {
		t.Fatalf("legacy=%d partition=%d twins=%d sequences=%d, want all %d",
			legacyRows, partitionRows, matchingTwins, distinctSeqs, appenders)
	}

	verified, err := store.verifyAuditChain(ctx, appenders)
	if err != nil {
		t.Fatal(err)
	}
	if !verified.OK || verified.Status != "healthy" || !verified.MirrorParity || verified.Checked != appenders {
		t.Fatalf("verification = %+v, want healthy %d-link mirrored chain", verified, appenders)
	}
}

func postgresDSNWithSearchPath(dsn, schema string) (string, error) {
	// Allow-list the prefixes the integration tests generate. The schema name is
	// interpolated into DDL, so it must never come from anywhere but here.
	if !isSafePartitionName(schema, "audit_chain_test") && !isSafePartitionName(schema, "writecache_test") {
		return "", fmt.Errorf("unsafe test schema")
	}
	parsed, err := url.Parse(dsn)
	if err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema, nil
}
