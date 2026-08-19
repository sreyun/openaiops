package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestSanitizeResourceKey(t *testing.T) {
	cases := map[string]string{
		"hyperv:8f3c-guid":       "hyperv:8f3c-guid",
		"  hyperv:x  ":           "hyperv:x",
		"":                       "",
		"a/b":                    "", // 会被 mux 拆成两段，匹配不上
		"bad\nkey":               "", // 控制字符：会污染审计与配置
		strings.Repeat("k", 201): "",
	}
	for in, want := range cases {
		if got := sanitizeResourceKey(in); got != want {
			t.Errorf("sanitizeResourceKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResourceNoteRoundTripAndDelete(t *testing.T) {
	cs := testConfigStore(t)
	key := "hyperv:vm-guid-1"

	if err := cs.setResourceNote(key, ResourceNote{Alias: "订单库主库", Note: "勿动"}, "alice"); err != nil {
		t.Fatal(err)
	}
	got := cs.resourceNotesSnapshot()[key]
	if got.Alias != "订单库主库" || got.Note != "勿动" {
		t.Fatalf("存回来的内容不对: %+v", got)
	}
	if got.UpdatedBy != "alice" || got.UpdatedAt == 0 {
		t.Errorf("缺少改动留痕: %+v", got)
	}

	// 三项清空 = 删除：界面上"清空并保存"就是删除，不该再要一个删除按钮。
	if err := cs.setResourceNote(key, ResourceNote{}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.resourceNotesSnapshot()[key]; ok {
		t.Error("清空后应删除该条")
	}
	// 删一条本来就不存在的，不算错
	if err := cs.setResourceNote("hyperv:nope", ResourceNote{}, "alice"); err != nil {
		t.Errorf("删除不存在的注解不该报错: %v", err)
	}
}

// 注解跟着配置整份读写，没有上限会把配置文件撑成负担；到顶时只拒新增，更新已有的仍放行。
func TestResourceNotesLimit(t *testing.T) {
	cs := testConfigStore(t)
	cs.mu.Lock()
	cs.cfg.ResourceNotes = make(map[string]ResourceNote, maxResourceNotes)
	for i := 0; i < maxResourceNotes; i++ {
		cs.cfg.ResourceNotes[string(rune('a'+i%26))+"-"+strings.Repeat("x", i%7)+"-"+strconv.Itoa(i)] = ResourceNote{Alias: "a"}
	}
	existing := ""
	for k := range cs.cfg.ResourceNotes {
		existing = k
		break
	}
	cs.mu.Unlock()

	if err := cs.setResourceNote("brand:new", ResourceNote{Alias: "x"}, "bob"); err == nil {
		t.Error("到达上限后应拒绝新增")
	}
	if err := cs.setResourceNote(existing, ResourceNote{Alias: "改个名"}, "bob"); err != nil {
		t.Errorf("已存在的条目仍应可更新: %v", err)
	}
}

func TestSanitizeResourceNoteTrims(t *testing.T) {
	in := ResourceNote{
		Alias: strings.Repeat("长", 100),
		Note:  strings.Repeat("备", 600),
		Tags:  []string{"a", "a", " ", strings.Repeat("t", 50), "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"},
	}
	out := sanitizeResourceNote(in)
	if len([]rune(out.Alias)) != maxResourceAliasLen {
		t.Errorf("别名未截断: %d", len([]rune(out.Alias)))
	}
	if len([]rune(out.Note)) != maxResourceNoteLen {
		t.Errorf("备注未截断: %d", len([]rune(out.Note)))
	}
	if len(out.Tags) > maxResourceTags {
		t.Errorf("标签数超限: %d", len(out.Tags))
	}
	for _, tag := range out.Tags {
		if strings.TrimSpace(tag) == "" {
			t.Error("空标签应被丢弃")
		}
		if len([]rune(tag)) > maxResourceTagLen {
			t.Errorf("标签未截断: %q", tag)
		}
	}
	if out.Tags[0] != "a" || (len(out.Tags) > 1 && out.Tags[1] == "a") {
		t.Errorf("标签未去重: %v", out.Tags)
	}
}
