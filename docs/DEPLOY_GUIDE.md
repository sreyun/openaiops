# 部署与运维指南 · DEPLOY GUIDE

> 面向生产环境的部署架构、韧性、备份与排障建议。基础安装见 [INSTALL.md](./INSTALL.md)。

---

## 一、生产部署参考架构

```
                ┌─────────────────────────────────────┐
                │           反向代理 (Nginx)            │  TLS 终止 / 限流
                └───────────────┬───────────────────────┘
                                │
                ┌───────────────┴───────────────────────┐
                │         AIOps 服务端           │
                │  check/apimon · alerts · incidents     │
                │  remediation · slos · tickets · AI 诊断 │
                └───────┬───────────────────────┬───────┘
                        │                        │
              ┌─────────┴────────┐     ┌────────┴────────┐
              │   PostgreSQL      │     │  VictoriaMetrics│
              │ (关系+审计+JSONB   │     │   (指标时序)     │
              │  +pgvector RAG)   │     │                 │
              └──────────────────┘     └──────────────────┘
                        ▲                        ▲
                        │ 上报 (X-Agent-Fingerprint)
                ┌───────┴────────────────────────┴───────┐
                │  Agent（多服务端 servers[] 并发广播）   │
                │  主机 / 进程 / 端口 / 磁盘 / 网络 / GPU  │
                └────────────────────────────────────────┘
```

