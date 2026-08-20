package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// TestLogSearchPageAndStats 覆盖服务端分页(offset/total/页不重叠)与统计口径
// （ByLevel 在按某级别过滤列表时仍保留其它级别总数——需求核心）。
func TestLogSearchPageAndStats(t *testing.T) {
	ls := newLogStore()
	var lines []shared.LogLine
	for i := 0; i < 60; i++ {
		lines = append(lines, shared.LogLine{Ts: int64(1000 + i), Level: "error", Message: fmt.Sprintf("err-%d", i)})
	}
	for i := 0; i < 40; i++ {
		lines = append(lines, shared.LogLine{Ts: int64(2000 + i), Level: "warn", Message: fmt.Sprintf("warn-%d", i)})
	}
	for i := 0; i < 20; i++ {
		lines = append(lines, shared.LogLine{Ts: int64(3000 + i), Level: "info", Message: fmt.Sprintf("info-%d", i)})
	}
	ls.ingest("h1", "web", lines)

	// 分页：120 条 / 每页 50 → 3 页
	p1, total := ls.searchPage("", "", "", 0, 1, 50)
	if total != 120 {
		t.Fatalf("total=%d want 120", total)
	}
	if len(p1) != 50 {
		t.Fatalf("page1 len=%d want 50", len(p1))
	}
	p3, _ := ls.searchPage("", "", "", 0, 3, 50)
	if len(p3) != 20 {
		t.Fatalf("page3 len=%d want 20 (120-100)", len(p3))
	}
	// 页间不重叠 → offset 计算正确
	p2, _ := ls.searchPage("", "", "", 0, 2, 50)
	if p1[0].Message == p2[0].Message || p1[len(p1)-1].Message == p2[0].Message {
		t.Fatal("page1/page2 内容重叠，offset 计算错误")
	}

	// 统计：按 error 过滤时，ByLevel 仍保留 warn/info 的总数
	stats := ls.searchStats("", "error", "", 0)
	if stats.ByLevel["error"] != 60 || stats.ByLevel["warn"] != 40 || stats.ByLevel["info"] != 20 {
		t.Fatalf("过滤 error 时其它级别应保留总数，实际 %+v", stats.ByLevel)
	}
	// 但列表(searchPage)仍按 error 过滤：total=60 且只含 error
	ep, eTotal := ls.searchPage("", "error", "", 0, 1, 50)
	if eTotal != 60 {
		t.Fatalf("error 列表 total=%d want 60", eTotal)
	}
	for _, it := range ep {
		if it.Level != "error" {
			t.Fatalf("error 过滤下列表出现 %s", it.Level)
		}
	}
}

