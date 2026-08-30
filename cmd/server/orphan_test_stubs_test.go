package main

import "testing"

// Stubs for symbols removed with the open-source license drop (v0.20.46) and
// the Vue v2 console removal (v0.20.38). Keeping `go test ./cmd/server` compiling
// is enough; the classic-console cache test is skipped via empty-body failure
// unless a real classicAppJS is restored.

func licenseResetForTest(t *testing.T) {
	t.Helper()
}

// classicAppJS used to return (bundle, etag, missingModule). Vue v2 console is gone.
func classicAppJS() (body []byte, etag string, miss string) {
	return nil, "", "classic console removed"
}
