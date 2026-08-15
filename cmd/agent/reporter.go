package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// reportTransport is the shared transport for all server targets (report POSTs).
// Connection reuse avoids TCP handshake overhead on every 10s report cycle.
// Default http.Transport is used by http.DefaultClient; we create our own so
// each target's client shares the same pool without colliding with global state.
//
// v5.2.6: HTTP/2 is explicitly disabled (TLSNextProto set to empty map).
// HTTP/2 multiplexes all requests over a single TCP connection. When the
// server restarts, that single connection dies and ALL concurrent requests
// fail simultaneously. With HTTP/1.1, each request gets its own connection
// from the pool, so a server restart only affects in-flight requests — new
// ones immediately succeed on fresh connections. This dramatically improves
// recovery time after server restarts (from 30s+ to <5s).
var reportTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	MaxIdleConns:          50,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ForceAttemptHTTP2:     false, // v5.2.6: disable HTTP/2 for better restart recovery
	TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12,
	},
	ResponseHeaderTimeout: 15 * time.Second,
}

// errForbidden signals that the server rejected a report with 403 (host not
// registered or fingerprint not bound). reportOnce reacts by re-registering.
var errForbidden = errors.New("forbidden")

// gzipCompressThreshold: payloads below 512 bytes skip gzip (the overhead of
// gzip headers + the CPU cost outweighs the tiny bandwidth saving).
const gzipCompressThreshold = 512

// serverTarget is the runtime state for one backend server connection.
// Each target has its own HTTP client (connection pool isolation), its own
// token, its own registration state, and now its own retry backoff +
// circuit breaker — so one server being down or rejecting reports never
// affects the others.
type serverTarget struct {
	server string
	token  string
	// tokenFile, when set, is re-read before each register attempt so a compose
	// sidecar can pick up /app/server-data/.install_token once the server writes it.
	tokenFile string
	httpc     *http.Client // isolated connection pool + 30s timeout

	regMu      sync.Mutex
	registered bool
	// canonicalHostID 是服务端在注册响应里下发的规范身份（见 register）。
	canonicalHostID string

	// Retry + circuit breaker: independent per-target so one failing server
	// never starves or delays reports to healthy servers.
	bo *backoff
	cb *circuitBreaker

	// gzipMu protects disableGzip, which is set true when the server returns
	// 400 on a gzip-compressed request (proxy corruption, server bug, etc.).
	// Once disabled, all subsequent reports to this target skip compression.
	gzipMu      sync.Mutex
	disableGzip bool

	logKey []byte // 服务端注册时下发的日志加密密钥（32B AES-256）；空 = 明文上报

	probeMu sync.Mutex // 分布式探测：确保同一 target 同时只跑一轮探测任务，避免慢探测堆积

	// backfill 是发往**这个** target 失败的采样缓冲。每个 target 一份是必须的：
	// 多面板部署里 A 通着、B 挂了，只有 B 欠着那些点，共用一份会让 A 收到重复数据。
	backfillMu sync.Mutex
	backfill   []shared.BackfillSample
}

// 补传缓冲的保留策略。
//
// 目标是「最长兜住 7 天的中断」，但 7 天 × 10 秒间隔 = 6 万多条样本，每条 Metrics
// 带着磁盘/GPU/连接数组约 2~4 KB —— 原样缓存要 150~250 MB，装在被监控主机上完全
// 不可接受：监控 Agent 绝不能因为服务端挂了而把业务机的内存吃掉。
//
// 所以按「越老越稀」分级保留，与服务端自己的 raw/1m/5m 分层同构：
//
//	  < 1h ：全部保留（原始分辨率，中断恢复后曲线看不出接缝）
//	1h~24h：至少间隔 60s
//	24h~7d：至少间隔 600s
//	  > 7d ：丢弃
//
// 条数上限约 360 + 1380 + 864 ≈ 2600 条，内存占用稳定在 10 MB 量级，且**与中断时长
// 无关**——中断越久，老数据自动变稀，不会无界增长。硬上限 agentBackfillMaxSamples
// 是最后一道防线（采集间隔被调到 1 秒之类的极端配置）。
const (
	agentBackfillMaxAge      = 7 * 24 * time.Hour
	agentBackfillFullWindow  = time.Hour        // 该窗口内不抽稀
	agentBackfillMidWindow   = 24 * time.Hour   // 1h~24h
	agentBackfillMidSpacing  = 60 * time.Second // 1h~24h 的最小间隔
	agentBackfillOldSpacing  = 600 * time.Second
	agentBackfillMaxSamples  = 4000
	agentBackfillPerBatch    = 60               // 每批补传条数
	agentBackfillDrainPeriod = 20 * time.Second // 后台补传节奏
)

// spoolBackfill 记下一条没能送出去的采样。
func (t *serverTarget) spoolBackfill(s shared.BackfillSample) {
	// 历史点不需要进程名列表：服务端的历史链路本来就会丢弃它（见 Store.UpsertAuthenticated
	// 里的 sample.ProcessNames = nil），留着只会让缓冲和补传包白白膨胀几十倍。
	s.Metrics.ProcessNames = nil
	t.backfillMu.Lock()
	defer t.backfillMu.Unlock()
	t.backfill = append(t.backfill, s)
	t.pruneBackfillLocked(s.Ts)
}

// pruneBackfillLocked 按分级保留策略压缩缓冲。调用方必须持有 backfillMu。
// now 用最新样本的时间戳而不是 time.Now()，这样单元测试可以完全确定地驱动它。
func (t *serverTarget) pruneBackfillLocked(now int64) {
	if len(t.backfill) == 0 {
		return
	}
	maxAge := int64(agentBackfillMaxAge / time.Second)
	full := int64(agentBackfillFullWindow / time.Second)
	mid := int64(agentBackfillMidWindow / time.Second)
	midGap := int64(agentBackfillMidSpacing / time.Second)
	oldGap := int64(agentBackfillOldSpacing / time.Second)

	kept := t.backfill[:0]
	var lastKept int64 = -1 << 62
	for _, s := range t.backfill {
		age := now - s.Ts
		if age > maxAge {
			continue // 超过 7 天：丢
		}
		gap := int64(0)
		switch {
		case age <= full:
			gap = 0 // 最近 1 小时全留
		case age <= mid:
			gap = midGap
		default:
			gap = oldGap
		}
		if gap > 0 && s.Ts-lastKept < gap {
			continue
		}
		kept = append(kept, s)
		lastKept = s.Ts
	}
	t.backfill = kept
	// 兜底硬上限：极端采集间隔下仍不允许无界增长，丢最旧的。
	if n := len(t.backfill) - agentBackfillMaxSamples; n > 0 {
		t.backfill = append(t.backfill[:0], t.backfill[n:]...)
	}
}

