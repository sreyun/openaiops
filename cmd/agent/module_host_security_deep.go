package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Deep host posture checks that go beyond the CIS-lite baseline: local account
// hygiene, privilege escalation surface (SUID/sudoers), kernel hardening
// sysctls, audit/patching posture, and Windows/macOS platform controls.

func collectDeepHardeningFindings() []hostSecFinding {
	switch runtime.GOOS {
	case "linux":
		var fs []hostSecFinding
		fs = append(fs, collectAccountFindings()...)
		fs = append(fs, collectSudoersFindings()...)
		fs = append(fs, collectSuidFindings()...)
		fs = append(fs, collectKernelHardeningFindings()...)
		fs = append(fs, collectAuditPatchFindings()...)
		fs = append(fs, collectSensitivePermFindings()...)
		fs = append(fs, collectContainerExposureFindings()...)
		return fs
	case "windows":
		return collectWindowsPostureFindings()
	case "darwin":
		return collectDarwinPostureFindings()
	}
	return nil
}

// --- Linux: local accounts ---

type passwdEntry struct {
	Name  string
	UID   int
	GID   int
	Home  string
	Shell string
}

func parsePasswd(raw string) []passwdEntry {
	var out []passwdEntry
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		uid, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		gid, _ := strconv.Atoi(f[3])
		out = append(out, passwdEntry{Name: f[0], UID: uid, GID: gid, Home: f[5], Shell: f[6]})
	}
	return out
}

func interactiveShell(sh string) bool {
	sh = strings.TrimSpace(sh)
	switch sh {
	case "", "/sbin/nologin", "/usr/sbin/nologin", "/bin/false", "/usr/bin/false", "/bin/sync", "/dev/null":
		return false
	}
	return true
}

func collectAccountFindings() []hostSecFinding {
	raw, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil
	}
	users := parsePasswd(string(raw))
	var fs []hostSecFinding

	var rootAlias []string
	byUID := map[int][]string{}
	byName := map[string]int{}
	for _, u := range users {
		byUID[u.UID] = append(byUID[u.UID], u.Name)
		byName[u.Name]++
		if u.UID == 0 && u.Name != "root" {
			rootAlias = append(rootAlias, u.Name)
		}
	}
	if len(rootAlias) > 0 {
		sort.Strings(rootAlias)
		fs = append(fs, hostSecFinding{
			Level: "critical", ID: "acct_uid0_alias", Title: "存在非 root 的 UID=0 账号",
			Detail:  "UID 0: " + strings.Join(rootAlias, ", "),
			Suggest: "UID 0 等同 root，属于典型后门手法。确认来源并删除或改为普通 UID",
		})
	}
	var dupUID []string
	for uid, names := range byUID {
		if len(names) > 1 {
			sort.Strings(names)
			dupUID = append(dupUID, fmt.Sprintf("%d=%s", uid, strings.Join(names, "/")))
		}
	}
	if len(dupUID) > 0 {
		sort.Strings(dupUID)
		fs = append(fs, hostSecFinding{
			Level: "high", ID: "acct_dup_uid", Title: "存在共享 UID 的账号",
			Detail:  truncateStr(strings.Join(dupUID, ", "), 240),
			Suggest: "共享 UID 会让审计无法区分操作者，应为每个账号分配唯一 UID",
		})
	}

	// /etc/shadow is root-only; a non-privileged agent simply skips this check.
	if shadow, err := os.ReadFile("/etc/shadow"); err == nil {
		var empty, noExpire []string
		for _, line := range strings.Split(string(shadow), "\n") {
			f := strings.Split(strings.TrimSpace(line), ":")
			if len(f) < 5 || f[0] == "" {
				continue
			}
			hash := f[1]
			if hash == "" {
				empty = append(empty, f[0])
			}
			// Only accounts that can actually log in matter here.
			if hash == "" || strings.HasPrefix(hash, "!") || strings.HasPrefix(hash, "*") {
				continue
			}
			if maxDays, err := strconv.Atoi(f[4]); err != nil || maxDays >= 99999 {
				noExpire = append(noExpire, f[0])
			}
		}
		if len(empty) > 0 {
			fs = append(fs, hostSecFinding{
				Level: "critical", ID: "acct_empty_password", Title: "存在空密码账号",
				Detail:  "账号: " + truncateStr(strings.Join(empty, ", "), 200),
				Suggest: "立即设置强密码或锁定账号（passwd -l）",
			})
		}
		if len(noExpire) > 3 {
			fs = append(fs, hostSecFinding{
				Level: "low", ID: "acct_password_never_expires", Title: "多数账号密码永不过期",
				Detail:  fmt.Sprintf("%d 个可登录账号未设置密码有效期", len(noExpire)),
				Suggest: "按等保/CIS 要求配置 PASS_MAX_DAYS（如 90 天）",
			})
		}
	}

	// System accounts (UID < 1000) should not have an interactive shell.
	var sysShell []string
	for _, u := range users {
		if u.UID != 0 && u.UID < 1000 && interactiveShell(u.Shell) {
			sysShell = append(sysShell, u.Name+"("+u.Shell+")")
		}
	}
	if len(sysShell) > 0 {
		sort.Strings(sysShell)
		fs = append(fs, hostSecFinding{
			Level: "medium", ID: "acct_system_shell", Title: "系统账号具备可登录 Shell",
			Detail:  truncateStr(strings.Join(sysShell, ", "), 240),
			Suggest: "将服务账号 Shell 改为 /sbin/nologin，减少被利用为登录入口的风险",
		})
	}

	if v := loginDefsValue("PASS_MAX_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 180 {
			fs = append(fs, hostSecFinding{
				Level: "low", ID: "acct_pass_max_days", Title: "密码最长有效期过长",
				Detail: "PASS_MAX_DAYS=" + v, Suggest: "建议 ≤90 天（/etc/login.defs）",
			})
		}
	}
	if v := loginDefsValue("PASS_MIN_LEN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n < 8 {
			fs = append(fs, hostSecFinding{
				Level: "medium", ID: "acct_pass_min_len", Title: "密码最小长度不足",
				Detail: "PASS_MIN_LEN=" + v, Suggest: "建议 ≥8 并启用 pam_pwquality 复杂度策略",
			})
		}
	}
	return fs
}

