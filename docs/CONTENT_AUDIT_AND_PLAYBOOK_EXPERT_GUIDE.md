# 内容审计、Agent 安装与自动化剧本专家指南

## 1. 结论

Agent 的内容审计采用可插拔被动采集：

- Linux 默认使用原生 `AF_PACKET`；Windows/macOS 使用 Wireshark TShark，底层分别依赖 Npcap 与 libpcap/BPF。
- 可以审计指定端口的明文 HTTP 请求与响应，并重组 `Content-Length`、chunked 和 SSE；支持 IPv4/IPv6。
- 可以从 TLS ClientHello/DNS 中获取目的域名元数据（未启用 ECH 且握手包可见时）。
- 不能从 HTTPS 流量中恢复 prompt、completion、Header 或 URL path。TLS 1.3 的应用数据是 AEAD 密文；没有端点密钥或主动终止 TLS 时，被动抓包只能看到元数据。
- 即使持有服务器证书私钥，也通常无法离线解密采用前向保密密钥交换的 TLS 1.3 会话。
- ECH 普及后，SNI 本身也可能不可见，因此不能把 SNI 抓包当成长期可靠的大模型资产识别方案。

因此，大模型 HTTPS 内容审计的首选不是终端通用解密，而是让大模型调用统一经过应用层 LLM Gateway，在 TLS 终止之后、转发到模型提供方之前和收到响应之后完成结构化审计与 DLP。

## 2. 方案选型

| 方案 | 可见内容 | 准确度 | 主要代价/风险 | 建议 |
|---|---|---:|---|---|
| 被动 DNS/SNI | 域名、IP、端口、时间、流量规模 | 中 | ECH、DoH/DoQ、连接复用会降低可见性 | 保留为资产发现与旁路佐证 |
| 明文 HTTP 抓包 | 请求/响应正文 | 中 | 丢包、乱序、HTTP/2、隐私和主机资源开销 | 仅用于受控内网 Ollama/vLLM 明文端口 |
| LLM API Gateway / 反向代理 | 身份、模型、prompt、completion、tool call、usage、延迟、错误 | 高 | 需要统一调用入口和高可用部署 | **生产首选** |
| 应用 SDK / OpenTelemetry | 调用上下文、业务用户、trace、模型与 token | 高 | 需要改造应用；正文采集须显式配置 | 与 Gateway 组合 |
| 企业 TLS Inspection | 通用 HTTPS 正文 | 中 | 根证书下发、证书固定、QUIC、双向 TLS、法律合规和故障域 | 只用于明确授权、受管终端和限定域名 |
| SSLKEYLOGFILE / TLS uprobe | 特定进程会话密钥或明文 | 低至中 | 运行库/版本强耦合、密钥高敏感、覆盖不稳定 | 故障取证，不作为长期审计主路径 |

### 为什么最初只支持 Linux

原实现直接调用 Linux 专有的 `AF_PACKET`/cBPF，Windows 没有这个内核接口，macOS 则使用 `/dev/bpf`/libpcap。若把 libpcap 通过 CGO 静态绑定进 Agent，会显著增加交叉编译、运行库和驱动分发成本；Windows 仍必须单独安装内核抓包驱动。现在采用两层后端：

- `native`：Linux `AF_PACKET`，零第三方运行依赖，适合服务器与高吞吐节点。
- `tshark`：跨平台字段流适配器，不落地 pcap；Windows 由 Npcap 抓包，macOS 由 libpcap/BPF 抓包。TShark 负责接口差异、分片协议识别与 SNI/DNS 字段解析，Agent 继续负责有界 TCP 重组、策略和上报。
- `auto`：Linux 选择 native；Windows/macOS 选择 tshark。显式选择不支持的 native 会启动失败，而不是静默跳过。

依赖准备：

```text
Windows: 安装 Wireshark + Npcap；受限模式下需管理员权限运行采集
macOS:   安装 Wireshark；普通用户采集建议安装 Wireshark.dmg 中的 ChmodBPF
Linux:   native 需要 root/CAP_NET_RAW；也可显式使用 tshark
```

## 3. 推荐的大模型审计架构

```text
业务应用/用户
    │  企业身份、应用ID、成本中心、请求ID
    ▼
LLM Gateway（终止企业侧 TLS）
    ├─ 请求标准化：OpenAI / Anthropic / Ollama / Azure OpenAI
    ├─ 输入 DLP：密钥、PII、内部敏感词、越权工具参数
    ├─ 策略：模型白名单、区域、预算、速率、最大上下文、tool allowlist
    ├─ 结构化审计：model、operation、stream、token、latency、status、policy decision
    ├─ 内容策略：默认摘要/指纹；命中事件才加密留存受限正文
    └─ 输出 DLP：敏感回显、提示注入结果、危险 tool call
    │
    ▼
外部/内部模型服务（HTTPS）
```

Agent 继续承担主机身份、网络旁证和受控明文端口审计；Gateway 事件通过专用审计入口进入服务端。两者使用统一 `request_id / trace_id / principal / model / provider` 关联，避免把“抓到一个 TCP 流”误当成“确定的业务用户行为”。

