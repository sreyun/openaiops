package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// 平台自身故障的统一归口 —— 医生也得给自己看病。
//
// 这套东西的由来：Windows 升级那件事里，平台其实**一直知道**失败原因，它就写在
// 主机本地的助手日志里；平台也一直知道自己在重复失败，它就摆在五条一模一样的记录里。
// 可这些信息哪一条都没有走进「事件 → 诊断 → 处置 → 回验」那条链，于是一个能被
// 一句话定位的问题，拖成了「谁也说不清原因」。
//
// 平台对**别人**的故障有完整的闭环（告警 → 事件 → AI 诊断 → 关联 → 自愈 → 回验），
// 对**自己**的故障却只有一行 slog：panic 只写日志、写库失败只写日志、升级失败只写
// 一条会被覆盖的记录。日志是给已经知道要查什么的人看的；不知道该查什么的时候，日志
// 等于不存在。
//
// 所以这里做的事只有一件：**把平台自身的故障，变成和主机故障完全同一种东西。**
//
//	reportPlatformFault → 按指纹聚合 → 达到条件 raise 成 Incident
//	                                    ↓（既有链路，不需要新写）
//	                       消息中心 / AI 自动诊断 / RAG 相似案例关联 /
//	                       拓扑 RCA / 值班指派 / 结论转动作 / 客观回验
//
// 一旦它是一个 Incident，AI 对话里问「平台最近有什么问题」就能查到它，诊断结论能一键
// 转成工单，处置之后的回验会回流成记忆——这些全是既有能力，不必为「自身故障」再造一套。
//
// 刻意的取舍：
//   - **聚合而不是逐条**。自身故障最典型的形态就是同一件事每分钟发生一次，逐条落库
//     只会把真正的信息淹掉。同指纹合并计数，只有第一次和跨过阈值那次会惊动人。
//   - **不是所有故障都开事件**。warning 级要连续 N 次才开——偶发的一次写库超时不值得
//     叫醒任何人；critical（panic、持久层不可用）第一次就开。
//   - **证据随事件走**。原文与证据挂到事件时间线上，AI 诊断读的就是它。这正是上一轮
//     教训的直接落实：证据必须完整抵达人眼，不能在最后一步被切掉。
// ============================================================================

const (
	// warning 级连续多少次才值得开一个事件。1 次多半是抖动，3 次说明它不会自己好。
	platformFaultIncidentThreshold = 3
	// 聚合表上限。这是可观测数据，不是账本；超了淘汰最久没再出现的那条。
	platformFaultMaxTracked = 300
	// 同一指纹两次上报间隔超过这个值就当作「新的一串」，计数重来。
	// 否则一条每天出现一次的偶发故障，攒上一周也会假装成「连续失败」。
	platformFaultStreakGapSec = 30 * 60
)

