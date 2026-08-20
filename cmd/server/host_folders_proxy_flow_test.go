package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 反代场景下的「改分组」端到端回归。
//
// 走的是 httpHandler() —— 与生产完全同一条中间件链，而不是测试里自己拼的短链。
// 现场反馈的"改分组一直失败"根本没走到 handler：面板开在 https://a.bc.com:8443，
// nginx 按最常见的 `proxy_set_header Host $host` 转发（$host 不含端口），服务端看到的
// r.Host 只剩 a.bc.com，浏览器发来的 Origin 却是带 :8443 的 —— csrfOrigin 逐字节比较
// 对不上，所有写请求 403。读接口全是 GET 不过这一关，于是"界面一切正常，就是保存不了"。

// proxiedPanel 模拟一台 nginx 后面的面板：可以选择代理转不转 X-Forwarded-*。
type proxiedPanel struct {
	srv     *Server
	token   string
	host    string            // 服务端看到的 Host（代理写什么就是什么）
	origin  string            // 浏览器地址栏对应的 Origin
	headers map[string]string // 代理额外转发的头
}

func (p proxiedPanel) do(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "http://"+p.host+path, bytes.NewReader(b))
	req.Host = p.host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip") // 浏览器一律带，链上有 gzip 中间件
	req.Header.Set("X-Requested-With", "AIOps-Console")
	if p.origin != "" {
		req.Header.Set("Origin", p.origin)
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: p.token})
	rr := httptest.NewRecorder()
	p.srv.httpHandler().ServeHTTP(rr, req)

	raw := rr.Body.Bytes()
	if strings.EqualFold(rr.Header().Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("响应声明 gzip 却解不开：%v", err)
		}
		if raw, err = io.ReadAll(zr); err != nil {
			t.Fatalf("gzip 响应体损坏：%v", err)
		}
	}
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s 的响应不是 JSON：%v（%q）", method, path, err, string(raw))
		}
	}
	return rr.Code, out
}

func newProxiedPanel(t *testing.T, host, origin string, headers map[string]string) proxiedPanel {
	t.Helper()
	srv, _ := newTestServer(t)
	for _, id := range []string{"h1", "h2", "h3"} {
		h := srv.store.RegisterHost(id, id, "fp-"+id)
		h.LastSeen = time.Now().Unix()
	}
	return proxiedPanel{srv: srv, token: srv.auth.issueSession("admin"), host: host, origin: origin, headers: headers}
}

// 场景一：nginx `proxy_set_header Host $host` —— 端口被抹掉（最常见）。
func TestFolderFlowsBehindPortStrippingProxy(t *testing.T) {
	p := newProxiedPanel(t, "a.bc.com", "https://a.bc.com:8443", map[string]string{
		"X-Forwarded-Proto": "https",
		"X-Forwarded-Port":  "8443",
		"X-Forwarded-For":   "203.0.113.9",
	})
	assertFolderFlowsWork(t, p)
}

// 场景二：location 里没写 proxy_set_header Host —— nginx 默认把 Host 设成 proxy_pass 的
// 目标（127.0.0.1:8529），只有 X-Forwarded-Host 还留着真实域名。
func TestFolderFlowsBehindHostRewritingProxy(t *testing.T) {
	p := newProxiedPanel(t, "127.0.0.1:8529", "https://panel.example.com", map[string]string{
		"X-Forwarded-Host":  "panel.example.com",
		"X-Forwarded-Proto": "https",
	})
	assertFolderFlowsWork(t, p)
}

// 场景三：RFC 7239 Forwarded（Traefik / 部分 CDN 只发这一个头）。
func TestFolderFlowsBehindRFC7239Proxy(t *testing.T) {
	p := newProxiedPanel(t, "10.0.0.7:8529", "https://panel.example.com:8443", map[string]string{
		"Forwarded": `for=203.0.113.9;host=panel.example.com:8443;proto=https`,
	})
	assertFolderFlowsWork(t, p)
}

// 场景四：直连面板（没有反代），端口一致 —— 不能因为放宽了端口就把基本情形改坏。
func TestFolderFlowsDirectNoProxy(t *testing.T) {
	p := newProxiedPanel(t, "192.168.1.10:8529", "http://192.168.1.10:8529", nil)
	assertFolderFlowsWork(t, p)
}

