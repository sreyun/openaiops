package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"aiops-monitor/shared"
)

const metricsForecastMaxSeries = 32

// metricsForecastReq accepts already-aligned sample series (host/AI/SNMP/…).
// Unlike /dashboards/query-forecast it does not require PromQL.
type metricsForecastReq struct {
	Series     []metricsForecastIn `json:"series"`
	HorizonSec int64               `json:"horizon_sec"` // 0 = equal to max history span
	Step       int64               `json:"step"`        // 0 = auto from median delta
	NowTS      int64               `json:"now_ts"`      // optional client "now"; padded when series end earlier
	Method     string              `json:"method"`      // auto | damped-holt | drift | holt-winters | flat | …
	HostID     string              `json:"host_id"`     // optional: prepend VM lookback for a better fit
}

type metricsForecastIn struct {
	Name   string             `json:"name"`
	Points []metricsFCPointIn `json:"points"` // [[tsSec, val], ...] or {t|ts|timestamp, v|value}
}

// metricsFCPointIn accepts both classic [[ts,val]] arrays and object forms
// ({t,v} / {ts,value} / {timestamp,value}) used by some Vue clients.
type metricsFCPointIn [2]float64

func (p *metricsFCPointIn) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*p = metricsFCPointIn{}
		return nil
	}
	if b[0] == '[' {
		var arr []float64
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		if len(arr) < 2 {
			return fmt.Errorf("point array needs [ts, value]")
		}
		*p = metricsFCPointIn{arr[0], arr[1]}
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	ts := forecastJSONFloat(obj, "t", "ts", "timestamp", "time")
	val := forecastJSONFloat(obj, "v", "val", "value")
	*p = metricsFCPointIn{ts, val}
	return nil
}

func forecastJSONFloat(obj map[string]any, keys ...string) float64 {
	for _, k := range keys {
		v, ok := obj[k]
		if !ok || v == nil {
			continue
		}
		switch n := v.(type) {
		case float64:
			return n
		case json.Number:
			f, _ := n.Float64()
			return f
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
			if err == nil {
				return f
			}
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return 0
}

func metricsFCPointsToPairs(in []metricsFCPointIn) [][2]float64 {
	out := make([][2]float64, 0, len(in))
	for _, p := range in {
		out = append(out, [2]float64{p[0], p[1]})
	}
	return out
}

// handleMetricsForecast POST /api/v1/metrics/forecast
// Multi-series forecast for any Canvas/combo chart (CPU+mem+net, token cost, …).
func (s *Server) handleMetricsForecast(w http.ResponseWriter, r *http.Request) {
	var req metricsForecastReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if len(req.Series) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 series"})
		return
	}
	if len(req.Series) > metricsForecastMaxSeries {
		req.Series = req.Series[:metricsForecastMaxSeries]
	}

	// Normalize + hold-forward every series to a shared "now" so sparse metrics
	// (TCP/UDP transforms with trailing nulls) still get a future band aligned
	// with denser series like CPU%.
	type prepared struct {
		name string
		pts  [][2]float64
		step int64
		span int64
	}
	prep := make([]prepared, 0, len(req.Series))
	var globalEnd int64
	var maxSpan int64
	stepHint := req.Step

	for i, in := range req.Series {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = fmt.Sprintf("系列%d", i+1)
		}
		pts := cleanForecastPoints(metricsFCPointsToPairs(in.Points))
		if len(pts) < 4 {
			continue
		}
		step := stepHint
		if step <= 0 {
			step = inferStep(pts)
		}
		span := int64(pts[len(pts)-1][0] - pts[0][0])
		minSpan := step * 2
		if minSpan < 30 {
			minSpan = 30
		}
		if span < minSpan && len(pts) < 6 {
			continue
		}
		end := int64(pts[len(pts)-1][0])
		if end > globalEnd {
			globalEnd = end
		}
		if span > maxSpan {
			maxSpan = span
		}
		prep = append(prep, prepared{name: name, pts: pts, step: step, span: span})
	}
	if req.NowTS > globalEnd {
		globalEnd = req.NowTS
	}
	for i := range prep {
		prep[i].pts = holdForwardTo(prep[i].pts, globalEnd)
	}

	var lookbackSamples []shared.Sample
	if hid := strings.TrimSpace(req.HostID); hid != "" && maxSpan > 0 {
		if u, ok := s.currentUser(r); ok && s.userCanAccessHost(u, hid) {
			fitFrom := globalEnd - maxSpan - forecastFitLookback(maxSpan)
			keys := make([]string, 0, len(prep))
			for _, p := range prep {
				keys = append(keys, p.name)
			}
			lookbackSamples, _ = s.loadDurableHostHistory(hid, fitFrom, globalEnd, vmNamesForMetricKeys(keys))
		}
	}

	horizon := req.HorizonSec
	if horizon <= 0 {
		horizon = maxSpan
	}
	// 短历史时仍允许客户指定更长展望（AI 会话「近 6h」应对齐未来窗）
	if maxSpan > 0 && req.HorizonSec <= 0 && horizon > maxSpan*4 {
		horizon = maxSpan * 4
	}
	if req.HorizonSec > 0 && horizon > 90*24*3600 {
		horizon = 90 * 24 * 3600
	} else if horizon > 90*24*3600 {
		horizon = 90 * 24 * 3600
	}
	if horizon < 1 && maxSpan > 0 {
		horizon = maxSpan
	}
	// 历史很短时至少展望 30 分钟，保证图表右侧能看见虚线
	if maxSpan > 0 && maxSpan < 1800 && horizon < 1800 {
		horizon = 1800
	}

	out := make([]forecastSeriesOut, 0, len(prep)*2)
	reqMethod := normalizeForecastMethod(req.Method)
	meta := forecastMeta{
		Mode: "forecast", OK: false, NowTS: globalEnd, HorizonSec: horizon,
		Requested: reqMethod, Models: forecastModelCatalog(),
	}
	var bestMAPE float64 = 1e9
	bestMethod := ""
	anyOK := false

	for _, p := range prep {
		pts := p.pts
		out = append(out, forecastSeriesOut{
			Name: p.name, Kind: "history", Points: pts,
		})
		step := p.step
		if stepHint > 0 {
			step = stepHint
		}
		fromTS := int64(pts[len(pts)-1][0])
		// Ensure horizon reaches past the shared now even if this series was short.
		hz := horizon
		if globalEnd > fromTS {
			hz += globalEnd - fromTS
		}
		if hz < step*8 {
			hz = step * 8
		}
		learnKey := "" // UI metrics path: deterministic; no learn-key bias so model switches stay visible
		fitPts := prependHistoryPoints(pts, lookbackSamples, p.name)
		fc, mape, r2, method, errMsg := robustForecastWithKey(fitPts, fromTS, hz, step, learnKey, reqMethod)
		if errMsg != "" || len(fc) == 0 {
			continue
		}
		last := pts[len(pts)-1]
		anyOK = true
		if mape < bestMAPE {
			bestMAPE, bestMethod = mape, method
			meta.R2 = r2
		}
		fcPts := make([][2]float64, 0, len(fc)+1)
		band := make([]forecastPoint, 0, len(fc)+1)
		fcPts = append(fcPts, last)
		band = append(band, forecastPoint{TS: last[0], Value: last[1], Lo: last[1], Hi: last[1]})
		for _, fp := range fc {
			fcPts = append(fcPts, [2]float64{fp.TS, fp.Value})
			band = append(band, fp)
		}
		out = append(out, forecastSeriesOut{
			Name: p.name + " · 预测", Kind: "forecast", Points: fcPts, Band: band,
		})
		meta.Step = step
	}

	if anyOK {
		meta.OK = true
		meta.Method = bestMethod
		meta.MethodLabel = forecastMethodLabel(bestMethod)
		meta.MAPE = bestMAPE
		meta.Message = fmt.Sprintf("左=实时 · 右=预测（%s，MAPE≈%.1f%%，%d 序列）",
			forecastMethodLabel(bestMethod), bestMAPE, countForecastKinds(out))
	} else {
		meta.Message = "数据不足，暂无法预测（每条序列至少约 4 个采样点）"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     meta.OK,
		"series": out,
		"meta":   meta,
	})
}

