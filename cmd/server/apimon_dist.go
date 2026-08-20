package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// ============================================================================
// 分布式多点探测（迭代 D）
//
// 标记为「分布式」的 API 接口，由各地 agent 作为探针执行 HTTP 探测并回报结果。服务端
// 按 agent(host) 归为多个探测点，聚合出：某接口在「部分地域」失败(区域性故障) 还是
// 「全部地域」失败(全局故障)——这是服务端单点探测看不到的视角。任务经上报响应下发，
// 结果经 /api/v1/agent/probe-results 回报。独特优势：复用已有 agent 网络，免额外部署探针。
// ============================================================================

// distResult 是某接口在某探测点(agent)的最新结果。
type distResult struct {
	HostID    string  `json:"host_id"`
	Hostname  string  `json:"hostname"`
	OK        bool    `json:"ok"`
	LatencyMs float64 `json:"latency_ms"`
	Code      int     `json:"code"`
	Msg       string  `json:"msg,omitempty"`
	Ts        int64   `json:"ts"`
}

// distAgg 是某接口跨探测点的聚合。
type distAgg struct {
	TaskID  string       `json:"task_id"`
	Name    string       `json:"name"`
	Total   int          `json:"total"`    // 有效探测点数
	OKCount int          `json:"ok_count"` // 正常探测点数
	Scope   string       `json:"scope"`    // ok | regional | global
	Points  []distResult `json:"points"`   // 各探测点明细
}

// distScope 判定故障范围：全部点失败=global，部分失败=regional，否则=ok。
func distScope(total, okCount int) string {
	if total == 0 {
		return "ok"
	}
	if okCount == 0 {
		return "global"
	}
	if okCount < total {
		return "regional"
	}
	return "ok"
}

// distProbeManager 归集各 agent 回报的探测结果并聚合。
type distProbeManager struct {
	mu      sync.Mutex
	results map[string]map[string]distResult // taskID -> hostID -> 最新结果
	scope   map[string]string                // taskID -> 上次告警的范围，用于转换检测
	ttlSec  int64                            // 探测点过期时间（超过则不计入聚合）
}

func newDistProbeManager() *distProbeManager {
	return &distProbeManager{results: map[string]map[string]distResult{}, scope: map[string]string{}, ttlSec: 600}
}

// ingest 归集一个 agent 回报的一批结果。
func (m *distProbeManager) ingest(hostID, hostname string, results []shared.ProbeResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range results {
		byHost := m.results[r.TaskID]
		if byHost == nil {
			byHost = map[string]distResult{}
			m.results[r.TaskID] = byHost
		}
		byHost[hostID] = distResult{HostID: hostID, Hostname: hostname, OK: r.OK, LatencyMs: r.LatencyMs, Code: r.Code, Msg: r.Msg, Ts: r.Ts}
	}
}

// aggregate 计算某接口的跨探测点聚合（剔除过期点）。
func (m *distProbeManager) aggregate(taskID string, now int64) distAgg {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg := distAgg{TaskID: taskID, Scope: "ok"}
	for _, r := range m.results[taskID] {
		if now-r.Ts > m.ttlSec {
			continue // 过期探测点不计入
		}
		agg.Total++
		if r.OK {
			agg.OKCount++
		}
		agg.Points = append(agg.Points, r)
	}
	sort.Slice(agg.Points, func(i, j int) bool { return agg.Points[i].Hostname < agg.Points[j].Hostname })
	agg.Scope = distScope(agg.Total, agg.OKCount)
	return agg
}

// scopeTransition 记录并判定范围是否变化（用于「转换才告警」，避免重复推送）。
func (m *distProbeManager) scopeTransition(taskID, scope string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scope[taskID] == scope {
		return false
	}
	m.scope[taskID] = scope
	return true
}

// ---- Server：任务下发 / 结果归集 / 告警 / 看板 ----

