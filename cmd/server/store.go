package main

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"aiops-monitor/shared"
)

const (
	maxSamples        = 240  // cap for the legacy /metrics endpoint (tail of raw history)
	maxEvents         = 300  // global ring of recent plugin events
	eventsPerAPI      = 100  // cap returned by the events endpoint
	deleteSuppressSec = 60   // ignore a host's re-reports for this long after a manual delete
	maxActivity       = 1000 // ring of recent activity-log entries (persisted)
	maxAlertHistory   = 500  // ring of persisted alert event records
	eventCooldownSec  = 300  // min gap between identical plugin events (noise suppression)

	// History storage constants (multi-tier downsampling)
	histRawMax     = 1200 // raw samples: ~1.5h at 5s interval
	hist1mMax      = 2880 // 1-min aggregates: 48h (2880 points)
	hist5mMax      = 8640 // 5-min aggregates: 30 days (8640 points, 12/hour × 24h × 30d)
	hist1mInterval = 60   // aggregate to 1-min every 60s
	hist5mInterval = 300  // aggregate to 5-min every 300s
)

// Host is the aggregate record the server keeps per agent.
type Host struct {
	ID           string              `json:"id"`
	Hostname     string              `json:"hostname"`
	OS           string              `json:"os"`
	Platform     string              `json:"platform"`
	Arch         string              `json:"arch"`
	IP           string              `json:"ip"`
	Kernel       string              `json:"kernel"`
	Category     string              `json:"category"`
	AgentVersion string              `json:"agent_version,omitempty"` // running agent binary version
	ServerURL    string              `json:"server_url,omitempty"`    // agent-configured report base (relay-aware)
	Fingerprint  string              `json:"fingerprint,omitempty"`   // machine fingerprint (machine-id+MAC), bound at registration
	FirstSeen    int64               `json:"first_seen"`
	LastSeen     int64               `json:"last_seen"`
	Latest       *shared.Sample      `json:"latest"`
	Custom       map[string]float64  `json:"custom,omitempty"` // latest custom gauges from plugins
	Desktop      *shared.DesktopInfo `json:"desktop,omitempty"`

	histRaw  []shared.Sample // RAM cache only (~1.5h); durable history is VictoriaMetrics
	hist1m   []shared.Sample // RAM cache (48h of 1-min aggregates) — not persisted
	hist5m   []shared.Sample // RAM cache (30d of 5-min aggregates) — not persisted
	last1mTs int64           // timestamp of last 1-min aggregation
	last5mTs int64           // timestamp of last 5-min aggregation
}

// storedEvent decorates a plugin event with the host it came from.
type storedEvent struct {
	shared.Event
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname"`
}

// LogEntry is one line in the activity log. It unifies operator actions (operation),
// machine/system actions such as alert transitions and notifications (system),
// and plugin findings (plugin).
type LogEntry struct {
	Timestamp int64  `json:"timestamp"`
	Kind      string `json:"kind"`               // operation | system | plugin | terminal
	Level     string `json:"level"`              // info | warning | critical
	Actor     string `json:"actor"`              // display identity (username, or IP if anonymous)
	Username  string `json:"username,omitempty"` // dedicated login name for audit (independent of Actor/IP)
	IP        string `json:"ip,omitempty"`       // real client IP for audit traceability
	Host      string `json:"host,omitempty"`
	Message   string `json:"message"`
}

// AlertRecord is a persistent record of a single alert lifecycle event.
// Created when an alert fires; updated when it resolves. Survives restart via PG.
type AlertRecord struct {
	ID         int64   `json:"id"`
	Key        string  `json:"key"` // hostID/type/scope (dedup key)
	HostID     string  `json:"host_id"`
	Hostname   string  `json:"hostname"`
	IP         string  `json:"ip,omitempty"`
	Level      string  `json:"level"` // warning | critical
	Type       string  `json:"type"`  // cpu | memory | disk | offline | ...
	Scope      string  `json:"scope,omitempty"`
	Message    string  `json:"message"`
	Value      float64 `json:"value"`
	FiredAt    int64   `json:"fired_at"`
	ResolvedAt int64   `json:"resolved_at,omitempty"` // 0 = still firing
	Status     string  `json:"status"`                // firing | resolved | acknowledged | silenced
}

