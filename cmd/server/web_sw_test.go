package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 两份 SW 的缓存名必须带版本戳，且 activate 只扫自己的前缀。
//
// 这两条都是「坏了也没人看得见」的性质：
//   - 缓存名不随版本变 → sw.js 字节恒定 → 浏览器永不更新 SW，旧 chunk 永远累积；
//   - activate 扫全 origin → 两个控制台互相清空对方的离线缓存。
//
// 界面上一切正常，只有离线时才会暴露，所以在这里钉死。
func TestServiceWorkersAreVersionStampedAndNamespaced(t *testing.T) {
	old := appVersion
	appVersion = "v9.9.9"
	defer func() { appVersion = old }()

	mux := (&Server{}).Routes()
	cases := []struct {
		path   string
		prefix string
		other  string
	}{
		{"/sw.js", "aiops-classic-", "aiops-v2-"},
		{"/v2/sw.js", "aiops-v2-", "aiops-classic-"},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, c.path, nil))
		if c.path == "/v2/sw.js" && !v2Embedded() {
			continue // 没构建前端的二进制里没有这份 SW
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status=%d", c.path, rr.Code)
		}
		body := rr.Body.String()
		if strings.Contains(body, swVersionPlaceholder) {
			t.Fatalf("%s still contains %s — 版本戳没被替换", c.path, swVersionPlaceholder)
		}
		if !strings.Contains(body, c.prefix+"v9.9.9") {
			t.Fatalf("%s cache name must be %sv9.9.9", c.path, c.prefix)
		}
		// 各扫各的前缀：不能出现「删掉另一个控制台缓存」的可能。
		if strings.Contains(body, `CACHE_PREFIX = "`+c.other) {
			t.Fatalf("%s sweeps the other console's cache prefix %q", c.path, c.other)
		}
		if !strings.Contains(body, "startsWith(CACHE_PREFIX)") {
			t.Fatalf("%s activate must only delete caches under its own prefix", c.path)
		}
	}
}

// 版本号里的斜杠 / 空格之类不能漏进缓存名。
func TestSWCacheVersionSanitises(t *testing.T) {
	old := appVersion
	defer func() { appVersion = old }()
	for _, c := range []struct{ in, want string }{
		{"v1.1.75", "v1.1.75"},
		{"", "dev"},
		{"feature/branch build", "feature-branch-build"},
	} {
		appVersion = c.in
		if got := swCacheVersion(); got != c.want {
			t.Fatalf("swCacheVersion(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
