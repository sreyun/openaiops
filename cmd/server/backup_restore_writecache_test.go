package main

import (
	"testing"
)

// TestResetWriteCacheClearsSeedAndHashes ensures online restore can force the
// next flush to re-seed from PostgreSQL instead of trusting pre-restore hashes.
func TestResetWriteCacheClearsSeedAndHashes(t *testing.T) {
	c := newPGWriteCache()
	c.remember("hosts/h1", []byte(`{"id":"h1"}`))
	c.seed("hosts", []string{"h1"})
	if c.needsSeed("hosts") {
		t.Fatal("seeded table should not need seed")
	}
	if c.isChanged("hosts/h1", []byte(`{"id":"h1"}`)) {
		t.Fatal("unchanged payload should be cached as unchanged")
	}

	ps := &pgStore{wc: c}
	ps.resetWriteCache()
	if ps.wc == nil || ps.wc == c {
		t.Fatal("resetWriteCache must replace the cache instance")
	}
	if !ps.wc.needsSeed("hosts") {
		t.Fatal("after reset, hosts id set must re-seed from PG")
	}
	if !ps.wc.isChanged("hosts/h1", []byte(`{"id":"h1"}`)) {
		t.Fatal("after reset, previously-cached host must be treated as changed")
	}
}

// TestPGFlushSuspendedAfterRestore blocks the periodic mirror so pre-restore
// memory cannot overwrite (or skip-write against) the restored database.
func TestPGFlushSuspendedAfterRestore(t *testing.T) {
	was := pgFlushSuspended.Load()
	t.Cleanup(func() { pgFlushSuspended.Store(was) })

	pgFlushSuspended.Store(false)
	suspendPGFlushAfterRestore()
	if !pgFlushSuspended.Load() {
		t.Fatal("suspendPGFlushAfterRestore must set the hold flag")
	}

	srv := &Server{store: NewStore()}
	// Must return immediately without panicking on a nil pgStore — the guard
	// sits before any use of ps.
	srv.pgFlush(nil, true)
}
