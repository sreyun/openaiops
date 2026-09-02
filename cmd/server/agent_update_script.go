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

	"aiops-monitor/shared"
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
# known units must match cmd/agent knownAgentUnits (incl. aiops-relay).
# Gateway installs only ship aiops-relay.service; omitting it made unit_file_exists
# false → --install-service wrote a competing aiops-agent unit and never restarted
# the relay, so the swap looked healthy while the LAN gateway kept the old binary.
agent_alive() {
  for u in aiops-agent aiops-monitor-agent aiops-relay; do
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
    for u in aiops-agent aiops-monitor-agent aiops-relay; do
      [ -f "$base/${u}.service" ] && return 0
    done
  done
  return 1
}
start_units() {
  systemctl daemon-reload 2>/dev/null || true
  # Restart every known unit that accepts the job — shared binary on a
  # dual agent+relay host must reload both, not only the first success.
  ok=0
  for u in aiops-agent aiops-monitor-agent aiops-relay; do
    systemctl restart "$u" 2>/dev/null && ok=1
  done
  [ "$ok" -eq 1 ]
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
  host_run sh -c 'for u in aiops-agent aiops-monitor-agent aiops-relay; do
    rm -rf /etc/systemd/system/${u}.service.d /run/systemd/system/${u}.service.d 2>/dev/null || true
    f=/etc/systemd/system/${u}.service; [ -f "$f" ] || continue
    sed -i -e "s/^User=.*/User=root/" -e "s/^ProtectHome=.*/ProtectHome=false/" \
      -e "s/^ProtectSystem=.*/ProtectSystem=false/" -e "s/^PrivateTmp=.*/PrivateTmp=false/" \
      -e "s/^NoNewPrivileges=.*/NoNewPrivileges=false/" -e "s/^KillMode=.*/KillMode=process/" \
      -e "/^CapabilityBoundingSet=/d" "$f" 2>/dev/null || true
    grep -q "^ProtectSystem=false" "$f" || echo "ProtectSystem=false" >> "$f"
    grep -q "^User=root" "$f" || echo "User=root" >> "$f"
    grep -q "^KillMode=process" "$f" || echo "KillMode=process" >> "$f"
  done; systemctl daemon-reload' 2>/dev/null || true
  # 没有任何单元时才做完整安装（--install-service 会 stop+删除单元，是破坏性的）。
  # aiops-relay alone counts as "has a unit" — never invent aiops-agent beside it.
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
// 第四条约束，2026-08-17 现网丢过一次证据：**本脚本的 log/result 文件名不能与 Agent 侧
// module 助手那一组相同**。两条路都写在 ProgramData\aiops-agent-update\ 下，而 Agent 每次
// 开工都会 os.Remove 掉 aiops-agent-update.{log,result} 来清陈旧标记——对它自己完全正确，
// 但顺手把 legacy 这条路刚写下的失败原因一起删了。现网那台机器的取证输出里只剩计划任务的
// "Last Result: 1"，唯一记着"为什么失败"的两个文件已经不在。因此本脚本写
// aiops-agent-legacy-update.{log,result}；引导脚本等的标记也是同一个名字（见 $G）。
//
// 第三条隐性约束，踩过一次：**本脚本绝不能终止 AIOpsAgentLegacyUpdate 这个计划任务**。
// 它自己就是该任务的运行实例，`schtasks /End` 会连整个任务进程树一起终止——而历史上
// 那行调用恰好落在停服务、换二进制之前，于是每一次救援都死在那里：主机停在旧版本，
// 助手日志停在 "staging --version"，与「Windows 无法自动升级」的症状完全一致。清理上
// 一轮吊死的实例只能由引导脚本在 schtasks /Run 之前做。
const windowsUpdateHelperPS = `param([string]$Server,[string]$Bin,[string]$Sha)
$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
$helperPid = $PID
$Started = Get-Date
$Work = Split-Path -Parent $PSCommandPath
# These file names must differ from the agent module helper's -- it deletes its
# own aiops-agent-update.{log,result} on every run and would take this path's
# evidence with them. See the Go comment on windowsUpdateHelperPS.
$Log = Join-Path $Work 'aiops-agent-legacy-update.log'
function Write-Log($m){ try{ Add-Content -LiteralPath $Log -Value ("[{0}] {1}" -f (Get-Date -Format o), $m) -Encoding UTF8 }catch{} }
# The result file is this helper's only channel back to the panel, so it is
# written before anything that can fail. $Dir is unknown until the install is
# resolved -- the copy beside the exe is added once it is.
$Dir = $null
function Write-Result($m){
  $targets = @(Join-Path $Work 'aiops-agent-legacy-update.result')
  if($Dir){ $targets += (Join-Path $Dir 'aiops-agent-legacy-update.result') }
  foreach($p in $targets){ try{ Set-Content -LiteralPath $p -Value $m -Encoding UTF8 }catch{} }
}
# One swap at a time, fleet-wide-proof: the bootstrap tries WMI, then a scheduled
# task, then cmd, and moves on to the next one when the previous did not produce
# a result marker within 12s. On a slow host that check can time out while the
# first helper is merely starting up, and two helpers fighting over the same
# locked PE is worse than no update at all. A named global mutex makes the loser
# exit at once; the winner still writes the marker the bootstrap is waiting for,
# so the retry costs nothing. An abandoned mutex (previous helper killed
# mid-swap) grants ownership -- that is exactly when a retry SHOULD proceed.
$Mutex = $null
$owned = $true
try {
  $Mutex = New-Object Threading.Mutex($false,'Global\AIOpsAgentUpdateHelper')
  try { $owned = $Mutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $owned = $true }
} catch { $owned = $true }
if(-not $owned){
  # Still write the marker: the bootstrap wiped it and is now waiting for one, and
  # "a swap is already in progress" is a perfectly good answer to give it. Staying
  # silent here would make the bootstrap declare "helper never started" and fail an
  # update that is in fact running.
  Write-Log 'another update helper holds the lock; this instance exits'
  Write-Result ("running " + (Get-Date -Format o) + " stage=locked another helper is mid-swap")
  exit 0
}
Write-Result ("running " + (Get-Date -Format o) + " stage=start pid=" + $helperPid)
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
` + shared.WindowsVersionProbePS + `$procNames=@('aiops-agent','aiops-agent-windows-amd64','aiops-agent-windows-arm64','aiops-agent-windows-amd64-win2012')
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
# Restart-Agent returns 'service' / 'usermode' / 'failed', never a boolean.
# 'usermode' means the binary runs but no Windows service does: the host stays up
# only until that process or its logon session ends, and nothing restarts it on
# reboot. Callers MUST compare the returned string against 'failed'. A boolean
# test would silently invert the meaning: in PowerShell every non-empty string is
# truthy, so 'failed' would be read as success and the rollback never fires.
function Restart-Agent {
  $ok=$false
  $svcs=@()
  foreach($n in $svcNames){ if(Get-Service $n -ErrorAction SilentlyContinue){ $svcs += $n } }
  $hasSvc = ($svcs.Count -gt 0)
  Write-Log ("restart path hasService=$hasSvc cfg=$Cfg")
  if($hasSvc -and $Cfg){
    # $Cfg MUST be quoted here. Start-Process -ArgumentList joins an array with
    # single spaces and adds NO quoting of its own, so the default install path
    # 'C:\Program Files\AIOps Agent\config.yaml' arrives at the agent as
    # '--config C:\Program' plus two stray positional arguments. The agent then
    # writes that truncated path into the service ImagePath, and every later start
    # cannot find its config: it falls back to localhost:8529 and the host is
    # offline forever while the service looks perfectly healthy. This is how a
    # SUCCESSFUL swap still took hosts down. (The wscript calls a few lines up were
    # already quoting for exactly this reason.)
    $p=Start-Process -FilePath $Exe -ArgumentList @('--install-service','--config',('"'+$Cfg+'"')) -WorkingDirectory $Dir -Wait -PassThru -WindowStyle Hidden
    # Never judge by $p.ExitCode: on hosts that intercept or wrap process
    # creation (EDR), the getter throws and PowerShell swallows it into $null,
    # so "-eq 0" is False for a run that fully succeeded -- the same defect that
    # made the version probe reject a good binary. Judge by service state below.
    $code='unavailable'
    try{ if($null -ne $p){ $code=[string]$p.ExitCode } }catch{ $code='unavailable' }
    if([string]::IsNullOrEmpty($code)){ $code='unavailable' }
    Write-Log ("install-service exit=" + $code + " (advisory only; verdict comes from service state)")
  }
  # A registered service already carries '--service --config <abs>' in its
  # ImagePath, so plain start is the correct recovery -- including when no config
  # could be resolved at all. Gating this on $Cfg is how a host ended up with a
  # freshly swapped binary, a stopped service and no way back online.
  # Runs unconditionally now. --install-service normally starts the service
  # itself, in which case sc start just returns ERROR_SERVICE_ALREADY_RUNNING and
  # the status check below confirms it -- so this doubles as the verification the
  # exit code can no longer provide.
  if($svcs.Count -gt 0){
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
  if($ok){ return 'service' }
  if($true){
    $vbs=Join-Path $Dir 'start-agent.vbs'
    if(Test-Path -LiteralPath $vbs){
      Start-Process wscript.exe -ArgumentList ('"'+$vbs+'"') -WorkingDirectory $Dir -WindowStyle Hidden
    } else {
      foreach($tn in @('AIOpsAgent','AIOpsAgentKeepalive','AIOps Agent')){
        [void](Invoke-Native "$env:SystemRoot\System32\schtasks.exe" @('/Run','/TN',$tn))
      }
      # The else branch returns 'failed', not $false. This function's contract is
      # the three strings documented above, and the caller compares with
      # -eq 'failed'. A boolean here matches neither 'failed' nor 'usermode', so
      # the swap would be reported as a clean success on the one host where
      # nothing was restarted at all.
      # $Cfg must be quoted: Start-Process -ArgumentList joins with spaces and
      # quotes nothing, so 'C:\Program Files\...' would arrive truncated.
      if($Cfg){ Start-Process -FilePath $Exe -ArgumentList @('--config',('"'+$Cfg+'"')) -WorkingDirectory $Dir -WindowStyle Hidden }
      else { Write-Log 'FATAL: no service and no config beside exe; refusing bare Start-Process'; return 'failed' }
    }
  }
  # Last chance: a service may still be coming up. Only a Running service counts
  # as a real restart; anything else is at best a loose process.
  for($i=0;$i -lt 20;$i++){
    foreach($n in $svcNames){
      $s=Get-Service $n -ErrorAction SilentlyContinue
      if($s -and $s.Status -eq 'Running'){ Write-Log ("service running: " + $n); return 'service' }
    }
    if(Test-Running){
      Write-Log 'WARNING: agent process is up but no Windows service is running'
      return 'usermode'
    }
    Start-Sleep -Seconds 2
  }
  return 'failed'
}
$SvcCmd = Get-AgentServiceCommandLine
$Exe = Resolve-AgentExe
if(-not $Exe){
  Write-Log 'FATAL: agent exe not found (service ImagePath and known dirs)'
  Write-Result 'fail agent exe not found (no aiops service ImagePath, and none of the known install dirs has the binary)'
  exit 1
}
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
  Write-Result ("running " + (Get-Date -Format o) + " stage=resolved exe=" + $Exe)
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
  if(-not (Test-ProbeRunnable $probe)){ Remove-Item $New -Force -ErrorAction SilentlyContinue; throw ("staging not runnable (exit="+$probe.ExitCode+"): "+$probe.Output) }
  if($null -eq $probe.ExitCode){ Write-Log ("staging --version exit code unavailable; accepted on output") }
  Write-Log ("staging --version: " + $probe.Output)
  Write-Result ("running " + (Get-Date -Format o) + " stage=staged sha=" + $Actual)
  # Everything above is harmless; everything below stops the agent -- and the
  # agent is what carries the exec channel the bootstrap is still writing its
  # result on. Killing it early loses the bootstrap's output, which is the only
  # thing that tells the panel WHICH spawn path brought this helper up. Hold off
  # until the bootstrap has had its full marker window (12s) plus slack. The wait
  # is measured from helper start, so a slow download has already paid for it.
  $quiet = 20 - ((Get-Date) - $Started).TotalSeconds
  if($quiet -gt 0){ Start-Sleep -Seconds ([int][Math]::Ceiling($quiet)) }
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
  Write-Result ("running " + (Get-Date -Format o) + " stage=swap")
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
  $restartMode = Restart-Agent
  if($restartMode -eq 'failed'){ throw 'agent not running after update' }
  $ver = (Invoke-VersionProbe $Exe).Output
  if($restartMode -eq 'usermode'){
    # Swapped, and something is running -- but not as a service. Do not report
    # this as a successful update: the host will drop offline later while the
    # console still shows green, which is exactly how a fleet goes dark quietly.
    Write-Log ("update degraded: binary is at " + $ver + " but the Windows service is NOT running")
    Write-Result ("degraded " + (Get-Date -Format o) + " version=" + $ver + " reason=service-not-running")
    exit 0
  }
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
      $rbMode = Restart-Agent
      Write-Log ("rollback restart mode=" + $rbMode)
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
	// 清掉，schtasks /Run 会静默地什么都不做。所以陈旧实例只能在这里、在拉起新
	// 实例之前清理。
	//
	// 长度：本文经 base64(UTF-16LE) 膨胀 8/3 倍后要塞进 cmd.exe 的 8191 字符硬上限，所以
	// 这里的写法有意压缩过——`$R` 复用三处 System32 路径、`"$W\$N.ps1"` 代替 Join-Path、
	// 助手的启动参数用 `-nop -noni -ep`（powershell.exe 的标准缩写）、WMI 兜底用
	// `[wmiclass]` 而不是 Invoke-CimMethod（短 67 字符，且 PowerShell 2.0 上也有；本文始终
	// 由绝对路径的 Windows PowerShell 执行，不会落到没有 [wmiclass] 的 pwsh 上）。预算见
	// windowsUpdateBootstrapMaxLen；新增逻辑请优先放进服务端下发的助手正文。
	//
	// 拉起助手的那三次尝试写成 `&$Try 'wmi' {…}`（脚本块存进变量再 & 调用），而不是
	// 定义一个函数直接按名字调，是被现网教的：**PowerShell 的命令解析顺序是
	// 别名 → 函数 → cmdlet → 可执行文件，别名排在函数前面**。原来那个函数叫 `Sp`，而
	// `sp` 是 Set-ItemProperty 的内置别名，于是 `Sp 'wmi' {…}` 被解析成
	// `Set-ItemProperty -Path 'wmi' -Name {脚本块}`，报
	// 「Cannot evaluate parameter 'Name' because its argument is specified as a script
	// block and there is no input」——整条引导在拉起助手之前就终止了。短名字（本文因为
	// 命令行预算全是短名字）撞上内置别名的概率相当高：sp/gc/sc/ls/cp/mv/ni/gi/si/gm/gp…
	// 变量名没有这套解析规则，`&$Try` 只可能是我们自己定义的那个脚本块。
	// 同类约束由 TestBootstrapCommandsDoNotCollideWithBuiltinAliases 守着。
	//
	// 压缩成单字母变量名要付一个代价，这里踩过：**PowerShell 的变量名不区分大小写**。
	// 原来的 `$a` 存助手摘要、`$A` 存助手启动参数，是同一个变量；后写的参数把摘要冲掉，
	// 于是成功那行打出来的是 `helper=-nop -noni -`（参数串的前 12 个字符），而不是摘要。
	// 校验早在赋值之前完成，所以没有安全问题，但唯一能证明"下发的是哪一份助手"的字段
	// 变成了噪声。函数参数同理：`function Sp($m,$b)` 里的 `$m`/`$b` 会在函数体内遮蔽外面的
	// `$M`/`$B`。凡在本文里新起变量，先通读全文确认没有同名（忽略大小写）的。
	//
	// 拉起助手的顺序是 WMI → 计划任务 → cmd start，**WMI 必须排在计划任务前面**。
	//
	// 三者都能把助手送出 Agent 的 Job Object，但计划任务多带一层任务调度器策略，而它的
	// 默认值会让升级无声失败：DisallowStartIfOnBatteries=True 让电池供电的主机
	// （Win10/11 笔记本、平板、暴露电池的虚拟机）「注册成功、Start 成功、任务从不运行」；
	// StopIfGoingOnBatteries=True 会在升级途中掉电时把助手掐死在停服务与重启服务之间。
	// 显式传 -Settings 能治，但那串参数塞不进 cmd.exe 的命令行预算
	// （见 windowsUpdateBootstrapMaxLen——那条上限保护的正是这整条兜底链路）。
	//
	// Win32_Process.Create 没有这一层策略：进程由 WMI 服务创建，天然不在 Agent 的 Job 里，
	// 且继承调用方令牌（Agent 服务是 LocalSystem，助手就是 SYSTEM）。把它提到第一位，
	// 等于**从结构上**绕开电池策略，而不是靠加参数去补，一个字符的预算都不用花。
	// 计划任务与 cmd start 保留为 WMI 被安全基线禁用时的退路。
	//
	// **但「拉起来了」不等于「跑起来了」，而这条区别正是现网那条 pending_verify 的成因。**
	// 三条路都会「成功」得毫无意义：Win32_Process.Create 返回 0 只说明创建了进程，
	// 计划任务被电池策略挡住时 /Run 照样返回成功，cmd start 更是拉起就走。原来的引导
	// 只要有一条路没抛异常就打印 "legacy agent update ok"，于是服务端把它当成"已下发、
	// 等版本号"，5 分钟后超时，操作台上写的是「重启已排程但版本没跟上」——一句既不知道
	// 助手到底有没有起来、也不知道是哪条路把它拉起来的空话。
	//
	// 现在每拉起一次就等助手自己写下 result 标记（助手一进主流程就写 "running"，见
	// windowsUpdateHelperPS），最多等 12 秒；等不到就换下一条路。三条都等不到就 throw，
	// 让这次升级**当场以真实原因失败**，而不是伪装成 ok 再让服务端去猜。成功时把
	// via=wmi|task|cmd 一并打出来——哪条路在这台机器上可用，是下一次排障的起点。
	//
	// 计划任务这一路改用 schtasks.exe /Create /Run（而不是 Register-ScheduledTask 系列
	// cmdlet）有三个理由：ScheduledTasks 模块在 Server 2008 R2/2012 上根本不存在，那批机器
	// 过去等于只有 WMI 一条路；模块首次加载会往 stderr 吐一大段 CLIXML 进度记录，把
	// exec 输出里真正有用的那一行挤出可视范围（现网原样撞上过）；而且 CLI 写法更短。
	// 任务动作指向一个一次性生成的 .cmd，从而绕开 /TR 的嵌套引号地狱。
	//
	// TLS：这段引导只下载助手脚本一个文件，而它的 SHA-256（$H）在服务端生成时就写死在
	// 本文里，经 Agent 已鉴权、已验证证书的 exec 通道送达。因此严格校验失败时降级重试
	// 一次是安全的——摘要对不上照样 throw。老 Windows 的根证书库里没有 ISRG Root X1
	// 这类新根，HTTPS 域名部署下这一步曾是整条兜底链路的第一个死点。回调只影响本进程，
	// 而本进程紧接着就退出了；真正下载二进制的助手在另一个进程里，自行判断是否降级。
	ps := fmt.Sprintf(`$ErrorActionPreference='Stop';$ProgressPreference='SilentlyContinue'
foreach($v in @(3072,12288)){try{[Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor $v}catch{}}
$S='%s';$B='%s';$D='%s';$H='%s';$T='AIOpsAgentLegacyUpdate';$N='aiops-agent-update'
$R="$env:SystemRoot\System32"
$W="$env:ProgramData\$N"
try{md $W -Force|Out-Null}catch{$W="$env:TEMP\$N";md $W -Force|Out-Null}
$G='aiops-agent-legacy-update'
$F="$W\$N.ps1";$U="$S%s";$Mk="$W\$G.result";$Cm="$W\$N.cmd"
rm $F,$Mk -Force -EA 0
try{(New-Object Net.WebClient).DownloadFile($U,$F)}catch{[Net.ServicePointManager]::ServerCertificateValidationCallback={$true};(New-Object Net.WebClient).DownloadFile($U,$F)}
$sha=[Security.Cryptography.SHA256]::Create();$fs=[IO.File]::OpenRead($F)
try{$hx=([BitConverter]::ToString($sha.ComputeHash($fs))).Replace('-','').ToLowerInvariant()}finally{$fs.Dispose();$sha.Dispose()}
if($hx -ne $H){rm $F -Force -EA 0;throw "update helper sha256 mismatch (want $H got $hx)"}
$P="$R\WindowsPowerShell\v1.0\powershell.exe"
$Ar='-nop -noni -ep Bypass -File "'+$F+'" -Server "'+$S+'" -Bin "'+$B+'" -Sha "'+$D+'"'
Set-Content $Cm ('"'+$P+'" '+$Ar)
try{[void](& "$R\schtasks.exe" /End /TN $T 2>$null)}catch{}
$k=''
$Try={param($tg,$bk)if($k){return};try{&$bk}catch{return};for($i=0;$i -lt 12;$i++){if(Test-Path $Mk){$script:k=$tg;return};sleep 1}}
&$Try 'wmi' {if(([wmiclass]'Win32_Process').Create('"'+$P+'" '+$Ar).ReturnValue){throw 'x'}}
&$Try 'task' {[void](& "$R\schtasks.exe" /Create /TN $T /TR $Cm /SC ONCE /ST 23:59 /RU SYSTEM /F);if($LASTEXITCODE){throw 'x'};[void](& "$R\schtasks.exe" /Run /TN $T)}
&$Try 'cmd' {Start-Process $Cm -WindowStyle Hidden}
if(-not $k){throw "helper never started (wmi/task/cmd); see $W\$G.log"}
Write-Output "legacy agent update ok helper=$($hx.Substring(0,12)) via=$k log=$W\$G.log"
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
