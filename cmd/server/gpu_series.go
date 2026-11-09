package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"aiops-monitor/shared"
)

// GPU 时序：一张表同时驱动写入（vm push）、读回（history join）和断点续接（hold-forward）。
//
// 以前这三处各写一份字段 switch，加一个维度要改三处、漏一处就出现"写进去了但图上没有"
// 或"读回来被下一帧零值覆盖"。现在加维度只改这张表。
//
// 身份标签：gpu=<型号名>（沿用，PromQL 按型号过滤仍可用）+ gpu_index + gpu_uuid。只有
// gpu=<型号名> 时，8 块同型号卡在同一时间戳写出 8 条**同标签**样本，VictoriaMetrics 去重
// 后只剩 1 条——多卡主机的 GPU 趋势一直只是"随机某一块卡"。老 agent 不报 index/uuid，
// 标签集与升级前完全一致，原有序列不受影响。

type gpuFieldSpec struct {
	metric string                                 // VM 指标名
	short  string                                 // hold-forward 用的字段短名
	get    func(g shared.GPUInfo) (float64, bool) // 写入：值 + 是否已知（未知不写，避免把"不支持"画成 0）
	set    func(g *shared.GPUInfo, v float64)     // 读回：必须整体替换指针，绝不原地改（GPUInfo 被浅拷贝共享）
}

func gpuAlways(f func(g shared.GPUInfo) float64) func(shared.GPUInfo) (float64, bool) {
	return func(g shared.GPUInfo) (float64, bool) { return f(g), true }
}

func gpuPositive(f func(g shared.GPUInfo) float64) func(shared.GPUInfo) (float64, bool) {
	return func(g shared.GPUInfo) (float64, bool) { v := f(g); return v, v > 0 }
}

func gpuPtr(f func(g shared.GPUInfo) *float64) func(shared.GPUInfo) (float64, bool) {
	return func(g shared.GPUInfo) (float64, bool) {
		if p := f(g); p != nil {
			return *p, true
		}
		return 0, false
	}
}

func gpuF64p(v float64) *float64 { return &v }

