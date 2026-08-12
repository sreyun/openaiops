package main

import (
	"strings"
	"testing"
)

// TestDeriveAndSealLog 验证日志密钥派生 + gzip/AES-256-GCM 加解密往返（服务端侧，与 agent 同算法）。
func TestDeriveAndSealLog(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "test-master-key-123") // 隔离 + 自动还原，避免与并行的配置加密测试竞争

	fp := "fp-abc123"
	key := deriveLogKey(fp)
	if len(key) != 32 {
		t.Fatalf("deriveLogKey 长度=%d，应为 32", len(key))
	}
	if string(deriveLogKey(fp)) != string(key) {
		t.Fatal("deriveLogKey 应是确定性的")
	}
	if string(deriveLogKey("other-fp")) == string(key) {
		t.Fatal("不同指纹应派生不同密钥")
	}

	plain := []byte(`{"host_id":"h1","lines":[{"message":"boom secret"}]}`)
	sealed, err := sealLog(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "boom") {
		t.Fatal("密文不应泄露明文")
	}
	out, err := openLog(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(plain) {
		t.Fatalf("往返不一致: %s", out)
	}
	if _, err := openLog(deriveLogKey("wrong-fp"), sealed); err == nil {
		t.Fatal("错误密钥应解密失败")
	}
}

// TestDeriveLogKeyNoMaster 未配置主密钥时返回 nil（日志加密关闭，明文上报）。
func TestDeriveLogKeyNoMaster(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "") // 空 = 未配置主密钥
	if deriveLogKey("fp") != nil {
		t.Fatal("无主密钥应返回 nil（加密禁用）")
	}
}

// TestSanitizeLogPaths 验证路径清洗：换行/逗号分隔、剥离 shell 危险字符、输出合法 JSON 数组。
func TestSanitizeLogPaths(t *testing.T) {
	got := sanitizeLogPaths("/var/log/nginx/access.log\n/var/log/app/\n/etc/$(reboot)`whoami`")
	for _, bad := range []string{"$", "`", "(", ")", ";", "|", "&"} {
		if strings.Contains(got, bad) {
			t.Fatalf("危险字符 %q 泄露: %s", bad, got)
		}
	}
	if !strings.Contains(got, "/var/log/nginx/access.log") || !strings.Contains(got, "/var/log/app/") {
		t.Fatalf("合法路径被丢弃: %s", got)
	}
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
		t.Fatalf("应为 JSON 数组: %s", got)
	}
	if sanitizeLogPaths("") != "[]" || sanitizeLogPaths("  \n , ") != "[]" {
		t.Fatal("空/纯空白应为 []")
	}
}

// TestInstallScriptEmbedsLogPaths 验证安装脚本把日志路径写进 config.yaml 的 log_paths。
// 配置以 base64 注入脚本；解码后应为 `log_paths: [...]`。
func TestInstallScriptEmbedsLogPaths(t *testing.T) {
	lp := sanitizeLogPaths("/var/log/nginx/access.log\n/var/log/app/")
	sh := renderScript(installShTemplate, "http://s:8529", "tok", "prod", "", "", lp)
	cfg, err := extractInstallConfigYAML(sh)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "/var/log/nginx/access.log") {
		t.Fatal("config 缺少日志路径")
	}
	if !strings.Contains(cfg, "log_paths:") {
		t.Fatal("config.yaml 缺少 log_paths 字段")
	}
	if strings.Contains(sh, "__CONFIG_B64__") || strings.Contains(sh, "__LOG_PATHS__") {
		t.Fatal("占位符未被替换")
	}
	// 空 → log_paths: []（向后兼容，不影响现有安装）
	sh2 := renderScript(installShTemplate, "http://s:8529", "tok", "prod", "", "", "")
	cfg2, err := extractInstallConfigYAML(sh2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg2, "log_paths: []") {
		t.Fatal("空日志路径应渲染为 log_paths: []")
	}
}
