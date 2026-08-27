package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newSSOTestServer(t *testing.T) *Server {
	t.Helper()
	cs, err := NewConfigStore(filepath.Join(t.TempDir(), "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.UserByName("admin"); !ok {
		_ = cs.CreateUser("admin", "Admin123!", "Admin", "", RoleAdmin)
	}
	return &Server{store: NewStore(), cfg: cs, auth: NewAuth(cs)}
}

func TestMapSSORoleDeptAndDefault(t *testing.T) {
	cfg := SSOProviderConfig{
		DefaultRole: RoleViewer,
		DeptRoleMap: map[string]string{"运维部": RoleOperator, "安全": RoleAdmin},
	}
	if got := mapSSORole([]string{"运维部"}, nil, cfg); got != RoleOperator {
		t.Fatalf("dept map = %q", got)
	}
	if got := mapSSORole(nil, nil, cfg); got != RoleViewer {
		t.Fatalf("default = %q", got)
	}
	cfg.DefaultRole = ""
	if got := mapSSORole(nil, nil, cfg); got != "" {
		t.Fatalf("deny = %q", got)
	}
}

func TestDeriveAndAllocateSSOUsername(t *testing.T) {
	s := newSSOTestServer(t)
	base := deriveSSOUsername(ssoProviderWechat, "张三", "ops@example.com", "openidABC123XYZ")
	if base != "ops" {
		t.Fatalf("email local = %q", base)
	}
	u1 := allocateSSOUsername(s.cfg, "admin")
	if u1 == "admin" {
		t.Fatal("should suffix when admin exists")
	}
	if sanitizeUsername(u1) == "" {
		t.Fatalf("invalid allocated username %q", u1)
	}
}

func TestProvisionSSOUserBindBySubject(t *testing.T) {
	s := newSSOTestServer(t)
	u1, err := s.provisionSSOUser(ssoProvisionReq{
		Provider: ssoProviderFeishu, Subject: "ou_abc", UsernameHint: "feishu_ops",
		DisplayName: "飞书运维", Email: "ops@ex.com", Role: RoleOperator, AutoCreate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.provisionSSOUser(ssoProvisionReq{
		Provider: ssoProviderFeishu, Subject: "ou_abc", UsernameHint: "other",
		DisplayName: "Again", Role: RoleViewer, AutoCreate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if u1 != u2 {
		t.Fatalf("same subject should reuse user: %s vs %s", u1, u2)
	}
	acc, ok := s.cfg.UserByIdentity(ssoProviderFeishu, "ou_abc")
	if !ok || acc.Username != u1 {
		t.Fatalf("identity lookup failed: %+v", acc)
	}
	if acc.Role != RoleViewer {
		t.Fatalf("role should update on re-login, got %s", acc.Role)
	}
}

func TestProvisionSSOUserRequiresAutoCreate(t *testing.T) {
	s := newSSOTestServer(t)
	_, err := s.provisionSSOUser(ssoProvisionReq{
		Provider: ssoProviderDingtalk, Subject: "ding_1", UsernameHint: "newbie",
		Role: RoleViewer, AutoCreate: false,
	})
	if err == nil {
		t.Fatal("expected error without auto_create")
	}
}

// An IdP user whose preferred_username / email local-part equals an existing
// local account must NOT silently bind to that account on login. Otherwise a
// stranger asserting hint "admin" takes over the unbound admin user.
func TestProvisionSSOUserRejectsUsernameCollisionTakeover(t *testing.T) {
	s := newSSOTestServer(t)
	if _, ok := s.cfg.UserByName("admin"); !ok {
		t.Fatal("fixture admin missing")
	}
	// AutoCreate off: collision must fail closed (not bind + session).
	_, err := s.provisionSSOUser(ssoProvisionReq{
		Provider: ssoProviderOIDC, Subject: "attacker-sub", UsernameHint: "admin",
		Email: "admin@evil.example", Role: RoleViewer, AutoCreate: false,
	})
	if err == nil {
		t.Fatal("expected reject when colliding with unbound local admin")
	}
	if _, ok := s.cfg.UserByIdentity(ssoProviderOIDC, "attacker-sub"); ok {
		t.Fatal("identity must not be bound to admin on failed provision")
	}
	// AutoCreate on: allocate a distinct username, never hijack admin.
	got, err := s.provisionSSOUser(ssoProvisionReq{
		Provider: ssoProviderOIDC, Subject: "attacker-sub", UsernameHint: "admin",
		Email: "admin@evil.example", Role: RoleViewer, AutoCreate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == "admin" {
		t.Fatal("must not return local admin username on IdP hint collision")
	}
	acc, ok := s.cfg.UserByIdentity(ssoProviderOIDC, "attacker-sub")
	if !ok || acc.Username == "admin" {
		t.Fatalf("identity bound to wrong user: %+v ok=%v", acc, ok)
	}
}

func TestSSOConfigMaskAndKeepSecret(t *testing.T) {
	s := newSSOTestServer(t)
	err := s.cfg.SetSSOConfig(SSOConfig{
		Feishu: SSOProviderConfig{Enabled: true, AppID: "cli_a", AppSecret: "secret-feishu", DefaultRole: RoleViewer, AutoCreate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	masked := maskSSOConfig(s.cfg.SSOConfig())
	if masked.Feishu.AppSecret != "****" {
		t.Fatalf("mask = %q", masked.Feishu.AppSecret)
	}
	// Keep secret when posting ****
	err = s.cfg.SetSSOConfig(SSOConfig{
		Feishu: SSOProviderConfig{Enabled: true, AppID: "cli_a", AppSecret: "****", DefaultRole: RoleOperator, AutoCreate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := s.cfg.SSOConfig()
	if got.Feishu.AppSecret != "secret-feishu" {
		t.Fatalf("secret not preserved: %q", got.Feishu.AppSecret)
	}
	if got.Feishu.DefaultRole != RoleOperator {
		t.Fatalf("role not updated: %q", got.Feishu.DefaultRole)
	}
}

func TestSSOLoginInfoAndDisabledProvider(t *testing.T) {
	s := newSSOTestServer(t)
	_ = s.cfg.SetSSOConfig(SSOConfig{
		Wechat: SSOProviderConfig{Enabled: true, AppID: "wx123", AppSecret: "sec", DefaultRole: RoleViewer},
	})
	rw := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/sso/info", nil)
	s.handleSSOLoginInfo(rw, req)
	if rw.Code != 200 {
		t.Fatalf("info status %d", rw.Code)
	}
	var body struct {
		Providers []struct {
			ID string `json:"id"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range body.Providers {
		if p.ID == ssoProviderWechat {
			found = true
		}
	}
	if !found {
		t.Fatalf("wechat missing: %+v", body.Providers)
	}

	rw2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/auth/feishu/login", nil)
	s.handleSSOLogin(rw2, req2)
	if rw2.Code != http.StatusFound {
		t.Fatalf("disabled feishu login = %d, want redirect", rw2.Code)
	}
	loc := rw2.Header().Get("Location")
	if !strings.Contains(loc, "sso_error=disabled") || !strings.Contains(loc, "sso_provider=feishu") {
		t.Fatalf("redirect loc = %q", loc)
	}
}

func TestRedirectSSOError(t *testing.T) {
	s := newSSOTestServer(t)
	rw := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/wechat/callback", nil)
	s.redirectSSOError(rw, req, ssoProviderWechat, "no_role")
	if rw.Code != http.StatusFound {
		t.Fatalf("status %d", rw.Code)
	}
	loc := rw.Header().Get("Location")
	if !strings.Contains(loc, "sso_error=no_role") || !strings.Contains(loc, "sso_provider=wechat") {
		t.Fatalf("loc=%q", loc)
	}
}

func TestSSOAuthorizeURLs(t *testing.T) {
	cfg := SSOProviderConfig{AppID: "app1"}
	u, err := ssoAuthorizeURL(ssoProviderWechat, cfg, "https://ops.example.com/api/v1/auth/wechat/callback", "st")
	if err != nil || !strings.Contains(u, "qrconnect") || !strings.Contains(u, "snsapi_login") {
		t.Fatalf("wechat url = %q err=%v", u, err)
	}
	u2, err := ssoAuthorizeURL(ssoProviderFeishu, cfg, "https://x/cb", "st")
	if err != nil || !strings.Contains(u2, "open.feishu.cn") || !strings.Contains(u2, "app_id=app1") {
		t.Fatalf("feishu url = %q err=%v", u2, err)
	}
	u3, err := ssoAuthorizeURL(ssoProviderWecom, SSOProviderConfig{AppID: "wwcorp", AgentID: "1000002"}, "https://ops.example.com/api/v1/auth/wecom/callback", "st")
	if err != nil || !strings.Contains(u3, "open.work.weixin.qq.com") || !strings.Contains(u3, "agentid=1000002") {
		t.Fatalf("wecom url = %q err=%v", u3, err)
	}
	if _, err := ssoAuthorizeURL(ssoProviderWecom, SSOProviderConfig{AppID: "wwcorp"}, "https://x/cb", "st"); err == nil {
		t.Fatal("wecom without agent_id should fail")
	}
}

func TestSSOWecomReadyAndBindUnbind(t *testing.T) {
	if ssoWecomReady(SSOProviderConfig{Enabled: true, AppID: "c", AppSecret: "s"}) {
		t.Fatal("wecom needs agent_id")
	}
	if !ssoWecomReady(SSOProviderConfig{Enabled: true, AppID: "c", AppSecret: "s", AgentID: "1"}) {
		t.Fatal("wecom should be ready")
	}
	s := newSSOTestServer(t)
	if err := s.cfg.CreateUser("ops1", "Password1!", "Ops", "", RoleOperator); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.BindUserIdentity("ops1", ssoProviderWecom, "ZhangSan"); err != nil {
		t.Fatal(err)
	}
	if u, ok := s.cfg.UserByIdentity(ssoProviderWecom, "ZhangSan"); !ok || u.Username != "ops1" {
		t.Fatalf("bind lookup failed: %+v %v", u, ok)
	}
	if err := s.cfg.UnbindUserIdentity("ops1", ssoProviderWecom); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.cfg.UserByIdentity(ssoProviderWecom, "ZhangSan"); ok {
		t.Fatal("expected unbound")
	}
}
