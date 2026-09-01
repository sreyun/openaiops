# 变更日志

本文件记录 AIOps 公开发布版本的重要变更。

**版本线说明（仅保留有序 v0.x）**

- 历史里程碑：`v0.1.0`–`v0.15.0`（由原 v6.x 序号重置而来，仅保留 v0.x tag）
- 正式发布线：`v0.16.0` → … → `v0.19.67` → **`v0.20.49`**
- 中间补丁已合并归档：`v0.16.1`–`v0.16.7` → **`v0.16.8`**；`v0.18.1`–`v0.18.8` → **`v0.18.9`**；`v0.19.1`–`v0.19.46` → **`v0.19.47`**
- 基线 tag **`v0.19.0`** 保留

---

## [Unreleased]

### 安全加固（P0）
- **SQL 工作台只读校验升级为 AST 白名单**：MySQL 走 Vitess AST（拒绝 DML/DDL 节点、`FOR UPDATE`、`INTO OUTFILE`、`pg_sleep/set_config/lo_export/load_file` 等危险函数）；PostgreSQL 走 fail-closed 词法校验；两种驱动均追加数据库会话级 READ ONLY 兜底（写操作即使绕过上层校验也会被数据库拒绝），并修复 PG `search_path` 事务级失效问题。
- **AI 语音出站加固**：TTS/STT 由裸 `http.DefaultClient` 改为带 120s 超时 + SSRF 出站守卫的客户端。
- **Agent Windows unsafe 修复**：`collector_windows.go` 改用 `unsafe.Slice`；`desktop_clipboard_windows.go` 增加 `handleToPointer` 安全转换；`go vet ./cmd/agent` 清零。
- **发布流水线**：release 构建 Go 版本改为跟随 `go.mod`；Release 附带 `SHA256SUMS.txt` 校验和。

### ⚠️ 升级注意（Breaking Change）
- **docker-compose 不再内置默认口令**：`POSTGRES_PASSWORD` / `AIOPS_SECRET_KEY` 缺失时启动直接失败（`${VAR:?}`）。
  - 升级方式：在仓库目录执行 `bash scripts/secure-compose.sh`（Linux/macOS）或 `powershell -ExecutionPolicy Bypass -File scripts/secure-compose.ps1`（Windows）生成/补写强随机密钥到 `.env`，再 `docker compose up -d`。
  - 已在使用默认口令的存量部署：务必在升级前改密，并同步 `AIOPS_POSTGRES_DSN` 与 PostgreSQL 实例密码。

### AI 闭环与模型路由
- **SRE 效果周报**：每周一 08:00 自动推送近 7 天闭环率 / AI 验证通过率 / 采纳率 / MTTR / 变更失败率 / 告警噪音 / Skill 与记忆命中（`duty_report.go`），并沉淀 RAG 记忆（`effect:weekly`）。
- **模型路由与成本护栏**：新增 `resolveModelRoute`（任务映射 / cheap_model 路由 + 每模型单价）、`EstimateQueryCost` 与 `costGuardrailOK`；`AIConfig` 新增 `ModelPricingJSON`、`MaxCostPerQueryCNY`。

### 文档
- 新增 `docs/engineering/scale-1000-hosts.md`（千台部署手册）、`docs/aiops-ai/closed-loop-weekly-report.md`（AI 闭环周报）、`docs/commercial/pricing-model.md`（定价模型）、`docs/compliance/china-sovereignty.md`（信创合规）、`docs/enterprise/edition-roadmap.md`（企业版路线图）；`docs/README.md` 已建立索引。

---

## [v0.20.49] — 2026-09-01

### 文档

- **README**：新增 Agent OTA 自动升级专章（自动入队、SHA-256 校验、维护窗、批量推送、Nginx 反代注意）；对比表与架构图同步。
- **多语言简介**（`docs/i18n/`）：版本号、仓库地址、许可证（AGPL-3.0）与 OTA 能力对齐主 README。
- **使用指南**（`docs/guides/user-guide.md`）：§七 改为 OTA 优先；§六 Nginx 补充 Agent 升级缓冲配置；FAQ 修正 PG/VM 依赖表述并新增 OTA 排障。

