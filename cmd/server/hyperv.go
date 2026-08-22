package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"aiops-monitor/shared"
)

// ============================================================================
// Hyper-V 虚拟机：agent 上报接收 + 前端查询 + 指标/变更/关联
//
// 数据模型与硬件资产同构：每台物理宿主机一份 guest 清单，慢变、要追踪变更。
// 走独立指纹鉴权通道，落 PG(JSONB) + 每 VM 数值指标进 VictoriaMetrics。
// ============================================================================

// handleAgentHyperV receives a physical host's Hyper-V guest inventory.
func (s *Server) handleAgentHyperV(w http.ResponseWriter, r *http.Request) {
	var rep shared.HyperVReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		slog.Warn("Hyper-V 上报 JSON 解析失败", "err", err, "remote", r.RemoteAddr)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if rep.HostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id required"})
		return
	}

	// Fingerprint verification (same pattern as hardware/terminal/forward).
	fp := r.Header.Get("X-Agent-Fingerprint")
	if fp == "" {
		fp = r.URL.Query().Get("fp")
	}
	if !s.forwardFingerprintOKByHost(rep.HostID, fp) {
		slog.Warn("Hyper-V 上报指纹校验失败", "host_id", rep.HostID, "fp", fp, "remote", r.RemoteAddr)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "fingerprint mismatch"})
		return
	}

	hostname, ip := rep.HostID, ""
	if h := s.hostByID(rep.HostID); h != nil {
		hostname, ip = h.Hostname, h.IP
	}
	if hostname == rep.HostID && rep.HostName != "" {
		hostname = rep.HostName
	}

	// 缓存最新清单（供告警评估每轮复用）。采集失败时 put 会保留上一份好数据、只记 lastError。
	guests := normalizeHyperVGuests(rep.Guests)
	s.hv.put(rep.HostID, hostname, ip, guests, rep.Error, rep.HostTotalMemMB, rep.HostAvailMemMB)

	if rep.Error != "" {
		slog.Warn("Hyper-V 采集失败，保留上一份清单不覆盖", "host_id", rep.HostID, "err", rep.Error)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	ts := rep.Timestamp
	if ts == 0 {
		ts = time.Now().Unix()
	}

	if s.pg != nil {
		// 变更必须在 upsert **之前**比对：upsert 会覆盖上一份，之后就没有旧值可比了。
		s.recordHyperVChanges(rep.HostID, guests)
		s.pg.upsertHyperVInventory(rep.HostID, hostname, guests)
		// Agent 重装会换 host_id：同机旧清单会残留并在树里并排出现。上报成功后顺手清掉
		// 同指纹/同名的孤儿行，避免等用户点清理。
		s.purgeStaleHyperVDuplicates(rep.HostID)
	}

	// 每 VM 数值指标写入 VictoriaMetrics（趋势曲线 + 历史）。
	s.pushHyperVMetrics(rep.HostID, guests, ts)

	slog.Info("Hyper-V 上报已存储", "host_id", rep.HostID, "vms", len(guests))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleHyperVList returns Hyper-V inventories, grouped per physical host. With
// ?host= it returns one host; otherwise every host that reported guests. Each
// guest is enriched with linked_host_id/linked_host_name when it maps to a
// managed host (by name or IP) — that's the "my machines run in Hyper-V" bridge.
func (s *Server) handleHyperVList(w http.ResponseWriter, r *http.Request) {
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"inventories": []any{}})
		return
	}
	host := r.URL.Query().Get("host")
	var rows []map[string]any
	if host != "" {
		if !s.requireHostAccess(w, r, host) {
			return
		}
		if inv, ok := s.pg.getHyperVInventory(host); ok {
			rows = []map[string]any{inv}
		}
	} else {
		var err error
		rows, err = s.pg.getAllHyperVInventories()
		if err != nil {
			slog.Warn("查询 Hyper-V 清单失败", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
			return
		}
		rows = s.filterInventoryRows(r, rows)
	}
	dedupHyperVRowGuests(rows) // heal legacy twins on read (GUID + name-only orphans)
	// Agent 重装换 host_id 后，同机可能留下多份宿主机清单（同名、无内存标注的那条就是孤儿）。
	// 读路径只展示赢家，并把可清理数量返回给前端做横幅。
	var staleTotal int
	if host == "" {
		kept, stale := s.splitHyperVInventories(rows)
		rows = kept
		staleTotal = len(stale)
	}
	s.enrichHyperVLinks(rows)
	// Annotate each host with its own RAM (from the in-memory store, latest report)
	// so the frontend can show "宿主机名 · 可用/总内存" without a PG schema change.
	for _, row := range rows {
		hid, _ := row["host_id"].(string)
		if hid == "" {
			continue
		}
		if total, avail := s.hv.hostMemOf(hid); total > 0 {
			row["host_total_mem_mb"] = total
			row["host_avail_mem_mb"] = avail
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"inventories": rows,
		"stale_total": staleTotal,
	})
}

