package main

import (
	"log/slog"
	"testing"
)

// TestSuspendFlushAfterRestoreBlocksPGFlush pins the critical restore safety
// property introduced with the write-dedup cache: after an in-process
// drop-and-recreate restore, memory still holds the pre-restore snapshot while
// PG holds the dump. Any further pgFlush (15s ticker or SIGTERM heavy flush)
// must no-op, otherwise live hosts/incidents partially overwrite the restore.
func TestSuspendFlushAfterRestoreBlocksPGFlush(t *testing.T) {
	ps := &pgStore{wc: newPGWriteCache()}
	if !ps.flushAllowed() {
		t.Fatal("fresh pgStore must allow flush")
	}

	ps.suspendFlushAfterRestore()
	if ps.flushAllowed() {
		t.Fatal("suspendFlushAfterRestore must block flushAllowed")
	}

	// pgFlush must return before touching manager exports. A Server with nil
	// incident/ticket managers would panic if the suspend guard were missing.
	s := &Server{pg: ps}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	defer slog.SetDefault(prev)
	s.pgFlush(ps, true)
}

func TestSuspendFlushAfterRestoreIsIdempotent(t *testing.T) {
	ps := &pgStore{wc: newPGWriteCache()}
	ps.suspendFlushAfterRestore()
	ps.suspendFlushAfterRestore()
	if ps.flushAllowed() {
		t.Fatal("repeated suspend must keep flush blocked")
	}
	var nilStore *pgStore
	nilStore.suspendFlushAfterRestore() // must not panic
	if nilStore.flushAllowed() {
		t.Fatal("nil pgStore must not report flushAllowed")
	}
}
