package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
)

// panelForecastReq extends panel query with forecast / compare controls.
type panelForecastReq struct {
	panelQueryReq
	Mode            string `json:"mode"`             // forecast | pop | yoy | forecast+pop | forecast+yoy
	HorizonSec      int64  `json:"horizon_sec"`      // 0 = equal to (to-from)
	HistoryLookback int64  `json:"history_lookback"` // extra history for model fit; 0 = auto
	Method          string `json:"method"`           // auto | damped-holt | drift | holt-winters | flat | …
}

type forecastPoint struct {
	TS    float64 `json:"ts"`
	Value float64 `json:"value"`
	Lo    float64 `json:"lo"`
	Hi    float64 `json:"hi"`
}

type forecastSeriesOut struct {
	Name      string            `json:"name"`
	Kind      string            `json:"kind"` // history|forecast|band|compare_pop|compare_yoy
	Labels    map[string]string `json:"labels,omitempty"`
	Points    [][2]float64      `json:"points,omitempty"`
	Band      []forecastPoint   `json:"band,omitempty"` // forecast with lo/hi
	CompareOf string            `json:"compare_of,omitempty"`
}

type forecastMeta struct {
	Mode        string              `json:"mode"`
	HorizonSec  int64               `json:"horizon_sec"`
	Step        int64               `json:"step"`
	NowTS       int64               `json:"now_ts,omitempty"` // boundary: left=history, right=forecast
	Method      string              `json:"method,omitempty"`
	MethodLabel string              `json:"method_label,omitempty"`
	Requested   string              `json:"requested_method,omitempty"` // auto | damped-holt | …
	Suggested   string              `json:"suggested_method,omitempty"` // morphology hint
	MAPE        float64             `json:"mape,omitempty"`
	R2          float64             `json:"r2,omitempty"`
	Message     string              `json:"message,omitempty"`
	OK          bool                `json:"ok"`
	Models      []forecastModelInfo `json:"models,omitempty"`
}

// handleDashboardQueryForecast returns history + optional forecast / PoP / YoY overlays.
// Layout contract: history occupies [from,to]; forecast occupies (to, to+horizon] on the same chart.
func (s *Server) handleDashboardQueryForecast(w http.ResponseWriter, r *http.Request) {
	var req panelForecastReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := validatePanelQueryReq(&req.panelQueryReq, true, false); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "forecast"
	}
	if !s.dashBackendReady(req.DataSource) {
		writeJSON(w, http.StatusOK, map[string]any{"series": []any{}, "available": false, "meta": forecastMeta{Mode: mode, OK: false, Message: "数据源不可用"}})
		return
	}

	rangeSec := req.To - req.From
	if req.Step <= 0 {
		req.Step = rangeSec / 300
		if req.Step < 15 {
			req.Step = 15
		}
	}
	horizon := req.HorizonSec
	if horizon <= 0 {
		// 默认未来窗 = 可见历史窗，保证「现在」落在图中央（左历史 | 右预测）
		horizon = rangeSec
	}
	if horizon > 90*24*3600 {
		horizon = 90 * 24 * 3600
	}
	// 自定义超长预测仍允许，但默认路径保持对等拆分；上限放宽到 4× 可见窗
	if horizon > rangeSec*4 {
		horizon = rangeSec * 4
	}

	// Longer fit window for short panels: bursty metrics need more context than the visible 3–5 min.
	lookback := req.HistoryLookback
	if lookback <= 0 {
		lookback = forecastFitLookback(rangeSec)
	}
	fitFrom := req.From - lookback
	if fitFrom < req.To-90*24*3600 {
		fitFrom = req.To - 90*24*3600
	}

	expr := substituteVars(healPanelQueryExpr(req.DataSource, req.Expr), req.Vars, req.Step, rangeSec)
	wantForecast := strings.Contains(mode, "forecast")
	wantPoP := strings.Contains(mode, "pop")
	wantYoY := strings.Contains(mode, "yoy")

	out := make([]forecastSeriesOut, 0, 12)
	meta := forecastMeta{Mode: mode, HorizonSec: horizon, Step: req.Step, NowTS: req.To, OK: true}

	hist, ok := s.dashRangeSeries(req.DataSource, expr, req.From, req.To, req.Step)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"series": []any{}, "available": true,
			"meta": forecastMeta{Mode: mode, OK: false, Message: "查询失败（表达式或数据源）"},
		})
		return
	}
	for i, m := range hist {
		name := legendNameFromLabels(m.Labels, i)
		out = append(out, forecastSeriesOut{
			Name: name, Kind: "history", Labels: m.Labels, Points: m.Points,
		})
	}

	if wantPoP {
		popFrom, popTo := req.From-rangeSec, req.To-rangeSec
		if popSeries, ok2 := s.dashRangeSeries(req.DataSource, expr, popFrom, popTo, req.Step); ok2 {
			for i, m := range popSeries {
				shifted := shiftPoints(m.Points, rangeSec)
				name := legendNameFromLabels(m.Labels, i) + " · 环比"
				out = append(out, forecastSeriesOut{
					Name: name, Kind: "compare_pop", Labels: m.Labels, Points: shifted, CompareOf: "pop",
				})
			}
		}
	}
	if wantYoY {
		const year = int64(365 * 24 * 3600)
		yoyFrom, yoyTo := req.From-year, req.To-year
		if yoySeries, ok2 := s.dashRangeSeries(req.DataSource, expr, yoyFrom, yoyTo, req.Step); ok2 {
			for i, m := range yoySeries {
				shifted := shiftPoints(m.Points, year)
				name := legendNameFromLabels(m.Labels, i) + " · 同比"
				out = append(out, forecastSeriesOut{
					Name: name, Kind: "compare_yoy", Labels: m.Labels, Points: shifted, CompareOf: "yoy",
				})
			}
		}
	}

	if wantForecast {
		fitSeries, okFit := s.dashRangeSeries(req.DataSource, expr, fitFrom, req.To, req.Step)
		if !okFit || len(fitSeries) == 0 {
			fitSeries = hist
		}
		if len(fitSeries) == 0 {
			meta.OK = false
			meta.Message = "数据不足，暂无法预测"
		} else {
			// Forecast each visible history series (matched by labels), up to 12 for combo charts.
			maxFC := metricsForecastMaxSeries
			if maxFC > len(hist) {
				maxFC = len(hist)
			}
			var bestMAPE, bestR2 float64
			bestMethod := ""
			anyOK := false
			for hi := 0; hi < maxFC; hi++ {
				src := fitSeriesFor(fitSeries, hist[hi].Labels, hi)
				if len(src.Points) < 8 {
					continue
				}
				// UI path: empty learnKey + no calibration so the same history yields the
				// same forecast (matches /metrics/forecast host-detail contract).
				fc, mape, r2, method, errMsg := robustForecastWithKey(src.Points, req.To, horizon, req.Step, "", req.Method)
				if errMsg != "" || len(fc) == 0 {
					if hi == 0 {
						meta.OK = false
						meta.Message = errMsg
					}
					continue
				}
				anyOK = true
				if bestMethod == "" || mape < bestMAPE {
					bestMAPE, bestR2, bestMethod = mape, r2, method
				}
				name := legendNameFromLabels(hist[hi].Labels, hi) + " · 预测"
				// Bridge: start forecast at last history point so left/right join seamlessly.
				pts := make([][2]float64, 0, len(fc)+1)
				band := make([]forecastPoint, 0, len(fc)+1)
				if last, okLast := lastValidPoint(hist[hi].Points); okLast {
					pts = append(pts, [2]float64{last[0], last[1]})
					band = append(band, forecastPoint{TS: last[0], Value: last[1], Lo: last[1], Hi: last[1]})
				}
				for _, p := range fc {
					pts = append(pts, [2]float64{p.TS, p.Value})
					band = append(band, p)
				}
				out = append(out, forecastSeriesOut{
					Name: name, Kind: "forecast", Labels: hist[hi].Labels, Points: pts, Band: band,
				})
			}
			if anyOK {
				meta.OK = true
				meta.Method = bestMethod
				meta.MethodLabel = forecastMethodLabel(bestMethod)
				meta.Requested = normalizeForecastMethod(req.Method)
				meta.Models = forecastModelCatalog()
				meta.MAPE = bestMAPE
				meta.R2 = bestR2
				meta.Message = fmt.Sprintf("左=历史 · 中轴=现在 · 右=预测（%s，MAPE≈%.1f%%）", forecastMethodLabel(bestMethod), bestMAPE)
			} else if meta.Message == "" {
				meta.OK = false
				meta.Message = "数据不足，暂无法预测（至少需要约 4 个采样点）"
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"series":    out,
		"step":      req.Step,
		"available": true,
		"meta":      meta,
	})
}

