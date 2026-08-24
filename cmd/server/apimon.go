package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// API 性能监控（Task 3）
//
// 面向「一个业务系统的一批接口」的批量健康/性能监控：把每个接口按业务系统分组，
// 定时批量探测，复用自定义拨测的高级 HTTP 探测引擎（probeHTTPAdvanced，含
// DNS/TCP/TLS/TTFB 分段计时 + 状态码/关键字/JSON 断言 + 证书检测），结果持久化到
// VictoriaMetrics（aiops_api_* 指标族，重启不丢）。聚合表(平均/ P95 响应时间、
// 1h/24h 可用率、吞吐)由 VM 用 PromQL 现算；历史曲线从 VM 回读；异常按业务系统
// 配置的级别走统一告警通道（与自定义拨测一致）。
// ============================================================================

// APIEndpoint 是一个业务系统下被监控的单个接口。字段语义与 CustomCheck 的 HTTP
// 高级模式一致，便于直接复用 probeHTTPAdvanced。
type APIEndpoint struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	URL           string            `json:"url"`
	Method        string            `json:"method,omitempty"` // 默认 GET
	Headers       map[string]string `json:"headers,omitempty"`
	Body          string            `json:"body,omitempty"`
	ExpectStatus  int               `json:"expect_status,omitempty"`  // 期望状态码（0=默认 <400 通过）
	ExpectKeyword string            `json:"expect_keyword,omitempty"` // 响应体应包含的关键字
	JSONPath      string            `json:"json_path,omitempty"`      // JSON 断言点路径，如 code / data.token
	JSONExpect    string            `json:"json_expect,omitempty"`    // JSON 断言期望值（留空=只要求路径存在）
	TimeoutSec    int               `json:"timeout_sec,omitempty"`    // 单次探测超时（秒，默认 10，可为慢接口调大，上限 60）
	Retries       int               `json:"retries,omitempty"`        // 失败重试次数（默认 0，抑制瞬时抖动，上限 3）
	Distributed   bool              `json:"distributed,omitempty"`    // 分布式多点探测：由各地 agent 作为探针执行（迭代 D）
	Protocol      string            `json:"protocol,omitempty"`       // http(默认) | graphql | websocket（协议扩展 F）
	Enabled       bool              `json:"enabled"`
}

// toCheck 把接口适配成一个 HTTP 高级拨测，复用 probeHTTPAdvanced 的完整探测能力。
// commonHeaders 为业务系统级公共请求头，接口级 Headers 会覆盖同名 key；
// commonBody 为业务系统级公共请求体，按 mergeAPIBody 的规则与接口级 Body 合并。
func (e APIEndpoint) toCheck(commonHeaders map[string]string, commonBody string) CustomCheck {
	// 合并：先复制公共头，再用接口级覆盖（接口级优先）
	merged := make(map[string]string, len(commonHeaders)+len(e.Headers))
	for k, v := range commonHeaders {
		merged[k] = v
	}
	for k, v := range e.Headers {
		merged[k] = v
	}
	method := e.Method
	body := mergeAPIBody(commonBody, e.Body)
	graphql := e.Protocol == "graphql"
	if graphql {
		method = "POST"
		if merged["Content-Type"] == "" && merged["content-type"] == "" {
			merged["Content-Type"] = "application/json"
		}
		body = graphqlBody(body)
	}
	return CustomCheck{
		ID: e.ID, Name: e.Name, Type: "http", Target: e.URL,
		Advanced: true, Method: method, Headers: merged, Body: body,
		ExpectStatus: e.ExpectStatus, ExpectKeyword: e.ExpectKeyword,
		JSONPath: e.JSONPath, JSONExpect: e.JSONExpect, TimeoutSec: e.TimeoutSec, TraceParent: true, GraphQL: graphql,
	}
}

// graphqlBody 把原始 GraphQL 查询包装成 {"query":"..."}；若 Body 已是含 query 键的 JSON 请求体则原样。
// 注意：GraphQL 查询本身常以 { 开头（匿名查询），不能只按前缀判断——须真正解析出 query 键才算已包装。
func graphqlBody(body string) string {
	t := strings.TrimSpace(body)
	if t == "" {
		return body
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(t), &probe) == nil {
		if _, ok := probe["query"]; ok {
			return body // 已是 {"query":...} 形式，原样发送
		}
	}
	b, _ := json.Marshal(map[string]string{"query": t})
	return string(b)
}

