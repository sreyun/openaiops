package main

import (
	"fmt"
	"testing"
	"time"
)

// Flooding the execution ring must not evict a pending_approval that still owns
// schedBusy. Before the fix, startExecution kept the newest 100 rows blindly;
// approve/reject then 404'd and the schedule never fired again until restart.
func TestPendingApprovalSurvivesExecRingTrim(t *testing.T) {
	s, _ := newTestServer(t)
	pb, err := s.playbooks.Upsert(Playbook{
		Name: "needs-approval",
		Steps: []PlaybookStep{{
			Name: "restart", Module: "service", Target: "all", TimeoutSec: 30,
			Args: map[string]string{"name": "nginx", "state": "restarted"},
		}},
		Schedule: &PlaybookSchedule{Enabled: true, Kind: "interval", IntervalMin: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := s.store.RegisterHost("h1", "n1", "fp-pending-trim")
	h.OS = "linux"
	h.LastSeen = time.Now().Unix()

	// Production path: dueSchedules marks schedBusy before fireScheduledPlaybook.
	s.playbooks.mu.Lock()
	s.playbooks.schedBusy[pb.ID] = true
	s.playbooks.mu.Unlock()
	s.fireScheduledPlaybook(pb)

	pendingID := int64(0)
	for _, e := range s.playbooks.ExecutionHistory() {
		if e.PlaybookID == pb.ID && e.Status == "pending_approval" {
			pendingID = e.ID
			break
		}
	}
	if pendingID == 0 {
		t.Fatal("expected pending_approval execution")
	}
	s.playbooks.mu.Lock()
	busy := s.playbooks.schedBusy[pb.ID]
	s.playbooks.mu.Unlock()
	if !busy {
		t.Fatal("schedBusy must stay set while pending_approval awaits human gate")
	}

	filler := Playbook{
		Name:  "filler",
		Steps: []PlaybookStep{{Name: "noop", Module: "gather_facts", Target: "all", TimeoutSec: 30}},
	}
	for i := 0; i < playbookExecRingCap+20; i++ {
		filler.Name = fmt.Sprintf("filler-%d", i)
		fp, err := s.playbooks.Upsert(filler)
		if err != nil {
			t.Fatal(err)
		}
		exec := s.playbooks.StartExecution(fp, "tester", []*Host{h})
		s.playbooks.FinishExecution(exec.ID, "completed")
	}

	got, ok := s.playbooks.GetExecution(pendingID)
	if !ok {
		t.Fatalf("pending_approval id=%d was trimmed out of the in-memory ring", pendingID)
	}
	if got.Status != "pending_approval" {
		t.Fatalf("status=%q want pending_approval", got.Status)
	}
	s.playbooks.mu.Lock()
	busy = s.playbooks.schedBusy[pb.ID]
	s.playbooks.mu.Unlock()
	if !busy {
		t.Fatal("schedBusy cleared while pending_approval still unresolved")
	}

	s.playbooks.FinishExecution(pendingID, "rejected")
	s.playbooks.clearSchedBusy(pb.ID)
	s.playbooks.mu.Lock()
	busy = s.playbooks.schedBusy[pb.ID]
	n := len(s.playbooks.executions)
	s.playbooks.mu.Unlock()
	if busy {
		t.Fatal("schedBusy must clear after reject")
	}
	if n > playbookExecRingCap+5 {
		t.Fatalf("ring grew without bound: %d", n)
	}
}

func TestTrimExecutionsKeepsRunningOverOldestCompleted(t *testing.T) {
	pm := newPlaybookManager(nil)
	for i := 0; i < playbookExecRingCap; i++ {
		pm.executions = append(pm.executions, PlaybookExecution{
			ID: int64(i + 1), Status: "completed",
		})
	}
	pm.executions = append(pm.executions, PlaybookExecution{
		ID: 9999, Status: "running", PlaybookID: "live",
	})
	pm.trimExecutionsLocked()
	if _, ok := pm.GetExecution(9999); !ok {
		t.Fatal("running execution must survive trim")
	}
	if len(pm.executions) > playbookExecRingCap {
		t.Fatalf("len=%d want <= %d", len(pm.executions), playbookExecRingCap)
	}
}
