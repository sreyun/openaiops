package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestRelayCache points a cache at an isolated temp dir so tests never share
// /tmp/aiops-relay-dl-cache with a real relay on the same box.
func newTestRelayCache(t *testing.T, upstream string) *relayDLCache {
	t.Helper()
	c := newRelayDLCache(upstream, "")
	c.dir = t.TempDir()
	return c
}

// The relay caches /dl. During a fleet upgrade the server replaces the binary
// AND its .sha256 together, so a stale cache hit hands the agent an old binary
// whose old checksum still matches: the SHA-256 gate passes, the agent
// "upgrades" to the version it already had, and the server retries it until the
// cache expires. The cache must revalidate against the upstream ETag.
func TestRelayDLCacheRefetchesWhenUpstreamChanges(t *testing.T) {
	var body atomic.Value // string
	body.Store("v1-binary")
	var hits atomic.Int64

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := body.Load().(string)
		w.Header().Set("ETag", `"`+fmt.Sprintf("%x", len(cur))+"-"+cur[:2]+`"`)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		hits.Add(1)
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte("deadbeef  x\n"))
			return
		}
		_, _ = w.Write([]byte(cur))
	}))
	defer up.Close()

	c := newTestRelayCache(t, up.URL)
	get := func() string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/dl/aiops-agent-linux-amd64", nil)
		if !c.serve(rec, req) {
			t.Fatal("cache declined to serve")
		}
		return rec.Body.String()
	}

	if got := get(); got != "v1-binary" {
		t.Fatalf("first fetch = %q", got)
	}
	firstHits := hits.Load()

	// Immediate re-request: served from cache, no refetch.
	if got := get(); got != "v1-binary" {
		t.Fatalf("cached fetch = %q", got)
	}
	if hits.Load() != firstHits {
		t.Fatal("cache should not refetch within the revalidate window")
	}

	// Server upgraded. Age the cache past the revalidate window, then the very
	// next request must observe the change instead of waiting out a 10min TTL.
	body.Store("v2-binary")
	old := time.Now().Add(-2 * relayDLRevalidateAfter)
	for _, n := range []string{"aiops-agent-linux-amd64", "aiops-agent-linux-amd64.sha256"} {
		_ = os.Chtimes(c.dir+"/"+n, old, old)
	}
	if got := get(); got != "v2-binary" {
		t.Fatalf("after upstream change = %q, want v2-binary (stale cache breaks self-update)", got)
	}
}

// Serving a cached copy when the cloud is unreachable is the whole point of
// relay mode on an isolated network — it must not fall through to a proxy that
// cannot connect either.
func TestRelayDLCacheServesStaleWhenUpstreamDown(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte("cached-binary"))
	}))
	c := newTestRelayCache(t, up.URL)

	rec := httptest.NewRecorder()
	if !c.serve(rec, httptest.NewRequest(http.MethodGet, "/dl/aiops-agent-linux-amd64", nil)) {
		t.Fatal("initial fetch failed")
	}
	up.Close() // cloud goes away

	old := time.Now().Add(-2 * relayDLRevalidateAfter)
	for _, n := range []string{"aiops-agent-linux-amd64", "aiops-agent-linux-amd64.sha256"} {
		_ = os.Chtimes(c.dir+"/"+n, old, old)
	}
	rec2 := httptest.NewRecorder()
	if !c.serve(rec2, httptest.NewRequest(http.MethodGet, "/dl/aiops-agent-linux-amd64", nil)) {
		t.Fatal("must serve the cached copy while upstream is unreachable")
	}
	if rec2.Body.String() != "cached-binary" {
		t.Fatalf("stale serve = %q", rec2.Body.String())
	}
}

