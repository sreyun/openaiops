package main

import (
	"encoding/json"
	"testing"
)

const sampleN9E = `{
  "name": "主机大盘",
  "tags": "host prod",
  "configs": {
    "version": "3.0.0",
    "var": [
      {"type":"query","name":"ident","definition":"label_values(cpu_usage_active, ident)","multi":true,"allOption":true},
      {"type":"custom","name":"env","definition":"prod, test"},
      {"type":"datasource","name":"ds"}
    ],
    "panels": [
      {"type":"timeseries","name":"CPU","layout":{"h":7,"w":12,"x":0,"y":0},"targets":[{"expr":"cpu_usage_active{ident=\"$ident\"}","legend":"{{ident}}"}],"options":{"standardOptions":{"util":"percent"}}},
      {"type":"stat","name":"Mem","layout":{"h":4,"w":6,"x":12,"y":0},"targets":[{"expr":"mem_used_bytes"}],"options":{"standardOptions":{"util":"bytesIEC"}}},
      {"type":"barGauge","name":"Disk","layout":{"h":4,"w":6,"x":18,"y":0},"targets":[{"expr":"disk_used_percent"}]},
      {"type":"row","name":"分组","layout":{"h":1,"w":24,"x":0,"y":7}},
      {"type":"pie","name":"占比","layout":{"h":7,"w":8,"x":0,"y":8},"targets":[{"expr":"sum by (ident)(up)"}]},
      {"type":"text","name":"说明","layout":{"h":3,"w":24,"x":0,"y":15},"options":{"content":"# hello"}}
    ]
  }
}`

func TestMapNightingaleDashboard(t *testing.T) {
	d, err := mapNightingaleDashboard([]byte(sampleN9E), "", "nightingale")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "主机大盘" {
		t.Fatalf("名称错误: %q", d.Name)
	}
	if len(d.Tags) != 2 || d.Tags[0] != "host" || d.Tags[1] != "prod" {
		t.Fatalf("标签拆分错误: %v", d.Tags)
	}
	if len(d.Vars) != 2 { // query + custom；datasource 跳过
		t.Fatalf("变量数应为 2，实为 %d", len(d.Vars))
	}
	if d.Vars[0].Type != "query" || !d.Vars[0].Multi || !d.Vars[0].IncludeAll {
		t.Fatalf("query 变量映射错误: %+v", d.Vars[0])
	}
	if d.Vars[1].Type != "custom" || len(d.Vars[1].Options) != 2 || d.Vars[1].Options[0] != "prod" {
		t.Fatalf("custom 变量映射错误: %+v", d.Vars[1])
	}
	if len(d.Panels) != 5 { // row 展平剔除
		t.Fatalf("面板数应为 5（row 剔除），实为 %d", len(d.Panels))
	}
	by := map[string]DashPanel{}
	for _, p := range d.Panels {
		by[p.Title] = p
	}
	if by["CPU"].Type != "timeseries" || by["CPU"].Unit != "percent" || by["CPU"].Grid.W != 12 {
		t.Fatalf("CPU 面板映射错误: %+v", by["CPU"])
	}
	if by["CPU"].Targets[0].Legend != "{{ident}}" {
		t.Fatalf("图例未保留: %+v", by["CPU"].Targets)
	}
	if by["CPU"].Targets[0].Expr != `cpu_usage_active{ident=~"$ident"}` {
		t.Fatalf("导入应把 ident=\"$ident\" 提升为 =~: %q", by["CPU"].Targets[0].Expr)
	}
	if by["Mem"].Type != "stat" || by["Mem"].Unit != "bytes" || by["Mem"].Grid.X != 12 {
		t.Fatalf("Mem 面板映射错误: %+v", by["Mem"])
	}
	if by["Disk"].Type != "bargauge" {
		t.Fatalf("barGauge 应映射 bargauge: %+v", by["Disk"])
	}
	if by["占比"].Type != "piechart" {
		t.Fatalf("pie 应映射为 piechart: %+v", by["占比"])
	}
	if by["说明"].Type != "text" || by["说明"].Text != "# hello" {
		t.Fatalf("text 面板正文错误: %+v", by["说明"])
	}
}

func TestUnmarshalMaybeString(t *testing.T) {
	type box struct {
		A int `json:"a"`
	}
	var b box
	if !unmarshalMaybeString(json.RawMessage(`"{\"a\":5}"`), &b) || b.A != 5 {
		t.Fatalf("字符串形态解析失败: %+v", b)
	}
	b = box{}
	if !unmarshalMaybeString(json.RawMessage(`{"a":7}`), &b) || b.A != 7 {
		t.Fatalf("对象形态解析失败: %+v", b)
	}
}

func TestDetectTemplateFormat(t *testing.T) {
	cases := map[string]string{
		`{"configs":"{}"}`:                                        "nightingale",
		`{"var":[]}`:                                              "nightingale",
		`{"panels":[{"layout":{}}]}`:                              "nightingale",
		`{"templating":{"list":[]},"panels":[]}`:                  "grafana",
		`{"panels":[{"gridPos":{}}]}`:                             "grafana",
		`{"schemaVersion":39,"panels":[]}`:                        "grafana",
		`{"format":"aiops","dashboard":{"name":"x","panels":[]}}`: "aiops",
		`{"name":"主机看板","panels":[{"title":"CPU","type":"stat","grid":{"x":0,"y":0,"w":6,"h":4},"targets":[{"expr":"aiops_cpu_percent"}]}]}`: "aiops",
	}
	for in, want := range cases {
		if got := detectTemplateFormat([]byte(in)); got != want {
			t.Fatalf("detectTemplateFormat(%s)=%s，应为 %s", in, got, want)
		}
	}
}

func TestMapAIOpsDashboard(t *testing.T) {
	raw := []byte(`{"format":"aiops","dashboard":{"name":"模板A","description":"d","panels":[{"id":1,"title":"CPU","type":"stat","grid":{"x":0,"y":0,"w":6,"h":4},"targets":[{"expr":"aiops_cpu_percent"}]}]}}`)
	d, err := mapAIOpsDashboard(raw, "", "aiops-template")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "" {
		t.Fatalf("导入应清空 ID，got %q", d.ID)
	}
	if d.Name != "模板A" || len(d.Panels) != 1 || d.Panels[0].Targets[0].Expr != "aiops_cpu_percent" {
		t.Fatalf("映射错误: %+v", d)
	}
}
