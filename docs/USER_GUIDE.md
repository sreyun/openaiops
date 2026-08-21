# AIOps 安装使用说明书

> 轻量级主机监控运维平台 —— Go 原生采集 + Python 插件层 + 实时面板 + 阈值告警 + 远程终端 + 自动化剧本。单二进制服务端、零依赖 Agent、三平台原生采集（含 GPU）、一条命令安装、开箱即用。

本说明书与**官方网站**内容一一对应，既可作为官网各栏目的配套详解，也可被官网直接引用。全文分为「安装部署」与「功能使用」两大主线，覆盖服务端与 Windows / Linux / macOS 客户端的完整落地流程。

- **在线官网**：`website/index.html`（首页 / 功能详情 / 解决方案 / 产品对比 / 常见问题 / 联系我们）
- **纯安装速查**：见 [INSTALL.md](INSTALL.md)
- **完整开发者文档**：见 [README.md](README.md)

---

## 官网栏目对应关系

下表把官网每个页面 / 区块映射到本说明书对应章节，便于交叉查阅或从网站直接跳转。

| 官网页面 / 区块 | 对应章节 |
|---|---|
| 首页 · Hero「3 分钟完成部署」 | [二、快速开始](#二快速开始3-分钟上线) |
| 首页 · 痛点与方案 | [一、产品概览](#一产品概览) |
| 首页 · CTA「一条命令部署」 | [三、安装部署](#三安装部署) |
| 功能详情 · 01 监控与指标 | [4.1 监控与指标](#41-监控与指标) |
| 功能详情 · 02 告警与通知 | [4.2 告警与通知](#42-告警与通知) |
| 功能详情 · 03 远程访问与审计 | [4.3 远程访问与审计](#43-远程访问与审计) |
| 功能详情 · 04 自动化运维 | [4.4 自动化运维（剧本）](#44-自动化运维剧本) |
| 功能详情 · 05 安全与权限 | [4.5 安全与权限](#45-安全与权限) |
| 功能详情 · 06 部署与架构 | [4.6 部署与架构](#46-部署与架构) |
| 功能详情 · 07 扩展能力（插件） | [4.7 扩展能力（Python 插件）](#47-扩展能力python-插件) |
| 解决方案页 | [一、产品概览](#一产品概览) + [四、功能使用说明](#四功能使用说明) |
| 产品对比页 | [附录 A：与传统方案对比](#附录-a与传统方案对比) |
| 常见问题（FAQ）页 | [八、常见问题 FAQ](#八常见问题-faq) |
| 联系我们页 | [九、获取支持与联系我们](#九获取支持与联系我们) |

---

## 目录

- [一、产品概览](#一产品概览)
- [二、快速开始（3 分钟上线）](#二快速开始3-分钟上线)
- [三、安装部署](#三安装部署)
  - [3.1 部署服务端](#31-部署服务端)
  - [3.2 安装客户端 Agent](#32-安装客户端-agent)
  - [3.3 开机自启](#33-开机自启)
  - [3.4 安装验证](#34-安装验证)
- [四、功能使用说明](#四功能使用说明)
  - [4.1 监控与指标](#41-监控与指标)
  - [4.1.1 Kubernetes 集群（服务端直连）](#411-kubernetes-集群服务端直连)
  - [4.1.2 虚拟机管控与主机容器](#412-虚拟机管控与主机容器)
  - [4.2 告警与通知](#42-告警与通知)
  - [4.3 远程访问与审计](#43-远程访问与审计)
  - [4.4 自动化运维（剧本）](#44-自动化运维剧本)
  - [4.5 安全与权限](#45-安全与权限)
  - [4.6 部署与架构](#46-部署与架构)
  - [4.7 扩展能力（Python 插件）](#47-扩展能力python-插件)
- [五、配置参考](#五配置参考)
- [六、跨网络部署（Nginx 反代）](#六跨网络部署nginx-反代)
- [七、升级与卸载](#七升级与卸载)
- [八、常见问题 FAQ](#八常见问题-faq)
- [九、获取支持与联系我们](#九获取支持与联系我们)
- [附录 A：与传统方案对比](#附录-a与传统方案对比)

---

## 一、产品概览

AIOps 用**一个服务端 + 每台主机一个 Agent**的极简架构，替代「Prometheus + Grafana + Alertmanager + 工单系统」的多组件拼装。

**核心定位**

- **服务端**：1 个 Go 单二进制（约 15MB），内置面板 + 采集聚合 + 告警推送，默认监听 `:8529`。生产安装基线为 **PostgreSQL（关系数据）+ VictoriaMetrics（时序）**（见 `docker-compose.yml`）；不再依赖内置 `aiops.db`，也**无需 MySQL / Redis / Kafka**。
- **客户端 Agent**：装在每台被监控主机上，零第三方依赖，支持 **Windows / Linux / macOS**（含 amd64 / arm64）。
- **插件层（可选）**：需要服务探活、CPU 异常检测、进程监控等增强能力时，可启用 Python 插件（依赖 `psutil`）。

**它解决的痛点**（对应官网首页「痛点与方案」）

| 传统困境 | 本产品的解法 |
|---|---|
| 每台机器手工装 Agent、批量纳管难 | 一条命令自动部署 Agent，批量纳管 |
| 多个开源组件拼装，部署维护成本高 | 单二进制、零外部依赖，3 分钟上线 |
| 内网机器要监控 / 要远程，得开端口、拉 VPN | Agent 反向连接，免开入站端口，浏览器直达终端 |
| 告警刷屏，真故障被淹没 | 分级 + 去重冷却 + 噪音抑制，告警量降低约 80% |

---

## 二、快速开始（3 分钟上线）

> 对应官网 Hero「开源免费 · 单二进制 · 3 分钟完成部署」。

**① 启动服务端（Docker 一键，推荐）**

```bash
git clone https://github.com/sreyun/aiops-monitor.git && cd aiops-monitor
docker compose up -d
# 浏览器打开 http://localhost:8529
```

或直接运行预编译二进制：

```bash
./bin/aiops-server            # 默认监听 :8529
```

**② 登录面板**

浏览器打开 `http://<服务端IP>:8529`，默认凭据 `admin / admin`。

> ⚠️ 首次登录后**请立即修改用户名与密码**，并建议启用 MFA 两步验证。

**③ 一条命令安装 Agent**

面板右上角点 **「安装 Agent」** → 选择目标系统 → 复制命令到被监控主机执行（命令已内置服务端地址与 Token）：

```bash
# Linux（root/sudo）— 自动检测 amd64/arm64
curl -fsSL "http://<服务端>:8529/install.sh?token=<TOKEN>" | sudo sh

# macOS — 自动检测 Intel/Apple Silicon
curl -fsSL "http://<服务端>:8529/install.sh?token=<TOKEN>" | sh

# Windows（管理员 PowerShell）
irm "http://<服务端>:8529/install.ps1?token=<TOKEN>" | iex
```

几秒后刷新面板，即可看到该主机卡片与实时指标。完成。

---

## 三、安装部署

> 对应官网首页 CTA「一条命令部署服务端，一条命令安装 Agent」。本章为完整落地流程；纯速查见 [INSTALL.md](INSTALL.md)。

### 3.1 部署服务端

服务端是单个 Go 二进制，内置面板，无外部依赖。`bin/` 下已附预编译产物。

**方式一：Docker（推荐）**

```bash
git clone https://github.com/sreyun/aiops-monitor.git && cd aiops-monitor
docker compose up -d aiops-server
```

- 服务端数据通过 volume 持久化（`/app/data`），配置文件在 `./server_config.json`
- 默认端口 `8529`，可在 `docker-compose.yml` 修改映射
- 镜像支持 `amd64` / `arm64` 双架构，`docker pull` 自动匹配

**方式二：二进制直接运行**

```bash
# Linux / macOS
./bin/aiops-server                          # 默认监听 :8529
./bin/aiops-server -addr 0.0.0.0:9000       # 指定地址/端口
./bin/aiops-server -config /path/to/config  # 指定配置文件
```

```powershell
# Windows
.\bin\aiops-server.exe                      # 默认 :8529
.\bin\aiops-server.exe -addr 0.0.0.0:9000
```

**方式三：自行编译（需 Go 1.22+）**

```bash
go build -o bin/aiops-server ./cmd/server
go build -o bin/aiops-agent  ./cmd/agent
```

Windows 一键构建（自动注入 Git tag 版本号）：

```powershell
powershell -File build.ps1                  # 仅本机
powershell -File build.ps1 -CrossCompile    # 含交叉编译 Linux/macOS 产物
```

**放行端口**（否则客户端连不上、浏览器打不开面板）：

```bash
# Linux firewalld
firewall-cmd --add-port=8529/tcp --permanent && firewall-cmd --reload
# Linux ufw
ufw allow 8529/tcp
```

```powershell
# Windows 防火墙
New-NetFirewallRule -DisplayName "AIOps" -Direction Inbound -Protocol TCP -LocalPort 8529 -Action Allow
```

服务端配置（告警 Webhook / 阈值 / 分类覆盖）持久化在其工作目录的 `server_config.json`。

### 3.2 安装客户端 Agent

> ✅ **推荐：一条命令安装（Token 模式）**——见 [二、快速开始](#二快速开始3-分钟上线)。命令会自动下载对应架构 Agent + 插件、写好配置、注册开机自启并上线。弹窗填「主机分类」可让新机自动归组；点「重置」可轮换 Token（旧命令随即失效）。

下面的**手动安装**适用于内网隔离、需自定义路径或不便联网下载的场景。每台被监控主机需要两样东西，放在同一目录：

```
aiops-agent(.exe / -mac)     # 对应平台的 Agent 二进制
plugins/                     # 插件目录（整个拷过去，可选）
```

Agent 需从「包含 `plugins/` 的目录」运行，或用 `--plugins-dir` 指定 plugins 的**绝对路径**（服务化时推荐）。

**Windows 客户端**（假设放在 `C:\aiops-agent\`）

```powershell
cd C:\aiops-agent
.\aiops-agent.exe --server http://<服务端IP>:8529 --category 生产
```

**Linux 客户端**（假设放在 `/opt/aiops-agent/`）

```bash
cd /opt/aiops-agent
chmod +x aiops-agent
./aiops-agent --server http://<服务端IP>:8529 --category 生产
```

**macOS 客户端**（假设放在 `/usr/local/aiops-agent/`）

```bash
cd /usr/local/aiops-agent
chmod +x aiops-agent-mac
./aiops-agent-mac --server http://<服务端IP>:8529 --category 办公终端
```

> 若 macOS 提示「无法验证开发者」，在 `系统设置 → 隐私与安全性` 点「仍要打开」，或执行 `xattr -d com.apple.quarantine ./aiops-agent-mac`。

**Python 插件依赖（可选）**：想用服务探活 / 异常检测 / 进程监控插件时：

```bash
pip install -r plugins/requirements.txt      # 即 psutil
```

不装也能跑——基础指标照常原生采集，只是这几个插件会静默跳过。

### 3.3 开机自启

**Linux（systemd，推荐）**

```bash
cp deploy/aiops-agent.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now aiops-agent
systemctl status aiops-agent          # 查看状态
journalctl -u aiops-agent -f          # 跟踪日志
```

`WorkingDirectory` 指向含 `plugins/` 的目录即可；崩溃自动重启（`Restart=always`）。服务端自启同理，用 `deploy/aiops-server.service`。

**Windows —— 方式 A：NSSM（推荐，带自动重启）**

```powershell
nssm install AIOps-Agent C:\aiops-agent\aiops-agent.exe "--server http://<IP>:8529 --category 生产"
nssm set AIOps-Agent AppDirectory C:\aiops-agent     # 关键：设工作目录才能找到 plugins\
nssm start AIOps-Agent
```

**Windows —— 方式 B：任务计划程序（原生）**

用仓库自带的 `deploy/start-agent.bat`（会先 `cd` 到自身目录再启动），放到 `C:\aiops-agent\`，改好 IP 与分类，然后：

```powershell
schtasks /Create /TN "AIOps-Agent" /TR "C:\aiops-agent\start-agent.bat" /SC ONSTART /RU SYSTEM /RL HIGHEST /F
schtasks /Run /TN "AIOps-Agent"
```

> 直接用 `schtasks` 指向 exe 会因工作目录是 `System32` 而找不到 `plugins\`，务必通过 `start-agent.bat`（或用 NSSM 的 `AppDirectory`）。

**macOS（launchd）**

```bash
cp deploy/com.aiops.agent.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.aiops.agent.plist
launchctl start com.aiops.agent
```

### 3.4 安装验证

1. 浏览器打开 `http://<服务端IP>:8529`，几秒后应看到该主机卡片出现在对应**分类分组**下。
2. 卡片应有真实的 CPU / 内存 / SWAP / **每个磁盘一条** / 负载 1·5·15 / 网络收发 / TCP 连接数 / 进程数。
3. 点主机名可看**趋势弹窗**（CPU/内存/磁盘 sparkline）。
4. 装了 psutil 的话，底部会出现 `svc.*`、`proc.*`、`cpu.anomaly_zscore` 等自定义指标 chips。

命令行快速自检（在服务端上）：

```bash
curl http://localhost:8529/api/v1/hosts | python3 -m json.tool
```

---

## 四、功能使用说明

> 本章按官网「功能详情」页的 **7 大分组**逐一对应，说明每项能力在面板中的使用方法。

### 4.1 监控与指标

对应官网分组 **01 监控与指标**。

**基础指标（三平台原生采集，零依赖）**

CPU 使用率/核数、内存/SWAP、全部本地磁盘、网络收发速率、TCP 连接数、负载 1/5/15、进程数、运行时长——Linux 走 `/proc`+`syscall`、Windows 走 Win32 API、macOS 走 `sysctl`+系统命令。

**GPU 监控**：NVIDIA（`nvidia-smi`）、AMD（Linux sysfs）、Apple（macOS `ioreg`），best-effort 采集使用率/显存/温度，结果缓存约 12s；无 GPU/无工具时不显示，不影响其他指标。

**交互式趋势图**：纯 Canvas 绘制，支持悬停十字线 + 数值气泡、框选放大、双击还原、放大预览；渐变填充、统一时间跨度控件（1h ~ 30 天）、水平图例。点主机名即可打开趋势弹窗。

**自定义拨测**：面板「监控」页可添加主动拨测，定时探测并在异常时自动告警：

| 类型 | 需要填写 | 判定为异常 |
|---|---|---|
| **HTTP 网站** | URL（如 `https://example.com`） | 状态码 ≥ 400，或超时/请求失败 |
| **TCP 端口** | 主机:端口（如 `10.0.0.5:3306`） | 无法建立连接 |
| **Ping 主机** | 主机地址 / IP（如 `8.8.8.8`） | 100% 丢包（不可达） |
| **进程存活** | ① 目标主机 ＋ ② 进程名称 | 目标主机未上报该进程（或离线） |

> 进程监控需先选目标主机再填进程名（服务端核对该主机 Agent 上报的进程列表），匹配规则为不区分大小写的子串匹配。每项支持列表/胶囊双视图 + 历史曲线回看。

#### 4.1.0 SQL 工具（MySQL 美化 / 审核 / 优化 + EXPLAIN）

面板 **运维工具 → SQL 工具**：面向 MySQL **5.7 / 8.x** 的 DBA 辅助，与观测「数据源」（Prometheus / Loki）分离。

| 能力 | 说明 |
|---|---|
| **全面分析** | Vitess AST 解析 + 静态规则 +（可选）`information_schema` 元数据索引建议 + `EXPLAIN FORMAT=JSON` 复合打分 |
| **离线美化** | 关键字大写、缩进与换行；无需连库 |
| **离线审核** | AST 优先；解析失败回退正则（`SELECT *`、无 WHERE 的 UPDATE/DELETE、`LIKE '%x'`、函数包列、缺 LIMIT 等） |
| **离线优化** | 静态改写建议；有连接时按 SQLAdvisor 理论（等值→范围→ORDER/GROUP，过滤已有覆盖索引）给出 DDL |
| **EXPLAIN** | 可选只读连接；解析索引命中 / 全表扫描 / filesort / temporary，并计入综合分 |
| **AI 深度** | 「AI 美化 / 审核 / 深度优化」注入综合分、breakdown、index_hints 与 EXPLAIN 摘要 |

**综合分**：`100 − 静态惩罚 − 元数据惩罚 − EXPLAIN 惩罚`（crit/warn/info 权重不同）。选择 MySQL 连接后点「全面分析」会自动拉元数据并跑 EXPLAIN（仅 SELECT/WITH）。

**只读账号建议（最小权限）**

```sql
CREATE USER 'aiops_ro'@'%' IDENTIFIED BY '...';
GRANT SELECT, SHOW VIEW ON app.* TO 'aiops_ro'@'%';
-- 全面分析还需读取 information_schema（TABLES / COLUMNS / STATISTICS）
-- 多数环境对 information_schema 默认可读；若收紧权限请显式授予
FLUSH PRIVILEGES;
```

连接由管理员在 **连接管理** 中配置（密码加密存储）；工具侧拒绝多语句与 DDL/DML/`INTO OUTFILE`，查询超时默认 10s。配置示例见 `server_config.example.json` 的 `mysql_connections`。

#### 4.1.1 Kubernetes 集群（服务端直连）

面板 **资源 → K8s**：由**服务端**直连 kube-apiserver（不依赖某台 Agent）。支持只读浏览（Namespaces / Nodes / Pods / Deployments / Events / Pod 日志）与受控变更（Deployment **Scale** / **Restart**，二次确认，写入操作审计）。

**接入步骤（推荐 ServiceAccount Token）**

1. 在目标集群创建只读（或按需加 patch）ServiceAccount，例如：

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: aiops-monitor
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: aiops-monitor
rules:
  - apiGroups: [""]
    resources: ["namespaces", "nodes", "pods", "pods/log", "events"]
    verbs: ["get", "list"]
  - apiGroups: ["apps"]
    resources: ["deployments", "deployments/scale"]
    verbs: ["get", "list", "patch"]   # 仅查看可去掉 patch
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: aiops-monitor
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: aiops-monitor
subjects:
  - kind: ServiceAccount
    name: aiops-monitor
    namespace: kube-system
```

2. 创建长期 Token（或粘贴 kubeconfig），在面板 **资源 → K8s → 集群配置**（管理员）填写 API Server + Token + CA，或粘贴 kubeconfig；点**连通测试**。
3. Token / kubeconfig 在服务端加密存储；列表回显为 `****`，保存时留空表示保持原值。

**权限**

| 操作 | 角色 |
|---|---|
| 查看集群资源 / 日志 | viewer+ |
| Scale / Restart Deployment | operator+（需二次确认） |
| 增删改集群配置、连通测试 | admin |

变更会写入操作审计（含集群名、命名空间、资源、Scale 新旧副本数）。本阶段不支持删 Pod、编辑 YAML、Helm，也不引入完整 `client-go`。

#### 4.1.2 虚拟机管控与主机容器

**资源 → 虚拟机（Hyper-V）**（需 Windows 宿主机 Agent）：

- 启停 / 关机 / 重启 / 强制关机（operator+，二次确认，审计）
- 编辑 vCPU / 启动内存（Running 中部分改配可能失败，按 Hyper-V 限制）
- 已关联纳管 Guest 时可「打开终端」；未关联时请在 Guest 安装 Agent

**资源 → 容器**（Docker / Podman）：

- Agent 自动采集 `docker ps -a` / `podman` 清单并上报
- 列表查看、Start/Stop/Restart、查看日志（operator+ 变更）

**AI 闭环定位**：Sreyun 工具 `locate_resource` / `query_containers` / `query_k8s` / `k8s_scale` / `k8s_restart`，可将容器/Pod 问题映射到纳管主机、Hyper-V 父机与硬件变更线索。K8s Node 按主机名/IP 自动关联纳管主机。

### 4.2 告警与通知

对应官网分组 **02 告警与通知**。告警在面板可视化配置，无需改文件：

1. 面板右上角点 **告警设置**。
2. 填**飞书**或**钉钉**机器人 Webhook（钉钉开「加签」再填 Secret），勾选启用。
3. **邮件推送**：展开 SMTP 区域，填服务器/端口/账号/授权码——465 端口选隐式 TLS，587 不选。
4. 点 **发送测试** 确认通道连通（失败会返回真实错误码，便于排查关键词/签名问题）。
5. 点 **保存**——保存后**立即补推当前未恢复告警一次**，之后仅在告警「新触发/恢复」时各推一次，不刷屏。

**告警类型与级别**

| 告警类型 | 触发条件 | 级别 |
|---|---|---|
| CPU / 内存 / 磁盘 | 超过设定阈值 | 警告 / 严重 |
| 主机失联 | 超过设定时长未上报（默认 30s） | 严重 |
| GPU 使用率 | ≥ 80% 警告，≥ 90% 严重 | 警告 / 严重 |
| 系统负载 | 5min 负载 ≥ 核数 × 2 | 警告 / 严重 |
| HTTP / TCP / Ping / 进程 | 拨测异常 | 自定义 |

**分级与降噪**：严重 / 警告两级；事件去重冷却（5 分钟内相同事件不重复推送）结合噪音抑制，告警量降低约 80%。
**桌面通知**：浏览器 Notification API 桌面弹窗 + 声音提醒，无需打开页面也能第一时间感知。

> 飞书自定义机器人若设了「自定义关键词」，请把关键词设为 `AIOps` 或 `告警`。钉钉建议用「加签」安全设置，把 Secret 填进面板即可自动签名。

### 4.3 远程访问与审计

对应官网分组 **03 远程访问与审计**。

**远程终端**：主机卡片一键打开浏览器直连终端，Agent 反向连接**免开入站端口**（被控端无需开放 22）。
**趋势弹窗**（首页或主机页点开某台机器看曲线）右上角也有终端 / 桌面两个图标按钮，看完指标可直接上机器，不必再回列表里找它一遍；主机离线或该能力被关闭时按钮不出现。

- **多标签**：可同时开多台主机/多个终端；**收起悬浮卡片**最小化到右下角，WebSocket 保持连接，点击展开恢复。
- **完整 VT100 仿真**：`vim`/`top` 等全屏程序可用；窗口自适应；移动端虚拟键盘。
- **复制**：拖选后按 **Ctrl+C**，或直接**在高亮上点右键**即复制（右键点在别处只弹菜单，不会冲掉你正准备粘贴的剪贴板）；标题栏还有一个**复制按钮**——有选区复制选区，没有就复制整屏。没有选中内容时 Ctrl+C 仍然是 SIGINT，不会抢走中断。
  面板开在**明文 HTTP** 上时浏览器不提供剪贴板 API，系统会自动退回旧接口；两条路都被浏览器策略挡住时会弹出**手动复制框**（内容已选中，按 Ctrl+C 即可），不会只丢一句"复制失败"。
- **跨平台 TTY**：Windows ConPTY（chcp 65001 + GBK→UTF-8）、Linux/macOS openpty。

**终端会话回放**：所有会话全程录制（带时间戳帧 + 终端尺寸变化），支持进度条拖拽、1x/2x/4x/8x 倍速播放，回放自动还原录制时的终端尺寸。列表支持按**操作者 / 主机 / IP** 三维搜索。**只读旁观**让多名管理员同时查看活跃会话，用于协作排障。

**端口转发（TCP / HTTP）**：通过 Agent 反向隧道把内网服务映射到本地浏览器，零公网暴露。

- **TCP 端口映射**（适合数据库、SSH 等长连接）：在面板「转发」页创建规则，或用 API：

  ```bash
  # 例：将 Agent 主机的 MySQL 3306 映射到服务端 13306
  curl -X POST http://<服务端>:8529/api/v1/forward \
    -d '{"host_id":"abc123","target_port":3306,"local_port":13306}'
  # 然后本地直连
  mysql -h 127.0.0.1 -P 13306 -u root -p
  ```

  支持自动分配端口（`local_port: 0`）或指定端口；规则可启用/禁用/编辑/复制/删除；列表 / 卡片双视图。

- **HTTP 反向代理**（无状态，无需建规则）：

  ```bash
  # 访问 Agent 主机 abc123 上 8080 端口的 /api/health
  curl http://<服务端>:8529/proxy/abc123/8080/api/health
  # 支持 GET/POST/PUT/DELETE/PATCH 全方法 + WebSocket 升级
  ```

  面板可保存常用代理为快捷入口；`window.open()` 场景用一次性 proxy_token 鉴权。

> 端口转发默认开启，可在告警设置中通过 `forward_disabled: true` 全局关闭。

**操作日志与审计**：全量操作日志（操作 / 系统 / 插件三类），支持筛选与 CSV 导出；与终端录制、命令审计共同构成完整审计闭环，可直接作为等保测评材料。

**网络与大模型内容审计**：

- Linux 默认使用原生 `AF_PACKET`；Windows/macOS 使用 Wireshark TShark（Windows 需 Npcap，macOS 普通用户建议安装 ChmodBPF）。
- 被动采集可获得 DNS/SNI 与指定端口的明文 HTTP 请求/响应，支持 IPv4/IPv6、TCP 乱序/重传、chunked、gzip/deflate 与 SSE；不会解密 HTTPS。
- 正文策略包括 `metadata`、`redacted`（默认）和 `full`。建议同时配置端口、域名白名单、排除路径与事件限流。
- 云端大模型 HTTPS 审计使用 `POST /api/v1/integrations/content-audit`：在 LLM Gateway/SDK 完成 TLS 终止后，上报 principal、application、model、token、tool call、策略决策、hash 等结构化字段。服务端需设置独立的 `AIOPS_CONTENT_AUDIT_INGEST_TOKEN`。

完整配置、权限和接入示例见 [内容审计专家指南](CONTENT_AUDIT_AND_PLAYBOOK_EXPERT_GUIDE.md)。

**主机安全扫描（安全中心 → 主机安全）**

- Agent 模块 `host_security_scan` 采集：OS/包清单/监听与进程/加固基线/关键路径哈希/IOC 启发式，以及可选 **ClamAV**（`clamscan`）。
- 主机侧建议安装：`apt install clamav` / `yum install clamav` / `apk add clamav`；未安装时报告字段 `clamav: unavailable`，扫描仍可用（加固 + 包 CVE）。
- 服务端用 [OSV](https://osv.dev) `querybatch` 匹配包 CVE，计算 0–100 安全分与修复建议；结果落在 `data/security/host_scans.json`（有 PG 时配置仍走 ConfigStore）。
- 定时：`host_security.enabled` + `schedule`（`interval` / `daily` / `weekly`，与剧本调度相同）。配置 API：`GET/POST /api/v1/security/host/config`（写入需 admin）。
- 权限：查看与一键扫描 **operator+**；viewer 不可见安全中心。

**Web 漏洞扫描（安全中心 → Web 扫描）**

- 纯服务端 **Nuclei**（Docker 镜像已内置 `nuclei` 二进制）。配置 `web_security.nuclei_path`（默认 `nuclei`）、`templates_dir`、`severity`、`rate_limit`、`concurrency`、`timeout_sec`。
- 首次/可选启动更新模板：`web_security.update_templates: true`（执行 `nuclei -update-templates`）；也可手工更新后把模板目录挂到容器。
- 目标 CRUD：`/api/v1/security/web/targets`；立即扫 `POST .../targets/{id}/scan`。仅 `http`/`https`；默认禁止私网/保留地址，需 admin 将 `allow_private: true`。
- Tags 白名单：`cves,misconfig,exposures,default-logins,...`。单目标超时与全局并发信号量（默认 1）防止打爆站点。
- 扫描完成后生成结构化报告（摘要、风险分布、修复建议），前端可导出 Markdown / Excel / Word / PDF；AI Assist 任务：`host_security_diagnosis` / `web_vuln_diagnosis`。

### 4.4 自动化运维（剧本）

对应官网分组 **04 自动化运维**。面板「自动化」页可编排剧本——一组按顺序在目标主机上批量执行的 shell 命令：

**创建剧本**：填名称 + 若干步骤，每步包含：

- **类型**：优先使用跨平台内置只读/变更模块，也可使用 Shell（Linux `sh -c`、Windows `cmd /c`）
- **目标**：`全部` / `分类:xxx` / `系统:linux|windows|macos` / `主机:<ID>`
- **可靠性**：超时、最大尝试次数、退避、是否继续；仅幂等步骤才开启非零退出码重试
- **条件与变量**：`when` 条件、`register` 输出变量与 `{{变量}}` 引用；未知/前向引用会在保存时拒绝
- **显式回滚**：剧本开启自动回滚后，失败时按成功步骤逆序执行各步明确配置的回滚命令

**执行原理**：执行前先做确定性 preflight（在线/离线目标、风险、并发、回滚覆盖）；命令经 Agent 反向通道下发，以一次性子进程执行、回传输出与退出码。在线主机按剧本 `max_parallel` 限流并行，每台按步骤顺序运行；高风险手工执行必须显式确认。执行历史保留最近 100 次（操作者、尝试次数、回滚与结果全部可追溯）。

> 命令为非交互式，不要用 `vim`/`top`/`ssh` 等需交互的程序。每步是独立进程，`cd`/`export` 不跨步骤保留——连续操作写同一步内用 `&&` 串联。

**故障闭环演示清单（约 15 分钟）**

1. 构造告警或手动开事件 → 打开事件详情  
2. 「AI 诊断」→ 对话区顶部出现**诊断证据链**卡片（来源类型 / 标题 / 摘要）  
3. 「生成修复提案」→ 待审修复出现在详情闭环条与「自动修复」列表  
4. （可选）开一条**变更冻结窗**覆盖该主机 → 自愈/剧本预检显示「冻结中」，未确认不可直跑  
5. 在事件详情或自动修复页**批准**执行  
6. 「升级工单」→ 闭环条显示工单状态 → **关闭工单**后事件回写为已解决  
7. 另测：主机巡检报告页点「AI 分析」，Assist 带入 findings 上下文  

### 4.5 安全与权限

对应官网分组 **05 安全与权限**。

**多用户 RBAC（三级角色）**

- **admin**：全部权限，含用户管理（创建/编辑/删除/重置密码/解绑 MFA）
- **operator**：除用户管理外的所有操作（终端/剧本/配置/主机删除）
- **viewer**：仅查看；可管理自己的资料/密码/MFA
- 路由级拦截：每个 API 请求经 `authMiddleware` → `routeAllowed` 检查权限

**MFA 两步验证**：TOTP（RFC 6238，兼容 Google Authenticator），启用后登录与敏感操作需密码 + 6 位动态码。经 OIDC / 飞书 / 钉钉 / 微信 / 企业微信 SSO 登录时**跳过本地 TOTP**（信任 IdP 侧认证）。

**多提供商 SSO（安全中心 → 单点登录，Tab 切换）**

| 提供商 | 说明 | 回调 URL |
|---|---|---|
| OIDC | Keycloak / Azure AD / Okta 等；回调校验 ID Token（JWKS / iss / aud / exp / nonce） | `{PublicURL}/api/v1/auth/oidc/callback` |
| 飞书 | 开放平台网页应用 OAuth | `{PublicURL}/api/v1/auth/feishu/callback` |
| 钉钉 | 应用 Client ID/Secret OAuth | `{PublicURL}/api/v1/auth/dingtalk/callback` |
| 微信 | **开放平台网站应用**扫码（`snsapi_login`） | `{PublicURL}/api/v1/auth/wechat/callback` |
| 企业微信 | 自建应用扫码（CorpID + Secret + AgentId） | `{PublicURL}/api/v1/auth/wecom/callback` |

- 首次登录可按配置自动建本地用户，并以 `(provider, open_id/union_id/userid/sub)` 永久绑定（有 union_id 时优先）。
- **手动绑定已有账号**：个人信息 →「单点绑定」Tab，对已登录用户走 `?bind=1` 授权后写入身份，无需依赖自动建号。
- 各提供商独立默认角色与可选部门→角色映射；与告警用的飞书/钉钉 Webhook **凭证分离**。
- 登录页根据已启用提供商显示对应按钮。

**账户找回（未登录即可完成，双重验证防枚举）**

| 步骤 | 说明 |
|---|---|
| ① 邮箱验证码 | 输入已绑定邮箱 → 收 6 位验证码 → 输入验证 |
| ② MFA 动态口令（可选） | 若账户已启用 TOTP，进一步输入 6 位动态口令作为第二因素 |
| ③ 获取结果 | 双重验证通过后显示用户名（找回用户名）或签发一次性重置令牌（重置密码，15 分钟有效） |

> 验证码：6 位随机数、10 分钟 TTL、单次使用、错误 5 次作废、60 秒发送间隔。丢手机时可通过绑定邮箱验证码解除 MFA。

**机器指纹鉴权**：Agent 注册时绑定机器指纹（machine-id + 主 MAC 的 SHA-256 前 12 位），后续上报与终端通道均按指纹鉴权，**不再依赖安装 Token**——Token 轮换后已装 Agent 无需更新配置（7 天宽限期）。多服务端场景每个服务端独立校验。

**其他安全机制**：登录限流（默认 5 分钟 8 次）、会话 `HttpOnly`+`SameSite=Lax`（HTTPS 下加 `Secure`）、请求体上限 2 MiB、安全响应头（nosniff/DENY/no-referrer）、密钥脱敏回显、主机身份防克隆（克隆镜像自动重生 `host_id`）。

### 4.6 部署与架构

对应官网分组 **06 部署与架构**。

- **单二进制服务端**：约 15MB；配套自托管 **PostgreSQL + VictoriaMetrics**（Compose 一键拉起），一台小机器即可跑通全栈。
- **持久化**：配置/用户/审计/事件/工单等进 PostgreSQL；指标进 VictoriaMetrics；支持配置迁移与一键回滚（Revert）。
- **实时数据推送**：WebSocket 实时推送，网络异常自动降级为轮询，恢复后无缝重连；gzip 8-10 倍压缩降低带宽。
- **PWA 离线访问**：可安装到桌面（含手机），独立窗口运行；App Shell 离线缓存，断网仍看最后已知状态。
- **多服务端推送**：单 Agent 同时向多个服务端推送，**采集一次广播所有**；各端独立鉴权/重试。配置见 [五、配置参考](#五配置参考) 的 `servers` 数组，或面板「安装 Agent」弹窗勾选「多服务端推送」。
- **网关中继模式（Relay）**：内网仅一台联网机器代理所有请求到云端，二进制/上报/终端自动穿透：

  ```bash
  # ① 网关机器（能联网）
  curl -fsSL "https://cloud-server/install-relay.sh?token=TOKEN" | sudo sh
  # ② 内网机器（经网关间接上报）
  curl -fsSL "http://<网关IP>:8529/install.sh?token=TOKEN" | sudo sh
  ```

  > Relay 与多服务端推送互斥：Relay 是「一台机器代理所有请求到单一上游」，多服务端是「一台机器主动推送到多个上游」。

### 4.7 扩展能力（Python 插件）

对应官网分组 **07 扩展能力**。插件 = 一个可执行脚本，向 stdout 打印 JSON 对象。用 SDK 只需几行：

```python
# plugins/my_check.py
from plugin_sdk import Plugin

p = Plugin()
p.metric("mysql.connections", 42)          # 自定义指标（gauge）
p.metric("mysql.qps", 1350.5)
p.event("warning", "主从延迟 8s")           # 事件（info | warning | critical）
p.emit()                                   # 输出 JSON
```

放进 `plugins/` 目录即自动发现，按 `--plugin-interval` 周期执行。插件崩溃/超时/坏 JSON 只记录跳过，不影响核心采集。非 `.py` 可执行文件也能作为插件，可用任意语言编写。

**进程监控插件**：编辑 `plugins/process_monitor.json`，把要盯的进程名填进 `processes`（子串匹配），进程消失即产 critical 事件：

```json
{ "processes": ["nginx", "mysqld", "redis-server", "java"] }
```

---

## 五、配置参考

### Agent 配置（`config.example.yaml`）

```yaml
server: "http://localhost:8529"
servers:
  - server: "https://monitor-a:8529"
    token: "token-a"
report_interval: 30
plugin_interval: 60
disk_path: "/"
plugins_dir: "plugins"
python: "python3"
state_file: "agent_state.json"
category: ""
token: ""
sni_dns_capture:
  enabled: false
  capture_backend: "auto"
  interface: ""
  tls_metadata_ports: [443, 8443, 9443]
  content_audit: false
  content_audit_ports: [11434, 8000, 8080]
  content_audit_max_body: 4096
  content_audit_body_mode: "redacted"
  content_audit_include_hosts: []
  content_audit_exclude_paths: ["/health*", "/metrics*"]
  content_audit_max_events_per_min: 2000
```

| 字段 | 默认值 | 说明 |
|---|---|---|
| `server` | `http://localhost:8529` | 单服务端地址（`servers` 为空时回退到此） |
| `servers` | `[]` | 多服务端列表，每项含 `server`+`token`；非空时优先 |
| `report_interval` | `30` | 基础指标上报间隔（秒） |
| `plugin_interval` | `60` | 插件执行周期（秒） |
| `disk_path` | `/` | 主磁盘路径（所有本地盘自动识别） |
| `plugins_dir` | `plugins` | 插件目录（可用绝对路径） |
| `python` | `python3` | Python 解释器（Windows 为 `python`） |
| `category` | `""` | 主机分类（面板按此分组） |
| `token` | `""` | 安装 Token（可选） |
| `sni_dns_capture` | 未启用 | DNS/SNI 与受控 HTTP 内容审计；跨平台后端及数据策略见上文 |

### Agent 命令行参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `--server` | 服务端地址 | `http://localhost:8529` |
| `--category` | 主机分类 | 空 |
| `--interval` | 基础指标上报间隔（秒） | `10` |
| `--plugin-interval` | 插件执行周期（秒） | `15` |
| `--plugins-dir` | 插件目录 | `plugins` |
| `--python` | Python 解释器 | `python3`（Win 为 `python`） |
| `--disk-path` | 主磁盘路径 | `/`（Win 为系统盘） |
| `--token` | 安装 Token | 空 |
| `--relay` | 网关中继模式 | `false` |
| `--listen` | Relay 监听地址 | `:8529` |
| `--config` | 配置文件路径 | `config.json` |

> 优先级：命令行 Flag > 配置文件 > 默认值。`servers` 数组非空时优先于 `server`+`token`。

### 服务端配置（`server_config.example.json`）

| 字段 | 默认值 | 说明 |
|---|---|---|
| `alerts_enabled` | `true` | 启用告警推送 |
| `feishu.enabled` / `feishu.webhook` | `false` / `""` | 飞书推送开关与 Webhook |
| `dingtalk.enabled` / `webhook` / `secret` | `false` / `""` / `""` | 钉钉推送开关、Webhook、加签 Secret |
| `thresholds.cpu_warn` / `cpu_crit` | `80` / `90` | CPU 警告 / 严重阈值（%） |
| `thresholds.mem_warn` / `mem_crit` | `80` / `90` | 内存警告 / 严重阈值（%） |
| `thresholds.disk_warn` / `disk_crit` | `85` / `95` | 磁盘警告 / 严重阈值（%） |
| `thresholds.offline_after_sec` | `30` | 主机失联判定秒数 |
| `k8s_clusters` | `[]` | Kubernetes 集群列表（`id`/`name`/`api_server`+`token`+`ca_cert` 或 `kubeconfig_yaml`；密钥落盘加密） |

### 服务端命令行参数

| 参数 | 说明 | 默认值 |
|---|---|---|
| `-addr` | 监听地址/端口 | `:8529` |
| `-config` | 配置文件路径 | `server_config.json` |
| `-dist` | Agent 二进制与 `plugins.zip` 存放目录 | `./dist` |

---

## 六、跨网络部署（Nginx 反代）

用域名 + HTTPS 对外时走 Nginx 反代。普通监控走默认 HTTP 代理即可；但**远程终端、远程桌面、端口转发、Agent 自动升级**都跑在 Agent 拨出的长连接/流式通道上，Nginx 的默认值（不转发 `Upgrade`、双向缓冲、`proxy_read_timeout 60s`）恰好会把它们掐断，症状是「指标正常、终端连不上、Agent 自动升级永远失败」。完整示例见仓库里的 `deploy/nginx-aiops.conf`，最小必需配置：

```nginx
# http {} 层，全局一次
map $http_upgrade $connection_upgrade { default upgrade; '' close; }

server {
    listen 443 ssl;
    server_name monitor.example.com;

    location / {
        proxy_pass http://127.0.0.1:8529;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $http_host;                  # 带端口；写成 $host 会丢端口
        proxy_set_header X-Forwarded-Host $http_host;      # 兜底：面板据此认出"浏览器眼中的自己"
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Port  $server_port;   # 面板开在非标准端口（:8443）时必备
        proxy_read_timeout 3600s;      # 长连接：终端会话不被中断
        proxy_send_timeout 3600s;
        proxy_buffering off;           # 关闭下行缓冲，实时流
        proxy_request_buffering off;   # 关闭上行缓冲 —— 少了这行 Agent 自动升级会一直失败
        client_max_body_size 100m;     # 与服务端 bodyLimit 对齐
    }
}
```

> 面向公网时**务必**置于反向代理之后并启用 HTTPS。远程终端为双鉴权（浏览器登录会话 + Agent Token）。

> **写操作报 403 / 提示「来源校验未通过」**：反代把 Host 改写成了内网地址（`location` 里没写
> `proxy_set_header Host`，nginx 默认发 `proxy_pass` 的目标），或改域名的同时没转发 `X-Forwarded-Host`。
> 读接口是 GET，不过这一关，所以现象是「面板一切正常，一按保存就失败」。按上面的 `Host $http_host`
> + `X-Forwarded-Host $http_host` 配置，或在设置里写死 `public_url` 即可。

> **终端连不上 / Agent 自动升级一直失败，但主机在线、指标正常**：几乎一定是上面那两条缓冲开关没关。
> Agent 的上行通道是一个「请求体持续不结束」的 POST，而 `proxy_request_buffering on`（Nginx 默认）
> 的语义是**把请求体收全了再转发上游**——于是服务端在命令跑完前根本收不到它，判成「Agent 未接单」。
> 服务端能认出这种情形：它会在系统日志里写清要加的那几行，并且**这一轮升级/剧本仍会跑完**，
> 但交互式终端必须把配置改对才能用。

> **非标准端口（如 `https://a.bc.com:8443`）**：`$host` 不带端口，少了 `X-Forwarded-Port` 服务端就只能看到 `a.bc.com`。
> 面板会用地址栏的端口把安装命令补回来，安装脚本里的 `SERVER=` 也由命令里的 `?port=` 兜底；
> 但把上面那行 `X-Forwarded-Port` 加上（或直接在设置里写死 `public_url`）最稳妥。

---

## 七、升级与卸载

**升级 Agent**：停服务 → 替换二进制（和有改动的 `plugins/`）→ 起服务。`host_id` 存在 `agent_state.json`，升级不改变主机身份。

```bash
systemctl stop aiops-agent && cp new/aiops-agent /opt/aiops-agent/ && systemctl start aiops-agent
```

**卸载**：

```bash
# Linux
systemctl disable --now aiops-agent && rm /etc/systemd/system/aiops-agent.service
# Windows（NSSM）
nssm stop AIOps-Agent && nssm remove AIOps-Agent confirm
# Windows（任务计划）
schtasks /Delete /TN "AIOps-Agent" /F
# macOS
launchctl unload ~/Library/LaunchAgents/com.aiops.agent.plist
```

在面板上对下线的主机点卡片右上角 **✕** 可删除（约 60 秒内不会因残留上报而复活）。

---

## 八、常见问题 FAQ

> 对应官网「常见问题」页。

**面板上看不到主机？**
- 检查 Agent 日志有没有「上报成功」；没有多半是连不上服务端。
- 服务端 `8529` 端口是否放行；`--server` 地址（IP/端口/http 前缀）是否正确。
- 网络是否互通：`curl http://<服务端IP>:8529/api/v1/summary`。

**主机出现了，但基础指标是 0？**
- 正常情况下不该发生（三平台均原生采集）。若异常，把 Agent 日志贴出来。
- 极少数最小化系统缺 `/proc`（Linux）或权限受限时，可装 psutil 让 `core_metrics.py` 兜底。

**没有自定义指标 / 插件事件？**
- `plugins/` 没跟 Agent 放一起，或没用 `--plugins-dir` 指对路径。
- 没装 psutil（`svc.*`、`proc.*`、异常检测都依赖它）。

**磁盘只显示一个 / 少了盘？**
- 本地固定盘会全部自动识别。Windows 默认**不含**移动 U 盘、网络映射盘；Linux 只统计 `/dev/` 真实挂载（NFS/CIFS 默认不计）。

**告警没收到推送？**
- 「告警设置」里 Webhook 是否**已保存**且**已勾选启用**；点「发送测试」看返回。
- 飞书关键词 / 钉钉加签是否匹配（见 [4.2 告警与通知](#42-告警与通知)）。

**远程终端连不上（但指标正常）？**
- 走了 Nginx 却没放行 WebSocket。按 [六、跨网络部署](#六跨网络部署nginx-反代) 配置 `Upgrade` 头并关闭缓冲。

**终端里复制不出来？**
- 拖选后按 Ctrl+C，或在高亮上点右键；标题栏的复制按钮任何时候都可用（没有选区时复制整屏）。
- 明文 HTTP 下浏览器不给剪贴板 API，面板会自动走兜底；若连兜底也被浏览器策略挡下，会弹出手动复制框——内容已选中，按 Ctrl+C 即可。
- 想彻底避开这类限制，把面板放到 HTTPS 后面（见 [六、跨网络部署](#六跨网络部署nginx-反代)），浏览器就会开放剪贴板 API。

**部署要不要额外装数据库 / 消息队列？**
- 不需要。服务端单二进制内嵌持久化，零外部依赖。

**数据存在哪、安全吗？**
- 全部落在你自己掌控的 PostgreSQL / VictoriaMetrics，数据主权完全自持，不经任何第三方。

**主机分类不对 / 想改？**
- Agent 端用 `--category` 设定；也可在面板点卡片上的分类标签**手动覆盖**（覆盖优先于 Agent 上报）。

---

## 九、获取支持与联系我们

> 对应官网「联系我们」页。

- **电子邮件**：bigdatasafe@gmail.com（一般 1–2 个工作日内回复）
- **问题反馈**：GitHub Issues — <https://github.com/sreyun/aiops-monitor/issues>
- **开源社区**：GitHub 仓库 — <https://github.com/sreyun/aiops-monitor>

欢迎提交 Issue、PR 或使用反馈，我们会认真对待每一条建议。

---

## 附录 A：与传统方案对比

> 对应官网「产品对比」页。

| 维度 | 传统方案（Prometheus + Grafana + Alertmanager + …） | AIOps |
|---|---|---|
| 组件数量 | 5~6 个独立组件拼装 | 1 个服务端二进制 |
| 外部依赖 | 时序库 / 消息队列 / 多数据库 | 仅 PostgreSQL + VictoriaMetrics（自托管，Compose 附带） |
| 部署时间 | 数小时~数天 | 约 3 分钟 |
| Agent 依赖 | 需 exporter，常需额外运行时 | 单二进制，零第三方依赖 |
| 远程终端 | 需自建 VPN + SSH 堡垒机 | 内置，Agent 反向连接免开端口 |
| 自动化运维 | 需额外 Ansible/SaltStack | 内置可视化剧本编排 |
| 资源占用 | 较高 | 1 核 1G 即可跑 |
| 数据主权 | 视组件而定 | 完全自持 |

---

*本说明书随产品持续更新。最新版本以仓库 [README.md](README.md) 与官网为准。*
