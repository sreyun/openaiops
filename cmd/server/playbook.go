package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Playbook is an operator-defined automation: a sequence of shell commands run
// on a set of target hosts via the Agent reverse-terminal channel.
type Playbook struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Steps       []PlaybookStep    `json:"steps"`
	Strategy    PlaybookStrategy  `json:"strategy,omitempty"`
	Schedule    *PlaybookSchedule `json:"schedule,omitempty"` // optional timed trigger
	CreatedAt   int64             `json:"created_at"`
	UpdatedAt   int64             `json:"updated_at"`
}

// PlaybookStrategy contains fleet-wide execution controls. MaxParallel is
// bounded server-side to avoid a thundering herd; AutoRollback runs explicit
// successful-step rollback commands in reverse order after a terminal failure.
type PlaybookStrategy struct {
	MaxParallel  int  `json:"max_parallel,omitempty"` // default 30, max 100
	AutoRollback bool `json:"auto_rollback,omitempty"`
}

// PlaybookSchedule defines an optional timed trigger for a playbook. A minimal,
// dependency-free model covering the common cases (no full cron parser):
//   - kind="interval": run every IntervalMin minutes
//   - kind="daily":    run every day at At ("HH:MM", server local time)
//   - kind="weekly":   run every week on Weekday (0=Sun..6=Sat) at At
type PlaybookSchedule struct {
	Enabled     bool   `json:"enabled"`
	Kind        string `json:"kind"`                   // interval | daily | weekly
	IntervalMin int    `json:"interval_min,omitempty"` // kind=interval
	At          string `json:"at,omitempty"`           // "HH:MM" for daily/weekly
	Weekday     int    `json:"weekday,omitempty"`      // 0=Sun..6=Sat for weekly
}

// PlaybookStep is one command in a playbook. Target selectors:
// "all" = every online host; "folder:ID" = hosts in folder (incl. subtree);
// "category:xxx" = hosts in category xxx; "system:os" = OS/distro match;
// "host:ID" = a single host by ID.
type PlaybookStep struct {
	Name          string `json:"name"`
	Command       string `json:"command"`
	CommandWin    string `json:"command_win,omitempty"` // Windows 覆盖命令（留空=用 Command）
	CommandMac    string `json:"command_mac,omitempty"` // macOS 覆盖命令（留空=用 Command）
	Target        string `json:"target"`                // "all" | "folder:ID" | "host:ID" | multi comma-joined
	TimeoutSec    int    `json:"timeout_sec"`
	ContinueErr   bool   `json:"continue_on_error"`
	IgnoreExit    bool   `json:"ignore_exit,omitempty"`     // 非零退出码也算成功（grep/diff 等过滤命令）
	Register      string `json:"register,omitempty"`        // 把本步输出存入该变量名，供后续步骤 {{名}} 引用
	When          string `json:"when,omitempty"`            // 条件：求值为空/false/0/no 则跳过本步
	MaxAttempts   int    `json:"max_attempts,omitempty"`    // 基础设施失败最大尝试次数（默认3，最大6）
	RetryDelaySec int    `json:"retry_delay_sec,omitempty"` // 线性退避基数（默认2秒，最大60）
	RetryOnExit   bool   `json:"retry_on_exit,omitempty"`   // 显式允许重试命令非零退出（仅幂等步骤）
	Rollback      string `json:"rollback,omitempty"`        // 本步成功后，后续失败时的 Linux/默认回滚命令
	RollbackWin   string `json:"rollback_win,omitempty"`    // Windows 回滚覆盖
	RollbackMac   string `json:"rollback_mac,omitempty"`    // macOS 回滚覆盖
	// 内置模块（非空则走模块、忽略上面的 Command）：
	// 只读：gather_facts / host_inspect / disk_usage / mem_info / net_* / docker_* / ...
	// 变更：service / package / copy
	Module string            `json:"module,omitempty"`
	Args   map[string]string `json:"args,omitempty"`
}

