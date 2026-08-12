package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
)

// ============================================================
// Port Forwarding API — Refactored Interface Layer
//
// This file defines the clean API contract for TCP and HTTP
// forwarding. It separates request/response types from the
// core forward.go implementation, making the interface easy
// to understand, document, and maintain.
// ============================================================

// --- TCP Forwarding API ---

// TCPForwardRequest creates a persistent TCP port mapping.
//
// Example (curl):
//
//	curl -X POST http://localhost:8529/api/v1/forward \
//	  -H "Content-Type: application/json" \
//	  -H "Cookie: aiops_session=<your-session>" \
//	  -d '{"host_id":"abc123","target_port":3306,"local_port":13306}'
//
// This opens 0.0.0.0:13306 on the server (configurable via forward_listen),
// relaying all TCP traffic through the agent to localhost:3306 on the target host.
// Use any MySQL client to connect:
//
//	mysql -h 127.0.0.1 -P 13306 -u root -p
type TCPForwardRequest struct {
	HostID     string `json:"host_id"`              // Required: monitored host ID
	TargetPort int    `json:"target_port"`          // Required: port on the target host (1-65535)
	LocalPort  int    `json:"local_port,omitempty"` // Optional: local listen port (0 = auto-assign)
}

// TCPForwardResponse is returned when a TCP forward rule is created.
type TCPForwardResponse struct {
	ID         string `json:"id"` // Rule ID (use for deletion)
	HostID     string `json:"host_id"`
	Hostname   string `json:"hostname"`
	TargetPort int    `json:"target_port"`
	LocalPort  int    `json:"local_port"`
	ListenAddr string `json:"listen_addr"` // e.g. "0.0.0.0:13306"
	Status     string `json:"status"`      // always "active"
	CreatedAt  int64  `json:"created_at"`
	Operator   string `json:"operator"`
	Sessions   int    `json:"sessions"` // current active connections
}

// --- HTTP Forwarding API ---

// HTTPForwardUsage shows how to use the HTTP proxy endpoint.
//
// The HTTP proxy is stateless — no rule creation needed. Just
// send any HTTP request to the proxy URL:
//
//   GET  /proxy/{hostID}/{port}/{path}
//   POST /proxy/{hostID}/{port}/{path}
//   PUT  /proxy/{hostID}/{port}/{path}
//   DELETE /proxy/{hostID}/{port}/{path}
//
// Example (curl):
//   curl http://localhost:8529/proxy/abc123/8080/api/health
//   curl -X POST http://localhost:8529/proxy/abc123/3000/api/users \
//     -H "Content-Type: application/json" \
//     -d '{"name":"test"}'
//
// The proxy tunnels the request through the agent to
// localhost:{port} on the target host. WebSocket upgrades
// are also supported:
//   ws://localhost:8529/proxy/abc123/8080/ws

// ForwardStatsResponse exposes aggregate forwarding metrics.
type ForwardStatsResponse struct {
	ActiveSessions int   `json:"active_sessions"`
	TotalSessions  int64 `json:"total_sessions"`
	TotalBytes     int64 `json:"total_bytes"`
	Errors         int64 `json:"errors"`
	MaxSessions    int   `json:"max_sessions"`
}

// handleForwardStats returns aggregate forwarding statistics.
// GET /api/v1/forward/stats
func (s *Server) handleForwardStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ForwardStatsResponse{
		ActiveSessions: int(atomic.LoadInt64(&s.forward.stats.ActiveSessions)),
		TotalSessions:  atomic.LoadInt64(&s.forward.stats.TotalSessions),
		TotalBytes:     atomic.LoadInt64(&s.forward.stats.TotalBytes),
		Errors:         atomic.LoadInt64(&s.forward.stats.Errors),
		MaxSessions:    maxForwardSessions,
	})
}

// handleForwardGroupDelete 删除同一端口范围组的所有转发规则（一次删完，免逐条删除）。
// DELETE /api/v1/forward/group/{gid}
func (s *Server) handleForwardGroupDelete(w http.ResponseWriter, r *http.Request) {
	gid := r.PathValue("gid")
	if gid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	deleted := 0
	for _, id := range s.forward.groupRuleIDs(gid) {
		rule := s.forward.getRule(id)
		if rule == nil {
			continue
		}
		if !s.requireHostAccess(w, r, rule.hostID) {
			return
		}
		if s.forward.removeRule(id) {
			deleted++
		}
	}
	if deleted == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "forward.rule_not_found")})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: fmt.Sprintf("删除端口范围转发组 %s（%d 条）", gid, deleted)})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// handleForwardGroupToggle 整组启用 / 停用同一端口范围组的所有规则。
