package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 自检必须走 /healthz、带上 relay secret，并如实回报状态码——它是内网机器全体 502 时
// 网关机上唯一的第一手结论。
func TestRelayUpstreamHealthProbesHealthzWithSecret(t *testing.T) {
	var gotPath, gotSecret string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotSecret = r.URL.Path, r.Header.Get("X-Relay-Secret")
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	status, err := relayUpstreamHealth(up.URL+"/", "s3cr3t")
	if err != nil || status != http.StatusOK {
		t.Fatalf("探测应成功: status=%d err=%v", status, err)
	}
	if gotPath != "/healthz" {
		t.Fatalf("探测路径应为 /healthz，实际 %q", gotPath)
	}
	if gotSecret != "s3cr3t" {
		t.Fatalf("探测应带上 relay secret，实际 %q", gotSecret)
	}
}

func TestRelayUpstreamHealthReportsUnreachable(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := up.URL
	up.Close() // 端口已关：等价于"网关机连不上上游"

	if _, err := relayUpstreamHealth(addr, ""); err == nil {
		t.Fatal("上游不可达时必须返回错误，否则自检会谎报链路正常")
	}
}

// 回源必须走 HTTP/1.1：h2 单连接一断，中继上整个内网同时失联；且反代 ALPN 谈成 h2
// 却不能真正承载时，表现正是"中继全 502、网关机自己上报却正常"。
func TestRelayTransportStaysHTTP11(t *testing.T) {
	if relayTransport.ForceAttemptHTTP2 {
		t.Fatal("relayTransport 不应启用 HTTP/2")
	}
}

// 中继回源必须继承上报那条路径的 http→https 升级。两者分裂时的现场是"网关机自己
// 上报正常、内网全体 502(EOF)"，同一份配置、同一个域名，几乎无法自查。
func TestResolveRelayUpstreamFollowsHTTPSUpgrade(t *testing.T) {
	orig := relayUpstreamProbe
	defer func() { relayUpstreamProbe = orig }()
	relayUpstreamProbe = func(raw string) string {
		if raw == "http://aiops.example.com" {
			return "https://aiops.example.com"
		}
		return raw
	}
	if got := resolveRelayUpstream("http://aiops.example.com/"); got != "https://aiops.example.com" {
		t.Fatalf("上游强制 HTTPS 时回源地址必须升级，实际 %q", got)
	}
}

// 探测不升级时不能擅自改写地址（自建 http 上游、内网 :8529 都合法）。
func TestResolveRelayUpstreamKeepsPlainHTTPWhenNoUpgrade(t *testing.T) {
	orig := relayUpstreamProbe
	defer func() { relayUpstreamProbe = orig }()
	relayUpstreamProbe = func(raw string) string { return raw }
	if got := resolveRelayUpstream("http://10.0.0.9:8529/"); got != "http://10.0.0.9:8529" {
		t.Fatalf("不应改写普通 http 上游，实际 %q", got)
	}
}
