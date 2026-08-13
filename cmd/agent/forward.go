package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Port forwarding — agent side.
//
// The agent long-polls the server for forward session requests. When one
// arrives, the agent dials localhost:targetPort and opens two HTTP streams:
//   - rx (server→agent): framed user data, relayed to the TCP connection
//   - tx (agent→server): raw target service output, relayed to the user
//
// This mirrors the terminal reverse channel architecture but is completely
// independent — separate endpoints, separate polling loop, separate session
// handling. The two features share zero code paths.

// forwardWaitHTTP bounds the long-poll so a half-open network can't wedge the
// poller. Slightly above the server's 25s poll timeout, matching termWaitHTTP.
var forwardWaitHTTP = &http.Client{Timeout: 35 * time.Second}

// forwardSessionTimeout bounds how long a single forward session can run
// before being forcibly closed. Prevents forgotten sessions from leaking
// TCP connections and goroutines.
const forwardSessionTimeout = 15 * time.Minute

// agentGet issues a GET carrying the agent fingerprint (the reverse-channel
// auth credential) in the X-Agent-Fingerprint header instead of the URL query,
// keeping it out of server access / reverse-proxy logs. Non-secret params
// (host, session) stay in rawURL.
func agentGet(client *http.Client, rawURL, fp string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Agent-Fingerprint", fp)
	return client.Do(req)
}

// runForwardChannelFor runs a persistent reverse forward channel for one
// server target. Each target gets its own goroutine so forward sessions from
// different servers don't interfere.
func (a *Agent) runForwardChannelFor(t *serverTarget) {
	if a.identity.Fingerprint == "" {
		slog.Warn("端口转发通道未启用：未采集到机器指纹", "server", t.server)
		return
	}
	slog.Info("端口转发通道已就绪，等待服务端呼叫…", "server", t.server)
	backoff := newBackoffTimer(1*time.Second, 60*time.Second)
	for {
		sid, targetPort, mode, remoteTarget, ok := a.forwardWait(t)
		if !ok {
			d := backoff.next()
			slog.Debug("转发通道连接失败，指数退避等待", "delay", d, "retry", backoff.retry)
			time.Sleep(d)
			continue
		}
		backoff.reset() // success: reset backoff
		if sid == "" {
			continue // long-poll timeout, re-poll immediately
		}
		go a.runForwardSession(t.server, sid, targetPort, mode, remoteTarget)
	}
}

