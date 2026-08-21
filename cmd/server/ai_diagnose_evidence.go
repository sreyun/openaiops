package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// gatherLiveDiagnoseEvidence pulls fresh host/alert signals into the diagnose prompt
// before the LLM runs — deterministic, no LLM round-trip.
func (s *Server) gatherLiveDiagnoseEvidence(inc Incident) (extra string, cites []RAGCitation) {
	if s == nil {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("\n\n【实时证据刷新】\n")
	n := 0
	if hid := strings.TrimSpace(inc.HostID); hid != "" && s.store != nil {
		if h, ok := s.store.GetHost(hid); ok && h != nil {
			age := time.Now().Unix() - h.LastSeen
			online := age <= 180
			line := fmt.Sprintf("- 主机 %s (%s)：心跳 %ds 前 · %s",
				firstNonEmptyOrDash(h.Hostname, hid), hid, age, map[bool]string{true: "在线", false: "离线/超时"}[online])
			b.WriteString(line + "\n")
			cites = append(cites, RAGCitation{Kind: "inspect", Source: "host:" + hid, Title: "主机心跳", Summary: line})
			n++
			if h.Latest != nil {
				m := h.Latest.Metrics
				ml := fmt.Sprintf("- 实时指标：CPU %.1f%% · Mem %.1f%% · Disk %.1f%% · Load1 %.2f",
					m.CPUPercent, m.MemPercent, m.DiskPercent, m.Load1)
				b.WriteString(ml + "\n")
				cites = append(cites, RAGCitation{Kind: "metric", Source: "host:" + hid, Title: "host.metrics", Summary: ml})
				n++
			}
			now := time.Now().Unix()
			if samples, ok := s.loadDurableHostHistory(hid, now-6*3600, now, vmNamesForMetricKeys([]string{"cpu", "memory", "disk", "load"})); ok {
				if tl := formatHostTrendLine(samples, 6); tl != "" {
					line := "- " + tl + "（VictoriaMetrics）"
					b.WriteString(line + "\n")
					cites = append(cites, RAGCitation{Kind: "metric", Source: "host:" + hid, Title: "host.trend.6h", Summary: line})
					n++
				}
			}
		}
	}
	if s.notifier != nil {
		type scoredAlert struct {
			level int
			sum   string
			key   string
			typ   string
		}
		var cand []scoredAlert
		for _, a := range s.notifier.ActiveAlerts() {
			if inc.HostID != "" && a.HostID != "" && a.HostID != inc.HostID {
				continue
			}
			lvl := 1
			switch strings.ToLower(a.Level) {
			case "critical", "crit", "fatal":
				lvl = 3
			case "warning", "warn":
				lvl = 2
			}
			// Prefer exact key match.
			if inc.Key != "" && alertKey(a) == inc.Key {
				lvl += 10
			}
			cand = append(cand, scoredAlert{
				level: lvl,
				sum:   fmt.Sprintf("%s %s %s", a.Level, a.Type, a.Message),
				key:   alertKey(a),
				typ:   a.Type,
			})
		}
		sort.Slice(cand, func(i, j int) bool { return cand[i].level > cand[j].level })
		firing := 0
		for _, a := range cand {
			if firing >= 5 {
				break
			}
			firing++
			b.WriteString("- 活动告警：" + a.sum + "\n")
			cites = append(cites, RAGCitation{Kind: "alert", Source: a.key, Title: a.typ, Summary: a.sum})
			n++
		}
		if firing == 0 && inc.Key != "" {
			b.WriteString("- 关联告警 key 当前未 firing（可能已恢复）\n")
			cites = append(cites, RAGCitation{Kind: "alert", Source: inc.Key, Title: "alert.cleared", Summary: "key 未 firing"})
			n++
		}
	}
	if n == 0 {
		return "", nil
	}
	return b.String(), cites
}

// loopVerifyHostSignals extends post-fix checks with live metric thresholds
// aligned to configured alert Thresholds (CPUCrit/MemCrit/DiskCrit).
func (s *Server) loopVerifyHostSignals(inc Incident) (ok bool, notes []string) {
	ok = true
	if s == nil || strings.TrimSpace(inc.HostID) == "" {
		return true, nil
	}
	if s.store == nil {
		return false, []string{"无法读取主机指标（store 不可用）"}
	}
	h, found := s.store.GetHost(inc.HostID)
	if !found || h == nil {
		return false, []string{"主机不存在，无法做指标回验"}
	}
	if h.Latest == nil {
		return false, []string{"主机无最新采样，无法做指标回验"}
	}
	m := h.Latest.Metrics
	th := Thresholds{CPUCrit: 90, MemCrit: 90, DiskCrit: 90}
	if s.cfg != nil {
		th = s.cfg.Thresholds()
		if th.CPUCrit <= 0 {
			th.CPUCrit = 90
		}
		if th.MemCrit <= 0 {
			th.MemCrit = 90
		}
		if th.DiskCrit <= 0 {
			th.DiskCrit = 90
		}
	}
	typ := strings.ToLower(inc.Type + " " + inc.Key + " " + inc.Title)
	switch {
	case strings.Contains(typ, "cpu"):
		if m.CPUPercent >= th.CPUCrit {
			ok = false
			notes = append(notes, fmt.Sprintf("CPU 仍高（%.1f%% ≥ 严重阈值 %.0f%%）", m.CPUPercent, th.CPUCrit))
		} else {
			notes = append(notes, fmt.Sprintf("CPU 已回落（%.1f%% < %.0f%%）", m.CPUPercent, th.CPUCrit))
		}
	case strings.Contains(typ, "mem") || strings.Contains(typ, "memory") || strings.Contains(typ, "内存"):
		if m.MemPercent >= th.MemCrit {
			ok = false
			notes = append(notes, fmt.Sprintf("内存仍高（%.1f%% ≥ 严重阈值 %.0f%%）", m.MemPercent, th.MemCrit))
		} else {
			notes = append(notes, fmt.Sprintf("内存已回落（%.1f%% < %.0f%%）", m.MemPercent, th.MemCrit))
		}
	case strings.Contains(typ, "disk") || strings.Contains(typ, "磁盘"):
		if m.DiskPercent >= th.DiskCrit {
			ok = false
			notes = append(notes, fmt.Sprintf("磁盘仍高（%.1f%% ≥ 严重阈值 %.0f%%）", m.DiskPercent, th.DiskCrit))
		} else {
			notes = append(notes, fmt.Sprintf("磁盘已回落（%.1f%% < %.0f%%）", m.DiskPercent, th.DiskCrit))
		}
	default:
		// Generic: fail if both CPU and Mem remain critical (system still stressed).
		if m.CPUPercent >= th.CPUCrit && m.MemPercent >= th.MemCrit {
			ok = false
			notes = append(notes, fmt.Sprintf("主机仍高压：CPU %.1f%% / Mem %.1f%%", m.CPUPercent, m.MemPercent))
		} else {
			notes = append(notes, fmt.Sprintf("抽样指标 CPU %.1f%% / Mem %.1f%% / Disk %.1f%%", m.CPUPercent, m.MemPercent, m.DiskPercent))
		}
	}
	return ok, notes
}
