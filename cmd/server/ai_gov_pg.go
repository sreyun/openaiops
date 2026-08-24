package main

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Wire PostgreSQL into aiGovHub for durable write-approvals + tool audit.
func (s *Server) wireAIGovPG() {
	if s == nil || s.aiGov == nil || s.pg == nil {
		return
	}
	s.aiGov.pg = s.pg
	prev := s.aiGov.onRecord
	s.aiGov.onRecord = func(e aiToolAuditEntry) {
		s.pg.insertAIToolAudit(e)
		if prev != nil {
			prev(e)
		}
	}
}

func (p *pgStore) upsertWriteApproval(a writeApproval) {
	if p == nil || p.db == nil || a.ID == "" {
		return
	}
	_, _ = p.db.Exec(`
INSERT INTO ai_write_approvals(id, tool, args_hash, actor, created_at, expires_at, used, used_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (id) DO UPDATE SET used=EXCLUDED.used, used_at=EXCLUDED.used_at`,
		a.ID, a.Tool, a.ArgsHash, a.Actor, a.CreatedAt, a.ExpiresAt, a.Used, a.UsedAt)
}

func (p *pgStore) consumeWriteApprovalPG(ctx context.Context, id, tool, argsHash string) bool {
	if p == nil || p.db == nil || id == "" {
		return false
	}
	now := time.Now().Unix()
	consumed := false
	err := p.withPgTx(ctx, func(tx *sql.Tx) error {
		var a writeApproval
		err := tx.QueryRowContext(ctx, `
SELECT id, tool, args_hash, actor, created_at, expires_at, used FROM ai_write_approvals WHERE id=$1 FOR UPDATE`, id).
			Scan(&a.ID, &a.Tool, &a.ArgsHash, &a.Actor, &a.CreatedAt, &a.ExpiresAt, &a.Used)
		if err != nil || a.Used {
			return nil
		}
		if a.ExpiresAt > 0 && now > a.ExpiresAt {
			_, _ = tx.ExecContext(ctx, `DELETE FROM ai_write_approvals WHERE id=$1`, id)
			return nil
		}
		if a.Tool != "" && tool != "" && !strings.EqualFold(a.Tool, tool) {
			return nil
		}
		if strings.TrimSpace(a.ArgsHash) == "" || strings.TrimSpace(argsHash) == "" || a.ArgsHash != argsHash {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ai_write_approvals SET used=TRUE, used_at=$2 WHERE id=$1`, id, now); err != nil {
			return err
		}
		consumed = true
		return nil
	})
	return err == nil && consumed
}

func (p *pgStore) insertAIToolAudit(e aiToolAuditEntry) {
	if p == nil || p.db == nil {
		return
	}
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().Unix()
	}
	_, _ = p.db.Exec(`
INSERT INTO ai_tool_audit(ts, actor, tool, action, host_id, approved, blocked, detail, incident_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		e.Timestamp, e.Actor, e.Tool, e.Action, e.HostID, e.Approved, e.Blocked, truncateRun(e.Detail, 2000), e.IncidentID)
}

func (p *pgStore) listAIToolAudit(limit int) []aiToolAuditEntry {
	if p == nil || p.db == nil {
		return nil
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := p.db.Query(`
SELECT ts, actor, tool, action, host_id, approved, blocked, detail, incident_id
FROM ai_tool_audit ORDER BY ts DESC LIMIT $1`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []aiToolAuditEntry
	for rows.Next() {
		var e aiToolAuditEntry
		if rows.Scan(&e.Timestamp, &e.Actor, &e.Tool, &e.Action, &e.HostID, &e.Approved, &e.Blocked, &e.Detail, &e.IncidentID) == nil {
			out = append(out, e)
		}
	}
	noteRowsErr("listAIToolAudit", rows)
	return out
}
