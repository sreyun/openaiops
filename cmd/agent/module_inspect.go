package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// host_inspect：跨平台深度主机巡检（对齐 linux_inspect.sh 的结构化报告思路）。
// 输出纯 JSON，供服务端存储与 Web 渲染。覆盖 Windows / Linux（含麒麟/UOS）/ macOS。
//
// Windows 中文环境：cmdOut 经 ensureUTF8（GBK→UTF-8）；FQDN/系统/内核走专用采集，
// 不再使用 hostname -f / 本地化 ver 文案填内核。修复乱码需升级目标机 Agent 后重跑巡检。

type inspectReport struct {
	Version        string            `json:"version"`
	Timestamp      string            `json:"timestamp"`
	ElapsedSeconds float64           `json:"elapsed_seconds"`
	Host           inspectHost       `json:"host"`
	Metrics        inspectMetrics    `json:"metrics"`
	Sections       []inspectSection  `json:"sections"`
	Findings       []inspectFinding  `json:"findings"`
	Result         inspectResult     `json:"result"`
	Thresholds     inspectThresholds `json:"thresholds"`
}

type inspectHost struct {
	Hostname      string `json:"hostname"`
	FQDN          string `json:"fqdn,omitempty"`
	IP            string `json:"ip"`
	OS            string `json:"os"`
	OSFamily      string `json:"os_family"`
	DistroID      string `json:"distro_id,omitempty"`      // rocky / kylin / ubuntu …
	DistroVersion string `json:"distro_version,omitempty"` // major: 9 / 10 / 11
	PkgFamily     string `json:"pkg_family,omitempty"`     // rpm / deb / apk …
	GOOS          string `json:"goos"`
	Kernel        string `json:"kernel"`
	Arch          string `json:"arch"`
	UptimeDays    int    `json:"uptime_days"`
	VirtType      string `json:"virt_type,omitempty"`
	Firewall      string `json:"firewall,omitempty"`
	Timezone      string `json:"timezone,omitempty"`
}

type inspectMetrics struct {
	CPUUsagePct    float64 `json:"cpu_usage_pct"`
	CPUCores       int     `json:"cpu_cores"`
	Load1m         float64 `json:"load_1m"`
	Load5m         float64 `json:"load_5m"`
	Load15m        float64 `json:"load_15m"`
	MemUsagePct    float64 `json:"mem_usage_pct"`
	SwapUsagePct   float64 `json:"swap_usage_pct"`
	DiskAlertCount int     `json:"disk_alert_count"`
	InodeAlertCnt  int     `json:"inode_alert_count,omitempty"`
	FDUsagePct     float64 `json:"fd_usage_pct,omitempty"`
	TCPConnections int     `json:"tcp_connections"`
	TCPListen      int     `json:"tcp_listen"`
	TCPCloseWait   int     `json:"tcp_close_wait,omitempty"`
	ProcessCount   int     `json:"process_count"`
	ZombieCount    int     `json:"zombie_count"`
	DStateCount    int     `json:"d_state_count,omitempty"`
	OOMCount       int     `json:"oom_count,omitempty"`
	SSLExpiring    int     `json:"ssl_expiring,omitempty"`
	SSLExpired     int     `json:"ssl_expired,omitempty"`
	ContainerCount int     `json:"container_count,omitempty"`
}

type inspectThresholds struct {
	CPUWarn   float64 `json:"cpu_warn"`
	MemWarn   float64 `json:"mem_warn"`
	DiskWarn  float64 `json:"disk_warn"`
	InodeWarn float64 `json:"inode_warn"`
	SwapWarn  float64 `json:"swap_warn"`
	FDWarn    float64 `json:"fd_warn"`
	LoadMult  float64 `json:"load_mult"`
	SSLDays   int     `json:"ssl_days_warn"`
}

type inspectSection struct {
	ID      string        `json:"id"`
	Title   string        `json:"title"`
	Status  string        `json:"status"` // ok|warn|crit|info|skip
	Summary string        `json:"summary,omitempty"`
	Items   []inspectItem `json:"items,omitempty"`
}

type inspectItem struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Status string `json:"status,omitempty"`
}

type inspectFinding struct {
	Level   string `json:"level"` // warn|crit
	Message string `json:"message"`
	Section string `json:"section,omitempty"`
}

type inspectResult struct {
	Warnings int `json:"warnings"`
	Critical int `json:"critical"`
	ExitCode int `json:"exit_code"`
}

type inspectBuilder struct {
	rep     inspectReport
	profile string // quick | standard | deep
}

func moduleHostInspect(args map[string]string) ([]byte, int) {
	return moduleHostInspectCtx(context.Background(), args)
}

