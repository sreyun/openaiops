package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// HostOpenPort is a normalized listening socket from Agent net listen output.
type HostOpenPort struct {
	Proto   string `json:"proto"` // tcp|udp
	Port    int    `json:"port"`
	Addr    string `json:"addr,omitempty"` // bind address
	Process string `json:"process,omitempty"`
	Service string `json:"service,omitempty"` // well-known name
	Risk    string `json:"risk,omitempty"`    // crit|high|medium|""
	Public  bool   `json:"public"`            // bound on 0.0.0.0 / :: / *
	Raw     string `json:"raw,omitempty"`
}

// highRiskPorts maps port → (service, base risk). Risk may be elevated when Public.
var highRiskPorts = map[int]struct {
	Service string
	Risk    string
}{
	21:    {"ftp", "high"},
	23:    {"telnet", "crit"},
	135:   {"msrpc", "high"},
	139:   {"netbios", "high"},
	445:   {"smb", "crit"},
	1433:  {"mssql", "high"},
	1521:  {"oracle", "high"},
	2375:  {"docker", "crit"},
	2376:  {"docker-tls", "medium"},
	3306:  {"mysql", "high"},
	3389:  {"rdp", "crit"},
	5432:  {"postgresql", "high"},
	5601:  {"kibana", "medium"},
	5900:  {"vnc", "high"},
	6379:  {"redis", "crit"},
	9200:  {"elasticsearch", "high"},
	9300:  {"elasticsearch", "high"},
	11211: {"memcached", "crit"},
	27017: {"mongodb", "crit"},
	22:    {"ssh", "medium"},
	111:   {"rpcbind", "medium"},
	873:   {"rsync", "medium"},
	2049:  {"nfs", "high"},
	5985:  {"winrm", "high"},
	5986:  {"winrm-https", "medium"},
	8080:  {"http-alt", "medium"},
	8443:  {"https-alt", "medium"},
	9000:  {"http-alt", "medium"},
	9090:  {"http-alt", "medium"},
}

var (
	reListenAddrPort = regexp.MustCompile(`(?i)(?:\[?([0-9a-f:.%*]+)\]?|(\*))[:.](\d+)\b`)
	reListenProcess  = regexp.MustCompile(`(?i)(?:users:\(\("([^"]+)"|(\d+)/([^\s]+)|pid=(\d+))`)
	reListenProto    = regexp.MustCompile(`(?i)\b(tcp6?|udp6?|tcp|udp)\b`)
)

func parseListenPorts(lines []string) []HostOpenPort {
	seen := map[string]HostOpenPort{}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		low := strings.ToLower(ln)
		if strings.HasPrefix(low, "state ") || strings.HasPrefix(low, "proto ") ||
			strings.Contains(low, "local address") || strings.Contains(low, "foreign address") {
			continue
		}
		// Prefer LISTEN / UNCONN (ss udp) / LISTENING lines when present.
		if strings.Contains(low, "estab") && !strings.Contains(low, "listen") {
			continue
		}
		p := extractOpenPort(ln)
		if p.Port <= 0 || p.Port > 65535 {
			continue
		}
		// Merge IPv4/IPv6 / multi-bind of the same proto+port so UI doesn't show duplicates.
		key := fmt.Sprintf("%s|%d", p.Proto, p.Port)
		if prev, ok := seen[key]; ok {
			seen[key] = mergeOpenPort(prev, p)
			continue
		}
		seen[key] = p
	}
	out := make([]HostOpenPort, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RiskRank() != out[j].RiskRank() {
			return out[i].RiskRank() > out[j].RiskRank()
		}
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	return out
}

func (p HostOpenPort) RiskRank() int {
	switch p.Risk {
	case "crit":
		return 3
	case "high":
		return 2
	case "medium":
		return 1
	default:
		return 0
	}
}

func mergeOpenPort(a, b HostOpenPort) HostOpenPort {
	out := a
	if b.RiskRank() > out.RiskRank() {
		out.Risk = b.Risk
		if b.Service != "" {
			out.Service = b.Service
		}
	}
	if out.Service == "" {
		out.Service = b.Service
	}
	if out.Process == "" {
		out.Process = b.Process
	}
	if b.Public {
		out.Public = true
	}
	out.Addr = mergeListenAddrs(a.Addr, b.Addr)
	// Re-score with merged public flag (e.g. local + * → public high/crit).
	annotatePortRisk(&out)
	if out.Raw == "" {
		out.Raw = b.Raw
	}
	return out
}

func mergeListenAddrs(a, b string) string {
	a = normalizeListenAddr(a)
	b = normalizeListenAddr(b)
	if a == "" || a == b {
		return b
	}
	if b == "" {
		return a
	}
	// Prefer a single public bind label when either side is public.
	if isPublicBind(a) && isPublicBind(b) {
		if a == "*" || b == "*" {
			return "*"
		}
		if a == "0.0.0.0" || b == "0.0.0.0" {
			return "0.0.0.0"
		}
		return a
	}
	if isPublicBind(a) {
		return a
	}
	if isPublicBind(b) {
		return b
	}
	return a + "," + b
}