var gpuFieldSpecs = []gpuFieldSpec{
	// —— 升级前就有的 6 个字段：保持"总是写"的语义不变 ——
	{"aiops_gpu_util_percent", "util",
		gpuAlways(func(g shared.GPUInfo) float64 { return g.UtilPercent }),
		func(g *shared.GPUInfo, v float64) { g.UtilPercent = v }},
	{"aiops_gpu_temp_c", "temp",
		gpuAlways(func(g shared.GPUInfo) float64 { return g.Temp }),
		func(g *shared.GPUInfo, v float64) { g.Temp = v }},
	{"aiops_gpu_mem_percent", "mem_pct",
		gpuAlways(func(g shared.GPUInfo) float64 { return g.MemPercent }),
		func(g *shared.GPUInfo, v float64) { g.MemPercent = v }},
	{"aiops_gpu_mem_used_bytes", "mem_used",
		gpuAlways(func(g shared.GPUInfo) float64 { return float64(g.MemUsed) }),
		func(g *shared.GPUInfo, v float64) { g.MemUsed = uint64(v) }},
	{"aiops_gpu_mem_free_bytes", "mem_free",
		gpuAlways(func(g shared.GPUInfo) float64 { return float64(g.MemFree) }),
		func(g *shared.GPUInfo, v float64) { g.MemFree = uint64(v) }},
	{"aiops_gpu_mem_total_bytes", "mem_total",
		gpuAlways(func(g shared.GPUInfo) float64 { return float64(g.MemTotal) }),
		func(g *shared.GPUInfo, v float64) { g.MemTotal = uint64(v) }},

	// —— 新维度：只在已知时写 ——
	// util_max：新 agent（带 index）即使空闲为 0 也写；老 agent 没有窗口峰值，不写，免得
	// 画出一条"峰值低于均值"的假线。
	{"aiops_gpu_util_max_percent", "util_max",
		func(g shared.GPUInfo) (float64, bool) { return g.UtilMax, g.UtilMax > 0 || g.Index != "" },
		func(g *shared.GPUInfo, v float64) { g.UtilMax = v }},
	{"aiops_gpu_mem_util_percent", "mem_util",
		gpuPtr(func(g shared.GPUInfo) *float64 { return g.MemUtilPercent }),
		func(g *shared.GPUInfo, v float64) { g.MemUtilPercent = gpuF64p(v) }},
	{"aiops_gpu_power_watts", "power",
		gpuPositive(func(g shared.GPUInfo) float64 { return g.PowerW }),
		func(g *shared.GPUInfo, v float64) { g.PowerW = v }},
	{"aiops_gpu_power_limit_watts", "power_limit",
		gpuPositive(func(g shared.GPUInfo) float64 { return g.PowerLimitW }),
		func(g *shared.GPUInfo, v float64) { g.PowerLimitW = v }},
	{"aiops_gpu_fan_percent", "fan",
		gpuPtr(func(g shared.GPUInfo) *float64 { return g.FanPercent }),
		func(g *shared.GPUInfo, v float64) { g.FanPercent = gpuF64p(v) }},
	{"aiops_gpu_sm_clock_mhz", "sm_clock",
		gpuPositive(func(g shared.GPUInfo) float64 { return g.SMClockMHz }),
		func(g *shared.GPUInfo, v float64) { g.SMClockMHz = v }},
	{"aiops_gpu_sm_clock_max_mhz", "sm_clock_max",
		gpuPositive(func(g shared.GPUInfo) float64 { return g.SMClockMaxMHz }),
		func(g *shared.GPUInfo, v float64) { g.SMClockMaxMHz = v }},
	{"aiops_gpu_throttle_thermal_ratio", "thr_thermal",
		gpuPtr(func(g shared.GPUInfo) *float64 { return g.ThrottleThermal }),
		func(g *shared.GPUInfo, v float64) { g.ThrottleThermal = gpuF64p(v) }},
	{"aiops_gpu_throttle_power_ratio", "thr_power",
		gpuPtr(func(g shared.GPUInfo) *float64 { return g.ThrottlePower }),
		func(g *shared.GPUInfo, v float64) { g.ThrottlePower = gpuF64p(v) }},
	{"aiops_gpu_throttle_hw_ratio", "thr_hw",
		gpuPtr(func(g shared.GPUInfo) *float64 { return g.ThrottleHW }),
		func(g *shared.GPUInfo, v float64) { g.ThrottleHW = gpuF64p(v) }},
	{"aiops_gpu_ecc_uncorrected", "ecc_ue",
		gpuPtr(func(g shared.GPUInfo) *float64 { return g.ECCUncorrected }),
		func(g *shared.GPUInfo, v float64) { g.ECCUncorrected = gpuF64p(v) }},
	{"aiops_gpu_proc_count", "proc_count",
		func(g shared.GPUInfo) (float64, bool) {
			if g.ProcCount == nil {
				return 0, false
			}
			return float64(*g.ProcCount), true
		},
		func(g *shared.GPUInfo, v float64) { n := int(v); g.ProcCount = &n }},
}

var (
	gpuSpecByMetric = func() map[string]*gpuFieldSpec {
		m := make(map[string]*gpuFieldSpec, len(gpuFieldSpecs))
		for i := range gpuFieldSpecs {
			m[gpuFieldSpecs[i].metric] = &gpuFieldSpecs[i]
		}
		return m
	}()
	gpuSpecByShort = func() map[string]*gpuFieldSpec {
		m := make(map[string]*gpuFieldSpec, len(gpuFieldSpecs))
		for i := range gpuFieldSpecs {
			m[gpuFieldSpecs[i].short] = &gpuFieldSpecs[i]
		}
		return m
	}()
)

