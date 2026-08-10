package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// -----------------------------------------------------------------------
// Account recovery: username recovery + password reset.
// These endpoints are PUBLIC (no session) — gated by email verification
// codes + optional MFA (TOTP) as a second factor.
//
// Flow:
//  1. POST /account/recover-send-code   — email → send 6-digit code
//  2. POST /account/recover-verify      — email + code → if MFA: {mfa_required:true}
//     else → {username} or {reset_token}
//  3. POST /account/recover-verify-mfa  — email + code + totp → final result
//  4. POST /account/reset-password      — reset_token + new_password
// -----------------------------------------------------------------------

// handleRecoverSendCode sends a 6-digit verification code to the given email
// address. The purpose distinguishes username recovery from password reset.
// The response is always the same regardless of whether the email exists, to
// prevent email enumeration.
func (s *Server) handleRecoverSendCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email   string `json:"email"`
		Purpose string `json:"purpose"` // "recover_username" | "recover_password"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if !validEmail(req.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.invalid_email")})
		return
	}
	if req.Purpose != "recover_username" && req.Purpose != "recover_password" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.invalid_purpose")})
		return
	}
	// Check SMTP availability BEFORE the user lookup so this response is identical
	// whether or not the address maps to a real account (removes the enumeration
	// oracle where a found user returned no_smtp but a missing one returned code_sent).
	cfg := s.cfg.Get()
	if !cfg.SMTP.Enabled || cfg.SMTP.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.no_smtp")})
		return
	}
	// Prevent email enumeration: a non-existent address gets the same "code sent".
	user, found := s.cfg.UserByEmail(req.Email)
	if !found || user.Email == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.code_sent")})
		return
	}
	code, err := s.emailMgr.issueCode(user.Email, req.Purpose)
	if err != nil {
		// Rate-limited: keep the response identical to the success/not-found path so
		// it can't be used to probe for accounts; record the reason server-side.
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.clientIP(r), Message: Tz("log.reset_code_failed", err.Error())})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.code_sent")})
		return
	}
	// Build email content based on purpose
	var title, body string
	if req.Purpose == "recover_username" {
		title = Tz("recovery.email_username_code_subject")
		body = fmt.Sprintf(Tz("recovery.email_username_code_body"), code)
	} else {
		title = Tz("recovery.email_reset_subject")
		body = fmt.Sprintf(Tz("recovery.email_reset_body"), code)
	}
	html := fmt.Sprintf(`<div style="font-family:sans-serif;padding:20px;max-width:500px;margin:0 auto">
  <h2>%s</h2>
  <p>%s</p>
  <p>%s</p>
  <p style="color:#888;font-size:13px">%s</p>
  <p style="color:#888;font-size:13px">%s</p>
</div>`, title, body, Tz("recovery.email_reset_validity"), Tz("recovery.email_reset_disclaimer"), fmt.Sprintf(Tz("recovery.email_time"), time.Now().Format("2006-01-02 15:04:05")))
	if err := sendEmail(cfg.SMTP, user.Email, title, html); err != nil {
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.clientIP(r), Message: Tz("log.reset_code_failed", err.Error())})
		// Same generic response even on send failure — don't reveal the address exists.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.code_sent")})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.clientIP(r), Message: Tz("log.reset_code_sent")})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.code_sent")})
}

