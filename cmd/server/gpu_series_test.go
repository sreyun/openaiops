package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"aiops-monitor/shared"
)

func gpuTestF(v float64) *float64 { return &v }
func gpuTestI(v int) *int         { return &v }

// promTextToVMExport converts Prometheus text lines (as written by
// writeGPUSeries) into VictoriaMetrics /api/v1/export NDJSON, so the write and
// read halves can be tested as one round trip without a live VM.
func promTextToVMExport(t *testing.T, text string, host string) string {
	t.Helper()
	lineRE := regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\{(.*)\} (\S+) (\d+)$`)
	lblRE := regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"`)
	var b strings.Builder
	for _, ln := range strings.Split(strings.TrimSpace(text), "\n") {
		m := lineRE.FindStringSubmatch(ln)
		if m == nil {
			t.Fatalf("unparseable prom line: %q", ln)
		}
		metric := map[string]string{"__name__": m[1]}
		for _, l := range lblRE.FindAllStringSubmatch(m[2], -1) {
			metric[l[1]] = l[2]
		}
		if host != "" {
			metric["host"] = host
		}
		v, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			t.Fatalf("bad value in %q", ln)
		}
		ts, _ := strconv.ParseInt(m[4], 10, 64)
		row, _ := json.Marshal(map[string]any{"metric": metric, "values": []float64{v}, "timestamps": []int64{ts}})
		b.Write(row)
		b.WriteByte('\n')
	}
	return b.String()
}

// 8 块同型号卡：每块卡必须是独立序列（带 gpu_index / gpu_uuid），否则同标签同时间戳会被
// VictoriaMetrics 去重成一条——这是多卡主机 GPU 趋势"只剩一条线"的根因。
func TestWriteGPUSeriesDistinctIdentityForIdenticalCards(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		writeGPUSeries(&b, `host="h1"`, shared.GPUInfo{
			Name: "NVIDIA A100-SXM4-80GB", Index: strconv.Itoa(i), UUID: fmt.Sprintf("GPU-%04d", i),
			UtilPercent: float64(10 * i),
		}, 1000)
	}
	seen := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if !strings.HasPrefix(ln, "aiops_gpu_util_percent{") {
			continue
		}
		lbl := ln[:strings.Index(ln, "}")+1]
		if seen[lbl] {
			t.Fatalf("duplicate series labels for identical cards: %s", lbl)
		}
		seen[lbl] = true
		if !strings.Contains(lbl, `gpu_index="`) || !strings.Contains(lbl, `gpu_uuid="GPU-`) {
			t.Fatalf("missing identity labels: %s", lbl)
		}
	}
	if len(seen) != 8 {
		t.Fatalf("want 8 distinct util series, got %d", len(seen))
	}
}

// 新维度只在"已知"时写：未知（nil / 0 W / 0 MHz）不写，避免把"不支持"画成一条 0 线；
// 0 是合法读数的指针字段（进程数 0、降频占比 0）必须照写。老 agent（无 index）的标签集
// 与升级前一致，也不会冒出 util_max。
func TestWriteGPUSeriesOptionalFields(t *testing.T) {
	var b strings.Builder
	writeGPUSeries(&b, `host="h1"`, shared.GPUInfo{
		Name: "A100", Index: "0", UUID: "GPU-1",
		UtilPercent: 0, UtilMax: 0,
		ProcCount: gpuTestI(0), ThrottlePower: gpuTestF(0),
	}, 1000)
	out := b.String()
	for _, want := range []string{"aiops_gpu_util_max_percent{", "aiops_gpu_proc_count{", "aiops_gpu_throttle_power_ratio{"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %s to be written:\n%s", want, out)
		}
	}
	for _, absent := range []string{"aiops_gpu_power_watts{", "aiops_gpu_fan_percent{", "aiops_gpu_sm_clock_mhz{", "aiops_gpu_ecc_uncorrected{", "aiops_gpu_mem_util_percent{"} {
		if strings.Contains(out, absent) {
			t.Errorf("unknown field %s must not be written:\n%s", absent, out)
		}
	}

	b.Reset()
	writeGPUSeries(&b, `host="h1"`, shared.GPUInfo{Name: "GPU0", UtilPercent: 30}, 1000) // 老 agent
	old := b.String()
	if strings.Contains(old, "gpu_index=") || strings.Contains(old, "gpu_uuid=") || strings.Contains(old, "util_max") {
		t.Fatalf("legacy GPU must keep the pre-upgrade label set and series:\n%s", old)
	}
	if !strings.Contains(old, `aiops_gpu_util_percent{host="h1",gpu="GPU0"} 30 1000`) {
		t.Fatalf("legacy series format changed:\n%s", old)
	}
}

