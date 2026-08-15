package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode/utf16"
)

// buildLegacyAgentUpdateCommand returns a one-shot shell/PowerShell command that
// downloads /dl/$bin, verifies SHA-256, replaces the running binary, and restarts
// the agent service — without wiping config (unlike a full reinstall).
//
// sha is the server-computed digest of that artifact (see agentDistSHA256). It is
// optional — an empty value simply means the script falls back to fetching
// /dl/$bin.sha256 and must then insist on a valid server certificate.
func buildLegacyAgentUpdateCommand(goos, serverURL, bin, sha string, force bool) string {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	bin = strings.TrimSpace(bin)
	if serverURL == "" || bin == "" {
		return ""
	}
	_ = force
	switch strings.ToLower(goos) {
	case "linux", "darwin":
		return legacyUnixAgentUpdateScript(serverURL, bin, sha, goos == "darwin")
	case "windows":
		return legacyWindowsAgentUpdateScript(serverURL, bin, sha)
	default:
		return ""
	}
}

// sanitizeSHA256Hex returns v as lowercase hex only when it is a well-formed
// SHA-256 digest, and "" otherwise. Anything that reaches a generated script must
// be shell/PowerShell-inert: this value is interpolated into a single-quoted
// PowerShell literal and a shell variable, and it also decides whether the script
// is allowed to relax certificate validation — so a malformed digest has to
// degrade to "unpinned", never to "pinned to something unmatchable" (which would
// fail every update) and never to injectable text.
func sanitizeSHA256Hex(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) != 64 {
		return ""
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return v
}

// legacyRestartHelperSh is the body of the *detached* restart helper written by
// the legacy update script. It must never run inline: this script is executed
// through the agent's exec channel, i.e. as a child inside the agent's own
// systemd unit cgroup — and `systemctl stop|restart` tears that whole cgroup
// down, killing the restarter halfway. That is precisely how a fleet-wide
// auto-update leaves every host with a stopped, never-restarted agent.
//
// The helper therefore (1) moves itself out of the agent cgroup, (2) heals the
// unit without stopping it, (3) issues a single restart job, and (4) watches the
// agent back up, rolling back to aiops-agent.bak when it does not return.
const legacyRestartHelperSh = `#!/bin/sh
DIR="$1"
CFG="$2"
EXE="$DIR/aiops-agent"
BAK="$DIR/aiops-agent.bak"
LOG=/var/log/aiops-agent-update.log
( : >> "$LOG" ) 2>/dev/null || LOG=/tmp/aiops-agent-update.log
ulog() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG" 2>/dev/null || true; }

escape_cgroup() {
  [ "$(id -u)" -eq 0 ] || return 0
  if [ -w /sys/fs/cgroup/cgroup.procs ]; then
    echo $$ > /sys/fs/cgroup/cgroup.procs 2>/dev/null && return 0
  fi
  for c in /sys/fs/cgroup/systemd /sys/fs/cgroup/name=systemd; do
    if [ -w "$c/cgroup.procs" ]; then
      echo $$ > "$c/cgroup.procs" 2>/dev/null && return 0
    fi
  done
  return 1
}
escape_cgroup || true
sleep 2

host_run() {
  if [ "$(id -u)" -eq 0 ] && command -v nsenter >/dev/null 2>&1 && [ -e /proc/1/ns/mnt ]; then
    nsenter -t 1 -m -u -i -n -- "$@"
  else
    "$@"
  fi
}
# agent_proc_alive: only a *daemon* agent counts. The remote-desktop worker is
# the same binary with the same process name in its own session, and a restart
# SIGKILLs the daemon before its cleanup runs — a bare pgrep would see the stale
# worker, fake a healthy upgrade and suppress the rollback below.
agent_proc_alive() {
  for p in $(pgrep -x aiops-agent 2>/dev/null) $(pgrep -f '[/]aiops-agent( |$)' 2>/dev/null); do
    case "$(ps -o args= -p "$p" 2>/dev/null)" in
      *--desktop-worker*) continue ;;
      "") continue ;;
    esac
    return 0
  done
  return 1
}
agent_alive() {
  for u in aiops-agent aiops-monitor-agent; do
    systemctl is-active --quiet "$u" 2>/dev/null && return 0
  done
  agent_proc_alive
}
wait_alive() {
  waited=0
  while [ "$waited" -lt 60 ]; do
    if agent_alive; then
      sleep 8
      agent_alive && return 0
    fi
    sleep 3
    waited=$((waited + 3))
  done
  return 1
}
unit_file_exists() {
  for base in /etc/systemd/system /run/systemd/system /lib/systemd/system /usr/lib/systemd/system; do
    for u in aiops-agent aiops-monitor-agent; do
      [ -f "$base/${u}.service" ] && return 0
    done
  done
  return 1
}
start_units() {
  systemctl daemon-reload 2>/dev/null || true
  systemctl restart aiops-agent 2>/dev/null && return 0
  systemctl restart aiops-monitor-agent 2>/dev/null && return 0
  return 1
}
relaunch() {
  pkill -x aiops-agent 2>/dev/null || pkill -f '[/]aiops-agent( |$)' 2>/dev/null || true
  sleep 1
  if [ -n "$CFG" ]; then
    nohup "$EXE" --config "$CFG" >/dev/null 2>&1 &
  else
    nohup "$EXE" >/dev/null 2>&1 &
  fi
  sleep 1
}

ulog "legacy update helper start: dir=$DIR cfg=$CFG"
RESTARTED=0
if command -v systemctl >/dev/null 2>&1 && [ "$(id -u)" -eq 0 ]; then
  # 只解锁单元文件，不 stop（stop 会连本助手一起杀）。
  host_run sh -c 'for u in aiops-agent aiops-monitor-agent; do
    rm -rf /etc/systemd/system/${u}.service.d /run/systemd/system/${u}.service.d 2>/dev/null || true
    f=/etc/systemd/system/${u}.service; [ -f "$f" ] || continue
    sed -i -e "s/^User=.*/User=root/" -e "s/^ProtectHome=.*/ProtectHome=false/" \
      -e "s/^ProtectSystem=.*/ProtectSystem=false/" -e "s/^PrivateTmp=.*/PrivateTmp=false/" \
      -e "s/^NoNewPrivileges=.*/NoNewPrivileges=false/" -e "/^CapabilityBoundingSet=/d" "$f" 2>/dev/null || true
    grep -q "^ProtectSystem=false" "$f" || echo "ProtectSystem=false" >> "$f"
    grep -q "^User=root" "$f" || echo "User=root" >> "$f"
  done; systemctl daemon-reload' 2>/dev/null || true
  # 没有任何单元时才做完整安装（--install-service 会 stop+删除单元，是破坏性的）。
  if ! unit_file_exists && [ -n "$CFG" ]; then
    host_run "$EXE" --install-service --config "$CFG" >/dev/null 2>&1 && RESTARTED=1
  fi
  if [ "$RESTARTED" -eq 0 ] && start_units; then
    RESTARTED=1
  fi
fi
[ "$RESTARTED" -eq 0 ] && relaunch

if wait_alive; then
  ulog "legacy update helper ok: agent running after restart"
  exit 0
fi
ulog "agent did not come back after legacy update — rolling back to $BAK"
if [ -f "$BAK" ]; then
  cp -f "$BAK" "$EXE" 2>/dev/null || true
  chmod +x "$EXE" 2>/dev/null || true
  start_units || relaunch
  if wait_alive; then
    ulog "rollback ok: previous agent binary restored and running"
    exit 1
  fi
fi
ulog "rollback failed: agent still not running"
exit 1
`

