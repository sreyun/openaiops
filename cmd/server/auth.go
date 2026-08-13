package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// isPublicPath reports whether a request may proceed without a session:
// the dashboard shell + static assets, agent register/report, the install /
// uninstall scripts and downloads, and the login / me endpoints.
func isPublicPath(r *http.Request) bool {
	p := r.URL.Path
	switch p {
	case "/", "/healthz", "/style.css", "/app.js", "/theme-init.js", "/i18n-dashboard.js", "/i18n-dashboard.en.js", "/i18n-dashboard.zh-TW.js",
		"/sw.js", "/manifest.json", "/icon.svg", // PWA shell: SW must register on the pre-login page too
		"/install.sh", "/install.ps1", "/uninstall.sh", "/uninstall.ps1",
		"/install-relay.sh", "/install-relay.ps1",
		"/api/v1/login", "/api/v1/login/sms-code", "/api/v1/me",
		"/api/v1/auth/oidc/info", "/api/v1/auth/oidc/login", "/api/v1/auth/oidc/callback",
		"/api/v1/auth/sso/info",
		"/api/v1/auth/feishu/login", "/api/v1/auth/feishu/callback",
		"/api/v1/auth/dingtalk/login", "/api/v1/auth/dingtalk/callback",
		"/api/v1/auth/wechat/login", "/api/v1/auth/wechat/callback",
		"/api/v1/auth/wecom/login", "/api/v1/auth/wecom/callback",
		"/api/v1/forward/health",
		"/api/v1/account/recover-send-code",
		"/api/v1/account/recover-verify",
		"/api/v1/account/recover-verify-mfa",
		"/api/v1/account/recover-username",
		"/api/v1/account/send-reset-code",
		"/api/v1/account/reset-password",
		"/api/v1/agent/register", "/api/v1/agent/report",
		"/api/v1/mcp",                        // MCP server：外部 Agent(如 Hermes Agent) 连接，在 handler 内做 Bearer Token 鉴权
		"/api/v1/prom/write",                 // Prometheus remote_write 接收：外部 exporter/telegraf/OTel 推送，在 handler 内做 Bearer 令牌鉴权
		"/api/v1/integrations/content-audit", // LLM Gateway/SDK structured audit ingest; dedicated Bearer token in handler
		"/api/v1/agent/logs",                 // fingerprint-gated log ingest (checked in the handler)
		"/api/v1/brand",                      // public console branding metadata
		"/status",                            // public Status Page (HTML)
		"/api/v1/public/status":              // public Status Page (JSON; optional token in handler)
		return true
	}
	if strings.HasPrefix(p, "/api/v1/brand/logo/") {
		return true
	}
	// Agent-facing hardware/netflow/hyperv/snmp ingest are fingerprint-gated, not
	// session-gated (the fingerprint is verified inside each handler).
	if p == "/api/v1/agent/hardware" || p == "/api/v1/agent/netflow" || p == "/api/v1/agent/hyperv" ||
		p == "/api/v1/agent/containers" ||
		p == "/api/v1/agent/snmp" || p == "/api/v1/agent/snmp/trap" || p == "/api/v1/agent/dnsmap" ||
		p == "/api/v1/agent/content-audit" || p == "/api/v1/agent/probe-results" {
		return true
	}
	// 拆分后的前端静态模块（/js/*.js、/css/*）与 /app.js、/style.css 同属登录前外壳，需放行。
	if strings.HasPrefix(p, "/js/") || strings.HasPrefix(p, "/css/") {
		return true
	}
	// Agent-facing terminal reverse channels are token-gated, not session-gated.
	if strings.HasPrefix(p, "/api/v1/agent/terminal/") {
		return true
	}
	if strings.HasPrefix(p, "/api/v1/agent/desktop/") {
		return true
	}
	// Agent-facing port forwarding reverse channels are fingerprint-gated.
	if strings.HasPrefix(p, "/api/v1/agent/forward/") {
		return true
	}
	return strings.HasPrefix(p, "/dl/")
}

// ctxKeyProxyUser carries the username authenticated via a one-shot proxy_token
// when the session cookie is absent (window.open /proxy/ flow).
type ctxKeyProxyUser struct{}

// currentUser resolves the logged-in user's account from the session cookie,
// falling back to a proxy-token identity stored on the request context.
func (s *Server) currentUser(r *http.Request) (AccountConfig, bool) {
	name := s.auth.userForRequest(r)
	if name == "" {
		if v, ok := r.Context().Value(ctxKeyProxyUser{}).(string); ok {
			name = v
		}
	}
	if name == "" {
		return AccountConfig{}, false
	}
	return s.cfg.UserByName(name)
}

