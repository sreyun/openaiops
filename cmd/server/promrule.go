package main

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// 指标告警规则（PromQL）——把抓取/推送来的 Prometheus 生态指标接入告警。
//
// 规则的表达式已编码条件（如 mysql_up==0、rate(errors[5m])>10、jvm_memory_used/max>0.9），
// 定时对 VM 做即时查询，非空结果的每组标签即一个告警实例；for 时长去抖后经 pushChannels 推送
// （自动过治理→incident→AI 研判），标签集消失即恢复。与主机阈值告警统一走同一通道。
// ============================================================================

// PromRule 是一条指标告警规则。
type PromRule struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Expr      string `json:"expr"`              // PromQL 条件表达式（非空结果=告警）
	ForSec    int    `json:"for_sec,omitempty"` // 持续时长（秒），连续满足才触发，抑制抖动
	Level     string `json:"level"`             // warning | critical
	Message   string `json:"message,omitempty"` // 告警文案，支持 {{label}} 与 {{value}} 模板；空=自动
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

var promTemplateRe = regexp.MustCompile(`\{\{\s*\$?(\w+)\s*\}\}`)

// ---- ConfigStore CRUD（与其它配置同机制，无密钥字段） ----

func (cs *ConfigStore) PromRules() []PromRule {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]PromRule, len(cs.cfg.PromRules))
	copy(out, cs.cfg.PromRules)
	return out
}

func (cs *ConfigStore) UpsertPromRule(r PromRule) (PromRule, error) {
	cs.mu.Lock()
	if r.ID == "" {
		r.ID = genToken()[:8]
		r.CreatedAt = time.Now().Unix()
		cs.cfg.PromRules = append(cs.cfg.PromRules, r)
	} else {
		found := false
		for i := range cs.cfg.PromRules {
			if cs.cfg.PromRules[i].ID == r.ID {
				r.CreatedAt = cs.cfg.PromRules[i].CreatedAt
				cs.cfg.PromRules[i] = r
				found = true
				break
			}
		}
		if !found {
			r.CreatedAt = time.Now().Unix()
			cs.cfg.PromRules = append(cs.cfg.PromRules, r)
		}
	}
	cs.mu.Unlock()
	return r, cs.save()
}

func (cs *ConfigStore) DeletePromRule(id string) error {
	cs.mu.Lock()
	kept := cs.cfg.PromRules[:0]
	for _, r := range cs.cfg.PromRules {
		if r.ID != id {
			kept = append(kept, r)
		}
	}
	cs.cfg.PromRules = kept
	cs.mu.Unlock()
	return cs.save()
}

// ---- 评估引擎 ----

type ruleInstance struct {
	firstSeen int64
	fired     bool
	labels    map[string]string
	value     float64
}

type promRuleManager struct {
	cfg      *ConfigStore
	vm       *vmWriter
	notifier *Notifier
	store    *Store

	mu     sync.Mutex
	active map[string]*ruleInstance // key = ruleID + "\x00" + labelsFingerprint
}

func newPromRuleManager(cfg *ConfigStore, vm *vmWriter, notifier *Notifier, store *Store) *promRuleManager {
	return &promRuleManager{cfg: cfg, vm: vm, notifier: notifier, store: store, active: map[string]*ruleInstance{}}
}

func (m *promRuleManager) Run(tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	m.evaluate()
	for range t.C {
		m.evaluate()
	}
}

func (m *promRuleManager) evaluate() {
	if m.vm == nil || !m.vm.enabled() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("promrule evaluate panic recovered", "err", r)
		}
	}()
	now := time.Now().Unix()
	cfg := m.cfg.Get()
	rules := m.cfg.PromRules()
	current := map[string]bool{}  // 本轮仍在告警的实例
	queried := map[string]bool{}  // 本轮成功查询过的规则（查询失败不误判恢复）
	byID := map[string]PromRule{} // 供恢复时取规则
	for _, r := range rules {
		byID[r.ID] = r
		if !r.Enabled || strings.TrimSpace(r.Expr) == "" {
			continue
		}
		series, ok := m.vm.vmQueryVector(r.Expr)
		if !ok {
			continue // 查询失败：跳过，不触发也不恢复
		}
		queried[r.ID] = true
		for _, s := range series {
			key := r.ID + "\x00" + promFingerprint(s.Labels)
			current[key] = true
			m.mu.Lock()
			inst := m.active[key]
			if inst == nil {
				inst = &ruleInstance{firstSeen: now}
				m.active[key] = inst
			}
			inst.labels, inst.value = s.Labels, s.Value
			shouldFire := !inst.fired && now-inst.firstSeen >= int64(r.ForSec)
			if shouldFire {
				inst.fired = true
			}
			m.mu.Unlock()
			if shouldFire {
				m.fire(r, s, cfg)
			}
		}
	}
	// 恢复：本轮已成功查询该规则、但某标签集不再出现 → 恢复并清理
	m.mu.Lock()
	for key, inst := range m.active {
		ruleID := key[:strings.IndexByte(key, 0)]
		if current[key] || !queried[ruleID] {
			continue
		}
		wasFired := inst.fired
		lbls := inst.labels
		delete(m.active, key)
		m.mu.Unlock()
		if wasFired {
			m.resolve(byID[ruleID], lbls, cfg)
		}
		m.mu.Lock()
	}
	m.mu.Unlock()
}

