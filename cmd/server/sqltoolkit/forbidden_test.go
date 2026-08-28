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

func TestForbiddenWriteBlocksSpacedAndFileFuncs(t *testing.T) {
	cases := []string{
		"SELECT load_file ('/etc/passwd')",
		"SELECT LOAD_FILE\t('/etc/passwd')",
		"SELECT pg_sleep (10)",
		"SELECT pg_read_file('/etc/passwd')",
		"SELECT pg_read_binary_file('/var/lib/postgresql/data/pg_hba.conf')",
		"SELECT pg_ls_dir('.')",
		"SELECT pg_stat_file('/etc/passwd')",
		"SELECT lo_export (1, '/tmp/x')",
	}
	for _, sql := range cases {
		if !ForbiddenWrite(sql) {
			t.Fatalf("ForbiddenWrite must reject %q", sql)
		}
	}
}
