package main

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// runRelay starts the agent in gateway relay mode: it listens on a local port
// and reverse-proxies all requests to the upstream cloud server. Internal
// machines that can't reach the internet point their agents at this relay
// instead of the cloud — only the gateway machine needs internet access.
//
// Install scripts (/install.sh, /install.ps1) are intercepted so SERVER= and
// embedded CONFIG_B64 point at the relay. Internal machines then download
// binaries and report metrics through the relay.
//
// v5.4.1: relaySecret is an optional shared secret that the relay injects as
// X-Relay-Secret on every proxied request. When configured on the upstream
// server, all agent-facing requests via the relay must carry this header.
func runRelay(listenAddr, upstream, relaySecret string) {
	target, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("Relay: 无效的上游地址 %q: %v", upstream, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = 100 * time.Millisecond
	proxy.Transport = relayTransport

	// Interactive channels get immediate flushing. The reverse terminal, remote
	// desktop and port-forward all ride plain HTTP streams (rx/tx) through this
	// relay; a 100ms buffer window is a 100ms lag on every keystroke and every
	// frame, which is exactly what makes a relayed session feel broken.
	streamProxy := httputil.NewSingleHostReverseProxy(target)
	streamProxy.FlushInterval = -1 // -1 = flush after every write
	streamProxy.Transport = relayTransport

	// 回源失败时把**真实原因**回给客户端。
	//
	// Go 的默认行为是吐一个裸 "502 Bad Gateway"，真实错误只进中继自己的日志。装机的人
	// 在被装的那台内网机器上，看不到中继的日志，于是只能看到一个不含任何线索的 502，
	// 连"是中继连不上上游"还是"上游没有这个文件"都分不出来。
	relayProxyError := func(w http.ResponseWriter, r *http.Request, e error) {
		slog.Warn("Relay 回源失败", "path", r.URL.Path, "upstream", upstream, "err", e)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "Relay: 回源失败\n路径: %s\n上游: %s\n原因: %v\n\n"+
			"排查：① 中继机能否直接访问上游（curl -I %s）；"+
			"② 若中继机需经 HTTP 代理出网，请为中继进程设置 HTTP_PROXY/HTTPS_PROXY/NO_PROXY；"+
			"③ 上游是否仍在监听该端口。\n", r.URL.Path, upstream, e, upstream)
	}
	proxy.ErrorHandler = relayProxyError
	streamProxy.ErrorHandler = relayProxyError

	dlCache := newRelayDLCache(upstream, relaySecret)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/install.sh" || r.URL.Path == "/install.ps1" {
			serveRelayInstallScript(w, r, upstream, relaySecret)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/dl/") {
			if dlCache.serve(w, r) {
				return
			}
		}
		// Never let a client smuggle its own relay secret upstream: we are the
		// only party allowed to assert "this request came through the relay".
		r.Header.Del("X-Relay-Secret")
		if relaySecret != "" {
			r.Header.Set("X-Relay-Secret", relaySecret)
		}
		if isRelayStreamingPath(r.URL.Path) {
			streamProxy.ServeHTTP(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	slog.Info("╔══════════════════════════════════════════════════════╗")
	slog.Info("║  AIOps Agent — 网关中继模式 (Relay)                    ║")
	slog.Info("║  监听: " + listenAddr + "  上游: " + upstream + "  ║")
	slog.Info("╚══════════════════════════════════════════════════════╝")
	relayPort := listenAddr
	if _, port, err := net.SplitHostPort(listenAddr); err == nil && port != "" {
		relayPort = ":" + port
	} else if !strings.HasPrefix(listenAddr, ":") {
		relayPort = ":" + listenAddr
	}
	slog.Info("内网机器安装命令", "cmd", "curl -fsSL http://<本机IP>"+relayPort+"/install.sh | sh")

	if listenAddr == "" || strings.HasPrefix(listenAddr, ":") ||
		strings.HasPrefix(listenAddr, "0.0.0.0:") {
		slog.Warn("⚠ 监听地址绑定到所有网卡——如不需外部访问，建议用 --listen 192.168.x.x:8529 绑定到内网IP")
	}

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Relay 启动失败: %v", err)
	}
}

