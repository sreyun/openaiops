//go:build linux

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"aiops-monitor/shared"
)

const ethPAll = 0x0003 // ETH_P_ALL

func htonsU16(v uint16) uint16 { return v<<8 | v>>8 }

// ---- 经典 BPF(cBPF)：内核侧放行 IPv4 TCP/UDP 与 IPv6（含 VLAN），其余在内核直接丢弃，
// 大幅降低 CPU 与 syscall/拷贝开销。IPv6 扩展头使内核短过滤很容易误杀，所以保守地放行
// 所有 IPv6，再由用户态严格解析 TCP/UDP。
// 端口级精细过滤(只留 53/443/http)留待现场压测后再收紧（错误的端口过滤会静默丢目标流量）。
type sockFilter struct {
	code uint16
	jt   uint8
	jf   uint8
	k    uint32
}

// sockFprog mirrors C struct sock_fprog. Do NOT hand-write the padding: the
// pointer field is 8 bytes on amd64/arm64/loong64/riscv64 but 4 on 386/arm, and
// a fixed [6]byte gap silently misaligns the filter pointer on every 32-bit
// build. Go's natural alignment reproduces the C layout on both widths.
type sockFprog struct {
	length uint16
	filter *sockFilter
}

const (
	solSocket      = 1
	soAttachFilter = 26
)

var ipTCPUDPFilter = []sockFilter{
	{0x28, 0, 0, 12},     // 0  ldh [12] ethertype
	{0x15, 0, 3, 0x0800}, // 1  IPv4 → 2，否则 → 5
	{0x30, 0, 0, 23},     // 2  IPv4 next protocol
	{0x15, 9, 0, 6},      // 3  TCP → ACCEPT(13)
	{0x15, 8, 9, 17},     // 4  UDP → ACCEPT，否则 DROP(14)
	{0x15, 7, 0, 0x86dd}, // 5  IPv6 → ACCEPT，否则 → 6
	{0x15, 0, 7, 0x8100}, // 6  802.1Q VLAN → 7，否则 DROP
	{0x28, 0, 0, 16},     // 7  VLAN 内层 ethertype
	{0x15, 0, 3, 0x0800}, // 8  内层 IPv4 → 9，否则 → 12
	{0x30, 0, 0, 27},     // 9  VLAN IPv4 next protocol
	{0x15, 2, 0, 6},      // 10 TCP → ACCEPT
	{0x15, 1, 2, 17},     // 11 UDP → ACCEPT，否则 DROP
	{0x15, 0, 1, 0x86dd}, // 12 内层 IPv6 → ACCEPT，否则 DROP
	{0x06, 0, 0, 0xffff}, // 13 ACCEPT
	{0x06, 0, 0, 0},      // 14 DROP
}

