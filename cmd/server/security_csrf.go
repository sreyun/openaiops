package main

import (
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// csrfOriginMiddleware rejects mutating API calls whose Origin/Referer does not
// match the request Host or an explicitly allowed CORS origin. Same-origin
// dashboard traffic always passes; cross-site form POSTs are blocked.
func (s *Server) csrfOriginMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		// Agent / public bootstrap paths authenticate by token/fingerprint, not cookie.
		if isPublicPath(r) || strings.HasPrefix(p, "/api/v1/agent/") ||
			strings.HasPrefix(p, "/proxy/") || strings.HasPrefix(p, "/dl/") ||
			p == "/api/v1/prom/write" || p == "/api/v1/mcp" ||
			p == "/api/v1/integrations/content-audit" ||
			strings.HasPrefix(p, "/api/v1/auth/oidc/") {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(p, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if s.originAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		seen := strings.Join(s.selfHostCandidates(r), " / ")
		slog.Warn("CSRF/Origin 校验拒绝", "method", r.Method, "path", p,
			"origin", r.Header.Get("Origin"), "referer", r.Header.Get("Referer"),
			"host", r.Host, "self_hosts", seen)
		// 报错必须能自查：裸一句 "origin not allowed" 在界面上就是"修改失败"，
		// 而真正的原因在 nginx 配置里 —— 把两边各自看到的地址都摆出来。
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": Tr(r, "auth.origin_rejected", browserOrigin(r), seen),
			"code":  "origin_not_allowed",
		})
	})
}

// browserOrigin 取浏览器自报的来源（Origin 优先，其次 Referer），仅用于日志与报错文案。
func browserOrigin(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("Origin")); v != "" {
		return v
	}
	return strings.TrimSpace(r.Header.Get("Referer"))
}

func (s *Server) originAllowed(r *http.Request) bool {
	origin := browserOrigin(r)
	if origin == "" {
		// Non-browser clients (curl/scripts) with session cookie: allow when
		// no Origin/Referer — CSRF requires a browser to inject Origin.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if s.originIsSelf(u.Host, r) {
		return true
	}
	return s.corsOriginListed(u.Scheme + "://" + u.Host)
}

// originIsSelf 判断这个 Origin 是不是"面板自己"。
//
// 主机名必须严格相同，端口则**只在服务端确实知道对外端口时**才比。理由是现场最常见的一种
// 部署：面板开在 https://a.bc.com:8443，nginx 按最普遍的写法 `proxy_set_header Host $host`
// 转发（$host 不含端口），服务端看到的 r.Host 只剩 a.bc.com。此时浏览器发的
// Origin: https://a.bc.com:8443 与之逐字节比较必然不等 —— 读接口全是 GET 不过这一关，
// 界面看着一切正常，一按保存（改分组/改配置/新建分组…）就是一句"origin not allowed"。
//
// 端口被抹掉时放宽到"同主机名即可"是有代价的：同主机名的另一个端口上如果跑着攻击者能控制
// 的页面，它就能带着会话 Cookie 发跨端口写请求。这个代价可以接受，因为 Cookie 本身**不按
// 端口隔离**（RFC 6265 §8.5）：那个页面既然同主机名，它已经能读写同名 Cookie，CSRF 只是它
// 能做的事里最轻的一件。而反过来，为了这点防护把所有非标准端口的反代部署全部锁死，代价大
// 得多。**只在端口未知时放宽**——服务端如果确实看到了端口（直连，或代理转了
// X-Forwarded-Port / Host 带端口），仍然严格比对。
func (s *Server) originIsSelf(originHost string, r *http.Request) bool {
	name := hostnameOnly(originHost)
	if name == "" {
		return false
	}
	port := portOnly(originHost)
	// 代理明说的对外端口：Host 里没有端口时，用它把"未知"补成"已知"。
	hint := strings.TrimSpace(firstForwardedValue(r.Header.Get("X-Forwarded-Port")))
	if hint == "" {
		hint = forwardedHeaderPort(r.Header.Get("Forwarded"))
	}
	for _, cand := range s.selfHostCandidates(r) {
		if !strings.EqualFold(hostnameOnly(cand), name) {
			continue
		}
		cp := portOnly(cand)
		if cp == "" {
			// 端口被反代抹掉：本机根本不知道对外端口，同主机名即认自己；
			// 若代理转了 X-Forwarded-Port，就按它比对（更严）。
			if hint == "" || port == "" || port == hint {
				return true
			}
			continue
		}
		if cp == port {
			return true
		}
	}
	return false
}

