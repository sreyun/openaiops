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

func TestForbiddenWriteBlocksMutatingSelectWrappers(t *testing.T) {
	cases := []string{
		"SELECT query_to_xml('DELETE FROM t', true, true, '')",
		"SELECT query_to_xml ('DROP TABLE t', true, true, '')",
		"SELECT pg_catalog.query_to_xml('DELETE FROM t',true,true,'')",
		"WITH x AS (SELECT query_to_xml('DELETE FROM u',true,true,'') AS v) SELECT * FROM x",
		"SELECT dblink_exec('dbname=postgres','DELETE FROM t')",
		"SELECT * FROM dblink('dbname=postgres','DELETE FROM t RETURNING 1') AS t(x int)",
		"SELECT setval('my_seq', 1)",
	}
	for _, sql := range cases {
		if !ForbiddenWrite(sql) {
			t.Fatalf("ForbiddenWrite must block mutating SELECT wrapper: %s", sql)
		}
	}
	if ForbiddenWrite("SELECT query_to_xml_report FROM reports") {
		t.Fatal("identifier containing query_to_xml must not be treated as a call")
	}
}
