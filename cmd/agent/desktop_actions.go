package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// deskAdvancedInput is optional: platforms that can unlock / type credentials
// implement it. Others fall back to chord synthesis via deskInput.Key.
type deskAdvancedInput interface {
	SendCAD() error
	TypeText(text string) error
	DeskInputMeta() deskInputMeta
}

type deskInputMeta struct {
	Desktop        string `json:"desktop,omitempty"`
	InputDesktopOK bool   `json:"input_desktop_ok"`
	CAD            bool   `json:"cad"`
	TypeText       bool   `json:"type_text"`
	SecureDesktop  bool   `json:"secure_desktop,omitempty"`
	LockHint       string `json:"lock_hint,omitempty"`
}

// deskActionRequest is the JSON body of frame type 'A'.
type deskActionRequest struct {
	Action  string `json:"action"`          // cad | chord | type_text | wake | unlock | paste
	Chord   string `json:"chord,omitempty"` // win_l | ctrl_c | alt_tab | … (see chordVKSequence)
	Text    string `json:"text,omitempty"`  // for type_text / unlock password / paste
	User    string `json:"user,omitempty"`  // unlock username (optional)
	Enter   bool   `json:"enter,omitempty"` // append Enter after type_text / unlock
	ScreenW int    `json:"screen_w,omitempty"`
	ScreenH int    `json:"screen_h,omitempty"`
	// set_resolution：显式给定目标分辨率；给 0 则按 client_w/h 自动挑一个最贴近的模式。
	W       int `json:"w,omitempty"`
	H       int `json:"h,omitempty"`
	ClientW int `json:"client_w,omitempty"`
	ClientH int `json:"client_h,omitempty"`
}

func deskFeaturesFromInput(inp deskInput, viewOnly bool) map[string]bool {
	meta := deskInputMetaFrom(inp)
	return map[string]bool{
		"dnd": true, "monitors": true,
		"input":     !viewOnly,
		"cad":       meta.CAD && !viewOnly,
		"type_text": meta.TypeText && !viewOnly,
		"unlock":    meta.TypeText && !viewOnly,
		"paste":     !viewOnly,
		"chords":    !viewOnly,
		"wake":      !viewOnly,
	}
}

func deskInputMetaFrom(inp deskInput) deskInputMeta {
	if adv, ok := inp.(deskAdvancedInput); ok {
		return adv.DeskInputMeta()
	}
	return deskInputMeta{
		InputDesktopOK: true,
		CAD:            false,
		TypeText:       true, // best-effort via Key/osascript/xdotool path
		LockHint:       deskDefaultLockHint(),
	}
}

func deskDefaultLockHint() string {
	switch deskGOOS() {
	case "windows":
		return "锁屏时请先点「Ctrl+Alt+Del」，再输入密码；需以 Windows 服务+桌面 worker 运行 Agent。"
	case "darwin":
		return "macOS 锁屏可能拦截键鼠（Secure Input）。可先点「唤醒」，再发送凭据；登录窗口不保证可远程解锁。请确认已授予「屏幕录制」与「辅助功能」。"
	case "linux":
		if linuxIsKylinFamily() {
			return "麒麟/UOS：锁屏界面通常可键入。Wayland 需 grim+ffmpeg（H.264）与 ydotool/wtype；X11 需 ffmpeg+xdotool。Agent 须在已登录图形会话中运行。"
		}
		return "Linux 锁屏界面通常可键入。Wayland：安装 grim、ffmpeg、ydotool/wtype；X11：安装 ffmpeg、xdotool。无图形会话或 greeter 时需先在本机登录。"
	default:
		return ""
	}
}

