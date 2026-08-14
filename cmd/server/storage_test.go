package main

import (
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// The VM read path must reassemble per-series export lines into per-timestamp
// samples (join by ts, ms→s, correct field mapping, sorted).
func TestVMExportParse(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_cpu_percent","host":"h1"},"values":[50,60],"timestamps":[105000,100000]}
{"metric":{"__name__":"aiops_mem_percent","host":"h1"},"values":[70,80],"timestamps":[105000,100000]}
{"metric":{"__name__":"aiops_load1","host":"h1"},"values":[1.5],"timestamps":[100000]}`
	s := parseVMExport(strings.NewReader(nd))
	if len(s) != 2 {
		t.Fatalf("expected 2 samples (2 distinct timestamps), got %d", len(s))
	}
	// sorted ascending: ts=100 first
	if s[0].Timestamp != 100 || s[0].CPUPercent != 60 || s[0].MemPercent != 80 || s[0].Load1 != 1.5 {
		t.Errorf("sample@100 wrong: %+v", s[0])
	}
	if s[1].Timestamp != 105 || s[1].CPUPercent != 50 || s[1].MemPercent != 70 {
		t.Errorf("sample@105 wrong: %+v", s[1])
	}
	// load1 only arrived at ts=100 — LOCF must carry it to ts=105 (was the
	// "missing curve" bug when independent Prom series join by exact ts).
	if s[1].Load1 != 1.5 {
		t.Errorf("sample@105 Load1 should LOCF from 100, got %v", s[1].Load1)
	}
}

// Staggered load1/5/15 series must all stay continuous after join — otherwise
// host-detail「系统负载」flickers between 1/2/3 curves across time ranges.
func TestVMExportParseLoadLOCF(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_load1","host":"h1"},"values":[1.1,1.2],"timestamps":[100000,110000]}
{"metric":{"__name__":"aiops_load5","host":"h1"},"values":[2.5],"timestamps":[105000]}
{"metric":{"__name__":"aiops_load15","host":"h1"},"values":[3.0,3.1],"timestamps":[100000,115000]}`
	s := parseVMExport(strings.NewReader(nd))
	if len(s) != 4 {
		t.Fatalf("expected 4 timestamps, got %d", len(s))
	}
	// ts=100: load1=1.1, load15=3.0, load5 unset→0
	if s[0].Load1 != 1.1 || s[0].Load15 != 3.0 || s[0].Load5 != 0 {
		t.Errorf("ts=100 wrong: load1=%v load5=%v load15=%v", s[0].Load1, s[0].Load5, s[0].Load15)
	}
	// ts=105: load5 arrives; load1/15 LOCF
	if s[1].Load1 != 1.1 || s[1].Load5 != 2.5 || s[1].Load15 != 3.0 {
		t.Errorf("ts=105 LOCF wrong: load1=%v load5=%v load15=%v", s[1].Load1, s[1].Load5, s[1].Load15)
	}
	// ts=110: load1 updates; load5/15 LOCF
	if s[2].Load1 != 1.2 || s[2].Load5 != 2.5 || s[2].Load15 != 3.0 {
		t.Errorf("ts=110 LOCF wrong: load1=%v load5=%v load15=%v", s[2].Load1, s[2].Load5, s[2].Load15)
	}
	// ts=115: load15 updates; load1/5 LOCF
	if s[3].Load1 != 1.2 || s[3].Load5 != 2.5 || s[3].Load15 != 3.1 {
		t.Errorf("ts=115 LOCF wrong: load1=%v load5=%v load15=%v", s[3].Load1, s[3].Load5, s[3].Load15)
	}
	// Real zero must not be overwritten by LOCF once marked present.
	nd2 := `{"metric":{"__name__":"aiops_load1","host":"h1"},"values":[1.5,0],"timestamps":[100000,105000]}
{"metric":{"__name__":"aiops_load5","host":"h1"},"values":[2.0],"timestamps":[100000]}`
	s2 := parseVMExport(strings.NewReader(nd2))
	if len(s2) != 2 || s2[1].Load1 != 0 || s2[1].Load5 != 2.0 {
		t.Errorf("real zero Load1 must stick + Load5 LOCF: %+v", s2)
	}
}

func TestAlignSamplesToStep(t *testing.T) {
	in := []shared.Sample{
		{Timestamp: 100, Metrics: shared.Metrics{Load1: 1, Load5: 2}},
		{Timestamp: 108, Metrics: shared.Metrics{Load1: 1.5, Load5: 2.5}},
	}
	out := alignSamplesToStep(in, 100, 120, 5)
	if len(out) < 4 {
		t.Fatalf("expected stepped grid, got %d: %+v", len(out), out)
	}
	// t=100 → load1=1; t=105 still LOCF from 100; t=110 from 108
	by := map[int64]shared.Sample{}
	for _, s := range out {
		by[s.Timestamp] = s
	}
	if by[100].Load1 != 1 || by[105].Load1 != 1 {
		t.Errorf("LOCF before update: %+v", by)
	}
	if by[110].Load1 != 1.5 || by[110].Load5 != 2.5 {
		t.Errorf("after update: %+v", by[110])
	}
}

func TestAlignSamplesToStepSkipsLargeGaps(t *testing.T) {
	in := []shared.Sample{
		{Timestamp: 100, Metrics: shared.Metrics{Load1: 1}},
		{Timestamp: 1000, Metrics: shared.Metrics{Load1: 9}},
	}
	out := alignSamplesToStep(in, 100, 1000, 5)
	if len(out) > 20 {
		t.Fatalf("large hole should not LOCF-fill the grid, got %d points", len(out))
	}
	var sawLate bool
	for _, s := range out {
		if s.Timestamp >= 990 && s.Load1 == 9 {
			sawLate = true
		}
		if s.Timestamp > 120 && s.Timestamp < 980 && s.Load1 == 1 {
			t.Fatalf("LOCF across hole at t=%d", s.Timestamp)
		}
	}
	if !sawLate {
		t.Fatal("expected samples to resume after the hole")
	}
}

func TestHistoryCoversWindow(t *testing.T) {
	mk := func(first, last int64, n int) []shared.Sample {
		if n < 2 {
			n = 2
		}
		out := make([]shared.Sample, n)
		span := last - first
		for i := 0; i < n; i++ {
			ts := first
			if n > 1 {
				ts = first + span*int64(i)/int64(n-1)
			}
			out[i] = shared.Sample{Timestamp: ts}
		}
		return out
	}
	from, to := int64(1_700_000_000), int64(1_700_000_000+6*3600)
	if !historyCoversWindow(mk(from, to, 360), from, to) {
		t.Fatal("full 6h window should cover")
	}
	if historyCoversWindow(mk(from+4*3600, to, 40), from, to) {
		t.Fatal("starting 4h late must not count as covering 6h")
	}
	if historyCoversWindow(mk(from, to, 5), from, to) {
		t.Fatal("too few points")
	}
}

func TestPromTsSeconds(t *testing.T) {
	sec, ok := promTsSeconds(float64(1_700_000_000))
	if !ok || sec != 1_700_000_000 {
		t.Fatalf("seconds: %d ok=%v", sec, ok)
	}
	sec, ok = promTsSeconds(float64(1_700_000_000_000))
	if !ok || sec != 1_700_000_000 {
		t.Fatalf("ms: %d ok=%v", sec, ok)
	}
	if _, ok := promTsSeconds(0.0); ok {
		t.Fatal("zero ts must be rejected")
	}
	sec, ok = promTsSeconds("1700000000")
	if !ok || sec != 1_700_000_000 {
		t.Fatalf("string: %d ok=%v", sec, ok)
	}
}

// Ephemeral overlay paths that appear once must not ride hold-forward across a
// long window (that is what painted a rainbow disk chart on 6h+).
func TestFinalizeHistJoinPrunesEphemeralDisks(t *testing.T) {
	byTs := map[int64]*histJoinCell{}
	base := int64(1_700_000_000)
	for i := 0; i < 12; i++ {
		ts := base + int64(i)*60
		applyHistJoinMetric(byTs, ts, "aiops_cpu_percent", "", "", "", "", 10)
		applyHistJoinMetric(byTs, ts, "aiops_disk_vol_percent", "", "C:", "", "", 40)
		if i == 0 {
			applyHistJoinMetric(byTs, ts, "aiops_disk_vol_percent", "", "/var/lib/docker/overlay2/abc", "", "", 1)
		}
	}
	out := finalizeHistJoin(byTs)
	if len(out) != 12 {
		t.Fatalf("samples=%d", len(out))
	}
	if len(out[0].Disks) != 2 {
		t.Fatalf("first sample should have C: + overlay, got %+v", out[0].Disks)
	}
	last := out[len(out)-1]
	if len(last.Disks) != 1 || last.Disks[0].Path != "C:" {
		t.Fatalf("overlay must be pruned by the end, got %+v", last.Disks)
	}
}

// GPU 利用率在 VM 里是带 gpu 标签的独立系列（每块显卡一条），parseVMExport 必须按名
// 重建每个时间点的 GPUs 数组——否则历史读回缺 gpus，前端画不出「GPU 近期趋势图」（曾漏）。
func TestVMExportParseGPU(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"GPU0"},"values":[30,40],"timestamps":[100000,105000]}
{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"GPU1"},"values":[55],"timestamps":[100000]}
{"metric":{"__name__":"aiops_cpu_percent","host":"h1"},"values":[10],"timestamps":[100000]}`
	s := parseVMExport(strings.NewReader(nd))
	if len(s) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(s))
	}
	if len(s[0].GPUs) != 2 { // ts=100：两块显卡都应重建出来
		t.Fatalf("sample@100 应重建 2 块 GPU，实际 %d：%+v", len(s[0].GPUs), s[0].GPUs)
	}
	byName := map[string]float64{}
	for _, g := range s[0].GPUs {
		byName[g.Name] = g.UtilPercent
	}
	if byName["GPU0"] != 30 || byName["GPU1"] != 55 {
		t.Errorf("sample@100 GPU 值错误：%+v", s[0].GPUs)
	}
	// Stable order by Name regardless of VM series arrival order.
	if s[0].GPUs[0].Name != "GPU0" || s[0].GPUs[1].Name != "GPU1" {
		t.Errorf("sample@100 GPU 应按 Name 排序：%+v", s[0].GPUs)
	}
	// ts=105：GPU0 更新；GPU1 由 hold-forward 保留（避免多卡曲线闪烁消失）
	if len(s[1].GPUs) != 2 {
		t.Fatalf("sample@105 应 hold-forward 2 块 GPU，实际 %d：%+v", len(s[1].GPUs), s[1].GPUs)
	}
	by105 := map[string]float64{}
	for _, g := range s[1].GPUs {
		by105[g.Name] = g.UtilPercent
	}
	if by105["GPU0"] != 40 || by105["GPU1"] != 55 {
		t.Errorf("sample@105 GPU hold-forward 错误：%+v", s[1].GPUs)
	}
}

// GPU series arriving in reverse order must still sort by Name after parse.
func TestVMExportParseGPUStableOrder(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"GPU1"},"values":[55],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"GPU0"},"values":[30],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"A100"},"values":[10],"timestamps":[100000]}`
	s := parseVMExport(strings.NewReader(nd))
	if len(s) != 1 || len(s[0].GPUs) != 3 {
		t.Fatalf("expected 1 sample / 3 GPUs, got samples=%d gpus=%v", len(s), s)
	}
	got := []string{s[0].GPUs[0].Name, s[0].GPUs[1].Name, s[0].GPUs[2].Name}
	want := []string{"A100", "GPU0", "GPU1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GPU order unstable: got %v want %v", got, want)
		}
	}
}

