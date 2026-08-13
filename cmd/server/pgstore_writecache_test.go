package main

import (
	"testing"

	"aiops-monitor/shared"
)

func TestPGWriteCacheOnlyRemembersAfterCommit(t *testing.T) {
	c := newPGWriteCache()
	raw := []byte(`{"a":1}`)

	if !c.isChanged("hosts/h1", raw) {
		t.Fatal("an unknown key must read as changed")
	}
	// isChanged must be side-effect free: a flush that fails between the check and
	// the commit would otherwise leave PG stuck on the old value forever.
	if !c.isChanged("hosts/h1", raw) {
		t.Fatal("isChanged must not record the value it inspected")
	}

	c.remember("hosts/h1", raw)
	if c.isChanged("hosts/h1", raw) {
		t.Fatal("remembered value must read as unchanged")
	}
	if !c.isChanged("hosts/h1", []byte(`{"a":2}`)) {
		t.Fatal("different content must read as changed")
	}

	c.forget("hosts/h1")
	if !c.isChanged("hosts/h1", raw) {
		t.Fatal("forgotten key must read as changed again")
	}
}

func TestPGWriteCacheMirrorDeleteSemantics(t *testing.T) {
	c := newPGWriteCache()
	if !c.needsSeed("hosts") {
		t.Fatal("a fresh cache must seed the id set from PG first")
	}
	// Rows that existed while the process was down must still be mirrored away —
	// that is what the old "DELETE 全表 + 重插" did implicitly.
	c.seed("hosts", []string{"h1", "h2", "h3"})
	if c.needsSeed("hosts") {
		t.Fatal("seeded table must not ask to seed again")
	}

	live := map[string]bool{"h1": true, "h3": true}
	missing := c.missingIDs("hosts", live)
	if len(missing) != 1 || missing[0] != "h2" {
		t.Fatalf("expected exactly h2 to be deleted, got %v", missing)
	}

	c.setIDs("hosts", live)
	if got := c.missingIDs("hosts", live); len(got) != 0 {
		t.Fatalf("after setIDs nothing should be pending deletion, got %v", got)
	}
}

func TestPGWriteCacheInvalidateTableIsPrefixScoped(t *testing.T) {
	c := newPGWriteCache()
	c.remember("hosts/h1", []byte("a"))
	c.remember("hosts-id/h1", []byte("b"))
	c.remember("tickets/1", []byte("c"))
	c.seed("hosts", []string{"h1"})

	c.invalidateTable("hosts")

	if !c.isChanged("hosts/h1", []byte("a")) {
		t.Fatal("invalidateTable must drop the table's own hashes")
	}
	if c.isChanged("tickets/1", []byte("c")) {
		t.Fatal("invalidateTable must not touch another table")
	}
	if !c.needsSeed("hosts") {
		t.Fatal("invalidateTable must force a re-seed of the id set")
	}
	// "hosts-id/" is a sibling namespace, not a child of "hosts/": saveHosts has to
	// invalidate it explicitly, and this pins that it is not swept up for free.
	if c.isChanged("hosts-id/h1", []byte("b")) {
		t.Fatal("invalidateTable(\"hosts\") must not silently clear the hosts-id namespace")
	}
	c.invalidateTable("hosts-id")
	if !c.isChanged("hosts-id/h1", []byte("b")) {
		t.Fatal("invalidateTable(\"hosts-id\") must clear the identity digests")
	}
}

// TestHostIdentityDigestIgnoresMetricChurn is the whole point of the fast/slow
// split: metric drift must not make the fast 15s flush rewrite a host row, or the
// content-hash dedup buys nothing on the single largest writer in the schema.
func TestHostIdentityDigestIgnoresMetricChurn(t *testing.T) {
	base := &Host{
		ID: "h1", Hostname: "web-01", OS: "linux", Platform: "Debian 12", Arch: "amd64",
		IP: "10.0.0.5", Kernel: "6.1.0", Category: "prod", AgentVersion: "0.19.68",
		ServerURL: "https://mon.example:8529", Fingerprint: "fp-abc",
		FirstSeen: 1786000000, LastSeen: 1786600000,
		Latest: &shared.Sample{Timestamp: 1786600000, Metrics: shared.Metrics{CPUPercent: 10}},
		Custom: map[string]float64{"queue_depth": 3},
	}
	want := string(hostIdentityDigest(base))

	churn := *base
	churn.LastSeen = base.LastSeen + 15
	churn.Latest = &shared.Sample{Timestamp: 1786600015, Metrics: shared.Metrics{CPUPercent: 91.7, MemPercent: 55}}
	churn.Custom = map[string]float64{"queue_depth": 9001}
	if got := string(hostIdentityDigest(&churn)); got != want {
		t.Fatalf("metric/LastSeen churn changed the identity digest:\n want %s\n  got %s", want, got)
	}

	for name, mutate := range map[string]func(h *Host){
		"hostname":      func(h *Host) { h.Hostname = "web-02" },
		"ip":            func(h *Host) { h.IP = "10.0.0.6" },
		"agent_version": func(h *Host) { h.AgentVersion = "0.19.69" },
		"category":      func(h *Host) { h.Category = "staging" },
		"fingerprint":   func(h *Host) { h.Fingerprint = "fp-xyz" },
		"platform":      func(h *Host) { h.Platform = "Debian 13" },
		"server_url":    func(h *Host) { h.ServerURL = "https://relay.lan:8529" },
	} {
		changed := *base
		mutate(&changed)
		if string(hostIdentityDigest(&changed)) == want {
			t.Errorf("%s change must be visible to the fast flush", name)
		}
	}

	if hostIdentityDigest(nil) != nil {
		t.Fatal("nil host must digest to nil")
	}
}
