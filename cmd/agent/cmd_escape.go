package main

import (
	"bytes"
	"os/exec"
	"strings"
)

// buildCmdExeCmdLine assembles the literal command line for `cmd.exe /c <cmd>`.
//
// Kept in this build-tag-free file so Linux CI can assert its shape: the defect
// it fixes (Go's CRT-style `\"` escaping, which cmd.exe does not understand) is
// invisible on any platform where the tests actually run. See useRawCmdLine.
//
// Only argv[0] is quoted — the program path may contain spaces, while the
// remainder after /c must reach cmd.exe byte for byte.
func buildCmdExeCmdLine(exePath, command string) string {
	return `"` + strings.ReplaceAll(exePath, `"`, "") + `" /c ` + command
}

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
