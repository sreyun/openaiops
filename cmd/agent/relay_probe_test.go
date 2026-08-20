package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 自检必须走 /healthz、带上 relay secret，并如实回报状态码——它是内网机器全体 502 时
// 网关机上唯一的第一手结论。
func TestRelayUpstreamHealthProbesHealthzWithSecret(t *testing.T) {
	var gotPath, gotSecret string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotSecret = r.URL.Path, r.Header.Get("X-Relay-Secret")
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	status, err := relayUpstreamHealth(up.URL+"/", "s3cr3t")
	if err != nil || status != http.StatusOK {
		t.Fatalf("探测应成功: status=%d err=%v", status, err)
	}
	if gotPath != "/healthz" {
		t.Fatalf("探测路径应为 /healthz，实际 %q", gotPath)
	}
	if gotSecret != "s3cr3t" {
		t.Fatalf("探测应带上 relay secret，实际 %q", gotSecret)
	}
}

func TestRelayUpstreamHealthReportsUnreachable(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := up.URL
	up.Close() // 端口已关：等价于"网关机连不上上游"

	if _, err := relayUpstreamHealth(addr, ""); err == nil {
		t.Fatal("上游不可达时必须返回错误，否则自检会谎报链路正常")
	}
}

// 回源必须走 HTTP/1.1：h2 单连接一断，中继上整个内网同时失联；且反代 ALPN 谈成 h2
// 却不能真正承载时，表现正是"中继全 502、网关机自己上报却正常"。
func TestRelayTransportStaysHTTP11(t *testing.T) {
	if relayTransport.ForceAttemptHTTP2 {
		t.Fatal("relayTransport 不应启用 HTTP/2")
	}
}

// 中继回源必须继承上报那条路径的 http→https 升级。两者分裂时的现场是"网关机自己
// 上报正常、内网全体 502(EOF)"，同一份配置、同一个域名，几乎无法自查。
func TestResolveRelayUpstreamFollowsHTTPSUpgrade(t *testing.T) {
	orig := relayUpstreamProbe
	defer func() { relayUpstreamProbe = orig }()
	relayUpstreamProbe = func(raw string) string {
		if raw == "http://aiops.example.com" {
			return "https://aiops.example.com"
		}
		return raw
	}
	if got := resolveRelayUpstream("http://aiops.example.com/"); got != "https://aiops.example.com" {
		t.Fatalf("上游强制 HTTPS 时回源地址必须升级，实际 %q", got)
	}
}

// 探测不升级时不能擅自改写地址（自建 http 上游、内网 :8529 都合法）。
func TestResolveRelayUpstreamKeepsPlainHTTPWhenNoUpgrade(t *testing.T) {
	orig := relayUpstreamProbe
	defer func() { relayUpstreamProbe = orig }()
	relayUpstreamProbe = func(raw string) string { return raw }
	if got := resolveRelayUpstream("http://10.0.0.9:8529/"); got != "http://10.0.0.9:8529" {
		t.Fatalf("不应改写普通 http 上游，实际 %q", got)
	}
}

// 多服务端下"某一个目标缺 token"必须能被单独看见：其余面板一切正常，只有这一个
// 永远没有这台主机，而日志里那条 403 和别的目标混在一起，几乎无从分辨。
func TestServerTargetKeepsItsOwnToken(t *testing.T) {
	targets := []ServerConfig{
		{Server: "https://a.example.com", Token: "tok-a"},
		{Server: "https://b.example.com"},
	}
	a := NewAgent(targets, time.Minute, time.Minute, nil, nil, "h1", "")
	if len(a.targets) != 2 {
		t.Fatalf("目标数不对: %d", len(a.targets))
	}
	if a.targets[0].token != "tok-a" || a.targets[1].token != "" {
		t.Fatalf("token 没有按目标各自绑定: %q / %q", a.targets[0].token, a.targets[1].token)
	}
	// 每个目标各有断路器：一个面板 403 刷屏不能拖住其它面板的上报。
	if a.targets[0].cb == a.targets[1].cb {
		t.Fatal("多个目标共用了同一个断路器")
	}
}