// takeBackfill 取出最多 agentBackfillPerBatch 条待补传采样（最旧优先）。
func (t *serverTarget) takeBackfill() []shared.BackfillSample {
	t.backfillMu.Lock()
	defer t.backfillMu.Unlock()
	n := len(t.backfill)
	if n == 0 {
		return nil
	}
	if n > agentBackfillPerBatch {
		n = agentBackfillPerBatch
	}
	out := make([]shared.BackfillSample, n)
	copy(out, t.backfill[:n])
	t.backfill = append(t.backfill[:0], t.backfill[n:]...)
	return out
}

// backfillPending 返回当前还欠着多少条（用于日志/自检）。
func (t *serverTarget) backfillPending() int {
	t.backfillMu.Lock()
	defer t.backfillMu.Unlock()
	return len(t.backfill)
}

// returnBackfill 把一批没送成的补传样本放回队首，保持时间顺序。
func (t *serverTarget) returnBackfill(items []shared.BackfillSample) {
	if len(items) == 0 {
		return
	}
	t.backfillMu.Lock()
	defer t.backfillMu.Unlock()
	t.backfill = append(items, t.backfill...)
	newest := t.backfill[len(t.backfill)-1].Ts
	t.pruneBackfillLocked(newest)
}

// refreshToken loads AIOPS_TOKEN_FILE (or the path stored on this target) when
// present. Safe to call repeatedly — used while waiting for the server to
// publish .install_token on first boot.
func (t *serverTarget) refreshToken() {
	if t.tokenFile == "" {
		return
	}
	b, err := os.ReadFile(t.tokenFile)
	if err != nil {
		return
	}
	if tok := strings.TrimSpace(string(b)); tok != "" {
		t.token = tok
	}
}

// register sends the agent's identity (with this target's token) to the server.
// On success the target is marked registered; 403 or network errors return false
// but don't crash — the agent keeps retrying on subsequent report cycles.
// Token is never logged in full — only the first 4 chars for debugging.
func (t *serverTarget) register(base shared.Report) bool {
	t.refreshToken()
	body, _ := json.Marshal(map[string]string{
		"host_id":     base.HostID,
		"hostname":    base.Hostname,
		"token":       t.token,
		"fingerprint": base.Fingerprint,
	})
	resp, err := t.httpc.Post(t.server+"/api/v1/agent/register", "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Error("注册失败(将继续上报)", "server", t.server, "err", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("注册被拒，可能 Token 已失效或指纹无效", "server", t.server, "status", resp.StatusCode)
		return false
	}
	// 取出服务端下发的日志加密密钥（base64）；之后每批日志用它 gzip+AES-GCM 加密上报。
	// host_id 是服务端认定的**规范身份**：与我们上报的不同即表示"这台机器早有记录"
	// （重装换了随机 id），调用方应改用它，否则历史数据会被劈成两半。
	var rr struct {
		LogKey string `json:"log_key"`
		HostID string `json:"host_id"`
	}
	if json.NewDecoder(resp.Body).Decode(&rr) == nil {
		if rr.LogKey != "" {
			if k, err := base64.StdEncoding.DecodeString(rr.LogKey); err == nil && len(k) == 32 {
				t.logKey = k
			}
		}
	}
	t.regMu.Lock()
	// Only overwrite on a real value — a body we failed to decode must not wipe
	// the canonical id we are already reporting under.
	if strings.TrimSpace(rr.HostID) != "" {
		t.canonicalHostID = strings.TrimSpace(rr.HostID)
	}
	t.registered = true
	t.regMu.Unlock()
	slog.Info("已向服务端注册", "server", t.server, "token_prefix", maskToken(t.token))
	return true
}

// isRegistered returns whether this target was successfully registered.
func (t *serverTarget) isRegistered() bool {
	t.regMu.Lock()
	defer t.regMu.Unlock()
	return t.registered
}

// hostIDOr returns the id THIS panel knows this machine by, falling back to the
// agent's local id before the first successful registration.
//
// 多服务端下这一步是必须的，不是优化：每个面板独立按指纹认领机器，两块面板完全
// 可能给同一台机器不同的 host_id（典型场景——机器重装换了随机 id，面板 A 是新记录
// 而面板 B 仍持有旧记录，注册时被 CanonicalHostID 认回旧 id）。服务端
// UpsertAuthenticated 严格按 host_id 查表，用错 id 就是 403 → 重新注册 → 再 403
// 的死循环：那块面板上这台机器永远离线，终端/桌面/端口转发也全部失效，而
// reconcileIdentity 在多服务端下会主动跳过（绝不能把 A 的 id 套给 B）。
func (t *serverTarget) hostIDOr(fallback string) string {
	t.regMu.Lock()
	defer t.regMu.Unlock()
	if t.canonicalHostID != "" {
		return t.canonicalHostID
	}
	return fallback
}

// send posts one report payload to this server. The report's Token field is
// set to this target's token before marshalling. The body is gzip-compressed
// when above the threshold to reduce bandwidth, UNLESS a previous 400 response
// triggered gzip degradation (proxy corruption on external networks).
// Returns errForbidden on 403 so the caller can re-register and retry.
// Returns errBadPayload on 400 when the body was gzip-compressed — the caller
// should disable gzip and retry.
var errBadPayload = errors.New("bad payload (server returned 400)")

