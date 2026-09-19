package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testBackupServer(t *testing.T) *Server {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cfg.json")
	cs, err := NewConfigStore(cfgPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: cs, store: NewStore()}
}

// TestPruneBackupsExceptProtectsRestoreTarget pins the DR failure mode where
// restorePGBackup's pre-restore safety dump pushes the fleet over RetainCount
// and prune deletes the dump about to be restored — AFTER Stat already passed,
// BEFORE DROP DATABASE — leaving an empty DB and a missing restore path.
func TestPruneBackupsExceptProtectsRestoreTarget(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AIOPS_BACKUP_DIR", dir)
	srv := testBackupServer(t)

	mk := func(name string, ageMin int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("dump"), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(-time.Duration(ageMin) * time.Minute)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
		return name
	}

	// retain=2 → without protection the oldest of 3 is deleted when a 4th arrives.
	oldest := mk("aiops-pg-20260101-000000.dump", 40)
	mk("aiops-pg-20260102-000000.dump", 30)
	mk("aiops-pg-20260103-000000.dump", 20)
	mk("aiops-pg-20260104-safety.dump", 1) // simulates the fresh safety dump

	srv.pruneBackupsExcept(2, oldest)

	if _, err := os.Stat(filepath.Join(dir, oldest)); err != nil {
		t.Fatalf("restore target %s must survive prune: %v", oldest, err)
	}
	// Newest two non-protected still kept; the middle one beyond retain is gone.
	if _, err := os.Stat(filepath.Join(dir, "aiops-pg-20260102-000000.dump")); !os.IsNotExist(err) {
		t.Fatalf("unprotected overflow dump should be pruned, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aiops-pg-20260103-000000.dump")); err != nil {
		t.Fatalf("second-newest should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aiops-pg-20260104-safety.dump")); err != nil {
		t.Fatalf("safety dump should remain: %v", err)
	}
}

// TestPruneBackupsWithoutProtectStillTrimsOldest keeps the default retain
// behaviour unchanged when no restore is in flight.
func TestPruneBackupsWithoutProtectStillTrimsOldest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AIOPS_BACKUP_DIR", dir)
	srv := testBackupServer(t)

	for i, age := range []int{40, 30, 20} {
		name := "aiops-pg-2026010" + string(rune('1'+i)) + "-000000.dump"
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(-time.Duration(age) * time.Minute)
		_ = os.Chtimes(p, ts, ts)
	}
	srv.pruneBackups(2)
	if _, err := os.Stat(filepath.Join(dir, "aiops-pg-20260101-000000.dump")); !os.IsNotExist(err) {
		t.Fatalf("oldest unprotected dump should be removed, err=%v", err)
	}
}
