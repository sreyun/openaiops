package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Multi-provider OAuth SSO (Feishu / DingTalk / WeChat Open Platform / WeCom)
// alongside OIDC. Alert webhooks under ServerConfig.Feishu / Dingtalk are
// unrelated — login apps use SSOConfig only.
// ============================================================================

const (
	ssoProviderOIDC     = "oidc"
	ssoProviderFeishu   = "feishu"
	ssoProviderDingtalk = "dingtalk"
	ssoProviderWechat   = "wechat"
	ssoProviderWecom    = "wecom"
)

// SSOProviderConfig is one OAuth login application.
type SSOProviderConfig struct {
	Enabled     bool   `json:"enabled"`
	AppID       string `json:"app_id,omitempty"`     // Feishu app_id / DingTalk client_id / WeChat appid / WeCom corpid
	AppSecret   string `json:"app_secret,omitempty"` // never echoed to browser
	AgentID     string `json:"agent_id,omitempty"`   // WeCom agentid (required for WeCom QR)
	RedirectURL string `json:"redirect_url,omitempty"`
	DefaultRole string `json:"default_role,omitempty"` // admin|operator|viewer; empty = deny
	AutoCreate  bool   `json:"auto_create,omitempty"`
	// DeptRoleMap / TagRoleMap map IdP department or tag names → role (first match).
	DeptRoleMap map[string]string `json:"dept_role_map,omitempty"`
	TagRoleMap  map[string]string `json:"tag_role_map,omitempty"`
}

// SSOConfig holds Feishu / DingTalk / WeChat / WeCom login settings.
type SSOConfig struct {
	Feishu   SSOProviderConfig `json:"feishu,omitempty"`
	Dingtalk SSOProviderConfig `json:"dingtalk,omitempty"`
	Wechat   SSOProviderConfig `json:"wechat,omitempty"`
	Wecom    SSOProviderConfig `json:"wecom,omitempty"`
}

type ssoStateEntry struct {
	Provider  string
	Nonce     string
	BindUser  string // non-empty = bind to this logged-in user
	ExpiresAt int64
}

var (
	ssoStates   = map[string]ssoStateEntry{}
	ssoStatesMu sync.Mutex
)

// ssoProfile is the normalized identity returned by a provider adapter.
type ssoProfile struct {
	Provider     string
	Subject      string // stable: prefer union_id, else open_id / sub
	UsernameHint string
	DisplayName  string
	Email        string
	Depts        []string
	Tags         []string
}

func (cs *ConfigStore) SSOConfig() SSOConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.cfg.SSO
}

func (cs *ConfigStore) SetSSOConfig(c SSOConfig) error {
	cs.mu.Lock()
	keepSecret := func(in, prev string) string {
		if in == "" || strings.Contains(in, "****") {
			return prev
		}
		return in
	}
	c.Feishu.AppSecret = keepSecret(c.Feishu.AppSecret, cs.cfg.SSO.Feishu.AppSecret)
	c.Dingtalk.AppSecret = keepSecret(c.Dingtalk.AppSecret, cs.cfg.SSO.Dingtalk.AppSecret)
	c.Wechat.AppSecret = keepSecret(c.Wechat.AppSecret, cs.cfg.SSO.Wechat.AppSecret)
	c.Wecom.AppSecret = keepSecret(c.Wecom.AppSecret, cs.cfg.SSO.Wecom.AppSecret)
	normalizeSSOProvider := func(p *SSOProviderConfig) {
		if p.DefaultRole != "" && !validRole(p.DefaultRole) {
			p.DefaultRole = RoleViewer
		}
	}
	normalizeSSOProvider(&c.Feishu)
	normalizeSSOProvider(&c.Dingtalk)
	normalizeSSOProvider(&c.Wechat)
	normalizeSSOProvider(&c.Wecom)
	cs.cfg.SSO = c
	cs.mu.Unlock()
	return cs.save()
}

