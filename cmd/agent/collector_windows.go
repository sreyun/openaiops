//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"aiops-monitor/shared"
)

// windowsCollector reads base metrics through the Win32 API via
// syscall.NewLazyDLL — no cgo, no third-party dependency, mirroring the
// zero-dependency spirit of the Linux procfs collector.
var (
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")
	modpsapi    = syscall.NewLazyDLL("psapi.dll")
	modiphlpapi = syscall.NewLazyDLL("iphlpapi.dll")

	procGetSystemTimes       = modkernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
	procGetDiskFreeSpaceExW  = modkernel32.NewProc("GetDiskFreeSpaceExW")
	procGetTickCount64       = modkernel32.NewProc("GetTickCount64")
	procGetLogicalDrives     = modkernel32.NewProc("GetLogicalDriveStringsW")
	procGetDriveType         = modkernel32.NewProc("GetDriveTypeW")
	procDeviceIoControl      = modkernel32.NewProc("DeviceIoControl")
	procEnumProcesses        = modpsapi.NewProc("EnumProcesses")
	procGetIfTable           = modiphlpapi.NewProc("GetIfTable")
	procGetIfTable2          = modiphlpapi.NewProc("GetIfTable2")
	procFreeMibTable         = modiphlpapi.NewProc("FreeMibTable")
	procGetTcpTable          = modiphlpapi.NewProc("GetTcpTable")
	procGetUdpTable          = modiphlpapi.NewProc("GetUdpTable")
	procGetTcp6Table         = modiphlpapi.NewProc("GetTcp6Table")
	procGetUdp6Table         = modiphlpapi.NewProc("GetUdp6Table")
	procRtlGetVersion        = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetVersion")

	modkernel32x                 = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snapshot = modkernel32x.NewProc("CreateToolhelp32Snapshot")
	procProcess32First           = modkernel32x.NewProc("Process32FirstW")
	procProcess32Next            = modkernel32x.NewProc("Process32NextW")
)

type filetime struct {
	low  uint32
	high uint32
}

func (f filetime) u64() uint64 { return uint64(f.high)<<32 | uint64(f.low) }

// memoryStatusEx mirrors Win32 MEMORYSTATUSEX (64 bytes).
type memoryStatusEx struct {
	length     uint32
	memoryLoad uint32
	totalPhys  uint64
	availPhys  uint64
	totalPage  uint64
	availPage  uint64
	totalVirt  uint64
	availVirt  uint64
	availExt   uint64
}

type windowsCollector struct {
	diskPath                       string
	prevIdle, prevKernel, prevUser uint64
	prevRx, prevTx                 uint64
	prevNetT                       time.Time
	prevDiskRead, prevDiskWrite    uint64
	prevDiskROps, prevDiskWOps     uint64
	prevDiskT                      time.Time
	load1, load5, load15           float64
	lastT                          time.Time
	primed                         bool
}

func newCollector(diskPath string) Collector {
	if diskPath == "" {
		diskPath = defaultDiskPath()
	}
	return &windowsCollector{diskPath: diskPath}
}

func (c *windowsCollector) Name() string    { return "windows-winapi" }
func (c *windowsCollector) Supported() bool { return true }