// Store holds all host state and a ring of recent plugin events.
type Store struct {
	mu           sync.RWMutex
	hosts        map[string]*Host
	events       []storedEvent
	activity     []LogEntry
	deleted      map[string]int64  // hostID -> unix time of manual deletion (re-add suppression)
	lastEvent    map[string]int64  // dedup key -> last unix time (plugin-event noise suppression)
	alertStates  map[string]string // alert key -> "acknowledged" | "silenced" (persisted)
	alertHistory []AlertRecord     // persistent alert lifecycle records (ring buffer, cap=maxAlertHistory)
	alertSeq     int64             // monotonically increasing ID for AlertRecord
	dirty        bool              // set on every mutation; consumed by the embedded DB's autosave
	pg           *pgStore          // when set, audit log + events are also written to PostgreSQL
	onAudit      func(LogEntry)    // optional SIEM/SOC export hook (set by Server)
}

// BindPG wires PostgreSQL as the durable store for host metadata, the audit log,
// plugin events and alert-ack states, seeding the in-memory state from it.
func (s *Store) BindPG(pg *pgStore) {
	if pg == nil {
		return
	}
	audit, _ := pg.loadRecentAudit(maxActivity)
	events, _ := pg.loadRecentEvents(maxEvents)
	hosts, _ := pg.loadHosts()
	alertHistory, _ := pg.loadRecentAlerts(maxAlertHistory)
	var alertStates map[string]string
	if raw, _ := pg.loadKV("alert_states"); raw != nil {
		_ = json.Unmarshal(raw, &alertStates)
	}
	s.mu.Lock()
	s.pg = pg
	if len(audit) > 0 {
		s.activity = audit
	}
	if len(events) > 0 {
		s.events = events
	}
	for _, h := range hosts { // metadata + Latest; durable history lives in VictoriaMetrics
		if h.ID != "" {
			hh := *h
			s.hosts[h.ID] = &hh
		}
	}
	if alertStates != nil {
		s.alertStates = alertStates
	}
	if len(alertHistory) > 0 {
		s.alertHistory = alertHistory
		for _, r := range alertHistory {
			if r.ID > s.alertSeq {
				s.alertSeq = r.ID
			}
		}
	}
	s.mu.Unlock()
}

// exportHosts returns copies of every host's metadata WITHOUT the history tiers
// (those live in VictoriaMetrics) — for periodic persistence to PostgreSQL.
func (s *Store) exportHosts() []*Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		cp := *h
		cp.histRaw, cp.hist1m, cp.hist5m = nil, nil, nil
		out = append(out, &cp)
	}
	return out
}

// exportAlertStates returns a copy of the ack/silence state map.
func (s *Store) exportAlertStates() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.alertStates))
	for k, v := range s.alertStates {
		out[k] = v
	}
	return out
}

func NewStore() *Store {
	return &Store{hosts: make(map[string]*Host), deleted: make(map[string]int64), lastEvent: make(map[string]int64), alertStates: make(map[string]string)}
}

