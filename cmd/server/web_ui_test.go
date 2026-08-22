package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 出厂默认界面必须始终是经典版：/v2 是可选的并行入口，不能顺手把 `/` 抢过去。
// 老的 ?ui=v2 / ?ui=legacy 开关早已废弃，这里一并钉住它们不会改变 `/` 的行为。
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
		if strings.Contains(body, `/v2/assets/`) {
			t.Fatalf("%s: `/` must serve the classic shell, not the Vue one", path)
		}
	}
}

// v2Embedded 报告这个二进制里到底有没有 Vue 产物。
//
// 为什么要分支：产物由 `npm run build` 直接写进 web/v2（frontend/vite.config.ts 的
// outDir），而 CI 的 go-gate 任务只跑 go test、根本不装 Node。两种状态都是合法的，
// 但**各自的契约不同**，所以下面按状态分别断言，而不是笼统地跳过。
//
// 直接复用生产代码里的判断，避免测试与 /summary、路由注册各持一份「算不算装了」的
// 定义——那样三者一旦漂移，测试反而会替错误背书。
func v2Embedded() bool { return v2ConsoleEmbedded() }

// 带前端产物时：/v2 可用，且缓存头必须分开——入口每次回源，哈希资源永久不可变。
// 这两条弄反就是「换版后用户看到旧界面」与「每次刷新重下 4MB」。
func TestV2ServedWhenEmbedded(t *testing.T) {
	if !v2Embedded() {
		t.Skip("web/v2 not built into this binary")
	}
	mux := (&Server{}).Routes()

	req := httptest.NewRequest(http.MethodGet, "/v2", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("/v2 status=%d want 307", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/v2/" {
		t.Fatalf("/v2 redirect to %q want /v2/", loc)
	}

	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v2/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/v2/ status=%d want 200", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "/v2/assets/") {
		t.Fatal("/v2/ must serve the Vue shell referencing hashed assets")
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("/v2/ Cache-Control=%q want no-cache (entry must revalidate)", cc)
	}

	// 用**真实存在**的哈希资源验证 immutable：Go 的 ServeContent 在错误路径上会主动
	// 剥掉 Cache-Control（net/http/fs.go），拿一个不存在的文件去断言等于在测 404 的
	// 行为，不是在测缓存策略。
	assets, err := fs.Glob(webFS, "web/v2/assets/*.js")
	if err != nil || len(assets) == 0 {
		t.Fatalf("no hashed assets under web/v2/assets (err=%v)", err)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/"+strings.TrimPrefix(assets[0], "web/"), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("%s status=%d want 200", assets[0], rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("/v2/assets Cache-Control=%q want immutable", cc)
	}
}

// CSP 是 `script-src 'self'` 且**没有** unsafe-inline，所以 Vue 外壳里一旦出现内联
// <script>（vite 插件、内联 runtime、内联主题脚本…）浏览器会直接拒绝执行——页面白屏，
// 而服务端一切正常、日志里什么都没有。构建配置改动很容易无声引入这种脚本，所以这里
// 把"产物里不许有内联脚本"钉死。
func TestV2ShellHasNoInlineScript(t *testing.T) {
	if !v2Embedded() {
		t.Skip("web/v2 not built into this binary")
	}
	shell, err := webFS.ReadFile("web/v2/index.html")
	if err != nil {
		t.Fatalf("read v2 shell: %v", err)
	}
	for _, tag := range inlineScriptTags(string(shell)) {
		t.Fatalf("v2 shell has an inline <script%s> — CSP script-src 'self' will block it", tag)
	}
}

// inlineScriptTags 返回所有「没有 src 属性」的 <script> 开标签属性串。
func inlineScriptTags(html string) []string {
	var out []string
	rest := html
	for {
		i := strings.Index(rest, "<script")
		if i < 0 {
			return out
		}
		rest = rest[i+len("<script"):]
		j := strings.Index(rest, ">")
		if j < 0 {
			return out
		}
		attrs := rest[:j]
		rest = rest[j+1:]
		if !strings.Contains(attrs, "src=") {
			out = append(out, attrs)
		}
	}
}

// 不带前端产物时：/v2 必须干净地 404。此前这里是「注册一条永远 404 的路由」，
// 现在改成条件注册，所以要确认没产物时它确实落到 404 而不是 200 空壳。
func TestV2AbsentWhenNotBuilt(t *testing.T) {
	if v2Embedded() {
		t.Skip("web/v2 is built into this binary")
	}
	mux := (&Server{}).Routes()
	for _, path := range []string{"/v2", "/v2/", "/v2/index.html", "/v2/assets/x.js"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d want 404 (no v2 assets in this build)", path, rr.Code)
		}
	}
}

// /v2 的外壳是登录前就要拿到的静态产物（登录页本身在这个 SPA 里），所以放行；
// 但放行范围必须**只**是外壳——/api/v1 一律不能因为带了 /v2 前缀就绕过会话。
func TestV2ShellIsPublicButAPIStaysGated(t *testing.T) {
	for _, p := range []string{"/v2", "/v2/", "/v2/assets/x.js"} {
		if !isPublicPath(httptest.NewRequest(http.MethodGet, p, nil)) {
			t.Fatalf("%s must be reachable before login (login page lives in the SPA)", p)
		}
	}
	for _, p := range []string{"/api/v1/hosts", "/api/v1/users", "/api/v1/settings"} {
		if isPublicPath(httptest.NewRequest(http.MethodGet, p, nil)) {
			t.Fatalf("%s must stay session-gated", p)
		}
	}
}
