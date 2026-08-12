package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Port forwarding relay — server side.
//
// Two modes:
//   - TCP port mapping: the server opens a local TCP listener (0.0.0.0:port by default)
//     and relays each accepted connection through the agent to localhost:targetPort
//     on the monitored host.
//   - HTTP reverse proxy: the server handles HTTP requests at /proxy/{hostID}/{port}/...
//     and tunnels them through the agent to the target's HTTP service.

// ---- Constants (P0: security limits) ----

const (
	maxForwardSessions  = 300              // P0: maximum concurrent forwarding sessions
	maxForwardBodySize  = 100 << 20        // P0: maximum HTTP request body (100MB) to prevent OOM
	forwardReadBufSize  = 32 << 10         // P1: 32KB read buffer (was 16KB)
	forwardReadTimeout  = 30 * time.Second // P1: HTTP response read timeout
	forwardTCPKeepAlive = 60 * time.Second // P1: TCP keepalive interval
)

// hopByHopHeaders are per-connection headers that must not be forwarded (RFC 7230 §6.1).
// P2: replaced whitelist approach with blacklist (more complete, fewer missed headers).
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// forwardStats tracks aggregate forwarding metrics (P3: observability).
type forwardStats struct {
	ActiveSessions int64
	TotalSessions  int64
	TotalBytes     int64
	Errors         int64
	TotalLatencyMs int64 // 累计延迟毫秒
	LatencySamples int64 // 延迟采样计数
	// 带宽滑动窗口（最近 60 秒字节数）
	bwMu      sync.Mutex
	bwBuckets [60]int64 // 每秒一个桶
	bwIdx     int
	bwLastSec int64
}

func (fs *forwardStats) incActive() {
	atomic.AddInt64(&fs.ActiveSessions, 1)
	atomic.AddInt64(&fs.TotalSessions, 1)
}
func (fs *forwardStats) decActive() { atomic.AddInt64(&fs.ActiveSessions, -1) }
func (fs *forwardStats) addBytes(n int64) {
	atomic.AddInt64(&fs.TotalBytes, n)
	fs.recordBW(n)
}
func (fs *forwardStats) incError() { atomic.AddInt64(&fs.Errors, 1) }
func (fs *forwardStats) addLatency(ms int64) {
	atomic.AddInt64(&fs.TotalLatencyMs, ms)
	atomic.AddInt64(&fs.LatencySamples, 1)
}

// recordBW appends bytes to the current second's bandwidth bucket.
func (fs *forwardStats) recordBW(n int64) {
	fs.bwMu.Lock()
	defer fs.bwMu.Unlock()
	now := time.Now().Unix()
	if now != fs.bwLastSec {
		fs.bwIdx = (fs.bwIdx + 1) % 60
		fs.bwBuckets[fs.bwIdx] = 0
		fs.bwLastSec = now
	}
	fs.bwBuckets[fs.bwIdx] += n
}

// bandwidthBps returns the current bytes-per-second rate (last 60s avg).
func (fs *forwardStats) bandwidthBps() float64 {
	fs.bwMu.Lock()
	defer fs.bwMu.Unlock()
	var total int64
	for _, v := range fs.bwBuckets {
		total += v
	}
	return float64(total) / 60.0
}

