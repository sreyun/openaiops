# AI 对话入口归一与闭环设计

目标（用户原话）：「AI 对话入口合并，后端和前端都要归一，从而彻底闭环 AI 对话、AI 诊断、
AI 决策与分析能力」。

**先纠正一个前提**：我一开始也以为 `/ai/assist` 与 `/hermes/chat` 是两套重复实现，读完
代码发现不是——它们是**两层**，重复的只有界面。方案因此不是「二选一」，而是「明确分层 +
把三个对话入口合成一个 + 把断掉的动作闭环接上」。

---

## 一、现状（读码得到，非推测）

| | `/api/v1/ai/assist` | `/api/v1/hermes/chat` |
|---|---|---|
| 形态 | 任务化**一次性** SSE | **有状态**自主体会话 |
| 入参 | `task` + `input` + `context` + 最多 20 轮短历史 | 会话 id + 消息 |
| 能力 | 生成/解释/分析，**不执行动作** | Function Calling 工具、审批、撤销、规则、模板 |
| 会话 | 无（前端自己带 history） | 持久化，可 `/undo` |
| 调用点 | **9 处**，遍布两个控制台（看板、硬件、SQL、SRE、剧本…） | Hermes 页与全局 dock |
| 反馈 | `assist_id` → `/ai/assist/feedback` → RAG 记忆 | **复用同一条**反馈链路 |

也就是说：`/ai/assist` 是**全站 AI 能力的底座**（一个按钮就能就地问一句），`/hermes/chat`
是**能动手的那一层**。删掉任何一个都会丢能力。

真正的重复在界面：

1. `/hermes` 页面（`HermesChat.vue`，`variant="page"`）
2. 全局 Hermes dock（同一个组件，`variant="dock"`）
3. **`SreView` 的「AI 助手」标签**——另写了一套消息状态、附件、流式解析，走 `/ai/assist`

第 3 个是纯粹的重复实现：同样的对话框、更少的能力（没有工具、没有审批、没有会话），
而用户没有任何线索判断该用哪个。

## 二、断掉的闭环

「对话 → 诊断 → 决策 → 分析」这条链，前两段是通的，后两段是断的：

- **诊断**：新建 critical 事件会自动触发 `autoDiagnose`，结论挂到事件时间线。✅
- **决策**：AI 给出的结论**只能被读**。要据此建工单 / 触发自愈 / 关联变更，得人工离开
  对话、去另一个页面、把结论重新敲一遍。
- **回验**：动作做完之后，AI 不知道自己的建议是否奏效——`/ai/assist/feedback` 只收
  「有用 / 没用」的人工点击，没有**客观结果**回流。（对照：自愈侧刚补上的告警回验
  已经产出了客观信号，见 `remediation.go` 的 `Verify` 字段。）

## 三、方案

### 分层（后端「归一」的真实含义）

不合并两个端点，而是**把它们的共享部分归一**，各自只保留不可替代的那一半：

```
        ┌──────────── 共享层（归一目标）────────────┐
        │ 上下文构建 · 提示模板 · 模型路由与成本护栏 │
        │ assist_id · 反馈 · RAG 记忆 · run 追踪     │
        └───────────┬───────────────────┬───────────┘
                    │                   │
        /ai/assist（一次性·无副作用）  /hermes/chat（有状态·可动手）
```

- 提示模板与上下文构建目前各写各的，先并到一处（`sreyun.go` 已有热加载模板机制，
  `/ai/assist` 没用上）。
- run 追踪（`ai_runs.go`）目前只覆盖一侧，两侧都要落同一张表，成本与调用统计才对得齐。

### 前端归一：三个入口 → 一个

- **删掉 `SreView` 的「AI 助手」标签**的自建对话实现，改为调起全局 Hermes dock，并把
  SRE 上下文（当前事件/主机/时间窗）预置进去。机制已经存在：`hermesBriefing` store 的
  `enqueue({task, title, message, autoSend})`，Hermes 页的「闭环演示」就是这么用的。
- 保留全站「AI 辅助」按钮（`/ai/assist`）——它是就地问一句，不是对话，不该被合并掉。
- 结果：**一个对话入口（dock，随处可唤起）+ 一个就地辅助按钮**，语义清晰。

### 闭环：让结论能变成动作

在 Hermes 的回答下方给出**基于结论的动作**，而不是让人另开页面：

| 动作 | 落到哪 | 已有的接口 |
|---|---|---|
| 建工单 | `POST /tickets`，带 AI 结论与来源 run_id | ✅ 已有 |
| 提自愈 | `remediationManager.ProposeManual`（挂 pending_approval，人工批准后执行） | ✅ 已有 |
| 关联变更 | `POST /changes/{id}/link` | ✅ 已有 |
| 记入事件时间线 | `incidents.AddEvent` | ✅ 已有 |