func (m *promRuleManager) fire(r PromRule, s promSeries, cfg ServerConfig) {
	lvl := r.Level
	if lvl != "warning" && lvl != "critical" {
		lvl = "warning"
	}
	a := Alert{
		Level: lvl, Type: "promrule", Scope: r.ID + "/" + promFingerprint(s.Labels),
		Hostname: ruleInstanceHost(s.Labels, r.Name), Message: renderRuleMessage(r, s.Labels, s.Value),
		Value: s.Value, Timestamp: time.Now().Unix(),
	}
	m.store.AddLog(LogEntry{Kind: KindSystem, Level: lvl, Actor: "指标告警", Host: a.Hostname, Message: a.Message})
	if cfg.AlertsEnabled {
		m.notifier.enqueuePush(cfg, a, true)
		// 闭环：开 Incident → 触发 AI 研判 + 自学习，并对匹配主机跑自动修复（与阈值告警一致）。
		if m.notifier.incidents != nil {
			incID := m.notifier.incidents.OnAlertTransition(a, alertKey(a), true)
			if incID != 0 && m.notifier.remediation != nil {
				m.notifier.remediation.OnAlert(a, incID)
			}
		}
	}
}

func (m *promRuleManager) resolve(r PromRule, labels map[string]string, cfg ServerConfig) {
	host := ruleInstanceHost(labels, r.Name)
	a := Alert{
		Level: "info", Type: "promrule", Scope: r.ID + "/" + promFingerprint(labels),
		Hostname: host, Message: "指标告警已恢复：" + r.Name, Timestamp: time.Now().Unix(),
	}
	m.store.AddLog(LogEntry{Kind: KindSystem, Level: "info", Actor: "指标告警", Host: host, Message: a.Message})
	if cfg.AlertsEnabled {
		m.notifier.enqueuePush(cfg, a, false)
		// 闭环：按同一 alertKey 恢复对应 Incident（fire/resolve 的 HostID/Type/Scope 一致）。
		if m.notifier.incidents != nil {
			m.notifier.incidents.OnAlertTransition(a, alertKey(a), false)
		}
	}
}

// evalPreview 供 UI「测试」：立即评估表达式，返回命中序列数 + 前几条样例。
func (m *promRuleManager) evalPreview(expr string) (int, []string, bool) {
	if m.vm == nil || !m.vm.enabled() {
		return 0, nil, false
	}
	series, ok := m.vm.vmQueryVector(expr)
	if !ok {
		return 0, nil, false
	}
	samples := make([]string, 0, 5)
	for i, s := range series {
		if i >= 5 {
			break
		}
		samples = append(samples, fmt.Sprintf("%s = %g", promLabelsStr(s.Labels), s.Value))
	}
	return len(series), samples, true
}

// ---- helpers ----

func promFingerprint(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte(',')
	}
	return b.String()
}

func promLabelsStr(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func ruleInstanceHost(labels map[string]string, fallback string) string {
	if v := labels["instance"]; v != "" {
		return v
	}
	if v := labels["job"]; v != "" {
		return v
	}
	return fallback
}

// renderRuleMessage 渲染告警文案：{{label}} 替换为标签值，{{value}}/{{$value}} 替换为数值；空模板给默认。
func renderRuleMessage(r PromRule, labels map[string]string, value float64) string {
	tmpl := strings.TrimSpace(r.Message)
	if tmpl == "" {
		return fmt.Sprintf("%s：%s = %g", r.Name, promLabelsStr(labels), value)
	}
	return promTemplateRe.ReplaceAllStringFunc(tmpl, func(mm string) string {
		k := promTemplateRe.FindStringSubmatch(mm)[1]
		if k == "value" {
			return fmt.Sprintf("%g", value)
		}
		if v, ok := labels[k]; ok {
			return v
		}
		return mm
	})
}