// validatePasswordStrength enforces the account password policy: at least 8
// characters including an uppercase letter, a lowercase letter, a digit and a
// special (non-alphanumeric) character.
func validatePasswordStrength(pw string) bool {
	if len([]rune(pw)) < 8 {
		return false
	}
	var up, lo, dg, sp bool
	for _, c := range pw {
		switch {
		case c >= 'A' && c <= 'Z':
			up = true
		case c >= 'a' && c <= 'z':
			lo = true
		case c >= '0' && c <= '9':
			dg = true
		default:
			sp = true // any non-alphanumeric rune counts as a special character
		}
	}
	return up && lo && dg && sp
}

// routeAllowed enforces RBAC: any logged-in role may manage its own account;
// viewer is otherwise read-only; the remote terminal needs operator+; user
// management, admin ops, alert/AI settings writes need admin; every other write needs operator+.
func (s *Server) routeAllowed(r *http.Request, role string) bool {
	rank := roleRank(role)
	if rank == 0 {
		return false
	}
	p := r.URL.Path
	switch p { // own-account self-service: any logged-in role
	case "/api/v1/logout", "/api/v1/password", "/api/v1/profile", "/api/v1/account/init",
		"/api/v1/mfa/setup", "/api/v1/mfa/enable", "/api/v1/mfa/disable",
		"/api/v1/mfa/unbind-via-email",
		"/api/v1/auth/sso/identities":
		return true
	}
	if strings.HasPrefix(p, "/api/v1/auth/sso/identities/") {
		return true
	}
	if strings.HasPrefix(p, "/api/v1/users") || p == "/api/v1/mfa/global" || strings.HasPrefix(p, "/api/v1/admin/") ||
		p == "/api/v1/security/secret-rotate" || p == "/api/v1/security/rewrap-secrets" || p == "/api/v1/security/key-status" { // user mgmt + admin ops: admin only
		return rank >= roleRank(RoleAdmin)
	}
	// Content audit / AI tool audit / audit export: high-sensitivity security data.
	if strings.HasPrefix(p, "/api/v1/content-audit") || p == "/api/v1/ai/tool-audit" {
		return rank >= roleRank(RoleOperator)
	}
	// Host / Web security scan: operator+; nuclei path & allow_private config → admin.
	if p == "/api/v1/security/overview" || strings.HasPrefix(p, "/api/v1/security/host") || strings.HasPrefix(p, "/api/v1/security/web") ||
		strings.HasPrefix(p, "/api/v1/security/feeds") || strings.HasPrefix(p, "/api/v1/security/findings/") {
		// Feed settings carry an outbound proxy URL and can start large
		// downloads, so writes there are admin-only like the engine config.
		if p == "/api/v1/security/web/config" || p == "/api/v1/security/host/config" ||
			p == "/api/v1/security/web/engine/refresh" || strings.HasPrefix(p, "/api/v1/security/feeds/") {
			if r.Method != http.MethodGet {
				return rank >= roleRank(RoleAdmin)
			}
		}
		if strings.HasPrefix(p, "/api/v1/security/findings/") && r.Method == http.MethodGet {
			return rank >= roleRank(RoleViewer)
		}
		return rank >= roleRank(RoleOperator)
	}
	if p == "/api/v1/audit-export" || strings.HasPrefix(p, "/api/v1/auth/oidc/config") ||
		strings.HasPrefix(p, "/api/v1/auth/sso/config") ||
		p == "/api/v1/install/revoke-token" || p == "/api/v1/install/token-policy" ||
		p == "/api/v1/install/reset-token" ||
		(p == "/api/v1/agents/auto-update-policy" && r.Method != http.MethodGet) {
		return rank >= roleRank(RoleAdmin)
	}
	// NOTE: POST /api/v1/agents/update (fleet binary replacement) intentionally
	// stays operator+ via the default write rule below — see auth_test.go
	// "operator can start agent update". It is additionally gated per host by
	// remoteGateCheck(highRisk=true), which demands an approved change outside
	// freeze windows. Raise it here only as a deliberate policy change.
	// Install info is readable by viewer+ (server_url / policy), but the handler
	// masks the raw token for non-admins — keep route at viewer so the panel loads.
	// SQL toolkit: offline tools + EXPLAIN → viewer+; connection CRUD/test → admin.
	if strings.HasPrefix(p, "/api/v1/sql/") {
		if strings.HasPrefix(p, "/api/v1/sql/change-requests") {
			if strings.HasSuffix(p, "/approve") || strings.HasSuffix(p, "/reject") {
				return rank >= roleRank(RoleAdmin)
			}
			return rank >= roleRank(RoleOperator)
		}
		// Slow-SQL collect / processlist / locks / schema health: operator+ (heavier / ops).
		if strings.HasSuffix(p, "/slow-sql/run") || strings.HasSuffix(p, "/processlist") ||
			strings.HasSuffix(p, "/locks") || strings.HasSuffix(p, "/schema/health") ||
			strings.HasSuffix(p, "/slow-sql/ps-limits/apply") {
			return rank >= roleRank(RoleOperator)
		}
		if r.Method == http.MethodGet {
			return rank >= roleRank(RoleViewer)
		}
		if p == "/api/v1/sql/beautify" || p == "/api/v1/sql/audit" || p == "/api/v1/sql/optimize" ||
			p == "/api/v1/sql/analyze" || strings.HasSuffix(p, "/explain") || strings.HasSuffix(p, "/query") {
			return rank >= roleRank(RoleViewer)
		}
		if strings.HasSuffix(p, "/exec-ddl") {
			return rank >= roleRank(RoleOperator)
		}
		return rank >= roleRank(RoleAdmin)
	}
	// CI/CD: reads → viewer+; pipeline control (trigger/retry/cancel/diagnose) →
	// operator+; connection CRUD holds access tokens for the SCM, so → admin.
	if strings.HasPrefix(p, "/api/v1/cicd/") {
		if r.Method == http.MethodGet {
			return rank >= roleRank(RoleViewer)
		}
		if strings.HasPrefix(p, "/api/v1/cicd/connections") {
			return rank >= roleRank(RoleAdmin)
		}
		return rank >= roleRank(RoleOperator)
	}
	// K8s: cluster config writes + connectivity test → admin; scale/restart → operator+; GET → viewer+.
	if strings.HasPrefix(p, "/api/v1/k8s/") {
		if r.Method == http.MethodGet {
			return rank >= roleRank(RoleViewer)
		}
		if strings.HasSuffix(p, "/scale") || strings.HasSuffix(p, "/restart") ||
			strings.HasSuffix(p, "/undo") || strings.HasSuffix(p, "/exec") ||
			strings.HasSuffix(p, "/apply") ||
			(r.Method == http.MethodPost && strings.HasSuffix(p, "/namespaces")) ||
			(r.Method == http.MethodDelete && strings.Contains(p, "/pods/")) {
			return rank >= roleRank(RoleOperator)
		}
		return rank >= roleRank(RoleAdmin)
	}
	// Hyper-V / 容器写操作：operator+（路径须限定前缀，避免误匹配 /api/v1/config、/ai/config）。
	if r.Method != http.MethodGet {
		if strings.HasPrefix(p, "/api/v1/hyperv/") && (strings.HasSuffix(p, "/power") || strings.HasSuffix(p, "/config")) {
			return rank >= roleRank(RoleOperator)
		}
		if strings.HasPrefix(p, "/api/v1/containers/") && (strings.HasSuffix(p, "/action") || strings.HasSuffix(p, "/exec") ||
			strings.Contains(p, "/compose/")) {
			return rank >= roleRank(RoleOperator)
		}
	}
	// 敏感系统配置：告警通道/阈值、AI Provider 设置及其连通性测试 —— 仅管理员可写。
	// GET 仍按下方 viewer+ 放行（密钥已脱敏），供界面回填与能力探测。
	switch p {
	case "/api/v1/config":
		if r.Method != http.MethodGet {
			return rank >= roleRank(RoleAdmin)
		}
	case "/api/v1/config/test",
		"/api/v1/ai/config",
		"/api/v1/ai/test",
		"/api/v1/ai/test-embed",
		"/api/v1/ai/test-speech",
		"/api/v1/ai/test-rerank",
		"/api/v1/ai/test-weknora",
		"/api/v1/ai/list-weknora-kbs",
		"/api/v1/ai/models",
		"/api/v1/ai/terminal-access",
		"/api/v1/ai/mcp-clients/test",
		"/api/v1/ai/mcp-clients/sync":
		if r.Method != http.MethodGet {
			return rank >= roleRank(RoleAdmin)
		}
	}
	if strings.Contains(p, "/terminal") || strings.Contains(p, "/desktop") || strings.HasPrefix(p, "/api/v1/forward") || strings.HasPrefix(p, "/proxy/") || p == "/api/v1/proxy-token" { // remote shell + desktop + port forwarding: operator+
		return rank >= roleRank(RoleOperator)
	}
	if r.Method == http.MethodGet { // reads: viewer+
		return rank >= roleRank(RoleViewer)
	}
	return rank >= roleRank(RoleOperator) // other writes/actions: operator+
}

