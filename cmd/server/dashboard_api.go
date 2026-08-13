package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---- 仪表盘 HTTP 端点 ----

func (s *Server) handleListDashboards(w http.ResponseWriter, r *http.Request) {
	// 列表只回元信息（不含面板体），减小载荷。
	type meta struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description,omitempty"`
		Tags        []string `json:"tags,omitempty"`
		Panels      int      `json:"panels"`
		Source      string   `json:"source,omitempty"`
		LogoURL     string   `json:"logo_url,omitempty"`
		UpdatedAt   int64    `json:"updated_at"`
	}
	var out []meta
	for _, d := range s.cfg.Dashboards() {
		out = append(out, meta{d.ID, d.Name, d.Description, d.Tags, len(d.Panels), d.Source, d.Appearance.LogoURL, d.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"dashboards": out})
}

func (s *Server) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	d, ok := s.cfg.DashboardByID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "仪表盘不存在"})
		return
	}
	// 惰性修复历史导入（=~ / 布局）：仅内存 heal 后返回，不在 GET 时落盘抬升 revision，
	// 避免并发编辑/AI 应用被无谓 409；用户下次显式保存时会持久化已修复内容。
	_ = healImportedDashboard(&d)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleUpsertDashboard(w http.ResponseWriter, r *http.Request) {
	var d Dashboard
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	expected := int64(-1)
	if d.ID != "" {
		// 客户端带了 revision（含 0）则启用写锁内乐观锁；未带字段时 json 零值也为 0，
		// 仅当显式更新已有看板时校验——用「请求体是否含 revision」无法区分，故：
		// 有 ID 且客户端传了非负 revision 字段时一律校验（前端保存总会带上）。
		expected = d.Revision
	}
	saved, err := s.cfg.UpsertDashboardIfRevision(d, expected)
	if err != nil {
		if errDashboardRevisionConflict(err) {
			cur, _ := s.cfg.DashboardByID(d.ID)
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":            "该仪表盘已被其他操作更新，请刷新后合并修改",
				"current_revision": cur.Revision,
				"updated_at":       cur.UpdatedAt,
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Message: "保存仪表盘：" + saved.Name})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": saved.ID, "revision": saved.Revision, "updated_at": saved.UpdatedAt,
	})
}

func (s *Server) handleDeleteDashboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.cfg.DeleteDashboard(id)
	s.removeDashboardAssets(id)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Message: "删除仪表盘：" + id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// panelQueryReq 是面板查询请求：表达式 + 时间范围 + 已选变量值。
type panelQueryReq struct {
	Expr       string            `json:"expr"`
	From       int64             `json:"from"`
	To         int64             `json:"to"`
	Step       int64             `json:"step"`
	Vars       map[string]string `json:"vars"`
	DataSource string            `json:"datasource"` // 数据源 id（""=内置 VM）
	Limit      int               `json:"limit"`      // 日志面板取行上限
}

// instantQueryWindow derives the evaluation time, $__interval and $__range for
// an instant panel from the dashboard's selected [from,to].
//
// evalAt is 0 ("now") whenever the window ends at or after the present, so live
// dashboards keep reading the freshest sample; it is pinned to `to` only for a
// window that genuinely ends in the past. rangeSec falls back to one hour when
// the client sends no window at all, preserving the old behaviour for callers
// that never learned to pass one.
func instantQueryWindow(from, to int64) (evalAt, stepSec, rangeSec int64) {
	const (
		defaultRange = int64(3600)
		defaultStep  = int64(60)
		// 窗口右端落在这个容差内一律按「现在」处理：前端会把 to 对齐到 step 网格，
		// 硬比较会把实时看板误判成历史窗口。
		nowSlack = int64(120)
	)
	if to <= 0 || from <= 0 || to <= from {
		return 0, defaultStep, defaultRange
	}
	rangeSec = to - from
	stepSec = rangeSec / 480
	switch {
	case stepSec < 5:
		stepSec = 5
	case stepSec > 300:
		stepSec = 300
	}
	if to < time.Now().Unix()-nowSlack {
		evalAt = to
	}
	return evalAt, stepSec, rangeSec
}