// selfHostCandidates 列出"浏览器可能用来访问面板的地址"，供 Origin 比对与报错文案使用。
//
// X-Forwarded-Host / Forwarded **不挂 trust_proxy 门禁**，与 forwardedHTTPS 的理由同源：
// CSRF 的威胁模型是浏览器，而浏览器发不出自定义请求头（会触发预检，预检又要先过 CORS 白
// 名单）。能伪造这两个头的攻击者是直连服务端的脚本 —— 他手里必须已经有会话 Cookie，
// CSRF 对他毫无意义。挂上门禁反而会把"nginx 没配 trust_proxy"的部署整体锁死。
func (s *Server) selfHostCandidates(r *http.Request) []string {
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" {
			return
		}
		if _, dup := seen[strings.ToLower(h)]; dup {
			return
		}
		seen[strings.ToLower(h)] = struct{}{}
		out = append(out, h)
	}
	if r != nil {
		add(r.Host)
		add(firstForwardedValue(r.Header.Get("X-Forwarded-Host")))
		add(forwardedHeaderHost(r.Header.Get("Forwarded")))
		// 网关中继（cmd/agent/relay.go）改写 Host 前留下的原始地址。上游若还有一层
		// nginx，X-Forwarded-Host 会被它覆盖，这条自定义头是那种两层代理下唯一活下来的线索。
		add(firstForwardedValue(r.Header.Get("X-AIOps-Client-Host")))
	}
	if s != nil && s.cfg != nil {
		if pu := strings.TrimSpace(s.cfg.PublicURL()); pu != "" {
			if u, err := url.Parse(pu); err == nil && u.Host != "" {
				add(u.Host)
			}
		}
	}
	return out
}

// hostnameOnly strips the port (and IPv6 brackets) from a host[:port] value.
func hostnameOnly(hostport string) string {
	h := strings.TrimSpace(hostport)
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "[") { // [::1] / [::1]:8529
		if i := strings.Index(h, "]"); i > 0 {
			h = h[1:i]
		} else {
			return ""
		}
	} else if strings.Count(h, ":") == 1 { // host:port（多于一个冒号 = 裸 IPv6，整段都是主机）
		h = h[:strings.IndexByte(h, ':')]
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// portOnly returns the explicit port of a host[:port] value, or "" when absent.
func portOnly(hostport string) string {
	h := strings.TrimSpace(hostport)
	if h == "" {
		return ""
	}
	if i := strings.LastIndex(h, "]"); i >= 0 { // [::1]:8529
		if rest := h[i+1:]; strings.HasPrefix(rest, ":") {
			return rest[1:]
		}
		return ""
	}
	if strings.Count(h, ":") != 1 {
		return ""
	}
	return h[strings.IndexByte(h, ':')+1:]
}

func (s *Server) corsOriginListed(origin string) bool {
	for _, o := range s.cfg.CORSOrigins() {
		if strings.TrimSpace(o) == origin {
			return true
		}
	}
	return false
}

// logProductionSecurityBaseline emits warnings (or fatals under AIOPS_STRICT_SECURITY)
// for insecure production defaults that block enterprise procurement.
func logProductionSecurityBaseline(cfg *ConfigStore) {
	strict := strings.EqualFold(os.Getenv("AIOPS_STRICT_SECURITY"), "1") ||
		strings.EqualFold(os.Getenv("AIOPS_STRICT_SECURITY"), "true")
	warnOrFatal := func(msg string) {
		if strict {
			slog.Error("安全基线未通过（AIOPS_STRICT_SECURITY）", "msg", msg)
			os.Exit(1)
		}
		slog.Warn("安全基线建议", "msg", msg)
	}
	if !secretEncryptionEnabled() {
		warnOrFatal("未设置 AIOPS_SECRET_KEY：配置密钥以明文存库，生产环境必须设置")
	}
	if dsn := os.Getenv("AIOPS_POSTGRES_DSN"); strings.Contains(strings.ToLower(dsn), "password=postgres") ||
		strings.Contains(dsn, "password=admin") || strings.Contains(dsn, ":postgres@") {
		warnOrFatal("PostgreSQL DSN 疑似使用默认口令，生产环境请更换强密码")
	}
	// Default admin/admin after first boot is a common POC leftover.
	if u, ok := cfg.UserByName("admin"); ok {
		if verifyPassword("admin", u.Salt, u.Hash) {
			warnOrFatal("默认管理员账号仍使用弱口令 admin，请立即修改")
		}
	}
	if len(cfg.CORSOrigins()) == 0 {
		slog.Info("CORS 白名单为空：跨域 API 不再回显 *，仅同源仪表盘可调用变更类接口")
	}
}
