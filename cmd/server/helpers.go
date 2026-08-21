package main

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// ---- 反向代理没被信任时的自查 ----
//
// trust_proxy 关着时，clientIP 只认 TCP 对端地址——面板在 nginx 后面，那就是 127.0.0.1。
// 后果不是"日志少个字段"这么轻：登录失败限流按 IP 计（5 分钟 8 次），**全公司共用同一个
// 127.0.0.1**，任何一个人连输错几次密码，其他人一起被挡在门外五分钟；审计日志里的来源
// IP 也全是 127.0.0.1，出了事查不到人。
//
// 又不能自作主张去信 X-Forwarded-For：面板若直接暴露在公网，伪造这个头就能绕过限流、
// 伪造审计来源。所以这里只做**识别 + 说清楚**：确认上游是本机/内网的一跳、且带着代理头，
// 就在日志里喊一次，并在真正被限流时把这条线索一并回给用户。
func proxyHeadersPresent(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "X-Forwarded-Host", "Forwarded", "CF-Connecting-IP"} {
		if strings.TrimSpace(r.Header.Get(h)) != "" {
			return true
		}
	}
	return false
}

// untrustedProxyDetected 报告"前面确实有一层反代，但 trust_proxy 没开"。
//
// 只在 TCP 对端是回环/内网地址时才算数：公网直连过来的伪造头不该把日志刷成告警。
func (s *Server) untrustedProxyDetected(r *http.Request) bool {
	if s == nil || s.cfg == nil || s.cfg.TrustProxy() || !proxyHeadersPresent(r) {
		return false
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

var untrustedProxyWarnOnce sync.Once

// warnUntrustedProxyOnce 在整个进程生命周期里只喊一次，免得刷屏。
func (s *Server) warnUntrustedProxyOnce(r *http.Request) {
	if !s.untrustedProxyDetected(r) {
		return
	}
	untrustedProxyWarnOnce.Do(func() {
		slog.Warn("检测到反向代理，但 trust_proxy 没有开启",
			"影响", "登录限流与审计日志里的客户端 IP 全部是代理地址：一个人连续输错密码会把所有人一起挡在门外，审计也追不到真实来源",
			"修复", `在 server_config.json 里设置 "trust_proxy": true（确认面板只经反代对外，且反代已转发 X-Real-IP / X-Forwarded-For）`,
			"peer", r.RemoteAddr, "x_forwarded_for", r.Header.Get("X-Forwarded-For"), "x_real_ip", r.Header.Get("X-Real-IP"))
	})
}

// parsePageLimitOffset reads ?limit=/?offset= for list endpoints that must keep
// answering older callers in the legacy shape: paged is false when neither
// parameter is present, and the caller then writes the bare array it always did.
// Same contract as parseListLimitOffset (containers), minus that endpoint's
// filter-specific triggers.
func parsePageLimitOffset(r *http.Request, def, max int) (limit, offset int, paged bool) {
	q := r.URL.Query()
	rawLimit, rawOffset := q.Get("limit"), q.Get("offset")
	if rawLimit == "" && rawOffset == "" {
		return 0, 0, false
	}
	limit, _ = strconv.Atoi(rawLimit)
	offset, _ = strconv.Atoi(rawOffset)
	if limit <= 0 {
		limit = def
	}
	if limit > max {
		limit = max
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset, true
}

// normalizeIPv6Loopback maps the IPv6 loopback "::1" to its IPv4 equivalent so
// audit logs show a consistent "127.0.0.1" regardless of whether the local
// connection arrived over IPv4 or IPv6.
func normalizeIPv6Loopback(ip string) string {
	if ip == "::1" {
		return "127.0.0.1"
	}
	return ip
}

// sanitizeClientIP trims and validates a candidate IP from a proxy header or
// RemoteAddr. Returns "" when the value is not a usable IP address.
func sanitizeClientIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Some proxies send host:port
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	// Strip surrounding brackets from bare IPv6 ("[::1]")
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	ip := net.ParseIP(raw)
	if ip == nil {
		return ""
	}
	return normalizeIPv6Loopback(ip.String())
}

// clientIP returns the request's client address for audit logs and login
// rate-limiting. Reverse-proxy headers are honored ONLY when trust_proxy is
// enabled — otherwise they are attacker-forgeable and a directly-exposed
// server would let anyone reset their rate-limit bucket (and forge audit-log
// origins) by spoofing a header, so we use the raw connection address instead.
//
// Extraction priority (when TrustProxy is on):
//  1. CF-Connecting-IP   — Cloudflare always sets this to the visitor's IP
//  2. X-Real-IP          — commonly set by nginx (proxy_set_header X-Real-IP $remote_addr)
//  3. X-Forwarded-For[0] — the LEFTMOST entry is the original client; each proxy
//     appends the sender's address to the right, so in CDN→Nginx→Server the
//     header reads "clientIP, cdnEdgeIP" and [0] = clientIP (the real public IP)
//  4. RemoteAddr          — direct TCP connection (fallback)
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy() {
		// 1. CF-Connecting-IP (Cloudflare — always the end-user's IP)
		if ip := sanitizeClientIP(r.Header.Get("CF-Connecting-IP")); ip != "" {
			return ip
		}
		// 2. X-Real-IP (nginx / hostproxy single-value header)
		if ip := sanitizeClientIP(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		// 3. X-Forwarded-For — first (leftmost) entry is the original client.
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			if idx := strings.Index(f, ","); idx >= 0 {
				f = f[:idx]
			}
			if ip := sanitizeClientIP(f); ip != "" {
				return ip
			}
		}
	}
	// 4. Fallback: raw TCP connection address
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := sanitizeClientIP(host); ip != "" {
			return ip
		}
		return normalizeIPv6Loopback(host)
	}
	if ip := sanitizeClientIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

// actorIP returns both the operator identity and the real client IP for audit
// logging. When the request carries an authenticated session the Actor is the
// username; otherwise it falls back to the client IP. The IP is always the
// resolved client address (honoring TrustProxy) regardless of authentication
// state, so every log entry is fully traceable even for logged-in users behind
// NAT / VPN / CDN.
func (s *Server) actorIP(r *http.Request) (actor, ip string) {
	ip = s.clientIP(r)
	if u, ok := s.currentUser(r); ok && u.Username != "" {
		return u.Username, ip
	}
	return ip, ip
}

// auditActor returns display actor, dedicated username field, and client IP.
func (s *Server) auditActor(r *http.Request) (actor, username, ip string) {
	ip = s.clientIP(r)
	if u, ok := s.currentUser(r); ok && u.Username != "" {
		return u.Username, u.Username, ip
	}
	return ip, "", ip
}

// addAuditLog fills Actor / Username / IP from the request when missing, then stores the entry.
func (s *Server) addAuditLog(r *http.Request, e LogEntry) {
	actor, user, ip := s.auditActor(r)
	if e.IP == "" {
		e.IP = ip
	}
	if e.Username == "" {
		e.Username = user
	}
	if e.Actor == "" || e.Actor == ip {
		e.Actor = actor
	}
	if e.Username == "" && e.Actor != "" && e.Actor != ip && !looksLikeIPAddr(e.Actor) {
		e.Username = e.Actor
	}
	s.store.AddLog(e)
}

func looksLikeIPAddr(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// rough: IPv4 or bracketed/colon IPv6 / host:port
	if strings.Count(s, ".") == 3 {
		ok := true
		for _, p := range strings.Split(strings.Split(s, ":")[0], ".") {
			if p == "" {
				ok = false
				break
			}
			for _, c := range p {
				if c < '0' || c > '9' {
					ok = false
					break
				}
			}
		}
		if ok {
			return true
		}
	}
	return strings.Contains(s, ":") && !strings.Contains(s, " ")
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// isHTTPS reports whether the request reached us over TLS, optionally honoring
// the X-Forwarded-Proto reverse-proxy header when trust_proxy is enabled.
// When trust_proxy is off (the default), X-Forwarded-Proto is ignored because
// a directly-exposed server would let an attacker forge it. Used to set the
// Secure flag on the session cookie.
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.cfg.TrustProxy() && r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	return false
}

// forwardedHTTPS reports whether the request reached the *edge* over TLS: either
// directly, or at a reverse proxy that said so via X-Forwarded-Proto.
//
// 它只喂给 preferHTTPSPublicBase，也就是只对**隐式 80 端口**的地址生效：
// http://panel.example.com → https://panel.example.com，而 http://10.0.0.9:8529
// 这种显式端口原样保留。所以 trust_proxy 关着时"忽略转发头"的既有约定没有被推翻——
// 变的只是"面板在 TLS 后面、地址又没写端口"这一种情形，那里 http 一定是错的。
//
// 与 isHTTPS 分开是刻意的：isHTTPS 决定 Cookie 的 Secure 位，那条路径继续受
// trust_proxy 门禁保护，不在这次改动范围内。这里只用来**生成对外地址**（安装命令、
// 中继上游），而伪造 X-Forwarded-Proto 在这个用途上无利可图：协议升级不改主机，
// 最坏是给一台没有 TLS 的服务端生成 https 命令，坏的只是伪造者自己那次安装。
//
// 不认它的代价则是实打实的：TLS 由前面的 nginx 终结时 r.TLS 永远是 nil，于是面板
// 明明是 https，生成的却是 http:// 的安装命令和中继上游——中继回源打到只收 TLS 的
// 前门被直接断开（EOF），内网全员 502，而网关机自己的上报却一切正常，极难自查。
func forwardedHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(firstForwardedValue(r.Header.Get("X-Forwarded-Proto")), "https")
}

