package sqltoolkit

import "testing"

// 行数上限的改写规则。这几条各自对应一个真实故障：
//   - 列名重复：套派生表会让 `SELECT a.id, b.id …` 直接报 1060；
//   - 上限失效：以 `-- 注释` 结尾时把 LIMIT 接在同一行会被注释吞掉；
//   - 上限误判：`limit_amount` 这样的列名让"已经有 LIMIT 了"成立，于是真的没有上限。
func TestApplyRowLimit(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		limit   int
		offset  int
		want    string
		applied bool
	}{
		{
			name: "普通 SELECT 追加 LIMIT（不套派生表，列名重复也不会报错）",
			in:   "SELECT a.id, b.id FROM a JOIN b ON a.k=b.k", limit: 200,
			want: "SELECT a.id, b.id FROM a JOIN b ON a.k=b.k\nLIMIT 200", applied: true,
		},
		{
			name: "带偏移",
			in:   "SELECT 1", limit: 50, offset: 100,
			want: "SELECT 1\nLIMIT 50 OFFSET 100", applied: true,
		},
		{
			name: "用户已经写了 LIMIT：尊重原样",
			in:   "SELECT * FROM t ORDER BY id LIMIT 10", limit: 200,
			want: "SELECT * FROM t ORDER BY id LIMIT 10", applied: false,
		},
		{
			name: "MySQL 的 LIMIT off, n 也算已有上限",
			in:   "SELECT * FROM t LIMIT 10, 20", limit: 200,
			want: "SELECT * FROM t LIMIT 10, 20", applied: false,
		},
		{
			name: "标准写法 FETCH FIRST … ONLY 同样算",
			in:   "SELECT * FROM t OFFSET 5 ROWS FETCH FIRST 10 ROWS ONLY", limit: 200,
			want: "SELECT * FROM t OFFSET 5 ROWS FETCH FIRST 10 ROWS ONLY", applied: false,
		},
		{
			name: "子查询里的 LIMIT 约束不了外层，仍要补",
			in:   "SELECT * FROM (SELECT id FROM t LIMIT 10) x JOIN y ON x.id=y.id", limit: 200,
			want: "SELECT * FROM (SELECT id FROM t LIMIT 10) x JOIN y ON x.id=y.id\nLIMIT 200", applied: true,
		},
		{
			name: "UNION 的前半段有 LIMIT，外层没有：要补",
			in:   "(SELECT a FROM t1 LIMIT 5) UNION ALL SELECT a FROM t2", limit: 200,
			want: "(SELECT a FROM t1 LIMIT 5) UNION ALL SELECT a FROM t2\nLIMIT 200", applied: true,
		},
		{
			name: "叫 limit_amount 的列名不是 LIMIT 子句",
			in:   "SELECT limit_amount FROM t", limit: 200,
			want: "SELECT limit_amount FROM t\nLIMIT 200", applied: true,
		},
		{
			name: "字符串里的 limit 不算",
			in:   "SELECT * FROM t WHERE note = 'no limit 10'", limit: 200,
			want: "SELECT * FROM t WHERE note = 'no limit 10'\nLIMIT 200", applied: true,
		},
		{
			name: "以行注释结尾：LIMIT 必须换行写，否则会被注释吞掉",
			in:   "SELECT * FROM t -- 只看前几行", limit: 200,
			want: "SELECT * FROM t -- 只看前几行\nLIMIT 200", applied: true,
		},
		{
			name: "SHOW 不吃 LIMIT，原样跑",
			in:   "SHOW TABLES", limit: 200,
			want: "SHOW TABLES", applied: false,
		},
		{
			name: "结尾的分号要去掉，否则 LIMIT 接在分号后面是语法错误",
			in:   "SELECT 1;", limit: 10,
			want: "SELECT 1\nLIMIT 10", applied: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, applied := ApplyRowLimit(c.in, c.limit, c.offset)
			if got != c.want || applied != c.applied {
				t.Fatalf("got %q (applied=%v)\nwant %q (applied=%v)", got, applied, c.want, c.applied)
			}
		})
	}
}

func TestHasOuterRowLimit(t *testing.T) {
	yes := []string{
		"SELECT 1 LIMIT 1",
		"SELECT 1 limit 10 offset 5",
		"SELECT 1 LIMIT 10, 20",
		"SELECT 1 FETCH FIRST 10 ROWS ONLY",
		"SELECT 1 LIMIT 1;",
		"SELECT 1 LIMIT 1  ",
	}
	for _, s := range yes {
		if !HasOuterRowLimit(s) {
			t.Errorf("%q 应当被判定为已有外层上限", s)
		}
	}
	no := []string{
		"SELECT limit_amount FROM t",
		"SELECT * FROM (SELECT 1 LIMIT 1) x",
		"SELECT * FROM t WHERE s='limit 10'",
		"SELECT 1",
	}
	for _, s := range no {
		if HasOuterRowLimit(s) {
			t.Errorf("%q 不该被判定为已有外层上限", s)
		}
	}
}
