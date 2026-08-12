package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFolderTreeDepth(t *testing.T) {
	// chain builds a single linear branch n levels deep.
	chain := func(n int) []HostFolderNode {
		var build func(level int) []HostFolderNode
		build = func(level int) []HostFolderNode {
			if level > n {
				return nil
			}
			return []HostFolderNode{{
				ID:       fmt.Sprintf("n%d", level),
				Name:     fmt.Sprintf("L%d", level),
				Children: build(level + 1),
			}}
		}
		return build(1)
	}
	if err := validateFolderTree(chain(MaxHostFolderDepth)); err != nil {
		t.Fatalf("depth %d should be ok: %v", MaxHostFolderDepth, err)
	}
	if err := validateFolderTree(chain(MaxHostFolderDepth + 1)); err == nil {
		t.Fatalf("depth %d should fail", MaxHostFolderDepth+1)
	}
}

func testConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	cs, err := NewConfigStore(filepath.Join(t.TempDir(), "cfg.json"), nil)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	return cs
}

func TestHostFolderMigrateAndCategoryL1(t *testing.T) {
	cs := testConfigStore(t)
	cs.cfg.Categories = map[string]string{"h1": "生产"}
	cs.cfg.HostFolders = nil
	cs.cfg.HostFolderAssign = nil
	hosts := []*Host{
		{ID: "h1", Hostname: "a", Category: "生产"},
		{ID: "h2", Hostname: "b", Category: "DB"},
		{ID: "h3", Hostname: "c"},
	}
	if !cs.ensureHostFoldersMigrated(hosts) {
		t.Fatal("expected migration")
	}
	if cs.cfg.HostFolders == nil {
		t.Fatal("HostFolders should be non-nil after migrate")
	}
	if len(cs.cfg.HostFolders) != 2 {
		t.Fatalf("want 2 L1 folders, got %d", len(cs.cfg.HostFolders))
	}
	if cs.cfg.HostFolderAssign["h1"] == "" || cs.cfg.HostFolderAssign["h2"] == "" {
		t.Fatal("h1/h2 should be assigned")
	}
	if _, ok := cs.cfg.HostFolderAssign["h3"]; ok {
		t.Fatal("h3 should stay ungrouped")
	}
	if cs.ensureHostFoldersMigrated(hosts) {
		t.Fatal("second migrate should be no-op")
	}

	if err := cs.setCategoryWithFolder("h3", "办公"); err != nil {
		t.Fatal(err)
	}
	if len(cs.cfg.HostFolders) != 3 {
		t.Fatalf("category should create L1, got %d", len(cs.cfg.HostFolders))
	}
	if cs.cfg.Categories["h3"] != "办公" {
		t.Fatalf("category sync: %q", cs.cfg.Categories["h3"])
	}
}

func TestDeleteHostFolderMovesUp(t *testing.T) {
	cs := testConfigStore(t)
	cs.cfg.HostFolders = []HostFolderNode{{
		ID: "p", Name: "Prod", Children: []HostFolderNode{{ID: "c", Name: "DB"}},
	}}
	cs.cfg.HostFolderAssign = map[string]string{"h1": "c"}
	cs.cfg.Categories = map[string]string{"h1": "DB"}
	if err := cs.deleteHostFolder("c"); err != nil {
		t.Fatal(err)
	}
	if cs.cfg.HostFolderAssign["h1"] != "p" {
		t.Fatalf("want parent p, got %q", cs.cfg.HostFolderAssign["h1"])
	}
	if cs.cfg.Categories["h1"] != "Prod" {
		t.Fatalf("category should be parent name, got %q", cs.cfg.Categories["h1"])
	}
	if err := cs.deleteHostFolder("p"); err != nil {
		t.Fatal(err)
	}
	if cs.cfg.HostFolderAssign["h1"] != HostFolderUngroupedID {
		t.Fatalf("deleting L1 should mark ungrouped, got %q", cs.cfg.HostFolderAssign["h1"])
	}
	if cs.cfg.Categories["h1"] != "" {
		t.Fatalf("category should be cleared, got %q", cs.cfg.Categories["h1"])
	}
}

func TestExplicitUngroupedDoesNotBounceBack(t *testing.T) {
	cs := testConfigStore(t)
	cs.cfg.HostFolders = []HostFolderNode{{ID: "prod", Name: "生产"}}
	cs.cfg.HostFolderAssign = map[string]string{"h1": HostFolderUngroupedID}
	cs.cfg.Categories = map[string]string{"h1": ""}
	hosts := []*Host{{ID: "h1", Hostname: "a", Category: "生产"}}
	if cs.ensureHostFoldersMigrated(hosts) {
		t.Fatal("explicit ungrouped must not re-file from Agent.Category")
	}
	if cs.cfg.HostFolderAssign["h1"] != HostFolderUngroupedID {
		t.Fatalf("want ungrouped sentinel, got %q", cs.cfg.HostFolderAssign["h1"])
	}
}

