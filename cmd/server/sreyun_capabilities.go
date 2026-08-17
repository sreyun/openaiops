package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// registerCapabilityTools wires dashboard / assist / diagnosis / duty capabilities into
// the Hermes tool registry so the global AI chat can dispatch them as first-class actions.
func (h *SreyunCore) registerCapabilityTools() {
	h.tools["list_dashboards"] = SreyunTool{
		Name:        "list_dashboards",
		Description: "列出平台已有监控看板（id / 名称 / 面板数 / 数据源）。制作或优化看板前先用它确认现状。",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Execute:     h.execListDashboards,
	}
	h.tools["create_dashboard"] = SreyunTool{
		Name: "create_dashboard",
		Description: "根据自然语言需求生成并保存一张监控看板。" +
			"用户说「做一张 CPU/内存看板」「按主机负载做仪表盘」时必须调用。" +
			"返回 dashboard_id，前端会提供「打开看板」按钮。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]string{"type": "string", "description": "看板需求描述，越具体越好"},
				"name":   map[string]string{"type": "string", "description": "可选看板名称"},
			},
			"required": []string{"prompt"},
		},
		Execute: h.execCreateDashboard,
	}
	h.tools["get_dashboard"] = SreyunTool{
		Name:        "get_dashboard",
		Description: "读取指定看板的结构摘要（面板类型、查询、数据源），用于分析或优化前了解现状。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dashboard_id": map[string]string{"type": "string", "description": "看板 ID"},
			},
			"required": []string{"dashboard_id"},
		},
		Execute: h.execGetDashboard,
	}
	h.tools["analyze_dashboard"] = SreyunTool{
		Name:        "analyze_dashboard",
		Description: "对指定看板做 AI 诊断：布局、查询、阈值、可用性等，并给出改进建议。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dashboard_id": map[string]string{"type": "string", "description": "看板 ID"},
				"focus":        map[string]string{"type": "string", "description": "可选关注点，如「查询性能」「布局」「告警阈值」"},
			},
			"required": []string{"dashboard_id"},
		},
		Execute: h.execAnalyzeDashboard,
	}
	h.tools["optimize_dashboard"] = SreyunTool{
		Name: "optimize_dashboard",
		Description: "对指定看板做 AI 优化，产出可应用的看板 JSON 草案（不会自动写入）。" +
			"用户确认后可用 apply_dashboard_optimize 写入（需写审批）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dashboard_id": map[string]string{"type": "string", "description": "看板 ID"},
				"goal":         map[string]string{"type": "string", "description": "优化目标，如「精简面板」「修正 PromQL」「增强可读性」"},
			},
			"required": []string{"dashboard_id"},
		},
		Execute: h.execOptimizeDashboard,
	}
	h.tools["apply_dashboard_optimize"] = SreyunTool{
		Name: "apply_dashboard_optimize",
		Description: "将优化产出的看板 JSON 应用到现有看板（写操作）。" +
			"必须先拿到写工具审批 approval_id；json 须含完整看板结构。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dashboard_id": map[string]string{"type": "string", "description": "目标看板 ID"},
				"json":         map[string]string{"type": "string", "description": "AI 优化产出的看板 JSON（可含 ```json 代码块）"},
				"approval_id":  map[string]string{"type": "string", "description": "写工具审批 ID"},
				"preview_only": map[string]any{"type": "boolean", "description": "仅预览不写入，默认 false"},
			},
			"required": []string{"dashboard_id", "json"},
		},
		Execute: h.execApplyDashboardOptimize,
	}
	h.tools["run_assist_task"] = SreyunTool{
		Name: "run_assist_task",
		Description: "调度平台内置 AI 任务引擎：安全诊断/加固、硬件诊断、SQL 审计、剧本生成、LogQL/PromQL、值班报告等。" +
			"task 示例：host_security_diagnosis、host_security_remediation、web_vuln_diagnosis、hardware_diagnosis、" +
			"content_audit_diagnosis、snmp_diagnosis、netflow_diagnosis、dashboard_analysis、promql、logql、playbook、duty_report。" +
			"context 放原始数据摘要（扫描结果、指标片段、SQL 文本等）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task":    map[string]string{"type": "string", "description": "任务名，小写字母/数字/下划线"},
				"input":   map[string]string{"type": "string", "description": "用户需求或问题"},
				"context": map[string]string{"type": "string", "description": "相关上下文/数据摘要"},
			},
			"required": []string{"task", "input"},
		},
		Execute: h.execRunAssistTask,
	}
	h.tools["diagnose_incident"] = SreyunTool{
		Name:        "diagnose_incident",
		Description: "对指定事件/故障单做 AI 根因诊断（含现场证据与历史案例）。给出根因、置信度、证据与处置建议。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"incident_id": map[string]any{"type": "integer", "description": "事件 ID"},
			},
			"required": []string{"incident_id"},
		},
		Execute: h.execDiagnoseIncident,
	}
	h.tools["get_duty_context"] = SreyunTool{
		Name:        "get_duty_context",
		Description: "获取当前值班/运维态势摘要（主机、告警、事件等），适合做巡检开场或值班报告。",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Execute:     h.execGetDutyContext,
	}
}

