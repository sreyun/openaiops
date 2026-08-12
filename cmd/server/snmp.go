package main

// SNMP 采集数据的服务端接入：Agent 上报接收（指纹校验）、VM 时序写入（基数封顶）、
// PG 快照存储、前端查询端点。风格对齐 hardware_netflow.go 的 handleAgentNetFlow /
// vmNetFlowMetrics / rollupNetFlow。

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"aiops-monitor/shared"
)

// trapSevRule 是一条 trapOID 前缀 → 严重度的映射。
type trapSevRule struct{ prefix, severity string }

// builtinTrapSeverity 是内置的企业私有 trap 严重度精修表。只收录确定性高、跨型号稳定的
// 条目（Cisco ciscoEnvMon 环境监控子树最典型），其余交给用户可配的 SNMPTrapSeverity。
// agent 端对企业私有 trap 保守判为 info，这里把明确该关注的抬到 warning/critical。
var builtinTrapSeverity = []trapSevRule{
	{"1.3.6.1.4.1.9.9.13.3.0.2", "critical"}, // Cisco 冗余电源故障
	{"1.3.6.1.4.1.9.9.13.3.0.3", "critical"}, // Cisco 温度告警
	{"1.3.6.1.4.1.9.9.13.3.0.4", "critical"}, // Cisco 风扇故障
	{"1.3.6.1.4.1.9.9.13.3.0.5", "warning"},  // Cisco 温度关机预警
}

// validTrapSeverity 只接受三档，防用户配置里写入非法值污染告警链路。
func validTrapSeverity(s string) bool {
	return s == "info" || s == "warning" || s == "critical"
}

// refineTrapSeverity 精修 trap 严重度：用户配置(前缀最长匹配)优先 → 内置厂商表 →
// 回退 agent 端启发式判定。让企业私有 trap 不再一律 info，且用户无需重装 agent 即可调整。
func refineTrapSeverity(trapOID, agentSev string, override map[string]string) string {
	if s := longestPrefixSeverity(trapOID, override); s != "" {
		return s
	}
	for _, r := range builtinTrapSeverity {
		if trapOID == r.prefix || strings.HasPrefix(trapOID, r.prefix+".") {
			return r.severity
		}
	}
	return agentSev
}

// longestPrefixSeverity 在用户覆盖表里做「最长前缀匹配」，返回其严重度（无匹配返回 ""）。
func longestPrefixSeverity(trapOID string, m map[string]string) string {
	best, bestLen := "", -1
	for prefix, sev := range m {
		if !validTrapSeverity(sev) {
			continue
		}
		if (trapOID == prefix || strings.HasPrefix(trapOID, prefix+".")) && len(prefix) > bestLen {
			best, bestLen = sev, len(prefix)
		}
	}
	return best
}

// snmpMaxIfaces 是单台设备一轮最多写入 VM 的接口数上限。接口是稳定基数（不像
// netflow 的 src_port 那样爆炸），但仍硬性封顶，守住"时序库成本由序列数决定"这条命。
const snmpMaxIfaces = 300

// ============================================================================
// Agent-facing ingest（指纹校验）
// ============================================================================

