# 商业交付手册

> 面向**签发方 / 实施方**：怎么把这套平台交付给付费客户，怎么签发与管理授权，
> 交付前要检查什么，出问题时售后怎么最快拿到现场信息。
> 客户侧文档看 `docs/DEPLOY_GUIDE.md` 与 `docs/USER_GUIDE.md`。

---

## 一、授权与计量

### 1.1 设计口径（为什么是这样）

- **离线验签**：授权文件是 Ed25519 签名的 JSON，服务端只内置公钥。**不需要联网回调许可服务器**——客户内网多半也不允许。
- **超限降级，不停服**：过期或超限时，采集、告警、Agent 上报一律照常。降级只落在"人发起的写操作"上（非 GET 的 `/api/v1` 请求），并始终放行登录、改密和授权上传本身。监控产品在客户产线上瞎眼，比欠费严重得多。
- **数据摄入永不拦**：Agent 反向通道、Prometheus remote_write、LLM 网关的内容审计上报、只读 MCP 一律放行。拦掉它们等于授权一过期就开始**丢客户的数据**——那些数据补不回来，比停服更糟。
- **默认不强制**：开源自建部署不该被授权层拦住。`licenseEnforceDefault = "0"`，商业交付镜像才打开。
- **绑部署不绑机器**：容器换一次调度 machine-id 就变了。这里绑的是首次启动生成、随 PostgreSQL 持久化的 `install_id`（部署指纹）——搬迁数据库 = 同一套部署，重装数据库 = 新部署。

### 1.2 状态机

| 状态 | 触发条件 | 平台行为 |
|---|---|---|
| `active` | 在有效期内且未超限 | 全功能 |
| `over_quota` | 已登记主机数 ≥ 授权上限 | **拒绝新主机注册**；已登记主机不受影响；其余功能正常 |
| `grace` | 已过期但在宽限期内（默认 30 天） | 全功能 + 控制台横幅 + 每日一条系统日志 |
| `expired` | 过了宽限期 | **人发起的写操作降级为只读**（HTTP 402）；采集/告警/上报/各类数据摄入照常 |
| `invalid` | 签名不符或部署指纹不匹配 | 等同未授权 |
| `unlicensed` | 没装授权文件 | 强制模式下等同 `expired`；非强制模式仅展示 |

判定入口只有一个：`(*Server).licenseStatus()`（`cmd/server/license.go`）。注册准入、只读降级、控制台横幅、`/metrics` 全读它。

### 1.3 签发流程

```bash
go build ./cmd/licensetool

# 一次性：生成签发密钥对。私钥只留在签发方，切勿入库。
./licensetool -gen-key

# 把公钥内置进服务端构建（或用 AIOPS_LICENSE_PUBKEY 环境变量覆盖）
go build -trimpath -ldflags "
  -s -w
  -X main.appVersion=$(git describe --tags)
  -X main.licenseVendorPubKey=<公钥 base64>
  -X main.licenseEnforceDefault=1
" ./cmd/server

# 客户报来部署指纹（控制台「授权与用量」页显示，或诊断包 meta.json 里的 install_id）
AIOPS_LICENSE_PRIVKEY=<私钥 base64> ./licensetool -issue \
  -customer "某某集团" -edition enterprise \
  -max-hosts 200 -expires 2027-08-31 -grace 30 \
  -install-id AIO-XXXX-XXXX-XXXX-XXXX \
  -notes "合同 HT-2026-0821" \
  -out license.txt

# 发出去之前自己验一遍
AIOPS_LICENSE_PUBKEY=<公钥 base64> ./licensetool -verify license.txt
```

客户拿到 `license.txt` 后：控制台 →「设置 → 授权」（Vue 版）或「个人信息 → 数据与备份 → 授权与用量」（经典版）粘贴安装。也可以挂载文件并设 `AIOPS_LICENSE_FILE=/etc/aiops/license.txt`，首次启动会自动导入并落库。

### 1.4 相关开关

| 变量 | 默认 | 作用 |
|---|---|---|
| `AIOPS_LICENSE_ENFORCE` | 跟随构建期 `licenseEnforceDefault`（开源构建为 0） | `1` 打开强制；`0` 只展示与计量 |
| `AIOPS_LICENSE_PUBKEY` | 内置公钥 | 覆盖验签公钥（私有分发方自签） |
| `AIOPS_LICENSE_FILE` | 空 | 首次启动导入的授权文件路径（导入后落 `kv_state`） |

### 1.5 计量

- **当前用量** = 已登记主机数（含离线），与按主机数报价一致
- **历史峰值**单独记录并持久化——续费谈判要的是"这一年最多接过多少台"，只看当下的数字会被客户在续费前一天下线一批机器抹平
- 到期前 30 天起，每天写一条系统日志（`log.license_expiring`），巡检看得见
- Prometheus：`aiops_license_days_left` / `aiops_license_hosts_used` / `aiops_license_hosts_max` / `aiops_license_read_only`

