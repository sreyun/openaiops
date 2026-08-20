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
