package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutesRegister ensures ServeMux patterns do not conflict at registration
// time (Go 1.22+ panics on overlapping wildcards, e.g. {id}/preflight vs executions/{id}).
func TestRoutesRegister(t *testing.T) {
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("Routes() panicked (likely ServeMux pattern conflict): %v", rec)
		}
	}()
	(&Server{}).Routes()
}

// 不存在的 API 路径必须回 404，且**任何方法都一样**。
//
// 这条不是形式主义：`GET /` 是根子树模式，在 Go 的 ServeMux 里匹配任意路径。没有
// `/api/` 兜底时，一个 POST 到不存在的接口会被判成"路径匹配、方法不符"→ 405，于是
// "这台面板没有这个接口"被报成"方法不允许"，排查方向完全指错。真实反馈就是这么来的：
// 老部署（compose 里还是缓存的旧 latest 镜像）前端有新按钮、后端没有新路由，用户看到
// 的是一句 "修改失败 HTTP 405"。
func TestUnknownAPIPathIs404ForEveryMethod(t *testing.T) {
	mux := (&Server{}).Routes()
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/v1/definitely-not-a-route/x", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s 未知 API 路径回了 %d，期望 404（405 会把'版本过旧'误报成'方法不允许'）", method, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("%s 的 404 不是 JSON（Content-Type=%q）——前端 r.json() 拿不到 error 字段", method, ct)
		}
	}
}

// 真实注册的路由不能被 /api/ 兜底吃掉：`/api/` 比具体路由更宽，Go 选最具体的那条。
func TestRegisteredAPIRoutesStillWinOverCatchAll(t *testing.T) {
	srv, _ := newTestServer(t)
	mux := srv.Routes()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code == http.StatusNotFound {
		t.Fatalf("GET /api/v1/hosts 被 /api/ 兜底吃掉了：%d %s", rr.Code, rr.Body.String())
	}
}

// 兜底要分清"方法用错"和"接口不存在"：前者仍是 405 且必须带 Allow，后者才是 404。
// 混成一个的话，运维拿到"接口不存在"却其实只是方法写错了，方向照样是歪的。
func TestWrongMethodOnRealPathStillReturns405WithAllow(t *testing.T) {
	srv, _ := newTestServer(t)
	mux := srv.Routes()
	// POST /api/v1/hosts/folder/batch 是真实路由；换成 PUT 就该是 405 + Allow: POST。
	req := httptest.NewRequest(http.MethodPut, "/api/v1/hosts/folder/batch", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("真实路径上的错方法应回 405，得到 %d: %s", rr.Code, rr.Body.String())
	}
	if allow := rr.Header().Get("Allow"); !strings.Contains(allow, http.MethodPost) {
		t.Errorf("405 必须带 Allow 指出可用方法，实际 Allow=%q", allow)
	}
}

// 兜底还得盖住 /api/ **之外**的路径。
//
// 上面那条只挡住了 /api/ 开头的。写方法打到任何其它未注册路径时，Go 会拿根子树模式
// `GET /` 去匹配，判成"路径匹配、方法不符"，回一个**纯文本 405 Method Not Allowed**——
// 既不是 JSON，也没说清是"接口不存在"，正是最误导人的那种回答。
// 这个坑真实发生过：新版页面（SW 缓存的）打到旧二进制上，用户看到的就是这句纯文本 405。
func TestUnknownNonAPIWritePathIs404JSON(t *testing.T) {
	mux := (&Server{}).Routes()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(method, "/totally/bogus", nil))
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s /totally/bogus 回了 %d，期望 404（纯文本 405 会把'接口不存在'说成'方法不允许'）", method, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("%s /totally/bogus 的 Content-Type = %q，期望 JSON", method, ct)
		}
		// 报错里要带上正在跑的版本号，用户才有得核对。
		if !strings.Contains(rr.Body.String(), appVersion) {
			t.Errorf("%s /totally/bogus 的响应没带服务端版本号：%s", method, rr.Body.String())
		}
	}
	// GET 不受影响：根路径仍旧交给面板首页（这条兜底只注册写方法）。
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code == http.StatusNotFound {
		t.Error("GET / 被兜底抢走了，面板首页打不开")
	}
	// 真实存在的非 /api 写接口是更具体的模式，不能被根兜底误伤。
	// 这里只问"路由到哪条模式"，不真的调处理器——端口转发在空 Server 上跑不起来，
	// 而要断言的本来就是匹配结果本身。
	if sm, ok := mux.(*http.ServeMux); ok {
		if _, pattern := sm.Handler(httptest.NewRequest(http.MethodPost, "/proxy/h1/8080/x", nil)); pattern == "POST /" {
			t.Error("已注册的 POST /proxy/... 被根兜底抢走了")
		}
	}
}
