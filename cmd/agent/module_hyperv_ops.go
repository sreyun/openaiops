//go:build windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// moduleHyperVPower starts/stops/restarts a Hyper-V guest on this host.
// Args: action=start|stop|restart|force_stop; vm_id (GUID) preferred; name fallback.
func moduleHyperVPower(ctx context.Context, args map[string]string) ([]byte, int) {
	ctx = moduleCtx(ctx)
	action := strings.ToLower(strings.TrimSpace(args["action"]))
	vmID := strings.TrimSpace(args["vm_id"])
	name := strings.TrimSpace(args["name"])
	if action == "" {
		return []byte("hyperv_power 缺少 action（start|stop|restart|force_stop）"), 1
	}
	if vmID == "" && name == "" {
		return []byte("hyperv_power 缺少 vm_id 或 name"), 1
	}
	sel := hypervVMSelectPS(vmID, name)
	var ps string
	switch action {
	case "start":
		ps = sel + "; Start-VM -VM $vm -ErrorAction Stop; 'ok start ' + $vm.Name"
	case "stop":
		ps = sel + "; Stop-VM -VM $vm -ErrorAction Stop; 'ok stop ' + $vm.Name"
	case "force_stop":
		ps = sel + "; Stop-VM -VM $vm -Force -TurnOff -ErrorAction Stop; 'ok force_stop ' + $vm.Name"
	case "restart":
		ps = sel + "; Restart-VM -VM $vm -Force -ErrorAction Stop; 'ok restart ' + $vm.Name"
	default:
		return []byte("未知 action: " + action), 1
	}
	return runHyperVOpsPS(ctx, ps, 120*time.Second)
}

