package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"aiops-monitor/shared"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// mockPgStore builds a pgStore backed by sqlmock (no real PG, matching the
// repo's offline test convention).
func mockPgStore(t *testing.T) (*pgStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &pgStore{db: db}, mock
}

func TestWithPgTxCommit(t *testing.T) {
	p, mock := mockPgStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO ai_call_events`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := p.withPgTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO ai_call_events(...)`)
		return err
	})
	if err != nil {
		t.Fatalf("commit path: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestWithPgTxRollbackOnError(t *testing.T) {
	p, mock := mockPgStore(t)
	mock.ExpectBegin()
	mock.ExpectRollback()

	sentinel := errors.New("boom")
	err := p.withPgTx(context.Background(), func(tx *sql.Tx) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestWithPgTxNilDB(t *testing.T) {
	var p *pgStore
	if err := p.withPgTx(context.Background(), func(tx *sql.Tx) error { return nil }); err == nil {
		t.Fatal("expected error for nil pgStore")
	}
}

func TestBumpVersionSuccess(t *testing.T) {
	p, mock := mockPgStore(t)
	mock.ExpectExec(`UPDATE ai_runs SET version=version\+1 WHERE id=\$1 AND version=\$2`).
		WithArgs("run1", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := bumpVersion(context.Background(), p.db, "ai_runs", "id", "run1", 3)
	if err != nil {
		t.Fatalf("bump success: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestBumpVersionConflict(t *testing.T) {
	p, mock := mockPgStore(t)
	mock.ExpectExec(`UPDATE ai_runs SET version=version\+1 WHERE id=\$1 AND version=\$2`).
		WithArgs("run1", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := bumpVersion(context.Background(), p.db, "ai_runs", "id", "run1", 3)
	if !errors.Is(err, ErrPgConflict) {
		t.Fatalf("expected ErrPgConflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestAppendEventDualCommitsStableTwinIdentity(t *testing.T) {
	p, mock := mockPgStore(t)
	event := storedEvent{
		Event:  shared.Event{Timestamp: 1786284000, Level: "info", Source: "plugin", Message: "ready"},
		HostID: "host-1", Hostname: "node-1",
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO events.*RETURNING id`).
		WithArgs(int64(1786284000), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectExec(`INSERT INTO events_p\(id,ts,data\)`).
		WithArgs(int64(17), int64(1786284000), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := p.appendEventDual(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendEventDualRollsBackOnPartitionFailure(t *testing.T) {
	p, mock := mockPgStore(t)
	event := storedEvent{Event: shared.Event{Timestamp: 1786284000, Level: "info", Source: "plugin", Message: "ready"}}
	wantErr := errors.New("events partition failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO events.*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
	mock.ExpectExec(`INSERT INTO events_p\(id,ts,data\)`).WillReturnError(wantErr)
	mock.ExpectRollback()

	if err := p.appendEventDual(context.Background(), event); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want partition failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendAICallEventDualCommitsStableTwinIdentity(t *testing.T) {
	p, mock := mockPgStore(t)
	stat := aiCallStat{
		Ts: 1786284000, Task: "chat", Model: "model-a", Actor: "admin", LatencyMs: 12, OK: true,
		Error: "", MemHits: 1, SkillHits: 2, ReplyChars: 30, ApproxTokens: 10,
		PromptTokens: 7, CompletionTokens: 3, CostEstimate: 0.25,
		UsageSource: "exact", PromptVersion: "prompt-v1", RouteReason: "primary",
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO ai_call_events.*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(23)))
	mock.ExpectExec(`(?s)INSERT INTO ai_call_events_p\s*\(\s*id,`).
		WithArgs(
			int64(23), stat.Ts, stat.Task, stat.Model, stat.Actor, stat.LatencyMs, stat.OK, stat.Error,
			stat.MemHits, stat.SkillHits, stat.ReplyChars, stat.ApproxTokens,
			stat.PromptTokens, stat.CompletionTokens, stat.CostEstimate,
			stat.UsageSource, stat.PromptVersion, stat.RouteReason,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := p.appendAICallEventDual(context.Background(), stat); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendAICallEventDualRollsBackOnPartitionFailure(t *testing.T) {
	p, mock := mockPgStore(t)
	stat := aiCallStat{Ts: 1786284000, Task: "chat", Model: "model-a", OK: true}
	wantErr := errors.New("ai partition failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)INSERT INTO ai_call_events.*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(23)))
	mock.ExpectExec(`(?s)INSERT INTO ai_call_events_p\s*\(\s*id,`).WillReturnError(wantErr)
	mock.ExpectRollback()

	if err := p.appendAICallEventDual(context.Background(), stat); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want partition failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRecentAuditUsesTimestampAndStableIdentityOrder(t *testing.T) {
	p, mock := mockPgStore(t)
	mock.ExpectQuery(`SELECT data FROM \(SELECT id,ts,data FROM audit_log_p ORDER BY ts DESC, id DESC LIMIT \$1\) t ORDER BY ts ASC, id ASC`).
		WithArgs(2).
		WillReturnRows(sqlmock.NewRows([]string{"data"}).
			AddRow([]byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"one"}`)).
			AddRow([]byte(`{"timestamp":2,"kind":"operation","level":"info","actor":"admin","message":"two"}`)))

	got, err := p.loadRecentAudit(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Timestamp != 1 || got[1].Timestamp != 2 {
		t.Fatalf("events = %+v", got)
	}
}

func TestLoadRecentEventsUsesTimestampAndStableIdentityOrder(t *testing.T) {
	p, mock := mockPgStore(t)
	mock.ExpectQuery(`SELECT data FROM \(SELECT id,ts,data FROM events_p ORDER BY ts DESC, id DESC LIMIT \$1\) t ORDER BY ts ASC, id ASC`).
		WithArgs(2).
		WillReturnRows(sqlmock.NewRows([]string{"data"}).
			AddRow([]byte(`{"timestamp":1,"level":"info","source":"plugin","message":"one"}`)).
			AddRow([]byte(`{"timestamp":2,"level":"info","source":"plugin","message":"two"}`)))

	got, err := p.loadRecentEvents(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Timestamp != 1 || got[1].Timestamp != 2 {
		t.Fatalf("events = %+v", got)
	}
}