// serverURL returns the externally-reachable base URL for agent install scripts.
// It "follows the browser": the generated install / uninstall command carries the
// exact address the admin used to reach the panel, which is by definition reachable.
//
// Priority:
//  1. public_url (explicit admin config or AIOPS_PUBLIC_URL env var) — the reliable
//     override for reverse-proxy / stable-domain deployments.
//  2. The request address the admin's browser used: X-Forwarded-Host/Proto behind a
//     proxy, otherwise r.Host (and r.TLS for scheme).
//  3. 端口若被反代抹掉（`proxy_set_header Host $host` 不带端口），再用
//     X-Forwarded-Port / Forwarded / Origin / 面板拼的 ?port= 补回来，见 recoverEdgePort。
//
// We deliberately do NOT guess a LAN IP by scanning interfaces. Inside a container
// that resolves to the container's own docker-network address (e.g. 172.18.0.4),
// which is unreachable from anywhere else — the #1 cause of "install command points
// at the wrong address". Browsing the panel via a real address, or setting public_url,
// is both correct and predictable.
func (s *Server) serverURL(r *http.Request) string {
	if u := s.cfg.PublicURL(); u != "" {
		// 显式配置优先，端口也照抄：管理员写了 https://a.bc.com 就是不带端口，
		// 不能再拿请求头去"补"一个他没写的端口。
		if forwardedHTTPS(r) {
			u = preferHTTPSPublicBase(u)
		}
		return strings.TrimRight(u, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	// Honor X-Forwarded-* only when trust_proxy is on — same gate as client IP /
	// Secure cookie — so a directly-exposed server cannot be tricked into minting
	// install commands that point at an attacker-controlled host.
	if s.cfg.TrustProxy() {
		if p := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); p != "" {
			scheme = p
		}
		if h := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); h != "" {
			host = h
		}
	}
	raw := scheme + "://" + host
	// If the admin reached the panel over HTTPS, never mint an http:// install URL
	// for default ports. Reverse proxies that 301 http→https cause Go's default
	// HTTP client to convert POST /api/v1/agent/register into GET → 404.
	if forwardedHTTPS(r) {
		raw = preferHTTPSPublicBase(raw)
	}
	// 端口补回必须在协议升级之后：先升级（隐式 80 → https），再补 8443，
	// 否则 preferHTTPSPublicBase 看见显式端口就不升级了，会生成 http://host:8443。
	return strings.TrimRight(recoverEdgePort(raw, r), "/")
}

