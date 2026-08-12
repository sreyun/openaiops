package main

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

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
//    appends the sender's address to the right, so in CDN→Nginx→Server the
//    header reads "clientIP, cdnEdgeIP" and [0] = clientIP (the real public IP)
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

// serverURL returns the externally-reachable base URL for agent install scripts.
// It "follows the browser": the generated install / uninstall command carries the
// exact address the admin used to reach the panel, which is by definition reachable.
//
// Priority:
//  1. public_url (explicit admin config or AIOPS_PUBLIC_URL env var) — the reliable
//     override for reverse-proxy / stable-domain deployments.
//  2. The request address the admin's browser used: X-Forwarded-Host/Proto behind a
//     proxy, otherwise r.Host (and r.TLS for scheme).
//
// We deliberately do NOT guess a LAN IP by scanning interfaces. Inside a container
// that resolves to the container's own docker-network address (e.g. 172.18.0.4),
// which is unreachable from anywhere else — the #1 cause of "install command points
// at the wrong address". Browsing the panel via a real address, or setting public_url,
// is both correct and predictable.
func (s *Server) serverURL(r *http.Request) string {
	var raw string
	if u := s.cfg.PublicURL(); u != "" {
		raw = u
	} else {
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
		raw = scheme + "://" + host
	}
	// If the admin reached the panel over HTTPS, never mint an http:// install URL
	// for default ports. Reverse proxies that 301 http→https cause Go's default
	// HTTP client to convert POST /api/v1/agent/register into GET → 404.
	if s.isHTTPS(r) {
		raw = preferHTTPSPublicBase(raw)
	}
	return strings.TrimRight(raw, "/")
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
	u.Host = u.Hostname()
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
