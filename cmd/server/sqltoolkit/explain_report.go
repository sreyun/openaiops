package sqltoolkit

import (
	"fmt"
	"strings"
)

// BuildExplainReport turns EXPLAIN hits + SQL shape + optional schema meta into a
// detailed Chinese report: per-step walkthrough, index DDL, and optimization tips.
func BuildExplainReport(sql string, d Dialect, analysis *ExplainAnalysis, meta SchemaMeta) ExplainReport {
	raw := strings.TrimSpace(sql)
	if d == "" {
		d = DialectMySQL57
	}
	shape := ExtractQueryShape(raw)
	opt := OptimizeWithMeta(raw, d, shape, meta)
	report := ExplainReport{
		Steps:        []ExplainStepDetail{},
		Findings:     []Finding{},
		IndexHints:   opt.IndexHints,
		Suggestions:  []Finding{},
		RewrittenSQL: opt.RewrittenSQL,
		MetadataUsed: len(meta) > 0,
	}

	if analysis != nil {
		report.Findings = append(report.Findings, AuditExplainDetailed(analysis, shape, meta)...)
		for i, h := range analysis.TableAccess {
			report.Steps = append(report.Steps, describeExplainStep(i+1, h, shape, meta, d))
		}
		report.Overview = buildExplainOverview(analysis, shape, meta)
		report.Health = explainHealth(analysis)
	} else {
		report.Overview = "未拿到结构化执行计划，以下建议主要依据 SQL 静态分析。"
		report.Health = "caution"
	}

	// Prefer explain-driven index hints when full scan + equality preds exist.
	if analysis != nil && analysis.FullScans > 0 && len(report.IndexHints) == 0 && shape != nil {
		report.IndexHints = AdviseIndexes(shape, meta)
		for i := range report.IndexHints {
			if report.IndexHints[i].DDL == "" {
				report.IndexHints[i].DDL = IndexDDLForDialect(d, report.IndexHints[i].Table, report.IndexHints[i].Columns)
			}
		}
	}

	// Merge optimize suggestions, then add explain-specific tips not already covered.
	seen := map[string]bool{}
	for _, s := range opt.Suggestions {
		key := s.ID + "|" + s.Title
		if seen[key] {
			continue
		}
		seen[key] = true
		report.Suggestions = append(report.Suggestions, s)
	}
	for _, f := range report.Findings {
		key := f.ID + "|" + f.Title
		if seen[key] {
			continue
		}
		seen[key] = true
		// Surface finding as actionable suggestion when it has Suggest text.
		if f.Suggest != "" {
			report.Suggestions = append(report.Suggestions, Finding{
				ID: f.ID, Level: f.Level, Title: f.Title, Detail: f.Detail, Suggest: f.Suggest,
			})
		}
	}
	report.Suggestions = append(report.Suggestions, explainGeneralTips(analysis, shape, d)...)

	if len(report.IndexHints) == 0 && analysis != nil && analysis.FullScans > 0 {
		report.Suggestions = append(report.Suggestions, Finding{
			ID: "need_where_index", Level: "warn", Title: "未能自动推导索引列",
			Detail:  "执行计划存在全表/全索引扫描，但未从 WHERE/JOIN 解析到可用等值或范围列（可能是函数包裹、OR、前导模糊或表达式条件）。",
			Suggest: "请手工确认过滤列；避免对列使用函数（如 DATE(col)、LOWER(col)）；等值条件优先建 BTree 索引。",
		})
	}
	return report
}

func explainHealth(a *ExplainAnalysis) string {
	if a == nil {
		return "caution"
	}
	if a.FullScans > 0 {
		for _, h := range a.TableAccess {
			if h.FullScanRisk && h.Rows >= 10000 {
				return "poor"
			}
		}
		return "caution"
	}
	if a.TempTables > 0 || a.Filesorts > 0 {
		return "caution"
	}
	if a.IndexHits > 0 {
		return "good"
	}
	return "caution"
}

