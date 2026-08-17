package main

import (
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