// relayTransport 是中继回源用的连接池。
//
// Proxy 这一行是必须的，而且它的缺失曾经表现为一个极难自查的故障：**同一个上游，
// /install.sh 通、/dl 502**。原因是两条回源路径对代理的处理不一致——
// serveRelayInstallScript 走 relayClient（用 http.DefaultTransport，天然认
// HTTP_PROXY/HTTPS_PROXY/NO_PROXY），而 /dl 的缓存回源与直连代理都走本 Transport，
// 没有 Proxy 字段就是 nil，即"永远直连"。
//
// 中继机恰恰是最可能挂代理的那一台：它被选作网关，就是因为只有它能出网，而企业里
// "能出网"往往等于"经由 HTTP 代理出网"。于是安装脚本拿得到、二进制拿不到。
var relayTransport = &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 50,
	IdleConnTimeout:     90 * time.Second,
	ForceAttemptHTTP2:   true,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
}

var relayClient = &http.Client{Timeout: 30 * time.Second}

// relayDLFetchTimeout 限制一次回源下载的总时长。
//
// 必须有上限：fetch 是**持着该产物的锁**跑的，一个卡死的上游连接会把这个产物的所有
// /dl 请求永久挡住（内网所有机器同时装不上 Agent，且没有任何超时能把它解开）。
// 取值要照顾跨境慢链路上的多 MB 二进制，所以比常规请求超时宽得多。
const relayDLFetchTimeout = 10 * time.Minute

var relayDLClient = &http.Client{Transport: relayTransport, Timeout: relayDLFetchTimeout}

var serverLineRe = regexp.MustCompile(`((?:SERVER|\$Server)\s*=\s*")[^"]+(")`)

// Install scripts embed a full commented config.example.yaml reference; 1 MiB is safe.
const maxInstallScriptSize = 1 << 20

// isRelayStreamingPath marks the hijacked / long-lived byte pipes that must not
// sit in a flush buffer: the agent-facing terminal, desktop and forward rx/tx
// streams, plus the browser-facing WebSocket and HTTP-proxy paths in case an
// operator points a panel at the relay directly.
func isRelayStreamingPath(p string) bool {
	for _, pre := range []string{
		"/api/v1/agent/terminal/",
		"/api/v1/agent/desktop/",
		"/api/v1/agent/forward/",
		"/agent/terminal/",
		"/agent/desktop/",
		"/agent/forward/",
		"/proxy/",
		"/ws",
	} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return strings.Contains(p, "/terminal") || strings.Contains(p, "/desktop") ||
		strings.Contains(p, "/forward")
}

// relayPublicScheme reports the scheme internal machines should use to reach
// this relay. Defaults to http (the relay serves plaintext), but honours TLS
// termination in front of it — otherwise the rewritten install script hands out
// an http:// SERVER= that a TLS-only front door will reject.
func relayPublicScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if p := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))); p != "" {
		if i := strings.IndexByte(p, ','); i >= 0 {
			p = strings.TrimSpace(p[:i])
		}
		if p == "https" {
			return "https"
		}
	}
	return "http"
}

func sanitizeHost(h string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.' || r == ':' || r == '-' || r == '[' || r == ']':
			return r
		default:
			return -1
		}
	}, h)
}

func serveRelayInstallScript(w http.ResponseWriter, r *http.Request, upstream, relaySecret string) {
	upstreamURL := upstream + r.URL.Path
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET", upstreamURL, nil)
	if err != nil {
		http.Error(w, "Relay: 构建请求失败", http.StatusInternalServerError)
		return
	}
	if relaySecret != "" {
		req.Header.Set("X-Relay-Secret", relaySecret)
	}

	resp, err := relayClient.Do(req)
	if err != nil {
		http.Error(w, "Relay: 无法连接上游服务端 ("+upstream+")", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInstallScriptSize))
	if err != nil {
		http.Error(w, "Relay: 读取安装脚本失败", http.StatusInternalServerError)
		return
	}
	if len(body) >= maxInstallScriptSize {
		http.Error(w, "Relay: 安装脚本过大", http.StatusBadGateway)
		return
	}

	host := sanitizeHost(r.Host)
	if host == "" {
		http.Error(w, "Relay: 无效的 Host 头", http.StatusBadRequest)
		return
	}

	relayURL := relayPublicScheme(r) + "://" + host
	rewritten := rewriteInstallScriptForRelay(string(body), relayURL)

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.WriteString(w, rewritten)
}