// MarkDirty flags the store so the next AutoSave persists. Used by SRE managers
// (incidents / tickets) whose state lives in the DB snapshot but not the Store.
func (s *Store) MarkDirty() {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// GetHost returns a shallow copy of one host by id (for fingerprint verification).
func (s *Store) GetHost(id string) (*Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[id]
	if !ok {
		return nil, false
	}
	cp := *h
	return &cp, true
}

// RegisterHost binds a machine fingerprint to a host record at registration time.
// It creates the host if absent, or fills in a missing fingerprint. A conflicting
// fingerprint on an existing host is NOT overwritten (prevents token-holder
// identity hijack); callers should reject the registration when Fingerprint
// remains unequal to the request. Token-based admission is checked beforehand.
func (s *Store) RegisterHost(hostID, hostname, fingerprint string) *Host {
	return s.registerHost(hostID, hostname, fingerprint, false)
}

// RegisterHostRebindFP is used when an install token authorizes updating the
// machine fingerprint bound to an existing host (Agent upgrades that stabilize
// the fingerprint algorithm, or NIC-enumeration changes on Windows 11).
func (s *Store) RegisterHostRebindFP(hostID, hostname, fingerprint string) *Host {
	return s.registerHost(hostID, hostname, fingerprint, true)
}

func (s *Store) registerHost(hostID, hostname, fingerprint string, allowFPRebind bool) *Host {
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	if dt, ok := s.deleted[hostID]; ok {
		if now-dt < deleteSuppressSec {
			return &Host{ID: hostID} // recently deleted; suppress re-registration briefly
		}
		delete(s.deleted, hostID)
	}
	h, ok := s.hosts[hostID]
	if !ok {
		h = &Host{ID: hostID, Hostname: hostname, Fingerprint: fingerprint, FirstSeen: now, LastSeen: now}
		s.hosts[hostID] = h
		s.dirty = true
		return h
	}
	// Existing record: fill missing fingerprint, or rebind when explicitly allowed.
	if fingerprint != "" && h.Fingerprint != fingerprint {
		if h.Fingerprint == "" || allowFPRebind {
			h.Fingerprint = fingerprint
			s.dirty = true
		}
	}
	if hostname != "" && h.Hostname != hostname {
		h.Hostname = hostname
		s.dirty = true
	}
	return h
}

// CanonicalHostID resolves a machine fingerprint to the host id already
// established for that machine, so a reinstalled agent keeps its identity.
//
// 为什么需要它：Agent 的 host_id 是随机生成后存在本地状态文件里的，卸载重装会把
// 状态文件一起删掉 → 新的随机 id。而**平台里的一切都按 host_id 存**：VM 指标的
// host 标签、日志、告警、事件、硬件快照与变更、Flow 明细……于是同一台物理机重装
// 一次，历史就被从中间劈成两半，旧的那半再也关联不到这台机器。
//
// 认指纹不认 id：指纹 = sha256(machine-id | 主 MAC)，跨重装稳定、跨机器唯一
// （克隆状态文件到别的机器时指纹对不上，Agent 侧会自行重新生成 id，不会误判成同一台）。
//
// 取 FirstSeen 最早的那条作为规范身份：这样即使之前已经因重装攒下了重复记录，
// Agent 升级后也会认回最初那条，历史自动接续，多余的记录随后可被清理。
// 返回 (id, true) 表示调用方应改用 id；(_, false) 表示保持原样。
func (s *Store) CanonicalHostID(claimed, fingerprint string) (string, bool) {
	if fingerprint == "" {
		return "", false // 没有指纹就无法判定同一台机器，绝不猜
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var oldest *Host
	for _, h := range s.hosts {
		if h.Fingerprint != fingerprint {
			continue
		}
		if oldest == nil || h.FirstSeen < oldest.FirstSeen {
			oldest = h
		}
	}
	if oldest == nil || oldest.ID == claimed {
		return "", false // 该机器还没记录，或它本来就是规范身份
	}
	return oldest.ID, true
}

// UpsertAuthenticated applies a report after verifying the agent's fingerprint
// against the one bound at registration. Returns (nil, false) when the host is
// unregistered or the fingerprint does not match — the caller must reject the
// report with 403. Hosts whose fingerprint was never bound (legacy records)
// are bound on first use (TOFU) instead of being rejected. Verification and
// update happen under a single lock to avoid a TOCTOU window (host deleted
// between check and upsert) and the double-lock overhead of GetHost + Upsert
// on the hot report path.
func (s *Store) UpsertAuthenticated(r shared.Report, fingerprint string) (*Host, bool) {
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()

	if dt, ok := s.deleted[r.HostID]; ok {
		if now-dt < deleteSuppressSec {
			return nil, false // recently deleted by an operator; ignore re-report
		}
		delete(s.deleted, r.HostID) // suppression window elapsed
	}

	h, ok := s.hosts[r.HostID]
	if !ok {
		return nil, false // not registered
	}
	if h.Fingerprint == "" {
		// TOFU（trust-on-first-use）：老主机记录存在但指纹未绑定（早期版本注册
		// 不写指纹 / 历史导入 / 升级迁移），首次带指纹上报即自动绑定，避免
		// 这类 agent 永远 403 离线。已绑定指纹的主机仍严格比对（见下）。
		if fingerprint == "" {
			return nil, false // 上报也不带指纹，无从绑定
		}
		h.Fingerprint = fingerprint
		slog.Info("TOFU：为指纹未绑定的老主机自动绑定机器指纹", "host", shortID(h.ID), "hostname", h.Hostname)
	} else if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(h.Fingerprint)) != 1 {
		return nil, false // fingerprint mismatch
	}
	h.Hostname = r.Hostname
	h.OS = r.OS
	h.Platform = r.Platform
	h.Arch = r.Arch
	h.IP = r.IP
	h.Kernel = r.Kernel
	h.Category = r.Category
	if v := strings.TrimSpace(r.AgentVersion); v != "" {
		h.AgentVersion = v
	}
	if v := strings.TrimSpace(r.ServerURL); v != "" {
		h.ServerURL = strings.TrimRight(v, "/")
	}
	h.LastSeen = now
	if r.Desktop != nil {
		cp := *r.Desktop
		h.Desktop = &cp
	}

	sample := shared.Sample{Timestamp: now, Metrics: r.Metrics}
	sample.ProcessNames = nil // history never stores process lists (only Latest keeps them)

	// ---- Time-series history (multi-tier downsampling) ----
	// Tier 1: Raw samples (5s interval, ~1.5h)
	h.histRaw = append(h.histRaw, sample)
	if len(h.histRaw) > histRawMax {
		h.histRaw = h.histRaw[len(h.histRaw)-histRawMax:]
	}

	// Tier 2: 1-min aggregates (last 48h)
	if now-h.last1mTs >= hist1mInterval {
		agg := h.aggregateSamples(h.histRaw, aggWindowStart(h.last1mTs, now, hist1mInterval), now, hist1mInterval)
		if agg != nil {
			h.hist1m = append(h.hist1m, *agg)
			if len(h.hist1m) > hist1mMax {
				h.hist1m = h.hist1m[len(h.hist1m)-hist1mMax:]
			}
			h.last1mTs = now
		}
	}

	// Tier 3: 5-min aggregates (last 7 days)
	if now-h.last5mTs >= hist5mInterval {
		agg := h.aggregateSamples(h.hist1m, aggWindowStart(h.last5mTs, now, hist5mInterval), now, hist5mInterval)
		if agg != nil {
			h.hist5m = append(h.hist5m, *agg)
			if len(h.hist5m) > hist5mMax {
				h.hist5m = h.hist5m[len(h.hist5m)-hist5mMax:]
			}
			h.last5mTs = now
		}
	}

	latest := sample
	latest.ProcessNames = r.Metrics.ProcessNames // Latest alone carries the process list
	h.Latest = &latest
	if len(r.Custom) > 0 {
		h.Custom = r.Custom
	}

	for _, e := range r.Events {
		if e.Timestamp == 0 {
			e.Timestamp = now
		}
		// Noise suppression: an agent running a misconfigured probe (e.g. the old
		// example_service_check hitting 127.0.0.1:8529) would otherwise flood the
		// log every cycle. Record an identical event at most once per cooldown.
		key := h.ID + "|" + e.Source + "|" + e.Level + "|" + e.Message
		if last, ok := s.lastEvent[key]; ok && now-last < eventCooldownSec {
			continue
		}
		if len(s.lastEvent) > 2000 { // bound the dedup map (values-with-numbers make unique keys)
			for k, v := range s.lastEvent {
				if now-v >= eventCooldownSec {
					delete(s.lastEvent, k)
				}
			}
		}
		s.lastEvent[key] = now
		se := storedEvent{Event: e, HostID: h.ID, Hostname: h.Hostname}
		s.events = append(s.events, se)
		if s.pg != nil {
			go s.pg.appendEvent(se)
		}
		s.appendLog(LogEntry{Timestamp: e.Timestamp, Kind: KindPlugin, Level: e.Level, Actor: e.Source, Host: h.Hostname, Message: e.Message})
	}
	if len(s.events) > maxEvents {
		s.events = s.events[len(s.events)-maxEvents:]
	}
	s.dirty = true
	// Return a shallow snapshot — never the live map pointer — so callers (e.g.
	// async auto-update) cannot race with subsequent UpsertAuthenticated writers.
	return hostMeta(h), true
}

// hostMeta returns a shallow copy suitable for list APIs: the Latest sample is
// copied with its (potentially huge) process list stripped, so /hosts stays
// lean. Process names are served on demand via GetProcessNames.
func hostMeta(h *Host) *Host {
	cp := *h
	if h.Latest != nil {
		l := *h.Latest
		l.ProcessNames = nil
		cp.Latest = &l
	}
	return &cp
}

// aggWindowStart 把降采样窗口的起点钳制在「最多回看一个 interval」。
//
// 原来直接用 last1mTs / last5mTs 当起点，主机中断后重新上报时就出问题：
// 假设一台机器掉线 4 小时再回来，last1mTs 停在 4 小时前，窗口就成了 [4h前, 现在)，
// aggregateSamples 会把**整个 raw 环**（含中断前的全部样本）平均成一个点，
// 再打上"现在"的时间戳塞进 1m 层。5m 层同理，还会把这个已经错的点再平均一遍。
// 结果是内存分层里凭空出现一个跨越几小时的伪样本——VM 读失败回退到内存时，
// 曲线上就是一个来路不明的平台，这正是"数据错乱"的一种。
//
// 首次聚合（last=0）同理：不钳制的话会把启动以来的所有样本压成一个点。
func aggWindowStart(last, now, interval int64) int64 {
	if lo := now - interval; last < lo {
		return lo
	}
	return last
}

// aggregateSamples aggregates samples within [from, to] into a single sample.
// It computes the average of numeric metrics and takes the last value for counters.
func (h *Host) aggregateSamples(samples []shared.Sample, from, to, interval int64) *shared.Sample {
	if len(samples) == 0 {
		return nil
	}

	// Find samples in the aggregation window
	var window []shared.Sample
	for _, s := range samples {
		if s.Timestamp >= from && s.Timestamp < to {
			window = append(window, s)
		}
	}
	if len(window) == 0 {
		return nil
	}

	// Compute averages
	var agg shared.Sample
	agg.Timestamp = to
	n := float64(len(window))

	// CPU
	var cpuSum float64
	for _, s := range window {
		cpuSum += s.CPUPercent
	}
	agg.CPUPercent = cpuSum / n
	agg.CPUCores = window[len(window)-1].CPUCores // take last

	// Memory
	var memUsedSum, memTotalSum float64
	for _, s := range window {
		memUsedSum += float64(s.MemUsed)
		memTotalSum += float64(s.MemTotal)
	}
	avgMemUsed := uint64(memUsedSum / n)
	avgMemTotal := uint64(memTotalSum / n)
	agg.MemUsed = avgMemUsed
	agg.MemTotal = avgMemTotal
	if avgMemTotal > 0 {
		agg.MemPercent = float64(avgMemUsed) / float64(avgMemTotal) * 100
	}

	// Swap
	var swapUsedSum, swapTotalSum float64
	for _, s := range window {
		swapUsedSum += float64(s.SwapUsed)
		swapTotalSum += float64(s.SwapTotal)
	}
	avgSwapUsed := uint64(swapUsedSum / n)
	avgSwapTotal := uint64(swapTotalSum / n)
	agg.SwapUsed = avgSwapUsed
	agg.SwapTotal = avgSwapTotal
	if avgSwapTotal > 0 {
		agg.SwapPercent = float64(avgSwapUsed) / float64(avgSwapTotal) * 100
	}

	// Disk (root filesystem)
	var diskUsedSum, diskTotalSum float64
	for _, s := range window {
		diskUsedSum += float64(s.DiskUsed)
		diskTotalSum += float64(s.DiskTotal)
	}
	avgDiskUsed := uint64(diskUsedSum / n)
	avgDiskTotal := uint64(diskTotalSum / n)
	agg.DiskUsed = avgDiskUsed
	agg.DiskTotal = avgDiskTotal
	if avgDiskTotal > 0 {
		agg.DiskPercent = float64(avgDiskUsed) / float64(avgDiskTotal) * 100
	}

	// Per-disk info: aggregate each mount point
	if len(window) > 0 && len(window[0].Disks) > 0 {
		diskMap := make(map[string][]shared.DiskInfo)
		for _, s := range window {
			for _, d := range s.Disks {
				diskMap[d.Path] = append(diskMap[d.Path], d)
			}
		}
		for path, infos := range diskMap {
			var totalSum, usedSum float64
			for _, d := range infos {
				totalSum += float64(d.Total)
				usedSum += float64(d.Used)
			}
			avgTotal := uint64(totalSum / float64(len(infos)))
			avgUsed := uint64(usedSum / float64(len(infos)))
			percent := 0.0
			if avgTotal > 0 {
				percent = float64(avgUsed) / float64(avgTotal) * 100
			}
			agg.Disks = append(agg.Disks, shared.DiskInfo{
				Path:    path,
				Total:   avgTotal,
				Used:    avgUsed,
				Percent: percent,
			})
		}
	}

	// Per-GPU info: aggregate each GPU by name (average util / VRAM)
	if len(window) > 0 && len(window[0].GPUs) > 0 {
		type gacc struct {
			util, memUsed, memFree, memTotal, temp, n float64
		}
		order := []string{}
		gmap := map[string]*gacc{}
		for _, s := range window {
			for _, g := range s.GPUs {
				a := gmap[g.Name]
				if a == nil {
					a = &gacc{}
					gmap[g.Name] = a
					order = append(order, g.Name)
				}
				a.util += g.UtilPercent
				a.memUsed += float64(g.MemUsed)
				a.memFree += float64(g.MemFree)
				a.memTotal += float64(g.MemTotal)
				a.temp += g.Temp
				a.n++
			}
		}
		for _, name := range order {
			a := gmap[name]
			if a.n == 0 {
				continue
			}
			gi := shared.GPUInfo{
				Name:        name,
				UtilPercent: a.util / a.n,
				MemUsed:     uint64(a.memUsed / a.n),
				MemFree:     uint64(a.memFree / a.n),
				MemTotal:    uint64(a.memTotal / a.n),
				Temp:        a.temp / a.n,
			}
			if gi.MemTotal > 0 {
				gi.MemPercent = float64(gi.MemUsed) / float64(gi.MemTotal) * 100
			}
			agg.GPUs = append(agg.GPUs, gi)
		}
	}

	// Network rates (average)
	var netSentSum, netRecvSum float64
	for _, s := range window {
		netSentSum += s.NetSentRate
		netRecvSum += s.NetRecvRate
	}
	agg.NetSentRate = netSentSum / n
	agg.NetRecvRate = netRecvSum / n

	// Connections (average)
	var connsSum float64
	for _, s := range window {
		connsSum += float64(s.NetConns)
	}
	agg.NetConns = int(connsSum / n)

	// Per-(proto,state) connection counts: average each series by key so the
	// connection-count / session-state trend survives 1m/5m downsampling.
	if len(window) > 0 && len(window[0].Conns) > 0 {
		type ckey struct{ proto, state string }
		type cacc struct{ sum, cnt float64 }
		corder := []ckey{}
		cmap := map[ckey]*cacc{}
		for _, s := range window {
			for _, c := range s.Conns {
				k := ckey{c.Proto, c.State}
				a := cmap[k]
				if a == nil {
					a = &cacc{}
					cmap[k] = a
					corder = append(corder, k)
				}
				a.sum += float64(c.Count)
				a.cnt++
			}
		}
		for _, k := range corder {
			a := cmap[k]
			if a.cnt == 0 {
				continue
			}
			agg.Conns = append(agg.Conns, shared.ConnStat{Proto: k.proto, State: k.state, Count: int(a.sum / a.cnt)})
		}
	}

	// Load averages
	var l1Sum, l5Sum, l15Sum float64
	for _, s := range window {
		l1Sum += s.Load1
		l5Sum += s.Load5
		l15Sum += s.Load15
	}
	agg.Load1 = l1Sum / n
	agg.Load5 = l5Sum / n
	agg.Load15 = l15Sum / n

	// Process count (average)
	var procSum float64
	for _, s := range window {
		procSum += float64(s.ProcCount)
	}
	agg.ProcCount = int(procSum / n)

	// Uptime (take max, as it only increases)
	var maxUptime uint64
	for _, s := range window {
		if s.Uptime > maxUptime {
			maxUptime = s.Uptime
		}
	}
	agg.Uptime = maxUptime

	return &agg
}

// ListHosts returns metadata + latest sample + custom gauges for every host.
func (s *Store) ListHosts() []*Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, hostMeta(h))
	}
	return out
}