func fitSeriesFor(fit []promMatrix, labels map[string]string, fallbackIdx int) promMatrix {
	if len(fit) == 0 {
		return promMatrix{}
	}
	if key := labelsKey(labels); key != "" {
		for _, s := range fit {
			if labelsKey(s.Labels) == key {
				return s
			}
		}
	}
	if fallbackIdx >= 0 && fallbackIdx < len(fit) {
		return fit[fallbackIdx]
	}
	return fit[0]
}

func labelsKey(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k == "__name__" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte('|')
	}
	return b.String()
}

func lastValidPoint(pts [][2]float64) ([2]float64, bool) {
	for i := len(pts) - 1; i >= 0; i-- {
		if !math.IsNaN(pts[i][1]) && !math.IsInf(pts[i][1], 0) {
			return pts[i], true
		}
	}
	return [2]float64{}, false
}

func legendNameFromLabels(labels map[string]string, idx int) string {
	if labels == nil {
		return fmt.Sprintf("系列%d", idx+1)
	}
	for _, k := range []string{"instance", "job", "name", "__name__", "direction", "device", "iface"} {
		if v := strings.TrimSpace(labels[k]); v != "" {
			return v
		}
	}
	return fmt.Sprintf("系列%d", idx+1)
}

func shiftPoints(pts [][2]float64, deltaSec int64) [][2]float64 {
	out := make([][2]float64, len(pts))
	d := float64(deltaSec)
	for i, p := range pts {
		out[i] = [2]float64{p[0] + d, p[1]}
	}
	return out
}

// holtLinearForecast keeps the historical name; implementation is robustForecast.
func holtLinearForecast(hist [][2]float64, fromTS, horizon, step int64) (band []forecastPoint, mape, r2 float64, method, errMsg string) {
	return robustForecast(hist, fromTS, horizon, step)
}

// robustForecast 兼容入口（无自学习 key，智能匹配）。
func robustForecast(hist [][2]float64, fromTS, horizon, step int64) (band []forecastPoint, mape, r2 float64, method, errMsg string) {
	return robustForecastWithKey(hist, fromTS, horizon, step, "", fcModelAuto)
}

// robustForecastWithKey 多模型 holdout 选型；learnKey 非空时用历史自学习得分微调选型。
// preferMethod：auto（默认按数据类型智能匹配）或用户指定的模型族。
func robustForecastWithKey(hist [][2]float64, fromTS, horizon, step int64, learnKey string, preferMethod ...string) (band []forecastPoint, mape, r2 float64, method, errMsg string) {
	prefer := fcModelAuto
	if len(preferMethod) > 0 {
		prefer = preferMethod[0]
	}
	return robustForecastSelect(hist, fromTS, horizon, step, learnKey, prefer)
}

// seriesProfile 刻画时序形态，驱动模型候选与选型偏置。
type seriesProfile struct {
	period      int
	cv          float64 // 标准差 / (|均值|+ε)
	trendStr    float64 // |后半均值-前半均值| / (IQR+ε)
	seasonStr   float64 // 最佳自相关
	lag1        float64 // 一阶自相关：低 → 高频采样噪声（勿原样回放）
	bursty      bool    // 稀疏尖刺（磁盘 IO 等），适合平滑后的形态回放
	jittery     bool    // 密集高频抖动（连接数等），禁止锯齿回放
	stationary  bool    // 低趋势低季节 → 才适合偏 flat
	monotonicUp bool    // 稳态单调上升（磁盘占用、容量类），偏向近线性外推
}

