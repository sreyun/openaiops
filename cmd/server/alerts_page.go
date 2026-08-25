package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// 服务端分页契约（第 2 期，见 docs/superpowers/plans/2026-08-24-scale-5000-program.md）。
//
// 上万条告警时两个控制台都只**渲染**一页，但 GET /alerts 仍然整份返回：每个控制台
// 每 30 秒（经典版 5 秒）几 MB。契约按 8 月 8 日路线图：
//
//   - 不带 limit：行为完全不变，返回整个数组（经典版、概览、实时 store 都还这么用）。
//   - 带 limit：服务端筛选 + 排序 + 切片，响应体**仍是数组**（经典版 Array.isArray），
//     总数与全量计数走响应头，不改变形状：
//       X-Total-Count     筛选后的总数（分页器用）
//       X-Alert-Total / X-Alert-Critical / X-Alert-Warning / X-Alert-Active   全量计数（KPI 用）
//       X-Alert-Types     "cpu:12,disk:3,…" 全量按类型计数（类型 chip 用）
//   - GET /alerts/summary：同一份计数的 JSON 版，给不需要行的地方（角标、看板）用。
//
// 筛选参数：level / status(active|acknowledged|silenced|resolved|all) / type / host（主机名、
// IP、ID 子串）/ scope / q（多词 AND，对主机、IP、分组、正文、类型、范围、级别）。
// 排序：sort=level|since|host|type|scope|message|status|timestamp，order=asc|desc。

type alertPageQuery struct {
	limit, offset int
	level, status string
	typ, host     string
	scope, q      string
	sortKey       string
	order         string
}

const alertPageMaxLimit = 500

// parseAlertPageQuery 只有在请求带 limit 时才进入分页模式（ok=true）。
func parseAlertPageQuery(r *http.Request) (alertPageQuery, bool) {
	qs := r.URL.Query()
	raw := strings.TrimSpace(qs.Get("limit"))
	if raw == "" {
		return alertPageQuery{}, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		n = 50
	}
	if n > alertPageMaxLimit {
		n = alertPageMaxLimit
	}
	off, _ := strconv.Atoi(qs.Get("offset"))
	if off < 0 {
		off = 0
	}
	lower := func(k string) string { return strings.ToLower(strings.TrimSpace(qs.Get(k))) }
	return alertPageQuery{
		limit: n, offset: off,
		level: lower("level"), status: lower("status"), typ: lower("type"),
		host: lower("host"), scope: lower("scope"), q: lower("q"),
		sortKey: lower("sort"), order: lower("order"),
	}, true
}

func alertMatchesQuery(a Alert, q alertPageQuery) bool {
	if q.level != "" && q.level != "all" && strings.ToLower(a.Level) != q.level {
		return false
	}
	switch q.status {
	case "", "all":
	case "active":
		if a.Status != "" {
			return false
		}
	default:
		if strings.ToLower(a.Status) != q.status {
			return false
		}
	}
	if q.typ != "" && q.typ != "all" && strings.ToLower(a.Type) != q.typ {
		return false
	}
	if q.host != "" {
		hay := strings.ToLower(a.Hostname + " " + a.IP + " " + a.HostID)
		if !strings.Contains(hay, q.host) {
			return false
		}
	}
	if q.scope != "" && !strings.Contains(strings.ToLower(a.Scope), q.scope) {
		return false
	}
	if q.q != "" {
		hay := strings.ToLower(strings.Join([]string{a.Hostname, a.IP, a.Group, a.Message, a.HostID, a.Type, a.Scope, a.Level}, " "))
		for _, tok := range strings.Fields(q.q) {
			if !strings.Contains(hay, tok) {
				return false
			}
		}
	}
	return true
}

func filterAlertsByQuery(list []Alert, q alertPageQuery) []Alert {
	out := make([]Alert, 0, len(list))
	for _, a := range list {
		if alertMatchesQuery(a, q) {
			out = append(out, a)
		}
	}
	return out
}

func alertLevelRank(level string) int {
	switch strings.ToLower(level) {
	case "critical", "crit":
		return 3
	case "warning", "warn":
		return 2
	case "info":
		return 1
	}
	return 0
}

