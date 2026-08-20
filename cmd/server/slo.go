package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// ============================================================================
// SLO — Service Level Objectives + error budgets.
//
// An SLO measures an SLI (a "good ratio") over a rolling window and compares it
// to a target. Two SLI sources are supported with zero new dependencies:
//   - "check":  a synthetic check's up ratio (OK points / total points)
//   - "metric": the fraction of samples where a host metric stays in a good
//               band (e.g. cpu_percent < 90)
// The remaining error budget and burn rate are derived from the SLI, and a
// burned-through budget automatically opens an incident.
// ============================================================================

// SLO defines one objective.
type SLO struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Enabled    bool    `json:"enabled"`
	SourceType string  `json:"source_type"`          // check | api | metric | promql
	CheckID    string  `json:"check_id,omitempty"`   // source_type=check
	APIID      string  `json:"api_id,omitempty"`     // source_type=api（apimon 接口 ID）
	HostID     string  `json:"host_id,omitempty"`    // source_type=metric
	Metric     string  `json:"metric,omitempty"`     // cpu_percent | mem_percent | disk_percent | load1 | ...
	Comparator string  `json:"comparator,omitempty"` // < | <= | > | >=  (defines the "good" band)
	Threshold  float64 `json:"threshold,omitempty"`
	GoodQuery  string  `json:"good_query,omitempty"`  // source_type=promql：达标(好)事件计数 PromQL，用 $window 占位滚动窗口
	TotalQuery string  `json:"total_query,omitempty"` // source_type=promql：总事件计数 PromQL（$window 占位）
	Target     float64 `json:"target"`                // objective %, e.g. 99.9
	WindowDays int     `json:"window_days"`           // rolling window
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
}

// SLOStatus is an SLO plus its computed health.
type SLOStatus struct {
	SLO
	SLI         float64 `json:"sli"`          // achieved good ratio over the window (%)
	ErrorBudget float64 `json:"error_budget"` // remaining budget (% of the allowance)
	BurnRate    float64 `json:"burn_rate"`    // consumed / allowed (>1 = over budget pace)
	GoodEvents  int64   `json:"good_events"`
	TotalEvents int64   `json:"total_events"`
	Breaching   bool    `json:"breaching"` // SLI < target
	Healthy     bool    `json:"healthy"`
	BurnState   string  `json:"burn_state"` // "" | "fast" | "slow"（多窗口燃烧档，供看板即时呈现）
}

// sampleMetric extracts a named metric from a sample; ok=false if unknown.
func sampleMetric(s shared.Sample, name string) (float64, bool) {
	switch name {
	case "cpu_percent":
		return s.CPUPercent, true
	case "mem_percent":
		return s.MemPercent, true
	case "disk_percent":
		return s.DiskPercent, true
	case "load1":
		return s.Load1, true
	case "load5":
		return s.Load5, true
	case "load15":
		return s.Load15, true
	case "diskio_util":
		return s.DiskIOUtilPercent, true
	case "net_recv_rate":
		return s.NetRecvRate, true
	case "net_sent_rate":
		return s.NetSentRate, true
	}
	return 0, false
}

// satisfies reports whether v is in the "good" band defined by comparator+threshold.
func satisfies(v float64, cmp string, thr float64) bool {
	switch cmp {
	case "<":
		return v < thr
	case "<=":
		return v <= thr
	case ">":
		return v > thr
	case ">=":
		return v >= thr
	case "==":
		return v == thr
	}
	return false
}

// sloBudget derives the remaining error budget (%) and burn rate from an SLI and
// target. allowed-bad = 100-target; consumed-bad = 100-sli. A 100% target means
// any bad event exhausts the budget.
func sloBudget(sli, target float64) (budgetRemaining, burnRate float64) {
	allowedBad := 100 - target
	actualBad := 100 - sli
	if allowedBad <= 0 {
		if actualBad <= 0 {
			return 100, 0
		}
		return 0, actualBad // effectively infinite pace; report the magnitude
	}
	budgetRemaining = (allowedBad - actualBad) / allowedBad * 100
	if budgetRemaining < 0 {
		budgetRemaining = 0
	}
	if budgetRemaining > 100 {
		budgetRemaining = 100
	}
	burnRate = actualBad / allowedBad
	return
}

