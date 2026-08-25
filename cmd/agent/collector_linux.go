//go:build linux

package main

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"aiops-monitor/shared"
)

var errParse = errors.New("parse error")

type cpuTimes struct{ idle, total uint64 }
type netTotals struct{ rx, tx uint64 }

// linuxCollector reads base metrics straight from procfs and syscalls.
// This is the "high-frequency, close-to-the-system" core the hybrid design
// keeps in Go: no external dependency, tiny footprint, safe to poll often.
//
// Optimization: disk enumeration (which calls syscall.Statfs per mount) and
// process listing (which reads /proc/*/comm for every PID) are cached with
// per-cycle throttling — they change slowly and don't need 10s granularity.
type linuxCollector struct {
	diskPath    string
	prevCPU     cpuTimes
	prevNet     netTotals
	prevNetT    time.Time
	prevDiskIO  diskIOTotals
	prevDiskIOT time.Time
	primed      bool

	// Cached results — refreshed once per multi-cycle cache window.
	diskCache   []shared.DiskInfo
	diskCacheAt time.Time
	procCache   procInfo
	procCacheAt time.Time
	procCacheMu sync.Mutex

	// v5.4.0: security-awareness — track permission errors to avoid log spam.
	secWarned    bool
	secPermPaths []string // procfs paths that returned permission errors
}

type procInfo struct {
	count int
	names []string
}

type diskIOTotals struct{ readBytes, writeBytes, readIOs, writeIOs uint64 }

// diskCacheTTL: disk usage changes slowly; 60s refresh is sufficient.
const diskCacheTTL = 60 * time.Second

// procCacheTTL: process count/names change faster but not every 10s.
const procCacheTTL = 20 * time.Second

func newCollector(diskPath string) Collector {
	if diskPath == "" {
		diskPath = "/"
	}
	return &linuxCollector{diskPath: diskPath}
}

func (c *linuxCollector) Name() string    { return "linux-procfs" }
func (c *linuxCollector) Supported() bool { return true }

