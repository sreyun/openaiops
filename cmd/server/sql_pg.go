package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aiops-monitor/cmd/server/sqltoolkit"

	_ "github.com/lib/pq"
)

func driverOf(c MySQLConnection) string {
	d := strings.ToLower(strings.TrimSpace(c.Driver))
	if d == "postgres" || d == "postgresql" || d == "pg" {
		return "postgres"
	}
	return "mysql"
}

func postgresDSN(c MySQLConnection) string {
	user := c.User
	if user == "" {
		user = "postgres"
	}
	port := c.Port
	if port <= 0 {
		port = 5432
	}
	db := c.Database
	if db == "" {
		db = "postgres"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, port),
		Path:   "/" + db,
	}
	q := url.Values{}
	ssl := strings.ToLower(strings.TrimSpace(c.TLS))
	switch ssl {
	case "true", "require":
		q.Set("sslmode", "require")
	case "verify-full", "verify-ca":
		q.Set("sslmode", ssl)
	case "skip-verify":
		// lib/pq has no verify-off; prefer is the closest non-failing default.
		q.Set("sslmode", "prefer")
	case "prefer", "preferred":
		q.Set("sslmode", "prefer")
	case "disable", "false", "":
		q.Set("sslmode", "disable")
	default:
		q.Set("sslmode", "disable")
	}
	q.Set("connect_timeout", "8")
	if extras := strings.TrimSpace(c.Params); extras != "" {
		if ev, err := url.ParseQuery(extras); err == nil {
			for k, vs := range ev {
				for _, v := range vs {
					q.Set(k, v)
				}
			}
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func pgOpen(c MySQLConnection) (*sql.DB, error) {
	db, err := sql.Open("postgres", postgresDSN(c))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(2 * time.Minute)
	return db, nil
}

func pgPing(c MySQLConnection) (string, error) {
	db, err := pgOpen(c)
	if err != nil {
		return "", err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var ver string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&ver); err != nil {
		return "", err
	}
	return truncateRun(ver, 200), nil
}

// pgPeelExplain returns the inner statement for EXPLAIN, preserving original casing.
func pgPeelExplain(sqlText string) (inner string, err error) {
	sqlText = strings.TrimSpace(sqlText)
	sqlText = strings.TrimSuffix(sqlText, ";")
	stripped := strings.TrimSpace(sqltoolkit.StripCommentsAndStrings(sqlText))
	low := strings.ToLower(stripped)
	if !strings.HasPrefix(low, "explain") {
		return stripped, nil
	}
	restStripped := strings.TrimSpace(stripped[len("explain"):])
	restLow := strings.ToLower(strings.TrimSpace(sqltoolkit.StripCommentsAndStrings(restStripped)))
	if strings.HasPrefix(restLow, "analyze") {
		return "", fmt.Errorf("禁止 EXPLAIN ANALYZE（可能执行语句）")
	}
	if strings.HasPrefix(restLow, "(") {
		end := strings.Index(restStripped, ")")
		if end < 0 {
			return "", fmt.Errorf("EXPLAIN 选项括号不匹配")
		}
		opts := strings.ToLower(restStripped[1:end])
		if strings.Contains(opts, "analyze") {
			return "", fmt.Errorf("禁止 EXPLAIN ANALYZE（可能执行语句）")
		}
		restStripped = strings.TrimSpace(restStripped[end+1:])
	}
	return strings.TrimSpace(restStripped), nil
}

func pgExplain(c MySQLConnection, sqlText string) (map[string]any, error) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return nil, fmt.Errorf("sql required")
	}
	if strings.Count(sqlText, ";") > 1 || (strings.Contains(sqlText, ";") && !strings.HasSuffix(strings.TrimSpace(sqlText), ";")) {
		return nil, fmt.Errorf("仅允许单条语句")
	}
	sqlText = strings.TrimSuffix(strings.TrimSpace(sqlText), ";")
	if sqltoolkit.ForbiddenWrite(sqlText) {
		return nil, fmt.Errorf("禁止写操作")
	}
	inner, err := pgPeelExplain(sqlText)
	if err != nil {
		return nil, err
	}
	kw := sqltoolkit.FirstKeyword(inner)
	if kw != "select" && kw != "with" && kw != "values" {
		return nil, fmt.Errorf("PostgreSQL 只读：仅允许对 SELECT/WITH 做 EXPLAIN")
	}
	if sqltoolkit.ForbiddenWrite(inner) {
		return nil, fmt.Errorf("禁止写操作")
	}
	prepared, prepNotes := sqltoolkit.PrepareSQLForExplain(inner, sqltoolkit.DialectPostgres)
	if prepared != "" {
		inner = prepared
	}
	attachPrep := func(err error) (map[string]any, error) {
		body := map[string]any{}
		if len(prepNotes) > 0 {
			body["prepare_notes"] = prepNotes
		}
		if prepared != "" {
			body["prepared_sql"] = prepared
		}
		if err != nil {
			body["error"] = err.Error()
		}
		return body, err
	}

	db, err := pgOpen(c)
	if err != nil {
		return attachPrep(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Prefer JSON for structured analysis; also keep TEXT for operators.
	jsonSQL := "EXPLAIN (FORMAT JSON) " + inner
	var rawJSON string
	if err := db.QueryRowContext(ctx, jsonSQL).Scan(&rawJSON); err != nil {
		return attachPrep(err)
	}
	analysis := analyzePGExplainJSON(rawJSON)
	var parsed any
	_ = json.Unmarshal([]byte(rawJSON), &parsed)

	textSQL := "EXPLAIN (FORMAT TEXT) " + inner
	rs, err := db.QueryContext(ctx, textSQL)
	planText := ""
	if err == nil {
		defer rs.Close()
		var lines []string
		for rs.Next() {
			var line string
			if rs.Scan(&line) == nil {
				lines = append(lines, line)
			}
		}
		noteRowsErr("pgExplain", rs)
		planText = strings.Join(lines, "\n")
	}

	out := map[string]any{
		"driver":       "postgres",
		"plan":         planText,
		"raw":          rawJSON,
		"explain_json": parsed,
		"analysis":     analysis,
		"readonly":     true,
	}
	if len(prepNotes) > 0 {
		out["prepare_notes"] = prepNotes
		out["prepared_sql"] = inner
	}
	attachExplainAdvice(out, c, strings.TrimSpace(c.Database), inner, analysis)
	return out, nil
}

func analyzePGExplainJSON(raw string) *sqltoolkit.ExplainAnalysis {
	out := &sqltoolkit.ExplainAnalysis{TableAccess: []sqltoolkit.ExplainHit{}}
	var root any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		out.Summary = "无法解析 EXPLAIN JSON"
		return out
	}
	// PG returns [{ "Plan": { ... } }]
	var planNode map[string]any
	switch v := root.(type) {
	case []any:
		if len(v) > 0 {
			if m, ok := v[0].(map[string]any); ok {
				planNode, _ = m["Plan"].(map[string]any)
			}
		}
	case map[string]any:
		planNode, _ = v["Plan"].(map[string]any)
	}
	if planNode == nil {
		out.Summary = "EXPLAIN JSON 无 Plan 节点"
		return out
	}
	walkPGPlan(planNode, &out.TableAccess)
	for _, h := range out.TableAccess {
		if h.FullScanRisk {
			out.FullScans++
		} else if h.Key != "" || strings.Contains(strings.ToLower(h.AccessType), "index") {
			out.IndexHits++
		}
	}
	out.Summary = fmt.Sprintf("PostgreSQL 计划：%d 个扫描节点，全表/Seq Scan %d，索引相关 %d",
		len(out.TableAccess), out.FullScans, out.IndexHits)
	return out
}

func walkPGPlan(node map[string]any, hits *[]sqltoolkit.ExplainHit) {
	if node == nil {
		return
	}
	nodeType, _ := node["Node Type"].(string)
	rel, _ := node["Relation Name"].(string)
	schema, _ := node["Schema"].(string)
	indexName, _ := node["Index Name"].(string)
	alias, _ := node["Alias"].(string)
	table := rel
	if schema != "" && rel != "" {
		table = schema + "." + rel
	} else if table == "" {
		table = alias
	}
	rows := float64(0)
	switch r := node["Plan Rows"].(type) {
	case float64:
		rows = r
	case json.Number:
		rows, _ = r.Float64()
	}
	nt := strings.ToLower(nodeType)
	full := strings.Contains(nt, "seq scan") || nt == "sequential scan"
	hit := sqltoolkit.ExplainHit{
		Table:        table,
		AccessType:   nodeType,
		Key:          indexName,
		Rows:         rows,
		FullScanRisk: full && table != "",
		UsingIndex:   indexName != "" || strings.Contains(nt, "index"),
	}
	if filt, ok := node["Filter"].(string); ok && filt != "" {
		hit.Condition = filt
	}
	if hit.Table != "" || hit.AccessType != "" {
		*hits = append(*hits, hit)
	}
	if kids, ok := node["Plans"].([]any); ok {
		for _, k := range kids {
			if m, ok := k.(map[string]any); ok {
				walkPGPlan(m, hits)
			}
		}
	}
}

func pgCollectProcesslist(c MySQLConnection) ([]SQLProcessRow, error) {
	db, err := pgOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	q := `
SELECT pid, COALESCE(usename,''), COALESCE(client_addr::text,''), COALESCE(datname,''),
       COALESCE(state,''), COALESCE(EXTRACT(EPOCH FROM (now()-query_start))::bigint,0),
       COALESCE(wait_event_type,''), COALESCE(LEFT(query,500),'')
FROM pg_stat_activity
WHERE pid <> pg_backend_pid()
ORDER BY query_start NULLS LAST
LIMIT 200`
	rs, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := make([]SQLProcessRow, 0, 32)
	for rs.Next() {
		var row SQLProcessRow
		var waitEv string
		var state string
		if err := rs.Scan(&row.ID, &row.User, &row.Host, &row.DB, &state, &row.TimeSec, &waitEv, &row.Info); err != nil {
			continue
		}
		// Align with UI: Command ≈ wait_event_type / activity, State ≈ backend state.
		row.Command = waitEv
		if row.Command == "" {
			row.Command = state
		}
		row.State = state
		out = append(out, row)
	}
	return out, rs.Err()
}

func pgCollectLocks(c MySQLConnection) ([]SQLLockRow, error) {
	db, err := pgOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	q := `
SELECT
  COALESCE(blocked.pid::text,''), COALESCE(blocked.pid,0), COALESCE(LEFT(blocked.query,400),''),
  COALESCE(blocking.pid::text,''), COALESCE(blocking.pid,0), COALESCE(LEFT(blocking.query,400),''),
  COALESCE(blocked.wait_event,''), COALESCE(a.locktype,''), COALESCE(a.mode,'')
FROM pg_catalog.pg_locks a
JOIN pg_stat_activity blocked ON blocked.pid = a.pid
JOIN pg_catalog.pg_locks b ON a.locktype = b.locktype AND a.database IS NOT DISTINCT FROM b.database
  AND a.relation IS NOT DISTINCT FROM b.relation AND a.page IS NOT DISTINCT FROM b.page
  AND a.tuple IS NOT DISTINCT FROM b.tuple AND a.virtualxid IS NOT DISTINCT FROM b.virtualxid
  AND a.transactionid IS NOT DISTINCT FROM b.transactionid AND a.classid IS NOT DISTINCT FROM b.classid
  AND a.objid IS NOT DISTINCT FROM b.objid AND a.objsubid IS NOT DISTINCT FROM b.objsubid
  AND a.pid <> b.pid
JOIN pg_stat_activity blocking ON blocking.pid = b.pid
WHERE NOT a.granted AND b.granted
LIMIT 100`
	rs, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := make([]SQLLockRow, 0, 16)
	for rs.Next() {
		var row SQLLockRow
		if err := rs.Scan(&row.WaitingTrxID, &row.WaitingPID, &row.WaitingQuery,
			&row.BlockingTrxID, &row.BlockingPID, &row.BlockingQuery,
			&row.WaitStarted, &row.LockType, &row.LockMode); err != nil {
			continue
		}
		out = append(out, row)
	}
	return out, rs.Err()
}

func pgKillSession(c MySQLConnection, pid int64) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid")
	}
	db, err := pgOpen(c)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var ok bool
	if err := db.QueryRowContext(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("pg_terminate_backend returned false（会话可能已结束）")
	}
	return nil
}

