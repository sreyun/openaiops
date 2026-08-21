package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestAuditDeskActionNoPasswordLeak(t *testing.T) {
	s := &Server{store: NewStore()}
	payload, _ := json.Marshal(map[string]any{
		"action": "type_text",
		"text":   "P@ssw0rd-超级机密",
		"enter":  true,
	})
	if !auditDeskAction(s, "alice", "1.2.3.4", "hmsrv18", payload) {
		t.Fatal("expected audit to accept type_text")
	}
	logs := s.store.RecentActivity()
	if len(logs) == 0 {
		t.Fatal("no log written")
	}
	msg := logs[0].Message
	if strings.Contains(msg, "P@ssw0rd") || strings.Contains(msg, "超级机密") {
		t.Fatalf("password leaked into audit log: %q", msg)
	}
	if !strings.Contains(msg, "type_text") || !strings.Contains(msg, "长度=") {
		t.Fatalf("unexpected audit message: %q", msg)
	}
}

func TestAuditDeskActionCAD(t *testing.T) {
	s := &Server{store: NewStore()}
	payload, _ := json.Marshal(map[string]any{"action": "cad"})
	if !auditDeskAction(s, "bob", "10.0.0.1", "host1", payload) {
		t.Fatal("cad should be accepted")
	}
	if auditDeskAction(s, "bob", "10.0.0.1", "host1", []byte(`{"action":"nope"}`)) {
		t.Fatal("unknown action should be rejected")
	}
}

// UI 里发得出的每一个远程桌面动作，服务端都必须认。
//
// 这条守的是一类**静默失效**：'A' 帧的动作名不在白名单里就被服务端直接丢掉，既不转发也
// 不报错。界面上的「解锁」按钮就这么哑过——它发的是 unlock，而白名单里只有
// cad/chord/type_text/wake，于是点了毫无反应，日志里也查不到任何痕迹。
// 用源码里的字面量对照白名单，谁再加动作忘了同步，这里就红。
func TestDeskActionsFromUIAreAudited(t *testing.T) {
	files := []string{"web/js/desktop.js"}
	re := regexp.MustCompile(`action:\s*"([a-z_]+)"`)
	// 鼠标帧（'M'）也用 action 字段，但它们走的不是 'A' 白名单这条路。
	mouse := map[string]bool{"move": true, "down": true, "up": true, "click": true, "desktop": true}
	found := map[string]bool{}
	for _, f := range files {
		src := readWebFile(t, f)
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			if !mouse[m[1]] {
				found[m[1]] = true
			}
		}
	}
	if len(found) == 0 {
		t.Fatal("没有从界面源码里解析出任何远程桌面动作，用例本身失效了")
	}
	for act := range found {
		if !deskAuditedActions[act] {
			t.Errorf("界面会发送 %q，但服务端白名单不认它——这个按钮点了会毫无反应", act)
		}
	}
}

// 解锁/粘贴的凭据绝不能进日志：审计要记"发生过"，不记"发了什么"。
func TestDeskActionAuditNeverLogsCredentials(t *testing.T) {
	srv, _ := newTestServer(t)
	secret := "Sup3rSecret!"
	for _, act := range []string{"unlock", "type_text", "paste"} {
		payload := []byte(`{"action":"` + act + `","user":"admin","text":"` + secret + `","enter":true}`)
		if !auditDeskAction(srv, "op", "1.1.1.1", "host1", payload) {
			t.Fatalf("%s 被白名单挡住了", act)
		}
	}
	for _, e := range srv.store.RecentActivity() {
		if strings.Contains(e.Message, secret) {
			t.Fatalf("审计日志里出现了凭据原文：%s", e.Message)
		}
	}
}
