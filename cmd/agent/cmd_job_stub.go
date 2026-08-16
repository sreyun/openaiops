//go:build !windows

package main

import "os/exec"

func detachFromServiceJob(*exec.Cmd) {}

// useRawCmdLine is a no-op off Windows: /bin/sh -c takes the command as a
// normal argv element and needs no hand-built command line.
func useRawCmdLine(*exec.Cmd, string, string) {}