// TestLogStorePersistRoundTrip mirrors the PG blob cycle: export → JSON → import.
// It guards the fix for "logs lost after container restart".
func TestLogStorePersistRoundTrip(t *testing.T) {
	src := newLogStore()
	src.ingest("h1", "web", []shared.LogLine{
		{Ts: 100, Source: "/var/log/a", Level: "ERROR", Message: "boom"},
		{Ts: 101, Source: "/var/log/a", Level: "info", Message: "ok"},
	})
	raw, err := json.Marshal(src.export())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var logs []StoredLog
	if err := json.Unmarshal(raw, &logs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dst := newLogStore()
	dst.importLogs(logs)
	if dst.count() != 2 {
		t.Fatalf("restored count=%d, want 2", dst.count())
	}
	if dst.errorCount(0) != 1 {
		t.Fatalf("restored errorCount=%d, want 1", dst.errorCount(0))
	}
	// New ingests continue to append onto restored history.
	dst.ingest("h1", "web", []shared.LogLine{{Ts: 102, Level: "warn", Message: "later"}})
	if dst.count() != 3 {
		t.Fatalf("post-restore count=%d, want 3", dst.count())
	}
}

// TestLogStorePersistCap ensures persistence only writes a bounded warm tail.
func TestLogStorePersistCap(t *testing.T) {
	ls := newLogStore()
	lines := make([]shared.LogLine, logPersistCap+500)
	for i := range lines {
		lines[i] = shared.LogLine{Ts: int64(i), Level: "info", Message: "x"}
	}
	ls.ingest("h1", "web", lines)
	exported := ls.export()
	if len(exported) != logPersistCap {
		t.Fatalf("exported=%d, want %d (capped)", len(exported), logPersistCap)
	}
	// The tail must be the newest lines.
	if exported[len(exported)-1].Ts != int64(logPersistCap+499) {
		t.Fatalf("tail Ts=%d, want newest", exported[len(exported)-1].Ts)
	}
}

// TestInspectionPersistRoundTrip guards the fix for "AI inspections lost after
// restart" and verifies the ID sequence resumes past the highest persisted ID.
func TestInspectionPersistRoundTrip(t *testing.T) {
	src := &aiManager{nextID: 0}
	src.reports = []InspectionReport{
		{ID: 1, Ts: 100, Trigger: "scheduled", Source: "heuristic", Summary: "健康"},
		{ID: 2, Ts: 200, Trigger: "manual", Source: "ai", Model: "gpt", Summary: "异常"},
	}
	src.nextID = 2
	raw, err := json.Marshal(src.exportReports())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reps []InspectionReport
	if err := json.Unmarshal(raw, &reps); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dst := newAIManager(nil)
	dst.importReports(reps)
	got := dst.Reports() // newest-first
	if len(got) != 2 || got[0].ID != 2 || got[0].Summary != "异常" {
		t.Fatalf("restored reports wrong: %+v", got)
	}
	if dst.nextID != 2 {
		t.Fatalf("nextID=%d, want 2 (resume from max persisted ID)", dst.nextID)
	}
}

func TestLogStoreSearch(t *testing.T) {
	ls := newLogStore()
	ls.ingest("h1", "web", []shared.LogLine{
		{Ts: 100, Source: "/var/log/a", Level: "ERROR", Message: "connection refused"},
		{Ts: 101, Source: "/var/log/a", Level: "info", Message: "started ok"},
		{Ts: 102, Source: "/var/log/a", Level: "warn", Message: "slow query"},
	})
	if ls.count() != 3 {
		t.Fatalf("count=%d, want 3", ls.count())
	}
	if r := ls.search("", "", "refused", 0, 10); len(r) != 1 || r[0].Message != "connection refused" {
		t.Fatalf("keyword search failed: %v", r)
	}
	// "ERROR" must normalize to "error".
	if r := ls.search("", "error", "", 0, 10); len(r) != 1 {
		t.Fatalf("expected 1 error line, got %d", len(r))
	}
	if r := ls.recentErrors(0, 10); len(r) != 2 { // error + warn
		t.Fatalf("recentErrors=%d, want 2", len(r))
	}
	if ls.errorCount(0) != 1 {
		t.Fatalf("errorCount=%d, want 1", ls.errorCount(0))
	}
	if all := ls.search("", "", "", 0, 10); all[0].Ts != 102 {
		t.Fatalf("expected newest-first, got Ts=%d", all[0].Ts)
	}
}

func TestHeuristicInspect(t *testing.T) {
	ctx := inspectionContext{
		OnlineHosts:   3,
		OfflineHosts:  []string{"db-01"},
		FiringAlerts:  []Alert{{Level: "critical", Hostname: "web-01", Message: "CPU 96%"}},
		BreachingSLOs: []SLOStatus{{SLO: SLO{Name: "API可用性", Target: 99.9}, SLI: 99.0}},
		HighUsage:     []string{"web-01 CPU 96%"},
		ErrorCount:    60,
	}
	summary, findings := heuristicInspect(ctx)
	if summary == "" {
		t.Fatal("empty summary")
	}
	var crit, warn int
	for _, f := range findings {
		switch f.Severity {
		case "critical":
			crit++
		case "warning":
			warn++
		}
	}
	if crit < 3 {
		t.Errorf("expected >=3 critical findings (offline+alert+errors>=50), got %d", crit)
	}
	if warn < 2 {
		t.Errorf("expected >=2 warning findings (slo+high-usage), got %d", warn)
	}
	// A healthy snapshot yields no findings.
	if s2, f2 := heuristicInspect(inspectionContext{OnlineHosts: 5}); len(f2) != 0 || s2 == "" {
		t.Errorf("healthy inspection should have no findings, got %d", len(f2))
	}
}

func TestHeuristicDiagnose(t *testing.T) {
	out := heuristicDiagnose(Incident{Type: "disk", Title: "disk full"}, "主机: web-01")
	if !strings.Contains(out, "清理") {
		t.Errorf("disk diagnosis should mention cleanup, got: %s", out)
	}
	// Unknown type still returns a sensible generic direction.
	if g := heuristicDiagnose(Incident{Type: ""}, ""); g == "" {
		t.Error("generic diagnosis must not be empty")
	}
}