func maskSSOConfig(c SSOConfig) SSOConfig {
	if c.Feishu.AppSecret != "" {
		c.Feishu.AppSecret = "****"
	}
	if c.Dingtalk.AppSecret != "" {
		c.Dingtalk.AppSecret = "****"
	}
	if c.Wechat.AppSecret != "" {
		c.Wechat.AppSecret = "****"
	}
	if c.Wecom.AppSecret != "" {
		c.Wecom.AppSecret = "****"
	}
	return c
}

func (s *Server) handleGetSSOConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, maskSSOConfig(s.cfg.SSOConfig()))
}

func (s *Server) handleSetSSOConfig(w http.ResponseWriter, r *http.Request) {
	var c SSOConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.SetSSOConfig(c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: "更新多提供商 SSO 配置"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSSOLoginInfo lists enabled SSO providers for the login page buttons.
func (s *Server) handleSSOLoginInfo(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.SSOConfig()
	oidc := s.cfg.OIDCConfig()
	type item struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		LoginURL string `json:"login_url"`
	}
	out := []item{}
	if oidc.Enabled && strings.TrimSpace(oidc.Issuer) != "" && strings.TrimSpace(oidc.ClientID) != "" {
		out = append(out, item{ID: ssoProviderOIDC, Name: "OIDC", LoginURL: "/api/v1/auth/oidc/login"})
	}
	if ssoProviderReady(cfg.Feishu) {
		out = append(out, item{ID: ssoProviderFeishu, Name: "飞书", LoginURL: "/api/v1/auth/feishu/login"})
	}
	if ssoProviderReady(cfg.Dingtalk) {
		out = append(out, item{ID: ssoProviderDingtalk, Name: "钉钉", LoginURL: "/api/v1/auth/dingtalk/login"})
	}
	if ssoProviderReady(cfg.Wechat) {
		out = append(out, item{ID: ssoProviderWechat, Name: "微信", LoginURL: "/api/v1/auth/wechat/login"})
	}
	if ssoWecomReady(cfg.Wecom) {
		out = append(out, item{ID: ssoProviderWecom, Name: "企业微信", LoginURL: "/api/v1/auth/wecom/login"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func ssoProviderReady(p SSOProviderConfig) bool {
	return p.Enabled && strings.TrimSpace(p.AppID) != "" && strings.TrimSpace(p.AppSecret) != ""
}

func ssoWecomReady(p SSOProviderConfig) bool {
	return ssoProviderReady(p) && strings.TrimSpace(p.AgentID) != ""
}

func (s *Server) ssoBindUsername(r *http.Request) (string, bool) {
	if r.URL.Query().Get("bind") != "1" {
		return "", false
	}
	acc, ok := s.currentUser(r)
	if !ok || strings.TrimSpace(acc.Username) == "" {
		return "", false
	}
	return acc.Username, true
}

func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	provider := ssoProviderFromPath(r.URL.Path)
	cfg, ok := s.ssoProviderConfig(provider)
	ready := ssoProviderReady(cfg)
	if provider == ssoProviderWecom {
		ready = ssoWecomReady(cfg)
	}
	if !ok || !ready {
		s.redirectSSOError(w, r, provider, "disabled")
		return
	}
	bindUser, okBind := s.ssoBindUsername(r)
	if r.URL.Query().Get("bind") == "1" && !okBind {
		s.redirectSSOError(w, r, provider, "bind_auth")
		return
	}
	state := randomOIDCToken(24)
	nonce := randomOIDCToken(16)
	ssoStatesMu.Lock()
	pruneExpiredUnixMap(ssoStates, time.Now().Unix(), maxSSOStates, func(e ssoStateEntry) int64 { return e.ExpiresAt })
	ssoStates[state] = ssoStateEntry{Provider: provider, Nonce: nonce, BindUser: bindUser, ExpiresAt: time.Now().Add(10 * time.Minute).Unix()}
	ssoStatesMu.Unlock()
	redirect := s.ssoRedirectURL(r, provider, cfg)
	authURL, err := ssoAuthorizeURL(provider, cfg, redirect, state)
	if err != nil {
		s.redirectSSOError(w, r, provider, "config")
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	provider := ssoProviderFromPath(r.URL.Path)
	cfg, ok := s.ssoProviderConfig(provider)
	ready := ssoProviderReady(cfg)
	if provider == ssoProviderWecom {
		ready = ssoWecomReady(cfg)
	}
	if !ok || !ready {
		s.redirectSSOError(w, r, provider, "disabled")
		return
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		code := "denied"
		if !strings.EqualFold(errMsg, "access_denied") {
			code = "idp"
		}
		s.redirectSSOError(w, r, provider, code)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	ssoStatesMu.Lock()
	st, ok := ssoStates[state]
	delete(ssoStates, state)
	ssoStatesMu.Unlock()
	if !ok || st.Provider != provider || time.Now().Unix() > st.ExpiresAt || code == "" {
		s.redirectSSOError(w, r, provider, "state")
		return
	}
	redirect := s.ssoRedirectURL(r, provider, cfg)
	profile, err := ssoExchangeAndProfile(provider, cfg, redirect, code)
	if err != nil {
		slog.Warn("SSO exchange failed", "provider", provider, "err", err)
		s.redirectSSOError(w, r, provider, "exchange")
		return
	}
	if st.BindUser != "" {
		if err := s.cfg.BindUserIdentity(st.BindUser, profile.Provider, profile.Subject); err != nil {
			s.redirectSSOError(w, r, provider, ssoProvisionErrorCode(err))
			return
		}
		s.redirectSSOBound(w, r, provider)
		return
	}
	role := mapSSORole(profile.Depts, profile.Tags, cfg)
	if role == "" {
		s.redirectSSOError(w, r, provider, "no_role")
		return
	}
	username, err := s.provisionSSOUser(ssoProvisionReq{
		Provider:     profile.Provider,
		Subject:      profile.Subject,
		UsernameHint: profile.UsernameHint,
		DisplayName:  profile.DisplayName,
		Email:        profile.Email,
		Role:         role,
		AutoCreate:   cfg.AutoCreate,
	})
	if err != nil {
		s.redirectSSOError(w, r, provider, ssoProvisionErrorCode(err))
		return
	}
	s.finishSSOLogin(w, r, username, provider)
}

func (s *Server) finishSSOLogin(w http.ResponseWriter, r *http.Request, username, provider string) {
	sessTok := s.auth.issueSession(username)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sessTok, Path: "/", HttpOnly: true,
		Secure: s.isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL / time.Second),
	})
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: username, IP: s.clientIP(r),
		Message: fmt.Sprintf("SSO 登录成功（%s）", provider)})
	http.Redirect(w, r, "/", http.StatusFound)
}

// redirectSSOError sends the browser back to the login page with a stable error code
// for localized display (never dump raw IdP/English text into the login card).
func (s *Server) redirectSSOError(w http.ResponseWriter, r *http.Request, provider, code string) {
	if code == "" {
		code = "unknown"
	}
	q := url.Values{}
	q.Set("sso_error", code)
	if provider != "" {
		q.Set("sso_provider", provider)
	}
	http.Redirect(w, r, "/?"+q.Encode(), http.StatusFound)
}

func (s *Server) redirectSSOBound(w http.ResponseWriter, r *http.Request, provider string) {
	q := url.Values{}
	q.Set("sso_bound", "1")
	if provider != "" {
		q.Set("sso_provider", provider)
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: fmt.Sprintf("SSO 身份绑定成功（%s）", provider)})
	http.Redirect(w, r, "/?"+q.Encode(), http.StatusFound)
}

