package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Windows CIM/PowerShell helpers for playbook readonly modules and host_inspect.
// Prefer Get-CimInstance (Win11 / Server 2025 drop WMIC); fall back to Get-WmiObject
// then legacy wmic for older hosts.

func winRunPS(timeoutSec int, script string) string {
	ps := windowsPowerShellPath()
	raw := string(cmdOutRaw(timeoutSec, ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script))
	return strings.ReplaceAll(raw, "\r\n", "\n")
}

func winLooksEmptyOrFailed(out string) bool {
	s := strings.TrimSpace(out)
	if s == "" {
		return true
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "is not recognized") || strings.Contains(low, "不是内部或外部命令") {
		return true
	}
	if strings.Contains(low, "commandnotfoundexception") {
		return true
	}
	return false
}

// One PowerShell process collects disk+mem+cpu+model — avoids N× cold starts on Win11.
const winPSBasicsBatchScript = `[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ErrorActionPreference='SilentlyContinue'
Write-Output '---DISK---'
$disks=$null; try { $disks=@(Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3') } catch {}; if (-not $disks -or $disks.Count -eq 0) { try { $disks=@(Get-WmiObject Win32_LogicalDisk -Filter 'DriveType=3') } catch {} }
foreach ($d in @($disks)) { if (-not $d.Size -or [uint64]$d.Size -eq 0) { continue }; $free=[uint64]$d.FreeSpace; $size=[uint64]$d.Size; $used=$size-$free; $pct=[math]::Round(($used/$size)*100,1); Write-Output ('Caption='+$d.DeviceID); Write-Output ('FreeSpace='+$free); Write-Output ('Size='+$size); Write-Output ('UsedPercent='+$pct); Write-Output '' }
Write-Output '---MEM---'
$o=$null; try { $o=Get-CimInstance Win32_OperatingSystem } catch {}; if (-not $o) { try { $o=Get-WmiObject Win32_OperatingSystem } catch {} }
if ($o) { Write-Output ('FreePhysicalMemory='+[uint64]$o.FreePhysicalMemory); Write-Output ('TotalVisibleMemorySize='+[uint64]$o.TotalVisibleMemorySize) }
Write-Output '---CPU---'
$cpus=@(); try { $cpus=@(Get-CimInstance Win32_Processor) } catch {}; if (-not $cpus -or $cpus.Count -eq 0) { try { $cpus=@(Get-WmiObject Win32_Processor) } catch {} }
$load=0; $cores=0; $n=0; foreach ($c in @($cpus)) { if ($null -ne $c.LoadPercentage) { $load += [double]$c.LoadPercentage; $n++ }; if ($c.NumberOfLogicalProcessors) { $cores += [int]$c.NumberOfLogicalProcessors } elseif ($c.NumberOfCores) { $cores += [int]$c.NumberOfCores } }; if ($n -gt 0) { $load = [math]::Round($load / $n, 1) }; Write-Output ('LoadPercentage='+$load); Write-Output ('NumberOfCores='+$cores)
Write-Output '---MODEL---'
$cs=$null; try { $cs=Get-CimInstance Win32_ComputerSystem } catch {}; if (-not $cs) { try { $cs=Get-WmiObject Win32_ComputerSystem } catch {} }; if ($cs -and $cs.Model) { Write-Output ('Model='+[string]$cs.Model) }
Write-Output '---BOOT---'
$bo=$null; try { $bo=Get-CimInstance Win32_OperatingSystem } catch {}; if (-not $bo) { try { $bo=Get-WmiObject Win32_OperatingSystem } catch {} }; if ($bo -and $bo.LastBootUpTime) { $t=$bo.LastBootUpTime; if ($t -is [datetime]) { Write-Output ('LastBootUpTime='+$t.ToString('yyyyMMddHHmmss')) } else { Write-Output ('LastBootUpTime='+[string]$t) } }
`

