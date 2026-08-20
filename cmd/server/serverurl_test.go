package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServerURLFollowsBrowser locks the "follow the browser" behavior: the
// generated install / uninstall command must carry the exact address the admin
// used to reach the panel, never an auto-detected LAN/container IP.
//
// Regression guard for the docker case: the server used to scan network
// interfaces and substitute the first non-loopback IP when the admin browsed
// from localhost — inside a container that is the container's own docker-network
// address (e.g. 172.18.0.4), reachable by nobody. The command must instead
// reflect the request host (or X-Forwarded-Host behind a trusted proxy).
func TestServerURLFollowsBrowser(t *testing.T) {
	srv, _ := newTestServer(t)

	check := func(name, host, xfHost, xfProto, want string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/install/info", nil)
		req.Host = host
		if xfHost != "" {
			req.Header.Set("X-Forwarded-Host", xfHost)
		}
		if xfProto != "" {
			req.Header.Set("X-Forwarded-Proto", xfProto)
		}
		if got := srv.serverURL(req); got != want {
			t.Errorf("%s: serverURL(host=%q, xfHost=%q, xfProto=%q) = %q, want %q",
				name, host, xfHost, xfProto, got, want)
		}
	}

	// A real address the admin browsed → used verbatim.
	check("lan ip host", "192.168.1.50:8529", "", "", "http://192.168.1.50:8529")
	// The bug: browsing localhost must NOT be rewritten to a guessed IP. It stays
	// localhost (predictable) — the admin uses a real address or sets public_url.
	check("localhost stays localhost", "localhost:8529", "", "", "http://localhost:8529")
	check("loopback stays loopback (no container IP)", "127.0.0.1:8529", "", "", "http://127.0.0.1:8529")
	// Without trust_proxy, forged X-Forwarded-* must be ignored.
	check("xf ignored without trust_proxy", "172.18.0.4:8529", "aiops.example.com", "https", "http://172.18.0.4:8529")

	srv.cfg.mu.Lock()
	srv.cfg.cfg.TrustProxy = true
	srv.cfg.mu.Unlock()
	// Behind a trusted reverse proxy the forwarded headers describe the client-facing host.
	check("x-forwarded-host wins with trust_proxy", "172.18.0.4:8529", "aiops.example.com", "https", "https://aiops.example.com")
	// Proxies may append a list; the first token is the client-facing hop.
	check("x-forwarded list takes first", "172.18.0.4:8529", "aiops.example.com, internal:8529", "https, http", "https://aiops.example.com")
}

// TestServerURLPublicURLOverride verifies an explicit public_url always wins —
// the reliable knob for reverse-proxy / stable-domain deployments.
func TestServerURLPublicURLOverride(t *testing.T) {
	t.Setenv("AIOPS_PUBLIC_URL", "https://mon.corp.local")
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/install/info", nil)
	req.Host = "localhost:8529" // even a loopback browse must yield the override
	if got := srv.serverURL(req); got != "https://mon.corp.local" {
		t.Errorf("public_url override: serverURL = %q, want %q", got, "https://mon.corp.local")
	}
}

func TestServerURLUpgradesHTTPPublicURLWhenPanelIsHTTPS(t *testing.T) {
	t.Setenv("AIOPS_PUBLIC_URL", "http://aiops.example.com")
	srv, _ := newTestServer(t)
	srv.cfg.mu.Lock()
	srv.cfg.cfg.TrustProxy = true
	srv.cfg.mu.Unlock()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/install/info", nil)
	req.Host = "172.18.0.4:8529"
	req.Header.Set("X-Forwarded-Host", "aiops.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	if got := srv.serverURL(req); got != "https://aiops.example.com" {
		t.Fatalf("expected https install URL, got %q", got)
	}
}

