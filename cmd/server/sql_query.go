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
