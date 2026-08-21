package main

import (
	"net/http"
	"strings"
)

// Host/folder-scoped RBAC: empty AllowedFolderIDs means unrestricted (legacy).
// When set, the user may only see/act on hosts assigned to those folders
// (or nested under them). Admins always bypass.

func (u AccountConfig) hostScopeRestricted() bool {
	return len(u.AllowedFolderIDs) > 0 || len(u.AllowedHostIDs) > 0 || len(u.AllowedTags) > 0
}

func (s *Server) userCanAccessHost(u AccountConfig, hostID string) bool {
	if roleRank(u.Role) >= roleRank(RoleAdmin) {
		return true
	}
	if !u.hostScopeRestricted() {
		return true
	}
	for _, id := range u.AllowedHostIDs {
		if id == hostID {
			return true
		}
	}
	h, ok := s.store.GetHost(hostID)
	if !ok {
		return false
	}
	if len(u.AllowedTags) > 0 {
		cat := strings.TrimSpace(h.Category)
		for _, t := range u.AllowedTags {
			if strings.EqualFold(strings.TrimSpace(t), cat) {
				return true
			}
		}
	}
	if len(u.AllowedFolderIDs) == 0 {
		// Only host-id / tag rules applied above.
		return len(u.AllowedHostIDs) > 0 && containsStr(u.AllowedHostIDs, hostID)
	}
	assign := s.cfg.HostFolderAssign()
	folderID := assign[hostID]
	if folderID == "" {
		return false
	}
	allowed := map[string]bool{}
	for _, fid := range u.AllowedFolderIDs {
		allowed[fid] = true
		for _, child := range s.cfg.FolderDescendantIDs(fid) {
			allowed[child] = true
		}
	}
	return allowed[folderID]
}

func (s *Server) filterHostsForUser(r *http.Request, hosts []*Host) []*Host {
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return hosts
	}
	out := make([]*Host, 0, len(hosts))
	for _, h := range hosts {
		if h != nil && s.userCanAccessHost(u, h.ID) {
			out = append(out, h)
		}
	}
	return out
}

func (s *Server) requireHostAccess(w http.ResponseWriter, r *http.Request, hostID string) bool {
	u, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	if s.userCanAccessHost(u, hostID) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权访问该主机（主机组/标签授权）"})
	return false
}

// filterAlertsForUser drops alerts whose HostID is outside the caller's scope.
// Alerts without a host_id (global checks) are kept for scoped users only when
// they are not host-bound; host-bound entries are always filtered.
func (s *Server) filterAlertsForUser(r *http.Request, alerts []Alert) []Alert {
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return alerts
	}
	out := make([]Alert, 0, len(alerts))
	for _, a := range alerts {
		if a.HostID == "" || s.userCanAccessHost(u, a.HostID) {
			out = append(out, a)
		}
	}
	return out
}

// filterAlertRecordsForUser mirrors filterAlertsForUser for persistent history.
func (s *Server) filterAlertRecordsForUser(r *http.Request, records []AlertRecord) []AlertRecord {
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return records
	}
	out := make([]AlertRecord, 0, len(records))
	for _, rec := range records {
		if rec.HostID == "" || s.userCanAccessHost(u, rec.HostID) {
			out = append(out, rec)
		}
	}
	return out
}

// filterInventoryRows keeps only inventory maps whose host_id the caller may access.
func (s *Server) filterInventoryRows(r *http.Request, rows []map[string]any) []map[string]any {
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return rows
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		hid, _ := row["host_id"].(string)
		if hid != "" && s.userCanAccessHost(u, hid) {
			out = append(out, row)
		}
	}
	return out
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// HostFolderAssign returns a copy of hostID → folderID map.
func (cs *ConfigStore) HostFolderAssign() map[string]string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make(map[string]string, len(cs.cfg.HostFolderAssign))
	for k, v := range cs.cfg.HostFolderAssign {
		out[k] = v
	}
	return out
}

// FolderDescendantIDs returns all folder IDs under root (not including root).
func (cs *ConfigStore) FolderDescendantIDs(root string) []string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	n := findFolderNode(cs.cfg.HostFolders, root)
	if n == nil {
		return nil
	}
	var out []string
	var walk func([]HostFolderNode)
	walk = func(list []HostFolderNode) {
		for _, c := range list {
			out = append(out, c.ID)
			walk(c.Children)
		}
	}
	walk(n.Children)
	return out
}

// hostLogVisibility 返回一个"这台主机的日志能不能给这个人看"的谓词，不受限时返回 nil
// （调用方据此跳过过滤，零开销）。
//
// 与 filterHostsForUser 用同一套判定：解析不出用户、未设限、管理员，三种情况都不过滤。
// 单独抽出来是因为日志检索要在**环形缓冲的扫描循环里**逐条问一次，不能每条都去解析会话。
func (s *Server) hostLogVisibility(r *http.Request) func(string) bool {
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return nil
	}
	// 结果缓存在闭包里：一次检索会对同一批 host_id 反复提问，而 userCanAccessHost
	// 每次都要查主机、查分组、展开子分组，逐条现算会把日志检索拖慢一个量级。
	cache := map[string]bool{}
	return func(hostID string) bool {
		if v, hit := cache[hostID]; hit {
			return v
		}
		v := s.userCanAccessHost(u, hostID)
		cache[hostID] = v
		return v
	}
}
