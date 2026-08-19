package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 中继安装必须"暂存 + 原子替换"，不能直接写 aiops-agent。
//
// 网关上中继是常驻进程，内核不允许写正在执行的映像（ETXTBSY）：curl 只会说一句
// "Can't open 'aiops-agent'"，脚本还会把它误诊成下载失败并打印 (HTTP 200)。结果是
// 装过一次的网关再也重装不了，而重装正是补 token / 换上游 / 升版本的标准手段。
func TestRelayInstallStagesBinaryBeforeReplace(t *testing.T) {
	tpl := relayInstallShTemplate
	if strings.Contains(tpl, `"$SERVER/dl/$BIN" -o aiops-agent`) {
		t.Error("中继安装脚本仍在直接下载到 aiops-agent：中继在跑时会 ETXTBSY，重装必然失败")
	}
	for _, must := range []string{`NEW=".aiops-agent.new"`, `mv -f "$NEW" aiops-agent`} {
		if !strings.Contains(tpl, must) {
			t.Errorf("中继安装脚本缺少暂存/原子替换步骤: %q", must)
		}
	}
	// 校验必须针对暂存文件，否则校验的是旧二进制，等于没校验。
	if !strings.Contains(tpl, `sha256sum "$NEW"`) {
		t.Error("SHA-256 必须校验暂存文件而不是就地文件")
	}
	// HTTP 200 却失败 = 本地写入问题，得说出来，否则用户会一直去查网络。
	if !strings.Contains(tpl, "失败在本地写入") {
		t.Error("下载失败的解释里缺少『服务端 200、本地写入失败』这一种")
	}
}

// TLS 由前置反代终结时 r.TLS 是 nil；只要 X-Forwarded-Proto 说是 https，
// 生成的安装命令与中继上游就必须是 https —— 否则中继回源打到只收 TLS 的前门
// 会被直接断开（EOF），内网全员 502。
func TestServerURLFollowsForwardedProtoHTTPS(t *testing.T) {
	s := &Server{cfg: newTestConfigStore(t)}
	r := httptest.NewRequest(http.MethodGet, "http://aiops.example.com/install-relay.sh", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := s.serverURL(r); got != "https://aiops.example.com" {
		t.Fatalf("反代终结 TLS 时应生成 https 地址，实际 %q", got)
	}
}

// 没有任何 TLS 线索时保持 http，不能凭空升级（内网 :8529 直连面板是常态）。
func TestServerURLStaysHTTPWithoutTLSHint(t *testing.T) {
	s := &Server{cfg: newTestConfigStore(t)}
	r := httptest.NewRequest(http.MethodGet, "http://10.0.0.9:8529/install.sh", nil)
	if got := s.serverURL(r); got != "http://10.0.0.9:8529" {
		t.Fatalf("无 TLS 线索时不应改写协议，实际 %q", got)
	}
}

// 续传遇上"上次中断留下的、来自另一个版本的分片"时，续传出来的是两个版本的拼接。
// 校验必须能自愈：清空重下一次再判死刑——否则那个坏分片一直在原地，重跑多少次都失败，
// 而用户手上只有一句 "SHA-256 mismatch"。两个安装脚本都要有这一次重试。
func TestInstallScriptsRetryOnceOnChecksumMismatch(t *testing.T) {
	for name, tpl := range map[string]string{
		"install.sh":       installShTemplate,
		"relay-install.sh": relayInstallShTemplate,
	} {
		if !strings.Contains(tpl, "清空后重新完整下载一次") {
			t.Errorf("%s: 校验失败后缺少\"清空重下一次\"的自愈路径", name)
		}
	}
	// 现役二进制只在校验通过后才被替换。
	if strings.Index(relayInstallShTemplate, `mv -f "$NEW" aiops-agent`) <
		strings.Index(relayInstallShTemplate, "SHA-256 mismatch for $BIN") {
		t.Error("中继脚本在校验之前就替换了现役二进制")
	}
}
