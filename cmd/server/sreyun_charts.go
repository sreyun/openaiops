package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"aiops-monitor/shared"
)

// registerChartTools wires in-chat chart / stat / drill-down tools for Hermes.
func (h *SreyunCore) registerChartTools() {
	h.tools["render_chart"] = SreyunTool{
		Name: "render_chart",
		Description: "在 AI 对话中生成并展示趋势图表（前端内嵌 Canvas）。" +
			"用户要看 CPU/内存/磁盘/负载/网络 趋势、对比曲线时优先调用。" +
			"source=host 时用主机历史指标；source=promql 时用 PromQL 区间查询。" +
			"不要在回复里粘贴大段原始采样 JSON，图表由 UI 动作展示。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source":     map[string]string{"type": "string", "description": "host | promql，默认 host"},
				"host_id":    map[string]string{"type": "string", "description": "主机 ID / 主机名 / IP（source=host）"},
				"metrics":    map[string]string{"type": "string", "description": "逗号分隔：cpu,memory,disk,load,network,io；默认 cpu"},
				"expr":       map[string]string{"type": "string", "description": "PromQL（source=promql）"},
				"datasource": map[string]string{"type": "string", "description": "数据源 ID，留空=内置 VM"},
				"range":      map[string]string{"type": "string", "description": "时间范围，如 1h/6h/24h/7d，默认 6h"},
				"title":      map[string]string{"type": "string", "description": "图表标题"},
			},
		},
		Execute: h.execRenderChart,
	}
	h.tools["query_metric_range"] = SreyunTool{
		Name:        "query_metric_range",
		Description: "查询主机指定指标的时间序列摘要（点数/最小/最大/平均/最新），并在对话中渲染趋势图。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host_id": map[string]string{"type": "string", "description": "主机 ID / 主机名 / IP"},
				"metric":  map[string]string{"type": "string", "description": "cpu|memory|disk|load|network|io，默认 cpu"},
				"range":   map[string]string{"type": "string", "description": "1h/6h/24h/7d，默认 6h"},
			},
			"required": []string{"host_id"},
		},
		Execute: h.execQueryMetricRange,
	}
	h.tools["query_promql_range"] = SreyunTool{
		Name:        "query_promql_range",
		Description: "执行 PromQL 区间查询并在对话中渲染图表。适合自定义指标、多序列对比、跨主机聚合。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"expr":       map[string]string{"type": "string", "description": "PromQL 表达式"},
				"datasource": map[string]string{"type": "string", "description": "数据源 ID，留空=内置 VM"},
				"range":      map[string]string{"type": "string", "description": "1h/6h/24h/7d，默认 6h"},
				"title":      map[string]string{"type": "string", "description": "图表标题"},
			},
			"required": []string{"expr"},
		},
		Execute: h.execQueryPromqlRange,
	}
	h.tools["show_instant_stat"] = SreyunTool{
		Name:        "show_instant_stat",
		Description: "在对话中展示大数字指标卡（可选迷你趋势）。适合「当前 CPU 多少」「内存占用」等瞬时值展示。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host_id": map[string]string{"type": "string", "description": "主机 ID / 主机名 / IP"},
				"metric":  map[string]string{"type": "string", "description": "cpu|memory|disk|load，默认 cpu"},
				"range":   map[string]string{"type": "string", "description": "用于 sparkline 的范围，默认 1h"},
			},
			"required": []string{"host_id"},
		},
		Execute: h.execShowInstantStat,
	}
	h.tools["analyze_metric_trend"] = SreyunTool{
		Name: "analyze_metric_trend",
		Description: "分析主机近期指标趋势（早/晚窗口对比）并生成趋势图 + 下钻入口。" +
			"用户说「分析趋势」「有没有异常波动」「过去几小时资源变化」时优先调用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host_id": map[string]string{"type": "string", "description": "主机 ID / 主机名 / IP"},
				"hours":   map[string]any{"type": "number", "description": "回溯小时数，默认 6，最大 72"},
				"metrics": map[string]string{"type": "string", "description": "逗号分隔指标，默认 cpu,memory,disk,load"},
			},
			"required": []string{"host_id"},
		},
		Execute: h.execAnalyzeMetricTrend,
	}
	h.tools["forecast_metric"] = SreyunTool{
		Name: "forecast_metric",
		Description: "基于历史时序做多模型预测（Holt-Winters / 形态回放 / 季节 / 阻尼 Holt），返回预测曲线+置信带+MAPE/R²，并在对话中以虚线图表展示。" +
			"用户问「未来会不会超阈值」「预测明天 CPU」「还能撑多久」时必须调用本工具，勿臆测。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host_id":    map[string]string{"type": "string", "description": "主机 ID / 主机名 / IP（与 expr 二选一）"},
				"metric":     map[string]string{"type": "string", "description": "cpu|memory|disk|load，默认 cpu（host 模式）"},
				"expr":       map[string]string{"type": "string", "description": "PromQL（与 host_id 二选一）"},
				"datasource": map[string]string{"type": "string", "description": "数据源 ID，留空=内置"},
				"range":      map[string]string{"type": "string", "description": "历史窗口，如 6h/24h/7d，默认 24h；预测窗默认等于该窗口"},
				"horizon":    map[string]string{"type": "string", "description": "自定义预测时长，如 3d/2h；留空=与 range 等长"},
				"threshold":  map[string]any{"type": "number", "description": "可选阈值；若给出则估算穿越时间"},
			},
		},
		Execute: h.execForecastMetric,
	}
	h.tools["propose_skill"] = SreyunTool{
		Name: "propose_skill",
		Description: "当识别到重复运维模式（如多次「查日志→定位 OOM→重启」）时，提议创建可复用 Skill 草稿。" +
			"confirm=false 仅提案；confirm=true 写入技能库（draft 状态，待用户启用）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]string{"type": "string", "description": "技能名"},
				"trigger": map[string]string{"type": "string", "description": "触发条件"},
				"steps":   map[string]string{"type": "string", "description": "步骤（含预期结果与回滚）"},
				"tags":    map[string]string{"type": "string", "description": "标签"},
				"confirm": map[string]any{"type": "boolean", "description": "true=入库 draft"},
				"pattern": map[string]string{"type": "string", "description": "模式指纹，用于累计重复次数"},
			},
			"required": []string{"name", "steps"},
		},
		Execute: h.execProposeSkill,
	}
	h.tools["remember_preference"] = SreyunTool{
		Name:        "remember_preference",
		Description: "记住用户长期偏好（常用看板、关注指标、习惯时间范围等），跨会话复用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":   map[string]string{"type": "string", "description": "如 preferred_range / focus_metrics / favorite_dashboard"},
				"value": map[string]string{"type": "string", "description": "偏好内容"},
			},
			"required": []string{"key", "value"},
		},
		Execute: h.execRememberPreference,
	}
}