// runNative 开一个 AF_PACKET 原始套接字抓包，内核 BPF 过滤 + 读/解析解耦(worker 池)，
// 解析 DNS/SNI/明文 HTTP。需 root/CAP_NET_RAW。
func (sc *sniCollector) runNative(ctx context.Context, reporter func(shared.DNSMapReport), contentReporter func(shared.ContentAuditReport)) error {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htonsU16(ethPAll)))
	if err != nil {
		return fmt.Errorf("打开 AF_PACKET 失败（需 root/CAP_NET_RAW）: %w", err)
	}
	var closeOnce sync.Once
	closeFD := func() { closeOnce.Do(func() { _ = syscall.Close(fd) }) }
	defer closeFD()
	go func() {
		select {
		case <-ctx.Done():
			closeFD() // 解除阻塞中的 Recvfrom
		}
	}()

	// 内核 BPF 过滤（best-effort）。必须在 bind 之前挂：bind 前的短暂窗口即使漏几个包也无所谓，
	// 但挂上后能立刻少收无关包。
	if err := attachBPF(fd, ipTCPUDPFilter); err != nil {
		slog.Warn("SNI/DNS 抓取: BPF 过滤挂载失败，退回全收+用户态过滤（CPU 略高）", "err", err)
	} else {
		slog.Info("SNI/DNS 抓取: 已挂 BPF 内核过滤（IPv4 TCP/UDP + IPv6）")
	}

	ifaceName := "all"
	if sc.cfg.Interface != "" {
		if ifi, e := net.InterfaceByName(sc.cfg.Interface); e == nil {
			ll := &syscall.SockaddrLinklayer{Protocol: htonsU16(ethPAll), Ifindex: ifi.Index}
			if e := syscall.Bind(fd, ll); e != nil {
				slog.Warn("SNI/DNS 抓取: 绑定网卡失败，改为全部网卡", "iface", sc.cfg.Interface, "err", e)
			} else {
				ifaceName = sc.cfg.Interface
			}
		} else {
			slog.Warn("SNI/DNS 抓取: 网卡不存在，改为全部网卡", "iface", sc.cfg.Interface)
		}
	}

	// 读/解析解耦 + 连接亲和：读循环只 recvfrom+拷贝+轻量解析(取四层)+按【规范连接键】哈希路由；
	// 同一连接的所有包永远进同一 worker，故每 worker 独占的 reassembler(TCP 流重组)【无锁】。
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > 8 {
		workers = 8
	}
	chans := make([]chan l4Info, workers)
	var dropped atomic.Uint64
	defer func() {
		for _, ch := range chans {
			close(ch)
		}
	}()
	for i := 0; i < workers; i++ {
		chans[i] = make(chan l4Info, 4096)
		ch := chans[i]
		ras := newReassembler(sc.cfg, sc.addContent) // 每 worker 独占，无锁
		go func() {
			sweep := time.NewTicker(20 * time.Second)
			defer sweep.Stop()
			for {
				select {
				case info, ok := <-ch:
					if !ok {
						return
					}
					runSafe("sni-parse", func() { sc.handleL4(info) })
					if sc.cfg.ContentAudit && info.proto == 6 {
						runSafe("content-reasm", func() { ras.feed(info) })
					}
				case <-sweep.C:
					if sc.cfg.ContentAudit {
						runSafe("content-sweep", func() { ras.sweepIdle(60) })
					}
				}
			}
		}()
	}
	slog.Info("SNI/DNS 抓取器启动", "iface", ifaceName, "workers", workers, "content_audit", sc.cfg.ContentAudit)

	// 周期 flush（含 recover）；顺带把丢包计数打出来，便于判断是否需要指定网卡/收紧过滤。
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				runSafe("sni-final-flush", func() { sc.flush(reporter) })
				runSafe("content-final-flush", func() { sc.flushContent(contentReporter) })
				return
			case <-ticker.C:
				runSafe("sni-flush", func() { sc.flush(reporter) })
				runSafe("content-flush", func() { sc.flushContent(contentReporter) })
				sc.logContentDrops()
				if d := dropped.Swap(0); d > 0 {
					slog.Warn("SNI/DNS 抓取: 解析队列满导致丢包（考虑指定 interface 或收紧过滤）", "dropped_30s", d)
				}
			}
		}
	}()

	buf := make([]byte, 65536)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if err == syscall.EINTR {
				continue
			}
			slog.Warn("SNI/DNS 抓取: 读取错误，继续", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if n <= 0 {
			continue
		}
		// 必须拷贝：buf 会被下一次 recvfrom 复用，而 worker 异步处理（info.payload 指向此拷贝）。
		frame := make([]byte, n)
		copy(frame, buf[:n])
		info, ok := parseEthernetFrame(frame)
		if !ok || len(info.payload) == 0 {
			continue
		}
		wk := routeWorker(info, workers)
		select {
		case chans[wk] <- info:
		default:
			dropped.Add(1) // 队列满：丢弃而非阻塞抓包（丢包好过卡死整条采集）
		}
	}
}

// routeWorker 按【规范连接键】(无向)哈希选 worker，保证 TCP 连接的两个方向落到同一 worker/reassembler。
// 非 TCP 按源 IP 哈希即可。FNV-1a。
func routeWorker(info l4Info, n int) int {
	var h uint32 = 2166136261
	mix := func(s string) {
		for i := 0; i < len(s); i++ {
			h ^= uint32(s[i])
			h *= 16777619
		}
	}
	if info.proto == 6 {
		k := makeConnKey(info.srcIP, info.srcPort, info.dstIP, info.dstPort)
		mix(k.ipA)
		mix(k.ipB)
		h = (h ^ uint32(k.portA)) * 16777619
		h = (h ^ uint32(k.portB)) * 16777619
	} else {
		mix(info.srcIP)
	}
	return int(h % uint32(n))
}
