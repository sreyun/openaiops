package main

import (
	"fmt"
	"net"
	"strings"
)

const forwardWhitelistMaxEntries = 64

// forwardListenNeedsWhitelist reports whether a bind host exposes a raw TCP/UDP
// tunnel beyond loopback and therefore must keep an explicit source-IP allowlist.
//
// createRule already enforces this at create time. updateRuleWhitelist must
// enforce the same rule: otherwise an operator creates a Docker 0.0.0.0 forward
// with a whitelist, then unchecks the box in the edit modal and silently opens
// the tunnel to every client that can reach the port.
func forwardListenNeedsWhitelist(listenHost string) bool {
	h := strings.TrimSpace(strings.Trim(listenHost, "[]"))
	if h == "" {
		return false
	}
	return h != "127.0.0.1" && !strings.EqualFold(h, "localhost") && h != "::1"
}

// ruleListenHost extracts the host part of a rule's listenAddr (host:port).
func ruleListenHost(r *forwardRule) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.listenAddr)
	if err != nil {
		return r.listenAddr
	}
	return host
}

// wlSnap is an immutable snapshot of a rule's source-IP whitelist for hot reads.
type wlSnap struct {
	enabled bool
	nets    []*net.IPNet // parsed CIDRs / single-IP /32|/128
	raw     []string     // original entries for API echo
}

func (r *forwardRule) setWhitelist(enabled bool, list []string) {
	snap := &wlSnap{enabled: enabled, raw: append([]string(nil), list...)}
	if enabled {
		for _, e := range list {
			if n := parseIPOrCIDR(e); n != nil {
				snap.nets = append(snap.nets, n)
			}
		}
	}
	r.wl.Store(snap)
	r.whitelistEnabled = enabled
	r.whitelist = append([]string(nil), list...)
}

func (r *forwardRule) whitelistSnapshot() (enabled bool, list []string, nets []*net.IPNet) {
	v := r.wl.Load()
	if v == nil {
		return r.whitelistEnabled, r.whitelist, nil
	}
	s := v.(*wlSnap)
	return s.enabled, s.raw, s.nets
}

// normalizeWhitelist parses, dedupes, and validates IP/CIDR entries.
// When enabled and the list is empty after normalize, returns an error
// (open-enabled-with-empty-list would silently deny everyone — require explicit entries).
func normalizeWhitelist(enabled bool, entries []string) ([]string, error) {
	if !enabled {
		// Keep raw list for UI round-trip even when disabled.
		out := make([]string, 0, len(entries))
		seen := map[string]bool{}
		for _, e := range entries {
			e = strings.TrimSpace(e)
			if e == "" || seen[e] {
				continue
			}
			if parseIPOrCIDR(e) == nil {
				return nil, fmt.Errorf("非法白名单项：%s（需为 IP 或 CIDR）", e)
			}
			seen[e] = true
			out = append(out, e)
			if len(out) > forwardWhitelistMaxEntries {
				return nil, fmt.Errorf("白名单最多 %d 条", forwardWhitelistMaxEntries)
			}
		}
		return out, nil
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("已启用来源 IP 白名单，请至少填写一条 IP 或 CIDR")
	}
	out := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if seen[e] {
			continue
		}
		if parseIPOrCIDR(e) == nil {
			return nil, fmt.Errorf("非法白名单项：%s（需为 IP 或 CIDR）", e)
		}
		seen[e] = true
		out = append(out, e)
		if len(out) > forwardWhitelistMaxEntries {
			return nil, fmt.Errorf("白名单最多 %d 条", forwardWhitelistMaxEntries)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("已启用来源 IP 白名单，请至少填写一条 IP 或 CIDR")
	}
	return out, nil
}

func parseIPOrCIDR(s string) *net.IPNet {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if _, n, err := net.ParseCIDR(s); err == nil {
		return n
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
}

// clientAllowed reports whether addr may use a forward listener.
// When enabled is false, always allow. When enabled with empty nets, deny all.
func clientAllowed(enabled bool, nets []*net.IPNet, addr net.Addr) bool {
	if !enabled {
		return true
	}
	ip := addrIP(addr)
	if ip == nil {
		return false
	}
	if len(nets) == 0 {
		return false
	}
	for _, n := range nets {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

func addrIP(addr net.Addr) net.IP {
	if addr == nil {
		return nil
	}
	switch a := addr.(type) {
	case *net.TCPAddr:
		return a.IP
	case *net.UDPAddr:
		return a.IP
	case *net.IPAddr:
		return a.IP
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			host = addr.String()
		}
		return net.ParseIP(host)
	}
}
