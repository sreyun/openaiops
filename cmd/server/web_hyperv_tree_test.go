package main

import (
	"regexp"
	"strings"
	"testing"
)

// Hyper-V 左树的折叠必须真的能收起来。
//
// 这条曾经坏过，而且是"看起来一切正常"的坏法：树的渲染里有
//
//	const filtering = searchActive || !!HV_FILTER.status || !!treeQ
//	const rootCollapsed = !filtering && HV_COLLAPSED.has(rootId)
//	const collapsed     = HV_COLLAPSED.has(inv.host_id) && !filtering
//
// 而 HV_FILTER.status 的默认值就是 "running"（默认只看运行中的机器）。于是 filtering
// 开箱即为真，两处 `!filtering` 把折叠状态**永远**判没：箭头点下去，HV_COLLAPSED 确实
// 写进去了，重渲染时又被强制展开——用户看到的就是"Hyper-V 的树收不起来"。
//
// filtering 的本意只是「搜索时把命中露出来」，所以它只能由搜索框决定。状态下拉是常驻的
// 视图模式，不是一次性查找。主机树一直是这么做的（hosts.js 只认树内搜索 q）。
//
// 用文本断言是因为经典版控制台没有 JS 单测环境；这一条壁垒很薄，但它精确地挡住回归，
// 而且失败信息会直接说明为什么不能这么写。
func TestHyperVTreeCollapseIsNotOverriddenByStatusFilter(t *testing.T) {
	raw, err := webFS.ReadFile("web/js/hyperv.js")
	if err != nil {
		t.Fatalf("read hyperv.js: %v", err)
	}
	src := string(raw)

	// 1) 状态筛选的默认值仍然是 running（这正是让 bug 常驻的前提，值变了要重新审视本测试）
	if !strings.Contains(src, `const HV_FILTER = { q: "", status: "running" }`) {
		t.Log("HV_FILTER 默认值已变化，请重新确认折叠逻辑是否仍然正确")
	}

	// 2) filtering 不得由状态筛选参与决定
	re := regexp.MustCompile(`const filtering = ([^;\n]+);`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("hyperv.js 里找不到 `const filtering = ...`，树的折叠逻辑被改动过，请同步本测试")
	}
	if strings.Contains(m[1], "HV_FILTER.status") {
		t.Errorf("filtering 又把状态筛选算进去了：%q\n"+
			"HV_FILTER.status 默认是 \"running\"，这会让 `!filtering` 永远为假，"+
			"Hyper-V 左树的折叠将完全失效（点箭头没反应）。filtering 只能由搜索框决定。", m[1])
	}

	// 3) 折叠状态确实被渲染读取（根节点与宿主机节点各一处）
	for _, want := range []string{
		"HV_COLLAPSED.has(rootId)",
		"HV_COLLAPSED.has(inv.host_id)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("渲染不再读取折叠状态：缺少 %s", want)
		}
	}
}
