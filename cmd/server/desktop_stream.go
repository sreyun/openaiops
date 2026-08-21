package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Web remote desktop: browser WebSocket ↔ agent reverse channel (screen stream + input + files).
// Independent of TCP port-forward / local RDP-VNC clients.

const (
	deskIdleTimeout = 2 * time.Hour
	deskHardTimeout = 8 * time.Hour
)

type deskSession struct {
	id       string
	hostID   string
	hostname string
	operator string
	ip       string
	lang     string

	changeID   int64 // linked approved change (audit glue)
	incidentID int64 // linked incident loop (audit glue)

	toAgent   chan []byte
	toBrowser chan []byte
	agentUp   chan struct{}
	done      chan struct{}
	upOnce    sync.Once
	doneOnce  sync.Once

	lastActive atomic.Int64 // unix nano; idle timeout
	rearms     atomic.Int32 // Agent 侧断线后重新接管的次数（见 rearmAgent）
	txLive     atomic.Int32 // 当前挂着的 Agent tx 流数量
	createdAt  int64
	recording  []deskRecordFrame
	recMu      sync.Mutex
}

func (s *deskSession) markAgentUp() { s.upOnce.Do(func() { close(s.agentUp) }) }

// txLive 记录当前是否有 Agent 的 tx 流挂着。重新接管的判定要看它，
// 不能看 agentUp —— 那是个只关一次的信号，第一个 worker 上来之后就永远是"已连接"。
func (s *deskSession) setAgentAttached(v bool) {
	if v {
		s.txLive.Add(1)
		return
	}
	s.txLive.Add(-1)
}
func (s *deskSession) agentAttached() bool { return s.txLive.Load() > 0 }
func (s *deskSession) close() {
	s.doneOnce.Do(func() { close(s.done) })
}
func (s *deskSession) touch() { s.lastActive.Store(time.Now().UnixNano()) }

type deskManager struct {
	mu              sync.Mutex
	sessions        map[string]*deskSession
	waiters         map[string]chan string
	pendingSessions map[string][]string
	lastWaitAt      map[string]time.Time
	archived        []deskArchive
	recDir          string
}

func newDeskManager() *deskManager {
	return &deskManager{
		sessions:        map[string]*deskSession{},
		waiters:         map[string]chan string{},
		pendingSessions: map[string][]string{},
		lastWaitAt:      map[string]time.Time{},
	}
}

func deskID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func deskFrame(typ byte, payload []byte) []byte {
	if len(payload) > 0xffff {
		payload = payload[:0xffff]
	}
	b := make([]byte, 3+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint16(b[1:], uint16(len(payload)))
	copy(b[3:], payload)
	return b
}

func (m *deskManager) create(hostID, hostname, operator, ip, lang string) *deskSession {
	s := &deskSession{
		id: deskID(), hostID: hostID, hostname: hostname, operator: operator, ip: ip, lang: lang,
		toAgent: make(chan []byte, 128), toBrowser: make(chan []byte, 256),
		agentUp: make(chan struct{}), done: make(chan struct{}),
		createdAt: time.Now().Unix(),
	}
	s.touch()
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	return s
}

func (m *deskManager) get(id string) *deskSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *deskManager) remove(id string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	if ok {
		if q := m.pendingSessions[s.hostID]; len(q) > 0 {
			kept := make([]string, 0, len(q))
			for _, sid := range q {
				if sid != id {
					kept = append(kept, sid)
				}
			}
			if len(kept) == 0 {
				delete(m.pendingSessions, s.hostID)
			} else {
				m.pendingSessions[s.hostID] = kept
			}
		}
	}
	m.mu.Unlock()
	if ok {
		m.archiveSession(s)
		s.close()
	}
}

func (m *deskManager) notifyAgent(hostID, sessionID string) (ok bool, alive bool) {
	m.mu.Lock()
	w := m.waiters[hostID]
	delete(m.waiters, hostID)
	last := m.lastWaitAt[hostID]
	alive = w != nil || (!last.IsZero() && time.Since(last) < 90*time.Second)
	if w == nil {
		m.pendingSessions[hostID] = append(m.pendingSessions[hostID], sessionID)
		m.mu.Unlock()
		return true, alive
	}
	m.mu.Unlock()
	select {
	case w <- sessionID:
		return true, true
	default:
		m.mu.Lock()
		m.pendingSessions[hostID] = append(m.pendingSessions[hostID], sessionID)
		m.mu.Unlock()
		return true, alive
	}
}

