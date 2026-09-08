package main

import (
	"net"
	"path/filepath"
	"testing"
)

func TestForwardListenNeedsWhitelist(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", false},
		{"localhost", false},
		{"LOCALHOST", false},
		{"::1", false},
		{"[::1]", false},
		{"0.0.0.0", true},
		{"192.168.1.10", true},
		{"", false},
	}
	for _, tc := range cases {
		if got := forwardListenNeedsWhitelist(tc.host); got != tc.want {
			t.Errorf("forwardListenNeedsWhitelist(%q)=%v want %v", tc.host, got, tc.want)
		}
	}
}

func TestUpdateRuleWhitelistCannotDisableOnPublicListen(t *testing.T) {
	cfg, err := NewConfigStore(filepath.Join(t.TempDir(), "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newForwardManager(cfg)
	r := &forwardRule{
		id: "r1", hostID: "h1", hostname: "host",
		targetPort: 3306, localPort: 13306,
		listenAddr: "0.0.0.0:13306", enabled: true,
	}
	r.setWhitelist(true, []string{"10.0.0.1"})
	m.mu.Lock()
	m.rules[r.id] = r
	m.mu.Unlock()

	if _, err := m.updateRuleWhitelist("r1", false, nil); err == nil {
		t.Fatal("expected error when disabling whitelist on 0.0.0.0 listener")
	}
	if _, err := m.updateRuleWhitelist("r1", true, nil); err == nil {
		t.Fatal("expected error when enabling whitelist with empty list on 0.0.0.0")
	}
	if _, err := m.updateRuleWhitelist("r1", true, []string{"10.0.0.2"}); err != nil {
		t.Fatalf("replacing whitelist entries should succeed: %v", err)
	}
	on, list, _ := r.whitelistSnapshot()
	if !on || len(list) != 1 || list[0] != "10.0.0.2" {
		t.Fatalf("whitelist not updated: on=%v list=%v", on, list)
	}

	// Loopback may disable whitelist.
	r2 := &forwardRule{
		id: "r2", hostID: "h1", hostname: "host",
		targetPort: 3306, localPort: 13307,
		listenAddr: "127.0.0.1:13307", enabled: true,
	}
	r2.setWhitelist(true, []string{"10.0.0.1"})
	m.mu.Lock()
	m.rules[r2.id] = r2
	m.mu.Unlock()
	if _, err := m.updateRuleWhitelist("r2", false, nil); err != nil {
		t.Fatalf("loopback may disable whitelist: %v", err)
	}
}

func TestNormalizeWhitelist(t *testing.T) {
	// disabled: empty OK
	out, err := normalizeWhitelist(false, nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("disabled empty: out=%v err=%v", out, err)
	}
	// enabled empty → error
	if _, err := normalizeWhitelist(true, nil); err == nil {
		t.Fatal("enabled empty should fail")
	}
	if _, err := normalizeWhitelist(true, []string{"  ", ""}); err == nil {
		t.Fatal("enabled blank lines should fail")
	}
	if _, err = normalizeWhitelist(true, []string{"10.0.0.1", "10.0.0.1", "192.168.0.0/24", " bad "}); err == nil {
		t.Fatal("invalid entry should fail")
	}
	out, err = normalizeWhitelist(true, []string{"10.0.0.1", "10.0.0.1", "192.168.0.0/24", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 after dedupe, got %v", out)
	}
}

func TestClientAllowed(t *testing.T) {
	nets := []*net.IPNet{
		parseIPOrCIDR("10.0.0.0/8"),
		parseIPOrCIDR("203.0.113.10"),
		parseIPOrCIDR("2001:db8::/32"),
	}
	if !clientAllowed(false, nets, &net.TCPAddr{IP: net.ParseIP("1.2.3.4")}) {
		t.Fatal("disabled should allow any")
	}
	if clientAllowed(true, nil, &net.TCPAddr{IP: net.ParseIP("10.0.0.1")}) {
		t.Fatal("enabled empty nets should deny")
	}
	if !clientAllowed(true, nets, &net.TCPAddr{IP: net.ParseIP("10.1.2.3")}) {
		t.Fatal("10.0.0.0/8 should allow")
	}
	if !clientAllowed(true, nets, &net.UDPAddr{IP: net.ParseIP("203.0.113.10")}) {
		t.Fatal("exact IP should allow")
	}
	if clientAllowed(true, nets, &net.TCPAddr{IP: net.ParseIP("203.0.113.11")}) {
		t.Fatal("near miss should deny")
	}
	if !clientAllowed(true, nets, &net.TCPAddr{IP: net.ParseIP("2001:db8::1")}) {
		t.Fatal("IPv6 CIDR should allow")
	}
	if clientAllowed(true, nets, &net.TCPAddr{IP: net.ParseIP("2001:db9::1")}) {
		t.Fatal("IPv6 outside CIDR should deny")
	}
}

func TestParseIPOrCIDR(t *testing.T) {
	if parseIPOrCIDR("") != nil {
		t.Fatal("empty")
	}
	if parseIPOrCIDR("not-an-ip") != nil {
		t.Fatal("garbage")
	}
	n := parseIPOrCIDR("127.0.0.1")
	if n == nil || !n.Contains(net.ParseIP("127.0.0.1")) {
		t.Fatal("single IPv4")
	}
	n = parseIPOrCIDR("::1")
	if n == nil || !n.Contains(net.ParseIP("::1")) {
		t.Fatal("single IPv6")
	}
}
