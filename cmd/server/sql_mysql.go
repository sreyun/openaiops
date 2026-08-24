package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aiops-monitor/cmd/server/sqltoolkit"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func mysqlDSN(c MySQLConnection) string {
	user := c.User
	if user == "" {
		user = "root"
	}
	port := c.Port
	if port <= 0 {
		port = 3306
	}
	cfg := mysqldriver.NewConfig()
	cfg.User = user
	cfg.Passwd = c.Password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", c.Host, port)
	cfg.DBName = c.Database
	cfg.ParseTime = true
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 5 * time.Second
	cfg.InterpolateParams = true
	cfg.Params = map[string]string{}
	if c.TLS != "" {
		cfg.TLSConfig = c.TLS
	}
	if c.Params != "" {
		if extra, err := url.ParseQuery(c.Params); err == nil {
			for k, vs := range extra {
				if len(vs) > 0 {
					cfg.Params[k] = vs[0]
				}
			}
		}
	}
	return cfg.FormatDSN()
}

func mysqlOpen(c MySQLConnection) (*sql.DB, error) {
	db, err := sql.Open("mysql", mysqlDSN(c))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(2 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func mysqlTestConnection(c MySQLConnection) (string, error) {
	db, err := mysqlOpen(c)
	if err != nil {
		return "", err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ver string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&ver); err != nil {
		return "", err
	}
	return ver, nil
}

var reSafeIdent = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func mysqlExplain(c MySQLConnection, query string) (map[string]any, error) {
	return mysqlExplainInSchema(c, "", query)
}

// mysqlExplainInSchema runs EXPLAIN after optionally selecting a schema (for multi-DB connections).
func mysqlExplainInSchema(c MySQLConnection, schema, query string) (map[string]any, error) {
	query = strings.TrimSpace(query)
	schema = strings.TrimSpace(schema)
	if query == "" {
		return nil, fmt.Errorf("sql required")
	}
	if sqltoolkit.ForbiddenWrite(query) {
		return nil, fmt.Errorf("拒绝执行写操作或危险语句")
	}
	prepared, prepNotes := sqltoolkit.PrepareSQLForExplain(query, dialectForConn(c))
	if prepared != "" {
		query = prepared
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
	kw := sqltoolkit.FirstKeyword(query)
	if kw != "select" && kw != "with" && kw != "explain" {
		return attachPrep(fmt.Errorf("EXPLAIN 仅允许 SELECT / WITH / EXPLAIN 语句"))
	}
	if !sqltoolkit.IsReadOnlyQuery(query) {
		return attachPrep(fmt.Errorf("仅允许单条只读查询"))
	}
	explainSQL := query
	if kw != "explain" {
		explainSQL = "EXPLAIN FORMAT=JSON " + strings.TrimSuffix(query, ";")
	} else if !strings.Contains(strings.ToLower(query), "format") {
		// EXPLAIN SELECT ... → EXPLAIN FORMAT=JSON SELECT ...
		rest := strings.TrimSpace(query[len("explain"):])
		explainSQL = "EXPLAIN FORMAT=JSON " + rest
	}

	if schema != "" {
		if !reSafeIdent.MatchString(schema) {
			return attachPrep(fmt.Errorf("非法库名"))
		}
		c.Database = schema
	}
	db, err := mysqlOpen(c)
	if err != nil {
		return attachPrep(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if schema != "" {
		if _, err := db.ExecContext(ctx, "USE `"+schema+"`"); err != nil {
			return attachPrep(fmt.Errorf("无法切换到库 %s: %w（请确认库名正确且账号有权限）", schema, err))
		}
	}
	var raw string
	if err := db.QueryRowContext(ctx, explainSQL).Scan(&raw); err != nil {
		msg := err.Error()
		low := strings.ToLower(msg)
		if schema == "" && (strings.Contains(low, "no database selected") || strings.Contains(low, "1046")) {
			return attachPrep(fmt.Errorf("未指定数据库：实例上有多个库时，请先选择 Schema，或从慢 SQL 填入（会自动带库名）后再 EXPLAIN"))
		}
		return attachPrep(fmt.Errorf("EXPLAIN 失败: %w", err))
	}
	var parsed any
	_ = json.Unmarshal([]byte(raw), &parsed)
	analysis := analyzeExplainJSON(raw)
	out := map[string]any{
		"explain_json": parsed,
		"raw":          raw,
		"analysis":     analysis,
	}
	if len(prepNotes) > 0 {
		out["prepare_notes"] = prepNotes
		out["prepared_sql"] = query
	}
	attachExplainAdvice(out, c, schema, query, analysis)
	return out, nil
}

// mysqlListBusinessDatabases returns non-system schemas on the instance.
func mysqlListBusinessDatabases(c MySQLConnection) ([]string, error) {
	db, err := mysqlOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if mysqlSystemSchema(name) {
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// mysqlExecDDL runs a narrowly-whitelisted index DDL with a short timeout.
func mysqlExecDDL(c MySQLConnection, ddl string, timeoutSec int) (map[string]any, error) {
	ddl = strings.TrimSpace(ddl)
	if ddl == "" {
		return nil, fmt.Errorf("sql required")
	}
	if !sqltoolkit.IsAllowedIndexDDL(ddl) {
		return nil, fmt.Errorf("仅允许 CREATE/ALTER 索引类 DDL（拒绝 DROP/DML/建表等）")
	}
	if timeoutSec < 5 {
		timeoutSec = 30
	}
	if timeoutSec > 120 {
		timeoutSec = 120
	}
	db, err := mysqlOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	start := time.Now()
	res, err := db.ExecContext(ctx, strings.TrimSuffix(ddl, ";"))
	if err != nil {
		return nil, fmt.Errorf("DDL 执行失败: %w", err)
	}
	aff, _ := res.RowsAffected()
	return map[string]any{
		"ok":            true,
		"rows_affected": aff,
		"elapsed_ms":    time.Since(start).Milliseconds(),
	}, nil
}

func analyzeExplainJSON(raw string) *sqltoolkit.ExplainAnalysis {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return &sqltoolkit.ExplainAnalysis{Summary: "无法解析 EXPLAIN JSON", TableAccess: []sqltoolkit.ExplainHit{}}
	}
	queryBlock, _ := root["query_block"].(map[string]any)
	hits := []sqltoolkit.ExplainHit{}
	walkExplain(queryBlock, &hits)
	indexHits, fullScans, filesorts, temps := 0, 0, 0, 0
	for _, h := range hits {
		if h.FullScanRisk {
			fullScans++
		}
		if h.UsingFilesort {
			filesorts++
		}
		if h.UsingTemp {
			temps++
		}
		if h.Key != "" && h.Key != "null" && h.Key != "<nil>" {
			indexHits++
		}
	}
	summary := fmt.Sprintf("表访问 %d 处；命中索引 %d；全表/索引扫描风险 %d", len(hits), indexHits, fullScans)
	if filesorts > 0 {
		summary += fmt.Sprintf("；filesort %d", filesorts)
	}
	if temps > 0 {
		summary += fmt.Sprintf("；temporary %d", temps)
	}
	if fullScans > 0 {
		summary += "。存在 ALL/index 全扫描，请结合 rows 与过滤条件优化。"
	} else if indexHits > 0 {
		summary += "。主要路径已使用索引（仍需关注回表与 rows 估计）。"
	}
	return &sqltoolkit.ExplainAnalysis{
		Summary:     summary,
		IndexHits:   indexHits,
		FullScans:   fullScans,
		Filesorts:   filesorts,
		TempTables:  temps,
		TableAccess: hits,
	}
}

func walkExplain(node map[string]any, hits *[]sqltoolkit.ExplainHit) {
	if node == nil {
		return
	}
	// 父节点的 using_filesort / using_temporary_table 在下面 table 分支里与表级标志
	// 合并（见 h.UsingFilesort ||= …），这里不需要再单独处理一次。
	if t, ok := node["table"].(map[string]any); ok {
		h := sqltoolkit.ExplainHit{
			Table:        fmt.Sprint(t["table_name"]),
			AccessType:   fmt.Sprint(t["access_type"]),
			Key:          fmt.Sprint(t["key"]),
			PossibleKeys: joinAny(t["possible_keys"]),
			Ref:          joinAny(t["ref"]),
		}
		if kl, ok := t["key_length"]; ok && kl != nil {
			h.KeyLength = fmt.Sprint(kl)
		}
		if cond, ok := t["attached_condition"]; ok && cond != nil {
			h.Condition = fmt.Sprint(cond)
		}
		h.Rows, _ = toFloat(t["rows"])
		h.Filtered, _ = toFloat(t["filtered"])
		if cost, ok := t["cost_info"].(map[string]any); ok {
			rc, _ := toFloat(cost["read_cost"])
			ec, _ := toFloat(cost["eval_cost"])
			h.Cost = rc + ec
		}
		if ui, ok := t["using_index"].(bool); ok {
			h.UsingIndex = ui
		}
		if fs, ok := t["using_filesort"].(bool); ok {
			h.UsingFilesort = fs
		}
		if ut, ok := t["using_temporary_table"].(bool); ok {
			h.UsingTemp = ut
		}
		// parent ordering/grouping may set filesort/temp
		if fs, ok := node["using_filesort"].(bool); ok {
			h.UsingFilesort = h.UsingFilesort || fs
		}
		if ut, ok := node["using_temporary_table"].(bool); ok {
			h.UsingTemp = h.UsingTemp || ut
		}
		at := strings.ToLower(h.AccessType)
		if at == "all" || (at == "index" && !h.UsingIndex) {
			h.FullScanRisk = true
			h.Message = "全表或全索引扫描风险"
		} else if h.Key != "" && h.Key != "<nil>" && h.Key != "null" {
			h.Message = "使用索引 " + h.Key
		} else if at == "ref" || at == "eq_ref" || at == "const" || at == "range" {
			h.Message = "访问类型 " + h.AccessType
		}
		*hits = append(*hits, h)
	}
	for _, k := range []string{"nested_loop", "grouping_operation", "ordering_operation", "duplicates_removal", "union_result", "optimized_away_selects"} {
		if arr, ok := node[k].([]any); ok {
			for _, it := range arr {
				if m, ok := it.(map[string]any); ok {
					walkExplain(m, hits)
				}
			}
		} else if m, ok := node[k].(map[string]any); ok {
			walkExplain(m, hits)
		}
	}
}

// mysqlFetchMetadataInSchema is like mysqlFetchMetadata but forces TABLE_SCHEMA when schema is set.
// Table names may be schema-qualified (db.tbl); unqualified names use schema or connection default.
func mysqlFetchMetadataInSchema(c MySQLConnection, schema string, tables []string) (sqltoolkit.SchemaMeta, error) {
	meta := sqltoolkit.SchemaMeta{}
	if len(tables) == 0 {
		return meta, nil
	}
	type named struct{ schema, table, key string }
	var clean []named
	for _, t := range tables {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		sch, tbl := "", t
		if i := strings.LastIndexByte(t, '.'); i > 0 {
			sch = t[:i]
			tbl = t[i+1:]
		}
		if sch == "" {
			sch = strings.TrimSpace(schema)
		}
		if sch == "" {
			sch = c.Database
		}
		if tbl == "" || !reSafeIdent.MatchString(tbl) {
			continue
		}
		if sch != "" && !reSafeIdent.MatchString(sch) {
			continue
		}
		key := strings.ToLower(tbl)
		clean = append(clean, named{schema: sch, table: tbl, key: key})
		meta[key] = &sqltoolkit.TableMeta{Name: tbl}
	}
	if len(clean) == 0 {
		return meta, nil
	}
	db, err := mysqlOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Prefer a single schema when all tables share one (or connection default).
	dbName := strings.TrimSpace(schema)
	if dbName == "" {
		dbName = c.Database
	}
	if dbName == "" {
		for _, n := range clean {
			if n.schema != "" {
				dbName = n.schema
				break
			}
		}
	}
	placeholders := make([]string, len(clean))
	args := make([]any, 0, len(clean)+1)
	if dbName != "" {
		args = append(args, dbName)
	}
	for i, n := range clean {
		placeholders[i] = "?"
		args = append(args, n.table)
	}
	inList := strings.Join(placeholders, ",")

	// TABLE_ROWS — require schema when known to avoid cross-DB name collisions.
	qTables := "SELECT TABLE_NAME, TABLE_ROWS, AVG_ROW_LENGTH FROM information_schema.TABLES WHERE TABLE_NAME IN (" + inList + ")"
	if dbName != "" {
		qTables = "SELECT TABLE_NAME, TABLE_ROWS, AVG_ROW_LENGTH FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME IN (" + inList + ")"
	}
	if rows, err := db.QueryContext(ctx, qTables, args...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			var tr, ar sql.NullInt64
			if err := rows.Scan(&name, &tr, &ar); err != nil {
				continue
			}
			tm := meta[strings.ToLower(name)]
			if tm == nil {
				continue
			}
			tm.TableRows = tr.Int64
			tm.AvgRowLen = ar.Int64
		}
		noteRowsErr("mysqlFetchMetadataInSchema#1", rows)
	}

	// COLUMNS
	qCols := "SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE, IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_NAME IN (" + inList + ") ORDER BY ORDINAL_POSITION"
	colArgs := args
	if dbName != "" {
		qCols = "SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE, IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME IN (" + inList + ") ORDER BY ORDINAL_POSITION"
	}
	if rows, err := db.QueryContext(ctx, qCols, colArgs...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var tname, cname, dtype, nullable string
			if err := rows.Scan(&tname, &cname, &dtype, &nullable); err != nil {
				continue
			}
			tm := meta[strings.ToLower(tname)]
			if tm == nil {
				continue
			}
			tm.Columns = append(tm.Columns, sqltoolkit.ColumnMeta{
				Name: cname, DataType: dtype, Nullable: strings.EqualFold(nullable, "YES"),
			})
		}
		noteRowsErr("mysqlFetchMetadataInSchema#2", rows)
	}

	// STATISTICS → indexes
	qIdx := "SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_NAME IN (" + inList + ") ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX"
	idxArgs := args
	if dbName != "" {
		qIdx = "SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME IN (" + inList + ") ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX"
	}
	type idxBuild struct {
		name   string
		unique bool
		cols   []string
	}
	building := map[string]map[string]*idxBuild{} // table -> indexName -> build
	if rows, err := db.QueryContext(ctx, qIdx, idxArgs...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var tname, iname, cname string
			var nonUnique int
			var seq int
			if err := rows.Scan(&tname, &iname, &nonUnique, &seq, &cname); err != nil {
				continue
			}
			tk := strings.ToLower(tname)
			if building[tk] == nil {
				building[tk] = map[string]*idxBuild{}
			}
			b := building[tk][iname]
			if b == nil {
				b = &idxBuild{name: iname, unique: nonUnique == 0}
				building[tk][iname] = b
			}
			b.cols = append(b.cols, cname)
		}
		noteRowsErr("mysqlFetchMetadataInSchema#3", rows)
	}
	for tk, idxs := range building {
		tm := meta[tk]
		if tm == nil {
			continue
		}
		for _, b := range idxs {
			tm.Indexes = append(tm.Indexes, sqltoolkit.IndexMeta{Name: b.name, Unique: b.unique, Columns: b.cols})
		}
	}
	return meta, nil
}

func joinAny(v any) string {
	switch t := v.(type) {
	case []any:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			parts = append(parts, fmt.Sprint(x))
		}
		return strings.Join(parts, ",")
	case string:
		return t
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	case int:
		return float64(t), true
	default:
		return 0, false
	}
}

func mysqlSystemSchema(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "mysql", "information_schema", "performance_schema", "sys":
		return true
	default:
		return false
	}
}

// mysqlSchema lists databases / tables / columns.
// When connection has no default database:
//   - database="" table="" → list business databases
//   - database=foo table="" → list tables in foo
//   - database=foo table=bar → columns/indexes for foo.bar
func mysqlSchema(c MySQLConnection, database, table string) (map[string]any, error) {
	db, err := mysqlOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dbName := strings.TrimSpace(database)
	if dbName == "" {
		dbName = strings.TrimSpace(c.Database)
	}
	table = strings.TrimSpace(table)

	if table == "" && dbName == "" {
		rows, err := db.QueryContext(ctx, "SHOW DATABASES")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		dbs := []string{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				continue
			}
			if mysqlSystemSchema(name) {
				continue
			}
			dbs = append(dbs, name)
		}
		noteRowsErr("mysqlSchema#1", rows)
		return map[string]any{"databases": dbs}, nil
	}

	if table == "" {
		if !reSafeIdent.MatchString(dbName) {
			return nil, fmt.Errorf("非法库名")
		}
		rows, err := db.QueryContext(ctx,
			"SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME",
			dbName)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		tables := []string{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				continue
			}
			tables = append(tables, name)
		}
		noteRowsErr("mysqlSchema#2", rows)
		return map[string]any{"database": dbName, "tables": tables}, nil
	}

	if !reSafeIdent.MatchString(table) {
		return nil, fmt.Errorf("非法表名")
	}
	if dbName == "" {
		return nil, fmt.Errorf("查看表结构需要指定 database（连接未配置默认库时请先选择库）")
	}
	if !reSafeIdent.MatchString(dbName) {
		return nil, fmt.Errorf("非法库名")
	}
	qualified := "`" + dbName + "`.`" + table + "`"
	colRows, err := db.QueryContext(ctx, "SHOW FULL COLUMNS FROM "+qualified)
	if err != nil {
		return nil, err
	}
	defer colRows.Close()
	columns := []map[string]any{}
	colNames, _ := colRows.Columns()
	for colRows.Next() {
		vals := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := colRows.Scan(ptrs...); err != nil {
			continue
		}
		m := map[string]any{}
		for i, col := range colNames {
			m[col] = stringifySQLVal(vals[i])
		}
		columns = append(columns, m)
	}
	noteRowsErr("mysqlSchema#3", colRows)
	idxRows, err := db.QueryContext(ctx, "SHOW INDEX FROM "+qualified)
	if err != nil {
		return nil, err
	}
	defer idxRows.Close()
	cols, _ := idxRows.Columns()
	indexes := []map[string]any{}
	for idxRows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := idxRows.Scan(ptrs...); err != nil {
			continue
		}
		m := map[string]any{}
		for i, col := range cols {
			m[col] = stringifySQLVal(vals[i])
		}
		indexes = append(indexes, m)
	}
	noteRowsErr("mysqlSchema#4", idxRows)
	var createSQL string
	var tblName string
	if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE "+qualified).Scan(&tblName, &createSQL); err != nil {
		return map[string]any{"database": dbName, "table": table, "columns": columns, "indexes": indexes}, nil
	}
	return map[string]any{
		"database":     dbName,
		"table":        table,
		"columns":      columns,
		"indexes":      indexes,
		"create_table": createSQL,
	}, nil
}

