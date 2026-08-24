package main

import (
	"strings"
	"testing"
	"time"
)

// AI 黄金集（P2-3）：不调真实 LLM，校验任务路由策略与关键 system prompt 契约。
// CI 烟雾用：go test ./cmd/server/ -count=1 -run 'TestAIGolden|TestAIStats|TestAssistTaskPolicy'

type goldenCase struct {
	name           string
	task           string
	wantMemKind    string
	wantNoThink    bool
	wantTimeout    bool // Timeout > 0
	promptMustHave []string
	promptForbid   []string
}

func TestAssistTaskPolicy_Golden(t *testing.T) {
	cases := []goldenCase{
		{name: "logql", task: "logql", wantMemKind: "chat", wantTimeout: true, promptMustHave: []string{"LogQL"}},
		{name: "promql", task: "promql", wantMemKind: "chat", wantTimeout: true, promptMustHave: []string{"PromQL"}},
		{name: "playbook", task: "playbook", wantMemKind: "chat", wantTimeout: true, promptMustHave: []string{"剧本"}},
		{name: "chart", task: "chart_analysis", wantMemKind: "diagnosis", promptMustHave: []string{"SRE"}},
		{name: "hw", task: "hardware_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"硬件"}},
		{name: "hyperv", task: "hyperv_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"Hyper-V"}},
		{name: "snmp", task: "snmp_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"SNMP"}},
		{name: "trap", task: "trap_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"Trap"}},
		{name: "netflow", task: "netflow_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"流量"}},
		{name: "checks", task: "checks_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"拨测"}},
		{name: "forward", task: "forward_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"转发"}},
		{name: "apimon", task: "apimon_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"API"}},
		{name: "audit", task: "audit_diagnosis", wantMemKind: "diagnosis"},
		{name: "content", task: "content_audit_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"敏感"}},
		{name: "host_sec", task: "host_security_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"主机"}},
		{name: "host_sec_rem", task: "host_security_remediation", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"动作"}},
		{name: "host_sec_finding", task: "host_security_finding", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"单条"}},
		{name: "web_vuln", task: "web_vuln_diagnosis", wantMemKind: "diagnosis", promptMustHave: []string{"Web"}},
		{name: "web_vuln_rem", task: "web_vuln_remediation", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"动作"}},
		{name: "web_vuln_finding", task: "web_vuln_finding", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"单条"}},
		{name: "hv_ops", task: "hyperv_ops_plan", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"Hyper-V"}},
		{name: "ct_ops", task: "container_ops_plan", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"容器"}},
		{name: "k8s_ops", task: "k8s_ops_plan", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"Kubernetes"}},
		{name: "sql_rem", task: "sql_remediation", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"sql"}},
		{name: "dash_opt", task: "dashboard_optimize", wantMemKind: "diagnosis", wantNoThink: false, wantTimeout: true, promptMustHave: []string{"看板", "BI", "VictoriaMetrics", "LogQL"}},
		{name: "dash_ana", task: "dashboard_analysis", wantMemKind: "diagnosis", wantNoThink: false, wantTimeout: true, promptMustHave: []string{"VictoriaMetrics"}},
		{name: "dash_prompt", task: "dashboard_prompt_optimize", wantMemKind: "chat", wantNoThink: false, wantTimeout: true, promptMustHave: []string{"看板", "BI", "$__range"}},
		{name: "remediation", task: "remediation_rule", wantMemKind: "chat", wantTimeout: true, promptMustHave: []string{"规则"}},
		{name: "duty", task: "duty_report", wantMemKind: "chat", promptMustHave: []string{"值班"}},
		{name: "sql_beautify", task: "sql_beautify", wantMemKind: "chat", wantTimeout: true, promptMustHave: []string{"美化", "sql"}},
		{name: "sql_audit", task: "sql_audit", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"审核"}},
		{name: "sql_optimize", task: "sql_optimize", wantMemKind: "diagnosis", wantTimeout: true, promptMustHave: []string{"优化", "索引"}},
		{name: "generic", task: "generic", wantMemKind: "chat"},
	}
	if len(cases) < 20 {
		t.Fatalf("golden set too small: %d", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := assistTaskPolicy(c.task)
			if p.MemKind != c.wantMemKind {
				t.Errorf("MemKind=%q want %q", p.MemKind, c.wantMemKind)
			}
			if p.DisableThink != c.wantNoThink {
				t.Errorf("DisableThink=%v want %v", p.DisableThink, c.wantNoThink)
			}
			if strings.HasPrefix(c.task, "dashboard_") && !p.EnableThink {
				t.Errorf("dashboard task should EnableThink")
			}
			if c.task == "dashboard_optimize" {
				if p.ThinkingBudget <= 0 || p.ThinkingBudget > 1024 {
					t.Errorf("dashboard_optimize ThinkingBudget 应在 1~1024，got %d", p.ThinkingBudget)
				}
				if p.MaxTokens < 8192 {
					t.Errorf("dashboard_optimize MaxTokens 应足够容纳完整 JSON，got %d", p.MaxTokens)
				}
				if p.Timeout < 180*time.Second {
					t.Errorf("dashboard_optimize timeout too short: %v", p.Timeout)
				}
			}
			if c.wantTimeout && p.Timeout <= 0 {
				t.Errorf("expected Timeout > 0")
			}
			// 只有标了 wantTimeout 的任务强制要求 Timeout > 0；其余任务给不给都算合法，
			// 所以这里没有对应的反向断言（原来写成一个空的 if，读起来像漏了断言）。
			if p.RememberSource != "assist:"+c.task {
				t.Errorf("RememberSource=%q", p.RememberSource)
			}
			sys := buildAssistSystemPrompt(c.task, "【测试上下文】host=demo")
			if !strings.Contains(sys, "【测试上下文】") {
				t.Errorf("context not injected")
			}
			for _, must := range c.promptMustHave {
				if !strings.Contains(sys, must) {
					t.Errorf("prompt missing %q; got prefix %q", must, trimLine(sys, 80))
				}
			}
			for _, forbid := range c.promptForbid {
				if strings.Contains(sys, forbid) {
					t.Errorf("prompt unexpectedly contains %q", forbid)
				}
			}
		})
	}
}

