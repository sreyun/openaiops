package main

import (
	"testing"
)

// 首次迁移会用传进去的主机列表**整体重建**分组树与归属表。所以它只能看到全量主机——
// 传按权限过滤后的列表，等于让"谁先打开页面"决定别人的数据怎么迁：升级后第一个访问
// /api/v1/hosts 的人如果只被授权了几台机器，其余主机的分类会在那一次请求里全部丢掉。
func TestMigrationRebuildsFromEveryHostNotJustVisibleOnes(t *testing.T) {
	cs := testConfigStore(t)
	all := []*Host{
		{ID: "h1", Hostname: "db-01", Category: "数据库"},
		{ID: "h2", Hostname: "web-01", Category: "Web"},
		{ID: "h3", Hostname: "cache-01", Category: "缓存"},
	}
	// 模拟"受限用户只看得到 h1"的场景：迁移若只吃到这一台，另外两台的分类就没了。
	visibleOnly := all[:1]

	if !cs.ensureHostFoldersMigrated(all) {
		t.Fatal("首次迁移应返回 dirty")
	}
	folders, assign := cs.hostFoldersSnapshot()
	if len(folders) != 3 {
		t.Fatalf("应从三个分类建出三个分组，实际 %d", len(folders))
	}
	for _, h := range all {
		if assign[h.ID] == "" {
			t.Errorf("%s 没有分组归属", h.ID)
		}
	}

	// 再次调用（即使只带部分主机）不得推翻已有的树——增量分支只能新增。
	cs.ensureHostFoldersMigrated(visibleOnly)
	folders2, assign2 := cs.hostFoldersSnapshot()
	if len(folders2) != 3 {
		t.Errorf("后续调用把分组树改坏了：%d", len(folders2))
	}
	for _, h := range all {
		if assign2[h.ID] != assign[h.ID] {
			t.Errorf("%s 的归属被改了：%q → %q", h.ID, assign[h.ID], assign2[h.ID])
		}
	}
}

// 删主机要把它在配置里的痕迹一并删掉，而不是标成"显式未分组"。
// 哨兵是给活着的主机用的；主机都没了还留着，config.json 会随主机来去单向增长——
// 弹性伸缩环境里每台短命节点都留两条永不回收的记录。
func TestForgetHostRemovesEntriesInsteadOfLeavingSentinel(t *testing.T) {
	cs := testConfigStore(t)
	f, err := cs.addHostFolder("", "数据库")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.assignHostFolder("h1", f.ID); err != nil {
		t.Fatal(err)
	}

	if err := cs.forgetHost("h1"); err != nil {
		t.Fatal(err)
	}
	cs.mu.RLock()
	_, hasCat := cs.cfg.Categories["h1"]
	fid, hasFolder := cs.cfg.HostFolderAssign["h1"]
	cs.mu.RUnlock()
	if hasCat {
		t.Error("分类覆盖未删除")
	}
	if hasFolder {
		t.Errorf("分组归属未删除，残留 %q", fid)
	}

	// 对照：显式设为未分组仍然要留哨兵（活着的主机靠它挡住 Agent 上报的旧分类）。
	if err := cs.assignHostFolder("h2", HostFolderUngroupedID); err != nil {
		t.Fatal(err)
	}
	cs.mu.RLock()
	got := cs.cfg.HostFolderAssign["h2"]
	cs.mu.RUnlock()
	if got != HostFolderUngroupedID {
		t.Errorf("显式未分组的哨兵不该丢：%q", got)
	}

	// 抹一台本来就没记录的主机：不报错、也不该白写一次盘
	if err := cs.forgetHost("never-seen"); err != nil {
		t.Errorf("抹一台没记录的主机不该报错: %v", err)
	}
}