func moduleHostInspectCtx(ctx context.Context, args map[string]string) ([]byte, int) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return []byte(`{"error":"cancelled"}`), 130
	}
	start := time.Now()
	profile := "standard"
	if args != nil {
		switch strings.ToLower(strings.TrimSpace(args["profile"])) {
		case "quick", "fast":
			profile = "quick"
		case "deep", "full":
			profile = "deep"
		case "standard", "std", "":
			profile = "standard"
		default:
			profile = "standard"
		}
	}
	if runtime.GOOS == "windows" {
		// One PowerShell cold-start for disk/mem/cpu/model/boot before collectors fan out.
		winEnsureBasics()
	}
	b := &inspectBuilder{
		profile: profile,
		rep: inspectReport{
			Version:   "2.0",
			Timestamp: time.Now().Format(time.RFC3339),
			Thresholds: inspectThresholds{
				CPUWarn: 80, MemWarn: 85, DiskWarn: 85, InodeWarn: 85,
				SwapWarn: 50, FDWarn: 80, LoadMult: 2.0, SSLDays: 30,
			},
		},
	}
	check := func() bool { return ctx.Err() == nil }
	// 基础（全平台）
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectHost()
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectCPU()
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectMem()
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectDisk()
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectNet()
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectProcess()
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectServices()
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectSecurity()
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectTime()
	// 深度（Linux 为主；其它 OS 返回 skip）
	if profile != "quick" {
		if !check() {
			return []byte(`{"error":"cancelled"}`), 130
		}
		b.collectInode()
		b.collectFD()
		b.collectDiskIO()
		if !check() {
			return []byte(`{"error":"cancelled"}`), 130
		}
		b.collectContainers()
		b.collectCron()
		b.collectKernel()
		if !check() {
			return []byte(`{"error":"cancelled"}`), 130
		}
		b.collectLogs()
		b.collectSSL()
	}
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	if profile == "deep" {
		b.collectLargeFiles()
		if !check() {
			return []byte(`{"error":"cancelled"}`), 130
		}
		b.collectUpdates()
	} else if profile == "standard" {
		// standard：轻量更新探测（仅本地缓存，不联网 apt update / yum check-update）
		b.collectUpdatesLight()
	}
	if !check() {
		return []byte(`{"error":"cancelled"}`), 130
	}
	b.collectRecommend()
	b.finalize(start)
	out, err := json.Marshal(b.rep)
	if err != nil {
		return []byte(`{"error":"marshal failed"}`), 1
	}
	// 退出码与报告 result.exit_code 对齐：严重=2，警告=1，正常=0
	return out, b.rep.Result.ExitCode
}

func (b *inspectBuilder) addFinding(level, section, msg string) {
	b.rep.Findings = append(b.rep.Findings, inspectFinding{Level: level, Section: section, Message: msg})
	if level == "crit" {
		b.rep.Result.Critical++
	} else {
		b.rep.Result.Warnings++
	}
}

func (b *inspectBuilder) worst(a, c string) string {
	rank := map[string]int{"ok": 0, "info": 0, "skip": 0, "warn": 1, "crit": 2}
	if rank[c] > rank[a] {
		return c
	}
	return a
}

func (b *inspectBuilder) finalize(start time.Time) {
	b.rep.ElapsedSeconds = time.Since(start).Seconds()
	status := "ok"
	for _, s := range b.rep.Sections {
		status = b.worst(status, s.Status)
	}
	_ = status
	if b.rep.Result.Critical > 0 {
		b.rep.Result.ExitCode = 2
	} else if b.rep.Result.Warnings > 0 {
		b.rep.Result.ExitCode = 1
	}
}

func (b *inspectBuilder) collectHost() {
	hn, _ := os.Hostname()
	fqdn := inspectResolveFQDN(hn)
	ips := localIPv4s()
	ip := primaryIP()
	if ip == "" && len(ips) > 0 {
		ip = ips[0]
	}
	family, pretty, kernel := detectOSFamily()
	distroID, distroVer, pkgFam := "", "", ""
	if runtime.GOOS == "linux" {
		d := detectLinuxDistro()
		distroID, distroVer, pkgFam = d.ID, d.Version, d.Pkg
		if d.Family != "" {
			family = d.Family
		}
		if d.Pretty != "" {
			pretty = d.Pretty
		}
	}
	uptimeDays := uptimeDays()
	tz := detectTimezone()
	virt := detectVirt()
	fw := detectFirewall()
	b.rep.Host = inspectHost{
		Hostname: hn, FQDN: fqdn, IP: ip, OS: pretty, OSFamily: family,
		DistroID: distroID, DistroVersion: distroVer, PkgFamily: pkgFam,
		GOOS: runtime.GOOS, Kernel: kernel, Arch: runtime.GOARCH, UptimeDays: uptimeDays,
		VirtType: virt, Firewall: fw, Timezone: tz,
	}
	items := []inspectItem{
		{Label: "主机名", Value: hn},
		{Label: "FQDN", Value: fqdn},
		{Label: "IP", Value: strings.Join(ips, " ")},
		{Label: "系统", Value: pretty},
		{Label: "系统族", Value: family},
	}
	if distroID != "" {
		items = append(items, inspectItem{Label: "发行版", Value: distroID})
	}
	if distroVer != "" {
		items = append(items, inspectItem{Label: "主版本", Value: distroVer})
	}
	if pkgFam != "" {
		items = append(items, inspectItem{Label: "包管理族", Value: pkgFam})
	}
	items = append(items,
		inspectItem{Label: "内核", Value: kernel},
		inspectItem{Label: "架构", Value: runtime.GOARCH},
		inspectItem{Label: "运行天数", Value: fmt.Sprintf("%d", uptimeDays)},
		inspectItem{Label: "虚拟化", Value: virt},
		inspectItem{Label: "防火墙", Value: fw},
		inspectItem{Label: "时区", Value: tz},
		inspectItem{Label: "巡检档位", Value: b.profile},
	)
	if runtime.GOOS == "linux" {
		if v := strings.TrimSpace(readFileTrim("/sys/class/dmi/id/sys_vendor")); v != "" {
			items = append(items, inspectItem{Label: "厂商", Value: v})
		}
		if p := strings.TrimSpace(readFileTrim("/sys/class/dmi/id/product_name")); p != "" {
			items = append(items, inspectItem{Label: "型号", Value: p})
		}
	}
	b.rep.Sections = append(b.rep.Sections, inspectSection{
		ID: "host", Title: "主机概览", Status: "ok", Summary: pretty + " · " + family + " · " + b.profile, Items: items,
	})
}