func ssoProvisionErrorCode(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not provisioned"), strings.Contains(msg, "auto_create"):
		return "no_user"
	case strings.Contains(msg, "no mapped role"):
		return "no_role"
	case strings.Contains(msg, "already bound"):
		return "conflict"
	case strings.Contains(msg, "no usable username"), strings.Contains(msg, "subject missing"):
		return "bad_profile"
	case strings.Contains(msg, "create user"):
		return "provision"
	case strings.Contains(msg, "not found"):
		return "no_user"
	default:
		return "unknown"
	}
}

// handleListSSOIdentities returns the current user's bound SSO identities.
func (s *Server) handleListSSOIdentities(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	ids := make([]ExternalIdentity, 0, len(acc.Identities))
	ids = append(ids, acc.Identities...)
	cfg := s.cfg.SSOConfig()
	oidc := s.cfg.OIDCConfig()
	type avail struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		BindURL string `json:"bind_url"`
		Bound   bool   `json:"bound"`
		Enabled bool   `json:"enabled"`
	}
	bound := map[string]bool{}
	for _, id := range ids {
		bound[strings.ToLower(id.Provider)] = true
	}
	out := []avail{}
	if oidc.Enabled && strings.TrimSpace(oidc.Issuer) != "" && strings.TrimSpace(oidc.ClientID) != "" {
		out = append(out, avail{ID: ssoProviderOIDC, Name: "OIDC", BindURL: "/api/v1/auth/oidc/login?bind=1", Bound: bound[ssoProviderOIDC], Enabled: true})
	}
	if ssoProviderReady(cfg.Feishu) {
		out = append(out, avail{ID: ssoProviderFeishu, Name: "飞书", BindURL: "/api/v1/auth/feishu/login?bind=1", Bound: bound[ssoProviderFeishu], Enabled: true})
	}
	if ssoProviderReady(cfg.Dingtalk) {
		out = append(out, avail{ID: ssoProviderDingtalk, Name: "钉钉", BindURL: "/api/v1/auth/dingtalk/login?bind=1", Bound: bound[ssoProviderDingtalk], Enabled: true})
	}
	if ssoProviderReady(cfg.Wechat) {
		out = append(out, avail{ID: ssoProviderWechat, Name: "微信", BindURL: "/api/v1/auth/wechat/login?bind=1", Bound: bound[ssoProviderWechat], Enabled: true})
	}
	if ssoWecomReady(cfg.Wecom) {
		out = append(out, avail{ID: ssoProviderWecom, Name: "企业微信", BindURL: "/api/v1/auth/wecom/login?bind=1", Bound: bound[ssoProviderWecom], Enabled: true})
	}
	writeJSON(w, http.StatusOK, map[string]any{"identities": ids, "providers": out})
}

