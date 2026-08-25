package main

import (
	"sort"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// ============================================================================
// Log aggregation — the second observability pillar.
//
// Agents tail their configured log sources and POST batches here; the server
// keeps a capped in-memory ring that operators (and the AI inspector) can search
// by host / level / keyword / time. Logs are high-volume and ephemeral, so they
// are deliberately NOT persisted — after a restart they simply re-accumulate.
// ============================================================================

// StoredLog is one collected log line, enriched with the source hostname.
type StoredLog struct {
	Ts       int64  `json:"ts"`
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname"`
	Source   string `json:"source"`
	Level    string `json:"level"`
	Message  string `json:"message"`
}

const logStoreCap = 50000

// logPersistCap bounds how many recent lines get written to PG. Memory keeps the
// full ring for search; persistence only needs a warm tail to survive restart,
// so the periodic blob stays small enough to avoid heavy WAL churn.
const logPersistCap = 8000

type logStore struct {
	// 读写锁而不是互斥锁：写者只有 ingest 一个，检索/统计/取错误行全是只读。
	// 500 个日志采集器持续写入时，用互斥锁会让「两个人同时搜日志」也互相排队，
	// 而搜索恰恰是这里最慢的操作。
	mu   sync.RWMutex
	logs []StoredLog
}

func newLogStore() *logStore { return &logStore{} }

// normalizeLevel collapses assorted level spellings to error|warn|info|debug.
func normalizeLevel(l string) string {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "error", "err", "fatal", "panic", "crit", "critical", "emerg", "alert":
		return "error"
	case "warn", "warning":
		return "warn"
	case "debug", "trace":
		return "debug"
	default:
		return "info"
	}
}

func (ls *logStore) ingest(hostID, hostname string, lines []shared.LogLine) {
	if len(lines) == 0 {
		return
	}
	ls.mu.Lock()
	for _, l := range lines {
		msg := l.Message
		if len(msg) > 4000 {
			msg = msg[:4000]
		}
		ls.logs = append(ls.logs, StoredLog{
			Ts: l.Ts, HostID: hostID, Hostname: hostname,
			Source: l.Source, Level: normalizeLevel(l.Level), Message: msg,
		})
	}
	if len(ls.logs) > logStoreCap {
		ls.logs = ls.logs[len(ls.logs)-logStoreCap:]
	}
	ls.mu.Unlock()
}

