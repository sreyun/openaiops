package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// 8 块同型号卡：解析出来必须是 8 个不同身份（index + uuid），否则一路到时序库都会并成 1 块。
func TestParseNvidiaSmiRowsIdenticalCardsKeepIdentity(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		// index, uuid, name, util.gpu, util.mem, mem.used, mem.total, mem.free, temp, power.draw, power.limit,
		// fan, clocks.sm, clocks.max.sm, throttle, ecc
		b.WriteString(strconv.Itoa(i) + ", GPU-0000000" + strconv.Itoa(i) + ", NVIDIA A100-SXM4-80GB, " +
			strconv.Itoa(10*i) + ", 35, 40960, 81920, 40960, 65, 312.50, 400.00, [N/A], 1410, 1410, 0x0000000000000004, 0\n")
	}
	rows := parseNvidiaSmiRows(b.String(), nvidiaSmiFields)
	if len(rows) != 8 {
		t.Fatalf("want 8 rows, got %d", len(rows))
	}
	keys := map[string]bool{}
	for i, r := range rows {
		g := r.info
		keys[g.Key()] = true
		if g.Index != strconv.Itoa(i) || !strings.HasPrefix(g.UUID, "GPU-") {
			t.Fatalf("row %d identity wrong: %+v", i, g)
		}
		if g.UtilPercent != float64(10*i) || g.MemPercent != 50 || g.Temp != 65 || g.PowerW != 312.5 || g.PowerLimitW != 400 {
			t.Fatalf("row %d values wrong: %+v", i, g)
		}
		if g.MemUtilPercent == nil || *g.MemUtilPercent != 35 {
			t.Fatalf("row %d memory-bandwidth util missing: %+v", i, g.MemUtilPercent)
		}
		if g.FanPercent != nil {
			t.Fatalf("row %d: fan [N/A] must be unknown (nil), not 0", i)
		}
		if g.ECCUncorrected == nil || *g.ECCUncorrected != 0 {
			t.Fatalf("row %d: ECC 0 is a real reading, got %v", i, g.ECCUncorrected)
		}
		if !r.maskOK || r.mask != thrSwPowerCap {
			t.Fatalf("row %d throttle mask = %#x ok=%v", i, r.mask, r.maskOK)
		}
	}
	if len(keys) != 8 {
		t.Fatalf("identical cards collapsed: %d distinct keys", len(keys))
	}
}

// 型号名里带逗号时，多出来的列要并回 name，其余列保持对齐。
func TestParseNvidiaSmiRowsNameWithComma(t *testing.T) {
	fields := []string{"index", "name", "utilization.gpu", "memory.used", "memory.total"}
	rows := parseNvidiaSmiRows("0, Weird, Inc GPU, 77, 1024, 2048\n", fields)
	if len(rows) != 1 || rows[0].info.Name != "Weird, Inc GPU" || rows[0].info.UtilPercent != 77 || rows[0].info.MemPercent != 50 {
		t.Fatalf("comma in name misaligned columns: %+v", rows)
	}
}

type fakeSmi struct {
	reject   map[string]string // field -> "stdout" | "stderr"
	reportAs map[string]string // field -> name reported in the error (simulates an unexpected message)
	calls    []string
	out      func(fields []string) string
	fail     error
}

func (f *fakeSmi) run(args ...string) (string, string, error) {
	q := strings.TrimPrefix(args[0], "--query-gpu=")
	f.calls = append(f.calls, q)
	if f.fail != nil {
		return "", "", f.fail
	}
	fields := strings.Split(q, ",")
	for _, fl := range fields {
		if where, bad := f.reject[fl]; bad {
			name := fl
			if alias, ok := f.reportAs[fl]; ok {
				name = alias
			}
			msg := `Field "` + name + `" is not a valid field to query.` + "\n"
			if where == "stderr" {
				return "", msg, errors.New("exit status 2")
			}
			return msg, "", errors.New("exit status 2")
		}
	}
	return f.out(fields), "", nil
}

func csvRow(fields []string, vals map[string]string) string {
	cells := make([]string, len(fields))
	for i, f := range fields {
		if v, ok := vals[f]; ok {
			cells[i] = v
		} else {
			cells[i] = "[N/A]"
		}
	}
	return strings.Join(cells, ", ") + "\n"
}

