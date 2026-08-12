package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// handleGetConfig returns the alert config with webhooks/secrets masked.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	c.Categories = nil
	c.Feishu.Webhook = maskSecret(c.Feishu.Webhook)
	c.Dingtalk.Webhook = maskSecret(c.Dingtalk.Webhook)
	c.Dingtalk.Secret = maskSecret(c.Dingtalk.Secret)
	c.CustomWebhook.URL = maskSecret(c.CustomWebhook.URL)
	c.SMTP.Password = maskSecret(c.SMTP.Password)
	c.SMS.SecretKey = maskSecret(c.SMS.SecretKey)
	c.SMS.AccessKey = maskSecret(c.SMS.AccessKey)
	c.VoiceCall.SecretKey = maskSecret(c.VoiceCall.SecretKey)
	c.VoiceCall.AccessKey = maskSecret(c.VoiceCall.AccessKey)
	c.AI.APIKey = maskSecret(c.AI.APIKey)                         // AI provider credential
	c.AI.EmbedAPIKey = maskSecret(c.AI.EmbedAPIKey)               // 嵌入服务凭证（独立于对话）
	c.AI.RerankAPIKey = maskSecret(c.AI.RerankAPIKey)
	c.AI.MCPToken = maskSecret(c.AI.MCPToken)
	if strings.TrimSpace(c.AI.MCPClientsJSON) != "" {
		c.AI.MCPClientsJSON = maskMCPClientsJSONForAPI(c.AI.MCPClientsJSON)
	}
	c.AI.WeKnoraAPIKey = maskSecret(c.AI.WeKnoraAPIKey)
	c.PostgresDSN = maskSecret(c.PostgresDSN)                     // DSN carries the PostgreSQL password
	c.InstallToken = maskSecret(c.InstallToken)                   // agent enrollment token — not for viewers
	c.PrevInstallToken = maskSecret(c.PrevInstallToken)           // grace-period token must not leak via config GET
	c.PromWriteToken = maskSecret(c.PromWriteToken)
	c.RelaySecret = maskSecret(c.RelaySecret)                     // gateway relay shared secret
	c.CustomWebhook.Headers = maskSecret(c.CustomWebhook.Headers) // may carry auth tokens (e.g. X-Token)
	c.OIDC.ClientSecret = maskSecret(c.OIDC.ClientSecret)
	c.SSO.Feishu.AppSecret = maskSecret(c.SSO.Feishu.AppSecret)
	c.SSO.Dingtalk.AppSecret = maskSecret(c.SSO.Dingtalk.AppSecret)
	c.SSO.Wechat.AppSecret = maskSecret(c.SSO.Wechat.AppSecret)
	c.SSO.Wecom.AppSecret = maskSecret(c.SSO.Wecom.AppSecret)
	if len(c.DataSources) > 0 { // 数据源 Basic Auth 密码脱敏
		// 复制切片再脱敏——切片底层数组与已存配置共享，就地改会污染真实密码。
		ds := make([]DataSource, len(c.DataSources))
		copy(ds, c.DataSources)
		for i := range ds {
			ds[i].AuthPass = maskSecret(ds[i].AuthPass)
		}
		c.DataSources = ds
	}
	if len(c.MySQLConnections) > 0 {
		conns := make([]MySQLConnection, len(c.MySQLConnections))
		for i, mc := range c.MySQLConnections {
			conns[i] = maskMySQLConnection(mc)
		}
		c.MySQLConnections = conns
	}
	if len(c.K8sClusters) > 0 {
		clusters := make([]K8sClusterConfig, len(c.K8sClusters))
		for i, kc := range c.K8sClusters {
			clusters[i] = maskK8sCluster(kc)
		}
		c.K8sClusters = clusters
	}
	// Never expose the password hash/salt or the MFA secret to the browser.
	c.Account.Salt, c.Account.Hash, c.Account.MFASecret = "", "", ""
	c.Users = nil // the user list (with hashes) is served via /api/v1/users, not here
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var in ServerConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	mergeSecrets(&in, s.cfg.Get())
	if err := s.cfg.Set(in); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// config changed: re-sync alert state so a newly configured webhook
	// immediately receives the currently-outstanding alerts.
	s.notifier.ResetState()
	go s.notifier.Trigger()
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.update_config")})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleThresholdPresets returns the three recommended threshold profiles for one-click apply in UI.
func (s *Server) handleThresholdPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"conservative": thresholdConfigFromThresholds(ConservativeThresholds()),
		"standard":     thresholdConfigFromThresholds(StandardThresholds()),
		"relaxed":      thresholdConfigFromThresholds(RelaxedThresholds()),
	})
}

func (s *Server) handleTestConfig(w http.ResponseWriter, r *http.Request) {
	var in ServerConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	mergeSecrets(&in, s.cfg.Get())
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.test_alert")})
	if errs := s.notifier.SendTest(in); len(errs) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "errors": errs})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleInstallInfo returns the data the panel needs to render one-line install
// commands: the reachable server URL and the current install token.
// The raw install token is admin-only; other roles get a masked placeholder so a
// Viewer session cannot enroll new agents.
func (s *Server) handleInstallInfo(w http.ResponseWriter, r *http.Request) {
	cs := s.cfg
	cs.mu.RLock()
	maxUses := cs.cfg.InstallTokenMaxUses
	useCount := cs.cfg.InstallTokenUseCount
	expiresAt := cs.cfg.InstallTokenExpiresAt
	revoked := cs.cfg.InstallTokenRevoked
	cs.mu.RUnlock()
	tok := s.cfg.InstallToken()
	u, ok := s.currentUser(r)
	if !ok || roleRank(u.Role) < roleRank(RoleAdmin) {
		tok = maskSecret(tok)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server_url":       s.serverURL(r),
		"token":            tok,
		"require_token":    s.cfg.AgentTokenRequired(),
		"max_uses":         maxUses,
		"use_count":        useCount,
		"expires_at":       expiresAt,
		"revoked":          revoked,
		"prev_valid_until": s.cfg.PrevTokenValidUntil(),
	})
}