---

## [v0.19.67] — 2026-08-05

### 变更

- **CI / embed**：将 `cmd/agent/config_example.yaml` 纳入版本库（此前被 `.gitignore` 忽略导致 `go:embed` 在 CI `go test` 失败）；CI 增加 `go generate ./cmd/agent`。

---

## [v0.19.66] — 2026-08-05

### 变更

- **远程终端**：非 root Agent 的权限提示改走长连接 TX banner，避免 `termSendPlain` 一次性 POST 触发服务端 `sess.close()` 导致「重连中」死循环；PTY 显式 `Ctty`；交互 Shell 优先可写 `$HOME`。
- **Docker Agent**：创建可写 `/home/aiops`（镜像 + compose tmpfs），entrypoint 设置 `HOME`，便于登录 Shell / 历史文件。
- **登录页**：账号框同时支持用户名/手机号；移除「忘记用户名」「手机号登录」切换。

---

## [v0.19.65] — 2026-08-05

### 变更

- **自测修复**：短信登录 OTP 在 MFA 二次校验完成前不再提前作废（修复 MFA+SMS 死锁）；登录短信默认 `{"code":"${code}"}` 模板参数；剧本包导入改走 `playbooks.Upsert` 校验；README/多语言 UTF-8 BOM 清除。
- **AI 闭环一键 Demo**：`POST /api/v1/incidents/{id}/loop/demo`（管理员）自动补诊断证据并跑 dry-run→提案→批准→回验→Skill；事件页「一键 Demo」按钮；`scripts/demo-year1-loop.sh` 修正 cookie/`AIOPS_USER` 并默认 ONE_CLICK。
- **舰队门禁**：`finalizeAgentUpdateJobWhenVerified` 与 SMS OTP 生命周期单测；Agent 更新/回滚确认改用 `uiConfirm`。
- **经典控制台**：剧本/事件列表加载态；版本还原与拒绝执行等高危确认统一应用内对话框。

---

## [v0.19.64] — 2026-08-05

### 变更

- **P0 舰队可靠性**：Linux systemd 统一写入 `aiops-agent`（遗留 `aiops-monitor-agent` 仅作检测/重启回退）；新增热更新/终端权限浸泡清单与 CI 门禁（`cmd/agent` 更新相关测试 + `pending_verify` 生命周期测例）。
- **P1 通知与登录**：短信登录 OTP 真实发送（复用告警 SMS）+ 验证码登录；自定义 Webhook 增加 Slack / Teams 快速模板；内置自愈/巡检剧本包（`playbooks/packs` 导入 API + 控制台按钮）。
- **P1 文档**：README / 多语言 badge 对齐 v0.19.63+；Linux 终端只读边缘说明（nsenter / allow-nonroot / 容器 / Android `/ws/push`）；Year-1 闭环演示脚本 `scripts/demo-year1-loop.sh`。
- **P2 经典 UI**：主机/桌面/剧本等高危确认改用应用内 `uiConfirm`；发布产物增加 `SHA256SUMS.txt`；Compose/示例密钥改为强制 `.env` 注入。

---

## [v0.19.63] — 2026-08-05

### 变更

- **三端自动升级深度自查修复**：Windows 更新 helper 改用 ProgramData 工作目录、任务注册后校验存活再降级 breakaway、避免双启动；soft-retry 360s 且 pending_verify 期间不重入；校验失败立即释放冷却；macOS `--install-service` 后强制 `kickstart`；Linux 遗留升级脚本补 `nsenter` 解锁；模块超时与下载对齐至 600s；dist 清单 SHA256 按 mtime 缓存。
- **Linux 远程终端只读再加固**：nsenter `--wd` 固定宿主机 `/root`（或真实 HOME），nsenter 失败可降级纯 shell；自愈失败不再盲目 restart 死循环；`AIOPS_USER≠root` 安装写入 `allow-nonroot`。

