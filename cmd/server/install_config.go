package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"strings"
)

// configExampleYAML is the full commented agent config reference.
// Kept in sync with repo-root config.example.yaml (see go:generate).
//
//go:generate go run copy_example.go
//go:embed config_example.yaml
var configExampleYAML []byte

// buildInstallConfigYAML produces the post-install config.yaml:
// active connection settings first, then the full option reference as comments
// so operators can uncomment sections without hunting docs.
func buildInstallConfigYAML(server, token, category, folderID, serversJSON, logPaths string, audit installAuditOptions, windows bool) string {
	if strings.TrimSpace(logPaths) == "" {
		logPaths = "[]"
	}
	diskPath := "/"
	python := "python3"
	if windows {
		diskPath = `C:\`
		python = "python"
	}
	var b strings.Builder
	b.WriteString("# ============================================================================\n")
	b.WriteString("# AIOps · Agent 配置（安装脚本生成）\n")
	b.WriteString("# ----------------------------------------------------------------------------\n")
	b.WriteString("# 上方为当前生效项；下方「完整配置参考」默认全部注释，取消注释即可启用可选采集器。\n")
	b.WriteString("# 修改后请重启 Agent（systemctl restart aiops-agent / 重开 start-agent）。\n")
	b.WriteString("# 同目录另有 config.example.yaml（Agent 首次启动写入）可对照。\n")
	b.WriteString("# ============================================================================\n\n")

	if strings.TrimSpace(serversJSON) != "" {
		b.WriteString("servers: ")
		b.WriteString(serversJSON)
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "server: %q\n", server)
		fmt.Fprintf(&b, "token: %q\n", token)
	}
	fmt.Fprintf(&b, "category: %q\n", category)
	if strings.TrimSpace(folderID) != "" {
		fmt.Fprintf(&b, "folder_id: %q\n", folderID)
	}
	fmt.Fprintf(&b, "log_paths: %s\n", logPaths)
	b.WriteString("report_interval: 30\n")
	b.WriteString("plugin_interval: 60\n")
	fmt.Fprintf(&b, "disk_path: %q\n", diskPath)
	fmt.Fprintf(&b, "python: %q\n", python)
	b.WriteString("plugins_dir: \"plugins\"\n")
	b.WriteString("state_file: \"agent_state.json\"\n")
	b.WriteString("log_encrypt: true\n")
	b.WriteString("tls_skip_verify: false\n")
	b.WriteString("ca_cert: \"\"\n")
	b.WriteString("# hyperv_interval_sec: 60\n")
	b.WriteString("# hyperv_disabled: false\n")
	b.WriteString("# container_interval_sec: 60\n")
	b.WriteString("# container_disabled: false\n")
	b.WriteString("\nsni_dns_capture:\n")
	fmt.Fprintf(&b, "  enabled: %v\n", audit.SNIEnabled || audit.ContentAudit)
	fmt.Fprintf(&b, "  interface: %q\n", audit.SNIInterface)
	fmt.Fprintf(&b, "  capture_backend: %q\n", audit.CaptureBackend)
	b.WriteString("  max_entries_per_min: 5000\n")
	b.WriteString("  tls_metadata_ports: [443,8443,9443]\n")
	fmt.Fprintf(&b, "  content_audit: %v\n", audit.ContentAudit)
	fmt.Fprintf(&b, "  content_audit_ports: %s\n", audit.ContentAuditPorts)
	fmt.Fprintf(&b, "  content_audit_max_body: %d\n", audit.ContentAuditMaxBody)
	fmt.Fprintf(&b, "  content_audit_body_mode: %q\n", audit.ContentAuditBodyMode)
	fmt.Fprintf(&b, "  content_audit_include_hosts: %s\n", audit.ContentAuditIncludeHosts)
	fmt.Fprintf(&b, "  content_audit_exclude_paths: %s\n", audit.ContentAuditExcludePaths)
	fmt.Fprintf(&b, "  content_audit_max_events_per_min: %d\n", audit.ContentAuditMaxEventsPerMin)

	b.WriteString("\n# ============================================================================\n")
	b.WriteString("# 完整配置参考（默认注释状态 · 与 config.example.yaml 同步）\n")
	b.WriteString("# 需要启用某采集器时：去掉对应行首 #，按说明填写后重启 Agent。\n")
	b.WriteString("# ============================================================================\n")
	b.WriteString(commentYAMLAsReference(string(configExampleYAML)))
	return b.String()
}

func commentYAMLAsReference(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trim := strings.TrimLeft(line, " \t")
		if trim == "" {
			b.WriteString("#\n")
			continue
		}
		if strings.HasPrefix(trim, "#") {
			b.WriteString(line)
			b.WriteByte('\n')
			continue
		}
		b.WriteString("# ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func installConfigB64(server, token, category, folderID, serversJSON, logPaths string, audit installAuditOptions, windows bool) string {
	yaml := buildInstallConfigYAML(server, token, category, folderID, serversJSON, logPaths, audit, windows)
	return base64.StdEncoding.EncodeToString([]byte(yaml))
}