// 多盘同理：aiops_disk_vol_* 带 path 标签，parseVMExport 须按分区重建 s.Disks（percent/used/
// total），否则历史里只剩聚合根分区一条线（Windows C/D/E、Linux/macOS 多挂载点均受影响）。
func TestVMExportParseDisks(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_disk_vol_percent","host":"h1","path":"C:"},"values":[60,62],"timestamps":[100000,105000]}
{"metric":{"__name__":"aiops_disk_vol_used_bytes","host":"h1","path":"C:"},"values":[600,620],"timestamps":[100000,105000]}
{"metric":{"__name__":"aiops_disk_vol_total_bytes","host":"h1","path":"C:"},"values":[1000,1000],"timestamps":[100000,105000]}
{"metric":{"__name__":"aiops_disk_vol_percent","host":"h1","path":"D:"},"values":[30],"timestamps":[100000]}`
	s := parseVMExport(strings.NewReader(nd))
	if len(s) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(s))
	}
	if len(s[0].Disks) != 2 || s[0].Disks[0].Path != "C:" || s[0].Disks[1].Path != "D:" { // 按 path 排序
		t.Fatalf("sample@100 分区重建/排序错误：%+v", s[0].Disks)
	}
	if s[0].Disks[0].Percent != 60 || s[0].Disks[0].Used != 600 || s[0].Disks[0].Total != 1000 {
		t.Errorf("C: 明细（percent/used/total）错误：%+v", s[0].Disks[0])
	}
	// ts=105：C: 更新；D: hold-forward 保留
	if len(s[1].Disks) != 2 || s[1].Disks[0].Path != "C:" || s[1].Disks[0].Percent != 62 {
		t.Errorf("sample@105 C: 错误：%+v", s[1].Disks)
	}
	if s[1].Disks[1].Path != "D:" || s[1].Disks[1].Percent != 30 {
		t.Errorf("sample@105 D: 应 hold-forward：%+v", s[1].Disks)
	}
}

// GPU 多指标：util/temp/mem_* 各自是带 gpu 标签的独立系列，parseVMExport 须把它们并回同一块
// 显卡的不同字段（本次 GPU 深度指标扩展的读回路径）。
func TestVMExportParseGPUFull(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"GPU0"},"values":[30],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_temp_c","host":"h1","gpu":"GPU0"},"values":[65],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_mem_used_bytes","host":"h1","gpu":"GPU0"},"values":[2000],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_mem_free_bytes","host":"h1","gpu":"GPU0"},"values":[6000],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_mem_total_bytes","host":"h1","gpu":"GPU0"},"values":[8000],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_mem_percent","host":"h1","gpu":"GPU0"},"values":[25],"timestamps":[100000]}`
	s := parseVMExport(strings.NewReader(nd))
	if len(s) != 1 || len(s[0].GPUs) != 1 {
		t.Fatalf("应重建 1 个样本 1 块 GPU，实际 samples=%d", len(s))
	}
	g := s[0].GPUs[0]
	if g.Name != "GPU0" || g.UtilPercent != 30 || g.Temp != 65 || g.MemUsed != 2000 || g.MemFree != 6000 || g.MemTotal != 8000 || g.MemPercent != 25 {
		t.Errorf("GPU 多指标重建错误：%+v", g)
	}
}