---

## [v0.19.62] — 2026-08-03

### 变更

- **远程终端 root 仍只读深度修复（Linux）**：交互 Shell 经 `nsenter -t 1 -m…` 进入宿主机挂载命名空间；启动自愈 / **自动升级重启助手**均在 PID 1 挂载命名空间内执行 `--install-service` 或原地解锁 unit（修复「热更新后仍只读、重装才正常」：沙箱内 `/etc` RO 导致旧逻辑写不了 unit）；安装脚本校验有效 `Protect*` 并二次解锁。
- **Windows Agent 自动升级彻底加固**：更新 helper 优先以 SYSTEM 计划任务拉起，并带 `CREATE_BREAKAWAY_FROM_JOB`（修复服务停止时 Job 连带杀死 helper、换包永不发生）；脚本写入工作目录、换包重试/`sc.exe` 启停、结果文件回写；遗留脚本扩大安装目录/进程名扫描。

---

## [v0.19.61] — 2026-08-03

### 变更

- **远程终端权限彻底修复（Linux）**：安装默认 `User=root`（不再继承 `SUDO_USER`，避免 vim 编辑 `/etc` 出现 E45 只读）；unit 显式 `ProtectSystem=false`；Agent 启动自愈重写仍带沙箱/非 root 的旧 unit；热更新时优先 `--install-service` 刷新权限；非 root 会话给出提示。
- **移除新版 Web UI**：默认与唯一控制台为经典版；删除嵌入的 Vue/`web/v2` 产物；`/v2` 入口关闭（404）；顶栏「新版 UI」入口移除；`make build` 不再依赖前端构建。

---

## [v0.19.60] — 2026-08-03

### 变更

- **仓库文档布局收敛**：根目录仅保留 `README.md` / `CHANGELOG.md`；多语言简介迁入 `docs/i18n/`；删除旧路径跳转页与 `docs/` 扁平 stub。

---

## [v0.19.59] — 2026-08-03

### 变更

- **文档整理**：`docs/` 按 getting-started / guides / engineering 分类；同步清理公开文档中的控制台实现细节表述；删除迁移说明文档；旧路径保留跳转。

---

## [v0.19.58] — 2026-08-03

### 变更

- **多语言 README（繁中／日／韩／法／德／西／葡／俄）与中英结构对齐**；长文归拢至 `docs/`（安装 / 部署 / 使用 / 转发 / 内容审计）；根目录保留兼容跳转；中英 README 聚焦核心能力与 3 分钟上手，便于社区发现与 star。

---

## [v0.19.57] — 2026-08-03

### 变更

- **远程终端完整权限（Linux/macOS）**：去掉 `ProtectHome=read-only` / `ProtectSystem=strict` / `PrivateTmp` / `NoNewPrivileges` 等会把交互 shell 弄成半只读的沙箱；unit/plist 写入 `SHELL`/`HOME`/`USER`，保证远端终端可写家目录与系统路径、可用 sudo。
- **主机树/系统树自动刷新**：服务端推送 `hosts_changed`；主机加入/离开时自动刷新主机树、系统树及剧本/巡检/安全/硬件/容器等选机树，无需再点手动刷新；合并去重避免双拉与重建风暴。
- **远程运维增强**：远程桌面（JPEG/质量/文件传输/回放）、自动化（YAML 步骤 + 执行日志 + 体检）、终端搜索/历史/文件传输。

---

## [v0.19.56] — 2026-07-30

### 变更

- **Web 控制台稳定性**：修复登录后会话确认；补齐导航与深链；安全中心改为 KPI 摘要展示。

---

## [v0.19.55] — 2026-07-30

### 变更

- **Web 控制台体验**：概览 / 告警 / 主机 / 事件 / 看板 / 拨测等核心视图体验优化；KPI 可点击跳转、列表骨架屏、主题切换、连接态提示、推送优先轮询降频；安全 / SRE / 设置入口完善。

---

## [v0.19.54] — 2026-07-30

### 变更