// 写 → 读回 全链路：每个维度都要按卡原样回来（这次扩展最容易出的问题就是"写进去了，
// 读回来少字段 / 并错卡"）。
func TestGPUSeriesRoundTripThroughVMExport(t *testing.T) {
	in := []shared.GPUInfo{
		{
			Name: "NVIDIA H100", Index: "0", UUID: "GPU-aaa", UtilPercent: 41.5, UtilMax: 97,
			MemUsed: 40 << 30, MemFree: 40 << 30, MemTotal: 80 << 30, MemPercent: 50, Temp: 71,
			MemUtilPercent: gpuTestF(33), PowerW: 512.5, PowerLimitW: 700, FanPercent: gpuTestF(0),
			SMClockMHz: 1755, SMClockMaxMHz: 1980,
			ThrottleThermal: gpuTestF(0), ThrottlePower: gpuTestF(0.667), ThrottleHW: gpuTestF(0),
			ECCUncorrected: gpuTestF(2), ProcCount: gpuTestI(3),
		},
		{Name: "NVIDIA H100", Index: "1", UUID: "GPU-bbb", UtilPercent: 5, UtilMax: 12, MemTotal: 80 << 30, Temp: 40, ProcCount: gpuTestI(0)},
	}
	var b strings.Builder
	for _, g := range in {
		writeGPUSeries(&b, `host="h1"`, g, 100000)
	}
	samples := parseVMExport(strings.NewReader(promTextToVMExport(t, b.String(), "")))
	if len(samples) != 1 || len(samples[0].GPUs) != 2 {
		t.Fatalf("want 1 sample with 2 GPUs, got %+v", samples)
	}
	got := samples[0].GPUs
	g0, g1 := got[0], got[1]
	if g0.Index != "0" || g0.UUID != "GPU-aaa" || g1.Index != "1" || g1.UUID != "GPU-bbb" {
		t.Fatalf("identity/order wrong: %+v / %+v", g0, g1)
	}
	if g0.UtilPercent != 41.5 || g0.UtilMax != 97 || g0.Temp != 71 || g0.MemPercent != 50 || g0.MemUsed != 40<<30 {
		t.Errorf("core fields lost: %+v", g0)
	}
	if g0.PowerW != 512.5 || g0.PowerLimitW != 700 || g0.SMClockMHz != 1755 || g0.SMClockMaxMHz != 1980 {
		t.Errorf("power/clock lost: %+v", g0)
	}
	if g0.MemUtilPercent == nil || *g0.MemUtilPercent != 33 || g0.FanPercent == nil || *g0.FanPercent != 0 {
		t.Errorf("mem-bw / fan lost: %+v", g0)
	}
	if g0.ThrottlePower == nil || *g0.ThrottlePower != 0.667 || g0.ECCUncorrected == nil || *g0.ECCUncorrected != 2 {
		t.Errorf("throttle / ECC lost: %+v", g0)
	}
	if g0.ProcCount == nil || *g0.ProcCount != 3 || g1.ProcCount == nil || *g1.ProcCount != 0 {
		t.Errorf("proc_count lost: %+v / %+v", g0.ProcCount, g1.ProcCount)
	}
	if g1.PowerW != 0 || g1.FanPercent != nil || g1.ECCUncorrected != nil {
		t.Errorf("GPU1 must not inherit GPU0's optional fields: %+v", g1)
	}
}

