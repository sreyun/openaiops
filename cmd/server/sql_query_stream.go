package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"aiops-monitor/cmd/server/sqltoolkit"
)

// SQL 工作台的**流式**执行路径。
//
// 原来的 /query 一把梭：把整个结果集读进 []map[string]any，序列化成一大坨 JSON，再一次性
// 发给浏览器。数据量一大或者查询一慢，这条路上的每一环都会出问题：
//
//   - 浏览器在整条查询跑完之前**什么都看不到**，也没法中途放弃；
//   - 结果集在服务端内存里存一份、JSON 里再存一份，每一行还把所有列名重复一遍；
//   - 查询上下文是 context.Background()，**用户关掉页面查询仍在数据库上跑到底**——
//     慢查询最要命的不是慢，是没人看了它还在烧 CPU；
//   - 行上限写死 2000，想导出更多数据完全没有出路。
//
// 这里给出两条新路径，都以"边读边发、随时可停"为原则：
//
//	POST …/query/stream  → application/x-ndjson，逐批下发行数据（列式数组，不重复列名）
//	POST …/query/export  → text/csv，边读边写，行上限单独放宽，服务端不留整份结果
//
// 两条都把 r.Context() 一路带到数据库驱动：浏览器一断开（用户点了取消、关了页面），
// 连接随之关闭，MySQL/PostgreSQL 会终止这条查询。

const (
	// 流式查询的行上限：远高于老接口的 2000，但仍要有顶——浏览器渲染几十万行同样会卡死。
	sqlStreamMaxRows = 50000
	// 导出的行上限：这条路只写 CSV，不进内存也不进 DOM，可以放得更宽。
	sqlExportMaxRows = 500000
	// 每批下发多少行。批太小则 JSON 框架开销占比高，太大则首屏来得慢。
	sqlStreamBatchRows = 500
	// 单元格文本上限：一行 BLOB/JSON 长文本能把浏览器直接拖死。
	sqlCellMaxBytes = 64 << 10
)

func clampSQLStreamLimit(n int) int {
	if n <= 0 {
		return 1000
	}
	if n > sqlStreamMaxRows {
		return sqlStreamMaxRows
	}
	return n
}

func clampSQLExportLimit(n int) int {
	if n <= 0 {
		return 100000
	}
	if n > sqlExportMaxRows {
		return sqlExportMaxRows
	}
	return n
}

func clampSQLOffset(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100_000_000 {
		return 100_000_000
	}
	return n
}

