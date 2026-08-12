package main

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestAppendAuditChainedCommitsStableTwinIdentity(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "audit-test-passphrase")
	t.Setenv("AIOPS_SECRET_KEY_ID", "current")
	t.Setenv("AIOPS_SECRET_KEYS_PREV", "")
	p, mock := mockPgStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(auditChainAdvisoryLock).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT content_hash, chain_seq FROM audit_log_p`).
		WillReturnRows(sqlmock.NewRows([]string{"content_hash", "chain_seq"}).AddRow("prev", int64(9)))
	mock.ExpectQuery(`(?s)INSERT INTO audit_log.*RETURNING id`).
		WithArgs(int64(1786284000), sqlmock.AnyArg(), sqlmock.AnyArg(), "prev", int64(10), int16(2), "current").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))
	mock.ExpectExec(`INSERT INTO audit_log_p`).
		WithArgs(int64(42), int64(1786284000), sqlmock.AnyArg(), sqlmock.AnyArg(), "prev", int64(10), int16(2), "current").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	seq, err := p.appendAuditChained(context.Background(), LogEntry{
		Timestamp: 1786284000,
		Kind:      "operation",
		Level:     "info",
		Actor:     "admin",
		Message:   "saved",
	})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 10 {
		t.Fatalf("sequence = %d, want 10", seq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendAuditChainedRollsBackWhenPartitionInsertFails(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "audit-test-passphrase")
	t.Setenv("AIOPS_SECRET_KEY_ID", "current")
	t.Setenv("AIOPS_SECRET_KEYS_PREV", "")
	p, mock := mockPgStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(auditChainAdvisoryLock).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT content_hash, chain_seq FROM audit_log_p`).
		WillReturnRows(sqlmock.NewRows([]string{"content_hash", "chain_seq"}).AddRow("prev", int64(9)))
	mock.ExpectQuery(`(?s)INSERT INTO audit_log.*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))
	wantErr := errors.New("partition unavailable")
	mock.ExpectExec(`INSERT INTO audit_log_p`).
		WillReturnError(wantErr)
	mock.ExpectRollback()

	seq, err := p.appendAuditChained(context.Background(), LogEntry{
		Timestamp: 1786284000,
		Kind:      "operation",
		Level:     "info",
		Actor:     "admin",
		Message:   "saved",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want partition failure", err)
	}
	if seq != 10 {
		t.Fatalf("sequence = %d, want attempted sequence 10", seq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAppendAuditChainedStartsEmptyChainAtOne(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "audit-test-passphrase")
	t.Setenv("AIOPS_SECRET_KEY_ID", "current")
	t.Setenv("AIOPS_SECRET_KEYS_PREV", "")
	p, mock := mockPgStore(t)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(auditChainAdvisoryLock).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT content_hash, chain_seq FROM audit_log_p`).
		WillReturnRows(sqlmock.NewRows([]string{"content_hash", "chain_seq"}))
	mock.ExpectQuery(`(?s)INSERT INTO audit_log.*RETURNING id`).
		WithArgs(int64(1786284000), sqlmock.AnyArg(), sqlmock.AnyArg(), "", int64(1), int16(2), "current").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(1)))
	mock.ExpectExec(`INSERT INTO audit_log_p`).
		WithArgs(int64(1), int64(1786284000), sqlmock.AnyArg(), sqlmock.AnyArg(), "", int64(1), int16(2), "current").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	seq, err := p.appendAuditChained(context.Background(), LogEntry{
		Timestamp: 1786284000,
		Kind:      "operation",
		Level:     "info",
		Actor:     "admin",
		Message:   "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatalf("sequence = %d, want 1", seq)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