// sloManager evaluates SLOs and raises incidents when a budget is exhausted.
type sloManager struct {
	mu  sync.Mutex
	cfg *ConfigStore
	// data sources (injected during wiring)
	metricSamples func(hostID string, fromTs int64) []shared.Sample
	checkPoints   func(checkID string) []CheckPoint
	apiPoints     func(apiID string, fromTs int64) []APIHistPoint
	promScalar    func(query string) (float64, bool)                              // source_type=promql：即时标量查询（注入，内部走 VM）
	promRange     func(query string, from, to, step int64) ([]vmRangePoint, bool) // source_type=promql：区间查询（趋势用）
	incidents     *incidentManager
	burning       map[string]bool // sloID -> currently in a burn incident
}

func newSLOManager(cfg *ConfigStore) *sloManager {
	return &sloManager{cfg: cfg, burning: map[string]bool{}}
}

// 多窗口多燃烧率阈值（Google SRE Workbook）：长短窗口双双超阈才判定，避免瞬时抖动误报。
const (
	sloFastBurnThr = 14.4 // 快烧：约 1h 消耗 2% 的 30d 预算 → 紧急
	sloSlowBurnThr = 6.0  // 慢烧：约 6h 消耗 5% 的 30d 预算 → 警告
)

// goodTotal 统计一个 SLO 在 [fromTs, now] 窗口内的达标/总事件数，覆盖全部 SLI 来源。
func (m *sloManager) goodTotal(s SLO, now, fromTs int64) (good, total int64) {
	switch s.SourceType {
	case "check":
		if m.checkPoints != nil {
			for _, p := range m.checkPoints(s.CheckID) {
				if p.Ts < fromTs {
					continue
				}
				total++
				if p.OK {
					good++
				}
			}
		}
	case "api":
		if m.apiPoints != nil {
			for _, p := range m.apiPoints(s.APIID, fromTs) {
				total++
				if p.OK {
					good++
				}
			}
		}
	case "metric":
		if m.metricSamples != nil {
			for _, smp := range m.metricSamples(s.HostID, fromTs) {
				v, ok := sampleMetric(smp, s.Metric)
				if !ok {
					continue
				}
				total++
				if satisfies(v, s.Comparator, s.Threshold) {
					good++
				}
			}
		}
	case "promql":
		// good/total 计数查询：把 $window 占位替换为当前窗口时长（如 "300s"），瞬时聚合。
		if m.promScalar != nil && s.TotalQuery != "" {
			win := now - fromTs
			if win < 1 {
				win = 1
			}
			winStr := strconv.FormatInt(win, 10) + "s"
			tv, ok := m.promScalar(strings.ReplaceAll(s.TotalQuery, "$window", winStr))
			if !ok || tv <= 0 {
				return
			}
			gv := tv // GoodQuery 留空则视作全部达标（仅用总量衡量“有无数据”）
			if s.GoodQuery != "" {
				gv, _ = m.promScalar(strings.ReplaceAll(s.GoodQuery, "$window", winStr))
			}
			total = int64(tv + 0.5)
			good = int64(gv + 0.5)
			if good > total {
				good = total
			}
			if good < 0 {
				good = 0
			}
		}
	}
	return
}

// burnLevel 判定当前燃烧档："fast"(快烧,紧急) | "slow"(慢烧,警告) | ""(正常)，长短窗口双确认。
func (m *sloManager) burnLevel(s SLO, now int64) string {
	burn := func(dur int64) (float64, bool) {
		good, total := m.goodTotal(s, now, now-dur)
		if total == 0 {
			return 0, false
		}
		_, br := sloBudget(float64(good)/float64(total)*100, s.Target)
		return br, true
	}
	b5, ok5 := burn(300)
	b30, ok30 := burn(1800)
	b1h, _ := burn(3600)
	b6h, _ := burn(21600)
	if ok5 && b1h >= sloFastBurnThr && b5 >= sloFastBurnThr {
		return "fast"
	}
	if ok30 && b6h >= sloSlowBurnThr && b30 >= sloSlowBurnThr {
		return "slow"
	}
	return ""
}

