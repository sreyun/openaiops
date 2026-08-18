package main

import (
	"fmt"
	"strings"
	"testing"

	"aiops-monitor/shared"
)

// NetFlow 指标的成败标准只有一个：**序列数必须有上界**。
// 老实现按五元组打 label，src_port 是临时端口 → 每条 flow 一条新序列 →
// 时序库几天就被拖垮。这些测试守住"基数封顶"这条命。

func mkFlows(n int) []shared.FlowRecord {
	out := make([]shared.FlowRecord, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, shared.FlowRecord{
			SrcIP: "10.0.0.1",
			DstIP: fmt.Sprintf("10.1.%d.%d", i/250, i%250), // 大量不同对端
			// 临时端口：老实现下这一项就足以让每条 flow 变成一条独立序列
			SrcPort:  uint16(30000 + i%20000),
			DstPort:  443,
			Protocol: 6,
			Bytes:    uint64(1000 + i),
			Packets:  uint64(10 + i),
		})
	}
	return out
}

func TestNetflowRollupBoundsCardinality(t *testing.T) {
	const flows = 5000
	rep := shared.NetFlowReport{
		HostID: "h1", Source: "netflow", Timestamp: 1784000000,
		Flows: mkFlows(flows),
	}

	agg := rollupNetFlow("h1", "", rep)
	// 上界 = 3 条总量 + Top-N 对端 + Top-N 端口
	maxSeries := 3 + netflowTopN*2
	if len(agg) > maxSeries {
		t.Fatalf("%d 条 flow 产出 %d 条序列，上界应为 %d —— 基数没封住", flows, len(agg), maxSeries)
	}
	// 老实现会是 flows*2 = 10000 条，这里必须远小于它
	if len(agg) >= flows {
		t.Fatalf("序列数 %d 与 flow 数同阶，等于没聚合", len(agg))
	}

	joined := strings.Join(agg, "\n")
	// src_port 是罪魁祸首，绝不能出现在任何 label 里
	if strings.Contains(joined, "src_port=") {
		t.Error("指标里仍带 src_port —— 这正是基数爆炸的根源")
	}
	// 总量必须准确：聚合不能把数据算丢
	var wantBytes uint64
	for _, f := range rep.Flows {
		wantBytes += f.Bytes
	}
	if !strings.Contains(joined, fmt.Sprintf("aiops_netflow_total_bytes{host=\"h1\",source=\"netflow\"} %d", wantBytes)) {
		t.Errorf("总字节数不对，期望 %d。实际输出:\n%s", wantBytes, firstLines(joined, 5))
	}
}

func TestNetflowRollupPicksTopPeers(t *testing.T) {
	rep := shared.NetFlowReport{
		HostID: "h1", Source: "netflow", Timestamp: 1784000000,
		Flows: []shared.FlowRecord{
			{SrcIP: "10.0.0.1", DstIP: "1.1.1.1", SrcPort: 40000, DstPort: 443, Protocol: 6, Bytes: 100},
			{SrcIP: "10.0.0.1", DstIP: "2.2.2.2", SrcPort: 40001, DstPort: 443, Protocol: 6, Bytes: 9000},
			{SrcIP: "10.0.0.1", DstIP: "2.2.2.2", SrcPort: 40002, DstPort: 443, Protocol: 6, Bytes: 1000},
		},
	}
	agg := rollupNetFlow("h1", "10.0.0.1", rep)
	joined := strings.Join(agg, "\n")

	// 同一对端的多条 flow 要合并：2.2.2.2 应是 9000+1000=10000
	if !strings.Contains(joined, `aiops_netflow_peer_bytes{host="h1",peer="2.2.2.2",source="netflow"} 10000`) {
		t.Errorf("对端流量未正确合并:\n%s", joined)
	}
	// 服务端口取两端较小者：40000 是临时端口，443 才是服务端口
	if !strings.Contains(joined, `port="443"`) {
		t.Errorf("未按服务端口 443 聚合:\n%s", joined)
	}
	if strings.Contains(joined, `port="40000"`) {
		t.Error("把临时端口当成服务端口了")
	}
}

// 入向流量：本机是目的端时，对端应是**源** IP，否则 Top talkers 全变成自己。
func TestNetflowRollupInboundPeerIsSource(t *testing.T) {
	rep := shared.NetFlowReport{
		HostID: "h1", Source: "netflow", Timestamp: 1784000000,
		Flows: []shared.FlowRecord{
			{SrcIP: "203.0.113.9", DstIP: "10.0.0.1", SrcPort: 50000, DstPort: 22, Protocol: 6, Bytes: 500},
		},
	}
	agg := rollupNetFlow("h1", "10.0.0.1", rep)
	joined := strings.Join(agg, "\n")
	if !strings.Contains(joined, `peer="203.0.113.9"`) {
		t.Errorf("入向流量的对端应是源 IP:\n%s", joined)
	}
	if strings.Contains(joined, `peer="10.0.0.1"`) {
		t.Error("把本机 IP 当成了对端")
	}
}

func firstLines(s string, n int) string {
	ls := strings.Split(s, "\n")
	if len(ls) > n {
		ls = ls[:n]
	}
	return strings.Join(ls, "\n")
}
