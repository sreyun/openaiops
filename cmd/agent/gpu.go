package main

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// GPU collection is best-effort: it shells out to always-present vendor tools
// (nvidia-smi) or reads OS interfaces (sysfs / ioreg). To avoid paying that
// cost every report cycle, results are memoized for gpuCacheTTL. 对齐默认
// report_interval(30s)：nvidia-smi 冷启动不便宜，一轮报告至多探一次即可。
const gpuCacheTTL = 30 * time.Second

var gpuCache struct {
	mu   sync.Mutex
	at   time.Time
	data []shared.GPUInfo
}

// cachedGPUs runs probe at most once per gpuCacheTTL and returns the last result
// otherwise. A nil result is cached too, so a machine with no GPU tool doesn't
// fork a process every cycle.
func cachedGPUs(probe func() []shared.GPUInfo) []shared.GPUInfo {
	gpuCache.mu.Lock()
	defer gpuCache.mu.Unlock()
	if !gpuCache.at.IsZero() && time.Since(gpuCache.at) < gpuCacheTTL {
		return gpuCache.data
	}
	gpuCache.data = probe()
	gpuCache.at = time.Now()
	return gpuCache.data
}

// runCmd executes a command and returns stdout, or "" on any error. Used by the
// Linux/Windows GPU probes (macOS reuses collector_darwin's run()). A hard 4s
// timeout is essential: nvidia-smi is known to hang for tens of seconds (or
// indefinitely) when the driver/GPU is wedged, and cachedGPUs runs the probe
// while holding gpuCache.mu — without the timeout that would stall the whole
// report loop and make an otherwise-healthy host flap offline.
func runCmd(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

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

// nvidiaSmiQuery is the field list requested from nvidia-smi. Column order here
// must match parseNvidiaSmi's indexing.
const nvidiaSmiQuery = "name,utilization.gpu,memory.used,memory.total,temperature.gpu,memory.free"

// nvidiaSmiGPUs probes NVIDIA GPUs. Shared by the Linux and Windows collectors.
func nvidiaSmiGPUs() []shared.GPUInfo {
	out := runCmd("nvidia-smi",
		"--query-gpu="+nvidiaSmiQuery,
		"--format=csv,noheader,nounits")
	return parseNvidiaSmi(out)
}

// parseNvidiaSmi parses the CSV (noheader,nounits) output of nvidia-smi. Each
// line is: name, util%, memUsedMiB, memTotalMiB, tempC, memFreeMiB. Missing/[N/A]
// fields parse to 0.
func parseNvidiaSmi(out string) []shared.GPUInfo {
	var gpus []shared.GPUInfo
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		f := strings.Split(ln, ",")
		if len(f) < 4 {
			continue
		}
		name := strings.TrimSpace(f[0])
		if name == "" {
			name = "GPU"
		}
		util := parseNum(f[1])
		usedMiB := parseNum(f[2])
		totalMiB := parseNum(f[3])
		g := shared.GPUInfo{
			Name:        name,
			UtilPercent: round1(util),
			MemUsed:     uint64(usedMiB) * 1024 * 1024,
			MemTotal:    uint64(totalMiB) * 1024 * 1024,
		}
		if totalMiB >= usedMiB {
			g.MemFree = uint64(totalMiB-usedMiB) * 1024 * 1024 // fallback: total-used
		}
		if totalMiB > 0 {
			g.MemPercent = round1(usedMiB / totalMiB * 100)
		}
		if len(f) >= 5 {
			g.Temp = round1(parseNum(f[4]))
		}
		if len(f) >= 6 {
			if free := parseNum(f[5]); free > 0 {
				g.MemFree = uint64(free) * 1024 * 1024 // prefer nvidia-smi's memory.free
			}
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// parseNum tolerantly parses a numeric field that may carry a stray unit token
// (e.g. "37 %", "1024 MiB", "[N/A]"). Non-numeric input yields 0.
func parseNum(s string) float64 {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
