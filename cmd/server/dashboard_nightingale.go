package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ============================================================================
// 导入兼容看板 JSON（务实子集）。
//
// 导出形态：{name, tags, configs}，configs 为 JSON（字符串或对象），内含
// {version, var[], panels[]}。面板用 react-grid-layout 的 layout{x,y,w,h}（同 24 栏），
// 变量用 var[]（definition=label_values(...) 或 custom 逗号候选），单位在
// options.standardOptions.util。映射策略与 Grafana 导入一致：已知类型直接映射，
// 未知类型保留为 unsupported 占位（带原始查询），不静默丢弃。
// ============================================================================

type n9eExport struct {
	Name    string          `json:"name"`
	Tags    string          `json:"tags"`
	Configs json.RawMessage `json:"configs"` // 字符串（JSON 编码）或对象
}

type n9eConfigs struct {
	Var    []n9eVar   `json:"var"`
	Panels []n9ePanel `json:"panels"`
}

type n9eVar struct {
	Type       string `json:"type"` // query | custom | constant | textbox | datasource
	Name       string `json:"name"`
	Definition string `json:"definition"` // query: label_values(...); custom: "a,b,c"
	Reg        string `json:"reg"`
	Multi      bool   `json:"multi"`
	AllOption  bool   `json:"allOption"`
}

