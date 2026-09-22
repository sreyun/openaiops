package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---- API 性能监控 HTTP 端点 ----

// handleAPIMonOverview 返回所有业务系统 + 接口，合并「实时状态」(内存)与「聚合指标」(VM 现算)，
// 供前端一张聚合表直接渲染（最新状态 / 平均·P95 响应时间 / 1h·24h 可用率 / 吞吐）。
func (s *Server) handleAPIMonOverview(w http.ResponseWriter, r *http.Request) {
	systems := s.cfg.APISystems()
	st := s.apimon.statusSnapshot()
	down := s.apimon.downSnapshot()
	var agg map[string]apiAggregate
	if s.vm != nil {
		agg = s.vm.queryAPIAggregate()
	}

	out := make([]map[string]any, 0, len(systems))
	for _, sys := range systems {
		eps := make([]map[string]any, 0, len(sys.Endpoints))
		for _, ep := range sys.Endpoints {
			m := map[string]any{
				"id": ep.ID, "name": ep.Name, "url": ep.URL, "method": ep.Method,
				"enabled": ep.Enabled, "headers": ep.Headers, "body": ep.Body,
				"expect_status": ep.ExpectStatus, "expect_keyword": ep.ExpectKeyword,
				"json_path": ep.JSONPath, "json_expect": ep.JSONExpect,
				"timeout_sec": ep.TimeoutSec, "retries": ep.Retries, "distributed": ep.Distributed, "protocol": ep.Protocol,
				// 实时状态（默认值 = 尚未探测）
				"ok": true, "message": "", "latency_ms": 0.0, "status_code": 0,
				"cert_days": -1, "resp_bytes": int64(0), "checked_at": int64(0),
				// VM 聚合（-1 = 暂无数据）
				"avg_ms": 0.0, "p95_ms": 0.0, "p99_ms": 0.0, "avail_1h": -1.0, "avail_24h": -1.0, "samples_1h": 0.0,
				"down": false, "down_since": int64(0),
			}
			if s2, ok := st[ep.ID]; ok {
				m["ok"], m["message"], m["latency_ms"], m["status_code"] = s2.OK, s2.Message, s2.LatencyMs, s2.StatusCode
				m["cert_days"], m["resp_bytes"], m["checked_at"] = s2.CertDays, s2.RespBytes, s2.CheckedAt
			}
			if a, ok := agg[ep.ID]; ok {
				m["avg_ms"], m["p95_ms"], m["samples_1h"] = a.AvgMs, a.P95Ms, a.Samples1h
				m["p99_ms"] = a.P99Ms
				m["avail_1h"], m["avail_24h"] = a.Avail1h, a.Avail24h
			}
			if ds, ok := down[ep.ID]; ok {
				m["down"], m["down_since"] = true, ds
			}
			eps = append(eps, m)
		}
		out = append(out, map[string]any{
			"id": sys.ID, "name": sys.Name, "interval_sec": sys.IntervalSec,
			"level": sys.Level, "env": sys.Env, "enabled": sys.Enabled, "created_at": sys.CreatedAt, "maint_until": sys.MaintUntil,
			"common_headers": sys.CommonHeaders, // 回显系统级公共请求头（此前遗漏→编辑时清空→保存被清零）
			"common_body":    sys.CommonBody,    // 回显系统级公共请求体（同理必须回显，否则编辑即清零）
			"host_ids":       sys.HostIDs,       // 回显承载主机关联
			"endpoints":      eps,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"systems": out})
}

// handleUpsertAPISystem 新增/更新一个业务系统（含其接口列表），保存后立即探测一次。
func (s *Server) handleUpsertAPISystem(w http.ResponseWriter, r *http.Request) {
	var sys APISystem
	if err := json.NewDecoder(r.Body).Decode(&sys); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	sys.Name = strings.TrimSpace(sys.Name)
	if sys.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "业务系统名称不能为空"})
		return
	}
	if sys.IntervalSec < 5 {
		sys.IntervalSec = 60
	}
	if sys.Level != "warning" && sys.Level != "critical" {
		sys.Level = "critical"
	}
	sys.Env = strings.ToLower(strings.TrimSpace(sys.Env)) // 环境标签规整（prod/staging/dev 或自定义）
	// 承载主机关联会经 /apimon/systems/{id}/hosts 回传 CPU/内存等实时指标——
	// 受限账号不能把范围外主机绑进来当指标旁路。
	if !s.requireHostIDsAccess(w, r, sys.HostIDs) {
		return
	}
	// 分布式探测任务会随每次 agent report 下发到**整支舰队**（含授权范围外主机），
	// 等同于让外机去访问探测 URL。主机范围受限的账号不得开启。
	if u, ok := s.currentUser(r); ok && u.hostScopeRestricted() && roleRank(u.Role) < roleRank(RoleAdmin) {
		for _, ep := range sys.Endpoints {
			if ep.Distributed {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": "主机范围受限账号不能启用分布式探测（会下发到授权范围外的 Agent）",
				})
				return
			}
		}
	}
	// 清洗接口：去空名/空 URL，规整方法
	cleaned := make([]APIEndpoint, 0, len(sys.Endpoints))
	for _, ep := range sys.Endpoints {
		ep.Name = strings.TrimSpace(ep.Name)
		ep.URL = strings.TrimSpace(ep.URL)
		if ep.Name == "" || ep.URL == "" {
			continue
		}
		ep.Method = strings.ToUpper(strings.TrimSpace(ep.Method))
		if ep.TimeoutSec <= 0 {
			ep.TimeoutSec = 10 // 默认 10s（比拨测全局 5s 宽松，适配业务接口）
		} else if ep.TimeoutSec > 60 {
			ep.TimeoutSec = 60
		}
		if ep.Retries < 0 {
			ep.Retries = 0
		} else if ep.Retries > 3 {
			ep.Retries = 3
		}
		cleaned = append(cleaned, ep)
	}
	sys.Endpoints = cleaned
	saved, err := s.cfg.UpsertAPISystem(sys)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.apimon.runNow(saved.ID)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Message: "保存 API 监控业务系统：" + saved.Name})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": saved.ID})
}