const winPSDiskScript = `[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ErrorActionPreference='SilentlyContinue'; $disks=$null; try { $disks=@(Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3') } catch {}; if (-not $disks -or $disks.Count -eq 0) { try { $disks=@(Get-WmiObject Win32_LogicalDisk -Filter 'DriveType=3') } catch {} }; foreach ($d in @($disks)) { if (-not $d.Size -or [uint64]$d.Size -eq 0) { continue }; $free=[uint64]$d.FreeSpace; $size=[uint64]$d.Size; $used=$size-$free; $pct=[math]::Round(($used/$size)*100,1); Write-Output ('Caption='+$d.DeviceID); Write-Output ('FreeSpace='+$free); Write-Output ('Size='+$size); Write-Output ('UsedPercent='+$pct); Write-Output '' }`

const winPSMemScript = `[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ErrorActionPreference='SilentlyContinue'; $o=$null; try { $o=Get-CimInstance Win32_OperatingSystem } catch {}; if (-not $o) { try { $o=Get-WmiObject Win32_OperatingSystem } catch {} }; if ($o) { Write-Output ('FreePhysicalMemory='+[uint64]$o.FreePhysicalMemory); Write-Output ('TotalVisibleMemorySize='+[uint64]$o.TotalVisibleMemorySize) }`

const winPSCPUScript = `[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ErrorActionPreference='SilentlyContinue'; $cpus=@(); try { $cpus=@(Get-CimInstance Win32_Processor) } catch {}; if (-not $cpus -or $cpus.Count -eq 0) { try { $cpus=@(Get-WmiObject Win32_Processor) } catch {} }; $load=0; $cores=0; $n=0; foreach ($c in @($cpus)) { if ($null -ne $c.LoadPercentage) { $load += [double]$c.LoadPercentage; $n++ }; if ($c.NumberOfLogicalProcessors) { $cores += [int]$c.NumberOfLogicalProcessors } elseif ($c.NumberOfCores) { $cores += [int]$c.NumberOfCores } }; if ($n -gt 0) { $load = [math]::Round($load / $n, 1) }; Write-Output ('LoadPercentage='+$load); Write-Output ('NumberOfCores='+$cores)`

const winPSModelScript = `[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ErrorActionPreference='SilentlyContinue'; $o=$null; try { $o=Get-CimInstance Win32_ComputerSystem } catch {}; if (-not $o) { try { $o=Get-WmiObject Win32_ComputerSystem } catch {} }; if ($o -and $o.Model) { Write-Output ('Model='+[string]$o.Model) }`

const winPSPkgScript = `[Console]::OutputEncoding=[Text.Encoding]::UTF8; $ErrorActionPreference='SilentlyContinue'; $pkgs=@(); try { $pkgs=@(Get-Package -ErrorAction SilentlyContinue | Select-Object -First 200) } catch {}; if ($pkgs -and $pkgs.Count -gt 0) { foreach ($p in $pkgs) { Write-Output ((([string]$p.Name) + [char]9 + ([string]$p.Version))) } } else { Write-Output '未找到已安装软件包（Get-Package 为空）' }`

type winBasicsCache struct {
	mu        sync.Mutex
	expiry    time.Time
	disk      []winDiskRow
	memTot    uint64
	memFree   uint64
	cpuLoad   float64
	model     string
	bootStamp string // yyyyMMddHHmmss from CIM
	rawOK     bool
}

var winBasics = &winBasicsCache{}