func (c *linuxCollector) Collect() (shared.Metrics, error) {
	now := time.Now()
	m := shared.Metrics{CPUCores: runtime.NumCPU()}

	// v5.4.0: Track permission errors across all collection points for diagnostics.
	var permErrors []string

	if ct, err := readCPUTimes(); err == nil {
		if c.primed && ct.total > c.prevCPU.total {
			totalDelta := ct.total - c.prevCPU.total
			// idle 里折算了 iowait，而内核文档明确 iowait **可以回退**（CPU 热插拔同理）。
			// 直接相减会 uint64 下溢成天文数字 → totalDelta-idleDelta 再次回绕 →
			// CPU% 变成 -9.2e17 这种脏数据，污染图表/告警/AI 基线。故双向夹紧。
			idleDelta := uint64(0)
			if ct.idle > c.prevCPU.idle {
				idleDelta = ct.idle - c.prevCPU.idle
			}
			if idleDelta > totalDelta {
				idleDelta = totalDelta
			}
			m.CPUPercent = round1(float64(totalDelta-idleDelta) / float64(totalDelta) * 100)
		}
		c.prevCPU = ct
	} else if isPermissionError(err) {
		permErrors = append(permErrors, "/proc/stat")
	}

	if mi, err := readMemInfo(); err == nil {
		if mi.memTotal > 0 {
			used := mi.memTotal - mi.memAvail
			m.MemTotal = mi.memTotal
			m.MemUsed = used
			m.MemPercent = round1(float64(used) / float64(mi.memTotal) * 100)
		}
		if mi.swapTotal > 0 {
			sused := mi.swapTotal - mi.swapFree
			m.SwapTotal = mi.swapTotal
			m.SwapUsed = sused
			m.SwapPercent = round1(float64(sused) / float64(mi.swapTotal) * 100)
		}
		// Containers: prefer cgroup v2 memory limit so demos don't look like host RAM.
		if lim, usage, ok := readCgroupMemory(); ok && lim > 0 {
			m.MemTotal = lim
			if usage > lim {
				usage = lim
			}
			m.MemUsed = usage
			m.MemPercent = round1(float64(usage) / float64(lim) * 100)
		}
	} else if isPermissionError(err) {
		permErrors = append(permErrors, "/proc/meminfo")
	}

	var st syscall.Statfs_t
	if err := syscall.Statfs(c.diskPath, &st); err == nil && st.Blocks > 0 {
		bsize := uint64(st.Bsize)
		total := st.Blocks * bsize
		// Bavail = space available to non-root (matches df); Bfree includes reserved blocks.
		avail := st.Bavail * bsize
		used := total - avail
		if used > total {
			used = total - st.Bfree*bsize
		}
		m.DiskTotal = total
		m.DiskUsed = used
		m.DiskPercent = round1(float64(used) / float64(total) * 100)
	} else if err != nil && isPermissionError(err) {
		permErrors = append(permErrors, c.diskPath+" (statfs)")
	}

	// Cached disk enumeration: syscall.Statfs per mount is expensive and disk
	// usage doesn't change meaningfully in <60s. Cache until TTL expires.
	if c.diskCacheAt.IsZero() || time.Since(c.diskCacheAt) > diskCacheTTL {
		c.diskCache = enumLinuxDisks()
		c.diskCacheAt = now
	}
	m.Disks = c.diskCache

	// Disk: syscall.Statfs on the primary disk path (fast, always refreshed)
	if nt, err := readNet(); err == nil {
		if c.primed && !c.prevNetT.IsZero() {
			if elapsed := now.Sub(c.prevNetT).Seconds(); elapsed > 0 {
				m.NetSentRate = round1(rate(nt.tx, c.prevNet.tx, elapsed))
				m.NetRecvRate = round1(rate(nt.rx, c.prevNet.rx, elapsed))
			}
		}
		c.prevNet = nt
		c.prevNetT = now
	} else if isPermissionError(err) {
		permErrors = append(permErrors, "/proc/net/dev")
	}

	m.Conns, m.NetConns = collectConnStats()

	// Disk IO: read/write rates from /proc/diskstats
	if dio, err := readDiskIO(); err == nil {
		if c.primed && !c.prevDiskIOT.IsZero() {
			if elapsed := now.Sub(c.prevDiskIOT).Seconds(); elapsed > 0 {
				m.DiskReadRate = round1(rate(dio.readBytes, c.prevDiskIO.readBytes, elapsed))
				m.DiskWriteRate = round1(rate(dio.writeBytes, c.prevDiskIO.writeBytes, elapsed))
				// IOPS: read/write operations per second
				m.DiskReadIOPS = round1(rate(dio.readIOs, c.prevDiskIO.readIOs, elapsed))
				m.DiskWriteIOPS = round1(rate(dio.writeIOs, c.prevDiskIO.writeIOs, elapsed))
				// IO util: rough estimate based on total bytes throughput vs typical disk bandwidth
				totalRate := m.DiskReadRate + m.DiskWriteRate
				m.DiskIOUtilPercent = round1(totalRate / 200e6 * 100) // 200 MB/s as reference max
				if m.DiskIOUtilPercent > 100 {
					m.DiskIOUtilPercent = 100
				}
			}
		}
		c.prevDiskIO = dio
		c.prevDiskIOT = now
	}

	if l1, l5, l15, err := readLoadAvg(); err == nil {
		m.Load1, m.Load5, m.Load15 = l1, l5, l15
	} else if isPermissionError(err) {
		permErrors = append(permErrors, "/proc/loadavg")
	}

	// Process count + names: cached to avoid reading /proc twice (countProcs
	// and listProcNames each call ReadDir + open comm files independently).
	c.procCacheMu.Lock()
	if c.procCacheAt.IsZero() || time.Since(c.procCacheAt) > procCacheTTL {
		c.procCache = readProcInfo()
		c.procCacheAt = now
	}
	pi := c.procCache
	c.procCacheMu.Unlock()
	m.ProcCount = pi.count
	m.ProcessNames = pi.names
	if up, err := readUptime(); err == nil {
		m.Uptime = up
	} else if isPermissionError(err) {
		permErrors = append(permErrors, "/proc/uptime")
	}
	m.GPUs = cachedGPUs(linuxGPUs)

	// v5.4.0: Permission error diagnostics — log once with actionable guidance.
	if len(permErrors) > 0 && !c.secWarned {
		c.secWarned = true
		c.secPermPaths = permErrors
		slog.Error("数据采集权限不足：部分 /proc 路径被安全模块拦截",
			"blocked_paths", permErrors,
			"hint", "请检查 kysec/SELinux/AppArmor 配置，或尝试以下方法:",
			"fix_1", "sudo setenforce 0  (临时关闭 SELinux，仅用于排查)",
			"fix_2", "sudo kysec_set -m permissive  (临时切换 kysec 为宽容模式)",
			"fix_3", "以 root 身份运行 Agent: sudo ./aiops-agent",
			"fix_4", "为 Agent 添加 kysec 白名单: sudo kysec_adm -a /path/to/aiops-agent",
		)
	}

	c.primed = true
	return m, nil
}