func countForecastKinds(list []forecastSeriesOut) int {
	n := 0
	for _, s := range list {
		if s.Kind == "forecast" {
			n++
		}
	}
	return n
}

func sanitizeForecastLearnKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if s == "" {
		return "series"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.' || r == '%':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := b.String()
	if out == "" {
		return "series"
	}
	return out
}

func cleanForecastPoints(pts [][2]float64) [][2]float64 {
	out := make([][2]float64, 0, len(pts))
	for _, p := range pts {
		if p[0] <= 0 {
			continue
		}
		if p[1] != p[1] { // NaN
			continue
		}
		out = append(out, p)
	}
	if len(out) < 2 {
		return out
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	dedup := out[:0]
	for _, p := range out {
		if len(dedup) > 0 && dedup[len(dedup)-1][0] == p[0] {
			dedup[len(dedup)-1] = p
			continue
		}
		dedup = append(dedup, p)
	}
	return dedup
}

// holdForwardTo appends the last value at endTS when the series ends earlier,
// so multi-series forecast shares one "现在" boundary.
func holdForwardTo(pts [][2]float64, endTS int64) [][2]float64 {
	if len(pts) == 0 || endTS <= 0 {
		return pts
	}
	last := pts[len(pts)-1]
	if int64(last[0]) >= endTS {
		return pts
	}
	out := make([][2]float64, len(pts)+1)
	copy(out, pts)
	out[len(pts)] = [2]float64{float64(endTS), last[1]}
	return out
}

func inferStep(pts [][2]float64) int64 {
	if len(pts) < 2 {
		return 15
	}
	// Median positive delta
	deltas := make([]float64, 0, len(pts)-1)
	for i := 1; i < len(pts); i++ {
		d := pts[i][0] - pts[i-1][0]
		if d > 0 {
			deltas = append(deltas, d)
		}
	}
	if len(deltas) == 0 {
		return 15
	}
	sort.Float64s(deltas)
	med := int64(deltas[len(deltas)/2] + 0.5)
	if med < 1 {
		med = 15
	}
	if med > 3600 {
		med = 3600
	}
	return med
}
