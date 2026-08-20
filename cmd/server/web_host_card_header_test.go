package main

import (
	"regexp"
	"strings"
	"testing"
)

// 主机卡片的头一行只放「主机名 + 分组 + 终端/桌面/删除」。
//
// 坏过一次，症状是"卡片上根本看不到主机名"：头一行左边是标题、右边是标签，两边都写了
// flex:1，而右边塞着分组徽标 + 系统类型 + Agent 版本 + 三个按钮（定宽内容 ~250px）。
// 卡片本身只有 ~300px 宽，标题的 flex-basis 是 0 且 min-width:0，于是被压到零宽，
// 用户看到的就是"第一行只有一排标签"。
//
// 修法是把两个定宽徽标（系统类型、Agent 版本）挪到 IP 行下面单独一行，并让标题优先
// 拿宽度（标签行不再 grow、标题留一个最小宽度）。这条测试钉住这两件事。
func TestHostCardHeaderKeepsHostName(t *testing.T) {
	raw, err := webFS.ReadFile("web/js/hosts.js")
	if err != nil {
		t.Fatalf("read hosts.js: %v", err)
	}
	src := string(raw)

	card := between(t, src, "function hostCard(h) {", "\n/* ---------- 渲染：主机列表表头")
	head := between(t, card, `<div class="host-head">`, `<div class="host-meta"`)

	for _, forbidden := range []string{"os-badge", "agentVer"} {
		if strings.Contains(head, forbidden) {
			t.Errorf("卡片头里又出现了 %s——系统类型/Agent 版本属于 IP 行下面那一行。\n"+
				"它们是定宽内容，回到头一行就会把主机名挤到零宽（卡片上看不到名字）。", forbidden)
		}
	}
	for _, want := range []string{"hostCategoryBadgeHTML", `data-act="term"`, `data-act="desktop"`, `data-act="del"`} {
		if !strings.Contains(head, want) {
			t.Errorf("卡片头里少了 %s：头一行应保留主机名、分组徽标与终端/桌面/删除按钮", want)
		}
	}

	sys := between(t, card, `<div class="host-sys">`, "</div>")
	if !strings.Contains(sys, "os-badge") || !strings.Contains(sys, "${agentVer}") {
		t.Errorf("IP 行下面那行应同时有系统类型徽标与 Agent 版本徽标，实际是：\n  %s", strings.TrimSpace(sys))
	}
	if strings.Index(card, `<div class="host-meta"`) > strings.Index(card, `<div class="host-sys">`) {
		t.Error("host-sys 应排在 host-meta（IP 行）之后")
	}
}

// 头一行的宽度分配：标题先拿，标签行只在剩余空间里收缩。
func TestHostCardHeaderFlexGivesTitleThePriority(t *testing.T) {
	raw, err := webFS.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(raw)

	tags := cssRule(t, css, ".host-tags")
	if !strings.Contains(tags, "flex:0 1 auto") {
		t.Errorf(".host-tags 必须是 flex:0 1 auto（不参与 grow）：\n  %s\n"+
			"一旦它和 .host-name 同时 grow，长分组路径就能把主机名压到零宽。", tags)
	}
	name := cssRule(t, css, ".host-name")
	if !regexp.MustCompile(`min-width:\s*[1-9]`).MatchString(name) {
		t.Errorf(".host-name 需要一个非零 min-width 兜底，否则窄卡片上标题仍会被压没：\n  %s", name)
	}
}

// between 取出 src 中 start 与 end 之间的片段（不含标记本身）。
func between(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("找不到 %q——渲染结构被改动过，请同步本测试", start)
	}
	rest := src[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("找不到 %q（在 %q 之后）", end, start)
	}
	return rest[:j]
}

// cssRule 取出选择器对应的那条声明块（只认单选择器、写在一行的规则）。
func cssRule(t *testing.T, css, selector string) string {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(selector) + `\{[^}]*\}`)
	m := re.FindString(css)
	if m == "" {
		t.Fatalf("style.css 里找不到 %s 的规则", selector)
	}
	return m
}
