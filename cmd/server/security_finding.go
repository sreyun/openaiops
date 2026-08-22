package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	findingStatusOpen          = "open"
	findingStatusAck           = "ack"
	findingStatusFalsePositive = "false_positive"
	findingStatusResolved      = "resolved"
)

var validFindingStatuses = map[string]bool{
	findingStatusOpen: true, findingStatusAck: true,
	findingStatusFalsePositive: true, findingStatusResolved: true,
}

// SecurityFindingState tracks operator disposition for a stable finding key.
type SecurityFindingState struct {
	Key       string `json:"key"`
	Scope     string `json:"scope"` // host|web
	Status    string `json:"status"`
	Note      string `json:"note,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

type securityFindingManager struct {
	mu     sync.Mutex
	states map[string]SecurityFindingState
	dir    string
}

func newSecurityFindingManager(dir string) *securityFindingManager {
	m := &securityFindingManager{states: map[string]SecurityFindingState{}, dir: dir}
	m.load()
	return m
}

func (m *securityFindingManager) path() string {
	return filepath.Join(m.dir, "finding_states.json")
}

func (m *securityFindingManager) load() {
	if m.dir == "" {
		return
	}
	b, err := os.ReadFile(m.path())
	if err != nil {
		return
	}
	var list []SecurityFindingState
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, st := range list {
		if st.Key != "" {
			m.states[st.Key] = st
		}
	}
}

func (m *securityFindingManager) saveLocked() {
	if m.dir == "" {
		return
	}
	list := make([]SecurityFindingState, 0, len(m.states))
	for _, st := range m.states {
		list = append(list, st)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	b, err := json.Marshal(list)
	if err != nil {
		slog.Warn("finding states marshal failed", "err", err)
		return
	}
	_ = os.MkdirAll(m.dir, 0o750)
	tmp := m.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		slog.Warn("finding states write failed", "err", err)
		return
	}
	if err := os.Rename(tmp, m.path()); err != nil {
		slog.Warn("finding states rename failed", "err", err)
	}
}

func shortDiscHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(p))))
		_, _ = h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:6])
}

// hostFindingKey uniquely identifies a host finding. Discriminator (package/detail)
// prevents ClamAV/IOC/CVE-on-different-packages from collapsing to one status key.
func hostFindingKey(hostID string, f HostFinding) string {
	hostID = strings.TrimSpace(hostID)
	id := strings.TrimSpace(f.ID)
	if id == "" {
		id = strings.TrimSpace(f.CVE)
	}
	if id == "" {
		id = strings.TrimSpace(f.Title)
	}
	disc := strings.TrimSpace(f.Package)
	if disc == "" {
		disc = strings.TrimSpace(f.Detail)
	}
	if disc == "" {
		disc = strings.TrimSpace(f.Title)
	}
	return fmt.Sprintf("host:%s:%s:%s:%s", hostID, strings.TrimSpace(f.Category), id, shortDiscHash(disc))
}

// hostFindingKeyLegacy is the pre-discriminator key for dual-read migration.
func hostFindingKeyLegacy(hostID string, f HostFinding) string {
	hostID = strings.TrimSpace(hostID)
	id := strings.TrimSpace(f.ID)
	if id == "" {
		id = strings.TrimSpace(f.CVE)
	}
	if id == "" {
		id = strings.TrimSpace(f.Title)
	}
	return fmt.Sprintf("host:%s:%s:%s", hostID, strings.TrimSpace(f.Category), id)
}

// webFindingKey uniquely identifies a web finding including matcher-name so
// multi-matcher templates (missing security headers) can be dispositioned separately.
func webFindingKey(targetID, templateID, url, matcher string) string {
	targetID = strings.TrimSpace(targetID)
	templateID = strings.TrimSpace(templateID)
	url = strings.ToLower(strings.TrimSpace(url))
	matcher = strings.ToLower(strings.TrimSpace(matcher))
	sum := sha256.Sum256([]byte(url + "\x00" + matcher))
	return fmt.Sprintf("web:%s:%s:%s", targetID, templateID, hex.EncodeToString(sum[:8]))
}

func webFindingKeyLegacy(targetID, templateID, url string) string {
	targetID = strings.TrimSpace(targetID)
	templateID = strings.TrimSpace(templateID)
	url = strings.ToLower(strings.TrimSpace(url))
	sum := sha256.Sum256([]byte(url))
	return fmt.Sprintf("web:%s:%s:%s", targetID, templateID, hex.EncodeToString(sum[:6]))
}

func (m *securityFindingManager) get(key string) (SecurityFindingState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[key]
	return st, ok
}

func (m *securityFindingManager) list(scope string) []SecurityFindingState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SecurityFindingState, 0, len(m.states))
	for _, st := range m.states {
		if scope == "" || st.Scope == scope {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func (m *securityFindingManager) upsert(key, scope, status, note, actor string) (SecurityFindingState, error) {
	key = strings.TrimSpace(key)
	status = strings.TrimSpace(status)
	if key == "" {
		return SecurityFindingState{}, fmt.Errorf("key required")
	}
	if !validFindingStatuses[status] {
		return SecurityFindingState{}, fmt.Errorf("invalid status")
	}
	st := SecurityFindingState{
		Key: key, Scope: scope, Status: status, Note: strings.TrimSpace(note),
		UpdatedAt: time.Now().Unix(), UpdatedBy: actor,
	}
	m.mu.Lock()
	m.states[key] = st
	m.saveLocked()
	m.mu.Unlock()
	return st, nil
}

func mergeHostFindingStatus(m *securityFindingManager, hostID string, findings []HostFinding) []HostFinding {
	if m == nil || len(findings) == 0 {
		return findings
	}
	out := make([]HostFinding, len(findings))
	copy(out, findings)
	for i := range out {
		key := hostFindingKey(hostID, out[i])
		if st, ok := m.get(key); ok {
			out[i].Status = st.Status
			if st.Note != "" {
				out[i].StatusNote = st.Note
			}
			continue
		}
		// Dual-read legacy key (no discriminator).
		if st, ok := m.get(hostFindingKeyLegacy(hostID, out[i])); ok {
			out[i].Status = st.Status
			if st.Note != "" {
				out[i].StatusNote = st.Note
			}
			continue
		}
		out[i].Status = findingStatusOpen
	}
	return out
}

func mergeWebFindingStatus(m *securityFindingManager, targetID string, findings []WebFinding) []WebFinding {
	if m == nil || len(findings) == 0 {
		return findings
	}
	out := make([]WebFinding, len(findings))
	copy(out, findings)
	for i := range out {
		url := out[i].URL
		if url == "" {
			url = out[i].MatchedAt
		}
		key := webFindingKey(targetID, out[i].TemplateID, url, out[i].MatcherName)
		if st, ok := m.get(key); ok {
			out[i].Status = st.Status
			if st.Note != "" {
				out[i].StatusNote = st.Note
			}
			continue
		}
		if out[i].MatcherName != "" {
			if st, ok := m.get(webFindingKeyLegacy(targetID, out[i].TemplateID, url)); ok {
				out[i].Status = st.Status
				if st.Note != "" {
					out[i].StatusNote = st.Note
				}
				continue
			}
		}
		out[i].Status = findingStatusOpen
	}
	return out
}

func (s *Server) handleListSecurityFindingStates(w http.ResponseWriter, r *http.Request) {
	if s.secFindings == nil {
		writeJSON(w, http.StatusOK, map[string]any{"states": []SecurityFindingState{}})
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	writeJSON(w, http.StatusOK, map[string]any{"states": s.secFindings.list(scope)})
}

func (s *Server) handleUpdateSecurityFindingState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key      string `json:"key"`
		Scope    string `json:"scope"`
		Status   string `json:"status"`
		Note     string `json:"note"`
		HostID   string `json:"host_id"`
		TargetID string `json:"target_id"`
		Finding  struct {
			ID          string `json:"id"`
			Category    string `json:"category"`
			CVE         string `json:"cve"`
			Title       string `json:"title"`
			Detail      string `json:"detail"`
			Package     string `json:"package"`
			TemplateID  string `json:"template_id"`
			URL         string `json:"url"`
			MatcherName string `json:"matcher_name"`
		} `json:"finding"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSecErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if s.secFindings == nil {
		writeSecErr(w, http.StatusServiceUnavailable, "finding store unavailable")
		return
	}
	// 标记"已忽略/已修复"会改变这台主机的安全结论，属于写操作：
	// 主机组授权受限的账号不能替范围外的主机下这个结论。
	if strings.TrimSpace(req.HostID) != "" && !s.requireHostAccess(w, r, req.HostID) {
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		switch strings.TrimSpace(req.Scope) {
		case "host":
			key = hostFindingKey(req.HostID, HostFinding{
				ID: req.Finding.ID, Category: req.Finding.Category,
				CVE: req.Finding.CVE, Title: req.Finding.Title,
				Detail: req.Finding.Detail, Package: req.Finding.Package,
			})
		case "web":
			key = webFindingKey(req.TargetID, req.Finding.TemplateID, req.Finding.URL, req.Finding.MatcherName)
		default:
			writeSecErr(w, http.StatusBadRequest, "scope or key required")
			return
		}
	}
	st, err := s.secFindings.upsert(key, req.Scope, req.Status, req.Note, s.actorName(r))
	if err != nil {
		writeSecErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.store.AddLog(LogEntry{
		Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: fmt.Sprintf("security finding %s → %s", key, st.Status),
	})
	writeJSON(w, http.StatusOK, st)
}
