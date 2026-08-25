package main

import (
	"testing"
	"time"

	"aiops-monitor/shared"
)

// 这一组测试钉住"5000 台规模"那一轮改动里最容易被顺手改回去的几处语义。

// 进程基线只看最近 1 小时（60 个 1 分钟点），而不是整条 48 小时的环。
func TestProcBaselineWindow(t *testing.T) {
	var hist []shared.Sample
	for i := 0; i < 200; i++ {
		hist = append(hist, shared.Sample{Metrics: shared.Metrics{ProcCount: 100}})
	}
	for i := 0; i < 60; i++ {
		hist = append(hist, shared.Sample{Metrics: shared.Metrics{ProcCount: 400}})
	}
	if got := procBaseline(hist, 60); got != 400 {
		t.Fatalf("baseline should use only the last 60 points, got %v", got)
	}
	if got := procBaseline(hist[:10], 60); got != 100 {
		t.Fatalf("short history should average what it has, got %v", got)
	}
	if got := procBaseline(nil, 60); got != 0 {
		t.Fatalf("empty history must yield 0, got %v", got)
	}
}

// 进程检查的大小写折叠匹配：零分配路径要与原来 strings.ToLower+Contains 的语义一致。
func TestProbeProcessFoldMatch(t *testing.T) {
	st := NewStore()
	st.hosts["h1"] = &Host{ID: "h1", Latest: &shared.Sample{Metrics: shared.Metrics{
		ProcessNames: []string{"systemd", "Nginx.EXE", "postgres: writer"},
	}}}
	cr := newCheckRunner(newTestConfigStore(t), st, nil, "")
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"h1/nginx", true},
		{"h1/NGINX", true},
		{"h1/postgres", true},
		{"h1/redis", false},
		{"nope/nginx", false},
		{"h1", false},
	} {
		ok, _ := cr.probeProcess(tc.target)
		if ok != tc.want {
			t.Errorf("probeProcess(%q) = %v, want %v", tc.target, ok, tc.want)
		}
	}
}

// 正在执行中的检查不会被 sweep 重复派发，即便它已经跨过了自己的间隔。
func TestSweepSkipsInflight(t *testing.T) {
	cfg := newTestConfigStore(t)
	c, err := cfg.UpsertCheck(CustomCheck{Name: "p", Type: "process", Target: "h1/nginx", IntervalSec: 5, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	cr := newCheckRunner(cfg, NewStore(), nil, "")
	cr.mu.Lock()
	cr.inflight[c.ID] = true
	cr.lastRun[c.ID] = time.Now().Add(-time.Hour)
	cr.mu.Unlock()

	cr.sweep()

	cr.mu.Lock()
	last := cr.lastRun[c.ID]
	_, ran := cr.status[c.ID]
	cr.mu.Unlock()
	if ran || time.Since(last) < time.Minute {
		t.Fatalf("in-flight check must not be re-dispatched (ran=%v lastRun=%v)", ran, last)
	}

	cr.clearInflight(c.ID)
	cr.sweep()
	cr.mu.Lock()
	_, ran = cr.status[c.ID]
	cr.mu.Unlock()
	if !ran {
		t.Fatal("check should run once it is no longer in flight")
	}
}

// 经典控制台的 app.js 只拼一次，且带稳定的 ETag。
func TestClassicAppJSCached(t *testing.T) {
	b1, e1, miss := classicAppJS()
	if miss != "" {
		t.Fatalf("module missing: %s", miss)
	}
	if len(b1) == 0 || e1 == "" {
		t.Fatal("empty bundle or etag")
	}
	b2, e2, _ := classicAppJS()
	if &b1[0] != &b2[0] || e1 != e2 {
		t.Fatal("bundle must be built once and reused")
	}
}
