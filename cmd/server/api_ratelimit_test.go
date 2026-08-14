package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIRateLimiterSeparateBuckets(t *testing.T) {
	lim := newAPIRateLimiter()
	for i := 0; i < apiRateMaxAnonPerIP; i++ {
		if !lim.allow("ip:1.1.1.1", apiRateMaxAnonPerIP) {
			t.Fatalf("anon denied at %d", i)
		}
	}
	if lim.allow("ip:1.1.1.1", apiRateMaxAnonPerIP) {
		t.Fatal("expected anon IP bucket to deny after cap")
	}
	if !lim.allow("u:admin", apiRateMaxAuthPerUser) {
		t.Fatal("authenticated user bucket must be independent of the IP bucket")
	}
}

func TestAPIRateLimitMiddlewareAuthenticatedNotStarvedByAnonIP(t *testing.T) {
	cfg := newTestConfigStore(t)
	s := &Server{cfg: cfg, auth: NewAuth(cfg)}
	tok := s.auth.issueSession("admin")
	h := s.apiRateLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	fill := func(cookie string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
		req.RemoteAddr = "10.1.1.1:1234"
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < apiRateMaxAnonPerIP; i++ {
		if code := fill(""); code != http.StatusOK {
			t.Fatalf("anon fill %d: HTTP %d", i, code)
		}
	}
	anonRec := httptest.NewRecorder()
	anonReq := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	anonReq.RemoteAddr = "10.1.1.1:1234"
	h.ServeHTTP(anonRec, anonReq)
	if anonRec.Code != http.StatusTooManyRequests {
		t.Fatalf("anon over cap: want 429 got %d", anonRec.Code)
	}
	if anonRec.Header().Get("Retry-After") != "5" {
		t.Fatalf("Retry-After=%q", anonRec.Header().Get("Retry-After"))
	}
	var body map[string]any
	if err := json.Unmarshal(anonRec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "rate_limited" {
		t.Fatalf("code=%v body=%s", body["code"], anonRec.Body.String())
	}
	if body["error"] == "登录尝试过于频繁，请 5 分钟后再试" {
		t.Fatal("429 must not reuse the login-lockout copy")
	}

	if code := fill(tok); code != http.StatusOK {
		t.Fatalf("valid session on same IP should not inherit the anon bucket, got HTTP %d", code)
	}
	if code := fill("not-a-real-session"); code != http.StatusTooManyRequests {
		t.Fatalf("fake cookie must stay on the IP bucket, got HTTP %d", code)
	}
}