// authMiddleware gates every non-public path on a valid session AND a sufficient
// role for the requested route.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// v5.4.1: verify relay shared secret when configured. Requests that
		// carry X-Relay-Secret must match the configured secret; requests
		// without the header are allowed (direct, not through relay).
		if relaySecret := s.cfg.RelaySecret(); relaySecret != "" {
			if hdr := r.Header.Get("X-Relay-Secret"); hdr != "" {
				if subtle.ConstantTimeCompare([]byte(hdr), []byte(relaySecret)) != 1 {
					s.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: s.clientIP(r), IP: s.clientIP(r), Message: Tz("log.relay_secret_mismatch")})
					writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.relay_unauthorized")})
					return
				}
			}
		}
		if isPublicPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		// HTTP proxy token auth: allows window.open in new tab without relying on
		// the session cookie (which may not be sent cross-context in some browsers).
		// Priority: cookie (set by handleProxyToken) > query param (fallback).
		if strings.HasPrefix(r.URL.Path, "/proxy/") {
			var tok string
			if c, err := r.Cookie("proxy_token"); err == nil && c.Value != "" {
				tok = c.Value
			} else if pt := r.URL.Query().Get("pt"); pt != "" {
				tok = pt
			}
			if tok != "" {
				if user := s.auth.validateProxyToken(tok); user != "" {
					// 纵深防御：代理令牌本就仅 operator+ 可签发，这里仍按令牌所属用户的
					// 当前角色复核 RBAC，防止签发后被降权的用户在令牌有效窗口内经 /proxy/ 越权。
					if s.routeAllowed(r, s.cfg.RoleOf(user)) {
						r = r.WithContext(context.WithValue(r.Context(), ctxKeyProxyUser{}, user))
						next.ServeHTTP(w, r)
						return
					}
					writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.insufficient_permission")})
					return
				}
			}
		}
		name := s.auth.userForRequest(r)
		if name == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
			return
		}
		// Forced password-change sessions (default admin/admin or admin reset)
		// must not unlock the rest of the API — only init/password/me/logout.
		if s.auth.isPasswordChangeOnly(r) {
			switch r.URL.Path {
			case "/api/v1/me", "/api/v1/password", "/api/v1/account/init", "/api/v1/logout":
				// allowed
			default:
				writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.must_change_password")})
				return
			}
		}
		// Restricted sessions (global MFA enforcement) can only touch MFA endpoints.
		if s.auth.isRestricted(r) {
			p := r.URL.Path
			if p != "/api/v1/mfa/setup" && p != "/api/v1/mfa/enable" && p != "/api/v1/logout" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.mfa_required_first")})
				return
			}
		}
		if !s.routeAllowed(r, s.cfg.RoleOf(name)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.insufficient_permission")})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- auth handlers ----

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		LoginType string `json:"login_type"` // "username" (default), "phone", "sms"
		Code      string `json:"code"`       // TOTP second factor (only when MFA is enabled)
		SMSCode   string `json:"sms_code"`   // OTP for login_type=sms
	}
	ip := s.clientIP(r)
	if !s.auth.loginAllowed(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": Tr(r, "auth.too_many_attempts")})
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	// Resolve and authenticate by login type.
	// Default account login accepts username OR bound phone + password.
	var acc AccountConfig
	var authenticated bool
	switch req.LoginType {
	case "sms":
		acc, authenticated = s.authenticateSMSLogin(w, r, req.Username, req.SMSCode, ip)
	case "phone":
		// Legacy client: same as unified account login.
		fallthrough
	default:
		acc, authenticated = s.authenticateAccountLogin(w, r, req.Username, req.Password, ip)
	}
	if !authenticated {
		return // error response already written by the authenticate* helper
	}
	// Credentials verified — proceed to MFA + session issuance.
	// SMS OTP replaces password; pass empty password into completeLogin.
	pass := req.Password
	if req.LoginType == "sms" {
		pass = ""
	}
	s.completeLogin(w, r, acc, pass, req.Code, ip)
}

