package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

// PostgreSQL 存储体积的诊断与一次性回收。
//
// 背景：周期刷写在 PG 里留下的不是数据，是**膨胀**。PG 是 MVCC，每次 UPDATE/DELETE
// 都写一个新版本并把旧版本标记为死元组；autovacuum 回收后空间只是「可复用」，堆文件
// 本身不会缩回给操作系统。于是一套 500 台的机群会出现 pg-data 9.3 GiB 而 vm-data
// 只有 93 MiB 的反差——真正的存量数据只有几百 MB。
//
// 写路径的修复（pgstore_writecache.go + saveHosts 的快慢双周期）只能保证「以后不再
// 涨」。已经积下来的那几 GB 需要一次性回收，而回收手段（VACUUM FULL）会持
// ACCESS EXCLUSIVE 锁、重写整张表，绝不能放在启动路径上悄悄执行——所以这里做成显式
// 的离线子命令：
//
//	aiops-server -pg-report    # 只读诊断：每张表多大、死元组多少、autovacuum 何时跑过
//	aiops-server -pg-reclaim   # 一次性回收：对膨胀表执行 VACUUM (FULL, ANALYZE)
//
// 两个子命令都读 AIOPS_POSTGRES_DSN，跑完即退出，不启动任何服务。

// pgTableStat is one row of the storage diagnostic.
type pgTableStat struct {
	Name        string
	TotalBytes  int64 // heap + indexes + TOAST
	TableBytes  int64 // heap only
	ToastBytes  int64
	IndexBytes  int64
	LiveTuples  int64
	DeadTuples  int64
	LastVacuum  sql.NullTime
	LastAutoVac sql.NullTime
}

// deadRatio is the share of row versions in this table that are dead. It is the
// single most useful number here: a small table with a high ratio is exactly the
// "rewritten every cycle" pattern, and it is what VACUUM FULL reclaims.
func (s pgTableStat) deadRatio() float64 {
	total := s.LiveTuples + s.DeadTuples
	if total <= 0 {
		return 0
	}
	return float64(s.DeadTuples) / float64(total)
}

