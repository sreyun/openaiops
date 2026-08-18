package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// installAuditOptions controls the cross-platform packet-observation block
// written by the one-line installer. Linux supports the native AF_PACKET
// backend; Windows/macOS use TShark over Npcap/libpcap/BPF.
type installAuditOptions struct {
	SNIEnabled                  bool
	SNIInterface                string
	CaptureBackend              string
	ContentAudit                bool
	ContentAuditPorts           string
	ContentAuditMaxBody         int
	ContentAuditBodyMode        string
	ContentAuditIncludeHosts    string
	ContentAuditExcludePaths    string
	ContentAuditMaxEventsPerMin int
}

// renderScript injects the server URL / token / category / folder_id / serversJSON into an
// install template. Placeholders are used (not fmt) so the shell/PowerShell '%'
// and '$' characters pass through untouched. serversJSON is a pre-validated JSON
// array string (e.g. [{"server":"...","token":"..."}]); when empty the template
// falls back to the single server+token config.
func renderScript(tmpl, server, token, category, folderID, serversJSON, logPaths string) string {
	return renderScriptWithAudit(tmpl, server, token, category, folderID, serversJSON, logPaths, installAuditOptions{})
}

func renderScriptWithAudit(tmpl, server, token, category, folderID, serversJSON, logPaths string, audit installAuditOptions) string {
	if strings.TrimSpace(logPaths) == "" {
		logPaths = "[]" // 必须是合法 JSON 数组（同时是合法 YAML flow 序列），否则生成的 config.yaml 语法错误
	}
	if strings.TrimSpace(audit.ContentAuditPorts) == "" {
		audit.ContentAuditPorts = "[11434,8000,8080]"
	}
	if audit.ContentAuditMaxBody <= 0 {
		audit.ContentAuditMaxBody = 4096
	}
	if audit.CaptureBackend == "" {
		audit.CaptureBackend = "auto"
	}
	if audit.ContentAuditBodyMode == "" {
		audit.ContentAuditBodyMode = "redacted"
	}
	if audit.ContentAuditIncludeHosts == "" {
		audit.ContentAuditIncludeHosts = "[]"
	}
	if audit.ContentAuditExcludePaths == "" {
		audit.ContentAuditExcludePaths = `["/health*","/metrics*","/ready*","/live*"]`
	}
	if audit.ContentAuditMaxEventsPerMin <= 0 {
		audit.ContentAuditMaxEventsPerMin = 2000
	}
	windows := strings.Contains(tmpl, "$ErrorActionPreference")
	cfgB64 := installConfigB64(server, token, category, folderID, serversJSON, logPaths, audit, windows)
	return strings.NewReplacer(
		"__SERVER__", server,
		"__TOKEN__", token,
		"__CATEGORY__", category,
		"__FOLDER_ID__", folderID,
		"__SERVERS_JSON__", serversJSON,
		"__LOG_PATHS__", logPaths,
		"__CONFIG_B64__", cfgB64,
		"__SNI_ENABLED__", strconv.FormatBool(audit.SNIEnabled || audit.ContentAudit),
		"__SNI_INTERFACE__", audit.SNIInterface,
		"__CAPTURE_BACKEND__", audit.CaptureBackend,
		"__CONTENT_AUDIT__", strconv.FormatBool(audit.ContentAudit),
		"__CONTENT_AUDIT_PORTS__", audit.ContentAuditPorts,
		"__CONTENT_AUDIT_MAX_BODY__", strconv.Itoa(audit.ContentAuditMaxBody),
		"__CONTENT_AUDIT_BODY_MODE__", audit.ContentAuditBodyMode,
		"__CONTENT_AUDIT_INCLUDE_HOSTS__", audit.ContentAuditIncludeHosts,
		"__CONTENT_AUDIT_EXCLUDE_PATHS__", audit.ContentAuditExcludePaths,
		"__CONTENT_AUDIT_MAX_EVENTS__", strconv.Itoa(audit.ContentAuditMaxEventsPerMin),
	).Replace(tmpl)
}

// sanitizeLogPaths 把用户填写的日志路径（换行或逗号分隔）清洗为一个【合法 JSON 数组字符串】，
// 用于注入安装脚本生成的 config.yaml 的 log_paths 字段（JSON 数组同时是合法 YAML flow 序列）。
// 关键安全点：路径会被写进未加引号的 shell heredoc，若含 $ ` 等会被展开导致命令注入，
// 因此逐字符白名单（仅保留路径合法字符），再用 json.Marshal 正确转义。
func sanitizeLogPaths(raw string) string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
	var paths []string
	seen := map[string]bool{}
	for _, f := range fields {
		clean := strings.TrimSpace(strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
				return r
			case r == '/', r == '.', r == '_', r == '-', r == ':', r == '*', r == ' ', r == '\\':
				return r
			default:
				return -1 // 丢弃 $ ` " ; | & < > ( ) 等危险字符
			}
		}, strings.TrimSpace(f)))
		if clean == "" || seen[clean] {
			continue
		}
		if len(clean) > 256 {
			clean = clean[:256]
		}
		seen[clean] = true
		paths = append(paths, clean)
		if len(paths) >= 20 { // 上限 20 条，避免超长命令
			break
		}
	}
	if len(paths) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(paths)
	return string(b)
}

const (
	maxServersJSONBytes   = 8192
	maxServersJSONEntries = 8
)

// sanitizeServersJSON parses a JSON array of {server,token} objects, sanitizes
// each URL, and re-serializes as compact JSON. Returns "" if input is empty or
// invalid — the install template then falls back to single-server config.
// Bounded to avoid oversized public install URLs / generated scripts.
func sanitizeServersJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) > maxServersJSONBytes {
		raw = raw[:maxServersJSONBytes]
	}
	var entries []struct {
		Server string `json:"server"`
		Token  string `json:"token"`
	}
	if json.Unmarshal([]byte(raw), &entries) != nil || len(entries) == 0 {
		return ""
	}
	if len(entries) > maxServersJSONEntries {
		entries = entries[:maxServersJSONEntries]
	}
	type clean struct {
		Server string `json:"server"`
		Token  string `json:"token,omitempty"`
	}
	out := make([]clean, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		s := sanitizeServerURL(e.Server)
		if s == "" {
			continue
		}
		key := strings.ToLower(strings.TrimRight(s, "/"))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean{Server: s, Token: sanitizeToken(e.Token)})
	}
	if len(out) == 0 {
		return ""
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// sanitizeAuditInstallOptions turns public install-script query parameters into
// a bounded, injection-safe configuration. Content capture implies the shared
// DNS/SNI collector on both native and TShark backends.
func sanitizeAuditInstallOptions(r map[string]string) installAuditOptions {
	on := func(v string) bool {
		v = strings.ToLower(strings.TrimSpace(v))
		return v == "1" || v == "true" || v == "yes" || v == "on"
	}
	iface := strings.Map(func(ch rune) rune {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			return ch
		case ch == '-', ch == '_', ch == '.', ch == ':':
			return ch
		default:
			return -1
		}
	}, strings.TrimSpace(r["sni_interface"]))
	if len(iface) > 128 {
		iface = iface[:128]
	}

	seen := map[int]bool{}
	ports := make([]int, 0, 16)
	for _, raw := range strings.FieldsFunc(r["content_audit_ports"], func(ch rune) bool {
		return ch == ',' || ch == ';' || ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t'
	}) {
		p, err := strconv.Atoi(raw)
		if err != nil || p < 1 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, p)
		if len(ports) >= 32 {
			break
		}
	}
	if len(ports) == 0 {
		ports = []int{11434, 8000, 8080}
	}
	portJSON, _ := json.Marshal(ports)

	maxBody, _ := strconv.Atoi(strings.TrimSpace(r["content_audit_max_body"]))
	if maxBody == 0 {
		maxBody = 4096
	}
	if maxBody < 1024 {
		maxBody = 1024
	}
	if maxBody > 65536 {
		maxBody = 65536
	}
	backend := strings.ToLower(strings.TrimSpace(r["capture_backend"]))
	if backend != "native" && backend != "tshark" {
		backend = "auto"
	}
	bodyMode := strings.ToLower(strings.TrimSpace(r["content_audit_body_mode"]))
	if bodyMode != "metadata" && bodyMode != "full" {
		bodyMode = "redacted"
	}
	maxEvents, _ := strconv.Atoi(strings.TrimSpace(r["content_audit_max_events_per_min"]))
	if maxEvents <= 0 {
		maxEvents = 2000
	}
	if maxEvents > 100000 {
		maxEvents = 100000
	}
	includeHosts := sanitizeAuditPatternList(r["content_audit_include_hosts"], 64, 253)
	excludePaths := sanitizeAuditPatternList(r["content_audit_exclude_paths"], 64, 512)
	if strings.TrimSpace(r["content_audit_exclude_paths"]) == "" {
		excludePaths = `["/health*","/metrics*","/ready*","/live*"]`
	}
	contentAudit := on(r["content_audit"])
	return installAuditOptions{
		SNIEnabled:                  on(r["sni_enabled"]) || contentAudit,
		SNIInterface:                iface,
		CaptureBackend:              backend,
		ContentAudit:                contentAudit,
		ContentAuditPorts:           string(portJSON),
		ContentAuditMaxBody:         maxBody,
		ContentAuditBodyMode:        bodyMode,
		ContentAuditIncludeHosts:    includeHosts,
		ContentAuditExcludePaths:    excludePaths,
		ContentAuditMaxEventsPerMin: maxEvents,
	}
}

