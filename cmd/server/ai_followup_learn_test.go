package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// 客观回验的第一段：由 AI 结论建出的工单必须记住自己出自哪条 run，否则工单被解决时
// 没有任何东西能把「这条结论管用」回流给记忆库。
func TestAIFollowupTicketRecordsRunProvenance(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.persistAIRun(AIRun{ID: "run_prov_1", Kind: "sreyun", Task: "sreyun",
		Answer: "根因：归档未清理。处置：清理并加保留策略。", Actor: "alice"})

	rr := followupReq(t, srv, map[string]any{
		"run_id": "run_prov_1", "action": aiActCreateTicket, "title": "清理归档",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create_ticket: got %d, want 200 (body: %s)", rr.Code, rr.Body)
	}
	var out struct {
		Ticket Ticket `json:"ticket"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rr.Body)
	}
	if out.Ticket.AIRunID != "run_prov_1" {
		t.Fatalf("returned ticket ai_run_id = %q, want run_prov_1", out.Ticket.AIRunID)
	}
	stored, ok := srv.tickets.Get(out.Ticket.ID)
	if !ok {
		t.Fatal("ticket not found in the store")
	}
	if stored.AIRunID != "run_prov_1" {
		t.Fatalf("stored ticket ai_run_id = %q, want run_prov_1 (SLA finalize must not drop it)", stored.AIRunID)
	}
	// 把结论转成工单就是最强的「采纳」表态，不该再要求人另外点一次「有用」。
	snap := srv.aiStats.snapshot()
	if n, _ := snap["feedback_applied"].(int64); n != 1 {
		t.Fatalf("feedback_applied = %v, want 1 — 闭环动作必须记成一次采纳", snap["feedback_applied"])
	}
}

// Provenance is what flips memories to verified, so a client must not be able to
// claim it — otherwise anyone could get an arbitrary AI answer marked "验证过".
func TestTicketCreateDropsClientClaimedAIRun(t *testing.T) {
	m := &ticketManager{}
	tk, err := m.Create(Ticket{Title: "伪造来源", AIRunID: "run_i_did_not_earn"}, "mallory")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tk.AIRunID != "" {
		t.Fatalf("client-claimed ai_run_id survived Create: %q", tk.AIRunID)
	}
	stored, ok := m.Get(tk.ID)
	if !ok || stored.AIRunID != "" {
		t.Fatalf("client-claimed ai_run_id reached the store: %+v", stored)
	}
}

// 记忆写入需要 PG + 嵌入模型，测试环境两者都没有——但这两个回调挂在告警回验和工单
// 更新的主路径上，无论如何都不能 panic 或阻塞。
func TestLearningHooksAreSafeWithoutPG(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.pg = nil
	srv.learnFromAIFollowupTicket(Ticket{ID: 1, Title: "t", AIRunID: "run_x"}, "已解决")
	srv.learnFromAIFollowupTicket(Ticket{ID: 2, Title: "t"}, "已解决") // 无来源：直接跳过
	srv.learnFromRemediationVerify(RemediationRun{ID: 1, Verify: "cleared", PlaybookID: "pb1"})
	srv.learnFromRemediationVerify(RemediationRun{ID: 2, Verify: "still_firing", PlaybookID: "pb1"})
}

// 回验结论必须真的送达学习闭环。这是「这条自愈到底有没有用」唯一不靠人点赞的信号，
// 少一次回调，记忆库就永远停在「执行成功」这个过程指标上。
func TestRemediationVerifyReachesLearningLoop(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stillFiring bool
		want        string
	}{
		{"cleared", false, "cleared"},
		{"still firing", true, "still_firing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRemediationManager(nil)
			m.verifyAfter = time.Millisecond
			m.alertActive = func(string) bool { return tc.stillFiring }

			got := make(chan RemediationRun, 4)
			m.onVerify = func(run RemediationRun) { got <- run }

			m.mu.Lock()
			m.runs = append(m.runs, RemediationRun{
				ID: 1, Status: "running", AlertKey: "h1/cpu/", PlaybookID: "pb1",
				PlaybookName: "restart-svc", HostID: "h1", Hostname: "web-01", IncidentID: 7,
			})
			m.nextID = 2
			m.mu.Unlock()

			m.finish(1, true, "", "")

			select {
			case run := <-got:
				if run.Verify != tc.want {
					t.Fatalf("onVerify verdict = %q, want %q", run.Verify, tc.want)
				}
				if run.PlaybookID != "pb1" || run.IncidentID != 7 {
					t.Fatalf("onVerify must carry the run's learning keys, got %+v", run)
				}
				if run.VerifiedAt == 0 {
					t.Fatal("onVerify fired before VerifiedAt was stamped")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("onVerify never fired; the verify verdict never reaches the learning loop")
			}
		})
	}
}

// 人工提案没有告警可回看，也就没有客观结论——不能拿它去强化任何记忆。
func TestRemediationVerifySkipsLearningForManualProposals(t *testing.T) {
	m := newRemediationManager(nil)
	m.verifyAfter = time.Millisecond
	m.alertActive = func(string) bool { return false }
	fired := make(chan RemediationRun, 1)
	m.onVerify = func(run RemediationRun) { fired <- run }

	m.mu.Lock()
	m.runs = append(m.runs, RemediationRun{ID: 1, Status: "running", AlertKey: "proposal/42", Hostname: "web-01"})
	m.mu.Unlock()

	m.finish(1, true, "", "")
	select {
	case run := <-fired:
		t.Fatalf("manual proposal must not feed the learning loop, got %+v", run)
	case <-time.After(120 * time.Millisecond):
	}
}
