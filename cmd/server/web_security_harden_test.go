package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAssertURLAllowedDNSFailClosed(t *testing.T) {
	err := assertURLAllowed("http://this-host-should-not-resolve.invalid/", false)
	if err == nil {
		t.Fatal("unresolvable host must be rejected when private denied")
	}
}

func TestAssertURLAllowedUnspecified(t *testing.T) {
	if err := assertURLAllowed("http://0.0.0.0/", false); err == nil {
		t.Fatal("0.0.0.0 must be blocked")
	}
}

// Cloud metadata must stay blocked even when 「允许私网」is on. Aliyun IMDS
// (100.100.100.200) is neither RFC1918 nor link-local, so a private-only check
// previously let operators SSRF it via web-scan targets.
func TestAssertURLAllowedCloudMetadataAlwaysBlocked(t *testing.T) {
	cases := []string{
		"http://100.100.100.200/latest/meta-data/",
		"http://169.254.169.254/latest/meta-data/",
		"http://169.254.170.2/v2/metadata",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://metadata.tencentyun.com/latest/meta-data/",
	}
	for _, u := range cases {
		if err := assertURLAllowed(u, false); err == nil {
			t.Fatalf("allowPrivate=false must reject %s", u)
		}
		if err := assertURLAllowed(u, true); err == nil {
			t.Fatalf("allowPrivate=true must still reject cloud metadata %s", u)
		}
	}
	// RFC1918 stays gated by the allowPrivate flag.
	if err := assertURLAllowed("http://10.0.0.8/", false); err == nil {
		t.Fatal("10/8 must be blocked when private denied")
	}
	if err := assertURLAllowed("http://10.0.0.8/", true); err != nil {
		t.Fatalf("10/8 must be allowed when private permitted: %v", err)
	}
}

func TestConstrainPathUnderRoot(t *testing.T) {
	root := t.TempDir()
	if full, ok := constrainPathUnderRoot("http/cves", root); !ok || !strings.HasPrefix(full, root) {
		t.Fatalf("rel path: %q ok=%v", full, ok)
	}
	if _, ok := constrainPathUnderRoot("../etc/passwd", root); ok {
		t.Fatal("escape must fail")
	}
	if _, ok := constrainPathUnderRoot("/etc/passwd", root); ok {
		t.Fatal("abs outside must fail")
	}
	if runtime.GOOS == "windows" {
		if _, ok := constrainPathUnderRoot(`C:\Windows\System32\config`, root); ok {
			t.Fatal("windows abs outside must fail")
		}
	}
	_ = filepath.Join(root, "x")
}

func TestRedactCurlCommand(t *testing.T) {
	in := `curl -H 'Authorization: Bearer secret-token' -H 'Cookie: sid=abc' 'https://x/?password=hunter2'`
	out := redactCurlCommand(in)
	if strings.Contains(out, "secret-token") || strings.Contains(out, "sid=abc") || strings.Contains(out, "hunter2") {
		t.Fatalf("secrets leaked: %s", out)
	}
	if !strings.Contains(out, "********") {
		t.Fatalf("expected redaction marks: %s", out)
	}
}

func TestWebAuthWarmupBlocksPrivateRedirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer final.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/", http.StatusFound)
	}))
	defer redir.Close()

	_, err := resolveWebAuthHeaders(WebScanTarget{
		BaseURL:      redir.URL + "/",
		AuthType:     "header_body",
		AuthLoginURL: redir.URL + "/login",
		AuthMethod:   "GET",
		AuthHeader:   "X-Test: 1",
		AuthBody:     "x=1",
	}, false)
	if err == nil {
		t.Fatal("redirect to loopback must fail when private denied")
	}
	if !strings.Contains(err.Error(), "重定向") && !strings.Contains(err.Error(), "私网") && !strings.Contains(err.Error(), "禁止") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestWebTargetScheduleDueIntervalUsesLastScanAt(t *testing.T) {
	m := newWebScanManager(t.TempDir(), 1)
	now := time.Now()
	t0 := WebScanTarget{
		ID: "wt-1", LastScanAt: now.Add(-5 * time.Minute).Unix(),
		Schedule: &PlaybookSchedule{Enabled: true, Kind: "interval", IntervalMin: 60},
	}
	if webTargetScheduleDue(t0, m, now) {
		t.Fatal("should not fire within interval seeded by LastScanAt")
	}
	t1 := WebScanTarget{
		ID: "wt-2", LastScanAt: now.Add(-2 * time.Hour).Unix(),
		Schedule: &PlaybookSchedule{Enabled: true, Kind: "interval", IntervalMin: 60},
	}
	if !webTargetScheduleDue(t1, m, now) {
		t.Fatal("should fire after interval")
	}
}

func TestWebScanManagerFailsStuckRunningOnLoad(t *testing.T) {
	dir := t.TempDir()
	m := &webScanManager{scans: []*WebScanResult{{ID: "ws-1", Status: "running"}}, lastRun: map[string]int64{}, dir: dir}
	m.saveLocked()
	m2 := newWebScanManager(dir, 1)
	if len(m2.scans) != 1 || m2.scans[0].Status != "failed" {
		t.Fatalf("%+v", m2.scans)
	}
}

func TestSanitizeWebTargetIntervalFloor(t *testing.T) {
	t0 := WebScanTarget{
		Name: "n", BaseURL: "https://example.com", Tags: []string{"misconfig"},
		Schedule: &PlaybookSchedule{Enabled: true, Kind: "interval", IntervalMin: 5},
	}
	if err := sanitizeWebTarget(&t0, false); err != nil {
		t.Fatal(err)
	}
	if t0.Schedule.IntervalMin != 15 {
		t.Fatalf("floor=%d", t0.Schedule.IntervalMin)
	}
}