// computeStatus evaluates one SLO against its window.
func (m *sloManager) computeStatus(s SLO, now int64) SLOStatus {
	win := s.WindowDays
	if win < 1 {
		win = 30
	}
	good, total := m.goodTotal(s, now, now-int64(win)*86400)
	sli := 100.0
	if total > 0 {
		sli = float64(good) / float64(total) * 100
	}
	budget, burn := sloBudget(sli, s.Target)
	return SLOStatus{
		SLO: s, SLI: sli, ErrorBudget: budget, BurnRate: burn,
		GoodEvents: good, TotalEvents: total,
		Breaching: total > 0 && sli < s.Target,
		Healthy:   total == 0 || sli >= s.Target,
		BurnState: m.burnLevel(s, now),
	}
}

// sloPoint 是 SLI 计算的一个原子事件点（时间 + 是否达标），统一各来源用于自定义区间与趋势。
type sloPoint struct {
	Ts int64
	OK bool
}

// rangePoints 取一个 SLO 在 [fromTs, toTs] 内的全部事件点，覆盖 check/api/metric 三种来源。
func (m *sloManager) rangePoints(s SLO, fromTs, toTs int64) []sloPoint {
	var out []sloPoint
	switch s.SourceType {
	case "check":
		if m.checkPoints != nil {
			for _, p := range m.checkPoints(s.CheckID) {
				if p.Ts >= fromTs && p.Ts <= toTs {
					out = append(out, sloPoint{p.Ts, p.OK})
				}
			}
		}
	case "api":
		if m.apiPoints != nil {
			for _, p := range m.apiPoints(s.APIID, fromTs) {
				if p.Ts >= fromTs && p.Ts <= toTs {
					out = append(out, sloPoint{p.Ts, p.OK})
				}
			}
		}
	case "metric":
		if m.metricSamples != nil {
			for _, smp := range m.metricSamples(s.HostID, fromTs) {
				if smp.Timestamp < fromTs || smp.Timestamp > toTs {
					continue
				}
				v, ok := sampleMetric(smp, s.Metric)
				if !ok {
					continue
				}
				out = append(out, sloPoint{smp.Timestamp, satisfies(v, s.Comparator, s.Threshold)})
			}
		}
	case "promql":
		// 区间趋势：按 step 桶用 query_range 取 good/total，每桶达标率≥目标即记为达标点。
		// （即时看板用 goodTotal 的按事件加权口径；此处为“每桶是否达标”的趋势视图。）
		if m.promRange != nil && s.TotalQuery != "" {
			step := (toTs - fromTs) / 120
			if step < 30 {
				step = 30
			}
			winStr := strconv.FormatInt(step, 10) + "s"
			tot, ok := m.promRange(strings.ReplaceAll(s.TotalQuery, "$window", winStr), fromTs, toTs, step)
			if !ok {
				return out
			}
			goodMap := map[int64]float64{}
			if s.GoodQuery != "" {
				if gp, ok := m.promRange(strings.ReplaceAll(s.GoodQuery, "$window", winStr), fromTs, toTs, step); ok {
					for _, p := range gp {
						goodMap[p.Ts] = p.Val
					}
				}
			} else {
				for _, p := range tot {
					goodMap[p.Ts] = p.Val
				}
			}
			passRatio := s.Target / 100.0
			for _, tp := range tot {
				if tp.Val <= 0 {
					continue // 该桶无事件，跳过（不把空窗画成 0%）
				}
				g, ok := goodMap[tp.Ts]
				if !ok {
					// good 序列缺该桶时不要当成 0（假失败）；跳过该点
					continue
				}
				out = append(out, sloPoint{tp.Ts, g/tp.Val >= passRatio-1e-9})
			}
		}
	}
	return out
}

