// hostproxy is a tiny host-side reverse proxy for Docker Desktop / published-port
// setups where the container only sees the bridge gateway (e.g. 192.168.97.1)
// as RemoteAddr. It listens on the public port, forwards to the container's
// localhost-mapped port, and injects X-Real-IP / X-Forwarded-* from the real
// TCP peer so aiops-server (with trust_proxy) can record the visitor IP.
//
// Critical: preserve the browser Host (e.g. 127.0.0.1:8529). Rewriting Host to
// the upstream port (18529) breaks CSRF Origin checks (Origin :8529 ≠ Host :18529)
// and breaks logout / forecast / any cookie-authenticated POST.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", envOr("AIOPS_HTTP_LISTEN", ":8529"), "public listen address")
	target := flag.String("target", envOr("AIOPS_PROXY_TARGET", "http://127.0.0.1:18529"), "upstream container URL")
	flag.Parse()

	u, err := url.Parse(*target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		log.Fatalf("invalid -target %q: %v", *target, err)
	}

	proxy := &httputil.ReverseProxy{}
	proxy.FlushInterval = 50 * time.Millisecond
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		log.Printf("upstream error %s %s: %v", r.Method, r.URL.Path, e)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
	// Rewrite（Go 1.20+）取代已弃用的 Director。差别不只是名字：Rewrite 拿得到
	// **未被改写过的** ProxyRequest.In，因此浏览器原始 Host、TLS 状态与对端地址都能
	// 从 In 上直接读，不必像 Director 那样先把值抄出来再调用原 Director 补上——那个
	// 抄来抄去的顺序正是这类反代最容易出错的地方。
	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(u)
		// 上游按 pr.Out.URL 拨号；Host 保留浏览器原值，供 CSRF 校验 / Cookie 作用域 /
		// 绝对 URL 生成使用。
		if h := pr.In.Host; h != "" {
			pr.Out.Host = h
			pr.Out.Header.Set("X-Forwarded-Host", h)
		}
		client := peerIP(pr.In)
		if client == "" {
			return
		}
		pr.Out.Header.Set("X-Real-IP", client)
		pr.Out.Header.Set("X-Forwarded-Proto", forwardedProto(pr.In))
		prior := strings.TrimSpace(pr.In.Header.Get("X-Forwarded-For"))
		if prior == "" {
			pr.Out.Header.Set("X-Forwarded-For", client)
		} else if !hasIPToken(prior, client) {
			pr.Out.Header.Set("X-Forwarded-For", client+", "+prior)
		}
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           proxy,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("hostproxy listening on %s → %s (preserves Host, injects visitor X-Real-IP)", *listen, u.String())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if host == "::1" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}

func forwardedProto(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if p := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); p != "" {
		return p
	}
	return "http"
}

func hasIPToken(list, ip string) bool {
	for _, p := range strings.Split(list, ",") {
		if strings.TrimSpace(p) == ip {
			return true
		}
	}
	return false
}