// gpuHistoryMetricNames is every per-GPU series plus the host-level count —
// the GPU family for host-history reads.
func gpuHistoryMetricNames() []string {
	out := make([]string, 0, len(gpuFieldSpecs)+1)
	for _, s := range gpuFieldSpecs {
		out = append(out, s.metric)
	}
	return append(out, "aiops_gpus_count")
}

// gpuSeriesLabels returns the label suffix identifying one GPU's series.
func gpuSeriesLabels(g shared.GPUInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, `,gpu="%s"`, lblEsc(g.Name))
	if g.Index != "" {
		fmt.Fprintf(&b, `,gpu_index="%s"`, lblEsc(g.Index))
	}
	if g.UUID != "" {
		fmt.Fprintf(&b, `,gpu_uuid="%s"`, lblEsc(g.UUID))
	}
	return b.String()
}

// writeGPUSeries appends one GPU's samples in Prometheus text format.
func writeGPUSeries(b *strings.Builder, hostLbl string, g shared.GPUInfo, ms int64) {
	gl := hostLbl + gpuSeriesLabels(g)
	for _, s := range gpuFieldSpecs {
		if v, ok := s.get(g); ok {
			fmt.Fprintf(b, "%s{%s} %g %d\n", s.metric, gl, v, ms)
		}
	}
}

// gpuIdentFromLabels rebuilds a GPU's identity from VM series labels.
func gpuIdentFromLabels(labels map[string]string) shared.GPUInfo {
	name := labels["gpu"]
	if name == "" {
		name = "GPU"
	}
	return shared.GPUInfo{Name: name, Index: labels["gpu_index"], UUID: labels["gpu_uuid"]}
}

// setSampleGPU folds one aiops_gpu_* series value into the sample's GPUs (by
// identity, not by name) and returns the field's short name ("" if unknown).
func setSampleGPU(s *shared.Sample, id shared.GPUInfo, metric string, val float64) string {
	spec := gpuSpecByMetric[metric]
	if spec == nil {
		return ""
	}
	if id.Name == "" {
		id.Name = "GPU"
	}
	key := id.Key()
	idx := -1
	for i := range s.GPUs {
		if s.GPUs[i].Key() == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.GPUs = append(s.GPUs, shared.GPUInfo{Name: id.Name, Index: id.Index, UUID: id.UUID})
		idx = len(s.GPUs) - 1
	}
	spec.set(&s.GPUs[idx], val)
	return spec.short
}

// sortGPUs orders GPUs by numeric index (GPU2 before GPU10), then name, then
// key — stable across requests so chart colours don't shuffle.
func sortGPUs(gs []shared.GPUInfo) {
	if len(gs) < 2 {
		return
	}
	sort.SliceStable(gs, func(a, b int) bool {
		ia, ib := gpuIndexNum(gs[a].Index), gpuIndexNum(gs[b].Index)
		if ia != ib {
			return ia < ib
		}
		if gs[a].Name != gs[b].Name {
			return gs[a].Name < gs[b].Name
		}
		return gs[a].Key() < gs[b].Key()
	})
}

// gpuIndexNum parses a GPU index for ordering; unknown sorts last.
func gpuIndexNum(s string) int {
	if s == "" {
		return 1 << 30
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 1 << 30
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 1 << 30
		}
	}
	return n
}

// gpusWithoutProcesses returns a copy of gs with per-process lists dropped —
// history tiers keep only ProcCount (Latest alone carries the process table).
func gpusWithoutProcesses(gs []shared.GPUInfo) []shared.GPUInfo {
	if len(gs) == 0 {
		return gs
	}
	out := make([]shared.GPUInfo, len(gs))
	copy(out, gs)
	for i := range out {
		out[i].Processes = nil
	}
	return out
}