所有写动作都必须走既有的 `approval_id` 强制审批（见 `docs/ci-gate.md`），不新增旁路。

**客观回验**：动作产生的结果（工单是否解决、自愈是否让告警消除——自愈侧已有 `Verify`）
回流成 AI 记忆的 `verified` 标记，而不是只靠人点「有用」。这才是「决策能力闭环」。

## 四、分阶段

| 阶段 | 内容 | 风险 | 状态 |
|---|---|---|---|
| A | SreView 的 AI 标签改为唤起 dock + 预置上下文；删掉自建对话实现 | 低，可单独发 | 部分 |
| B | 回答下方的动作区（建工单 / 提自愈 / 关联变更），全部复用既有接口 + 审批 | 中 | ✅ 完成 |
| C | 提示模板与上下文构建归一；两侧 run 追踪落同一张表 | 中，纯后端 | 部分 |
| D | 客观回验回流记忆（工单解决 / 自愈 Verify → memory.verified） | 中 | ✅ 完成 |

建议顺序 A → B → D → C：A 立刻消除「该用哪个」的困惑，B 让闭环第一次真正闭上，
D 让它自我改进，C 是内部整洁度，收益最慢。

### 落地记录（2026-08-16）

**B 已完成**——两侧控制台都能把结论一键转成动作：

- 后端 `cmd/server/ai_followup.go`：`POST /api/v1/ai/followup` 执行 create_ticket /
  add_incident_note / link_change；propose_remediation 是纯导航，仍进既有审批流。
  正文一律取 `AIRun.Answer` 服务端原文，客户端只能给标题这类短标签。
- 动作区由 `emitAIFollowupActions` 以 SSE `action` 帧下发，格式与工具产出的
  `_ui_actions` 一致，前端不需要第二套解析；`opsAllowedUI` 白名单同步放行。
- 门控：只读角色不给按钮，无 run_id 不给，回答短于 80 字不给（避免寒暄挂满按钮）。
- `persistAIRun` 改为所有 kind 都进热缓存——PG 落库是异步的，而按钮就挂在刚吐完的
  回答下面，只缓存 assist 会让 Hermes 的动作随机撞上「run 已过期」。
- 前端：经典控制台 `web/js/sre.js`（出厂 UI）+ Vue 侧 `ai-chat-actions.ts` /
  `AiChatWidgets.vue`；会话可绑定 `incident_id`，绑了才出现事件级三个动作。三语齐备。

**D 已完成**——`cmd/server/ai_followup_learn.go`，接的是两个不需要人表态的客观信号：

| 信号 | 触发点 | 回流 |
|---|---|---|
| 由 AI 结论建出的工单被解决/关闭 | `handleUpdateTicket` | 结论作为 **verified** `resolution` 记忆入库（source `ai_run:<id>`）并强化 |
| 自愈回验 `Verify == "cleared"` | `remediationManager.onVerify` | 剧本经验升格 verified + 强化；事件诊断记忆标记 verified |
| 自愈回验 `still_firing` | 同上 | 剧本经验与事件诊断按 `penalizeUnhelpful` 下沉——跑得完但修不好的剧本必须在检索里降权 |

- 工单靠新增的 `Ticket.AIRunID` 找回来源；该字段**服务端专用**，`Create` 会清掉客户端
  传来的值，否则任何人都能声称 AI 出处，把任意结论刷成 verified。
- 记忆只在**回验之后**才写，不在建工单那一刻写：那时还没有任何证据说明结论是对的。
  采纳本身另记为 `applied` 反馈（`recordAIFollowupAdoption`），只强化、不入库。

**C 的一半已经是现状**：`/ai/assist`（`ai_orchestrator.go`）与 `/hermes/chat`
（`sre_api.go`）都已 `persistAIRun` 落同一张 `ai_runs`，成本与调用统计本就对得齐。
剩下的只有「提示模板与上下文构建归一」（`/ai/assist` 没用上 `sreyun.go` 的热加载模板）。

**A 的剩余部分**：Vue `SreView` 的「AI 助手」标签仍是自建对话实现。注意 Vue 控制台
自 v0.19.61 起已不随产品发布（见 CLAUDE.md），这一条只影响仓库整洁度，不影响用户所见。
经典控制台侧本就只有一个 AI 对话入口，已通过事件详情的「AI 对话（本事件）」带上下文唤起。

## 五、明确不做

- 不合并 `/ai/assist` 与 `/hermes/chat` 为一个端点。它们的形态（一次性 vs 有状态）
  和调用场景（9 处就地按钮 vs 一个对话）都不同，强行合并会让 9 个调用点被迫背上会话语义。
- 不做「AI 自动执行写操作」。审批门是刻意的，闭环指的是**结论可一键转成待审批的动作**，
  不是绕过人。