// PlatformFault 是平台自身的一类故障（已按指纹聚合）。
type PlatformFault struct {
	Fingerprint string `json:"fingerprint"`
	// Component 是出问题的子系统：agent_update / pg / vm / loop / notify / ai / scan…
	Component string `json:"component"`
	// Kind 是这一类故障的短代码，同一 Component 下可细分（如 loop/panic）。
	Kind     string `json:"kind"`
	Level    string `json:"level"` // warning | critical
	HostID   string `json:"host_id,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// Message 是给人读的原文；Evidence 是日志尾巴/堆栈这类附加证据。
	Message  string `json:"message"`
	Evidence string `json:"evidence,omitempty"`
	Count    int    `json:"count"`
	FirstAt  int64  `json:"first_at"`
	LastAt   int64  `json:"last_at"`
	// IncidentID 非 0 表示这条故障已经进了 SRE 闭环。
	IncidentID int64 `json:"incident_id,omitempty"`
}

type platformFaultManager struct {
	mu     sync.Mutex
	faults map[string]*PlatformFault
}

func newPlatformFaultManager() *platformFaultManager {
	return &platformFaultManager{faults: map[string]*PlatformFault{}}
}

// platformFaultKey 决定「这两次是不是同一个问题」。
//
// 带上 hostID 是有意的：同一个原因发生在两台机器上是两件事（一台机器的问题 vs 一批
// 机器的问题），合并了就看不出规模。复用 agentUpdateFailFingerprint 抹掉时间戳、pid、
// 哈希与数字——那正是「每次都不一样但不影响是不是同一个问题」的部分。
func platformFaultKey(component, kind, hostID, msg string) string {
	return strings.Join([]string{component, kind, hostID, agentUpdateFailFingerprint(msg)}, "|")
}

// record 聚合一次上报，返回聚合后的快照与「是否该开事件了」。
func (m *platformFaultManager) record(f PlatformFault) (PlatformFault, bool) {
	now := time.Now().Unix()
	key := platformFaultKey(f.Component, f.Kind, f.HostID, f.Message)
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.faults[key]
	if !ok || now-cur.LastAt > platformFaultStreakGapSec {
		// 隔太久再出现就是新的一串：一条每天一次的偶发故障不该攒成「连续失败」。
		cur = &PlatformFault{Fingerprint: key, FirstAt: now}
		m.faults[key] = cur
	}
	cur.Component, cur.Kind, cur.Level = f.Component, f.Kind, f.Level
	cur.HostID, cur.Hostname = f.HostID, f.Hostname
	cur.Message = strings.TrimSpace(f.Message)
	if e := strings.TrimSpace(f.Evidence); e != "" {
		cur.Evidence = e
	}
	cur.Count++
	cur.LastAt = now

	// 该不该开事件：critical 第一次就开，warning 攒到阈值才开；已经开过的不再重复开。
	shouldRaise := cur.IncidentID == 0 &&
		(cur.Level == "critical" || cur.Count >= platformFaultIncidentThreshold)

	if len(m.faults) > platformFaultMaxTracked {
		oldest, oldestTS := "", int64(1<<63-1)
		for k, v := range m.faults {
			if v.LastAt < oldestTS {
				oldestTS, oldest = v.LastAt, k
			}
		}
		if oldest != "" && oldest != key {
			delete(m.faults, oldest)
		}
	}
	return *cur, shouldRaise
}

// bindIncident 记下这条故障已经进了哪个事件，避免每次上报都再开一个。
func (m *platformFaultManager) bindIncident(fingerprint string, id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.faults[fingerprint]; ok {
		f.IncidentID = id
	}
}

// snapshot 返回按最近发生排序的聚合视图。
func (m *platformFaultManager) snapshot(limit int) []PlatformFault {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	out := make([]PlatformFault, 0, len(m.faults))
	for _, f := range m.faults {
		out = append(out, *f)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt > out[j].LastAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// reportPlatformFault 是全平台自身故障的**唯一入口**。
//
// 调用方只管说「我这儿出问题了」，聚合、判定、开事件、挂证据都在这里。这一点很重要：
// 如果每个子系统各自决定「要不要惊动人」，最后的结果一定是有的太吵、有的永远沉默——
// 而 Windows 升级那件事恰恰是后者。
//
// 绝不阻塞调用方，也绝不因为自身出错影响业务路径。
func (s *Server) reportPlatformFault(component, kind, level, hostID, message, evidence string) {
	if s == nil || s.faults == nil || strings.TrimSpace(message) == "" {
		return
	}
	if level != "critical" {
		level = "warning"
	}
	hostname := ""
	if hostID != "" && s.store != nil {
		if h, ok := s.store.GetHost(hostID); ok && h != nil {
			hostname = h.Hostname
		}
	}
	f, shouldRaise := s.faults.record(PlatformFault{
		Component: component, Kind: kind, Level: level,
		HostID: hostID, Hostname: hostname,
		Message: message, Evidence: evidence,
	})
	// 活动记录一律落一条：它是「这件事发生过」的最低成本证据，且不打扰任何人。
	if s.store != nil {
		s.store.AddLog(LogEntry{
			Kind: KindSystem, Level: level, Actor: Tz("platform.self_fault"),
			Host: hostname, Message: platformFaultLogLine(f),
		})
	}
	if !shouldRaise || s.incidents == nil {
		return
	}
	safeGo("platform-fault-incident", func() { s.raisePlatformFaultIncident(f) })
}

func platformFaultLogLine(f PlatformFault) string {
	line := fmt.Sprintf("[%s/%s] %s", f.Component, f.Kind, f.Message)
	if f.Count > 1 {
		line = fmt.Sprintf("[%s/%s]（第 %d 次）%s", f.Component, f.Kind, f.Count, f.Message)
	}
	return trimLine(line, 900)
}

// raisePlatformFaultIncident 把一条自身故障送进 SRE 闭环。
//
// 用的是和主机故障**完全同一个** incidents.raise：因此它自动获得消息中心、AI 自动诊断
// （critical）、RAG 相似案例关联、拓扑 RCA、值班指派、结论转动作与客观回验。这就是把
// 自身故障做成「和别人的故障同一种东西」的全部收益——一行都不用重写。
func (s *Server) raisePlatformFaultIncident(f PlatformFault) {
	title := fmt.Sprintf("平台自身故障：%s/%s", f.Component, f.Kind)
	if f.Hostname != "" {
		title += "（" + f.Hostname + "）"
	}
	key := "platform/" + f.Fingerprint
	id, created := s.incidents.raise(key, title, f.Level, "platform", f.HostID, f.Hostname, "platform_"+f.Component)
	if id == 0 {
		return
	}
	s.faults.bindIncident(f.Fingerprint, id)
	if !created {
		return
	}
	// 证据挂到时间线：AI 诊断读的就是事件时间线，证据不进去等于没采集。
	var b strings.Builder
	fmt.Fprintf(&b, "组件：%s / %s\n级别：%s\n出现次数：%d（首次 %s，最近 %s）\n",
		f.Component, f.Kind, f.Level, f.Count,
		time.Unix(f.FirstAt, 0).Format("2006-01-02 15:04:05"),
		time.Unix(f.LastAt, 0).Format("2006-01-02 15:04:05"))
	if f.HostID != "" {
		fmt.Fprintf(&b, "主机：%s（%s）\n", firstNonEmpty(f.Hostname, f.HostID), f.HostID)
	}
	fmt.Fprintf(&b, "\n【原文】\n%s\n", f.Message)
	if f.Evidence != "" {
		fmt.Fprintf(&b, "\n【证据】\n%s\n", trimLine(f.Evidence, 4000))
	}
	s.incidents.AddEvent(id, "note", "platform", b.String())
	if s.messages != nil {
		s.messages.push("incident", f.Level, title, trimLine(f.Message, 220), "sre", strconv.FormatInt(id, 10))
	}
	// warning 级不会走 onChange 里的 autoDiagnose（那条只对 critical 开）。但自身故障
	// 恰恰是最需要 AI 先看一眼的一类——它往往横跨多个子系统，人不一定知道该从哪查起。
	if f.Level != "critical" {
		if inc, ok := s.incidents.Get(id); ok {
			safeGo("platform-fault-diagnose", func() { s.autoDiagnose(inc) })
		}
	}
}

// handlePlatformFaults 返回聚合后的自身故障。只读，viewer 也能看——「平台自己有没有
// 毛病」不该是需要权限才能知道的事。
func (s *Server) handlePlatformFaults(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= platformFaultMaxTracked {
			limit = n
		}
	}
	list := s.faults.snapshot(limit)
	open := 0
	for _, f := range list {
		if f.IncidentID != 0 {
			open++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"faults":        list,
		"count":         len(list),
		"with_incident": open,
		"threshold":     platformFaultIncidentThreshold,
	})
}

// platformFaultSink 是给**拿不到 *Server** 的组件用的上报口（vmWriter、pgStore、
// 包级后台函数）。把它做成变量而不是到处塞 Server 引用，是因为这些组件本就刻意与
// Server 解耦；为了上报一条故障去反向依赖整个 Server，代价比收益大。
var platformFaultSink func(component, kind, level, hostID, message, evidence string)

// reportFault 是那个上报口的安全包装：没装配时静默、自身出错不外溢。
func reportFault(component, kind, level, hostID, message, evidence string) {
	sink := platformFaultSink
	if sink == nil {
		return
	}
	defer func() { _ = recover() }()
	sink(component, kind, level, hostID, message, evidence)
}

// bindPlatformFaultSinks 把包级的 panic 钩子接到这台 Server 上。
//
// panic 一律按 critical 上报：一条常驻循环反复崩溃，意味着某个监控能力已经静默失效，
// 而「监控自己坏了」是唯一一种不会有人来告诉你的故障。指纹取函数名 + panic 值，堆栈
// 作为证据挂到事件时间线上——AI 诊断读的就是它。
func (s *Server) bindPlatformFaultSinks() {
	platformFaultSink = s.reportPlatformFault
	onPlatformPanic = func(name, kind string, r any, stack string) {
		s.reportPlatformFault("loop", kind, "critical", "",
			fmt.Sprintf("后台%s「%s」发生 panic：%v", map[string]string{
				"loop_panic": "循环", "task_panic": "任务",
			}[kind], name, r),
			stack)
	}
}
