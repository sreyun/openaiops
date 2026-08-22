package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleDashboardServesClassicUI(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/", "/?ui=v2", "/?ui=legacy"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		s.handleDashboard(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, rr.Code)
		}
		body := rr.Body.String()
		if !strings.Contains(body, `id="app"`) {
			t.Fatalf("%s: expected classic index.html shell", path)
		}
		if strings.Contains(body, `/v2/assets/`) || strings.Contains(body, "新版 UI") {
			t.Fatalf("%s: must not serve Vue/v2 shell", path)
		}
	}
}

func TestV2RoutesClosed(t *testing.T) {
	s := &Server{}
	mux := s.Routes()
	for _, path := range []string{"/v2", "/v2/", "/v2/index.html", "/v2/assets/x.js"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d want 404", path, rr.Code)
		}
	}
	if isPublicPath(httptest.NewRequest(http.MethodGet, "/v2/assets/x.js", nil)) {
		t.Fatal("/v2/ must not be a public auth bypass path")
	}
}

func TestV2AssetsRemovedFromEmbed(t *testing.T) {
	if _, err := webFS.ReadFile("web/v2/index.html"); err == nil {
		t.Fatal("web/v2/index.html must be removed from embed")
	}
	if entries, err := fs.ReadDir(webFS, "web/v2"); err == nil && len(entries) > 0 {
		t.Fatalf("web/v2 must be empty or absent, found %d entries", len(entries))
	}
}