func profileSeries(vals []float64) seriesProfile {
	n := len(vals)
	p := seriesProfile{}
	if n < 4 {
		return p
	}
	mean, ss := 0.0, 0.0
	for _, v := range vals {
		mean += v
	}
	mean /= float64(n)
	for _, v := range vals {
		d := v - mean
		ss += d * d
	}
	std := math.Sqrt(ss / float64(n))
	p.cv = std / (math.Abs(mean) + 1e-9)
	iqr := seriesIQR(vals)
	half := n / 2
	a, b := recentMedian(vals[:half], half), recentMedian(vals[half:], n-half)
	p.trendStr = math.Abs(b-a) / (iqr + math.Abs(mean)*0.05 + 1e-9)
	p.period, p.seasonStr = detectSeasonPeriodCorr(vals)
	p.lag1 = lag1Autocorr(vals)
	med := recentMedian(vals, n)
	mx := vals[0]
	for _, v := range vals {
		if v > mx {
			mx = v
		}
	}
	peakRatio := mx / (math.Abs(med) + 1e-9)
	// 稀疏突发：少数尖刺、多数点贴近低位（磁盘 IO）；不是连接数那种步步抖
	spikeN := 0
	thr := med + math.Max(iqr, math.Abs(med)*0.5+1)*1.5
	for _, v := range vals {
		if v >= thr {
			spikeN++
		}
	}
	spikeFrac := float64(spikeN) / float64(n)
	p.bursty = peakRatio >= 2.5 && spikeFrac > 0.02 && spikeFrac <= 0.28
	// 高频抖动（连接数等）：仅在非稀疏突发时标记，避免磁盘 IO 尖刺被误判而禁用形态回放
	stepMAE := seriesMAEScale(vals)
	if !p.bursty {
		p.jittery = p.lag1 < 0.55 || (std > 1e-9 && stepMAE/std > 0.85)
	}
	p.stationary = !p.bursty && !p.jittery && p.trendStr < 0.35 && p.seasonStr < 0.45
	// 单调上升：后三分之一中位显著高于前三分之一，且多数相邻差分非负（存储空间类）
	if n >= 12 && !p.jittery {
		t1, t2 := n/3, n-n/3
		if t2 > t1 {
			earlyMed := recentMedian(vals[:t1], t1)
			lateMed := recentMedian(vals[t2:], n-t2)
			up := lateMed - earlyMed
			scale := math.Max(iqr, math.Abs(earlyMed)*0.02+0.5)
			pos, steps := 0, 0
			for i := 1; i < n; i++ {
				steps++
				if vals[i] >= vals[i-1]-1e-9 {
					pos++
				}
			}
			if up >= scale*0.8 && steps > 0 && float64(pos)/float64(steps) >= 0.72 {
				p.monotonicUp = true
				p.stationary = false
			}
		}
	}
	return p
}

func lag1Autocorr(vals []float64) float64 {
	n := len(vals)
	if n < 3 {
		return 1
	}
	mean := 0.0
	for _, v := range vals {
		mean += v
	}
	mean /= float64(n)
	var num, d0, d1 float64
	for i := 0; i < n-1; i++ {
		a, b := vals[i]-mean, vals[i+1]-mean
		num += a * b
		d0 += a * a
		d1 += b * b
	}
	den := math.Sqrt(d0 * d1)
	if den < 1e-12 {
		return 0
	}
	return num / den
}

// movingAverage 简单滑动均值，用于剥离高频采样噪声后再做形态/趋势外推。
func movingAverage(vals []float64, win int) []float64 {
	n := len(vals)
	if n == 0 {
		return nil
	}
	if win < 3 {
		win = 3
	}
	if win%2 == 0 {
		win++
	}
	if win > n {
		win = n
		if win%2 == 0 && win > 1 {
			win--
		}
	}
	half := win / 2
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		lo, hi := i-half, i+half
		if lo < 0 {
			lo = 0
		}
		if hi >= n {
			hi = n - 1
		}
		sum := 0.0
		for j := lo; j <= hi; j++ {
			sum += vals[j]
		}
		out[i] = sum / float64(hi-lo+1)
	}
	return out
}