// 界面上真正会点的三条路径，一条都不能少：
// 新建分组 → 单台改分组 → 批量改分组（含改回未分组）。
func assertFolderFlowsWork(t *testing.T, p proxiedPanel) {
	t.Helper()

	code, body := p.do(t, http.MethodPost, "/api/v1/host-folders", map[string]string{"name": "数据库", "parent_id": ""})
	if code != http.StatusOK {
		t.Fatalf("新建分组失败：%d %v", code, body)
	}
	folder, _ := body["folder"].(map[string]any)
	fid, _ := folder["id"].(string)
	if fid == "" {
		t.Fatalf("新建分组没返回 id：%v", body)
	}

	if code, body := p.do(t, http.MethodPost, "/api/v1/hosts/h1/folder", map[string]string{"folder_id": fid}); code != http.StatusOK {
		t.Fatalf("单台改分组失败：%d %v", code, body)
	}
	if code, body := p.do(t, http.MethodPost, "/api/v1/hosts/folder/batch",
		map[string]any{"host_ids": []string{"h2", "h3"}, "folder_id": fid}); code != http.StatusOK {
		t.Fatalf("批量改分组失败：%d %v", code, body)
	}
	if code, body := p.do(t, http.MethodPost, "/api/v1/hosts/folder/batch",
		map[string]any{"host_ids": []string{"h3"}, "folder_id": HostFolderUngroupedID}); code != http.StatusOK {
		t.Fatalf("批量改回未分组失败：%d %v", code, body)
	}
	// 分组重命名（界面上"整组改名"走的是这条）。
	if code, body := p.do(t, http.MethodPatch, "/api/v1/host-folders/"+fid, map[string]string{"name": "数据库-新"}); code != http.StatusOK {
		t.Fatalf("分组改名失败：%d %v", code, body)
	}

	_, assign := p.srv.cfg.hostFoldersSnapshot()
	for id, want := range map[string]string{"h1": fid, "h2": fid, "h3": HostFolderUngroupedID} {
		if assign[id] != want {
			t.Errorf("%s 的归属是 %q，期望 %q", id, assign[id], want)
		}
	}
}

// 放宽端口不等于放弃 CSRF：主机名不同一律拒绝，并且报错要说得清两边各自看到的地址。
func TestCrossSiteFolderWriteRejectedWithActionableError(t *testing.T) {
	p := newProxiedPanel(t, "a.bc.com", "https://evil.example.com", nil)
	code, body := p.do(t, http.MethodPost, "/api/v1/hosts/folder/batch",
		map[string]any{"host_ids": []string{"h1"}, "folder_id": ""})
	if code != http.StatusForbidden {
		t.Fatalf("跨站写请求必须被拒绝，实际 %d %v", code, body)
	}
	if body["code"] != "origin_not_allowed" {
		t.Errorf("缺少可判定的 code：%v", body["code"])
	}
	msg, _ := body["error"].(string)
	for _, want := range []string{"evil.example.com", "a.bc.com"} {
		if !strings.Contains(msg, want) {
			t.Errorf("报错里应出现 %q，实际：%s", want, msg)
		}
	}
}

// ---- originAllowed 的单元级边界 ----

func TestOriginIsSelfBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		origin  string
		headers map[string]string
		public  string
		want    bool
	}{
		{name: "端口被抹掉：同主机名放行", host: "a.bc.com", origin: "https://a.bc.com:8443", want: true},
		{name: "端口被抹掉：主机名不同仍拒绝", host: "a.bc.com", origin: "https://evil.com:8443", want: false},
		{name: "端口已知且一致", host: "a.bc.com:8529", origin: "http://a.bc.com:8529", want: true},
		{name: "端口已知且不一致：拒绝", host: "127.0.0.1:18529", origin: "http://127.0.0.1:8529", want: false},
		{
			name: "代理转了 X-Forwarded-Port：按它比，一致放行",
			host: "a.bc.com", origin: "https://a.bc.com:8443",
			headers: map[string]string{"X-Forwarded-Port": "8443"}, want: true,
		},
		{
			name: "代理转了 X-Forwarded-Port：不一致拒绝",
			host: "a.bc.com", origin: "https://a.bc.com:3000",
			headers: map[string]string{"X-Forwarded-Port": "8443"}, want: false,
		},
		{
			name: "X-Forwarded-Host 带端口，与 Origin 一致",
			host: "127.0.0.1:8529", origin: "https://a.bc.com:8443",
			headers: map[string]string{"X-Forwarded-Host": "a.bc.com:8443"}, want: true,
		},
		{name: "public_url 兜底", host: "127.0.0.1:8529", origin: "https://a.bc.com", public: "https://a.bc.com", want: true},
		{name: "IPv6 直连", host: "[2001:db8::1]:8529", origin: "http://[2001:db8::1]:8529", want: true},
		{name: "IPv6 端口被抹掉", host: "[2001:db8::1]", origin: "http://[2001:db8::1]:8443", want: true},
		{name: "大小写与末尾点不影响判定", host: "A.BC.com.", origin: "https://a.bc.com:8443", want: true},
		{name: "Origin 是 null（沙箱 iframe）一律拒绝", host: "a.bc.com", origin: "null", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{cfg: &ConfigStore{cfg: ServerConfig{PublicURL: tc.public}}}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/folder/batch", nil)
			req.Host = tc.host
			req.Header.Set("Origin", tc.origin)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if got := srv.originAllowed(req); got != tc.want {
				t.Fatalf("originAllowed=%v，期望 %v（host=%s origin=%s）", got, tc.want, tc.host, tc.origin)
			}
		})
	}
}

