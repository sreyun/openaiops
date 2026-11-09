package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// ============================================================================
// GPU 采集：后台多次采样 + 上报窗口聚合
// ============================================================================
//
// 旧实现每 30s 在上报时同步跑一次 nvidia-smi 并缓存 30s，有三个问题：
//
//  1. 单点快照。nvidia-smi 的 utilization.gpu 只是最近 1/6~1 秒的占用率，训练/推理这种
//     脉冲型负载在 30s 里打一个点，趋势图是噪声，峰值几乎必然被漏掉。
//  2. 缓存 TTL 与上报周期同为 30s，ticker 抖动几毫秒就会命中旧缓存，于是每隔一轮上报
//     一份过期数据（趋势图上成对的"台阶"）；把 report_interval 调到 10s 时更糟，同一个
//     值连续上报 3 次。
//  3. 采集在上报协程里同步执行，驱动卡死时最多阻塞 4s。
//
// 现在由一个后台协程按 gpuSampleInterval（默认 report_interval/3，夹在 5~15s）采样，
// 保留最近若干帧；Collect() 只取"最近一个上报窗口"内的帧做聚合（利用率取均值 + 峰值、
// 温度取峰值、功耗与频率取均值、降频取占比），不再阻塞上报，也不再有缓存相位问题。

const (
	gpuIdleBackoff   = 60 * time.Second // 没探到 GPU 时放慢探测（绝大多数主机没有 GPU）
	gpuProcTopN      = 8                // 每卡最多上报的进程数（按显存降序）
	gpuProbeTimeout  = 4 * time.Second
	gpuMinSampleIntv = 2 * time.Second
	gpuMaxSampleIntv = 300 * time.Second
)

// runCmdTimeout is like runCmd but with a caller-chosen timeout and it surfaces
// the error. The Hyper-V collector needs both: powershell.exe cold-start plus
// Get-VM on a busy host can take well over runCmd's fixed 4s, and a non-zero exit
// (cmdlet missing, access denied) must be distinguishable from "no VMs". Uses
// Output() (not CombinedOutput) so stdout stays clean JSON; on failure the
// *exec.ExitError still carries stderr for diagnostics.
//
//lint:ignore U1000 used by hyperv_windows.go; CI analyses linux only
func runCmdTimeout(d time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// runNvidiaSmi runs nvidia-smi and returns stdout and stderr separately: on a
// successful run only stdout is CSV (stderr can carry infoROM warnings), but
// on failure the "Field ... is not a valid field" diagnostic may be on either.
// The hard 4s timeout is essential: nvidia-smi is known to hang for tens of
// seconds (or indefinitely) when the driver/GPU is wedged.
func runNvidiaSmi(args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gpuProbeTimeout)
	defer cancel()
	var o, e bytes.Buffer
	cmd := exec.CommandContext(ctx, "nvidia-smi", args...)
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	return o.String(), e.String(), err
}

// ---------------------------------------------------------------------------
// nvidia-smi：字段协商 + 按列名解析
// ---------------------------------------------------------------------------

// nvidiaSmiFields 是期望采集的列。不同驱动版本支持的字段不一样（老驱动没有
// clocks_throttle_reasons.active，个别型号不认 utilization.memory），nvidia-smi 碰到
// 一个不认识的字段会让**整条查询**失败——固定列表要么只能保守到最老的驱动，要么在老
// 驱动上一个数都采不到。所以按列名解析，并在失败时剔除报错字段重试（见 negotiate）。
var nvidiaSmiFields = []string{
	"index", "uuid", "name",
	"utilization.gpu", "utilization.memory",
	"memory.used", "memory.total", "memory.free",
	"temperature.gpu",
	"power.draw", "power.limit",
	"fan.speed",
	"clocks.sm", "clocks.max.sm",
	"clocks_throttle_reasons.active",
	"ecc.errors.uncorrected.volatile.total",
}

