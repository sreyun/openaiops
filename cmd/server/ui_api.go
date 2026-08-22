package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	all := s.store.ListHosts()
	// 迁移是**全局**动作：首次迁移那条分支会用传进去的列表整体重建 HostFolders 与
	// HostFolderAssign。这里原来传的是按权限过滤后的列表——只要升级后第一个访问
	// /api/v1/hosts 的人是受限用户（只授权了几台机器），其余所有主机的分类就会在这一次
	// 请求里被丢掉。谁在看，不能决定别人的数据怎么迁。
	if s.cfg.ensureHostFoldersMigrated(all) {
		_ = s.cfg.save()
	}
	hosts := s.filterHostsForUser(r, all)
	now := time.Now().Unix()
	offline := int64(s.cfg.Thresholds().OfflineAfter.Seconds())
	// staleAfter 是"数据滞后但主机尚未判离线"的阈值。它必须高于正常上报节奏（默认 30s），
	// 否则每个上报周期后半段都会误报 ⚠。取离线阈值的 2/3（默认 60s → 40s），既随阈值档位
	// 自适应，又始终落在 [staleAfter, offline) 的告警带内，避免与正常节拍冲突而频繁闪烁。
	staleAfter := offline * 2 / 3
	if staleAfter < 1 {
		staleAfter = 1
	}

	folders, assign := s.cfg.hostFoldersSnapshot()
	paths := folderPathMap(folders)

	type hostView struct {
		*Host
		Online     bool   `json:"online"`
		Stale      bool   `json:"stale"`
		FolderID   string `json:"folder_id"`
		FolderPath string `json:"folder_path,omitempty"`
	}
	views := make([]hostView, 0, len(hosts))
	for _, h := range hosts {
		if cat, ok := s.cfg.CategoryOverride(h.ID); ok {
			h.Category = cat // manual override wins over the agent-reported category
		}
		// SECURITY: never expose the agent fingerprint to the browser. It is the
		// sole credential authenticating the agent reverse channels (terminal
		// rx/tx, report, logs, forward); leaking it to any viewer would let them
		// hijack terminals or spoof host telemetry. h is a copy (hostMeta), so
		// blanking it here does not affect the stored host.
		h.Fingerprint = ""
		age := now - h.LastSeen
		online := age <= offline
		fid := assign[h.ID]
		if fid == "" {
			fid = HostFolderUngroupedID
		}
		fpath := paths[fid]
		views = append(views, hostView{Host: h, Online: online, Stale: online && age > staleAfter, FolderID: fid, FolderPath: fpath})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].FolderPath != views[j].FolderPath {
			return views[i].FolderPath < views[j].FolderPath
		}
		if views[i].Category != views[j].Category {
			return views[i].Category < views[j].Category
		}
		return views[i].Hostname < views[j].Hostname
	})
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleHostMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireHostAccess(w, r, id) {
		return
	}
	samples, ok := s.store.GetSamples(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.host_not_found")})
		return
	}
	writeJSON(w, http.StatusOK, samples)
}

// handleHostHistory returns time-series data for a host within [from, to] range.
// Query params: from (unix timestamp), to (unix timestamp).
// Defaults: from = now - 24h, to = now.
func (s *Server) handleHostHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireHostAccess(w, r, id) {
		return
	}
	now := time.Now().Unix()

	// Parse query parameters
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	var from, to int64
	if toStr != "" {
		var err error
		to, err = strconv.ParseInt(toStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_to_param")})
			return
		}
	} else {
		to = now
	}

	if fromStr != "" {
		var err error
		from, err = strconv.ParseInt(fromStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_from_param")})
			return
		}
	} else {
		from = now - 86400 // default: last 24 hours
	}

	if from >= to {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.from_less_than_to")})
		return
	}

	const hostHistoryAPIMaxPts = 600
	out, source, ok := s.loadDurableHostHistorySource(id, from, to, nil)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.host_not_found")})
		return
	}
	// The body stays a bare array (the classic console does Array.isArray on it),
	// so provenance rides on a header. A degraded read is the difference between
	// "this host has no data" and "VictoriaMetrics did not answer" — which used to
	// be indistinguishable from the outside.
	w.Header().Set("X-AIOps-History-Source", source)
	if source == historySourceFallback {
		// 把「为什么」也带上：查询失败 / 熔断 / 窗口为空的处置方向完全不同，
		// 只说「没返回数据」等于把人推回猜测。见 vm_diag.go。
		if reason := historyFallbackReasonFor(id); reason != "" {
			w.Header().Set("X-AIOps-History-Reason", reason)
		}
		s.warnHistoryFallback(id, from, to)
	}

	writeJSON(w, http.StatusOK, downsampleSamples(out, hostHistoryAPIMaxPts))
}