// robustForecastSelect 多模型 holdout 选型；learnKey 非空时用历史自学习得分微调选型。
// preferMethod=auto 时按数据类型（形态画像）自动匹配最优模型；否则强制使用用户所选模型族。
func robustForecastSelect(hist [][2]float64, fromTS, horizon, step int64, learnKey, preferMethod string) (band []forecastPoint, mape, r2 float64, method, errMsg string) {
	if step < 1 {
		step = 15
	}
	vals, ts, errMsg := prepareForecastSeries(hist, step)
	if errMsg != "" {
		return nil, 0, 0, "", errMsg
	}
	n := len(vals)
	nonNeg := true
	for _, v := range vals {
		if v < -1e-12 {
			nonNeg = false
			break
		}
	}
	prof := profileSeries(vals)
	preferMethod = normalizeForecastMethod(preferMethod)
	// 抖动序列：先平滑再拟合，避免把采样噪声当成趋势/形态
	fitVals := vals
	if prof.jittery {
		fitVals = movingAverage(vals, fcMax(5, n/25))
		fitVals = winsorizeMAD(fitVals, 4.0)
	} else if !prof.bursty {
		fitVals = winsorizeMAD(vals, 4.0)
	} else {
		fitVals = winsorizeMAD(vals, 4.5)
	}
	loB, hiB := forecastValueBounds(fitVals)

	hold := n / 4
	if hold < 4 {
		hold = 4
	}
	if hold > n/3 {
		hold = n / 3
	}
	trainN := n - hold
	if trainN < 8 {
		trainN = n - 3
		hold = 3
	}
	if trainN < 6 {
		trainN = n - 2
		hold = 2
	}

	period := prof.period
	if period < 4 {
		period = 0
	}
	// 无显著季节时，用「展望窗」或近期窗口做形态回放周期
	shapeP := period
	if shapeP < 4 {
		shapeP = hold
		if shapeP < 8 {
			shapeP = fcMin(16, n/3)
		}
		if shapeP < 4 {
			shapeP = fcMin(8, n/2)
		}
	}

	candNames := []string{"flat", "damped-holt", "drift"}
	// 稀疏突发才启用形态回放；高频抖动序列禁用（否则会出现锯齿虚线）
	if prof.bursty && !prof.jittery {
		candNames = append(candNames, "shape-replay")
	}
	if period >= 4 && n >= period*2 {
		candNames = append(candNames, fmt.Sprintf("seasonal-%d", period), fmt.Sprintf("holt-winters-%d", period))
	} else if n >= 24 && prof.seasonStr >= 0.35 && !prof.jittery {
		wp := shapeP
		if wp >= 6 && n >= wp*2 {
			candNames = append(candNames, fmt.Sprintf("seasonal-%d", wp))
		}
	}
	if forced := forceCandidateNames(preferMethod, period, shapeP, n, prof); len(forced) > 0 {
		candNames = forced
	}

	type candScore struct {
		name string
		pred []float64
		nMAE float64
	}
	trainFit := fitVals[:trainN]
	actualHold := vals[trainN:]
	// holdout 也对比平滑后的轨迹，避免奖励「拟合噪声」
	holdTruth := actualHold
	if prof.jittery {
		holdTruth = fitVals[trainN:]
	}
	scale := seriesMAEScale(holdTruth)
	var cands []candScore
	for _, name := range candNames {
		var holdPred []float64
		switch {
		case name == "shape-replay":
			holdPred = shapeReplayForecast(fitVals[:trainN], hold, shapeP)
		case strings.HasPrefix(name, "holt-winters-"):
			p := atoiSuffix(name, "holt-winters-")
			holdPred = holtWintersForecast(trainFit, hold, p, 0.88)
		case strings.HasPrefix(name, "seasonal-"):
			p := atoiSuffix(name, "seasonal-")
			holdPred = seasonalNaiveForecast(fitVals[:trainN], hold, p)
		case name == "damped-holt":
			phi := 0.88
			if prof.jittery {
				phi = 0.82
			}
			holdPred = dampedHoltForecast(trainFit, hold, phi)
		case name == "drift":
			damp := 0.82
			if prof.jittery {
				damp = 0.7
			} else if prof.monotonicUp {
				damp = 0.96 // 存储类近线性，减少阻尼低估
			}
			holdPred = driftForecast(trainFit, hold, damp)
		default:
			base := recentLevel(trainFit, fcMin(12, len(trainFit)))
			holdPred = constantForecast(hold, base)
		}
		score := normalizedMAE(holdTruth, holdPred, scale)
		// 额外惩罚预测段的高频抖动（防止再选出锯齿模型）
		score *= 1 + 0.55*forecastJitterPenalty(holdPred)
		if learnKey != "" {
			score = learnAdjustCandidateScore(learnKey, name, score)
		}
		switch {
		case name == "flat" && prof.bursty:
			score *= 1.25
		case name == "flat" && prof.jittery:
			score *= 1.08
		case name == "flat" && prof.stationary:
			score *= 0.97
		case name == "shape-replay" && prof.bursty:
			score *= 0.85
		case name == "shape-replay" && prof.jittery:
			score *= 1.6
		case strings.HasPrefix(name, "seasonal-") && prof.seasonStr >= 0.4 && !prof.jittery:
			score *= 0.9
		case strings.HasPrefix(name, "holt-winters-") && prof.seasonStr >= 0.35 && prof.trendStr >= 0.25:
			score *= 0.88
		case name == "damped-holt" && (prof.trendStr >= 0.35 || prof.jittery):
			score *= 0.88
		case name == "drift" && prof.jittery:
			score *= 1.2 // 抖动序列的伪趋势易发散触顶
		case name == "drift" && prof.monotonicUp:
			score *= 0.78 // 存储/容量类优先近线性漂移
		case name == "flat" && prof.monotonicUp:
			score *= 1.4
		case name == "damped-holt" && prof.monotonicUp:
			score *= 0.92
		}
		cands = append(cands, candScore{name, holdPred, score})
	}
	if len(cands) == 0 {
		return nil, 0, 0, "", "无可用预测模型"
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.nMAE < best.nMAE {
			best = c
		}
	}
	forcedPick := preferMethod != "" && preferMethod != fcModelAuto
	var flatCand *candScore
	for i := range cands {
		if cands[i].name == "flat" {
			flatCand = &cands[i]
			break
		}
	}
	if !forcedPick && best.name != "flat" && flatCand != nil && (prof.stationary || (prof.jittery && prof.trendStr < 0.4)) {
		rel := (flatCand.nMAE - best.nMAE) / math.Max(flatCand.nMAE, 1e-6)
		thresh := 0.04
		if prof.jittery {
			thresh = 0.08
		}
		if rel < thresh {
			best = *flatCand
		}
	}
	// 稀疏突发若误选 flat，改用形态/季节类；抖动序列则偏向阻尼 Holt
	if !forcedPick && best.name == "flat" && prof.bursty && !prof.jittery {
		var alt *candScore
		for i := range cands {
			c := &cands[i]
			if c.name == "shape-replay" || strings.HasPrefix(c.name, "seasonal-") || strings.HasPrefix(c.name, "holt-winters-") {
				if alt == nil || c.nMAE < alt.nMAE {
					alt = c
				}
			}
		}
		if alt != nil {
			best = *alt
		}
	}
	if !forcedPick && (best.name == "flat" || best.name == "drift") && prof.jittery && prof.trendStr >= 0.35 {
		for i := range cands {
			if cands[i].name == "damped-holt" {
				best = cands[i]
				break
			}
		}
	}
	// 单调上升（磁盘占用等）：禁止 flat 低估填满时间
	if !forcedPick && prof.monotonicUp && best.name == "flat" {
		for i := range cands {
			if cands[i].name == "drift" {
				best = cands[i]
				break
			}
		}
	}

	steps := int(horizon / step)
	if steps < 1 {
		steps = 1
	}
	if steps > 2000 {
		steps = 2000
	}

	var future []float64
	switch {
	case best.name == "shape-replay":
		sp := shapeP
		if sp < 4 {
			sp = fcMin(fcMax(steps, 8), n/2)
		}
		future = shapeReplayForecast(fitVals, steps, sp)
		method = "shape-replay"
	case strings.HasPrefix(best.name, "holt-winters-"):
		p := atoiSuffix(best.name, "holt-winters-")
		future = holtWintersForecast(fitVals, steps, p, 0.88)
		method = best.name
	case strings.HasPrefix(best.name, "seasonal-"):
		p := atoiSuffix(best.name, "seasonal-")
		future = seasonalNaiveForecast(fitVals, steps, p)
		method = best.name
	case best.name == "damped-holt":
		phi := 0.88
		if prof.jittery {
			phi = 0.82
		}
		future = dampedHoltForecast(fitVals, steps, phi)
		method = "damped-holt"
	case best.name == "drift":
		damp := 0.82
		if prof.jittery {
			damp = 0.7
		} else if prof.monotonicUp {
			damp = 0.96
		}
		future = driftForecast(fitVals, steps, damp)
		method = "drift"
	default:
		future = constantForecast(steps, recentLevel(fitVals, fcMin(fcMax(8, n/10), n)))
		method = "flat"
	}

	// 最终再轻平滑预测轨迹，去掉残余锯齿；与末点衔接。
	// 用户强制选模时跳过重平滑，避免把「一路涨/跌」与「基本不变」抹成同一条线。
	if !forcedPick && len(future) >= 5 {
		sw := len(future) / 3
		if sw < 3 {
			sw = 3
		}
		if sw > 9 {
			sw = 9
		}
		future = movingAverage(future, sw)
	}
	future = blendForecastAnchor(future, vals[n-1], fcMin(4, len(future)))

	rmse := 0.0
	if len(best.pred) > 0 {
		var ss float64
		for i := 0; i < len(best.pred) && trainN+i < n; i++ {
			e := vals[trainN+i] - best.pred[i]
			ss += e * e
		}
		rmse = math.Sqrt(ss / float64(len(best.pred)))
	}
	if rmse < 1e-9 {
		rmse = seriesStd(vals) * 0.25
		if rmse < 0.01 {
			rmse = 0.01
		}
	}
	iqr := seriesIQR(vals)
	// 突发序列允许更宽置信带，体现真实不确定性
	rmseCap := 2.4
	if prof.bursty {
		rmseCap = 3.5
	}
	if iqr > 0 && rmse > iqr*rmseCap {
		rmse = iqr * rmseCap
	}
	mape = mapeOf(actualHold, best.pred)
	r2 = holdoutR2(actualHold, best.pred)

	anchor := vals[n-1]
	// 漂移钳制：抖动序列收紧，防止假趋势直线爬升后触顶成平台
	maxDelta := math.Max(iqr*2.0, math.Abs(anchor)*0.35+seriesStd(fitVals)*1.0+1)
	if method == "shape-replay" || strings.HasPrefix(method, "seasonal-") || strings.HasPrefix(method, "holt-winters-") {
		maxDelta = math.Max(maxDelta, seriesStd(fitVals)*2.2)
		maxDelta = math.Max(maxDelta, (hiB-loB)*0.75)
	}
	if method == "drift" || (prof.jittery && method == "damped-holt") {
		// 展望期内允许的变化 ≈ 近期斜率×步数×阻尼，避免 TIME_WAIT 类假趋势顶格
		recentSlope := 0.0
		if n >= 8 {
			recentSlope = (fitVals[n-1] - fitVals[n-8]) / 7
		}
		cap := math.Abs(recentSlope)*float64(steps)*0.55 + seriesStd(fitVals)*0.8 + 1
		if !forcedPick && cap < maxDelta {
			maxDelta = cap
		}
	}
	if forcedPick {
		// 放宽钳制，让下拉切换模型时右侧虚线形态可辨
		maxDelta = math.Max(maxDelta, (hiB-loB)*0.95)
		maxDelta = math.Max(maxDelta, math.Abs(anchor)*0.55+seriesStd(fitVals)*2.2+1)
		if method == "flat" {
			maxDelta = math.Min(maxDelta, seriesStd(fitVals)*0.35+1)
		}
	}
	if looksLikePercentUnit(vals) {
		maxDelta = math.Min(maxDelta, 35)
	}

	t0 := float64(fromTS)
	if t0 < ts[n-1] {
		t0 = ts[n-1]
	}
	band = make([]forecastPoint, 0, steps)
	for k := 1; k <= steps; k++ {
		fv := future[k-1]
		if fv > anchor+maxDelta {
			fv = anchor + maxDelta
		} else if fv < anchor-maxDelta {
			fv = anchor - maxDelta
		}
		fv = clampForecast(fv, loB, hiB, nonNeg)
		width := 1.64 * rmse * math.Sqrt(1+float64(k)/10)
		wCap := 2.2
		if prof.bursty {
			wCap = 3.2
		}
		if iqr > 0 && width > iqr*wCap {
			width = iqr * wCap
		}
		lo, hi := fv-width, fv+width
		if nonNeg && lo < 0 {
			lo = 0
		}
		if looksLikePercentUnit(vals) {
			if hi > 100 {
				hi = 100
			}
			if lo < 0 {
				lo = 0
			}
		}
		band = append(band, forecastPoint{
			TS:    t0 + float64(k)*float64(step),
			Value: fv,
			Lo:    lo,
			Hi:    hi,
		})
	}
	return band, mape, r2, method, ""
}

