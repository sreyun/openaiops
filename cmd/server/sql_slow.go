package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"aiops-monitor/cmd/server/sqltoolkit"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// SlowSQLReport is one collection+advice run for a MySQL connection.
type SlowSQLReport struct {
	ID             string              `json:"id"`
	ConnectionID   string              `json:"connection_id"`
	ConnectionName string              `json:"connection_name,omitempty"`
	Trigger        string              `json:"trigger,omitempty"` // manual | schedule
	Source         string              `json:"source"`            // performance_schema
	Status         string              `json:"status"`            // running | completed | failed
	Error          string              `json:"error,omitempty"`
	StartedAt      int64               `json:"started_at"`
	FinishedAt     int64               `json:"finished_at,omitempty"`
	ItemCount      int                 `json:"item_count"`
	Items          []SlowSQLItem       `json:"items"`
	Trend          *SlowSQLDigestTrend `json:"trend,omitempty"`
	// PSLimits: probed MySQL performance_schema text length settings (MySQL only).
	PSLimits *SlowSQLPSLimits `json:"ps_limits,omitempty"`
}

// SlowSQLPSLimits holds MySQL digest / SQL_TEXT length caps and remediation hints.
type SlowSQLPSLimits struct {
	MaxDigestLength int    `json:"max_digest_length"`
	MaxSQLTextLength int   `json:"performance_schema_max_sql_text_length"`
	RemedySQL       string `json:"remedy_sql,omitempty"`
	RemedyNote      string `json:"remedy_note,omitempty"`
}

// SlowSQLDigestTrend compares digests against the previous completed report.
type SlowSQLDigestTrend struct {
	PreviousReportID string   `json:"previous_report_id,omitempty"`
	NewDigests       int      `json:"new_digests"`
	GoneDigests      int      `json:"gone_digests"`
	Worsened         int      `json:"worsened"`
	Improved         int      `json:"improved"`
	SamplesNew       []string `json:"samples_new,omitempty"`
	SamplesWorse     []string `json:"samples_worse,omitempty"`
}

// SlowSQLItem is one digest with metrics and rule-engine advice.
type SlowSQLItem struct {
	Schema       string                 `json:"schema,omitempty"`
	Digest       string                 `json:"digest,omitempty"`
	SQL          string                 `json:"sql"`
	CountStar    int64                  `json:"count_star"`
	SumLatencyMs float64                `json:"sum_latency_ms"`
	AvgLatencyMs float64                `json:"avg_latency_ms"`
	MaxLatencyMs float64                `json:"max_latency_ms"`
	FirstSeen    string                 `json:"first_seen,omitempty"`
	LastSeen     string                 `json:"last_seen,omitempty"`
	Score        int                    `json:"score"`
	Findings     []sqltoolkit.Finding   `json:"findings,omitempty"`
	Suggestions  []sqltoolkit.Finding   `json:"suggestions,omitempty"`
	IndexHints   []sqltoolkit.IndexHint `json:"index_hints,omitempty"`
	RewrittenSQL string                 `json:"rewritten_sql,omitempty"`
	ExplainUsed  bool                   `json:"explain_used"`
	MetadataUsed bool                   `json:"metadata_used"`
	AnalyzeError string                 `json:"analyze_error,omitempty"`
	// Trend: new|worse|better|same (vs previous completed report).
	Trend string `json:"trend,omitempty"`
	// SQLTruncated: DIGEST_TEXT/SQL_TEXT hit performance_schema length limits.
	SQLTruncated bool `json:"sql_truncated,omitempty"`
	// SQLRecovered: SQL was replaced with a longer sample from statement history.
	SQLRecovered bool `json:"sql_recovered,omitempty"`
	// ParamsUnresolved: SQL still has ? / $n after recovery attempt (digest-only).
	ParamsUnresolved bool `json:"params_unresolved,omitempty"`
	// RecoverySource: history_long|history|current|slow_log|processlist|cache
	RecoverySource string `json:"recovery_source,omitempty"`
	// SchemaInferred: SCHEMA_NAME was empty; filled from SQL qualifiers / information_schema.
	SchemaInferred bool `json:"schema_inferred,omitempty"`
}

type slowDigestRow struct {
	Schema       string
	Digest       string
	SQL          string
	CountStar    int64
	SumLatencyMs float64
	AvgLatencyMs float64
	MaxLatencyMs float64
	FirstSeen    string
	LastSeen     string
	Truncated    bool
	Recovered    bool
	SchemaInfer  bool
	RecoverSrc   string
}

type slowSQLManager struct {
	mu       sync.Mutex
	dir      string
	latest   map[string]*SlowSQLReport // connectionID -> latest
	history  []*SlowSQLReport          // ring, newest first
	lastRun  map[string]int64          // schedule bookkeeping
	inflight map[string]bool
	digestFT *sqlDigestFulltextCache
}

const slowSQLHistoryCap = 40

