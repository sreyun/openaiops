package main

import (
	"strings"
	"testing"

	"aiops-monitor/cmd/server/sqltoolkit"
)

// Dashboard / datasource / SREyun SQL panels historically gated with
// IsReadOnlyQuery+ForbiddenWrite. ForbiddenWrite looks for space-padded
// " delete ", so CTE DML after "(" slipped through while workbench
// StrictReadOnly* already blocked it. These helpers must share that gate
// before any connection is opened.
func TestPgQueryReadOnlyRejectsCTEDelete(t *testing.T) {
	q := "WITH x AS (DELETE FROM orders RETURNING *) SELECT * FROM x"
	if sqltoolkit.ForbiddenWrite(q) {
		t.Fatal("precondition: ForbiddenWrite must miss (delete after '('); otherwise this test is obsolete")
	}
	_, _, err := pgQueryReadOnly(MySQLConnection{Host: "127.0.0.1", Port: 1, User: "u", Database: "d"}, q, 10)
	if err == nil {
		t.Fatal("expected CTE DELETE to be rejected before dial")
	}
	if !strings.Contains(err.Error(), "只读") && !strings.Contains(strings.ToLower(err.Error()), "mutating") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMysqlQueryReadOnlyRejectsCTEDelete(t *testing.T) {
	q := "WITH x AS (DELETE FROM orders RETURNING id) SELECT * FROM x"
	if sqltoolkit.ForbiddenWrite(q) {
		t.Fatal("precondition: ForbiddenWrite must miss (delete after '('); otherwise this test is obsolete")
	}
	_, _, err := mysqlQueryReadOnly(MySQLConnection{Host: "127.0.0.1", Port: 1, User: "u", Database: "d"}, q, 10)
	if err == nil {
		t.Fatal("expected CTE DELETE to be rejected before dial")
	}
	if !strings.Contains(err.Error(), "只读") && !strings.Contains(strings.ToLower(err.Error()), "mutating") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPgQueryReadOnlyStillAllowsPlainSelect(t *testing.T) {
	// Must fail on dial (no server), not on the read-only gate.
	_, _, err := pgQueryReadOnly(MySQLConnection{Host: "127.0.0.1", Port: 1, User: "u", Database: "d"},
		"SELECT 1 AS n", 10)
	if err == nil {
		t.Fatal("expected dial failure without a local postgres")
	}
	if strings.Contains(err.Error(), "只读") {
		t.Fatalf("plain SELECT must not be rejected as non-readonly: %v", err)
	}
}