// GetSamples returns the tail of the raw history for one host (legacy
// /metrics endpoint; the /history endpoint serves the tiered archive).
func (s *Store) GetSamples(id string) ([]shared.Sample, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[id]
	if !ok {
		return nil, false
	}
	src := h.histRaw
	if len(src) > maxSamples {
		src = src[len(src)-maxSamples:]
	}
	cp := make([]shared.Sample, len(src))
	copy(cp, src)
	return cp, true
}

// GetProcessNames returns the latest reported process list for one host.
func (s *Store) GetProcessNames(id string) ([]string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[id]
	if !ok || h.Latest == nil {
		return nil, false
	}
	return h.Latest.ProcessNames, true
}

// GetHistory returns time-series data for a host within [from, to] range.
// Preferred tier by span:
// - < 2h: raw samples (~3–5s)
// - < 48h: 1-min aggregates
// - >= 48h: 5-min aggregates
// If the preferred tier is empty in-window (common for new hosts / thin
// aggregation), fall through to denser/coarser tiers so switching 1h→3h/6h
// does not suddenly return an empty chart.
func (s *Store) GetHistory(id string, from, to int64) ([]shared.Sample, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[id]
	if !ok {
		return nil, false
	}

	span := to - from
	var tiers [][]shared.Sample
	switch {
	case span < 7200:
		tiers = [][]shared.Sample{h.histRaw, h.hist1m, h.hist5m}
	case span < 172800:
		tiers = [][]shared.Sample{h.hist1m, h.histRaw, h.hist5m}
	default:
		tiers = [][]shared.Sample{h.hist5m, h.hist1m, h.histRaw}
	}

	filter := func(src []shared.Sample) []shared.Sample {
		result := make([]shared.Sample, 0, len(src))
		for _, sample := range src {
			if sample.Timestamp >= from && sample.Timestamp <= to {
				result = append(result, sample)
			}
		}
		return result
	}
	for _, src := range tiers {
		if result := filter(src); len(result) > 0 {
			return result, true
		}
	}
	return []shared.Sample{}, true
}