- **Web 性能与体验**：按视图降载全局轮询；概览 TOP/告警差量更新；安全扫描 running 软刷新；主机树搜索防抖；看板 ECharts 实例复用。
  - 轮询：`overview|hosts|alerts|log|checks|forward` 保持全量；其它视图默认 `summary+hosts`，alerts/activity 45s TTL；dashboard/SRE/安全/k8s 等显式 15–20s；in-flight refresh 合并。
  - 概览：TOP 签名跳过/条形 patch；告警列表走 `diffUpdateList`；KPI/健康卡优先 textContent 更新。
  - 安全：Host/Web 扫描进行中只 patch 进度/历史状态/KPI，状态跃迁再全量 paint；feeds 签名跳过重复 paint。
  - 主机树搜索 250ms 防抖（清空立即响应）；看板时间范围切换复用 ECharts，避免 dispose 闪白。

---

## [v0.19.53] — 2026-07-30

### 变更

- **展示脱敏**：用户可见面禁止出现 `hermes` 与原始主机 ID；统一展示为「主机名 (IP)」。
  - 服务端：`display_redact.go`；AI SSE delta / 终稿 / tool chip `target` 脱敏；系统提示禁止复述 ID / Hermes；审计 actor `hermes`→`ai`。
  - Web：`HostPicker.hostTitle` 不再回退到 id；`filterDisplayContent` 替换 hermes + 已知 host id；安全审计 / 巡检 AI 上下文 / 修复提案上下文去 ID。
- **Android 0.19.9（独立产物）**：仪表盘网格与图表、SQL 查询/变更单、安全 feeds/引擎、终端复制粘贴、桌面剪贴板/滚轮/拖拽修复，同步脱敏规则。

---

## [v0.19.52] — 2026-07-29

### 变更

- **安全加固**：写审批强制绑定 `args_hash`（禁止空哈希万能令牌）；CmdPolicy 段级白名单 + 空列表 fail-closed，并移出默认解释器/下载器；加密失败不再回退明文；MCP sync/test 需 admin。
- **RBAC / SRE**：剧本执行、巡检列表/详情/对比、告警 ack/silence 按主机范围过滤；自动修复 `pending_approval` 占用冷却与限流；OnCall 已 Ack 不再升级。
- **剧本取消竞态**：终端态 sticky（cancel-wins），迟到的成功回写不再覆盖 cancelled。
- **AI**：诊断命令超时/取消时中止对应剧本执行。
- **前端 / i18n**：`esc` 转义单引号；空目标预览不再误显示「全部」；全选在线不误选离线；主机树搜索保焦；zh-CN / en / zh-TW 字典 key 对齐。

---

## [v0.19.51] — 2026-07-29

### 变更

- **剧本目标统一主机树**：去掉「全部主机 / 系统类型」快捷项，改为树形多选 + 全选在线 / 全选可见；空目标不可保存；旧 `all`/`system:` 打开时自动展开为分组/主机勾选。
- **剧本停止自测加固**：服务重启将残留 running/待审批执行标为 cancelled；取消会话不向主机下发 kill。
- **主机/Web 安全轮询**：进度签名跳过无变化请求；扫描进行中不反复拉取 findings 全文，完成后按需取详情。

---

## [v0.19.50] — 2026-07-29

### 变更

- **剧本手动停止**：运行中 / 待审批执行可一键彻底停止；未开始主机不再下发任务，进行中会话关闭并由 Agent `CommandContext` 中止本地进程，**不下发 kill 脚本**，避免额外主机负载。
- **卡住清理**：历史列表可对长期「执行中」记录停止；服务重启后残留的 running 也可标记为已停止。
- **Agent**：新增 `terminal/alive` 探测；巡检模块支持协作取消。

---

## [v0.19.49] — 2026-07-29

### 变更