func readFileTrim(p string) string {
	raw, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func detectOSFamily() (family, pretty, kernel string) {
	kernel = runtime.GOOS + "/" + runtime.GOARCH
	switch runtime.GOOS {
	case "windows":
		family = "windows"
		pretty, kernel = inspectWindowsOSIdentity()
		return
	case "darwin":
		family = "darwin"
		pretty = "macOS"
		if out := cmdOut(3, "sw_vers", "-productVersion"); out != "" {
			pretty = "macOS " + strings.TrimSpace(out)
		}
		if out := cmdOut(3, "uname", "-r"); out != "" {
			kernel = strings.TrimSpace(out)
		}
		return
	default:
		if out := cmdOut(3, "uname", "-r"); out != "" {
			kernel = strings.TrimSpace(out)
		}
		d := detectLinuxDistro()
		family, pretty = d.Family, d.Pretty
		if family == "" {
			family = "linux"
		}
		if pretty == "" {
			pretty = "Linux"
		}
		return
	}
}

func readOSRelease() (id, idLike, ver, pretty string) {
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(ln, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			id = v
		case "ID_LIKE":
			idLike = v
		case "VERSION_ID":
			ver = v
		case "PRETTY_NAME":
			pretty = v
		}
	}
	return
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func uptimeDays() int {
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/uptime")
		if err != nil {
			return 0
		}
		parts := strings.Fields(string(raw))
		if len(parts) == 0 {
			return 0
		}
		sec, _ := strconv.ParseFloat(parts[0], 64)
		return int(sec / 86400)
	case "darwin":
		out := cmdOut(3, "sysctl", "-n", "kern.boottime")
		// { sec = 123, usec = 0 } ...
		if i := strings.Index(out, "sec = "); i >= 0 {
			rest := out[i+6:]
			n := 0
			fmt.Sscanf(rest, "%d", &n)
			if n > 0 {
				return int(time.Since(time.Unix(int64(n), 0)).Hours() / 24)
			}
		}
	case "windows":
		// Prefer batched CIM (same PowerShell as disk/mem/cpu). Fall back to wmic.
		if stamp := winCollectBootStamp(); len(stamp) >= 14 {
			if t, err := time.Parse("20060102150405", stamp[:14]); err == nil {
				return int(time.Since(t).Hours() / 24)
			}
		}
		out := cmdOut(5, "cmd", "/c", "wmic os get lastbootuptime /value")
		for _, ln := range strings.Split(out, "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "LastBootUpTime=") {
				v := strings.TrimPrefix(ln, "LastBootUpTime=")
				if len(v) >= 14 {
					t, err := time.Parse("20060102150405", v[:14])
					if err == nil {
						return int(time.Since(t).Hours() / 24)
					}
				}
			}
		}
	}
	return 0
}

func detectTimezone() string {
	if z, _ := time.Now().Zone(); z != "" {
		if runtime.GOOS == "linux" {
			if out := cmdOut(2, "timedatectl", "show", "-p", "Timezone", "--value"); out != "" {
				return strings.TrimSpace(out)
			}
			if raw, err := os.ReadFile("/etc/timezone"); err == nil {
				return strings.TrimSpace(string(raw))
			}
		}
		return z
	}
	return ""
}

func detectVirt() string {
	switch runtime.GOOS {
	case "linux":
		if out := cmdOut(2, "systemd-detect-virt"); out != "" && !strings.Contains(out, "none") {
			return strings.TrimSpace(out)
		}
		if raw, err := os.ReadFile("/sys/class/dmi/id/product_name"); err == nil {
			return strings.TrimSpace(string(raw))
		}
	case "windows":
		if m := winCollectComputerModel(); m != "" {
			return m
		}
	case "darwin":
		if out := cmdOut(2, "sysctl", "-n", "machdep.cpu.brand_string"); out != "" {
			return "apple/" + strings.TrimSpace(out)
		}
	}
	return "unknown"
}

func detectFirewall() string {
	switch runtime.GOOS {
	case "linux":
		for _, fw := range []string{"firewalld", "ufw", "nftables", "iptables", "SuSEfirewall2"} {
			if out := cmdOut(2, "systemctl", "is-active", fw); strings.TrimSpace(out) == "active" {
				return fw + ":active"
			}
		}
		if out := cmdOut(2, "ufw", "status"); strings.Contains(out, "Status: active") {
			return "ufw:active"
		}
		if fileExists("/usr/sbin/iptables") || fileExists("/sbin/iptables") {
			return "iptables:present"
		}
		return "inactive"
	case "windows":
		out := cmdOut(4, "cmd", "/c", "netsh advfirewall show allprofiles state")
		if strings.Contains(strings.ToLower(out), "on") {
			return "windows-firewall:on"
		}
		return "windows-firewall:check"
	case "darwin":
		out := cmdOut(2, "defaults", "read", "/Library/Preferences/com.apple.alf", "globalstate")
		switch strings.TrimSpace(out) {
		case "0":
			return "alf:off"
		case "1", "2":
			return "alf:on"
		}
	}
	return "unknown"
}

