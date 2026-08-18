# Windows Agent 升级：换版成功了，服务却没起来——而操作台是绿的

承接 `2026-08-18-windows-agent-update-version-probe.md`。上一轮修好的是「升不上去」；
这一轮修的是它的反面：**升上去了，但那台机器其实已经在倒计时**。

## 一、差集在哪

助手换完二进制之后调用 `Restart-Agent`，它有三种结果：

| 返回 | 含义 |
|---|---|
| `service` | Windows 服务真的 Running —— 唯一的成功 |
| `usermode` | 二进制在跑，但只是一个游离进程：登录会话一结束、或机器一重启就没了，且没有任何东西会把它拉回来 |
| `failed` | 什么都没起来，助手自己回滚 |

服务端判定升级成功只看一件事：**主机上报的版本号有没有追上目标**。而 `usermode` 下
版本号**会**追上——那个游离进程照样上报——于是任务行判 `success` 并变绿。真正的后果要
等几小时甚至一次重启之后才出现：主机悄无声息地掉线，而任务表上写着「升级成功」。

助手其实早就把这件事写下来了：`Write-Result ("degraded … reason=service-not-running")`。
**只是服务端从来没读过它。**

## 二、这一轮之前留下的两处半成品

三态返回值是上一轮引入的，但收尾没做完，而且带着缺陷发布了（v1.1.70）：

1. **`cmd/server/agent_update_script.go` 的 `Restart-Agent` 漏了一条返回值**：找不到服务
   也找不到配置的那条 FATAL 分支仍然 `return $false`。PowerShell 的 `-eq` 由**左**操作数
   定类型，`$false -eq 'failed'` 会把右边的非空字符串转成 `$true`，结果是 `False`——
   调用方既不回滚，也不判 `usermode`，直接落到 `Write-Result ("ok …")`。**一台根本没
   重启成功的主机被报成升级成功**，正是这套设计要防的那件事。
   → 改成 `return 'failed'`，并加 `TestLegacyRestartAgentNeverReturnsABoolean`：逐行扫
   `Restart-Agent` 函数体，任何不是三态字符串的 `return` 直接红。

2. **`TestWindowsUpdateHelperLeavesHealthyAgentAloneOnPreSwapFailure` 是红的**：脚本里
   `[void](Restart-Agent)` 改成了 `$rbMode = Restart-Agent`，断言没跟着改。
   `go test ./...` 不过，而 `release.yml` 的 `go-gate` 是发版硬门禁——v1.1.70 是在门禁
   红着的情况下打的 tag。

## 三、这一轮做的事：让服务端读懂 degraded

`cmd/server/agent_update_degraded.go`：

- `windowsUpdateResultCommand()`——只取两条路径 result 文件（module 与 legacy）的**最后
  一行**。与 `windowsUpdateEvidenceCommand` 的区别是代价：那条是写失败判决前的一次性
  取证，这条要在**每台成功升级的 Windows 主机**上都跑一次，所以不碰日志正文。同样走
  `-EncodedCommand`（现网老 Agent 会弄坏带双引号的命令）。
- `degradedSwapVerdict(out, ver)`——纯函数，可测。**只认版本号对得上的那一条**：result
  文件是累积的，上一轮留下的 degraded 还躺在里面，拿它给这一轮定罪等于让一台已经修好的
  主机永远判不了成功。BOM 要剥两次——它在正文开头，也就是取证命令加的文件名前缀**之后**。
- `markHostUpdateVerified` 在**加锁之前**问一句（exec 最长 30 秒，而每一次任务读取
  ——包括操作台轮询——都要过同一把 `m.mu`）。

判决记成 `failed` 而不是新造一个状态，三个理由：

1. 它**就是**一次没做成的升级——那台机器会在下一次重启后彻底掉线；
2. 操作台「回滚失败主机」按钮筛的正是 `status === "failed"`，而回滚恰好是这里唯一正确的
   补救；红色计数、告警抬头也都是现成的；
3. 不会因此陷入重试循环：自动扫描按「版本号还落后」挑主机，而这台已经不落后了。

也正因为第 3 条，它**一次就到头**，永远累不成「连续同因失败」，`noteAgentUpdateFailure`
里那条抬头不会为它触发。所以 `markHostUpdateDegraded` 自己抬头：活动记录 + 告警 +
`reportPlatformFault`（开事件、AI 诊断、可转工单）。

另外补上 `helperProgressMarker`：它认 `running` / `ok ` / `fail `，不认 `degraded `——
而那是助手的**终局**结果。漏掉它，一台日志写不进去、只留下 result 的主机会被判成
「助手根本没起来」，白白再走一遍 legacy 救援，两个助手抢同一次换版。

## 四、不确定仍然不等于失败

`windowsSwapDegraded` 全部尽力而为：非 Windows、主机已离线、exec 失败、文件不存在
——一律返回 `""`，按原来的成功处理。这是上一轮的教训在这一轮的应用：探针把「读不出
退出码」当成「不可运行」，挡死了整个升级；这里绝不能把「问不出来」升级成「判失败」。

## 五、回归测试

| 测试 | 断言 |
|---|---|
| `TestDegradedSwapVerdictReadsHelperResult` | 助手的 degraded 判决被读出来，原文带回 |
| `TestDegradedSwapVerdictIgnoresAnEarlierRound` | 上一轮的 degraded 不给这一轮定罪 |
| `TestDegradedSwapVerdictSurvivesBOMAfterTheFilePrefix` | BOM 在文件名前缀之后也要剥掉；版本号带不带 `v` 都认 |
| `TestDegradedSwapVerdictLeavesASuccessfulSwapAlone` | `ok` 不被读成 degraded；读不到 result / 版本未知时按成功处理 |
| `TestWindowsUpdateResultCommandStaysCheapAndQuoteFree` | 走 `-EncodedCommand`、无引号、不拉日志正文 |
| `TestLegacyRestartAgentNeverReturnsABoolean` | `Restart-Agent` 只返回三态字符串 |
| `TestHelperProgressMarkerSurvivesUTF8BOM` | 新增 `degraded ` 用例 |
