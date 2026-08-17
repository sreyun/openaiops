# Windows Agent 升级：这一轮修的是「为什么修了四轮还是说不清」

承接 `2026-08-12-pg-write-amplification-and-windows-update.md` 与
`2026-08-14-windows-agent-update-tls-pinning.md`。

那两轮修的都是 Agent 侧助手脚本本身的缺陷（`$Args` 自动变量、换完二进制起不来、
兜底路径的证书链）。这一轮先问了一个不同的问题：**既然证据链路早就修好了，为什么
「Windows 升不上去」仍然是一个谁也说不清原因的现象？**

读完全链路后，答案是：证据一路运到了操作台，最后一步被前端切掉了；而在那之前，
操作台早就已经先弹了一次「轮询超时」，把一次**正在正常进行**的校验读成了失败。

---

## 一、控制台的轮询预算只有服务端阶梯的三分之一（主因）

服务端为一台主机准备的完整阶梯是：

```
module 助手 verify（5 分钟）
  → legacy 救援 exec（最长 600s）
    → 救援 verify（再 5 分钟）
```

`agentUpdateJobFinalizeWindow` 就是按这条链路算的——**22 分钟**。而出厂控制台
（`web/js/agent-update.js`）的轮询预算是 `200 次 × 2s ≈ 6.5 分钟`，注释还写着
"covers pending_verify"，实际上连第一段都没走完。

**为什么这件事只咬 Windows**：两个平台其实都会进 `pending_verify`（模块的成功输出里
就有 "restart scheduled"），但含义完全不同：

- **Linux**：模块返回时二进制**已经换好了**——`os.Rename` 允许覆盖运行中的 ELF，
  剩下的只是把服务拉起来。版本号通常几十秒内追上，第一段 verify 就收摊，前端根本
  碰不到上限。
- **Windows**：运行中的 PE 改不了，模块返回时**一个字节都还没换**，换版要等助手在
  Agent 被杀之后的独立进程里完成。助手一旦没做成，就要走 legacy 救援——而整条救援
  阶梯按构造只对 Windows 开（`rescueWindowsAgentUpdate` 对非 Windows 直接返回 false）。

也就是说：**超出 6.5 分钟的那一段，永远只有 Windows 主机会走到**。服务端还在校验、
甚至救援脚本正跑到一半，前端已经弹了一条红色的「更新任务轮询超时」。

操作台上看到的「Windows Agent 升不上去」，有很大一部分就是这么来的。

修复：
- 预算提到 `720 × 2s = 24 分钟`，覆盖服务端 22 分钟的整条阶梯；
- 文案改成「前端已停止轮询；服务端仍在校验该任务」，并去掉 `err` 色调——前端不看了
  不等于升级失败了；
- 新增 `TestConsolePollBudgetCoversServerVerifyLadder`：直接读 `agent-update.js` 解析
  轮询次数与间隔，与 `agentUpdateJobFinalizeWindow` 对比。以后谁改了阶梯长度、忘了
  改前端，CI 就会红。

## 二、失败详情把唯一有用的那一段切掉了

`windowsUpdateEvidence` 会把主机本地助手日志的尾巴取回来，Agent 也会用
`takeUpdateDiagnostics` 把上一轮的记录捎回来。两者都**拼在 message 的结尾**，服务端
按 1200 字封顶。

而前端：

| 位置 | 原来 |
|---|---|
| `showAgentUpdateJobDetail`（任务详情） | 压成一行再 `slice(0, 160)` |
| `loadAgentAutoUpdateJobs`（设置页最近任务） | `slice(0, 400)` |

160 字连服务端那句英文开场白都装不下，证据一个字都到不了人眼前。这就是「每一轮修复
都是在没有证据的情况下押一个假设」的直接原因。

修复：两处都不再截断；任务详情按行缩进展开（保留助手日志的换行），设置页加
`white-space:pre-wrap`。回归测试 `TestConsoleJobDetailDoesNotTruncateHostMessage` /
`TestSettingsJobListDoesNotTruncateHostMessage` 禁止 `h.message … slice(` 回来。

## 三、UTF-8 BOM 打掉了「助手起来了没有」的三个标记

助手用 `Set-Content -Encoding UTF8` 写 result 文件——在 **Windows PowerShell 5.1** 里
这个编码的意思是「UTF-8 **带 BOM**」（无 BOM 要到 PowerShell Core 6+ 才是默认）。