func newSlowSQLManager(dir string) *slowSQLManager {
	m := &slowSQLManager{
		dir:      dir,
		latest:   map[string]*SlowSQLReport{},
		history:  make([]*SlowSQLReport, 0, 16),
		lastRun:  map[string]int64{},
		inflight: map[string]bool{},
		digestFT: newSQLDigestFulltextCache(dir),
	}
	m.load()
	return m
}

func (m *slowSQLManager) pathLatest(connID string) string {
	return filepath.Join(m.dir, "latest-"+sanitizeFilePart(connID)+".json")
}

func (m *slowSQLManager) pathHistory() string {
	return filepath.Join(m.dir, "history.json")
}

func sanitizeFilePart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

func (m *slowSQLManager) load() {
	_ = os.MkdirAll(m.dir, 0o750)
	entries, _ := os.ReadDir(m.dir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "latest-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.dir, name))
		if err != nil {
			continue
		}
		var rep SlowSQLReport
		if json.Unmarshal(b, &rep) != nil || rep.ConnectionID == "" {
			continue
		}
		cp := rep
		m.latest[rep.ConnectionID] = &cp
	}
	if b, err := os.ReadFile(m.pathHistory()); err == nil {
		var hist []*SlowSQLReport
		if json.Unmarshal(b, &hist) == nil {
			m.history = hist
		}
	}
}

func (m *slowSQLManager) saveLatestLocked(rep *SlowSQLReport) {
	if rep == nil || rep.ConnectionID == "" {
		return
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(m.dir, 0o750)
	_ = os.WriteFile(m.pathLatest(rep.ConnectionID), b, 0o640)
}

func (m *slowSQLManager) saveHistoryLocked() {
	if len(m.history) > slowSQLHistoryCap {
		m.history = m.history[:slowSQLHistoryCap]
	}
	b, err := json.MarshalIndent(m.history, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(m.dir, 0o750)
	_ = os.WriteFile(m.pathHistory(), b, 0o640)
}

func (m *slowSQLManager) begin(connID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight[connID] {
		return false
	}
	m.inflight[connID] = true
	return true
}

func (m *slowSQLManager) end(connID string) {
	m.mu.Lock()
	delete(m.inflight, connID)
	m.mu.Unlock()
}

func (m *slowSQLManager) store(rep *SlowSQLReport) {
	if rep == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *rep
	if cp.Items == nil {
		cp.Items = []SlowSQLItem{}
	}
	// Avoid stacking duplicate "running" placeholders in the history ring.
	if cp.Status == "running" {
		m.latest[cp.ConnectionID] = &cp
		m.saveLatestLocked(&cp)
		return
	}
	m.latest[cp.ConnectionID] = &cp
	hist := cp
	m.history = append([]*SlowSQLReport{&hist}, m.history...)
	m.saveLatestLocked(&cp)
	m.saveHistoryLocked()
}

func (m *slowSQLManager) previousCompleted(connID, excludeID string) *SlowSQLReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.history {
		if h == nil || h.ConnectionID != connID || h.Status != "completed" || h.ID == excludeID {
			continue
		}
		cp := *h
		return &cp
	}
	if cur := m.latest[connID]; cur != nil && cur.Status == "completed" && cur.ID != excludeID {
		cp := *cur
		return &cp
	}
	return nil
}

func slowDigestKey(it SlowSQLItem) string {
	d := strings.TrimSpace(it.Digest)
	if d != "" {
		return strings.ToLower(d)
	}
	return strings.ToLower(strings.TrimSpace(it.Schema)) + "|" + strings.ToLower(strings.TrimSpace(it.SQL))
}

func attachSlowSQLTrend(prev, cur *SlowSQLReport) {
	if cur == nil || cur.Status != "completed" {
		return
	}
	if prev == nil || prev.Status != "completed" {
		return
	}
	trend := &SlowSQLDigestTrend{PreviousReportID: prev.ID}
	prevMap := map[string]SlowSQLItem{}
	for _, it := range prev.Items {
		prevMap[slowDigestKey(it)] = it
	}
	curMap := map[string]bool{}
	for i := range cur.Items {
		it := &cur.Items[i]
		k := slowDigestKey(*it)
		curMap[k] = true
		old, ok := prevMap[k]
		if !ok {
			it.Trend = "new"
			trend.NewDigests++
			if len(trend.SamplesNew) < 5 {
				trend.SamplesNew = append(trend.SamplesNew, truncateRun(it.SQL, 80))
			}
			continue
		}
		// Significant latency change: >20% and at least 20ms absolute.
		delta := it.AvgLatencyMs - old.AvgLatencyMs
		thresh := old.AvgLatencyMs * 0.2
		if thresh < 20 {
			thresh = 20
		}
		switch {
		case delta >= thresh:
			it.Trend = "worse"
			trend.Worsened++
			if len(trend.SamplesWorse) < 5 {
				trend.SamplesWorse = append(trend.SamplesWorse, truncateRun(it.SQL, 80))
			}
		case delta <= -thresh:
			it.Trend = "better"
			trend.Improved++
		default:
			it.Trend = "same"
		}
	}
	for k := range prevMap {
		if !curMap[k] {
			trend.GoneDigests++
		}
	}
	cur.Trend = trend
}

func (m *slowSQLManager) getLatest(connID string) *SlowSQLReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	rep := m.latest[connID]
	if rep == nil {
		return nil
	}
	cp := *rep
	return &cp
}