// computeStatusRange 计算某 SLO 在任意 [fromTs, toTs] 区间的状态（供用户自定义时间范围查询）。
func (m *sloManager) computeStatusRange(s SLO, fromTs, toTs int64) SLOStatus {
	var good, total int64
	for _, p := range m.rangePoints(s, fromTs, toTs) {
		total++
		if p.OK {
			good++
		}
	}
	sli := 100.0
	if total > 0 {
		sli = float64(good) / float64(total) * 100
	}
	budget, burn := sloBudget(sli, s.Target)
	return SLOStatus{
		SLO: s, SLI: sli, ErrorBudget: budget, BurnRate: burn,
		GoodEvents: good, TotalEvents: total,
		Breaching: total > 0 && sli < s.Target,
		Healthy:   total == 0 || sli >= s.Target,
	}
}

// sloTrendPoint 是趋势曲线的一个点：某时间桶内的可用率。
type sloTrendPoint struct {
	Ts    int64   `json:"timestamp"`
	SLI   float64 `json:"sli"`
	Good  int64   `json:"good"`
	Total int64   `json:"total"`
}

// sloTrend 把 [fromTs, toTs] 均分为约 60 个桶，逐桶现算可用率，得到 SLI 随时间的趋势曲线。
// 无数据的桶跳过（曲线自然断点），避免把空窗画成 0%。
func (m *sloManager) sloTrend(s SLO, fromTs, toTs int64) []sloTrendPoint {
	if toTs <= fromTs {
		return nil
	}
	const buckets = 60
	bw := (toTs - fromTs) / buckets
	if bw < 1 {
		bw = 1
	}
	good := make([]int64, buckets+1)
	total := make([]int64, buckets+1)
	for _, p := range m.rangePoints(s, fromTs, toTs) {
		bi := int((p.Ts - fromTs) / bw)
		if bi < 0 {
			bi = 0
		}
		if bi > buckets {
			bi = buckets
		}
		total[bi]++
		if p.OK {
			good[bi]++
		}
	}
	out := []sloTrendPoint{}
	for i := 0; i <= buckets; i++ {
		if total[i] == 0 {
			continue
		}
		out = append(out, sloTrendPoint{
			Ts:   fromTs + int64(i)*bw + bw/2, // 桶中点
			SLI:  float64(good[i]) / float64(total[i]) * 100,
			Good: good[i], Total: total[i],
		})
	}
	return out
}

// Evaluate returns the current status of every SLO (for the API/UI).
func (m *sloManager) Evaluate() []SLOStatus {
	if m.cfg == nil {
		return nil
	}
	now := time.Now().Unix()
	slos := m.cfg.SLOs()
	out := make([]SLOStatus, 0, len(slos))
	for _, s := range slos {
		out = append(out, m.computeStatus(s, now))
	}
	return out
}

