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

// requireForwardRuleAccess resolves a forwarding rule by id and enforces the
// caller's host scope on it.
//
// 端口转发规则本身就是一条**通往某台主机的网络通路**：把规则改到别的主机、复制一份、
// 或者把停用的规则重新打开，等价于给自己开一条到那台机器的隧道。此前只有"新建"
// 这一个入口检查了主机授权（forward_api.go 的 handleForwardCreate / handleHTTPProxyCreate），
// 编辑 / 删除 / 启停 / 复制、以及整组操作全都没检查——被主机组授权限制住的 operator
// 可以照样操作范围外主机的规则。
func (s *Server) requireForwardRuleAccess(w http.ResponseWriter, r *http.Request, ruleID string) (*forwardRule, bool) {
	rule := s.forward.getRule(ruleID)
	if rule == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "forward.rule_not_found")})
		return nil, false
	}
	if !s.requireHostAccess(w, r, rule.hostID) {
		return nil, false
	}
	return rule, true
}

// requireForwardGroupAccess enforces the caller's host scope on every rule in a
// port-range group. 整组操作里只要有一条落在授权范围外就整体拒绝——放行"部分成功"
// 只会让人以为组里剩下的规则不存在。
func (s *Server) requireForwardGroupAccess(w http.ResponseWriter, r *http.Request, gid string) bool {
	u, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	if !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return true
	}
	for _, id := range s.forward.groupRuleIDs(gid) {
		if rule := s.forward.getRule(id); rule != nil && !s.userCanAccessHost(u, rule.hostID) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权访问该主机（主机组/标签授权）"})
			return false
		}
	}
	return true
}

// requireHTTPProxyAccess mirrors requireForwardRuleAccess for HTTP proxy entries.
func (s *Server) requireHTTPProxyAccess(w http.ResponseWriter, r *http.Request, proxyID string) bool {
	for _, p := range s.cfg.ListHTTPProxies() {
		if p.ID == proxyID {
			return s.requireHostAccess(w, r, p.HostID)
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	return false
}

// filterForwardRulesForUser drops rules whose host is outside the caller's scope.
// 与 handleHTTPProxyList 的既有做法一致：列表本身就会暴露"有哪些主机、开了哪些端口"。
func (s *Server) filterForwardRulesForUser(r *http.Request, rules []forwardInfo) []forwardInfo {
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return rules
	}
	out := make([]forwardInfo, 0, len(rules))
	for _, rule := range rules {
		if s.userCanAccessHost(u, rule.HostID) {
			out = append(out, rule)
		}
	}
	return out
}

// requireIncidentAccess enforces the caller's host scope on one incident.
//
// SRE 事件挂在具体主机上（HostID），而事件详情、AI 诊断、闭环操作会把这台主机的
// 指标、日志乃至终端摘要一并拉出来——对被主机组/标签限制住的账号，这等于绕开授权
// 读取范围外主机的运行数据。没有主机归属的平台级事件（HostID 为空）照常放行，
// 与 filterAlertsForUser 对无主机告警的处理一致。
func (s *Server) requireIncidentAccess(w http.ResponseWriter, r *http.Request, hostID string) bool {
	if strings.TrimSpace(hostID) == "" {
		return true
	}
	return s.requireHostAccess(w, r, hostID)
}

// filterIncidentsForUser drops host-bound incidents outside the caller's scope.
func (s *Server) filterIncidentsForUser(r *http.Request, list []Incident) []Incident {
	u, ok := s.currentUser(r)
	if !ok || !u.hostScopeRestricted() || roleRank(u.Role) >= roleRank(RoleAdmin) {
		return list
	}
	out := make([]Incident, 0, len(list))
	for _, inc := range list {
		if strings.TrimSpace(inc.HostID) == "" || s.userCanAccessHost(u, inc.HostID) {
			out = append(out, inc)
		}
	}
	return out
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