// deskMaxRearms 限制一次会话里允许 Agent 侧重新接管多少次。
// Windows 上登录/注销/切换用户会让服务重新派生桌面 worker，一次会话经历两三次很正常；
// 但如果 worker 起来就崩、崩了又起，无限重试只会让用户对着"正在恢复"干等。
const deskMaxRearms = 6

// deskRearmWait 是等新 worker 接管的时间上限。服务里的督导循环 2 秒一轮，
// 加上派生进程 + 长轮询接单，正常情况下几秒内就能回来。
const deskRearmWait = 45 * time.Second

// rearmAgent 在 **Agent 侧断了、浏览器还连着** 时把会话重新挂回待接管队列。
//
// 这修的是现场那句"Windows 登录窗口输完用户名密码后要重新进一次才能看到画面"：
// 登录会改变活动会话，SYSTEM 服务据此杀掉旧的桌面 worker、在新会话里派生一个新的。
// 旧 worker 的 tx 一断，服务端原来直接把整个会话关掉 —— 浏览器那边就是"已断开"，
// 只能关掉重开。可这时候浏览器明明还在，新 worker 也马上就会来接单。
//
// 现在改成：留住会话、告诉浏览器"正在恢复"、把 session 重新排进待接管队列。
// 新 worker 通过 deskWait 拿到**同一个 session id**，接着推流即可；超时才真正收摊。
func (m *deskManager) rearmAgent(sess *deskSession) bool {
	if sess == nil {
		return false
	}
	select {
	case <-sess.done:
		return false // 浏览器早就走了，没人可等
	default:
	}
	if sess.rearms.Add(1) > deskMaxRearms {
		return false
	}
	select {
	case sess.toBrowser <- append([]byte{'S'}, mustJSON(map[string]any{"phase": "agent_reconnecting"})...):
	default:
	}
	m.notifyAgent(sess.hostID, sess.id)
	go func() {
		t := time.NewTimer(deskRearmWait)
		defer t.Stop()
		select {
		case <-t.C:
			select {
			case <-sess.done:
				return
			default:
			}
			if sess.agentAttached() {
				return // 新 worker 已经接上了
			}
			select {
			case sess.toBrowser <- append([]byte{'E'}, mustJSON(map[string]string{"error": Tz("desktop.timeout")})...):
			default:
			}
			time.Sleep(200 * time.Millisecond)
			sess.close()
		case <-sess.done:
		}
	}()
	return true
}

func (m *deskManager) registerWaiter(hostID string) chan string {
	ch := make(chan string, 1)
	m.mu.Lock()
	m.waiters[hostID] = ch
	m.lastWaitAt[hostID] = time.Now()
	m.mu.Unlock()
	return ch
}

func (m *deskManager) unregisterWaiter(hostID string, ch chan string) {
	m.mu.Lock()
	if m.waiters[hostID] == ch {
		delete(m.waiters, hostID)
	}
	m.mu.Unlock()
}

// handleOpenDesktop preflight: secondary auth + host online; browser then opens WS.
// POST /api/v1/hosts/{id}/desktop
func (s *Server) handleOpenDesktop(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.TerminalEnabled() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "desktop.disabled")})
		return
	}
	hostID := r.PathValue("id")
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	if !s.enforceRemoteGate(w, r, hostID, false) {
		return
	}
	verified, hasPassword := s.auth.isTerminalVerified(r)
	if !verified {
		code := "terminal_verify_required"
		if !hasPassword {
			code = "terminal_password_not_set"
		}
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": Tr(r, "terminal_auth."+code),
			"code":  code,
		})
		return
	}
	h := s.hostByID(hostID)
	if h == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.host_not_found")})
		return
	}
	offlineSec := int64(s.cfg.Thresholds().OfflineAfter.Seconds())
	if time.Now().Unix()-h.LastSeen > offlineSec {
		writeJSON(w, http.StatusConflict, map[string]string{"error": Tr(r, "desktop.host_offline")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":       "web",
		"ws_path":    "/api/v1/hosts/" + hostID + "/desktop/ws",
		"host_id":    hostID,
		"hostname":   h.Hostname,
		"os":         h.OS,
		"platform":   h.Platform,
		"supported":  true,
		"file_xfer":  true,
		"idle_hours": int(deskIdleTimeout.Hours()),
	})
}

