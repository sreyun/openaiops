package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ============================================================================
// 「同一台主机连续以同一个原因升级失败」的熔断与抬升。
//
// 这套东西的起因是一台真实主机。server11 从 v0.19.98 升 v0.19.100，助手日志里连续
// 五次、间隔约 6 分钟、逐字相同：
//
//	downloaded aiops-agent.exe sha=<与服务端 pin 一致>
//	update failed: staging not runnable (exit=): v0.19.100
//
// 那个缺陷本身已经修了（见 shared/agent_update_probe.go）。但**更值得修的是这五次**：
// 平台把同一个原因原封不动地重复了五遍，每一遍都记在一条会被下一遍覆盖的记录里，
// 没有任何一处说过「这台机器一直在以同一个原因失败」。于是现象只能是「Windows 升不
// 上去，谁也说不清原因」——直到有人亲自登上那台机器把日志翻出来。
//
// 重试本身没错：Windows 换版确实有一批偶发失败，下一轮就好了。错的是**无限次**重试
// 一个不会自愈的原因，并且在重试的过程中始终保持沉默。
//
// 所以这里做两件事，一件不做：
//
//   - **计数**：把失败原因归一化成指纹（抹掉时间戳、pid、哈希、版本号里的数字），
//     同指纹连续命中就累加。原因一变，计数归零重来——那是另一个问题，值得重试。
//   - **到点抬头**：连续 N 次同因失败时，发一条告警 + 一条活动记录，把**原文**带上，
//     并让自动升级停在这台主机上（跳过原因写明为什么停）。人看到的不再是「没升上去」，
//     而是「连续 3 次都是这句话」。
//   - **不做永久拉黑**：目标版本变了（发了新版，可能就修好了）、上报版本变了（人工
//     升上去了）、失败原因变了，任何一条成立就自动解除。手工推送本来就不走这道闸门。
//
// 这条经验对整个平台通用：**一个不会自愈的失败被无限重试，等价于把它藏起来。**
// ============================================================================

const (
	// 连续同因失败到几次算「不会自愈」。3 次意味着大约 18 分钟（软重试窗 360s）——
	// 足够放过偶发（Windows 换版抢锁、临时网络），又不至于让人等太久。
	agentUpdateFailStreakLimit = 3
	// 与 skipReasons 同量级：这是可观测数据，不是账本，超了淘汰最老的。
	agentUpdateMaxFailStreaks = 500
)

// agentUpdateFailStreak 是一台主机当前的连续同因失败状态。
type agentUpdateFailStreak struct {
	// Fingerprint 是归一化后的原因，用来判断「还是不是同一个问题」。
	Fingerprint string `json:"fingerprint"`
	// Reason 是**未经归一化的原文**，给人读的。抬头时带的就是它。
	Reason    string `json:"reason"`
	Count     int    `json:"count"`
	FirstAt   int64  `json:"first_at"`
	LastAt    int64  `json:"last_at"`
	FromVer   string `json:"from_version,omitempty"`
	TargetVer string `json:"target_version,omitempty"`
	// Raised 记录「已经抬到人眼前了」，避免每一轮扫描都再发一次告警。
	Raised bool `json:"raised"`
}

var (
	// 归一化要抹掉的是「每次都不一样、但不影响是不是同一个问题」的部分。
	reFailStreakTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?([+-]\d{2}:\d{2}|Z)?`)
	reFailStreakKV   = regexp.MustCompile(`(?i)\b(pid|helper|sha|sha256|task|job|run)=\S+`)
	reFailStreakHex  = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	reFailStreakNum  = regexp.MustCompile(`\d+`)
	reFailStreakWS   = regexp.MustCompile(`\s+`)
)

// agentUpdateFailFingerprint 把一条失败消息压成「这是哪一类失败」。
//
// 版本号里的数字一起抹掉是有意的：v0.19.100 升失败和 v0.19.101 升失败，只要那句话
// 一样，就是同一个问题（而「换了一版之后还失败吗」由 TargetVer 单独把关，不靠指纹）。
func agentUpdateFailFingerprint(msg string) string {
	s := strings.ToLower(strings.TrimSpace(msg))
	s = reFailStreakTime.ReplaceAllString(s, " ")
	s = reFailStreakKV.ReplaceAllString(s, " ")
	s = reFailStreakHex.ReplaceAllString(s, " ")
	s = reFailStreakNum.ReplaceAllString(s, "#")
	s = reFailStreakWS.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 220 {
		s = string(r[:220])
	}
	return s
}

// noteFailure 记一次失败，返回累加后的状态与「是否正好在这一次达到阈值」。
//
// 只有「正好达到」那一次返回 true：抬头是一次性的，此后每 6 分钟再喊一遍只会变成
// 新的噪音——而噪音正是这套机制要消灭的东西。
func (m *agentUpdateManager) noteFailure(hostID, fromVer, targetVer, msg string) (agentUpdateFailStreak, bool) {
	if m == nil || hostID == "" {
		return agentUpdateFailStreak{}, false
	}
	fp := agentUpdateFailFingerprint(msg)
	if fp == "" {
		return agentUpdateFailStreak{}, false
	}
	now := time.Now().Unix()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failStreaks == nil {
		m.failStreaks = map[string]agentUpdateFailStreak{}
	}
	st, ok := m.failStreaks[hostID]
	// 指纹变了或目标版本变了 = 换了一个问题，从头数。
	if !ok || st.Fingerprint != fp || st.TargetVer != targetVer {
		st = agentUpdateFailStreak{Fingerprint: fp, FirstAt: now, TargetVer: targetVer}
	}
	st.Count++
	st.LastAt = now
	st.Reason = strings.TrimSpace(msg)
	if fromVer != "" {
		st.FromVer = fromVer
	}
	justReached := st.Count == agentUpdateFailStreakLimit && !st.Raised
	if justReached {
		st.Raised = true
	}
	m.failStreaks[hostID] = st
	if len(m.failStreaks) > agentUpdateMaxFailStreaks {
		oldest, oldestTS := "", int64(1<<63-1)
		for id, v := range m.failStreaks {
			if v.LastAt < oldestTS {
				oldestTS, oldest = v.LastAt, id
			}
		}
		if oldest != "" && oldest != hostID {
			delete(m.failStreaks, oldest)
		}
	}
	return st, justReached
}

// clearFailStreak 在这台主机升成功、或版本已经追上时调用。
func (m *agentUpdateManager) clearFailStreak(hostID string) {
	if m == nil || hostID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.failStreaks, hostID)
}

// failStreak 读当前状态，供自动升级闸门判断。
func (m *agentUpdateManager) failStreak(hostID string) (agentUpdateFailStreak, bool) {
	if m == nil || hostID == "" {
		return agentUpdateFailStreak{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.failStreaks[hostID]
	return st, ok
}

type agentUpdateFailStreakView struct {
	agentUpdateFailStreak
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname"`
}

