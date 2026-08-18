package main

import (
	"fmt"
	"regexp"
	"strings"
)

// CmdPolicyConfig controls playbook / remediation shell command safety.
// Mode: "advisory" = log-only for allowlist miss (still blocks danger); "strict" = reject non-allowlisted.
type CmdPolicyConfig struct {
	Mode            string   `json:"mode,omitempty"`              // advisory | strict (default strict for auto-remediation path)
	AllowPrefixes   []string `json:"allow_prefixes,omitempty"`   // optional extra allow prefixes
	DenyPatterns    []string `json:"deny_patterns,omitempty"`    // extra deny regexes
	DisableBuiltins bool     `json:"disable_builtins,omitempty"` // if true, only AllowPrefixes (dangerous)
}

var (
	dangerCmdRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\brm\s+(-[a-zA-Z]*f|-[a-zA-Z]*r)`),
		regexp.MustCompile(`(?i)\bmkfs(\.|$)`),
		regexp.MustCompile(`(?i)\bdd\s+if=`),
		regexp.MustCompile(`(?i)\b(shutdown|reboot|halt|poweroff)\b`),
		regexp.MustCompile(`(?i)\b(wipefs|shred)\b`),
		regexp.MustCompile(`(?i)\bcrontab\s+-r\b`),
		regexp.MustCompile(`(?i)\b(userdel|passwd)\b`),
		regexp.MustCompile(`(?i)>\s*/dev/sd`),
		regexp.MustCompile(`(?i)\bchmod\s+-R\s+777\b`),
		regexp.MustCompile(`(?i)\bcurl\b.*\|\s*(ba)?sh\b`),
		regexp.MustCompile(`(?i)\bwget\b.*\|\s*(ba)?sh\b`),
	}
)

// diagCommandAllowed 校验诊断命令是否为「只读命令 + 只读管道过滤」。
// 保留原语义；实现委托给 evaluateDiagCommand。
func diagCommandAllowed(command string) (bool, string) {
	return evaluateDiagCommand(command)
}

func evaluateDiagCommand(command string) (bool, string) {
	cmdTrim := strings.TrimSpace(command)
	if cmdTrim == "" {
		return false, "请指定诊断命令"
	}
	if strings.ContainsAny(cmdTrim, ";&$<>\n\r\\(){}`") {
		return false, "诊断命令含被禁止的字符（; & $ < > ` 等），仅允许只读命令与管道过滤"
	}
	allow := []string{
		"top", "df", "iostat", "vmstat", "mpstat", "sar", "pidstat", "netstat", "ss", "free",
		"ps", "uptime", "cat", "head", "tail", "grep", "egrep", "ls", "du", "lsof", "dmesg",
		"journalctl", "systemctl status", "docker ps", "docker logs", "docker stats",
		"kubectl get", "kubectl describe", "wc", "sort", "uniq", "cut", "tr", "nl", "tac",
		"column", "date", "hostname", "uname", "who", "w",
	}
	deniedPaths := []string{
		"/etc/shadow", "/etc/gshadow", "/etc/master.passwd",
		".ssh/", ".gnupg/", ".aws/", ".kube/config",
		"/etc/sudoers", "/root/.bash_history",
	}
	segOK := func(seg string) bool {
		seg = strings.ToLower(strings.TrimSpace(seg))
		for _, p := range allow {
			if seg == p || strings.HasPrefix(seg, p+" ") {
				return true
			}
		}
		return false
	}
	for _, seg := range strings.Split(cmdTrim, "|") {
		if !segOK(seg) {
			return false, fmt.Sprintf("诊断命令 %q 含非白名单命令，仅允许只读诊断命令（top/df/free/ps/ss/cat/grep/journalctl 等）及其管道过滤", command)
		}
		segLower := strings.ToLower(seg)
		for _, dp := range deniedPaths {
			if strings.Contains(segLower, dp) {
				return false, fmt.Sprintf("诊断命令包含敏感路径 %q，已拦截", dp)
			}
		}
	}
	return true, ""
}

// evaluatePlaybookCommand checks a playbook/remediation shell command.
// Returns (ok, forceApproval, reason). forceApproval is set when advisory mode
// would still prefer human gate for risky commands.
func evaluatePlaybookCommand(command string, pol CmdPolicyConfig) (ok bool, forceApproval bool, reason string) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false, false, "命令不能为空"
	}
	if pol.Mode == "" {
		pol.Mode = "strict"
	}
	// Always block known-dangerous patterns.
	for _, re := range dangerCmdRes {
		if re.MatchString(cmd) {
			return false, true, "命令匹配危险模式，已拦截（如 rm -rf / mkfs / shutdown / curl|sh）"
		}
	}
	for _, pat := range pol.DenyPatterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		re, err := regexp.Compile("(?i)" + pat)
		if err != nil {
			continue
		}
		if re.MatchString(cmd) {
			return false, true, "命令匹配自定义拒绝规则"
		}
	}

	allow := append([]string{}, pol.AllowPrefixes...)
	if !pol.DisableBuiltins {
		allow = append(allow,
			"systemctl", "service", "docker", "kubectl", "nginx", "apachectl",
			"supervisorctl", "pm2", "kill", "pkill", "nice", "renice",
			"ip", "iptables", "nft", "sysctl", "echo", "printf", "true", ":",
			"sleep", "logger", "date", "hostname", "uname", "cat",
			"sed", "awk", "grep", "head", "tail", "ls", "df", "free", "ps",
			"ss", "netstat",
		)
	}
	if len(allow) == 0 {
		// Fail closed: empty allowlist after builtins disabled must deny.
		if pol.Mode == "advisory" {
			return true, true, "允许前缀列表为空，建议人工审批后执行"
		}
		return false, true, "允许前缀列表为空（strict 模式拒绝）"
	}
	// Validate every pipe segment's first word (not only the left-most command).
	segments := strings.Split(cmd, "|")
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		first := firstShellWord(seg)
		hit := false
		for _, p := range allow {
			p = strings.ToLower(strings.TrimSpace(p))
			if p == "" {
				continue
			}
			low := strings.ToLower(seg)
			if first == p || strings.HasPrefix(low, p+" ") || strings.HasPrefix(low, p+"\t") {
				hit = true
				break
			}
		}
		if !hit {
			if pol.Mode == "advisory" {
				return true, true, "管道段命令不在允许前缀列表，建议人工审批后执行"
			}
			return false, true, fmt.Sprintf("命令 %q 不在允许前缀列表（strict 模式）", first)
		}
	}
	// Always scan full command for classic injection meta (even when pipes are used).
	if pol.Mode == "strict" && strings.ContainsAny(cmd, ";$<>`") {
		return false, true, "命令含注入风险元字符"
	}
	return true, false, ""
}

func firstShellWord(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	// strip simple env assignments PREFIX=val cmd
	for {
		sp := strings.IndexAny(cmd, " \t")
		tok := cmd
		if sp >= 0 {
			tok = cmd[:sp]
		}
		if strings.Contains(tok, "=") && !strings.HasPrefix(tok, "=") {
			if sp < 0 {
				return ""
			}
			cmd = strings.TrimSpace(cmd[sp:])
			continue
		}
		return strings.ToLower(tok)
	}
}

// validatePlaybookCommands / playbookNeedsForcedApproval → playbook_modules.go
