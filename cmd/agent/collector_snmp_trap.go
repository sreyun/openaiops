package main

// SNMP Trap 接收器（agent 侧）。监听 UDP:162，解析 v1/v2c trap，归一成事件后周期批量
// 上报服务端。结构与 netflowReceiver 同构（UDP 监听 + 周期 flush + mutex 保护 batch）。

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"aiops-monitor/shared"
)

const (
	snmpTrapFlushSec = 5    // flush 周期秒
	snmpTrapBatchCap = 5000 // batch 上限，防慢 server 时无界增长
)

type snmpTrapReceiver struct {
	cfg    SNMPConfig
	hostID string
	fp     string
	conn   net.PacketConn

	mu    sync.Mutex
	batch []shared.SNMPTrapEvent
}

func newSNMPTrapReceiver(cfg SNMPConfig, hostID, fp string) *snmpTrapReceiver {
	return &snmpTrapReceiver{cfg: cfg, hostID: hostID, fp: fp}
}

// run 监听 :162，读循环解析 trap，周期 flush 上报。
func (tr *snmpTrapReceiver) run(reporter func(shared.SNMPTrapReport)) {
	addr := tr.cfg.TrapListen
	if addr == "" {
		addr = ":162"
	}
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		slog.Error("SNMP Trap UDP 监听失败", "addr", addr, "err", err,
			"hint", "162 是特权端口，需管理员/root 权限，或改用非特权端口如 :1162")
		return
	}
	tr.conn = conn
	slog.Info("SNMP Trap 接收器启动", "addr", addr)

	go func() {
		ticker := time.NewTicker(snmpTrapFlushSec * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			runSafe("snmp-trap-flush", func() { tr.flush(reporter) })
		}
	}()

	buf := make([]byte, 65535)
	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			// 仅连接关闭时退出。瞬时读错误必须 continue，否则接收器永久静默失效——
			// Windows 上尤其危险：v3 inform 回包发给不可达主机后，ICMP port-unreachable
			// 会让下一次 ReadFrom 返回 WSAECONNRESET(10054)，此前的 return 会直接杀死接收器。
			if errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Warn("SNMP Trap UDP 读取错误，继续", "err", err)
			continue
		}
		if n < 2 {
			continue
		}
		srcIP := ""
		if ua, ok := src.(*net.UDPAddr); ok {
			srcIP = ua.IP.String()
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		runSafe("snmp-trap-parse", func() { tr.parseTrap(data, srcIP, src) })
	}
}

// enqueue 归一后的事件入 batch（受上限保护），并记一条日志。v1/v2c/v3 共用。
func (tr *snmpTrapReceiver) enqueue(ev shared.SNMPTrapEvent, srcIP string) {
	ev.SourceIP = srcIP
	ev.Timestamp = time.Now().Unix()
	tr.mu.Lock()
	if len(tr.batch) < snmpTrapBatchCap {
		tr.batch = append(tr.batch, ev)
	}
	tr.mu.Unlock()
	slog.Info("收到 SNMP Trap", "src", srcIP, "version", ev.Version, "trap_oid", ev.TrapOID, "severity", ev.Severity)
}

// communityAllowed 空白名单=全收，否则精确匹配。
func (tr *snmpTrapReceiver) communityAllowed(c string) bool {
	if len(tr.cfg.TrapCommunities) == 0 {
		return true
	}
	for _, allowed := range tr.cfg.TrapCommunities {
		if allowed == c {
			return true
		}
	}
	return false
}

// parseTrap 判版本分派 v1/v2c/v3 解析，归一后入 batch。
func (tr *snmpTrapReceiver) parseTrap(data []byte, srcIP string, srcAddr net.Addr) {
	ver, rest, err := parseMessageHeader(data)
	if err != nil {
		slog.Warn("SNMP Trap 报文头解析失败", "src", srcIP, "err", err)
		return
	}
	// v3：结构与 v1/v2c 完全不同（USM 安全参数 + ScopedPDU），单独走 USM 验签/解密路径。
	if ver == 3 {
		tr.parseV3Trap(data, srcIP, srcAddr)
		return
	}
	// v1/v2c 都在 version 后跟 community
	ctag, cc, prest, err := readTLV(rest)
	if err != nil || ctag != tagOctetString {
		slog.Warn("SNMP Trap community 解析失败", "src", srcIP)
		return
	}
	community := string(cc)
	if !tr.communityAllowed(community) {
		slog.Warn("SNMP Trap community 不在白名单，丢弃", "src", srcIP)
		return
	}
	ptag, pc, _, err := readTLV(prest)
	if err != nil {
		slog.Warn("SNMP Trap PDU 解析失败", "src", srcIP, "err", err)
		return
	}

	var ev shared.SNMPTrapEvent
	switch {
	case ver == 0 && ptag == pduTrapV1:
		ev = parseV1Trap(pc, srcIP, community)
	case ver == 1 && ptag == pduTrapV2:
		ev = parseV2Trap(pc, srcIP, community)
	default:
		slog.Warn("SNMP Trap 不支持的版本/类型", "src", srcIP, "version", ver, "pdu_tag", fmt.Sprintf("%#x", ptag))
		return
	}
	tr.enqueue(ev, srcIP)
}