func (m *slowSQLManager) listLatest() []*SlowSQLReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*SlowSQLReport, 0, len(m.latest))
	for _, r := range m.latest {
		if r == nil {
			continue
		}
		cp := *r
		cp.Items = nil // overview omits heavy items
		out = append(out, &cp)
	}
	return out
}

func mysqlDSNSlow(c MySQLConnection) string {
	// Same as mysqlDSN but longer read timeout for digest scans.
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
	cfg.Timeout = 8 * time.Second
	cfg.ReadTimeout = 60 * time.Second
	cfg.WriteTimeout = 10 * time.Second
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

func mysqlOpenSlow(c MySQLConnection) (*sql.DB, error) {
	db, err := sql.Open("mysql", mysqlDSNSlow(c))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(3 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func picosecondsToMs(ps float64) float64 {
	return ps / 1e9
}

func formatSQLTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return ""
		}
		return t.Format(time.RFC3339)
	case []byte:
		return string(t)
	case string:
		return t
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func humanizeSlowSQLErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "performance_schema") && (strings.Contains(low, "doesn't exist") || strings.Contains(low, "unknown table") || strings.Contains(low, "1146")):
		return "无法读取 performance_schema.events_statements_summary_by_digest。请确认已启用 performance_schema，且账号具备 SELECT 权限。示例：SET GLOBAL performance_schema=ON; GRANT SELECT ON performance_schema.* TO 'user'@'%';"
	case strings.Contains(low, "access denied") || strings.Contains(low, "1045") || strings.Contains(low, "1142"):
		return "账号权限不足，无法查询 performance_schema。请授予 SELECT ON performance_schema.*（建议只读账号）。"
	default:
		return "慢 SQL 采集失败：" + truncateRun(msg, 240)
	}
}

// buildSlowDigestQuery builds the P_S digest query. Exported logic kept testable via helpers.
func buildSlowDigestQuery(exclude []string, minAvgMs float64, topN int) (string, []any) {
	if topN <= 0 {
		topN = 30
	}
	if topN > 100 {
		topN = 100
	}
	if minAvgMs < 0 {
		minAvgMs = 0
	}
	ex := exclude
	if len(ex) == 0 {
		ex = defaultSlowSQLExcludeSchemas()
	}
	ph := make([]string, 0, len(ex))
	args := make([]any, 0, len(ex)+2)
	for _, s := range ex {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ph = append(ph, "?")
		args = append(args, s)
	}
	// AVG_TIMER_WAIT is picoseconds; convert threshold ms → ps.
	args = append(args, minAvgMs*1e9, topN)
	whereEx := "1=1"
	if len(ph) > 0 {
		whereEx = "(SCHEMA_NAME IS NULL OR SCHEMA_NAME NOT IN (" + strings.Join(ph, ",") + "))"
	}
	q := `
SELECT SCHEMA_NAME, DIGEST, DIGEST_TEXT, COUNT_STAR,
       SUM_TIMER_WAIT, AVG_TIMER_WAIT, MAX_TIMER_WAIT,
       FIRST_SEEN, LAST_SEEN
FROM performance_schema.events_statements_summary_by_digest
WHERE DIGEST_TEXT IS NOT NULL
  AND ` + whereEx + `
  AND AVG_TIMER_WAIT >= ?
ORDER BY SUM_TIMER_WAIT DESC
LIMIT ?`
	return strings.TrimSpace(q), args
}

func mysqlCollectSlowDigestsWithCache(c MySQLConnection, cfg *SlowSQLMonitorConfig, cache *sqlDigestFulltextCache) ([]slowDigestRow, error) {
	cfg = cfg.withDefaults()
	db, err := mysqlOpenSlow(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	maxDigest, maxSQLText := mysqlPSTextLimits(ctx, db)

	q, args := buildSlowDigestQuery(cfg.ExcludeSchemas, cfg.MinAvgLatencyMs, cfg.TopN)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("%s", humanizeSlowSQLErr(err))
	}
	defer rows.Close()

	out := make([]slowDigestRow, 0, cfg.TopN)
	for rows.Next() {
		var schema, digest sql.NullString
		var digText string
		var count int64
		var sumPS, avgPS, maxPS float64
		var first, last any
		if err := rows.Scan(&schema, &digest, &digText, &count, &sumPS, &avgPS, &maxPS, &first, &last); err != nil {
			continue
		}
		digText = strings.TrimSpace(digText)
		if digText == "" {
			continue
		}
		row := slowDigestRow{
			Schema:       strings.TrimSpace(schema.String),
			Digest:       strings.TrimSpace(digest.String),
			SQL:          digText,
			CountStar:    count,
			SumLatencyMs: picosecondsToMs(sumPS),
			AvgLatencyMs: picosecondsToMs(avgPS),
			MaxLatencyMs: picosecondsToMs(maxPS),
			FirstSeen:    formatSQLTime(first),
			LastSeen:     formatSQLTime(last),
		}
		enrichSlowDigestSQL(ctx, db, &row, maxDigest, maxSQLText, cfg.ExcludeSchemas, c.ID, cache)
		out = append(out, row)
	}
	return out, rows.Err()
}