func loginDefsValue(key string) string {
	raw, err := os.ReadFile("/etc/login.defs")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 && strings.EqualFold(f[0], key) {
			return f[1]
		}
	}
	return ""
}

// --- Linux: privilege escalation surface ---

func collectSudoersFindings() []hostSecFinding {
	var files []string
	if fileExists("/etc/sudoers") {
		files = append(files, "/etc/sudoers")
	}
	if entries, err := os.ReadDir("/etc/sudoers.d"); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join("/etc/sudoers.d", e.Name()))
			}
		}
	}
	var nopasswd, allAll []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "Defaults") {
				continue
			}
			if strings.Contains(line, "NOPASSWD") {
				nopasswd = append(nopasswd, filepath.Base(f)+": "+truncateStr(line, 100))
			}
			// "user ALL=(ALL) ALL" style unrestricted grants to a non-root principal.
			if strings.Contains(line, "ALL=(ALL") && strings.HasSuffix(strings.TrimSpace(line), "ALL") &&
				!strings.HasPrefix(line, "root") && !strings.HasPrefix(line, "%wheel") && !strings.HasPrefix(line, "%sudo") {
				allAll = append(allAll, filepath.Base(f)+": "+truncateStr(line, 100))
			}
		}
	}
	var fs []hostSecFinding
	if len(nopasswd) > 0 {
		fs = append(fs, hostSecFinding{
			Level: "high", ID: "sudo_nopasswd", Title: "sudo 存在免密提权规则",
			Detail:  truncateStr(strings.Join(nopasswd, " | "), 300),
			Suggest: "免密 sudo 会把任意本地账号变成 root 通道；仅在自动化必需时保留并限定具体命令",
		})
	}
	if len(allAll) > 0 {
		fs = append(fs, hostSecFinding{
			Level: "medium", ID: "sudo_unrestricted", Title: "sudo 存在无限制提权授权",
			Detail:  truncateStr(strings.Join(allAll, " | "), 300),
			Suggest: "按最小权限原则限定可执行命令集合",
		})
	}
	return fs
}

