package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPIEndpointToCheck 验证接口→高级拨测的字段映射（复用 probeHTTPAdvanced 的关键）。
func TestAPIEndpointToCheck(t *testing.T) {
	ep := APIEndpoint{
		ID: "ep1", Name: "登录", URL: "https://x/login", Method: "POST",
		Headers: map[string]string{"X-Api-Key": "k"}, Body: `{"u":"a"}`,
		ExpectStatus: 200, ExpectKeyword: "ok", JSONPath: "code", JSONExpect: "0",
	}
	c := ep.toCheck(nil, "") // 无公共头/公共体
	if !c.Advanced || c.Type != "http" {
		t.Fatal("应为 http 高级拨测")
	}
	if c.Target != ep.URL || c.Method != "POST" || c.Body != ep.Body {
		t.Fatal("URL/方法/请求体未正确映射")
	}
	if c.Headers["X-Api-Key"] != "k" || c.ExpectStatus != 200 || c.ExpectKeyword != "ok" {
		t.Fatal("头/状态码/关键字未正确映射")
	}
	if c.JSONPath != "code" || c.JSONExpect != "0" {
		t.Fatal("JSON 断言未正确映射")
	}
}

// TestAPIEndpointToCheckWithCommonHeaders 验证公共请求头合并 + 接口级覆盖。
func TestAPIEndpointToCheckWithCommonHeaders(t *testing.T) {
	ep := APIEndpoint{
		ID: "ep1", Name: "查询", URL: "https://x/query", Method: "GET",
		Headers: map[string]string{"X-Override": "ep-value", "X-Extra": "ep-only"},
	}
	c := ep.toCheck(map[string]string{
		"Authorization": "Bearer shared",
		"X-Override":    "sys-value",
	}, "")
	// 公共头应被继承
	if c.Headers["Authorization"] != "Bearer shared" {
		t.Fatal("公共头 Authorization 未被继承")
	}
	// 接口级应覆盖同名公共头
	if c.Headers["X-Override"] != "ep-value" {
		t.Fatalf("接口级 X-Override 应覆盖公共头，实际=%s", c.Headers["X-Override"])
	}
	// 接口级独有头应保留
	if c.Headers["X-Extra"] != "ep-only" {
		t.Fatal("接口级独有头 X-Extra 丢失")
	}
	// 合并后总共 3 个 key
	if len(c.Headers) != 3 {
		t.Fatalf("合并后应有 3 个 key，实际=%d", len(c.Headers))
	}
}

// TestGenTraceparent 验证 W3C traceparent 头格式(00-<32hex>-<16hex>-01)与随机性。
func TestGenTraceparent(t *testing.T) {
	tp := genTraceparent()
	parts := strings.Split(tp, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || parts[3] != "01" {
		t.Fatalf("traceparent 格式错误: %q", tp)
	}
	if tp == genTraceparent() {
		t.Error("traceparent 应每次随机不同")
	}
}

// TestGraphQLBody 验证 GraphQL 请求体包装：原始查询包成 {"query":...}，已是 JSON 则原样。
func TestGraphQLBody(t *testing.T) {
	if got := graphqlBody("{ user { id } }"); !strings.Contains(got, `"query"`) {
		t.Fatalf("原始查询应包成 query: %s", got)
	}
	if got := graphqlBody(`{"query":"x","variables":{}}`); got != `{"query":"x","variables":{}}` {
		t.Fatalf("已是 JSON 应原样: %s", got)
	}
}

// TestGraphQLCheckErrors 验证 GraphQL 响应校验：有 errors/缺 data/非 JSON 均失败。
func TestGraphQLCheckErrors(t *testing.T) {
	if graphqlCheckErrors([]byte(`{"data":{"user":{"id":1}}}`)) != "" {
		t.Error("正常响应应通过")
	}
	if graphqlCheckErrors([]byte(`{"errors":[{"message":"boom"}]}`)) == "" {
		t.Error("含 errors 应失败")
	}
	if graphqlCheckErrors([]byte(`{"data":null}`)) == "" {
		t.Error("data=null 应失败")
	}
	if graphqlCheckErrors([]byte(`not json`)) == "" {
		t.Error("非 JSON 应失败")
	}
}

// TestWSAcceptKey 用 RFC6455 官方示例验证 Sec-WebSocket-Accept 计算。
func TestWSAcceptKey(t *testing.T) {
	if got := wsAcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("wsAcceptKey 与 RFC6455 示例不符: %s", got)
	}
}