// RecentEvents returns the most recent plugin events, newest first.
func (s *Store) RecentEvents() []storedEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.events)
	if n > eventsPerAPI {
		n = eventsPerAPI
	}
	out := make([]storedEvent, 0, n)
	for i := len(s.events) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, s.events[i])
	}
	return out
}

// DeleteHost removes a host and its events, and briefly suppresses re-adding it
// (so a still-running agent doesn't immediately resurrect a just-cleaned entry).
// Returns false if the host was not present.
func (s *Store) DeleteHost(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[id]; !ok {
		return false
	}
	delete(s.hosts, id)
	s.deleted[id] = time.Now().Unix()
	kept := s.events[:0]
	for _, e := range s.events {
		if e.HostID != id {
			kept = append(kept, e)
		}
	}
	s.events = kept
	s.dirty = true
	return true
}

// appendLog adds an activity-log entry; the caller must already hold s.mu.
func (s *Store) appendLog(e LogEntry) {
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().Unix()
	}
	// Ensure username is an independent audit field when Actor is a login name.
	if e.Username == "" && e.Actor != "" {
		a := strings.TrimSpace(e.Actor)
		if a != "" && a != "scheduler" && a != "system" && a != "ai-chat" && a != "notify" &&
			!strings.Contains(a, ".") && !strings.Contains(a, ":") && !strings.Contains(a, " ") {
			e.Username = a
		}
	}
	s.activity = append(s.activity, e)
	if len(s.activity) > maxActivity {
		s.activity = s.activity[len(s.activity)-maxActivity:]
	}
	s.dirty = true
	if s.pg != nil { // PostgreSQL keeps the full, durable audit trail
		go s.pg.appendAudit(e)
	}
	if s.onAudit != nil {
		go s.onAudit(e)
	}
}