// linuxGPUs prefers nvidia-smi (NVIDIA), then falls back to the amdgpu sysfs
// interface. Returns nil when neither is available.
func linuxGPUs() []shared.GPUInfo {
	if g := nvidiaSmiGPUs(); len(g) > 0 {
		return g
	}
	return amdSysfsGPUs()
}

// amdSysfsGPUs reads utilization and VRAM from /sys/class/drm/card*/device,
// exposed by the amdgpu kernel driver. No third-party dependency.
func amdSysfsGPUs() []shared.GPUInfo {
	var gpus []shared.GPUInfo
	for i := 0; i < 8; i++ {
		base := "/sys/class/drm/card" + strconv.Itoa(i) + "/device"
		busy, err := os.ReadFile(base + "/gpu_busy_percent")
		if err != nil {
			continue
		}
		util, _ := strconv.ParseFloat(strings.TrimSpace(string(busy)), 64)
		g := shared.GPUInfo{Name: "GPU card" + strconv.Itoa(i), UtilPercent: round1(util)}
		if ub, err := os.ReadFile(base + "/mem_info_vram_used"); err == nil {
			if tb, err := os.ReadFile(base + "/mem_info_vram_total"); err == nil {
				used, _ := strconv.ParseUint(strings.TrimSpace(string(ub)), 10, 64)
				total, _ := strconv.ParseUint(strings.TrimSpace(string(tb)), 10, 64)
				g.MemUsed, g.MemTotal = used, total
				if total >= used {
					g.MemFree = total - used
				}
				if total > 0 {
					g.MemPercent = round1(float64(used) / float64(total) * 100)
				}
			}
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// ---- procfs helpers ----

func readCPUTimes() (cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 5 && fields[0] == "cpu" {
			var total, idle uint64
			for i := 1; i < len(fields); i++ {
				v, e := strconv.ParseUint(fields[i], 10, 64)
				if e != nil {
					continue
				}
				total += v
				if i == 4 || i == 5 { // idle + iowait
					idle += v
				}
			}
			return cpuTimes{idle: idle, total: total}, nil
		}
	}
	return cpuTimes{}, errParse
}

type memInfo struct {
	memTotal, memAvail, swapTotal, swapFree uint64
	memFree, buffers, cached                uint64
}

func readMemInfo() (memInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return memInfo{}, err
	}
	defer f.Close()
	return parseMemInfoScanner(bufio.NewScanner(f)), nil
}

// parseMemInfoScanner parses /proc/meminfo lines. When MemAvailable is missing
// (older CentOS/RHEL kernels), fall back to MemFree+Buffers+Cached so usage
// does not appear as ~100%.
func parseMemInfoScanner(sc *bufio.Scanner) memInfo {
	var mi memInfo
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			mi.memTotal = parseMeminfoKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			mi.memAvail = parseMeminfoKB(line)
		case strings.HasPrefix(line, "MemFree:"):
			mi.memFree = parseMeminfoKB(line)
		case strings.HasPrefix(line, "Buffers:"):
			mi.buffers = parseMeminfoKB(line)
		case strings.HasPrefix(line, "Cached:"):
			mi.cached = parseMeminfoKB(line)
		case strings.HasPrefix(line, "SwapTotal:"):
			mi.swapTotal = parseMeminfoKB(line)
		case strings.HasPrefix(line, "SwapFree:"):
			mi.swapFree = parseMeminfoKB(line)
		}
	}
	if mi.memAvail == 0 && mi.memTotal > 0 {
		mi.memAvail = mi.memFree + mi.buffers + mi.cached
		if mi.memAvail > mi.memTotal {
			mi.memAvail = mi.memTotal
		}
	}
	return mi
}