// suidExpected lists binaries that legitimately ship with the SUID bit.
var suidExpected = map[string]bool{
	"su": true, "sudo": true, "passwd": true, "chsh": true, "chfn": true,
	"newgrp": true, "gpasswd": true, "mount": true, "umount": true,
	"ping": true, "ping6": true, "pkexec": true, "fusermount": true, "fusermount3": true,
	"unix_chkpwd": true, "ssh-agent": true, "at": true, "crontab": true,
	"dbus-daemon-launch-helper": true, "polkit-agent-helper-1": true, "sg": true,
	"expiry": true, "chage": true, "write": true, "wall": true, "screen": true,
	"pam_timestamp_check": true, "utempter": true, "vmware-user-suid-wrapper": true,
	"snap-confine": true, "mount.nfs": true, "umount.nfs": true, "sudoedit": true,
}

// suidDangerous are interpreters/tools that grant instant root when SUID.
var suidDangerous = map[string]bool{
	"bash": true, "sh": true, "dash": true, "zsh": true, "ksh": true,
	"python": true, "python2": true, "python3": true, "perl": true, "ruby": true,
	"php": true, "lua": true, "node": true, "awk": true, "gawk": true, "mawk": true,
	"find": true, "vim": true, "vi": true, "nano": true, "less": true, "more": true,
	"cp": true, "mv": true, "tar": true, "nmap": true, "env": true, "docker": true,
}

func collectSuidFindings() []hostSecFinding {
	out := cmdOut(20, "bash", "-lc",
		`find / -xdev \( -perm -4000 -o -perm -2000 \) -type f 2>/dev/null | head -n 400`)
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var dangerous, unexpected []string
	total := 0
	for _, p := range strings.Split(out, "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		total++
		base := filepath.Base(p)
		switch {
		case suidDangerous[base]:
			dangerous = append(dangerous, p)
		case !suidExpected[base] && len(unexpected) < 20:
			unexpected = append(unexpected, p)
		}
	}
	var fs []hostSecFinding
	if len(dangerous) > 0 {
		fs = append(fs, hostSecFinding{
			Level: "critical", ID: "suid_interpreter", Title: "危险的 SUID/SGID 程序",
			Detail:  truncateStr(strings.Join(dangerous, ", "), 300),
			Suggest: "解释器/通用工具带 SUID 可直接提权到 root，通常是入侵留下的后门。核实后 chmod u-s 并排查",
		})
	}
	if len(unexpected) > 0 {
		fs = append(fs, hostSecFinding{
			Level: "low", ID: "suid_unexpected", Title: "非常见 SUID/SGID 程序",
			Detail: fmt.Sprintf("共 %d 个 SUID/SGID 文件，其中非常见: %s",
				total, truncateStr(strings.Join(unexpected, ", "), 260)),
			Suggest: "与软件包清单核对（rpm -Vf / dpkg -S），移除不必要的 SUID 位",
		})
	}
	return fs
}

// --- Linux: kernel hardening ---

type sysctlCheck struct {
	Key     string
	Want    string
	Level   string
	Title   string
	Suggest string
}

