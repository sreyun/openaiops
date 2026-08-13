# PG 写放大治理 + Windows 自动升级修复：迁移与兼容

日期：2026-08-12

本文回答三个问题：**历史数据怎么迁移**、**老版本升到新版本如何兼容**、**PG 里的数据要不要搬到 VM**。

---

## 一、要不要把 PG 的数据迁移到 VictoriaMetrics？

**不需要，而且不能。** PG 里没有任何 VM 缺失的时序数据——一条都没有。

每一个指标样本在产生的那一刻就已经写进 VM 了：

| 数据 | 写入 VM 的位置 |
|---|---|
| 主机基础指标（CPU/内存/磁盘/网络/负载/GPU/连接数…） | `agent_api.go:178` → `vm.enqueue(..., rep.Metrics)`，每次上报 |
| 硬件/BMC（Redfish、温度、风扇、功耗、健康分） | `hardware_netflow.go` → `pushHardware` / `pushHardwareLabeled` |
| SNMP 设备 | `snmp.go:201` → `pushRawLine` |
| Hyper-V | `hyperv.go:367` → `pushRawLine` |
| 拨测 / API 监控 | `check.go` → `enqueueCheck`，`apimon.go` → `enqueueAPI` |
| Prometheus 抓取 | `promscrape.go:379` → `writeLabeled` |

PG 的 `hosts.data` 里确实有指标（`Host.Latest`，一个完整样本，实测 0.8–1.9 KB），但它是**同一个数据的最新一个点**，作用是重启后控制台不至于空白、离线主机还能显示最后已知状态。多级降采样历史环（`histRaw` / `hist1m` / `hist5m`）在 `store.go:163` `exportHosts()` 里被显式置 nil，**从来没有落过 PG**。

所以 9.3 GiB 的 pg-data 不是 9.3 GiB 的指标。它是大约 1 MB 的「最新样本」行，被每天重写约 290 万次留下的**死元组**。把它搬到 VM 等于搬垃圾——VM 里已经有了那份数据的完整历史，压缩后只有 93 MiB。

正确的处理是两步，都不涉及数据搬迁：

1. **别再产生**——写路径修复（见第二节）
2. **把已经积下的空间还回去**——`-pg-reclaim` 一次性回收（见第三节）

### 唯一真正会无限增长的 PG 表：`flow_records`

`cleanupFlowRecords()`（`pgstore.go:4229`）注释写明「Flow 明细现在**永久保留**」，7 天删除已被移除，归档靠 DROP/DETACH 月分区——但代码里没有任何地方自动 DROP。NetFlow 明细是 (src_ip, dst_ip, port) 这种高基数维度数据，**放 VM 会把索引打爆**，留在 PG 分区表是正确选择；但需要一个分区保留策略。

`-pg-report` 能直接区分这两类：`flow_records` 大而死元组占比低 = 真实数据，需要的是保留策略；`hosts`/`kv_state` 小而死元组占比高 = 膨胀，需要的是回收。**先跑诊断再决定，不要猜。**

---

## 二、写路径修复：改了什么，为什么不需要 schema 迁移

### 改动

| 位置 | 改动 |
|---|---|
| `pgstore_writecache.go` | 写前内容哈希去重；哈希只在**事务提交成功后**记账 |
| `saveHosts` | 「DELETE 全表 + 重插」→ 只写变化行 + 只删差集；快/慢双周期 |
| `saveIncidents` / `saveTickets` / `saveOnCallPages` / `saveChangeRecords` | 同上 |
| `saveKVIfChanged` | 9 个 blob 内容没变就不写 |
| `pgFlushHeavyEveryNth` | 日志 blob + 带指标的整行 hosts 回写，从 30 秒放慢到 5 分钟 |
| 迁移 v13 | 给周期重写的表设更贴身的 autovacuum 参数（含 TOAST 副表） |

`hosts` 的快慢双周期是这次的关键：`Host.Latest` **每个上报周期都在变**，所以内容哈希去重对它完全无效。快周期（15s）只比对「身份摘要」（`hostIdentityDigest`：ID/主机名/OS/IP/版本/分类/指纹…，不含 Latest/Custom/LastSeen），指标漂移一概不写；慢周期（5min）与退出前那次才做整行回写。

### 为什么不需要数据迁移

**表结构一个字节都没改。** `hosts(id, data)` 的列定义、JSONB 的形状、`loadHosts` 的解析全部不变。改的只是「什么时候写」，不是「写什么」。