func legacyUnixAgentUpdateScript(server, bin, sha string, darwin bool) string {
	// Restart runs in a DETACHED helper (own cgroup / session) so a service stop
	// cannot kill it mid-swap; this script only stages the binary and hands off.
	// The server keeps the host in pending_verify until agent_version catches up,
	// so handing off before the restart completes does not fake success.
	restart := `
CFG=""
for c in "$DIR/config.yaml" "$DIR/config.yml" "$HOME/.aiops-agent/config.yaml"; do
  [ -f "$c" ] && CFG="$c" && break
done
HELPER="$DIR/.aiops-agent-restart.sh"
cat > "$HELPER" <<'AIOPS_RESTART_EOF'
` + legacyRestartHelperSh + `AIOPS_RESTART_EOF
chmod +x "$HELPER"
STARTED=0
if [ "$(id -u)" -eq 0 ] && command -v systemd-run >/dev/null 2>&1; then
  if systemd-run --quiet --collect --unit="aiops-agent-update-$$" \
      --description="AIOps Agent self-update helper" \
      --property=Type=oneshot --property=KillMode=process --property=TimeoutStartSec=600 \
      /bin/sh "$HELPER" "$DIR" "$CFG" >/dev/null 2>&1; then
    STARTED=1
  fi
fi
if [ "$STARTED" -eq 0 ]; then
  if command -v setsid >/dev/null 2>&1; then
    setsid /bin/sh "$HELPER" "$DIR" "$CFG" >/dev/null 2>&1 &
  else
    nohup /bin/sh "$HELPER" "$DIR" "$CFG" >/dev/null 2>&1 &
  fi
fi
`
	if darwin {
		// kickstart 优先：它是 launchd 内部的一次原子重启，即使本脚本随旧进程被杀，
		// job 也会被拉起来。--install-service 会先 bootout/unload 旧 job——那一步会
		// 带走作为其子进程的本脚本，剩下"卸载了但没装回去"的死机器，故仅在完全没有
		// job 可踢时才回退到它。
		restart = `
CFG=""
for c in "$DIR/config.yaml" "$DIR/config.yml" "$HOME/.aiops-agent/config.yaml"; do
  [ -f "$c" ] && CFG="$c" && break
done
UIDN=$(id -u)
# --install-service uses com.aiops.monitor.agent; the one-click installer uses
# com.aiops.agent (root LaunchDaemon or per-user LaunchAgent). Try all four.
AGENT_LABELS="system/com.aiops.monitor.agent system/com.aiops.agent gui/$UIDN/com.aiops.agent gui/$UIDN/com.aiops.monitor.agent"
xattr -dr com.apple.quarantine aiops-agent 2>/dev/null || true
RESTARTED=0
# The remote-desktop worker is the same binary with the same process name; a
# bare pgrep would mistake a stale worker for a healthy daemon.
agent_proc_alive() {
  for p in $(pgrep -x aiops-agent 2>/dev/null); do
    case "$(ps -o args= -p "$p" 2>/dev/null)" in
      *--desktop-worker*) continue ;;
      "") continue ;;
    esac
    return 0
  done
  return 1
}
for label in $AGENT_LABELS; do
  if launchctl kickstart -k "$label" 2>/dev/null; then RESTARTED=1; break; fi
done
if [ "$RESTARTED" -eq 0 ] && [ -n "$CFG" ]; then
  "$DIR/aiops-agent" --install-service --config "$CFG" >/dev/null 2>&1 || true
  for label in $AGENT_LABELS; do
    if launchctl kickstart -k "$label" 2>/dev/null; then RESTARTED=1; break; fi
  done
fi
if [ "$RESTARTED" -eq 0 ]; then
  pkill -x aiops-agent 2>/dev/null || true
  sleep 1
  if [ -n "$CFG" ]; then
    nohup "$DIR/aiops-agent" --config "$CFG" >/dev/null 2>&1 &
  else
    nohup "$DIR/aiops-agent" >/dev/null 2>&1 &
  fi
  sleep 2
  agent_proc_alive && RESTARTED=1
fi
if [ "$RESTARTED" -eq 0 ]; then
  echo "restart failed (launchctl/nohup)"; exit 1
fi
`
	}
	return fmt.Sprintf(`set -e
SERVER=%q
BIN=%q
PINNED=%q
DIR=""
for d in /opt/aiops-agent "$HOME/.aiops-agent" /usr/local/aiops-agent; do
  if [ -x "$d/aiops-agent" ]; then DIR="$d"; break; fi
done
if [ -z "$DIR" ]; then
  EXE=$(command -v aiops-agent 2>/dev/null || true)
  if [ -n "$EXE" ] && [ -x "$EXE" ]; then DIR=$(dirname "$EXE"); fi
fi
if [ -z "$DIR" ] || [ ! -x "$DIR/aiops-agent" ]; then
  echo "agent binary not found under known install dirs"; exit 1
fi
cd "$DIR"
NEW=".aiops-agent.new"
rm -f "$NEW"
# fetch: verify the server certificate first. The insecure retry exists only for
# the binary, and only when PINNED carries the server-computed digest: that digest
# travelled the agent's authenticated report channel, so it identifies the artifact
# independently of this transport. A digest fetched from $BIN.sha256 travels the
# same connection as the binary and proves nothing against an on-path attacker, so
# an unpinned fetch never gets the retry. Old distros with an expired DST Root CA X3
# (or a private/enterprise CA, or a TLS-inspecting proxy) are the reason it exists.
#
# Every conditional below is written as an "if", never as "cmd && return": under
# set -e an AND-OR list whose left side fails takes the whole script down, which
# would skip the very fallback this function exists for.
fetch() {
  url="$1"; out="$2"; allow_insecure="$3"
  if command -v curl >/dev/null 2>&1; then
    if curl -fSL --retry 3 -o "$out" "$url"; then return 0; fi
    if [ "$allow_insecure" != "1" ]; then return 1; fi
    echo "warning: TLS verification failed for $url; retrying without it (payload pinned to sha256=$PINNED)" >&2
    if curl -fSLk --retry 2 -o "$out" "$url"; then return 0; fi
    return 1
  fi
  if command -v wget >/dev/null 2>&1; then
    if wget -q -O "$out" "$url"; then return 0; fi
    if [ "$allow_insecure" != "1" ]; then return 1; fi
    echo "warning: TLS verification failed for $url; retrying without it (payload pinned to sha256=$PINNED)" >&2
    if wget -q --no-check-certificate -O "$out" "$url"; then return 0; fi
    return 1
  fi
  echo "curl/wget required" >&2
  return 1
}
ALLOW=0
if [ -n "$PINNED" ]; then ALLOW=1; fi
if ! fetch "$SERVER/dl/$BIN" "$NEW" "$ALLOW"; then
  echo "download failed: $SERVER/dl/$BIN (check the server certificate chain on this host, or the agent's ca_cert setting)"
  rm -f "$NEW"; exit 1
fi
EXPECTED="$PINNED"
if [ -z "$EXPECTED" ]; then
  if ! fetch "$SERVER/dl/$BIN.sha256" ".aiops-agent.sha256" 0; then
    echo "checksum download failed: $SERVER/dl/$BIN.sha256"
    rm -f "$NEW"; exit 1
  fi
  EXPECTED=$(awk '{print $1}' .aiops-agent.sha256 | tr 'A-F' 'a-f')
fi
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$NEW" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$NEW" | awk '{print $1}')
else
  echo "sha256sum/shasum required"; exit 1
fi
if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "SHA-256 mismatch"; rm -f "$NEW"; exit 1
fi
cp -f aiops-agent aiops-agent.bak 2>/dev/null || true
mv -f "$NEW" aiops-agent
chmod +x aiops-agent
%s
echo "legacy agent update ok sha=$ACTUAL"
`, server, bin, sanitizeSHA256Hex(sha), restart)
}

