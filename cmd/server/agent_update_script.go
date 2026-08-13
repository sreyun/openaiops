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
func buildLegacyAgentUpdateCommand(goos, serverURL, bin string, force bool) string {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	bin = strings.TrimSpace(bin)
	if serverURL == "" || bin == "" {
		return ""
	}
	_ = force
	switch strings.ToLower(goos) {
	case "linux", "darwin":
		return legacyUnixAgentUpdateScript(serverURL, bin, goos == "darwin")
	case "windows":
		return legacyWindowsAgentUpdateScript(serverURL, bin)
	default:
		return ""
	}
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

func legacyUnixAgentUpdateScript(server, bin string, darwin bool) string {
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
if command -v curl >/dev/null 2>&1; then
  curl -fSL --retry 3 -o "$NEW" "$SERVER/dl/$BIN"
  curl -fsSL -o ".aiops-agent.sha256" "$SERVER/dl/$BIN.sha256"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$NEW" "$SERVER/dl/$BIN"
  wget -q -O ".aiops-agent.sha256" "$SERVER/dl/$BIN.sha256"
else
  echo "curl/wget required"; exit 1
fi
EXPECTED=$(awk '{print $1}' .aiops-agent.sha256 | tr 'A-F' 'a-f')
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
`, server, bin, restart)
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
// $Server / $Bin 由引导脚本作为命令行参数传入；其余路径本脚本自行推导。
const windowsUpdateHelperPS = `param([string]$Server,[string]$Bin)
$ErrorActionPreference='Stop'
$helperPid = $PID
$Work = Split-Path -Parent $PSCommandPath
$Log = Join-Path $Work 'aiops-agent-update.log'
function Write-Log($m){ try{ Add-Content -LiteralPath $Log -Value ("[{0}] {1}" -f (Get-Date -Format o), $m) -Encoding UTF8 }catch{} }
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
# Resolve the installed binary. The service ImagePath is authoritative and is
# tried first: a directory guess cannot find installs outside the standard
# locations, and under a SYSTEM scheduled task $env:LOCALAPPDATA points at
# systemprofile, so a per-user install would never be found by path guessing.
function Resolve-AgentExe {
  foreach($n in $svcNames){
    try{
      $svc = Get-CimInstance Win32_Service -Filter ("Name='" + $n + "'") -ErrorAction SilentlyContinue
      if(-not $svc -or -not $svc.PathName){ continue }
      $p = $svc.PathName.Trim()
      if($p.StartsWith('"')){
        $end = $p.IndexOf('"',1)
        if($end -gt 1){ $p = $p.Substring(1, $end-1) }
      } else {
        $ix = $p.ToLowerInvariant().IndexOf('.exe')
        if($ix -gt 0){ $p = $p.Substring(0, $ix+4) }
      }
      if($p -and (Test-Path -LiteralPath $p)){ return $p }
    }catch{}
  }
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
# Older helpers registered themselves as the one-shot AIOpsAgentSelfUpdate task
# and then hung on the --version probe, so the task never stops. Clear the task
# and its process before taking over.
function Clear-StuckSelfUpdateTask {
  foreach($tn in @('AIOpsAgentSelfUpdate','AIOpsAgentLegacyUpdate')){
    try { [void](Invoke-Native "$env:SystemRoot\System32\schtasks.exe" @('/End','/TN',$tn)) } catch {}
  }
  try { [void](Invoke-Native "$env:SystemRoot\System32\schtasks.exe" @('/Delete','/TN','AIOpsAgentSelfUpdate','/F')) } catch {}
  try {
    Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
      Where-Object { $_.CommandLine -and $_.CommandLine -match 'aiops-agent-update-helper\.ps1' -and $_.ProcessId -ne $helperPid } |
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
  $hasSvc=$false
  foreach($n in $svcNames){ if(Get-Service $n -ErrorAction SilentlyContinue){ $hasSvc=$true; break } }
  if($hasSvc -and $Cfg){
    $p=Start-Process -FilePath $Exe -ArgumentList @('--install-service','--config',$Cfg) -WorkingDirectory $Dir -Wait -PassThru -WindowStyle Hidden
    if($p -and $p.ExitCode -eq 0){ $ok=$true }
    if(-not $ok){
      foreach($n in $svcNames){
        $svc=Get-Service $n -ErrorAction SilentlyContinue
        if($svc){ try{ Start-Service $n; Start-Sleep 3; if(Test-Running){ $ok=$true; break } }catch{} }
      }
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
$Exe = Resolve-AgentExe
if(-not $Exe){ Write-Log 'FATAL: agent exe not found (service ImagePath and known dirs)'; exit 1 }
$Dir = Split-Path -Parent $Exe
$Bak = $Exe + '.bak'
$New = Join-Path $Dir '.aiops-agent.update.exe'
$Cfg = $null
foreach($n in @('config.yaml','config.yml','config.json')){ $c=Join-Path $Dir $n; if(Test-Path -LiteralPath $c){ $Cfg=$c; break } }
$swapped = $false
try {
  Write-Log ("helper start pid=$helperPid exe=$Exe cfg=$Cfg bin=$Bin")
  Write-Result ("running " + (Get-Date -Format o))
  # Give the exec channel time to hand the bootstrap's output back to the server
  # before the agent that carries that channel is stopped.
  Start-Sleep -Seconds 3
  try{[Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor 3072}catch{}
  Remove-Item -LiteralPath $New -Force -ErrorAction SilentlyContinue
  (New-Object Net.WebClient).DownloadFile("$Server/dl/$Bin", $New)
  $Expected = ((New-Object Net.WebClient).DownloadString("$Server/dl/$Bin.sha256") -split '\s+')[0].Trim().ToLowerInvariant()
  $Sha=[Security.Cryptography.SHA256]::Create(); $Stream=[IO.File]::OpenRead($New)
  try{ $Actual=([BitConverter]::ToString($Sha.ComputeHash($Stream))).Replace('-','').ToLowerInvariant() } finally { $Stream.Dispose(); $Sha.Dispose() }
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
    [void](Restart-Agent)
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
func legacyWindowsAgentUpdateScript(server, bin string) string {
	// The bootstrap runs INLINE, inside the agent's service Job Object: stopping
	// the service here would kill this very process mid-swap. It therefore only
	// downloads + verifies + spawns, and never touches the service or the binary.
	ps := fmt.Sprintf(`$ErrorActionPreference='Stop'
try{[Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor 3072}catch{}
$S='%s';$B='%s';$H='%s';$T='AIOpsAgentLegacyUpdate';$N='aiops-agent-update'
$W=Join-Path $env:ProgramData $N
try{md $W -Force|Out-Null}catch{$W=Join-Path $env:TEMP $N;md $W -Force|Out-Null}
$F=Join-Path $W "$N.ps1"
Remove-Item -LiteralPath $F -Force -EA 0
(New-Object Net.WebClient).DownloadFile("$S%s",$F)
$sha=[Security.Cryptography.SHA256]::Create();$fs=[IO.File]::OpenRead($F)
try{$a=([BitConverter]::ToString($sha.ComputeHash($fs))).Replace('-','').ToLowerInvariant()}finally{$fs.Dispose();$sha.Dispose()}
if($a -ne $H){Remove-Item -LiteralPath $F -Force -EA 0;throw "update helper sha256 mismatch (want $H got $a)"}
$P="$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
$A='-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "'+$F+'" -Server "'+$S+'" -Bin "'+$B+'"'
$k=$false
try{
 $t=New-ScheduledTaskAction -Execute $P -Argument $A
 $g=New-ScheduledTaskTrigger -Once -At ((Get-Date).AddYears(10))
 try{$n=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest;Register-ScheduledTask -TaskName $T -Action $t -Trigger $g -Principal $n -Force|Out-Null}catch{Register-ScheduledTask -TaskName $T -Action $t -Trigger $g -Force|Out-Null}
 Start-ScheduledTask -TaskName $T -EA Stop;$k=$true
}catch{}
if(-not $k){try{$c=Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine=('"'+$P+'" '+$A)};if($c -and $c.ReturnValue -eq 0){$k=$true}}catch{}}
if(-not $k){Start-Process -FilePath "$env:SystemRoot\System32\cmd.exe" -ArgumentList ('/c start "" /b "'+$P+'" '+$A) -WindowStyle Hidden}
Write-Output ("legacy agent update ok helper=$($a.Substring(0,12)) -> restart scheduled (detached, log $W\$N.log)")
`,
		strings.ReplaceAll(server, "'", "''"),
		strings.ReplaceAll(bin, "'", "''"),
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
