package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"aiops-monitor/shared"
)

// ============================================================================
// Agent-facing ingest endpoints (fingerprint-gated)
// ============================================================================

// handleAgentHardware receives Redfish hardware snapshots from agents.
func (s *Server) handleAgentHardware(w http.ResponseWriter, r *http.Request) {
	var rep shared.HardwareReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		slog.Warn("硬件上报 JSON 解析失败", "err", err, "remote", r.RemoteAddr)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if rep.HostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id required"})
		return
	}

	// Fingerprint verification (same pattern as terminal/forward)
	fp := r.Header.Get("X-Agent-Fingerprint")
	if fp == "" {
		fp = r.URL.Query().Get("fp")
	}
	if !s.forwardFingerprintOKByHost(rep.HostID, fp) {
		slog.Warn("硬件上报指纹校验失败", "host_id", rep.HostID, "fp", fp, "remote", r.RemoteAddr)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "fingerprint mismatch"})
		return
	}

	// 缓存最新快照（供告警评估每轮复用，避免每 10s 查一次 PG）
	hostname, ip := rep.HostID, ""
	if h := s.hostByID(rep.HostID); h != nil {
		hostname, ip = h.Hostname, h.IP
	}
	s.hw.put(rep.HostID, hostname, ip, rep.Snapshots)

	// Store snapshots in PG (upsert)
	if s.pg != nil {
		// Rename detection: when a user changes config.json "name" for the same
		// physical device (same target_url), migrate the old record to the new name
		// so history/events/changes aren't orphaned under the old name.
		seenRename := map[string]bool{} // old_name → already migrated
		for _, snap := range rep.Snapshots {
			if snap.TargetURL == "" {
				continue
			}
			oldName := s.pg.findHardwareTargetByURL(rep.HostID, snap.TargetURL)
			if oldName != "" && oldName != snap.TargetName && !seenRename[oldName] {
				s.pg.renameHardwareTarget(rep.HostID, oldName, snap.TargetName)
				// Update in-memory lastHealth tracking: move old key to new
				s.hw.migrateHealthKey(rep.HostID, oldName, snap.TargetName)
				seenRename[oldName] = true
			}
		}

		for _, snap := range rep.Snapshots {
			// 采集失败（BMC 超时等）时快照各字段都是零值：直接 upsert 会把上一份**好数据**
			// 覆盖成空白，整张卡片瞬间变“无数据/严重”。这类快照只报警不落库。
			if snap.Error != "" {
				slog.Warn("硬件采集失败，保留上一次快照不覆盖", "host_id", rep.HostID, "target", snap.TargetName, "err", snap.Error)
				continue
			}
			// 资产变更必须在 upsert **之前**比对：upsert 会把上一份快照覆盖掉，
			// 之后就没有"旧值"可比了。
			s.recordHardwareChanges(rep.HostID, snap)

			s.pg.upsertHardwareSnapshot(rep.HostID, snap)
			s.pg.purgeOtherHardwareByURL(rep.HostID, snap.TargetName, snap.TargetURL)

			// Write numeric metrics to VM
			s.vmHardwareMetrics(rep.HostID, snap)

			// 健康事件：仅在**状态变化**时记一条。此前每轮（30-60s）都插，一台 Warning 主机
			// 会无限追加相同事件，且该表无查询/无保留策略。
			if snap.Health != "" && snap.Health != "OK" && s.hw.healthChanged(rep.HostID, snap.TargetName, snap.Health) {
				s.pg.insertHardwareEvent(rep.HostID, snap.TargetName, "health_change",
					strings.ToLower(snap.Health), fmt.Sprintf("健康状态: %s", snap.Health))
			}
		}
		slog.Info("硬件上报已存储", "host_id", rep.HostID, "snapshots", len(rep.Snapshots))
	} else {
		slog.Warn("硬件上报已接收但 PG 未配置，数据未持久化", "host_id", rep.HostID, "snapshots", len(rep.Snapshots))
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAgentNetFlow receives aggregated NetFlow/packet flows from agents.
func (s *Server) handleAgentNetFlow(w http.ResponseWriter, r *http.Request) {
	var rep shared.NetFlowReport
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

	// Write aggregated metrics to VM
	s.vmNetFlowMetrics(rep.HostID, rep)

	// 喂给流量异常告警的每主机基线（突增/丢包检测），不查 PG。
	hostname, ip := rep.HostID, ""
	if h := s.hostByID(rep.HostID); h != nil {
		hostname, ip = h.Hostname, h.IP
	}
	s.nf.put(rep.HostID, hostname, ip, rep)

	// 存 Flow 明细到 PG（供明细查询/CSV 导出）——异步入队，HTTP 摄入立即返回，
	// 不再在请求里同步写上万行、占住连接池导致其它请求（含 UI）断线。
	if s.pg != nil && len(rep.Flows) > 0 {
		s.pg.insertFlowRecordsAsync(rep.HostID, rep.Source, rep.Flows)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ============================================================================
// Frontend query endpoints
// ============================================================================

// handleHardwareHealth returns the latest hardware snapshot for a host.
func (s *Server) handleHardwareHealth(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")

	// 不带 host = 批量模式：一次取回调用者有权看见的全部快照。
	//
	// 这条分支是为了根治控制台硬件页的一次规模事故：它原来「先取全部主机，再对每一台
	// 各发一个 /hardware/health」——500 台现场就是 500 个并发请求，浏览器每域名 6 条
	// 连接排队、服务端跑 500 次 handler、API 限流开始回 429，而其中约 470 个请求
	// 落在根本没有 BMC 数据的虚拟机上，纯属浪费。
	//
	// **RBAC 不因为改成批量就放宽**：仍然逐行过 userCanAccessHost，
	// 只是把"逐台请求"换成了"一次请求 + 服务端过滤"。
	if hostID == "" {
		if s.pg == nil {
			writeJSON(w, http.StatusOK, map[string]any{"snapshots": []any{}})
			return
		}
		u, ok := s.currentUser(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		all, err := s.pg.getAllHardwareSnapshots()
		if err != nil {
			slog.Warn("查询全部硬件快照失败", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
			return
		}
		can := s.hostAccessFor(u)
		out := make([]map[string]any, 0, len(all))
		for _, row := range all {
			hid, _ := row["host_id"].(string)
			if hid == "" || !can(hid) {
				continue
			}
			out = append(out, row)
		}
		writeJSON(w, http.StatusOK, map[string]any{"snapshots": out})
		return
	}

	if !s.requireHostAccess(w, r, hostID) {
		return
	}

	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"snapshots": []any{}})
		return
	}

	snapshots, err := s.pg.getHardwareSnapshots(hostID)
	if err != nil {
		slog.Warn("查询硬件快照失败", "host", hostID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": snapshots})
}

// handleHardwareEvents returns recorded hardware state transitions for a host.
// These complement the BMC's own SEL (which rides along in the snapshot): the
// SEL is the vendor's view, this is what our own polling actually observed.
func (s *Server) handleHardwareEvents(w http.ResponseWriter, r *http.Request) {
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
	events, err := s.pg.getHardwareEvents(hostID, r.URL.Query().Get("target"), limit)
	if err != nil {
		slog.Warn("查询硬件事件失败", "host", hostID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleDeleteHardware deletes a specific hardware target's snapshot,
// events and change records from both in-memory store and PostgreSQL.
func (s *Server) handleDeleteHardware(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("hostID")
	target := r.URL.Query().Get("target")
	if hostID == "" || target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hostID and target required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	// 从内存中移除
	s.hw.remove(hostID, target)
	// 从 PG 中级联删除快照 + 事件 + 变更记录
	if s.pg != nil {
		s.pg.deleteHardwareSnapshot(hostID, target)
	}
	label := s.hostLabelForID(hostID)
	slog.Info("删除硬件资产记录", "host", hostID, "target", target, "actor", s.clientIP(r))
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Host: label, Message: Tz("log.delete_hardware", label, target)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleHardwareHistory returns hardware metric history from VM.
func (s *Server) handleHardwareHistory(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	metric := r.URL.Query().Get("metric")  // temperature, power, fan_rpm, health_score
	rangeStr := r.URL.Query().Get("range") // e.g. "24h", "7d"

	if hostID == "" || metric == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host and metric required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}

	from, to := parseTimeRange(rangeStr)
	target := r.URL.Query().Get("target") // optional: specific Redfish target name

	if !s.vm.enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"points": []any{}})
		return
	}

	// Build PromQL query based on metric
	var promql string
	switch metric {
	case "temperature":
		promql = fmt.Sprintf(`aiops_hardware_temperature{host="%s"`, hostID)
	case "power":
		promql = fmt.Sprintf(`aiops_hardware_power_watts{host="%s"`, hostID)
	case "fan_rpm":
		promql = fmt.Sprintf(`aiops_hardware_fan_rpm{host="%s"`, hostID)
	case "health_score":
		promql = fmt.Sprintf(`aiops_hardware_health_score{host="%s"`, hostID)
	default:
		promql = fmt.Sprintf(`aiops_hardware_temperature{host="%s"`, hostID)
	}
	if target != "" {
		promql += fmt.Sprintf(`, target="%s"`, target)
	}
	promql += "}"

	points := s.vm.queryRawRange(promql, from, to)
	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}

// handleNetFlowSummary returns Top-N aggregated flow data.
// stripMask 去掉 IP 的 /掩码 后缀（PG inet::text 会带 /32），得到可供 net.ParseIP / 反查 / SNI 用的纯 IP。
func stripMask(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

func (s *Server) handleNetFlowSummary(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	rangeStr := r.URL.Query().Get("range")
	dimension := r.URL.Query().Get("dimension") // src_ip, dst_ip, src_port, dst_port, protocol
	topN := 20
	if n, err := strconv.Atoi(r.URL.Query().Get("top")); err == nil && n > 0 && n <= 100 {
		topN = n
	}

	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}

	from, to := parseTimeRange(rangeStr)
	if dimension == "" {
		dimension = "dst_ip"
	}

	// 数据源从 VM 改为 PG：VM 里不再保留 src_port/五元组这类高基数 label
	// （那正是压垮时序库的原因），明细在 flow_records 里永久保留，
	// 任意维度的 Top-N 交给关系库做，又准又不炸基数。
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"summary": []any{}})
		return
	}
	summary, err := s.pg.getFlowSummary(hostID, dimension, from, to, topN)
	if err != nil {
		slog.Warn("查询 Flow 汇总失败", "host", hostID, "dimension", dimension, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	// 维度为 IP 时富化每项排行的 key（把裸 IP 补域名/归属），让「流量排行」直接可读。
	if (dimension == "dst_ip" || dimension == "src_ip") && !s.cfg.Get().FlowEnrichDisabled && len(summary) > 0 {
		ips := make([]string, 0, len(summary))
		for _, it := range summary {
			if k, _ := it["key"].(string); k != "" {
				it["key"] = stripMask(k) // 去 /32 掩码：既让富化能解析，也让排行里的 IP 更干净
				ips = append(ips, stripMask(k))
			}
		}
		en := flowEnrich.enrichMany(ips, 600*time.Millisecond) // 内联只等已缓存/快解析的，避免整页卡 3s；未解析的由下方后台预热补
		for _, it := range summary {
			if k, _ := it["key"].(string); k != "" {
				e := en[k]
				if dom, _, ok := dnsObservations.lookup(hostID, k); ok {
					e.Host = dom
				}
				if e.hasData() {
					it["enrich"] = e
				}
			}
		}
		go flowEnrich.enrichMany(ips, 20*time.Second) // 后台预热
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary, "dimension": dimension})
}

// handleNetFlowIPHistory returns time-bucketed curves plus drill-down rankings
// for a clicked IP in the traffic ranking/detail table.
func (s *Server) handleNetFlowIPHistory(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	ip := stripMask(r.URL.Query().Get("ip"))
	dimension := r.URL.Query().Get("dimension")
	if hostID == "" || net.ParseIP(ip) == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid host and ip required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	if dimension != "src_ip" && dimension != "dst_ip" {
		dimension = "dst_ip"
	}
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"points": []any{}})
		return
	}
	from, to := requestTimeRange(r)
	data, err := s.pg.getFlowIPHistory(hostID, dimension, ip, from, to)
	if err != nil {
		slog.Warn("查询 IP 流量历史失败", "host", hostID, "ip", ip, "dimension", dimension, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	data["host"] = hostID
	data["ip"] = ip
	data["dimension"] = dimension
	data["from"] = from
	data["to"] = to

	// Enrich peer IPs so the drill-down answers “communicating with whom”.
	if peers, ok := data["peers"].([]map[string]any); ok && len(peers) > 0 && !s.cfg.Get().FlowEnrichDisabled {
		ips := make([]string, 0, len(peers))
		for _, row := range peers {
			if key, _ := row["key"].(string); key != "" {
				ips = append(ips, key)
			}
		}
		enriched := flowEnrich.enrichMany(ips, 600*time.Millisecond)
		for _, row := range peers {
			key, _ := row["key"].(string)
			e := enriched[key]
			if dom, _, found := dnsObservations.lookup(hostID, key); found {
				e.Host = dom
			}
			if e.hasData() {
				row["enrich"] = e
			}
		}
		go flowEnrich.enrichMany(ips, 20*time.Second)
	}
	writeJSON(w, http.StatusOK, data)
}

// handleNetFlowHosts returns only the hosts that actually have flow data in the
// window, ranked by traffic volume. The frontend uses this to filter the host
// selector to "hosts with traffic" (large first), hiding idle hosts.
func (s *Server) handleNetFlowHosts(w http.ResponseWriter, r *http.Request) {
	from, to := parseTimeRange(r.URL.Query().Get("range"))
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"hosts": []any{}})
		return
	}
	hosts, err := s.pg.getFlowHosts(from, to, 200)
	if err != nil {
		slog.Warn("查询有流量主机失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	// 这是个聚合列表（"最近有流量的主机"），没有 host 参数可查，所以在这里按授权裁剪：
	// 否则受限账号虽然点不开明细，却能从这份列表拿到全平台的主机 ID 和流量规模。
	if allow := s.hostLogVisibility(r); allow != nil {
		kept := hosts[:0]
		for _, h := range hosts {
			if allow(fmt.Sprint(h["host_id"])) {
				kept = append(kept, h)
			}
		}
		hosts = kept
	}
	s.annotateHostNames(hosts)
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts})
}