func (t *serverTarget) send(rep shared.Report) error {
	rep.Token = t.token
	rep.HostID = t.hostIDOr(rep.HostID)
	// Tell each panel the base URL this agent actually reaches IT by. The server
	// stores it as Host.ServerURL and hands it back as the /dl download base for
	// self-update (agentReportedDownloadBase). Reporting servers[0] to every
	// target would tell panel B to serve upgrades from panel A — wrong artifact,
	// and unreachable whenever the two panels live on different networks. In
	// relay mode this is the relay URL, which is exactly right.
	rep.ServerURL = strings.TrimRight(strings.TrimSpace(t.server), "/")
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}

	// Decide whether to gzip: only if payload is large enough AND gzip has
	// not been disabled for this target (see sendWithRetry fallback).
	t.gzipMu.Lock()
	useGzip := !t.disableGzip
	t.gzipMu.Unlock()

	var reader *bytes.Reader
	contentEnc := ""
	if useGzip && len(body) >= gzipCompressThreshold {
		buf := getBytesBuf()
		defer putBytesBuf(buf)
		gw, _ := gzip.NewWriterLevel(buf, 3) // level 3 = best speed/size trade
		gw.Write(body)
		gw.Close()
		if buf.Len() < len(body) { // only compress if it actually shrinks
			reader = bytes.NewReader(buf.Bytes())
			contentEnc = "gzip"
		} else {
			reader = bytes.NewReader(body)
		}
	} else {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest("POST", t.server+"/api/v1/agent/report", reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if contentEnc != "" {
		req.Header.Set("Content-Encoding", contentEnc)
	}

	resp, err := t.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return errForbidden
	}
	if resp.StatusCode == http.StatusBadRequest {
		// 400 when we sent gzip → likely proxy corruption on external network.
		// Signal caller to disable gzip and retry immediately.
		if contentEnc == "gzip" {
			return errBadPayload
		}
		// 400 without gzip → genuine bad request, don't retry.
		return fmt.Errorf("服务端返回状态码 400（请求格式错误）")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("服务端返回状态码 %d", resp.StatusCode)
	}
	// 解析响应：服务端可能下发分布式探测任务（迭代 D）。解析失败不影响上报本身。
	var rr shared.ReportResponse
	if json.NewDecoder(io.LimitReader(resp.Body, 512<<10)).Decode(&rr) == nil && len(rr.ProbeTasks) > 0 {
		go t.runProbeTasks(rep.HostID, rep.Hostname, rep.Fingerprint, rr.ProbeTasks)
	}
	return nil
}

// sendWithRetry wraps send() with in-cycle retries and gzip degradation.
// On external networks, transient failures (proxy timeouts, gzip corruption)
// are common — retrying within the same cycle avoids wasting a full 10s
// report interval and prevents the circuit breaker from opening prematurely.
//
// Retry strategy:
//   - Up to 3 attempts per cycle (initial + 2 retries)
//   - 1s delay between retries (short enough to stay within one cycle)
//   - On 400 with gzip: disable gzip for this target, retry immediately
//   - On 403: re-register then retry
//   - Network errors / 5xx: retry after short delay
func (t *serverTarget) sendWithRetry(rep shared.Report) error {
	const maxAttempts = 3
	const retryDelay = 1 * time.Second

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			slog.Info("上报重试", "server", t.server, "attempt", attempt+1, "last_err", lastErr)
			time.Sleep(retryDelay)
		}

		err := t.send(rep)
		if err == nil {
			return nil // success
		}

		lastErr = err

		// 403 → re-register once, then retry
		if errors.Is(err, errForbidden) {
			slog.Warn("上报被拒(指纹未绑定)，重新注册后重试", "server", t.server)
			if t.register(rep) {
				// Registration succeeded, retry the send
				continue
			}
			return fmt.Errorf("注册失败，跳过本次上报")
		}

		// 400 with gzip → disable gzip for this target, retry without compression
		if errors.Is(err, errBadPayload) {
			slog.Warn("服务端返回400，疑似gzip被外网代理损坏，已禁用压缩", "server", t.server)
			t.gzipMu.Lock()
			t.disableGzip = true
			t.gzipMu.Unlock()
			continue // retry immediately without gzip
		}

		// Other errors (network timeout, 5xx, etc.) → retry
	}
	return lastErr
}

// Agent ties together the native collector (fast base metrics) and the plugin
// runner (slower custom/AI layer), then reports both to all configured backends.
// Metrics are collected exactly once per cycle and broadcast to every target —
// no duplicate collection regardless of how many servers are configured.
type Agent struct {
	targets        []*serverTarget
	reportInterval time.Duration
	pluginInterval time.Duration
	collector      Collector
	plugins        *PluginRunner
	identity       shared.Report // template with host fields pre-filled (Token is per-target)
	httpc          *http.Client  // used for non-report HTTP (e.g. plugin downloads)

	logPaths   []string // log files/dirs to tail and forward (empty = collector disabled)
	logEncrypt bool     // 加密上报日志（gzip+AES-GCM），有服务端下发密钥时生效；--log-encrypt=false 可关
	stateFile  string   // 身份状态文件路径；认回规范 host_id 后要写回这里

	// 新增采集器配置（可选，未配置时不启动）
	redfishTargets    []RedfishTarget
	oceanStorTargets  []OceanStorTarget
	netflowCfg        *NetFlowConfig
	packetCfg         *PacketConfig
	snmpCfg           *SNMPConfig
	sniCfg            *SNIConfig
	hypervInterval    time.Duration // Hyper-V 虚拟机采集间隔（0 → 默认 60s）
	hypervDisabled    bool          // 显式关闭 Hyper-V 采集（默认自动探测）
	containerInterval time.Duration
	containerDisabled bool

	// desktopDisabled skips the in-process web-desktop channel. Set by the
	// Windows service, which delegates screen capture/input to a helper worker
	// spawned into the active console session (so it can follow the secure
	// desktop, i.e. lock/login screens). Exactly one process must own the
	// desktop channel per host to avoid double-registration on the server.
	desktopDisabled bool

	mu            sync.Mutex
	latestCustom  map[string]float64
	pendingEvents []shared.Event
	latestBase    *shared.Metrics // from a core plugin, used when native unsupported
}

func NewAgent(servers []ServerConfig, reportInterval, pluginInterval time.Duration,
	collector Collector, plugins *PluginRunner, hostID, category string) *Agent {
	tokenFile := strings.TrimSpace(os.Getenv("AIOPS_TOKEN_FILE"))
	targets := make([]*serverTarget, len(servers))
	for i, s := range servers {
		targets[i] = &serverTarget{
			server:    s.Server,
			token:     s.Token,
			tokenFile: tokenFile,
			httpc:     newAgentHTTPClient(30 * time.Second),
			bo:        newBackoff(1*time.Second, 60*time.Second),
			cb:        newCircuitBreaker(8, 15*time.Second), // open after 8 consecutive failures, cooldown 15s — tuned for external networks where transient errors are common
		}
		targets[i].refreshToken()
	}
	serverURL := ""
	if len(targets) > 0 {
		serverURL = strings.TrimRight(strings.TrimSpace(targets[0].server), "/")
	}
	return &Agent{
		targets:        targets,
		reportInterval: reportInterval,
		pluginInterval: pluginInterval,
		collector:      collector,
		plugins:        plugins,
		httpc:          newAgentHTTPClient(30 * time.Second),
		latestCustom:   map[string]float64{},
		identity: shared.Report{
			HostID:       hostID,
			Hostname:     hostname(),
			OS:           runtime.GOOS,
			Platform:     osVersion(),
			Arch:         runtime.GOARCH,
			IP:           primaryIP(),
			Kernel:       kernelVersion(),
			Category:     category,
			AgentVersion: agentVersion(),
			ServerURL:    serverURL,
			Fingerprint:  machineFingerprint(),
		},
	}
}