// 中继启动时印的内网安装命令必须能直接粘贴：带真实地址、带 token。
// 占位符命令（http://<本机IP>/install.sh，无 token）抄下去就是一台注册不上的机器。
func TestRelayInstallHintsCarryToken(t *testing.T) {
	hints := relayInstallHints(":8529", "tok-1")
	if len(hints) == 0 {
		t.Fatal("至少要给出一条安装命令")
	}
	for _, h := range hints {
		if !strings.Contains(h, "?token=tok-1") {
			t.Errorf("安装命令缺少 token: %s", h)
		}
		if !strings.Contains(h, ":8529/install.sh") {
			t.Errorf("安装命令缺少中继端口/路径: %s", h)
		}
		if strings.Contains(h, "<") {
			// 有真实网卡地址时不该再出现占位符；没有网卡地址的机器另说（下面那条用例）。
			if len(rankedLocalIPv4s()) > 0 {
				t.Errorf("有可用网卡地址时不应印占位符: %s", h)
			}
		}
	}
	if len(hints) > 3 {
		t.Errorf("最多列 3 条，实际 %d 条", len(hints))
	}
}

// 没有 token 时不能凭空造一个 ?token=，否则命令看着完整、装出来照样 403。
func TestRelayInstallHintsOmitEmptyToken(t *testing.T) {
	for _, h := range relayInstallHints(":8529", "  ") {
		if strings.Contains(h, "token=") {
			t.Errorf("无 token 时不应出现 token 参数: %s", h)
		}
	}
}

// 回源必须把 Host 改成上游的 Host。
//
// 少了这一步，内网 agent 的 Host（中继自己的 192.168.x.x:8529）会被原样送到上游；
// 按名字分流的 nginx 认不出这个 Host，回给内网机器一页 HTML 错误页。而 /install.sh
// 与 /dl 走中继自己构造的请求、Host 天然正确——于是现场是"装得上、连不上"。
func TestRelayProxyRewritesHostToUpstream(t *testing.T) {
	var gotHost string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	target, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := newRelayProxy(target, 0)

	req := httptest.NewRequest(http.MethodPost, "http://192.168.30.114:8529/api/v1/agent/report", nil)
	req.Host = "192.168.30.114:8529" // 内网 agent 眼里的地址
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	if gotHost != target.Host {
		t.Fatalf("上游收到的 Host 应是 %q，实际 %q（按名字分流的反代会因此返回错误页）", target.Host, gotHost)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("回源应成功，实际 %d", rr.Code)
	}
}

// 内网机器只输一句 curl http://网关:8529/install.sh 时，中继要把自己的 token 补上：
// 否则装完注册被 403 拒，症状是"装得上、面板里看不到"。
func TestRelayInstallScriptFillsMissingToken(t *testing.T) {
	var gotToken string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("token")
		_, _ = w.Write([]byte("#!/bin/sh\nSERVER=\"https://panel.example.com\"\n"))
	}))
	defer up.Close()

	req := httptest.NewRequest(http.MethodGet, "http://192.168.30.114:8529/install.sh", nil)
	req.Host = "192.168.30.114:8529"
	rr := httptest.NewRecorder()
	serveRelayInstallScript(rr, req, up.URL, "", "gw-token")

	if gotToken != "gw-token" {
		t.Fatalf("中继应补上自己的 token，上游实际收到 %q", gotToken)
	}
	if body := rr.Body.String(); !strings.Contains(body, "http://192.168.30.114:8529") {
		t.Fatalf("SERVER= 应被改写成中继地址，实际: %s", body)
	}
}

// 请求自带 token 时以自带的为准，中继不得覆盖（多面板/多 token 场景会被改错）。
func TestRelayInstallScriptKeepsExplicitToken(t *testing.T) {
	var gotToken string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("token")
		_, _ = w.Write([]byte("#!/bin/sh\n"))
	}))
	defer up.Close()

	req := httptest.NewRequest(http.MethodGet, "http://192.168.30.114:8529/install.sh?token=explicit", nil)
	req.Host = "192.168.30.114:8529"
	serveRelayInstallScript(httptest.NewRecorder(), req, up.URL, "", "gw-token")

	if gotToken != "explicit" {
		t.Fatalf("自带 token 必须原样透传，实际 %q", gotToken)
	}
}

// 中继自己没有 token 时不能凭空造：那说明这套部署本就没打算发 token。
func TestRelayInstallScriptWithoutTokenStaysEmpty(t *testing.T) {
	var gotToken string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.URL.Query().Get("token")
		_, _ = w.Write([]byte("#!/bin/sh\n"))
	}))
	defer up.Close()

	req := httptest.NewRequest(http.MethodGet, "http://192.168.30.114:8529/install.sh", nil)
	req.Host = "192.168.30.114:8529"
	serveRelayInstallScript(httptest.NewRecorder(), req, up.URL, "", "")

	if gotToken != "" {
		t.Fatalf("中继无 token 时不应补 token，实际 %q", gotToken)
	}
}