func (p *pgStore) tableStats() ([]pgTableStat, error) {
	rows, err := p.db.Query(`
		SELECT c.relname,
		       pg_total_relation_size(c.oid),
		       pg_table_size(c.oid),
		       COALESCE(pg_total_relation_size(c.reltoastrelid), 0),
		       pg_indexes_size(c.oid),
		       COALESCE(st.n_live_tup, 0),
		       COALESCE(st.n_dead_tup, 0),
		       st.last_vacuum,
		       st.last_autovacuum
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_stat_user_tables st ON st.relid = c.oid
		-- current_schema()，不是写死的 'public'：服务端的表就住在 search_path 的首个
		-- schema 里，部署可以自定义。写死 public 会让诊断在这类部署上报出「什么表都没有」。
		WHERE n.nspname = current_schema() AND c.relkind IN ('r','p')
		ORDER BY pg_total_relation_size(c.oid) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pgTableStat
	for rows.Next() {
		var s pgTableStat
		if err := rows.Scan(&s.Name, &s.TotalBytes, &s.TableBytes, &s.ToastBytes, &s.IndexBytes,
			&s.LiveTuples, &s.DeadTuples, &s.LastVacuum, &s.LastAutoVac); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *pgStore) databaseSize() (int64, error) {
	var n int64
	err := p.db.QueryRow(`SELECT pg_database_size(current_database())`).Scan(&n)
	return n, err
}

func fmtTime(t sql.NullTime) string {
	if !t.Valid {
		return "never"
	}
	return t.Time.Format("2006-01-02 15:04")
}

// reclaimCandidates picks the tables worth a VACUUM FULL: meaningfully large AND
// meaningfully dead. Rewriting a table that is already compact only costs an
// exclusive lock for nothing.
func reclaimCandidates(stats []pgTableStat) []pgTableStat {
	const (
		minBytes    = 16 << 20 // below 16 MiB the lock is not worth the few MB back
		minDeadFrac = 0.20
	)
	var out []pgTableStat
	for _, s := range stats {
		if s.TotalBytes >= minBytes && s.deadRatio() >= minDeadFrac {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalBytes > out[j].TotalBytes })
	return out
}

// openPGForMaintenance connects using the same DSN the server uses. Unlike
// pgFromEnv it never returns nil silently — a maintenance command that quietly
// did nothing would be worse than one that fails loudly.
func openPGForMaintenance() *pgStore {
	dsn := strings.TrimSpace(os.Getenv("AIOPS_POSTGRES_DSN"))
	if dsn == "" {
		log.Fatal("AIOPS_POSTGRES_DSN 未配置：维护子命令需要与服务端相同的 PostgreSQL DSN")
	}
	ps, err := openPGStore(dsn)
	if err != nil {
		log.Fatalf("PostgreSQL 连接失败: %v", err)
	}
	return ps
}

// runPGReport prints a read-only storage diagnostic and exits. Safe at any time.
func runPGReport() {
	ps := openPGForMaintenance()
	defer ps.close()

	total, err := ps.databaseSize()
	if err != nil {
		log.Fatalf("读取数据库体积失败: %v", err)
	}
	stats, err := ps.tableStats()
	if err != nil {
		log.Fatalf("读取表统计失败: %v", err)
	}

	fmt.Printf("PostgreSQL 数据库总体积: %s\n\n", humanBytes(total))
	fmt.Printf("%-26s %10s %10s %10s %9s %9s %7s  %s\n",
		"TABLE", "TOTAL", "HEAP", "TOAST", "LIVE", "DEAD", "DEAD%", "LAST AUTOVACUUM")
	fmt.Println(strings.Repeat("-", 108))
	for _, s := range stats {
		if s.TotalBytes < 64<<10 {
			continue // 忽略几十 KB 的小表，避免淹没真正的大头
		}
		fmt.Printf("%-26s %10s %10s %10s %9d %9d %6.1f%%  %s\n",
			s.Name, humanBytes(s.TotalBytes), humanBytes(s.TableBytes), humanBytes(s.ToastBytes),
			s.LiveTuples, s.DeadTuples, s.deadRatio()*100, fmtTime(s.LastAutoVac))
	}

	cands := reclaimCandidates(stats)
	fmt.Println()
	if len(cands) == 0 {
		fmt.Println("没有需要一次性回收的膨胀表（没有表同时满足 ≥16 MiB 且死元组占比 ≥20%）。")
		fmt.Println("如果数据库体积仍然偏大，那是真实数据，不是膨胀。")
		return
	}
	var reclaimable int64
	fmt.Println("以下表存在明显膨胀，建议执行一次性回收（aiops-server -pg-reclaim）：")
	for _, s := range cands {
		est := int64(float64(s.TotalBytes) * s.deadRatio())
		reclaimable += est
		fmt.Printf("  %-26s %8s  死元组 %.1f%%  预计可回收约 %s\n",
			s.Name, humanBytes(s.TotalBytes), s.deadRatio()*100, humanBytes(est))
	}
	fmt.Printf("\n预计总计可回收约 %s。\n", humanBytes(reclaimable))
	fmt.Println("注意：VACUUM FULL 会对每张表持 ACCESS EXCLUSIVE 锁并重写整表，期间该表读写全部阻塞。")
	fmt.Println("请在维护窗口执行，并确保磁盘剩余空间大于最大单表体积（回收期间新旧两份并存）。")
}

// runPGReclaim performs the one-time space reclaim and exits.
//
// 刻意做成独立子命令而不是启动时自动执行：VACUUM FULL 会重写整表并持
// ACCESS EXCLUSIVE 锁，在一个几 GB 的库上可能要几分钟，把它塞进启动路径等于让每次
// 重启都变成一次不可预期的停机。什么时候承担这个锁，是运维的决定，不是程序的。
func runPGReclaim(only string) {
	ps := openPGForMaintenance()
	defer ps.close()

	stats, err := ps.tableStats()
	if err != nil {
		log.Fatalf("读取表统计失败: %v", err)
	}
	before, _ := ps.databaseSize()

	var targets []pgTableStat
	if want := strings.TrimSpace(only); want != "" {
		wanted := map[string]bool{}
		for _, name := range strings.Split(want, ",") {
			if n := strings.TrimSpace(name); n != "" {
				wanted[n] = true
			}
		}
		for _, s := range stats {
			if wanted[s.Name] {
				targets = append(targets, s)
				delete(wanted, s.Name)
			}
		}
		for n := range wanted {
			log.Fatalf("表 %q 在 public schema 中不存在，已中止（未执行任何回收）", n)
		}
	} else {
		targets = reclaimCandidates(stats)
	}
	if len(targets) == 0 {
		fmt.Println("没有需要回收的表，未执行任何操作。")
		return
	}

	fmt.Printf("回收前数据库体积: %s\n", humanBytes(before))
	for _, s := range targets {
		fmt.Printf("VACUUM (FULL, ANALYZE) %s … (%s, 死元组 %.1f%%)\n",
			s.Name, humanBytes(s.TotalBytes), s.deadRatio()*100)
		start := time.Now()
		// 表名来自 pg_class，不是用户输入；仍然用 quote_ident 语义包一层双引号，
		// 避免大小写/保留字表名被误解析。
		if _, err := ps.db.Exec(`VACUUM (FULL, ANALYZE) "` + strings.ReplaceAll(s.Name, `"`, `""`) + `"`); err != nil {
			// 单表失败不应中断整轮回收：锁等待超时、磁盘不足都只影响这一张表。
			fmt.Printf("  失败: %v（跳过，继续处理其余表）\n", err)
			continue
		}
		fmt.Printf("  完成，用时 %s\n", time.Since(start).Round(time.Second))
	}

	after, _ := ps.databaseSize()
	fmt.Printf("\n回收后数据库体积: %s", humanBytes(after))
	if before > after {
		fmt.Printf("（释放 %s）", humanBytes(before-after))
	}
	fmt.Println()
}
