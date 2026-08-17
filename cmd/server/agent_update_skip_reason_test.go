package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 「为什么这台没升级」这张表是操作台上唯一能回答该问题的地方，而它最坏的失效方式不是
// 空着，是**说了一句过时的话**：主机早就升完了、或者早就离线了，表里还写着当初那条
// pending_job。下面几个用例分别钉住那几条出口。

// putTestHost seeds the in-memory Store directly: the auto-update gate only ever
// reads hosts, and going through the register/report handlers would drag a whole
// fingerprint handshake into a test about skip reasons.
func putTestHost(srv *Server, h *Host) {
	srv.store.mu.Lock()
	srv.store.hosts[h.ID] = h
	srv.store.mu.Unlock()
}

// writeFakeAgentDist drops a Windows artifact into the server's dist dir so the
// gate gets past no_artifact and reaches the check under test.
//
// 内容必须带上 appVersion：真实产物是用 `-X main.appVersion=<ver>` 构建的，版本串就在
// 二进制里，而 stale_artifact 闸门查的正是这个（见 agentDistCarriesVersion）。写 "fake"
// 会撞在那道闸门上，测试就再也走不到它真正想测的检查。
func writeFakeAgentDist(t *testing.T, srv *Server) {
	t.Helper()
	writeFakeAgentDistVersion(t, srv, appVersion)
}

