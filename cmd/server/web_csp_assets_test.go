package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The console ships under `script-src 'self'` with NO 'unsafe-inline'
// (see securityHeadersMiddleware). Under that policy the browser refuses to run
// BOTH inline <script> blocks and inline event-handler attributes.
//
// 这类违规没有任何编译期或运行期信号：页面照常渲染，只是那个按钮/链接点了没反应，
// 而且只在浏览器控制台里留一行 "Refused to execute inline event handler"。
// 之前 sre.js 里工单详情的「关联事件」链接就是这样死了很久没人发现。
// 这条测试直接扫描**打进二进制的那份资源**，让它在 CI 里而不是在用户点击时暴露。

// inlineHandlerRE matches an HTML inline event-handler ATTRIBUTE (onclick="…").
// It deliberately does not match JS property assignment (`btn.onclick = fn`),
// which CSP allows — hence the "not preceded by a dot or identifier char" guard.
var inlineHandlerRE = regexp.MustCompile(`(^|[^.\w])on(abort|blur|change|click|contextmenu|dblclick|drag|dragend|dragover|drop|error|focus|input|keydown|keypress|keyup|load|mousedown|mouseout|mouseover|mouseup|paste|reset|scroll|submit|toggle|wheel)\s*=\s*["'` + "`" + `]`)

// inlineScriptRE matches a <script> tag that has no src= attribute.
var inlineScriptRE = regexp.MustCompile(`(?is)<script(?:\s[^>]*)?>`)
var scriptSrcRE = regexp.MustCompile(`(?is)\ssrc\s*=`)

// htmlCommentRE strips <!-- … --> before scanning: index.html explains in a
// comment why the modules must stay in ONE <script src>, and that prose contains
// a literal "<script>" that is obviously not executed.
var htmlCommentRE = regexp.MustCompile(`(?s)<!--.*?-->`)

func TestClassicConsoleHasNoInlineEventHandlers(t *testing.T) {
	var offenders []string
	err := fs.WalkDir(webFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".js", ".html":
		default:
			return nil
		}
		// The bundled ECharts build is third-party and not our markup.
		if strings.Contains(p, "/vendor/") {
			return nil
		}
		b, readErr := fs.ReadFile(webFS, p)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(b), "\n") {
			// Skip comment lines: several of them legitimately quote the pattern
			// while explaining why it must not be used.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "<!--") {
				continue
			}
			if inlineHandlerRE.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d", filepath.ToSlash(p), i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded console: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("inline event-handler attributes are dead under script-src 'self' — "+
			"render first, then bind with addEventListener (see sre.js bindIncLink):\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

func TestClassicConsoleHasNoInlineScriptBlocks(t *testing.T) {
	b, err := fs.ReadFile(webFS, "web/index.html")
	if err != nil {
		// Path shape differs by embed root; fall back to a walk.
		var found []byte
		_ = fs.WalkDir(webFS, ".", func(p string, d fs.DirEntry, e error) error {
			if e != nil || d.IsDir() || filepath.Base(p) != "index.html" {
				return nil
			}
			found, _ = fs.ReadFile(webFS, p)
			return fs.SkipAll
		})
		if len(found) == 0 {
			t.Skip("index.html not found in embedded FS")
		}
		b = found
	}
	html := htmlCommentRE.ReplaceAllString(string(b), "")
	for _, tag := range inlineScriptRE.FindAllString(html, -1) {
		if !scriptSrcRE.MatchString(tag) {
			t.Fatalf("inline <script> block is blocked by script-src 'self' — "+
				"externalise it like /theme-init.js. Offending tag: %s", tag)
		}
	}
}
