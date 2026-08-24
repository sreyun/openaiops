# 容量规格书

> 这份文档是**报价与验收**用的：销售照着它出规格，实施照着它验收，运维照着它扩容。
> 里面的每个数字要么是代码里量出来的，要么写清了是估算和怎么自己测。

适用版本：v1.1.x（服务端 + Agent + PostgreSQL + VictoriaMetrics 四件套）

---

## 一、先说结论：三档规格

| 档位 | 纳管主机数 | 服务端 | PostgreSQL | VictoriaMetrics | 说明 |
|---|---|---|---|---|---|
| 小型 | ≤ 50 | 2C / 4G / 40G | 2C / 4G / 50G | 2C / 4G / 100G | 单机 all-in-one（compose 默认）足够 |
| 中型 | ≤ 200 | 4C / 8G / 80G | 4C / 8G / 200G | 4C / 8G / 500G | 建议 PG 与 VM 拆到独立卷 |
| 大型 | ≤ 500 | 8C / 16G / 160G | 8C / 16G / 500G | 8C / 16G / 1T | 建议三者分机部署；超过 500 台按下面的公式重新算 |

上表按 **默认采集间隔 30 秒 + 默认保留期**（审计 180 天 / 告警历史 90 天 / 运行历史 90 天 / 时序按 VM `retention` 配置）给出。改了这些参数就得按第二节重算。

单节点部署的可用性承诺是**单实例 + 快速恢复**，不是双活：服务端目前没有选主，跑两副本会重复告警、重复执行剧本。RTO/RPO 口径见第五节。

---

## 二、算法：每台主机吃掉多少资源

### 2.1 服务端内存（可精确计算）

服务端为每台主机维护三层内存历史环（raw 1200 点 / 1m 2880 点 / 5m 8640 点），实测：

```
每台主机 = sizeof(Sample) × (1200 + 2880 + 8640)
         = 344 B × 12720
         = 4 375 680 B ≈ 4.17 MB      ← 这是下界
```

**下界的含义**：raw 层每个采样点还挂着磁盘卷、连接数、GPU 三个切片，实际占用更高；
按经验取 **6–8 MB/台** 做规划更稳。启动日志里有一行 `内存态历史环容量预算`，直接打印当前机群的合计值，验收时抄它。

于是：

| 主机数 | 历史环下界 | 规划值（×1.8 含切片与其它内存态） |
|---|---|---|
| 50 | 0.21 GB | ~0.4 GB |
| 200 | 0.83 GB | ~1.5 GB |
| 500 | 2.09 GB | ~3.8 GB |
| 1000 | 4.17 GB | ~7.5 GB |

再加上 Go 运行时、HTTP 缓冲、AI/终端会话等，服务端总内存按上表 ×2 + 1 GB 估。

**不够用时先降环深**，不必立刻加内存：

```bash
AIOPS_HIST_RAW_MAX=600 AIOPS_HIST_1M_MAX=1440 AIOPS_HIST_5M_MAX=4320
```

代价只有"VM 不可用时的回看深度"和"图表右端的实时叠加窗口"——持久历史在 VictoriaMetrics 里，不受影响。

### 2.2 VictoriaMetrics 磁盘（估算）

一台主机的基础指标约 **25–60 条时间序列**（磁盘卷数、GPU 数会放大），默认 30 秒一个点：

```
每台每天 ≈ 40 series × 2880 点 × ~0.7 B/点(压缩后) ≈ 80 KB
每台每年 ≈ 29 MB
200 台一年 ≈ 5.8 GB          500 台一年 ≈ 15 GB
```

再加上 SNMP/NetFlow/接口拨测/exporter 抓取，按 **2–3 倍**留量。规格表里的容量已按此留了余量。

实测方法（比任何估算都准，验收时用这个）：

```bash
curl -s "$VM_URL/api/v1/status/tsdb" | head -40      # 实际序列数
du -sh /path/to/vm-data                              # 实际占盘
```

### 2.3 PostgreSQL 磁盘（估算）

PG 里放的是关系数据与审计：主机、事件、工单、变更、审计链、AI 调用观测、RAG 向量。
体量主要由**保留期**和**告警/操作频度**决定，与主机数不成正比。经验值：

| 数据 | 主要驱动 | 200 台参考 |
|---|---|---|
| 审计链（双轨） | 操作频度 × 审计保留期 180 天 | 5–20 GB |
| 告警历史 | 告警数 × 90 天 | 1–5 GB |
| 运行历史（会话/Run/剧本/Trap/硬件事件） | 运维活跃度 × 90 天 | 2–10 GB |
| RAG 向量 + AI 观测 | AI 使用量 | 1–10 GB |

