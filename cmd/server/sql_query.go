package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aiops-monitor/cmd/server/sqltoolkit"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// sqlWorkbenchQueryResult is the workbench "Run" response: rows + timing split.
type sqlWorkbenchQueryResult struct {
	OK        bool             `json:"ok"`
	Driver    string           `json:"driver"`
	Schema    string           `json:"schema,omitempty"`
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	RowCount  int              `json:"row_count"`
	Limit     int              `json:"limit"`
	Truncated bool             `json:"truncated"`
	ExecMs    int64            `json:"exec_ms"`  // until QueryContext returns (server accepted / started streaming)
	FetchMs   int64            `json:"fetch_ms"` // client-side row fetch / decode
	TotalMs   int64            `json:"total_ms"`
	Error     string           `json:"error,omitempty"`
}

func clampSQLQueryTimeout(sec int) time.Duration {
	if sec <= 0 {
		return 20 * time.Second
	}
	if sec > 60 {
		sec = 60
	}
	return time.Duration(sec) * time.Second
}

// handleSQLWorkbenchQuery runs a single read-only statement and returns rows + timings.
// POST /api/v1/sql/connections/{id}/query
func (s *Server) handleSQLWorkbenchQuery(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	c, ok := s.cfg.GetMySQLConnection(id)
	if err := mysqlConnReady(c, ok); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	var req struct {
		SQL        string `json:"sql"`
		Schema     string `json:"schema"`
		Database   string `json:"database"`
		Limit      int    `json:"limit"`
		Offset     int    `json:"offset"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	sqlText := strings.TrimSpace(req.SQL)
	if sqlText == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请先输入 SQL"})
		return
	}
	if sqltoolkit.HasUnboundPlaceholder(sqlText) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "SQL 仍含 ? / $n 占位符：请填入真实参数后再运行（运行不会用探测值替参）",
		})
		return
	}
	schema := strings.TrimSpace(firstNonEmpty(req.Schema, req.Database))
	if schema == "" {
		schema = inferSchemaFromSQLText(sqlText)
	}
	if schema == "" {
		schema = strings.TrimSpace(c.Database)
	}
	limit := clampSQLReadLimit(req.Limit)
	timeout := clampSQLQueryTimeout(req.TimeoutSec)

	// 与流式路径共用一套准备逻辑（只读校验、库名校验、追加 LIMIT、独占连接、会话参数），
	// 并把查询的生命周期绑到这次 HTTP 请求上：用户关掉页面，数据库那边也会停下来。
	// 老代码这里用的是 context.Background()——没人看了查询还在烧 CPU。
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	totalStart := time.Now()
	sess, prepErr := prepareSQLRead(ctx, c, sqlReadRequest{
		SQL: sqlText, Schema: schema, Limit: limit, Offset: req.Offset, TimeoutSec: req.TimeoutSec,
	}, limit, timeout)
	if prepErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": prepErr.Error()})
		return
	}
	defer sess.close()

	execStart := time.Now()
	rs, qErr := sess.conn.QueryContext(ctx, sess.execSQL)
	execMs := time.Since(execStart).Milliseconds()
	if qErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok": false, "error": sqlFriendlyError(qErr, ctx),
			"exec_ms": execMs, "total_ms": time.Since(totalStart).Milliseconds(), "schema": sess.schema,
		})
		return
	}
	defer rs.Close()
	fetchStart := time.Now()
	cols, rows, truncated, scanErr := scanSQLResultRowsLimited(rs, limit, sess.driver == "mysql")
	fetchMs := time.Since(fetchStart).Milliseconds()
	if scanErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok": false, "error": sqlFriendlyError(scanErr, ctx),
			"exec_ms": execMs, "fetch_ms": fetchMs,
			"total_ms": time.Since(totalStart).Milliseconds(), "schema": sess.schema,
		})
		return
	}
	res := &sqlWorkbenchQueryResult{
		OK: true, Driver: sess.driver, Schema: sess.schema,
		Columns: cols, Rows: rows, RowCount: len(rows), Limit: limit, Truncated: truncated,
		ExecMs: execMs, FetchMs: fetchMs, TotalMs: time.Since(totalStart).Milliseconds(),
	}
	s.recordSQLHistory(r, "query", id, sqlText, nil)
	writeJSON(w, http.StatusOK, res)
}

func mysqlWorkbenchQuery(c MySQLConnection, schema, sqlText string, limit int, timeout time.Duration) (*sqlWorkbenchQueryResult, error) {
	sqlText = strings.TrimSpace(sqlText)
	if reason := sqltoolkit.StrictReadOnlyMySQL(sqlText); reason != "" {
		return nil, fmt.Errorf("仅允许只读查询：%s", reason)
	}
	kw := sqltoolkit.FirstKeyword(sqlText)
	if kw != "select" && kw != "with" && kw != "show" && kw != "desc" && kw != "describe" {
		return nil, fmt.Errorf("仅允许 SELECT/WITH/SHOW/DESC")
	}
	schema = strings.TrimSpace(schema)
	if schema != "" {
		if !reSafeIdent.MatchString(schema) {
			return nil, fmt.Errorf("非法库名")
		}
		c.Database = schema
	}
	execSQL := strings.TrimSuffix(sqlText, ";")
	wrapped := false
	if kw == "select" || kw == "with" {
		if !strings.Contains(strings.ToLower(sqltoolkit.StripCommentsAndStrings(execSQL)), "limit") {
			execSQL = fmt.Sprintf("SELECT * FROM (%s) AS _aiops_q LIMIT %d", execSQL, limit)
			wrapped = true
		}
	}

	totalStart := time.Now()
	db, err := mysqlOpenForRead(c, timeout+2*time.Second)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// 使用独占连接：会话级 SET/USE 必须与查询落在同一条连接上，
	// 否则连接池可能把查询分到未设置只读模式的连接。
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取数据库连接失败：%w", err)
	}
	defer conn.Close()
	// 数据库侧兜底：即使 SQL 绕过上层校验，任何写操作也会被数据库拒绝。
	if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
		return nil, fmt.Errorf("设置只读会话失败：%w", err)
	}
	if schema != "" {
		if _, err := conn.ExecContext(ctx, "USE `"+schema+"`"); err != nil {
			return nil, fmt.Errorf("切换库失败：%w", err)
		}
	}

	execStart := time.Now()
	rs, err := conn.QueryContext(ctx, execSQL)
	execMs := time.Since(execStart).Milliseconds()
	if err != nil {
		return &sqlWorkbenchQueryResult{ExecMs: execMs, TotalMs: time.Since(totalStart).Milliseconds(), Schema: schema}, err
	}
	defer rs.Close()

	fetchStart := time.Now()
	cols, rows, err := scanSQLResultRows(rs, limit, true)
	fetchMs := time.Since(fetchStart).Milliseconds()
	if err != nil {
		return &sqlWorkbenchQueryResult{
			ExecMs: execMs, FetchMs: fetchMs, TotalMs: time.Since(totalStart).Milliseconds(), Schema: schema,
		}, err
	}
	truncated := len(rows) >= limit || wrapped && len(rows) >= limit
	return &sqlWorkbenchQueryResult{
		OK: true, Driver: "mysql", Schema: schema,
		Columns: cols, Rows: rows, RowCount: len(rows), Limit: limit, Truncated: truncated,
		ExecMs: execMs, FetchMs: fetchMs, TotalMs: time.Since(totalStart).Milliseconds(),
	}, nil
}

func pgWorkbenchQuery(c MySQLConnection, schema, sqlText string, limit int, timeout time.Duration) (*sqlWorkbenchQueryResult, error) {
	sqlText = strings.TrimSpace(sqlText)
	if reason := sqltoolkit.StrictReadOnlyPostgres(sqlText); reason != "" {
		return nil, fmt.Errorf("仅允许只读查询：%s", reason)
	}
	kw := sqltoolkit.FirstKeyword(sqlText)
	if kw != "select" && kw != "with" && kw != "values" && kw != "show" && kw != "table" {
		return nil, fmt.Errorf("PostgreSQL 仅允许 SELECT/WITH/VALUES")
	}
	schema = strings.TrimSpace(schema)
	if schema != "" && !reSafeIdent.MatchString(schema) {
		return nil, fmt.Errorf("非法 schema 名")
	}
	execSQL := strings.TrimSuffix(sqlText, ";")
	wrapped := false
	if !strings.Contains(strings.ToLower(sqltoolkit.StripCommentsAndStrings(execSQL)), "limit") {
		execSQL = fmt.Sprintf("SELECT * FROM (%s) AS _aiops_q LIMIT %d", execSQL, limit)
		wrapped = true
	}

	totalStart := time.Now()
	db, err := pgOpen(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取数据库连接失败：%w", err)
	}
	defer conn.Close()
	// 会话级只读兜底：任何写操作都会被 PostgreSQL 拒绝。
	if _, err := conn.ExecContext(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY"); err != nil {
		return nil, fmt.Errorf("设置只读会话失败：%w", err)
	}
	if schema != "" {
		// set_config(..., false) 为会话级；旧代码用 true（事务级）在 autocommit
		// 下仅对 SET 语句自身生效，后续查询拿不到 search_path。
		if _, err := conn.ExecContext(ctx, `SELECT set_config('search_path', $1, false)`, schema); err != nil {
			return nil, fmt.Errorf("设置 search_path 失败：%w", err)
		}
	}

	execStart := time.Now()
	rs, err := conn.QueryContext(ctx, execSQL)
	execMs := time.Since(execStart).Milliseconds()
	if err != nil {
		return &sqlWorkbenchQueryResult{ExecMs: execMs, TotalMs: time.Since(totalStart).Milliseconds(), Schema: schema, Driver: "postgres"}, err
	}
	defer rs.Close()

	fetchStart := time.Now()
	cols, rows, err := scanSQLResultRows(rs, limit, false)
	fetchMs := time.Since(fetchStart).Milliseconds()
	if err != nil {
		return &sqlWorkbenchQueryResult{
			Driver: "postgres", Schema: schema,
			ExecMs: execMs, FetchMs: fetchMs, TotalMs: time.Since(totalStart).Milliseconds(),
		}, err
	}
	truncated := len(rows) >= limit || (wrapped && len(rows) >= limit)
	return &sqlWorkbenchQueryResult{
		OK: true, Driver: "postgres", Schema: schema,
		Columns: cols, Rows: rows, RowCount: len(rows), Limit: limit, Truncated: truncated,
		ExecMs: execMs, FetchMs: fetchMs, TotalMs: time.Since(totalStart).Milliseconds(),
	}, nil
}

// scanSQLResultRowsLimited 读取至多 limit 行，并如实告诉调用方**是不是还有更多**。
//
// 老写法用 len(rows) >= limit 判断截断：结果正好等于 limit 行时会谎报"已截断"，
// 用户以为数据没取全，其实取全了。这里多读一行来判断，读完就把多的那行丢掉。
func scanSQLResultRowsLimited(rs *sql.Rows, limit int, mysqlStyle bool) ([]string, []map[string]any, bool, error) {
	cols, err := rs.Columns()
	if err != nil {
		return nil, nil, false, err
	}
	prealloc := limit
	if prealloc > 512 {
		prealloc = 512 // 上限很大时不要一上来就吃内存
	}
	rows := make([]map[string]any, 0, prealloc)
	truncated := false
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rs.Next() {
		if len(rows) >= limit {
			truncated = true // 还有下一行 → 确实被截断了
			break
		}
		if err := rs.Scan(ptrs...); err != nil {
			return cols, rows, truncated, err
		}
		m := make(map[string]any, len(cols))
		for i, col := range cols {
			m[col] = sqlCellValue(vals[i], mysqlStyle)
		}
		rows = append(rows, m)
	}
	return cols, rows, truncated, rs.Err()
}

func scanSQLResultRows(rs *sql.Rows, limit int, mysqlStyle bool) ([]string, []map[string]any, error) {
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
			if mysqlStyle {
				m[col] = stringifySQLVal(vals[i])
			} else {
				m[col] = normalizeSQLValue(vals[i])
			}
		}
		rows = append(rows, m)
	}
	return cols, rows, rs.Err()
}

// mysqlOpenForRead opens MySQL with an extended read timeout for workbench queries.
func mysqlOpenForRead(c MySQLConnection, readTimeout time.Duration) (*sql.DB, error) {
	if readTimeout < 5*time.Second {
		readTimeout = 20 * time.Second
	}
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
	cfg.ReadTimeout = readTimeout
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
	db, err := sql.Open("mysql", cfg.FormatDSN())
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
