package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 现网这一行（server11，2026-08-17 16:32）是 Windows 升级最常走到的终点：
//
//	failed | script | v0.19.93→v0.19.96 | restart scheduled but agent_version still behind v0.19.96
//	                                      (helper may have failed; will soft-retry)
//
// 「可能失败了」是操作员早就知道的那半句，真正写着原因的是那台机器上的
// ProgramData\aiops-agent-update\aiops-agent-update.log。legacy 救援分支下判决前会去取它，
// 但救援只对 method=module 的主机开——走过 script 的主机（上面这行就是）直接落到这里，
// 于是最需要证据的那一类反而一条都没有。
func TestVerifyFailureVerdictCarriesHostEvidence(t *testing.T) {
	srv, _ := newTestServer(t)
	h := &Host{ID: "h1", Hostname: "server11", OS: "windows", Arch: "amd64",
		AgentVersion: "v0.19.93", ServerURL: "https://panel.example.com"}
	// LastSeen=0 → 换版之后再没回来过。此时 exec 一定收不到回答，windowsUpdateEvidence
	// 走的是免 exec 的那条分支（否则每台失联主机都要先干等 90s 的 exec 取件超时）。
	putTestHost(srv, h)

	job := &agentUpdateJob{ID: "au-1", Status: "running", TargetVer: "v0.19.96", CreatedAt: time.Now().Unix(),
		Hosts: []*agentUpdateHostResult{{HostID: "h1", Hostname: "server11", Status: "pending_verify", Method: "script"}}}
	srv.agentUpdates.put(job)

	srv.markHostUpdateVerifyFailed(job, "h1", "v0.19.96")

	got := srv.agentUpdates.snapshot("au-1")
	if got == nil || len(got.Hosts) != 1 {
		t.Fatal("job vanished")
	}
	hr := got.Hosts[0]
	if hr.Status != "failed" {
		t.Fatalf("status=%q, want failed", hr.Status)
	}
	if !strings.Contains(hr.Message, "aiops-agent-update.log") {
		t.Fatalf("verdict still names no evidence at all: %q", hr.Message)
	}
	if !strings.Contains(hr.Message, "v0.19.96") {
		t.Fatalf("verdict lost the target version: %q", hr.Message)
	}
}

// 助手把 result/log 写在主流程第一步，两个文件都不存在只有一种解释：助手根本没起来，
// 拉起它的那条路（WMI / 计划任务 / cmd）静默失败了。这跟「起来了但换版失败」是两种
// 完全不同的故障，从前都被 windowsUpdateEvidence 的 `return ""` 抹成同一句空话。
func TestEmptyHelperLogIsItselfTheEvidence(t *testing.T) {
	empty := helperEvidenceSummary("", nil)
	if strings.TrimSpace(empty) == "" {
		t.Fatal("an empty helper log must be reported as evidence, not swallowed")
	}
	if !strings.Contains(empty, "never started") {
		t.Fatalf("an empty log means the helper never ran; say so: %q", empty)
	}

	// 取不回来跟「取回来是空的」必须分得开：前者要去查连通性，后者要去查拉起助手的那条路。
	unreachable := helperEvidenceSummary("", errTestEvidence{})
	if strings.Contains(unreachable, "never started") || !strings.Contains(unreachable, "could not read") {
		t.Fatalf("unreachable host must not be reported as 'helper never started': %q", unreachable)
	}

	// 真有日志时，CLIXML 噪声不能把它挤掉。
	withLog := helperEvidenceSummary(realWorldCLIXML, nil)
	if strings.Contains(withLog, "<Objs") {
		t.Fatalf("evidence carries raw CLIXML into the verdict: %q", withLog)
	}
	if !strings.Contains(withLog, "legacy agent update ok") {
		t.Fatalf("evidence lost its content: %q", withLog)
	}
}

type errTestEvidence struct{}

func (errTestEvidence) Error() string { return "exec pickup timeout" }

// pending_verify 会在操作台上停留好几分钟，而它原本只说了「命令已下发」这件过去的事。
// 读表的人真正需要的是未来：还要等多久、等不到会怎样。缺了这半句，一台正在按计划推进
// 的主机和一台真卡死的主机在屏幕上长得一模一样，于是有人会去做多余的手工干预。
func TestPendingVerifyRowSaysWhatHappensNext(t *testing.T) {
	job := &agentUpdateJob{ID: "au-1", TargetVer: "v0.19.98"}

	win := pendingVerifyNextStep("windows", "module", job)
	if !strings.Contains(win, "5 分钟") {
		t.Fatalf("没给出校验窗口时长：%q", win)
	}
	if !strings.Contains(win, "script-rescue") {
		t.Fatalf("module 路径超时会自动走 legacy 救援，这一点必须写在行里：%q", win)
	}

	// 已经走过 script 的主机不会被救援（问题不在 Agent 侧助手上），所以不能承诺救援。
	if s := pendingVerifyNextStep("windows", "script", job); strings.Contains(s, "script-rescue") {
		t.Fatalf("script 路径不该承诺一次不会发生的救援：%q", s)
	}

	// 非 Windows 与回滚都没有救援阶梯。
	if s := pendingVerifyNextStep("linux", "module", job); strings.Contains(s, "script-rescue") {
		t.Fatalf("Linux 没有 legacy 救援这一步：%q", s)
	}
	rb := &agentUpdateJob{ID: "au-2", Rollback: true}
	if s := pendingVerifyNextStep("windows", "module", rb); strings.Contains(s, "script-rescue") {
		t.Fatalf("回滚不会触发救援：%q", s)
	}
}

// 「这个日志文件我没看到」——现网问的第一个问题。证据在主机本地，服务端一直有取它的
// 能力，但以前只在写失败判决时用；pending_verify 的那十几分钟里谁也拿不到，只能远程
// 桌面上去翻。这条接口把同一条命令变成一次点击，测试钉住它的边界与控制台的接线。
func TestAgentUpdateEvidenceEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	putTestHost(srv, &Host{ID: "hw", Hostname: "server11", OS: "windows", Arch: "amd64", LastSeen: time.Now().Unix()})
	putTestHost(srv, &Host{ID: "hl", Hostname: "debian", OS: "linux", Arch: "amd64", LastSeen: time.Now().Unix()})

	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/update/evidence", strings.NewReader(body))
		srv.handleAgentUpdateEvidence(rr, req)
		return rr
	}
	if rr := post(`{"host_id":"nope"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("未知主机应 404，得到 %d", rr.Code)
	}
	// Linux 主机不该走这条路：它的换版是 shell 脚本内联做的，结果本来就回在任务消息里。
	rr := post(`{"host_id":"hl"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("非 Windows 主机应 400，得到 %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Windows") {
		t.Fatalf("拒绝原因没说清适用范围：%s", rr.Body.String())
	}

	// 控制台侧：按钮存在、打到正确的接口、且只给 Windows 行。
	js, err := webFS.ReadFile("web/js/settings.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{"data-agent-evidence", "agents/update/evidence", `String(h.os || "").toLowerCase().indexOf("win")`} {
		if !strings.Contains(src, want) {
			t.Fatalf("控制台没有接上取证按钮，缺 %q", want)
		}
	}
}
