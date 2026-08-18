package shared

// Windows 升级助手的「换版前探针」——服务端的 legacy 救援脚本与 Agent 自带的模块助手
// **共用同一份**。此前两边各存一份逐字相同的副本，于是同一个缺陷同时长在两条升级路径
// 上：模块助手判死之后退到 legacy 救援，救援用同样的判据再判死一次，主机每 6 分钟重来
// 一遍，永远升不上去，而两条路径给出的原因还长得一模一样，反而让人以为是同一次失败。
//
// 放在 shared 是为了让「改一处忘另一处」不可能发生：两个包都从这里取。
//
// 当年的缺陷（现场证据见 docs/superpowers/plans/2026-08-18-windows-agent-update-version-probe.md）：
// 退出码是用 `Start-Process -PassThru` 交回来的对象读的，而那个对象在进程创建被拦截/
// 包装的主机上（EDR 最常见）读 .ExitCode 会抛 InvalidOperationException——PowerShell 的
// 属性访问会把异常**吞掉并返回 $null**，不进 catch、不留痕迹。于是判据
// `$probe.ExitCode -ne 0` 变成 `$null -ne 0`，恒真：
//
//	downloaded aiops-agent.exe sha=<与服务端 pin 一致>
//	update failed: staging not runnable (exit=): v0.19.100
//
// 括号里是空的，冒号后面是探针自己读回来的版本号——二进制跑起来了、版本也对，却被判成
// 「不可运行」。真正致命的不是那个异常，而是判据**把「无法判定」等同于「判定为失败」**。
//
// 现在：退出码由我们自己 Start() 的 Process 对象来读；读不出来时以输出为准（打印出了
// 版本号就说明它跑起来了），与 Go 侧 agentBinaryVersionProbe 的既定原则一致
// （"Do not block the upgrade on an inconclusive probe"）。

// WindowsVersionProbePS 提供 Invoke-VersionProbe 与 Test-ProbeRunnable 两个 PowerShell
// 函数。三条硬约束，各有一条测试守着：
//   - 纯 ASCII：脚本会被下发到目标主机并按 ASCII 校验（TestWindowsUpdateHelperIsASCII），
//     中文说明一律留在上面的 Go 注释里；
//   - 不得出现反引号：文本要拼进 Go 原始字符串字面量；
//   - 不得出现 %：其中一处拼进 fmt.Sprintf 的格式串，% 会被当成格式动词。
const WindowsVersionProbePS = `# Running an agent binary's --version needs a hard timeout: the probe target can
# turn into a daemon at any moment, and an unbounded pipe read would hang the
# helper forever right before the swap, leaving the host silently on the old
# version with nothing to report.
#
# The exit code MUST come from a Process object we started ourselves. The object
# handed back by Start-Process -PassThru cannot always be queried for ExitCode
# (process creation intercepted or wrapped, e.g. by EDR); the getter throws, and
# PowerShell swallows property-getter exceptions into $null. A judgement written
# as "-ne 0" then fires on a perfectly good binary, which is how a staged agent
# that had already printed its version was rejected as "not runnable".
function Invoke-VersionProbe {
  param([string]$File,[int]$TimeoutSec = 20)
  $psi = New-Object Diagnostics.ProcessStartInfo
  $psi.FileName = $File
  $psi.Arguments = '--version'
  $psi.UseShellExecute = $false
  $psi.CreateNoWindow = $true
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  try { $psi.WorkingDirectory = (Split-Path -Parent $File) } catch {}
  # Same as the Go-side probe: force the callee onto its print-and-exit fast path.
  try { $psi.EnvironmentVariables['AIOPS_UPDATE_PROBE'] = '1' } catch {}
  $p = New-Object Diagnostics.Process
  $p.StartInfo = $psi
  try {
    [void]$p.Start()
    # Drain both pipes before waiting. The other order deadlocks the moment a
    # pipe buffer fills: the child blocks on write, we block on WaitForExit.
    $txt = ('' + $p.StandardOutput.ReadToEnd() + $p.StandardError.ReadToEnd()).Trim()
    if (-not $p.WaitForExit($TimeoutSec * 1000)) {
      try { $p.Kill() } catch {}
      return [pscustomobject]@{ ExitCode = -1; Output = ("version probe timed out after " + $TimeoutSec + "s") }
    }
    $code = $null
    try { $code = [int]$p.ExitCode } catch { $code = $null }
    return [pscustomobject]@{ ExitCode = $code; Output = $txt }
  } catch {
    return [pscustomobject]@{ ExitCode = -1; Output = $_.Exception.Message }
  } finally {
    try { $p.Dispose() } catch {}
  }
}
# Test-ProbeRunnable answers one question: can this binary start on this host?
#
# Exit code 0 says yes. An exit code we could not read says NOTHING, and must not
# be read as a no: the probe exists to prove the binary starts, and a printed
# version string already proves it. What must still be rejected: a non-zero exit,
# a binary that could not be started at all or timed out (exit code -1), and one
# that printed nothing we can recognise.
function Test-ProbeRunnable {
  # Parameter name must not differ only by case from the caller's variable:
  # PowerShell is case-insensitive, so $Probe and $probe are ONE variable and the
  # binding would silently clobber the caller's. TestPowerShellScriptsHaveNoCaseColludingVariables
  # guards this repo-wide.
  param($ProbeResult)
  if ($null -eq $ProbeResult) { return $false }
  if ($null -ne $ProbeResult.ExitCode) { return ([int]$ProbeResult.ExitCode -eq 0) }
  return ((('' + $ProbeResult.Output).Trim()) -match '[0-9]+\.[0-9]+')
}
`
