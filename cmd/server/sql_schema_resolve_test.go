package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 不在界面上显式选库，也必须能跑查询——这是最常见的一种用法（打开工具，敲 SQL，回车）。
//
// 这条曾经是必现故障：firstNonEmpty 全空时返回 "-"（它本是硬件报表里"空字段显示短横"
// 的展示辅助），于是 schema 变成 "-"，跳过了"从 SQL 推断"和"用连接自带库名"两个兜底，
// 最后被库名正则挡下，用户看到的是一句与真实原因毫不相干的「非法库名 / schema 名」。
// 四个只读查询接口（/query、/query/stream 与 sql_api 的两处）当时都是这个写法。
func TestPrepareSQLReadFallsBackToConnectionDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cases := []struct {
		name string
		conn MySQLConnection
		req  sqlReadRequest
	}{
		{"postgres：请求没带库名，连接带了", MySQLConnection{Driver: "postgres", Host: "127.0.0.1", Port: 1, Database: "aiops", User: "u"}, sqlReadRequest{SQL: "SELECT 1 AS a"}},
		{"mysql：请求没带库名，连接带了", MySQLConnection{Driver: "mysql", Host: "127.0.0.1", Port: 1, Database: "verifydb", User: "u"}, sqlReadRequest{SQL: "SELECT 1 AS a"}},
		{"两边都没库名：也不该报非法库名", MySQLConnection{Driver: "mysql", Host: "127.0.0.1", Port: 1, User: "u"}, sqlReadRequest{SQL: "SELECT 1 AS a"}},
		{"库名从 SQL 里推断", MySQLConnection{Driver: "mysql", Host: "127.0.0.1", Port: 1, User: "u"}, sqlReadRequest{SQL: "SELECT * FROM shop.orders"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 端口 1 必然连不上：能走到"连接失败"就说明校验这一关过了，
			// 这里要断言的正是"没有被库名校验误挡"。
			_, err := prepareSQLRead(ctx, c.conn, c.req, 10, time.Second)
			if err != nil && strings.Contains(err.Error(), "非法库名") {
				t.Fatalf("库名校验误挡：%v", err)
			}
		})
	}
}

// 真正非法的库名仍然要挡住——这条防的是上面那个修复被改过头。
func TestPrepareSQLReadStillRejectsInjectedSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := MySQLConnection{Driver: "mysql", Host: "127.0.0.1", Port: 1, User: "u"}
	for _, bad := range []string{"db`; DROP TABLE x; --", "a b", "db-name", "库名", "x'y"} {
		_, err := prepareSQLRead(ctx, conn, sqlReadRequest{SQL: "SELECT 1", Schema: bad}, 10, time.Second)
		if err == nil || !strings.Contains(err.Error(), "非法库名") {
			t.Fatalf("非法库名 %q 没有被挡下：%v", bad, err)
		}
	}
}

// 两个辅助函数的语义必须泾渭分明：一个给展示（空就画短横），一个给取值（空就是空）。
func TestFirstNonEmptyVariants(t *testing.T) {
	if got := firstNonEmpty("", "  ", ""); got != "" {
		t.Fatalf("取值语义应当返回空串，实际 %q", got)
	}
	if got := firstNonEmpty("", "b"); got != "b" {
		t.Fatalf("应当返回 b，实际 %q", got)
	}
	if got := firstNonEmptyOrDash("", "  "); got != "-" {
		t.Fatalf("展示语义应当返回短横，实际 %q", got)
	}
	if got := firstNonEmptyOrDash("a", "b"); got != "a" {
		t.Fatalf("应当返回 a，实际 %q", got)
	}
}
