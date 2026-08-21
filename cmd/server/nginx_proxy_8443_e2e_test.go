package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 真·nginx 端到端（第二组）：面板开在**非标准端口 + TLS 在 nginx 上终止**，
// 也就是现场那句"常规 80/443 没问题，一换成 :8443 就出事"的部署形态。
//
// 只在 AIOPS_NGINX_E2E=1 且机器上装了 nginx 时跑（CI 的 go-gate 里没有 nginx）：
//
//	AIOPS_NGINX_E2E=1 go test ./cmd/server -run TestNginxE2E -v

// startNginxE2E 起一台真实 nginx。conf 里的 __DIR__ 会被替换成 nginx 的工作目录
// （pid / 日志 / 各类临时目录都在里面），返回该目录；退出时负责收干净。
func startNginxE2E(t *testing.T, conf string, ports ...string) string {
	t.Helper()
	if _, err := exec.LookPath("nginx"); err != nil {
		t.Skip("nginx not installed")
	}
	if os.Getenv("AIOPS_NGINX_E2E") != "1" {
		t.Skip("set AIOPS_NGINX_E2E=1")
	}
	dir := t.TempDir()
	for _, d := range []string{"logs", "tmp", "client_body", "proxy", "fastcgi", "uwsgi", "scgi"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	confPath := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(confPath, []byte(strings.ReplaceAll(conf, "__DIR__", dir)), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("nginx", "-c", confPath, "-p", dir)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("nginx 起不来：%v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("nginx", "-c", confPath, "-p", dir, "-s", "quit").Run()
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
		if b, err := os.ReadFile(filepath.Join(dir, "logs", "error.log")); err == nil && len(b) > 0 {
			t.Logf("nginx error.log:\n%s", b)
		}
	})
	for _, p := range ports {
		deadline := time.Now().Add(10 * time.Second)
		for {
			c, err := net.DialTimeout("tcp", "127.0.0.1:"+p, 300*time.Millisecond)
			if err == nil {
				_ = c.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("nginx 没在 10s 内监听 %s", p)
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	return dir
}

// selfSignedForE2E 生成一张 127.0.0.1 的自签证书。
func selfSignedForE2E(t *testing.T, dir string) (crt, key string) {
	t.Helper()
	crt, key = filepath.Join(dir, "e2e.crt"), filepath.Join(dir, "e2e.key")
	gen := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
		"-keyout", key, "-out", crt, "-days", "2", "-subj", "/CN=127.0.0.1",
		"-addext", "subjectAltName=IP:127.0.0.1")
	if err := gen.Run(); err != nil {
		t.Skipf("openssl 不可用：%v", err)
	}
	return crt, key
}

// 面板开在 https://127.0.0.1:18444（TLS 由 nginx 终止），两种最常见的 Host 写法各跑一遍。
//
// 这条用例钉住的是一个只在**非标准端口**上出现的缺陷：nginx 按 `Host $http_host`
// 转发（端口保留）时，服务端看到的 scheme 是 http（r.TLS 为 nil、trust_proxy 默认关）、
// Host 是 127.0.0.1:18444，于是拼出 http://127.0.0.1:18444 —— 一个只收 TLS 的端口上的
// 明文地址。安装命令 curl 不通、install.sh 里的 SERVER= 让 Agent 永远注册不上。
// 端口是 443 时 Host 里根本没有端口，走的是"隐式 80 → https"那条分支，一切正常。
func TestNginxE2ETLS8443InstallAddress(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.store.RegisterHost("h1", "h1", "fp-h1")
	f, err := srv.cfg.addHostFolder("", "数据库")
	if err != nil {
		t.Fatal(err)
	}
	token := srv.auth.issueSession("admin")
	backend := httptest.NewServer(srv.httpHandler())
	defer backend.Close()
	_, upPort, _ := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))

	certDir := t.TempDir()
	crt, key := selfSignedForE2E(t, certDir)
	conf := fmt.Sprintf(`
daemon off;
pid __DIR__/nginx.pid;
error_log __DIR__/logs/error.log warn;
events { worker_connections 64; }
http {
  access_log off;
  client_body_temp_path __DIR__/client_body;
  proxy_temp_path __DIR__/proxy;
  fastcgi_temp_path __DIR__/fastcgi;
  uwsgi_temp_path __DIR__/uwsgi;
  scgi_temp_path __DIR__/scgi;
  server {                                  # A：Host $host —— 端口被抹掉
    listen 127.0.0.1:18443 ssl;
    ssl_certificate %[2]s; ssl_certificate_key %[3]s;
    location / {
      proxy_pass http://127.0.0.1:%[1]s;
      proxy_http_version 1.1;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
    }
  }
  server {                                  # B：Host $http_host —— 端口保留
    listen 127.0.0.1:18444 ssl;
    ssl_certificate %[2]s; ssl_certificate_key %[3]s;
    location / {
      proxy_pass http://127.0.0.1:%[1]s;
      proxy_http_version 1.1;
      proxy_set_header Host $http_host;
      proxy_set_header X-Forwarded-Proto $scheme;
    }
  }
}
`, upPort, crt, key)
	startNginxE2E(t, conf, "18443", "18444")

	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	for _, tc := range []struct {
		port, label string
	}{
		{"18443", "Host $host（端口被抹）"},
		{"18444", "Host $http_host（端口保留）"},
	} {
		origin := "https://127.0.0.1:" + tc.port

		// 1) 面板拉安装命令：协议必须是 https，端口必须是地址栏里那个。
		req, _ := http.NewRequest(http.MethodGet, origin+"/api/v1/install/info", nil)
		req.Header.Set("Referer", origin+"/v2/") // 同源 GET 不带 Origin，端口只能靠 Referer
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("[%s] /install/info: %v", tc.label, err)
		}
		var info struct {
			ServerURL string `json:"server_url"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&info)
		resp.Body.Close()
		if info.ServerURL != origin {
			t.Errorf("[%s] /install/info server_url = %q, want %q", tc.label, info.ServerURL, origin)
		}

		// 2) 目标机上 curl 到的 install.sh：SERVER= 必须同样正确。
		//    ?port= 是面板拼进安装命令的兜底（curl 那一跳没有 Referer）。
		req2, _ := http.NewRequest(http.MethodGet,
			origin+"/install.sh?token="+srv.cfg.InstallToken()+"&port="+tc.port, nil)
		resp2, err := cl.Do(req2)
		if err != nil {
			t.Fatalf("[%s] /install.sh: %v", tc.label, err)
		}
		body, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		want := `SERVER="` + origin + `"`
		if !strings.Contains(string(body), want) {
			line := "（没找到 SERVER= 行）"
			for _, l := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(strings.TrimSpace(l), "SERVER=") {
					line = strings.TrimSpace(l)
					break
				}
			}
			t.Errorf("[%s] install.sh 里是 %s，want %s", tc.label, line, want)
		}

		// 3) 写操作（改分组）在两种写法下都必须过来源校验。
		b, _ := json.Marshal(map[string]any{"host_ids": []string{"h1"}, "folder_id": f.ID})
		req3, _ := http.NewRequest(http.MethodPost, origin+"/api/v1/hosts/folder/batch", strings.NewReader(string(b)))
		req3.Header.Set("Content-Type", "application/json")
		req3.Header.Set("Origin", origin)
		req3.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		resp3, err := cl.Do(req3)
		if err != nil {
			t.Fatalf("[%s] 改分组: %v", tc.label, err)
		}
		raw, _ := io.ReadAll(resp3.Body)
		resp3.Body.Close()
		if resp3.StatusCode != http.StatusOK {
			t.Errorf("[%s] 批量改分组 → %d %s", tc.label, resp3.StatusCode, strings.TrimSpace(string(raw)))
		}
	}
}

// 真·nginx + **默认的 proxy_request_buffering on**：Agent 的 tx 上行流会被整包缓冲
// 到命令结束才转发上游。这正是现场"终端连不上、Agent 自动升级永远失败"的成因。
//
// 修好之后这条链路必须做到两件事：升级/剧本照常跑完（拿到完整输出与退出码），
// 并留下一条指向 nginx 配置的判定——而不是一句把矛头指向 Agent 的"未接单"。
func TestNginxE2EBufferedUpstreamStillCompletes(t *testing.T) {
	edgeProxyDiagState.reset() // 包级状态：上一条用例留下的判定会让下面的计数假红
	oldPickup := execPickupTimeout
	execPickupTimeout = 500 * time.Millisecond
	defer func() { execPickupTimeout = oldPickup }()

	srv, _ := newTestServer(t)
	const hostID, fp = "h-nginx", "fp-nginx"
	srv.store.RegisterHost(hostID, "web-01", fp)
	backend := httptest.NewServer(srv.httpHandler())
	defer backend.Close()
	_, upPort, _ := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))

	conf := fmt.Sprintf(`
daemon off;
pid __DIR__/nginx.pid;
error_log __DIR__/logs/error.log warn;
events { worker_connections 64; }
http {
  access_log off;
  client_body_temp_path __DIR__/client_body;
  proxy_temp_path __DIR__/proxy;
  fastcgi_temp_path __DIR__/fastcgi;
  uwsgi_temp_path __DIR__/uwsgi;
  scgi_temp_path __DIR__/scgi;
  server {
    listen 127.0.0.1:18445;
    location / {
      proxy_pass http://127.0.0.1:%[1]s;
      proxy_http_version 1.1;
      proxy_set_header Host $http_host;
      # 刻意**不写** proxy_request_buffering off —— 复现现场那份缺配置的反代。
    }
  }
}
`, upPort)
	startNginxE2E(t, conf, "18445")

	base := "http://127.0.0.1:18445"
	stop := make(chan struct{})
	defer close(stop)
	go fakeStreamingAgent(t, base, hostID, fp, "linux update ok\n[AIOPS_EXIT]0\n", stop)

	h, _ := srv.store.GetHost(hostID)
	out, kind, err := srv.execCommandOnHost(h, "echo hi", 60)
	if err != nil || kind != execOK {
		t.Fatalf("经缺配置的真 nginx，执行仍应跑完：kind=%v err=%v out=%q", kind, err, out)
	}
	if !strings.Contains(out, "linux update ok") {
		t.Fatalf("输出没拿全：%q", out)
	}
	v := edgeProxyDiagState.snapshot()
	if len(v) != 1 || v[0].Kind != "upstream_buffered" {
		t.Fatalf("必须留下「反代缓冲了上行流」的判定：%+v", v)
	}
	t.Logf("判定：%s", v[0].Detail)
}

// fakeStreamingAgent 模仿真 Agent 的 exec 会话：先开 tx（请求体是管道，边跑边写），
// 同时 1.5s 一次心跳；命令结束才关闭管道。经 proxy_request_buffering on 的 nginx 时，
// 整个请求体会被攒到管道关闭那一刻才转发给上游。
func fakeStreamingAgent(t *testing.T, base, hostID, fp, output string, stop <-chan struct{}) {
	t.Helper()
	cl := &http.Client{Timeout: 60 * time.Second}
	for {
		select {
		case <-stop:
			return
		default:
		}
		req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/agent/terminal/wait?host="+hostID, nil)
		req.Header.Set("X-Agent-Fingerprint", fp)
		resp, err := cl.Do(req)
		if err != nil {
			return
		}
		var got struct {
			Session string `json:"session"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if got.Session == "" {
			continue
		}
		pr, pw := io.Pipe()
		done := make(chan struct{})
		go func() { // 心跳：小 GET，不受请求体缓冲影响
			tick := time.NewTicker(50 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-stop:
					return
				case <-tick.C:
				}
				ar, _ := http.NewRequest(http.MethodGet, base+"/api/v1/agent/terminal/alive?session="+got.Session, nil)
				ar.Header.Set("X-Agent-Fingerprint", fp)
				if r, err := cl.Do(ar); err == nil {
					_, _ = io.Copy(io.Discard, r.Body)
					r.Body.Close()
				}
			}
		}()
		go func() {
			time.Sleep(1500 * time.Millisecond) // 命令执行中
			_, _ = pw.Write([]byte(output))
			_ = pw.Close()
			close(done)
		}()
		tx, _ := http.NewRequest(http.MethodPost, base+"/api/v1/agent/terminal/tx?session="+got.Session, pr)
		tx.Header.Set("X-Agent-Fingerprint", fp)
		tx.Header.Set("Content-Type", "application/octet-stream")
		if r, err := cl.Do(tx); err == nil {
			_, _ = io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		return
	}
}