// search returns matching logs newest-first (up to limit).
func (ls *logStore) search(hostID, level, keyword string, since int64, limit int) []StoredLog {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	kw := strings.ToLower(strings.TrimSpace(keyword))
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	out := make([]StoredLog, 0, limit)
	for i := len(ls.logs) - 1; i >= 0 && len(out) < limit; i-- {
		l := ls.logs[i]
		if hostID != "" && l.HostID != hostID {
			continue
		}
		if level != "" && l.Level != level {
			continue
		}
		if since > 0 && l.Ts < since {
			continue
		}
		if kw != "" && !containsSubstrFold(l.Message, kw) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// searchPage returns paginated matching logs newest-first plus total count.
// offset = (page-1) * pageSize; limit = pageSize.
//
// **一趟扫完**。原来是两趟：先整环扫一遍数总数，再整环扫一遍取当前页。
// 计数与取页本来就能在同一趟里完成：匹配到就计数，落在当前页窗口里就顺手收下。
//
// 收益取决于命中分布，实测（5 万条环、-benchtime 200x，见 logstore_bench_test.go）：
//
//   - 命中很多、看第一页：两趟的第二趟很快就凑够一页提前退出，**基本没有差别**。
//   - 命中稀少或集中在很旧的记录里（"搜一个上周才出现过的错误码"）、或者翻到很深的
//     页码：第二趟也要把整个环走完，于是老代码是 112 ms、新代码 40 ms。
//
// 也就是说这不是普适的两倍加速，而是**砍掉了最坏情况**——恰好日志检索的最坏情况就是
// 用户最着急的那一次（翻历史找线索），而且它发生在持锁期间，会一并挡住日志摄入。
// allow 是主机可见性谓词（nil 表示不限制）：受主机组/标签授权约束的账号只能看到
// 自己范围内那些主机的日志。必须**在计数之前**过滤，否则 total 与分页会把越权条目
// 算进去——用户虽然看不到内容，却能从总数和页码推断出范围外有多少条。
func (ls *logStore) searchPage(hostID, level, keyword string, since int64, page, pageSize int, allow func(string) bool) ([]StoredLog, int) {
	if pageSize <= 0 || pageSize > 2000 {
		pageSize = 50
	}
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	kw := strings.ToLower(strings.TrimSpace(keyword))

	ls.mu.RLock()
	defer ls.mu.RUnlock()

	total := 0
	out := make([]StoredLog, 0, pageSize)
	for i := len(ls.logs) - 1; i >= 0; i-- {
		l := ls.logs[i]
		if hostID != "" && l.HostID != hostID {
			continue
		}
		if allow != nil && !allow(l.HostID) {
			continue
		}
		if level != "" && l.Level != level {
			continue
		}
		if since > 0 && l.Ts < since {
			continue
		}
		if kw != "" && !containsSubstrFold(l.Message, kw) {
			continue
		}
		// total 必须把**所有**匹配都数进去（分页控件要靠它算总页数），
		// 所以即使当前页已经收满也不能提前退出。
		if total >= offset && len(out) < pageSize {
			out = append(out, l)
		}
		total++
	}
	return out, total
}

// logStats holds aggregated statistics for the current search scope.
type logStats struct {
	ByLevel          map[string]int `json:"by_level"`
	TopHosts         []logHostCount `json:"top_hosts"`
	TimeDistribution map[string]int `json:"time_distribution"`
}

type logHostCount struct {
	Hostname string `json:"hostname"`
	HostID   string `json:"host_id"`
	Count    int    `json:"count"`
}

// searchStats aggregates stats for matching logs (level breakdown, top hosts, time distribution).
// allow 语义同 searchPage。这里尤其要紧：统计里的 TopHosts 直接就是一串主机名，
// 不过滤等于把授权范围外的主机清单原样发出去。
func (ls *logStore) searchStats(hostID, level, keyword string, since int64, allow func(string) bool) logStats {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	now := time.Now().Unix()

	ls.mu.RLock()
	defer ls.mu.RUnlock()

	stats := logStats{
		ByLevel:          map[string]int{"error": 0, "warn": 0, "info": 0, "debug": 0},
		TimeDistribution: map[string]int{"1h": 0, "6h": 0, "24h": 0},
	}
	hostCounts := map[string]struct {
		hostname string
		count    int
	}{}

	for i := len(ls.logs) - 1; i >= 0; i-- {
		l := ls.logs[i]
		if hostID != "" && l.HostID != hostID {
			continue
		}
		if allow != nil && !allow(l.HostID) {
			continue
		}
		// 统计面板刻意不按 level 过滤：ByLevel 需展示所有级别的总数（需求：筛选某级别时其他级别
		// 保留总数）；TopHosts / 时间分布同样按整体口径统计。level 过滤只作用于日志列表(searchPage)。
		if since > 0 && l.Ts < since {
			// Still count for time distribution if within 24h window
			if l.Ts < now-86400 {
				continue
			}
		}
		if kw != "" && !containsSubstrFold(l.Message, kw) {
			continue
		}

		// Level breakdown
		stats.ByLevel[l.Level]++

		// Host distribution
		if l.Hostname != "" {
			hc := hostCounts[l.Hostname]
			hc.hostname = l.Hostname
			hc.count++
			hostCounts[l.Hostname] = hc
		}

		// Time distribution
		diff := now - l.Ts
		switch {
		case diff <= 3600:
			stats.TimeDistribution["1h"]++
		case diff <= 21600:
			stats.TimeDistribution["6h"]++
		case diff <= 86400:
			stats.TimeDistribution["24h"]++
		}
	}

	// Top 5 hosts
	type hc struct {
		hn, hid string
		n       int
	}
	var sorted []hc
	for _, v := range hostCounts {
		sorted = append(sorted, hc{v.hostname, "", v.count})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].n > sorted[j].n })
	for i := 0; i < len(sorted) && i < 5; i++ {
		stats.TopHosts = append(stats.TopHosts, logHostCount{
			Hostname: sorted[i].hn,
			HostID:   sorted[i].hid,
			Count:    sorted[i].n,
		})
	}

	return stats
}

// recentErrors returns up to limit error/warn lines since a timestamp (AI input).
func (ls *logStore) recentErrors(since int64, limit int) []StoredLog {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	out := []StoredLog{}
	for i := len(ls.logs) - 1; i >= 0 && len(out) < limit; i-- {
		l := ls.logs[i]
		if l.Ts < since {
			continue
		}
		if l.Level == "error" || l.Level == "warn" {
			out = append(out, l)
		}
	}
	return out
}

// errorCount counts error lines since a timestamp (for AI/UI).
func (ls *logStore) errorCount(since int64) int {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	n := 0
	for _, l := range ls.logs {
		if l.Ts >= since && l.Level == "error" {
			n++
		}
	}
	return n
}

func (ls *logStore) count() int {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return len(ls.logs)
}

// export returns a snapshot copy of the most recent logPersistCap lines for PG
// persistence (chronological order).
func (ls *logStore) export() []StoredLog {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	src := ls.logs
	if len(src) > logPersistCap {
		src = src[len(src)-logPersistCap:]
	}
	out := make([]StoredLog, len(src))
	copy(out, src)
	return out
}

// importLogs restores the log buffer from PG on startup (capped to logStoreCap).
func (ls *logStore) importLogs(logs []StoredLog) {
	if len(logs) == 0 {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if len(logs) > logStoreCap {
		logs = logs[len(logs)-logStoreCap:]
	}
	ls.logs = logs
}