// 序号按数值排：GPU2 在 GPU10 前面（字符串排序会把 10 排到 2 前，颜色随之错位）。
func TestVMExportParseGPUSortsByNumericIndex(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"A100","gpu_index":"10"},"values":[1],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"A100","gpu_index":"2"},"values":[2],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"A100","gpu_index":"0"},"values":[3],"timestamps":[100000]}`
	s := parseVMExport(strings.NewReader(nd))
	if len(s) != 1 || len(s[0].GPUs) != 3 {
		t.Fatalf("want 3 GPUs, got %+v", s)
	}
	got := []string{s[0].GPUs[0].Index, s[0].GPUs[1].Index, s[0].GPUs[2].Index}
	if strings.Join(got, ",") != "0,2,10" {
		t.Fatalf("GPU order = %v, want 0,2,10", got)
	}
}

// hold-forward 只覆盖本格到达的字段，而且绝不能原地改写上一格共享的指针。
func TestMergeGPUHoldForwardKeepsPointersImmutable(t *testing.T) {
	fanPrev := gpuTestF(40)
	prev := []shared.GPUInfo{{Name: "A100", Index: "0", UUID: "GPU-1", UtilPercent: 10, Temp: 60, FanPercent: fanPrev, PowerW: 300}}
	cur := []shared.GPUInfo{{Name: "A100", Index: "0", UUID: "GPU-1", UtilPercent: 80, FanPercent: gpuTestF(70)}}
	fields := map[string]map[string]bool{"uuid:GPU-1": {"util": true, "fan": true}}
	out := mergeGPUHoldForward(prev, cur, fields)
	if len(out) != 1 {
		t.Fatalf("want 1 GPU, got %+v", out)
	}
	g := out[0]
	if g.UtilPercent != 80 || g.Temp != 60 || g.PowerW != 300 || g.FanPercent == nil || *g.FanPercent != 70 {
		t.Fatalf("merge wrong: %+v", g)
	}
	if *fanPrev != 40 || *prev[0].FanPercent != 40 {
		t.Fatalf("previous cell's pointer was mutated in place: %v", *fanPrev)
	}
}

// 掉卡 / 换卡 / 升级后身份变化：旧身份最多续 histDiskHoldMiss 格，不能平直续到窗口末尾。
func TestFinalizeHistJoinPrunesVanishedGPU(t *testing.T) {
	byTs := map[int64]*histJoinCell{}
	old := shared.GPUInfo{Name: "A100"} // 升级前：按型号的老身份
	applyHistJoinGPU(byTs, 0, "aiops_gpu_util_percent", old, 50)
	for i := int64(1); i <= 10; i++ {
		ts := i * 30
		applyHistJoinMetric(byTs, ts, "aiops_cpu_percent", "", "", "", "", 10)
		applyHistJoinGPU(byTs, ts, "aiops_gpu_util_percent", shared.GPUInfo{Name: "A100", Index: "0"}, 60)
	}
	out := finalizeHistJoin(byTs)
	last := out[len(out)-1]
	if len(last.GPUs) != 1 || last.GPUs[0].Index != "0" {
		t.Fatalf("stale legacy GPU identity carried to window end: %+v", last.GPUs)
	}
	// 短暂缺席（≤ histDiskHoldMiss 格）仍应续接，不让曲线闪断。
	if len(out[2].GPUs) != 2 {
		t.Fatalf("short absence should still hold forward, got %+v", out[2].GPUs)
	}
}

// RAM 降采样层（raw→1m→5m）也必须按身份聚合，同型号多卡不能被平均成一块。
func TestAggregateGPUWindowByIdentity(t *testing.T) {
	mk := func(u0, u1 float64, thr float64) shared.Sample {
		return shared.Sample{Metrics: shared.Metrics{GPUs: []shared.GPUInfo{
			{Name: "A100", Index: "0", UUID: "GPU-0", UtilPercent: u0, UtilMax: u0 + 5, ThrottlePower: gpuTestF(thr), ThrottleReasons: []string{"sw_power_cap"}, ProcCount: gpuTestI(2)},
			{Name: "A100", Index: "1", UUID: "GPU-1", UtilPercent: u1},
		}}}
	}
	out := aggregateGPUWindow([]shared.Sample{mk(20, 90, 0), mk(40, 100, 1)})
	if len(out) != 2 {
		t.Fatalf("identical cards merged: %+v", out)
	}
	if out[0].UtilPercent != 30 || out[0].UtilMax != 45 || out[1].UtilPercent != 95 || out[1].UtilMax != 100 {
		t.Fatalf("avg/peak wrong: %+v / %+v", out[0], out[1])
	}
	if out[0].ThrottlePower == nil || *out[0].ThrottlePower != 0.5 || out[1].ThrottlePower != nil {
		t.Fatalf("throttle ratio wrong: %+v / %+v", out[0].ThrottlePower, out[1].ThrottlePower)
	}
	if out[0].ProcCount == nil || *out[0].ProcCount != 2 || len(out[0].ThrottleReasons) != 1 {
		t.Fatalf("levels / reasons wrong: %+v", out[0])
	}
}

// 进程表只随 Latest；历史层只留进程数（与 ProcessNames 同口径，控制内存）。
func TestStoreKeepsGPUProcessesOnlyInLatest(t *testing.T) {
	st := NewStore()
	st.RegisterHost("h1", "node-1", "fp-aaa")
	rep := shared.Report{HostID: "h1", Hostname: "node-1", Fingerprint: "fp-aaa", Metrics: shared.Metrics{
		GPUs: []shared.GPUInfo{{Name: "A100", Index: "0", UUID: "GPU-0", ProcCount: gpuTestI(1),
			Processes: []shared.GPUProcess{{PID: 42, Name: "python", MemUsed: 1 << 30}}}},
	}}
	h, ok := st.UpsertAuthenticated(rep, "fp-aaa")
	if !ok {
		t.Fatal("upsert failed")
	}
	if len(h.Latest.GPUs) != 1 || len(h.Latest.GPUs[0].Processes) != 1 {
		t.Fatalf("Latest must keep the process table: %+v", h.Latest.GPUs)
	}
	samples, _ := st.GetSamples("h1")
	if len(samples) == 0 || len(samples[len(samples)-1].GPUs) != 1 {
		t.Fatalf("history sample missing GPU: %+v", samples)
	}
	hg := samples[len(samples)-1].GPUs[0]
	if len(hg.Processes) != 0 || hg.ProcCount == nil || *hg.ProcCount != 1 {
		t.Fatalf("history must drop process list but keep count: %+v", hg)
	}
}

// 同型号两块卡同时过热：必须是两条 Scope 不同的告警，不能后一块覆盖前一块。
func TestGPUAlertsDistinctScopesForIdenticalCards(t *testing.T) {
	th := DefaultThresholds()
	now := time.Now().Unix()
	h := mkHost("h1", "node-1", now, shared.Metrics{CPUCores: 8, GPUs: []shared.GPUInfo{
		{Name: "NVIDIA A100", Index: "0", UUID: "GPU-0", UtilPercent: 99},
		{Name: "NVIDIA A100", Index: "1", UUID: "GPU-1", UtilPercent: 99},
	}})
	scopes := map[string]bool{}
	for _, a := range Evaluate([]*Host{h}, th) {
		if a.Type == "gpu" {
			scopes[a.Scope] = true
		}
	}
	if !scopes["GPU0 · NVIDIA A100"] || !scopes["GPU1 · NVIDIA A100"] {
		t.Fatalf("identical cards must alert separately, got scopes %v", scopes)
	}
}

func TestHostHistoryIncludesGPUDimensions(t *testing.T) {
	have := map[string]bool{}
	for _, n := range hostHistoryMetricNames {
		have[n] = true
	}
	for _, want := range []string{"aiops_gpu_util_max_percent", "aiops_gpu_power_watts", "aiops_gpu_mem_util_percent",
		"aiops_gpu_sm_clock_mhz", "aiops_gpu_throttle_power_ratio", "aiops_gpu_proc_count", "aiops_gpus_count"} {
		if !have[want] {
			t.Errorf("history allowlist missing %s", want)
		}
	}
	fam := vmNamesForMetricKeys([]string{"gpu"})
	if len(fam) < len(gpuFieldSpecs) {
		t.Errorf("gpu family selects %d names, want ≥%d", len(fam), len(gpuFieldSpecs))
	}
}