### HTTPS/LLM Gateway 结构化摄入

服务端提供专用入口：

```http
POST /api/v1/integrations/content-audit
Authorization: Bearer <AIOPS_CONTENT_AUDIT_INGEST_TOKEN>
Content-Type: application/json
```

部署服务端时通过环境变量设置至少 24 字符的独立随机令牌；未配置时接口返回 `503`，令牌不复用登录 Cookie 或 Agent 安装 Token。示例：

```json
{
  "host_id": "llm-gateway-prod",
  "timestamp": 1784822400,
  "events": [{
    "capture_backend": "gateway",
    "body_mode": "metadata",
    "host": "api.openai.com",
    "path": "/v1/responses",
    "status": 200,
    "principal_id": "user-1042",
    "application_id": "knowledge-assistant",
    "event_id": "audit-01JZ...",
    "request_id": "req-01",
    "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
    "llm_provider": "openai",
    "llm_model": "gpt-5",
    "llm_operation": "responses",
    "input_tokens": 912,
    "output_tokens": 188,
    "latency_ms": 1420,
    "tool_calls": 1,
    "policy_decision": "allow",
    "risk_labels": ["pii_redacted"],
    "req_bytes": 8321,
    "resp_bytes": 2910,
    "req_sha256": "64位十六进制摘要",
    "resp_sha256": "64位十六进制摘要"
  }]
}
```

Gateway 事件未显式提供 `body_mode` 时，服务端按 `metadata` 失败关闭并移除正文；只有显式 `redacted` 或 `full` 才接收相应正文。入口限制为单请求 16 MiB、每批 256 条，并复用内容审计的 DLP、告警、保留与查询链路。`event_id` 在同一 `host_id` 下幂等去重；未给 `event_id` 时 Gateway 事件回退使用 `request_id`。LiteLLM、自建 Envoy `ext_proc`、OpenResty 或应用 SDK 可在 TLS 终止后将各自回调转换为此结构。

### 建议事件字段

- 身份：`principal_id`、`tenant_id`、`application_id`、`source_host_id`
- 调用：`request_id`、`trace_id`、`provider`、`model`、`operation`、`stream`
- 治理：`policy_version`、`decision`、`risk_labels`、`tool_names`
- 用量：`input_tokens`、`output_tokens`、`latency_ms`、`status_code`
- 内容：`prompt_hash`、`completion_hash`、字符数；原文默认不存
- 取证：只有命中策略或明确开启采样时，使用独立 KMS 包络加密保存正文，并记录访问审计

### 数据保护基线

1. 默认最小化：先保存结构化元数据、hash、token 数，不默认永久保存 prompt/completion。
2. 分级保留：普通元数据 30–90 天；命中事件按法规/制度保留；调试正文尽量控制在小时或天级。
3. 职责分离：平台管理员不能默认查看正文；正文查看需要专门角色、理由和二次审计。
4. 密钥隔离：审计正文的 KMS 密钥与业务数据库密钥分离，支持按租户/环境吊销。
5. 可证明删除：按 `tenant / principal / time range` 执行删除，并记录删除任务结果。
6. 质量指标：事件漏报率、采样率、Gateway 绕过率、SNI 与 Gateway 资产差异、DLP 误报/漏报。

## 4. 当前仓库落地能力

内容审计查询会对常见接口做结构化识别：

- OpenAI Compatible：`/v1/chat/completions`、`/v1/responses`、`/v1/completions`、`/v1/embeddings`
- Anthropic：`/v1/messages`
- Ollama：`/api/chat`、`/api/generate`

返回结果附加 provider、model、operation、stream、prompt/completion 字符数、token usage 和 tool call 数。这些字段由查询时解析得到，不额外复制正文。

安装弹窗提供跨平台 DNS/SNI 与明文 HTTP 内容审计配置。服务端对后端、网卡、端口、正文上限、域名/路径规则和事件速率做范围校验；内容审计会自动开启底层采集器，空端口列表被归一化为 `11434, 8000, 8080`，不再退化成“扫描所有 TCP 端口”。

Agent 在上报前执行数据策略：

- `metadata`：不上传正文，只保留可见字节数与 SHA-256，适合常态资产和调用关联。
- `redacted`（默认）：递归遮蔽 JSON 凭据字段，并遮蔽 LLM/AWS 密钥、JWT、Bearer Token、私钥、邮箱、身份证号和手机号；可配置额外字段。
- `full`：显式 break-glass 模式，保留截断范围内的原文；URL 查询凭据仍会移除。
- 域名 allowlist、域名/path denylist、端口白名单和每分钟事件上限在端点执行；事件按 128 条/8 MiB 自动分批。

服务端对摄入再次执行零信任校验：请求最大 16 MiB、每批最多 256 条、字段和正文有硬上限、时间/IP/hash/来源字段归一化。数据库保留 `capture_backend`、`body_mode`、字节数、哈希和端侧脱敏证据，便于证明“采集了什么、以何种策略处理”。

## 5. Agent 安装与配置基线

