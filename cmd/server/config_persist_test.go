package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent mutators used to unlock-then-save; without persistMu a stale
// snapshot can clobber a newer mutation on disk/PG.
func TestConfigStoreConcurrentSavePreservesMutations(t *testing.T) {
	cs := newTestConfigStore(t)
	const rounds = 40
	var wg sync.WaitGroup
	errCh := make(chan error, rounds*2)
	for i := 0; i < rounds; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := cs.UpsertPlaybook(Playbook{ID: "pb-race", Name: fmt.Sprintf("pb-%d", i)})
			errCh <- err
		}()
		go func() {
			defer wg.Done()
			errCh <- cs.CreateUser(fmt.Sprintf("raceuser-%d", i), "password12345", "Race", "", RoleViewer)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent mutation: %v", err)
		}
	}

	reloaded, err := NewConfigStore(cs.path, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	foundPB := false
	for _, p := range reloaded.Playbooks() {
		if p.ID == "pb-race" {
			foundPB = true
			break
		}
	}
	if !foundPB {
		t.Fatal("playbook lost after concurrent saves — stale snapshot clobber")
	}
	users := 0
	reloaded.mu.RLock()
	for _, u := range reloaded.cfg.Users {
		if len(u.Username) >= 9 && u.Username[:9] == "raceuser-" {
			users++
		}
	}
	reloaded.mu.RUnlock()
	if users != rounds {
		t.Fatalf("users lost after concurrent saves: got %d want %d", users, rounds)
	}
}

func TestNewConfigStoreRefusesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server_config.json")
	if err := os.WriteFile(path, []byte(`{"thresholds":`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewConfigStore(path, nil)
	if err == nil {
		t.Fatal("expected error for corrupt config, got nil (would wipe with defaults)")
	}
	// Live file must remain untouched (no factory-default overwrite).
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"thresholds":` {
		t.Fatalf("corrupt config was overwritten: %q", b)
	}
}

func TestNewConfigStoreRefusesEmptyTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server_config.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewConfigStore(path, nil)
	if err == nil {
		t.Fatal("expected error for empty truncated config")
	}
}

func TestWriteConfigFileAtomicReplacesLive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server_config.json")
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := []byte("{\n  \"ok\": true\n}")
	if err := writeConfigFileAtomic(path, payload); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(payload) {
		t.Fatalf("got %q want %q", b, payload)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config mode too open: %v", fi.Mode())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "server_config.json" {
			t.Fatalf("unexpected leftover file %q", e.Name())
		}
	}
}