func (b *inspectBuilder) collectCPU() {
	cores := runtime.NumCPU()
	b.rep.Metrics.CPUCores = cores
	st := "ok"
	items := []inspectItem{{Label: "逻辑 CPU", Value: fmt.Sprintf("%d", cores)}}
	var load1, load5, load15, cpuPct float64

	switch runtime.GOOS {
	case "linux":
		if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
			f := strings.Fields(string(raw))
			if len(f) >= 3 {
				load1, _ = strconv.ParseFloat(f[0], 64)
				load5, _ = strconv.ParseFloat(f[1], 64)
				load15, _ = strconv.ParseFloat(f[2], 64)
			}
		}
		cpuPct = sampleLinuxCPU()
	case "darwin":
		if out := cmdOut(3, "sysctl", "-n", "vm.loadavg"); out != "" {
			// { 1.2 1.1 1.0 }
			out = strings.Trim(out, "{} \n")
			f := strings.Fields(out)
			if len(f) >= 3 {
				load1, _ = strconv.ParseFloat(f[0], 64)
				load5, _ = strconv.ParseFloat(f[1], 64)
				load15, _ = strconv.ParseFloat(f[2], 64)
			}
		}
		if out := cmdOut(4, "ps", "-A", "-o", "%cpu"); out != "" {
			sum := 0.0
			for i, ln := range strings.Split(out, "\n") {
				if i == 0 {
					continue
				}
				v, err := strconv.ParseFloat(strings.TrimSpace(ln), 64)
				if err == nil {
					sum += v
				}
			}
			cpuPct = sum / float64(cores)
			if cpuPct > 100 {
				cpuPct = 100
			}
		}
	case "windows":
		cpuPct = winCollectCPULoadPct()
	}

	b.rep.Metrics.CPUUsagePct = round1(cpuPct)
	b.rep.Metrics.Load1m, b.rep.Metrics.Load5m, b.rep.Metrics.Load15m = load1, load5, load15
	items = append(items, inspectItem{Label: "CPU 使用率", Value: fmt.Sprintf("%.1f%%", cpuPct)})
	if load1 > 0 || runtime.GOOS != "windows" {
		items = append(items, inspectItem{Label: "Load 1/5/15", Value: fmt.Sprintf("%.2f / %.2f / %.2f", load1, load5, load15)})
	}
	warnLoad := float64(cores) * b.rep.Thresholds.LoadMult
	if cpuPct >= b.rep.Thresholds.CPUWarn+10 {
		st = "crit"
		b.addFinding("crit", "cpu", fmt.Sprintf("CPU 使用率过高: %.1f%%", cpuPct))
	} else if cpuPct >= b.rep.Thresholds.CPUWarn {
		st = "warn"
		b.addFinding("warn", "cpu", fmt.Sprintf("CPU 使用率偏高: %.1f%%", cpuPct))
	}
	if load1 >= warnLoad*1.2 && cores > 0 {
		st = b.worst(st, "crit")
		b.addFinding("crit", "cpu", fmt.Sprintf("1 分钟负载过高: %.2f (阈值≈%.1f)", load1, warnLoad))
	} else if load1 >= warnLoad && cores > 0 {
		st = b.worst(st, "warn")
		b.addFinding("warn", "cpu", fmt.Sprintf("1 分钟负载偏高: %.2f", load1))
	}
	b.rep.Sections = append(b.rep.Sections, inspectSection{ID: "cpu", Title: "CPU / 负载", Status: st, Items: items})
}

func sampleLinuxCPU() float64 {
	read := func() (idle, total uint64) {
		raw, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0
		}
		line := strings.SplitN(string(raw), "\n", 2)[0]
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			return 0, 0
		}
		var vals []uint64
		for _, x := range f[1:] {
			n, _ := strconv.ParseUint(x, 10, 64)
			vals = append(vals, n)
			total += n
		}
		if len(vals) > 3 {
			idle = vals[3]
		}
		return
	}
	i1, t1 := read()
	time.Sleep(200 * time.Millisecond)
	i2, t2 := read()
	if t2 <= t1 || i2 < i1 {
		return 0
	}
	dt, di := float64(t2-t1), float64(i2-i1)
	return (1 - di/dt) * 100
}

func (b *inspectBuilder) collectMem() {
	st := "ok"
	var memPct, swapPct float64
	var memTotal, memAvail, swapTotal, swapFree uint64
	items := []inspectItem{}

	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/meminfo")
		if err == nil {
			kv := map[string]uint64{}
			for _, ln := range strings.Split(string(raw), "\n") {
				f := strings.Fields(ln)
				if len(f) < 2 {
					continue
				}
				n, _ := strconv.ParseUint(f[1], 10, 64)
				kv[strings.TrimSuffix(f[0], ":")] = n // kB
			}
			memTotal = kv["MemTotal"] * 1024
			memAvail = kv["MemAvailable"] * 1024
			if memAvail == 0 {
				memAvail = (kv["MemFree"] + kv["Buffers"] + kv["Cached"]) * 1024
			}
			swapTotal = kv["SwapTotal"] * 1024
			swapFree = kv["SwapFree"] * 1024
		}
	case "darwin":
		if out := cmdOut(2, "sysctl", "-n", "hw.memsize"); out != "" {
			memTotal, _ = strconv.ParseUint(strings.TrimSpace(out), 10, 64)
		}
		// rough: page size * free pages from vm_stat
		pageSize := uint64(4096)
		if out := cmdOut(2, "pagesize"); out != "" {
			if n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64); err == nil {
				pageSize = n
			}
		}
		freePages := uint64(0)
		for _, ln := range strings.Split(cmdOut(3, "vm_stat"), "\n") {
			if strings.Contains(ln, "Pages free") {
				f := strings.Fields(ln)
				if len(f) > 0 {
					freePages, _ = strconv.ParseUint(strings.TrimSuffix(f[len(f)-1], "."), 10, 64)
				}
			}
		}
		memAvail = freePages * pageSize
	case "windows":
		memTotal, memAvail = winCollectMemBytes()
	}

	if memTotal > 0 {
		used := memTotal - memAvail
		if used > memTotal {
			used = memTotal
		}
		memPct = float64(used) / float64(memTotal) * 100
		items = append(items,
			inspectItem{Label: "内存总量", Value: humanBytes(memTotal)},
			inspectItem{Label: "可用内存", Value: humanBytes(memAvail)},
			inspectItem{Label: "内存使用率", Value: fmt.Sprintf("%.1f%%", memPct)},
		)
	}
	if swapTotal > 0 {
		swapUsed := swapTotal - swapFree
		swapPct = float64(swapUsed) / float64(swapTotal) * 100
		items = append(items, inspectItem{Label: "Swap 使用率", Value: fmt.Sprintf("%.1f%%", swapPct)})
	}
	b.rep.Metrics.MemUsagePct = round1(memPct)
	b.rep.Metrics.SwapUsagePct = round1(swapPct)
	if memPct >= b.rep.Thresholds.MemWarn+10 {
		st = "crit"
		b.addFinding("crit", "mem", fmt.Sprintf("内存使用率过高: %.1f%%", memPct))
	} else if memPct >= b.rep.Thresholds.MemWarn {
		st = "warn"
		b.addFinding("warn", "mem", fmt.Sprintf("内存使用率偏高: %.1f%%", memPct))
	}
	if swapPct >= 80 {
		st = b.worst(st, "crit")
		b.addFinding("crit", "mem", fmt.Sprintf("Swap 使用率过高: %.1f%%", swapPct))
	} else if swapPct >= b.rep.Thresholds.SwapWarn {
		st = b.worst(st, "warn")
		b.addFinding("warn", "mem", fmt.Sprintf("Swap 使用率偏高: %.1f%%", swapPct))
	}
	b.rep.Sections = append(b.rep.Sections, inspectSection{ID: "mem", Title: "内存 / Swap", Status: st, Items: items})
}

