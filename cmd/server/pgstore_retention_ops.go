package main

import (
	"fmt"
	"log/slog"
	"time"
)

// 运维/AI 历史表的保留策略。
//
// 这些表原先一条清理路径都没有：会话、Run 记录、剧本与自愈执行历史、SNMP Trap
// 全都只进不出。它们不像 hosts 那样反复 UPDATE（那是膨胀），而是**真实数据的
// 无界增长**——VACUUM FULL 对它们无能为力，只有按时间删除才会变小。这正是
// 「清理过了、库还在涨」的另一半原因：一半是膨胀没回收，一半是根本没人删。
//
// 归属划分：
//   - ai_tool_audit 是审计性质，跟随 AuditDays（默认 180 天），不单独放宽；
//   - 其余是可再生的运行历史，跟随 OpsHistoryDays（默认 90 天）；
//   - terminal_recordings 刻意不在此列——终端录像是合规审计证据，按设计永久保留，
//     要清理必须由管理员显式决定，不能被一条默认策略悄悄删掉。

// opsRetentionTable is one time-bounded operational history table.
type opsRetentionTable struct {
	Table string
	// Column 是时间列；Epoch 表示它是 BIGINT unix 秒而非 TIMESTAMPTZ。
	Column string
	Epoch  bool
}

// opsRetentionTables lists the history tables trimmed by OpsHistoryDays.
func opsRetentionTables() []opsRetentionTable {
	return []opsRetentionTable{
		{Table: "hermes_sessions", Column: "updated_at"},
		{Table: "ai_runs", Column: "created_at"},
		{Table: "playbook_executions", Column: "ts", Epoch: true},
		{Table: "remediation_runs", Column: "ts", Epoch: true},
		{Table: "snmp_traps", Column: "received_at"},
		{Table: "hardware_events", Column: "created_at"},
		{Table: "hyperv_events", Column: "created_at"},
	}
}

// cleanupOpsHistory trims the unbounded operational history tables.
//
// 每张表单独执行、单独计数：某张表不存在或列名对不上（旧库升级上来的形态差异）
// 只应跳过这一张，不能中断整轮清理。
func (p *pgStore) cleanupOpsHistory(retainDays int) {
	if p == nil || p.db == nil {
		return
	}
	if retainDays <= 0 {
		retainDays = 90
	}
	cutTime := time.Now().AddDate(0, 0, -retainDays)
	cutEpoch := cutTime.Unix()

	var totalDeleted int64
	for _, t := range opsRetentionTables() {
		// 表名与列名均来自本文件内的固定清单，不含外部输入。
		var stmt string
		var arg any
		if t.Epoch {
			stmt = fmt.Sprintf(`DELETE FROM %s WHERE %s > 0 AND %s < $1`, t.Table, t.Column, t.Column)
			arg = cutEpoch
		} else {
			stmt = fmt.Sprintf(`DELETE FROM %s WHERE %s < $1`, t.Table, t.Column)
			arg = cutTime
		}
		res, err := p.db.Exec(stmt, arg)
		if err != nil {
			slog.Debug("ops history cleanup skipped", "table", t.Table, "err", err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			totalDeleted += n
			slog.Info("已清理过期运行历史", "table", t.Table, "rows", n, "retain_days", retainDays)
		}
	}
	if totalDeleted > 0 {
		// DELETE 只把行标记为死元组，堆文件不会缩回给操作系统。这里主动 ANALYZE
		// 一次，让 -pg-report 的膨胀估算拿到最新的行数与行宽统计，从而如实反映
		// 「删掉了但空间还占着」，而不是继续报「无需回收」。
		p.analyzeOpsRetentionTables()
		slog.Info("运行历史清理完成；释放的空间需一次性回收才会归还文件系统",
			"rows", totalDeleted, "hint", "aiops-server -pg-report / -pg-reclaim")
	}
}

// analyzeOpsRetentionTables refreshes planner statistics after a bulk delete so
// the bloat estimate in -pg-report reflects reality instead of stale row counts.
func (p *pgStore) analyzeOpsRetentionTables() {
	for _, t := range opsRetentionTables() {
		if _, err := p.db.Exec(`ANALYZE ` + t.Table); err != nil {
			slog.Debug("analyze skipped", "table", t.Table, "err", err)
		}
	}
}

// cleanupAIToolAudit trims the AI tool audit trail. Audit-grade, so it follows
// AuditDays rather than the shorter operational window.
func (p *pgStore) cleanupAIToolAudit(retainDays int) {
	if p == nil || p.db == nil {
		return
	}
	if retainDays <= 0 {
		retainDays = 180
	}
	cut := time.Now().AddDate(0, 0, -retainDays).Unix()
	if res, err := p.db.Exec(`DELETE FROM ai_tool_audit WHERE ts > 0 AND ts < $1`, cut); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			slog.Info("已清理过期 AI 工具审计", "rows", n, "retain_days", retainDays)
		}
	}
}