// TestMergeAPIBody 验证系统级公共请求体与接口级请求体的合并规则。
func TestMergeAPIBody(t *testing.T) {
	// 接口体为空 → 用公共体
	if got := mergeAPIBody(`{"appId":"a"}`, ""); got != `{"appId":"a"}` {
		t.Fatalf("接口体空应回退公共体，实际=%s", got)
	}
	// 公共体为空 → 用接口体
	if got := mergeAPIBody("", `{"u":"x"}`); got != `{"u":"x"}` {
		t.Fatalf("公共体空应用接口体，实际=%s", got)
	}
	// 皆为 JSON 对象 → 字段级浅合并，接口体覆盖同名字段（Go 序列化 map 按 key 字母序）
	if got := mergeAPIBody(`{"appId":"a","sign":"s"}`, `{"sign":"ep","user":"u"}`); got != `{"appId":"a","sign":"ep","user":"u"}` {
		t.Fatalf("字段级合并/覆盖错误，实际=%s", got)
	}
	// 非 JSON 对象（表单串）→ 接口体整体覆盖，绝不破坏原文
	if got := mergeAPIBody("a=1&b=2", "c=3"); got != "c=3" {
		t.Fatalf("非 JSON 应接口体优先，实际=%s", got)
	}
}

// TestAPISystemsSecretEncryption 验证 apimon 请求头/请求体的静态加密往返，
// 且深拷贝隔离确保加密副本时绝不污染内存中的明文实时配置（save 路径的最高风险点）。
func TestAPISystemsSecretEncryption(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "test-apimon-key")
	systems := []APISystem{{
		ID: "s1", Name: "订单",
		CommonHeaders: map[string]string{"Authorization": "Bearer secret-token"},
		CommonBody:    `{"appId":"1001"}`,
		Endpoints: []APIEndpoint{{
			ID: "ep1", Name: "下单", URL: "https://x/order",
			Headers: map[string]string{"X-Api-Key": "ep-key"},
			Body:    `{"sku":"A"}`,
		}},
	}}
	// 模拟 save 路径：深拷贝后加密副本
	c := ServerConfig{APISystems: deepCopyAPISystems(systems)}
	encryptConfigSecrets(&c)
	// ① 原始明文未被污染（深拷贝隔离生效）
	if systems[0].CommonHeaders["Authorization"] != "Bearer secret-token" ||
		systems[0].Endpoints[0].Headers["X-Api-Key"] != "ep-key" ||
		systems[0].CommonBody != `{"appId":"1001"}` || systems[0].Endpoints[0].Body != `{"sku":"A"}` {
		t.Fatal("深拷贝失败：加密污染了内存中的明文实时配置")
	}
	// ② 副本敏感字段已加密（enc:v1 或 enc:v2）
	if !isEncryptedSecret(c.APISystems[0].CommonHeaders["Authorization"]) ||
		!isEncryptedSecret(c.APISystems[0].CommonBody) ||
		!isEncryptedSecret(c.APISystems[0].Endpoints[0].Headers["X-Api-Key"]) ||
		!isEncryptedSecret(c.APISystems[0].Endpoints[0].Body) {
		t.Fatal("apimon 敏感字段未被加密")
	}
	// ③ 解密还原明文（load 路径）
	decryptConfigSecrets(&c)
	if c.APISystems[0].CommonHeaders["Authorization"] != "Bearer secret-token" ||
		c.APISystems[0].CommonBody != `{"appId":"1001"}` ||
		c.APISystems[0].Endpoints[0].Headers["X-Api-Key"] != "ep-key" ||
		c.APISystems[0].Endpoints[0].Body != `{"sku":"A"}` {
		t.Fatalf("解密未能还原明文: %+v", c.APISystems[0])
	}
}

// TestPushAPIFormat 验证 pushAPI 生成的 Prometheus 文本含 aiops_api_* 指标族与 api_id/system/endpoint 标签。
func TestPushAPIFormat(t *testing.T) {
	var body string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	v := &vmWriter{httpc: ts.Client()}
	v.pushAPI(ts.URL, []vmAPISample{{
		apiID: "ep1", system: "订单系统", endpoint: "下单",
		ts: 1700000000, ok: true, latencyMs: 42.5, statusCode: 200,
		dnsMs: 1.2, tcpMs: 2.3, tlsMs: 3.4, ttfbMs: 40.1, certDays: 20, respBytes: 512,
	}})

	for _, want := range []string{
		`aiops_api_up{api_id="ep1",system="订单系统",endpoint="下单"} 1 1700000000000`,
		"aiops_api_latency_ms{", "aiops_api_status_code{", "aiops_api_dns_ms{",
		"aiops_api_tcp_ms{", "aiops_api_tls_ms{", "aiops_api_ttfb_ms{",
		"aiops_api_cert_days{", "aiops_api_resp_bytes{",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("推送体缺少 %q\n实际:\n%s", want, body)
		}
	}
}