func prepareForecastSeries(hist [][2]float64, step int64) (vals, ts []float64, errMsg string) {
	type pv struct{ t, v float64 }
	raw := make([]pv, 0, len(hist))
	for _, p := range hist {
		if math.IsNaN(p[1]) || math.IsInf(p[1], 0) {
			continue
		}
		raw = append(raw, pv{p[0], p[1]})
	}
	if len(raw) < 4 {
		return nil, nil, "数据不足，暂无法预测（有效采样不足）"
	}
	sort.Slice(raw, func(i, j int) bool { return raw[i].t < raw[j].t })
	// Deduplicate timestamps (keep last).
	dedup := raw[:0]
	for _, p := range raw {
		if len(dedup) > 0 && p.t == dedup[len(dedup)-1].t {
			dedup[len(dedup)-1] = p
			continue
		}
		dedup = append(dedup, p)
	}
	raw = dedup
	if len(raw) < 4 {
		return nil, nil, "数据不足，暂无法预测（有效采样不足）"
	}
	span := raw[len(raw)-1].t - raw[0].t
	// 短窗也允许预测（AI 会话/刚上线主机常见只有数分钟样本）
	minSpan := float64(step * 2)
	if minSpan < 30 {
		minSpan = 30
	}
	if span < minSpan && len(raw) < 6 {
		return nil, nil, "数据不足，暂无法预测（时间跨度过短）"
	}

	vals = make([]float64, len(raw))
	ts = make([]float64, len(raw))
	for i, p := range raw {
		ts[i], vals[i] = p.t, p.v
	}
	// 保留原始波动供形态回放；极端野值仅在非突发路径由 fitVals 掐尖。
	// 重采样到均匀栅格后再交给各模型——原因见 resampleForecastSeries。
	return resampleForecastSeries(ts, vals, step)
}

// forecastMaxFitPoints 限制拟合栅格的点数：自定义超长窗（90 天）配上小 step
// 会算出几十万个点，模型是 O(n·候选数)，会把一次图表刷新拖成秒级。
const forecastMaxFitPoints = 3000

