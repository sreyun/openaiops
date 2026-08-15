//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestLinuxUnitNeedsPrivilegeHeal(t *testing.T) {
	good := `[Service]
User=root
ProtectHome=false
ProtectSystem=false
PrivateTmp=false
NoNewPrivileges=false
`
	if linuxUnitNeedsPrivilegeHeal(good, false) {
		t.Fatal("good unit should not need heal")
	}
	badUser := `[Service]
User=alice
ProtectHome=false
ProtectSystem=false
PrivateTmp=false
NoNewPrivileges=false
`
	if !linuxUnitNeedsPrivilegeHeal(badUser, false) {
		t.Fatal("non-root User must heal")
	}
	if linuxUnitNeedsPrivilegeHeal(badUser, true) {
		t.Fatal("allow-nonroot + unlock should not heal")
	}
	if !linuxUnitNeedsPrivilegeHeal("ProtectSystem=strict\nProtectHome=false\n", false) {
		t.Fatal("ProtectSystem=strict must heal")
	}
}

func TestLinuxUnitNeedsKillModeHeal(t *testing.T) {
	if linuxUnitNeedsKillModeHeal("KillMode=process\n") {
		t.Fatal("process is the desired mode")
	}
	if !linuxUnitNeedsKillModeHeal("KillMode=mixed\n") {
		t.Fatal("mixed must heal — it SIGKILLs terminal-started Java on Agent stop")
	}
	if !linuxUnitNeedsKillModeHeal("KillMode=control-group\n") {
		t.Fatal("control-group must heal")
	}
	if !linuxUnitNeedsKillModeHeal("[Service]\nUser=root\n") {
		t.Fatal("missing KillMode defaults to control-group and must heal")
	}
}

func TestHealLinuxUnitBody(t *testing.T) {
	in := `[Unit]
Description=AIOps Agent
[Service]
Type=simple
User=ubuntu
Group=ubuntu
Environment=HOME=/home/ubuntu
Environment=USER=ubuntu
Environment=LOGNAME=ubuntu
ExecStart=/opt/aiops-agent/aiops-agent --config /opt/aiops-agent/config.yaml
ProtectHome=read-only
ProtectSystem=strict
PrivateTmp=true
NoNewPrivileges=true
CapabilityBoundingSet=CAP_NET_RAW
[Install]
WantedBy=multi-user.target
`
	out, changed := healLinuxUnitBody(in, false)
	if !changed {
		t.Fatal("expected change")
	}
	for _, want := range []string{
		"User=root",
		"Group=root",
		"ProtectHome=false",
		"ProtectSystem=false",
		"PrivateTmp=false",
		"NoNewPrivileges=false",
		"Environment=HOME=/root",
		"Environment=USER=root",
		"ExecStart=/opt/aiops-agent/aiops-agent --config /opt/aiops-agent/config.yaml",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CapabilityBoundingSet=") {
		t.Fatal("CapabilityBoundingSet should be stripped")
	}
	if strings.Contains(out, "ProtectHome=read-only") || strings.Contains(out, "User=ubuntu") {
		t.Fatal("old sandbox/user lines must be gone")
	}
	if !strings.Contains(out, "KillMode=process") {
		t.Fatal("healed unit must pin KillMode=process")
	}
}

func TestHealLinuxUnitBodyRewritesMixedKillModeWithoutTouchingUser(t *testing.T) {
	in := `[Service]
User=root
ProtectHome=false
ProtectSystem=false
PrivateTmp=false
NoNewPrivileges=false
KillMode=mixed
`
	out, changed := healLinuxUnitBody(in, false)
	if !changed {
		t.Fatal("KillMode=mixed must be rewritten")
	}
	if !strings.Contains(out, "KillMode=process") || strings.Contains(out, "KillMode=mixed") {
		t.Fatalf("KillMode not rewritten:\n%s", out)
	}
	if !strings.Contains(out, "User=root") {
		t.Fatal("privilege-clean unit should keep User=root")
	}
}

func TestForceCleanUnitBody(t *testing.T) {
	in := `[Service]
User=ubuntu
ExecStart=/opt/aiops-agent/aiops-agent --config /opt/aiops-agent/config.yaml
ProtectSystem=strict
`
	out := forceCleanUnitBody(in, false)
	for _, want := range []string{
		"User=root",
		"ProtectSystem=false",
		"ProtectHome=false",
		"ExecStart=/opt/aiops-agent/aiops-agent --config /opt/aiops-agent/config.yaml",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	svc := forceCleanUnitBody(`[Service]
ExecStart=/usr/local/bin/aiops-agent --service --config /etc/aiops/config.yaml
`, false)
	if !strings.Contains(svc, "--service --config /etc/aiops/config.yaml") {
		t.Fatalf("service flag lost:\n%s", svc)
	}
}