// Run starts the agent's main loop. It registers to all targets, then
// runs collection => report cycles until interrupted.
// The main loop is wrapped in a defer/recover so a panic in any cycle
// (e.g. a nil dereference from a corrupted /proc read) can't kill the
// whole agent — it's logged and the loop restarts.
// reconcileIdentity asks the server which host id this machine is already known
// by, and adopts it before anything starts reporting.
//
// 必须在 Run 的最前面做：下面的采集器（Redfish / OceanStor / NetFlow）在构造时就
// 把 host_id 拷贝走了，认回身份要是晚于它们启动，这些采集器会一直用旧 id 上报。
//
// 尽力而为：服务端不可达时保持本地 id 继续跑（监控不能因为对不上身份就停摆），
// 下次启动会再试一次；届时服务端仍会按指纹把它认回同一条记录。
// allowedUpdateBases returns configured report server URLs that agent_update
// may download from (prevents playbook-supplied SSRF / cross-host redirects).
func (a *Agent) allowedUpdateBases() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.targets))
	for _, t := range a.targets {
		if t == nil {
			continue
		}
		s := strings.TrimRight(strings.TrimSpace(t.server), "/")
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (a *Agent) reconcileIdentity() {
	if a.identity.Fingerprint == "" {
		return // 拿不到机器指纹（无 machine-id 且无 MAC）时无从判定，保持原样
	}
	// Multi-server: never adopt one panel's host_id for all targets — that poisons
	// the other panels' history. Each server binds via fingerprint on register.
	if len(a.targets) > 1 {
		return
	}
	for _, t := range a.targets {
		if !t.register(a.identity) {
			continue // 该服务端不可达/拒绝，换下一个
		}
		canonical := t.hostIDOr("")
		if canonical == "" || canonical == a.identity.HostID {
			return // 服务端认可当前身份（或是不认识 host_id 的旧版服务端）
		}
		slog.Warn("服务端按机器指纹认回了既有身份，改用之（Agent 重装会换新的随机 id，"+
			"沿用新 id 会让这台机器的历史数据被劈成两半）",
			"old", short(a.identity.HostID), "canonical", short(canonical))
		a.identity.HostID = canonical
		persistHostID(a.stateFile, canonical, a.identity.Fingerprint)
		return
	}
}

// RunDesktopOnly runs ONLY the web-desktop channel for every target and nothing
// else (no metrics, plugins, terminal, forward…). It is the entry point for the
// Windows desktop worker process, which the service spawns into the active
// console session so screen capture/input can follow the secure desktop
// (lock/login screens). It blocks until ctx is cancelled.
func (a *Agent) RunDesktopOnly(ctx context.Context) {
	// The worker does not register (the service does), so reconcileIdentity would
	// be a no-op. Instead re-read the canonical host id the service may have just
	// persisted, so deskWait's host param matches the server's host record.
	if a.stateFile != "" {
		if id := readHostIDFromState(a.stateFile); id != "" {
			a.identity.HostID = id
		}
	}
	slog.Info("桌面 worker 启动",
		"host", a.identity.Hostname,
		"id", short(a.identity.HostID),
		"servers", len(a.targets))
	if a.identity.Fingerprint == "" {
		slog.Error("桌面 worker 无机器指纹，无法通过服务端鉴权，退出")
		return
	}
	var wg sync.WaitGroup
	for _, t := range a.targets {
		wg.Add(1)
		tgt := t
		go func() {
			defer wg.Done()
			a.runDesktopChannelFor(tgt)
		}()
	}
	<-ctx.Done()
	slog.Info("桌面 worker 收到退出信号")
	// runDesktopChannelFor loops on deskWait; process exit tears it down.
}

func (a *Agent) Run(ctx context.Context) {
	// 认回规范身份必须先于一切上报与采集器启动
	a.reconcileIdentity()
	slog.Info("Agent 核心启动",
		"host", a.identity.Hostname,
		"os", a.identity.OS,
		"collector", a.collector.Name(),
		"id", short(a.identity.HostID),
		"servers", len(a.targets))
	for i, t := range a.targets {
		slog.Info("服务端", "index", i+1, "url", t.server, "token", maskToken(t.token))
	}
	if a.identity.Fingerprint != "" {
		slog.Info("机器指纹", "fingerprint", short(a.identity.Fingerprint))
	}
	if !a.collector.Supported() {
		slog.Info("提示: 当前平台无原生采集器，基础指标依赖 core 插件(plugins/core_metrics.py)")
	}

	// childWg tracks all goroutines spawned by Run so the caller can wait
	// for a clean drain on context cancellation.
	var childWg sync.WaitGroup

	// Register to all targets in the BACKGROUND. registerTarget retries 3× with
	// backoff sleeps; doing it synchronously here delayed startup by seconds per
	// unreachable server (the "slow start" symptom on hosts whose server is slow).
	// The report loop re-registers on 403, so the first reports self-heal.
	for _, t := range a.targets {
		tgt := t
		go a.registerTarget(tgt)
	}

	childWg.Add(1)
	go func() {
		defer childWg.Done()
		a.pluginLoop(ctx)
	}()

	// Start one terminal channel per target
	for _, t := range a.targets {
		childWg.Add(1)
		tgt := t
		go func() {
			defer childWg.Done()
			a.runTerminalChannelFor(tgt)
		}()
	}

	// Start one web-desktop channel per target (unless delegated to a worker).
	if !a.desktopDisabled {
		for _, t := range a.targets {
			childWg.Add(1)
			tgt := t
			go func() {
				defer childWg.Done()
				a.runDesktopChannelFor(tgt)
			}()
		}
	}

	// Start one forward channel per target
	for _, t := range a.targets {
		childWg.Add(1)
		tgt := t
		go func() {
			defer childWg.Done()
			a.runForwardChannelFor(tgt)
		}()
	}

	// Start one log collector per target
	for _, t := range a.targets {
		childWg.Add(1)
		tgt := t
		go func() {
			defer childWg.Done()
			a.runLogCollectorFor(tgt)
		}()
	}

	// Start one backfill drainer per target: it refills the gaps left by report
	// cycles that could not reach this server, strictly in the background so the
	// live report path stays untouched. See runBackfillDrainerFor.
	for _, t := range a.targets {
		childWg.Add(1)
		tgt := t
		go func() {
			defer childWg.Done()
			a.runBackfillDrainerFor(ctx, tgt)
		}()
	}

	// Start hardware collectors
	if len(a.redfishTargets) > 0 || len(a.oceanStorTargets) > 0 {
		agg := newHardwareAggregator(a.identity.HostID, a.identity.Fingerprint, a.postHardwareReport)
		if len(a.redfishTargets) > 0 {
			newRedfishCollector(a.redfishTargets, a.identity.HostID, a.identity.Fingerprint).run(agg.submit)
			slog.Info("Redfish 硬件采集器已启动", "targets", len(a.redfishTargets))
		}
		if len(a.oceanStorTargets) > 0 {
			newOceanStorCollector(a.oceanStorTargets, a.identity.HostID, a.identity.Fingerprint).run(agg.submit)
			slog.Info("OceanStor 存储采集器已启动", "targets", len(a.oceanStorTargets))
		}
	}

	// Start NetFlow receiver. run() contains a BLOCKING UDP read loop, so it MUST run
	// in its own goroutine — calling it inline would block Run() and prevent the main
	// report loop (base metrics) and every collector below from ever starting. That
	// was the "enable NetFlow → base monitoring dies / heavy-monitoring host hangs at
	// startup" bug. Not tracked in childWg: a best-effort UDP receiver has no critical
	// state to drain, so it's fine to let the process exit abandon it (fast shutdown).
	if a.netflowCfg != nil && a.netflowCfg.Listen != "" {
		nr := newNetflowReceiver(*a.netflowCfg, a.identity.HostID, a.identity.Fingerprint)
		go nr.run(func(rep shared.NetFlowReport) { a.postNetFlowReport(rep) })
		slog.Info("NetFlow 接收器已启动", "listen", a.netflowCfg.Listen)
	}

	// Start packet collector (also a blocking loop → own goroutine).
	if a.packetCfg != nil && a.packetCfg.Enabled {
		pc := newPacketCollector(*a.packetCfg, a.identity.HostID, a.identity.Fingerprint)
		go pc.run(func(rep shared.NetFlowReport) { a.postNetFlowReport(rep) })
		slog.Info("五元组包采集器已启动")
	}

	// Start SNMP poller + trap receiver（网络设备纳管）。poller.run() spawns a goroutine
	// per target and returns; trap receiver.run() has a BLOCKING UDP read loop, so it
	// too must run in its own goroutine (same reason as NetFlow above).
	if a.snmpCfg != nil {
		if len(a.snmpCfg.Targets) > 0 {
			sc := newSNMPCollector(*a.snmpCfg, a.identity.HostID, a.identity.Fingerprint)
			sc.run(a.postSNMPReport)
			slog.Info("SNMP 轮询采集器已启动", "targets", len(a.snmpCfg.Targets))
		}
		if a.snmpCfg.TrapEnabled {
			tr := newSNMPTrapReceiver(*a.snmpCfg, a.identity.HostID, a.identity.Fingerprint)
			go tr.run(a.postSNMPTrapReport)
			slog.Info("SNMP Trap 接收器已启动", "listen", a.snmpCfg.TrapListen)
		}
	}

	// Start DNS/SNI/content capture. Linux defaults to AF_PACKET; Windows/macOS
	// use TShark over Npcap/libpcap/BPF. Context cancellation also terminates the
	// external capture process, avoiding orphan tshark children after Agent stop.
	if a.sniCfg != nil && a.sniCfg.Enabled {
		sc := newSNICollector(*a.sniCfg, a.identity.HostID, a.identity.Fingerprint)
		go sc.run(ctx, a.postDNSMapReport, a.postContentAuditReport)
		slog.Info("SNI/DNS 抓取器已启动",
			"backend", effectiveCaptureBackend(a.sniCfg.CaptureBackend),
			"iface", a.sniCfg.Interface, "content_audit", a.sniCfg.ContentAudit)
	}

	// Start Hyper-V guest inventory collector.
	//
	// The Hyper-V PowerShell module autoloads lazily: on a slow host (notably
	// Windows Server 2012) the FIRST probe right after boot can exceed the probe
	// timeout and wrongly report "not a Hyper-V host". A single startup probe would
	// then disable guest collection for the whole process lifetime — the exact
	// "Server 2012 Hyper-V 依然采集不到虚拟机" symptom, persisting across reboots
	// because every boot loses the race. Re-probe with exponential backoff so a
	// slow first autoload (or Hyper-V enabled later) is eventually picked up, while
	// a genuine non-Hyper-V host is polled only occasionally.
	if !a.hypervDisabled {
		childWg.Add(1)
		go func() {
			defer childWg.Done()
			backoff := 15 * time.Second
			const maxBackoff = 10 * time.Minute
			for {
				if hypervAvailable() {
					slog.Info("Hyper-V 虚拟机采集器已启动")
					a.runHyperVCollector(ctx) // blocks until ctx is cancelled
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < maxBackoff {
					if backoff *= 2; backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			}
		}()
	}

	if !a.containerDisabled {
		childWg.Add(1)
		go func() {
			defer childWg.Done()
			backoff := 15 * time.Second
			const maxBackoff = 10 * time.Minute
			for {
				if containersAvailable() {
					slog.Info("主机容器采集器已启动")
					a.runContainerCollector(ctx)
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < maxBackoff {
					if backoff *= 2; backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			}
		}()
	}

	// base-metric report loop with context-aware ticker.
	a.reportOnceSafe()
	ticker := time.NewTicker(a.reportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("上报循环收到停止信号，等待子协程排空...")
			// Bound the drain: a collector goroutine blocked on a slow poll / in-flight
			// HTTP / UDP read that doesn't watch ctx would otherwise hang childWg.Wait()
			// until systemd's 90s SIGKILL — the "slow/laggy stop" symptom. Wait up to 5s
			// for a clean drain, then exit regardless.
			done := make(chan struct{})
			go func() { childWg.Wait(); close(done) }()
			select {
			case <-done:
				slog.Info("所有子协程已退出。")
			case <-time.After(5 * time.Second):
				slog.Warn("部分子协程未在 5s 内退出，直接结束（避免 systemctl stop 卡顿）")
			}
			return
		case <-ticker.C:
			a.reportOnceSafe()
		}
	}
}

// reportOnceSafe calls reportOnce inside defer/recover so a panic in
// collection or network I/O never stops the agent.
func (a *Agent) reportOnceSafe() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("上报循环 panic 已恢复（采集不中断）", "panic", r)
		}
	}()
	a.reportOnce()
}

// registerTarget tries to register to one server with exponential backoff.
// Best-effort: failures are logged but don't block startup — the agent will
// retry registration on the next 403 during reporting.
//
// Always attempts a real /register call (including empty token for anonymous
// mode). Compose sidecars may start before the server publishes .install_token;
// refreshToken inside register() picks it up on later attempts.
func (a *Agent) registerTarget(t *serverTarget) {
	const maxAttempts = 12
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if t.register(a.identity) {
			return
		}
		if attempt < maxAttempts-1 {
			d := t.bo.next()
			slog.Info("注册失败，等待后重试", "server", t.server, "wait", d.Round(time.Second))
			time.Sleep(d)
		}
	}
	slog.Warn("注册最终失败，将在上报时继续重试", "server", t.server)
}

