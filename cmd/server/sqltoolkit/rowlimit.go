package sqltoolkit

import (
	"fmt"
	"regexp"
	"strings"
)

// 只读查询的行数上限：**追加 LIMIT**，而不是把整条语句套进派生表。
//
// 原来的做法是 `SELECT * FROM (<用户 SQL>) AS _aiops_q LIMIT n`。它有两个真问题：
//
//  1. **列名重复直接报错**。`SELECT a.id, b.id FROM a JOIN b …` 单独跑没事，套进派生表就是
//     MySQL 1060 "Duplicate column name 'id'"。用户看到的是"这条 SQL 在客户端能跑，
//     在面板里就报错"，而错误信息里连派生表都看不见。
//  2. **多一层物化**。派生表会让优化器少掉一些下推机会，大结果集上白白多一次拷贝。
//
// 追加 LIMIT 没有这两个问题：语义等价、优化器照常下推、列名原样。
//
// 判断"用户是不是已经自己写了 LIMIT"只看**语句尾部**：子查询里的 LIMIT 约束不了外层结果，
// 而 `… LIMIT 10) UNION SELECT …` 这种尾部没有 LIMIT 的语句仍然需要我们兜底。
// 旧代码用 strings.Contains(sql, "limit") 一刀切，一个叫 limit_amount 的列名就能让上限失效。

var (
	// 结尾的 LIMIT：MySQL 的 `LIMIT n` / `LIMIT off, n`，以及 `LIMIT n OFFSET m`。
	reTailLimit = regexp.MustCompile(`(?is)\blimit\s+\d+(\s*,\s*\d+)?(\s+offset\s+\d+)?\s*$`)
	// SQL 标准写法：`OFFSET n ROWS FETCH FIRST/NEXT m ROWS ONLY`（PostgreSQL 支持）。
	reTailFetch = regexp.MustCompile(`(?is)\bfetch\s+(first|next)\b.*\bonly\s*$`)
)

// HasOuterRowLimit 报告语句最外层是否已经带了行数上限。
func HasOuterRowLimit(sqlText string) bool {
	stripped := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(StripCommentsAndStrings(sqlText)), ";"))
	if stripped == "" {
		return false
	}
	return reTailLimit.MatchString(stripped) || reTailFetch.MatchString(stripped)
}

// ApplyRowLimit 给只读查询补上行数上限（必要时再加偏移），返回改写后的 SQL 与是否改写过。
//
// 不改写的情况：语句本身已有外层 LIMIT（尊重用户写的）、或者是 SHOW/DESC 这类不吃 LIMIT
// 的语句。追加时**先换行再写 LIMIT**：用户的 SQL 可能以 `-- 注释` 结尾，直接接在后面会被
// 注释吞掉，变成一条没有上限的查询——那正是最危险的一种"看起来生效了"。
func ApplyRowLimit(sqlText string, limit, offset int) (string, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sqlText), ";"))
	if trimmed == "" || limit <= 0 {
		return trimmed, false
	}
	// 括号开头的联合查询（`(SELECT …) UNION ALL SELECT …`）同样是可以加 LIMIT 的查询，
	// 但首个词是 "("。判首词之前先把开头的括号剥掉。
	switch FirstKeyword(strings.TrimLeft(trimmed, "( \t\r\n")) {
	case "select", "with", "values", "table":
	default:
		return trimmed, false // SHOW / DESC / EXPLAIN：原样跑
	}
	if HasOuterRowLimit(trimmed) {
		return trimmed, false
	}
	out := trimmed + "\nLIMIT " + fmt.Sprint(limit)
	if offset > 0 {
		out += " OFFSET " + fmt.Sprint(offset)
	}
	return out, true
}
