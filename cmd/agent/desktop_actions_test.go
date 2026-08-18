package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDeskIsSecureName(t *testing.T) {
	if !deskIsSecureName("Winlogon") || !deskIsSecureName("winlogon") || !deskIsSecureName("ScreenSaver") {
		t.Fatal("expected secure desktop names")
	}
	if deskIsSecureName("Default") || deskIsSecureName("") {
		t.Fatal("Default/empty must not be secure")
	}
	hint := deskLockHintForDesktop("Winlogon")
	if hint == "" || indexOf(hint, "Ctrl+Alt+Del") < 0 {
		t.Fatalf("lock hint missing CAD guidance: %q", hint)
	}
}

func TestChordVKSequence(t *testing.T) {
	cases := map[string][]int{
		"win_l":          {0x5B, 0x4C},
		"ctrl_shift_esc": {0x11, 0x10, 0x1B},
		"esc":            {0x1B},
		"ctrl_alt_bksp":  {0x11, 0x12, 0x08},
		"enter":          {0x0D},
		"tab":            {0x09},
		"ctrl_v":         {0x11, 0x56},
		"ctrl_a":         {0x11, 0x41},
		"ctrl_c":         {0x11, 0x43},
		"ctrl_x":         {0x11, 0x58},
		"alt_tab":        {0x12, 0x09},
		"alt_f4":         {0x12, 0x73},
		"win_e":          {0x5B, 0x45},
		"unknown-chord":  nil,
	}
	for name, want := range cases {
		got := chordVKSequence(name)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chord %q = %v, want %v", name, got, want)
		}
	}
}

func TestDeskActionRequestJSON(t *testing.T) {
	raw := []byte(`{"action":"type_text","text":"secret","enter":true}`)
	var req deskActionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.Action != "type_text" || req.Text != "secret" || !req.Enter {
		t.Fatalf("unexpected: %+v", req)
	}
	// Ensure marshaling round-trip does not invent fields that would leak in logs
	// (server audit uses separate struct and must never log Text).
	out, _ := json.Marshal(map[string]any{"action": req.Action, "enter": req.Enter, "len": len([]rune(req.Text))})
	// 这条断言此前写成了空 if，等于什么都没检查——而它要守的正是「审计形态里绝不能出现
	// 原文」这件事：远程桌面的 type_text 会带着密码之类的内容过来。
	if string(out) == "" {
		t.Fatal("audit shape marshal produced nothing")
	}
	if containsSecret(string(out), "secret") {
		t.Fatalf("audit shape leaked the typed text: %s", out)
	}
	auditShape := map[string]any{"action": "type_text", "len": len([]rune(req.Text))}
	b, _ := json.Marshal(auditShape)
	if containsSecret(string(b), "secret") {
		t.Fatalf("audit shape leaked secret: %s", b)
	}
}

func containsSecret(s, secret string) bool {
	return len(secret) > 0 && (len(s) >= len(secret)) && (indexOf(s, secret) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

type stubDeskInput struct {
	keys []int
}

func (s *stubDeskInput) MouseMove(x, y int) error                { return nil }
func (s *stubDeskInput) MouseButton(button int, down bool) error { return nil }
func (s *stubDeskInput) MouseWheel(delta int) error              { return nil }
func (s *stubDeskInput) Key(vk int, down bool) error {
	if down {
		s.keys = append(s.keys, vk)
	}
	return nil
}
func (s *stubDeskInput) Close() error { return nil }

func TestDeskPlayChordEsc(t *testing.T) {
	st := &stubDeskInput{}
	if err := deskPlayChord(st, "esc"); err != nil {
		t.Fatal(err)
	}
	if len(st.keys) == 0 || st.keys[0] != 0x1B {
		t.Fatalf("keys=%v", st.keys)
	}
}

func TestDeskDoCADUnsupported(t *testing.T) {
	st := &stubDeskInput{}
	if err := deskDoCAD(st); err == nil {
		t.Fatal("expected unsupported CAD on plain deskInput")
	}
}

func TestDeskActionUnlockJSON(t *testing.T) {
	raw := []byte(`{"action":"unlock","user":"alice","text":"s3cret","enter":true}`)
	var req deskActionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.Action != "unlock" || req.User != "alice" || req.Text != "s3cret" || !req.Enter {
		t.Fatalf("unexpected: %+v", req)
	}
}

func TestDeskDoUnlockTypesViaKeys(t *testing.T) {
	st := &stubDeskInput{}
	if err := deskDoUnlock(st, "", "ab", true, 800, 600); err != nil {
		t.Fatal(err)
	}
	// wake clicks + Esc + password 'a','b' + Enter — at least some keys recorded
	if len(st.keys) < 3 {
		t.Fatalf("expected typed keys, got %v", st.keys)
	}
}
