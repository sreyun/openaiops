package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// Hyper-V guest inventory collection.
//
// Runs only on Windows Hyper-V hosts (hypervAvailable gates the goroutine; the
// non-Windows stub returns false). Guest inventory is slow-changing list data,
// so it rides its own low-frequency channel (POST /api/v1/agent/hyperv) instead
// of the high-frequency base-metric report — same split as Redfish hardware.
//
// parseHyperV / splitComma / hypervHealth live here (cross-platform, pure) so
// they stay unit-testable on any OS; only the PowerShell exec is Windows-only.

// hypervHostStats carries the physical host's own resource snapshot alongside the
// guest inventory (currently RAM; populated by the Windows collector, zero elsewhere).
type hypervHostStats struct {
	TotalMemMB float64
	AvailMemMB float64
}

// hypervRaw mirrors one object emitted by hypervScript's ConvertTo-Json. Field
// names match the PowerShell property names (json is case-insensitive). IP and
// Switches arrive comma-joined to dodge PS 5.1's "single-element array collapses
// to a scalar" JSON quirk.
type hypervRaw struct {
	Name                 string
	Id                   string
	State                string
	Status               string
	CPUUsage             float64
	CPUGuestPct          float64
	ProcessorCount       int
	MemAssignedMB        float64
	MemDemandMB          float64
	MemStartupMB         float64
	MemMinMB             float64
	MemMaxMB             float64
	DynamicMemoryEnabled bool
	UptimeSec            int64
	Generation           int
	Version              string
	IntegrationState     string
	IP                   string
	Switches             string
	VHDCount             int
	CheckpointCount      int
	ReplState            string
	ReplHealth           string
	// 嵌套集合：PS 单元素数组会折叠成对象，故用 RawMessage + unwrapArray 兜底。
	Nics        json.RawMessage
	Disks       json.RawMessage
	Checkpoints json.RawMessage
}

// unwrapArray unmarshals a nested collection that PowerShell's ConvertTo-Json may
// have emitted as a single object (1-element arrays collapse) or null/empty.
func unwrapArray(raw json.RawMessage, out any) {
	s := bytes.TrimSpace([]byte(raw))
	if len(s) == 0 || string(s) == "null" || string(s) == "[]" {
		return
	}
	if s[0] == '{' {
		s = append(append([]byte{'['}, s...), ']')
	}
	_ = json.Unmarshal(s, out)
}

