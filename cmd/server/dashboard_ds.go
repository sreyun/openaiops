package main

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
)

// ============================================================================
// 仪表盘数据源分发：面板/变量查询按数据源路由到「内置 VictoriaMetrics」或用户配置的
// 外部 Prometheus / Loki。Prometheus 与 VM 同为 Prometheus HTTP API，复用同一套解析。
// ============================================================================

// ---- Prometheus API 响应解析（VM 与外部 Prometheus 通用）----

func parsePromMatrixBody(body []byte) ([]promMatrix, bool) {
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil || out.Status != "success" {
		return nil, false
	}
	series := make([]promMatrix, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		pts := make([][2]float64, 0, len(r.Values))
		for _, pair := range r.Values {
			if len(pair) < 2 {
				continue
			}
			// 必须走 promTsSeconds：直接 .(float64) 断言在时间戳被编码成字符串/
			// json.Number 时会静默得到 0，整条曲线塌到 1970。内置 VM 路径
			// （vmQueryRangeSeries）早就修过这个坑，外部 Prometheus/VM 数据源这条
			// 解析路径却一直漏着——同一个仪表盘换个数据源就画出乱码般的曲线。
			tsSec, ok := promTsSeconds(pair[0])
			if !ok {
				continue
			}
			sv, _ := pair[1].(string)
			f, err := strconv.ParseFloat(sv, 64)
			if err != nil {
				continue // 跳过 NaN/Inf
			}
			pts = append(pts, [2]float64{float64(tsSec), f})
		}
		series = append(series, promMatrix{Labels: r.Metric, Points: pts})
	}
	return series, true
}

func parsePromVectorBody(body []byte) ([]promSeries, bool) {
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil || out.Status != "success" {
		return nil, false
	}
	series := make([]promSeries, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		if len(r.Value) < 2 {
			continue
		}
		s, _ := r.Value[1].(string)
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		series = append(series, promSeries{Labels: r.Metric, Value: f})
	}
	return series, true
}

func parsePromLabelsBody(body []byte) ([]string, bool) {
	var out struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil || out.Status != "success" {
		return nil, false
	}
	sort.Strings(out.Data)
	return out.Data, true
}

// ---- 外部 Prometheus 数据源查询（走 dataSourceGet，Basic Auth，不走 SSRF，与监控路径一致）----

func dsPromRange(ds DataSource, promql string, start, end, step int64) ([]promMatrix, bool) {
	if step < 1 {
		step = 60
	}
	q := url.Values{"query": {promql}, "start": {strconv.FormatInt(start, 10)}, "end": {strconv.FormatInt(end, 10)}, "step": {strconv.FormatInt(step, 10)}}
	body, code, err := dataSourceGet(ds, "/api/v1/query_range", q)
	if err != nil || code != 200 {
		return nil, false
	}
	return parsePromMatrixBody(body)
}

// dsPromInstantAt evaluates at unix time `at` (0 = now). See vmQueryVectorAt.
func dsPromInstantAt(ds DataSource, promql string, at int64) ([]promSeries, bool) {
	q := url.Values{"query": {promql}}
	if at > 0 {
		q.Set("time", strconv.FormatInt(at, 10))
	}
	body, code, err := dataSourceGet(ds, "/api/v1/query", q)
	if err != nil || code != 200 {
		return nil, false
	}
	return parsePromVectorBody(body)
}

func dsPromLabelValues(ds DataSource, label, match string) ([]string, bool) {
	if label == "" {
		return nil, false
	}
	q := url.Values{}
	if match != "" {
		q.Set("match[]", match)
	}
	body, code, err := dataSourceGet(ds, "/api/v1/label/"+url.PathEscape(label)+"/values", q)
	if err != nil || code != 200 {
		return nil, false
	}
	return parsePromLabelsBody(body)
}

// dashLogLine 是日志面板的一行（Loki）。
type dashLogLine struct {
	TsMs   int64             `json:"ts_ms"`
	Line   string            `json:"line"`
	Labels map[string]string `json:"labels,omitempty"`
}

// dsLokiRange 对 Loki 执行 LogQL 区间查询，返回最近若干行（新→旧）。
func dsLokiRange(ds DataSource, logql string, startNs, endNs int64, limit int) ([]dashLogLine, bool) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	q := url.Values{"query": {logql}, "limit": {strconv.Itoa(limit)}, "start": {strconv.FormatInt(startNs, 10)}, "end": {strconv.FormatInt(endNs, 10)}, "direction": {"backward"}}
	body, code, err := dataSourceGet(ds, "/loki/api/v1/query_range", q)
	if err != nil || code != 200 {
		return nil, false
	}
	var r struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil || r.Status != "success" {
		return nil, false
	}
	var lines []dashLogLine
	for _, res := range r.Data.Result {
		for _, v := range res.Values {
			if len(v) != 2 {
				continue
			}
			ns, _ := strconv.ParseInt(v[0], 10, 64)
			lines = append(lines, dashLogLine{TsMs: ns / 1e6, Line: v[1], Labels: res.Stream})
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].TsMs > lines[j].TsMs })
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines, true
}

// ---- 数据源分发（Server 方法：按 dsID 路由 VM / 外部 Prometheus）----

// lookupPromDS 返回可用于指标查询的数据源；dsID 为空/"vm"(内置) 或非 prometheus/vm、未启用时 ok=false（回退内置 VM）。
// 外部 VictoriaMetrics 与 Prometheus 同为 Prometheus HTTP API，故 type=vm 与 type=prometheus 同路走。
func (s *Server) lookupPromDS(dsID string) (DataSource, bool) {
	if dsID == "" || dsID == "vm" {
		return DataSource{}, false // 内置 VM
	}
	ds, exists := s.cfg.GetDataSource(dsID)
	if !exists || (ds.Type != "prometheus" && ds.Type != "vm") || !ds.Enabled {
		return DataSource{}, false
	}
	return ds, true
}

// dashBackendReady 报告某数据源当前是否可查（VM 已启用 / 外部 DS 存在且启用）。
func (s *Server) dashBackendReady(dsID string) bool {
	if ds, ok := s.lookupPromDS(dsID); ok {
		return ds.URL != ""
	}
	// 未命中外部 prometheus：若 dsID 指向一个存在但非法的源，仍回退 VM
	return s.vm != nil && s.vm.enabled()
}

func (s *Server) dashRangeSeries(dsID, promql string, from, to, step int64) ([]promMatrix, bool) {
	if ds, ok := s.lookupPromDS(dsID); ok {
		return dsPromRange(ds, promql, from, to, step)
	}
	if s.vm == nil || !s.vm.enabled() {
		return nil, false
	}
	return s.vm.vmQueryRangeSeries(promql, from, to, step)
}

// dashVectorAt runs an instant query evaluated at unix time `at` (0 = now).
func (s *Server) dashVectorAt(dsID, promql string, at int64) ([]promSeries, bool) {
	if ds, ok := s.lookupPromDS(dsID); ok {
		return dsPromInstantAt(ds, promql, at)
	}
	if s.vm == nil || !s.vm.enabled() {
		return nil, false
	}
	return s.vm.vmQueryVectorAt(promql, at)
}

func (s *Server) dashLabelValues(dsID, label, match string) ([]string, bool) {
	if ds, ok := s.lookupPromDS(dsID); ok {
		return dsPromLabelValues(ds, label, match)
	}
	if s.vm == nil || !s.vm.enabled() {
		return nil, false
	}
	return s.vm.vmLabelValues(label, match)
}