// recoverEdgePort 补回被反向代理抹掉的对外端口。
//
// 面板开在 https://a.bc.com:8443，nginx 却按最常见的写法 `proxy_set_header Host $host`
// 转发（$host 不含端口），服务端看到的 r.Host 就只剩 a.bc.com——生成的安装命令与脚本里的
// SERVER= 于是指向默认 443，Agent 注册直接连不上，而面板本身一切正常，极难自查。
//
// 只在地址【没有显式端口】时生效，永远不覆盖已有端口。按可信度取第一个命中的线索：
//
//  1. ?port=（面板自己拼进安装命令的兜底——curl 那一跳没有 Referer，只能靠它）
//  2. X-Forwarded-Port（nginx/Traefik/ALB 的标准写法）
//  3. Forwarded: host=…:port（RFC 7239）
//  4. Origin / Referer 的端口（浏览器亲口说的地址，仅在主机名相同时采信）
//
// 与 forwardedHTTPS 同样不挂 trust_proxy 门禁，理由也相同：这些线索只能改端口、
// 改不了主机，伪造者最多把【自己那一次】安装命令指到同一台机器的另一个端口上。
func recoverEdgePort(raw string, r *http.Request) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || u.Port() != "" {
		return raw
	}
	host := u.Hostname()
	if host == "" {
		return raw
	}
	scheme := strings.ToLower(u.Scheme)
	hints := []string{
		installScriptPortParam(r),
		firstForwardedValue(r.Header.Get("X-Forwarded-Port")),
		forwardedHeaderPort(r.Header.Get("Forwarded")),
		browserOriginPort(r.Header.Get("Origin"), host),
		browserOriginPort(r.Header.Get("Referer"), host),
	}
	for _, h := range hints {
		if p := normalizeEdgePort(h, scheme); p != "" {
			u.Host = net.JoinHostPort(host, p)
			return u.String()
		}
	}
	return raw
}