func (c *windowsCollector) Collect() (shared.Metrics, error) {
	now := time.Now()
	m := shared.Metrics{CPUCores: runtime.NumCPU()}

	// ---- CPU: GetSystemTimes (kernel time already includes idle) ----
	var idle, kernel, user filetime
	if r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	); r != 0 {
		i, k, u := idle.u64(), kernel.u64(), user.u64()
		if c.primed {
			idleD := i - c.prevIdle
			totalD := (k - c.prevKernel) + (u - c.prevUser)
			if totalD > 0 {
				m.CPUPercent = round1(float64(totalD-idleD) / float64(totalD) * 100)
			}
		}
		c.prevIdle, c.prevKernel, c.prevUser = i, k, u
	}

	// ---- Memory + page file (Windows "swap") ----
	var msx memoryStatusEx
	msx.length = uint32(unsafe.Sizeof(msx))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&msx))); r != 0 {
		m.MemTotal = msx.totalPhys
		m.MemUsed = msx.totalPhys - msx.availPhys
		if msx.totalPhys > 0 {
			m.MemPercent = round1(float64(m.MemUsed) / float64(msx.totalPhys) * 100)
		}
		// The commit limit beyond physical RAM approximates the page file.
		if msx.totalPage > msx.totalPhys {
			swapTotal := msx.totalPage - msx.totalPhys
			usedCommit := msx.totalPage - msx.availPage
			usedPhys := msx.totalPhys - msx.availPhys
			var swapUsed uint64
			if usedCommit > usedPhys {
				swapUsed = usedCommit - usedPhys
			}
			if swapUsed > swapTotal {
				swapUsed = swapTotal
			}
			m.SwapTotal = swapTotal
			m.SwapUsed = swapUsed
			if swapTotal > 0 {
				m.SwapPercent = round1(float64(swapUsed) / float64(swapTotal) * 100)
			}
		}
	}

	// ---- Disk: GetDiskFreeSpaceExW ----
	if p, err := syscall.UTF16PtrFromString(c.diskPath); err == nil {
		var freeAvail, total, totalFree uint64
		if r, _, _ := procGetDiskFreeSpaceExW.Call(
			uintptr(unsafe.Pointer(p)),
			uintptr(unsafe.Pointer(&freeAvail)),
			uintptr(unsafe.Pointer(&total)),
			uintptr(unsafe.Pointer(&totalFree)),
		); r != 0 && total > 0 {
			m.DiskTotal = total
			m.DiskUsed = total - totalFree
			m.DiskPercent = round1(float64(total-totalFree) / float64(total) * 100)
		}
	}

	// ---- All local fixed disks (C:, D:, …) ----
	m.Disks = enumWindowsDisks()

	// ---- Network: GetIfTable, byte counters diffed over time ----
	if rx, tx, ok := readIfTable(); ok {
		if c.primed && !c.prevNetT.IsZero() {
			if elapsed := now.Sub(c.prevNetT).Seconds(); elapsed > 0 {
				m.NetSentRate = round1(rate(tx, c.prevTx, elapsed))
				m.NetRecvRate = round1(rate(rx, c.prevRx, elapsed))
			}
		}
		c.prevRx, c.prevTx = rx, tx
		c.prevNetT = now
	}

	// ---- Disk IO: IOCTL_DISK_PERFORMANCE 累计计数器差分为速率（读写字节 + 读写 IOPS） ----
	if rb, wb, ro, wo := winDiskIO(); rb+wb+ro+wo > 0 {
		if c.primed && !c.prevDiskT.IsZero() {
			if elapsed := now.Sub(c.prevDiskT).Seconds(); elapsed > 0 {
				m.DiskReadRate = round1(rate(rb, c.prevDiskRead, elapsed))
				m.DiskWriteRate = round1(rate(wb, c.prevDiskWrite, elapsed))
				m.DiskReadIOPS = round1(rate(ro, c.prevDiskROps, elapsed))
				m.DiskWriteIOPS = round1(rate(wo, c.prevDiskWOps, elapsed))
			}
		}
		c.prevDiskRead, c.prevDiskWrite = rb, wb
		c.prevDiskROps, c.prevDiskWOps = ro, wo
		c.prevDiskT = now
	}

	// ---- TCP per-state + UDP connection counts ----
	m.Conns, m.NetConns = collectConnStats()

	// ---- Process count ----
	m.ProcCount = countProcs()
	m.ProcessNames = listProcessNames()

	// ---- GPU (NVIDIA via nvidia-smi; best-effort, cached) ----
	m.GPUs = cachedGPUs(nvidiaSmiGPUs)

	// ---- Uptime: GetTickCount64 (ms) ----
	if r, _, _ := procGetTickCount64.Call(); r != 0 {
		m.Uptime = uint64(r) / 1000
	}

	// ---- Load average: Windows has none, approximate via EWMA of busy cores ----
	c.updateLoad(m.CPUPercent, m.CPUCores, now)
	m.Load1, m.Load5, m.Load15 = round2(c.load1), round2(c.load5), round2(c.load15)

	c.lastT = now
	c.primed = true
	return m, nil
}

