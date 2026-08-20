package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CheckStatus is the latest runtime result of a custom check.
type CheckStatus struct {
	OK        bool
	Message   string
	LatencyMs float64 // response/connect time; for ping this is the average round-trip time
	CheckedAt int64
	// Type-specific detail (0 / -1 when not applicable):
	StatusCode int     // HTTP: last status code
	CertDays   int     // HTTP: days until the TLS certificate expires (-1 = not HTTPS / unknown)
	LossPct    float64 // ping: packet-loss percentage (-1 = not a ping check)
}

const selfCheckID = "__self_health__"

// checkHistMax bounds the per-check time-series ring kept for the "history
// curve" view (≈24h at the default 30s interval; longer at bigger intervals).
const checkHistMax = 2880

// CheckPoint is one sampled result kept for a check's trend chart.
type CheckPoint struct {
	Ts         int64   `json:"timestamp"`
	OK         bool    `json:"ok"`
	LatencyMs  float64 `json:"latency_ms"`
	StatusCode int     `json:"status_code,omitempty"`
	LossPct    float64 `json:"loss_pct,omitempty"`
	// Optional timing breakdown (written when probe records them; LOCF-aligned on VM read).
	DnsMs     float64 `json:"dns_ms,omitempty"`
	TcpMs     float64 `json:"tcp_ms,omitempty"`
	TlsMs     float64 `json:"tls_ms,omitempty"`
	TtfbMs    float64 `json:"ttfb_ms,omitempty"`
	CertDays  float64 `json:"cert_days,omitempty"`
	RespBytes float64 `json:"resp_bytes,omitempty"`
}

// checkRunner executes operator-defined synthetic checks (HTTP / TCP) on their
// intervals and raises alerts + notifications on failure and recovery.
type checkRunner struct {
	cfg      *ConfigStore
	store    *Store
	notifier *Notifier
	httpc    *http.Client
	httpcAPI *http.Client // API 业务监控专用：无全局超时，由 per-check TimeoutSec 经 context 精确控制（业务接口常慢于 5s）
	selfAddr string       // e.g. "127.0.0.1:8529" — used for the built-in self health-check

	mu        sync.Mutex
	status    map[string]CheckStatus
	down      map[string]bool
	downSince map[string]int64 // check id -> unix time it first went down
	lastRun   map[string]time.Time
	history   map[string][]CheckPoint // check id -> bounded time-series for the trend view
	failCount map[string]int          // consecutive failures (debounce: require 2 before marking down)
	okCount   map[string]int          // consecutive successes (debounce: require 2 before marking up)
	vm        *vmWriter               // 持久化拨测结果到 VictoriaMetrics（可选；重启后仍可查历史趋势）
}

func newCheckRunner(cfg *ConfigStore, store *Store, notifier *Notifier, selfAddr string) *checkRunner {
	httpc := newGuardedHTTPClient(5 * time.Second) // SSRF: block cloud metadata; private nets OK by default
	httpc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpcAPI := newGuardedHTTPClient(0)
	httpcAPI.Timeout = 0 // per-check TimeoutSec via context (apimon slow APIs)
	httpcAPI.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &checkRunner{
		cfg: cfg, store: store, notifier: notifier, selfAddr: selfAddr,
		httpc:     httpc,
		httpcAPI:  httpcAPI,
		status:    map[string]CheckStatus{},
		down:      map[string]bool{},
		downSince: map[string]int64{},
		lastRun:   map[string]time.Time{},
		history:   map[string][]CheckPoint{},
		failCount: map[string]int{},
		okCount:   map[string]int{},
	}
}

// recordHistory appends a bounded time-series point; the caller must hold cr.mu.
func (cr *checkRunner) recordHistory(id string, p CheckPoint) {
	h := append(cr.history[id], p)
	if len(h) > checkHistMax {
		h = h[len(h)-checkHistMax:]
	}
	cr.history[id] = h
}

// HistoryOf returns a copy of a check's trend series (nil if none yet).
func (cr *checkRunner) HistoryOf(id string) []CheckPoint {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	src := cr.history[id]
	if len(src) == 0 {
		return nil
	}
	out := make([]CheckPoint, len(src))
	copy(out, src)
	return out
}

// markDown records the down/up transition time; caller holds cr.mu.
func (cr *checkRunner) markDown(id string, down bool) {
	if down {
		if _, ok := cr.downSince[id]; !ok {
			cr.downSince[id] = time.Now().Unix()
		}
	} else {
		delete(cr.downSince, id)
	}
}

