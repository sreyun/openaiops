package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelfTestPassesOnSuccessfulRegistration(t *testing.T) {
	t.Setenv("AIOPS_MACHINE_ID", "selftest-machine")
	var gotToken, gotFP string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/register" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotToken, gotFP = req["token"], req["fingerprint"]
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "host_id": req["host_id"]})
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runSelfTest(&out, []ServerConfig{{Server: srv.URL, Token: "tok-1"}}, "host-1", "C:\\x\\config.yaml", "")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if gotToken != "tok-1" {
		t.Errorf("token not forwarded, got %q", gotToken)
	}
	if gotFP == "" {
		t.Error("fingerprint must be sent; the server rejects registrations without one")
	}
	if !strings.Contains(out.String(), "PASS") {
		t.Errorf("missing PASS verdict:\n%s", out.String())
	}
}

// A rejected token is the single most likely reason an install looks perfect and
// no host appears, so it must be named explicitly rather than reported as a
// generic HTTP error.
func TestSelfTestExplainsRejectedToken(t *testing.T) {
	t.Setenv("AIOPS_MACHINE_ID", "selftest-machine")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runSelfTest(&out, []ServerConfig{{Server: srv.URL, Token: "stale"}}, "host-1", "cfg", "")
	if code == 0 {
		t.Fatalf("a 403 must fail the self-test\n%s", out.String())
	}
	s := out.String()
	for _, want := range []string{"403", "Token", "重新生成安装命令"} {
		if !strings.Contains(s, want) {
			t.Errorf("diagnosis missing %q:\n%s", want, s)
		}
	}
}

func TestSelfTestExplainsHostIDConflict(t *testing.T) {
	t.Setenv("AIOPS_MACHINE_ID", "selftest-machine")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	var out bytes.Buffer
	if code := runSelfTest(&out, []ServerConfig{{Server: srv.URL}}, "host-1", "cfg", ""); code == 0 {
		t.Fatal("a 409 must fail the self-test")
	}
	if !strings.Contains(out.String(), "agent_state.json") {
		t.Errorf("conflict diagnosis should point at the cloned state file:\n%s", out.String())
	}
}

// Dropped packets (firewall / security group) and a closed port need different
// fixes, and neither looks like "the service is running" from the installer.
func TestSelfTestReportsUnreachableServer(t *testing.T) {
	t.Setenv("AIOPS_MACHINE_ID", "selftest-machine")
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening on that port any more

	var out bytes.Buffer
	if code := runSelfTest(&out, []ServerConfig{{Server: url}}, "host-1", "cfg", ""); code == 0 {
		t.Fatal("an unreachable server must fail the self-test")
	}
	if !strings.Contains(out.String(), "TCP 连接") {
		t.Errorf("missing TCP stage diagnosis:\n%s", out.String())
	}
}

func TestSelfTestRejectsBadServerURL(t *testing.T) {
	t.Setenv("AIOPS_MACHINE_ID", "selftest-machine")
	var out bytes.Buffer
	if code := runSelfTest(&out, []ServerConfig{{Server: "not-a-url"}}, "h", "cfg", ""); code == 0 {
		t.Fatal("a malformed server URL must fail")
	}
}

func TestSelfTestPersistsCanonicalHostID(t *testing.T) {
	t.Setenv("AIOPS_MACHINE_ID", "selftest-machine")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "host_id": "canonical-host"})
	}))
	defer srv.Close()

	dir := t.TempDir()
	state := filepath.Join(dir, "agent_state.json")
	var out bytes.Buffer
	code := runSelfTest(&out, []ServerConfig{{Server: srv.URL, Token: "tok"}}, "local-host", filepath.Join(dir, "config.yaml"), state)
	if code != 0 {
		t.Fatalf("exit=%d\n%s", code, out.String())
	}
	got := readHostIDFromState(state)
	if got != "canonical-host" {
		t.Fatalf("state host_id=%q, want canonical-host\n%s", got, out.String())
	}
}

func TestSelfTestFollowsHTTPToHTTPSStyleRedirect(t *testing.T) {
	t.Setenv("AIOPS_MACHINE_ID", "selftest-machine")
	var gotMethod string
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "host_id": req["host_id"]})
	}))
	defer final.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+r.URL.RequestURI(), http.StatusMovedPermanently)
	}))
	defer redir.Close()

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(cfg, []byte("server: \""+redir.URL+"\"\ntoken: tok\n"), 0o644)

	var out bytes.Buffer
	code := runSelfTest(&out, []ServerConfig{{Server: redir.URL, Token: "tok"}}, "host-1", cfg, "")
	if code != 0 {
		t.Fatalf("exit=%d\n%s", code, out.String())
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method=%s want POST", gotMethod)
	}
	if !strings.Contains(out.String(), "重定向") {
		t.Fatalf("expected redirect info:\n%s", out.String())
	}
}

func TestUpgradeConfigServerURLUsesTempThenRename(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	body := "server: http://panel.example.com\ntoken: secret-tok\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	upgradeConfigServerURL(cfg, "http://panel.example.com", "https://panel.example.com", &out)
	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "https://panel.example.com") {
		t.Fatalf("server not upgraded: %s", got)
	}
	if strings.Contains(string(got), "http://panel.example.com") {
		t.Fatalf("old http URL still present: %s", got)
	}
	if !strings.Contains(string(got), "secret-tok") {
		t.Fatal("token must survive the rewrite")
	}
	if _, err := os.Stat(cfg + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file should be gone after rename, err=%v", err)
	}
}
