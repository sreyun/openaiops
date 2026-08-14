package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestExtractJSONObject(t *testing.T) {
	cases := []struct{ in, want string }{
		{"前言\n```json\n{\"a\":1}\n```\n后语", `{"a":1}`},
		{"直接 {\"b\":2} 结束", `{"b":2}`},
		{"```\n{\"c\":3}\n```", `{"c":3}`},
		{"没有任何 JSON", ""},
		// 流式截断：缺收尾 ```
		{"要点\n```json\n{\"panels\":[{\"title\":\"A\"}]}", `{"panels":[{"title":"A"}]}`},
	}
	for _, c := range cases {
		if got := extractJSONObject(c.in); got != c.want {
			t.Fatalf("extractJSONObject(%q)=%q，应为 %q", c.in, got, c.want)
		}
	}
	// 散文里的花括号不应抢走含 panels 的对象
	noisy := `可用写法 {expr} 示例。\n` + "```json\n" + `{"name":"n","panels":[{"title":"A","type":"stat","targets":[{"expr":"up"}]}]}` + "\n```"
	got := extractJSONObject(noisy)
	if !strings.Contains(got, `"panels"`) || !strings.Contains(got, `"name"`) {
		t.Fatalf("应优先抽取含 panels 的对象，实为 %q", got)
	}
}

func TestDecodeAIDashSpecRepairsLLMJunk(t *testing.T) {
	// 尾逗号（对象内 + 数组后）
	raw := "```json\n{\n" +
		`  "name": "脏JSON",` + "\n" +
		`  "panels": [` + "\n" +
		`    {"title": "CPU", "type": "stat", "targets": [{"expr": "aiops_cpu_percent"}],},` + "\n" +
		`  ],` + "\n" +
		`}` + "\n```"
	spec, ok := decodeAIDashSpec(raw)
	if !ok {
		t.Fatal("尾逗号脏 JSON 应能解码")
	}
	if len(spec.Panels) != 1 || spec.Panels[0].Title != "CPU" {
		t.Fatalf("panels 异常: %+v", spec.Panels)
	}
	// 弯引号键/值：repair 会把弯引号统一成 "
	curly := "```json\n{\n  \u201cname\u201d: \u201cy\u201d,\n  \u201cpanels\u201d: [{\u201ctitle\u201d:\u201cT\u201d,\u201ctype\u201d:\u201cstat\u201d,\u201ctargets\u201d:[{\u201cexpr\u201d:\u201cup\u201d}]}]\n}\n```"
	spec2, ok2 := decodeAIDashSpec(curly)
	if !ok2 || spec2.specName() != "y" || len(spec2.Panels) != 1 {
		t.Fatalf("弯引号 JSON 应能解码: ok=%v spec=%+v", ok2, spec2)
	}
}

func TestBuiltinAIDashFallback(t *testing.T) {
	spec, ok := builtinAIDashFallback("构建主机黄金信号看板：顶部展示在线数、CPU、内存")
	if !ok || len(spec.Panels) < 8 {
		t.Fatalf("黄金信号兜底应有足够面板: ok=%v n=%d", ok, len(spec.Panels))
	}
	d, warns := sanitizeAIDash(spec, "", "ai")
	if len(d.Panels) < 8 {
		t.Fatalf("sanitize 后面板过少: %d warns=%v", len(d.Panels), warns)
	}
	if panelsGridOverlap(d.Panels) {
		t.Fatal("内置模板布局不应重叠")
	}
	// 非明确主题不得误兜底，避免「随便描述」生成错误主机看板
	if _, ok := builtinAIDashFallback("给我一个好看的运维看板"); ok {
		t.Fatal("笼统需求不应命中内置兜底")
	}
}

func TestRepairTruncatedDashJSON(t *testing.T) {
	// 第二个 panel 被截断
	js := `{"name":"t","panels":[{"title":"A","type":"stat","targets":[{"expr":"up"}]},{"title":"B","type":"stat","targets":[{"expr":"mem`
	got := repairTruncatedDashJSON(js)
	var spec aiDashSpec
	if err := json.Unmarshal([]byte(got), &spec); err != nil {
		t.Fatalf("修复后应可解析: %v\n%s", err, got)
	}
	if len(spec.Panels) != 1 || spec.Panels[0].Title != "A" {
		t.Fatalf("应保留完整面板 A，实为 %+v", spec.Panels)
	}
}