// installScriptPortParam reads the ?port= hint the panel appends to the install
// one-liner. Restricted to the install scripts on purpose: those are the only
// requests where nobody can hand us a Referer (curl / irm on the target machine),
// and the OIDC/SSO callbacks build redirect URIs from the same serverURL — their
// query string comes from the IdP, not from us.
func installScriptPortParam(r *http.Request) string {
	switch r.URL.Path {
	case "/install.sh", "/install.ps1", "/install-relay.sh", "/install-relay.ps1":
		return r.URL.Query().Get("port")
	}
	return ""
}

// normalizeEdgePort keeps only a real port number, and drops the scheme's implicit
// default (adding ":443" to an https URL changes nothing but confuses operators).
func normalizeEdgePort(raw, scheme string) string {
	p := strings.TrimSpace(raw)
	if p == "" || len(p) > 5 {
		return ""
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return ""
	}
	if (scheme == "http" && n == 80) || (scheme == "https" && n == 443) {
		return ""
	}
	return strconv.Itoa(n)
}

// forwardedHeaderPort pulls the port out of the first element's host= parameter
// of an RFC 7239 Forwarded header (`for=1.2.3.4;host=a.bc.com:8443;proto=https`).
func forwardedHeaderPort(v string) string {
	first := firstForwardedValue(v)
	if first == "" {
		return ""
	}
	for _, part := range strings.Split(first, ";") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "host") {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		if _, port, err := net.SplitHostPort(val); err == nil {
			return port
		}
		return ""
	}
	return ""
}

// forwardedHeaderHost pulls the host out of the first element's host= parameter
// of an RFC 7239 Forwarded header (`for=1.2.3.4;host=a.bc.com:8443;proto=https`).
// Used by the CSRF Origin check when the proxy rewrote Host to an internal address.
func forwardedHeaderHost(v string) string {
	first := firstForwardedValue(v)
	if first == "" {
		return ""
	}
	for _, part := range strings.Split(first, ";") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "host") {
			continue
		}
		return strings.Trim(strings.TrimSpace(val), `"`)
	}
	return ""
}

