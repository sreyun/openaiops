package main

import "testing"

// license.go was removed in v0.20.46; keep a no-op so older tests still compile.
func licenseResetForTest(t *testing.T) {
	t.Helper()
}

// classic Vue v2 console bundle was removed; signal missing so callers skip/fail clearly.
func classicAppJS() (body []byte, etag string, miss string) {
	return nil, "", "classic console removed"
}