// PUT /api/v1/forward/group/{gid}/toggle
func (s *Server) handleForwardGroupToggle(w http.ResponseWriter, r *http.Request) {
	gid := r.PathValue("gid")
	if gid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	for _, id := range s.forward.groupRuleIDs(gid) {
		rule := s.forward.getRule(id)
		if rule == nil {
			continue
		}
		if !s.requireHostAccess(w, r, rule.hostID) {
			return
		}
	}
	toggled := 0
	for _, id := range s.forward.groupRuleIDs(gid) {
		rule, err := s.forward.toggleRule(id, req.Enabled)
		if err != nil {
			continue
		}
		if req.Enabled && rule.listener == nil && rule.packetConn == nil {
			_ = s.rebindRuleListener(rule) // 重新启用：按协议重建监听
		}
		toggled++
	}
	if toggled == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "forward.rule_not_found")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"toggled": toggled, "enabled": req.Enabled})
}

// handleForwardGroupCopy 复制同一端口范围组的所有转发规则（整组克隆）。
// POST /api/v1/forward/group/{gid}/copy
func (s *Server) handleForwardGroupCopy(w http.ResponseWriter, r *http.Request) {
	gid := r.PathValue("gid")
	if gid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	user, _ := s.currentUser(r)
	operator := s.clientIP(r)
	if user.Username != "" {
		operator = user.Username
	}
	listenHost := s.cfg.ForwardListenAddr()
	var created []forwardInfo
	for _, id := range s.forward.groupRuleIDs(gid) {
		orig := s.forward.getRule(id)
		if orig == nil {
			continue
		}
		if !s.requireHostAccess(w, r, orig.hostID) {
			return
		}
		wlOn, wlList, _ := orig.whitelistSnapshot()
		newRule, err := s.forward.createRule(orig.hostID, orig.hostname, orig.targetPort, 0, listenHost, orig.protocol, "", operator, orig.remoteTarget, wlOn, wlList)
		if err != nil {
			continue
		}
		go s.serveRule(newRule)
		created = append(created, infoFromRule(newRule, 0, 0))
	}
	if len(created) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "forward.rule_not_found")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"copied": len(created), "rules": created})
}

