package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrandPublicGetEmpty(t *testing.T) {
	cs := newTestConfigStore(t)
	srv := &Server{cfg: cs}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/brand", nil)
	rr := httptest.NewRecorder()
	srv.handleGetBrand(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out BrandConfig
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ProductName != "" || out.Tagline != "" || out.LogoURL != "" {
		t.Fatalf("expected empty brand, got %+v", out)
	}
}

func TestBrandAdminSetRoundTrip(t *testing.T) {
	cs := newTestConfigStore(t)
	srv := &Server{cfg: cs}

	body := []byte(`{"product_name":"  AcmeOps  ","tagline":" 运维中台 ","logo_url":" /api/v1/brand/logo/logo-0123456789abcdef.png "}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/brand", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.handleSetAdminBrand(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("set status %d body %s", rr.Code, rr.Body.String())
	}
	got := cs.Brand()
	if got.ProductName != "AcmeOps" || got.Tagline != "运维中台" || got.LogoURL != "/api/v1/brand/logo/logo-0123456789abcdef.png" {
		t.Fatalf("got %+v", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/brand", nil)
	rr = httptest.NewRecorder()
	srv.handleGetAdminBrand(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status %d body %s", rr.Code, rr.Body.String())
	}
	var out BrandConfig
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out != got {
		t.Fatalf("admin get = %+v, want %+v", out, got)
	}
}

func TestSetBrandReplacesAllFields(t *testing.T) {
	cs := newTestConfigStore(t)
	if err := cs.SetBrand(BrandConfig{ProductName: "AcmeOps", Tagline: "运维中台", LogoURL: "/api/v1/brand/logo/logo-0123456789abcdef.png"}); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetBrand(BrandConfig{ProductName: "NewOps", Tagline: "", LogoURL: ""}); err != nil {
		t.Fatal(err)
	}

	got := cs.Brand()
	if got.ProductName != "NewOps" || got.Tagline != "" || got.LogoURL != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestBrandPublicPaths(t *testing.T) {
	for _, p := range []string{"/api/v1/brand", "/api/v1/brand/logo/logo-0123456789abcdef.png"} {
		if !isPublicPath(httptest.NewRequest(http.MethodGet, p, nil)) {
			t.Fatalf("%s must be public", p)
		}
	}
}

func TestConfigSetPreservesBrand(t *testing.T) {
	cs := newTestConfigStore(t)
	want := BrandConfig{ProductName: "AcmeOps", Tagline: "运维中台", LogoURL: "/api/v1/brand/logo/logo-0123456789abcdef.png"}
	if err := cs.SetBrand(want); err != nil {
		t.Fatal(err)
	}

	next := cs.Get()
	next.Brand = BrandConfig{}
	if err := cs.Set(next); err != nil {
		t.Fatal(err)
	}

	if got := cs.Brand(); got != want {
		t.Fatalf("brand after Set = %+v, want %+v", got, want)
	}
}

func TestBrandLogoUploadAndPublicGet(t *testing.T) {
	cs := newTestConfigStore(t)
	srv := &Server{cfg: cs}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "logo.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(tinyPNG(t)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/brand/logo", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	srv.handleUploadBrandLogo(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("upload status %d body %s", rr.Code, rr.Body.String())
	}
	var up struct {
		OK    bool        `json:"ok"`
		URL   string      `json:"url"`
		Brand BrandConfig `json:"brand"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	name, ok := parseBrandLogoURL(up.URL)
	if !up.OK || !ok || !strings.HasPrefix(name, "logo-") || !strings.HasSuffix(name, ".png") {
		t.Fatalf("upload response = %+v", up)
	}
	if got := cs.Brand().LogoURL; got != up.URL {
		t.Fatalf("brand logo_url = %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cs.path), "brand-assets", name)); err != nil {
		t.Fatalf("logo not written: %v", err)
	}

	get := httptest.NewRequest(http.MethodGet, up.URL, nil)
	get.SetPathValue("name", name)
	grr := httptest.NewRecorder()
	srv.handleGetBrandLogo(grr, get)
	if grr.Code != http.StatusOK {
		t.Fatalf("get status %d body %s", grr.Code, grr.Body.String())
	}
	if !strings.HasPrefix(grr.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("content-type = %q", grr.Header().Get("Content-Type"))
	}
	if grr.Body.Len() == 0 {
		t.Fatal("empty logo body")
	}
}

func TestBrandLogoUploadUsesUniqueNameAndRemovesPrevious(t *testing.T) {
	cs := newTestConfigStore(t)
	srv := &Server{cfg: cs}

	first := uploadBrandLogoForTest(t, srv)
	firstName, ok := parseBrandLogoURL(first.URL)
	if !ok {
		t.Fatalf("bad first URL %q", first.URL)
	}

	second := uploadBrandLogoForTest(t, srv)
	secondName, ok := parseBrandLogoURL(second.URL)
	if !ok {
		t.Fatalf("bad second URL %q", second.URL)
	}
	if first.URL == second.URL || firstName == secondName {
		t.Fatalf("uploads should get unique URLs, first=%q second=%q", first.URL, second.URL)
	}
	if got := cs.Brand().LogoURL; got != second.URL {
		t.Fatalf("brand logo_url = %q, want %q", got, second.URL)
	}
	dir := filepath.Join(filepath.Dir(cs.path), "brand-assets")
	if _, err := os.Stat(filepath.Join(dir, firstName)); !os.IsNotExist(err) {
		t.Fatalf("old logo should be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, secondName)); err != nil {
		t.Fatalf("new logo should remain: %v", err)
	}
}

func TestBrandLogoRejectOversize(t *testing.T) {
	cs := newTestConfigStore(t)
	srv := &Server{cfg: cs}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "logo.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(bytes.Repeat([]byte{0}, dashAssetLogoMaxBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/brand/logo", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	srv.handleUploadBrandLogo(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if got := cs.Brand().LogoURL; got != "" {
		t.Fatalf("logo_url should remain empty, got %q", got)
	}
}

func TestBrandLogoDeleteClearsLocalLogo(t *testing.T) {
	cs := newTestConfigStore(t)
	srv := &Server{cfg: cs}
	dir := filepath.Join(filepath.Dir(cs.path), "brand-assets")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	logoPath := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(logoPath, tinyPNG(t), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetBrand(BrandConfig{ProductName: "AIOps", LogoURL: "/api/v1/brand/logo/logo.png"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/brand/logo", nil)
	rr := httptest.NewRecorder()
	srv.handleDeleteBrandLogo(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status %d body %s", rr.Code, rr.Body.String())
	}
	if got := cs.Brand(); got.ProductName != "AIOps" || got.LogoURL != "" {
		t.Fatalf("brand after delete = %+v", got)
	}
	if _, err := os.Stat(logoPath); !os.IsNotExist(err) {
		t.Fatalf("logo file should be removed, err=%v", err)
	}
}

func TestBrandRejectsExternalLogoURL(t *testing.T) {
	cs := newTestConfigStore(t)
	if err := cs.SetBrand(BrandConfig{LogoURL: "https://evil.example/logo.png"}); err == nil {
		t.Fatal("external logo_url should be rejected")
	}

	srv := &Server{cfg: cs}
	body := []byte(`{"product_name":"Acme","logo_url":"https://evil.example/logo.png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/brand", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.handleSetAdminBrand(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}

func TestBrandAdminSetRemovesOldLocalLogoWhenChanged(t *testing.T) {
	cs := newTestConfigStore(t)
	srv := &Server{cfg: cs}
	dir := filepath.Join(filepath.Dir(cs.path), "brand-assets")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	oldName := "logo-0123456789abcdef.png"
	oldPath := filepath.Join(dir, oldName)
	if err := os.WriteFile(oldPath, tinyPNG(t), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetBrand(BrandConfig{ProductName: "AIOps", LogoURL: "/api/v1/brand/logo/" + oldName}); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"product_name":"AIOps","logo_url":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/brand", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.handleSetAdminBrand(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old logo should be removed after clear, err=%v", err)
	}
}

type brandLogoUploadResp struct {
	OK    bool        `json:"ok"`
	URL   string      `json:"url"`
	Brand BrandConfig `json:"brand"`
}

func uploadBrandLogoForTest(t *testing.T, srv *Server) brandLogoUploadResp {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "logo.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(tinyPNG(t)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/brand/logo", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	srv.handleUploadBrandLogo(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("upload status %d body %s", rr.Code, rr.Body.String())
	}
	var up brandLogoUploadResp
	if err := json.Unmarshal(rr.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	return up
}
