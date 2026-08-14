# Windows Agent 自动升级：收尾三处缺陷 + HTTPS 证书链

承接 `2026-08-12-pg-write-amplification-and-windows-update.md`。那一轮修掉了 `$Args`
自动变量把助手吊死在换二进制之前的问题；这一轮修掉的是**换完二进制之后**主机起不来，
以及 HTTPS 域名部署下**下载这一步根本走不通**。

前者会让主机带着一个崭新的、从未跑起来过的二进制永久离线，后者会让整条兜底链路在第一步
就失败——两者的现象都是「版本号不动」，与上一轮的症状难以区分，所以要一起收掉。

---

## 一、换完二进制之后起不来（两处，已修）

### 1.1 已注册的服务，只在「exe 旁边有配置」时才会被启动

助手停服务 → 换 PE → 重启。原来的重启阶梯是：

```
if (有服务 && 有配置) { --install-service --config <cfg>; 若失败则 sc start / Start-Service }
else                  { user-mode 分支 }        <-- user-mode 又以「没有配置」为由拒绝启动
```

`sc start` 被**嵌在**配置判断内部。而 `--install-service` 写进 ImagePath 的是
`"<exe>" --service --config "<绝对路径>"`（`cmd/agent/service_windows.go:106`），配置文件
**不必**在 exe 旁边。于是「配置放在别处」的安装：服务已停、二进制已换、`$Cfg` 为空 →
直接掉进 user-mode → user-mode 拒绝启动 → 主机永久离线。

修复：把「直接启动已注册的服务」提到配置判断**之外**。已注册的服务自带绝对路径的
`--config`，直接 start 永远是正确的恢复动作。

- `cmd/agent/module_agent_update_scripts.go`（module 主路径助手）
- `cmd/server/agent_update_script.go`（legacy 兜底助手）

### 1.2 助手不知道进程真正在用哪份配置

module 路径原来用 `resolveAgentConfigBesideExe(dir)` 猜配置路径。同样的道理：
`--config` 可以指向任意绝对路径。

修复：`main()` 启动时把实际解析出的配置路径记进 `agentActiveConfigPath`（绝对化），
`agentUpdateConfigPath()` 优先用它，取不到再退回「exe 旁边」。

legacy 助手侧对应的做法是直接从服务 ImagePath 里正则取 `--config`
（`Get-ConfigFromCommandLine`），拿不到再退回猜测。

### 1.3 助手把自己所在的计划任务给结束了

`schtasks /End /TN AIOpsAgentLegacyUpdate` 原本写在助手内部的 `Clear-StuckSelfUpdateTask`
里，而助手**正是以该任务实例的身份运行的**——`/End` 会连整个任务进程树一起终止。那行调用
恰好落在停服务、换二进制之前，于是每一次救援都死在那里：日志停在 `staging --version`，
主机停在旧版本。这和上一轮 `$Args` 的症状一模一样。

修复：助手只清理 `AIOpsAgentSelfUpdate`（老版本 Agent 自己注册的一次性任务）；清理自己
这条任务的陈旧实例交给**引导脚本**，在 `Start-ScheduledTask` 之前做——计划任务默认
`MultipleInstances=IgnoreNew`，上一轮吊死的实例不清掉，`Start-ScheduledTask` 会静默地
什么都不做。

回归测试逐行扫描助手正文，禁止任何非注释行同时出现 `AIOpsAgentLegacyUpdate` 与
`/End` / `Stop-ScheduledTask` / `Unregister-ScheduledTask`。

---

## 二、HTTPS + 域名部署：兜底路径的证书链问题

### 2.1 问题

**Agent 的 TLS 配置只对 Agent 自己的 Go HTTP 客户端生效。**

| 路径 | 下载方 | 认 `ca_cert` / `tls_skip_verify` |
|---|---|---|
| module（新版 Agent） | Go，`reportTransport` | ✅ |
| Windows 引导脚本 | `Net.WebClient` | ❌ 走 Windows 根证书库 |
| Windows 助手正文 | `Net.WebClient` | ❌ 同上 |
| Linux/macOS 兜底 | `curl` / `wget` | ❌ 走系统 CA 包 |

兜底路径在主机上用 PowerShell / curl 下载，完全用不上 `config.yaml` 里的 `ca_cert`。
面板发布在真实域名 + HTTPS 上时，下面几种情况会让它在**第一步下载**就失败：

- **Server 2012 / 2008 R2 的根证书库**里没有 ISRG Root X1 之类的新根，一条完全合法的
  Let's Encrypt 链在这里就是不受信任；这恰恰是最需要兜底路径的那批机器。
- 老 Linux 上过期的 DST Root CA X3。
- 私有 / 企业 CA、做 TLS 审计的中间代理。
- `.NET < 4.7` 默认 `SecurityProtocol` 不含 TLS 1.2（`Tls12` 枚举在 4.0 上根本不存在，
  所以只能用数值 3072）。