func mysqlPSTextLimits(ctx context.Context, db *sql.DB) (maxDigest, maxSQLText int) {
	maxDigest, maxSQLText = 1024, 1024
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT @@global.max_digest_length").Scan(&v); err == nil && v.Valid && v.Int64 > 0 {
		maxDigest = int(v.Int64)
	}
	v = sql.NullInt64{}
	if err := db.QueryRowContext(ctx, "SELECT @@global.performance_schema_max_sql_text_length").Scan(&v); err == nil && v.Valid && v.Int64 > 0 {
		maxSQLText = int(v.Int64)
	}
	return maxDigest, maxSQLText
}

func sqlLikelyTruncated(text string, limit int) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if limit > 0 && len(text) >= limit-8 {
		return true
	}
	// Unbalanced quotes/parens or ends mid-token → almost certainly cut off.
	if strings.Count(text, "(") > strings.Count(text, ")") {
		return true
	}
	low := strings.ToLower(strings.TrimRight(text, " \t\r\n;"))
	for _, suffix := range []string{" from", " where", " and", " or", " join", " on", " select", " in (", " values (", ","} {
		if strings.HasSuffix(low, strings.TrimSpace(suffix)) || strings.HasSuffix(low, suffix) {
			return true
		}
	}
	return false
}

func enrichSlowDigestSQL(ctx context.Context, db *sql.DB, row *slowDigestRow, maxDigest, maxSQLText int, exclude []string, connID string, cache *sqlDigestFulltextCache) {
	if row == nil {
		return
	}
	// Prefer previously cached full text before hitting live P_S (avoids digest regression).
	if cache != nil && row.Digest != "" {
		if e, ok := cache.Get(connID, row.Digest); ok && shouldPreferRecoveredSQL(row.SQL, e.SQL) {
			row.SQL = e.SQL
			row.Recovered = true
			row.RecoverSrc = "cache"
			if e.Source != "" {
				row.RecoverSrc = "cache:" + e.Source
			}
		}
	}
	truncated := sqlLikelyTruncated(row.SQL, maxDigest)
	if !truncated {
		truncated = sqlLikelyTruncated(row.SQL, maxSQLText)
	}
	needRecover := truncated || len(row.SQL) < 40 || sqltoolkit.HasDigestPlaceholders(row.SQL)
	if row.Digest != "" && needRecover && db != nil {
		if full, src, ok := mysqlRecoverSQLByDigest(ctx, db, row.Digest, row.SQL); ok {
			full = strings.TrimSpace(full)
			if shouldPreferRecoveredSQL(row.SQL, full) {
				row.SQL = full
				row.Recovered = true
				row.RecoverSrc = src
			}
		}
	}
	row.Truncated = sqlLikelyTruncated(row.SQL, maxDigest) || sqlLikelyTruncated(row.SQL, maxSQLText) ||
		!sqltoolkit.ExtractQueryShape(row.SQL).ParseOK && sqlLikelyTruncated(row.SQL, 0)
	// Re-evaluate truncation after recovery using structural parse.
	if shape := sqltoolkit.ExtractQueryShape(row.SQL); shape != nil && shape.ParseOK {
		row.Truncated = false
	} else if row.Truncated || (shape != nil && shape.ParseError != "") {
		if sqlLikelyTruncated(row.SQL, maxDigest) || sqlLikelyTruncated(row.SQL, 0) {
			row.Truncated = true
		}
	}
	// Persist best sample when not truncated (or clearly longer than digest).
	if cache != nil && row.Digest != "" && row.SQL != "" {
		if !row.Truncated || row.Recovered {
			src := row.RecoverSrc
			if src == "" {
				src = "digest"
			}
			cache.Put(connID, row.Digest, row.SQL, src)
		}
	}
	if row.Schema == "" {
		if s := inferSchemaFromSQLText(row.SQL); s != "" {
			row.Schema = s
			row.SchemaInfer = true
		} else if names := tableNamesForSchemaLookup(row.SQL); len(names) > 0 {
			if s := mysqlInferSchemaFromTables(ctx, db, names, exclude); s != "" {
				row.Schema = s
				row.SchemaInfer = true
			}
		}
	}
}