func winEnsureBasics() {
	winBasics.mu.Lock()
	defer winBasics.mu.Unlock()
	if time.Now().Before(winBasics.expiry) && winBasics.rawOK {
		return
	}
	out := winRunPS(18, winPSBasicsBatchScript)
	sec := map[string]string{}
	cur := ""
	var b strings.Builder
	flush := func() {
		if cur != "" {
			sec[cur] = b.String()
			b.Reset()
		}
	}
	for _, ln := range strings.Split(out, "\n") {
		t := strings.TrimSpace(ln)
		switch t {
		case "---DISK---", "---MEM---", "---CPU---", "---MODEL---", "---BOOT---":
			flush()
			cur = strings.Trim(t, "-")
			continue
		}
		if cur != "" {
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	flush()
	winBasics.disk = parseWinDiskKVBlocks(sec["DISK"])
	var freeKB, totalKB uint64
	for _, ln := range strings.Split(sec["MEM"], "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "FreePhysicalMemory=") {
			freeKB, _ = strconv.ParseUint(strings.TrimPrefix(ln, "FreePhysicalMemory="), 10, 64)
		}
		if strings.HasPrefix(ln, "TotalVisibleMemorySize=") {
			totalKB, _ = strconv.ParseUint(strings.TrimPrefix(ln, "TotalVisibleMemorySize="), 10, 64)
		}
	}
	winBasics.memFree = freeKB * 1024
	winBasics.memTot = totalKB * 1024
	winBasics.cpuLoad = 0
	for _, ln := range strings.Split(sec["CPU"], "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "LoadPercentage=") {
			winBasics.cpuLoad, _ = strconv.ParseFloat(strings.TrimPrefix(ln, "LoadPercentage="), 64)
		}
	}
	winBasics.model = ""
	for _, ln := range strings.Split(sec["MODEL"], "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "Model=") {
			winBasics.model = strings.TrimPrefix(ln, "Model=")
			break
		}
	}
	winBasics.bootStamp = ""
	for _, ln := range strings.Split(sec["BOOT"], "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "LastBootUpTime=") {
			v := strings.TrimPrefix(ln, "LastBootUpTime=")
			if len(v) >= 14 {
				winBasics.bootStamp = v[:14]
			}
			break
		}
	}
	winBasics.rawOK = len(winBasics.disk) > 0 || winBasics.memTot > 0 || winBasics.cpuLoad > 0 || winBasics.model != "" || winBasics.bootStamp != ""
	winBasics.expiry = time.Now().Add(20 * time.Second)
}

// winCollectBootStamp returns yyyyMMddHHmmss from the batched CIM snapshot.
func winCollectBootStamp() string {
	winEnsureBasics()
	return winBasics.bootStamp
}

func winCIMDiskUsageText(ctx context.Context) ([]byte, int) {
	winEnsureBasics()
	if rows := winBasics.disk; len(rows) > 0 {
		var b strings.Builder
		b.WriteString("$ powershell Get-CimInstance Win32_LogicalDisk (batched)\n")
		b.WriteString(formatWinDiskUsageHuman(rows))
		return []byte(b.String()), 0
	}
	out := winRunPS(12, winPSDiskScript)
	if !winLooksEmptyOrFailed(out) && strings.Contains(out, "Caption=") {
		var b strings.Builder
		b.WriteString("$ powershell Get-CimInstance Win32_LogicalDisk\n")
		b.WriteString(strings.TrimSpace(out))
		b.WriteByte('\n')
		return []byte(b.String()), 0
	}
	fb, exit := runModuleCmds(ctx, [][]string{{"cmd", "/c", "wmic logicaldisk get Caption,FreeSpace,Size /format:list"}})
	if exit == 0 && !winLooksEmptyOrFailed(string(fb)) {
		return fb, 0
	}
	var b strings.Builder
	b.WriteString("$ powershell Get-CimInstance Win32_LogicalDisk\n")
	if strings.TrimSpace(out) != "" {
		b.WriteString(strings.TrimSpace(out))
		b.WriteByte('\n')
	}
	if len(fb) > 0 {
		b.Write(fb)
	}
	b.WriteString("[error] Windows 磁盘采集失败：CIM/PowerShell 无有效输出\n")
	return []byte(b.String()), 1
}