// mergeAPIBody 合并系统级公共请求体与接口级请求体，规则（与公共请求头「接口级覆盖」精神一致）：
//   - 接口体为空 → 用公共体；公共体为空 → 用接口体（完全向后兼容）
//   - 两者皆为 JSON 对象 → 顶层字段浅合并，接口体同名字段覆盖公共体
//     （典型场景：公共体放 appId/token/sign 等公共参数，接口体只写各自业务字段）
//   - 否则（表单 / XML / 非对象 JSON，无法安全合并）→ 接口体整体覆盖公共体
func mergeAPIBody(commonBody, epBody string) string {
	cb, eb := strings.TrimSpace(commonBody), strings.TrimSpace(epBody)
	if cb == "" {
		return epBody
	}
	if eb == "" {
		return commonBody
	}
	var cm, em map[string]json.RawMessage
	if json.Unmarshal([]byte(cb), &cm) == nil && json.Unmarshal([]byte(eb), &em) == nil {
		for k, v := range em {
			cm[k] = v // 接口级字段覆盖同名公共字段
		}
		if out, err := json.Marshal(cm); err == nil {
			return string(out)
		}
	}
	return epBody // 非 JSON 对象：接口级整体优先
}

// APIHistPoint 是接口历史曲线的一个采样点。除总延时/状态外，携带响应时间分解
// （DNS/TCP/TLS/TTFB，单位 ms）与响应体大小（字节），供前端画「响应时间分解组合曲线」
// 与「响应体量」曲线——数据本就写入 VM，此前仅回读了总延时，现全量暴露。
type APIHistPoint struct {
	Ts         int64   `json:"timestamp"`
	OK         bool    `json:"ok"`
	LatencyMs  float64 `json:"latency_ms"`
	StatusCode int     `json:"status_code,omitempty"`
	DnsMs      float64 `json:"dns_ms"`
	TcpMs      float64 `json:"tcp_ms"`
	TlsMs      float64 `json:"tls_ms"`
	TtfbMs     float64 `json:"ttfb_ms"`
	RespBytes  float64 `json:"resp_bytes"`
}

// APISystem 是一个业务系统：一批接口 + 统一的探测周期与告警级别。
type APISystem struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	IntervalSec   int               `json:"interval_sec"`  // 批量探测周期（秒，最小 5）
	Level         string            `json:"level"`         // warning | critical
	Env           string            `json:"env,omitempty"` // 环境标签：prod|staging|dev（分组/过滤，迭代 E）
	Enabled       bool              `json:"enabled"`
	CommonHeaders map[string]string `json:"common_headers,omitempty"` // 业务系统级公共请求头，所有接口共用
	CommonBody    string            `json:"common_body,omitempty"`    // 业务系统级公共请求体，所有接口共用（JSON 对象则与接口体字段级合并）
	HostIDs       []string          `json:"host_ids,omitempty"`       // 承载该业务系统的主机 ID（异常下钻主机指标用，迭代 E）
	Endpoints     []APIEndpoint     `json:"endpoints"`
	CreatedAt     int64             `json:"created_at"`
	MaintUntil    int64             `json:"maint_until,omitempty"` // 维护窗口：此 Unix 时间前抑制该系统告警（探测继续，迭代 E）
}

// ---- ConfigStore：业务系统 CRUD（持久化到 PG/JSON，与 checks 同机制） ----

func (cs *ConfigStore) APISystems() []APISystem {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]APISystem, len(cs.cfg.APISystems))
	copy(out, cs.cfg.APISystems)
	return out
}