var chatChartColors = []string{
	"#4c8dff", "#22c55e", "#f59e0b", "#ef4d5a", "#a855f7",
	"#06b6d4", "#eab308", "#ec4899", "#14b8a6", "#f97316",
}

func parseChartRange(raw string, defHours int) (from, to int64, label string) {
	to = time.Now().Unix()
	if defHours <= 0 {
		defHours = 6
	}
	hours := defHours
	s := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case s == "" || s == "6h":
		hours = 6
		label = "6h"
	case strings.HasSuffix(s, "h"):
		var n int
		if _, err := fmt.Sscanf(s, "%dh", &n); err == nil && n > 0 {
			hours = n
			label = fmt.Sprintf("%dh", n)
		}
	case strings.HasSuffix(s, "d"):
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err == nil && n > 0 {
			hours = n * 24
			label = fmt.Sprintf("%dd", n)
		}
	case strings.HasSuffix(s, "m"):
		var n int
		if _, err := fmt.Sscanf(s, "%dm", &n); err == nil && n > 0 {
			from = to - int64(n)*60
			label = fmt.Sprintf("%dm", n)
			return from, to, label
		}
	default:
		var n float64
		if _, err := fmt.Sscanf(s, "%f", &n); err == nil && n > 0 {
			hours = int(n)
			label = fmt.Sprintf("%dh", hours)
		}
	}
	if hours < 1 {
		hours = 1
	}
	if hours > 168 {
		hours = 168
	}
	if label == "" {
		label = fmt.Sprintf("%dh", hours)
	}
	from = to - int64(hours)*3600
	return from, to, label
}