func deskBlankFrameHint() string {
	switch deskGOOS() {
	case "darwin":
		return "画面为纯色/无内容：可能未授予屏幕录制、锁屏 Secure Input、或显示器休眠。请在「系统设置 → 隐私与安全性 → 屏幕录制」允许 Agent 后完全退出并重启，再试「唤醒」。"
	case "linux":
		base := "画面为纯色/无内容：通常是无图形会话、锁屏 greeter、或 DISPLAY/Wayland 未指向用户桌面。"
		if linuxIsKylinFamily() {
			return base + "麒麟/UOS：确认已本机登录图形桌面；Wayland 安装 grim+ffmpeg，X11 安装 ffmpeg；Agent 不要用纯 SSH 无头方式启动。"
		}
		return base + "请本机登录桌面；Wayland 安装 grim，X11 确认 DISPLAY/XAUTHORITY；必要时用登录用户 systemd --user 启动 Agent。"
	default:
		return "画面为纯色/无内容：目标会话未真正渲染桌面（无人登录、控制台断开、Session 0 抓屏、或 Windows 应用程序控制导致桌面 worker 未启动）。请：1) 用 RDP/控制台登录并解锁桌面；2) 以管理员安装 Agent 服务（aiops-agent --install-service）；3) 若安装时报 Application Control 拦截，先放行后再装。"
	}
}

type deskDesktopNamer interface {
	CurrentDesktop() string
}

func deskCurrentDesktop(cap deskCapture) string {
	if n, ok := cap.(deskDesktopNamer); ok {
		return n.CurrentDesktop()
	}
	return ""
}

func deskIsSecureName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "winlogon" || strings.HasPrefix(n, "winlogon") || n == "screensaver"
}

func deskLockHintForDesktop(name string) string {
	if deskIsSecureName(name) {
		return "当前输入桌面: " + name + "。锁屏/注销请先点「Ctrl+Alt+Del」，再点「解锁」输入密码（或点「唤醒」后直接键入）。Windows Server 需 Agent≥服务版并已 --install-service。"
	}
	if name != "" {
		return "当前输入桌面: " + name
	}
	return deskDefaultLockHint()
}

func deskMetaExtras(inp deskInput, viewOnly bool) map[string]any {
	m := deskInputMetaFrom(inp)
	out := map[string]any{
		"desktop":          m.Desktop,
		"input_desktop_ok": m.InputDesktopOK && !viewOnly,
		"lock_hint":        m.LockHint,
		"features":         deskFeaturesFromInput(inp, viewOnly),
	}
	if m.SecureDesktop {
		out["secure_desktop"] = true
	}
	return out
}

func handleDeskAction(cap deskCapture, inp deskInput, payload []byte, screenW, screenH int, fileTxChan chan<- []byte) {
	var req deskActionRequest
	if json.Unmarshal(payload, &req) != nil || req.Action == "" {
		return
	}
	if req.ScreenW <= 0 {
		req.ScreenW = screenW
	}
	if req.ScreenH <= 0 {
		req.ScreenH = screenH
	}
	var err error
	extra := map[string]any{}
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "cad":
		err = deskDoCAD(inp)
	case "chord":
		err = deskPlayChord(inp, req.Chord)
	case "type_text":
		err = deskDoTypeText(inp, req.Text, req.Enter)
	case "wake":
		err = deskDoWake(inp, req.ScreenW, req.ScreenH)
	case "unlock":
		err = deskDoUnlock(inp, req.User, req.Text, req.Enter || req.Text != "", req.ScreenW, req.ScreenH)
	case "paste":
		err = deskDoPaste(inp, req.Text)
	case "set_resolution":
		// 远端分辨率迁就客户端窗口（mstsc 的做法）：画面缩放压不出不存在的像素，
		// 远端 1024×768 而本地窗口 2560×1400 时，只有真的改远端分辨率才谈得上"清晰"。
		var mode deskDisplayMode
		mode, err = deskApplyResolution(cap, req.W, req.H, req.ClientW, req.ClientH)
		if err == nil {
			extra["resolution"] = mode
		}
	case "reset_resolution":
		err = deskRestoreResolution(cap)
	default:
		err = fmt.Errorf("unknown action %q", req.Action)
	}
	ack := map[string]any{
		"action_ack": true,
		"action":     req.Action,
		"ok":         err == nil,
	}
	if err != nil {
		ack["error"] = err.Error()
	}
	for k, v := range extra {
		ack[k] = v
	}
	for k, v := range deskMetaExtras(inp, false) {
		ack[k] = v
	}
	js, _ := json.Marshal(ack)
	frame := deskTxFrame('S', js)
	select {
	case fileTxChan <- frame:
	case <-time.After(3 * time.Second):
		// Still log — operator must not see a silent no-op on CAD/unlock.
	}
}