// looksLikePhone reports whether id is shaped like a mainland mobile number
// (11 digits starting with 1), after stripping spaces/dashes.
func looksLikePhone(id string) bool {
	s := strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, strings.TrimSpace(id))
	if len(s) != 11 || s[0] != '1' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// authenticateAccountLogin accepts username or bound phone + password.
func (s *Server) authenticateAccountLogin(w http.ResponseWriter, r *http.Request, id, password, ip string) (AccountConfig, bool) {
	id = strings.TrimSpace(id)
	if looksLikePhone(id) {
		if _, found := s.cfg.UserByPhone(id); found {
			return s.authenticatePhoneLogin(w, r, id, password, ip)
		}
	}
	return s.authenticateUsernameLogin(w, r, id, password, ip)
}

// authenticateUsernameLogin verifies username+password via CheckPassword (which
// handles PBKDF2 upgrade). Returns the resolved account and whether authentication
// succeeded. On failure, writes the error response and returns false.
func (s *Server) authenticateUsernameLogin(w http.ResponseWriter, r *http.Request, username, password, ip string) (AccountConfig, bool) {
	acc, ok := s.auth.CheckPassword(username, password)
	if !ok {
		s.auth.loginFailed(ip)
		s.auth.loginAccountFailed(username)
		s.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: username, Username: username, IP: ip, Message: Tz("log.login_failed", username)})
		if !s.auth.loginAllowed(ip) {
			s.autoDefendOnLoginLockout(ip, username)
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.invalid_credentials")})
		return acc, false
	}
	return acc, true
}