// PlaybookExecution is one run of a playbook: tracks per-host status + output.
type PlaybookExecution struct {
	ID           int64                     `json:"id"`
	PlaybookID   string                    `json:"playbook_id"`
	PlaybookName string                    `json:"playbook_name"`
	Operator     string                    `json:"operator"`
	StartTime    int64                     `json:"start_time"`
	EndTime      int64                     `json:"end_time,omitempty"`
	Status       string                    `json:"status"` // pending_approval | running | completed | failed | partial | cancelled | rejected
	HostResults  map[string]HostExecResult `json:"host_results"`
	Trigger      string                    `json:"trigger,omitempty"` // manual | schedule
	RiskNote     string                    `json:"risk_note,omitempty"`
}

// HostExecResult tracks one host's execution outcome.
type HostExecResult struct {
	Hostname string       `json:"hostname"`
	Status   string       `json:"status"`           // pending | running | success | failed | timeout | skipped | cancelled
	Reason   string       `json:"reason,omitempty"` // no_pickup | timeout | exit | skipped_when | cancelled | error
	Output   string       `json:"output"`
	Steps    []StepResult `json:"steps"`
}

// StepResult is one step's outcome on one host.
type StepResult struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Output   string `json:"output"`
	Duration int64  `json:"duration_ms"`
	Attempts int    `json:"attempts,omitempty"`
	Rollback bool   `json:"rollback,omitempty"`
}

// playbookManager stores playbooks and execution history in memory + config.
type playbookManager struct {
	mu         sync.Mutex
	cfg        *ConfigStore
	executions []PlaybookExecution
	nextExecID int64
	// --- scheduler bookkeeping (in-memory; resets on restart) ---
	lastCheck time.Time            // last scheduler tick, for daily/weekly windowing
	lastRun   map[string]time.Time // playbook ID -> last scheduled fire (interval baseline + dedup)
	schedBusy map[string]bool      // playbook ID -> a scheduled run is currently in flight
}

func newPlaybookManager(cfg *ConfigStore) *playbookManager {
	return &playbookManager{
		cfg: cfg, nextExecID: 1,
		lastRun:   map[string]time.Time{},
		schedBusy: map[string]bool{},
	}
}

