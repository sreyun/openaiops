package main

import (
	"encoding/base64"
	"fmt"
	"strings"
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

// legacyWindowsUpdateHelperPS is the *detached* stop/swap/restart body for the
// legacy Windows update path.
//
// 为什么必须分离：这段脚本经 Agent 的 exec 通道执行，是 Agent 进程的子进程，因而
// 落在服务的 Job Object 里。`sc.exe stop` 一旦停掉服务，Job 会把这个子进程一起
// 杀掉——换二进制、重启服务全都来不及跑，主机就停在"服务已停止"。module 路径靠
// schtasks(SYSTEM)/CREATE_BREAKAWAY_FROM_JOB 规避了这件事，而这条 legacy 路径恰恰
// 是在 module helper 起不来时才启用的兜底，绝不能自带同一个致命缺陷。
//
// 变量 $Exe/$New/$Cfg/$Log 由外层脚本以赋值头拼在前面（字面 here-string 不插值）。
const legacyWindowsUpdateHelperPS = `
$ErrorActionPreference='Stop'
$helperPid = $PID
function Write-Log($m){ try{ Add-Content -LiteralPath $Log -Value ("[{0}] {1}" -f (Get-Date -Format o), $m) -Encoding UTF8 }catch{} }
function Invoke-Native {
  param([string]$File,[string[]]$Arguments)
  # Never merge native stderr via 2>&1: Windows PowerShell 5.1 turns it into
  # NativeCommandError records that $ErrorActionPreference='Stop' promotes to a
  # terminating error. Judge by exit code instead.
  #
  # 参数名不能是 $Args —— 那是 PowerShell 自动变量，声明成参数后每次调用都被清空，
  # "& $File @Args" 会退化成不带参数裸跑目标程序（详见 agent 侧同名函数的注释）。
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
# 跑 Agent 二进制的 --version 必须有硬超时：探测对象随时可能变成守护进程，
# 一旦它不退出，助手会永久吊死在换二进制之前，主机静默停在旧版本。
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
function Stop-AgentProcesses {
  Get-Process -Name $procNames -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne $helperPid } | Stop-Process -Force -ErrorAction SilentlyContinue
  # 也清掉「从暂存文件被裸跑起来」的野生 Agent：进程名形如 .aiops-agent.new.<pid>，
  # 不匹配上面的名字列表，但它的映像路径一定含 aiops-agent。这是老版本 helper 留下的
  # 残骸（"& $new" 不带参数把刚下载的 Agent 当守护进程拉起，永不退出），救援时必须扫掉，
  # 否则它会一直占着 CPU 并让后续排障看到一个幽灵进程。
  Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object {
      $_.ProcessId -ne $helperPid -and $_.ExecutablePath -and
      $_.ExecutablePath -match 'aiops-agent' -and
      (-not ($_.CommandLine -and $_.CommandLine -match 'aiops-agent-(update-helper|legacy-update)'))
    } |
    ForEach-Object { try { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue } catch {} }
}
# 老版本 helper 会把自己注册成 AIOpsAgentSelfUpdate 一次性计划任务，然后吊死在
# --version 探测上，任务永远停不下来。救援前先把它连任务带进程一起结束掉。
function Clear-StuckSelfUpdateTask {
  try { [void](Invoke-Native "$env:SystemRoot\System32\schtasks.exe" @('/End','/TN','AIOpsAgentSelfUpdate')) } catch {}
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
$Dir = Split-Path -Parent $Exe
$Bak = $Exe + '.bak'
$swapped = $false
try {
  Write-Log ("legacy helper start pid=$helperPid exe=$Exe cfg=$Cfg")
  Start-Sleep -Seconds 3
  if(-not (Test-Path -LiteralPath $New)){ throw "staging missing: $New" }
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
  Write-Log ("legacy update ok version=" + (Invoke-VersionProbe $Exe).Output)
  exit 0
} catch {
  Write-Log ("legacy update failed: " + $_.Exception.Message)
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

func legacyWindowsAgentUpdateScript(server, bin string) string {
	// Keep as one encoded command; restart must use --install-service / --config
	// for service installs, and VBS/schtasks/Start-Process --config for per-user
	// one-liner installs. Bare Start-Process $Exe (no args) breaks terminal+desktop.
	ps := fmt.Sprintf(`$ErrorActionPreference='Stop'
try{[Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor 3072}catch{}
function Invoke-Native {
  param([string]$File,[string[]]$Arguments)
  # Never merge native stderr via 2>&1: Windows PowerShell 5.1 turns it into
  # NativeCommandError records that $ErrorActionPreference='Stop' promotes to a
  # terminating error, so a harmless startup warning aborted the whole update.
  #
  # 参数名不能是 $Args —— PowerShell 自动变量会把它清空，见 agent 侧同名函数注释。
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
# --version 探测必须有硬超时，否则一个不退出的二进制会把整条 exec 通道占满 10 分钟，
# 并在主机上留下一个从暂存路径拉起的野生 Agent 进程。
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
$Server='%s'; $Bin='%s'
$exeNames=@('aiops-agent.exe','aiops-agent-windows-amd64.exe','aiops-agent-windows-arm64.exe','aiops-agent-windows-amd64-win2012.exe')
$cands=@((Join-Path $env:ProgramFiles 'AIOps Agent'),(Join-Path ${env:ProgramFiles(x86)} 'AIOps Agent'),(Join-Path $env:LOCALAPPDATA 'aiops-agent'),(Join-Path $env:ProgramData 'aiops-agent'),(Join-Path $env:ProgramData 'AIOps Agent'))
$Dir=$null; $Exe=$null
foreach($d in $cands){
  foreach($n in $exeNames){ $p=Join-Path $d $n; if(Test-Path -LiteralPath $p){ $Dir=$d; $Exe=$p; break } }
  if($Exe){ break }
}
if(-not $Exe){ throw 'agent exe not found' }
$New=Join-Path $Dir '.aiops-agent.new.exe'
$Cfg=$null
foreach($n in @('config.yaml','config.yml','config.json')){ $c=Join-Path $Dir $n; if(Test-Path -LiteralPath $c){ $Cfg=$c; break } }
Invoke-WebRequest "$Server/dl/$Bin" -OutFile $New -UseBasicParsing
$Expected=((Invoke-WebRequest "$Server/dl/$Bin.sha256" -UseBasicParsing).Content -split '\s+')[0].Trim().ToLowerInvariant()
$Sha=[Security.Cryptography.SHA256]::Create(); $Stream=[IO.File]::OpenRead($New)
try{ $Actual=([BitConverter]::ToString($Sha.ComputeHash($Stream))).Replace('-','').ToLowerInvariant() } finally { $Stream.Dispose(); $Sha.Dispose() }
if(-not $Expected -or $Expected -ne $Actual){ Remove-Item $New -Force; throw 'SHA-256 mismatch' }
$probe = Invoke-VersionProbe $New
if($probe.ExitCode -ne 0){ Remove-Item $New -Force -ErrorAction SilentlyContinue; throw ("staging not runnable (exit="+$probe.ExitCode+"): "+$probe.Output) }

# ---- hand off to a DETACHED helper --------------------------------------
# Everything above is safe to run inline (no service stop). Everything below
# would kill this very process: we are a child of the agent, inside the service
# Job Object, so stopping the service tears us down mid-swap.
$Work=$null
foreach($d in @((Join-Path $env:ProgramData 'aiops-agent-update'),(Join-Path $env:SystemRoot 'Temp\aiops-agent-update'),(Join-Path $env:TEMP 'aiops-agent-update'))){
  if(-not $d){ continue }
  try{ New-Item -ItemType Directory -Force -Path $d | Out-Null; $probeFile=Join-Path $d '.w'; Set-Content -LiteralPath $probeFile -Value '1'; Remove-Item -LiteralPath $probeFile -Force; $Work=$d; break }catch{}
}
if(-not $Work){ throw 'no writable work dir for update helper' }
$Helper=Join-Path $Work 'aiops-agent-legacy-update.ps1'
$LogPath=Join-Path $Work 'aiops-agent-update.log'
function Quote-PS([string]$s){ return "'" + $s.Replace("'","''") + "'" }
$nl=[Environment]::NewLine
$hdr='$Exe = '+(Quote-PS $Exe)+$nl+'$New = '+(Quote-PS $New)+$nl+'$Cfg = '+(Quote-PS ([string]$Cfg))+$nl+'$Log = '+(Quote-PS $LogPath)+$nl
$body=@'
%s
'@
[IO.File]::WriteAllText($Helper, $hdr + $body, (New-Object Text.UTF8Encoding $false))
$PSExe="$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
$HelperArgs='-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "'+$Helper+'"'
$spawned=$false
# 1) Scheduled task as SYSTEM — fully outside our job object.
try{
  $act=New-ScheduledTaskAction -Execute $PSExe -Argument $HelperArgs
  $trg=New-ScheduledTaskTrigger -Once -At ((Get-Date).AddYears(10))
  try{
    $prin=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    Register-ScheduledTask -TaskName 'AIOpsAgentLegacyUpdate' -Action $act -Trigger $trg -Principal $prin -Force | Out-Null
  }catch{
    Register-ScheduledTask -TaskName 'AIOpsAgentLegacyUpdate' -Action $act -Trigger $trg -Force | Out-Null
  }
  Start-ScheduledTask -TaskName 'AIOpsAgentLegacyUpdate' -ErrorAction Stop
  $spawned=$true
}catch{}
# 2) WMI process create — the child is parented by WmiPrvSE, not by us.
if(-not $spawned){
  try{
    $r=Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = ('"'+$PSExe+'" '+$HelperArgs) }
    if($r -and $r.ReturnValue -eq 0){ $spawned=$true }
  }catch{}
}
# 3) Last resort: cmd start /b (weakest job isolation, but better than inline).
if(-not $spawned){
  Start-Process -FilePath "$env:SystemRoot\System32\cmd.exe" -ArgumentList ('/c start "" /b "'+$PSExe+'" '+$HelperArgs) -WindowStyle Hidden
  $spawned=$true
}
if(-not $spawned){ throw 'failed to spawn detached update helper' }
Write-Output ('legacy agent update ok sha='+$Actual+' -> restart scheduled (detached helper, log '+$LogPath+')')
`,
		strings.ReplaceAll(server, "'", "''"),
		strings.ReplaceAll(bin, "'", "''"),
		legacyWindowsUpdateHelperPS,
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
