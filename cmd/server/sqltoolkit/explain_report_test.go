package sqltoolkit

import "testing"

func TestBuildExplainReportFullScanIndex(t *testing.T) {
	sql := `SELECT name, tel FROM user WHERE name = '熊俊峰'`
	analysis := &ExplainAnalysis{
		Summary:   "表访问 1 处；命中索引 0；全表/索引扫描风险 1",
		FullScans: 1,
		IndexHits: 0,
		TableAccess: []ExplainHit{{
			Table: "user", AccessType: "ALL", Rows: 10, Filtered: 10,
			FullScanRisk: true, Condition: "(`user`.`name` = '熊俊峰')",
		}},
	}
	meta := SchemaMeta{
		"user": &TableMeta{
			Name: "user", TableRows: 200,
			Columns: []ColumnMeta{{Name: "name", DataType: "varchar"}, {Name: "tel", DataType: "varchar"}},
			Indexes: []IndexMeta{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
		},
	}
	rep := BuildExplainReport(sql, DialectMySQL57, analysis, meta)
	if rep.Health != "caution" && rep.Health != "poor" {
		t.Fatalf("health=%s", rep.Health)
	}
	if len(rep.Steps) != 1 {
		t.Fatalf("steps=%d", len(rep.Steps))
	}
	if rep.Steps[0].Severity != "warn" && rep.Steps[0].Severity != "crit" {
		t.Fatalf("step severity=%s", rep.Steps[0].Severity)
	}
	if len(rep.IndexHints) == 0 {
		t.Fatal("expected index hint on name")
	}
	foundName := false
	for _, h := range rep.IndexHints {
		for _, c := range h.Columns {
			if equalIdent(c, "name") {
				foundName = true
			}
		}
		if h.DDL == "" {
			t.Fatal("ddl empty")
		}
	}
	if !foundName {
		t.Fatalf("hints=%+v", rep.IndexHints)
	}
	if rep.Overview == "" || len(rep.Suggestions) == 0 {
		t.Fatal("overview/suggestions missing")
	}
}