// capabilityResult is the JSON envelope returned by capability tools.
// `_ui_actions` is consumed by the chat UI to render action cards (open dashboard / export / …).
type capabilityResult struct {
	OK         bool             `json:"ok"`
	Summary    string           `json:"summary,omitempty"`
	Error      string           `json:"error,omitempty"`
	Data       any              `json:"data,omitempty"`
	Answer     string           `json:"answer,omitempty"`
	UIActions  []map[string]any `json:"_ui_actions,omitempty"`
	ExportHint string           `json:"export_hint,omitempty"`
}

func capabilityJSON(v capabilityResult) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"ok":false,"error":"encode failed"}`
	}
	return string(b)
}

func openDashboardAction(id, name string) map[string]any {
	label := "打开看板"
	if strings.TrimSpace(name) != "" {
		label = "打开看板 · " + name
	}
	return map[string]any{"type": "open_dashboard", "id": id, "label": label, "name": name}
}

func exportReportAction(title, body string) map[string]any {
	if strings.TrimSpace(title) == "" {
		title = "AI 分析报告"
	}
	return map[string]any{
		"type":  "export_report",
		"label": "导出报告",
		"title": title,
		"body":  body,
	}
}

func showChartAction(id, label, title string, chart map[string]any, source map[string]any) map[string]any {
	if strings.TrimSpace(label) == "" {
		label = "查看图表"
	}
	act := map[string]any{
		"type":  "show_chart",
		"id":    id,
		"label": label,
		"title": title,
		"chart": chart,
	}
	if source != nil {
		act["source"] = source
	}
	return act
}

func showStatAction(id, label, title string, value float64, unit string, sparkline [][2]float64, thresholds map[string]float64) map[string]any {
	if strings.TrimSpace(label) == "" {
		label = "查看指标"
	}
	act := map[string]any{
		"type":  "show_stat",
		"id":    id,
		"label": label,
		"title": title,
		"value": value,
		"unit":  unit,
	}
	if len(sparkline) > 0 {
		pts := make([][]float64, 0, len(sparkline))
		for _, p := range sparkline {
			pts = append(pts, []float64{p[0], p[1]})
		}
		act["sparkline"] = pts
	}
	if thresholds != nil {
		act["thresholds"] = thresholds
	}
	return act
}

func drillDownAction(label, target string, extra map[string]any) map[string]any {
	if strings.TrimSpace(label) == "" {
		label = "下钻查看"
	}
	act := map[string]any{"type": "drill_down", "label": label, "target": target}
	for k, v := range extra {
		act[k] = v
	}
	return act
}

func navigateViewAction(view, label, title string) map[string]any {
	if strings.TrimSpace(label) == "" {
		label = "打开界面"
	}
	act := map[string]any{"type": "navigate_view", "view": view, "label": label}
	if strings.TrimSpace(title) != "" {
		act["title"] = title
	}
	return act
}

func showTableAction(id, label, title string, columns []string, rows []map[string]any) map[string]any {
	if strings.TrimSpace(label) == "" {
		label = "查看表格"
	}
	return map[string]any{
		"type":    "show_table",
		"id":      id,
		"label":   label,
		"title":   title,
		"columns": columns,
		"rows":    rows,
	}
}

func showLogsAction(id, label, title string, lines []map[string]any) map[string]any {
	if strings.TrimSpace(label) == "" {
		label = "查看日志"
	}
	return map[string]any{
		"type":  "show_logs",
		"id":    id,
		"label": label,
		"title": title,
		"lines": lines,
	}
}

func (h *SreyunCore) execListDashboards(args map[string]any) (string, error) {
	if h.s == nil || h.s.cfg == nil {
		return capabilityJSON(capabilityResult{OK: false, Error: "配置不可用"}), nil
	}
	list := h.s.cfg.Dashboards()
	type row struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Panels  int    `json:"panels"`
		Source  string `json:"data_source,omitempty"`
		Updated int64  `json:"updated_at,omitempty"`
		Link    string `json:"link,omitempty"`
	}
	out := make([]row, 0, len(list))
	actions := make([]map[string]any, 0, 12)
	for _, d := range list {
		link := "aiops://dashboard/" + d.ID
		out = append(out, row{ID: d.ID, Name: d.Name, Panels: len(d.Panels), Source: d.DataSource, Updated: d.UpdatedAt, Link: link})
		if len(actions) < 12 {
			actions = append(actions, openDashboardAction(d.ID, d.Name))
		}
	}
	sum := fmt.Sprintf("共 %d 张看板", len(out))
	if len(out) > 0 {
		sum += "。回复中可用 Markdown 链接 [看板名](aiops://dashboard/{id}) 方便用户一键打开。"
	}
	return capabilityJSON(capabilityResult{OK: true, Summary: sum, Data: out, UIActions: actions}), nil
}

func (h *SreyunCore) execCreateDashboard(args map[string]any) (string, error) {
	prompt, _ := args["prompt"].(string)
	name, _ := args["name"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return capabilityJSON(capabilityResult{OK: false, Error: "prompt 必填"}), nil
	}
	if len(prompt) > 32<<10 {
		return capabilityJSON(capabilityResult{OK: false, Error: "需求描述过长"}), nil
	}
	cfg := h.s.cfg.AIConfig()
	if !cfg.Enabled || cfg.Endpoint == "" || cfg.Model == "" {
		return capabilityJSON(capabilityResult{OK: false, Error: "AI 未配置或未启用"}), nil
	}
	d, warns, err := h.s.generateDashboardViaAI(prompt, "", "ai-chat", strings.TrimSpace(name))
	if err != nil {
		return capabilityJSON(capabilityResult{OK: false, Error: err.Error()}), nil
	}
	sum := fmt.Sprintf("已生成看板「%s」（%d 个面板）", d.Name, len(d.Panels))
	if len(warns) > 0 {
		sum += fmt.Sprintf("，%d 处提示", len(warns))
	}
	h.s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: "ai-chat", Message: "AI 对话生成看板：" + d.Name})
	return capabilityJSON(capabilityResult{
		OK:      true,
		Summary: sum,
		Data: map[string]any{
			"dashboard_id": d.ID,
			"name":         d.Name,
			"panels":       len(d.Panels),
			"warnings":     warns,
		},
		UIActions: []map[string]any{openDashboardAction(d.ID, d.Name)},
	}), nil
}

func (h *SreyunCore) execGetDashboard(args map[string]any) (string, error) {
	id, _ := args["dashboard_id"].(string)
	id = strings.TrimSpace(id)
	d, ok := h.s.cfg.DashboardByID(id)
	if !ok {
		return capabilityJSON(capabilityResult{OK: false, Error: "看板不存在"}), nil
	}
	type panel struct {
		ID    int    `json:"panel_id"`
		Title string `json:"title"`
		Type  string `json:"type"`
		Query string `json:"query,omitempty"`
		DS    string `json:"data_source,omitempty"`
	}
	panels := make([]panel, 0, len(d.Panels))
	for _, p := range d.Panels {
		q := ""
		if len(p.Targets) > 0 {
			q = p.Targets[0].Expr
		}
		ds := p.DataSource
		if ds == "" {
			ds = d.DataSource
		}
		panels = append(panels, panel{ID: p.ID, Title: p.Title, Type: p.Type, Query: q, DS: ds})
	}
	return capabilityJSON(capabilityResult{
		OK:      true,
		Summary: fmt.Sprintf("看板「%s」共 %d 面板", d.Name, len(panels)),
		Data: map[string]any{
			"id": d.ID, "name": d.Name, "data_source": d.DataSource,
			"description": d.Description, "panels": panels,
		},
		UIActions: []map[string]any{openDashboardAction(d.ID, d.Name)},
	}), nil
}

func (h *SreyunCore) dashboardContextText(id string) (Dashboard, string, error) {
	d, ok := h.s.cfg.DashboardByID(id)
	if !ok {
		return Dashboard{}, "", fmt.Errorf("看板不存在")
	}
	b, _ := json.MarshalIndent(d, "", "  ")
	if len(b) > 80<<10 {
		b = b[:80<<10]
	}
	ctx := "看板 JSON：\n" + string(b)
	if h.s != nil {
		if digest := strings.TrimSpace(h.s.buildDashboardDigest(d)); digest != "" {
			ctx += "\n\n当前数据（VictoriaMetrics / Loki / 配置数据源，与实时面板同一套查询）：\n" + digest
		}
	}
	return d, ctx, nil
}

func (h *SreyunCore) execAnalyzeDashboard(args map[string]any) (string, error) {
	id, _ := args["dashboard_id"].(string)
	focus, _ := args["focus"].(string)
	d, ctxText, err := h.dashboardContextText(strings.TrimSpace(id))
	if err != nil {
		return capabilityJSON(capabilityResult{OK: false, Error: err.Error()}), nil
	}
	input := "请分析该监控看板并给出改进建议。"
	if strings.TrimSpace(focus) != "" {
		input += " 关注点：" + strings.TrimSpace(focus)
	}
	answer, err := h.s.runAssistTaskSync(context.Background(), "dashboard_analysis", input, ctxText)
	if err != nil {
		return capabilityJSON(capabilityResult{OK: false, Error: err.Error()}), nil
	}
	return capabilityJSON(capabilityResult{
		OK: true, Summary: "看板分析完成", Answer: answer,
		UIActions: []map[string]any{
			openDashboardAction(d.ID, d.Name),
			exportReportAction("看板分析 · "+d.Name, answer),
		},
	}), nil
}

func (h *SreyunCore) execOptimizeDashboard(args map[string]any) (string, error) {
	id, _ := args["dashboard_id"].(string)
	goal, _ := args["goal"].(string)
	d, ctxText, err := h.dashboardContextText(strings.TrimSpace(id))
	if err != nil {
		return capabilityJSON(capabilityResult{OK: false, Error: err.Error()}), nil
	}
	input := "请优化该监控看板，输出完整可应用的看板 JSON（```json 代码块）。"
	if strings.TrimSpace(goal) != "" {
		input += " 优化目标：" + strings.TrimSpace(goal)
	}
	answer, err := h.s.runAssistTaskSync(context.Background(), "dashboard_optimize", input, ctxText)
	if err != nil {
		return capabilityJSON(capabilityResult{OK: false, Error: err.Error()}), nil
	}
	return capabilityJSON(capabilityResult{
		OK: true, Summary: "看板优化草案已生成（尚未写入）。确认后调用 apply_dashboard_optimize。",
		Answer: answer,
		Data:   map[string]any{"dashboard_id": d.ID, "name": d.Name},
		UIActions: []map[string]any{
			openDashboardAction(d.ID, d.Name),
			exportReportAction("看板优化 · "+d.Name, answer),
		},
	}), nil
}

