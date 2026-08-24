package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// fakeRows 只实现 rowsErrSource——noteRowsErr 用接口而不是 *sql.Rows，
// 正是为了让这条诊断路径不必真的连一次数据库就能测。
type fakeRows struct{ err error }

func (f fakeRows) Err() error { return f.err }

// captureSlog 把默认 logger 换成写进 buffer 的 handler，返回还原函数。
func captureSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return buf, func() { slog.SetDefault(prev) }
}

func TestNoteRowsErr(t *testing.T) {
	t.Run("nil 结果集不告警也不 panic", func(t *testing.T) {
		buf, restore := captureSlog(t)
		defer restore()
		noteRowsErr("rowsDiagNilRows", nil)
		var typed rowsErrSource // 显式的 nil 接口值
		noteRowsErr("rowsDiagNilIface", typed)
		// 按 op 断言而不是按"缓冲区为空"：包里别的测试可能有后台 goroutine
		// 也在往默认 logger 写，按空断言会偶发性地假红。
		if out := buf.String(); strings.Contains(out, "rowsDiagNil") {
			t.Fatalf("nil 结果集不该产生日志：%s", out)
		}
	})

	t.Run("读完整的结果集不告警", func(t *testing.T) {
		buf, restore := captureSlog(t)
		defer restore()
		noteRowsErr("rowsDiagClean", fakeRows{})
		if out := buf.String(); strings.Contains(out, "rowsDiagClean") {
			t.Fatalf("Err() 为 nil 时不该告警：%s", out)
		}
	})

	t.Run("中途断掉时带 op 告警", func(t *testing.T) {
		buf, restore := captureSlog(t)
		defer restore()
		noteRowsErr("rowsDiagBroken", fakeRows{err: errors.New("connection reset by peer")})
		out := buf.String()
		// op 是这条日志的全部价值：没有它就只知道"某处的结果集断了"。
		if !strings.Contains(out, "rowsDiagBroken") {
			t.Fatalf("告警里必须带 op，实际：%s", out)
		}
		if !strings.Contains(out, "connection reset by peer") {
			t.Fatalf("告警里必须带原始错误，实际：%s", out)
		}
	})
}

// clampTermCommandPage 是分页的唯一判据：handler 回显给前端的 limit 与查询实际用的
// limit 必须是同一个值，否则前端按回显算总页数，翻到后面就是空白页。
func TestClampTermCommandPage(t *testing.T) {
	cases := []struct {
		name                  string
		limit, offset         int
		wantLimit, wantOffset int
	}{
		{"缺省值走默认", 0, 0, 100, 0},
		{"负数走默认", -5, -3, 100, 0},
		{"超上限收敛到默认", 1000, 0, 100, 0},
		{"上限边界保留", 500, 20, 500, 20},
		{"正常值原样保留", 25, 50, 25, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotLimit, gotOffset := clampTermCommandPage(c.limit, c.offset)
			if gotLimit != c.wantLimit || gotOffset != c.wantOffset {
				t.Fatalf("clampTermCommandPage(%d, %d) = (%d, %d)，期望 (%d, %d)",
					c.limit, c.offset, gotLimit, gotOffset, c.wantLimit, c.wantOffset)
			}
		})
	}
}
