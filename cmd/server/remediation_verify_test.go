package main

import (
	"testing"
	"time"
)

// TestRemediationVerifiesTheAlertActuallyCleared pins the observation half of the
// closed loop.
//
// 「剧本退出码 0」和「问题解决了」是两个不同的断言。此前 finish() 只看前者就把 run
// 标成 success 并推送「自动修复成功」，于是重启一个起来就挂的服务、清一块马上又满的
// 磁盘，都会被记成成功——运维收到假的成功通知不再盯着，冷却窗还被占满。
func TestRemediationVerifiesTheAlertActuallyCleared(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stillFiring bool
		wantVerify  string
		wantNotify  bool // a "not fixed" escalation must be pushed
	}{
		{"alert cleared", false, "cleared", false},
		{"alert still firing", true, "still_firing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRemediationManager(nil)
			m.verifyAfter = time.Millisecond
			m.alertActive = func(string) bool { return tc.stillFiring }

			notifies := make(chan string, 8)
			m.onNotify = func(level, title, _ string, _ int64) { notifies <- level + "|" + title }

			m.mu.Lock()
			m.runs = append(m.runs, RemediationRun{
				ID: 1, Status: "running", AlertKey: "h1/cpu/", PlaybookName: "restart-svc",
				HostID: "h1", Hostname: "web-01",
			})
			m.nextID = 2
			m.mu.Unlock()

			m.finish(1, true, "", "")

			deadline := time.Now().Add(3 * time.Second)
			var got RemediationRun
			for time.Now().Before(deadline) {
				m.mu.Lock()
				if r := m.findRun(1); r != nil {
					got = *r
				}
				m.mu.Unlock()
				if got.Verify == tc.wantVerify {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if got.Verify != tc.wantVerify {
				t.Fatalf("verify = %q, want %q", got.Verify, tc.wantVerify)
			}
			if got.VerifiedAt == 0 {
				t.Fatal("VerifiedAt not stamped")
			}
			// The playbook itself did run, so the run stays "success" either way —
			// the verdict lives in Verify, not by rewriting execution history.
			if got.Status != "success" {
				t.Fatalf("status = %q, want success", got.Status)
			}

			escalated := false
			for len(notifies) > 0 {
				msg := <-notifies
				if msg[:8] == "critical" {
					escalated = true
				}
			}
			if escalated != tc.wantNotify {
				t.Fatalf("escalation notify = %v, want %v", escalated, tc.wantNotify)
			}
		})
	}
}

// A manual proposal has no alert behind it, so there is nothing to verify — and
// scheduleVerify must not leave it stuck in "pending" forever.
func TestRemediationSkipsVerifyForManualProposals(t *testing.T) {
	m := newRemediationManager(nil)
	m.verifyAfter = time.Millisecond
	m.alertActive = func(string) bool { return true }
	m.mu.Lock()
	m.runs = append(m.runs, RemediationRun{ID: 1, Status: "running", AlertKey: "proposal/42", Hostname: "web-01"})
	m.mu.Unlock()

	m.finish(1, true, "", "")
	time.Sleep(50 * time.Millisecond)

	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.findRun(1); r == nil || r.Verify != "" {
		t.Fatalf("manual proposal must carry no verify verdict, got %q", r.Verify)
	}
}
