package main

import (
	"net"
	"testing"
)

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