func (b *inspectBuilder) collectDisk() {
	st := "ok"
	items := []inspectItem{}
	alert := 0
	type diskRow struct{ mount, size, used, avail, pct string }

	var rows []diskRow
	switch runtime.GOOS {
	case "windows":
		for _, r := range winCollectDiskRows() {
			free, size := r.Free, r.Size
			if size == 0 {
				continue
			}
			used := size - free
			pct := float64(used) / float64(size) * 100
			id := r.ID
			rows = append(rows, diskRow{mount: id, size: humanBytes(size), used: humanBytes(used), avail: humanBytes(free), pct: fmt.Sprintf("%.0f%%", pct)})
			ist := "ok"
			if pct >= b.rep.Thresholds.DiskWarn+10 {
				ist, st, alert = "crit", b.worst(st, "crit"), alert+1
				b.addFinding("crit", "disk", fmt.Sprintf("%s 磁盘使用率过高: %.0f%%", id, pct))
			} else if pct >= b.rep.Thresholds.DiskWarn {
				ist, st, alert = "warn", b.worst(st, "warn"), alert+1
				b.addFinding("warn", "disk", fmt.Sprintf("%s 磁盘使用率偏高: %.0f%%", id, pct))
			}
			items = append(items, inspectItem{Label: id, Value: fmt.Sprintf("%s 已用 / %s (%.0f%%)", humanBytes(used), humanBytes(size), pct), Status: ist})
		}
	default:
		args := []string{"-hP"}
		if runtime.GOOS == "linux" {
			args = []string{"-hPT"}
		}
		out := cmdOut(5, "df", args...)
		for i, ln := range strings.Split(out, "\n") {
			if i == 0 || strings.TrimSpace(ln) == "" {
				continue
			}
			f := strings.Fields(ln)
			if len(f) < 6 {
				continue
			}
			// Linux df -hPT: Filesystem Type Size Used Avail Use% Mounted
			// macOS df -hP: Filesystem Size Used Avail Capacity Mounted
			var fstype, size, used, avail, usep, mount string
			if runtime.GOOS == "linux" && len(f) >= 7 {
				fstype, size, used, avail, usep, mount = f[1], f[2], f[3], f[4], f[5], f[6]
			} else {
				size, used, avail, usep, mount = f[1], f[2], f[3], f[4], strings.Join(f[5:], " ")
			}
			if skipMount(mount, fstype) {
				continue
			}
			pctStr := strings.TrimSuffix(usep, "%")
			pct, _ := strconv.ParseFloat(pctStr, 64)
			ist := "ok"
			if pct >= b.rep.Thresholds.DiskWarn+10 {
				ist, st, alert = "crit", b.worst(st, "crit"), alert+1
				b.addFinding("crit", "disk", fmt.Sprintf("%s 磁盘使用率过高: %.0f%%", mount, pct))
			} else if pct >= b.rep.Thresholds.DiskWarn {
				ist, st, alert = "warn", b.worst(st, "warn"), alert+1
				b.addFinding("warn", "disk", fmt.Sprintf("%s 磁盘使用率偏高: %.0f%%", mount, pct))
			}
			items = append(items, inspectItem{
				Label: mount, Value: fmt.Sprintf("%s 已用 / %s · 可用 %s (%s)", used, size, avail, usep), Status: ist,
			})
			_ = rows
		}
	}
	b.rep.Metrics.DiskAlertCount = alert
	sum := fmt.Sprintf("%d 个挂载点", len(items))
	if alert > 0 {
		sum += fmt.Sprintf("，%d 个超阈值", alert)
	}
	b.rep.Sections = append(b.rep.Sections, inspectSection{ID: "disk", Title: "磁盘空间", Status: st, Summary: sum, Items: items})
}

func skipMount(mount, fstype string) bool {
	ft := strings.ToLower(fstype)
	if strings.HasPrefix(ft, "tmpfs") || ft == "devtmpfs" || ft == "squashfs" || ft == "overlay" || ft == "proc" || ft == "sysfs" {
		return true
	}
	if strings.HasPrefix(mount, "/snap") || strings.HasPrefix(mount, "/run") || strings.HasPrefix(mount, "/sys") || strings.HasPrefix(mount, "/proc") {
		return true
	}
	return false
}