func TestDecodeAIDashSpecTruncatedFence(t *testing.T) {
	raw := "优化建议：\n```json\n{\"name\":\"x\",\"panels\":[{\"title\":\"CPU\",\"type\":\"stat\",\"targets\":[{\"expr\":\"up\"}]},{\"title\":\"Bad\",\"type\":\"stat\",\"targets\":[{\"expr\":\"mem"
	spec, ok := decodeAIDashSpec(raw)
	if !ok {
		t.Fatal("截断 JSON 应能部分解码")
	}
	if len(spec.Panels) < 1 {
		t.Fatalf("至少应有 1 个完整面板，实为 %d", len(spec.Panels))
	}
}

func TestSanitizeAIDash(t *testing.T) {
	raw := `{
      "name": "t",
      "vars": [{"name":"instance","type":"weird"}],
      "panels": [
        {"title":"A","type":"timeseries","w":12,"h":8,"targets":[{"expr":"up"}]},
        {"title":"B","type":"foobar","w":12,"h":8,"targets":[{"expr":"rate(x[5m])","legend":"{{job}}"}]},
        {"title":"C","type":"stat","w":6,"h":4,"targets":[{"expr":"  "}]},
        {"title":"D","type":"text","w":24,"h":3,"text":"hi"},
        {"title":"E","type":"timeseries","w":18,"h":8,"targets":[{"expr":"y"}]}
      ]
    }`
	var spec aiDashSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	d, warns := sanitizeAIDash(spec, "", "ai")
	if len(d.Panels) != 4 {
		t.Fatalf("应保留 4 个面板（C 无有效查询被跳过），实为 %d", len(d.Panels))
	}
	if len(warns) != 1 {
		t.Fatalf("应有 1 条警告（C 被跳过），实为 %d", len(warns))
	}
	if d.Vars[0].Type != "query" {
		t.Fatalf("未知变量类型应回退 query，实为 %q", d.Vars[0].Type)
	}
	by := map[string]DashPanel{}
	for _, p := range d.Panels {
		by[p.Title] = p
	}
	if by["B"].Type != "timeseries" {
		t.Fatalf("未知类型(foobar)应回退 timeseries，实为 %q", by["B"].Type)
	}
	if by["D"].Type != "text" {
		t.Fatalf("text 面板应保留，实为 %q", by["D"].Type)
	}
	// 分区布局：timeseries (A/B/E) 在前，text (D) 在后；A/B 同行 y=0，E 换行，D 更后。
	if by["A"].Grid.X != 0 || by["A"].Grid.Y != 0 {
		t.Fatalf("A 应在 (0,0)，实为 (%d,%d)", by["A"].Grid.X, by["A"].Grid.Y)
	}
	if by["B"].Grid.X != 12 || by["B"].Grid.Y != 0 {
		t.Fatalf("B 应在 (12,0)，实为 (%d,%d)", by["B"].Grid.X, by["B"].Grid.Y)
	}
	if by["E"].Grid.Y != by["A"].Grid.H {
		t.Fatalf("E 应在 timeseries 首行下方 y=%d，实为 y=%d", by["A"].Grid.H, by["E"].Grid.Y)
	}
	if by["D"].Grid.Y < by["E"].Grid.Y {
		t.Fatalf("text 面板 D 应排在趋势区之后，D.y=%d E.y=%d", by["D"].Grid.Y, by["E"].Grid.Y)
	}
	if panelsGridOverlap(d.Panels) {
		t.Fatal("sanitize 布局不应重叠")
	}
}

