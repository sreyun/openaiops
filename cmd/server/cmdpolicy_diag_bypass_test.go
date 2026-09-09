package main

import "testing"

// AI run_diagnostic used to allow shell globs and the Windows default install
// path past the sensitive-path denylist. Commands run under /bin/sh -c on the
// agent, so cat /etc/shado* expands after a literal denylist and exfiltrates
// /etc/shadow; the same hole leaked Agent config.yaml (install token).
func TestDiagCommandBlocksGlobAndWindowsAgentConfig(t *testing.T) {
	blocked := []string{
		"cat /etc/shado*",
		"cat /etc/shado?",
		"cat /opt/aiops-agent/config.ya*",
		"head -n 20 /etc/./shado*",
		"cat /proc/self/enviro*",
		`cat "C:/Program Files/AIOps Agent/config.yaml"`,
		`cat 'C:/Program Files/AIOps Agent/config.json'`,
		"cat C:/ProgramData/aiops-agent/agent_state.json",
		"cat /opt/aiops/config.yaml", // custom AIOPS_DIR without "-agent" suffix
	}
	for _, cmd := range blocked {
		if ok, reason := diagCommandAllowed(cmd); ok {
			t.Errorf("should block %q, but allowed", cmd)
		} else if reason == "" {
			t.Errorf("blocked %q without reason", cmd)
		}
	}
}

func TestDiagCommandStillAllowsOrdinaryReadsAfterGlobFix(t *testing.T) {
	allowed := []string{
		"df -hT",
		"cat /proc/meminfo",
		"tail -n 200 /var/log/messages",
		"journalctl -n 100 --no-pager",
		"ps aux | head -20",
		"cat /opt/app/application.yaml",
		"cat /opt/app/config.yaml", // app config without "aiops" in path
	}
	for _, cmd := range allowed {
		if ok, reason := diagCommandAllowed(cmd); !ok {
			t.Errorf("ordinary read blocked: %q (%s)", cmd, reason)
		}
	}
}

func TestDeniedSensitivePathCoversWindowsAIOpsAgent(t *testing.T) {
	for _, p := range []string{
		`C:\Program Files\AIOps Agent\config.yaml`,
		`C:/Program Files/AIOps Agent/config.yaml`,
		`/opt/aiops/config.yaml`,
		`D:/AIOps Agent/agent_state.json`,
	} {
		if !deniedSensitivePath(p) {
			t.Errorf("Windows/custom AIOps install path not denied: %s", p)
		}
	}
	if deniedSensitivePath("/opt/app/config.yaml") {
		t.Error("unrelated app config.yaml must not be denied")
	}
}