// Partial GPU field updates must not clobber other fields via whole-struct overwrite.
func TestVMExportParseGPUFieldHold(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_gpu_util_percent","host":"h1","gpu":"GPU0"},"values":[30,40],"timestamps":[100000,105000]}
{"metric":{"__name__":"aiops_gpu_temp_c","host":"h1","gpu":"GPU0"},"values":[65],"timestamps":[100000]}
{"metric":{"__name__":"aiops_gpu_mem_percent","host":"h1","gpu":"GPU0"},"values":[25],"timestamps":[100000]}`
	s := parseVMExport(strings.NewReader(nd))
	if len(s) != 2 || len(s[1].GPUs) != 1 {
		t.Fatalf("samples=%d gpus=%v", len(s), s)
	}
	g := s[1].GPUs[0]
	if g.UtilPercent != 40 || g.Temp != 65 || g.MemPercent != 25 {
		t.Errorf("ts=105 应更新 util=40 并 hold temp/mem：%+v", g)
	}
}

// 连接计数在 VM 里是带 proto+state 标签的独立系列，parseVMExport 须按 (协议,状态) 重建
// s.Conns，否则「连接数 / 会话状态」趋势图读不到历史。
func TestVMExportParseConns(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_net_conn_count","host":"h1","proto":"tcp","state":"ESTABLISHED"},"values":[12,15],"timestamps":[100000,105000]}
{"metric":{"__name__":"aiops_net_conn_count","host":"h1","proto":"tcp","state":"TIME_WAIT"},"values":[3],"timestamps":[100000]}
{"metric":{"__name__":"aiops_net_conn_count","host":"h1","proto":"udp","state":""},"values":[7],"timestamps":[100000]}`
	s := parseVMExport(strings.NewReader(nd))
	if len(s) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(s))
	}
	// ts=100：tcp ESTABLISHED=12, tcp TIME_WAIT=3, udp=7 — 排序后 tcp 在 udp 前，ESTABLISHED 在 TIME_WAIT 前
	if len(s[0].Conns) != 3 {
		t.Fatalf("sample@100 应重建 3 条连接序列，实际 %d：%+v", len(s[0].Conns), s[0].Conns)
	}
	get := func(sm int, proto, state string) int {
		for _, c := range s[sm].Conns {
			if c.Proto == proto && c.State == state {
				return c.Count
			}
		}
		return -1
	}
	if get(0, "tcp", "ESTABLISHED") != 12 || get(0, "tcp", "TIME_WAIT") != 3 || get(0, "udp", "") != 7 {
		t.Errorf("sample@100 连接计数错误：%+v", s[0].Conns)
	}
	// ts=105：ESTABLISHED 更新；TIME_WAIT / udp hold-forward 保留
	if len(s[1].Conns) != 3 || get(1, "tcp", "ESTABLISHED") != 15 {
		t.Errorf("sample@105 连接计数错误：%+v", s[1].Conns)
	}
	if get(1, "tcp", "TIME_WAIT") != 3 || get(1, "udp", "") != 7 {
		t.Errorf("sample@105 连接应 hold-forward：%+v", s[1].Conns)
	}
}