// TestDecodeAIDashSpecGrafanaAliases locks in the "应用优化后看板为空" fix: LLMs often
// emit Grafana-native JSON (outer {"dashboard":{...}}, title instead of name, target
// query/legendFormat instead of expr/legend, gridPos instead of w/h). These must still
// produce populated panels rather than an empty dashboard.
func TestDecodeAIDashSpecGrafanaAliases(t *testing.T) {
	raw := "```json\n" + `{
      "dashboard": {
        "title": "Grafana 风格看板",
        "panels": [
          {"title":"CPU","type":"timeseries","gridPos":{"w":12,"h":8},
           "targets":[{"query":"rate(cpu[5m])","legendFormat":"{{instance}}"}]},
          {"title":"Mem","type":"stat","w":6,"h":4,
           "targets":[{"expr":"mem_used"}]}
        ]
      }
    }` + "\n```"
	spec, ok := decodeAIDashSpec(raw)
	if !ok {
		t.Fatal("decodeAIDashSpec 应能解析 Grafana 包裹格式")
	}
	if spec.specName() != "Grafana 风格看板" {
		t.Fatalf("title 别名应作为看板名，实为 %q", spec.specName())
	}
	d, _ := sanitizeAIDash(spec, "", "ai")
	if len(d.Panels) != 2 {
		t.Fatalf("应解析出 2 个面板（不再为空），实为 %d", len(d.Panels))
	}
	by := map[string]DashPanel{}
	for _, p := range d.Panels {
		by[p.Title] = p
	}
	cpu := by["CPU"]
	if len(cpu.Targets) != 1 || cpu.Targets[0].Expr != "rate(cpu[5m])" {
		t.Fatalf("query 别名应映射为 expr，实为 %+v", cpu.Targets)
	}
	if cpu.Targets[0].Legend != "{{instance}}" {
		t.Fatalf("legendFormat 别名应映射为 legend，实为 %q", cpu.Targets[0].Legend)
	}
	// sanitize 会按分区重排并铺满 24 栏：单独 timeseries 行宽为 24（不再保留 gridPos 原值）。
	// 此处锁定「别名可解析且面板非空」；宽度由 layoutAIDashPanels 决定。
	if cpu.Grid.W < 8 || cpu.Grid.W > 24 {
		t.Fatalf("CPU 面板宽度异常：%d", cpu.Grid.W)
	}
	if by["Mem"].Type != "stat" {
		t.Fatalf("Mem 应为 stat，实为 %q", by["Mem"].Type)
	}
	// 仍验证 gridPos 在装箱前被正确读入：仅含 gridPos、无顶层 w 时不应被当成 0 宽丢弃。
	if len(spec.Panels) < 1 || spec.Panels[0].GridPos.W != 12 {
		t.Fatalf("decode 后 gridPos.w 应为 12，实为 %+v", spec.Panels)
	}
}

func TestDecodeAIDashSpecLenientOptionsAndRows(t *testing.T) {
	raw := "```json\n" + `{
      "name": "宽松 options",
      "panels": [
        {"type":"row","title":"组","panels":[
          {"title":"CPU","type":"timeseries","w":12,"h":8,
           "options":{"legend":{"placement":"bottom","showLegend":true},"limit":"10",
             "thresholds":{"mode":"absolute","steps":[{"value":null,"color":"green"},{"value":90,"color":"red"}]},
             "palette":"neon-dream"},
           "targets":[{"expr":"aiops_cpu_percent","legend":"{{instance}}"}]}
        ]}
      ]
    }` + "\n```"
	spec, ok := decodeAIDashSpec(raw)
	if !ok {
		t.Fatal("decode should succeed with Grafana legend object + nested row")
	}
	if len(spec.Panels) != 1 {
		t.Fatalf("row should flatten to 1 panel, got %d", len(spec.Panels))
	}
	o := spec.Panels[0].Options
	if o.Legend != "bottom" {
		t.Fatalf("legend placement → %q", o.Legend)
	}
	if o.Limit != 10 {
		t.Fatalf("string limit → %d", o.Limit)
	}
	if len(o.Thresholds) < 1 {
		t.Fatalf("thresholds.steps should map, got %+v", o.Thresholds)
	}
	d, _ := sanitizeAIDash(spec, "", "ai")
	if err := normalizeDashboard(&d); err != nil {
		t.Fatalf("normalize after soft palette clear should pass: %v", err)
	}
	if d.Panels[0].Options.Palette != "" {
		t.Fatalf("unknown palette should soft-clear, got %q", d.Panels[0].Options.Palette)
	}
	if len(d.Panels[0].Options.Thresholds) != 0 {
		t.Fatalf("AI sanitize must strip thresholds by default, got %+v", d.Panels[0].Options.Thresholds)
	}
}

