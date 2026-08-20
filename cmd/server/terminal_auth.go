// v5.3.0: Terminal secondary authentication — protocol agreement + password
// The remote terminal is the most powerful feature in the platform (full shell
// access), so it requires a second layer of authentication beyond the login
// session:
//  1. One-time protocol agreement (liability disclaimer)
//  2. Terminal password setup (guardian password, different from login)
//  3. Per-session verification (once per login, cached in session)
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
)

// validateTerminalPassword checks the terminal password strength requirements:
// ≥8 chars, must contain uppercase, lowercase, digit, and special char.
func validateTerminalPassword(pass string) error {
	if len(pass) < 8 {
		return &validationError{key: "terminal_auth.password_too_short"}
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, r := range pass {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			hasSpecial = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit || !hasSpecial {
		return &validationError{key: "terminal_auth.password_weak"}
	}
	return nil
}

type validationError struct {
	key string
}

func (e *validationError) Error() string {
	return Tz(e.key)
}

// handleTerminalPasswordStatus returns whether the current user has a terminal
// password set. GET /api/user/terminal-password/status
func (s *Server) handleTerminalPasswordStatus(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	has := s.cfg.HasTerminalPassword(acc.Username)
	// Also report whether THIS session already passed terminal verification, so a
	// browser refresh (the session server-side still remembers it) doesn't force
	// the user to re-enter the terminal password.
	verified, _ := s.auth.isTerminalVerified(r)
	writeJSON(w, http.StatusOK, map[string]bool{"has_password": has, "verified": verified})
}

// handleTerminalPasswordSet sets or changes the terminal secondary password.
// POST /api/user/terminal-password/set
// When changing (not first-time setup), MFA code or login password is required.
func (s *Server) handleTerminalPasswordSet(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"` // MFA TOTP code (required when changing, if MFA enabled)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := validateTerminalPassword(req.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// If the user already has a terminal password, this is a CHANGE — require
	// second-factor verification: MFA TOTP (if MFA enabled) or login password.
	hasExisting := s.cfg.HasTerminalPassword(acc.Username)
	if hasExisting {
		if acc.MFAEnabled {
			if strings.TrimSpace(req.Code) == "" {
				writeJSON(w, http.StatusOK, map[string]any{"mfa_required": true})
				return
			}
			if res := s.auth.verifyAndConsumeTOTP(acc.Username, acc.MFASecret, req.Code); res != totpOK {
				s.writeTOTPFailure(w, r, res, http.StatusBadRequest)
				return
			}
		} else {
			// No MFA: verify the current login password
			if _, ok2 := s.auth.CheckPassword(acc.Username, req.Code); !ok2 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "auth.wrong_password")})
				return
			}
		}
	}

	if err := s.cfg.SetTerminalPassword(acc.Username, req.Password); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": Tr(r, "auth.save_failed")})
		return
	}

	// After setting the password, automatically mark the session as terminal-verified
	// so the user doesn't need to verify immediately after setting.
	s.auth.markTerminalVerified(r)

	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "warning",
		Message: Tz("log.terminal_password_set", acc.Username)})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTerminalPasswordVerify verifies the terminal secondary password for the
// current session. POST /api/user/terminal-password/verify
func (s *Server) handleTerminalPasswordVerify(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": Tr(r, "auth.unauthorized")})
		return
	}
	if !s.cfg.HasTerminalPassword(acc.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "terminal_auth.not_set")})
		return
	}

	// Rate limiting
	allowed, remaining := s.auth.terminalAttemptAllowed(acc.Username)
	if !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":       Tr(r, "terminal_auth.locked"),
			"locked":      true,
			"retry_after": 300,
		})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}

	if !s.cfg.VerifyTerminalPassword(acc.Username, req.Password) {
		s.auth.terminalAttemptFailed(acc.Username)
		remaining--
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error":     Tr(r, "terminal_auth.wrong_password"),
			"remaining": remaining,
		})
		return
	}

	s.auth.terminalAttemptReset(acc.Username)
	s.auth.markTerminalVerified(r)

	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info",
		Message: Tz("log.terminal_verified", acc.Username)})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