// handleAgentSNMP 接收 agent 轮询上报的 SNMP 设备指标。
func (s *Server) handleAgentSNMP(w http.ResponseWriter, r *http.Request) {
	var rep shared.SNMPReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if rep.HostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id required"})
		return
	}
	fp := r.Header.Get("X-Agent-Fingerprint")
	if fp == "" {
		fp = r.URL.Query().Get("fp")
	}
	if !s.forwardFingerprintOKByHost(rep.HostID, fp) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "fingerprint mismatch"})
		return
	}

	// 缓存最新快照供告警评估每轮复用（含采集失败的快照，用于报"采集失败"告警）。
	// put 按 IP 合并，不会因「每轮只报一台设备」冲掉同 host 下其它设备。
	hostname, ip := rep.HostID, ""
	if h := s.hostByID(rep.HostID); h != nil {
		hostname, ip = h.Hostname, h.IP
	}
	s.snmp.put(rep.HostID, hostname, ip, rep.Snapshots)

	if s.pg != nil {
		// Rename detection: config "name" changed for the same IP → migrate the
		// old PG row (and operSeen keys) instead of inserting a duplicate.
		seenRename := map[string]bool{}
		for _, snap := range rep.Snapshots {
			if snap.TargetIP == "" || snap.TargetName == "" {
				continue
			}
			oldName := s.pg.findSNMPDeviceByIP(rep.HostID, snap.TargetIP)
			if oldName != "" && oldName != snap.TargetName && !seenRename[oldName] {
				s.pg.renameSNMPDevice(rep.HostID, oldName, snap.TargetName)
				s.snmp.migrateDeviceKey(rep.HostID, oldName, snap.TargetName)
				seenRename[oldName] = true
			}
		}
	}

	for _, snap := range rep.Snapshots {
		// 采集失败（超时/认证失败）时快照各字段是零值：只报警不落库/不写时序，
		// 否则会把上一份好数据覆盖成空白，接口瞬间全变 down。
		if snap.Error != "" {
			slog.Warn("SNMP 采集失败，保留上一份快照不覆盖", "host", rep.HostID, "device", snap.TargetName, "err", snap.Error)
			continue
		}
		s.vmSNMPMetrics(rep.HostID, snap)
		if s.pg != nil {
			s.pg.upsertSNMPSnapshot(rep.HostID, snap)
			// 历史改名残留：同 IP 下旧 device_name 行一并清掉。
			s.pg.purgeOtherSNMPByIP(rep.HostID, snap.TargetName, snap.TargetIP)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAgentSNMPTrap 接收 agent 上报的 SNMP Trap 事件：落库 + 事件日志 +
// warning/critical 直接走渠道推送（范式 B），critical 额外转 Incident 复用学习回路。
func (s *Server) handleAgentSNMPTrap(w http.ResponseWriter, r *http.Request) {
	var rep shared.SNMPTrapReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if rep.HostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id required"})
		return
	}
	fp := r.Header.Get("X-Agent-Fingerprint")
	if fp == "" {
		fp = r.URL.Query().Get("fp")
	}
	if !s.forwardFingerprintOKByHost(rep.HostID, fp) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "fingerprint mismatch"})
		return
	}

	hostname := rep.HostID
	if h := s.hostByID(rep.HostID); h != nil {
		hostname = h.Hostname
	}
	cfg := s.cfg.Get()
	for _, ev := range rep.Traps {
		// 服务端精修严重度：企业私有 trap 不再一律 info，且用户可配无需重装 agent。
		sev := refineTrapSeverity(ev.TrapOID, ev.Severity, cfg.SNMPTrapSeverity)
		if s.pg != nil {
			s.pg.insertSNMPTrap(rep.HostID, ev)
		}
		// 事件流：每条 trap 记一条系统日志（含 info 级，供审计回溯）。
		s.store.AddLog(LogEntry{
			Kind: KindSystem, Level: sev, Actor: "SNMP Trap", Host: hostname,
			Message: Tz("alert.trap_event", ev.SourceIP, ev.TrapOID),
		})
		// 告警：warning/critical 走统一渠道推送（含治理静默/路由）。
		if sev == "warning" || sev == "critical" {
			a := Alert{
				HostID: rep.HostID, Hostname: hostname, IP: ev.SourceIP,
				Level: sev, Type: "trap",
				Scope:     ev.SourceIP + "/" + ev.TrapOID,
				Message:   Tz("alert.trap_event", ev.SourceIP, ev.TrapOID),
				Timestamp: ev.Timestamp,
			}
			if cfg.AlertsEnabled {
				s.notifier.pushChannels(cfg, a, true)
			}
			// critical trap 转 Incident，自动获相似历史召回 + 解决经验沉淀。
			if sev == "critical" && s.incidents != nil {
				s.incidents.OnAlertTransition(a, alertKey(a), true)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ============================================================================
// VM 写入（基数封顶）
// ============================================================================

func (s *Server) vmSNMPMetrics(hostID string, snap shared.SNMPSnapshot) {
	if !s.vm.enabled() {
		return
	}
	for _, line := range rollupSNMP(hostID, snap) {
		s.vm.pushRawLine(line)
	}
}

// rollupSNMP 把一台设备一轮快照转成一组 BOUNDED 的 Prometheus 行。
// 抽成纯函数是为了能直接对"产出多少条序列"做断言——每接口固定条数、且接口数封顶。
// 注意：Prometheus 导入格式时间戳单位是毫秒（snap.Timestamp 是秒，须 *1000，
// 否则历史全写进 1970，见 hardware_netflow.go 同款注释）。
func rollupSNMP(hostID string, snap shared.SNMPSnapshot) []string {
	var out []string
	ts := snap.Timestamp * 1000
	host := lblEsc(hostID)
	device := lblEsc(snap.TargetName)
	deviceIP := lblEsc(snap.TargetIP)

	reach := 0.0
	if snap.Reachable {
		reach = 1
	}
	out = append(out, fmt.Sprintf(`aiops_snmp_reachable{host="%s",device="%s"} %g %d`, host, device, reach, ts))
	if snap.System.UptimeSec > 0 {
		out = append(out, fmt.Sprintf(`aiops_snmp_sys_uptime{host="%s",device="%s"} %g %d`, host, device, snap.System.UptimeSec, ts))
	}

	ifaces := snap.Interfaces
	if len(ifaces) > snmpMaxIfaces {
		slog.Warn("SNMP 接口数超过 VM 写入上限，截断", "host", hostID, "device", snap.TargetName, "count", len(ifaces), "max", snmpMaxIfaces)
		ifaces = ifaces[:snmpMaxIfaces]
	}
	for _, iface := range ifaces {
		// device_ip + ifindex are stable identities. Keep device/ifname as display
		// labels for compatibility, while history queries use the stable pair so
		// device/interface renames don't make the curve disappear.
		lbl := fmt.Sprintf(`host="%s",device="%s",device_ip="%s",ifindex="%d",ifname="%s"`,
			host, device, deviceIP, iface.Index, lblEsc(iface.Name))
		operUp := 0.0
		if iface.OperUp {
			operUp = 1
		}
		out = append(out, fmt.Sprintf(`aiops_snmp_if_oper_up{%s} %g %d`, lbl, operUp, ts))
		if iface.SpeedBps > 0 {
			out = append(out, fmt.Sprintf(`aiops_snmp_if_speed_bps{%s} %d %d`, lbl, iface.SpeedBps, ts))
		}
		if iface.RateValid {
			out = append(out,
				fmt.Sprintf(`aiops_snmp_if_in_bps{%s} %g %d`, lbl, iface.InBps, ts),
				fmt.Sprintf(`aiops_snmp_if_out_bps{%s} %g %d`, lbl, iface.OutBps, ts),
				fmt.Sprintf(`aiops_snmp_if_in_util{%s} %g %d`, lbl, iface.InUtilPercent, ts),
				fmt.Sprintf(`aiops_snmp_if_out_util{%s} %g %d`, lbl, iface.OutUtilPercent, ts),
				fmt.Sprintf(`aiops_snmp_if_in_err_pps{%s} %g %d`, lbl, iface.InErrPps, ts),
				fmt.Sprintf(`aiops_snmp_if_out_err_pps{%s} %g %d`, lbl, iface.OutErrPps, ts),
				fmt.Sprintf(`aiops_snmp_if_in_disc_pps{%s} %g %d`, lbl, iface.InDiscardPps, ts),
				fmt.Sprintf(`aiops_snmp_if_out_disc_pps{%s} %g %d`, lbl, iface.OutDiscardPps, ts))
		}
	}
	return out
}

// ============================================================================
// 前端查询端点
// ============================================================================

// handleSNMPList 返回一台主机（agent）下所有被轮询设备的最新快照。
// handleSNMPHosts returns only the hosts that actually have SNMP network-device
// data (polled devices and/or traps), ranked by device count. The frontend uses
// this to filter the host selector to "hosts with network devices", hiding the rest.
func (s *Server) handleSNMPHosts(w http.ResponseWriter, r *http.Request) {
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"hosts": []any{}})
		return
	}
	hosts, err := s.pg.getSNMPHosts()
	if err != nil {
		slog.Warn("查询有网络设备的主机失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	hosts = s.filterInventoryRows(r, hosts)
	s.annotateHostNames(hosts)
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts})
}