func TestTokenize(t *testing.T) {
	got := tokenize("MySQL 连接数 qps_total rate() a")
	// 期望：mysql, qps_total, rate（"a" 单字符被丢，CJK 被分隔）
	want := map[string]bool{"mysql": true, "qps_total": true, "rate": true}
	if len(got) != 3 {
		t.Fatalf("分词数应为 3，实为 %d：%v", len(got), got)
	}
	for _, tok := range got {
		if !want[tok] {
			t.Fatalf("意外的词元 %q（全部：%v）", tok, got)
		}
	}
}

func TestSanitizeAIDashNormalizesChineseVarAndHealsExpr(t *testing.T) {
	spec := aiDashSpec{
		Name: "t",
		Vars: []aiDashVar{{Name: "实例", Type: "query", Query: "label_values(node_uname_info, instance)"}},
		Panels: []aiDashPanel{
			{Title: "CPU趋势", Type: "timeseries", W: 12, H: 12, Targets: []aiDashTarget{
				{Expr: `rate(node_load5{instance="$实例"}[5m])`},
			}},
			{Title: "CPU均值", Type: "stat", W: 6, H: 4, Targets: []aiDashTarget{
				{Expr: "avg(aiops_cpu_percent)"},
			}},
		},
	}
	d, warns := sanitizeAIDash(spec, "", "ai")
	if len(d.Vars) != 1 || d.Vars[0].Name != "instance" {
		t.Fatalf("变量应规范为 instance: %+v warns=%v", d.Vars, warns)
	}
	if !strings.Contains(d.Vars[0].Query, "aiops_cpu_percent") {
		t.Fatalf("变量查询应改为平台指标: %q", d.Vars[0].Query)
	}
	var trend DashPanel
	for _, p := range d.Panels {
		if p.Title == "CPU趋势" {
			trend = p
		}
	}
	if trend.Title == "" {
		t.Fatal("缺少趋势面板")
	}
	expr := trend.Targets[0].Expr
	if strings.Contains(expr, "node_load") || strings.Contains(expr, "rate(") || strings.Contains(expr, "$实例") {
		t.Fatalf("趋势表达式应被治愈: %q", expr)
	}
	if !strings.Contains(expr, "aiops_load5") || !strings.Contains(expr, "$instance") {
		t.Fatalf("趋势表达式应含 aiops_load5 与 $instance: %q", expr)
	}
	if !strings.Contains(expr, `instance=~"$instance"`) {
		t.Fatalf("趋势表达式应使用 =~ 过滤: %q", expr)
	}
	if !d.Vars[0].IncludeAll {
		t.Fatal("instance 变量应默认 IncludeAll")
	}
	if trend.Grid.H < 5 || trend.Grid.H > 10 {
		t.Fatalf("timeseries 高度应在 5~10，实为 %d", trend.Grid.H)
	}
}

func TestAIPanelHeightStatFitsContent(t *testing.T) {
	// 紧凑 KPI：缺省/过矮/过高均钳到 4；合法 3~5 保留
	if got := aiPanelHeight("stat", 0); got != 4 {
		t.Fatalf("缺省 stat 高度应为 4，实为 %d", got)
	}
	if got := aiPanelHeight("stat", 2); got != 4 {
		t.Fatalf("过矮 stat 应抬到 4，实为 %d", got)
	}
	if got := aiPanelHeight("stat", 4); got != 4 {
		t.Fatalf("合法 h=4 应保留，实为 %d", got)
	}
	if got := aiPanelHeight("stat", 3); got != 3 {
		t.Fatalf("合法 h=3 应保留，实为 %d", got)
	}
	if got := aiPanelHeight("stat", 10); got != 4 {
		t.Fatalf("过高 stat 应钳回 4，实为 %d", got)
	}
	if got := aiPanelHeight("gauge", 0); got != 5 {
		t.Fatalf("缺省 gauge 高度应为 5，实为 %d", got)
	}
	if got := aiDashSectionRowHeight("stat"); got != 4 {
		t.Fatalf("KPI 行高应为 4，实为 %d", got)
	}
	if got := aiDashSectionRowHeight("gauge"); got != 5 {
		t.Fatalf("gauge 行高应为 5，实为 %d", got)
	}
	spec := aiDashSpec{Name: "kpi", Panels: []aiDashPanel{
		{Title: "A", Type: "stat", W: 6, H: 4, Targets: []aiDashTarget{{Expr: "aiops_cpu_percent"}}},
		{Title: "B", Type: "stat", W: 6, H: 4, Targets: []aiDashTarget{{Expr: "aiops_mem_percent"}}},
		{Title: "C", Type: "stat", W: 6, H: 3, Targets: []aiDashTarget{{Expr: "aiops_disk_percent"}}},
		{Title: "D", Type: "stat", W: 6, H: 8, Targets: []aiDashTarget{{Expr: "aiops_load1"}}},
	}}
	d, _ := sanitizeAIDash(spec, "", "ai")
	if len(d.Panels) != 4 {
		t.Fatalf("panels=%d", len(d.Panels))
	}
	for _, p := range d.Panels {
		if p.Grid.H != 4 {
			t.Fatalf("KPI「%s」高度应为 4（紧凑），实为 %d", p.Title, p.Grid.H)
		}
	}
}

