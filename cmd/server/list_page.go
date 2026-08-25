package main

import (
	"net/http"
	"strconv"
	"strings"
)

// 通用列表分页参数（与 alerts_page.go 同一份契约）：带 limit 才进入分页模式，
// 响应体保持数组，总数走 X-Total-Count。见 docs/superpowers/plans/2026-08-24-scale-5000-program.md。
type listPage struct {
	limit, offset int
	q             string
}

const listPageMaxLimit = 500

func parseListPage(r *http.Request) (listPage, bool) {
	qs := r.URL.Query()
	raw := strings.TrimSpace(qs.Get("limit"))
	if raw == "" {
		return listPage{}, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		n = 50
	}
	if n > listPageMaxLimit {
		n = listPageMaxLimit
	}
	off, _ := strconv.Atoi(qs.Get("offset"))
	if off < 0 {
		off = 0
	}
	return listPage{limit: n, offset: off, q: strings.ToLower(strings.TrimSpace(qs.Get("q")))}, true
}

// pageBounds 返回 [start, end) 并把越界 offset 钳成空页而不是 500。
func pageBounds(total int, p listPage) (int, int) {
	start := p.offset
	if start > total {
		start = total
	}
	end := start + p.limit
	if end > total {
		end = total
	}
	return start, end
}

// matchesTokens：多词 AND，大小写不敏感（与前端 shared/search.ts、经典版 matchesSearchTokens 同语义）。
func matchesTokens(hay, q string) bool {
	if q == "" {
		return true
	}
	hay = strings.ToLower(hay)
	for _, tok := range strings.Fields(q) {
		if !strings.Contains(hay, tok) {
			return false
		}
	}
	return true
}

func setTotalHeader(w http.ResponseWriter, total int) {
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	w.Header().Set("Access-Control-Expose-Headers", "X-Total-Count, X-List-Total, X-List-Down")
}

// ---- /activity ----

// activityQuery：kind / level / host / since（unix 秒）/ q。
type activityQuery struct {
	page  listPage
	kind  string
	level string
	host  string
	since int64
}

func parseActivityQuery(r *http.Request) (activityQuery, bool) {
	p, ok := parseListPage(r)
	if !ok {
		return activityQuery{}, false
	}
	qs := r.URL.Query()
	since, _ := strconv.ParseInt(qs.Get("since"), 10, 64)
	return activityQuery{
		page:  p,
		kind:  strings.ToLower(strings.TrimSpace(qs.Get("kind"))),
		level: strings.ToLower(strings.TrimSpace(qs.Get("level"))),
		host:  strings.ToLower(strings.TrimSpace(qs.Get("host"))),
		since: since,
	}, true
}

func activityMatches(e LogEntry, q activityQuery) bool {
	if q.kind != "" && q.kind != "all" && strings.ToLower(e.Kind) != q.kind {
		return false
	}
	if q.level != "" && q.level != "all" && strings.ToLower(e.Level) != q.level {
		return false
	}
	if q.since > 0 && e.Timestamp < q.since {
		return false
	}
	if q.host != "" && !strings.Contains(strings.ToLower(e.Host), q.host) {
		return false
	}
	return matchesTokens(strings.Join([]string{e.Actor, e.Username, e.IP, e.Host, e.Message, e.Kind, e.Level}, " "), q.page.q)
}

// ---- /checks ----

// checksQuery：type / state(ok|down|pending|disabled) / q。
type checksQuery struct {
	page  listPage
	typ   string
	state string
}

func parseChecksQuery(r *http.Request) (checksQuery, bool) {
	p, ok := parseListPage(r)
	if !ok {
		return checksQuery{}, false
	}
	qs := r.URL.Query()
	return checksQuery{
		page:  p,
		typ:   strings.ToLower(strings.TrimSpace(qs.Get("type"))),
		state: strings.ToLower(strings.TrimSpace(qs.Get("state"))),
	}, true
}

// checkRowState 把一行检查归到 ok / down / pending / disabled 四态之一。
func checkRowState(m map[string]any) string {
	if en, _ := m["enabled"].(bool); !en {
		return "disabled"
	}
	if at, _ := m["checked_at"].(int64); at == 0 {
		return "pending"
	}
	if ok, _ := m["ok"].(bool); ok {
		return "ok"
	}
	return "down"
}

func checkRowMatches(m map[string]any, q checksQuery) bool {
	if q.typ != "" && q.typ != "all" {
		if t, _ := m["type"].(string); strings.ToLower(t) != q.typ {
			return false
		}
	}
	if q.state != "" && q.state != "all" && checkRowState(m) != q.state {
		return false
	}
	if q.page.q == "" {
		return true
	}
	name, _ := m["name"].(string)
	target, _ := m["target"].(string)
	msg, _ := m["message"].(string)
	typ, _ := m["type"].(string)
	id, _ := m["id"].(string)
	return matchesTokens(strings.Join([]string{name, target, msg, typ, id}, " "), q.page.q)
}
