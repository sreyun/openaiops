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
func runRelay(listenAddr, upstream, relaySecret, installToken string) {
	target, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("Relay: 无效的上游地址 %q: %v", upstream, err)
	}

	proxy := newRelayProxy(target, 100*time.Millisecond)

	// Interactive channels get immediate flushing. The reverse terminal, remote
	// desktop and port-forward all ride plain HTTP streams (rx/tx) through this
	// relay; a 100ms buffer window is a 100ms lag on every keystroke and every
	// frame, which is exactly what makes a relayed session feel broken.
	streamProxy := newRelayProxy(target, -1) // -1 = flush after every write

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
			serveRelayInstallScript(w, r, upstream, relaySecret, installToken)
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
	for _, line := range relayInstallHints(relayPort, installToken) {
		slog.Info("内网机器安装命令", "cmd", line)
	}
	if strings.TrimSpace(installToken) == "" {
		slog.Warn("中继未持有安装 token：上面的命令也不带 token，服务端若开启校验，内网机器装完会注册被拒(403)",
			"fix", "在面板『安装 Agent → 网关中继』复制带 ?token= 的命令重跑一次安装")
	}

	if listenAddr == "" || strings.HasPrefix(listenAddr, ":") ||
		strings.HasPrefix(listenAddr, "0.0.0.0:") {
		slog.Warn("⚠ 监听地址绑定到所有网卡——如不需外部访问，建议用 --listen 192.168.x.x:8529 绑定到内网IP")
	}

	// 启动时探一次上游。中继回源不通时，内网每一台 agent 只会看到 502，而它们的运维
	// 看不到这台网关机的日志；这里把结论**提前**印在中继自己的日志第一屏，装机的人
	// systemctl status aiops-relay 一眼就能看到"到底是不是我这台出不去网"。
	go probeRelayUpstream(upstream, relaySecret)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Relay 启动失败: %v", err)
	}
}

// newRelayProxy 建一个指向 target 的反代，并把 Host 头改写成**上游的** Host。
//
// 改写 Host 是必须的，而它的缺失是一个看起来毫无道理的故障：内网机器能从中继上
// 装好 Agent、能下载二进制，却一连上报就收到一页 HTML 错误页（日志里是
// `服务端返回状态码 xxx: <html>…`）。
//
// 原因是 httputil.NewSingleHostReverseProxy **只改 URL，不改 Host 头**：内网 agent
// 发来的 Host 是中继自己的 192.168.x.x:8529，被原样送到上游。上游前面只要是按名字
// 分流的 nginx / 负载均衡（生产上几乎总是），这个 Host 落不到 aiops 那个 server 块
// 上，于是回来的是默认站点的 404/421 错误页，而不是 API 的 JSON。
//
// 为什么 /install.sh 与 /dl 不受影响、掩盖了这个坑：那两条路径走的是中继**自己构造**
// 的请求（serveRelayInstallScript / relayDLCache.fetch），Host 天然就是上游域名。于是
// 现场表现成"装得上、连不上"——最容易让人往 token 和指纹上想的组合。
//
// 改写 Host 的**副作用**要一起补掉：面板用"浏览器眼中的自己"做写请求的来源校验
// （Origin vs Host）。经中继打开面板时，Origin 是中继地址、Host 已被改成上游域名，
// 两边对不上 —— 读接口是 GET 不过这一关，于是又是那个熟悉的现象："界面一切正常，
// 一按保存就失败"。所以这里把改写前的 Host 原样留在转发头里：
//
//   - X-Forwarded-Host：标准写法，中继直连面板时够用；
//   - X-AIOps-Client-Host：上游若还有一层 nginx，`proxy_set_header X-Forwarded-Host`
//     会把上一条覆盖掉，这条自定义头 nginx 不会碰，是这种两层代理下唯一活得下来的线索。
//     它只参与来源校验，不参与安装地址生成（那条链有自己的端口补回逻辑，别互相干扰）。
func newRelayProxy(target *url.URL, flush time.Duration) *httputil.ReverseProxy {
	p := httputil.NewSingleHostReverseProxy(target)
	p.FlushInterval = flush
	p.Transport = relayTransport
	orig := p.Director
	p.Director = func(r *http.Request) {
		clientHost := strings.TrimSpace(r.Host)
		orig(r)
		r.Host = target.Host
		if clientHost == "" {
			return
		}
		if r.Header.Get("X-Forwarded-Host") == "" {
			r.Header.Set("X-Forwarded-Host", clientHost)
		}
		r.Header.Set("X-AIOps-Client-Host", clientHost)
	}
	return p
}

// relayInstallHints 生成**能直接粘贴**的内网安装命令：真实网卡地址 + token。
//
// 原来这里印的是 http://<本机IP>:8529/install.sh —— 既没有地址也没有 token。照着抄的人
// 装完，内网机器一样注册不上（服务端开着安装 Token 校验就是 403）；而这恰恰是网关自己
// 刚踩过的坑。一条"看起来像命令的占位符"比不给提示更坑人。
//
// 多网卡时全列出来（最多 3 条）：网关按定义至少有内外两个网段，机器自己没法判断哪一个
// 是内网机器够得着的那个，与其猜错不如都摆出来让人挑。
func relayInstallHints(relayPort, installToken string) []string {
	q := ""
	if t := strings.TrimSpace(installToken); t != "" {
		q = "?token=" + t
	}
	ips := rankedLocalIPv4s()
	if len(ips) == 0 {
		return []string{"curl -fsSL \"http://<本机内网IP>" + relayPort + "/install.sh" + q + "\" | sh"}
	}
	if len(ips) > 3 {
		ips = ips[:3]
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, "curl -fsSL \"http://"+ip+relayPort+"/install.sh"+q+"\" | sh")
	}
	return out
}