func collectKernelHardeningFindings() []hostSecFinding {
	checks := []sysctlCheck{
		{"net/ipv4/tcp_syncookies", "1", "medium", "未启用 TCP SYN Cookies", "抵御 SYN Flood：net.ipv4.tcp_syncookies=1"},
		{"net/ipv4/conf/all/accept_redirects", "0", "medium", "接受 ICMP 重定向", "net.ipv4.conf.all.accept_redirects=0，防止路由劫持"},
		{"net/ipv4/conf/all/accept_source_route", "0", "medium", "接受源路由报文", "net.ipv4.conf.all.accept_source_route=0"},
		{"net/ipv4/conf/all/rp_filter", "1", "low", "未启用反向路径过滤", "net.ipv4.conf.all.rp_filter=1，抑制地址伪造"},
		{"net/ipv4/conf/all/log_martians", "1", "info", "未记录异常源地址报文", "net.ipv4.conf.all.log_martians=1 便于溯源"},
		{"kernel/dmesg_restrict", "1", "low", "内核日志对普通用户可读", "kernel.dmesg_restrict=1，避免泄露内核地址"},
		{"kernel/kptr_restrict", "1", "low", "内核指针未脱敏", "kernel.kptr_restrict=1（或 2）"},
		{"fs/suid_dumpable", "0", "medium", "SUID 程序允许核心转储", "fs.suid_dumpable=0，防止内存中的凭据落盘"},
		{"kernel/yama/ptrace_scope", "1", "low", "ptrace 未受限", "kernel.yama.ptrace_scope=1，限制跨进程注入"},
		{"net/ipv4/conf/all/send_redirects", "0", "low", "发送 ICMP 重定向", "非路由主机应设为 0"},
	}
	var fs []hostSecFinding
	for _, c := range checks {
		raw, err := os.ReadFile("/proc/sys/" + c.Key)
		if err != nil {
			continue
		}
		got := strings.TrimSpace(string(raw))
		if got == c.Want {
			continue
		}
		// kptr_restrict=2 is stricter than 1, ptrace_scope>=1 is fine.
		if (c.Key == "kernel/kptr_restrict" || c.Key == "kernel/yama/ptrace_scope") && got > c.Want {
			continue
		}
		key := strings.ReplaceAll(c.Key, "/", ".")
		fs = append(fs, hostSecFinding{
			Level: c.Level, ID: "sysctl." + key, Title: "内核参数不安全 — " + c.Title,
			Detail: key + "=" + got + "（建议 " + c.Want + "）", Suggest: c.Suggest,
		})
	}
	return fs
}

// --- Linux: audit / patching / permissions ---

func collectAuditPatchFindings() []hostSecFinding {
	var fs []hostSecFinding
	auditRunning := strings.Contains(cmdOut(4, "systemctl", "is-active", "auditd"), "active") ||
		fileExists("/var/run/auditd.pid")
	if !auditRunning {
		fs = append(fs, hostSecFinding{
			Level: "medium", ID: "auditd_off", Title: "未运行审计服务 auditd",
			Detail:  "systemctl is-active auditd 非 active",
			Suggest: "等保/CIS 要求留存操作审计：安装并启用 auditd，配置关键路径与提权审计规则",
		})
	}
	// Unattended security updates.
	hasAuto := fileExists("/etc/apt/apt.conf.d/20auto-upgrades") ||
		fileExists("/etc/apt/apt.conf.d/50unattended-upgrades") ||
		strings.Contains(cmdOut(4, "systemctl", "is-enabled", "dnf-automatic.timer"), "enabled") ||
		strings.Contains(cmdOut(4, "systemctl", "is-enabled", "yum-cron"), "enabled")
	if !hasAuto {
		fs = append(fs, hostSecFinding{
			Level: "low", ID: "auto_updates_off", Title: "未启用自动安全更新",
			Detail:  "未检测到 unattended-upgrades / dnf-automatic",
			Suggest: "对无人值守主机启用自动安全补丁，或纳入统一补丁窗口管理",
		})
	}
	// Time sync — audit logs are worthless with drifting clocks.
	timeSynced := strings.Contains(cmdOut(4, "timedatectl", "show", "-p", "NTPSynchronized", "--value"), "yes") ||
		strings.Contains(cmdOut(4, "systemctl", "is-active", "chronyd"), "active") ||
		strings.Contains(cmdOut(4, "systemctl", "is-active", "ntpd"), "active")
	if !timeSynced {
		fs = append(fs, hostSecFinding{
			Level: "low", ID: "time_sync_off", Title: "未启用时间同步",
			Detail:  "未检测到 NTP/chrony 同步状态",
			Suggest: "时间漂移会破坏日志关联与证书校验，请启用 chrony/systemd-timesyncd",
		})
	}
	return fs
}

// sensitivePerm describes a file whose permissions must not be looser than Max.
type sensitivePerm struct {
	Path  string
	Max   os.FileMode
	Level string
}