func validatePanelQueryReq(req *panelQueryReq, withRange, logs bool) error {
	if req == nil {
		return fmt.Errorf("查询请求不能为空")
	}
	req.Expr = strings.TrimSpace(req.Expr)
	if req.Expr == "" {
		return fmt.Errorf("查询表达式不能为空")
	}
	if len(req.Expr) > maxDashboardExpr {
		return fmt.Errorf("查询表达式不能超过 16 KiB")
	}
	if len(req.DataSource) > 128 {
		return fmt.Errorf("数据源 ID 过长")
	}
	if len(req.Vars) > maxDashboardVars {
		return fmt.Errorf("模板变量不能超过 %d 个", maxDashboardVars)
	}
	for k, v := range req.Vars {
		if !dashVarNameValid.MatchString(k) || len(v) > 4096 {
			return fmt.Errorf("模板变量 %q 无效或值过长", k)
		}
	}
	if !withRange {
		return nil
	}
	now := time.Now().Unix()
	if req.To <= 0 {
		req.To = now
	}
	if req.From <= 0 {
		req.From = req.To - 3600
	}
	if req.To <= req.From {
		return fmt.Errorf("查询结束时间必须晚于开始时间")
	}
	maxRange := int64(90 * 24 * 3600)
	if logs {
		maxRange = 7 * 24 * 3600
	}
	if req.To-req.From > maxRange {
		return fmt.Errorf("查询时间范围过大，最大允许 %d 天", maxRange/(24*3600))
	}
	if req.To > now+300 {
		return fmt.Errorf("查询结束时间不能超过当前时间 5 分钟")
	}
	if req.Step < 0 {
		return fmt.Errorf("查询步长不能为负数")
	}
	if !logs && req.Step > 0 {
		const maxPoints = 5000
		if span := req.To - req.From; span > 0 && span/req.Step > maxPoints {
			req.Step = span / maxPoints
			if req.Step < 1 {
				req.Step = 1
			}
		}
	}
	if logs {
		if req.Limit <= 0 {
			req.Limit = 200
		}
		if req.Limit > 2000 {
			req.Limit = 2000
		}
	}
	return nil
}

// healPanelQueryExpr 对内置 VictoriaMetrics 上的 Grafana node_* 公式做运行时纠偏，
// 让存量 AI 看板在未重新保存时也能出图。外部数据源可能真有 node_exporter，不改写。
func healPanelQueryExpr(dsID, expr string) string {
	if strings.TrimSpace(dsID) != "" {
		return expr
	}
	if !dashExprHasNodeMetric(expr) {
		return expr
	}
	return healAIDashExpr(expr)
}

func (s *Server) handleDashboardQuery(w http.ResponseWriter, r *http.Request) {
	var req panelQueryReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := validatePanelQueryReq(&req, true, false); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !s.dashBackendReady(req.DataSource) {
		writeJSON(w, http.StatusOK, map[string]any{"series": []any{}, "available": false})
		return
	}
	rangeSec := req.To - req.From
	if req.Step <= 0 {
		req.Step = rangeSec / 300 // 约 300 个点
		if req.Step < 15 {
			req.Step = 15
		}
	}
	expr := substituteVars(healPanelQueryExpr(req.DataSource, req.Expr), req.Vars, req.Step, rangeSec)
	series, ok := s.dashRangeSeries(req.DataSource, expr, req.From, req.To, req.Step)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"series": []any{}, "available": true, "error": "查询失败（表达式或数据源）"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": series, "step": req.Step})
}

