package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// pushClient represents a connected browser WebSocket client receiving push updates.
type pushClient struct {
	ws            *wsConn
	done          chan struct{}
	closed        bool
	lastRosterSig string // last pushed host-id roster; empty until first hosts_changed
}

// pushHub manages connected WebSocket clients and broadcasts data to them.
type pushHub struct {
	mu      sync.Mutex
	clients map[*pushClient]bool
}

func newPushHub() *pushHub {
	return &pushHub{clients: make(map[*pushClient]bool)}
}

// handlePushWS upgrades an HTTP request to a WebSocket and streams periodic
// push updates (summary + alerts) to the connected browser. The browser falls
// back to REST polling when this endpoint is unavailable.
func (s *Server) handlePushWS(w http.ResponseWriter, r *http.Request) {
	// Require authentication (same session cookie as the REST API)
	if _, ok := s.currentUser(r); !ok {
		http.Error(w, Tr(r, "auth.unauthorized"), http.StatusUnauthorized)
		return
	}
	ws, err := wsAccept(w, r)
	if err != nil {
		return
	}
	defer ws.Close()

	c := &pushClient{ws: ws, done: make(chan struct{})}
	s.push.Register(c)
	defer s.push.Unregister(c)

	// Send an initial ping immediately, then periodic updates every 3 seconds
	s.pushPush(c)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.pushPush(c)
		case <-c.done:
			return
		}
	}
}

// pushSnapshot 是一轮推送的全部产物：摘要与告警的 JSON 字节、主机名册签名。
//
// 它在所有 WebSocket 客户端之间**共享**。原来 pushPush 对每个连接各算一遍：
// ListHosts（复制全部主机）+ Evaluate（遍历全部主机）+ 四个附加评估器 + 名册签名排序，
// 每 3 秒一次——10 个打开的控制台就是十倍的工作量，5000 台时每个控制台每 3 秒都要
// 让服务端复制 5000 台主机再评估一遍。现在同一秒内的所有客户端拿同一份快照。
type pushSnapshot struct {
	at          time.Time
	totalHosts  int
	onlineHosts int
	summaryJSON []byte
	alertsJSON  []byte
	rosterSig   string
}

// 状态是包级变量而不是 Server 字段：往 handlers.go 的 Server 结构体加字段会打断
// 开源镜像仓的发版构建（见 CLAUDE.md）。
var (
	pushSnapMu   sync.Mutex
	pushSnapLast *pushSnapshot
)

// pushSnapshotTTL 略小于推送周期（3 s），让同一轮 tick 上的客户端复用一份快照，
// 同时保证任何客户端拿到的数据都不超过一个周期的陈旧度。
const pushSnapshotTTL = 2 * time.Second

func (s *Server) currentPushSnapshot() *pushSnapshot {
	pushSnapMu.Lock()
	defer pushSnapMu.Unlock()
	if pushSnapLast != nil && time.Since(pushSnapLast.at) < pushSnapshotTTL {
		return pushSnapLast
	}
	snap := s.buildPushSnapshot()
	pushSnapLast = snap
	return snap
}

func (s *Server) buildPushSnapshot() *pushSnapshot {
	hosts := s.store.ListHosts()
	now := time.Now().Unix()
	th := s.cfg.Thresholds()
	offlineSec := int64(th.OfflineAfter.Seconds())

	online := 0
	for _, h := range hosts {
		if now-h.LastSeen <= offlineSec {
			online++
		}
	}
	alerts := Evaluate(hosts, th)
	alerts = append(alerts, EvaluateForward(s.forward.Snapshot(), th)...)
	alerts = append(alerts, EvaluateHyperV(s.hv)...)
	alerts = append(alerts, EvaluateSNMP(s.snmp, th)...)
	alerts = append(alerts, EvaluateNetFlow(s.nf, th)...)
	crit, warn := 0, 0
	for _, a := range alerts {
		if a.Level == "critical" {
			crit++
		} else {
			warn++
		}
	}
	summary := map[string]any{
		"type": "summary",
		"data": map[string]any{
			"total_hosts":      len(hosts),
			"online_hosts":     online,
			"offline_hosts":    len(hosts) - online,
			"critical_alerts":  crit,
			"warning_alerts":   warn,
			"plugin_events":    s.store.EventCount(),
			"server_time_unix": now,
			"version":          appVersion,
			"terminal_enabled": s.cfg.TerminalEnabled(),
			"desktop_enabled":  s.cfg.TerminalEnabled(),
		},
	}
	snap := &pushSnapshot{at: time.Now(), totalHosts: len(hosts), onlineHosts: online, rosterSig: hostRosterSig(hosts)}
	snap.summaryJSON, _ = json.Marshal(summary)
	snap.alertsJSON, _ = json.Marshal(map[string]any{"type": "alerts", "data": alerts})
	return snap
}

// pushPush sends the current summary + alerts to a single client.
func (s *Server) pushPush(c *pushClient) {
	snap := s.currentPushSnapshot()
	if len(snap.summaryJSON) > 0 {
		_ = c.ws.WriteText(snap.summaryJSON)
	}
	if len(snap.alertsJSON) > 0 {
		_ = c.ws.WriteText(snap.alertsJSON)
	}
	// Notify browsers when the host roster changes so host/type trees refresh
	// without waiting for the manual tree refresh button or the next REST poll.
	sig := snap.rosterSig
	if c.lastRosterSig == "" {
		c.lastRosterSig = sig // seed; avoid a redundant fetch right after connect
	} else if sig != c.lastRosterSig {
		c.lastRosterSig = sig
		chg := map[string]any{
			"type": "hosts_changed",
			"data": map[string]any{
				"total_hosts":  snap.totalHosts,
				"online_hosts": snap.onlineHosts,
				"sig":          sig,
			},
		}
		if data, err := json.Marshal(chg); err == nil {
			_ = c.ws.WriteText(data)
		}
	}
}

// hostRosterSig is a stable fingerprint of enrolled host IDs (join/leave only).
func hostRosterSig(hosts []*Host) string {
	ids := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if h != nil && h.ID != "" {
			ids = append(ids, h.ID)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// Register adds a client to the hub.
func (h *pushHub) Register(c *pushClient) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

// Unregister removes a client from the hub and signals the handler to exit.
func (h *pushHub) Unregister(c *pushClient) {
	h.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.done)
	}
	delete(h.clients, c)
	h.mu.Unlock()
}

// BroadcastCount returns the number of connected push clients (for monitoring).
func (h *pushHub) BroadcastCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
