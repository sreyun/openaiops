package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMetricsEndpointAuthAndPayload 钉住两件事：/metrics 不能匿名放行（里面有主机规模、
// 告警面与授权信息），以及运维真正会用到的那几个量确实在输出里。
func TestMetricsEndpointAuthAndPayload(t *testing.T) {
	licenseResetForTest(t)
	srv, _ := newTestServer(t)
	srv.store.RegisterHost("h1", "n1", "fp1")

	// 1. 没有会话、也没有令牌 → 401
	rr := httptest.NewRecorder()
	srv.handleMetrics(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("匿名访问 /metrics 应 401，得到 %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "aiops_hosts_total") {
		t.Fatal("401 响应里居然带了指标内容")
	}

	// 2. 配了令牌 → Bearer 放行；错令牌仍然 401
	t.Setenv("AIOPS_METRICS_TOKEN", "s3cr3t-token")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rr = httptest.NewRecorder()
	srv.handleMetrics(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("错误令牌应 401，得到 %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer s3cr3t-token")
	rr = httptest.NewRecorder()
	srv.handleMetrics(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("正确令牌应 200，得到 %d (%s)", rr.Code, rr.Body)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"aiops_build_info", "aiops_hosts_total", "aiops_agent_online_ratio",
		"aiops_alerts_active{level=\"critical\"}", "aiops_pg_flush_duration_seconds",
		"aiops_license_state", "aiops_license_hosts_used", "aiops_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/metrics 缺少 %s", want)
		}
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Content-Type 应为 Prometheus 文本格式，得到 %q", rr.Header().Get("Content-Type"))
	}
	// ?token= 同样可用（有些采集端加不了 header）
	rr = httptest.NewRecorder()
	srv.handleMetrics(rr, httptest.NewRequest(http.MethodGet, "/metrics?token=s3cr3t-token", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("?token= 应放行，得到 %d", rr.Code)
	}
}

// TestMetricsLabelEscaping：版本号里带引号不能把整份输出弄成非法格式。
func TestMetricsLabelEscaping(t *testing.T) {
	var b strings.Builder
	writeMetricLine(&b, "aiops_build_info", 1, "version", `v1"2\3`)
	got := b.String()
	if !strings.Contains(got, `version="v1\"2\\3"`) {
		t.Fatalf("标签未正确转义: %q", got)
	}
}

// TestSupportBundleContentsAndSanitization：诊断包会被邮件转发、贴进工单，
// 所以既要有该有的东西，也**绝不能**带出密钥。
func TestSupportBundleContentsAndSanitization(t *testing.T) {
	licenseResetForTest(t)
	srv, token := newTestServer(t)
	srv.store.RegisterHost("h1", "node-1", "fp1")

	rr := httptest.NewRecorder()
	srv.handleSupportBundle(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/support-bundle", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("诊断包应 200，得到 %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type 应为 application/zip，得到 %q", ct)
	}
	raw := rr.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("诊断包不是合法 zip: %v", err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打不开 %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = data
	}
	for _, want := range []string{
		"README.txt", "meta.json", "config.sanitized.json", "license.json",
		"connectivity.json", "schema_migrations.json", "hosts.json",
		"platform_faults.json", "env.json", "activity.log", "goroutines.txt", "metrics.txt",
	} {
		if _, ok := files[want]; !ok {
			t.Fatalf("诊断包缺少 %s", want)
		}
	}

	// 安装 Token 是凭据：必须已打码。
	if token != "" {
		for name, data := range files {
			if bytes.Contains(data, []byte(token)) {
				t.Fatalf("诊断包 %s 里带出了安装 Token 明文", name)
			}
		}
	}
	var meta map[string]any
	if err := json.Unmarshal(files["meta.json"], &meta); err != nil {
		t.Fatalf("meta.json 不是合法 JSON: %v", err)
	}
	if meta["install_id"] == "" || meta["install_id"] == nil {
		t.Fatal("meta.json 缺少部署指纹")
	}
	if !bytes.Contains(files["hosts.json"], []byte("node-1")) {
		t.Fatal("hosts.json 里应有主机身份")
	}
	// 主机条目只带身份与在线状态，不该出现指标字段。
	if bytes.Contains(files["hosts.json"], []byte("cpu_percent")) {
		t.Fatal("hosts.json 里混进了指标数据")
	}

	// env.json：非白名单的 AIOPS_* 只报"已设置"，不带值。
	t.Setenv("AIOPS_FAKE_SECRET", "super-secret-value")
	env := supportEnv()
	if env["AIOPS_FAKE_SECRET"] != "(set, masked)" {
		t.Fatalf("环境变量未打码: %v", env["AIOPS_FAKE_SECRET"])
	}
}

// TestBackupKindAndPruneByKind：保留策略必须**按种类**各留 N 份。
// 混在一起排序会出现"新做的时序备份被一串 PG 备份挤出保留窗口"——
// 那等于时序备份开了等于没开。
func TestBackupKindAndPruneByKind(t *testing.T) {
	if got := backupKindOf("aiops-vm-20260101-000000.native.gz"); got != "vm" {
		t.Fatalf("vm 备份被判成 %s", got)
	}
	if got := backupKindOf("aiops-rec-20260101-000000.tar.gz"); got != "recordings" {
		t.Fatalf("录像备份被判成 %s", got)
	}
	if got := backupKindOf("aiops-pg-20260101-000000.dump"); got != "postgres" {
		t.Fatalf("PG 备份被判成 %s", got)
	}
	if got := backupKindOf("legacy-backup.dump"); got != "postgres" {
		t.Fatalf("历史文件名应按 PG 处理，得到 %s", got)
	}

	dir := t.TempDir()
	t.Setenv("AIOPS_BACKUP_DIR", dir)
	srv, _ := newTestServer(t)
	mk := func(name string, ageMin int) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(-time.Duration(ageMin) * time.Minute)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	// PG 五份（够多，会触发裁剪），时序与录像各一份（新做的，绝不能被挤掉）
	for i := 0; i < 5; i++ {
		mk("aiops-pg-2026010"+string(rune('1'+i))+"-000000.dump", 100+i*10)
	}
	mk("aiops-vm-20260101-000000.native.gz", 5)
	mk("aiops-rec-20260101-000000.tar.gz", 1)

	srv.pruneBackups(2) // 每种各留 2 份

	left := map[string]int{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		left[backupKindOf(e.Name())]++
	}
	if left["postgres"] != 2 {
		t.Fatalf("PG 备份应留 2 份，实际 %d", left["postgres"])
	}
	if left["vm"] != 1 {
		t.Fatalf("时序备份被误删（剩 %d）——保留策略没有按种类分组", left["vm"])
	}
	if left["recordings"] != 1 {
		t.Fatalf("录像备份被误删（剩 %d）", left["recordings"])
	}
}
