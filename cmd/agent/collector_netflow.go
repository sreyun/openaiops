package main

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"aiops-monitor/shared"
)

// NetFlowConfig is the agent config section for NetFlow receiver.
type NetFlowConfig struct {
	Listen         string         `json:"listen"`            // ":2055"
	Protocols      []string       `json:"protocols"`         // ["v5","v9"]
	BufferSize     int            `json:"buffer_size"`       // 65536
	WindowSec      int            `json:"window_sec"`        // 300 (5min)
	MaxFlowsPerSec int            `json:"max_flows_per_sec"` // 10000
	ActiveTargets  []ActiveTarget `json:"active_targets,omitempty"`
}

// ActiveTarget is one device to actively poll for flow stats (SNMP/REST).
type ActiveTarget struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Protocol string `json:"protocol"` // "snmp" | "rest"
	Port     int    `json:"port"`
	Interval int    `json:"interval_sec"`
}

// flowKey is the five-tuple hash key for flow aggregation.
type flowKey struct {
	SrcIP   string
	DstIP   string
	SrcPort uint16
	DstPort uint16
	Proto   uint8
}

type flowEntry struct {
	key       flowKey
	bytes     uint64
	packets   uint64
	firstSeen int64
	lastSeen  int64
	tcpFlags  uint8
	srcAS     uint32
	dstAS     uint32
	inputIf   uint32
	outputIf  uint32
}

// flowAggregator holds a time-windowed aggregation of flow records.
type flowAggregator struct {
	mu       sync.Mutex
	flows    map[flowKey]*flowEntry
	maxFlows int // memory cap
	dropped  uint64
}

func newFlowAggregator(maxFlows int) *flowAggregator {
	return &flowAggregator{
		flows:    make(map[flowKey]*flowEntry),
		maxFlows: maxFlows,
	}
}

// add inserts or merges one flow record into the aggregation window.
func (fa *flowAggregator) add(rec shared.FlowRecord) {
	fa.mu.Lock()
	defer fa.mu.Unlock()

	key := flowKey{
		SrcIP:   rec.SrcIP,
		DstIP:   rec.DstIP,
		SrcPort: rec.SrcPort,
		DstPort: rec.DstPort,
		Proto:   rec.Protocol,
	}

	if e, ok := fa.flows[key]; ok {
		e.bytes += rec.Bytes
		e.packets += rec.Packets
		e.tcpFlags |= rec.TCPFlags
		if rec.LastSeen > e.lastSeen {
			e.lastSeen = rec.LastSeen
		}
		return
	}

	// Memory cap: evict lowest-traffic entry if at capacity
	if len(fa.flows) >= fa.maxFlows {
		fa.evictMin()
	}
	fa.flows[key] = &flowEntry{
		key:       key,
		bytes:     rec.Bytes,
		packets:   rec.Packets,
		firstSeen: rec.FirstSeen,
		lastSeen:  rec.LastSeen,
		tcpFlags:  rec.TCPFlags,
		srcAS:     rec.SrcAS,
		dstAS:     rec.DstAS,
		inputIf:   rec.InputIf,
		outputIf:  rec.OutputIf,
	}
}

// evictMin removes the flow entry with the smallest byte count (must hold mu).
func (fa *flowAggregator) evictMin() {
	var minKey flowKey
	var minBytes uint64 = ^uint64(0)
	for k, v := range fa.flows {
		if v.bytes < minBytes {
			minBytes = v.bytes
			minKey = k
		}
	}
	delete(fa.flows, minKey)
	fa.dropped++
}

// flush drains the aggregator and returns the current window's flow records + stats.
func (fa *flowAggregator) flush() ([]shared.FlowRecord, shared.NetFlowStats) {
	fa.mu.Lock()
	defer fa.mu.Unlock()

	records := make([]shared.FlowRecord, 0, len(fa.flows))
	var totalBytes, totalPackets uint64
	for _, e := range fa.flows {
		records = append(records, shared.FlowRecord{
			SrcIP:     e.key.SrcIP,
			DstIP:     e.key.DstIP,
			SrcPort:   e.key.SrcPort,
			DstPort:   e.key.DstPort,
			Protocol:  e.key.Proto,
			Bytes:     e.bytes,
			Packets:   e.packets,
			FirstSeen: e.firstSeen,
			LastSeen:  e.lastSeen,
			TCPFlags:  e.tcpFlags,
			SrcAS:     e.srcAS,
			DstAS:     e.dstAS,
			InputIf:   e.inputIf,
			OutputIf:  e.outputIf,
		})
		totalBytes += e.bytes
		totalPackets += e.packets
	}

	stats := shared.NetFlowStats{
		TotalFlows:     len(records),
		TotalBytes:     totalBytes,
		TotalPackets:   totalPackets,
		DroppedPackets: fa.dropped,
	}

	// Reset window
	fa.flows = make(map[flowKey]*flowEntry)
	fa.dropped = 0

	return records, stats
}

