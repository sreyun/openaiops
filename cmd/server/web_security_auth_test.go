package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseHeaderLines(t *testing.T) {
	got := parseHeaderLines("Cookie: a=1\n\nX-Api-Key: secret\nbadline\nAuthorization: Bearer tok")
	if len(got) != 3 {
		t.Fatalf("want 3 headers, got %d: %#v", len(got), got)
	}
	if got[0] != "Cookie: a=1" || !strings.HasPrefix(got[2], "Authorization:") {
		t.Fatalf("unexpected: %#v", got)
	}
}

func TestResolveWebAuthHeadersBasic(t *testing.T) {
	hs, err := resolveWebAuthHeaders(WebScanTarget{
		AuthType: "basic", AuthUser: "alice", AuthPass: "s3cret",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 {
		t.Fatalf("want 1 header, got %#v", hs)
	}
	want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if hs[0] != want {
		t.Fatalf("got %q want %q", hs[0], want)
	}
}

func TestResolveWebAuthHeadersBearer(t *testing.T) {
	hs, err := resolveWebAuthHeaders(WebScanTarget{
		AuthType: "bearer", AuthPass: "Bearer tok-123",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 || hs[0] != "Authorization: Bearer tok-123" {
		t.Fatalf("got %#v", hs)
	}
}

func TestResolveWebAuthHeadersCookie(t *testing.T) {
	hs, err := resolveWebAuthHeaders(WebScanTarget{
		AuthType:   "cookie",
		AuthHeader: "Cookie: sid=abc\nX-Trace: 1",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 2 {
		t.Fatalf("got %#v", hs)
	}
}

func TestWebAuthWarmupFormCapturesCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("username") != "u1" || r.Form.Get("password") != "p1" {
			http.Error(w, "bad creds", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "sess-ok", Path: "/"})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	hs, err := resolveWebAuthHeaders(WebScanTarget{
		BaseURL:      srv.URL + "/",
		AuthType:     "form",
		AuthUser:     "u1",
		AuthPass:     "p1",
		AuthLoginURL: "/login",
		AuthMethod:   "POST",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	var cookie string
	for _, h := range hs {
		if strings.HasPrefix(strings.ToLower(h), "cookie:") {
			cookie = h
		}
	}
	if !strings.Contains(cookie, "session=sess-ok") {
		t.Fatalf("expected session cookie, got %#v", hs)
	}
}

func TestWebAuthWarmupHeaderBodyJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
			http.Error(w, "bad ct", http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-Api-Key") != "k1" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "tok", Value: "t9", Path: "/"})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hs, err := resolveWebAuthHeaders(WebScanTarget{
		BaseURL:         srv.URL + "/",
		AuthType:        "header_body",
		AuthLoginURL:    srv.URL + "/auth",
		AuthMethod:      "POST",
		AuthHeader:      "X-Api-Key: k1",
		AuthBody:        `{"user":"{{username}}","pass":"{{password}}"}`,
		AuthUser:        "alice",
		AuthPass:        "secret",
		AuthContentType: "application/json",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(hs, "\n")
	if !strings.Contains(joined, "tok=t9") || !strings.Contains(joined, "X-Api-Key: k1") {
		t.Fatalf("unexpected headers: %#v", hs)
	}
}

func TestValidateWebTargetAuth(t *testing.T) {
	if err := validateWebTargetAuth(WebScanTarget{AuthType: "basic"}); err == nil {
		t.Fatal("basic without creds should fail")
	}
	if err := validateWebTargetAuth(WebScanTarget{
		AuthType: "form", AuthUser: "a", AuthPass: "b", AuthLoginURL: "/login",
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateWebTargetAuth(WebScanTarget{AuthType: "header_body", AuthLoginURL: "/x"}); err == nil {
		t.Fatal("header_body needs header or body")
	}
}

func TestSubstituteAuthPlaceholders(t *testing.T) {
	got := substituteAuthPlaceholders(`u={{username}}&p={{password}}`, "a", "b")
	if got != "u=a&p=b" {
		t.Fatalf("got %q", got)
	}
}

func TestIsMaskedSecret(t *testing.T) {
	if !isMaskedSecret("") || !isMaskedSecret("********") || !isMaskedSecret("Cookie: ****") {
		t.Fatal("expected masked")
	}
	if isMaskedSecret("real-secret") {
		t.Fatal("should not treat as masked")
	}
}
