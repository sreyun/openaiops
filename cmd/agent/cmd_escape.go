package main

import (
	"bytes"
	"os/exec"
)

// runCmdEscaped is CombinedOutput plus the two isolation steps that keep
// playbook / exec children from dying with the Agent:
//
//   - Linux: move the process tree out of the Agent systemd cgroup
//   - Windows: CREATE_BREAKAWAY_FROM_JOB so SCM job teardown cannot reap them
func runCmdEscaped(cmd *exec.Cmd) ([]byte, error) {
	var buf bytes.Buffer
	if cmd.Stdout == nil {
		cmd.Stdout = &buf
	}
	if cmd.Stderr == nil {
		cmd.Stderr = &buf
	}
	detachFromServiceJob(cmd)
	if err := cmd.Start(); err != nil {
		return buf.Bytes(), err
	}
	if cmd.Process != nil {
		escapeAgentCgroupTree(cmd.Process.Pid)
	}
	err := cmd.Wait()
	return buf.Bytes(), err
}