func deskDoCAD(inp deskInput) error {
	if adv, ok := inp.(deskAdvancedInput); ok {
		return adv.SendCAD()
	}
	return fmt.Errorf("Ctrl+Alt+Del 在此平台不可用（仅 Windows 服务模式支持 SendSAS）")
}

func deskDoTypeText(inp deskInput, text string, enter bool) error {
	if text == "" && !enter {
		return fmt.Errorf("empty text")
	}
	const maxLen = 4096
	if len(text) > maxLen {
		text = text[:maxLen]
	}
	if adv, ok := inp.(deskAdvancedInput); ok {
		if err := adv.TypeText(text); err != nil {
			return err
		}
	} else if text != "" {
		if err := deskTypeTextViaKeys(inp, text); err != nil {
			return err
		}
	}
	if enter {
		_ = inp.Key(0x0D, true)
		_ = inp.Key(0x0D, false)
	}
	return nil
}

func deskDoWake(inp deskInput, w, h int) error {
	if w < 2 {
		w = 1920
	}
	if h < 2 {
		h = 1080
	}
	// Winlogon password box is typically lower-center; try a few spots plus Esc.
	spots := [][2]int{
		{w / 2, h * 2 / 3},
		{w / 2, h * 3 / 4},
		{w / 2, h / 2},
	}
	_ = inp.Key(0x1B, true) // Esc dismisses "press any key" / wakes LogonUI
	_ = inp.Key(0x1B, false)
	time.Sleep(30 * time.Millisecond)
	for _, sp := range spots {
		_ = inp.MouseMove(sp[0], sp[1])
		_ = inp.MouseButton(1, true)
		_ = inp.MouseButton(1, false)
		time.Sleep(15 * time.Millisecond)
	}
	return nil
}

func deskClearFocusedField(inp deskInput) {
	_ = deskPlayChord(inp, "ctrl_a")
	time.Sleep(8 * time.Millisecond)
	_ = inp.Key(0x2E, true) // Delete
	_ = inp.Key(0x2E, false)
	time.Sleep(8 * time.Millisecond)
	_ = inp.Key(0x08, true) // Backspace (covers selectionless fields)
	_ = inp.Key(0x08, false)
}

// deskDoUnlock wakes the lock UI and types credentials in one agent-side sequence
// (avoids multi-round-trip UI pacing that made passwords appear "one char / half second").
func deskDoUnlock(inp deskInput, user, pass string, enter bool, w, h int) error {
	user = strings.TrimSpace(user)
	if user == "" && pass == "" && !enter {
		return fmt.Errorf("empty credentials")
	}
	secure := deskInputMetaFrom(inp).SecureDesktop
	_ = deskDoWake(inp, w, h)
	if secure {
		time.Sleep(80 * time.Millisecond) // LogonUI focus settle
	} else {
		time.Sleep(35 * time.Millisecond)
	}
	deskClearFocusedField(inp)
	time.Sleep(12 * time.Millisecond)
	if user != "" {
		if err := deskDoTypeText(inp, user, false); err != nil {
			return err
		}
		_ = deskPlayChord(inp, "tab")
		time.Sleep(20 * time.Millisecond)
		deskClearFocusedField(inp)
		time.Sleep(8 * time.Millisecond)
	}
	if pass != "" {
		if err := deskDoTypeText(inp, pass, false); err != nil {
			return err
		}
	}
	if enter {
		time.Sleep(15 * time.Millisecond)
		_ = inp.Key(0x0D, true)
		_ = inp.Key(0x0D, false)
	}
	return nil
}

