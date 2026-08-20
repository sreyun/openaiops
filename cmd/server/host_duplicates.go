package main

import (
	"net/http"
	"sort"
	"time"
)

// ---------------------------------------------------------------------------
// 重复主机识别与清理
//
// 成因：Agent 的 host_id 是**随机生成**后存在 agent_state.json 里的。卸载重装会
// 连状态文件一起删掉 → 新的随机 id → 服务端当成一台全新主机注册，于是同一台物理
// 机出现两条记录（同名、同 IP），老的那条永远留着，把硬件/流量面板搅乱。
//
// 判据用**机器指纹**（machine-id + 主 MAC，注册时绑定）而不是主机名：
// 主机名可以重名（多台机器都叫 localhost），指纹跨重装稳定且唯一。
//
// 安全：指纹是 Agent 反向通道（终端/上报/转发）的**唯一凭据**，绝不能下发给浏览器。
// 这里只用它在服务端分组，对外暴露的是序号，不带任何指纹派生数据。
// ---------------------------------------------------------------------------

type dupHostView struct {
	ID        string `json:"id"`
	Hostname  string `json:"hostname"`
	IP        string `json:"ip"`
	Online    bool   `json:"online"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	// Current 表示这是该物理机**当前在用**的身份（最近一次上报的那条）。
	// 重装后新 id 会是 current，老 id 变成待清理。
	Current bool `json:"current"`
	// Stale 表示可安全清理：非当前身份 且 已离线。
	Stale bool `json:"stale"`
}

type dupGroupView struct {
	Group    int           `json:"group"`
	Hostname string        `json:"hostname"`
	Hosts    []dupHostView `json:"hosts"`
	Stale    int           `json:"stale"`
}

// findDuplicateHosts groups hosts by machine fingerprint, returning only groups
// that actually contain more than one record.
func (s *Server) findDuplicateHosts() []dupGroupView {
	hosts := s.store.ListHosts()
	now := time.Now().Unix()
	offlineAfter := int64(s.cfg.Thresholds().OfflineAfter.Seconds())

	byFP := map[string][]*Host{}
	for _, h := range hosts {
		if h.Fingerprint == "" {
			continue // 没绑定指纹就无法可靠判定，宁可不报也不要误删
		}
		byFP[h.Fingerprint] = append(byFP[h.Fingerprint], h)
	}

	var groups []dupGroupView
	for _, hs := range byFP {
		if len(hs) < 2 {
			continue
		}
		// 最近上报的那条 = 当前身份
		sort.Slice(hs, func(i, j int) bool { return hs[i].LastSeen > hs[j].LastSeen })
		g := dupGroupView{Group: len(groups) + 1, Hostname: hs[0].Hostname}
		for i, h := range hs {
			online := now-h.LastSeen <= offlineAfter
			isCur := i == 0
			v := dupHostView{
				ID: h.ID, Hostname: h.Hostname, IP: h.IP, Online: online,
				FirstSeen: h.FirstSeen, LastSeen: h.LastSeen, Current: isCur,
				// 只有"非当前 且 已离线"才算可清理：万一两条都还在上报（真是两台机器
				// 共用了克隆的状态文件），谁都不该被自动删掉。
				Stale: !isCur && !online,
			}
			if v.Stale {
				g.Stale++
			}
			g.Hosts = append(g.Hosts, v)
		}
		groups = append(groups, g)
	}
	// 可清理的排前面，其次按主机名，输出稳定
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Stale != groups[j].Stale {
			return groups[i].Stale > groups[j].Stale
		}
		return groups[i].Hostname < groups[j].Hostname
	})
	for i := range groups {
		groups[i].Group = i + 1
	}
	return groups
}

// handleHostDuplicates lists duplicate host records (same machine, different id).
func (s *Server) handleHostDuplicates(w http.ResponseWriter, r *http.Request) {
	groups := s.findDuplicateHosts()
	stale := 0
	for _, g := range groups {
		stale += g.Stale
	}
	if groups == nil {
		groups = []dupGroupView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "stale_total": stale})
}

// handleCleanupDuplicates deletes stale duplicate host records.
//
// 只删"同指纹 + 非当前身份 + 已离线"的记录 —— 三个条件同时满足才动手。
// 这是**不可逆**操作，所以判据必须严到不可能误伤：正在上报的主机、
// 没有指纹的主机、组里唯一的主机，一律不碰。
func (s *Server) handleCleanupDuplicates(w http.ResponseWriter, r *http.Request) {
	groups := s.findDuplicateHosts()
	var deleted []string
	for _, g := range groups {
		for _, h := range g.Hosts {
			if !h.Stale {
				continue
			}
			if s.store.DeleteHost(h.ID) {
				_ = s.cfg.forgetHost(h.ID)
				s.removeHyperVForHost(h.ID)
				deleted = append(deleted, h.ID)
			}
		}
	}
	if len(deleted) > 0 {
		s.store.AddLog(LogEntry{
			Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
			Message: Tz("log.cleanup_duplicate_hosts", len(deleted)),
		})
	}
	if deleted == nil {
		deleted = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "count": len(deleted)})
}
