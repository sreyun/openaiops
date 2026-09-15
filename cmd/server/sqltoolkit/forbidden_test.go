package sqltoolkit

import "testing"

func TestForbiddenWriteBlocksSleepAndCopy(t *testing.T) {
	if !ForbiddenWrite("SELECT pg_sleep(1)") {
		t.Fatal("pg_sleep must be forbidden")
	}
	if !ForbiddenWrite("COPY t TO STDOUT") {
		t.Fatal("COPY must be forbidden")
	}
	if !ForbiddenWrite("SELECT sleep(5)") {
		t.Fatal("sleep() must be forbidden")
	}
	if ForbiddenWrite("SELECT id FROM users WHERE id = 1") {
		t.Fatal("plain SELECT must be allowed")
	}
}

// PostgreSQL SELECT … INTO creates a table (CREATE TABLE AS). Dashboard /
// datasource panels historically gated with ForbiddenWrite only, which listed
// INTO OUTFILE/DUMPFILE but not bare INTO — so `… INTO steal LIMIT 1` ran as a
// "read-only" panel query. StrictReadOnly already rejected this shape.
func TestForbiddenWriteBlocksSelectInto(t *testing.T) {
	cases := []string{
		"SELECT * INTO new_table FROM users LIMIT 10",
		"SELECT * INTO limit_backup FROM users",
		"WITH x AS (SELECT * FROM users) SELECT * INTO steal FROM x LIMIT 1",
		"SELECT * INTO TEMPORARY steal FROM users LIMIT 5",
		"SELECT id INTO OUTFILE '/tmp/x' FROM users",
	}
	for _, sql := range cases {
		if !ForbiddenWrite(sql) {
			t.Fatalf("expected ForbiddenWrite to block %q", sql)
		}
	}
}