// readCgroupMemory returns (limitBytes, usageBytes, ok) from cgroup v2 when the
// process is constrained (Docker/K8s). "max" / missing limit → ok=false.
func readCgroupMemory() (limit, usage uint64, ok bool) {
	// cgroup v2 unified hierarchy
	for _, base := range []string{"/sys/fs/cgroup", "/sys/fs/cgroup/memory"} {
		limPath := base + "/memory.max"
		if base == "/sys/fs/cgroup/memory" {
			limPath = base + "/memory.limit_in_bytes"
		}
		b, err := os.ReadFile(limPath)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(b))
		if s == "" || s == "max" {
			continue
		}
		lim, err := strconv.ParseUint(s, 10, 64)
		if err != nil || lim == 0 || lim >= (1<<62) {
			continue // unbound / nonsense
		}
		usagePath := base + "/memory.current"
		if base == "/sys/fs/cgroup/memory" {
			usagePath = base + "/memory.usage_in_bytes"
		}
		ub, err := os.ReadFile(usagePath)
		if err != nil {
			continue
		}
		u, err := strconv.ParseUint(strings.TrimSpace(string(ub)), 10, 64)
		if err != nil {
			continue
		}
		return lim, u, true
	}
	return 0, 0, false
}

func parseMeminfoKB(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		return v * 1024 // kB -> bytes
	}
	return 0
}

func readNet() (netTotals, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return netTotals{}, err
	}
	defer f.Close()
	var nt netTotals
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue // header lines
		}
		if strings.TrimSpace(line[:idx]) == "lo" {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) >= 9 {
			rx, _ := strconv.ParseUint(fields[0], 10, 64) // recv bytes
			tx, _ := strconv.ParseUint(fields[8], 10, 64) // trans bytes
			nt.rx += rx
			nt.tx += tx
		}
	}
	return nt, nil
}

// tcpStateNames maps the Linux procfs hex state code (column index 3 of
// /proc/net/tcp*) to a canonical TCP state name.
var tcpStateNames = map[string]string{
	"01": "ESTABLISHED", "02": "SYN_SENT", "03": "SYN_RECV", "04": "FIN_WAIT1",
	"05": "FIN_WAIT2", "06": "TIME_WAIT", "07": "CLOSE", "08": "CLOSE_WAIT",
	"09": "LAST_ACK", "0A": "LISTEN", "0B": "CLOSING",
}

// collectConnStats reads /proc/net/{tcp,tcp6,udp,udp6} and returns per-state TCP
// counts plus a single UDP total, along with the established-TCP count (for the
// legacy NetConns field). Zero external dependencies — pure procfs.
func collectConnStats() ([]shared.ConnStat, int) {
	tcpStates := map[string]int{}
	udpTotal := 0
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		forEachNetRow(path, func(fields []string) {
			if len(fields) >= 4 {
				st := tcpStateNames[strings.ToUpper(fields[3])]
				if st == "" {
					st = "OTHER"
				}
				tcpStates[st]++
			}
		})
	}
	for _, path := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		forEachNetRow(path, func(fields []string) { udpTotal++ })
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

// forEachNetRow scans a /proc/net/{tcp,udp}* file, calling fn with the
// whitespace-split fields of each data row (the header line is skipped).
func forEachNetRow(path string, fn func(fields []string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false // skip header
			continue
		}
		fn(strings.Fields(sc.Text()))
	}
}

func readLoadAvg() (l1, l5, l15 float64, err error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) >= 3 {
		l1, _ = strconv.ParseFloat(fields[0], 64)
		l5, _ = strconv.ParseFloat(fields[1], 64)
		l15, _ = strconv.ParseFloat(fields[2], 64)
		return round2(l1), round2(l5), round2(l15), nil
	}
	return 0, 0, 0, errParse
}

func readUptime() (uint64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) >= 1 {
		v, e := strconv.ParseFloat(fields[0], 64)
		return uint64(v), e
	}
	return 0, errParse
}