// AddLog records an activity-log entry (locks internally).
func (s *Store) AddLog(e LogEntry) {
	s.mu.Lock()
	s.appendLog(e)
	s.mu.Unlock()
}

// RecentActivity returns activity-log entries, newest first.
func (s *Store) RecentActivity() []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LogEntry, 0, len(s.activity))
	for i := len(s.activity) - 1; i >= 0; i-- {
		out = append(out, s.activity[i])
	}
	return out
}

// ---------- 告警状态管理 (确认 / 静默) ----------

// SetAlertState sets the state for an alert key ("acknowledged" or "silenced").
func (s *Store) SetAlertState(key, state string) {
	s.mu.Lock()
	s.alertStates[key] = state
	s.dirty = true
	s.mu.Unlock()
}

// ClearAlertState removes the state for an alert key (e.g. un-ack / un-silence).
func (s *Store) ClearAlertState(key string) {
	s.mu.Lock()
	delete(s.alertStates, key)
	s.dirty = true
	s.mu.Unlock()
}

// GetAlertState returns the state of an alert key, or "" if untouched.
func (s *Store) GetAlertState(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.alertStates[key]
}

// AlertStates returns a snapshot copy of all alert states.
func (s *Store) AlertStates() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make(map[string]string, len(s.alertStates))
	for k, v := range s.alertStates {
		cp[k] = v
	}
	return cp
}

