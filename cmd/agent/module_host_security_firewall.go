package main

import (
	"runtime"
	"strings"
)

// hostSecFirewall is the host firewall switch snapshot for host_security_scan.
type hostSecFirewall struct {
	Status string `json:"status"`           // on|off|partial|unknown
	Engine string `json:"engine,omitempty"` // ufw|firewalld|iptables|macos|windows|pf
	Detail string `json:"detail,omitempty"`
}

func collectFirewallStatus() hostSecFirewall {
	switch runtime.GOOS {
	case "darwin":
		return collectDarwinFirewall()
	case "windows":
		return collectWindowsFirewall()
	default:
		return collectLinuxFirewall()
	}
}

func firewallFindings(fw hostSecFirewall) []hostSecFinding {
	switch fw.Status {
	case "off":
		return []hostSecFinding{{
			Level: "medium", ID: "firewall_off", Title: "系统防火墙未开启",
			Detail: fwDetail(fw), Suggest: firewallEnableSuggest(fw.Engine),
		}}
	case "partial":
		return []hostSecFinding{{
			Level: "medium", ID: "firewall_partial", Title: "系统防火墙部分关闭",
			Detail: fwDetail(fw), Suggest: "请开启全部网络配置文件的防火墙（域/专用/公用）",
		}}
	case "unknown":
		if fw.Detail != "" {
			return []hostSecFinding{{
				Level: "info", ID: "firewall_unknown", Title: "未能判定防火墙状态",
				Detail: fwDetail(fw), Suggest: "确认 Agent 权限，或手动检查 ufw/firewalld/系统防火墙",
			}}
		}
	}
	return nil
}

func fwDetail(fw hostSecFirewall) string {
	parts := []string{}
	if fw.Engine != "" {
		parts = append(parts, "引擎="+fw.Engine)
	}
	if fw.Detail != "" {
		parts = append(parts, fw.Detail)
	}
	return strings.Join(parts, " · ")
}

func firewallEnableSuggest(engine string) string {
	switch engine {
	case "macos":
		return "系统设置 → 网络 → 防火墙 → 开启"
	case "windows":
		return "Windows 安全中心 → 防火墙和网络保护 → 开启各配置文件防火墙"
	case "ufw":
		return "执行 sudo ufw enable，并按业务放行必要端口"
	case "firewalld":
		return "执行 sudo systemctl enable --now firewalld"
	case "iptables", "nftables":
		return "启用 nftables/iptables 持久化规则，默认策略建议 DROP 并放行必要端口"
	default:
		return "启用操作系统自带防火墙并限制入站来源"
	}
}

func collectDarwinFirewall() hostSecFirewall {
	fw := hostSecFirewall{Status: "unknown", Engine: "macos"}
	bin := "/usr/libexec/ApplicationFirewall/socketfilterfw"
	if !fileExists(bin) {
		fw.Detail = "未找到 Application Firewall"
		return fw
	}
	out := strings.TrimSpace(cmdOut(5, bin, "--getglobalstate"))
	fw.Detail = truncateRun(out, 200)
	fw.Status = parseMacOSFirewallState(out)
	return fw
}

func parseMacOSFirewallState(out string) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "disabled") || strings.Contains(low, "state = 0"):
		return "off"
	case strings.Contains(low, "enabled") || strings.Contains(low, "state = 1") || strings.Contains(low, "state = 2"):
		return "on"
	default:
		return "unknown"
	}
}

