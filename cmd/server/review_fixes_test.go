package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 只读诊断通道：白名单命令的写入/破坏性参数必须被拦截，常见只读用法不能误伤。
func TestDiagCommandRejectsWriteOptions(t *testing.T) {
	blocked := []string{
		"cat /dev/null | sort -o /etc/passwd",
		"cat /var/log/syslog | sort --output=/etc/cron.d/x",
		"cat /var/log/syslog | sort --out=/tmp/x",
		"cat /var/log/syslog | sort -nro/tmp/x",
		"cat /var/log/syslog | sort --compress-program=sh",
		"uniq /etc/hosts /etc/passwd",
		"journalctl --vacuum-time=1s",
		"journalctl --rotate",
		"journalctl --flu",
		"dmesg -C",
		"dmesg -c",
		"dmesg -n 1",
		"dmesg --clear",
		"date -s 2020-01-01",
		"date --set=2020-01-01",
		"date 010100002020",
		"hostname evil",
		"hostname -F /tmp/name",
		"ss -K dst 10.0.0.5",
		"ss --kill",
		"sar -o /etc/x 1 1",
		"kubectl get secrets -o yaml",
		"kubectl get secret/db -A",
	}
	for _, c := range blocked {
		if ok, _ := diagCommandAllowed(c); ok {
			t.Errorf("应拦截写入/破坏性诊断命令：%q", c)
		}
	}
	allowed := []string{
		"df -h",
		"top -bn1 | head -20",
		"ps aux | sort -nrk3 | head",
		"cat /var/log/syslog | sort -t: -k3 | uniq -c",
		"journalctl -u nginx --no-pager --since today | tail -50",
		"dmesg -T | tail -50",
		"dmesg -lcrit",
		"date",
		"date -Iseconds",
		"date +%F",
		"hostname -I",
		"ss -tulpn",
		"sar -u 1 3",
		"kubectl get pods -A",
		"uniq -c /var/log/syslog",
	}
	for _, c := range allowed {
		if ok, reason := diagCommandAllowed(c); !ok {
			t.Errorf("只读诊断命令被误拦：%q（%s）", c, reason)
		}
	}
}

// 失败计数表超限时，不能把正在被爆破的账号计数一起清空。
func TestLoginAccountThrottleSurvivesKeySpray(t *testing.T) {
	a := NewAuth(newTestConfigStore(t))
	for i := 0; i < loginAccountMaxFail-1; i++ {
		a.loginAccountFailed("admin")
	}
	for i := 0; i <= maxLoginFailureKeys+10; i++ {
		u := fmt.Sprintf("spray-%d", i)
		a.loginAccountAllowed(u)
		a.loginAccountFailed(u)
	}
	a.loginAccountFailed("admin")
	if a.loginAccountAllowed("admin") {
		t.Fatal("喷洒随机用户名后，admin 的失败计数被清空了——逐账号限流可被绕过")
	}
	a.limMu.Lock()
	n := len(a.loginHitUser)
	a.limMu.Unlock()
	if n > maxLoginFailureKeys+1 {
		t.Fatalf("失败计数表未受上限约束：%d", n)
	}
}

// 用户名登录路径必须在校验密码之前就执行逐账号限流。
func TestUsernameLoginEnforcesAccountThrottle(t *testing.T) {
	srv, _ := newTestServer(t)
	for i := 0; i < loginAccountMaxFail; i++ {
		srv.auth.loginAccountFailed("admin")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login",
		strings.NewReader(`{"username":"admin","password":"whatever"}`))
	req.RemoteAddr = "198.51.100.7:4000"
	rr := httptest.NewRecorder()
	srv.handleLogin(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("账号已达失败上限，用户名登录应 429，得到 %d %s", rr.Code, rr.Body.String())
	}
}