func (cr *checkRunner) Run(tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	cr.sweep()
	selfTick := time.NewTicker(30 * time.Second) // reduced from 10s to avoid excessive probing
	defer selfTick.Stop()
	if cr.selfAddr != "" {
		go cr.runSelfCheck() // run once immediately
	}
	for {
		select {
		case <-t.C:
			cr.sweep()
		case <-selfTick.C:
			if cr.selfAddr != "" {
				go cr.runSelfCheck()
			}
		}
	}
}

// runSelfCheck performs a lightweight HTTP check against the server's own
// /healthz endpoint. This replaces the old user-configured 127.0.0.1 check
// which fails inside Docker because 127.0.0.1 may not route correctly.
func (cr *checkRunner) runSelfCheck() {
	target := "http://127.0.0.1:" + portFromAddr(cr.selfAddr) + "/healthz"
	start := time.Now()
	resp, err := cr.httpc.Get(target)
	lat := float64(time.Since(start).Milliseconds())
	ok := false
	msg := ""
	code := 0
	if err != nil {
		msg = Tz("check.request_failed", err.Error())
	} else {
		code = resp.StatusCode
		resp.Body.Close()
		msg = "HTTP " + resp.Status
		ok = resp.StatusCode < 400
	}
	nowUnix := time.Now().Unix()
	cr.mu.Lock()
	cr.status[selfCheckID] = CheckStatus{OK: ok, Message: msg, LatencyMs: lat, CheckedAt: nowUnix, StatusCode: code, CertDays: -1}
	cr.recordHistory(selfCheckID, CheckPoint{Ts: nowUnix, OK: ok, LatencyMs: lat, StatusCode: code})
	wasDown := cr.down[selfCheckID]
	nowDown := wasDown
	// Same debounce as runCheck: require 2 consecutive results before toggling state
	const debounceThreshold = 2
	if !ok {
		cr.failCount[selfCheckID]++
		cr.okCount[selfCheckID] = 0
		if cr.failCount[selfCheckID] >= debounceThreshold && !wasDown {
			nowDown = true
			cr.down[selfCheckID] = true
			cr.markDown(selfCheckID, true)
		}
	} else {
		cr.okCount[selfCheckID]++
		cr.failCount[selfCheckID] = 0
		if cr.okCount[selfCheckID] >= debounceThreshold && wasDown {
			nowDown = false
			cr.down[selfCheckID] = false
			cr.markDown(selfCheckID, false)
		}
	}
	cr.mu.Unlock()

	// 持久化自检结果到 VM
	cr.vm.enqueueCheck(vmCheckSample{checkID: selfCheckID, name: SelfCheckName(), checkType: "http", ts: nowUnix, ok: ok, latencyMs: lat, statusCode: code, lossPct: -1})

	if nowDown && !wasDown {
		// Self health-check failures should not clutter the activity log
		// They are still tracked in alerts and visible in the checks view
	} else if !nowDown && wasDown {
		// Recovery also doesn't need an activity log entry for internal checks
	}
}

// portFromAddr extracts the port portion from an addr like ":8529" or "0.0.0.0:8529".
func portFromAddr(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i+1:]
	}
	return "8529"
}

func (cr *checkRunner) sweep() {
	now := time.Now()
	for _, c := range cr.cfg.Checks() {
		if !c.Enabled {
			continue
		}
		iv := c.IntervalSec
		if iv < 5 {
			iv = 30
		}
		cr.mu.Lock()
		last := cr.lastRun[c.ID]
		due := last.IsZero() || now.Sub(last) >= time.Duration(iv)*time.Second
		if due {
			cr.lastRun[c.ID] = now
		}
		cr.mu.Unlock()
		if due {
			cr.runCheck(c)
		}
	}
	cr.gc()
}

// gc drops runtime state for checks that no longer exist.
func (cr *checkRunner) gc() {
	live := map[string]bool{}
	for _, c := range cr.cfg.Checks() {
		live[c.ID] = true
	}
	cr.mu.Lock()
	for id := range cr.status {
		if id != selfCheckID && !live[id] {
			delete(cr.status, id)
			delete(cr.down, id)
			delete(cr.downSince, id)
			delete(cr.lastRun, id)
			delete(cr.history, id)
			delete(cr.failCount, id)
			delete(cr.okCount, id)
		}
	}
	cr.mu.Unlock()
}

