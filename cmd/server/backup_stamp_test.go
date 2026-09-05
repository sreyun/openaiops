package main

import (
	"os"
	"strings"
	"sync"
	"testing"
)

func TestUniqueBackupStampDistinctUnderBurst(t *testing.T) {
	const n = 200
	seen := make(map[string]bool, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			backupCreateMu.Lock()
			s := uniqueBackupStamp()
			backupCreateMu.Unlock()
			mu.Lock()
			defer mu.Unlock()
			if seen[s] {
				t.Errorf("duplicate stamp %q", s)
			}
			seen[s] = true
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("got %d unique stamps, want %d", len(seen), n)
	}
	for s := range seen {
		if !strings.Contains(s, "-") || len(s) < len("20060102-150405-000000") {
			t.Fatalf("unexpected stamp shape %q", s)
		}
	}
}

func TestOpenExclusiveBackupFileRejectsCollision(t *testing.T) {
	dir := t.TempDir()
	id1, path1, err := reserveBackupArtifact(dir, backupPrefixVM, ".native.gz")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" || path1 == "" {
		t.Fatal("empty reservation")
	}
	// Force a same-name collision by calling OpenFile with O_EXCL on the reserved path.
	if _, err := os.OpenFile(path1, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); !os.IsExist(err) {
		t.Fatalf("second exclusive open: want IsExist, got %v", err)
	}
	id2, path2, err := reserveBackupArtifact(dir, backupPrefixVM, ".native.gz")
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 || path2 == path1 {
		t.Fatalf("second reservation reused id %q", id2)
	}
}

func TestBackupKindOfAcceptsStampSuffix(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"aiops-pg-20260905-110405-000001.dump", "postgres"},
		{"aiops-vm-20260905-110405-000002.native.gz", "vm"},
		{"aiops-rec-20260905-110405-000003.tar.gz", "recordings"},
		{"aiops-pg-20260905-110405.dump", "postgres"}, // legacy second-precision
	}
	for _, c := range cases {
		if got := backupKindOf(c.id); got != c.want {
			t.Fatalf("%s: kind=%s want %s", c.id, got, c.want)
		}
	}
}