func collectLinuxFirewall() hostSecFirewall {
	// Prefer higher-level managers first.
	if have("ufw") {
		out := strings.TrimSpace(cmdOut(5, "ufw", "status"))
		st := parseUFWStatus(out)
		if st != "unknown" || out != "" {
			return hostSecFirewall{Status: st, Engine: "ufw", Detail: truncateRun(firstLine(out), 200)}
		}
	}
	if have("firewall-cmd") {
		out := strings.TrimSpace(cmdOut(5, "firewall-cmd", "--state"))
		st := parseFirewalldState(out)
		if st != "unknown" || out != "" {
			return hostSecFirewall{Status: st, Engine: "firewalld", Detail: truncateRun(out, 200)}
		}
	}
	if have("systemctl") {
		// nftables 未启用不代表没有防火墙：很多发行版此时仍然在跑 iptables，
		// 所以这里只认 active，其余状态一律往下继续探测，不提前下结论。
		if out := strings.TrimSpace(cmdOut(4, "systemctl", "is-active", "nftables")); out == "active" {
			return hostSecFirewall{Status: "on", Engine: "nftables", Detail: "nftables active"}
		}
		if out := strings.TrimSpace(cmdOut(4, "systemctl", "is-active", "firewalld")); out == "active" {
			return hostSecFirewall{Status: "on", Engine: "firewalld", Detail: "firewalld active"}
		}
	}
	if have("iptables") {
		out := strings.TrimSpace(cmdOut(6, "iptables", "-L", "INPUT", "-n"))
		st := parseIptablesInput(out)
		if st != "unknown" {
			return hostSecFirewall{Status: st, Engine: "iptables", Detail: truncateRun(firstLine(out), 200)}
		}
	}
	return hostSecFirewall{Status: "unknown", Detail: "未检测到 ufw/firewalld/iptables 状态"}
}

func parseUFWStatus(out string) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "status: active"):
		return "on"
	case strings.Contains(low, "status: inactive"):
		return "off"
	default:
		return "unknown"
	}
}

func parseFirewalldState(out string) string {
	low := strings.ToLower(strings.TrimSpace(out))
	switch low {
	case "running":
		return "on"
	case "not running", "dead", "inactive":
		return "off"
	default:
		if strings.Contains(low, "not running") {
			return "off"
		}
		return "unknown"
	}
}

func parseIptablesInput(out string) string {
	low := strings.ToLower(out)
	if !strings.Contains(low, "chain input") {
		return "unknown"
	}
	if strings.Contains(low, "policy drop") || strings.Contains(low, "policy reject") {
		return "on"
	}
	// ACCEPT policy with extra rules still counts as "on" (filtering configured).
	lines := 0
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "Chain ") || strings.HasPrefix(ln, "target") {
			continue
		}
		lines++
	}
	if lines > 0 {
		return "on"
	}
	if strings.Contains(low, "policy accept") {
		return "off"
	}
	return "unknown"
}

func collectWindowsFirewall() hostSecFirewall {
	fw := hostSecFirewall{Status: "unknown", Engine: "windows"}
	out := strings.TrimSpace(cmdOut(8, "cmd", "/c", "netsh advfirewall show allprofiles state"))
	if out == "" {
		fw.Detail = "netsh 无输出"
		return fw
	}
	fw.Detail = truncateRun(out, 240)
	fw.Status = parseWindowsFirewallState(out)
	return fw
}

func parseWindowsFirewallState(out string) string {
	// Typical:
	// Domain Profile Settings: ... State ON
	// Private Profile Settings: ... State ON
	// Public Profile Settings: ... State OFF
	on, off := 0, 0
	for _, ln := range strings.Split(out, "\n") {
		low := strings.ToLower(strings.TrimSpace(ln))
		if !strings.Contains(low, "state") {
			continue
		}
		switch {
		case strings.HasSuffix(low, " on") || strings.Contains(low, "state                                 on") || strings.Contains(low, "state on"):
			on++
		case strings.HasSuffix(low, " off") || strings.Contains(low, "state                                 off") || strings.Contains(low, "state off"):
			off++
		}
	}
	switch {
	case on > 0 && off == 0:
		return "on"
	case off > 0 && on == 0:
		return "off"
	case on > 0 && off > 0:
		return "partial"
	default:
		low := strings.ToLower(out)
		if strings.Contains(low, "state                                 on") || strings.Count(low, "\ton") > 0 {
			return "on"
		}
		return "unknown"
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func truncateRun(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
