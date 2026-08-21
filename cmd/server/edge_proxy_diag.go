package main

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// 反向代理配错的自我诊断。
//
// 修的是一整类"面板一切正常、远程能力全哑"的故障，现场表现是：主机在线、指标照常、
// 改配置也生效，唯独**远程终端连不上、Agent 自动升级永远失败**，而日志里只有一句
// "Agent 未接单"——一句把矛头指向 Agent 的话，于是运维去重装 Agent、去查防火墙，
// 查一整天也查不到真正的原因在 nginx 配置里。
//
// 成因：Agent 没有入站端口，所有远程能力都靠它**拨出**两条明文 HTTP 流（见
// terminal.go 顶部注释）。其中 tx（Agent→服务端）是一个**请求体持续不结束**的 POST：
// Agent 先把请求头发出来让服务端知道"我接单了"（markAgentUp），再一边执行一边往
// 请求体里写输出。而 nginx 的默认值 `proxy_request_buffering on` 的语义是
// **把整个请求体收全了再转发给上游**——于是这个请求在命令跑完之前根本不会到达服务端，
// 服务端等 90s 等不到，判为"Agent 未接单"，升级失败；下一轮再来一次，永远如此。
// 同理 `proxy_buffering on` 会把服务端下发的 rx 流憋住，`proxy_read_timeout 60s`
// 会把长连接掐断。这三条在 deploy/nginx-aiops.conf 里都写了，但配反代的人未必看过。
//
// 能确诊是因为服务端手里有一个决定性的反证：Agent 接单后会**每 1.5 秒**调一次
// /api/v1/agent/terminal/alive（一个普通的小 GET，不受请求体缓冲影响），交互式会话
// 还会挂上 rx。所以"alive 心跳还在跳 / rx 还开着，tx 却一直没到"这个组合，只可能是
// 上行流被中间某一跳缓冲住了——Agent 死了不会心跳，网断了也不会心跳。
//
// 确诊之后做两件事，缺一不可：
//  1. **别再判死**：既然确认 Agent 在跑，就把等待延到命令自己的预算上限。缓冲的请求体
//     会在命令结束时整包到达，输出与退出码一个不少——于是自动升级/剧本在配错的反代
//     后面**照样能跑完**（只有交互式终端救不回来，它本质上需要实时双向流）。
//  2. **把话说清楚**：失败信息与日志里直接给出要加的那几行 nginx 配置，而不是"未接单"。
type edgeProxyDiag struct {
	mu      sync.Mutex
	hosts   map[string]edgeProxyVerdict
	lastLog map[string]time.Time
}

// edgeProxyDiagState 是这套判定的唯一存放处。
//
// 刻意做成包级变量而不是 *Server 上的字段：这份状态是纯粹的观测数据（谁被反代拖住了），
// 没有任何依赖 Server 的东西，而挂到 Server 上就意味着**改一个诊断功能要同时动
// handlers.go 里的结构体定义**——那个文件是全仓最容易产生分叉/漏同步的一处（415 个
// 文件共用一个 package main，struct 定义与 NewServer 都在里面）。同样的取舍在
// self_fault.go 的 platformFaultSink 上已经做过一次，理由相同。
var edgeProxyDiagState = newEdgeProxyDiag()

// edgeProxyVerdict 是一台主机上最近一次"反代把上行流缓冲住了"的判定。
type edgeProxyVerdict struct {
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname,omitempty"`
	Kind     string `json:"kind"` // 目前只有 upstream_buffered
	At       int64  `json:"at"`   // unix 秒
	Detail   string `json:"detail"`
}

// edgeProxyLogEvery 限制同一台主机重复刷日志的频率：一支机队同时升级时，
// 每台都报一遍等于把真正有用的那一条淹掉。
const edgeProxyLogEvery = 10 * time.Minute

func newEdgeProxyDiag() *edgeProxyDiag {
	return &edgeProxyDiag{
		hosts:   map[string]edgeProxyVerdict{},
		lastLog: map[string]time.Time{},
	}
}

