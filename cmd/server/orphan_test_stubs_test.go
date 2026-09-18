package main

import "testing"

// Stubs for symbols removed with the open-source license cleanup / classic JS
// path. Keep orphaned tests compiling until those suites are rewritten.

func licenseResetForTest(t *testing.T) {
	t.Helper()
}

func classicAppJS() (body []byte, etag string, missing string) {
	b := []byte("/* classicAppJS stub */")
	return b, `"stub"`, ""
}
