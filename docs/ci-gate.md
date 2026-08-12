# CI Gate（安全与 SQL 变更门禁）

本平台将「检测 → 研判 → 审批 → 执行 → 回验」拆开：自动化发现与 AI 摘要不直接改生产；高风险变更必须走变更单。

## 安全扫描门禁

| 阶段 | 行为 |
|------|------|
| 检测 | Agent 加固/CIS-lite + OSV；Web Nuclei；内容审计 DLP |
| 告警 | 危急/高危 Finding、慢 SQL、策略 `deny`/`block` → 通知；危急可建 Incident |
| AI | 可选扫后摘要（`auto_ai_summary`）；不替代引擎检出 |
| 基线 | 与上次完成扫描对比 added/removed/worsened/improved |

建议在发布流水线中：

1. 对预发目标跑 Web 扫描（或复用最近报告），阻断 **critical** 未处置项。
2. 主机镜像构建后跑一次 `host_security_scan`，阻断 **critical hardening / malware**。
3. 内容审计 Gateway 摄入使用独立 Bearer（`AIOPS_CONTENT_AUDIT_INGEST_TOKEN`），策略拦截事件必须告警。

## SQL 变更门禁

| 操作 | 要求 |
|------|------|
| EXPLAIN / Schema / 进程锁 | 只读 |
| 索引 DDL（非 prod） | `allow_exec=true` 或变更单 |
| 索引 DDL（prod） | **必须** propose → approve → 一次性 execute |
| `KILL <pid>` | **必须** 变更单（kind=`kill`），审批后执行 |
| 冻结窗 | Change Window 勾选 category=`sql` 且 `freeze=true` 时，禁止 DDL（KILL 仍可用于解堵） |
| 通用变更挂接 | SQL 变更单创建/审批/执行时自动 upsert `ChangeRecord`（`kind=sql`），双向 `change_id` / `sql_change_ids` |

PostgreSQL 连接（`driver=postgres`）支持测试连通、只读查询、EXPLAIN（禁止 ANALYZE）、慢 SQL（`pg_stat_statements`）、进程/锁、Schema 浏览与健康抽检、以及经变更单的 `pg_terminate_backend`（KILL）。**不提供** DDL 变更执行；数据源类型 `postgres`/`mysql` 可关联 SQL 工具连接，供仪表盘表格面板与 AI `query_datasource` 闭环查询。

## ITSM 轻量闭环（工单 / 变更 / 服务请求）

| 对象 | 说明 |
|------|------|
| OpsLink | 工单/变更/事件上的结构化关联：`host` / `slo` / `sql_change` / `incident` / `ticket` / `change` 等 |
| Ticket.kind | `incident`（事件升级）· `service_request`（目录项）· `task`；`GET /tickets?kind=` 过滤 |
| 服务目录 | `GET /api/v1/service-request/catalog`（默认账号开通/权限/扩容等） |
| ChangeRecord | 状态机 `draft→pending_approval→approved→scheduled→in_progress→completed/rolled_back`；高风险+冻结窗须先审批 |
| 事件联动 | 告警/SLO/慢 SQL 开事件时写 Links；可一键升级工单、开应急变更、关联服务请求 |

## AI 安全与闭环门禁（Wave 1）

| 面 | 要求 |
|------|------|
| 出站 | AI / Embed / Models / WeKnora 一律走 `newGuardedHTTPClient`（拦 metadata/link-local；云上建议 `AIOPS_SSRF_STRICT=true`） |
| 反馈 | `/ai/assist/feedback` **必须**带服务端 `assist_id`，只用服务端原文入库，禁止客户端伪造 answer 投毒 RAG |
| 附件 | Hermes 上传文件默认不进公共记忆；仅显式开启未验证学习时脱敏后写入 |
| 写工具 | 推荐 `POST /api/v1/ai/write-approval` 签发短时 `approval_id`；全局 `hermes_auto_approve` 仍可用但会审计 |
| 配额 | `daily_quota_per_user` 覆盖 Assist / Chat / Sreyun / Diagnose；`quota_exempt_tasks` 可豁免；MCP 另有每分钟限流 |
| 验证 | Assist 的 promql / logql / pgsql 生成后做只读探针，结果经 SSE `meta.verify` 回传 |
| MCP | Bearer + 只读白名单 + Body ≤1MiB + 默认 60 次/分钟；`GET/POST/DELETE /api/v1/mcp` Streamable HTTP（JSON 或 SSE）；调用记入写工具审计 |

