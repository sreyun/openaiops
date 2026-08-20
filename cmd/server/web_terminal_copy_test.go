package main

import (
	"strings"
	"testing"
)

// 终端里"选不中、复制不了"坏过一整轮，根因有三条，这里逐条钉住。
//
//  1. 按下鼠标的瞬间把焦点交给隐藏 textarea——focus() 会把选区上下文切走，
//     正在进行的拖拽选区当场作废，于是怎么拖都选不中；
//  2. 右键菜单（复制/粘贴/全选）被注释掉了，"待修复后重新启用"一直没启用；
//  3. 复制走 navigator.clipboard 而不做兜底——明文 HTTP 是非安全上下文，
//     navigator.clipboard 根本不存在，复制静默失败。
func TestTerminalSelectionIsNotStolenByFocus(t *testing.T) {
	src := readWebFile(t, "web/js/terminal.js")

	if strings.Contains(src, "在 mousedown 阶段先设置一个延迟聚焦守卫") {
		t.Error("mousedown 阶段又在抢焦点了：按住鼠标时 focus() 会清掉正在拖的选区，终端里将无法选中文本")
	}
	mousedown := jsHandlerBody(t, src, `screen.addEventListener("mousedown"`)
	if strings.Contains(mousedown, "input.focus(") {
		t.Errorf("mousedown 处理里不能 focus 隐藏 textarea：\n  %s", strings.TrimSpace(mousedown))
	}
	// <pre> 自己被鼠标聚焦时也不能立刻转交焦点（同一个坑的第二条路径）。
	focusBody := jsHandlerBody(t, src, `screen.addEventListener("focus"`)
	if !strings.Contains(focusBody, "_termDragging") {
		t.Errorf("focus 转交没有躲开鼠标拖拽期：\n  %s", strings.TrimSpace(focusBody))
	}
}

func TestTerminalContextMenuIsEnabled(t *testing.T) {
	src := readWebFile(t, "web/js/terminal.js")

	if strings.Contains(src, `// screen.addEventListener("contextmenu"`) {
		t.Fatal("终端右键菜单还是被注释掉的——复制/粘贴/全选都点不出来")
	}
	if !strings.Contains(src, `screen.addEventListener("contextmenu"`) {
		t.Fatal("终端没有注册 contextmenu 监听，右键出不来菜单")
	}
	for _, action := range []string{`data-action="copy"`, `data-action="select-all"`, `data-action="copy-all"`} {
		if !strings.Contains(src, action) {
			t.Errorf("右键菜单缺少 %s（复制窗口内容要靠「全选」与「复制全部」）", action)
		}
	}
}

func TestTerminalCopyFallsBackOnPlainHTTP(t *testing.T) {
	term := readWebFile(t, "web/js/terminal.js")
	core := readWebFile(t, "web/js/core.js")

	if strings.Contains(term, "navigator.clipboard.writeText") {
		t.Error("terminal.js 直接调 navigator.clipboard.writeText：明文 HTTP 下它不存在，复制会静默失败，" +
			"应统一走 copyToClipboard（内含 execCommand 兜底）")
	}
	if !strings.Contains(term, "function termCopyText") || !strings.Contains(term, "copyToClipboard(out)") {
		t.Error("终端复制应收敛到 termCopyText → copyToClipboard 一条路径")
	}
	entry := jsFunctionBody(t, core, "function copyToClipboard(text) {")
	if !strings.Contains(entry, "window.isSecureContext") {
		t.Error("copyToClipboard 要先判断安全上下文（明文 HTTP 下 navigator.clipboard 不存在）")
	}
	if !strings.Contains(entry, "execCommandCopy(text)") {
		t.Error("copyToClipboard 必须退回 execCommandCopy")
	}
	// 异步剪贴板 API 被拒（没授权 / 文档没聚焦 / 策略拦截）也要有下家，
	// 否则用户看到的就是一句"复制失败"——真机上就是这么翻的。
	if !strings.Contains(entry, ".catch(() => execCommandCopy(text))") {
		t.Error("navigator.clipboard.writeText 被 reject 时没有回退到 execCommand")
	}
	fallback := jsFunctionBody(t, core, "function execCommandCopy(text) {")
	if !strings.Contains(fallback, `execCommand("copy")`) {
		t.Error("execCommandCopy 里没有 execCommand 兜底")
	}
}

// 跨行选中必须带换行：Range.toString() 只拼文本节点、不认块级边界，
// 整屏命令会被粘成一整行。
func TestTerminalSelectionKeepsLineBreaks(t *testing.T) {
	src := readWebFile(t, "web/js/terminal.js")
	body := jsFunctionBody(t, src, "function getSelectedTermText(tab) {")
	if !strings.Contains(body, "s.toString()") {
		t.Error("选区文本要用 Selection.toString()（它按渲染结构补换行）")
	}
	if strings.Contains(body, "rng.toString()") {
		t.Error("选区文本不能用 Range.toString()：跨行会被拼成一整行")
	}
	if !strings.Contains(src, "function normalizeTermCopyText") {
		t.Error("复制出去的文本要去掉每行尾部的填充空格（normalizeTermCopyText）")
	}
}

// 全局焦点守卫必须给"选文本"让路。
//
// 这是同一个坑的第三条路径，也是最隐蔽的一条：_refocusActiveTermInput() 与 focusin 监听
// 会把焦点拉回隐藏 textarea。用户按下鼠标开始拖选时 <pre> 先拿到焦点 → focusin 触发 →
// 焦点被拉走 → document 选区当场清空，于是无论怎么拖都选不中。
// 真机（无头 Chromium 实拖）验过：只改 mousedown/focus 两处不够，必须连这里一起放行。
func TestTerminalFocusGuardYieldsToSelection(t *testing.T) {
	src := readWebFile(t, "web/js/terminal.js")

	if !strings.Contains(src, "function termFocusGuardBusy()") {
		t.Fatal("缺少 termFocusGuardBusy：焦点守卫没有「正在拖选/有选区就别抢焦点」的判断")
	}
	guard := jsFunctionBody(t, src, "function _refocusActiveTermInput() {")
	if !strings.Contains(guard, "termFocusGuardBusy()") {
		t.Errorf("_refocusActiveTermInput 没有先问一句是不是在选文本：\n  %s", strings.TrimSpace(guard))
	}
	focusin := jsHandlerBody(t, src, `document.addEventListener("focusin"`)
	if !strings.Contains(focusin, "termFocusGuardBusy()") {
		t.Errorf("focusin 焦点守卫没有给选区让路：\n  %s", strings.TrimSpace(focusin))
	}
}

func readWebFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := webFS.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// jsHandlerBody 取出 `xxx.addEventListener("evt"` 之后到下一个 `});` 的片段。
func jsHandlerBody(t *testing.T, src, marker string) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("找不到 %q", marker)
	}
	rest := src[i:]
	j := strings.Index(rest, "\n  });")
	if j < 0 {
		j = len(rest)
		if j > 800 {
			j = 800
		}
	}
	return rest[:j]
}

// jsFunctionBody 取出函数声明之后到下一个顶格 `}` 的片段。
func jsFunctionBody(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("找不到 %q", decl)
	}
	rest := src[i+len(decl):]
	j := strings.Index(rest, "\n}")
	if j < 0 {
		t.Fatalf("%q 的函数体没有闭合", decl)
	}
	return rest[:j]
}
