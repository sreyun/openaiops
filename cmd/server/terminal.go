package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Remote terminal relay.
//
// The agent has no inbound ports, so it *dials out*: it long-polls `wait` for a
// session, then opens two plain-HTTP streams — `rx` (server→agent keystrokes)
// and `tx` (agent→server shell output). The operator's browser speaks WebSocket
// to the server; the server relays bytes between the browser socket and the two
// agent streams. Agent terminal endpoints are gated by the machine fingerprint
// (bound at registration), not the install token.

type termSession struct {
	id        string
	hostID    string
	hostname  string
	operator  string
	ip        string        // client IP of the operator (for audit + display)
	mode      string        // "" = interactive terminal, "exec" = one-shot playbook command
	command   string        // the command to run when mode == "exec"
	toAgent   chan []byte   // browser keystrokes → agent (rx stream)
	toBrowser chan []byte   // agent shell output → browser
	agentUp   chan struct{} // closed once the agent attaches its tx stream
	done      chan struct{}
	upOnce    sync.Once
	doneOnce  sync.Once

	// --- terminal enhancements ---
	recording      []termRecordFrame          // session recording (timestamped I/O frames)
	observers      map[*termObserver]struct{} // read-only watchers
	cmdBuffer      string                     // accumulates input for command-level audit
	lastCommand    string                     // last extracted command (for audit logging)
	pwPromptActive bool                       // 上段输出是密码提示 → 抑制下一行输入的审计（避免记录密码）
	createdAt      int64                      // session start time
	recMu          sync.Mutex                 // protects recording + cmdBuffer + observers
	lang           string                     // operator's preferred UI language (for agent-side messages)
	changeID       int64                      // linked approved change (audit glue)
	incidentID     int64                      // linked incident loop (audit glue)
	execID         int64                      // playbook execution id (exec mode); 0 = unrelated
}

func (s *termSession) markAgentUp() { s.upOnce.Do(func() { close(s.agentUp) }) }
func (s *termSession) close() {
	s.doneOnce.Do(func() {
		close(s.done)
		s.recMu.Lock()
		for obs := range s.observers {
			close(obs.done)
		}
		s.recMu.Unlock()
	})
}

// fanOut delivers a copy of output to all observers under recMu, so it can't race
// with add/removeObserver (which also hold recMu). Best-effort: drops on a slow
// observer rather than blocking the live session.
func (s *termSession) fanOut(b []byte) {
	s.recMu.Lock()
	for obs := range s.observers {
		select {
		case obs.ch <- b:
		default:
		}
	}
	s.recMu.Unlock()
}

// termRecordFrame is one timestamped frame in the session recording.
type termRecordFrame struct {
	Ts   int64  `json:"ts"`   // unix millisecond
	Type string `json:"type"` // "input" | "output"
	Data string `json:"data"` // base64-encoded raw bytes
}

// termObserver is a read-only watcher of a terminal session.
type termObserver struct {
	ch   chan []byte // receives a copy of toBrowser output
	done chan struct{}
}

// recordFrame appends a frame to the session recording (max 50k frames).
func (s *termSession) recordFrame(typ string, data []byte) {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if len(s.recording) > 50000 {
		return // cap to prevent unbounded memory
	}
	s.recording = append(s.recording, termRecordFrame{
		Ts:   time.Now().UnixMilli(),
		Type: typ,
		Data: base64.StdEncoding.EncodeToString(data),
	})
}

type termManager struct {
	mu       sync.Mutex
	sessions map[string]*termSession
	waiters  map[string]chan string // hostID -> a waiting agent poll
	archived []termArchive          // in-memory index of recent ended sessions (for replay)
	recDir   string                 // directory where session recordings are persisted as files
	pg       *pgStore               // 永久审计留存：已结束会话录制写入 PG（可选，非空即启用）
	// pendingSessions stores session IDs that were created for a host but the
	// agent wasn't in a long-poll wait at the time of notification. This fixes
	// a race condition in batch playbook execution: when multiple hosts are
	// executed in parallel, some agents may be between poll cycles (just finished
	// one long-poll, starting the next) — notifyAgent would send to the old
	// waiter channel that nobody reads, and the session would be lost. With
	// pendingSessions, the session ID persists until the agent's next poll picks
	// it up immediately (no long-poll wait needed).
	pendingSessions map[string][]string // hostID -> queued session IDs
	// lastWaitAt records when the agent last entered terminal/wait for a host.
	// Used so the UI can fail fast when the reverse channel is clearly dead
	// instead of spinning 35s on "waiting for agent".
	lastWaitAt map[string]time.Time
	// onArchive is called when a session ends and is archived. Used to save
	// terminal output summary to AI memory for cross-session RAG retrieval.
	onArchive func(info termSessionInfo, text string)
}

