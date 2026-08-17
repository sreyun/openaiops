package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// dist 里的产物落后一格，是一台机器"每一次升级都成功、版本却纹丝不动"的完美配方：
// 助手下载、校验、换版、重启全对，主机上报的还是旧版本；服务端只能看到 pending_verify
// 超时，5 分钟后走 legacy 救援，再失败，再软重试——整支机队每半小时被停一次服务，而
// 屏幕上永远只有「重启已排程但版本没跟上」。这类故障不会自愈，所以必须挡在自动升级前。
func TestStaleDistArtifactBlocksTheUpdateLoop(t *testing.T) {
	srv, _ := newTestServer(t)
	old := appVersion
	appVersion = "v9.9.9"
	defer func() { appVersion = old }()
	if err := srv.cfg.SetAgentAutoUpdatePolicy(true, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	h := &Host{ID: "h1", Hostname: "server11", OS: "windows", Arch: "amd64",
		AgentVersion: "v9.9.8", ServerURL: "https://panel.example.com", LastSeen: time.Now().Unix()}
	putTestHost(srv, h)

	// dist 里放的是"上一版"的产物。
	writeFakeAgentDistVersion(t, srv, "v9.9.7")
	ok, reason, detail := srv.decideAutoUpdate(h)
	if ok || reason != "stale_artifact" {
		t.Fatalf("ok=%v reason=%q detail=%q，期望 stale_artifact", ok, reason, detail)
	}
	if !strings.Contains(detail, "aiops-agent.exe") || !strings.Contains(detail, "v9.9.9") {
		t.Fatalf("跳过原因没说清是哪个文件、缺哪个版本：%q", detail)
	}

	// 换成对版本的产物就必须放行（否则这道闸门会把整个自动升级冻死）。
	writeFakeAgentDistVersion(t, srv, "v9.9.9")
	resetAgentDistVersionCache() // 同一刻度内原地替换，缓存看不见，见其注释
	if ok, reason, detail := srv.decideAutoUpdate(h); !ok {
		t.Fatalf("版本对得上的产物被挡住了：reason=%q detail=%q", reason, detail)
	}
}

// 读不出来 ≠ 版本不对。检测失败必须放行——把"我不知道"当成"它是错的"，会在一台
// 权限受限或文件被占用的服务器上直接冻结整支机队的升级。
func TestArtifactVersionCheckFailsOpen(t *testing.T) {
	if !agentDistCarriesVersion("/nonexistent/aiops-agent.exe", "v1.2.3") {
		t.Fatal("unreadable artifact must not be reported as a version mismatch")
	}
	if !agentDistCarriesVersion("", "v1.2.3") || !agentDistCarriesVersion("x", "") {
		t.Fatal("missing inputs must fail open")
	}
}

// 版本串跨过读取块边界时也必须找得到——真实二进制是几十 MB，分块读是必然的。
func TestArtifactVersionFoundAcrossChunkBoundary(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/agent.bin"
	const ver = "v1.2.345"
	// 1MB 是 fileContainsBytes 的块大小；把版本串正好压在边界上。
	body := make([]byte, (1<<20)+len(ver))
	for i := range body {
		body[i] = 'A'
	}
	copy(body[(1<<20)-len(ver)/2:], ver)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if !agentDistCarriesVersion(path, ver) {
		t.Fatal("a version string straddling two read windows was missed")
	}
	if agentDistCarriesVersion(path, "v9.8.7") {
		t.Fatal("reported a version the file does not contain")
	}
}
