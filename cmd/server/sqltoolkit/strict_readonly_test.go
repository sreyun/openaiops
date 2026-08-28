package sqltoolkit

import "testing"

func TestStrictReadOnlyMySQLAllows(t *testing.T) {
	allowed := []string{
		"SELECT id, name FROM users WHERE id = 1",
		"select * from users",
		"SELECT * FROM a UNION SELECT * FROM b",
		"WITH recent AS (SELECT id FROM orders WHERE ts > 100) SELECT * FROM recent",
		"SHOW TABLES",
		"DESC users",
		"VALUES (1, 2), (3, 4)",
		"SELECT 1;",
	}
	for _, sql := range allowed {
		if reason := StrictReadOnlyMySQL(sql); reason != "" {
			t.Fatalf("should allow %q, got reason: %s", sql, reason)
		}
	}
}

func TestStrictReadOnlyMySQLRejects(t *testing.T) {
	rejected := []string{
		"INSERT INTO t (id) VALUES (1)",
		"UPDATE t SET a = 1",
		"DELETE FROM t",
		"DROP TABLE t",
		"SELECT * FROM users FOR UPDATE",
		"SELECT * FROM users LOCK IN SHARE MODE",
		"SELECT * FROM t INTO OUTFILE '/tmp/x'",
		"SELECT pg_sleep(10)",
		"SELECT sleep(5)",
		"SELECT BENCHMARK(1000000, SHA1('a'))",
		"SELECT GET_LOCK('x', 1)",
		"SELECT LOAD_FILE('/etc/passwd')",
		"SELECT load_file ('/etc/passwd')",
		"SELECT LOAD_FILE\t('/etc/passwd')",
		"SELECT sleep (5)",
		"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x",
		"SELECT 1; SELECT 2",
		"SET SESSION TRANSACTION READ ONLY",
	}
	for _, sql := range rejected {
		if reason := StrictReadOnlyMySQL(sql); reason == "" {
			t.Fatalf("should reject %q", sql)
		}
	}
}

func TestStrictReadOnlyPostgresAllows(t *testing.T) {
	allowed := []string{
		"SELECT id FROM users",
		"WITH recent AS (SELECT id FROM orders) SELECT * FROM recent",
		"VALUES (1, 2)",
		"SHOW search_path",
		"TABLE users",
		"EXPLAIN SELECT * FROM users",
	}
	for _, sql := range allowed {
		if reason := StrictReadOnlyPostgres(sql); reason != "" {
			t.Fatalf("should allow %q, got reason: %s", sql, reason)
		}
	}
}

func TestStrictReadOnlyPostgresRejects(t *testing.T) {
	rejected := []string{
		"INSERT INTO t (id) VALUES (1)",
		"UPDATE t SET a = 1",
		"DELETE FROM t",
		"SELECT * FROM users FOR UPDATE",
		"SELECT * FROM users FOR SHARE",
		"SELECT * INTO new_table FROM users",
		"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x",
		"SELECT pg_sleep(10)",
		"SELECT pg_sleep (10)",
		"SELECT pg_read_file('/etc/passwd')",
		"SELECT pg_read_binary_file('/etc/passwd')",
		"SELECT pg_ls_dir('/etc')",
		"SELECT pg_stat_file('/etc/passwd')",
		"SELECT set_config('statement_timeout', '1s', true)",
		"COPY t TO STDOUT",
		"SELECT lo_export(123, '/tmp/x')",
		"SELECT lo_export (123, '/tmp/x')",
		"SELECT 1; DROP TABLE t",
	}
	for _, sql := range rejected {
		if reason := StrictReadOnlyPostgres(sql); reason == "" {
			t.Fatalf("should reject %q", sql)
		}
	}
}
