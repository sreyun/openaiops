package main

import "testing"

func TestBackupsToPruneProtectsRestoreTarget(t *testing.T) {
	// Newest-first list after a pre-restore safety dump is inserted.
	// retain=2 would previously drop the oldest entry — which is the dump
	// being restored — before pg_restore runs.
	list := []BackupMeta{
		{ID: "safety.dump"},
		{ID: "keep.dump"},
		{ID: "restore-me.dump"},
	}
	got := backupsToPrune(list, 2, "restore-me.dump")
	if len(got) != 0 {
		t.Fatalf("protected restore target should not be pruned, got %+v", got)
	}

	// Without protection the oldest falls off.
	got = backupsToPrune(list, 2)
	if len(got) != 1 || got[0].ID != "restore-me.dump" {
		t.Fatalf("unprotected prune = %+v, want [restore-me.dump]", got)
	}
}

func TestBackupsToPruneRetainOneStillProtects(t *testing.T) {
	list := []BackupMeta{
		{ID: "safety.dump"},
		{ID: "only.dump"},
	}
	got := backupsToPrune(list, 1, "only.dump")
	if len(got) != 0 {
		t.Fatalf("retain=1 must not delete protected restore target: %+v", got)
	}
	got = backupsToPrune(list, 1)
	if len(got) != 1 || got[0].ID != "only.dump" {
		t.Fatalf("unprotected retain=1 = %+v", got)
	}
}

func TestBackupsToPruneKeepsNewest(t *testing.T) {
	list := []BackupMeta{
		{ID: "n0"}, {ID: "n1"}, {ID: "n2"}, {ID: "n3"},
	}
	got := backupsToPrune(list, 2)
	if len(got) != 2 || got[0].ID != "n2" || got[1].ID != "n3" {
		t.Fatalf("got %+v", got)
	}
}
