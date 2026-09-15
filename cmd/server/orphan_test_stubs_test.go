package main

import "testing"

// Stubs for symbols removed with the license system / Vue v2 cleanup so the
// server test package still compiles. Production binaries do not use these.

func licenseResetForTest(t *testing.T) {
	t.Helper()
}

func classicAppJS() (body []byte, etag string, missing string) {
	return []byte("/* classic console removed */"), `"removed"`, ""
}