// 老驱动不认的字段（报错可能在 stdout 也可能在 stderr）要被剔除后重试，协商结果缓存，
// 之后的采样不再重复试错。
func TestNvsmiQuerierNegotiatesUnsupportedFields(t *testing.T) {
	f := &fakeSmi{
		reject: map[string]string{"clocks_throttle_reasons.active": "stdout", "utilization.memory": "stderr"},
		out: func(fields []string) string {
			return csvRow(fields, map[string]string{"index": "0", "uuid": "GPU-1", "name": "Tesla T4", "utilization.gpu": "55", "memory.used": "100", "memory.total": "200"})
		},
	}
	q := &nvsmiQuerier{run: f.run}
	rows := q.query()
	if len(rows) != 1 || rows[0].info.UtilPercent != 55 || rows[0].info.Index != "0" {
		t.Fatalf("negotiated query failed: %+v", rows)
	}
	if rows[0].maskOK || rows[0].info.MemUtilPercent != nil {
		t.Fatalf("rejected fields must be absent, got %+v", rows[0])
	}
	for _, fl := range q.fields {
		if fl == "clocks_throttle_reasons.active" || fl == "utilization.memory" {
			t.Fatalf("negotiated list still contains %s", fl)
		}
	}
	n := len(f.calls)
	if n != 3 {
		t.Fatalf("want 3 calls to negotiate (2 rejections + success), got %d", n)
	}
	q.query()
	if len(f.calls) != n+1 {
		t.Fatalf("negotiation must be cached: %d extra calls", len(f.calls)-n)
	}
}

// 没装驱动 / 无设备：不是字段问题，不能改动协商结果，也不能退回老查询反复 fork。
func TestNvsmiQuerierNoDeviceLeavesFieldsAlone(t *testing.T) {
	f := &fakeSmi{fail: errors.New("exec: \"nvidia-smi\": executable file not found")}
	q := &nvsmiQuerier{run: f.run}
	if rows := q.query(); rows != nil {
		t.Fatalf("want nil, got %+v", rows)
	}
	if q.fields != nil || len(f.calls) != 1 {
		t.Fatalf("no-device path should be one call and leave fields unset: calls=%d fields=%v", len(f.calls), q.fields)
	}
}

// 报错里点名的字段不在请求列表里（协商无从继续）时，退回升级前的老查询；老查询没有
// index 列，用行号补身份，两块同型号老卡仍是两个身份。
func TestNvsmiQuerierFallsBackToLegacyQuery(t *testing.T) {
	f := &fakeSmi{
		reject:   map[string]string{"index": "stdout"},
		reportAs: map[string]string{"index": "some.renamed.field"},
		out: func(fields []string) string {
			return csvRow(fields, map[string]string{"name": "GTX 1080", "utilization.gpu": "12", "memory.used": "1", "memory.total": "8", "temperature.gpu": "50", "memory.free": "7"}) +
				csvRow(fields, map[string]string{"name": "GTX 1080", "utilization.gpu": "34", "memory.used": "1", "memory.total": "8", "temperature.gpu": "51", "memory.free": "7"})
		},
	}
	q := &nvsmiQuerier{run: f.run}
	rows := q.query()
	if len(f.calls) != 2 || f.calls[1] != strings.Join(nvidiaSmiLegacyFields, ",") {
		t.Fatalf("want negotiated attempt then legacy query, got %v", f.calls)
	}
	if len(rows) != 2 || rows[0].info.Index != "0" || rows[1].info.Index != "1" || rows[1].info.UtilPercent != 34 || rows[0].info.Temp != 50 {
		t.Fatalf("legacy fallback rows wrong: %+v", rows)
	}
	if rows[0].info.Key() == rows[1].info.Key() {
		t.Fatal("two identical legacy cards must still get distinct keys")
	}
}

