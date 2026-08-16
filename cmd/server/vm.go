package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aiops-monitor/shared"
)

// ============================================================================
// VictoriaMetrics integration (optional, enabled via AIOPS_VM_URL).
//
// When a VM URL is configured, every host report is also pushed to VM in the
// Prometheus text exposition format via /api/v1/import/prometheus — stdlib HTTP
// only, no protobuf/snappy. This offloads long-term / large-scale time-series to
// a purpose-built TSDB while the embedded
// tiered store keeps serving the built-in dashboards. Pushes are batched and
// fire-and-forget so agent ingest never blocks on VM.
// ============================================================================

// VMConfig configures the optional VictoriaMetrics writer.
type VMConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"` // e.g. http://victoriametrics:8428
}

type vmSample struct {
	hostID, hostname, category string
	ts                         int64
	m                          shared.Metrics
}

// vmCheckSample 是一次自定义拨测/接口探测结果，排队持久化到 VM（重启不丢，可查历史趋势）。
type vmCheckSample struct {
	checkID, name, checkType    string
	ts                          int64
	ok                          bool
	latencyMs                   float64
	statusCode                  int
	lossPct                     float64
	dnsMs, tcpMs, tlsMs, ttfbMs float64 // HTTP 高级模式分段计时（0=未测）
	certDays                    int     // 证书剩余天数（-1=非 HTTPS/未知）
	respBytes                   int64   // 响应体大小（0=未记）
}

// vmAPISample 是一次 API 性能监控探测结果，排队持久化到 VM（aiops_api_* 指标族）。
type vmAPISample struct {
	apiID, system, endpoint     string
	ts                          int64
	ok                          bool
	latencyMs                   float64
	statusCode                  int
	dnsMs, tcpMs, tlsMs, ttfbMs float64
	certDays                    int
	respBytes                   int64
}

type vmWriter struct {
	cfg     *ConfigStore
	ch      chan vmSample
	checkCh chan vmCheckSample
	apiCh   chan vmAPISample
	httpc   *http.Client
	// breaker guards INGEST (/api/v1/import/prometheus) only; readBreaker guards
	// every query path. They must stay separate — see newVMWriter.
	breaker      *vmCircuitBreaker
	readBreaker  *vmCircuitBreaker
	historyCache *vmHistoryCache
	// diag 记录读写两个方向最近一次的结果，供 /api/v1/vm/diagnostics 回答
	// 「到底写进去没有 / 读得到吗」。见 vm_diag.go。
	diag    vmDiag
	dropped atomic.Uint64
	stopCh       chan struct{}
	stopped      chan struct{}
	stopOnce     sync.Once
	// rawCh carries pre-formatted Prometheus text lines (hardware / SNMP / NetFlow /
	// Hyper-V / exporter scrape) through the SAME batch+retry pipeline as host
	// samples. 这些指标此前是「每次调用起一个 goroutine 发一个请求、失败就算了」：
	// SNMP 一轮几十台设备就是几十个并发请求，VM 一抖就集体失败，而且没有任何重试，
	// VM 重启期间这些数据永久丢失——比主机曲线还容易断。
	rawCh chan string
}

func newVMWriter(cfg *ConfigStore) *vmWriter {
	// 读写各自一个熔断器。共用一个是「主机曲线一会有一会没有」的直接原因：
	// 写入路径每台主机每个采集周期都在跑，一旦 VM 对写入返回 4xx/5xx（一条标签不合法、
	// 短暂过载、磁盘满），连续 5 次就把熔断器打开 30 秒；而这 30 秒里**所有查询**都被
	// 直接拒绝，loadDurableHostHistory 静默退回内存环——内存只有 raw 1200 / 1m 2880 /
	// 5m 8640 个点，24h/7d 窗口一下就缩水，前端于是显示「仅覆盖 x/y」甚至空图。
	// 写入出问题不该让读图瞎掉，反过来也一样。
	return &vmWriter{
		cfg: cfg, ch: make(chan vmSample, 8192), checkCh: make(chan vmCheckSample, 4096), apiCh: make(chan vmAPISample, 4096),
		httpc:        &http.Client{Timeout: vmQueryTimeout()},
		breaker:      newVMCircuitBreaker(),
		readBreaker:  newVMCircuitBreaker(),
		historyCache: newVMHistoryCache(),
		stopCh:       make(chan struct{}),
		stopped:      make(chan struct{}),
		rawCh:        make(chan string, 4096),
	}
}

// enqueueAPI 排队一次 API 探测结果到 VM（VM 未启用或缓冲满时非阻塞丢弃）。
func (v *vmWriter) enqueueAPI(s vmAPISample) {
	if v == nil {
		return
	}
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return
	}
	select {
	case v.apiCh <- s:
	default:
	}
}

// enqueueCheck 排队一次拨测结果到 VM（VM 未启用或缓冲满时非阻塞丢弃）。
func (v *vmWriter) enqueueCheck(cs vmCheckSample) {
	if v == nil {
		return
	}
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return
	}
	select {
	case v.checkCh <- cs:
	default:
	}
}

// enqueue queues one sample for VM (no-op + non-blocking when VM is disabled or
// the buffer is full — VM must never slow down ingest).
func (v *vmWriter) enqueue(hostID, hostname, category string, ts int64, m shared.Metrics) {
	if v == nil {
		return
	}
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return
	}
	select {
	case v.ch <- vmSample{hostID, hostname, category, ts, m}:
	default: // drop on overflow rather than block ingest
		n := v.dropped.Add(1)
		if n == 1 || n%200 == 0 {
			slog.Warn("VictoriaMetrics 写入队列已满，样本被丢弃（历史以 VM 为准，丢点会在曲线上形成空洞）", "dropped", n, "host", hostID)
		}
	}
}

// run batches queued samples and pushes them to VM every few seconds.
// Failed pushes keep the batch and retry next tick — dropping them was why a
// VM restart punched a hole in every chart (RAM had the points, VM never did).
// vmReadyBudget caps how long run() waits for VictoriaMetrics to answer before
// starting to push anyway.
//
// compose 里 server 只 depends_on victoriametrics 的 service_started —— 容器起来了
// 不等于 8428 端口能应答：VM 启动时要先扫描并合并磁盘分片，数据量大时要几十秒。
// 这段窗口里每一批写入都会失败、进重试缓冲、并给写熔断器记账，而它正好落在**每次发版
// 重启之后**——也就是用户最容易发现「曲线缺了一段」的时刻。
//
// 先等端口应答再开泵：等待期间样本照常在 channel 里排队（容量 8192），VM 一就绪就整批
// 落库，不丢点。等不到也只是回到原来的行为，不阻塞启动。
const vmReadyBudget = 90 * time.Second

// waitReady blocks until VM's HTTP endpoint answers, the budget expires, or the
// writer is shut down.
//
// 判据刻意是「**收到了任何 HTTP 响应**」而不是「200」：这里要确认的是端点已经存在，
// 而不是某个具体路径的语义。反代或旧版本在 /health 上回 404 同样说明 VM 活着、能收写入，
// 拿状态码卡反而会把可用的部署判成不可用。
func (v *vmWriter) waitReady(budget time.Duration) {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return
	}
	url := strings.TrimRight(c.URL, "/") + "/health"
	deadline := time.Now().Add(budget)
	probe := &http.Client{Timeout: 3 * time.Second}
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		resp, err := probe.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if attempt > 0 {
				slog.Info("VictoriaMetrics 已就绪，开始写入", "attempts", attempt+1)
			}
			return
		}
		select {
		case <-v.stopCh:
			return
		case <-time.After(2 * time.Second):
		}
	}
	slog.Warn("VictoriaMetrics 在就绪预算内未应答，仍按原有重试逻辑开始写入",
		"url", url, "budget", budget.String())
}

