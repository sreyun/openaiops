package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var brandLogoNameValid = regexp.MustCompile(`^logo(?:-[0-9a-f]{16})?\.(png|jpg|jpeg|webp|svg)$`)

type BrandConfig struct {
	ProductName string `json:"product_name"`
	Tagline     string `json:"tagline"`
	LogoURL     string `json:"logo_url"`
}

func (cs *ConfigStore) Brand() BrandConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.cfg.Brand
}

func (cs *ConfigStore) SetBrand(c BrandConfig) error {
	if err := normalizeBrandConfig(&c); err != nil {
		return err
	}

	cs.mu.Lock()
	cs.cfg.Brand = c
	cs.mu.Unlock()
	return cs.save()
}

func normalizeBrandConfig(c *BrandConfig) error {
	c.ProductName = strings.TrimSpace(c.ProductName)
	c.Tagline = strings.TrimSpace(c.Tagline)
	c.LogoURL = strings.TrimSpace(c.LogoURL)
	if c.LogoURL != "" {
		if _, ok := parseBrandLogoURL(c.LogoURL); !ok {
			return fmt.Errorf("logo_url 必须为空或使用本地品牌 Logo 路径")
		}
	}
	return nil
}

func (s *Server) brandAssetsDir() string {
	if s == nil || s.cfg == nil || s.cfg.path == "" {
		return filepath.Join("data", "brand-assets")
	}
	return filepath.Join(filepath.Dir(s.cfg.path), "brand-assets")
}

func parseBrandLogoURL(url string) (name string, ok bool) {
	url = strings.TrimSpace(url)
	const prefix = "/api/v1/brand/logo/"
	if !strings.HasPrefix(url, prefix) {
		return "", false
	}
	name = strings.TrimPrefix(url, prefix)
	if !brandLogoNameValid.MatchString(name) {
		return "", false
	}
	return name, true
}

func (s *Server) removeBrandLogoURL(url string) {
	name, ok := parseBrandLogoURL(url)
	if !ok {
		return
	}
	_ = os.Remove(filepath.Join(s.brandAssetsDir(), name))
}

func (s *Server) handleGetBrand(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Brand())
}

func (s *Server) handleGetAdminBrand(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Brand())
}

func (s *Server) handleSetAdminBrand(w http.ResponseWriter, r *http.Request) {
	var c BrandConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := normalizeBrandConfig(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	oldURL := s.cfg.Brand().LogoURL
	if err := s.cfg.SetBrand(c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if oldURL != "" && oldURL != c.LogoURL {
		s.removeBrandLogoURL(oldURL)
	}
	writeJSON(w, http.StatusOK, s.cfg.Brand())
}

// handleUploadBrandLogo POST /api/v1/admin/brand/logo
// multipart: file
func (s *Server) handleUploadBrandLogo(w http.ResponseWriter, r *http.Request) {
	const uploadMax = dashAssetLogoMaxBytes + 64<<10
	r.Body = http.MaxBytesReader(w, r.Body, uploadMax)
	if err := r.ParseMultipartForm(uploadMax); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "上传表单无效或文件过大"})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少文件字段 file"})
		return
	}
	defer file.Close()
	if hdr.Size > 0 && hdr.Size > dashAssetLogoMaxBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("文件超过限制（logo 最大 %d KiB）", dashAssetLogoMaxBytes>>10)})
		return
	}

	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	head = head[:n]
	mime, ok := sniffDashAssetMIME(head, hdr.Header.Get("Content-Type"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "仅支持 PNG / JPEG / WebP / SVG 图片"})
		return
	}
	ext, ok := dashAssetExtForMIME(mime)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不支持的图片类型"})
		return
	}

	dir := s.brandAssetsDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法创建资源目录"})
		return
	}
	name := "logo-" + genToken()[:16] + ext
	if !brandLogoNameValid.MatchString(name) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "资源命名失败"})
		return
	}
	dest := filepath.Join(dir, name)
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法写入资源文件"})
		return
	}
	written, err := out.Write(head)
	if err == nil {
		var n64 int64
		n64, err = io.Copy(out, io.LimitReader(file, dashAssetLogoMaxBytes-int64(written)+1))
		written += int(n64)
	}
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(dest)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "写入资源失败"})
		return
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "写入资源失败"})
		return
	}
	if int64(written) > dashAssetLogoMaxBytes {
		_ = os.Remove(dest)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("文件超过限制（logo 最大 %d KiB）", dashAssetLogoMaxBytes>>10)})
		return
	}

	cur := s.cfg.Brand()
	oldURL := cur.LogoURL
	url := "/api/v1/brand/logo/" + name
	cur.LogoURL = url
	if err := s.cfg.SetBrand(cur); err != nil {
		_ = os.Remove(dest)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if oldURL != "" && oldURL != url {
		s.removeBrandLogoURL(oldURL)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": url, "brand": s.cfg.Brand(), "bytes": written})
}

// handleDeleteBrandLogo DELETE /api/v1/admin/brand/logo
func (s *Server) handleDeleteBrandLogo(w http.ResponseWriter, r *http.Request) {
	cur := s.cfg.Brand()
	oldURL := cur.LogoURL
	cur.LogoURL = ""
	if err := s.cfg.SetBrand(cur); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.removeBrandLogoURL(oldURL)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "brand": s.cfg.Brand()})
}

// handleGetBrandLogo GET /api/v1/brand/logo/{name}
func (s *Server) handleGetBrandLogo(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if !brandLogoNameValid.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.brandAssetsDir(), name)
	clean := filepath.Clean(path)
	root := filepath.Clean(s.brandAssetsDir())
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", dashAssetMIMEForExt(filepath.Ext(name)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, name, st.ModTime(), f)
}