func extractOpenPort(line string) HostOpenPort {
	p := HostOpenPort{Raw: line, Proto: "tcp"}
	if strings.HasPrefix(strings.TrimSpace(line), "$ ") {
		return p
	}
	if m := reListenProto.FindStringSubmatch(line); len(m) > 1 {
		pr := strings.ToLower(m[1])
		if strings.HasPrefix(pr, "udp") {
			p.Proto = "udp"
		} else {
			p.Proto = "tcp"
		}
	}
	matches := reListenAddrPort.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return p
	}
	// First addr:port is usually local bind.
	m := matches[0]
	addr := m[1]
	if addr == "" {
		addr = m[2]
	}
	port, _ := strconv.Atoi(m[3])
	p.Port = port
	p.Addr = normalizeListenAddr(addr)
	p.Public = isPublicBind(p.Addr)
	if pm := reListenProcess.FindStringSubmatch(line); len(pm) > 0 {
		switch {
		case pm[1] != "":
			p.Process = pm[1]
		case pm[3] != "":
			p.Process = pm[3]
		}
	}
	// lsof: "sshd  123 root ... TCP *:22 (LISTEN)"
	if p.Process == "" {
		fields := strings.Fields(line)
		if len(fields) >= 1 {
			cmd := fields[0]
			if cmd != "" && !strings.EqualFold(cmd, "COMMAND") && !strings.EqualFold(cmd, "Proto") &&
				!strings.Contains(cmd, ":") && !strings.HasPrefix(cmd, "[") {
				p.Process = cmd
			}
		}
	}
	annotatePortRisk(&p)
	return p
}

func normalizeListenAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.Trim(addr, "[]")
	if addr == "" || addr == "*" {
		return "*"
	}
	if addr == "0.0.0.0" || addr == "::" || addr == "::0" {
		return addr
	}
	return addr
}

func isPublicBind(addr string) bool {
	switch addr {
	case "", "*", "0.0.0.0", "::", "::0", "::ffff:0.0.0.0":
		return true
	default:
		return false
	}
}

func annotatePortRisk(p *HostOpenPort) {
	info, ok := highRiskPorts[p.Port]
	if !ok {
		return
	}
	p.Service = info.Service
	p.Risk = info.Risk
	// Local-only binds are informational for attention ports (SSH etc.).
	if !p.Public {
		switch info.Risk {
		case "crit", "high":
			p.Risk = "medium"
		case "medium":
			p.Risk = ""
			return
		}
	}
}

func filterRiskyPorts(ports []HostOpenPort) []HostOpenPort {
	out := make([]HostOpenPort, 0)
	for _, p := range ports {
		if p.Risk == "crit" || p.Risk == "high" || p.Risk == "medium" {
			out = append(out, p)
		}
	}
	return out
}

func portRiskFindings(ports []HostOpenPort) []HostFinding {
	risky := filterRiskyPorts(ports)
	if len(risky) == 0 {
		return nil
	}
	var out []HostFinding
	// Cap to avoid flooding score/UI.
	for i, p := range risky {
		if i >= 25 {
			break
		}
		svc := p.Service
		if svc == "" {
			svc = "unknown"
		}
		bind := p.Addr
		if bind == "" {
			bind = "*"
		}
		title := fmt.Sprintf("高危端口开放：%s/%d（%s）", p.Proto, p.Port, svc)
		detail := fmt.Sprintf("监听 %s:%d", bind, p.Port)
		if p.Process != "" {
			detail += " · 进程 " + p.Process
		}
		if p.Public {
			detail += " · 对外绑定（0.0.0.0/::）"
		} else {
			detail += " · 本机/内网绑定"
		}
		suggest := "确认业务需要；非必要则关闭或限制来源 IP/防火墙；数据库/缓存勿对公网暴露"
		switch p.Port {
		case 22:
			suggest = "限制 SSH 来源、禁用密码登录、启用密钥与 fail2ban/防护墙"
		case 3389:
			suggest = "勿对公网开放 RDP；改用 VPN/堡垒机并启用 NLA"
		case 6379, 11211, 27017:
			suggest = "必须绑定 127.0.0.1 或内网，并启用认证；禁止公网暴露"
		case 445, 139:
			suggest = "关闭不必要的 SMB 共享，或严格限制网段访问"
		}
		out = append(out, HostFinding{
			Level: p.Risk, Category: "port", ID: fmt.Sprintf("port.%s.%d", p.Proto, p.Port),
			Title: title, Detail: detail, Suggest: suggest,
		})
	}
	return out
}

func summarizePorts(ports []HostOpenPort) (count, risky int, sample []int) {
	count = len(ports)
	seenPort := map[int]bool{}
	seenRisky := map[int]bool{}
	for _, p := range ports {
		if (p.Risk == "crit" || p.Risk == "high" || p.Risk == "medium") && !seenRisky[p.Port] {
			seenRisky[p.Port] = true
			risky++
		}
		if !seenPort[p.Port] && len(sample) < 12 {
			seenPort[p.Port] = true
			sample = append(sample, p.Port)
		}
	}
	sort.Ints(sample)
	return count, risky, sample
}
