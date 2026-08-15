package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 预测自我学习：台账 → 与实测对比 → 校准因子 + AI 偏差记忆 → 后续预测自动纠偏。

const (
	forecastCalibFile   = "forecast_calib.json"
	forecastLedgerMax   = 400
	forecastCalibMaxKey = 200
)

type forecastLedger struct {
	ID          string          `json:"id"`
	Key         string          `json:"key"`
	Method      string          `json:"method"`
	Expr        string          `json:"expr,omitempty"`
	Predicted   []forecastPoint `json:"predicted"`
	PredictedAt int64           `json:"predicted_at"`
	HorizonSec  int64           `json:"horizon_sec"`
	Step        int64           `json:"step"`
	Anchor      float64         `json:"anchor"`
	Evaluated   bool            `json:"evaluated"`
}

type forecastCalibration struct {
	Key             string             `json:"key"`
	BiasOffset      float64            `json:"bias_offset"`
	Scale           float64            `json:"scale"`
	MethodScores    map[string]float64 `json:"method_scores"`
	EvalCount       int                `json:"eval_count"`
	LastMAPE        float64            `json:"last_mape"`
	PreferredMethod string             `json:"preferred_method"`
	UpdatedAt       int64              `json:"updated_at"`
}

type forecastLearnStore struct {
	mu       sync.Mutex
	path     string
	Ledgers  []forecastLedger                `json:"ledgers"`
	Calibs   map[string]*forecastCalibration `json:"calibs"`
	dirty    bool
	lastAI   int64
	lastSave time.Time
}

var fcLearn = &forecastLearnStore{Calibs: map[string]*forecastCalibration{}}

type forecastEvalResult struct {
	key, method     string
	mape, bias      float64
	scale           float64
	n               int
	predMid, actMid float64
}

func forecastMetricKey(parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:10])
}

func (s *Server) initForecastLearn() {
	if s.cfg == nil || s.cfg.path == "" {
		return
	}
	fcLearn.mu.Lock()
	fcLearn.path = filepath.Join(filepath.Dir(s.cfg.path), forecastCalibFile)
	fcLearn.mu.Unlock()
	fcLearn.load()
	go s.runForecastLearnLoop(10 * time.Minute)
}

func (st *forecastLearnStore) load() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.Calibs == nil {
		st.Calibs = map[string]*forecastCalibration{}
	}
	if st.path == "" {
		return
	}
	raw, err := os.ReadFile(st.path)
	if err != nil || len(raw) == 0 {
		return
	}
	var disk forecastLearnStore
	if json.Unmarshal(raw, &disk) != nil {
		return
	}
	st.Ledgers = disk.Ledgers
	if disk.Calibs != nil {
		st.Calibs = disk.Calibs
	}
}

// saveLockedThrottled 用于**高频、可丢**的写入路径（记台账）。调用方必须持有 st.mu。
//
// 台账在每一次 AI 预测调用时都会追加一条，而 saveLocked 会把整份 Ledgers+Calibs
// 重新序列化落盘（400 条台账 × 24 个采样点，约 1 MB），还是在锁里同步做的。接线之前
// 这条路径根本没被调用过，所以代价看不出来；现在它活了，就变成了 AI 请求路径上的一次
// 兆字节级同步写。台账只是学习用的账本，掉几十秒不影响正确性，节流到 15 秒即可。
// 校准结果（evaluateForecastLedgers）仍走无节流的 saveLocked，那才是真正要落住的东西。
func (st *forecastLearnStore) saveLockedThrottled(minGap time.Duration) {
	if !st.dirty {
		return
	}
	if !st.lastSave.IsZero() && time.Since(st.lastSave) < minGap {
		return
	}
	st.saveLocked()
}