// note 记下一次判定，并返回"这次要不要写日志"（按主机限流）。
func (d *edgeProxyDiag) note(hostID, hostname, kind, detail string) bool {
	if d == nil || hostID == "" {
		return false
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hosts[hostID] = edgeProxyVerdict{
		HostID: hostID, Hostname: hostname, Kind: kind,
		At: now.Unix(), Detail: detail,
	}
	// 判定表按主机存，机队规模上限内不会无限增长；仍加一道兜底，防止长期运行下
	// 被已删除的主机撑大。
	if len(d.hosts) > 5000 {
		d.hosts = map[string]edgeProxyVerdict{hostID: d.hosts[hostID]}
		d.lastLog = map[string]time.Time{}
	}
	if last, ok := d.lastLog[hostID]; ok && now.Sub(last) < edgeProxyLogEvery {
		return false
	}
	d.lastLog[hostID] = now
	return true
}

// reset 清空判定表。只有测试会用到：包级状态在同一个进程里跨用例存活，
// 上一条用例留下的判定会让下一条"不该有判定"的断言假红。
func (d *edgeProxyDiag) reset() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.hosts = map[string]edgeProxyVerdict{}
	d.lastLog = map[string]time.Time{}
	d.mu.Unlock()
}

// snapshot 按时间倒序返回最近的判定，供诊断接口/排障使用。
func (d *edgeProxyDiag) snapshot() []edgeProxyVerdict {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	out := make([]edgeProxyVerdict, 0, len(d.hosts))
	for _, v := range d.hosts {
		out = append(out, v)
	}
	d.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// edgeProxyFixHint 是要加进反代的那几行。文案里出现的是 nginx 指令名，
// 三语共用同一段（配置项不翻译），所以不走 i18n。
// i18n-exempt: nginx 指令原文，翻译反而会让人抄错
const edgeProxyFixHint = "proxy_request_buffering off; proxy_buffering off; " +
	"proxy_read_timeout 3600s; proxy_send_timeout 3600s;"

// edgeProxyBufferedDetail 组装那句"要怎么修"的完整说明。
func edgeProxyBufferedDetail(hostname string) string {
	return Tz("proxy.upstream_buffered", hostname, edgeProxyFixHint)
}

// noteEdgeUpstreamBuffered 记录判定并（按主机限流）写一条运维日志。
// 返回给调用方拼进错误信息的说明文本。
func (s *Server) noteEdgeUpstreamBuffered(hostID, hostname string) string {
	detail := edgeProxyBufferedDetail(hostname)
	if s == nil {
		return detail
	}
	if edgeProxyDiagState.note(hostID, hostname, "upstream_buffered", detail) {
		slog.Warn("反向代理缓冲了 Agent 上行流，远程通道被拖垮",
			"host", hostID, "hostname", hostname, "修复", edgeProxyFixHint,
			"示例配置", "deploy/nginx-aiops.conf")
		// 走平台自身故障的既有归口（self_fault.go）：聚合计数、活动记录，连续几次
		// 之后自动升成事件，从而接上「诊断 → 处置 → 回验」那条链。反代配错正是那种
		// "平台一直知道、却从没走进闭环"的问题，这里不该再造一套通报机制。
		//
		// **不带 hostID**：故障在边缘那一跳，不在某台主机上。带上主机会把一次机队级
		// 的配置问题拆成几十条各自计数的记录，真正的规模反而看不见。
		s.reportPlatformFault("edge_proxy", "upstream_buffered", "warning", "", detail,
			fmt.Sprintf("首个命中的主机：%s（host_id=%s）\n建议配置：%s\n完整示例：deploy/nginx-aiops.conf",
				hostname, hostID, edgeProxyFixHint))
	}
	return detail
}

// edgeProxyBufferedError 是判死时给出的错误：既说清"不是 Agent 的问题"，也给出要加的配置。
func edgeProxyBufferedError(hostname string, waited time.Duration) error {
	return fmt.Errorf("%s", Tz("proxy.upstream_buffered_timeout",
		hostname, int(waited.Seconds()), edgeProxyFixHint))
}