func stringifySQLVal(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339)
	default:
		return t
	}
}

// mysqlQueryReadOnly runs a single SELECT/WITH for datasource / dashboard panels.
func mysqlQueryReadOnly(c MySQLConnection, sqlText string, limit int) ([]string, []map[string]any, error) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return nil, nil, fmt.Errorf("sql required")
	}
	if !sqltoolkit.IsReadOnlyQuery(sqlText) || sqltoolkit.ForbiddenWrite(sqlText) {
		return nil, nil, fmt.Errorf("仅允许单条只读 SELECT/WITH")
	}
	kw := sqltoolkit.FirstKeyword(sqlText)
	if kw != "select" && kw != "with" && kw != "show" && kw != "desc" && kw != "describe" {
		return nil, nil, fmt.Errorf("仅允许 SELECT/WITH/SHOW")
	}
	limit = clampSQLReadLimit(limit)
	execSQL := strings.TrimSuffix(sqlText, ";")
	if kw == "select" || kw == "with" {
		if !strings.Contains(strings.ToLower(sqltoolkit.StripCommentsAndStrings(execSQL)), "limit") {
			// limit is a bounded int (not user text) — safe to embed as a literal.
			execSQL = fmt.Sprintf("SELECT * FROM (%s) AS _aiops_q LIMIT %d", execSQL, limit)
		}
	}
	db, err := mysqlOpen(c)
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
	cols, err := rs.Columns()
	if err != nil {
		return nil, nil, err
	}
	var rows []map[string]any
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
			m[col] = stringifySQLVal(vals[i])
		}
		rows = append(rows, m)
	}
	return cols, rows, rs.Err()
}