// browserOriginPort returns the port of an Origin/Referer header, but only when it
// names the same host we already derived — a cross-origin page must not be able to
// bend the generated install address.
func browserOriginPort(raw, host string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || host == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return ""
	}
	if !strings.EqualFold(u.Hostname(), host) {
		return ""
	}
	return u.Port()
}

// preferHTTPSPublicBase upgrades http://host[/] (implicit :80) to https://host.
// Explicit non-80 ports (lab :8529 etc.) are left alone.
func preferHTTPSPublicBase(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || !strings.EqualFold(u.Scheme, "http") {
		return raw
	}
	if p := u.Port(); p != "" && p != "80" {
		return raw
	}
	u.Scheme = "https"
	// Hostname() 返回的 IPv6 字面量是不带方括号的，直接写回 Host 会拼出
	// https://2001:db8::1 —— 再解析就是"端口非法"，整条地址作废。
	if h := u.Hostname(); strings.Contains(h, ":") {
		u.Host = "[" + h + "]"
	} else {
		u.Host = h
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

// firstForwardedValue returns the first comma-separated token of an X-Forwarded-*
// header, trimmed. Proxies may append a list (e.g. "https, http"); the first entry
// is the value seen by the client-facing hop.
func firstForwardedValue(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// ---- secret masking helpers ----

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
}

// mergeSecrets keeps existing webhook/secret values when the incoming ones are
// blank or still masked, so the panel can submit without re-typing secrets.
func mergeSecrets(in *ServerConfig, old ServerConfig) {
	in.Feishu.Webhook = keepIfBlank(in.Feishu.Webhook, old.Feishu.Webhook)
	in.Dingtalk.Webhook = keepIfBlank(in.Dingtalk.Webhook, old.Dingtalk.Webhook)
	in.Dingtalk.Secret = keepIfBlank(in.Dingtalk.Secret, old.Dingtalk.Secret)
	in.CustomWebhook.URL = keepIfBlank(in.CustomWebhook.URL, old.CustomWebhook.URL)
	in.SMTP.Password = keepIfBlank(in.SMTP.Password, old.SMTP.Password)
	if in.SMTP.FromName == "" {
		in.SMTP.FromName = old.SMTP.FromName
	}
	// Custom webhook headers may carry auth tokens and are masked in GET responses;
	// restore the stored value when the browser submits a blank/masked placeholder.
	in.CustomWebhook.Headers = keepIfBlank(in.CustomWebhook.Headers, old.CustomWebhook.Headers)
	// 短信 / 语音的 AccessKey + SecretKey 在 GET 里被 maskSecret 脱敏（如 LTAI****GHIJ）。
	// 表单回传脱敏串时必须还原为原值——否则「发送测试」或再次保存会拿脱敏串当真实凭证去做
	// ACS3-HMAC-SHA256 签名，导致阿里云返回 SignatureDoesNotMatch / InvalidAccessKeyId。
	in.SMS.AccessKey = keepIfBlank(in.SMS.AccessKey, old.SMS.AccessKey)
	in.SMS.SecretKey = keepIfBlank(in.SMS.SecretKey, old.SMS.SecretKey)
	in.VoiceCall.AccessKey = keepIfBlank(in.VoiceCall.AccessKey, old.VoiceCall.AccessKey)
	in.VoiceCall.SecretKey = keepIfBlank(in.VoiceCall.SecretKey, old.VoiceCall.SecretKey)
	// 数据源 Basic Auth 密码同理：GET 脱敏，全量配置回传脱敏串时按 ID 还原原值。
	for i := range in.DataSources {
		if p := in.DataSources[i].AuthPass; p == "" || strings.Contains(p, "****") {
			for _, od := range old.DataSources {
				if od.ID == in.DataSources[i].ID {
					in.DataSources[i].AuthPass = od.AuthPass
					break
				}
			}
		}
	}
}

func keepIfBlank(newv, oldv string) string {
	t := strings.TrimSpace(newv)
	if t == "" || strings.Contains(t, "****") {
		return oldv
	}
	return newv
}

// smsSafeVarMax 是阿里云短信模板变量单字段上限（字符）。此前用 45 会截断主机 IP 与异常详情。
// 官方模板变量通常可到数百字；500 足以完整承载「主机+IP+类型+异常+时间」，仍远低于短信计费分段上限。
const smsSafeVarMax = 500

// voiceSafeVarMax 语音 TTS 模板变量更短一些，避免部分模板拒收过长变量。
const voiceSafeVarMax = 300

// smsSafeVar 清洗要塞进短信模板变量的文本，使其符合阿里云短信内容审核。
// 阿里云对变量内容有严格限制：不支持 emoji、换行、【】（签名专用）及多数特殊符号，
// 且单个变量长度有限——否则报 isv.UNSUPPORTED_SMS_CONTENT。这里：换行/制表→空格，
// 只保留 中文/字母/数字/常用标点，丢弃 emoji 等其它符号，折叠空白并截断到 smsSafeVarMax。
func smsSafeVar(s string) string {
	return smsSafeVarN(s, smsSafeVarMax)
}

func smsSafeVarN(s string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = smsSafeVarMax
	}
	s = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ',
			r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			unicode.Is(unicode.Han, r):
			b.WriteRune(r)
		case strings.ContainsRune("，。：；、！？（）().,:;-/_%+@", r):
			b.WriteRune(r)
		default:
			// 丢弃 emoji / 其它特殊符号（如 ✅ ★ 【 】）
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ") // 折叠多余空白
	if rs := []rune(out); len(rs) > maxRunes {
		out = string(rs[:maxRunes])
	}
	return out
}

// ---- install-script parameter sanitizers ----
// /install.sh and /install.ps1 are public and echo these query params into a
// shell/PowerShell script that a machine pipes straight to sh/iex. Any of them
// could otherwise carry quotes/`$`/backticks/`;` that break out of the quoted
// assignment and inject commands, so each is reduced to a safe charset. Real
// values (hex token, a URL, a category name) are unaffected.

func sanitizeToken(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 128 {
		s = s[:128]
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, s)
}

func sanitizeCategory(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.', r == ' ':
			return r
		case unicode.Is(unicode.Han, r):
			return r
		default:
			return -1
		}
	}, strings.TrimSpace(s))
	if rs := []rune(s); len(rs) > 48 {
		s = string(rs[:48])
	}
	return s
}