// shouldPreferRecoveredSQL prefers history SQL_TEXT when it is longer or clears placeholders.
func shouldPreferRecoveredSQL(current, recovered string) bool {
	recovered = strings.TrimSpace(recovered)
	if recovered == "" {
		return false
	}
	curPH := sqltoolkit.HasDigestPlaceholders(current)
	recPH := sqltoolkit.HasDigestPlaceholders(recovered)
	if curPH && !recPH {
		return true
	}
	if !curPH && recPH {
		return false
	}
	return len(recovered) > len(current)
}

type sqlRecoverCandidate struct {
	SQL    string
	Source string
}

func mysqlRecoverSQLByDigest(ctx context.Context, db *sql.DB, digest, digestText string) (string, string, bool) {
	digest = strings.TrimSpace(digest)
	if digest == "" || db == nil {
		return "", "", false
	}
	var best sqlRecoverCandidate
	consider := func(text, source string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		// Never prefer another digest-shaped sample over current digest text.
		if sqltoolkit.HasDigestPlaceholders(text) && !sqltoolkit.HasDigestPlaceholders(best.SQL) && best.SQL != "" {
			return
		}
		if best.SQL == "" || shouldPreferRecoveredSQL(best.SQL, text) {
			best = sqlRecoverCandidate{SQL: text, Source: source}
		}
	}

	// 1) P_S statement history / current — take several longest samples and pick
	// the one that clears DIGEST '?' placeholders when possible.
	psQueries := []struct {
		src string
		q   string
	}{
		{"history_long", `SELECT SQL_TEXT FROM performance_schema.events_statements_history_long
		 WHERE DIGEST = ? AND SQL_TEXT IS NOT NULL AND SQL_TEXT <> ''
		 ORDER BY CHAR_LENGTH(SQL_TEXT) DESC LIMIT 30`},
		{"history", `SELECT SQL_TEXT FROM performance_schema.events_statements_history
		 WHERE DIGEST = ? AND SQL_TEXT IS NOT NULL AND SQL_TEXT <> ''
		 ORDER BY CHAR_LENGTH(SQL_TEXT) DESC LIMIT 30`},
		{"current", `SELECT SQL_TEXT FROM performance_schema.events_statements_current
		 WHERE DIGEST = ? AND SQL_TEXT IS NOT NULL AND SQL_TEXT <> ''
		 ORDER BY CHAR_LENGTH(SQL_TEXT) DESC LIMIT 20`},
	}
	for _, item := range psQueries {
		rows, err := db.QueryContext(ctx, item.q, digest)
		if err != nil {
			// Older MySQL may lack history_long; fall back to single-row query.
			var text string
			if err2 := db.QueryRowContext(ctx, strings.Replace(item.q, "LIMIT 30", "LIMIT 1", 1), digest).Scan(&text); err2 == nil {
				consider(text, item.src)
			} else if err2 := db.QueryRowContext(ctx, strings.Replace(item.q, "LIMIT 20", "LIMIT 1", 1), digest).Scan(&text); err2 == nil {
				consider(text, item.src)
			}
			continue
		}
		func() {
			defer rows.Close()
			for rows.Next() {
				var text string
				if rows.Scan(&text) != nil {
					continue
				}
				consider(text, item.src)
				// Early stop once we have a full literal sample.
				if best.SQL != "" && !sqltoolkit.HasDigestPlaceholders(best.SQL) {
					return
				}
			}
		}()
		if best.SQL != "" && !sqltoolkit.HasDigestPlaceholders(best.SQL) {
			break
		}
	}

	// 2) mysql.slow_log (TABLE output) via STATEMENT_DIGEST when available (MySQL 8+).
	rowsSL, errSL := db.QueryContext(ctx, `
		SELECT sql_text FROM mysql.slow_log
		 WHERE sql_text IS NOT NULL AND sql_text <> ''
		   AND STATEMENT_DIGEST(sql_text) = ?
		 ORDER BY CHAR_LENGTH(sql_text) DESC LIMIT 10`, digest)
	if errSL == nil {
		func() {
			defer rowsSL.Close()
			for rowsSL.Next() {
				var text string
				if rowsSL.Scan(&text) != nil {
					continue
				}
				consider(text, "slow_log")
				if best.SQL != "" && !sqltoolkit.HasDigestPlaceholders(best.SQL) {
					return
				}
			}
		}()
	} else {
		var text string
		err := db.QueryRowContext(ctx, `
			SELECT sql_text FROM mysql.slow_log
			 WHERE sql_text IS NOT NULL AND sql_text <> ''
			   AND STATEMENT_DIGEST(sql_text) = ?
			 ORDER BY CHAR_LENGTH(sql_text) DESC LIMIT 1`, digest).Scan(&text)
		if err == nil {
			consider(text, "slow_log")
		}
	}

	// 3) Weak PROCESSLIST match: longest INFO sharing a stable prefix with digest text.
	prefix := sqlRecoverPrefix(digestText)
	if len(prefix) >= 16 {
		rows, qerr := db.QueryContext(ctx, `
			SELECT INFO FROM information_schema.PROCESSLIST
			 WHERE INFO IS NOT NULL AND INFO <> ''
			 ORDER BY CHAR_LENGTH(INFO) DESC LIMIT 80`)
		if qerr == nil {
			func() {
				defer rows.Close()
				plow := strings.ToLower(prefix)
				for rows.Next() {
					var info string
					if rows.Scan(&info) != nil {
						continue
					}
					info = strings.TrimSpace(info)
					if sqltoolkit.HasDigestPlaceholders(info) {
						continue
					}
					low := strings.ToLower(info)
					if strings.HasPrefix(low, plow) || strings.Contains(low, plow) {
						consider(info, "processlist")
						if !sqltoolkit.HasDigestPlaceholders(best.SQL) {
							break
						}
					}
				}
			}()
		}
	}

	if best.SQL == "" {
		return "", "", false
	}
	if !shouldPreferRecoveredSQL(digestText, best.SQL) {
		return "", "", false
	}
	return best.SQL, best.Source, true
}

