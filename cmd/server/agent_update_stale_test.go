package main

import (
	"fmt"
	"strings"
	"testing"
)

// Agent 判出「下载到的产物是旧版本」时不能退到 legacy 脚本救援：脚本从同一个 base
// （中继）拉同一个文件，只会拿到同一份旧二进制，而它校验的是服务端 pin 的 sha256，
// 于是失败原因会从"产物是旧的"变成"校验和不符"——同一个故障换一张更难懂的脸。
func TestStaleArtifactDoesNotFallBackToLegacyScript(t *testing.T) {
	out := "agent_update: stale artifact: aiops-agent-linux-amd64 from http://10.0.0.9:8529 reports v0.19.98," +
		" but this upgrade targets v0.19.100 — the download source is serving an outdated artifact"
	if shouldLegacyAgentUpdateFallback(out, fmt.Errorf("exit status 1")) {
		t.Fatal("产物过期属于不会自愈的原因，必须停在这里并把原文抬到屏幕上，而不是再跑一遍 legacy")
	}
	// 对照组：真正的下载失败仍然要救援（代理/证书链/TLS 这些换条路就能过）。
	if !shouldLegacyAgentUpdateFallback("agent_update: download failed: dial tcp: connection refused", fmt.Errorf("exit status 1")) {
		t.Fatal("下载失败仍应退到 legacy 脚本")
	}
}

// 网关机自己也要进主机列表：装中继时若不把 token 写进 config.yaml，开了
// require_token 的服务端会一直拒绝它注册，它就永远只是一个转发器。
func TestRelayInstallScriptsCarryTokenAndCategory(t *testing.T) {
	sh := renderScript(relayInstallShTemplate, "https://panel.example", "tok-123", "", "", "", "")
	if !strings.Contains(sh, `TOKEN="tok-123"`) {
		t.Error("install-relay.sh 没有带上 token")
	}
	if !strings.Contains(sh, `printf 'token: "%s"\n' "$TOKEN"`) {
		t.Error("install-relay.sh 没有把 token 写进 config.yaml")
	}
	if !strings.Contains(sh, `category: "relay-gateway"`) {
		t.Error("install-relay.sh 缺少网关默认分类，主机列表里认不出这台是网关")
	}
	if !strings.Contains(sh, "chmod 600 config.yaml") {
		t.Error("config.yaml 现在同时带 token 与 relay_secret，必须收权限")
	}

	ps := renderScript(relayInstallPs1Template, "https://panel.example", "tok-123", "线上网关", "f-9", "", "")
	if !strings.Contains(ps, `$Token    = "tok-123"`) {
		t.Error("install-relay.ps1 没有带上 token")
	}
	if !strings.Contains(ps, `$RelayLines.Add("token: '"`) {
		t.Error("install-relay.ps1 没有把 token 写进 config.yaml")
	}
	if !strings.Contains(ps, `$Category = "线上网关"`) || !strings.Contains(ps, `$FolderID = "f-9"`) {
		t.Error("install-relay.ps1 丢了 category/folder_id，网关会落在资产树之外")
	}
}
