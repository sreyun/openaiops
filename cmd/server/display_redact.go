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
	return redactUserFacingTextSorted(text, idToLabel, sortedRedactIDs(idToLabel))
}

// sortedRedactIDs 把主机 ID 按长度降序排好（先替换长的，避免前缀误伤）。
// 单独拆出来是为了让批量调用方（活动日志一次上千条）只排一次序。
func sortedRedactIDs(idToLabel map[string]string) []string {
	if len(idToLabel) == 0 {
		return nil
	}
	ids := make([]string, 0, len(idToLabel))
	for id := range idToLabel {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return len(ids[i]) > len(ids[j]) })
	return ids
}

// redactUserFacingTextSorted 是 redactUserFacingText 的主体；ids 必须是 sortedRedactIDs 的结果。
//
// 只有正文里真的出现了疑似主机 ID 的十六进制串才走逐 ID 替换：绝大多数活动日志
// 行根本不含主机 ID，原来却对每一行都跑 2 × 主机数 次 strings.ReplaceAll——
// 1000 行 × 5000 台就是一千万次整行扫描，每次 GET /activity 都来一遍。
func redactUserFacingTextSorted(text string, idToLabel map[string]string, ids []string) string {
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
	if len(ids) > 0 && hostIDHexRe.MatchString(t) {
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

// activityRedactor 是一次请求内复用的脱敏上下文：ID → 标签、按长度排好的 ID 列表、
// 以及主机名/标签 → 标签的反查表。
//
// 原来的按条脱敏对**每一条**日志：重排一次全部主机 ID，且 Host 字段是裸主机名
// （最常见的情况）时再 ListHosts 复制全部主机线性找一遍——1000 条 × 5000 台。
// 那条按条入口已随本次重构删除，调用方一律先建 redactor 再逐条 sanitize。
type activityRedactor struct {
	labels map[string]string
	ids    []string
	byName map[string]string
}

func (s *Server) newActivityRedactor(idToLabel map[string]string) *activityRedactor {
	rd := &activityRedactor{labels: idToLabel, ids: sortedRedactIDs(idToLabel), byName: map[string]string{}}
	if s != nil && s.store != nil {
		for _, h := range s.store.ListHosts() {
			if h == nil {
				continue
			}
			lab := hostDisplayLabelFromHost(h)
			if h.Hostname != "" {
				if _, dup := rd.byName[h.Hostname]; !dup {
					rd.byName[h.Hostname] = lab
				}
			}
			rd.byName[lab] = lab
		}
	}
	return rd
}

func (rd *activityRedactor) sanitize(e LogEntry) LogEntry {
	e.Message = redactUserFacingTextSorted(e.Message, rd.labels, rd.ids)
	if e.Host != "" {
		if lab, ok := rd.labels[e.Host]; ok {
			e.Host = lab
		} else if lab, ok := rd.byName[e.Host]; ok {
			// Host may already be a bare hostname — enrich with IP when possible.
			e.Host = lab
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
