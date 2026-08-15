package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// TestLogEncryptHandshakeEndToEnd 验证日志加密全链路：注册下发 log_key → 用该密钥加密一批
// 日志 → 服务端 handleAgentLogs 按指纹重派生密钥解密解压并入库；且请求体不含明文。
func TestLogEncryptHandshakeEndToEnd(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "test-master-key-e2e") // 隔离 + 自动还原

	srv, token := newTestServer(t)
	const hostID, fp = "h-log", "fp-log-0001"

	rr := postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": hostID, "hostname": "n", "token": token, "fingerprint": fp,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("注册失败: %d %s", rr.Code, rr.Body)
	}
	var reg struct {
		LogKey     string `json:"log_key"`
		LogEncrypt bool   `json:"log_encrypt"`
	}
	if json.Unmarshal(rr.Body.Bytes(), &reg) != nil || !reg.LogEncrypt || reg.LogKey == "" {
		t.Fatalf("注册响应应下发 log_key + log_encrypt: %s", rr.Body)
	}
	key, err := base64.StdEncoding.DecodeString(reg.LogKey)
	if err != nil || len(key) != 32 {
		t.Fatalf("log_key 非法: len=%d err=%v", len(key), err)
	}

	// 用下发密钥加密一批日志（sealLog 与 agent 的 sealLogAgent 同算法）→ 上报
	batch := shared.LogBatch{HostID: hostID, Lines: []shared.LogLine{{Ts: time.Now().Unix(), Source: "/var/log/x", Level: "error", Message: "secret-boom-42"}}}
	plain, _ := json.Marshal(batch)
	sealed, err := sealLog(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("secret-boom-42")) {
		t.Fatal("密文泄露明文")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/logs", bytes.NewReader(sealed))
	req.Header.Set("X-Log-Enc", "aesgcm-gzip")
	req.Header.Set("X-Agent-Fingerprint", fp)
	rr2 := httptest.NewRecorder()
	srv.handleAgentLogs(rr2, req)
	if rr2.Code != http.StatusOK {
		t.Fatalf("加密日志入库失败: %d %s", rr2.Code, rr2.Body)
	}
}

// newTestServer builds a real Server backed by an in-memory Store and a throwaway
// ConfigStore (no PostgreSQL needed — persistence is orthogonal to the agent
// handshake). It exercises the actual handleRegister / handleReport handlers.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	store := NewStore()
	cfg := newTestConfigStore(t)
	notifier := NewNotifier(store, cfg)
	srv := NewServer(store, cfg, notifier, t.TempDir(), "127.0.0.1:0")
	return srv, cfg.InstallToken()
}