自带诊断（**这是官方口径，报价时用它，不要靠猜**）：

```bash
./aiops-server -pg-report          # 只读：各表大小、膨胀估算、索引占比
./aiops-server -pg-reclaim         # 维护窗口内执行：VACUUM FULL（会持 ACCESS EXCLUSIVE 锁）
```

保留期在「设置 → 数据与备份 → 数据保留期」里改，改小立刻生效于下一轮清理。

### 2.4 网络与 Agent 侧

- Agent 常驻内存 20–60 MB，CPU 占用 < 1%（默认 30 秒间隔）
- 单台主机上行 ≈ 3–8 KB/次上报 → 200 台 ≈ 30–55 KB/s
- 终端/桌面/端口转发是按需流量，不计入基线

---

## 三、超过上限会怎样（必须写进合同附件）

授权层不会因为超限停服（见 `docs/COMMERCIAL_DELIVERY.md`），**但物理上限是真的**。超配时的表现与观测点：

| 现象 | 先看哪个指标 | 处置 |
|---|---|---|
| 平台变卡、GC 停顿长 | `aiops_memory_alloc_bytes`、`aiops_goroutines` | 降历史环深度（2.1）或加内存 |
| 曲线出现断点 | `aiops_vm_dropped_samples_total` 持续增长、`aiops_vm_queue_depth` 逼近 `aiops_vm_queue_capacity` | 扩 VM 或降采集频率 |
| 写入延迟、退出时丢内存态 | `aiops_pg_flush_duration_seconds` 从几十毫秒涨到秒级 | 扩 PG、缩短保留期、跑 `-pg-reclaim` |
| VM 查询失败、历史查不到 | `aiops_vm_breaker_state{breaker="read"}` = 2（开路） | 查 VM 存活与磁盘 |
| 主机注册被拒 | `aiops_license_hosts_used` / `aiops_license_hosts_max` | 扩容授权 |

指标出口：`GET /metrics`（Prometheus 文本格式）。鉴权用 `AIOPS_METRICS_TOKEN`（Bearer 或 `?token=`），未配置时退回会话鉴权——**不会匿名开放**，因为里面有主机规模与授权信息。

推荐的自监控告警线（接客户自己的 Prometheus）：

```promql
aiops_agent_online_ratio < 0.9                              # 在线率掉了
increase(aiops_vm_dropped_samples_total[10m]) > 0           # 正在丢样
aiops_pg_flush_duration_seconds > 2                         # PG 撑不住了
aiops_vm_breaker_state > 0                                  # VM 熔断
aiops_license_days_left < 30                                # 该续费了
aiops_license_read_only == 1                                # 已降级为只读
```

---

## 四、验收怎么做

1. 装够目标数量的 Agent，跑满 **24 小时**（跨过一次每日备份与清理）
2. 抓一份 `/metrics`，逐条比对第三节的阈值
3. 跑一次 `./aiops-server -pg-report`，记录各表大小
4. `curl "$VM_URL/api/v1/status/tsdb"` 记录序列数与磁盘占用
5. 下载一次诊断包（「授权与用量 → 下载诊断包」）作为验收快照留档
6. 跑一次恢复演练：`scripts/backup-verify.sh`，把输出贴进验收材料

第 6 步是最容易被跳过、也最不该跳过的一步——"有备份"和"恢复得回来"是两件事。

---

## 五、可用性口径（合同用词）

| 项 | 承诺 |
|---|---|
| 架构 | 单实例服务端 + PostgreSQL + VictoriaMetrics；**不支持多副本同时运行**（无选主，会重复告警与重复执行剧本） |
| RPO | 关系数据 ≤ 每日备份间隔（默认 02:30 一次；可调）；内存态历史 ≤ 5 分钟（`pgFlush` 重周期） |
| RTO | 单节点重启 < 2 分钟；从备份完整恢复 ≤ 30 分钟（含 `pg_restore` 与服务重启，取决于库大小） |
| 数据 | 时序与录像的备份需在「备份范围」里显式开启，默认只备 PostgreSQL |
| 容灾 | 支持跨机房**冷备/切换**部署，不是双活 |

RTO 的数字必须用真实数据演练过再写进合同——演练脚本就是 `scripts/backup-verify.sh`。