func (st *forecastLearnStore) saveLocked() {
	if st.path == "" || !st.dirty {
		return
	}
	// Marshal 而不是 MarshalIndent：这是机器读写的账本，不是给人看的配置，
	// 缩进白白多出约 40% 的体积与序列化时间。
	raw, err := json.Marshal(map[string]any{
		"ledgers": st.Ledgers,
		"calibs":  st.Calibs,
	})
	if err != nil {
		return
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, st.path)
	st.dirty = false
	st.lastSave = time.Now()
}

func (st *forecastLearnStore) getCalib(key string) *forecastCalibration {
	st.mu.Lock()
	defer st.mu.Unlock()
	c := st.Calibs[key]
	if c == nil {
		return nil
	}
	cp := *c
	if c.MethodScores != nil {
		cp.MethodScores = make(map[string]float64, len(c.MethodScores))
		for k, v := range c.MethodScores {
			cp.MethodScores[k] = v
		}
	}
	return &cp
}

func sampleForecastBand(band []forecastPoint, maxN int) []forecastPoint {
	if len(band) <= maxN {
		out := make([]forecastPoint, len(band))
		copy(out, band)
		return out
	}
	out := make([]forecastPoint, 0, maxN)
	for i := 0; i < maxN; i++ {
		idx := i * (len(band) - 1) / (maxN - 1)
		out = append(out, band[idx])
	}
	return out
}

func seriesStdFromBand(band []forecastPoint) float64 {
	if len(band) == 0 {
		return 0
	}
	m := 0.0
	for _, p := range band {
		m += p.Value
	}
	m /= float64(len(band))
	ss := 0.0
	for _, p := range band {
		d := p.Value - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(band)))
}

func applyForecastCalibration(key string, band []forecastPoint, anchor float64) ([]forecastPoint, *forecastCalibration) {
	cal := fcLearn.getCalib(key)
	if cal == nil || len(band) == 0 || cal.EvalCount < 1 {
		return band, cal
	}
	scale := cal.Scale
	if scale <= 0.2 || scale > 3 {
		scale = 1
	}
	bias := cal.BiasOffset
	maxAbsBias := math.Abs(anchor)*0.25 + seriesStdFromBand(band)*0.5 + 1
	if bias > maxAbsBias {
		bias = maxAbsBias
	} else if bias < -maxAbsBias {
		bias = -maxAbsBias
	}
	out := make([]forecastPoint, len(band))
	for i, p := range band {
		v := anchor + (p.Value-anchor)*scale + bias
		lo := anchor + (p.Lo-anchor)*scale + bias
		hi := anchor + (p.Hi-anchor)*scale + bias
		if lo > v {
			lo = v
		}
		if hi < v {
			hi = v
		}
		out[i] = forecastPoint{TS: p.TS, Value: v, Lo: lo, Hi: hi}
	}
	return out, cal
}

func learnAdjustCandidateScore(key, method string, score float64) float64 {
	cal := fcLearn.getCalib(key)
	if cal == nil || cal.EvalCount < 2 || len(cal.MethodScores) == 0 {
		return score
	}
	sc, ok := cal.MethodScores[method]
	if !ok {
		return score
	}
	boost := 1.0 - math.Min(0.25, sc/120)
	if boost < 0.75 {
		boost = 0.75
	}
	return score * boost
}

func (s *Server) noteForecastLedger(key, method, expr string, band []forecastPoint, nowTS, horizon, step int64, anchor float64) {
	if key == "" || len(band) == 0 || nowTS <= 0 {
		return
	}
	fcLearn.mu.Lock()
	defer fcLearn.mu.Unlock()
	fcLearn.Ledgers = append(fcLearn.Ledgers, forecastLedger{
		ID: fmt.Sprintf("%s-%d", key, nowTS), Key: key, Method: method, Expr: strings.TrimSpace(expr),
		Predicted: sampleForecastBand(band, 24), PredictedAt: nowTS,
		HorizonSec: horizon, Step: step, Anchor: anchor,
	})
	if len(fcLearn.Ledgers) > forecastLedgerMax {
		fcLearn.Ledgers = fcLearn.Ledgers[len(fcLearn.Ledgers)-forecastLedgerMax:]
	}
	fcLearn.dirty = true
	fcLearn.saveLockedThrottled(15 * time.Second)
}