// TestParseVMAPIExport 验证把 VM /export NDJSON 按时间戳重组为历史点。
func TestParseVMAPIExport(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_api_up","api_id":"ep1"},"values":[1,0],"timestamps":[1700000000000,1700000030000]}
{"metric":{"__name__":"aiops_api_latency_ms","api_id":"ep1"},"values":[40,55],"timestamps":[1700000000000,1700000030000]}
{"metric":{"__name__":"aiops_api_status_code","api_id":"ep1"},"values":[200,500],"timestamps":[1700000000000,1700000030000]}`
	pts := parseVMAPIExport(strings.NewReader(nd))
	if len(pts) != 2 {
		t.Fatalf("应重组出 2 个时间点，实际 %d", len(pts))
	}
	if !pts[0].OK || pts[0].LatencyMs != 40 || pts[0].StatusCode != 200 {
		t.Fatalf("首点重组错误: %+v", pts[0])
	}
	if pts[1].OK || pts[1].LatencyMs != 55 || pts[1].StatusCode != 500 {
		t.Fatalf("次点重组错误: %+v", pts[1])
	}
}

// Staggered API timing series must LOCF — missing dns/tcp must not paint as 0ms.
func TestParseVMAPIExportLOCF(t *testing.T) {
	nd := `{"metric":{"__name__":"aiops_api_up","api_id":"ep1"},"values":[1],"timestamps":[1700000000000]}
{"metric":{"__name__":"aiops_api_latency_ms","api_id":"ep1"},"values":[40,55],"timestamps":[1700000000000,1700000030000]}
{"metric":{"__name__":"aiops_api_dns_ms","api_id":"ep1"},"values":[5],"timestamps":[1700000000000]}
{"metric":{"__name__":"aiops_api_tcp_ms","api_id":"ep1"},"values":[8],"timestamps":[1700000000000]}
{"metric":{"__name__":"aiops_api_tls_ms","api_id":"ep1"},"values":[12],"timestamps":[1700000000000]}
{"metric":{"__name__":"aiops_api_ttfb_ms","api_id":"ep1"},"values":[20],"timestamps":[1700000000000]}
`
	pts := parseVMAPIExport(strings.NewReader(nd))
	if len(pts) != 2 {
		t.Fatalf("应重组出 2 个时间点，实际 %d", len(pts))
	}
	if !pts[0].OK || pts[0].DnsMs != 5 || pts[0].TcpMs != 8 || pts[0].TlsMs != 12 || pts[0].TtfbMs != 20 {
		t.Fatalf("首点分解错误: %+v", pts[0])
	}
	if !pts[1].OK || pts[1].LatencyMs != 55 || pts[1].DnsMs != 5 || pts[1].TcpMs != 8 || pts[1].TlsMs != 12 || pts[1].TtfbMs != 20 {
		t.Fatalf("次点 LOCF 失败（假 0ms/假离线）: %+v", pts[1])
	}
}

// TestAPIRunnerProbe 端到端跑一次接口探测：命中 httptest 服务 → 记录实时状态（OK+延迟）。
func TestAPIRunnerProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	defer srv.Close()

	cfg := newTestConfigStore(t)
	cr := newCheckRunner(cfg, NewStore(), nil, "")
	ar := newAPIRunner(cr, cfg, NewStore(), nil, newVMWriter(cfg)) // VM 未启用 → enqueueAPI 空跑

	sys := APISystem{ID: "s1", Name: "订单系统", Level: "critical"}
	ep := APIEndpoint{
		ID: "ep1", Name: "查询", URL: srv.URL, Method: "GET",
		Headers:      map[string]string{"X-Token": "secret"},
		ExpectStatus: 200, JSONPath: "code", JSONExpect: "0", Enabled: true,
	}
	ar.probe(sys, ep)

	st := ar.statusSnapshot()
	s, ok := st["ep1"]
	if !ok {
		t.Fatal("探测后应记录实时状态")
	}
	if !s.OK {
		t.Fatalf("接口应判为正常，实际 msg=%s", s.Message)
	}
	if s.StatusCode != 200 || s.LatencyMs <= 0 {
		t.Fatalf("状态码/延迟异常: code=%d lat=%f", s.StatusCode, s.LatencyMs)
	}
	if s.System != "订单系统" || s.Name != "查询" {
		t.Fatal("业务系统/接口名未记录")
	}
}
