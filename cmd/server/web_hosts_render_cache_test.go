package main

import (
	"regexp"
	"strings"
	"testing"
)

// 主机列表的差量渲染缓存键必须覆盖分组信息。
//
// 这条坏过一次，症状是"改了分组（单台/批量）或改了分组名，列表上还是旧的，刷新才变"：
// 命中缓存时 renderHosts 走的是 updateHostCard，而它只补在线状态与 CPU/内存/磁盘三个
// 数值——分组标签它不管。数据早就回来了，只是没人重画。
//
// 修法是把分组信息放进键，而不是在每个改分组的入口去记得手动作废缓存（那种约定迟早会
// 被下一个改动漏掉）。这里同时钉住两件事：键里有分组，且 updateHostCard 仍然只做数值补丁
// （如果哪天它开始重建整行，这条测试可以随之放宽）。
func TestHostListRenderCacheKeyIncludesFolder(t *testing.T) {
	raw, err := webFS.ReadFile("web/js/hosts.js")
	if err != nil {
		t.Fatalf("read hosts.js: %v", err)
	}
	src := string(raw)

	sig := regexp.MustCompile(`const folderSig = ([^;]+);`).FindStringSubmatch(src)
	if sig == nil {
		t.Fatal("hosts.js 里找不到 `const folderSig = ...`，差量渲染的缓存键被改动过，请同步本测试")
	}
	for _, want := range []string{"folder_id", "folder_path", "category"} {
		if !strings.Contains(sig[1], want) {
			t.Errorf("分组签名里没有 %s：\n  %s\n"+
				"缺了它，改分组/改分组名之后列表会命中缓存、只补指标数值，"+
				"分组标签停留在旧值，用户必须手动刷新。", want, strings.TrimSpace(sig[1]))
		}
	}

	key := regexp.MustCompile(`(?s)const newKey = (.*?);\n`).FindStringSubmatch(src)
	if key == nil {
		t.Fatal("hosts.js 里找不到 `const newKey = ...`")
	}
	if !strings.Contains(key[1], "folderSig") {
		t.Errorf("渲染缓存键没有带上分组签名：\n  %s", strings.TrimSpace(key[1]))
	}

	// 分组改动之后要显式作废缓存（不依赖服务端这一次一定把新值带回来）
	if !strings.Contains(src, "invalidateHostRenderCache();\n  await refresh(true);") {
		t.Error("afterHostFolderChange 应先作废差量缓存再强制刷新")
	}
}