func (cr *checkRunner) runCheck(c CustomCheck) {
	start := time.Now()
	var ok bool
	var msg string
	code, certDays := 0, -1
	lossPct, pingRTT := -1.0, -1.0
	var adv *httpProbeResult // HTTP 高级模式的分段计时结果（非高级模式为 nil）
	switch c.Type {
	case "http":
		if c.Advanced {
			r := cr.probeHTTPAdvanced(c)
			ok, msg, code, certDays = r.ok, r.msg, r.code, r.certDays
			adv = &r
		} else {
			ok, msg, code, certDays = cr.probeHTTP(c.Target)
		}
	case "tcp":
		ok, msg = cr.probeTCP(c.Target, checkTimeout(c, 5*time.Second))
	case "ping":
		ok, msg, pingRTT, lossPct = cr.probePing(c.Target)
	case "process":
		ok, msg = cr.probeProcess(c.Target)
	case "udp":
		ok, msg = cr.probeUDP(c.Target, checkTimeout(c, 5*time.Second))
	case "dns":
		ok, msg = cr.probeDNS(c, checkTimeout(c, 5*time.Second))
	default:
		ok, msg = false, Tz("check.unknown_type", c.Type)
	}
	lat := float64(time.Since(start).Milliseconds())
	if c.Type == "ping" { // ping "latency" is the ICMP round-trip time, not the command duration
		if pingRTT >= 0 {
			lat = pingRTT
		} else {
			lat = 0
		}
	}
	if adv != nil && adv.totalMs > 0 { // 高级模式用精确测得的总耗时
		lat = adv.totalMs
	}
	nowUnix := time.Now().Unix()
	cr.mu.Lock()
	cr.status[c.ID] = CheckStatus{OK: ok, Message: msg, LatencyMs: lat, CheckedAt: nowUnix, StatusCode: code, CertDays: certDays, LossPct: lossPct}
	cr.recordHistory(c.ID, CheckPoint{Ts: nowUnix, OK: ok, LatencyMs: lat, StatusCode: code, LossPct: lossPct})
	wasDown := cr.down[c.ID]
	nowDown := wasDown
	// Debounce: require 2 consecutive failures before marking down,
	// and 2 consecutive successes before marking up. This prevents
	// transient flaps (e.g. HTTP timeout at the boundary) from causing
	// the alert to intermittently appear/disappear in the UI.
	const debounceThreshold = 2
	if !ok {
		cr.failCount[c.ID]++
		cr.okCount[c.ID] = 0
		if cr.failCount[c.ID] >= debounceThreshold && !wasDown {
			nowDown = true
			cr.down[c.ID] = true
			cr.markDown(c.ID, true)
		}
	} else {
		cr.okCount[c.ID]++
		cr.failCount[c.ID] = 0
		if cr.okCount[c.ID] >= debounceThreshold && wasDown {
			nowDown = false
			cr.down[c.ID] = false
			cr.markDown(c.ID, false)
		}
	}
	cr.mu.Unlock()

	// 持久化本次拨测结果到 VM（重启不丢，供历史曲线读取）；高级模式附带分段计时
	cs := vmCheckSample{checkID: c.ID, name: c.Name, checkType: c.Type, ts: nowUnix, ok: ok, latencyMs: lat, statusCode: code, lossPct: lossPct, certDays: certDays}
	if adv != nil {
		cs.dnsMs, cs.tcpMs, cs.tlsMs, cs.ttfbMs = adv.dnsMs, adv.tcpMs, adv.tlsMs, adv.ttfbMs
	}
	cr.vm.enqueueCheck(cs)

	// Only fire transition notifications when the debounced down state actually changes
	if nowDown && !wasDown {
		cr.transition(c, false, msg)
	} else if !nowDown && wasDown {
		cr.transition(c, true, msg)
	}
}

func (cr *checkRunner) transition(c CustomCheck, up bool, msg string) {
	lvl := c.Level
	if lvl == "" {
		lvl = "critical"
	}
	a := Alert{Level: lvl, Type: "check", Scope: c.ID, Hostname: c.Name, Timestamp: time.Now().Unix()}
	if up {
		a.Level = "info"
		a.Message = Tz("check.custom_recovered", c.Name)
	} else {
		a.Message = Tz("check.custom_failed", c.Name, msg)
	}
	cr.store.AddLog(LogEntry{Kind: KindSystem, Level: a.Level, Actor: Tz("check.custom_monitor"), Host: c.Name, Message: a.Message})
	if cfg := cr.cfg.Get(); cfg.AlertsEnabled {
		cr.notifier.pushChannels(cfg, a, !up)
	}
}

