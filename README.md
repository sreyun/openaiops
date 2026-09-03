<div align="center">

# AIOps

**一个开源、可私有化的主机监控与 SRE 运维平台**  
观测 · 告警 · 自愈 · 远程运维 · Agent OTA · AI 诊断 —— 收敛进一个你完全掌控的二进制。

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](README.md) · [繁體中文](docs/i18n/zh-TW.md) · [English](docs/i18n/en.md) · [日本語](docs/i18n/ja.md) · [한국어](docs/i18n/ko.md) · [Français](docs/i18n/fr.md) · [Deutsch](docs/i18n/de.md) · [Español](docs/i18n/es.md) · [Português](docs/i18n/pt-BR.md) · [Русский](docs/i18n/ru.md)**

[快速开始](#-3-分钟上手) · [默认凭据](#-默认登录凭据) · [核心能力](#-核心能力) · [文档中心](docs/README.md) · [变更日志](CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## 为什么选 AIOps

运维工具越堆越多：监控一套、告警一套、终端一套、剧本又一套；商业产品还按主机/模块计费，数据却在别人的云上。

AIOps 把高频路径收敛为 **一个可自托管的平台**：

| | AIOps | 典型拼装栈 |
|---|---|---|
| **组件** | 1 个 Go 服务端 + 1 个零依赖 Agent | Zabbix / Prometheus / Grafana / Alertmanager / 堡垒机 / 剧本系统… |
| **上线** | `docker compose up -d`，约 3 分钟 | 多组件联调，常以天计 |
| **数据** | PostgreSQL + VictoriaMetrics，**永久自持** | SaaS 或分散多库 |
| **远程** | Web 终端 / 桌面 / 端口转发，Agent **反向连接**免开入站 | 另购堡垒机或 VPN |
| **机群** | **Agent OTA 自动升级**（SHA-256 校验、维护窗、批量推送、失败回滚） | 逐台 SSH 替换二进制 |
| **闭环** | 告警 → 剧本/自愈 → 事件/SLO/工单 → AI 研判 | 工具之间靠人肉粘合 |
| **许可** | **AGPL-3.0**，无主机数阉割 | 按节点 / 模块收费 |

> 适合：自建机房、混合云、信创环境；需要「看得见、管得住、改得动、说得清」的运维与 SRE 团队。

---

## 🔑 默认登录凭据

| 项目 | 值 |
|---|---|
| **Web 控制台地址** | `http://<服务器IP>:8529` |
| **默认用户名** | `admin` |
| **默认密码** | `admin` |
| **首次登录** | 强制修改密码（`MustChangePassword`），改密后方可正常使用 |

> **安全提醒**：生产环境部署后请**立即修改默认密码**并启用 MFA（多因素认证）。如忘记密码，可通过服务端命令行工具重置：`./aiops-server -reset-admin`。

---

## ✨ 核心能力

围绕七条主线，而不是功能清单堆砌：

```
  观测 ──────────► 治理 ──────────► 自愈 ──────────► 诊断
  主机/GPU/日志      静默·抑制·路由     剧本·审批护栏     AI·RAG·MCP
  拨测/API/带外      多渠道通知         事件·SLO·工单     证据门控

  远程运维 · 终端/桌面/转发（反向隧道）     机群 · Agent OTA 自动升级
  安全 · RBAC/MFA/FIM/审计
```

### 1. 观测 —— 看得见

- **跨平台原生采集**：Linux / Windows / macOS / 麒麟等，Agent **零第三方依赖**。
- **指标与日志**：CPU·内存·磁盘·网络·进程·GPU；日志增量上报，可全文检索。
- **业务与带外**：HTTP/TCP/Ping 拨测、API 可用率；Redfish / SNMP / NetFlow / 容器 / K8s / Hyper-V。

### 2. 治理 —— 管得住噪音

- 阈值档位 + **静默 / 抑制 / 路由**，危急走电话短信、警告走 IM。
- 飞书 / 钉钉 / 邮件 / 短信 / 语音；告警生命周期与恢复通知。

### 3. 自愈与 SRE —— 改得动、可度量

- **剧本**：多步骤并行、目标按主机树多选、审批与危险命令护栏。
- **事件 / SLO / 工单 / 消息中心**一条链路；变更冻结窗与 break-glass 可审计。

### 4. AI 诊断 —— 说得清

- 巡检与根因研判（OpenAI 兼容模型，未配置时启发式兜底）。
- **RAG**（pgvector）+ Skills；**MCP** Streamable HTTP，对接 Cursor / Claude。
- 对话 / 语音（TTS·STT）可在设置页一键自测。

### 5. 远程运维 —— 进得去

- **Web 终端**：反向通道、录制回放、旁观、命令审计、二次密码；Linux/macOS 完整交互权限。
- **远程桌面**：JPEG / H.264、多屏、文件与剪贴板。
- **端口转发 / HTTP 代理**：跳板进内网服务，SSRF 防护。

### 6. 安全与交付 —— 扛得住

- RBAC（admin / operator / viewer）· MFA · Agent 机器指纹 · 配置 AES-256-GCM。
- 主机/Web 安全扫描、FIM、内容审计（合规可控）。
- Web 控制台与 **Android / HarmonyOS** 客户端独立分发。

### 7. Agent OTA —— 机群升得动

服务端升级后，**无需逐台登录**即可把采集端推到同一版本：

- **自动 OTA**（默认开启）：在线且版本落后的 Agent 会在上报时自动入队；也可在控制台 **批量 / 选择性推送**，或调用 `POST /api/v1/agents/update`。
- **安全换版**：从服务端 `/dl/` 拉取匹配 **OS/架构** 的二进制（含 Windows Server 2012 专用构建），**SHA-256 校验**；换版前备份 `.bak`，失败自动回滚。
- **可控灰度**：维护窗口（`HH:MM-HH:MM`）、按主机 / 分类豁免、连续同因失败熔断；`GET /api/v1/agents/auto-update-status` 可查看每台「为什么没升级」。
- **全链路可观测**：任务状态 `running → pending_verify → success/failed`；Windows / Linux / macOS 均有独立升级助手与日志，控制台可拉取现场证据。
- **部署注意**：OTA 走 Agent **反向长连接**（与远程终端相同）；Nginx 反代须关闭双向缓冲并放行 WebSocket，否则会出现「指标正常、升级永远失败」——见 [deploy/nginx-aiops.conf](deploy/nginx-aiops.conf) 与 [docs/getting-started/deploy.md](docs/getting-started/deploy.md)。

当前版本 **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · 镜像 [GitHub](https://github.com/sreyun/openaiops) / [Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 📸 产品截图

### Web 控制台

<table>
  <tr>
    <td align="center"><b>概览仪表盘</b><br/><br/>![概览仪表盘](image/1-shouye.png)<br/>集群资源、告警与活动的统一视图：主机在线率、系统健康状态、活跃告警一览；CPU / GPU / 内存 / 磁盘 / IO / IOPS 资源 TOP10 实时排行，一眼定位瓶颈主机。</td>
    <td align="center"><b>主机管理</b><br/><br/>![主机管理](image/2-zhuji.png)<br/>左侧资产树按机房 / 业务分组，右侧卡片式展示每台主机的实时指标——CPU、内存、交换分区、各磁盘分区、1/5/15 分钟负载、网络吞吐、IOPS、进程与连接数，支持网格 / 列表双视图。</td>
  </tr>
  <tr>
    <td align="center"><b>Web 远程终端</b><br/><br/>![Web 远程终端](image/3-zhongduan.png)<br/>经 Agent 反向通道直连目标主机，无需开放 SSH 入站端口。支持多标签页同时连接多台主机、命令审计与录制回放、旁观模式。</td>
    <td align="center"><b>远程桌面</b><br/><br/>![远程桌面](image/4-zhuomian.png)<br/>JPEG / H.264 双编码远程桌面，支持多屏切换、分辨率自适应、Ctrl+Alt+Del 等系统快捷键；右侧面板提供文件上传/下载与剪贴板同步，操作体验接近本地桌面。</td>
  </tr>
  <tr>
    <td align="center"><b>Agent 安装</b><br/><br/>![Agent 安装](image/5-agent.png)<br/>一条命令完成 Agent 部署，支持 Linux / Windows / macOS 三平台。可选标准模式、网关中继模式、多服务端推送模式；Token 策略与自动更新策略均可在控制台统一管理。</td>
    <td align="center"><b>硬件资源监控</b><br/><br/>![硬件资源监控](image/6-jiqi.png)<br/>通过 Redfish / BMC / iDRAC / iLO 带外采集物理服务器硬件状态：厂商、型号、序列号、电源/温度/功耗、BIOS 版本；BMC 事件日志（SEL）完整留存，支持 AI 诊断。</td>
  </tr>
  <tr>
    <td align="center"><b>容器管理</b><br/><br/>![容器管理](image/7-docker.png)<br/>统一管理主机上的 Docker / Podman 容器与 Compose 项目：实时状态、端口映射、镜像信息一目了然；支持一键启停、重启、日志查看，跨主机批量筛选。</td>
    <td align="center"><b>剧本编排</b><br/><br/>![剧本编排](image/8-juben.png)<br/>可视化自动化运维剧本：系统巡检、网络巡检、安全巡检、systemd 服务重启、K8s Deployment 滚动重启、深度主机巡检、Java 应用巡检/性能分析/异常分析等内置剧本开箱即用，支持自定义多步骤并行与审批护栏。</td>
  </tr>
  <tr>
    <td align="center"><b>SRE 中枢</b><br/><br/>![SRE 中枢](image/9-sre.png)<br/>告警触发 / SLO 燃尽 / 手动创建的事件统一汇聚于此，含完整时间线与自动修复记录。支持事件、自动修复、依赖拓扑、SLO、工单、On-call、变更、平台健康巡检八大子模块。</td>
    <td align="center"><b>AI 诊断</b><br/><br/>![AI 诊断](image/10-ai.png)<br/>在 SRE 事件列表中一键唤起 AI 助手，自动分析当前告警根因并给出处置建议。AI 会梳理告警关联、检索相似案例、检查关键主机健康状态，思考过程全程可见。</td>
  </tr>
  <tr>
    <td align="center"><b>告警设置</b><br/><br/>![告警设置](image/11-setting.png)<br/>多渠道告警推送配置：飞书、钉钉、Webhook、邮件、短信、电话六通道可选；支持静默 / 抑制 / 路由策略，危急走电话短信、警告走 IM，避免告警风暴。</td>
    <td align="center"><b>AI 设置</b><br/><br/>![AI 设置](image/12-aiset.png)<br/>AI 能力一站式配置：对话模型（OpenAI 兼容 / 百炼 / DeepSeek / Ollama / Anthropic / Claude）、RAG 向量库、研判与成本（MoA / 单价）、MCP 集成、调用观测、安全授权六项设置，支持语音输入/播报。</td>
  </tr>
</table>

### 移动端 App（Android / HarmonyOS）

> ️ **说明**：移动端 App（Android / HarmonyOS）为独立分发包，**开源社区版本不提供 App 安装包**。如需使用移动端，请联系项目方获取。

<table>
  <tr>
    <td align="center"><b>SRE 驾驶舱</b><br/><br/>![SRE 驾驶舱](image/app01.jpg)<br/>移动端总览页：主机在线率、严重/警告告警数一目了然；快捷入口覆盖硬件监控、虚拟机、网络流量、拨测、主机监控、日志检索、运维编排、仪表盘；待处理事件按优先级排列。</td>
    <td align="center"><b>基础设施监控</b><br/><br/>![基础设施监控](image/app02.jpg)<br/>移动端基础设施页：主机/资源/网络/拨测四大维度切换；GPU 资源概览（型号、显存、温度）；主机列表按分组筛选，实时展示 CPU、内存、磁盘等核心指标。</td>
  </tr>
  <tr>
    <td align="center"><b>移动端远程终端</b><br/><br/>![移动端远程终端](image/app03.jpg)<br/>移动端 Web 终端：经 Agent 反向通道直连目标主机，完整终端交互体验；支持快捷键、字体缩放、屏幕旋转，随时随地排查问题。</td>
    <td align="center"><b>AI 运维助手</b><br/><br/>![AI 运维助手](image/app04.jpg)<br/>移动端 AI 对话：自然语言描述问题，AI 自动检索历史案例、拉取告警详情、检查主机健康状态，给出根因分析与处置建议；底部导航栏覆盖总览/监控/告警/运维/AI 五大入口。</td>
  </tr>
</table>

---

## 🚀 3 分钟上手

> 服务端**强依赖** PostgreSQL 与 VictoriaMetrics，缺一不可。

```bash
# 1) 生成强随机密钥到 .env（PostgreSQL 密码、AIOPS_SECRET_KEY 等）
bash scripts/secure-compose.sh
#    Windows：powershell -ExecutionPolicy Bypass -File scripts/secure-compose.ps1

# 2) 一键拉起 server + VictoriaMetrics + Postgres(pgvector)
docker compose up -d

# 3) 浏览器打开 http://localhost:8529
#    默认账号：admin / admin（首次登录强制修改密码）
#    完成首次安全初始化 →「安装命令」页生成 Agent 指令 → 粘贴到目标主机
#    服务端升级后，Agent 默认会自动 OTA 到同版本（可在控制台调整策略）
```

二进制 / 源码构建：

```bash
export AIOPS_POSTGRES_DSN="postgres://aiops:密码@127.0.0.1:5432/aiops?sslmode=disable"
export AIOPS_VM_URL="http://127.0.0.1:8428"
./aiops-server   # 默认 :8529

go build ./cmd/server ./cmd/agent   # 需 Go 1.26+
```

完整步骤 → **[docs/getting-started/install.md](docs/getting-started/install.md)** · 生产加固 → **[docs/getting-started/deploy.md](docs/getting-started/deploy.md)**

---

## 🏗 架构一览

```mermaid
flowchart LR
  subgraph Clients
    Web[Web Console]
    Mob[Android / HarmonyOS]
  end
  subgraph Server
    API[HTTP · WS · MCP]
    Core[告警 · 剧本 · SRE · AI]
    PG[(PostgreSQL)]
    VM[(VictoriaMetrics)]
  end
  subgraph Fleet
    Ag[Agent × N]
    Ext[BMC · 交换机 · 存储]
  end
  Web --> API
  Mob --> API
  API --> Core
  Core --> PG
  Core --> VM
  Ag -->|反向上报 / 终端隧道 / OTA| API
  Ag --> Ext
```

- **Agent 主动出站**：机房无需为每台机器开 SSH/RDP 入站；指标上报、远程终端与 **OTA 升级** 共用反向长连接。
- **关系与时序分离**：审计 / 工单 / RAG 在 PG；指标趋势在 VictoriaMetrics。
- **机群换版**：服务端内置各平台 Agent 二进制（`/dl/`）；版本落后时自动或手动推送，Agent 本地校验 SHA-256 后热替换并重启服务。

---

## 📚 文档中心

所有长文与多语言简介在 [`docs/`](docs/README.md)；根目录仅保留本 README 与变更日志。

| 你想… | 打开 |
|------|------|
| 安装 Agent / 服务端 | [docs/getting-started/install.md](docs/getting-started/install.md) · [EN](docs/getting-started/install.en.md) |
| 生产部署 / 备份 / 容灾 | [docs/getting-started/deploy.md](docs/getting-started/deploy.md) · [EN](docs/getting-started/deploy.en.md) |
| Agent OTA 升级 / 浸泡验收 | [docs/engineering/agent-update-soak.md](docs/engineering/agent-update-soak.md) |
| 按功能学怎么用 | [docs/guides/user-guide.md](docs/guides/user-guide.md) |
| 端口转发 | [docs/guides/forward.md](docs/guides/forward.md) |
| 内容审计与剧本 | [docs/guides/content-audit.md](docs/guides/content-audit.md) |
| CI / SQL 门禁 | [docs/engineering/ci-gate.md](docs/engineering/ci-gate.md) |

---

## 🤝 贡献与社区

欢迎 Issue / PR / 翻译。开发前建议：`make build` · `make audit`（vet / test / govulncheck 等）。

- 面板 i18n：`cmd/server/web/i18n-dashboard*.js`
- 安全漏洞请私信，勿公开 Issue

如果 AIOps 帮你省下了一套拼装栈，**请点一下 Star** —— 这是对开源维护最直接的支持。

---

## 📄 许可证

[AGPL-3.0](LICENSE) · 无主机数限制、无功能阉割套路。`vendor/` 遵循各自许可证；移动端为独立分发包，源码不在本仓库。

---

<p align="center">
  <b>AIOps · 把运维的复杂度，收敛进一个你完全掌控的平台。</b><br/>
  <sub>Star ⭐ · Fork · 提 Issue · 一起把自托管运维做扎实</sub>
</p>