func (v *vmWriter) run() {
	defer close(v.stopped)
	v.waitReady(vmReadyBudget)
	buf := make([]vmSample, 0, 512)
	cbuf := make([]vmCheckSample, 0, 256)
	abuf := make([]vmAPISample, 0, 256)
	rbuf := make([]string, 0, 256)
	// rbytes 按**字节**记账，因为 rbuf 的条目大小相差好几个数量级：pushRawLine 送来的是
	// 单行，writeLabeled 送来的是一整份 exporter 抓取（几千行、几 MB）。只按条数封顶的话，
	// 4096 条抓取正文就是几个 GB —— 监控服务端会先于 VM 自己死掉。
	rbytes := 0
	addRaw := func(line string) {
		rbuf = append(rbuf, line)
		rbytes += len(line)
	}
	clearRaw := func() { rbuf, rbytes = rbuf[:0], 0 }
	trimRaw := func() {
		if rbytes <= vmRawRetryMaxBytes && len(rbuf) <= vmRetryBufMax {
			return
		}
		i, freed := 0, 0
		for i < len(rbuf)-1 && (rbytes-freed > vmRawRetryMaxBytes || len(rbuf)-i > vmRetryBufMax) {
			freed += len(rbuf[i])
			i++
		}
		if i > 0 {
			slog.Warn("VictoriaMetrics 硬件/网络指标重试队列超限，丢弃最旧批次",
				"dropped", i, "freed_bytes", freed, "kept", len(rbuf)-i)
			rbuf = append([]string(nil), rbuf[i:]...)
			rbytes -= freed
		}
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	flush := func() {
		if v.cfg == nil {
			buf, cbuf, abuf = buf[:0], cbuf[:0], abuf[:0]
			clearRaw()
			return
		}
		c := v.cfg.VMConfig()
		if !c.Enabled || c.URL == "" {
			buf, cbuf, abuf = buf[:0], cbuf[:0], abuf[:0]
			clearRaw()
			return
		}
		if len(buf) > 0 && v.push(c.URL, buf) {
			buf = buf[:0]
		} else if len(buf) > vmRetryBufMax {
			slog.Warn("VictoriaMetrics 主机样本重试队列过长，丢弃最旧一批", "kept", vmRetryBufMax, "dropped", len(buf)-vmRetryBufMax)
			buf = append([]vmSample(nil), buf[len(buf)-vmRetryBufMax:]...)
		}
		if len(cbuf) > 0 && v.pushChecks(c.URL, cbuf) {
			cbuf = cbuf[:0]
		} else if len(cbuf) > vmRetryBufMax {
			cbuf = append([]vmCheckSample(nil), cbuf[len(cbuf)-vmRetryBufMax:]...)
		}
		if len(abuf) > 0 && v.pushAPI(c.URL, abuf) {
			abuf = abuf[:0]
		} else if len(abuf) > vmRetryBufMax {
			abuf = append([]vmAPISample(nil), abuf[len(abuf)-vmRetryBufMax:]...)
		}
		if len(rbuf) > 0 && v.pushRaw(c.URL, rbuf) {
			clearRaw()
		} else {
			trimRaw()
		}
	}
	drain := func() {
		for {
			select {
			case s := <-v.ch:
				buf = append(buf, s)
			case cs := <-v.checkCh:
				cbuf = append(cbuf, cs)
			case as := <-v.apiCh:
				abuf = append(abuf, as)
			case raw := <-v.rawCh:
				addRaw(raw)
			default:
				return
			}
		}
	}
	for {
		select {
		case <-v.stopCh:
			drain()
			flush()
			return
		case s := <-v.ch:
			buf = append(buf, s)
			if len(buf) >= 512 {
				flush()
			}
		case cs := <-v.checkCh:
			cbuf = append(cbuf, cs)
			if len(cbuf) >= 256 {
				flush()
			}
		case as := <-v.apiCh:
			abuf = append(abuf, as)
			if len(abuf) >= 256 {
				flush()
			}
		case raw := <-v.rawCh:
			addRaw(raw)
			if len(rbuf) >= 256 || rbytes >= vmRawFlushBytes {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

const vmRetryBufMax = 4096

const (
	// vmRawRetryMaxBytes 封顶硬件/SNMP/NetFlow/抓取指标的重试积压（按字节，理由见 rbytes）。
	vmRawRetryMaxBytes = 32 << 20
	// vmRawFlushBytes 让一份大抓取不必等满 256 条就发出去。
	vmRawFlushBytes = 4 << 20
)

// vmImportDone reports whether a batch is FINISHED WITH — i.e. the caller may
// clear it. It is deliberately not the same as "succeeded".
//
// 重试必须区分「服务端暂时不行」和「这批数据本身不合法」：
//
//   - 网络错误 / 熔断打开 / 5xx / 429 → VM 侧的问题，重试一定要有，否则就是当初那个
//     「VM 一重启，每台主机曲线上留一个永远补不回来的洞」。
//   - 其它 4xx → 这批内容有问题（一条非法标签、一个 NaN、超长的标签值）。重试一万次
//     也不会变成 2xx，却会每 5 秒原样重发一次，把后面所有健康数据一起堵在队列里，
//     直到被 vmRetryBufMax 挤掉——一条坏样本可以让整台服务器的时序写入长期瘫痪。
//     这种批次要丢掉并大声报警，而不是无限重试。
func (v *vmWriter) vmImportDone(resp *http.Response, err error, kind string, n int) bool {
	if err != nil {
		slog.Warn("VictoriaMetrics 写入失败（将重试）", "kind", kind, "n", n, "err", err)
		v.diag.writeErr(err.Error())
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	switch {
	case resp.StatusCode/100 == 2:
		v.diag.writeOK()
		return true
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		slog.Warn("VictoriaMetrics 暂时不可用（将重试）", "kind", kind, "status", resp.StatusCode, "n", n)
		v.diag.writeErr(fmt.Sprintf("HTTP %d (%s)", resp.StatusCode, kind))
		return false
	default:
		v.diag.writeErr(fmt.Sprintf("HTTP %d rejected (%s)", resp.StatusCode, kind))
		// 5xx 已经在 doVMRequest 里打过熔断器，4xx 没有。
		if v.breaker != nil {
			v.breaker.failure()
		}
		slog.Warn("VictoriaMetrics 拒绝写入（不可重试，该批已丢弃）",
			"kind", kind, "status", resp.StatusCode, "n", n)
		return true
	}
}

// vmImport posts one pre-formatted Prometheus text body. Returns whether the
// batch may be cleared (see vmImportDone).
func (v *vmWriter) vmImport(url, body, kind string, n int) bool {
	if strings.TrimSpace(body) == "" {
		return true
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(url, "/")+"/api/v1/import/prometheus", strings.NewReader(body))
	if err != nil {
		return true // 构造失败是编程错误，重试不会好转
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := v.doVMRequest(req)
	return v.vmImportDone(resp, err, kind, n)
}

// pushRaw writes the batched hardware / SNMP / NetFlow / Hyper-V / scrape lines.
func (v *vmWriter) pushRaw(url string, lines []string) bool {
	if len(lines) == 0 {
		return true
	}
	return v.vmImport(url, strings.Join(lines, "\n")+"\n", "raw", len(lines))
}

// shutdown flushes in-flight VM writes. Safe to call once; no-op if run() never started
// (waits up to d then returns).
func (v *vmWriter) shutdown(d time.Duration) {
	if v == nil {
		return
	}
	v.stopOnce.Do(func() { close(v.stopCh) })
	select {
	case <-v.stopped:
	case <-time.After(d):
		slog.Warn("VictoriaMetrics 写入队列关闭超时，未刷完的样本会丢失")
	}
}

// pushChecks 把拨测结果批量写入 VM（Prometheus 文本格式）。
// 指标：aiops_check_up(1/0) / _latency_ms / _status_code / _loss_pct，label 含 check_id/check_type/name。
func (v *vmWriter) pushChecks(url string, samples []vmCheckSample) bool {
	var b strings.Builder
	for _, s := range samples {
		lbl := fmt.Sprintf(`check_id="%s",check_type="%s",name="%s"`, lblEsc(s.checkID), lblEsc(s.checkType), lblEsc(s.name))
		ms := s.ts * 1000
		up := 0.0
		if s.ok {
			up = 1
		}
		fmt.Fprintf(&b, "aiops_check_up{%s} %g %d\n", lbl, up, ms)
		fmt.Fprintf(&b, "aiops_check_latency_ms{%s} %g %d\n", lbl, s.latencyMs, ms)
		if s.statusCode > 0 {
			fmt.Fprintf(&b, "aiops_check_status_code{%s} %d %d\n", lbl, s.statusCode, ms)
		}
		if s.lossPct >= 0 {
			fmt.Fprintf(&b, "aiops_check_loss_pct{%s} %g %d\n", lbl, s.lossPct, ms)
		}
		if s.dnsMs > 0 {
			fmt.Fprintf(&b, "aiops_check_dns_ms{%s} %g %d\n", lbl, s.dnsMs, ms)
		}
		if s.tcpMs > 0 {
			fmt.Fprintf(&b, "aiops_check_tcp_ms{%s} %g %d\n", lbl, s.tcpMs, ms)
		}
		if s.tlsMs > 0 {
			fmt.Fprintf(&b, "aiops_check_tls_ms{%s} %g %d\n", lbl, s.tlsMs, ms)
		}
		if s.ttfbMs > 0 {
			fmt.Fprintf(&b, "aiops_check_ttfb_ms{%s} %g %d\n", lbl, s.ttfbMs, ms)
		}
		if s.certDays >= 0 {
			fmt.Fprintf(&b, "aiops_check_cert_days{%s} %d %d\n", lbl, s.certDays, ms)
		}
		if s.respBytes > 0 {
			fmt.Fprintf(&b, "aiops_check_resp_bytes{%s} %d %d\n", lbl, s.respBytes, ms)
		}
	}
	return v.vmImport(url, b.String(), "checks", len(samples))
}

// writeLabeled 把带标签样本以 Prometheus 文本格式写入 VM（/api/v1/import/prometheus）。
// 供 exporter 抓取摄入使用；VM 原生按标签存储，与主机/拨测指标同库，可被阈值/SLO/AI 联合查询。
func (v *vmWriter) writeLabeled(samples []shared.LabeledSample) {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" || len(samples) == 0 {
		return
	}
	var b strings.Builder
	for _, s := range samples {
		if s.Name == "" {
			continue
		}
		b.WriteString(s.Name)
		if len(s.Labels) > 0 {
			b.WriteByte('{')
			first := true
			for k, val := range s.Labels {
				if k == "" {
					continue
				}
				if !first {
					b.WriteByte(',')
				}
				first = false
				b.WriteString(k)
				b.WriteString(`="`)
				b.WriteString(lblEsc(val))
				b.WriteString(`"`)
			}
			b.WriteByte('}')
		}
		fmt.Fprintf(&b, " %g %d\n", s.Value, s.TsMs)
	}
	// 与 pushRawLine 同一条管道：批量、失败重试、停机排空。此前这里是「发一次，
	// 失败就丢」，于是 exporter 抓取来的指标在 VM 重启期间同样是永久缺口。
	v.pushRawLine(strings.TrimRight(b.String(), "\n"))
}

// queryCheckHistory 从 VM 读取某拨测在 [from,to] 的结果序列，重组为 []CheckPoint（重启后仍可查历史）。
func (v *vmWriter) queryCheckHistory(checkID string, from, to int64) []CheckPoint {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return nil
	}
	q := url.Values{
		"match[]": {fmt.Sprintf(`{check_id=%q,__name__=~"aiops_check_.*"}`, checkID)},
		"start":   {strconv.FormatInt(from, 10)},
		"end":     {strconv.FormatInt(to, 10)},
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.URL, "/")+"/api/v1/export?"+q.Encode(), nil)
	if err != nil {
		return nil
	}
	resp, err := v.doVMQuery(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	return parseVMCheckExport(resp.Body)
}

// parseVMCheckExport 把 VM /export 的 NDJSON（每行一条 series）按时间戳重组为 []CheckPoint。
// Presence+LOCF：交错的 up/latency/status 不会再被当成「假 0ms / 假离线」。
func parseVMCheckExport(r io.Reader) []CheckPoint {
	byTs := map[int64]*checkJoinCell{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		var line struct {
			Metric     map[string]string `json:"metric"`
			Values     []float64         `json:"values"`
			Timestamps []int64           `json:"timestamps"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		name := line.Metric["__name__"]
		for i := range line.Values {
			if i >= len(line.Timestamps) {
				break
			}
			ts := line.Timestamps[i] / 1000
			c := byTs[ts]
			if c == nil {
				c = &checkJoinCell{ts: ts, p: CheckPoint{Ts: ts, LossPct: -1}}
				byTs[ts] = c
			}
			val := line.Values[i]
			switch name {
			case "aiops_check_up":
				c.p.OK = val >= 0.5
				c.mark("ok")
			case "aiops_check_latency_ms":
				c.p.LatencyMs = val
				c.mark("latency_ms")
			case "aiops_check_status_code":
				c.p.StatusCode = int(val)
				c.mark("status_code")
			case "aiops_check_loss_pct":
				c.p.LossPct = val
				c.mark("loss_pct")
			case "aiops_check_dns_ms":
				c.p.DnsMs = val
				c.mark("dns_ms")
			case "aiops_check_tcp_ms":
				c.p.TcpMs = val
				c.mark("tcp_ms")
			case "aiops_check_tls_ms":
				c.p.TlsMs = val
				c.mark("tls_ms")
			case "aiops_check_ttfb_ms":
				c.p.TtfbMs = val
				c.mark("ttfb_ms")
			case "aiops_check_cert_days":
				c.p.CertDays = val
				c.mark("cert_days")
			case "aiops_check_resp_bytes":
				c.p.RespBytes = val
				c.mark("resp_bytes")
			}
		}
	}
	return finalizeCheckJoin(byTs)
}

// pushAPI 把 API 性能监控探测结果批量写入 VM（Prometheus 文本格式）。
// 指标：aiops_api_up(1/0) / _latency_ms / _status_code / _dns_ms / _tcp_ms /
// _tls_ms / _ttfb_ms / _cert_days / _resp_bytes，label 含 api_id/system/endpoint。
func (v *vmWriter) pushAPI(url string, samples []vmAPISample) bool {
	var b strings.Builder
	for _, s := range samples {
		lbl := fmt.Sprintf(`api_id="%s",system="%s",endpoint="%s"`, lblEsc(s.apiID), lblEsc(s.system), lblEsc(s.endpoint))
		ms := s.ts * 1000
		up := 0.0
		if s.ok {
			up = 1
		}
		fmt.Fprintf(&b, "aiops_api_up{%s} %g %d\n", lbl, up, ms)
		fmt.Fprintf(&b, "aiops_api_latency_ms{%s} %g %d\n", lbl, s.latencyMs, ms)
		if s.statusCode > 0 {
			fmt.Fprintf(&b, "aiops_api_status_code{%s} %d %d\n", lbl, s.statusCode, ms)
		}
		if s.dnsMs > 0 {
			fmt.Fprintf(&b, "aiops_api_dns_ms{%s} %g %d\n", lbl, s.dnsMs, ms)
		}
		if s.tcpMs > 0 {
			fmt.Fprintf(&b, "aiops_api_tcp_ms{%s} %g %d\n", lbl, s.tcpMs, ms)
		}
		if s.tlsMs > 0 {
			fmt.Fprintf(&b, "aiops_api_tls_ms{%s} %g %d\n", lbl, s.tlsMs, ms)
		}
		if s.ttfbMs > 0 {
			fmt.Fprintf(&b, "aiops_api_ttfb_ms{%s} %g %d\n", lbl, s.ttfbMs, ms)
		}
		if s.certDays >= 0 {
			fmt.Fprintf(&b, "aiops_api_cert_days{%s} %d %d\n", lbl, s.certDays, ms)
		}
		if s.respBytes > 0 {
			fmt.Fprintf(&b, "aiops_api_resp_bytes{%s} %d %d\n", lbl, s.respBytes, ms)
		}
	}
	return v.vmImport(url, b.String(), "api", len(samples))
}

// queryAPIHistory 从 VM 读取某接口在 [from,to] 的探测序列，重组为 []APIHistPoint（历史曲线）。
func (v *vmWriter) queryAPIHistory(apiID string, from, to int64) []APIHistPoint {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return nil
	}
	q := url.Values{
		"match[]": {fmt.Sprintf(`{api_id=%q,__name__=~"aiops_api_.*"}`, apiID)},
		"start":   {strconv.FormatInt(from, 10)},
		"end":     {strconv.FormatInt(to, 10)},
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.URL, "/")+"/api/v1/export?"+q.Encode(), nil)
	if err != nil {
		return nil
	}
	resp, err := v.doVMQuery(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	return parseVMAPIExport(resp.Body)
}

// parseVMAPIExport 把 VM /export 的 NDJSON 按时间戳重组为 []APIHistPoint（aiops_api_* 指标族）。
// Presence+LOCF：DNS/TCP/TLS/TTFB 与总延时交错到达时不再被当成假 0ms，可用性也不再假离线。
func parseVMAPIExport(r io.Reader) []APIHistPoint {
	byTs := map[int64]*apiJoinCell{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		var line struct {
			Metric     map[string]string `json:"metric"`
			Values     []float64         `json:"values"`
			Timestamps []int64           `json:"timestamps"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		name := line.Metric["__name__"]
		for i := range line.Values {
			if i >= len(line.Timestamps) {
				break
			}
			ts := line.Timestamps[i] / 1000
			c := byTs[ts]
			if c == nil {
				c = &apiJoinCell{ts: ts, p: APIHistPoint{Ts: ts}}
				byTs[ts] = c
			}
			val := line.Values[i]
			switch name {
			case "aiops_api_up":
				c.p.OK = val >= 0.5
				c.mark("ok")
			case "aiops_api_latency_ms":
				c.p.LatencyMs = val
				c.mark("latency_ms")
			case "aiops_api_status_code":
				c.p.StatusCode = int(val)
				c.mark("status_code")
			case "aiops_api_dns_ms":
				c.p.DnsMs = val
				c.mark("dns_ms")
			case "aiops_api_tcp_ms":
				c.p.TcpMs = val
				c.mark("tcp_ms")
			case "aiops_api_tls_ms":
				c.p.TlsMs = val
				c.mark("tls_ms")
			case "aiops_api_ttfb_ms":
				c.p.TtfbMs = val
				c.mark("ttfb_ms")
			case "aiops_api_resp_bytes":
				c.p.RespBytes = val
				c.mark("resp_bytes")
			}
		}
	}
	return finalizeAPIJoin(byTs)
}

// apiAggregate 是一个接口由 VM 现算的性能聚合（平均/ P95 响应时间、1h/24h 可用率、1h 采样数）。
type apiAggregate struct {
	AvgMs     float64 `json:"avg_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	Avail1h   float64 `json:"avail_1h"`  // 百分比
	Avail24h  float64 `json:"avail_24h"` // 百分比
	Samples1h float64 `json:"samples_1h"`
}

// vmInstantByAPI 执行一次 PromQL 瞬时查询，返回 api_id -> 数值（VM 侧现算聚合）。
// promSeries 是 PromQL 即时查询返回的一条结果（标签集 + 值）。
type promSeries struct {
	Labels map[string]string
	Value  float64
}

// vmQueryVector 对 VM 执行 PromQL 即时查询，返回结果向量（每条 = 一组标签 + 值）。
// 供指标告警规则评估用：表达式已编码条件（如 mysql_up==0 / rate(errors[5m])>10），
// 非空结果即表示这些标签集正处于告警状态（与 Prometheus 告警规则语义一致）。ok=false 表示 VM 未启用/查询失败。
func (v *vmWriter) vmQueryVector(promql string) ([]promSeries, bool) {
	return v.vmQueryVectorAt(promql, 0)
}

// vmQueryVectorAt evaluates the instant query at unix time `at` (0 = now), so a
// dashboard viewing a past window reads the value at the end of that window
// rather than the current one.
func (v *vmWriter) vmQueryVectorAt(promql string, at int64) ([]promSeries, bool) {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return nil, false
	}
	q := url.Values{"query": {promql}}
	if at > 0 {
		q.Set("time", strconv.FormatInt(at, 10))
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.URL, "/")+"/api/v1/query?"+q.Encode(), nil)
	if err != nil {
		return nil, false
	}
	resp, err := v.doVMQuery(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"` // [ts, "strval"]
			} `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Status != "success" {
		return nil, false
	}
	series := make([]promSeries, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		if len(r.Value) < 2 {
			continue
		}
		s, _ := r.Value[1].(string)
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		series = append(series, promSeries{Labels: r.Metric, Value: f})
	}
	return series, true
}

// vmQueryScalar 执行 PromQL 即时查询，把返回向量各序列求和为单一标量。
// 供 SLO 的 promql 源用（good/total 计数查询，通常已 sum(...) 聚合）。ok=false 表示 VM 未启用/查询失败。
func (v *vmWriter) vmQueryScalar(promql string) (float64, bool) {
	series, ok := v.vmQueryVector(promql)
	if !ok {
		return 0, false
	}
	var sum float64
	for _, s := range series {
		sum += s.Value
	}
	return sum, true
}

// vmRangePoint 是区间查询在某时间点上的聚合值（各序列求和）。
type vmRangePoint struct {
	Ts  int64
	Val float64
}

// vmQueryRange 执行 PromQL 区间查询（/api/v1/query_range），把每个时间点上各序列求和后按时间升序返回。
// step 为秒。供 SLO promql 源的自定义区间趋势用。ok=false 表示 VM 未启用/查询失败。
func (v *vmWriter) vmQueryRange(promql string, startTs, endTs, stepSec int64) ([]vmRangePoint, bool) {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return nil, false
	}
	if stepSec < 1 {
		stepSec = 60
	}
	q := url.Values{
		"query": {promql},
		"start": {strconv.FormatInt(startTs, 10)},
		"end":   {strconv.FormatInt(endTs, 10)},
		"step":  {strconv.FormatInt(stepSec, 10)},
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.URL, "/")+"/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, false
	}
	resp, err := v.doVMQuery(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Values [][]any `json:"values"` // [[ts, "strval"], ...]
			} `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Status != "success" {
		return nil, false
	}
	agg := map[int64]float64{}
	for _, r := range out.Data.Result {
		for _, pair := range r.Values {
			if len(pair) < 2 {
				continue
			}
			tsF, _ := pair[0].(float64)
			sv, _ := pair[1].(string)
			f, err := strconv.ParseFloat(sv, 64)
			if err != nil {
				continue
			}
			agg[int64(tsF)] += f
		}
	}
	pts := make([]vmRangePoint, 0, len(agg))
	for ts, val := range agg {
		pts = append(pts, vmRangePoint{Ts: ts, Val: val})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Ts < pts[j].Ts })
	return pts, true
}

// promMatrix 是区间查询返回的一条时间序列（标签集 + 点集）。供仪表盘多序列面板用。
type promMatrix struct {
	Labels map[string]string `json:"labels"`
	Points [][2]float64      `json:"points"` // [[tsSec, val], ...]
}

// vmQueryRangeSeries 执行 PromQL 区间查询，逐序列返回（不聚合），供仪表盘时序面板绘多条曲线。
func (v *vmWriter) vmQueryRangeSeries(promql string, startTs, endTs, stepSec int64) ([]promMatrix, bool) {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return nil, false
	}
	if stepSec < 1 {
		stepSec = 60
	}
	q := url.Values{
		"query": {promql},
		"start": {strconv.FormatInt(startTs, 10)},
		"end":   {strconv.FormatInt(endTs, 10)},
		"step":  {strconv.FormatInt(stepSec, 10)},
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.URL, "/")+"/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, false
	}
	resp, err := v.doVMQuery(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Status != "success" {
		return nil, false
	}
	series := make([]promMatrix, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		pts := make([][2]float64, 0, len(r.Values))
		for _, pair := range r.Values {
			if len(pair) < 2 {
				continue
			}
			tsSec, ok := promTsSeconds(pair[0])
			if !ok {
				continue
			}
			sv, _ := pair[1].(string)
			f, err := strconv.ParseFloat(sv, 64)
			if err != nil {
				continue // 跳过 NaN/Inf
			}
			pts = append(pts, [2]float64{float64(tsSec), f})
		}
		series = append(series, promMatrix{Labels: r.Metric, Points: pts})
	}
	return series, true
}

// vmLabelValues 取某标签的全部取值（可选按 match[] 序列选择器过滤），供仪表盘模板变量 label_values(...) 解析。
func (v *vmWriter) vmLabelValues(label, match string) ([]string, bool) {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" || label == "" {
		return nil, false
	}
	q := url.Values{}
	if strings.TrimSpace(match) != "" {
		q.Set("match[]", match)
	}
	u := strings.TrimRight(c.URL, "/") + "/api/v1/label/" + url.PathEscape(label) + "/values"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, false
	}
	resp, err := v.doVMQuery(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var out struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Status != "success" {
		return nil, false
	}
	sort.Strings(out.Data)
	return out.Data, true
}

func (v *vmWriter) vmInstantByAPI(promql string) map[string]float64 {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return nil
	}
	q := url.Values{"query": {promql}}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.URL, "/")+"/api/v1/query?"+q.Encode(), nil)
	if err != nil {
		return nil
	}
	resp, err := v.doVMQuery(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var out struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"` // [ts, "strval"]
			} `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil
	}
	m := map[string]float64{}
	for _, r := range out.Data.Result {
		id := r.Metric["api_id"]
		if id == "" || len(r.Value) < 2 {
			continue
		}
		s, _ := r.Value[1].(string)
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			m[id] = f
		}
	}
	return m
}

// queryAPIAggregate 用 5 次 PromQL 瞬时查询算出所有接口的聚合，按 api_id 归并返回。
// 一次查询即覆盖全部接口（VM 按 api_id label 返回多条结果），与接口数量无关。
func (v *vmWriter) queryAPIAggregate() map[string]apiAggregate {
	if !v.enabled() {
		return map[string]apiAggregate{}
	}
	avg := v.vmInstantByAPI(`avg_over_time(aiops_api_latency_ms[1h])`)
	p95 := v.vmInstantByAPI(`quantile_over_time(0.95, aiops_api_latency_ms[1h])`)
	p99 := v.vmInstantByAPI(`quantile_over_time(0.99, aiops_api_latency_ms[1h])`)
	a1 := v.vmInstantByAPI(`avg_over_time(aiops_api_up[1h]) * 100`)
	a24 := v.vmInstantByAPI(`avg_over_time(aiops_api_up[24h]) * 100`)
	cnt := v.vmInstantByAPI(`count_over_time(aiops_api_up[1h])`)
	out := map[string]apiAggregate{}
	get := func(m map[string]float64, id string) float64 {
		if m == nil {
			return 0
		}
		return m[id]
	}
	seen := map[string]bool{}
	for _, m := range []map[string]float64{avg, p95, p99, a1, a24, cnt} {
		for id := range m {
			seen[id] = true
		}
	}
	for id := range seen {
		out[id] = apiAggregate{
			AvgMs: get(avg, id), P95Ms: get(p95, id), P99Ms: get(p99, id),
			Avail1h: get(a1, id), Avail24h: get(a24, id), Samples1h: get(cnt, id),
		}
	}
	return out
}

// lblEsc escapes a Prometheus label value.
func lblEsc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(s)
}

// push formats the samples as Prometheus text and imports them into VM.
func (v *vmWriter) push(url string, samples []vmSample) bool {
	var b strings.Builder
	for _, s := range samples {
		lbl := fmt.Sprintf(`host="%s",instance="%s"`, lblEsc(s.hostID), lblEsc(s.hostname))
		if s.category != "" {
			lbl += fmt.Sprintf(`,category="%s"`, lblEsc(s.category))
		}
		ms := s.ts * 1000
		w := func(name string, val float64) { fmt.Fprintf(&b, "aiops_%s{%s} %g %d\n", name, lbl, val, ms) }
		w("cpu_percent", s.m.CPUPercent)
		w("cpu_cores", float64(s.m.CPUCores))
		w("mem_percent", s.m.MemPercent)
		w("mem_used_bytes", float64(s.m.MemUsed))
		w("mem_total_bytes", float64(s.m.MemTotal))
		w("swap_percent", s.m.SwapPercent)
		w("swap_used_bytes", float64(s.m.SwapUsed))
		w("swap_total_bytes", float64(s.m.SwapTotal))
		w("disk_percent", s.m.DiskPercent)
		w("disk_used_bytes", float64(s.m.DiskUsed))
		w("disk_total_bytes", float64(s.m.DiskTotal))
		w("uptime_seconds", float64(s.m.Uptime))
		w("disk_io_util_percent", s.m.DiskIOUtilPercent)
		w("disk_read_rate", s.m.DiskReadRate)
		w("disk_write_rate", s.m.DiskWriteRate)
		w("disk_read_iops", s.m.DiskReadIOPS)
		w("disk_write_iops", s.m.DiskWriteIOPS)
		w("net_sent_rate", s.m.NetSentRate)
		w("net_recv_rate", s.m.NetRecvRate)
		w("net_conns", float64(s.m.NetConns))
		w("load1", s.m.Load1)
		w("load5", s.m.Load5)
		w("load15", s.m.Load15)
		w("proc_count", float64(s.m.ProcCount))
		w("cpu_idle_percent", 100-s.m.CPUPercent)
		if s.m.MemTotal >= s.m.MemUsed {
			w("mem_free_bytes", float64(s.m.MemTotal-s.m.MemUsed))
		}
		w("mem_free_percent", 100-s.m.MemPercent)
		if s.m.SwapTotal >= s.m.SwapUsed {
			w("swap_free_bytes", float64(s.m.SwapTotal-s.m.SwapUsed))
		}
		if s.m.DiskTotal >= s.m.DiskUsed {
			w("disk_free_bytes", float64(s.m.DiskTotal-s.m.DiskUsed))
		}
		w("disk_free_percent", 100-s.m.DiskPercent)
		w("net_total_rate", s.m.NetSentRate+s.m.NetRecvRate)
		if s.m.CPUCores > 0 {
			w("load1_per_core", s.m.Load1/float64(s.m.CPUCores))
			w("load5_per_core", s.m.Load5/float64(s.m.CPUCores))
			w("load15_per_core", s.m.Load15/float64(s.m.CPUCores))
		}
		w("api_avail_percent", s.m.APIAvailPercent)
		w("api_avg_resp_ms", s.m.APIAvgRespMs)
		w("api_p95_resp_ms", s.m.APIP95RespMs)
		w("api_throughput_rps", s.m.APIThroughputRPS)
		w("task_fail_count", float64(s.m.TaskFailCount))
		w("task_timeout_sec", s.m.TaskTimeoutSec)
		w("gpus_count", float64(len(s.m.GPUs)))
		w("mounts_count", float64(len(s.m.Disks)))
		for _, d := range s.m.Disks {
			dl := lbl + fmt.Sprintf(`,path="%s"`, lblEsc(d.Path))
			fmt.Fprintf(&b, "aiops_disk_vol_percent{%s} %g %d\n", dl, d.Percent, ms)
			fmt.Fprintf(&b, "aiops_disk_vol_used_bytes{%s} %g %d\n", dl, float64(d.Used), ms)
			fmt.Fprintf(&b, "aiops_disk_vol_total_bytes{%s} %g %d\n", dl, float64(d.Total), ms)
		}
		for _, g := range s.m.GPUs {
			gl := lbl + fmt.Sprintf(`,gpu="%s"`, lblEsc(g.Name))
			fmt.Fprintf(&b, "aiops_gpu_util_percent{%s} %g %d\n", gl, g.UtilPercent, ms)
			fmt.Fprintf(&b, "aiops_gpu_temp_c{%s} %g %d\n", gl, g.Temp, ms)
			fmt.Fprintf(&b, "aiops_gpu_mem_percent{%s} %g %d\n", gl, g.MemPercent, ms)
			fmt.Fprintf(&b, "aiops_gpu_mem_used_bytes{%s} %g %d\n", gl, float64(g.MemUsed), ms)
			fmt.Fprintf(&b, "aiops_gpu_mem_free_bytes{%s} %g %d\n", gl, float64(g.MemFree), ms)
			fmt.Fprintf(&b, "aiops_gpu_mem_total_bytes{%s} %g %d\n", gl, float64(g.MemTotal), ms)
		}
		// 每 (协议,状态) 一条连接计数序列，支撑「连接数 / 会话状态」趋势图
		for _, c := range s.m.Conns {
			cl := lbl + fmt.Sprintf(`,proto="%s",state="%s"`, lblEsc(c.Proto), lblEsc(c.State))
			fmt.Fprintf(&b, "aiops_net_conn_count{%s} %g %d\n", cl, float64(c.Count), ms)
		}
	}
	return v.vmImport(url, b.String(), "samples", len(samples))
}

// enabled reports whether VM is the active time-series store.
func (v *vmWriter) enabled() bool {
	if v == nil {
		return false
	}
	c := v.cfg.VMConfig()
	return c.Enabled && c.URL != ""
}

// setSampleMetric writes one VM series value into the matching Sample field.
func setSampleMetric(s *shared.Sample, name string, val float64) {
	switch strings.TrimPrefix(name, "aiops_") {
	case "cpu_percent":
		s.CPUPercent = val
	case "cpu_cores":
		s.CPUCores = int(val)
	case "mem_percent":
		s.MemPercent = val
	case "mem_used_bytes":
		s.MemUsed = uint64(val)
	case "mem_total_bytes":
		s.MemTotal = uint64(val)
	case "swap_percent":
		s.SwapPercent = val
	case "swap_used_bytes":
		s.SwapUsed = uint64(val)
	case "swap_total_bytes":
		s.SwapTotal = uint64(val)
	case "disk_percent":
		s.DiskPercent = val
	case "disk_used_bytes":
		s.DiskUsed = uint64(val)
	case "disk_total_bytes":
		s.DiskTotal = uint64(val)
	case "uptime_seconds":
		s.Uptime = uint64(val)
	case "disk_io_util_percent":
		s.DiskIOUtilPercent = val
	case "disk_read_rate":
		s.DiskReadRate = val
	case "disk_write_rate":
		s.DiskWriteRate = val
	case "disk_read_iops":
		s.DiskReadIOPS = val
	case "disk_write_iops":
		s.DiskWriteIOPS = val
	case "net_sent_rate":
		s.NetSentRate = val
	case "net_recv_rate":
		s.NetRecvRate = val
	case "net_conns":
		s.NetConns = int(val)
	case "load1":
		s.Load1 = val
	case "load5":
		s.Load5 = val
	case "load15":
		s.Load15 = val
	case "proc_count":
		s.ProcCount = int(val)
	case "api_avail_percent":
		s.APIAvailPercent = val
	case "api_avg_resp_ms":
		s.APIAvgRespMs = val
	case "api_p95_resp_ms":
		s.APIP95RespMs = val
	case "api_throughput_rps":
		s.APIThroughputRPS = val
	case "task_fail_count":
		s.TaskFailCount = int(val)
	case "task_timeout_sec":
		s.TaskTimeoutSec = val
	}
}

// setSampleGPU 把一条带 gpu 标签的 aiops_gpu_* 系列（按显卡名区分）并回该时间点样本的 GPUs
// 数组，按名重建每块显卡的 利用率/温度/显存 各字段。返回写入的字段短名（供 presence 合并）。
func setSampleGPU(s *shared.Sample, gpuName, name string, val float64) string {
	if gpuName == "" {
		gpuName = "GPU"
	}
	idx := -1
	for i := range s.GPUs {
		if s.GPUs[i].Name == gpuName {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.GPUs = append(s.GPUs, shared.GPUInfo{Name: gpuName})
		idx = len(s.GPUs) - 1
	}
	switch name {
	case "aiops_gpu_util_percent":
		s.GPUs[idx].UtilPercent = val
		return "util"
	case "aiops_gpu_temp_c":
		s.GPUs[idx].Temp = val
		return "temp"
	case "aiops_gpu_mem_percent":
		s.GPUs[idx].MemPercent = val
		return "mem_pct"
	case "aiops_gpu_mem_used_bytes":
		s.GPUs[idx].MemUsed = uint64(val)
		return "mem_used"
	case "aiops_gpu_mem_free_bytes":
		s.GPUs[idx].MemFree = uint64(val)
		return "mem_free"
	case "aiops_gpu_mem_total_bytes":
		s.GPUs[idx].MemTotal = uint64(val)
		return "mem_total"
	}
	return ""
}

// setSampleConn 把一条带 proto+state 标签的 aiops_net_conn_count 系列并回样本的 Conns 数组，
// 按 (协议,状态) 重建，支撑「连接数 / 会话状态」趋势图。
func setSampleConn(s *shared.Sample, proto, state string, val float64) {
	if proto == "" {
		return
	}
	for i := range s.Conns {
		if s.Conns[i].Proto == proto && s.Conns[i].State == state {
			s.Conns[i].Count = int(val)
			return
		}
	}
	s.Conns = append(s.Conns, shared.ConnStat{Proto: proto, State: state, Count: int(val)})
}

// setSampleDisk 把一条带 path 标签的 aiops_disk_vol_* 系列并回该时间点样本的 Disks 数组，
// 按分区路径重建（每个分区的 percent/used/total）。返回写入的字段短名（供 presence 合并）。
func setSampleDisk(s *shared.Sample, path, name string, val float64) string {
	if path == "" {
		return ""
	}
	idx := -1
	for i := range s.Disks {
		if s.Disks[i].Path == path {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.Disks = append(s.Disks, shared.DiskInfo{Path: path})
		idx = len(s.Disks) - 1
	}
	switch name {
	case "aiops_disk_vol_percent":
		s.Disks[idx].Percent = val
		return "percent"
	case "aiops_disk_vol_used_bytes":
		s.Disks[idx].Used = uint64(val)
		return "used"
	case "aiops_disk_vol_total_bytes":
		s.Disks[idx].Total = uint64(val)
		return "total"
	}
	return ""
}

// adaptiveHistoryStep picks a PromQL step so each host-history window yields a
// stable ~400–600 point density. Without this, raw /export floods long ranges
// (timeouts / empty charts) while short ranges look fine — the classic
// "switching 1h→6h→24h returns different empty/wrong curves" symptom.
func adaptiveHistoryStep(from, to int64) int64 {
	span := to - from
	if span < 1 {
		span = 1
	}
	step := span / 480
	switch {
	case step < 5:
		return 5
	case step > 3600:
		// Very long custom ranges: 1h buckets. Do not cap at 300s — that
		// turned 7d/14d into 2000–4000 points and exploded nested disk arrays.
		return 3600
	default:
		return step
	}
}

// queryHistory reads a host's series back from VM (the authoritative time-series
// store) over [from,to] and reassembles []shared.Sample keyed by timestamp.
// Prefer stepped query_range (consistent density); fall back to raw export for
// short windows when MetricsQL selector is unavailable.
func (v *vmWriter) queryHistory(hostID string, from, to int64) ([]shared.Sample, bool) {
	return v.queryHistoryFilter(hostID, from, to, nil)
}

func (v *vmWriter) queryHistoryFilter(hostID string, from, to int64, names []string) ([]shared.Sample, bool) {
	out, ok, _ := v.queryHistoryFilterReason(hostID, from, to, names)
	return out, ok
}

// queryHistoryFilterReason is queryHistoryFilter plus WHY it came back empty.
//
// 「查询失败」「熔断」「窗口确实为空」此前都塌缩成同一个 ok=false，于是前端只能把三种
// 原因并列写进一句提示里让人自己猜。可它们的处置方向完全相反：前两种查读路径（VM 是否
// 可达、断路器为何打开），第三种查写路径（数据到底有没有写进去）。分开报，才谈得上排查。
func (v *vmWriter) queryHistoryFilterReason(hostID string, from, to int64, names []string) ([]shared.Sample, bool, string) {
	if v == nil || !v.enabled() {
		return nil, false, historyReasonDisabled
	}
	if strings.TrimSpace(hostID) == "" {
		return nil, false, historyReasonQueryError
	}
	out, ok := v.queryHistoryFilterInner(hostID, from, to, names)
	if ok && len(out) > 0 {
		v.diag.readOK()
		return out, true, historyReasonNone
	}
	// 断路器优先判定：它开着的时候查询根本没有发出去，谈不上「窗口为空」。
	if v.queryBreaker().state() == "open" {
		return nil, false, historyReasonBreaker
	}
	// 走到这里说明请求真的发出去了。用一次极轻的存在性探测把「VM 答了但没数据」和
	// 「VM 根本没答上来」分开——前者是写入侧的问题，后者是读路径的问题。
	if _, probeOK := v.vmInstantScalar(`count(aiops_cpu_percent)`); !probeOK {
		v.diag.readErr("history query failed and the liveness probe also failed")
		return nil, false, historyReasonQueryError
	}
	v.diag.readEmpty()
	return nil, false, historyReasonEmpty
}

func (v *vmWriter) queryHistoryFilterInner(hostID string, from, to int64, names []string) ([]shared.Sample, bool) {
	if !v.enabled() || strings.TrimSpace(hostID) == "" {
		return nil, false
	}
	step := adaptiveHistoryStep(from, to)
	// Cache the VM half only. The caller always overlays the last 15 minutes of
	// RAM on top, so a cached result can never show a stale tail — see
	// vm_history_cache.go for why both window ends must be bucketed.
	key := vmHistoryCacheKey(hostID, from, to, step, names)
	if out, ok := v.historyCache.get(key); ok {
		return out, true
	}
	if out, ok := v.queryHistoryRangeNames(hostID, from, to, step, names); ok && len(out) > 0 {
		// Incomplete windows (VM just came back with two minutes of new data
		// for a 24h chart) must not be cached — that froze the short slice
		// for the whole TTL and made every poll look like a broken trend.
		if vmHistoryCacheable(out, from, to) {
			v.historyCache.put(key, out, vmHistoryCacheTTL(step))
		}
		return out, true
	}
	// query_range used to be `{__name__=~"aiops_.*"}` which exploded on
	// overlay/PVC paths and timed out for 6h+ — then there was no export
	// fallback, so the API served a few minutes of RAM. The allowlist
	// (~50 names + ephemeral path filter) is the default; /export still
	// covers ≤24h when the selector is missing.
	if queryHistoryAllowsExportFallback(to - from) {
		out, ok := v.queryHistoryExportNames(hostID, from, to, names)
		if ok && len(out) > 0 && vmHistoryCacheable(out, from, to) {
			v.historyCache.put(key, out, vmHistoryCacheTTL(step))
		}
		return out, ok
	}
	return nil, false
}

func (v *vmWriter) queryHistoryExport(hostID string, from, to int64) ([]shared.Sample, bool) {
	return v.queryHistoryExportNames(hostID, from, to, nil)
}

func (v *vmWriter) queryHistoryExportNames(hostID string, from, to int64, names []string) ([]shared.Sample, bool) {
	c := v.cfg.VMConfig()
	if !c.Enabled || c.URL == "" {
		return nil, false
	}
	q := url.Values{
		"match[]": {fmt.Sprintf(`{host=%q,__name__=~"%s",path!~"%s"}`, hostID, hostHistoryNameREOf(names), ephemeralDiskPathRE)},
		"start":   {strconv.FormatInt(from, 10)},
		"end":     {strconv.FormatInt(to, 10)},
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.URL, "/")+"/api/v1/export?"+q.Encode(), nil)
	if err != nil {
		return nil, false
	}
	resp, err := v.doVMQuery(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	out := parseVMExport(resp.Body)
	if len(out) == 0 {
		return nil, false
	}
	return downsampleSamples(out, 600), true // shared helper in sreyun_charts.go
}

// queryHistoryRange uses MetricsQL series selector + query_range so long windows
// stay bounded. Reassembles the same Sample shape as parseVMExport.
func (v *vmWriter) queryHistoryRange(hostID string, from, to, step int64) ([]shared.Sample, bool) {
	return v.queryHistoryRangeNames(hostID, from, to, step, nil)
}

func (v *vmWriter) queryHistoryRangeNames(hostID string, from, to, step int64, names []string) ([]shared.Sample, bool) {
	expr := hostHistoryRangeExprNames(hostID, names)
	series, ok := v.vmQueryRangeSeries(expr, from, to, step)
	if !ok || len(series) == 0 {
		return nil, false
	}
	byTs := map[int64]*histJoinCell{}
	for _, ser := range series {
		name := ser.Labels["__name__"]
		if name == "" {
			continue
		}
		gpuName := ser.Labels["gpu"]
		diskPath := ser.Labels["path"]
		connProto := ser.Labels["proto"]
		connState := ser.Labels["state"]
		for _, pt := range ser.Points {
			ts, ok := promTsSeconds(pt[0])
			if !ok {
				continue
			}
			applyHistJoinMetric(byTs, ts, name, gpuName, diskPath, connProto, connState, pt[1])
		}
	}
	out := finalizeHistJoin(byTs)
	// Snap onto the requested step grid with LOCF so short windows (1h/3h)
	// never leave irregular holes when VM omits empty evaluation steps.
	out = alignSamplesToStep(out, from, to, step)
	return out, len(out) > 0
}

// parseVMExport reassembles VM's /api/v1/export NDJSON (one line per series) into
// []shared.Sample joined by timestamp. Split out so it can be unit-tested without
// a live VM. Scalar gauges and nested inventories are LOCF-aligned so staggered
// series (load1/5/15 gaps) never paint as missing curves.
func parseVMExport(r io.Reader) []shared.Sample {
	byTs := map[int64]*histJoinCell{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		var line struct {
			Metric     map[string]string `json:"metric"`
			Values     []float64         `json:"values"`
			Timestamps []int64           `json:"timestamps"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		name := line.Metric["__name__"]
		gpuName := line.Metric["gpu"]     // GPU 系列带 gpu 标签（每块显卡一条），需按名重建 s.GPUs
		diskPath := line.Metric["path"]   // 磁盘分区系列带 path 标签（每个分区一条），需按路径重建 s.Disks
		connProto := line.Metric["proto"] // 连接计数系列带 proto+state 标签，需按 (协议,状态) 重建 s.Conns
		connState := line.Metric["state"]
		for i := range line.Values {
			if i >= len(line.Timestamps) {
				break
			}
			ts := line.Timestamps[i] / 1000
			applyHistJoinMetric(byTs, ts, name, gpuName, diskPath, connProto, connState, line.Values[i])
		}
	}
	return finalizeHistJoin(byTs)
}

