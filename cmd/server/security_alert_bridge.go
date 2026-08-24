package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Shared dedupe for host/web/slow-SQL security alerts (longer window than content DLP).
var (
	secAlertMu   sync.Mutex
	secAlertSeen = map[string]int64{}
)

const secAlertWindowSec int64 = 1800 // 30m — scans are infrequent; avoid re-page storms

func shouldAlertSecurity(key string, now int64) bool {
	if now <= 0 {
		now = time.Now().Unix()
	}
	secAlertMu.Lock()
	defer secAlertMu.Unlock()
	if last, ok := secAlertSeen[key]; ok && now-last < secAlertWindowSec {
		return false
	}
	secAlertSeen[key] = now
	if len(secAlertSeen) > 10000 {
		for k, t := range secAlertSeen {
			if now-t >= secAlertWindowSec {
				delete(secAlertSeen, k)
			}
		}
	}
	return true
}

func findingLevelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "critical", "crit":
		return 3
	case "high":
		return 2
	case "medium", "med", "warning", "warn":
		return 1
	default:
		return 0
	}
}

func (s *Server) notifyHostSecurityScanCompleted(scan *HostScanResult) {
	if s == nil || scan == nil || scan.Status != "completed" || s.notifier == nil {
		return
	}
	cfg := s.cfg.Get()
	if !cfg.AlertsEnabled {
		return
	}
	hostLabel := scan.Hostname
	if hostLabel == "" {
		hostLabel = scan.HostID
	}
	findings := mergeHostFindingStatus(s.secFindings, scan.HostID, scan.Findings)
	crit, high := 0, 0
	var sample []string
	for _, f := range findings {
		if !findingOpen(f.Status) {
			continue
		}
		lv := strings.ToLower(f.Level)
		switch lv {
		case "critical", "crit":
			crit++
			if len(sample) < 3 {
				sample = append(sample, fmt.Sprintf("[危急] %s", strings.TrimSpace(f.Title)))
			}
		case "high":
			high++
			if len(sample) < 3 {
				sample = append(sample, fmt.Sprintf("[高危] %s", strings.TrimSpace(f.Title)))
			}
		}
	}
	// Always evaluate baseline rise (even without open high/crit).
	s.raiseSecurityBaselineEarlyWarning("host_security", scan.HostID, hostLabel, scan.BaselineDiff)
	if crit == 0 && high == 0 {
		return
	}
	level := "warning"
	if crit > 0 {
		level = "critical"
	}
	msg := fmt.Sprintf("主机安全扫描「%s」发现开放风险：危急 %d · 高危 %d", hostLabel, crit, high)
	if len(sample) > 0 {
		msg += "；示例：" + strings.Join(sample, "；")
	}
	now := time.Now().Unix()
	dedupe := fmt.Sprintf("host_security|%s|%s", scan.HostID, level)
	if !shouldAlertSecurity(dedupe, now) {
		return
	}
	a := Alert{
		HostID:    scan.HostID,
		Hostname:  hostLabel,
		Level:     level,
		Type:      "host_security",
		Scope:     "scan:" + scan.ID,
		Message:   msg,
		Timestamp: now,
	}
	s.store.AddLog(LogEntry{Kind: KindSystem, Level: level, Actor: "主机安全", Host: hostLabel, Message: msg})
	s.notifier.enqueuePush(cfg, a, true)
	var incidentID int64
	if level == "critical" && s.incidents != nil {
		incidentID = s.incidents.OnAlertTransition(a, alertKey(a), true)
	}
	s.triggerSecurityRemediation(a, incidentID)
}