- **升级**：新版本启动 → 写缓存为空 → `needsSeed` 从 PG 播种 id 集合 → 首轮刷写把所有主机整行同步一次 → 之后进入按需写。无需任何人工步骤。
- **回退**：老版本读同样的行，完全正常。老版本只是会恢复「每 15 秒整表重写」的行为。
- **停机期间被删掉的主机**：`needsSeed` → `seed` → `missingIDs` 这条路径就是为它准备的，重启后第一轮刷写会把它们镜像删除。已有集成测试 `TestSaveHostsMirrorsDeletes` 覆盖。
- **迁移 v13 是幂等的**：`ALTER TABLE ... SET (...)` 只改 reloptions，跑几次结果一样；表不存在时 `to_regclass` 返回 NULL 直接跳过。

### 数据新鲜度的代价（明确说明）

慢周期把「非正常退出时最多丢失的指标快照」从 15 秒放宽到 5 分钟。影响面：

- **在线主机**：无影响。重启后几秒内新上报就覆盖了。
- **离线主机**：最多显示 5 分钟前的最后已知状态。主机一旦离线，它的 Latest 就不再变化，下一次慢周期刷写会把最终值落库——所以「最后已知状态」本身不会丢。
- **正常退出**：无影响。`main.go:453` 的最后一次 flush 一定是 heavy。
- **审计**：完全无影响。每条日志行已经由 `appendLog → appendAudit` 同步进 `audit_log` / `audit_log_p` 哈希链，kv_state 里的 `logs` blob 只是重启回填内存环的缓存。

---

## 三、历史膨胀数据的回收

写路径修复只保证「以后不再涨」。已经积下来的死元组需要一次性回收——PG 的 autovacuum 只把空间标记为「可复用」，**不会把堆文件还给操作系统**。

```bash
# 1) 只读诊断：每张表多大、死元组多少、autovacuum 何时跑过、预计能回收多少
aiops-server -pg-report

# 2) 一次性回收（维护窗口执行）
aiops-server -pg-reclaim

# 只回收指定表
aiops-server -pg-reclaim -pg-reclaim-tables=hosts,kv_state
```

两个子命令都读 `AIOPS_POSTGRES_DSN`，跑完即退出，不启动任何服务。

**为什么做成显式子命令而不是启动时自动执行**：`VACUUM FULL` 会持 ACCESS EXCLUSIVE 锁并重写整表，在几 GB 的库上可能要几分钟，期间该表读写全部阻塞。把它塞进启动路径等于让每次重启都变成一次不可预期的停机。什么时候承担这个锁，是运维的决定。

**执行前必须确认**：磁盘剩余空间要大于最大单表体积——VACUUM FULL 期间新旧两份并存。

`-pg-reclaim` 只挑「≥16 MiB 且死元组占比 ≥20%」的表，已经紧凑的表不会被白白锁一遍。单表失败（锁等待、磁盘不足）只跳过该表，不中断整轮。

### 实测（PostgreSQL 13.23）

构造一张与现网同形状的膨胀表（12000 行真实数据、8 轮整表 UPDATE 不 vacuum）：

```
bloat_demo   142.0MB   死元组 88.9%   预计可回收约 126.2MB
→ VACUUM (FULL, ANALYZE) 完成，用时 3s
→ 数据库体积 155.9MB → 29.8MB（释放 126.1MB），12000 行一行不少
```

---

## 四、Windows Agent 自动升级：这是本次唯一有真正兼容陷阱的部分

### 缺陷

三段 PowerShell 里都有 `function Invoke-Native { param([string]$File,[string[]]$Args) ... & $File @Args }`。

`$Args` 是 PowerShell 的**自动变量**。声明成参数后，每次调用都会被「未绑定实参」重新赋值成空数组——位置绑定、`-Args` 命名绑定一样中招。已在 PowerShell 7.4.6 上实测确认：

```
Bad  (param $Args)      -> count=0     # 参数被静默吞掉
Good (param $Arguments) -> count=2
```

于是 `Invoke-Native $new @('--version')` 实际执行的是不带任何参数的 `& $new`：把刚下载的 Agent 当守护进程前台拉起，`Out-String` 永远等不到管道结束，**升级助手在停服务、换二进制之前就永久吊死**。

受影响范围（`git log` 定位到 `a1817a3`，随 `Invoke-Native` 一起引入）：

- `cmd/agent/module_agent_update_scripts.go` — module 主路径
- `cmd/server/agent_update_script.go` ×2 — legacy 兜底路径

主路径和兜底路径**同时坏掉**，所以没有任何逃生口，100% 的 Windows 主机升级失败。

### 修复