func collectSensitivePermFindings() []hostSecFinding {
	checks := []sensitivePerm{
		{"/etc/shadow", 0o640, "high"},
		{"/etc/gshadow", 0o640, "medium"},
		{"/etc/passwd", 0o644, "medium"},
		{"/etc/group", 0o644, "low"},
		{"/etc/sudoers", 0o440, "high"},
		{"/etc/ssh/sshd_config", 0o600, "medium"},
		{"/boot/grub/grub.cfg", 0o600, "low"},
	}
	var fs []hostSecFinding
	for _, c := range checks {
		fi, err := os.Stat(c.Path)
		if err != nil {
			continue
		}
		perm := fi.Mode().Perm()
		if perm&^c.Max == 0 {
			continue
		}
		fs = append(fs, hostSecFinding{
			Level: c.Level, ID: "perm." + strings.ReplaceAll(strings.TrimPrefix(c.Path, "/"), "/", "_"),
			Title:   "敏感文件权限过宽 — " + c.Path,
			Detail:  fmt.Sprintf("当前 %04o，建议不超过 %04o", uint32(perm), uint32(c.Max)),
			Suggest: fmt.Sprintf("chmod %04o %s", uint32(c.Max), c.Path),
		})
	}
	// A world-readable private SSH host key is an immediate host-impersonation risk.
	if entries, err := os.ReadDir("/etc/ssh"); err == nil {
		var bad []string
		for _, e := range entries {
			n := e.Name()
			if !strings.HasPrefix(n, "ssh_host_") || !strings.HasSuffix(n, "_key") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			if fi.Mode().Perm()&0o077 != 0 {
				bad = append(bad, fmt.Sprintf("%s(%04o)", n, uint32(fi.Mode().Perm())))
			}
		}
		if len(bad) > 0 {
			fs = append(fs, hostSecFinding{
				Level: "high", ID: "perm.ssh_host_key", Title: "SSH 主机私钥权限过宽",
				Detail: strings.Join(bad, ", "), Suggest: "chmod 600 /etc/ssh/ssh_host_*_key",
			})
		}
	}
	return fs
}

func collectContainerExposureFindings() []hostSecFinding {
	var fs []hostSecFinding
	if fi, err := os.Stat("/var/run/docker.sock"); err == nil {
		if fi.Mode().Perm()&0o006 != 0 {
			fs = append(fs, hostSecFinding{
				Level: "critical", ID: "docker_sock_world", Title: "Docker socket 全局可访问",
				Detail:  fmt.Sprintf("/var/run/docker.sock mode=%04o", uint32(fi.Mode().Perm())),
				Suggest: "访问 docker.sock 等同 root：改为 root:docker 0660，并审查 docker 组成员",
			})
		}
	}
	// Containers started with --privileged bypass most kernel isolation.
	if have("docker") {
		out := cmdOut(8, "bash", "-lc",
			`docker ps --quiet 2>/dev/null | head -n 40 | xargs -r docker inspect --format '{{.Name}} {{.HostConfig.Privileged}}' 2>/dev/null | grep -i true | head -n 10`)
		if strings.TrimSpace(out) != "" {
			fs = append(fs, hostSecFinding{
				Level: "high", ID: "docker_privileged", Title: "存在特权容器",
				Detail:  truncateStr(strings.TrimSpace(out), 240),
				Suggest: "特权容器可直接操作宿主内核与设备，改用最小 capability 集合",
			})
		}
	}
	return fs
}

// --- Windows ---

