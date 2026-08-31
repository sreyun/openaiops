package main

import (
	"database/sql"
	"testing"
)

// Empty in-memory host set + write-cache mirror-delete would DELETE every PG host
// row after a failed BindPG loadHosts. Pin the cache semantics and BindPG error.
func TestWriteCacheEmptyLiveWouldDeleteAllSeededHosts(t *testing.T) {
	c := newPGWriteCache()
	c.seed("hosts", []string{"h1", "h2", "h3"})
	if got := c.missingIDs("hosts", map[string]bool{}); len(got) != 3 {
		t.Fatalf("empty live set must mark all seeded ids for deletion, got %v", got)
	}
}

func TestBindPGReturnsErrorWhenLoadHostsFails(t *testing.T) {
	db, err := sql.Open("postgres", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1")
	if err != nil {
		t.Skipf("open postgres driver: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_ = db.Close() // force subsequent Query to fail
	pg := &pgStore{db: db, wc: newPGWriteCache()}
	store := NewStore()
	if err := store.BindPG(pg); err == nil {
		t.Fatal("BindPG must surface loadHosts failure (empty map + later flush would wipe PG)")
	}
	if n := len(store.ListHosts()); n != 0 {
		t.Fatalf("failed BindPG must not leave hosts loaded, got %d", n)
	}
}