// resampleForecastSeries 把不等间隔的历史点重采样到固定 step 的均匀时间栅格。
//
// 这是整个预测/趋势链路里最关键的一步，之前完全缺失。本文件所有模型
// （dampedHoltForecast / driftForecast / holtWintersForecast / seasonalNaiveForecast /
// shapeReplayForecast）都是**按数组下标**递推的：它们默认 vals[i] 与 vals[i+1]
// 相隔恰好一个 step。而实际喂进来的序列几乎从来不是均匀的：
//
//   - 主机详情曲线 = VM 那半（adaptiveHistoryStep，24h 窗为 180s）+ 内存实时尾巴
//     （Agent 上报间隔 5～10s）拼接而成；
//   - /metrics/forecast 还会把更粗粒度的 VM lookback 直接前置到可见窗之前；
//   - VM 的 query_range 在无数据的评估点上不出点，alignSamplesToStep 又会跳过
//     超过 3 个 step 的空洞，于是序列里天然带断点。
//
// 后果是系统性的：drift 的斜率是 (末值-中值)/下标差，密集尾段会让「每步变化」
// 按下标而不是按时间计算，外推时再乘以 step 秒——24h 窗下末段斜率被放大近 20 倍。
// 磁盘写满时间、CPU 触阈 ETA、AI 预警（raiseForecastEarlyWarning）全部建立在这个
// 斜率上，所以表现出来就是"趋势分析异常"：一会儿说 2 小时后写满，一会儿说 30 天。
//
// 另外还要处理断点：跨越真实中断（主机离线、VM 空洞）做线性插值等于凭空造数据，
// 更糟的是把中断前后的水位差当成趋势。这里的做法是只保留**最后一段连续区间**。
func resampleForecastSeries(rawTs, rawVals []float64, step int64) (vals, ts []float64, errMsg string) {
	n := len(rawTs)
	if n < 2 || step < 1 {
		return rawVals, rawTs, ""
	}
	fstep := float64(step)
	maxGap := fstep * 5
	if med := medianDeltaOf(rawTs); med > 0 && med*5 > maxGap {
		maxGap = med * 5
	}
	start := 0
	for i := n - 1; i > 0; i-- {
		if rawTs[i]-rawTs[i-1] > maxGap {
			start = i
			break
		}
	}
	// 尾段太短就退回整段：宁可带着断点拟合，也好过只剩两三个点直接判"数据不足"。
	if n-start < 4 {
		start = 0
	}
	rawTs, rawVals = rawTs[start:], rawVals[start:]
	n = len(rawTs)

	span := rawTs[n-1] - rawTs[0]
	steps := int(span/fstep) + 1
	if steps < 4 {
		return rawVals, rawTs, "" // 跨度不足几个栅格，重采样反而丢信息
	}
	if steps > forecastMaxFitPoints {
		steps = forecastMaxFitPoints
	}

	// 栅格锚在**最后一个真实采样点**上向前铺：末点必须精确，
	// 因为 band 的起点、blendForecastAnchor 的锚点都取 vals[n-1]。
	end := rawTs[n-1]
	vals = make([]float64, steps)
	ts = make([]float64, steps)
	j := 0
	for k := 0; k < steps; k++ {
		t := end - float64(steps-1-k)*fstep
		for j+1 < n && rawTs[j+1] <= t {
			j++
		}
		ts[k] = t
		switch {
		case t <= rawTs[0]:
			vals[k] = rawVals[0]
		case j+1 >= n:
			vals[k] = rawVals[n-1]
		default:
			t0, t1 := rawTs[j], rawTs[j+1]
			if t1 <= t0 {
				vals[k] = rawVals[j+1]
				break
			}
			w := (t - t0) / (t1 - t0)
			vals[k] = rawVals[j]*(1-w) + rawVals[j+1]*w
		}
	}
	return vals, ts, ""
}

// medianDeltaOf 返回相邻时间戳差值的中位数（用于识别真实中断的阈值）。
func medianDeltaOf(ts []float64) float64 {
	if len(ts) < 2 {
		return 0
	}
	d := make([]float64, 0, len(ts)-1)
	for i := 1; i < len(ts); i++ {
		if delta := ts[i] - ts[i-1]; delta > 0 {
			d = append(d, delta)
		}
	}
	if len(d) == 0 {
		return 0
	}
	sort.Float64s(d)
	return d[len(d)/2]
}

func atoiSuffix(name, prefix string) int {
	s := strings.TrimPrefix(name, prefix)
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + int(c-'0')
	}
	return v
}

func constantForecast(steps int, v float64) []float64 {
	out := make([]float64, steps)
	for i := range out {
		out[i] = v
	}
	return out
}

func recentLevel(vals []float64, k int) float64 {
	// 用近期均值（非中位数）保留更多动态，避免把波动抹平
	if k > len(vals) {
		k = len(vals)
	}
	if k <= 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals[len(vals)-k:] {
		sum += v
	}
	return sum / float64(k)
}

func seriesStd(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	m := 0.0
	for _, v := range vals {
		m += v
	}
	m /= float64(len(vals))
	ss := 0.0
	for _, v := range vals {
		d := v - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(vals)))
}

func seriesMAEScale(vals []float64) float64 {
	if len(vals) < 2 {
		return 1
	}
	sum := 0.0
	for i := 1; i < len(vals); i++ {
		sum += math.Abs(vals[i] - vals[i-1])
	}
	s := sum / float64(len(vals)-1)
	if s < 1e-9 {
		s = math.Abs(recentLevel(vals, len(vals))) * 0.05
	}
	if s < 1e-9 {
		s = 1
	}
	return s
}

func normalizedMAE(actual, pred []float64, scale float64) float64 {
	n := len(actual)
	if len(pred) < n {
		n = len(pred)
	}
	if n == 0 {
		return 999
	}
	if scale < 1e-9 {
		scale = 1
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += math.Abs(actual[i] - pred[i])
	}
	return (sum / float64(n)) / scale
}

// blendForecastAnchor 让预测前几步从历史末点平滑过渡到模型轨迹。
func blendForecastAnchor(future []float64, anchor float64, blend int) []float64 {
	if len(future) == 0 || blend <= 0 {
		return future
	}
	out := append([]float64(nil), future...)
	if blend > len(out) {
		blend = len(out)
	}
	for i := 0; i < blend; i++ {
		w := float64(i+1) / float64(blend+1)
		out[i] = (1-w)*anchor + w*out[i]
	}
	return out
}

// forecastJitterPenalty 衡量预测轨迹的逐步抖动（0≈平滑，≈1 锯齿严重）。
func forecastJitterPenalty(pred []float64) float64 {
	if len(pred) < 4 {
		return 0
	}
	step := seriesMAEScale(pred)
	std := seriesStd(pred)
	if std < 1e-9 {
		return 0
	}
	r := step / std
	// 平滑曲线 step/std 通常 <0.5；原样噪声回放常 >1
	if r <= 0.55 {
		return 0
	}
	p := (r - 0.55) / 1.2
	if p > 1 {
		p = 1
	}
	return p
}

// shapeReplayForecast 回放最近周期的【平滑形态】（非原始采样点），
// 只用于稀疏突发序列；禁止把高频噪声逐点复制成锯齿虚线。
func shapeReplayForecast(vals []float64, steps, period int) []float64 {
	n := len(vals)
	out := make([]float64, steps)
	if n == 0 || steps == 0 {
		return out
	}
	if period < 4 {
		period = fcMin(fcMax(8, steps), n/2)
	}
	if period > n {
		period = n
	}
	if period < 2 {
		return constantForecast(steps, vals[n-1])
	}
	shape := winsorizeMAD(append([]float64(nil), vals[n-period:]...), 4.5)
	// 关键平滑：去掉点噪，只保留尖刺包络/慢变形态
	sw := period / 6
	if sw < 5 {
		sw = 5
	}
	if sw > 15 {
		sw = 15
	}
	shape = movingAverage(shape, sw)
	shape = movingAverage(shape, fcMin(5, sw))
	var shapeMean float64
	for _, v := range shape {
		shapeMean += v
	}
	shapeMean /= float64(len(shape))
	period = len(shape)
	level := recentLevel(vals, fcMin(period, n))
	level = 0.55*level + 0.45*vals[n-1]
	drift := 0.0
	if n >= period*2 {
		prev := movingAverage(vals[n-2*period:n-period], sw)
		var pm float64
		for _, v := range prev {
			pm += v
		}
		pm /= float64(len(prev))
		drift = (shapeMean - pm) / float64(period)
		if drift > 0 {
			drift *= 0.4
		} else {
			drift *= 0.3
		}
	}
	for k := 0; k < steps; k++ {
		raw := shape[k%period]
		centered := raw - shapeMean
		amp := 0.75 - 0.2*float64(k)/float64(steps+1)
		if amp < 0.4 {
			amp = 0.4
		}
		out[k] = level + amp*centered + drift*float64(k+1)
	}
	return out
}

