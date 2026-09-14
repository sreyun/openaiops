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

// MySQL /*!…*/ versioned comments are executable. Stripping them as ordinary
// block comments previously let SELECT-shaped writes past ForbiddenWrite (used
// by dashboard/datasource mysqlQueryReadOnly with no READ ONLY session).
func TestForbiddenWriteBlocksMySQLVersionedCommentWrites(t *testing.T) {
	cases := []string{
		`SELECT 1/*!50000 INTO OUTFILE '/tmp/x'*/`,
		`SELECT 'x' LIMIT 1/*!50000 INTO OUTFILE '/var/lib/mysql-files/exfil.csv'*/`,
		`SELECT * FROM t WHERE 1/*!50000 INTO DUMPFILE '/tmp/y'*/`,
		`SELECT /*!50000 SLEEP(5) */ 1`,
	}
	for _, sql := range cases {
		if !ForbiddenWrite(sql) {
			t.Fatalf("ForbiddenWrite must block MySQL versioned-comment write: %q", sql)
		}
	}
	// Ordinary (non-executable) hints stay comments and must not false-positive.
	if ForbiddenWrite("SELECT /* hint */ id FROM users") {
		t.Fatal("plain block comments must still be stripped")
	}
}