// winPostureScript collects the whole Windows posture in ONE PowerShell process:
// spawning powershell.exe per check would dominate the scan's runtime.
const winPostureScript = `
$ErrorActionPreference='SilentlyContinue'
$o=[ordered]@{}
try{$m=Get-MpComputerStatus;$o.defender=@{rtp=$m.RealTimeProtectionEnabled;av=$m.AntivirusEnabled;age=$m.AntivirusSignatureAge;tamper=$m.IsTamperProtected}}catch{}
try{$o.bitlocker=@(Get-BitLockerVolume | Where-Object {$_.VolumeType -eq 'OperatingSystem'} | ForEach-Object {@{mount=$_.MountPoint;status=[string]$_.ProtectionStatus}})}catch{}
try{$o.smb1=[bool](Get-SmbServerConfiguration).EnableSMB1Protocol}catch{}
try{
  $ts='HKLM:\SYSTEM\CurrentControlSet\Control\Terminal Server'
  $o.rdp_enabled=((Get-ItemProperty $ts).fDenyTSConnections -eq 0)
  $o.rdp_nla=((Get-ItemProperty "$ts\WinStations\RDP-Tcp").UserAuthentication -eq 1)
}catch{}
try{$o.uac=((Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System').EnableLUA -eq 1)}catch{}
try{$w='HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon';$p=Get-ItemProperty $w;$o.autologon=($p.AutoAdminLogon -eq '1' -and $p.DefaultPassword -ne $null)}catch{}
try{$o.guest=[bool](Get-LocalUser -Name 'Guest').Enabled}catch{}
try{$o.admins=@(Get-LocalGroupMember -Group (Get-LocalGroup -SID 'S-1-5-32-544').Name | ForEach-Object {[string]$_.Name})}catch{}
try{$o.psv2=[bool]((Get-WindowsOptionalFeature -Online -FeatureName MicrosoftWindows-PowerShellV2).State -eq 'Enabled')}catch{}
try{$o.llmnr=((Get-ItemProperty 'HKLM:\SOFTWARE\Policies\Microsoft\Windows NT\DNSClient').EnableMulticast -eq 0)}catch{}
try{$o.reboot_pending=(Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending')}catch{}
try{$n=net accounts;$o.net_accounts=($n -join "` + "`" + `n")}catch{}
$o | ConvertTo-Json -Depth 4 -Compress
`

type winPosture struct {
	Defender struct {
		RTP    *bool    `json:"rtp"`
		AV     *bool    `json:"av"`
		Age    *float64 `json:"age"`
		Tamper *bool    `json:"tamper"`
	} `json:"defender"`
	BitLocker []struct {
		Mount  string `json:"mount"`
		Status string `json:"status"`
	} `json:"bitlocker"`
	SMB1          *bool    `json:"smb1"`
	RDPEnabled    *bool    `json:"rdp_enabled"`
	RDPNLA        *bool    `json:"rdp_nla"`
	UAC           *bool    `json:"uac"`
	AutoLogon     *bool    `json:"autologon"`
	Guest         *bool    `json:"guest"`
	Admins        []string `json:"admins"`
	PSv2          *bool    `json:"psv2"`
	LLMNRDisabled *bool    `json:"llmnr"`
	RebootPending *bool    `json:"reboot_pending"`
	NetAccounts   string   `json:"net_accounts"`
}