// 窗口聚合：利用率均值+峰值、温度峰值、功耗/频率均值、降频占比；只报最新一帧里还在的卡。
func TestAggregateGPUFrames(t *testing.T) {
	gpu := func(util, temp, power, clk float64, mask uint64) gpuReading {
		return gpuReading{info: shared.GPUInfo{Name: "A100", Index: "0", UUID: "GPU-0", UtilPercent: util, Temp: temp, PowerW: power, SMClockMHz: clk, MemTotal: 80, MemUsed: 40}, mask: mask, maskOK: true}
	}
	gone := gpuReading{info: shared.GPUInfo{Name: "A100", Index: "1", UUID: "GPU-1", UtilPercent: 99}}
	t0 := time.Unix(1000, 0)
	frames := []gpuFrame{
		{at: t0, gpus: []gpuReading{gpu(10, 60, 200, 1400, 0), gone}},
		{at: t0.Add(10 * time.Second), gpus: []gpuReading{gpu(90, 75, 400, 1100, thrSwPowerCap)}},
		{at: t0.Add(20 * time.Second), gpus: []gpuReading{gpu(20, 70, 300, 1400, 0)}},
	}
	out := aggregateGPUFrames(frames)
	if len(out) != 1 {
		t.Fatalf("GPU missing from newest frame must not be reported: %+v", out)
	}
	g := out[0]
	if g.UtilPercent != 40 || g.UtilMax != 90 {
		t.Errorf("util avg/peak = %v/%v, want 40/90", g.UtilPercent, g.UtilMax)
	}
	if g.Temp != 75 || g.PowerW != 300 || g.SMClockMHz != 1300 {
		t.Errorf("temp peak / power avg / clock avg = %v/%v/%v, want 75/300/1300", g.Temp, g.PowerW, g.SMClockMHz)
	}
	if g.ThrottlePower == nil || *g.ThrottlePower != 0.333 || g.ThrottleThermal == nil || *g.ThrottleThermal != 0 {
		t.Errorf("throttle ratios wrong: power=%v thermal=%v", g.ThrottlePower, g.ThrottleThermal)
	}
	if len(g.ThrottleReasons) != 1 || g.ThrottleReasons[0] != "sw_power_cap" {
		t.Errorf("throttle reasons = %v", g.ThrottleReasons)
	}
}

// 旧实现：30s 一次单点快照——3 次采样里一次 100% 的尖峰，有 2/3 概率根本不出现在趋势里。
// 现在：窗口内每次采样都计入，峰值必然保留，均值反映真实占用。
func TestGPUSamplerCapturesBurstWithinReportWindow(t *testing.T) {
	now := time.Unix(10000, 0)
	seq := []float64{5, 100, 5}
	i := 0
	s := &gpuSampler{
		interval: 10 * time.Second, window: 30 * time.Second, now: func() time.Time { return now },
		probe: func() []gpuReading {
			v := seq[i%len(seq)]
			i++
			return []gpuReading{{info: shared.GPUInfo{Name: "A100", Index: "0", UUID: "GPU-0", UtilPercent: v}}}
		},
	}
	for k := 0; k < 3; k++ {
		s.sampleOnce()
		now = now.Add(10 * time.Second)
	}
	now = now.Add(-10 * time.Second) // 上报发生在最后一次采样之后不久
	got := s.snapshot()
	if len(got) != 1 || got[0].UtilMax != 100 || got[0].UtilPercent != 36.7 {
		t.Fatalf("burst lost: %+v", got)
	}
	// 采样协程卡死（驱动挂起）：数据过旧时报"无 GPU"，而不是一份过期读数。
	now = now.Add(2 * time.Minute)
	if got := s.snapshot(); got != nil {
		t.Fatalf("stale frames must not be reported: %+v", got)
	}
}