// handleUnbindSSOIdentity removes one provider binding from the current user.
func (s *Server) handleUnbindSSOIdentity(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	provider := strings.TrimSpace(strings.ToLower(r.PathValue("provider")))
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider required"})
		return
	}
	if err := s.cfg.UnbindUserIdentity(acc.Username, provider); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: acc.Username, IP: s.clientIP(r),
		Message: fmt.Sprintf("解除 SSO 绑定（%s）", provider)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type ssoProvisionReq struct {
	Provider     string
	Subject      string
	UsernameHint string
	DisplayName  string
	Email        string
	Role         string
	AutoCreate   bool
}

// provisionSSOUser finds or creates a local user for an SSO subject and binds the identity.
func (s *Server) provisionSSOUser(req ssoProvisionReq) (string, error) {
	req.Provider = strings.TrimSpace(strings.ToLower(req.Provider))
	req.Subject = strings.TrimSpace(req.Subject)
	if req.Provider == "" || req.Subject == "" {
		return "", fmt.Errorf("SSO subject missing")
	}
	if !validRole(req.Role) {
		return "", fmt.Errorf("SSO user has no mapped role")
	}
	if existing, ok := s.cfg.UserByIdentity(req.Provider, req.Subject); ok {
		display := req.DisplayName
		email := req.Email
		if display == "" {
			display = existing.DisplayName
		}
		if email == "" {
			email = existing.Email
		}
		_ = s.cfg.UpdateUserMeta(existing.Username, display, email, req.Role)
		return existing.Username, nil
	}

	base := deriveSSOUsername(req.Provider, req.UsernameHint, req.Email, req.Subject)
	if base == "" {
		return "", fmt.Errorf("SSO user has no usable username")
	}
	// Never auto-bind an IdP subject to an existing local username on the
	// unauthenticated login path. UsernameHint / email local-part come from the
	// IdP (preferred_username, nick, …); an attacker who can assert hint "admin"
	// would otherwise take over the unbound local admin account and get a full
	// session via finishSSOLogin. Linking must go through the authenticated
	// bind flow (st.BindUser != ""). allocateSSOUsername below suffixes on clash.

	if !req.AutoCreate {
		return "", fmt.Errorf("user not provisioned; enable auto_create or create locally")
	}
	username := allocateSSOUsername(s.cfg, base)
	pw := randomOIDCToken(24) + "Aa1!"
	display := req.DisplayName
	if display == "" {
		display = username
	}
	if err := s.cfg.CreateUser(username, pw, display, req.Email, req.Role); err != nil {
		return "", fmt.Errorf("create user failed: %w", err)
	}
	if err := s.cfg.BindUserIdentity(username, req.Provider, req.Subject); err != nil {
		return "", err
	}
	return username, nil
}

