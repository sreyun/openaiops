package main

import (
	"testing"

	"aiops-monitor/shared"
)

// --- Incident dedup + lifecycle ---

func TestIncidentDedupAndResolve(t *testing.T) {
	im := newIncidentManager()
	a := Alert{Type: "cpu", Level: "critical", HostID: "h1", Hostname: "web", Message: "CPU high"}
	key := "h1/cpu/"
	inc1 := im.OnAlertTransition(a, key, true)
	inc2 := im.OnAlertTransition(a, key, true) // same key must reuse
	if inc1 == 0 || inc2 == 0 || inc1 != inc2 {
		t.Fatalf("firing twice on the same key must reuse one incident")
	}
	if im.OpenCount() != 1 {
		t.Fatalf("expected 1 open incident, got %d", im.OpenCount())
	}
	if _, ok := im.Ack(inc1, "alice"); !ok {
		t.Fatal("ack failed")
	}
	im.OnAlertTransition(a, key, false) // recover
	if im.OpenCount() != 0 {
		t.Fatalf("recover must resolve the incident, open=%d", im.OpenCount())
	}
	got, _ := im.Get(inc1)
	if got.Status != "resolved" {
		t.Fatalf("status should be resolved, got %q", got.Status)
	}
}

// --- SLO math ---

func TestSLOBudget(t *testing.T) {
	cases := []struct {
		sli, target, budget, burn float64
	}{
		{100, 99, 100, 0},   // no bad → full budget
		{99.5, 99, 50, 0.5}, // half the allowance consumed
		{99, 99, 0, 1},      // exactly at target → budget gone
		{98, 99, 0, 2},      // over budget → clamped, burn 2x
		{100, 100, 100, 0},  // 100% target, perfect
	}
	for _, c := range cases {
		b, br := sloBudget(c.sli, c.target)
		if b != c.budget || br != c.burn {
			t.Errorf("sloBudget(%.2f,%.2f)=%.2f,%.2f; want %.2f,%.2f", c.sli, c.target, b, br, c.budget, c.burn)
		}
	}
}

func TestSLOComputeMetric(t *testing.T) {
	m := newSLOManager(nil)
	m.metricSamples = func(hostID string, fromTs int64) []shared.Sample {
		return []shared.Sample{
			{Timestamp: 100, Metrics: shared.Metrics{CPUPercent: 50}}, // good (<90)
			{Timestamp: 101, Metrics: shared.Metrics{CPUPercent: 95}}, // bad
			{Timestamp: 102, Metrics: shared.Metrics{CPUPercent: 80}}, // good
			{Timestamp: 103, Metrics: shared.Metrics{CPUPercent: 88}}, // good
		}
	}
	slo := SLO{SourceType: "metric", HostID: "h1", Metric: "cpu_percent", Comparator: "<", Threshold: 90, Target: 99, WindowDays: 30}
	st := m.computeStatus(slo, 200)
	if st.TotalEvents != 4 || st.GoodEvents != 3 {
		t.Fatalf("expected 3/4 good, got %d/%d", st.GoodEvents, st.TotalEvents)
	}
	if st.SLI != 75 {
		t.Fatalf("expected SLI 75, got %.2f", st.SLI)
	}
	if !st.Breaching {
		t.Fatalf("SLI 75 < target 99 must be breaching")
	}
}

func TestSLOComputeCheck(t *testing.T) {
	m := newSLOManager(nil)
	m.checkPoints = func(checkID string) []CheckPoint {
		return []CheckPoint{
			{Ts: 100, OK: true}, {Ts: 101, OK: false}, {Ts: 102, OK: true}, {Ts: 50, OK: false},
		}
	}
	slo := SLO{SourceType: "check", CheckID: "c1", Target: 99, WindowDays: 30}
	st := m.computeStatus(slo, 120) // window from = 120 - 30*86400, so all points included
	if st.TotalEvents != 4 || st.GoodEvents != 2 {
		t.Fatalf("expected 2/4 good, got %d/%d", st.GoodEvents, st.TotalEvents)
	}
}