// termArchiveCap is how many ended sessions' recordings are retained for replay.
// Archives are persisted in the DB snapshot, so they survive restarts ("permanent"
// within this retention window). Recordings are already frame-capped per session.
const termArchiveCap = 100

// termArchive keeps an ended session's recording so it can still be replayed
// after the live session has been removed.
type termArchive struct {
	info      termSessionInfo
	recording []termRecordFrame
}

// dbTermArchive is the JSON-serializable form of a termArchive for the DB snapshot.
type dbTermArchive struct {
	Info      termSessionInfo   `json:"info"`
	Recording []termRecordFrame `json:"recording"`
}

// --- recording file persistence: one JSON file per ended session under recDir
// (a plain bind-mounted directory — no DB), so replays survive restarts. ---

func (m *termManager) recordingPath(id string) string {
	return filepath.Join(m.recDir, id+".json")
}

// persistRecording writes one ended session's recording to its file (atomic).
func (m *termManager) persistRecording(a termArchive) {
	if m.recDir == "" || a.info.ID == "" || len(a.recording) == 0 {
		return
	}
	if err := os.MkdirAll(m.recDir, 0o750); err != nil { // 会话录制含敏感内容，禁止 others 读
		return
	}
	b, err := json.Marshal(dbTermArchive{Info: a.info, Recording: a.recording})
	if err != nil {
		return
	}
	tmp := m.recordingPath(a.info.ID) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, m.recordingPath(a.info.ID))
	}
}

// readRecordingFile loads one session's full recording from disk (on-demand replay).
func (m *termManager) readRecordingFile(id string) []termRecordFrame {
	if m.recDir == "" {
		return nil
	}
	b, err := os.ReadFile(m.recordingPath(id))
	if err != nil {
		return nil
	}
	var d dbTermArchive
	if json.Unmarshal(b, &d) != nil {
		return nil
	}
	return d.Recording
}

// loadRecordings indexes the most-recent persisted recordings into the in-memory
// archive (metadata only — full frames are read from file on replay) so ended
// sessions still list and replay after a restart.
func (m *termManager) loadRecordings(dir string) {
	m.recDir = dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // first run / no recordings yet
	}
	type fe struct {
		id  string
		mod int64
	}
	var files []fe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fe{strings.TrimSuffix(e.Name(), ".json"), info.ModTime().Unix()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod }) // oldest first
	if len(files) > termArchiveCap {
		files = files[len(files)-termArchiveCap:]
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.archived = m.archived[:0]
	for _, f := range files {
		b, err := os.ReadFile(m.recordingPath(f.id))
		if err != nil {
			continue
		}
		var d dbTermArchive
		if json.Unmarshal(b, &d) != nil {
			continue
		}
		d.Info.Active = false
		m.archived = append(m.archived, termArchive{info: d.Info}) // frames lazy-loaded on replay
	}
}

func newTermManager() *termManager {
	return &termManager{
		sessions:        map[string]*termSession{},
		waiters:         map[string]chan string{},
		pendingSessions: map[string][]string{},
		lastWaitAt:      map[string]time.Time{},
	}
}