// readProcInfo returns the process count and unique process names from /proc,
// merging what was previously two independent scans (countProcs + listProcNames)
// into a single ReadDir pass — halving /proc directory reads.
// When /proc/[pid]/comm is blocked by security modules, it degrades to
// /proc/[pid]/cmdline (which often has more permissive access controls).
func readProcInfo() procInfo {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return procInfo{}
	}
	seen := map[string]bool{}
	var names []string
	restricted := false
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		allDigit := true
		for _, r := range e.Name() {
			if r < '0' || r > '9' {
				allDigit = false
				break
			}
		}
		if !allDigit {
			continue
		}
		count++
		if len(names) < maxProcessNames {
			name := readProcNameDegraded(e.Name())
			if name == "restricted" {
				restricted = true
				continue
			}
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	// If all comm reads were blocked, mark as "restricted" so the server
	// knows it's a permission issue, not an empty process list.
	if restricted && len(names) == 0 {
		names = append(names, "restricted")
	}
	return procInfo{count: count, names: names}
}

// readProcNameDegraded reads the process name for a given PID, falling back
// from /proc/[pid]/comm to /proc/[pid]/cmdline when security modules block
// comm access. Returns "restricted" if both fail due to permissions.
func readProcNameDegraded(pid string) string {
	// Primary: /proc/[pid]/comm (cleaner, just the process name)
	b, err := os.ReadFile("/proc/" + pid + "/comm")
	if err == nil && len(b) > 0 {
		return strings.TrimSpace(string(b))
	}
	// Degraded: /proc/[pid]/cmdline (argv[0], may include path)
	if isPermissionError(err) {
		cb, cerr := os.ReadFile("/proc/" + pid + "/cmdline")
		if cerr == nil && len(cb) > 0 {
			// cmdline is null-separated; take the first field (program name)
			cmdline := string(cb)
			if idx := strings.IndexByte(cmdline, 0); idx > 0 {
				cmdline = cmdline[:idx]
			}
			// Extract basename from path
			if idx := strings.LastIndexByte(cmdline, '/'); idx >= 0 {
				cmdline = cmdline[idx+1:]
			}
			return strings.TrimSpace(cmdline)
		}
		if isPermissionError(cerr) {
			return "restricted"
		}
	}
	return ""
}

// enumLinuxDisks returns usage for every real filesystem mount, de-duplicated
// by backing device and skipping pseudo filesystems. Container overlay roots
// and network mounts (NFS/ZFS/…) are included so demos are not empty.
func enumLinuxDisks() []shared.DiskInfo {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	return enumLinuxDisksFromMounts(f)
}

func enumLinuxDisksFromMounts(r io.Reader) []shared.DiskInfo {
	seen := map[string]bool{}
	var out []shared.DiskInfo
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		dev, mount, fstype := fields[0], fields[1], fields[2]
		if !includeLinuxMount(dev, mount, fstype) {
			continue
		}
		key := dev + "|" + mount
		if seen[key] {
			continue
		}
		if strings.HasPrefix(dev, "/dev/") && seen[dev] {
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil || st.Blocks == 0 {
			continue
		}
		seen[key] = true
		if strings.HasPrefix(dev, "/dev/") {
			seen[dev] = true
		}
		bsize := uint64(st.Bsize)
		total := st.Blocks * bsize
		avail := st.Bavail * bsize
		used := total - avail
		if used > total {
			used = total - st.Bfree*bsize
		}
		out = append(out, shared.DiskInfo{
			Path:    unescapeMount(mount),
			Total:   total,
			Used:    used,
			Percent: round1(float64(used) / float64(total) * 100),
		})
	}
	return out
}

// containerVolumeRoots 是容器运行时与 kubelet 的数据目录。
//
// 它们**下面**的挂载点是每容器/每 Pod 的临时产物：/var/lib/kubelet/pods/<uid>/volumes/…、
// /var/lib/docker/containers/<id>/mounts/…。一台跑着几十个 Pod 的节点会因此多出几十上百
// 条"磁盘"，把主机卡片和列表刷满，而且它们绝大多数只是节点根盘的绑定挂载——同一块盘被
// 数了很多遍。运维要看的是节点的盘，不是每个 Pod 的卷。
var containerVolumeRoots = []string{
	"/var/lib/docker",
	"/var/lib/containers",
	"/var/lib/containerd",
	"/run/containerd",
	"/var/lib/kubelet",
	"/var/lib/rancher",
	"/run/k3s",
	"/var/lib/crio",
	"/run/crio",
	"/var/lib/origin",
	"/var/snap/microk8s",
}

