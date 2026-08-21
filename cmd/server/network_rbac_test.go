package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aiops-monitor/shared"
)

// 网络与硬件的读接口同样受主机授权约束。
//
// 它们全都以 ?host=<id> 取数，之前一个都没有校验：受限账号把 host 换成别人的 ID
// 就能拿到那台机器的流量五元组、SNMP 设备清单、BMC 序列号与硬件事件。
// 这些恰恰是"看不到主机"也照样有价值的情报。
func TestNetworkAndHardwareReadsRespectHostScope(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	s := &Server{cfg: cfg, store: store, auth: NewAuth(cfg)}
	_ = store.RegisterHost("host-a", "alpha", "fp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_ = store.RegisterHost("host-b", "beta", "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	_, _ = store.UpsertAuthenticated(shared.Report{
		HostID: "host-b", Hostname: "beta", Fingerprint: "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}, "fp-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	salt := genToken()[:16]
	cfg.cfg.Users = append(cfg.cfg.Users, AccountConfig{
		Username: "scoped", DisplayName: "Scoped", Role: RoleOperator,
		Salt: salt, Hash: hashPassword("Passw0rd!", salt),
		AllowedHostIDs: []string{"host-a"},
	})
	_ = cfg.save()
	tok := s.auth.issueSession("scoped")

	cases := []struct {
		name    string
		url     string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"硬件健康", "/api/v1/hardware/health?host=host-b", s.handleHardwareHealth},
		{"硬件事件", "/api/v1/hardware/events?host=host-b", s.handleHardwareEvents},
		{"硬件历史", "/api/v1/hardware/history?host=host-b&metric=temperature", s.handleHardwareHistory},
		{"流量汇总", "/api/v1/netflow/summary?host=host-b", s.handleNetFlowSummary},
		{"流量明细", "/api/v1/netflow/flows?host=host-b", s.handleNetFlowFlows},
		{"流量包速率", "/api/v1/netflow/packets?host=host-b", s.handleNetFlowPackets},
		{"单 IP 历史", "/api/v1/netflow/ip-history?host=host-b&ip=10.0.0.1", s.handleNetFlowIPHistory},
		{"SNMP 设备", "/api/v1/snmp/list?host=host-b", s.handleSNMPList},
		{"SNMP trap", "/api/v1/snmp/traps?host=host-b", s.handleSNMPTraps},
		{"SNMP 接口历史", "/api/v1/snmp/if-history?host=host-b&ifindex=1", s.handleSNMPInterfaceHistory},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, c.url, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
			c.handler(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("越权访问 host-b 没有被拒：状态 %d，响应 %s", rr.Code, rr.Body.String())
			}
		})
	}

	// 反向断言：授权内的主机不能被误伤（这里 host-a 没有数据，但**不能是 403**）。
	for _, c := range cases {
		rr := httptest.NewRecorder()
		url := c.url
		for i := 0; i+6 <= len(url); i++ {
			if url[i:i+6] == "host-b" {
				url = url[:i] + "host-a" + url[i+6:]
				break
			}
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		c.handler(rr, req)
		if rr.Code == http.StatusForbidden {
			t.Fatalf("%s：授权内的 host-a 被误拒", c.name)
		}
	}
}
