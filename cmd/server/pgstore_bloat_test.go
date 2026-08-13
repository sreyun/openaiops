package main

import "testing"

// The case that broke the loop: a table that autovacuum has already cleaned.
// n_dead_tup is back to ~0, so the old dead-tuple-only rule reported "no bloat
// worth reclaiming" — while the heap file was still sitting at its multi-GB high
// water mark, which is exactly what the operator sees on disk.
func TestReclaimCandidatesFindsVacuumedBloat(t *testing.T) {
	// 200k rows × ~200B ≈ 45 MiB of real data, occupying a 1 GiB heap.
	vacuumed := pgTableStat{
		Name:        "kv_state",
		TotalBytes:  1 << 30,
		HeapBytes:   1 << 30,
		LiveTuples:  200_000,
		RelTuples:   200_000,
		DeadTuples:  120, // autovacuum just ran
		AvgRowWidth: 200,
	}
	if r := vacuumed.deadRatio(); r > 0.01 {
		t.Fatalf("precondition: dead ratio should be negligible, got %.4f", r)
	}
	if vacuumed.bloatBytes() <= 0 {
		t.Fatal("bloat estimate must see the hole the dead-tuple ratio cannot")
	}
	if got := vacuumed.bloatRatio(); got < 0.9 {
		t.Errorf("bloat ratio = %.2f, want ≈0.95 for a 1 GiB heap holding ~45 MiB", got)
	}
	if len(reclaimCandidates([]pgTableStat{vacuumed})) != 1 {
		t.Error("an already-vacuumed but physically bloated table must be a reclaim candidate")
	}
}

// The pre-existing signal must keep working: bloat in progress, before
// autovacuum has caught up.
func TestReclaimCandidatesStillFindsDeadTuples(t *testing.T) {
	churning := pgTableStat{
		Name:       "hosts",
		TotalBytes: 64 << 20,
		HeapBytes:  64 << 20,
		LiveTuples: 500,
		RelTuples:  500,
		DeadTuples: 40_000,
		// No ANALYZE stats at all — the dead-tuple rule has to carry this one.
		AvgRowWidth: 0,
	}
	if churning.bloatBytes() != 0 {
		t.Error("without ANALYZE statistics the estimate must abstain, not guess")
	}
	if len(reclaimCandidates([]pgTableStat{churning})) != 1 {
		t.Error("high dead-tuple table must remain a candidate")
	}
	if churning.estReclaimBytes() <= 0 {
		t.Error("estimate should fall back to the dead-tuple share")
	}
}

// A compact table must never be proposed: VACUUM FULL takes an ACCESS EXCLUSIVE
// lock and rewrites the whole table, so a false positive costs real downtime.
func TestReclaimCandidatesSkipsHealthyTables(t *testing.T) {
	// ~200k rows × 200B, stored in a heap only slightly larger than required.
	healthy := pgTableStat{
		Name:        "audit_log",
		TotalBytes:  64 << 20,
		HeapBytes:   52 << 20,
		LiveTuples:  200_000,
		RelTuples:   200_000,
		DeadTuples:  100,
		AvgRowWidth: 200,
	}
	if got := healthy.bloatRatio(); got >= 0.30 {
		t.Fatalf("precondition: table should look compact, bloat ratio %.2f", got)
	}
	if n := len(reclaimCandidates([]pgTableStat{healthy})); n != 0 {
		t.Errorf("compact table proposed for VACUUM FULL (%d candidates)", n)
	}

	// Small tables stay out regardless of how bad the ratio looks.
	tiny := pgTableStat{
		Name: "schema_migrations", TotalBytes: 2 << 20, HeapBytes: 2 << 20,
		LiveTuples: 10,
		RelTuples:  10, DeadTuples: 900, AvgRowWidth: 50,
	}
	if n := len(reclaimCandidates([]pgTableStat{tiny})); n != 0 {
		t.Errorf("sub-16MiB table proposed for VACUUM FULL (%d candidates)", n)
	}

	// Big ratio but trivial absolute gain — not worth the lock.
	thin := pgTableStat{
		Name: "app_config", TotalBytes: 20 << 20, HeapBytes: 20 << 20,
		LiveTuples: 60_000,
		RelTuples:  60_000, DeadTuples: 5, AvgRowWidth: 250,
	}
	if thin.bloatBytes() >= 8<<20 {
		t.Skip("fixture no longer models a small absolute gain")
	}
	if n := len(reclaimCandidates([]pgTableStat{thin})); n != 0 {
		t.Errorf("table with <8MiB reclaimable proposed for VACUUM FULL (%d candidates)", n)
	}
}

// pg_stat_user_tables is fed by the asynchronous stats collector, so right after
// a bulk DELETE n_live_tup can still report the pre-delete row count. Sizing the
// estimate off that made a 90%-hollow table look healthy — caught by the live-PG
// integration test. pg_class.reltuples is written synchronously by VACUUM/ANALYZE
// and must win.
func TestBloatEstimatePrefersRelTuplesOverStaleStats(t *testing.T) {
	s := pgTableStat{
		Name:        "aiops_probe",
		TotalBytes:  13_287_424,
		HeapBytes:   13_287_424,
		LiveTuples:  60_000, // stale: the stats collector has not caught up
		RelTuples:   6_000,  // fresh: what actually survived the delete
		DeadTuples:  0,      // VACUUM already ran
		AvgRowWidth: 192,
	}
	// Measured on PostgreSQL 13: VACUUM FULL took this table 13,287,424 → 1,335,296 B.
	const actualReclaim = 13_287_424 - 1_335_296
	got := s.bloatBytes()
	if got == 0 {
		t.Fatal("estimate abstained on a table that is 90% hole")
	}
	if diff := float64(got-actualReclaim) / float64(actualReclaim); diff > 0.02 || diff < -0.10 {
		t.Errorf("estimate %d B vs measured %d B (%.1f%% off)", got, actualReclaim, diff*100)
	}

	// Fall back to n_live_tup only when reltuples carries no information.
	noRel := s
	noRel.RelTuples = 0
	if noRel.estRows() != 60_000 {
		t.Error("should fall back to n_live_tup when reltuples is unset")
	}
	// PG14+ marks "never vacuumed/analyzed" as -1: abstain rather than guess.
	never := s
	never.RelTuples = -1
	if never.estRows() != 0 || never.bloatBytes() != 0 {
		t.Error("reltuples = -1 means unknown; the estimate must abstain")
	}
}

// Never-analysed tables must abstain rather than report the whole heap as bloat,
// which would send the operator to lock a table for nothing.
func TestBloatEstimateAbstainsWithoutStatistics(t *testing.T) {
	for _, s := range []pgTableStat{
		{Name: "never_analyzed", HeapBytes: 1 << 30, LiveTuples: 1_000_000, RelTuples: 1_000_000, AvgRowWidth: 0},
		{Name: "empty_stats", HeapBytes: 1 << 30, LiveTuples: 0, RelTuples: 0, AvgRowWidth: 120},
	} {
		if s.expectedHeapBytes() != 0 || s.bloatBytes() != 0 {
			t.Errorf("%s: estimate must abstain without usable statistics", s.Name)
		}
	}
}