// DISK_PERFORMANCE（Win32）：一块磁盘的累计 IO 统计。仅用到读写字节数与读写次数。
type diskPerformance struct {
	BytesRead           int64
	BytesWritten        int64
	ReadTime            int64
	WriteTime           int64
	IdleTime            int64
	ReadCount           uint32
	WriteCount          uint32
	QueueDepth          uint32
	SplitCount          uint32
	QueryTime           int64
	StorageDeviceNumber uint32
	StorageManagerName  [8]uint16
}

const ioctlDiskPerformance = 0x70020 // CTL_CODE(IOCTL_DISK_BASE,0x0008,METHOD_BUFFERED,FILE_ANY_ACCESS)

// winDiskIO 汇总所有物理磁盘（\\.\PhysicalDriveN）的累计读写字节数与读写次数，经
// IOCTL_DISK_PERFORMANCE 获取。需管理员权限——Agent 作为 Windows 服务以 SYSTEM 运行时
// 满足；权限不足则打不开磁盘句柄，返回 0（上层保持 0，不报错）。
func winDiskIO() (readBytes, writeBytes, readOps, writeOps uint64) {
	miss := 0
	for i := 0; i < 32 && miss < 4; i++ {
		p, err := syscall.UTF16PtrFromString(`\\.\PhysicalDrive` + strconv.Itoa(i))
		if err != nil {
			miss++
			continue
		}
		h, err := syscall.CreateFile(p, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
		if err != nil {
			miss++
			continue
		}
		miss = 0
		var dp diskPerformance
		var ret uint32
		r, _, _ := procDeviceIoControl.Call(
			uintptr(h), uintptr(ioctlDiskPerformance),
			0, 0,
			uintptr(unsafe.Pointer(&dp)), unsafe.Sizeof(dp),
			uintptr(unsafe.Pointer(&ret)), 0,
		)
		syscall.CloseHandle(h)
		if r != 0 {
			readBytes += uint64(dp.BytesRead)
			writeBytes += uint64(dp.BytesWritten)
			readOps += uint64(dp.ReadCount)
			writeOps += uint64(dp.WriteCount)
		}
	}
	return
}

// updateLoad maintains a Unix-like 1/5/15-minute load approximation. Windows
// exposes no run-queue load average, so we treat "busy cores" (cpu% * cores)
// as the instantaneous load and smooth it with an exponentially weighted
// moving average whose decay matches the sampling interval.
func (c *windowsCollector) updateLoad(cpuPercent float64, cores int, now time.Time) {
	instant := cpuPercent / 100.0 * float64(cores)
	if !c.primed || c.lastT.IsZero() {
		c.load1, c.load5, c.load15 = instant, instant, instant
		return
	}
	dt := now.Sub(c.lastT).Seconds()
	if dt <= 0 {
		return
	}
	c.load1 = ewma(c.load1, instant, dt, 60)
	c.load5 = ewma(c.load5, instant, dt, 300)
	c.load15 = ewma(c.load15, instant, dt, 900)
}

func ewma(prev, x, dt, tau float64) float64 {
	alpha := 1 - math.Exp(-dt/tau)
	return prev + alpha*(x-prev)
}

// mibIfRow2 mirrors Windows MIB_IF_ROW2 (netioapi.h) for 64-bit octet counters.
// Layout matches golang.org/x/sys/windows.MibIfRow2.
type mibIfRow2 struct {
	InterfaceLuid               uint64
	InterfaceIndex              uint32
	InterfaceGuid               [16]byte
	Alias                       [257]uint16
	Description                 [257]uint16
	PhysicalAddressLength       uint32
	PhysicalAddress             [32]byte
	PermanentPhysicalAddress    [32]byte
	Mtu                         uint32
	Type                        uint32
	TunnelType                  uint32
	MediaType                   uint32
	PhysicalMediumType          uint32
	AccessType                  uint32
	DirectionType               uint32
	InterfaceAndOperStatusFlags uint8
	OperStatus                  uint32
	AdminStatus                 uint32
	MediaConnectState           uint32
	NetworkGuid                 [16]byte
	ConnectionType              uint32
	TransmitLinkSpeed           uint64
	ReceiveLinkSpeed            uint64
	InOctets                    uint64
	InUcastPkts                 uint64
	InNUcastPkts                uint64
	InDiscards                  uint64
	InErrors                    uint64
	InUnknownProtos             uint64
	InUcastOctets               uint64
	InMulticastOctets           uint64
	InBroadcastOctets           uint64
	OutOctets                   uint64
	OutUcastPkts                uint64
	OutNUcastPkts               uint64
	OutDiscards                 uint64
	OutErrors                   uint64
	OutUcastOctets              uint64
	OutMulticastOctets          uint64
	OutBroadcastOctets          uint64
	OutQLen                     uint64
}

type mibIfTable2 struct {
	NumEntries uint32
	Table      [1]mibIfRow2
}

// readIfTable prefers GetIfTable2 (64-bit counters, Vista+/Server 2008+) and
// falls back to classic GetIfTable (32-bit) when unavailable.
func readIfTable() (rx, tx uint64, ok bool) {
	if rx, tx, ok = readIfTable2(); ok {
		return rx, tx, true
	}
	return readIfTableLegacy()
}

func readIfTable2() (rx, tx uint64, ok bool) {
	if err := procGetIfTable2.Find(); err != nil {
		return 0, 0, false
	}
	var table *mibIfTable2
	r, _, _ := procGetIfTable2.Call(uintptr(unsafe.Pointer(&table)))
	if r != 0 || table == nil {
		return 0, 0, false
	}
	defer procFreeMibTable.Call(uintptr(unsafe.Pointer(table)))
	const ifTypeLoopback = 24
	n := int(table.NumEntries)
	if n <= 0 || n > 4096 {
		return 0, 0, false
	}
	// unsafe.Slice 直接基于 C 堆内存首元素构造切片，避免 uintptr 指针运算往返（go vet unsafeptr 告警）。
	rows := unsafe.Slice(&table.Table[0], n)
	for i := 0; i < n; i++ {
		if rows[i].Type == ifTypeLoopback {
			continue
		}
		rx += rows[i].InOctets
		tx += rows[i].OutOctets
	}
	return rx, tx, true
}

// readIfTableLegacy sums per-interface byte counters from the classic MIB_IFTABLE,
// skipping the software loopback. Counters are 32-bit; rate() tolerates wrap.
func readIfTableLegacy() (rx, tx uint64, ok bool) {
	var size uint32
	procGetIfTable.Call(0, uintptr(unsafe.Pointer(&size)), 0) // size probe
	if size == 0 || size > 1<<20 {                            // sanity cap: ifTable >1MB is impossible
		return 0, 0, false
	}
	buf := getBuf32K()
	defer putBuf32K(buf)
	if uint32(len(buf)) < size {
		buf = make([]byte, size)
	}
	if r, _, _ := procGetIfTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
	); r != 0 { // NO_ERROR == 0
		return 0, 0, false
	}
	const (
		rowSize        = 860
		offType        = 516
		offInOctets    = 552
		offOutOctets   = 576
		ifTypeLoopback = 24
	)
	n := binary.LittleEndian.Uint32(buf[0:])
	for i := 0; i < int(n); i++ {
		base := 4 + i*rowSize
		if base+offOutOctets+4 > len(buf) {
			break
		}
		if binary.LittleEndian.Uint32(buf[base+offType:]) == ifTypeLoopback {
			continue
		}
		rx += uint64(binary.LittleEndian.Uint32(buf[base+offInOctets:]))
		tx += uint64(binary.LittleEndian.Uint32(buf[base+offOutOctets:]))
	}
	return rx, tx, true
}

