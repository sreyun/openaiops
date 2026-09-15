package main

import (
	"strings"
	"testing"

	"aiops-monitor/cmd/server/sqltoolkit"
)

// Dashboard / datasource / SREyun SQL panels historically gated with
// IsReadOnlyQuery+ForbiddenWrite. These helpers must share the workbench
// StrictReadOnly* gate before any connection is opened.

func TestPgQueryReadOnlyRejectsCTEDelete(t *testing.T) {
	q := "WITH x AS (DELETE FROM orders RETURNING *) SELECT * FROM x"
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
	_, _, err := mysqlQueryReadOnly(MySQLConnection{Host: "127.0.0.1", Port: 1, User: "u", Database: "d"}, q, 10)
	if err == nil {
		t.Fatal("expected CTE DELETE to be rejected before dial")
	}
	if !strings.Contains(err.Error(), "只读") && !strings.Contains(strings.ToLower(err.Error()), "mutating") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// SELECT … INTO new_table is CREATE TABLE AS under PostgreSQL. The auto LIMIT
// wrapper only applies when the text lacks the substring "limit", so
// `… INTO steal LIMIT 1` (and `INTO limit_backup`) skip the subquery wrap that
// would otherwise syntax-error — and execute as a mutating statement.
func TestPgQueryReadOnlyRejectsSelectInto(t *testing.T) {
	cases := []string{
		"SELECT * INTO new_table FROM users LIMIT 10",
		"SELECT * INTO limit_backup FROM users",
		"WITH x AS (SELECT 1 AS n) SELECT * INTO steal FROM x LIMIT 1",
	}
	for _, q := range cases {
		// Precondition: denylist and StrictReadOnly must agree this is mutating.
		if !sqltoolkit.ForbiddenWrite(q) {
			t.Fatalf("precondition: ForbiddenWrite must block %q", q)
		}
		if reason := sqltoolkit.StrictReadOnlyPostgres(q); reason == "" {
			t.Fatalf("precondition: StrictReadOnlyPostgres must reject %q", q)
		}
		_, _, err := pgQueryReadOnly(MySQLConnection{Host: "127.0.0.1", Port: 1, User: "u", Database: "d"}, q, 10)
		if err == nil {
			t.Fatalf("expected SELECT INTO to be rejected before dial: %q", q)
		}
		if !strings.Contains(err.Error(), "只读") && !strings.Contains(strings.ToLower(err.Error()), "into") {
			t.Fatalf("unexpected error for %q: %v", q, err)
		}
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
