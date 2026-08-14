package main

import (
	"strings"
	"testing"
)

func TestCanonicalLinuxUnitNameInUpdateScripts(t *testing.T) {
	sh := buildLegacyAgentUpdateCommand("linux", "http://x:8529", "aiops-agent-linux-amd64", testPinSHA, false)
	if !strings.Contains(sh, "systemctl restart aiops-agent") {
		t.Fatal("linux update must prefer aiops-agent")
	}
	// Legacy fallback OK, but must not be the only restart target.
	idxNew := strings.Index(sh, "systemctl restart aiops-agent")
	idxOld := strings.Index(sh, "systemctl restart aiops-monitor-agent")
	if idxNew < 0 {
		t.Fatal("missing aiops-agent restart")
	}
	if idxOld >= 0 && idxOld < idxNew {
		t.Fatal("legacy unit must not be preferred over aiops-agent")
	}
}

func TestPlaybookPacksEmbedded(t *testing.T) {
	list, err := listEmbeddedPlaybookPacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 2 {
		t.Fatalf("expected >=2 packs, got %v", list)
	}
	seen := map[string]bool{}
	for _, p := range list {
		seen[p.ID] = true
		if p.Count < 1 {
			t.Fatalf("pack %s empty", p.ID)
		}
	}
	for _, id := range []string{"selfheal", "inspect"} {
		if !seen[id] {
			t.Fatalf("missing pack %s", id)
		}
		pack, err := loadEmbeddedPlaybookPack(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(pack.Playbooks) == 0 {
			t.Fatalf("%s has no playbooks", id)
		}
	}
}

func TestAgentUpdatePendingVerifyLifecycle(t *testing.T) {
	m := newAgentUpdateManager()
	job := &agentUpdateJob{
		ID: "j1", Status: "running",
		Hosts: []*agentUpdateHostResult{
			{HostID: "h1", Status: "pending_verify"},
		},
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	m.setHostResult(job.Hosts[0], "pending_verify", "module", "awaiting version ack")
	if job.Hosts[0].Status != "pending_verify" {
		t.Fatalf("status=%s", job.Hosts[0].Status)
	}
	m.setHostResult(job.Hosts[0], "success", "module", "version matched")
	if job.Hosts[0].Status != "success" {
		t.Fatalf("after ack status=%s", job.Hosts[0].Status)
	}
}