// 没有 Origin 只有 Referer 的写请求（部分浏览器的表单导航）走同一套判断。
func TestRefererFallbackBehindProxy(t *testing.T) {
	srv := &Server{cfg: &ConfigStore{}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hosts/folder/batch", nil)
	req.Host = "panel.example.com"
	req.Header.Set("Referer", "https://panel.example.com:8443/v2/")
	if !srv.originAllowed(req) {
		t.Fatal("Referer 兜底在端口被抹掉时也要认")
	}
	req.Header.Set("Referer", "https://evil.example.com/x")
	if srv.originAllowed(req) {
		t.Fatal("Referer 指向别的站点时必须拒绝")
	}
}

// 反代抹掉端口时，控制台的**写操作一条都不能**被来源校验挡下。
//
// "改分组失败"只是用户最先撞上的那一个：故障在中间件层，波及所有写接口（保存配置、
// 认领告警、写注解、删主机…）。这条用例把各模块的代表性写请求都放进同一条真实中间件链里，
// 确保这类"整片写操作被 403"的故障不会换个模块再来一次。
func TestConsoleWritesBehindPortStrippingProxy(t *testing.T) {
	p := newProxiedPanel(t, "a.bc.com", "https://a.bc.com:8443", map[string]string{
		"X-Forwarded-Proto": "https",
	})
	code, body := p.do(t, http.MethodPost, "/api/v1/host-folders", map[string]string{"name": "业务A"})
	if code != http.StatusOK {
		t.Fatalf("新建分组失败：%d %v", code, body)
	}
	folder, _ := body["folder"].(map[string]any)
	fid, _ := folder["id"].(string)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		wantOK bool // true = 必须 200；false = 只要求"不是被来源校验挡下的"
	}{
		{"分组改名", http.MethodPatch, "/api/v1/host-folders/" + fid, map[string]string{"name": "业务A-新"}, true},
		{"保存分组树", http.MethodPut, "/api/v1/host-folders",
			map[string]any{"folders": []map[string]any{{"id": fid, "name": "业务A-新"}}}, true},
		{"单台改分组", http.MethodPost, "/api/v1/hosts/h1/folder", map[string]string{"folder_id": fid}, true},
		{"批量改分组", http.MethodPost, "/api/v1/hosts/folder/batch",
			map[string]any{"host_ids": []string{"h1", "h2"}, "folder_id": fid}, true},
		{"旧版分类接口", http.MethodPost, "/api/v1/hosts/h1/category", map[string]string{"category": "业务B"}, true},
		{"资源注解", http.MethodPut, "/api/v1/resource-notes/host:h1", map[string]string{"owner": "ops"}, true},
		{"删除主机", http.MethodDelete, "/api/v1/hosts/h3", nil, true},
		{"认领告警", http.MethodPost, "/api/v1/alerts/ack", map[string]string{"id": "probe"}, false},
		{"新建拨测", http.MethodPost, "/api/v1/checks", map[string]string{"name": "probe"}, false},
		{"保存系统配置", http.MethodPost, "/api/v1/config", map[string]any{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := p.do(t, tc.method, tc.path, tc.body)
			if body["code"] == "origin_not_allowed" {
				t.Fatalf("%s %s 被来源校验挡下（反代抹端口场景）：%v", tc.method, tc.path, body["error"])
			}
			if code == http.StatusUnauthorized {
				t.Fatalf("%s %s 返回 401，会话在链上丢了：%v", tc.method, tc.path, body)
			}
			if tc.wantOK && code != http.StatusOK {
				t.Fatalf("%s %s 期望 200，实际 %d %v", tc.method, tc.path, code, body)
			}
		})
	}
}

// 经网关中继打开面板：中继把 Host 改成了上游域名，上游前面的 nginx 又把
// X-Forwarded-Host 覆盖成自己 —— 只剩中继留下的 X-AIOps-Client-Host 能证明
// "浏览器眼中的地址"。这条链路上改分组同样必须成功。
func TestFolderWritesThroughRelayGateway(t *testing.T) {
	p := newProxiedPanel(t, "panel.example.com", "http://192.168.30.114:8529", map[string]string{
		"X-Forwarded-Host":    "panel.example.com", // 上游 nginx 覆盖后的值
		"X-AIOps-Client-Host": "192.168.30.114:8529",
	})
	assertFolderFlowsWork(t, p)
}