func (s *Server) handleDeleteAPISystem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, sys := range s.cfg.APISystems() {
		if sys.ID == id {
			if !s.requireHostIDsAccess(w, r, sys.HostIDs) {
				return
			}
			break
		}
	}
	_ = s.cfg.DeleteAPISystem(id)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Message: "删除 API 监控业务系统：" + id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAPISystemMaint 设置/解除业务系统维护窗口。{minutes:N} 静音 N 分钟；0 或缺省=解除。
// 窗口内探测照常进行（数据仍入 VM），仅抑制该系统的告警推送。
func (s *Server) handleAPISystemMaint(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Minutes int `json:"minutes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var until int64
	if req.Minutes > 0 {
		until = time.Now().Unix() + int64(req.Minutes)*60
	}
	if err := s.cfg.SetAPISystemMaint(id, until); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	action := "解除维护窗口"
	if until > 0 {
		action = "进入维护窗口 " + strconv.Itoa(req.Minutes) + " 分钟"
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Message: "API 业务系统" + action + "：" + id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "maint_until": until})
}

// handleRunAPISystem 立即探测某业务系统的全部接口（fire-and-forget）。
func (s *Server) handleRunAPISystem(w http.ResponseWriter, r *http.Request) {
	s.apimon.runNow(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAPIEndpointHistory 返回某接口从 VM 读取的历史序列（延迟/状态随时间）。
func (s *Server) handleAPIEndpointHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var pts []APIHistPoint
	if s.vm != nil && s.vm.enabled() {
		to := time.Now().Unix()
		from := to - 24*3600 // 默认最近 24h
		if m := r.URL.Query().Get("since_min"); m != "" {
			if v, _ := strconv.Atoi(m); v > 0 {
				from = to - int64(v)*60
			}
		}
		pts = s.vm.queryAPIHistory(id, from, to)
	}
	if pts == nil {
		pts = []APIHistPoint{}
	}
	writeJSON(w, http.StatusOK, pts)
}

// ---- API 合成事务（迭代 C）HTTP 端点 ----

// handleAPITxnOverview 返回所有合成事务 + 每个事务的最新执行结果（含分步结果）。
func (s *Server) handleAPITxnOverview(w http.ResponseWriter, r *http.Request) {
	txns := s.cfg.APITransactions()
	st := s.apimon.txnStatusSnapshot()
	out := make([]map[string]any, 0, len(txns))
	for _, t := range txns {
		m := map[string]any{
			"id": t.ID, "name": t.Name, "interval_sec": t.IntervalSec, "level": t.Level,
			"enabled": t.Enabled, "vars": t.Vars, "steps": t.Steps, "created_at": t.CreatedAt,
			// 最新执行结果（默认值 = 尚未执行）
			"ok": true, "failed_step": -1, "total_ms": 0.0, "checked_at": int64(0), "step_results": []any{},
		}
		if res, ok := st[t.ID]; ok {
			m["ok"], m["failed_step"], m["total_ms"], m["checked_at"] = res.OK, res.FailedStep, res.TotalMs, res.CheckedAt
			m["step_results"] = res.Steps
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": out})
}

// handleUpsertAPITransaction 新增/更新一个合成事务（含步骤列表），保存后立即执行一次。
func (s *Server) handleUpsertAPITransaction(w http.ResponseWriter, r *http.Request) {
	var t APITransaction
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "事务名称不能为空"})
		return
	}
	if t.IntervalSec < 5 {
		t.IntervalSec = 60
	}
	if t.Level != "warning" && t.Level != "critical" {
		t.Level = "critical"
	}
	cleaned := make([]APIStep, 0, len(t.Steps))
	for _, step := range t.Steps {
		step.Name = strings.TrimSpace(step.Name)
		step.URL = strings.TrimSpace(step.URL)
		if step.Name == "" || step.URL == "" {
			continue
		}
		step.Method = strings.ToUpper(strings.TrimSpace(step.Method))
		if step.TimeoutSec <= 0 {
			step.TimeoutSec = 10
		} else if step.TimeoutSec > 60 {
			step.TimeoutSec = 60
		}
		cleaned = append(cleaned, step)
	}
	if len(cleaned) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请至少添加一个步骤（需填步骤名与 URL）"})
		return
	}
	t.Steps = cleaned
	saved, err := s.cfg.UpsertAPITransaction(t)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.apimon.runTxnNow(saved.ID)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Message: "保存 API 合成事务：" + saved.Name})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": saved.ID})
}

func (s *Server) handleDeleteAPITransaction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.cfg.DeleteAPITransaction(id)
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning", Message: "删除 API 合成事务：" + id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRunAPITransaction 立即执行某合成事务（fire-and-forget）。
func (s *Server) handleRunAPITransaction(w http.ResponseWriter, r *http.Request) {
	s.apimon.runTxnNow(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSLAReport 生成 SLA 报表：各接口近 N 天(默认30，上限90)的可用率 / 估算停机 / P95 / P99 /
// 探测数，从 VM 现算。供前端表格展示与 CSV 导出（面向对外 SLA 承诺与月度汇报）。
func (s *Server) handleSLAReport(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if v, _ := strconv.Atoi(d); v > 0 && v <= 90 {
			days = v
		}
	}
	rows := []map[string]any{}
	if s.vm != nil && s.vm.enabled() {
		win := strconv.Itoa(days) + "d"
		avail := s.vm.vmInstantByAPI(`avg_over_time(aiops_api_up[` + win + `]) * 100`)
		cnt := s.vm.vmInstantByAPI(`count_over_time(aiops_api_up[` + win + `])`)
		p95 := s.vm.vmInstantByAPI(`quantile_over_time(0.95, aiops_api_latency_ms[` + win + `])`)
		p99 := s.vm.vmInstantByAPI(`quantile_over_time(0.99, aiops_api_latency_ms[` + win + `])`)
		for _, sys := range s.cfg.APISystems() {
			for _, ep := range sys.Endpoints {
				a, ok := avail[ep.ID]
				if !ok {
					continue // 无数据的接口不列入报表
				}
				rows = append(rows, map[string]any{
					"system": sys.Name, "endpoint": ep.Name, "url": ep.URL,
					"availability": a, "samples": cnt[ep.ID],
					"p95_ms": p95[ep.ID], "p99_ms": p99[ep.ID],
					"downtime_min": (100 - a) / 100 * float64(days) * 24 * 60, // 估算停机 = 不可用比例 × 窗口
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "rows": rows})
}

// handleAPISystemHosts 返回某业务系统「承载主机」的实时指标快照，供异常下钻——把接口异常与承载
// 主机的 CPU/内存/磁盘/网络关联。主机由 APISystem.HostIDs 显式关联（编辑业务系统时勾选）。
func (s *Server) handleAPISystemHosts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var hostIDs []string
	for _, sys := range s.cfg.APISystems() {
		if sys.ID == id {
			hostIDs = sys.HostIDs
			break
		}
	}
	if !s.requireHostIDsAccess(w, r, hostIDs) {
		return
	}
	now := time.Now().Unix()
	out := []map[string]any{}
	for _, hid := range hostIDs {
		h, ok := s.store.GetHost(hid)
		if !ok {
			continue
		}
		m := map[string]any{
			"id": h.ID, "hostname": h.Hostname, "ip": h.IP,
			"last_seen": h.LastSeen, "online": now-h.LastSeen < 180,
			"cpu": 0.0, "mem": 0.0, "disk": 0.0, "load1": 0.0, "net_recv": 0.0, "net_sent": 0.0,
		}
		if h.Latest != nil {
			m["cpu"], m["mem"], m["disk"] = h.Latest.CPUPercent, h.Latest.MemPercent, h.Latest.DiskPercent
			m["load1"] = h.Latest.Load1
			m["net_recv"], m["net_sent"] = h.Latest.NetRecvRate, h.Latest.NetSentRate
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}