// handleRecoverVerify checks the email verification code. If MFA (TOTP) is
// enabled on the account, it returns mfa_required=true without consuming the
// code — the caller must then call recover-verify-mfa with the TOTP code.
// Otherwise it returns the username (for username recovery) or a reset token
// (for password reset) directly.
func (s *Server) handleRecoverVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email   string `json:"email"`
		Code    string `json:"code"`
		Purpose string `json:"purpose"` // "recover_username" | "recover_password"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	req.Code = strings.TrimSpace(req.Code)
	if !validEmail(req.Email) || len(req.Code) != 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.invalid_params")})
		return
	}
	if req.Purpose != "recover_username" && req.Purpose != "recover_password" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.invalid_purpose")})
		return
	}
	user, found := s.cfg.UserByEmail(req.Email)
	if !found {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.code_invalid")})
		return
	}
	// Mark the code as verified (email step passed) without consuming it.
	// This allows the MFA second-factor step to consume it later if needed.
	if verifiedEmail := s.emailMgr.markCodeVerified(user.Email, req.Purpose, req.Code); verifiedEmail == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.code_invalid")})
		return
	}
	// If MFA is enabled, require TOTP as second factor
	if user.MFAEnabled {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           true,
			"mfa_required": true,
		})
		return
	}
	// No MFA — email verification is sufficient. Consume the code and return
	// result only on a successful one-shot consume (same gate as MFA path).
	// Ignoring a failed consume previously let concurrent recover-verify calls
	// mint multiple reset_tokens from a single email OTP.
	if consumedEmail := s.emailMgr.consumeVerifiedCode(user.Email, req.Purpose, req.Code); consumedEmail == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.code_invalid")})
		return
	}
	s.finalizeRecovery(w, r, user, req.Purpose)
}