// handleDashboardQueryInstant 即时查询，供 stat/gauge/bargauge/table 取当前值。
func (s *Server) handleDashboardQueryInstant(w http.ResponseWriter, r *http.Request) {
	var req panelQueryReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	// withRange=true: instant panels now expand $__range from the picker, so the
	// same 90-day / future caps as /query must apply. Skipping them let a crafted
	// from/to (e.g. epoch→now) push multi-year avg_over_time windows into VM.
	if err := validatePanelQueryReq(&req, true, false); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !s.dashBackendReady(req.DataSource) {
		writeJSON(w, http.StatusOK, map[string]any{"series": []any{}, "available": false})
		return
	}
	// 仪表/饼图/柱状/直方图等「瞬时」面板同样归看板时间选择器管辖：
	//   - $__range / $__interval 必须按所选窗口展开，此前写死 3600s/60s，于是
	//     topk(5, avg_over_time(x[$__range])) 这类表达式无论选 1h 还是 14d 都只统计 1 小时；
	//   - 求值时刻取窗口右端而非 time.Now()，否则选了历史区间仍然显示当前值。
	// 两者叠加的结果就是「点了时间跨度没有任何反应」。
	evalAt, stepSec, rangeSec := instantQueryWindow(req.From, req.To)
	expr := substituteVars(healPanelQueryExpr(req.DataSource, req.Expr), req.Vars, stepSec, rangeSec)
	vec, ok := s.dashVectorAt(req.DataSource, expr, evalAt)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"series": []any{}, "available": true, "error": "查询失败（表达式或数据源）"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": vec})
}

// handleDashboardQuerySQL runs a read-only SQL query for table/stat panels backed by
// postgres/mysql datasources (or a linked SQL toolkit connection).
func (s *Server) handleDashboardQuerySQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Expr       string            `json:"expr"`
		SQL        string            `json:"sql"`
		DataSource string            `json:"datasource"`
		Limit      int               `json:"limit"`
		Vars       map[string]string `json:"vars"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	rawSQL := strings.TrimSpace(req.Expr)
	if rawSQL == "" {
		rawSQL = strings.TrimSpace(req.SQL)
	}
	if len(req.DataSource) > 128 || len(rawSQL) > maxDashboardExpr {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "查询请求过大或字段无效"})
		return
	}
	if len(req.Vars) > maxDashboardVars {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("模板变量不能超过 %d 个", maxDashboardVars)})
		return
	}
	for k, v := range req.Vars {
		if !dashVarNameValid.MatchString(k) || len(v) > 4096 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("模板变量 %q 无效或值过长", k)})
			return
		}
	}
	ds, ok := s.cfg.GetDataSource(req.DataSource)
	if !ok || !ds.Enabled || !isSQLDataSourceType(ds.Type) {
		writeJSON(w, http.StatusOK, map[string]any{"columns": []any{}, "rows": []any{}, "available": false})
		return
	}
	c, err := s.resolveSQLConnFromDataSource(ds)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"columns": []any{}, "rows": []any{}, "available": true, "error": err.Error()})
		return
	}
	sqlText := substituteVars(rawSQL, req.Vars, 60, 3600)
	if sqlText == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "SQL 表达式必填"})
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	var cols []string
	var rows []map[string]any
	if driverOf(c) == "postgres" {
		cols, rows, err = pgQueryReadOnly(c, sqlText, limit)
	} else {
		cols, rows, err = mysqlQueryReadOnly(c, sqlText, limit)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"columns": []any{}, "rows": []any{}, "available": true, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"columns": cols, "rows": rows, "available": true, "driver": driverOf(c),
	})
}

// handleDashboardQueryLogs 日志面板查询（Loki 数据源，LogQL 区间）。
func (s *Server) handleDashboardQueryLogs(w http.ResponseWriter, r *http.Request) {
	var req panelQueryReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := validatePanelQueryReq(&req, true, true); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ds, ok := s.cfg.GetDataSource(req.DataSource)
	if !ok || ds.Type != "loki" || !ds.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{"lines": []any{}, "available": false})
		return
	}
	now := time.Now()
	endNs := now.UnixNano()
	if req.To > 0 {
		endNs = req.To * 1e9
	}
	startNs := now.Add(-time.Hour).UnixNano()
	if req.From > 0 {
		startNs = req.From * 1e9
	}
	logql := substituteVars(req.Expr, req.Vars, 60, 3600)
	lines, qok := dsLokiRange(ds, logql, startNs, endNs, req.Limit)
	if !qok {
		writeJSON(w, http.StatusOK, map[string]any{"lines": []any{}, "available": true, "error": "日志查询失败（LogQL 或 Loki）"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// handleDashboardVarValues 解析一个模板变量的候选值（custom 直给 / query 走 label_values，按数据源）。
func (s *Server) handleDashboardVarValues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashVar
		DataSource string `json:"datasource"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name != "" && !dashVarNameValid.MatchString(req.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "模板变量名无效"})
		return
	}
	if len(req.DataSource) > 128 || len(req.Query) > maxDashboardExpr || len(req.Options) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "模板变量请求过大或字段无效"})
		return
	}
	lv := func(label, match string) ([]string, bool) { return s.dashLabelValues(req.DataSource, label, match) }
	writeJSON(w, http.StatusOK, map[string]any{"values": resolveVarValues(req.DashVar, lv)})
}

