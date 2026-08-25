//go:build darwin

package main

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aiops-monitor/shared"
)

// darwinCollector gathers base metrics with zero third-party dependencies:
// syscall.Statfs for disk, and the always-present sysctl/vm_stat/netstat/ps
// tools for everything else. It cross-compiles from any host (no cgo);
// runtime values should be spot-checked on a real Mac.
type darwinCollector struct {
	diskPath                    string
	prevRx, prevTx              uint64
	prevNetT                    time.Time
	prevDiskRead, prevDiskWrite uint64
	prevDiskROps, prevDiskWOps  uint64
	prevDiskT                   time.Time
	primed                      bool
}

func newCollector(diskPath string) Collector {
	if diskPath == "" {
		diskPath = "/"
	}
	return &darwinCollector{diskPath: diskPath}
}

func (c *darwinCollector) Name() string    { return "darwin-sysctl" }
func (c *darwinCollector) Supported() bool { return true }

func (c *darwinCollector) Collect() (shared.Metrics, error) {
	now := time.Now()
	m := shared.Metrics{CPUCores: runtime.NumCPU()}

	m.CPUPercent = darwinCPU()

	total := sysctlUint("hw.memsize")
	used := darwinMemUsed()
	if used > total {
		used = total
	}
	m.MemTotal, m.MemUsed = total, used
	if total > 0 {
		m.MemPercent = round1(float64(used) / float64(total) * 100)
	}

	if st, su := darwinSwap(); st > 0 {
		m.SwapTotal, m.SwapUsed = st, su
		m.SwapPercent = round1(float64(su) / float64(st) * 100)
	}

	var fs syscall.Statfs_t
	if err := syscall.Statfs(c.diskPath, &fs); err == nil && fs.Blocks > 0 {
		bsize := uint64(fs.Bsize)
		dt := fs.Blocks * bsize
		free := fs.Bfree * bsize
		m.DiskTotal = dt
		m.DiskUsed = dt - free
		m.DiskPercent = round1(float64(dt-free) / float64(dt) * 100)
	}

	m.Disks = darwinDisks()

	if rx, tx, ok := darwinNet(); ok {
		if c.primed && !c.prevNetT.IsZero() {
			if el := now.Sub(c.prevNetT).Seconds(); el > 0 {
				m.NetSentRate = round1(rate(tx, c.prevTx, el))
				m.NetRecvRate = round1(rate(rx, c.prevRx, el))
			}
		}
		c.prevRx, c.prevTx = rx, tx
		c.prevNetT = now
	}

	// 磁盘 IO：ioreg 的 IOBlockStorageDriver 累计读写字节 + 操作数，差分为速率与 IOPS
	if rb, wb, ro, wo := darwinDiskIO(); rb+wb+ro+wo > 0 {
		if c.primed && !c.prevDiskT.IsZero() {
			if el := now.Sub(c.prevDiskT).Seconds(); el > 0 {
				m.DiskReadRate = round1(rate(rb, c.prevDiskRead, el))
				m.DiskWriteRate = round1(rate(wb, c.prevDiskWrite, el))
				m.DiskReadIOPS = round1(rate(ro, c.prevDiskROps, el))
				m.DiskWriteIOPS = round1(rate(wo, c.prevDiskWOps, el))
			}
		}
		c.prevDiskRead, c.prevDiskWrite = rb, wb
		c.prevDiskROps, c.prevDiskWOps = ro, wo
		c.prevDiskT = now
	}

	m.Conns, m.NetConns = darwinConnStats()
	m.Load1, m.Load5, m.Load15 = darwinLoad()
	m.ProcCount = darwinProcCount()
	m.ProcessNames = darwinProcNames()
	m.Uptime = darwinUptime()
	m.GPUs = cachedGPUs(darwinGPUs)

	c.primed = true
	return m, nil
}

// darwinDiskIO 从 ioreg 的 IOBlockStorageDriver 统计里汇总所有磁盘的累计读写字节数与
// 读写操作数（供上层算速率）。字段位于每块盘的 "Statistics" 字典中，跨多块盘求和。
func darwinDiskIO() (readBytes, writeBytes, readOps, writeOps uint64) {
	out := run("ioreg", "-r", "-c", "IOBlockStorageDriver", "-w", "0")
	if out == "" {
		return
	}
	readBytes = sumIORegStat(out, `"Bytes (Read)"`)
	writeBytes = sumIORegStat(out, `"Bytes (Write)"`)
	readOps = sumIORegStat(out, `"Operations (Read)"`)
	writeOps = sumIORegStat(out, `"Operations (Write)"`)
	return
}