func (b *inspectBuilder) collectNet() {
	st := "ok"
	items := []inspectItem{}
	listen, estab, closeWait, timeWait, synRecv := 0, 0, 0, 0, 0

	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		var ips []string
		for _, a := range addrs {
			ips = append(ips, a.String())
		}
		extra := ""
		if runtime.GOOS == "linux" {
			if oper := readFileTrim("/sys/class/net/" + iface.Name + "/operstate"); oper != "" {
				extra = " · " + oper
			}
			if rx := readFileTrim("/sys/class/net/" + iface.Name + "/statistics/rx_errors"); rx != "" && rx != "0" {
				extra += " · rx_err=" + rx
			}
			if tx := readFileTrim("/sys/class/net/" + iface.Name + "/statistics/tx_errors"); tx != "" && tx != "0" {
				extra += " · tx_err=" + tx
			}
		}
		if len(ips) == 0 && extra == "" {
			continue
		}
		items = append(items, inspectItem{Label: iface.Name, Value: strings.Join(ips, ", ") + extra})
	}

	switch runtime.GOOS {
	case "windows":
		out := cmdOut(8, "cmd", "/c", "netstat -ano")
		for _, ln := range strings.Split(out, "\n") {
			u := strings.ToUpper(ln)
			if strings.Contains(u, "LISTENING") {
				listen++
			}
			if strings.Contains(u, "ESTABLISHED") {
				estab++
			}
			if strings.Contains(u, "CLOSE_WAIT") {
				closeWait++
			}
			if strings.Contains(u, "TIME_WAIT") {
				timeWait++
			}
		}
	default:
		countState := func(state string) int {
			out := cmdOut(4, "ss", "-tn", "state", state)
			if out == "" {
				return 0
			}
			return maxInt(0, strings.Count(out, "\n")-1)
		}
		estab = countState("established")
		closeWait = countState("close-wait")
		timeWait = countState("time-wait")
		synRecv = countState("syn-recv")
		if lo := cmdOut(5, "ss", "-tln"); lo != "" {
			listen = maxInt(0, strings.Count(lo, "\n")-1)
		}
		// 监听端口样例（前 15）
		if lo := cmdOut(5, "ss", "-tlnp"); lo != "" {
			n := 0
			for i, ln := range strings.Split(lo, "\n") {
				if i == 0 || strings.TrimSpace(ln) == "" {
					continue
				}
				n++
				if n > 15 {
					break
				}
				items = append(items, inspectItem{Label: fmt.Sprintf("监听#%d", n), Value: strings.Join(strings.Fields(ln), " ")})
			}
		}
		if gw := cmdOut(2, "ip", "route"); gw != "" {
			for _, ln := range strings.Split(gw, "\n") {
				if strings.HasPrefix(ln, "default") {
					items = append(items, inspectItem{Label: "默认路由", Value: strings.TrimSpace(ln)})
					break
				}
			}
		}
		if dns := cmdOut(2, "grep", "^nameserver", "/etc/resolv.conf"); dns != "" {
			var ns []string
			for _, ln := range strings.Split(dns, "\n") {
				f := strings.Fields(ln)
				if len(f) >= 2 {
					ns = append(ns, f[1])
				}
			}
			if len(ns) > 0 {
				items = append(items, inspectItem{Label: "DNS", Value: strings.Join(ns, " ")})
			}
		}
	}
	conns := estab + closeWait + timeWait + synRecv
	b.rep.Metrics.TCPListen = listen
	b.rep.Metrics.TCPConnections = conns
	b.rep.Metrics.TCPCloseWait = closeWait
	items = append(items,
		inspectItem{Label: "LISTEN", Value: fmt.Sprintf("%d", listen)},
		inspectItem{Label: "ESTAB", Value: fmt.Sprintf("%d", estab)},
		inspectItem{Label: "TIME_WAIT", Value: fmt.Sprintf("%d", timeWait)},
		inspectItem{Label: "CLOSE_WAIT", Value: fmt.Sprintf("%d", closeWait), Status: ternary(closeWait > 50, "warn", "ok")},
		inspectItem{Label: "SYN_RECV", Value: fmt.Sprintf("%d", synRecv)},
	)
	if closeWait > 50 {
		st = "warn"
		b.addFinding("warn", "net", fmt.Sprintf("CLOSE_WAIT 连接偏高: %d", closeWait))
	}
	b.rep.Sections = append(b.rep.Sections, inspectSection{ID: "net", Title: "网络", Status: st, Items: items})
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func (b *inspectBuilder) collectProcess() {
	st := "ok"
	items := []inspectItem{}
	procs, zombies, dstate := 0, 0, 0
	switch runtime.GOOS {
	case "windows":
		out := cmdOut(8, "cmd", "/c", "tasklist /FO CSV /NH")
		procs = maxInt(0, strings.Count(out, "\n"))
	case "darwin":
		out := cmdOut(5, "ps", "-axo", "stat=")
		for _, ln := range strings.Split(out, "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			procs++
			if strings.HasPrefix(ln, "Z") {
				zombies++
			}
		}
	default:
		out := cmdOut(5, "ps", "-eo", "stat=")
		for _, ln := range strings.Split(out, "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			procs++
			if strings.Contains(ln, "Z") {
				zombies++
			}
			if strings.HasPrefix(ln, "D") {
				dstate++
			}
		}
		// CPU / MEM TOP 5
		appendTop := func(sortKey, label string) {
			out := cmdOut(5, "ps", "aux", "--sort="+sortKey)
			n := 0
			for i, ln := range strings.Split(out, "\n") {
				if i == 0 || strings.TrimSpace(ln) == "" {
					continue
				}
				f := strings.Fields(ln)
				if len(f) < 11 {
					continue
				}
				n++
				cmd := strings.Join(f[10:], " ")
				if len(cmd) > 80 {
					cmd = cmd[:80] + "…"
				}
				items = append(items, inspectItem{
					Label: fmt.Sprintf("%s#%d", label, n),
					Value: fmt.Sprintf("pid=%s cpu=%s%% mem=%s%% %s", f[1], f[2], f[3], cmd),
				})
				if n >= 5 {
					break
				}
			}
		}
		appendTop("-%cpu", "CPU")
		appendTop("-%mem", "MEM")
	}
	b.rep.Metrics.ProcessCount = procs
	b.rep.Metrics.ZombieCount = zombies
	b.rep.Metrics.DStateCount = dstate
	items = append([]inspectItem{
		{Label: "进程数", Value: fmt.Sprintf("%d", procs)},
		{Label: "僵尸进程(Z)", Value: fmt.Sprintf("%d", zombies)},
		{Label: "D 状态进程", Value: fmt.Sprintf("%d", dstate)},
	}, items...)
	if zombies > 20 {
		st = "crit"
		b.addFinding("crit", "proc", fmt.Sprintf("僵尸进程过多: %d", zombies))
	} else if zombies > 0 {
		st = "warn"
		b.addFinding("warn", "proc", fmt.Sprintf("存在僵尸进程: %d", zombies))
	}
	if dstate > 0 {
		st = b.worst(st, "warn")
		b.addFinding("warn", "proc", fmt.Sprintf("存在 D 状态(不可中断)进程: %d", dstate))
	}
	b.rep.Sections = append(b.rep.Sections, inspectSection{ID: "proc", Title: "进程", Status: st, Items: items})
}