// handleNetFlowFlows returns flow detail records from PG.
func (s *Server) handleNetFlowFlows(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}

	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"flows": []any{}})
		return
	}

	limit := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	filter := r.URL.Query().Get("filter") // e.g. "src_ip:10.0.0.0/8"
	from, to := int64(0), int64(0)
	q := r.URL.Query()
	if q.Get("range") != "" || q.Get("from") != "" || q.Get("to") != "" {
		from, to = requestTimeRange(r)
	}

	flows, err := s.pg.getFlowRecordsRange(hostID, filter, limit, from, to)
	if err != nil {
		slog.Warn("查询 Flow 明细失败", "host", hostID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	// 目的地富化：把裸 IP 补上「域名 + 归属组织(ASN) + 国家」，让"IP 在访问什么"可读。
	// 惰性 + 缓存，首次稍慢、之后秒回；内网/保留地址不富化。可用 flow_enrich_disabled 关闭。
	if !s.cfg.Get().FlowEnrichDisabled && len(flows) > 0 {
		// 关键修复：PG inet::text 带 /32 掩码（如 192.168.30.15/32），net.ParseIP 无法解析 → 富化/PTR/SNI
		// 全部落空、域名永不显示。这里先去掉掩码再富化，同时把明细里的 IP 也规整为纯 IP（更干净）。
		ips := make([]string, 0, len(flows)*2)
		for _, f := range flows {
			if v, _ := f["dst_ip"].(string); v != "" {
				f["dst_ip"] = stripMask(v)
				ips = append(ips, stripMask(v))
			}
			if v, _ := f["src_ip"].(string); v != "" {
				f["src_ip"] = stripMask(v)
				ips = append(ips, stripMask(v))
			}
		}
		en := flowEnrich.enrichMany(ips, 600*time.Millisecond) // 内联只等已缓存/快解析的，避免整页卡 3s；未解析的由下方后台预热补
		for _, f := range flows {
			if v, _ := f["dst_ip"].(string); v != "" {
				e := en[v]
				// agent 观测到的真实域名(SNI/DNS)优先于反向 PTR——那是用户实际请求的域名。
				if dom, _, ok := dnsObservations.lookup(hostID, v); ok {
					e.Host = dom
				}
				if e.hasData() {
					f["dst_enrich"] = e
				}
			}
			if v, _ := f["src_ip"].(string); v != "" {
				e := en[v]
				if dom, _, ok := dnsObservations.lookup(hostID, v); ok {
					e.Host = dom
				}
				if e.hasData() {
					f["src_enrich"] = e
				}
			}
		}
		// 后台预热缓存：本次 3s 内没解析完的 IP 继续在后台解析入缓存，下次刷新即可显示域名。
		go flowEnrich.enrichMany(ips, 20*time.Second)
	}
	writeJSON(w, http.StatusOK, map[string]any{"flows": flows})
}

// handleNetFlowPackets returns packet capture statistics.
func (s *Server) handleNetFlowPackets(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")
	rangeStr := r.URL.Query().Get("range")

	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}

	from, to := parseTimeRange(rangeStr)

	if !s.vm.enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"points": []any{}})
		return
	}

	promql := fmt.Sprintf(`aiops_netflow_packets{host="%s", source="packet"}`, hostID)
	points := s.vm.queryRawRange(promql, from, to)
	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}