func (h *SreyunCore) execApplyDashboardOptimize(args map[string]any) (string, error) {
	id, _ := args["dashboard_id"].(string)
	raw, _ := args["json"].(string)
	approval, _ := args["approval_id"].(string)
	preview, _ := args["preview_only"].(bool)
	id = strings.TrimSpace(id)
	cur, ok := h.s.cfg.DashboardByID(id)
	if !ok {
		return capabilityJSON(capabilityResult{OK: false, Error: "看板不存在"}), nil
	}
	if !preview {
		if msg, blocked := h.sreyunWriteBlocked("apply_dashboard_optimize", id+"|"+approval, args); blocked {
			return capabilityJSON(capabilityResult{OK: false, Error: msg}), nil
		}
	}
	spec, ok := decodeAIDashSpec(raw)
	if !ok {
		return capabilityJSON(capabilityResult{OK: false, Error: "未找到可解析的看板 JSON"}), nil
	}
	d, warns := sanitizeAIDash(spec, cur.Name, cur.Source)
	h.s.resolveAIDashPanelSources(&d, &warns)
	if len(d.Panels) == 0 {
		return capabilityJSON(capabilityResult{OK: false, Error: "AI 未给出有效面板"}), nil
	}
	d.ID = cur.ID
	d.DataSource = cur.DataSource
	d.Description = cur.Description
	d.Tags = cur.Tags
	d.Appearance = cur.Appearance
	if spec.specName() == "" {
		d.Name = cur.Name
	}
	if preview {
		return capabilityJSON(capabilityResult{
			OK: true, Summary: fmt.Sprintf("预览通过：将写入 %d 个面板", len(d.Panels)),
			Data:      map[string]any{"panels": len(d.Panels), "warnings": warns, "preview_only": true},
			UIActions: []map[string]any{openDashboardAction(cur.ID, cur.Name)},
		}), nil
	}
	if _, err := h.s.cfg.UpsertDashboard(d); err != nil {
		return capabilityJSON(capabilityResult{OK: false, Error: err.Error()}), nil
	}
	h.s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: "ai-chat", Message: "AI 对话应用看板优化：" + d.Name})
	return capabilityJSON(capabilityResult{
		OK: true, Summary: fmt.Sprintf("已将优化应用到看板「%s」（%d 面板）", d.Name, len(d.Panels)),
		Data:      map[string]any{"dashboard_id": d.ID, "name": d.Name, "panels": len(d.Panels), "warnings": warns},
		UIActions: []map[string]any{openDashboardAction(d.ID, d.Name)},
	}), nil
}

