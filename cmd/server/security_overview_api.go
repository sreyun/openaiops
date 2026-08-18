package main

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleSecurityOverview(w http.ResponseWriter, r *http.Request) {
	hostCfg := s.cfg.HostSecurity()
	webCfg := s.cfg.WebSecurity()

	counts := s.countOpenSecurityFindingsDetail()
	hostRunning, hostStuck := s.hostSec.scanActivity(hostCfg.TimeoutSec)
	webRunning, webStuck := s.webSec.scanActivity(webCfg.TimeoutSec)

	hostSched := scheduleHealthFromPlaybook(hostCfg.Enabled, hostCfg.Schedule)
	webScheduled := 0
	for _, t := range webCfg.Targets {
		if t.Enabled && t.Schedule != nil && t.Schedule.Enabled {
			webScheduled++
		}
	}
	webSched := map[string]any{
		"enabled":           webScheduled > 0,
		"scheduled_targets": webScheduled,
		"total_targets":     len(webCfg.Targets),
	}
	healthy := (!hostSched["enabled"].(bool) || hostSched["healthy"].(bool)) &&
		(webScheduled == 0 || webSched["enabled"].(bool))

	writeJSON(w, http.StatusOK, map[string]any{
		"open_critical":      counts.Critical,
		"open_high":          counts.High,
		"host_open_critical": counts.HostCritical,
		"host_open_high":     counts.HostHigh,
		"web_open_critical":  counts.WebCritical,
		"web_open_high":      counts.WebHigh,
		"schedule": map[string]any{
			"healthy": healthy,
			"host":    hostSched,
			"web":     webSched,
		},
		"scans": map[string]any{
			"host_running":  hostRunning,
			"web_running":   webRunning,
			"host_stuck":    hostStuck,
			"web_stuck":     webStuck,
			"total_running": hostRunning + webRunning,
			"total_stuck":   hostStuck + webStuck,
		},
	})
}

type openSecurityCounts struct {
	Critical, High         int
	HostCritical, HostHigh int
	WebCritical, WebHigh   int
}

func (s *Server) countOpenSecurityFindingsDetail() openSecurityCounts {
	var out openSecurityCounts
	s.hostSec.mu.Lock()
	for _, scan := range s.hostSec.lastByHost {
		if scan == nil || scan.Status != "completed" {
			continue
		}
		findings := mergeHostFindingStatus(s.secFindings, scan.HostID, scan.Findings)
		for _, f := range findings {
			if !findingOpen(f.Status) {
				continue
			}
			switch strings.ToLower(f.Level) {
			case "critical", "crit":
				out.Critical++
				out.HostCritical++
			case "high":
				out.High++
				out.HostHigh++
			}
		}
	}
	s.hostSec.mu.Unlock()

	latestWeb := map[string]*WebScanResult{}
	s.webSec.mu.Lock()
	for _, sc := range s.webSec.scans {
		if sc == nil || sc.Status != "completed" {
			continue
		}
		prev := latestWeb[sc.TargetID]
		if prev == nil || sc.FinishedAt > prev.FinishedAt {
			latestWeb[sc.TargetID] = sc
		}
	}
	for _, sc := range latestWeb {
		findings := mergeWebFindingStatus(s.secFindings, sc.TargetID, sc.Findings)
		for _, f := range findings {
			if !findingOpen(f.Status) {
				continue
			}
			switch strings.ToLower(f.Severity) {
			case "critical", "crit":
				out.Critical++
				out.WebCritical++
			case "high":
				out.High++
				out.WebHigh++
			}
		}
	}
	s.webSec.mu.Unlock()
	return out
}

func scheduleHealthFromPlaybook(enabled bool, sc *PlaybookSchedule) map[string]any {
	out := map[string]any{
		"enabled": false,
		"healthy": true,
		"kind":    "",
	}
	if !enabled || sc == nil || !sc.Enabled {
		return out
	}
	out["enabled"] = true
	out["kind"] = sc.Kind
	switch sc.Kind {
	case "interval":
		if sc.IntervalMin < 15 {
			out["healthy"] = false
			out["detail"] = "interval below 15m minimum"
		}
	case "daily", "weekly":
		if _, ok := parseHHMM(sc.At); !ok {
			out["healthy"] = false
			out["detail"] = "invalid schedule time"
		}
	default:
		out["healthy"] = false
		out["detail"] = "unknown schedule kind"
	}
	return out
}

func findingOpen(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "resolved", "false_positive", "ack", "accepted":
		return false
	default:
		return true
	}
}

func (m *hostSecurityManager) scanActivity(timeoutSec int) (running, stuck int) {
	if timeoutSec <= 0 {
		timeoutSec = 180
	}
	grace := int64(60)
	limit := int64(timeoutSec) + grace
	now := time.Now().Unix()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sc := range m.scans {
		if sc == nil || sc.Status != "running" {
			continue
		}
		running++
		if sc.StartedAt > 0 && now-sc.StartedAt > limit {
			stuck++
		}
	}
	return running, stuck
}

func (m *webScanManager) scanActivity(timeoutSec int) (running, stuck int) {
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	grace := int64(120)
	limit := int64(timeoutSec) + grace
	now := time.Now().Unix()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sc := range m.scans {
		if sc == nil || sc.Status != "running" {
			continue
		}
		running++
		if sc.StartedAt > 0 && now-sc.StartedAt > limit {
			stuck++
		}
	}
	return running, stuck
}