func deriveSSOUsername(provider, hint, email, subject string) string {
	if u := sanitizeUsername(hint); u != "" {
		return u
	}
	email = strings.TrimSpace(email)
	if i := strings.Index(email, "@"); i > 0 {
		if u := sanitizeUsername(email[:i]); u != "" {
			return u
		}
	}
	// Fallback: provider + short subject hash-like suffix
	sub := subject
	if len(sub) > 12 {
		sub = sub[len(sub)-12:]
	}
	raw := provider + "_" + strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, sub)
	if u := sanitizeUsername(raw); u != "" {
		return u
	}
	if u := sanitizeUsername(provider + "_user"); u != "" {
		return u
	}
	return ""
}

func allocateSSOUsername(cs *ConfigStore, base string) string {
	if _, exists := cs.UserByName(base); !exists {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := base
		suffix := fmt.Sprintf("_%d", i)
		if len(cand)+len(suffix) > 32 {
			cand = cand[:32-len(suffix)]
		}
		cand = cand + suffix
		if sanitizeUsername(cand) == "" {
			continue
		}
		if _, exists := cs.UserByName(cand); !exists {
			return cand
		}
	}
	return sanitizeUsername(base + "_" + randomOIDCToken(4)[:6])
}

func mapSSORole(depts, tags []string, cfg SSOProviderConfig) string {
	for _, d := range depts {
		if role, ok := cfg.DeptRoleMap[d]; ok && validRole(role) {
			return role
		}
	}
	for _, t := range tags {
		if role, ok := cfg.TagRoleMap[t]; ok && validRole(role) {
			return role
		}
	}
	if validRole(cfg.DefaultRole) {
		return cfg.DefaultRole
	}
	return ""
}

func (s *Server) ssoProviderConfig(provider string) (SSOProviderConfig, bool) {
	c := s.cfg.SSOConfig()
	switch provider {
	case ssoProviderFeishu:
		return c.Feishu, true
	case ssoProviderDingtalk:
		return c.Dingtalk, true
	case ssoProviderWechat:
		return c.Wechat, true
	case ssoProviderWecom:
		return c.Wecom, true
	default:
		return SSOProviderConfig{}, false
	}
}

func ssoProviderFromPath(path string) string {
	// /api/v1/auth/{provider}/login|callback
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "auth" {
		return strings.ToLower(parts[3])
	}
	return ""
}

func (s *Server) ssoRedirectURL(r *http.Request, provider string, cfg SSOProviderConfig) string {
	if u := strings.TrimSpace(cfg.RedirectURL); u != "" {
		return u
	}
	base := strings.TrimRight(s.serverURL(r), "/")
	return base + "/api/v1/auth/" + provider + "/callback"
}