// sumIORegStat 汇总 ioreg 文本里所有 `key=<数字>` 的数值之和（同一 key 可能出现在多块盘）。
func sumIORegStat(out, key string) uint64 {
	var total uint64
	mark := key + "="
	for idx := 0; ; {
		i := strings.Index(out[idx:], mark)
		if i < 0 {
			break
		}
		p := idx + i + len(mark)
		j := p
		for j < len(out) && out[j] >= '0' && out[j] <= '9' {
			j++
		}
		if j > p {
			if v, err := strconv.ParseUint(out[p:j], 10, 64); err == nil {
				total += v
			}
		}
		idx = p // 越过本次 mark（p 严格大于起点，不会死循环）
	}
	return total
}

// darwinGPUs reads GPU utilization from IOKit's IOAccelerator objects via
// `ioreg`. Apple Silicon and discrete GPUs expose a "Device Utilization %"
// counter inside PerformanceStatistics. VRAM/temperature aren't reliably
// available without elevated tools, so only utilization + model are reported.
func darwinGPUs() []shared.GPUInfo {
	out := run("ioreg", "-r", "-d", "1", "-w", "0", "-c", "IOAccelerator")
	if out == "" {
		return nil
	}
	const mark = `"Device Utilization %"=`
	var gpus []shared.GPUInfo
	idx := 0
	for {
		i := strings.Index(out[idx:], mark)
		if i < 0 {
			break
		}
		p := idx + i + len(mark)
		j := p
		for j < len(out) && out[j] >= '0' && out[j] <= '9' {
			j++
		}
		util, _ := strconv.ParseFloat(out[p:j], 64)
		gpus = append(gpus, shared.GPUInfo{
			Name:        darwinGPUModel(out[:idx+i]),
			UtilPercent: round1(util),
		})
		idx = j
	}
	return gpus
}

// darwinGPUModel finds the nearest preceding `"model"=<"NAME">` (or
// `"model" = "NAME"`) in the ioreg text, which names the accelerator.
func darwinGPUModel(seg string) string {
	k := strings.LastIndex(seg, `"model"`)
	if k < 0 {
		return "GPU"
	}
	rest := seg[k:]
	if a := strings.Index(rest, `<"`); a >= 0 {
		if b := strings.IndexByte(rest[a+2:], '"'); b >= 0 {
			if name := strings.TrimSpace(rest[a+2 : a+2+b]); name != "" {
				return name
			}
		}
	}
	return "GPU"
}

// ---- helpers (shell out to always-present macOS tools) ----

func run(name string, args ...string) string {
	// Bounded timeout so a wedged system tool (e.g. a hung `top`/`ioreg`) can't
	// stall the report loop. 8s comfortably covers `top -l 2`, which samples twice
	// with a ~1s gap.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func sysctlUint(key string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(run("sysctl", "-n", key)), 10, 64)
	return v
}

// darwinCPU parses `top -l 2` (two samples; the second reflects real usage).
func darwinCPU() float64 {
	out := run("top", "-l", "2", "-n", "0")
	idle := -1.0
	for _, ln := range strings.Split(out, "\n") {
		i := strings.Index(ln, "CPU usage:")
		if i < 0 {
			continue
		}
		j := strings.Index(ln, "idle")
		if j < 0 {
			continue
		}
		fields := strings.Fields(ln[:j])
		if len(fields) == 0 {
			continue
		}
		v := strings.TrimSuffix(fields[len(fields)-1], "%")
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			idle = f // keep the last (second sample)
		}
	}
	if idle < 0 {
		return 0
	}
	return round1(100 - idle)
}

func darwinMemUsed() uint64 {
	out := run("vm_stat")
	if out == "" {
		return 0
	}
	pageSize := uint64(4096)
	if i := strings.Index(out, "page size of "); i >= 0 {
		f := strings.Fields(out[i+len("page size of "):])
		if len(f) > 0 {
			if v, err := strconv.ParseUint(f[0], 10, 64); err == nil {
				pageSize = v
			}
		}
	}
	get := func(prefix string) uint64 {
		for _, ln := range strings.Split(out, "\n") {
			if strings.HasPrefix(ln, prefix) {
				v := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(ln, prefix)), ".")
				n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
				return n
			}
		}
		return 0
	}
	return (get("Pages active:") + get("Pages wired down:") + get("Pages occupied by compressor:")) * pageSize
}

