package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// 端口转发规则是一条通往某台主机的网络通路：改到别的主机、复制一份、把停用的规则
// 重新打开，等价于给自己开一条到那台机器的隧道。此前只有「新建」检查了主机授权，
// 编辑 / 删除 / 启停 / 复制 / 整组操作全都没检查——被主机组授权限制住的 operator
// 可以照样操作范围外主机的规则，列表里也照样看得见。
func TestForwardRulesRespectHostScope(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), forward: newForwardManager(cfg)}

	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	for _, h := range []struct{ id, name, fp string }{
		{"host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	} {
		_, _ = store.UpsertAuthenticated(shared.Report{HostID: h.id, Hostname: h.name, Fingerprint: h.fp}, h.fp)
	}

	salt := genToken()[:16]
	cfg.cfg.Users = append(cfg.cfg.Users, AccountConfig{
		Username: "scoped", DisplayName: "Scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
		AllowedHostIDs: []string{"host-a"},
	})
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	tok := s.auth.issueSession("scoped")
	withSession := func(req *http.Request) *http.Request {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		return req
	}

	ruleA, err := s.forward.createRule("host-a", "alpha", 3306, 0, "127.0.0.1", "tcp", "", "op", "", false, nil)
	if err != nil {
		t.Fatalf("create rule A: %v", err)
	}
	ruleB, err := s.forward.createRule("host-b", "beta", 3307, 0, "127.0.0.1", "tcp", "", "op", "", false, nil)
	if err != nil {
		t.Fatalf("create rule B: %v", err)
	}
	t.Cleanup(func() {
		s.forward.removeRule(ruleA.id)
		s.forward.removeRule(ruleB.id)
	})

	call := func(method, path, body, id string, h func(http.ResponseWriter, *http.Request)) int {
		rr := httptest.NewRecorder()
		req := withSession(httptest.NewRequest(method, path, strings.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", id)
		h(rr, req)
		return rr.Code
	}

	// 范围外主机的规则：删 / 启停 / 复制 / 改，一律 403
	if code := call(http.MethodPut, "/api/v1/forward/x/toggle", `{"enabled":false}`, ruleB.id, s.handleForwardToggle); code != http.StatusForbidden {
		t.Fatalf("toggle 范围外规则: want 403 got %d", code)
	}
	if code := call(http.MethodPost, "/api/v1/forward/x/copy", `{}`, ruleB.id, s.handleForwardCopy); code != http.StatusForbidden {
		t.Fatalf("copy 范围外规则: want 403 got %d", code)
	}
	if code := call(http.MethodPut, "/api/v1/forward/x", `{"target_port":3307}`, ruleB.id, s.handleForwardEdit); code != http.StatusForbidden {
		t.Fatalf("edit 范围外规则: want 403 got %d", code)
	}
	if code := call(http.MethodDelete, "/api/v1/forward/x", "", ruleB.id, s.handleForwardDelete); code != http.StatusForbidden {
		t.Fatalf("delete 范围外规则: want 403 got %d", code)
	}
	if s.forward.getRule(ruleB.id) == nil {
		t.Fatal("范围外的规则被删掉了")
	}

	// 自己有权的规则改到范围外主机，同样要被挡下——否则等于换个方向开隧道。
	if code := call(http.MethodPut, "/api/v1/forward/x", `{"host_id":"host-b","target_port":3306}`, ruleA.id, s.handleForwardEdit); code != http.StatusForbidden {
		t.Fatalf("把自己的规则改到范围外主机: want 403 got %d", code)
	}
	if got := s.forward.getRule(ruleA.id).hostID; got != "host-a" {
		t.Fatalf("规则的主机被改掉了: %s", got)
	}

	// 列表只应看到授权范围内的规则
	rr := httptest.NewRecorder()
	s.handleForwardList(rr, withSession(httptest.NewRequest(http.MethodGet, "/api/v1/forward", nil)))
	body := rr.Body.String()
	if !strings.Contains(body, "host-a") {
		t.Fatalf("列表里应该有 host-a: %s", body)
	}
	if strings.Contains(body, "host-b") {
		t.Fatalf("列表泄露了范围外主机: %s", body)
	}

	// 自己范围内的规则照常可以操作
	if code := call(http.MethodPut, "/api/v1/forward/x/toggle", `{"enabled":false}`, ruleA.id, s.handleForwardToggle); code != http.StatusOK {
		t.Fatalf("toggle 自己的规则: want 200 got %d", code)
	}
}

// 清除告警状态是写操作：主机授权受限的账号不能清掉范围外主机的告警。
func TestAlertClearRespectsHostScope(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg)}
	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	salt := genToken()[:16]
	cfg.cfg.Users = append(cfg.cfg.Users, AccountConfig{
		Username: "scoped", DisplayName: "Scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
		AllowedHostIDs: []string{"host-a"},
	})
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	tok := s.auth.issueSession("scoped")

	post := func(hostID string) int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/clear",
			strings.NewReader(`{"host_id":"`+hostID+`","type":"cpu","scope":""}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		s.handleAlertClear(rr, req)
		return rr.Code
	}
	if code := post("host-b"); code != http.StatusForbidden {
		t.Fatalf("清除范围外主机的告警: want 403 got %d", code)
	}
	if code := post("host-a"); code != http.StatusOK {
		t.Fatalf("清除自己主机的告警: want 200 got %d", code)
	}
}
