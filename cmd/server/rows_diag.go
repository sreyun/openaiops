package main

import "log/slog"

// rowsErrSource 是 *sql.Rows 的最小面：只要能报 Err() 就能被诊断。
// 用接口而不是 *sql.Rows，是为了让单测不必真的连一次数据库。
type rowsErrSource interface{ Err() error }

// noteRowsErr 记录"结果集在迭代中途断了"。
//
// `for rows.Next() { ... }` 会在**出错**和**读完**两种情况下同样地退出循环，
// 不查 rows.Err() 就分不清这两者。于是连接在读一半时断掉，代码会把已经读到的
// 那几行当成完整答案返回——SQL 工作台报"这个库没有 MyISAM 表"、用量看板报出
// 少算的 token、事件列表少几条，全都长得和"确实就这么多"一模一样。
//
// 这里刻意**不改变返回值**：这些查询几乎都是尽力而为的探测（不少还写成
// `if rs, err := db.Query(...); err == nil` —— 整条查询失败本来就被跳过），
// 把中途出错升级成硬错误会让一台老版本 MySQL 直接打掉整块面板。留一条带
// op 的告警，让"数据看着不对"这件事在日志里有据可查。
func noteRowsErr(op string, rows rowsErrSource) {
	if rows == nil {
		return
	}
	if err := rows.Err(); err != nil {
		slog.Warn("结果集提前结束，返回数据可能不完整", "op", op, "err", err)
	}
}