// handleForwardGroupEdit 批量编辑同一端口范围组的所有转发规则（host / target port / local port）。
// PUT /api/v1/forward/group/{gid}
func (s *Server) handleForwardGroupEdit(w http.ResponseWriter, r *http.Request) {
	gid := r.PathValue("gid")
	if gid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	var req struct {
		HostID     string `json:"host_id"`
		TargetPort int    `json:"target_port"`
		LocalPort  int    `json:"local_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	// 收集组内旧规则（目标端口 / 主机 / 协议），并求最小端口作为整段平移的基准。
	// 关键修复：此前把 req.TargetPort 直接套到组内每一条 → 整段塌成同一个端口、只剩一条能
	// 绑定；改为「按最小端口平移」，保留各端口的相对偏移（TCP / UDP 通用）。
	type oldRule struct {
		target           int
		hostID, hostname string
		protocol         string
		wlEnabled        bool
		whitelist        []string
	}
	var olds []oldRule
	oldStart := 0
	for _, id := range s.forward.groupRuleIDs(gid) {
		rule := s.forward.getRule(id)
		if rule == nil {
			continue
		}
		wlOn, wlList, _ := rule.whitelistSnapshot()
		olds = append(olds, oldRule{rule.targetPort, rule.hostID, rule.hostname, rule.protocol, wlOn, wlList})
		if oldStart == 0 || rule.targetPort < oldStart {
			oldStart = rule.targetPort
		}
	}
	if len(olds) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "forward.rule_not_found")})
		return
	}
	for _, o := range olds {
		if !s.requireHostAccess(w, r, o.hostID) {
			return
		}
	}
	// 可选：整组改到新主机。
	newHost, newHostname := "", ""
	if req.HostID != "" {
		if !s.requireHostAccess(w, r, req.HostID) {
			return
		}
		newHost = req.HostID
		newHostname = s.hostLabelForID(req.HostID)
	}
	newStart := req.TargetPort
	if newStart < 1 || newStart > 65535 {
		newStart = oldStart // 未改端口则不平移
	}
	delta := newStart - oldStart
	user, _ := s.currentUser(r)
	operator := s.clientIP(r)
	if user.Username != "" {
		operator = user.Username
	}
	// 先删旧规则（关掉全部旧监听），再按平移后的端口整段重建——避免就地改端口时新旧端口
	// 区间重叠导致的绑定冲突。沿用同一 group id，组关系不变；本地端口镜像目标端口。
	for _, id := range s.forward.groupRuleIDs(gid) {
		s.forward.removeRule(id)
	}
	listenHost := s.cfg.ForwardListenAddr()
	var created []forwardInfo
	for _, o := range olds {
		host, hostname := o.hostID, o.hostname
		if newHost != "" {
			host, hostname = newHost, newHostname
		}
		nt := o.target + delta
		if nt < 1 || nt > 65535 {
			continue
		}
		rule, err := s.forward.createRule(host, hostname, nt, nt, listenHost, o.protocol, gid, operator, "", o.wlEnabled, o.whitelist)
		if err != nil {
			continue
		}
		go s.serveRule(rule)
		created = append(created, infoFromRule(rule, 0, 0))
	}
	if len(created) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "整组编辑失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"edited": len(created), "rules": created})
}

// handleForwardHealth checks if forwarding is available.
// GET /api/v1/forward/health
func (s *Server) handleForwardHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     s.cfg.ForwardEnabled(),
		"max_body":    maxForwardBodySize,
		"max_session": maxForwardSessions,
	})
}

// --- HTTP Proxy Shortcuts API ---

// httpProxyInfo is the API view of an HTTP proxy shortcut, enriched with live
// connection counts (active + cumulative) tracked by the forward manager.
type httpProxyInfo struct {
	HTTPProxyConfig
	Sessions      int   `json:"sessions"`       // 当前活跃连接（请求）数
	TotalSessions int64 `json:"total_sessions"` // 累计总连接（请求）数
}

// handleHTTPProxyList returns all saved HTTP proxy configurations with live stats.
// GET /api/v1/http-proxy
func (s *Server) handleHTTPProxyList(w http.ResponseWriter, r *http.Request) {
	proxies := s.cfg.ListHTTPProxies()
	m := s.forward
	m.mu.Lock()
	out := make([]httpProxyInfo, 0, len(proxies))
	for _, p := range proxies {
		active, total := m.counts(sessionCounterKey("", p.HostID, p.TargetPort))
		out = append(out, httpProxyInfo{HTTPProxyConfig: p, Sessions: active, TotalSessions: total})
	}
	m.mu.Unlock()
	if u, ok := s.currentUser(r); ok && u.hostScopeRestricted() && roleRank(u.Role) < roleRank(RoleAdmin) {
		filtered := out[:0]
		for _, p := range out {
			if s.userCanAccessHost(u, p.HostID) {
				filtered = append(filtered, p)
			}
		}
		out = filtered
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHTTPProxyCreate creates a new HTTP proxy shortcut.
// POST /api/v1/http-proxy
func (s *Server) handleHTTPProxyCreate(w http.ResponseWriter, r *http.Request) {
	var req HTTPProxyConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if req.HostID == "" || req.TargetPort < 1 || req.TargetPort > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "forward.host_port_required")})
		return
	}
	if !s.requireHostAccess(w, r, req.HostID) {
		return
	}
	// Lookup hostname
	for _, h := range s.store.ListHosts() {
		if h.ID == req.HostID {
			req.Hostname = h.Hostname
			break
		}
	}
	user, _ := s.currentUser(r)
	req.Operator = user.Username
	if req.Operator == "" {
		req.Operator = s.clientIP(r)
	}
	if err := s.cfg.AddHTTPProxy(req); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Return the created proxy with ID
	proxies := s.cfg.ListHTTPProxies()
	for _, p := range proxies {
		if p.HostID == req.HostID && p.TargetPort == req.TargetPort {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	writeJSON(w, http.StatusOK, req)
}

// handleProxyToken generates a short-lived, single-use token for
// authenticating HTTP proxy requests opened via window.open().
// The token is returned as JSON AND set as a SameSite=Lax cookie so the
// new tab automatically carries it — no query-param gymnastics needed.
// GET /api/v1/proxy-token
func (s *Server) handleProxyToken(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(r)
	if !ok || user.Username == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	tok := s.auth.generateProxyToken(user.Username)
	// Set as a short-lived SameSite=Lax cookie so the subsequent window.open
	// automatically carries it. Using raw header write to avoid any potential
	// interaction with gzip-wrapped ResponseWriter.
	// HttpOnly so page JS can't read the proxy token; Secure when served over TLS.
	secure := ""
	if s.isHTTPS(r) {
		secure = "; Secure"
	}
	ck := fmt.Sprintf("proxy_token=%s; Path=/; Max-Age=%d; HttpOnly; SameSite=Lax%s", tok, int(proxyTokenTTL.Seconds()), secure)
	w.Header().Add("Set-Cookie", ck)
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

// handleHTTPProxyDelete deletes an HTTP proxy shortcut.
// DELETE /api/v1/http-proxy/{id}
func (s *Server) handleHTTPProxyDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	if err := s.cfg.DeleteHTTPProxy(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "deleted"})
}

// --- TCP Forward Enhanced API ---

// handleForwardToggle enables or disables a TCP forwarding rule.
// PUT /api/v1/forward/{id}/toggle
func (s *Server) handleForwardToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	existing := s.forward.getRule(id)
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "forward.rule_not_found")})
		return
	}
	if !s.requireHostAccess(w, r, existing.hostID) {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	rule, err := s.forward.toggleRule(id, req.Enabled)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	// v5.4.1: re-create the listener when re-enabling a rule that was stopped.
	// 按协议重建：UDP 用 ListenPacket + serveForwardUDP，TCP 用 Listen + serveForwardListener。
	if req.Enabled && rule.listener == nil && rule.packetConn == nil {
		if rule.protocol == "udp" {
			pc, lnErr := net.ListenPacket("udp", rule.listenAddr)
			if lnErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": lnErr.Error()})
				return
			}
			s.forward.mu.Lock()
			rule.packetConn = pc
			s.forward.mu.Unlock()
			go s.serveForwardUDP(rule)
		} else {
			ln, lnErr := net.Listen("tcp", rule.listenAddr)
			if lnErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": lnErr.Error()})
				return
			}
			s.forward.mu.Lock()
			rule.listener = ln
			s.forward.mu.Unlock()
			go s.serveForwardListener(rule)
		}
	}
	// Count sessions belonging to this rule (under lock)
	sessions := 0
	s.forward.mu.Lock()
	for _, sess := range s.forward.sessions {
		if sess.ruleID == id {
			sessions++
		}
	}
	s.forward.mu.Unlock()
	writeJSON(w, http.StatusOK, infoFromRule(rule, sessions, 0))
}

// handleForwardEdit updates a TCP forwarding rule (host, target port, local port).
// PUT /api/v1/forward/{id}
func (s *Server) handleForwardEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	var req struct {
		HostID           string   `json:"host_id"`
		TargetPort       int      `json:"target_port"`
		LocalPort        int      `json:"local_port"`
		RemoteTarget     string   `json:"remote_target"` // 跳板目标
		WhitelistEnabled *bool    `json:"whitelist_enabled"`
		Whitelist        []string `json:"whitelist"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if req.TargetPort < 1 || req.TargetPort > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "forward.invalid_port")})
		return
	}
	existing := s.forward.getRule(id)
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "forward.rule_not_found")})
		return
	}
	if !s.requireHostAccess(w, r, existing.hostID) {
		return
	}
	// Lookup hostname when host_id is provided
	var hostname string
	if req.HostID != "" {
		if !s.requireHostAccess(w, r, req.HostID) {
			return
		}
		hostname = s.hostLabelForID(req.HostID)
	}
	rule, err := s.forward.updateRule(id, req.HostID, hostname, req.TargetPort, req.LocalPort, req.RemoteTarget)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if req.WhitelistEnabled != nil || req.Whitelist != nil {
		wlOn := rule.whitelistEnabled
		if req.WhitelistEnabled != nil {
			wlOn = *req.WhitelistEnabled
		}
		wlIn := req.Whitelist
		if wlIn == nil {
			_, wlIn, _ = rule.whitelistSnapshot()
		}
		wl, wlErr := normalizeWhitelist(wlOn, wlIn)
		if wlErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": wlErr.Error()})
			return
		}
		rule, _ = s.forward.updateRuleWhitelist(id, wlOn, wl)
	}
	// v5.4.1: when localPort changed, the old listener was closed — rebind
	// v5.5.76: handle both TCP and UDP re-listen after edit
	if rule.enabled {
		if rule.protocol == "udp" {
			if rule.packetConn == nil {
				pc, lnErr := net.ListenPacket("udp", rule.listenAddr)
				if lnErr != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": lnErr.Error()})
					return
				}
				s.forward.mu.Lock()
				rule.packetConn = pc
				s.forward.mu.Unlock()
				go s.serveForwardUDP(rule)
			}
		} else {
			if rule.listener == nil {
				ln, lnErr := net.Listen("tcp", rule.listenAddr)
				if lnErr != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": lnErr.Error()})
					return
				}
				s.forward.mu.Lock()
				rule.listener = ln
				s.forward.mu.Unlock()
				go s.serveForwardListener(rule)
			}
		}
	}
	// Count sessions belonging to this rule (under lock)
	sessions := 0
	s.forward.mu.Lock()
	for _, sess := range s.forward.sessions {
		if sess.ruleID == id {
			sessions++
		}
	}
	s.forward.mu.Unlock()
	writeJSON(w, http.StatusOK, infoFromRule(rule, sessions, 0))
}