func ssoAuthorizeURL(provider string, cfg SSOProviderConfig, redirect, state string) (string, error) {
	switch provider {
	case ssoProviderFeishu:
		q := url.Values{}
		q.Set("app_id", cfg.AppID)
		q.Set("redirect_uri", redirect)
		q.Set("state", state)
		return "https://open.feishu.cn/open-apis/authen/v1/index?" + q.Encode(), nil
	case ssoProviderDingtalk:
		q := url.Values{}
		q.Set("redirect_uri", redirect)
		q.Set("response_type", "code")
		q.Set("client_id", cfg.AppID)
		q.Set("scope", "openid")
		q.Set("state", state)
		q.Set("prompt", "consent")
		return "https://login.dingtalk.com/oauth2/auth?" + q.Encode(), nil
	case ssoProviderWechat:
		q := url.Values{}
		q.Set("appid", cfg.AppID)
		q.Set("redirect_uri", redirect)
		q.Set("response_type", "code")
		q.Set("scope", "snsapi_login")
		q.Set("state", state)
		return "https://open.weixin.qq.com/connect/qrconnect?" + q.Encode() + "#wechat_redirect", nil
	case ssoProviderWecom:
		if strings.TrimSpace(cfg.AgentID) == "" {
			return "", fmt.Errorf("wecom agent_id required")
		}
		q := url.Values{}
		q.Set("appid", cfg.AppID)
		q.Set("agentid", cfg.AgentID)
		q.Set("redirect_uri", redirect)
		q.Set("state", state)
		return "https://open.work.weixin.qq.com/wwopen/sso/qrConnect?" + q.Encode(), nil
	default:
		return "", fmt.Errorf("unknown SSO provider")
	}
}

func ssoExchangeAndProfile(provider string, cfg SSOProviderConfig, redirect, code string) (ssoProfile, error) {
	switch provider {
	case ssoProviderFeishu:
		return feishuSSOProfile(cfg, redirect, code)
	case ssoProviderDingtalk:
		return dingtalkSSOProfile(cfg, code)
	case ssoProviderWechat:
		return wechatSSOProfile(cfg, code)
	case ssoProviderWecom:
		return wecomSSOProfile(cfg, code)
	default:
		return ssoProfile{}, fmt.Errorf("unknown SSO provider")
	}
}

func feishuSSOProfile(cfg SSOProviderConfig, _ /* redirect */, code string) (ssoProfile, error) {
	var out ssoProfile
	out.Provider = ssoProviderFeishu
	appTok, err := feishuAppAccessToken(cfg.AppID, cfg.AppSecret)
	if err != nil {
		return out, err
	}
	userTok, err := feishuUserAccessToken(appTok, code)
	if err != nil {
		return out, err
	}
	info, err := feishuUserInfo(userTok)
	if err != nil {
		return out, err
	}
	unionID := strings.TrimSpace(strClaim(info, "union_id"))
	openID := strings.TrimSpace(strClaim(info, "open_id"))
	out.Subject = unionID
	if out.Subject == "" {
		out.Subject = openID
	}
	if out.Subject == "" {
		return out, fmt.Errorf("feishu: no open_id")
	}
	out.DisplayName = oidcFirstNonEmpty(strClaim(info, "name"), strClaim(info, "en_name"))
	out.Email = oidcFirstNonEmpty(strClaim(info, "email"), strClaim(info, "enterprise_email"))
	out.UsernameHint = oidcFirstNonEmpty(strClaim(info, "user_id"), out.Email, out.DisplayName)
	return out, nil
}

