# 端口转发使用指南

AIOps 提供两种端口转发模式：**TCP 端口映射** 和 **HTTP 反向代理**。两者均通过 Agent 反向隧道实现，无需在被监控主机上开放额外端口。

---

## 一、TCP 端口映射

将服务端的本地端口映射到被监控主机的指定端口，适用于 MySQL、Redis、SSH 等任意 TCP 协议服务。

### 创建转发

```bash
curl -X POST http://localhost:8529/api/v1/forward \
  -H "Content-Type: application/json" \
  -H "Cookie: aiops_session=<your-session>" \
  -d '{
    "host_id": "abc123",
    "target_port": 3306,
    "local_port": 13306
  }'
```

**参数说明：**

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `host_id` | string | 是 | 被监控主机 ID（在主机列表中查看） |
| `target_port` | int | 是 | 目标主机上的端口号（1-65535） |
| `local_port` | int | 否 | 服务端本地监听端口（0 = 自动分配） |
| `whitelist_enabled` | bool | 否 | 是否启用来源 IP 白名单（默认 `false`） |
| `whitelist` | string[] | 条件 | IP 或 CIDR 列表；启用白名单时至少一条 |

**响应示例：**

```json
{
  "id": "a1b2c3d4",
  "host_id": "abc123",
  "hostname": "web-server-01",
  "target_port": 3306,
  "local_port": 13306,
  "listen_addr": "127.0.0.1:13306",
  "status": "active",
  "created_at": 1720473600,
  "operator": "admin",
  "sessions": 0
}
```

### 来源 IP 白名单（可选）

对**服务端对外监听的转发端口**限制来源 IP（TCP / UDP 同一策略）。默认关闭；开启后仅白名单内地址可 `Accept` / 收包，未命中直接关闭连接或丢弃报文。

```bash
curl -X POST http://localhost:8529/api/v1/forward \
  -H "Content-Type: application/json" \
  -H "Cookie: aiops_session=<your-session>" \
  -d '{
    "host_id": "abc123",
    "target_port": 3306,
    "local_port": 13306,
    "whitelist_enabled": true,
    "whitelist": ["203.0.113.10", "10.0.0.0/8", "192.168.1.0/24"]
  }'
```

编辑时同样可热更新（无需重绑端口）：

```bash
curl -X PUT http://localhost:8529/api/v1/forward/a1b2c3d4 \
  -H "Content-Type: application/json" \
  -H "Cookie: aiops_session=<your-session>" \
  -d '{
    "host_id": "abc123",
    "target_port": 3306,
    "local_port": 13306,
    "whitelist_enabled": true,
    "whitelist": ["10.0.0.0/8"]
  }'
```

**规则：**

- `whitelist_enabled=false`（默认）：不校验来源，行为与现网一致
- 开启且名单为空：拒绝创建/更新（避免误配成「全拒」）
- 支持单 IP 与 CIDR（IPv4 / IPv6）；最多 64 条
- 控制台「新建/编辑转发」中白名单区块默认收起，按需展开

### 使用转发

创建后，用任何 TCP 客户端连接 `127.0.0.1:13306` 即可访问目标主机的 3306 端口：

```bash
# MySQL
mysql -h 127.0.0.1 -P 13306 -u root -p

# Redis
redis-cli -h 127.0.0.1 -p 13306

# SSH（需目标主机开启 SSH）
ssh -p 13306 user@127.0.0.1
```

### 查看活跃转发

```bash
curl http://localhost:8529/api/v1/forward \
  -H "Cookie: aiops_session=<your-session>"
```

### 关闭转发

```bash
curl -X DELETE http://localhost:8529/api/v1/forward/a1b2c3d4 \
  -H "Cookie: aiops_session=<your-session>"
```

### 查看统计

```bash
curl http://localhost:8529/api/v1/forward/stats \
  -H "Cookie: aiops_session=<your-session>"
```

**响应：**
```json
{
  "active_sessions": 3,
  "total_sessions": 127,
  "total_bytes": 5368709120,
  "errors": 2,
  "max_sessions": 300
}
```

---

## 二、HTTP 反向代理