// parseV1Trap 解析 v1 Trap-PDU 并按 RFC 3584 归一 trapOID。
func parseV1Trap(content []byte, srcIP, community string) shared.SNMPTrapEvent {
	ev := shared.SNMPTrapEvent{Version: "1", Community: community, SourceIP: srcIP}
	// enterprise (OID)
	tag, c, rest, err := readTLV(content)
	if err != nil {
		return ev
	}
	if tag == tagOID {
		if eoid, e := decodeOIDValue(c); e == nil {
			ev.Enterprise = oidToString(eoid)
		}
	}
	// agent-addr (IpAddress, 4B)
	if _, c, rest, err = readTLV(rest); err == nil && len(c) == 4 {
		ev.AgentAddr = fmt.Sprintf("%d.%d.%d.%d", c[0], c[1], c[2], c[3])
	}
	// generic-trap
	generic := 0
	if _, c, rest, err = readTLV(rest); err == nil {
		g, _ := decodeInteger(c)
		generic = int(g)
	}
	ev.GenericTrap = generic
	// specific-trap
	specific := 0
	if _, c, rest, err = readTLV(rest); err == nil {
		s, _ := decodeInteger(c)
		specific = int(s)
	}
	ev.SpecificTrap = specific
	// time-stamp (TimeTicks)
	if _, c, rest, err = readTLV(rest); err == nil {
		u, _ := decodeUnsigned(c)
		ev.UptimeSec = float64(u) / 100
	}
	// variable-bindings
	if tag, c, _, err = readTLV(rest); err == nil && tag == tagSequence {
		if vbs, e := parseVarbinds(c); e == nil {
			ev.Varbinds = toSharedVarbinds(vbs)
		}
	}

	ev.TrapOID = normalizeV1TrapOID(ev.Enterprise, generic, specific)
	if ev.AgentAddr == "" || ev.AgentAddr == "0.0.0.0" {
		ev.AgentAddr = srcIP
	}
	ev.Severity = inferSeverity(ev.TrapOID)
	return ev
}

// parseV2Trap 解析 v2c SNMPv2-Trap-PDU（标准 PDU 结构）。
func parseV2Trap(content []byte, srcIP, community string) shared.SNMPTrapEvent {
	ev := shared.SNMPTrapEvent{Version: "2c", Community: community, SourceIP: srcIP}
	p, err := parsePDU(pduTrapV2, content)
	if err != nil {
		return ev
	}
	fillTrapFromPDU(&ev, p)
	return ev
}

// fillTrapFromPDU 从标准 PDU(v2c-Trap / Inform)提取 trapOID/uptime/负载 varbinds 并
// 推断严重度。v2c 与 v3 共用——约定 varbind[0]=sysUpTime.0、varbind[1]=snmpTrapOID.0。
func fillTrapFromPDU(ev *shared.SNMPTrapEvent, p pdu) {
	sysUpTimeOID := []uint32{1, 3, 6, 1, 2, 1, 1, 3, 0}
	snmpTrapOID := []uint32{1, 3, 6, 1, 6, 3, 1, 1, 4, 1, 0}
	var payload []varbind
	for _, vb := range p.varbinds {
		switch {
		case oidEqual(vb.oid, sysUpTimeOID):
			ev.UptimeSec = float64(vb.value.Uint) / 100
		case oidEqual(vb.oid, snmpTrapOID):
			ev.TrapOID = oidToString(vb.value.OID)
		default:
			payload = append(payload, vb)
		}
	}
	ev.Varbinds = toSharedVarbinds(payload)
	ev.Severity = inferSeverity(ev.TrapOID)
}

// normalizeV1TrapOID 按 RFC 3584 把 v1 generic/specific 映射为标准 trapOID。
func normalizeV1TrapOID(enterprise string, generic, specific int) string {
	if generic >= 0 && generic <= 5 {
		// coldStart/warmStart/linkDown/linkUp/authFailure/egpNeighborLoss
		return fmt.Sprintf("1.3.6.1.6.3.1.1.5.%d", generic+1)
	}
	// generic=6 enterpriseSpecific
	if enterprise != "" {
		return fmt.Sprintf("%s.0.%d", enterprise, specific)
	}
	return fmt.Sprintf("1.3.6.1.4.1.0.%d", specific)
}

// inferSeverity 按标准 trapOID 启发式推断严重度（server 侧可再精修）。
func inferSeverity(trapOID string) string {
	switch trapOID {
	case "1.3.6.1.6.3.1.1.5.1": // coldStart
		return "warning"
	case "1.3.6.1.6.3.1.1.5.2": // warmStart
		return "info"
	case "1.3.6.1.6.3.1.1.5.3": // linkDown
		return "warning"
	case "1.3.6.1.6.3.1.1.5.4": // linkUp
		return "info"
	case "1.3.6.1.6.3.1.1.5.5": // authenticationFailure
		return "warning"
	case "1.3.6.1.6.3.1.1.5.6": // egpNeighborLoss
		return "warning"
	}
	return "info" // 企业私有默认 info
}

func toSharedVarbinds(vbs []varbind) []shared.SNMPVarbind {
	out := make([]shared.SNMPVarbind, 0, len(vbs))
	for _, vb := range vbs {
		out = append(out, shared.SNMPVarbind{
			OID:   oidToString(vb.oid),
			Type:  vb.value.Kind(),
			Value: vb.value.String(),
		})
	}
	return out
}

// flush 排空 batch → SNMPTrapReport 上报。
func (tr *snmpTrapReceiver) flush(reporter func(shared.SNMPTrapReport)) {
	tr.mu.Lock()
	if len(tr.batch) == 0 {
		tr.mu.Unlock()
		return
	}
	batch := tr.batch
	tr.batch = nil
	tr.mu.Unlock()

	reporter(shared.SNMPTrapReport{
		HostID:      tr.hostID,
		Fingerprint: tr.fp,
		Timestamp:   time.Now().Unix(),
		Traps:       batch,
	})
	slog.Info("SNMP Trap 批量上报", "count", len(batch))
}