// sqlReadRequest 是流式查询 / 导出共用的请求体。
type sqlReadRequest struct {
	SQL        string `json:"sql"`
	Schema     string `json:"schema"`
	Database   string `json:"database"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
	TimeoutSec int    `json:"timeout_sec"`
	Format     string `json:"format,omitempty"` // 导出：csv（默认）
	Filename   string `json:"filename,omitempty"`
}

// sqlReadSession 是一次只读查询已经准备好的执行上下文。
type sqlReadSession struct {
	conn    *sql.Conn
	db      *sql.DB
	driver  string
	schema  string
	execSQL string
	limit   int
	offset  int
	limited bool // 是否由我们补的 LIMIT（用户自己写了就不补）
}

func (s *sqlReadSession) close() {
	if s == nil {
		return
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.db != nil {
		_ = s.db.Close()
	}
}

// prepareSQLRead 做所有"跑之前"的事：只读校验、库名校验、补 LIMIT、开专用连接、设会话参数。
//
// 连接是**独占**的（db.Conn）：会话级的只读开关、search_path、statement_timeout 必须和查询
// 落在同一条物理连接上，否则连接池随手换一条，前面设的东西全白设。
func prepareSQLRead(ctx context.Context, c MySQLConnection, req sqlReadRequest, limit int, timeout time.Duration) (*sqlReadSession, error) {
	sqlText := strings.TrimSpace(req.SQL)
	if sqlText == "" {
		return nil, fmt.Errorf("请先输入 SQL")
	}
	if sqltoolkit.HasUnboundPlaceholder(sqlText) {
		return nil, fmt.Errorf("SQL 仍含 ? / $n 占位符：请填入真实参数后再运行")
	}
	schema := strings.TrimSpace(firstNonEmpty(req.Schema, req.Database))
	if schema == "" {
		schema = inferSchemaFromSQLText(sqlText)
	}
	if schema == "" {
		schema = strings.TrimSpace(c.Database)
	}
	if schema != "" && !reSafeIdent.MatchString(schema) {
		return nil, fmt.Errorf("非法库名 / schema 名")
	}
	offset := clampSQLOffset(req.Offset)

	if driverOf(c) == "postgres" {
		if reason := sqltoolkit.StrictReadOnlyPostgres(sqlText); reason != "" {
			return nil, fmt.Errorf("仅允许只读查询：%s", reason)
		}
		switch sqltoolkit.FirstKeyword(strings.TrimLeft(sqlText, "( \t\r\n")) {
		case "select", "with", "values", "show", "table":
		default:
			return nil, fmt.Errorf("PostgreSQL 仅允许 SELECT/WITH/VALUES/TABLE/SHOW")
		}
		// 下推 limit+1：数据库正好还回 limit 行时，我们无从判断"是不是还有更多"。
		// 多要一行，多出来的那行只用来标记 truncated，不会发给客户端。
		execSQL, limited := sqltoolkit.ApplyRowLimit(sqlText, limit+1, offset)
		db, err := pgOpen(c)
		if err != nil {
			return nil, err
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("获取数据库连接失败：%w", err)
		}
		sess := &sqlReadSession{conn: conn, db: db, driver: "postgres", schema: schema,
			execSQL: execSQL, limit: limit, offset: offset, limited: limited}
		if _, err := conn.ExecContext(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY"); err != nil {
			sess.close()
			return nil, fmt.Errorf("设置只读会话失败：%w", err)
		}
		// 数据库侧的硬闸：网络断了、进程被 kill 了，这条查询也会在超时后被 PostgreSQL 自己终止。
		// 只靠 ctx 是不够的——那只保证"我们不等了"，不保证"它不跑了"。
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET statement_timeout = %d", timeout.Milliseconds())); err != nil {
			sess.close()
			return nil, fmt.Errorf("设置语句超时失败：%w", err)
		}
		if schema != "" {
			// 先确认这个 schema 真的存在，再去设 search_path。
			//
			// set_config('search_path', '不存在的名字') **不会报错**——PostgreSQL 照单全收，
			// 于是错误一路推迟到用户那句 SELECT，最后以 `relation "xxx" does not exist` 的形式
			// 冒出来。用户看到的是"我的表没了"，真相是"schema 选错了"，两者长得毫无关系。
			// 这里提前拦一次，并把可选的 schema 列出来，让人一眼知道该选哪个。
			if err := pgEnsureSchemaExists(ctx, conn, schema); err != nil {
				sess.close()
				return nil, err
			}
			if _, err := conn.ExecContext(ctx, `SELECT set_config('search_path', $1, false)`, schema); err != nil {
				sess.close()
				return nil, fmt.Errorf("设置 search_path 失败：%w", err)
			}
		}
		return sess, nil
	}

	if reason := sqltoolkit.StrictReadOnlyMySQL(sqlText); reason != "" {
		return nil, fmt.Errorf("仅允许只读查询：%s", reason)
	}
	switch sqltoolkit.FirstKeyword(strings.TrimLeft(sqlText, "( \t\r\n")) {
	case "select", "with", "show", "desc", "describe":
	default:
		return nil, fmt.Errorf("仅允许 SELECT/WITH/SHOW/DESC")
	}
	if schema == "" {
		if dbs, e := mysqlListBusinessDatabases(c); e == nil && len(dbs) == 1 {
			schema = dbs[0]
		}
	}
	if schema != "" {
		c.Database = schema
	}
	// 同 PostgreSQL 分支：下推 limit+1，多出来的那行只用于判断是否被截断。
	execSQL, limited := sqltoolkit.ApplyRowLimit(sqlText, limit+1, offset)
	db, err := mysqlOpenForRead(c, timeout+2*time.Second)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("获取数据库连接失败：%w", err)
	}
	sess := &sqlReadSession{conn: conn, db: db, driver: "mysql", schema: schema,
		execSQL: execSQL, limit: limit, offset: offset, limited: limited}
	if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
		sess.close()
		return nil, fmt.Errorf("设置只读会话失败：%w", err)
	}
	// MySQL 侧的硬闸，与 PostgreSQL 的 statement_timeout 同义。8.0 起可用；
	// 老版本没有这个变量，设置失败不致命（ctx 仍然会在超时后掐断连接）。
	_, _ = conn.ExecContext(ctx, fmt.Sprintf("SET SESSION MAX_EXECUTION_TIME=%d", timeout.Milliseconds()))
	if schema != "" {
		if _, err := conn.ExecContext(ctx, "USE `"+schema+"`"); err != nil {
			sess.close()
			return nil, fmt.Errorf("切换库失败：%w", err)
		}
	}
	return sess, nil
}

// pgEnsureSchemaExists 在设 search_path 之前确认 schema 存在，不存在则给出可选清单。
//
// 为什么值得单独查一次：PostgreSQL 允许把 search_path 设成任何字符串，包括根本不存在的
// schema。错误因此不会出现在"设置"这一步，而是变成后面每一句 SELECT 的
// `relation "xxx" does not exist`——排查方向完全被带偏。多这一次 pg_namespace 查询
// （毫秒级、走系统目录）换来的是一句能直接照做的报错。
func pgEnsureSchemaExists(ctx context.Context, conn *sql.Conn, schema string) error {
	var exists bool
	if err := conn.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schema).Scan(&exists); err != nil {
		// 目录查不动就别拦路：把判断交回给真正的查询，至少不会平白多一条失败。
		return nil
	}
	if exists {
		return nil
	}
	var avail []string
	if rows, err := conn.QueryContext(ctx,
		`SELECT nspname FROM pg_namespace
		 WHERE nspname NOT IN ('pg_catalog','information_schema','pg_toast')
		   AND nspname NOT LIKE 'pg_temp_%' AND nspname NOT LIKE 'pg_toast_temp_%'
		 ORDER BY nspname LIMIT 30`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				avail = append(avail, n)
			}
		}
		noteRowsErr("pgEnsureSchemaExists", rows)
	}
	if len(avail) == 0 {
		return fmt.Errorf("schema %q 在当前数据库里不存在", schema)
	}
	return fmt.Errorf("schema %q 在当前数据库里不存在；可选：%s"+
		"（注意 PostgreSQL 这里选的是 schema，不是库名——库由连接配置决定）",
		schema, strings.Join(avail, "、"))
}

// sqlCellValue 把一个数据库值转成可以直接进 JSON / CSV 的形式，并截断超长文本。
func sqlCellValue(v any, mysqlStyle bool) any {
	var out any
	if mysqlStyle {
		out = stringifySQLVal(v)
	} else {
		out = normalizeSQLValue(v)
	}
	if s, ok := out.(string); ok && len(s) > sqlCellMaxBytes {
		return s[:sqlCellMaxBytes] + "…(truncated)"
	}
	return out
}

// handleSQLQueryStream 以 NDJSON 逐批下发查询结果。
// POST /api/v1/sql/connections/{id}/query/stream
func (s *Server) handleSQLQueryStream(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	c, ok := s.cfg.GetMySQLConnection(id)
	if err := mysqlConnReady(c, ok); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	var req sqlReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	limit := clampSQLStreamLimit(req.Limit)
	timeout := clampSQLQueryTimeout(req.TimeoutSec)

	// 查询的生命周期绑在这次 HTTP 请求上：用户点"取消"或关掉页面 → 请求断开 →
	// ctx 取消 → 驱动关连接 → 数据库终止查询。这是"慢查询没人看了还在烧 CPU"的正解。
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	totalStart := time.Now()
	sess, err := prepareSQLRead(ctx, c, req, limit, timeout)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	defer sess.close()

	execStart := time.Now()
	// 多取一行用于判断"是不是被截断了"——够不够正好 limit 行说明不了问题。
	rs, err := sess.conn.QueryContext(ctx, sess.execSQL)
	execMs := time.Since(execStart).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": sqlFriendlyError(err, ctx), "ok": false, "exec_ms": execMs, "schema": sess.schema,
		})
		return
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "ok": false})
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // 反代不要缓冲，否则"边读边发"退化成一次性
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	emit := func(v any) bool {
		if err := enc.Encode(v); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	if !emit(map[string]any{
		"type": "meta", "columns": cols, "schema": sess.schema, "driver": sess.driver,
		"exec_ms": execMs, "limit": limit, "offset": sess.offset, "limit_applied": sess.limited,
	}) {
		return
	}

	fetchStart := time.Now()
	mysqlStyle := sess.driver == "mysql"
	batch := make([][]any, 0, sqlStreamBatchRows)
	count := 0
	truncated := false
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	scanErr := ""
	for rs.Next() {
		if count >= limit {
			truncated = true
			break
		}
		if err := rs.Scan(ptrs...); err != nil {
			scanErr = err.Error()
			break
		}
		row := make([]any, len(cols))
		for i := range cols {
			row[i] = sqlCellValue(vals[i], mysqlStyle)
		}
		batch = append(batch, row)
		count++
		if len(batch) >= sqlStreamBatchRows {
			if !emit(map[string]any{"type": "rows", "rows": batch}) {
				return // 客户端走了：ctx 会取消查询
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 && !emit(map[string]any{"type": "rows", "rows": batch}) {
		return
	}
	if scanErr == "" && rs.Err() != nil {
		scanErr = sqlFriendlyError(rs.Err(), ctx)
	}
	fetchMs := time.Since(fetchStart).Milliseconds()
	end := map[string]any{
		"type": "end", "row_count": count, "truncated": truncated,
		"exec_ms": execMs, "fetch_ms": fetchMs, "total_ms": time.Since(totalStart).Milliseconds(),
	}
	if scanErr != "" {
		end["error"] = scanErr
	}
	emit(end)
	if scanErr == "" {
		s.recordSQLHistory(r, "query", id, req.SQL, nil)
	}
}

// handleSQLQueryExport 以 CSV 流式导出查询结果。
// POST /api/v1/sql/connections/{id}/query/export
//
// 与流式查询同源，区别只在于：行上限单独放宽、结果直接写成 CSV。整个过程服务端不留
// 完整结果——这是"大结果集导出"唯一站得住的做法。
func (s *Server) handleSQLQueryExport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	c, ok := s.cfg.GetMySQLConnection(id)
	if err := mysqlConnReady(c, ok); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	var req sqlReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	limit := clampSQLExportLimit(req.Limit)
	// 导出通常比交互查询跑得久，允许更长的超时（仍有上限）。
	timeout := clampSQLQueryTimeout(req.TimeoutSec)
	if req.TimeoutSec <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	sess, err := prepareSQLRead(ctx, c, req, limit, timeout)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	defer sess.close()

	rs, err := sess.conn.QueryContext(ctx, sess.execSQL)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": sqlFriendlyError(err, ctx)})
		return
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	name := sanitizeExportFilename(req.Filename)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// UTF-8 BOM：Excel 不带 BOM 打开中文 CSV 就是乱码，这是导出功能最常见的投诉。
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = neutralizeCSVFormula(c)
	}
	_ = cw.Write(header)
	flusher, _ := w.(http.Flusher)

	mysqlStyle := sess.driver == "mysql"
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	line := make([]string, len(cols))
	count := 0
	truncated := false
	failure := ""
	for rs.Next() {
		if count >= limit {
			truncated = true
			break
		}
		if err := rs.Scan(ptrs...); err != nil {
			// 与下面的 rs.Err() 走同一套翻译：中途超时最常见的表现就是 Scan 失败，
			// 直接把驱动原文写进文件既不好懂，也和接口上的报错对不上。
			failure = sqlFriendlyError(err, ctx)
			break
		}
		for i := range cols {
			v := sqlCellValue(vals[i], mysqlStyle)
			if v == nil {
				line[i] = ""
				continue
			}
			line[i] = neutralizeCSVFormula(fmt.Sprint(v))
		}
		if err := cw.Write(line); err != nil {
			return
		}
		count++
		if count%2000 == 0 {
			cw.Flush()
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	if failure == "" && rs.Err() != nil {
		failure = sqlFriendlyError(rs.Err(), ctx)
	}
	// 响应头早就发出去了，改不了状态码。但一份**看起来完整**的残缺 CSV 比一个错误
	// 危险得多——报表会被当成全量数据去做判断。所以在文件末尾留一条显式标记行。
	if failure != "" || truncated {
		note := fmt.Sprintf("%s 导出在第 %d 行结束", csvIncompleteMarker, count)
		if truncated {
			note = fmt.Sprintf("%s 已达导出上限 %d 行，结果不完整", csvIncompleteMarker, limit)
		}
		if failure != "" {
			note += "：" + failure
		}
		tail := make([]string, len(cols))
		if len(tail) == 0 {
			tail = []string{""}
		}
		tail[0] = neutralizeCSVFormula(note)
		_ = cw.Write(tail)
		slog.Warn("SQL 导出未完整结束", "conn", id, "rows", count, "truncated", truncated, "err", failure)
	}
	cw.Flush()
	if flusher != nil {
		flusher.Flush()
	}
	s.recordSQLHistory(r, "export", id, req.SQL, nil)
}

// csvIncompleteMarker 标记导出被截断 / 中途出错。以 # 开头，既不像公式也不像数据。
const csvIncompleteMarker = "#AIOPS_EXPORT_INCOMPLETE"

// rePlainNumber 是"这就是个数字"的判据：带符号的整数/小数。
var rePlainNumber = regexp.MustCompile(`^[+-]?\d+(\.\d+)?$`)

// neutralizeCSVFormula 防电子表格公式注入（CWE-1236）。
//
// 导出的每一格都是**业务库里的任意内容**——比 Web 端导出的告警文本更不可控。
// Excel / LibreOffice / Numbers / WPS 打开 CSV 时，以 = + - @ 开头（以及被解析前
// 就会被剥掉的前导 Tab / CR）的单元格会被当公式执行，`=cmd|'...'!A1` 这类载荷
// 可以在打开报表的运维同事机器上落地执行。前缀一个单引号让它变回字面文本。
//
// 例外：纯数字（"-12"、"+3.5"）是数据不是公式，加前缀会把数值列毁掉——
// 与 Web 端 rowsToCSV / Android HyperVExport 保持同一套判据。
func neutralizeCSVFormula(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
	default:
		return s
	}
	if rePlainNumber.MatchString(s) {
		return s
	}
	return "'" + s
}

// sanitizeExportFilename 把用户给的文件名收敛成安全的 ASCII 文件名。
// 文件名要进 Content-Disposition 头：换行或引号能把响应头整个撬开。
func sanitizeExportFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "sql-result-" + time.Now().Format("20060102-150405")
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 80 {
			break
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		out = "sql-result"
	}
	if !strings.HasSuffix(strings.ToLower(out), ".csv") {
		out += ".csv"
	}
	return out
}

// sqlFriendlyError 把驱动错误翻成用户能据以行动的话。
//
// 超时/取消尤其要说清楚：光甩一句 "context deadline exceeded" 会让人以为是面板坏了，
// 而实际动作是"把超时调大，或者给查询加索引/加 LIMIT"。
func sqlFriendlyError(err error, ctx context.Context) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return "查询超时已被终止：" + msg + "（可调大超时，或先用 EXPLAIN 看看是否缺索引 / 加上更严格的过滤条件）"
	case context.Canceled:
		return "查询已取消：" + msg
	}
	if strings.Contains(msg, "context canceled") {
		return "查询已取消"
	}
	return msg
}
