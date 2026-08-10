package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRecoverVerifyPasswordOTPOneShot(t *testing.T) {
	dir := t.TempDir()
	cfg, err := NewConfigStore(filepath.Join(dir, "cfg.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.CreateUser("ops", "ops-pass-123", "Ops", "ops@example.com", RoleOperator); err != nil {
		t.Fatal(err)
	}
	em := newEmailManager()
	code, err := em.issueCode("ops@example.com", "recover_password")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: cfg, store: NewStore(), auth: NewAuth(cfg), emailMgr: em}

	body := `{"email":"ops@example.com","code":"` + code + `","purpose":"recover_password"}`
	var (
		mu     sync.Mutex
		tokens []string
		codes  []int
		wg     sync.WaitGroup
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/account/recover-verify", strings.NewReader(body))
			srv.handleRecoverVerify(rr, req)
			mu.Lock()
			defer mu.Unlock()
			codes = append(codes, rr.Code)
			if rr.Code != http.StatusOK {
				return
			}
			var resp map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Errorf("json: %v", err)
				return
			}
			if tok, _ := resp["reset_token"].(string); tok != "" {
				tokens = append(tokens, tok)
			}
		}()
	}
	wg.Wait()

	if len(tokens) != 1 {
		t.Fatalf("expected exactly one reset_token from a single OTP, got %d (status codes=%v)", len(tokens), codes)
	}
	// Replay after consume must fail.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/recover-verify", strings.NewReader(body))
	srv.handleRecoverVerify(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("replay after consume: got %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestConsumeVerifiedCodeOneShot(t *testing.T) {
	em := newEmailManager()
	code, err := em.issueCode("a@b.com", "recover_password")
	if err != nil {
		t.Fatal(err)
	}
	if em.markCodeVerified("a@b.com", "recover_password", code) == "" {
		t.Fatal("mark should succeed")
	}
	if em.consumeVerifiedCode("a@b.com", "recover_password", code) == "" {
		t.Fatal("first consume should succeed")
	}
	if em.consumeVerifiedCode("a@b.com", "recover_password", code) != "" {
		t.Fatal("second consume must fail")
	}
}
