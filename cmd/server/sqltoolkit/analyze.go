package sqltoolkit

import "strings"

// AnalyzeInput carries offline SQL plus optional live metadata / EXPLAIN.
type AnalyzeInput struct {
	SQL      string
	Dialect  Dialect
	Meta     SchemaMeta
	Explain  *ExplainAnalysis
}

// Analyze runs Vitess parse → AST audit (+ regex fallback) → advisor → EXPLAIN/meta scoring.
func Analyze(in AnalyzeInput) AnalyzeResult {
	d := in.Dialect
	if d == "" {
		d = DialectMySQL80
	}
	raw := strings.TrimSpace(in.SQL)
	out := AnalyzeResult{
		Dialect: d,
		Breakdown: ScoreBreakdown{Base: 100},
		Findings:  []Finding{},
	}
	if raw == "" {
		out.Findings = []Finding{{ID: "empty", Level: "crit", Title: "空 SQL", Detail: "未提供语句"}}
		out.Score = 0
		out.Breakdown.StaticPenalty = 100
		out.Breakdown.Final = 0
		return out
	}

	shape := ExtractQueryShape(raw)
	out.Shape = shape
	out.Parsed = shape.ParseOK
	out.ParseError = shape.ParseError

	var static []Finding
	if shape.ParseOK {
		static = AuditAST(shape, d)
	} else {
		static = append(static, Finding{
			ID: "parse_error", Level: "info", Title: "AST 解析失败，已回退正则审核",
			Detail: shape.ParseError, Rule: "parse_error",
		})
		static = append(static, AuditRegex(raw, d).Findings...)
	}

	opt := OptimizeWithMeta(raw, d, shape, in.Meta)
	out.RewrittenSQL = opt.RewrittenSQL
	out.Suggestions = opt.Suggestions
	out.IndexHints = opt.IndexHints

	metaFindings := AuditMeta(shape, in.Meta, opt.IndexHints)
	out.MetadataUsed = len(in.Meta) > 0

	explainFindings := AuditExplain(in.Explain)
	out.ExplainUsed = in.Explain != nil
	out.Explain = in.Explain

	// Deduplicate across buckets (prefer first occurrence)
	seen := map[string]bool{}
	addAll := func(src []Finding, bucket *int) {
		for _, f := range src {
			key := f.ID
			if seen[key] && f.ID != "explain_full_scan" && f.ID != "explain_high_rows" {
				// allow multiple explain_* with different details via detail key
				key = f.ID + "|" + f.Detail
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out.Findings = append(out.Findings, f)
			*bucket += Penalty(f.Level)
		}
	}
	addAll(static, &out.Breakdown.StaticPenalty)
	addAll(metaFindings, &out.Breakdown.MetaPenalty)
	addAll(explainFindings, &out.Breakdown.ExplainPenalty)

	final := 100 - out.Breakdown.StaticPenalty - out.Breakdown.MetaPenalty - out.Breakdown.ExplainPenalty
	if final < 0 {
		final = 0
	}
	out.Score = final
	out.Breakdown.Final = final
	return out
}
