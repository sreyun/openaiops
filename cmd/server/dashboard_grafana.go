package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ============================================================================
// 导入 Grafana 看板（务实子集）。
//
// 支持 timeseries/graph、stat/singlestat、gauge、bargauge、table、text 面板 + PromQL
// 查询目标 + 模板变量（label_values 解析）。无法映射的面板类型保留为 unsupported 占位
// （带原始查询），不静默丢弃。容忍多版本 schema：flat panels[] + 嵌套 row.panels + 旧 rows[]。
// ============================================================================

type grafanaDash struct {
	Title      string   `json:"title"`
	Tags       []string `json:"tags"`
	Templating struct {
		List []grafanaVar `json:"list"`
	} `json:"templating"`
	Panels []grafanaPanel `json:"panels"`
	Rows   []struct {
		Panels []grafanaPanel `json:"panels"`
	} `json:"rows"`
}

type grafanaVar struct {
	Name    string          `json:"name"`
	Label   string          `json:"label"`
	Type    string          `json:"type"`
	Query   json.RawMessage `json:"query"` // string 或 {query:...}
	Current struct {
		Value json.RawMessage `json:"value"` // string 或 []string
	} `json:"current"`
	Options []struct {
		Value string `json:"value"`
	} `json:"options"`
	Multi      bool `json:"multi"`
	IncludeAll bool `json:"includeAll"`
}

type grafanaPanel struct {
	ID      int      `json:"id"`
	Title   string   `json:"title"`
	Type    string   `json:"type"`
	GridPos DashGrid `json:"gridPos"`
	Targets []struct {
		Expr         string `json:"expr"`
		LegendFormat string `json:"legendFormat"`
		RefID        string `json:"refId"`
	} `json:"targets"`
	FieldConfig struct {
		Defaults  grafanaFieldDefaults   `json:"defaults"`
		Overrides []grafanaFieldOverride `json:"overrides"`
	} `json:"fieldConfig"`
	Options json.RawMessage `json:"options"` // Grafana panel options (legend etc.)
	Yaxes   []struct {
		Format string `json:"format"`
	} `json:"yaxes"`
	Format  string         `json:"format"`
	Content string         `json:"content"`
	Panels  []grafanaPanel `json:"panels"` // 折叠 row 内嵌面板
}

type grafanaFieldDefaults struct {
	Unit     string   `json:"unit"`
	Min      *float64 `json:"min"`
	Max      *float64 `json:"max"`
	Decimals int      `json:"decimals"`
	NoValue  string   `json:"noValue"`
	Color    struct {
		Mode       string `json:"mode"`
		FixedColor string `json:"fixedColor"`
	} `json:"color"`
	Thresholds struct {
		Mode  string `json:"mode"`
		Steps []struct {
			Value *float64 `json:"value"`
			Color string   `json:"color"`
		} `json:"steps"`
	} `json:"thresholds"`
	Mappings []grafanaValueMapping `json:"mappings"`
	Custom   grafanaFieldCustom    `json:"custom"`
}

type grafanaFieldCustom struct {
	DrawStyle         string      `json:"drawStyle"`
	LineInterpolation string      `json:"lineInterpolation"`
	LineWidth         json.Number `json:"lineWidth"`
	FillOpacity       json.Number `json:"fillOpacity"`
	GradientMode      string      `json:"gradientMode"`
	ShowPoints        string      `json:"showPoints"` // auto|always|never
	PointSize         json.Number `json:"pointSize"`
	SpanNulls         interface{} `json:"spanNulls"` // bool or number
	Stacking          struct {
		Mode  string `json:"mode"` // none|normal|percent
		Group string `json:"group"`
	} `json:"stacking"`
	AxisPlacement string   `json:"axisPlacement"`
	AxisLabel     string   `json:"axisLabel"`
	AxisSoftMin   *float64 `json:"axisSoftMin"`
	AxisSoftMax   *float64 `json:"axisSoftMax"`
}

type grafanaValueMapping struct {
	Type    string          `json:"type"` // value|range|regex|special
	Options json.RawMessage `json:"options"`
	// Legacy Grafana 7 flat form
	Value   interface{} `json:"value"`
	Text    string      `json:"text"`
	From    *float64    `json:"from"`
	To      *float64    `json:"to"`
	Pattern string      `json:"pattern"`
}