结果：module 路径能升级的机器一切正常，而**最需要兜底的老机器**在兜底的第一步就死掉。

### 2.2 为什么不能简单地「关掉校验」

助手下载的是**会以 LocalSystem 身份在全机群运行的二进制**。它的完整性凭证是同一台服务器上
的 `/dl/<bin>.sha256`——和二进制走**同一条连接**。中间人把两半一起换掉，校验等于没做。
所以只要还依赖 `.sha256`，就绝不能放松证书校验。

### 2.3 修法：把完整性凭证挪到带外

服务端本来就有产物文件，能直接算摘要（`agentDistSHA256`，复用 `cachedFileSHA256` 缓存）。
把摘要**写进生成的脚本**，经 Agent 已鉴权、已验证证书的 exec 通道下发：

```
服务端 → (exec 通道，TLS 已验证) → 引导脚本内含 $D='<64 hex>'
                                    → -Sha 传给助手 → $Pin
                                    → 下载后按 $Pin 校验
```

有了带外摘要，「下载链路是否可信」与「装上去的二进制是否正确」就彻底解耦了。于是：

- **严格校验优先**：先按正常 TLS 下载，成功就到此为止（绝大多数主机走这条）。
- **失败且持有带外摘要**时，才降级重试一次（PowerShell 临时设
  `ServerCertificateValidationCallback`，`finally` 里还原；curl `-k` / wget
  `--no-check-certificate`），并把警告打进日志与 exec 输出。
- **没有带外摘要**（服务端 dist 目录里没有这个产物）时，**不降级**，直接报错并提示去修
  证书链或 `ca_cert`。此时脚本仍回退到下载 `.sha256`，而那一步永远走严格校验。

这不仅解决了证书问题，本身就是一次**安全增强**：即使证书完全正常，原来的
「`.sha256` 与二进制同链路」也挡不住在途攻击，现在挡得住了。

顺带把 TLS 1.3（12288）也加进 `SecurityProtocol`，每个标志位各自 `try`——12288 在
`.NET < 4.8` 上会抛异常，写在一起会把已经设好的 3072 一起丢掉。

### 2.4 引导脚本为什么可以无条件降级

引导脚本只下载**助手脚本**一个文件，而助手的 SHA-256（`$H`）在服务端生成引导脚本时就写死
在正文里。摘要对不上照样 `throw`。回调只影响引导脚本自己这个进程，而它紧接着就退出了；
真正下载二进制的助手在另一个进程里，自行判断是否降级。

### 2.5 长度预算

引导脚本经 `cmd.exe /c` 下发，硬上限 8191 字符（见 `windowsUpdateBootstrapMaxLen`）。
加进去的是 64 字符摘要 + 降级重试，为此腾出空间：

- `$R="$env:SystemRoot\System32"` 复用三处
- `Register-ScheduledTask` 改用 splatting，去掉重复的参数列表
- 成功信息缩短

`TestLegacyWindowsUpdateCommandFitsWindowsShellLimit` 现在按**带摘要**（生产形态、也是更长
的那种）来量。

---

## 三、回归测试

| 测试 | 断言 |
|---|---|
| `TestWindowsUpdateHelperNeverEndsItsOwnScheduledTask` | 助手正文不含结束自身任务的调用；引导脚本的 `/End` 在 `Start-ScheduledTask` 之前 |
| `TestWindowsUpdateHelperStartsExistingServiceWithoutConfig` | `sc start` 在配置判断**之外**；`Get-ConfigFromCommandLine` 存在 |
| `TestWindowsHelperStartsRegisteredServiceWithoutConfig`（agent 侧） | 同上，module 助手 |
| `TestAgentUpdateConfigPathPrefersTheLiveConfig` | 优先用进程实际配置；路径失效时回退 |
| `TestSanitizeSHA256Hex` | 非法摘要降级为「无 pin」，不可注入 |
| `TestLegacyScriptsCarryTheServerPinnedDigest` | 三段脚本都带上服务端摘要；无摘要时渲染成空 |
| `TestWindowsUpdateHelperRelaxesTLSOnlyBehindThePin` | 降级重试在 `-not $Pin -> throw` 之后；`finally` 还原回调；`.sha256` 回退路径不降级 |
| `TestUnixLegacyScriptRelaxesTLSOnlyBehindThePin` | 同上；且重试写成 `if` 而非 `cmd && return`（`set -e` 会在回退之前退出脚本） |
| `TestWindowsUpdateScriptsEnableModernTLS` | 3072 / 12288 都在 |
| `TestLegacyWindowsUpdateCommandFitsWindowsShellLimit` | 带摘要的引导脚本仍在预算内 |

## 四、升级顺序

仍然是**先升服务端**。这一轮的证书修复同样只存在于服务端生成的脚本里，服务端一升级，
现网所有老 Agent 的兜底路径立刻具备这个能力，不需要人工重装。
