package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderEmbeddedTemplate(t *testing.T) {
	tpl, ver, err := render("assist-logql", promptVars{"context_block": "\n\n【上下文】\nhost=demo"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(tpl, "LogQL 专家") || !strings.Contains(tpl, "host=demo") {
		t.Fatalf("template render incomplete: %.80s", tpl)
	}
	if ver == "" || len(ver) != 12 {
		t.Fatalf("bad version fingerprint %q", ver)
	}
}

func TestRenderMissingTemplateFallsBack(t *testing.T) {
	// 无模板时 render 返回 err，调用方回退内联字符串。
	_, _, err := render("assist-no-such-template", promptVars{})
	if err == nil {
		t.Fatal("expected error for missing template")
	}
	// buildAssistSystemPrompt 对无模板的 task 回退内联（不 panic、非空）。
	sys := buildAssistSystemPrompt("hardware_diagnosis", "")
	if sys == "" {
		t.Fatal("fallback should return non-empty")
	}
	if !strings.Contains(sys, "hardware") && !strings.Contains(sys, "硬件") {
		t.Fatalf("fallback should contain task hint: %.60s", sys)
	}
}

func TestOverrideDirTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	// 写一个覆盖模板，内容含独特标记。
	content := "【覆盖模板】你是自定义 LogQL 专家。{{context_block}}"
	if err := os.WriteFile(filepath.Join(dir, "assist-logql.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	defaultPromptStore.SetOverrideDir(dir)
	defer defaultPromptStore.SetOverrideDir("")

	tpl, _, err := render("assist-logql", promptVars{"context_block": "CTX"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tpl, "自定义 LogQL 专家") || !strings.Contains(tpl, "CTX") {
		t.Fatalf("override template not used: %.80s", tpl)
	}
}

func TestHashVersionDeterministic(t *testing.T) {
	first, second := hashVersion("abc"), hashVersion("abc")
	if first != second {
		t.Fatal("hash should be deterministic")
	}
	if hashVersion("abc") == hashVersion("abd") {
		t.Fatal("hash should differ for different inputs")
	}
}

func TestPromptVersionFor(t *testing.T) {
	if v := promptVersionFor("assist-logql"); v == "" {
		t.Fatal("expected version for existing template")
	}
	if v := promptVersionFor("nope"); v != "" {
		t.Fatalf("expected empty version for missing, got %q", v)
	}
}