func TestPreferHTTPSPublicBase(t *testing.T) {
	if got := preferHTTPSPublicBase("http://aiops.example.com"); got != "https://aiops.example.com" {
		t.Fatalf("got %q", got)
	}
	if got := preferHTTPSPublicBase("http://192.168.1.10:8529"); got != "http://192.168.1.10:8529" {
		t.Fatalf("lab port must stay http: %q", got)
	}
	if got := preferHTTPSPublicBase("https://aiops.example.com"); got != "https://aiops.example.com" {
		t.Fatalf("already https: %q", got)
	}
	// IPv6 字面量必须留着方括号，否则升级完的地址根本解析不了。
	if got := preferHTTPSPublicBase("http://[2001:db8::1]"); got != "https://[2001:db8::1]" {
		t.Fatalf("ipv6: %q", got)
	}
}

func TestFirstForwardedValue(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"aiops.example.com":      "aiops.example.com",
		"a.example.com, b.local": "a.example.com",
		"  https , http ":        "https",
	}
	for in, want := range cases {
		if got := firstForwardedValue(in); got != want {
			t.Errorf("firstForwardedValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// 面板开在 https://a.bc.com:8443、nginx 用 `proxy_set_header Host $host` 转发时，
// 服务端看到的 Host 只剩 a.bc.com——安装命令与脚本里的 SERVER= 会指向默认 443，
// Agent 注册连不上，而面板本身一切正常。端口必须从代理/浏览器留下的线索里补回来。
func TestServerURLRecoversPortStrippedByProxy(t *testing.T) {
	srv, _ := newTestServer(t)

	newReq := func(path string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "a.bc.com" // 代理抹掉了 :8443
		return req
	}

	t.Run("x-forwarded-port", func(t *testing.T) {
		req := newReq("/install.sh")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Port", "8443")
		if got := srv.serverURL(req); got != "https://a.bc.com:8443" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("x-forwarded-port list takes first", func(t *testing.T) {
		req := newReq("/install.sh")
		req.Header.Set("X-Forwarded-Proto", "https, http")
		req.Header.Set("X-Forwarded-Port", "8443, 80")
		if got := srv.serverURL(req); got != "https://a.bc.com:8443" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("rfc7239 forwarded", func(t *testing.T) {
		req := newReq("/install.sh")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Forwarded", `for=1.2.3.4;host="a.bc.com:8443";proto=https`)
		if got := srv.serverURL(req); got != "https://a.bc.com:8443" {
			t.Fatalf("got %q", got)
		}
	})

	// 同源 GET 浏览器不一定发 Origin，但 Referer 一定在——面板页拉 /install/info 时
	// 靠它把地址栏里的端口带过来。
	t.Run("referer of the panel page", func(t *testing.T) {
		req := newReq("/api/v1/install/info")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Referer", "https://a.bc.com:8443/v2/")
		if got := srv.serverURL(req); got != "https://a.bc.com:8443" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("origin header", func(t *testing.T) {
		req := newReq("/api/v1/install/info")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Origin", "https://a.bc.com:8443")
		if got := srv.serverURL(req); got != "https://a.bc.com:8443" {
			t.Fatalf("got %q", got)
		}
	})

	// curl 那一跳没有 Referer，代理也未必配 X-Forwarded-Port：面板把端口拼进
	// 安装命令的 ?port=，脚本里的 SERVER= 才不会掉端口。
	t.Run("panel injected ?port=", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/install.sh?token=abc&port=8443", nil)
		req.Host = "a.bc.com"
		req.Header.Set("X-Forwarded-Proto", "https")
		if got := srv.serverURL(req); got != "https://a.bc.com:8443" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("ipv6 host", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/install.sh?port=8443", nil)
		req.Host = "[2001:db8::1]"
		req.Header.Set("X-Forwarded-Proto", "https")
		if got := srv.serverURL(req); got != "https://[2001:db8::1]:8443" {
			t.Fatalf("got %q", got)
		}
	})
}

// 端口补回只能补"缺的那一个"，不能改写已有端口，也不能被跨站页面 / 无关接口带偏。
func TestServerURLPortRecoveryStaysNarrow(t *testing.T) {
	srv, _ := newTestServer(t)

	t.Run("explicit port wins", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/install.sh?port=9999", nil)
		req.Host = "a.bc.com:8443"
		req.Header.Set("X-Forwarded-Port", "9999")
		req.Header.Set("Referer", "https://a.bc.com:1234/v2/")
		if got := srv.serverURL(req); got != "http://a.bc.com:8443" {
			t.Fatalf("已有端口不得被改写，实际 %q", got)
		}
	})

	t.Run("default port not appended", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/install.sh?port=443", nil)
		req.Host = "a.bc.com"
		req.Header.Set("X-Forwarded-Proto", "https")
		if got := srv.serverURL(req); got != "https://a.bc.com" {
			t.Fatalf("got %q", got)
		}
		req2 := httptest.NewRequest(http.MethodGet, "/install.sh?port=80", nil)
		req2.Host = "a.bc.com"
		if got := srv.serverURL(req2); got != "http://a.bc.com" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("cross-origin referer ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/install/info", nil)
		req.Host = "a.bc.com"
		req.Header.Set("Referer", "https://evil.example.com:1234/x")
		req.Header.Set("Origin", "https://evil.example.com:1234")
		if got := srv.serverURL(req); got != "http://a.bc.com" {
			t.Fatalf("跨站 Origin/Referer 不得影响生成地址，实际 %q", got)
		}
	})

	t.Run("port param only on install scripts", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/install/info?port=9999", nil)
		req.Host = "a.bc.com"
		if got := srv.serverURL(req); got != "http://a.bc.com" {
			t.Fatalf("非安装脚本路径不应采信 ?port=，实际 %q", got)
		}
	})

	t.Run("garbage port ignored", func(t *testing.T) {
		for _, bad := range []string{"abc", "0", "70000", "-1", "8443;rm -rf /", "  "} {
			req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
			req.Host = "a.bc.com"
			req.Header.Set("X-Forwarded-Port", bad)
			if got := srv.serverURL(req); got != "http://a.bc.com" {
				t.Fatalf("X-Forwarded-Port=%q 应被忽略，实际 %q", bad, got)
			}
		}
	})

	t.Run("public_url wins verbatim", func(t *testing.T) {
		t.Setenv("AIOPS_PUBLIC_URL", "https://mon.corp.local")
		srv2, _ := newTestServer(t)
		req := httptest.NewRequest(http.MethodGet, "/install.sh?port=8443", nil)
		req.Host = "mon.corp.local"
		req.Header.Set("X-Forwarded-Port", "8443")
		if got := srv2.serverURL(req); got != "https://mon.corp.local" {
			t.Fatalf("显式 public_url 不得被请求头改写，实际 %q", got)
		}
	})
}

// 生成的安装脚本里 SERVER= 必须带上端口——这才是 Agent 真正拿去注册的地址。
func TestInstallScriptCarriesRecoveredPort(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{"/install.sh?token=abc&port=8443", "/install.ps1?token=abc&port=8443"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "a.bc.com"
		req.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		srv.handleInstallScript(w, req)
		body := w.Body.String()
		if !strings.Contains(body, "https://a.bc.com:8443") {
			t.Fatalf("%s: 脚本里的 SERVER= 丢了端口", path)
		}
		if strings.Contains(body, `"https://a.bc.com"`) {
			t.Fatalf("%s: 脚本里仍有不带端口的地址", path)
		}
	}
}

// 面板要判断"这个地址能不能用地址栏的端口补"：public_url 写死的一律照抄，
// 请求推导出来的才允许前端补端口（服务端拿不到 Referer/Origin，见 recoverEdgePort）。
func TestInstallInfoReportsPublicURLFixed(t *testing.T) {
	check := func(t *testing.T, srv *Server, want bool) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/install/info", nil)
		req.Host = "a.bc.com"
		w := httptest.NewRecorder()
		srv.handleInstallInfo(w, req)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
		if got, _ := body["server_url_fixed"].(bool); got != want {
			t.Fatalf("server_url_fixed = %v, want %v", body["server_url_fixed"], want)
		}
	}
	t.Run("derived from request", func(t *testing.T) {
		srv, _ := newTestServer(t)
		check(t, srv, false)
	})
	t.Run("public_url configured", func(t *testing.T) {
		t.Setenv("AIOPS_PUBLIC_URL", "https://mon.corp.local")
		srv, _ := newTestServer(t)
		check(t, srv, true)
	})
}