func (s *Server) finalizeForecastWithLearning(key, method, expr string, band []forecastPoint, nowTS, horizon, step int64, anchor float64) ([]forecastPoint, string, int) {
	if len(band) == 0 {
		return band, method, 0
	}
	calibrated, cal := applyForecastCalibration(key, band, anchor)
	s.noteForecastLedger(key, method, expr, calibrated, nowTS, horizon, step, anchor)
	n := 0
	label := method
	if cal != nil && cal.EvalCount > 0 {
		n = cal.EvalCount
		label = method + fmt.Sprintf("+learn%d", n)
	}
	return calibrated, label, n
}

func (s *Server) runForecastLearnLoop(every time.Duration) {
	if every < time.Minute {
		every = time.Minute
	}
	time.Sleep(45 * time.Second)
	s.evaluateForecastLedgers()
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		s.evaluateForecastLedgers()
	}
}

func (s *Server) evaluateForecastLedgers() {
	now := time.Now().Unix()
	fcLearn.mu.Lock()
	ledgers := append([]forecastLedger(nil), fcLearn.Ledgers...)
	fcLearn.mu.Unlock()

	var results []forecastEvalResult
	changed := false
	for i := range ledgers {
		L := &ledgers[i]
		if L.Evaluated || len(L.Predicted) == 0 || strings.TrimSpace(L.Expr) == "" {
			continue
		}
		wait := L.HorizonSec * 2 / 5
		if wait < 300 {
			wait = 300
		}
		if now < L.PredictedAt+wait {
			continue
		}
		er, ok := s.evaluateOneLedger(L)
		if !ok {
			continue
		}
		results = append(results, er)
		L.Evaluated = true
		changed = true
	}
	if !changed {
		return
	}

	fcLearn.mu.Lock()
	fcLearn.Ledgers = ledgers
	for _, er := range results {
		c := fcLearn.Calibs[er.key]
		if c == nil {
			c = &forecastCalibration{Key: er.key, Scale: 1, MethodScores: map[string]float64{}}
			fcLearn.Calibs[er.key] = c
		}
		if c.MethodScores == nil {
			c.MethodScores = map[string]float64{}
		}
		alpha := 0.35
		if c.EvalCount == 0 {
			c.BiasOffset, c.Scale, c.LastMAPE = er.bias, er.scale, er.mape
		} else {
			c.BiasOffset = (1-alpha)*c.BiasOffset + alpha*er.bias
			c.Scale = (1-alpha)*c.Scale + alpha*er.scale
			c.LastMAPE = (1-alpha)*c.LastMAPE + alpha*er.mape
		}
		if c.Scale < 0.4 {
			c.Scale = 0.4
		}
		if c.Scale > 2.5 {
			c.Scale = 2.5
		}
		baseMethod := strings.Split(er.method, "+")[0]
		c.MethodScores[baseMethod] += math.Max(0, 40-er.mape)
		c.EvalCount++
		c.UpdatedAt = now
		bestM, bestS := "", math.Inf(-1)
		for m, sc := range c.MethodScores {
			if sc > bestS {
				bestS, bestM = sc, m
			}
		}
		c.PreferredMethod = bestM
	}
	if len(fcLearn.Calibs) > forecastCalibMaxKey {
		type kv struct {
			k string
			t int64
		}
		arr := make([]kv, 0, len(fcLearn.Calibs))
		for k, c := range fcLearn.Calibs {
			arr = append(arr, kv{k, c.UpdatedAt})
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i].t < arr[j].t })
		for i := 0; i < len(arr)-forecastCalibMaxKey; i++ {
			delete(fcLearn.Calibs, arr[i].k)
		}
	}
	fcLearn.dirty = true
	fcLearn.saveLocked()
	fcLearn.mu.Unlock()

	for _, er := range results {
		s.recordForecastOutcome(er.key, er.predMid, er.actMid, 12)
	}
	s.maybeAIForecastCalibrationSummary(results)
	slog.Info("预测自学习已更新校准", "evaluated", len(results))
}

