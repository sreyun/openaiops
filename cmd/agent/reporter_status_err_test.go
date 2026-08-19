package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aiops-monitor/shared"
)

// 中继回源失败时，真实原因只写在 502 的响应体里（cmd/agent/relay.go 的 ErrorHandler），
// 而中继机的日志内网机器上的人看不到。上报失败的错误里必须带上这段正文——否则用户
// 手上只剩一个不含任何线索的"服务端返回状态码 502"。
func TestSendSurfacesRelayBodyOn502(t *testing.T) {
	const reason = "Relay: 回源失败\n路径: /api/v1/agent/report\n上游: https://panel.example.com\n原因: dial tcp 1.2.3.4:443: i/o timeout\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(reason))
	}))
	defer srv.Close()

	tg := &serverTarget{server: srv.URL, httpc: &http.Client{Timeout: 5 * time.Second}}
	err := tg.send(shared.Report{HostID: "h1", Hostname: "n1"})
	if err == nil {
		t.Fatal("502 must surface as an error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("状态码丢了: %v", err)
	}
	for _, want := range []string{"回源失败", "i/o timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误里缺少中继给出的原因 %q: %v", want, err)
		}
	}
	// 单行化，否则一条日志被拆成多行，journalctl 里更难读。
	if strings.ContainsAny(err.Error(), "\n\r") {
		t.Fatalf("错误信息必须是单行: %q", err.Error())
	}
}

// 空正文（例如反代吐的裸 502）不能退化成 "状态码 502: " 这种带悬空冒号的信息。
func TestSendKeepsPlainMessageWhenBodyEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	tg := &serverTarget{server: srv.URL, httpc: &http.Client{Timeout: 5 * time.Second}}
	err := tg.send(shared.Report{HostID: "h1"})
	if err == nil || err.Error() != "服务端返回状态码 502" {
		t.Fatalf("空正文时应保持原措辞，实际: %v", err)
	}
}