// aggregateGPUWindow folds a window of samples (raw → 1m, 1m → 5m) into one
// GPUInfo per GPU identity. Activity metrics are averaged over the samples
// that carried them; UtilMax keeps the window peak; levels and counters
// (limits, max clock, ECC, process count) take the newest value; throttle
// ratios average correctly as "fraction of time throttled".
func aggregateGPUWindow(window []shared.Sample) []shared.GPUInfo {
	type sumN struct {
		sum float64
		n   int
	}
	add := func(a *sumN, v float64) { a.sum += v; a.n++ }
	mean := func(a sumN) *float64 {
		if a.n == 0 {
			return nil
		}
		return gpuF64p(a.sum / float64(a.n))
	}
	type acc struct {
		last                                   shared.GPUInfo
		n                                      float64
		util, memUsed, memFree, memTotal, temp float64
		utilMax                                float64
		memUtil, fan, thrT, thrP, thrH         sumN
		power, clk                             sumN
		reasons                                []string
	}
	order := []string{}
	m := map[string]*acc{}
	for _, s := range window {
		for _, g := range s.GPUs {
			k := g.Key()
			a := m[k]
			if a == nil {
				a = &acc{}
				m[k] = a
				order = append(order, k)
			}
			a.last = g
			a.n++
			a.util += g.UtilPercent
			a.memUsed += float64(g.MemUsed)
			a.memFree += float64(g.MemFree)
			a.memTotal += float64(g.MemTotal)
			a.temp += g.Temp
			if pk := max(g.UtilMax, g.UtilPercent); pk > a.utilMax {
				a.utilMax = pk
			}
			if g.MemUtilPercent != nil {
				add(&a.memUtil, *g.MemUtilPercent)
			}
			if g.FanPercent != nil {
				add(&a.fan, *g.FanPercent)
			}
			if g.ThrottleThermal != nil {
				add(&a.thrT, *g.ThrottleThermal)
			}
			if g.ThrottlePower != nil {
				add(&a.thrP, *g.ThrottlePower)
			}
			if g.ThrottleHW != nil {
				add(&a.thrH, *g.ThrottleHW)
			}
			if g.PowerW > 0 {
				add(&a.power, g.PowerW)
			}
			if g.SMClockMHz > 0 {
				add(&a.clk, g.SMClockMHz)
			}
			for _, r := range g.ThrottleReasons {
				if !slices.Contains(a.reasons, r) {
					a.reasons = append(a.reasons, r)
				}
			}
		}
	}
	out := make([]shared.GPUInfo, 0, len(order))
	for _, k := range order {
		a := m[k]
		gi := shared.GPUInfo{
			Name:            a.last.Name,
			Index:           a.last.Index,
			UUID:            a.last.UUID,
			UtilPercent:     a.util / a.n,
			UtilMax:         a.utilMax,
			MemUsed:         uint64(a.memUsed / a.n),
			MemFree:         uint64(a.memFree / a.n),
			MemTotal:        uint64(a.memTotal / a.n),
			Temp:            a.temp / a.n,
			MemUtilPercent:  mean(a.memUtil),
			PowerLimitW:     a.last.PowerLimitW,
			FanPercent:      mean(a.fan),
			SMClockMaxMHz:   a.last.SMClockMaxMHz,
			ThrottleThermal: mean(a.thrT),
			ThrottlePower:   mean(a.thrP),
			ThrottleHW:      mean(a.thrH),
			ThrottleReasons: a.reasons,
			ECCUncorrected:  a.last.ECCUncorrected,
			ProcCount:       a.last.ProcCount,
		}
		if p := mean(a.power); p != nil {
			gi.PowerW = *p
		}
		if c := mean(a.clk); c != nil {
			gi.SMClockMHz = *c
		}
		if gi.MemTotal > 0 {
			gi.MemPercent = float64(gi.MemUsed) / float64(gi.MemTotal) * 100
		}
		out = append(out, gi)
	}
	return out
}
