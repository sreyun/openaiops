package main

import (
	"strings"
	"testing"
)

func TestBackfillClockSkewIgnoresAbsurdOffsets(t *testing.T) {
	const now = int64(1_800_000_000)
	if got := backfillClockSkew(now-30, now); got != 30 {
		t.Fatalf("normal skew: got %d want 30", got)
	}
	if got := backfillClockSkew(now+45, now); got != -45 {
		t.Fatalf("agent ahead: got %d want -45", got)
	}
	if got := backfillClockSkew(0, now); got != 0 {
		t.Fatalf("missing reported_at must mean no correction, got %d", got)
	}
	// A host whose clock is a year off must not drag its samples a year across the
	// timeline — fall back to no correction.
	if got := backfillClockSkew(now-365*24*3600, now); got != 0 {
		t.Fatalf("absurd skew must degrade to 0, got %d", got)
	}
}

func TestBackfillTimestampRejectsFutureAndTooOld(t *testing.T) {
	const now = int64(1_800_000_000)
	if ts, ok := backfillTimestamp(now-600, 0, now); !ok || ts != now-600 {
		t.Fatalf("plain in-window sample rejected: ts=%d ok=%v", ts, ok)
	}
	// Skew is applied.
	if ts, ok := backfillTimestamp(now-600, 30, now); !ok || ts != now-570 {
		t.Fatalf("skew not applied: ts=%d ok=%v", ts, ok)
	}
	if _, ok := backfillTimestamp(now+3600, 0, now); ok {
		t.Fatal("future timestamps must be rejected")
	}
	// Small forward tolerance is fine (collect→deliver latency + rounding).
	if _, ok := backfillTimestamp(now+30, 0, now); !ok {
		t.Fatal("30s of forward tolerance should be accepted")
	}
	if _, ok := backfillTimestamp(now-agentBackfillMaxAgeSec-60, 0, now); ok {
		t.Fatal("samples older than the retention window must be rejected")
	}
	if _, ok := backfillTimestamp(0, 0, now); ok {
		t.Fatal("zero timestamp must be rejected")
	}
}

// Agent 侧缓冲上限（7 天）与服务端接收窗口必须一致：服务端收窄会让 Agent 辛苦攒下的
// 老数据在入口被静默丢弃，放宽则等于允许往任意历史位置写点。
func TestBackfillRetentionWindowIsSevenDays(t *testing.T) {
	if agentBackfillMaxAgeSec != 7*24*3600 {
		t.Fatalf("server backfill window = %ds, agent buffers 7 days", agentBackfillMaxAgeSec)
	}
}

// The rescue bootstrap must NOT put the Scheduled Task first.
//
// Task Scheduler's defaults (DisallowStartIfOnBatteries / StopIfGoingOnBatteries)
// silently refuse to run — or kill mid-swap — on any battery-powered host, and
// the -Settings override that fixes that does not fit in the cmd.exe budget this
// path is bounded by. Win32_Process.Create has no such policy layer and already
// escapes the agent's Job Object, so it has to be the first choice; the task and
// cmd start remain as fallbacks for hosts where WMI is locked down.
func TestLegacyWindowsBootstrapPrefersWMIOverScheduledTask(t *testing.T) {
	cmd := legacyWindowsAgentUpdateScript("https://panel.example", "aiops-agent.exe",
		strings.Repeat("ab", 32))
	ps := decodeLegacyWindowsPS(t, cmd)
	wmi := strings.Index(ps, "Win32_Process")
	task := strings.Index(ps, "/Create /TN")
	cmdStart := strings.Index(ps, "Start-Process $Cm")
	if wmi < 0 || task < 0 || cmdStart < 0 {
		t.Fatalf("bootstrap lost one of its three launchers:\n%s", ps)
	}
	if wmi > task {
		t.Fatal("Win32_Process.Create must be tried BEFORE the scheduled task " +
			"(task scheduler refuses to run on battery, silently)")
	}
	if task > cmdStart {
		t.Fatal("cmd start should stay the last resort")
	}
}