---

## 二、交付前检查清单

实施前逐条打勾，缺一条就不要点交付。

- [ ] **规格核对**：按 `docs/CAPACITY.md` 三档规格确认 CPU/内存/磁盘，并按客户实际主机数复算内存
- [ ] **密钥**：`POSTGRES_PASSWORD`、`AIOPS_SECRET_KEY` 由 `scripts/secure-compose.sh` 生成，且已纳入客户的密钥管理（`AIOPS_SECRET_KEY` 丢失 = 加密配置不可解密）
- [ ] **授权**：授权文件已安装，控制台「授权与用量」显示 `active`，主机上限与合同一致
- [ ] **备份**：每日备份已开启；**「备份范围」里的时序与录像按合同勾选**（默认只备 PostgreSQL）
- [ ] **恢复演练**：`scripts/backup-verify.sh` 跑通并留档（不跑这一条，RTO 就是编的）
- [ ] **指标出口**：`AIOPS_METRICS_TOKEN` 已设置，客户 Prometheus 已抓到 `/metrics`，第三节告警线已配
- [ ] **保留期**：审计/告警/运行历史保留期按客户合规要求设置
- [ ] **升级纪律**：告知 `docker compose pull` 必须带（只 `up -d` 不会换镜像）
- [ ] **账号**：默认口令已改；管理员/操作员/只读三档角色按客户组织划好；主机级 RBAC 按需配置
- [ ] **AI 边界**：如客户不接受数据出网，改用私有模型或关闭 AI；把数据流向写进交付说明
- [ ] **SBOM**：从 GitHub Release 下载 `aiops-server.cdx.json` / `aiops-agent.cdx.json` 交给客户采购

---

## 三、售后：先要诊断包

出问题时**第一件事是要一份诊断包**，不要先申请远程登录——那要 VPN、要审批、要人陪同，一次往返半天。

控制台 → 授权与用量 →「下载诊断包」，或：

```bash
curl -b cookie.txt -o support.zip https://<面板>/api/v1/admin/support-bundle
```

包里有（**不含任何密钥与业务数据**）：

| 文件 | 用途 |
|---|---|
| `meta.json` | 版本、运行时长、部署指纹、Go/OS/CPU、主机与在线数 |
| `config.sanitized.json` | 平台配置，密钥/令牌/DSN 已按与 `GET /api/v1/config` 完全相同的口径打码 |
| `license.json` | 授权状态与用量 |
| `connectivity.json` | PG ping 与连接池、VM 熔断与队列水位、`pgFlush` 延迟 |
| `schema_migrations.json` | 已应用的迁移版本——升级类问题第一句话就是问这个 |
| `hosts.json` | 主机身份与在线状态（不含任何指标） |
| `platform_faults.json` | 平台自身故障（panic / 自诊断） |
| `env.json` | `AIOPS_*` 环境变量；凭据只标记"已设置"，不带值 |
| `activity.log` | 最近活动日志 |
| `goroutines.txt` | goroutine 快照——卡死/泄漏看它 |
| `metrics.txt` | `/metrics` 的一次快照 |

---

## 四、还没做、别承诺的事

写在这里是为了**不让销售在现场把它们说出去**：

| 项 | 现状 | 能说什么 |
|---|---|---|
| 高可用 / 多副本 | 无选主，跑两副本会重复告警与重复执行剧本 | 只承诺"单实例 + 30 分钟内从备份恢复" |
| 多租户 | 只有主机级 RBAC，没有租户隔离 | MSP 场景走"每客户一套实例" |
| 数据库回滚 | `schema_migrate.go` 是 forward-only，没有 down | 升级前自动备份；回滚 = 换镜像 + 还原备份 |
| 双活 / 异地多活 | 不支持 | 跨机房**冷备/切换** |
| 授权在线核验 | 刻意不做 | 强调"离线验签、不回调、不采集客户数据" |

---

## 五、相关文件

| 位置 | 内容 |
|---|---|
| `cmd/server/license.go` | 授权状态机、准入、只读降级中间件、HTTP 接口 |
| `cmd/server/license_test.go` | 状态机与准入的回归测试（判定错一次就是事故） |
| `cmd/licensetool/` | 签发方工具（不交付给客户） |
| `cmd/server/metrics_prom.go` | `/metrics` 指标出口 |
| `cmd/server/support_bundle.go` | 诊断包 |
| `cmd/server/backup_full.go` | 时序 + 录像备份 |
| `scripts/backup-verify.sh` | 恢复演练 |
| `docs/CAPACITY.md` | 容量规格书 |