const (
	// relayDLRevalidateAfter is how long a cached artifact is served without
	// asking upstream whether it changed.
	//
	// 必须很短：服务端升级后 /dl 下的二进制与 .sha256 是**成对**更新的，而中继旧缓存
	// 里的两者也彼此匹配。于是内网 agent 下载到旧二进制、SHA-256 校验通过、"升级成功"，
	// 版本却纹丝不动 —— 服务端的 pending_verify 超时、soft-retry，整条自动升级链路
	// 在缓存 TTL 内空转。用 ETag 条件回源换掉"盲信 TTL"，代价只有一次 HEAD。
	relayDLRevalidateAfter = 15 * time.Second
	// relayDLCacheTTL bounds the pair-generation check (binary vs .sha256 written
	// in the same fetch), not the trust window.
	relayDLCacheTTL = 10 * time.Minute
)

type relayDLCache struct {
	dir      string
	upstream string
	secret   string
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
}

func newRelayDLCache(upstream, secret string) *relayDLCache {
	dir := filepath.Join(os.TempDir(), "aiops-relay-dl-cache")
	_ = os.MkdirAll(dir, 0o755)
	return &relayDLCache{dir: dir, upstream: upstream, secret: secret, locks: map[string]*sync.Mutex{}}
}

func (c *relayDLCache) lockFor(name string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.locks[name]
	if l == nil {
		l = &sync.Mutex{}
		c.locks[name] = l
	}
	return l
}

// pairKey collapses binary + .sha256 into one lock/generation so they cannot desync.
func relayDLPairKey(name string) string {
	return strings.TrimSuffix(name, ".sha256")
}