// handleRecoverVerifyMFA completes the second-factor TOTP verification for
// accounts that have MFA enabled. The email code must have been previously
// verified via handleRecoverVerify.
func (s *Server) handleRecoverVerifyMFA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Code     string `json:"code"`
		TOTPCode string `json:"totp_code"`
		Purpose  string `json:"purpose"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	req.Code = strings.TrimSpace(req.Code)
	req.TOTPCode = strings.TrimSpace(req.TOTPCode)
	if !validEmail(req.Email) || len(req.Code) != 6 || len(normalizeTOTPCode(req.TOTPCode)) != 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.invalid_params")})
		return
	}
	if req.Purpose != "recover_username" && req.Purpose != "recover_password" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.invalid_purpose")})
		return
	}
	user, found := s.cfg.UserByEmail(req.Email)
	if !found || !user.MFAEnabled || user.MFASecret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.mfa_not_configured")})
		return
	}
	// Verify TOTP without consuming first — email code must succeed too, otherwise
	// a mistyped email code would burn a still-valid authenticator digit.
	totpRes, totpStep := s.auth.checkTOTP(user.Username, user.MFASecret, req.TOTPCode)
	if totpRes != totpOK {
		if totpRes == totpInvalid {
			s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.clientIP(r), Message: Tz("log.totp_recovery_failed", user.Username)})
		}
		s.writeTOTPFailure(w, r, totpRes, http.StatusUnauthorized)
		return
	}
	// Consume the previously-verified email code, then mark the TOTP step used.
	if consumedEmail := s.emailMgr.consumeVerifiedCode(user.Email, req.Purpose, req.Code); consumedEmail == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.code_invalid")})
		return
	}
	if res := s.auth.consumeTOTPStep(user.Username, totpStep); res != totpOK {
		s.writeTOTPFailure(w, r, res, http.StatusUnauthorized)
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.clientIP(r), Message: Tz("log.totp_recovery_success", user.Username)})
	s.finalizeRecovery(w, r, user, req.Purpose)
}

// finalizeRecovery returns the final recovery result: username for username
// recovery, or a reset token for password reset.
func (s *Server) finalizeRecovery(w http.ResponseWriter, r *http.Request, user AccountConfig, purpose string) {
	if purpose == "recover_username" {
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.clientIP(r), Message: Tz("log.username_recovered", user.Username)})
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"username": user.Username,
		})
		return
	}
	// purpose == "recover_password": issue a one-time reset token
	tok := s.emailMgr.issueResetToken(user.Username, user.Email)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.clientIP(r), Message: Tz("log.reset_token_issued", user.Username)})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"reset_token": tok,
	})
}

// handleResetPassword resets the account password after verifying the email
// code OR a one-time reset token issued by the recovery flow.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username   string `json:"username"`
		Email      string `json:"email"`
		Code       string `json:"code"`
		NewPass    string `json:"new_password"`
		ResetToken string `json:"reset_token"`
		TOTPCode   string `json:"totp_code"` // v5.4.1: MFA for legacy path
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}

	// Path A: one-time reset token (issued by the new recovery flow)
	if strings.TrimSpace(req.ResetToken) != "" {
		username, _, ok := s.emailMgr.consumeResetToken(req.ResetToken)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.token_invalid")})
			return
		}
		if !validatePasswordStrength(strings.TrimSpace(req.NewPass)) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.password_policy")})
			return
		}
		_ = s.cfg.SetUserPassword(username, req.NewPass)
		s.auth.clearUserSessions(username)
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.clientIP(r), Message: Tz("log.reset_password", username)})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.password_reset")})
		return
	}

	// Path B: legacy flow (username + email + code)
	// v5.4.1: MFA verification is now required when the account has MFA enabled.
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)
	req.Code = strings.TrimSpace(req.Code)
	if req.Username == "" || !validEmail(req.Email) || len(req.Code) != 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.invalid_params")})
		return
	}
	if !validatePasswordStrength(strings.TrimSpace(req.NewPass)) {
		// Enforce the full password policy here too (was a weak ≥4-char check),
		// matching the token-based reset path and normal password changes.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.password_policy")})
		return
	}
	user, found := s.cfg.UserByName(req.Username)
	if !found || !strings.EqualFold(req.Email, user.Email) || user.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.user_email_mismatch")})
		return
	}
	if !s.emailMgr.verifyCode(user.Email, "reset_password", req.Code) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.code_invalid")})
		return
	}
	// v5.4.1: require MFA TOTP when the account has MFA enabled
	if user.MFAEnabled && user.MFASecret != "" {
		totpCode := strings.TrimSpace(req.TOTPCode)
		if totpCode == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.mfa_required_for_reset")})
			return
		}
		if res := s.auth.verifyAndConsumeTOTP(user.Username, user.MFASecret, totpCode); res != totpOK {
			if res == totpInvalid {
				s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.clientIP(r), Message: Tz("log.totp_recovery_failed", user.Username)})
			}
			s.writeTOTPFailure(w, r, res, http.StatusUnauthorized)
			return
		}
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.clientIP(r), Message: Tz("log.totp_recovery_success", user.Username)})
	}
	_ = s.cfg.SetUserPassword(user.Username, req.NewPass)
	s.auth.clearUserSessions(user.Username)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.clientIP(r), Message: Tz("log.reset_password", user.Username)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.password_reset")})
}

// handleRecoverUsername is kept for backward compatibility but the new flow
// (recover-send-code → recover-verify → optionally recover-verify-mfa) is the
// preferred path with full email + optional MFA verification.
func (s *Server) handleRecoverUsername(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if !validEmail(req.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.invalid_email")})
		return
	}
	if user, found := s.cfg.UserByEmail(req.Email); found {
		cfg := s.cfg.Get()
		if cfg.SMTP.Enabled && cfg.SMTP.Host != "" {
			html := fmt.Sprintf(`<div style="font-family:sans-serif;padding:20px;max-width:500px;margin:0 auto">
  <h2>%s</h2>
  <p>%s</p>
  <p style="color:#888;font-size:13px">%s</p>
  <p style="color:#888;font-size:13px">%s</p>