func feishuAppAccessToken(appID, secret string) (string, error) {
	body, _ := json.Marshal(map[string]string{"app_id": appID, "app_secret": secret})
	req, err := http.NewRequest(http.MethodPost, "https://open.feishu.cn/open-apis/auth/v3/app_access_token/internal",
		strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// 这里原来是 http.Post，走的是 http.DefaultClient——**没有超时**。
	// 登录回调恰好是最不能挂住的路径：飞书那端不回包，这条请求就一直占着一个
	// 处理协程，同一时间的登录会跟着一起卡。与本文件其它出站调用取齐。
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var j struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		AppAccessToken    string `json:"app_access_token"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return "", err
	}
	if j.Code != 0 {
		return "", fmt.Errorf("feishu app token: %s", j.Msg)
	}
	tok := j.AppAccessToken
	if tok == "" {
		tok = j.TenantAccessToken
	}
	if tok == "" {
		return "", fmt.Errorf("feishu: empty app_access_token")
	}
	return tok, nil
}

func feishuUserAccessToken(appTok, code string) (string, error) {
	body, _ := json.Marshal(map[string]string{"grant_type": "authorization_code", "code": code})
	req, err := http.NewRequest(http.MethodPost, "https://open.feishu.cn/open-apis/authen/v1/access_token",
		strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+appTok)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var j struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return "", err
	}
	if j.Code != 0 || j.Data.AccessToken == "" {
		return "", fmt.Errorf("feishu user token: %s", j.Msg)
	}
	return j.Data.AccessToken, nil
}

func feishuUserInfo(userTok string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, "https://open.feishu.cn/open-apis/authen/v1/user_info", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+userTok)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var j struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, err
	}
	if j.Code != 0 || j.Data == nil {
		return nil, fmt.Errorf("feishu user info: %s", j.Msg)
	}
	return j.Data, nil
}

func dingtalkSSOProfile(cfg SSOProviderConfig, code string) (ssoProfile, error) {
	var out ssoProfile
	out.Provider = ssoProviderDingtalk
	body, _ := json.Marshal(map[string]string{
		"clientId": cfg.AppID, "clientSecret": cfg.AppSecret,
		"code": code, "grantType": "authorization_code",
	})
	req, err := http.NewRequest(http.MethodPost, "https://api.dingtalk.com/v1.0/oauth2/userAccessToken",
		strings.NewReader(string(body)))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tok struct {
		AccessToken string `json:"accessToken"`
		OpenID      string `json:"openId"`
		UnionID     string `json:"unionId"`
		Code        string `json:"code"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return out, err
	}
	if tok.AccessToken == "" {
		return out, fmt.Errorf("dingtalk token: %s %s", tok.Code, tok.Message)
	}
	ureq, err := http.NewRequest(http.MethodGet, "https://api.dingtalk.com/v1.0/contact/users/me", nil)
	if err != nil {
		return out, err
	}
	ureq.Header.Set("x-acs-dingtalk-access-token", tok.AccessToken)
	uresp, err := client.Do(ureq)
	if err != nil {
		return out, err
	}
	defer uresp.Body.Close()
	uraw, _ := io.ReadAll(io.LimitReader(uresp.Body, 1<<20))
	var u struct {
		Nick      string `json:"nick"`
		OpenID    string `json:"openId"`
		UnionID   string `json:"unionId"`
		Email     string `json:"email"`
		Mobile    string `json:"mobile"`
		StateCode string `json:"stateCode"`
	}
	_ = json.Unmarshal(uraw, &u)
	out.Subject = oidcFirstNonEmpty(u.UnionID, tok.UnionID, u.OpenID, tok.OpenID)
	if out.Subject == "" {
		return out, fmt.Errorf("dingtalk: no openId")
	}
	out.DisplayName = u.Nick
	out.Email = u.Email
	out.UsernameHint = oidcFirstNonEmpty(u.Nick, u.Email, out.Subject)
	return out, nil
}