// forwardWait long-polls the server for a pending forward session.
func (a *Agent) forwardWait(t *serverTarget) (sessionID string, targetPort int, mode string, remoteTarget string, ok bool) {
	server := t.server
	q := url.Values{"host": {t.hostIDOr(a.identity.HostID)}}
	resp, err := agentGet(forwardWaitHTTP, server+"/api/v1/agent/forward/wait?"+q.Encode(), a.identity.Fingerprint)
	if err != nil {
		return "", 0, "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, "", "", false
	}
	var out struct {
		Session      string `json:"session"`
		TargetPort   int    `json:"target_port"`
		Mode         string `json:"mode"`
		RemoteTarget string `json:"remote_target"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Session, out.TargetPort, out.Mode, out.RemoteTarget, true
}

// runForwardSession dials localhost:targetPort and relays data between the TCP
// connection and the server's rx/tx streams. A panic in this goroutine must
// never crash the whole agent.
func (a *Agent) runForwardSession(server, sid string, targetPort int, mode, remoteTarget string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("转发会话异常已恢复（不影响 Agent 运行）", "session", sid, "panic", r)
		}
	}()
	if mode == "udp" { // UDP：走数据报中继（两个方向都按帧保留数据报边界）
		a.runForwardSessionUDP(server, sid, targetPort, remoteTarget)
		return
	}
	// 跳板模式：如果 remoteTarget 非空，拨号远程地址；否则走本机 localhost
	target := "localhost:" + strconv.Itoa(targetPort)
	if remoteTarget != "" {
		target = remoteTarget
		slog.Info("跳板转发模式", "session", sid, "remote_target", remoteTarget)
	}

	// P1: 添加连接超时控制（5秒）
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.Dial("tcp", target)
	if err != nil {
		slog.Warn("转发目标连接失败", "session", sid, "target", target, "err", err)
		// Send an error frame to the server so it knows the target is unreachable
		var errBuf bytes.Buffer
		// P1: 修复 Content-Length 计算错误
		errMsg := fmt.Sprintf("HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\nAgent failed to connect to localhost:%d: %s", targetPort, err.Error())
		errBuf.WriteString(errMsg)
		req, _ := http.NewRequest("POST",
			server+"/api/v1/agent/forward/tx?session="+sid, &errBuf)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Agent-Fingerprint", a.identity.Fingerprint)
		if resp, err := termHTTP.Do(req); err == nil {
			resp.Body.Close()
		}
		return
	}
	// P1: 注意 - 不要在这里设置 SetDeadline，因为它会同时影响读写操作
	// HTTP 代理的响应可能是流式的、长时间的，设置全局 deadline 会导致连接被意外关闭
	// 如果需要超时控制，应该在具体的读写操作中单独设置
	slog.Info("转发会话开始", "session", sid, "target", target)

	var once sync.Once
	closeAll := func() { once.Do(func() { _ = conn.Close() }) }
	defer closeAll()

	// Session timeout: cap each forward session duration so a forgotten
	// connection doesn't leak TCP descriptors and goroutines indefinitely.
	timeoutTimer := time.AfterFunc(forwardSessionTimeout, func() {
		slog.Warn("转发会话超时，强制关闭", "session", sid, "target", target)
		closeAll()
	})
	defer timeoutTimer.Stop()

	var wg sync.WaitGroup
	wg.Add(2)

	// tx: stream target service output up (POST body ends when target closes conn)
	// 用 conn 作为 POST body：Go 的 http 客户端会一直从 conn 读取直到目标关闭
	// 连接（EOF），再把 POST body 收尾。因此「目标响应发完」这一刻，tx 自然结束。
	go func() {
		defer wg.Done()
		req, err := http.NewRequest("POST",
			server+"/api/v1/agent/forward/tx?session="+sid, conn)
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Agent-Fingerprint", a.identity.Fingerprint)
		if resp, err := termHTTP.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	// rx: framed user data from the server → the TCP connection
	// 收到 'c' 帧（请求已完整下达）即返回；不要在 rx 里关闭 conn。
	// conn 的最终关闭由下方 wg.Wait() 之后统一执行——届时 rx（请求写完）
	// 与 tx（响应读完）都已完成，可安全关闭，避免提前关闭导致响应被截断。
	go func() {
		defer wg.Done()
		resp, err := agentGet(termHTTP, server+"/api/v1/agent/forward/rx?session="+sid, a.identity.Fingerprint)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		readForwardFrames(resp.Body, conn)
	}()

	wg.Wait()
	// rx 已把请求写完整、tx 已把响应读完整后再关闭连接。
	slog.Info("转发会话结束", "session", sid, "target", target)
}

// readForwardFrames parses the rx stream: each frame is [type:1][len:2 BE][payload].
// type 'd' = data bytes, 'c' = close signal.
func readForwardFrames(r io.Reader, conn net.Conn) {
	var hdr [3]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint16(hdr[1:]))
		payload := make([]byte, n)
		if n > 0 {
			if _, err := io.ReadFull(r, payload); err != nil {
				return
			}
		}
		switch hdr[0] {
		case 'd':
			if _, err := conn.Write(payload); err != nil {
				return
			}
		case 'c':
			return // close signal from the server
		}
	}
}

// runForwardSessionUDP relays UDP datagrams between the server tunnel and a local
// UDP service (localhost:targetPort). Both directions are framed
// ([type:1][len:2 BE][payload]) so datagram boundaries survive the byte-stream
// tunnel: rx 'd' 帧 → 一个 UDP 数据报写往目标；目标回程数据报 → 封一帧上行 tx。
func (a *Agent) runForwardSessionUDP(server, sid string, targetPort int, remoteTarget string) {
	target := "localhost:" + strconv.Itoa(targetPort)
	if remoteTarget != "" {
		target = remoteTarget
		slog.Info("UDP 跳板转发模式", "session", sid, "remote_target", remoteTarget)
	}
	conn, err := net.Dial("udp", target) // 连接态 UDP：Write=一个数据报，Read=一个数据报
	if err != nil {
		slog.Warn("UDP 转发目标连接失败", "session", sid, "target", target, "err", err)
		return
	}
	defer conn.Close()
	slog.Info("UDP 转发会话开始", "session", sid, "target", target)

	var once sync.Once
	closeAll := func() { once.Do(func() { _ = conn.Close() }) }
	defer closeAll()

	timeoutTimer := time.AfterFunc(forwardSessionTimeout, closeAll) // 会话时长上限，防泄漏
	defer timeoutTimer.Stop()

	var wg sync.WaitGroup
	wg.Add(2)

	// tx: 读本地 UDP 回程数据报 → 逐个封帧 → POST 上行（用 io.Pipe 作为分帧 body）
	go func() {
		defer wg.Done()
		defer closeAll()
		pr, pw := io.Pipe()
		go func() {
			buf := make([]byte, 64*1024)
			var hdr [3]byte
			hdr[0] = 'd'
			for {
				_ = conn.SetReadDeadline(time.Now().Add(forwardSessionTimeout))
				n, rerr := conn.Read(buf)
				if n > 0 {
					binary.BigEndian.PutUint16(hdr[1:], uint16(n))
					if _, e := pw.Write(hdr[:]); e != nil {
						break
					}
					if _, e := pw.Write(buf[:n]); e != nil {
						break
					}
				}
				if rerr != nil {
					break
				}
			}
			_ = pw.Close()
		}()
		req, rerr := http.NewRequest("POST", server+"/api/v1/agent/forward/tx?session="+sid, pr)
		if rerr != nil {
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Agent-Fingerprint", a.identity.Fingerprint)
		// Per-frame flush (see deskStreamClient): UDP datagrams must not linger in
		// the shared client's 4KB write buffer.
		if resp, e := deskStreamClient().Do(req); e == nil {
			resp.Body.Close()
		}
	}()

	// rx: 服务端下行帧 → 每个 'd' 帧写一个 UDP 数据报到目标（复用 readForwardFrames）
	go func() {
		defer wg.Done()
		defer closeAll()
		resp, rerr := agentGet(termHTTP, server+"/api/v1/agent/forward/rx?session="+sid, a.identity.Fingerprint)
		if rerr != nil {
			return
		}
		defer resp.Body.Close()
		readForwardFrames(resp.Body, conn)
	}()

	wg.Wait()
	slog.Info("UDP 转发会话结束", "session", sid, "target", target)
}
