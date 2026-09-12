package main

import (
	"os"
	"path/filepath"
	"testing"
)

// config.yml 是一等公民配置名（启动探测 / 升级助手 / 文档均支持），敏感路径闸门
// 若只认 config.yaml，换个后缀就能用 file_head 把安装 token / relay_secret 读走。
func TestAgentDeniedPathBlocksConfigYmlInInstallDir(t *testing.T) {
	for _, p := range []string{
		"/opt/aiops-agent/config.yml",
		"/usr/local/aiops-agent/config.yml",
		"/home/op/.aiops-agent/config.yml",
		`C:\Program Files\aiops-agent\config.yml`,
	} {
		if !agentDeniedPath(p) {
			t.Errorf("install-dir config.yml must be denied: %s", p)
		}
	}
	// 业务应用自己的 config.yml 不能误伤。
	if agentDeniedPath("/opt/app/config.yml") {
		t.Error("ordinary app config.yml must not be denied")
	}
}

func TestModuleFileHeadRefusesConfigYml(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "aiops-agent")
	if err := os.MkdirAll(install, 0o750); err != nil {
		t.Fatal(err)
	}
	yml := filepath.Join(install, "config.yml")
	if err := os.WriteFile(yml, []byte("token: secret-install-token\nrelay_secret: s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, exit := moduleFileHead(map[string]string{"path": yml})
	if exit == 0 {
		t.Fatalf("file_head must refuse install-dir config.yml, got exit=0 body=%q", out)
	}
	if !agentDeniedPath(yml) {
		t.Fatal("agentDeniedPath must catch the same path file_head rejected")
	}
}
