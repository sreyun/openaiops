package main

import (
	"fmt"
	"strings"
)

// Self-update restart helpers, as plain string builders.
//
// These scripts are the most failure-prone part of the whole product: they run
// while the agent that could report an error is being torn down, on hosts the
// CI never touches. Keeping them in a build-tag-free file (instead of next to
// their GOOS-specific launchers) means `go test ./cmd/agent` exercises the
// Windows helper and the macOS helper from a Linux CI runner — see
// module_agent_update_scripts_test.go.

// cgroupEscapeSh moves the helper out of the agent's own systemd cgroup.
//
// 为什么必须逃出 cgroup：`systemctl stop|restart aiops-agent` 会终止**整个 unit
// cgroup** 里的进程。升级助手是 Agent 派生的子进程，天然落在同一个 cgroup 里
// （Setsid 只脱离会话，管不了 cgroup），于是它一执行 stop 就把自己也一并杀掉——
// stop 之后的 enable/start 再也没机会跑，主机就永久停在"服务已停止"状态。
// 这正是批量自动升级把整个机群打离线的原因，务必保留这段逃逸。
const cgroupEscapeSh = `
escape_cgroup() {
  [ "$(id -u)" -eq 0 ] || return 0
  # cgroup v2 (unified): 根 cgroup 豁免 no-internal-processes 规则，可直接迁入。
  if [ -w /sys/fs/cgroup/cgroup.procs ]; then
    echo $$ > /sys/fs/cgroup/cgroup.procs 2>/dev/null && return 0
  fi
  # cgroup v1: systemd 控制器所在层级。
  for c in /sys/fs/cgroup/systemd /sys/fs/cgroup/name=systemd; do
    if [ -w "$c/cgroup.procs" ]; then
      echo $$ > "$c/cgroup.procs" 2>/dev/null && return 0
    fi
  done
  return 1
}
escape_cgroup || true
`

// agentUpdateLogSh appends helper progress to a file that survives the swap, so
// "升级后失联" can be diagnosed on the box even though the agent is gone.
const agentUpdateLogSh = `
LOG=/var/log/aiops-agent-update.log
( : >> "$LOG" ) 2>/dev/null || LOG=/tmp/aiops-agent-update.log
ulog() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG" 2>/dev/null || true; }
`

// agentProcAliveSh defines agent_proc_alive(): true only for a *daemon* agent
// process.
//
// 关键：远程桌面 worker 是同一个二进制、同一个进程名（`--desktop-worker`），跑在
// 独立会话里。`systemctl restart` / `launchctl kickstart -k` 直接 SIGKILL 守护
// 进程，它的清理逻辑（stopWorker）根本来不及跑，于是 worker 会残留。裸 `pgrep -x
// aiops-agent` 会把这个残留当成"agent 活着"，让看门狗误判升级成功并跳过回滚——
// 新二进制起不来的机器就这样静默变砖。必须按命令行把 worker 排除掉。
const agentProcAliveSh = `
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
`

// darwinAgentLabelsSh lists every launchd label the agent can be installed
// under: --install-service writes com.aiops.monitor.agent, while the one-click
// install.sh writes com.aiops.agent (root LaunchDaemon or per-user LaunchAgent).
const darwinAgentLabelsSh = `
UIDN=$(id -u)
AGENT_LABELS="system/com.aiops.monitor.agent system/com.aiops.agent gui/$UIDN/com.aiops.agent gui/$UIDN/com.aiops.monitor.agent"
`