// ============================================================================
// VM write helpers
// ============================================================================

// hardwareHealthScore maps a Redfish health string to a numeric series value.
// ok=false for empty/unknown health so the caller can skip writing the metric
// instead of silently reporting 0 (= Critical).
func hardwareHealthScore(h string) (float64, bool) {
	switch h {
	case "OK":
		return 2, true
	case "Warning":
		return 1, true
	case "Critical":
		return 0, true
	}
	return 0, false
}

func (s *Server) vmHardwareMetrics(hostID string, snap shared.HardwareSnapshot) {
	if !s.vm.enabled() {
		return
	}
	// Prometheus 导入格式的时间戳单位是**毫秒**（见 vm.go 主采样管道的 `ms := s.ts * 1000`）。
	// snap.Timestamp 是 Unix 秒，此前直接透传 → 1.7e9 被当成毫秒 = 1970-01-21，
	// 于是硬件/NetFlow 历史全写进 1970，查最近 24h 永远查不到点 → 历史曲线一直空。
	ts := snap.Timestamp * 1000
	target := snap.TargetName

	// Health score: OK=2 / Warning=1 / Critical=0。Health 为空或未知（如采集失败）时
	// **不写该指标**——否则 var score 的零值 0 会被当成 Critical，误报一条“严重”。
	// 传感器数据仍照常写入，不受影响。
	if score, ok := hardwareHealthScore(snap.Health); ok {
		s.vm.pushHardware(hostID, target, ts, "aiops_hardware_health_score", score)
	}

	// Temperature sensors
	for _, t := range snap.Temps {
		s.vm.pushHardwareLabeled(hostID, target, ts, "aiops_hardware_temperature",
			t.Reading, "sensor", t.Name)
	}

	// Fan RPM
	for _, f := range snap.Fans {
		s.vm.pushHardwareLabeled(hostID, target, ts, "aiops_hardware_fan_rpm",
			float64(f.RPM), "fan_name", f.Name)
	}

	// Power watts
	if snap.Power.TotalWatts > 0 {
		s.vm.pushHardware(hostID, target, ts, "aiops_hardware_power_watts", snap.Power.TotalWatts)
	}
}

