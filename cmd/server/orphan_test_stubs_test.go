package main

import "testing"

// licenseResetForTest was removed with the licensing system (v0.20.46).
// Keep a no-op stub so metrics_support_test.go still compiles.
func licenseResetForTest(t *testing.T) {
	t.Helper()
}

// classicAppJS was removed with the Vue v2 console cleanup. Stub a tiny bundle
// so scale_hotpath_test.go's cache assertion still compiles; the real classic
// console is no longer shipped.
func classicAppJS() (body []byte, etag string, missing string) {
	b := []byte("/* classic console removed */")
	return b, "stub", ""
}
