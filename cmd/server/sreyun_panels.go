package main

import (
	"fmt"
	"strings"
	"time"
)

func (h *SreyunCore) registerPanelTools() {
	h.tools["query_dashboard_panel"] = SreyunTool{
		Name: "query_dashboard_panel",
		Description: "执行仪表盘中某个面板的查询，并在对话中渲染对应组件（趋势图/指标卡/表格/日志）。" +
			"用户说「看这张看板的 CPU 面板」「把某某面板数据调出来」时调用。" +
			"可先 get_dashboard 拿 panel_id / 标题。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dashboard_id": map[string]string{"type": "string", "description": "看板 ID"},
				"panel_id":     map[string]any{"type": "integer", "description": "面板 ID（优先）"},
				"panel_title":  map[string]string{"type": "string", "description": "面板标题（panel_id 未知时模糊匹配）"},
				"range":        map[string]string{"type": "string", "description": "时间范围 1h/6h/24h/7d，默认 6h"},
			},
			"required": []string{"dashboard_id"},
		},
		Execute: h.execQueryDashboardPanel,
	}
	h.tools["list_dashboard_panels"] = SreyunTool{
		Name:        "list_dashboard_panels",
		Description: "列出指定看板的全部面板（id/标题/类型/查询摘要），便于随后 query_dashboard_panel。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dashboard_id": map[string]string{"type": "string", "description": "看板 ID"},
			},
			"required": []string{"dashboard_id"},
		},
		Execute: h.execListDashboardPanels,
	}
}

func (h *SreyunCore) execListDashboardPanels(args map[string]any) (string, error) {
	id, _ := args["dashboard_id"].(string)
	d, ok := h.s.cfg.DashboardByID(strings.TrimSpace(id))
	if !ok {
		return capabilityJSON(capabilityResult{OK: false, Error: "看板不存在"}), nil
	}
	type row struct {
		ID    int    `json:"panel_id"`
		Title string `json:"title"`
		Type  string `json:"type"`
		Query string `json:"query,omitempty"`
		DS    string `json:"data_source,omitempty"`
	}
	out := make([]row, 0, len(d.Panels))
	for _, p := range d.Panels {
		q := ""
		if len(p.Targets) > 0 {
			q = p.Targets[0].Expr
		}
		ds := p.DataSource
		if ds == "" {
			ds = d.DataSource
		}
		out = append(out, row{ID: p.ID, Title: p.Title, Type: p.Type, Query: q, DS: ds})
	}
	return capabilityJSON(capabilityResult{
		OK:        true,
		Summary:   fmt.Sprintf("看板「%s」共 %d 个面板", d.Name, len(out)),
		Data:      map[string]any{"dashboard_id": d.ID, "name": d.Name, "panels": out},
		UIActions: []map[string]any{openDashboardAction(d.ID, d.Name)},
	}), nil
}

func findDashPanel(d Dashboard, panelID int, title string) (DashPanel, bool) {
	if panelID > 0 {
		for _, p := range d.Panels {
			if p.ID == panelID {
				return p, true
			}
		}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return DashPanel{}, false
	}
	low := strings.ToLower(title)
	var fuzzy DashPanel
	fuzzyOK := false
	for _, p := range d.Panels {
		if strings.EqualFold(p.Title, title) {
			return p, true
		}
		if strings.Contains(strings.ToLower(p.Title), low) {
			fuzzy, fuzzyOK = p, true
		}
	}
	return fuzzy, fuzzyOK
}

func panelDataSource(d Dashboard, p DashPanel) string {
	if strings.TrimSpace(p.DataSource) != "" {
		return p.DataSource
	}
	return d.DataSource
}