</div>`, Tz("recovery.email_username_subject"), fmt.Sprintf(Tz("recovery.email_username_body"), user.Username), Tz("recovery.email_disclaimer"), fmt.Sprintf(Tz("recovery.email_time"), time.Now().Format("2006-01-02 15:04:05")))
			if err := sendEmail(cfg.SMTP, req.Email, Tz("recovery.email_username_subject"), html); err != nil {
				s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.clientIP(r), Message: Tz("log.username_email_failed", err.Error())})
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": Tr(r, "recovery.email_send_failed")})
				return
			}
			s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.clientIP(r), Message: Tz("log.username_email_sent")})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.username_sent")})
}

// handleSendResetCode is kept for backward compatibility. New clients should
// use handleRecoverSendCode with purpose="recover_password".
func (s *Server) handleSendResetCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.username_required")})
		return
	}
	user, found := s.cfg.UserByName(req.Username)
	if !found || user.Email == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.code_sent")})
		return
	}
	cfg := s.cfg.Get()
	if !cfg.SMTP.Enabled || cfg.SMTP.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.no_smtp")})
		return
	}
	code, err := s.emailMgr.issueCode(user.Email, "reset_password")
	if err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}
	html := fmt.Sprintf(`<div style="font-family:sans-serif;padding:20px;max-width:500px;margin:0 auto">
  <h2>%s</h2>
  <p>%s</p>
  <p>%s</p>
  <p style="color:#888;font-size:13px">%s</p>
  <p style="color:#888;font-size:13px">%s</p>
</div>`, Tz("recovery.email_reset_subject"), fmt.Sprintf(Tz("recovery.email_reset_body"), code), Tz("recovery.email_reset_validity"), Tz("recovery.email_reset_disclaimer"), fmt.Sprintf(Tz("recovery.email_time"), time.Now().Format("2006-01-02 15:04:05")))
	if err := sendEmail(cfg.SMTP, user.Email, Tz("recovery.email_reset_subject"), html); err != nil {
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.clientIP(r), Message: Tz("log.reset_code_failed", err.Error())})
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": Tr(r, "recovery.email_send_failed")})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.clientIP(r), Message: Tz("log.reset_code_sent")})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.code_sent")})
}

// handleMFAUnbindViaEmail disables MFA after verifying a code sent to the bound
// email. This is the recovery path when the operator lost their authenticator.
// Requires an active session (logged-in user unbinding their own MFA).
func (s *Server) handleMFAUnbindViaEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"` // "send_code" | "verify"
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	req.Action = strings.TrimSpace(req.Action)
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	if acc.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.no_email_bound")})
		return
	}
	if !acc.MFAEnabled {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.mfa_not_enabled")})
		return
	}
	if req.Action == "send_code" {
		cfg := s.cfg.Get()
		if !cfg.SMTP.Enabled || cfg.SMTP.Host == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.no_smtp_short")})
			return
		}
		code, err := s.emailMgr.issueCode(acc.Email, "mfa_unbind")
		if err != nil {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
		html := fmt.Sprintf(`<div style="font-family:sans-serif;padding:20px;max-width:500px;margin:0 auto">
  <h2>%s</h2>
  <p>%s</p>
  <p>%s</p>
  <p>%s</p>
  <p style="color:#888;font-size:13px">%s</p>
</div>`, Tz("recovery.email_mfa_title"), Tz("recovery.email_mfa_desc"), fmt.Sprintf(Tz("recovery.email_mfa_code"), code), Tz("recovery.email_mfa_validity"), Tz("recovery.email_mfa_disclaimer"))
		if err := sendEmail(cfg.SMTP, acc.Email, Tz("recovery.email_mfa_subject"), html); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": Tr(r, "recovery.email_send_error", err.Error())})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.code_sent_ok")})
		return
	}
	if req.Action == "verify" {
		req.Code = strings.TrimSpace(req.Code)
		if len(req.Code) != 6 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.code_required")})
			return
		}
		if !s.emailMgr.verifyCode(acc.Email, "mfa_unbind", req.Code) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.code_invalid")})
			return
		}
		_ = s.cfg.SetUserMFA(acc.Username, false, "")
		s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.clientIP(r), Message: Tz("log.mfa_unbind", acc.Username)})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": Tr(r, "recovery.mfa_disabled")})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "recovery.unknown_action")})
}
