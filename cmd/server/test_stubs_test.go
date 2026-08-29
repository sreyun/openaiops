package main

import "testing"

// Stubs for symbols removed with the license/v2-console teardown (v0.20.46+)
// so the rest of the package's tests still compile.

func licenseResetForTest(t *testing.T) {
	t.Helper()
}

func classicAppJS() (body []byte, etag string, missing string) {
	return []byte("/* stub */"), `"stub"`, ""
}