// authenticatePhoneLogin resolves an account by phone number and verifies the
// password separately (since CheckPassword uses UserByName). On failure, writes
// the error response and returns false.
func (s *Server) authenticatePhoneLogin(w http.ResponseWriter, r *http.Request, phone, password, ip string) (AccountConfig, bool) {
	acc, found := s.cfg.UserByPhone(phone)
	if !found {
		hashPassword(password, "dummy-salt-000000") // constant-ish timing
		s.auth.loginFailed(ip)
		s.auth.loginAccountFailed(phone)
		s.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: ip, IP: ip, Message: Tz("log.login_failed", "phone:"+phone)})
		if !s.auth.loginAllowed(ip) {
			s.autoDefendOnLoginLockout(ip, "phone:"+phone)
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.invalid_credentials")})
		return acc, false
	}
	if !verifyPassword(password, acc.Salt, acc.Hash) {
		s.auth.loginFailed(ip)
		s.auth.loginAccountFailed(acc.Username)
		s.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: acc.Username, Username: acc.Username, IP: ip, Message: Tz("log.login_failed", acc.Username)})
		if !s.auth.loginAllowed(ip) {
			s.autoDefendOnLoginLockout(ip, acc.Username)
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.invalid_credentials")})
		return acc, false
	}
	if !s.auth.loginAccountAllowed(acc.Username) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": Tr(r, "auth.too_many_attempts")})
		return acc, false
	}
	return acc, true
}

// authenticateSMSLogin verifies a one-time SMS code previously issued by
// handleLoginSMSCode. On failure, writes the error response and returns false.
// The OTP is NOT consumed here — completeLogin deletes it only after a session
// is issued, so MFA round-trips can resubmit the same sms_code (like password).
func (s *Server) authenticateSMSLogin(w http.ResponseWriter, r *http.Request, phone, smsCode, ip string) (AccountConfig, bool) {
	phone = strings.TrimSpace(phone)
	smsCode = strings.TrimSpace(smsCode)
	var acc AccountConfig
	if phone == "" || smsCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "login.sms_code_required")})
		return acc, false
	}
	acc, found := s.cfg.UserByPhone(phone)
	if !found {
		s.auth.loginFailed(ip)
		s.auth.loginAccountFailed(phone)
		s.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: ip, IP: ip, Message: Tz("log.login_failed", "sms:"+maskPhone(phone))})
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.invalid_credentials")})
		return acc, false
	}
	if !s.auth.loginAccountAllowed(acc.Username) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": Tr(r, "auth.too_many_attempts")})
		return acc, false
	}
	smsCodeMu.Lock()
	entry, ok := smsCodes[phone]
	valid := ok && time.Now().Before(entry.ExpireAt) && subtle.ConstantTimeCompare([]byte(entry.Code), []byte(smsCode)) == 1
	smsCodeMu.Unlock()
	if !valid {
		s.auth.loginFailed(ip)
		s.auth.loginAccountFailed(acc.Username)
		s.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: acc.Username, Username: acc.Username, IP: ip, Message: Tz("log.login_failed", "sms:"+acc.Username)})
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "login.sms_code_invalid")})
		return acc, false
	}
	return acc, true
}