func TestPasswordPolicy(t *testing.T) {
	good := []string{"Abcd123!", "P@ssw0rd", "aB3$aB3$", "Zx9#mnop", "长密码Ab1!x"}
	for _, p := range good {
		if !validatePasswordStrength(p) {
			t.Errorf("should accept strong password %q", p)
		}
	}
	bad := map[string]string{
		"":          "empty",
		"Ab1!xy":    "too short (6)",
		"abcdefg1!": "no uppercase",
		"ABCDEFG1!": "no lowercase",
		"Abcdefgh!": "no digit",
		"Abcdefg12": "no special",
		"abcdefgh":  "only lowercase",
	}
	for p, why := range bad {
		if validatePasswordStrength(p) {
			t.Errorf("should reject %q (%s)", p, why)
		}
	}
}

func TestRecordingPersistence(t *testing.T) {
	dir := t.TempDir()
	m := newTermManager()
	m.recDir = dir
	arch := termArchive{
		info:      termSessionInfo{ID: "sess1", Hostname: "web-01", Operator: "alice", Frames: 2},
		recording: []termRecordFrame{{Ts: 1, Type: "output", Data: "aGk="}, {Ts: 2, Type: "input", Data: "eA=="}},
	}
	m.persistRecording(arch)

	// Read the frames straight back from the file.
	if got := m.readRecordingFile("sess1"); len(got) != 2 {
		t.Fatalf("readRecordingFile: expected 2 frames, got %d", len(got))
	}

	// A fresh manager (simulating a restart) indexes the persisted recording...
	m2 := newTermManager()
	m2.loadRecordings(dir)
	var found *termSessionInfo
	for _, s := range m2.listSessions() {
		if s.ID == "sess1" {
			cp := s
			found = &cp
		}
	}
	if found == nil {
		t.Fatal("loadRecordings should index the persisted session after restart")
	}
	if found.Frames != 2 || found.Active {
		t.Errorf("restored session info wrong: frames=%d active=%v", found.Frames, found.Active)
	}
	// ...and replay reads the frames lazily from the file.
	if got := m2.getRecording("sess1"); len(got) != 2 {
		t.Fatalf("getRecording after restart should read 2 frames from file, got %d", len(got))
	}
	// Unknown session → nil (no panic).
	if m2.getRecording("nope") != nil {
		t.Error("unknown session should return nil")
	}
}