- **多机剧本 / 巡检性能**：执行轮询默认 `compact=1`（输出预览）；进度签名跳过无变化重绘；大输出截断 +「展开全文」按需拉完整结果。
- **后端节流**：剧本 `host_inspect` 等重模块并发对齐巡检上限；PG 持久化 debounce；巡检入库后剧本侧仅保留短预览；执行列表剥离 stdout。
- **主机巡检 API**：列表/轮询去掉完整 `report`，保留 `findings_brief` / CPU·MEM 摘要；单机详情 `?host_id=` 按需回传全文。
- **安全扫描列表**：主机/Web 扫描历史接口剥离 findings / FIM / 端口明细，详情接口仍返回完整数据。

---

## [v0.19.48] — 2026-07-29

### 变更

- **剧本目标多选**：主机树支持同时勾选多个分组 / 主机 / 系统类型；`target` 以逗号拼接保存，后端 `ResolveTargets` 做并集去重；兼容原单项选择。

---

## [v0.19.47] — 2026-07-29

相对 **v0.19.0** 的累计发布（含原 **v0.19.1–v0.19.46** 全部内容）。中间 tag 已删除，详情见下方归档表。

### 本版新增

- **主机选择统一为分组树**：新增共享 `HostPicker`；剧本步骤目标改为树形选择（全部主机 / 系统类型 / 分组 / 主机），行内同时展示**主机名与 IP**。
- **后端目标选择器**：剧本支持 `folder:ID`（含子分组与 `__ungrouped__`）；校验与 AI 生成提示同步。
- **其它选主机入口对齐**：深度巡检、API 监控「承载主机」、变更窗/变更记录改为树形多选；事件/SLO/日志/拓扑等单选下拉显示 `主机名 (IP)`。
- **样式**：`.hs-pick-*` 全局化，供剧本 / 巡检 / API / 安全中心等复用。

### 亮点摘要（v0.19.1–v0.19.46）

- **AI**：对话闭环调度看板/诊断/导出；附件/语音/图表下钻与永久保留；看板生成韧性与多模型预测；语音测试闭环；设置可发现性与 MCP Streamable HTTP / 只读 SRE·AI 工具；企业级 AI 治理（白名单、路由、A/B、TCO）。
- **Windows / Agent**：安装上报、自动更新闭环、App Control、http→https、Win2012 终端/桌面一批生产修复；Agent 运行日志滚动；ProtectHome / 网关中继保护。
- **安全与基础设施**：主机安全 FIM、威胁情报、Web 扫描；K8s 探活与 Endpoint；Status Page / Playbook 版本 / SLA / 备份 OSS / 密钥轮换；审计链与分区。
- **SQL / 运维**：工作台只读查询、EXPLAIN 解读、慢 SQL 还原与体验、OpenAPI/Knife4j 导入；剧本多系统适配与执行体验。
- **MCP**：`list_hosts`、容器/K8s 查询截断治理；外部 MCP Clients 接入。
- **仓库**：Android / 鸿蒙移出版本库并清历史；`.gitignore` 收紧；版本线 tag 治理。

### 按原版本归档（便于对照）