type grafanaFieldOverride struct {
	Matcher struct {
		ID      string          `json:"id"`
		Options json.RawMessage `json:"options"`
	} `json:"matcher"`
	Properties []struct {
		ID    string          `json:"id"`
		Value json.RawMessage `json:"value"`
	} `json:"properties"`
}

// mapGrafanaDashboard 把 Grafana 看板 JSON 映射为内部 Dashboard。
func mapGrafanaDashboard(raw []byte, nameOverride, source string) (Dashboard, error) {
	// grafana.com 下载多为裸看板模型；也可能包一层 {"dashboard": {...}}。
	var probe struct {
		Dashboard json.RawMessage `json:"dashboard"`
	}
	body := raw
	if json.Unmarshal(raw, &probe) == nil && len(probe.Dashboard) > 0 {
		body = probe.Dashboard
	}
	var g grafanaDash
	if err := json.Unmarshal(body, &g); err != nil {
		return Dashboard{}, fmt.Errorf("解析 Grafana JSON 失败：%v", err)
	}

	d := Dashboard{Source: source, Tags: g.Tags}
	d.Name = strings.TrimSpace(nameOverride)
	if d.Name == "" {
		d.Name = strings.TrimSpace(g.Title)
	}
	if d.Name == "" {
		d.Name = "导入的看板"
	}

	// 模板变量
	for _, gv := range g.Templating.List {
		dv, ok := mapGrafanaVar(gv)
		if ok {
			d.Vars = append(d.Vars, dv)
		}
	}

	// 面板：flat + 嵌套 row + 旧 rows[]
	var flat []grafanaPanel
	flat = append(flat, flattenPanels(g.Panels)...)
	for _, r := range g.Rows {
		flat = append(flat, flattenPanels(r.Panels)...)
	}
	nextID := 1
	for _, gp := range flat {
		p := mapGrafanaPanel(gp)
		if p.ID == 0 {
			p.ID = nextID
		}
		if p.ID >= nextID {
			nextID = p.ID + 1
		}
		d.Panels = append(d.Panels, p)
	}
	if len(d.Panels) == 0 {
		return Dashboard{}, fmt.Errorf("未从该看板解析到任何面板")
	}
	sortPanels(d.Panels)
	healImportedDashboard(&d)
	return d, nil
}

// flattenPanels 展开 row 型面板的内嵌 panels（折叠行），返回可渲染面板列表。
func flattenPanels(panels []grafanaPanel) []grafanaPanel {
	var out []grafanaPanel
	for _, p := range panels {
		if p.Type == "row" {
			if len(p.Panels) > 0 {
				out = append(out, flattenPanels(p.Panels)...)
			}
			continue // 行标题本身不渲染为面板
		}
		out = append(out, p)
	}
	return out
}

func mapGrafanaVar(gv grafanaVar) (DashVar, bool) {
	switch gv.Type {
	case "query", "custom", "constant", "textbox":
	default:
		return DashVar{}, false // datasource/adhoc/interval 等跳过
	}
	dv := DashVar{
		Name: gv.Name, Label: gv.Label, Type: gv.Type,
		Multi: gv.Multi, IncludeAll: gv.IncludeAll,
		Query:   rawQueryString(gv.Query),
		Current: rawCurrentValue(gv.Current.Value),
	}
	for _, o := range gv.Options {
		if o.Value != "" && o.Value != "$__all" {
			dv.Options = append(dv.Options, o.Value)
		}
	}
	return dv, gv.Name != ""
}

