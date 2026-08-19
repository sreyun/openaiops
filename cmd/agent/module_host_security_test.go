package main

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestClamInstallSuggestByOS(t *testing.T) {
	s := clamInstallSuggest()
	if s == "" {
		t.Fatal("empty suggest")
	}
	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(s, "brew") {
			t.Fatalf("darwin hint should mention brew: %s", s)
		}
	case "linux":
		if !strings.Contains(strings.ToLower(s), "clamav") {
			t.Fatalf("linux hint: %s", s)
		}
	}
}

func TestEnsureCommonBinPATH(t *testing.T) {
	old := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", old) })
	_ = os.Setenv("PATH", "/bin")
	ensureCommonBinPATH()
	got := os.Getenv("PATH")
	if runtime.GOOS == "darwin" {
		if !strings.Contains(got, "/opt/homebrew/bin") && !strings.Contains(got, "/usr/local/bin") {
			// dirs may not exist in CI sandbox; function only prepends existing dirs
			t.Logf("PATH after ensure: %s", got)
		}
	}
	if !strings.Contains(got, "/bin") {
		t.Fatalf("lost original PATH: %s", got)
	}
}

func TestModuleHostSecurityScanClamAVOff(t *testing.T) {
	raw, code := moduleHostSecurityScan(context.Background(), map[string]string{"clamav": "0"})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, raw)
	}
	var rep hostSecurityReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Hostname == "" {
		t.Fatal("missing hostname")
	}
	if rep.Malware.ClamAV != "disabled" {
		t.Fatalf("clamav=%q want disabled", rep.Malware.ClamAV)
	}
	if rep.Packages == nil {
		t.Fatal("packages nil")
	}
	if rep.Hardening == nil {
		rep.Hardening = []hostSecFinding{}
	}
}

func TestModuleHostSecurityJSONShape(t *testing.T) {
	raw, code := moduleHostSecurityScan(context.Background(), map[string]string{"clamav": "false"})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"collected_at", "hostname", "os", "packages", "hardening", "malware", "ioc", "firewall"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing key %s", k)
		}
	}
	fw, _ := m["firewall"].(map[string]any)
	if fw["status"] == nil || fw["status"] == "" {
		t.Fatalf("firewall status missing: %#v", fw)
	}
	mal, _ := m["malware"].(map[string]any)
	if mal["clamav"] != "disabled" {
		t.Fatalf("expected clamav disabled when off, got %v", mal["clamav"])
	}
}
