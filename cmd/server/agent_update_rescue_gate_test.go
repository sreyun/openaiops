package main

import (
	"errors"
	"strings"
	"testing"
)

// TestModuleFailureFallsBackToServerScript pins the rule that decides whether a
// failed module update gets a second chance through the server-generated legacy
// script.
//
// 这条规则是「Windows 机群修不好」的结构性根源：Agent 侧的助手脚本由 Agent 自己的
// 代码生成，坏了就送不进修复；唯一能单方面修好现网的是服务端下发的 legacy 脚本。
// 此前只要模块自己报错（输出带 "agent_update:"）就一律不回退，而现网最常见的失败
// ——下载被掐、老根证书库连不上证书链——恰好全在这一类里。
func TestModuleFailureFallsBackToServerScript(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		// Transport-class: the legacy script downloads differently (server-pinned
		// SHA-256 lets it survive a broken chain), so retrying is worth it.
		{"download refused", "agent_update: download failed: dial tcp 10.0.0.5:8529: connection refused", true},
		{"tls chain", `agent_update: download failed: x509: certificate signed by unknown authority`, true},
		{"http status", "agent_update: download failed: HTTP 502 from https://mon/dl/aiops-agent.exe", true},
		{"no base", "agent_update: missing server URL", true},
		{"base rejected", "agent_update: server URL not allowed", true},
		{"exe unresolvable", "agent_update: resolve executable: readlink /proc/self/exe: no such file", true},

		// Integrity / compatibility: the script fetches the same artifact and
		// reaches the same verdict, so a retry only doubles the failure.
		{"checksum", "agent_update: download failed: SHA-256 mismatch for aiops-agent.exe (want a got b)", false},
		{"not runnable", "agent_update: download failed: aiops-agent.exe not runnable: exit status 1", false},
		{"unsupported", "agent_update: unsupported platform", false},

		// Concurrency: a second helper racing the same swap is worse than waiting.
		{"already running", "agent_update: another self-update is already running on this host (skipped)", false},

		// Pre-existing behaviour must not regress.
		{"unknown module", "未知模块: agent_update", true},
		{"helper spawn", "agent_update: start update helper: scheduled task did not run", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldLegacyAgentUpdateFallback(tc.out, errors.New("module agent_update failed")); got != tc.want {
				t.Fatalf("shouldLegacyAgentUpdateFallback(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

// The evidence command must survive the exec path of the agents already in the
// field, whose cmd.exe wrapper mangles every double quote (see useRawCmdLine).
// -EncodedCommand is the only quote-free shape.
func TestWindowsUpdateEvidenceCommandIsQuoteFree(t *testing.T) {
	cmd := windowsUpdateEvidenceCommand()
	if strings.Contains(cmd, `"`) {
		t.Fatalf("evidence command carries a double quote, which old agents will mangle:\n%s", cmd)
	}
	if !strings.Contains(cmd, "-EncodedCommand ") {
		t.Fatal("evidence command must be sent as -EncodedCommand")
	}
	if len(cmd) > 2000 {
		t.Fatalf("evidence command is %d chars; keep it far below the cmd.exe limit", len(cmd))
	}
	// It has to actually read the helper's own files, or it proves nothing.
	body := decodeLegacyWindowsPS(t, cmd)
	for _, want := range []string{"aiops-agent-update.log", "aiops-agent-update.result", "ProgramData"} {
		if !strings.Contains(body, want) {
			t.Fatalf("evidence script does not read %s:\n%s", want, body)
		}
	}
}