// TestSLOComputeAPI 验证 apimon 接口作为 SLI 源：OK 率即 SLI。
func TestSLOComputeAPI(t *testing.T) {
	m := newSLOManager(nil)
	m.apiPoints = func(apiID string, fromTs int64) []APIHistPoint {
		return []APIHistPoint{{Ts: 100, OK: true}, {Ts: 101, OK: false}, {Ts: 102, OK: true}, {Ts: 103, OK: true}}
	}
	slo := SLO{SourceType: "api", APIID: "ep1", Target: 99, WindowDays: 30}
	st := m.computeStatus(slo, 120)
	if st.TotalEvents != 4 || st.GoodEvents != 3 {
		t.Fatalf("expected 3/4 good, got %d/%d", st.GoodEvents, st.TotalEvents)
	}
	if st.SLI != 75 {
		t.Fatalf("expected SLI 75%%, got %.1f", st.SLI)
	}
}

// TestSLOBurnLevel 验证多窗口多燃烧率判定：90%OK→快烧(burn 100)、99%OK→慢烧(burn 10)、100%OK→无。
func TestSLOBurnLevel(t *testing.T) {
	m := newSLOManager(nil)
	slo := SLO{SourceType: "api", APIID: "ep1", Target: 99.9, WindowDays: 30}
	set := func(okPct int) {
		m.apiPoints = func(apiID string, fromTs int64) []APIHistPoint {
			pts := make([]APIHistPoint, 100)
			for i := range pts {
				pts[i] = APIHistPoint{Ts: int64(i), OK: i >= (100 - okPct)}
			}
			return pts
		}
	}
	set(90)
	if lvl := m.burnLevel(slo, 1_000_000); lvl != "fast" {
		t.Fatalf("90%% OK 应快烧，得 %q", lvl)
	}
	set(99)
	if lvl := m.burnLevel(slo, 1_000_000); lvl != "slow" {
		t.Fatalf("99%% OK 应慢烧，得 %q", lvl)
	}
	set(100)
	if lvl := m.burnLevel(slo, 1_000_000); lvl != "" {
		t.Fatalf("100%% OK 应无燃烧，得 %q", lvl)
	}
}

// TestSLORangeAndTrend 验证自定义区间状态与趋势分桶：区间裁剪、SLI 计算、趋势点合法。
func TestSLORangeAndTrend(t *testing.T) {
	m := newSLOManager(nil)
	// 10 个点 ts=100..109，前 2 个(100/101)失败，其余成功
	m.apiPoints = func(apiID string, fromTs int64) []APIHistPoint {
		pts := []APIHistPoint{}
		for i := 0; i < 10; i++ {
			pts = append(pts, APIHistPoint{Ts: int64(100 + i), OK: i >= 2})
		}
		return pts
	}
	slo := SLO{SourceType: "api", APIID: "ep1", Target: 99}
	// 全区间 [100,109]：8/10 达标 → 80%
	if st := m.computeStatusRange(slo, 100, 109); st.TotalEvents != 10 || st.GoodEvents != 8 || st.SLI != 80 {
		t.Fatalf("全区间状态错误: %+v", st)
	}
	// 子区间 [102,109]：只含成功点 → 100%
	if st := m.computeStatusRange(slo, 102, 109); st.TotalEvents != 8 || st.SLI != 100 {
		t.Fatalf("子区间裁剪错误: %+v", st)
	}
	// 趋势分桶：非空、每桶 SLI 合法
	tr := m.sloTrend(slo, 100, 110)
	if len(tr) == 0 {
		t.Fatal("趋势应非空")
	}
	for _, p := range tr {
		if p.SLI < 0 || p.SLI > 100 || p.Total <= 0 {
			t.Fatalf("趋势点非法: %+v", p)
		}
	}
}

// --- Remediation matching + guards ---

