# 安装指南 · INSTALL

> AIOps 开源运维监控平台。本文档覆盖服务端、采集端 Agent 与安卓客户端的安装配置。
> 所有步骤均基于真实架构；如与代码默认行为不符，以代码与 `--help` 输出为准。

---

## 一、环境要求

| 组件 | 要求 | 说明 |
|---|---|---|
| PostgreSQL | 14+ | **必选**。承载关系数据、审计、事件/工单、JSONB 配置、以及 pgvector RAG 向量（`diagnosis_embeddings`）。 |
| VictoriaMetrics | 最新稳定版 | **必选**。承载所有指标时序数据。 |
| Docker | 20.10+（推荐） | 一键编排服务端 + 双存储。 |
| Go | 1.22+（仅源码构建时需要） | 服务端/采集端为零框架、零 CGO 的纯 Go 二进制。 |
| 采集端操作系统 | Linux / Windows / macOS（amd64 + arm64） | 原生采集器；深度适配 openEuler / EulerOS / 麒麟 V10–V11 / Aliyun Linux / Rocky / CentOS / Debian，以及 Windows 10/11、Server 2012–2025（含 Hyper-V）、macOS 12–15。 |

> ⚠️ **双存储强制依赖**：PostgreSQL 与 VictoriaMetrics **两者都必须可用**。服务启动时会校验，缺少任一将直接失败（fail-fast），不会降级运行。

---

## 二、方式一：Docker Compose 一键部署（推荐）

仓库根目录提供：

- `docker-compose.yml` — **正式环境**（拉取 SWR 预构建镜像）
- `docker-compose.dev.yml` — **开发 overlay**（本地 `Dockerfile.dev` 构建）
- `.env.example` — 密钥与端口模板（复制为 `.env`，勿提交）

**正式环境**

```bash
# 推荐：自动下载编排并生成强随机密钥到 .env
bash <(curl -fsSL https://raw.githubusercontent.com/sreyun/openaiops/master/scripts/secure-compose.sh)
docker compose up -d

# 或在已克隆仓库内：
cp .env.example .env          # 修改 POSTGRES_PASSWORD / AIOPS_SECRET_KEY
docker compose up -d
# 可选本机 Agent：docker compose --profile agent up -d
```

**开发环境**

```bash
cp .env.example .env
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build
```

`AIOPS_POSTGRES_DSN` 由 compose 根据 `POSTGRES_PASSWORD` 自动拼接；`AIOPS_VM_URL` 默认指向编排内 VictoriaMetrics。浏览器打开 `http://<服务器IP>:8529`，首次登录后请尽快修改密码并启用 MFA。

---

## 三、方式二：二进制部署

1. 从 [Release](https://github.com/sreyun/openaiops/releases) 下载对应平台的服务端 `aiops-server` 与采集端 `aiops-agent` 二进制。
2. 设置环境变量后启动服务端：
   ```bash
   export AIOPS_POSTGRES_DSN="postgres://aiops:密码@127.0.0.1:5432/aiops?sslmode=disable"
   export AIOPS_VM_URL="http://127.0.0.1:8428"
   export AIOPS_LISTEN=":8529"
   ./aiops-server
   ```
3. 采集端在目标主机运行：
   ```bash
   ./aiops-agent --server https://你的服务端地址 --token <安装令牌>
   ```

---

## 四、关键配置项

| 变量 | 作用 |
|---|---|
| `AIOPS_POSTGRES_DSN` | PostgreSQL 连接串（必填）。 |
| `AIOPS_VM_URL` | VictoriaMetrics 写入/查询地址（必填）。 |
| `AIOPS_LISTEN` | 服务端监听地址，默认 `:8529`。 |
| `AIOPS_TLS_CERT` / `AIOPS_TLS_KEY` | 启用 HTTPS（生产建议前置 Nginx 终止 TLS）。 |
| `AIOPS_SECRET_KEY` | 用于加密静态敏感数据（如终端录制的对称密钥）；请设置为高强度随机值。 |
| `AIOPS_RELAY_SECRET` | 网关中继 (`relay`) 的共享密钥，中继与服务端须一致。 |

---

## 五、采集端 Agent 部署

- **机器指纹**：Agent 启动时基于 `machine-id` + 主 MAC 生成指纹（`X-Agent-Fingerprint`），随每次上报携带，用于防克隆与主机身份识别。
- **安装令牌**：新主机首次接入须携带安装令牌；已知主机凭指纹匹配可免令牌。令牌支持轮换，并为已安装 Agent 提供 **7 天宽限期**，避免轮换期间脱线。
- **多服务端容灾**：Agent 配置 `servers[]` 可并发向多个服务端广播上报，单个服务端不可达时自动断路、退避并重连，保障数据不丢。
- **平台支持**：Linux / Windows / macOS 提供原生采集器（主机/进程/端口/磁盘/网络/DiskIO/IOPS/GPU 等）；SNMP、Redfish、NetFlow、日志等通过插件或独立采集模块补充。

---

## 六、安卓客户端连接

- 安卓 App 为**私有化自托管**分发（未上架应用商店），在 Android Studio 打开 `android/` 构建后安装到内网设备。
- 应用内「设置」页填写你的服务器地址（DataStore 持久化），`usesCleartextTraffic` 允许通过 http 连接内网自托管服务。
- 登录 `POST /api/v1/login` 获取会话，Cookie 双轨持久化；支持登录 MFA 动态口令、终端二次密码；内置自建长连接推送，严重告警可在手机端实时接收。
- 诚实说明：当前沙箱环境未重新编译验证，目录内历史 APK 表明工程可成功构建，但不保证当前源码零编译错误；账号自服务（MFA 自助绑定、忘记密码、首登强制改密）仍在网页端完成。

---

## 七、安全加固建议

- **MFA**：为管理员与操作员账号启用 TOTP MFA（单次使用防重放）。
- **终端二次密码**：远程终端与端口转发操作需二次密码验证，并有失败限流。
- **RBAC**：角色分 `admin / operator / viewer`；远程终端、端口转发、代理等敏感操作要求 `operator+`。
- **合规表述**：平台能力可用于「契合等保审计要求」的运维审计与追溯，相关落地以你所在行业的正式测评为准。

---

## 八、常见问题

**Q：服务起不来，日志提示存储连接失败？**
A：检查 `AIOPS_POSTGRES_DSN` 与 `AIOPS_VM_URL` 是否都可达。两者为强制依赖，缺一不可。

**Q：新 Agent 接入被拒绝？**
A：确认安装令牌正确且在宽限期内；已接入主机凭机器指纹可免令牌重连。

**Q：端口转发提示无权限？**
A：`/proxy` 与 `/forward` 类操作要求 `operator+` 角色；请确认当前账号角色。

**Q：AI 巡检诊断不工作？**
A：AI 诊断依赖已配置的 AI 能力（含向量嵌入维度匹配）；未配置时系统会以启发式诊断兜底，结论仍可参考但不具备 RAG 检索增强。

> 更完整的运维、容灾、备份与排障内容见 [DEPLOY_GUIDE.md](./DEPLOY_GUIDE.md)。