- **反向代理**：生产环境建议在服务端前放置 Nginx，由其终止 TLS、做访问限流与上游保护。
  照抄仓库里的 [`deploy/nginx-aiops.conf`](../deploy/nginx-aiops.conf)，**别只写 `proxy_pass`**：
  远程终端、远程桌面、端口转发、Agent 自动升级跑在 Agent 拨出的长连接/流式通道上，Nginx 的默认值
  （不转发 `Upgrade`、双向缓冲、`proxy_read_timeout 60s`）恰好会把它们掐断，而症状极具欺骗性——
  主机在线、指标正常，只有终端连不上、Agent 自动升级永远失败。逐条说明见
  [USER_GUIDE.md 第六节](USER_GUIDE.md#六跨网络部署nginx-反代)。
- **防火墙**：服务端仅暴露必要端口；Agent 通过出站连接上报，无需在服务端开放 Agent 入站端口。

---

## 二、跨机房容灾

- **多服务端广播**：采集端 `servers[]` 可配置多个服务端地址，采集一次、并发广播上报；单个服务端不可达时自动断路、退避并以 gzip 降级重连，数据不丢。
- **网关中继 (relay)**：跨网段 / 防火墙后的主机，可通过中继反向隧道纳管。`X-Relay-Secret` 用于中继与服务端之间的身份校验（防 Host 头注入）。中继与服务端须配置一致的中继密钥。
  网关机自己也会照常采集并直连上报，因此它同样出现在主机列表里、可远程终端与自动升级；这依赖 `config.yaml` 里的 `token`（新版安装脚本会写入）。本改动之前装的网关重跑一次带 `?token=` 的中继安装命令即可，配置与 `/dl` 缓存都保留。
- **数据韧性**：关系数据落在 PostgreSQL（建议开启流复制 / 定期备份）；指标落在 VictoriaMetrics（建议配置 `retention` 与远端存储）。

> 容灾表述：平台支持**跨机房容灾**部署，而非双活 / 多活架构。

---

## 三、备份与升级

- **PostgreSQL**：定期 `pg_dump` 或流复制；备份包含审计、事件、工单与 RAG 向量。Web 管理员可在「个人信息 → 数据与备份」启用每日自动备份、下载与二次确认还原（需服务端 PATH 含 `pg_dump` / `pg_restore`，目录默认 `./backups` 或环境变量 `AIOPS_BACKUP_DIR`）。还原后建议重启服务端以刷新内存态。
- **VictoriaMetrics 与录像**：控制台「备份范围」里可开启 **包含时序数据** 与 **包含终端/桌面录像**（默认关闭，因为它们明显占盘）。开启后「整套备份」与每日计划会一并导出：时序走 VM 的 `/api/v1/export/native` 落成 `aiops-vm-*.native.gz`，录像打成 `aiops-rec-*.tar.gz`，与 PG 备份同目录、同台账、**按种类各留 N 份**。
- **恢复演练**：`scripts/backup-verify.sh` 会起一次性 PostgreSQL / VictoriaMetrics 容器，把备份真的还原一遍并跑存活性查询，全程不碰生产。**"有备份"和"恢复得回来"是两件事**，交付与验收都以这份输出为准。

  ```bash
  BACKUP_DIR=/data/backups bash scripts/backup-verify.sh
  ```

- **各类备份怎么还原**：PostgreSQL 用备份列表里的「还原」（删库重建模式，还原前自动打保护性备份）；时序用 `gzip -dc aiops-vm-*.native.gz | curl --data-binary @- "$VM_URL/api/v1/import/native"`；录像 `tar -xzf aiops-rec-*.tar.gz` 解回录像目录。时序/录像备份**不能**走 PostgreSQL 还原流程，服务端会在删库之前拦住。
- **配置**：`AIOPS_SECRET_KEY`、`AIOPS_RELAY_SECRET` 等密钥请纳入密钥管理，升级时保持不变以避免数据不可解密。
- **平台自身可观测**：`GET /metrics` 输出 Prometheus 文本格式（在线率、告警数、VM 熔断与丢样、`pgFlush` 延迟、PG 连接池、授权余量、goroutine/内存）。用 `AIOPS_METRICS_TOKEN` 配 Bearer 令牌（也支持 `?token=`）；未配置时退回会话鉴权，**不会匿名开放**。推荐告警线见 `docs/CAPACITY.md` 第三节。
- **一键诊断包**：`GET /api/v1/admin/support-bundle`（管理员）下载 zip，含版本、脱敏配置、迁移版本、PG/VM 连通性、最近活动日志与 goroutine 快照，可直接发给技术支持；**不含任何密钥与业务数据**。
- **授权（商业交付）**：授权文件离线验签，超限/到期只降级不停服——采集、告警与 Agent 上报照常，被拦的是新主机注册与人发起的写操作。安装入口在控制台「授权与用量」。签发与状态机见 `docs/COMMERCIAL_DELIVERY.md`。
- **容量规格**：报价与验收口径见 `docs/CAPACITY.md`（每台主机的内存下界是量出来的，PG/VM 给了公式与实测命令）。
- **升级**：建议先在预发环境验证，再滚动升级服务端；采集端可分批灰度。
- **升级命令（docker compose 部署，务必带 `pull`）**：

  ```bash
  docker compose pull        # 缺了这一步就不会真的升级
  docker compose up -d
  ```

  `docker compose up -d` 单独跑**不会**换镜像：compose 对只声明 `image:` 的服务默认
  `pull_policy: missing`，本地已经有一个 `latest` 就不再回源。于是"一路 up -d 升上来"
  的站点可能长期停在几个月前的镜像上，而且不容易发现——控制台前端和后端在同一个二进制里，
  界面和接口一起旧着，自洽。真正暴露的时候通常是照着新版文档操作，撞上"这个接口不存在"。

  升级后核对版本：面板「关于我们」里的版本号，或
  `docker compose images aiops-server`（看镜像创建时间）。

  离线/内网环境无法回源时，请手工 `docker load` 新镜像后再 `up -d`；不要给服务加
  `pull_policy: always`，那会让断网时连重启都起不来。

---

## 四、AI 巡检诊断与 RAG

- AI 巡检定时（约 30 分钟）对指标与事件做诊断，结论可回灌向量库（`diagnosis_embeddings`，维度需与所配嵌入模型一致）。
- 检索相似历史案例时支持 **👍/👎 反馈重排**，形成「诊断 → 反馈 → 检索质量提升」的学习闭环。
- 未配置 AI 能力时，系统以启发式诊断兜底，保证闭环不中断。

---

## 五、故障排查

| 现象 | 排查方向 |
|---|---|
| 服务端启动即退出 | 检查 PostgreSQL / VictoriaMetrics 是否都可达（双强制依赖）。 |
| 指标缺失 | 确认 VM 写入地址、Agent 上报通道与防火墙出站策略。 |
| 告警不触发 | 检查阈值预设档位、告警治理的静默 / 抑制规则是否误命中。 |
| 远程终端连不上 | 确认账号角色为 `operator+`、终端二次密码正确、会话 Cookie 未过期。 |
| 中继纳管失败 | 核对 `X-Relay-Secret` 中继与服务端是否一致、Host 头是否被正确转发。 |

---

## 六、合规与安全边界

- 平台提供完整的操作审计、终端录制审计与内容审计能力，可用于「**契合等保审计要求**」的运维追溯；正式等保测评请依你所在行业的规范执行，平台不宣称"满足等保 / 一键通过"。
- 静态敏感数据（如终端录制）建议启用 `AIOPS_SECRET_KEY` 加密；传输层建议全程 TLS。
- 角色权限遵循最小权限原则：`viewer` 仅查看，`operator` 可操作运维动作，`admin` 管理用户与全局策略。