// ForwardSnapshot is a point-in-time snapshot of forwarding metrics.
type ForwardSnapshot struct {
	ActiveSessions int64   `json:"active_sessions"`
	TotalSessions  int64   `json:"total_sessions"`
	TotalBytes     int64   `json:"total_bytes"`
	Errors         int64   `json:"errors"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	BandwidthBps   float64 `json:"bandwidth_bps"`
	MaxSessions    int64   `json:"max_sessions"`
}

// Snapshot returns a point-in-time snapshot of forwarding metrics.
func (m *forwardManager) Snapshot() ForwardSnapshot {
	var s ForwardSnapshot
	s.ActiveSessions = atomic.LoadInt64(&m.stats.ActiveSessions)
	s.TotalSessions = atomic.LoadInt64(&m.stats.TotalSessions)
	s.TotalBytes = atomic.LoadInt64(&m.stats.TotalBytes)
	s.Errors = atomic.LoadInt64(&m.stats.Errors)
	samples := atomic.LoadInt64(&m.stats.LatencySamples)
	if samples > 0 {
		s.AvgLatencyMs = float64(atomic.LoadInt64(&m.stats.TotalLatencyMs)) / float64(samples)
	}
	s.BandwidthBps = m.stats.bandwidthBps()
	s.MaxSessions = maxForwardSessions
	return s
}

// forwardSession is one tunneled connection (TCP or HTTP).
type forwardSession struct {
	id          string
	ruleID      string // TCP rule that spawned this session; "" for HTTP
	counterKey  string // per-rule / per-http-proxy connection-counter key
	hostID      string
	hostname    string
	targetPort  int
	mode        string // "tcp" | "http"
	operator    string
	toAgent     chan []byte   // user data → agent (rx stream)
	toUser      chan []byte   // agent data → user (tx stream)
	agentUp     chan struct{} // closed once the agent attaches its tx stream
	done        chan struct{}
	upOnce      sync.Once
	doneOnce    sync.Once
	closeReason string // P3: reason the session ended
	mu          sync.Mutex
	lastActive  int64 // unix seconds of last data transfer (for idle timeout)
}

func (s *forwardSession) markAgentUp() { s.upOnce.Do(func() { close(s.agentUp) }) }
func (s *forwardSession) close() {
	s.doneOnce.Do(func() { close(s.done) })
}
func (s *forwardSession) closeWith(reason string) {
	s.mu.Lock()
	if s.closeReason == "" {
		s.closeReason = reason
	}
	s.mu.Unlock()
	s.close()
}
func (s *forwardSession) touch() {
	s.mu.Lock()
	s.lastActive = time.Now().Unix()
	s.mu.Unlock()
}
func (s *forwardSession) getCloseReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeReason != "" {
		return s.closeReason
	}
	return Tz("log.forward_reason_eof")
}

// forwardRule is a persistent TCP forwarding rule with its own listener.
type forwardRule struct {
	id           string
	hostID       string
	hostname     string
	targetPort   int
	localPort    int
	listenAddr   string // "127.0.0.1:port"
	listener     net.Listener
	packetConn   net.PacketConn // UDP：非空表示 UDP 转发（与 listener 二选一）
	protocol     string         // "tcp"(默认) | "udp"
	groupID      string         // 端口范围批量创建时同组共享，供整组删除/停用
	operator     string
	createdAt    int64
	enabled      bool   // whether this rule is currently active
	remoteTarget string // 跳板目标，如 "192.168.30.220:3306"（为空时走 Agent 本机 localhost）
	// Source IP whitelist (hot-updated via setWhitelist / atomic snapshot).
	whitelistEnabled bool
	whitelist        []string
	wl               atomic.Value // *wlSnap
}

// forwardWaitInfo is what the agent receives from the long-poll.
type forwardWaitInfo struct {
	sessionID    string
	targetPort   int
	mode         string
	remoteTarget string // 跳板目标地址（为空时 Agent 走 localhost）
}

// forwardInfo is the JSON view for the API.
type forwardInfo struct {
	ID            string `json:"id"`
	HostID        string `json:"host_id"`
	Hostname      string `json:"hostname"`
	TargetPort    int    `json:"target_port"`
	LocalPort     int    `json:"local_port"`
	ListenAddr    string `json:"listen_addr"`
	Status        string `json:"status"`
	CreatedAt     int64  `json:"created_at"`
	Operator      string `json:"operator"`
	Sessions      int    `json:"sessions"`       // 当前活跃连接（会话）数
	TotalSessions int64  `json:"total_sessions"` // 累计总连接（会话）数
	Enabled       bool   `json:"enabled"`
	Protocol      string `json:"protocol,omitempty"`       // "tcp" | "udp"
	GroupID       string `json:"group_id,omitempty"`       // 端口范围批量组（同组共享），供整组操作
	RemoteTarget  string `json:"remote_target,omitempty"` // 跳板目标地址
	WhitelistEnabled bool     `json:"whitelist_enabled,omitempty"`
	Whitelist        []string `json:"whitelist,omitempty"`
}

// fwdCounter tracks per-rule / per-http-proxy connection counts. active is the
// current live count (inc on create, dec on close); total is cumulative
// (inc-only). Guarded by forwardManager.mu.
type fwdCounter struct {
	active int64
	total  int64
}

type forwardManager struct {
	mu              sync.Mutex
	rules           map[string]*forwardRule
	sessions        map[string]*forwardSession
	waiters         map[string]chan forwardWaitInfo // hostID -> a waiting agent poll
	pendingSessions map[string][]forwardWaitInfo    // v5.2.5: queued for agents between polls
	connCounts      map[string]*fwdCounter          // per-rule / per-http-proxy 活跃+累计连接数
	stats           forwardStats                    // P3: aggregate metrics
	cfg             *ConfigStore                    // config reference for port range
	store           *Store                          // store reference for host lookup
	server          *Server                         // server reference for serveForwardListener (restart)
}

func newForwardManager(cfg *ConfigStore) *forwardManager {
	fm := &forwardManager{
		rules:           map[string]*forwardRule{},
		sessions:        map[string]*forwardSession{},
		waiters:         map[string]chan forwardWaitInfo{},
		pendingSessions: map[string][]forwardWaitInfo{},
		connCounts:      map[string]*fwdCounter{},
		cfg:             cfg,
	}
	go fm.idleChecker()
	return fm
}

// sessionCounterKey derives the connection-counter key for a session: per-rule
// for TCP/UDP (ruleID set), per host+port for HTTP/WS proxies (ruleID empty).
func sessionCounterKey(ruleID, hostID string, targetPort int) string {
	if ruleID != "" {
		return "rule:" + ruleID
	}
	return "hp:" + hostID + ":" + strconv.Itoa(targetPort)
}

// counts returns (active, total) for a counter key. Caller must hold m.mu.
func (m *forwardManager) counts(key string) (int, int64) {
	if c := m.connCounts[key]; c != nil {
		return int(c.active), c.total
	}
	return 0, 0
}

// idleChecker closes sessions that have had no data for forwardIdleTimeout.
// P1: Fixed lock ordering — collects session references under m.mu, then
// operates on each without holding m.mu to avoid deadlock with other paths
// that may hold sess.mu → m.mu.
func (m *forwardManager) idleChecker() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		// Collect idle session IDs under the manager lock
		m.mu.Lock()
		type idleEntry struct {
			sess *forwardSession
			idle int64
		}
		var idles []idleEntry
		now := time.Now().Unix()
		for _, sess := range m.sessions {
			sess.mu.Lock()
			idle := now - sess.lastActive
			sess.mu.Unlock()
			if idle > int64(forwardIdleTimeout.Seconds()) {
				idles = append(idles, idleEntry{sess, idle})
			}
		}
		m.mu.Unlock()
		// Close idle sessions outside the manager lock
		for _, entry := range idles {
			slog.Info(Tz("log.forward_idle_timeout"), "session", entry.sess.id, "idle_sec", entry.idle)
			entry.sess.closeWith(Tz("log.forward_reason_timeout"))
		}
	}
}

const forwardIdleTimeout = 30 * time.Minute

// forwardFrame encodes one rx message as [type:1][len:2 BE][payload].
// type 'd' = data, 'c' = close signal.
func forwardFrame(typ byte, payload []byte) []byte {
	if len(payload) > 0xffff {
		payload = payload[:0xffff]
	}
	b := make([]byte, 3+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint16(b[1:], uint16(len(payload)))
	copy(b[3:], payload)
	return b
}

// rawForwardReader 把 agent 经 tx POST 回传的响应字节（来自 sess.toUser 通道）
// 暴露为一个连续的 io.Reader，供 io.ReadAll 一次性完整读取。
//
// ====== 竞态修复（彻底消除 unexpected EOF） ======
// 问题：handleAgentForwardTx 先向 toUser 发送最后一帧，再 close(sess.done)，
// 两个操作在同一个 goroutine 中纳秒级连续执行。rawForwardReader 的双 select
// 在阻塞等待时，若 toUser 和 done 同时就绪，Go 的 select 会随机选择 → 50%
// 概率选中 done 而丢弃通道缓冲中已到达的最后一帧 → http.ReadResponse 读到
// 截断的数据 → "unexpected EOF"。
//
// 修复：done 关闭后，必须再做一次非阻塞排空确认通道无残留数据。因为写端
// 保证「写通道」在「关 done」之前，所以当 done 被关闭时，若有数据则一定
// 已在通道缓冲中，非阻塞读取必定能拿到。
//
// 设计要点：
//   - 不依赖 io.Pipe：pipe 的写端关闭时机若与 http.ReadResponse 的读取错开，
//     解析器会读到提前的 EOF → "unexpected EOF"。
//   - 直接消费通道：agent 关闭 tx POST（= 目标服务关闭连接）后 toUser 不再有数据，
//     ReadAll 自然结束。
//   - 可选地把前 diagLeft 字节镜像到诊断缓冲区，用于超时/错误时的日志预览。
type rawForwardReader struct {
	ch       chan []byte
	done     <-chan struct{}
	diag     *bytes.Buffer
	diagMu   *sync.Mutex
	diagLeft int
	remain   []byte // 上次 Read 未消费完的剩余数据（防止 io.ReadAll 分批读取时截断）
}

func (x *rawForwardReader) Read(p []byte) (int, error) {
	// 优先返回上次 Read 未消费完的剩余数据
	if len(x.remain) > 0 {
		n := copy(p, x.remain)
		if n < len(x.remain) {
			x.remain = x.remain[n:]
		} else {
			x.remain = nil
		}
		return n, nil
	}

	for {
		// P0: 优先非阻塞地消费通道中已到达的数据。这是竞态修复的第一层：
		// 在进入阻塞等待之前，先排空通道缓冲中的所有就绪帧。
		select {
		case b, ok := <-x.ch:
			if !ok {
				return 0, io.EOF
			}
			if len(b) == 0 {
				continue
			}
			return x.emit(p, b), nil
		default:
		}

		// 阻塞等待：数据到达 或 会话结束（done 关闭）
		select {
		case b, ok := <-x.ch:
			if !ok {
				return 0, io.EOF
			}
			if len(b) == 0 {
				continue
			}
			return x.emit(p, b), nil
		case <-x.done:
			// P0: 竞态修复关键 — done 关闭后必须再做一次非阻塞排空。
			// handleAgentForwardTx 保证「向 toUser 写最后一帧」发生在
			// 「close(sess.done)」之前；当 select 因调度随机选中 done
			// 时，toUser 缓冲中的最后一帧尚未被消费，必须在此抢先取出。
			// 只有当 toUser 中也确实无数据时，才是真正的流结束。
			select {
			case b, ok := <-x.ch:
				if !ok {
					return 0, io.EOF
				}
				if len(b) == 0 {
					continue
				}
				return x.emit(p, b), nil
			default:
				return 0, io.EOF
			}
		}
	}
}

// emit 写入诊断镜像并返回拷贝到 p 的字节数。
// 当 p 不足容纳整个 b 时，将未消费部分保存到 x.remain 供下次 Read 使用，
// 防止 io.ReadAll 分批增长缓冲区时数据截断丢失。
func (x *rawForwardReader) emit(p, b []byte) int {
	if x.diag != nil && x.diagLeft > 0 {
		x.diagMu.Lock()
		if x.diagLeft > 0 {
			n := len(b)
			if n > x.diagLeft {
				n = x.diagLeft
			}
			x.diag.Write(b[:n])
			x.diagLeft -= n
		}
		x.diagMu.Unlock()
	}
	n := copy(p, b)
	if n < len(b) {
		// p 太小装不下整个 b，把剩余部分保存起来
		x.remain = make([]byte, len(b)-n)
		copy(x.remain, b[n:])
	}
	return n
}

// truncateStr 截取 s 为前 max 个字符（中文按 rune 处理），超出时追加 "…"。
func truncateStr(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// ---- session lifecycle ----

func (m *forwardManager) createSession(ruleID, hostID, hostname string, targetPort int, mode, operator string) (*forwardSession, error) {
	m.mu.Lock()
	// P0: enforce maximum session count
	if len(m.sessions) >= maxForwardSessions {
		m.mu.Unlock()
		m.stats.incError()
		return nil, fmt.Errorf("%s", Tz("forward.too_many_sessions"))
	}
	key := sessionCounterKey(ruleID, hostID, targetPort)
	s := &forwardSession{
		id: termID(), ruleID: ruleID, counterKey: key, hostID: hostID, hostname: hostname,
		targetPort: targetPort, mode: mode, operator: operator,
		toAgent: make(chan []byte, 64), toUser: make(chan []byte, 256),
		agentUp: make(chan struct{}), done: make(chan struct{}),
		lastActive: time.Now().Unix(),
	}
	m.sessions[s.id] = s
	c := m.connCounts[key]
	if c == nil {
		c = &fwdCounter{}
		m.connCounts[key] = c
	}
	c.active++
	c.total++
	m.stats.incActive()
	m.mu.Unlock()
	return s, nil
}

func (m *forwardManager) getSession(id string) *forwardSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *forwardManager) removeSession(id string) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
		if c := m.connCounts[sess.counterKey]; c != nil {
			if c.active--; c.active < 0 {
				c.active = 0
			}
		}
		m.stats.decActive()
	}
	m.mu.Unlock()
	_ = sess
}

// notifyAgent hands a new forward session to the agent currently long-polling
// for hostID. If no active waiter exists, the notification is queued in
// pendingSessions so the agent can pick it up on its next poll cycle —
// eliminating the race where the agent is between polls and the notification
// is silently dropped (causing 502).
func (m *forwardManager) notifyAgent(hostID string, info forwardWaitInfo) bool {
	m.mu.Lock()
	w := m.waiters[hostID]
	delete(m.waiters, hostID)
	if w == nil {
		// No active waiter — queue for next poll (same pattern as termManager)
		m.pendingSessions[hostID] = append(m.pendingSessions[hostID], info)
		m.mu.Unlock()
		return true
	}
	m.mu.Unlock()
	select {
	case w <- info:
		return true
	default:
		// Waiter channel full — queue instead of dropping
		m.mu.Lock()
		m.pendingSessions[hostID] = append(m.pendingSessions[hostID], info)
		m.mu.Unlock()
		return true
	}
}

// agentOfflineReason diagnoses why no agent forward waiter exists for hostID,
// returning a human-readable reason in the current language to help the user
// understand whether the host is truly offline, has never registered, or may
// have a fingerprint mismatch.
func agentOfflineReason(store *Store, hostID string) string {
	host, ok := store.GetHost(hostID)
	if !ok {
		return Tz("forward.reason_host_unknown")
	}
	now := time.Now().Unix()
	offlineSec := now - host.LastSeen
	if offlineSec > 120 {
		ago := fmt.Sprintf("%d", offlineSec/60)
		return Tz("forward.reason_agent_down", ago)
	}
	if host.Fingerprint == "" {
		return Tz("forward.reason_no_fingerprint")
	}
	return Tz("forward.reason_channel_not_ready")
}

func (m *forwardManager) registerWaiter(hostID string) chan forwardWaitInfo {
	ch := make(chan forwardWaitInfo, 1)
	m.mu.Lock()
	m.waiters[hostID] = ch
	m.mu.Unlock()
	return ch
}

func (m *forwardManager) unregisterWaiter(hostID string, ch chan forwardWaitInfo) {
	m.mu.Lock()
	if m.waiters[hostID] == ch {
		delete(m.waiters, hostID)
	}
	m.mu.Unlock()
}

// ---- rule management ----

func persistedFromRule(r *forwardRule) PersistedForwardRule {
	enabled, list, _ := r.whitelistSnapshot()
	return PersistedForwardRule{
		ID: r.id, HostID: r.hostID, Hostname: r.hostname,
		TargetPort: r.targetPort, LocalPort: r.localPort,
		ListenAddr: r.listenAddr, Operator: r.operator,
		CreatedAt: r.createdAt, Enabled: r.enabled,
		Protocol: r.protocol, GroupID: r.groupID, RemoteTarget: r.remoteTarget,
		WhitelistEnabled: enabled, Whitelist: append([]string(nil), list...),
	}
}

func infoFromRule(r *forwardRule, sessions int, total int64) forwardInfo {
	enabled, list, _ := r.whitelistSnapshot()
	return forwardInfo{
		ID: r.id, HostID: r.hostID, Hostname: r.hostname,
		TargetPort: r.targetPort, LocalPort: r.localPort,
		ListenAddr: r.listenAddr, Status: "active",
		CreatedAt: r.createdAt, Operator: r.operator,
		Sessions: sessions, TotalSessions: total,
		Enabled: r.enabled, Protocol: r.protocol, GroupID: r.groupID,
		RemoteTarget: r.remoteTarget,
		WhitelistEnabled: enabled, Whitelist: append([]string(nil), list...),
	}
}

func (m *forwardManager) createRule(hostID, hostname string, targetPort, localPort int, listenHost, protocol, groupID, operator, remoteTarget string, wlEnabled bool, wl []string) (*forwardRule, error) {
	if protocol != "udp" {
		protocol = "tcp"
	}
	// Non-loopback bind exposes a raw TCP/UDP tunnel with no session auth on accept.
	// Require an explicit source-IP whitelist so operators cannot accidentally open 0.0.0.0.
	if listenHost != "" && listenHost != "127.0.0.1" && listenHost != "localhost" && listenHost != "::1" {
		if !wlEnabled || len(wl) == 0 {
			return nil, fmt.Errorf("非本机监听地址 %s 必须启用源 IP 白名单（whitelist_enabled + whitelist）", listenHost)
		}
	}
	// If localPort is 0 or requested port is unavailable, try ports in the configured range
	minPort, maxPort := m.cfg.ForwardPortRangeBounds()
	var ln net.Listener
	var pc net.PacketConn
	var err error
	actualPort := localPort
	// tryBind 按协议绑定 TCP 监听器或 UDP 报文连接（二选一）。
	tryBind := func(addr string) error {
		if protocol == "udp" {
			pc, err = net.ListenPacket("udp", addr)
		} else {
			ln, err = net.Listen("tcp", addr)
		}
		return err
	}
	bound := func() bool { return ln != nil || pc != nil }

	if localPort > 0 {
		if tryBind(listenHost+":"+strconv.Itoa(localPort)) != nil {
			actualPort = 0 // 指定端口占用，退回端口段
		}
	}
	if !bound() {
		rng := int(time.Now().UnixNano() % int64((maxPort-minPort)+1))
		for attempt := 0; attempt < 100; attempt++ {
			candidate := minPort + ((rng + attempt) % ((maxPort - minPort) + 1))
			if tryBind(listenHost+":"+strconv.Itoa(candidate)) == nil {
				actualPort = candidate
				break
			}
		}
		if !bound() { // 端口段也满，退回 OS 自动分配
			if tryBind(listenHost+":0") != nil {
				return nil, fmt.Errorf("%s", Tz("forward.listen_failed", err))
			}
		}
	}

	if protocol == "udp" {
		actualPort = pc.LocalAddr().(*net.UDPAddr).Port
	} else {
		actualPort = ln.Addr().(*net.TCPAddr).Port
	}
	actualAddr := listenHost + ":" + strconv.Itoa(actualPort)
	// 安全提示：绑定到非回环地址（如 Docker 部署常用的 0.0.0.0）时，任何能访问该端口的
	// 客户端都可经隧道直达目标主机 localhost 的内网服务（Redis/MySQL/SSH）。转发是裸 TCP
	// 隧道、无法对任意 TCP 客户端做票据握手，故此暴露必须靠防火墙/网络隔离控制——这里显式告警。
	if listenHost != "127.0.0.1" && listenHost != "localhost" && listenHost != "::1" {
		slog.Warn("端口转发监听在非回环地址，暴露面较大：请确保有防火墙/网络隔离限制来源", "addr", actualAddr, "host", hostname, "operator", operator)
	}
	now := time.Now().Unix()
	r := &forwardRule{
		id: termID()[:8], hostID: hostID, hostname: hostname,
		targetPort: targetPort, localPort: actualPort,
		listenAddr: actualAddr,
		listener:   ln, packetConn: pc, protocol: protocol, groupID: groupID,
		operator: operator, createdAt: now,
		enabled: true, remoteTarget: remoteTarget,
	}
	r.setWhitelist(wlEnabled, wl)
	m.mu.Lock()
	m.rules[r.id] = r
	m.mu.Unlock()
	// Persist to config (PostgreSQL)
	_ = m.cfg.AddForwardRule(persistedFromRule(r))
	return r, nil
}

// serveRule 按协议分发到 TCP 监听循环或 UDP 报文循环。
func (s *Server) serveRule(rule *forwardRule) {
	if rule.protocol == "udp" {
		s.serveForwardUDP(rule)
	} else {
		s.serveForwardListener(rule)
	}
}

// groupRuleIDs 返回同一端口范围组（groupID）下的所有规则 ID，供整组删除 / 启停。
func (m *forwardManager) groupRuleIDs(gid string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if gid == "" {
		return nil
	}
	var ids []string
	for id, r := range m.rules {
		if r.groupID == gid {
			ids = append(ids, id)
		}
	}
	return ids
}

// rebindRuleListener 按协议为一条已停用规则重建监听器并启动 serve（重新启用时用）。
func (s *Server) rebindRuleListener(rule *forwardRule) error {
	if rule.protocol == "udp" {
		pc, err := net.ListenPacket("udp", rule.listenAddr)
		if err != nil {
			return err
		}
		s.forward.mu.Lock()
		rule.packetConn = pc
		s.forward.mu.Unlock()
		go s.serveForwardUDP(rule)
		return nil
	}
	ln, err := net.Listen("tcp", rule.listenAddr)
	if err != nil {
		return err
	}
	s.forward.mu.Lock()
	rule.listener = ln
	s.forward.mu.Unlock()
	go s.serveForwardListener(rule)
	return nil
}

func (m *forwardManager) getRule(id string) *forwardRule {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rules[id]
}

func (m *forwardManager) removeRule(id string) bool {
	m.mu.Lock()
	r, ok := m.rules[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	delete(m.rules, id)
	// close all sessions belonging to this rule
	for sid, sess := range m.sessions {
		if sess.ruleID == id {
			sess.closeWith(Tz("log.forward_reason_eof"))
			delete(m.sessions, sid)
			m.stats.decActive()
		}
	}
	m.mu.Unlock()
	if r.listener != nil {
		_ = r.listener.Close()
	}
	if r.packetConn != nil { // UDP：删除时关闭报文连接，避免泄漏 UDP socket
		_ = r.packetConn.Close()
	}
	// Persist deletion to config (PostgreSQL)
	_ = m.cfg.DeleteForwardRule(id)
	return true
}

// toggleRule enables or disables a forwarding rule.
// When disabled, the listener is stopped but the rule config is preserved.
// When enabled with a nil listener, the caller must re-create the listener
// and call serveForwardListener.
func (m *forwardManager) toggleRule(id string, enable bool) (*forwardRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rules[id]
	if !ok {
		return nil, fmt.Errorf("rule not found")
	}
	if r.enabled == enable {
		return r, nil // already in desired state
	}
	r.enabled = enable
	// v5.4.1: actually stop the listener when disabling, so TCP connections
	// are no longer accepted and forwarded.
	if !enable {
		if r.listener != nil {
			_ = r.listener.Close()
			r.listener = nil
		}
		if r.packetConn != nil { // UDP：关闭报文连接以停止接收新数据报
			_ = r.packetConn.Close()
			r.packetConn = nil
		}
	}
	if !enable { // 彻底停用：断开该规则下所有在途会话（不只是停止接受新连接，否则已建立的连接仍能访问）
		for sid, sess := range m.sessions {
			if sess.ruleID == id {
				sess.closeWith(Tz("log.forward_reason_eof"))
				delete(m.sessions, sid)
				m.stats.decActive()
			}
		}
	}
	// Persist toggle to config (PostgreSQL)
	_ = m.cfg.ToggleForwardRule(id, enable)
	return r, nil
}

// updateRule modifies host_id, hostname, target_port and local_port of an existing rule.
// When hostID is non-empty, both hostID and hostname are updated so the rule points to
// the correct host after editing. When hostID is empty, host fields are left unchanged.
// v5.4.1: when localPort changes, the old listener is closed and the new port is
// reflected in listenAddr. The caller must re-create the listener and restart
// serveForwardListener.
func (m *forwardManager) updateRule(id, hostID, hostname string, targetPort, localPort int, remoteTarget string) (*forwardRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rules[id]
	if !ok {
		return nil, fmt.Errorf("rule not found")
	}
	if hostID != "" {
		r.hostID = hostID
		r.hostname = hostname
	}
	if targetPort > 0 {
		r.targetPort = targetPort
	}
	// Update remote target (jump mode)
	if remoteTarget != "" || r.remoteTarget != "" {
		r.remoteTarget = remoteTarget
	}
	// v5.4.1: rebind listener when localPort changes
	if localPort > 0 && localPort != r.localPort {
		if r.listener != nil {
			_ = r.listener.Close()
			r.listener = nil
		}
		if r.packetConn != nil {
			_ = r.packetConn.Close()
			r.packetConn = nil
		}
		r.localPort = localPort
		// Rebuild listenAddr with the new port, keeping the original host
		if host, _, err := net.SplitHostPort(r.listenAddr); err == nil && host != "" {
			r.listenAddr = net.JoinHostPort(host, strconv.Itoa(localPort))
		}
	}
	// Persist update to config (PostgreSQL)
	_ = m.cfg.UpdateForwardRule(id, persistedFromRule(r))
	return r, nil
}

// updateRuleWhitelist hot-updates the source IP whitelist without rebinding the listener.
func (m *forwardManager) updateRuleWhitelist(id string, enabled bool, list []string) (*forwardRule, error) {
	m.mu.Lock()
	r, ok := m.rules[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("rule not found")
	}
	r.setWhitelist(enabled, list)
	m.mu.Unlock()
	_ = m.cfg.UpdateForwardRule(id, persistedFromRule(r))
	return r, nil
}

// copyRule duplicates an existing rule with a new ID.
func (m *forwardManager) copyRule(id string) (*forwardRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rules[id]
	if !ok {
		return nil, fmt.Errorf("rule not found")
	}
	wlOn, wlList, _ := r.whitelistSnapshot()
	newRule := &forwardRule{
		id:           termID()[:8],
		hostID:       r.hostID,
		hostname:     r.hostname,
		targetPort:   r.targetPort,
		localPort:    0, // will be auto-assigned
		listenAddr:   "",
		listener:     nil,
		packetConn:   nil,
		protocol:     r.protocol,
		groupID:      r.groupID,
		operator:     r.operator,
		createdAt:    time.Now().Unix(),
		enabled:      true,
		remoteTarget: r.remoteTarget,
	}
	newRule.setWhitelist(wlOn, wlList)
	m.rules[newRule.id] = newRule
	return newRule, nil
}

// restoreRules recreates TCP forward listeners from persisted config on startup.
// The caller (NewServer) must pass a server reference so listeners can be started.
func (m *forwardManager) restoreRules(srv *Server) {
	rules := m.cfg.ListForwardRules()
	if len(rules) == 0 {
		return
	}
	listenHost := m.cfg.ForwardListenAddr()
	for _, pr := range rules {
		if !pr.Enabled {
			// Store disabled rules without a listener
			dr := &forwardRule{
				id: pr.ID, hostID: pr.HostID, hostname: pr.Hostname,
				targetPort: pr.TargetPort, localPort: pr.LocalPort,
				listenAddr: pr.ListenAddr, operator: pr.Operator,
				createdAt: pr.CreatedAt, enabled: false, protocol: pr.Protocol, groupID: pr.GroupID,
				remoteTarget: pr.RemoteTarget,
			}
			dr.setWhitelist(pr.WhitelistEnabled, pr.Whitelist)
			m.mu.Lock()
			m.rules[pr.ID] = dr
			m.mu.Unlock()
			continue
		}
		// 按协议重新绑定（TCP 监听器 / UDP 报文连接）
		proto := pr.Protocol
		if proto != "udp" {
			proto = "tcp"
		}
		var ln net.Listener
		var pc net.PacketConn
		var err error
		if proto == "udp" {
			if pc, err = net.ListenPacket("udp", pr.ListenAddr); err != nil {
				slog.Warn("恢复 UDP 转发监听失败，尝试自动分配端口", "id", pr.ID, "addr", pr.ListenAddr, "err", err)
				pc, err = net.ListenPacket("udp", listenHost+":0")
			}
		} else {
			if ln, err = net.Listen("tcp", pr.ListenAddr); err != nil {
				slog.Warn("恢复转发规则监听失败，尝试自动分配端口", "id", pr.ID, "addr", pr.ListenAddr, "err", err)
				ln, err = net.Listen("tcp", listenHost+":0")
			}
		}
		if err != nil {
			slog.Error("恢复转发规则失败，跳过", "id", pr.ID, "proto", proto, "err", err)
			continue
		}
		var actualPort int
		var actualAddr string
		if proto == "udp" {
			actualPort = pc.LocalAddr().(*net.UDPAddr).Port
			actualAddr = pc.LocalAddr().String()
		} else {
			actualPort = ln.Addr().(*net.TCPAddr).Port
			actualAddr = ln.Addr().String()
		}
		r := &forwardRule{
			id: pr.ID, hostID: pr.HostID, hostname: pr.Hostname,
			targetPort: pr.TargetPort, localPort: actualPort,
			listenAddr: actualAddr, listener: ln, packetConn: pc, protocol: proto, groupID: pr.GroupID,
			operator: pr.Operator, createdAt: pr.CreatedAt, enabled: true,
			remoteTarget: pr.RemoteTarget,
		}
		r.setWhitelist(pr.WhitelistEnabled, pr.Whitelist)
		// If the port changed, update the persisted config
		if actualPort != pr.LocalPort {
			_ = m.cfg.UpdateForwardRule(pr.ID, persistedFromRule(r))
		}
		m.mu.Lock()
		m.rules[r.id] = r
		m.mu.Unlock()
		go srv.serveRule(r)
	}
}

func (m *forwardManager) listRules() []forwardInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]forwardInfo, 0, len(m.rules))
	for _, r := range m.rules {
		sessions := 0
		for _, s := range m.sessions {
			if s.ruleID == r.id {
				sessions++
			}
		}
		_, total := m.counts("rule:" + r.id)
		out = append(out, infoFromRule(r, sessions, total))
	}
	return out
}

// ---- API handlers (browser-facing, auth-gated) ----

// handleForwardCreate creates a TCP port forwarding rule.
func (s *Server) handleForwardCreate(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.ForwardEnabled() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "forward.disabled")})
		return
	}
	var req struct {
		HostID           string   `json:"host_id"`
		TargetPort       int      `json:"target_port"`
		TargetPortEnd    int      `json:"target_port_end"` // > target_port：端口范围批量转发（含端点）
		LocalPort        int      `json:"local_port"`      // 0 = auto-allocate
		Protocol         string   `json:"protocol"`        // "tcp"(默认) | "udp"
		RemoteTarget     string   `json:"remote_target"`   // 跳板目标，如 "192.168.30.220:3306"
		WhitelistEnabled bool     `json:"whitelist_enabled"`
		Whitelist        []string `json:"whitelist"`
	}
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
	wl, wlErr := normalizeWhitelist(req.WhitelistEnabled, req.Whitelist)
	if wlErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": wlErr.Error()})
		return
	}
	hostname := s.hostLabelForID(req.HostID)
	operator, clientIP := s.actorIP(r)
	user := operator
	if looksLikeIPAddr(operator) {
		user = ""
	}
	listenHost := s.cfg.ForwardListenAddr()
	// 端口范围批量转发：target_port..target_port_end（含）。每个端口一条独立规则，
	// 复用单端口的监听/会话/隧道全套机制（Agent 无需改动）。范围模式下本地端口镜像
	// 目标端口（listen:P → target:P），便于成组访问；被占用时回退到端口段自动分配。
	end := req.TargetPortEnd
	isRange := end > req.TargetPort
	if !isRange {
		end = req.TargetPort
	}
	if end < 1 || end > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "forward.host_port_required")})
		return
	}
	const maxRangePorts = 100
	if end-req.TargetPort+1 > maxRangePorts {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "端口范围过大，单次最多 100 个端口"})
		return
	}
	groupID := ""
	if isRange {
		groupID = termID()[:8] // 同批范围共享一个组 ID，供整组删除 / 启停
	}
	var created []forwardInfo
	var firstErr error
	for p := req.TargetPort; p <= end; p++ {
		lp := req.LocalPort
		if isRange {
			lp = p // 范围：本地端口镜像目标端口
		}
		rule, err := s.forward.createRule(req.HostID, hostname, p, lp, listenHost, req.Protocol, groupID, operator, req.RemoteTarget, req.WhitelistEnabled, wl)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		go s.serveRule(rule)
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: operator, Username: user, IP: clientIP, Host: hostname,
			Message: Tz("log.forward_create", "规则", hostname, p, rule.listenAddr)})
		created = append(created, infoFromRule(rule, 0, 0))
	}
	if len(created) == 0 {
		msg := "创建转发失败"
		if firstErr != nil {
			msg = firstErr.Error()
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": msg})
		return
	}
	if !isRange { // 单端口：保持原有响应契约（前端读 listen_addr 等字段）
		writeJSON(w, http.StatusOK, created[0])
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": created, "count": len(created), "listen_addr": created[0].ListenAddr})
}

// handleForwardDelete closes a forwarding rule and its listener.
func (s *Server) handleForwardDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rule := s.forward.getRule(id)
	if rule == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "forward.rule_not_found")})
		return
	}
	if !s.requireHostAccess(w, r, rule.hostID) {
		return
	}
	operator, clientIP := s.actorIP(r)
	s.forward.removeRule(id)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: operator, IP: clientIP, Host: rule.hostname,
		Message: Tz("log.forward_close", rule.hostname, rule.targetPort)})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleForwardList returns active forwarding rules visible to the caller.
func (s *Server) handleForwardList(w http.ResponseWriter, r *http.Request) {
	all := s.forward.listRules()
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		writeJSON(w, http.StatusOK, all)
		return
	}
	out := make([]forwardInfo, 0, len(all))
	for _, rule := range all {
		if rule.HostID == "" || s.userCanAccessHost(u, rule.HostID) {
			out = append(out, rule)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// serveForwardListener accepts TCP connections for a rule and tunnels each
// one through the agent reverse channel.
func (s *Server) serveForwardListener(rule *forwardRule) {
	if rule == nil || rule.listener == nil {
		return // 防御：未绑定监听器的规则（如复制后尚未建链）不应进入 Accept 循环，避免 nil panic
	}
	for {
		conn, err := rule.listener.Accept()
		if err != nil {
			return // listener closed
		}
		enabled, _, nets := rule.whitelistSnapshot()
		if !clientAllowed(enabled, nets, conn.RemoteAddr()) {
			slog.Debug("转发 TCP 来源 IP 被白名单拒绝", "rule", rule.id, "remote", conn.RemoteAddr().String(), "listen", rule.listenAddr)
			_ = conn.Close()
			continue
		}
		// P1: set TCP keepalive on accepted connections
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(forwardTCPKeepAlive)
		}
		go s.handleForwardTCPConn(rule, conn)
	}
}

// handleForwardTCPConn relays one user TCP connection through the agent.
func (s *Server) handleForwardTCPConn(rule *forwardRule, conn net.Conn) {
	defer conn.Close()
	sess, err := s.forward.createSession(rule.id, rule.hostID, rule.hostname, rule.targetPort, "tcp", rule.operator)
	if err != nil {
		// P3: error feedback to user instead of silent drop
		return
	}
	defer s.forward.removeSession(sess.id)
	defer sess.close()

	// P3: TCP forward audit log
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: rule.operator, Host: rule.hostname,
		Message: Tz("log.forward_tcp", rule.hostname, rule.targetPort)})
	// 跳板模式审计：记录远程目标地址
	if rule.remoteTarget != "" {
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: rule.operator, Host: rule.hostname,
			Message: Tz("log.forward_jump", rule.hostname, rule.remoteTarget)})
	}

	// notify agent
	if !s.forward.notifyAgent(rule.hostID, forwardWaitInfo{sessionID: sess.id, targetPort: rule.targetPort, mode: "tcp", remoteTarget: rule.remoteTarget}) {
		sess.closeWith(Tz("log.forward_reason_agent_down"))
		return // agent not polling
	}
	// watchdog: if agent never attaches, don't hang
	go func() {
		select {
		case <-sess.agentUp:
		case <-time.After(10 * time.Second):
			sess.closeWith(Tz("log.forward_reason_timeout"))
		case <-sess.done:
		}
	}()

	var bytesTransferred int64

	// user → agent (read from TCP, send to toAgent channel as data frames)
	go func() {
		defer sess.close()
		buf := make([]byte, forwardReadBufSize) // P1: 32KB buffer
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				sess.touch()
				b := make([]byte, n)
				copy(b, buf[:n])
				atomic.AddInt64(&bytesTransferred, int64(n))
				select {
				case sess.toAgent <- forwardFrame('d', b):
				case <-sess.done:
					return
				}
			}
			if err != nil {
				// signal close to agent
				select {
				case sess.toAgent <- forwardFrame('c', nil):
				case <-sess.done:
				}
				if err != io.EOF {
					sess.closeWith(Tz("log.forward_reason_error"))
				}
				return
			}
		}
	}()

	// agent → user (read from toUser channel, write to TCP)
	go func() {
		defer sess.close()
		for {
			select {
			case b := <-sess.toUser:
				sess.touch()
				atomic.AddInt64(&bytesTransferred, int64(len(b)))
				if _, err := conn.Write(b); err != nil {
					sess.closeWith(Tz("log.forward_reason_error"))
					return
				}
			case <-sess.done:
				return
			}
		}
	}()

	<-sess.done

	// P3: log close reason + bytes transferred
	s.forward.stats.addBytes(bytesTransferred)
	s.store.AddLog(LogEntry{Kind: KindSystem, Level: "info", Actor: rule.operator, Host: rule.hostname,
		Message: Tz("log.forward_tcp_closed", rule.hostname, rule.targetPort, sess.getCloseReason())})
}

// ---- HTTP reverse proxy ----

// handleHTTPProxy tunnels an HTTP request through the agent to the target's
// HTTP service. The URL pattern is /proxy/{hostID}/{port}/{path...}.
// injectBaseTag inserts a <base href="..."> element at the start of a page's
// <head> so that relative asset URLs in a path-proxied app resolve back through
// the /proxy/{host}/{port}/ prefix instead of hitting the monitor's own root.
//
// It is a best-effort text transform:
//   - if the document already declares a <base>, it is left untouched (the app
//     knows its own base better than we do);
//   - if there is no <head>/<html> to anchor to, the body is returned unchanged.
//
// NOTE: <base> only fixes RELATIVE URLs (e.g. "static/app.js"). Root-absolute
// URLs ("/static/app.js") still bypass the proxy prefix — those apps need to be
// reached via a sub-domain-style proxy instead.
func injectBaseTag(body []byte, baseHref string) []byte {
	lower := bytes.ToLower(body)
	if bytes.Contains(lower, []byte("<base")) {
		return body // respect the app's own <base>
	}
	// html.EscapeString keeps the attribute value from breaking out of the
	// double quotes even if hostID somehow carried markup metacharacters.
	tag := []byte(`<base href="` + html.EscapeString(baseHref) + `">`)
	insertAfterTag := func(name string) ([]byte, bool) {
		i := bytes.Index(lower, []byte(name))
		if i < 0 {
			return body, false
		}
		end := bytes.IndexByte(body[i:], '>')
		if end < 0 {
			return body, false
		}
		pos := i + end + 1
		out := make([]byte, 0, len(body)+len(tag))
		out = append(out, body[:pos]...)
		out = append(out, tag...)
		out = append(out, body[pos:]...)
		return out, true
	}
	if out, ok := insertAfterTag("<head"); ok {
		return out
	}
	if out, ok := insertAfterTag("<html"); ok {
		return out
	}
	return body
}

func (s *Server) handleHTTPProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !s.cfg.ForwardEnabled() {
		http.Error(w, Tr(r, "forward.disabled"), http.StatusForbidden)
		return
	}
	hostID := r.PathValue("hostID")
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	portStr := r.PathValue("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, Tr(r, "forward.invalid_port"), http.StatusBadRequest)
		return
	}
	// 停用即失效：若该 host+port 存在已保存的 HTTP 转发配置且全部处于停用状态，则拒绝访问。
	// /proxy 路由本身按 host+port 直通，此前只受全局开关约束，导致停用单条配置后仍可访问（本次修复点）。
	if cfgs := s.cfg.ListHTTPProxies(); len(cfgs) > 0 {
		match, anyEnabled := false, false
		for _, p := range cfgs {
			if p.HostID == hostID && p.TargetPort == port {
				match = true
				if p.Enabled {
					anyEnabled = true
				}
			}
		}
		if match && !anyEnabled {
			http.Error(w, Tr(r, "forward.disabled"), http.StatusForbidden)
			return
		}
	}
	hostname := s.hostLabelForID(hostID)
	operator, clientIP := s.actorIP(r)

	// P2: WebSocket upgrade detection — tunnel as raw TCP instead of HTTP
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.handleWSProxy(w, r, hostID, hostname, port, operator)
		return
	}

	sess, err := s.forward.createSession("", hostID, hostname, port, "http", operator)
	if err != nil {
		http.Error(w, Tr(r, "forward.too_many_sessions"), http.StatusServiceUnavailable)
		return
	}
	defer s.forward.removeSession(sess.id)
	defer sess.close()

	// notify agent
	if !s.forward.notifyAgent(hostID, forwardWaitInfo{sessionID: sess.id, targetPort: port, mode: "http"}) {
		s.forward.stats.incError()
		msg := agentOfflineReason(s.store, hostID)
		slog.Warn(Tz("log.forward_parse_failed_short"), "host", hostname, "hostID", hostID, "port", port, "path", r.URL.Path, "reason", msg)
		http.Error(w, Tr(r, "forward.agent_offline")+": "+msg, http.StatusBadGateway)
		return
	}
	// wait for agent to attach
	select {
	case <-sess.agentUp:
	case <-time.After(10 * time.Second):
		s.forward.stats.incError()
		http.Error(w, Tr(r, "forward.agent_timeout"), http.StatusGatewayTimeout)
		return
	case <-sess.done:
		s.forward.stats.incError()
		http.Error(w, Tr(r, "forward.session_closed"), http.StatusBadGateway)
		return
	}

	// *** CRITICAL: Start the response reader IMMEDIATELY after agentUp,
	// BEFORE sending request frames. If the Agent failed to connect to the
	// target, it already sent error data via the tx POST. The reader must
	// be running to capture that data before the session ends. ***
	// 不再使用 io.Pipe（关闭时机易与 http.ReadResponse 产生竞态导致
	// unexpected EOF），改为由一个 reader 直接从 sess.toUser 通道读取
	// agent 回传的全部响应字节，并同步镜像前 maxDiagBytes 用于诊断日志。
	var rawResponseBuf bytes.Buffer
	var rawResponseMu sync.Mutex
	const maxDiagBytes = 2048 // 仅记录前 2KB 用于诊断
	respReader := &rawForwardReader{
		ch:       sess.toUser,
		done:     sess.done,
		diag:     &rawResponseBuf,
		diagMu:   &rawResponseMu,
		diagLeft: maxDiagBytes,
	}

	// construct raw HTTP request bytes
	// ====== v5.2.9: CRITICAL FIX — request construction order ======
	// Previously the body was io.Copy'd into reqBuf BEFORE the Content-Length
	// header and final CRLF, which meant the target server would see body bytes
	// interleaved with headers — a protocol violation causing "bad request"
	// errors or silently dropped requests.
	//
	// Fix: read body into a separate buffer first, then construct the request
	// in the correct order: request-line → headers (including Content-Length)
	// → CRLF → body.
	path := "/" + r.PathValue("path")
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}

	// P0: read body first into a separate buffer (prevent OOM with limit)
	var bodyBytes []byte
	if r.Body != nil {
		limitedBody := io.LimitReader(r.Body, maxForwardBodySize)
		bodyBytes, _ = io.ReadAll(limitedBody)
		s.forward.stats.addBytes(int64(len(bodyBytes)))
	}

	var reqBuf bytes.Buffer
	fmt.Fprintf(&reqBuf, "%s %s HTTP/1.1\r\n", r.Method, path)
	// set Host header to the target
	fmt.Fprintf(&reqBuf, "Host: localhost:%d\r\n", port)
	// Add Connection: close to force the target to close the connection after
	// responding. Without this, HTTP/1.1 keep-alive would keep the TCP connection
	// open, causing the agent's tx goroutine (which reads from the conn as a POST
	// body) to hang indefinitely waiting for EOF.
	fmt.Fprintf(&reqBuf, "Connection: close\r\n")
	// v5.2.9: add Content-Length BEFORE the body (protocol-correct order).
	// Also skip the original Content-Length header to avoid duplicates.
	fmt.Fprintf(&reqBuf, "Content-Length: %d\r\n", len(bodyBytes))
	// P2: copy all headers EXCEPT hop-by-hop, Host, and Content-Length
	// (v5.2.5: skip Host; v5.2.9: skip Content-Length — we already set it)
	for k, vs := range r.Header {
		if hopByHopHeaders[k] || strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			fmt.Fprintf(&reqBuf, "%s: %s\r\n", k, v)
		}
	}
	// add forwarding headers
	fmt.Fprintf(&reqBuf, "X-Forwarded-For: %s\r\n", s.clientIP(r))
	fmt.Fprintf(&reqBuf, "X-Forwarded-Proto: %s\r\n", schemeOf(r))
	fmt.Fprintf(&reqBuf, "X-Real-IP: %s\r\n", s.clientIP(r))
	// End of headers
	reqBuf.WriteString("\r\n")
	// Body (after the header-body separator)
	if len(bodyBytes) > 0 {
		reqBuf.Write(bodyBytes)
	}

	// send the request through the tunnel in chunks.
	// If the Agent closed the session (e.g. because it failed to connect to the
	// target and already sent error data), jump to response reading instead of
	// returning — the pipe reader already has the Agent's error response.
	data := reqBuf.Bytes()
	for len(data) > 0 {
		chunk := data
		if len(chunk) > 0xffff {
			chunk = chunk[:0xffff]
		}
		sess.touch()
		select {
		case sess.toAgent <- forwardFrame('d', chunk):
		case <-sess.done:
			// Agent disconnected before we could send the full request.
			// Don't return — the pipe reader may already have error data.
			goto readResponse
		}
		data = data[len(chunk):]
	}
	// signal end of request
	select {
	case sess.toAgent <- forwardFrame('c', nil):
	case <-sess.done:
		// Agent disconnected; proceed to read whatever is available
	}

readResponse:
	// 彻底修复：不再使用 bufio.NewReader(pr) + http.ReadResponse，
	// 那样在 io.Pipe 关闭时极易抛出 "unexpected EOF"（被包装为 502）。
	// 改为：先把 agent 回传的全部响应字节完整读出来（带上限），
	// 再用 http.ReadResponse(bytes.NewReader(raw), r) 解析。
	// 关键改进：
	//   1) 传入原始请求 r，让 ReadResponse 正确判断 HEAD/CONNECT 等无 body 场景；
	//   2) 先 io.ReadAll 再解析，避免 pipe/缓冲竞态造成的半截响应；
	//   3) 解析失败时把原始字节作为兜底响应返回，而不是返回笼统的 502。
	type readResult struct {
		raw []byte
		err error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		// P0: 防止恶意超大响应撑爆内存（50MB 上限）
		const maxRespBytes = 50 << 20
		limited := io.LimitReader(respReader, maxRespBytes)
		raw, e := io.ReadAll(limited)
		resultCh <- readResult{raw, e}
	}()

	var rawResp []byte
	select {
	case res := <-resultCh:
		rawResp, err = res.raw, res.err
	case <-time.After(forwardReadTimeout):
		s.forward.stats.incError()
		rawResponseMu.Lock()
		rawPreview := truncateStr(rawResponseBuf.String(), 300)
		rawResponseMu.Unlock()
		slog.Warn(Tz("log.forward_parse_failed_short"), "host", hostname, "port", port, "path", path,
			"err", "read timeout", "raw_preview", rawPreview, "timeout", forwardReadTimeout)
		http.Error(w, Tr(r, "forward.agent_timeout"), http.StatusGatewayTimeout)
		return
	}

	if err != nil {
		s.forward.stats.incError()
		rawResponseMu.Lock()
		rawPreview := truncateStr(rawResponseBuf.String(), 300)
		rawResponseMu.Unlock()
		slog.Warn(Tz("log.forward_parse_failed_short"), "host", hostname, "port", port, "path", path,
			"err", err.Error(), "raw_len", len(rawResp), "raw_preview", rawPreview)
		http.Error(w, Tr(r, "forward.parse_response_failed", "读取上游响应失败: "+err.Error()), http.StatusBadGateway)
		return
	}

	// 尝试按 HTTP 响应解析
	resp, parseErr := http.ReadResponse(bufio.NewReader(bytes.NewReader(rawResp)), r)
	if parseErr == nil {
		defer resp.Body.Close()

		// HTML rewrite: inject a <base> so a path-proxied app's relative asset
		// URLs resolve through /proxy/{host}/{port}/ rather than the monitor's
		// own root. Requires the full body (HEAD/empty bodies fall through to the
		// verbatim relay so their original Content-Length is preserved).
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
			body, rerr := io.ReadAll(resp.Body)
			if rerr == nil && len(body) > 0 {
				enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
				decoded, canRewrite := body, enc == ""
				if enc == "gzip" {
					if gz, gerr := gzip.NewReader(bytes.NewReader(body)); gerr == nil {
						if d, derr := io.ReadAll(gz); derr == nil {
							decoded, canRewrite = d, true // will re-serve uncompressed
						}
						gz.Close()
					}
				}
				if canRewrite {
					decoded = injectBaseTag(decoded, "/proxy/"+hostID+"/"+strconv.Itoa(port)+"/")
					for k, vs := range resp.Header {
						// body length changed and it is now uncompressed, so drop
						// the upstream Content-Length / Content-Encoding.
						if hopByHopHeaders[k] || k == "Content-Length" || k == "Content-Encoding" {
							continue
						}
						for _, v := range vs {
							w.Header().Add(k, v)
						}
					}
					w.Header().Set("Content-Length", strconv.Itoa(len(decoded)))
					w.WriteHeader(resp.StatusCode)
					n, _ := w.Write(decoded)
					s.forward.stats.addBytes(int64(n))
					s.forward.stats.addLatency(time.Since(start).Milliseconds())
					s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: operator, IP: clientIP, Host: hostname,
						Message: Tz("log.forward_http", hostname, port, r.Method, path, resp.StatusCode)})
					return
				}
				// Encoding we can't decode (br/deflate): relay verbatim, no rewrite.
				for k, vs := range resp.Header {
					if hopByHopHeaders[k] {
						continue
					}
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
				w.WriteHeader(resp.StatusCode)
				n, _ := w.Write(body)
				s.forward.stats.addBytes(int64(n))
				s.forward.stats.addLatency(time.Since(start).Milliseconds())
				s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: operator, IP: clientIP, Host: hostname,
					Message: Tz("log.forward_http", hostname, port, r.Method, path, resp.StatusCode)})
				return
			}
			// empty/HEAD body or read error: fall through to verbatim relay.
		}

		for k, vs := range resp.Header {
			if hopByHopHeaders[k] {
				continue // P2: strip hop-by-hop from response too
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		n, _ := io.Copy(w, resp.Body)
		s.forward.stats.addBytes(n)
		s.forward.stats.addLatency(time.Since(start).Milliseconds())
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: operator, IP: clientIP, Host: hostname,
			Message: Tz("log.forward_http", hostname, port, r.Method, path, resp.StatusCode)})
		return
	}

	// 解析失败兜底：把 agent 回传的原始字节原样返回给浏览器。
	// 这样当目标是非 HTTP 服务（如返回纯文本错误、或 TLS 握手失败）时，
	// 用户至少能看到真实内容，而不是笼统的 502。
	s.forward.stats.incError()
	// 诊断：输出 rawResp 前 500 字符用于定位 unexpected EOF 等解析失败原因
	rawPreview := truncateStr(string(rawResp), 500)
	slog.Warn(Tz("log.forward_parse_failed_short"), "host", hostname, "port", port, "path", path,
		"err", parseErr.Error(), "raw_len", len(rawResp), "raw_preview", rawPreview)
	s.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: operator, IP: clientIP, Host: hostname,
		Message: Tz("log.forward_parse_failed", port, path, parseErr.Error())})
	if len(rawResp) == 0 {
		http.Error(w, Tr(r, "forward.parse_response_failed", parseErr.Error()), http.StatusBadGateway)
		return
	}
	// 原始内容可能是 agent 自己生成的 HTTP 502 错误页（目标不可达），
	// 尝试识别并返回对应状态码。
	statusCode := http.StatusOK
	contentType := "text/plain; charset=utf-8"
	if bytes.HasPrefix(rawResp, []byte("HTTP/")) {
		if idx := bytes.Index(rawResp, []byte("\r\n")); idx > 0 {
			parts := strings.Fields(string(rawResp[:idx]))
			if len(parts) >= 2 {
				if sc, e := strconv.Atoi(parts[1]); e == nil {
					statusCode = sc
				}
			}
		}
		// Try to extract Content-Type from raw HTTP response
		if ctIdx := bytes.Index(rawResp, []byte("Content-Type:")); ctIdx >= 0 {
			lineEnd := bytes.Index(rawResp[ctIdx:], []byte("\r\n"))
			if lineEnd > 0 {
				contentType = strings.TrimSpace(string(rawResp[ctIdx+13 : ctIdx+lineEnd]))
			}
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Del("Content-Length")
	w.WriteHeader(statusCode)
	_, _ = w.Write(rawResp)
}

// handleWSProxy tunnels a WebSocket upgrade request through the agent.
// P2: WebSocket passthrough support.
func (s *Server) handleWSProxy(w http.ResponseWriter, r *http.Request, hostID, hostname string, port int, operator string) {
	sess, err := s.forward.createSession("", hostID, hostname, port, "tcp", operator)
	if err != nil {
		http.Error(w, Tr(r, "forward.too_many_sessions"), http.StatusServiceUnavailable)
		return
	}
	defer s.forward.removeSession(sess.id)
	defer sess.close()

	if !s.forward.notifyAgent(hostID, forwardWaitInfo{sessionID: sess.id, targetPort: port, mode: "tcp"}) {
		s.forward.stats.incError()
		msg := agentOfflineReason(s.store, hostID)
		slog.Warn(Tz("log.forward_agent_offline"), "host", hostname, "hostID", hostID, "port", port, "reason", msg)
		http.Error(w, Tr(r, "forward.agent_offline")+": "+msg, http.StatusBadGateway)
		return
	}
	select {
	case <-sess.agentUp:
	case <-time.After(10 * time.Second):
		s.forward.stats.incError()
		http.Error(w, Tr(r, "forward.agent_timeout"), http.StatusGatewayTimeout)
		return
	case <-sess.done:
		http.Error(w, Tr(r, "forward.session_closed"), http.StatusBadGateway)
		return
	}

	// Construct the WebSocket upgrade request as raw HTTP
	var reqBuf bytes.Buffer
	path := "/" + r.PathValue("path")
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	fmt.Fprintf(&reqBuf, "%s %s HTTP/1.1\r\n", r.Method, path)
	fmt.Fprintf(&reqBuf, "Host: localhost:%d\r\n", port)
	for k, vs := range r.Header {
		// Forward all headers including Upgrade, Connection, Sec-WebSocket-*
		for _, v := range vs {
			fmt.Fprintf(&reqBuf, "%s: %s\r\n", k, v)
		}
	}
	reqBuf.WriteString("\r\n")

	// Send the upgrade request through the tunnel
	data := reqBuf.Bytes()
	for len(data) > 0 {
		chunk := data
		if len(chunk) > 0xffff {
			chunk = chunk[:0xffff]
		}
		sess.touch()
		select {
		case sess.toAgent <- forwardFrame('d', chunk):
		case <-sess.done:
			return
		}
		data = data[len(chunk):]
	}

	// Hijack the HTTP connection to get a raw bidirectional stream
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "WebSocket hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()
	if clientBuf != nil {
		// flush any buffered data
		if clientBuf.Reader.Buffered() > 0 {
			extra := make([]byte, clientBuf.Reader.Buffered())
			clientBuf.Read(extra)
			select {
			case sess.toAgent <- forwardFrame('d', extra):
			case <-sess.done:
			}
		}
	}

	// Bidirectional relay: client → agent and agent → client
	done := make(chan struct{}, 2)

	// client → agent
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, forwardReadBufSize)
		for {
			n, err := clientConn.Read(buf)
			if n > 0 {
				sess.touch()
				b := make([]byte, n)
				copy(b, buf[:n])
				select {
				case sess.toAgent <- forwardFrame('d', b):
				case <-sess.done:
					return
				}
			}
			if err != nil {
				select {
				case sess.toAgent <- forwardFrame('c', nil):
				case <-sess.done:
				}
				return
			}
		}
	}()

	// agent → client
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			select {
			case b := <-sess.toUser:
				sess.touch()
				if _, err := clientConn.Write(b); err != nil {
					return
				}
			case <-sess.done:
				return
			}
		}
	}()

	<-done
	sess.close()
}

// schemeOf returns the request scheme (http or https).
func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// ---- Agent-facing handlers (fingerprint-gated, not session-gated) ----

// handleAgentForwardWait: agent long-polls here; returns a session id + target
// port when a user opens a forward connection for this host, or {} on timeout.
func (s *Server) handleAgentForwardWait(w http.ResponseWriter, r *http.Request) {
	if !s.forwardFingerprintOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	host := r.URL.Query().Get("host")
	if host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.host_required")})
		return
	}
	// v5.2.5: Check pending sessions first (same pattern as terminal)
	s.forward.mu.Lock()
	if pending := s.forward.pendingSessions[host]; len(pending) > 0 {
		info := pending[0]
		if len(pending) == 1 {
			delete(s.forward.pendingSessions, host)
		} else {
			s.forward.pendingSessions[host] = pending[1:]
		}
		s.forward.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"session":       info.sessionID,
			"target_port":   info.targetPort,
			"mode":          info.mode,
			"remote_target": info.remoteTarget,
		})
		return
	}
	s.forward.mu.Unlock()
	// No pending — register waiter for long-poll
	ch := s.forward.registerWaiter(host)
	defer s.forward.unregisterWaiter(host, ch)
	select {
	case info := <-ch:
		writeJSON(w, http.StatusOK, map[string]any{
			"session":       info.sessionID,
			"target_port":   info.targetPort,
			"mode":          info.mode,
			"remote_target": info.remoteTarget,
		})
	case <-time.After(25 * time.Second):
		writeJSON(w, http.StatusOK, map[string]string{})
	case <-r.Context().Done():
	}
}

// handleAgentForwardRx streams user data down to the agent (chunked).
func (s *Server) handleAgentForwardRx(w http.ResponseWriter, r *http.Request) {
	sess := s.forward.getSession(r.URL.Query().Get("session"))
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.session_gone")})
		return
	}
	if !s.forwardFingerprintOKByHost(sess.hostID, agentFP(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	for {
		select {
		case b := <-sess.toAgent:
			if _, err := w.Write(b); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-sess.done:
			// v5.2.5: drain remaining frames from toAgent after session close
			// to prevent data loss from select race (same pattern as rawForwardReader)
			for {
				select {
				case b := <-sess.toAgent:
					if _, err := w.Write(b); err != nil {
						return
					}
					if flusher != nil {
						flusher.Flush()
					}
				default:
					return
				}
			}
		case <-r.Context().Done():
			// The agent closed the rx stream. In HTTP mode this is NORMAL: it
			// happens right after the full request ('c' frame) is delivered, while
			// the response is still streaming back on tx. Closing the session here
			// would race-kill the response path (raw_len=0 → "unexpected EOF" 502).
			// So just stop relaying — the session close is driven by tx completion
			// (HTTP) or the user/target side (TCP), with the idle checker as backstop.
			return
		}
	}
}

// handleAgentForwardTx receives data from the agent (chunked request body)
// and fans it to the user connection.
func (s *Server) handleAgentForwardTx(w http.ResponseWriter, r *http.Request) {
	sess := s.forward.getSession(r.URL.Query().Get("session"))
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.session_gone")})
		return
	}
	if !s.forwardFingerprintOKByHost(sess.hostID, agentFP(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	sess.markAgentUp()
	defer sess.close()
	if sess.mode == "udp" { // UDP：agent 上行按帧封装数据报，逐帧还原后投递给对应 UDP 流
		readForwardTxFrames(r.Body, sess)
		return
	}
	buf := make([]byte, forwardReadBufSize) // P1: 32KB buffer
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			b := make([]byte, n)
			copy(b, buf[:n])
			s.forward.stats.addBytes(int64(n))
			select {
			case sess.toUser <- b:
			case <-sess.done:
				// v5.2.9: CRITICAL FIX — send the current chunk b BEFORE draining.
				// Previously the chunk b was discarded when sess.done was selected,
				// and only a new read from r.Body was drained. This caused data loss
				// (typically the last response chunk), resulting in truncated HTTP
				// responses → "unexpected EOF" / 502 errors.
				//
				// Fix: send b first (non-blocking — if toUser is full, the session
				// is already ending and the reader will drain), then drain remaining
				// body data.
				select {
				case sess.toUser <- b:
				default:
				}
				// Drain remaining body data to toUser
				for {
					n2, err2 := r.Body.Read(buf)
					if n2 > 0 {
						b2 := make([]byte, n2)
						copy(b2, buf[:n2])
						s.forward.stats.addBytes(int64(n2))
						select {
						case sess.toUser <- b2:
						default:
						}
					}
					if err2 != nil {
						break
					}
				}
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// agentFP extracts the agent fingerprint (the report/forward/terminal auth
// credential) from a request. New agents send it in the X-Agent-Fingerprint
// header to keep it out of access/reverse-proxy logs; older agents send it as
// the ?fp= query param, which we still accept for backward compatibility.
func agentFP(r *http.Request) string {
	if h := r.Header.Get("X-Agent-Fingerprint"); h != "" {
		return h
	}
	return r.URL.Query().Get("fp")
}

// forwardFingerprintOKByHost verifies the agent-presented fingerprint against
// the fingerprint bound to hostID at registration (constant-time).
func (s *Server) forwardFingerprintOKByHost(hostID, fp string) bool {
	if hostID == "" || fp == "" {
		return false
	}
	host, ok := s.store.GetHost(hostID)
	if !ok || host.Fingerprint == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(fp), []byte(host.Fingerprint)) == 1
}

// forwardFingerprintOK is the request-flavored wrapper for handleAgentForwardWait.
func (s *Server) forwardFingerprintOK(r *http.Request) bool {
	return s.forwardFingerprintOKByHost(r.URL.Query().Get("host"), agentFP(r))
}