// 只读模块的 lines 参数必须收敛成纯数字且有上限。
//
// 不做数字校验时，Windows 分支把它拼进 "cmd /c wevtutil …"——lines="1 & whoami" 就是
// 一次任意命令执行，而 journal_recent 标注的是**只读**。不做上限时，
// journalctl -n 999999999 会把整个 journal 读进内存再回传，足以撑爆小内存机器。
func TestModuleJournalLinesIsClampedNumeric(t *testing.T) {
	cases := map[string]string{
		"":               "80",
		"abc":            "80",
		"0":              "80",
		"-5":             "80",
		"1 & whoami":     "80",
		"200":            "200",
		"999999999":      "5000",
		"  120  ":        "120",
		"5000; rm -rf /": "80",
	}
	for in, want := range cases {
		if got := moduleJournalLines(in); got != want {
			t.Errorf("moduleJournalLines(%q) = %q, want %q", in, got, want)
		}
	}
}

// 输出上限：超出后必须截断并留下明确标记，而不是把几百 MB 收进内存再回传。
func TestCappedBufferTruncatesWithNotice(t *testing.T) {
	c := &cappedBuffer{max: 16}
	n, err := c.Write([]byte(strings.Repeat("x", 100)))
	if err != nil || n != 100 {
		t.Fatalf("写入方必须看到全量写入（否则命令会收到 EPIPE 死掉）: n=%d err=%v", n, err)
	}
	out := string(c.Bytes())
	if len(out) <= 16 && !strings.Contains(out, "截断") {
		t.Fatalf("截断标记缺失: %q", out)
	}
	if strings.Count(out, "x") != 16 {
		t.Fatalf("上限未生效，实际保留 %d 字节", strings.Count(out, "x"))
	}
	// 二次取用不应重复追加标记。
	if a, b := string(c.Bytes()), string(c.Bytes()); a != b {
		t.Fatal("重复调用 Bytes() 追加了多次截断标记")
	}
}

// Agent 自己的 config.yaml 里是安装 token 与 relay_secret：只读巡检读走它，等于拿到
// 能让任意机器注册进面板的凭据。这条比 /etc/shadow 更现实，必须挡住。
func TestAgentDeniedPathCoversOwnSecretsAndEquivalents(t *testing.T) {
	for _, p := range []string{
		"/opt/aiops-agent/config.yaml",
		"/root/.aiops-agent/agent_state.json",
		"/etc/./shadow",
		"/proc/self/environ",
		"/proc/1234/environ",
		"/home/u/.ssh/id_rsa",
		"/tmp/backup/server.key",
		`C:\Windows\..\Windows\System32\config\SAM`,
	} {
		if !agentDeniedPath(p) {
			t.Errorf("敏感路径未被拦截: %s", p)
		}
	}
	for _, p := range []string{"/var/log/messages", "/etc/hosts", "/opt/app/application.yaml"} {
		if agentDeniedPath(p) {
			t.Errorf("普通路径被误伤: %s", p)
		}
	}
}

// 改写 Host 之后要把"改写前的地址"留在转发头里。
//
// 少了这一步，经中继打开面板的人一按保存就失败：面板拿 Origin（中继地址）与 Host
// （已被改成上游域名）比对，判定为跨站写请求。X-Forwarded-Host 是标准写法；上游若还有
// 一层 nginx 会把它覆盖成上游域名，所以另外再留一条 nginx 不会碰的 X-AIOps-Client-Host。
func TestRelayProxyPreservesClientHostForOriginCheck(t *testing.T) {
	var xfh, aiops string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xfh = r.Header.Get("X-Forwarded-Host")
		aiops = r.Header.Get("X-AIOps-Client-Host")
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	target, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://192.168.30.114:8529/api/v1/hosts/folder/batch", nil)
	req.Host = "192.168.30.114:8529"
	newRelayProxy(target, 0).ServeHTTP(httptest.NewRecorder(), req)

	if xfh != "192.168.30.114:8529" {
		t.Errorf("X-Forwarded-Host 应是中继地址，实际 %q", xfh)
	}
	if aiops != "192.168.30.114:8529" {
		t.Errorf("X-AIOps-Client-Host 应是中继地址，实际 %q", aiops)
	}
}