// pluginLoop runs plugins on a slower tick, independently of the report loop.
// Wrapped in defer/recover so a panic in plugin execution (e.g. nil map from
// a corrupted plugin output) doesn't kill the whole agent.
func (a *Agent) pluginLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("插件循环 panic 已恢复，尝试重启", "panic", r)
			go a.pluginLoop(ctx)
		}
	}()
	a.runPlugins()
	ticker := time.NewTicker(a.pluginInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runPlugins()
		}
	}
}

func (a *Agent) runPlugins() {
	res := a.plugins.RunAll(func(format string, args ...any) {
		slog.Info(fmt.Sprintf(format, args...))
	})
	a.mu.Lock()
	if len(res.custom) > 0 {
		a.latestCustom = res.custom
	}
	if res.base != nil {
		a.latestBase = res.base
	}
	if len(res.events) > 0 {
		a.pendingEvents = append(a.pendingEvents, res.events...)
		slog.Info("插件产生事件", "count", len(res.events))
	}
	a.mu.Unlock()
}

// reportOnce collects metrics exactly once, then broadcasts the report to all
// configured server targets concurrently. Each target independently handles
// 403 (re-register + retry) and network errors — one server being down never
// blocks or affects the others. Events are re-queued only if ALL targets
// failed (at least one success means events were delivered).
//
// Circuit breaker: if a target has 8 consecutive failures (each already retried
// 3x internally), the breaker opens and we skip that target for 15s — preventing
// futile connection attempts that waste CPU and network resources. Threshold and
// cooldown are tuned for external networks: old values (5/30s) were too aggressive
// and caused agents to go "offline" after brief network jitter.
func (a *Agent) reportOnce() {
	var base shared.Metrics
	if a.collector.Supported() {
		m, err := a.collector.Collect()
		if err != nil {
			slog.Error("原生采集失败", "err", err)
		}
		base = m
	}

	a.mu.Lock()
	if !a.collector.Supported() && a.latestBase != nil {
		base = *a.latestBase
	}
	custom := make(map[string]float64, len(a.latestCustom))
	for k, v := range a.latestCustom {
		custom[k] = v
	}
	events := a.pendingEvents
	a.pendingEvents = nil
	a.mu.Unlock()

	// Refresh primary IP each cycle — NICs/APIPA can change after start,
	// and Windows Hyper-V hosts often expose 169.254 before a real LAN IP.
	if ip := primaryIP(); ip != "" {
		a.identity.IP = ip
	}

	// Build the base report (Token is set per-target inside send()).
	rep := a.identity
	rep.Metrics = base
	if len(custom) > 0 {
		rep.Custom = custom
	}
	rep.Events = events
	rep.Desktop = probeDesktopServices()
	// 采集时刻：这条样本万一送不出去，要靠它在补传时落回正确的时间点。
	// 实时上报本身不带它——实时样本由服务端在收到时打戳，保持原样。
	collectedAt := time.Now().Unix()
	// 本周期这条样本，发失败时要原样存起来（各 target 独立）。
	thisSample := shared.BackfillSample{Ts: collectedAt, Metrics: base}

	// Broadcast to all targets concurrently — each gets its own goroutine so
	// a slow/unreachable server can't block the others (30s timeout isolation).
	var wg sync.WaitGroup
	results := make([]bool, len(a.targets)) // results[i] = true if target i succeeded

	for i, t := range a.targets {
		// Circuit breaker check: skip targets whose breaker is open.
		// We still check allow() inside the goroutine so the half-open trial
		// works correctly.
		if t.cb.isOpen() {
			// v5.2.6: When circuit breaker opens, reset registration flag
			// so the next successful report cycle triggers re-registration.
			// This ensures the agent re-establishes its server-side state
			// after a server restart or network partition.
			t.regMu.Lock()
			t.registered = false
			t.regMu.Unlock()
			// 熔断期同样要攒着。这段窗口恰恰是服务端重启的高发期，跳过就等于
			// 把「最该补回来的那几十秒」直接扔掉。
			t.spoolBackfill(thisSample)
			results[i] = false
			continue
		}

		wg.Add(1)
		go func(idx int, tgt *serverTarget) {
			defer wg.Done()

			// Circuit breaker: skip if open (already checked above, but
			// double-check for the half-open race).
			if !tgt.cb.allow() {
				tgt.spoolBackfill(thisSample)
				results[idx] = false
				return
			}

			// v5.2.6: If not registered (e.g. after circuit breaker reset),
			// try to register before sending the report.
			if !tgt.isRegistered() {
				if tgt.register(rep) {
					slog.Info("断路器恢复后重新注册成功", "server", tgt.server)
				}
			}

			// 实时上报永远是那个"小而快"的包：补传**不搭这趟车**，由 runBackfillDrainerFor
			// 在后台用独立端点按自己的节奏慢慢滴。把几百 KB 的历史挂在每个周期的实时包上，
			// 会让链路差的机器连实时数据都送不出去——本末倒置。
			//
			// sendWithRetry handles in-cycle retries, gzip degradation,
			// and 403 re-registration — all within a single report cycle.
			err := tgt.sendWithRetry(rep)
			if err != nil {
				slog.Error("上报失败", "server", tgt.server, "err", err)
				tgt.spoolBackfill(thisSample)
				tgt.cb.failure()
				if tgt.cb.isOpen() {
					slog.Warn("断路器已打开，暂停向该服务端上报", "server", tgt.server)
					// v5.2.6: Reset registration on breaker open
					tgt.regMu.Lock()
					tgt.registered = false
					tgt.regMu.Unlock()
				}
				results[idx] = false
				return
			}
			tgt.cb.success()
			tgt.bo.reset()
			results[idx] = true
			slog.Info("上报成功",
				"server", tgt.server,
				"cpu", base.CPUPercent,
				"mem", base.MemPercent,
				"disk", base.DiskPercent,
				"custom", len(custom),
				"events", len(events))
		}(i, t)
	}
	wg.Wait()

	// Re-queue events only if ALL targets failed — at least one success means
	// the events were delivered (duplicates across servers are acceptable;
	// duplicates to the SAME server from re-queueing are not).
	allFailed := true
	for _, ok := range results {
		if ok {
			allFailed = false
			break
		}
	}
	if allFailed && len(events) > 0 {
		a.mu.Lock()
		a.pendingEvents = append(events, a.pendingEvents...)
		a.mu.Unlock()
	}
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// runBackfillDrainerFor 后台把中断期间攒下的采样一批一批补给这个 target。
//
// 三条设计原则，都是「实时优先」的直接推论：
//
//  1. **独立通道**。走 /api/v1/agent/backfill，不占用实时上报那条 POST，补传再慢、
//     再大、再失败，都不会影响每个周期的实时数据。
//  2. **只在链路健康时才滴**。断路器是开的（服务端还没恢复）就一条都不发——那只会
//     制造无谓的超时，还会把断路器一直摁住，反过来拖累实时上报的恢复。
//  3. **一次一批、留有间隔**。每 20 秒最多 60 条：服务端刚重启完最不需要的就是全网
//     Agent 同时灌历史。满缓冲（约 2600 条）大致 15 分钟补完，期间实时数据始终正常。
func (a *Agent) runBackfillDrainerFor(ctx context.Context, t *serverTarget) {
	// 相位错开：服务端重启后，全网 Agent 的断路器几乎在同一秒闭合，drainer 又是固定周期，
	// 于是几百台机器会在同一个 20 秒窗里齐刷刷发补传——正好砸在服务端刚起来、最脆弱的
	// 时刻。用 host_id 派生一个稳定偏移把它们摊开；确定性的（不是随机数），所以同一台机器
	// 每次启动的相位一致，便于复现问题。
	if d := backfillDrainPhase(a.identity.HostID + "|" + t.server); d > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
	}
	ticker := time.NewTicker(agentBackfillDrainPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if t.backfillPending() == 0 {
			continue
		}
		// 服务端仍不可用时不要浪费一次往返，等实时上报把断路器合上再说。
		if t.cb.isOpen() || !t.isRegistered() {
			continue
		}
		batch := t.takeBackfill()
		if len(batch) == 0 {
			continue
		}
		if err := a.postBackfill(t, batch); err != nil {
			t.returnBackfill(batch)
			slog.Warn("补传失败，样本已放回缓冲等待下一轮", "server", t.server, "samples", len(batch), "err", err)
			continue
		}
		slog.Info("已补传中断期间的采样", "server", t.server, "samples", len(batch),
			"oldest_ts", batch[0].Ts, "remaining", t.backfillPending())
	}
}

