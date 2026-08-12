# Year-1 POC 验收清单

POC **只认有回验数据的闭环次数**，不认功能勾选清单。

## 旗舰场景 A：数据库救援

1. 触发慢 SQL / 连接类 critical 告警并生成事件  
2. AI 诊断且时间线带 citations  
3. 事件详情闭环条：`Dry-run` → `提案` → `批准` → `回验` → `沉淀 Skill`  
4. `GET /api/v1/incidents/{id}/loop` 的 `stage` 最终为 `verified` 或 `promoted`  
5. `GET /api/v1/sre/effect` 中 `closed_loop_count` 增加  
6. `GET /api/v1/incidents/{id}/case-export` 可下载案例包（timeline / verify / change / sessions）

## 旗舰场景 B：变更窗应急

1. 配置冻结窗（freeze=true）  
2. 事件开应急变更 / 闭环提案 `mode=emergency_change`  
3. 冻结+高风险禁止直接 `in_progress`  
4. **作者不能自批**（SoD）；管理员可用 `break_glass=1`  
5. 审批后 start/complete，回验关闭事件  
6. 冻结窗内打开终端/桌面：无已批准变更或闭环≥approved 时被拒（管理员 break-glass）

## 旗舰场景 C：业务服务影响面

1. `POST /api/v1/services` 创建业务服务并绑定 `host_ids`  
2. `GET /api/v1/services/{id}/impact` 返回未决事件与近期变更  
3. 变更保存后可查 `GET /api/v1/changes/{id}/impact`

## 三支柱验收补充

| 支柱 | 验收点 |
|------|--------|
| 闭环可信 | `loop/verify` 返回 `checks`（host/alert/remediation/service）；`force=true` 非管理员 403；写审批/工具审计落 PG |
| 远程+闸门 | 冻结或主机有危急未决事件时远程需闸门；会话审计带 `change_id`/`incident_id`；AI 对话可签发写审批 |
| 学习资产 | Hermes 多工具轮次只产 **draft** Skill；`verify_ok` 后 promote 为 **active**；效果看板有 skill/memory 命中与 draft/active 计数 |
| Phase D | 循环冻结窗（daily/weekly）；`GET /hosts/{id}/remote-preflight` 统一闸门芯片；Skill 版本 + 客户包 export/import；作用域（服务/分类）检索过滤 |

## KPI 定义（试点默认）

| KPI | 定义 | 目标 |
|-----|------|------|
| 闭环完成率 | 有 verify_ok 的事件 / 窗口内事件（`closed_loop_rate`） | ≥ 40% |
| AI 验证通过率 | verify_json.ok / 有 verify 的 runs | ≥ 60% |
| AI 采纳率 | feedback ∈ {helpful,applied} / 有反馈的 runs | ≥ 50% |
| MTTR P50/P75/P90 | resolved_at − created_at | P75 相对基线 −20% |
| MTTA P50/P75 | acked_at − created_at | 环比下降 |
| 告警噪声比 | (复开 key + 抖动 key) / 已解决 | 环比下降 |
| 变更失败率 | rolled_back 或完成后 24h 内关联新事件 / 已执行变更 | ≤ 15% |
| 变更 Lead Time P75 | ended/executed − created | 试点观测 |
| Skill 辅助验证率 | skill_hits>0 且 verify.ok / 有 skill+verify 的 runs | 环比上升 |

效果 API：`GET /api/v1/sre/effect?days=14`。

## 配置开关

| 键 | 默认 | 含义 |
|----|------|------|
| `loop_force_allow_non_admin` | false | 为 true 时非管理员可用闭环 force |
| `remote_gate_disabled` | false | 为 true 时关闭远程闸门 |
| `remote_gate_mode` | `freeze_or_highrisk` | 冻结窗或危急未决主机触发闸门 |

## 明确不做（验收范围外）

安卓/鸿蒙客户端源码、完整 OTLP APM、外部 ITSM 双向同步、完整 ITIL CAB。