// parseHyperV parses hypervScript's JSON output into guests. It tolerates the
// three shapes PS 5.1 can emit: "[]"/null (no VMs), a bare "{...}" object (one
// VM — ConvertTo-Json drops the array brackets), and a normal "[...]" array.
func parseHyperV(out string) ([]shared.HyperVGuest, error) {
	s := strings.TrimSpace(out)
	if s == "" || s == "null" || s == "[]" {
		return nil, nil
	}
	// Diagnostic marker: Get-VM threw (it does NOT throw for a 0-VM host — that
	// returns "[]" above — so this is a real failure, almost always access-denied
	// because the agent isn't elevated). Surface a clear, actionable error instead
	// of the silent empty inventory that made "0 VMs" and "can't read VMs" look alike.
	if strings.Contains(s, `"__hyperv_error__"`) {
		var diag struct {
			Elevated bool   `json:"elevated"`
			Message  string `json:"message"`
		}
		_ = json.Unmarshal([]byte(s), &diag)
		msg := strings.TrimSpace(diag.Message)
		if !diag.Elevated {
			return nil, fmt.Errorf("无法枚举 Hyper-V 虚拟机：Agent 未以管理员/SYSTEM 身份运行。请在【以管理员身份运行的 PowerShell】中重新执行安装命令（Hyper-V 采集需要管理员权限）。原始错误: %s", msg)
		}
		return nil, fmt.Errorf("Get-VM 调用失败（Agent 已提权，请确认 Hyper-V 角色/管理服务正常）: %s", msg)
	}
	if strings.HasPrefix(s, "{") { // single VM: wrap back into an array
		s = "[" + s + "]"
	}
	var raws []hypervRaw
	if err := json.Unmarshal([]byte(s), &raws); err != nil {
		return nil, err
	}
	guests := make([]shared.HyperVGuest, 0, len(raws))
	for _, r := range raws {
		if r.Name == "" {
			continue
		}
		g := shared.HyperVGuest{
			Name: r.Name, ID: r.Id, State: r.State, Status: r.Status,
			CPUUsage: r.CPUUsage, CPUGuestPct: r.CPUGuestPct, ProcessorCount: r.ProcessorCount,
			MemAssignedMB: r.MemAssignedMB, MemDemandMB: r.MemDemandMB,
			MemStartupMB: r.MemStartupMB, MemMinMB: r.MemMinMB, MemMaxMB: r.MemMaxMB,
			DynamicMemEnabled: r.DynamicMemoryEnabled,
			UptimeSec:         r.UptimeSec, Generation: r.Generation, Version: r.Version,
			IntegrationState: sanitizeHyperVText(r.IntegrationState),
			IPAddresses:      splitComma(r.IP), Switches: splitComma(r.Switches),
			VHDCount: r.VHDCount, CheckpointCount: r.CheckpointCount,
			ReplState: r.ReplState, ReplHealth: r.ReplHealth,
		}
		g.Health = hypervHealth(r.State, r.ReplHealth)
		// Nested collections use tag-less intermediate structs so Go matches the
		// PowerShell PascalCase keys by FIELD NAME (the shared types carry
		// snake_case tags for the frontend, which json only matches against the
		// tag — so PascalCase input wouldn't bind directly).
		var rawNics []struct {
			Name, MAC, Switch, Status, IP string
			Connected                     bool
		}
		unwrapArray(r.Nics, &rawNics)
		for _, rn := range rawNics {
			g.Nics = append(g.Nics, shared.HyperVNic{
				Name: sanitizeHyperVText(rn.Name), MAC: rn.MAC,
				Switch: sanitizeHyperVText(rn.Switch), Status: sanitizeHyperVText(rn.Status),
				Connected: rn.Connected, IPAddresses: splitComma(rn.IP),
			})
		}
		var rawDisks []struct {
			Path, ControllerType                 string
			ControllerNumber, ControllerLocation int
			FileSizeGB                           float64
		}
		unwrapArray(r.Disks, &rawDisks)
		for _, rd := range rawDisks {
			g.Disks = append(g.Disks, shared.HyperVDisk{
				Path: rd.Path, ControllerType: rd.ControllerType,
				ControllerNumber: rd.ControllerNumber, ControllerLocation: rd.ControllerLocation,
				FileSizeGB: rd.FileSizeGB,
			})
		}
		var rawCps []struct{ Name, Created, Parent string }
		unwrapArray(r.Checkpoints, &rawCps)
		for _, rc := range rawCps {
			g.Checkpoints = append(g.Checkpoints, shared.HyperVCheckpoint{
				Name: rc.Name, Created: rc.Created, ParentName: rc.Parent,
			})
		}
		guests = append(guests, g)
	}
	return guests, nil
}

// sanitizeHyperVText trims BOM/noise and drops fields that are pure U+FFFD
// mojibake from a prior GBK→UTF-8 mishandle (so the UI shows empty instead of
// diamonds). Fresh collects after the UTF-8 PowerShell path should never hit this.
func sanitizeHyperVText(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(s, "\ufeff"))
	if s == "" {
		return ""
	}
	bad := 0
	for _, r := range s {
		if r == '\uFFFD' {
			bad++
		}
	}
	if bad > 0 && bad*2 >= len([]rune(s)) {
		return ""
	}
	return s
}