var grafanaIDRe = regexp.MustCompile(`^\d+$`)

// handleImportGrafana 导入看板模板：从 grafana.com 按 ID 拉取，或解析粘贴/上传的 JSON
// （自动识别 Grafana / 兼容看板等导出格式），映射后保存。
func (s *Server) handleImportGrafana(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GrafanaID string `json:"grafana_id"`
		JSON      string `json:"json"`
		Name      string `json:"name"`
		Format    string `json:"format"` // ""/auto | grafana | nightingale | aiops（留空则自动识别；nightingale 为内部兼容格式键）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	var raw []byte
	source := "import"
	format := strings.TrimSpace(req.Format)
	if strings.TrimSpace(req.JSON) != "" {
		raw = []byte(req.JSON)
		if format == "" || format == "auto" {
			format = detectTemplateFormat(raw)
		}
	} else {
		id := strings.TrimSpace(req.GrafanaID)
		if !grafanaIDRe.MatchString(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请填写 grafana.com 看板 ID（纯数字），或粘贴/上传 Grafana / 兼容看板 JSON"})
			return
		}
		// grafana.com 官方看板下载端点（公网，SSRF 守卫放行公网 IP）。
		url := "https://grafana.com/api/dashboards/" + id + "/revisions/latest/download"
		client := newGuardedHTTPClient(20 * time.Second)
		resp, err := client.Get(url)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "拉取 grafana.com 失败：" + err.Error()})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "grafana.com 返回 " + strconv.Itoa(resp.StatusCode) + "（检查看板 ID 是否存在）"})
			return
		}
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 上限 8MB
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "读取响应失败：" + err.Error()})
			return
		}
		source = "grafana:" + id
		format = "grafana"
	}

	var d Dashboard
	var err error
	switch format {
	case "nightingale":
		if source == "import" {
			source = "nightingale"
		}
		d, err = mapNightingaleDashboard(raw, req.Name, source)
	case "aiops":
		if source == "import" {
			source = "aiops-template"
		}
		d, err = mapAIOpsDashboard(raw, req.Name, source)
	default:
		d, err = mapGrafanaDashboard(raw, req.Name, source)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	saved, err := s.cfg.UpsertDashboard(d)
	if err != nil {
		// 把「面板超限」等校验错误以 400 返回，便于前端直接展示可读原因。
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	unsupported := 0
	for _, p := range saved.Panels {
		if p.Type == "unsupported" {
			unsupported++
		}
	}
	kind := "Grafana"
	if format == "nightingale" {
		kind = "兼容看板"
	} else if format == "aiops" {
		kind = "AIOps 模板"
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Message: "导入 " + kind + " 看板：" + saved.Name + "（" + strconv.Itoa(len(saved.Panels)) + " 面板）"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": saved.ID, "name": saved.Name, "panels": len(saved.Panels), "unsupported": unsupported, "format": format})
}