// sortAlertsBy 稳定排序；key 为空时保持 Evaluate 的原始顺序。
func sortAlertsBy(list []Alert, key, order string) {
	if key == "" {
		return
	}
	desc := order != "asc"
	less := func(i, j int) bool {
		a, b := list[i], list[j]
		var c int
		switch key {
		case "level":
			c = alertLevelRank(a.Level) - alertLevelRank(b.Level)
		case "since":
			// 不能写成 int(a.Since - b.Since)：Unix 秒相减在 32 位上会截断，
			// 相距足够远的两条告警会比出反的结果。
			if a.Since > b.Since {
				c = 1
			} else if a.Since < b.Since {
				c = -1
			} else {
				c = 0
			}
		case "timestamp":
			if a.Timestamp > b.Timestamp {
				c = 1
			} else if a.Timestamp < b.Timestamp {
				c = -1
			}
		case "host":
			c = strings.Compare(strings.ToLower(a.Hostname), strings.ToLower(b.Hostname))
		case "type":
			c = strings.Compare(a.Type, b.Type)
		case "scope":
			c = strings.Compare(a.Scope, b.Scope)
		case "message":
			c = strings.Compare(a.Message, b.Message)
		case "status":
			c = strings.Compare(a.Status, b.Status)
		default:
			return false
		}
		if desc {
			return c > 0
		}
		return c < 0
	}
	sort.SliceStable(list, less)
}

type alertSummary struct {
	Total        int            `json:"total"`
	Critical     int            `json:"critical"`
	Warning      int            `json:"warning"`
	Info         int            `json:"info"`
	Active       int            `json:"active"`
	Acknowledged int            `json:"acknowledged"`
	Silenced     int            `json:"silenced"`
	Resolved     int            `json:"resolved"`
	ByType       map[string]int `json:"by_type"`
	TopHosts     []alertHostHit `json:"top_hosts"`
}

type alertHostHit struct {
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname"`
	Count    int    `json:"count"`
}

func summarizeAlerts(list []Alert) alertSummary {
	s := alertSummary{Total: len(list), ByType: map[string]int{}}
	hosts := map[string]*alertHostHit{}
	for _, a := range list {
		switch alertLevelRank(a.Level) {
		case 3:
			s.Critical++
		case 2:
			s.Warning++
		case 1:
			s.Info++
		}
		switch a.Status {
		case "":
			s.Active++
		case "acknowledged":
			s.Acknowledged++
		case "silenced":
			s.Silenced++
		case "resolved":
			s.Resolved++
		}
		s.ByType[strings.ToLower(a.Type)]++
		if a.HostID != "" {
			h := hosts[a.HostID]
			if h == nil {
				h = &alertHostHit{HostID: a.HostID, Hostname: a.Hostname}
				hosts[a.HostID] = h
			}
			h.Count++
		}
	}
	for _, h := range hosts {
		s.TopHosts = append(s.TopHosts, *h)
	}
	sort.Slice(s.TopHosts, func(i, j int) bool {
		if s.TopHosts[i].Count != s.TopHosts[j].Count {
			return s.TopHosts[i].Count > s.TopHosts[j].Count
		}
		return s.TopHosts[i].HostID < s.TopHosts[j].HostID
	})
	if len(s.TopHosts) > 10 {
		s.TopHosts = s.TopHosts[:10]
	}
	if s.TopHosts == nil {
		s.TopHosts = []alertHostHit{}
	}
	return s
}

func alertTypesHeader(byType map[string]int) string {
	keys := make([]string, 0, len(byType))
	for k := range byType {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, byType[k]))
	}
	return strings.Join(parts, ",")
}

// writeAlertPage 输出一页告警：响应体是数组，计数走头。
func writeAlertPage(w http.ResponseWriter, all []Alert, q alertPageQuery) {
	sum := summarizeAlerts(all)
	filtered := filterAlertsByQuery(all, q)
	sortAlertsBy(filtered, q.sortKey, q.order)
	total := len(filtered)
	start := q.offset
	if start > total {
		start = total
	}
	end := start + q.limit
	if end > total {
		end = total
	}
	page := filtered[start:end]
	if page == nil {
		page = []Alert{}
	}
	h := w.Header()
	h.Set("X-Total-Count", strconv.Itoa(total))
	h.Set("X-Alert-Total", strconv.Itoa(sum.Total))
	h.Set("X-Alert-Critical", strconv.Itoa(sum.Critical))
	h.Set("X-Alert-Warning", strconv.Itoa(sum.Warning))
	h.Set("X-Alert-Active", strconv.Itoa(sum.Active))
	h.Set("X-Alert-Types", alertTypesHeader(sum.ByType))
	h.Set("Access-Control-Expose-Headers", "X-Total-Count, X-Alert-Total, X-Alert-Critical, X-Alert-Warning, X-Alert-Active, X-Alert-Types")
	writeJSON(w, http.StatusOK, page)
}

// handleAlertsSummary: GET /api/v1/alerts/summary —— 计数不拉行。
func (s *Server) handleAlertsSummary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, summarizeAlerts(s.collectAlerts(r)))
}