// buildLinuxAgentRestartScript returns the detached helper that unlocks the
// systemd unit, restarts it, and rolls back to .bak if the agent stays down.
func buildLinuxAgentRestartScript(exe, dir, cfgPath, unit string) string {
	return fmt.Sprintf(`%s%s%s
sleep 2
EXE=%s
DIR=%s
CFG=%s
UNIT=%s
BAK="$EXE.bak"
RESTARTED=0

# Run a command in the host mount namespace when possible (escape ProtectSystem).
host_run() {
  if [ "$(id -u)" -eq 0 ] && command -v nsenter >/dev/null 2>&1 && [ -e /proc/1/ns/mnt ]; then
    nsenter -t 1 -m -u -i -n -- "$@"
  else
    "$@"
  fi
}

# Shared body: unlock units (must execute inside host mount ns).
# 只改写单元文件，绝不 stop —— stop 会连助手一起杀掉（见 cgroupEscapeSh 注释）。
UNLOCK_SH='for u in aiops-agent aiops-monitor-agent; do
  for base in /etc/systemd/system /run/systemd/system /lib/systemd/system /usr/lib/systemd/system; do
    rm -rf "$base/${u}.service.d" 2>/dev/null || true
  done
  f="/etc/systemd/system/${u}.service"
  [ -f "$f" ] || continue
  sed -i \
    -e "s/^User=.*/User=root/" \
    -e "s/^Group=.*/Group=root/" \
    -e "s/^ProtectHome=.*/ProtectHome=false/" \
    -e "s/^ProtectSystem=.*/ProtectSystem=false/" \
    -e "s/^PrivateTmp=.*/PrivateTmp=false/" \
    -e "s/^NoNewPrivileges=.*/NoNewPrivileges=false/" \
    -e "s|^Environment=HOME=.*|Environment=HOME=/root|" \
    -e "s|^Environment=USER=.*|Environment=USER=root|" \
    -e "s|^Environment=LOGNAME=.*|Environment=LOGNAME=root|" \
    -e "/^CapabilityBoundingSet=/d" \
    -e "/^ReadWritePaths=/d" \
    -e "/^ReadOnlyPaths=/d" \
    -e "/^InaccessiblePaths=/d" \
    -e "/^TemporaryFileSystem=/d" \
    "$f" 2>/dev/null || true
  grep -q "^User=root" "$f" 2>/dev/null || echo "User=root" >> "$f"
  grep -q "^ProtectHome=false" "$f" 2>/dev/null || echo "ProtectHome=false" >> "$f"
  grep -q "^ProtectSystem=false" "$f" 2>/dev/null || echo "ProtectSystem=false" >> "$f"
  grep -q "^PrivateTmp=false" "$f" 2>/dev/null || echo "PrivateTmp=false" >> "$f"
  grep -q "^NoNewPrivileges=false" "$f" 2>/dev/null || echo "NoNewPrivileges=false" >> "$f"
done
systemctl daemon-reload 2>/dev/null || true'

unit_file_exists() {
  for base in /etc/systemd/system /run/systemd/system /lib/systemd/system /usr/lib/systemd/system; do
    for u in "$UNIT" aiops-agent aiops-monitor-agent; do
      [ -f "$base/${u}.service" ] && return 0
    done
  done
  return 1
}

agent_alive() {
  for u in "$UNIT" aiops-agent aiops-monitor-agent; do
    systemctl is-active --quiet "$u" 2>/dev/null && return 0
  done
  agent_proc_alive
}

# Wait until the agent is back AND stays up (a crash-looping binary must not
# count as success — Restart=always would otherwise mask a broken upgrade).
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

start_units() {
  systemctl daemon-reload 2>/dev/null || true
  for u in "$UNIT" aiops-agent aiops-monitor-agent; do
    if systemctl restart "$u" 2>/dev/null; then
      return 0
    fi
  done
  return 1
}

ulog "update helper start: exe=$EXE unit=$UNIT cfg=$CFG"

if command -v systemctl >/dev/null 2>&1; then
  # 1) Unlock the existing unit in the host ns (no stop, no reinstall).
  if [ "$(id -u)" -eq 0 ]; then
    host_run sh -c "$UNLOCK_SH"
  elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    sudo -n nsenter -t 1 -m -- sh -c "$UNLOCK_SH" 2>/dev/null \
      || sudo -n sh -c "$UNLOCK_SH" 2>/dev/null || true
  fi
  # 2) No unit at all (first-time service adoption) → full --install-service.
  #    Reinstall is destructive (it stops+removes the unit), so it is reserved
  #    for the case where there is nothing to restart.
  if ! unit_file_exists && [ -n "$CFG" ] && [ -f "$CFG" ]; then
    if [ "$(id -u)" -eq 0 ]; then
      host_run "$EXE" --install-service --config "$CFG" >/tmp/aiops-agent-update-install.log 2>&1 && RESTARTED=1
    elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
      sudo -n nsenter -t 1 -m -u -i -n -- "$EXE" --install-service --config "$CFG" >/tmp/aiops-agent-update-install.log 2>&1 \
        || sudo -n "$EXE" --install-service --config "$CFG" >/tmp/aiops-agent-update-install.log 2>&1
      agent_alive && RESTARTED=1
    fi
  fi
  # 3) Normal upgrade path: one restart job (stop+start atomically queued).
  if [ "$RESTARTED" -eq 0 ] && start_units; then
    RESTARTED=1
  fi
fi

if [ "$RESTARTED" -eq 0 ]; then
  if [ -z "$CFG" ]; then
    for c in "$DIR/config.yaml" "$DIR/config.yml" "$HOME/.aiops-agent/config.yaml"; do
      [ -f "$c" ] && CFG="$c" && break
    done
  fi
  pkill -x aiops-agent 2>/dev/null || pkill -f '[/]aiops-agent( |$)' 2>/dev/null || true
  sleep 1
  if [ -n "$CFG" ]; then
    nohup "$EXE" --config "$CFG" >/dev/null 2>&1 &
  else
    nohup "$EXE" >/dev/null 2>&1 &
  fi
  sleep 1
  agent_alive && RESTARTED=1
fi

# 4) Watchdog: the swap is only "done" once the agent is running again. A dead
#    agent cannot be reached remotely any more, so rollback must happen here.
if wait_alive; then
  ulog "update helper ok: agent running after restart"
  exit 0
fi

ulog "agent did not come back after update — rolling back to $BAK"
if [ -f "$BAK" ]; then
  cp -f "$BAK" "$EXE" 2>/dev/null || true
  chmod +x "$EXE" 2>/dev/null || true
  start_units || {
    pkill -x aiops-agent 2>/dev/null || true
    sleep 1
    if [ -n "$CFG" ]; then
      nohup "$EXE" --config "$CFG" >/dev/null 2>&1 &
    else
      nohup "$EXE" >/dev/null 2>&1 &
    fi
  }
  if wait_alive; then
    ulog "rollback ok: previous agent binary restored and running"
    echo "agent update failed, rolled back to previous binary" >&2
    exit 1
  fi
  ulog "rollback failed: agent still not running"
else
  ulog "no backup binary at $BAK — cannot roll back"
fi
echo "agent restart failed" >&2
exit 1
`, cgroupEscapeSh, agentUpdateLogSh, agentProcAliveSh,
		shellQuote(exe), shellQuote(dir), shellQuote(cfgPath), shellQuote(unit))
}

