package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Closed-loop auto-remediation — connect the alert engine to playbooks.
//
// When an alert fires the engine matches it against operator-defined rules; a
// matching rule triggers a playbook scoped to the affected host. Guards make it
// safe to run commands automatically: an optional human-approval gate, a
// per-host cooldown, and a per-rule hourly rate limit — so a flapping alert can
// never unleash a storm of remediation runs.
// ============================================================================

// RemediationRule maps a class of alerts to a playbook, with safety guards.
type RemediationRule struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	MatchTypes    []string `json:"match_types,omitempty"`    // alert types (cpu/memory/...); empty = any
	MinLevel      string   `json:"min_level,omitempty"`      // "" any | warning | critical
	MatchCategory string   `json:"match_category,omitempty"` // host category filter; empty = any
	PlaybookID    string   `json:"playbook_id"`
	// DryRun: match + log only, never execute playbook.
	DryRun bool `json:"dry_run,omitempty"`
	// RollbackPlaybookID: on failed auto-run, optionally trigger this playbook.
	RollbackPlaybookID string `json:"rollback_playbook_id,omitempty"`
	// Guards
	RequireApproval bool  `json:"require_approval"` // queue for operator approval instead of auto-running
	CooldownSec     int   `json:"cooldown_sec"`     // min seconds between runs for the same host
	MaxPerHour      int   `json:"max_per_hour"`     // per-rule hourly cap (0 = unlimited)
	CreatedAt       int64 `json:"created_at"`
	UpdatedAt       int64 `json:"updated_at"`
}