func collectWindowsPostureFindings() []hostSecFinding {
	raw := strings.TrimSpace(string(cmdOutRaw(45, windowsPowerShellPath(), "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", winPostureScript)))
	if raw == "" {
		return []hostSecFinding{{
			Level: "info", ID: "win_posture_unavailable", Title: "Windows 安全基线采集失败",
			Detail:  "PowerShell 无输出",
			Suggest: "以管理员权限运行 Agent，并确认未被执行策略/EDR 阻断",
		}}
	}
	// A single BitLocker volume serializes as an object, not an array.
	raw = strings.ReplaceAll(raw, `"bitlocker":{`, `"bitlocker":[{`)
	if strings.Contains(raw, `"bitlocker":[{`) && !strings.Contains(raw, `}]`) {
		raw = strings.Replace(raw, `},"smb1"`, `}],"smb1"`, 1)
	}
	var p winPosture
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return []hostSecFinding{{
			Level: "info", ID: "win_posture_parse", Title: "Windows 安全基线解析失败",
			Detail: truncateStr(err.Error(), 200), Suggest: "升级 PowerShell 或以管理员运行 Agent",
		}}
	}

	var fs []hostSecFinding
	isFalse := func(b *bool) bool { return b != nil && !*b }
	isTrue := func(b *bool) bool { return b != nil && *b }

	if isFalse(p.Defender.AV) || isFalse(p.Defender.RTP) {
		fs = append(fs, hostSecFinding{
			Level: "critical", ID: "win_defender_off", Title: "Windows Defender 未启用实时防护",
			Detail:  fmt.Sprintf("AntivirusEnabled=%v RealTimeProtection=%v", boolStr(p.Defender.AV), boolStr(p.Defender.RTP)),
			Suggest: "开启实时保护，或确认已部署等效的第三方 EDR",
		})
	}
	if p.Defender.Age != nil && *p.Defender.Age > 7 {
		fs = append(fs, hostSecFinding{
			Level: "medium", ID: "win_defender_stale", Title: "病毒库过期",
			Detail:  fmt.Sprintf("特征库已 %.0f 天未更新", *p.Defender.Age),
			Suggest: "执行 Update-MpSignature 并检查更新通道",
		})
	}
	if isFalse(p.Defender.Tamper) {
		fs = append(fs, hostSecFinding{
			Level: "low", ID: "win_defender_tamper", Title: "未启用防篡改保护",
			Detail: "IsTamperProtected=false", Suggest: "开启 Tamper Protection，防止恶意软件关闭杀软",
		})
	}
	unprotected := 0
	for _, v := range p.BitLocker {
		if !strings.EqualFold(v.Status, "On") && v.Status != "1" {
			unprotected++
		}
	}
	if unprotected > 0 {
		fs = append(fs, hostSecFinding{
			Level: "high", ID: "win_bitlocker_off", Title: "系统盘未启用 BitLocker 加密",
			Detail:  fmt.Sprintf("%d 个系统卷未加密", unprotected),
			Suggest: "对便携/外置设备启用 BitLocker 全盘加密并托管恢复密钥",
		})
	}
	if isTrue(p.SMB1) {
		fs = append(fs, hostSecFinding{
			Level: "critical", ID: "win_smb1", Title: "SMBv1 协议已启用",
			Detail:  "EnableSMB1Protocol=true",
			Suggest: "SMBv1 是 WannaCry/EternalBlue 的传播面，立即禁用：Set-SmbServerConfiguration -EnableSMB1Protocol $false",
		})
	}
	if isTrue(p.RDPEnabled) && isFalse(p.RDPNLA) {
		fs = append(fs, hostSecFinding{
			Level: "high", ID: "win_rdp_nla", Title: "RDP 未启用网络级身份验证",
			Detail:  "UserAuthentication=0",
			Suggest: "启用 NLA，避免未认证会话消耗与预认证漏洞暴露",
		})
	}
	if isFalse(p.UAC) {
		fs = append(fs, hostSecFinding{
			Level: "high", ID: "win_uac_off", Title: "UAC 已关闭",
			Detail: "EnableLUA=0", Suggest: "开启 UAC，恢复管理员操作的提权确认",
		})
	}
	if isTrue(p.AutoLogon) {
		fs = append(fs, hostSecFinding{
			Level: "critical", ID: "win_autologon", Title: "启用了明文密码自动登录",
			Detail:  "Winlogon AutoAdminLogon=1 且存在 DefaultPassword",
			Suggest: "注册表中的 DefaultPassword 为明文，任何本地用户均可读取。请关闭自动登录并改密",
		})
	}
	if isTrue(p.Guest) {
		fs = append(fs, hostSecFinding{
			Level: "high", ID: "win_guest_enabled", Title: "Guest 账号已启用",
			Detail: "Guest.Enabled=true", Suggest: "禁用来宾账号：net user guest /active:no",
		})
	}
	if n := len(p.Admins); n > 5 {
		fs = append(fs, hostSecFinding{
			Level: "medium", ID: "win_admin_sprawl", Title: "本地管理员组成员过多",
			Detail:  fmt.Sprintf("%d 名成员: %s", n, truncateStr(strings.Join(p.Admins, ", "), 200)),
			Suggest: "按最小权限收敛本地管理员，改用 LAPS/特权账号申领",
		})
	}
	if isTrue(p.PSv2) {
		fs = append(fs, hostSecFinding{
			Level: "medium", ID: "win_psv2", Title: "PowerShell 2.0 引擎已启用",
			Detail:  "MicrosoftWindows-PowerShellV2=Enabled",
			Suggest: "PSv2 可绕过脚本块日志与 AMSI，建议卸载该可选功能",
		})
	}
	if p.LLMNRDisabled != nil && !*p.LLMNRDisabled {
		fs = append(fs, hostSecFinding{
			Level: "low", ID: "win_llmnr", Title: "LLMNR 未通过策略禁用",
			Detail:  "DNSClient/EnableMulticast != 0",
			Suggest: "LLMNR/NBT-NS 投毒是内网凭据窃取常用手法，建议组策略禁用",
		})
	}
	if isTrue(p.RebootPending) {
		fs = append(fs, hostSecFinding{
			Level: "low", ID: "win_reboot_pending", Title: "存在待重启的更新",
			Detail: "CBS RebootPending", Suggest: "安排窗口重启，未生效的补丁不提供防护",
		})
	}
	fs = append(fs, winPasswordPolicyFindings(p.NetAccounts)...)
	return fs
}

