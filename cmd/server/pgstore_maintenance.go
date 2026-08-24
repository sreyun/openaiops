package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
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
	Name       string
	TotalBytes int64 // heap + indexes + TOAST + FSM/VM
	TableBytes int64 // pg_table_size: heap + TOAST + FSM/VM
	HeapBytes  int64 // pg_relation_size: main fork only — the bloat estimate's basis
	ToastBytes int64
	IndexBytes int64
	LiveTuples int64
	DeadTuples int64
	// RelTuples 是 pg_class.reltuples —— VACUUM/ANALYZE 会**同步**写进系统目录，
	// 而 n_live_tup 走异步的统计收集器，批量删除之后可能长时间停留在旧值。
	// 膨胀估算必须用前者：用后者会在刚清理完的表上把「应有大小」算成删除前的规模，
	// 从而把一张 90% 是空洞的表判成「几乎没有膨胀」。负数表示从未统计过（PG14+ 用 -1）。
	RelTuples   float64
	AvgRowWidth float64 // 来自 pg_stats；0 表示该表从未被 ANALYZE 过
	LastVacuum  sql.NullTime
	LastAutoVac sql.NullTime
}

// deadRatio is the share of row versions in this table that are dead right now.
//
// 这个数字只在「膨胀正在发生」时有意义，不能用它判断「膨胀是否已经存在」：
// (auto)VACUUM 清完死元组后 n_dead_tup 归零，但堆文件不会缩回给操作系统。
// 迁移 13 把这些写密集表的 autovacuum_vacuum_scale_factor 压到 0.01 之后，
// autovacuum 跟得更紧，死元组常年接近 0 —— 于是一个已经膨胀到几 GB 的库，
// 死元组占比却是 0.1%。只看这个比例会得出「没有膨胀」的错误结论。
// 判断存量膨胀要用 bloatBytes。
func (s pgTableStat) deadRatio() float64 {
	total := s.LiveTuples + s.DeadTuples
	if total <= 0 {
		return 0
	}
	return float64(s.DeadTuples) / float64(total)
}

// PostgreSQL 页内布局常量，用于从行数与平均行宽反推「这张表本该多大」。
const (
	pgPageSize = 8192
	// 页头 24B；此处再留出 FSM/对齐的余量，宁可低估膨胀也不要虚报。
	pgPageUsable = 8168
	// HeapTupleHeader 23B 向上对齐到 24B，加上页内 ItemIdData 指针 4B。
	pgTupleOverhead = 28
)

// estRows 是用于反推表大小的行数：优先 pg_class.reltuples（随 VACUUM/ANALYZE 同步
// 更新），统计收集器的 n_live_tup 仅作兜底。返回 0 表示无可用统计。
func (s pgTableStat) estRows() float64 {
	if s.RelTuples > 0 {
		return s.RelTuples
	}
	if s.RelTuples < 0 {
		return 0 // PG14+ 的 -1：从未 vacuum/analyze，任何估算都是猜测
	}
	if s.LiveTuples > 0 {
		return float64(s.LiveTuples)
	}
	return 0
}

// expectedHeapBytes 是按行数 × 平均行宽估算出的「无膨胀时的堆大小」。
// 返回 0 表示数据不足以估算（从未 ANALYZE、或统计信息里没有行）。
func (s pgTableStat) expectedHeapBytes() int64 {
	rows := s.estRows()
	if s.AvgRowWidth <= 0 || rows <= 0 {
		return 0
	}
	perRow := s.AvgRowWidth + pgTupleOverhead
	tuplesPerPage := float64(pgPageUsable) / perRow
	if tuplesPerPage < 1 {
		tuplesPerPage = 1 // 超宽行：一行一页（更宽的部分已经进 TOAST）
	}
	pages := math.Ceil(rows / tuplesPerPage)
	return int64(pages) * pgPageSize
}

