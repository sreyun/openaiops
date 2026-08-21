package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

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
	p1, total := ls.searchPage("", "", "", 0, 1, 50, nil)
	if total != 120 {
		t.Fatalf("total=%d want 120", total)
	}
	if len(p1) != 50 {
		t.Fatalf("page1 len=%d want 50", len(p1))
	}
	p3, _ := ls.searchPage("", "", "", 0, 3, 50, nil)
	if len(p3) != 20 {
		t.Fatalf("page3 len=%d want 20 (120-100)", len(p3))
	}
	// 页间不重叠 → offset 计算正确
	p2, _ := ls.searchPage("", "", "", 0, 2, 50, nil)
	if p1[0].Message == p2[0].Message || p1[len(p1)-1].Message == p2[0].Message {
		t.Fatal("page1/page2 内容重叠，offset 计算错误")
	}

	// 统计：按 error 过滤时，ByLevel 仍保留 warn/info 的总数
	stats := ls.searchStats("", "error", "", 0, nil)
	if stats.ByLevel["error"] != 60 || stats.ByLevel["warn"] != 40 || stats.ByLevel["info"] != 20 {
		t.Fatalf("过滤 error 时其它级别应保留总数，实际 %+v", stats.ByLevel)
	}
	// 但列表(searchPage)仍按 error 过滤：total=60 且只含 error
	ep, eTotal := ls.searchPage("", "error", "", 0, 1, 50, nil)
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

// 分页要一趟扫完，并且 total 必须是**全部匹配数**而不是本页条数——
// 分页控件靠它算总页数，早退会让"第 2 页"直接消失。
func TestLogSearchPageSinglePassKeepsTotal(t *testing.T) {
	ls := newLogStore()
	lines := make([]shared.LogLine, 0, 120)
	for i := 0; i < 120; i++ {
		lvl := "info"
		if i%3 == 0 {
			lvl = "error"
		}
		lines = append(lines, shared.LogLine{
			Ts: int64(1700000000 + i), Level: lvl, Source: "app",
			Message: "line-" + strconv.Itoa(i) + " needle",
		})
	}
	ls.ingest("h1", "host-1", lines)

	// 第 1 页
	page1, total := ls.searchPage("", "error", "needle", 0, 1, 10, nil)
	if total != 40 {
		t.Fatalf("总数 = %d, want 40（120 条里每 3 条一个 error）", total)
	}
	if len(page1) != 10 {
		t.Fatalf("第一页条数 = %d, want 10", len(page1))
	}
	// 第 2 页应当与第 1 页无重叠，且仍报同样的总数
	page2, total2 := ls.searchPage("", "error", "needle", 0, 2, 10, nil)
	if total2 != total {
		t.Fatalf("翻页后总数变了：%d → %d", total, total2)
	}
	if len(page2) != 10 {
		t.Fatalf("第二页条数 = %d, want 10", len(page2))
	}
	if page1[0].Message == page2[0].Message {
		t.Fatal("两页内容重复，偏移没生效")
	}
	// 越界页返回空，但总数照旧（前端据此禁用"下一页"）
	last, total3 := ls.searchPage("", "error", "needle", 0, 99, 10, nil)
	if len(last) != 0 || total3 != total {
		t.Fatalf("越界页 = %d 条 total=%d", len(last), total3)
	}
	// 关键字不匹配时应当干净地空
	if rows, n := ls.searchPage("", "", "no-such-token", 0, 1, 10, nil); len(rows) != 0 || n != 0 {
		t.Fatalf("不匹配的关键字返回了 %d 条 total=%d", len(rows), n)
	}
}

// 受限账号的日志检索：范围外主机的日志既不能出现在列表里，也不能计进 total
// （光给总数也是泄露——能推断出范围外有多少条、什么时候在报错），
// 更不能出现在统计的 TopHosts 里（那直接就是一串主机名）。
func TestLogSearchAppliesHostVisibility(t *testing.T) {
	ls := newLogStore()
	now := time.Now().Unix()
	var a, b []shared.LogLine
	for i := 0; i < 20; i++ {
		a = append(a, shared.LogLine{Ts: now, Level: "error", Message: "boom alpha"})
		b = append(b, shared.LogLine{Ts: now, Level: "error", Message: "boom beta"})
	}
	ls.ingest("host-a", "alpha", a)
	ls.ingest("host-b", "beta", b)
	onlyA := func(hostID string) bool { return hostID == "host-a" }

	items, total := ls.searchPage("", "", "boom", 0, 1, 100, onlyA)
	if total != 20 {
		t.Fatalf("total 把范围外的日志算进去了：%d，应当是 20", total)
	}
	for _, l := range items {
		if l.HostID != "host-a" {
			t.Fatalf("列表里出现了范围外主机的日志：%+v", l)
		}
	}

	stats := ls.searchStats("", "", "boom", 0, onlyA)
	for _, h := range stats.TopHosts {
		if h.Hostname == "beta" {
			t.Fatalf("统计的 TopHosts 泄露了范围外的主机名：%+v", stats.TopHosts)
		}
	}

	// 不受限时（nil）行为不变，别把过滤写成"永远过滤"。
	if _, all := ls.searchPage("", "", "boom", 0, 1, 100, nil); all != 40 {
		t.Fatalf("不受限时总数应当是 40，实际 %d", all)
	}
}