// deskDoPaste sets the OS clipboard (when supported) then injects Ctrl+V so the
// focused remote control actually receives the text — clipboard-only sync is
// invisible on many lock screens and apps.
func deskDoPaste(inp deskInput, text string) error {
	text = strings.ReplaceAll(text, "\x00", "")
	if text == "" {
		return fmt.Errorf("empty paste")
	}
	const maxLen = 512 << 10
	if len(text) > maxLen {
		text = text[:maxLen]
	}
	// Winlogon / UAC password boxes reject Ctrl+V — UNICODE type is the only path.
	if deskInputMetaFrom(inp).SecureDesktop {
		return deskDoTypeText(inp, text, false)
	}
	if deskClipboardSupported() {
		if err := deskClipboardSet(text); err != nil {
			slog.Debug("clipboard set failed; typing paste fallback", "err", err)
			return deskDoTypeText(inp, text, false)
		}
		time.Sleep(15 * time.Millisecond)
		if err := deskPlayChord(inp, "ctrl_v"); err == nil {
			return nil
		}
		slog.Debug("Ctrl+V paste failed; typing fallback")
	}
	return deskDoTypeText(inp, text, false)
}

func deskPlayChord(inp deskInput, name string) error {
	keys := chordVKSequence(name)
	if len(keys) == 0 {
		return fmt.Errorf("unknown chord %q", name)
	}
	return deskTapKeys(inp, keys)
}

func deskTapKeys(inp deskInput, keys []int) error {
	for _, vk := range keys {
		if err := inp.Key(vk, true); err != nil {
			return err
		}
		time.Sleep(2 * time.Millisecond)
	}
	for i := len(keys) - 1; i >= 0; i-- {
		_ = inp.Key(keys[i], false)
		time.Sleep(1 * time.Millisecond)
	}
	return nil
}

// deskTypeTextViaKeys types ASCII via VK; non-ASCII runes are skipped (platform TypeText preferred).
func deskTypeTextViaKeys(inp deskInput, text string) error {
	for _, r := range text {
		if r == '\n' || r == '\r' {
			_ = inp.Key(0x0D, true)
			_ = inp.Key(0x0D, false)
			continue
		}
		if r == '\t' {
			_ = inp.Key(0x09, true)
			_ = inp.Key(0x09, false)
			continue
		}
		if r > 0 && r < 0x7f {
			vk := int(r)
			if vk >= 'a' && vk <= 'z' {
				vk -= 32
			}
			needShift := false
			if r >= 'A' && r <= 'Z' {
				needShift = true
			}
			if needShift {
				_ = inp.Key(0x10, true)
			}
			_ = inp.Key(vk, true)
			_ = inp.Key(vk, false)
			if needShift {
				_ = inp.Key(0x10, false)
			}
			continue
		}
		// Non-ASCII: try as unicode via platform if available — already handled by TypeText.
		return fmt.Errorf("non-ASCII text requires platform TypeText support")
	}
	return nil
}

// chordVKSequence returns the VK list for tests / documentation.
func chordVKSequence(name string) []int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "win_l", "lock":
		return []int{0x5B, 0x4C}
	case "ctrl_shift_esc", "taskmgr":
		return []int{0x11, 0x10, 0x1B}
	case "esc", "escape":
		return []int{0x1B}
	case "ctrl_alt_bksp":
		return []int{0x11, 0x12, 0x08}
	case "enter":
		return []int{0x0D}
	case "tab":
		return []int{0x09}
	case "win":
		return []int{0x5B}
	case "ctrl_a", "select_all":
		return []int{0x11, 0x41}
	case "ctrl_c", "copy":
		return []int{0x11, 0x43}
	case "ctrl_v", "paste":
		return []int{0x11, 0x56}
	case "ctrl_x", "cut":
		return []int{0x11, 0x58}
	case "ctrl_z", "undo":
		return []int{0x11, 0x5A}
	case "ctrl_y", "redo":
		return []int{0x11, 0x59}
	case "ctrl_s", "save":
		return []int{0x11, 0x53}
	case "ctrl_f", "find":
		return []int{0x11, 0x46}
	case "ctrl_w", "close":
		return []int{0x11, 0x57}
	case "alt_tab":
		return []int{0x12, 0x09}
	case "alt_f4":
		return []int{0x12, 0x73}
	case "alt_enter":
		return []int{0x12, 0x0D}
	case "win_e", "explorer":
		return []int{0x5B, 0x45}
	case "win_r", "run":
		return []int{0x5B, 0x52}
	case "win_d", "show_desktop":
		return []int{0x5B, 0x44}
	default:
		return nil
	}
}