// parseHHMM parses "HH:MM" (24h) into minutes-of-day; ok=false if malformed.
func parseHHMM(s string) (int, bool) {
	var h, m int
	if n, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &m); err != nil || n != 2 {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// scheduledInstant returns today's date at the given "HH:MM" in now's location.
func scheduledInstant(now time.Time, hhmm string) (time.Time, bool) {
	mins, ok := parseHHMM(hhmm)
	if !ok {
		return time.Time{}, false
	}
	return time.Date(now.Year(), now.Month(), now.Day(), mins/60, mins%60, 0, 0, now.Location()), true
}

// sanitizeSchedule validates a schedule in place. A disabled schedule is accepted
// as-is; an enabled one must be well-formed for its kind.
func sanitizeSchedule(sc *PlaybookSchedule) error {
	if sc == nil || !sc.Enabled {
		return nil
	}
	switch sc.Kind {
	case "interval":
		if sc.IntervalMin < 1 {
			return fmt.Errorf("%s", Tz("playbook.sched_bad_interval"))
		}
	case "daily":
		if _, ok := parseHHMM(sc.At); !ok {
			return fmt.Errorf("%s", Tz("playbook.sched_bad_time"))
		}
	case "weekly":
		if sc.Weekday < 0 || sc.Weekday > 6 {
			return fmt.Errorf("%s", Tz("playbook.sched_bad_weekday"))
		}
		if _, ok := parseHHMM(sc.At); !ok {
			return fmt.Errorf("%s", Tz("playbook.sched_bad_time"))
		}
	default:
		return fmt.Errorf("%s", Tz("playbook.sched_bad_kind"))
	}
	return nil
}

// dueSchedules returns the playbooks whose schedule is due to fire at `now`,
// updating internal bookkeeping so each occurrence fires exactly once. Playbooks
// with a scheduled run already in flight are skipped to avoid pileup. The caller
// must clearSchedBusy(id) when each returned playbook's run finishes.
func (pm *playbookManager) dueSchedules(now time.Time) []Playbook {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	prevCheck := pm.lastCheck
	if prevCheck.IsZero() {
		prevCheck = now // first tick establishes a baseline; never fire retroactively
	}
	var due []Playbook
	for _, pb := range pm.cfg.Playbooks() {
		sc := pb.Schedule
		if sc == nil || !sc.Enabled || pm.schedBusy[pb.ID] {
			continue
		}
		fire := false
		switch sc.Kind {
		case "interval":
			if sc.IntervalMin >= 1 {
				last, seen := pm.lastRun[pb.ID]
				if !seen {
					pm.lastRun[pb.ID] = now // baseline; first fire one interval later
				} else if now.Sub(last) >= time.Duration(sc.IntervalMin)*time.Minute {
					fire = true
				}
			}
		case "daily":
			if inst, ok := scheduledInstant(now, sc.At); ok && inst.After(prevCheck) && !inst.After(now) {
				fire = true
			}
		case "weekly":
			if int(now.Weekday()) == sc.Weekday {
				if inst, ok := scheduledInstant(now, sc.At); ok && inst.After(prevCheck) && !inst.After(now) {
					fire = true
				}
			}
		}
		if fire {
			pm.lastRun[pb.ID] = now
			pm.schedBusy[pb.ID] = true
			due = append(due, pb)
		}
	}
	pm.lastCheck = now
	return due
}

// clearSchedBusy releases the in-flight guard for a playbook's scheduled run.
func (pm *playbookManager) clearSchedBusy(id string) {
	pm.mu.Lock()
	delete(pm.schedBusy, id)
	pm.mu.Unlock()
}

// List returns all playbooks from config.
func (pm *playbookManager) List() []Playbook {
	return pm.cfg.Playbooks()
}

// Get returns a playbook by ID.
func (pm *playbookManager) Get(id string) (Playbook, bool) {
	for _, p := range pm.cfg.Playbooks() {
		if p.ID == id {
			return p, true
		}
	}
	return Playbook{}, false
}

// Upsert creates or updates a playbook.
func (pm *playbookManager) Upsert(p Playbook) (Playbook, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return Playbook{}, fmt.Errorf("%s", Tz("playbook.name_required"))
	}
	for i := range p.Steps {
		p.Steps[i].Name = strings.TrimSpace(p.Steps[i].Name)
		p.Steps[i].Command = strings.TrimSpace(p.Steps[i].Command)
		if p.Steps[i].TimeoutSec < 5 {
			p.Steps[i].TimeoutSec = 30
		}
		if p.Steps[i].MaxAttempts <= 0 {
			p.Steps[i].MaxAttempts = 3
		}
		if p.Steps[i].MaxAttempts > 6 {
			return Playbook{}, fmt.Errorf("步骤 %s: max_attempts 最大为 6", p.Steps[i].Name)
		}
		if p.Steps[i].RetryDelaySec <= 0 {
			p.Steps[i].RetryDelaySec = 2
		}
		if p.Steps[i].RetryDelaySec > 60 {
			return Playbook{}, fmt.Errorf("步骤 %s: retry_delay_sec 最大为 60", p.Steps[i].Name)
		}
		if !validPlaybookTarget(p.Steps[i].Target) {
			return Playbook{}, fmt.Errorf("步骤 %s: 无效目标选择器 %q", p.Steps[i].Name, p.Steps[i].Target)
		}
	}
	if len(p.Steps) == 0 {
		return Playbook{}, fmt.Errorf("%s", Tz("playbook.step_required"))
	}
	if err := validatePlaybookCommands(p.Steps, pm.cfg.CmdPolicy()); err != nil {
		return Playbook{}, err
	}
	if err := validatePlaybookVariables(p.Steps); err != nil {
		return Playbook{}, err
	}
	if p.Strategy.MaxParallel <= 0 {
		p.Strategy.MaxParallel = 30
	}
	if p.Strategy.MaxParallel > 100 {
		return Playbook{}, fmt.Errorf("strategy.max_parallel 最大为 100")
	}
	if err := sanitizeSchedule(p.Schedule); err != nil {
		return Playbook{}, err
	}
	now := time.Now().Unix()
	p.UpdatedAt = now
	if p.ID == "" {
		p.ID = genToken()[:8]
		p.CreatedAt = now
	}
	return pm.cfg.UpsertPlaybook(p)
}

func validPlaybookTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false // must pick hosts/folders in the tree (no empty / implicit all)
	}
	if target == "all" {
		return true // legacy playbooks / readonly templates
	}
	parts := splitPlaybookTargets(target)
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if !validPlaybookTargetOne(p) {
			return false
		}
	}
	return true
}

func validPlaybookTargetOne(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" || target == "all" {
		return true
	}
	for _, prefix := range []string{"folder:", "category:", "system:", "host:"} {
		if strings.HasPrefix(target, prefix) {
			value := strings.TrimSpace(strings.TrimPrefix(target, prefix))
			if value == "" {
				return false
			}
			if prefix == "system:" {
				base := value
				if i := strings.Index(value, ":"); i >= 0 {
					base = value[:i]
				}
				return knownPlaybookSystemBase(base)
			}
			return true
		}
	}
	return false
}

// splitPlaybookTargets splits a multi-select target string into selectors.
// Selectors are comma-separated (e.g. "host:a,folder:b,system:linux").
func splitPlaybookTargets(target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	if !strings.Contains(target, ",") {
		return []string{target}
	}
	raw := strings.Split(target, ",")
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// Delete removes a playbook by ID.
func (pm *playbookManager) Delete(id string) error {
	return pm.cfg.DeletePlaybook(id)
}

// ResolveTargets expands a target selector into a list of hosts.
// A single selector uses the classic prefixes; multiple selectors may be
// comma-joined (multi-select UI) and are resolved as a union (deduped by host ID).
// Supported prefixes: "all"; "folder:ID"; "category:xxx"; "system:xxx"; "host:ID".
func (pm *playbookManager) ResolveTargets(target string, hosts []*Host) []*Host {
	parts := splitPlaybookTargets(strings.TrimSpace(target))
	if len(parts) == 0 {
		parts = []string{"all"}
	}
	if len(parts) == 1 {
		return pm.resolveOneTarget(parts[0], hosts)
	}
	seen := make(map[string]struct{})
	var result []*Host
	for _, p := range parts {
		if p == "all" {
			return pm.resolveOneTarget("all", hosts)
		}
		for _, h := range pm.resolveOneTarget(p, hosts) {
			if _, ok := seen[h.ID]; ok {
				continue
			}
			seen[h.ID] = struct{}{}
			result = append(result, h)
		}
	}
	return result
}

func (pm *playbookManager) resolveOneTarget(target string, hosts []*Host) []*Host {
	target = strings.TrimSpace(target)
	var result []*Host
	switch {
	case target == "" || target == "all":
		result = append(result, hosts...)
	case strings.HasPrefix(target, "folder:"):
		fid := strings.TrimSpace(target[len("folder:"):])
		if fid == "" || pm.cfg == nil {
			break
		}
		allow := map[string]struct{}{fid: {}}
		if fid != HostFolderUngroupedID {
			for _, id := range pm.cfg.FolderDescendantIDs(fid) {
				allow[id] = struct{}{}
			}
		}
		for _, h := range hosts {
			hf := pm.cfg.hostFolderOf(h.ID)
			if _, ok := allow[hf]; ok {
				result = append(result, h)
			}
		}
	case strings.HasPrefix(target, "category:"):
		cat := target[len("category:"):]
		for _, h := range hosts {
			// Use the EFFECTIVE category: an operator-set override wins over the
			// agent-self-reported category, exactly as the host list display does.
			// Otherwise a host's playbook membership would be driven by whatever
			// category its (untrusted) agent chose to report.
			effective := h.Category
			if pm.cfg != nil {
				if ov, ok := pm.cfg.CategoryOverride(h.ID); ok {
					effective = ov
				}
			}
			if effective == cat || (effective == "" && cat == Tz("playbook.uncategorized")) {
				result = append(result, h)
			}
		}
	case strings.HasPrefix(target, "system:"):
		sys := strings.ToLower(target[len("system:"):])
		for _, h := range hosts {
			if matchHostSystemSelector(h, sys) {
				result = append(result, h)
			}
		}
	case strings.HasPrefix(target, "host:"):
		hid := target[len("host:"):]
		for _, h := range hosts {
			if h.ID == hid {
				result = append(result, h)
			}
		}
	}
	return result
}

// exportLastRun returns schedule fire times for PG persistence (survives restart).
func (pm *playbookManager) exportLastRun() map[string]int64 {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	out := make(map[string]int64, len(pm.lastRun))
	for k, t := range pm.lastRun {
		out[k] = t.Unix()
	}
	return out
}

// importLastRun restores schedule baselines so interval/daily fires do not reset on restart.
func (pm *playbookManager) importLastRun(m map[string]int64) {
	if len(m) == 0 {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.lastRun == nil {
		pm.lastRun = map[string]time.Time{}
	}
	for k, ts := range m {
		if ts > 0 {
			pm.lastRun[k] = time.Unix(ts, 0)
		}
	}
}

// ExecutionHistory returns recent playbook executions.
func (pm *playbookManager) ExecutionHistory() []PlaybookExecution {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	out := make([]PlaybookExecution, len(pm.executions))
	copy(out, pm.executions)
	// reverse: newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// exportExecutions returns a copy of execution history for PG persistence.
func (pm *playbookManager) exportExecutions() []PlaybookExecution {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	out := make([]PlaybookExecution, len(pm.executions))
	copy(out, pm.executions)
	return out
}

// importExecutions restores execution history from PG at startup.
func (pm *playbookManager) importExecutions(execs []PlaybookExecution) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.executions = execs
	if len(pm.executions) > 100 {
		pm.executions = pm.executions[len(pm.executions)-100:]
	}
	// Restore nextExecID to max seen so new IDs are monotonically increasing.
	for i := range pm.executions {
		e := &pm.executions[i]
		if e.ID >= pm.nextExecID {
			pm.nextExecID = e.ID + 1
		}
		// Process restart cannot resume in-flight runners — mark orphaned
		// running/pending_approval as cancelled so UI is not stuck forever.
		if e.Status == "running" || e.Status == "pending_approval" {
			e.Status = "cancelled"
			if e.EndTime == 0 {
				e.EndTime = time.Now().Unix()
			}
			for hid, hr := range e.HostResults {
				st := strings.TrimSpace(hr.Status)
				if st == "pending" || st == "running" || st == "" {
					hr.Status = "cancelled"
					hr.Reason = "cancelled"
					if len(hr.Steps) == 0 {
						hr.Steps = []StepResult{{Name: "（服务重启）", Status: "cancelled", Output: "服务重启，执行中断"}}
					} else {
						for j := range hr.Steps {
							if hr.Steps[j].Status == "running" || hr.Steps[j].Status == "pending" || hr.Steps[j].Status == "" {
								hr.Steps[j].Status = "cancelled"
								if strings.TrimSpace(hr.Steps[j].Output) == "" {
									hr.Steps[j].Output = "服务重启，执行中断"
								}
							}
						}
					}
					e.HostResults[hid] = hr
				}
			}
		}
	}
}

// StartExecution creates a new execution record and returns it.
func (pm *playbookManager) StartExecution(pb Playbook, operator string, hosts []*Host) *PlaybookExecution {
	return pm.startExecution(pb, operator, hosts, "running", "manual", "")
}

// StartScheduledExecution starts a readonly scheduled run immediately.
func (pm *playbookManager) StartScheduledExecution(pb Playbook, operator string, hosts []*Host) *PlaybookExecution {
	return pm.startExecution(pb, operator, hosts, "running", "schedule", "")
}

// StartPendingExecution records a scheduled high-risk run awaiting human approval.
func (pm *playbookManager) StartPendingExecution(pb Playbook, operator string, hosts []*Host, riskNote string) *PlaybookExecution {
	return pm.startExecution(pb, operator, hosts, "pending_approval", "schedule", riskNote)
}

func (pm *playbookManager) startExecution(pb Playbook, operator string, hosts []*Host, status, trigger, riskNote string) *PlaybookExecution {
	pm.mu.Lock()
	pm.nextExecID++
	exec := PlaybookExecution{
		ID:           pm.nextExecID,
		PlaybookID:   pb.ID,
		PlaybookName: pb.Name,
		Operator:     operator,
		StartTime:    time.Now().Unix(),
		Status:       status,
		Trigger:      trigger,
		RiskNote:     riskNote,
		HostResults:  map[string]HostExecResult{},
	}
	for _, h := range hosts {
		exec.HostResults[h.ID] = HostExecResult{
			Hostname: h.Hostname,
			Status:   "pending",
		}
	}
	pm.executions = append(pm.executions, exec)
	// Trim in-memory ring (PG table keeps full history via upsertPlaybookExecution).
	if len(pm.executions) > 100 {
		pm.executions = pm.executions[len(pm.executions)-100:]
	}
	pm.mu.Unlock()
	return &exec
}

// SetExecutionStatus updates status (and optionally end time for terminal states).
func (pm *playbookManager) SetExecutionStatus(execID int64, status string, finished bool) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for i := range pm.executions {
		if pm.executions[i].ID != execID {
			continue
		}
		pm.executions[i].Status = status
		if finished {
			pm.executions[i].EndTime = time.Now().Unix()
		}
		return true
	}
	return false
}

// playbookTerminalStatus reports whether an execution status is finished.
func playbookTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "partial", "cancelled", "rejected":
		return true
	default:
		return false
	}
}