func writeFakeAgentDistVersion(t *testing.T, srv *Server, ver string) {
	t.Helper()
	if srv.distDir == "" {
		t.Fatal("test server has no dist dir")
	}
	body := []byte("MZ fake agent binary built as " + ver + "\n")
	if err := os.WriteFile(filepath.Join(srv.distDir, "aiops-agent.exe"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPendingJobSkipDetailNamesTheJob(t *testing.T) {
	srv, _ := newTestServer(t)
	job := &agentUpdateJob{
		ID: "au-42", Status: "running", TargetVer: "v1.2.3", CreatedAt: time.Now().Unix() - 90,
		Hosts: []*agentUpdateHostResult{{
			HostID: "h1", Status: "pending_verify", Method: "module",
			Updated: time.Now().Unix() - 90,
		}},
	}
	srv.agentUpdates.put(job)

	busy, detail := srv.hostPendingAgentUpdate("h1")
	if !busy {
		t.Fatal("host inside a live job must read as busy")
	}
	for _, want := range []string{"au-42", "pending_verify", "module", "v1.2.3", "1m30s"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("skip detail %q missing %q — 操作台上又只剩一句「有任务在进行中」", detail, want)
		}
	}

	// A finished job must stop blocking: otherwise the host never gets a retry.
	srv.agentUpdates.setJobStatus(job, "done", true)
	if busy, _ := srv.hostPendingAgentUpdate("h1"); busy {
		t.Fatal("a done job must not keep blocking auto-update")
	}
}

// 需要人动手的理由（缺安装包）必须压过会自愈的理由（有任务在跑），否则一台永远升不上去
// 的机器每一轮都只会显示 pending_job，真正该修的那条一次都不出现。
func TestDurableSkipReasonBeatsTransientPendingJob(t *testing.T) {
	srv, _ := newTestServer(t)
	old := appVersion
	appVersion = "v9.9.9"
	defer func() { appVersion = old }()
	if err := srv.cfg.SetAgentAutoUpdatePolicy(true, "", nil, nil); err != nil {
		t.Fatal(err)
	}

	h := &Host{ID: "h1", Hostname: "server11", OS: "windows", Arch: "amd64",
		AgentVersion: "v1.0.0", LastSeen: time.Now().Unix()}
	putTestHost(srv, h)
	srv.agentUpdates.put(&agentUpdateJob{
		ID: "au-7", Status: "running", TargetVer: appVersion, CreatedAt: time.Now().Unix(),
		Hosts: []*agentUpdateHostResult{{HostID: "h1", Status: "pending_verify", Method: "module"}},
	})

	enq, reason, detail := srv.decideAutoUpdate(h)
	if enq {
		t.Fatal("no artifact on disk — must not enqueue")
	}
	if reason != "no_artifact" {
		t.Fatalf("reason=%q detail=%q，期望 no_artifact（缺包才是要人处理的那条）", reason, detail)
	}
}

// 自动升级是一台主机一个 job，槽位很快就被填满。淘汰**活着的** job 不只是丢历史：那台
// 主机的跳过原因会从 pending_job 退化成 cooldown，它的真实结果也再也回不到表里。
func TestPutNeverEvictsALiveJob(t *testing.T) {
	m := newAgentUpdateManager()
	for i := 0; i < agentUpdateMaxJobs+20; i++ {
		status := "done"
		if i < 5 {
			status = "running" // 最老的五个还活着
		}
		m.put(&agentUpdateJob{
			ID: fmt.Sprintf("au-%03d", i), Status: status, CreatedAt: int64(1000 + i),
			Hosts: []*agentUpdateHostResult{{HostID: fmt.Sprintf("h%d", i), Status: "pending_verify"}},
		})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("au-%03d", i)
		if m.jobs[id] == nil {
			t.Fatalf("活着的 job %s 被挤掉了：这台主机的升级结果从此无人可见", id)
		}
	}
	if len(m.jobs) > agentUpdateMaxJobs+5 {
		t.Fatalf("done 的 job 没有被回收：len=%d", len(m.jobs))
	}
}

// 全是活 job 时宁可超出上限也不能丢——丢掉的正是「正在换版」的那些。
func TestPutKeepsAllWhenNothingIsFinished(t *testing.T) {
	m := newAgentUpdateManager()
	for i := 0; i < agentUpdateMaxJobs+10; i++ {
		m.put(&agentUpdateJob{ID: fmt.Sprintf("au-%03d", i), Status: "running", CreatedAt: int64(1000 + i)})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) != agentUpdateMaxJobs+10 {
		t.Fatalf("活着的 job 被淘汰了：len=%d，期望 %d", len(m.jobs), agentUpdateMaxJobs+10)
	}
}

// 冷却理由里的数字必须是真的：版本仍落后的主机走的是 360s 软重试窗，不是 1800s。
func TestCooldownReasonQuotesTheRealWindow(t *testing.T) {
	srv, _ := newTestServer(t)
	old := appVersion
	appVersion = "v9.9.9"
	defer func() { appVersion = old }()
	if err := srv.cfg.SetAgentAutoUpdatePolicy(true, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	// dist 里放一个能匹配上的二进制，否则会先撞在 no_artifact 上。
	writeFakeAgentDist(t, srv)

	h := &Host{ID: "h1", Hostname: "server11", OS: "windows", Arch: "amd64",
		AgentVersion: "v1.0.0", ServerURL: "https://panel.example.com", LastSeen: time.Now().Unix()}
	putTestHost(srv, h)
	srv.agentUpdates.refreshInFlight("h1") // 刚刚有过一次升级动作

	_, reason, detail := srv.decideAutoUpdate(h)
	if reason != "cooldown" {
		t.Fatalf("reason=%q detail=%q，期望 cooldown", reason, detail)
	}
	if strings.Contains(detail, "1800") {
		t.Fatalf("冷却理由报了一个比现实大五倍的数字：%q", detail)
	}
	if !strings.Contains(detail, "360") {
		t.Fatalf("冷却理由没说清是哪个窗口：%q", detail)
	}
}

// 「最近的升级任务」是 pending_job / cooldown 那几条理由指过去的地方，也是唯一能看到
// Windows 助手日志尾巴的地方。服务端返回的是 {"jobs": [...]}，控制台若把它当裸数组读，
// 这张表就永远是空的——而空表和「最近没有升级任务」长得一模一样，于是"升级失败了"与
// "从没升过"在屏幕上无从分辨。这条测试同时钉住两端。
func TestUpdateJobsResponseShapeMatchesConsole(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.agentUpdates.put(&agentUpdateJob{
		ID: "au-1", Status: "done", TargetVer: "v9.9.9", CreatedAt: time.Now().Unix(),
		Hosts: []*agentUpdateHostResult{{HostID: "h1", Hostname: "server11", Status: "failed"}},
	})
	rr := httptest.NewRecorder()
	srv.handleAgentUpdateJobs(rr, httptest.NewRequest(http.MethodGet, "/api/v1/agents/update/jobs?limit=60", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rr.Code)
	}
	var body struct {
		Jobs []struct {
			ID    string `json:"id"`
			Hosts []struct {
				Hostname string `json:"hostname"`
			} `json:"hosts"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Jobs) != 1 || len(body.Jobs[0].Hosts) != 1 {
		t.Fatalf("响应结构变了，控制台的解包会跟着失效：%s", rr.Body.String())
	}

	js, err := webFS.ReadFile("web/js/settings.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, "jobs.jobs") {
		t.Fatal("控制台没有从 {\"jobs\": …} 里解包，这张表会永远显示为空")
	}
	if strings.Contains(src, "agents/update/jobs?limit=5`") {
		t.Fatal("limit=5 装不下一台一个 job 的机队：多数主机的结果不会有行")
	}
}

func TestAutoUpdateSkipClearedOnceVersionCatchesUp(t *testing.T) {
	srv, _ := newTestServer(t)
	old := appVersion
	appVersion = "v9.9.9"
	defer func() { appVersion = old }()

	putTestHost(srv, &Host{ID: "h1", Hostname: "server11", OS: "windows", Arch: "amd64",
		AgentVersion: "v9.9.9", LastSeen: time.Now().Unix()})
	srv.agentUpdates.recordSkip("h1", "pending_job", "已有升级任务在进行中")

	srv.maybeAutoUpdateHost("h1")

	for _, sk := range srv.agentUpdates.skipSnapshot(srv) {
		if sk.HostID == "h1" {
			t.Fatalf("已升到目标版本的主机仍挂着跳过原因 %q，看上去像还没升级", sk.Reason)
		}
	}
}

func TestAutoUpdateScanRecordsOfflineSkip(t *testing.T) {
	srv, _ := newTestServer(t)
	old := appVersion
	appVersion = "v9.9.9"
	defer func() { appVersion = old }()
	if err := srv.cfg.SetAgentAutoUpdatePolicy(true, "", nil, nil); err != nil {
		t.Fatal(err)
	}

	putTestHost(srv, &Host{ID: "h1", Hostname: "server11", OS: "windows", Arch: "amd64",
		AgentVersion: "v1.0.0", LastSeen: time.Now().Unix() - 86400})
	putTestHost(srv, &Host{ID: "h2", Hostname: "server12", OS: "windows", Arch: "amd64",
		AgentVersion: "v9.9.9", LastSeen: time.Now().Unix() - 86400})
	// 上一轮留下的、如今已经过时的理由。
	srv.agentUpdates.recordSkip("h1", "pending_job", "已有升级任务在进行中")

	srv.runAgentAutoUpdateScan()

	got := map[string]agentUpdateSkipView{}
	for _, sk := range srv.agentUpdates.skipSnapshot(srv) {
		got[sk.HostID] = sk
	}
	if got["h1"].Reason != "offline" {
		t.Fatalf("离线且版本落后的主机应记 offline，得到 %q（%q）", got["h1"].Reason, got["h1"].Detail)
	}
	if !strings.Contains(got["h1"].Detail, "最后上报于") {
		t.Fatalf("offline 理由要带上离线多久：%q", got["h1"].Detail)
	}
	if _, ok := got["h2"]; ok {
		t.Fatal("已是目标版本的离线主机不该被记进跳过表（纯噪音）")
	}
}
