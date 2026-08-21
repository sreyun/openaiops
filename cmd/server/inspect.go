package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Host deep-inspection (agent module host_inspect) — batch run + web report store.

const (
	hostInspectCap       = 100
	hostInspectOutCap    = 2 << 20 // 2 MiB JSON reports
	hostInspectTimeout   = 180
	hostInspectConcLimit = 8
)

// hostInspectFindingBrief is a tiny finding row for list/poll without full reports.
type hostInspectFindingBrief struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type hostInspectItem struct {
	HostID        string                    `json:"host_id"`
	Hostname      string                    `json:"hostname"`
	OS            string                    `json:"os"`
	IP            string                    `json:"ip"`
	Status        string                    `json:"status"` // pending|running|ok|warn|crit|error
	Error         string                    `json:"error,omitempty"`
	Warnings      int                       `json:"warnings"`
	Critical      int                       `json:"critical"`
	OSFamily      string                    `json:"os_family,omitempty"`
	CPUPct        *float64                  `json:"cpu_pct,omitempty"`
	MemPct        *float64                  `json:"mem_pct,omitempty"`
	FindingsBrief []hostInspectFindingBrief `json:"findings_brief,omitempty"`
	HasReport     bool                      `json:"has_report,omitempty"`
	Report        json.RawMessage           `json:"report,omitempty"`
	FinishedAt    int64                     `json:"finished_at,omitempty"`
	DurationMs    int64                     `json:"duration_ms,omitempty"`
}

type hostInspectBatch struct {
	ID         string            `json:"id"`
	Operator   string            `json:"operator"`
	Source     string            `json:"source,omitempty"` // e.g. playbook: nightly-inspect
	Status     string            `json:"status"`           // running|done
	StartedAt  int64             `json:"started_at"`
	FinishedAt int64             `json:"finished_at,omitempty"`
	HostCount  int               `json:"host_count"`
	DoneCount  int               `json:"done_count"`
	OKCount    int               `json:"ok_count"`
	WarnCount  int               `json:"warn_count"`
	CritCount  int               `json:"crit_count"`
	ErrCount   int               `json:"err_count"`
	Items      []hostInspectItem `json:"items"`
}

type hostInspectManager struct {
	mu      sync.RWMutex
	batches []*hostInspectBatch
	seq     atomic.Uint64
	persist func([]*hostInspectBatch) // optional PG persist hook
}

func newHostInspectManager() *hostInspectManager {
	return &hostInspectManager{}
}

func (m *hostInspectManager) setPersist(fn func([]*hostInspectBatch)) {
	m.mu.Lock()
	m.persist = fn
	m.mu.Unlock()
}

func (m *hostInspectManager) importBatches(list []*hostInspectBatch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(list) == 0 {
		return
	}
	// Batches persisted as "running" cannot resume after process restart — mark them
	// failed so the UI does not show forever-stuck「巡检中」rows (same class of bug
	// as orphaned security scans).
	for _, b := range list {
		if b == nil {
			continue
		}
		if b.Status == "running" || b.Status == "pending" {
			b.Status = "done"
			for i := range b.Items {
				st := b.Items[i].Status
				if st == "running" || st == "pending" || st == "" {
					b.Items[i].Status = "error"
					if strings.TrimSpace(b.Items[i].Error) == "" {
						b.Items[i].Error = "服务重启，巡检中断"
					}
					b.ErrCount++
					b.DoneCount++
				}
			}
			if b.FinishedAt == 0 {
				b.FinishedAt = time.Now().Unix()
			}
		}
	}
	m.batches = list
	if len(m.batches) > hostInspectCap {
		m.batches = m.batches[:hostInspectCap]
	}
}

func (m *hostInspectManager) snapshot() []*hostInspectBatch {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*hostInspectBatch, len(m.batches))
	for i, b := range m.batches {
		out[i] = cloneInspectBatch(b)
	}
	return out
}

func (m *hostInspectManager) persistAsync() {
	m.mu.RLock()
	fn := m.persist
	m.mu.RUnlock()
	if fn == nil {
		return
	}
	go fn(m.snapshot())
}