func boolStr(b *bool) string {
	if b == nil {
		return "unknown"
	}
	return strconv.FormatBool(*b)
}

// winPasswordPolicyFindings parses `net accounts` output (locale-independent by
// matching on the numeric tail of each line).
func winPasswordPolicyFindings(out string) []hostSecFinding {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	num := func(keys ...string) (int, bool) {
		for _, line := range strings.Split(out, "\n") {
			low := strings.ToLower(line)
			matched := false
			for _, k := range keys {
				if strings.Contains(low, strings.ToLower(k)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			if n, err := strconv.Atoi(f[len(f)-1]); err == nil {
				return n, true
			}
		}
		return 0, false
	}
	var fs []hostSecFinding
	if n, ok := num("Minimum password length", "密码最短长度"); ok && n < 8 {
		fs = append(fs, hostSecFinding{
			Level: "medium", ID: "win_pass_min_len", Title: "密码最小长度不足",
			Detail: fmt.Sprintf("Minimum password length=%d", n), Suggest: "设置为 ≥8 并启用复杂度要求",
		})
	}
	if n, ok := num("Lockout threshold", "锁定阈值"); ok && n == 0 {
		fs = append(fs, hostSecFinding{
			Level: "medium", ID: "win_no_lockout", Title: "未配置账号锁定阈值",
			Detail: "Lockout threshold=0", Suggest: "设置登录失败锁定（如 5 次），抑制在线暴力破解",
		})
	}
	return fs
}

// --- macOS ---

func collectDarwinPostureFindings() []hostSecFinding {
	var fs []hostSecFinding
	if out := strings.TrimSpace(cmdOut(6, "spctl", "--status")); out != "" &&
		strings.Contains(strings.ToLower(out), "disabled") {
		fs = append(fs, hostSecFinding{
			Level: "high", ID: "mac_gatekeeper_off", Title: "Gatekeeper 已关闭",
			Detail: out, Suggest: "sudo spctl --master-enable，恢复对未签名应用的拦截",
		})
	}
	if out := strings.TrimSpace(cmdOut(6, "systemsetup", "-getremotelogin")); out != "" &&
		strings.Contains(strings.ToLower(out), "on") {
		fs = append(fs, hostSecFinding{
			Level: "low", ID: "mac_remote_login", Title: "远程登录（SSH）已开启",
			Detail: out, Suggest: "无需远程运维时关闭；保留时限制来源并改用密钥认证",
		})
	}
	if out := strings.TrimSpace(cmdOut(8, "defaults", "read",
		"/Library/Preferences/com.apple.SoftwareUpdate", "AutomaticCheckEnabled")); out == "0" {
		fs = append(fs, hostSecFinding{
			Level: "low", ID: "mac_autoupdate_off", Title: "未启用自动检查系统更新",
			Detail: "AutomaticCheckEnabled=0", Suggest: "开启自动更新检查，及时获取安全补丁",
		})
	}
	return fs
}