func (h *SreyunCore) execRunAssistTask(args map[string]any) (string, error) {
	task, _ := args["task"].(string)
	input, _ := args["input"].(string)
	ctxText, _ := args["context"].(string)
	task = strings.TrimSpace(task)
	if !validAssistTaskName(task) {
		return capabilityJSON(capabilityResult{OK: false, Error: "无效的 task 名称"}), nil
	}
	if strings.TrimSpace(input) == "" && strings.TrimSpace(ctxText) == "" {
		return capabilityJSON(capabilityResult{OK: false, Error: "input 或 context 至少提供一项"}), nil
	}
	if strings.TrimSpace(input) == "" {
		input = "请根据上下文进行分析并给出结论。"
	}
	answer, err := h.s.runAssistTaskSync(context.Background(), task, input, ctxText)
	if err != nil {
		return capabilityJSON(capabilityResult{OK: false, Error: err.Error()}), nil
	}
	title := "AI · " + task
	return capabilityJSON(capabilityResult{
		OK: true, Summary: "任务 " + task + " 已完成", Answer: answer,
		UIActions: []map[string]any{exportReportAction(title, answer)},
	}), nil
}

func (h *SreyunCore) execDiagnoseIncident(args map[string]any) (string, error) {
	var id int64
	switch v := args["incident_id"].(type) {
	case float64:
		id = int64(v)
	case int64:
		id = v
	case int:
		id = int64(v)
	case json.Number:
		n, _ := v.Int64()
		id = n
	case string:
		fmt.Sscanf(strings.TrimSpace(v), "%d", &id)
	}
	if id <= 0 {
		return capabilityJSON(capabilityResult{OK: false, Error: "incident_id 无效"}), nil
	}
	inc, found := h.s.incidents.Get(id)
	if !found {
		return capabilityJSON(capabilityResult{OK: false, Error: "事件不存在"}), nil
	}
	if actor := scopeActorFromArgs(args); actor != "" && inc.HostID != "" && !h.actorCanAccessHost(actor, inc.HostID) {
		return capabilityJSON(capabilityResult{OK: false, Error: "无权访问该事件所属主机"}), nil
	}
	cfg := h.s.cfg.AIConfig()
	if !cfg.Enabled || cfg.Endpoint == "" || cfg.Model == "" {
		return capabilityJSON(capabilityResult{OK: false, Error: "AI 未配置或未启用"}), nil
	}
	sys := h.s.buildIncidentDiagnosisPrompt(inc)
	liveExtra, _ := h.s.gatherLiveDiagnoseEvidence(inc)
	sys += liveExtra
	ragQuery := strings.TrimSpace(inc.Title + " " + inc.Type + " " + inc.Hostname)
	memText, _, _, _ := h.s.retrieveMemoryWithCitations("diagnosis", ragQuery, 6)
	skillText, _, _, _ := h.s.retrieveSkillsDetailed(ragQuery, 3)
	sys += diagnosisOrchestrationHint() + memText + skillText
	userMsg := fmt.Sprintf(`请对事件 #%d 进行诊断分析，严格按以下结构输出：

## 根因研判
## 置信度
## 关键证据
## 处置建议`, inc.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	reply, _, _, err := aiChatVWithFallback(ctx, cfg, []map[string]string{
		{"role": "system", "content": sys},
		{"role": "user", "content": userMsg},
	}, nil, nil, nil)
	if err != nil {
		return capabilityJSON(capabilityResult{OK: false, Error: err.Error()}), nil
	}
	title := fmt.Sprintf("事件 #%d 诊断", inc.ID)
	return capabilityJSON(capabilityResult{
		OK: true, Summary: title + "完成", Answer: reply,
		Data:      map[string]any{"incident_id": inc.ID, "title": inc.Title},
		UIActions: []map[string]any{exportReportAction(title, reply)},
	}), nil
}

func (h *SreyunCore) execGetDutyContext(args map[string]any) (string, error) {
	// Reuse the same payload the Copilot/duty UI consumes — scoped to actor.
	hosts := h.s.store.ListHosts()
	actor := scopeActorFromArgs(args)
	if actor != "" {
		if u, ok := h.s.cfg.UserByName(actor); ok && u.hostScopeRestricted() && roleRank(u.Role) < roleRank(RoleAdmin) {
			filtered := make([]*Host, 0, len(hosts))
			for _, ht := range hosts {
				if ht != nil && h.s.userCanAccessHost(u, ht.ID) {
					filtered = append(filtered, ht)
				}
			}
			hosts = filtered
		}
	}
	now := time.Now().Unix()
	online := 0
	for _, ht := range hosts {
		if now-ht.LastSeen <= 60 {
			online++
		}
	}
	alertN := 0
	if h.s.notifier != nil {
		for _, a := range h.s.notifier.ActiveAlerts() {
			if a.HostID == "" || h.actorCanAccessHost(actor, a.HostID) {
				alertN++
			}
		}
	}
	if h.s.checks != nil {
		for _, a := range h.s.checks.DownAlerts() {
			if a.HostID == "" || h.actorCanAccessHost(actor, a.HostID) {
				alertN++
			}
		}
	}
	incOpen := 0
	if h.s.incidents != nil {
		for _, inc := range h.s.incidents.List() {
			if inc.Status != "open" && inc.Status != "investigating" {
				continue
			}
			if inc.HostID != "" && !h.actorCanAccessHost(actor, inc.HostID) {
				continue
			}
			incOpen++
		}
	}
	sum := fmt.Sprintf("主机 %d（在线 %d）· 活跃告警 %d · 未关闭事件 %d", len(hosts), online, alertN, incOpen)
	return capabilityJSON(capabilityResult{
		OK: true, Summary: sum,
		Data: map[string]any{
			"hosts_total": len(hosts), "hosts_online": online,
			"alerts_active": alertN, "incidents_open": incOpen,
			"hint": "列出全部主机请调用 list_hosts；单机容器明细用 query_containers(host_id=...)，勿对 query_containers 做无 host_id 的全量明细拉取。",
		},
	}), nil
}

// runAssistTaskSync runs an assist-class task without SSE — used by Hermes tools.
// Aligned with streamOrchestratedAssist: routing, A/B, cost recording, action sanitize.
func (s *Server) runAssistTaskSync(ctx context.Context, task, userMsg, contextText string) (string, error) {
	cfg := s.cfg.AIConfig()
	if !cfg.Enabled || cfg.Endpoint == "" || cfg.Model == "" {
		return "", fmt.Errorf("AI 未配置或未启用")
	}
	policy := assistTaskPolicy(task)
	cfg = applyRoutedModel(cfg, task)
	actor := "ai"
	expID, variant := s.pickAssistExperiment(cfg, task, actor)
	cfg = s.applyExperimentVariantOn(cfg, expID, variant)
	safeCtx := sanitizeAssistContext(contextText)
	// 与 streamOrchestratedAssist 共用同一条装配流水线（ai_prompt_shared.go）——
	// 此前是两份手抄，靠注释里一句「Aligned with…」维持一致。
	// 外部 MCP 不注入：这里本身就跑在 Hermes 的工具循环里，再套一层外部预取会让
	// 一次工具调用的延迟不可控。
	parts := s.buildAssistPrompt(cfg, assistPromptReq{
		Task: task, Actor: actor, RAGQuery: strings.TrimSpace(userMsg + " " + contextText),
		ExperimentSuffix: experimentPromptSuffix(s, expID, variant),
	})
	sys := parts.System
	memHits, skillHits := parts.MemHits, parts.SkillHits
	if strings.TrimSpace(userMsg) == "" {
		userMsg = "请根据上述上下文进行分析并给出结论。"
	}
	userPayload := userMsg
	if safeCtx != "" {
		userPayload = safeCtx + "\n\n【用户请求】\n" + userMsg
	}
	msgs := []map[string]string{
		{"role": "system", "content": sys},
		{"role": "user", "content": userPayload},
	}
	opts := aiCallOpts{
		DisableThinking: policy.DisableThink,
		EnableThinking:  policy.EnableThink,
		ThinkingBudget:  policy.ThinkingBudget,
		MaxTokens:       policy.MaxTokens,
		Timeout:         policy.Timeout,
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 90 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	start := time.Now()
	usedModel := cfg.Model
	reply, _, err := aiChatVOpts(callCtx, cfg, msgs, nil, nil, opts)
	if err != nil && thinkingParamForcedTrueError(err) && !opts.EnableThinking {
		retry := opts
		retry.EnableThinking = true
		retry.DisableThinking = false
		if retry.ThinkingBudget <= 0 {
			retry.ThinkingBudget = 512
		}
		reply, _, err = aiChatVOpts(callCtx, cfg, msgs, nil, nil, retry)
	}
	if err != nil {
		for _, model := range fallbackModelList(cfg) {
			retryCfg := cfg
			retryCfg.Model = model
			reply, _, err = aiChatVOpts(callCtx, retryCfg, msgs, nil, nil, opts)
			if err == nil {
				usedModel = model
				break
			}
		}
	}
	latency := time.Since(start).Milliseconds()
	errStr := ""
	if err != nil {
		errStr = err.Error()
		s.recordAICallActor(callCtx, task, usedModel, actor, latency, false, errStr, memHits, skillHits, "")
		return "", err
	}
	reply = strings.TrimSpace(reply)
	reply, _ = sanitizeAssistActionReply(task, reply)
	s.recordAICallActor(callCtx, task, usedModel, actor, latency, true, "", memHits, skillHits, reply)
	_ = expID
	_ = variant
	return reply, nil
}

// extractUIActionsFromToolResult pulls `_ui_actions` out of a capability tool JSON result.
func extractUIActionsFromToolResult(result string) []map[string]any {
	result = strings.TrimSpace(result)
	if result == "" || result[0] != '{' {
		return nil
	}
	var envelope struct {
		UIActions []map[string]any `json:"_ui_actions"`
	}
	if err := json.Unmarshal([]byte(result), &envelope); err != nil {
		return nil
	}
	return filterUIActions(envelope.UIActions)
}