func TestHealImportedAIDashboardNodeMetrics(t *testing.T) {
	d := Dashboard{
		Source: "ai",
		Panels: []DashPanel{
			{ID: 1, Title: "CPU 使用率趋势", Type: "timeseries", Grid: DashGrid{X: 0, Y: 0, W: 12, H: 7},
				Targets: []DashTarget{{Expr: `100 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100`}}},
			{ID: 2, Title: "网络吞吐 (收/发)", Type: "timeseries", Grid: DashGrid{X: 12, Y: 0, W: 12, H: 7},
				Targets: []DashTarget{
					{Expr: `rate(node_network_receive_bytes_total{instance=~"$instance"}[5m])`},
					{Expr: `rate(node_network_transmit_bytes_total{instance=~"$instance"}[5m])`},
				}},
		},
	}
	if !healImportedDashboard(&d) {
		t.Fatal("AI 看板 node_* 应被纠偏")
	}
	if got := d.Panels[0].Targets[0].Expr; got != "aiops_cpu_percent" {
		t.Fatalf("CPU 面板: %q", got)
	}
	if got := d.Panels[1].Targets[0].Expr; got != `aiops_net_recv_rate{instance=~"$instance"}` {
		t.Fatalf("网络接收: %q", got)
	}
	if got := d.Panels[1].Targets[1].Expr; got != `aiops_net_sent_rate{instance=~"$instance"}` {
		t.Fatalf("网络发送: %q", got)
	}
}

func TestHealPanelQueryExprBuiltinOnly(t *testing.T) {
	in := `rate(node_network_receive_bytes_total[5m])`
	if got := healPanelQueryExpr("", in); got != "aiops_net_recv_rate" {
		t.Fatalf("内置 DS 应纠偏: %q", got)
	}
	if got := healPanelQueryExpr("prom-1", in); got != in {
		t.Fatalf("外部 DS 不应改写: %q", got)
	}
}

func TestHealAIDashExpr(t *testing.T) {
	if got := healAIDashExpr(`rate(aiops_cpu_percent[5m])`); got != "aiops_cpu_percent" {
		t.Fatalf("gauge rate 应剥离: %q", got)
	}
	if got := healAIDashExpr(`rate(aiops_load5{instance="$instance"}[5m])`); got != `aiops_load5{instance=~"$instance"}` {
		t.Fatalf("带标签的 gauge rate 应剥离并提升 =~: %q", got)
	}
	if got := healAIDashExpr(`aiops_cpu_percent{instance="$instance"}`); got != `aiops_cpu_percent{instance=~"$instance"}` {
		t.Fatalf("等值变量过滤应提升为 =~: %q", got)
	}

	cpu := `100 - (avg(rate(node_cpu_seconds_total{mode="idle",instance=~"$instance"}[5m])) * 100)`
	if got := healAIDashExpr(cpu); got != `aiops_cpu_percent{instance=~"$instance"}` {
		t.Fatalf("CPU idle 公式应纠成 aiops_cpu_percent: %q", got)
	}
	mem := `(1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes) * 100`
	if got := healAIDashExpr(mem); got != "aiops_mem_percent" {
		t.Fatalf("内存 Available 公式应纠成 aiops_mem_percent: %q", got)
	}
	net := `rate(node_network_receive_bytes_total{instance=~"$instance"}[5m])`
	if got := healAIDashExpr(net); got != `aiops_net_recv_rate{instance=~"$instance"}` {
		t.Fatalf("网络 receive rate 应纠成 aiops_net_recv_rate: %q", got)
	}
	diskIO := `rate(node_disk_io_time_seconds_total{instance=~"$instance"}[5m])`
	if got := healAIDashExprWithTitle("磁盘 IO 利用率", diskIO); got != `aiops_disk_io_util_percent{instance=~"$instance"}` {
		t.Fatalf("磁盘 IO 应纠成 aiops_disk_io_util_percent: %q", got)
	}
	if got := healAIDashExprWithTitle("网络吞吐 (收/发)", `rate(node_network_transmit_bytes_total[5m])`); got != "aiops_net_sent_rate" {
		t.Fatalf("标题+发送应纠成 aiops_net_sent_rate: %q", got)
	}
	if got := healAIDashExpr(`node_load1{instance="$instance"}`); got != `aiops_load1{instance=~"$instance"}` {
		t.Fatalf("node_load1 应纠成 aiops_load1: %q", got)
	}
}