func mapGrafanaPanel(gp grafanaPanel) DashPanel {
	p := DashPanel{
		ID: gp.ID, Title: gp.Title, Grid: gp.GridPos, Text: gp.Content,
		Min: gp.FieldConfig.Defaults.Min, Max: gp.FieldConfig.Defaults.Max,
		Decimals: gp.FieldConfig.Defaults.Decimals,
	}
	if p.Grid.W == 0 {
		p.Grid.W = 12
	}
	if p.Grid.H == 0 {
		p.Grid.H = 8
	}
	// 单位：新版 fieldConfig → 旧 graph yaxes → 旧 singlestat/gauge format
	p.Unit = gp.FieldConfig.Defaults.Unit
	if p.Unit == "" && len(gp.Yaxes) > 0 {
		p.Unit = gp.Yaxes[0].Format
	}
	if p.Unit == "" {
		p.Unit = gp.Format
	}
	// 目标（仅保留含 PromQL expr 的）；超过上限截断，避免整板导入失败。
	for _, t := range gp.Targets {
		if strings.TrimSpace(t.Expr) == "" {
			continue
		}
		if len(p.Targets) >= maxDashboardTargets {
			break
		}
		p.Targets = append(p.Targets, DashTarget{Expr: t.Expr, Legend: t.LegendFormat, RefID: t.RefID})
	}
	p.Type = mapGrafanaPanelType(gp.Type)
	if p.Type == "unsupported" {
		p.RawType = gp.Type
	} else if !dashNoTargetTypes[p.Type] && !dashComingSoonTypes[p.Type] && len(p.Targets) == 0 {
		// 混合数据源 / 无 PromQL 的面板：降级为 unsupported 占位，避免 normalize 整板失败。
		p.RawType = gp.Type
		p.Type = "unsupported"
	}
	p.Options = mapGrafanaPanelOptions(gp)
	return p
}

// mapGrafanaPanelOptions maps Grafana fieldConfig.defaults/overrides + panel options into DashPanelOptions.
func mapGrafanaPanelOptions(gp grafanaPanel) DashPanelOptions {
	o := mapGrafanaFieldDefaults(gp.FieldConfig.Defaults)
	for _, ov := range gp.FieldConfig.Overrides {
		mapped := mapGrafanaOverride(ov)
		if mapped.MatcherID != "" || mapped.Unit != "" || mapped.Options.Palette != "" ||
			len(mapped.Options.Thresholds) > 0 || len(mapped.Options.Mappings) > 0 {
			o.Overrides = append(o.Overrides, mapped)
		}
	}
	// Legend placement from Grafana options JSON when present.
	if len(gp.Options) > 0 {
		var opt struct {
			Legend struct {
				DisplayMode string `json:"displayMode"`
				Placement   string `json:"placement"`
				ShowLegend  *bool  `json:"showLegend"`
			} `json:"legend"`
		}
		if json.Unmarshal(gp.Options, &opt) == nil {
			if opt.Legend.ShowLegend != nil && !*opt.Legend.ShowLegend {
				o.Legend = "hidden"
			} else if opt.Legend.DisplayMode == "hidden" {
				o.Legend = "hidden"
			} else if opt.Legend.Placement == "right" {
				o.Legend = "right"
			} else if opt.Legend.Placement == "top" {
				o.Legend = "top"
			} else if opt.Legend.Placement == "bottom" {
				o.Legend = "bottom"
			}
		}
	}
	return o
}

func mapGrafanaFieldDefaults(def grafanaFieldDefaults) DashPanelOptions {
	var o DashPanelOptions
	if def.Decimals > 0 {
		d := def.Decimals
		if d > 10 {
			d = 10
		}
		o.Decimals = &d
	}
	o.NoValue = strings.TrimSpace(def.NoValue)
	o.ColorMode = strings.TrimSpace(def.Color.Mode)
	o.Palette = mapGrafanaColorMode(def.Color.Mode)
	if strings.EqualFold(def.Color.Mode, "fixed") {
		if c := mapGrafanaColor(def.Color.FixedColor); c != "" {
			o.Colors = []string{c}
		}
	}
	mode := strings.ToLower(strings.TrimSpace(def.Thresholds.Mode))
	if mode == "percentage" {
		o.ThresholdMode = "percentage"
	} else if mode != "" {
		o.ThresholdMode = "absolute"
	}
	for _, s := range def.Thresholds.Steps {
		if s.Value == nil {
			// Grafana base step (null): keep as 0 when color present so ladder is complete.
			c := mapGrafanaColor(s.Color)
			if c == "" {
				continue
			}
			o.Thresholds = append(o.Thresholds, DashThreshold{Value: 0, Color: c})
			continue
		}
		c := mapGrafanaColor(s.Color)
		if c == "" {
			continue
		}
		o.Thresholds = append(o.Thresholds, DashThreshold{Value: *s.Value, Color: c})
	}
	o.Mappings = mapGrafanaMappings(def.Mappings)
	applyGrafanaCustom(&o, def.Custom)
	return o
}

func mapGrafanaColorMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "palette-classic", "palette-classic-by-name":
		return "classic"
	case "fixed":
		return "custom"
	case "continuous-GrYlRd", "continuous-RdYlGr", "thresholds":
		return "traffic"
	case "continuous-BlYlRd", "continuous-BlPu", "continuous-blues":
		return "cool"
	case "continuous-YlRd", "continuous-OrRd", "continuous-reds":
		return "warm"
	case "continuous-Grays", "continuous-greys":
		return "mono"
	default:
		return ""
	}
}

func applyGrafanaCustom(o *DashPanelOptions, c grafanaFieldCustom) {
	if o == nil {
		return
	}
	ds := strings.ToLower(strings.TrimSpace(c.DrawStyle))
	o.DrawStyle = ds
	switch ds {
	case "bars":
		o.ChartStyle = "bar"
	case "line":
		o.ChartStyle = "line"
	case "points":
		o.ChartStyle = "line"
		o.ShowPoints = true
	}
	if strings.EqualFold(c.LineInterpolation, "smooth") {
		o.Smooth = true
	}
	switch strings.ToLower(strings.TrimSpace(c.ShowPoints)) {
	case "always":
		o.ShowPoints = true
	case "never":
		o.ShowPoints = false
	}
	if v, err := c.LineWidth.Float64(); err == nil {
		o.LineWidth = &v
	}
	if v, err := c.FillOpacity.Float64(); err == nil {
		o.FillOpacity = &v
		if v > 0 && o.ChartStyle == "line" {
			o.ChartStyle = "area"
		}
	}
	if v, err := c.PointSize.Float64(); err == nil {
		o.PointSize = &v
	}
	o.GradientMode = strings.ToLower(strings.TrimSpace(c.GradientMode))
	o.AxisPlacement = strings.ToLower(strings.TrimSpace(c.AxisPlacement))
	if strings.EqualFold(c.Stacking.Mode, "normal") || strings.EqualFold(c.Stacking.Mode, "percent") {
		o.Stacked = true
	}
	switch sp := c.SpanNulls.(type) {
	case bool:
		o.SpanNulls = sp
	case float64:
		o.SpanNulls = sp != 0
	case json.Number:
		if f, err := sp.Float64(); err == nil {
			o.SpanNulls = f != 0
		}
	}
}

func mapGrafanaMappings(in []grafanaValueMapping) []DashValueMapping {
	var out []DashValueMapping
	for _, m := range in {
		typ := strings.ToLower(strings.TrimSpace(m.Type))
		switch typ {
		case "value", "range", "regex", "special":
		case "valuemap", "valueMap":
			typ = "value"
		default:
			// Legacy without type: infer
			if m.Pattern != "" {
				typ = "regex"
			} else if m.From != nil || m.To != nil {
				typ = "range"
			} else if m.Value != nil || len(m.Options) > 0 {
				typ = "value"
			} else {
				continue
			}
		}
		dm := DashValueMapping{Type: typ}
		// Modern options blob
		if len(m.Options) > 0 {
			switch typ {
			case "value":
				var opt map[string]struct {
					Text  string `json:"text"`
					Color string `json:"color"`
					Index int    `json:"index"`
				}
				if json.Unmarshal(m.Options, &opt) == nil {
					for k, v := range opt {
						out = append(out, DashValueMapping{
							Type: "value", Value: k, Text: v.Text,
							Color: mapGrafanaColor(v.Color), Index: v.Index,
						})
					}
					continue
				}
			case "range":
				var opt struct {
					From   *float64 `json:"from"`
					To     *float64 `json:"to"`
					Result struct {
						Text  string `json:"text"`
						Color string `json:"color"`
						Index int    `json:"index"`
					} `json:"result"`
				}
				if json.Unmarshal(m.Options, &opt) == nil {
					dm.From, dm.To = opt.From, opt.To
					dm.Text = opt.Result.Text
					dm.Color = mapGrafanaColor(opt.Result.Color)
					dm.Index = opt.Result.Index
					out = append(out, dm)
					continue
				}
			case "regex":
				var opt struct {
					Pattern string `json:"pattern"`
					Result  struct {
						Text  string `json:"text"`
						Color string `json:"color"`
						Index int    `json:"index"`
					} `json:"result"`
				}
				if json.Unmarshal(m.Options, &opt) == nil {
					dm.Pattern = opt.Pattern
					dm.Text = opt.Result.Text
					dm.Color = mapGrafanaColor(opt.Result.Color)
					dm.Index = opt.Result.Index
					out = append(out, dm)
					continue
				}
			case "special":
				var opt struct {
					Match  string `json:"match"`
					Result struct {
						Text  string `json:"text"`
						Color string `json:"color"`
						Index int    `json:"index"`
					} `json:"result"`
				}
				if json.Unmarshal(m.Options, &opt) == nil {
					dm.Special = opt.Match
					dm.Text = opt.Result.Text
					dm.Color = mapGrafanaColor(opt.Result.Color)
					dm.Index = opt.Result.Index
					out = append(out, dm)
					continue
				}
			}
		}
		// Legacy flat
		dm.Text = m.Text
		dm.From, dm.To = m.From, m.To
		dm.Pattern = m.Pattern
		if m.Value != nil {
			dm.Value = fmt.Sprint(m.Value)
		}
		out = append(out, dm)
	}
	return out
}

