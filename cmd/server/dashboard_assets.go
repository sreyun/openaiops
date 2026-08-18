package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	dashAssetLogoMaxBytes = 512 << 10 // 512 KiB
	dashAssetBGMaxBytes   = 2 << 20   // 2 MiB
	dashAssetUploadMax    = 3 << 20   // multipart 上限（略大于背景）
)

// dashboardAssetsDir 看板外观资源目录（与配置同卷：data/dashboard-assets）。
func (s *Server) dashboardAssetsDir() string {
	if s == nil || s.cfg == nil || s.cfg.path == "" {
		return filepath.Join("data", "dashboard-assets")
	}
	return filepath.Join(filepath.Dir(s.cfg.path), "dashboard-assets")
}

func dashAssetExtForMIME(ct string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0])) {
	case "image/png":
		return ".png", true
	case "image/jpeg", "image/jpg":
		return ".jpg", true
	case "image/webp":
		return ".webp", true
	case "image/svg+xml":
		return ".svg", true
	default:
		return "", false
	}
}

func dashAssetMIMEForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

func sniffDashAssetMIME(head []byte, declared string) (string, bool) {
	sniffed := http.DetectContentType(head)
	// SVG 常被 sniff 成 text/xml / text/plain
	decl := strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	if decl == "image/svg+xml" {
		low := strings.ToLower(string(head))
		if strings.Contains(low, "<svg") {
			return "image/svg+xml", true
		}
	}
	switch sniffed {
	case "image/png", "image/jpeg", "image/webp":
		return sniffed, true
	}
	if decl == "image/svg+xml" && (sniffed == "text/xml; charset=utf-8" || sniffed == "text/plain; charset=utf-8" || sniffed == "application/xml") {
		low := strings.ToLower(string(head))
		if strings.Contains(low, "<svg") {
			return "image/svg+xml", true
		}
	}
	return "", false
}

func parseDashAssetURL(url string) (dashID, name string, ok bool) {
	url = strings.TrimSpace(url)
	const prefix = "/api/v1/dashboards/assets/"
	if !strings.HasPrefix(url, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(url, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	dashID, name = parts[0], parts[1]
	if !dashAssetDashIDRe.MatchString(dashID) || !dashAssetNameValid.MatchString(name) {
		return "", "", false
	}
	return dashID, name, true
}

func (s *Server) removeDashAssetFile(dashID, name string) {
	if !dashAssetDashIDRe.MatchString(dashID) || !dashAssetNameValid.MatchString(name) {
		return
	}
	path := filepath.Join(s.dashboardAssetsDir(), dashID, name)
	_ = os.Remove(path)
}

// removeDashboardAssets 删除某看板的全部外观资源目录。
func (s *Server) removeDashboardAssets(dashID string) {
	if !dashAssetDashIDRe.MatchString(dashID) {
		return
	}
	_ = os.RemoveAll(filepath.Join(s.dashboardAssetsDir(), dashID))
}

// handleUploadDashboardAsset POST /api/v1/dashboards/{id}/assets
// multipart: file + kind=logo|background
func (s *Server) handleUploadDashboardAsset(w http.ResponseWriter, r *http.Request) {
	dashID := strings.TrimSpace(r.PathValue("id"))
	if !dashAssetDashIDRe.MatchString(dashID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的仪表盘 ID"})
		return
	}
	if _, ok := s.cfg.DashboardByID(dashID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "仪表盘不存在"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, dashAssetUploadMax)
	if err := r.ParseMultipartForm(dashAssetUploadMax); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "上传表单无效或文件过大"})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	var maxBytes int64
	switch kind {
	case "logo":
		maxBytes = dashAssetLogoMaxBytes
	case "background":
		maxBytes = dashAssetBGMaxBytes
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind 须为 logo 或 background"})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少文件字段 file"})
		return
	}
	defer file.Close()
	if hdr.Size > 0 && hdr.Size > maxBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("文件超过限制（%s 最大 %d KiB）", kind, maxBytes>>10)})
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
	dir := filepath.Join(s.dashboardAssetsDir(), dashID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法创建资源目录"})
		return
	}
	name := genToken()[:16] + ext
	if !dashAssetNameValid.MatchString(name) {
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
		n64, err = io.Copy(out, io.LimitReader(file, maxBytes-int64(written)+1))
		written += int(n64)
	}
	_ = out.Close()
	if err != nil {
		_ = os.Remove(dest)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "写入资源失败"})
		return
	}
	if int64(written) > maxBytes {
		_ = os.Remove(dest)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("文件超过限制（%s 最大 %d KiB）", kind, maxBytes>>10)})
		return
	}
	url := "/api/v1/dashboards/assets/" + dashID + "/" + name

	// 替换时删除旧文件（若仍指向本看板资产）
	if cur, ok := s.cfg.DashboardByID(dashID); ok {
		old := ""
		if kind == "logo" {
			old = cur.Appearance.LogoURL
		} else {
			old = cur.Appearance.BackgroundURL
		}
		if old != "" && old != url {
			if oid, oname, ok := parseDashAssetURL(old); ok && oid == dashID {
				s.removeDashAssetFile(oid, oname)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": url, "kind": kind, "bytes": written})
}

// handleGetDashboardAsset GET /api/v1/dashboards/assets/{dashID}/{name}
func (s *Server) handleGetDashboardAsset(w http.ResponseWriter, r *http.Request) {
	dashID := strings.TrimSpace(r.PathValue("dashID"))
	name := strings.TrimSpace(r.PathValue("name"))
	if !dashAssetDashIDRe.MatchString(dashID) || !dashAssetNameValid.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.dashboardAssetsDir(), dashID, name)
	// 防止路径穿越（Join 已净化，再校验 Clean 后仍在目录内）
	clean := filepath.Clean(path)
	root := filepath.Clean(filepath.Join(s.dashboardAssetsDir(), dashID))
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
