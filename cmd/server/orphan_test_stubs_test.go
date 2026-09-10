package main

import "testing"

// Stubs for symbols removed with the open-source license cleanup.
// Keep orphaned tests compiling until those suites are rewritten.

func licenseResetForTest(t *testing.T) {
	t.Helper()
}

func classicAppJS() (body []byte, etag string, missing string) {
	// Stable non-empty stub so scale_hotpath_test still compiles/passes
	// after classicAppJS was dropped from the production code path.
	b := []byte("/* classicAppJS stub */")
	return b, `"stub"`, ""
}