func sqlRecoverPrefix(digestText string) string {
	s := strings.TrimSpace(digestText)
	if s == "" {
		return ""
	}
	// DIGEST_TEXT uses '?' for every string literal; strip those markers so the
	// remaining skeleton can match live SQL_TEXT / PROCESSLIST INFO.
	s = strings.ReplaceAll(s, "'?'", "")
	s = strings.ReplaceAll(s, `"?"`, "")
	cut := strings.IndexByte(s, '?')
	if cut > 0 {
		s = s[:cut]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, " \t=<>(,")
	if len(s) > 96 {
		s = s[:96]
	}
	return s
}

func buildSlowSQLPSLimits(maxDigest, maxSQLText int) *SlowSQLPSLimits {
	if maxDigest <= 0 {
		maxDigest = 1024
	}
	if maxSQLText <= 0 {
		maxSQLText = 1024
	}
	target := 8192
	if maxDigest >= target && maxSQLText >= target {
		return &SlowSQLPSLimits{
			MaxDigestLength:  maxDigest,
			MaxSQLTextLength: maxSQLText,
			RemedyNote:       "当前限额已≥8192；若仍截断请检查语句历史是否已刷掉，或粘贴完整 SQL。",
		}
	}
	return &SlowSQLPSLimits{
		MaxDigestLength:  maxDigest,
		MaxSQLTextLength: maxSQLText,
		RemedySQL: "SET PERSIST max_digest_length=8192;\n" +
			"SET PERSIST performance_schema_max_sql_text_length=8192;\n" +
			"-- 多数版本需重启 mysqld 后生效，然后重新采集慢 SQL",
		RemedyNote: "提高限额并重启 MySQL 后重新采集，可显著降低 DIGEST/SQL_TEXT 截断。产品不会自动「编造」缺失尾部。",
	}
}

var reQualifiedTable = regexp.MustCompile(`(?i)(?:from|join|update|into|table)\s+(?:` +
	"`([a-zA-Z0-9_]+)`|([a-zA-Z0-9_]+))" +
	`\s*\.\s*` +
	"(?:`([a-zA-Z0-9_]+)`|([a-zA-Z0-9_]+))")

func inferSchemaFromSQLText(sqlText string) string {
	shape := sqltoolkit.ExtractQueryShape(sqlText)
	if shape != nil {
		if s := shape.DominantSchema(); s != "" {
			return s
		}
	}
	counts := map[string]int{}
	best, bestN := "", 0
	for _, m := range reQualifiedTable.FindAllStringSubmatch(sqlText, -1) {
		s := m[1]
		if s == "" {
			s = m[2]
		}
		s = strings.TrimSpace(s)
		if s == "" || isSystemSchemaName(s) {
			continue
		}
		counts[s]++
		if counts[s] > bestN {
			best, bestN = s, counts[s]
		}
	}
	return best
}