// backfillDrainPhase 用 FNV-1a 把一个稳定字符串映射到 [0, drainPeriod) 的偏移。
func backfillDrainPhase(seed string) time.Duration {
	var h uint32 = 2166136261
	for i := 0; i < len(seed); i++ {
		h ^= uint32(seed[i])
		h *= 16777619
	}
	period := int64(agentBackfillDrainPeriod / time.Millisecond)
	if period <= 0 {
		return 0
	}
	return time.Duration(int64(h)%period) * time.Millisecond
}

// postBackfill 把一批补传采样送到服务端（指纹鉴权，与 hardware/netflow 同构）。
func (a *Agent) postBackfill(t *serverTarget, batch []shared.BackfillSample) error {
	rep := shared.BackfillReport{
		HostID:     t.hostIDOr(a.identity.HostID),
		Hostname:   a.identity.Hostname,
		ReportedAt: time.Now().Unix(),
		Samples:    batch,
	}
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	// 补传包比实时包大一到两个数量级，压缩收益明显（同构指标 JSON 通常能压到 1/10）。
	buf := getBytesBuf()
	defer putBytesBuf(buf)
	gw, _ := gzip.NewWriterLevel(buf, 3)
	_, _ = gw.Write(body)
	_ = gw.Close()
	reader := bytes.NewReader(body)
	enc := ""
	if buf.Len() < len(body) {
		reader = bytes.NewReader(buf.Bytes())
		enc = "gzip"
	}
	req, err := http.NewRequest("POST", t.server+"/api/v1/agent/backfill", reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if enc != "" {
		req.Header.Set("Content-Encoding", enc)
	}
	if fp := a.identity.Fingerprint; fp != "" {
		req.Header.Set("X-Agent-Fingerprint", fp)
	}
	resp, err := t.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode == http.StatusNotFound {
		// 老服务端没有这个端点：补传对它没有意义，丢掉这批而不是无限重试。
		return nil
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("服务端返回状态码 %d", resp.StatusCode)
	}
	return nil
}

// postHardwareReport sends a Redfish hardware snapshot to all server targets.
func (a *Agent) postHardwareReport(rep shared.HardwareReport) {
	fp := a.identity.Fingerprint
	baseHostID := rep.HostID
	for _, t := range a.targets {
		go func(tgt *serverTarget) {
			// Marshal per target:每块面板认的 host_id 可能不同（见 hostIDOr），
			// 用错 id 这条上报会被 403 静默丢掉。
			r := rep
			r.HostID = tgt.hostIDOr(baseHostID)
			body, err := json.Marshal(r)
			if err != nil {
				slog.Warn("硬件上报序列化失败", "err", err)
				return
			}
			req, err := http.NewRequest("POST", tgt.server+"/api/v1/agent/hardware", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if fp != "" {
				req.Header.Set("X-Agent-Fingerprint", fp)
			}
			resp, err := tgt.httpc.Do(req)
			if err != nil {
				slog.Warn("硬件上报失败", "server", tgt.server, "err", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				// 读取响应体以便诊断拒绝原因（如 fingerprint mismatch）
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				slog.Warn("硬件上报被拒", "server", tgt.server, "status", resp.StatusCode,
					"host_id", r.HostID, "snapshots", len(rep.Snapshots), "body", string(respBody))
			} else {
				slog.Info("硬件上报成功", "server", tgt.server, "host_id", r.HostID,
					"snapshots", len(rep.Snapshots))
			}
		}(t)
	}
}

// postNetFlowReport sends aggregated NetFlow/packet flows to all server targets.
func (a *Agent) postNetFlowReport(rep shared.NetFlowReport) {
	fp := a.identity.Fingerprint
	baseHostID := rep.HostID
	for _, t := range a.targets {
		go func(tgt *serverTarget) {
			// Marshal per target:每块面板认的 host_id 可能不同（见 hostIDOr），
			// 用错 id 这条上报会被 403 静默丢掉。
			r := rep
			r.HostID = tgt.hostIDOr(baseHostID)
			body, err := json.Marshal(r)
			if err != nil {
				slog.Warn("NetFlow 上报序列化失败", "err", err)
				return
			}
			req, err := http.NewRequest("POST", tgt.server+"/api/v1/agent/netflow", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if fp != "" {
				req.Header.Set("X-Agent-Fingerprint", fp)
			}
			resp, err := tgt.httpc.Do(req)
			if err != nil {
				slog.Warn("NetFlow 上报失败", "server", tgt.server, "err", err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				slog.Warn("NetFlow 上报被拒", "server", tgt.server, "status", resp.StatusCode)
			}
		}(t)
	}
}

// postSNMPReport sends polled SNMP device metrics to all server targets.
// postDNSMapReport 上报 SNI/DNS 域名观测到所有 server（与 postSNMPReport 同构）。
func (a *Agent) postDNSMapReport(rep shared.DNSMapReport) {
	fp := a.identity.Fingerprint
	baseHostID := rep.HostID
	for _, t := range a.targets {
		go func(tgt *serverTarget) {
			// Marshal per target:每块面板认的 host_id 可能不同（见 hostIDOr），
			// 用错 id 这条上报会被 403 静默丢掉。
			r := rep
			r.HostID = tgt.hostIDOr(baseHostID)
			body, err := json.Marshal(r)
			if err != nil {
				slog.Warn("域名观测上报序列化失败", "err", err)
				return
			}
			req, err := http.NewRequest("POST", tgt.server+"/api/v1/agent/dnsmap", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if fp != "" {
				req.Header.Set("X-Agent-Fingerprint", fp)
			}
			resp, err := tgt.httpc.Do(req)
			if err != nil {
				slog.Warn("域名观测上报失败", "server", tgt.server, "err", err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				slog.Warn("域名观测上报被拒", "server", tgt.server, "status", resp.StatusCode)
			}
		}(t)
	}
}

// postContentAuditReport 上报明文 HTTP 内容审计事件（与 postDNSMapReport 同构）。
func (a *Agent) postContentAuditReport(rep shared.ContentAuditReport) {
	fp := a.identity.Fingerprint
	baseHostID := rep.HostID
	for _, t := range a.targets {
		go func(tgt *serverTarget) {
			// Marshal per target:每块面板认的 host_id 可能不同（见 hostIDOr），
			// 用错 id 这条上报会被 403 静默丢掉。
			r := rep
			r.HostID = tgt.hostIDOr(baseHostID)
			body, err := json.Marshal(r)
			if err != nil {
				slog.Warn("内容审计上报序列化失败", "err", err)
				return
			}
			req, err := http.NewRequest("POST", tgt.server+"/api/v1/agent/content-audit", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if fp != "" {
				req.Header.Set("X-Agent-Fingerprint", fp)
			}
			resp, err := tgt.httpc.Do(req)
			if err != nil {
				slog.Warn("内容审计上报失败", "server", tgt.server, "err", err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				slog.Warn("内容审计上报被拒", "server", tgt.server, "status", resp.StatusCode)
			}
		}(t)
	}
}

func (a *Agent) postSNMPReport(rep shared.SNMPReport) {
	fp := a.identity.Fingerprint
	baseHostID := rep.HostID
	for _, t := range a.targets {
		go func(tgt *serverTarget) {
			// Marshal per target:每块面板认的 host_id 可能不同（见 hostIDOr），
			// 用错 id 这条上报会被 403 静默丢掉。
			r := rep
			r.HostID = tgt.hostIDOr(baseHostID)
			body, err := json.Marshal(r)
			if err != nil {
				slog.Warn("SNMP 上报序列化失败", "err", err)
				return
			}
			req, err := http.NewRequest("POST", tgt.server+"/api/v1/agent/snmp", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if fp != "" {
				req.Header.Set("X-Agent-Fingerprint", fp)
			}
			resp, err := tgt.httpc.Do(req)
			if err != nil {
				slog.Warn("SNMP 上报失败", "server", tgt.server, "err", err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				slog.Warn("SNMP 上报被拒", "server", tgt.server, "status", resp.StatusCode)
			}
		}(t)
	}
}

// postSNMPTrapReport sends received SNMP traps to all server targets.
func (a *Agent) postSNMPTrapReport(rep shared.SNMPTrapReport) {
	fp := a.identity.Fingerprint
	baseHostID := rep.HostID
	for _, t := range a.targets {
		go func(tgt *serverTarget) {
			// Marshal per target:每块面板认的 host_id 可能不同（见 hostIDOr），
			// 用错 id 这条上报会被 403 静默丢掉。
			r := rep
			r.HostID = tgt.hostIDOr(baseHostID)
			body, err := json.Marshal(r)
			if err != nil {
				slog.Warn("SNMP Trap 上报序列化失败", "err", err)
				return
			}
			req, err := http.NewRequest("POST", tgt.server+"/api/v1/agent/snmp/trap", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if fp != "" {
				req.Header.Set("X-Agent-Fingerprint", fp)
			}
			resp, err := tgt.httpc.Do(req)
			if err != nil {
				slog.Warn("SNMP Trap 上报失败", "server", tgt.server, "err", err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				slog.Warn("SNMP Trap 上报被拒", "server", tgt.server, "status", resp.StatusCode)
			}
		}(t)
	}
}