// deepCopyAPISystems 深拷贝业务系统切片（含每个系统的公共头 map、接口切片及接口头 map），
// 供 ConfigStore.save 在加密前隔离出独立副本，避免 encryptConfigSecrets 就地把内存中的
// 明文实时配置也加密了（headers/body 是引用类型，浅拷贝 slice 仍共享底层 map）。
func deepCopyAPISystems(in []APISystem) []APISystem {
	out := make([]APISystem, len(in))
	for i, s := range in {
		out[i] = s // 复制标量字段（含 CommonBody）
		if s.CommonHeaders != nil {
			m := make(map[string]string, len(s.CommonHeaders))
			for k, v := range s.CommonHeaders {
				m[k] = v
			}
			out[i].CommonHeaders = m
		}
		if s.HostIDs != nil {
			out[i].HostIDs = append([]string(nil), s.HostIDs...)
		}
		if s.Endpoints != nil {
			eps := make([]APIEndpoint, len(s.Endpoints))
			for j, e := range s.Endpoints {
				eps[j] = e
				if e.Headers != nil {
					hm := make(map[string]string, len(e.Headers))
					for k, v := range e.Headers {
						hm[k] = v
					}
					eps[j].Headers = hm
				}
			}
			out[i].Endpoints = eps
		}
	}
	return out
}

// UpsertAPISystem 新增或按 ID 替换一个业务系统（含其接口列表）。新建时分配系统 ID +
// CreatedAt；任何缺 ID 的接口都会补一个稳定 ID（供指标 label 与历史查询使用）。
func (cs *ConfigStore) UpsertAPISystem(sys APISystem) (APISystem, error) {
	cs.mu.Lock()
	for i := range sys.Endpoints {
		if strings.TrimSpace(sys.Endpoints[i].ID) == "" {
			sys.Endpoints[i].ID = genToken()[:8]
		}
	}
	if sys.ID == "" {
		sys.ID = genToken()[:8]
		sys.CreatedAt = time.Now().Unix()
		cs.cfg.APISystems = append(cs.cfg.APISystems, sys)
	} else {
		found := false
		for i := range cs.cfg.APISystems {
			if cs.cfg.APISystems[i].ID == sys.ID {
				sys.CreatedAt = cs.cfg.APISystems[i].CreatedAt
				cs.cfg.APISystems[i] = sys
				found = true
				break
			}
		}
		if !found {
			sys.CreatedAt = time.Now().Unix()
			cs.cfg.APISystems = append(cs.cfg.APISystems, sys)
		}
	}
	cs.mu.Unlock()
	return sys, cs.save()
}

func (cs *ConfigStore) DeleteAPISystem(id string) error {
	cs.mu.Lock()
	kept := cs.cfg.APISystems[:0]
	for _, s := range cs.cfg.APISystems {
		if s.ID != id {
			kept = append(kept, s)
		}
	}
	cs.cfg.APISystems = kept
	cs.mu.Unlock()
	return cs.save()
}

// SetAPISystemMaint 设置业务系统的维护窗口截止时间（until=0 解除）。窗口内探测继续、仅抑制告警。
func (cs *ConfigStore) SetAPISystemMaint(id string, until int64) error {
	cs.mu.Lock()
	found := false
	for i := range cs.cfg.APISystems {
		if cs.cfg.APISystems[i].ID == id {
			cs.cfg.APISystems[i].MaintUntil = until
			found = true
			break
		}
	}
	cs.mu.Unlock()
	if !found {
		return fmt.Errorf("api system not found")
	}
	return cs.save()
}

func (cs *ConfigStore) ToggleAPISystem(id string, enabled bool) error {
	cs.mu.Lock()
	found := false
	for i := range cs.cfg.APISystems {
		if cs.cfg.APISystems[i].ID == id {
			cs.cfg.APISystems[i].Enabled = enabled
			found = true
			break
		}
	}
	cs.mu.Unlock()
	if !found {
		return fmt.Errorf("api system not found")
	}
	return cs.save()
}

// ---- 运行器 ----

// apiEndpointStatus 是一个接口最近一次探测的实时结果（内存态，供聚合表「最新状态」列）。
type apiEndpointStatus struct {
	System, Name string
	OK           bool
	Message      string
	LatencyMs    float64
	StatusCode   int
	CertDays     int
	RespBytes    int64
	CheckedAt    int64
}

// apiRunner 定时批量探测所有业务系统的接口。复用 checkRunner 的探测引擎（probeHTTPAdvanced
// + 共享 http.Client），把结果持久化到 VM 并做去抖动的异常/恢复告警。
type apiRunner struct {
	cr       *checkRunner // 复用其 probeHTTPAdvanced 与 httpc
	cfg      *ConfigStore
	store    *Store
	notifier *Notifier
	vm       *vmWriter

	mu        sync.Mutex
	status    map[string]apiEndpointStatus
	down      map[string]bool
	downSince map[string]int64
	lastRun   map[string]time.Time
	failCount map[string]int
	okCount   map[string]int
	sem       chan struct{} // 并发探测限流：避免大量接口同时到期时打爆目标/本机 fd
	txn       *txnState     // 合成事务的调度/状态（迭代 C）
}