func (m *hostInspectManager) list() []*hostInspectBatch {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*hostInspectBatch, len(m.batches))
	for i, b := range m.batches {
		out[i] = cloneInspectBatch(b)
	}
	return out
}

func (m *hostInspectManager) get(id string) (*hostInspectBatch, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, b := range m.batches {
		if b.ID == id {
			return cloneInspectBatch(b), true
		}
	}
	return nil, false
}

func (m *hostInspectManager) add(b *hostInspectBatch) {
	m.mu.Lock()
	m.batches = append([]*hostInspectBatch{b}, m.batches...)
	if len(m.batches) > hostInspectCap {
		m.batches = m.batches[:hostInspectCap]
	}
	m.mu.Unlock()
	m.persistAsync()
}

func (m *hostInspectManager) updateItem(batchID string, idx int, item hostInspectItem, bumpCounts bool) {
	m.mu.Lock()
	changed := false
	for _, b := range m.batches {
		if b.ID != batchID {
			continue
		}
		if idx < 0 || idx >= len(b.Items) {
			break
		}
		b.Items[idx] = item
		if bumpCounts {
			b.DoneCount++
			switch item.Status {
			case "ok":
				b.OKCount++
			case "warn":
				b.WarnCount++
			case "crit":
				b.CritCount++
			default:
				b.ErrCount++
			}
		}
		changed = true
		break
	}
	m.mu.Unlock()
	if changed {
		m.persistAsync()
	}
}

func (m *hostInspectManager) finish(batchID string) {
	m.mu.Lock()
	for _, b := range m.batches {
		if b.ID == batchID {
			b.Status = "done"
			b.FinishedAt = time.Now().Unix()
			break
		}
	}
	m.mu.Unlock()
	m.persistAsync()
}

func cloneInspectBatch(b *hostInspectBatch) *hostInspectBatch {
	if b == nil {
		return nil
	}
	cp := *b
	cp.Items = make([]hostInspectItem, len(b.Items))
	copy(cp.Items, b.Items)
	for i := range cp.Items {
		if len(b.Items[i].Report) > 0 {
			cp.Items[i].Report = append(json.RawMessage(nil), b.Items[i].Report...)
		}
		if len(b.Items[i].FindingsBrief) > 0 {
			cp.Items[i].FindingsBrief = append([]hostInspectFindingBrief(nil), b.Items[i].FindingsBrief...)
		}
	}
	return &cp
}

// compactInspectBatch strips bulky report JSON for list/poll responses.
// Keeps status counts, metrics hints and findings_brief for fleet UI.
func compactInspectBatch(b *hostInspectBatch) *hostInspectBatch {
	out := cloneInspectBatch(b)
	if out == nil {
		return nil
	}
	for i := range out.Items {
		if len(out.Items[i].Report) > 0 {
			out.Items[i].HasReport = true
			out.Items[i].Report = nil
		}
	}
	return out
}

func compactInspectBatches(list []*hostInspectBatch) []*hostInspectBatch {
	out := make([]*hostInspectBatch, len(list))
	for i, b := range list {
		out[i] = compactInspectBatch(b)
	}
	return out
}

func recalcInspectBatchCounts(b *hostInspectBatch) {
	if b == nil {
		return
	}
	b.OKCount, b.WarnCount, b.CritCount, b.ErrCount, b.DoneCount = 0, 0, 0, 0, 0
	for _, it := range b.Items {
		switch it.Status {
		case "pending", "running", "":
			continue
		case "ok":
			b.OKCount++
		case "warn":
			b.WarnCount++
		case "crit":
			b.CritCount++
		default:
			b.ErrCount++
		}
		b.DoneCount++
	}
}