// distProbeTasks 构建当前所有「分布式」接口的探测任务（下发给 agent）。
func (s *Server) distProbeTasks() []shared.ProbeTask {
	var tasks []shared.ProbeTask
	for _, sys := range s.cfg.APISystems() {
		if !sys.Enabled {
			continue
		}
		for _, ep := range sys.Endpoints {
			if !ep.Enabled || !ep.Distributed || ep.URL == "" {
				continue
			}
			to := ep.TimeoutSec
			if to <= 0 {
				to = 10
			}
			headers := map[string]string{}
			for k, v := range sys.CommonHeaders {
				headers[k] = v
			}
			for k, v := range ep.Headers {
				headers[k] = v
			}
			tasks = append(tasks, shared.ProbeTask{
				ID: ep.ID, Name: sys.Name + " / " + ep.Name, URL: ep.URL, Method: ep.Method,
				Headers: headers, Body: ep.Body, ExpectStatus: ep.ExpectStatus, TimeoutSec: to,
			})
		}
	}
	return tasks
}

// handleProbeResults 接收 agent 回报的分布式探测结果，归集并按范围(区域性/全局)转换告警。
func (s *Server) handleProbeResults(w http.ResponseWriter, r *http.Request) {
	var rep shared.ProbeResultReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if rep.HostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.host_required")})
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
	s.distProbes.ingest(rep.HostID, rep.Hostname, rep.Results)
	now := time.Now().Unix()
	seen := map[string]bool{}
	for _, res := range rep.Results {
		if seen[res.TaskID] {
			continue
		}
		seen[res.TaskID] = true
		agg := s.distProbes.aggregate(res.TaskID, now)
		if s.distProbes.scopeTransition(res.TaskID, agg.Scope) {
			s.distAlert(res.TaskID, agg)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// distAlert 按聚合范围推送告警（global=紧急 / regional=警告 / ok=恢复），走 pushChannels（已含治理）。
func (s *Server) distAlert(taskID string, agg distAgg) {
	name := s.apiEndpointName(taskID)
	var level, msg string
	switch agg.Scope {
	case "global":
		level = "critical"
		msg = fmt.Sprintf("分布式探测·全局故障：接口「%s」在全部 %d 个探测点均失败", name, agg.Total)
	case "regional":
		level = "warning"
		msg = fmt.Sprintf("分布式探测·区域性故障：接口「%s」在 %d/%d 个探测点失败（部分地域不可达）", name, agg.Total-agg.OKCount, agg.Total)
	default:
		level = "info"
		msg = fmt.Sprintf("分布式探测已恢复：接口「%s」全部 %d 个探测点正常", name, agg.Total)
	}
	a := Alert{Level: level, Type: "api_dist", Scope: taskID, Hostname: name, Message: msg, Timestamp: time.Now().Unix()}
	s.store.AddLog(LogEntry{Kind: KindSystem, Level: level, Actor: "分布式探测", Host: name, Message: msg})
	if s.endpointInMaintenance(taskID) {
		return // 维护窗口内抑制分布式告警推送
	}
	if cfg := s.cfg.Get(); cfg.AlertsEnabled {
		s.notifier.pushChannels(cfg, a, agg.Scope != "ok")
	}
}

// endpointInMaintenance 判断某接口所属业务系统当前是否处于维护窗口（窗口内抑制告警）。
func (s *Server) endpointInMaintenance(endpointID string) bool {
	now := time.Now().Unix()
	for _, sys := range s.cfg.APISystems() {
		if sys.MaintUntil <= now {
			continue
		}
		for _, ep := range sys.Endpoints {
			if ep.ID == endpointID {
				return true
			}
		}
	}
	return false
}

// apiEndpointName 由接口 ID 找展示名（系统 / 接口）。
func (s *Server) apiEndpointName(id string) string {
	for _, sys := range s.cfg.APISystems() {
		for _, ep := range sys.Endpoints {
			if ep.ID == id {
				return sys.Name + " / " + ep.Name
			}
		}
	}
	return id
}

// handleDistStatus 供看板：所有分布式接口的跨探测点聚合（含各点明细）。
func (s *Server) handleDistStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	out := []distAgg{}
	for _, t := range s.distProbeTasks() {
		agg := s.distProbes.aggregate(t.ID, now)
		agg.Name = t.Name
		out = append(out, agg)
	}
	writeJSON(w, http.StatusOK, map[string]any{"distributed": out})
}
