package main

import (
	"net/http"
	"testing"
	"time"
)

// 出站连接池的性质属于「坏了也不报错」：连接不复用只表现为更慢，没有任何日志、
// 没有任何失败。这里把两条容易被无声改回去的性质钉住。

// 受 SSRF 守卫的客户端必须**共用**同一个 Transport。
//
// 原来每次调用都新建 Transport，而连接池就挂在 Transport 上——用完即弃等于连接零复用：
// AI 的每一次对话/函数调用/向量检索都要重新握一次 TCP + TLS。
func TestGuardedClientsShareOneTransport(t *testing.T) {
	a := newGuardedHTTPClient(5 * time.Second)
	b := newGuardedHTTPClient(30 * time.Second)
	if a.Transport == nil || b.Transport == nil {
		t.Fatal("guarded client must not fall back to http.DefaultTransport")
	}
	if a.Transport != b.Transport {
		t.Fatal("每次调用都新建 Transport = 连接零复用；必须共用同一个连接池")
	}
	// 超时仍然各自独立——共用连接池不该把超时也绑死。
	if a.Timeout == b.Timeout {
		t.Fatalf("per-call timeout lost: %v == %v", a.Timeout, b.Timeout)
	}
	tr, ok := a.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", a.Transport)
	}
	if tr.DialContext == nil {
		t.Fatal("SSRF 守卫挂在 DialContext 的 Control 上，不能丢")
	}
	// per-host 才是真正起作用的上限：出站全打向少数几个端点。
	if tr.MaxIdleConnsPerHost <= 2 {
		t.Fatalf("MaxIdleConnsPerHost=%d，等同于 net/http 默认值 2", tr.MaxIdleConnsPerHost)
	}
}

// VictoriaMetrics 客户端同理：全部时序读写打向同一台 VM，per-host 上限是 2 时，
// 一次看板刷新的十几条并发查询里超出的部分每次都要新建连接。
func TestVMClientHasTunedPool(t *testing.T) {
	tr := newVMTransport()
	if tr.MaxIdleConnsPerHost <= 2 {
		t.Fatalf("MaxIdleConnsPerHost=%d，等同于默认值 2", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxIdleConns < tr.MaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConns=%d 小于 per-host 上限 %d，per-host 设置形同虚设",
			tr.MaxIdleConns, tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout <= 0 {
		t.Fatal("IdleConnTimeout 必须设置，否则空闲连接不回收")
	}
	w := newVMWriter(&ConfigStore{})
	if w.httpc.Transport == nil {
		t.Fatal("VM 客户端回落到了 http.DefaultTransport（每主机仅 2 条空闲连接）")
	}
}