// holtWintersForecast 加法 Holt-Winters（水平+阻尼趋势+季节）。
func holtWintersForecast(vals []float64, steps, period int, phi float64) []float64 {
	n := len(vals)
	out := make([]float64, steps)
	if n < period*2 || period < 2 {
		return dampedHoltForecast(vals, steps, phi)
	}
	if phi <= 0 || phi >= 1 {
		phi = 0.92
	}
	alpha, beta, gamma := 0.35, 0.15, 0.25
	// 初始化：首季平均为水平，季节为相对偏差
	level := 0.0
	for i := 0; i < period; i++ {
		level += vals[i]
	}
	level /= float64(period)
	season := make([]float64, period)
	for i := 0; i < period; i++ {
		season[i] = vals[i] - level
	}
	trend := 0.0
	if n >= period*2 {
		var s2 float64
		for i := period; i < period*2; i++ {
			s2 += vals[i]
		}
		trend = (s2/float64(period) - level) / float64(period)
	}
	for t := period; t < n; t++ {
		s := season[t%period]
		prev := level
		y := vals[t]
		level = alpha*(y-s) + (1-alpha)*(level+phi*trend)
		trend = beta*(level-prev) + (1-beta)*phi*trend
		season[t%period] = gamma*(y-level) + (1-gamma)*s
	}
	sumPhi := 0.0
	p := 1.0
	for k := 0; k < steps; k++ {
		p *= phi
		sumPhi += p
		out[k] = level + sumPhi*trend + season[(n+k)%period]
	}
	return out
}

func winsorizeMAD(vals []float64, k float64) []float64 {
	if len(vals) < 8 {
		return vals
	}
	tmp := append([]float64(nil), vals...)
	sort.Float64s(tmp)
	med := tmp[len(tmp)/2]
	devs := make([]float64, len(vals))
	for i, v := range vals {
		devs[i] = math.Abs(v - med)
	}
	sort.Float64s(devs)
	mad := devs[len(devs)/2]
	if mad < 1e-12 {
		return vals
	}
	scale := 1.4826 * mad
	lo, hi := med-k*scale, med+k*scale
	out := make([]float64, len(vals))
	for i, v := range vals {
		if v < lo {
			out[i] = lo
		} else if v > hi {
			out[i] = hi
		} else {
			out[i] = v
		}
	}
	return out
}