func TestHealAIDashLegend(t *testing.T) {
	cases := map[string]string{
		"":                                       "{{instance}}",
		"{{host}}":                               "{{instance}}",
		"{{category}} - {{host}} - {{instance}}": "{{category}} · {{instance}}",
		"{{category}} · {{instance}}":            "{{category}} · {{instance}}",
		"{{instance}}":                           "{{instance}}",
	}
	for in, want := range cases {
		if got := healAIDashLegend(in); got != want {
			t.Fatalf("healAIDashLegend(%q)=%q，want %q", in, got, want)
		}
	}
	if got := healAIDashLegendFor("gauge", ""); got != "" {
		t.Fatalf("gauge 空图例应保持空，实为 %q", got)
	}
	if got := healAIDashLegendFor("stat", ""); got != "" {
		t.Fatalf("stat 空图例应保持空，实为 %q", got)
	}
	if got := healAIDashLegendFor("timeseries", ""); got != "{{instance}}" {
		t.Fatalf("timeseries 空图例应补 {{instance}}，实为 %q", got)
	}
}

func TestWithNoThinkHint(t *testing.T) {
	cfg := AIConfig{Model: "qwen3-max", Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1"}
	msgs := []map[string]string{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "生成看板"},
	}
	out := withNoThinkHint(msgs, cfg)
	if !strings.Contains(out[0]["content"], "禁止深度思考") {
		t.Fatalf("system 应注入禁止深度思考：%q", out[0]["content"])
	}
	if !strings.Contains(out[1]["content"], "/no_think") {
		t.Fatalf("user 应对 Qwen 追加 /no_think：%q", out[1]["content"])
	}
}

func TestThinkingModelOrGateway(t *testing.T) {
	if !thinkingModelOrGateway(AIConfig{Model: "qwen3-32b", Endpoint: "http://x"}) {
		t.Fatal("qwen3 应判定为思考模型")
	}
	if thinkingModelOrGateway(AIConfig{Model: "gpt-4o-mini", Endpoint: "https://api.openai.com/v1"}) {
		t.Fatal("gpt-4o-mini 不应注入 enable_thinking（避免 OpenAI 400）")
	}
}