func (s *Server) evaluateOneLedger(L *forecastLedger) (forecastEvalResult, bool) {
	var er forecastEvalResult
	from, to := L.PredictedAt, L.PredictedAt+L.HorizonSec
	if to <= from {
		return er, false
	}
	step := L.Step
	if step < 15 {
		step = 15
	}
	mat, okQ := s.dashRangeSeries("", L.Expr, from, to, step)
	if !okQ || len(mat) == 0 || len(mat[0].Points) < 3 {
		return er, false
	}
	actual := mat[0].Points
	var sumAbs, sumBias, sumPredDev, sumActDev, predSum, actSum float64
	n := 0
	for _, p := range L.Predicted {
		av, aok := nearestPoint(actual, p.TS, float64(step)*3)
		if !aok {
			continue
		}
		den := math.Abs(av)
		if den < 1e-6 {
			den = 1
		}
		sumAbs += math.Abs(av-p.Value) / den
		sumBias += av - p.Value
		sumPredDev += p.Value - L.Anchor
		sumActDev += av - L.Anchor
		predSum += p.Value
		actSum += av
		n++
	}
	if n < 2 {
		return er, false
	}
	er.key, er.method = L.Key, L.Method
	er.mape = (sumAbs / float64(n)) * 100
	er.bias = sumBias / float64(n)
	er.n = n
	er.predMid = predSum / float64(n)
	er.actMid = actSum / float64(n)
	if math.Abs(sumPredDev) > 1e-6 {
		er.scale = sumActDev / sumPredDev
	} else {
		er.scale = 1
	}
	if er.scale < 0.3 || er.scale > 3 {
		er.scale = 1
	}
	return er, true
}

func nearestPoint(pts [][2]float64, ts, maxDelta float64) (float64, bool) {
	if len(pts) == 0 {
		return 0, false
	}
	best := pts[0]
	bestD := math.Abs(pts[0][0] - ts)
	for _, p := range pts[1:] {
		d := math.Abs(p[0] - ts)
		if d < bestD {
			bestD, best = d, p
		}
	}
	if bestD > maxDelta {
		return 0, false
	}
	return best[1], true
}

func (s *Server) maybeAIForecastCalibrationSummary(results []forecastEvalResult) {
	if len(results) == 0 {
		return
	}
	fcLearn.mu.Lock()
	last := fcLearn.lastAI
	fcLearn.mu.Unlock()
	now := time.Now().Unix()
	if now-last < 6*3600 {
		return
	}
	cfg := s.cfg.AIConfig()
	if !cfg.Enabled || cfg.Endpoint == "" {
		return
	}
	var b strings.Builder
	b.WriteString("监控预测自学习误差评估，请给出≤120字中文调校要点：\n")
	for i, er := range results {
		if i >= 8 {
			break
		}
		b.WriteString(fmt.Sprintf("- %s method=%s MAPE≈%.1f%% bias≈%.3f scale≈%.2f pred≈%.2f actual≈%.2f\n",
			er.key, er.method, er.mape, er.bias, er.scale, er.predMid, er.actMid))
	}
	out, err := aiCompleteOpts(cfg,
		"你是时序预测调校专家。根据误差给出后续预测修正建议（偏高/偏低、模型偏好、突发注意）。只输出调校要点。",
		b.String(),
		aiCallOpts{DisableThinking: true, MaxTokens: 400, Timeout: 60 * time.Second},
	)
	if err != nil || strings.TrimSpace(out) == "" {
		return
	}
	s.rememberAI("forecast_bias", "forecast_learn:"+time.Now().Format("20060102"),
		"【预测自学习调校】\n"+strings.TrimSpace(out))
	fcLearn.mu.Lock()
	fcLearn.lastAI = now
	fcLearn.mu.Unlock()
}