// buildDarwinAgentRestartScript returns the detached helper that kickstarts the
// launchd job and rolls back to .bak if the agent stays down.
func buildDarwinAgentRestartScript(exe, dir, cfgPath string) string {
	return fmt.Sprintf(`%s%s%s
sleep 2
EXE=%s
DIR=%s
CFG=%s
BAK="$EXE.bak"
xattr -dr com.apple.quarantine "$EXE" 2>/dev/null || true
RESTARTED=0

# launchd job state is authoritative; the process scan is only a fallback for
# plain (non-launchd) installs and already excludes the GUI desktop worker.
agent_alive() {
  for label in $AGENT_LABELS; do
    if launchctl print "$label" 2>/dev/null | grep -q "state = running"; then
      return 0
    fi
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
kickstart() {
  for label in $AGENT_LABELS; do
    launchctl kickstart -k "$label" 2>/dev/null && return 0
  done
  return 1
}

ulog "update helper start: exe=$EXE cfg=$CFG"
# launchd 已托管时只做 kickstart；--install-service 会 bootout 旧 job，
# 可能把助手一起带走，仅在没有任何 job 可踢时才回退到它。
if kickstart; then
  RESTARTED=1
elif [ -n "$CFG" ] && [ -f "$CFG" ]; then
  "$EXE" --install-service --config "$CFG" >/dev/null 2>&1 && RESTARTED=1
  kickstart && RESTARTED=1
fi
if [ "$RESTARTED" -eq 0 ]; then
  if [ -z "$CFG" ]; then
    for c in "$DIR/config.yaml" "$DIR/config.yml" "$HOME/.aiops-agent/config.yaml"; do
      [ -f "$c" ] && CFG="$c" && break
    done
  fi
  pkill -x aiops-agent 2>/dev/null || true
  sleep 1
  if [ -n "$CFG" ]; then
    nohup "$EXE" --config "$CFG" >/dev/null 2>&1 &
  else
    nohup "$EXE" >/dev/null 2>&1 &
  fi
  sleep 1
  agent_alive && RESTARTED=1
fi

if wait_alive; then
  ulog "update helper ok: agent running after restart"
  exit 0
fi
ulog "agent did not come back after update — rolling back to $BAK"
if [ -f "$BAK" ]; then
  cp -f "$BAK" "$EXE" 2>/dev/null || true
  chmod +x "$EXE" 2>/dev/null || true
  kickstart || {
    pkill -x aiops-agent 2>/dev/null || true
    sleep 1
    if [ -n "$CFG" ]; then
      nohup "$EXE" --config "$CFG" >/dev/null 2>&1 &
    else
      nohup "$EXE" >/dev/null 2>&1 &
    fi
  }
  if wait_alive; then
    ulog "rollback ok: previous agent binary restored and running"
    echo "agent update failed, rolled back to previous binary" >&2
    exit 1
  fi
fi
echo "agent restart failed" >&2
exit 1
`, agentUpdateLogSh, darwinAgentLabelsSh, agentProcAliveSh,
		shellQuote(exe), shellQuote(dir), shellQuote(cfgPath))
}

