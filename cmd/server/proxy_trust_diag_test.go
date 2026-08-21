package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// trust_proxy 关着、前面又确实有一层反代时，要认得出来——这是"一个人输错密码把全公司
// 挡在门外"的根因（clientIP 全是 127.0.0.1，登录限流按 IP 计）。
func TestUntrustedProxyDetection(t *testing.T) {
	cases := []struct {
		name       string
		trust      bool
		remoteAddr string
		headers    map[string]string
		want       bool
	}{
		{"本机 nginx 转发 + trust_proxy 关着", false, "127.0.0.1:52344",
			map[string]string{"X-Forwarded-For": "203.0.113.9"}, true},
		{"内网网关转发", false, "10.0.0.7:4001",
			map[string]string{"X-Real-IP": "203.0.113.9"}, true},
		{"已开 trust_proxy 就不用再提醒", true, "127.0.0.1:52344",
			map[string]string{"X-Forwarded-For": "203.0.113.9"}, false},
		{"没有代理头 = 直连", false, "127.0.0.1:52344", nil, false},
		{"公网直连伪造代理头：不认（否则日志天天被刷）", false, "203.0.113.9:41000",
			map[string]string{"X-Forwarded-For": "10.0.0.1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{cfg: &ConfigStore{cfg: ServerConfig{TrustProxy: tc.trust}}}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/login", nil)
			req.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if got := srv.untrustedProxyDetected(req); got != tc.want {
				t.Fatalf("untrustedProxyDetected=%v，期望 %v", got, tc.want)
			}
		})
	}
}

// 被登录限流挡住时，如果根因是"反代没被信任"，报错里必须说出来：
// 挡住的那个人多半不是输错密码的那个，光说"尝试过于频繁"只会让他一直等下去。
func TestLoginRateLimitMentionsUntrustedProxy(t *testing.T) {
	srv, _ := newTestServer(t)
	post := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": "definitely-wrong"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/login", bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:52344"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		rr := httptest.NewRecorder()
		srv.handleLogin(rr, req)
		return rr
	}
	var last *httptest.ResponseRecorder
	for i := 0; i < loginMaxFailures+2; i++ {
		last = post()
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("连续失败应触发限流，实际 %d %s", last.Code, last.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(last.Body.Bytes(), &resp)
	if !strings.Contains(resp.Error, "trust_proxy") {
		t.Fatalf("限流报错应点出 trust_proxy 这条线索，实际：%s", resp.Error)
	}
}
