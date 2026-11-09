package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// classicAppJSModules 是经典控制台 web/js/ 下的源模块，按依赖顺序拼成单个 /app.js。
// 必须作为单脚本加载——整文件函数提升(hoisting)才生效；若用多个独立 <script>，
// 早模块顶层调用晚模块里定义的 helper/handler 会因每脚本独立提升而 ReferenceError。
var classicAppJSModules = []string{
	"core", "export", "duplicates", "overview", "hosts", "host-picker", "forecast", "agent-update",
	"terminal", "desktop", "settings", "nav", "attachments", "sre", "host-inspect", "ai-assist",
	"ops-actions", "apimon", "governance", "datasource", "sql-toolkit", "cicd", "hardware", "hyperv",
	"containers", "k8s", "netflow", "snmp", "content-audit", "security-overview", "host-security",
	"security-feeds", "web-security", "security-center", "scrape", "dash_charts", "dashboard", "init",
}

var (
	classicAppJSOnce    sync.Once
	classicAppJSBody    []byte
	classicAppJSETag    string
	classicAppJSMissing string
)

// classicAppJS 返回拼好的 /app.js、它的强 ETag，以及缺失的模块名（非空即构建失败）。
//
// 模块来自 go:embed，进程生命周期内不会变，所以只拼一次：此前每次打开页面都要把
// 三十多个模块重新读出、重新拼接、再整段 gzip，且不带 ETag，浏览器每次都得全量下载。
// 机群上规模后，这是登录页最大的一笔无谓开销。
func classicAppJS() (body []byte, etag string, missing string) {
	classicAppJSOnce.Do(func() {
		var buf bytes.Buffer
		for _, m := range classicAppJSModules {
			b, err := webFS.ReadFile("web/js/" + m + ".js")
			if err != nil {
				classicAppJSMissing = m
				return
			}
			buf.Write(b)
			buf.WriteString("\n;\n") // 模块间安全分隔（空语句），防 ASI 边界问题
		}
		classicAppJSBody = buf.Bytes()
		sum := sha256.Sum256(classicAppJSBody)
		classicAppJSETag = `"` + hex.EncodeToString(sum[:12]) + `"`
	})
	return classicAppJSBody, classicAppJSETag, classicAppJSMissing
}

// serveClassicAppJS 输出 /app.js。缺模块时在写任何正文之前就返回 500——
// 旧实现边读边写，读到缺失模块时 200 头早已发出，浏览器拿到的是"半截脚本 + 一行报错文本"，
// 表现为整页白屏的语法错误，而状态码却是 200，监控也看不出来。
func serveClassicAppJS(w http.ResponseWriter, r *http.Request) {
	body, etag, missing := classicAppJS()
	if missing != "" {
		http.Error(w, "js module missing: "+missing, http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/javascript; charset=utf-8")
	// no-cache = 每次都回源校验；配合强 ETag，内容不变时只回 304、不回正文。
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// etagMatches 按 RFC 9110 §13.1.2 的弱比较判断 If-None-Match 是否命中（含 "*"）。
func etagMatches(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimSpace(part)
		if p == "*" {
			return true
		}
		p = strings.TrimPrefix(p, "W/")
		if p == etag {
			return true
		}
	}
	return false
}