func (b *inspectBuilder) collectServices() {
	st := "info"
	items := []inspectItem{}
	names := []string{
		"sshd", "ssh", "crond", "cron", "rsyslog", "syslog-ng",
		"firewalld", "ufw", "nftables", "chronyd", "chrony", "ntpd", "ntp", "systemd-timesyncd",
		"docker", "podman", "containerd", "kubelet",
		"nginx", "httpd", "apache2", "mysqld", "mariadb", "postgresql",
		"redis", "redis-server", "mongod", "php-fpm", "haproxy", "keepalived",
		"zabbix-agent", "zabbix-agent2", "node_exporter", "NetworkManager",
	}
	switch runtime.GOOS {
	case "windows":
		for _, n := range []string{"Wuauserv", "EventLog", "Winmgmt", "Schedule", "Themes", "Dnscache"} {
			out := cmdOut(3, "sc", "query", n)
			status := "unknown"
			if strings.Contains(out, "RUNNING") {
				status = "running"
			} else if strings.Contains(out, "STOPPED") {
				status = "stopped"
			} else if strings.Contains(out, "1060") {
				status = "notfound"
			}
			if status == "notfound" {
				continue
			}
			items = append(items, inspectItem{Label: n, Value: status})
		}
	case "darwin":
		out := cmdOut(4, "launchctl", "list")
		for _, n := range []string{"com.openssh.sshd", "com.apple.cron"} {
			val := "notfound"
			if strings.Contains(out, n) {
				val = "loaded"
			}
			items = append(items, inspectItem{Label: n, Value: val})
		}
	default:
		failedN := 0
		for _, n := range names {
			val := strings.TrimSpace(cmdOut(2, "systemctl", "is-active", n))
			if val == "" || val == "not-found" || strings.Contains(val, "could not be found") {
				// 二次确认 unit 是否存在
				if uf := cmdOut(2, "systemctl", "list-unit-files", n+".service", "--no-pager", "--no-legend"); !strings.Contains(uf, n+".service") {
					continue
				}
				val = "inactive"
			}
			ist := "ok"
			if val != "active" && val != "activating" {
				ist = "warn"
			}
			items = append(items, inspectItem{Label: n, Value: val, Status: ist})
		}
		if out := cmdOut(4, "systemctl", "--failed", "--no-pager", "--no-legend"); out != "" {
			for _, ln := range strings.Split(out, "\n") {
				ln = strings.TrimSpace(ln)
				if ln == "" || strings.HasPrefix(ln, "UNIT") {
					continue
				}
				f := strings.Fields(ln)
				if len(f) == 0 {
					continue
				}
				failedN++
				items = append(items, inspectItem{Label: "FAILED", Value: f[0], Status: "crit"})
			}
		}
		if failedN > 0 {
			st = "crit"
			b.addFinding("crit", "svc", fmt.Sprintf("存在 %d 个失败 systemd 服务", failedN))
		}
	}
	if len(items) == 0 {
		st = "skip"
	}
	b.rep.Sections = append(b.rep.Sections, inspectSection{ID: "svc", Title: "关键服务", Status: st, Items: items})
}

func (b *inspectBuilder) collectSecurity() {
	st := "info"
	items := []inspectItem{
		{Label: "防火墙", Value: b.rep.Host.Firewall},
	}
	switch runtime.GOOS {
	case "linux":
		if out := cmdOut(3, "getenforce"); out != "" {
			sel := strings.TrimSpace(out)
			items = append(items, inspectItem{Label: "SELinux", Value: sel})
			if strings.EqualFold(sel, "Disabled") || strings.EqualFold(sel, "Permissive") {
				st = b.worst(st, "warn")
			}
		}
		// SSH 关键配置
		sshCfg := "/etc/ssh/sshd_config"
		if fileExists(sshCfg) {
			rootLogin := sshCfgValue(sshCfg, "PermitRootLogin", "默认(yes)")
			sshPort := sshCfgValue(sshCfg, "Port", "22")
			maxAuth := sshCfgValue(sshCfg, "MaxAuthTries", "默认(6)")
			pubkey := sshCfgValue(sshCfg, "PubkeyAuthentication", "默认(yes)")
			items = append(items,
				inspectItem{Label: "SSH Root登录", Value: rootLogin, Status: ternary(strings.EqualFold(rootLogin, "yes"), "warn", "ok")},
				inspectItem{Label: "SSH 端口", Value: sshPort},
				inspectItem{Label: "MaxAuthTries", Value: maxAuth},
				inspectItem{Label: "PubkeyAuthentication", Value: pubkey},
			)
			if strings.EqualFold(rootLogin, "yes") {
				st = b.worst(st, "warn")
				b.addFinding("warn", "sec", "SSH 允许 root 密码/登录（PermitRootLogin=yes），建议收紧")
			}
		}
		// UID=0 账户
		if raw, err := os.ReadFile("/etc/passwd"); err == nil {
			var roots []string
			loginN := 0
			for _, ln := range strings.Split(string(raw), "\n") {
				f := strings.Split(ln, ":")
				if len(f) < 7 {
					continue
				}
				if f[2] == "0" {
					roots = append(roots, f[0])
				}
				sh := f[6]
				if !strings.Contains(sh, "nologin") && !strings.Contains(sh, "false") && sh != "/bin/sync" {
					loginN++
				}
			}
			items = append(items,
				inspectItem{Label: "UID=0 账户", Value: strings.Join(roots, " ")},
				inspectItem{Label: "可登录账户数", Value: fmt.Sprintf("%d", loginN)},
			)
			if len(roots) > 1 {
				st = b.worst(st, "warn")
				b.addFinding("warn", "sec", "存在多个 UID=0 账户: "+strings.Join(roots, ","))
			}
		}
		// SUID 样例（限路径、限数量）
		if out := cmdOut(8, "find", "/usr/local", "/opt", "/home", "/tmp", "-xdev", "-type", "f", "-perm", "-4000", "-printf", "%p\n"); out != "" {
			n := 0
			var sample []string
			for _, ln := range strings.Split(out, "\n") {
				ln = strings.TrimSpace(ln)
				if ln == "" {
					continue
				}
				n++
				if len(sample) < 8 {
					sample = append(sample, ln)
				}
			}
			items = append(items, inspectItem{Label: "可疑 SUID(样例)", Value: fmt.Sprintf("%d 个；%s", n, strings.Join(sample, "; "))})
		}
		failed := 0
		if out := cmdOut(5, "journalctl", "-n", "200", "--no-pager", "-u", "sshd"); out != "" {
			low := strings.ToLower(out)
			failed = strings.Count(low, "failed") + strings.Count(out, "Invalid user")
		} else if fileExists("/var/log/secure") {
			out := cmdOut(4, "tail", "-n", "200", "/var/log/secure")
			low := strings.ToLower(out)
			failed = strings.Count(low, "failed") + strings.Count(out, "Invalid user")
		} else if fileExists("/var/log/auth.log") {
			out := cmdOut(4, "tail", "-n", "200", "/var/log/auth.log")
			low := strings.ToLower(out)
			failed = strings.Count(low, "failed") + strings.Count(out, "Invalid user")
		}
		items = append(items, inspectItem{Label: "近期 SSH 失败关键词(估)", Value: fmt.Sprintf("%d", failed)})
		if failed > 30 {
			st = b.worst(st, "warn")
			b.addFinding("warn", "sec", fmt.Sprintf("近期 SSH 认证失败较多: %d", failed))
		}
		if b.rep.Host.Firewall == "inactive" || b.rep.Host.Firewall == "unknown" {
			st = b.worst(st, "warn")
			b.addFinding("warn", "sec", "未检测到活跃防火墙服务")
		}
	case "windows":
		items = append(items, inspectItem{Label: "说明", Value: "请结合 Windows 安全事件日志 / 组策略复核"})
	case "darwin":
		items = append(items, inspectItem{Label: "说明", Value: "请结合 Console 认证日志复核"})
	}
	b.rep.Sections = append(b.rep.Sections, inspectSection{ID: "sec", Title: "安全", Status: st, Items: items})
}

