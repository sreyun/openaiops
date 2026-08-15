//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detachFromServiceJob lets a child survive SCM / service Job Object teardown.
// CREATE_BREAKAWAY_FROM_JOB is the same flag the self-update helper already uses.
func detachFromServiceJob(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createBreakawayJob | createNewProcessGroup
}