func wechatSSOProfile(cfg SSOProviderConfig, code string) (ssoProfile, error) {
	var out ssoProfile
	out.Provider = ssoProviderWechat
	q := url.Values{}
	q.Set("appid", cfg.AppID)
	q.Set("secret", cfg.AppSecret)
	q.Set("code", code)
	q.Set("grant_type", "authorization_code")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get("https://api.weixin.qq.com/sns/oauth2/access_token?" + q.Encode())
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tok struct {
		AccessToken string `json:"access_token"`
		OpenID      string `json:"openid"`
		UnionID     string `json:"unionid"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return out, err
	}
	if tok.AccessToken == "" || tok.OpenID == "" {
		return out, fmt.Errorf("wechat token: %d %s", tok.ErrCode, tok.ErrMsg)
	}
	uq := url.Values{}
	uq.Set("access_token", tok.AccessToken)
	uq.Set("openid", tok.OpenID)
	uresp, err := client.Get("https://api.weixin.qq.com/sns/userinfo?" + uq.Encode())
	if err != nil {
		return out, err
	}
	defer uresp.Body.Close()
	uraw, _ := io.ReadAll(io.LimitReader(uresp.Body, 1<<20))
	var u struct {
		OpenID   string `json:"openid"`
		Nickname string `json:"nickname"`
		UnionID  string `json:"unionid"`
		ErrCode  int    `json:"errcode"`
		ErrMsg   string `json:"errmsg"`
	}
	_ = json.Unmarshal(uraw, &u)
	out.Subject = oidcFirstNonEmpty(u.UnionID, tok.UnionID, u.OpenID, tok.OpenID)
	if out.Subject == "" {
		return out, fmt.Errorf("wechat: no openid")
	}
	out.DisplayName = u.Nickname
	out.UsernameHint = oidcFirstNonEmpty(u.Nickname, "wx_"+out.Subject)
	return out, nil
}

func wecomSSOProfile(cfg SSOProviderConfig, code string) (ssoProfile, error) {
	var out ssoProfile
	out.Provider = ssoProviderWecom
	client := &http.Client{Timeout: 15 * time.Second}
	tq := url.Values{}
	tq.Set("corpid", cfg.AppID)
	tq.Set("corpsecret", cfg.AppSecret)
	tresp, err := client.Get("https://qyapi.weixin.qq.com/cgi-bin/gettoken?" + tq.Encode())
	if err != nil {
		return out, err
	}
	defer tresp.Body.Close()
	traw, _ := io.ReadAll(io.LimitReader(tresp.Body, 1<<20))
	var tok struct {
		AccessToken string `json:"access_token"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := json.Unmarshal(traw, &tok); err != nil {
		return out, err
	}
	if tok.AccessToken == "" {
		return out, fmt.Errorf("wecom token: %d %s", tok.ErrCode, tok.ErrMsg)
	}
	uq := url.Values{}
	uq.Set("access_token", tok.AccessToken)
	uq.Set("code", code)
	uresp, err := client.Get("https://qyapi.weixin.qq.com/cgi-bin/user/getuserinfo?" + uq.Encode())
	if err != nil {
		return out, err
	}
	defer uresp.Body.Close()
	uraw, _ := io.ReadAll(io.LimitReader(uresp.Body, 1<<20))
	var u struct {
		UserID   string `json:"UserId"`
		OpenID   string `json:"OpenId"`
		DeviceID string `json:"DeviceId"`
		ErrCode  int    `json:"errcode"`
		ErrMsg   string `json:"errmsg"`
	}
	if err := json.Unmarshal(uraw, &u); err != nil {
		return out, err
	}
	out.Subject = oidcFirstNonEmpty(u.UserID, u.OpenID)
	if out.Subject == "" {
		return out, fmt.Errorf("wecom userinfo: %d %s", u.ErrCode, u.ErrMsg)
	}
	out.UsernameHint = out.Subject
	out.DisplayName = out.Subject
	// Best-effort detail for enterprise members (UserId present).
	if u.UserID != "" {
		dq := url.Values{}
		dq.Set("access_token", tok.AccessToken)
		dq.Set("userid", u.UserID)
		dresp, err := client.Get("https://qyapi.weixin.qq.com/cgi-bin/user/get?" + dq.Encode())
		if err == nil {
			defer dresp.Body.Close()
			draw, _ := io.ReadAll(io.LimitReader(dresp.Body, 1<<20))
			var detail struct {
				Name    string `json:"name"`
				Email   string `json:"email"`
				BizMail string `json:"biz_mail"`
				ErrCode int    `json:"errcode"`
			}
			if json.Unmarshal(draw, &detail) == nil && detail.ErrCode == 0 {
				out.DisplayName = oidcFirstNonEmpty(detail.Name, out.DisplayName)
				out.Email = oidcFirstNonEmpty(detail.Email, detail.BizMail)
				out.UsernameHint = oidcFirstNonEmpty(detail.Name, u.UserID)
			}
		}
	}
	return out, nil
}