func newAPIRunner(cr *checkRunner, cfg *ConfigStore, store *Store, notifier *Notifier, vm *vmWriter) *apiRunner {
	return &apiRunner{
		cr: cr, cfg: cfg, store: store, notifier: notifier, vm: vm,
		status:    map[string]apiEndpointStatus{},
		down:      map[string]bool{},
		downSince: map[string]int64{},
		lastRun:   map[string]time.Time{},
		failCount: map[string]int{},
		okCount:   map[string]int{},
		sem:       make(chan struct{}, 16), // 最多 16 个并发探测
		txn:       newTxnState(),
	}
}

func (ar *apiRunner) Run(tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	ar.sweep()
	ar.sweepTxn()
	for range t.C {
		ar.sweep()
		ar.sweepTxn()
	}
}

func (ar *apiRunner) sweep() {
	now := time.Now()
	for _, sys := range ar.cfg.APISystems() {
		if !sys.Enabled {
			continue
		}
		iv := sys.IntervalSec
		if iv < 5 {
			iv = 60
		}
		for _, ep := range sys.Endpoints {
			if !ep.Enabled || strings.TrimSpace(ep.URL) == "" {
				continue
			}
			ar.mu.Lock()
			last := ar.lastRun[ep.ID]
			due := last.IsZero() || now.Sub(last) >= time.Duration(iv)*time.Second
			if due {
				ar.lastRun[ep.ID] = now
			}
			ar.mu.Unlock()
			if due {
				go ar.probeLimited(sys, ep) // 接口独立并发；受 sem 限流 + panic 恢复
			}
		}
	}
	ar.gc()
}

// gc 清理已删除接口的运行态。
func (ar *apiRunner) gc() {
	live := map[string]bool{}
	for _, sys := range ar.cfg.APISystems() {
		for _, ep := range sys.Endpoints {
			live[ep.ID] = true
		}
	}
	ar.mu.Lock()
	for id := range ar.status {
		if !live[id] {
			delete(ar.status, id)
			delete(ar.down, id)
			delete(ar.downSince, id)
			delete(ar.lastRun, id)
			delete(ar.failCount, id)
			delete(ar.okCount, id)
		}
	}
	ar.mu.Unlock()
}

// probeLimited 是 probe 的受控入口：sem 并发限流 + panic 恢复，供 sweep/runNow 的 go 启动点复用，
// 避免单接口 panic 拖垮整个 server，也避免海量接口同时到期时的 goroutine/连接风暴。
func (ar *apiRunner) probeLimited(sys APISystem, ep APIEndpoint) {
	ar.sem <- struct{}{}
	defer func() { <-ar.sem }()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("apimon probe panic recovered", "system", sys.Name, "endpoint", ep.Name, "err", r)
			ar.store.AddLog(LogEntry{Kind: KindSystem, Level: "warning", Actor: "API性能监控", Host: ep.Name, Message: fmt.Sprintf("接口探测发生 panic 已恢复：%v", r)})
		}
	}()
	ar.probe(sys, ep)
}