// probeHTTP returns (ok, message, statusCode, certDaysRemaining). certDays is -1
// for plain HTTP or when the certificate can't be read; for HTTPS it is the whole
// number of days until the leaf certificate's NotAfter.
func (cr *checkRunner) probeHTTP(target string) (bool, string, int, int) {
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "http://" + target
	}

	// Retry once on failure to avoid transient errors
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := cr.httpc.Get(target)
		if err == nil {
			certDays := certDaysRemaining(resp)
			code := resp.StatusCode
			resp.Body.Close()
			if code >= 400 {
				return false, "HTTP " + resp.Status, code, certDays
			}
			return true, "HTTP " + resp.Status, code, certDays
		}
		lastErr = err
		if attempt == 0 {
			time.Sleep(500 * time.Millisecond) // brief pause before retry
		}
	}
	return false, Tz("check.request_failed", lastErr.Error()), 0, -1
}

// certDaysRemaining returns whole days until the served leaf TLS certificate
// expires, or -1 for non-HTTPS responses or when no peer certificate is present.
func certDaysRemaining(resp *http.Response) int {
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return -1
	}
	notAfter := resp.TLS.PeerCertificates[0].NotAfter
	d := time.Until(notAfter).Hours() / 24
	return int(d)
}

// httpProbeResult 是高级 HTTP 拨测的完整结果（含分段计时）。
type httpProbeResult struct {
	ok                                   bool
	msg                                  string
	code, certDays                       int
	dnsMs, tcpMs, tlsMs, ttfbMs, totalMs float64
	bytes                                int64  // 响应体大小（读取上限内）
	body                                 []byte // 仅 CaptureBody=true 时填充（合成事务变量提取用）
}

// ms 把耗时转成毫秒，保留纳秒精度（亚毫秒的极快接口也不会被截断成 0）。
func ms(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

// probeHTTPAdvanced 执行高级 HTTP 拨测：自定义 方法/请求头/请求体（含静态鉴权头）+
// 分段计时(DNS/TCP/TLS/TTFB/总) + 状态码/关键字/JSON 断言校验 + 证书到期阈值。
func (cr *checkRunner) probeHTTPAdvanced(c CustomCheck) httpProbeResult {
	target := c.Target
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "http://" + target
	}
	method := strings.ToUpper(strings.TrimSpace(c.Method))
	if method == "" {
		method = "GET"
	}
	res := httpProbeResult{certDays: -1}
	var bodyReader io.Reader
	if c.Body != "" {
		bodyReader = strings.NewReader(c.Body)
	}
	req, err := http.NewRequest(method, target, bodyReader)
	if err != nil {
		res.msg = Tz("check.request_failed", err.Error())
		return res
	}
	for k, v := range c.Headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	if c.Body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.TraceParent && req.Header.Get("traceparent") == "" {
		if tp := genTraceparent(); tp != "" {
			req.Header.Set("traceparent", tp) // 合成探测可被后端 trace 关联（W3C Trace Context）
		}
	}

	var dnsStart, connStart, tlsStart, firstByte time.Time
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			if !dnsStart.IsZero() {
				res.dnsMs = ms(time.Since(dnsStart))
			}
		},
		ConnectStart: func(_, _ string) { connStart = time.Now() },
		ConnectDone: func(_, _ string, _ error) {
			if !connStart.IsZero() {
				res.tcpMs = ms(time.Since(connStart))
			}
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			if !tlsStart.IsZero() {
				res.tlsMs = ms(time.Since(tlsStart))
			}
		},
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}
	client := cr.httpc
	ctx := context.Background()
	if c.TimeoutSec > 0 {
		client = cr.httpcAPI // 无全局 5s 上限，靠 context 精确控制
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(c.TimeoutSec)*time.Second)
		defer cancel()
	}
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.msg = Tz("check.request_failed", err.Error())
		res.totalMs = ms(time.Since(start))
		return res
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10)) // 最多读 256KB 用于校验
	resp.Body.Close()
	res.bytes = int64(len(body))
	if c.CaptureBody {
		res.body = body // 合成事务：留存响应体供 jsonPathValue 提取变量
	}
	res.totalMs = ms(time.Since(start))
	if res.totalMs <= 0 {
		res.totalMs = 0.01 // 时钟粒度下极快响应可能测得 0：钳到极小正值，避免"0=无数据"歧义
	}
	if !firstByte.IsZero() {
		res.ttfbMs = ms(firstByte.Sub(start))
	}
	res.code = resp.StatusCode
	res.certDays = certDaysRemaining(resp)

	// 校验：状态码
	if c.ExpectStatus > 0 {
		if resp.StatusCode != c.ExpectStatus {
			res.msg = fmt.Sprintf("状态码 %d ≠ 期望 %d", resp.StatusCode, c.ExpectStatus)
			return res
		}
	} else if resp.StatusCode >= 400 {
		res.msg = "HTTP " + resp.Status
		return res
	}
	// 校验：响应体关键字（正则或纯文本）
	if c.ExpectKeyword != "" {
		if c.KeywordIsRegex {
			re, e := regexp.Compile(c.ExpectKeyword)
			if e != nil {
				res.msg = "关键字正则无效：" + e.Error()
				return res
			}
			if !re.Match(body) {
				res.msg = "响应未匹配正则：" + c.ExpectKeyword
				return res
			}
		} else if !strings.Contains(string(body), c.ExpectKeyword) {
			res.msg = "响应未包含关键字：" + c.ExpectKeyword
			return res
		}
	}
	// 校验：JSON 路径断言（如 code == 0）
	if c.JSONPath != "" {
		val, ok := jsonPathValue(body, c.JSONPath)
		if !ok {
			res.msg = "JSON 路径不存在：" + c.JSONPath
			return res
		}
		if c.JSONExpect != "" && val != c.JSONExpect {
			res.msg = fmt.Sprintf("JSON %s=%q ≠ 期望 %q", c.JSONPath, val, c.JSONExpect)
			return res
		}
	}
	// 校验：证书剩余天数低于阈值
	if c.CertWarnDays > 0 && res.certDays >= 0 && res.certDays < c.CertWarnDays {
		res.msg = fmt.Sprintf("证书剩余 %d 天 < 阈值 %d 天", res.certDays, c.CertWarnDays)
		return res
	}
	// GraphQL 语义校验：响应须含 data 且 errors 为空
	if c.GraphQL {
		if gqlErr := graphqlCheckErrors(body); gqlErr != "" {
			res.msg = gqlErr
			return res
		}
	}
	res.ok = true
	res.msg = fmt.Sprintf("HTTP %s · TTFB %.0fms", resp.Status, res.ttfbMs)
	return res
}