// moduleHyperVSet updates processor count and/or memory settings.
// Args:
//   - processor_count
//   - memory_mb (startup)
//   - memory_min_mb / memory_max_mb (dynamic range)
//   - dynamic_memory = true|false|1|0|yes|no
//
// Changing CPU/memory while Running usually fails on Hyper-V; we preflight and
// return a clear Chinese error so the UI can prompt to shut down first.
func moduleHyperVSet(ctx context.Context, args map[string]string) ([]byte, int) {
	ctx = moduleCtx(ctx)
	vmID := strings.TrimSpace(args["vm_id"])
	name := strings.TrimSpace(args["name"])
	if vmID == "" && name == "" {
		return []byte("hyperv_set 缺少 vm_id 或 name"), 1
	}
	cpuStr := strings.TrimSpace(args["processor_count"])
	memStr := strings.TrimSpace(args["memory_mb"])
	minStr := strings.TrimSpace(args["memory_min_mb"])
	maxStr := strings.TrimSpace(args["memory_max_mb"])
	dynStr := strings.TrimSpace(args["dynamic_memory"])
	if cpuStr == "" && memStr == "" && minStr == "" && maxStr == "" && dynStr == "" {
		return []byte("hyperv_set 需要 processor_count / memory_mb / memory_min_mb / memory_max_mb / dynamic_memory 至少一项"), 1
	}

	sel := hypervVMSelectPS(vmID, name)
	var parts []string
	parts = append(parts, sel)

	// Preflight: CPU / memory changes require Off or Saved for most hosts.
	needOffline := cpuStr != "" || memStr != "" || minStr != "" || maxStr != "" || dynStr != ""
	if needOffline {
		parts = append(parts, `
$st=[string]$vm.State
if ($st -ne 'Off' -and $st -ne 'Saved') {
  throw ("NEED_VM_OFF: 修改 CPU/内存需要虚拟机处于「关机(Off)」或「已保存(Saved)」状态，当前=" + $st + "。请先关机后再改配。")
}`)
	}

	if cpuStr != "" {
		n, err := strconv.Atoi(cpuStr)
		if err != nil || n < 1 || n > 256 {
			return []byte("processor_count 无效"), 1
		}
		parts = append(parts, fmt.Sprintf(
			`Set-VMProcessor -VM $vm -Count %d -ErrorAction Stop; 'ok cpu=' + [string]%d`, n, n))
	}

	var startupMB, minMB, maxMB int64
	var haveStartup, haveMin, haveMax bool
	if memStr != "" {
		mb, err := strconv.ParseInt(memStr, 10, 64)
		if err != nil || mb < 32 || mb > 1024*1024 {
			return []byte("memory_mb 无效（32~1048576）"), 1
		}
		startupMB, haveStartup = mb, true
	}
	if minStr != "" {
		mb, err := strconv.ParseInt(minStr, 10, 64)
		if err != nil || mb < 32 || mb > 1024*1024 {
			return []byte("memory_min_mb 无效"), 1
		}
		minMB, haveMin = mb, true
	}
	if maxStr != "" {
		mb, err := strconv.ParseInt(maxStr, 10, 64)
		if err != nil || mb < 32 || mb > 1024*1024 {
			return []byte("memory_max_mb 无效"), 1
		}
		maxMB, haveMax = mb, true
	}
	if haveMin && haveMax && minMB > maxMB {
		return []byte("memory_min_mb 不能大于 memory_max_mb"), 1
	}
	if haveStartup && haveMin && startupMB < minMB {
		return []byte("memory_mb(启动) 不能小于 memory_min_mb"), 1
	}
	if haveStartup && haveMax && startupMB > maxMB {
		return []byte("memory_mb(启动) 不能大于 memory_max_mb"), 1
	}

	dynOn := false
	haveDyn := false
	switch strings.ToLower(dynStr) {
	case "":
	case "1", "true", "yes", "on":
		dynOn, haveDyn = true, true
	case "0", "false", "no", "off":
		dynOn, haveDyn = false, true
	default:
		return []byte("dynamic_memory 无效（true/false）"), 1
	}

	if haveDyn || haveStartup || haveMin || haveMax {
		var memArgs []string
		if haveDyn {
			if dynOn {
				memArgs = append(memArgs, "-DynamicMemoryEnabled $true")
			} else {
				memArgs = append(memArgs, "-DynamicMemoryEnabled $false")
			}
		}
		if haveStartup {
			memArgs = append(memArgs, fmt.Sprintf("-StartupBytes %d", startupMB*1024*1024))
		}
		if haveMin {
			memArgs = append(memArgs, fmt.Sprintf("-MinimumBytes %d", minMB*1024*1024))
		}
		if haveMax {
			memArgs = append(memArgs, fmt.Sprintf("-MaximumBytes %d", maxMB*1024*1024))
		}
		parts = append(parts, fmt.Sprintf(
			`Set-VMMemory -VM $vm %s -ErrorAction Stop; 'ok mem'`, strings.Join(memArgs, " ")))
	}

	parts = append(parts, `'ok config ' + $vm.Name`)
	return runHyperVOpsPS(ctx, strings.Join(parts, "; "), 120*time.Second)
}

func hypervVMSelectPS(vmID, name string) string {
	if vmID != "" {
		id := strings.ReplaceAll(vmID, "'", "''")
		return fmt.Sprintf(`$ErrorActionPreference='Stop'; $vm=Get-VM -Id '%s' -ErrorAction Stop`, id)
	}
	n := strings.ReplaceAll(name, "'", "''")
	return fmt.Sprintf(`$ErrorActionPreference='Stop'; $vm=Get-VM -Name '%s' -ErrorAction Stop`, n)
}

func runHyperVOpsPS(ctx context.Context, script string, timeout time.Duration) ([]byte, int) {
	ctx = moduleCtx(ctx)
	if moduleStopped(ctx) {
		return []byte("hyperv 操作" + moduleStopMsg), moduleStopExit
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.WaitDelay = 5 * time.Second
	out, err := moduleCombinedOutput(cmd)
	if moduleStopped(ctx) {
		return []byte("hyperv 操作已中止" + moduleStopMsg), moduleStopExit
	}
	if cctx.Err() == context.DeadlineExceeded {
		return []byte("hyperv 操作超时"), 1
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return []byte(msg), 1
	}
	return out, 0
}