// nvidiaSmiLegacyFields 是升级前一直在用的查询，协商彻底失败时退回它。
var nvidiaSmiLegacyFields = []string{"name", "utilization.gpu", "memory.used", "memory.total", "temperature.gpu", "memory.free"}

var nvsmiInvalidFieldRE = regexp.MustCompile(`Field "([^"]+)" is not a valid field to query`)

type nvsmiRunner func(args ...string) (stdout, stderr string, err error)

// nvsmiQuerier remembers the negotiated field list so the retry cost is paid
// once per agent lifetime, not every sample.
type nvsmiQuerier struct {
	mu     sync.Mutex
	fields []string // nil = not negotiated yet
	run    nvsmiRunner
}

var nvsmi = &nvsmiQuerier{run: runNvidiaSmi}

func (q *nvsmiQuerier) query() []gpuReading {
	q.mu.Lock()
	fields := q.fields
	q.mu.Unlock()
	if fields == nil {
		fields = nvidiaSmiFields
	}
	for attempt := 0; attempt <= len(nvidiaSmiFields); attempt++ {
		out, errOut, err := q.run("--query-gpu="+strings.Join(fields, ","), "--format=csv,noheader,nounits")
		if err == nil {
			q.mu.Lock()
			q.fields = fields
			q.mu.Unlock()
			return parseNvidiaSmiRows(out, fields)
		}
		m := nvsmiInvalidFieldRE.FindStringSubmatch(errOut + "\n" + out)
		if m == nil {
			// 未安装 / 无设备 / 超时：不是字段问题，别动已协商的列表。
			return nil
		}
		next := make([]string, 0, len(fields))
		for _, f := range fields {
			if f != m[1] {
				next = append(next, f)
			}
		}
		if len(next) == len(fields) || len(next) == 0 {
			break // 报错字段不在列表里，协商无从继续
		}
		fields = next
	}
	out, _, err := q.run("--query-gpu="+strings.Join(nvidiaSmiLegacyFields, ","), "--format=csv,noheader,nounits")
	if err != nil {
		return nil
	}
	q.mu.Lock()
	q.fields = nvidiaSmiLegacyFields
	q.mu.Unlock()
	return parseNvidiaSmiRows(out, nvidiaSmiLegacyFields)
}

// nvidiaSmiReadings probes NVIDIA GPUs (Linux and Windows).
func nvidiaSmiReadings() []gpuReading { return nvsmi.query() }

// NVML clocks_throttle_reasons bits (nvml.h nvmlClocksThrottleReason*).
const (
	thrSwPowerCap   = 0x4
	thrHwSlowdown   = 0x8
	thrSwThermal    = 0x20
	thrHwThermal    = 0x40
	thrHwPowerBrake = 0x80

	thrThermalMask = thrSwThermal | thrHwThermal
	thrPowerMask   = thrSwPowerCap | thrHwPowerBrake
)

// throttleReasonNames lists the reasons that mean "clocks are being held below
// what the workload asks for". GpuIdle / ApplicationsClocksSetting / SyncBoost /
// DisplayClockSetting are normal operating states, not throttling.
var throttleReasonNames = []struct {
	bit  uint64
	name string
}{
	{thrSwPowerCap, "sw_power_cap"},
	{thrHwPowerBrake, "hw_power_brake"},
	{thrSwThermal, "sw_thermal"},
	{thrHwThermal, "hw_thermal"},
	{thrHwSlowdown, "hw_slowdown"},
}

// gpuReading is one instantaneous probe of one GPU.
type gpuReading struct {
	info   shared.GPUInfo
	mask   uint64 // throttle reasons bitmask
	maskOK bool
}