func winCIMMemInfoText(ctx context.Context) ([]byte, int) {
	winEnsureBasics()
	if winBasics.memTot > 0 {
		var b strings.Builder
		b.WriteString("$ powershell Get-CimInstance Win32_OperatingSystem (batched)\n")
		fmt.Fprintf(&b, "FreePhysicalMemory=%d\nTotalVisibleMemorySize=%d\n", winBasics.memFree/1024, winBasics.memTot/1024)
		return []byte(b.String()), 0
	}
	out := winRunPS(10, winPSMemScript)
	if !winLooksEmptyOrFailed(out) && strings.Contains(out, "TotalVisibleMemorySize=") {
		var b strings.Builder
		b.WriteString("$ powershell Get-CimInstance Win32_OperatingSystem\n")
		b.WriteString(strings.TrimSpace(out))
		b.WriteByte('\n')
		return []byte(b.String()), 0
	}
	return runModuleCmds(ctx, [][]string{{"cmd", "/c", "wmic OS get FreePhysicalMemory,TotalVisibleMemorySize /format:list"}})
}

func winCIMCPULoadText(ctx context.Context) ([]byte, int) {
	winEnsureBasics()
	if winBasics.cpuLoad > 0 || winBasics.rawOK {
		var b strings.Builder
		b.WriteString("$ powershell Get-CimInstance Win32_Processor (batched)\n")
		fmt.Fprintf(&b, "LoadPercentage=%.1f\n", winBasics.cpuLoad)
		return []byte(b.String()), 0
	}
	out := winRunPS(12, winPSCPUScript)
	if !winLooksEmptyOrFailed(out) && strings.Contains(out, "LoadPercentage=") {
		var b strings.Builder
		b.WriteString("$ powershell Get-CimInstance Win32_Processor\n")
		b.WriteString(strings.TrimSpace(out))
		b.WriteByte('\n')
		return []byte(b.String()), 0
	}
	return runModuleCmds(ctx, [][]string{{"cmd", "/c", "wmic cpu get LoadPercentage,NumberOfCores /format:list"}})
}

func winCIMPkgListText(ctx context.Context) ([]byte, int) {
	out := winRunPS(25, winPSPkgScript)
	if !winLooksEmptyOrFailed(out) && !strings.Contains(out, "Get-Package 为空") {
		var b strings.Builder
		b.WriteString("$ powershell Get-Package\n")
		b.WriteString(strings.TrimSpace(out))
		b.WriteByte('\n')
		return []byte(b.String()), 0
	}
	// Skip slow/missing `wmic product` on modern Windows.
	var b strings.Builder
	b.WriteString("$ powershell Get-Package\n")
	if strings.TrimSpace(out) != "" {
		b.WriteString(strings.TrimSpace(out))
		b.WriteByte('\n')
	} else {
		b.WriteString("未找到已安装软件包（Get-Package 为空；已跳过缓慢的 wmic product）\n")
	}
	return []byte(b.String()), 0
}

type winDiskRow struct {
	ID   string
	Free uint64
	Size uint64
}

func parseWinDiskKVBlocks(out string) []winDiskRow {
	var rows []winDiskRow
	var cur winDiskRow
	flush := func() {
		if cur.ID != "" && cur.Size > 0 {
			rows = append(rows, cur)
		}
		cur = winDiskRow{}
	}
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(strings.TrimSuffix(ln, "\r"))
		if ln == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch strings.ToLower(k) {
		case "caption", "deviceid":
			if cur.ID != "" && cur.Size > 0 {
				flush()
			}
			cur.ID = v
		case "freespace":
			cur.Free, _ = strconv.ParseUint(v, 10, 64)
		case "size":
			cur.Size, _ = strconv.ParseUint(v, 10, 64)
		}
	}
	flush()
	return rows
}

