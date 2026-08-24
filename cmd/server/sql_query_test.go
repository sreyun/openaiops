package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"aiops-monitor/cmd/server/sqltoolkit"
)

func TestClampSQLQueryTimeout(t *testing.T) {
	if clampSQLQueryTimeout(0) != 20*time.Second {
		t.Fatal("default")
	}
	if clampSQLQueryTimeout(5) != 5*time.Second {
		t.Fatal("5s")
	}
	if clampSQLQueryTimeout(120) != 60*time.Second {
		t.Fatal("cap 60")
	}
}

func TestWorkbenchQueryRejectsPlaceholders(t *testing.T) {
	if !sqltoolkit.HasUnboundPlaceholder("SELECT * FROM t WHERE id = ?") {
		t.Fatal("expected placeholder")
	}
	if sqltoolkit.HasUnboundPlaceholder("SELECT * FROM user LIMIT 10") {
		t.Fatal("plain select")
	}
}

// 只读闸与库名校验现在统一由 prepareSQLRead 把关（两个驱动共用一条路径），
// 原来 mysqlWorkbenchQuery/pgWorkbenchQuery 那两份重复实现已经没有调用方，随本次清理删除。
// 这里直接盯住现役那条路径，别让覆盖率跟着死代码一起消失。
func TestPrepareSQLReadGuards(t *testing.T) {
	ctx := context.Background()
	conn := MySQLConnection{Host: "127.0.0.1", Port: 1, Driver: "mysql"}

	_, err := prepareSQLRead(ctx, conn, sqlReadRequest{SQL: "DELETE FROM t", Schema: "db"}, 50, 2*time.Second)
	if err == nil || (!strings.Contains(err.Error(), "只读") && !strings.Contains(err.Error(), "允许")) {
		t.Fatalf("写语句应被只读闸拦下，得到 %v", err)
	}

	_, err = prepareSQLRead(ctx, conn, sqlReadRequest{SQL: "SELECT 1", Schema: "bad-name!"}, 50, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "非法") {
		t.Fatalf("非法库名应被拒绝，得到 %v", err)
	}

	_, err = prepareSQLRead(ctx, conn, sqlReadRequest{SQL: "SELECT * FROM t WHERE id = ?", Schema: "db"}, 50, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "占位符") {
		t.Fatalf("未绑定占位符应被拒绝，得到 %v", err)
	}

	pg := MySQLConnection{Host: "127.0.0.1", Port: 1, Driver: "postgres"}
	_, err = prepareSQLRead(ctx, pg, sqlReadRequest{SQL: "UPDATE t SET a=1", Schema: "public"}, 50, 2*time.Second)
	if err == nil || (!strings.Contains(err.Error(), "只读") && !strings.Contains(err.Error(), "允许")) {
		t.Fatalf("PostgreSQL 写语句应被拦下，得到 %v", err)
	}
}