// handleHyperVEvents returns a host's VM change/state events, newest first.
func (s *Server) handleHyperVEvents(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.pg.getHyperVEvents(hostID, limit)
	if err != nil {
		slog.Warn("查询 Hyper-V 事件失败", "host", hostID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleDeleteHyperV removes a host's Hyper-V inventory (in-memory + PG).
func (s *Server) handleDeleteHyperV(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("hostID")
	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hostID required"})
		return
	}
	label := s.hostLabelForID(hostID)
	s.removeHyperVForHost(hostID)
	slog.Info("删除 Hyper-V 清单", "host", hostID, "actor", s.clientIP(r))
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Host: label, Message: Tz("log.delete_hyperv", label)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCleanupHyperVDuplicates permanently deletes orphan Hyper-V inventories
// (same physical host, old host_id after Agent reinstall). Only non-live losers
// are removed; the currently reporting identity is kept.
func (s *Server) handleCleanupHyperVDuplicates(w http.ResponseWriter, r *http.Request) {
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"deleted": []string{}, "count": 0})
		return
	}
	rows, err := s.pg.getAllHyperVInventories()
	if err != nil {
		slog.Warn("查询 Hyper-V 清单失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	_, stale := s.splitHyperVInventories(rows)
	deleted := make([]string, 0, len(stale))
	for _, id := range stale {
		s.removeHyperVForHost(id)
		deleted = append(deleted, id)
	}
	if len(deleted) > 0 {
		s.store.AddLog(LogEntry{
			Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
			Message: Tz("log.cleanup_hyperv_duplicates", len(deleted)),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "count": len(deleted)})
}

// removeHyperVForHost drops one host's Hyper-V inventory from memory and PG.
func (s *Server) removeHyperVForHost(hostID string) {
	if hostID == "" {
		return
	}
	s.hv.remove(hostID)
	if s.pg != nil {
		s.pg.deleteHyperVInventory(hostID)
	}
}

// purgeStaleHyperVDuplicates deletes orphan inventories that share a group with
// keepHostID (same fingerprint / hostname). Called after a successful agent report.
func (s *Server) purgeStaleHyperVDuplicates(keepHostID string) {
	if s.pg == nil || keepHostID == "" {
		return
	}
	rows, err := s.pg.getAllHyperVInventories()
	if err != nil || len(rows) < 2 {
		return
	}
	_, stale := s.splitHyperVInventories(rows)
	if len(stale) == 0 {
		return
	}
	keepKey := ""
	for _, row := range rows {
		if hid, _ := row["host_id"].(string); hid == keepHostID {
			name, _ := row["host_name"].(string)
			keepKey = s.hypervDedupKey(hid, name)
			break
		}
	}
	if keepKey == "" || strings.HasPrefix(keepKey, "id:") {
		return
	}
	for _, id := range stale {
		name := ""
		for _, row := range rows {
			if hid, _ := row["host_id"].(string); hid == id {
				name, _ = row["host_name"].(string)
				break
			}
		}
		if s.hypervDedupKey(id, name) != keepKey {
			continue
		}
		s.removeHyperVForHost(id)
		slog.Info("已清理同机旧 Hyper-V 清单", "keep", keepHostID, "removed", id)
	}
}

// enrichHyperVLinks annotates each guest in the rows with linked_host_id /
// linked_host_name when it corresponds to a managed host. Matches by hostname
// (case-insensitive, FQDN short name), then by any of the guest's reported IPs.
func (s *Server) enrichHyperVLinks(rows []map[string]any) {
	if len(rows) == 0 {
		return
	}
	byName := map[string]*Host{}
	byIP := map[string]*Host{}
	for _, h := range s.store.ListHosts() {
		if h.Hostname != "" {
			hn := strings.ToLower(strings.TrimSpace(h.Hostname))
			byName[hn] = h
			if i := strings.IndexByte(hn, '.'); i > 0 {
				byName[hn[:i]] = h
			}
		}
		if h.IP != "" {
			byIP[strings.TrimSpace(h.IP)] = h
		}
		// Also index secondary IPs if present on host metrics/labels later — primary IP is enough for most guests.
	}
	for _, row := range rows {
		guests, _ := row["guests"].([]any)
		for _, gi := range guests {
			g, ok := gi.(map[string]any)
			if !ok {
				continue
			}
			var match *Host
			if name, _ := g["name"].(string); name != "" {
				key := strings.ToLower(strings.TrimSpace(name))
				match = byName[key]
				if match == nil {
					if i := strings.IndexByte(key, '.'); i > 0 {
						match = byName[key[:i]]
					}
				}
			}
			if match == nil {
				if ips, ok := g["ip_addresses"].([]any); ok {
					for _, ipi := range ips {
						ip, _ := ipi.(string)
						ip = strings.TrimSpace(ip)
						if ip == "" {
							continue
						}
						if h := byIP[ip]; h != nil {
							match = h
							break
						}
					}
				}
			}
			if match != nil {
				g["linked_host_id"] = match.ID
				g["linked_host_name"] = match.Hostname
				g["link_match"] = "agent"
			} else {
				g["link_match"] = "none"
			}
		}
	}
}

// pushHyperVMetrics writes per-VM numeric series to VictoriaMetrics. Labels are
// host + vm_id (stable GUID, name fallback) + vm (display name). Using vm_id as
// the series identity keeps charts continuous across renames; vm is for humans.
func (s *Server) pushHyperVMetrics(hostID string, guests []shared.HyperVGuest, ts int64) {
	if !s.vm.enabled() || len(guests) == 0 {
		return
	}
	ms := ts * 1000
	host := lblEsc(hostID)
	var b strings.Builder
	for _, g := range guests {
		if g.Name == "" && g.ID == "" {
			continue
		}
		vmName := g.Name
		if vmName == "" {
			vmName = g.ID
		}
		vmID := g.ID
		if vmID == "" {
			vmID = g.Name
		}
		vm := lblEsc(vmName)
		vid := lblEsc(vmID)
		stateVal := 0.0
		if g.State == "Running" {
			stateVal = 1
		}
		fmt.Fprintf(&b, "aiops_hyperv_state{host=%q,vm_id=%q,vm=%q} %g %d\n", host, vid, vm, stateVal, ms)
		fmt.Fprintf(&b, "aiops_hyperv_cpu_percent{host=%q,vm_id=%q,vm=%q} %g %d\n", host, vid, vm, g.CPUUsage, ms)
		if g.CPUGuestPct > 0 {
			fmt.Fprintf(&b, "aiops_hyperv_cpu_guest_percent{host=%q,vm_id=%q,vm=%q} %g %d\n", host, vid, vm, g.CPUGuestPct, ms)
		}
		if g.MemAssignedMB > 0 {
			fmt.Fprintf(&b, "aiops_hyperv_mem_assigned_mb{host=%q,vm_id=%q,vm=%q} %g %d\n", host, vid, vm, g.MemAssignedMB, ms)
		}
		if g.MemDemandMB > 0 {
			fmt.Fprintf(&b, "aiops_hyperv_mem_demand_mb{host=%q,vm_id=%q,vm=%q} %g %d\n", host, vid, vm, g.MemDemandMB, ms)
		}
		if g.UptimeSec > 0 {
			fmt.Fprintf(&b, "aiops_hyperv_uptime_sec{host=%q,vm_id=%q,vm=%q} %d %d\n", host, vid, vm, g.UptimeSec, ms)
		}
	}
	if b.Len() > 0 {
		s.vm.pushRawLine(strings.TrimRight(b.String(), "\n"))
	}
}

// recordHyperVChanges diffs the incoming guests against the stored inventory and
// persists VM add / remove / state-change events. Identity is the VM GUID (falls
// back to name) so a renamed VM isn't logged as remove+add.
//
// Only diffs when a previous inventory exists: the first report is a baseline,
// not a set of "added" events (mirrors recordHardwareChanges).
func (s *Server) recordHyperVChanges(hostID string, cur []shared.HyperVGuest) {
	if s.pg == nil {
		return
	}
	prev, ok := s.pg.getHyperVInventoryDecoded(hostID)
	if !ok {
		return // 首次入库 = 建立基线，不产生事件
	}
	for _, c := range diffHyperVGuests(prev, cur) {
		s.pg.insertHyperVEvent(hostID, c.vmName, c.vmID, c.kind, c.severity, c.message)
	}
}

// hypervChange is one detected inventory difference.
type hypervChange struct {
	vmName, vmID, kind, severity, message string
}

// diffHyperVGuests compares two guest lists (by GUID, name fallback) and returns
// add / remove / state-change events. Pure function so it's unit-testable without
// a database. Callers must have a previous baseline — a first-ever inventory is
// not diffed (that's handled by recordHyperVChanges returning early).
func diffHyperVGuests(prev, cur []shared.HyperVGuest) []hypervChange {
	prevByID := map[string]shared.HyperVGuest{}
	for _, g := range prev {
		prevByID[hypervKey(g)] = g
	}
	curByID := map[string]shared.HyperVGuest{}
	for _, g := range cur {
		curByID[hypervKey(g)] = g
	}

	var out []hypervChange
	for _, g := range cur {
		k := hypervKey(g)
		old, existed := curLookup(prevByID, k)
		if !existed {
			out = append(out, hypervChange{g.Name, g.ID, "vm_added", "info",
				fmt.Sprintf("发现新虚拟机 %s（%s）", g.Name, g.State)})
		} else if old.State != g.State {
			sev := "info"
			// 由运行转为非运行 = 值得关注（宕机/被停）。
			if old.State == "Running" && g.State != "Running" {
				sev = "warning"
			}
			out = append(out, hypervChange{g.Name, g.ID, "state_change", sev,
				fmt.Sprintf("虚拟机 %s 状态变化：%s → %s", g.Name, old.State, g.State)})
		}
	}
	for _, g := range prev {
		if _, still := curByID[hypervKey(g)]; !still {
			out = append(out, hypervChange{g.Name, g.ID, "vm_removed", "warning",
				fmt.Sprintf("虚拟机 %s 已移除或迁移", g.Name)})
		}
	}
	return out
}

func curLookup(m map[string]shared.HyperVGuest, k string) (shared.HyperVGuest, bool) {
	g, ok := m[k]
	return g, ok
}

// hypervKey identifies a guest across reports: prefer the stable GUID, fall back
// to the name so guests without a reported ID still diff sanely.
func hypervKey(g shared.HyperVGuest) string {
	if g.ID != "" {
		return g.ID
	}
	return "name:" + g.Name
}

// hypervAlertScope is the alert Scope base for one guest. Prefer GUID so a rename
// keeps the same alertKey (HostID/Type/Scope); fall back to display name when the
// agent didn't report an ID (legacy / empty Id).
func hypervAlertScope(g shared.HyperVGuest) string {
	if g.ID != "" {
		return g.ID
	}
	return g.Name
}

// normalizeHyperVGuests drops nameless entries and dedupes by hypervKey (last wins).
// Guards against corrupted/legacy inventories that somehow accumulated rename twins.
//
// It also removes "name-only orphans": a guest with an empty ID whose Name is already
// covered by a GUID-bearing guest is a stale ghost (legacy pre-GUID snapshot, a rename
// that left a "name:<old>" twin, or a VM sharing a physical host's display name). Since
// the GUID entry is the stable identity, the name-only twin is dropped so the same VM —
// and same-name-as-host VMs — appear exactly once in 资源→虚拟机.
func normalizeHyperVGuests(guests []shared.HyperVGuest) []shared.HyperVGuest {
	if len(guests) == 0 {
		return guests
	}
	order := make([]string, 0, len(guests))
	byKey := map[string]shared.HyperVGuest{}
	for _, g := range guests {
		if g.Name == "" && g.ID == "" {
			continue
		}
		k := hypervKey(g)
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = g
	}
	guidNames := map[string]bool{}
	for _, k := range order {
		if g := byKey[k]; g.ID != "" && g.Name != "" {
			guidNames[g.Name] = true
		}
	}
	out := make([]shared.HyperVGuest, 0, len(order))
	for _, k := range order {
		g := byKey[k]
		if g.ID == "" && guidNames[g.Name] {
			continue // name-only orphan superseded by its GUID entry
		}
		out = append(out, g)
	}
	return out
}

// dedupHyperVRowGuests heals legacy PG inventories on read: it removes duplicate and
// name-only-orphan guests from the decoded map rows returned to the UI, so twins that
// were persisted before the write-path fix stop showing without needing a fresh report.
func dedupHyperVRowGuests(rows []map[string]any) {
	for _, row := range rows {
		guests, ok := row["guests"].([]any)
		if !ok || len(guests) == 0 {
			continue
		}
		guidNames := map[string]bool{}
		for _, gi := range guests {
			g, _ := gi.(map[string]any)
			if g == nil {
				continue
			}
			id, _ := g["id"].(string)
			name, _ := g["name"].(string)
			if id != "" && name != "" {
				guidNames[name] = true
			}
		}
		seen := map[string]bool{}
		out := make([]any, 0, len(guests))
		for _, gi := range guests {
			g, _ := gi.(map[string]any)
			if g == nil {
				continue
			}
			id, _ := g["id"].(string)
			name, _ := g["name"].(string)
			if id == "" && name == "" {
				continue
			}
			if id == "" && guidNames[name] {
				continue
			}
			key := id
			if key == "" {
				key = "name:" + name
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, gi)
		}
		row["guests"] = out
		row["guest_count"] = len(out)
	}
}

// hypervDedupKey groups inventory rows that belong to the same physical Hyper-V
// host. Prefer machine fingerprint (stable across Agent reinstall); fall back to
// hostname when the managed host is gone but the inventory row remains.
func (s *Server) hypervDedupKey(hostID, hostName string) string {
	if h := s.hostByID(hostID); h != nil && h.Fingerprint != "" {
		return "fp:" + h.Fingerprint
	}
	name := strings.ToLower(strings.TrimSpace(hostName))
	if name == "" || name == strings.ToLower(hostID) {
		return "id:" + hostID
	}
	// Orphan inventory (host record deleted / never linked): if exactly one
	// managed host uses this hostname and has a fingerprint, attach to that
	// group so the twin collapses with the live reporter.
	var fps []string
	seen := map[string]bool{}
	for _, h := range s.store.ListHosts() {
		if strings.ToLower(h.Hostname) != name || h.Fingerprint == "" {
			continue
		}
		if !seen[h.Fingerprint] {
			seen[h.Fingerprint] = true
			fps = append(fps, h.Fingerprint)
		}
	}
	if len(fps) == 1 {
		return "fp:" + fps[0]
	}
	return "name:" + name
}

func hypervRowUpdatedAt(row map[string]any) time.Time {
	switch v := row["updated_at"].(type) {
	case time.Time:
		return v
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func hypervRowGuestCount(row map[string]any) int {
	switch v := row["guest_count"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	if guests, ok := row["guests"].([]any); ok {
		return len(guests)
	}
	return 0
}

// splitHyperVInventories collapses same-machine host twins (reinstall → new
// host_id). Returns the rows to show and the orphan host_ids safe to delete.
func (s *Server) splitHyperVInventories(rows []map[string]any) (kept []map[string]any, staleIDs []string) {
	if len(rows) == 0 {
		return rows, nil
	}
	offlineAfter := int64(300)
	if s.cfg != nil {
		offlineAfter = int64(s.cfg.Thresholds().OfflineAfter.Seconds())
	}
	now := time.Now().Unix()

	type scored struct {
		row     map[string]any
		hostID  string
		live    bool
		online  bool
		updated time.Time
		guests  int
	}
	groups := map[string][]scored{}
	order := []string{}
	for _, row := range rows {
		hid, _ := row["host_id"].(string)
		if hid == "" {
			continue
		}
		name, _ := row["host_name"].(string)
		key := s.hypervDedupKey(hid, name)
		online := false
		if h := s.hostByID(hid); h != nil {
			online = now-h.LastSeen <= offlineAfter
		}
		sc := scored{
			row:     row,
			hostID:  hid,
			live:    s.hv.has(hid),
			online:  online,
			updated: hypervRowUpdatedAt(row),
			guests:  hypervRowGuestCount(row),
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], sc)
	}

	better := func(a, b scored) bool {
		if a.live != b.live {
			return a.live
		}
		if a.online != b.online {
			return a.online
		}
		if !a.updated.Equal(b.updated) {
			return a.updated.After(b.updated)
		}
		if a.guests != b.guests {
			return a.guests > b.guests
		}
		return a.hostID < b.hostID
	}

	kept = make([]map[string]any, 0, len(groups))
	for _, key := range order {
		g := groups[key]
		if len(g) == 1 {
			kept = append(kept, g[0].row)
			continue
		}
		sort.Slice(g, func(i, j int) bool { return better(g[i], g[j]) })
		kept = append(kept, g[0].row)
		for _, loser := range g[1:] {
			// Never mark a still-live reporter as stale (clone / dual-agent edge case).
			if loser.live {
				kept = append(kept, loser.row)
				continue
			}
			staleIDs = append(staleIDs, loser.hostID)
		}
	}
	return kept, staleIDs
}