func forecastValueBounds(vals []float64) (lo, hi float64) {
	mn, mx := vals[0], vals[0]
	for _, v := range vals {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	span := mx - mn
	if span < 1e-9 {
		span = math.Abs(mx)*0.2 + 1
	}
	// Recent slope hint: allow more headroom when clearly rising, still capped.
	headroom := 0.25 * span
	n := len(vals)
	if n >= 8 {
		a, b := recentMedian(vals[:n/2], n/2), recentMedian(vals[n/2:], n-n/2)
		if b > a {
			headroom = math.Max(headroom, (b-a)*1.2)
		}
	}
	if headroom > span*0.6 {
		headroom = span * 0.6 // tighter cap — prevent 90%→150% explosions
	}
	lo, hi = mn-0.1*span, mx+headroom
	if looksLikePercentUnit(vals) {
		if lo < 0 {
			lo = 0
		}
		if hi > 100 {
			hi = 100
		}
	}
	return lo, hi
}

func looksLikePercentUnit(vals []float64) bool {
	if len(vals) == 0 {
		return false
	}
	in := 0
	for _, v := range vals {
		if v >= -0.5 && v <= 105 {
			in++
		}
	}
	if float64(in)/float64(len(vals)) < 0.9 {
		return false
	}
	med := recentMedian(vals, len(vals))
	return med >= 0 && med <= 100
}

func seriesIQR(vals []float64) float64 {
	if len(vals) < 4 {
		return 0
	}
	tmp := append([]float64(nil), vals...)
	sort.Float64s(tmp)
	q1 := tmp[len(tmp)/4]
	q3 := tmp[(len(tmp)*3)/4]
	return math.Max(0, q3-q1)
}

func clampForecast(v, lo, hi float64, nonNeg bool) float64 {
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	if nonNeg && v < 0 {
		v = 0
	}
	return v
}

func recentMedian(vals []float64, k int) float64 {
	if k > len(vals) {
		k = len(vals)
	}
	if k <= 0 {
		return 0
	}
	slice := append([]float64(nil), vals[len(vals)-k:]...)
	sort.Float64s(slice)
	return slice[len(slice)/2]
}

func dampedHoltForecast(vals []float64, steps int, phi float64) []float64 {
	if phi <= 0 || phi >= 1 {
		phi = 0.92
	}
	alpha, beta := tuneHoltAB(vals)
	level, trend := vals[0], 0.0
	if len(vals) > 1 {
		trend = vals[1] - vals[0]
	}
	for i := 1; i < len(vals); i++ {
		prev := level
		level = alpha*vals[i] + (1-alpha)*(level+phi*trend)
		trend = beta*(level-prev) + (1-beta)*phi*trend
	}
	out := make([]float64, steps)
	sumPhi := 0.0
	p := 1.0
	for k := 0; k < steps; k++ {
		p *= phi
		sumPhi += p
		out[k] = level + sumPhi*trend
	}
	return out
}

func tuneHoltAB(vals []float64) (alpha, beta float64) {
	// Small grid search on last-holdout one-step SSE.
	n := len(vals)
	hold := n / 5
	if hold < 2 {
		hold = 2
	}
	trainN := n - hold
	if trainN < 4 {
		return 0.35, 0.12
	}
	bestA, bestB, bestSSE := 0.35, 0.12, math.Inf(1)
	for _, a := range []float64{0.2, 0.3, 0.45, 0.6, 0.75} {
		for _, b := range []float64{0.05, 0.1, 0.2, 0.35} {
			level, trend := vals[0], 0.0
			if trainN > 1 {
				trend = vals[1] - vals[0]
			}
			sse := 0.0
			for i := 1; i < n; i++ {
				prev := level
				preLevel := prev + trend
				level = a*vals[i] + (1-a)*(level+trend)
				trend = b*(level-prev) + (1-b)*trend
				if i >= trainN {
					e := vals[i] - preLevel
					sse += e * e
				}
			}
			if sse < bestSSE {
				bestSSE, bestA, bestB = sse, a, b
			}
		}
	}
	return bestA, bestB
}

func driftForecast(vals []float64, steps int, damp float64) []float64 {
	n := len(vals)
	if n < 2 {
		return constantForecast(steps, vals[0])
	}
	// Use second half for slope to reduce early-regime bias.
	start := n / 2
	if start > n-2 {
		start = 0
	}
	slope := (vals[n-1] - vals[start]) / float64(n-1-start)
	if damp <= 0 || damp >= 1 {
		damp = 0.9
	}
	out := make([]float64, steps)
	last := vals[n-1]
	s := slope
	for k := 0; k < steps; k++ {
		s *= damp
		last += s
		out[k] = last
	}
	return out
}

func detectSeasonPeriod(vals []float64) int {
	p, corr := detectSeasonPeriodCorr(vals)
	if corr < 0.32 {
		return 0
	}
	return p
}

func detectSeasonPeriodCorr(vals []float64) (period int, bestCorr float64) {
	n := len(vals)
	if n < 16 {
		return 0, 0
	}
	mean := 0.0
	for _, v := range vals {
		mean += v
	}
	mean /= float64(n)
	bestLag := 0
	maxLag := n / 2
	if maxLag > 120 {
		maxLag = 120
	}
	for lag := 4; lag <= maxLag; lag++ {
		var num, d1, d2 float64
		m := n - lag
		for i := 0; i < m; i++ {
			a, b := vals[i]-mean, vals[i+lag]-mean
			num += a * b
			d1 += a * a
			d2 += b * b
		}
		den := math.Sqrt(d1 * d2)
		if den < 1e-12 {
			continue
		}
		corr := num / den
		if corr > bestCorr {
			bestCorr, bestLag = corr, lag
		}
	}
	if bestCorr < 0.28 {
		return 0, bestCorr
	}
	return bestLag, bestCorr
}

func seasonalNaiveForecast(vals []float64, steps, period int) []float64 {
	if period < 2 {
		period = 2
	}
	n := len(vals)
	out := make([]float64, steps)
	if n < period {
		return constantForecast(steps, recentLevel(vals, n))
	}
	// 保留季节形态：轻度对齐到近期水位，不再用中位数大幅抹平尖刺
	level := recentLevel(vals, fcMin(period, n))
	var seasonMean float64
	for i := n - period; i < n; i++ {
		seasonMean += vals[i]
	}
	seasonMean /= float64(period)
	shift := level - seasonMean
	for k := 0; k < steps; k++ {
		idx := n - period + (k % period)
		out[k] = vals[idx] + 0.35*shift
	}
	return out
}

func mapeOf(actual, pred []float64) float64 {
	n := len(actual)
	if len(pred) < n {
		n = len(pred)
	}
	if n == 0 {
		return 999
	}
	var sum float64
	cnt := 0
	for i := 0; i < n; i++ {
		a := math.Abs(actual[i])
		if a < 1e-9 {
			sum += math.Abs(actual[i] - pred[i])
			cnt++
			continue
		}
		sum += math.Abs((actual[i] - pred[i]) / a)
		cnt++
	}
	if cnt == 0 {
		return 999
	}
	return (sum / float64(cnt)) * 100
}

func holdoutR2(actual, pred []float64) float64 {
	n := len(actual)
	if len(pred) < n {
		n = len(pred)
	}
	if n < 2 {
		return 0
	}
	mean := 0.0
	for i := 0; i < n; i++ {
		mean += actual[i]
	}
	mean /= float64(n)
	var ssRes, ssTot float64
	for i := 0; i < n; i++ {
		e := actual[i] - pred[i]
		ssRes += e * e
		d := actual[i] - mean
		ssTot += d * d
	}
	if ssTot < 1e-12 {
		return 0
	}
	r2 := 1 - ssRes/ssTot
	if r2 < 0 {
		return 0
	}
	if r2 > 1 {
		return 1
	}
	return r2
}

func fcMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func fcMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// computePoPChange returns percent change of last vs previous window average for digests/stat.
func computePoPChange(cur, prev [][2]float64) (pct float64, ok bool) {
	avg := func(pts [][2]float64) (float64, bool) {
		if len(pts) == 0 {
			return 0, false
		}
		var s float64
		for _, p := range pts {
			s += p[1]
		}
		return s / float64(len(pts)), true
	}
	a, oka := avg(cur)
	b, okb := avg(prev)
	if !oka || !okb || math.Abs(b) < 1e-12 {
		return 0, false
	}
	return (a - b) / math.Abs(b) * 100, true
}

// crossThresholdInBand 在**已经画给用户看的那条预测曲线**上找阈值穿越点。
//
// 为什么必须有这个函数：原来"预计 X 小时后触及阈值"是用 forecastCrossThreshold
// 另跑一次 robustForecast 算出来的——不同的 horizon（step*200）、不同的选型入口
// （无 learnKey、强制 auto），于是同一次回答里图上那条虚线和文字结论可以互相矛盾，
// 而真正据以创建预警事件（raiseForecastEarlyWarning）的是文字那一路。
// 用户看到的和系统据以告警的不是同一个预测，这个闭环本身就是断的。
func crossThresholdInBand(band []forecastPoint, lastVal, threshold float64) (crossAt int64, ok bool) {
	if len(band) == 0 {
		return 0, false
	}
	rising := threshold > lastVal
	for _, p := range band {
		if rising && p.Value >= threshold {
			return int64(p.TS), true
		}
		if !rising && p.Value <= threshold {
			return int64(p.TS), true
		}
	}
	return 0, false
}

// forecastDeadline helper for AI: estimate when series crosses threshold using linear trend.
func forecastCrossThreshold(hist [][2]float64, threshold float64, step int64) (crossAt int64, ok bool) {
	if len(hist) < 4 || step < 1 {
		return 0, false
	}
	fc, _, _, _, errMsg := robustForecast(hist, int64(hist[len(hist)-1][0]), step*200, step)
	if errMsg != "" || len(fc) == 0 {
		return 0, false
	}
	last := hist[len(hist)-1][1]
	rising := threshold > last
	for _, p := range fc {
		if rising && p.Value >= threshold {
			return int64(p.TS), true
		}
		if !rising && p.Value <= threshold {
			return int64(p.TS), true
		}
	}
	return 0, false
}
