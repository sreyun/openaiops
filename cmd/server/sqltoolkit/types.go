// Package sqltoolkit provides SQL beautify / audit / optimize helpers (MySQL + PostgreSQL).
package sqltoolkit

import "strings"

// Dialect selects engine-specific advice (MySQL versions or PostgreSQL).
type Dialect string

const (
	DialectMySQL57  Dialect = "mysql57"
	DialectMySQL80  Dialect = "mysql80"
	DialectPostgres Dialect = "postgres"
)

func NormalizeDialect(d string) Dialect {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "mysql80", "8", "8.0", "mysql8":
		return DialectMySQL80
	case "postgres", "postgresql", "pg", "pg14", "pg15", "pg16":
		return DialectPostgres
	default:
		return DialectMySQL57
	}
}

// Finding is one audit / optimize issue.
type Finding struct {
	ID      string `json:"id"`
	Level   string `json:"level"` // info | warn | crit
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Rule    string `json:"rule,omitempty"`
	Suggest string `json:"suggest,omitempty"`
}

// AuditResult is the output of Audit.
type AuditResult struct {
	Findings []Finding `json:"findings"`
	Score    int       `json:"score"` // 0-100, higher is better
	Parsed   bool      `json:"parsed,omitempty"`
}

// OptimizeResult is rewrite advice (optionally metadata-aware).
type OptimizeResult struct {
	RewrittenSQL string      `json:"rewritten_sql,omitempty"`
	Suggestions  []Finding   `json:"suggestions"`
	IndexHints   []IndexHint `json:"index_hints,omitempty"`
}

// IndexHint suggests a possible index.
type IndexHint struct {
	Table   string   `json:"table,omitempty"`
	Columns []string `json:"columns,omitempty"`
	Reason  string   `json:"reason"`
	DDL     string   `json:"ddl,omitempty"`
	Meta    bool     `json:"meta,omitempty"` // true when derived with live metadata
}

// PredicateKind classifies a WHERE/ON condition for index advice.
type PredicateKind string

const (
	PredEqual PredicateKind = "eq"
	PredRange PredicateKind = "range"
	PredLike  PredicateKind = "like"
	PredOther PredicateKind = "other"
)

// Predicate is a simplified column predicate from AST.
type Predicate struct {
	Table       string        `json:"table,omitempty"`
	Column      string        `json:"column"`
	Kind        PredicateKind `json:"kind"`
	FuncWrapped bool          `json:"func_wrapped,omitempty"`
	LeadingLike bool          `json:"leading_like,omitempty"`
	Literal     string        `json:"literal,omitempty"`
	LitIsString bool          `json:"lit_is_string,omitempty"`
}

// TableRef is a table (optionally aliased) referenced in the query.
type TableRef struct {
	Name   string `json:"name"`
	Schema string `json:"schema,omitempty"` // optional database/schema qualifier
	Alias  string `json:"alias,omitempty"`
}

// QueryShape is a Vitess-derived structural summary of one SQL statement.
type QueryShape struct {
	StmtType      string      `json:"stmt_type"` // select|update|delete|insert|other
	Tables        []TableRef  `json:"tables"`
	SelectStar    bool        `json:"select_star"`
	HasLimit      bool        `json:"has_limit"`
	HasWhere      bool        `json:"has_where"`
	HasOr         bool        `json:"has_or"`
	HasNotIn      bool        `json:"has_not_in"`
	OrderByRand   bool        `json:"order_by_rand"`
	HasJoin       bool        `json:"has_join"`
	JoinMissingOn bool        `json:"join_missing_on"`
	HasIndexHint  bool        `json:"has_index_hint"`
	IsCTE         bool        `json:"is_cte"`
	HasWindow     bool        `json:"has_window"`
	IntoOutfile   bool        `json:"into_outfile"`
	WherePreds    []Predicate `json:"where_preds,omitempty"`
	JoinPreds     []Predicate `json:"join_preds,omitempty"`
	OrderCols     []string    `json:"order_cols,omitempty"`
	GroupCols     []string    `json:"group_cols,omitempty"`
	MultiStmt     bool        `json:"multi_stmt"`
	ParseOK       bool        `json:"parse_ok"`
	ParseError    string      `json:"parse_error,omitempty"`
}

// ColumnMeta describes one column from information_schema.
type ColumnMeta struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Nullable bool   `json:"nullable"`
}

// IndexMeta is one index (ordered columns).
type IndexMeta struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Columns []string `json:"columns"`
}

// TableMeta is live schema/stats for one table.
type TableMeta struct {
	Name      string       `json:"name"`
	TableRows int64        `json:"table_rows"`
	AvgRowLen int64        `json:"avg_row_length,omitempty"`
	Columns   []ColumnMeta `json:"columns,omitempty"`
	Indexes   []IndexMeta  `json:"indexes,omitempty"`
}