- 安装包先写入 staging 文件，从同一服务端获取 SHA-256 后校验，再原子替换；失败时保留旧二进制。
- YAML/JSON 解析失败时 Agent 直接退出，不再悄悄使用 `localhost:8529` 继续运行。
- 配置启动校验包含：服务端 URL、采集周期、内容审计平台/端口/正文上限。
- 命令行显式参数优先于环境变量；显式单服务端配置会覆盖文件中的 `servers[]`。
- `config.example.yaml` 必须与 Agent 内嵌副本同步，并由测试校验。

生产建议：

1. Linux 使用 root systemd 服务或仅授予必要能力；高流量节点明确指定 `interface`。Windows/macOS 先用 `tshark -D` 确认接口与采集权限。
2. 服务端使用受信任 HTTPS 证书；自签名场景配置 `ca_cert`，不要长期使用 `tls_skip_verify`。
3. 安装 Token 只用于注册；重置后重新生成安装命令，不在工单/聊天中长期保存完整 Token。
4. 安装后检查 `systemctl status aiops-agent`、Agent 日志中的有效 server、Dashboard 主机身份和 `dropped_30s`。

## 6. Playbook 专家执行模型

剧本保存时执行确定性校验：

- 命令策略、内置模块参数、回滚命令全部受同一安全策略约束。
- 未知目标选择器、非法 register 名、未知变量和前向变量引用会被拒绝，避免变量被静默替换为空串。
- 每步最大尝试 1–6 次，基础设施故障默认重试；非零退出码只有显式 `retry_on_exit` 才重试。
- 每个剧本可限制 `max_parallel`；失败后可按成功步骤逆序执行显式 rollback。

执行前调用：

```http
GET /api/v1/playbooks/{id}/preflight
```

预检返回在线/离线目标、每步风险、变更步骤回滚覆盖、最大并发和告警信息。前端对 Shell/变更类执行给出确定性确认。

示例：

```json
{
  "name": "Nginx 滚动重载",
  "strategy": {
    "max_parallel": 5,
    "auto_rollback": true
  },
  "steps": [
    {
      "name": "变更前状态",
      "module": "service_status",
      "args": {"name": "nginx"},
      "target": "category:生产",
      "register": "before",
      "timeout_sec": 30,
      "max_attempts": 3,
      "retry_delay_sec": 2
    },
    {
      "name": "校验配置并重载",
      "command": "nginx -t && systemctl reload nginx",
      "command_win": "",
      "command_mac": "nginx -t && brew services restart nginx",
      "rollback": "systemctl restart nginx",
      "rollback_mac": "brew services restart nginx",
      "target": "category:生产",
      "timeout_sec": 60,
      "max_attempts": 2,
      "retry_delay_sec": 5
    },
    {
      "name": "变更后验证",
      "module": "service_status",
      "args": {"name": "nginx"},
      "target": "category:生产",
      "timeout_sec": 30
    }
  ]
}
```

### 专家编写原则

1. 每个变更剧本至少有前置检查、变更、后置验证；高风险步骤提供显式回滚。
2. 只有可证明幂等的命令才开启非零退出重试。
3. 小批并发先行，确认错误率、延迟和业务 SLO 后再扩大并发。
4. 只读模块优先于 Shell；模块参数是 argv/结构化输入，更容易审计和跨平台。
5. 不在命令或变量中放密码、Token、私钥；使用受控凭据系统或环境注入。
6. `continue_on_error` 仅用于互不依赖的诊断步骤，变更链默认失败即停。
7. AI 预检是补充；命令策略、确定性 preflight、审批和变更窗口才是强制控制面。

## 7. 上线验收

- HTTPS 抓包测试必须证明正文不可见，且 UI 明确显示能力边界。
- Ollama/vLLM 明文测试覆盖分包、重传、chunked、SSE、截断与敏感标签。
- LLM Gateway 测试覆盖流式/非流式、tool calls、多 provider、DLP 阻断、超时和重试。
- Agent 安装测试覆盖 Linux amd64/arm64、Windows 管理员/非管理员、macOS launchd、校验和不匹配、坏 YAML。
- Playbook 测试覆盖未知变量拒绝、预检风险、并发上限、基础设施重试、非幂等退出不重试、逆序回滚与回滚失败审计。

## 8. 规范与工具参考

- TLS 1.3：[RFC 8446](https://www.rfc-editor.org/rfc/rfc8446.html)
- Encrypted ClientHello：[RFC 9849](https://www.rfc-editor.org/rfc/rfc9849.html)
- TShark 命令行与字段输出：[Wireshark TShark Manual](https://www.wireshark.org/docs/man-pages/tshark.html)
- Windows 抓包驱动：[Npcap Reference Guide](https://npcap.com/guide/)
- macOS 抓包权限：[Wireshark macOS / ChmodBPF](https://www.wireshark.org/docs/wsug_html_chunked/ChBuildInstallOSXInstall.html)
- GenAI 结构化语义：[OpenTelemetry GenAI Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/)
- 代理外部处理：[Envoy External Processing Filter](https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/filters/http/ext_proc/v3/ext_proc.proto.html)
