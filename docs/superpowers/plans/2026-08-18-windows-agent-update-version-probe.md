# Windows Agent 升级：把「读不出退出码」当成「二进制不可运行」

承接 `2026-08-16-windows-agent-update-visibility.md`。那一轮的结论是：四条读码可证的
缺陷都修完了，**剩下的必须看真实主机的记录**，否则任何 Agent 侧改动都还是在押假设。

这一轮拿到了那份记录。它一句话就把原因指了出来。

## 一、现场证据

`server11`，v0.19.98 → v0.19.100，`C:\ProgramData\aiops-agent-update\aiops-agent-legacy-update.log`
里连续五次、每次间隔约 6 分钟，逐字相同：

```
[2026-08-18T09:02:53] helper start pid=15276 exe=C:\Program Files\AIOps Agent\aiops-agent.exe
[2026-08-18T09:02:53] server-pinned sha256=20248e0b78a7991feee2cf95faffb2323202457110188bed68c484568afe496a
[2026-08-18T09:03:31] downloaded aiops-agent.exe sha=20248e0b78a7991fe…（与 pin 一致）
[2026-08-18T09:03:32] update failed: staging not runnable (exit=): v0.19.100
[2026-08-18T09:03:32] agent still running and nothing was swapped; leaving it alone
```

第四行是全部答案：

- 抛这句话的是 `throw ("staging not runnable (exit="+$probe.ExitCode+"): "+$probe.Output)`；
- **括号里是空的**——`$probe.ExitCode` 是 `$null`，不是某个非 0 的数；
- **冒号后面是 `v0.19.100`**——那是探针自己从这个二进制读回来的版本号。

也就是说：这个二进制下载完整（sha 与服务端 pin 一致）、在这台机器上**跑起来了**、
**打印了正确的新版本号**，然后被判成「不可运行」，升级在换版之前被自己挡下。
主机毫发无损地留在旧版本，六分钟后再来一遍。五次日志一模一样，说明这不是偶发。

## 二、为什么退出码会是 $null

```powershell
$p = Start-Process -FilePath $File -ArgumentList '--version' -NoNewWindow -PassThru …
$p.WaitForExit()
return [pscustomobject]@{ ExitCode = $p.ExitCode; Output = $txt }
```

`Start-Process -PassThru` 交回来的 `Process` 对象并不总是「由我们启动」的那个句柄。
在进程创建被拦截/包装的主机上（EDR 是最常见的一类），读 `.ExitCode` 会抛
`InvalidOperationException`。关键在于 **PowerShell 的属性访问会把这个异常吞掉并返回
`$null`**——它不会进 `catch`，不会留下任何痕迹，只是安静地变成空值。

于是判据 `if($probe.ExitCode -ne 0)` 就成了 `if($null -ne 0)`，**恒真**。

真正致命的不是那个异常，而是**判据把「无法判定」等同于「判定为失败」**：探针要证明
的是「它能起来」，而它把版本号打出来了，这件事本身已经证明了。

## 三、同一个缺陷长在两条升级路径上

`Invoke-VersionProbe` 在两处各有一份**逐字相同**的副本：

| 位置 | 用途 |
|---|---|
| `cmd/agent/module_agent_update_scripts.go` | Agent 自带的模块助手（第一条路径） |
| `cmd/server/agent_update_script.go` | 服务端下发的 legacy 救援脚本（兜底路径） |

所以现场看到的是：模块助手判死 → 退到 legacy 救援 → 救援用**同一个错判**再判死一次。
两条路径给出的原因还长得一模一样，反而让人以为是同一次失败的重复输出。

修复因此不是改两遍，而是**把它收成一处**：`shared/agent_update_probe.go` 的
`WindowsVersionProbePS`，两个包都从这里取（`shared` 本就同时被 cmd/server 与 cmd/agent 引用）。

## 四、修复

1. **退出码由我们自己持有的 `Process` 对象来读**：`New-Object Diagnostics.ProcessStartInfo`
   + `New-Object Diagnostics.Process`，`UseShellExecute=$false` 直接重定向管道。句柄是
   自己 `Start()` 的，`.ExitCode` 不再依赖 `Start-Process` 交回来的对象。
2. **显式区分「读不出来」与「非 0」**：新增 `Test-ProbeRunnable`——
   - 退出码可读 → 按 `-eq 0` 判；
   - 退出码为 `$null` → **以输出为准**：打印出形如 `\d+\.\d+` 的版本号就算跑起来了；
   - 起都起不来（`Start()` 抛异常）或超时 → 退出码是 `-1`，照旧判死。
   这与 Go 侧 `agentBinaryVersionProbe` 早就写下的原则一致：
   *"Do not block the upgrade on an inconclusive probe."*
3. 顺带与 Go 侧探针对齐：给被探进程设 `AIOPS_UPDATE_PROBE=1`，走「只打印版本就退出」
   的快路径（`cmd/agent/main.go:143`）。
4. 探针改为先把两条管道读干净再 `WaitForExit`——反过来的话管道一满就是双向死等。

## 五、回归测试

| 测试 | 断言 |
|---|---|
| `TestWindowsVersionProbePSIsSpliceSafe` | 探针文本无反引号、无 `%`（它会被拼进原始字符串字面量，其中一处还落在 `fmt.Sprintf` 的格式串里） |
| `TestWindowsVersionProbePSKeepsBothHalves` | 保留取值与判定两半；代码行不得再出现 `Start-Process`；判定必须先看退出码是否可读 |
| `TestLegacyHelperAcceptsProbeWithUnreadableExitCode` | 救援脚本走 `Test-ProbeRunnable`，且不再出现 `$probe.ExitCode -ne 0` |
| `TestLegacyHelperUsesSharedVersionProbe` / `TestWindowsHelperUsesSharedVersionProbe` | 两条路径都内嵌 `shared.WindowsVersionProbePS`，谁再抄一份就红 |

## 六、这次留下的通用教训

与「主机历史曲线看不到」那一轮（`2026-08-14` / `2026-08-15`）合在一起，是同一条：

> **不确定不等于失败，没画出来不等于没采到。**
> 判据取不到值时，不要按最坏结果处理；数据展示不到位时，先怀疑展示层丢了它，
> 而不是先怀疑没采到。

对应到代码上的三条硬规矩，其它模块也该按它体检一遍：

1. 任何「判失败/判不可用」的分支，都要能回答「取不到判据时会走到这里吗」；
2. 证据（日志尾巴、错误原文、采样点）在抵达人眼之前不得被截断或判空丢弃；
3. 同一段逻辑不得存在第二份副本——尤其是跑在目标主机上、CI 摸不到的脚本。