func TestAddChildDepthLimit(t *testing.T) {
	cs := testConfigStore(t)
	cs.cfg.HostFolders = []HostFolderNode{}
	// Build a chain exactly MaxHostFolderDepth deep — every level must succeed.
	parent := ""
	for i := 1; i <= MaxHostFolderDepth; i++ {
		n, err := cs.addHostFolder(parent, fmt.Sprintf("L%d", i))
		if err != nil {
			t.Fatalf("L%d should be allowed (<= MaxHostFolderDepth=%d): %v", i, MaxHostFolderDepth, err)
		}
		parent = n.ID
	}
	// One level beyond the cap must be rejected.
	if _, err := cs.addHostFolder(parent, fmt.Sprintf("L%d", MaxHostFolderDepth+1)); err == nil {
		t.Fatalf("L%d should be rejected (exceeds MaxHostFolderDepth=%d)", MaxHostFolderDepth+1, MaxHostFolderDepth)
	}
}

func TestFolderPathMap(t *testing.T) {
	nodes := []HostFolderNode{{
		ID: "a", Name: "生产", Children: []HostFolderNode{{ID: "b", Name: "DB"}},
	}}
	paths := folderPathMap(nodes)
	if paths["b"] != "生产 / DB" {
		t.Fatalf("path=%q", paths["b"])
	}
}

func TestSanitizeFolderID(t *testing.T) {
	if got := sanitizeFolderID("__ungrouped__"); got != HostFolderUngroupedID {
		t.Fatalf("ungrouped: %q", got)
	}
	if got := sanitizeFolderID("hf-deadbeef"); got != "hf-deadbeef" {
		t.Fatalf("valid: %q", got)
	}
	if got := sanitizeFolderID("hf-abc;rm -rf /"); got != "hf-abcrm-rf" && !strings.Contains(got, ";") {
		// injection chars stripped
		if strings.Contains(got, ";") || strings.Contains(got, " ") {
			t.Fatalf("injection not stripped: %q", got)
		}
	}
	if got := sanitizeFolderID(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestApplyAgentFolderHintNested(t *testing.T) {
	cs := testConfigStore(t)
	cs.cfg.HostFolders = []HostFolderNode{{
		ID: "prod", Name: "生产", Children: []HostFolderNode{{ID: "db", Name: "DB"}},
	}}
	cs.cfg.HostFolderAssign = map[string]string{}

	if err := cs.applyAgentFolderHint("h1", "db", "ignored"); err != nil {
		t.Fatal(err)
	}
	if cs.cfg.HostFolderAssign["h1"] != "db" {
		t.Fatalf("want nested db, got %q", cs.cfg.HostFolderAssign["h1"])
	}
	if cs.cfg.Categories["h1"] != "DB" {
		t.Fatalf("leaf category want DB, got %q", cs.cfg.Categories["h1"])
	}

	// Invalid folder_id + category on fresh host → L1
	if err := cs.applyAgentFolderHint("h2", "missing-id", "测试"); err != nil {
		t.Fatal(err)
	}
	if cs.hostFolderOf("h2") == "missing-id" {
		t.Fatal("invalid folder_id must not stick")
	}
	n := findL1FolderByName(cs.cfg.HostFolders, "测试")
	if n == nil || cs.cfg.HostFolderAssign["h2"] != n.ID {
		t.Fatalf("want L1 测试 assign, got %q", cs.cfg.HostFolderAssign["h2"])
	}

	// Already assigned: category-only must not overwrite
	prev := cs.cfg.HostFolderAssign["h1"]
	if err := cs.applyAgentFolderHint("h1", "", "other"); err != nil {
		t.Fatal(err)
	}
	if cs.cfg.HostFolderAssign["h1"] != prev {
		t.Fatalf("category must not move assigned host, got %q", cs.cfg.HostFolderAssign["h1"])
	}
}

func TestBuildInstallConfigYAMLFolderID(t *testing.T) {
	cfg := buildInstallConfigYAML("http://s:8529", "tok", "DB", "hf-abc", "", "[]", installAuditOptions{}, false)
	if !strings.Contains(cfg, `folder_id: "hf-abc"`) {
		t.Fatalf("missing folder_id in config:\n%s", cfg)
	}
	if !strings.Contains(cfg, `category: "DB"`) {
		t.Fatalf("missing category:\n%s", cfg)
	}
	cfg2 := buildInstallConfigYAML("http://s:8529", "tok", "prod", "", "", "[]", installAuditOptions{}, false)
	if strings.Contains(cfg2, "folder_id:") {
		t.Fatal("empty folder_id should omit key")
	}
}
