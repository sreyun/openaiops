package main

import "testing"

// licenseResetForTest was removed with the licensing package (v0.20.46).
// Keep a no-op so metrics / support-bundle tests still compile and isolate env.
func licenseResetForTest(t *testing.T) {
	t.Helper()
}

// classicAppJS served the removed Vue classic console bundle. Stub keeps
// scale_hotpath_test compiling; callers must treat a non-empty miss as absent.
func classicAppJS() (bundle []byte, etag string, miss string) {
	return nil, "", "classic console removed"
}