func sanitizeAuditPatternList(raw string, maxCount, maxLen int) string {
	fields := strings.FieldsFunc(raw, func(ch rune) bool {
		return ch == ',' || ch == ';' || ch == '\n' || ch == '\r'
	})
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		clean := strings.ToLower(strings.TrimSpace(strings.Map(func(ch rune) rune {
			switch {
			case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
				return ch
			case ch == '.', ch == '-', ch == '_', ch == '*', ch == '/', ch == ':', ch == '?', ch == '=':
				return ch
			default:
				return -1
			}
		}, field)))
		if clean == "" || seen[clean] {
			continue
		}
		if len(clean) > maxLen {
			clean = clean[:maxLen]
		}
		seen[clean] = true
		out = append(out, clean)
		if len(out) >= maxCount {
			break
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// installShTemplate installs the agent on Linux / macOS.
// Flow: detect prior install → if present stop+uninstall → download → write config →
// enable unit and systemctl/launchd restart (restart works for both fresh and reinstall).
// It works without root:
// as root it registers a systemd service, otherwise it installs under $HOME and
// starts in the background.
const installShTemplate = `#!/bin/sh
set -e
SERVER="__SERVER__"
TOKEN="__TOKEN__"
CATEGORY="__CATEGORY__"
FOLDER_ID="__FOLDER_ID__"
if [ "$(id -u)" = "0" ]; then DIR="${AIOPS_DIR:-/opt/aiops-agent}"; else DIR="${AIOPS_DIR:-$HOME/.aiops-agent}"; fi

OS=$(uname -s)
ARCH=$(uname -m)
case "$OS" in
  Linux)
    case "$ARCH" in
      x86_64|amd64)   BIN="aiops-agent-linux-amd64" ;;
      aarch64|arm64)   BIN="aiops-agent-linux-arm64" ;;
      loongarch64)     BIN="aiops-agent-linux-loong64" ;;
      riscv64)         BIN="aiops-agent-linux-riscv64" ;;
      i386|i686)       BIN="aiops-agent-linux-386" ;;
      armv7l|armv7|armv6l|armhf) BIN="aiops-agent-linux-arm" ;;
      *)
        echo "[AIOps] ERROR: unsupported architecture: $ARCH (supported: x86_64/amd64, aarch64/arm64, loongarch64, riscv64, i386/i686, armv7l)"
        exit 1
        ;;
    esac
    ;;
  Darwin)
    case "$ARCH" in
      arm64)           BIN="aiops-agent-darwin-arm64" ;;
      x86_64)          BIN="aiops-agent-darwin-amd64" ;;
      *)
        echo "[AIOps] ERROR: unsupported architecture: $ARCH (supported: arm64, x86_64)"
        exit 1
        ;;
    esac
    ;;
  *) echo "unsupported OS: $OS"; exit 1 ;;
esac

# Download helper: curl preferred, wget fallback (Alpine/minimal images).
aiops_fetch() {
  # $1=url $2=out
  _url="$1"; _out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fSL --retry 3 --retry-delay 2 -C - "$_url" -o "$_out"
    return $?
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -q --tries=3 -O "$_out" "$_url"
    return $?
  fi
  echo "[AIOps] ERROR: need curl or wget to download $_url"
  return 1
}

# True only when systemd is the real init (not a container that merely has systemctl on PATH).
aiops_has_systemd() {
  command -v systemctl >/dev/null 2>&1 || return 1
  if [ -r /proc/1/comm ] && [ "$(cat /proc/1/comm 2>/dev/null)" = "systemd" ]; then
    return 0
  fi
  _st=$(systemctl is-system-running 2>/dev/null || true)
  case "$_st" in
    running|degraded|maintenance|starting) return 0 ;;
  esac
  return 1
}

# Purge a systemd unit completely: stop/disable, remove unit file AND drop-in
# dirs (systemctl edit leftovers), clear failed state. Incomplete uninstall used
# to leave *.service.d overrides that re-applied ProtectHome / CapabilityBoundingSet
# on the next install and broke the remote terminal (fork/exec bash: permission denied).
aiops_purge_systemd_unit() {
  _u="$1"
  [ -n "$_u" ] || return 0
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop "$_u" 2>/dev/null || true
    systemctl disable "$_u" 2>/dev/null || true
    systemctl reset-failed "$_u" 2>/dev/null || true
  fi
  rm -f "/etc/systemd/system/${_u}.service" \
        "/lib/systemd/system/${_u}.service" \
        "/usr/lib/systemd/system/${_u}.service" \
        "/etc/systemd/system/${_u}.service.wants" \
        "/etc/systemd/system/multi-user.target.wants/${_u}.service"
  rm -rf "/etc/systemd/system/${_u}.service.d" \
         "/lib/systemd/system/${_u}.service.d" \
         "/usr/lib/systemd/system/${_u}.service.d" \
         "/run/systemd/system/${_u}.service.d"
}

# Detect a prior one-liner / --install-service agent so reinstall can stop+uninstall first.
aiops_is_installed() {
  for _d in "$DIR" /opt/aiops-agent "${HOME}/.aiops-agent"; do
    [ -n "$_d" ] || continue
    if [ -x "$_d/aiops-agent" ] || [ -f "$_d/config.yaml" ] || [ -f "$_d/config.json" ]; then
      return 0
    fi
  done
  for _u in aiops-agent aiops-monitor-agent; do
    if [ -f "/etc/systemd/system/${_u}.service" ] || [ -d "/etc/systemd/system/${_u}.service.d" ] || \
       [ -f "/lib/systemd/system/${_u}.service" ] || [ -f "/usr/lib/systemd/system/${_u}.service" ]; then
      return 0
    fi
  done
  for _pl in \
    "$HOME/Library/LaunchAgents/com.aiops.agent.plist" \
    "/Library/LaunchDaemons/com.aiops.agent.plist" \
    "$HOME/Library/LaunchAgents/com.aiops.monitor.agent.plist" \
    "/Library/LaunchDaemons/com.aiops.monitor.agent.plist"
  do
    [ -f "$_pl" ] && return 0
  done
  if command -v pgrep >/dev/null 2>&1; then
    pgrep -f 'aiops-agent' >/dev/null 2>&1 && return 0
  elif command -v ps >/dev/null 2>&1; then
    ps -ax -o args= 2>/dev/null | grep -F 'aiops-agent' | grep -v grep >/dev/null 2>&1 && return 0
  fi
  return 1
}

# Stop service(s) and remove a previous agent install before writing the new one.
aiops_stop_and_uninstall_existing() {
  if ! aiops_is_installed; then
    echo "[AIOps] no existing agent found — fresh install"
    return 0
  fi
  echo "[AIOps] existing agent detected — stopping service, uninstalling, then reinstalling"
  if command -v systemctl >/dev/null 2>&1; then
    for _u in aiops-agent aiops-monitor-agent; do
      aiops_purge_systemd_unit "$_u"
    done
    systemctl daemon-reload 2>/dev/null || true
  fi
  for _pl in \
    "$HOME/Library/LaunchAgents/com.aiops.agent.plist" \
    "/Library/LaunchDaemons/com.aiops.agent.plist" \
    "$HOME/Library/LaunchAgents/com.aiops.monitor.agent.plist" \
    "/Library/LaunchDaemons/com.aiops.monitor.agent.plist"
  do
    [ -f "$_pl" ] || continue
    launchctl unload "$_pl" 2>/dev/null || true
    launchctl bootout system "$_pl" 2>/dev/null || true
    if [ "$(id -u)" != "0" ]; then
      launchctl bootout "gui/$(id -u)" "$_pl" 2>/dev/null || true
    fi
    rm -f "$_pl"
  done
  if command -v crontab >/dev/null 2>&1; then
    crontab -l 2>/dev/null | grep -v 'aiops-agent --config' | crontab - 2>/dev/null || true
  fi
  if [ "$(id -u)" = "0" ] && [ -d /var/spool/cron/crontabs ]; then
    for _cf in /var/spool/cron/crontabs/*; do
      [ -f "$_cf" ] || continue
      if grep -q 'aiops-agent --config' "$_cf" 2>/dev/null; then
        grep -v 'aiops-agent --config' "$_cf" > "$_cf.aiops.tmp" 2>/dev/null && mv "$_cf.aiops.tmp" "$_cf" || rm -f "$_cf.aiops.tmp"
      fi
    done
  fi
  pkill -x aiops-agent 2>/dev/null || true
  pkill -f 'aiops-agent --config' 2>/dev/null || true
  sleep 1 2>/dev/null || true
  for _d in "$DIR" /opt/aiops-agent "${HOME}/.aiops-agent"; do
    [ -n "$_d" ] || continue
    [ -d "$_d" ] || continue
    # Keep custom AIOPS_DIR / default roots empty for a clean reinstall.
    rm -rf "$_d"
  done
  echo "[AIOps] previous agent uninstalled"
}

aiops_stop_and_uninstall_existing

echo "[AIOps] installing to $DIR (server $SERVER)"
echo "[AIOps] platform $OS/$ARCH → $BIN"
mkdir -p "$DIR"
cd "$DIR"
# Download to a staging file, verify the server-published SHA-256, then replace
# atomically. A truncated/corrupted/mismatched download must never overwrite a
# working agent binary.
NEW=".aiops-agent.new"
if ! aiops_fetch "$SERVER/dl/$BIN" "$NEW"; then
  rm -f "$NEW"
  echo "[AIOps] ERROR: failed to download $SERVER/dl/$BIN (HTTP 404 usually means the server image was built without this platform binary)."
  echo "[AIOps] Fix: rebuild server with full agent dist (production Dockerfile, or updated Dockerfile.dev that cross-compiles darwin/linux/windows agents)."
  echo "[AIOps] Probe: curl -fsSI \"$SERVER/dl/$BIN\"   # or: wget -S --spider \"$SERVER/dl/$BIN\""
  exit 1
fi
if ! aiops_fetch "$SERVER/dl/$BIN.sha256" ".aiops-agent.sha256"; then
  rm -f "$NEW" ".aiops-agent.sha256"
  echo "[AIOps] ERROR: failed to download checksum $SERVER/dl/$BIN.sha256"
  exit 1
fi
EXPECTED=$(awk '{print $1}' ".aiops-agent.sha256")
rm -f ".aiops-agent.sha256"
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$NEW" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$NEW" | awk '{print $1}')
else
  echo "[AIOps] ERROR: sha256sum/shasum not found; refusing an unverified install."
  rm -f "$NEW"
  exit 1
fi
if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "[AIOps] ERROR: agent SHA-256 verification failed."
  rm -f "$NEW"
  exit 1
fi
[ -f aiops-agent ] && cp -f aiops-agent aiops-agent.bak 2>/dev/null || true
mv -f "$NEW" aiops-agent
chmod +x aiops-agent
if aiops_fetch "$SERVER/dl/plugins.zip" plugins.zip 2>/dev/null; then
  command -v unzip >/dev/null 2>&1 && unzip -oq plugins.zip
  rm -f plugins.zip
fi
# Annotated config.yaml: active settings + full commented option reference.
# Generated server-side (base64) so every install ships complete docs.
AIOPS_CONFIG_B64='__CONFIG_B64__'
if command -v base64 >/dev/null 2>&1; then
  if ! printf '%s' "$AIOPS_CONFIG_B64" | base64 -d > config.yaml 2>/dev/null; then
    if ! printf '%s' "$AIOPS_CONFIG_B64" | base64 -D > config.yaml 2>/dev/null; then
      echo "[AIOps] ERROR: failed to decode config.yaml (base64)"
      exit 1
    fi
  fi
else
  echo "[AIOps] ERROR: base64 not found; cannot write config.yaml"
  exit 1
fi
# Verify config.yaml was written correctly — on some systems set -e causes the
# script to exit partway (e.g. plugins download failure) BEFORE reaching here,
# leaving config.yaml missing. The agent would then silently use the hardcoded
# default (localhost:8529). Catch this early so the user sees the real error.
if [ ! -s config.yaml ]; then
  echo "[AIOps] ERROR: config.yaml was not created! Installation incomplete."
  echo "[AIOps] This usually means a download step failed. Re-run the install command."
  exit 1
fi
# Restrict config.yaml to owner-only (contains tokens/secrets).
chmod 600 config.yaml 2>/dev/null || true
echo "[AIOps] config.yaml written (active settings + full commented reference)"
if [ "__SNI_ENABLED__" = "true" ] && { [ "__CAPTURE_BACKEND__" = "tshark" ] || [ "$OS" = "Darwin" ]; }; then
  if ! command -v tshark >/dev/null 2>&1 && [ ! -x /Applications/Wireshark.app/Contents/MacOS/tshark ]; then
    echo "[AIOps] WARNING: network content audit needs TShark on $OS."
    echo "[AIOps] Install Wireshark first; on macOS also install its ChmodBPF package."
  else
    echo "[AIOps] TShark dependency detected for cross-platform network audit."
  fi
fi
# Migrate: remove a stale config.json left by a pre-YAML install. The agent now
# prefers config.yaml, but leaving both would be confusing — drop the old one.
rm -f config.json 2>/dev/null || true
echo "[AIOps] config written: $DIR/config.yaml (server: $SERVER)"

if [ "$OS" = "Linux" ] && [ "$(id -u)" = "0" ] && aiops_has_systemd; then
  # Linux + root + real systemd → unit file.
  # Default User=root so the remote terminal can edit /etc, install packages, etc.
  # (Older builds defaulted to SUDO_USER; vim then hit E45 readonly on /etc/*.)
  # Least privilege opt-in: AIOPS_USER=alice curl … | sudo bash
  # Never create a dedicated "aiops" system account.
  if [ -z "${AIOPS_USER:-}" ]; then
    AIOPS_USER="root"
  fi
  if ! id "$AIOPS_USER" >/dev/null 2>&1; then
    echo "[AIOps] ERROR: run-as user '$AIOPS_USER' does not exist."
    echo "[AIOps] Set AIOPS_USER to an existing account, or omit it for root."
    exit 1
  fi
  # Opt out of Agent startup heal that would escalate User=root via sudo/--install-service.
  if [ "$AIOPS_USER" != "root" ]; then
    mkdir -p /etc/aiops-agent
    : > /etc/aiops-agent/allow-nonroot
    echo "[AIOps] wrote /etc/aiops-agent/allow-nonroot (AIOPS_USER=$AIOPS_USER)"
  else
    rm -f /etc/aiops-agent/allow-nonroot 2>/dev/null || true
  fi
  AIOPS_GROUP="$(id -gn "$AIOPS_USER" 2>/dev/null || echo "$AIOPS_USER")"
  chown -R "$AIOPS_USER:$AIOPS_GROUP" "$DIR"
  # SNI / content audit needs packet capture → raise NET_RAW/NET_ADMIN via ambient
  # capabilities. Do NOT set CapabilityBoundingSet to only those two: that drops
  # the rest of root's capabilities and breaks interactive shell / file ops
  # (Go reports fork/exec /bin/bash: permission denied).
  UNIT_CAPS=""
  if [ "__SNI_ENABLED__" = "true" ] || [ "__CONTENT_AUDIT__" = "true" ]; then
    UNIT_CAPS="AmbientCapabilities=CAP_NET_RAW CAP_NET_ADMIN
"
  fi
  # Prefer the account's login shell when interactive; never leave SHELL=nologin
  # (breaks web terminal). Fall back to bash/sh on Alpine/busybox.
  TERM_SHELL=""
  if command -v getent >/dev/null 2>&1; then
    TERM_SHELL=$(getent passwd "$AIOPS_USER" 2>/dev/null | cut -d: -f7)
  fi
  case "$TERM_SHELL" in
    ""|*nologin*|*false*|*true*|*sync*) TERM_SHELL="" ;;
  esac
  [ -n "$TERM_SHELL" ] && [ -x "$TERM_SHELL" ] || TERM_SHELL="/bin/bash"
  [ -x "$TERM_SHELL" ] || TERM_SHELL="/usr/bin/bash"
  [ -x "$TERM_SHELL" ] || TERM_SHELL="/bin/sh"
  TERM_HOME=$(getent passwd "$AIOPS_USER" 2>/dev/null | cut -d: -f6)
  [ -n "$TERM_HOME" ] && [ -d "$TERM_HOME" ] || TERM_HOME=$(eval echo "~$AIOPS_USER" 2>/dev/null || true)
  [ -n "$TERM_HOME" ] && [ -d "$TERM_HOME" ] || TERM_HOME="$DIR"
  # Remote terminal needs a real interactive shell with full FS access (write
  # under $HOME, /etc, package managers, etc.). Explicitly disable sandbox
  # directives; wipe leftover drop-ins so older Protect* overrides cannot stick.
  aiops_purge_systemd_unit aiops-agent
  aiops_purge_systemd_unit aiops-monitor-agent
  cat > /etc/systemd/system/aiops-agent.service <<UNIT
[Unit]
Description=AIOps Agent
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=$AIOPS_USER
Group=$AIOPS_GROUP
WorkingDirectory=$DIR
Environment=SHELL=$TERM_SHELL
Environment=HOME=$TERM_HOME
Environment=USER=$AIOPS_USER
Environment=LOGNAME=$AIOPS_USER
ExecStart=$DIR/aiops-agent --config $DIR/config.yaml
Restart=always
RestartSec=5
KillMode=process
$UNIT_CAPS
ProtectHome=false
ProtectSystem=false
PrivateTmp=false
NoNewPrivileges=false
[Install]
WantedBy=multi-user.target
UNIT
  # A prior NON-root install may have left a background (nohup) instance and an
  # @reboot crontab entry. Remove them so switching to the systemd install
  # doesn't end up running two agents (duplicate reports for the same host).
  if command -v crontab >/dev/null 2>&1; then
    crontab -l 2>/dev/null | grep -v "$DIR/aiops-agent --config" | crontab - 2>/dev/null || true
  fi
  # Best-effort: scrub other users' @reboot lines that point at this binary.
  if [ -d /var/spool/cron/crontabs ]; then
    for _cf in /var/spool/cron/crontabs/*; do
      [ -f "$_cf" ] || continue
      if grep -q "$DIR/aiops-agent --config" "$_cf" 2>/dev/null; then
        grep -v "$DIR/aiops-agent --config" "$_cf" > "$_cf.aiops.tmp" 2>/dev/null && mv "$_cf.aiops.tmp" "$_cf" || rm -f "$_cf.aiops.tmp"
      fi
    done
  fi
  systemctl daemon-reload
  systemctl enable aiops-agent >/dev/null 2>&1 || true
  # Always restart (not start / enable --now): works for both fresh install and
  # reinstall, and guarantees the newly written binary is the one that runs.
  systemctl restart aiops-agent
  echo "[AIOps] systemd service restarted: aiops-agent (user=$AIOPS_USER, boot autostart + auto-restart)"
  # Verify effective sandbox props — drop-ins / vendor fragments can re-lock /etc.
  _eff_user=$(systemctl show aiops-agent -p User --value 2>/dev/null || true)
  _eff_ps=$(systemctl show aiops-agent -p ProtectSystem --value 2>/dev/null || true)
  _eff_ph=$(systemctl show aiops-agent -p ProtectHome --value 2>/dev/null || true)
  echo "[AIOps] effective unit: User=${_eff_user:-?} ProtectSystem=${_eff_ps:-?} ProtectHome=${_eff_ph:-?}"
  if [ "$AIOPS_USER" = "root" ]; then
    _need_unlock=0
    case "${_eff_ps}" in yes|true|strict|full) _need_unlock=1 ;; esac
    case "${_eff_ph}" in yes|true|read-only) _need_unlock=1 ;; esac
    if [ "$_need_unlock" = "1" ]; then
      echo "[AIOps] WARNING: sandbox still active after install (ProtectSystem=${_eff_ps} ProtectHome=${_eff_ph}); purging drop-ins and rewriting unit."
      aiops_purge_systemd_unit aiops-agent
      cat > /etc/systemd/system/aiops-agent.service <<UNIT
[Unit]
Description=AIOps Agent
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=root
Group=root
WorkingDirectory=$DIR
Environment=SHELL=$TERM_SHELL
Environment=HOME=$TERM_HOME
Environment=USER=root
Environment=LOGNAME=root
ExecStart=$DIR/aiops-agent --config $DIR/config.yaml
Restart=always
RestartSec=5
KillMode=process
$UNIT_CAPS
ProtectHome=false
ProtectSystem=false
PrivateTmp=false
NoNewPrivileges=false
[Install]
WantedBy=multi-user.target
UNIT
      systemctl daemon-reload
      systemctl enable aiops-agent >/dev/null 2>&1 || true
      systemctl restart aiops-agent
    fi
  fi
  # 麒麟/UOS 系统自动检测并配置 kysec 白名单
  # POSIX redirects only — Debian/dash rejects bashism &>/dev/null.
  if command -v kysec_adm >/dev/null 2>&1; then
    kysec_adm -a $DIR/aiops-agent 2>/dev/null && echo "[AIOps] kysec whitelist added: $DIR/aiops-agent" || true
  fi
  # SELinux: check and warn if enforcing
  if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null)" = "Enforcing" ]; then
    echo "[AIOps] WARNING: SELinux is enforcing. If agent data collection is blocked, run:"
    echo "  sudo setenforce 0  (temporary) then inspect AVC denials with ausearch."
  fi
elif [ "$OS" = "Darwin" ]; then
  # macOS → launchd. Prefer the installing operator (SUDO_USER / AIOPS_USER);
  # never create a dedicated service account. Root-only (no SUDO_USER) keeps a
  # system LaunchDaemon for headless boot collection.
  # Remote terminal needs a real interactive SHELL (not nologin) and a usable
  # home cwd — mirror the Linux unit: no home/system sandbox in the job plist.
  if [ -z "${AIOPS_USER:-}" ]; then
    if [ "$(id -u)" = "0" ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ] && id "$SUDO_USER" >/dev/null 2>&1; then
      AIOPS_USER="$SUDO_USER"
    elif [ "$(id -u)" = "0" ]; then
      AIOPS_USER="root"
    else
      AIOPS_USER="$(id -un)"
    fi
  fi
  if ! id "$AIOPS_USER" >/dev/null 2>&1; then
    echo "[AIOps] ERROR: run-as user '$AIOPS_USER' does not exist."
    exit 1
  fi
  AIOPS_UID="$(id -u "$AIOPS_USER")"
  if [ "$AIOPS_USER" != "root" ]; then
    AIOPS_HOME=$(eval echo "~$AIOPS_USER" 2>/dev/null || true)
    [ -n "$AIOPS_HOME" ] && [ -d "$AIOPS_HOME" ] || AIOPS_HOME="/Users/$AIOPS_USER"
    chown -R "$AIOPS_USER" "$DIR" 2>/dev/null || true
    PLIST_DIR="$AIOPS_HOME/Library/LaunchAgents"
    mkdir -p "$PLIST_DIR"
    chown "$AIOPS_USER" "$PLIST_DIR" 2>/dev/null || true
  else
    AIOPS_HOME="/var/root"
    [ -d "$AIOPS_HOME" ] || AIOPS_HOME="/Users/root"
    PLIST_DIR="/Library/LaunchDaemons"
    mkdir -p "$PLIST_DIR"
  fi
  TERM_SHELL=""
  if command -v dscl >/dev/null 2>&1; then
    TERM_SHELL=$(dscl . -read "/Users/$AIOPS_USER" UserShell 2>/dev/null | awk '{print $2}')
  fi
  [ -z "$TERM_SHELL" ] && TERM_SHELL=$(/usr/bin/dscl . -read "/Users/$AIOPS_USER" UserShell 2>/dev/null | awk '{print $2}')
  case "$TERM_SHELL" in
    ""|*nologin*|*false*|*true*|*sync*) TERM_SHELL="" ;;
  esac
  [ -n "$TERM_SHELL" ] && [ -x "$TERM_SHELL" ] || TERM_SHELL="/bin/zsh"
  [ -x "$TERM_SHELL" ] || TERM_SHELL="/bin/bash"
  [ -x "$TERM_SHELL" ] || TERM_SHELL="/bin/sh"
  PLIST="$PLIST_DIR/com.aiops.agent.plist"
  cat > "$PLIST" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.aiops.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>$DIR/aiops-agent</string>
    <string>--config</string>
    <string>$DIR/config.yaml</string>
  </array>
  <key>WorkingDirectory</key><string>$DIR</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SHELL</key><string>$TERM_SHELL</string>
    <key>HOME</key><string>$AIOPS_HOME</string>
    <key>USER</key><string>$AIOPS_USER</string>
    <key>LOGNAME</key><string>$AIOPS_USER</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <!-- Agent writes rotating agent.log under $DIR; do not also redirect here (FD would block rotation). -->
  <key>StandardOutPath</key><string>/dev/null</string>
  <key>StandardErrorPath</key><string>/dev/null</string>
</dict>
</plist>
PL
  [ "$AIOPS_USER" != "root" ] && chown "$AIOPS_USER" "$PLIST" 2>/dev/null || true
  # Strip the quarantine xattr: a curl-downloaded binary can carry
  # com.apple.quarantine, and after a reboot Gatekeeper blocks launchd from starting
  # a quarantined/unsigned binary — a prime cause of "monitoring dead after restart".
  xattr -dr com.apple.quarantine "$DIR/aiops-agent" 2>/dev/null || true
  launchctl unload "$PLIST" 2>/dev/null || true
  if [ "$AIOPS_USER" = "root" ]; then
    launchctl bootout system "$PLIST" 2>/dev/null || true
    launchctl bootstrap system "$PLIST" 2>/dev/null || launchctl load -w "$PLIST" 2>/dev/null || launchctl load "$PLIST" 2>/dev/null || true
    launchctl kickstart -k system/com.aiops.agent 2>/dev/null || true
    echo "[AIOps] launchd LaunchDaemon restarted: com.aiops.agent (user=root, starts at boot + keepalive)"
  else
    # Per-user LaunchAgent under the installing operator (gui/\$AIOPS_UID).
    launchctl bootout "gui/$AIOPS_UID" "$PLIST" 2>/dev/null || true
    launchctl bootstrap "gui/$AIOPS_UID" "$PLIST" 2>/dev/null || launchctl asuser "$AIOPS_UID" launchctl load -w "$PLIST" 2>/dev/null || true
    launchctl enable "gui/$AIOPS_UID/com.aiops.agent" 2>/dev/null || true
    launchctl kickstart -k "gui/$AIOPS_UID/com.aiops.agent" 2>/dev/null || launchctl asuser "$AIOPS_UID" launchctl kickstart "gui/$AIOPS_UID/com.aiops.agent" 2>/dev/null || true
    echo "[AIOps] launchd LaunchAgent restarted: com.aiops.agent (user=$AIOPS_USER, starts at login + keepalive)"
    echo "[AIOps] NOTE: per-user agent starts after LOGIN. For headless boot collection as root,"
    echo "[AIOps] re-run as root without sudo (or AIOPS_USER=root)."
  fi
else
  # Fallback (non-root Linux without systemd): restart now + a @reboot crontab entry
  # so it survives reboots. root+systemd is recommended for restart-on-crash too.
  # Redirect to /dev/null — Agent itself writes rolling agent.log under $DIR (7×10MiB).
  pkill -f "$DIR/aiops-agent" 2>/dev/null || true
  sleep 1 2>/dev/null || true
  nohup "$DIR/aiops-agent" --config "$DIR/config.yaml" >/dev/null 2>&1 &
  if command -v crontab >/dev/null 2>&1; then
    ( crontab -l 2>/dev/null | grep -v "$DIR/aiops-agent --config" ; \
      echo "@reboot $DIR/aiops-agent --config $DIR/config.yaml >/dev/null 2>&1" ) | crontab - 2>/dev/null || true
    echo "[AIOps] agent restarted in background + @reboot autostart added (log: $DIR/agent.log, 7x10MiB rotate)"
  else
    echo "[AIOps] agent restarted in background (log: $DIR/agent.log, 7x10MiB rotate)"
  fi
fi
echo "[AIOps] done. Check the dashboard for this host."
`

// installPs1Template installs the agent on Windows, privilege-adaptive.
// Flow: detect prior install → if present stop+uninstall → download → write config →
// register service/autostart and Restart-Service (restart for both fresh and reinstall).
//
// Privilege-adaptive:
//   - Prefer ELEVATED when Hyper-V / Smart App Control / AppLocker is present:
//     installs under %ProgramFiles%\AIOps Agent as a LocalSystem service (boot
//     autostart, desktop worker, Get-VM). AppData is the common AppLocker deny
//     path; after an App Control block the script auto-retries elevated once.
//   - Run NON-elevated (no policy pressure): classic per-user install under
//     %LOCALAPPDATA% (HKCU Run + 5-min keepalive). No admin required, but it
//     cannot collect Hyper-V guests and is often blocked by Application Control.
//
// config.yaml is UTF-8 (no BOM); the agent is launched via a hidden VBS
// supervisor that only starts it when not already running (no duplicates).
const installPs1Template = `$ErrorActionPreference = "Stop"
# Windows PowerShell 5.1 inherits the legacy console code page (commonly GBK
# 936). The Go Agent writes UTF-8; when its stderr is captured by PowerShell the
# bytes were decoded as GBK and every Chinese installation log became mojibake.
# Align the console, native-command decoder, and pipeline encoder before invoking
# curl.exe or aiops-agent.exe. All assignments are best-effort for headless hosts.
try {
  $Utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [Console]::InputEncoding = $Utf8NoBom
  [Console]::OutputEncoding = $Utf8NoBom
  $global:OutputEncoding = $Utf8NoBom
  if (Get-Command chcp.com -ErrorAction SilentlyContinue) {
    & chcp.com 65001 | Out-Null
  }
} catch {}
# Force TLS 1.2 before any download. Windows Server 2012/2016 default Invoke-WebRequest
# to TLS 1.0, which fails against a TLS1.2-only HTTPS server ("Could not create SSL/TLS
# secure channel") — a very common Windows install failure. Numeric 3072 = Tls12 avoids
# an enum-undefined error on older .NET where the Tls12 name isn't defined.
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072 } catch {}
$Server   = "__SERVER__"
$Token    = "__TOKEN__"
$Category = "__CATEGORY__"
$FolderID = "__FOLDER_ID__"
$LogPaths = '__LOG_PATHS__'
$ServersJson = '__SERVERS_JSON__'
$CaptureBackend = "__CAPTURE_BACKEND__"
$ContentAudit = "__CONTENT_AUDIT__"
# Elevated installs run the agent as SYSTEM (needed for Hyper-V Get-VM) and live
# machine-wide under ProgramData; non-elevated installs stay per-user as before.
$IsAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)

function Test-AiopsSmartAppControlOn {
  try {
    $ci = Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\CI\Policy' -ErrorAction SilentlyContinue
    if ($ci -and ($ci.VerifiedAndReputablePolicyEnforced -eq 1 -or $ci.SkciEnabled -eq 1)) { return $true }
  } catch {}
  return $false
}
function Test-AiopsAppLockerPresent {
  try {
    $alp = Get-AppLockerPolicy -Effective -ErrorAction SilentlyContinue
    if ($alp -and $alp.RuleCollections -and $alp.RuleCollections.Count -gt 0) { return $true }
  } catch {}
  return $false
}
# Relaunch the same one-liner elevated. Used for Hyper-V, AppLocker-friendly
# Program Files install, and App Control recovery (AppData is often denied).
function Request-AiopsElevatedInstall([string]$Reason) {
  $q = "token=" + [Uri]::EscapeDataString([string]$Token)
  if ($Category) { $q += "&category=" + [Uri]::EscapeDataString([string]$Category) }
  if ($FolderID) { $q += "&folder_id=" + [Uri]::EscapeDataString([string]$FolderID) }
  if ($LogPaths -and $LogPaths -ne "[]") { $q += "&log_paths=" + [Uri]::EscapeDataString([string]$LogPaths) }
  if ($ServersJson) { $q += "&servers_json=" + [Uri]::EscapeDataString([string]$ServersJson) }
  if ($CaptureBackend) { $q += "&capture_backend=" + [Uri]::EscapeDataString([string]$CaptureBackend) }
  if ($ContentAudit -eq 'true') { $q += "&content_audit=1" }
  $reinvoke = '[Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor 3072; irm "' + $Server + '/install.ps1?' + $q + '" | iex'
  $enc = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($reinvoke))
  # -NoExit keeps the elevated window open. The real install (and every error it
  # can report) happens over there, and without this the window vanished the
  # instant it finished — leaving the operator with a machine that never showed
  # up in the dashboard and no output to explain why.
  Start-Process powershell -Verb RunAs -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-NoExit','-EncodedCommand',$enc -ErrorAction Stop
  Write-Host ("[AIOps] Elevated installer launched (reason=" + $Reason + "). Approve the UAC prompt; the install continues in the NEW admin window (close it when done).")
}

$SacOnEarly = Test-AiopsSmartAppControlOn
$AppLockerEarly = Test-AiopsAppLockerPresent
$HyperVHost = [bool](Get-Service -Name vmms -ErrorAction SilentlyContinue)
# Win10/11 workstations: AppData + non-elevated installs fail often (Smart App
# Control, AppLocker, UAC, disabled WScript). Prefer Program Files + service.
$IsWorkstation = $false
try {
  $pt = (Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop).ProductType
  if ($pt -eq 1) { $IsWorkstation = $true }
} catch {
  try {
    $pt = (Get-WmiObject -Class Win32_OperatingSystem -ErrorAction Stop).ProductType
    if ($pt -eq 1) { $IsWorkstation = $true }
  } catch {}
}
# Prefer Program Files + Windows service whenever Soft policies / Hyper-V / Win10-11
# make per-user AppData installs fragile. AppData is the #1 AppLocker deny target.
$PreferElevated = $HyperVHost -or $SacOnEarly -or $AppLockerEarly -or $IsWorkstation
if (-not $IsAdmin -and $PreferElevated) {
  if ($HyperVHost) {
    Write-Host "[AIOps] Hyper-V host detected but PowerShell is not elevated."
    Write-Host "[AIOps] Requesting administrator rights (UAC) so Hyper-V + service install work..."
  } elseif ($SacOnEarly) {
    Write-Host '[AIOps] Smart App Control is ON - preferring elevated Program Files install.'
    Write-Host '[AIOps] 智能应用控制已开启：优先请求管理员安装到 Program Files（若仍拦截需临时关闭 SAC）。'
  } elseif ($IsWorkstation) {
    Write-Host '[AIOps] Windows 10/11 detected - preferring elevated Program Files + Windows service install.'
    Write-Host '[AIOps] 检测到 Windows 10/11：优先请求管理员安装到 Program Files 并注册系统服务（避免用户态 Run/WScript 被策略拦截）。'
  } else {
    Write-Host '[AIOps] AppLocker/policy detected - preferring elevated Program Files install.'
    Write-Host '[AIOps] 检测到应用程序控制策略：优先请求管理员安装到 Program Files（受信任路径）。'
  }
  try {
    Request-AiopsElevatedInstall 'prefer-elevated'
    return
  } catch {
    Write-Host "[AIOps] Elevation declined or unavailable; continuing with a per-user install."
    if ($HyperVHost) {
      Write-Host "[AIOps] NOTE: Hyper-V VM collection stays OFF until you re-run elevated."
    }
    if ($SacOnEarly -or $AppLockerEarly) {
      Write-Host "[AIOps] WARNING: AppData installs are often blocked by Application Control. If the next step fails, re-run in an elevated PowerShell (管理员)." -ForegroundColor Yellow
    }
  }
}
if ($IsAdmin) {
  # ProgramData is commonly denied by AppLocker/SRP default-deny policies.
  # Program Files is the standard trusted executable location on managed Windows.
  $Dir = Join-Path $env:ProgramFiles "AIOps Agent"
} else {
  $Dir = Join-Path $env:LOCALAPPDATA "aiops-agent"
}

Write-Host "[AIOps] installing to $Dir (server $Server, admin=$IsAdmin)"

# Modern Agent builds (Go ≥1.21) require Windows 10 / Server 2016+.
# Server 2012 / 2012 R2 (and Win8 / 8.1) use a dedicated Go 1.20 binary:
#   aiops-agent-windows-amd64-win2012.exe
# Windows 7 / Server 2008 R2 and older remain unsupported.
function Get-AiopsWindowsOSVersion {
  try {
    $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
  } catch {
    try { $os = Get-WmiObject -Class Win32_OperatingSystem -ErrorAction Stop } catch { return $null }
  }
  return $os
}
function Test-AiopsWindowsSupported {
  $os = Get-AiopsWindowsOSVersion
  if (-not $os) { return $true }
  $ver = [version]$os.Version
  # major≥10 → Win10/11 + Server 2016+
  # 6.2 / 6.3 → Server 2012 / 2012 R2 (+ Win8 / 8.1) via legacy Agent build
  if ($ver.Major -ge 10) { return $true }
  if ($ver.Major -eq 6 -and $ver.Minor -ge 2) { return $true }
  Write-Host ""
  Write-Host ("[AIOps] FATAL: " + $os.Caption + " (" + $os.Version + ") is not supported by this Agent build.") -ForegroundColor Red
  Write-Host "[AIOps] Supported: Windows 8/8.1, Windows 10/11, Windows Server 2012 / 2012 R2 / 2016 / 2019 / 2022 / 2025." -ForegroundColor Yellow
  Write-Host "[AIOps] 当前 Agent 不支持 Windows 7 与 Windows Server 2008/2008 R2；请升级 OS。" -ForegroundColor Yellow
  return $false
}
function Test-AiopsNeedsWin2012Agent {
  $os = Get-AiopsWindowsOSVersion
  if (-not $os) { return $false }
  $ver = [version]$os.Version
  return ($ver.Major -eq 6 -and $ver.Minor -ge 2 -and $ver.Minor -le 3)
}
if (-not (Test-AiopsWindowsSupported)) { throw "Unsupported Windows version for AIOps Agent" }

# Never call cmd.exe — locked-down hosts often block it via GPO ("This program is
# blocked by group policy") while still allowing PowerShell + schtasks.exe/sc.exe.
# With $ErrorActionPreference=Stop that used to abort the whole install on line 1.
function Remove-AiopsScheduledTask([string]$Name) {
  $prev = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  try {
    if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
      $t = Get-ScheduledTask -TaskName $Name -ErrorAction SilentlyContinue
      if ($t) { Unregister-ScheduledTask -TaskName $Name -Confirm:$false -ErrorAction SilentlyContinue }
    }
  } catch {}
  try { & "$env:SystemRoot\System32\schtasks.exe" /Delete /TN $Name /F 1>$null 2>$null | Out-Null } catch {}
  $ErrorActionPreference = $prev
}
function Stop-AiopsServiceQuiet {
  $prev = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  try {
    $svc = Get-Service -Name 'AiopsMonitorAgent' -ErrorAction SilentlyContinue
    if ($svc -and $svc.Status -ne 'Stopped') {
      Stop-Service -Name 'AiopsMonitorAgent' -Force -ErrorAction SilentlyContinue
    }
  } catch {}
  try { & "$env:SystemRoot\System32\sc.exe" stop AiopsMonitorAgent 1>$null 2>$null | Out-Null } catch {}
  $ErrorActionPreference = $prev
}
function Remove-AiopsServiceQuiet {
  Stop-AiopsServiceQuiet
  Start-Sleep -Milliseconds 800
  $prev = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  try { & "$env:SystemRoot\System32\sc.exe" delete AiopsMonitorAgent 1>$null 2>$null | Out-Null } catch {}
  $ErrorActionPreference = $prev
}
function Test-AiopsAlreadyInstalled {
  $prev = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  try {
    if (Get-Service -Name 'AiopsMonitorAgent' -ErrorAction SilentlyContinue) { $ErrorActionPreference = $prev; return $true }
    if (Get-Process -Name 'aiops-agent' -ErrorAction SilentlyContinue) { $ErrorActionPreference = $prev; return $true }
    foreach ($name in @('AIOpsAgent','AIOps-Agent')) {
      if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
        if (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue) { $ErrorActionPreference = $prev; return $true }
      }
    }
    $run = Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'AIOpsAgent' -ErrorAction SilentlyContinue
    if ($run) { $ErrorActionPreference = $prev; return $true }
    foreach ($cand in @(
      $Dir,
      (Join-Path $env:LOCALAPPDATA 'aiops-agent'),
      (Join-Path $env:ProgramFiles 'AIOps Agent'),
      (Join-Path $env:ProgramData 'aiops-agent')
    )) {
      if (-not $cand) { continue }
      if (Test-Path (Join-Path $cand 'aiops-agent.exe')) { $ErrorActionPreference = $prev; return $true }
      if (Test-Path (Join-Path $cand 'config.yaml')) { $ErrorActionPreference = $prev; return $true }
    }
  } catch {}
  $ErrorActionPreference = $prev
  return $false
}
# Sweep EVERY user profile for a pre-Program-Files install.
#
# Before the agent moved into Program Files it installed under the *installing
# user's* profile: %LOCALAPPDATA%\aiops-agent plus that user's HKCU Run entry.
# Both the installer and the uninstaller only ever looked at the CURRENT user's
# HKCU and LOCALAPPDATA — and elevating through UAC switches both to whichever
# admin approved the prompt. So on Windows 10/11 the old per-user agent survived
# every "uninstall": it kept auto-starting at logon, kept reporting under the
# same machine fingerprint (therefore the same host card), and the dashboard kept
# showing the OLD agent version while the freshly installed service looked like
# it had done nothing at all.
function Remove-AiopsAllUserInstalls {
  $prev = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  $found = $false
  $profiles = @()
  try {
    $profiles = Get-ChildItem 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList' -ErrorAction SilentlyContinue |
      ForEach-Object {
        $sid = Split-Path -Leaf $_.Name
        # Machine/service accounts never carry a per-user install, and probing
        # their profile paths only raises access-denied noise mid-install.
        if ($sid -eq 'S-1-5-18' -or $sid -eq 'S-1-5-19' -or $sid -eq 'S-1-5-20' -or $sid -like 'S-1-5-80-*') { return }
        $p = $null
        try { $p = (Get-ItemProperty -Path $_.PSPath -Name ProfileImagePath -ErrorAction SilentlyContinue).ProfileImagePath } catch {}
        if ($p -and (Test-Path -LiteralPath $p -ErrorAction SilentlyContinue)) { New-Object PSObject -Property @{ Sid = $sid; Path = $p } }
      }
  } catch {}
  foreach ($u in $profiles) {
    # Autostart entry: use the hive if the user is logged on, otherwise mount
    # their NTUSER.DAT just long enough to delete the value.
    $hive = "Registry::HKEY_USERS\" + $u.Sid
    $mounted = $false
    if (-not (Test-Path $hive)) {
      $hive = $null
      $dat = Join-Path $u.Path 'NTUSER.DAT'
      if (Test-Path -LiteralPath $dat) {
        & "$env:SystemRoot\System32\reg.exe" load 'HKU\AIOpsTmp' $dat 1>$null 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) { $hive = 'Registry::HKEY_USERS\AIOpsTmp'; $mounted = $true }
      }
    }
    if ($hive) {
      $runKey = $hive + '\Software\Microsoft\Windows\CurrentVersion\Run'
      foreach ($n in @('AIOpsAgent','AIOpsRelay')) {
        $existing = $null
        try { $existing = Get-ItemProperty -Path $runKey -Name $n -ErrorAction SilentlyContinue } catch {}
        if ($existing) {
          $found = $true
          Write-Host ("[AIOps] removing stale autostart: " + $u.Path + " -> " + $n)
          Remove-ItemProperty -Path $runKey -Name $n -ErrorAction SilentlyContinue
        }
      }
    }
    if ($mounted) {
      # PowerShell keeps registry handles open; without this reg unload fails and
      # the profile stays mounted until reboot.
      [gc]::Collect(); [gc]::WaitForPendingFinalizers()
      & "$env:SystemRoot\System32\reg.exe" unload 'HKU\AIOpsTmp' 1>$null 2>$null | Out-Null
    }
    foreach ($sub in @('AppData\Local\aiops-agent','AppData\Roaming\aiops-agent')) {
      $d = Join-Path $u.Path $sub
      if (Test-Path -LiteralPath $d) {
        $found = $true
        Write-Host ("[AIOps] removing legacy per-user install: " + $d)
        Remove-Item -Recurse -Force -LiteralPath $d -ErrorAction SilentlyContinue
        if (Test-Path -LiteralPath $d) {
          Write-Host ("[AIOps] WARNING: could not delete " + $d + " (locked or access denied); re-run elevated.") -ForegroundColor Yellow
        }
      }
    }
  }
  if ($found -and -not $IsAdmin) {
    Write-Host '[AIOps] WARNING: found other users'' agents but this window is not elevated; some may survive. Re-run as Administrator.' -ForegroundColor Yellow
  }
  $ErrorActionPreference = $prev
}

# Carry the host identity across a reinstall.
#
# Everything in the platform is keyed by host_id, so losing agent_state.json
# splits a host's metrics, logs, alerts and hardware history in two. The
# uninstall step below deletes whole directories, so stash the identity first.
# System32 is in the candidate list because a service installed before paths were
# anchored wrote its state there (the SCM's working directory).
function Save-AiopsIdentity {
  $stash = Join-Path $env:TEMP 'aiops-agent-state.json'
  Remove-Item -LiteralPath $stash -Force -ErrorAction SilentlyContinue
  $candidates = New-Object System.Collections.Generic.List[string]
  foreach ($cand in @(
    (Join-Path $Dir 'agent_state.json'),
    (Join-Path $env:ProgramFiles 'AIOps Agent\agent_state.json'),
    (Join-Path $env:ProgramData 'aiops-agent\agent_state.json'),
    (Join-Path $env:LOCALAPPDATA 'aiops-agent\agent_state.json'),
    (Join-Path $env:SystemRoot 'System32\agent_state.json')
  )) {
    if ($cand) { [void]$candidates.Add($cand) }
  }
  # Walk every user profile the same way Remove-AiopsAllUserInstalls does.
  # Elevating through UAC switches LOCALAPPDATA to the approving admin — without
  # this scan, the only good agent_state.json (under the original user's AppData)
  # is deleted first and the host card splits / remote maintenance opens a ghost.
  try {
    Get-ChildItem 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList' -ErrorAction SilentlyContinue |
      ForEach-Object {
        $sid = Split-Path -Leaf $_.Name
        if ($sid -eq 'S-1-5-18' -or $sid -eq 'S-1-5-19' -or $sid -eq 'S-1-5-20' -or $sid -like 'S-1-5-80-*') { return }
        $p = $null
        try { $p = (Get-ItemProperty -Path $_.PSPath -Name ProfileImagePath -ErrorAction SilentlyContinue).ProfileImagePath } catch {}
        if (-not $p -or -not (Test-Path -LiteralPath $p -ErrorAction SilentlyContinue)) { return }
        foreach ($sub in @('AppData\Local\aiops-agent\agent_state.json','AppData\Roaming\aiops-agent\agent_state.json')) {
          [void]$candidates.Add((Join-Path $p $sub))
        }
      }
  } catch {}
  foreach ($cand in $candidates) {
    if (-not $cand) { continue }
    if (Test-Path -LiteralPath $cand) {
      try {
        Copy-Item -LiteralPath $cand -Destination $stash -Force -ErrorAction Stop
        Write-Host ("[AIOps] stashed host identity from " + $cand)
        return $stash
      } catch {}
    }
  }
  return $null
}
function Restore-AiopsIdentity([string]$Stash) {
  if (-not $Stash -or -not (Test-Path -LiteralPath $Stash)) { return }
  $target = Join-Path $Dir 'agent_state.json'
  if (-not (Test-Path -LiteralPath $target)) {
    Copy-Item -LiteralPath $Stash -Destination $target -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $target) {
      Write-Host "[AIOps] preserved existing host identity (dashboard history and host card are kept)"
    }
  }
  Remove-Item -LiteralPath $Stash -Force -ErrorAction SilentlyContinue
  # Drop the copy the old SCM working directory left behind so a downgraded
  # binary can never resurrect a second identity for this machine.
  Remove-Item -LiteralPath (Join-Path $env:SystemRoot 'System32\agent_state.json') -Force -ErrorAction SilentlyContinue
}
function Uninstall-AiopsExisting {
  Write-Host "[AIOps] existing agent detected — stopping service, uninstalling, then reinstalling"
  Remove-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "AIOpsAgent" -ErrorAction SilentlyContinue
  Remove-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "AIOpsRelay" -ErrorAction SilentlyContinue
  Remove-AiopsScheduledTask 'AIOpsAgent'
  Remove-AiopsScheduledTask 'AIOps-Agent'
  Remove-AiopsServiceQuiet
  Start-Sleep -Milliseconds 1200
  Get-Process aiops-agent -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
  # Only stop AIOps supervisor scripts — never kill unrelated wscript.exe hosts.
  try {
    Get-CimInstance Win32_Process -Filter "Name = 'wscript.exe'" -ErrorAction SilentlyContinue |
      Where-Object { $_.CommandLine -and ($_.CommandLine -match 'aiops|start-agent\.vbs') } |
      ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
  } catch {
    try {
      Get-WmiObject Win32_Process -Filter "Name = 'wscript.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and ($_.CommandLine -match 'aiops|start-agent\.vbs') } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    } catch {}
  }
  Start-Sleep -Seconds 2
  foreach ($cand in @(
    $Dir,
    (Join-Path $env:LOCALAPPDATA 'aiops-agent'),
    (Join-Path $env:ProgramFiles 'AIOps Agent'),
    (Join-Path $env:ProgramData 'aiops-agent')
  )) {
    if (-not $cand) { continue }
    if (Test-Path $cand) {
      Remove-Item -Recurse -Force $cand -ErrorAction SilentlyContinue
    }
  }
  Write-Host "[AIOps] previous agent uninstalled"
}

# Prefer Invoke-WebRequest for downloads (curl.exe is often GPO-blocked).
# If already installed: stop + uninstall first; otherwise fresh install.
$SavedIdentity = Save-AiopsIdentity
if (Test-AiopsAlreadyInstalled) {
  Uninstall-AiopsExisting
} else {
  Write-Host "[AIOps] no existing agent found — fresh install"
  Remove-AiopsScheduledTask 'AIOpsAgent'
  Stop-AiopsServiceQuiet
  Get-Process aiops-agent -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
}
# Unconditional: Test-AiopsAlreadyInstalled only inspects the current user, so an
# install owned by a different profile reports "no existing agent" and would then
# keep running alongside the new one.
Remove-AiopsAllUserInstalls
New-Item -ItemType Directory -Force $Dir | Out-Null
Restore-AiopsIdentity $SavedIdentity
Start-Sleep -Milliseconds 400

$AgentExe = Join-Path $Dir "aiops-agent.exe"
$AgentNew = Join-Path $Dir ".aiops-agent.new.exe"
$AgentBak = Join-Path $Dir "aiops-agent.exe.bak"
# Download helper: NEVER call curl.exe/cmd.exe — hardened GPO hosts block them
# ("This program is blocked by group policy") and with $ErrorActionPreference=Stop
# that aborts the whole install. Invoke-WebRequest / WebClient are enough.
function Clear-AiopsMotw([string]$Path) {
  if (-not $Path -or -not (Test-Path -LiteralPath $Path)) { return }
  try { Unblock-File -LiteralPath $Path -ErrorAction SilentlyContinue } catch {}
  try { Remove-Item -LiteralPath ($Path + ':Zone.Identifier') -Force -ErrorAction SilentlyContinue } catch {}
}
function Get-AiopsRemoteFile([string]$Url, [string]$OutFile) {
  $prev = $ErrorActionPreference; $ErrorActionPreference = 'Stop'
  Remove-Item $OutFile -Force -ErrorAction SilentlyContinue
  # Prefer byte-array write: avoids Mark-of-the-Web that OutFile/DownloadFile attach.
  try {
    $wc = New-Object System.Net.WebClient
    $bytes = $wc.DownloadData($Url)
    if ($bytes -and $bytes.Length -gt 0) {
      [System.IO.File]::WriteAllBytes($OutFile, $bytes)
      Clear-AiopsMotw $OutFile
      if ((Test-Path $OutFile) -and ((Get-Item $OutFile).Length -gt 0)) {
        $ErrorActionPreference = $prev
        return
      }
    }
  } catch {}
  try {
    Invoke-WebRequest $Url -OutFile $OutFile -UseBasicParsing
    Clear-AiopsMotw $OutFile
    if ((Test-Path $OutFile) -and ((Get-Item $OutFile).Length -gt 0)) {
      $ErrorActionPreference = $prev
      return
    }
  } catch {}
  try {
    (New-Object System.Net.WebClient).DownloadFile($Url, $OutFile)
    Clear-AiopsMotw $OutFile
    if ((Test-Path $OutFile) -and ((Get-Item $OutFile).Length -gt 0)) {
      $ErrorActionPreference = $prev
      return
    }
  } catch {}
  $ErrorActionPreference = $prev
  throw ("download failed: " + $Url)
}
# Native arch binary: amd64 keeps legacy /dl/aiops-agent.exe; ARM64 uses a
# dedicated name so WOW64/x64 emulation never downloads the wrong PE.
# Server 2012 / 2012 R2 must use the Go 1.20 win2012 build — modern Go ≥1.21
# binaries exit immediately on those kernels.
$ProcArch = [string]$env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $ProcArch = [string]$env:PROCESSOR_ARCHITEW6432 }
$AgentRemote = 'aiops-agent.exe'
$NeedsWin2012 = Test-AiopsNeedsWin2012Agent
switch ($ProcArch.ToUpperInvariant()) {
  'ARM64' {
    if ($NeedsWin2012) {
      Write-Host '[AIOps] FATAL: Windows Server 2012/R2 on ARM64 is not supported.' -ForegroundColor Red
      exit 1
    }
    $AgentRemote = 'aiops-agent-windows-arm64.exe'
  }
  'AMD64' {
    if ($NeedsWin2012) { $AgentRemote = 'aiops-agent-windows-amd64-win2012.exe' }
    else { $AgentRemote = 'aiops-agent.exe' }
  }
  'X86' {
    Write-Host '[AIOps] FATAL: 32-bit Windows (x86) is not supported.' -ForegroundColor Red
    exit 1
  }
  default {
    Write-Host ("[AIOps] WARN: unknown PROCESSOR_ARCHITECTURE=" + $ProcArch + "; trying aiops-agent.exe") -ForegroundColor Yellow
  }
}
if ($NeedsWin2012) {
  Write-Host "[AIOps] Detected Windows Server 2012/2012 R2 (or Win8/8.1) — using Go 1.20 legacy Agent build." -ForegroundColor Cyan
}
Write-Host ("[AIOps] platform windows/" + $ProcArch + " → " + $AgentRemote)
try {
  Get-AiopsRemoteFile "$Server/dl/$AgentRemote" $AgentNew
} catch {
  if ($NeedsWin2012) {
    Write-Host ""
    Write-Host ("[AIOps] FATAL: 服务端缺少兼容 Server 2012 的 Agent：" + $AgentRemote) -ForegroundColor Red
    Write-Host "[AIOps] 请升级服务端到含 win2012 产物的版本，或在构建时执行 scripts/build-agent-win2012.sh 并将二进制放入 dist/。" -ForegroundColor Yellow
    throw
  }
  if ($AgentRemote -ne 'aiops-agent.exe') {
    Write-Host ("[AIOps] WARN: " + $AgentRemote + " missing on server; falling back to aiops-agent.exe (will fail on ARM64)") -ForegroundColor Yellow
    $AgentRemote = 'aiops-agent.exe'
    Get-AiopsRemoteFile "$Server/dl/$AgentRemote" $AgentNew
  } else { throw }
}
$Expected = ((Invoke-WebRequest "$Server/dl/$AgentRemote.sha256" -UseBasicParsing).Content -split '\s+')[0].Trim().ToLowerInvariant()
$Sha = [Security.Cryptography.SHA256]::Create()
$Stream = [IO.File]::OpenRead($AgentNew)
try { $Actual = ([BitConverter]::ToString($Sha.ComputeHash($Stream))).Replace("-","").ToLowerInvariant() }
finally { $Stream.Dispose(); $Sha.Dispose() }
if (-not $Expected -or $Expected -ne $Actual) {
  Remove-Item $AgentNew -Force -ErrorAction SilentlyContinue
  throw "Agent SHA-256 verification failed; existing binary was not replaced."
}
if (Test-Path $AgentBak) { Remove-Item $AgentBak -Force -ErrorAction SilentlyContinue }
if (Test-Path $AgentExe) { Move-Item $AgentExe $AgentBak -Force }
try { Move-Item $AgentNew $AgentExe -Force }
catch {
  if (Test-Path $AgentBak) { Move-Item $AgentBak $AgentExe -Force }
  throw
}
# Clear Mark-of-the-Web / Zone.Identifier from HTTP downloads. This helps
# SmartScreen; WDAC/AppLocker/SAC still need path trust, allow rules, or Off.
Clear-AiopsMotw $AgentExe
Clear-AiopsMotw $AgentNew
Clear-AiopsMotw $AgentBak

# Preflight: confirm Windows will let us execute the binary BEFORE we claim
# service install succeeded. "Application Control policy has blocked this file"
# (WDAC / AppLocker / Smart App Control) otherwise surfaces as a cryptic
# NativeCommandFailed at --install-service, and the Session-0 schtasks fallback
# would fail the same way.
function Test-AiopsAgentRunnable([string]$Exe) {
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = $Exe
  $psi.Arguments = '-h'
  $psi.UseShellExecute = $false
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  $psi.CreateNoWindow = $true
  try {
    $p = [Diagnostics.Process]::Start($psi)
    if (-not $p) { return @{ Ok = $false; Detail = 'Start returned null' } }
    $null = $p.StandardOutput.ReadToEnd()
    $errOut = $p.StandardError.ReadToEnd()
    $p.WaitForExit(15000) | Out-Null
    # Go flag -h exits 2 after printing usage — that still proves the image ran.
    return @{ Ok = $true; ExitCode = $p.ExitCode; Detail = $errOut }
  } catch {
    return @{ Ok = $false; Detail = $_.Exception.Message }
  }
}
# Escape for PowerShell single-quoted literals. MUST use the String overload of
# Replace — Replace([char], …) with $q+$q is a classic PS 5.1 trap (char+char
# becomes Int32, or a two-char string fails Char conversion → MethodException
# that used to abort install AFTER the real Application Control error).
function Escape-AiopsPsSq([string]$s) {
  return ([string]$s).Replace([string]"'", [string]"''")
}
function Write-AiopsAllowHelper([string]$Exe, [string]$Sha256, [string]$OutPs1, [string]$OutXml, [string]$OutCip) {
  $nl = [char]10
  $body = @(
    '#Requires -RunAsAdministrator'
    '# AIOps: allowlist helper for Application Control. Generated by the installer.'
    '$ErrorActionPreference = ''Continue'''
    ('$Exe = ''' + (Escape-AiopsPsSq $Exe) + '''')
    ('$Sha = ''' + (Escape-AiopsPsSq $Sha256) + '''')
    ('$Xml = ''' + (Escape-AiopsPsSq $OutXml) + '''')
    ('$Cip = ''' + (Escape-AiopsPsSq $OutCip) + '''')
    'Write-Host ("[AIOps] Allowing " + $Exe + " (SHA-256=" + $Sha + ")")'
    'try {'
    '  $fi = Get-AppLockerFileInformation -Path $Exe -ErrorAction Stop'
    '  $ar = New-AppLockerPolicy -RuleType Path,Hash -FileInformation $fi -User Everyone -RuleNamePrefix ''AIOpsAgent'' -ErrorAction Stop'
    '  Set-AppLockerPolicy -PolicyObject $ar -Merge -ErrorAction Stop'
    '  Set-Service AppIDSvc -StartupType Automatic -ErrorAction SilentlyContinue'
    '  Start-Service AppIDSvc -ErrorAction SilentlyContinue'
    '  Write-Host "[AIOps] AppLocker allow merged. Re-run the AIOps install command." -ForegroundColor Green'
    '} catch { Write-Host ("[AIOps] AppLocker merge skipped: " + $_.Exception.Message) }'
    '$hasCI = Get-Command New-CIPolicy -ErrorAction SilentlyContinue'
    'if ($hasCI) {'
    '  try {'
    '    New-CIPolicy -Level Hash -ScanPath $Exe -UserPEs -Fallback Hash -FilePath $Xml -ErrorAction Stop'
    '    ConvertFrom-CIPolicy -XmlFilePath $Xml -BinaryFilePath $Cip | Out-Null'
    '    $dest = Join-Path $env:windir ''System32\CodeIntegrity\CiPolicies\Active'''
    '    New-Item -ItemType Directory -Force $dest | Out-Null'
    '    Copy-Item $Cip (Join-Path $dest ([guid]::NewGuid().ToString() + ''.cip'')) -Force'
    '    Write-Host ("[AIOps] WDAC policy written; reboot once, then re-run install. XML=" + $Xml) -ForegroundColor Green'
    '  } catch {'
    '    Write-Host ("[AIOps] WDAC policy generation failed: " + $_.Exception.Message)'
    '    Write-Host ("[AIOps] Give IT this hash to allowlist: " + $Sha)'
    '  }'
    '} else {'
    '  Write-Host "[AIOps] ConfigCI module not available. Give IT this SHA-256 to allowlist:"'
    '  Write-Host ("        " + $Sha)'
    '  Write-Host ("        Path: " + $Exe)'
    '}'
    'Write-Host "[AIOps] If Smart App Control is On and Off is greyed out, only IT allowlist / OS reset / signed agent will help."'
  ) -join $nl
  [System.IO.File]::WriteAllText($OutPs1, $body, (New-Object System.Text.UTF8Encoding $false))
}
function Try-AiopsAppLockerAllow([string]$Exe) {
  try {
    if (-not (Get-Command Get-AppLockerFileInformation -ErrorAction SilentlyContinue)) { return $false }
    $fi = Get-AppLockerFileInformation -Path $Exe -ErrorAction Stop
    # Path+Hash: hash alone breaks on every Agent binary replace/update (Win11
    # managed fleets then look "installed" while terminal/desktop never start).
    $ar = New-AppLockerPolicy -RuleType Path,Hash -FileInformation $fi -User Everyone -RuleNamePrefix 'AIOpsAgent' -ErrorAction Stop
    Set-AppLockerPolicy -PolicyObject $ar -Merge -ErrorAction Stop
    Set-Service AppIDSvc -StartupType Automatic -ErrorAction SilentlyContinue
    Start-Service AppIDSvc -ErrorAction SilentlyContinue
    Write-Host '[AIOps] AppLocker Path+Hash allow merged for Agent binary.' -ForegroundColor Green
    return $true
  } catch {
    Write-Host ("[AIOps] AppLocker auto-allow skipped: " + $_.Exception.Message)
    return $false
  }
}
$Probe = Test-AiopsAgentRunnable $AgentExe
if (-not $Probe.Ok) {
  # Covers WDAC, AppLocker and Software Restriction Policies (SRP). English
  # Windows commonly says exactly "This program is blocked by group policy".
  $Blocked = ($Probe.Detail -match 'Application Control|AppLocker|Smart App Control|Software Restriction|group policy|blocked by policy|blocked this (file|program)|被策略|组策略|無法執行|无法运行|cannot run|0x8007065')
  Write-Host ""
  Write-Host "[AIOps] FATAL: cannot execute $AgentExe" -ForegroundColor Red
  Write-Host ("[AIOps] " + $Probe.Detail) -ForegroundColor Red
  if ($Blocked) {
    Write-Host ""
    Write-Host "[AIOps] Windows Application Control / Group Policy blocked aiops-agent.exe." -ForegroundColor Yellow
    Write-Host "[AIOps] 本机应用程序控制或组策略拦截了 Agent。" -ForegroundColor Yellow

    $SacOn = Test-AiopsSmartAppControlOn
    $AppLockerOn = Test-AiopsAppLockerPresent
    if ($SacOn) {
      Write-Host '[AIOps] Detected: Smart App Control ON（智能应用控制开启；无签名 Agent 会被拦截，需临时关闭 SAC 或使用已签名构建）。' -ForegroundColor Yellow
    }
    if ($AppLockerOn) {
      Write-Host '[AIOps] Detected: AppLocker policy present（存在 AppLocker 策略；Program Files 通常比 AppData 更易放行）。' -ForegroundColor Yellow
    }

    # Non-admin + AppData: one automatic UAC retry into Program Files. This is
    # the common Win10/11 failure mode after declining the first Hyper-V prompt.
    $ElevateMarker = Join-Path $env:TEMP 'aiops-install-elevate-appcontrol.flag'
    if (-not $IsAdmin -and ($Dir -like '*\AppData\Local\*' -or $Dir -like '*\AppData\Roaming\*')) {
      $AlreadyTried = Test-Path -LiteralPath $ElevateMarker
      if (-not $AlreadyTried) {
        try {
          Set-Content -LiteralPath $ElevateMarker -Value (Get-Date -Format o) -Encoding ASCII -Force
          Write-Host '[AIOps] AppData binary blocked - requesting administrator install to Program Files...' -ForegroundColor Yellow
          Write-Host '[AIOps] AppData 被拦截，正在请求管理员权限安装到 Program Files（请在 UAC 点 是）...' -ForegroundColor Yellow
          Request-AiopsElevatedInstall 'appcontrol-appdata'
          return
        } catch {
          Write-Host "[AIOps] Elevation declined; showing manual recovery steps." -ForegroundColor Yellow
        }
      }
    }

    $AllowPs1 = Join-Path $Dir "allow-aiops-agent.ps1"
    $AllowXml = Join-Path $Dir "aiops-agent-allow-wdac.xml"
    $AllowCip = Join-Path $Dir "aiops-agent-allow-wdac.cip"
    try {
      Write-AiopsAllowHelper $AgentExe $Actual $AllowPs1 $AllowXml $AllowCip
    } catch {
      Write-Host ("[AIOps] allow helper write failed: " + $_.Exception.Message) -ForegroundColor Yellow
      $AllowPs1 = $null
    }

    # Admin path: try AppLocker hash allow in-process, then re-probe so the
    # operator does not need a second manual step when AppLocker is the blocker.
    $Recovered = $false
    if ($IsAdmin) {
      Write-Host '[AIOps] Trying AppLocker hash allow (admin)...'
      if (Try-AiopsAppLockerAllow $AgentExe) {
        Start-Sleep -Seconds 2
        $Probe2 = Test-AiopsAgentRunnable $AgentExe
        if ($Probe2.Ok) {
          Write-Host '[AIOps] AppLocker allow succeeded - continuing install.' -ForegroundColor Green
          $Recovered = $true
          $Probe = $Probe2
        } else {
          Write-Host ("[AIOps] Still blocked after AppLocker allow: " + $Probe2.Detail) -ForegroundColor Yellow
        }
      }
      # Offer one-click allow helper when AppLocker merge alone failed.
      if (-not $Recovered -and $AllowPs1 -and (Test-Path $AllowPs1)) {
        Write-Host '[AIOps] Launching allow-aiops-agent.ps1 (admin)...'
        try {
          $p = Start-Process powershell -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-File',$AllowPs1) -Wait -PassThru -WindowStyle Normal
          Start-Sleep -Seconds 2
          $Probe3 = Test-AiopsAgentRunnable $AgentExe
          if ($Probe3.Ok) {
            Write-Host '[AIOps] Allow helper succeeded - continuing install.' -ForegroundColor Green
            $Recovered = $true
            $Probe = $Probe3
          } else {
            Write-Host ("[AIOps] Still blocked after allow helper (exit=" + $p.ExitCode + ")") -ForegroundColor Yellow
          }
        } catch {
          Write-Host ("[AIOps] allow helper launch failed: " + $_.Exception.Message) -ForegroundColor Yellow
        }
      }
    }

    if (-not $Recovered) {
      Write-Host ""
      Write-Host '[AIOps] Fix / 处理办法:' -ForegroundColor Yellow
      Write-Host '[AIOps]  1) 右键开始菜单 PowerShell/终端 -> 以管理员身份运行，再执行原安装命令（装到 Program Files）'
      Write-Host ('[AIOps]     irm "' + $Server + '/install.ps1?token=' + $Token + '" | iex')
      if ($AllowPs1 -and (Test-Path $AllowPs1)) {
        Write-Host '[AIOps]  2) 管理员运行放行脚本后重装:'
        Write-Host ('[AIOps]     powershell -ExecutionPolicy Bypass -File "' + $AllowPs1 + '"')
      } else {
        Write-Host '[AIOps]  2) 让 IT 在 AppLocker / SRP / WDAC 中按路径、哈希或发布者放行'
      }
      Write-Host ('[AIOps]     Path: ' + $AgentExe)
      Write-Host ('[AIOps]     SHA-256 = ' + $Actual)
      Write-Host '[AIOps]  3) Windows 11 个人设备: 设置 -> 隐私和安全性 -> Windows 安全中心 -> 应用和浏览器控制 -> 智能应用控制 -> 关闭，然后重装'
      Write-Host '[AIOps]     (Smart App Control has no per-app exception; unsigned agents need SAC Off or a signed build)'
      try {
        if ($SacOn) {
          Write-Host '[AIOps] Opening Windows Security App and browser control...'
          Start-Process 'windowsdefender://appbrowser' -ErrorAction SilentlyContinue
        }
      } catch {}
      throw 'Agent binary blocked by OS policy; install aborted (no Session-0 fallback).'
    }
  } else {
    throw ("Agent binary not runnable: " + $Probe.Detail)
  }
}
try { Remove-Item -LiteralPath (Join-Path $env:TEMP 'aiops-install-elevate-appcontrol.flag') -Force -ErrorAction SilentlyContinue } catch {}
try {
  Get-AiopsRemoteFile "$Server/dl/plugins.zip" "$Dir\plugins.zip"
  # Expand-Archive needs PowerShell 5+; Server 2012 often ships PS 3/4 — use .NET ZipFile.
  Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction Stop
  $zip = [System.IO.Compression.ZipFile]::OpenRead("$Dir\plugins.zip")
  try {
    foreach ($entry in $zip.Entries) {
      if ($entry.FullName -match '[\\/]$') { continue }
      $out = Join-Path $Dir $entry.FullName
      $parent = Split-Path -Parent $out
      if (-not (Test-Path $parent)) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
      [System.IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $out, $true)
    }
  } finally { $zip.Dispose() }
  Remove-Item "$Dir\plugins.zip" -Force -ErrorAction SilentlyContinue
  Write-Host "[AIOps] plugins extracted"
} catch {
  try {
    if (Get-Command Expand-Archive -ErrorAction SilentlyContinue) {
      Expand-Archive -Path "$Dir\plugins.zip" -DestinationPath $Dir -Force
      Remove-Item "$Dir\plugins.zip" -Force -ErrorAction SilentlyContinue
      Write-Host "[AIOps] plugins extracted (Expand-Archive)"
    } else { throw $_ }
  } catch { Write-Host "[AIOps] plugins skipped: $_" }
}

# Annotated config.yaml from server (active settings + full commented reference).
$AiopsConfigB64 = '__CONFIG_B64__'
try {
  $cfgBytes = [Convert]::FromBase64String($AiopsConfigB64)
  $cfgText = [Text.Encoding]::UTF8.GetString($cfgBytes)
  [System.IO.File]::WriteAllText("$Dir\config.yaml", $cfgText, (New-Object System.Text.UTF8Encoding $false))
  Write-Host "[AIOps] config.yaml written (active settings + full commented reference)"
} catch {
  throw "[AIOps] ERROR: failed to write config.yaml: $_"
}
if ("__SNI_ENABLED__" -eq "true") {
  $TSharkCandidates = @(
    (Get-Command tshark.exe -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Source -ErrorAction SilentlyContinue),
    (Join-Path $env:ProgramFiles "Wireshark\tshark.exe")
  ) | Where-Object { $_ -and (Test-Path $_) }
  if (-not $TSharkCandidates) {
    Write-Warning "Network content audit needs Wireshark TShark and Npcap. Install both, then restart aiops-agent."
  } else {
    Write-Host "[AIOps] TShark/Npcap audit dependency detected." -ForegroundColor Green
  }
}
# Migrate: remove a stale config.json from a pre-YAML install (agent now prefers YAML).
Remove-Item "$Dir\config.json" -Force -ErrorAction SilentlyContinue

# User-level autostart + keepalive (no admin required).
# start-agent.vbs is a *supervisor*: it launches the agent ONLY if it is not
# already running, so neither the logon Run key nor the 5-minute keepalive task
# ever spawns a duplicate. Two triggers together mean the agent survives both a
# reboot (Run key at logon) and being stopped/killed (task relaunches within 5m).
$exe  = "$Dir\aiops-agent.exe"
$conf = "$Dir\config.yaml"
$vbs  = "$Dir\start-agent.vbs"
$runLine = 'CreateObject("WScript.Shell").Run """' + $exe + '"" --config ""' + $conf + '""", 0, False'
$vbsBody = @"
' AIOps agent supervisor — start the agent only if it is not already running.
Dim running : running = False
On Error Resume Next
Dim wmi : Set wmi = GetObject("winmgmts:{impersonationLevel=impersonate}!\\.\root\cimv2")
Dim procs : Set procs = wmi.ExecQuery("SELECT ProcessId FROM Win32_Process WHERE Name = 'aiops-agent.exe'")
If Not procs Is Nothing Then If procs.Count > 0 Then running = True
On Error GoTo 0
If Not running Then $runLine
"@
[System.IO.File]::WriteAllText($vbs, $vbsBody, (New-Object System.Text.UTF8Encoding $false))

# (Prior instance already stopped + task deleted before the download above.)
if ($IsAdmin) {
  # Elevated: install a real Windows service (LocalSystem, SERVICE_AUTO_START +
  # crash-recovery). This is strictly better than the SYSTEM keepalive task:
  #   1. Boot autostart — the host reports metrics after a reboot BEFORE anyone
  #      logs in (a per-user/Session-0 keepalive left the host offline until login).
  #   2. Remote desktop actually works. A Session-0 task CANNOT capture the screen
  #      (GDI BitBlt fails with "must run in an interactive session"); the service
  #      spawns a desktop worker into the active console session and follows the
  #      secure desktop, so capture works even on the lock/login screen.
  # A SYSTEM service has the same privileges the keepalive task had, so Hyper-V
  # Get-VM collection still works.
  Remove-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "AIOpsAgent" -ErrorAction SilentlyContinue
  Remove-AiopsScheduledTask 'AIOpsAgent'
  $eap = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  # Do not pipe native stderr through ForEach-Object: Windows PowerShell 5.1
  # decodes redirected native output using its legacy code page before the
  # pipeline sees it. Let the UTF-8 Agent write directly to the UTF-8 console.
  $InstallErr = $null
  try {
    & $AgentExe --install-service --config $conf
    $AgentInstallExit = $LASTEXITCODE
  } catch {
    $InstallErr = $_.Exception.Message
    $AgentInstallExit = 1
    Write-Host ("[AIOps] " + $InstallErr) -ForegroundColor Red
  }
  if ($AgentInstallExit -ne 0) {
    Write-Warning "[AIOps] Agent service installer exited with code $AgentInstallExit"
  }
  # Poll for the service: CreateService+Start can take >1.5s on a busy 2012 host.
  # IMPORTANT: if the service EXISTS we must NOT fall back to a SYSTEM schtasks
  # keepalive — that Session-0 agent fights the service worker for deskWait and
  # produces black / "connected" remote desktop. Only fall back when SCM refused
  # to register the service at all.
  $svc = $null
  for ($i = 0; $i -lt 30; $i++) {
    $svc = Get-Service -Name "AiopsMonitorAgent" -ErrorAction SilentlyContinue
    if ($svc) { break }
    Start-Sleep -Milliseconds 500
  }
  if ($svc) {
    # Always restart (not only Start): covers fresh install and reinstall.
    try { Restart-Service -Name "AiopsMonitorAgent" -Force -ErrorAction SilentlyContinue } catch {}
    for ($i = 0; $i -lt 20; $i++) {
      $svc.Refresh()
      if ($svc.Status -eq 'Running') { break }
      if ($svc.Status -eq 'Stopped') {
        try { Restart-Service -Name "AiopsMonitorAgent" -Force -ErrorAction SilentlyContinue } catch {
          Start-Service -Name "AiopsMonitorAgent" -ErrorAction SilentlyContinue
        }
      }
      Start-Sleep -Milliseconds 500
    }
    $svc.Refresh()
    if ($svc.Status -eq 'Running') {
      Write-Host "[AIOps] Windows service restarted: AiopsMonitorAgent (LocalSystem, boot autostart + crash-restart + desktop worker)."
    } else {
      Write-Host ""
      Write-Host ("[AIOps] FATAL: AiopsMonitorAgent service is registered but not Running (status=$($svc.Status)).") -ForegroundColor Red
      Write-Host "[AIOps] Remote terminal/desktop will NOT work until the service is Running (desktop worker is spawned by the service)." -ForegroundColor Yellow
      Write-Host ("[AIOps] Check agent.log under: " + $Dir) -ForegroundColor Yellow
      Write-Host "[AIOps] Not falling back to Session-0 keepalive (that breaks remote desktop)." -ForegroundColor Yellow
      throw "AiopsMonitorAgent service failed to reach Running state"
    }
  } else {
    $PolicyBlocked = ($InstallErr -and ($InstallErr -match 'Application Control|AppLocker|Smart App Control|Software Restriction|group policy|blocked by policy|被策略|组策略|无法运行'))
    if ($PolicyBlocked) {
      Write-Host "[AIOps] FATAL: Windows Application Control blocked aiops-agent.exe — refusing Session-0 keepalive fallback (it would also be blocked)." -ForegroundColor Red
      Write-Host "[AIOps] Ask IT to allowlist $AgentExe (WDAC/AppLocker path or hash), or temporarily turn off Smart App Control, then re-run this installer." -ForegroundColor Yellow
      throw "Agent blocked by Application Control policy"
    }
    Write-Host "[AIOps] service registration unavailable; falling back to SYSTEM keepalive task."
    Write-Host "[AIOps] WARNING: Session-0 keepalive cannot drive interactive remote desktop (lock screen / console capture). Prefer fixing service install." -ForegroundColor Yellow
    $trTask = 'wscript.exe \"' + $vbs + '\"'
    $ErrorActionPreference = 'Continue'
    & "$env:SystemRoot\System32\schtasks.exe" /Create /TN "AIOpsAgent" /TR $trTask /SC MINUTE /MO 5 /RU SYSTEM /RL HIGHEST /F 1>$null 2>$null | Out-Null
    & "$env:SystemRoot\System32\schtasks.exe" /Run /TN "AIOpsAgent" 1>$null 2>$null | Out-Null
  }
  $ErrorActionPreference = $eap
} else {
  # Non-elevated: classic per-user autostart. Prefer launching the exe directly —
  # many Win10/11 / enterprise images disable Windows Script Host, which made the
  # old VBS+Run-key path look installed while nothing ever started.
  function Test-AiopsWScriptEnabled {
    try {
      $hkcu = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows Script Host\Settings' -ErrorAction SilentlyContinue
      if ($hkcu -and $hkcu.Enabled -eq 0) { return $false }
    } catch {}
    try {
      $hklm = Get-ItemProperty 'HKLM:\Software\Microsoft\Windows Script Host\Settings' -ErrorAction SilentlyContinue
      if ($hklm -and $hklm.Enabled -eq 0) { return $false }
    } catch {}
    return $true
  }
  $WshOk = Test-AiopsWScriptEnabled
  $AgentCmd = '"' + $exe + '" --config "' + $conf + '"'
  New-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "AIOpsAgent" -Value $AgentCmd -PropertyType String -Force | Out-Null
  if ($WshOk) {
    $trTask = 'wscript.exe \"' + $vbs + '\"'
  } else {
    Write-Host "[AIOps] Windows Script Host is disabled; using direct agent autostart (no VBS)." -ForegroundColor Yellow
    $trTask = 'powershell.exe -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -Command "Start-Process -FilePath ''' + $exe + ''' -ArgumentList ''--config'',''' + $conf + ''' -WindowStyle Hidden"'
  }
  $eap = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  & "$env:SystemRoot\System32\schtasks.exe" /Create /TN "AIOpsAgent" /TR $trTask /SC MINUTE /MO 5 /F 1>$null 2>$null | Out-Null
  $ErrorActionPreference = $eap
  # Start immediately (do not rely solely on Run key / schtasks / WScript).
  try {
    Start-Process -FilePath $exe -ArgumentList @('--config', $conf) -WindowStyle Hidden -ErrorAction Stop
    Write-Host "[AIOps] agent process started (user-level)."
  } catch {
    Write-Host ("[AIOps] WARN: failed to start agent process: " + $_.Exception.Message) -ForegroundColor Yellow
    if ($WshOk) {
      Start-Process "wscript.exe" -ArgumentList ('"' + $vbs + '"') -ErrorAction SilentlyContinue
    }
  }
  Write-Host "[AIOps] installed (user-level, no admin). Check the dashboard."
  Write-Host "[AIOps] NOTE: Hyper-V VM collection needs admin. On a Hyper-V host, re-run this install command in an ELEVATED PowerShell."
  Write-Host "[AIOps] TIP: For reliable boot-time monitoring on Windows 10/11, re-run in an elevated PowerShell (registers AiopsMonitorAgent service)."
}
# Prove the host can actually reach the panel before claiming success.
# "Service is Running" says nothing about DNS, firewalls or token validity: a
# service that cannot register reaches Running and then reports into the void,
# which is how a fully green install produced an empty dashboard with nothing to
# look at (a Windows service has no console and its stderr is discarded).
$SelfTestExit = -1
$eap2 = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
try {
  & $AgentExe --selftest --config $conf
  $SelfTestExit = $LASTEXITCODE
} catch {
  Write-Host ("[AIOps] self-test could not run: " + $_.Exception.Message) -ForegroundColor Yellow
}
$ErrorActionPreference = $eap2

Write-Host "[AIOps] --- capability summary ---"
Write-Host ("[AIOps] elevated   : " + $IsAdmin)
Write-Host ("[AIOps] install dir: " + $Dir)
$svcOk = $false
try { $svcOk = [bool](Get-Service -Name 'AiopsMonitorAgent' -ErrorAction SilentlyContinue) } catch {}
Write-Host ("[AIOps] win service: " + $svcOk + " (SYSTEM service => disk IO + Hyper-V)")
$hv = $false
try { $hv = [bool](Get-Service -Name 'vmms' -ErrorAction SilentlyContinue) } catch {}
Write-Host ("[AIOps] Hyper-V role: " + $hv + $(if ($hv -and -not $IsAdmin) { " (needs elevated reinstall)" } else { "" }))
Write-Host ("[AIOps] agent log   : " + (Join-Path $Dir 'agent.log') + " (7×10MB rolling)")
if ($SelfTestExit -eq 0) {
  Write-Host "[AIOps] connectivity: OK — 主机已注册，面板上应能看到这台机器。" -ForegroundColor Green
  Write-Host "[AIOps] done. Check the dashboard for this host."
} elseif ($SelfTestExit -lt 0) {
  Write-Host "[AIOps] connectivity: UNKNOWN (self-test did not run)" -ForegroundColor Yellow
  Write-Host "[AIOps] done, but connectivity was not verified. Check the dashboard for this host."
} else {
  Write-Host "[AIOps] connectivity: FAILED — 主机不会出现在面板，原因见上方 [selftest] 输出。" -ForegroundColor Red
  Write-Host ("[AIOps] 重新自检: & '" + $AgentExe + "' --selftest --config '" + $conf + "'") -ForegroundColor Yellow
  Write-Host ("[AIOps] 运行日志: " + (Join-Path $Dir 'agent.log') + " (7×10MB 滚动覆盖)") -ForegroundColor Yellow
  # Deliberately no "exit": this script is consumed via irm ... | iex, so exiting
  # tears down the operator's console — taking the diagnosis above with it.
  $global:LASTEXITCODE = 1
}
`

// relayInstallShTemplate installs the agent in GATEWAY RELAY mode on Linux /
// macOS. The relay listens on a local port and reverse-proxies all requests to
// the cloud server — internal machines that can't reach the internet point their
// agents at this relay instead. Only the gateway machine needs internet access.
const relayInstallShTemplate = `#!/bin/sh
set -e
SERVER="__SERVER__"
TOKEN="__TOKEN__"
CATEGORY="__CATEGORY__"
FOLDER_ID="__FOLDER_ID__"
LISTEN="${RELAY_LISTEN:-:8529}"
if [ "$(id -u)" = "0" ]; then DIR="${AIOPS_DIR:-/opt/aiops-agent}"; else DIR="${AIOPS_DIR:-$HOME/.aiops-agent}"; fi

# True only when systemd is the real init (not a container that merely has systemctl).
aiops_has_systemd() {
  command -v systemctl >/dev/null 2>&1 || return 1
  if [ -r /proc/1/comm ] && [ "$(cat /proc/1/comm 2>/dev/null)" = "systemd" ]; then
    return 0
  fi
  _st=$(systemctl is-system-running 2>/dev/null || true)
  case "$_st" in
    running|degraded|maintenance|starting) return 0 ;;
  esac
  return 1
}

OS=$(uname -s)
ARCH=$(uname -m)
case "$OS" in
  Linux)
    case "$ARCH" in
      x86_64|amd64)   BIN="aiops-agent-linux-amd64" ;;
      aarch64|arm64)   BIN="aiops-agent-linux-arm64" ;;
      loongarch64)     BIN="aiops-agent-linux-loong64" ;;
      riscv64)         BIN="aiops-agent-linux-riscv64" ;;
      i386|i686)       BIN="aiops-agent-linux-386" ;;
      armv7l|armv7|armv6l|armhf) BIN="aiops-agent-linux-arm" ;;
      *)
        echo "[AIOps] ERROR: unsupported architecture: $ARCH (supported: x86_64/amd64, aarch64/arm64, loongarch64, riscv64, i386/i686, armv7l)"
        exit 1
        ;;
    esac
    ;;
  Darwin)
    case "$ARCH" in
      arm64)           BIN="aiops-agent-darwin-arm64" ;;
      x86_64)          BIN="aiops-agent-darwin-amd64" ;;
      *)
        echo "[AIOps] ERROR: unsupported architecture: $ARCH (supported: arm64, x86_64)"
        exit 1
        ;;
    esac
    ;;
  *) echo "unsupported OS: $OS"; exit 1 ;;
esac

echo "[AIOps] installing relay to $DIR (upstream $SERVER)"
echo "[AIOps] platform $OS/$ARCH → $BIN"
mkdir -p "$DIR"
cd "$DIR"
# resumable + retried download: on flaky/cross-border links, don't re-fetch the
# whole 7.5MB from scratch. -C - resumes a partial; on a complete file the server
# returns 416, so fall back to a plain full GET.
if ! curl -fSL --retry 3 --retry-delay 2 -C - "$SERVER/dl/$BIN" -o aiops-agent; then
  if ! curl -fsSL "$SERVER/dl/$BIN" -o aiops-agent; then
    echo "[AIOps] ERROR: failed to download $SERVER/dl/$BIN (HTTP 404 usually means the server image lacks this platform binary)."
    echo "[AIOps] Fix: rebuild server with full agent dist (production Dockerfile or updated Dockerfile.dev)."
    exit 1
  fi
fi
# SHA-256 verify (parity with normal install; refuse replace on mismatch).
if curl -fsSL "$SERVER/dl/$BIN.sha256" -o ".aiops-agent.sha256" 2>/dev/null; then
  EXPECTED=$(awk '{print $1}' .aiops-agent.sha256 | tr 'A-F' 'a-f')
  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL=$(sha256sum aiops-agent | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL=$(shasum -a 256 aiops-agent | awk '{print $1}')
  else
    ACTUAL=""
  fi
  if [ -n "$EXPECTED" ] && [ -n "$ACTUAL" ] && [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "[AIOps] ERROR: SHA-256 mismatch for $BIN"; rm -f aiops-agent; exit 1
  fi
  rm -f .aiops-agent.sha256
fi
chmod +x aiops-agent

# Write YAML without expanding metacharacters inside AIOPS_RELAY_SECRET.
# token/category are what put the gateway itself in the host list: it relays AND
# reports, so the one machine every internal agent depends on is monitored too.
{
  printf 'relay: true\n'
  printf 'listen: "%s"\n' "$LISTEN"
  printf 'server: "%s"\n' "$SERVER"
  if [ -n "$TOKEN" ]; then printf 'token: "%s"\n' "$TOKEN"; fi
  if [ -n "$CATEGORY" ]; then printf 'category: "%s"\n' "$CATEGORY"; else printf 'category: "relay-gateway"\n'; fi
  if [ -n "$FOLDER_ID" ]; then printf 'folder_id: "%s"\n' "$FOLDER_ID"; fi
  if [ -n "${AIOPS_RELAY_SECRET:-}" ]; then
    _esc=$(printf '%s' "$AIOPS_RELAY_SECRET" | sed 's/\\/\\\\/g; s/"/\\"/g')
    printf 'relay_secret: "%s"\n' "$_esc"
  fi
} > config.yaml
if [ ! -s config.yaml ]; then
  echo "[AIOps] ERROR: config.yaml was not created! Installation incomplete."
  exit 1
fi
# Owner-only: this file now carries the install token as well as relay_secret.
chmod 600 config.yaml 2>/dev/null || true
rm -f config.json 2>/dev/null || true
echo "[AIOps] config written: $DIR/config.yaml (upstream: $SERVER, listen: $LISTEN)"

aiops_start_relay_fallback() {
  # Match by config path — argv has no "relay" flag, so pkill ...*relay never worked.
  pkill -f "$DIR/aiops-agent --config $DIR/config.yaml" 2>/dev/null || true
  pkill -f "$DIR/aiops-agent" 2>/dev/null || true
  sleep 1 2>/dev/null || true
  nohup "$DIR/aiops-agent" --config "$DIR/config.yaml" > "$DIR/relay.log" 2>&1 &
  echo "[AIOps] relay started in background (log: $DIR/relay.log)"
}

if aiops_has_systemd && [ "$(id -u)" = "0" ]; then
  cat > /etc/systemd/system/aiops-relay.service <<UNIT
[Unit]
Description=AIOps Relay
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
WorkingDirectory=$DIR
ExecStart=$DIR/aiops-agent --config $DIR/config.yaml
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable aiops-relay >/dev/null 2>&1 || true
  # restart must not abort install under set -e when systemd is half-broken
  if ! systemctl restart aiops-relay; then
    echo "[AIOps] WARN: systemctl restart aiops-relay failed; falling back to nohup"
    aiops_start_relay_fallback
  else
    echo "[AIOps] relay systemd service (re)started: aiops-relay (listen $LISTEN)"
  fi
elif [ "$OS" = "Darwin" ]; then
  # macOS launchd — mirror normal agent install so relay survives reboot.
  if [ -z "${AIOPS_USER:-}" ]; then
    if [ "$(id -u)" = "0" ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ] && id "$SUDO_USER" >/dev/null 2>&1; then
      AIOPS_USER="$SUDO_USER"
    elif [ "$(id -u)" = "0" ]; then
      AIOPS_USER="root"
    else
      AIOPS_USER="$(id -un)"
    fi
  fi
  AIOPS_UID="$(id -u "$AIOPS_USER" 2>/dev/null || id -u)"
  if [ "$AIOPS_USER" != "root" ]; then
    AIOPS_HOME=$(eval echo "~$AIOPS_USER" 2>/dev/null || true)
    [ -n "$AIOPS_HOME" ] && [ -d "$AIOPS_HOME" ] || AIOPS_HOME="/Users/$AIOPS_USER"
    chown -R "$AIOPS_USER" "$DIR" 2>/dev/null || true
    PLIST_DIR="$AIOPS_HOME/Library/LaunchAgents"
    mkdir -p "$PLIST_DIR"
    chown "$AIOPS_USER" "$PLIST_DIR" 2>/dev/null || true
  else
    PLIST_DIR="/Library/LaunchDaemons"
    mkdir -p "$PLIST_DIR"
  fi
  PLIST="$PLIST_DIR/com.aiops.relay.plist"
  cat > "$PLIST" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.aiops.relay</string>
  <key>ProgramArguments</key>
  <array>
    <string>$DIR/aiops-agent</string>
    <string>--config</string>
    <string>$DIR/config.yaml</string>
  </array>
  <key>WorkingDirectory</key><string>$DIR</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$DIR/relay.log</string>
  <key>StandardErrorPath</key><string>$DIR/relay.log</string>
</dict>
</plist>
PL
  [ "$AIOPS_USER" != "root" ] && chown "$AIOPS_USER" "$PLIST" 2>/dev/null || true
  xattr -dr com.apple.quarantine "$DIR/aiops-agent" 2>/dev/null || true
  launchctl unload "$PLIST" 2>/dev/null || true
  if [ "$AIOPS_USER" = "root" ]; then
    launchctl bootout system "$PLIST" 2>/dev/null || true
    launchctl bootstrap system "$PLIST" 2>/dev/null || launchctl load -w "$PLIST" 2>/dev/null || true
    launchctl kickstart -k system/com.aiops.relay 2>/dev/null || true
    echo "[AIOps] launchd LaunchDaemon restarted: com.aiops.relay (listen $LISTEN)"
  else
    launchctl bootout "gui/$AIOPS_UID" "$PLIST" 2>/dev/null || true
    launchctl bootstrap "gui/$AIOPS_UID" "$PLIST" 2>/dev/null || launchctl asuser "$AIOPS_UID" launchctl load -w "$PLIST" 2>/dev/null || true
    launchctl enable "gui/$AIOPS_UID/com.aiops.relay" 2>/dev/null || true
    launchctl kickstart -k "gui/$AIOPS_UID/com.aiops.relay" 2>/dev/null || true
    echo "[AIOps] launchd LaunchAgent restarted: com.aiops.relay (user=$AIOPS_USER, listen $LISTEN)"
  fi
else
  aiops_start_relay_fallback
fi
RELAY_PORT="${LISTEN##*:}"
echo ""
echo "[AIOps] Relay ready! Internal machines install with:"
echo "  curl -fsSL http://<this-host-ip>:${RELAY_PORT}/install.sh | sh"
`

// relayInstallPs1Template installs the agent in GATEWAY RELAY mode on Windows.
const relayInstallPs1Template = `$ErrorActionPreference = "Stop"
# Force TLS 1.2 (numeric 3072) so downloads work on Server 2012/2016 which default to TLS 1.0.
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072 } catch {}
$Server   = "__SERVER__"
$Token    = "__TOKEN__"
$Category = "__CATEGORY__"
$FolderID = "__FOLDER_ID__"
$Listen = if ($env:RELAY_LISTEN) { $env:RELAY_LISTEN } else { ":8529" }
$Dir    = Join-Path $env:LOCALAPPDATA "aiops-agent"

Write-Host "[AIOps] installing relay to $Dir (upstream $Server)"
New-Item -ItemType Directory -Force $Dir | Out-Null
# Stop only relay/agent processes from THIS install dir (do not kill unrelated agents).
Get-Process aiops-agent -ErrorAction SilentlyContinue | Where-Object {
  try {
    $p = $_.Path
    $p -and $p.StartsWith($Dir, [System.StringComparison]::OrdinalIgnoreCase)
  } catch { $false }
} | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 800
$ProcArch = [string]$env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $ProcArch = [string]$env:PROCESSOR_ARCHITEW6432 }
$AgentRemote = 'aiops-agent.exe'
if ($ProcArch.ToUpperInvariant() -eq 'ARM64') { $AgentRemote = 'aiops-agent-windows-arm64.exe' }
Write-Host ("[AIOps] platform windows/" + $ProcArch + " → " + $AgentRemote)
try {
  Invoke-WebRequest "$Server/dl/$AgentRemote" -OutFile "$Dir\aiops-agent.exe" -UseBasicParsing
} catch {
  if ($AgentRemote -ne 'aiops-agent.exe') {
    Write-Host ("[AIOps] WARN: " + $AgentRemote + " missing; falling back to aiops-agent.exe") -ForegroundColor Yellow
    Invoke-WebRequest "$Server/dl/aiops-agent.exe" -OutFile "$Dir\aiops-agent.exe" -UseBasicParsing
  } else { throw }
}

# YAML is the default config format (single-quoted scalars are backslash-safe; any
# embedded single-quote is doubled per YAML rules). No YAML serializer in PowerShell.
# SHA-256 verify when checksum is available.
try {
  $sumBody = (Invoke-WebRequest "$Server/dl/$AgentRemote.sha256" -UseBasicParsing).Content
  $Expected = (($sumBody -split '\s+')[0]).Trim().ToLowerInvariant()
  $Sha = [Security.Cryptography.SHA256]::Create()
  $Stream = [IO.File]::OpenRead("$Dir\aiops-agent.exe")
  try { $Actual = ([BitConverter]::ToString($Sha.ComputeHash($Stream))).Replace('-','').ToLowerInvariant() }
  finally { $Stream.Dispose(); $Sha.Dispose() }
  if ($Expected -and $Actual -and ($Expected -ne $Actual)) { throw "SHA-256 mismatch for $AgentRemote" }
} catch {
  if ($_.Exception.Message -match 'SHA-256') { throw }
  Write-Host "[AIOps] WARN: checksum unavailable; continuing" -ForegroundColor Yellow
}
$RelayLines = New-Object System.Collections.Generic.List[string]
$RelayLines.Add("relay: true")
$RelayLines.Add("listen: '" + (([string]$Listen) -replace "'", "''") + "'")
$RelayLines.Add("server: '" + (([string]$Server) -replace "'", "''") + "'")
# token/category put the gateway itself in the host list: it relays AND reports.
if ($Token) { $RelayLines.Add("token: '" + (([string]$Token) -replace "'", "''") + "'") }
if ($Category) {
  $RelayLines.Add("category: '" + (([string]$Category) -replace "'", "''") + "'")
} else {
  $RelayLines.Add("category: 'relay-gateway'")
}
if ($FolderID) { $RelayLines.Add("folder_id: '" + (([string]$FolderID) -replace "'", "''") + "'") }
if ($env:AIOPS_RELAY_SECRET) {
  $RelayLines.Add("relay_secret: '" + (([string]$env:AIOPS_RELAY_SECRET) -replace "'", "''") + "'")
}
$cfg = ($RelayLines -join ([char]10)) + ([char]10)
[System.IO.File]::WriteAllText("$Dir\config.yaml", $cfg, (New-Object System.Text.UTF8Encoding $false))
# Migrate: remove a stale config.json from a pre-YAML install (agent now prefers YAML).
Remove-Item "$Dir\config.json" -Force -ErrorAction SilentlyContinue

$exe  = "$Dir\aiops-agent.exe"
$conf = "$Dir\config.yaml"
$vbs  = "$Dir\start-relay.vbs"
$line = 'CreateObject("WScript.Shell").Run """' + $exe + '"" --config ""' + $conf + '""", 0, False'
[System.IO.File]::WriteAllText($vbs, $line, (New-Object System.Text.ASCIIEncoding))
New-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "AIOpsRelay" -Value ('wscript.exe "' + $vbs + '"') -PropertyType String -Force | Out-Null

Get-Process aiops-agent -ErrorAction SilentlyContinue | Where-Object {
  try {
    $p = $_.Path
    $p -and $p.StartsWith($Dir, [System.StringComparison]::OrdinalIgnoreCase)
  } catch { $false }
} | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 400
Start-Process "wscript.exe" -ArgumentList ('"' + $vbs + '"')
$Port = $Listen -replace '.*:', ''
Write-Host "[AIOps] relay installed and started (listen $Listen)"
Write-Host "[AIOps] internal machines use: http://<this-host-ip>:$Port"
`

// uninstallShTemplate stops + removes the agent on Linux / macOS.
const uninstallShTemplate = `#!/bin/sh
if [ "$(id -u)" = "0" ]; then DIR="${AIOPS_DIR:-/opt/aiops-agent}"; else DIR="${AIOPS_DIR:-$HOME/.aiops-agent}"; fi
echo "[AIOps] uninstalling from $DIR"
# Full unit purge: stop/disable + remove unit file AND *.service.d drop-ins
# (systemctl edit leftovers). Leaving drop-ins behind re-applies old hardening
# on reinstall and breaks remote terminal.
aiops_purge_systemd_unit() {
  _u="$1"
  [ -n "$_u" ] || return 0
  if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now "$_u" 2>/dev/null || true
    systemctl stop "$_u" 2>/dev/null || true
    systemctl reset-failed "$_u" 2>/dev/null || true
  fi
  rm -f "/etc/systemd/system/${_u}.service" \
        "/lib/systemd/system/${_u}.service" \
        "/usr/lib/systemd/system/${_u}.service" \
        "/etc/systemd/system/multi-user.target.wants/${_u}.service"
  rm -rf "/etc/systemd/system/${_u}.service.d" \
         "/lib/systemd/system/${_u}.service.d" \
         "/usr/lib/systemd/system/${_u}.service.d" \
         "/run/systemd/system/${_u}.service.d"
}
if command -v systemctl >/dev/null 2>&1; then
  for _u in aiops-agent aiops-monitor-agent aiops-relay; do
    aiops_purge_systemd_unit "$_u"
  done
  systemctl daemon-reload 2>/dev/null || true
fi
# launchd (macOS): remove both the per-user LaunchAgent and the root LaunchDaemon
# (one-liner label + legacy --install-service label).
for PLIST in \
  "$HOME/Library/LaunchAgents/com.aiops.agent.plist" \
  "/Library/LaunchDaemons/com.aiops.agent.plist" \
  "$HOME/Library/LaunchAgents/com.aiops.monitor.agent.plist" \
  "/Library/LaunchDaemons/com.aiops.monitor.agent.plist" \
  "$HOME/Library/LaunchAgents/com.aiops.relay.plist" \
  "/Library/LaunchDaemons/com.aiops.relay.plist"
do
  if [ -f "$PLIST" ]; then
    launchctl unload "$PLIST" 2>/dev/null || true
    launchctl bootout system "$PLIST" 2>/dev/null || true
    if [ "$(id -u)" != "0" ]; then
      launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true
    fi
    rm -f "$PLIST"
  fi
done
# Remove the @reboot crontab entry added by the non-root fallback install.
if command -v crontab >/dev/null 2>&1; then
  crontab -l 2>/dev/null | grep -v 'aiops-agent --config' | crontab - 2>/dev/null || true
fi
pkill -x aiops-agent 2>/dev/null || true
pkill -f 'aiops-agent --config' 2>/dev/null || true
# Also drop the other common install root so a root↔user switch leaves no orphan.
for _d in "$DIR" /opt/aiops-agent "${HOME}/.aiops-agent"; do
  [ -n "$_d" ] && [ -d "$_d" ] && rm -rf "$_d"
done
echo "[AIOps] uninstalled. You may delete the host card in the dashboard."
`

// uninstallPs1Template stops + removes the agent on Windows (user-level).
// v5.2.6: Comprehensive rewrite to fix multiple uninstall failures.
// v5.2.9: Regression fixes:
//  1. Replace Get-CimInstance (unreliable CommandLine) with taskkill / Get-Process
//  2. Kill ALL wscript.exe instances (safe on uninstall — no other apps use it)
//  3. Kill ALL aiops-agent.exe instances by name
//  4. Add $ErrorActionPreference = "Continue" for error visibility
//  5. Longer retry delays (2/4/8s) and MoveFileEx for stubborn files
//  6. Explicitly delete VBS files before EXE to release Run registry triggers
const uninstallPs1Template = `$ErrorActionPreference = "Continue"
# Clean all install locations: per-user, current elevated Program Files path,
# and the legacy ProgramData path used before hardened-GPO compatibility.
$Dirs = @(
  (Join-Path $env:LOCALAPPDATA "aiops-agent"),
  (Join-Path $env:ProgramFiles "AIOps Agent"),
  (Join-Path $env:ProgramData "aiops-agent")
)
Write-Host "[AIOps] uninstalling ($($Dirs -join '; '))"

# An elevated (SYSTEM) install registers its service + files machine-wide.
# Removing the service and Program Files/legacy ProgramData needs admin: without it,
# is access-denied (silently), the task relaunches the agent within 5 min, and the
# file deletion below fails — the classic "uninstall didn't work". Warn up front.
$IsAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
$SystemDirs = @((Join-Path $env:ProgramFiles "AIOps Agent"), (Join-Path $env:ProgramData "aiops-agent"))
$SystemInstall = $SystemDirs | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $IsAdmin -and $SystemInstall) {
    Write-Host "[AIOps] WARNING: an elevated (SYSTEM) install exists at $SystemInstall."
    Write-Host "[AIOps] Its Windows service / SYSTEM scheduled task CANNOT be removed without admin"
    Write-Host "[AIOps] and will keep the agent running. Re-run this uninstall in an ELEVATED PowerShell"
    Write-Host "[AIOps] (Run as Administrator) to fully remove it."
}

# Step 1: Remove ALL autostart entries (normal + relay modes).
# HKCU alone is not enough: a per-user install belongs to whoever ran it, and an
# elevated uninstall sees the ADMIN's hive and profile. That is why uninstalling
# "had no effect" and an old agent kept reporting — it lived in another profile.
Remove-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "AIOpsAgent" -ErrorAction SilentlyContinue
Remove-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "AIOpsRelay" -ErrorAction SilentlyContinue
function Remove-AiopsAllUserInstalls {
  $found = $false
  $profiles = @()
  try {
    $profiles = Get-ChildItem 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList' -ErrorAction SilentlyContinue |
      ForEach-Object {
        $sid = Split-Path -Leaf $_.Name
        # Machine/service accounts never carry a per-user install, and probing
        # their profile paths only raises access-denied noise mid-install.
        if ($sid -eq 'S-1-5-18' -or $sid -eq 'S-1-5-19' -or $sid -eq 'S-1-5-20' -or $sid -like 'S-1-5-80-*') { return }
        $p = $null
        try { $p = (Get-ItemProperty -Path $_.PSPath -Name ProfileImagePath -ErrorAction SilentlyContinue).ProfileImagePath } catch {}
        if ($p -and (Test-Path -LiteralPath $p -ErrorAction SilentlyContinue)) { New-Object PSObject -Property @{ Sid = $sid; Path = $p } }
      }
  } catch {}
  foreach ($u in $profiles) {
    $hive = "Registry::HKEY_USERS\" + $u.Sid
    $mounted = $false
    if (-not (Test-Path $hive)) {
      $hive = $null
      $dat = Join-Path $u.Path 'NTUSER.DAT'
      if (Test-Path -LiteralPath $dat) {
        & "$env:SystemRoot\System32\reg.exe" load 'HKU\AIOpsTmp' $dat 1>$null 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) { $hive = 'Registry::HKEY_USERS\AIOpsTmp'; $mounted = $true }
      }
    }
    if ($hive) {
      $runKey = $hive + '\Software\Microsoft\Windows\CurrentVersion\Run'
      foreach ($n in @('AIOpsAgent','AIOpsRelay')) {
        $existing = $null
        try { $existing = Get-ItemProperty -Path $runKey -Name $n -ErrorAction SilentlyContinue } catch {}
        if ($existing) {
          $found = $true
          Write-Host ("[AIOps] removing stale autostart: " + $u.Path + " -> " + $n)
          Remove-ItemProperty -Path $runKey -Name $n -ErrorAction SilentlyContinue
        }
      }
    }
    if ($mounted) {
      [gc]::Collect(); [gc]::WaitForPendingFinalizers()
      & "$env:SystemRoot\System32\reg.exe" unload 'HKU\AIOpsTmp' 1>$null 2>$null | Out-Null
    }
    foreach ($sub in @('AppData\Local\aiops-agent','AppData\Roaming\aiops-agent')) {
      $d = Join-Path $u.Path $sub
      if (Test-Path -LiteralPath $d) {
        $found = $true
        Write-Host ("[AIOps] removing legacy per-user install: " + $d)
        Remove-Item -Recurse -Force -LiteralPath $d -ErrorAction SilentlyContinue
      }
    }
  }
  if ($found -and -not $IsAdmin) {
    Write-Host '[AIOps] WARNING: found other users'' agents but this window is not elevated; some may survive. Re-run as Administrator.' -ForegroundColor Yellow
  }
}
# Called after the processes are killed below — the directories are locked until
# then. The Run-key sweep it also does is safe at any point.

# Step 2: Remove the keepalive scheduled task FIRST — otherwise it relaunches the
# agent within 5 minutes and the file deletion below fails ("can't uninstall").
# Delete both the current name and the legacy hyphenated one.
# Never use cmd.exe (GPO often blocks it); prefer ScheduledTasks cmdlets + schtasks.exe.
function Remove-AiopsScheduledTask([string]$Name) {
  try {
    if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
      $t = Get-ScheduledTask -TaskName $Name -ErrorAction SilentlyContinue
      if ($t) { Unregister-ScheduledTask -TaskName $Name -Confirm:$false -ErrorAction SilentlyContinue }
    }
  } catch {}
  try { & "$env:SystemRoot\System32\schtasks.exe" /Delete /TN $Name /F 1>$null 2>$null | Out-Null } catch {}
}
function Stop-AiopsServiceQuiet {
  try {
    $svc = Get-Service -Name 'AiopsMonitorAgent' -ErrorAction SilentlyContinue
    if ($svc -and $svc.Status -ne 'Stopped') {
      Stop-Service -Name 'AiopsMonitorAgent' -Force -ErrorAction SilentlyContinue
    }
  } catch {}
  try { & "$env:SystemRoot\System32\sc.exe" stop AiopsMonitorAgent 1>$null 2>$null | Out-Null } catch {}
}
Remove-AiopsScheduledTask 'AIOpsAgent'
Remove-AiopsScheduledTask 'AIOps-Agent'

# Step 2b: Stop + remove the Windows service (elevated service installs).
# Stop FIRST (clean stop won't trigger crash-recovery), then delete. Deleting
# needs admin; without it the service keeps the exe locked and restarts the host.
Stop-AiopsServiceQuiet
Start-Sleep -Milliseconds 1200
try { & "$env:SystemRoot\System32\sc.exe" delete AiopsMonitorAgent 1>$null 2>$null | Out-Null } catch {}

# Step 3: Kill related processes via PowerShell only — taskkill.exe is also
# frequently GPO-blocked on the same hosts that block cmd.exe / curl.exe.
Get-Process aiops-agent -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Get-Process wscript -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

# Step 4: Wait for process handles to release (increased to 3s)
Start-Sleep -Seconds 3

# Step 4b: Sweep every other user profile (see Remove-AiopsAllUserInstalls).
Remove-AiopsAllUserInstalls
# The pre-anchoring SYSTEM service wrote its identity into the SCM working dir.
Remove-Item -LiteralPath (Join-Path $env:SystemRoot 'System32\agent_state.json') -Force -ErrorAction SilentlyContinue

# Step 5: Delete files with retry logic (handles stubborn file locks), for BOTH
# install locations. Delete VBS files FIRST -- removing them prevents wscript.exe
# from being relaunched by the Run registry.
$files = @("start-agent.vbs", "start-relay.vbs", "aiops-agent.exe", "config.yaml", "config.json", "agent_state.json", "agent.log", "agent.log.1", "agent.log.2", "agent.log.3", "agent.log.4", "agent.log.5", "agent.log.6", "agent-desktop.log", "agent-desktop.log.1", "agent-desktop.log.2", "agent-desktop.log.3", "agent-desktop.log.4", "agent-desktop.log.5", "agent-desktop.log.6", "plugins.zip")
foreach ($Dir in $Dirs) {
    foreach ($f in $files) {
        $path = Join-Path $Dir $f
        if (Test-Path $path) { Remove-Item $path -Force -ErrorAction SilentlyContinue }
    }
    $pluginsDir = Join-Path $Dir "plugins"
    if (Test-Path $pluginsDir) { Remove-Item -Recurse -Force $pluginsDir -ErrorAction SilentlyContinue }
    for ($i = 2; $i -le 8; $i *= 2) {
        if (Test-Path $Dir) { Remove-Item -Recurse -Force $Dir -ErrorAction SilentlyContinue }
        if (-not (Test-Path $Dir)) { break }
        Start-Sleep -Seconds $i
    }
}

# Last resort -- schedule deletion of any still-locked dir on next reboot.
$stuck = @($Dirs | Where-Object { Test-Path $_ })
if ($stuck.Count -gt 0) {
    Write-Host "[AIOps] scheduling cleanup on next reboot for: $($stuck -join '; ')"
    $bat = Join-Path $env:TEMP "aiops-uninstall.bat"
    $sb = New-Object System.Text.StringBuilder
    [void]$sb.AppendLine("@echo off")
    [void]$sb.AppendLine(":retry")
    [void]$sb.AppendLine("timeout /t 5 /nobreak >nul")
    foreach ($d in $stuck) { [void]$sb.AppendLine('rmdir /s /q "' + $d + '" 2>nul') }
    foreach ($d in $stuck) { [void]$sb.AppendLine('if exist "' + $d + '" goto retry') }
    [void]$sb.AppendLine('del "%~f0" 2>nul')
    [System.IO.File]::WriteAllText($bat, $sb.ToString(), (New-Object System.Text.ASCIIEncoding))
    New-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\RunOnce" -Name "AIOpsCleanup" -Value ("cmd.exe /c " + $bat) -PropertyType String -Force | Out-Null
    Write-Host "[AIOps] warning: some files could not be deleted. Cleanup will finish on next reboot."
} else {
    Write-Host "[AIOps] uninstalled. You may delete the host card in the dashboard."
}
`