// netflowReceiver is the NetFlow UDP listener + aggregator.
type netflowReceiver struct {
	cfg    NetFlowConfig
	hostID string
	fp     string
	agg    *flowAggregator
	conn   net.PacketConn

	// v9 template cache (sourceID+templateID → template)。仅 read-loop 单协程访问，
	// v9TemplateCount 用普通 int 计数即可（无需原子）。缓存有上限，防构造报文循环换
	// sourceID/templateID 撑爆内存。
	v9Templates     sync.Map
	v9TemplateCount int
}

// v9 模板防护上限：正常设备模板字段数 <数十、模板数 <数十；远超即视为异常/构造报文。
const (
	maxV9Fields    = 512
	maxV9Templates = 4096
)

// v9Template stores one NetFlow v9 template for decoding data flowsets.
type v9Template struct {
	templateID uint16
	fieldCount uint16
	fields     []v9Field
	recordLen  int // computed total record length in bytes
}

type v9Field struct {
	fieldType uint16
	fieldLen  uint16
}

func newNetflowReceiver(cfg NetFlowConfig, hostID, fp string) *netflowReceiver {
	maxFlows := 100000
	return &netflowReceiver{
		cfg:    cfg,
		hostID: hostID,
		fp:     fp,
		agg:    newFlowAggregator(maxFlows),
	}
}

// run starts the UDP listener and the periodic flush goroutine.
func (nr *netflowReceiver) run(reporter func(shared.NetFlowReport)) {
	addr := nr.cfg.Listen
	if addr == "" {
		addr = ":2055"
	}

	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		slog.Error("NetFlow UDP 监听失败", "addr", addr, "err", err)
		return
	}
	nr.conn = conn
	slog.Info("NetFlow 接收器启动", "addr", addr, "protocols", nr.cfg.Protocols)

	// Set read buffer
	if nr.cfg.BufferSize > 0 {
		if uc, ok := conn.(*net.UDPConn); ok {
			uc.SetReadBuffer(nr.cfg.BufferSize)
		}
	}

	// Flush goroutine
	windowSec := nr.cfg.WindowSec
	if windowSec <= 0 {
		windowSec = 300
	}
	go func() {
		ticker := time.NewTicker(time.Duration(windowSec) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			runSafe("netflow-flush", func() {
				records, stats := nr.agg.flush()
				if len(records) == 0 {
					return
				}
				reporter(shared.NetFlowReport{
					HostID:      nr.hostID,
					Fingerprint: nr.fp,
					Source:      "netflow",
					Timestamp:   time.Now().Unix(),
					WindowSec:   windowSec,
					Flows:       records,
					Stats:       stats,
				})
				slog.Info("NetFlow 聚合窗口刷新", "flows", stats.TotalFlows, "bytes", stats.TotalBytes, "dropped", stats.DroppedPackets)
			})
		}
	}()

	// Read loop
	buf := make([]byte, 65535)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			slog.Warn("NetFlow UDP 读取错误", "err", err)
			continue
		}
		if n < 4 {
			continue
		}
		runSafe("netflow-parse", func() { nr.parsePacket(buf[:n]) })
	}
}

// parsePacket dispatches by NetFlow version.
func (nr *netflowReceiver) parsePacket(data []byte) {
	if len(data) < 4 {
		return
	}
	version := binary.BigEndian.Uint16(data[0:2])
	switch version {
	case 5:
		nr.parseV5(data)
	case 9:
		nr.parseV9(data)
	default:
		slog.Warn("NetFlow 不支持的版本", "version", version)
	}
}