// stabilizeSampleArrays sorts nested GPU/Disk/Conn slices so cross-request order is stable
// (frontend used to key GPUs by array index — unsorted VM order caused chart flicker).
func stabilizeSampleArrays(s *shared.Sample) {
	if s == nil {
		return
	}
	if len(s.GPUs) > 1 {
		sort.Slice(s.GPUs, func(a, b int) bool { return s.GPUs[a].Name < s.GPUs[b].Name })
	}
	if len(s.Disks) > 1 {
		sort.Slice(s.Disks, func(a, b int) bool { return s.Disks[a].Path < s.Disks[b].Path })
	}
	if len(s.Conns) > 1 {
		sort.Slice(s.Conns, func(a, b int) bool {
			if s.Conns[a].Proto != s.Conns[b].Proto {
				return s.Conns[a].Proto < s.Conns[b].Proto
			}
			return s.Conns[a].State < s.Conns[b].State
		})
	}
}

// ============================================================================
// Hardware + NetFlow VM write helpers
// ============================================================================

// pushHardware writes one hardware metric to VM immediately (fire-and-forget).
// 标签值一律走 lblEsc：target / 传感器名等来自 Agent（乃至 BMC）上报，未转义时一个
// 形如 `a"} evil{x="` 的传感器名就能凭空造出/污染其它序列。
func (v *vmWriter) pushHardware(hostID, target string, ts int64, metric string, val float64) {
	v.pushRawLine(fmt.Sprintf(`%s{host="%s",target="%s"} %f %d`, metric, lblEsc(hostID), lblEsc(target), val, ts))
}