func TestApplyThinkingKnobsNeverSendsFalse(t *testing.T) {
	cfg := AIConfig{Model: "qwen3-max", Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1"}
	body := map[string]any{}
	applyThinkingKnobs(body, cfg, aiProvOpenAI, aiCallOpts{DisableThinking: true})
	if _, ok := body["enable_thinking"]; ok {
		t.Fatalf("DisableThinking 不得写入 enable_thinking（部分网关仅允许 true）：%v", body)
	}
	body2 := map[string]any{}
	applyThinkingKnobs(body2, cfg, aiProvOpenAI, aiCallOpts{EnableThinking: true, ThinkingBudget: 512})
	if body2["enable_thinking"] != true {
		t.Fatalf("EnableThinking 应写入 true，got %v", body2["enable_thinking"])
	}
	if body2["thinking_budget"] != 512 {
		t.Fatalf("应写入 thinking_budget=512，got %v", body2["thinking_budget"])
	}
	// 未显式预算时也应有安全默认，防止思维链拖死生成
	body3 := map[string]any{}
	applyThinkingKnobs(body3, cfg, aiProvOpenAI, aiCallOpts{EnableThinking: true})
	if body3["thinking_budget"] == nil || body3["thinking_budget"].(int) <= 0 {
		t.Fatalf("EnableThinking 缺省应带 thinking_budget，got %v", body3["thinking_budget"])
	}
}

func TestThinkingParamForcedTrueError(t *testing.T) {
	err := fmt.Errorf(`HTTP 400：请求参数错误 — {"error":{"message":"The value of the enable_thinking parameter is restricted to True."}}`)
	if !thinkingParamForcedTrueError(err) {
		t.Fatal("应识别 enable_thinking restricted to True")
	}
	if thinkingParamForcedTrueError(fmt.Errorf("timeout")) {
		t.Fatal("无关错误不应匹配")
	}
}

func TestDiffDashboardsForHumanReview(t *testing.T) {
	before := Dashboard{Panels: []DashPanel{
		{ID: 1, Title: "CPU", Type: "timeseries", Grid: DashGrid{W: 12, H: 7}, Targets: []DashTarget{{Expr: "cpu_old"}}},
		{ID: 2, Title: "旧面板", Type: "stat", Grid: DashGrid{W: 6, H: 4}, Targets: []DashTarget{{Expr: "old"}}},
		{ID: 3, Title: "保持", Type: "stat", Grid: DashGrid{W: 6, H: 4}, Targets: []DashTarget{{Expr: "same"}}},
	}}
	after := Dashboard{Panels: []DashPanel{
		{ID: 10, Title: "CPU", Type: "timeseries", Grid: DashGrid{W: 12, H: 7}, Targets: []DashTarget{{Expr: "cpu_new"}}},
		{ID: 11, Title: "保持", Type: "stat", Grid: DashGrid{W: 6, H: 4}, Targets: []DashTarget{{Expr: "same"}}},
		{ID: 12, Title: "新增", Type: "gauge", Grid: DashGrid{W: 8, H: 6}, Targets: []DashTarget{{Expr: "new"}}},
	}}
	got := diffDashboards(before, after)
	if got.Before != 3 || got.After != 3 || got.Unchanged != 1 {
		t.Fatalf("摘要错误: %+v", got)
	}
	if len(got.Added) != 1 || got.Added[0] != "新增" {
		t.Fatalf("新增错误: %+v", got.Added)
	}
	if len(got.Removed) != 1 || got.Removed[0] != "旧面板" {
		t.Fatalf("删除错误: %+v", got.Removed)
	}
	if len(got.Changed) != 1 || got.Changed[0] != "CPU" {
		t.Fatalf("调整错误: %+v", got.Changed)
	}
}

func TestSanitizeAIDashKeepsLogsExprAndDatasource(t *testing.T) {
	raw := `{
      "name": "logs",
      "panels": [
        {"title":"Nginx 错误","type":"logs","datasource":{"type":"loki","uid":"loki-1"},
         "targets":[{"expr":"{job=\"nginx\"} |= \"error\""}]},
        {"title":"CPU","type":"stat","targets":[{"expr":"{__name__=~\"aiops_.*\"}"}]}
      ]
    }`
	var spec aiDashSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	d, warns := sanitizeAIDash(spec, "", "ai")
	if len(d.Panels) != 2 {
		t.Fatalf("panels=%d warns=%v", len(d.Panels), warns)
	}
	by := map[string]DashPanel{}
	for _, p := range d.Panels {
		by[p.Title] = p
	}
	logs := by["Nginx 错误"]
	if logs.Type != "logs" {
		t.Fatalf("type=%q", logs.Type)
	}
	if logs.DataSource != "loki-1" {
		t.Fatalf("logs datasource=%q, want loki-1", logs.DataSource)
	}
	if got := logs.Targets[0].Expr; got != `{job="nginx"} |= "error"` {
		t.Fatalf("LogQL 被改写了: %q", got)
	}
	if got := by["CPU"].Targets[0].Expr; got != "aiops_cpu_percent" {
		t.Fatalf("无界选择器应收成 aiops_cpu_percent: %q", got)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, `aiops_.*`) {
		t.Fatalf("应提示去掉无界选择器, warns=%v", warns)
	}
}

func TestHealImportedDashboardSkipsLogQL(t *testing.T) {
	logql := `{job="nginx"} |= "error" | json | unwrap bytes | avg_over_time([$__range])`
	d := Dashboard{
		Source: "ai",
		Panels: []DashPanel{
			{ID: 1, Title: "错误日志", Type: "logs", Targets: []DashTarget{{Expr: logql}}},
			{ID: 2, Title: "CPU", Type: "stat", Targets: []DashTarget{{Expr: `100 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100`}}},
		},
	}
	if !healImportedDashboard(&d) {
		t.Fatal("CPU 面板应被纠偏")
	}
	var logExpr, cpuExpr string
	for _, p := range d.Panels {
		if p.Type == "logs" && len(p.Targets) > 0 {
			logExpr = p.Targets[0].Expr
		}
		if p.Title == "CPU" && len(p.Targets) > 0 {
			cpuExpr = p.Targets[0].Expr
		}
	}
	if logExpr != logql {
		t.Fatalf("LogQL 不应被 heal: %q", logExpr)
	}
	if cpuExpr != "aiops_cpu_percent" {
		t.Fatalf("CPU: %q", cpuExpr)
	}
}

func TestRewriteUnboundedAIOpsNameSelector(t *testing.T) {
	cases := map[string]string{
		`{__name__=~"aiops_.*"}`:                       "aiops_cpu_percent",
		`{__name__=~"aiops_.*",instance=~"$instance"}`: `aiops_cpu_percent{instance=~"$instance"}`,
		`sum({__name__=~'aiops_.*'})`:                  "sum(aiops_cpu_percent)",
		`aiops_cpu_percent{instance=~"$instance"}`:     `aiops_cpu_percent{instance=~"$instance"}`,
	}
	for in, want := range cases {
		if got := rewriteUnboundedAIOpsNameSelector(in); got != want {
			t.Fatalf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestCoerceDashDataSourceRef(t *testing.T) {
	if got := coerceDashDataSourceRef(json.RawMessage(`"loki-1"`)); got != "loki-1" {
		t.Fatalf("string: %q", got)
	}
	if got := coerceDashDataSourceRef(json.RawMessage(`{"type":"loki","uid":"abc"}`)); got != "abc" {
		t.Fatalf("grafana object: %q", got)
	}
	if got := coerceDashDataSourceRef(json.RawMessage(`{"name":"Loki"}`)); got != "Loki" {
		t.Fatalf("name: %q", got)
	}
	if got := coerceDashDataSourceRef(nil); got != "" {
		t.Fatalf("nil: %q", got)
	}
}

func TestResolveDataSourceByNameAndType(t *testing.T) {
	cs := &ConfigStore{cfg: ServerConfig{DataSources: []DataSource{
		{ID: "loki-1", Name: "Loki", Type: "loki", Enabled: true},
		{ID: "prom-1", Name: "Prom", Type: "prometheus", Enabled: true},
	}}}
	if ds, ok := cs.ResolveDataSource("loki-1"); !ok || ds.ID != "loki-1" {
		t.Fatalf("by id: %+v ok=%v", ds, ok)
	}
	if ds, ok := cs.ResolveDataSource("Loki"); !ok || ds.ID != "loki-1" {
		t.Fatalf("by name: %+v ok=%v", ds, ok)
	}
	if ds, ok := cs.ResolveDataSource("loki"); !ok || ds.ID != "loki-1" {
		t.Fatalf("by type: %+v ok=%v", ds, ok)
	}
	if _, ok := cs.ResolveDataSource("vm"); ok {
		t.Fatal("builtin vm must not resolve to an external DS")
	}
}

func TestHealPanelQueryExprStripsUnboundedSelector(t *testing.T) {
	in := `{__name__=~"aiops_.*",instance=~"$instance"}`
	got := healPanelQueryExpr("", in)
	if got != `aiops_cpu_percent{instance=~"$instance"}` {
		t.Fatalf("got %q", got)
	}
	if healPanelQueryExpr("prom-1", in) != in {
		t.Fatal("external DS must not rewrite")
	}
}