// pgQueryReadOnly runs a single SELECT/WITH/VALUES and returns columns + row maps.
func pgQueryReadOnly(c MySQLConnection, sqlText string, limit int) (cols []string, rows []map[string]any, err error) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return nil, nil, fmt.Errorf("sql required")
	}
	if !sqltoolkit.IsReadOnlyQuery(sqlText) {
		return nil, nil, fmt.Errorf("仅允许单条只读 SELECT/WITH")
	}
	if sqltoolkit.ForbiddenWrite(sqlText) {
		return nil, nil, fmt.Errorf("禁止写操作")
	}
	kw := sqltoolkit.FirstKeyword(sqlText)
	if kw != "select" && kw != "with" && kw != "values" && kw != "show" && kw != "table" {
		return nil, nil, fmt.Errorf("PostgreSQL 数据源仅允许 SELECT/WITH/VALUES")
	}
	limit = clampSQLReadLimit(limit)
	// Cap result size without rewriting user LIMIT when present.
	// limit is a bounded int (not user text) — safe to embed as a literal.
	execSQL := strings.TrimSuffix(sqlText, ";")
	if !strings.Contains(strings.ToLower(sqltoolkit.StripCommentsAndStrings(execSQL)), "limit") {
		execSQL = fmt.Sprintf("SELECT * FROM (%s) AS _aiops_q LIMIT %d", execSQL, limit)
	}

	db, err := pgOpen(c)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rs, err := db.QueryContext(ctx, execSQL)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	cols, err = rs.Columns()
	if err != nil {
		return nil, nil, err
	}
	for rs.Next() {
		if len(rows) >= limit {
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			continue
		}
		m := make(map[string]any, len(cols))
		for i, col := range cols {
			m[col] = normalizeSQLValue(vals[i])
		}
		rows = append(rows, m)
	}
	return cols, rows, rs.Err()
}

func normalizeSQLValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339Nano)
	default:
		return t
	}
}

func pgSchema(c MySQLConnection, schema, table string) (map[string]any, error) {
	db, err := pgOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	schema = strings.TrimSpace(schema)
	table = strings.TrimSpace(table)

	if schema == "" && table == "" {
		rs, err := db.QueryContext(ctx, `
SELECT nspname FROM pg_namespace
WHERE nspname NOT IN ('pg_catalog','information_schema','pg_toast')
  AND nspname NOT LIKE 'pg_temp_%' AND nspname NOT LIKE 'pg_toast_temp_%'
ORDER BY nspname`)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		dbs := []string{}
		for rs.Next() {
			var name string
			if rs.Scan(&name) == nil {
				dbs = append(dbs, name)
			}
		}
		noteRowsErr("pgSchema#1", rs)
		return map[string]any{"driver": "postgres", "databases": dbs, "schemas": dbs}, nil
	}

	if table == "" {
		if !reSafeIdent.MatchString(schema) {
			return nil, fmt.Errorf("非法 schema 名")
		}
		rs, err := db.QueryContext(ctx, `
SELECT c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m')
ORDER BY c.relname`, schema)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		tables := []string{}
		for rs.Next() {
			var name string
			if rs.Scan(&name) == nil {
				tables = append(tables, name)
			}
		}
		noteRowsErr("pgSchema#2", rs)
		return map[string]any{"driver": "postgres", "database": schema, "schema": schema, "tables": tables}, nil
	}

	if !reSafeIdent.MatchString(schema) || !reSafeIdent.MatchString(table) {
		return nil, fmt.Errorf("非法标识符")
	}
	rs, err := db.QueryContext(ctx, `
SELECT a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod),
       NOT a.attnotnull, COALESCE(pg_get_expr(ad.adbin, ad.adrelid),''),
       COALESCE(col_description(a.attrelid, a.attnum),'')
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	columns := []map[string]any{}
	for rs.Next() {
		var name, typ, def, comment string
		var nullable bool
		if rs.Scan(&name, &typ, &nullable, &def, &comment) != nil {
			continue
		}
		columns = append(columns, map[string]any{
			"Field": name, "Type": typ, "Null": map[bool]string{true: "YES", false: "NO"}[nullable],
			"Default": def, "Comment": comment,
		})
	}
	noteRowsErr("pgSchema#3", rs)
	idxRows, err := db.QueryContext(ctx, `
SELECT i.relname, ix.indisunique, array_to_string(array_agg(a.attname ORDER BY x.n), ',')
FROM pg_index ix
JOIN pg_class t ON t.oid = ix.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS x(attnum, n) ON true
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = x.attnum
WHERE n.nspname = $1 AND t.relname = $2
GROUP BY i.relname, ix.indisunique
ORDER BY i.relname`, schema, table)
	indexes := []map[string]any{}
	if err == nil {
		defer idxRows.Close()
		for idxRows.Next() {
			var name, cols string
			var unique bool
			if idxRows.Scan(&name, &unique, &cols) == nil {
				indexes = append(indexes, map[string]any{"Key_name": name, "Non_unique": map[bool]int{true: 0, false: 1}[unique], "Columns": cols})
			}
		}
		noteRowsErr("pgSchema#4", idxRows)
	}
	return map[string]any{
		"driver": "postgres", "database": schema, "schema": schema, "table": table,
		"columns": columns, "indexes": indexes,
	}, nil
}

func pgSchemaHealth(c MySQLConnection) ([]SchemaHealthFinding, error) {
	db, err := pgOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out []SchemaHealthFinding

	// Tables without primary key
	rs, err := db.QueryContext(ctx, `
SELECT n.nspname, c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r' AND n.nspname NOT IN ('pg_catalog','information_schema')
  AND NOT EXISTS (
    SELECT 1 FROM pg_constraint con
    WHERE con.conrelid = c.oid AND con.contype = 'p'
  )
LIMIT 50`)
	if err == nil {
		for rs.Next() {
			var schema, table string
			if rs.Scan(&schema, &table) == nil {
				out = append(out, SchemaHealthFinding{
					Level: "medium", Code: "no_pk", Schema: schema, Table: table,
					Title: "表缺少主键", Detail: schema + "." + table,
					Suggest: "为业务表补充 PRIMARY KEY，便于逻辑复制与在线变更",
				})
			}
		}
		noteRowsErr("pgSchemaHealth#1", rs)
		rs.Close()
	}

	// Large tables with no indexes (approx via reltuples / relpages)
	rs2, err := db.QueryContext(ctx, `
SELECT n.nspname, c.relname, COALESCE(c.reltuples,0)::bigint
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r' AND n.nspname NOT IN ('pg_catalog','information_schema')
  AND COALESCE(c.reltuples,0) >= 100000
  AND NOT EXISTS (
    SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND NOT i.indisprimary
  )
ORDER BY c.reltuples DESC
LIMIT 30`)
	if err == nil {
		for rs2.Next() {
			var schema, table string
			var rowsEst int64
			if rs2.Scan(&schema, &table, &rowsEst) == nil {
				out = append(out, SchemaHealthFinding{
					Level: "high", Code: "large_no_index", Schema: schema, Table: table,
					Title: "大表几乎无二级索引", Detail: fmt.Sprintf("%s.%s ≈ %d rows", schema, table, rowsEst),
					Suggest: "检查高频过滤/JOIN 列并创建合适索引；可用 EXPLAIN 验证",
				})
			}
		}
		noteRowsErr("pgSchemaHealth#2", rs2)
		rs2.Close()
	}

	// FK columns without supporting index
	rs3, err := db.QueryContext(ctx, `
SELECT n.nspname, c.relname, a.attname
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS u(attnum, ord) ON true
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = u.attnum
WHERE con.contype = 'f' AND n.nspname NOT IN ('pg_catalog','information_schema')
  AND NOT EXISTS (
    SELECT 1 FROM pg_index i
    WHERE i.indrelid = c.oid AND u.attnum = ANY (i.indkey::smallint[])
  )
LIMIT 40`)
	if err == nil {
		for rs3.Next() {
			var schema, table, col string
			if rs3.Scan(&schema, &table, &col) == nil {
				out = append(out, SchemaHealthFinding{
					Level: "medium", Code: "fk_no_index", Schema: schema, Table: table,
					Title: "外键列缺少索引", Detail: schema + "." + table + "." + col,
					Suggest: "为外键列创建索引，降低级联/JOIN 与锁等待成本",
				})
			}
		}
		noteRowsErr("pgSchemaHealth#3", rs3)
		rs3.Close()
	}

	// Unused indexes (idx_scan = 0) with non-trivial size
	rs4, err := db.QueryContext(ctx, `
SELECT schemaname, relname, indexrelname, pg_relation_size(indexrelid)
FROM pg_stat_user_indexes
WHERE idx_scan = 0 AND pg_relation_size(indexrelid) > 1024*1024
ORDER BY pg_relation_size(indexrelid) DESC
LIMIT 30`)
	if err == nil {
		for rs4.Next() {
			var schema, table, idx string
			var sz int64
			if rs4.Scan(&schema, &table, &idx, &sz) == nil {
				out = append(out, SchemaHealthFinding{
					Level: "low", Code: "unused_index", Schema: schema, Table: table,
					Title: "疑似未使用索引", Detail: fmt.Sprintf("%s.%s.%s (~%d MB)", schema, table, idx, sz/(1024*1024)),
					Suggest: "确认业务低峰后评估 DROP INDEX；注意唯一约束与覆盖索引场景",
				})
			}
		}
		noteRowsErr("pgSchemaHealth#4", rs4)
		rs4.Close()
	}
	return out, nil
}

// pgCollectSlowDigests reads pg_stat_statements (extension required).
func pgCollectSlowDigests(c MySQLConnection, cfg *SlowSQLMonitorConfig) ([]slowDigestRow, error) {
	db, err := pgOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	var installed bool
	_ = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_stat_statements')`).Scan(&installed)
	if !installed {
		return nil, fmt.Errorf("需要安装并启用扩展 pg_stat_statements（CREATE EXTENSION pg_stat_statements）")
	}

	topN := 30
	minAvg := 100.0
	if cfg != nil {
		if cfg.TopN > 0 {
			topN = cfg.TopN
		}
		if cfg.MinAvgLatencyMs > 0 {
			minAvg = cfg.MinAvgLatencyMs
		}
	}
	q := `
SELECT queryid::text,
       LEFT(query, 2000),
       calls,
       (total_exec_time / NULLIF(calls,0))::float8 AS avg_ms,
       total_exec_time::float8 AS sum_ms,
       rows::float8
FROM pg_stat_statements
WHERE calls > 0 AND (total_exec_time / NULLIF(calls,0)) >= $1
  AND query NOT ILIKE '%pg_stat_statements%'
ORDER BY total_exec_time DESC
LIMIT $2`
	rs, err := db.QueryContext(ctx, q, minAvg, topN)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := make([]slowDigestRow, 0, topN)
	for rs.Next() {
		var row slowDigestRow
		var avg, sum, rowsF float64
		var calls int64
		if err := rs.Scan(&row.Digest, &row.SQL, &calls, &avg, &sum, &rowsF); err != nil {
			continue
		}
		_ = rowsF
		row.Schema = c.Database
		row.CountStar = calls
		row.AvgLatencyMs = avg
		row.SumLatencyMs = sum
		row.MaxLatencyMs = avg // pg_stat_statements has no max in older versions; approx
		out = append(out, row)
	}
	return out, rs.Err()
}