func (s *Server) handleSNMPList(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"devices": []any{}})
		return
	}
	devices, err := s.pg.getSNMPSnapshots(hostID)
	if err != nil {
		slog.Warn("查询 SNMP 快照失败", "host", hostID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

// handleDeleteSNMP removes one polled network device's snapshot (orphan cleanup
// after a target is removed from agent config, or leftover rename rows).
func (s *Server) handleDeleteSNMP(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("hostID")
	device := r.URL.Query().Get("device")
	if hostID == "" || device == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hostID and device required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	s.snmp.remove(hostID, device)
	if s.pg != nil {
		s.pg.deleteSNMPSnapshot(hostID, device)
	}
	label := s.hostLabelForID(hostID)
	slog.Info("删除 SNMP 设备记录", "host", hostID, "device", device, "actor", s.clientIP(r))
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Host: label, Message: Tz("log.delete_snmp", label, device)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSNMPTraps 返回一台主机最近收到的 trap 事件。
func (s *Server) handleSNMPTraps(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"traps": []any{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	traps, err := s.pg.getSNMPTraps(hostID, limit)
	if err != nil {
		slog.Warn("查询 SNMP Trap 失败", "host", hostID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"traps": traps})
}

var snmpHistoryMetrics = map[string]string{
	"oper_up":      "aiops_snmp_if_oper_up",
	"speed_bps":    "aiops_snmp_if_speed_bps",
	"in_bps":       "aiops_snmp_if_in_bps",
	"out_bps":      "aiops_snmp_if_out_bps",
	"in_util":      "aiops_snmp_if_in_util",
	"out_util":     "aiops_snmp_if_out_util",
	"in_err_pps":   "aiops_snmp_if_in_err_pps",
	"out_err_pps":  "aiops_snmp_if_out_err_pps",
	"in_disc_pps":  "aiops_snmp_if_in_disc_pps",
	"out_disc_pps": "aiops_snmp_if_out_disc_pps",
}

// requestTimeRange accepts the same absolute from/to model as host trends and
// falls back to the legacy relative range query.
func requestTimeRange(r *http.Request) (int64, int64) {
	from, ferr := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, terr := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if ferr == nil && terr == nil && from > 0 && to > from {
		return from, to
	}
	return parseTimeRange(r.URL.Query().Get("range"))
}

// handleSNMPInterfaceHistory returns all important time series for one
// interface. ifindex is the stable identity; device_ip keeps history available
// across display-name changes. Legacy series are queried by device/ifname.
func (s *Server) handleSNMPInterfaceHistory(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	device := r.URL.Query().Get("device")
	deviceIP := r.URL.Query().Get("device_ip")
	ifindex := r.URL.Query().Get("ifindex")
	ifname := r.URL.Query().Get("ifname")
	if hostID == "" || (ifindex == "" && ifname == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host and interface required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	if !s.vm.enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"series": map[string]any{}})
		return
	}
	from, to := requestTimeRange(r)
	baseMatcher := fmt.Sprintf(`host="%s"`, lblEsc(hostID))
	matchers := baseMatcher
	if deviceIP != "" {
		matchers += fmt.Sprintf(`,device_ip="%s"`, lblEsc(deviceIP))
	} else if device != "" {
		matchers += fmt.Sprintf(`,device="%s"`, lblEsc(device))
	}
	if ifindex != "" {
		if _, err := strconv.Atoi(ifindex); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ifindex"})
			return
		}
		matchers += fmt.Sprintf(`,ifindex="%s"`, ifindex)
	} else {
		matchers += fmt.Sprintf(`,ifname="%s"`, lblEsc(ifname))
	}

	series := make(map[string][]any, len(snmpHistoryMetrics))
	for key, metric := range snmpHistoryMetrics {
		promql := metric + "{" + matchers + "}"
		// Series written before device_ip was introduced are still addressable by
		// the old device label. Union them so upgrading doesn't hide old history.
		if deviceIP != "" && device != "" {
			legacy := baseMatcher + fmt.Sprintf(`,device="%s"`, lblEsc(device))
			if ifindex != "" {
				legacy += fmt.Sprintf(`,ifindex="%s"`, ifindex)
			} else {
				legacy += fmt.Sprintf(`,ifname="%s"`, lblEsc(ifname))
			}
			promql = "(" + promql + " or " + metric + "{" + legacy + "})"
		}
		series[key] = s.vm.queryRawRange(promql, from, to)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"series": series, "from": from, "to": to,
		"host": hostID, "device": device, "device_ip": deviceIP,
		"ifindex": ifindex, "ifname": ifname,
	})
}