// handleSetCategory sets (or clears, when empty) a manual category override.
func (s *Server) handleSetCategory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireHostAccess(w, r, id) {
		return
	}
	var req struct {
		Category string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	cat := strings.TrimSpace(req.Category)
	_ = s.cfg.SetCategory(id, cat)
	label := s.hostLabelForID(id)
	msg := Tz("log.set_category", label, cat)
	if cat == "" {
		msg = Tz("log.clear_category", label)
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Host: label, Message: msg})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "host_id": id, "category": cat})
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireHostAccess(w, r, id) {
		return
	}
	label := s.hostLabelForID(id)
	ok := s.store.DeleteHost(id)
	lastHistoryFallbackReason.Delete(id) // 主机没了，它的降级留痕也该走
	_ = s.cfg.forgetHost(id)             // 主机没了，配置里它的分类与分组归属一并删掉（不是标成"未分组"）
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.host_not_found")})
		return
	}
	// 主机删了，连带清掉该 host_id 下的 Hyper-V 清单，避免虚拟机树留下幽灵宿主机。
	s.removeHyperVForHost(id)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Host: label, Message: Tz("log.delete_host", label)})
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "host_id": id})
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	// Active alerts from real-time evaluation (snapshot of current metric state)
	hosts := s.filterHostsForUser(r, s.store.ListHosts())
	alerts := Evaluate(hosts, s.cfg.Thresholds())
	// Hyper-V 虚拟机告警并入实时列表（与 CPU/磁盘等一致地带上 Since/Status）
	alerts = append(alerts, EvaluateHyperV(s.hv)...)
	// SNMP 网络设备 + NetFlow 流量异常并入实时列表
	alerts = append(alerts, EvaluateSNMP(s.snmp, s.cfg.Thresholds())...)
	alerts = append(alerts, EvaluateNetFlow(s.nf, s.cfg.Thresholds())...)
	// 列表与通知看到的是同一条信息：告警属于哪个分组。
	alerts = s.cfg.decorateAlertGroups(alerts)
	since := s.notifier.ActiveSince()
	states := s.store.AlertStates()
	for i := range alerts {
		if t, ok := since[alertKey(alerts[i])]; ok {
			alerts[i].Since = t
		}
		alerts[i].Status = states[alertKey(alerts[i])]
	}
	alerts = append(alerts, s.checks.DownAlerts()...)
	for i := range alerts {
		if alerts[i].Status == "" {
			if st, ok := states[alertKey(alerts[i])]; ok {
				alerts[i].Status = st
			}
		}
	}
	if alerts == nil {
		alerts = []Alert{}
	}
	// Append resolved alerts from persistent history so the alerts page shows
	// both currently-firing and recently-recovered records.
	showHistory := r.URL.Query().Get("history") == "true"
	sevenDaysAgo := time.Now().Unix() - 7*86400
	history := s.filterAlertRecordsForUser(r, s.store.AlertHistory(200, false))
	for _, rec := range history {
		// Skip records that are still active (already covered by Evaluate result)
		if rec.ResolvedAt == 0 {
			continue
		}
		// Without ?history=true, only include records resolved within the last 7 days
		if !showHistory && rec.ResolvedAt < sevenDaysAgo {
			continue
		}
		alerts = append(alerts, Alert{
			HostID:    rec.HostID,
			Hostname:  rec.Hostname,
			IP:        rec.IP,
			Level:     rec.Level,
			Type:      rec.Type,
			Scope:     rec.Scope,
			Since:     rec.FiredAt,
			Message:   rec.Message,
			Value:     rec.Value,
			Timestamp: rec.ResolvedAt,
			Status:    "resolved",
		})
	}
	alerts = s.filterAlertsForUser(r, alerts)
	writeJSON(w, http.StatusOK, alerts)
}