// handleDesktopWS upgrades to WebSocket and relays screen/input/file frames.
// GET /api/v1/hosts/{id}/desktop/ws
//
// Session lifetime mirrors the terminal channel:
//   - browser disconnect closes the session
//   - agent TX end closes after a short drain so the last error/meta frames
//     still reach the browser (avoids "已断开" wiping the real cause)
//   - idle / hard timeout / agent-wait timeout also close
func (s *Server) handleDesktopWS(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.TerminalEnabled() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "desktop.disabled")})
		return
	}
	hostID := r.PathValue("id")
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	// Re-check gate on WS upgrade so a stale POST open cannot bypass later.
	if !s.enforceRemoteGate(w, r, hostID, false) {
		return
	}
	verified, hasPassword := s.auth.isTerminalVerified(r)
	if !verified {
		code := "terminal_verify_required"
		if !hasPassword {
			code = "terminal_password_not_set"
		}
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": Tr(r, "terminal_auth."+code),
			"code":  code,
		})
		return
	}
	h := s.hostByID(hostID)
	if h == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.host_not_found")})
		return
	}
	ws, err := wsAccept(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "terminal.ws_required")})
		return
	}
	defer ws.Close()

	operator, clientIP := s.actorIP(r)
	sess := s.desk.create(hostID, h.Hostname, operator, clientIP, langFromRequest(r))
	sess.changeID, sess.incidentID = s.remoteSessionLinks(hostID)
	defer s.desk.remove(sess.id)
	msg := Tz("log.open_desktop", h.Hostname)
	if sess.changeID > 0 || sess.incidentID > 0 {
		msg += fmt.Sprintf(" [change_id=%d incident_id=%d]", sess.changeID, sess.incidentID)
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: operator, IP: clientIP, Host: h.Hostname, Message: msg})
	defer s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: operator, IP: clientIP, Host: h.Hostname, Message: Tz("log.close_desktop", h.Hostname)})

	if _, alive := s.desk.notifyAgent(hostID, sess.id); !alive {
		_ = ws.WriteBinary(append([]byte{'E'}, mustJSON(map[string]string{"error": Tz("desktop.no_channel")})...))
		return
	}
	// Queue "waiting" before any agent frames so the UI does not flash "connected".
	select {
	case sess.toBrowser <- append([]byte{'S'}, mustJSON(map[string]any{"phase": "waiting_agent", "w": 1280, "h": 720})...):
	default:
	}
	var gotVideo atomic.Bool
	go func() {
		select {
		case <-sess.agentUp:
			select {
			case sess.toBrowser <- append([]byte{'S'}, mustJSON(map[string]any{"phase": "agent_up"})...):
			case <-sess.done:
				return
			}
			// The agent's tx stream is up, but if no video frame arrives shortly the
			// UI would spin on "正在推送画面" forever. Surface an actionable
			// diagnostic instead of an infinite spinner.
			select {
			case <-time.After(15 * time.Second):
				if !gotVideo.Load() {
					select {
					case sess.toBrowser <- append([]byte{'E'}, mustJSON(map[string]string{
						"error": Tz("desktop.no_frame"),
						"level": "warn",
					})...):
					case <-sess.done:
					}
				}
			case <-sess.done:
			}
		case <-time.After(35 * time.Second):
			select {
			case sess.toBrowser <- append([]byte{'E'}, mustJSON(map[string]string{
				"error": Tz("desktop.timeout") + "\n" + Tz("desktop.timeout_hint_https"),
			})...):
			case <-sess.done:
			}
			// Let the writer flush the error frame before tearing the socket down.
			time.Sleep(300 * time.Millisecond)
			sess.close()
		case <-sess.done:
		}
	}()

	// Idle / hard timeout watchdog
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		deadline := time.Now().Add(deskHardTimeout)
		for {
			select {
			case <-t.C:
				last := time.Unix(0, sess.lastActive.Load())
				if time.Since(last) > deskIdleTimeout || time.Now().After(deadline) {
					select {
					case sess.toBrowser <- append([]byte{'E'}, mustJSON(map[string]string{"error": Tz("desktop.timeout")})...):
					default:
					}
					time.Sleep(200 * time.Millisecond)
					sess.close()
					return
				}
			case <-sess.done:
				return
			}
		}
	}()

	// browser → agent
	go func() {
		defer sess.close()
		for {
			data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if len(data) == 0 {
				continue
			}
			sess.touch()
			typ := data[0]
			payload := data[1:]
			switch typ {
			case 'P': // app-level ping → pong
				_ = ws.WriteBinary([]byte{'P'})
				continue
			case 'A':
				// Lock-screen actions (CAD / chords / type_text). Audit type_text
				// without logging the plaintext credential.
				if auditDeskAction(s, operator, clientIP, h.Hostname, payload) {
					// framed below
				} else {
					continue
				}
			case 'M', 'W', 'B', 'Q', 'N', 'C', 'f', 'u', 'e', 'd':
				// 上行方向也要防积压：链路一慢，60Hz 的鼠标移动就会在队列里排成一串，
				// 于是"松手之后光标还在慢慢挪"。中间那些移动点没有信息价值——真正的
				// 远程桌面同样只保最新位置。按下/抬起/滚轮/按键一个都不能丢。
				if typ == 'M' && len(sess.toAgent) > deskAgentMoveBacklog && deskIsMouseMove(payload) {
					continue
				}
				// framed to agent below
			default:
				continue
			}
			for {
				chunk := payload
				if len(chunk) > 0xffff {
					chunk = chunk[:0xffff]
				}
				select {
				case sess.toAgent <- deskFrame(typ, chunk):
				case <-sess.done:
					return
				}
				if len(payload) <= 0xffff {
					break
				}
				payload = payload[0xffff:]
			}
		}
	}()

	// agent → browser. Drain remaining frames when done so error/meta is not lost.
	go func() {
		write := func(b []byte) bool {
			sess.touch()
			if len(b) > 0 {
				switch b[0] {
				case 'S':
					sess.recordFrame("meta", b[1:])
				case 'K':
					gotVideo.Store(true)
					if time.Now().UnixMilli()%500 < 120 {
						sess.recordFrame("jpeg", b[1:])
					}
				case 'T':
					// 脏块差分帧。录像必须连着存：只存整帧的话，回放会退化成
					// 5 秒一张幻灯片（整帧现在只在关键帧时才发）。
					gotVideo.Store(true)
					sess.recordFrame("tiles", b[1:])
				case 'H':
					gotVideo.Store(true)
					sess.recordFrame("h264", b[1:])
				}
			}
			return ws.WriteBinary(b) == nil
		}
		for {
			select {
			case b := <-sess.toBrowser:
				if !write(b) {
					sess.close()
					return
				}
			case <-sess.done:
				for {
					select {
					case b := <-sess.toBrowser:
						_ = write(b)
					default:
						return
					}
				}
			}
		}
	}()

	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := ws.WritePing(nil); err != nil {
					sess.close()
					return
				}
			case <-sess.done:
				return
			}
		}
	}()
	<-sess.done
}

