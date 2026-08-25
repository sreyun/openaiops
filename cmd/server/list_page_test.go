package main

import (
	"net/http/httptest"
	"testing"
)

func TestListPageBoundsAndTokens(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/checks?limit=1000&offset=-5&q=Nginx%20DB01", nil)
	p, ok := parseListPage(r)
	if !ok || p.limit != listPageMaxLimit || p.offset != 0 || p.q != "nginx db01" {
		t.Fatalf("parse: %+v ok=%v", p, ok)
	}
	if _, ok := parseListPage(httptest.NewRequest("GET", "/api/v1/checks", nil)); ok {
		t.Fatal("no limit must stay in legacy full-list mode")
	}
	if st, en := pageBounds(30, listPage{limit: 50, offset: 100}); st != 30 || en != 30 {
		t.Fatalf("out-of-range offset should give an empty page, got %d..%d", st, en)
	}
	if !matchesTokens("nginx on DB01 crashed", "db01 nginx") || matchesTokens("nginx on db01", "redis") {
		t.Fatal("token AND semantics broken")
	}
}

func TestCheckRowStateAndFilter(t *testing.T) {
	rows := []map[string]any{
		{"id": "a", "name": "web", "type": "http", "target": "https://x", "enabled": true, "ok": true, "checked_at": int64(10), "message": ""},
		{"id": "b", "name": "db", "type": "tcp", "target": "10.0.0.1:5432", "enabled": true, "ok": false, "checked_at": int64(10), "message": "timeout"},
		{"id": "c", "name": "new", "type": "process", "target": "h/nginx", "enabled": true, "ok": true, "checked_at": int64(0), "message": ""},
		{"id": "d", "name": "old", "type": "ping", "target": "1.1.1.1", "enabled": false, "ok": true, "checked_at": int64(10), "message": ""},
	}
	want := []string{"ok", "down", "pending", "disabled"}
	for i, m := range rows {
		if got := checkRowState(m); got != want[i] {
			t.Fatalf("row %d state %q want %q", i, got, want[i])
		}
	}
	n := 0
	for _, m := range rows {
		if checkRowMatches(m, checksQuery{state: "down"}) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("state=down should match 1, got %d", n)
	}
	n = 0
	for _, m := range rows {
		if checkRowMatches(m, checksQuery{typ: "tcp", page: listPage{q: "5432"}}) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("type+q should match 1, got %d", n)
	}
}

func TestActivityMatches(t *testing.T) {
	e := LogEntry{Timestamp: 1000, Kind: "operation", Level: "warning", Actor: "admin", IP: "10.0.0.9", Host: "db01 (10.0.0.1)", Message: "打开终端"}
	if !activityMatches(e, activityQuery{kind: "operation", level: "warning", host: "db01", since: 900, page: listPage{q: "终端 admin"}}) {
		t.Fatal("should match")
	}
	if activityMatches(e, activityQuery{since: 2000}) || activityMatches(e, activityQuery{kind: "plugin"}) {
		t.Fatal("should not match")
	}
}
