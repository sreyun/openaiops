package main

import (
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestWriteApprovalPGIsSoleAuthority(t *testing.T) {
	p, mock := mockPgStore(t)
	h := newAIGovHub()
	h.pg = p

	exp := time.Now().Unix() + 600
	// Seed memory as if issueWriteApproval cached the token locally, but force
	// authorization to go through PG (sole authority when wired).
	h.approvals = map[string]writeApproval{
		"wap_test": {
			ID: "wap_test", Tool: "k8s_scale", ArgsHash: "hash1",
			Actor: "bob", CreatedAt: 1, ExpiresAt: exp,
		},
	}

	rows := sqlmock.NewRows([]string{"id", "tool", "args_hash", "actor", "created_at", "expires_at", "used"}).
		AddRow("wap_test", "k8s_scale", "hash1", "bob", int64(1), exp, false)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tool, args_hash").
		WithArgs("wap_test").WillReturnRows(rows)
	mock.ExpectExec("UPDATE ai_write_approvals").
		WithArgs("wap_test", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if !h.consumeWriteApproval("wap_test", "k8s_scale", "hash1") {
		t.Fatal("first PG-backed consume should succeed")
	}
	if _, ok := h.approvals["wap_test"]; ok {
		t.Fatal("memory cache must be cleared after PG consume")
	}

	// Second consume: PG reports already used → deny (even if memory were re-seeded).
	h.approvals["wap_test"] = writeApproval{
		ID: "wap_test", Tool: "k8s_scale", ArgsHash: "hash1",
		Actor: "bob", CreatedAt: 1, ExpiresAt: exp,
	}
	usedRows := sqlmock.NewRows([]string{"id", "tool", "args_hash", "actor", "created_at", "expires_at", "used"}).
		AddRow("wap_test", "k8s_scale", "hash1", "bob", int64(1), exp, true)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tool, args_hash").
		WithArgs("wap_test").WillReturnRows(usedRows)
	mock.ExpectCommit()

	if h.consumeWriteApproval("wap_test", "k8s_scale", "hash1") {
		t.Fatal("second consume must fail when PG marks used")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