func normalizeMetricKeys(raw string, def string) []string {
	if strings.TrimSpace(raw) == "" {
		raw = def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		k := strings.ToLower(strings.TrimSpace(p))
		switch k {
		case "mem", "memory", "ram":
			k = "memory"
		case "cpu", "disk", "load", "network", "net", "io":
			if k == "net" {
				k = "network"
			}
		case "all":
			for _, x := range []string{"cpu", "memory", "disk", "load"} {
				if !seen[x] {
					seen[x] = true
					out = append(out, x)
				}
			}
			continue
		default:
			if k == "" {
				continue
			}
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		out = []string{"cpu"}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func metricLabel(key string) string {
	switch key {
	case "cpu":
		return "CPU %"
	case "memory":
		return "内存 %"
	case "disk":
		return "磁盘 %"
	case "load":
		return "Load1"
	case "network":
		return "网络 MB/s"
	case "io":
		return "磁盘 IO MB/s"
	default:
		return key
	}
}

func sampleMetricValue(s shared.Sample, key string) (float64, bool) {
	switch key {
	case "cpu":
		return s.CPUPercent, true
	case "memory":
		return s.MemPercent, true
	case "disk":
		return s.DiskPercent, true
	case "load":
		return s.Load1, true
	case "network":
		return (s.NetRecvRate + s.NetSentRate) / 1048576, true
	case "io":
		return (s.DiskReadRate + s.DiskWriteRate) / 1048576, true
	default:
		return 0, false
	}
}

// hostMetricPromExpr 把主机图表的指标键翻成等价 PromQL，供预测台账事后核对实测值用。
// 口径必须与 sampleMetricValue 完全一致（含 network/io 的 /1048576 换算），
// 否则自学习算出来的 bias/scale 是在跟一个单位不同的序列比，越学越偏。
// 返回 "" 表示该键没有单序列等价表达式，调用方应放弃记台账而不是记一个错的。
func hostMetricPromExpr(hostID, key string) string {
	if hostID == "" {
		return ""
	}
	h := lblEsc(hostID)
	switch key {
	case "cpu":
		return fmt.Sprintf(`aiops_cpu_percent{host="%s"}`, h)
	case "memory":
		return fmt.Sprintf(`aiops_mem_percent{host="%s"}`, h)
	case "disk":
		return fmt.Sprintf(`aiops_disk_percent{host="%s"}`, h)
	case "load":
		return fmt.Sprintf(`aiops_load1{host="%s"}`, h)
	case "network":
		return fmt.Sprintf(`(aiops_net_recv_rate{host="%s"} + aiops_net_sent_rate{host="%s"}) / 1048576`, h, h)
	case "io":
		return fmt.Sprintf(`(aiops_disk_read_rate{host="%s"} + aiops_disk_write_rate{host="%s"}) / 1048576`, h, h)
	default:
		return ""
	}
}

func (h *SreyunCore) loadHostSamples(hostID string, from, to int64, keys ...string) []shared.Sample {
	if h.s == nil {
		return nil
	}
	samples, _ := h.s.loadDurableHostHistory(hostID, from, to, vmNamesForMetricKeys(keys))
	return samples
}

func downsampleSamples(samples []shared.Sample, maxPts int) []shared.Sample {
	if maxPts < 30 {
		maxPts = 30
	}
	n := len(samples)
	if n <= maxPts {
		return samples
	}
	out := make([]shared.Sample, 0, maxPts)
	step := float64(n-1) / float64(maxPts-1)
	for i := 0; i < maxPts; i++ {
		idx := int(math.Round(float64(i) * step))
		if idx >= n {
			idx = n - 1
		}
		out = append(out, samples[idx])
	}
	return out
}

func hostSamplesToChatChart(samples []shared.Sample, metrics []string, title string) (chart map[string]any, stats map[string]any) {
	samples = downsampleSamples(samples, 180)
	seriesDefs := make([]map[string]any, 0, len(metrics))
	for i, m := range metrics {
		seriesDefs = append(seriesDefs, map[string]any{
			"key":   "s" + fmt.Sprint(i),
			"label": metricLabel(m),
			"color": chatChartColors[i%len(chatChartColors)],
		})
	}
	rows := make([]map[string]any, 0, len(samples))
	statAcc := map[string]*struct {
		min, max, sum float64
		n             int
	}{}
	for _, m := range metrics {
		statAcc[m] = &struct {
			min, max, sum float64
			n             int
		}{min: math.Inf(1), max: math.Inf(-1)}
	}
	for _, s := range samples {
		row := map[string]any{"timestamp": s.Timestamp}
		for i, m := range metrics {
			v, ok := sampleMetricValue(s, m)
			if !ok {
				continue
			}
			row["s"+fmt.Sprint(i)] = round3(v)
			acc := statAcc[m]
			if v < acc.min {
				acc.min = v
			}
			if v > acc.max {
				acc.max = v
			}
			acc.sum += v
			acc.n++
		}
		rows = append(rows, row)
	}
	yMax := 100.0
	for _, m := range metrics {
		if m == "load" || m == "network" || m == "io" {
			yMax = 0
			break
		}
	}
	chart = map[string]any{
		"samples": rows,
		"series":  seriesDefs,
		"title":   title,
	}
	if len(samples) > 0 {
		chart["now_ts"] = samples[len(samples)-1].Timestamp
		span := samples[len(samples)-1].Timestamp - samples[0].Timestamp
		if span < 3600 {
			span = 3600
		}
		chart["horizon_sec"] = span
	}
	if yMax > 0 {
		chart["y_min"] = 0
		chart["y_max"] = yMax
	}
	stats = map[string]any{}
	for _, m := range metrics {
		acc := statAcc[m]
		if acc.n == 0 {
			continue
		}
		stats[m] = map[string]any{
			"min": round3(acc.min),
			"max": round3(acc.max),
			"avg": round3(acc.sum / float64(acc.n)),
			"n":   acc.n,
		}
	}
	return chart, stats
}

func promMatrixToChatChart(series []promMatrix, title string, maxSeries int) map[string]any {
	if maxSeries <= 0 {
		maxSeries = 6
	}
	if len(series) > maxSeries {
		series = series[:maxSeries]
	}
	// Build union of timestamps
	tsSet := map[int64]struct{}{}
	for _, s := range series {
		for _, p := range s.Points {
			tsSet[int64(p[0])] = struct{}{}
		}
	}
	tsList := make([]int64, 0, len(tsSet))
	for ts := range tsSet {
		tsList = append(tsList, ts)
	}
	sort.Slice(tsList, func(i, j int) bool { return tsList[i] < tsList[j] })
	// Downsample timestamps
	if len(tsList) > 180 {
		step := float64(len(tsList)-1) / 179.0
		reduced := make([]int64, 0, 180)
		for i := 0; i < 180; i++ {
			idx := int(math.Round(float64(i) * step))
			if idx >= len(tsList) {
				idx = len(tsList) - 1
			}
			reduced = append(reduced, tsList[idx])
		}
		tsList = reduced
	}
	seriesDefs := make([]map[string]any, 0, len(series))
	lookups := make([]map[int64]float64, len(series))
	for i, s := range series {
		lbl := chartLegendFromLabels(s.Labels)
		if lbl == "" {
			lbl = fmt.Sprintf("series-%d", i+1)
		}
		seriesDefs = append(seriesDefs, map[string]any{
			"key":   "s" + fmt.Sprint(i),
			"label": lbl,
			"color": chatChartColors[i%len(chatChartColors)],
		})
		m := make(map[int64]float64, len(s.Points))
		for _, p := range s.Points {
			m[int64(p[0])] = p[1]
		}
		lookups[i] = m
	}
	rows := make([]map[string]any, 0, len(tsList))
	last := make([]float64, len(series))
	have := make([]bool, len(series))
	for _, ts := range tsList {
		row := map[string]any{"timestamp": ts}
		for i := range series {
			if v, ok := lookups[i][ts]; ok {
				last[i] = v
				have[i] = true
				row["s"+fmt.Sprint(i)] = round3(v)
			} else if have[i] {
				// LOCF：多序列 Prom 矩阵时间戳不对齐时补齐，避免聊天图缺线
				row["s"+fmt.Sprint(i)] = round3(last[i])
			}
		}
		rows = append(rows, row)
	}
	return map[string]any{
		"samples": rows,
		"series":  seriesDefs,
		"title":   title,
	}
}

func chartLegendFromLabels(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	for _, k := range []string{"instance", "host", "hostname", "job", "device", "mountpoint", "name"} {
		if v := strings.TrimSpace(labels[k]); v != "" {
			return v
		}
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		if k == "__name__" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return strings.Join(parts, ",")
}

func round3(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*1000) / 1000
}

func chartID() string {
	t := genToken()
	if len(t) > 10 {
		return "c" + t[:10]
	}
	return "c" + t
}

func (h *SreyunCore) execRenderChart(args map[string]any) (string, error) {
	source, _ := args["source"].(string)
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		source = "host"
	}
	title, _ := args["title"].(string)
	rangeRaw, _ := args["range"].(string)
	from, to, rangeLabel := parseChartRange(rangeRaw, 6)

	if source == "promql" {
		expr, _ := args["expr"].(string)
		ds, _ := args["datasource"].(string)
		return h.renderPromqlChart(expr, ds, from, to, rangeLabel, title)
	}

	hostID, _ := args["host_id"].(string)
	metricsRaw, _ := args["metrics"].(string)
	return h.renderHostChart(hostID, metricsRaw, from, to, rangeLabel, title)
}

func (h *SreyunCore) execQueryMetricRange(args map[string]any) (string, error) {
	hostID, _ := args["host_id"].(string)
	metric, _ := args["metric"].(string)
	rangeRaw, _ := args["range"].(string)
	from, to, rangeLabel := parseChartRange(rangeRaw, 6)
	return h.renderHostChart(hostID, metric, from, to, rangeLabel, "")
}

func (h *SreyunCore) execQueryPromqlRange(args map[string]any) (string, error) {
	expr, _ := args["expr"].(string)
	ds, _ := args["datasource"].(string)
	title, _ := args["title"].(string)
	rangeRaw, _ := args["range"].(string)
	from, to, rangeLabel := parseChartRange(rangeRaw, 6)
	return h.renderPromqlChart(expr, ds, from, to, rangeLabel, title)
}

func (h *SreyunCore) renderHostChart(hostRef, metricsRaw string, from, to int64, rangeLabel, title string) (string, error) {
	hst := h.resolveHostRef(hostRef)
	if hst == nil {
		return capabilityJSON(capabilityResult{OK: false, Error: fmt.Sprintf("未找到主机 %q", hostRef)}), nil
	}
	metrics := normalizeMetricKeys(metricsRaw, "cpu")
	samples := h.loadHostSamples(hst.ID, from, to, metrics...)
	if len(samples) < 2 {
		return capabilityJSON(capabilityResult{
			OK:    false,
			Error: fmt.Sprintf("主机 %s 在 %s 内历史样本不足（%d）", hst.Hostname, rangeLabel, len(samples)),
		}), nil
	}
	if strings.TrimSpace(title) == "" {
		title = fmt.Sprintf("%s · %s", hst.Hostname, rangeLabel)
	}
	chart, stats := hostSamplesToChatChart(samples, metrics, title)
	id := chartID()
	actions := []map[string]any{
		showChartAction(id, "查看趋势图 · "+hst.Hostname, title, chart, map[string]any{
			"kind": "host_history", "host_id": hst.ID, "metrics": strings.Join(metrics, ","), "range": rangeLabel,
		}),
		drillDownAction("打开主机详情 · "+hst.Hostname, "host_detail", map[string]any{
			"host_id": hst.ID, "host_name": hst.Hostname,
		}),
		drillDownAction("拓宽到 24h 再看", "prompt", map[string]any{
			"prompt": fmt.Sprintf("请用图表展示主机 %s 最近 24 小时的 %s 趋势", hst.Hostname, strings.Join(metrics, "/")),
		}),
	}
	sum := fmt.Sprintf("已生成「%s」趋势图（%d 点，指标 %s）", title, len(samples), strings.Join(metrics, ","))
	if note := historyCoverageNote(samples, from, to); note != "" {
		sum += "。" + note
	}
	return capabilityJSON(capabilityResult{
		OK:      true,
		Summary: sum,
		Data: map[string]any{
			"chart_id": id,
			"host_id":  hst.ID,
			"hostname": hst.Hostname,
			"range":    rangeLabel,
			"metrics":  metrics,
			"points":   len(samples),
			"stats":    stats,
		},
		UIActions: actions,
	}), nil
}

func (h *SreyunCore) renderPromqlChart(expr, dsID string, from, to int64, rangeLabel, title string) (string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return capabilityJSON(capabilityResult{OK: false, Error: "expr 必填"}), nil
	}
	if h.s == nil {
		return capabilityJSON(capabilityResult{OK: false, Error: "服务不可用"}), nil
	}
	step := (to - from) / 180
	if step < 15 {
		step = 15
	}
	series, ok := h.s.dashRangeSeries(strings.TrimSpace(dsID), expr, from, to, step)
	if !ok || len(series) == 0 {
		return capabilityJSON(capabilityResult{OK: false, Error: "PromQL 区间查询无数据或数据源不可用"}), nil
	}
	if strings.TrimSpace(title) == "" {
		title = trimLine(expr, 48) + " · " + rangeLabel
	}
	chart := promMatrixToChatChart(series, title, 6)
	id := chartID()
	actions := []map[string]any{
		showChartAction(id, "查看 PromQL 图表", title, chart, map[string]any{
			"kind": "promql", "expr": expr, "datasource": dsID, "range": rangeLabel,
		}),
		drillDownAction("拓宽到 24h", "prompt", map[string]any{
			"prompt": fmt.Sprintf("请用 PromQL `%s` 绘制最近 24 小时趋势图", expr),
		}),
	}
	return capabilityJSON(capabilityResult{
		OK:      true,
		Summary: fmt.Sprintf("已生成 PromQL 图表「%s」（%d 条序列）", title, len(series)),
		Data: map[string]any{
			"chart_id": id,
			"expr":     expr,
			"range":    rangeLabel,
			"series_n": len(series),
		},
		UIActions: actions,
	}), nil
}

func (h *SreyunCore) execShowInstantStat(args map[string]any) (string, error) {
	hostRef, _ := args["host_id"].(string)
	metric, _ := args["metric"].(string)
	rangeRaw, _ := args["range"].(string)
	hst := h.resolveHostRef(hostRef)
	if hst == nil {
		return capabilityJSON(capabilityResult{OK: false, Error: fmt.Sprintf("未找到主机 %q", hostRef)}), nil
	}
	keys := normalizeMetricKeys(metric, "cpu")
	key := keys[0]
	var value float64
	ok := false
	if hst.Latest != nil {
		value, ok = sampleMetricValue(*hst.Latest, key)
	}
	from, to, rangeLabel := parseChartRange(rangeRaw, 1)
	samples := h.loadHostSamples(hst.ID, from, to, key)
	spark := make([][2]float64, 0, 60)
	if len(samples) > 0 {
		ds := downsampleSamples(samples, 60)
		for _, s := range ds {
			if v, good := sampleMetricValue(s, key); good {
				spark = append(spark, [2]float64{float64(s.Timestamp), round3(v)})
				if !ok {
					value, ok = v, true
				}
			}
		}
	}
	if !ok {
		return capabilityJSON(capabilityResult{OK: false, Error: "暂无该指标数据"}), nil
	}
	unit := "%"
	if key == "load" {
		unit = ""
	}
	if key == "network" || key == "io" {
		unit = "MB/s"
	}
	thresholds := map[string]float64{"warn": 75, "crit": 90}
	if key == "load" {
		cores := 1.0
		if hst.Latest != nil && hst.Latest.CPUCores > 0 {
			cores = float64(hst.Latest.CPUCores)
		}
		thresholds = map[string]float64{"warn": cores * 0.7, "crit": cores}
	}
	title := fmt.Sprintf("%s · %s", hst.Hostname, metricLabel(key))
	id := chartID()
	actions := []map[string]any{
		showStatAction(id, "查看 "+metricLabel(key), title, round3(value), unit, spark, thresholds),
		drillDownAction("查看 "+rangeLabel+" 趋势图", "prompt", map[string]any{
			"prompt": fmt.Sprintf("请用图表展示主机 %s 最近 %s 的 %s 趋势", hst.Hostname, rangeLabel, key),
		}),
		drillDownAction("打开主机详情", "host_detail", map[string]any{
			"host_id": hst.ID, "host_name": hst.Hostname,
		}),
	}
	return capabilityJSON(capabilityResult{
		OK:      true,
		Summary: fmt.Sprintf("%s 当前 %s = %.2f%s", hst.Hostname, metricLabel(key), value, unit),
		Data: map[string]any{
			"host_id": hst.ID, "metric": key, "value": round3(value), "unit": unit,
		},
		UIActions: actions,
	}), nil
}

func (h *SreyunCore) execAnalyzeMetricTrend(args map[string]any) (string, error) {
	hostRef, _ := args["host_id"].(string)
	metricsRaw, _ := args["metrics"].(string)
	hours := 6.0
	if v, ok := args["hours"].(float64); ok && v > 0 {
		hours = v
	}
	if hours > 72 {
		hours = 72
	}
	hst := h.resolveHostRef(hostRef)
	if hst == nil {
		return capabilityJSON(capabilityResult{OK: false, Error: fmt.Sprintf("未找到主机 %q", hostRef)}), nil
	}
	metrics := normalizeMetricKeys(metricsRaw, "cpu,memory,disk,load")
	from := time.Now().Unix() - int64(hours)*3600
	to := time.Now().Unix()
	rangeLabel := fmt.Sprintf("%.0fh", hours)
	samples := h.loadHostSamples(hst.ID, from, to, metrics...)
	if len(samples) < 2 {
		return capabilityJSON(capabilityResult{
			OK:    false,
			Error: fmt.Sprintf("主机 %s 最近 %s 历史不足", hst.Hostname, rangeLabel),
		}), nil
	}
	title := fmt.Sprintf("%s 趋势分析 · %s", hst.Hostname, rangeLabel)
	chart, stats := hostSamplesToChatChart(samples, metrics, title)

	// early/late window deltas for narrative
	n := len(samples)
	third := n / 3
	if third < 1 {
		third = 1
	}
	early, late := samples[:third], samples[n-third:]
	avgWin := func(ss []shared.Sample, key string) float64 {
		var sum float64
		var c int
		for _, s := range ss {
			if v, ok := sampleMetricValue(s, key); ok {
				sum += v
				c++
			}
		}
		if c == 0 {
			return 0
		}
		return sum / float64(c)
	}
	type trendRow struct {
		Metric string  `json:"metric"`
		Early  float64 `json:"early_avg"`
		Late   float64 `json:"late_avg"`
		Delta  float64 `json:"delta"`
		Trend  string  `json:"trend"`
	}
	trends := make([]trendRow, 0, len(metrics))
	notable := make([]string, 0)
	for _, m := range metrics {
		e, l := avgWin(early, m), avgWin(late, m)
		d := l - e
		row := trendRow{Metric: metricLabel(m), Early: round3(e), Late: round3(l), Delta: round3(d), Trend: trendArrow(d)}
		if m == "load" {
			row.Trend = trendArrow(d * 10)
		}
		trends = append(trends, row)
		if math.Abs(d) >= 5 || (m == "load" && math.Abs(d) >= 0.5) {
			notable = append(notable, fmt.Sprintf("%s %s%.1f", metricLabel(m), row.Trend, d))
		}
	}
	id := chartID()
	actions := []map[string]any{
		showChartAction(id, "查看趋势图 · "+hst.Hostname, title, chart, map[string]any{
			"kind": "host_history", "host_id": hst.ID, "metrics": strings.Join(metrics, ","), "range": rangeLabel,
		}),
		drillDownAction("打开主机详情下钻", "host_detail", map[string]any{
			"host_id": hst.ID, "host_name": hst.Hostname,
		}),
		drillDownAction("只看 CPU 曲线", "prompt", map[string]any{
			"prompt": fmt.Sprintf("请用图表展示主机 %s 最近 %s 的 CPU 趋势", hst.Hostname, rangeLabel),
		}),
		drillDownAction("对比内存与磁盘", "prompt", map[string]any{
			"prompt": fmt.Sprintf("请用图表对比主机 %s 最近 %s 的内存和磁盘使用率", hst.Hostname, rangeLabel),
		}),
	}
	sum := fmt.Sprintf("已完成 %s 近 %s 趋势分析", hst.Hostname, rangeLabel)
	if note := historyCoverageNote(samples, from, to); note != "" {
		sum += "。" + note + "，勿把局部波动当成全程趋势"
	} else if len(notable) > 0 {
		sum += "；显著变化：" + strings.Join(notable, "，")
	} else {
		sum += "；整体波动平稳"
	}
	if chart != nil {
		chart["now_ts"] = to
		chart["horizon_sec"] = int64(hours) * 3600
	}

	// 预测触阈提醒：对 CPU/内存/磁盘做外推，文字提醒并触发提前预警
	th := h.s.cfg.Thresholds()
	step := (to - from) / 180
	if step < 15 {
		step = 15
	}
	warnParts := make([]string, 0)
	for _, m := range metrics {
		var thr float64
		switch m {
		case "cpu":
			thr = th.CPUWarn
		case "memory", "mem":
			thr = th.MemWarn
		case "disk":
			thr = th.DiskWarn
		default:
			continue
		}
		hist := make([][2]float64, 0, len(samples))
		for _, s := range samples {
			if v, ok := sampleMetricValue(s, m); ok {
				hist = append(hist, [2]float64{float64(s.Timestamp), v})
			}
		}
		if len(hist) < 4 {
			continue
		}
		if cross, ok := forecastCrossThreshold(hist, thr, step); ok && cross > to {
			eta := cross - to
			warnParts = append(warnParts, fmt.Sprintf("%s 约 %s 后触及 %.0f", metricLabel(m), formatHorizon(eta), thr))
			h.s.raiseForecastEarlyWarning(hst.ID, hst.Hostname, m, thr, cross, to, hist[len(hist)-1][1])
		}
	}
	if len(warnParts) > 0 {
		sum += "。⚠️ 预测预警：" + strings.Join(warnParts, "；") + "——建议提前处置，将问题抹杀在摇篮中"
	}

	return capabilityJSON(capabilityResult{
		OK:      true,
		Summary: sum,
		Data: map[string]any{
			"chart_id":          id,
			"host_id":           hst.ID,
			"hostname":          hst.Hostname,
			"range":             rangeLabel,
			"trends":            trends,
			"stats":             stats,
			"points":            len(samples),
			"forecast_warnings": warnParts,
		},
		UIActions: actions,
	}), nil
}

func (h *SreyunCore) execForecastMetric(args map[string]any) (string, error) {
	if h.s == nil {
		return capabilityJSON(capabilityResult{OK: false, Error: "服务不可用"}), nil
	}
	hostRef, _ := args["host_id"].(string)
	metric, _ := args["metric"].(string)
	expr, _ := args["expr"].(string)
	ds, _ := args["datasource"].(string)
	rangeRaw, _ := args["range"].(string)
	horizonRaw, _ := args["horizon"].(string)
	var threshold float64
	hasThr := false
	if v, ok := args["threshold"].(float64); ok {
		threshold, hasThr = v, true
	}

	metricKeyGuess := strings.ToLower(strings.TrimSpace(firstNonEmpty(metric, expr)))
	isStorage := strings.Contains(metricKeyGuess, "disk") || strings.Contains(metricKeyGuess, "storage") ||
		strings.Contains(metricKeyGuess, "filesystem") || strings.Contains(metricKeyGuess, "空间")
	defaultHours := 24
	if isStorage {
		defaultHours = 72 // 存储类用更长历史，提升填满时间估算
	}
	from, to, rangeLabel := parseChartRange(rangeRaw, defaultHours)
	rangeSec := to - from
	horizonSec := rangeSec
	if isStorage && horizonSec < 48*3600 {
		horizonSec = 48 * 3600
	}
	if strings.TrimSpace(horizonRaw) != "" {
		hf, ht, _ := parseChartRange(horizonRaw, 24)
		if ht > hf {
			horizonSec = ht - hf
		}
	}
	step := rangeSec / 180
	if step < 15 {
		step = 15
	}

	var hist [][2]float64
	title := "趋势预测"
	metricKey := "cpu"
	var hostID, hostname string
	if strings.TrimSpace(expr) != "" {
		fitFrom := from - forecastFitLookback(rangeSec)
		series, ok := h.s.dashRangeSeries(strings.TrimSpace(ds), strings.TrimSpace(expr), fitFrom, to, step)
		if !ok || len(series) == 0 || len(series[0].Points) == 0 {
			return capabilityJSON(capabilityResult{OK: false, Error: "PromQL 历史数据不足，无法预测"}), nil
		}
		hist = series[0].Points
		title = trimLine(expr, 40) + " · 预测"
		metricKey = sanitizeForecastLearnKey(expr)
	} else {
		hst := h.resolveHostRef(hostRef)
		if hst == nil {
			return capabilityJSON(capabilityResult{OK: false, Error: "请提供 host_id 或 expr"}), nil
		}
		keys := normalizeMetricKeys(metric, "cpu")
		metricKey = keys[0]
		hostID, hostname = hst.ID, hst.Hostname
		fitFrom := from - forecastFitLookback(rangeSec)
		samples := h.loadHostSamples(hst.ID, fitFrom, to, metricKey)
		for _, s := range samples {
			if v, ok := sampleMetricValue(s, metricKey); ok {
				hist = append(hist, [2]float64{float64(s.Timestamp), v})
			}
		}
		title = fmt.Sprintf("%s · %s 预测", hst.Hostname, metricLabel(metricKey))
	}
	if !hasThr && h.s != nil {
		th := h.s.cfg.Thresholds()
		switch metricKey {
		case "cpu":
			threshold, hasThr = th.CPUWarn, true
		case "memory", "mem":
			threshold, hasThr = th.MemWarn, true
		case "disk":
			threshold, hasThr = th.DiskWarn, true
		}
	}
	learnKey := "ai:" + metricKey
	if hostID != "" {
		learnKey = "ai:" + hostID + ":" + metricKey
	}
	band, mape, r2, method, errMsg := robustForecastWithKey(hist, to, horizonSec, step, learnKey, fcModelAuto)
	if errMsg != "" {
		return capabilityJSON(capabilityResult{OK: false, Error: errMsg}), nil
	}

	// 记台账 + 套用已学到的偏差/尺度校准，闭合"预测 → 等实测 → 评估 → 纠偏"这一圈。
	//
	// 这一步此前完全没有接线：finalizeForecastWithLearning / noteForecastLedger 在整个
	// 生产代码里没有任何调用点（只有测试在调），所以 Ledgers 永远是空的，
	// runForecastLearnLoop 每 10 分钟醒来什么都评估不到，Calibs 的 EvalCount 恒为 0，
	// 于是 learnAdjustCandidateScore 和 applyForecastCalibration 全都在第一行就 return，
	// forecastBiasHints 也永远取不到内容——"预测自学习"整条链路是死的。
	//
	// evalExpr 必须给得出来才能评估：evaluateOneLedger 用 PromQL 去查当时的真实值，
	// Expr 为空的台账会被直接跳过。主机指标这里补上等价的 PromQL（与
	// sampleMetricValue 的口径逐字对应，含 /1048576 的单位换算）。
	evalExpr := strings.TrimSpace(expr)
	if evalExpr == "" && hostID != "" {
		evalExpr = hostMetricPromExpr(hostID, metricKey)
	}
	anchor := hist[len(hist)-1][1]
	band, method, _ = h.s.finalizeForecastWithLearning(learnKey, method, evalExpr, band, to, horizonSec, step, anchor)

	// Build chat chart: history solid + forecast dashed
	seriesDefs := []map[string]any{
		{"key": "hist", "label": "历史", "color": chatChartColors[0]},
		{"key": "fc", "label": "预测", "color": chatChartColors[3], "dashed": true, "kind": "forecast"},
	}
	tsMap := map[int64]map[string]any{}
	for _, p := range hist {
		ts := int64(p[0])
		row := tsMap[ts]
		if row == nil {
			row = map[string]any{"timestamp": ts}
			tsMap[ts] = row
		}
		row["hist"] = round3(p[1])
	}
	for _, p := range band {
		ts := int64(p.TS)
		row := tsMap[ts]
		if row == nil {
			row = map[string]any{"timestamp": ts}
			tsMap[ts] = row
		}
		row["fc"] = round3(p.Value)
	}
	tsList := make([]int64, 0, len(tsMap))
	for ts := range tsMap {
		tsList = append(tsList, ts)
	}
	sort.Slice(tsList, func(i, j int) bool { return tsList[i] < tsList[j] })
	rows := make([]map[string]any, 0, len(tsList))
	for _, ts := range tsList {
		rows = append(rows, tsMap[ts])
	}
	chart := map[string]any{"samples": rows, "series": seriesDefs, "title": title, "now_ts": to}

	sum := fmt.Sprintf("已完成预测（%s，MAPE≈%.1f%%，R²≈%.2f，方法 %s，展望 %s）",
		rangeLabel, mape, r2, method, formatHorizon(horizonSec))
	lastVal := hist[len(hist)-1][1]
	data := map[string]any{
		"mape": mape, "r2": r2, "method": method,
		"horizon_sec": horizonSec, "points": len(band),
		"forecast_last": round3(band[len(band)-1].Value),
		"forecast_hi":   round3(band[len(band)-1].Hi),
		"forecast_lo":   round3(band[len(band)-1].Lo),
		"metric":        metricKey,
	}
	// 历史偏差记忆真正回到模型手上。原来这里只是「算出来、扔掉、然后在文案里
	// 声称已注入」——bias 变量赋值后从未被使用，模型看到的上下文里根本没有它。
	if bias := h.s.forecastBiasHints(metricKey+" "+title, 2); bias != "" {
		data["bias_hints"] = bias
		sum += "；已结合历史偏差记忆修正"
	}
	if hasThr {
		// 用画出来的那条曲线判断穿越，保证「图上看到的」和「据以预警的」是同一个预测。
		if cross, ok := crossThresholdInBand(band, lastVal, threshold); ok {
			data["cross_threshold_at"] = cross
			data["cross_threshold_in"] = cross - to
			data["threshold"] = threshold
			sum += fmt.Sprintf("；⚠️ 按趋势预计约 %s 后触及阈值 %.2f，请提前关注", formatHorizon(cross-to), threshold)
			if hostID != "" {
				h.s.raiseForecastEarlyWarning(hostID, hostname, metricKey, threshold, cross, to, lastVal)
			} else {
				go h.s.rememberAI("forecast_bias", "forecast_anchor:"+metricKey,
					fmt.Sprintf("预测锚点：%s 阈值 %.2f，预计穿越时间戳 %d，预测末端 %.2f", metricKey, threshold, cross, band[len(band)-1].Value))
			}
		} else {
			sum += fmt.Sprintf("；在展望窗口内未必触及阈值 %.2f", threshold)
		}
	}
	id := chartID()
	actions := []map[string]any{
		showChartAction(id, "查看预测曲线", title, chart, map[string]any{
			"kind": "forecast", "range": rangeLabel, "horizon_sec": horizonSec,
		}),
	}
	return capabilityJSON(capabilityResult{OK: true, Summary: sum, Data: data, UIActions: actions}), nil
}

func formatHorizon(sec int64) string {
	if sec < 0 {
		sec = -sec
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm", sec/60)
	}
	if sec < 48*3600 {
		return fmt.Sprintf("%.0fh", float64(sec)/3600)
	}
	return fmt.Sprintf("%.0fd", float64(sec)/86400)
}

func (h *SreyunCore) execProposeSkill(args map[string]any) (string, error) {
	if h.s == nil {
		return capabilityJSON(capabilityResult{OK: false, Error: "服务不可用"}), nil
	}
	name, _ := args["name"].(string)
	trigger, _ := args["trigger"].(string)
	steps, _ := args["steps"].(string)
	tags, _ := args["tags"].(string)
	pattern, _ := args["pattern"].(string)
	confirm, _ := args["confirm"].(bool)
	actor := scopeActorFromArgs(args)
	if pattern == "" {
		pattern = name
	}
	propose, autoName, autoTrig, autoSteps := h.s.trackOpsPattern(actor, pattern, steps)
	if !confirm {
		msg := "已记录运维模式"
		if propose {
			msg = fmt.Sprintf("检测到重复模式，建议创建 Skill「%s」。请向用户确认后再次调用 propose_skill(confirm=true)", autoName)
		}
		return capabilityJSON(capabilityResult{
			OK: true, Summary: msg,
			Data: map[string]any{
				"should_propose": propose,
				"draft_name":     firstNonEmptyOrDash(autoName, name),
				"draft_trigger":  firstNonEmptyOrDash(autoTrig, trigger),
				"draft_steps":    firstNonEmptyOrDash(autoSteps, steps),
			},
		}), nil
	}
	id, err := h.s.proposeSkillDraft(firstNonEmpty(name, autoName), firstNonEmpty(trigger, autoTrig), firstNonEmpty(steps, autoSteps), tags)
	if err != nil {
		return capabilityJSON(capabilityResult{OK: false, Error: err.Error()}), nil
	}
	return capabilityJSON(capabilityResult{
		OK:      true,
		Summary: fmt.Sprintf("已创建 Skill 草稿 #%d，请在技能库中审核启用", id),
		Data:    map[string]any{"skill_id": id, "status": "draft"},
	}), nil
}

func (h *SreyunCore) execRememberPreference(args map[string]any) (string, error) {
	if h.s == nil {
		return capabilityJSON(capabilityResult{OK: false, Error: "服务不可用"}), nil
	}
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	actor := scopeActorFromArgs(args)
	h.s.rememberUserPreference(actor, key, value)
	return capabilityJSON(capabilityResult{
		OK: true, Summary: "已记住偏好 " + strings.TrimSpace(key),
		Data: map[string]any{"key": key, "value": value},
	}), nil
}
