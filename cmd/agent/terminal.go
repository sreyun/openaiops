package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Remote terminal — agent side.
//
// The agent has no inbound ports, so it dials out: a persistent long-poll asks
// the server whether an operator opened a terminal; when one is, the agent opens
// two plain-HTTP streams — rx (framed keystrokes/resize down) and tx (shell
// output up) — and bridges them to a locally-spawned shell. All terminal
// requests carry the machine fingerprint (bound at registration), NOT the
// install token — so rotating the token never breaks already-installed agents.
//
// The shell is a real pseudo-terminal where available (Windows ConPTY, Linux/
// macOS openpty) so interactive TTY features work; it falls back to piped stdio
// on platforms without a native PTY.

// termShell is a spawned interactive shell — a PTY master or a piped fallback.
type termShell interface {
	io.Reader                    // shell output
	Write(p []byte) (int, error) // keystrokes to the shell
	Resize(cols, rows int) error // window size (no-op for piped fallback)
	Wait() error                 // block until the shell exits
	Close() error                // terminate + release
}

// newPTY is provided per-platform; it returns nil when no native PTY is
// available, so startShell falls back to piped stdio.
// (see pty_windows.go / pty_linux.go / pty_darwin.go / pty_other.go)

var (
	termHTTP = &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:      10,
			IdleConnTimeout:   90 * time.Second,
			ForceAttemptHTTP2: false, // match reportTransport: H2 single-conn death kills all streams
		},
		CheckRedirect: preserveAgentRedirect,
	} // rx/tx streams are long-lived — no client-level timeout
	// termWaitHTTP bounds the long-poll wait so a half-open network can't wedge
	// the poller forever (which would silently kill the terminal channel while
	// metrics keep reporting). Slightly above the server's 25s poll timeout.
	termWaitHTTP = &http.Client{Timeout: 35 * time.Second, CheckRedirect: preserveAgentRedirect}
	// termSessionTimeout is the maximum duration of a single terminal session.
	// After this duration, the shell is killed and resources released —
	// prevents forgotten sessions from leaking PTY descriptors and memory.
	// v5.4.0: raised from 30m to 24h so operators can keep long-lived sessions
	// (e.g. monitoring a build overnight) without being forcibly disconnected.
	termSessionTimeout = 24 * time.Hour
)

// backoffTimer implements exponential backoff with jitter for reconnection.
// Formula: base * 2^(retry-1) + rand(0, base), capped at max.
// After deepBackoffThreshold consecutive failures, uses deep backoff (5min probe).
// Shared by terminal and forward channels.
type backoffTimer struct {
	retry int
	base  time.Duration
	max   time.Duration
}

func newBackoffTimer(base, max time.Duration) *backoffTimer {
	return &backoffTimer{base: base, max: max}
}

const deepBackoffThreshold = 5

// next returns the next backoff duration and increments the retry counter.
func (b *backoffTimer) next() time.Duration {
	b.retry++
	if b.retry > deepBackoffThreshold {
		return 5 * time.Minute // deep backoff: probe every 5min
	}
	d := b.base * time.Duration(1<<(b.retry-1))
	if d > b.max {
		d = b.max
	}
	d += time.Duration(rand.Int63n(int64(b.base))) // jitter
	return d
}

// reset resets the retry counter on successful connection.
func (b *backoffTimer) reset() {
	b.retry = 0
}

// deadlineReader wraps an io.ReadCloser with periodic SetReadDeadline to detect
// half-open connections (e.g. NAT silent drops). Every heartbeatInterval the
// deadline is refreshed; if no data arrives within the window, Read returns
// a timeout error, allowing the caller to close the stale connection.
type deadlineReader struct {
	conn     net.Conn // underlying connection for SetReadDeadline (may be nil)
	body     io.ReadCloser
	interval time.Duration
}

func newDeadlineReader(body io.ReadCloser, interval time.Duration) *deadlineReader {
	// Try to extract net.Conn from the response body for deadline support
	type connAccessor interface {
		SetReadDeadline(time.Time) error
	}
	var conn net.Conn
	if ca, ok := body.(connAccessor); ok {
		if c, ok := ca.(net.Conn); ok {
			conn = c
		}
	}
	return &deadlineReader{conn: conn, body: body, interval: interval}
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	if d.conn != nil {
		_ = d.conn.SetReadDeadline(time.Now().Add(d.interval))
	}
	return d.body.Read(p)
}

func (d *deadlineReader) Close() error {
	return d.body.Close()
}

// agentDict is a minimal embedded i18n table for user-visible terminal
// messages. The agent is a separate binary and does not import the server's
// i18n package; only the handful of file-transfer errors that render in the
// browser terminal are translated here. The language is supplied per terminal
// session from the operator's dashboard preference (threaded through the
// terminal/wait response), so these messages follow the UI language.
var agentDict = map[string]map[string]string{
	"zh-CN": {
		"agent.file.upload_too_large":   "文件超过100MB限制",
		"agent.file.create_failed":      "无法创建文件: %v",
		"agent.file.upload_oversize":    "上传数据超过声明大小",
		"agent.file.write_failed":       "写入文件失败: %v",
		"agent.file.not_found":          "文件不存在或无法访问: %v",
		"agent.file.dir_unsupported":    "不支持下载目录",
		"agent.file.download_too_large": "文件超过100MB限制",
		"agent.file.open_failed":        "无法打开文件: %v",
	},
	"zh-TW": {
		"agent.file.upload_too_large":   "檔案超過100MB限制",
		"agent.file.create_failed":      "無法建立檔案: %v",
		"agent.file.upload_oversize":    "上傳資料超過宣告大小",
		"agent.file.write_failed":       "寫入檔案失敗: %v",
		"agent.file.not_found":          "檔案不存在或無法存取: %v",
		"agent.file.dir_unsupported":    "不支援下載目錄",
		"agent.file.download_too_large": "檔案超過100MB限制",
		"agent.file.open_failed":        "無法開啟檔案: %v",
	},
	"en": {
		"agent.file.upload_too_large":   "File exceeds 100MB limit",
		"agent.file.create_failed":      "Failed to create file: %v",
		"agent.file.upload_oversize":    "Uploaded data exceeds declared size",
		"agent.file.write_failed":       "Failed to write file: %v",
		"agent.file.not_found":          "File not found or inaccessible: %v",
		"agent.file.dir_unsupported":    "Directory download is not supported",
		"agent.file.download_too_large": "File exceeds 100MB limit",
		"agent.file.open_failed":        "Failed to open file: %v",
	},
}