// 进程按 UUID 归卡，按显存降序取前 N；没有 UUID 的卡不猜（nil = 未知，而不是 0）。
func TestAttachGPUProcesses(t *testing.T) {
	out := "GPU-a, 101, /usr/bin/python3, 2048\n" +
		"GPU-a, 102, C:\\Program Files\\app\\train.exe, 8192\n" +
		"GPU-b, 103, [Insufficient Permissions], [N/A]\n" +
		"garbage line\n"
	procs := parseComputeApps(out)
	gpus := []shared.GPUInfo{{Name: "A", UUID: "GPU-a"}, {Name: "B", UUID: "GPU-b"}, {Name: "C", UUID: "GPU-c"}, {Name: "AMD", Index: "1"}}
	attachGPUProcesses(gpus, procs)
	a := gpus[0]
	if a.ProcCount == nil || *a.ProcCount != 2 || len(a.Processes) != 2 || a.Processes[0].PID != 102 || a.Processes[0].Name != "train.exe" || a.Processes[0].MemUsed != 8192<<20 {
		t.Fatalf("GPU-a processes wrong: %+v", a)
	}
	if gpus[1].ProcCount == nil || *gpus[1].ProcCount != 1 || gpus[1].Processes[0].Name != "" {
		t.Fatalf("GPU-b processes wrong: %+v", gpus[1])
	}
	if gpus[2].ProcCount == nil || *gpus[2].ProcCount != 0 || gpus[2].Processes != nil {
		t.Fatalf("idle GPU must report 0 processes: %+v", gpus[2])
	}
	if gpus[3].ProcCount != nil {
		t.Fatalf("GPU without UUID must stay unknown: %+v", gpus[3])
	}
}

// amdgpu sysfs：card0 是 BMC 显卡（无 gpu_busy_percent），card1..card9 是 9 块加速卡——旧实现
// 只扫 card0..card7，会漏掉 card8/card9；连接器节点（card1-HDMI-A-1）不能被当成卡。
func TestAMDSysfsReadingsEnumeratesAllCards(t *testing.T) {
	root := t.TempDir()
	write := func(rel, val string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(val+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("card0/device/vendor", "0x1a03") // ast BMC VGA
	write("card1-HDMI-A-1/status", "disconnected")
	for n := 1; n <= 9; n++ {
		d := "card" + strconv.Itoa(n) + "/device/"
		write(d+"gpu_busy_percent", strconv.Itoa(n*10))
		write(d+"mem_busy_percent", "25")
		write(d+"mem_info_vram_used", "1073741824")
		write(d+"mem_info_vram_total", "4294967296")
		write(d+"product_name", "AMD Instinct MI300X")
		write(d+"hwmon/hwmon3/temp1_input", "54000")
		write(d+"hwmon/hwmon3/power1_average", "350000000")
		write(d+"hwmon/hwmon3/power1_cap", "750000000")
		write(d+"pp_dpm_sclk", "0: 500Mhz\n1: 1700Mhz *\n2: 2100Mhz")
		write(d+"ras/umc_err_count", "ue: 1\nce: 7")
	}
	rs := amdSysfsReadingsAt(root)
	if len(rs) != 9 {
		t.Fatalf("want 9 amdgpu cards, got %d", len(rs))
	}
	last := rs[8].info
	if last.Index != "9" || last.Name != "AMD Instinct MI300X" || last.UtilPercent != 90 || last.MemPercent != 25 {
		t.Fatalf("card9 core fields wrong: %+v", last)
	}
	if last.Temp != 54 || last.PowerW != 350 || last.PowerLimitW != 750 || last.SMClockMHz != 1700 || last.SMClockMaxMHz != 2100 {
		t.Fatalf("card9 hwmon/clock fields wrong: %+v", last)
	}
	if last.MemUtilPercent == nil || *last.MemUtilPercent != 25 || last.ECCUncorrected == nil || *last.ECCUncorrected != 1 {
		t.Fatalf("card9 mem-bw / ECC wrong: %+v", last)
	}
	if rs[0].info.Index != "1" || rs[1].info.Index != "2" {
		t.Fatalf("cards must be ordered numerically: %s, %s", rs[0].info.Index, rs[1].info.Index)
	}
}

func TestGPUSampleIntervalFor(t *testing.T) {
	cases := []struct{ report, conf, want time.Duration }{
		{30 * time.Second, 0, 10 * time.Second},
		{5 * time.Second, 0, 5 * time.Second},
		{300 * time.Second, 0, 15 * time.Second},
		{30 * time.Second, 3 * time.Second, 3 * time.Second},
		{30 * time.Second, time.Second, 2 * time.Second},
		{30 * time.Second, time.Hour, 300 * time.Second},
	}
	for _, c := range cases {
		if got := gpuSampleIntervalFor(c.report, c.conf); got != c.want {
			t.Errorf("gpuSampleIntervalFor(%v,%v)=%v want %v", c.report, c.conf, got, c.want)
		}
	}
}
