package main

import (
	"regexp"
	"sort"
	"strings"
)

var (
	hermesWordRe = regexp.MustCompile(`(?i)\bhermes(?:\s+agent)?\b`)
	// Typical AIOps host IDs / shortIDs: 8–32 hex chars (case-insensitive). Last-resort scrub only.
	hostIDHexRe = regexp.MustCompile(`\b[0-9a-fA-F]{8,32}\b`)
)

// hostDisplayLabel returns "hostname (ip)" for user-facing UI. Never returns a raw host ID.
func hostDisplayLabel(hostname, ip, id string) string {
	name := strings.TrimSpace(hostname)
	addr := strings.TrimSpace(ip)
	switch {
	case name != "" && addr != "":
		return name + " (" + addr + ")"
	case name != "":
		return name
	case addr != "":
		return addr
	default:
		_ = id // intentionally unused — never expose raw id
		return "未知主机"
	}
}

// hostDisplayLabelFromHost formats a *Host for UI.
func hostDisplayLabelFromHost(h *Host) string {
	if h == nil {
		return "未知主机"
	}
	return hostDisplayLabel(h.Hostname, h.IP, h.ID)
}

// buildHostLabelMap builds id → display label for redaction.
func (s *Server) buildHostLabelMap() map[string]string {
	out := map[string]string{}
	if s == nil || s.store == nil {
		return out
	}
	for _, h := range s.store.ListHosts() {
		if h == nil || h.ID == "" {
			continue
		}
		out[h.ID] = hostDisplayLabelFromHost(h)
	}
	return out
}

// redactUserFacingText replaces agent branding and known host IDs for end-user copy.
func redactUserFacingText(text string, idToLabel map[string]string) string {
	if text == "" {
		return text
	}
	t := hermesWordRe.ReplaceAllString(text, "智能运维服务")
	t = strings.ReplaceAll(t, "hermes_auto_approve", "ai_auto_approve")
	t = strings.ReplaceAll(t, "reason=hermes_auto_approve", "reason=ai_auto_approve")
	t = strings.ReplaceAll(t, "hermes_enabled", "ai_agent_enabled")
	t = strings.ReplaceAll(t, "hermes_terminal_enabled", "ai_terminal_enabled")
	t = strings.ReplaceAll(t, "Hermes", "智能运维服务")
	t = strings.ReplaceAll(t, "HERMES", "智能运维服务")
	if len(idToLabel) > 0 {
		// Replace longer IDs first to avoid partial clashes.
		ids := make([]string, 0, len(idToLabel))
		for id := range idToLabel {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return len(ids[i]) > len(ids[j]) })
		for _, id := range ids {
			if id == "" {
				continue
			}
			lab := idToLabel[id]
			t = strings.ReplaceAll(t, id, lab)
			// Historical audit lines often used shortID (first 8 hex chars).
			if len(id) >= 8 {
				t = strings.ReplaceAll(t, id[:8], lab)
			}
		}
	}
	// Last resort: scrub leftover long hex blobs that look like host IDs.
	t = hostIDHexRe.ReplaceAllStringFunc(t, func(m string) string {
		if lab, ok := idToLabel[m]; ok {
			return lab
		}
		for id, lab := range idToLabel {
			if strings.HasPrefix(id, m) || strings.HasPrefix(m, id) {
				return lab
			}
		}
		return "未知主机"
	})
	return t
}

// hostLabelForID resolves a host id to "hostname (ip)" for user-facing audit/UI.
func (s *Server) hostLabelForID(hostID string) string {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return "未知主机"
	}
	if s != nil && s.store != nil {
		if h, ok := s.store.GetHost(hostID); ok && h != nil {
			return hostDisplayLabelFromHost(h)
		}
	}
	return "未知主机"
}

// sanitizeActivityEntry rewrites a log line for API consumers: no raw host IDs,
// Host field is hostname (ip), Username is filled when possible.
func (s *Server) sanitizeActivityEntry(e LogEntry, idToLabel map[string]string) LogEntry {
	e.Message = redactUserFacingText(e.Message, idToLabel)
	if e.Host != "" {
		if lab, ok := idToLabel[e.Host]; ok {
			e.Host = lab
		} else if s != nil && s.store != nil {
			if h, ok := s.store.GetHost(e.Host); ok && h != nil {
				e.Host = hostDisplayLabelFromHost(h)
			} else {
				// Host may already be a bare hostname — enrich with IP when possible.
				for _, h := range s.store.ListHosts() {
					if h != nil && (h.Hostname == e.Host || hostDisplayLabelFromHost(h) == e.Host) {
						e.Host = hostDisplayLabelFromHost(h)
						break
					}
				}
			}
		}
	}
	if e.Username == "" && e.Actor != "" && !looksLikeIPAddr(e.Actor) {
		e.Username = e.Actor
	}
	return e
}

// (h *SreyunCore) hostLabelForID resolves a host_id to a safe display label.
func (h *SreyunCore) hostLabelForID(hostID string) string {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return "未知主机"
	}
	if h != nil && h.s != nil && h.s.store != nil {
		if hh, ok := h.s.store.GetHost(hostID); ok && hh != nil {
			return hostDisplayLabelFromHost(hh)
		}
	}
	return "未知主机"
}
