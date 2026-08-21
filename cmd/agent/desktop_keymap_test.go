package main

import "testing"

func TestDeskKeyToVKBasics(t *testing.T) {
	cases := map[string]int{
		"KeyA":         'A',
		"Digit5":       '5',
		"Space":        0x20,
		"Enter":        0x0D,
		"Escape":       0x1B,
		"Backspace":    0x08,
		"Tab":          0x09,
		"ArrowLeft":    0x25,
		"ArrowUp":      0x26,
		"ArrowRight":   0x27,
		"ArrowDown":    0x28,
		"F5":           0x74,
		"F12":          0x7B,
		"Minus":        0xBD,
		"Equal":        0xBB,
		"BracketLeft":  0xDB,
		"BracketRight": 0xDD,
		"Backslash":    0xDC,
		"Semicolon":    0xBA,
		"Quote":        0xDE,
		"Comma":        0xBC,
		"Period":       0xBE,
		"Slash":        0xBF,
		"Backquote":    0xC0,
		"MetaLeft":     0x5B,
		"MetaRight":    0x5C,
		"ControlLeft":  0x11,
		"ControlRight": 0xA3,
		"AltRight":     0xA5,
		"ShiftLeft":    0x10,
		"ShiftRight":   0xA1,
		"Numpad7":      0x67,
		"NumpadDivide": 0x6F,
		"Delete":       0x2E,
		"Insert":       0x2D,
		"Home":         0x24,
		"End":          0x23,
	}
	for code, want := range cases {
		if got := deskKeyToVK("", code); got != want {
			t.Fatalf("code %s: got 0x%X want 0x%X", code, got, want)
		}
	}
	if got := deskKeyToVK("a", ""); got != 'A' {
		t.Fatalf("key a: got 0x%X", got)
	}
	// Punctuation must NOT map to VK (0x24='$'' is VK_HOME — historic bug).
	if got := deskKeyToVK("$", "Unknown"); got != 0 {
		t.Fatalf("punctuation $ must not become VK, got 0x%X", got)
	}
	if got := deskKeyToVK("#", ""); got != 0 {
		t.Fatalf("punctuation # must not become VK, got 0x%X", got)
	}
	if r, ok := deskPrintableRune("@"); !ok || r != '@' {
		t.Fatalf("printable @: %v %v", r, ok)
	}
	if _, ok := deskPrintableRune("Backspace"); ok {
		t.Fatal("Backspace must not be printable")
	}
	if _, ok := deskPrintableRune("Delete"); ok {
		t.Fatal("Delete must not be printable")
	}
}

func TestDeskVKExtended(t *testing.T) {
	if !deskVKExtended(0x25) {
		t.Fatal("ArrowLeft should be extended")
	}
	if deskVKExtended('A') {
		t.Fatal("A should not be extended")
	}
}

// VkKeyScanExW 的返回值解码。
//
// 这段位运算是"远程桌面里打开 cmd 敲不进字"那条修复的核心：把字符还原成**真键**
// （VK + 需要的 Shift/Ctrl/Alt），再按扫描码注入，控制台/游戏才收得到——
// KEYEVENTF_UNICODE 注入的 VK=0 事件只有 GUI 程序认。错一位就会变成"打 a 出 A"。
func TestDeskVkScanState(t *testing.T) {
	cases := []struct {
		name                   string
		res                    uint16
		vk                     int
		shift, ctrl, alt, want bool
	}{
		{"小写 a：VK_A 不带修饰", 0x0041, 0x41, false, false, false, true},
		{"大写 A：VK_A + Shift", 0x0141, 0x41, true, false, false, true},
		{"Ctrl 组合", 0x0241, 0x41, false, true, false, true},
		{"AltGr = Ctrl+Alt", 0x0641, 0x41, false, true, true, true},
		{"当前布局打不出：-1", 0xFFFF, 0, false, false, false, false},
		{"VK 为 0 视为无效", 0x0100, 0, false, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vk, shift, ctrl, alt, ok := deskVkScanState(c.res)
			if ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
			if !c.want {
				return
			}
			if vk != c.vk || shift != c.shift || ctrl != c.ctrl || alt != c.alt {
				t.Fatalf("got vk=%#x shift=%v ctrl=%v alt=%v, want vk=%#x shift=%v ctrl=%v alt=%v",
					vk, shift, ctrl, alt, c.vk, c.shift, c.ctrl, c.alt)
			}
		})
	}
}