func termID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// termFrame encodes one rx message as [type:1][len:2 BE][payload] so the agent
// can parse input vs resize from the raw stream.
func termFrame(typ byte, payload []byte) []byte {
	if len(payload) > 0xffff {
		payload = payload[:0xffff]
	}
	b := make([]byte, 3+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint16(b[1:], uint16(len(payload)))
	copy(b[3:], payload)
	return b
}

func (m *termManager) create(hostID, hostname, operator string) *termSession {
	return m.createFull(hostID, hostname, operator, "", "")
}

// createExecWithExecID tags the session with a playbook execution id so cancel can
// close/remove every related exec session without touching interactive terminals.
func (m *termManager) createExecWithExecID(hostID, hostname, command string, execID int64) *termSession {
	s := m.createFull(hostID, hostname, "playbook-exec", "exec", command)
	s.execID = execID
	return s
}

func (m *termManager) createFull(hostID, hostname, operator, mode, command string) *termSession {
	s := &termSession{
		id: termID(), hostID: hostID, hostname: hostname, operator: operator,
		mode: mode, command: command,
		toAgent: make(chan []byte, 64), toBrowser: make(chan []byte, 256),
		agentUp: make(chan struct{}), done: make(chan struct{}),
		observers: map[*termObserver]struct{}{},
		createdAt: time.Now().Unix(),
	}
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	return s
}
func (m *termManager) get(id string) *termSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// abortSessionsByExecID closes and removes all playbook-exec sessions for execID.
// Pending queue entries are purged via remove(). Does not send commands to hosts.
func (m *termManager) abortSessionsByExecID(execID int64) int {
	if m == nil || execID == 0 {
		return 0
	}
	m.mu.Lock()
	ids := make([]string, 0, 8)
	for id, s := range m.sessions {
		if s != nil && s.execID == execID {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		if s := m.get(id); s != nil {
			s.close()
		}
		m.remove(id)
	}
	return len(ids)
}

// sessionAlive reports whether an exec/interactive session still exists and is open.
func (m *termManager) sessionAlive(id string) bool {
	s := m.get(id)
	if s == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// remove deletes a session, first archiving its recording so an ended interactive
// session can still be replayed. Internal playbook-exec sessions are not archived.
func (m *termManager) remove(id string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	if ok {
		// Purge this id from the host's pending queue so a recovering agent never
		// picks up a now-dead session (e.g. after its exec timed out). Without this,
		// a briefly-offline agent could attach to a stale session and waste a poll.
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
	if ok && s.operator != "playbook-exec" {
		s.recMu.Lock()
		if len(s.recording) > 0 {
			rec := make([]termRecordFrame, len(s.recording))
			copy(rec, s.recording)
			arch := termArchive{
				info: termSessionInfo{
					ID: s.id, HostID: s.hostID, Hostname: s.hostname,
					Operator: s.operator, IP: s.ip, CreatedAt: s.createdAt,
					Active: false, Frames: len(rec),
					ChangeID: s.changeID, IncidentID: s.incidentID,
				},
				recording: rec,
			}
			m.archived = append(m.archived, arch)
			if len(m.archived) > termArchiveCap { // in-memory index cap
				m.archived = m.archived[len(m.archived)-termArchiveCap:]
			}
			go m.persistRecording(arch) // write to /app/data/recordings so replays survive restart
			if m.pg != nil {
				go m.pg.saveTermRecording(arch) // 永久审计留存：完整录制写入 PG（不受 100 条内存上限影响）
			}
			// 提取终端输出摘要存入 AI 记忆库，供跨会话 RAG 检索复用
			if m.onArchive != nil {
				go func(a termArchive) {
					var lines []string
					for _, f := range a.recording {
						if f.Type != "output" {
							continue
						}
						data, err := base64.StdEncoding.DecodeString(f.Data)
						if err != nil || len(data) == 0 {
							continue
						}
						text := stripANSI(string(data))
						if text != "" {
							lines = append(lines, text)
						}
					}
					if len(lines) == 0 {
						return
					}
					// 取最近 50 行作为摘要（避免过长）
					if len(lines) > 50 {
						lines = lines[len(lines)-50:]
					}
					summary := strings.Join(lines, "\n")
					m.onArchive(a.info, summary)
				}(arch)
			}
		}
		s.recMu.Unlock()
	}
	m.mu.Unlock()
}

// notifyAgent hands a new sessionID to the agent currently long-polling for
// hostID. Returns (deliveredOrQueued, channelAlive):
//   - channelAlive=true when a waiter is live OR the agent polled within ~90s
//   - channelAlive=false means the reverse channel looks dead; UI should fail fast
func (m *termManager) notifyAgent(hostID, sessionID string) (ok bool, alive bool) {
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
func (m *termManager) registerWaiter(hostID string) chan string {
	ch := make(chan string, 1)
	m.mu.Lock()
	m.waiters[hostID] = ch
	m.lastWaitAt[hostID] = time.Now()
	m.mu.Unlock()
	return ch
}
func (m *termManager) unregisterWaiter(hostID string, ch chan string) {
	m.mu.Lock()
	if m.waiters[hostID] == ch {
		delete(m.waiters, hostID)
	}
	m.mu.Unlock()
}

// termFingerprintOKByHost verifies the agent-presented fingerprint against the
// fingerprint bound to hostID at registration (constant-time). The terminal
// reverse channel authenticates by fingerprint, not the install token, so
// rotating the token never breaks already-installed agents' terminals.
func (s *Server) termFingerprintOKByHost(hostID, fp string) bool {
	if hostID == "" || fp == "" {
		return false
	}
	host, ok := s.store.GetHost(hostID)
	if !ok || host.Fingerprint == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(fp), []byte(host.Fingerprint)) == 1
}

// termFingerprintOK is the request-flavored wrapper for handleAgentTermWait,
// which carries host + fp as query params.
func (s *Server) termFingerprintOK(r *http.Request) bool {
	return s.termFingerprintOKByHost(r.URL.Query().Get("host"), agentFP(r))
}

// handleTerminal (browser side) upgrades to WebSocket and relays a shell session.
// Auth is enforced by authMiddleware (session cookie) before we get here.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("id")
	if !s.cfg.TerminalEnabled() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "terminal.disabled")})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	// Remote ⊆ change gate: freeze window or critical-incident host.
	if !s.enforceRemoteGate(w, r, hostID, false) {
		return
	}
	// v5.3.0: terminal secondary verification — check before WebSocket upgrade
	// so the frontend can show the password dialog before trying to open a WS.
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
	ws, err := wsAccept(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "terminal.ws_required")})
		return
	}
	defer ws.Close()

	// Look up hostname+IP for audit log (never surface raw host id)
	hostname := s.hostLabelForID(hostID)
	// operator = username (or IP fallback); clientIP always records the real
	// client address for audit traceability (honoring TrustProxy / CF-Connecting-IP).
	operator, clientIP := s.actorIP(r)
	sess := s.term.create(hostID, hostname, operator)
	sess.lang = langFromRequest(r)
	sess.ip = clientIP
	sess.changeID, sess.incidentID = s.remoteSessionLinks(hostID)
	defer s.term.remove(sess.id)
	op := operator
	user := op
	if looksLikeIPAddr(op) {
		user = ""
	}
	msg := Tz("log.open_terminal", hostname)
	if sess.changeID > 0 || sess.incidentID > 0 {
		msg += fmt.Sprintf(" [关联变更#%d · 事件#%d]", sess.changeID, sess.incidentID)
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: op, Username: user, IP: clientIP, Host: hostname, Message: msg})
	defer s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: op, Username: user, IP: clientIP, Host: hostname, Message: Tz("log.close_terminal", hostname)})
	s.serveTerminalWS(ws, sess, hostID, hostname, op, clientIP)
}

