package main

import "testing"

// Stubs for symbols removed with the license system / Vue v2 console cleanup
// (b8f29f4 / b9e35f3). A few older tests still reference them; keep the package
// buildable without resurrecting the deleted features.

func licenseResetForTest(t *testing.T) {
	t.Helper()
}

func classicAppJS() (body []byte, etag string, missing string) {
	return []byte("/* classic console removed */"), `"dead"`, ""
}
