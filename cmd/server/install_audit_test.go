package main

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestSanitizeAuditInstallOptions(t *testing.T) {
	got := sanitizeAuditInstallOptions(map[string]string{
		"sni_enabled":                      "false",
		"sni_interface":                    `eth0";reboot`,
		"content_audit":                    "true",
		"content_audit_ports":              "11434, 8000, 0, 65536, nope, 11434",
		"content_audit_max_body":           "999999",
		"capture_backend":                  "tshark",
		"content_audit_body_mode":          "metadata",
		"content_audit_include_hosts":      "*.Example.com,evil$host,*.Example.com",
		"content_audit_exclude_paths":      "/health*,/metrics*",
		"content_audit_max_events_per_min": "999999",
	})
	if !got.SNIEnabled || !got.ContentAudit {
		t.Fatal("content audit must imply collector enabled")
	}
	if got.SNIInterface != "eth0reboot" {
		t.Fatalf("interface not sanitized: %q", got.SNIInterface)
	}
	if got.ContentAuditPorts != "[11434,8000]" {
		t.Fatalf("ports = %q", got.ContentAuditPorts)
	}
	if got.ContentAuditMaxBody != 65536 {
		t.Fatalf("max body = %d", got.ContentAuditMaxBody)
	}
	if got.CaptureBackend != "tshark" || got.ContentAuditBodyMode != "metadata" {
		t.Fatalf("capture policy not sanitized: %+v", got)
	}
	if got.ContentAuditIncludeHosts != `["*.example.com","evilhost"]` {
		t.Fatalf("host patterns = %s", got.ContentAuditIncludeHosts)
	}
	if got.ContentAuditMaxEventsPerMin != 100000 {
		t.Fatalf("event limit = %d", got.ContentAuditMaxEventsPerMin)
	}
}

func extractInstallConfigYAML(script string) (string, error) {
	re := regexp.MustCompile(`(?:AIOPS_CONFIG_B64|AiopsConfigB64)\s*=\s*'([A-Za-z0-9+/=]+)'`)
	m := re.FindStringSubmatch(script)
	if len(m) < 2 {
		return "", fmt.Errorf("install script missing CONFIG_B64")
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func TestRenderInstallAuditConfig(t *testing.T) {
	opts := installAuditOptions{
		SNIEnabled: true, SNIInterface: "eth0", ContentAudit: true,
		CaptureBackend: "tshark", ContentAuditPorts: "[11434,8000]", ContentAuditMaxBody: 8192,
		ContentAuditBodyMode: "redacted", ContentAuditIncludeHosts: `["*.example.com"]`,
		ContentAuditExcludePaths: `["/health*","/metrics*","/ready*","/live*"]`, ContentAuditMaxEventsPerMin: 1200,
	}
	for name, tmpl := range map[string]string{"sh": installShTemplate, "ps1": installPs1Template} {
		out := renderScriptWithAudit(tmpl, "https://monitor.example", "tok", "prod", "", "", "[]", opts)
		cfg, err := extractInstallConfigYAML(out)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range []string{
			"enabled: true", "content_audit: true",
			"content_audit_ports: [11434,8000]", "content_audit_max_body: 8192",
			"capture_backend:", "tshark", "content_audit_body_mode:",
			"content_audit_include_hosts: [\"*.example.com\"]",
			"content_audit_max_events_per_min: 1200",
			"container_interval_sec",
			"完整配置参考",
		} {
			if !strings.Contains(cfg, want) {
				t.Errorf("%s config missing %q", name, want)
			}
		}
		if strings.Contains(out, "__CONFIG_B64__") || strings.Contains(out, "__CONTENT_AUDIT") || strings.Contains(out, "__SNI_") {
			t.Errorf("%s installer has unresolved placeholders", name)
		}
		// Reference section must be commented (no active duplicate server key after header).
		if !strings.Contains(cfg, "# server:") && !strings.Contains(cfg, "#server:") {
			// example starts with comments; ensure at least one commented key exists
			if !strings.Contains(cfg, "# report_interval:") {
				t.Errorf("%s: expected commented reference keys", name)
			}
		}
	}
}

func TestBuildInstallConfigYAMLAnnotated(t *testing.T) {
	cfg := buildInstallConfigYAML("http://s:8529", "tok", "prod", "", "", "[]", installAuditOptions{}, false)
	if !strings.Contains(cfg, `server: "http://s:8529"`) {
		t.Fatal("missing active server")
	}
	if !strings.Contains(cfg, "完整配置参考") {
		t.Fatal("missing reference section")
	}
	if !strings.Contains(cfg, "# container_interval_sec:") && !strings.Contains(cfg, "#container_interval_sec") {
		// active section has commented container lines; reference has them too
		if !strings.Contains(cfg, "container_interval_sec") {
			t.Fatal("missing container_interval_sec docs")
		}
	}
}