func tableNamesForSchemaLookup(sqlText string) []string {
	shape := sqltoolkit.ExtractQueryShape(sqlText)
	if shape != nil && shape.ParseOK {
		return shape.TableNames()
	}
	// Fallback: bare table after FROM/JOIN when AST fails (truncated SQL).
	re := regexp.MustCompile(`(?i)(?:from|join)\s+(?:` + "`([a-zA-Z0-9_]+)`|([a-zA-Z0-9_]+))" + `(?:\s|\z|,|\)|)`)
	out := []string{}
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(sqlText, -1) {
		n := m[1]
		if n == "" {
			n = m[2]
		}
		n = strings.TrimSpace(n)
		if n == "" || seen[strings.ToLower(n)] || isSystemSchemaName(n) {
			continue
		}
		// Skip if this match is the schema part of schema.table (handled elsewhere).
		seen[strings.ToLower(n)] = true
		out = append(out, n)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func isSystemSchemaName(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mysql", "information_schema", "performance_schema", "sys", "pg_catalog", "pg_toast":
		return true
	default:
		return false
	}
}

func mysqlInferSchemaFromTables(ctx context.Context, db *sql.DB, tables, exclude []string) string {
	names := make([]string, 0, len(tables))
	for _, t := range tables {
		t = strings.TrimSpace(t)
		if t == "" || !reSafeIdent.MatchString(t) {
			continue
		}
		names = append(names, t)
	}
	if len(names) == 0 {
		return ""
	}
	ex := exclude
	if len(ex) == 0 {
		ex = defaultSlowSQLExcludeSchemas()
	}
	phTables := make([]string, len(names))
	args := make([]any, 0, len(names)+len(ex))
	for i, n := range names {
		phTables[i] = "?"
		args = append(args, n)
	}
	phEx := make([]string, 0, len(ex))
	for _, s := range ex {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		phEx = append(phEx, "?")
		args = append(args, s)
	}
	whereEx := "1=1"
	if len(phEx) > 0 {
		whereEx = "TABLE_SCHEMA NOT IN (" + strings.Join(phEx, ",") + ")"
	}
	q := `SELECT TABLE_SCHEMA, COUNT(DISTINCT TABLE_NAME) AS cnt
FROM information_schema.TABLES
WHERE TABLE_NAME IN (` + strings.Join(phTables, ",") + `)
  AND ` + whereEx + `
GROUP BY TABLE_SCHEMA
ORDER BY cnt DESC, TABLE_SCHEMA ASC
LIMIT 1`
	var schema string
	var cnt int
	if err := db.QueryRowContext(ctx, q, args...).Scan(&schema, &cnt); err != nil || schema == "" {
		return ""
	}
	return schema
}

func dialectForConn(c MySQLConnection) sqltoolkit.Dialect {
	if driverOf(c) == "postgres" {
		return sqltoolkit.DialectPostgres
	}
	switch c.VersionHint {
	case "mysql57":
		return sqltoolkit.DialectMySQL57
	case "mysql80":
		return sqltoolkit.DialectMySQL80
	default:
		return sqltoolkit.DialectMySQL80
	}
}

func analyzeSlowDigest(c MySQLConnection, row slowDigestRow) SlowSQLItem {
	item := SlowSQLItem{
		Schema:         row.Schema,
		Digest:         row.Digest,
		SQL:            row.SQL,
		CountStar:      row.CountStar,
		SumLatencyMs:   row.SumLatencyMs,
		AvgLatencyMs:   row.AvgLatencyMs,
		MaxLatencyMs:   row.MaxLatencyMs,
		FirstSeen:      row.FirstSeen,
		LastSeen:       row.LastSeen,
		Score:          100,
		SQLTruncated:     row.Truncated,
		SQLRecovered:     row.Recovered,
		ParamsUnresolved: sqltoolkit.HasDigestPlaceholders(row.SQL),
		RecoverySource:   row.RecoverSrc,
		SchemaInferred:   row.SchemaInfer,
	}
	d := dialectForConn(c)
	in := sqltoolkit.AnalyzeInput{SQL: row.SQL, Dialect: d}

	kw := sqltoolkit.FirstKeyword(row.SQL)
	canExplain := kw == "select" || kw == "with"
	if canExplain && !row.Truncated && !sqltoolkit.ForbiddenWrite(row.SQL) {
		if driverOf(c) == "postgres" {
			// pg_stat_statements digests may contain $1 placeholders — EXPLAIN may fail.
			if expl, err := pgExplain(c, row.SQL); err == nil {
				if a, ok := expl["analysis"].(*sqltoolkit.ExplainAnalysis); ok {
					in.Explain = a
				}
			}
		} else {
			shape := sqltoolkit.ExtractQueryShape(row.SQL)
			if shape != nil && shape.ParseOK {
				if meta, err := mysqlFetchMetadataInSchema(c, row.Schema, shape.TableNames()); err == nil {
					in.Meta = meta
				}
			}
			if expl, err := mysqlExplainInSchema(c, row.Schema, row.SQL); err == nil {
				if a, ok := expl["analysis"].(*sqltoolkit.ExplainAnalysis); ok {
					in.Explain = a
				}
			}
		}
	}
	if row.Truncated {
		item.AnalyzeError = "SQL 文本可能被 performance_schema 截断（max_digest_length / performance_schema_max_sql_text_length）。请提高限额并重启后重采，或粘贴完整 SQL；无法从摘要编造缺失尾部。"
	}

	res := sqltoolkit.Analyze(in)
	item.Score = res.Score
	item.Findings = res.Findings
	item.Suggestions = res.Suggestions
	item.IndexHints = res.IndexHints
	item.RewrittenSQL = res.RewrittenSQL
	item.ExplainUsed = res.ExplainUsed
	item.MetadataUsed = res.MetadataUsed
	return item
}

func (s *Server) runSlowSQLCollect(connID, trigger string) (*SlowSQLReport, error) {
	c, ok := s.cfg.GetMySQLConnection(connID)
	if err := mysqlConnReady(c, ok); err != nil {
		return nil, err
	}
	if s.sqlSlow == nil {
		return nil, fmt.Errorf("slow sql manager not ready")
	}
	if !s.sqlSlow.begin(connID) {
		return nil, fmt.Errorf("该连接的慢 SQL 检查正在进行中")
	}
	defer s.sqlSlow.end(connID)

	cfg := c.SlowSQL.withDefaults()
	source := "performance_schema"
	if driverOf(c) == "postgres" {
		source = "pg_stat_statements"
	}
	rep := &SlowSQLReport{
		ID:             "ss-" + randomHex(6),
		ConnectionID:   c.ID,
		ConnectionName: c.Name,
		Trigger:        trigger,
		Source:         source,
		Status:         "running",
		StartedAt:      time.Now().Unix(),
		Items:          []SlowSQLItem{},
	}
	s.sqlSlow.store(rep)

	var rows []slowDigestRow
	var err error
	if driverOf(c) == "postgres" {
		rows, err = pgCollectSlowDigests(c, cfg)
	} else {
		var cache *sqlDigestFulltextCache
		if s.sqlSlow != nil {
			cache = s.sqlSlow.digestFT
		}
		// Probe limits for report remediation UI (best-effort).
		if db, oerr := mysqlOpenSlow(c); oerr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			md, mt := mysqlPSTextLimits(ctx, db)
			cancel()
			_ = db.Close()
			rep.PSLimits = buildSlowSQLPSLimits(md, mt)
		}
		rows, err = mysqlCollectSlowDigestsWithCache(c, cfg, cache)
	}
	if err != nil {
		rep.Status = "failed"
		rep.Error = err.Error()
		rep.FinishedAt = time.Now().Unix()
		s.sqlSlow.store(rep)
		return rep, err
	}

	items := make([]SlowSQLItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, analyzeSlowDigest(c, row))
	}
	rep.Items = items
	rep.ItemCount = len(items)
	rep.Status = "completed"
	rep.FinishedAt = time.Now().Unix()
	if prev := s.sqlSlow.previousCompleted(c.ID, rep.ID); prev != nil {
		attachSlowSQLTrend(prev, rep)
	}
	s.sqlSlow.store(rep)
	s.notifySlowSQLReport(c, rep)
	s.raiseSlowSQLTrendEarlyWarning(c, rep)
	return rep, nil
}