func postJSON(t *testing.T, h http.HandlerFunc, path string, v any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(v)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

// TestAgentHandshakeEndToEnd walks the full agent↔server admission handshake
// through the real HTTP handlers: token-gated registration, fingerprint-gated
// reporting, rejection of spoofed fingerprints, and token-less re-registration
// of a known host (server-restart recovery).
func TestAgentHandshakeEndToEnd(t *testing.T) {
	srv, token := newTestServer(t)
	const hostID = "host-abc"
	const fp = "fp-legit-0001"

	// 1. New agent registers with a VALID token + fingerprint → 200.
	rr := postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": hostID, "hostname": "node-1", "token": token, "fingerprint": fp,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("register with valid token: got %d, want 200 (body: %s)", rr.Code, rr.Body)
	}

	// 2. Authenticated report with the bound fingerprint → 200.
	rep := shared.Report{HostID: hostID, Hostname: "node-1", Fingerprint: fp}
	rr = postJSON(t, srv.handleReport, "/api/v1/agent/report", rep)
	if rr.Code != http.StatusOK {
		t.Fatalf("report with matching fingerprint: got %d, want 200 (body: %s)", rr.Code, rr.Body)
	}

	// 3. Spoofed report: correct host_id but WRONG fingerprint → 403 (this is the
	//    core anti-spoofing guarantee — the fingerprint is the report credential).
	spoof := shared.Report{HostID: hostID, Hostname: "node-1", Fingerprint: "fp-attacker"}
	rr = postJSON(t, srv.handleReport, "/api/v1/agent/report", spoof)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("report with spoofed fingerprint: got %d, want 403", rr.Code)
	}

	// 4. New/unknown host WITHOUT a valid token → 403 (admission is token-gated).
	rr = postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": "host-evil", "hostname": "evil", "token": "wrong-token", "fingerprint": "fp-x",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("register unknown host with bad token: got %d, want 403", rr.Code)
	}

	// 5. KNOWN host re-registers WITHOUT a token but with its MATCHING fingerprint
	//    → 200. This is the server-restart / rotated-token recovery path.
	rr = postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": hostID, "hostname": "node-1", "token": "", "fingerprint": fp,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("known host token-less re-register: got %d, want 200 (body: %s)", rr.Code, rr.Body)
	}

	// 6. But a known host_id with a DIFFERENT fingerprint and no token → 403
	//    (an attacker who learned the host_id but not the fingerprint can't hijack).
	rr = postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": hostID, "hostname": "node-1", "token": "", "fingerprint": "fp-attacker",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("known host wrong-fingerprint token-less re-register: got %d, want 403", rr.Code)
	}

	// 7. Install token may rebind fingerprint (Win11 NIC-order / Agent upgrade).
	const fp2 = "fp-after-stable-mac"
	rr = postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": hostID, "hostname": "node-1", "token": token, "fingerprint": fp2,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("token fingerprint rebind: got %d, want 200 (body: %s)", rr.Code, rr.Body)
	}
	rr = postJSON(t, srv.handleReport, "/api/v1/agent/report", shared.Report{
		HostID: hostID, Hostname: "node-1", Fingerprint: fp2,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("report after fingerprint rebind: got %d, want 200", rr.Code)
	}
	rr = postJSON(t, srv.handleReport, "/api/v1/agent/report", shared.Report{
		HostID: hostID, Hostname: "node-1", Fingerprint: fp,
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("old fingerprint after rebind must 403: got %d", rr.Code)
	}
}

// TestInstallScriptsRobustness renders the install/uninstall templates and
// asserts the cross-platform autostart + keepalive + clean-uninstall guarantees
// are present. When AIOPS_RENDER_DIR is set the rendered scripts are also dumped
// there for external shell/PowerShell syntax checking.
func TestInstallScriptsRobustness(t *testing.T) {
	server, token := "https://mon.example.com", "tok-123"
	shIn := renderScript(installShTemplate, server, token, "prod", "", "", "")
	ps1In := renderScript(installPs1Template, server, token, "prod", "", "", "")
	shUn := renderScript(uninstallShTemplate, server, token, "prod", "", "", "")
	ps1Un := renderScript(uninstallPs1Template, server, token, "prod", "", "", "")

	must := func(name, hay string, needles ...string) {
		for _, n := range needles {
			if !strings.Contains(hay, n) {
				t.Errorf("%s: missing %q", name, n)
			}
		}
	}
	// macOS now gets a real launchd job (autostart on boot + keepalive), and Linux
	// root install uses systemd with Restart=always. Run-as user is the installer
	// (SUDO_USER / AIOPS_USER / root) — never auto-create a dedicated aiops account.
	must("install.sh", shIn,
		`elif [ "$OS" = "Darwin" ]`, "com.aiops.agent.plist",
		"<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><true/>",
		"launchctl load", "Restart=always", "@reboot",
		"User=$AIOPS_USER", "NoNewPrivileges=false",
		"AIOPS_USER=\"root\"", "Never create a dedicated",
		"gui/$AIOPS_UID", "aiops_has_systemd", "aiops_fetch", "unsupported architecture",
		"AmbientCapabilities=CAP_NET_RAW",
		"TERM_SHELL=", "Environment=SHELL=$TERM_SHELL", "Environment=HOME=", "ProtectHome=false",
		"ProtectSystem=false", "PrivateTmp=false", "KillMode=process",
		"<key>EnvironmentVariables</key>", "<key>SHELL</key>", "<key>HOME</key>",
		"aiops_is_installed", "aiops_stop_and_uninstall_existing", "aiops_purge_systemd_unit",
		".service.d",
		"existing agent detected", "systemctl restart aiops-agent",
		"kickstart -k")
	if strings.Contains(shIn, "CapabilityBoundingSet=") {
		t.Error("install.sh must not set CapabilityBoundingSet= (drops other root caps; breaks terminal)")
	}
	// Linux systemd path must default AIOPS_USER=root (macOS may still use SUDO_USER for LaunchAgent).
	linuxIdx := strings.Index(shIn, `aiops_has_systemd`)
	unitIdx := strings.Index(shIn, `/etc/systemd/system/aiops-agent.service`)
	if linuxIdx < 0 || unitIdx < 0 || unitIdx < linuxIdx {
		t.Error("install.sh missing Linux systemd unit path")
	} else {
		linuxBlock := shIn[linuxIdx:unitIdx]
		if !strings.Contains(linuxBlock, `AIOPS_USER="root"`) {
			t.Error("Linux install must default AIOPS_USER=root so remote terminal can write /etc")
		}
		if strings.Contains(linuxBlock, `AIOPS_USER="$SUDO_USER"`) {
			t.Error("Linux install must not default AIOPS_USER to SUDO_USER (vim E45 on /etc)")
		}
	}
	// Match real unit directives (line-start), not prose comments.
	for _, bad := range []string{
		"\nProtectHome=true", "\nProtectHome=read-only", "\nProtectSystem=strict",
		"\nReadWritePaths=", "\nPrivateTmp=true", "\nNoNewPrivileges=true",
	} {
		if strings.Contains(shIn, bad) {
			t.Errorf("install.sh must not set %q (blocks full remote shell access)", strings.TrimSpace(bad))
		}
	}
	if strings.Contains(shIn, "aiops_stop_and_uninstall_existing") {
		// Agent reinstall must not tear down the separate gateway relay service.
		stopFn := shIn
		if i := strings.Index(shIn, "aiops_stop_and_uninstall_existing()"); i >= 0 {
			stopFn = shIn[i:]
			if j := strings.Index(stopFn[1:], "\n}"); j >= 0 {
				stopFn = stopFn[:j+2]
			}
		}
		if strings.Contains(stopFn, "aiops-relay") {
			t.Error("agent reinstall must not purge aiops-relay (separate gateway service)")
		}
	}
	must("uninstall.sh", shUn,
		"aiops_purge_systemd_unit", ".service.d",
		"aiops-agent", "aiops-monitor-agent", "aiops-relay",
		"daemon-reload")
	must("install.ps1 (reinstall)", ps1In,
		"Test-AiopsAlreadyInstalled", "Uninstall-AiopsExisting",
		"existing agent detected", "Restart-Service")
	// YAML is the default config format now: the script must write config.yaml
	// (base64-decoded payload), point the service at it, and migrate away any
	// stale config.json.
	must("install.sh (yaml)", shIn,
		"AIOPS_CONFIG_B64", "config.yaml", "--config $DIR/config.yaml",
		"rm -f config.json")
	// Windows: supervisor VBS (no duplicates) + logon autostart + 5-min keepalive task.
	must("install.ps1", ps1In,
		"start-agent.vbs", "Win32_Process",
		`schtasks.exe`, "/SC MINUTE /MO 5",
		`CurrentVersion\Run`,
		"Remove-AiopsScheduledTask", "Stop-AiopsServiceQuiet",
		"Get-AiopsRemoteFile", "Invoke-WebRequest")
	// Locked-down GPO hosts often block cmd.exe / curl.exe / taskkill.exe.
	if strings.Contains(ps1In, "cmd /c") || strings.Contains(ps1Un, "cmd /c") {
		t.Error("install/uninstall.ps1 must not invoke cmd.exe (GPO blocks it on hardened hosts)")
	}
	if strings.Contains(ps1Un, "& taskkill") || strings.Contains(ps1Un, "taskkill /") {
		t.Error("uninstall.ps1 must not invoke taskkill.exe (GPO blocks it; use Stop-Process)")
	}
	// Agent download must not require curl.exe as the only path.
	must("install.ps1 (download)", ps1In, "Get-AiopsRemoteFile", "WebClient")
	// PowerShell 5.1 defaults to a legacy console code page. The installer must
	// switch both console and native pipeline encodings to UTF-8, and must not
	// pipe Agent stderr through ForEach-Object (which caused Chinese mojibake).
	must("install.ps1 (utf8)", ps1In,
		`[Console]::InputEncoding = $Utf8NoBom`,
		`[Console]::OutputEncoding = $Utf8NoBom`,
		`$global:OutputEncoding = $Utf8NoBom`,
		`chcp.com 65001`)
	if strings.Contains(ps1In, `2>&1 | ForEach-Object`) {
		t.Error("install.ps1 (utf8): Agent output must not pass through the PowerShell 5.1 native-output decoding pipeline")
	}
	// Windows must also write config.yaml (hand-built, no JSON serializer) and
	// remove a stale config.json from a pre-YAML install.
	must("install.ps1 (yaml)", ps1In,
		`WriteAllText("$Dir\config.yaml"`, `$conf = "$Dir\config.yaml"`,
		`Remove-Item "$Dir\config.json"`)
	// Server 2012 / PS<5: ZipFile extract, not Expand-Archive-only.
	must("install.ps1 (zip)", ps1In,
		"System.IO.Compression.ZipFile", "ZipFileExtensions", "capability summary")
	// Prefer elevated Program Files install for Hyper-V / Smart App Control /
	// AppLocker / Windows 10-11 workstations — AppData per-user installs are the
	// common deny target, and disabled WScript leaves a silent "installed" state.
	must("install.ps1 (uac)", ps1In,
		"Get-Service -Name vmms", "-Verb RunAs", "-EncodedCommand",
		"SecurityProtocol", "Request-AiopsElevatedInstall",
		"Test-AiopsSmartAppControlOn", "Test-AiopsAppLockerPresent", "PreferElevated",
		"IsWorkstation", "ProductType", "Test-AiopsWindowsSupported",
		"Test-AiopsNeedsWin2012Agent", "aiops-agent-windows-amd64-win2012.exe",
		"Test-AiopsWScriptEnabled", "Start-Process -FilePath $exe")
	// Elevated installs must register the real Windows service (boot autostart +
	// crash-recovery + interactive desktop worker), which is what makes Hyper-V
	// collection, reboot persistence, and lock-screen remote desktop all work.
	must("install.ps1 (service)", ps1In,
		"--install-service", "AiopsMonitorAgent")
	// Downloads should avoid MOTW; Application Control must be detected early and
	// AppData blocks must auto-retry elevated Program Files (no Session-0 fallback).
	must("install.ps1 (app-control)", ps1In,
		"Unblock-File", "Clear-AiopsMotw", "WriteAllBytes",
		"Test-AiopsAgentRunnable", "Application Control", "Zone.Identifier",
		"allow-aiops-agent.ps1", "New-CIPolicy", "appcontrol-appdata",
		"group policy", `Join-Path $env:ProgramFiles "AIOps Agent"`,
		"windowsdefender://appbrowser", "Path,Hash",
		"stashed host identity", "service failed to reach Running state",
		"aiops|start-agent\\.vbs")
	// A per-user install belongs to the profile that ran it, and elevating swaps
	// HKCU/LOCALAPPDATA to the approving admin — so both scripts must sweep every
	// profile, or the old agent keeps auto-starting and reporting after an
	// "uninstall" while the new install looks like it did nothing.
	for name, body := range map[string]string{"install.ps1": ps1In, "uninstall.ps1": ps1Un} {
		must(name+" (all-user cleanup)", body,
			"Remove-AiopsAllUserInstalls",
			`HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList`,
			"ProfileImagePath", "NTUSER.DAT", "reg.exe",
			`Registry::HKEY_USERS\`, "AppData\\Local\\aiops-agent",
			`System32\agent_state.json`)
	}
	// Reinstalls must not orphan the host record: everything on the server is
	// keyed by host_id, which lives in agent_state.json inside the deleted dir.
	must("install.ps1 (identity)", ps1In,
		"Save-AiopsIdentity", "Restore-AiopsIdentity", "aiops-agent-state.json")
	// "Service is Running" proves nothing about DNS, firewalls or token validity.
	// The installer must verify the handshake and fail loudly instead of printing
	// a green "done" for a host that will never appear.
	must("install.ps1 (selftest)", ps1In,
		"--selftest", "$SelfTestExit", "connectivity: OK", "connectivity: FAILED",
		"agent.log")

	// Uninstall must tear down every autostart mechanism it created.
	must("uninstall.sh", shUn,
		"LaunchDaemons/com.aiops.agent.plist", "launchctl unload", "crontab")
	must("uninstall.ps1", ps1Un,
		"Remove-AiopsScheduledTask", "AIOpsAgent", "AIOpsRelay", "sc.exe")

	if dir := os.Getenv("AIOPS_RENDER_DIR"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		for name, body := range map[string]string{
			"install.sh": shIn, "install.ps1": ps1In,
			"uninstall.sh": shUn, "uninstall.ps1": ps1Un,
		} {
			_ = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		}
		t.Logf("rendered scripts written to %s", dir)
	}
}
