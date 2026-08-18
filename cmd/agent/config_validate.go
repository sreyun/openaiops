package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// resolveConfigRelativePaths anchors relative file/directory settings to the
// directory that holds config.yaml.
//
// A service manager decides the working directory, and on Windows the SCM picks
// C:\Windows\System32. The installer ships `state_file: "agent_state.json"`, so
// the LocalSystem service wrote the host identity to System32 while the per-user
// wscript supervisor and any manual run wrote it next to the binary. Every launch
// context therefore minted its OWN host_id and the same machine showed up as
// several hosts (or kept flapping between them). `plugins_dir: "plugins"` had the
// same problem and simply never resolved, so a service install silently ran with
// zero plugins even though the installer had just extracted them.
//
// Anchoring to the config directory is a no-op for setups that already ran with
// the working directory set to the install dir, so upgrades keep their paths.
func resolveConfigRelativePaths(cfg *config, cfgPath string) {
	if cfg == nil {
		return
	}
	base := configBaseDir(cfgPath)
	if base == "" {
		return
	}
	cfg.StateFile = anchorPath(base, cfg.StateFile)
	cfg.PluginsDir = anchorPath(base, cfg.PluginsDir)
}

// agentConfigCandidates 是配置文件的查找顺序：先按**工作目录**找，再按**可执行文件
// 所在目录**找。
//
// 第二段是一次现网故障的直接修复。Windows 服务由 SCM 拉起时工作目录是
// C:\Windows\System32，不是安装目录。ImagePath 里的 --config 一旦丢了或写成相对路径，
// "config.yaml" 就被解析成 C:\Windows\System32\config.yaml——找不到，然后 Agent
// **不报错、不退出**，带着默认的 localhost:8529 一直跑下去：服务状态正常、进程活着、
// 二进制是最新的、重启多少次都一样，而控制台上这台主机永远离线。
//
// 更难查的是证据也跟着跑偏了：startServiceFileLog 用的是同一个 cfgPath 推出来的目录，
// 于是运行日志、config.example.yaml、agent_state.json 全落进了 System32——东西一直在
// 写，只是写在了没人会去看的地方。
//
// resolveConfigRelativePaths 早就为 state_file / plugins_dir 做过同一件事（那一次的现场
// 是「同一台机器在多个 host 之间反复横跳」）。配置文件自身反而一直没有——补上之后，
// 服务无论被怎样注册（绝对 --config、相对 --config、或者根本没有 --config），都能找回
// 装在自己旁边的那份配置。
func agentConfigCandidates() []string {
	names := []string{"config.yaml", "config.yml", "config.json"}
	out := append([]string{}, names...)
	exe, err := os.Executable()
	if err != nil {
		return out
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if dir == "" || dir == "." {
		return out
	}
	for _, n := range names {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// exeDir 返回可执行文件所在目录（解析软链后），取不到返回 ""。
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// agentLogBaseDir 决定运行日志写到哪里。
//
// 配置**读到了**就写在它旁边（历史行为，安装目录）。一个字节都没读到时不能沿用
// configBaseDir——那会把 filepath.Abs("config.yaml") 锚到工作目录上，而 Windows 服务的
// 工作目录是 C:\Windows\System32。现网就是这么丢的：ImagePath 里的 --config 被空格截断
// 之后，唯一写着原因的那条 WARN 落进了 System32\agent.log，没人会去那里找。
// 读不到配置时锚到二进制旁边，至少人能在安装目录里看见它。
func agentLogBaseDir(cfgPath string, cfgFound bool) string {
	if cfgFound {
		return configBaseDir(cfgPath)
	}
	if d := exeDir(); d != "" {
		return d
	}
	return configBaseDir(cfgPath)
}

// firstExistingBesideExe 返回可执行文件旁边第一个真实存在的配置文件，没有则返回 ""。
func firstExistingBesideExe() string {
	for _, c := range agentConfigCandidates() {
		if !filepath.IsAbs(c) {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// configBaseDir returns the absolute directory holding the config file, falling
// back to the directory of the running executable when the path cannot be made
// absolute (the executable dir is where the installer puts config.yaml anyway).
func configBaseDir(cfgPath string) string {
	if p := strings.TrimSpace(cfgPath); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			return filepath.Dir(abs)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

func anchorPath(base, p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	// A Windows path like `C:foo` or `\foo` is not reported absolute by
	// filepath.IsAbs but is still drive/root relative — joining it would produce
	// nonsense, so leave it to the OS.
	if runtime.GOOS == "windows" && (filepath.VolumeName(p) != "" || strings.HasPrefix(p, `\`) || strings.HasPrefix(p, "/")) {
		return p
	}
	return filepath.Join(base, p)
}

// normalizeAndValidateConfig fails closed on dangerous/broken agent
// configuration. In particular, an explicitly configured agent must never
// silently fall back to localhost after a YAML/JSON error.
func normalizeAndValidateConfig(cfg *config) error {
	if cfg == nil {
		return fmt.Errorf("配置为空")
	}
	if cfg.ReportInterval < 5 || cfg.ReportInterval > 3600 {
		return fmt.Errorf("report_interval 必须在 5..3600 秒之间")
	}
	if cfg.PluginInterval < 5 || cfg.PluginInterval > 86400 {
		return fmt.Errorf("plugin_interval 必须在 5..86400 秒之间")
	}
	if strings.TrimSpace(cfg.PluginsDir) == "" {
		cfg.PluginsDir = "plugins"
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		cfg.StateFile = "agent_state.json"
	}

	// Gateway relay: single upstream only — servers[] would leave cfg.Server empty
	// and break the reverse-proxy target.
	if cfg.Relay {
		if len(cfg.Servers) > 0 {
			return fmt.Errorf("relay 模式不支持 servers 多服务端配置，请使用单一 server")
		}
		cfg.Server = strings.TrimRight(strings.TrimSpace(cfg.Server), "/")
		if cfg.Server == "" {
			return fmt.Errorf("relay 模式必须配置 server（上游云监控地址）")
		}
		u, err := url.Parse(cfg.Server)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return fmt.Errorf("relay server 必须是无内嵌凭据的 http/https URL")
		}
		if strings.TrimSpace(cfg.Listen) == "" {
			cfg.Listen = ":8529"
		}
	} else {
		targets := cfg.Servers
		if len(targets) == 0 && strings.TrimSpace(cfg.Server) != "" {
			targets = []ServerConfig{{Server: cfg.Server, Token: cfg.Token}}
		}
		if len(targets) == 0 {
			return fmt.Errorf("至少配置一个 server 或 servers")
		}
		for i := range targets {
			targets[i].Server = strings.TrimRight(strings.TrimSpace(targets[i].Server), "/")
			u, err := url.Parse(targets[i].Server)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return fmt.Errorf("servers[%d].server 必须是无内嵌凭据的 http/https URL", i)
			}
		}
		if len(cfg.Servers) > 0 {
			cfg.Servers = targets
		} else {
			cfg.Server = targets[0].Server
		}
	}

	if cfg.SNI != nil {
		cfg.SNI.CaptureBackend = strings.ToLower(strings.TrimSpace(cfg.SNI.CaptureBackend))
		if cfg.SNI.CaptureBackend == "" {
			cfg.SNI.CaptureBackend = "auto"
		}
		switch cfg.SNI.CaptureBackend {
		case "auto", "native", "tshark":
		default:
			return fmt.Errorf("sni_dns_capture.capture_backend 必须是 auto/native/tshark")
		}
		if cfg.SNI.CaptureBackend == "native" && runtime.GOOS != "linux" {
			return fmt.Errorf("native 抓包后端仅支持 Linux；%s 请使用 tshark", runtime.GOOS)
		}
		cfg.SNI.Interface = strings.TrimSpace(cfg.SNI.Interface)
		if len(cfg.SNI.Interface) > 128 || strings.ContainsAny(cfg.SNI.Interface, "\r\n\x00") {
			return fmt.Errorf("sni_dns_capture.interface 非法或过长")
		}
		cfg.SNI.TSharkPath = strings.TrimSpace(cfg.SNI.TSharkPath)
		if cfg.SNI.TSharkPath != "" {
			base := strings.ToLower(filepath.Base(cfg.SNI.TSharkPath))
			if base != "tshark" && base != "tshark.exe" {
				return fmt.Errorf("sni_dns_capture.tshark_path 必须指向 tshark 可执行文件")
			}
		}
		if cfg.SNI.MaxEntriesPerMin < 0 || cfg.SNI.MaxEntriesPerMin > 100000 {
			return fmt.Errorf("sni_dns_capture.max_entries_per_min 必须在 0..100000 之间")
		}
		if cfg.SNI.MaxEntriesPerMin == 0 {
			cfg.SNI.MaxEntriesPerMin = 5000
		}
		if len(cfg.SNI.TLSMetadataPorts) == 0 {
			cfg.SNI.TLSMetadataPorts = []int{443, 8443, 9443}
		}
		var err error
		if cfg.SNI.TLSMetadataPorts, err = normalizeAuditPorts(cfg.SNI.TLSMetadataPorts, 32); err != nil {
			return fmt.Errorf("tls_metadata_ports: %w", err)
		}
		if cfg.SNI.ContentAudit {
			cfg.SNI.Enabled = true
			if len(cfg.SNI.ContentAuditPorts) == 0 {
				cfg.SNI.ContentAuditPorts = []int{11434, 8000, 8080}
			}
			if cfg.SNI.ContentAuditPorts, err = normalizeAuditPorts(cfg.SNI.ContentAuditPorts, 32); err != nil {
				return fmt.Errorf("content_audit_ports: %w", err)
			}
			if cfg.SNI.ContentAuditMaxBody == 0 {
				cfg.SNI.ContentAuditMaxBody = 4096
			}
			if cfg.SNI.ContentAuditMaxBody < 1024 || cfg.SNI.ContentAuditMaxBody > 65536 {
				return fmt.Errorf("content_audit_max_body 必须在 1024..65536 字节之间")
			}
			cfg.SNI.ContentAuditBodyMode = normalizeContentBodyMode(cfg.SNI.ContentAuditBodyMode)
			if cfg.SNI.ContentAuditMaxEventsPerMin == 0 {
				cfg.SNI.ContentAuditMaxEventsPerMin = 2000
			}
			if cfg.SNI.ContentAuditMaxEventsPerMin < 1 || cfg.SNI.ContentAuditMaxEventsPerMin > 100000 {
				return fmt.Errorf("content_audit_max_events_per_min 必须在 1..100000 之间")
			}
			if cfg.SNI.ContentAuditIncludeHosts, err = normalizeAuditPatterns(cfg.SNI.ContentAuditIncludeHosts, 64, 253); err != nil {
				return fmt.Errorf("content_audit_include_hosts: %w", err)
			}
			if cfg.SNI.ContentAuditExcludeHosts, err = normalizeAuditPatterns(cfg.SNI.ContentAuditExcludeHosts, 64, 253); err != nil {
				return fmt.Errorf("content_audit_exclude_hosts: %w", err)
			}
			if len(cfg.SNI.ContentAuditExcludePaths) == 0 {
				cfg.SNI.ContentAuditExcludePaths = []string{"/health*", "/metrics*", "/ready*", "/live*"}
			}
			if cfg.SNI.ContentAuditExcludePaths, err = normalizeAuditPatterns(cfg.SNI.ContentAuditExcludePaths, 64, 512); err != nil {
				return fmt.Errorf("content_audit_exclude_paths: %w", err)
			}
			if cfg.SNI.ContentAuditRedactKeys, err = normalizeAuditRedactKeys(cfg.SNI.ContentAuditRedactKeys); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeAuditPorts(in []int, max int) ([]int, error) {
	seen := map[int]bool{}
	out := make([]int, 0, len(in))
	for _, p := range in {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("含非法端口 %d", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
		if len(out) > max {
			return nil, fmt.Errorf("端口最多允许 %d 个", max)
		}
	}
	return out, nil
}

func normalizeAuditPatterns(in []string, maxCount, maxLen int) ([]string, error) {
	if len(in) > maxCount {
		return nil, fmt.Errorf("规则最多允许 %d 条", maxCount)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		if len(p) > maxLen || strings.ContainsAny(p, "\r\n\x00") {
			return nil, fmt.Errorf("规则非法或过长")
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

func normalizeAuditRedactKeys(in []string) ([]string, error) {
	if len(in) > 64 {
		return nil, fmt.Errorf("content_audit_redact_keys 最多允许 64 个字段")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		k := normalizeAuditKey(raw)
		if k == "" {
			continue
		}
		if len(k) > 64 {
			return nil, fmt.Errorf("content_audit_redact_keys 含过长字段")
		}
		for _, r := range k {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
				return nil, fmt.Errorf("content_audit_redact_keys 含非法字段 %q", raw)
			}
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}
