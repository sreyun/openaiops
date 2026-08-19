package main

import (
	"fmt"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

func seedLogStore(n int) *logStore {
	ls := newLogStore()
	// 真实日志的形状：几百字节一行，含时间戳、级别、模块名与一段消息。
	body := strings.Repeat(benchLogLine, 6)
	lines := make([]shared.LogLine, 0, 1000)
	for i := 0; i < n; i++ {
		lines = append(lines, shared.LogLine{
			Ts: int64(1700000000 + i), Source: "/var/log/app.log", Level: "info",
			Message: fmt.Sprintf("2026-08-18T00:00:%02d app[%d] %s", i%60, i%2000, body),
		})
		if len(lines) == 1000 {
			ls.ingest(fmt.Sprintf("host-%d", i%500), "h", lines)
			lines = lines[:0]
		}
	}
	if len(lines) > 0 {
		ls.ingest("host-0", "h", lines)
	}
	return ls
}

// 关键字检索是最贵的一条路径：它要对每一条日志正文做一次匹配。
func BenchmarkLogSearchKeyword(b *testing.B) {
	ls := seedLogStore(logStoreCap)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := ls.search("", "", "EXHAUSTED", 0, 200); len(out) == 0 {
			b.Fatal("expected matches")
		}
	}
}

// 分页检索要扫两遍（先数总数再取当页），代价加倍。
func BenchmarkLogSearchPageKeyword(b *testing.B) {
	ls := seedLogStore(logStoreCap)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, total := ls.searchPage("", "", "EXHAUSTED", 0, 1, 50); total == 0 {
			b.Fatal("expected matches")
		}
	}
}