| 原版本 | 变更要点 |
|--------|----------|
| v0.19.1 | 修复 Grafana 大模板导入与仪表盘 AI 应用 |
| v0.19.2 | 深度适配国产与国际 OS 矩阵，完善 ARM/Hyper-V 与巡检编排兼容 |
| v0.19.3 | Agent 批量远程更新与网关中继商业级加固 |
| v0.19.4 | 主机安全文件完整性监控（FIM）与内容差异 |
| v0.19.5 | 加固 K8s 集群探活与配置编辑体验 |
| v0.19.6 | 修复 Agent 自动更新重启导致终端/远程桌面失效 |
| v0.19.7 | 修复 nologin 服务账号导致 Web 终端不可用 |
| v0.19.8 | K8s Endpoint 显示真实地址并修复 Windows Agent 自动更新 |
| v0.19.9 | 加固 Windows 安装绕过 App Control，主机分类省略显示 |
| v0.19.10 | 安装页自动更新默认开启并加固网关中继/多服务，修复终端桌面与 FIM |
| v0.19.11 | 威胁情报源统一更新通道，主机/Web 安全增强与列表布局修复 |
| v0.19.12 | 修复 Web 终端滚动条遮挡底部提示符与新输出 |
| v0.19.13 | 修复 Windows Agent 安装后不上报与静默失败 |
| v0.19.14 | AI 对话闭环统一调度看板/诊断/导出与反馈 |
| v0.19.15 | 修复 Windows 10/11 与 Server 安装后主机不上报 |
| v0.19.16 | 修复 Web 扫描情报源点击无反应 |
| v0.19.17 | AI 对话增强看板链接、附件预览、可配置语音与对话内图表下钻 |
| v0.19.18 | 修复 Agent http→https 重定向注册 404；增强 AI 界面调度/看板组件/安全防御与自我进化 |
| v0.19.19 | 修复 Windows 终端/远程桌面因 http→https 流式通道卡住无法接入 |
| v0.19.20 | 修复终端长输出后输入区不可见；主机列表默认按 IP 升序 |
| v0.19.21 | 前端设计系统深度优化（全局配色/间距/动效/AI 交互/组件统一） |
| v0.19.22 | 增强 AI 看板生成韧性与多模型预测；默认关闭预测开关 |
| v0.19.23 | 修复看板预测关闭后仍预留未来轴；接入预测自学习调校 |
| v0.19.24 | 补齐 AI 编排依赖，修复 release 构建编译失败 |
| v0.19.26 | 修复 Win2012 Agent 构建（无效 x/exp 版本与 slog API） |
| v0.19.27 | 修复趋势图时间切换抖动与 AI 优化看板无法应用 |
| v0.19.28 | 将 Android 目录移出版本库 |
| v0.19.29 | 修复 Win2012 远程终端乱码与提示符阶梯错位 |
| v0.19.30 | 修复 Win2012 终端 PATH 缺失与管道无回显 |
| v0.19.31 | 彻底修复 Win2012 终端 Path 大小写与输入回显 |
| v0.19.32 | 修复 AI 看板趋势图因 node_* 指标导致大面积空白 |
| v0.19.33 | 修复 Win2012 桌面闪屏重连、终端滚动隐藏输入；强化 Agent 自动更新 |
| v0.19.34 | AI 对话图表永久保留与导出；清爽化输入栏与 README 多语言入口 |
| v0.19.35 | 提升 AI 设置可发现性；容器库存 `updated_at` 统一为 Unix 秒；归档 v0.19.1–v0.19.34 |
| v0.19.36 | 增强 MCP Streamable HTTP 与只读 SRE/AI 工具 |
| v0.19.37 | AI 语音配置增加测试按钮与播报闭环 |
| v0.19.38 | 修复 Linux 远程终端因 ProtectHome/能力边界无法启动 |
| v0.19.39 | 强化 Windows Agent 自动更新闭环，并清理版本线残留 |
| v0.19.40 | 将鸿蒙 App 工程移出版本库 |
| v0.19.41 | 从 Git 历史清除鸿蒙与 Android 工程 |
| v0.19.42 | 保护 aiops-relay，并收紧 ProtectHome 与 Windows 更新校验 |
| v0.19.43 | 企业级治理补齐与 AI/SRE/DB/安全能力提升至挑战者上限 |
| v0.19.44 | SQL 工作台/慢 SQL/EXPLAIN/Agent 日志与剧本体验全面增强 |
| v0.19.45 | 收紧 `.gitignore`，过滤运行时产物与敏感文件 |
| v0.19.46 | MCP Server 增加 `list_hosts` 并治理容器查询截断 |

> 另含文档与杂项：README/LICENSE 多语言同步、评估文档清理，以及若干中间 fix/chore 提交。

---

## [v0.19.0] — 基线（tag 保留）

远程门禁、事件闭环与作用域记忆强化：补齐诊断证据闸门/回验学习、Hermes draft 质量门与 AI 可观测，并深度加强记忆作用域、已验证强化与检索 UI。

---

更早版本请参阅对应历史 tag（如 `v0.18.9`、`v0.16.8`）与 git 提交记录。