func TestAIGolden_HyperVAndHardwarePromptsDistinct(t *testing.T) {
	hw := buildAssistSystemPrompt("hardware_diagnosis", "")
	hv := buildAssistSystemPrompt("hyperv_diagnosis", "")
	if !strings.Contains(hw, "硬件") {
		t.Fatal("hardware prompt")
	}
	if !strings.Contains(hv, "Hyper-V") {
		t.Fatal("hyperv prompt")
	}
	if hw == hv {
		t.Fatal("prompts should differ")
	}
}

func TestAIStatsHub_RecordAndSnapshot(t *testing.T) {
	h := newAIStatsHub()
	h.record(aiCallStat{Ts: time.Now().Unix(), Task: "logql", Model: "m", LatencyMs: 100, OK: true, ApproxTokens: 50})
	h.record(aiCallStat{Ts: time.Now().Unix(), Task: "logql", Model: "m", LatencyMs: 200, OK: false, Error: "boom", ApproxTokens: 10})
	h.record(aiCallStat{Ts: time.Now().Unix(), Task: "chat", Model: "m", LatencyMs: 50, OK: true, ApproxTokens: 20})
	snap := h.snapshot()
	if snap["total"].(int64) != 3 {
		t.Fatalf("total=%v", snap["total"])
	}
	if snap["fail"].(int64) != 1 {
		t.Fatalf("fail=%v", snap["fail"])
	}
	if snap["approx_tokens_total"].(int64) != 80 {
		t.Fatalf("tokens=%v", snap["approx_tokens_total"])
	}
	by := snap["by_task"].(map[string]aiTaskAgg)
	if by["logql"].Count != 2 || by["logql"].Fail != 1 {
		t.Fatalf("logql agg=%+v", by["logql"])
	}
	if by["logql"].AvgMs != 150 {
		t.Fatalf("avg=%d", by["logql"].AvgMs)
	}
	h.recordFeedback("logql", "applied")
	h.recordFeedback("logql", "helpful")
	h.recordFeedback("chat", "unhelpful")
	snap = h.snapshot()
	if snap["feedback_total"].(int64) != 3 || snap["feedback_applied"].(int64) != 1 ||
		snap["feedback_helpful"].(int64) != 1 || snap["feedback_unhelpful"].(int64) != 1 {
		t.Fatalf("feedback aggregate=%v", snap)
	}
	if got := snap["feedback_positive_rate"].(float64); got < 0.66 || got > 0.67 {
		t.Fatalf("positive rate=%v", got)
	}
	fbBy := snap["feedback_by_task"].(map[string]aiFeedbackAgg)
	if fbBy["logql"].Total != 2 || fbBy["chat"].Unhelpful != 1 {
		t.Fatalf("feedback by task=%+v", fbBy)
	}
}

func TestEstimateTokens_Smoke(t *testing.T) {
	if estimateTokens("") != 0 {
		t.Fatal("empty")
	}
	n := estimateTokens("你好世界 hello world")
	if n <= 0 {
		t.Fatal("expected positive estimate")
	}
}

func TestAllowedAIImageMIME(t *testing.T) {
	for _, mime := range []string{"image/png", "image/jpeg", "image/webp", "IMAGE/GIF"} {
		if !allowedAIImageMIME(mime) {
			t.Fatalf("expected %q to be allowed", mime)
		}
	}
	for _, mime := range []string{"", "image/svg+xml", "text/html", "application/octet-stream"} {
		if allowedAIImageMIME(mime) {
			t.Fatalf("expected %q to be rejected", mime)
		}
	}
}