func (s *Server) notifyWebSecurityScanCompleted(scan *WebScanResult) {
	if s == nil || scan == nil || scan.Status != "completed" || s.notifier == nil {
		return
	}
	cfg := s.cfg.Get()
	if !cfg.AlertsEnabled {
		return
	}
	findings := mergeWebFindingStatus(s.secFindings, scan.TargetID, scan.Findings)
	crit, high := 0, 0
	var sample []string
	for _, f := range findings {
		if !findingOpen(f.Status) {
			continue
		}
		sev := strings.ToLower(f.Severity)
		switch sev {
		case "critical", "crit":
			crit++
			if len(sample) < 3 {
				sample = append(sample, fmt.Sprintf("[危急] %s", strings.TrimSpace(f.Name)))
			}
		case "high":
			high++
			if len(sample) < 3 {
				sample = append(sample, fmt.Sprintf("[高危] %s", strings.TrimSpace(f.Name)))
			}
		}
	}
	if crit == 0 && high == 0 {
		name := scan.TargetName
		if name == "" {
			name = scan.BaseURL
		}
		s.raiseSecurityBaselineEarlyWarning("web_security", scan.TargetID, name, scan.BaselineDiff)
		return
	}
	level := "warning"
	if crit > 0 {
		level = "critical"
	}
	name := scan.TargetName
	if name == "" {
		name = scan.BaseURL
	}
	msg := fmt.Sprintf("Web 扫描「%s」发现开放风险：危急 %d · 高危 %d", name, crit, high)
	if len(sample) > 0 {
		msg += "；示例：" + strings.Join(sample, "；")
	}
	now := time.Now().Unix()
	dedupe := fmt.Sprintf("web_security|%s|%s", scan.TargetID, level)
	if !shouldAlertSecurity(dedupe, now) {
		return
	}
	a := Alert{
		HostID:    scan.TargetID,
		Hostname:  name,
		Level:     level,
		Type:      "web_security",
		Scope:     "scan:" + scan.ID,
		Message:   msg,
		Timestamp: now,
	}
	s.store.AddLog(LogEntry{Kind: KindSystem, Level: level, Actor: "Web安全", Host: name, Message: msg})
	s.notifier.enqueuePush(cfg, a, true)
	var incidentID int64
	if level == "critical" && s.incidents != nil {
		incidentID = s.incidents.OnAlertTransition(a, alertKey(a), true)
	}
	// Match rules on both web_security (alert type) and web_vuln (plan alias).
	s.triggerSecurityRemediation(a, incidentID)
	a2 := a
	a2.Type = "web_vuln"
	s.triggerSecurityRemediation(a2, incidentID)
	s.raiseSecurityBaselineEarlyWarning("web_security", scan.TargetID, name, scan.BaselineDiff)
}

// raiseSecurityBaselineEarlyWarning triggers when consecutive scans show rising
// findings (added/worsened) — early signal before a full critical page storm.
func (s *Server) raiseSecurityBaselineEarlyWarning(kind, refID, label string, diff *ScanBaselineDiff) {
	if s == nil || s.incidents == nil || diff == nil {
		return
	}
	if diff.Added+diff.Worsened < 2 {
		return
	}
	domain := "安全"
	switch kind {
	case "host_security":
		domain = "主机安全"
	case "web_security":
		domain = "Web安全"
	}
	title := fmt.Sprintf("[预测预警] %s「%s」风险呈上升趋势", domain, label)
	analysis := fmt.Sprintf("较上次扫描：新增 %d · 恶化 %d · 缓解 %d。示例新增：%s\n建议：优先处置新增/恶化项，避免风险累积。",
		diff.Added, diff.Worsened, diff.Improved, strings.Join(diff.SamplesAdded, "；"))
	key := fmt.Sprintf("sec_trend:%s:%s", kind, refID)
	id, created := s.incidents.raise(key, title, "warning", "AI预测预警", refID, label, kind)
	if id > 0 {
		s.incidents.AddEvent(id, "ai_analysis", "AI", analysis)
		s.store.MarkDirty()
		if created {
			go s.rememberAI("alert", key, fmt.Sprintf("【预测预警】%s\n%s", title, analysis))
		}
	}
}

// triggerSecurityRemediation matches RemediationRules (host_security / web_vuln / …).
// Matching rules default to require_approval unless explicitly auto-enabled.
func (s *Server) triggerSecurityRemediation(a Alert, incidentID int64) {
	if s == nil || s.remediation == nil {
		return
	}
	s.remediation.OnAlert(a, incidentID)
}