Wave 2/3（已落地骨架）：

| 能力 | 说明 |
|------|------|
| `ai_runs` | PG 表 + `GET /api/v1/ai/runs`；Assist/Diagnose/Sreyun 回传 `run_id`；反馈绑定 run |
| 写工具 | **强制** `approval_id`（`POST /api/v1/ai/write-approval`），关闭裸 auto-approve |
| MCP scoped | `mcp_scoped_tokens_json`：`metrics/logs/sql/hardware/infra/knowledge/alerts/sre/ai`；含值班/诊断只读工具，不含 propose_skill 等写偏好工具 |
| 行业知识包 | 内置 mysql/postgres/kubernetes/network；`POST /api/v1/ai/skill-packs/import` |
| On-call Copilot | `GET /api/v1/ai/copilot/context` + 前端「值班助手」 |
| Fallback | `fallback_models` 主模型失败时切换 |
| Eval | `go test` 内 `TestEval*` 黄金用例（离线） |

## Year-1 闭环 / 效果 / 服务树（MVP）

| 能力 | 门禁要点 |
|------|----------|
| 事件闭环 | `POST /api/v1/incidents/{id}/loop/{dry-run\|propose\|approve\|verify\|promote}`；无 citations / 低置信度时拦截提案；`force=true` 默认仅管理员 |
| 证据回验 | `verify` 检查 host/告警/修复运行/服务危急面，结构化写入 `verify_json.checks`；POC 只认 `closed_loop_count` |
| Change SoD | 作者不可自批（`break_glass=1` + 管理员可绕过） |
| 远程闸门 | 冻结窗或主机危急未决时，终端/桌面需已批准 Change 或 loop≥approved |
| 写审批审计 | `POST /api/v1/ai/write-approval`；审批与工具审计双写 PostgreSQL |
| 效果看板 | `GET /api/v1/sre/effect`：MTTR/MTTA、噪声、变更失败率、闭环率、AI 采纳/验证、Skill/记忆命中 |
| 学习资产 | draft Skill 不进检索；verify 后 promote 为 active；案例导出 `GET /incidents/{id}/case-export` |
| 业务服务树 | `GET/POST/DELETE /api/v1/services`；impact API |
| 技能包 | 内置 `incident-loop` / `change-freeze` 等；`POST /api/v1/ai/skill-packs/import` |
| 客户技能包 | `GET /api/v1/ai/skills/export` · `POST /api/v1/ai/skills/import`（默认 draft，需激活） |
| 远程预检 | `GET /api/v1/hosts/{id}/remote-preflight`；终端/桌面/容器共用冻结·闸门徽章；管理员 `break_glass=1` |
| 循环冻结窗 | ChangeWindow `recur=daily\|weekly` + `recur_start_hm`/`recur_end_hm` |
| Skill 作用域 | `service_ids` / `categories`；`POST /api/v1/ai/skills/{id}/scope`；检索按主机/服务上下文过滤 |

## 示例流水线钩子

```bash
# 伪代码：发布前检查最近 Web 扫描是否仍有 open critical
curl -fsS -H "Cookie: ..." \
  "$AIOPS/api/v1/security/overview" | jq -e '.web.open_critical == 0'

# Year-1：效果窗口内至少有可统计的闭环样本（试点阈值按验收文档调整）
curl -fsS -H "Cookie: ..." \
  "$AIOPS/api/v1/sre/effect?days=14" | jq -e '.closed_loop_count >= 0'
```

将上述检查接入 GitHub Actions / GitLab CI；失败则阻断合并或部署。