func TestRemediationMatch(t *testing.T) {
	m := newRemediationManager(nil)
	rule := RemediationRule{Enabled: true, PlaybookID: "pb1", MatchTypes: []string{"cpu"}, MinLevel: "warning"}
	if !m.matches(rule, Alert{Type: "cpu", Level: "critical"}) {
		t.Error("cpu/critical should match")
	}
	if m.matches(rule, Alert{Type: "memory", Level: "critical"}) {
		t.Error("memory should not match a cpu-only rule")
	}
	crit := rule
	crit.MinLevel = "critical"
	if crit.matchesLevelTest("warning") {
		t.Error("warning must not meet a critical min-level")
	}
	if !m.matches(crit, Alert{Type: "cpu", Level: "critical"}) {
		t.Error("cpu/critical should meet a critical min-level")
	}
	disabled := rule
	disabled.Enabled = false
	if m.matches(disabled, Alert{Type: "cpu", Level: "critical"}) {
		t.Error("disabled rule must never match")
	}
}

// helper for the test above (keeps intent explicit).
func (r RemediationRule) matchesLevelTest(level string) bool {
	return r.MinLevel == "" || levelRank(level) >= levelRank(r.MinLevel)
}

func TestRemediationCooldownAndApproval(t *testing.T) {
	m := newRemediationManager(nil)
	launched := 0
	m.getPlaybook = func(id string) (Playbook, bool) { return Playbook{ID: "pb1", Name: "restart"}, true }
	m.resolveHost = func(id string) *Host { return &Host{ID: "h1", Hostname: "web"} }
	m.trigger = func(pb Playbook, host *Host, op string, onDone func(ok bool)) int64 {
		launched++
		onDone(true)
		return int64(launched)
	}
	a := Alert{Type: "cpu", Level: "critical", HostID: "h1", Hostname: "web"}

	// Auto-run with a cooldown: first fires, second is suppressed.
	rule := RemediationRule{ID: "r1", Enabled: true, PlaybookID: "pb1", CooldownSec: 60}
	m.evaluateRule(rule, a, 0)
	m.evaluateRule(rule, a, 0)
	if launched != 1 {
		t.Fatalf("cooldown must suppress the 2nd run, launched=%d", launched)
	}
	sawCooldown := false
	for _, run := range m.Runs() {
		if run.Status == "skipped_cooldown" {
			sawCooldown = true
		}
	}
	if !sawCooldown {
		t.Fatal("expected a skipped_cooldown run record")
	}

	// Approval gate: queued, then approved → runs.
	rule2 := RemediationRule{ID: "r2", Enabled: true, PlaybookID: "pb1", RequireApproval: true}
	m.evaluateRule(rule2, a, 0)
	var pendingID int64
	for _, run := range m.Runs() {
		if run.Status == "pending_approval" {
			pendingID = run.ID
		}
	}
	if pendingID == 0 {
		t.Fatal("expected a pending_approval run")
	}
	before := launched
	if err := m.Approve(pendingID, "alice"); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if launched != before+1 {
		t.Fatalf("approval must launch the playbook, launched %d→%d", before, launched)
	}
}

func TestRemediationRateLimit(t *testing.T) {
	m := newRemediationManager(nil)
	launched := 0
	m.getPlaybook = func(id string) (Playbook, bool) { return Playbook{ID: "pb1", Name: "x"}, true }
	m.resolveHost = func(id string) *Host { return &Host{ID: "h1", Hostname: "web"} }
	m.trigger = func(pb Playbook, host *Host, op string, onDone func(ok bool)) int64 {
		launched++
		onDone(true)
		return 1
	}
	// No cooldown, but max 2 per hour. Different hosts so cooldown-per-host never blocks.
	rule := RemediationRule{ID: "r1", Enabled: true, PlaybookID: "pb1", MaxPerHour: 2}
	for i := 0; i < 5; i++ {
		a := Alert{Type: "cpu", Level: "critical", HostID: "h" + string(rune('0'+i)), Hostname: "n"}
		m.evaluateRule(rule, a, 0)
	}
	if launched != 2 {
		t.Fatalf("rate limit should cap at 2, launched=%d", launched)
	}
}