// consumeSMSLoginCode removes a pending OTP after a successful login session is issued.
func consumeSMSLoginCode(phone string) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return
	}
	smsCodeMu.Lock()
	delete(smsCodes, phone)
	smsCodeMu.Unlock()
}

// completeLogin handles the post-authentication phase: default-credential
// detection, MFA second factor, session issuance and the response.
func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, acc AccountConfig, password, code, ip string) {
	// Detect default admin/admin even if an older config cleared MustChangePassword
	// (pre-v5.4 installs). This does NOT authorize login by itself — CheckPassword
	// already verified the hash; we only force the change gate.
	if acc.Username == "admin" && password == "admin" {
		if !acc.MustChangePassword {
			s.cfg.SetMustChangePassword(acc.Username)
			acc.MustChangePassword = true
		}
	}
	// Password OK. If MFA is on, require a valid TOTP code as the second factor.
	// The requirement is revealed only AFTER the password checks out, so an
	// unauthenticated prober can't learn whether the account has MFA enabled.
	if acc.MFAEnabled {
		if strings.TrimSpace(code) == "" {
			writeJSON(w, http.StatusOK, map[string]any{"mfa_required": true})
			return
		}
		switch res := s.auth.verifyAndConsumeTOTP(acc.Username, acc.MFASecret, code); res {
		case totpOK:
			// continue to session issuance
		case totpReplay:
			// Valid code already spent — do NOT count toward lockout (common when
			// the authenticator still shows the same digits and the user retries).
			s.store.AddLog(LogEntry{Kind: KindSystem, Level: "info", Actor: acc.Username, Username: acc.Username, IP: ip, Message: Tz("log.totp_replay", acc.Username)})
			s.writeTOTPFailure(w, r, totpReplay, http.StatusUnauthorized)
			return
		default:
			s.auth.loginFailed(ip)
			s.auth.loginAccountFailed(acc.Username)
			s.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: acc.Username, Username: acc.Username, IP: ip, Message: Tz("log.totp_failed", acc.Username)})
			s.writeTOTPFailure(w, r, totpInvalid, http.StatusUnauthorized)
			return
		}
	}
	// Credentials fully verified — clear the per-account failed-attempt counter.
	s.auth.loginAccountReset(acc.Username)
	// Burn SMS OTP only after MFA (if any) passed — keeps the same sms_code
	// usable across the mfa_required round-trip.
	consumeSMSLoginCode(acc.Phone)
	// Forced password change BEFORE full access or MFA enrollment. A full
	// session here would let API clients skip the SPA gate with admin/admin.
	if acc.MustChangePassword {
		tok := s.auth.issuePasswordChangeSession(acc.Username)
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
			Secure:   s.isHTTPS(r),
			SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL / time.Second),
		})
		msg := Tz("log.login_success", acc.Username)
		if acc.Username == "admin" && password == "admin" {
			msg = Tz("log.default_credentials", acc.Username)
		}
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: acc.Username, Username: acc.Username, IP: ip, Message: msg})
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                   true,
			"must_change_password": true,
			"message":              Tr(r, "auth.must_change_password"),
		})
		return
	}
	// Global MFA policy: if admin has enabled MFARequired and this user hasn't
	// set up MFA yet, issue a restricted session and direct them to enroll.
	if s.cfg.MFARequired() && !acc.MFAEnabled {
		tok := s.auth.issueRestrictedSession(acc.Username)
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
			Secure:   s.isHTTPS(r),
			SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL / time.Second),
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"require_mfa_setup": true,
			"message":           Tr(r, "auth.global_mfa_required"),
		})
		return
	}
	tok := s.auth.issueSession(acc.Username)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL / time.Second),
	})
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: acc.Username, Username: acc.Username, IP: ip, Message: Tz("log.login_success", acc.Username)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMe returns the current profile, or 401 if not logged in (the panel uses
// this to decide whether to show the login screen).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": acc.Username, "display_name": acc.DisplayName, "email": acc.Email, "phone": acc.Phone,
		"mfa_enabled": acc.MFAEnabled, "role": acc.Role,
		"must_change_password": acc.MustChangePassword,
	})
}

