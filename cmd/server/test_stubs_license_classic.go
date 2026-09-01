package main

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
)

// licenseResetForTest was removed with the licensing subsystem (v0.20.46).
// Keep a no-op so metrics/support tests still compile against open-source builds.
func licenseResetForTest(t *testing.T) {
	t.Helper()
}

// classicAppJS mirrors GET /app.js: concatenate web/js modules once and return
// a stable ETag. Used by scale_hotpath_test after the inlined handler grew.
var (
	classicAppOnce sync.Once
	classicAppBody []byte
	classicAppETag string
	classicAppMiss string
)

func classicAppJS() (body []byte, etag string, missing string) {
	classicAppOnce.Do(func() {
		// Keep in sync with GET /app.js in handlers.go.
		modules := []string{
			"core", "export", "duplicates", "overview", "hosts", "host-picker", "forecast",
			"agent-update", "terminal", "desktop", "settings", "nav", "attachments", "sre",
			"host-inspect", "ai-assist", "ops-actions", "apimon", "governance", "datasource",
			"sql-toolkit", "cicd", "hardware", "hyperv", "containers", "k8s", "netflow", "snmp",
			"content-audit", "security-overview", "host-security", "security-feeds", "web-security",
			"security-center", "scrape", "dash_charts", "dashboard", "init",
		}
		var buf []byte
		for _, m := range modules {
			b, err := webFS.ReadFile("web/js/" + m + ".js")
			if err != nil {
				classicAppMiss = m
				classicAppBody = nil
				classicAppETag = ""
				return
			}
			buf = append(buf, b...)
			buf = append(buf, '\n', ';', '\n')
		}
		sum := sha256.Sum256(buf)
		classicAppBody = buf
		classicAppETag = `"` + hex.EncodeToString(sum[:8]) + `"`
	})
	return classicAppBody, classicAppETag, classicAppMiss
}
