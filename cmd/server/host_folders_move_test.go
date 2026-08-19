package main

import (
	"testing"
)

func mustFolder(t *testing.T, cs *ConfigStore, parent, name string) HostFolderNode {
	t.Helper()
	n, err := cs.addHostFolder(parent, name)
	if err != nil {
		t.Fatalf("addHostFolder(%q,%q): %v", parent, name, err)
	}
	return n
}

// 分组建错了层级是常事（先建了「华东」才想起来应该挂在「IDC机房」下面）。
// 改层级不能要求用户删掉重建——里面的主机会一起掉出去。
func TestMoveHostFolderReparents(t *testing.T) {
	cs := testConfigStore(t)
	idc := mustFolder(t, cs, "", "IDC机房")
	east := mustFolder(t, cs, "", "华东")
	db := mustFolder(t, cs, east.ID, "数据库")
	if err := cs.assignHostFolder("h1", db.ID); err != nil {
		t.Fatal(err)
	}

	if err := cs.moveHostFolder(east.ID, idc.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	folders, assign := cs.hostFoldersSnapshot()
	paths := folderPathMap(folders)
	if got, want := paths[db.ID], "IDC机房 / 华东 / 数据库"; got != want {
		t.Errorf("移动后路径 = %q, want %q", got, want)
	}
	if assign["h1"] != db.ID {
		t.Errorf("主机跟着子树走丢了: %v", assign["h1"])
	}
	if len(folders) != 1 {
		t.Errorf("根下应只剩 IDC机房，实际 %d 个", len(folders))
	}
}

// 把父级挪进自己的子树 = 一个环。摘下来之后目标父级已不在树里，天真的实现会把整棵
// 子树连同里面的主机一起丢掉——必须在动手前就拒绝，且原树一根头发都不能少。
func TestMoveHostFolderRejectsCycleWithoutLosingSubtree(t *testing.T) {
	cs := testConfigStore(t)
	a := mustFolder(t, cs, "", "A")
	b := mustFolder(t, cs, a.ID, "B")
	c := mustFolder(t, cs, b.ID, "C")
	if err := cs.assignHostFolder("h1", c.ID); err != nil {
		t.Fatal(err)
	}

	for _, target := range []struct{ name, id string }{{"自己", a.ID}, {"子节点", b.ID}, {"孙节点", c.ID}} {
		if err := cs.moveHostFolder(a.ID, target.id); err == nil {
			t.Errorf("把 A 挪到%s下应被拒绝", target.name)
		}
	}
	folders, assign := cs.hostFoldersSnapshot()
	paths := folderPathMap(folders)
	if got, want := paths[c.ID], "A / B / C"; got != want {
		t.Fatalf("被拒之后树被改坏了: %q, want %q", got, want)
	}
	if assign["h1"] != c.ID {
		t.Fatalf("主机分配丢失: %v", assign["h1"])
	}
}

func TestMoveHostFolderGuards(t *testing.T) {
	cs := testConfigStore(t)
	a := mustFolder(t, cs, "", "A")
	if err := cs.moveHostFolder("", a.ID); err == nil {
		t.Error("空 id 应拒绝")
	}
	if err := cs.moveHostFolder(HostFolderUngroupedID, a.ID); err == nil {
		t.Error("未分组是虚拟节点，不能移动")
	}
	if err := cs.moveHostFolder("no-such-id", a.ID); err == nil {
		t.Error("不存在的分组应拒绝")
	}
	if err := cs.moveHostFolder(a.ID, "no-such-parent"); err == nil {
		t.Error("不存在的父级应拒绝")
	}
	// 挪到根：parent_id 传空或「未分组」都表示根。
	b := mustFolder(t, cs, a.ID, "B")
	if err := cs.moveHostFolder(b.ID, HostFolderUngroupedID); err != nil {
		t.Fatalf("挪到根失败: %v", err)
	}
	folders, _ := cs.hostFoldersSnapshot()
	if len(folders) != 2 {
		t.Errorf("根下应有 2 个分组，实际 %d", len(folders))
	}
}

// 分组是按创建时间堆进去的，几十个机房要靠眼睛扫。排序必须是"人读的顺序"：
// srv2 在 srv10 前面，而不是字典序的 srv10 在前。
func TestSortFolderTreeForDisplayIsNatural(t *testing.T) {
	in := []HostFolderNode{
		{ID: "1", Name: "srv10"},
		{ID: "2", Name: "srv2"},
		{ID: "3", Name: "Beijing", Children: []HostFolderNode{
			{ID: "3b", Name: "rack-12"},
			{ID: "3a", Name: "rack-3"},
		}},
		{ID: "4", Name: "apple"},
	}
	out := sortFolderTreeForDisplay(in)
	got := []string{out[0].Name, out[1].Name, out[2].Name, out[3].Name}
	want := []string{"apple", "Beijing", "srv2", "srv10"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顶层顺序 = %v, want %v", got, want)
		}
	}
	if out[1].Children[0].Name != "rack-3" || out[1].Children[1].Name != "rack-12" {
		t.Errorf("子层没排: %v", out[1].Children)
	}
	// 排序只作用于副本：存储顺序不能被洗掉，否则前端整棵 PUT 回来会把手工顺序覆盖掉。
	if in[0].Name != "srv10" || in[2].Children[0].Name != "rack-12" {
		t.Error("入参被就地改写了")
	}
}

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"srv2", "srv10", true},
		{"srv10", "srv2", false},
		{"srv02", "srv2", false}, // 数值相等时不强制顺序，只要不判成 srv02 < srv2 的字典序错觉
		{"a", "B", true},
		{"30-测试", "4-测试", false},
		{"4-测试", "30-测试", true},
		{"abc", "abcd", true},
		{"", "a", true},
	}
	for _, c := range cases {
		if got := naturalLess(c.a, c.b); got != c.want {
			t.Errorf("naturalLess(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// 逐台点是十几次弹窗；批量一次落盘。要点是"要么全改，要么全不改"。
func TestAssignHostFoldersBatch(t *testing.T) {
	cs := testConfigStore(t)
	f := mustFolder(t, cs, "", "数据库")
	if err := cs.assignHostFoldersBatch([]string{"h1", "h2", "h3"}, f.ID); err != nil {
		t.Fatalf("batch: %v", err)
	}
	_, assign := cs.hostFoldersSnapshot()
	for _, h := range []string{"h1", "h2", "h3"} {
		if assign[h] != f.ID {
			t.Errorf("%s 未落到目标分组: %v", h, assign[h])
		}
	}
	// 目标分组不存在：一台都不许改。
	before := assign["h1"]
	if err := cs.assignHostFoldersBatch([]string{"h1", "h2"}, "no-such"); err == nil {
		t.Error("不存在的分组应拒绝")
	}
	_, after := cs.hostFoldersSnapshot()
	if after["h1"] != before {
		t.Error("失败的批量操作改动了数据")
	}
	// 批量挪到未分组。
	if err := cs.assignHostFoldersBatch([]string{"h1"}, ""); err != nil {
		t.Fatal(err)
	}
	_, assign = cs.hostFoldersSnapshot()
	if assign["h1"] != HostFolderUngroupedID {
		t.Errorf("未分组哨兵未写入: %v", assign["h1"])
	}
	if err := cs.assignHostFoldersBatch(nil, f.ID); err == nil {
		t.Error("空列表应拒绝")
	}
}