// handleContainerTerminal upgrades to WebSocket and opens interactive docker/podman exec -it.
func (s *Server) handleContainerTerminal(w http.ResponseWriter, r *http.Request) {
	hostID := strings.TrimSpace(r.PathValue("hostID"))
	cid := strings.TrimSpace(r.PathValue("id"))
	if hostID == "" || cid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host and container id required"})
		return
	}
	if !s.cfg.TerminalEnabled() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "terminal.disabled")})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	// Same freeze/critical-incident gate as host terminal (container exec is still remote control).
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
	shell := strings.TrimSpace(r.URL.Query().Get("shell"))
	if shell == "" {
		shell = "sh"
	}
	if len(shell) > 32 || strings.ContainsAny(shell, " \t\n|&;`$") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shell"})
		return
	}
	ws, err := wsAccept(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "terminal.ws_required")})
		return
	}
	defer ws.Close()

	hostname := s.hostLabelForID(hostID)
	cname := strings.TrimSpace(r.URL.Query().Get("name"))
	label := hostname + " · 容器"
	if cname != "" {
		label = hostname + " · " + cname
	}
	operator, clientIP := s.actorIP(r)
	sess := s.term.createFull(hostID, label, operator, "container_exec", cid+"|"+shell)
	sess.lang = langFromRequest(r)
	sess.ip = clientIP
	sess.changeID, sess.incidentID = s.remoteSessionLinks(hostID)
	defer s.term.remove(sess.id)
	op := operator
	user := op
	if looksLikeIPAddr(op) {
		user = ""
	}
	msg := fmt.Sprintf("打开容器终端：%s shell=%s", label, shell)
	if sess.changeID > 0 || sess.incidentID > 0 {
		msg += fmt.Sprintf(" [关联变更#%d · 事件#%d]", sess.changeID, sess.incidentID)
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: op, Username: user, IP: clientIP, Host: hostname, Message: msg})
	defer s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: op, Username: user, IP: clientIP, Host: hostname,
		Message: fmt.Sprintf("关闭容器终端：%s", label)})
	s.serveTerminalWS(ws, sess, hostID, label, op, clientIP)
}