func (s *Server) startSlowSQLScheduler() {
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		// small delay so boot storm settles
		time.Sleep(8 * time.Second)
		for range t.C {
			s.tickSlowSQLSchedule()
		}
	}()
}

func (s *Server) tickSlowSQLSchedule() {
	if s.sqlSlow == nil {
		return
	}
	now := time.Now()
	for _, c := range s.cfg.ListMySQLConnections() {
		if !c.Enabled || c.SlowSQL == nil || !c.SlowSQL.Enabled {
			continue
		}
		sc := c.SlowSQL.withDefaults().Schedule
		if !slowSQLScheduleDue(sc, s.sqlSlow, c.ID, now) {
			continue
		}
		connID := c.ID
		go func() {
			if _, err := s.runSlowSQLCollect(connID, "schedule"); err != nil {
				slog.Info("slow sql scheduled run", "connection_id", connID, "err", err.Error())
			}
		}()
	}
}

func slowSQLScheduleDue(sc *PlaybookSchedule, m *slowSQLManager, connID string, now time.Time) bool {
	if sc == nil || !sc.Enabled || m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight[connID] {
		return false
	}
	key := connID
	last := m.lastRun[key]
	if last == 0 {
		if rep := m.latest[connID]; rep != nil && rep.FinishedAt > 0 {
			last = rep.FinishedAt
			m.lastRun[key] = last
		}
	}
	switch sc.Kind {
	case "interval":
		min := sc.IntervalMin
		if min < 15 {
			min = 15
		}
		if last > 0 && now.Unix()-last < int64(min)*60 {
			return false
		}
		m.lastRun[key] = now.Unix()
		return true
	case "daily":
		mins, ok := parseHHMM(sc.At)
		if !ok || now.Hour()*60+now.Minute() != mins {
			return false
		}
		day := now.Format("2006-01-02")
		dayKey := key + ":" + day
		if m.lastRun[dayKey] > 0 {
			return false
		}
		m.lastRun[dayKey] = now.Unix()
		m.lastRun[key] = now.Unix()
		return true
	case "weekly":
		mins, ok := parseHHMM(sc.At)
		if !ok || int(now.Weekday()) != sc.Weekday || now.Hour()*60+now.Minute() != mins {
			return false
		}
		y, w := now.ISOWeek()
		weekKey := fmt.Sprintf("%s:%d-W%02d", key, y, w)
		if m.lastRun[weekKey] > 0 {
			return false
		}
		m.lastRun[weekKey] = now.Unix()
		m.lastRun[key] = now.Unix()
		return true
	default:
		return false
	}
}