func (h *SreyunCore) execQueryDashboardPanel(args map[string]any) (string, error) {
	dashID, _ := args["dashboard_id"].(string)
	title, _ := args["panel_title"].(string)
	rangeRaw, _ := args["range"].(string)
	panelID := 0
	switch v := args["panel_id"].(type) {
	case float64:
		panelID = int(v)
	case int:
		panelID = v
	}
	d, ok := h.s.cfg.DashboardByID(strings.TrimSpace(dashID))
	if !ok {
		return capabilityJSON(capabilityResult{OK: false, Error: "看板不存在"}), nil
	}
	p, ok := findDashPanel(d, panelID, title)
	if !ok {
		return capabilityJSON(capabilityResult{OK: false, Error: "面板不存在，请 list_dashboard_panels"}), nil
	}
	from, to, rangeLabel := parseChartRange(rangeRaw, 6)
	dsID := panelDataSource(d, p)
	actions := []map[string]any{
		openDashboardAction(d.ID, d.Name),
		drillDownAction("打开看板面板", "dashboard", map[string]any{"dashboard_id": d.ID, "panel_id": p.ID}),
	}
	ptype := strings.ToLower(strings.TrimSpace(p.Type))
	switch ptype {
	case "timeseries", "graph":
		chart, errMsg := h.queryPanelTimeseries(dsID, p, from, to)
		if errMsg != "" {
			return capabilityJSON(capabilityResult{OK: false, Error: errMsg, UIActions: actions}), nil
		}
		cid := fmt.Sprintf("panel_%s_%d", d.ID, p.ID)
		actions = append([]map[string]any{showChartAction(cid, "查看面板图", p.Title, chart, map[string]any{
			"kind": "dashboard_panel", "dashboard_id": d.ID, "panel_id": p.ID, "range": rangeLabel,
		})}, actions...)
		return capabilityJSON(capabilityResult{
			OK: true, Summary: fmt.Sprintf("已渲染面板「%s」趋势（%s）", p.Title, rangeLabel),
			Data:      map[string]any{"dashboard_id": d.ID, "panel_id": p.ID, "type": p.Type, "range": rangeLabel},
			UIActions: actions,
		}), nil
	case "stat", "gauge", "bargauge":
		val, unit, spark, errMsg := h.queryPanelStat(dsID, p, from, to)
		if errMsg != "" {
			return capabilityJSON(capabilityResult{OK: false, Error: errMsg, UIActions: actions}), nil
		}
		if unit == "" {
			unit = p.Unit
		}
		sid := fmt.Sprintf("stat_%s_%d", d.ID, p.ID)
		actions = append([]map[string]any{showStatAction(sid, "查看指标", p.Title, val, unit, spark, nil)}, actions...)
		return capabilityJSON(capabilityResult{
			OK: true, Summary: fmt.Sprintf("面板「%s」当前值 %.3g %s", p.Title, val, unit),
			Data:      map[string]any{"dashboard_id": d.ID, "panel_id": p.ID, "value": val, "unit": unit},
			UIActions: actions,
		}), nil
	case "table":
		cols, rows, errMsg := h.queryPanelTable(dsID, p, from, to)
		if errMsg != "" {
			return capabilityJSON(capabilityResult{OK: false, Error: errMsg, UIActions: actions}), nil
		}
		tid := fmt.Sprintf("table_%s_%d", d.ID, p.ID)
		actions = append([]map[string]any{showTableAction(tid, "查看表格", p.Title, cols, rows)}, actions...)
		return capabilityJSON(capabilityResult{
			OK: true, Summary: fmt.Sprintf("面板「%s」表格 %d 列 / %d 行", p.Title, len(cols), len(rows)),
			Data:      map[string]any{"dashboard_id": d.ID, "panel_id": p.ID, "columns": cols, "row_count": len(rows)},
			UIActions: actions,
		}), nil
	case "logs":
		lines, errMsg := h.queryPanelLogs(dsID, p, from, to)
		if errMsg != "" {
			return capabilityJSON(capabilityResult{OK: false, Error: errMsg, UIActions: actions}), nil
		}
		lid := fmt.Sprintf("logs_%s_%d", d.ID, p.ID)
		actions = append([]map[string]any{showLogsAction(lid, "查看日志", p.Title, lines)}, actions...)
		return capabilityJSON(capabilityResult{
			OK: true, Summary: fmt.Sprintf("面板「%s」日志 %d 行", p.Title, len(lines)),
			Data:      map[string]any{"dashboard_id": d.ID, "panel_id": p.ID, "line_count": len(lines)},
			UIActions: actions,
		}), nil
	case "text":
		return capabilityJSON(capabilityResult{
			OK: true, Summary: "文本面板内容如下", Answer: strings.TrimSpace(p.Text),
			Data:      map[string]any{"dashboard_id": d.ID, "panel_id": p.ID, "type": "text"},
			UIActions: actions,
		}), nil
	default:
		return capabilityJSON(capabilityResult{
			OK:        false,
			Error:     fmt.Sprintf("面板类型 %q 暂不支持内嵌渲染（raw=%s），可打开看板查看", p.Type, p.RawType),
			UIActions: actions,
		}), nil
	}
}

func (h *SreyunCore) queryPanelTimeseries(dsID string, p DashPanel, from, to int64) (map[string]any, string) {
	if len(p.Targets) == 0 || strings.TrimSpace(p.Targets[0].Expr) == "" {
		return nil, "面板无查询表达式"
	}
	if !h.s.dashBackendReady(dsID) {
		return nil, "数据源不可用"
	}
	rangeSec := to - from
	step := rangeSec / 300
	if step < 15 {
		step = 15
	}
	var all []promMatrix
	for i, t := range p.Targets {
		if i >= 6 {
			break
		}
		expr := substituteVars(t.Expr, nil, step, rangeSec)
		series, ok := h.s.dashRangeSeries(dsID, expr, from, to, step)
		if !ok {
			continue
		}
		all = append(all, series...)
	}
	if len(all) == 0 {
		return nil, "查询失败或无数据"
	}
	return promMatrixToChatChart(all, p.Title, 6), ""
}