func (s *Server) serveTerminalWS(ws *wsConn, sess *termSession, hostID, hostname, op, clientIP string) {
	hostIP := ""
	if h := s.hostByID(hostID); h != nil {
		hostIP = h.IP
	}

	if _, alive := s.term.notifyAgent(hostID, sess.id); !alive {
		_ = ws.WriteBinary([]byte("\r\n\x1b[31m" + Tz("terminal.no_channel") + "\x1b[0m\r\n\r\n" + Tz("terminal.no_channel_hint_1") + "\r\n" + Tz("terminal.no_channel_hint_2") + "\r\n" + Tz("terminal.no_channel_hint_3") + "\r\n\r\n" + Tz("terminal.no_channel_hint_4") + "\r\n" + Tz("terminal.no_channel_hint_https") + "\r\n"))
		return
	}
	go func() {
		select {
		case <-sess.agentUp:
		case <-time.After(35 * time.Second):
			_ = ws.WriteBinary([]byte("\r\n\x1b[31m" + Tz("terminal.timeout") + "\x1b[0m\r\n" + Tz("terminal.timeout_hint_https") + "\r\n"))
			sess.close()
		case <-sess.done:
		}
	}()

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
			typ, payload := byte('i'), data
			switch data[0] {
			case 'r':
				typ, payload = 'r', data[1:]
			case 'i':
				typ, payload = 'i', data[1:]
			case 'u':
				typ, payload = 'u', data[1:]
			case 'e':
				typ, payload = 'e', nil
			case 'f':
				typ, payload = 'f', data[1:]
			case 'd':
				typ, payload = 'd', data[1:]
			}
			if len(payload) == 0 && typ != 'e' {
				continue
			}
			if typ == 'i' {
				if cmd := sess.processCommandAudit(payload); cmd != "" {
					s.store.AddLog(LogEntry{Kind: KindTerminal, Level: "info", Actor: op, IP: clientIP, Host: hostname, Message: Tz("log.terminal_cmd", hostname, hostIP, cmd)})
				}
			}
			if typ == 'r' {
				sess.recordFrame("resize", payload)
			}
			for {
				chunk := payload
				if len(chunk) > 0xffff {
					chunk = chunk[:0xffff]
				}
				select {
				case sess.toAgent <- termFrame(typ, chunk):
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
	go func() {
		defer sess.close()
		for {
			select {
			case b := <-sess.toBrowser:
				sess.recordFrame("output", b)
				sess.notePasswordPrompt(b)
				sess.fanOut(b)
				if err := ws.WriteBinary(b); err != nil {
					return
				}
			case <-sess.done:
				return
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
					return
				}
			case <-sess.done:
				return
			}
		}
	}()
	<-sess.done
}

// handleAgentTermWait: agent long-polls here; returns a session id when the
// operator opens a terminal for this host, or {} on timeout (agent re-polls).
// Before entering the long-poll, it checks pendingSessions — if a batch exec
// notification arrived while the agent was between polls, it's delivered here
// immediately without waiting.
func (s *Server) handleAgentTermWait(w http.ResponseWriter, r *http.Request) {
	if !s.termFingerprintOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	host := r.URL.Query().Get("host")
	if host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.host_required")})
		return
	}
	// Check for pending sessions FIRST — this is the batch-execution race fix.
	// If notifyAgent queued a session while no waiter was active, deliver it
	// immediately without entering the long-poll.
	s.term.mu.Lock()
	if pending := s.term.pendingSessions[host]; len(pending) > 0 {
		// Deliver the first session that is still live; drop any already-removed
		// ones (e.g. their exec timed out) so the agent never wastes a poll cycle
		// attaching to a dead session.
		var sid string
		rest := pending
		for len(rest) > 0 {
			cand := rest[0]
			rest = rest[1:]
			if _, live := s.term.sessions[cand]; live {
				sid = cand
				break
			}
		}
		if len(rest) == 0 {
			delete(s.term.pendingSessions, host)
		} else {
			s.term.pendingSessions[host] = rest
		}
		if sid != "" {
			sess := s.term.sessions[sid]
			s.term.mu.Unlock()
			out := map[string]string{"session": sid}
			if sess != nil {
				if sess.mode == "exec" || sess.mode == "container_exec" {
					out["mode"] = sess.mode
					out["command"] = sess.command
				}
				if sess.lang != "" {
					out["lang"] = sess.lang
				}
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		// all pending were dead — fall through to the normal long-poll
	}
	s.term.mu.Unlock()
	// No pending session — enter long-poll as usual.
	ch := s.term.registerWaiter(host)
	defer s.term.unregisterWaiter(host, ch)
	select {
	case sid := <-ch:
		out := map[string]string{"session": sid}
		if sess := s.term.get(sid); sess != nil {
			if sess.mode == "exec" || sess.mode == "container_exec" {
				out["mode"] = sess.mode
				out["command"] = sess.command
			}
			if sess.lang != "" {
				out["lang"] = sess.lang
			}
		}
		writeJSON(w, http.StatusOK, out)
	case <-time.After(25 * time.Second):
		writeJSON(w, http.StatusOK, map[string]string{})
	case <-r.Context().Done():
	}
}

// handleAgentTermAlive lets the agent notice a cancelled/removed session and stop
// local CommandContext work — without the server sending kill scripts to the host.
func (s *Server) handleAgentTermAlive(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimSpace(r.URL.Query().Get("session"))
	if sid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session required"})
		return
	}
	sess := s.term.get(sid)
	if sess == nil {
		writeJSON(w, http.StatusGone, map[string]any{"alive": false, "reason": "gone"})
		return
	}
	if !s.termFingerprintOKByHost(sess.hostID, agentFP(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	select {
	case <-sess.done:
		writeJSON(w, http.StatusGone, map[string]any{"alive": false, "reason": "closed"})
		return
	default:
	}
	writeJSON(w, http.StatusOK, map[string]any{"alive": true})
}

// handleAgentTermRx streams operator keystrokes down to the agent (chunked).
func (s *Server) handleAgentTermRx(w http.ResponseWriter, r *http.Request) {
	sess := s.term.get(r.URL.Query().Get("session"))
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
	// Tell nginx (and other X-Accel-aware proxies) NOT to buffer this response —
	// keystrokes must reach the agent in real time. Saves the operator from having
	// to set `proxy_buffering off` for this stream.
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

// handleAgentTermTx receives the shell's output stream from the agent (chunked
// request body) and fans it to the browser. The stream uses a simple frame format:
//
//	[type:1][len:4 BE][payload]
//	'O' (0x4F) = normal PTY output → toBrowser as raw bytes
//	'Z' (0x5A) = ZMODEM signal      → toBrowser as [0xFF][0xFE]['Z'][len:4][json]
//	'D' (0x44) = download data chunk → toBrowser as [0xFF][0xFE]['D'][len:4][data]
//	'E' (0x45) = transfer complete   → toBrowser as [0xFF][0xFE]['E'][len:4][]
func (s *Server) handleAgentTermTx(w http.ResponseWriter, r *http.Request) {
	sess := s.term.get(r.URL.Query().Get("session"))
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "common.session_gone")})
		return
	}
	if !s.termFingerprintOKByHost(sess.hostID, agentFP(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	sess.markAgentUp()
	defer sess.close()

	// Exec sessions (playbook): the agent sends raw command output (no framing),
	// so read the entire body as-is and send it to toBrowser.
	// Capped at 50 MiB (execOutputLimit) to prevent a runaway command (e.g.
	// cat /dev/urandom) from exhausting server memory.
	if sess.mode == "exec" {
		const execOutputLimit = 50 << 20 // 50 MiB
		data, err := io.ReadAll(io.LimitReader(r.Body, execOutputLimit))
		if err == nil && len(data) > 0 {
			b := make([]byte, len(data))
			copy(b, data)
			select {
			case sess.toBrowser <- b:
			case <-sess.done:
			}
		}
		return
	}

	// Read framed data from the agent.
	var hdr [5]byte
	for {
		// Read the 5-byte header: [type:1][len:4 BE]
		_, err := io.ReadFull(r.Body, hdr[:])
		if err != nil {
			return
		}
		typ := hdr[0]
		payloadLen := int(binary.BigEndian.Uint32(hdr[1:]))
		if payloadLen > 100<<20 { // cap at 100MB
			return
		}
		payload := make([]byte, payloadLen)
		if payloadLen > 0 {
			if _, err := io.ReadFull(r.Body, payload); err != nil {
				return
			}
		}

		switch typ {
		case 'O': // Normal PTY output
			b := make([]byte, len(payload))
			copy(b, payload)
			select {
			case sess.toBrowser <- b:
			case <-sess.done:
				return
			}

		case 'Z': // ZMODEM signal
			// Send as [0xFF][0xFE]['Z'][len:4][payload]
			b := buildZmBrowserFrame('Z', payload)
			select {
			case sess.toBrowser <- b:
			case <-sess.done:
				return
			}

		case 'D': // Download data chunk
			b := buildZmBrowserFrame('D', payload)
			select {
			case sess.toBrowser <- b:
			case <-sess.done:
				return
			}

		case 'E': // Transfer complete
			b := buildZmBrowserFrame('E', nil)
			select {
			case sess.toBrowser <- b:
			case <-sess.done:
				return
			}

		case 'F': // File info (upload ACK or download metadata)
			b := buildZmBrowserFrame('F', payload)
			select {
			case sess.toBrowser <- b:
			case <-sess.done:
				return
			}
		}
	}
}

// buildZmBrowserFrame builds a ZMODEM browser frame: [0xFF][0xFE][type][len:4][payload].
func buildZmBrowserFrame(typ byte, payload []byte) []byte {
	hdr := make([]byte, 7)
	hdr[0] = 0xFF
	hdr[1] = 0xFE
	hdr[2] = typ
	binary.BigEndian.PutUint32(hdr[3:], uint32(len(payload)))
	return append(hdr, payload...)
}

// ---- terminal enhancement: session list, replay, observer, command audit ----

// termSessionInfo is the JSON view of an active or recently-ended session.
type termSessionInfo struct {
	ID         string `json:"id"`
	HostID     string `json:"host_id"`
	Hostname   string `json:"hostname"`
	Operator   string `json:"operator"`
	IP         string `json:"ip"`
	CreatedAt  int64  `json:"created_at"`
	Active     bool   `json:"active"`
	Observers  int    `json:"observers"`
	Frames     int    `json:"frames"`
	ChangeID   int64  `json:"change_id,omitempty"`
	IncidentID int64  `json:"incident_id,omitempty"`
}

// listSessions returns active sessions + ended sessions. Ended sessions come from
// the permanent PG store (full history) when available, else the in-memory archive.
func (m *termManager) listSessions() []termSessionInfo {
	m.mu.Lock()
	out := make([]termSessionInfo, 0, len(m.sessions)+len(m.archived))
	seen := map[string]bool{}
	for _, s := range m.sessions {
		if s.operator == "playbook-exec" {
			continue // hide internal playbook sessions from the session list
		}
		s.recMu.Lock()
		out = append(out, termSessionInfo{
			ID: s.id, HostID: s.hostID, Hostname: s.hostname,
			Operator: s.operator, IP: s.ip, CreatedAt: s.createdAt,
			Active: true, Observers: len(s.observers),
			Frames: len(s.recording), ChangeID: s.changeID, IncidentID: s.incidentID,
		})
		s.recMu.Unlock()
		seen[s.id] = true
	}
	// snapshot the in-memory archive (newest first) under the lock; PG I/O happens after unlock
	memArch := make([]termSessionInfo, 0, len(m.archived))
	for i := len(m.archived) - 1; i >= 0; i-- {
		memArch = append(memArch, m.archived[i].info)
	}
	pg := m.pg
	m.mu.Unlock()

	// Recent in-memory archives first (covers the async PG-write window), then the
	// permanent PG history (full retention); dedup by id so nothing appears twice.
	add := func(list []termSessionInfo) {
		for _, a := range list {
			if !seen[a.ID] {
				out = append(out, a)
				seen[a.ID] = true
			}
		}
	}
	add(memArch)
	if pg != nil {
		add(pg.listTermRecordings(500)) // 展示全量历史（不止内存里的最近 100 条）
	}
	return out
}

// getRecording returns the recorded frames for a session (for replay). Live
// sessions come from memory; ended sessions come from the in-memory archive if
// still loaded, otherwise from the persisted file (survives restart / eviction).
func (m *termManager) getRecording(sessionID string) []termRecordFrame {
	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		s.recMu.Lock()
		out := make([]termRecordFrame, len(s.recording))
		copy(out, s.recording)
		s.recMu.Unlock()
		m.mu.Unlock()
		return out
	}
	for _, a := range m.archived {
		if a.info.ID == sessionID && len(a.recording) > 0 {
			out := make([]termRecordFrame, len(a.recording))
			copy(out, a.recording)
			m.mu.Unlock()
			return out
		}
	}
	m.mu.Unlock()
	return m.readRecordingFile(sessionID) // 内容存本地文件（PG 只存元数据索引），按 id 直读
}

// addObserver attaches a read-only observer to a session.
func (m *termManager) addObserver(sessionID string) (*termObserver, bool) {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	// Session may have closed between lookup and lock; refuse rather than hang the WS.
	select {
	case <-s.done:
		return nil, false
	default:
	}
	obs := &termObserver{
		ch:   make(chan []byte, 128),
		done: make(chan struct{}),
	}
	s.observers[obs] = struct{}{}
	return obs, true
}

// removeObserver detaches an observer.
func (m *termManager) removeObserver(sessionID string, obs *termObserver) {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return
	}
	s.recMu.Lock()
	delete(s.observers, obs)
	s.recMu.Unlock()
}

