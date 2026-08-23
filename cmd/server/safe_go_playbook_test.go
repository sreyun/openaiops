package main

import (
	"testing"
	"time"
)

// v0.20.31 把剧本执行换成 safeGo 之后，panic 不再杀进程——但 clearSchedBusy
// 若写在 runPlaybookExecution 后面就永远执行不到，schedBusy 会卡住，后续 tick
// 永久跳过这本剧本。defer 清 busy + 收尾 running 才是隔离后的正确收尾。
func TestPlaybookPanicClearsSchedBusyAndFailsRunning(t *testing.T) {
	s, _ := newTestServer(t)
	pb, err := s.playbooks.Upsert(Playbook{
		Name:  "panic-sched",
		Steps: []PlaybookStep{{Name: "noop", Module: "gather_facts", Target: "all", TimeoutSec: 5}},
		Schedule: &PlaybookSchedule{Enabled: true, Kind: "interval", IntervalMin: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := s.store.RegisterHost("h1", "n1", "fp-h1")
	h.OS = "linux"
	h.LastSeen = time.Now().Unix()

	hosts := []*Host{h}
	exec := s.playbooks.StartScheduledExecution(pb, "tester", hosts)
	s.playbooks.mu.Lock()
	s.playbooks.schedBusy[pb.ID] = true
	s.playbooks.mu.Unlock()

	done := make(chan struct{})
	safeGo("playbook-exec-test", func() {
		defer close(done)
		defer s.playbooks.clearSchedBusy(pb.ID)
		defer s.finishPlaybookAfterPanic(exec.ID)
		panic("simulated playbook crash")
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("safeGo never finished")
	}

	s.playbooks.mu.Lock()
	busy := s.playbooks.schedBusy[pb.ID]
	s.playbooks.mu.Unlock()
	if busy {
		t.Fatal("schedBusy must be cleared after panic, otherwise schedule is stuck forever")
	}
	got, ok := s.playbooks.GetExecution(exec.ID)
	if !ok {
		t.Fatal("execution missing")
	}
	if got.Status != "failed" {
		t.Fatalf("status=%q want failed (must not stay running after panic)", got.Status)
	}
}

func TestFinishPlaybookAfterPanicNoopWhenAlreadyTerminal(t *testing.T) {
	s, _ := newTestServer(t)
	pb, err := s.playbooks.Upsert(Playbook{
		Name:  "done",
		Steps: []PlaybookStep{{Name: "noop", Module: "gather_facts", Target: "all", TimeoutSec: 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := s.store.RegisterHost("h1", "n1", "fp-h1")
	exec := s.playbooks.StartExecution(pb, "tester", []*Host{h})
	s.playbooks.FinishExecution(exec.ID, "completed")
	s.finishPlaybookAfterPanic(exec.ID)
	got, _ := s.playbooks.GetExecution(exec.ID)
	if got.Status != "completed" {
		t.Fatalf("terminal status must stay sticky, got %q", got.Status)
	}
}

// 容器终端审计 Host 是 "display · 容器名"；授权过滤必须认派生标签，否则 scoped
// 用户永远看不到容器会话里敲过的命令。
func TestTerminalHostAllowedMatchesContainerLabel(t *testing.T) {
	allow := []string{"web (10.0.0.1)"}
	if !terminalHostAllowed("web (10.0.0.1)", allow) {
		t.Fatal("exact host label must match")
	}
	if !terminalHostAllowed("web (10.0.0.1) · nginx", allow) {
		t.Fatal("container session label must match allowed host")
	}
	if terminalHostAllowed("db (10.0.0.2)", allow) {
		t.Fatal("out-of-scope host must not match")
	}
	if terminalHostAllowed("web (10.0.0.1)-extra", allow) {
		t.Fatal("suffix without ' · ' separator must not match")
	}
}