// isContainerNestedMount 报告挂载点是否落在容器运行时数据目录**内部**。
//
// 只排嵌套的，数据目录本身要留着：把一块独立盘挂到 /var/lib/docker 是常见做法，那块盘
// 写满会让整个节点的容器全挂——恰恰是最该盯的一块盘。
func isContainerNestedMount(mount string) bool {
	for _, root := range containerVolumeRoots {
		if strings.HasPrefix(mount, root+"/") {
			return true
		}
	}
	return false
}

// includeLinuxMount decides whether a /proc/mounts row should contribute disk usage.
func includeLinuxMount(dev, mount, fstype string) bool {
	if mount == "/boot" || strings.HasPrefix(mount, "/boot/") {
		return false
	}
	if isContainerNestedMount(mount) {
		return false
	}
	switch fstype {
	case "proc", "sysfs", "tmpfs", "devtmpfs", "cgroup", "cgroup2",
		"squashfs", "autofs", "mqueue", "debugfs", "tracefs", "devpts",
		"fusectl", "pstore", "bpf", "nsfs", "rpc_pipefs", "binfmt_misc":
		return false
	case "overlay":
		// Container rootfs — keep "/" (and similar) so Disks is not empty in Docker.
		return mount == "/" || mount == "/app" || mount == "/app/data"
	}
	if strings.HasPrefix(dev, "/dev/") {
		return true
	}
	// Network / pooled FS without a /dev/ node (NFS, ZFS dataset, virtiofs, 9p…).
	switch fstype {
	case "nfs", "nfs4", "zfs", "fuse.sshfs", "fuse.rclone", "virtiofs", "9p", "cifs", "smb3":
		return true
	}
	return false
}

// unescapeMount decodes the octal escapes procfs uses inside mount points
// (space -> \040, tab -> \011, newline -> \012, backslash -> \134).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

// isLinuxDiskPartition reports partition-style names that already have a parent
// whole-disk row in diskstats (sda1, vda2, nvme0n1p3, mmcblk0p1).
func isLinuxDiskPartition(dev string) bool {
	if strings.HasPrefix(dev, "nvme") || strings.HasPrefix(dev, "mmcblk") {
		idx := strings.LastIndex(dev, "p")
		if idx <= 0 || idx >= len(dev)-1 {
			return false
		}
		for _, c := range dev[idx+1:] {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	i := len(dev)
	for i > 0 && dev[i-1] >= '0' && dev[i-1] <= '9' {
		i--
	}
	if i == 0 || i == len(dev) {
		return false
	}
	base := dev[:i]
	if len(base) == 3 {
		p := base[:2]
		return p == "sd" || p == "vd" || p == "hd"
	}
	return strings.HasPrefix(base, "xvd") && len(base) >= 4
}

func osVersion() string {
	if p := linuxPrettyName(); p != "" {
		return p
	}
	return "Linux"
}

func kernelVersion() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readDiskIO reads cumulative read/write bytes from /proc/diskstats for all
// non-loop, non-ram physical/virtual disks.
func readDiskIO() (diskIOTotals, error) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return diskIOTotals{}, err
	}
	defer f.Close()
	var dio diskIOTotals
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 14 {
			continue
		}
		dev := fields[2]
		// Skip loopback, ram, dm, and partitions (sda1/nvme0n1p2) so we do not
		// double-count whole-disk + partition rows from /proc/diskstats.
		if strings.HasPrefix(dev, "loop") || strings.HasPrefix(dev, "ram") || strings.HasPrefix(dev, "dm-") {
			continue
		}
		if isLinuxDiskPartition(dev) {
			continue
		}
		// Field indices (Linux kernel Documentation/iostats.txt):
		//   5 = sectors read, 9 = sectors written
		//   7 = read IOs completed, 11 = write IOs completed
		rdSec, _ := strconv.ParseUint(fields[5], 10, 64)
		wrSec, _ := strconv.ParseUint(fields[9], 10, 64)
		rdIO, _ := strconv.ParseUint(fields[7], 10, 64)
		wrIO, _ := strconv.ParseUint(fields[11], 10, 64)
		dio.readBytes += rdSec * 512
		dio.writeBytes += wrSec * 512
		dio.readIOs += rdIO
		dio.writeIOs += wrIO
	}
	return dio, nil
}
