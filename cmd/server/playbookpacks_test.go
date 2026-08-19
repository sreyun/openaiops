package main

import (
	"strings"
	"testing"
)

// 内置剧本包是**出厂内容**：用户导入后直接对着生产机群跑。一个模块名拼错、一个变更类
// 模块混进只读包，都不会在编译期暴露，只会在别人的机器上暴露。这组测试把它们钉住。
func TestEmbeddedPlaybookPacksAreValid(t *testing.T) {
	packs, err := listEmbeddedPlaybookPacks()
	if err != nil {
		t.Fatalf("列出内置剧本包失败: %v", err)
	}
	if len(packs) == 0 {
		t.Fatal("没有内置剧本包")
	}
	seenPack := map[string]bool{}
	for _, info := range packs {
		if seenPack[info.ID] {
			t.Fatalf("剧本包 ID 重复: %s", info.ID)
		}
		seenPack[info.ID] = true

		pack, err := loadEmbeddedPlaybookPack(info.ID)
		if err != nil {
			t.Fatalf("加载剧本包 %s 失败: %v", info.ID, err)
		}
		if len(pack.Playbooks) == 0 {
			t.Fatalf("剧本包 %s 里没有剧本", info.ID)
		}
		seenPB := map[string]bool{}
		for _, pb := range pack.Playbooks {
			if strings.TrimSpace(pb.ID) == "" || strings.TrimSpace(pb.Name) == "" {
				t.Fatalf("%s: 剧本缺 id 或 name: %+v", info.ID, pb)
			}
			if seenPB[pb.ID] {
				t.Fatalf("%s: 剧本 ID 重复: %s", info.ID, pb.ID)
			}
			seenPB[pb.ID] = true
			// description 是用户在导入列表里唯一能读到的东西；空描述等于让人盲选。
			if strings.TrimSpace(pb.Description) == "" {
				t.Fatalf("%s/%s: 缺 description", info.ID, pb.ID)
			}
			if len(pb.Steps) == 0 {
				t.Fatalf("%s/%s: 没有步骤", info.ID, pb.ID)
			}
			for i, st := range pb.Steps {
				if strings.TrimSpace(st.Name) == "" {
					t.Fatalf("%s/%s: 第 %d 步缺 name", info.ID, pb.ID, i+1)
				}
				if strings.TrimSpace(st.Module) == "" && strings.TrimSpace(st.Command) == "" {
					t.Fatalf("%s/%s/%s: 既没有 module 也没有 command", info.ID, pb.ID, st.Name)
				}
				// 模块名必须在服务端目录里，且 Agent 端有对应实现（两侧同名约定）。
				if st.Module != "" {
					if err := validatePlaybookModule(st); err != nil {
						t.Fatalf("%s/%s/%s: %v", info.ID, pb.ID, st.Name, err)
					}
				}
				if st.TimeoutSec <= 0 {
					t.Fatalf("%s/%s/%s: timeout_sec 必须为正", info.ID, pb.ID, st.Name)
				}
			}
		}
	}
}

// 名字里写着「只读」的剧本包，每一步都必须真的是只读模块。
//
// 这条不是形式主义：只读巡检包是用户最敢在生产上一键全机群跑的东西——正因为它承诺
// 不改任何状态。混进一个 service/package/copy，破坏的是这个承诺本身。
func TestReadOnlyPacksContainOnlyReadOnlyModules(t *testing.T) {
	for _, packID := range []string{"inspect", "java"} {
		pack, err := loadEmbeddedPlaybookPack(packID)
		if err != nil {
			t.Fatalf("加载 %s 失败: %v", packID, err)
		}
		for _, pb := range pack.Playbooks {
			for _, st := range pb.Steps {
				if st.Module == "" {
					t.Fatalf("%s/%s/%s: 只读包里不应出现裸命令步骤（无法静态判定是否只读）", packID, pb.ID, st.Name)
				}
				meta, ok := knownPlaybookModules[st.Module]
				if !ok {
					t.Fatalf("%s/%s/%s: 未知模块 %s", packID, pb.ID, st.Name, st.Module)
				}
				if !meta.ReadOnly {
					t.Fatalf("%s/%s/%s: 模块 %s 会修改系统，不能出现在只读包里", packID, pb.ID, st.Name, st.Module)
				}
			}
		}
	}
}

// java_heap_histo 的 live=1 会触发一次 Full GC（STW）。内置的只读剧本绝不能默认打开它——
// 一次"只读巡检"把生产 JVM 停顿几秒，是这批模块里唯一能造成业务影响的动作。
func TestJavaPackNeverForcesFullGC(t *testing.T) {
	pack, err := loadEmbeddedPlaybookPack("java")
	if err != nil {
		t.Fatalf("加载 java 包失败: %v", err)
	}
	for _, pb := range pack.Playbooks {
		for _, st := range pb.Steps {
			if st.Module != "java_heap_histo" {
				continue
			}
			if st.Args != nil && strings.TrimSpace(st.Args["live"]) == "1" {
				t.Fatalf("%s/%s: 内置剧本把 java_heap_histo 的 live 打开了，会在生产上触发 Full GC 停顿", pb.ID, st.Name)
			}
		}
	}
}

// 新增的 AI 闭环任务必须真的有专用提示词。
//
// buildAssistSystemPrompt 的 switch 落到 default 时不会报错，只会给一段通用提示——
// 于是"巡检诊断"退化成泛泛而谈的建议，而没有任何征兆说明提示词根本没接上。
func TestClosedLoopAssistPromptsExist(t *testing.T) {
	for _, task := range []string{"inspect_diagnosis", "java_diagnosis", "inspect_remediation", "host_inspect_analysis"} {
		if !validAssistTaskName(task) {
			t.Fatalf("任务名 %q 不合法", task)
		}
		got := buildAssistSystemPrompt(task, "ctx-probe")
		fallback := buildAssistSystemPrompt("definitely_not_a_task", "ctx-probe")
		if got == fallback {
			t.Fatalf("任务 %s 没有专用提示词，落到了默认分支", task)
		}
		if !strings.Contains(got, "ctx-probe") {
			t.Fatalf("任务 %s 的提示词没有注入上下文", task)
		}
	}
}

// 诊断类提示词必须要求给出「修复建议」与「后续指引」——这正是此前巡检做完就断掉的两段。
func TestInspectDiagnosisDemandsFixAndNextSteps(t *testing.T) {
	for _, task := range []string{"inspect_diagnosis", "host_inspect_analysis"} {
		p := buildAssistSystemPrompt(task, "")
		for _, must := range []string{"【结论】", "【异常项】", "【根因】", "【修复建议】", "【后续指引】"} {
			if !strings.Contains(p, must) {
				t.Fatalf("任务 %s 的提示词缺少 %s 段", task, must)
			}
		}
	}
	jp := buildAssistSystemPrompt("java_diagnosis", "")
	for _, must := range []string{"【GC】", "【内存】", "【线程】", "【异常】", "【处置与调优】"} {
		if !strings.Contains(jp, must) {
			t.Fatalf("java_diagnosis 提示词缺少 %s 段", must)
		}
	}
}
