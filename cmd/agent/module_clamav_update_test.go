package main

import (
	"context"
	"strings"
	"testing"
)

// TestParseProxyHostPort covers the forms operators actually paste into a proxy
// field. freshclam wants host and port separately, so anything we accept here
// has to survive that split.
func TestParseProxyHostPort(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{in: "", wantHost: "", wantPort: 0},
		{in: "proxy.corp:3128", wantHost: "proxy.corp", wantPort: 3128},
		{in: "http://proxy.corp:3128", wantHost: "proxy.corp", wantPort: 3128},
		{in: "https://proxy.corp:8443", wantHost: "proxy.corp", wantPort: 8443},
		{in: "http://proxy.corp/", wantHost: "proxy.corp", wantPort: 8080},
		{in: "10.0.0.9:8080", wantHost: "10.0.0.9", wantPort: 8080},
		{in: "socks5://proxy.corp:1080", wantErr: true},
		{in: "http://user:pass@proxy.corp:3128", wantErr: true},
		{in: "proxy.corp:notaport", wantErr: true},
		{in: "proxy.corp:99999", wantErr: true},
		{in: "proxy corp:3128", wantErr: true},
	}
	for _, c := range cases {
		host, port, err := parseProxyHostPort(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseProxyHostPort(%q) accepted an invalid proxy (host=%q port=%d)", c.in, host, port)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseProxyHostPort(%q) failed: %v", c.in, err)
			continue
		}
		if host != c.wantHost || port != c.wantPort {
			t.Errorf("parseProxyHostPort(%q) = %q/%d, want %q/%d", c.in, host, port, c.wantHost, c.wantPort)
		}
	}
}

// TestBuildFreshclamConfigReplacesExistingProxy guards against ending up with
// two HTTPProxyServer directives: freshclam takes the last one, so a stale
// entry silently sending traffic to a dead proxy is the failure to avoid.
func TestBuildFreshclamConfigReplacesExistingProxy(t *testing.T) {
	base := strings.Join([]string{
		"DatabaseMirror database.clamav.net",
		"HTTPProxyServer old.proxy.local",
		"  httpproxyport 1234",
		"LogVerbose false",
	}, "\n")

	got := buildFreshclamConfig(base, "new.proxy.local", 3128)

	if strings.Contains(got, "old.proxy.local") || strings.Contains(got, "1234") {
		t.Errorf("stale proxy directives survived:\n%s", got)
	}
	if n := strings.Count(strings.ToLower(got), "httpproxyserver"); n != 1 {
		t.Errorf("expected exactly one HTTPProxyServer, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "HTTPProxyServer new.proxy.local") || !strings.Contains(got, "HTTPProxyPort 3128") {
		t.Errorf("new proxy not written:\n%s", got)
	}
	// Unrelated settings must be preserved: this config replaces the host's own
	// for one run, and dropping its mirror would break the update.
	for _, keep := range []string{"DatabaseMirror database.clamav.net", "LogVerbose false"} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost host setting %q:\n%s", keep, got)
		}
	}
}

// TestClamavUpdateReportsMissingBinary keeps the no-ClamAV case actionable
// rather than surfacing an exec error.
func TestClamavUpdateReportsMissingBinary(t *testing.T) {
	if findFreshclamBin() != "" {
		t.Skip("freshclam is installed on this machine")
	}
	out, code := moduleClamavUpdate(context.Background(), map[string]string{})
	if code == 0 {
		t.Fatal("missing freshclam must be reported as a failure")
	}
	if !strings.Contains(string(out), "freshclam") {
		t.Errorf("error should name the missing tool, got: %s", out)
	}
}

// TestClamavUpdateRejectsBadProxyBeforeRunning makes sure a malformed proxy
// fails fast instead of being written into a config file.
func TestClamavUpdateRejectsBadProxyBeforeRunning(t *testing.T) {
	out, code := moduleClamavUpdate(context.Background(), map[string]string{"proxy": "socks5://p:1080"})
	if code == 0 {
		t.Fatal("unsupported proxy scheme should be rejected")
	}
	if !strings.Contains(string(out), "http") {
		t.Errorf("error should explain the supported schemes, got: %s", out)
	}
}

func TestClampSec(t *testing.T) {
	if got := clampSec(10, 60, 3600); got != 60 {
		t.Errorf("clampSec low = %d, want 60", got)
	}
	if got := clampSec(99999, 60, 3600); got != 3600 {
		t.Errorf("clampSec high = %d, want 3600", got)
	}
	if got := clampSec(300, 60, 3600); got != 300 {
		t.Errorf("clampSec passthrough = %d, want 300", got)
	}
}
