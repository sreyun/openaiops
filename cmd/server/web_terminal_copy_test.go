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
	//
	// 判断必须是 termFocusGuardBusy()，不能只看 _termDragging：松手之后拖拽标志已经落下，
	// 而选区还在——那一刻 <pre> 刚拿到焦点，转交过去高亮当场就没了。
	focusBody := jsHandlerBody(t, src, `screen.addEventListener("focus"`)
	if !strings.Contains(focusBody, "termFocusGuardBusy()") {
		t.Errorf("focus 转交没有给「正在拖选 / 已有选区」让路：\n  %s", strings.TrimSpace(focusBody))
	}
}

// 选区快照：复制这条链上任何一环都可能让"现场选区"读成空串（焦点被交给隐藏 textarea、
// 右键点在选区外、输出刷新换掉行 DOM）。所以选区还活着的时候就要存一份，复制动作用快照兜底。
//
// 两处抓取缺一不可：selectionchange 最及时但不是所有环境都发；document 上 **捕获阶段**
// 的 mouseup 排在所有会动焦点的处理器之前，拖选收尾那一刻选区必然还在。
func TestTerminalKeepsSelectionSnapshot(t *testing.T) {
	src := readWebFile(t, "web/js/terminal.js")

	for _, want := range []string{
		"function termCaptureSelection()",
		`document.addEventListener("selectionchange", termCaptureSelection)`,
		`document.addEventListener("mouseup", termCaptureSelection, true)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("选区快照缺了 %q —— 焦点一动选区就没，Ctrl+C 会当成「没选中」把 ^C 打进 shell", want)
		}
	}
	// 复制动作走带快照的 termSelectionText；判断"用户此刻是不是正选着"的地方走 live 版本。
	// 混用会出事：用户在页面别处复制时，终端会把上一次的选区塞进人家的剪贴板。
	if !strings.Contains(src, "function termLiveSelectionText(tab)") {
		t.Error("缺少 termLiveSelectionText：抢焦点判断与全局 copy 拦截必须只看现场选区")
	}
	copyHandler := jsHandlerBody(t, src, `document.addEventListener("copy"`)
	if !strings.Contains(copyHandler, "termLiveSelectionText(") {
		t.Errorf("全局 copy 拦截用了带快照的读法，会抢走页面别处的复制：\n  %s", strings.TrimSpace(copyHandler))
	}
}

// 右键：点在高亮上就直接复制（PuTTY / Windows Terminal 的老习惯，也是用户嘴里的"右击复制"）；
// 点在别处只弹菜单，不能把用户攒着准备粘贴的剪贴板冲掉。
func TestTerminalRightClickCopiesSelection(t *testing.T) {
	src := readWebFile(t, "web/js/terminal.js")

	if !strings.Contains(src, "function termPointInSelection(x, y)") {
		t.Fatal("缺少 termPointInSelection：无法区分「右键点在高亮上」和「点在别处」")
	}
	if !strings.Contains(src, "getClientRects") {
		t.Error("命中判断应基于选区自身的可视矩形（caretRangeFromPoint / caretPositionFromPoint 各内核不一致）")
	}
	menu := jsFunctionBody(t, src, "function showTermContextMenu(tab, e) {")
	if !strings.Contains(menu, "termPointInSelection(e.clientX, e.clientY)") || !strings.Contains(menu, "termCopyText(selText)") {
		t.Errorf("右键弹菜单时没有实现「点在高亮上即复制」：\n  %s", strings.TrimSpace(menu))
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
	if !strings.Contains(term, "function termCopyText") || !strings.Contains(term, "copyToClipboardOrPrompt(out)") {
		t.Error("终端复制应收敛到 termCopyText → copyToClipboardOrPrompt 一条路径")
	}
	// 工具栏那个看得见的复制按钮是"确定路径"：快捷键会被浏览器/输入法吃掉，
	// 右键菜单也不是所有环境都弹得出来，总得有一个点了必有结果的入口。
	if !strings.Contains(term, "function termCopyBtnClick()") {
		t.Error("终端标题栏缺少复制按钮的处理器 termCopyBtnClick")
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
	// 两条自动路都被挡住时不能只丢一句"复制失败"——用户要的那段输出就在屏幕上却拿不走。
	// 兜到底：弹一个内容已选中的只读框，按 Ctrl+C 就走。
	prompt := jsFunctionBody(t, core, "function copyToClipboardOrPrompt(text) {")
	if !strings.Contains(prompt, "showManualCopyDialog(out)") {
		t.Error("copyToClipboardOrPrompt 没有在两条自动路都失败时弹手动复制框")
	}
	manual := jsFunctionBody(t, core, "function showManualCopyDialog(text) {")
	if !strings.Contains(manual, "ta.select()") {
		t.Error("手动复制框必须把内容预先选中，否则用户还得自己拖一遍")
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
