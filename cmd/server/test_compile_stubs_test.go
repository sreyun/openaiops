package main

import "testing"

// Stubs for symbols removed with the license system / classic Vue console so the
// remaining *_test.go files still compile. Production code no longer calls these.

func licenseResetForTest(t *testing.T) {
	t.Helper()
}

func classicAppJS() (body []byte, etag string, missing string) {
	return nil, "", "classic console removed"
}
