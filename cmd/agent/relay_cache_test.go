package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
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