1. 参数改名 `$Args` → `$Arguments`（三处）
2. 新增 `Invoke-VersionProbe`：跑 Agent 二进制的 `--version` 走带硬超时（20s）的 `Start-Process` + `WaitForExit(ms)` + `Kill()`。这类探测的输入本来就是「随时可能变成守护进程」的程序，绝不能用无界的管道捕获
3. 回归测试：`psAutomaticVarParams` 扫描生成的脚本，禁止任何 PowerShell 自动变量出现在 `param()` 里；并断言 `--version` 不走无界的 `Invoke-Native`

### ⚠️ 兼容陷阱：改 Agent 源码救不了现网的 Agent

**Windows 升级助手脚本是运行中的 Agent 用自己的代码生成的。** 装在现网的 Agent 会一遍遍生成同一段坏脚本——新版本里的修复永远送不到它们手上，因为「把新版本装上去」这件事本身就要靠那段坏脚本。

更糟的是这类缺陷绕开了原有的兜底逻辑：module 路径**报告成功**（"restart scheduled"），只是版本号永远不追上。而 `shouldLegacyAgentUpdateFallback` 只在 module 返回**错误**时才回退到 legacy 脚本，所以主机永久卡在旧版本，且每轮重试都在主机上多留一个吊死的暂存进程。

**救援机制**（`rescueWindowsAgentUpdate`，`agent_update.go`）：

legacy 脚本的文本完全由**服务端**生成并下发。所以只要服务端升级，就能单方面修好它，进而救回所有老 Agent：

```
module 路径报告成功 → pending_verify → 5 分钟版本号没动
  → 判定助手没干成活
  → 用服务端下发的 legacy 脚本重试一次（Clear-StuckSelfUpdateTask 先清残留）
  → 再等一个 5 分钟 verify 窗口
  → 成功则 success，失败则 failed 并提示去看主机上的 aiops-agent-update.log
```

安全性：只在 pending_verify 窗口耗尽后触发（此时若 module 助手真在正常工作，版本早该跳了）；每台主机每个 job 至多一次；已经走过 script 路径的主机不再重试。

legacy 救援脚本同时清理坏版本留下的残骸：

- `Stop-AgentProcesses` 增加 CIM 扫描，按映像路径匹配，能杀掉进程名形如 `.aiops-agent.new.<pid>` 的野生暂存进程（它不匹配原来的名字列表）
- `Clear-StuckSelfUpdateTask` 结束并删除吊死的 `AIOpsAgentSelfUpdate` 一次性计划任务

### 升级顺序（重要）

**先升服务端，再让 Windows Agent 升级。** 反过来不行——救援逻辑在服务端。

现网 Linux/macOS Agent 不受此缺陷影响（它们走 sh 助手，没有 `$Args` 问题）。

---

## 五、验证

| 验证项 | 方式 | 结果 |
|---|---|---|
| `$Args` 确实被清空 | PowerShell 7.4.6 实跑 | 确认，count=0 |
| 三段生成脚本语法正确 | `[Parser]::ParseFile` 实跑 | 3/3 PARSE OK，无自动变量参数 |
| 修复后 `Invoke-Native` 传参正常 | 从**真实生成的脚本**里抽出函数实跑 | exit=0，参数到位 |
| `Invoke-VersionProbe` 能掐断不退出的进程 | 用真实守护进程实跑 | 3s 超时并 Kill，无残留 |
| 快周期不重写指标变化的行 | 真实 PG，用 `xmin` 判定物理写 | 5 轮刷写 xmin 不变 |
| 慢周期确实落库指标 | 真实 PG + `loadHosts` 回读 | CPU=93.5 正确持久化 |
| 身份变化仍走快周期 | 真实 PG，`xmin` 变化 | agent_version 变更立即写入 |
| 停机期间删除的主机被镜像删除 | 真实 PG，重置写缓存模拟重启 | 残留行被清除 |
| 迁移 v13 真的落到表上（含 TOAST） | 真实 PG 查 `reloptions` / `reltoastrelid` | 主表 + TOAST 均已设置 |
| 诊断与回收工具 | 真实 PG，142MB/88.9% 死元组 | 释放 126.1MB，数据完整 |

用 `xmin` 而不是行数/内容来判定「有没有发生写」：跳过写和重写成相同内容，从外面看结果一模一样，只有 MVCC 的事务号能区分。

集成测试需要真实 PG：

```bash
AIOPS_TEST_PG_DSN="postgres://user:pass@host:5432/db?sslmode=disable" \
  go test ./cmd/server -run 'TestSaveHosts|TestAutovacuumTuning|TestPGMaintenanceQueries' -v
```

> 注：若 PG 的 `vector` 扩展装在 `public`，而部署使用自定义 schema，search_path 需带上 `public` 兜底类型解析，否则 bootstrap DDL 会报 `type "vector" does not exist`。
