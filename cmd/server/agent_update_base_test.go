package main

import "testing"

// TestAgentUpdateDownloadBaseFallsBackToPublicURL pins the escape hatch open.
//
// 闸门允许「支持模块的主机」以**空基址**入队（模块跑在 Agent 自己进程里，用它已配置的
// 上报地址即可），于是 job.ServerURL 常常是空的。但服务端生成的 legacy 脚本必须把 /dl
// 地址写死进正文——此前它只看 agent 上报值和 job 值，两个都空就直接判死：
//
//	"no download base is known for the legacy rescue"
//
// 而闸门在非模块主机那一支是会回退到 public_url 的。结果：只要 Agent 没上报 server_url，
// 这条专为「Agent 侧助手已损坏」准备的逃生口就永远打不开——而模块路径正是老 Windows
// Agent 上坏掉的那条。两条路同时失效 = 主机永久停在旧版本。
func TestAgentUpdateDownloadBaseFallsBackToPublicURL(t *testing.T) {
	cfg, err := NewConfigStore(t.TempDir()+"/cfg.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.mu.Lock()
	cfg.cfg.PublicURL = "https://panel.example.com/"
	cfg.mu.Unlock()
	s := &Server{cfg: cfg}

	// 1) agent 上报了基址 → 用它（relay 场景下这是唯一能通的地址）
	h := &Host{ID: "h1", ServerURL: "http://relay.lan:8529/"}
	if got := s.agentUpdateDownloadBase(h, ""); got != "http://relay.lan:8529" {
		t.Fatalf("agent-reported base must win, got %q", got)
	}

	// 2) agent 没报，但 job 带了 → 用 job 的
	if got := s.agentUpdateDownloadBase(&Host{ID: "h2"}, "http://job.example:8529/"); got != "http://job.example:8529" {
		t.Fatalf("job base should be used, got %q", got)
	}

	// 3) 两者皆空 → 必须回退到面板 public_url，而不是放弃救援
	if got := s.agentUpdateDownloadBase(&Host{ID: "h3"}, ""); got != "https://panel.example.com" {
		t.Fatalf("public_url must keep the rescue reachable, got %q", got)
	}

	// 4) 连 public_url 都没配 → 只能放弃，但这时的报错是准确的
	cfg.mu.Lock()
	cfg.cfg.PublicURL = ""
	cfg.mu.Unlock()
	if got := s.agentUpdateDownloadBase(&Host{ID: "h4"}, ""); got != "" {
		t.Fatalf("with nothing configured the base must be empty, got %q", got)
	}
}