// parseV5 parses a NetFlow v5 packet (fixed 24-byte header + 48-byte records).
func (nr *netflowReceiver) parseV5(data []byte) {
	if len(data) < 24 {
		return
	}
	count := binary.BigEndian.Uint16(data[2:4])
	sysUptime := binary.BigEndian.Uint32(data[4:8])
	unixSecs := binary.BigEndian.Uint32(data[8:12])
	unixNsecs := binary.BigEndian.Uint32(data[12:16])
	_ = unixNsecs

	now := time.Now().Unix()

	offset := 24
	for i := uint16(0); i < count && offset+48 <= len(data); i++ {
		rec := data[offset : offset+48]
		srcIP := net.IP(rec[0:4]).String()
		dstIP := net.IP(rec[4:8]).String()
		srcPort := binary.BigEndian.Uint16(rec[8:10])
		dstPort := binary.BigEndian.Uint16(rec[10:12])
		proto := rec[13]
		tcpFlags := rec[12]
		packets := binary.BigEndian.Uint32(rec[16:20])
		bytes := binary.BigEndian.Uint32(rec[20:24])
		inputIf := binary.BigEndian.Uint16(rec[14:16])
		outputIf := binary.BigEndian.Uint16(rec[32:34])
		srcAS := binary.BigEndian.Uint16(rec[36:38])
		dstAS := binary.BigEndian.Uint16(rec[38:40])

		// Flow timing (approximate from sysUptime)
		firstMillis := binary.BigEndian.Uint32(rec[24:28])
		lastMillis := binary.BigEndian.Uint32(rec[28:32])
		var firstSeen, lastSeen int64
		if sysUptime > 0 {
			firstSeen = int64(unixSecs) - int64((sysUptime-firstMillis)/1000)
			lastSeen = int64(unixSecs) - int64((sysUptime-lastMillis)/1000)
		} else {
			firstSeen = now
			lastSeen = now
		}

		nr.agg.add(shared.FlowRecord{
			SrcIP:     srcIP,
			DstIP:     dstIP,
			SrcPort:   srcPort,
			DstPort:   dstPort,
			Protocol:  proto,
			Bytes:     uint64(bytes),
			Packets:   uint64(packets),
			FirstSeen: firstSeen,
			LastSeen:  lastSeen,
			TCPFlags:  tcpFlags,
			SrcAS:     uint32(srcAS),
			DstAS:     uint32(dstAS),
			InputIf:   uint32(inputIf),
			OutputIf:  uint32(outputIf),
		})
		offset += 48
	}
}

// parseV9 parses a NetFlow v9 packet (template-based).
func (nr *netflowReceiver) parseV9(data []byte) {
	if len(data) < 20 {
		return
	}
	count := binary.BigEndian.Uint16(data[2:4])
	// sysUptime := binary.BigEndian.Uint32(data[4:8])
	// unixSecs := binary.BigEndian.Uint32(data[8:12])
	sourceID := binary.BigEndian.Uint32(data[16:20])

	offset := 20
	for i := uint16(0); i < count && offset+4 <= len(data); i++ {
		flowSetID := binary.BigEndian.Uint16(data[offset : offset+2])
		flowSetLen := binary.BigEndian.Uint16(data[offset+2 : offset+4])
		if flowSetLen < 4 || offset+int(flowSetLen) > len(data) {
			break
		}
		flowSetData := data[offset+4 : offset+int(flowSetLen)]

		switch {
		case flowSetID == 0:
			// Template FlowSet
			nr.parseV9Template(flowSetData, sourceID)
		case flowSetID == 1:
			// Options Template FlowSet (skip for now)
		case flowSetID >= 256:
			// Data FlowSet — decode using cached template
			nr.decodeV9Data(flowSetID, flowSetData, sourceID)
		}
		offset += int(flowSetLen)
	}
}

// parseV9Template parses and caches template definitions from a Template FlowSet.
func (nr *netflowReceiver) parseV9Template(data []byte, sourceID uint32) {
	offset := 0
	for offset+4 <= len(data) {
		templateID := binary.BigEndian.Uint16(data[offset : offset+2])
		fieldCount := binary.BigEndian.Uint16(data[offset+2 : offset+4])
		offset += 4

		// 字段数上限：0 或 >512 视为异常/构造模板，停止解析（避免超大分配）。
		if fieldCount == 0 || fieldCount > maxV9Fields {
			return
		}

		// cap 用 fieldCount 但按实际读到的 append：报文被截断时不会留零值尾字段。
		fields := make([]v9Field, 0, fieldCount)
		recordLen := 0
		for j := uint16(0); j < fieldCount && offset+4 <= len(data); j++ {
			ft := binary.BigEndian.Uint16(data[offset : offset+2])
			fl := binary.BigEndian.Uint16(data[offset+2 : offset+4])
			fields = append(fields, v9Field{fieldType: ft, fieldLen: fl})
			recordLen += int(fl)
			offset += 4
		}

		tmplKey := fmt.Sprintf("%d_%d", sourceID, templateID)
		// 缓存上限：新 key 且已达上限则忽略（防循环换 templateID 撑爆内存）；已存在则更新。
		if _, exists := nr.v9Templates.Load(tmplKey); !exists {
			if nr.v9TemplateCount >= maxV9Templates {
				slog.Warn("NetFlow v9 模板缓存已达上限，忽略新模板", "source", sourceID, "template", templateID)
				continue
			}
			nr.v9TemplateCount++
		}
		nr.v9Templates.Store(tmplKey, &v9Template{
			templateID: templateID,
			fieldCount: fieldCount,
			fields:     fields,
			recordLen:  recordLen,
		})
	}
}

