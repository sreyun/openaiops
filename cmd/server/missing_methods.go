package main

import (
	"net/http"
	"time"
)

// sanitizeActivityEntry replaces raw host IDs in LogEntry with display labels.
// This prevents operators from seeing internal host IDs in the activity log.
func (s *Server) sanitizeActivityEntry(e LogEntry, labels map[string]string) LogEntry {
	if e.Host != "" {
		if label, ok := labels[e.Host]; ok {
			e.Host = label
		}
	}
	// Also redact host IDs that appear in the message text
	if e.Message != "" && len(labels) > 0 {
		e.Message = redactUserFacingText(e.Message, labels)
	}
	return e
}

// collectAlerts gathers all alerts (active + resolved history) with RBAC filtering.
// This is extracted from handleAlerts for reuse by handleAlertsSummary.
func (s *Server) collectAlerts(r *http.Request) []Alert {
	hosts := s.filterHostsForUser(r, s.store.ListHosts())
	alerts := Evaluate(hosts, s.cfg.Thresholds())
	alerts = append(alerts, EvaluateHyperV(s.hv)...)
	alerts = append(alerts, EvaluateSNMP(s.snmp, s.cfg.Thresholds())...)
	alerts = append(alerts, EvaluateNetFlow(s.nf, s.cfg.Thresholds())...)
	since := s.notifier.ActiveSince()
	states := s.store.AlertStates()
	for i := range alerts {
		if t, ok := since[alertKey(alerts[i])]; ok {
			alerts[i].Since = t
		}
		alerts[i].Status = states[alertKey(alerts[i])]
	}
	alerts = append(alerts, s.checks.DownAlerts()...)
	for i := range alerts {
		if alerts[i].Status == "" {
			if st, ok := states[alertKey(alerts[i])]; ok {
				alerts[i].Status = st
			}
		}
	}
	if alerts == nil {
		alerts = []Alert{}
	}
	// Append resolved alerts from persistent history
	showHistory := false
	if r != nil {
		showHistory = r.URL.Query().Get("history") == "true"
	}
	sevenDaysAgo := time.Now().Unix() - 7*86400
	history := s.filterAlertRecordsForUser(r, s.store.AlertHistory(200, false))
	for _, rec := range history {
		if rec.ResolvedAt == 0 {
			continue
		}
		if !showHistory && rec.ResolvedAt < sevenDaysAgo {
			continue
		}
		alerts = append(alerts, Alert{
			HostID:    rec.HostID,
			Hostname:  rec.Hostname,
			IP:        rec.IP,
			Level:     rec.Level,
			Type:      rec.Type,
			Scope:     rec.Scope,
			Since:     rec.FiredAt,
			Message:   rec.Message,
			Value:     rec.Value,
			Timestamp: rec.ResolvedAt,
			Status:    "resolved",
		})
	}
	alerts = s.filterAlertsForUser(r, alerts)
	return alerts
}

// licenseStatus returns the current license status.
// Open-source version always returns "active" with no restrictions.
func (s *Server) licenseStatus() licenseStatusResult {
	return licenseStatusResult{
		State:     "active",
		DaysLeft:  9999,
		UsedHosts: 0,
		MaxHosts:  0, // 0 = unlimited
		ReadOnly:  false,
		Enforced:  false,
		Vendor:    "open-source",
		InstallID: "",
	}
}

// licenseStatusResult represents the license status for API responses.
type licenseStatusResult struct {
	State     string `json:"state"`
	DaysLeft  int    `json:"days_left"`
	UsedHosts int    `json:"used_hosts"`
	MaxHosts  int    `json:"max_hosts"`
	ReadOnly  bool   `json:"read_only"`
	Enforced  bool   `json:"enforced"`
	Vendor    string `json:"vendor,omitempty"`
	InstallID string `json:"install_id,omitempty"`
}