// graphqlCheckErrors 校验 GraphQL 响应：有 errors 则失败，缺 data 也失败。返回空串=通过。
func graphqlCheckErrors(body []byte) string {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "GraphQL 响应非合法 JSON"
	}
	if len(resp.Errors) > 0 {
		msg := resp.Errors[0].Message
		if msg == "" {
			msg = "GraphQL 返回错误"
		}
		return "GraphQL 错误：" + msg
	}
	if len(resp.Data) == 0 || string(resp.Data) == "null" {
		return "GraphQL 响应缺少 data"
	}
	return ""
}

// jsonPathValue 取 JSON 里点路径的值并转成字符串（用于断言比较）。支持对象逐级 key，如 code / data.token。
func jsonPathValue(body []byte, path string) (string, bool) {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return "", false
	}
	cur := v
	for _, key := range strings.Split(path, ".") {
		key = strings.TrimSpace(strings.TrimPrefix(key, "$"))
		if key == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[key]
		if !ok {
			return "", false
		}
	}
	switch t := cur.(type) {
	case string:
		return t, true
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), true
		}
		return strconv.FormatFloat(t, 'g', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	case nil:
		return "", true
	default:
		b, _ := json.Marshal(t)
		return string(b), true
	}
}

// checkTimeout 返回该检查的探测超时：优先 c.TimeoutSec（秒），否则用 def（默认 5s）。
// 让 TCP/UDP/DNS 拨测的超时可按检查配置，而非硬编码。
func checkTimeout(c CustomCheck, def time.Duration) time.Duration {
	if c.TimeoutSec > 0 {
		return time.Duration(c.TimeoutSec) * time.Second
	}
	return def
}

func (cr *checkRunner) probeTCP(target string, timeout time.Duration) (bool, string) {
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		return false, Tz("check.connect_failed", err.Error())
	}
	conn.Close()
	return true, Tz("check.connect_ok")
}

// probeUDP sends a small probe packet to target (host:port) and waits for any
// response. UDP is connectionless so the result is best-effort:
//   - Immediate ICMP "port unreachable" → port closed / not reachable → fail
//   - Any UDP response received → service alive → ok
//   - Read timeout (no response within 3s) → assumed ok (port may be open but
//     service silent, e.g. DNS without a valid query); treated as "no reply"
func (cr *checkRunner) probeUDP(target string, timeout time.Duration) (bool, string) {
	conn, err := net.DialTimeout("udp", target, timeout)
	if err != nil {
		return false, Tz("check.connect_failed", err.Error())
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	// Send a minimal probe payload (single byte)
	_, err = conn.Write([]byte{0x00})
	if err != nil {
		return false, Tz("check.connect_failed", err.Error())
	}
	buf := make([]byte, 1024)
	_, err = conn.Read(buf)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			// No response within timeout — port may be open but service silent
			return true, Tz("check.udp_no_reply")
		}
		// Check for ICMP "port unreachable" wrapped in net.OpError
		if opErr, ok := err.(*net.OpError); ok {
			return false, Tz("check.connect_failed", opErr.Err.Error())
		}
		return false, Tz("check.connect_failed", err.Error())
	}
	return true, Tz("check.connect_ok")
}

