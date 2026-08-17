package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 安全条款必须在提示词最前，任务模板紧随其后。顺序不是审美问题：模型对系统提示词
// 开头的约束最敏感，把防注入条款推到检索材料后面等于把它埋进噪音里。
func TestAssistPromptPutsSafetyClauseFirst(t *testing.T) {
	srv, _ := newTestServer(t)
	parts := srv.buildAssistPrompt(AIConfig{}, assistPromptReq{Task: "promql", RAGQuery: "cpu 使用率"})

	if !strings.HasPrefix(parts.System, aiUntrustedDataClause) {
		t.Fatalf("系统提示词必须以安全边界条款开头，实际开头：%.120q", parts.System)
	}
	if !strings.Contains(parts.System, "PromQL") {
		t.Fatalf("任务模板没有拼进来：%.400q", parts.System)
	}
}

// 两个 AI 入口共用一条装配线的全部意义，就是条款只有一处定义。任何人再把这段字面量
// 抄回调用点，这条测试立刻红——否则改一处忘另一处不会有任何报错，只会让某条入口悄悄
// 失去防注入约束。
func TestSafetyClauseHasExactlyOneDefinition(t *testing.T) {
	const needle = "【安全边界】调用方上下文"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			continue
		}
		if strings.Contains(string(b), needle) && name != "ai_prompt_shared.go" {
			offenders = append(offenders, name)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("安全边界条款被重新内联到 %v；请改用 aiUntrustedDataClause", offenders)
	}
}

// 热加载模板此前只喂给 Hermes，于是「在 AI 设置里改了模板」对全站 9 个就地按钮毫无
// 效果。现在 assist 也吃得到，但只吃分类命中的——否则「生成 PromQL」会背上一堆排障模板。
func TestActiveTemplatesForFiltersByTask(t *testing.T) {
	srv, _ := newTestServer(t)
	h := newSreyunCore(srv)
	h.cachedTemplates = []sreyunTemplate{
		{Name: "promql 写法", Category: "promql", Content: "PROMQL_TPL", Active: true},
		{Name: "通用口径", Category: "global", Content: "GLOBAL_TPL", Active: true},
		{Name: "assist 通用", Category: "assist", Content: "ASSIST_TPL", Active: true},
		{Name: "排障流程", Category: "diagnosis", Content: "DIAG_TPL", Active: true},
	}
	srv.sreyun = h

	got := srv.hotTemplateBlock("promql")
	for _, want := range []string{"PROMQL_TPL", "GLOBAL_TPL", "ASSIST_TPL"} {
		if !strings.Contains(got, want) {
			t.Fatalf("命中的模板 %q 没有注入：%q", want, got)
		}
	}
	if strings.Contains(got, "DIAG_TPL") {
		t.Fatalf("与任务无关的模板被注入了：%q", got)
	}
	if block := srv.hotTemplateBlock("logql"); strings.Contains(block, "PROMQL_TPL") {
		t.Fatalf("logql 任务不该拿到 promql 模板：%q", block)
	}
}

// assist 是遍布全站的就地按钮，每次点击都要付这份 token。运维配了一堆模板也不能把
// 「解释这张图」变成一次大请求。
func TestHotTemplateBlockRespectsBudget(t *testing.T) {
	srv, _ := newTestServer(t)
	h := newSreyunCore(srv)
	big := strings.Repeat("模", 3000)
	for i := 0; i < 5; i++ {
		h.cachedTemplates = append(h.cachedTemplates, sreyunTemplate{
			Name: "t", Category: "assist", Content: big, Active: true,
		})
	}
	srv.sreyun = h
	if n := len(srv.hotTemplateBlock("promql")); n > aiHotTemplateBudget+len(big) {
		t.Fatalf("热加载模板注入没有收敛，长度 %d", n)
	}
}

// 没有 Sreyun 核心（AI 未启用）时装配不能炸——assist 按钮在 AI 未配置时也会被点到。
func TestHotTemplateBlockWithoutSreyun(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.sreyun = nil
	if got := srv.hotTemplateBlock("promql"); got != "" {
		t.Fatalf("无 Sreyun 时应返回空，得到 %q", got)
	}
}

func TestAssistTaskWantsForecastBias(t *testing.T) {
	for _, tc := range []struct {
		task, query string
		want        bool
	}{
		{"forecast_analysis", "磁盘", true},
		{"promql", "未来一周会不会满", true},
		{"promql", "预测磁盘增长", true},
		{"promql", "现在 CPU 多少", false},
		{"chart_analysis", "解释这张图", false},
	} {
		if got := assistTaskWantsForecastBias(tc.task, tc.query); got != tc.want {
			t.Fatalf("assistTaskWantsForecastBias(%q, %q) = %v, want %v", tc.task, tc.query, got, tc.want)
		}
	}
}
