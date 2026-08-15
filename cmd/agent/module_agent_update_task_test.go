package main

import (
	"strings"
	"testing"
)

// The three Scheduled Task defaults below are the reason Windows self-update can
// fail with NOTHING in any log: the task registers, Start-ScheduledTask returns
// success, and the helper is simply never run (or is killed mid-swap).
//
//	DisallowStartIfOnBatteries=True → 电池供电的主机（Win10/11 笔记本、平板、
//	                                  暴露电池的虚拟机）任务直接不运行
//	StopIfGoingOnBatteries=True     → 升级途中掉电，助手被杀在停服务与重启之间
//	MultipleInstances=IgnoreNew     → 上一轮吊死的实例还在，之后每次 Start 都空转
func TestWindowsUpdateTaskOverridesDangerousSchedulerDefaults(t *testing.T) {
	script := buildWindowsUpdateTaskScript(windowsSelfUpdateTaskName,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		`-NoProfile -Command "& 'C:\ProgramData\aiops-agent-update\helper.ps1'"`)

	for _, need := range []string{
		"-AllowStartIfOnBatteries",
		"-DontStopIfGoingOnBatteries",
		"-StartWhenAvailable",
		"-MultipleInstances StopExisting",
		"-ExecutionTimeLimit",
		"-Settings $settings",
	} {
		if !strings.Contains(script, need) {
			t.Fatalf("scheduled task script missing %q:\n%s", need, script)
		}
	}
	if strings.Count(script, "-Settings $settings") < 3 {
		t.Fatal("every Register-ScheduledTask call must pass -Settings, including the fallbacks")
	}
}

// A stale RUNNING instance is what makes the IgnoreNew default permanent, so the
// script has to end it before re-registering. Unregister alone is not enough.
func TestWindowsUpdateTaskEndsStaleInstanceBeforeRegistering(t *testing.T) {
	script := buildWindowsUpdateTaskScript(windowsSelfUpdateTaskName, "powershell.exe", "-NoProfile")
	end := strings.Index(script, "/End")
	reg := strings.Index(script, "Register-ScheduledTask")
	if end < 0 {
		t.Fatal("script never ends a stale task instance")
	}
	if reg < 0 || end > reg {
		t.Fatal("/End must run BEFORE Register-ScheduledTask")
	}
}

// An unelevated task cannot stop the service or write into Program Files, so the
// non-SYSTEM fallback must still ask for Highest.
func TestWindowsUpdateTaskFallbackStaysElevated(t *testing.T) {
	script := buildWindowsUpdateTaskScript(windowsSelfUpdateTaskName, "powershell.exe", "-NoProfile")
	if strings.Count(script, "-RunLevel Highest") < 2 {
		t.Fatalf("current-user fallback must also request -RunLevel Highest:\n%s", script)
	}
	if !strings.Contains(script, "NT AUTHORITY\\SYSTEM") {
		t.Fatal("should retry the SYSTEM principal with its fully-qualified name")
	}
}

// The helper script and the task script must agree on the task name, or the
// helper's own cleanup (schtasks /Delete) silently leaves the task behind.
func TestWindowsSelfUpdateTaskNameIsConsistent(t *testing.T) {
	helper := buildWindowsUpdateHelperScript(`C:\a\aiops-agent.exe`, `C:\a\.new.exe`, `C:\a\config.yaml`,
		`C:\log.txt`, `C:\a\r.result`, `C:\w\r.result`)
	if !strings.Contains(helper, windowsSelfUpdateTaskName) {
		t.Fatalf("helper does not reference task %q", windowsSelfUpdateTaskName)
	}
}