// UpdateHostResult updates one host's result in an execution.
// No-ops when the execution is already terminal or the host was cancelled
// (cancel must win over late in-flight step writers).
func (pm *playbookManager) UpdateHostResult(execID int64, hostID string, result HostExecResult) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for i := range pm.executions {
		if pm.executions[i].ID != execID {
			continue
		}
		if playbookTerminalStatus(pm.executions[i].Status) {
			return
		}
		if prev, ok := pm.executions[i].HostResults[hostID]; ok && prev.Status == "cancelled" && result.Status != "cancelled" {
			return
		}
		pm.executions[i].HostResults[hostID] = result
		return
	}
}

// FinishExecution marks an execution as done. Terminal statuses are sticky —
// cancelled/rejected/completed/failed/partial cannot be overwritten (cancel-wins).
func (pm *playbookManager) FinishExecution(execID int64, status string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for i := range pm.executions {
		if pm.executions[i].ID != execID {
			continue
		}
		cur := pm.executions[i].Status
		if playbookTerminalStatus(cur) && cur != status {
			if pm.executions[i].EndTime == 0 {
				pm.executions[i].EndTime = time.Now().Unix()
			}
			return
		}
		pm.executions[i].EndTime = time.Now().Unix()
		pm.executions[i].Status = status
		return
	}
}

// GetExecution returns a specific execution by ID (deep-copied host results).
func (pm *playbookManager) GetExecution(id int64) (PlaybookExecution, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, e := range pm.executions {
		if e.ID == id {
			return clonePlaybookExecution(e), true
		}
	}
	return PlaybookExecution{}, false
}