// RemediationRun records one (attempted) remediation.
type RemediationRun struct {
	ID           int64  `json:"id"`
	RuleID       string `json:"rule_id"`
	RuleName     string `json:"rule_name"`
	AlertKey     string `json:"alert_key"`
	AlertType    string `json:"alert_type"`
	HostID       string `json:"host_id"`
	Hostname     string `json:"hostname"`
	PlaybookID   string `json:"playbook_id"`
	PlaybookName string `json:"playbook_name"`
	// pending_approval | running | success | failed | dry_run | skipped_cooldown | skipped_ratelimit | rejected | no_playbook | rolling_back
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	ExecutionID        int64  `json:"execution_id,omitempty"`
	IncidentID         int64  `json:"incident_id,omitempty"`
	RollbackPlaybookID string `json:"rollback_playbook_id,omitempty"`
	RollbackExecID     int64  `json:"rollback_execution_id,omitempty"`
	// Verify records whether the ALERT actually cleared after a successful run —
	// "" (not applicable) | pending | cleared | still_firing | unknown.
	// Status 只说明剧本跑完了，Verify 才说明问题解决了。
	Verify     string `json:"verify,omitempty"`
	VerifiedAt int64  `json:"verified_at,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	DecidedAt  int64  `json:"decided_at,omitempty"`
	DecidedBy  string `json:"decided_by,omitempty"`
}

const remediationRunCap = 300

func levelRank(l string) int {
	switch l {
	case "critical":
		return 2
	case "warning":
		return 1
	default:
		return 0
	}
}

// remediationManager evaluates rules and tracks runs. Playbook execution is done
// through a server-provided callback so this file stays free of HTTP/agent code.
// remediationVerifyDelay is how long to wait after a successful playbook before
// asking whether the alert actually cleared.
//
// 下限由告警引擎决定：评估周期 10s，且需连续 alertClearTicks 次消失才判恢复，
// 也就是条件消失后还要 ~20-30s 才从 active 集合里退出。90s 给剧本留出真正生效的
// 时间（重启服务、清理磁盘、扩容），同时仍然足够快，能在运维还在看这条告警时给出结论。
const remediationVerifyDelay = 90 * time.Second

type remediationManager struct {
	mu      sync.Mutex
	cfg     *ConfigStore
	runs    []RemediationRun
	nextID  int64
	lastRun map[string]int64   // ruleID|hostID -> last run unix (cooldown)
	hourly  map[string][]int64 // ruleID -> recent run unix times (rate limit)

	// Server-provided hooks (set during wiring).
	getPlaybook func(id string) (Playbook, bool)
	resolveHost func(id string) *Host
	category    func(hostID string) string
	// trigger runs the playbook on one host asynchronously, invokes onDone(ok)
	// when it finishes, and returns the playbook execution ID immediately.
	trigger    func(pb Playbook, host *Host, operator string, onDone func(ok bool)) int64
	onIncident func(incidentID int64, kind, actor, text string)
	// onNotify surfaces a remediation transition (awaiting approval / success /
	// failure) to the message center so operators are alerted out-of-band.
	onNotify func(level, title, body string, incidentID int64)
	// onPersist writes each run to PG permanently (no ring-buffer loss).
	onPersist func(run RemediationRun)
	// alertActive answers "is that alert still firing?" — the observation half of
	// the loop. Nil disables verification (runs simply carry no Verify value).
	alertActive func(alertKey string) bool
	// onVerify fires once the verification window closes, carrying the run with its
	// final Verify verdict. This is the only **objective** outcome the platform has
	// about a remediation, so it is what feeds the learning loop — see
	// Server.learnFromRemediationVerify.
	onVerify func(run RemediationRun)
	// verifyAfter overrides remediationVerifyDelay in tests.
	verifyAfter time.Duration
}

func newRemediationManager(cfg *ConfigStore) *remediationManager {
	return &remediationManager{
		cfg: cfg, nextID: 1,
		lastRun: map[string]int64{},
		hourly:  map[string][]int64{},
	}
}

// matches reports whether a rule applies to an alert on a host.
func (m *remediationManager) matches(r RemediationRule, a Alert) bool {
	if !r.Enabled || r.PlaybookID == "" {
		return false
	}
	if r.MinLevel != "" && levelRank(a.Level) < levelRank(r.MinLevel) {
		return false
	}
	if len(r.MatchTypes) > 0 {
		hit := false
		for _, t := range r.MatchTypes {
			if t == a.Type {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if r.MatchCategory != "" {
		cat := ""
		if m.category != nil {
			cat = m.category(a.HostID)
		}
		if cat != r.MatchCategory {
			return false
		}
	}
	return true
}

// OnAlert is the notifier hook for a firing alert: run every matching rule
// through its guards, then execute or queue for approval.
func (m *remediationManager) OnAlert(a Alert, incidentID int64) {
	if m.cfg == nil {
		return
	}
	for _, r := range m.cfg.RemediationRules() {
		if !m.matches(r, a) {
			continue
		}
		m.evaluateRule(r, a, incidentID)
	}
}

func (m *remediationManager) evaluateRule(r RemediationRule, a Alert, incidentID int64) {
	now := time.Now().Unix()
	// Freeze window: force approval (never auto-run during freeze).
	freezeReason := ""
	if m.cfg != nil {
		cat := ""
		if m.category != nil {
			cat = m.category(a.HostID)
		}
		if w, ok := m.cfg.activeFreezeWindow(a.HostID, cat, now); ok {
			r.RequireApproval = true
			freezeReason = "变更冻结"
			if name := strings.TrimSpace(w.Name); name != "" {
				freezeReason = "变更冻结：" + name
			}
		}
	}
	pb, okPB := m.getPlaybookSafe(r.PlaybookID)
	if okPB && m.cfg != nil {
		pol := m.cfg.CmdPolicy()
		if err := validatePlaybookCommands(pb.Steps, pol); err != nil {
			m.mu.Lock()
			m.recordLocked(r, a, incidentID, "skipped_policy", err.Error())
			m.mu.Unlock()
			return
		}
		if !r.RequireApproval && playbookNeedsForcedApproval(pb.Steps, pol) {
			r.RequireApproval = true
		}
	}
	m.mu.Lock()
	// Cooldown: same rule + host within the window.
	ck := r.ID + "|" + a.HostID
	if r.CooldownSec > 0 && now-m.lastRun[ck] < int64(r.CooldownSec) {
		m.recordLocked(r, a, incidentID, "skipped_cooldown", Tz("remediation.reason_cooldown", r.CooldownSec))
		m.mu.Unlock()
		return
	}
	// Rate limit: prune to last hour, then check the cap.
	if r.MaxPerHour > 0 {
		cut := now - 3600
		times := m.hourly[r.ID][:0]
		for _, t := range m.hourly[r.ID] {
			if t >= cut {
				times = append(times, t)
			}
		}
		m.hourly[r.ID] = times
		if len(times) >= r.MaxPerHour {
			m.recordLocked(r, a, incidentID, "skipped_ratelimit", Tz("remediation.reason_ratelimit", r.MaxPerHour))
			m.mu.Unlock()
			return
		}
	}
	pbName := pb.Name
	if !okPB {
		m.recordLocked(r, a, incidentID, "no_playbook", Tz("remediation.reason_no_playbook"))
		m.mu.Unlock()
		return
	}
	if r.DryRun {
		run := m.recordLocked(r, a, incidentID, "dry_run", "演练模式：已匹配规则，未执行剧本")
		run.PlaybookName = pbName
		run.RollbackPlaybookID = r.RollbackPlaybookID
		m.setPlaybookNameLocked(run.ID, pbName)
		m.mu.Unlock()
		if m.onNotify != nil {
			m.onNotify("info", "自动修复演练："+r.Name,
				"主机 "+a.Hostname+" 匹配剧本「"+pbName+"」，dry_run 未实际执行。", incidentID)
		}
		return
	}
	if r.RequireApproval {
		// Reserve cooldown/rate-limit even while waiting for approval, otherwise
		// flapping alerts enqueue unbounded pending_approval runs.
		m.lastRun[ck] = now
		m.hourly[r.ID] = append(m.hourly[r.ID], now)
		reason := freezeReason
		run := m.recordLocked(r, a, incidentID, "pending_approval", reason)
		run.PlaybookName = pbName
		m.setPlaybookNameLocked(run.ID, pbName)
		m.mu.Unlock()
		if m.onIncident != nil && incidentID > 0 {
			msg := Tz("remediation.evt_pending", r.Name, pbName)
			if freezeReason != "" {
				msg = freezeReason + " · " + msg
			}
			m.onIncident(incidentID, "remediation", "auto", msg)
		}
		if m.onNotify != nil {
			tip := "修复剧本「" + pbName + "」已排队，等待人工审批，请在 SRE · 自动修复 页处理。"
			if freezeReason != "" {
				tip = freezeReason + "。" + tip
			}
			m.onNotify("warning", "自动修复待审批："+r.Name, tip, incidentID)
		}
		return
	}
	// Auto-run: reserve cooldown/rate-limit slots now to prevent double-fire.
	m.lastRun[ck] = now
	m.hourly[r.ID] = append(m.hourly[r.ID], now)
	run := m.recordLocked(r, a, incidentID, "running", "")
	run.RollbackPlaybookID = r.RollbackPlaybookID
	m.setPlaybookNameLocked(run.ID, pbName)
	runID := run.ID
	rollbackID := r.RollbackPlaybookID
	m.mu.Unlock()
	m.launch(runID, pb, a.HostID, incidentID, r.Name, rollbackID)
}

// launch executes the playbook for a run (outside the lock).
func (m *remediationManager) launch(runID int64, pb Playbook, hostID string, incidentID int64, ruleName, rollbackPlaybookID string) {
	host := (*Host)(nil)
	if m.resolveHost != nil {
		host = m.resolveHost(hostID)
	}
	if host == nil || m.trigger == nil {
		m.finish(runID, false, Tz("remediation.reason_host_gone"), rollbackPlaybookID)
		return
	}
	execID := m.trigger(pb, host, Tz("remediation.actor"), func(ok bool) {
		m.finish(runID, ok, "", rollbackPlaybookID)
	})
	m.mu.Lock()
	if run := m.findRun(runID); run != nil {
		run.ExecutionID = execID
		run.RollbackPlaybookID = rollbackPlaybookID
	}
	m.mu.Unlock()
	if m.onIncident != nil && incidentID > 0 {
		m.onIncident(incidentID, "remediation", "auto",
			Tz("remediation.evt_triggered", ruleName, pb.Name, host.Hostname))
	}
}

// finish updates a run's terminal status once its playbook execution completes.
func (m *remediationManager) finish(runID int64, ok bool, reason, rollbackPlaybookID string) {
	m.mu.Lock()
	run := m.findRun(runID)
	if run == nil {
		m.mu.Unlock()
		return
	}
	if ok {
		run.Status = "success"
	} else {
		run.Status = "failed"
		run.Reason = reason
	}
	cp := *run
	incID := run.IncidentID
	name, hostID, hostname := run.PlaybookName, run.HostID, run.Hostname
	if rollbackPlaybookID == "" {
		rollbackPlaybookID = run.RollbackPlaybookID
	}
	m.mu.Unlock()
	m.persistRun(cp)
	if m.onIncident != nil && incID > 0 {
		key := "remediation.evt_success"
		if !ok {
			key = "remediation.evt_failed"
		}
		m.onIncident(incID, "remediation", "auto", Tz(key, name, hostname))
	}
	if m.onNotify != nil {
		if ok {
			// 措辞刻意从「自动修复成功」改成「已执行」：此刻我们只知道剧本跑完了，
			// 还没有回看告警是否消失。结论由 verifyRun 在验证窗后单独推送。
			m.onNotify("success", "自动修复已执行："+name, "主机 "+hostname+" 已执行修复剧本，正在回看告警是否消除。", incID)
		} else {
			m.onNotify("critical", "自动修复失败："+name, "主机 "+hostname+"："+trimLine(reason, 160), incID)
		}
	}
	if ok {
		m.scheduleVerify(runID)
	}
	if !ok && rollbackPlaybookID != "" && m.getPlaybook != nil && m.trigger != nil {
		if rpb, okPB := m.getPlaybook(rollbackPlaybookID); okPB {
			host := (*Host)(nil)
			if m.resolveHost != nil {
				host = m.resolveHost(hostID)
			}
			if host != nil {
				m.mu.Lock()
				if run := m.findRun(runID); run != nil {
					run.Status = "rolling_back"
				}
				m.mu.Unlock()
				rid := m.trigger(rpb, host, "remediation-rollback", nil)
				m.mu.Lock()
				if run := m.findRun(runID); run != nil {
					run.RollbackExecID = rid
					run.Status = "failed"
					run.Reason = strings.TrimSpace(run.Reason + "；已触发回滚剧本 " + rpb.Name)
					cp2 := *run
					m.mu.Unlock()
					m.persistRun(cp2)
				} else {
					m.mu.Unlock()
				}
				if m.onNotify != nil {
					m.onNotify("warning", "自动修复回滚："+name, "已触发回滚剧本「"+rpb.Name+"」", incID)
				}
			}
		}
	}
}

// scheduleVerify closes the loop: after a successful playbook we wait one
// verification window and then ask whether the alert that triggered it is gone.
//
// 为什么必须有这一步：整套自愈的价值主张是「发现 → 处置 → 确认」，而此前只有前两步。
// 「剧本退出码 0」被直接当成「修复成功」推送出去，于是三件事同时发生：
//  1. 运维收到一条**可能是假的**成功通知，从此不再盯这条告警；
//  2. 每条规则的真实有效性无从回答——「这条自愈到底有没有用」没有任何数据支撑；
//  3. 冷却窗被这次「成功」占满，真正该做的处置反而被推迟。
//
// 现在把结论拆成两条消息：执行完一条，验证完一条。只在**没修好**时升级为告警级别，
// 修好了则安静地标成 cleared——不给运维增加噪声。
func (m *remediationManager) scheduleVerify(runID int64) {
	if m == nil || m.alertActive == nil {
		return
	}
	m.mu.Lock()
	run := m.findRun(runID)
	if run == nil || run.AlertKey == "" || strings.HasPrefix(run.AlertKey, "proposal/") {
		// 人工提案没有对应的告警键，无从回看。
		m.mu.Unlock()
		return
	}
	run.Verify = "pending"
	cp := *run
	delay := m.verifyAfter
	m.mu.Unlock()
	m.persistRun(cp)
	if delay <= 0 {
		delay = remediationVerifyDelay
	}
	go func() {
		time.Sleep(delay)
		m.verifyRun(runID)
	}()
}

// verifyRun records whether the triggering alert survived the remediation.
func (m *remediationManager) verifyRun(runID int64) {
	if m == nil || m.alertActive == nil {
		return
	}
	m.mu.Lock()
	run := m.findRun(runID)
	if run == nil || run.Verify != "pending" {
		m.mu.Unlock()
		return
	}
	key, name, hostname, incID := run.AlertKey, run.PlaybookName, run.Hostname, run.IncidentID
	m.mu.Unlock()

	stillFiring := m.alertActive(key)

	m.mu.Lock()
	run = m.findRun(runID)
	if run == nil || run.Verify != "pending" {
		m.mu.Unlock()
		return
	}
	if stillFiring {
		run.Verify = "still_firing"
	} else {
		run.Verify = "cleared"
	}
	run.VerifiedAt = time.Now().Unix()
	verdict := run.Verify
	cp := *run
	m.mu.Unlock()
	m.persistRun(cp)

	if m.onIncident != nil && incID > 0 {
		key := "remediation.evt_verify_cleared"
		if verdict == "still_firing" {
			key = "remediation.evt_verify_still_firing"
		}
		m.onIncident(incID, "remediation", "auto", Tz(key, name, hostname))
	}
	// 只在没修好时打扰人：修好了本来就是预期结果。
	if verdict == "still_firing" && m.onNotify != nil {
		m.onNotify("critical", "自动修复未生效："+name,
			"主机 "+hostname+" 的告警在修复剧本执行后仍在触发，需要人工介入。", incID)
	}
	// 回验结论回流学习闭环：这是「这条自愈到底有没有用」唯一不靠人点赞的答案。
	if m.onVerify != nil {
		m.onVerify(cp)
	}
}

// ProposeManual 事件级 L4 一键提案：不依赖规则，直接把剧本挂到 pending_approval，
// 人工批准后在该主机执行。RuleID 为空以区分自动规则触发的 runs。
func (m *remediationManager) ProposeManual(pb Playbook, hostID, hostname string, incidentID int64, title, actor string) (RemediationRun, error) {
	if strings.TrimSpace(pb.ID) == "" || len(pb.Steps) == 0 {
		return RemediationRun{}, fmt.Errorf("%s", Tz("remediation.reason_no_playbook"))
	}
	if strings.TrimSpace(hostID) == "" {
		return RemediationRun{}, fmt.Errorf("缺少目标主机")
	}
	if title == "" {
		title = "事件修复提案"
	}
	m.mu.Lock()
	m.nextID++
	run := RemediationRun{
		ID: m.nextID, RuleID: "", RuleName: title,
		AlertKey: fmt.Sprintf("proposal/%d", incidentID), AlertType: "proposal",
		HostID: hostID, Hostname: hostname,
		PlaybookID: pb.ID, PlaybookName: pb.Name,
		Status: "pending_approval", IncidentID: incidentID,
		CreatedAt: time.Now().Unix(), Reason: "proposed_by:" + actor,
	}
	m.runs = append(m.runs, run)
	if len(m.runs) > remediationRunCap {
		m.runs = m.runs[len(m.runs)-remediationRunCap:]
	}
	out := *m.findRun(run.ID)
	m.mu.Unlock()
	m.persistRun(out)
	if m.onIncident != nil && incidentID > 0 {
		m.onIncident(incidentID, "remediation", actor,
			Tz("remediation.evt_pending", title, pb.Name))
	}
	if m.onNotify != nil {
		m.onNotify("warning", "修复提案待审批："+title,
			"剧本「"+pb.Name+"」已挂到事件，等待人工审批后在主机 "+hostname+" 执行。", incidentID)
	}
	return out, nil
}

// Approve runs a pending remediation; Reject discards it.
func (m *remediationManager) Approve(runID int64, actor string) error {
	m.mu.Lock()
	run := m.findRun(runID)
	if run == nil {
		m.mu.Unlock()
		return fmt.Errorf("%s", Tz("remediation.run_not_found"))
	}
	if run.Status != "pending_approval" {
		m.mu.Unlock()
		return fmt.Errorf("%s", Tz("remediation.not_pending"))
	}
	pb, ok := m.getPlaybookSafe(run.PlaybookID)
	if !ok {
		run.Status = "no_playbook"
		cp := *run
		m.mu.Unlock()
		m.persistRun(cp)
		return fmt.Errorf("%s", Tz("remediation.reason_no_playbook"))
	}
	run.Status = "running"
	run.DecidedAt = time.Now().Unix()
	run.DecidedBy = actor
	// 规则触发的 runs 才占冷却/限频槽；一键提案（RuleID 空）不占用
	if run.RuleID != "" {
		m.lastRun[run.RuleID+"|"+run.HostID] = time.Now().Unix()
		m.hourly[run.RuleID] = append(m.hourly[run.RuleID], time.Now().Unix())
	}
	cp := *run
	runID, hostID, incID, ruleName := run.ID, run.HostID, run.IncidentID, run.RuleName
	m.mu.Unlock()
	m.persistRun(cp)
	rb := ""
	m.mu.Lock()
	if run := m.findRun(runID); run != nil {
		rb = run.RollbackPlaybookID
	}
	m.mu.Unlock()
	m.launch(runID, pb, hostID, incID, ruleName, rb)
	return nil
}

func (m *remediationManager) Reject(runID int64, actor string) error {
	m.mu.Lock()
	run := m.findRun(runID)
	if run == nil {
		m.mu.Unlock()
		return fmt.Errorf("%s", Tz("remediation.run_not_found"))
	}
	if run.Status != "pending_approval" {
		m.mu.Unlock()
		return fmt.Errorf("%s", Tz("remediation.not_pending"))
	}
	run.Status = "rejected"
	run.DecidedAt = time.Now().Unix()
	run.DecidedBy = actor
	cp := *run
	incID := run.IncidentID
	ruleName := run.RuleName
	m.mu.Unlock()
	m.persistRun(cp)
	if m.onIncident != nil && incID > 0 {
		m.onIncident(incID, "remediation", actor, Tz("remediation.evt_rejected", ruleName))
	}
	return nil
}

// --- internal helpers (caller holds mu unless noted) ---

func (m *remediationManager) findRun(id int64) *RemediationRun {
	for i := range m.runs {
		if m.runs[i].ID == id {
			return &m.runs[i]
		}
	}
	return nil
}

func (m *remediationManager) getPlaybookSafe(id string) (Playbook, bool) {
	if m.getPlaybook == nil {
		return Playbook{}, false
	}
	return m.getPlaybook(id)
}

func (m *remediationManager) setPlaybookNameLocked(runID int64, name string) {
	if run := m.findRun(runID); run != nil {
		run.PlaybookName = name
	}
}

func (m *remediationManager) persistRun(run RemediationRun) {
	if m.onPersist != nil {
		m.onPersist(run)
	}
}

func (m *remediationManager) recordLocked(r RemediationRule, a Alert, incidentID int64, status, reason string) *RemediationRun {
	m.nextID++
	run := RemediationRun{
		ID: m.nextID, RuleID: r.ID, RuleName: r.Name,
		AlertKey: alertKey(a), AlertType: a.Type,
		HostID: a.HostID, Hostname: a.Hostname,
		PlaybookID: r.PlaybookID, Status: status, Reason: reason,
		IncidentID: incidentID, CreatedAt: time.Now().Unix(),
	}
	m.runs = append(m.runs, run)
	// In-memory ring only; PG table keeps full history via onPersist.
	if len(m.runs) > remediationRunCap {
		m.runs = m.runs[len(m.runs)-remediationRunCap:]
	}
	found := m.findRun(run.ID)
	if found != nil {
		// Persist asynchronously after unlock is preferred; call with copy here is OK
		// because insert is idempotent upsert by id.
		cp := *found
		go m.persistRun(cp)
	}
	return found
}

// Runs returns run history newest-first.
func (m *remediationManager) Runs() []RemediationRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RemediationRun, len(m.runs))
	copy(out, m.runs)
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// PendingCount returns how many runs await approval (for nav badges).
func (m *remediationManager) PendingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i := range m.runs {
		if m.runs[i].Status == "pending_approval" {
			n++
		}
	}
	return n
}

// ExportGuards returns cooldown / rate-limit clocks for PG persistence.
func (m *remediationManager) ExportGuards() (lastRun map[string]int64, hourly map[string][]int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lastRun = make(map[string]int64, len(m.lastRun))
	for k, v := range m.lastRun {
		lastRun[k] = v
	}
	hourly = make(map[string][]int64, len(m.hourly))
	for k, v := range m.hourly {
		cp := make([]int64, len(v))
		copy(cp, v)
		hourly[k] = cp
	}
	return lastRun, hourly
}

// ImportGuards restores cooldown / rate-limit clocks after restart.
func (m *remediationManager) ImportGuards(lastRun map[string]int64, hourly map[string][]int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lastRun != nil {
		m.lastRun = lastRun
	}
	if hourly != nil {
		m.hourly = hourly
	}
}

// Export/Import bridge the manager to PostgreSQL.
func (m *remediationManager) Export() []RemediationRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RemediationRun, len(m.runs))
	copy(out, m.runs)
	return out
}

func (m *remediationManager) Import(list []RemediationRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = make([]RemediationRun, len(list))
	copy(m.runs, list)
	var maxID int64
	for _, r := range m.runs {
		if r.ID > maxID {
			maxID = r.ID
		}
	}
	m.nextID = maxID
}

// validateRemediationRule normalizes and checks a rule before persisting.
func validateRemediationRule(r *RemediationRule) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("%s", Tz("remediation.name_required"))
	}
	if r.PlaybookID == "" {
		return fmt.Errorf("%s", Tz("remediation.playbook_required"))
	}
	if r.MinLevel != "" && r.MinLevel != "warning" && r.MinLevel != "critical" {
		return fmt.Errorf("%s", Tz("remediation.bad_level"))
	}
	if r.CooldownSec < 0 {
		r.CooldownSec = 0
	}
	if r.MaxPerHour < 0 {
		r.MaxPerHour = 0
	}
	return nil
}