func winCollectDiskRows() []winDiskRow {
	winEnsureBasics()
	if len(winBasics.disk) > 0 {
		return append([]winDiskRow(nil), winBasics.disk...)
	}
	out := winRunPS(12, winPSDiskScript)
	rows := parseWinDiskKVBlocks(out)
	if len(rows) > 0 {
		return rows
	}
	csv := string(cmdOutRaw(8, "cmd", "/c", "wmic logicaldisk where DriveType=3 get DeviceID,FreeSpace,Size /format:csv"))
	for i, ln := range strings.Split(csv, "\n") {
		ln = strings.TrimSpace(strings.TrimSuffix(ln, "\r"))
		if i == 0 || ln == "" || strings.HasPrefix(ln, "Node,") {
			continue
		}
		f := strings.Split(ln, ",")
		if len(f) < 4 {
			continue
		}
		id, freeS, sizeS := f[len(f)-3], f[len(f)-2], f[len(f)-1]
		free, _ := strconv.ParseUint(freeS, 10, 64)
		size, _ := strconv.ParseUint(sizeS, 10, 64)
		if size == 0 {
			continue
		}
		rows = append(rows, winDiskRow{ID: id, Free: free, Size: size})
	}
	return rows
}

func winCollectMemBytes() (total, avail uint64) {
	winEnsureBasics()
	if winBasics.memTot > 0 {
		return winBasics.memTot, winBasics.memFree
	}
	out := winRunPS(10, winPSMemScript)
	var freeKB, totalKB uint64
	parse := func(s string) {
		for _, ln := range strings.Split(s, "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "FreePhysicalMemory=") {
				freeKB, _ = strconv.ParseUint(strings.TrimPrefix(ln, "FreePhysicalMemory="), 10, 64)
			}
			if strings.HasPrefix(ln, "TotalVisibleMemorySize=") {
				totalKB, _ = strconv.ParseUint(strings.TrimPrefix(ln, "TotalVisibleMemorySize="), 10, 64)
			}
		}
	}
	parse(out)
	if totalKB == 0 {
		parse(string(cmdOutRaw(5, "cmd", "/c", "wmic OS get FreePhysicalMemory,TotalVisibleMemorySize /value")))
	}
	return totalKB * 1024, freeKB * 1024
}

func winCollectCPULoadPct() float64 {
	winEnsureBasics()
	if winBasics.cpuLoad > 0 || winBasics.rawOK {
		return winBasics.cpuLoad
	}
	out := winRunPS(12, winPSCPUScript)
	parse := func(s string) float64 {
		for _, ln := range strings.Split(s, "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "LoadPercentage=") {
				v, _ := strconv.ParseFloat(strings.TrimPrefix(ln, "LoadPercentage="), 64)
				return v
			}
		}
		return 0
	}
	if v := parse(out); v > 0 {
		return v
	}
	return parse(string(cmdOutRaw(5, "cmd", "/c", "wmic cpu get loadpercentage /value")))
}

func winCollectComputerModel() string {
	winEnsureBasics()
	if winBasics.model != "" {
		return winBasics.model
	}
	out := winRunPS(6, winPSModelScript)
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "Model=") {
			return strings.TrimPrefix(ln, "Model=")
		}
	}
	wmic := string(cmdOutRaw(3, "cmd", "/c", "wmic computersystem get model /value"))
	for _, ln := range strings.Split(wmic, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "Model=") {
			return strings.TrimPrefix(ln, "Model=")
		}
	}
	return ""
}

func formatWinDiskUsageHuman(rows []winDiskRow) string {
	var b strings.Builder
	for _, r := range rows {
		used := r.Size - r.Free
		if used > r.Size {
			used = r.Size
		}
		pct := 0.0
		if r.Size > 0 {
			pct = float64(used) / float64(r.Size) * 100
		}
		fmt.Fprintf(&b, "Caption=%s FreeSpace=%d Size=%d UsedPercent=%.1f\n", r.ID, r.Free, r.Size, pct)
	}
	return b.String()
}