func (s *Server) handleResetToken(w http.ResponseWriter, r *http.Request) {
	tok := s.cfg.ResetToken()
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: Tz("log.reset_token")})
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

func (s *Server) handleRevokeInstallToken(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.RevokeInstallToken(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r), Message: "吊销安装 Token（已注册 Agent 不受影响）"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetInstallTokenPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxUses   int   `json:"max_uses"`
		ExpiresAt int64 `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.SetInstallTokenPolicy(req.MaxUses, req.ExpiresAt); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r), Message: "更新安装 Token 策略"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleInstallScript serves the platform install script (install.sh /
// install.ps1) with the server URL, token and category injected.
func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	// Do NOT fall back to the real install token when the query param is absent —
	// /install.sh is public, so injecting it would leak the token to anyone who
	// can reach the server. The dashboard always generates the command WITH the
	// token (from the authenticated /install/info), so legitimate installs carry it.
	token := sanitizeToken(r.URL.Query().Get("token"))
	// category & server are echoed into the shell/PowerShell install script inside
	// double quotes; sanitize so a crafted ?category= (or a forged X-Forwarded-Host
	// feeding serverURL) can't inject commands into the script a victim pipes to sh.
	category := sanitizeCategory(r.URL.Query().Get("category"))
	folderID := sanitizeFolderID(r.URL.Query().Get("folder_id"))
	if folderID != "" && folderID != HostFolderUngroupedID {
		folders, _ := s.cfg.hostFoldersSnapshot()
		n := findFolderNode(folders, folderID)
		if n == nil {
			folderID = "" // deleted / unknown — keep category-only legacy path
		} else if category == "" {
			category = sanitizeCategory(n.Name)
		}
	}
	server := sanitizeServerURL(s.serverURL(r))
	// Multi-server: the dashboard may pass a JSON array of {server,token} objects
	// so one agent pushes to multiple backends. Sanitized+re-serialized here so
	// a crafted payload can't inject shell/PowerShell metacharacters.
	serversJSON := sanitizeServersJSON(r.URL.Query().Get("servers_json"))
	// 日志采集路径（可选）：清洗为合法 JSON 数组注入生成的 config.json 的 log_paths
	logPaths := sanitizeLogPaths(r.URL.Query().Get("log_paths"))
	audit := sanitizeAuditInstallOptions(map[string]string{
		"sni_enabled":                      r.URL.Query().Get("sni_enabled"),
		"sni_interface":                    r.URL.Query().Get("sni_interface"),
		"capture_backend":                  r.URL.Query().Get("capture_backend"),
		"content_audit":                    r.URL.Query().Get("content_audit"),
		"content_audit_ports":              r.URL.Query().Get("content_audit_ports"),
		"content_audit_max_body":           r.URL.Query().Get("content_audit_max_body"),
		"content_audit_body_mode":          r.URL.Query().Get("content_audit_body_mode"),
		"content_audit_include_hosts":      r.URL.Query().Get("content_audit_include_hosts"),
		"content_audit_exclude_paths":      r.URL.Query().Get("content_audit_exclude_paths"),
		"content_audit_max_events_per_min": r.URL.Query().Get("content_audit_max_events_per_min"),
	})
	var body string
	if strings.HasSuffix(r.URL.Path, ".ps1") {
		// Windows uses the cross-platform TShark/Npcap backend. Keep explicit
		// native requests safe by normalizing them to auto.
		if audit.CaptureBackend == "native" {
			audit.CaptureBackend = "auto"
		}
		body = renderScriptWithAudit(installPs1Template, server, token, category, folderID, serversJSON, logPaths, audit)
	} else {
		body = renderScriptWithAudit(installShTemplate, server, token, category, folderID, serversJSON, logPaths, audit)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, body)
}

// handleRelayInstallScript serves the gateway relay install script
// (install-relay.sh / install-relay.ps1) — same token/category sanitization as
// the regular install script, but uses the relay templates that configure the
// agent in --relay mode.
func (s *Server) handleRelayInstallScript(w http.ResponseWriter, r *http.Request) {
	token := sanitizeToken(r.URL.Query().Get("token"))
	category := sanitizeCategory(r.URL.Query().Get("category"))
	folderID := sanitizeFolderID(r.URL.Query().Get("folder_id"))
	server := sanitizeServerURL(s.serverURL(r))
	var body string
	if strings.HasSuffix(r.URL.Path, ".ps1") {
		body = renderScript(relayInstallPs1Template, server, token, category, folderID, "", "")
	} else {
		body = renderScript(relayInstallShTemplate, server, token, category, folderID, "", "")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, body)
}

// handleUninstallScript serves the platform uninstall script (uninstall.sh /
// uninstall.ps1). These are static — no server URL / token needed.
func (s *Server) handleUninstallScript(w http.ResponseWriter, r *http.Request) {
	body := uninstallShTemplate
	if strings.HasSuffix(r.URL.Path, ".ps1") {
		body = uninstallPs1Template
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, body)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok", "time_unix": time.Now().Unix(),
	})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// Classic Web UI only — Vue/v2 shell removed; ignore ?ui= query.
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "dashboard not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