通过 `/proxy/{hostID}/{port}/{path}` 路径直接代理 HTTP 请求到目标主机，无需创建规则。支持所有 HTTP 方法、请求体、WebSocket 升级。

### GET 请求示例

```bash
# 代理到目标主机 8080 端口的 /api/health 路径
curl http://localhost:8529/proxy/abc123/8080/api/health
```

### POST 请求示例

```bash
curl -X POST http://localhost:8529/proxy/abc123/3000/api/users \
  -H "Content-Type: application/json" \
  -d '{"name":"test","email":"test@example.com"}'
```

### 带认证头的请求

```bash
curl http://localhost:8529/proxy/abc123/9200/index/_search \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"query":{"match_all":{}}}'
```

### WebSocket 示例

```bash
# 使用 websocat 或 wscat
websocat ws://localhost:8529/proxy/abc123/8080/ws
```

### 浏览器中使用

直接在浏览器中打开：
```
http://localhost:8529/proxy/abc123/8080/
```

### URL 格式

```
/proxy/{hostID}/{port}/{path...}?{query}
```

| 部分 | 说明 | 示例 |
|------|------|------|
| `hostID` | 被监控主机 ID | `abc123` |
| `port` | 目标主机端口号 | `8080` |
| `path...` | 请求路径（可含子路径） | `api/v1/users` |
| `query` | URL 查询参数（原样传递） | `?page=1&size=20` |

### 自动添加的转发头

代理会自动添加以下 HTTP 头：

| 头 | 值 | 说明 |
|----|----|------|
| `Host` | `localhost:{port}` | 目标主机地址 |
| `X-Forwarded-For` | 客户端 IP | 原始请求者 IP |
| `X-Forwarded-Proto` | `http` / `https` | 原始协议 |
| `X-Real-IP` | 客户端 IP | 同 X-Forwarded-For |

其他请求头（Content-Type、Authorization、Cookie 等）原样转发，hop-by-hop 头（Connection、Transfer-Encoding 等）被过滤。

---

## 三、健康检查

```bash
curl http://localhost:8529/api/v1/forward/health \
  -H "Cookie: aiops_session=<your-session>"
```

**响应：**
```json
{
  "enabled": true,
  "max_body": 104857600,
  "max_session": 300
}
```

---

## 四、两种模式对比

| 特性 | TCP 端口映射 | HTTP 反向代理 |
|------|-------------|-------------|
| **适用协议** | 任意 TCP（MySQL/Redis/SSH） | HTTP / WebSocket |
| **是否需创建规则** | 是（POST /api/v1/forward） | 否（直接请求 /proxy/...） |
| **连接模式** | 长连接（持续保持） | 每请求一次（无状态） |
| **本地端口** | 占用 127.0.0.1:port | 不占用（通过 HTTP 路由） |
| **WebSocket** | 支持（原始 TCP） | 支持（自动检测 Upgrade 头） |
| **审计日志** | 建立时 + 关闭时 | 每次请求 |
| **空闲超时** | 30 分钟自动关闭 | 无（请求结束即关闭） |
| **最大请求体** | N/A | 100 MB |
| **最大并发** | 300 会话 | 300 会话 |

---

## 五、架构说明

```
[用户/浏览器]
    │
    ├── TCP 模式 ──→ [Server: TCP Listener 127.0.0.1:port]
    │                    │
    │                    ├── 创建 forwardSession
    │                    ├── 通知 Agent (长轮询)
    │                    └── 双向流: 用户↔Agent↔localhost:targetPort
    │
    └── HTTP 模式 ──→ [Server: /proxy/{hostID}/{port}/...]
                         │
                         ├── 创建 forwardSession
                         ├── 构造原始 HTTP 报文
                         ├── 通知 Agent (长轮询)
                         └── 隧道传输: 请求→Agent→目标 HTTP 服务
                                          响应←Agent←目标 HTTP 服务
```

Agent 端无需开放入站端口——它主动长轮询服务端的 `/api/v1/agent/forward/wait`，收到会话后拨号 `localhost:targetPort` 并建立双向数据流。
