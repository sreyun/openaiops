package main

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestRunCmdEscapedEcho(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "echo ok")
	} else {
		cmd = exec.Command("echo", "ok")
	}
	out, err := runCmdEscaped(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("got %q", out)
	}
}