func buildExplainOverview(a *ExplainAnalysis, shape *QueryShape, meta SchemaMeta) string {
	var b strings.Builder
	b.WriteString(a.Summary)
	b.WriteString("\n\n")
	if a.FullScans > 0 {
		b.WriteString("风险等级：存在全表扫描（type=ALL）或非覆盖的全索引扫描（type=index）。")
		b.WriteString("在数据量增长后，此类路径往往成为慢查询主因；应优先用高选择度谓词 + 合适索引消除。\n")
	} else if a.IndexHits > 0 {
		b.WriteString("整体路径已使用索引。仍建议核对：是否回表过多、filtered 是否偏低、ORDER BY/GROUP BY 是否触发额外 filesort/临时表。\n")
	}
	if shape != nil && shape.ParseOK {
		preds := len(shape.WherePreds)
		b.WriteString(fmt.Sprintf("语句结构：类型=%s，表数=%d，WHERE 谓词≈%d，JOIN=%v，LIMIT=%v。\n",
			shape.StmtType, len(shape.Tables), preds, shape.HasJoin, shape.HasLimit))
		if len(shape.WherePreds) > 0 {
			parts := make([]string, 0, len(shape.WherePreds))
			for _, p := range shape.WherePreds {
				col := p.Column
				if p.Table != "" {
					col = p.Table + "." + col
				}
				tag := string(p.Kind)
				if p.FuncWrapped {
					tag += "+func"
				}
				if p.LeadingLike {
					tag += "+leading%"
				}
				parts = append(parts, fmt.Sprintf("%s(%s)", col, tag))
			}
			b.WriteString("过滤条件列：" + strings.Join(parts, "，") + "。\n")
		}
	}
	if meta != nil {
		for _, h := range a.TableAccess {
			tm := meta[normalizeIdent(baseTableName(h.Table))]
			if tm == nil || tm.TableRows <= 0 {
				continue
			}
			b.WriteString(fmt.Sprintf("元数据：表 %s 约 %d 行", tm.Name, tm.TableRows))
			if len(tm.Indexes) > 0 {
				names := make([]string, 0, len(tm.Indexes))
				for _, idx := range tm.Indexes {
					names = append(names, fmt.Sprintf("%s(%s)", idx.Name, strings.Join(idx.Columns, ",")))
				}
				b.WriteString("；现有索引：" + strings.Join(names, "；"))
			} else {
				b.WriteString("；当前未见二级索引（或未拉取到）")
			}
			b.WriteString("。\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func describeExplainStep(n int, h ExplainHit, shape *QueryShape, meta SchemaMeta, d Dialect) ExplainStepDetail {
	at := strings.ToLower(strings.TrimSpace(h.AccessType))
	step := ExplainStepDetail{
		Table:      h.Table,
		AccessType: h.AccessType,
		Key:        nilKey(h.Key),
		Rows:       h.Rows,
		Filtered:   h.Filtered,
		Condition:  h.Condition,
		Severity:   "info",
	}
	tbl := h.Table
	if tbl == "" {
		tbl = "(派生/临时)"
	}
	tm := meta[normalizeIdent(baseTableName(h.Table))]
	var analysis strings.Builder
	analysis.WriteString(fmt.Sprintf("步骤 %d：访问表「%s」，access_type=%s。", n, tbl, h.AccessType))
	if h.Rows > 0 {
		analysis.WriteString(fmt.Sprintf(" 优化器估计将检查约 %.0f 行", h.Rows))
		if h.Filtered > 0 {
			analysis.WriteString(fmt.Sprintf("，过滤后保留约 %.1f%%（约 %.0f 行进入下一阶段）", h.Filtered, h.Rows*h.Filtered/100))
		}
		analysis.WriteString("。")
	}
	if h.Condition != "" {
		analysis.WriteString(" 附加条件：" + h.Condition + "。")
	}
	if h.PossibleKeys != "" && h.PossibleKeys != "—" && h.PossibleKeys != "<nil>" {
		analysis.WriteString(" 候选索引 possible_keys=" + h.PossibleKeys + "。")
	}
	if h.Ref != "" {
		analysis.WriteString(" 索引查找 ref=" + h.Ref + "。")
	}
	if h.Cost > 0 {
		analysis.WriteString(fmt.Sprintf(" 代价估计≈%.2f。", h.Cost))
	}
	if tm != nil && tm.TableRows > 0 {
		analysis.WriteString(fmt.Sprintf(" 表统计约 %d 行。", tm.TableRows))
	}

	switch {
	case at == "all" || strings.Contains(at, "seq scan"):
		step.Severity = "warn"
		if h.Rows >= 100000 || (tm != nil && tm.TableRows >= 100000) {
			step.Severity = "crit"
		}
		step.Verdict = "全表扫描（未使用索引）"
		analysis.WriteString(" 这意味着引擎将逐行读取整张表（或大段堆表），再按 WHERE 过滤。")
		analysis.WriteString(" 若表持续增长，延迟与 IO 会近似线性恶化。")
		cols := equalityColsForTable(shape, h.Table)
		if len(cols) > 0 {
			step.Suggest = fmt.Sprintf("建议在 (%s) 上建立索引，使访问类型提升为 ref/range；DDL 见下方「索引建议」。", strings.Join(cols, ", "))
			analysis.WriteString(fmt.Sprintf(" 已从 SQL 识别到可用于索引的等值/范围列：%s。", strings.Join(cols, ", ")))
		} else {
			step.Suggest = "检查 WHERE 是否对列做了函数/隐式类型转换，或条件无法索引化（如 OR 跨列、前导 %LIKE）；必要时改写谓词后再建索引。"
		}
		if h.PossibleKeys == "" || h.PossibleKeys == "—" || h.PossibleKeys == "<nil>" {
			analysis.WriteString(" possible_keys 为空，说明优化器认为当前没有任何可用索引覆盖该条件。")
		} else {
			analysis.WriteString(" 虽有候选索引但未选用（key 为空），可能是统计信息不准、选择性估低、或索引前缀不匹配；可执行 ANALYZE TABLE 并核对索引列顺序。")
		}
	case at == "index" && !h.UsingIndex:
		step.Severity = "warn"
		step.Verdict = "全索引扫描（可能仍较重）"
		analysis.WriteString(" type=index 表示按索引顺序扫描整棵索引树，未必比全表扫描更轻（尤其非覆盖索引仍需回表）。")
		step.Suggest = "尽量用等值/范围条件把访问降到 ref/range；若只需索引列可改为覆盖索引（Using index）。"
	case at == "index" && h.UsingIndex:
		step.Severity = "info"
		step.Verdict = "索引覆盖扫描"
		analysis.WriteString(" 扫描索引但 Using index=true，无需回表，通常可接受；若 rows 很大仍建议加过滤条件。")
	case at == "ref" || at == "eq_ref" || at == "const" || at == "system":
		step.Severity = "ok"
		step.Verdict = "索引定位良好（" + h.AccessType + "）"
		analysis.WriteString(fmt.Sprintf(" 已使用索引 %s 做等值查找。", displayKey(h.Key)))
		if h.UsingIndex {
			analysis.WriteString(" Using index：覆盖索引，无需回表。")
		} else {
			analysis.WriteString(" 非覆盖索引：匹配后仍可能回表取投影列；若投影列固定，可考虑覆盖索引进一步降 IO。")
		}
	case at == "range":
		step.Severity = "ok"
		step.Verdict = "索引范围扫描"
		analysis.WriteString(fmt.Sprintf(" 使用索引 %s 做范围扫描。", displayKey(h.Key)))
		if h.Filtered > 0 && h.Filtered < 20 && h.Rows > 1000 {
			step.Severity = "info"
			analysis.WriteString(" filtered 偏低且 rows 较大，可考虑收紧范围或把更高选择度等值列放到索引最左。")
		}
		step.Suggest = "确认范围列在复合索引中位于等值列之后；避免对范围列再套函数。"
	case strings.Contains(at, "index scan") || strings.Contains(at, "index only") || strings.Contains(at, "bitmap"):
		step.Severity = "ok"
		step.Verdict = "索引访问（" + h.AccessType + "）"
		analysis.WriteString(" PostgreSQL/类索引节点：请结合 TEXT Plan 中的 Filter、Rows Removed by Filter 判断过滤是否下推到索引。")
	default:
		if h.FullScanRisk {
			step.Severity = "warn"
			step.Verdict = "存在扫描风险"
		} else if h.Key != "" && !isNilKey(h.Key) {
			step.Severity = "ok"
			step.Verdict = "已使用索引 " + h.Key
		} else {
			step.Verdict = "访问类型 " + h.AccessType
		}
	}

	extra := []string{}
	if h.UsingFilesort {
		extra = append(extra, "Using filesort（额外排序，ORDER BY 未完全由索引满足）")
		if step.Severity == "ok" {
			step.Severity = "info"
		}
	}
	if h.UsingTemp {
		extra = append(extra, "Using temporary（临时表，常见于 GROUP BY/DISTINCT/某些 ORDER BY）")
		if step.Severity == "ok" || step.Severity == "info" {
			step.Severity = "warn"
		}
	}
	if len(extra) > 0 {
		analysis.WriteString(" 额外标记：" + strings.Join(extra, "；") + "。")
		if step.Suggest == "" && h.UsingFilesort {
			step.Suggest = "让 ORDER BY 列成为所用索引的后缀，或减少排序列；大结果集排序会放大 CPU/内存压力。"
		}
		if h.UsingTemp && step.Suggest == "" {
			step.Suggest = "尝试覆盖索引包含 GROUP BY 列，或减少 DISTINCT；必要时拆分查询。"
		}
	}
	_ = d
	step.Analysis = strings.TrimSpace(analysis.String())
	return step
}

// AuditExplainDetailed expands EXPLAIN findings with richer Chinese detail.
func AuditExplainDetailed(ex *ExplainAnalysis, shape *QueryShape, meta SchemaMeta) []Finding {
	base := AuditExplain(ex)
	if ex == nil {
		return base
	}
	out := make([]Finding, 0, len(base)+8)
	seen := map[string]bool{}
	for _, f := range base {
		seen[f.ID+"|"+f.Detail] = true
		// Enrich short findings.
		f2 := f
		if f.ID == "explain_full_scan" {
			f2.Detail = enrichFullScanDetail(f.Detail, ex, shape, meta)
			if f2.Suggest == "补充选择性 WHERE / 合适索引" {
				cols := firstScanIndexCols(ex, shape)
				if len(cols) > 0 {
					f2.Suggest = fmt.Sprintf("为扫描表增加索引，优先列：%s；等值列在左、范围列在后。", strings.Join(cols, ", "))
				}
			}
		}
		out = append(out, f2)
	}
	// Additional cross-cutting findings
	if ex.FullScans > 0 && shape != nil && !shape.HasWhere && shape.StmtType == "select" {
		key := "explain_no_where|1"
		if !seen[key] {
			out = append(out, Finding{
				ID: "explain_no_where", Level: "warn", Title: "EXPLAIN：缺少 WHERE 过滤",
				Detail:  "全表扫描且语句未见 WHERE，极易在大表上打满 IO。",
				Suggest: "补上业务过滤条件；若确需全量导出，使用批处理主键翻页 + LIMIT，并避开高峰。",
			})
		}
	}
	if ex.IndexHits > 0 && ex.FullScans > 0 {
		out = append(out, Finding{
			ID: "explain_mixed_plan", Level: "info", Title: "EXPLAIN：混合访问路径",
			Detail:  "计划中部分表走了索引、部分仍全扫。JOIN 驱动顺序与被驱动表索引同样重要。",
			Suggest: "确保被驱动表的 JOIN 键有索引；小表驱动大表；避免在 JOIN 键上函数包裹。",
		})
	}
	for _, h := range ex.TableAccess {
		if h.Filtered > 0 && h.Filtered < 5 && h.Rows >= 1000 {
			out = append(out, Finding{
				ID: "explain_low_filter", Level: "warn", Title: "EXPLAIN：过滤率极低",
				Detail: fmt.Sprintf("表 %s rows≈%.0f 但 filtered≈%.2f%%，大量行读入后被丢掉。",
					h.Table, h.Rows, h.Filtered),
				Suggest: "把过滤条件下推到索引能命中的列；检查是否存在隐式类型转换导致索引失效。",
			})
		}
	}
	return dedupeFindings(out)
}

func enrichFullScanDetail(base string, ex *ExplainAnalysis, shape *QueryShape, meta SchemaMeta) string {
	var b strings.Builder
	b.WriteString(base)
	for _, h := range ex.TableAccess {
		if !h.FullScanRisk {
			continue
		}
		if h.Condition != "" {
			b.WriteString("；条件 " + h.Condition)
		}
		tm := meta[normalizeIdent(baseTableName(h.Table))]
		if tm != nil && tm.TableRows > 0 {
			b.WriteString(fmt.Sprintf("；表统计约 %d 行", tm.TableRows))
		}
		cols := equalityColsForTable(shape, h.Table)
		if len(cols) > 0 {
			b.WriteString("；建议索引列 " + strings.Join(cols, ","))
		}
	}
	return b.String()
}

func firstScanIndexCols(ex *ExplainAnalysis, shape *QueryShape) []string {
	if ex == nil {
		return nil
	}
	for _, h := range ex.TableAccess {
		if h.FullScanRisk {
			if cols := equalityColsForTable(shape, h.Table); len(cols) > 0 {
				return cols
			}
		}
	}
	if shape != nil {
		adv := AdviseIndexes(shape, nil)
		if len(adv) > 0 {
			return adv[0].Columns
		}
	}
	return nil
}

func equalityColsForTable(shape *QueryShape, table string) []string {
	if shape == nil {
		return nil
	}
	adv := &tableAdvice{table: baseTableName(table)}
	for _, p := range shape.WherePreds {
		if p.Column == "" || p.FuncWrapped || p.LeadingLike {
			continue
		}
		tbl := resolvePredTable(shape, p)
		if tbl == "" && len(shape.Tables) == 1 {
			tbl = shape.Tables[0].Name
		}
		if table != "" && tbl != "" && !equalIdent(baseTableName(tbl), baseTableName(table)) {
			// also allow alias match against explain table name
			if !equalIdent(tbl, table) {
				continue
			}
		}
		switch p.Kind {
		case PredEqual:
			adv.eq = appendUnique(adv.eq, p.Column)
		case PredRange:
			adv.rng = appendUnique(adv.rng, p.Column)
		}
	}
	for _, p := range shape.JoinPreds {
		if p.Kind != PredEqual || p.Column == "" {
			continue
		}
		tbl := resolvePredTable(shape, p)
		if table != "" && tbl != "" && !equalIdent(baseTableName(tbl), baseTableName(table)) && !equalIdent(tbl, table) {
			continue
		}
		adv.eq = appendUnique(adv.eq, p.Column)
	}
	attachOrder := len(shape.Tables) == 1
	if !attachOrder && table != "" && len(shape.Tables) > 0 {
		attachOrder = equalIdent(baseTableName(shape.Tables[0].Name), baseTableName(table))
	}
	if attachOrder {
		for _, c := range shape.OrderCols {
			adv.ord = appendUnique(adv.ord, c)
		}
		for _, c := range shape.GroupCols {
			adv.grp = appendUnique(adv.grp, c)
		}
	}
	return buildIndexCols(adv)
}

func explainGeneralTips(a *ExplainAnalysis, shape *QueryShape, d Dialect) []Finding {
	var tips []Finding
	if shape != nil && shape.SelectStar {
		tips = append(tips, Finding{
			ID: "tip_select_star", Level: "info", Title: "投影列优化",
			Detail:  "SELECT * 会阻止覆盖索引、放大网络与回表成本。",
			Suggest: "只选择业务需要的列；索引建议可进一步做成覆盖索引。",
		})
	}
	if a != nil && a.FullScans > 0 {
		tips = append(tips, Finding{
			ID: "tip_validate_index", Level: "info", Title: "上线前验证",
			Detail:  "索引创建后请用同一条 SQL 再跑 EXPLAIN，确认 type 变为 ref/range，且 rows 明显下降。",
			Suggest: "变更窗口执行；大表用 online DDL / pt-osc / gh-ost；先在从库或预发验证。",
		})
	}
	if d == DialectPostgres {
		tips = append(tips, Finding{
			ID: "tip_pg_analyze", Level: "info", Title: "统计信息",
			Detail:  "错误的 Seq Scan 有时来自过期统计。",
			Suggest: "在维护窗对相关表 ANALYZE；关注 n_dead_tup 与膨胀。",
		})
	} else {
		tips = append(tips, Finding{
			ID: "tip_mysql_stats", Level: "info", Title: "统计信息与选择性",
			Detail:  "优化器选错索引常见于过期统计或低选择度列（如状态码）。",
			Suggest: "ANALYZE TABLE；高选择度列放复合索引最左；低选择度列不宜单独建索引。",
		})
	}
	return tips
}

func baseTableName(t string) string {
	t = strings.TrimSpace(t)
	t = strings.Trim(t, "`\"")
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	return t
}

func isNilKey(k string) bool {
	k = strings.TrimSpace(strings.ToLower(k))
	return k == "" || k == "<nil>" || k == "null" || k == "nil" || k == "—"
}

func nilKey(k string) string {
	if isNilKey(k) {
		return ""
	}
	return k
}

func displayKey(k string) string {
	if isNilKey(k) {
		return "（无）"
	}
	return k
}