func (s *Server) notifySlowSQLReport(c MySQLConnection, rep *SlowSQLReport) {
	if s == nil || rep == nil || rep.Status != "completed" || s.notifier == nil {
		return
	}
	cfg := c.SlowSQL.withDefaults()
	if cfg.AlertDisabled {
		return
	}
	serverCfg := s.cfg.Get()
	if !serverCfg.AlertsEnabled {
		return
	}
	minAvg := cfg.AlertMinAvgLatencyMs
	if minAvg <= 0 {
		minAvg = cfg.MinAvgLatencyMs
	}
	if minAvg <= 0 {
		minAvg = 100
	}
	hot := 0
	var worst SlowSQLItem
	for _, it := range rep.Items {
		if it.AvgLatencyMs < minAvg {
			continue
		}
		hot++
		if it.AvgLatencyMs > worst.AvgLatencyMs || (it.AvgLatencyMs == worst.AvgLatencyMs && it.SumLatencyMs > worst.SumLatencyMs) {
			worst = it
		}
	}
	if hot == 0 {
		return
	}
	level := "warning"
	if worst.AvgLatencyMs >= minAvg*10 || worst.SumLatencyMs >= 60000 {
		level = "critical"
	}
	now := time.Now().Unix()
	dedupe := fmt.Sprintf("slow_sql|%s|%s", c.ID, level)
	if !shouldAlertSecurity(dedupe, now) {
		return
	}
	schema := worst.Schema
	if schema == "" {
		schema = "(unknown)"
	}
	msg := fmt.Sprintf("MySQL「%s」慢 SQL 检查：%d 条超过 %.0fms；最慢库 %s avg=%.0fms sum=%.0fms ×%d",
		c.Name, hot, minAvg, schema, worst.AvgLatencyMs, worst.SumLatencyMs, worst.CountStar)
	a := Alert{
		HostID:    "mysql:" + c.ID,
		Hostname:  c.Name,
		Level:     level,
		Type:      "slow_sql",
		Scope:     "report:" + rep.ID,
		Message:   msg,
		Timestamp: now,
	}
	s.store.AddLog(LogEntry{Kind: KindSystem, Level: level, Actor: "慢SQL", Host: c.Name, Message: msg})
	s.notifier.enqueuePush(serverCfg, a, true)
	if level == "critical" && s.incidents != nil {
		incID := s.incidents.OnAlertTransition(a, alertKey(a), true)
		if incID > 0 {
			s.incidents.AddLinks(incID, []OpsLink{
				{Type: "datasource", ID: c.ID, Role: "affects", Name: c.Name},
				{Type: "alert", ID: alertKey(a), Role: "caused_by", Name: "slow_sql"},
			}, "慢SQL", "慢 SQL 报告关联数据源 "+c.Name)
		}
	}
}

// raiseSlowSQLTrendEarlyWarning fires when digests worsen vs previous report —
// earlier than waiting for absolute latency alert thresholds.
func (s *Server) raiseSlowSQLTrendEarlyWarning(c MySQLConnection, rep *SlowSQLReport) {
	if s == nil || s.incidents == nil || rep == nil || rep.Trend == nil {
		return
	}
	tr := rep.Trend
	if tr.Worsened < 2 && tr.NewDigests < 3 {
		return
	}
	title := fmt.Sprintf("[预测预警] MySQL「%s」慢 SQL 呈恶化趋势", c.Name)
	analysis := fmt.Sprintf("较上次报告：新增 %d · 恶化 %d · 改善 %d。示例恶化：%s\n建议：优先审查恶化 digest 的执行计划/索引，避免拖垮业务。",
		tr.NewDigests, tr.Worsened, tr.Improved, strings.Join(tr.SamplesWorse, "；"))
	key := fmt.Sprintf("slow_sql_trend:%s", c.ID)
	id, created := s.incidents.raise(key, title, "warning", "AI预测预警", "mysql:"+c.ID, c.Name, "slow_sql")
	if id > 0 {
		s.incidents.AddEvent(id, "ai_analysis", "AI", analysis)
		s.store.MarkDirty()
		if created {
			go s.rememberAI("alert", "slow_sql_trend:"+c.ID, fmt.Sprintf("【预测预警】%s\n%s", title, analysis))
		}
	}
}