// optNum parses a nvidia-smi / sysfs numeric cell. "[N/A]", "[Not Supported]",
// "[Unknown Error]" and empty cells are "unknown" (ok=false) — not 0.
func optNum(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "[") || strings.EqualFold(s, "N/A") {
		return 0, false
	}
	if i := strings.IndexByte(s, ' '); i >= 0 { // stray unit token: "37 %"
		s = s[:i]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func f64p(v float64) *float64 { return &v }

// parseNvidiaSmiRows parses CSV (noheader,nounits) rows whose columns are
// `fields`. A GPU name containing a comma would add columns; those are folded
// back into the name column so the remaining columns stay aligned.
func parseNvidiaSmiRows(out string, fields []string) []gpuReading {
	col := make(map[string]int, len(fields))
	for i, f := range fields {
		col[f] = i
	}
	nameIdx, hasName := col["name"]
	var rows []gpuReading
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		parts := strings.Split(ln, ",")
		if len(parts) < len(fields) {
			continue // 告警行 / 截断行
		}
		if extra := len(parts) - len(fields); extra > 0 {
			if !hasName {
				continue
			}
			merged := strings.Join(parts[nameIdx:nameIdx+extra+1], ",")
			parts = append(append(append([]string{}, parts[:nameIdx]...), merged), parts[nameIdx+extra+1:]...)
		}
		cell := func(f string) (string, bool) {
			i, ok := col[f]
			if !ok {
				return "", false
			}
			return strings.TrimSpace(parts[i]), true
		}
		num := func(f string) (float64, bool) {
			c, ok := cell(f)
			if !ok {
				return 0, false
			}
			return optNum(c)
		}

		var r gpuReading
		g := &r.info
		if c, _ := cell("name"); c != "" {
			g.Name = c
		} else {
			g.Name = "GPU"
		}
		if c, ok := cell("index"); ok {
			if _, isNum := optNum(c); isNum {
				g.Index = c
			}
		} else {
			// 退回老查询（无 index 列）时，nvidia-smi 的行序就是 index 序：用行号补上身份，
			// 否则同型号多卡又会按名字并成一块。
			g.Index = strconv.Itoa(len(rows))
		}
		if c, ok := cell("uuid"); ok && strings.HasPrefix(c, "GPU-") {
			g.UUID = c
		}
		if v, ok := num("utilization.gpu"); ok {
			g.UtilPercent = round1(v)
		}
		if v, ok := num("utilization.memory"); ok {
			g.MemUtilPercent = f64p(round1(v))
		}
		usedMiB, okU := num("memory.used")
		totalMiB, okT := num("memory.total")
		if okU {
			g.MemUsed = uint64(usedMiB) * 1024 * 1024
		}
		if okT {
			g.MemTotal = uint64(totalMiB) * 1024 * 1024
		}
		if okU && okT && totalMiB >= usedMiB {
			g.MemFree = uint64(totalMiB-usedMiB) * 1024 * 1024 // fallback: total-used
		}
		if okU && okT && totalMiB > 0 {
			g.MemPercent = round1(usedMiB / totalMiB * 100)
		}
		if v, ok := num("memory.free"); ok && v > 0 {
			g.MemFree = uint64(v) * 1024 * 1024 // prefer nvidia-smi's memory.free
		}
		if v, ok := num("temperature.gpu"); ok {
			g.Temp = round1(v)
		}
		if v, ok := num("power.draw"); ok && v > 0 {
			g.PowerW = round1(v)
		}
		if v, ok := num("power.limit"); ok && v > 0 {
			g.PowerLimitW = round1(v)
		}
		if v, ok := num("fan.speed"); ok {
			g.FanPercent = f64p(round1(v))
		}
		if v, ok := num("clocks.sm"); ok && v > 0 {
			g.SMClockMHz = v
		}
		if v, ok := num("clocks.max.sm"); ok && v > 0 {
			g.SMClockMaxMHz = v
		}
		if v, ok := num("ecc.errors.uncorrected.volatile.total"); ok {
			g.ECCUncorrected = f64p(v)
		}
		if c, ok := cell("clocks_throttle_reasons.active"); ok {
			if m, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(c), "0x"), 16, 64); err == nil {
				r.mask, r.maskOK = m, true
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// nvidiaComputeApps lists compute processes per GPU UUID. ok=false means the
// query itself failed (tool missing / unsupported); an empty map with ok=true
// means "no processes", which is a real reading (ProcCount=0).
func nvidiaComputeApps() (map[string][]shared.GPUProcess, bool) {
	out, _, err := runNvidiaSmi("--query-compute-apps=gpu_uuid,pid,process_name,used_memory", "--format=csv,noheader,nounits")
	if err != nil {
		return nil, false
	}
	return parseComputeApps(out), true
}

func parseComputeApps(out string) map[string][]shared.GPUProcess {
	res := map[string][]shared.GPUProcess{}
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		parts := strings.Split(ln, ",")
		if len(parts) < 4 {
			continue
		}
		uuid := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(uuid, "GPU-") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || pid <= 0 {
			continue
		}
		name := strings.TrimSpace(strings.Join(parts[2:len(parts)-1], ",")) // 进程路径里的逗号
		if strings.HasPrefix(name, "[") {
			name = "" // [Insufficient Permissions] / [N/A]
		}
		if name != "" {
			name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
		}
		if len(name) > 64 {
			name = name[:64]
		}
		var mem uint64
		if v, ok := optNum(parts[len(parts)-1]); ok && v > 0 {
			mem = uint64(v) * 1024 * 1024
		}
		res[uuid] = append(res[uuid], shared.GPUProcess{PID: pid, Name: name, MemUsed: mem})
	}
	return res
}

// ---------------------------------------------------------------------------
// 窗口聚合
// ---------------------------------------------------------------------------

type gpuFrame struct {
	at   time.Time
	gpus []gpuReading
}

// aggregateGPUFrames folds the frames of one report window into one GPUInfo
// per GPU. Only GPUs present in the newest frame are reported: a card that fell
// off the bus mid-window must disappear now, not be papered over with stale
// readings. Levels (VRAM, limits, ECC counter, fan) take the newest value;
// activity (util, mem bandwidth, power, clocks) is averaged; util additionally
// keeps the peak and temperature takes the peak; throttling becomes a duty
// fraction.
func aggregateGPUFrames(frames []gpuFrame) []shared.GPUInfo {
	if len(frames) == 0 {
		return nil
	}
	latest := frames[len(frames)-1]
	if len(latest.gpus) == 0 {
		return nil
	}
	byKey := map[string][]gpuReading{}
	for _, f := range frames {
		for _, r := range f.gpus {
			k := r.info.Key()
			byKey[k] = append(byKey[k], r)
		}
	}
	out := make([]shared.GPUInfo, 0, len(latest.gpus))
	seen := map[string]bool{}
	for _, cur := range latest.gpus {
		k := cur.info.Key()
		if seen[k] {
			continue // 防御：同一帧里重复的身份（无 index/uuid 的同名卡）只报一次
		}
		seen[k] = true
		rs := byKey[k]
		g := cur.info // 名称/身份/显存/上限/风扇/ECC 取最新帧

		var utilSum, utilMax, memUtilSum, powerSum, clkSum, tempMax float64
		var memUtilN, powerN, clkN, maskN, thermalN, powerCapN, hwN int
		var reasons uint64
		for _, r := range rs {
			utilSum += r.info.UtilPercent
			if r.info.UtilPercent > utilMax {
				utilMax = r.info.UtilPercent
			}
			if r.info.MemUtilPercent != nil {
				memUtilSum += *r.info.MemUtilPercent
				memUtilN++
			}
			if r.info.PowerW > 0 {
				powerSum += r.info.PowerW
				powerN++
			}
			if r.info.SMClockMHz > 0 {
				clkSum += r.info.SMClockMHz
				clkN++
			}
			if r.info.Temp > tempMax {
				tempMax = r.info.Temp
			}
			if r.maskOK {
				maskN++
				if r.mask&thrThermalMask != 0 {
					thermalN++
				}
				if r.mask&thrPowerMask != 0 {
					powerCapN++
				}
				if r.mask&thrHwSlowdown != 0 {
					hwN++
				}
				reasons |= r.mask
			}
		}
		n := float64(len(rs))
		g.UtilPercent = round1(utilSum / n)
		g.UtilMax = round1(utilMax)
		if tempMax > 0 {
			g.Temp = round1(tempMax)
		}
		if memUtilN > 0 {
			g.MemUtilPercent = f64p(round1(memUtilSum / float64(memUtilN)))
		}
		if powerN > 0 {
			g.PowerW = round1(powerSum / float64(powerN))
		}
		if clkN > 0 {
			g.SMClockMHz = float64(int64(clkSum/float64(clkN) + 0.5))
		}
		if maskN > 0 {
			frac := func(c int) *float64 { return f64p(float64(int64(float64(c)/float64(maskN)*1000+0.5)) / 1000) }
			g.ThrottleThermal, g.ThrottlePower, g.ThrottleHW = frac(thermalN), frac(powerCapN), frac(hwN)
			g.ThrottleReasons = nil
			for _, tr := range throttleReasonNames {
				if reasons&tr.bit != 0 {
					g.ThrottleReasons = append(g.ThrottleReasons, tr.name)
				}
			}
		}
		out = append(out, g)
	}
	return out
}

// attachGPUProcesses sets ProcCount/Processes from a compute-apps query keyed
// by GPU UUID. Only GPUs with a UUID can be matched; others are left nil
// (unknown) rather than reported as "0 processes".
func attachGPUProcesses(gpus []shared.GPUInfo, procs map[string][]shared.GPUProcess) {
	for i := range gpus {
		if gpus[i].UUID == "" {
			continue
		}
		ps := append([]shared.GPUProcess(nil), procs[gpus[i].UUID]...)
		sort.SliceStable(ps, func(a, b int) bool { return ps[a].MemUsed > ps[b].MemUsed })
		n := len(ps)
		gpus[i].ProcCount = &n
		if len(ps) > gpuProcTopN {
			ps = ps[:gpuProcTopN]
		}
		if len(ps) > 0 {
			gpus[i].Processes = ps
		}
	}
}

// ---------------------------------------------------------------------------
// 后台采样器
// ---------------------------------------------------------------------------

type gpuProcProbe func() (map[string][]shared.GPUProcess, bool)

type gpuSampler struct {
	mu        sync.Mutex
	once      sync.Once
	probe     func() []gpuReading
	procProbe gpuProcProbe
	interval  time.Duration // 采样间隔
	window    time.Duration // 聚合窗口 = 上报间隔
	frames    []gpuFrame
	procs     map[string][]shared.GPUProcess
	procsOK   bool
	procsAt   time.Time
	now       func() time.Time
}

var defaultGPUSampler = &gpuSampler{interval: 10 * time.Second, window: 30 * time.Second, now: time.Now}

// gpuSampleIntervalFor derives the sampling period from the report interval:
// three samples per report window, clamped to 5..15s. An explicit configured
// value (gpu_sample_interval_sec) wins, clamped to 2..300s.
func gpuSampleIntervalFor(report, configured time.Duration) time.Duration {
	if configured > 0 {
		if configured < gpuMinSampleIntv {
			return gpuMinSampleIntv
		}
		if configured > gpuMaxSampleIntv {
			return gpuMaxSampleIntv
		}
		return configured
	}
	d := report / 3
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	if d > 15*time.Second {
		d = 15 * time.Second
	}
	return d
}

// configureGPUSampling is called once from main with the agent's report
// interval and the optional gpu_sample_interval_sec override.
func configureGPUSampling(report, configured time.Duration) {
	s := defaultGPUSampler
	s.mu.Lock()
	defer s.mu.Unlock()
	if report > 0 {
		s.window = report
	}
	s.interval = gpuSampleIntervalFor(s.window, configured)
}

// gpuSnapshot is what collectors call each report: it starts the background
// sampler on first use (taking one synchronous sample so the very first report
// already carries GPUs) and returns the aggregate of the last report window.
func gpuSnapshot(probe func() []gpuReading, procProbe gpuProcProbe) []shared.GPUInfo {
	s := defaultGPUSampler
	s.once.Do(func() {
		s.probe, s.procProbe = probe, procProbe
		s.sampleOnce()
		go s.loop()
	})
	return s.snapshot()
}

func (s *gpuSampler) loop() {
	for {
		s.mu.Lock()
		d := s.interval
		if n := len(s.frames); n == 0 || len(s.frames[n-1].gpus) == 0 {
			d = gpuIdleBackoff
		}
		s.mu.Unlock()
		time.Sleep(d)
		s.sampleOnce()
	}
}

func (s *gpuSampler) sampleOnce() {
	// 探测在锁外执行：nvidia-smi 卡住时最多 4s，不能让 snapshot()（上报路径）陪着等。
	readings := s.probe()
	s.mu.Lock()
	procEvery := s.window
	if procEvery < 15*time.Second {
		procEvery = 15 * time.Second
	}
	hasUUID := false // 进程只能按 UUID 归卡；AMD/macOS 没有 UUID，不必再去跑 nvidia-smi
	for _, r := range readings {
		if r.info.UUID != "" {
			hasUUID = true
			break
		}
	}
	needProcs := hasUUID && s.procProbe != nil && s.now().Sub(s.procsAt) >= procEvery
	s.mu.Unlock()

	var procs map[string][]shared.GPUProcess
	var procsOK bool
	if needProcs {
		procs, procsOK = s.procProbe()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.frames = append(s.frames, gpuFrame{at: now, gpus: readings})
	if needProcs {
		s.procs, s.procsOK, s.procsAt = procs, procsOK, now
	}
	keep := 2 * s.window
	if keep < 3*s.interval {
		keep = 3 * s.interval
	}
	cut := 0
	for cut < len(s.frames)-1 && now.Sub(s.frames[cut].at) > keep {
		cut++
	}
	if cut > 0 {
		s.frames = append([]gpuFrame(nil), s.frames[cut:]...)
	}
}

func (s *gpuSampler) snapshot() []shared.GPUInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.frames) == 0 {
		return nil
	}
	now := s.now()
	latest := s.frames[len(s.frames)-1]
	// 最新一帧太旧 = 采样协程被卡住（驱动挂死）或 GPU 已被移除：报"无 GPU"而不是一份
	// 几分钟前的数据——曲线在故障点断开，比一条看似正常的平线诚实。
	if now.Sub(latest.at) > 3*s.interval+gpuProbeTimeout+time.Second {
		return nil
	}
	start := len(s.frames) - 1
	for start > 0 && now.Sub(s.frames[start-1].at) < s.window {
		start--
	}
	gpus := aggregateGPUFrames(s.frames[start:])
	if s.procsOK && len(gpus) > 0 {
		attachGPUProcesses(gpus, s.procs)
	}
	return gpus
}

