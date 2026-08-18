package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"aiops-monitor/shared"
)

// PacketConfig is the agent config section for five-tuple packet capture.
type PacketConfig struct {
	Enabled          bool   `json:"enabled"`
	Interface        string `json:"interface"`
	BPFFilter        string `json:"bpf_filter"`
	SampleRate       int    `json:"sample_rate"`
	MaxPacketsPerMin int    `json:"max_packets_per_min"`
}

// packetCollector reads /proc/net/nf_conntrack periodically and computes
// incremental flow records by diffing against the previous snapshot.
// Linux-only; on other platforms this is a no-op.
type packetCollector struct {
	cfg    PacketConfig
	hostID string
	fp     string

	prevSnapshot map[string]conntrackEntry
}

type conntrackEntry struct {
	srcIP   string
	dstIP   string
	srcPort uint16
	dstPort uint16
	proto   uint8
	state   string
	bytes   uint64
	packets uint64
	lastSeen int64
}

func newPacketCollector(cfg PacketConfig, hostID, fp string) *packetCollector {
	return &packetCollector{
		cfg:          cfg,
		hostID:       hostID,
		fp:           fp,
		prevSnapshot: make(map[string]conntrackEntry),
	}
}

// ensureConntrackAcct 尽力开启 nf_conntrack 的字节/包计数。默认关闭时 /proc/net/nf_conntrack
// 的行不含 bytes=/packets=，flow 明细流量就无从统计。以 root 运行时写 sysctl 开启（可逆、
// 低风险，是本采集器正常工作的前提）；无权限则打印手动命令，不报错中断。
func ensureConntrackAcct() {
	const path = "/proc/sys/net/netfilter/nf_conntrack_acct"
	cur, err := os.ReadFile(path)
	if err != nil {
		return // 内核未加载 conntrack 或路径不存在，静默跳过
	}
	if strings.TrimSpace(string(cur)) == "1" {
		return // 已开启
	}
	if err := os.WriteFile(path, []byte("1"), 0o644); err == nil {
		slog.Info("已开启 nf_conntrack_acct（字节/包计数），flow 明细流量统计生效")
	} else {
		slog.Warn("nf_conntrack_acct 未开启且无权限自动开启，flow 明细的字节/包将为 0",
			"hint", "以 root 执行: sysctl -w net.netfilter.nf_conntrack_acct=1（可写入 /etc/sysctl.d/ 持久化）")
	}
}

// run starts the periodic nf_conntrack polling loop.
func (pc *packetCollector) run(reporter func(shared.NetFlowReport)) {
	if runtime.GOOS != "linux" {
		slog.Info("五元组包采集器: 仅支持 Linux (nf_conntrack)，当前平台跳过", "os", runtime.GOOS)
		return
	}
	if !pc.cfg.Enabled {
		return
	}
	slog.Info("五元组包采集器启动 (nf_conntrack)", "interval", "30s")
	ensureConntrackAcct()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		entries, err := pc.readConntrack()
		if err != nil {
			slog.Warn("读取 nf_conntrack 失败", "err", err)
			continue
		}

		flows := pc.diff(entries)
		if len(flows) == 0 {
			continue
		}

		// Rate limit: cap at MaxPacketsPerMin (default 6000)
		maxPkt := pc.cfg.MaxPacketsPerMin
		if maxPkt <= 0 {
			maxPkt = 6000
		}
		if len(flows) > maxPkt {
			flows = flows[:maxPkt]
		}

		var totalBytes, totalPackets uint64
		for _, f := range flows {
			totalBytes += f.Bytes
			totalPackets += f.Packets
		}

		reporter(shared.NetFlowReport{
			HostID:      pc.hostID,
			Fingerprint: pc.fp,
			Source:      "packet",
			Timestamp:   time.Now().Unix(),
			WindowSec:   30,
			Flows:       flows,
			Stats: shared.NetFlowStats{
				TotalFlows:   len(flows),
				TotalBytes:   totalBytes,
				TotalPackets: totalPackets,
			},
		})
	}
}

// readConntrack parses /proc/net/nf_conntrack and returns a map of current entries.
func (pc *packetCollector) readConntrack() (map[string]conntrackEntry, error) {
	f, err := os.Open("/proc/net/nf_conntrack")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	entries := make(map[string]conntrackEntry)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		entry, ok := parseConntrackLine(line)
		if !ok {
			continue
		}
		key := conntrackKey(entry)
		entries[key] = entry
	}

	return entries, scanner.Err()
}

