package main

import (
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