// probeDNS 解析一个域名并可选断言结果，支持记录类型(A/AAAA/CNAME/MX/TXT/NS)与自定义 DNS
// 服务器（Target 形如 "example.com" 或 "example.com@8.8.8.8"）。相比通用 UDP 探测（发单字节靠超时
// 猜活死），DNS 拨测发真正的查询、校验真实解析结果，是 DNS 健康的准确判据。
func (cr *checkRunner) probeDNS(c CustomCheck, timeout time.Duration) (bool, string) {
	host := strings.TrimSpace(c.Target)
	server := ""
	if i := strings.LastIndex(host, "@"); i >= 0 { // 域名@DNS服务器
		server = strings.TrimSpace(host[i+1:])
		host = strings.TrimSpace(host[:i])
	}
	if host == "" {
		return false, Tz("check.invalid_host")
	}
	rtype := strings.ToUpper(strings.TrimSpace(c.DNSType))
	if rtype == "" {
		rtype = "A"
	}
	resolver := net.DefaultResolver
	if server != "" {
		if !strings.Contains(server, ":") {
			server = net.JoinHostPort(server, "53")
		}
		resolver = &net.Resolver{
			PreferGo: true, // 用 Go 内建解析器，Dial 指向自定义服务器
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, server)
			},
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var answers []string
	var err error
	switch rtype {
	case "A", "AAAA":
		ipnet := "ip4"
		if rtype == "AAAA" {
			ipnet = "ip6"
		}
		var ips []net.IP
		ips, err = resolver.LookupIP(ctx, ipnet, host)
		for _, ip := range ips {
			answers = append(answers, ip.String())
		}
	case "CNAME":
		var cname string
		if cname, err = resolver.LookupCNAME(ctx, host); cname != "" {
			answers = append(answers, strings.TrimSuffix(cname, "."))
		}
	case "MX":
		var mxs []*net.MX
		mxs, err = resolver.LookupMX(ctx, host)
		for _, mx := range mxs {
			answers = append(answers, strings.TrimSuffix(mx.Host, "."))
		}
	case "TXT":
		answers, err = resolver.LookupTXT(ctx, host)
	case "NS":
		var nss []*net.NS
		nss, err = resolver.LookupNS(ctx, host)
		for _, ns := range nss {
			answers = append(answers, strings.TrimSuffix(ns.Host, "."))
		}
	default:
		return false, Tz("check.dns_bad_type", rtype)
	}
	if err != nil {
		return false, Tz("check.dns_failed", err.Error())
	}
	if len(answers) == 0 {
		return false, Tz("check.dns_empty")
	}
	joined := strings.Join(answers, ", ")
	if c.ExpectKeyword != "" && !strings.Contains(joined, c.ExpectKeyword) {
		return false, Tz("check.dns_mismatch", c.ExpectKeyword, joined)
	}
	return true, rtype + " → " + joined
}

// probePing runs the system `ping` (zero-dependency, no raw-socket privilege
// needed) against target and returns (ok, message, avgRTTms, lossPct). ICMP is
// unreachable/blocked → 100% loss → not ok. Reachable (any reply) → ok, with the
// loss percentage surfaced separately.
func (cr *checkRunner) probePing(target string) (bool, string, float64, float64) {
	target = strings.TrimSpace(target)
	// Guard against argument injection: target is passed as an argv element (no
	// shell), but a leading '-' or embedded whitespace could still be read as a
	// ping flag, so reject those outright.
	if target == "" || strings.HasPrefix(target, "-") || strings.ContainsAny(target, " \t\r\n") {
		return false, Tz("check.invalid_host"), -1, -1
	}
	const sent = 3
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "ping", "-n", strconv.Itoa(sent), "-w", "2000", target)
	case "darwin":
		cmd = exec.CommandContext(ctx, "ping", "-c", strconv.Itoa(sent), "-t", "6", target)
	default: // linux and other unix
		cmd = exec.CommandContext(ctx, "ping", "-c", strconv.Itoa(sent), "-W", "2", "-w", "6", target)
	}
	out, _ := cmd.CombinedOutput() // ping exits non-zero on 100% loss; parse output regardless
	received, avg := parsePingOutput(string(out))
	if received > sent {
		received = sent
	}
	loss := float64(sent-received) / float64(sent) * 100
	if received == 0 {
		return false, Tz("check.unreachable"), -1, 100
	}
	return true, Tz("check.reachable", avg, loss), avg, loss
}