func mapGrafanaOverride(ov grafanaFieldOverride) DashFieldOverride {
	out := DashFieldOverride{MatcherID: strings.TrimSpace(ov.Matcher.ID)}
	if len(ov.Matcher.Options) > 0 {
		var s string
		if json.Unmarshal(ov.Matcher.Options, &s) == nil {
			out.MatcherOptions = s
		} else {
			out.MatcherOptions = strings.TrimSpace(string(ov.Matcher.Options))
		}
	}
	for _, prop := range ov.Properties {
		id := strings.TrimSpace(prop.ID)
		val := prop.Value
		switch id {
		case "unit":
			var u string
			if json.Unmarshal(val, &u) == nil {
				out.Unit = u
			}
		case "decimals":
			var d int
			if json.Unmarshal(val, &d) == nil {
				out.Decimals = &d
			}
		case "min":
			var f float64
			if json.Unmarshal(val, &f) == nil {
				out.Min = &f
			}
		case "max":
			var f float64
			if json.Unmarshal(val, &f) == nil {
				out.Max = &f
			}
		case "noValue":
			var s string
			if json.Unmarshal(val, &s) == nil {
				out.NoValue = s
			}
		case "color":
			var c struct {
				Mode       string `json:"mode"`
				FixedColor string `json:"fixedColor"`
			}
			if json.Unmarshal(val, &c) == nil {
				out.Options.ColorMode = c.Mode
				out.Options.Palette = mapGrafanaColorMode(c.Mode)
				if fc := mapGrafanaColor(c.FixedColor); fc != "" {
					out.Options.Colors = []string{fc}
				}
			}
		case "thresholds":
			var th struct {
				Mode  string `json:"mode"`
				Steps []struct {
					Value *float64 `json:"value"`
					Color string   `json:"color"`
				} `json:"steps"`
			}
			if json.Unmarshal(val, &th) == nil {
				if strings.EqualFold(th.Mode, "percentage") {
					out.Options.ThresholdMode = "percentage"
				} else if th.Mode != "" {
					out.Options.ThresholdMode = "absolute"
				}
				for _, s := range th.Steps {
					if s.Value == nil {
						continue
					}
					if c := mapGrafanaColor(s.Color); c != "" {
						out.Options.Thresholds = append(out.Options.Thresholds, DashThreshold{Value: *s.Value, Color: c})
					}
				}
			}
		case "mappings":
			var maps []grafanaValueMapping
			if json.Unmarshal(val, &maps) == nil {
				out.Options.Mappings = mapGrafanaMappings(maps)
			}
		case "custom.drawStyle", "custom.lineWidth", "custom.fillOpacity",
			"custom.gradientMode", "custom.showPoints", "custom.pointSize",
			"custom.spanNulls", "custom.stacking", "custom.axisPlacement",
			"custom.lineInterpolation":
			applyGrafanaOverrideCustom(&out.Options, id, val)
		}
	}
	return out
}