// buildWindowsUpdateHelperScript returns the detached PowerShell helper that
// stops the service, swaps the locked PE, restarts the agent and restores .bak
// on failure.
func buildWindowsUpdateHelperScript(exe, staging, cfgPath, logPath, resultPath, altResult string) string {
	return fmt.Sprintf(`$ErrorActionPreference='Stop'
$log = '%s'
$resultPath = '%s'
$altResult = '%s'
$helperPid = $PID
function Write-Log($m){ try{ Add-Content -LiteralPath $log -Value ("[{0}] {1}" -f (Get-Date -Format o), $m) -Encoding UTF8 }catch{} }
function Write-Result($m){
  foreach ($p in @($resultPath, $altResult)) {
    if (-not $p) { continue }
    try { Set-Content -LiteralPath $p -Value $m -Encoding UTF8 } catch {}
  }
}
# Invoke-Native runs a native command WITHOUT merging its stderr into the success
# stream. Windows PowerShell 5.1 turns native stderr captured via 2>&1 into
# NativeCommandError records, and $ErrorActionPreference='Stop' promotes the
# first one to a terminating error — that alone aborted every self-update before
# the binary swap, because the agent prints a config warning on startup. Judge
# native commands by their exit code instead.
#
# 参数名绝不能叫 $Args：$Args 是 PowerShell 的自动变量，声明成参数后**每次调用都会
# 被清空成空数组**（位置绑定、-Args 命名绑定一样中招），于是 "& $File @Args" 退化成不带
# 任何参数地裸跑目标程序。这曾让全部 Windows Agent 的自动升级无一例外地失败：
# Invoke-Native $new @('--version') 实际执行的是 "& $new"，把刚下载的 Agent 当守护进程
# 前台拉起，Out-String 永远等不到管道结束，助手就吊死在换二进制之前。
function Invoke-Native {
  param([string]$File,[string[]]$Arguments)
  $prevEAP = $ErrorActionPreference
  $out = ''
  $code = -1
  try {
    $ErrorActionPreference = 'Continue'
    $out = (& $File @Arguments 2>$null | Out-String)
    $code = $LASTEXITCODE
  } catch {
    $out = $_.Exception.Message
    $code = -1
  } finally {
    $ErrorActionPreference = $prevEAP
  }
  return [pscustomobject]@{ ExitCode = $code; Output = $out.Trim() }
}
# Invoke-VersionProbe 是「跑一个 Agent 二进制的 --version」的唯一入口，且带硬超时。
# 这类探测的输入正是一个随时可能变成守护进程的程序，一旦它不退出，助手就永久卡死在
# 换二进制之前 —— 主机静默停在旧版本，且没有任何错误可上报。宁可超时判失败。
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
function Wait-ServiceState([string]$Name, [string]$Want, [int]$Seconds) {
  for ($i=0; $i -lt $Seconds; $i++) {
    $s = Get-Service -Name $Name -ErrorAction SilentlyContinue
    if ($s -and $s.Status -eq $Want) { return $true }
    Start-Sleep -Seconds 1
  }
  return $false
}
function Get-AgentServiceNames {
  return @('AiopsMonitorAgent','AIOps-Agent','AIOpsAgent')
}
function Stop-AgentProcesses {
  $names = @('aiops-agent','aiops-agent-windows-amd64','aiops-agent-windows-arm64','aiops-agent-windows-amd64-win2012')
  Get-Process -Name $names -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne $helperPid } | Stop-Process -Force -ErrorAction SilentlyContinue
  Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
    Where-Object {
      $_.ProcessId -ne $helperPid -and (
        $_.Name -match '^aiops-agent' -or ($_.ExecutablePath -and $_.ExecutablePath -match 'aiops-agent')
      ) -and (-not ($_.CommandLine -and $_.CommandLine -match 'aiops-agent-update-helper'))
    } |
    ForEach-Object { try { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue } catch {} }
}
# A leftover --desktop-worker (same exe, same process name, session 1) must not
# read as a healthy agent, or a failed swap would never roll back.
function Test-AgentRunning {
  $names = @('aiops-agent','aiops-agent-windows-amd64','aiops-agent-windows-arm64','aiops-agent-windows-amd64-win2012')
  foreach ($name in (Get-AgentServiceNames)) {
    $s = Get-Service -Name $name -ErrorAction SilentlyContinue
    if ($s -and $s.Status -eq 'Running') { return $true }
  }
  $procs = @(Get-Process -Name $names -ErrorAction SilentlyContinue)
  if ($procs.Count -eq 0) { return $false }
  $all = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue)
  # CIM unavailable (hardened / legacy hosts): cannot filter, trust the list.
  if ($all.Count -eq 0) { return $true }
  $daemon = $all | Where-Object {
    $_.Name -match '^aiops-agent' -and $_.ProcessId -ne $helperPid -and
    (-not ($_.CommandLine -and $_.CommandLine -match '--desktop-worker'))
  } | Select-Object -First 1
  return ($null -ne $daemon)
}
function Restart-AgentUserMode {
  param([string]$Exe,[string]$Cfg,[string]$Dir)
  if (-not $Cfg -or -not (Test-Path -LiteralPath $Cfg)) {
    Write-Log 'FATAL: no config beside exe; refusing bare Start-Process'
    return $false
  }
  $vbs = Join-Path $Dir 'start-agent.vbs'
  if (Test-Path -LiteralPath $vbs) {
    Write-Log 'user-mode: start-agent.vbs'
    Start-Process -FilePath "$env:SystemRoot\System32\wscript.exe" -ArgumentList (@('"'+$vbs+'"')) -WorkingDirectory $Dir -WindowStyle Hidden
    Start-Sleep -Seconds 4
    if (Test-AgentRunning) { Write-Log 'user-mode VBS ok'; return $true }
  }
  foreach ($tn in @('AIOpsAgent','AIOpsAgentKeepalive','AIOps Agent')) {
    $r = Invoke-Native "$env:SystemRoot\System32\schtasks.exe" @('/Run','/TN',$tn)
    Write-Log ("schtasks /Run $tn exit=" + $r.ExitCode + ": " + $r.Output)
    if ($r.ExitCode -eq 0) {
      Start-Sleep -Seconds 4
      if (Test-AgentRunning) { Write-Log ("user-mode schtasks ok: " + $tn); return $true }
    }
  }
  Write-Log ('user-mode Start-Process --config ' + $Cfg)
  Start-Process -FilePath $Exe -ArgumentList @('--config', $Cfg) -WorkingDirectory $Dir -WindowStyle Hidden
  Start-Sleep -Seconds 4
  return (Test-AgentRunning)
}
function Restart-AgentService {
  param([string]$Exe,[string]$Cfg,[string]$Dir)
  $svcs = @()
  foreach ($name in (Get-AgentServiceNames)) {
    if (Get-Service -Name $name -ErrorAction SilentlyContinue) { $svcs += $name }
  }
  $hasSvc = ($svcs.Count -gt 0)
  Write-Log ("restart path hasService=$hasSvc services=" + ($svcs -join ',') + " cfg=$Cfg")
  if ($hasSvc -and $Cfg -and (Test-Path -LiteralPath $Cfg)) {
    Write-Log ("install-service with config: " + $Cfg)
    $p = Start-Process -FilePath $Exe -ArgumentList @('--install-service','--config', $Cfg) -WorkingDirectory $Dir -Wait -PassThru -WindowStyle Hidden
    if ($p -and $p.ExitCode -eq 0) {
      foreach ($name in (Get-AgentServiceNames)) {
        if (Wait-ServiceState $name 'Running' 45) {
          Write-Log ("service running: " + $name)
          Write-Log ("post-restart version: " + (Invoke-VersionProbe $Exe).Output)
          return $true
        }
      }
    } else {
      $code = if ($p) { $p.ExitCode } else { 'null' }
      Write-Log ("install-service exit=" + $code)
    }
  }
  # 已注册的服务，它的 ImagePath 里本来就带着 "--service --config <绝对路径>"，所以
  # **直接启动**永远是正确的恢复动作——包括「exe 旁边找不到配置」的安装（--config 可以
  # 指向任意绝对路径）。这一步原先被锁在 $Cfg 判断里：换完二进制、停掉服务之后直接掉进
  # user-mode 分支，而 user-mode 又以「没有配置」为由拒绝启动，主机就带着一个崭新的、
  # 从未跑起来过的二进制永久离线。
  foreach ($name in $svcs) {
    try {
      [void](Invoke-Native "$env:SystemRoot\System32\sc.exe" @('start',$name))
      Start-Service -Name $name -ErrorAction SilentlyContinue
    } catch {
      Write-Log ("Start-Service $name failed: " + $_.Exception.Message)
      continue
    }
    if (Wait-ServiceState $name 'Running' 45) { Write-Log ("Start-Service ok: " + $name); return $true }
  }
  return (Restart-AgentUserMode -Exe $Exe -Cfg $Cfg -Dir $Dir)
}
try {
  Write-Log ("helper start pid=$helperPid")
  Write-Result ("running " + (Get-Date -Format o))
  # Let the module HTTP response finish before we stop the agent service.
  Start-Sleep -Seconds 3
  $exe = '%s'
  $new = '%s'
  $cfg = '%s'
  $dir = Split-Path -Parent $exe
  $bak = $exe + '.bak'
  $swapped = $false
  if (-not (Test-Path -LiteralPath $new)) { throw "staging missing: $new" }
  if (-not $cfg) {
    foreach ($n in @('config.yaml','config.yml','config.json')) {
      $c = Join-Path $dir $n
      if (Test-Path -LiteralPath $c) { $cfg = $c; break }
    }
  }
  Write-Log ("update begin exe=$exe cfg=$cfg")
  $probe = Invoke-VersionProbe $new
  if ($probe.ExitCode -ne 0) {
    throw ("staging binary not runnable before swap (exit=" + $probe.ExitCode + "): " + $probe.Output)
  }
  Write-Log ("staging --version: " + $probe.Output)
  foreach ($name in (Get-AgentServiceNames)) {
    $svc = Get-Service -Name $name -ErrorAction SilentlyContinue
    if ($svc) {
      try {
        [void](Invoke-Native "$env:SystemRoot\System32\sc.exe" @('stop',$name))
        Stop-Service -Name $name -Force -ErrorAction SilentlyContinue
      } catch {}
      [void](Wait-ServiceState $name 'Stopped' 40)
    }
  }
  Stop-AgentProcesses
  Start-Sleep -Milliseconds 1000
  # Second pass — service recovery may have respawned the old PE.
  Stop-AgentProcesses
  Start-Sleep -Milliseconds 500
  if (Test-Path -LiteralPath $exe) {
    try { Copy-Item -Force -LiteralPath $exe -Destination $bak } catch {
      Write-Log ("backup Copy-Item warning: " + $_.Exception.Message)
    }
  }
  $moved = $false
  for ($i=0; $i -lt 15; $i++) {
    try {
      Move-Item -Force -LiteralPath $new -Destination $exe
      $moved = $true
      break
    } catch {
      Write-Log ("Move-Item attempt $($i+1) failed: " + $_.Exception.Message)
      try {
        Copy-Item -Force -LiteralPath $new -Destination $exe
        Remove-Item -Force -LiteralPath $new -ErrorAction SilentlyContinue
        $moved = $true
        Write-Log ("Copy-Item fallback ok on attempt $($i+1)")
        break
      } catch {
        Write-Log ("Copy-Item attempt $($i+1) failed: " + $_.Exception.Message)
      }
      Start-Sleep -Seconds 1
      Stop-AgentProcesses
    }
  }
  if (-not $moved) { throw "Move-Item/Copy-Item failed after retries" }
  $swapped = $true
  try { Unblock-File -Path $exe -ErrorAction SilentlyContinue } catch {}
  if (-not (Restart-AgentService -Exe $exe -Cfg $cfg -Dir $dir)) {
    throw 'agent failed to restart after binary replace'
  }
  $ver = (Invoke-VersionProbe $exe).Output
  Write-Log ("update ok version=$ver")
  Write-Result ("ok " + (Get-Date -Format o) + " version=" + $ver)
} catch {
  Write-Log ("update failed: " + $_.Exception.Message)
  Write-Result ("fail " + $_.Exception.Message)
  try {
    $exe = '%s'
    $cfg = '%s'
    $dir = Split-Path -Parent $exe
    $bak = $exe + '.bak'
    if ((Test-Path -LiteralPath $bak) -and ($swapped -or -not (Test-Path -LiteralPath $exe))) {
      Write-Log ("restoring backup (swapped=$swapped)")
      Copy-Item -Force -LiteralPath $bak -Destination $exe
    } else {
      Write-Log ("skip backup restore (swapped=$swapped exe_exists=$((Test-Path -LiteralPath $exe)))")
    }
    # 只重启我们真正动过的东西。这里绝大多数失败发生在**碰服务之前**——暂存文件不见了、
    # staging --version 跑不起来——此时 Agent 还好端端地在跑。无条件重启意味着每一次失败
    # 的升级尝试都会把一个健康的服务停掉再 --install-service 装回去；一旦那一步失败（没有
    # 管理员权限、SCM 被占），"下载失败"就升级成了"主机离线"。
    if ($swapped -or -not (Test-AgentRunning)) {
      [void](Restart-AgentService -Exe $exe -Cfg $cfg -Dir $dir)
    } else {
      Write-Log 'agent still running and nothing was swapped; leaving it alone'
    }
  } catch {}
  exit 1
} finally {
  # Keep helper script for a while on failure (operators read TEMP); delete on success.
  try {
    if (Test-Path -LiteralPath $altResult) {
      $t = Get-Content -LiteralPath $altResult -Raw -ErrorAction SilentlyContinue
      if ($t -match '^ok ') { Remove-Item -Force -ErrorAction SilentlyContinue $PSCommandPath }
    }
  } catch {}
  # Cleanup one-shot task (best-effort).
  try { & "$env:SystemRoot\System32\schtasks.exe" /Delete /TN 'AIOpsAgentSelfUpdate' /F 2>$null | Out-Null } catch {}
}
`, psSingleQuote(logPath), psSingleQuote(resultPath), psSingleQuote(altResult),
		psSingleQuote(exe), psSingleQuote(staging), psSingleQuote(cfgPath),
		psSingleQuote(exe), psSingleQuote(cfgPath))
}

// ---- quoting helpers (shared by the builders above) ----

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func psSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func quoteWinArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
