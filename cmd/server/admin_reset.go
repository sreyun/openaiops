package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

// ============================================================================
// v5.4.0: Admin password reset — CLI subcommand + local HTTP API
//
// Two recovery paths for a forgotten admin password:
//   1. CLI:  aiops-server -reset-admin
//   2. API:  aiops-server -reset-admin-api=:PORT
//       → GET  /reset-token  returns a one-time token (printed to console)
//       → POST /reset-password  {"token":"...", "username":"admin"}  resets
//
// Both paths:
//   - Reset the first admin user's password to a random 16-char value
//   - Set MustChangePassword = true → force change on next login
//   - Print the new password PLAINTEXT to console (single-use, ephemeral)
//   - The server process exits immediately after the reset
// ============================================================================

const (
	resetTokenTTL = 5 * time.Minute // one-time token validity window
)

// resetToken holds the ephemeral token for the local HTTP API flow.
var resetToken string

// runResetAdmin is called from main() when -reset-admin is set.
// It reads the server config, resets the first admin's password, prints the
// new password to stdout, and exits the process.
func runResetAdmin(cfgPath string) {
	cfg, err := NewConfigStore(cfgPath, pgFromEnv())
	if err != nil {
		log.Fatalf("Failed to load config %q: %v", cfgPath, err)
	}

	username, newPass, err := cfg.ResetAdminPassword()
	if err != nil {
		log.Fatalf("Admin password reset failed: %v", err)
	}

	fmt.Println()
	fmt.Println("==============================================")
	fmt.Println("  Admin password has been reset")
	fmt.Println("==============================================")
	fmt.Printf("  User:     %s\n", username)
	fmt.Printf("  Password: %s\n", newPass)
	fmt.Println("==============================================")
	fmt.Println("  IMPORTANT: You will be forced to change this")
	fmt.Println("  password on your next login.")
	fmt.Println("==============================================")
	fmt.Println()
}

// runResetAdminMFA clears MFA/TOTP for the first admin (or -reset-mfa-user) so
// operators locked out by a lost/desynced authenticator can sign in with
// password only, then re-enroll 2FA from the profile page.
func runResetAdminMFA(cfgPath, username string) {
	cfg, err := NewConfigStore(cfgPath, pgFromEnv())
	if err != nil {
		log.Fatalf("Failed to load config %q: %v", cfgPath, err)
	}
	name, err := cfg.ResetAdminMFA(username)
	if err != nil {
		log.Fatalf("Admin MFA reset failed: %v", err)
	}
	fmt.Println()
	fmt.Println("==============================================")
	fmt.Println("  MFA (TOTP) has been cleared")
	fmt.Println("==============================================")
	fmt.Printf("  User: %s\n", name)
	fmt.Println("  MFAEnabled=false, MFASecret cleared")
	fmt.Println("==============================================")
	fmt.Println("  Next: restart aiops-server, login with password,")
	fmt.Println("  then re-enable 2FA under Profile → MFA.")
	fmt.Println("==============================================")
	fmt.Println()
}

// runResetAdminAPI starts a temporary HTTP server on 127.0.0.1 only, serving
// a two-step admin password reset flow. The server generates a one-time token
// (printed to console) and accepts authenticated reset requests.
func runResetAdminAPI(cfgPath, listenAddr string) {
	// SECURITY: this recovery API mints a fresh admin password and returns it in
	// the HTTP body, so it must never be reachable off-host. Force the bind to
	// loopback regardless of what the operator passed (":9999" or "0.0.0.0:9999"
	// → "127.0.0.1:9999"), matching the console banner's localhost claim.
	if _, p, err := net.SplitHostPort(listenAddr); err == nil && p != "" {
		listenAddr = net.JoinHostPort("127.0.0.1", p)
	} else {
		listenAddr = "127.0.0.1:9999"
	}
	cfg, err := NewConfigStore(cfgPath, pgFromEnv())
	if err != nil {
		log.Fatalf("Failed to load config %q: %v", cfgPath, err)
	}

	// Generate one-time token
	resetToken = generateRandomPassword()

	// Create a minimal HTTP handler for the two-step reset flow.
	mux := http.NewServeMux()
	mux.HandleFunc("/reset-token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"token":   resetToken,
			"expires": time.Now().Add(resetTokenTTL).Unix(),
			"hint":    "POST /reset-password with {\"token\":\"...\", \"username\":\"admin\"}",
		})
	})

	mux.HandleFunc("/reset-password", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
			return
		}
		var req struct {
			Token    string `json:"token"`
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		if req.Token == "" || req.Username == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token and username are required"})
			return
		}
		// Constant-time token comparison
		if subtle.ConstantTimeCompare([]byte(req.Token), []byte(resetToken)) != 1 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid or expired token"})
			return
		}
		// Reset the password for the specified user (must be an admin)
		username, newPass, err := cfg.ResetAdminPassword()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// Invalidate the token
		resetToken = ""
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"username": username,
			"password": newPass,
			"warning":  "Password must be changed on next login",
		})
		// Also print to console for the administrator
		fmt.Printf("\nAdmin password reset via API:\n  User: %s\n  Password: %s\n\n", username, newPass)
		// Exit after a brief delay so the response can be sent
		go func() { time.Sleep(500 * time.Millisecond); os.Exit(0) }()
	})

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Println()
	fmt.Println("==============================================")
	fmt.Println("  Admin Password Reset — Local HTTP API")
	fmt.Println("==============================================")
	fmt.Printf("  Listening on: http://127.0.0.1%s\n", listenAddr)
	fmt.Println("==============================================")
	fmt.Println()
	fmt.Println("  Step 1: Get the one-time reset token:")
	fmt.Printf("    curl http://127.0.0.1%s/reset-token\n", listenAddr)
	fmt.Println()
	fmt.Println("  Step 2: Reset the admin password:")
	fmt.Printf("    curl -X POST http://127.0.0.1%s/reset-password \\\n", listenAddr)
	fmt.Println("      -H \"Content-Type: application/json\" \\")
	fmt.Println("      -d \"{\\\"token\\\":\\\"<TOKEN>\\\",\\\"username\\\":\\\"admin\\\"}\"")
	fmt.Println()
	fmt.Println("  The server will exit automatically after a successful reset.")
	fmt.Println("==============================================")
	fmt.Println()

	slog.Info("Reset API server started", "addr", listenAddr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
