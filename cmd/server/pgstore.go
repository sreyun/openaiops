package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"aiops-monitor/shared"

	_ "github.com/lib/pq"
)

// pgFromEnv opens the PostgreSQL store from AIOPS_POSTGRES_DSN, or returns nil if
// it is unset or unreachable (callers then fall back to embedded/file mode).
func pgFromEnv() *pgStore {
	dsn := os.Getenv("AIOPS_POSTGRES_DSN")
	if dsn == "" {
		return nil
	}
	ps, err := openPGStore(dsn)
	if err != nil {
		slog.Error("PostgreSQL 连接失败，回落内嵌存储", "err", err)
		return nil
	}
	return ps
}

// ============================================================================
// PostgreSQL persistence (optional, enabled via AIOPS_POSTGRES_DSN).
//
// When a DSN is configured, the durable SRE records — incidents and work orders,
// which grow over time and benefit from a real database — are persisted to
// PostgreSQL instead of (well, in addition to) the embedded snapshot. Records
// are stored as JSONB rows keyed by id, so the Go structs stay the source of
// truth and no brittle column-per-field migration is needed. When no DSN is set,
// the server behaves exactly as before (embedded snapshot only).
// ============================================================================

type pgStore struct {
	db         *sql.DB
	flowJobs   chan flowJob // NetFlow 明细异步入库队列（解耦 agent POST 与 PG 写入，防连接池饿死）
	flowSpill  chan flowJob // 二级有界重试缓冲
	flowDrop   atomic.Int64
	flowSpillN atomic.Int64
	// wc 让 15 秒一次的 pgFlush 只写「真的变了」的行/blob。没有它，绝大多数周期
	// 写都是内容完全相同的重复 UPDATE，只产生死元组和 WAL（见 pgstore_writecache.go）。
	wc *pgWriteCache
}

// flowJob 是一批待入库的 Flow 明细。
type flowJob struct {
	hostID string
	source string
	flows  []shared.FlowRecord
}

