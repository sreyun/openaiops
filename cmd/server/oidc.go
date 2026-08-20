package main

import (
	"crypto/rand"
	"encoding/base64"
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

// OIDCConfig enables enterprise IdP login (OIDC authorization code flow).
type OIDCConfig struct {
	Enabled      bool   `json:"enabled"`
	Issuer       string `json:"issuer,omitempty"` // e.g. https://login.example.com/realms/ops
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"` // never echoed to browser
	RedirectURL  string `json:"redirect_url,omitempty"`  // optional; default {PublicURL}/api/v1/auth/oidc/callback
	Scopes       string `json:"scopes,omitempty"`        // default openid profile email groups
	GroupClaim   string `json:"group_claim,omitempty"`   // default groups
	// GroupRoleMap maps IdP group name → admin|operator|viewer. First match wins.
	GroupRoleMap map[string]string `json:"group_role_map,omitempty"`
	DefaultRole  string            `json:"default_role,omitempty"` // when no group matches; empty = deny
	AutoCreate   bool              `json:"auto_create,omitempty"`  // create local user on first login
}

type oidcStateEntry struct {
	Nonce     string
	BindUser  string // non-empty = bind identity to this logged-in user (not login)
	ExpiresAt int64
}

var (
	oidcStates   = map[string]oidcStateEntry{}
	oidcStatesMu sync.Mutex
)

func (cs *ConfigStore) OIDCConfig() OIDCConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.cfg.OIDC
}

func (cs *ConfigStore) SetOIDCConfig(c OIDCConfig) error {
	cs.mu.Lock()
	if c.ClientSecret == "" || strings.Contains(c.ClientSecret, "****") {
		c.ClientSecret = cs.cfg.OIDC.ClientSecret
	}
	if c.DefaultRole != "" && !validRole(c.DefaultRole) {
		c.DefaultRole = RoleViewer
	}
	cs.cfg.OIDC = c
	cs.mu.Unlock()
	return cs.save()
}