func parseHostInspectOutput(host *Host, output string, durationMs int64) hostInspectItem {
	item := hostInspectItem{
		HostID: host.ID, Hostname: host.Hostname, OS: host.OS, IP: host.IP,
		DurationMs: durationMs, FinishedAt: time.Now().Unix(),
	}
	body := strings.TrimSpace(output)
	if i := strings.Index(body, "{"); i >= 0 {
		body = body[i:]
	}
	if j := strings.LastIndex(body, "}"); j >= 0 {
		body = body[:j+1]
	}
	var rep struct {
		Host struct {
			OSFamily string `json:"os_family"`
		} `json:"host"`
		Result struct {
			Warnings int `json:"warnings"`
			Critical int `json:"critical"`
		} `json:"result"`
		Metrics struct {
			CPU *float64 `json:"cpu_usage_pct"`
			Mem *float64 `json:"mem_usage_pct"`
		} `json:"metrics"`
		Findings []struct {
			Level   string `json:"level"`
			Message string `json:"message"`
			Title   string `json:"title"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		item.Status = "error"
		item.Error = "巡检结果不是有效 JSON: " + truncateStr(body, 120)
		if err != nil {
			item.Error += " (" + err.Error() + ")"
		}
		return item
	}
	item.Report = json.RawMessage(body)
	item.HasReport = true
	item.Warnings = rep.Result.Warnings
	item.Critical = rep.Result.Critical
	item.OSFamily = rep.Host.OSFamily
	item.CPUPct = rep.Metrics.CPU
	item.MemPct = rep.Metrics.Mem
	if n := len(rep.Findings); n > 0 {
		limit := n
		if limit > 60 {
			limit = 60
		}
		item.FindingsBrief = make([]hostInspectFindingBrief, 0, limit)
		for _, f := range rep.Findings[:limit] {
			msg := strings.TrimSpace(f.Message)
			if msg == "" {
				msg = strings.TrimSpace(f.Title)
			}
			if msg == "" {
				continue
			}
			item.FindingsBrief = append(item.FindingsBrief, hostInspectFindingBrief{
				Level: strings.TrimSpace(f.Level), Message: msg,
			})
		}
	}
	switch {
	case rep.Result.Critical > 0:
		item.Status = "crit"
	case rep.Result.Warnings > 0:
		item.Status = "warn"
	default:
		item.Status = "ok"
	}
	return item
}

func playbookInspectBatchID(execID int64) string {
	return fmt.Sprintf("insp-pb-%d", execID)
}

// ingestPlaybookHostInspect stores a successful playbook host_inspect step into the
// inspect batch store (one batch per playbook execution).
func (s *Server) ingestPlaybookHostInspect(playbookName string, execID int64, host *Host, operator, output string, durationMs int64) {
	if s.inspect == nil || host == nil {
		return
	}
	source := "playbook: " + strings.TrimSpace(playbookName)
	if source == "playbook:" {
		source = "playbook"
	}
	item := parseHostInspectOutput(host, output, durationMs)
	s.inspect.ingestPlaybookItem(playbookInspectBatchID(execID), source, operator, item)
}

func (m *hostInspectManager) ingestPlaybookItem(batchID, source, operator string, item hostInspectItem) {
	m.mu.Lock()
	var batch *hostInspectBatch
	for _, b := range m.batches {
		if b != nil && b.ID == batchID {
			batch = b
			break
		}
	}
	if batch == nil {
		batch = &hostInspectBatch{
			ID: batchID, Operator: operator, Source: source,
			Status: "running", StartedAt: time.Now().Unix(),
			Items: make([]hostInspectItem, 0, 4),
		}
		m.batches = append([]*hostInspectBatch{batch}, m.batches...)
		if len(m.batches) > hostInspectCap {
			m.batches = m.batches[:hostInspectCap]
		}
	} else if strings.TrimSpace(batch.Source) == "" && source != "" {
		batch.Source = source
	}
	found := false
	for i, it := range batch.Items {
		if it.HostID == item.HostID {
			batch.Items[i] = item
			found = true
			break
		}
	}
	if !found {
		batch.Items = append(batch.Items, item)
	}
	recalcInspectBatchCounts(batch)
	batch.HostCount = len(batch.Items)
	m.mu.Unlock()
	m.persistAsync()
}

func (m *hostInspectManager) finishPlaybookBatch(batchID string) {
	m.mu.Lock()
	changed := false
	for _, b := range m.batches {
		if b == nil || b.ID != batchID {
			continue
		}
		if b.Status != "done" {
			b.Status = "done"
			b.FinishedAt = time.Now().Unix()
		}
		b.HostCount = len(b.Items)
		recalcInspectBatchCounts(b)
		changed = true
		break
	}
	m.mu.Unlock()
	if changed {
		m.persistAsync()
	}
}

// ---- HTTP ----

func (s *Server) handleListHostInspect(w http.ResponseWriter, r *http.Request) {
	if s.inspect == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	// Always compact list: full reports can be tens of MB across batches.
	list := compactInspectBatches(s.inspect.list())
	writeJSON(w, http.StatusOK, s.filterInspectBatchesForUser(r, list))
}

func (s *Server) handleGetHostInspect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, ok := s.inspect.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "巡检批次不存在"})
		return
	}
	view := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("view")))
	compact := r.URL.Query().Get("compact") == "1" || view == "compact" || view == "summary"
	hostID := strings.TrimSpace(r.URL.Query().Get("host_id"))
	if hostID != "" {
		if !s.requireHostAccess(w, r, hostID) {
			return
		}
		// One host full report + compact siblings — avoids pulling N×2MiB for a single view.
		out := compactInspectBatch(b)
		out = s.filterInspectBatchForUser(r, out)
		for i, it := range out.Items {
			if it.HostID == hostID {
				// Re-attach full report for the requested host from original batch.
				for _, src := range b.Items {
					if src.HostID == hostID && len(src.Report) > 0 {
						out.Items[i].Report = append(json.RawMessage(nil), src.Report...)
						out.Items[i].HasReport = true
						break
					}
				}
				break
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var out *hostInspectBatch
	if compact {
		out = compactInspectBatch(b)
	} else {
		out = b
	}
	writeJSON(w, http.StatusOK, s.filterInspectBatchForUser(r, out))
}

// filterInspectBatchForUser drops host items outside the caller's host scope.
func (s *Server) filterInspectBatchForUser(r *http.Request, b *hostInspectBatch) *hostInspectBatch {
	if b == nil {
		return nil
	}
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return b
	}
	cp := *b
	cp.Items = make([]hostInspectItem, 0, len(b.Items))
	for _, it := range b.Items {
		if s.userCanAccessHost(u, it.HostID) {
			cp.Items = append(cp.Items, it)
		}
	}
	cp.HostCount = len(cp.Items)
	return &cp
}

func (s *Server) filterInspectBatchesForUser(r *http.Request, list []*hostInspectBatch) []*hostInspectBatch {
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return list
	}
	out := make([]*hostInspectBatch, len(list))
	for i, b := range list {
		out[i] = s.filterInspectBatchForUser(r, b)
	}
	return out
}

func (s *Server) handleRunHostInspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HostIDs    []string `json:"host_ids"`
		TimeoutSec int      `json:"timeout_sec"`
		Profile    string   `json:"profile"` // quick | standard | deep
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	timeout := req.TimeoutSec
	if timeout < 30 {
		timeout = hostInspectTimeout
	}
	if timeout > 600 {
		timeout = 600
	}
	profile := strings.ToLower(strings.TrimSpace(req.Profile))
	switch profile {
	case "quick", "fast":
		profile = "quick"
	case "deep", "full":
		profile = "deep"
		if timeout < 240 {
			timeout = 240
		}
	default:
		profile = "standard"
	}

	offlineSec := int64(s.cfg.Thresholds().OfflineAfter.Seconds())
	if offlineSec <= 0 {
		offlineSec = 120
	}
	now := time.Now().Unix()
	all := s.store.ListHosts()
	var targets []*Host
	want := map[string]bool{}
	if len(req.HostIDs) == 0 {
		for _, h := range all {
			if h != nil && h.LastSeen > 0 && now-h.LastSeen <= offlineSec {
				targets = append(targets, h)
			}
		}
	} else {
		for _, id := range req.HostIDs {
			want[id] = true
		}
		for _, h := range all {
			if h != nil && want[h.ID] {
				targets = append(targets, h)
			}
		}
	}
	if u, ok := s.currentUser(r); ok && u.hostScopeRestricted() {
		filtered := make([]*Host, 0, len(targets))
		for _, h := range targets {
			if s.userCanAccessHost(u, h.ID) {
				filtered = append(filtered, h)
			}
		}
		targets = filtered
	}
	if len(targets) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有可体检的主机（请选择在线且已授权的主机）"})
		return
	}

	id := fmt.Sprintf("insp-%d-%d", time.Now().Unix(), s.inspect.seq.Add(1))
	items := make([]hostInspectItem, len(targets))
	for i, h := range targets {
		items[i] = hostInspectItem{
			HostID: h.ID, Hostname: h.Hostname, OS: h.OS, IP: h.IP, Status: "pending",
		}
	}
	batch := &hostInspectBatch{
		ID: id, Operator: s.actorName(r), Status: "running",
		StartedAt: time.Now().Unix(), HostCount: len(targets), Items: items,
	}

	s.inspect.add(batch)

	go s.runHostInspectBatch(batch.ID, targets, timeout, profile)
	writeJSON(w, http.StatusAccepted, batch)
}

// handleCompareHostInspect returns two finished batches for side-by-side history compare.
func (s *Server) handleCompareHostInspect(w http.ResponseWriter, r *http.Request) {
	aID := r.URL.Query().Get("a")
	bID := r.URL.Query().Get("b")
	if aID == "" || bID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要查询参数 a 与 b（两个批次 ID）"})
		return
	}
	a, okA := s.inspect.get(aID)
	b, okB := s.inspect.get(bID)
	if !okA || !okB {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "体检批次不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"a": s.filterInspectBatchForUser(r, a),
		"b": s.filterInspectBatchForUser(r, b),
	})
}

func (s *Server) runHostInspectBatch(batchID string, hosts []*Host, timeoutSec int, profile string) {
	sem := make(chan struct{}, hostInspectConcLimit)
	var wg sync.WaitGroup
	if profile == "" {
		profile = "standard"
	}
	cmd := buildModuleCommand("host_inspect", map[string]string{"profile": profile}, nil)
	for i, h := range hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, host *Host) {
			defer wg.Done()
			defer func() { <-sem }()
			item := hostInspectItem{
				HostID: host.ID, Hostname: host.Hostname, OS: host.OS, IP: host.IP, Status: "running",
			}
			s.inspect.updateItem(batchID, idx, item, false)
			start := time.Now()
			out, kind, err := s.execCommandOnHostSized(host, cmd, timeoutSec, hostInspectOutCap)
			item.DurationMs = time.Since(start).Milliseconds()
			if err != nil && kind != execExit {
				item.Status = "error"
				item.Error = err.Error()
				if out != "" {
					item.Error += " | " + truncateStr(out, 200)
				}
				item.FinishedAt = time.Now().Unix()
				s.inspect.updateItem(batchID, idx, item, true)
				return
			}
			item = parseHostInspectOutput(host, out, item.DurationMs)
			s.inspect.updateItem(batchID, idx, item, true)
		}(i, h)
	}
	wg.Wait()
	s.inspect.finish(batchID)
	slog.Info("host inspect batch done", "id", batchID, "hosts", len(hosts))
}

// execCommandOnHostSized is like execCommandOnHost but allows a larger output buffer (for JSON reports).
func (s *Server) execCommandOnHostSized(h *Host, command string, timeoutSec, maxBytes int) (string, execKind, error) {
	return s.execCommandOnHostCtx(context.Background(), 0, h, command, timeoutSec, maxBytes)
}

// waitAgentPickup 等 Agent 把 tx 上行流挂上来（markAgentUp）。
//
// 这里原本是一句直白的 `case <-time.After(execPickupTimeout)` → "Agent 未接单"。
// 它在一种很常见的部署下是错判：反向代理开着 nginx 默认的 proxy_request_buffering，
// tx 那个"边跑边写"的请求体会被整包缓冲到命令结束才转发给上游，于是接单信号必然迟到，
// 而 Agent 其实一直在正常执行。判死的代价是自动升级永远失败、且失败原因指向 Agent，
// 运维照着这句话去查 Agent 只会白查——真正要改的是反代的四行配置。
//
// 所以超时不再直接判死，而是先问一句"Agent 到底在不在"：alive 心跳（exec 会话每 1.5s
// 一次的小 GET）或挂着的 rx 流都不受请求体缓冲影响。心跳还在 → 认定是反代缓冲，记一条
// 带修复方法的告警，并把等待延长到命令自己的预算上限——缓冲的请求体会在命令结束时整包
// 到达，输出与退出码一个不少，这一次升级/剧本照常跑完。心跳没了 → 才是真的没接单。
func (s *Server) waitAgentPickup(ctx context.Context, sess *termSession, h *Host, timeoutSec int) (execKind, error) {
	start := time.Now()
	// 确认 Agent 还活着之后，最多再按命令自身的预算等这么久。
	hardStop := start.Add(execPickupTimeout + time.Duration(timeoutSec)*time.Second)
	next := execPickupTimeout
	buffered := false
	for {
		timer := time.NewTimer(next)
		select {
		case <-sess.agentUp:
			timer.Stop()
			return execOK, nil
		case <-sess.done:
			timer.Stop()
			return execAbnormal, fmt.Errorf("%s", Tz("playbook.abnormal"))
		case <-ctx.Done():
			timer.Stop()
			return execCancelled, fmt.Errorf("%s", "剧本已停止")
		case <-timer.C:
		}
		if !sess.agentAttached() {
			// 没有任何旁证：这才是真正意义上的"没接单"（离线、指纹不符、Agent 挂了）。
			return execNoPickup, fmt.Errorf("%s", Tz("playbook.no_pickup"))
		}
		if !buffered {
			buffered = true
			s.noteEdgeUpstreamBuffered(h.ID, h.Hostname)
		}
		if time.Now().After(hardStop) {
			return execNoPickup, edgeProxyBufferedError(h.Hostname, time.Since(start))
		}
		// 心跳还在，就按小步长继续复查——Agent 一旦掉线要马上收敛，不能干等到硬上限。
		next = 5 * time.Second
	}
}

// execCommandOnHostCtx runs a one-shot agent exec, honouring ctx cancellation and
// tagging the session with execID so fleet cancel can abort it without host kill scripts.
func (s *Server) execCommandOnHostCtx(ctx context.Context, execID int64, h *Host, command string, timeoutSec, maxBytes int) (string, execKind, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return "", execCancelled, fmt.Errorf("%s", "剧本已停止")
	}
	if timeoutSec < 5 {
		timeoutSec = 30
	}
	if maxBytes < 64*1024 {
		maxBytes = 512 * 1024
	}
	sess := s.term.createExecWithExecID(h.ID, h.Hostname, command, execID)
	defer s.term.remove(sess.id)
	defer sess.close()
	s.term.notifyAgent(h.ID, sess.id)

	if kind, err := s.waitAgentPickup(ctx, sess, h, timeoutSec); err != nil {
		return "", kind, err
	}

	var output []byte
	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()
	for {
		select {
		case b := <-sess.toBrowser:
			output = append(output, b...)
			if len(output) > maxBytes {
				output = output[len(output)-maxBytes:]
			}
		case <-timer.C:
			out, kind, err := parseExecOutput(output, true)
			return out, kind, err
		case <-sess.done:
			draining := true
			for draining {
				select {
				case b := <-sess.toBrowser:
					output = append(output, b...)
				default:
					draining = false
				}
			}
			out, kind, err := parseExecOutput(output, false)
			return out, kind, err
		case <-ctx.Done():
			out := strings.TrimRight(string(output), "\r\n")
			if out != "" {
				out += "\n"
			}
			return out + "（剧本已手动停止）", execCancelled, fmt.Errorf("%s", "剧本已停止")
		}
	}
}