// netflowTopN bounds how many peer / service-port series each report may emit.
// 时间序列数据库的成本由**序列数**决定，不是采样点数。这里必须硬性封顶。
const netflowTopN = 50

// vmNetFlowMetrics writes AGGREGATED flow metrics to VM.
//
// 此前是每条 flow 一条序列，label 里带 src_ip/src_port/dst_ip/dst_port：
//
//	aiops_netflow_bytes{host,src_ip,dst_ip,src_port,dst_port,proto,source}
//
// src_port 是**临时端口**（每条连接随机），等于每条 flow 都开一条新序列。
// 采集上限是 10000 flows/s，哪怕只跑到 100 flows/s 也是每天 860 万条新序列 ——
// VM 撑不住百万级以上的活跃序列，几天就被拖垮，而"永久保留"只会让它死得更快。
//
// 改为三类**基数可控**的聚合序列：总量 / 对端 Top-N / 服务端口 Top-N。
// 五元组明细不进 VM，落 PG（分区表，永久保留）供取证回溯。
func (s *Server) vmNetFlowMetrics(hostID string, rep shared.NetFlowReport) {
	if !s.vm.enabled() {
		return
	}
	// 本机 IP 用来判定"对端"是谁；查不到就退化成按 dst 侧统计（仍然可用）。
	selfIP := ""
	if h := s.hostByID(hostID); h != nil {
		selfIP = h.IP
	}
	for _, line := range rollupNetFlow(hostID, selfIP, rep) {
		s.vm.pushRawLine(line)
	}
}