// bloatBytes 估算主堆里已经无法归还给操作系统的空洞。
//
// 这是「存量膨胀」的正确度量：拿实际的主堆大小减去按行数反推的应有大小。
// 与 deadRatio 不同，它在 autovacuum 跑完之后依然成立 —— 那正是用户看到
// 「清理过了，pg-data 还是那么大」的场景。
//
// 只算主堆，不含 TOAST 与索引：TOAST 的膨胀形态不同（大对象整体重写），
// 索引膨胀要靠 REINDEX 而不是 VACUUM FULL，混在一起会虚报可回收量。
// 估不出来时返回 0，宁可漏报也不误导运维去承担一次 ACCESS EXCLUSIVE 锁。
func (s pgTableStat) bloatBytes() int64 {
	expected := s.expectedHeapBytes()
	if expected <= 0 || s.HeapBytes <= 0 {
		return 0
	}
	if b := s.HeapBytes - expected; b > 0 {
		return b
	}
	return 0
}

// bloatRatio 是主堆里空洞所占的比例。
func (s pgTableStat) bloatRatio() float64 {
	if s.HeapBytes <= 0 {
		return 0
	}
	return float64(s.bloatBytes()) / float64(s.HeapBytes)
}

func (p *pgStore) tableStats() ([]pgTableStat, error) {
	return p.tableStatsContext(context.Background())
}

// tableStatsContext 是带上下文的版本。/metrics 用它加超时：这条查询要扫
// pg_class/pg_stats，PG 一旦卡住，没有上下文的查询会把抓取协程永久挂住。
func (p *pgStore) tableStatsContext(ctx context.Context) ([]pgTableStat, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT c.relname,
		       pg_total_relation_size(c.oid),
		       pg_table_size(c.oid),
		       pg_relation_size(c.oid),
		       COALESCE(pg_total_relation_size(c.reltoastrelid), 0),
		       pg_indexes_size(c.oid),
		       COALESCE(st.n_live_tup, 0),
		       COALESCE(st.n_dead_tup, 0),
		       -- reltuples 随 VACUUM/ANALYZE 同步落目录，不像 n_live_tup 要等统计收集器；
		       -- 刚做完批量删除时只有它是准的。
		       c.reltuples,
		       -- 平均行宽：按列的非空占比加权，是从行数反推「应有大小」的关键输入。
		       -- 表从未 ANALYZE 时这里是 0，膨胀估算会相应放弃而不是虚报。
		       COALESCE((SELECT SUM((1 - sa.null_frac) * sa.avg_width)
		                 FROM pg_stats sa
		                 WHERE sa.schemaname = n.nspname AND sa.tablename = c.relname), 0),
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
		if err := rows.Scan(&s.Name, &s.TotalBytes, &s.TableBytes, &s.HeapBytes, &s.ToastBytes, &s.IndexBytes,
			&s.LiveTuples, &s.DeadTuples, &s.RelTuples, &s.AvgRowWidth, &s.LastVacuum, &s.LastAutoVac); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *pgStore) databaseSize() (int64, error) {
	return p.databaseSizeContext(context.Background())
}

func (p *pgStore) databaseSizeContext(ctx context.Context) (int64, error) {
	var n int64
	err := p.db.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&n)
	return n, err
}

func fmtTime(t sql.NullTime) string {
	if !t.Valid {
		return "never"
	}
	return t.Time.Format("2006-01-02 15:04")
}

// reclaimCandidates picks the tables worth a VACUUM FULL: meaningfully large AND
// meaningfully wasteful. Rewriting a table that is already compact only costs an
// exclusive lock for nothing.
//
// 两条判据缺一不可：
//   - deadRatio 抓「此刻正在膨胀」（autovacuum 还没追上）；
//   - bloatRatio 抓「已经膨胀完并被 vacuum 过」—— 死元组早已归零、堆文件却还留在
//     高水位。只用前者会对一个已经涨到几 GB 的库报「无需回收」，这正是之前
//     清理没能形成闭环的原因。
func reclaimCandidates(stats []pgTableStat) []pgTableStat {
	const (
		minBytes     = 16 << 20 // below 16 MiB the lock is not worth the few MB back
		minDeadFrac  = 0.20
		minBloatFrac = 0.30
		// 不管比例多高，回收量太小就不值得一把表锁。
		minBloatBytes = 8 << 20
	)
	var out []pgTableStat
	for _, s := range stats {
		if s.TotalBytes < minBytes {
			continue
		}
		byDead := s.deadRatio() >= minDeadFrac
		byBloat := s.bloatRatio() >= minBloatFrac && s.bloatBytes() >= minBloatBytes
		if byDead || byBloat {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalBytes > out[j].TotalBytes })
	return out
}