// applyPGSafetyTimeouts 向 DSN 注入连接级安全超时（作为运行时 GUC，随连接建立生效）：
//   - lock_timeout：单条语句等待锁不超过 15s，避免锁等待堆积拖垮连接池；
//   - idle_in_transaction_session_timeout：事务内空闲超 60s 即断开，回收泄漏/挂起的连接。
//
// statement_timeout 可选：AIOPS_PG_STATEMENT_TIMEOUT_MS（默认 60000；0=不设置）。
func applyPGSafetyTimeouts(dsn string) string {
	params := map[string]string{
		"lock_timeout":                        "15000",
		"idle_in_transaction_session_timeout": "60000",
	}
	if ms := strings.TrimSpace(os.Getenv("AIOPS_PG_STATEMENT_TIMEOUT_MS")); ms != "" && ms != "0" {
		params["statement_timeout"] = ms
	} else if strings.TrimSpace(os.Getenv("AIOPS_PG_STATEMENT_TIMEOUT_MS")) == "" {
		params["statement_timeout"] = "60000"
	}
	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return dsn
		}
		q := u.Query()
		for k, v := range params {
			if q.Get(k) == "" {
				q.Set(k, v)
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	// keyword/value 形式：只追加未出现过的参数
	out := dsn
	for k, v := range params {
		if !strings.Contains(lower, k) {
			out += fmt.Sprintf(" %s=%s", k, v)
		}
	}
	return out
}

// openPGStore connects, pings and migrates. A non-nil error means fall back to
// the embedded snapshot.
func openPGStore(dsn string) (*pgStore, error) {
	db, err := sql.Open("postgres", applyPGSafetyTimeouts(dsn))
	if err != nil {
		return nil, err
	}
	// 连接池可通过 AIOPS_PG_MAX_OPEN / AIOPS_PG_MAX_IDLE / AIOPS_PG_CONN_LIFE_MIN / AIOPS_PG_CONN_IDLE_MIN 覆盖。
	applyPGPoolSettings(db)
	ctxPing := make(chan error, 1)
	go func() { ctxPing <- db.Ping() }()
	select {
	case err := <-ctxPing:
		if err != nil {
			db.Close()
			return nil, err
		}
	case <-time.After(10 * time.Second):
		db.Close()
		return nil, sql.ErrConnDone
	}
	ps := &pgStore{db: db, flowJobs: make(chan flowJob, 512), flowSpill: make(chan flowJob, 256), wc: newPGWriteCache()}
	if err := runMigrationPhases(ps.migrateBootstrap, ps.migrateDualTrackPartitions, ps.runVersionedMigrations); err != nil {
		db.Close()
		return nil, err
	}
	if err := ps.backfillPartitionTwins(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	ps.ensureAIExperimentsTable()
	// 2 个后台工作协程串行化 Flow 明细写入：HTTP 摄入只入队即返回，写库不再占住请求连接。
	for i := 0; i < 2; i++ {
		go ps.flowIngestWorker()
	}
	go ps.flowSpillWorker()
	return ps, nil
}

func runMigrationPhases(bootstrap, dualTrack, versioned func() error) error {
	for _, phase := range []func() error{bootstrap, dualTrack, versioned} {
		if err := phase(); err != nil {
			return err
		}
	}
	return nil
}

// flowIngestWorker 从队列取批次并批量写库（见 insertFlowRecords）。
func (p *pgStore) flowIngestWorker() {
	for j := range p.flowJobs {
		p.insertFlowRecords(j.hostID, j.source, j.flows)
	}
}

// flowSpillWorker drains secondary buffer back into the primary queue when capacity frees.
func (p *pgStore) flowSpillWorker() {
	if p == nil || p.flowSpill == nil {
		return
	}
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		for {
			select {
			case j := <-p.flowSpill:
				select {
				case p.flowJobs <- j:
				default:
					// put back and wait
					select {
					case p.flowSpill <- j:
					default:
						n := p.flowDrop.Add(1)
						slog.Warn("Flow spill 回灌失败，丢弃", "host", j.hostID, "rows", len(j.flows))
						if n == 1 || n%200 == 0 {
							reportFault("pg", "flow_spill_lost", "warning", j.hostID,
								fmt.Sprintf("NetFlow 溢写批回灌失败并被丢弃，累计 %d 批；这批明细已经永久丢失", n), "")
						}
					}
					goto nextTick
				}
			default:
				goto nextTick
			}
		}
	nextTick:
	}
}

// insertFlowRecordsAsync 非阻塞入队；主队列满时进 spill；spill 满则 drop 并计数。
func (p *pgStore) insertFlowRecordsAsync(hostID, source string, flows []shared.FlowRecord) {
	if p == nil || len(flows) == 0 {
		return
	}
	job := flowJob{hostID: hostID, source: source, flows: flows}
	select {
	case p.flowJobs <- job:
		return
	default:
	}
	if p.flowSpill != nil {
		select {
		case p.flowSpill <- job:
			p.flowSpillN.Add(1)
			return
		default:
		}
	}
	dropped := p.flowDrop.Add(1)
	slog.Warn("Flow 入库队列已满，丢弃本批明细（写入跟不上摄入速率）",
		"host", hostID, "rows", len(flows), "dropped_total", dropped)
	// 丢批的后果是「事后查不到那条流量」，而查不到不会说自己是被丢掉的——和曲线上的
	// 洞是同一类沉默。只在头几次与每 200 次上报一次，避免持续过载时自己变成噪音源。
	if dropped == 1 || dropped%200 == 0 {
		reportFault("pg", "flow_queue_full", "warning", hostID,
			fmt.Sprintf("NetFlow 明细入库队列已满，已累计丢弃 %d 批（写入跟不上摄入速率）；"+
				"这些明细事后无法补回，流量回溯会出现缺口", dropped), "")
	}
}

func (p *pgStore) netflowQueueStats() map[string]any {
	if p == nil {
		return map[string]any{}
	}
	return map[string]any{
		"dropped_total": p.flowDrop.Load(),
		"spill_total":   p.flowSpillN.Load(),
		"queue_cap":     cap(p.flowJobs),
		"spill_cap":     cap(p.flowSpill),
	}
}

func (p *pgStore) migrateBootstrap() error {
	// 必须先于建表：老库里 flow_records 已存在时，下面的
	// CREATE TABLE IF NOT EXISTS 会直接跳过，分区永远不会生效。
	if err := p.migrateFlowRecordsToPartitioned(); err != nil {
		// 改造失败不该让整个服务起不来——退回非分区老表照样能跑，只是没法按月归档。
		slog.Error("flow_records 分区改造失败，继续以现有表结构运行", "err", err)
	}

	_, err := p.db.Exec(`
		CREATE EXTENSION IF NOT EXISTS vector;
		CREATE TABLE IF NOT EXISTS incidents (
			id         BIGINT PRIMARY KEY,
			status     TEXT,
			created_at BIGINT,
			data       JSONB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS incidents_status ON incidents(status);
		CREATE TABLE IF NOT EXISTS tickets (
			id         BIGINT PRIMARY KEY,
			status     TEXT,
			created_at BIGINT,
			data       JSONB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS tickets_status ON tickets(status);
		CREATE TABLE IF NOT EXISTS app_config (
			id   INT PRIMARY KEY,
			data JSONB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS audit_log (
			id   BIGSERIAL PRIMARY KEY,
			ts   BIGINT,
			data JSONB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS audit_log_ts ON audit_log(ts);
		CREATE TABLE IF NOT EXISTS events (
			id   BIGSERIAL PRIMARY KEY,
			ts   BIGINT,
			data JSONB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
		CREATE TABLE IF NOT EXISTS hosts (
			id   TEXT PRIMARY KEY,
			data JSONB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS kv_state (
			k    TEXT PRIMARY KEY,
			data JSONB NOT NULL
		);
		-- 终端会话录制的「永久审计索引」：只存元数据(info)，录制内容(帧)留在本地文件
		-- (/app/data/recordings/<id>.json，随持久卷永久保存)，避免大 blob 撑爆 PG。
		CREATE TABLE IF NOT EXISTS terminal_recordings (
			id   TEXT PRIMARY KEY,
			ts   BIGINT,
			info JSONB NOT NULL
		);
		-- 兼容早期把整段录制塞进 PG 的版本：删掉重列，回归「内容存文件、PG 只存元数据」。
		ALTER TABLE terminal_recordings DROP COLUMN IF EXISTS recording;
		CREATE INDEX IF NOT EXISTS terminal_recordings_ts ON terminal_recordings(ts DESC);
		-- AI 调用观测 / 成本分析（永久落库，容器重启不丢）
		CREATE TABLE IF NOT EXISTS ai_call_events (
			id                 BIGSERIAL PRIMARY KEY,
			ts                 BIGINT NOT NULL,
			task               TEXT NOT NULL DEFAULT '',
			model              TEXT NOT NULL DEFAULT '',
			actor              TEXT NOT NULL DEFAULT '',
			latency_ms         BIGINT NOT NULL DEFAULT 0,
			ok                 BOOLEAN NOT NULL DEFAULT TRUE,
			error              TEXT DEFAULT '',
			memory_hits        INT DEFAULT 0,
			skill_hits         INT DEFAULT 0,
			reply_chars        INT DEFAULT 0,
			approx_tokens      INT DEFAULT 0,
			prompt_tokens      INT DEFAULT 0,
			completion_tokens  INT DEFAULT 0,
			cost_estimate      DOUBLE PRECISION DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS ai_call_events_ts ON ai_call_events(ts DESC);
		CREATE INDEX IF NOT EXISTS ai_call_events_actor_ts ON ai_call_events(actor, ts DESC);
		CREATE INDEX IF NOT EXISTS ai_call_events_task_ts ON ai_call_events(task, ts DESC);
		-- AI 人工反馈质量信号：只保存任务、动作与内容哈希，不落原始提示词/回答。
		-- 用于衡量采纳率/有用率并审计自学习是否真的产生正向效果。
		CREATE TABLE IF NOT EXISTS ai_feedback_events (
			id          BIGSERIAL PRIMARY KEY,
			ts          BIGINT NOT NULL,
			task        TEXT NOT NULL DEFAULT '',
			actor       TEXT NOT NULL DEFAULT '',
			action      TEXT NOT NULL,
			source_hash TEXT NOT NULL DEFAULT '',
			CONSTRAINT ai_feedback_action_valid CHECK (action IN ('applied','helpful','unhelpful'))
		);
		CREATE INDEX IF NOT EXISTS ai_feedback_events_ts ON ai_feedback_events(ts DESC);
		CREATE INDEX IF NOT EXISTS ai_feedback_events_task_ts ON ai_feedback_events(task, ts DESC);
		-- AI Run：Assist/Diagnose/Sreyun 统一运行对象（Wave 2），串联验证与反馈
		CREATE TABLE IF NOT EXISTS ai_runs (
			id             TEXT PRIMARY KEY,
			kind           TEXT NOT NULL DEFAULT 'assist',
			task           TEXT NOT NULL DEFAULT '',
			actor          TEXT NOT NULL DEFAULT '',
			model          TEXT NOT NULL DEFAULT '',
			input          TEXT NOT NULL DEFAULT '',
			answer         TEXT NOT NULL DEFAULT '',
			content_hash   TEXT NOT NULL DEFAULT '',
			verify_json    JSONB,
			ok             BOOLEAN NOT NULL DEFAULT TRUE,
			latency_ms     BIGINT NOT NULL DEFAULT 0,
			memory_hits    INT DEFAULT 0,
			skill_hits     INT DEFAULT 0,
			feedback       TEXT NOT NULL DEFAULT '',
			incident_id    BIGINT DEFAULT 0,
			datasource_id  TEXT NOT NULL DEFAULT '',
			created_at     BIGINT NOT NULL,
			updated_at     BIGINT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS ai_runs_created ON ai_runs(created_at DESC);
		CREATE INDEX IF NOT EXISTS ai_runs_kind_created ON ai_runs(kind, created_at DESC);
		CREATE INDEX IF NOT EXISTS ai_runs_actor_created ON ai_runs(actor, created_at DESC);
		ALTER TABLE ai_runs ADD COLUMN IF NOT EXISTS meta_json JSONB;
		-- AI 写审批 / 工具审计（持久化，重启不丢）
		CREATE TABLE IF NOT EXISTS ai_write_approvals (
			id          TEXT PRIMARY KEY,
			tool        TEXT NOT NULL DEFAULT '',
			args_hash   TEXT NOT NULL DEFAULT '',
			actor       TEXT NOT NULL DEFAULT '',
			created_at  BIGINT NOT NULL,
			expires_at  BIGINT NOT NULL DEFAULT 0,
			used        BOOLEAN NOT NULL DEFAULT FALSE,
			used_at     BIGINT NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS ai_write_approvals_actor ON ai_write_approvals(actor, created_at DESC);
		CREATE TABLE IF NOT EXISTS ai_tool_audit (
			id          BIGSERIAL PRIMARY KEY,
			ts          BIGINT NOT NULL,
			actor       TEXT NOT NULL DEFAULT '',
			tool        TEXT NOT NULL DEFAULT '',
			action      TEXT NOT NULL DEFAULT '',
			host_id     TEXT NOT NULL DEFAULT '',
			approved    BOOLEAN NOT NULL DEFAULT FALSE,
			blocked     BOOLEAN NOT NULL DEFAULT FALSE,
			detail      TEXT NOT NULL DEFAULT '',
			incident_id BIGINT NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS ai_tool_audit_ts ON ai_tool_audit(ts DESC);
		-- 剧本执行历史（专用表，无环形上限丢失）
		CREATE TABLE IF NOT EXISTS playbook_executions (
			id          BIGINT PRIMARY KEY,
			ts          BIGINT NOT NULL,
			playbook_id TEXT,
			status      TEXT,
			data        JSONB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS playbook_executions_ts ON playbook_executions(ts DESC);
		-- 自动修复执行历史（专用表）
		CREATE TABLE IF NOT EXISTS remediation_runs (
			id      BIGINT PRIMARY KEY,
			ts      BIGINT NOT NULL,
			rule_id TEXT,
			status  TEXT,
			data    JSONB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS remediation_runs_ts ON remediation_runs(ts DESC);
		-- AI 诊断向量记忆（RAG 相似案例检索）
		CREATE TABLE IF NOT EXISTS diagnosis_embeddings (
			id          BIGSERIAL PRIMARY KEY,
			incident_id BIGINT,
			embedding   vector(1536),
			summary     TEXT NOT NULL,
			severity    TEXT,
			tags        TEXT,
			feedback    TEXT DEFAULT '',
			created_at  TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS diag_emb_incident ON diagnosis_embeddings(incident_id);
		-- 通用 AI 记忆库：对话 / 文件 / URL / 多轮历史 全部向量化，持续沉淀为可 RAG 检索的知识
		CREATE TABLE IF NOT EXISTS ai_memory_embeddings (
			id         BIGSERIAL PRIMARY KEY,
			kind       TEXT NOT NULL,
			source     TEXT,
			content    TEXT NOT NULL,
			embedding  vector(1536),
			created_at BIGINT NOT NULL,
			last_hit_at BIGINT DEFAULT 0,
			priority   REAL DEFAULT 1.0
		);
		-- 兼容老表：补增 last_hit_at / priority 列（若不存在）
		ALTER TABLE ai_memory_embeddings ADD COLUMN IF NOT EXISTS last_hit_at BIGINT DEFAULT 0;
		ALTER TABLE ai_memory_embeddings ADD COLUMN IF NOT EXISTS priority REAL DEFAULT 1.0;
		CREATE INDEX IF NOT EXISTS ai_mem_kind ON ai_memory_embeddings(kind);
		CREATE INDEX IF NOT EXISTS ai_mem_created ON ai_memory_embeddings(created_at DESC);
		CREATE INDEX IF NOT EXISTS ai_mem_kind_created ON ai_memory_embeddings(kind, created_at DESC);
		-- AI 技能库（自进化核心）：从 experience/resolution 记忆中提炼出的「可复用 SOP」。
		-- 与 ai_memory_embeddings（原始经验片段）不同，skill 是更高阶、命名化、带触发条件与
		-- 操作步骤的结构化产物，检索后作为「已掌握技能」注入提示词，让 AI 直接复用被验证的做法。
		CREATE TABLE IF NOT EXISTS ai_skills (
			id            BIGSERIAL PRIMARY KEY,
			name          TEXT NOT NULL,
			trigger_desc  TEXT NOT NULL,          -- 何时适用（自然语言，供语义匹配；trigger 是 SQL 关键字故用 _desc）
			steps         TEXT NOT NULL,          -- 怎么做（步骤 / SOP）
			tags          TEXT DEFAULT '',
			embedding     vector(1536),           -- name+trigger_desc 的向量，用于检索
			use_count     INT  DEFAULT 0,
			success_count INT  DEFAULT 0,
			priority      REAL DEFAULT 1.0,
			source        TEXT DEFAULT 'distilled', -- distilled | manual
			created_at    BIGINT NOT NULL,
			updated_at    BIGINT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS ai_skills_priority ON ai_skills(priority DESC);
		ALTER TABLE ai_skills ADD COLUMN IF NOT EXISTS status TEXT DEFAULT 'active';
		ALTER TABLE ai_skills ADD COLUMN IF NOT EXISTS version INT DEFAULT 1;
		ALTER TABLE ai_skills ADD COLUMN IF NOT EXISTS service_ids TEXT DEFAULT '';
		ALTER TABLE ai_skills ADD COLUMN IF NOT EXISTS categories TEXT DEFAULT '';
		CREATE INDEX IF NOT EXISTS ai_skills_status ON ai_skills(status);
		ALTER TABLE ai_memory_embeddings ADD COLUMN IF NOT EXISTS service_id TEXT DEFAULT '';
		ALTER TABLE ai_memory_embeddings ADD COLUMN IF NOT EXISTS category TEXT DEFAULT '';
		ALTER TABLE ai_memory_embeddings ADD COLUMN IF NOT EXISTS verified BOOLEAN DEFAULT false;
		CREATE INDEX IF NOT EXISTS ai_mem_verified ON ai_memory_embeddings(verified) WHERE verified = true;
		-- 经验规则库（高频问题 best practice）
		CREATE TABLE IF NOT EXISTS experience_rules (
			id          BIGSERIAL PRIMARY KEY,
			pattern     TEXT NOT NULL,
			conclusion  TEXT NOT NULL,
			severity    TEXT,
			incident_id BIGINT,
			created_at  TIMESTAMPTZ DEFAULT NOW()
		);
		-- Sreyun Agent 规则库（诊断规则 + 行动策略）
		CREATE TABLE IF NOT EXISTS hermes_rules (
			id          BIGSERIAL PRIMARY KEY,
			name        TEXT NOT NULL,
			description TEXT DEFAULT '',
			priority    INT DEFAULT 0,
			enabled     BOOLEAN DEFAULT true,
			config      JSONB NOT NULL,
			created_at  TIMESTAMPTZ DEFAULT NOW(),
			updated_at  TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS hermes_rules_enabled ON hermes_rules(enabled);
		-- Sreyun Agent 提示模板库（系统提示 + 场景模板）
		CREATE TABLE IF NOT EXISTS hermes_templates (
			id          BIGSERIAL PRIMARY KEY,
			name        TEXT NOT NULL,
			description TEXT DEFAULT '',
			content     TEXT NOT NULL,
			category    TEXT DEFAULT 'system',
			version     INT DEFAULT 1,
			active      BOOLEAN DEFAULT true,
			created_at  TIMESTAMPTZ DEFAULT NOW(),
			updated_at  TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS hermes_templates_active ON hermes_templates(active);
		-- Sreyun Agent 会话记忆
		CREATE TABLE IF NOT EXISTS hermes_sessions (
			id          BIGSERIAL PRIMARY KEY,
			incident_id BIGINT DEFAULT 0,
			status      TEXT DEFAULT 'active',
			messages    JSONB NOT NULL DEFAULT '[]',
			created_at  TIMESTAMPTZ DEFAULT NOW(),
			updated_at  TIMESTAMPTZ DEFAULT NOW()
		);
		-- 告警历史持久化记录（触发时写入，恢复时更新 resolved_at）
		CREATE TABLE IF NOT EXISTS alert_history (
			id          BIGSERIAL PRIMARY KEY,
			key         TEXT NOT NULL,
			fired_at    BIGINT NOT NULL,
			resolved_at BIGINT DEFAULT 0,
			data        JSONB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS alert_history_key ON alert_history(key);
		CREATE INDEX IF NOT EXISTS alert_history_fired ON alert_history(fired_at DESC);
		-- Redfish 硬件最新快照（UPSERT by host_id + target_name）
		CREATE TABLE IF NOT EXISTS hardware_snapshot (
			host_id     TEXT NOT NULL,
			target_name TEXT NOT NULL,
			target_url  TEXT,
			snapshot    JSONB NOT NULL,
			health      TEXT,
			updated_at  TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (host_id, target_name)
		);
		-- Redfish 硬件事件（状态变更/故障/固件升级）
		CREATE TABLE IF NOT EXISTS hardware_events (
			id          BIGSERIAL PRIMARY KEY,
			host_id     TEXT NOT NULL,
			target_name TEXT,
			event_type  TEXT NOT NULL,
			severity    TEXT,
			message     TEXT,
			created_at  TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_hw_events_host_time ON hardware_events(host_id, created_at DESC);
		-- 硬件资产变更历史：**只在部件真的增/删/换时**写一条，永久保留。
		-- 快照表只存最新一份（主键 host_id+target_name），换过哪块盘、哪条内存
		-- 事后完全查不到——这张表就是补这个洞。每轮都存整份快照则 99% 是重复数据。
		CREATE TABLE IF NOT EXISTS hardware_changes (
			id          BIGSERIAL PRIMARY KEY,
			host_id     TEXT NOT NULL,
			target_name TEXT NOT NULL,
			kind        TEXT NOT NULL,   -- disk / dimm / psu / cpu / gpu / raid / firmware / enclosure
			component   TEXT NOT NULL,   -- 槽位或部件名，如 "Bay 3" / "DIMM A1"
			action      TEXT NOT NULL,   -- added / removed / replaced / changed
			old_value   TEXT,
			new_value   TEXT,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_hw_changes_host_time ON hardware_changes(host_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_hw_changes_component ON hardware_changes(host_id, kind, component);
		-- Hyper-V 虚拟机清单：每台物理宿主机一份（整份 guests 存 JSONB），覆盖式 upsert。
		-- 与 hardware_snapshot 同构，只是一台宿主对应一份清单，故主键仅 host_id。
		CREATE TABLE IF NOT EXISTS hyperv_inventory (
			host_id     TEXT PRIMARY KEY,
			host_name   TEXT,
			guest_count INT DEFAULT 0,
			snapshot    JSONB NOT NULL,
			updated_at  TIMESTAMPTZ DEFAULT NOW()
		);
		-- Hyper-V 虚拟机事件：VM 增/删/状态跳变，只在变化时写一条，永久保留。
		CREATE TABLE IF NOT EXISTS hyperv_events (
			id         BIGSERIAL PRIMARY KEY,
			host_id    TEXT NOT NULL,
			vm_name    TEXT,
			vm_id      TEXT,
			kind       TEXT NOT NULL,   -- vm_added / vm_removed / state_change
			severity   TEXT,
			message    TEXT,
			created_at TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_hyperv_events_host_time ON hyperv_events(host_id, created_at DESC);
		-- Docker/Podman 容器清单：每台主机一份（整份 containers 存 JSONB）。
		CREATE TABLE IF NOT EXISTS container_inventory (
			host_id         TEXT PRIMARY KEY,
			host_name       TEXT,
			runtime         TEXT,
			container_count INT DEFAULT 0,
			snapshot        JSONB NOT NULL,
			updated_at      TIMESTAMPTZ DEFAULT NOW()
		);
		-- Flow 明细：按月分区、**永久保留**（归档靠 DROP/DETACH 分区，不再定时删除）。
		-- 分区键必须进主键，故 PK 是 (id, created_at)。
		CREATE TABLE IF NOT EXISTS flow_records (
			id          BIGSERIAL,
			host_id     TEXT NOT NULL,
			source      TEXT NOT NULL,
			src_ip      INET,
			dst_ip      INET,
			src_port    INT,
			dst_port    INT,
			protocol    INT,
			bytes       BIGINT,
			packets     BIGINT,
			first_seen  TIMESTAMPTZ,
			last_seen   TIMESTAMPTZ,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (id, created_at)
		) PARTITION BY RANGE (created_at);
		-- 兜底分区：任何月份分区没来得及建时，数据落这里而不是插入失败。
		CREATE TABLE IF NOT EXISTS flow_records_default PARTITION OF flow_records DEFAULT;
		CREATE INDEX IF NOT EXISTS idx_flow_host_time ON flow_records(host_id, created_at DESC);

		-- SNMP 设备快照：一台设备一份，按 (host_id, device_name) upsert。
		-- 与 hardware_snapshot 同构：采集失败（Error 非空）时上层不覆盖上一份好数据。
		CREATE TABLE IF NOT EXISTS snmp_snapshot (
			host_id     TEXT NOT NULL,
			device_name TEXT NOT NULL,
			device_ip   TEXT,
			snapshot    JSONB NOT NULL,
			reachable   BOOLEAN DEFAULT TRUE,
			updated_at  TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (host_id, device_name)
		);
		-- SNMP Trap 事件：追加写，供告警联动/查询/取证。
		CREATE TABLE IF NOT EXISTS snmp_traps (
			id          BIGSERIAL PRIMARY KEY,
			host_id     TEXT NOT NULL,
			source_ip   TEXT,
			version     TEXT,
			trap_oid    TEXT,
			severity    TEXT,
			uptime_sec  DOUBLE PRECISION,
			varbinds    JSONB,
			received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_snmp_traps_host_time ON snmp_traps(host_id, received_at DESC);

		-- 明文 HTTP 内容审计（Phase 2）。高敏感：body 可能含用户发给大模型的 prompt。
		-- 落 PG 是因为审计记录不可易失；保留期由 cleanup 定期清理（见 cleanupContentAudit）。
		CREATE TABLE IF NOT EXISTS content_audit (
			id          BIGSERIAL PRIMARY KEY,
			host_id     TEXT NOT NULL,
			src_ip      TEXT,
			dst_ip      TEXT,
			dst_port    INT,
			protocol    TEXT,
			method      TEXT,
			host        TEXT,
			path        TEXT,
			ctype       TEXT,
			body        TEXT,
			status         INT,
			resp_ctype     TEXT,
			resp_body      TEXT,
			req_truncated  BOOLEAN,
			resp_truncated BOOLEAN,
			capture_backend TEXT,
			body_mode       TEXT,
			req_bytes       BIGINT,
			resp_bytes      BIGINT,
			req_sha256      TEXT,
			resp_sha256     TEXT,
			redaction_count INT,
			redaction_labels TEXT,
			principal_id    TEXT,
			application_id  TEXT,
			event_id        TEXT,
			request_id      TEXT,
			trace_id        TEXT,
			llm_provider    TEXT,
			llm_model       TEXT,
			llm_operation   TEXT,
			llm_stream      BOOLEAN,
			input_tokens    INT,
			output_tokens   INT,
			tool_calls      INT,
			latency_ms      BIGINT,
			policy_decision TEXT,
			risk_labels     TEXT,
			sensitive   TEXT,
			observed_at TIMESTAMPTZ,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_content_audit_host_time ON content_audit(host_id, created_at DESC);
		-- 兼容：早期表可能缺响应/敏感列，幂等补齐。
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS status INT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS protocol TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS resp_ctype TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS resp_body TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS req_truncated BOOLEAN;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS resp_truncated BOOLEAN;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS capture_backend TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS body_mode TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS req_bytes BIGINT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS resp_bytes BIGINT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS req_sha256 TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS resp_sha256 TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS redaction_count INT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS redaction_labels TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS principal_id TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS application_id TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS event_id TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS request_id TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS trace_id TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS llm_provider TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS llm_model TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS llm_operation TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS llm_stream BOOLEAN;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS input_tokens INT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS output_tokens INT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS tool_calls INT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS latency_ms BIGINT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS policy_decision TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS risk_labels TEXT;
		ALTER TABLE content_audit ADD COLUMN IF NOT EXISTS sensitive TEXT;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_content_audit_event_id
			ON content_audit(host_id, event_id) WHERE event_id IS NOT NULL AND event_id <> '';
		CREATE INDEX IF NOT EXISTS idx_content_audit_llm_time
			ON content_audit(host_id, llm_provider, llm_model, created_at DESC)
			WHERE llm_provider IS NOT NULL AND llm_provider <> '';
		CREATE INDEX IF NOT EXISTS idx_content_audit_sensitive_time
			ON content_audit(host_id, created_at DESC)
			WHERE sensitive IS NOT NULL AND sensitive <> '';
	`)
	if err != nil {
		return err
	}
	return nil
}

// migrateFlowRecordsToPartitioned converts a pre-existing non-partitioned
// flow_records into a monthly-partitioned one, preserving rows.
//
// 必须在 initSchema **之前**跑：老表存在时 CREATE TABLE IF NOT EXISTS 不会报错也不会改造它，
// 于是分区永远不会生效。整个改造在一个事务里完成（PG 的 DDL 是事务性的），
// 中途失败会整体回滚，不会留下半吊子状态。
func (p *pgStore) migrateFlowRecordsToPartitioned() error {
	var exists, partitioned bool
	if err := p.db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='flow_records')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil // 全新部署：initSchema 会直接建成分区表
	}
	if err := p.db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM pg_partitioned_table pt JOIN pg_class c ON c.oid=pt.partrelid
		WHERE c.relname='flow_records')`).Scan(&partitioned); err != nil {
		return err
	}
	if partitioned {
		return nil // 已经是分区表
	}

	// 数据量太大时不在启动路径上做在线拷贝——那会把服务卡住好几分钟。
	// 老表此前一直有 7 天清理，正常不会很大；真超了就明确报出来让人工处理。
	var n int64
	if err := p.db.QueryRow(`SELECT count(*) FROM flow_records`).Scan(&n); err != nil {
		return err
	}
	const maxInlineRows = 5_000_000
	if n > maxInlineRows {
		slog.Error("flow_records 行数过多，跳过自动分区改造（避免启动时长时间锁表）",
			"rows", n, "limit", maxInlineRows,
			"action", "请在维护窗口手工改造：重命名旧表→建分区表→分批回灌→删旧表")
		return nil
	}

	slog.Info("开始把 flow_records 改造成按月分区表", "rows", n)
	start := time.Now()
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`ALTER TABLE flow_records RENAME TO flow_records_legacy`,
		`DROP INDEX IF EXISTS idx_flow_host_time`,
		`CREATE TABLE flow_records (
			id BIGSERIAL, host_id TEXT NOT NULL, source TEXT NOT NULL,
			src_ip INET, dst_ip INET, src_port INT, dst_port INT, protocol INT,
			bytes BIGINT, packets BIGINT, first_seen TIMESTAMPTZ, last_seen TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (id, created_at)
		) PARTITION BY RANGE (created_at)`,
		`CREATE TABLE flow_records_default PARTITION OF flow_records DEFAULT`,
		`INSERT INTO flow_records (host_id, source, src_ip, dst_ip, src_port, dst_port,
			protocol, bytes, packets, first_seen, last_seen, created_at)
		 SELECT host_id, source, src_ip, dst_ip, src_port, dst_port,
			protocol, bytes, packets, first_seen, last_seen, COALESCE(created_at, NOW())
		 FROM flow_records_legacy`,
		`DROP TABLE flow_records_legacy`,
		`CREATE INDEX idx_flow_host_time ON flow_records(host_id, created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("分区改造失败于 [%.60s]: %w", q, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("flow_records 已改造为按月分区表", "rows", n, "耗时", time.Since(start))
	return nil
}

// isSafeFlowPartitionName 校验分区表标识符只能是 flow_records_ 前缀 + 6 位数字（YYYYMM），
// 作为拼接进 DDL 前的最后一道防线。
func isSafeFlowPartitionName(name string) bool {
	const prefix = "flow_records_"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	suffix := name[len(prefix):]
	if len(suffix) != 6 {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ensureFlowPartitions creates monthly partitions for the current and next
// months. Idempotent; safe to call on every tick.
//
// 有 DEFAULT 兜底分区在，缺分区也不会插入失败；但数据落在 DEFAULT 里就没法按月
// DROP 归档了。注意：DEFAULT 里一旦已有该月数据，PG 会拒绝再建这个月的分区，
// 因此这里失败只记日志、不当错误——数据仍在 DEFAULT 中可查。
func (p *pgStore) ensureFlowPartitions() {
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, i, 0)
		end := start.AddDate(0, 1, 0)
		name := fmt.Sprintf("flow_records_%04d%02d", start.Year(), start.Month())
		// 防御性白名单：分区表名由 time.Now() 生成，理应恒为 flow_records_YYYYMM。这里再校验一次，
		// 万一上游逻辑被篡改产生异常标识符也不会拼进 DDL 执行（SQL 注入面归零）。
		if !isSafeFlowPartitionName(name) {
			slog.Warn("跳过异常分区名（疑似被篡改）", "partition", name)
			continue
		}
		q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF flow_records
			FOR VALUES FROM ('%s') TO ('%s')`,
			name, start.Format("2006-01-02"), end.Format("2006-01-02"))
		if _, err := p.db.Exec(q); err != nil {
			slog.Debug("创建 Flow 月分区未成功（多为 DEFAULT 分区已有该月数据，可忽略）",
				"partition", name, "err", err)
		}
	}
}

// --- hosts (metadata + latest + custom gauges; history lives in VM, not PG) ---

func (p *pgStore) loadHosts() ([]*Host, error) {
	rows, err := p.db.Query(`SELECT data FROM hosts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Host
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var h Host
		if json.Unmarshal(raw, &h) == nil && h.ID != "" {
			hh := h
			out = append(out, &hh)
		}
	}
	return out, rows.Err()
}

// hostIdentityDigest reduces a Host to the fields that identify it, deliberately
// dropping everything VictoriaMetrics already owns.
//
// Host.Latest 是一个完整的指标样本（CPU/内存/每块盘/每种连接状态/GPU/进程名，
// 实测 0.8–1.9 KB），Custom 是插件上报的自定义仪表盘值——两者都是纯时序数据，
// **每个上报周期都在变**，而且每一个点都已经由 vmWriter 写进了 VM。它们跟着 hosts
// 行每 15 秒重写一次，是 PG 体积远超 VM 的真正来源（内容哈希去重对它们完全无效，
// 因为内容确实每次都不同）。LastSeen 同理：每个周期都变，但启动后几秒内就会被
// 新上报覆盖，没有任何理由为它每 15 秒烧一个死元组。
//
// 摘要里只留「几乎不变」的部分，于是常态下 hosts 表一次写都不发；真正的指标值仍会
// 被慢周期的整行刷写（含退出前那次）落库，供重启后回填离线主机的最后已知状态。
func hostIdentityDigest(h *Host) []byte {
	if h == nil {
		return nil
	}
	raw, _ := json.Marshal([]any{
		h.ID, h.Hostname, h.OS, h.Platform, h.Arch, h.IP, h.Kernel,
		h.Category, h.AgentVersion, h.ServerURL, h.Fingerprint, h.FirstSeen,
	})
	return raw
}

// saveHosts mirrors the in-memory host set into PG, writing ONLY rows whose
// JSON actually changed and deleting only rows that disappeared.
//
// 原实现是「DELETE 全表 + 重插全部」，每 15 秒一次。500 台机群下这是每天约 290 万
// 个死元组、数 GB WAL，而其中绝大部分主机（尤其离线的那些）内容一个字节都没变。
// hosts 表只在启动时被 loadHosts 读一次（纯重启缓存），所以按需写完全等价。
//
// withMetrics=false 是快周期（15s）的轻量刷写：只有主机身份/元信息变了才写，指标
// 变化一概不写。withMetrics=true 是慢周期（pgFlushHeavyEveryNth）与退出前的整行
// 刷写，此时 Latest/Custom 的最新值才落库。两种模式共用同一份内容哈希缓存，所以
// 「轻量刷写跳过 → 下一次整行刷写补上」是自然发生的，不需要额外记账。
func (p *pgStore) saveHosts(hosts []*Host, withMetrics bool) error {
	const table = "hosts"
	// 首次刷写：先learn PG 里已有哪些 id，否则「进程停机期间被删掉的主机」永远删不掉。
	if p.wc.needsSeed(table) {
		ids, err := p.selectIDsText(`SELECT id FROM hosts`)
		if err != nil {
			return err
		}
		p.wc.seed(table, ids)
	}

	live := make(map[string]bool, len(hosts))
	type pendingHost struct {
		id    string
		raw   []byte
		ident []byte
	}
	var pending []pendingHost
	for _, h := range hosts {
		if h == nil || h.ID == "" {
			continue
		}
		raw, _ := json.Marshal(h)
		ident := hostIdentityDigest(h)
		live[h.ID] = true
		changed := p.wc.isChanged(table+"/"+h.ID, raw)
		if !withMetrics {
			// 快周期：指标漂移不算变化，只有身份/元信息变了才值得一次写。
			changed = changed && p.wc.isChanged(table+"-id/"+h.ID, ident)
		}
		if changed {
			pending = append(pending, pendingHost{id: h.ID, raw: raw, ident: ident})
		}
	}
	removed := p.wc.missingIDs(table, live)
	if len(pending) == 0 && len(removed) == 0 {
		return nil // 全员未变化：这一轮一次写都不发
	}

	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(pending) > 0 {
		stmt, err := tx.Prepare(`INSERT INTO hosts(id,data) VALUES($1,$2)
			ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, ph := range pending {
			if _, err := stmt.Exec(ph.id, ph.raw); err != nil {
				return err
			}
		}
	}
	for _, id := range removed {
		if _, err := tx.Exec(`DELETE FROM hosts WHERE id=$1`, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		// 事务失败 → 缓存必须退回，否则下轮会以为已经写过而永久跳过。
		// 身份摘要与整行哈希是同一次写的两半，必须一起退回。
		p.wc.invalidateTable(table)
		p.wc.invalidateTable(table + "-id")
		return err
	}
	for _, ph := range pending {
		p.wc.remember(table+"/"+ph.id, ph.raw)
		p.wc.remember(table+"-id/"+ph.id, ph.ident)
	}
	for _, id := range removed {
		p.wc.forget(table + "/" + id)
		p.wc.forget(table + "-id/" + id)
	}
	p.wc.setIDs(table, live)
	return nil
}

// --- small key-value state blobs (alert-ack states, login sessions) ---

func (p *pgStore) loadKV(key string) ([]byte, error) {
	var raw []byte
	err := p.db.QueryRow(`SELECT data FROM kv_state WHERE k=$1`, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return raw, err
}

func (p *pgStore) saveKV(key string, raw []byte) error {
	_, err := p.db.Exec(`INSERT INTO kv_state(k,data) VALUES($1,$2)
		ON CONFLICT(k) DO UPDATE SET data=EXCLUDED.data`, key, raw)
	if err != nil {
		return err
	}
	p.wc.remember("kv/"+key, raw)
	return nil
}

// saveKVIfAbsent 只在这个键还不存在时写入，返回是否真的写进去了。
//
// 存在的理由是部署指纹（install_id）：它一旦被覆盖，客户按旧指纹签发的授权立刻
// 变成 install mismatch，而旧指纹已经没了、找不回来。用 DO NOTHING 让"已经有一条"
// 这件事在数据库层面就赢，调用方只需要在没写进去时把库里那条读回来。
func (p *pgStore) saveKVIfAbsent(key string, raw []byte) (bool, error) {
	res, err := p.db.Exec(`INSERT INTO kv_state(k,data) VALUES($1,$2)
		ON CONFLICT(k) DO NOTHING`, key, raw)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		p.wc.remember("kv/"+key, raw)
	}
	return n > 0, nil
}

// saveKVIfChanged skips the UPDATE when the blob is byte-identical to what was
// last written. 周期刷写里 sessions / alert_states / slo_burning / playbook_* 这些
// 大多数时候纹丝不动，但每 15 秒都要被重写一遍——每次都是一个死元组加一份 WAL。
func (p *pgStore) saveKVIfChanged(key string, raw []byte) error {
	if !p.wc.isChanged("kv/"+key, raw) {
		return nil
	}
	return p.saveKV(key, raw)
}

// --- config blob (whole ServerConfig as one JSONB row; replaces the JSON file) ---

func (p *pgStore) loadConfigBlob() ([]byte, bool, error) {
	var raw []byte
	err := p.db.QueryRow(`SELECT data FROM app_config WHERE id=1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func (p *pgStore) saveConfigBlob(raw []byte) error {
	_, err := p.db.Exec(`INSERT INTO app_config(id,data) VALUES(1,$1)
		ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data`, raw)
	return err
}

// --- audit log (append-only, unbounded in PG; the store keeps a recent cache) ---

func (p *pgStore) appendAudit(e LogEntry) {
	seq, err := p.appendAuditChained(context.Background(), e)
	if err != nil {
		slog.Warn("PG audit append failed",
			"seq", seq,
			"operation", "append",
			"secret_degraded", auditChainSecretDegraded(),
			"error_class", auditAppendErrorClass(err))
		// 审计链写失败是**没有任何人会来告诉你**的一类故障：页面照常、告警照常，
		// 只有事后取证时才会发现链上缺了一段，而那时已经无从补起。按 critical 进
		// 自身故障归口，第一次就开事件。
		reportFault("pg", "audit_append_failed", "critical", "",
			"审计链追加失败（error_class="+auditAppendErrorClass(err)+
				"，密钥降级="+strconv.FormatBool(auditChainSecretDegraded())+"）；"+
				"此后这段时间的审计记录将无法通过完整性对账",
			err.Error())
	}
}

func auditAppendErrorClass(err error) string {
	if errors.Is(err, sql.ErrConnDone) {
		return "storage_unavailable"
	}
	return "database_error"
}

func (p *pgStore) loadRecentAudit(limit int) ([]LogEntry, error) {
	rows, err := p.db.Query(`SELECT data FROM (SELECT id,ts,data FROM audit_log_p ORDER BY ts DESC, id DESC LIMIT $1) t ORDER BY ts ASC, id ASC`, limit)
	if err != nil {
		rows, err = p.db.Query(`SELECT data FROM (SELECT id,ts,data FROM audit_log ORDER BY ts DESC, id DESC LIMIT $1) t ORDER BY ts ASC, id ASC`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var e LogEntry
		if json.Unmarshal(raw, &e) == nil {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// --- terminal session recordings (permanent audit retention) ---

// saveTermRecording persists one ended session's METADATA to PG permanently
// (idempotent). The recording CONTENT (frames) stays in the local file
// /app/data/recordings/<id>.json — PG only holds the audit index so the session
// list shows full history without bloating the DB with large blobs.
func (p *pgStore) saveTermRecording(a termArchive) {
	if a.info.ID == "" {
		return
	}
	info, err := json.Marshal(a.info)
	if err != nil {
		return
	}
	if _, err := p.db.Exec(
		`INSERT INTO terminal_recordings(id,ts,info) VALUES($1,$2,$3) ON CONFLICT (id) DO NOTHING`,
		a.info.ID, a.info.CreatedAt, info); err != nil {
		slog.Warn("PG 写终端会话录制索引失败", "err", err)
	}
}

// listTermRecordings returns recent ended sessions' metadata (newest first) from
// the permanent PG store, so the session list shows the full history, not just
// the last termArchiveCap sessions held in memory.
func (p *pgStore) listTermRecordings(limit int) []termSessionInfo {
	rows, err := p.db.Query(`SELECT info FROM terminal_recordings ORDER BY ts DESC LIMIT $1`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []termSessionInfo
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var info termSessionInfo
		if json.Unmarshal(raw, &info) == nil {
			info.Active = false
			out = append(out, info)
		}
	}
	noteRowsErr("listTermRecordings", rows)
	return out
}

// --- plugin events ---

func (p *pgStore) appendEvent(e storedEvent) {
	start := time.Now()
	if err := p.appendEventDual(context.Background(), e); err != nil {
		slog.Warn("PG event append failed", "operation", "append", "error_class", auditAppendErrorClass(err))
	}
	observePGSlow("INSERT events", start)
}

func (p *pgStore) appendEventDual(ctx context.Context, e storedEvent) error {
	if e.Timestamp <= 0 {
		e.Timestamp = time.Now().Unix()
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event mirror: %w", err)
	}
	return p.withPgTx(ctx, func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO events(ts,data) VALUES($1,$2) RETURNING id`, e.Timestamp, raw).Scan(&id); err != nil {
			return fmt.Errorf("insert legacy event mirror: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO events_p(id,ts,data) VALUES($1,$2,$3)`, id, e.Timestamp, raw); err != nil {
			return fmt.Errorf("insert partition event mirror: %w", err)
		}
		return nil
	})
}

func (p *pgStore) loadRecentEvents(limit int) ([]storedEvent, error) {
	rows, err := p.db.Query(`SELECT data FROM (SELECT id,ts,data FROM events_p ORDER BY ts DESC, id DESC LIMIT $1) t ORDER BY ts ASC, id ASC`, limit)
	if err != nil {
		rows, err = p.db.Query(`SELECT data FROM (SELECT id,ts,data FROM events ORDER BY ts DESC, id DESC LIMIT $1) t ORDER BY ts ASC, id ASC`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedEvent
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var e storedEvent
		if json.Unmarshal(raw, &e) == nil {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// --- alert history (fire on insert, resolve on update; unbounded in PG) ---

func (p *pgStore) appendAlertRecord(r AlertRecord) {
	raw, err := json.Marshal(r)
	if err != nil {
		return
	}
	if _, err := p.db.Exec(`INSERT INTO alert_history(key,fired_at,data) VALUES($1,$2,$3)`,
		r.Key, r.FiredAt, raw); err != nil {
		slog.Warn("PG 写告警历史失败", "err", err)
		// 告警照常弹给人，历史里却没有它。事后复盘（MTTR、噪音率、SLO）读的是这张表，
		// 缺一段就会安静地得出偏乐观的结论——比没有数据更糟。
		reportFault("pg", "alert_history_write_failed", "warning", "",
			"告警历史写入失败："+r.Key+"；该告警不会出现在事后复盘与 SLO 统计里", err.Error())
	}
}

func (p *pgStore) resolveAlertRecord(id int64, resolvedAt int64) {
	if _, err := p.db.Exec(`UPDATE alert_history SET resolved_at=$1 WHERE id=$2`, resolvedAt, id); err != nil {
		slog.Warn("PG 更新告警恢复时间失败", "err", err)
	}
}

func (p *pgStore) loadRecentAlerts(limit int) ([]AlertRecord, error) {
	rows, err := p.db.Query(`SELECT data FROM (SELECT id,data FROM alert_history ORDER BY id DESC LIMIT $1) t ORDER BY id ASC`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRecord
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var r AlertRecord
		if json.Unmarshal(raw, &r) == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// --- incidents ---

func (p *pgStore) loadIncidents() ([]Incident, error) {
	rows, err := p.db.Query(`SELECT data FROM incidents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var inc Incident
		if json.Unmarshal(raw, &inc) == nil {
			out = append(out, inc)
		}
	}
	return out, rows.Err()
}

func (p *pgStore) saveIncidents(list []Incident) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO incidents(id,status,created_at,data) VALUES($1,$2,$3,$4)
		ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status, data=EXCLUDED.data`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	// 已关闭的历史事件在内存里一动不动，却被每 15 秒重写一遍；只写变化行。
	written := make(map[string][]byte, len(list))
	for _, inc := range list {
		raw, _ := json.Marshal(inc)
		key := fmt.Sprintf("incidents/%d", inc.ID)
		if !p.wc.isChanged(key, raw) {
			continue
		}
		if _, err := stmt.Exec(inc.ID, inc.Status, inc.CreatedAt, raw); err != nil {
			return err
		}
		written[key] = raw
	}
	if len(written) == 0 {
		return nil
	}
	if err := tx.Commit(); err != nil {
		p.wc.invalidateTable("incidents")
		return err
	}
	for k, raw := range written {
		p.wc.remember(k, raw)
	}
	return nil
}

// --- tickets ---

func (p *pgStore) loadTickets() ([]Ticket, error) {
	rows, err := p.db.Query(`SELECT data FROM tickets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ticket
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var tk Ticket
		if json.Unmarshal(raw, &tk) == nil {
			out = append(out, tk)
		}
	}
	return out, rows.Err()
}

func (p *pgStore) saveTickets(list []Ticket) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO tickets(id,status,created_at,data) VALUES($1,$2,$3,$4)
		ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status, data=EXCLUDED.data`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	written := make(map[string][]byte, len(list))
	for _, tk := range list {
		raw, _ := json.Marshal(tk)
		key := fmt.Sprintf("tickets/%d", tk.ID)
		if !p.wc.isChanged(key, raw) {
			continue
		}
		if _, err := stmt.Exec(tk.ID, tk.Status, tk.CreatedAt, raw); err != nil {
			return err
		}
		written[key] = raw
	}
	if len(written) == 0 {
		return nil
	}
	if err := tx.Commit(); err != nil {
		p.wc.invalidateTable("tickets")
		return err
	}
	for k, raw := range written {
		p.wc.remember(k, raw)
	}
	return nil
}

// ============================================================================
// pgvector: AI 诊断向量记忆（RAG 相似案例检索）
// ============================================================================

// vecStr formats a []float64 as a pgvector literal string, e.g. "[0.1,0.2,...]".
func vecStr(v []float64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", f)
	}
	b.WriteByte(']')
	return b.String()
}

// insertDiagnosisEmbedding stores a diagnosis embedding for later RAG retrieval.
func (p *pgStore) insertDiagnosisEmbedding(incidentID int64, emb []float64, summary, severity, tags string) (int64, error) {
	var id int64
	err := p.db.QueryRow(
		`INSERT INTO diagnosis_embeddings(incident_id, embedding, summary, severity, tags)
		 VALUES($1, $2::vector, $3, $4, $5) RETURNING id`,
		incidentID, vecStr(emb), summary, severity, tags,
	).Scan(&id)
	return id, err
}

// ---- 反馈驱动的检索重排：让 👍/👎 真正改变 RAG 结果（learn 闭环）----
//
// 用户对诊断结论的 👍/👎（helpful/unhelpful）此前只作为提示标注展示，并不影响检索排序，
// 反馈形同虚设。这里把用户评价折算成「有效距离」的增减：👍 上浮、👎 下沉（通常被挤出 Top-N），
// 使每一次反馈都改变后续对话能检索到的历史案例——这才是可自我进化的学习闭环。
//
// 权重刻意保守且仅用于排序：对外返回的 similarCase.Distance 保持原始余弦距离，
// 展示的相似度% 依旧真实，不会被反馈"注水"。
const (
	feedbackHelpfulBonus     = 0.05 // 👍 案例：有效距离 -0.05，轻微提前
	feedbackPendingPenalty   = 0.04 // 未验证案例：轻微下沉，让现实验证案例优先
	feedbackUnhelpfulPenalty = 0.20 // 👎 案例：有效距离 +0.20，显著靠后（通常被挤出 Top-N）
)

// feedbackAdjustedDistance 返回用于排序的「有效距离」：在原始余弦距离上叠加反馈增减。
// 空 / pending 表示未验证，轻微下沉；未知值保持中性。
func feedbackAdjustedDistance(rawDistance float64, feedback string) float64 {
	switch feedback {
	case "helpful":
		return rawDistance - feedbackHelpfulBonus
	case "unhelpful":
		return rawDistance + feedbackUnhelpfulPenalty
	case "", "pending":
		return rawDistance + feedbackPendingPenalty
	default:
		return rawDistance
	}
}

// rerankByFeedback 按「有效距离」升序稳定重排候选案例，再截断到 limit：
// 👍 案例上浮、👎 案例下沉（通常被挤出 Top-N），实现反馈学习闭环。
// limit<=0 表示不截断；原始 Distance 不被修改。
func rerankByFeedback(cases []similarCase, limit int) []similarCase {
	sort.SliceStable(cases, func(i, j int) bool {
		return feedbackAdjustedDistance(cases[i].Distance, cases[i].Feedback) <
			feedbackAdjustedDistance(cases[j].Distance, cases[j].Feedback)
	})
	if limit > 0 && len(cases) > limit {
		cases = cases[:limit]
	}
	return cases
}

// searchSimilarCases returns the top-N similar diagnosis cases, re-ranked by user feedback.
// 先用向量索引按余弦距离取较大候选集（保留 ivfflat 索引加速），再交给 rerankByFeedback 让
// 👍/👎 影响最终排序，使用户反馈真正改变 RAG 检索结果（learn 闭环），而非仅作展示标注。
func (p *pgStore) searchSimilarCases(emb []float64, limit int) ([]similarCase, error) {
	if limit <= 0 {
		limit = 3
	}
	// 放大候选集：Top 案例被 👎 惩罚挤下去后，仍需有优质案例补位；至少取 12 条。
	fetch := limit * 4
	if fetch < 12 {
		fetch = 12
	}
	rows, err := p.db.Query(
		`SELECT id, incident_id, summary, severity, tags, feedback,
		        embedding <=> $1::vector AS distance
		 FROM diagnosis_embeddings
		 ORDER BY embedding <=> $1::vector
		 LIMIT $2`,
		vecStr(emb), fetch,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []similarCase
	for rows.Next() {
		var c similarCase
		if err := rows.Scan(&c.ID, &c.IncidentID, &c.Summary, &c.Severity, &c.Tags, &c.Feedback, &c.Distance); err != nil {
			continue
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rerankByFeedback(out, limit), nil
}

// updateDiagnosisFeedback records feedback on the newest diagnosis for the
// incident. Updating every historical diagnosis would incorrectly punish or
// reward otherwise unrelated turns from the same incident.
func (p *pgStore) updateDiagnosisFeedback(incidentID int64, feedback string) error {
	_, err := p.db.Exec(
		`UPDATE diagnosis_embeddings SET feedback=$1
		 WHERE id=(SELECT id FROM diagnosis_embeddings WHERE incident_id=$2 ORDER BY id DESC LIMIT 1)`,
		feedback, incidentID,
	)
	return err
}

type similarCase struct {
	ID         int64   `json:"id"`
	IncidentID int64   `json:"incident_id"`
	Summary    string  `json:"summary"`
	Severity   string  `json:"severity"`
	Tags       string  `json:"tags"`
	Feedback   string  `json:"feedback"`
	Distance   float64 `json:"distance"` // cosine distance, lower = more similar
}

// ---- 通用 AI 记忆（对话 / 文件 / URL / 多轮历史 向量化，持续沉淀 RAG 知识，自我进化）----

func (p *pgStore) insertMemoryEmbeddingScoped(kind, source, content string, emb []float64, ts int64, serviceID, category string, verified bool) error {
	pri := 1.0
	if verified {
		pri = 1.4
	} else if kind == "alert" || kind == "chat" {
		pri = 0.85 // demote noisy ungated writers at insert time
	}
	_, err := p.db.Exec(
		`INSERT INTO ai_memory_embeddings(kind, source, content, embedding, created_at, service_id, category, verified, priority)
		 VALUES($1, $2, $3, $4::vector, $5, $6, $7, $8, $9)`,
		kind, source, content, vecStr(emb), ts, serviceID, category, verified, pri)
	return err
}

type memoryHit struct {
	ID        int64   `json:"id"`
	Kind      string  `json:"kind"`
	Source    string  `json:"source"`
	Content   string  `json:"content"`
	Distance  float64 `json:"distance"`
	ServiceID string  `json:"service_id,omitempty"`
	Category  string  `json:"category,omitempty"`
	Verified  bool    `json:"verified,omitempty"`
}

// searchMemory 按余弦距离取最相近的 N 条 AI 记忆（RAG 检索，跨对话/文件/URL/历史）。
func (p *pgStore) searchMemory(emb []float64, limit int) ([]memoryHit, error) {
	if limit <= 0 {
		limit = 3
	}
	rows, err := p.db.Query(
		`SELECT id, kind, source, content, COALESCE(service_id,''), COALESCE(category,''), COALESCE(verified,false),
		        embedding <=> $1::vector AS distance
		 FROM ai_memory_embeddings
		 ORDER BY (embedding <=> $1::vector) / (GREATEST(priority, 0.1) * CASE WHEN COALESCE(verified,false) THEN 1.35 ELSE 1.0 END) LIMIT $2`,
		vecStr(emb), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []memoryHit
	for rows.Next() {
		var m memoryHit
		if err := rows.Scan(&m.ID, &m.Kind, &m.Source, &m.Content, &m.ServiceID, &m.Category, &m.Verified, &m.Distance); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// searchMemoryByKind 按 kind 优先检索记忆：先查指定 kind 的 Top-K，不足时补充其他 kind。
// 用于诊断对话优先召回历史诊断结论、普通对话优先召回通用知识等场景。
// 排序公式：distance / (priority * time_factor)，其中：
//   - time_factor = max(0.5, 1 - days/365) 时间衰减
//   - 最近 7 天额外 1.5x 权重加成
func (p *pgStore) searchMemoryByKind(emb []float64, preferKind string, limit int) ([]memoryHit, error) {
	if limit <= 0 {
		limit = 5
	}
	now := time.Now().Unix()
	sevenDaysAgo := now - 7*86400
	// 先查指定 kind 的前 limit 条
	preferred := limit * 2 / 3 // 2/3 给优先 kind
	if preferred < 1 {
		preferred = 1
	}
	rows, err := p.db.Query(
		`SELECT id, kind, source, content, COALESCE(service_id,''), COALESCE(category,''), COALESCE(verified,false),
		        embedding <=> $1::vector AS distance
		 FROM ai_memory_embeddings WHERE kind = $4
		 ORDER BY (embedding <=> $1::vector) / (GREATEST(priority, 0.1) *
		   GREATEST(0.5, 1.0 - (EXTRACT(EPOCH FROM NOW()) - created_at) / 31536000.0) *
		   CASE WHEN created_at > $3 THEN 1.5 ELSE 1.0 END *
		   CASE WHEN COALESCE(verified,false) THEN 1.35 ELSE 1.0 END)
		 LIMIT $2`,
		vecStr(emb), preferred, sevenDaysAgo, preferKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []memoryHit
	seen := make(map[string]bool)
	for rows.Next() {
		var m memoryHit
		if err := rows.Scan(&m.ID, &m.Kind, &m.Source, &m.Content, &m.ServiceID, &m.Category, &m.Verified, &m.Distance); err != nil {
			continue
		}
		key := m.Kind + ":" + m.Source
		if !seen[key] {
			out = append(out, m)
			seen[key] = true
		}
	}
	// 不足 limit 时，补充其他 kind
	if len(out) < limit {
		rows2, err2 := p.db.Query(
			`SELECT id, kind, source, content, COALESCE(service_id,''), COALESCE(category,''), COALESCE(verified,false),
			        embedding <=> $1::vector AS distance
			 FROM ai_memory_embeddings WHERE kind != $4
			 ORDER BY (embedding <=> $1::vector) / (GREATEST(priority, 0.1) *
			   GREATEST(0.5, 1.0 - (EXTRACT(EPOCH FROM NOW()) - created_at) / 31536000.0) *
			   CASE WHEN created_at > $3 THEN 1.5 ELSE 1.0 END *
			   CASE WHEN COALESCE(verified,false) THEN 1.35 ELSE 1.0 END)
			 LIMIT $2`,
			vecStr(emb), limit-len(out), sevenDaysAgo, preferKind)
		if err2 == nil {
			defer rows2.Close()
			for rows2.Next() {
				var m memoryHit
				if err := rows2.Scan(&m.ID, &m.Kind, &m.Source, &m.Content, &m.ServiceID, &m.Category, &m.Verified, &m.Distance); err != nil {
					continue
				}
				key := m.Kind + ":" + m.Source
				if !seen[key] {
					out = append(out, m)
					seen[key] = true
				}
			}
			noteRowsErr("searchMemoryByKind", rows2)
		}
	}
	return out, rows.Err()
}

// filterMemoriesByScope keeps global memories + those matching service/category context.
func filterMemoriesByScope(hits []memoryHit, serviceID, category string, limit int) []memoryHit {
	if serviceID == "" && category == "" {
		if limit > 0 && len(hits) > limit {
			return hits[:limit]
		}
		return hits
	}
	var out []memoryHit
	for _, h := range hits {
		global := strings.TrimSpace(h.ServiceID) == "" && strings.TrimSpace(h.Category) == ""
		if global || memoryMatchesScope(h.ServiceID, h.Category, serviceID, category) {
			out = append(out, h)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// searchMemoryByKinds 在指定 kind 集合内检索并按距离粗排，诊断场景优先 resolution > diagnosis > experience。
func (p *pgStore) searchMemoryByKinds(emb []float64, kinds []string, limit int) ([]memoryHit, error) {
	if limit <= 0 {
		limit = 5
	}
	if len(kinds) == 0 {
		return p.searchMemory(emb, limit)
	}
	kindBoost := map[string]float64{
		"resolution": 0.82, // 乘到 distance 上：更小 → 更靠前
		"knowledge":  0.86, // 已验证文档引用
		"pitfall":    0.88,
		"diagnosis":  0.92,
		"experience": 0.94,
		"alert":      1.15, // demote noisy auto alert cards
		"chat":       1.12,
	}
	var out []memoryHit
	seen := make(map[string]bool)
	per := (limit*2)/len(kinds) + 1
	if per < 2 {
		per = 2
	}
	for _, k := range kinds {
		hits, err := p.searchMemoryByKind(emb, k, per)
		if err != nil {
			continue
		}
		boost := kindBoost[k]
		if boost == 0 {
			boost = 1
		}
		for _, h := range hits {
			key := h.Kind + ":" + h.Source
			if seen[key] {
				continue
			}
			seen[key] = true
			h.Distance = h.Distance * boost
			if h.Verified {
				h.Distance *= 0.85 // verified memories rank ahead within the same kind band
			}
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// memoryContentHash 计算内容哈希用于去重判断（SHA256 前 16 位）。
func memoryContentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:8])
}

// hasDuplicateMemory 检查是否已存在高度相似的记忆（余弦距离 < 0.12，即相似度 > 88%）。
// 阈值从 0.05 放宽到 0.12，覆盖更多语义等价内容（如 "CPU 90%" vs "CPU 使用率超过 90%"）。
// 返回 duplicate ID 以便调用方执行合并逻辑。
func (p *pgStore) hasDuplicateMemory(emb []float64, kind string) (bool, int64, error) {
	var id int64
	err := p.db.QueryRow(
		`SELECT id FROM ai_memory_embeddings
		 WHERE kind = $2 AND embedding <=> $1::vector < 0.12
		 ORDER BY embedding <=> $1::vector LIMIT 1`,
		vecStr(emb), kind).Scan(&id)
	if err != nil {
		return false, 0, nil // no duplicate found
	}
	return true, id, nil
}

func (p *pgStore) mergeDuplicateMemoryEx(id int64, appendContent string, newEmb []float64, verified bool, serviceID, category string) error {
	_, err := p.db.Exec(
		`UPDATE ai_memory_embeddings
		 SET content = content || E'\n' || $2,
		     embedding = $3::vector,
		     created_at = $4,
		     priority = LEAST(GREATEST(priority, 0.1) * 1.15, $8),
		     verified = COALESCE(verified,false) OR $5,
		     service_id = CASE WHEN COALESCE(service_id,'')='' AND $6<>'' THEN $6 ELSE service_id END,
		     category = CASE WHEN COALESCE(category,'')='' AND $7<>'' THEN $7 ELSE category END,
		     last_hit_at = $4
		 WHERE id = $1`,
		id, appendContent, vecStr(newEmb), time.Now().Unix(), verified, serviceID, category, memoryPriorityCap)
	return err
}

func (p *pgStore) countVerifiedMemories() int {
	if p == nil || p.db == nil {
		return 0
	}
	var n int
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM ai_memory_embeddings WHERE COALESCE(verified,false)=true`).Scan(&n)
	return n
}

// markMemoryVerifiedBySource flips verified=true and gently boosts priority for kind+source.
func (p *pgStore) markMemoryVerifiedBySource(kind, source string) {
	if p == nil || p.db == nil || strings.TrimSpace(kind) == "" || strings.TrimSpace(source) == "" {
		return
	}
	_, _ = p.db.Exec(
		`UPDATE ai_memory_embeddings
		 SET verified = true,
		     priority = LEAST(GREATEST(priority, 0.1) * 1.2, $3),
		     last_hit_at = $4
		 WHERE kind = $1 AND source = $2`,
		kind, source, memoryPriorityCap, time.Now().Unix())
}

// touchMemoryHits 批量更新被检索命中的记忆的 last_hit_at 字段，
// 用于衰减策略判断“未被检索命中”的记忆。
func (p *pgStore) touchMemoryHits(ids []int64) {
	if len(ids) == 0 {
		return
	}
	now := time.Now().Unix()
	for _, id := range ids {
		_, _ = p.db.Exec(
			`UPDATE ai_memory_embeddings SET last_hit_at = $2 WHERE id = $1`,
			id, now)
	}
}

// ---- 正向强化：与 decayOldMemories 负向衰减对称，构成「采纳/成功/解决即强化」学习闭环 ----
//
// 检索排序公式为 distance / (priority * time_factor * recency)，priority 越大越靠前。此前
// priority 只会因衰减【下降】、从不上升，"好记忆"无法脱颖而出。这里补上正向半环：真实结果
// （被采纳 / 执行成功 / 事件解决 / 👍）把相关记忆的 priority 上调，让被验证有效的知识随使用上浮。
// 上限 5.0 与衰减下限 0.1 对称，避免单次反馈过度主导。
const memoryPriorityCap = 5.0

// boostMemoryPriority 按 factor 调整单条记忆优先级（factor>1 强化、<1 惩罚），并刷新 last_hit_at。
func (p *pgStore) boostMemoryPriority(id int64, factor float64) {
	if factor <= 0 {
		factor = 1.3
	}
	if _, err := p.db.Exec(
		`UPDATE ai_memory_embeddings
		 SET priority = LEAST(GREATEST(priority, 0.1) * $2, $3), last_hit_at = $4
		 WHERE id = $1`,
		id, factor, memoryPriorityCap, time.Now().Unix()); err != nil {
		slog.Warn("记忆强化失败", "id", id, "err", err)
	}
}

// boostMemoryBySource 对某 kind+source 的记忆整体调整优先级。适用于 source 唯一的场景
// （incident:ID / playbook:ID / session:ID）。返回受影响条数。
func (p *pgStore) boostMemoryBySource(kind, source string, factor float64) int64 {
	if factor <= 0 {
		factor = 1.3
	}
	res, err := p.db.Exec(
		`UPDATE ai_memory_embeddings
		 SET priority = LEAST(GREATEST(priority, 0.1) * $3, $4), last_hit_at = $5
		 WHERE kind = $1 AND source = $2`,
		kind, source, factor, memoryPriorityCap, time.Now().Unix())
	if err != nil {
		slog.Warn("按来源强化记忆失败", "kind", kind, "source", source, "err", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// boostNearestMemory 找与 emb 语义最相近的一条 kind 记忆并调整其优先级，返回其 id。
// 适用于 source 不唯一、需按内容定位具体交互的场景（如 AI 辅助采纳反馈）。
func (p *pgStore) boostNearestMemory(emb []float64, kind string, factor float64) (int64, bool) {
	var id int64
	if err := p.db.QueryRow(
		`SELECT id FROM ai_memory_embeddings WHERE kind = $2 ORDER BY embedding <=> $1::vector LIMIT 1`,
		vecStr(emb), kind).Scan(&id); err != nil {
		return 0, false
	}
	p.boostMemoryPriority(id, factor)
	return id, true
}

// ---- AI 技能库（自进化）：提炼产物的存取 / 检索 / 强化 / 管理 ----

// Skill 是从经验记忆中提炼出的一条可复用 SOP。
type Skill struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Trigger      string  `json:"trigger"` // 何时适用
	Steps        string  `json:"steps"`   // 怎么做
	Tags         string  `json:"tags"`
	UseCount     int     `json:"use_count"`
	SuccessCount int     `json:"success_count"`
	Priority     float64 `json:"priority"`
	Source       string  `json:"source"`
	Status       string  `json:"status"` // active | draft | archived
	Version      int     `json:"version"`
	ServiceIDs   string  `json:"service_ids,omitempty"` // comma-separated BusinessService ids; empty=global
	Categories   string  `json:"categories,omitempty"`  // comma-separated host categories; empty=global
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`
	Distance     float64 `json:"distance,omitempty"`
}

func (p *pgStore) insertSkill(name, trigger, steps, tags, source string, emb []float64) (int64, error) {
	now := time.Now().Unix()
	var id int64
	if len(emb) == 0 {
		return p.insertSkillNoEmbed(name, trigger, steps, tags, source)
	}
	err := p.db.QueryRow(
		`INSERT INTO ai_skills(name, trigger_desc, steps, tags, embedding, source, created_at, updated_at)
		 VALUES($1,$2,$3,$4,$5::vector,$6,$7,$7) RETURNING id`,
		name, trigger, steps, tags, vecStr(emb), source, now).Scan(&id)
	return id, err
}

func (p *pgStore) insertSkillNoEmbed(name, trigger, steps, tags, source string) (int64, error) {
	now := time.Now().Unix()
	var id int64
	err := p.db.QueryRow(
		`INSERT INTO ai_skills(name, trigger_desc, steps, tags, embedding, source, created_at, updated_at)
		 VALUES($1,$2,$3,$4,NULL,$5,$6,$6) RETURNING id`,
		name, trigger, steps, tags, source, now).Scan(&id)
	return id, err
}

func (p *pgStore) findSkillByNameSource(name, source string) (int64, bool) {
	var id int64
	err := p.db.QueryRow(
		`SELECT id FROM ai_skills WHERE name=$1 AND source=$2 ORDER BY updated_at DESC LIMIT 1`,
		name, source).Scan(&id)
	return id, err == nil && id > 0
}

func (p *pgStore) updateSkillText(id int64, name, trigger, steps string) error {
	_, err := p.db.Exec(
		`UPDATE ai_skills SET name=$2, trigger_desc=$3, steps=$4, version=COALESCE(version,1)+1, updated_at=$5 WHERE id=$1`,
		id, name, trigger, steps, time.Now().Unix())
	return err
}

// findSimilarSkill 返回与 emb 语义最近的【活跃】技能 id（若距离 ≤ maxDist），用于提炼时去重/合并。
func (p *pgStore) findSimilarSkill(emb []float64, maxDist float64) (int64, bool) {
	var id int64
	var dist float64
	if err := p.db.QueryRow(
		`SELECT id, embedding <=> $1::vector AS d FROM ai_skills
		 WHERE COALESCE(status,'active')='active'
		 ORDER BY embedding <=> $1::vector LIMIT 1`,
		vecStr(emb)).Scan(&id, &dist); err != nil || dist > maxDist {
		return 0, false
	}
	return id, true
}

// updateSkill 覆盖一条技能（用于「用中自改进」——把更好的步骤写回），并递增 version。
func (p *pgStore) updateSkill(id int64, name, trigger, steps string, emb []float64) error {
	_, err := p.db.Exec(
		`UPDATE ai_skills SET name=$2, trigger_desc=$3, steps=$4, embedding=$5::vector,
		 version=COALESCE(version,1)+1, updated_at=$6 WHERE id=$1`,
		id, name, trigger, steps, vecStr(emb), time.Now().Unix())
	return err
}

// searchSkills 按 距离/优先级 检索最相关技能，供注入提示词。
// maxDist 是【原始余弦距离】上限：先用它在 WHERE 里筛掉真正不相关的技能，再对相关候选做
// priority 加权排序取 Top-K。此顺序很关键——否则高 priority 的无关技能会凭加权分挤进 LIMIT、
// 再被上层按原始距离过滤掉，把真正相关但 priority 低的技能挤出候选集（系统越学越严重）。
func (p *pgStore) searchSkills(emb []float64, limit int, maxDist float64) ([]Skill, error) {
	if limit <= 0 {
		limit = 5
	}
	if maxDist <= 0 {
		maxDist = skillRelevantDist
	}
	rows, err := p.db.Query(
		`SELECT id, name, trigger_desc, steps, tags, use_count, success_count, priority, source,
		        COALESCE(version,1), COALESCE(service_ids,''), COALESCE(categories,''),
		        embedding <=> $1::vector AS distance
		 FROM ai_skills
		 WHERE COALESCE(status,'active')='active' AND embedding <=> $1::vector <= $3
		 ORDER BY (embedding <=> $1::vector) / GREATEST(priority, 0.1) LIMIT $2`,
		vecStr(emb), limit, maxDist)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Skill
	for rows.Next() {
		var s Skill
		if err := rows.Scan(&s.ID, &s.Name, &s.Trigger, &s.Steps, &s.Tags, &s.UseCount, &s.SuccessCount, &s.Priority, &s.Source,
			&s.Version, &s.ServiceIDs, &s.Categories, &s.Distance); err == nil {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

// searchSkillsScoped filters active skills by optional BusinessService / host category.
// Empty scope on a skill = global. When context provides service/category, scoped skills
// that do not match are excluded; when context is empty, all active skills are eligible.
func (p *pgStore) searchSkillsScoped(emb []float64, limit int, maxDist float64, serviceID, category string) ([]Skill, error) {
	oversample := limit
	if serviceID != "" || category != "" {
		oversample = limit * 3
	}
	skills, err := p.searchSkills(emb, oversample, maxDist)
	if err != nil {
		return nil, err
	}
	if serviceID == "" && category == "" {
		if len(skills) > limit {
			skills = skills[:limit]
		}
		return skills, nil
	}
	var out []Skill
	for _, sk := range skills {
		if skillMatchesScope(sk, serviceID, category) {
			out = append(out, sk)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func skillMatchesScope(sk Skill, serviceID, category string) bool {
	if svc := strings.TrimSpace(sk.ServiceIDs); svc != "" && serviceID != "" {
		hit := false
		for _, id := range strings.Split(svc, ",") {
			if strings.TrimSpace(id) == serviceID {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if cats := strings.TrimSpace(sk.Categories); cats != "" && category != "" {
		hit := false
		for _, c := range strings.Split(cats, ",") {
			if strings.EqualFold(strings.TrimSpace(c), category) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// listSkillsFiltered includeArchived=true 时含已归档；默认含 active+draft（draft 需人工激活）。
func (p *pgStore) listSkillsFiltered(includeArchived bool) ([]Skill, error) {
	q := `SELECT id, name, trigger_desc, steps, tags, use_count, success_count, priority, source,
	             COALESCE(status,'active'), COALESCE(version,1), COALESCE(service_ids,''), COALESCE(categories,''),
	             created_at, updated_at
	      FROM ai_skills`
	if !includeArchived {
		q += ` WHERE COALESCE(status,'active') IN ('active','draft')`
	}
	q += ` ORDER BY CASE COALESCE(status,'active') WHEN 'draft' THEN 0 WHEN 'active' THEN 1 ELSE 2 END,
	             priority DESC, updated_at DESC LIMIT 500`
	rows, err := p.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Skill
	for rows.Next() {
		var s Skill
		if err := rows.Scan(&s.ID, &s.Name, &s.Trigger, &s.Steps, &s.Tags, &s.UseCount, &s.SuccessCount, &s.Priority, &s.Source,
			&s.Status, &s.Version, &s.ServiceIDs, &s.Categories, &s.CreatedAt, &s.UpdatedAt); err == nil {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

func (p *pgStore) setSkillStatus(id int64, status string) error {
	status = strings.TrimSpace(status)
	if status != "active" && status != "archived" && status != "draft" {
		return fmt.Errorf("invalid status")
	}
	_, err := p.db.Exec(`UPDATE ai_skills SET status=$2, updated_at=$3 WHERE id=$1`, id, status, time.Now().Unix())
	return err
}

func (p *pgStore) setSkillScope(id int64, serviceIDs, categories string) error {
	_, err := p.db.Exec(
		`UPDATE ai_skills SET service_ids=$2, categories=$3, updated_at=$4 WHERE id=$1`,
		id, strings.TrimSpace(serviceIDs), strings.TrimSpace(categories), time.Now().Unix())
	return err
}

func (p *pgStore) countSkillsByStatus(status string) int {
	if p == nil || p.db == nil {
		return 0
	}
	var n int
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM ai_skills WHERE COALESCE(status,'active')=$1`, status).Scan(&n)
	return n
}

// mergeSkills 将 dropID 合并进 keepID：累加使用计数后删除 drop；保留 keep 的步骤（已验证优先）。
func (p *pgStore) mergeSkills(keepID, dropID int64) error {
	if keepID == 0 || dropID == 0 || keepID == dropID {
		return fmt.Errorf("invalid merge ids")
	}
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var uc, sc int
	if err := tx.QueryRow(`SELECT use_count, success_count FROM ai_skills WHERE id=$1`, dropID).Scan(&uc, &sc); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE ai_skills SET use_count=use_count+$2, success_count=success_count+$3, updated_at=$4 WHERE id=$1`,
		keepID, uc, sc, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ai_skills WHERE id=$1`, dropID); err != nil {
		return err
	}
	return tx.Commit()
}

// archiveStaleSkills 归档低质/过时技能：权重过低、或使用多次且成功率极低、或长期未更新且几乎无成功。
func (p *pgStore) archiveStaleSkills() int {
	now := time.Now().Unix()
	cutoff := now - 90*24*3600
	res, err := p.db.Exec(
		`UPDATE ai_skills SET status='archived', updated_at=$1
		 WHERE COALESCE(status,'active')='active' AND (
		   priority < 0.25
		   OR (use_count >= 5 AND success_count*1.0/GREATEST(use_count,1) < 0.15)
		   OR (updated_at < $2 AND success_count = 0 AND use_count >= 3)
		 )`, now, cutoff)
	if err != nil {
		slog.Warn("技能归档失败", "err", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

func (p *pgStore) deleteSkill(id int64) error {
	_, err := p.db.Exec(`DELETE FROM ai_skills WHERE id=$1`, id)
	return err
}

// recordSkillUse 记录一次技能被检索命中（use_count++），成功时额外强化 priority + success_count。
func (p *pgStore) recordSkillUse(id int64, success bool) {
	sc, factor := 0, 1.0
	if success {
		sc, factor = 1, 1.15
	}
	_, _ = p.db.Exec(
		`UPDATE ai_skills SET use_count=use_count+1, success_count=success_count+$2,
		 priority=LEAST(GREATEST(priority,0.1)*$3, 5.0), updated_at=$4 WHERE id=$1`,
		id, sc, factor, time.Now().Unix())
}

// boostSkillNearest 语义定位最近技能并强化（事件解决 / 采纳时调用），实现技能层面的学习闭环。
// 同步 use_count++（视强化为「一次被验证的使用」），保证 success_count ≤ use_count，前端成功率不越界。
func (p *pgStore) boostSkillNearest(emb []float64, factor float64) {
	var id int64
	if err := p.db.QueryRow(
		`SELECT id FROM ai_skills WHERE COALESCE(status,'active')='active'
		 ORDER BY embedding <=> $1::vector LIMIT 1`, vecStr(emb)).Scan(&id); err == nil {
		_, _ = p.db.Exec(
			`UPDATE ai_skills SET priority=LEAST(GREATEST(priority,0.1)*$2,5.0), use_count=use_count+1, success_count=success_count+1, updated_at=$3 WHERE id=$1`,
			id, factor, time.Now().Unix())
	}
}

// penalizeSkillNearest 差评时下调最近技能权重（不增加 success_count）。
func (p *pgStore) penalizeSkillNearest(emb []float64, factor float64) {
	if factor <= 0 || factor >= 1 {
		factor = 0.6
	}
	var id int64
	if err := p.db.QueryRow(
		`SELECT id FROM ai_skills WHERE COALESCE(status,'active')='active'
		 ORDER BY embedding <=> $1::vector LIMIT 1`, vecStr(emb)).Scan(&id); err == nil {
		_, _ = p.db.Exec(
			`UPDATE ai_skills SET priority=GREATEST(priority*$2, 0.1), updated_at=$3 WHERE id=$1`,
			id, factor, time.Now().Unix())
	}
}

// skillProven 判断一条技能是否已被现实验证（有成功记录或被多次使用）——提炼去重时用它保护
// 已验证的优质 SOP 不被一次较差的新生成覆盖。
func (p *pgStore) skillProven(id int64) bool {
	var uc, sc int
	if err := p.db.QueryRow(`SELECT use_count, success_count FROM ai_skills WHERE id=$1`, id).Scan(&uc, &sc); err != nil {
		return false
	}
	return sc > 0 || uc >= 3
}

// memoryBrowseItem 记忆浏览器列表项（不含 embedding）。
type memoryBrowseItem struct {
	ID        int64   `json:"id"`
	Kind      string  `json:"kind"`
	Source    string  `json:"source"`
	Content   string  `json:"content"`
	CreatedAt int64   `json:"created_at"`
	LastHitAt int64   `json:"last_hit_at"`
	Priority  float64 `json:"priority"`
	ServiceID string  `json:"service_id,omitempty"`
	Category  string  `json:"category,omitempty"`
	Verified  bool    `json:"verified"`
}

func (p *pgStore) listMemoriesFiltered(kind, verifiedFilter string, limit, offset int) ([]memoryBrowseItem, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	argN := 1
	if kind != "" {
		where = append(where, fmt.Sprintf("kind = $%d", argN))
		args = append(args, kind)
		argN++
	}
	switch strings.ToLower(strings.TrimSpace(verifiedFilter)) {
	case "1", "true", "yes":
		where = append(where, "COALESCE(verified,false)=true")
	case "0", "false", "no":
		where = append(where, "COALESCE(verified,false)=false")
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	countQ := "SELECT COUNT(*) FROM ai_memory_embeddings WHERE " + whereSQL
	if err := p.db.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := fmt.Sprintf(`SELECT id, kind, source, content, created_at, COALESCE(last_hit_at,0), COALESCE(priority,1),
		COALESCE(service_id,''), COALESCE(category,''), COALESCE(verified,false)
		FROM ai_memory_embeddings WHERE %s
		ORDER BY verified DESC, priority DESC, created_at DESC LIMIT $%d OFFSET $%d`, whereSQL, argN, argN+1)
	args = append(args, limit, offset)
	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []memoryBrowseItem
	for rows.Next() {
		var m memoryBrowseItem
		if err := rows.Scan(&m.ID, &m.Kind, &m.Source, &m.Content, &m.CreatedAt, &m.LastHitAt, &m.Priority,
			&m.ServiceID, &m.Category, &m.Verified); err == nil {
			if len([]rune(m.Content)) > 800 {
				m.Content = string([]rune(m.Content)[:800]) + "…"
			}
			out = append(out, m)
		}
	}
	return out, total, rows.Err()
}

func (p *pgStore) deleteMemory(id int64) error {
	_, err := p.db.Exec(`DELETE FROM ai_memory_embeddings WHERE id = $1`, id)
	return err
}

func (p *pgStore) memoryKindStats() map[string]int {
	out := map[string]int{}
	rows, err := p.db.Query(`SELECT kind, COUNT(*) FROM ai_memory_embeddings GROUP BY kind`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err == nil {
			out[k] = n
		}
	}
	noteRowsErr("memoryKindStats", rows)
	return out
}

// memoriesForDistill 取用于技能提炼的候选记忆：experience/resolution/diagnosis 类、较新、
// 按优先级(被强化程度)优先。这些是"被验证有价值"的经验，最适合提炼成可复用技能。
func (p *pgStore) memoriesForDistill(sinceTs int64, limit int) []memoryHit {
	rows, err := p.db.Query(
		`SELECT id, kind, source, content FROM ai_memory_embeddings
		 WHERE kind IN ('experience','resolution','diagnosis') AND created_at >= $1
		 ORDER BY priority DESC, created_at DESC LIMIT $2`,
		sinceTs, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []memoryHit
	for rows.Next() {
		var m memoryHit
		if err := rows.Scan(&m.ID, &m.Kind, &m.Source, &m.Content); err == nil {
			out = append(out, m)
		}
	}
	noteRowsErr("memoriesForDistill", rows)
	return out
}

// decayOldMemories 对超过 90 天且未被检索命中的记忆降低优先级（priority *= 0.8），
// 而非删除——保留历史知识但让新鲜记忆在检索时排名更高。
// 建议每天调用一次（由 Server 启动时 goroutine 驱动）。
func (p *pgStore) decayOldMemories() {
	cutoff := time.Now().Add(-90 * 24 * time.Hour).Unix()
	res, err := p.db.Exec(
		`UPDATE ai_memory_embeddings
		 SET priority = GREATEST(priority * 0.8, 0.1)
		 WHERE created_at < $1 AND (last_hit_at = 0 OR last_hit_at < $1)`,
		cutoff)
	if err != nil {
		slog.Warn("记忆衰减执行失败", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("记忆衰减完成", "降低优先级条数", n)
	}
}

// cleanupExpiredMemories 删除超过 365 天且优先级已降至 < 0.3 的记忆。
// 这些记忆已经历多次衰减且从未被检索命中，可安全清理以释放存储空间。
// P3-2: 记忆生命周期管理的硬清理环节。
func (p *pgStore) cleanupExpiredMemories() {
	cutoff := time.Now().Add(-365 * 24 * time.Hour).Unix()
	res, err := p.db.Exec(
		`DELETE FROM ai_memory_embeddings
		 WHERE created_at < $1 AND priority < 0.3
		   AND (last_hit_at = 0 OR last_hit_at < $1)`,
		cutoff)
	if err != nil {
		slog.Warn("记忆清理执行失败", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("记忆清理完成", "删除过期记忆", n)
	}
}

// capMemoriesByKind 对每种 kind 的记忆数量设置上限（maxPerKind），
// 超出时删除最旧且优先级最低的记忆，防止单一类型无限增长。
func (p *pgStore) capMemoriesByKind(maxPerKind int) {
	if maxPerKind <= 0 {
		maxPerKind = 2000
	}
	rows, err := p.db.Query(`SELECT kind, COUNT(*) FROM ai_memory_embeddings GROUP BY kind HAVING COUNT(*) > $1`, maxPerKind)
	if err != nil {
		slog.Warn("记忆容量检查失败", "err", err)
		return
	}
	defer rows.Close()
	totalDeleted := int64(0)
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			continue
		}
		excess := count - maxPerKind
		if excess <= 0 {
			continue
		}
		// 删除最旧且优先级最低的 excess 条
		res, err := p.db.Exec(
			`DELETE FROM ai_memory_embeddings WHERE id IN (
				SELECT id FROM ai_memory_embeddings WHERE kind = $1
				ORDER BY priority ASC, created_at ASC LIMIT $2
			)`, kind, excess)
		if err != nil {
			slog.Warn("记忆容量裁剪失败", "kind", kind, "err", err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			totalDeleted += n
		}
	}
	noteRowsErr("capMemoriesByKind", rows)
	if totalDeleted > 0 {
		slog.Info("记忆容量裁剪完成", "删除总数", totalDeleted, "上限", maxPerKind)
	}
}

// ============================================================================
// 经验规则库 CRUD
// ============================================================================

// experienceRule is one manually-curated or AI-extracted best-practice rule.
type experienceRule struct {
	ID         int64  `json:"id"`
	Pattern    string `json:"pattern"`
	Conclusion string `json:"conclusion"`
	Severity   string `json:"severity,omitempty"`
	IncidentID int64  `json:"incident_id,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

func (p *pgStore) insertExperienceRule(r experienceRule) (int64, error) {
	var id int64
	err := p.db.QueryRow(
		`INSERT INTO experience_rules(pattern, conclusion, severity, incident_id)
		 VALUES($1, $2, $3, $4) RETURNING id`,
		r.Pattern, r.Conclusion, r.Severity, r.IncidentID,
	).Scan(&id)
	return id, err
}

func (p *pgStore) listExperienceRules() ([]experienceRule, error) {
	rows, err := p.db.Query(`SELECT id, pattern, conclusion, severity, incident_id, created_at FROM experience_rules ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []experienceRule
	for rows.Next() {
		var r experienceRule
		if err := rows.Scan(&r.ID, &r.Pattern, &r.Conclusion, &r.Severity, &r.IncidentID, &r.CreatedAt); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *pgStore) deleteExperienceRule(id int64) error {
	_, err := p.db.Exec(`DELETE FROM experience_rules WHERE id=$1`, id)
	return err
}

// --- Sreyun rules CRUD ---

type sreyunRule struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Priority    int             `json:"priority"`
	Enabled     bool            `json:"enabled"`
	Config      json.RawMessage `json:"config"`
	CreatedAt   string          `json:"created_at,omitempty"`
	UpdatedAt   string          `json:"updated_at,omitempty"`
}

func (p *pgStore) listSreyunRules() ([]sreyunRule, error) {
	rows, err := p.db.Query(`SELECT id,name,description,priority,enabled,config,created_at,updated_at FROM hermes_rules ORDER BY priority DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sreyunRule
	for rows.Next() {
		var r sreyunRule
		var ca, ua sql.NullTime
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.Priority, &r.Enabled, &r.Config, &ca, &ua); err != nil {
			continue
		}
		if ca.Valid {
			r.CreatedAt = ca.Time.Format(time.RFC3339)
		}
		if ua.Valid {
			r.UpdatedAt = ua.Time.Format(time.RFC3339)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *pgStore) upsertSreyunRule(r sreyunRule) (int64, error) {
	if r.ID > 0 {
		_, err := p.db.Exec(`UPDATE hermes_rules SET name=$1,description=$2,priority=$3,enabled=$4,config=$5,updated_at=NOW() WHERE id=$6`,
			r.Name, r.Description, r.Priority, r.Enabled, r.Config, r.ID)
		return r.ID, err
	}
	var id int64
	err := p.db.QueryRow(`INSERT INTO hermes_rules(name,description,priority,enabled,config) VALUES($1,$2,$3,$4,$5) RETURNING id`,
		r.Name, r.Description, r.Priority, r.Enabled, r.Config).Scan(&id)
	return id, err
}

func (p *pgStore) deleteSreyunRule(id int64) error {
	_, err := p.db.Exec(`DELETE FROM hermes_rules WHERE id=$1`, id)
	return err
}

// --- Sreyun templates CRUD ---

type sreyunTemplate struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content"`
	Category    string `json:"category"`
	Version     int    `json:"version"`
	Active      bool   `json:"active"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

func (p *pgStore) listSreyunTemplates(activeOnly bool) ([]sreyunTemplate, error) {
	q := `SELECT id,name,description,content,category,version,active,created_at,updated_at FROM hermes_templates`
	if activeOnly {
		q += ` WHERE active=true`
	}
	q += ` ORDER BY id ASC`
	rows, err := p.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sreyunTemplate
	for rows.Next() {
		var t sreyunTemplate
		var ca, ua sql.NullTime
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Content, &t.Category, &t.Version, &t.Active, &ca, &ua); err != nil {
			continue
		}
		if ca.Valid {
			t.CreatedAt = ca.Time.Format(time.RFC3339)
		}
		if ua.Valid {
			t.UpdatedAt = ua.Time.Format(time.RFC3339)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *pgStore) upsertSreyunTemplate(t sreyunTemplate) (int64, error) {
	if t.ID > 0 {
		_, err := p.db.Exec(`UPDATE hermes_templates SET name=$1,description=$2,content=$3,category=$4,version=version+1,active=$5,updated_at=NOW() WHERE id=$6`,
			t.Name, t.Description, t.Content, t.Category, t.Active, t.ID)
		return t.ID, err
	}
	var id int64
	err := p.db.QueryRow(`INSERT INTO hermes_templates(name,description,content,category,active) VALUES($1,$2,$3,$4,$5) RETURNING id`,
		t.Name, t.Description, t.Content, t.Category, t.Active).Scan(&id)
	return id, err
}

func (p *pgStore) deleteSreyunTemplate(id int64) error {
	_, err := p.db.Exec(`DELETE FROM hermes_templates WHERE id=$1`, id)
	return err
}

// --- Sreyun sessions ---

func (p *pgStore) loadSreyunSession(id int64) ([]byte, error) {
	var raw []byte
	err := p.db.QueryRow(`SELECT messages FROM hermes_sessions WHERE id=$1`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return raw, err
}

func (p *pgStore) saveSreyunSession(id int64, messages []byte, incidentID int64) (int64, error) {
	if id > 0 {
		_, err := p.db.Exec(`UPDATE hermes_sessions SET messages=$1,updated_at=NOW() WHERE id=$2`, messages, id)
		return id, err
	}
	var newID int64
	err := p.db.QueryRow(`INSERT INTO hermes_sessions(incident_id,messages) VALUES($1,$2) RETURNING id`, incidentID, messages).Scan(&newID)
	return newID, err
}

func (p *pgStore) listSreyunSessions(limit int) ([]map[string]any, error) {
	rows, err := p.db.Query(`SELECT id,incident_id,status,created_at,updated_at,messages FROM hermes_sessions ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, iid int64
		var status string
		var ca, ua sql.NullTime
		var raw []byte
		if err := rows.Scan(&id, &iid, &status, &ca, &ua, &raw); err != nil {
			continue
		}
		m := map[string]any{"id": id, "incident_id": iid, "status": status}
		if ca.Valid {
			m["created_at"] = ca.Time.Format(time.RFC3339)
		}
		if ua.Valid {
			m["updated_at"] = ua.Time.Format(time.RFC3339)
		}
		// 从消息内容提取标题（首条用户消息）、摘要（末条消息）与条数，便于前端列表展示
		title, summary, count := sreyunSessionDigest(raw)
		m["title"] = title
		m["summary"] = summary
		m["msg_count"] = count
		out = append(out, m)
	}
	return out, rows.Err()
}

// sreyunSessionDigest 从会话 messages(JSON) 提取标题（首条 user 内容）、摘要（末条内容）与消息条数。
func sreyunSessionDigest(raw []byte) (title, summary string, count int) {
	if len(raw) == 0 {
		return "新会话", "", 0
	}
	var msgs []map[string]string
	if json.Unmarshal(raw, &msgs) != nil {
		return "新会话", "", 0
	}
	count = len(msgs)
	for _, m := range msgs {
		if m["role"] == "user" && strings.TrimSpace(m["content"]) != "" {
			title = sreyunTrunc(m["content"], 24)
			break
		}
	}
	if title == "" {
		title = "新会话"
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.TrimSpace(msgs[i]["content"]) != "" {
			summary = sreyunTrunc(msgs[i]["content"], 40)
			break
		}
	}
	return title, summary, count
}

// sreyunTrunc 按 Unicode 字符（rune）截断字符串，避免中文被切成半个字符。
func sreyunTrunc(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (p *pgStore) close() {
	if p != nil && p.db != nil {
		_ = p.db.Close()
	}
}

// bindPG wires an already-open PostgreSQL store as the persistence backend for
// all durable relational state: incidents, work orders, host metadata, alert-ack
// states and login sessions. It loads existing rows on start, then periodically
// writes the current state back.
func (s *Server) bindPG(ps *pgStore) {
	if ps == nil {
		return
	}
	s.pg = ps
	s.wireAIGovPG()
	if incs, err := ps.loadIncidents(); err == nil && len(incs) > 0 {
		s.incidents.Import(incs)
	}
	if tks, err := ps.loadTickets(); err == nil && len(tks) > 0 {
		s.tickets.Import(tks)
	}
	if pages, err := ps.loadOnCallPages(); err == nil && len(pages) > 0 {
		s.oncall.Import(pages)
	}
	if chgs, err := ps.loadChangeRecords(); err == nil && len(chgs) > 0 {
		s.changes.Import(chgs)
	}
	// Login sessions survive restart (no forced re-login in dual-DB mode).
	if raw, _ := ps.loadKV("sessions"); raw != nil {
		var sess map[string]dbSession
		if json.Unmarshal(raw, &sess) == nil {
			s.auth.importSessions(sess)
		}
	}
	// Notification-center feed + read state survive restart.
	if raw, _ := ps.loadKV("messages"); raw != nil {
		var msgs []Message
		if json.Unmarshal(raw, &msgs) == nil {
			s.messages.importMsgs(msgs)
		}
	}
	// AI inspection history survives restart (SRE 中枢巡检报告).
	if raw, _ := ps.loadKV("ai_inspections"); raw != nil {
		var reps []InspectionReport
		if json.Unmarshal(raw, &reps) == nil {
			s.ai.importReports(reps)
		}
	}
	// Host deep-inspect batches survive restart (主机深度体检).
	if s.inspect != nil {
		s.inspect.setPersist(func(batches []*hostInspectBatch) {
			raw, err := json.Marshal(batches)
			if err != nil {
				return
			}
			if err := ps.saveKV("host_inspect_batches", raw); err != nil {
				slog.Warn("persist host inspect batches failed", "err", err)
			}
		})
		if raw, _ := ps.loadKV("host_inspect_batches"); raw != nil {
			var batches []*hostInspectBatch
			if json.Unmarshal(raw, &batches) == nil {
				s.inspect.importBatches(batches)
			}
		}
	}
	// Remediation run history survives restart (自动修复执行历史).
	if raw, _ := ps.loadKV("remediation_runs"); raw != nil {
		var runs []RemediationRun
		if json.Unmarshal(raw, &runs) == nil {
			s.remediation.Import(runs)
			for _, run := range runs {
				ps.upsertRemediationRun(run)
			}
		}
	}
	// SLO burning state survives restart (SLO 燃烧状态).
	if raw, _ := ps.loadKV("slo_burning"); raw != nil {
		var burning map[string]bool
		if json.Unmarshal(raw, &burning) == nil {
			s.slos.importBurning(burning)
		}
	}
	// Aggregated agent logs survive restart (日志检索缓冲).
	if raw, _ := ps.loadKV("logs"); raw != nil {
		var logs []StoredLog
		if json.Unmarshal(raw, &logs) == nil {
			s.logs.importLogs(logs)
		}
	}
	// Playbook execution history survives restart (剧本执行审计).
	if raw, _ := ps.loadKV("playbook_executions"); raw != nil {
		var execs []PlaybookExecution
		if json.Unmarshal(raw, &execs) == nil {
			s.playbooks.importExecutions(execs)
			// Migrate KV blob into dedicated table (idempotent upsert).
			for _, e := range execs {
				ps.upsertPlaybookExecution(e)
			}
		}
	}
	if list := ps.listPlaybookExecutions(500); len(list) > 0 {
		s.playbooks.importExecutions(list)
	}
	if list := ps.listRemediationRuns(1000); len(list) > 0 {
		s.remediation.Import(list)
	}
	// Schedule / cooldown clocks survive restart (禁止临时存储).
	if raw, _ := ps.loadKV("playbook_last_run"); raw != nil {
		var m map[string]int64
		if json.Unmarshal(raw, &m) == nil {
			s.playbooks.importLastRun(m)
		}
	}
	if raw, _ := ps.loadKV("remediation_guards"); raw != nil {
		var g struct {
			LastRun map[string]int64   `json:"last_run"`
			Hourly  map[string][]int64 `json:"hourly"`
		}
		if json.Unmarshal(raw, &g) == nil {
			s.remediation.ImportGuards(g.LastRun, g.Hourly)
		}
	}
	go func() {
		t := time.NewTicker(pgFlushEvery)
		defer t.Stop()
		tick := 0
		for range t.C {
			tick++
			s.pgFlush(ps, tick%pgFlushHeavyEveryNth == 0)
		}
	}()
}

const (
	// pgFlushEvery 是内存态镜像回写 PG 的周期。
	pgFlushEvery = 15 * time.Second
	// pgFlushHeavyEveryNth 决定多少轮 flush 才做一次「重量级」刷写，即活动日志 blob
	// 加上带指标的整行 hosts 回写。
	//
	// 日志 blob 原来是每 2 轮（30 秒）一次，即每天 2880 次 × 约 1.4 MB ≈ 4 GB 的 TOAST
	// 重写与等量 WAL——而这个 blob 只是重启回填用的缓存，真正的审计留痕已经由
	// audit_log/audit_log_p 哈希链同步落库。
	//
	// hosts 行同理：里面的 Latest/Custom 是纯时序数据，每个上报周期都在变，因此内容
	// 哈希去重对它完全无效——500 台机群每天约 290 万次整行重写、数 GB WAL，而同一批
	// 数据在 VictoriaMetrics 里压缩后只有几十 MB。PG 侧只需要「重启后回填最后已知
	// 状态」，慢周期完全够用。
	//
	// 放慢到 5 分钟后，崩溃最多损失几分钟的内存环预热数据与指标快照；审计不受任何
	// 影响，在线主机重启后几秒内就会用新上报覆盖。退出前那次 flush 一定是重量级的。
	pgFlushHeavyEveryNth = 20 // 20 × 15s = 5min
)

// pgFlush persists the current relational state to PostgreSQL (also called on
// shutdown for a final flush). heavy gates the writes whose payload is large or
// changes every cycle — the aggregated-log blob and the metric-carrying half of
// the hosts rows — so the 15s flush does not rewrite them every time.
func (s *Server) pgFlush(ps *pgStore, heavy bool) {
	defer observePGFlush(time.Now(), heavy) // 刷写延迟是 PG 撑不撑得住最早的信号，见 metrics_prom.go
	if err := ps.saveIncidents(s.incidents.Export()); err != nil {
		slog.Warn("PG 同步事件失败", "err", err)
	}
	if err := ps.saveTickets(s.tickets.Export()); err != nil {
		slog.Warn("PG 同步工单失败", "err", err)
	}
	if err := ps.saveOnCallPages(s.oncall.Export()); err != nil {
		slog.Warn("PG 同步 On-call pages 失败", "err", err)
	}
	if err := ps.saveChangeRecords(s.changes.Export()); err != nil {
		slog.Warn("PG 同步变更记录失败", "err", err)
	}
	if err := ps.saveHosts(s.store.exportHosts(), heavy); err != nil {
		slog.Warn("PG 同步主机失败", "err", err)
	}
	// 下面这些 blob 每 15 秒都会被序列化一遍，但内容大多数时候完全没变。
	// saveKVIfChanged 用内容哈希把「没变还要写」的那部分整个掐掉：写才产生死元组
	// 和 WAL，不写就是零成本（见 pgstore_writecache.go 顶部的实测背景）。
	if raw, err := json.Marshal(s.store.exportAlertStates()); err == nil {
		_ = ps.saveKVIfChanged("alert_states", raw)
	}
	if raw, err := json.Marshal(s.auth.exportSessions()); err == nil {
		_ = ps.saveKVIfChanged("sessions", raw)
	}
	if raw, err := json.Marshal(s.messages.export()); err == nil {
		_ = ps.saveKVIfChanged("messages", raw)
	}
	// AI inspection history is small (≤ inspectionReportCap) — persist every flush.
	if raw, err := json.Marshal(s.ai.exportReports()); err == nil {
		_ = ps.saveKVIfChanged("ai_inspections", raw)
	}
	// Remediation run history is small (≤ remediationRunCap) — persist every flush.
	if raw, err := json.Marshal(s.remediation.Export()); err == nil {
		_ = ps.saveKVIfChanged("remediation_runs", raw)
	}
	// SLO burning state is tiny — persist every flush.
	if raw, err := json.Marshal(s.slos.exportBurning()); err == nil {
		_ = ps.saveKVIfChanged("slo_burning", raw)
	}
	// 活动日志 blob 是最贵的一个：8000 行整块序列化约 1.4 MB，而且每条日志行
	// **已经**由 appendLog → appendAudit 同步进了 audit_log/audit_log_p 哈希链
	// （store.go:791），这个 blob 只是重启时快速回填内存环的缓存。所以它按
	// pgFlushHeavyEveryNth 的慢节奏走，并且内容没变就不写；进程退出前那次 flush 一定带上它。
	if heavy {
		if raw, err := json.Marshal(s.logs.export()); err == nil {
			_ = ps.saveKVIfChanged("logs", raw)
		}
	}
	// Playbook execution history is small (≤ 100 records) — persist every flush.
	if raw, err := json.Marshal(s.playbooks.exportExecutions()); err == nil {
		_ = ps.saveKVIfChanged("playbook_executions", raw)
	}
	if raw, err := json.Marshal(s.playbooks.exportLastRun()); err == nil {
		_ = ps.saveKVIfChanged("playbook_last_run", raw)
	}
	{
		last, hourly := s.remediation.ExportGuards()
		if raw, err := json.Marshal(map[string]any{"last_run": last, "hourly": hourly}); err == nil {
			_ = ps.saveKVIfChanged("remediation_guards", raw)
		}
	}
}

// ============================================================================
// Hardware / NetFlow PG methods
// ============================================================================

func (p *pgStore) upsertHardwareSnapshot(hostID string, snap shared.HardwareSnapshot) {
	raw, _ := json.Marshal(snap)
	_, err := p.db.Exec(`
		INSERT INTO hardware_snapshot(host_id, target_name, target_url, snapshot, health, updated_at)
		VALUES($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (host_id, target_name) DO UPDATE
		SET snapshot=$4, health=$5, target_url=$3, updated_at=NOW()`,
		hostID, snap.TargetName, snap.TargetURL, raw, snap.Health)
	if err != nil {
		slog.Warn("Upsert 硬件快照失败", "host", hostID, "target", snap.TargetName, "err", err)
	}
}

// getHardwareSnapshotDecoded returns the stored snapshot for one target,
// decoded back into the wire struct so it can be diffed against a fresh one.
func (p *pgStore) getHardwareSnapshotDecoded(hostID, targetName string) (shared.HardwareSnapshot, bool) {
	var raw []byte
	err := p.db.QueryRow(`SELECT snapshot FROM hardware_snapshot WHERE host_id=$1 AND target_name=$2`,
		hostID, targetName).Scan(&raw)
	if err != nil {
		return shared.HardwareSnapshot{}, false
	}
	var snap shared.HardwareSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return shared.HardwareSnapshot{}, false
	}
	return snap, true
}

func (p *pgStore) insertHardwareChange(hostID, targetName string, c hwChange) {
	_, err := p.db.Exec(`
		INSERT INTO hardware_changes(host_id, target_name, kind, component, action, old_value, new_value)
		VALUES($1,$2,$3,$4,$5,$6,$7)`,
		hostID, targetName, c.Kind, c.Component, c.Action, c.Old, c.New)
	if err != nil {
		slog.Warn("写入硬件变更记录失败", "host", hostID, "component", c.Component, "err", err)
	}
}

// getHardwareChanges returns asset change history, newest first.
func (p *pgStore) getHardwareChanges(hostID, target string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT target_name, kind, component, action, COALESCE(old_value,''), COALESCE(new_value,''), created_at
	      FROM hardware_changes WHERE host_id=$1`
	args := []any{hostID}
	argN := 2
	if target != "" {
		q += fmt.Sprintf(` AND target_name=$%d`, argN)
		args = append(args, target)
		argN++
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, argN)
	args = append(args, limit)

	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var tn, kind, comp, action, oldV, newV string
		var ts time.Time
		if err := rows.Scan(&tn, &kind, &comp, &action, &oldV, &newV, &ts); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"target_name": tn, "kind": kind, "component": comp, "action": action,
			"old_value": oldV, "new_value": newV, "created_at": ts,
		})
	}
	return out, rows.Err()
}

func (p *pgStore) insertHardwareEvent(hostID, targetName, eventType, severity, message string) {
	_, err := p.db.Exec(`
		INSERT INTO hardware_events(host_id, target_name, event_type, severity, message)
		VALUES($1, $2, $3, $4, $5)`,
		hostID, targetName, eventType, severity, message)
	if err != nil {
		slog.Warn("插入硬件事件失败", "err", err)
	}
}

// getHardwareEvents returns recorded hardware state transitions for a host,
// newest first. Optionally narrowed to one Redfish target.
func (p *pgStore) getHardwareEvents(hostID, target string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT target_name, event_type, severity, message, created_at
	      FROM hardware_events WHERE host_id=$1`
	args := []any{hostID}
	argN := 2
	if target != "" {
		q += fmt.Sprintf(` AND target_name=$%d`, argN)
		args = append(args, target)
		argN++
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, argN)
	args = append(args, limit)

	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var targetName, eventType, severity, message sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&targetName, &eventType, &severity, &message, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"target_name": targetName.String,
			"event_type":  eventType.String,
			"severity":    severity.String,
			"message":     message.String,
			"created_at":  createdAt,
		})
	}
	return out, rows.Err()
}

func (p *pgStore) getHardwareSnapshots(hostID string) ([]map[string]any, error) {
	rows, err := p.db.Query(`
		SELECT target_name, target_url, snapshot, health, updated_at
		FROM hardware_snapshot WHERE host_id=$1 ORDER BY updated_at DESC`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var targetName, targetURL, health string
		var snapshot json.RawMessage
		var updatedAt time.Time
		if err := rows.Scan(&targetName, &targetURL, &snapshot, &health, &updatedAt); err != nil {
			continue
		}
		var snapData any
		json.Unmarshal(snapshot, &snapData)
		results = append(results, map[string]any{
			"target_name": targetName,
			"target_url":  targetURL,
			"health":      health,
			"snapshot":    snapData,
			"updated_at":  updatedAt,
		})
	}
	if results == nil {
		results = []map[string]any{}
	}
	return results, rows.Err()
}

func (p *pgStore) deleteHardwareSnapshot(hostID, targetName string) {
	_, err := p.db.Exec(`DELETE FROM hardware_snapshot WHERE host_id=$1 AND target_name=$2`, hostID, targetName)
	if err != nil {
		slog.Warn("删除硬件快照失败", "host", hostID, "target", targetName, "err", err)
	}
	// 级联清理关联的事件与变更记录
	_, _ = p.db.Exec(`DELETE FROM hardware_events WHERE host_id=$1 AND target_name=$2`, hostID, targetName)
	_, _ = p.db.Exec(`DELETE FROM hardware_changes WHERE host_id=$1 AND target_name=$2`, hostID, targetName)
}

// findHardwareTargetByURL returns the target_name of an existing snapshot that
// matches the given target_url, or "" if none found. Used to detect renames:
// when a user changes the config.json "name" field for the same physical device
// (same URL), we need to migrate the old record instead of creating a new one.
func (p *pgStore) findHardwareTargetByURL(hostID, targetURL string) string {
	if targetURL == "" {
		return ""
	}
	var name string
	err := p.db.QueryRow(`SELECT target_name FROM hardware_snapshot WHERE host_id=$1 AND target_url=$2`,
		hostID, targetURL).Scan(&name)
	if err != nil {
		return ""
	}
	return name
}

// renameHardwareTarget migrates all data from oldName to newName for a given
// host, covering snapshots, events, and changes. Called when the agent's
// config.json "name" field is changed for the same physical device (matched
// by target_url). Without this migration the old record lingers forever.
func (p *pgStore) renameHardwareTarget(hostID, oldName, newName string) {
	if oldName == newName || oldName == "" || newName == "" {
		return
	}
	slog.Info("硬件目标改名迁移", "host", hostID, "old", oldName, "new", newName)
	// 1. Delete the new name if it already exists (will be re-inserted by upsert)
	_, _ = p.db.Exec(`DELETE FROM hardware_snapshot WHERE host_id=$1 AND target_name=$2`, hostID, newName)
	// 2. Rename old → new in snapshots (preserves history)
	_, _ = p.db.Exec(`UPDATE hardware_snapshot SET target_name=$3 WHERE host_id=$1 AND target_name=$2`,
		hostID, oldName, newName)
	// 3. Rename in events (state transitions timeline)
	_, _ = p.db.Exec(`UPDATE hardware_events SET target_name=$3 WHERE host_id=$1 AND target_name=$2`,
		hostID, oldName, newName)
	// 4. Rename in changes (asset change history)
	_, _ = p.db.Exec(`UPDATE hardware_changes SET target_name=$3 WHERE host_id=$1 AND target_name=$2`,
		hostID, oldName, newName)
}

// purgeOtherHardwareByURL deletes sibling snapshots that share the same
// target_url but a different target_name — cleans any historical rename orphans.
func (p *pgStore) purgeOtherHardwareByURL(hostID, keepName, targetURL string) {
	if targetURL == "" || keepName == "" {
		return
	}
	res, err := p.db.Exec(`
		DELETE FROM hardware_snapshot
		WHERE host_id=$1 AND target_url=$2 AND target_name<>$3`, hostID, targetURL, keepName)
	if err != nil {
		slog.Warn("清理硬件同 URL 重复行失败", "host", hostID, "url", targetURL, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("已清理硬件同 URL 旧名残留", "host", hostID, "url", targetURL, "keep", keepName, "removed", n)
	}
}

// ============================================================================
// Hyper-V 虚拟机清单 PG methods（结构与 hardware_* 同构）
// ============================================================================

func (p *pgStore) upsertContainerInventory(hostID, hostName, runtime string, containers []shared.ContainerInfo) {
	if containers == nil {
		containers = []shared.ContainerInfo{}
	}
	raw, _ := json.Marshal(containers)
	_, err := p.db.Exec(`
		INSERT INTO container_inventory(host_id, host_name, runtime, container_count, snapshot, updated_at)
		VALUES($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (host_id) DO UPDATE
		SET host_name=$2, runtime=$3, container_count=$4, snapshot=$5, updated_at=NOW()`,
		hostID, hostName, runtime, len(containers), raw)
	if err != nil {
		slog.Warn("Upsert 容器清单失败", "host", hostID, "err", err)
	}
}

func (p *pgStore) scanContainerRow(hostID, hostName, runtime string, snapshot json.RawMessage, count int, updatedAt time.Time) map[string]any {
	var containers any
	_ = json.Unmarshal(snapshot, &containers)
	if containers == nil {
		containers = []any{}
	}
	return map[string]any{
		"host_id":         hostID,
		"host_name":       hostName,
		"runtime":         runtime,
		"container_count": count,
		"containers":      containers,
		// Unix 秒：与主机指标等 API 一致；避免 time.Time 默认 RFC3339 字符串导致客户端 Long 解析失败。
		"updated_at": updatedAt.Unix(),
	}
}

func (p *pgStore) getContainerInventory(hostID string) (map[string]any, bool) {
	var hostName, runtime string
	var snapshot json.RawMessage
	var count int
	var updatedAt time.Time
	err := p.db.QueryRow(`SELECT host_name, COALESCE(runtime,''), container_count, snapshot, updated_at
		FROM container_inventory WHERE host_id=$1`, hostID).Scan(&hostName, &runtime, &count, &snapshot, &updatedAt)
	if err != nil {
		return nil, false
	}
	return p.scanContainerRow(hostID, hostName, runtime, snapshot, count, updatedAt), true
}

func (p *pgStore) getAllContainerInventories() ([]map[string]any, error) {
	rows, err := p.db.Query(`SELECT host_id, host_name, COALESCE(runtime,''), container_count, snapshot, updated_at
		FROM container_inventory ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var hostID, hostName, runtime string
		var count int
		var snapshot json.RawMessage
		var updatedAt time.Time
		if err := rows.Scan(&hostID, &hostName, &runtime, &count, &snapshot, &updatedAt); err != nil {
			continue
		}
		out = append(out, p.scanContainerRow(hostID, hostName, runtime, snapshot, count, updatedAt))
	}
	return out, rows.Err()
}

// upsertHyperVInventory overwrites a host's guest inventory (whole list as JSONB).
func (p *pgStore) upsertHyperVInventory(hostID, hostName string, guests []shared.HyperVGuest) {
	if guests == nil {
		guests = []shared.HyperVGuest{}
	}
	raw, _ := json.Marshal(guests)
	_, err := p.db.Exec(`
		INSERT INTO hyperv_inventory(host_id, host_name, guest_count, snapshot, updated_at)
		VALUES($1, $2, $3, $4, NOW())
		ON CONFLICT (host_id) DO UPDATE
		SET host_name=$2, guest_count=$3, snapshot=$4, updated_at=NOW()`,
		hostID, hostName, len(guests), raw)
	if err != nil {
		slog.Warn("Upsert Hyper-V 清单失败", "host", hostID, "err", err)
	}
}

// getHyperVInventoryDecoded returns a host's stored guests decoded back into wire
// structs, so a fresh report can be diffed against it for change detection.
func (p *pgStore) getHyperVInventoryDecoded(hostID string) ([]shared.HyperVGuest, bool) {
	var raw []byte
	err := p.db.QueryRow(`SELECT snapshot FROM hyperv_inventory WHERE host_id=$1`, hostID).Scan(&raw)
	if err != nil {
		return nil, false
	}
	var guests []shared.HyperVGuest
	if err := json.Unmarshal(raw, &guests); err != nil {
		return nil, false
	}
	return guests, true
}

// hypervInventoryRow is one host's inventory as returned to the frontend/AI.
func (p *pgStore) scanHyperVRow(hostID, hostName string, snapshot json.RawMessage, guestCount int, updatedAt time.Time) map[string]any {
	var guests any
	json.Unmarshal(snapshot, &guests)
	if guests == nil {
		guests = []any{}
	}
	return map[string]any{
		"host_id":     hostID,
		"host_name":   hostName,
		"guest_count": guestCount,
		"guests":      guests,
		"updated_at":  updatedAt,
	}
}

// getHyperVInventory returns one host's inventory (nil,false when none).
func (p *pgStore) getHyperVInventory(hostID string) (map[string]any, bool) {
	var hostName string
	var snapshot json.RawMessage
	var guestCount int
	var updatedAt time.Time
	err := p.db.QueryRow(`SELECT host_name, guest_count, snapshot, updated_at
		FROM hyperv_inventory WHERE host_id=$1`, hostID).Scan(&hostName, &guestCount, &snapshot, &updatedAt)
	if err != nil {
		return nil, false
	}
	return p.scanHyperVRow(hostID, hostName, snapshot, guestCount, updatedAt), true
}

// getAllHyperVInventories returns every host's inventory, most-recently-updated first.
func (p *pgStore) getAllHyperVInventories() ([]map[string]any, error) {
	rows, err := p.db.Query(`SELECT host_id, host_name, guest_count, snapshot, updated_at
		FROM hyperv_inventory ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var hostID, hostName string
		var guestCount int
		var snapshot json.RawMessage
		var updatedAt time.Time
		if err := rows.Scan(&hostID, &hostName, &guestCount, &snapshot, &updatedAt); err != nil {
			continue
		}
		out = append(out, p.scanHyperVRow(hostID, hostName, snapshot, guestCount, updatedAt))
	}
	return out, rows.Err()
}

const hypervEventsPerHostCap = 500

func (p *pgStore) insertHyperVEvent(hostID, vmName, vmID, kind, severity, message string) {
	_, err := p.db.Exec(`
		INSERT INTO hyperv_events(host_id, vm_name, vm_id, kind, severity, message)
		VALUES($1, $2, $3, $4, $5, $6)`,
		hostID, vmName, vmID, kind, severity, message)
	if err != nil {
		slog.Warn("插入 Hyper-V 事件失败", "host", hostID, "vm", vmName, "err", err)
		return
	}
	// 保留每宿主最近 N 条，防止事件表无界增长。事件只在 VM 增删/状态跳变时写入，
	// 频率很低，故随插入裁剪的开销可忽略。
	_, _ = p.db.Exec(`DELETE FROM hyperv_events WHERE host_id=$1 AND id NOT IN (
		SELECT id FROM hyperv_events WHERE host_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2)`,
		hostID, hypervEventsPerHostCap)
}

// getHyperVEvents returns a host's VM change/state events, newest first.
func (p *pgStore) getHyperVEvents(hostID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := p.db.Query(`SELECT vm_name, vm_id, kind, severity, message, created_at
		FROM hyperv_events WHERE host_id=$1 ORDER BY created_at DESC LIMIT $2`, hostID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var vmName, vmID, kind, severity, message sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&vmName, &vmID, &kind, &severity, &message, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"vm_name":    vmName.String,
			"vm_id":      vmID.String,
			"kind":       kind.String,
			"severity":   severity.String,
			"message":    message.String,
			"created_at": createdAt,
		})
	}
	return out, rows.Err()
}

func (p *pgStore) deleteHyperVInventory(hostID string) {
	if _, err := p.db.Exec(`DELETE FROM hyperv_inventory WHERE host_id=$1`, hostID); err != nil {
		slog.Warn("删除 Hyper-V 清单失败", "host", hostID, "err", err)
	}
	_, _ = p.db.Exec(`DELETE FROM hyperv_events WHERE host_id=$1`, hostID)
}

// insertFlowRecords 批量写入 Flow 明细：多行 VALUES 分批（每批 500 行），把上万条的逐行往返
// 压缩到几十次，大幅缩短占用连接的时长（原来逐行 Exec 会拿住一条连接做上万次往返，饿死连接池）。
func (p *pgStore) insertFlowRecords(hostID, source string, flows []shared.FlowRecord) {
	if len(flows) == 0 {
		return
	}
	const cols = 11
	const batch = 500
	base := `INSERT INTO flow_records(host_id, source, src_ip, dst_ip, src_port, dst_port, protocol, bytes, packets, first_seen, last_seen) VALUES `
	for start := 0; start < len(flows); start += batch {
		end := start + batch
		if end > len(flows) {
			end = len(flows)
		}
		chunk := flows[start:end]
		var sb strings.Builder
		sb.WriteString(base)
		args := make([]any, 0, len(chunk)*cols)
		for i, f := range chunk {
			if i > 0 {
				sb.WriteByte(',')
			}
			b := i * cols
			fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				b+1, b+2, b+3, b+4, b+5, b+6, b+7, b+8, b+9, b+10, b+11)
			args = append(args, hostID, source, f.SrcIP, f.DstIP, f.SrcPort, f.DstPort, f.Protocol,
				f.Bytes, f.Packets, time.Unix(f.FirstSeen, 0), time.Unix(f.LastSeen, 0))
		}
		if _, err := p.db.Exec(sb.String(), args...); err != nil {
			slog.Warn("批量写入 flow_records 失败", "host", hostID, "rows", len(chunk), "err", err)
			return
		}
	}
}

// flowSummaryDims whitelists the columns callers may GROUP BY.
// 直接把 dimension 拼进 SQL 是注入面，必须白名单。
var flowSummaryDims = map[string]string{
	"src_ip":   "src_ip::text",
	"dst_ip":   "dst_ip::text",
	"src_port": "src_port::text",
	"dst_port": "dst_port::text",
	"protocol": "protocol::text",
	"source":   "source",
}

// getFlowSummary returns Top-N traffic grouped by one dimension, from PG.
//
// 为什么不查 VM：VM 里现在只存**基数可控的聚合**（总量/对端 Top-N/服务端口 Top-N），
// 不再有 src_port 这类高基数 label —— 那是压垮时序库的东西。按任意维度做
// Top-N 聚合本来就是关系库的活，明细在 PG 里永久保留，查它才对。
func (p *pgStore) getFlowSummary(hostID, dimension string, from, to int64, limit int) ([]map[string]any, error) {
	col, ok := flowSummaryDims[dimension]
	if !ok {
		col = flowSummaryDims["dst_ip"]
		dimension = "dst_ip"
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := fmt.Sprintf(`
		SELECT %s AS k, SUM(bytes)::bigint AS b, SUM(packets)::bigint AS pk, COUNT(*)::bigint AS n
		FROM flow_records
		WHERE host_id=$1 AND created_at >= to_timestamp($2) AND created_at <= to_timestamp($3)
		GROUP BY 1 ORDER BY b DESC LIMIT %d`, col, limit)

	rows, err := p.db.Query(q, hostID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var k sql.NullString
		var b, pk, n int64
		if err := rows.Scan(&k, &b, &pk, &n); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"key": k.String, "bytes": b, "packets": pk, "flows": n,
		})
	}
	return out, rows.Err()
}

// getFlowIPHistory returns bucketed traffic curves and drill-down dimensions for
// one ranked source/destination IP. Aggregation stays in PostgreSQL because the
// high-cardinality flow identity is intentionally not written to VictoriaMetrics.
func (p *pgStore) getFlowIPHistory(hostID, dimension, ip string, from, to int64) (map[string]any, error) {
	if dimension != "src_ip" && dimension != "dst_ip" {
		dimension = "dst_ip"
	}
	if net.ParseIP(stripMask(ip)) == nil {
		return nil, fmt.Errorf("invalid IP")
	}
	ip = stripMask(ip)
	span := to - from
	bucket := int64(60)
	switch {
	case span > 14*86400:
		bucket = 3600
	case span > 7*86400:
		bucket = 1800
	case span > 48*3600:
		bucket = 600
	case span > 12*3600:
		bucket = 300
	}
	peerCol := "src_ip"
	if dimension == "src_ip" {
		peerCol = "dst_ip"
	}
	q := fmt.Sprintf(`
		SELECT (floor(extract(epoch FROM created_at)/$5)*$5)::bigint AS ts,
		       COALESCE(SUM(bytes),0)::bigint,
		       COALESCE(SUM(packets),0)::bigint,
		       COUNT(*)::bigint,
		       COUNT(DISTINCT %s)::bigint
		FROM flow_records
		WHERE host_id=$1 AND created_at >= to_timestamp($2) AND created_at <= to_timestamp($3)
		  AND %s = $4::inet
		GROUP BY 1 ORDER BY 1`, peerCol, dimension)
	rows, err := p.db.Query(q, hostID, from, to, ip, bucket)
	if err != nil {
		return nil, err
	}
	points := []map[string]any{}
	for rows.Next() {
		var ts, bytes, packets, flows, peers int64
		if err := rows.Scan(&ts, &bytes, &packets, &flows, &peers); err != nil {
			continue
		}
		avg := float64(0)
		if packets > 0 {
			avg = float64(bytes) / float64(packets)
		}
		points = append(points, map[string]any{
			"timestamp": ts, "bytes": bytes, "packets": packets,
			"flows": flows, "peers": peers, "avg_packet_bytes": avg,
		})
	}
	noteRowsErr("getFlowIPHistory", rows)
	if err := rows.Close(); err != nil {
		return nil, err
	}

	top := func(expr string, limit int) ([]map[string]any, error) {
		query := fmt.Sprintf(`
			SELECT COALESCE(%s::text,''), COALESCE(SUM(bytes),0)::bigint,
			       COALESCE(SUM(packets),0)::bigint, COUNT(*)::bigint
			FROM flow_records
			WHERE host_id=$1 AND created_at >= to_timestamp($2) AND created_at <= to_timestamp($3)
			  AND %s = $4::inet
			GROUP BY 1 ORDER BY 2 DESC LIMIT %d`, expr, dimension, limit)
		rs, err := p.db.Query(query, hostID, from, to, ip)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		out := []map[string]any{}
		for rs.Next() {
			var key string
			var bytes, packets, flows int64
			if err := rs.Scan(&key, &bytes, &packets, &flows); err == nil && key != "" {
				out = append(out, map[string]any{
					"key": stripMask(key), "bytes": bytes, "packets": packets, "flows": flows,
				})
			}
		}
		return out, rs.Err()
	}

	peers, err := top(peerCol, 12)
	if err != nil {
		return nil, err
	}
	protocols, err := top("protocol", 8)
	if err != nil {
		return nil, err
	}
	srcPorts, err := top("src_port", 10)
	if err != nil {
		return nil, err
	}
	dstPorts, err := top("dst_port", 10)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"points": points, "peers": peers, "protocols": protocols,
		"src_ports": srcPorts, "dst_ports": dstPorts, "bucket_sec": bucket,
	}, nil
}

// ipIsh 判断字符串是否是可用于 inet 比较的 IP 或 CIDR（否则不加该条件，避免 SQL 报错）。
func ipIsh(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

// protoToNum 把协议名/数字转为 IP 协议号（flow_records.protocol 存的是数字）。
func protoToNum(s string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tcp":
		return 6, true
	case "udp":
		return 17, true
	case "icmp":
		return 1, true
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n, true
	}
	return 0, false
}

func (p *pgStore) getFlowRecords(hostID, filter string, limit int) ([]map[string]any, error) {
	return p.getFlowRecordsRange(hostID, filter, limit, 0, 0)
}

// getFlowRecordsRange is getFlowRecords with an optional created_at window.
// from/to <= 0 preserves the historical all-time behavior used by AI tools.
func (p *pgStore) getFlowRecordsRange(hostID, filter string, limit int, from, to int64) ([]map[string]any, error) {
	query := `SELECT source, src_ip::text, dst_ip::text, src_port, dst_port, protocol, bytes, packets, first_seen, last_seen
		FROM flow_records WHERE host_id=$1`
	args := []any{hostID}
	argIdx := 2
	if from > 0 && to > from {
		query += fmt.Sprintf(` AND created_at >= to_timestamp($%d) AND created_at <= to_timestamp($%d)`, argIdx, argIdx+1)
		args = append(args, from, to)
		argIdx += 2
	}

	// 筛选：支持 src_ip:/dst_ip:（精确 IP 或 CIDR，用 inet 包含 <<=）、src_port:/dst_port:/port:、
	// proto:（tcp/udp/icmp 或数字）、以及无前缀裸值（IP/CIDR→源或目的；纯数字→端口源或目的）。
	// 关键修复：原来 IP 用 `::text =` 精确字符串比较，CIDR 与松散写法永远不匹配、且未识别的写法静默不加
	// WHERE（返回全量，"筛选无效"）。现改为 inet 包含 + 裸值/别名兜底 + 去空格。
	if f := strings.TrimSpace(filter); f != "" {
		col, val := "", f
		if i := strings.Index(f, ":"); i > 0 {
			col = strings.ToLower(strings.TrimSpace(f[:i]))
			val = strings.TrimSpace(f[i+1:])
		}
		// 列名白名单：column 始终来自下方 switch 的硬编码字面量，绝不来自用户输入（val 才是用户值，
		// 已全部走 $N 参数化）。这里再做一次白名单断言作为纵深防御，防止未来误传入非受控列名。
		allowedFlowCols := map[string]bool{"src_ip": true, "dst_ip": true, "src_port": true, "dst_port": true}
		ipCond := func(column string) {
			if allowedFlowCols[column] && ipIsh(val) {
				query += fmt.Sprintf(` AND %s <<= $%d::inet`, column, argIdx)
				args = append(args, val)
				argIdx++
			}
		}
		portCond := func(column string) {
			if !allowedFlowCols[column] {
				return
			}
			if n, err := strconv.Atoi(val); err == nil {
				query += fmt.Sprintf(` AND %s = $%d`, column, argIdx)
				args = append(args, n)
				argIdx++
			}
		}
		switch col {
		case "src_ip", "src":
			ipCond("src_ip")
		case "dst_ip", "dst":
			ipCond("dst_ip")
		case "src_port":
			portCond("src_port")
		case "dst_port":
			portCond("dst_port")
		case "port":
			if n, err := strconv.Atoi(val); err == nil {
				query += fmt.Sprintf(` AND (src_port = $%d OR dst_port = $%d)`, argIdx, argIdx)
				args = append(args, n)
				argIdx++
			}
		case "proto", "protocol":
			if n, ok := protoToNum(val); ok {
				query += fmt.Sprintf(` AND protocol = $%d`, argIdx)
				args = append(args, n)
				argIdx++
			}
		case "ip", "": // 无前缀裸值兜底
			if ipIsh(val) {
				query += fmt.Sprintf(` AND (src_ip <<= $%d::inet OR dst_ip <<= $%d::inet)`, argIdx, argIdx)
				args = append(args, val)
				argIdx++
			} else if n, err := strconv.Atoi(val); err == nil {
				query += fmt.Sprintf(` AND (src_port = $%d OR dst_port = $%d)`, argIdx, argIdx)
				args = append(args, n)
				argIdx++
			}
		}
	}

	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, argIdx)
	args = append(args, limit)

	rows, err := p.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var source, srcIP, dstIP string
		var srcPort, dstPort, protocol int
		var bytes, packets int64
		var firstSeen, lastSeen time.Time
		if err := rows.Scan(&source, &srcIP, &dstIP, &srcPort, &dstPort, &protocol,
			&bytes, &packets, &firstSeen, &lastSeen); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"source":     source,
			"src_ip":     srcIP,
			"dst_ip":     dstIP,
			"src_port":   srcPort,
			"dst_port":   dstPort,
			"protocol":   protocol,
			"bytes":      bytes,
			"packets":    packets,
			"first_seen": firstSeen,
			"last_seen":  lastSeen,
		})
	}
	if results == nil {
		results = []map[string]any{}
	}
	return results, rows.Err()
}

// getFlowHosts returns the host_ids that actually have flow records in the window,
// ranked by total bytes desc (packets as tiebreak so acct-off hosts with 0 bytes but
// real connections still rank). Powers the "流量页只列有流量的主机" filter —— GROUP BY
// host_id inherently excludes hosts with no traffic at all.
func (p *pgStore) getFlowHosts(from, to int64, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := p.db.Query(`
		SELECT host_id, SUM(bytes)::bigint AS b, SUM(packets)::bigint AS pk, COUNT(*)::bigint AS n
		FROM flow_records
		WHERE created_at >= to_timestamp($1) AND created_at <= to_timestamp($2)
		GROUP BY host_id
		ORDER BY b DESC, pk DESC LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var hid string
		var b, pk, n int64
		if err := rows.Scan(&hid, &b, &pk, &n); err != nil {
			continue
		}
		out = append(out, map[string]any{"host_id": hid, "bytes": b, "packets": pk, "flows": n})
	}
	return out, rows.Err()
}

// ============================================================================
// SNMP PG methods
// ============================================================================

// upsertSNMPSnapshot 按 (host_id, device_name) upsert 一台设备的最新快照。
func (p *pgStore) upsertSNMPSnapshot(hostID string, snap shared.SNMPSnapshot) {
	raw, _ := json.Marshal(snap)
	_, err := p.db.Exec(`
		INSERT INTO snmp_snapshot(host_id, device_name, device_ip, snapshot, reachable, updated_at)
		VALUES($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (host_id, device_name) DO UPDATE
		SET snapshot=$4, device_ip=$3, reachable=$5, updated_at=NOW()`,
		hostID, snap.TargetName, snap.TargetIP, raw, snap.Reachable)
	if err != nil {
		slog.Warn("Upsert SNMP 快照失败", "host", hostID, "device", snap.TargetName, "err", err)
	}
}

// findSNMPDeviceByIP returns the device_name of an existing snapshot that matches
// the given device_ip (most recently updated wins), or "" if none. Used to detect
// renames: config.json "name" changed but IP (the connection identity) is unchanged.
func (p *pgStore) findSNMPDeviceByIP(hostID, deviceIP string) string {
	if deviceIP == "" {
		return ""
	}
	var name string
	err := p.db.QueryRow(`
		SELECT device_name FROM snmp_snapshot
		WHERE host_id=$1 AND device_ip=$2
		ORDER BY updated_at DESC LIMIT 1`, hostID, deviceIP).Scan(&name)
	if err != nil {
		return ""
	}
	return name
}

// renameSNMPDevice migrates a device row from oldName to newName for one agent
// host. Mirrors renameHardwareTarget: without this, changing config "name" for the
// same IP leaves the old row forever and the UI shows duplicates.
func (p *pgStore) renameSNMPDevice(hostID, oldName, newName string) {
	if oldName == newName || oldName == "" || newName == "" {
		return
	}
	slog.Info("SNMP 设备改名迁移", "host", hostID, "old", oldName, "new", newName)
	_, _ = p.db.Exec(`DELETE FROM snmp_snapshot WHERE host_id=$1 AND device_name=$2`, hostID, newName)
	_, _ = p.db.Exec(`UPDATE snmp_snapshot SET device_name=$3 WHERE host_id=$1 AND device_name=$2`,
		hostID, oldName, newName)
}

// purgeOtherSNMPByIP deletes sibling rows that share the same device_ip but a
// different device_name. Cleans historical rename orphans in one shot after the
// canonical name has been upserted.
func (p *pgStore) purgeOtherSNMPByIP(hostID, keepName, deviceIP string) {
	if deviceIP == "" || keepName == "" {
		return
	}
	res, err := p.db.Exec(`
		DELETE FROM snmp_snapshot
		WHERE host_id=$1 AND device_ip=$2 AND device_name<>$3`, hostID, deviceIP, keepName)
	if err != nil {
		slog.Warn("清理 SNMP 同 IP 重复行失败", "host", hostID, "ip", deviceIP, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("已清理 SNMP 同 IP 旧名残留", "host", hostID, "ip", deviceIP, "keep", keepName, "removed", n)
	}
}

// deleteSNMPSnapshot removes one device's snapshot for an agent host.
func (p *pgStore) deleteSNMPSnapshot(hostID, deviceName string) {
	if hostID == "" || deviceName == "" {
		return
	}
	_, err := p.db.Exec(`DELETE FROM snmp_snapshot WHERE host_id=$1 AND device_name=$2`, hostID, deviceName)
	if err != nil {
		slog.Warn("删除 SNMP 快照失败", "host", hostID, "device", deviceName, "err", err)
	}
}

// getSNMPSnapshots 返回一台主机（agent）下所有被轮询设备的最新快照。
func (p *pgStore) getSNMPSnapshots(hostID string) ([]map[string]any, error) {
	rows, err := p.db.Query(`
		SELECT device_name, device_ip, snapshot, reachable, updated_at
		FROM snmp_snapshot WHERE host_id=$1 ORDER BY updated_at DESC`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []map[string]any{}
	for rows.Next() {
		var deviceName, deviceIP string
		var snapshot json.RawMessage
		var reachable bool
		var updatedAt time.Time
		if err := rows.Scan(&deviceName, &deviceIP, &snapshot, &reachable, &updatedAt); err != nil {
			continue
		}
		var snapData any
		json.Unmarshal(snapshot, &snapData)
		results = append(results, map[string]any{
			"device_name": deviceName,
			"device_ip":   deviceIP,
			"reachable":   reachable,
			"snapshot":    snapData,
			"updated_at":  updatedAt,
		})
	}
	return results, rows.Err()
}

// insertSNMPTrap 追加写一条 trap 事件。
func (p *pgStore) insertSNMPTrap(hostID string, ev shared.SNMPTrapEvent) {
	vb, _ := json.Marshal(ev.Varbinds)
	_, err := p.db.Exec(`
		INSERT INTO snmp_traps(host_id, source_ip, version, trap_oid, severity, uptime_sec, varbinds, received_at)
		VALUES($1,$2,$3,$4,$5,$6,$7, to_timestamp($8))`,
		hostID, ev.SourceIP, ev.Version, ev.TrapOID, ev.Severity, ev.UptimeSec, vb, ev.Timestamp)
	if err != nil {
		slog.Warn("写入 SNMP Trap 失败", "host", hostID, "trap_oid", ev.TrapOID, "err", err)
	}
}

// getSNMPTraps 返回一台主机最近的 trap 事件（倒序）。
func (p *pgStore) getSNMPTraps(hostID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := p.db.Query(`
		SELECT source_ip, version, trap_oid, severity, COALESCE(uptime_sec,0), varbinds, received_at
		FROM snmp_traps WHERE host_id=$1 ORDER BY received_at DESC LIMIT $2`, hostID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []map[string]any{}
	for rows.Next() {
		var sourceIP, version, trapOID, severity string
		var uptime float64
		var varbinds json.RawMessage
		var receivedAt time.Time
		if err := rows.Scan(&sourceIP, &version, &trapOID, &severity, &uptime, &varbinds, &receivedAt); err != nil {
			continue
		}
		var vbData any
		json.Unmarshal(varbinds, &vbData)
		results = append(results, map[string]any{
			"source_ip":   sourceIP,
			"version":     version,
			"trap_oid":    trapOID,
			"severity":    severity,
			"uptime_sec":  uptime,
			"varbinds":    vbData,
			"received_at": receivedAt,
		})
	}
	return results, rows.Err()
}

// getSNMPHosts returns the hosts (agents) that have SNMP network-device data —
// polled device snapshots and/or received traps — ranked by device count desc.
// Powers the "网络设备页只列有网络设备的主机" filter. UNION 让只有 trap 的主机也能被选到，
// 其 traps 才在该页可见；DISTINCT 收敛 trap 侧，避免全表计数。
func (p *pgStore) getSNMPHosts() ([]map[string]any, error) {
	rows, err := p.db.Query(`
		SELECT host_id, SUM(dev)::bigint AS devices, SUM(reach)::bigint AS reachable, SUM(trp)::bigint AS traps
		FROM (
			SELECT host_id, 1 AS dev, (CASE WHEN reachable THEN 1 ELSE 0 END) AS reach, 0 AS trp FROM snmp_snapshot
			UNION ALL
			SELECT DISTINCT host_id, 0 AS dev, 0 AS reach, 1 AS trp FROM snmp_traps
		) u
		GROUP BY host_id
		ORDER BY devices DESC, traps DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var hid string
		var devices, reachable, traps int64
		if err := rows.Scan(&hid, &devices, &reachable, &traps); err != nil {
			continue
		}
		out = append(out, map[string]any{"host_id": hid, "devices": devices, "reachable": reachable, "traps": traps})
	}
	return out, rows.Err()
}

// insertContentAudit 批量写入明文 HTTP 内容审计事件。
// insertContentAudit 批量写入。labels 与 evs 一一对应（敏感命中标签，逗号分隔；空=未命中）。
func (p *pgStore) insertContentAudit(hostID string, evs []shared.ContentAuditEvent, labels []string) {
	if len(evs) == 0 {
		return
	}
	tx, err := p.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO content_audit
		(host_id, src_ip, dst_ip, dst_port, protocol, method, host, path, ctype, body,
		 status, resp_ctype, resp_body, req_truncated, resp_truncated,
		 capture_backend, body_mode, req_bytes, resp_bytes, req_sha256, resp_sha256,
		 redaction_count, redaction_labels,
		 principal_id, application_id, event_id, request_id, trace_id, llm_provider, llm_model,
		 llm_operation, llm_stream, input_tokens, output_tokens, tool_calls, latency_ms,
		 policy_decision, risk_labels, sensitive, observed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
		       $21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40)
		ON CONFLICT DO NOTHING`)
	if err != nil {
		return
	}
	defer stmt.Close()
	for i, e := range evs {
		var sens string
		if i < len(labels) {
			sens = labels[i]
		}
		_, _ = stmt.Exec(hostID, e.SrcIP, e.DstIP, int(e.DstPort), e.Protocol, e.Method, e.Host, e.Path, e.CType, e.Body,
			e.Status, e.RespCType, e.RespBody, e.ReqTruncated, e.RespTruncated,
			e.CaptureBackend, e.BodyMode, e.ReqBytes, e.RespBytes, e.ReqSHA256, e.RespSHA256,
			e.RedactionCount, strings.Join(e.RedactionLabels, ","),
			e.PrincipalID, e.ApplicationID, e.EventID, e.RequestID, e.TraceID, e.LLMProvider, e.LLMModel,
			e.LLMOperation, e.LLMStream, e.InputTokens, e.OutputTokens, e.ToolCalls, e.LatencyMS,
			e.PolicyDecision, strings.Join(e.RiskLabels, ","), sens, time.Unix(e.Ts, 0))
	}
	_ = tx.Commit()
}

// getContentAudit 查询内容审计记录，最新在前。filter 支持网络、采集来源以及
// provider/model/principal/decision/risk 等结构化治理维度。
func (p *pgStore) getContentAudit(hostID, filter string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT src_ip, dst_ip, dst_port, COALESCE(protocol,''), method, host, path, ctype, body,
	             COALESCE(status,0), COALESCE(resp_ctype,''), COALESCE(resp_body,''),
	             COALESCE(req_truncated,false), COALESCE(resp_truncated,false),
	             COALESCE(capture_backend,''), COALESCE(body_mode,''),
	             COALESCE(req_bytes,0), COALESCE(resp_bytes,0),
	             COALESCE(req_sha256,''), COALESCE(resp_sha256,''),
	             COALESCE(redaction_count,0), COALESCE(redaction_labels,''),
	             COALESCE(principal_id,''), COALESCE(application_id,''), COALESCE(event_id,''),
	             COALESCE(request_id,''), COALESCE(trace_id,''),
	             COALESCE(llm_provider,''), COALESCE(llm_model,''), COALESCE(llm_operation,''),
	             COALESCE(llm_stream,false), COALESCE(input_tokens,0), COALESCE(output_tokens,0),
	             COALESCE(tool_calls,0), COALESCE(latency_ms,0),
	             COALESCE(policy_decision,''), COALESCE(risk_labels,''),
	             COALESCE(sensitive,''), observed_at
	      FROM content_audit WHERE host_id=$1`
	args := []any{hostID}
	idx := 2
	if filter != "" {
		if col, val, ok := strings.Cut(filter, ":"); ok && val != "" {
			switch col {
			case "src_ip", "dst_ip", "host", "method", "protocol":
				q += fmt.Sprintf(" AND %s = $%d", col, idx)
				args = append(args, val)
				idx++
			case "backend":
				q += fmt.Sprintf(" AND capture_backend = $%d", idx)
				args = append(args, val)
				idx++
			case "body_mode":
				q += fmt.Sprintf(" AND body_mode = $%d", idx)
				args = append(args, val)
				idx++
			case "provider":
				q += fmt.Sprintf(" AND llm_provider = $%d", idx)
				args = append(args, val)
				idx++
			case "model":
				q += fmt.Sprintf(" AND llm_model = $%d", idx)
				args = append(args, val)
				idx++
			case "principal":
				q += fmt.Sprintf(" AND principal_id = $%d", idx)
				args = append(args, val)
				idx++
			case "decision":
				q += fmt.Sprintf(" AND policy_decision = $%d", idx)
				args = append(args, val)
				idx++
			case "risk":
				q += fmt.Sprintf(" AND risk_labels ILIKE $%d", idx)
				args = append(args, "%"+val+"%")
				idx++
			case "kw": // body/resp_body/path/host 模糊匹配
				q += fmt.Sprintf(" AND (body ILIKE $%d OR resp_body ILIKE $%d OR path ILIKE $%d OR host ILIKE $%d)", idx, idx, idx, idx)
				args = append(args, "%"+val+"%")
				idx++
			case "sens": // 只看命中敏感的
				q += " AND sensitive <> '' AND sensitive IS NOT NULL"
			}
		}
	}
	q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", idx)
	args = append(args, limit)
	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var srcIP, dstIP, protocol, method, host, path, ctype, body, respCType, respBody, sensitive string
		var captureBackend, bodyMode, reqSHA256, respSHA256, redactionLabels string
		var principalID, applicationID, eventID, requestID, traceID, llmProvider, llmModel, llmOperation string
		var policyDecision, riskLabels string
		var dstPort, status, reqBytes, respBytes, redactionCount, inputTokens, outputTokens, toolCalls int
		var latencyMS int64
		var reqTrunc, respTrunc, llmStream bool
		var observedAt time.Time
		if err := rows.Scan(&srcIP, &dstIP, &dstPort, &protocol, &method, &host, &path, &ctype, &body,
			&status, &respCType, &respBody, &reqTrunc, &respTrunc,
			&captureBackend, &bodyMode, &reqBytes, &respBytes, &reqSHA256, &respSHA256,
			&redactionCount, &redactionLabels,
			&principalID, &applicationID, &eventID, &requestID, &traceID,
			&llmProvider, &llmModel, &llmOperation, &llmStream,
			&inputTokens, &outputTokens, &toolCalls, &latencyMS,
			&policyDecision, &riskLabels, &sensitive, &observedAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"src_ip": srcIP, "dst_ip": dstIP, "dst_port": dstPort, "protocol": protocol, "method": method,
			"host": host, "path": path, "ctype": ctype, "body": body,
			"status": status, "resp_ctype": respCType, "resp_body": respBody,
			"req_truncated": reqTrunc, "resp_truncated": respTrunc,
			"capture_backend": captureBackend, "body_mode": bodyMode,
			"req_bytes": reqBytes, "resp_bytes": respBytes,
			"req_sha256": reqSHA256, "resp_sha256": respSHA256,
			"redaction_count": redactionCount, "redaction_labels": redactionLabels,
			"principal_id": principalID, "application_id": applicationID, "event_id": eventID,
			"request_id": requestID, "trace_id": traceID,
			"llm_provider": llmProvider, "llm_model": llmModel, "llm_operation": llmOperation,
			"llm_stream": llmStream, "llm_input_tokens": inputTokens, "llm_output_tokens": outputTokens,
			"llm_tool_calls": toolCalls, "latency_ms": latencyMS,
			"policy_decision": policyDecision, "risk_labels": riskLabels,
			"sensitive": sensitive, "observed_at": observedAt,
		})
	}
	return out, rows.Err()
}

// getContentAuditHosts 返回有内容审计记录的主机，按最近记录降序 + 条数。供"只列有数据的主机"过滤。
func (p *pgStore) getContentAuditHosts() ([]map[string]any, error) {
	rows, err := p.db.Query(`
		SELECT host_id, COUNT(*)::bigint AS events, EXTRACT(EPOCH FROM MAX(created_at))::bigint AS last
		FROM content_audit
		GROUP BY host_id
		ORDER BY last DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var hid string
		var events, last int64
		if err := rows.Scan(&hid, &events, &last); err != nil {
			continue
		}
		out = append(out, map[string]any{"host_id": hid, "events": events, "last": last})
	}
	return out, rows.Err()
}

// cleanupContentAudit deletes content_audit rows older than retainDays.
func (p *pgStore) cleanupContentAudit(retainDays int) {
	if retainDays <= 0 {
		retainDays = 30
	}
	cut := time.Now().AddDate(0, 0, -retainDays)
	_, _ = p.db.Exec(`DELETE FROM content_audit WHERE created_at < $1`, cut)
}

// cleanupAuditAndEvents retains audit/events via monthly partition drops on *_p.
// Audit rows are never DELETE'd (append-only / hash-chain integrity).
func (p *pgStore) cleanupAuditAndEvents(retainDays int) {
	if retainDays <= 0 {
		retainDays = 180
	}
	months := retainDays/30 + 1
	if months < 2 {
		months = 2
	}
	for _, parent := range retainedPartitionParents() {
		p.cleanupOldTSPartitions(parent, months)
	}
	// Legacy events table only (audit legacy never deleted).
	cut := time.Now().AddDate(0, 0, -retainDays).Unix()
	_, _ = p.db.Exec(`DELETE FROM events WHERE ts > 0 AND ts < $1`, cut)
}

func retainedPartitionParents() []string {
	return []string{"events_p"}
}

// cleanupAlertHistory removes old alert_history rows when the table uses ts column.
func (p *pgStore) cleanupAlertHistory(retainDays int) {
	if retainDays <= 0 {
		retainDays = 90
	}
	cut := time.Now().AddDate(0, 0, -retainDays).Unix()
	// alert_history schema varies; try common shapes
	_, _ = p.db.Exec(`DELETE FROM alert_history WHERE ts > 0 AND ts < $1`, cut)
	_, _ = p.db.Exec(`DELETE FROM alert_history WHERE created_at < to_timestamp($1)`, cut)
}

// cleanupOldFlowPartitions drops monthly partitions older than retainMonths.
func (p *pgStore) cleanupOldFlowPartitions(retainMonths int) {
	if retainMonths <= 0 {
		retainMonths = 12
	}
	p.ensureFlowPartitions()
	cut := time.Now().AddDate(0, -retainMonths, 0)
	rows, err := p.db.Query(`
SELECT c.relname FROM pg_class c
JOIN pg_inherits i ON i.inhrelid = c.oid
JOIN pg_class p ON p.oid = i.inhparent
WHERE p.relname = 'flow_records' AND c.relkind = 'r'`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		// Partitions are named flow_records_YYYYMM (see ensureFlowPartitions).
		if !isSafeFlowPartitionName(name) {
			continue
		}
		var ym int
		if _, err := fmt.Sscanf(name, "flow_records_%d", &ym); err != nil || ym < 197001 {
			continue
		}
		y, m := ym/100, ym%100
		if m < 1 || m > 12 {
			continue
		}
		partStart := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
		if partStart.Before(cut) {
			_, _ = p.db.Exec(`DROP TABLE IF EXISTS ` + quoteIdent(name))
			slog.Info("dropped old flow partition", "table", name)
		}
	}
	noteRowsErr("cleanupOldFlowPartitions", rows)
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (p *pgStore) loadOnCallPages() ([]OnCallPage, error) {
	rows, err := p.db.Query(`SELECT data FROM oncall_pages ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OnCallPage
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return out, err
		}
		var page OnCallPage
		if json.Unmarshal(raw, &page) == nil {
			out = append(out, page)
		}
	}
	return out, rows.Err()
}

func (p *pgStore) saveOnCallPages(list []OnCallPage) error {
	const table = "oncall_pages"
	if p.wc.needsSeed(table) {
		ids, err := p.selectIDsText(`SELECT id::text FROM oncall_pages`)
		if err != nil {
			return err
		}
		p.wc.seed(table, ids)
	}
	live := make(map[string]bool, len(list))
	type pendingPage struct {
		page OnCallPage
		raw  []byte
	}
	var pending []pendingPage
	for _, page := range list {
		raw, _ := json.Marshal(page)
		id := fmt.Sprint(page.ID)
		live[id] = true
		if p.wc.isChanged(table+"/"+id, raw) {
			pending = append(pending, pendingPage{page: page, raw: raw})
		}
	}
	removed := p.wc.missingIDs(table, live)
	if len(pending) == 0 && len(removed) == 0 {
		return nil
	}

	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, pp := range pending {
		if _, err := tx.Exec(`INSERT INTO oncall_pages(id, incident_id, status, created_at, data) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT(id) DO UPDATE SET incident_id=EXCLUDED.incident_id, status=EXCLUDED.status,
				created_at=EXCLUDED.created_at, data=EXCLUDED.data`,
			pp.page.ID, pp.page.IncidentID, pp.page.Status, pp.page.CreatedAt, pp.raw); err != nil {
			return err
		}
	}
	for _, id := range removed {
		if _, err := tx.Exec(`DELETE FROM oncall_pages WHERE id::text=$1`, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		p.wc.invalidateTable(table)
		return err
	}
	for _, pp := range pending {
		p.wc.remember(table+"/"+fmt.Sprint(pp.page.ID), pp.raw)
	}
	for _, id := range removed {
		p.wc.forget(table + "/" + id)
	}
	p.wc.setIDs(table, live)
	return nil
}

func (p *pgStore) loadChangeRecords() ([]ChangeRecord, error) {
	rows, err := p.db.Query(`SELECT data FROM change_records ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangeRecord
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return out, err
		}
		var rec ChangeRecord
		if json.Unmarshal(raw, &rec) == nil {
			out = append(out, rec)
		}
	}
	return out, rows.Err()
}

func (p *pgStore) saveChangeRecords(list []ChangeRecord) error {
	const table = "change_records"
	if p.wc.needsSeed(table) {
		ids, err := p.selectIDsText(`SELECT id::text FROM change_records`)
		if err != nil {
			return err
		}
		p.wc.seed(table, ids)
	}
	live := make(map[string]bool, len(list))
	type pendingChange struct {
		rec ChangeRecord
		raw []byte
	}
	var pending []pendingChange
	for _, rec := range list {
		raw, _ := json.Marshal(rec)
		id := fmt.Sprint(rec.ID)
		live[id] = true
		if p.wc.isChanged(table+"/"+id, raw) {
			pending = append(pending, pendingChange{rec: rec, raw: raw})
		}
	}
	removed := p.wc.missingIDs(table, live)
	if len(pending) == 0 && len(removed) == 0 {
		return nil
	}

	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, pc := range pending {
		if _, err := tx.Exec(`INSERT INTO change_records(id, status, started_at, data) VALUES($1,$2,$3,$4)
			ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status, started_at=EXCLUDED.started_at, data=EXCLUDED.data`,
			pc.rec.ID, pc.rec.Status, pc.rec.StartedAt, pc.raw); err != nil {
			return err
		}
	}
	for _, id := range removed {
		if _, err := tx.Exec(`DELETE FROM change_records WHERE id::text=$1`, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		p.wc.invalidateTable(table)
		return err
	}
	for _, pc := range pending {
		p.wc.remember(table+"/"+fmt.Sprint(pc.rec.ID), pc.raw)
	}
	for _, id := range removed {
		p.wc.forget(table + "/" + id)
	}
	p.wc.setIDs(table, live)
	return nil
}

// cleanupFlowRecords deletes flow records older than 7 days (called periodically).
func (p *pgStore) cleanupFlowRecords() {
	// Flow 明细现在**永久保留**（分区表，归档靠 DROP/DETACH 某个月的分区）。
	// 这里只维护分区，不再删数据 —— 原先的 7 天 DELETE 与"永久存储"直接冲突。
	p.ensureFlowPartitions()
}