// rollupNetFlow turns one report into a BOUNDED set of Prometheus lines.
// 抽成纯函数是为了能直接对"产出多少条序列"做断言——基数封顶是这段代码存在的唯一理由。
func rollupNetFlow(hostID, selfIP string, rep shared.NetFlowReport) []string {
	var out []string
	ts := rep.Timestamp * 1000 // Prometheus 导入格式要求毫秒；此前传秒导致数据写进 1970
	src := lblEsc(rep.Source)

	var total flowAgg
	byPeer := map[string]*flowAgg{}
	byPort := map[string]*flowAgg{}

	for _, f := range rep.Flows {
		total.bytes += f.Bytes
		total.packets += f.Packets

		peer := f.DstIP
		if selfIP != "" && f.DstIP == selfIP {
			peer = f.SrcIP // 入向流量，对端是源
		}
		a := byPeer[peer]
		if a == nil {
			a = &flowAgg{}
			byPeer[peer] = a
		}
		a.bytes += f.Bytes
		a.packets += f.Packets

		// 服务端口：取两端里**较小**的那个，临时端口一定是大的那个，
		// 这样 80/443/3306 这类真正有意义的服务端口才会被统计到。
		svc := f.DstPort
		if f.SrcPort < f.DstPort && f.SrcPort != 0 {
			svc = f.SrcPort
		}
		k := fmt.Sprintf("%d/%d", svc, f.Protocol)
		b := byPort[k]
		if b == nil {
			b = &flowAgg{}
			byPort[k] = b
		}
		b.bytes += f.Bytes
		b.packets += f.Packets
	}

	if total.bytes > 0 || total.packets > 0 {
		out = append(out,
			fmt.Sprintf(`aiops_netflow_total_bytes{host="%s",source="%s"} %d %d`, hostID, src, total.bytes, ts),
			fmt.Sprintf(`aiops_netflow_total_packets{host="%s",source="%s"} %d %d`, hostID, src, total.packets, ts),
			fmt.Sprintf(`aiops_netflow_flows{host="%s",source="%s"} %d %d`, hostID, src, len(rep.Flows), ts))
	}

	for _, kv := range topAggs(byPeer, netflowTopN) {
		out = append(out, fmt.Sprintf(`aiops_netflow_peer_bytes{host="%s",peer="%s",source="%s"} %d %d`,
			hostID, lblEsc(kv.key), src, kv.val.bytes, ts))
	}
	for _, kv := range topAggs(byPort, netflowTopN) {
		port, proto, _ := strings.Cut(kv.key, "/")
		out = append(out, fmt.Sprintf(`aiops_netflow_port_bytes{host="%s",port="%s",proto="%s",source="%s"} %d %d`,
			hostID, port, proto, src, kv.val.bytes, ts))
	}

	if rep.Stats.DroppedPackets > 0 {
		out = append(out, fmt.Sprintf(`aiops_netflow_dropped{host="%s"} %d %d`,
			hostID, rep.Stats.DroppedPackets, ts))
	}
	return out
}

