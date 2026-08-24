package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 流式查询 / 导出的边界。这些常量直接决定"数据量大的时候会不会把谁拖死"：
// 服务端内存、浏览器 DOM、导出文件大小各有各的天花板，不能共用一个数。
func TestSQLReadLimitsAreBounded(t *testing.T) {
	if got := clampSQLStreamLimit(0); got != 1000 {
		t.Fatalf("默认行数 = %d, want 1000", got)
	}
	if got := clampSQLStreamLimit(999999); got != sqlStreamMaxRows {
		t.Fatalf("上限没有收敛：%d", got)
	}
	if got := clampSQLExportLimit(0); got != 100000 {
		t.Fatalf("导出默认行数 = %d", got)
	}
	if got := clampSQLExportLimit(9_999_999); got != sqlExportMaxRows {
		t.Fatalf("导出上限没有收敛：%d", got)
	}
	// 导出可以比交互查询宽松得多：它只写 CSV，不进内存也不进 DOM。
	if sqlExportMaxRows <= sqlStreamMaxRows {
		t.Fatal("导出上限应当明显高于交互查询上限")
	}
	if clampSQLOffset(-5) != 0 {
		t.Fatal("负偏移要归零")
	}
	if clampSQLOffset(1<<40) > 100_000_000 {
		t.Fatal("偏移要有上限，否则等于让数据库扫全表")
	}
}

// 文件名要进 Content-Disposition 响应头：换行或引号能把响应头整个撬开
// （HTTP 响应拆分）。这条守的是那个注入面。
func TestSanitizeExportFilename(t *testing.T) {
	cases := []struct{ in, wantContains, wantNotContains string }{
		{in: `report`, wantContains: "report.csv"},
		{in: `re"port`, wantContains: "re_port.csv", wantNotContains: `"`},
		{in: "a\r\nX-Injected: 1", wantContains: ".csv", wantNotContains: "\n"},
		{in: `../../etc/passwd`, wantContains: ".csv", wantNotContains: "/"},
		{in: `订单导出`, wantContains: ".csv"},
		{in: strings.Repeat("x", 500), wantContains: ".csv"},
	}
	for _, c := range cases {
		got := sanitizeExportFilename(c.in)
		if !strings.Contains(got, c.wantContains) {
			t.Errorf("sanitizeExportFilename(%q) = %q，应当包含 %q", c.in, got, c.wantContains)
		}
		if c.wantNotContains != "" && strings.Contains(got, c.wantNotContains) {
			t.Errorf("sanitizeExportFilename(%q) = %q，不该包含 %q", c.in, got, c.wantNotContains)
		}
		if len(got) > 100 {
			t.Errorf("文件名过长：%d", len(got))
		}
	}
	if sanitizeExportFilename("") == ".csv" {
		t.Error("空文件名要给一个带时间戳的默认名")
	}
}

// 单元格要截断：一行 BLOB / 超长 JSON 能把浏览器直接拖死，也能把导出文件撑爆。
func TestSQLCellValueTruncatesHugeText(t *testing.T) {
	huge := strings.Repeat("A", sqlCellMaxBytes*2)
	got, ok := sqlCellValue(huge, false).(string)
	if !ok {
		t.Fatalf("类型不对：%T", sqlCellValue(huge, false))
	}
	if len(got) > sqlCellMaxBytes+32 {
		t.Fatalf("没有截断：%d 字节", len(got))
	}
	if !strings.HasSuffix(got, "(truncated)") {
		t.Fatalf("截断了却没告诉用户：%q", got[len(got)-20:])
	}
	// 正常大小的值必须原样返回，不能被这条截断逻辑改写。
	if v := sqlCellValue("hello", false); v != "hello" {
		t.Fatalf("正常值被改写了：%v", v)
	}
	if v := sqlCellValue(nil, false); v != nil {
		t.Fatalf("NULL 要保持为 nil，实际 %v", v)
	}
}

// 准备阶段就要挡掉写操作、非法库名、未填参数——这些都不该跑到数据库那一步。
// 用一个必然连不上的地址：如果校验漏了，测试会因为"试图连接"而超时/报连接错误，
// 那本身就是失败信号。
func TestPrepareSQLReadRejectsUnsafeInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := MySQLConnection{Host: "127.0.0.1", Port: 1, Database: "db", User: "u"}

	cases := []struct {
		name    string
		req     sqlReadRequest
		wantSub string
	}{
		{"写操作", sqlReadRequest{SQL: "DELETE FROM t", Schema: "db"}, "只读"},
		{"更新操作", sqlReadRequest{SQL: "UPDATE t SET a=1", Schema: "db"}, "只读"},
		{"非法库名", sqlReadRequest{SQL: "SELECT 1", Schema: "bad-name!"}, "非法"},
		{"空 SQL", sqlReadRequest{SQL: "   ", Schema: "db"}, "请先输入"},
		{"未填占位符", sqlReadRequest{SQL: "SELECT * FROM t WHERE id = ?", Schema: "db"}, "占位符"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := prepareSQLRead(ctx, conn, c.req, 100, 2*time.Second)
			if err == nil {
				t.Fatal("应当被拒绝")
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("错误信息没说清原因：%v", err)
			}
		})
	}
}

// 超时/取消必须说人话：光甩一句 context deadline exceeded，用户会以为是面板坏了，
// 而实际动作是"调大超时"或"给查询加索引"。
func TestSQLFriendlyError(t *testing.T) {
	deadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	msg := sqlFriendlyError(context.DeadlineExceeded, deadline)
	if !strings.Contains(msg, "超时") || !strings.Contains(msg, "EXPLAIN") {
		t.Fatalf("超时提示没有给出下一步动作：%q", msg)
	}

	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if got := sqlFriendlyError(context.Canceled, cctx); !strings.Contains(got, "取消") {
		t.Fatalf("取消提示不对：%q", got)
	}
	if got := sqlFriendlyError(nil, context.Background()); got != "" {
		t.Fatalf("没有错误时不该编一个出来：%q", got)
	}
}

// CSV 导出的每一格都是业务库里的任意内容。Excel / WPS / LibreOffice / Numbers
// 打开 CSV 时会把 = + - @ 开头的格子当公式求值——判据必须和新版控制台
// src/shared/export.ts 的 rowsToCSV、Android 的 HyperVExport 完全一致，
// 尤其是"纯数字放行"这条：把 -12 变成 '-12 会把整列数值毁掉。
func TestNeutralizeCSVFormula(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"normal", "normal"},
		{"web-01", "web-01"},
		{"=cmd|' /c calc'!A1", "'=cmd|' /c calc'!A1"},
		{"+1+1", "'+1+1"},
		{"@SUM(A1:A9)", "'@SUM(A1:A9)"},
		{"-2+3", "'-2+3"},
		{"\tleading tab", "'\tleading tab"},
		{"\rleading cr", "'\rleading cr"},
		// 数据不是公式：带符号的整数 / 小数原样保留数值语义。
		{"-12", "-12"},
		{"+3.5", "+3.5"},
		{"0", "0"},
		{"-0.001", "-0.001"},
		// 看着像数字但不是：仍要中和。
		{"-1.2.3", "'-1.2.3"},
		{"-1e9", "'-1e9"},
	}
	for _, c := range cases {
		if got := neutralizeCSVFormula(c.in); got != c.want {
			t.Errorf("neutralizeCSVFormula(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