// windowsUpdateHelperPS is the COMPLETE Windows update helper: it locates the
// installed agent, downloads /dl/$Bin, verifies SHA-256, probes the staged
// binary, stops the service, swaps the locked PE, restarts the agent and rolls
// back to .bak on failure.
//
// 它不经命令行下发，而是由服务端在 /dl/aiops-agent-update.ps1 上以文件形式提供，
// 由一段极小的引导脚本下载 + 校验 SHA-256 后落盘执行。原因见
// legacyWindowsAgentUpdateScript 的注释——命令行装不下它。
//
// 两条硬性约束：
//
//  1. **必须分离执行**。引导脚本经 Agent 的 exec 通道执行，是 Agent 进程的子进程，
//     因而落在服务的 Job Object 里。`sc.exe stop` 一旦停掉服务，Job 会把这个子进程
//     一起杀掉——换二进制、重启服务全都来不及跑，主机就停在「服务已停止」。所以本
//     脚本里的每一步都只在 schtasks(SYSTEM) / WMI 拉起的独立进程里跑。
//  2. **必须是纯 ASCII**。Windows PowerShell 5.1 用系统 ANSI 代码页读取无 BOM 的
//     .ps1。服务端下发的正文原样落盘（引导脚本不改写它，才能校验 SHA-256），所以
//     正文里出现非 ASCII 字符就会在 GBK/Latin-1 机器上解析错乱。中文说明一律留在
//     Go 注释里。
//
// $Server / $Bin / $Sha 由引导脚本作为命令行参数传入；其余路径本脚本自行推导。$Sha 是
// 服务端算出的产物摘要，见下面 Get-Payload 的注释——它是证书链断裂时仍能安全升级的前提。
//
// 第三条隐性约束，踩过一次：**本脚本绝不能终止 AIOpsAgentLegacyUpdate 这个计划任务**。
// 它自己就是该任务的运行实例，`schtasks /End` 会连整个任务进程树一起终止——而历史上
// 那行调用恰好落在停服务、换二进制之前，于是每一次救援都死在那里：主机停在旧版本，
// 助手日志停在 "staging --version"，与「Windows 无法自动升级」的症状完全一致。清理上
// 一轮吊死的实例只能由引导脚本在 Start-ScheduledTask 之前做。
const windowsUpdateHelperPS = `param([string]$Server,[string]$Bin,[string]$Sha)
$ErrorActionPreference='Stop'
$helperPid = $PID
$Work = Split-Path -Parent $PSCommandPath
$Log = Join-Path $Work 'aiops-agent-update.log'
function Write-Log($m){ try{ Add-Content -LiteralPath $Log -Value ("[{0}] {1}" -f (Get-Date -Format o), $m) -Encoding UTF8 }catch{} }
# Panels published on a real domain are served over HTTPS, so every download in
# this helper is a TLS handshake that has to succeed on hosts nobody has touched
# in years. Two things break there and neither is a server problem:
#   * TLS 1.2 is not in the default SecurityProtocol of .NET < 4.7 (the numeric
#     3072 is used because the Tls12 enum member does not exist on 4.0), and
#     TLS 1.3 (12288) throws on anything older than 4.8 -- so each flag is set in
#     its own try, or one unsupported value would discard the other.
#   * Server 2012 / 2008 R2 root stores predate ISRG Root X1 and friends, so a
#     perfectly valid Let's Encrypt chain is untrusted here until Windows Update
#     ships a root refresh. Private/enterprise CAs and TLS-inspecting proxies
#     land in the same place.
function Enable-ModernTls {
  foreach($v in @(3072,12288)){
    try{ [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor $v }catch{}
  }
}
# Download with certificate validation ON, and fall back to an unvalidated retry
# ONLY when the caller holds an out-of-band SHA-256 pin for the payload.
#
# The pin arrives inside this script's arguments, which came from the bootstrap,
# which the server generated and delivered over the agent's own authenticated,
# certificate-verified report channel. So it describes the artifact independently
# of whatever the download connection is. Fetching '<bin>.sha256' over the SAME
# connection proves nothing against an on-path attacker -- both halves would be
# swapped together -- which is why an unpinned download never gets the retry: the
# payload is a binary that will run as LocalSystem on every host in the fleet.
function Get-Payload {
  param([string]$Url,[string]$Dest,[string]$Pin)
  $err = $null
  try {
    Remove-Item -LiteralPath $Dest -Force -ErrorAction SilentlyContinue
    (New-Object Net.WebClient).DownloadFile($Url, $Dest)
    return
  } catch { $err = $_.Exception }
  Write-Log ("strict TLS download failed for " + $Url + ": " + $err.Message)
  if(-not $Pin){
    throw ("download failed for " + $Url + ": " + $err.Message +
      " (no server-pinned SHA-256 for this artifact, so retrying without certificate validation would be unsafe;" +
      " install the CA chain on this host, or point the agent at a server whose certificate it trusts)")
  }
  Write-Log ("retrying without certificate validation -- the payload is pinned to sha256=" + $Pin + ", so its integrity does not depend on the transport")
  $prev = [Net.ServicePointManager]::ServerCertificateValidationCallback
  try {
    [Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }
    Remove-Item -LiteralPath $Dest -Force -ErrorAction SilentlyContinue
    (New-Object Net.WebClient).DownloadFile($Url, $Dest)
  } finally {
    [Net.ServicePointManager]::ServerCertificateValidationCallback = $prev
  }
}
function Invoke-Native {
  param([string]$File,[string[]]$Arguments)
  # Never merge native stderr via 2>&1: Windows PowerShell 5.1 turns it into
  # NativeCommandError records that $ErrorActionPreference='Stop' promotes to a
  # terminating error. Judge by exit code instead.
  #
  # The parameter must NOT be named $Args: that is a PowerShell automatic
  # variable, and declaring it as a parameter silently clears it on every call,
  # so "& $File @Args" degenerates into running the target with no arguments.
  $prevEAP = $ErrorActionPreference
  $out = ''
  $code = -1
  try {
    $ErrorActionPreference = 'Continue'
    $out = (& $File @Arguments 2>$null | Out-String)
    $code = $LASTEXITCODE
  } catch { $out = $_.Exception.Message; $code = -1 } finally { $ErrorActionPreference = $prevEAP }
  return [pscustomobject]@{ ExitCode = $code; Output = $out.Trim() }
}
# Running an agent binary's --version needs a hard timeout: the probe target can
# turn into a daemon at any moment, and an unbounded pipe read would hang the
# helper forever right before the swap, leaving the host silently on the old
# version with nothing to report.
function Invoke-VersionProbe {
  param([string]$File,[int]$TimeoutSec = 20)
  $o = [IO.Path]::GetTempFileName()
  $e = [IO.Path]::GetTempFileName()
  try {
    $p = Start-Process -FilePath $File -ArgumentList '--version' -NoNewWindow -PassThru -RedirectStandardOutput $o -RedirectStandardError $e
    if (-not $p.WaitForExit($TimeoutSec * 1000)) {
      try { $p.Kill() } catch {}
      return [pscustomobject]@{ ExitCode = -1; Output = ("version probe timed out after " + $TimeoutSec + "s") }
    }
    $p.WaitForExit()
    $txt = '' + (Get-Content -LiteralPath $o -Raw -ErrorAction SilentlyContinue) + (Get-Content -LiteralPath $e -Raw -ErrorAction SilentlyContinue)
    return [pscustomobject]@{ ExitCode = $p.ExitCode; Output = $txt.Trim() }
  } catch {
    return [pscustomobject]@{ ExitCode = -1; Output = $_.Exception.Message }
  } finally {
    Remove-Item -Force -ErrorAction SilentlyContinue $o, $e
  }
}
$procNames=@('aiops-agent','aiops-agent-windows-amd64','aiops-agent-windows-arm64','aiops-agent-windows-amd64-win2012')
$svcNames=@('AiopsMonitorAgent','AIOps-Agent','AIOpsAgent')
$exeNames=@('aiops-agent.exe','aiops-agent-windows-amd64.exe','aiops-agent-windows-arm64.exe','aiops-agent-windows-amd64-win2012.exe')
# The service ImagePath is the authoritative description of the install: it
# carries BOTH the binary path and the --config the service was registered with
# (installAgentService writes '"<exe>" --service --config "<abs>"'). Read it once
# and derive everything from it; a directory guess cannot find installs outside
# the standard locations, and under a SYSTEM scheduled task $env:LOCALAPPDATA
# points at systemprofile, so a per-user install would never be found that way.
function Get-AgentServiceCommandLine {
  foreach($n in $svcNames){
    try{
      $svc = Get-CimInstance Win32_Service -Filter ("Name='" + $n + "'") -ErrorAction SilentlyContinue
      if($svc -and $svc.PathName){ return $svc.PathName.Trim() }
    }catch{}
  }
  return $null
}
function Get-ExeFromCommandLine {
  param([string]$Line)
  if(-not $Line){ return $null }
  $p = $Line.Trim()
  if($p.StartsWith('"')){
    $end = $p.IndexOf('"',1)
    if($end -gt 1){ $p = $p.Substring(1, $end-1) }
  } else {
    $ix = $p.ToLowerInvariant().IndexOf('.exe')
    if($ix -gt 0){ $p = $p.Substring(0, $ix+4) }
  }
  if($p -and (Test-Path -LiteralPath $p)){ return $p }
  return $null
}
# The config does NOT have to sit beside the exe: --install-service embeds
# whatever absolute path it was given. Guessing "config.yaml next to the binary"
# misses those installs, and a missing config used to make the restart refuse to
# run at all -- leaving the host with a swapped binary and a stopped service.
function Get-ConfigFromCommandLine {
  param([string]$Line)
  if(-not $Line){ return $null }
  $m = [regex]::Match($Line, '--config\s+(?:"([^"]+)"|([^\s"]+))')
  if(-not $m.Success){ return $null }
  $c = $m.Groups[1].Value
  if(-not $c){ $c = $m.Groups[2].Value }
  if($c -and (Test-Path -LiteralPath $c)){ return $c }
  return $null
}
function Resolve-AgentExe {
  $p = Get-ExeFromCommandLine (Get-AgentServiceCommandLine)
  if($p){ return $p }
  $dirs=@()
  foreach($d in @($env:ProgramFiles, ${env:ProgramFiles(x86)}, $env:ProgramData, $env:LOCALAPPDATA)){
    if($d){ $dirs += (Join-Path $d 'AIOps Agent'); $dirs += (Join-Path $d 'aiops-agent') }
  }
  $dirs += 'C:\aiops-agent'
  foreach($d in $dirs){
    foreach($n in $exeNames){
      $p = Join-Path $d $n
      if(Test-Path -LiteralPath $p){ return $p }
    }
  }
  return $null
}
function Stop-AgentProcesses {
  Get-Process -Name $procNames -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne $helperPid } | Stop-Process -Force -ErrorAction SilentlyContinue
  # Also kill wild agents launched straight from a staging file: their process
  # name looks like ".aiops-agent.new.<pid>" and does not match the list above,
  # but the image path always contains aiops-agent. These are wreckage from the
  # broken helper generation ("& $new" with no arguments ran the freshly
  # downloaded agent as a foreground daemon that never exits).
  Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object {
      $_.ProcessId -ne $helperPid -and $_.ExecutablePath -and
      $_.ExecutablePath -match 'aiops-agent' -and
      (-not ($_.CommandLine -and $_.CommandLine -match 'aiops-agent-(update-helper|legacy-update|update)'))
    } |
    ForEach-Object { try { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue } catch {} }
}
# Older agent-generated helpers registered themselves as the one-shot
# AIOpsAgentSelfUpdate task and then hung on the --version probe, so the task
# never stops. Clear that task and any stray helper process before taking over.
#
# NEVER end AIOpsAgentLegacyUpdate here: this script IS that task's running
# instance, so schtasks /End would terminate its own process tree. Clearing a
# stale instance belongs in the bootstrap, before the task is started.
function Clear-StuckSelfUpdateTask {
  try { [void](Invoke-Native "$env:SystemRoot\System32\schtasks.exe" @('/End','/TN','AIOpsAgentSelfUpdate')) } catch {}
  try { [void](Invoke-Native "$env:SystemRoot\System32\schtasks.exe" @('/Delete','/TN','AIOpsAgentSelfUpdate','/F')) } catch {}
  try {
    Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
      Where-Object { $_.CommandLine -and $_.CommandLine -match 'aiops-agent-update(-helper)?\.ps1' -and $_.ProcessId -ne $helperPid } |
      ForEach-Object { try { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue } catch {} }
  } catch {}
}
# A leftover --desktop-worker is the same binary with the same process name; it
# must not read as a healthy agent or a failed swap would never roll back.
function Test-Running {
  foreach($n in $svcNames){ $s=Get-Service $n -ErrorAction SilentlyContinue; if($s -and $s.Status -eq 'Running'){ return $true } }
  $procs = @(Get-Process -Name $procNames -ErrorAction SilentlyContinue)
  if($procs.Count -eq 0){ return $false }
  $all = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue)
  if($all.Count -eq 0){ return $true }
  $daemon = $all | Where-Object {
    $_.Name -match '^aiops-agent' -and $_.ProcessId -ne $helperPid -and
    (-not ($_.CommandLine -and $_.CommandLine -match '--desktop-worker'))
  } | Select-Object -First 1
  return ($null -ne $daemon)
}
function Restart-Agent {
  $ok=$false
  $svcs=@()
  foreach($n in $svcNames){ if(Get-Service $n -ErrorAction SilentlyContinue){ $svcs += $n } }
  $hasSvc = ($svcs.Count -gt 0)
  Write-Log ("restart path hasService=$hasSvc cfg=$Cfg")
  if($hasSvc -and $Cfg){
    $p=Start-Process -FilePath $Exe -ArgumentList @('--install-service','--config',$Cfg) -WorkingDirectory $Dir -Wait -PassThru -WindowStyle Hidden
    if($p -and $p.ExitCode -eq 0){ $ok=$true }
  }
  # A registered service already carries '--service --config <abs>' in its
  # ImagePath, so plain start is the correct recovery -- including when no config
  # could be resolved at all. Gating this on $Cfg is how a host ended up with a
  # freshly swapped binary, a stopped service and no way back online.
  if(-not $ok -and $svcs.Count -gt 0){
    foreach($n in $svcs){
      [void](Invoke-Native "$env:SystemRoot\System32\sc.exe" @('start',$n))
      try{ Start-Service -Name $n -ErrorAction SilentlyContinue }catch{}
      for($i=0;$i -lt 30;$i++){
        $s=Get-Service $n -ErrorAction SilentlyContinue
        if($s -and $s.Status -eq 'Running'){ $ok=$true; break }
        Start-Sleep -Seconds 1
      }
      if($ok){ Write-Log ("service started: " + $n); break }
    }
  }
  if(-not $ok){
    $vbs=Join-Path $Dir 'start-agent.vbs'
    if(Test-Path -LiteralPath $vbs){
      Start-Process wscript.exe -ArgumentList ('"'+$vbs+'"') -WorkingDirectory $Dir -WindowStyle Hidden
    } else {
      foreach($tn in @('AIOpsAgent','AIOpsAgentKeepalive','AIOps Agent')){
        [void](Invoke-Native "$env:SystemRoot\System32\schtasks.exe" @('/Run','/TN',$tn))
      }
      if($Cfg){ Start-Process -FilePath $Exe -ArgumentList @('--config',$Cfg) -WorkingDirectory $Dir -WindowStyle Hidden }
      else { Write-Log 'FATAL: no service and no config beside exe; refusing bare Start-Process'; return $false }
    }
  }
  for($i=0;$i -lt 20;$i++){ if(Test-Running){ return $true }; Start-Sleep -Seconds 2 }
  return $false
}
function Write-Result($m){
  foreach($p in @((Join-Path $Work 'aiops-agent-update.result'), (Join-Path $Dir 'aiops-agent-update.result'))){
    if(-not $p){ continue }
    try{ Set-Content -LiteralPath $p -Value $m -Encoding UTF8 }catch{}
  }
}
$SvcCmd = Get-AgentServiceCommandLine
$Exe = Resolve-AgentExe
if(-not $Exe){ Write-Log 'FATAL: agent exe not found (service ImagePath and known dirs)'; exit 1 }
$Dir = Split-Path -Parent $Exe
$Bak = $Exe + '.bak'
$New = Join-Path $Dir '.aiops-agent.update.exe'
$Cfg = Get-ConfigFromCommandLine $SvcCmd
if(-not $Cfg){
  foreach($n in @('config.yaml','config.yml','config.json')){ $c=Join-Path $Dir $n; if(Test-Path -LiteralPath $c){ $Cfg=$c; break } }
}
$swapped = $false
try {
  Write-Log ("helper start pid=$helperPid exe=$Exe cfg=$Cfg bin=$Bin")
  Write-Result ("running " + (Get-Date -Format o))
  # Give the exec channel time to hand the bootstrap's output back to the server
  # before the agent that carries that channel is stopped.
  Start-Sleep -Seconds 3
  Enable-ModernTls
  # Keep only what a hex digest can contain, so a malformed argument degrades to
  # "unpinned" (strict TLS, digest fetched over the wire) instead of pinning the
  # download to a value nothing can ever match.
  $Pin = ''
  if($Sha){ $Pin = ((($Sha -replace '[^0-9a-fA-F]','')).ToLowerInvariant()) }
  if($Pin.Length -ne 64){ $Pin = '' }
  if($Pin){ Write-Log ("server-pinned sha256=" + $Pin) } else { Write-Log 'no server-pinned sha256; download must validate the server certificate' }
  Get-Payload "$Server/dl/$Bin" $New $Pin
  $Expected = $Pin
  if(-not $Expected){
    $Expected = ((New-Object Net.WebClient).DownloadString("$Server/dl/$Bin.sha256") -split '\s+')[0].Trim().ToLowerInvariant()
  }
  $Hasher=[Security.Cryptography.SHA256]::Create(); $Stream=[IO.File]::OpenRead($New)
  try{ $Actual=([BitConverter]::ToString($Hasher.ComputeHash($Stream))).Replace('-','').ToLowerInvariant() } finally { $Stream.Dispose(); $Hasher.Dispose() }
  if(-not $Expected -or $Expected -ne $Actual){ Remove-Item $New -Force -ErrorAction SilentlyContinue; throw "SHA-256 mismatch (want $Expected got $Actual)" }
  Write-Log ("downloaded $Bin sha=$Actual")
  $probe = Invoke-VersionProbe $New
  if($probe.ExitCode -ne 0){ Remove-Item $New -Force -ErrorAction SilentlyContinue; throw ("staging not runnable (exit="+$probe.ExitCode+"): "+$probe.Output) }
  Write-Log ("staging --version: " + $probe.Output)
  Clear-StuckSelfUpdateTask
  foreach($name in $svcNames){
    if(Get-Service $name -ErrorAction SilentlyContinue){
      [void](Invoke-Native "$env:SystemRoot\System32\sc.exe" @('stop',$name))
      Stop-Service -Name $name -Force -ErrorAction SilentlyContinue
      for($i=0;$i -lt 40;$i++){ $s=Get-Service $name -ErrorAction SilentlyContinue; if($s -and $s.Status -eq 'Stopped'){ break }; Start-Sleep -Seconds 1 }
    }
  }
  Stop-AgentProcesses
  Start-Sleep -Seconds 1
  Stop-AgentProcesses
  if(Test-Path -LiteralPath $Exe){ Copy-Item -LiteralPath $Exe -Destination $Bak -Force -ErrorAction SilentlyContinue }
  $moved=$false
  for($i=0;$i -lt 15;$i++){
    try{ Move-Item -Force -LiteralPath $New -Destination $Exe; $moved=$true; break }catch{
      Write-Log ("Move-Item attempt " + ($i+1) + " failed: " + $_.Exception.Message)
      try{ Copy-Item -Force -LiteralPath $New -Destination $Exe; Remove-Item -Force -LiteralPath $New -ErrorAction SilentlyContinue; $moved=$true; break }catch{}
      Start-Sleep -Seconds 1
      Stop-AgentProcesses
    }
  }
  if(-not $moved){ throw 'replace binary failed (file lock)' }
  $swapped = $true
  try{ Unblock-File -Path $Exe -ErrorAction SilentlyContinue }catch{}
  if(-not (Restart-Agent)){ throw 'agent not running after update' }
  $ver = (Invoke-VersionProbe $Exe).Output
  Write-Log ("update ok version=" + $ver)
  Write-Result ("ok " + (Get-Date -Format o) + " version=" + $ver)
  exit 0
} catch {
  Write-Log ("update failed: " + $_.Exception.Message)
  Write-Result ("fail " + $_.Exception.Message)
  try {
    if((Test-Path -LiteralPath $Bak) -and ($swapped -or -not (Test-Path -LiteralPath $Exe))){
      Write-Log 'rolling back to .bak'
      Copy-Item -Force -LiteralPath $Bak -Destination $Exe
    }
    # Only restart what we actually disturbed. Most failures here happen BEFORE
    # the service is touched -- an unreachable server, an untrusted certificate,
    # a checksum mismatch, a staging binary that will not run -- and in those the
    # agent is still up and healthy. Restarting anyway means every failed attempt
    # tears down a working service and reinstalls it, which turns a harmless
    # "could not download" into an outage whenever --install-service then fails
    # (no admin rights, locked SCM). Restart only when the swap happened or the
    # agent is genuinely not running.
    if($swapped -or -not (Test-Running)){
      [void](Restart-Agent)
    } else {
      Write-Log 'agent still running and nothing was swapped; leaving it alone'
    }
  } catch {}
  exit 1
}
`