// parsePingOutput counts successful replies and averages their round-trip times.
// It keys off the per-reply time marker, which every platform and locale prints:
// "time=" (Linux/macOS/EN-Windows), "time<" (Windows sub-1ms, e.g. "time<1ms"),
// and the Chinese-Windows "时间=" / "时间<". Keying off replies rather than the
// OS-specific summary line keeps the parser format-independent.
func parsePingOutput(out string) (received int, avgRTTms float64) {
	var sum float64
	for _, marker := range []string{"time=", "time<", "时间=", "时间<"} {
		idx := 0
		for {
			i := strings.Index(out[idx:], marker)
			if i < 0 {
				break
			}
			p := idx + i + len(marker)
			j := p
			for j < len(out) && (out[j] == '.' || (out[j] >= '0' && out[j] <= '9')) {
				j++
			}
			if v, err := strconv.ParseFloat(out[p:j], 64); err == nil {
				received++
				sum += v
			}
			idx = j + 1
		}
	}
	if received > 0 {
		avgRTTms = sum / float64(received)
	}
	return received, avgRTTms
}

// probeProcess checks whether a given process name is running on the target host.
// Target format: "hostID/processName" (e.g. "abc123/nginx").
func (cr *checkRunner) probeProcess(target string) (bool, string) {
	idx := -1
	for i := 0; i < len(target); i++ {
		if target[i] == '/' {
			idx = i
			break
		}
	}
	if idx <= 0 || idx >= len(target)-1 {
		return false, Tz("check.bad_format")
	}
	hostID := target[:idx]
	procName := target[idx+1:]

	procNames, ok := cr.store.GetProcessNames(hostID)
	if !ok || len(procNames) == 0 {
		return false, Tz("check.no_process_data", shortID(hostID))
	}
	for _, p := range procNames {
		// substring match, case-insensitive: "nginx" matches "nginx.exe"
		if strings.Contains(strings.ToLower(p), strings.ToLower(procName)) {
			return true, Tz("check.process_running", procName, p)
		}
	}
	return false, Tz("check.process_not_running", procName, len(procNames))
}

func (cr *checkRunner) snapshot() map[string]CheckStatus {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	out := make(map[string]CheckStatus, len(cr.status))
	for k, v := range cr.status {
		out[k] = v
	}
	return out
}

// SelfCheckName is the display name for the built-in self health-check.
func SelfCheckName() string { return Tz("check.self_name") }