// darwinSwap reads swap usage from `sysctl -n vm.swapusage`, whose output is:
//
//	total = 2048.00M  used = 1234.50M  free = 813.50M  (encrypted)
//
// The value may attach the unit ("2048.00M") or separate it ("2048.00 M")
// depending on the macOS version, so we scan for the "total ="/"used =" markers
// and parse tolerantly rather than relying on fixed token positions.
func darwinSwap() (total, used uint64) {
	out := run("sysctl", "-n", "vm.swapusage")
	return swapAmount(out, "total"), swapAmount(out, "used")
}

// swapAmount returns the byte size that follows "<key> =" in the swapusage line.
func swapAmount(s, key string) uint64 {
	i := strings.Index(s, key)
	if i < 0 {
		return 0
	}
	rest := s[i+len(key):]
	if j := strings.IndexByte(rest, '='); j >= 0 {
		rest = rest[j+1:]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0
	}
	num, unit := fields[0], ""
	if n := len(num); n > 0 {
		switch num[n-1] {
		case 'K', 'k', 'M', 'm', 'G', 'g', 'T', 't', 'B', 'b':
			unit = strings.ToUpper(num[n-1 : n])
			num = num[:n-1]
		}
	}
	if unit == "" && len(fields) > 1 { // unit split off as its own token: "2048.00 M"
		if u := strings.ToUpper(fields[1]); u != "" {
			unit = u[:1]
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 {
		return 0
	}
	switch unit {
	case "T":
		return uint64(v * (1 << 40))
	case "G":
		return uint64(v * (1 << 30))
	case "M":
		return uint64(v * (1 << 20))
	case "K":
		return uint64(v * (1 << 10))
	default:
		return uint64(v) // already bytes
	}
}

// darwinNet sums Ibytes/Obytes per non-loopback interface (first row each).
func darwinNet() (rx, tx uint64, ok bool) {
	out := run("netstat", "-ibn")
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return 0, 0, false
	}
	ib, ob := -1, -1
	for i, h := range strings.Fields(lines[0]) {
		switch h {
		case "Ibytes":
			ib = i
		case "Obytes":
			ob = i
		}
	}
	if ib < 0 || ob < 0 {
		return 0, 0, false
	}
	seen := map[string]bool{}
	for _, ln := range lines[1:] {
		f := strings.Fields(ln)
		if len(f) <= ob {
			continue
		}
		name := f[0]
		if strings.HasPrefix(name, "lo") || seen[name] {
			continue
		}
		seen[name] = true
		r, _ := strconv.ParseUint(f[ib], 10, 64)
		t, _ := strconv.ParseUint(f[ob], 10, 64)
		rx, tx = rx+r, tx+t
	}
	return rx, tx, true
}

// normTCPState normalizes a macOS netstat state token to the canonical state
// name shared with the Linux/Windows collectors (empty = not a real state).
func normTCPState(s string) string {
	switch s {
	case "ESTABLISHED":
		return "ESTABLISHED"
	case "SYN_SENT":
		return "SYN_SENT"
	case "SYN_RCVD", "SYN_RECV":
		return "SYN_RECV"
	case "FIN_WAIT_1", "FIN_WAIT1":
		return "FIN_WAIT1"
	case "FIN_WAIT_2", "FIN_WAIT2":
		return "FIN_WAIT2"
	case "TIME_WAIT":
		return "TIME_WAIT"
	case "CLOSE_WAIT":
		return "CLOSE_WAIT"
	case "LAST_ACK":
		return "LAST_ACK"
	case "LISTEN":
		return "LISTEN"
	case "CLOSING":
		return "CLOSING"
	case "CLOSED", "CLOSE":
		return "CLOSE"
	}
	return ""
}

// darwinConnStats parses `netstat -an -p tcp` (per-state) and `-p udp` (total),
// returning per-proto/state counts plus the established-TCP count for NetConns.
func darwinConnStats() ([]shared.ConnStat, int) {
	tcpStates := map[string]int{}
	for _, ln := range strings.Split(run("netstat", "-an", "-p", "tcp"), "\n") {
		f := strings.Fields(ln)
		if len(f) < 4 || !strings.HasPrefix(f[0], "tcp") {
			continue
		}
		if st := normTCPState(f[len(f)-1]); st != "" {
			tcpStates[st]++
		}
	}
	udpTotal := 0
	for _, ln := range strings.Split(run("netstat", "-an", "-p", "udp"), "\n") {
		f := strings.Fields(ln)
		if len(f) >= 4 && strings.HasPrefix(f[0], "udp") {
			udpTotal++
		}
	}
	out := make([]shared.ConnStat, 0, len(tcpStates)+1)
	for st, n := range tcpStates {
		out = append(out, shared.ConnStat{Proto: "tcp", State: st, Count: n})
	}
	if udpTotal > 0 {
		out = append(out, shared.ConnStat{Proto: "udp", Count: udpTotal})
	}
	return out, tcpStates["ESTABLISHED"]
}

func darwinLoad() (l1, l5, l15 float64) {
	s := strings.NewReplacer("{", "", "}", "").Replace(run("sysctl", "-n", "vm.loadavg"))
	f := strings.Fields(s)
	if len(f) >= 3 {
		l1, _ = strconv.ParseFloat(f[0], 64)
		l5, _ = strconv.ParseFloat(f[1], 64)
		l15, _ = strconv.ParseFloat(f[2], 64)
	}
	return round2(l1), round2(l5), round2(l15)
}

func darwinProcCount() int {
	n := 0
	for _, ln := range strings.Split(run("ps", "-A", "-o", "pid="), "\n") {
		if strings.TrimSpace(ln) != "" {
			n++
		}
	}
	return n
}

// darwinProcNames returns unique process command names via ps, capped at 256.
func darwinProcNames() []string {
	out := run("ps", "-A", "-o", "comm=")
	if out == "" {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, ln := range strings.Split(out, "\n") {
		name := strings.TrimSpace(ln)
		if name == "" || seen[name] {
			continue
		}
		// ps comm may include full path; take basename
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
		if len(names) >= maxProcessNames {
			break
		}
	}
	return names
}

func darwinUptime() uint64 {
	s := run("sysctl", "-n", "kern.boottime")
	i := strings.Index(s, "sec = ")
	if i < 0 {
		return 0
	}
	rest := s[i+len("sec = "):]
	j := strings.IndexAny(rest, ",}")
	if j < 0 {
		return 0
	}
	sec, err := strconv.ParseUint(strings.TrimSpace(rest[:j]), 10, 64)
	if err != nil {
		return 0
	}
	if now := uint64(time.Now().Unix()); now > sec {
		return now - sec
	}
	return 0
}

// darwinDisks returns usage for every /dev-backed volume via `df -kP`. It
// enumerates all disks dynamically (any number), de-duplicates by device (APFS
// shows several synthesized volumes per container) and tolerates mount points
// that contain spaces.
func darwinDisks() []shared.DiskInfo {
	lines := strings.Split(run("df", "-kP"), "\n")
	if len(lines) < 2 {
		return nil
	}
	seen := map[string]bool{}
	var res []shared.DiskInfo
	for _, ln := range lines[1:] {
		f := strings.Fields(ln)
		if len(f) < 6 || !strings.HasPrefix(f[0], "/dev/") || seen[f[0]] {
			continue
		}
		mountPoint := strings.Join(f[5:], " ")
		// Skip /boot and macOS /System volumes (and their sub-mounts). Exact
		// directory match so a data disk like /bootstrap or /Systemx isn't excluded.
		if mountPoint == "/boot" || strings.HasPrefix(mountPoint, "/boot/") ||
			mountPoint == "/System" || strings.HasPrefix(mountPoint, "/System/") {
			continue
		}
		total, _ := strconv.ParseUint(f[1], 10, 64)
		used, _ := strconv.ParseUint(f[2], 10, 64)
		if total == 0 {
			continue
		}
		seen[f[0]] = true
		res = append(res, shared.DiskInfo{
			Path:    mountPoint, // columns 6..n are the mount point
			Total:   total * 1024,
			Used:    used * 1024,
			Percent: round1(float64(used) / float64(total) * 100),
		})
	}
	return res
}

func osVersion() string {
	if v := strings.TrimSpace(run("sw_vers", "-productVersion")); v != "" {
		return "macOS " + v
	}
	return "macOS"
}

func kernelVersion() string {
	return strings.TrimSpace(run("uname", "-r"))
}