func (s *Server) handleSetProfile(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	var req struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		Phone       string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	name := acc.Username
	// Optional self-rename: validate, apply, then repoint the session so the
	// current cookie keeps working under the new username.
	if strings.TrimSpace(req.Username) != "" && req.Username != acc.Username {
		uname := sanitizeUsername(req.Username)
		if uname == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.invalid_username_format")})
			return
		}
		oldName := acc.Username
		if err := s.cfg.RenameUser(oldName, uname); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.auth.renameSessions(oldName, uname)
		if s.sqlChanges != nil {
			s.sqlChanges.RenameActor(oldName, uname)
		}
		name = uname
	}
	_ = s.cfg.SetUserProfile(name, strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Email), strings.TrimSpace(req.Phone))
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.update_profile", name)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": name})
}

// ---- SMS verification code (login OTP) ----

// smsCodeEntry is a temporary in-memory store for SMS verification codes.
type smsCodeEntry struct {
	Code     string
	ExpireAt time.Time
}

var (
	smsCodeMu sync.Mutex
	smsCodes  = map[string]smsCodeEntry{} // phone -> entry
	smsLastMu sync.Mutex
	smsLast   = map[string]time.Time{} // phone -> last send time (rate limit)
)

// handleLoginSMSCode sends a 6-digit verification code to the given phone number
// via the configured cloud SMS channel (same as alert SMS).
// TemplateParam should use ${code} (e.g. {"code":"${code}"}) for OTP templates.
func (s *Server) handleLoginSMSCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	phone := strings.TrimSpace(req.Phone)
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "login.phone_required")})
		return
	}
	cfgSnap := s.cfg.Get()
	if !cfgSnap.SMS.Enabled {
		writeAPIError(w, r, http.StatusServiceUnavailable, "sms_disabled", Tr(r, "login.sms_disabled"))
		return
	}
	if s.notifier == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "sms_unavailable", Tr(r, "login.sms_unavailable"))
		return
	}
	// Check if phone is registered — do not reveal existence.
	_, found := s.cfg.UserByPhone(phone)
	if !found {
		time.Sleep(80 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]any{"message": Tr(r, "login.sms_sent")})
		return
	}
	// Rate limit: 60s between sends
	smsLastMu.Lock()
	pruneExpiredTimeMap(smsLast, time.Now(), maxSMSLast, func(t time.Time) time.Time {
		return t.Add(time.Hour)
	})
	last, exists := smsLast[phone]
	if exists && time.Since(last) < 60*time.Second {
		smsLastMu.Unlock()
		writeAPIError(w, r, http.StatusTooManyRequests, "rate_limited", Tr(r, "recovery.rate_limited"))
		return
	}
	smsLast[phone] = time.Now()
	smsLastMu.Unlock()
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "sms_gen_failed", "failed to generate code")
		return
	}
	code := fmt.Sprintf("%06d", n.Int64())
	smsCodeMu.Lock()
	pruneExpiredTimeMap(smsCodes, time.Now(), maxSMSCodes, func(e smsCodeEntry) time.Time { return e.ExpireAt })
	smsCodes[phone] = smsCodeEntry{Code: code, ExpireAt: time.Now().Add(5 * time.Minute)}
	smsCodeMu.Unlock()
	smsCfg := cfgSnap.SMS
	smsCfg.Phones = []string{phone}
	// Login OTP templates should use {"code":"${code}"}; if TemplateParam is empty
	// the SMS layer defaults to {"message":"<code>"} which may not match OTP templates.
	if strings.TrimSpace(smsCfg.TemplateParam) == "" {
		smsCfg.TemplateParam = `{"code":"${code}"}`
	}
	if err := s.notifier.sendSMS(smsCfg, code); err != nil {
		smsCodeMu.Lock()
		delete(smsCodes, phone)
		smsCodeMu.Unlock()
		slog.Warn("login SMS send failed", "err", err, "phone_suffix", maskPhone(phone))
		writeAPIError(w, r, http.StatusBadGateway, "sms_send_failed", Tr(r, "login.sms_send_failed"))
		return
	}
	s.store.AddLog(LogEntry{Kind: KindSystem, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r), Message: fmt.Sprintf("SMS login code sent to %s", maskPhone(phone))})
	writeJSON(w, http.StatusOK, map[string]any{"message": Tr(r, "login.sms_sent")})
}

