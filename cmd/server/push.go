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

// pushPush sends the current summary + alerts to a single client.
func (s *Server) pushPush(c *pushClient) {
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
			"plugin_events":    len(s.store.RecentEvents()),
			"server_time_unix": now,
			"version":          appVersion,
			"terminal_enabled": s.cfg.TerminalEnabled(),
			"desktop_enabled":  s.cfg.TerminalEnabled(),
		},
	}
	if data, err := json.Marshal(summary); err == nil {
		_ = c.ws.WriteText(data)
	}
	// Also push alerts
	alertsMsg := map[string]any{
		"type": "alerts",
		"data": alerts,
	}
	if data, err := json.Marshal(alertsMsg); err == nil {
		_ = c.ws.WriteText(data)
	}
	// Notify browsers when the host roster changes so host/type trees refresh
	// without waiting for the manual tree refresh button or the next REST poll.
	sig := hostRosterSig(hosts)
	if c.lastRosterSig == "" {
		c.lastRosterSig = sig // seed; avoid a redundant fetch right after connect
	} else if sig != c.lastRosterSig {
		c.lastRosterSig = sig
		chg := map[string]any{
			"type": "hosts_changed",
			"data": map[string]any{
				"total_hosts":  len(hosts),
				"online_hosts": online,
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