// dataSourceToSQLConn maps a SQL-type DataSource into the toolkit connection shape.
func dataSourceToSQLConn(ds DataSource) (MySQLConnection, error) {
	host := strings.TrimSpace(ds.URL)
	host = strings.TrimPrefix(host, "postgres://")
	host = strings.TrimPrefix(host, "postgresql://")
	host = strings.TrimPrefix(host, "mysql://")
	// If full URL with user@ was pasted into URL, peel host:port/db crudely.
	if i := strings.Index(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	port := ds.Port
	database := strings.TrimSpace(ds.Database)
	if strings.Contains(host, "/") {
		parts := strings.SplitN(host, "/", 2)
		host = parts[0]
		if database == "" && len(parts) > 1 {
			database = strings.SplitN(parts[1], "?", 2)[0]
		}
	}
	if strings.Contains(host, ":") {
		h, p, err := splitHostPortLoose(host)
		if err == nil {
			host = h
			if port <= 0 {
				port = p
			}
		}
	}
	drv := "mysql"
	if ds.Type == "postgres" || ds.Type == "postgresql" {
		drv = "postgres"
		if port <= 0 {
			port = 5432
		}
	} else if port <= 0 {
		port = 3306
	}
	if host == "" {
		return MySQLConnection{}, fmt.Errorf("主机地址为空")
	}
	return MySQLConnection{
		ID: ds.ID, Name: ds.Name, Driver: drv, Host: host, Port: port,
		User: ds.AuthUser, Password: ds.AuthPass, Database: database,
		TLS: ds.TLS, Params: ds.Params, Enabled: ds.Enabled,
	}, nil
}

func splitHostPortLoose(hp string) (string, int, error) {
	// last colon for IPv4 host:port (IPv6 rare in our UI)
	i := strings.LastIndex(hp, ":")
	if i <= 0 {
		return "", 0, fmt.Errorf("no port")
	}
	p, err := strconv.Atoi(hp[i+1:])
	if err != nil {
		return "", 0, err
	}
	return hp[:i], p, nil
}