// decodeV9Data decodes data records using a cached template.
func (nr *netflowReceiver) decodeV9Data(templateID uint16, data []byte, sourceID uint32) {
	tmplKey := fmt.Sprintf("%d_%d", sourceID, templateID)
	val, ok := nr.v9Templates.Load(tmplKey)
	if !ok {
		return // template not yet received
	}
	tmpl := val.(*v9Template)
	if tmpl.recordLen == 0 {
		return
	}

	now := time.Now().Unix()
	for offset := 0; offset+tmpl.recordLen <= len(data); offset += tmpl.recordLen {
		rec := data[offset : offset+tmpl.recordLen]
		fr := shared.FlowRecord{FirstSeen: now, LastSeen: now}

		pos := 0
		for _, f := range tmpl.fields {
			if pos+int(f.fieldLen) > len(rec) {
				break
			}
			fieldData := rec[pos : pos+int(f.fieldLen)]
			switch f.fieldType {
			case 1: // IN_BYTES
				fr.Bytes = readUint(fieldData)
			case 2: // IN_PKTS
				fr.Packets = readUint(fieldData)
			case 4: // PROTOCOL
				if len(fieldData) > 0 {
					fr.Protocol = fieldData[0]
				}
			case 6: // TCP_FLAGS
				if len(fieldData) > 0 {
					fr.TCPFlags = fieldData[0]
				}
			case 7: // L4_SRC_PORT
				fr.SrcPort = uint16(readUint(fieldData))
			case 8: // IPV4_SRC_ADDR
				if len(fieldData) == 4 {
					fr.SrcIP = net.IP(fieldData).String()
				}
			case 10: // INPUT_SNMP
				fr.InputIf = uint32(readUint(fieldData))
			case 11: // L4_DST_PORT
				fr.DstPort = uint16(readUint(fieldData))
			case 12: // IPV4_DST_ADDR
				if len(fieldData) == 4 {
					fr.DstIP = net.IP(fieldData).String()
				}
			case 14: // OUTPUT_SNMP
				fr.OutputIf = uint32(readUint(fieldData))
			case 16: // SRC_AS
				fr.SrcAS = uint32(readUint(fieldData))
			case 17: // DST_AS
				fr.DstAS = uint32(readUint(fieldData))
			}
			pos += int(f.fieldLen)
		}
		nr.agg.add(fr)
	}
}

// readUint reads a big-endian unsigned integer of variable width (1/2/4/8 bytes).
//
// 安全：字段长度来自 v9 模板，由报文发送方声明（NetFlow UDP 无认证，攻击者/异常 exporter
// 可控）。此前 default 分支 copy(buf[8-len(data):], data) 在 len>8 时下标为负，一个把数值
// 字段声明成 len=16 的构造模板即可触发 panic；读循环无 recover → 整个 agent 崩溃被 keepalive
// 反复重启。这里先把超长数据钳到低 8 字节，杜绝越界。
func readUint(data []byte) uint64 {
	if len(data) > 8 {
		data = data[len(data)-8:] // 取低 8 字节（大端最右有效）
	}
	switch len(data) {
	case 1:
		return uint64(data[0])
	case 2:
		return uint64(binary.BigEndian.Uint16(data))
	case 4:
		return uint64(binary.BigEndian.Uint32(data))
	case 8:
		return binary.BigEndian.Uint64(data)
	default:
		// len ∈ {0,3,5,6,7}：8-len(data) 恒为正，安全。
		var buf [8]byte
		copy(buf[8-len(data):], data)
		return binary.BigEndian.Uint64(buf[:])
	}
}