func sshCfgValue(path, key, def string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	prefix := strings.ToLower(key)
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		if strings.EqualFold(f[0], key) || strings.ToLower(f[0]) == prefix {
			return f[1]
		}
	}
	return def
}

func (b *inspectBuilder) collectTime() {
	st := "ok"
	now := time.Now().Format(time.RFC3339)
	items := []inspectItem{
		{Label: "本机时间", Value: now},
		{Label: "时区", Value: b.rep.Host.Timezone},
	}
	synced := "unknown"
	switch runtime.GOOS {
	case "linux":
		if out := cmdOut(2, "timedatectl", "show", "-p", "NTPSynchronized", "--value"); out != "" {
			synced = strings.TrimSpace(out)
			if synced == "yes" {
				synced = "已同步 (systemd)"
			} else if synced == "no" {
				st = "warn"
				b.addFinding("warn", "time", "NTP 未同步")
				synced = "未同步"
			}
		}
		if chron := cmdOut(3, "chronyc", "tracking"); chron != "" {
			for _, ln := range strings.Split(chron, "\n") {
				if strings.Contains(ln, "Leap status") {
					items = append(items, inspectItem{Label: "chronyd", Value: strings.TrimSpace(ln)})
					if strings.Contains(ln, "Normal") {
						synced = "已同步 (chronyd)"
						st = "ok"
					}
				}
			}
		} else if ntpq := cmdOut(3, "ntpq", "-pn"); strings.Contains(ntpq, "*") {
			synced = "已同步 (ntpd)"
		}
	case "darwin":
		synced = "sntp/check"
	case "windows":
		if out := cmdOut(3, "w32tm", "/query", "/status"); strings.Contains(out, "Leap Indicator") {
			synced = "w32tm:ok"
		}
	}
	items = append(items, inspectItem{Label: "时间同步", Value: synced})
	b.rep.Sections = append(b.rep.Sections, inspectSection{ID: "time", Title: "时间同步", Status: st, Items: items})
}

func cmdOut(timeoutSec int, name string, args ...string) string {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	return decodeCmdOut(out)
}

// decodeCmdOut converts console bytes (e.g. GBK on Chinese Windows) to UTF-8
// and strips NULs / control noise so JSON reports stay valid.
func decodeCmdOut(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return sanitizeInspectField(string(ensureUTF8(b)))
}

// cmdOutRaw returns UTF-8 command output without whitespace collapsing (newlines kept).
func cmdOutRaw(timeoutSec int, name string, args ...string) []byte {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil
	}
	return ensureUTF8(out)
}

func sanitizeInspectField(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		switch {
		case r == 0 || r == '\uFFFD':
			continue
		case r == '\r':
			continue
		case r == '\n' || r == '\t':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		case r < 0x20:
			continue
		case r == ' ':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// looksLikeCommandUsage rejects MSYS/Git "hostname" help fragments and similar junk.
func looksLikeCommandUsage(s string) bool {
	low := strings.ToLower(s)
	for _, bad := range []string{
		"sethostname", "usage:", "hostname -s", "hostname -f",
		"invalid option", "unrecognized option", "try `hostname",
		"command not found", "is not recognized",
	} {
		if strings.Contains(low, bad) {
			return true
		}
	}
	return false
}

// inspectResolveFQDN picks a platform-safe FQDN. Never runs Linux-only hostname -f on Windows.
func inspectResolveFQDN(fallback string) string {
	if runtime.GOOS == "windows" {
		return inspectWindowsFQDN(fallback)
	}
	if out := cmdOut(2, "hostname", "-f"); out != "" && !looksLikeCommandUsage(out) {
		return out
	}
	return fallback
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
