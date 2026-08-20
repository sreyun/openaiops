package sqltoolkit

import (
	"fmt"
	"strings"
)

// AdviseIndexes implements SQLAdvisor-style index suggestions:
// equality cols → range cols → order/group cols; skip if existing index covers prefix.
func AdviseIndexes(shape *QueryShape, meta SchemaMeta) []IndexHint {
	if shape == nil {
		return nil
	}
	byTable := map[string]*tableAdvice{}
	ensure := func(table string) *tableAdvice {
		key := normalizeIdent(table)
		if key == "" {
			return nil
		}
		if byTable[key] == nil {
			byTable[key] = &tableAdvice{table: table}
		}
		return byTable[key]
	}

	// Assign WHERE preds to tables
	for _, p := range shape.WherePreds {
		if p.Column == "" || p.FuncWrapped || p.LeadingLike {
			continue
		}
		tbl := resolvePredTable(shape, p)
		if tbl == "" && len(shape.Tables) == 1 {
			tbl = shape.Tables[0].Name
		}
		adv := ensure(tbl)
		if adv == nil {
			continue
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
		adv := ensure(tbl)
		if adv != nil {
			adv.eq = appendUnique(adv.eq, p.Column)
		}
	}
	// ORDER / GROUP attach to primary/driving table
	drive := pickDriveTable(shape, meta)
	if drive != "" {
		adv := ensure(drive)
		if adv != nil {
			for _, c := range shape.OrderCols {
				adv.ord = appendUnique(adv.ord, c)
			}
			for _, c := range shape.GroupCols {
				adv.grp = appendUnique(adv.grp, c)
			}
		}
	}

	var hints []IndexHint
	for _, adv := range byTable {
		cols := buildIndexCols(adv)
		if len(cols) == 0 {
			continue
		}
		if len(cols) > 4 {
			cols = cols[:4]
		}
		tm := meta[normalizeIdent(adv.table)]
		if tm != nil && indexCovers(tm.Indexes, cols) {
			continue
		}
		ddl := fmt.Sprintf("ALTER TABLE `%s` ADD INDEX idx_%s (%s);",
			adv.table, strings.Join(cols, "_"), quoteCols(cols))
		reason := "SQLAdvisor 风格：等值列在前、范围列其次，再接 ORDER/GROUP 列"
		if tm != nil {
			reason += fmt.Sprintf("；表约 %d 行，已过滤已有覆盖索引", tm.TableRows)
		} else {
			reason += "（无元数据，模板建议）"
		}
		hints = append(hints, IndexHint{
			Table: adv.table, Columns: cols, Reason: reason, DDL: ddl, Meta: tm != nil,
		})
	}
	return hints
}

type tableAdvice struct {
	table    string
	eq, rng  []string
	ord, grp []string
}

func buildIndexCols(a *tableAdvice) []string {
	out := []string{}
	for _, c := range a.eq {
		out = appendUnique(out, c)
	}
	for _, c := range a.rng {
		out = appendUnique(out, c)
	}
	for _, c := range a.grp {
		out = appendUnique(out, c)
	}
	for _, c := range a.ord {
		out = appendUnique(out, c)
	}
	return out
}

func resolvePredTable(shape *QueryShape, p Predicate) string {
	if p.Table != "" {
		for _, t := range shape.Tables {
			if equalIdent(t.Alias, p.Table) || equalIdent(t.Name, p.Table) {
				return t.Name
			}
		}
		return p.Table
	}
	return ""
}

func pickDriveTable(shape *QueryShape, meta SchemaMeta) string {
	if shape == nil || len(shape.Tables) == 0 {
		return ""
	}
	if len(shape.Tables) == 1 {
		return shape.Tables[0].Name
	}
	// Prefer table with equality predicates and smaller table_rows
	best := ""
	var bestRows int64 = -1
	eqCount := map[string]int{}
	for _, p := range shape.WherePreds {
		if p.Kind != PredEqual {
			continue
		}
		tbl := resolvePredTable(shape, p)
		if tbl != "" {
			eqCount[normalizeIdent(tbl)]++
		}
	}
	for _, t := range shape.Tables {
		key := normalizeIdent(t.Name)
		rows := int64(1 << 62)
		if tm := meta[key]; tm != nil && tm.TableRows > 0 {
			rows = tm.TableRows
		}
		// score: more eq first, then smaller table
		if best == "" || eqCount[key] > eqCount[normalizeIdent(best)] ||
			(eqCount[key] == eqCount[normalizeIdent(best)] && (bestRows < 0 || rows < bestRows)) {
			best = t.Name
			bestRows = rows
		}
	}
	return best
}

func indexCovers(indexes []IndexMeta, cols []string) bool {
	for _, idx := range indexes {
		if len(idx.Columns) < len(cols) {
			continue
		}
		ok := true
		for i, c := range cols {
			if !equalIdent(idx.Columns[i], c) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
