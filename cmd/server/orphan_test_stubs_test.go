package main

import "testing"

// Stubs for symbols removed with the open-source license / classic-console cleanup.
// Keep orphaned tests compiling until those suites are rewritten or deleted.

func licenseResetForTest(t *testing.T) {
	t.Helper()
}

func classicAppJS() (body []byte, etag string, missing string) {
	// Return a stable non-empty stub so scale_hotpath_test still compiles/passes
	// after the classic console was removed from the open-source tree.
	b := []byte("/* classic console removed */")
	return b, `"stub"`, ""
}