func applyGrafanaOverrideCustom(o *DashPanelOptions, id string, val json.RawMessage) {
	if o == nil {
		return
	}
	switch id {
	case "custom.drawStyle":
		var s string
		if json.Unmarshal(val, &s) == nil {
			applyGrafanaCustom(o, grafanaFieldCustom{DrawStyle: s})
		}
	case "custom.lineWidth":
		var f float64
		if json.Unmarshal(val, &f) == nil {
			o.LineWidth = &f
		}
	case "custom.fillOpacity":
		var f float64
		if json.Unmarshal(val, &f) == nil {
			o.FillOpacity = &f
			if f > 0 && (o.ChartStyle == "" || o.ChartStyle == "line") {
				o.ChartStyle = "area"
			}
		}
	case "custom.gradientMode":
		var s string
		if json.Unmarshal(val, &s) == nil {
			o.GradientMode = strings.ToLower(s)
		}
	case "custom.showPoints":
		var s string
		if json.Unmarshal(val, &s) == nil && strings.EqualFold(s, "always") {
			o.ShowPoints = true
		}
	case "custom.pointSize":
		var f float64
		if json.Unmarshal(val, &f) == nil {
			o.PointSize = &f
		}
	case "custom.spanNulls":
		var b bool
		if json.Unmarshal(val, &b) == nil {
			o.SpanNulls = b
		}
	case "custom.stacking":
		var st struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal(val, &st) == nil && (st.Mode == "normal" || st.Mode == "percent") {
			o.Stacked = true
		}
	case "custom.axisPlacement":
		var s string
		if json.Unmarshal(val, &s) == nil {
			o.AxisPlacement = strings.ToLower(s)
		}
	case "custom.lineInterpolation":
		var s string
		if json.Unmarshal(val, &s) == nil && strings.EqualFold(s, "smooth") {
			o.Smooth = true
		}
	}
}

func mapGrafanaColor(c string) string {
	c = strings.TrimSpace(c)
	switch strings.ToLower(c) {
	case "green", "semi-dark-green", "dark-green", "super-light-green":
		return "var(--ok)"
	case "yellow", "orange", "semi-dark-orange", "dark-orange":
		return "var(--warn)"
	case "red", "dark-red", "semi-dark-red", "super-light-red":
		return "var(--crit)"
	case "blue", "semi-dark-blue", "dark-blue", "super-light-blue", "purple":
		return "var(--accent)"
	case "text", "transparent":
		return "var(--muted)"
	}
	if strings.HasPrefix(c, "#") {
		return c
	}
	return ""
}

func mapGrafanaPanelType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "timeseries", "graph", "graph-old":
		return "timeseries"
	case "stat", "singlestat":
		return "stat"
	case "gauge":
		return "gauge"
	case "bargauge":
		return "bargauge"
	case "piechart", "grafana-piechart-panel", "piechart-old":
		return "piechart"
	case "barchart":
		return "barchart"
	case "histogram":
		return "histogram"
	case "state-timeline", "status-history":
		return "state-timeline"
	case "heatmap":
		return "heatmap"
	case "candlestick":
		return "candlestick"
	case "radar", "grafana-radar-panel":
		return "radar"
	case "nodegraph":
		return "nodegraph"
	case "xychart":
		return "unsupported"
	case "sankey", "grafana-sankey-panel":
		return "sankey"
	case "geomap":
		return "geomap"
	case "flamegraph", "grafana-flamegraph-panel":
		return "flamegraph"
	case "clock", "grafana-clock-panel":
		return "clock"
	case "news", "grafana-news-panel":
		return "news"
	case "alertlist":
		return "alertlist"
	case "logs":
		return "logs"
	case "table", "table-old":
		return "table"
	case "text", "markdown":
		return "text"
	default:
		return "unsupported"
	}
}

// rawQueryString 把变量 query（string 或 {query:...} 对象）取成字符串。
func rawQueryString(r json.RawMessage) string {
	if len(r) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	var obj struct {
		Query string `json:"query"`
	}
	if json.Unmarshal(r, &obj) == nil {
		return obj.Query
	}
	return ""
}

// rawCurrentValue 把变量 current.value（string 或 []string）取成字符串（多值用逗号连接）。
func rawCurrentValue(r json.RawMessage) string {
	if len(r) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	var arr []string
	if json.Unmarshal(r, &arr) == nil {
		return strings.Join(arr, ",")
	}
	return ""
}