// handleForwardCopy duplicates a TCP forwarding rule.
// POST /api/v1/forward/{id}/copy
func (s *Server) handleForwardCopy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	// Look up the original rule to copy its parameters
	orig := s.forward.getRule(id)
	if orig == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "forward.rule_not_found")})
		return
	}
	if !s.requireHostAccess(w, r, orig.hostID) {
		return
	}
	user, _ := s.currentUser(r)
	operator := s.clientIP(r)
	if user.Username != "" {
		operator = user.Username
	}
	// v5.4.1: use createRule (which creates a real listener) instead of
	// copyRule (which leaves listener=nil, causing a panic in serveForwardListener).
	listenHost := s.cfg.ForwardListenAddr()
	wlOn, wlList, _ := orig.whitelistSnapshot()
	newRule, err := s.forward.createRule(orig.hostID, orig.hostname, orig.targetPort, 0, listenHost, orig.protocol, "", operator, orig.remoteTarget, wlOn, wlList)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	go s.serveRule(newRule)
	// Count sessions for the new rule (under lock)
	sessions := 0
	s.forward.mu.Lock()
	for _, sess := range s.forward.sessions {
		if sess.ruleID == newRule.id {
			sessions++
		}
	}
	s.forward.mu.Unlock()
	writeJSON(w, http.StatusOK, infoFromRule(newRule, sessions, 0))
}