// agentT translates a terminal key for the given language, with optional
// fmt.Sprintf args (the dict values use %v placeholders). Falls back to zh-CN,
// then to the key itself if the key is unknown.
func agentT(lang, key string, args ...interface{}) string {
	if lang == "" {
		lang = "zh-CN"
	}
	m, ok := agentDict[lang]
	if !ok {
		m = agentDict["zh-CN"]
	}
	s, ok := m[key]
	if !ok {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// runTerminalChannelFor runs a persistent reverse terminal channel for one
// server target. Each target gets its own goroutine so terminal sessions from
// different servers don't interfere. The fingerprint (machine-bound) is the
// same for all targets; each server independently verifies it.
func (a *Agent) runTerminalChannelFor(t *serverTarget) {
	if a.identity.Fingerprint == "" {
		slog.Warn("远程终端通道未启用：未采集到机器指纹", "server", t.server)
		return
	}
	slog.Info("远程终端通道已就绪，等待服务端呼叫…", "server", t.server)
	backoff := newBackoffTimer(1*time.Second, 60*time.Second)
	for {
		// Keep HostID in sync with the service (desktop worker pattern): after
		// fingerprint rejoin the canonical id may change while this loop runs.
		if a.stateFile != "" {
			if id := readHostIDFromState(a.stateFile); id != "" && id != a.identity.HostID {
				slog.Info("终端通道刷新 HostID", "old", short(a.identity.HostID), "new", short(id))
				a.identity.HostID = id
			}
		}
		sid, mode, command, lang, ok := a.termWait(t)
		if !ok {
			d := backoff.next()
			slog.Debug("终端通道连接失败，指数退避等待", "delay", d, "retry", backoff.retry)
			time.Sleep(d)
			continue
		}
		backoff.reset() // success: reset backoff
		if sid == "" {
			continue // long-poll timeout, re-poll immediately
		}
		switch mode {
		case "exec":
			go a.runExecSession(t.server, sid, command) // one-shot playbook command (no PTY)
		case "container_exec":
			go a.runContainerTerminalSession(t.server, sid, command, lang)
		default:
			go a.runTerminalSession(t.server, sid, lang) // interactive host shell
		}
	}
}

func (a *Agent) termWait(t *serverTarget) (sessionID, mode, command, lang string, ok bool) {
	server := t.server
	// Per-target id: panels can know this machine by different host_ids (see
	// serverTarget.hostIDOr) — waiting on the wrong one means the panel's
	// terminal call is never picked up by this agent.
	hostID := t.hostIDOr(a.identity.HostID)
	q := url.Values{"host": {hostID}}
	resp, err := agentGet(termWaitHTTP, server+"/api/v1/agent/terminal/wait?"+q.Encode(), a.identity.Fingerprint)
	if err != nil {
		slog.Debug("终端 wait 请求失败", "err", err, "server", server)
		return "", "", "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		slog.Warn("终端 wait 被拒绝(403)：指纹与服务端主机记录不匹配，请确认 Agent 已成功注册",
			"server", server, "host_id", short(hostID))
		return "", "", "", "", false
	}
	if resp.StatusCode != http.StatusOK {
		slog.Debug("终端 wait 非 200", "status", resp.StatusCode, "server", server)
		return "", "", "", "", false
	}
	var out struct {
		Session string `json:"session"`
		Mode    string `json:"mode"`
		Command string `json:"command"`
		Lang    string `json:"lang"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Session, out.Mode, out.Command, out.Lang, true
}

// runExecSession runs a single playbook command via a one-shot child process
// (NOT an interactive PTY): far more reliable than the terminal + sentinel hack,
// especially on Linux bash where readline / prompts / login banners broke sentinel
// detection. It captures combined stdout+stderr, reports the exit code, streams
// the result up the tx channel, and ends — the agent returns to waiting at once.
func (a *Agent) runExecSession(server, sid, command string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("剧本命令会话异常已恢复", "session", sid, "panic", r)
		}
	}()
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}
	// Cancelable budget: server cancel closes the session → alive probe fails →
	// CommandContext kills the local process. No kill scripts are pushed to the host.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	go a.watchExecSessionCancel(server, sid, cancel)

	// 关键：先开 tx POST（用管道作为请求体），让请求头立刻到达服务端，从而服务端 tx 处理器的
	// markAgentUp 立即触发（标记「已接单」）。此前本函数是「跑完命令才 POST」，服务端 Phase1
	// 等 agentUp 的窗口（40s）会被慢命令占满 → 被误判为 no-pickup 而失败并重试（甚至把同一命令
	// 重复下发）。开管道后「接单」与「命令完成」解耦：接单即刻确认，Phase2 计时才是命令真实预算。
	pr, pw := io.Pipe()
	posted := make(chan struct{})
	go func() {
		defer close(posted)
		req, err := http.NewRequest("POST", server+"/api/v1/agent/terminal/tx?session="+sid, pr)
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Agent-Fingerprint", a.identity.Fingerprint)
		if resp, err := termHTTP.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	// 内置模块：以 modulePrefix 开头的命令不经过 shell，直接由 Agent 用 Go 执行（跨系统一致、
	// 无需运维背命令，且天然规避 grep 无匹配/命令不存在等 shell 语义坑）。否则按普通 shell 执行。
	var out []byte
	var exit int
	if payload, ok := strings.CutPrefix(command, modulePrefix); ok {
		out, exit = a.runModuleCtx(ctx, strings.TrimSpace(payload))
	} else {
		out, exit = runShellCommandCtx(ctx, command)
	}
	if ctx.Err() != nil && exit == 0 && len(out) == 0 {
		out = []byte("（剧本已在服务端停止，本机任务已中止）")
		exit = 130
	}
	// Fallback: some programs bypass chcp and emit bytes in the system ANSI
	// code page (e.g., a C program using printf with GBK literals). Convert any
	// non-UTF-8 bytes to UTF-8 via the Windows API (no-op on Linux/macOS).
	out = ensureUTF8(out)
	// 命令完成：写入全部输出 + 退出码标记，再关闭管道（tx body 结束 → 服务端判定命令完成）。
	body := append(out, []byte(fmt.Sprintf("\n[AIOPS_EXIT]%d\n", exit))...)
	_, _ = pw.Write(body)
	_ = pw.Close()
	<-posted
}

// watchExecSessionCancel polls session liveness; when the server cancelled/removed
// the session, cancel the local command context so the process stops promptly.
func (a *Agent) watchExecSessionCancel(server, sid string, cancel context.CancelFunc) {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		alive, ok := a.termSessionAlive(server, sid)
		if !ok {
			continue // transient network — keep waiting
		}
		if !alive {
			cancel()
			return
		}
	}
}

func (a *Agent) termSessionAlive(server, sid string) (alive bool, ok bool) {
	q := url.Values{}
	q.Set("session", sid)
	resp, err := agentGet(termWaitHTTP, server+"/api/v1/agent/terminal/alive?"+q.Encode(), a.identity.Fingerprint)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, true
	case http.StatusGone, http.StatusNotFound:
		return false, true
	default:
		return false, false
	}
}

// runShellCommand 用系统 shell 执行一条命令，返回合并输出（stdout+stderr）与退出码。
func runShellCommand(command string) ([]byte, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return runShellCommandCtx(ctx, command)
}

func runShellCommandCtx(ctx context.Context, command string) ([]byte, int) {
	if ctx == nil {
		ctx = context.Background()
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// chcp 65001 让 cmd 内建命令尽量输出 UTF-8。但 Agent 作为 Windows 服务运行时无控制台，
		// chcp 会失败——必须用「&」而非「&&」串接：否则 chcp 一失败就短路，真正的命令根本不执行，
		// cmd 直接返回 chcp 的非零退出码 → 表现为「命令退出码 1」。用「&」则无论 chcp 成败都执行
		// 命令，退出码取命令自身；chcp 的输出/报错全部丢弃，非 UTF-8 字节仍由上层 ensureUTF8 兜底。
		// Use absolute cmd + PATH repair so LocalSystem playbooks find ipconfig 等.
		chcp := filepath.Join(windowsSystemRoot(), "System32", "chcp.com")
		cmd = exec.CommandContext(ctx, windowsCmdPath(), "/c",
			fmt.Sprintf(`set "PATH=%s;%%PATH%%" & "%s" 65001 >nul 2>nul & %s`,
				strings.Join(windowsEssentialPathDirs(windowsSystemRoot()), ";"), chcp, command))
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	// Set a UTF-8 locale so command output (including Chinese) is encoded as
	// UTF-8 on all platforms. On Windows, chcp 65001 handles the console code
	// page; PYTHONIOENCODING helps Python programs that read the env.
	cmd.Env = execEnv()
	out, err := runCmdEscaped(cmd)
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
			out = append(out, []byte("\n"+err.Error())...)
		}
	}
	return out, exit
}

// runContainerTerminalSession opens an interactive docker/podman exec -it session.
// command format: "<containerID>|<shell>" (shell optional, default sh).
func (a *Agent) runContainerTerminalSession(server, sid, command, lang string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("容器终端会话异常已恢复", "session", sid, "panic", r)
		}
	}()
	parts := strings.SplitN(strings.TrimSpace(command), "|", 2)
	cid := strings.TrimSpace(parts[0])
	shell := "sh"
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		shell = strings.TrimSpace(parts[1])
	}
	cli := containerCLI()
	if cli == "" || cid == "" {
		a.termSendPlain(server, sid, "\r\n\x1b[31m未找到 docker/podman 或容器 ID 无效\x1b[0m\r\n")
		return
	}
	sh := newContainerExecPTY(cli, cid, shell, 120, 30)
	if sh == nil {
		a.termSendPlain(server, sid, "\r\n\x1b[31m无法启动容器交互终端（需 Linux/macOS Agent 与可用 PTY）\x1b[0m\r\n")
		return
	}
	slog.Info("容器终端会话开始", "session", sid, "container", cid, "cli", cli)
	a.streamInteractiveShell(server, sid, sh, lang, "")
}

// termSendPlain posts a short one-shot framed PTY output and ends the server
// TX handler (which closes the session). Use ONLY for fatal startup errors
// where no long-lived interactive stream will follow. Privilege warnings must
// go through streamInteractiveShell's banner instead — a one-shot TX would
// call handleAgentTermTx's defer sess.close() and kill the session before the
// real rx/tx streams attach (non-root Docker agents hit this every open).
func (a *Agent) termSendPlain(server, sid, msg string) {
	payload := []byte(msg)
	frame := make([]byte, 5+len(payload))
	frame[0] = 'O'
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	copy(frame[5:], payload)
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(frame)
		_ = pw.Close()
	}()
	req, err := http.NewRequest("POST", server+"/api/v1/agent/terminal/tx?session="+sid, pr)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Agent-Fingerprint", a.identity.Fingerprint)
	if resp, err := termHTTP.Do(req); err == nil {
		resp.Body.Close()
	}
}

func (a *Agent) runTerminalSession(server, sid, lang string) {
	// A terminal/playbook session must never crash the whole agent: recover any
	// panic so metrics reporting and future sessions keep working.
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("终端会话异常已恢复（不影响 Agent 运行）", "session", sid, "panic", r)
		}
	}()
	shell := shellPath()
	sh := startShell(120, 30)
	if sh == nil {
		slog.Warn("远程终端 shell 启动失败", "session", sid, "shell", shell)
		a.termSendPlain(server, sid, "\r\n\x1b[31m无法启动交互 Shell（"+shell+"）。请确认主机已安装 bash/sh。\x1b[0m\r\n")
		return
	}
	var banner string
	if runtime.GOOS == "linux" {
		diag := termPrivilegeDiag()
		if os.Geteuid() != 0 {
			banner = "\r\n\x1b[33m[AIOps] Agent 非 root（" + diag + "）。编辑 /etc 会只读；请用 root 重装：curl … | sudo bash\x1b[0m\r\n"
		} else if !etcWritable() {
			// Agent process may still be sandboxed; interactive shell tries nsenter.
			// Do not claim escape succeeded — operator should check vim/touch /etc.
			banner = "\r\n\x1b[33m[AIOps] Agent 进程命名空间内 /etc 只读（" + diag + "）。交互 Shell 会尝试 nsenter 进入宿主机挂载命名空间；若 vim 仍报 E45：检查 nsenter 是否可用，或改编辑 /etc/systemd/resolved.conf（resolv.conf 可能是只读 symlink）。\x1b[0m\r\n"
		}
		slog.Info("远程终端会话开始", "session", sid, "shell", shell, "diag", diag)
	} else {
		slog.Info("远程终端会话开始", "session", sid, "shell", shell, "uid", os.Geteuid())
	}
	a.streamInteractiveShell(server, sid, sh, lang, banner)
}

func (a *Agent) streamInteractiveShell(server, sid string, sh termShell, lang, banner string) {
	var once sync.Once
	closeAll := func() { once.Do(func() { _ = sh.Close() }) }

	// Session timeout: forcibly close after termSessionTimeout to prevent
	// abandoned sessions from leaking resources. The operator can reconnect.
	timeoutTimer := time.AfterFunc(termSessionTimeout, func() {
		slog.Warn("终端会话超时，强制关闭", "session", sid, "timeout", termSessionTimeout)
		closeAll()
	})
	defer timeoutTimer.Stop()

	// zmChan carries upload data received from the browser (via rx stream)
	// to the ZMODEM upload handler running in the tx goroutine.
	zmChan := make(chan []byte, 32)

	// fileTxChan carries file operation frames (upload ACK, download data)
	// from the rx goroutine to the tx goroutine for writing to the browser.
	// Buffer increased to 512 to handle large file downloads without blocking
	// the rx goroutine (which processes the terminal input stream).
	fileTxChan := make(chan []byte, 512)

	// tx: stream shell output up with ZMODEM detection + framing
	// Frame format: [type:1][len:4 BE][payload]
	//   'O' (0x4F) = normal PTY output
	//   'Z' (0x5A) = ZMODEM signal (JSON with filename/size)
	//   'D' (0x44) = download data chunk
	//   'E' (0x45) = transfer complete
	go func() {
		defer closeAll()
		pr, pw := io.Pipe()
		req, err := http.NewRequest("POST", server+"/api/v1/agent/terminal/tx?session="+sid, pr)
		if err != nil {
			pw.Close()
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Agent-Fingerprint", a.identity.Fingerprint)
		// Fire off the HTTP request in a goroutine; write to pw in the main goroutine.
		reqDone := make(chan error, 1)
		go func() {
			// deskStreamClient forces per-frame flushing (WriteBufferSize=1); the
			// shared termHTTP would hold small PTY output in its 4KB buffer, so
			// keystroke echo / prompts could stall until enough bytes accumulated.
			resp, doErr := deskStreamClient().Do(req)
			if doErr == nil {
				resp.Body.Close()
			}
			reqDone <- doErr
		}()

		// Privilege / sandbox notices must ride this long-lived TX — never a
		// separate termSendPlain POST (that closes the server session).
		if banner != "" {
			_ = writeFrame(pw, 'O', []byte(banner))
		}
		// Write framed PTY output to pw, with ZMODEM interception and file operations.
		streamPTYFramed(sh, pw, zmChan, fileTxChan)
		pw.Close()
		<-reqDone
	}()

	// rx: framed keystrokes / resize / upload from the server → the shell
	// Half-open detection: wrap rx body with deadline reader (90s heartbeat)
	// to detect NAT silent drops where the connection appears alive but
	// no data flows in either direction.
	go func() {
		defer closeAll()
		resp, err := agentGet(termHTTP, server+"/api/v1/agent/terminal/rx?session="+sid, a.identity.Fingerprint)
		if err != nil {
			slog.Warn("终端 rx 连接失败", "session", sid, "err", err)
			return
		}
		defer resp.Body.Close()
		// Wrap with deadline reader for half-open connection detection
		dr := newDeadlineReader(resp.Body, 90*time.Second)
		readTermFrames(dr, sh, lang, zmChan, fileTxChan)
	}()

	_ = sh.Wait()
	closeAll()
	slog.Info("远程终端会话结束", "session", sid)
}

// readTermFrames parses the rx stream: each frame is [type:1][len:2 BE][payload].
// type 'i' = input bytes, 'r' = resize ("colsxrows"),
// 'u' = upload data chunk, 'e' = end of upload,
// 'f' = file upload metadata (JSON), 'd' = download request (JSON).
func readTermFrames(r io.Reader, sh termShell, lang string, zmChan chan<- []byte, fileTxChan chan<- []byte) {
	var hdr [3]byte

	// File upload state (button-based, not ZMODEM)
	type fileUploadState struct {
		file     *os.File
		filename string
		size     int64
		received int64
	}
	var upload *fileUploadState

	// Helper: send a framed file-info message to the browser via fileTxChan.
	// Uses blocking send (with timeout) so ACKs are never silently dropped.
	sendFileInfo := func(typ string, meta map[string]interface{}) {
		meta["type"] = typ
		js, _ := json.Marshal(meta)
		frame := make([]byte, 5+len(js))
		frame[0] = 'F'
		binary.BigEndian.PutUint32(frame[1:], uint32(len(js)))
		copy(frame[5:], js)
		// Blocking send with 5s timeout — prevents silent ACK loss when
		// fileTxChan is congested (e.g. during a large download).
		select {
		case fileTxChan <- frame:
		case <-time.After(5 * time.Second):
			slog.Warn("文件信息帧发送超时（5s），丢弃", "type", typ)
		}
	}

	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			// Clean up upload if in progress
			if upload != nil {
				upload.file.Close()
				os.Remove(upload.file.Name())
				upload = nil
			}
			return
		}
		n := int(binary.BigEndian.Uint16(hdr[1:]))
		payload := make([]byte, n)
		if n > 0 {
			if _, err := io.ReadFull(r, payload); err != nil {
				if upload != nil {
					upload.file.Close()
					os.Remove(upload.file.Name())
					upload = nil
				}
				return
			}
		}
		switch hdr[0] {
		case 'i':
			if _, err := sh.Write(payload); err != nil {
				return
			}
		case 'r':
			if cols, rows, ok := parseSize(string(payload)); ok {
				_ = sh.Resize(cols, rows)
			}
		case 'f':
			// File upload metadata (button-based)
			var meta struct {
				Filename   string `json:"filename"`
				Size       int64  `json:"size"`
				TargetPath string `json:"target_path"`
			}
			if err := json.Unmarshal(payload, &meta); err != nil || meta.TargetPath == "" {
				slog.Warn("文件上传元数据无效", "err", err)
				continue
			}
			if meta.Size < 0 || meta.Size > 100<<20 {
				sendFileInfo("upload_ack", map[string]interface{}{
					"status": "error", "message": agentT(lang, "agent.file.upload_too_large"),
				})
				continue
			}
			// Path hygiene: clean the target, and confine a bare/relative path to a
			// safe temp dir so an upload can't land somewhere unexpected relative to
			// the agent's working directory. Absolute paths are honored (the operator
			// already has a full terminal on this host, so this is a guardrail against
			// surprises, not a privilege boundary).
			target := filepath.Clean(meta.TargetPath)
			if !filepath.IsAbs(target) {
				target = filepath.Join(os.TempDir(), filepath.Base(target))
			}
			f, err := os.Create(target)
			if err != nil {
				slog.Warn("无法创建上传文件", "path", target, "err", err)
				sendFileInfo("upload_ack", map[string]interface{}{
					"status": "error", "message": agentT(lang, "agent.file.create_failed", err),
				})
				continue
			}
			upload = &fileUploadState{
				file:     f,
				filename: meta.Filename,
				size:     meta.Size,
			}
			slog.Info("文件上传开始", "filename", meta.Filename, "target", target, "size", meta.Size)

		case 'u':
			if upload != nil {
				// Enforce the declared size: a client must not stream more than it
				// announced, otherwise a bad/hostile peer could fill the disk past
				// the 100MB cap.
				if upload.received+int64(len(payload)) > upload.size {
					slog.Warn("上传数据超过声明大小，已中止", "filename", upload.filename)
					upload.file.Close()
					os.Remove(upload.file.Name())
					sendFileInfo("upload_ack", map[string]interface{}{
						"status": "error", "message": agentT(lang, "agent.file.upload_oversize"),
					})
					upload = nil
					continue
				}
				// Button-based upload: write to file. On error, drop the upload and
				// STOP (previously this fell through to upload.received on a nil
				// upload → panic that crashed the session goroutine).
				if _, err := upload.file.Write(payload); err != nil {
					slog.Warn("写入上传文件失败", "err", err)
					upload.file.Close()
					os.Remove(upload.file.Name())
					upload = nil
					continue
				}
				upload.received += int64(len(payload))
			} else {
				// ZMODEM upload: forward to zmChan
				if len(payload) > 0 {
					select {
					case zmChan <- payload:
					default:
					}
				}
			}

		case 'e':
			if upload != nil {
				// Button-based upload complete
				upload.file.Close()
				slog.Info("文件上传完成", "filename", upload.filename, "received", upload.received)
				sendFileInfo("upload_ack", map[string]interface{}{
					"status":   "ok",
					"filename": upload.filename,
					"size":     upload.received,
				})
				upload = nil
			} else {
				// ZMODEM upload: forward to zmChan
				select {
				case zmChan <- nil:
				default:
				}
			}

		case 'd':
			// Download request (button-based)
			var meta struct {
				RemotePath string `json:"remote_path"`
			}
			if err := json.Unmarshal(payload, &meta); err != nil || meta.RemotePath == "" {
				slog.Warn("下载请求无效", "err", err)
				continue
			}
			go handleFileDownload(meta.RemotePath, lang, fileTxChan)
		}
	}
}

// handleFileDownload reads a remote file and sends it to the browser via fileTxChan.
func handleFileDownload(remotePath, lang string, fileTxChan chan<- []byte) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("文件下载异常", "path", remotePath, "panic", r)
		}
	}()

	// Helper: send a framed file-info message (blocking with timeout).
	sendFileInfo := func(typ string, meta map[string]interface{}) {
		meta["type"] = typ
		js, _ := json.Marshal(meta)
		frame := make([]byte, 5+len(js))
		frame[0] = 'F'
		binary.BigEndian.PutUint32(frame[1:], uint32(len(js)))
		copy(frame[5:], js)
		// Blocking send with 5s timeout — prevents silent ACK/data loss.
		select {
		case fileTxChan <- frame:
		case <-time.After(5 * time.Second):
			slog.Warn("文件下载帧发送超时（5s），丢弃", "type", typ)
		}
	}

	// Check file
	info, err := os.Stat(remotePath)
	if err != nil {
		sendFileInfo("download_error", map[string]interface{}{
			"message": agentT(lang, "agent.file.not_found", err),
		})
		return
	}
	if info.IsDir() {
		sendFileInfo("download_error", map[string]interface{}{
			"message": agentT(lang, "agent.file.dir_unsupported"),
		})
		return
	}
	if info.Size() > 100<<20 {
		sendFileInfo("download_error", map[string]interface{}{
			"message": agentT(lang, "agent.file.download_too_large"),
		})
		return
	}

	// Send metadata
	fname := info.Name()
	sendFileInfo("download_meta", map[string]interface{}{
		"filename": fname,
		"size":     info.Size(),
	})

	// Read and send file chunks
	f, err := os.Open(remotePath)
	if err != nil {
		sendFileInfo("download_error", map[string]interface{}{
			"message": agentT(lang, "agent.file.open_failed", err),
		})
		return
	}
	defer f.Close()

	slog.Info("文件下载开始", "path", remotePath, "size", info.Size())
	buf := make([]byte, 32<<10) // 32KB chunks
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			// Send 'D' frame — blocking with timeout to prevent data loss.
			frame := make([]byte, 5+n)
			frame[0] = 'D'
			binary.BigEndian.PutUint32(frame[1:], uint32(n))
			copy(frame[5:], chunk)
			select {
			case fileTxChan <- frame:
			case <-time.After(10 * time.Second):
				slog.Warn("文件下载数据块发送超时，取消下载", "path", remotePath)
				return
			}
		}
		if err != nil {
			break
		}
	}

	// Send 'E' frame (transfer complete) — blocking with timeout.
	endFrame := make([]byte, 5)
	endFrame[0] = 'E'
	select {
	case fileTxChan <- endFrame:
	case <-time.After(5 * time.Second):
		slog.Warn("文件下载结束帧发送超时", "path", remotePath)
	}
	slog.Info("文件下载完成", "path", remotePath, "size", info.Size())
}

func parseSize(s string) (cols, rows int, ok bool) {
	i := strings.IndexByte(s, 'x')
	if i <= 0 {
		return 0, 0, false
	}
	c, e1 := strconv.Atoi(s[:i])
	rw, e2 := strconv.Atoi(s[i+1:])
	if e1 != nil || e2 != nil || c <= 0 || rw <= 0 || c > 1000 || rw > 1000 {
		return 0, 0, false
	}
	return c, rw, true
}

// userHomeDir returns the home directory of the current OS user.
//
// Resolution chain (first success wins):
//  1. os.UserHomeDir()  — fastest, works for both cgo and pure-Go builds
//  2. user.Current().HomeDir — reads /etc/passwd (Linux/macOS) or Windows API
//  3. $HOME / $USERPROFILE env var — fallback for minimal containers / systemd
//  4. /root (uid 0, non-Windows) — last resort for root daemons
//
// Returns "" if nothing reliable is found. The result is NOT validated against
// the filesystem — callers should os.Stat() before using as cmd.Dir.
func userHomeDir() string {
	// 1. os.UserHomeDir — uses getpwuid_r on Unix, SHGetKnownFolderPath on Windows.
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	// 2. user.Current — reads /etc/passwd or Windows user API.
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	// 3. Environment variable — works when all else fails (systemd, containers).
	if h := strings.TrimSpace(os.Getenv("HOME")); h != "" {
		return h
	}
	if runtime.GOOS == "windows" {
		if h := strings.TrimSpace(os.Getenv("USERPROFILE")); h != "" {
			return h
		}
	} else {
		// 4. Hard-coded root fallback for uid-0 daemons.
		if os.Getuid() == 0 {
			return "/root"
		}
	}
	return ""
}

// isNonInteractiveShell reports shells that refuse an interactive session.
// Linux install creates system user "aiops" with /sbin/nologin; systemd then
// injects SHELL=/sbin/nologin into the agent. Spawning that "shell" prints
// "This account is currently not available." and exits immediately.
func isNonInteractiveShell(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "nologin", "false", "true", "sync":
		return true
	}
	low := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(low, "/nologin")
}

// shellExists reports that path is a regular file (executable preferred).
func shellExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// passwdShellForUID returns the login shell from /etc/passwd for uid, if any.
func passwdShellForUID(uid int) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	want := strconv.Itoa(uid)
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Split(ln, ":")
		if len(f) < 7 {
			continue
		}
		if f[2] == want {
			return strings.TrimSpace(f[6])
		}
	}
	return ""
}

// shellsFromEtcShells returns paths listed in /etc/shells (interactive candidates).
func shellsFromEtcShells() []string {
	b, err := os.ReadFile("/etc/shells")
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// shellPath returns a usable interactive shell for the remote terminal.
// Never returns nologin/false — those are valid passwd shells for service
// accounts but must not back an operator PTY.
func shellPath() string {
	if runtime.GOOS == "windows" {
		if c := os.Getenv("COMSPEC"); c != "" {
			return c
		}
		return "cmd.exe"
	}
	var candidates []string
	if sh := strings.TrimSpace(os.Getenv("SHELL")); sh != "" {
		candidates = append(candidates, sh)
	}
	if sh := passwdShellForUID(os.Getuid()); sh != "" {
		candidates = append(candidates, sh)
	}
	candidates = append(candidates, shellsFromEtcShells()...)
	candidates = append(candidates,
		"/bin/bash", "/usr/bin/bash",
		"/bin/zsh", "/usr/bin/zsh",
		"/bin/sh", "/usr/bin/sh",
		"/bin/ash", "/bin/ksh", "/usr/bin/ksh",
	)
	seen := map[string]bool{}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] || isNonInteractiveShell(c) || !shellExists(c) {
			continue
		}
		seen[c] = true
		return c
	}
	return "/bin/sh"
}

// envEntryKey reports whether entry is KEY=... (case-insensitive on Windows:
// CreateProcess / cmd resolve Path and PATH as the same variable, but a naïve
// prefix match on "PATH=" leaves a stale "Path=" entry that still wins).
func envEntryKey(entry, key string) bool {
	i := strings.IndexByte(entry, '=')
	if i <= 0 {
		return false
	}
	name := entry[:i]
	if runtime.GOOS == "windows" {
		return strings.EqualFold(name, key)
	}
	return name == key
}

// setEnvKey replaces or appends KEY=value in env. On Windows it also drops
// duplicate spellings of the same key (Path/PATH/path).
func setEnvKey(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	set := false
	for _, e := range env {
		if envEntryKey(e, key) {
			if !set {
				out = append(out, key+"="+value)
				set = true
			}
			continue
		}
		out = append(out, e)
	}
	if !set {
		out = append(out, key+"="+value)
	}
	return out
}

// getEnvKey returns the value for KEY in env (KEY=value), or "".
func getEnvKey(env []string, key string) string {
	for _, e := range env {
		if envEntryKey(e, key) {
			if i := strings.IndexByte(e, '='); i >= 0 {
				return e[i+1:]
			}
		}
	}
	return ""
}

// isUnixStylePathEnv detects a Unix PATH wrongly applied on Windows
// (historically buildShellEnv filled /usr/... when PATH was missing).
func isUnixStylePathEnv(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if strings.Contains(p, `\`) || strings.Contains(p, ";") {
		return false
	}
	if strings.HasPrefix(p, "/usr") || strings.HasPrefix(p, "/bin") || strings.HasPrefix(p, "/sbin") {
		return true
	}
	return strings.Contains(p, ":/bin") || strings.Contains(p, ":/usr") || strings.Contains(p, ":/sbin")
}

// windowsSystemRoot returns %SystemRoot% / %windir%, defaulting to C:\Windows.
func windowsSystemRoot() string {
	if r := strings.TrimSpace(os.Getenv("SystemRoot")); r != "" {
		return r
	}
	if r := strings.TrimSpace(os.Getenv("windir")); r != "" {
		return r
	}
	return `C:\Windows`
}

// windowsEssentialPathDirs are directories that must be on PATH for interactive
// cmd.exe (ipconfig, chcp, ping, …). LocalSystem services often inherit an
// empty or truncated PATH.
func windowsEssentialPathDirs(root string) []string {
	root = strings.TrimRight(strings.ReplaceAll(root, "/", `\`), `\`)
	sys32 := root + `\System32`
	return []string{
		sys32,
		root,
		sys32 + `\Wbem`,
		sys32 + `\WindowsPowerShell\v1.0`,
	}
}

// mergeWindowsPath prepends essential System32 dirs; drops a Unix-style PATH.
func mergeWindowsPath(root, existing string) string {
	if isUnixStylePathEnv(existing) {
		existing = ""
	}
	essential := windowsEssentialPathDirs(root)
	seen := map[string]bool{}
	var parts []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		key := strings.ToLower(dir)
		if seen[key] {
			return
		}
		seen[key] = true
		parts = append(parts, dir)
	}
	for _, d := range essential {
		add(d)
	}
	for _, d := range strings.Split(existing, ";") {
		add(d)
	}
	return strings.Join(parts, ";")
}

// enrichWindowsShellEnv forces SystemRoot/ComSpec/Path so remote cmd can find
// standard tools when the agent runs as a LocalSystem Windows service.
// Canonical key is "Path" (Windows os.Environ spelling) so we replace — not
// duplicate alongside — any existing Path/PATH entry.
func enrichWindowsShellEnv(env []string) []string {
	root := windowsSystemRoot()
	sys32 := filepath.Join(root, "System32")
	env = setEnvKey(env, "SystemRoot", root)
	env = setEnvKey(env, "windir", root)
	if getEnvKey(env, "SystemDrive") == "" {
		drive := filepath.VolumeName(root)
		if drive == "" {
			drive = "C:"
		}
		env = setEnvKey(env, "SystemDrive", drive)
	}
	comspec := filepath.Join(sys32, "cmd.exe")
	if c := getEnvKey(env, "ComSpec"); c == "" {
		if c = strings.TrimSpace(os.Getenv("ComSpec")); c == "" {
			c = comspec
		}
		env = setEnvKey(env, "ComSpec", c)
	}
	// os.Getenv is case-insensitive on Windows; merge process Path too.
	existing := getEnvKey(env, "Path")
	if existing == "" {
		existing = os.Getenv("Path")
	}
	env = setEnvKey(env, "Path", mergeWindowsPath(root, existing))
	if getEnvKey(env, "PATHEXT") == "" {
		env = setEnvKey(env, "PATHEXT", ".COM;.EXE;.BAT;.CMD;.VBS;.VBE;.JS;.JSE;.WSF;.WSH;.MSC")
	}
	return env
}

// ensureWindowsProcessPath repairs the agent process Path in-place so every
// child (terminal, playbook, update helper) inherits System32 even if a caller
// forgets enrichWindowsShellEnv. Safe no-op on non-Windows.
func ensureWindowsProcessPath() {
	if runtime.GOOS != "windows" {
		return
	}
	root := windowsSystemRoot()
	merged := mergeWindowsPath(root, os.Getenv("Path"))
	_ = os.Setenv("Path", merged)
	if strings.TrimSpace(os.Getenv("SystemRoot")) == "" {
		_ = os.Setenv("SystemRoot", root)
	}
	if strings.TrimSpace(os.Getenv("windir")) == "" {
		_ = os.Setenv("windir", root)
	}
	if strings.TrimSpace(os.Getenv("ComSpec")) == "" {
		_ = os.Setenv("ComSpec", filepath.Join(root, "System32", "cmd.exe"))
	}
	if strings.TrimSpace(os.Getenv("PATHEXT")) == "" {
		_ = os.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD;.VBS;.VBE;.JS;.JSE;.WSF;.WSH;.MSC")
	}
}

// windowsCmdPath returns an absolute path to cmd.exe.
func windowsCmdPath() string {
	if c := strings.TrimSpace(os.Getenv("ComSpec")); c != "" {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	p := filepath.Join(windowsSystemRoot(), "System32", "cmd.exe")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return "cmd.exe"
}

// windowsShellInitCmd is the /K fallback string when a bootstrap .cmd cannot be
// written. Avoid nested quotes — Go's EscapeArg + cmd's /K parser mangle them
// and the set PATH / chcp lines never run (symptoms: ipconfig/chcp not found).
func windowsShellInitCmd() string {
	root := windowsSystemRoot()
	sys32 := filepath.Join(root, "System32")
	pathPre := strings.Join(windowsEssentialPathDirs(root), ";")
	chcp := filepath.Join(sys32, "chcp.com")
	drive := strings.TrimSpace(os.Getenv("SystemDrive"))
	if drive == "" {
		drive = filepath.VolumeName(root)
	}
	if drive == "" {
		drive = "C:"
	}
	return fmt.Sprintf(
		`set Path=%s;%%Path%%&%s 65001 >nul 2>nul&set PYTHONIOENCODING=utf-8&cd /d %s\`,
		pathPre, chcp, drive,
	)
}

// writeWindowsShellBootstrap writes a .cmd that repairs Path then returns to an
// interactive prompt (cmd /K script.cmd). Using a file avoids CreateProcess
// quote-eating of complex /K strings on older Windows.
func writeWindowsShellBootstrap() string {
	root := windowsSystemRoot()
	sys32 := filepath.Join(root, "System32")
	pathPre := strings.Join(windowsEssentialPathDirs(root), ";")
	chcp := filepath.Join(sys32, "chcp.com")
	drive := strings.TrimSpace(os.Getenv("SystemDrive"))
	if drive == "" {
		drive = filepath.VolumeName(root)
	}
	if drive == "" {
		drive = "C:"
	}
	body := fmt.Sprintf("@echo off\r\n"+
		"set Path=%s;%%Path%%\r\n"+
		"set PATHEXT=.COM;.EXE;.BAT;.CMD;.VBS;.VBE;.JS;.JSE;.WSF;.WSH;.MSC\r\n"+
		"set PYTHONIOENCODING=utf-8\r\n"+
		"cd /d %s\\\r\n"+
		"if exist \"%s\" \"%s\" 65001 >nul 2>nul\r\n",
		pathPre, drive, chcp, chcp)
	dir := os.TempDir()
	if dir == "" {
		dir = drive + `\`
	}
	path := filepath.Join(dir, "aiops-term-init.cmd")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		slog.Warn("写入终端 PATH 引导脚本失败，回退 /K 内联", "err", err)
		return ""
	}
	return path
}

// dirUsableForShell reports whether dir can be used as an interactive shell cwd.
// systemd ProtectHome=true often still lets Stat succeed on /root while Open/chdir
// fails — Go then reports fork/exec bash: permission denied.
func dirUsableForShell(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// resolveWritableHome returns a home directory the interactive shell can use.
// Prefer the passwd/UserHomeDir home when it exists and is writable; otherwise
// fall back to cwd / temp so login shells and ~ expansion still work in
// read-only Docker agents (passwd home /home/aiops is often missing).
func resolveWritableHome() string {
	if h := userHomeDir(); h != "" && dirUsableForShell(h) && pathWritable(h) {
		return h
	}
	if dir := interactiveShellDir(); dir != "" && pathWritable(dir) {
		return dir
	}
	if t := os.TempDir(); dirUsableForShell(t) && pathWritable(t) {
		return t
	}
	if h := userHomeDir(); h != "" {
		return h
	}
	return ""
}

// interactiveShellDir is the cwd for a remote interactive shell. Avoid starting
// in LocalSystem's systemprofile (confusing and often PATH-poor context).
// When $HOME is blocked (ProtectHome), fall back to the process cwd, agent
// install dir, or a writable temp directory so the terminal still starts.
func interactiveShellDir() string {
	if dir := userHomeDir(); dir != "" {
		low := strings.ToLower(filepath.Clean(dir))
		if !strings.Contains(low, "systemprofile") && dirUsableForShell(dir) {
			return dir
		}
	}
	if wd, err := os.Getwd(); err == nil && dirUsableForShell(wd) {
		return wd
	}
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dirUsableForShell(dir) {
			return dir
		}
	}
	if runtime.GOOS == "windows" {
		drive := strings.TrimSpace(os.Getenv("SystemDrive"))
		if drive == "" {
			drive = "C:"
		}
		root := drive + `\`
		if dirUsableForShell(root) {
			return root
		}
	}
	if dirUsableForShell(os.TempDir()) {
		return os.TempDir()
	}
	return ""
}

// buildShellEnv returns a full environment for the spawned shell, filling in
// HOME/USER/PATH/SHELL when the parent process (e.g. systemd) lacks them.
// Without HOME, bash prints "cd: HOME not set" and can't resolve ~ paths.
func buildShellEnv() []string {
	env := os.Environ()
	has := func(key string) bool {
		return getEnvKey(env, key) != ""
	}
	// On Unix, ALWAYS force HOME to the correct user home directory.
	// The interactive shell is a separate session for the remote operator —
	// the agent's inherited HOME (from systemd, sudo, etc.) is often wrong
	// (e.g. /opt/AIOps-agent). Login shell "cd $HOME" depends on this value.
	if runtime.GOOS == "windows" {
		env = enrichWindowsShellEnv(env)
	} else {
		if h := resolveWritableHome(); h != "" {
			env = setEnvKey(env, "HOME", h)
		}
		// Also force USER / LOGNAME to match.
		if u, err := user.Current(); err == nil && u.Username != "" {
			env = setEnvKey(env, "USER", u.Username)
			env = setEnvKey(env, "LOGNAME", u.Username)
		} else if os.Getuid() == 0 {
			env = setEnvKey(env, "USER", "root")
			env = setEnvKey(env, "LOGNAME", "root")
		}
		// Always overwrite SHELL: systemd injects the passwd shell for User=,
		// which is /sbin/nologin for the hardened "aiops" service account.
		env = setEnvKey(env, "SHELL", shellPath())
		if !has("PATH") {
			env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
		}
	}
	env = append(env, "TERM=xterm-256color")
	// Ensure UTF-8 locale on Linux/macOS so command output (including Chinese)
	// is encoded as UTF-8 rather than the legacy C locale.
	if runtime.GOOS != "windows" {
		env = append(env, "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	} else {
		env = setEnvKey(env, "PYTHONIOENCODING", "utf-8")
	}
	return env
}

// execEnv returns the environment for playbook exec sessions. It inherits
// the agent's environment (PATH/HOME/…) and forces UTF-8 locale on all
// platforms so command output (including Chinese) is always UTF-8.
func execEnv() []string {
	env := os.Environ()
	if runtime.GOOS != "windows" {
		// Ensure UTF-8 locale on Linux/macOS: some minimal containers default to
		// the C locale which mangles non-ASCII output.
		env = append(env, "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	} else {
		// LocalSystem services often lack System32 on PATH — same fix as interactive.
		env = enrichWindowsShellEnv(env)
		env = setEnvKey(env, "PYTHONIOENCODING", "utf-8")
	}
	return env
}

// startShell returns a native PTY shell if the platform supports one, else a
// piped-stdio fallback.
func startShell(cols, rows int) termShell {
	if sh := newPTY(cols, rows); sh != nil {
		return sh
	}
	return newPipeShell()
}

// ---- piped fallback (no PTY) ----
// Used on Windows Server 2012 / Win8 (no ConPTY) and as a last-resort elsewhere.
// Piped cmd.exe ignores chcp for redirected stdout and emits ACP (GBK) + often
// bare LF — without conversion the browser shows � and staircase prompts.
// Redirected stdin also disables console echo, so we locally echo keystrokes.

type pipeChunk struct {
	data []byte
	err  error
}

type pipeShell struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	out       *os.File
	convBuf   []byte // leftover UTF-8 after a short Read buffer
	rawPend   []byte // incomplete ACP/MBCS bytes waiting for the next Read
	lastWasCR bool   // CRLF normalizer state across Read calls

	echoOn   bool
	echoMu   sync.Mutex
	echoBuf  []byte
	echoWake chan struct{}
	readCh   chan pipeChunk
}

func newPipeShell() termShell {
	name, args := shellCommand()
	dir := interactiveShellDir()
	if runtime.GOOS != "windows" {
		if n, a, via := linuxInteractiveShellInvocation(name, args, dir); via {
			name, args = n, a
			dir = "" // nsenter --wd handles cwd
		}
	}
	cmd := exec.Command(name, args...)
	cmd.Env = buildShellEnv()
	if dir != "" {
		cmd.Dir = dir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	detachFromServiceJob(cmd)
	if err := cmd.Start(); err != nil {
		slog.Warn("pipe shell 启动失败", "err", err, "dir", cmd.Dir)
		_ = stdin.Close()
		_ = pr.Close()
		_ = pw.Close()
		return nil
	}
	if cmd.Process != nil {
		escapeAgentCgroupTree(cmd.Process.Pid)
	}
	_ = pw.Close() // parent drops its write end so pr EOFs when the shell exits
	// Prefer browser local echo on Windows (OSC 666 + platform heuristic) for
	// zero-RTT typing. Keep a lightweight agent echo as fallback when the UI
	// has not enabled localEcho yet (older server JS): echo only after Enter
	// would be useless, so echo every keystroke until FE takes over — FE sets
	// localEcho and we cannot know that here, so Windows uses FE-only echo.
	echoOn := runtime.GOOS != "windows"
	p := &pipeShell{
		cmd:      cmd,
		stdin:    stdin,
		out:      pr,
		echoOn:   echoOn,
		echoWake: make(chan struct{}, 1),
		readCh:   make(chan pipeChunk, 32),
		// Ask the web VT to enable instant local echo (OSC body otherwise ignored).
		convBuf: []byte("\x1b]666;local_echo=1\a"),
	}
	if path := getEnvKey(cmd.Env, "Path"); path != "" {
		prefix := path
		if len(prefix) > 160 {
			prefix = prefix[:160] + "…"
		}
		slog.Info("pipe shell 已启动", "dir", cmd.Dir, "cmd", name, "path_prefix", prefix)
	} else {
		slog.Warn("pipe shell 启动时 Path 仍为空", "dir", cmd.Dir, "cmd", name)
	}
	go p.pumpStdout()
	return p
}

func (p *pipeShell) pumpStdout() {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.out.Read(buf)
		if n > 0 {
			chunk := append(append([]byte{}, p.rawPend...), buf[:n]...)
			converted, hold := ensureUTF8Hold(chunk)
			p.rawPend = hold
			normalized := normalizeOutputNewlines(converted, &p.lastWasCR)
			if len(normalized) > 0 {
				p.readCh <- pipeChunk{data: normalized}
			}
		}
		if err != nil {
			p.readCh <- pipeChunk{err: err}
			close(p.readCh)
			return
		}
	}
}

func (p *pipeShell) takeEcho(b []byte) int {
	p.echoMu.Lock()
	defer p.echoMu.Unlock()
	if len(p.echoBuf) == 0 {
		return 0
	}
	n := copy(b, p.echoBuf)
	p.echoBuf = p.echoBuf[n:]
	return n
}

func (p *pipeShell) pushEcho(data []byte) {
	if !p.echoOn || len(data) == 0 {
		return
	}
	echo := pipeLocalEcho(data)
	if len(echo) == 0 {
		return
	}
	p.echoMu.Lock()
	p.echoBuf = append(p.echoBuf, echo...)
	p.echoMu.Unlock()
	select {
	case p.echoWake <- struct{}{}:
	default:
	}
}

// pipeLocalEcho turns keystrokes into display bytes for pipe-mode shells
// (redirected cmd has no console echo).
func pipeLocalEcho(in []byte) []byte {
	out := make([]byte, 0, len(in)+2)
	for _, c := range in {
		switch c {
		case '\r', '\n':
			out = append(out, '\r', '\n')
		case 0x08, 0x7f: // BS / DEL
			out = append(out, '\b', ' ', '\b')
		case 0x03: // Ctrl+C
			out = append(out, '^', 'C')
		default:
			if c >= 32 || c == '\t' {
				out = append(out, c)
			}
		}
	}
	return out
}

func (p *pipeShell) Read(b []byte) (int, error) {
	for {
		if len(p.convBuf) > 0 {
			n := copy(b, p.convBuf)
			p.convBuf = p.convBuf[n:]
			return n, nil
		}
		if n := p.takeEcho(b); n > 0 {
			return n, nil
		}
		select {
		case <-p.echoWake:
			continue
		case chunk, ok := <-p.readCh:
			if !ok {
				if n := p.takeEcho(b); n > 0 {
					return n, nil
				}
				return 0, io.EOF
			}
			if len(chunk.data) > 0 {
				if len(chunk.data) <= len(b) {
					return copy(b, chunk.data), chunk.err
				}
				n := copy(b, chunk.data)
				p.convBuf = append(p.convBuf[:0], chunk.data[n:]...)
				return n, nil
			}
			if chunk.err != nil {
				if n := p.takeEcho(b); n > 0 {
					return n, nil
				}
				return 0, chunk.err
			}
		}
	}
}

func (p *pipeShell) Write(b []byte) (int, error) {
	// Local echo first so keystrokes appear immediately (Read may be blocked
	// on shell output otherwise).
	p.pushEcho(b)
	// No PTY → no kernel CR→LF translation, so map Enter (CR) to LF here.
	data := make([]byte, len(b))
	copy(data, b)
	for i := range data {
		if data[i] == '\r' {
			data[i] = '\n'
		}
	}
	return p.stdin.Write(data)
}
func (p *pipeShell) Resize(int, int) error { return nil } // piped shell has no window size
func (p *pipeShell) Wait() error           { return p.cmd.Wait() }
func (p *pipeShell) Close() error {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return nil
}

// normalizeOutputNewlines turns bare LF into CRLF so the web VT (which treats
// LF as "row++" without resetting column) does not draw staircase prompts.
// Already-correct CRLF sequences are left alone.
func normalizeOutputNewlines(in []byte, lastWasCR *bool) []byte {
	if len(in) == 0 {
		return in
	}
	need := false
	prevCR := false
	if lastWasCR != nil {
		prevCR = *lastWasCR
	}
	for _, c := range in {
		if c == '\n' && !prevCR {
			need = true
			break
		}
		prevCR = c == '\r'
	}
	if !need {
		if lastWasCR != nil {
			*lastWasCR = in[len(in)-1] == '\r'
		}
		return in
	}
	out := make([]byte, 0, len(in)+8)
	prevCR = false
	if lastWasCR != nil {
		prevCR = *lastWasCR
	}
	for _, c := range in {
		if c == '\n' && !prevCR {
			out = append(out, '\r', '\n')
		} else {
			out = append(out, c)
		}
		prevCR = c == '\r'
	}
	if lastWasCR != nil {
		*lastWasCR = prevCR
	}
	return out
}

// shellCommand picks the interactive shell per OS (used by the piped fallback).
// On Windows, prefer a bootstrap .cmd that repairs Path (quote-safe), else /K
// inline init; redirected pipes still emit ACP and rely on ensureUTF8Hold.
func shellCommand() (string, []string) {
	if runtime.GOOS == "windows" {
		if boot := writeWindowsShellBootstrap(); boot != "" {
			return windowsCmdPath(), []string{"/K", boot}
		}
		return windowsCmdPath(), []string{"/K", windowsShellInitCmd()}
	}
	return shellPath(), []string{"-l", "-i"} // -l: login shell
}

// ---- ZMODEM-aware PTY output stream ----

// writeFrame writes a framed message: [type:1][len:4 BE][payload].
func writeFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > 0x7FFFFFFF {
		payload = payload[:0x7FFFFFFF]
	}
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

// streamPTYFramed reads from the PTY, detects ZMODEM headers, and writes
// framed data to w. Normal PTY output is written as 'O' frames. When ZMODEM
// is detected, the function runs the ZMODEM protocol and writes 'Z'/'D'/'E'
// frames. Upload data is received via zmChan.
//
// Uses a goroutine for PTY reading so the main loop can multiplex PTY data,
// upload data (zmChan), and a rz/sz detection timer via select.
func streamPTYFramed(ptyReader io.Reader, w io.Writer, zmChan <-chan []byte, fileTxChan <-chan []byte) {
	// PTY reader goroutine → channel (avoids blocking main loop when
	// remote rz is waiting for ZFILE and no PTY output is coming).
	type ptyData struct {
		data []byte
		err  error
	}
	ptyCh := make(chan ptyData, 1)
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := ptyReader.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				ptyCh <- ptyData{data, nil}
			}
			if err != nil {
				ptyCh <- ptyData{nil, err}
				return
			}
		}
	}()

	// State
	inZmodem := false
	var zmSession *ZmSession
	var uploadBuf []byte
	uploadReady := false

	// rz/sz detection: after ZRQINIT+ZRINIT, wait 2s for ZFILE from remote.
	// If ZFILE arrives → sz (download). If timer fires → rz (upload).
	zmNeedDetect := false
	zmDetectTimer := time.NewTimer(0)
	if !zmDetectTimer.Stop() {
		<-zmDetectTimer.C
	}

	// zmAccum accumulates partial ZMODEM frame data across PTY read chunks.
	// When parseZmFrame returns nil (incomplete frame), the remaining bytes
	// are saved here and prepended to the next chunk.
	var zmAccum []byte

	// processZmFrames parses and handles ZMODEM frames from a byte slice.
	// It also detects sz (ZFILE arriving while zmNeedDetect is true).
	processZmFrames := func(chunk []byte) {
		zmBuf := append(zmAccum, chunk...)
		zmAccum = nil
		for len(zmBuf) > 0 {
			frame, consumed, _ := parseZmFrame(zmBuf)
			if frame == nil {
				// Partial frame — save remaining bytes for next chunk
				zmAccum = append(zmAccum, zmBuf...)
				break
			}
			zmBuf = zmBuf[consumed:]

			// sz detection: ZFILE arrived while we're waiting to distinguish sz vs rz
			if zmNeedDetect && frame.Type == ZFILE {
				if !zmDetectTimer.Stop() {
					select {
					case <-zmDetectTimer.C:
					default:
					}
				}
				zmNeedDetect = false
				info := parseZFileInfo(frame.Data)
				fname := "download.dat"
				fsize := int64(0)
				if info != nil {
					if info.Name != "" {
						fname = info.Name
					}
					if info.Size > 0 {
						fsize = info.Size
					}
				}
				zmJSON := fmt.Sprintf(`{"type":"sz","filename":"%s","size":%d}`, fname, fsize)
				writeFrame(w, 'Z', []byte(zmJSON))
				slog.Info("ZMODEM检测为sz(下载)", "filename", fname, "size", fsize)
			}

			// Skip ZRQINIT — we already sent ZRINIT in the detection block
			if frame.Type == ZRQINIT {
				continue
			}

			responses := zmSession.HandleFrame(frame)
			for _, resp := range responses {
				if w2, ok := ptyReader.(io.Writer); ok {
					w2.Write(resp)
				}
			}

			// Check if download is complete
			if zmSession.State == zmIdle && zmSession.DataBuf.Len() > 0 {
				fname := "download.dat"
				fsize := int64(zmSession.DataBuf.Len())
				if zmSession.File != nil && zmSession.File.Name != "" {
					fname = zmSession.File.Name
				}
				slog.Info("ZMODEM下载完成", "filename", fname, "size", fsize)

				// 'Z' frame (type "sz") already sent at ZFILE detection time.
				// Now send the file data and completion marker.
				data := zmSession.DataBuf.Bytes()
				chunkSz := 32 << 10
				for offset := 0; offset < len(data); offset += chunkSz {
					end := offset + chunkSz
					if end > len(data) {
						end = len(data)
					}
					writeFrame(w, 'D', data[offset:end])
				}
				writeFrame(w, 'E', nil)

				inZmodem = false
				zmSession = nil
				zmAccum = nil
				uploadBuf = nil
				uploadReady = false
				break
			}
		}
	}

	for {
		// Drain zmChan: accumulate upload data
		drained := true
		for drained {
			select {
			case chunk := <-zmChan:
				if chunk == nil {
					uploadReady = true
				} else {
					uploadBuf = append(uploadBuf, chunk...)
				}
			default:
				drained = false
			}
		}

		// If upload data is ready and we're in ZMODEM mode (rz), send ZFILE to remote.
		if uploadReady && len(uploadBuf) > 0 && inZmodem && zmSession != nil && zmSession.State == zmInit {
			slog.Info("ZMODEM上传数据已就绪，开始上传协议", "size", len(uploadBuf))
			zmSession.UploadData = uploadBuf
			zmSession.UploadFirstChunk = true
			zmSession.File = &ZFileInfo{Name: "upload.dat", Size: int64(len(uploadBuf))}
			zmSession.State = zmFile
			if w2, ok := ptyReader.(io.Writer); ok {
				w2.Write(buildZfileFrame(zmSession.File.Name, zmSession.File.Size))
			}
			uploadBuf = nil
			uploadReady = false
		}

		// Wait for PTY data, upload data, or detection timer
		select {
		case pd := <-ptyCh:
			if pd.err != nil {
				if pd.err != io.EOF {
					slog.Warn("PTY读取错误", "err", pd.err)
				}
				return
			}
			chunk := pd.data

			if inZmodem {
				processZmFrames(chunk)
			} else {
				// Check for ZMODEM header
				if HasZmodemHeader(chunk) {
					idx := IndexZmodemHeader(chunk)
					if idx > 0 {
						writeFrame(w, 'O', chunk[:idx])
					}
					zmChunk := chunk[idx:]
					frame, _, _ := parseZmFrame(zmChunk)

					// Enter ZMODEM mode
					inZmodem = true
					zmSession = NewZmSession()
					zmSession.State = zmInit

					if frame != nil && frame.Type == ZRQINIT {
						// Always send ZRINIT immediately — both sz and rz need it.
						// Without ZRINIT, the remote side will timeout.
						if w2, ok := ptyReader.(io.Writer); ok {
							w2.Write(buildZrinitFrame())
						}
						slog.Info("ZMODEM握手检测到(ZRQINIT)，已发送ZRINIT，等待检测sz/rz")

						// Start 2-second timer to detect sz vs rz.
						// If ZFILE arrives within 2s → sz (download).
						// If timer fires → rz (upload).
						zmNeedDetect = true
						zmDetectTimer.Reset(2 * time.Second)

						// Process all frames in the chunk (including ZFILE if it's sz)
						processZmFrames(zmChunk)
					} else {
						slog.Info("ZMODEM握手检测到(非ZRQINIT)，开始文件传输")
						processZmFrames(zmChunk)
					}
				} else {
					// Normal output
					writeFrame(w, 'O', chunk)
				}
			}

		case <-zmDetectTimer.C:
			// Timer fired — no ZFILE within 2s → it's rz (upload)
			if zmNeedDetect {
				zmJSON := `{"type":"rz"}`
				writeFrame(w, 'Z', []byte(zmJSON))
				zmNeedDetect = false
				slog.Info("ZMODEM检测为rz(上传)，等待文件选择")
			}

		case chunk := <-zmChan:
			if chunk == nil {
				uploadReady = true
			} else {
				uploadBuf = append(uploadBuf, chunk...)
			}

		case frame := <-fileTxChan:
			// File operation frames (upload ACK, download data) are pre-framed
			// and ready to write directly to the tx stream.
			if len(frame) > 0 {
				w.Write(frame)
			}
		}
	}
}