// 密码提示正则：输出尾部形如 "Password:" / "密码：" / "passphrase:" / "[sudo] password for x:"。
var pwPromptRE = regexp.MustCompile(`(?i)(password|passphrase|passwd|密\s*码|口\s*令)[^\n:：]{0,24}[:：]\s*$`)

// 内联密钥正则：命令里明文的 password=xxx / token: xxx / --password xxx，落审计前替换为 ***。
var (
	inlineSecretKVRE = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|secret[_-]?key)(\s*[=:]\s*)(\S+)`)
	inlinePwFlagRE   = regexp.MustCompile(`(?i)(--password[=\s]+)(\S+)`)
	mysqlPwFlagRE    = regexp.MustCompile(`(?i)(-p)(\S+)`)
)

// notePasswordPrompt 扫描 shell 输出尾部：若是密码提示则置位，下一条完整输入行（即用户键入的
// 密码）会被 processCommandAudit 跳过审计——避免把密码写进终端审计日志。
func (s *termSession) notePasswordPrompt(out []byte) {
	tail := out
	if len(tail) > 200 {
		tail = tail[len(tail)-200:]
	}
	if pwPromptRE.Match(tail) {
		s.recMu.Lock()
		s.pwPromptActive = true
		s.recMu.Unlock()
	}
}

// redactInlineSecrets 把命令行里明文的密码/令牌替换为 ***，再落审计日志。
func redactInlineSecrets(cmd string) string {
	cmd = inlineSecretKVRE.ReplaceAllString(cmd, "$1$2***")
	cmd = inlinePwFlagRE.ReplaceAllString(cmd, "$1***")
	if low := strings.ToLower(cmd); strings.Contains(low, "mysql") || strings.Contains(low, "mariadb") {
		cmd = mysqlPwFlagRE.ReplaceAllString(cmd, "$1***") // mysql -pSECRET → -p***
	}
	return cmd
}

// processCommandAudit extracts commands from the input stream by detecting
// Enter (CR/LF) and returning the accumulated line for audit logging.
// This is best-effort — control characters, escape sequences, and multi-line
// commands may cause imperfect extraction, but it captures the common case.
// Returns the completed command string (empty if no command was completed).
func (s *termSession) processCommandAudit(payload []byte) string {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	for _, b := range payload {
		if b == '\r' || b == '\n' {
			cmd := strings.TrimSpace(s.cmdBuffer)
			s.cmdBuffer = ""
			if cmd != "" && cmd[0] != 0x1b && cmd[0] != 0x03 {
				if s.pwPromptActive { // 紧随密码提示后的这一行是密码——不审计、不落库
					s.pwPromptActive = false
					return ""
				}
				s.lastCommand = cmd
				return redactInlineSecrets(cmd) // 内联密钥（password=/token=/mysql -p…）脱敏后再审计
			}
		} else if b >= 0x20 && b < 0x7f {
			s.cmdBuffer += string(b)
		} else if b == 0x7f || b == 0x08 {
			if len(s.cmdBuffer) > 0 {
				s.cmdBuffer = s.cmdBuffer[:len(s.cmdBuffer)-1]
			}
		}
	}
	return ""
}

// getDecodedRecording returns the decoded output frames for replay/observe.
func (m *termManager) getDecodedRecording(sessionID string) [][]byte {
	frames := m.getRecording(sessionID) // covers both live and archived sessions
	out := make([][]byte, 0, len(frames))
	for _, f := range frames {
		if f.Type == "output" {
			if data, err := base64.StdEncoding.DecodeString(f.Data); err == nil {
				out = append(out, data)
			}
		}
	}
	return out
}

// findSessionsByHost returns all terminal sessions (active + archived) for a
// given host, newest first. Used by the AI diagnosis layer to enrich the prompt
// with terminal operation context.
func (m *termManager) findSessionsByHost(hostID string) []termSessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []termSessionInfo
	for _, s := range m.sessions {
		if s.hostID != hostID || s.operator == "playbook-exec" {
			continue
		}
		s.recMu.Lock()
		out = append(out, termSessionInfo{
			ID: s.id, HostID: s.hostID, Hostname: s.hostname,
			Operator: s.operator, IP: s.ip, CreatedAt: s.createdAt,
			Active: true, Observers: len(s.observers),
			Frames: len(s.recording),
		})
		s.recMu.Unlock()
	}
	for i := len(m.archived) - 1; i >= 0; i-- {
		if m.archived[i].info.HostID == hostID {
			out = append(out, m.archived[i].info)
		}
	}
	return out
}