func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if !verifyPassword(req.Old, acc.Salt, acc.Hash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.wrong_old_password")})
		return
	}
	if !validatePasswordStrength(strings.TrimSpace(req.New)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.password_policy")})
		return
	}
	_ = s.cfg.SetUserPassword(acc.Username, req.New)
	// v5.4.0: clear MustChangePassword flag after a successful self-change
	s.cfg.ClearMustChangePassword(acc.Username)
	// Invalidate only THIS user's other sessions, then re-issue one for the current.
	s.auth.clearUserSessions(acc.Username)
	tok := s.auth.issueSession(acc.Username)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL / time.Second),
	})
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.change_password", acc.Username)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAccountInit performs the forced first-login credential setup in one
// atomic step: the user picks a new username AND a new password without
// re-entering the old one. It is deliberately gated on MustChangePassword being
// set — i.e. it only works right after a forced-change login (default admin/admin
// or an admin password reset) and refuses once the flag is cleared. So it can
// never be abused during normal operation to bypass the old-password check in
// handleSetPassword.
func (s *Server) handleAccountInit(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	if !acc.MustChangePassword {
		// Forced-init window is closed: normal changes must go through
		// /profile + /password (which verify the current password).
		writeJSON(w, http.StatusForbidden, map[string]string{"error": Tr(r, "auth.init_not_required")})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if !validatePasswordStrength(strings.TrimSpace(req.Password)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.password_policy")})
		return
	}
	name := acc.Username
	// Optional self-rename: validate, apply, then repoint the session so the
	// current cookie keeps working under the new username.
	if strings.TrimSpace(req.Username) != "" && req.Username != acc.Username {
		uname := sanitizeUsername(req.Username)
		if uname == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.invalid_username_format")})
			return
		}
		oldName := acc.Username
		if err := s.cfg.RenameUser(oldName, uname); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.auth.renameSessions(oldName, uname)
		if s.sqlChanges != nil {
			s.sqlChanges.RenameActor(oldName, uname)
		}
		name = uname
	}
	_ = s.cfg.SetUserPassword(name, req.Password)
	s.cfg.ClearMustChangePassword(name)
	// Force a fresh re-login: invalidate ALL of this user's sessions (including the
	// current one) and clear the session cookie, so the user must sign in again
	// with the new credentials. This confirms they actually know the new password
	// and starts a clean session under the (possibly renamed) account.
	s.auth.clearUserSessions(name)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: s.isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.change_password", name)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": name, "relogin": true})
}

// ---- MFA (TOTP two-factor) ----

// handleMFASetup issues a fresh TOTP secret + provisioning URL for the current
// user's enrollment. It does NOT enable MFA — the client must prove one valid
// code via handleMFAEnable, so a mis-scanned secret can never lock them out.
func (s *Server) handleMFASetup(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	secret := genTOTPSecret()
	if secret == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": Tr(r, "auth.gen_secret_failed")})
		return
	}
	uri := otpauthURL(acc.Username, secret)
	qr, err := genQRDataURI(uri)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": Tr(r, "auth.gen_qr_failed")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":      secret,
		"otpauth_url": uri,
		"qr_datauri":  qr,
	})
}

// handleMFAEnable turns the current user's MFA on after verifying they can
// produce a current code for the freshly-issued secret.
func (s *Server) handleMFAEnable(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	var req struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if !totpVerify(req.Secret, req.Code) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.totp_verify_failed")})
		return
	}
	_ = s.cfg.SetUserMFA(acc.Username, true, strings.TrimSpace(req.Secret))
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.enable_mfa", acc.Username)})
	// Upgrade a restricted session (global MFA enforcement) to a full session.
	s.auth.upgradeSession(r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- Global MFA policy ----

// handleMFAGlobalGet returns the current global MFA enforcement state.
func (s *Server) handleMFAGlobalGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"mfa_required": s.cfg.MFARequired(),
	})
}

// handleMFAGlobalSet toggles the global MFA enforcement policy (admin only).
func (s *Server) handleMFAGlobalSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Required bool `json:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.SetMFARequired(req.Required); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": Tr(r, "auth.save_failed")})
		return
	}
	action := Tz("log.global_mfa_off")
	if req.Required {
		action = Tz("log.global_mfa_on")
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: action})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mfa_required": req.Required})
}

// handleMFADisable turns the current user's MFA off after re-verifying their
// password, so a hijacked-but-unlocked session alone can't strip the factor.
func (s *Server) handleMFADisable(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if !verifyPassword(req.Password, acc.Salt, acc.Hash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.wrong_password")})
		return
	}
	_ = s.cfg.SetUserMFA(acc.Username, false, "")
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.disable_mfa", acc.Username)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