type flowAgg struct{ bytes, packets uint64 }

type flowAggKV struct {
	key string
	val *flowAgg
}

// topAggs returns the n entries with the highest byte count, sorted descending.
// 只发 Top-N 是基数封顶的关键：一台机器一轮最多贡献 n 条序列，
// 而不是"有多少个对端就开多少条"。
func topAggs(m map[string]*flowAgg, n int) []flowAggKV {
	out := make([]flowAggKV, 0, len(m))
	for k, v := range m {
		out = append(out, flowAggKV{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].val.bytes != out[j].val.bytes {
			return out[i].val.bytes > out[j].val.bytes
		}
		return out[i].key < out[j].key // 同流量时按 key 排，保证输出稳定
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// ============================================================================
// Helpers
// ============================================================================

func parseTimeRange(rangeStr string) (from, to int64) {
	to = time.Now().Unix()
	from = to - 86400 // default 24h
	if rangeStr == "" {
		return
	}
	rangeStr = strings.TrimSpace(rangeStr)
	if strings.HasSuffix(rangeStr, "h") {
		if h, err := strconv.Atoi(strings.TrimSuffix(rangeStr, "h")); err == nil {
			from = to - int64(h)*3600
		}
	} else if strings.HasSuffix(rangeStr, "d") {
		if d, err := strconv.Atoi(strings.TrimSuffix(rangeStr, "d")); err == nil {
			from = to - int64(d)*86400
		}
	} else if strings.HasSuffix(rangeStr, "m") {
		if m, err := strconv.Atoi(strings.TrimSuffix(rangeStr, "m")); err == nil {
			from = to - int64(m)*60
		}
	}
	return
}