// handleAlertHistory returns the full persistent alert history for audit and
// historical queries. Supports: ?limit=N&status=firing|resolved|all
func (s *Server) handleAlertHistory(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	statusFilter := r.URL.Query().Get("status")
	activeOnly := statusFilter == "firing"
	records := s.store.AlertHistory(limit, activeOnly)
	if statusFilter == "resolved" {
		filtered := records[:0]
		for _, rec := range records {
			if rec.ResolvedAt > 0 {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
	}
	if records == nil {
		records = []AlertRecord{}
	}
	records = s.filterAlertRecordsForUser(r, records)
	writeJSON(w, http.StatusOK, records)
}

// handleEvents returns recent plugin-generated events (the Python/AI layer's findings).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	events := s.store.RecentEvents()
	if events == nil {
		events = []storedEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

// handleActivity returns the unified activity log (operations + system + plugin).
// Messages / Host fields are sanitized so operators never see raw host IDs.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	items := s.store.RecentActivity()
	if items == nil {
		items = []LogEntry{}
	}
	labels := s.buildHostLabelMap()
	out := make([]LogEntry, len(items))
	for i, e := range items {
		out[i] = s.sanitizeActivityEntry(e, labels)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHostsMeta returns minimal host info (id + hostname) for the process-check UI.
func (s *Server) handleHostsMeta(w http.ResponseWriter, r *http.Request) {
	hosts := s.filterHostsForUser(r, s.store.ListHosts())
	type hostMeta struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
	}
	out := make([]hostMeta, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, hostMeta{ID: h.ID, Hostname: h.Hostname})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	hosts := s.filterHostsForUser(r, s.store.ListHosts())
	now := time.Now().Unix()
	th := s.cfg.Thresholds()
	offline := int64(th.OfflineAfter.Seconds())

	online := 0
	for _, h := range hosts {
		if now-h.LastSeen <= offline {
			online++
		}
	}
	crit, warn := 0, 0
	summ := append(append(Evaluate(hosts, th), s.checks.DownAlerts()...), EvaluateForward(s.forward.Snapshot(), th)...)
	summ = append(summ, EvaluateHyperV(s.hv)...)
	summ = append(summ, EvaluateSNMP(s.snmp, th)...)
	summ = append(summ, EvaluateNetFlow(s.nf, th)...)
	summ = s.filterAlertsForUser(r, summ)
	for _, a := range summ {
		if a.Level == "critical" {
			crit++
		} else {
			warn++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_hosts":      len(hosts),
		"online_hosts":     online,
		"offline_hosts":    len(hosts) - online,
		"critical_alerts":  crit,
		"warning_alerts":   warn,
		"plugin_events":    len(s.store.RecentEvents()),
		"server_time_unix": now,
		"version":          appVersion,
		"terminal_enabled": s.cfg.TerminalEnabled(),
		"desktop_enabled":  s.cfg.TerminalEnabled(),
		// 这个二进制里到底有没有打进 Vue 控制台。经典版据此决定要不要显示「新版控制台」
		// 入口——没构建前端时 /v2 是 404，给一个点了就 404 的菜单项比没有还糟。
		"v2_console": v2ConsoleEmbedded(),
	})
}

// alertAckSilenceReq is the JSON body for ack/silence operations.
type alertAckSilenceReq struct {
	HostID string `json:"host_id"`
	Type   string `json:"type"`
	Scope  string `json:"scope"`
}

func (s *Server) handleAlertAck(w http.ResponseWriter, r *http.Request) {
	var req alertAckSilenceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if req.HostID != "" && !s.requireHostAccess(w, r, req.HostID) {
		return
	}
	key := req.HostID + "/" + req.Type + "/" + req.Scope
	s.store.SetAlertState(key, "acknowledged")
	label := s.hostLabelForID(req.HostID)
	msg := Tz("log.alert_ack", label, req.Type)
	if req.Scope != "" {
		msg = Tz("log.alert_ack_scope", label, req.Type, req.Scope)
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Host: label, Message: msg})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "key": key, "new_status": "acknowledged"})
}

func (s *Server) handleAlertSilence(w http.ResponseWriter, r *http.Request) {
	var req alertAckSilenceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if req.HostID != "" && !s.requireHostAccess(w, r, req.HostID) {
		return
	}
	key := req.HostID + "/" + req.Type + "/" + req.Scope
	s.store.SetAlertState(key, "silenced")
	label := s.hostLabelForID(req.HostID)
	msg := Tz("log.alert_silence", label, req.Type)
	if req.Scope != "" {
		msg = Tz("log.alert_silence_scope", label, req.Type, req.Scope)
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Host: label, Message: msg})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "key": key, "new_status": "silenced"})
}

func (s *Server) handleAlertClear(w http.ResponseWriter, r *http.Request) {
	var req alertAckSilenceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	// 清除告警状态是写操作：主机组授权受限的账号不能清掉范围外主机的告警
	// （告警列表本身已按 filterAlertsForUser 过滤，这里补上写入侧的同一条线）。
	if req.HostID != "" && !s.requireHostAccess(w, r, req.HostID) {
		return
	}
	key := req.HostID + "/" + req.Type + "/" + req.Scope
	s.store.ClearAlertState(key)
	label := s.hostLabelForID(req.HostID)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Host: label, Message: Tz("log.alert_clear", label, req.Type)})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "key": key, "new_status": ""})
}