// ---------------------------------------------------------------------------
// AMD：amdgpu sysfs（无第三方依赖）
// ---------------------------------------------------------------------------

// amdSysfsReadingsAt reads amdgpu GPUs under root (normally /sys/class/drm).
// It lives in an OS-neutral file so tests can point it at a fake tree.
//
// 旧实现只看 card0..card7：带 BMC 显卡（ast，占 card0）的 8 卡 MI 服务器，第 8 块卡是
// card8，直接漏采。现在枚举全部 cardN（排除 card0-HDMI-A-1 这类连接器节点），并补上
// 显存带宽繁忙度、温度、功耗/上限、风扇、SM 频率与不可纠正 ECC 计数。
func amdSysfsReadingsAt(root string) []gpuReading {
	matches, _ := filepath.Glob(filepath.Join(root, "card[0-9]*"))
	type card struct {
		path string
		n    int
	}
	var cards []card
	for _, m := range matches {
		n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(m), "card"))
		if err != nil {
			continue // card0-HDMI-A-1 等连接器
		}
		cards = append(cards, card{m, n})
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].n < cards[j].n })

	readStr := func(p string) (string, bool) {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(b)), true
	}
	readNum := func(p string) (float64, bool) {
		s, ok := readStr(p)
		if !ok {
			return 0, false
		}
		return optNum(s)
	}

	var rs []gpuReading
	for _, c := range cards {
		base := filepath.Join(c.path, "device")
		util, ok := readNum(filepath.Join(base, "gpu_busy_percent"))
		if !ok {
			continue // 不是 amdgpu（或驱动太老）
		}
		var r gpuReading
		g := &r.info
		g.Index = strconv.Itoa(c.n)
		g.Name = "AMD GPU"
		if pn, ok := readStr(filepath.Join(base, "product_name")); ok && pn != "" {
			g.Name = pn
		}
		g.UtilPercent = round1(util)
		if v, ok := readNum(filepath.Join(base, "mem_busy_percent")); ok {
			g.MemUtilPercent = f64p(round1(v))
		}
		used, okU := readNum(filepath.Join(base, "mem_info_vram_used"))
		total, okT := readNum(filepath.Join(base, "mem_info_vram_total"))
		if okU && okT {
			g.MemUsed, g.MemTotal = uint64(used), uint64(total)
			if total >= used {
				g.MemFree = uint64(total - used)
			}
			if total > 0 {
				g.MemPercent = round1(used / total * 100)
			}
		}
		if hw, _ := filepath.Glob(filepath.Join(base, "hwmon", "hwmon*")); len(hw) > 0 {
			h := hw[0]
			if v, ok := readNum(filepath.Join(h, "temp1_input")); ok && v > 0 {
				g.Temp = round1(v / 1000) // m°C
			} else if v, ok := readNum(filepath.Join(h, "temp2_input")); ok && v > 0 {
				g.Temp = round1(v / 1000)
			}
			if v, ok := readNum(filepath.Join(h, "power1_average")); ok && v > 0 {
				g.PowerW = round1(v / 1e6) // µW
			} else if v, ok := readNum(filepath.Join(h, "power1_input")); ok && v > 0 {
				g.PowerW = round1(v / 1e6)
			}
			if v, ok := readNum(filepath.Join(h, "power1_cap")); ok && v > 0 {
				g.PowerLimitW = round1(v / 1e6)
			}
			if v, ok := readNum(filepath.Join(h, "pwm1")); ok {
				g.FanPercent = f64p(round1(v / 255 * 100))
			}
		}
		if s, ok := readStr(filepath.Join(base, "pp_dpm_sclk")); ok {
			g.SMClockMHz, g.SMClockMaxMHz = parseAMDDpmClock(s)
		}
		if s, ok := readStr(filepath.Join(base, "ras", "umc_err_count")); ok {
			for _, ln := range strings.Split(s, "\n") {
				if k, v, found := strings.Cut(ln, ":"); found && strings.TrimSpace(k) == "ue" {
					if n, ok := optNum(v); ok {
						g.ECCUncorrected = f64p(n)
					}
				}
			}
		}
		rs = append(rs, r)
	}
	return rs
}

// parseAMDDpmClock parses pp_dpm_sclk ("0: 500Mhz\n1: 1800Mhz *") into the
// current level (marked with '*') and the highest level, in MHz.
func parseAMDDpmClock(s string) (cur, max float64) {
	for _, ln := range strings.Split(s, "\n") {
		_, rest, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		low := strings.ToLower(rest)
		i := strings.Index(low, "mhz")
		if i <= 0 {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rest[:i]), 64)
		if err != nil {
			continue
		}
		if v > max {
			max = v
		}
		if strings.HasSuffix(rest, "*") {
			cur = v
		}
	}
	return cur, max
}