func (m *agentUpdateManager) failStreakSnapshot(s *Server) []agentUpdateFailStreakView {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	cp := make(map[string]agentUpdateFailStreak, len(m.failStreaks))
	for k, v := range m.failStreaks {
		cp[k] = v
	}
	m.mu.Unlock()
	out := make([]agentUpdateFailStreakView, 0, len(cp))
	for id, st := range cp {
		v := agentUpdateFailStreakView{agentUpdateFailStreak: st, HostID: id}
		if s != nil && s.store != nil {
			if h, ok := s.store.GetHost(id); ok && h != nil {
				v.Hostname = h.Hostname
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt > out[j].LastAt })
	return out
}

// noteAgentUpdateFailure 是三条失败路径（下发失败 / 重试耗尽 / 校验超时）共用的入口。
//
// 抬头用的是**告警 + 活动记录**，而不是再加一张只有翻到才看得见的表：这条信息的全部
// 价值就在于它会主动找人。消息里带失败原文——那正是此前唯一到不了人眼前的东西。
func (s *Server) noteAgentUpdateFailure(hostID, fromVer, msg string) {
	if s == nil || s.agentUpdates == nil || strings.TrimSpace(msg) == "" {
		return
	}
	st, justReached := s.agentUpdates.noteFailure(hostID, fromVer, appVersion, msg)
	if !justReached {
		return
	}
	hostLabel := hostID
	if s.store != nil {
		if h, ok := s.store.GetHost(hostID); ok && h != nil && h.Hostname != "" {
			hostLabel = h.Hostname
		}
	}
	reason := st.Reason
	if r := []rune(reason); len(r) > 600 {
		reason = string(r[:600]) + "…"
	}
	text := fmt.Sprintf(
		"主机「%s」连续 %d 次以同一个原因升级失败（目标版本 %s），自动升级已暂停，不再无谓重试。"+
			"原因原文：%s", hostLabel, st.Count, st.TargetVer, reason)
	if s.store != nil {
		s.store.AddLog(LogEntry{
			Kind: KindSystem, Level: "warning", Actor: Tz("agent_update.actor"),
			Host: hostLabel, Message: text,
		})
	}
	if s.notifier != nil {
		s.notifier.enqueuePush(s.cfg.Get(), Alert{
			HostID:    hostID,
			Hostname:  hostLabel,
			Level:     "warning",
			Type:      "agent_update",
			Scope:     "fail_streak",
			Message:   text,
			Timestamp: time.Now().Unix(),
		}, true)
	}
	// 同时进平台自身故障归口：升不上去是**平台自己**没做成的事，不是被监控对象的毛病。
	// 进去之后它会开事件、被 AI 诊断、可一键转工单并在处置后回验——这套机制的起因就是
	// 它，见 self_fault.go 顶部。
	s.reportPlatformFault("agent_update", "fail_streak", "warning", hostID, text, st.Reason)
}

// agentUpdateFailStreakGate 是自动升级闸门里的那一问：这台主机是不是已经在同一个坑里
// 反复摔了？是的话就停下来，并把「为什么停」写成人能直接照着处理的一段话。
//
// 只挡自动路径。人工推送不受影响——那是人在明确承担后果，而且往往正是为了验证修没修好。
func (s *Server) agentUpdateFailStreakGate(hostID string) (blocked bool, detail string) {
	if s == nil || s.agentUpdates == nil {
		return false, ""
	}
	st, ok := s.agentUpdates.failStreak(hostID)
	if !ok || st.Count < agentUpdateFailStreakLimit {
		return false, ""
	}
	// 目标版本变了说明发了新版，值得再试一次——熔断不能跨版本粘住。
	if st.TargetVer != appVersion {
		s.agentUpdates.clearFailStreak(hostID)
		return false, ""
	}
	reason := st.Reason
	if r := []rune(reason); len(r) > 400 {
		reason = string(r[:400]) + "…"
	}
	return true, fmt.Sprintf(
		"已连续 %d 次以同一个原因失败（首次 %s），自动升级已暂停以免无谓重复；"+
			"原因原文：%s。修好之后手工推送一次即可恢复自动升级（也可等下一个版本自动解除）",
		st.Count, time.Unix(st.FirstAt, 0).Format("2006-01-02 15:04:05"), reason)
}
