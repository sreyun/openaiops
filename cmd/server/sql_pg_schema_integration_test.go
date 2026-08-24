package main

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 这条用例复现的是「SQL 工具点运行就报错、什么结果也没有」。
//
// 根因在前端：PostgreSQL 连接的下拉框列的是 **schema**，而它默认选中的是连接配置里的
// **库名**（把库名 unshift 进 schema 列表再"因为列表里有"而选中）。运行时这个名字被
// 设成 search_path，于是每一句 SELECT 都报 relation ... does not exist。
//
// 服务端这一侧的问题是"错误来得太晚"：set_config 对不存在的 schema 照单全收，
// 错误一路推迟到用户那句 SELECT，长得像"我的表没了"。这里钉住两件事：
// 选错 schema 时**在准备阶段就报错并列出可选值**；选对时能正常出数据。
//
// 需要 AIOPS_TEST_SQLPG_DSN（形如 postgres://user:pass@host:5432/db?sslmode=disable）。
func sqlTestConn(t *testing.T) MySQLConnection {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("AIOPS_TEST_SQLPG_DSN"))
	if dsn == "" {
		t.Skip("AIOPS_TEST_SQLPG_DSN 未设置，跳过 SQL 工作台 PostgreSQL 用例")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN 解析失败: %v", err)
	}
	pw, _ := u.User.Password()
	port := 5432
	if p := u.Port(); p != "" {
		if n, e := strconv.Atoi(p); e == nil {
			port = n
		}
	}
	return MySQLConnection{
		Driver:   "postgres",
		Host:     u.Hostname(),
		Port:     port,
		User:     u.User.Username(),
		Password: pw,
		Database: strings.TrimPrefix(u.Path, "/"),
		TLS:      "disable",
	}
}

func TestPrepareSQLReadRejectsUnknownPGSchemaIntegration(t *testing.T) {
	c := sqlTestConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 前端旧行为送来的正是**库名**当 schema——它几乎不会同时是一个 schema 名。
	_, err := prepareSQLRead(ctx, c, sqlReadRequest{
		SQL: "SELECT * FROM demo", Schema: c.Database,
	}, 100, 10*time.Second)
	if err == nil {
		t.Fatal("库名被当成 schema 时应当在准备阶段就报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "schema") || !strings.Contains(msg, c.Database) {
		t.Fatalf("报错要说清是 schema 选错了，实际：%s", msg)
	}
	// 必须把可选值列出来，否则用户仍然不知道该填什么。
	if !strings.Contains(msg, "public") {
		t.Fatalf("报错里应列出可选 schema（至少有 public），实际：%s", msg)
	}
}

func TestPrepareSQLReadRunsWithCorrectPGSchemaIntegration(t *testing.T) {
	c := sqlTestConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess, err := prepareSQLRead(ctx, c, sqlReadRequest{
		SQL: "SELECT id, name FROM demo ORDER BY id", Schema: "public",
	}, 100, 10*time.Second)
	if err != nil {
		t.Fatalf("选对 schema 后应当能准备成功：%v", err)
	}
	defer sess.close()

	rows, err := sess.conn.QueryContext(ctx, sess.execSQL)
	if err != nil {
		t.Fatalf("查询应当成功：%v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("读取结果集出错：%v", err)
	}
	if n == 0 {
		t.Fatal("应当读到数据行（用例库里预置了两行）")
	}
}