// relayUpstreamProbe 是 http→https 升级探测，可在测试里替换。
var relayUpstreamProbe = probeUpgradeHTTPToHTTPS

// resolveRelayUpstream 决定中继回源用的上游地址。
//
// 它必须和上报走**同一次** http→https 升级：上报那条路径在 main 里晚于中继启动，由
// normalizeServersPreferHTTPS 把 http://host 探测升级成 https://host（上游 301 到 TLS，
// 或 80 端口压根不作答时）。中继却是在那之前按值取走 cfg.Server 的，于是同一个进程、
// 同一份配置会分裂成两个上游地址。
//
// 这个分裂在现场长这样，且极难自查：网关机自己的上报一切正常（https），内网每一台
// agent 的注册与上报却全是 502，中继日志里是 `upstream=http://… err=EOF`——EOF 正是
// 明文打到只收 TLS 的前门（nginx `return 444` / 只 listen 443）时的表现。域名相同、
// 配置只有一份，看上去"同一个地址一边通一边不通"。
func resolveRelayUpstream(server string) string {
	up := strings.TrimRight(strings.TrimSpace(server), "/")
	if u := strings.TrimRight(relayUpstreamProbe(up), "/"); u != "" && u != up {
		slog.Warn("中继上游只接受 HTTPS，回源地址已升级", "from", up, "to", u)
		return u
	}
	return up
}

// probeRelayUpstream 用**与真实回源完全相同的 Transport** 请求一次上游 /healthz。
// 必须同一个 Transport：代理、TLS 信任、HTTP 版本这三样只要有一样不同，探测通过而
// 回源 502 的情形就会出现，那比不探测更误导人。
func probeRelayUpstream(upstream, relaySecret string) {
	status, err := relayUpstreamHealth(upstream, relaySecret)
	if err != nil {
		slog.Error("Relay 回源自检失败：内网机器将全部收到 502", "upstream", upstream, "err", err)
		if strings.HasPrefix(strings.ToLower(upstream), "http://") {
			slog.Error("排查：上游是 http://——若服务端只开了 HTTPS，明文请求会被前门直接断开（正是 EOF）；" +
				"把 config.yaml 的 server 改成 https:// 后重启 aiops-relay。")
		}
		slog.Error("排查：① 本机能否直接访问上游（curl -I " + upstream + "）；" +
			"② 若需经 HTTP 代理出网，请在 aiops-relay 的 systemd 单元里加 " +
			"Environment=HTTP_PROXY=… HTTPS_PROXY=… NO_PROXY=…（服务不继承登录 shell 的环境变量）；" +
			"③ 上游自签证书请配置 ca_cert 或 tls_skip_verify。")
		return
	}
	if status >= 300 {
		slog.Warn("Relay 回源自检：上游返回非 2xx（回源链路通，但上游本身不健康）",
			"upstream", upstream, "status", status)
		return
	}
	slog.Info("Relay 回源自检通过", "upstream", upstream)
}

// relayUpstreamHealth 发出探测请求，返回上游状态码。
func relayUpstreamHealth(upstream, relaySecret string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(upstream, "/")+"/healthz", nil)
	if err != nil {
		return 0, err
	}
	if relaySecret != "" {
		req.Header.Set("X-Relay-Secret", relaySecret)
	}
	c := &http.Client{Transport: relayTransport, Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return resp.StatusCode, nil
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
// ForceAttemptHTTP2 关掉，和 reportTransport 保持一致（见 reporter.go 顶部那段说明）：
// HTTP/2 把所有请求复用到一条 TCP 连接上，上游一重启这条连接就死，中继上**整个内网**
// 的注册与上报会同时失败；HTTP/1.1 每个请求各拿一条连接，只有在途请求受影响。
// 另有一类现场故障也只在 h2 上出现：上游前面的反代 ALPN 谈成 h2 却不能真正承载，
// 表现为中继一切回源 502，而网关机自己的上报（HTTP/1.1）一切正常——最难自查的组合。
var relayTransport = &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 50,
	IdleConnTimeout:     90 * time.Second,
	ForceAttemptHTTP2:   false,
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

// serveRelayInstallScript 取上游的安装脚本、把 SERVER= 改写成中继地址后交给内网机器。
//
// 缺 token 时补上中继自己那一枚：内网机器装完照样要向上游注册，服务端开着安装 Token
// 校验时没有 token 就是 403——"装得上、面板里看不到"。而内网机器的运维手上往往只有
// 一句 curl http://网关:8529/install.sh，没有 token 的概念。中继本来就是这个内网的
// 装机来源，它手里的 token 也正是给这批机器用的，缺了就补，比让每台机器各自失败合理。
//
// 边界很清楚：**中继没配 token，就谁也补不到**——想收紧就别给网关配 token（网关自身
// 也就不再出现在主机列表里，这是同一个开关的两面）。请求自带 token 时一律以自带的为准。
func serveRelayInstallScript(w http.ResponseWriter, r *http.Request, upstream, relaySecret, installToken string) {
	q := r.URL.Query()
	if strings.TrimSpace(q.Get("token")) == "" && strings.TrimSpace(installToken) != "" {
		q.Set("token", installToken)
		slog.Info("内网装机请求未带 token，已用中继自己的安装 token 补上",
			"path", r.URL.Path, "remote", r.RemoteAddr)
	}
	upstreamURL := upstream + r.URL.Path
	if enc := q.Encode(); enc != "" {
		upstreamURL += "?" + enc
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