func (s *Server) handleGetOIDCConfig(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.OIDCConfig()
	if c.ClientSecret != "" {
		c.ClientSecret = "****"
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleSetOIDCConfig(w http.ResponseWriter, r *http.Request) {
	var c OIDCConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.SetOIDCConfig(c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: "更新 OIDC SSO 配置"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleOIDCLoginInfo is public: login page asks whether SSO button should show.
func (s *Server) handleOIDCLoginInfo(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.OIDCConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": c.Enabled && strings.TrimSpace(c.Issuer) != "" && strings.TrimSpace(c.ClientID) != "",
	})
}

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.OIDCConfig()
	if !c.Enabled {
		s.redirectSSOError(w, r, ssoProviderOIDC, "disabled")
		return
	}
	bindUser, okBind := s.ssoBindUsername(r)
	if r.URL.Query().Get("bind") == "1" && !okBind {
		s.redirectSSOError(w, r, ssoProviderOIDC, "bind_auth")
		return
	}
	disc, err := fetchOIDCDiscovery(c.Issuer)
	if err != nil {
		slog.Warn("OIDC discovery failed", "err", err)
		s.redirectSSOError(w, r, ssoProviderOIDC, "discovery")
		return
	}
	state := randomOIDCToken(24)
	nonce := randomOIDCToken(16)
	oidcStatesMu.Lock()
	pruneExpiredUnixMap(oidcStates, time.Now().Unix(), maxOIDCStates, func(e oidcStateEntry) int64 { return e.ExpiresAt })
	oidcStates[state] = oidcStateEntry{Nonce: nonce, BindUser: bindUser, ExpiresAt: time.Now().Add(10 * time.Minute).Unix()}
	oidcStatesMu.Unlock()

	scopes := strings.TrimSpace(c.Scopes)
	if scopes == "" {
		scopes = "openid profile email groups"
	}
	redirect := s.oidcRedirectURL(r, c)
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", scopes)
	q.Set("state", state)
	q.Set("nonce", nonce)
	http.Redirect(w, r, disc.AuthorizationEndpoint+"?"+q.Encode(), http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.OIDCConfig()
	if !c.Enabled {
		s.redirectSSOError(w, r, ssoProviderOIDC, "disabled")
		return
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		code := "denied"
		if !strings.EqualFold(errMsg, "access_denied") {
			code = "idp"
		}
		s.redirectSSOError(w, r, ssoProviderOIDC, code)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	oidcStatesMu.Lock()
	st, ok := oidcStates[state]
	delete(oidcStates, state)
	oidcStatesMu.Unlock()
	if !ok || time.Now().Unix() > st.ExpiresAt || code == "" {
		s.redirectSSOError(w, r, ssoProviderOIDC, "state")
		return
	}

	disc, err := fetchOIDCDiscovery(c.Issuer)
	if err != nil {
		s.redirectSSOError(w, r, ssoProviderOIDC, "discovery")
		return
	}
	oauthTok, err := exchangeOIDCCode(disc.TokenEndpoint, c, s.oidcRedirectURL(r, c), code)
	if err != nil {
		slog.Warn("OIDC token exchange failed", "err", err)
		s.redirectSSOError(w, r, ssoProviderOIDC, "exchange")
		return
	}
	if strings.TrimSpace(oauthTok.IDToken) == "" {
		s.redirectSSOError(w, r, ssoProviderOIDC, "id_token")
		return
	}
	if strings.TrimSpace(disc.JWKSURI) == "" {
		s.redirectSSOError(w, r, ssoProviderOIDC, "discovery")
		return
	}
	claims, err := validateOIDCIDToken(oauthTok.IDToken, c.Issuer, c.ClientID, st.Nonce, disc.JWKSURI)
	if err != nil {
		slog.Warn("OIDC id_token validation failed", "err", err)
		s.redirectSSOError(w, r, ssoProviderOIDC, "id_token")
		return
	}
	info, err := fetchOIDCUserInfo(disc.UserinfoEndpoint, oauthTok.AccessToken)
	if err != nil {
		slog.Warn("OIDC userinfo failed; using id_token claims", "err", err)
		info = map[string]any{}
	}
	if strClaim(info, "sub") == "" {
		info["sub"] = claims.Sub
	}
	if strClaim(info, "email") == "" && claims.Email != "" {
		info["email"] = claims.Email
	}
	if strClaim(info, "name") == "" && claims.Name != "" {
		info["name"] = claims.Name
	}
	if strClaim(info, "preferred_username") == "" && claims.PreferredUsername != "" {
		info["preferred_username"] = claims.PreferredUsername
	}
	sub := claims.Sub
	if sub == "" {
		s.redirectSSOError(w, r, ssoProviderOIDC, "bad_profile")
		return
	}
	if st.BindUser != "" {
		if err := s.cfg.BindUserIdentity(st.BindUser, ssoProviderOIDC, sub); err != nil {
			s.redirectSSOError(w, r, ssoProviderOIDC, ssoProvisionErrorCode(err))
			return
		}
		s.redirectSSOBound(w, r, ssoProviderOIDC)
		return
	}
	role := mapOIDCGroupsToRole(info, c)
	if role == "" {
		s.redirectSSOError(w, r, ssoProviderOIDC, "no_role")
		return
	}
	hint := oidcFirstNonEmpty(strClaim(info, "preferred_username"), strClaim(info, "email"), sub)
	display := oidcFirstNonEmpty(strClaim(info, "name"), strClaim(info, "preferred_username"))
	email := strClaim(info, "email")
	username, err := s.provisionSSOUser(ssoProvisionReq{
		Provider: ssoProviderOIDC, Subject: sub, UsernameHint: hint,
		DisplayName: display, Email: email, Role: role, AutoCreate: c.AutoCreate,
	})
	if err != nil {
		s.redirectSSOError(w, r, ssoProviderOIDC, ssoProvisionErrorCode(err))
		return
	}
	s.finishSSOLogin(w, r, username, ssoProviderOIDC)
}

func (s *Server) oidcRedirectURL(r *http.Request, c OIDCConfig) string {
	if u := strings.TrimSpace(c.RedirectURL); u != "" {
		return u
	}
	base := strings.TrimRight(s.serverURL(r), "/")
	return base + "/api/v1/auth/oidc/callback"
}

type oidcTokenResp struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
}

func exchangeOIDCCode(tokenURL string, c OIDCConfig, redirect, code string) (oidcTokenResp, error) {
	var out oidcTokenResp
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return out, fmt.Errorf("token status %d: %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	if out.AccessToken == "" {
		return out, fmt.Errorf("no access_token")
	}
	return out, nil
}

func fetchOIDCUserInfo(endpoint, accessToken string) (map[string]any, error) {
	out := map[string]any{}
	if endpoint == "" {
		return out, fmt.Errorf("no userinfo endpoint")
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out, fmt.Errorf("userinfo status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

func mapOIDCGroupsToRole(info map[string]any, c OIDCConfig) string {
	claim := c.GroupClaim
	if claim == "" {
		claim = "groups"
	}
	groups := stringSliceClaim(info, claim)
	for _, g := range groups {
		if role, ok := c.GroupRoleMap[g]; ok && validRole(role) {
			return role
		}
	}
	if validRole(c.DefaultRole) {
		return c.DefaultRole
	}
	return ""
}

func strClaim(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func stringSliceClaim(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		parts := strings.Split(t, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func oidcFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func randomOIDCToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
