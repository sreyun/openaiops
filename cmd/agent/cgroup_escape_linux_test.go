//go:build linux

package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestEscapeAgentCgroupRejectsInit(t *testing.T) {
	if escapeAgentCgroup(1) || escapeAgentCgroup(0) || escapeAgentCgroup(-1) {
		t.Fatal("must not touch pid 1 / invalid pids")
	}
}

func TestListChildPIDsFindsSpawnedChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, c := range listChildPIDs(os.Getpid()) {
			if c == cmd.Process.Pid {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("did not observe child pid %d under %d", cmd.Process.Pid, os.Getpid())
}
