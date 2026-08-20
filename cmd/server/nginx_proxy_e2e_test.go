package main

import (
	"bytes"
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

// 真·nginx 端到端：起一台真实 nginx（最常见的 `proxy_set_header Host $host` 写法，
// 端口被抹掉），把"改分组"完整跑一遍。
//
// 只在 AIOPS_NGINX_E2E=1 且机器上装了 nginx 时跑——CI 的 go-gate 里没有 nginx，默认 skip。
// 手工验证：AIOPS_NGINX_E2E=1 go test ./cmd/server -run TestNginxE2EFolderChange -v
//
// 这条用例是本轮故障的判决书：改之前，两条改分组请求经真 nginx 全是
// 403 {"error":"origin not allowed"}；改之后都是 200，且跨站 Origin 仍然被拒。
func TestNginxE2EFolderChange(t *testing.T) {
	if _, err := exec.LookPath("nginx"); err != nil {
		t.Skip("nginx not installed")
	}
	if os.Getenv("AIOPS_NGINX_E2E") != "1" {
		t.Skip("set AIOPS_NGINX_E2E=1")
	}
	srv, _ := newTestServer(t)
	srv.store.RegisterHost("h1", "h1", "fp-h1")
	srv.store.RegisterHost("h2", "h2", "fp-h2")
	f, err := srv.cfg.addHostFolder("", "数据库")
	if err != nil {
		t.Fatal(err)
	}
	token := srv.auth.issueSession("admin")

	backend := httptest.NewServer(srv.httpHandler())
	defer backend.Close()
	_, upPort, _ := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))

	dir := t.TempDir()
	for _, d := range []string{"logs", "tmp", "client_body", "proxy", "fastcgi", "uwsgi", "scgi"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	conf := fmt.Sprintf(`
daemon off;
pid %[1]s/nginx.pid;
error_log %[1]s/logs/error.log warn;
events { worker_connections 64; }
http {
  access_log %[1]s/logs/access.log;
  client_body_temp_path %[1]s/client_body;
  proxy_temp_path %[1]s/proxy;
  fastcgi_temp_path %[1]s/fastcgi;
  uwsgi_temp_path %[1]s/uwsgi;
  scgi_temp_path %[1]s/scgi;
  server {
    listen 127.0.0.1:18443;
    location / {
      proxy_pass http://127.0.0.1:%[2]s;
      proxy_http_version 1.1;
      proxy_set_header Host $host;                 # ← 现场最常见的写法：端口被抹掉
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
    }
  }
}
`, dir, upPort)
	confPath := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("nginx", "-c", confPath, "-p", dir)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("nginx 起不来：%v", err)
	}
	defer func() {
		_ = exec.Command("nginx", "-c", confPath, "-p", dir, "-s", "quit").Run()
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
		// 兜底：master 被杀时 worker 有时会留下来占着端口
		for i := 0; i < 40; i++ {
			c, err := net.DialTimeout("tcp", "127.0.0.1:18443", 100*time.Millisecond)
			if err != nil {
				return
			}
			_ = c.Close()
			time.Sleep(100 * time.Millisecond)
		}
		t.Log("警告：18443 仍被占用，可能有残留 nginx worker")
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", "127.0.0.1:18443", 300*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nginx 没在 10s 内监听 18443")
		}
		time.Sleep(150 * time.Millisecond)
	}

	post := func(path string, body any) (int, string) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:18443"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://127.0.0.1:18443") // 浏览器地址栏
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("请求 %s 失败：%v", path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	// 先制造一次被拒的写请求（跨站 Origin → 403），紧接着再发正常请求：
	// 早退中间件如果没把请求体读完，上游连接会被毒化，后续请求在 nginx 侧变成 502。
	{
		b, _ := json.Marshal(map[string]any{"host_ids": []string{"h1"}, "folder_id": f.ID})
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:18443/api/v1/hosts/folder/batch", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://evil.example.com")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("被拒请求发不出去：%v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Logf("跨站请求 → %d %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if code, body := post("/api/v1/hosts/h1/folder", map[string]string{"folder_id": f.ID}); code != 200 {
		t.Errorf("真 nginx 后单台改分组失败：%d %s", code, body)
	} else {
		t.Logf("单台改分组 OK：%s", body)
	}
	if code, body := post("/api/v1/hosts/folder/batch", map[string]any{"host_ids": []string{"h1", "h2"}, "folder_id": f.ID}); code != 200 {
		t.Errorf("真 nginx 后批量改分组失败：%d %s", code, body)
	} else {
		t.Logf("批量改分组 OK：%s", body)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "logs", "error.log")); err == nil {
		t.Logf("nginx error.log:\n%s", string(b))
	}
	_, assign := srv.cfg.hostFoldersSnapshot()
	if assign["h1"] != f.ID || assign["h2"] != f.ID {
		t.Errorf("落盘的归属不对：%v", assign)
	}
}