// splitComma turns a comma-joined field into a trimmed, empty-free slice.
func splitComma(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// hypervHealth normalizes a guest's health from its (non-localized) State enum
// and ReplicationHealth. Hyper-V's "*Critical" states (RunningCritical,
// OffCritical, …) mean the VM is in real trouble (e.g. its storage dropped).
// Off itself is NOT unhealthy here — an intentionally-stopped VM shouldn't read
// as a fault; the alert layer decides whether a stopped VM warrants a warning.
func hypervHealth(state, replHealth string) string {
	if strings.HasSuffix(state, "Critical") {
		return "Critical"
	}
	switch replHealth {
	case "Critical":
		return "Critical"
	case "Warning":
		return "Warning"
	}
	switch state {
	case "Running":
		return "OK"
	case "Paused", "Saved":
		return "Warning"
	}
	return ""
}

// runHyperVCollector periodically collects the guest inventory and posts it to
// all servers. On collection failure it still posts a report carrying Error so
// the server can surface "collection failed" without discarding the last good
// inventory.
func (a *Agent) runHyperVCollector(ctx context.Context) {
	interval := a.hypervInterval
	if interval < 30*time.Second {
		interval = 60 * time.Second
	}
	slog.Info("Hyper-V 采集器启动", "interval", interval)

	// WS2012 cold Get-VM can exceed 60s; skip overlapping ticks so we never pile
	// up concurrent powershell.exe collectors (CPU/memory spikes on the host).
	var inFlight sync.Mutex

	collectAndPost := func() {
		if !inFlight.TryLock() {
			slog.Warn("Hyper-V 上一轮采集仍在进行，跳过本轮")
			return
		}
		defer inFlight.Unlock()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Hyper-V 采集 panic 已恢复", "panic", r)
			}
		}()
		guests, hstats, err := hypervCollect()
		rep := shared.HyperVReport{
			HostID:         a.identity.HostID,
			Fingerprint:    a.identity.Fingerprint,
			Timestamp:      time.Now().Unix(),
			HostName:       a.identity.Hostname,
			HostTotalMemMB: hstats.TotalMemMB,
			HostAvailMemMB: hstats.AvailMemMB,
			Guests:         guests,
		}
		if err != nil {
			rep.Error = err.Error()
			slog.Warn("Hyper-V 采集失败", "err", err)
		} else {
			slog.Info("Hyper-V 采集完成", "vms", len(guests))
		}
		a.postHyperVReport(rep)
	}

	collectAndPost() // report immediately on start
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collectAndPost()
		}
	}
}

// postHyperVReport sends the Hyper-V guest inventory to all server targets.
// Mirrors postHardwareReport: fingerprint in the X-Agent-Fingerprint header,
// no credential in the body. Marshal per target — each panel may know this
// machine by a different host_id (see serverTarget.hostIDOr).
func (a *Agent) postHyperVReport(rep shared.HyperVReport) {
	fp := a.identity.Fingerprint
	baseHostID := rep.HostID
	for _, t := range a.targets {
		go func(tgt *serverTarget) {
			r := rep
			r.HostID = tgt.hostIDOr(baseHostID)
			body, err := json.Marshal(r)
			if err != nil {
				slog.Warn("Hyper-V 上报序列化失败", "err", err)
				return
			}
			req, err := http.NewRequest("POST", tgt.server+"/api/v1/agent/hyperv", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if fp != "" {
				req.Header.Set("X-Agent-Fingerprint", fp)
			}
			resp, err := tgt.httpc.Do(req)
			if err != nil {
				slog.Warn("Hyper-V 上报失败", "server", tgt.server, "err", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				slog.Warn("Hyper-V 上报被拒", "server", tgt.server, "status", resp.StatusCode,
					"host_id", r.HostID, "vms", len(r.Guests), "body", string(respBody))
			} else {
				slog.Info("Hyper-V 上报成功", "server", tgt.server, "host_id", r.HostID, "vms", len(r.Guests))
			}
		}(t)
	}
}