// 24h 滑动空闲超时必须在生产鉴权路径（userForRequest）上生效。
func TestSessionIdleTimeoutEnforced(t *testing.T) {
	a := NewAuth(newTestConfigStore(t))
	tok := a.issueSession("admin")
	req := newRoleRequest(http.MethodGet, "/api/v1/hosts")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	if a.userForRequest(req) != "admin" {
		t.Fatal("新会话应有效")
	}
	a.mu.Lock()
	s := a.sessions[sessionKey(tok)]
	s.lastSeen = time.Now().Add(-sessionIdleTimeout - time.Minute)
	a.sessions[sessionKey(tok)] = s
	a.mu.Unlock()
	if u := a.userForRequest(req); u != "" {
		t.Fatalf("空闲超时的会话仍被认作 %q", u)
	}
	if a.isRestricted(req) || a.isPasswordChangeOnly(req) {
		t.Fatal("已失效会话不应再报告受限状态")
	}
}

// 受限会话（强制改密）不能凭会话拿到 /metrics。
func TestFullSessionUserRejectsRestricted(t *testing.T) {
	a := NewAuth(newTestConfigStore(t))
	tok := a.issuePasswordChangeSession("admin")
	req := newRoleRequest(http.MethodGet, "/metrics")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	if a.fullSessionUser(req) != "" {
		t.Fatal("强制改密会话不应被当作完整会话")
	}
	tok2 := a.issueSession("admin")
	req2 := newRoleRequest(http.MethodGet, "/metrics")
	req2.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok2})
	if a.fullSessionUser(req2) != "admin" {
		t.Fatal("完整会话应被接受")
	}
}

// 找回流程的 TOTP 第二因子：错码要计数，5 次后作废邮件验证码。
func TestRecoverySecondFactorFailuresVoidEmailCode(t *testing.T) {
	em := newEmailManager()
	code, err := em.issueCode("u@example.com", "recover_password")
	if err != nil {
		t.Fatal(err)
	}
	if em.markCodeVerified("u@example.com", "recover_password", code) == "" {
		t.Fatal("邮件验证码应通过")
	}
	for i := 0; i < 5; i++ {
		em.recordSecondFactorFailure("u@example.com", "recover_password")
	}
	if em.consumeVerifiedCode("u@example.com", "recover_password", code) != "" {
		t.Fatal("TOTP 连错 5 次后邮件验证码应作废")
	}
}

// 邮件发送节流：同一地址 60s 内只能占用一次发送名额（含不带验证码的用户名找回邮件）。
func TestEmailReserveSendRateLimited(t *testing.T) {
	em := newEmailManager()
	if !em.reserveSend("u@example.com") {
		t.Fatal("首次发送应放行")
	}
	if em.reserveSend("U@example.com") {
		t.Fatal("60s 内重复发送应被节流（地址大小写不敏感）")
	}
	if _, err := em.issueCode("u@example.com", "reset_password"); err == nil {
		t.Fatal("同一地址的验证码发送与用户名邮件共享节流")
	}
}

// /app.js：缺模块时不得先吐 200 再拼报错；命中 ETag 回 304。
func TestServeClassicAppJSETag(t *testing.T) {
	rr := httptest.NewRecorder()
	serveClassicAppJS(rr, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/app.js 应 200，得到 %d", rr.Code)
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("/app.js 缺少 ETag")
	}
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	rr = httptest.NewRecorder()
	serveClassicAppJS(rr, req)
	if rr.Code != http.StatusNotModified || rr.Body.Len() != 0 {
		t.Fatalf("ETag 命中应 304 且无正文，得到 %d（%d 字节）", rr.Code, rr.Body.Len())
	}
}

// /metrics 必须真正注册在路由上，且对匿名请求 401（而不是落到 SPA 外壳）。
func TestMetricsRouteRegistered(t *testing.T) {
	srv, _ := newTestServer(t)
	rr := httptest.NewRecorder()
	srv.httpHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("匿名 GET /metrics 应 401，得到 %d", rr.Code)
	}
}