type n9ePanel struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Layout struct {
		H int `json:"h"`
		W int `json:"w"`
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"layout"`
	Targets []struct {
		Expr   string `json:"expr"`
		Legend string `json:"legend"`
	} `json:"targets"`
	Options struct {
		StandardOptions struct {
			Util string `json:"util"`
		} `json:"standardOptions"`
		Content string `json:"content"`
	} `json:"options"`
}

// mapNightingaleDashboard 把兼容看板 JSON 映射为内部 Dashboard。
func mapNightingaleDashboard(raw []byte, nameOverride, source string) (Dashboard, error) {
	var exp n9eExport
	if err := json.Unmarshal(raw, &exp); err != nil {
		return Dashboard{}, fmt.Errorf("解析兼容看板 JSON 失败：%v", err)
	}

	// configs 可能是字符串或对象；也可能整份就是 configs（无 name/tags 包裹）。
	var cfg n9eConfigs
	switch {
	case len(exp.Configs) > 0:
		if !unmarshalMaybeString(exp.Configs, &cfg) {
			return Dashboard{}, fmt.Errorf("解析兼容看板 configs 失败")
		}
	default:
		// 顶层直接是 configs（含 panels/var）
		_ = json.Unmarshal(raw, &cfg)
	}
	if len(cfg.Panels) == 0 {
		return Dashboard{}, fmt.Errorf("未从该兼容看板解析到任何面板")
	}

	d := Dashboard{Source: source}
	d.Name = strings.TrimSpace(nameOverride)
	if d.Name == "" {
		d.Name = strings.TrimSpace(exp.Name)
	}
	if d.Name == "" {
		d.Name = "导入的兼容看板"
	}
	d.Tags = append(d.Tags, splitN9eTags(exp.Tags)...)

	for _, gv := range cfg.Var {
		if dv, ok := mapN9eVar(gv); ok {
			d.Vars = append(d.Vars, dv)
		}
	}

	nextID := 1
	for _, gp := range cfg.Panels {
		if gp.Type == "row" {
			continue // row 仅作分组标题，不渲染为面板
		}
		p := mapN9ePanel(gp)
		p.ID = nextID
		nextID++
		d.Panels = append(d.Panels, p)
	}
	if len(d.Panels) == 0 {
		return Dashboard{}, fmt.Errorf("未从该兼容看板解析到可渲染面板")
	}
	sortPanels(d.Panels)
	healImportedDashboard(&d)
	return d, nil
}

func mapN9eVar(gv n9eVar) (DashVar, bool) {
	if strings.TrimSpace(gv.Name) == "" {
		return DashVar{}, false
	}
	switch gv.Type {
	case "query":
		return DashVar{Name: gv.Name, Type: "query", Query: gv.Definition, Multi: gv.Multi, IncludeAll: gv.AllOption}, true
	case "custom":
		var opts []string
		for _, o := range strings.Split(gv.Definition, ",") {
			if o = strings.TrimSpace(o); o != "" {
				opts = append(opts, o)
			}
		}
		return DashVar{Name: gv.Name, Type: "custom", Options: opts, Multi: gv.Multi, IncludeAll: gv.AllOption}, true
	case "constant", "textbox":
		return DashVar{Name: gv.Name, Type: gv.Type, Current: gv.Definition}, true
	default:
		return DashVar{}, false // datasource 等跳过
	}
}

func mapN9ePanel(gp n9ePanel) DashPanel {
	p := DashPanel{
		Title: gp.Name,
		Grid:  DashGrid{X: gp.Layout.X, Y: gp.Layout.Y, W: gp.Layout.W, H: gp.Layout.H},
		Unit:  mapN9eUnit(gp.Options.StandardOptions.Util),
		Text:  gp.Options.Content,
	}
	if p.Grid.W == 0 {
		p.Grid.W = 12
	}
	if p.Grid.H == 0 {
		p.Grid.H = 7
	}
	for _, t := range gp.Targets {
		if strings.TrimSpace(t.Expr) == "" {
			continue
		}
		if len(p.Targets) >= maxDashboardTargets {
			break
		}
		p.Targets = append(p.Targets, DashTarget{Expr: t.Expr, Legend: t.Legend})
	}
	p.Type = mapN9ePanelType(gp.Type)
	if p.Type == "unsupported" {
		p.RawType = gp.Type
	} else if p.Type != "text" && p.Type != "alertlist" && len(p.Targets) == 0 {
		p.RawType = gp.Type
		p.Type = "unsupported"
	}
	return p
}

func mapN9ePanelType(t string) string {
	switch t {
	case "timeseries":
		return "timeseries"
	case "stat":
		return "stat"
	case "gauge":
		return "gauge"
	case "barGauge", "bargauge":
		return "bargauge"
	case "pie":
		return "piechart"
	case "barchart":
		return "barchart"
	case "heatmap":
		return "heatmap"
	case "table":
		return "table"
	case "text":
		return "text"
	default: // hexbin / heatmap / iframe / ...
		return "unsupported"
	}
}

// mapN9eUnit 把 standardOptions.util 单位映射到内部单位串。
func mapN9eUnit(u string) string {
	switch u {
	case "percent":
		return "percent"
	case "percentUnit":
		return "percentunit"
	case "bytesIEC", "bytesSI", "bytes":
		return "bytes"
	case "bitsPerSecondIEC", "bitsPerSecondSI", "bytesPerSecondIEC", "bytesPerSecondSI", "Bps":
		return "Bps"
	case "seconds", "s":
		return "s"
	case "milliseconds", "ms":
		return "ms"
	case "cps", "ops", "reqps", "rps", "qps", "iops":
		return "reqps"
	default:
		return ""
	}
}

// unmarshalMaybeString 支持字段是「JSON 字符串」或「JSON 对象」两种形态。
func unmarshalMaybeString(raw json.RawMessage, v any) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil { // 是字符串 → 再解其内容
		return json.Unmarshal([]byte(s), v) == nil
	}
	return json.Unmarshal(raw, v) == nil // 是对象
}

func splitN9eTags(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	var out []string
	for _, t := range f {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// detectTemplateFormat 自动识别粘贴/上传的模板：AIOps 原生 / Grafana / 兼容看板。
func detectTemplateFormat(raw []byte) string {
	var probe struct {
		Format     string                       `json:"format"`
		Configs    json.RawMessage              `json:"configs"`
		Var        json.RawMessage              `json:"var"`
		Templating json.RawMessage              `json:"templating"`
		Dashboard  json.RawMessage              `json:"dashboard"`
		SchemaVer  json.RawMessage              `json:"schemaVersion"`
		Panels     []map[string]json.RawMessage `json:"panels"`
		Name       string                       `json:"name"`
		ID         string                       `json:"id"`
	}
	_ = json.Unmarshal(raw, &probe)
	if strings.EqualFold(strings.TrimSpace(probe.Format), "aiops") {
		return "aiops"
	}
	// 原生导出：顶层有 panels[].grid + targets[].expr，且无 Grafana gridPos / 兼容格式 layout
	if len(probe.Panels) > 0 && (probe.Name != "" || probe.ID != "") {
		hasGrid, hasGridPos, hasLayout := false, false, false
		for _, p := range probe.Panels {
			if _, ok := p["grid"]; ok {
				hasGrid = true
			}
			if _, ok := p["gridPos"]; ok {
				hasGridPos = true
			}
			if _, ok := p["layout"]; ok {
				hasLayout = true
			}
		}
		if hasGrid && !hasGridPos && !hasLayout {
			return "aiops"
		}
	}
	if len(probe.Configs) > 0 || len(probe.Var) > 0 {
		return "nightingale"
	}
	if len(probe.Templating) > 0 || len(probe.Dashboard) > 0 || len(probe.SchemaVer) > 0 {
		return "grafana"
	}
	for _, p := range probe.Panels {
		if _, ok := p["layout"]; ok {
			return "nightingale"
		}
		if _, ok := p["gridPos"]; ok {
			return "grafana"
		}
	}
	return "grafana"
}

// mapAIOpsDashboard 导入本平台原生看板模板（导出 JSON 可原样回灌）。
func mapAIOpsDashboard(raw []byte, name, source string) (Dashboard, error) {
	var wrap struct {
		Format    string          `json:"format"`
		Dashboard json.RawMessage `json:"dashboard"`
	}
	_ = json.Unmarshal(raw, &wrap)
	payload := raw
	if len(wrap.Dashboard) > 0 && strings.EqualFold(strings.TrimSpace(wrap.Format), "aiops") {
		payload = wrap.Dashboard
	}
	var d Dashboard
	if err := json.Unmarshal(payload, &d); err != nil {
		return Dashboard{}, fmt.Errorf("解析 AIOps 看板模板失败：%v", err)
	}
	if strings.TrimSpace(name) != "" {
		d.Name = strings.TrimSpace(name)
	}
	if strings.TrimSpace(d.Name) == "" {
		return Dashboard{}, fmt.Errorf("看板模板缺少名称")
	}
	d.ID = "" // 作为新看板导入
	d.Revision = 0
	d.CreatedAt = 0
	d.UpdatedAt = 0
	if source == "" {
		source = "aiops-template"
	}
	d.Source = source
	// 模板跨环境不携带图片 URL（死链）；保留配色 / fit / 透明度。
	d.Appearance.LogoURL = ""
	d.Appearance.BackgroundURL = ""
	if err := normalizeDashboard(&d); err != nil {
		return Dashboard{}, err
	}
	return d, nil
}
