package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aiops-monitor/shared"
)

func TestSearchResourcesMatchesHardwareFields(t *testing.T) {
	s := &Server{store: NewStore(), hw: newHardwareStore()}
	s.hw.put("host-1", "edge-node-01", "10.0.0.8", []shared.HardwareSnapshot{{
		TargetName: "idrac-prod",
		System: shared.RedfishSystem{
			Manufacturer: "Dell",
			Model:        "PowerEdge R740",
			SerialNumber: "SN-ABC-123",
		},
	}})

	results := s.searchResources(httptest.NewRequest(http.MethodGet, "/api/v1/resources/search?q=r740", nil), "r740", 20)
	if len(results) != 1 {
		t.Fatalf("expected one match, got %#v", results)
	}
	got := results[0]
	if got.Type != "hardware" || got.Name != "idrac-prod" || got.Host != "edge-node-01" ||
		got.Ref != "hardware:host-1/idrac-prod" || got.View != "hardware" {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestHandleResourceSearchEmptyQueryShape(t *testing.T) {
	s := &Server{store: NewStore()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/resources/search?q=&limit=20", nil)
	s.handleResourceSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != "{\"count\":0,\"query\":\"\",\"results\":[]}\n" {
		t.Fatalf("unexpected response: %s", got)
	}
}

// 全局搜索必须按主机授权切分结果。
//
// 修复前它完全不过滤：被限定在 host-a 的账号搜 "node"，能连 host-b 的主机名、IP，
// 以及 host-b 上的硬件型号/序列号一起拿到，还附带可直接跳转的 ref——等于绕开主机 RBAC
// 拿到一份全量资产清单。
func TestSearchResourcesRespectsHostScope(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg), hw: newHardwareStore()}
	_ = store.RegisterHost("host-a", "node-alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "node-beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	s.hw.put("host-b", "node-beta", "10.0.0.9", []shared.HardwareSnapshot{{
		TargetName: "node-secret-bmc",
		System:     shared.RedfishSystem{Manufacturer: "Dell", Model: "PowerEdge R750", SerialNumber: "SN-SECRET"},
	}})

	salt := genToken()[:16]
	cfg.cfg.Users = append(cfg.cfg.Users, AccountConfig{
		Username: "scoped", DisplayName: "Scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
		AllowedHostIDs: []string{"host-a"},
	})
	_ = cfg.save()
	tok := s.auth.issueSession("scoped")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resources/search?q=node", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	for _, got := range s.searchResources(req, "node", 50) {
		if got.HostID == "host-b" {
			t.Fatalf("越权结果泄露了 host-b 的资源：%#v", got)
		}
	}

	// 反向断言：授权内的主机必须仍然搜得到，别把过滤写成"一律不返回"。
	var sawAllowed bool
	for _, got := range s.searchResources(req, "node", 50) {
		if got.HostID == "host-a" {
			sawAllowed = true
		}
	}
	if !sawAllowed {
		t.Fatal("授权范围内的主机也被过滤掉了")
	}

	// 未受限的管理员照旧看到全部。
	adminSalt := genToken()[:16]
	cfg.cfg.Users = append(cfg.cfg.Users, AccountConfig{
		Username: "boss", DisplayName: "Boss", Role: RoleAdmin,
		Salt: adminSalt, Hash: hashPassword("Passw0rd!", adminSalt),
	})
	_ = cfg.save()
	adminReq := httptest.NewRequest(http.MethodGet, "/api/v1/resources/search?q=node", nil)
	adminReq.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.auth.issueSession("boss")})
	var sawB bool
	for _, got := range s.searchResources(adminReq, "node", 50) {
		if got.HostID == "host-b" {
			sawB = true
		}
	}
	if !sawB {
		t.Fatal("管理员不该被主机授权过滤")
	}
}