// pushHardwareLabeled writes one hardware metric with an extra label.
func (v *vmWriter) pushHardwareLabeled(hostID, target string, ts int64, metric string, val float64, extraKey, extraVal string) {
	v.pushRawLine(fmt.Sprintf(`%s{host="%s",target="%s",%s="%s"} %f %d`,
		metric, lblEsc(hostID), lblEsc(target), extraKey, lblEsc(extraVal), val, ts))
}

// pushRawLine writes one Prometheus text line directly to VM (fire-and-forget).
// Used by hardware/netflow metrics that don't fit the standard sample pipeline.
func (v *vmWriter) pushRawLine(line string) {
	if v == nil || !v.enabled() || strings.TrimSpace(line) == "" {
		return
	}
	select {
	case v.rawCh <- strings.TrimRight(line, "\n"):
	default:
		n := v.dropped.Add(1)
		if n == 1 || n%200 == 0 {
			slog.Warn("VictoriaMetrics 写入队列已满，硬件/网络指标样本被丢弃", "dropped", n)
		}
	}
}

// queryRawRange executes a range query against VM and returns raw results.
// Step adapts to the window so SNMP/hardware/netflow charts stay dense on 1h
// and bounded on 7d/14d (fixed step=60 used to under/oversample by range).
func (v *vmWriter) queryRawRange(promql string, from, to int64) []any {
	if v == nil || !v.enabled() {
		return nil
	}
	c := v.cfg.VMConfig()
	step := adaptiveHistoryStep(from, to)
	// TrimRight：配置里的 URL 带结尾斜杠时，原写法会拼出 //api/v1/query_range，
	// VM 直接 404 —— SNMP/硬件/NetFlow 图表整片空白，且没有任何报错线索。
	u := fmt.Sprintf("%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%d",
		strings.TrimRight(c.URL, "/"), url.QueryEscape(promql), from, to, step)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	resp, err := v.doVMQuery(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var result struct {
		Data struct {
			Result []any `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return nil
	}
	return result.Data.Result
}