// deskAuditedActions 是允许通过的远程桌面动作。
//
// **这张表就是白名单本身**：不在表里的动作会被服务端静默丢掉。曾经因此出过事——
// 界面上的「解锁」按钮发的是 unlock，而这里只认 cad/chord/type_text/wake，于是点了
// 毫无反应、日志里也没有任何痕迹。加动作时两边必须一起改，
// TestDeskActionsFromUIAreAudited 会盯着 UI 里出现的每一个动作名。
var deskAuditedActions = map[string]bool{
	"cad": true, "chord": true, "type_text": true, "wake": true,
	"unlock": true, "paste": true,
	"set_resolution": true, "reset_resolution": true,
}

// auditDeskAction logs lock-screen control actions. Returns false if the frame
// should be dropped (invalid JSON / unknown action). Never logs credential text.
func auditDeskAction(s *Server, operator, clientIP, hostname string, payload []byte) bool {
	var req struct {
		Action string `json:"action"`
		Chord  string `json:"chord"`
		Text   string `json:"text"`
		User   string `json:"user"`
		Enter  bool   `json:"enter"`
		W      int    `json:"w"`
		H      int    `json:"h"`
	}
	if json.Unmarshal(payload, &req) != nil {
		return false
	}
	act := strings.ToLower(strings.TrimSpace(req.Action))
	if !deskAuditedActions[act] {
		return false
	}
	msg := "远程桌面动作 " + act
	switch act {
	case "chord":
		msg += " (" + req.Chord + ")"
	case "type_text", "paste":
		n := len([]rune(req.Text))
		msg += "（凭据文本已发送，长度=" + itoa(n)
		if req.Enter {
			msg += "+Enter"
		}
		msg += "，内容未记录）"
	case "unlock":
		msg += "（解锁凭据已发送"
		if u := strings.TrimSpace(req.User); u != "" {
			msg += "，用户名=" + u
		}
		msg += "，口令未记录）"
	case "set_resolution":
		if req.W > 0 && req.H > 0 {
			msg += fmt.Sprintf("（切换到 %d×%d）", req.W, req.H)
		} else {
			msg += "（匹配操作员窗口）"
		}
	}
	s.store.AddLog(LogEntry{
		Kind: KindOperation, Level: "warning", Actor: operator, IP: clientIP, Host: hostname,
		Message: msg,
	})
	return true
}