// windowsUpdateHelperPath is the single source of truth for the helper URL: the
// route registration, the bootstrap's download line and the tests all read it,
// so a rename cannot silently leave the bootstrap fetching a 404.
const windowsUpdateHelperPath = "/dl/aiops-agent-update.ps1"

// windowsUpdateHelperScript returns the exact bytes served at
// windowsUpdateHelperPath. The bootstrap verifies this content against
// windowsUpdateHelperSHA256 before executing it, so the two must never be
// derived from different strings.
func windowsUpdateHelperScript() string { return windowsUpdateHelperPS }

var windowsUpdateHelperSHAOnce = sync.OnceValue(func() string {
	sum := sha256.Sum256([]byte(windowsUpdateHelperScript()))
	return hex.EncodeToString(sum[:])
})

// windowsUpdateHelperSHA256 is the hex digest the bootstrap pins.
func windowsUpdateHelperSHA256() string { return windowsUpdateHelperSHAOnce() }

// handleAgentUpdateHelperScript serves the Windows update helper. It sits under
// /dl/ so it inherits the same unauthenticated, agent-reachable path as the
// binaries it installs (isPublicPath / CSRF / gzip already treat /dl/ that way),
// and it is generated rather than read from distDir so a deployment cannot end
// up with a stale copy on disk. The content carries no secrets: server URL and
// artifact name are supplied by the bootstrap at run time.
func (s *Server) handleAgentUpdateHelperScript(w http.ResponseWriter, r *http.Request) {
	body := windowsUpdateHelperScript()
	w.Header().Set("Content-Type", "text/plain; charset=us-ascii")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", `"`+windowsUpdateHelperSHA256()+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, windowsUpdateHelperSHA256()) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.WriteString(w, body)
}

// windowsUpdateBootstrapMaxLen caps the generated exec command.
//
// 这是本文件里最重要的一个常量。Agent 在 Windows 上执行 exec 命令的方式是
// `cmd.exe /c "<整条命令>"`（cmd/agent/terminal.go:runShellCommandCtx），而
// **cmd.exe 的命令行硬上限是 8191 个字符**，CreateProcessW 的上限是 32767。
// 历史上这条 legacy 更新命令把整段 PowerShell 内联成 -EncodedCommand，长度
// 37,171 字符——既超 cmd.exe 上限 4.5 倍，也超 CreateProcessW 上限，于是
// **每一次 Windows 兜底升级/救援都在进程创建阶段就失败了**，而这条路径恰恰是
// Agent 侧助手出问题时唯一的逃生口（助手脚本由 Agent 自己的代码生成，坏了就
// 修不了）。主路径和兜底路径同时失效，Windows 机群因此永久停在旧版本。
//
// 预算：8191 − Agent 包的 PATH/chcp 前缀（约 190）− 安全余量。留 6000 是为了
// 让后续往引导脚本里加逻辑时先撞上测试，而不是先撞上现网。
const windowsUpdateBootstrapMaxLen = 6000

// legacyWindowsAgentUpdateScript returns the command sent over the agent exec
// channel. It is only a BOOTSTRAP: download the helper from the server, verify
// its SHA-256 against the digest pinned here, drop it next to a generated
// assignment header, and hand it to a detached SYSTEM process.
//
// 为什么必须是引导而不是整段脚本：见 windowsUpdateBootstrapMaxLen。
// 为什么整段脚本要放在服务端：Windows 升级助手一旦有致命缺陷，装在现网的 Agent
// 会一遍遍生成同一段坏脚本，新版本的修复永远送不到它们手上——因为「装上新版本」
// 这件事本身就要靠那段坏脚本。把正文交给服务端下发，服务端一升级就能单方面修好
// 所有老 Agent（见 rescueWindowsAgentUpdate）。
func legacyWindowsAgentUpdateScript(server, bin, sha string) string {
	// The bootstrap runs INLINE, inside the agent's service Job Object: stopping
	// the service here would kill this very process mid-swap. It therefore only
	// downloads + verifies + spawns, and never touches the service or the binary.
	//
	// 它还负责一件助手自己做不了的事：`schtasks /End /TN AIOpsAgentLegacyUpdate`。
	// 助手正是以该任务实例的身份运行的，在助手内部调用 /End 等于自杀（历史上就死在
	// 换二进制之前）；而计划任务默认 MultipleInstances=IgnoreNew，上一轮吊死的实例不
	// 清掉，Start-ScheduledTask 会静默地什么都不做。所以陈旧实例只能在这里、在拉起新
	// 实例之前清理。
	//
	// 长度：本文经 base64(UTF-16LE) 膨胀 8/3 倍后要塞进 cmd.exe 的 8191 字符硬上限，所以
	// 这里的写法有意压缩过——`$R` 复用三处 System32 路径、`"$W\$N.ps1"` 代替 Join-Path、
	// 助手的启动参数用 `-nop -noni -ep`（powershell.exe 的标准缩写）、WMI 兜底用
	// `[wmiclass]` 而不是 Invoke-CimMethod（短 67 字符，且 PowerShell 2.0 上也有；本文始终
	// 由绝对路径的 Windows PowerShell 执行，不会落到没有 [wmiclass] 的 pwsh 上）。预算见
	// windowsUpdateBootstrapMaxLen；新增逻辑请优先放进服务端下发的助手正文。
	//
	// TLS：这段引导只下载助手脚本一个文件，而它的 SHA-256（$H）在服务端生成时就写死在
	// 本文里，经 Agent 已鉴权、已验证证书的 exec 通道送达。因此严格校验失败时降级重试
	// 一次是安全的——摘要对不上照样 throw。老 Windows 的根证书库里没有 ISRG Root X1
	// 这类新根，HTTPS 域名部署下这一步曾是整条兜底链路的第一个死点。回调只影响本进程，
	// 而本进程紧接着就退出了；真正下载二进制的助手在另一个进程里，自行判断是否降级。
	ps := fmt.Sprintf(`$ErrorActionPreference='Stop'
foreach($v in @(3072,12288)){try{[Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor $v}catch{}}
$S='%s';$B='%s';$D='%s';$H='%s';$T='AIOpsAgentLegacyUpdate';$N='aiops-agent-update'
$R="$env:SystemRoot\System32"
$W="$env:ProgramData\$N"
try{md $W -Force|Out-Null}catch{$W="$env:TEMP\$N";md $W -Force|Out-Null}
$F="$W\$N.ps1";$U="$S%s"
rm $F -Force -EA 0
try{(New-Object Net.WebClient).DownloadFile($U,$F)}catch{[Net.ServicePointManager]::ServerCertificateValidationCallback={$true};(New-Object Net.WebClient).DownloadFile($U,$F)}
$sha=[Security.Cryptography.SHA256]::Create();$fs=[IO.File]::OpenRead($F)
try{$a=([BitConverter]::ToString($sha.ComputeHash($fs))).Replace('-','').ToLowerInvariant()}finally{$fs.Dispose();$sha.Dispose()}
if($a -ne $H){rm $F -Force -EA 0;throw "update helper sha256 mismatch (want $H got $a)"}
$P="$R\WindowsPowerShell\v1.0\powershell.exe"
$A='-nop -noni -ep Bypass -File "'+$F+'" -Server "'+$S+'" -Bin "'+$B+'" -Sha "'+$D+'"'
try{[void](& "$R\schtasks.exe" /End /TN $T 2>$null)}catch{}
$k=$false
try{
 $q=@{TaskName=$T;Action=(New-ScheduledTaskAction -Execute $P -Argument $A);Trigger=(New-ScheduledTaskTrigger -Once -At ((Get-Date).AddYears(10)));Force=$true}
 try{Register-ScheduledTask @q -Principal (New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest)|Out-Null}catch{Register-ScheduledTask @q|Out-Null}
 Start-ScheduledTask -TaskName $T -EA Stop;$k=$true
}catch{}
if(-not $k){try{if(([wmiclass]'Win32_Process').Create('"'+$P+'" '+$A).ReturnValue -eq 0){$k=$true}}catch{}}
if(-not $k){Start-Process -FilePath "$R\cmd.exe" -ArgumentList ('/c start "" /b "'+$P+'" '+$A) -WindowStyle Hidden}
Write-Output "legacy agent update ok helper=$($a.Substring(0,12)) log=$W\$N.log"
`,
		strings.ReplaceAll(server, "'", "''"),
		strings.ReplaceAll(bin, "'", "''"),
		sanitizeSHA256Hex(sha),
		windowsUpdateHelperSHA256(),
		windowsUpdateHelperPath,
	)
	// Prefer absolute powershell so LocalSystem / thin PATH still works when this
	// string is executed via cmd /c (agent runShellCommand expands %SystemRoot%).
	return `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand ` + psEncodedCommand(ps)
}

func psEncodedCommand(script string) string {
	u16 := utf16.Encode([]rune(script))
	raw := make([]byte, len(u16)*2)
	for i, v := range u16 {
		raw[i*2] = byte(v)
		raw[i*2+1] = byte(v >> 8)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
