package main

import (
	"net/http/httptest"
	"testing"
)

func TestAlertPageFilterSortSummary(t *testing.T) {
	list := []Alert{
		{HostID: "h1", Hostname: "db01", IP: "10.0.0.1", Level: "critical", Type: "cpu", Message: "cpu high", Since: 100},
		{HostID: "h2", Hostname: "web02", IP: "10.0.0.2", Level: "warning", Type: "disk", Scope: "/data", Message: "disk 91%", Since: 50, Status: "acknowledged"},
		{HostID: "h1", Hostname: "db01", IP: "10.0.0.1", Level: "warning", Type: "memory", Message: "mem 85%", Since: 80},
		{HostID: "h3", Hostname: "cache03", Level: "info", Type: "check", Message: "recovered", Status: "resolved"},
	}
	sum := summarizeAlerts(list)
	if sum.Total != 4 || sum.Critical != 1 || sum.Warning != 2 || sum.Info != 1 || sum.Active != 2 || sum.Acknowledged != 1 || sum.Resolved != 1 {
		t.Fatalf("summary mismatch: %+v", sum)
	}
	if sum.TopHosts[0].HostID != "h1" || sum.TopHosts[0].Count != 2 {
		t.Fatalf("top host mismatch: %+v", sum.TopHosts)
	}

	// 多词 AND：db01 + 85 只命中内存那条
	got := filterAlertsByQuery(list, alertPageQuery{q: "db01 85"})
	if len(got) != 1 || got[0].Type != "memory" {
		t.Fatalf("q filter: %+v", got)
	}
	// status=active 排除已确认与已恢复
	if got := filterAlertsByQuery(list, alertPageQuery{status: "active"}); len(got) != 2 {
		t.Fatalf("active filter: %d", len(got))
	}
	// level + host 子串
	if got := filterAlertsByQuery(list, alertPageQuery{level: "warning", host: "10.0.0.2"}); len(got) != 1 || got[0].Type != "disk" {
		t.Fatalf("level+host filter: %+v", got)
	}
	// 按级别降序，critical 在前
	cp := append([]Alert(nil), list...)
	sortAlertsBy(cp, "level", "desc")
	if cp[0].Level != "critical" || cp[3].Level != "info" {
		t.Fatalf("level sort: %+v", cp)
	}
	sortAlertsBy(cp, "since", "asc")
	if cp[0].Since != 0 || cp[3].Since != 100 {
		t.Fatalf("since sort: %+v", cp)
	}
}

func TestWriteAlertPageHeadersAndSlice(t *testing.T) {
	list := make([]Alert, 0, 130)
	for i := 0; i < 130; i++ {
		lv := "warning"
		if i%10 == 0 {
			lv = "critical"
		}
		list = append(list, Alert{HostID: "h", Hostname: "host", Level: lv, Type: "cpu", Message: "m"})
	}
	rec := httptest.NewRecorder()
	writeAlertPage(rec, list, alertPageQuery{limit: 50, offset: 100})
	if rec.Header().Get("X-Total-Count") != "130" || rec.Header().Get("X-Alert-Critical") != "13" || rec.Header().Get("X-Alert-Active") != "130" {
		t.Fatalf("headers: %v", rec.Header())
	}
	if rec.Header().Get("X-Alert-Types") != "cpu:130" {
		t.Fatalf("types header: %q", rec.Header().Get("X-Alert-Types"))
	}
	body := rec.Body.String()
	// 最后一页只有 30 条：数一下对象个数
	n := 0
	for i := 0; i+8 <= len(body); i++ {
		if body[i:i+8] == `"host_id` {
			n++
		}
	}
	if n != 30 {
		t.Fatalf("last page should hold 30 rows, got %d", n)
	}
	// 越界 offset 给空数组而不是 500
	rec2 := httptest.NewRecorder()
	writeAlertPage(rec2, list, alertPageQuery{limit: 50, offset: 999})
	if rec2.Body.String() != "[]\n" && rec2.Body.String() != "[]" {
		t.Fatalf("out-of-range offset should be empty array, got %q", rec2.Body.String())
	}
}