// winTCPStateNames maps Win32 MIB_TCP_STATE (dwState) codes to canonical state
// names (aligned with the Linux collector so charts share one legend).
var winTCPStateNames = map[uint32]string{
	1: "CLOSE", 2: "LISTEN", 3: "SYN_SENT", 4: "SYN_RECV", 5: "ESTABLISHED",
	6: "FIN_WAIT1", 7: "FIN_WAIT2", 8: "CLOSE_WAIT", 9: "CLOSING",
	10: "LAST_ACK", 11: "TIME_WAIT", 12: "DELETE_TCB",
}

// collectConnStats enumerates IPv4+IPv6 TCP/UDP via GetTcp(6)Table / GetUdp(6)Table.
func collectConnStats() ([]shared.ConnStat, int) {
	tcpStates := map[string]int{}
	countTCPTable := func(get *syscall.LazyProc, rowSize int) {
		if err := get.Find(); err != nil {
			return
		}
		var size uint32
		get.Call(0, uintptr(unsafe.Pointer(&size)), 0)
		if size == 0 || size > 1<<20 {
			return
		}
		buf := make([]byte, size)
		if r, _, _ := get.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0); r != 0 {
			return
		}
		n := binary.LittleEndian.Uint32(buf[0:])
		for i := 0; i < int(n); i++ {
			base := 4 + i*rowSize
			if base+4 > len(buf) {
				break
			}
			st := winTCPStateNames[binary.LittleEndian.Uint32(buf[base:])]
			if st == "" {
				st = "OTHER"
			}
			tcpStates[st]++
		}
	}
	countTCPTable(procGetTcpTable, 20)  // MIB_TCPROW
	countTCPTable(procGetTcp6Table, 52) // MIB_TCP6ROW

	countUDPTable := func(get *syscall.LazyProc) int {
		if err := get.Find(); err != nil {
			return 0
		}
		var size uint32
		get.Call(0, uintptr(unsafe.Pointer(&size)), 0)
		if size == 0 || size > 1<<20 {
			return 0
		}
		buf := make([]byte, size)
		if r, _, _ := get.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0); r != 0 || len(buf) < 4 {
			return 0
		}
		return int(binary.LittleEndian.Uint32(buf[0:]))
	}
	udpTotal := countUDPTable(procGetUdpTable) + countUDPTable(procGetUdp6Table)

	out := make([]shared.ConnStat, 0, len(tcpStates)+1)
	for st, cnt := range tcpStates {
		out = append(out, shared.ConnStat{Proto: "tcp", State: st, Count: cnt})
	}
	if udpTotal > 0 {
		out = append(out, shared.ConnStat{Proto: "udp", Count: udpTotal})
	}
	return out, tcpStates["ESTABLISHED"]
}

