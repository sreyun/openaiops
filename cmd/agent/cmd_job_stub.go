//go:build !windows

package main

import "os/exec"

func detachFromServiceJob(*exec.Cmd) {}