// estReclaimBytes is how much a VACUUM FULL on this table is expected to return
// to the filesystem: the measured heap bloat, or — when statistics are too thin
// to estimate it — the share implied by the current dead-tuple ratio.
func (s pgTableStat) estReclaimBytes() int64 {
	if b := s.bloatBytes(); b > 0 {
		return b
	}
	return int64(float64(s.TotalBytes) * s.deadRatio())
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
	fmt.Printf("%-26s %10s %10s %10s %9s %7s %10s %7s  %s\n",
		"TABLE", "TOTAL", "HEAP", "TOAST", "LIVE", "DEAD%", "BLOAT", "BLOAT%", "LAST AUTOVACUUM")
	fmt.Println(strings.Repeat("-", 122))
	for _, s := range stats {
		if s.TotalBytes < 64<<10 {
			continue // 忽略几十 KB 的小表，避免淹没真正的大头
		}
		bloat, bloatPct := "-", "-"
		if s.expectedHeapBytes() > 0 {
			bloat = humanBytes(s.bloatBytes())
			bloatPct = fmt.Sprintf("%.1f%%", s.bloatRatio()*100)
		}
		fmt.Printf("%-26s %10s %10s %10s %9d %6.1f%% %10s %7s  %s\n",
			s.Name, humanBytes(s.TotalBytes), humanBytes(s.HeapBytes), humanBytes(s.ToastBytes),
			s.LiveTuples, s.deadRatio()*100, bloat, bloatPct, fmtTime(s.LastAutoVac))
	}
	fmt.Println("\nBLOAT = 主堆实际大小 − 按行数与平均行宽反推的应有大小，即已经无法自行归还给")
	fmt.Println("操作系统的空洞。DEAD% 在 autovacuum 跑过之后会回到 0，但 BLOAT 不会——判断")
	fmt.Println("「清理了为什么还占着空间」要看 BLOAT 这一列。BLOAT 显示 - 表示该表尚未被")
	fmt.Println("ANALYZE 过，缺少估算所需的统计信息（可执行 ANALYZE <表名> 后重跑本报告）。")

	cands := reclaimCandidates(stats)
	fmt.Println()
	if len(cands) == 0 {
		fmt.Println("没有值得一次性回收的膨胀表（没有表同时满足 ≥16 MiB，且死元组占比 ≥20% 或")
		fmt.Println("主堆空洞 ≥30% 且 ≥8 MiB）。若数据库体积仍然偏大，请对照上表：")
		fmt.Println("  · BLOAT 普遍很小 → 确实是真实数据，应从保留策略入手（管理 → 保留策略）。")
		fmt.Println("  · 大表 BLOAT 显示 - → 先 ANALYZE 该表补齐统计信息，再重跑本报告。")
		fmt.Println("  · 索引占比高（TOTAL 远大于 HEAP+TOAST）→ 属于索引膨胀，需 REINDEX，")
		fmt.Println("    VACUUM FULL 顺带重建索引也能解决。")
		return
	}
	var reclaimable int64
	fmt.Println("以下表存在明显膨胀，建议执行一次性回收（aiops-server -pg-reclaim）：")
	for _, s := range cands {
		est := s.estReclaimBytes()
		reclaimable += est
		fmt.Printf("  %-26s %8s  死元组 %.1f%%  主堆空洞 %.1f%%  预计可回收约 %s\n",
			s.Name, humanBytes(s.TotalBytes), s.deadRatio()*100, s.bloatRatio()*100, humanBytes(est))
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
		fmt.Printf("VACUUM (FULL, ANALYZE) %s … (%s, 死元组 %.1f%%, 主堆空洞 %.1f%%)\n",
			s.Name, humanBytes(s.TotalBytes), s.deadRatio()*100, s.bloatRatio()*100)
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