// countProcs returns the number of running processes via EnumProcesses,
// growing the id buffer until it is not saturated.
func countProcs() int {
	size := 1024
	for {
		ids := make([]uint32, size)
		var needed uint32
		if r, _, _ := procEnumProcesses.Call(
			uintptr(unsafe.Pointer(&ids[0])),
			uintptr(size*4),
			uintptr(unsafe.Pointer(&needed)),
		); r == 0 {
			return 0
		}
		got := int(needed / 4)
		if got < size || size >= 1<<20 {
			return got
		}
		size *= 2
	}
}

// enumWindowsDisks returns usage for every fixed (local) drive. The logical
// drive strings come back as a double-NUL-terminated "C:\<NUL>D:\<NUL><NUL>"
// list; each fixed drive (DRIVE_FIXED) is measured with GetDiskFreeSpaceExW.
func enumWindowsDisks() []shared.DiskInfo {
	buf := make([]uint16, 256)
	r, _, _ := procGetLogicalDrives.Call(uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	if r == 0 || int(r) > len(buf) {
		return nil
	}
	const driveFixed = 3 // DRIVE_FIXED
	var out []shared.DiskInfo
	start := 0
	for i := 0; i < int(r); i++ {
		if buf[i] != 0 {
			continue
		}
		if i > start {
			root := buf[start : i+1] // include the terminating NUL for the Win32 calls
			if dt, _, _ := procGetDriveType.Call(uintptr(unsafe.Pointer(&root[0]))); dt == driveFixed {
				if di, ok := winDiskUsage(&root[0], syscall.UTF16ToString(buf[start:i])); ok {
					out = append(out, di)
				}
			}
		}
		start = i + 1
	}
	return out
}

func winDiskUsage(rootPtr *uint16, label string) (shared.DiskInfo, bool) {
	var freeAvail, total, totalFree uint64
	if r, _, _ := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	); r == 0 || total == 0 {
		return shared.DiskInfo{}, false
	}
	used := total - totalFree
	return shared.DiskInfo{
		Path:    strings.TrimSuffix(label, "\\"),
		Total:   total,
		Used:    used,
		Percent: round1(float64(used) / float64(total) * 100),
	}, true
}