func (h *SreyunCore) queryPanelStat(dsID string, p DashPanel, from, to int64) (float64, string, [][2]float64, string) {
	if len(p.Targets) == 0 || strings.TrimSpace(p.Targets[0].Expr) == "" {
		return 0, "", nil, "面板无查询表达式"
	}
	if !h.s.dashBackendReady(dsID) {
		return 0, "", nil, "数据源不可用"
	}
	// 与看板前端同一套语义：$__range/$__interval 按调用方给的窗口展开，求值时刻取窗口
	// 右端。此前写死 60/3600 并在 now 求值，AI 被要求「看过去 7 天」时实际只统计 1 小时。
	evalAt, stepSec, rangeSecVar := instantQueryWindow(from, to)
	expr := substituteVars(p.Targets[0].Expr, nil, stepSec, rangeSecVar)
	vec, ok := h.s.dashVectorAt(dsID, expr, evalAt)
	if !ok || len(vec) == 0 {
		return 0, "", nil, "瞬时查询失败或无数据"
	}
	val := vec[0].Value
	var spark [][2]float64
	rangeSec := to - from
	step := rangeSec / 60
	if step < 15 {
		step = 15
	}
	if series, ok := h.s.dashRangeSeries(dsID, expr, from, to, step); ok && len(series) > 0 {
		pts := series[0].Points
		if len(pts) > 60 {
			pts = pts[len(pts)-60:]
		}
		spark = pts
	}
	return val, p.Unit, spark, ""
}

func (h *SreyunCore) queryPanelTable(dsID string, p DashPanel, from, to int64) ([]string, []map[string]any, string) {
	if len(p.Targets) == 0 || strings.TrimSpace(p.Targets[0].Expr) == "" {
		return nil, nil, "面板无查询表达式"
	}
	expr := strings.TrimSpace(p.Targets[0].Expr)
	if ds, ok := h.s.cfg.GetDataSource(dsID); ok && isSQLDataSourceType(ds.Type) && ds.Enabled {
		c, err := h.s.resolveSQLConnFromDataSource(ds)
		if err != nil {
			return nil, nil, err.Error()
		}
		_, sqlStep, sqlRange := instantQueryWindow(from, to)
		sqlText := substituteVars(expr, nil, sqlStep, sqlRange)
		var cols []string
		var rows []map[string]any
		var qerr error
		if driverOf(c) == "postgres" {
			cols, rows, qerr = pgQueryReadOnly(c, sqlText, 100)
		} else {
			cols, rows, qerr = mysqlQueryReadOnly(c, sqlText, 100)
		}
		if qerr != nil {
			return nil, nil, qerr.Error()
		}
		if len(rows) > 50 {
			rows = rows[:50]
		}
		return cols, rows, ""
	}
	if !h.s.dashBackendReady(dsID) {
		return nil, nil, "数据源不可用"
	}
	evalAt, stepSec, rangeSecVar := instantQueryWindow(from, to)
	vec, ok := h.s.dashVectorAt(dsID, substituteVars(expr, nil, stepSec, rangeSecVar), evalAt)
	if !ok {
		return nil, nil, "查询失败"
	}
	cols := []string{"metric", "value"}
	rows := make([]map[string]any, 0, len(vec))
	for i, s := range vec {
		if i >= 50 {
			break
		}
		name := chartLegendFromLabels(s.Labels)
		if name == "" {
			name = fmt.Sprintf("series-%d", i+1)
		}
		rows = append(rows, map[string]any{"metric": name, "value": round3(s.Value)})
	}
	return cols, rows, ""
}

func (h *SreyunCore) queryPanelLogs(dsID string, p DashPanel, from, to int64) ([]map[string]any, string) {
	if len(p.Targets) == 0 || strings.TrimSpace(p.Targets[0].Expr) == "" {
		return nil, "面板无查询表达式"
	}
	ds, ok := h.s.cfg.ResolveDataSource(dsID)
	if !ok || ds.Type != "loki" || !ds.Enabled {
		return nil, "需要已启用的 Loki 数据源"
	}
	logql := dashLogQL(p.Targets[0].Expr, nil, from, to)
	lines, qok := dsLokiRange(ds, logql, unixSecToNs(from), unixSecToNs(to), 80)
	if !qok {
		return nil, "日志查询失败"
	}
	out := make([]map[string]any, 0, len(lines))
	for i, ln := range lines {
		if i >= 50 {
			break
		}
		ts := ""
		if ln.TsMs > 0 {
			ts = time.UnixMilli(ln.TsMs).Format("15:04:05")
		}
		out = append(out, map[string]any{"ts": ts, "line": trimLine(ln.Line, 400)})
	}
	return out, ""
}