func (ar *apiRunner) probe(sys APISystem, ep APIEndpoint) {
	// 按协议分发：websocket 走手写 WS 探测；http/graphql 走 probeHTTPAdvanced（graphql 在 toCheck 里变换请求体+校验）
	probeOnce := func() httpProbeResult {
		if ep.Protocol == "websocket" {
			return probeWebSocket(ep, sys.CommonHeaders)
		}
		return ar.cr.probeHTTPAdvanced(ep.toCheck(sys.CommonHeaders, sys.CommonBody))
	}
	res := probeOnce()
	// 失败重试：抑制瞬时抖动（网络毛刺 / 偶发 5xx），任一次成功即通过（默认 Retries=0 不重试）
	for attempt := 0; !res.ok && attempt < ep.Retries; attempt++ {
		time.Sleep(300 * time.Millisecond)
		res = probeOnce()
	}
	now := time.Now().Unix()

	ar.mu.Lock()
	ar.status[ep.ID] = apiEndpointStatus{
		System: sys.Name, Name: ep.Name, OK: res.ok, Message: res.msg,
		LatencyMs: res.totalMs, StatusCode: res.code, CertDays: res.certDays,
		RespBytes: res.bytes, CheckedAt: now,
	}
	wasDown := ar.down[ep.ID]
	nowDown := wasDown
	const debounceThreshold = 2 // 与拨测一致：连续 2 次才切换状态，抑制抖动
	if !res.ok {
		ar.failCount[ep.ID]++
		ar.okCount[ep.ID] = 0
		if ar.failCount[ep.ID] >= debounceThreshold && !wasDown {
			nowDown = true
			ar.down[ep.ID] = true
			if _, ok := ar.downSince[ep.ID]; !ok {
				ar.downSince[ep.ID] = now
			}
		}
	} else {
		ar.okCount[ep.ID]++
		ar.failCount[ep.ID] = 0
		if ar.okCount[ep.ID] >= debounceThreshold && wasDown {
			nowDown = false
			ar.down[ep.ID] = false
			delete(ar.downSince, ep.ID)
		}
	}
	ar.mu.Unlock()

	// 持久化到 VM（aiops_api_* 指标族，重启后仍可查历史 + 现算聚合）
	ar.vm.enqueueAPI(vmAPISample{
		apiID: ep.ID, system: sys.Name, endpoint: ep.Name, ts: now,
		ok: res.ok, latencyMs: res.totalMs, statusCode: res.code,
		dnsMs: res.dnsMs, tcpMs: res.tcpMs, tlsMs: res.tlsMs, ttfbMs: res.ttfbMs,
		certDays: res.certDays, respBytes: res.bytes,
	})

	if nowDown && !wasDown {
		ar.transition(sys, ep, false, res.msg)
	} else if !nowDown && wasDown {
		ar.transition(sys, ep, true, res.msg)
	}
}

// transition 在接口异常/恢复时写活动日志并推送告警（走与自定义拨测一致的通道）。
func (ar *apiRunner) transition(sys APISystem, ep APIEndpoint, up bool, msg string) {
	lvl := sys.Level
	if lvl == "" {
		lvl = "critical"
	}
	label := sys.Name + " / " + ep.Name
	a := Alert{Level: lvl, Type: "api", Scope: ep.ID, Hostname: label, Timestamp: time.Now().Unix()}
	if up {
		a.Level = "info"
		a.Message = fmt.Sprintf("接口已恢复：%s", label)
	} else {
		a.Message = fmt.Sprintf("接口异常：%s（%s）", label, msg)
	}
	ar.store.AddLog(LogEntry{Kind: KindSystem, Level: a.Level, Actor: "API性能监控", Host: ep.Name, Message: a.Message})
	if sys.MaintUntil > time.Now().Unix() {
		return // 维护窗口内：记录状态变化但抑制告警推送（探测继续，仅静音通知）
	}
	if cfg := ar.cfg.Get(); cfg.AlertsEnabled {
		ar.notifier.enqueuePush(cfg, a, !up)
	}
}

// statusSnapshot 返回所有接口的实时状态副本（供聚合表合并）。
func (ar *apiRunner) statusSnapshot() map[string]apiEndpointStatus {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	out := make(map[string]apiEndpointStatus, len(ar.status))
	for k, v := range ar.status {
		out[k] = v
	}
	return out
}

// downSnapshot 返回当前处于异常态的接口集合（供聚合表标红/告警列表）。
func (ar *apiRunner) downSnapshot() map[string]int64 {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	out := make(map[string]int64, len(ar.downSince))
	for k := range ar.down {
		if ar.down[k] {
			out[k] = ar.downSince[k]
		}
	}
	return out
}

// runNow 立即探测某业务系统的全部接口（新增/编辑后触发，快速出结果）。
func (ar *apiRunner) runNow(systemID string) {
	for _, sys := range ar.cfg.APISystems() {
		if sys.ID != systemID || !sys.Enabled {
			continue
		}
		for _, ep := range sys.Endpoints {
			if !ep.Enabled || strings.TrimSpace(ep.URL) == "" {
				continue
			}
			ar.mu.Lock()
			ar.lastRun[ep.ID] = time.Now()
			ar.mu.Unlock()
			go ar.probeLimited(sys, ep)
		}
		return
	}
}