// A client must not be able to assert "I came through the relay" by supplying
// the header itself, and the relay must stamp its own when configured.
func TestRelayStripsClientSuppliedSecret(t *testing.T) {
	var got atomic.Value
	got.Store("")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("X-Relay-Secret"))
	}))
	defer up.Close()
	target, _ := url.Parse(up.URL)

	for _, tc := range []struct{ configured, client, want string }{
		{"", "forged", ""},
		{"real", "forged", "real"},
		{"real", "", "real"},
	} {
		proxy := httputil.NewSingleHostReverseProxy(target)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Del("X-Relay-Secret")
			if tc.configured != "" {
				r.Header.Set("X-Relay-Secret", tc.configured)
			}
			proxy.ServeHTTP(w, r)
		})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/report", nil)
		if tc.client != "" {
			req.Header.Set("X-Relay-Secret", tc.client)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got.Load().(string) != tc.want {
			t.Fatalf("configured=%q client=%q → upstream saw %q, want %q",
				tc.configured, tc.client, got.Load().(string), tc.want)
		}
	}
}

func TestIsRelayStreamingPath(t *testing.T) {
	for _, p := range []string{
		"/api/v1/agent/terminal/rx", "/api/v1/agent/terminal/tx",
		"/api/v1/agent/desktop/rx", "/api/v1/agent/forward/tx",
		"/proxy/abc", "/ws",
	} {
		if !isRelayStreamingPath(p) {
			t.Errorf("%s must stream (buffered relay = laggy terminal/desktop)", p)
		}
	}
	for _, p := range []string{"/api/v1/agent/report", "/dl/aiops-agent-linux-amd64", "/install.sh"} {
		if isRelayStreamingPath(p) {
			t.Errorf("%s should not be treated as a stream", p)
		}
	}
}

func TestRelayPublicScheme(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	if relayPublicScheme(plain) != "http" {
		t.Fatal("plaintext relay must advertise http")
	}
	fwd := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	fwd.Header.Set("X-Forwarded-Proto", "https, http")
	if relayPublicScheme(fwd) != "https" {
		t.Fatal("TLS-terminated relay must advertise https in the rewritten install script")
	}
}

// 中继回源的连接池必须认环境代理。
//
// 这一条来自现网：install.sh 拿得到、/dl 下的二进制拿不到（502），同一个上游、同一台
// 中继机。原因是两条回源路径对代理的处理不一致——serveRelayInstallScript 用的
// relayClient 走 http.DefaultTransport（天然认 HTTP_PROXY），而 /dl 的缓存回源与直连
// 代理都走 relayTransport，Proxy 字段为 nil 即"永远直连"。
//
// 中继机恰恰是最可能挂代理的那台：它被选作网关就是因为只有它能出网，而企业里"能出网"
// 常常等于"经由 HTTP 代理出网"。
func TestRelayTransportHonorsProxyEnv(t *testing.T) {
	if relayTransport.Proxy == nil {
		t.Fatal("relayTransport.Proxy 为 nil：中继在需要走 HTTP 代理出网的机器上永远回源失败")
	}
	// 断言的是**函数身份**而不是"设个 HTTP_PROXY 看它选中没有"：
	// http.ProxyFromEnvironment 只在首次调用时读一次环境变量并缓存，同进程内后续改 env
	// 不生效（对长驻的中继进程无影响，但会让那种写法的测试变成看运行顺序的抛硬币）。
	want := reflect.ValueOf(http.ProxyFromEnvironment).Pointer()
	got := reflect.ValueOf(relayTransport.Proxy).Pointer()
	if got != want {
		t.Fatal("relayTransport.Proxy 不是 http.ProxyFromEnvironment：回源不会遵循 HTTP_PROXY/HTTPS_PROXY/NO_PROXY")
	}
}

// 回源下载是**持着该产物的锁**跑的：没有超时的话，一个卡死的上游连接会把这个产物的
// 所有 /dl 请求永久挡住——内网所有机器同时装不上 Agent，且没有任何机制能把它解开。
func TestRelayDLClientHasBoundedTimeout(t *testing.T) {
	if relayDLClient.Timeout <= 0 {
		t.Fatal("relayDLClient 没有超时：卡死的回源会永久占住产物锁")
	}
	if relayDLClient.Transport != relayTransport {
		t.Fatal("relayDLClient 必须复用 relayTransport，否则代理与连接池设置又会分叉")
	}
	// 跨境慢链路上的多 MB 二进制需要宽裕的窗口，太短会把正常下载判成失败。
	if relayDLClient.Timeout < 2*time.Minute {
		t.Fatalf("回源超时 %v 过短，慢链路上的正常下载会被误杀", relayDLClient.Timeout)
	}
}
