//go:build windows

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// reapLeftoverDesktopWorkers terminates host --desktop-worker leftovers left
// behind when the previous service process died before stopWorker ran.
func reapLeftoverDesktopWorkers(selfExe string) {
	_ = selfExe
	out, err := exec.Command("wmic", "process", "where",
		"CommandLine like '%--desktop-worker%'",
		"get", "ProcessId").Output()
	if err != nil {
		return
	}
	myPid := os.Getpid()
	for _, f := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(f)
		if err != nil || pid <= 1 || pid == myPid {
			continue
		}
		p, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		slog.Info("回收残留桌面 worker", "pid", pid)
		_ = p.Kill()
	}
}