// SchemaMeta is a map of lower(table) -> TableMeta.
type SchemaMeta map[string]*TableMeta

// ExplainHit is one table access node from EXPLAIN JSON.
type ExplainHit struct {
	Table         string  `json:"table,omitempty"`
	AccessType    string  `json:"access_type,omitempty"`
	Key           string  `json:"key,omitempty"`
	PossibleKeys  string  `json:"possible_keys,omitempty"`
	Rows          float64 `json:"rows,omitempty"`
	Filtered      float64 `json:"filtered,omitempty"`
	UsingIndex    bool    `json:"using_index,omitempty"`
	FullScanRisk  bool    `json:"full_scan_risk,omitempty"`
	UsingFilesort bool    `json:"using_filesort,omitempty"`
	UsingTemp     bool    `json:"using_temporary_table,omitempty"`
	Message       string  `json:"message,omitempty"`
	Condition     string  `json:"condition,omitempty"` // attached_condition / Filter
	Ref           string  `json:"ref,omitempty"`       // key lookup refs
	KeyLength     string  `json:"key_length,omitempty"`
	Cost          float64 `json:"cost,omitempty"` // read_cost+eval_cost if present
	SelectID      int     `json:"select_id,omitempty"`
}

// ExplainAnalysis is a normalized EXPLAIN summary.
type ExplainAnalysis struct {
	Summary     string       `json:"summary"`
	IndexHits   int          `json:"index_hits"`
	FullScans   int          `json:"full_scans"`
	Filesorts   int          `json:"filesorts,omitempty"`
	TempTables  int          `json:"temp_tables,omitempty"`
	TableAccess []ExplainHit `json:"table_access"`
}

// ExplainStepDetail is a human-readable per-table EXPLAIN walkthrough.
type ExplainStepDetail struct {
	Table      string  `json:"table,omitempty"`
	AccessType string  `json:"access_type,omitempty"`
	Severity   string  `json:"severity"` // ok | info | warn | crit
	Verdict    string  `json:"verdict"`
	Analysis   string  `json:"analysis"`
	Suggest    string  `json:"suggest,omitempty"`
	Key        string  `json:"key,omitempty"`
	Rows       float64 `json:"rows,omitempty"`
	Filtered   float64 `json:"filtered,omitempty"`
	Condition  string  `json:"condition,omitempty"`
}

// ExplainReport is the detailed post-EXPLAIN analysis shown under EXPLAIN JSON.
type ExplainReport struct {
	Overview     string              `json:"overview"`
	Health       string              `json:"health"` // good | caution | poor
	Steps        []ExplainStepDetail `json:"steps,omitempty"`
	Findings     []Finding           `json:"findings,omitempty"`
	IndexHints   []IndexHint         `json:"index_hints,omitempty"`
	Suggestions  []Finding           `json:"suggestions,omitempty"`
	RewrittenSQL string              `json:"rewritten_sql,omitempty"`
	MetadataUsed bool                `json:"metadata_used,omitempty"`
}

// ScoreBreakdown explains how the composite score was formed.
type ScoreBreakdown struct {
	Base           int `json:"base"`
	StaticPenalty  int `json:"static_penalty"`
	MetaPenalty    int `json:"meta_penalty"`
	ExplainPenalty int `json:"explain_penalty"`
	Final          int `json:"final"`
}

// AnalyzeResult is the unified output of /sql/analyze.
type AnalyzeResult struct {
	Dialect      Dialect          `json:"dialect"`
	Parsed       bool             `json:"parsed"`
	ParseError   string           `json:"parse_error,omitempty"`
	Score        int              `json:"score"`
	Breakdown    ScoreBreakdown   `json:"score_breakdown"`
	Findings     []Finding        `json:"findings"`
	Suggestions  []Finding        `json:"suggestions,omitempty"`
	IndexHints   []IndexHint      `json:"index_hints,omitempty"`
	RewrittenSQL string           `json:"rewritten_sql,omitempty"`
	MetadataUsed bool             `json:"metadata_used"`
	ExplainUsed  bool             `json:"explain_used"`
	Explain      *ExplainAnalysis `json:"explain,omitempty"`
	Shape        *QueryShape      `json:"shape,omitempty"`
}

// Penalty returns score deduction for a finding level.
func Penalty(level string) int {
	switch level {
	case "crit":
		return 25
	case "warn":
		return 12
	case "info":
		return 4
	default:
		return 0
	}
}

// ScoreFindings computes 100 - sum(penalties), clamped to [0,100].
func ScoreFindings(findings []Finding) int {
	score := 100
	for _, f := range findings {
		score -= Penalty(f.Level)
	}
	if score < 0 {
		score = 0
	}
	return score
}