// --- HTTP Proxy Enhanced API ---

// handleHTTPProxyToggle enables or disables an HTTP proxy shortcut.
// PUT /api/v1/http-proxy/{id}/toggle
func (s *Server) handleHTTPProxyToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.ToggleHTTPProxy(id, req.Enabled); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	// Return updated proxy
	proxies := s.cfg.ListHTTPProxies()
	for _, p := range proxies {
		if p.ID == id {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

// handleHTTPProxyEdit updates an HTTP proxy shortcut configuration.
// PUT /api/v1/http-proxy/{id}
func (s *Server) handleHTTPProxyEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	var req HTTPProxyConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if req.HostID == "" || req.TargetPort < 1 || req.TargetPort > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "forward.host_port_required")})
		return
	}
	// Lookup hostname
	for _, h := range s.store.ListHosts() {
		if h.ID == req.HostID {
			req.Hostname = h.Hostname
			break
		}
	}
	if err := s.cfg.UpdateHTTPProxy(id, req); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	// Return updated proxy
	proxies := s.cfg.ListHTTPProxies()
	for _, p := range proxies {
		if p.ID == id {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

// handleHTTPProxyCopy duplicates an HTTP proxy shortcut.
// POST /api/v1/http-proxy/{id}/copy
func (s *Server) handleHTTPProxyCopy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	newProxy, err := s.cfg.CopyHTTPProxy(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, newProxy)
}