// ---------- 告警历史持久化记录 ----------

// AddAlertRecord writes a new alert lifecycle record. Returns the assigned ID.
// The caller must NOT already hold s.mu.
func (s *Store) AddAlertRecord(r AlertRecord) int64 {
	s.mu.Lock()
	s.alertSeq++
	r.ID = s.alertSeq
	if r.FiredAt == 0 {
		r.FiredAt = time.Now().Unix()
	}
	r.Status = "firing"
	s.alertHistory = append(s.alertHistory, r)
	if len(s.alertHistory) > maxAlertHistory {
		s.alertHistory = s.alertHistory[len(s.alertHistory)-maxAlertHistory:]
	}
	s.dirty = true
	if s.pg != nil {
		go s.pg.appendAlertRecord(r)
	}
	s.mu.Unlock()
	return r.ID
}

// ResolveAlert marks the most recent firing record for key as resolved.
func (s *Store) ResolveAlert(key string, resolvedAt int64) {
	s.mu.Lock()
	// Walk backwards to find the latest firing record for this key.
	for i := len(s.alertHistory) - 1; i >= 0; i-- {
		if s.alertHistory[i].Key == key && s.alertHistory[i].ResolvedAt == 0 {
			s.alertHistory[i].ResolvedAt = resolvedAt
			s.alertHistory[i].Status = "resolved"
			s.dirty = true
			if s.pg != nil {
				go s.pg.resolveAlertRecord(s.alertHistory[i].ID, resolvedAt)
			}
			break
		}
	}
	s.mu.Unlock()
}

// AlertHistory returns alert history records, newest first.
// If activeOnly is true, only records with ResolvedAt == 0 are returned.
func (s *Store) AlertHistory(limit int, activeOnly bool) []AlertRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = maxAlertHistory
	}
	var out []AlertRecord
	for i := len(s.alertHistory) - 1; i >= 0 && len(out) < limit; i-- {
		if activeOnly && s.alertHistory[i].ResolvedAt != 0 {
			continue
		}
		out = append(out, s.alertHistory[i])
	}
	return out
}

// ImportAlertHistory loads alert records from PG at startup, restoring the in-memory buffer.
func (s *Store) ImportAlertHistory(records []AlertRecord) {
	if len(records) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alertHistory = records
	if len(s.alertHistory) > maxAlertHistory {
		s.alertHistory = s.alertHistory[len(s.alertHistory)-maxAlertHistory:]
	}
	// Restore alertSeq to the max ID seen so records get monotonically increasing IDs.
	for _, r := range records {
		if r.ID > s.alertSeq {
			s.alertSeq = r.ID
		}
	}
}