// DownAlerts returns the currently-failing checks as alerts for the /alerts view.
// It also generates threshold-based alerts for probe metrics (ping loss/latency,
// TCP timeout, HTTP response/status, process failures) when thresholds are configured.
func (cr *checkRunner) DownAlerts() []Alert {
	var out []Alert
	th := cr.cfg.Thresholds()

	// Self health-check
	cr.mu.Lock()
	if cr.down[selfCheckID] {
		st := cr.status[selfCheckID]
		out = append(out, Alert{
			Level: "critical", Type: "check", Scope: selfCheckID, Hostname: SelfCheckName(),
			Since:   cr.downSince[selfCheckID],
			Message: Tz("check.self_failed", st.Message), Timestamp: st.CheckedAt,
		})
	}
	cr.mu.Unlock()

	checks := cr.cfg.Checks()
	cr.mu.Lock()
	defer cr.mu.Unlock()
	for _, c := range checks {
		if !c.Enabled {
			continue
		}
		st := cr.status[c.ID]
		now := time.Now().Unix()

		// Existing boolean down/up alert
		if cr.down[c.ID] {
			lvl := c.Level
			if lvl == "" {
				lvl = "critical"
			}
			out = append(out, Alert{
				Level: lvl, Type: "check", Scope: c.ID, Hostname: c.Name,
				Since:   cr.downSince[c.ID],
				Message: Tz("check.custom_failed", c.Name, st.Message), Timestamp: st.CheckedAt,
			})
		}

		// ---- Threshold-based probe alerts (generated alongside down/up alerts) ----
		switch c.Type {
		case "ping":
			// Ping 丢包率
			if st.LossPct >= 0 && th.CheckPingLossWarn > 0 {
				lv := classify(st.LossPct, th.CheckPingLossWarn, th.CheckPingLossCrit)
				if lv != "" {
					out = append(out, Alert{
						Level: lv, Type: "check", Scope: c.ID + "/ping_loss", Hostname: c.Name,
						Message: Tz("alert.check_ping_loss", st.LossPct),
						Value:   st.LossPct, Timestamp: now,
					})
				}
			}
			// Ping 平均延迟
			if st.LatencyMs > 0 && th.CheckPingLatencyWarn > 0 {
				lv := classify(st.LatencyMs, th.CheckPingLatencyWarn, th.CheckPingLatencyCrit)
				if lv != "" {
					out = append(out, Alert{
						Level: lv, Type: "check", Scope: c.ID + "/ping_latency", Hostname: c.Name,
						Message: Tz("alert.check_ping_latency", st.LatencyMs),
						Value:   st.LatencyMs, Timestamp: now,
					})
				}
			}
		case "tcp":
			// TCP 连接超时
			if st.LatencyMs > 0 && th.CheckTCPTimeoutWarn > 0 {
				lv := classify(st.LatencyMs, th.CheckTCPTimeoutWarn, th.CheckTCPTimeoutCrit)
				if lv != "" {
					out = append(out, Alert{
						Level: lv, Type: "check", Scope: c.ID + "/tcp_timeout", Hostname: c.Name,
						Message: Tz("alert.check_tcp_timeout", st.LatencyMs),
						Value:   st.LatencyMs, Timestamp: now,
					})
				}
			}
		case "udp":
			// UDP 探测超时
			if st.LatencyMs > 0 && th.CheckUDPTimeoutWarn > 0 {
				lv := classify(st.LatencyMs, th.CheckUDPTimeoutWarn, th.CheckUDPTimeoutCrit)
				if lv != "" {
					out = append(out, Alert{
						Level: lv, Type: "check", Scope: c.ID + "/udp_timeout", Hostname: c.Name,
						Message: Tz("alert.check_udp_timeout", st.LatencyMs),
						Value:   st.LatencyMs, Timestamp: now,
					})
				}
			}
		case "dns":
			// DNS 解析超时（解析延迟超阈值即告警，与 up/down 告警并行）
			if st.LatencyMs > 0 && th.CheckDNSTimeoutWarn > 0 {
				lv := classify(st.LatencyMs, th.CheckDNSTimeoutWarn, th.CheckDNSTimeoutCrit)
				if lv != "" {
					out = append(out, Alert{
						Level: lv, Type: "check", Scope: c.ID + "/dns_timeout", Hostname: c.Name,
						Message: Tz("alert.check_dns_timeout", st.LatencyMs),
						Value:   st.LatencyMs, Timestamp: now,
					})
				}
			}
		case "http":
			// HTTP 响应时间
			if st.LatencyMs > 0 && th.CheckHTTPRespWarn > 0 {
				lv := classify(st.LatencyMs, th.CheckHTTPRespWarn, th.CheckHTTPRespCrit)
				if lv != "" {
					out = append(out, Alert{
						Level: lv, Type: "check", Scope: c.ID + "/http_resp", Hostname: c.Name,
						Message: Tz("alert.check_http_resp", st.LatencyMs),
						Value:   st.LatencyMs, Timestamp: now,
					})
				}
			}
			// HTTP 非 2xx 状态码
			if st.StatusCode > 0 && st.StatusCode >= 400 && th.CheckHTTPStatusWarn > 0 {
				// Use failCount as proxy for non-2xx count (incremented on each failure)
				fc := float64(cr.failCount[c.ID])
				if fc > 0 {
					lv := classify(fc, float64(th.CheckHTTPStatusWarn), float64(th.CheckHTTPStatusCrit))
					if lv != "" {
						out = append(out, Alert{
							Level: lv, Type: "check", Scope: c.ID + "/http_status", Hostname: c.Name,
							Message: Tz("alert.check_http_status", st.StatusCode, cr.failCount[c.ID]),
							Value:   fc, Timestamp: now,
						})
					}
				}
			}
		case "process":
			// 进程存活失败次数
			if !st.OK && th.CheckProcFailWarn > 0 {
				fc := float64(cr.failCount[c.ID])
				if fc > 0 {
					lv := classify(fc, float64(th.CheckProcFailWarn), float64(th.CheckProcFailCrit))
					if lv != "" {
						out = append(out, Alert{
							Level: lv, Type: "check", Scope: c.ID + "/proc_fail", Hostname: c.Name,
							Message: Tz("alert.check_proc_fail", cr.failCount[c.ID]),
							Value:   fc, Timestamp: now,
						})
					}
				}
			}
		}
	}
	return out
}

// runNow triggers an immediate check (used right after add / edit).
func (cr *checkRunner) runNow(id string) {
	for _, c := range cr.cfg.Checks() {
		if c.ID == id && c.Enabled {
			cr.mu.Lock()
			cr.lastRun[c.ID] = time.Now()
			cr.mu.Unlock()
			go cr.runCheck(c)
			return
		}
	}
}