// sanitizeFolderID keeps install/report folder ids safe for URL + config embedding.
// Accepts the ungrouped sentinel or ids matching hf-[hex] / alphanumeric-hyphen form.
func sanitizeFolderID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if s == HostFolderUngroupedID {
		return HostFolderUngroupedID
	}
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// sanitizeServerURL validates an install/public server URL for safe embedding
// into install scripts. Only http(s) scheme+host(+port) is kept; userinfo,
// path, query and fragment are rejected. IPv6 hosts are re-emitted with brackets.
func sanitizeServerURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" || len(u) > 256 {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	if parsed.User != nil {
		return ""
	}
	host := parsed.Hostname()
	if host == "" || strings.ContainsAny(host, "/?#@ \\") {
		return ""
	}
	port := parsed.Port()
	var out string
	if strings.Contains(host, ":") {
		// IPv6 literal from Hostname() is unbracketed.
		if port != "" {
			out = scheme + "://[" + host + "]:" + port
		} else {
			out = scheme + "://[" + host + "]"
		}
	} else if port != "" {
		out = scheme + "://" + host + ":" + port
	} else {
		out = scheme + "://" + host
	}
	if len(out) > 256 {
		return ""
	}
	return out
}

// sanitizeUsername validates the login username: 2–32 chars of ASCII letters,
// digits, dot, dash or underscore. Returns "" when invalid.
func sanitizeUsername(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || len(s) > 32 {
		return ""
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
		if !ok {
			return ""
		}
	}
	return s
}