// parseConntrackLine parses one line of /proc/net/nf_conntrack.
// Example: ipv4  2 tcp  6 431999 ESTABLISHED src=10.0.0.1 dst=10.0.0.2 sport=443 dport=52341 ...
func parseConntrackLine(line string) (conntrackEntry, bool) {
	var e conntrackEntry
	e.lastSeen = time.Now().Unix()

	fields := strings.Fields(line)
	if len(fields) < 6 {
		return e, false
	}

	// Detect protocol: ipv4/ipv6 + proto number
	protoStr := ""
	for _, f := range fields {
		switch f {
		case "tcp":
			e.proto = 6
			protoStr = "tcp"
		case "udp":
			e.proto = 17
			protoStr = "udp"
		case "icmp":
			e.proto = 1
			protoStr = "icmp"
		}
	}
	_ = protoStr

	// Extract key=value pairs
	for _, f := range fields {
		if !strings.Contains(f, "=") {
			// Check for state (ESTABLISHED, TIME_WAIT, etc.)
			switch f {
			case "ESTABLISHED", "TIME_WAIT", "CLOSE_WAIT", "SYN_SENT", "SYN_RECV",
				"FIN_WAIT", "CLOSE", "LAST_ACK", "LISTEN", "UNREPLIED", "ASSURED":
				e.state = f
			}
			continue
		}
		kv := strings.SplitN(f, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k, v := kv[0], kv[1]
		switch k {
		case "src":
			if e.srcIP == "" {
				e.srcIP = v
			}
		case "dst":
			if e.dstIP == "" {
				e.dstIP = v
			}
		case "sport":
			if e.srcPort == 0 {
				if p, err := strconv.ParseUint(v, 10, 16); err == nil {
					e.srcPort = uint16(p)
				}
			}
		case "dport":
			if e.dstPort == 0 {
				if p, err := strconv.ParseUint(v, 10, 16); err == nil {
					e.dstPort = uint16(p)
				}
			}
		case "bytes":
			// 真正解析！此前这里只有注释、没有代码，所以哪怕 conntrack 行里带了
			// bytes=/packets=（开启 nf_conntrack_acct 时）也被直接丢弃，导致 flow 明细的
			// 字节/包永远为 0。conntrack 每条有两组方向元组（orig + reply）各带一份，
			// += 累加得到双向总量。
			if b, err := strconv.ParseUint(v, 10, 64); err == nil {
				e.bytes += b
			}
		case "packets":
			if p, err := strconv.ParseUint(v, 10, 64); err == nil {
				e.packets += p
			}
		}
	}

	if e.srcIP == "" || e.dstIP == "" {
		return e, false
	}
	return e, true
}

func conntrackKey(e conntrackEntry) string {
	return fmt.Sprintf("%s:%d->%s:%d/%d", e.srcIP, e.srcPort, e.dstIP, e.dstPort, e.proto)
}

// diff computes incremental flow records between the current and previous snapshot.
func (pc *packetCollector) diff(current map[string]conntrackEntry) []shared.FlowRecord {
	var flows []shared.FlowRecord
	now := time.Now().Unix()

	for key, cur := range current {
		prev, existed := pc.prevSnapshot[key]
		if !existed {
			// New connection
			flows = append(flows, shared.FlowRecord{
				SrcIP:     cur.srcIP,
				DstIP:     cur.dstIP,
				SrcPort:   cur.srcPort,
				DstPort:   cur.dstPort,
				Protocol:  cur.proto,
				Bytes:     cur.bytes,
				Packets:   cur.packets,
				FirstSeen: now,
				LastSeen:  now,
			})
		} else {
			// Existing connection: compute delta
			var deltaBytes, deltaPackets uint64
			if cur.bytes > prev.bytes {
				deltaBytes = cur.bytes - prev.bytes
			}
			if cur.packets > prev.packets {
				deltaPackets = cur.packets - prev.packets
			}
			if deltaBytes > 0 || deltaPackets > 0 {
				flows = append(flows, shared.FlowRecord{
					SrcIP:     cur.srcIP,
					DstIP:     cur.dstIP,
					SrcPort:   cur.srcPort,
					DstPort:   cur.dstPort,
					Protocol:  cur.proto,
					Bytes:     deltaBytes,
					Packets:   deltaPackets,
					FirstSeen: prev.lastSeen,
					LastSeen:  now,
				})
			}
		}
	}

	// Update snapshot
	pc.prevSnapshot = current
	return flows
}