func (s *Server) handleAgentDeskWait(w http.ResponseWriter, r *http.Request) {
	if !s.termFingerprintOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	host := r.URL.Query().Get("host")
	if host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.host_required")})
		return
	}
	s.desk.mu.Lock()
	if pending := s.desk.pendingSessions[host]; len(pending) > 0 {
		var sid string
		rest := pending
		for len(rest) > 0 {
			cand := rest[0]
			rest = rest[1:]
			if _, live := s.desk.sessions[cand]; live {
				sid = cand
				break
			}
		}
		if len(rest) == 0 {
			delete(s.desk.pendingSessions, host)
		} else {
			s.desk.pendingSessions[host] = rest
		}
		if sid != "" {
			sess := s.desk.sessions[sid]
			s.desk.mu.Unlock()
			out := map[string]string{"session": sid}
			if sess != nil && sess.lang != "" {
				out["lang"] = sess.lang
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	s.desk.mu.Unlock()

	ch := s.desk.registerWaiter(host)
	defer s.desk.unregisterWaiter(host, ch)
	select {
	case sid := <-ch:
		if sess := s.desk.get(sid); sess != nil {
			out := map[string]string{"session": sid}
			if sess.lang != "" {
				out["lang"] = sess.lang
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{})
	case <-time.After(25 * time.Second):
		writeJSON(w, http.StatusOK, map[string]string{})
	case <-r.Context().Done():
	}
}

func (s *Server) handleAgentDeskRx(w http.ResponseWriter, r *http.Request) {
	sess := s.desk.get(r.URL.Query().Get("session"))
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.session_gone")})
		return
	}
	if !s.termFingerprintOKByHost(sess.hostID, agentFP(r)) {
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
			return
		case <-r.Context().Done():
			sess.close()
			return
		}
	}
}

// Agent tx frames: [type:1][len:4 BE][payload] — relayed to browser as [type][payload].
// Unlike a naive "defer sess.close()", we give the browser writer a short grace
// period to flush the last meta/error/jpeg frames. Otherwise a one-shot
// deskSendError TX races with sess.done and the UI only sees "已断开".
func (s *Server) handleAgentDeskTx(w http.ResponseWriter, r *http.Request) {
	sess := s.desk.get(r.URL.Query().Get("session"))
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.session_gone")})
		return
	}
	if !s.termFingerprintOKByHost(sess.hostID, agentFP(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	sess.markAgentUp()
	sess.setAgentAttached(true)
	defer func() {
		sess.setAgentAttached(false)
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if len(sess.toBrowser) == 0 {
				time.Sleep(50 * time.Millisecond)
				if len(sess.toBrowser) == 0 {
					break
				}
				continue
			}
			time.Sleep(20 * time.Millisecond)
		}
		// Agent 断了不等于会话结束：浏览器还在的话就等新 worker 接管（见 rearmAgent）。
		if s.desk.rearmAgent(sess) {
			return
		}
		sess.close()
	}()

	var hdr [5]byte
	for {
		if _, err := io.ReadFull(r.Body, hdr[:]); err != nil {
			return
		}
		typ := hdr[0]
		payloadLen := int(binary.BigEndian.Uint32(hdr[1:]))
		if payloadLen > 16<<20 {
			return
		}
		payload := make([]byte, payloadLen)
		if payloadLen > 0 {
			if _, err := io.ReadFull(r.Body, payload); err != nil {
				return
			}
		}
		out := make([]byte, 1+len(payload))
		out[0] = typ
		copy(out[1:], payload)
		if !sess.enqueueBrowser(out) {
			return
		}
	}
}

// deskAgentMoveBacklog：上行队列积压超过这么多帧时，丢弃新的"纯移动"事件。
// 队列本身有 128 的容量，32 已经是明显落后（60Hz 下约半秒）。
const deskAgentMoveBacklog = 32

// deskIsMouseMove 判断一个 'M' 载荷是不是纯移动（不含按下/抬起）。
// 只做字节匹配，不解 JSON：这条路径在每个鼠标事件上都会走一遍。
func deskIsMouseMove(payload []byte) bool {
	return bytes.Contains(payload, []byte(`"action":"move"`))
}

// deskIsVideoFrame 判断是不是"可以丢"的画面帧。
// 'K' 整帧 JPEG、'H' H.264 分片、'T' 脏块差分——它们都描述画面，晚到的不如新的。
func deskIsVideoFrame(b []byte) bool {
	return len(b) > 0 && (b[0] == 'K' || b[0] == 'H' || b[0] == 'T')
}

// deskBrowserVideoBacklog 是允许积压的画面帧数。
//
// 这个数字直接就是延迟：浏览器消费不过来时，队列有多深，用户看到的画面就落后多少帧。
// 原来的 256 在 20fps 下等于**十几秒**的缓冲——现场"点一下等三五秒"有一半来自这里。
// 实时画面的正确策略是丢旧留新：留 3 帧只是给突发抖动一点余量。
const deskBrowserVideoBacklog = 3

// enqueueBrowser forwards an agent frame to the browser writer.
//
// 控制帧（'S' 元信息 / 'E' 错误 / 'C' 剪贴板 / 'F','D' 文件…）**永不丢弃**：丢了它们
// 曾经把 deskSendError 的原因吃掉，UI 上只剩一句"已断开"。
//
// 画面帧相反：浏览器慢下来时**必须丢旧的**。差分帧被丢会让那一块停在旧像素上，
// 但 Agent 每 5 秒一定发一张整帧关键帧，最坏情况下几秒内自愈；相比之下"画面整体
// 落后十几秒"是不可用的。
//
// Returns false only when the session is done (caller should stop relaying).
func (s *deskSession) enqueueBrowser(out []byte) bool {
	if !deskIsVideoFrame(out) {
		// Control frames block until delivered (or session ends).
		select {
		case s.toBrowser <- out:
			return true
		case <-s.done:
			return false
		}
	}
	// 先把积压的旧画面清到阈值以下，再放新的。
	s.trimVideoBacklog(deskBrowserVideoBacklog)
	select {
	case s.toBrowser <- out:
		return true
	case <-s.done:
		return false
	default:
	}
	// 还是满的（队列容量本来就小、或者全是控制帧）：再丢一帧最旧的画面腾位置。
	// "最新的那一帧必须进得去"是这段的硬要求——丢新留旧等于让用户看着过期画面。
	s.trimVideoBacklog(0)
	select {
	case s.toBrowser <- out:
		return true
	case <-s.done:
		return false
	default:
		return true // 实在放不下就丢这一帧，下一帧马上就来
	}
}

// trimVideoBacklog 把队列里的旧画面丢到只剩 keep 帧；途中捞出来的控制帧一个都不丢，
// 原样按序放回队尾。keep=0 表示"至少丢一帧画面"。
func (s *deskSession) trimVideoBacklog(keep int) {
	var held [][]byte
	dropped := 0
drain:
	for len(s.toBrowser) > keep {
		select {
		case old := <-s.toBrowser:
			if deskIsVideoFrame(old) {
				dropped++
				if keep == 0 {
					break drain // 只需要腾一格
				}
				continue
			}
			held = append(held, old)
		default:
			break drain // 消费者刚好把队列掏空了
		}
	}
	for _, b := range held {
		select {
		case s.toBrowser <- b:
		case <-s.done:
			return
		default:
			return
		}
	}
	_ = dropped
}