Go 侧 `helperProgressMarker` 用 `HasPrefix` 判 `running` / `ok ` / `fail `，带着 BOM
**在任何一台真实 Windows 主机上都永远匹配不到**。整套判定于是退化成只剩日志里的
`helper start` 一条（那条用 `Contains`，BOM 伤不到它）。日志一旦写不进去——计划任务
退到受限用户主体、EDR 锁住 ProgramData——就再没有任何东西能证明助手已经在跑，一次
本来正常的升级会被判成「助手没起来」，白白多走一遍 legacy 脚本。

同一个 BOM 还会让捎回操作台的诊断文本以一串乱码开头，而那段文字的全部价值就是给人读。

修复：`helperProgressMarker` 与 `tailFileForDiagnostics` 都先剥 BOM。函数从
`module_agent_update_windows.go` 移到无 build tag 的 `module_agent_update_scripts.go`
（该文件头注释里写明的原因：让 Linux CI 也能断言 Windows 侧的纯字符串逻辑）。

## 四、软重试可能插进一次仍在换版的升级

`inFlight` 的时间戳只在**入队**和**进入 pending_verify** 时各打一次，而完整阶梯最长
20 分钟，远超 `agentUpdateSoftRetrySec`(360s)。`hostHasPendingAgentUpdate` 挡住了大部分
情况，但它 `j.Status == "done"` 就跳过——而 `finalizeAgentUpdateJobWhenVerified` 到点
会**无条件**把 job 标成 done，哪怕还有主机挂在 `pending_verify`（救援仍在跑）。此后
自动扫描看到「还落后」就会再下发一次。

Linux 上顶多多下一遍二进制（换版是 rename，systemd 还会把它拉起来）；Windows 上是
两个助手抢同一次换版——一个刚把服务停掉，另一个正在覆盖同一个 PE，主机最后停在
「服务已停止」。这是最难复现、也最恶劣的一类。

修复：`holdHostInFlight` 心跳，覆盖 exec 阶梯与 verify/救援阶梯两段。心跳只刷新
**已存在**的条目（`touchInFlightIfPresent`），否则一次晚到的刷新会把刚放行的主机
重新冻结整个硬冷却期——那等于把并发缺陷换成拒绝升级的缺陷。

## 五、回归测试

| 测试 | 断言 |
|---|---|
| `TestConsolePollBudgetCoversServerVerifyLadder` | 前端轮询预算 ≥ `agentUpdateJobFinalizeWindow` |
| `TestConsoleJobDetailDoesNotTruncateHostMessage` | 任务详情不再截断 `h.message` |
| `TestSettingsJobListDoesNotTruncateHostMessage` | 设置页最近任务同上 |
| `TestHelperProgressMarkerSurvivesUTF8BOM` | 三个标记带 BOM 也认；`scheduled` 占位仍不算「已启动」 |
| `TestTailFileForDiagnosticsStripsBOM` | 捎回操作台的证据不带 BOM |
| `TestHoldHostInFlightBlocksSoftRetryWhileWorking` | 升级进行中软重试被挡 |
| `TestHoldNeverResurrectsAReleasedHost` | 心跳不会把已放行的主机重新冻结 |
| `TestHoldReleaseIsIdempotent` / `…DegradesSafely` | release 可重复调用；无 hostID / 无管理器不炸 |

## 六、还需要现场证据的部分

上面四条都是**读码可证**的缺陷。Agent 侧助手在换版那一刻还可能死在哪里，则必须看
真实主机的记录，不能再猜。升级到本版本后，一台失败的 Windows 主机在

**设置 → Agent 自动升级 → 最近的升级任务**

那一行的「消息」列里会完整显示：

- 服务端取回的 `| host evidence: …`（`ProgramData\aiops-agent-update\` 下 result 与
  log 的尾部 25 行）；
- Agent 捎回的「上一轮升级助手留下的记录」。

拿到这段文字，才谈得上定位下一处缺陷；在此之前的任何 Agent 侧改动都仍然是押假设。
主机若在换版后彻底离线，消息里会直接写明「host is offline after the swap」，此时
需要人到那台机器上读 `ProgramData\aiops-agent-update\aiops-agent-update.log`。