// osVersionInfoEx mirrors Win32 RTL_OSVERSIONINFOEXW (includes ProductType).
type osVersionInfoEx struct {
	dwOSVersionInfoSize uint32
	dwMajorVersion      uint32
	dwMinorVersion      uint32
	dwBuildNumber       uint32
	dwPlatformId        uint32
	szCSDVersion        [128]uint16
	wServicePackMajor   uint16
	wServicePackMinor   uint16
	wSuiteMask          uint16
	wProductType        byte
	wReserved           byte
}

func winVersion() (major, minor, build uint32, productType byte) {
	var vi osVersionInfoEx
	vi.dwOSVersionInfoSize = uint32(unsafe.Sizeof(vi))
	procRtlGetVersion.Call(uintptr(unsafe.Pointer(&vi)))
	return vi.dwMajorVersion, vi.dwMinorVersion, vi.dwBuildNumber, vi.wProductType
}

func osVersion() string {
	maj, min, build, pt := winVersion()
	if pt == 0 {
		pt = verNTWorkstation // best-effort if EX layout rejected
	}
	return formatWindowsOSName(maj, min, build, pt)
}

func kernelVersion() string {
	maj, min, build, _ := winVersion()
	return fmt.Sprintf("%d.%d.%d", maj, min, build)
}

// processEntry32 mirrors Win32 PROCESSENTRY32W (568 bytes on 64-bit).
type processEntry32 struct {
	size            uint32
	cntUsage        uint32
	th32ProcessID   uint32
	th32DefaultHeap uintptr
	th32ModuleID    uint32
	cntThreads      uint32
	th32ParentPocID uint32
	pcPriClassBase  int32
	peFlags         uint32
	szExeFile       [260]uint16 // MAX_PATH
}

// listProcessNames returns unique process base names via CreateToolhelp32Snapshot.
func listProcessNames() []string {
	const snapProcess = 0x00000002
	r, _, _ := procCreateToolhelp32Snapshot.Call(uintptr(snapProcess), 0)
	if r == ^uintptr(0) { // INVALID_HANDLE_VALUE
		return nil
	}
	snap := r
	defer syscall.CloseHandle(syscall.Handle(snap))

	seen := map[string]bool{}
	var names []string
	var pe processEntry32
	pe.size = uint32(unsafe.Sizeof(pe))

	r, _, _ = procProcess32First.Call(snap, uintptr(unsafe.Pointer(&pe)))
	if r == 0 {
		return nil
	}
	for {
		name := syscall.UTF16ToString(pe.szExeFile[:])
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
			if len(names) >= maxProcessNames {
				break
			}
		}
		pe.size = uint32(unsafe.Sizeof(pe))
		r, _, _ = procProcess32Next.Call(snap, uintptr(unsafe.Pointer(&pe)))
		if r == 0 {
			break
		}
	}
	return names
}
