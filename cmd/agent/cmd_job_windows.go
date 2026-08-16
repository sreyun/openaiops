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

// useRawCmdLine hands cmd.exe the command line VERBATIM instead of letting Go
// build it from argv.
//
// os/exec builds the Windows command line with syscall.EscapeArg, which quotes
// an argument containing spaces and escapes every embedded `"` as `\"`. That is
// the CRT convention — and cmd.exe does not implement it (Go's own os/exec docs
// call cmd.exe out as an exception). So the wrapper the agent prepends to every
// exec/playbook command,
//
//	set "PATH=…;%PATH%" & "…\chcp.com" 65001 >nul 2>nul & <command>
//
// reached cmd.exe as
//
//	set \"PATH=…;%PATH%\" & \"…\chcp.com\" 65001 >nul 2>nul & <command>
//
// where `set \"PATH=…` defines a variable literally named `\"PATH` and the chcp
// call fails on a token starting with `\"`. Both failures are swallowed (the
// chcp output is redirected to nul, and the `&` chaining is deliberate so a
// failed chcp cannot short-circuit the real command), so the PATH repair this
// code exists to perform has silently never happened on any Windows host —
// exactly the LocalSystem thin-PATH problem it was written to fix. Any user
// command carrying quotes was mangled the same way.
//
// cmd.exe /c takes the rest of the line raw, so passing the line ourselves is
// both the fix and the simplest form. The remainder deliberately does not start
// with a quote: when it does, cmd strips the first and last quote of the line.
func useRawCmdLine(cmd *exec.Cmd, exePath, command string) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = buildCmdExeCmdLine(exePath, command)
}