// EvaluateAndAlert is the ticker body: exhausting an SLO's error budget opens an
// incident (deduped per SLO); recovering budget resolves it.
func (m *sloManager) EvaluateAndAlert() {
	if m.cfg == nil || m.incidents == nil {
		return
	}
	now := time.Now().Unix()
	for _, s := range m.cfg.SLOs() {
		if !s.Enabled {
			continue
		}
		st := m.computeStatus(s, now)
		key := "slo/" + s.ID
		exhausted := st.TotalEvents > 0 && st.ErrorBudget <= 0
		breaching := st.Breaching || exhausted
		m.mu.Lock()
		wasBurning := m.burning[s.ID]
		if exhausted && !wasBurning {
			m.burning[s.ID] = true
			m.mu.Unlock()
			m.incidents.raise(key, Tz("slo.incident_title", s.Name, st.SLI, s.Target),
				"critical", "slo", "", "", "slo")
			m.ensureSLOFreezeWindow(s, st)
		} else if !exhausted && wasBurning {
			delete(m.burning, s.ID)
			m.mu.Unlock()
			m.incidents.resolveByKey(key, Tz("slo.incident_recovered", s.Name))
		} else {
			m.mu.Unlock()
			if breaching {
				m.ensureSLOFreezeWindow(s, st)
			}
		}
		// 多窗口多燃烧率告警：早于「预算耗尽」的提前量（快烧=紧急 / 慢烧=警告）。
		// raise / resolveByKey 按 key 幂等，故无需额外状态跟踪。
		burnKey := "slo-burn/" + s.ID
		switch st.BurnState {
		case "fast":
			m.incidents.raise(burnKey, Tz("slo.burn_fast", s.Name), "critical", "slo", "", "", "slo")
		case "slow":
			m.incidents.raise(burnKey, Tz("slo.burn_slow", s.Name), "warning", "slo", "", "", "slo")
		default:
			m.incidents.resolveByKey(burnKey, Tz("slo.burn_recovered", s.Name))
		}
	}
}

// ensureSLOFreezeWindow upserts a temporary freeze ChangeWindow tied to the SLO.
func (m *sloManager) ensureSLOFreezeWindow(s SLO, st SLOStatus) {
	if m == nil || m.cfg == nil {
		return
	}
	ttlSec := int64(6 * 3600)
	if v := strings.TrimSpace(os.Getenv("AIOPS_SLO_FREEZE_TTL_SEC")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			ttlSec = n
		}
	}
	now := time.Now().Unix()
	id := "slo-freeze-" + s.ID
	w := ChangeWindow{
		ID: id, Name: "SLO 错误预算冻结：" + s.Name,
		Start: now, End: now + ttlSec, Freeze: true,
		Note:   fmt.Sprintf("自动创建：SLI=%.3f target=%.3f budget=%.2f%%", st.SLI, s.Target, st.ErrorBudget),
		SLOIDs: []string{s.ID}, UpdatedAt: now,
	}
	_, _ = m.cfg.UpsertChangeWindow(w)
}

// exportBurning / importBurning bridge the SLO burning state to PostgreSQL.
func (m *sloManager) exportBurning() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.burning))
	for k, v := range m.burning {
		out[k] = v
	}
	return out
}

func (m *sloManager) importBurning(burning map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.burning = make(map[string]bool, len(burning))
	for k, v := range burning {
		m.burning[k] = v
	}
}

// validateSLO normalizes and checks an SLO before persisting.
func validateSLO(s *SLO) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return fmt.Errorf("%s", Tz("slo.name_required"))
	}
	if s.Target <= 0 || s.Target > 100 {
		return fmt.Errorf("%s", Tz("slo.bad_target"))
	}
	if s.WindowDays < 1 {
		s.WindowDays = 30
	}
	switch s.SourceType {
	case "check":
		if s.CheckID == "" {
			return fmt.Errorf("%s", Tz("slo.check_required"))
		}
	case "api":
		if s.APIID == "" {
			return fmt.Errorf("%s", Tz("slo.api_required"))
		}
	case "metric":
		if s.HostID == "" || s.Metric == "" {
			return fmt.Errorf("%s", Tz("slo.metric_required"))
		}
		if !satisfiesValidComparator(s.Comparator) {
			return fmt.Errorf("%s", Tz("slo.bad_comparator"))
		}
	case "promql":
		s.TotalQuery = strings.TrimSpace(s.TotalQuery)
		s.GoodQuery = strings.TrimSpace(s.GoodQuery)
		if s.TotalQuery == "" {
			return fmt.Errorf("%s", Tz("slo.total_query_required"))
		}
	default:
		return fmt.Errorf("%s", Tz("slo.bad_source"))
	}
	return nil
}

func satisfiesValidComparator(c string) bool {
	switch c {
	case "<", "<=", ">", ">=", "==":
		return true
	}
	return false
}