func (c *relayDLCache) serve(w http.ResponseWriter, r *http.Request) bool {
	name := path.Base(r.URL.Path)
	if name == "" || name == "." || name == "/" || strings.ContainsAny(name, `/\`) {
		return false
	}
	cf := filepath.Join(c.dir, name)
	pair := relayDLPairKey(name)

	lk := c.lockFor(pair)
	lk.Lock()
	defer lk.Unlock()

	cached := false
	if fi, err := os.Stat(cf); err == nil && !fi.IsDir() && c.pairFresh(pair) {
		cached = true
		if time.Since(fi.ModTime()) < relayDLRevalidateAfter {
			http.ServeFile(w, r, cf) // just validated by another request
			return true
		}
	}

	if cached {
		switch c.revalidate(pair) {
		case relayRevalFresh:
			// Unchanged upstream: bump mtime so the next few requests skip the HEAD.
			now := time.Now()
			_ = os.Chtimes(cf, now, now)
			http.ServeFile(w, r, cf)
			return true
		case relayRevalUnreachable:
			// Cloud unreachable — serving the cached copy is the entire point of
			// relay mode for an isolated network. Loud, but not fatal.
			slog.Warn("Relay /dl 无法回源校验，先用本地缓存（内容可能已过期）", "pair", pair)
			http.ServeFile(w, r, cf)
			return true
		}
		slog.Info("Relay /dl 上游已变更，重新拉取", "pair", pair)
	}

	if err := c.fetchPair(pair); err != nil {
		if cached {
			slog.Warn("Relay /dl 回源失败，退回本地缓存", "pair", pair, "err", err)
			http.ServeFile(w, r, cf)
			return true
		}
		slog.Warn("Relay /dl 缓存回源失败，回退直连代理", "file", name, "err", err)
		return false
	}
	slog.Info("Relay /dl 缓存已刷新", "pair", pair)
	http.ServeFile(w, r, cf)
	return true
}

type relayRevalResult int

const (
	relayRevalStale relayRevalResult = iota
	relayRevalFresh
	relayRevalUnreachable
)

// revalidate asks upstream whether the cached artifact still matches, using the
// ETag the server sets on /dl (size-mtime for binaries, the digest for .sha256).
// One HEAD round-trip is negligible next to re-downloading a multi-MB agent, and
// it is what keeps a relayed fleet from upgrading into a stale binary.
func (c *relayDLCache) revalidate(pair string) relayRevalResult {
	want, err := os.ReadFile(filepath.Join(c.dir, pair+".etag"))
	if err != nil || len(want) == 0 {
		return relayRevalStale // never recorded → cannot trust the copy
	}
	req, err := http.NewRequest(http.MethodHead, c.upstream+"/dl/"+pair, nil)
	if err != nil {
		return relayRevalStale
	}
	if c.secret != "" {
		req.Header.Set("X-Relay-Secret", c.secret)
	}
	resp, err := relayClient.Do(req)
	if err != nil {
		return relayRevalUnreachable
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return relayRevalStale
	}
	if et := resp.Header.Get("ETag"); et != "" && et == string(want) {
		return relayRevalFresh
	}
	return relayRevalStale
}

func (c *relayDLCache) pairFresh(pair string) bool {
	bin := filepath.Join(c.dir, pair)
	sum := bin + ".sha256"
	fi1, err1 := os.Stat(bin)
	fi2, err2 := os.Stat(sum)
	if err1 != nil && err2 != nil {
		return false
	}
	now := time.Now()
	if err1 == nil && now.Sub(fi1.ModTime()) >= relayDLCacheTTL {
		return false
	}
	if err2 == nil && now.Sub(fi2.ModTime()) >= relayDLCacheTTL {
		return false
	}
	// If both exist, require mtimes within 30s (same generation).
	if err1 == nil && err2 == nil {
		d := fi1.ModTime().Sub(fi2.ModTime())
		if d < 0 {
			d = -d
		}
		if d > 30*time.Second {
			return false
		}
	}
	return true
}

func (c *relayDLCache) fetchPair(pair string) error {
	// Always refresh binary + checksum together when either is requested.
	paths := []string{"/dl/" + pair}
	if !strings.HasSuffix(pair, ".sha256") && !strings.HasSuffix(pair, ".zip") {
		paths = append(paths, "/dl/"+pair+".sha256")
	}
	var firstErr error
	for _, p := range paths {
		dst := filepath.Join(c.dir, path.Base(p))
		if err := c.fetch(p, dst); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// .sha256 missing is non-fatal for plugins.zip etc.
			if strings.HasSuffix(p, ".sha256") {
				continue
			}
			return err
		}
	}
	return firstErr
}

func (c *relayDLCache) fetch(urlPath, dst string) error {
	req, err := http.NewRequest("GET", c.upstream+urlPath, nil)
	if err != nil {
		return err
	}
	if c.secret != "" {
		req.Header.Set("X-Relay-Secret", c.secret)
	}
	// 用带总超时的 Client 而不是裸 RoundTrip：后者既没有超时，也不跟随重定向。
	resp, err := relayDLClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &relayDLError{status: resp.StatusCode}
	}
	tmp, err := os.CreateTemp(c.dir, "dl-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	tmp.Close()
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	// Record the upstream ETag next to the payload so later hits can be
	// revalidated with a HEAD instead of blindly trusting a time window.
	etagPath := dst + ".etag"
	if et := resp.Header.Get("ETag"); et != "" {
		_ = os.WriteFile(etagPath, []byte(et), 0o644)
	} else {
		_ = os.Remove(etagPath) // no validator → force a real refetch next time
	}
	return nil
}

type relayDLError struct{ status int }

func (e *relayDLError) Error() string { return "upstream status " + strconv.Itoa(e.status) }
