package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HostFolderUngroupedID is the virtual folder for hosts with no assignment.
const HostFolderUngroupedID = "__ungrouped__"

// MaxHostFolderDepth is the maximum nesting level (root = 1). It is a high
// safety cap only (guards against pathological/abusive trees and deep
// recursion) — operators can nest folders as deeply as they need in practice.
const MaxHostFolderDepth = 32

// HostFolderNode is one folder in the host organization tree.
type HostFolderNode struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Children []HostFolderNode `json:"children,omitempty"`
}

func newHostFolderID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("hf-%d", time.Now().UnixNano())
	}
	return "hf-" + hex.EncodeToString(b)
}

func sanitizeFolderName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)
	if rs := []rune(s); len(rs) > 48 {
		s = string(rs[:48])
	}
	return strings.TrimSpace(s)
}

func validateFolderTree(nodes []HostFolderNode) error {
	seen := map[string]struct{}{}
	var walk func([]HostFolderNode, int) error
	walk = func(list []HostFolderNode, depth int) error {
		for _, n := range list {
			if depth > MaxHostFolderDepth {
				return fmt.Errorf("host folder depth exceeds %d", MaxHostFolderDepth)
			}
			if n.ID == "" || n.ID == HostFolderUngroupedID {
				return fmt.Errorf("invalid folder id")
			}
			if _, ok := seen[n.ID]; ok {
				return fmt.Errorf("duplicate folder id %s", n.ID)
			}
			seen[n.ID] = struct{}{}
			if sanitizeFolderName(n.Name) == "" {
				return fmt.Errorf("empty folder name")
			}
			if err := walk(n.Children, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(nodes, 1)
}

func findFolderNode(nodes []HostFolderNode, id string) *HostFolderNode {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
		if c := findFolderNode(nodes[i].Children, id); c != nil {
			return c
		}
	}
	return nil
}

func folderPathMap(nodes []HostFolderNode) map[string]string {
	out := map[string]string{}
	var walk func([]HostFolderNode, string)
	walk = func(list []HostFolderNode, prefix string) {
		for _, n := range list {
			p := n.Name
			if prefix != "" {
				p = prefix + " / " + n.Name
			}
			out[n.ID] = p
			walk(n.Children, p)
		}
	}
	walk(nodes, "")
	return out
}

func folderSubtreeIDs(root HostFolderNode) []string {
	var ids []string
	var walk func(HostFolderNode)
	walk = func(n HostFolderNode) {
		ids = append(ids, n.ID)
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return ids
}

func removeFolderNode(nodes []HostFolderNode, id string) (out []HostFolderNode, removed *HostFolderNode, parentID string, ok bool) {
	for i := range nodes {
		if nodes[i].ID == id {
			rm := nodes[i]
			out = append(append([]HostFolderNode{}, nodes[:i]...), nodes[i+1:]...)
			return out, &rm, "", true
		}
		children, rm, pid, found := removeFolderNode(nodes[i].Children, id)
		if found {
			nodes[i].Children = children
			if pid == "" {
				pid = nodes[i].ID
			}
			return nodes, rm, pid, true
		}
	}
	return nodes, nil, "", false
}

func addChildFolder(nodes []HostFolderNode, parentID string, child HostFolderNode) ([]HostFolderNode, error) {
	if parentID == "" || parentID == HostFolderUngroupedID {
		nodes = append(nodes, child)
		if err := validateFolderTree(nodes); err != nil {
			return nil, err
		}
		return nodes, nil
	}
	var add func([]HostFolderNode, int) ([]HostFolderNode, bool, error)
	add = func(list []HostFolderNode, depth int) ([]HostFolderNode, bool, error) {
		for i := range list {
			if list[i].ID == parentID {
				if depth+1 > MaxHostFolderDepth {
					return list, false, fmt.Errorf("host folder depth exceeds %d", MaxHostFolderDepth)
				}
				list[i].Children = append(list[i].Children, child)
				return list, true, nil
			}
			ch, found, err := add(list[i].Children, depth+1)
			if err != nil {
				return list, false, err
			}
			if found {
				list[i].Children = ch
				return list, true, nil
			}
		}
		return list, false, nil
	}
	out, found, err := add(nodes, 1)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("parent folder not found")
	}
	return out, nil
}

func renameFolderNode(nodes []HostFolderNode, id, name string) ([]HostFolderNode, bool) {
	for i := range nodes {
		if nodes[i].ID == id {
			nodes[i].Name = name
			return nodes, true
		}
		ch, ok := renameFolderNode(nodes[i].Children, id, name)
		if ok {
			nodes[i].Children = ch
			return nodes, true
		}
	}
	return nodes, false
}

func findL1FolderByName(nodes []HostFolderNode, name string) *HostFolderNode {
	for i := range nodes {
		if nodes[i].Name == name {
			return &nodes[i]
		}
	}
	return nil
}

func sanitizeFolderTreeNames(nodes []HostFolderNode) []HostFolderNode {
	out := make([]HostFolderNode, len(nodes))
	for i, n := range nodes {
		out[i] = HostFolderNode{
			ID:       n.ID,
			Name:     sanitizeFolderName(n.Name),
			Children: sanitizeFolderTreeNames(n.Children),
		}
	}
	return out
}

// ensureHostFoldersMigrated builds L1 folders from existing categories when the
// tree has never been initialized (nil HostFolders). Also places any host that
// still has a category but no folder assignment into a matching L1 folder.
func (cs *ConfigStore) ensureHostFoldersMigrated(hosts []*Host) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	dirty := false
	if cs.cfg.Categories == nil {
		cs.cfg.Categories = map[string]string{}
	}
	if cs.cfg.HostFolderAssign == nil {
		cs.cfg.HostFolderAssign = map[string]string{}
		dirty = true
	}
	if cs.cfg.HostFolders == nil {
		folders := []HostFolderNode{}
		assign := map[string]string{}
		byName := map[string]string{}
		for _, h := range hosts {
			cat := ""
			if ov, ok := cs.cfg.Categories[h.ID]; ok {
				cat = strings.TrimSpace(ov)
			} else {
				cat = strings.TrimSpace(h.Category)
			}
			cat = sanitizeFolderName(cat)
			if cat == "" {
				continue
			}
			fid, ok := byName[cat]
			if !ok {
				fid = newHostFolderID()
				folders = append(folders, HostFolderNode{ID: fid, Name: cat})
				byName[cat] = fid
			}
			assign[h.ID] = fid
			cs.cfg.Categories[h.ID] = cat
		}
		cs.cfg.HostFolders = folders
		cs.cfg.HostFolderAssign = assign
		return true
	}
	// Incremental: hosts with a category but no folder assignment → L1 find-or-create.
	// Skip when already assigned (including explicit __ungrouped__).
	for _, h := range hosts {
		if fid, ok := cs.cfg.HostFolderAssign[h.ID]; ok && fid != "" {
			continue
		}
		// Explicit empty category override means "stay ungrouped".
		if ov, ok := cs.cfg.Categories[h.ID]; ok && strings.TrimSpace(ov) == "" {
			cs.cfg.HostFolderAssign[h.ID] = HostFolderUngroupedID
			dirty = true
			continue
		}
		cat := ""
		if ov, ok := cs.cfg.Categories[h.ID]; ok {
			cat = sanitizeFolderName(ov)
		} else {
			cat = sanitizeFolderName(h.Category)
		}
		if cat == "" {
			continue
		}
		n := findL1FolderByName(cs.cfg.HostFolders, cat)
		if n == nil {
			cs.cfg.HostFolders = append(cs.cfg.HostFolders, HostFolderNode{ID: newHostFolderID(), Name: cat})
			n = &cs.cfg.HostFolders[len(cs.cfg.HostFolders)-1]
		}
		cs.cfg.HostFolderAssign[h.ID] = n.ID
		cs.cfg.Categories[h.ID] = n.Name
		dirty = true
	}
	return dirty
}

func (cs *ConfigStore) hostFoldersSnapshot() (folders []HostFolderNode, assign map[string]string) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	folders = append([]HostFolderNode(nil), cs.cfg.HostFolders...)
	assign = map[string]string{}
	for k, v := range cs.cfg.HostFolderAssign {
		assign[k] = v
	}
	return folders, assign
}

func (cs *ConfigStore) setHostFoldersTree(folders []HostFolderNode) error {
	folders = sanitizeFolderTreeNames(folders)
	if err := validateFolderTree(folders); err != nil {
		return err
	}
	cs.mu.Lock()
	valid := map[string]struct{}{}
	var mark func([]HostFolderNode)
	mark = func(list []HostFolderNode) {
		for _, n := range list {
			valid[n.ID] = struct{}{}
			mark(n.Children)
		}
	}
	mark(folders)
	if cs.cfg.HostFolderAssign == nil {
		cs.cfg.HostFolderAssign = map[string]string{}
	}
	if cs.cfg.Categories == nil {
		cs.cfg.Categories = map[string]string{}
	}
	for hid, fid := range cs.cfg.HostFolderAssign {
		if _, ok := valid[fid]; !ok {
			delete(cs.cfg.HostFolderAssign, hid)
			delete(cs.cfg.Categories, hid)
		} else if n := findFolderNode(folders, fid); n != nil {
			cs.cfg.Categories[hid] = n.Name
		}
	}
	cs.cfg.HostFolders = folders
	cs.mu.Unlock()
	return cs.save()
}

func (cs *ConfigStore) addHostFolder(parentID, name string) (HostFolderNode, error) {
	name = sanitizeFolderName(name)
	if name == "" {
		return HostFolderNode{}, fmt.Errorf("empty folder name")
	}
	child := HostFolderNode{ID: newHostFolderID(), Name: name}
	cs.mu.Lock()
	if cs.cfg.HostFolders == nil {
		cs.cfg.HostFolders = []HostFolderNode{}
	}
	if cs.cfg.HostFolderAssign == nil {
		cs.cfg.HostFolderAssign = map[string]string{}
	}
	out, err := addChildFolder(cs.cfg.HostFolders, parentID, child)
	if err != nil {
		cs.mu.Unlock()
		return HostFolderNode{}, err
	}
	cs.cfg.HostFolders = out
	cs.mu.Unlock()
	if err := cs.save(); err != nil {
		return HostFolderNode{}, err
	}
	return child, nil
}

func (cs *ConfigStore) renameHostFolder(id, name string) error {
	name = sanitizeFolderName(name)
	if name == "" {
		return fmt.Errorf("empty folder name")
	}
	if id == "" || id == HostFolderUngroupedID {
		return fmt.Errorf("cannot rename this folder")
	}
	cs.mu.Lock()
	out, ok := renameFolderNode(cs.cfg.HostFolders, id, name)
	if !ok {
		cs.mu.Unlock()
		return fmt.Errorf("folder not found")
	}
	cs.cfg.HostFolders = out
	if cs.cfg.Categories == nil {
		cs.cfg.Categories = map[string]string{}
	}
	for hid, fid := range cs.cfg.HostFolderAssign {
		if fid == id {
			cs.cfg.Categories[hid] = name
		}
	}
	cs.mu.Unlock()
	return cs.save()
}

func (cs *ConfigStore) deleteHostFolder(id string) error {
	if id == "" || id == HostFolderUngroupedID {
		return fmt.Errorf("cannot delete this folder")
	}
	cs.mu.Lock()
	out, removed, parentID, ok := removeFolderNode(cs.cfg.HostFolders, id)
	if !ok || removed == nil {
		cs.mu.Unlock()
		return fmt.Errorf("folder not found")
	}
	subtree := folderSubtreeIDs(*removed)
	drop := map[string]struct{}{}
	for _, sid := range subtree {
		drop[sid] = struct{}{}
	}
	target := parentID
	if cs.cfg.Categories == nil {
		cs.cfg.Categories = map[string]string{}
	}
	for hid, fid := range cs.cfg.HostFolderAssign {
		if _, hit := drop[fid]; !hit {
			continue
		}
		if target == "" {
			// Explicit ungrouped sentinel — avoid bounce-back from Agent.Category.
			cs.cfg.HostFolderAssign[hid] = HostFolderUngroupedID
			cs.cfg.Categories[hid] = ""
		} else {
			cs.cfg.HostFolderAssign[hid] = target
			if n := findFolderNode(out, target); n != nil {
				cs.cfg.Categories[hid] = n.Name
			}
		}
	}
	cs.cfg.HostFolders = out
	cs.mu.Unlock()
	return cs.save()
}

// applyAgentFolderHint places a host from Agent report: valid folder_id wins
// (any tree depth or explicit ungrouped); otherwise legacy category creates/joins L1
// only when the host still has no HostFolderAssign entry.
func (cs *ConfigStore) applyAgentFolderHint(hostID, folderID, category string) error {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return nil
	}
	folderID = sanitizeFolderID(folderID)
	if folderID != "" {
		if folderID == HostFolderUngroupedID {
			return cs.assignHostFolder(hostID, HostFolderUngroupedID)
		}
		cs.mu.RLock()
		ok := findFolderNode(cs.cfg.HostFolders, folderID) != nil
		cs.mu.RUnlock()
		if ok {
			return cs.assignHostFolder(hostID, folderID)
		}
		// Deleted / unknown id → fall through to category L1 migration.
	}
	category = sanitizeFolderName(category)
	if category == "" {
		return nil
	}
	cs.mu.RLock()
	_, hasAssign := cs.cfg.HostFolderAssign[hostID]
	cs.mu.RUnlock()
	if hasAssign {
		return nil
	}
	return cs.setCategoryWithFolder(hostID, category)
}

func (cs *ConfigStore) assignHostFolder(hostID, folderID string) error {
	cs.mu.Lock()
	if cs.cfg.HostFolders == nil {
		cs.cfg.HostFolders = []HostFolderNode{}
	}
	if cs.cfg.HostFolderAssign == nil {
		cs.cfg.HostFolderAssign = map[string]string{}
	}
	if cs.cfg.Categories == nil {
		cs.cfg.Categories = map[string]string{}
	}
	if folderID == "" || folderID == HostFolderUngroupedID {
		// Explicit ungrouped: keep a sentinel assignment so ensureHostFoldersMigrated
		// will not re-file the host from a lingering Agent category string.
		cs.cfg.HostFolderAssign[hostID] = HostFolderUngroupedID
		cs.cfg.Categories[hostID] = ""
		cs.mu.Unlock()
		return cs.save()
	}
	n := findFolderNode(cs.cfg.HostFolders, folderID)
	if n == nil {
		cs.mu.Unlock()
		return fmt.Errorf("folder not found")
	}
	cs.cfg.HostFolderAssign[hostID] = folderID
	cs.cfg.Categories[hostID] = n.Name
	cs.mu.Unlock()
	return cs.save()
}

// setCategoryWithFolder syncs the legacy category API into L1 folders.
func (cs *ConfigStore) setCategoryWithFolder(hostID, cat string) error {
	cat = sanitizeFolderName(cat)
	if cat == "" {
		return cs.assignHostFolder(hostID, HostFolderUngroupedID)
	}
	cs.mu.Lock()
	if cs.cfg.HostFolders == nil {
		cs.cfg.HostFolders = []HostFolderNode{}
	}
	if cs.cfg.HostFolderAssign == nil {
		cs.cfg.HostFolderAssign = map[string]string{}
	}
	if cs.cfg.Categories == nil {
		cs.cfg.Categories = map[string]string{}
	}
	n := findL1FolderByName(cs.cfg.HostFolders, cat)
	if n == nil {
		cs.cfg.HostFolders = append(cs.cfg.HostFolders, HostFolderNode{ID: newHostFolderID(), Name: cat})
		n = &cs.cfg.HostFolders[len(cs.cfg.HostFolders)-1]
	}
	fid := n.ID
	name := n.Name
	cs.cfg.HostFolderAssign[hostID] = fid
	cs.cfg.Categories[hostID] = name
	cs.mu.Unlock()
	return cs.save()
}

func (cs *ConfigStore) hostFolderOf(hostID string) string {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if cs.cfg.HostFolderAssign == nil {
		return HostFolderUngroupedID
	}
	if fid, ok := cs.cfg.HostFolderAssign[hostID]; ok && fid != "" {
		return fid
	}
	return HostFolderUngroupedID
}

// --- HTTP handlers ---

type folderCountView struct {
	Total  int `json:"total"`
	Online int `json:"online"`
}

func (s *Server) handleGetHostFolders(w http.ResponseWriter, r *http.Request) {
	hosts := s.filterHostsForUser(r, s.store.ListHosts())
	if s.cfg.ensureHostFoldersMigrated(s.store.ListHosts()) {
		_ = s.cfg.save()
	}
	folders, assignAll := s.cfg.hostFoldersSnapshot()
	paths := folderPathMap(folders)
	offlineAfter := int64(s.cfg.Thresholds().OfflineAfter.Seconds())
	now := time.Now().Unix()
	counts := map[string]*folderCountView{HostFolderUngroupedID: {}}
	var initCounts func([]HostFolderNode)
	initCounts = func(list []HostFolderNode) {
		for _, n := range list {
			counts[n.ID] = &folderCountView{}
			initCounts(n.Children)
		}
	}
	initCounts(folders)

	// Scoped callers only receive assignments for hosts they may access — the full
	// assign map would otherwise leak every host_id in the fleet.
	assign := map[string]string{}
	for _, h := range hosts {
		fid := assignAll[h.ID]
		if fid == "" {
			fid = HostFolderUngroupedID
		}
		assign[h.ID] = fid
		c := counts[fid]
		if c == nil {
			c = counts[HostFolderUngroupedID]
		}
		c.Total++
		if now-h.LastSeen <= offlineAfter {
			c.Online++
		}
	}
	// Roll up: selection includes descendants, so badge counts must too —
	// otherwise a parent shows (0) while the right pane lists child hosts.
	direct := make(map[string]folderCountView, len(counts))
	for id, c := range counts {
		if c != nil {
			direct[id] = *c
		}
	}
	var rollupNode func(HostFolderNode) (int, int)
	rollupNode = func(n HostFolderNode) (int, int) {
		t, o := 0, 0
		if d, ok := direct[n.ID]; ok {
			t, o = d.Total, d.Online
		}
		for _, ch := range n.Children {
			ct, co := rollupNode(ch)
			t += ct
			o += co
		}
		counts[n.ID] = &folderCountView{Total: t, Online: o}
		return t, o
	}
	for _, n := range folders {
		rollupNode(n)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"folders": folders,
		"assign":  assign,
		"paths":   paths,
		"counts":  counts,
	})
}

func (s *Server) handlePutHostFolders(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folders []HostFolderNode `json:"folders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.setHostFoldersTree(req.Folders); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r), Message: "host folders tree saved"})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handlePostHostFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	node, err := s.cfg.addHostFolder(req.ParentID, req.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: "add host folder: " + node.Name})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "folder": node})
}

func (s *Server) handlePatchHostFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.renameHostFolder(id, req.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: "rename host folder: " + id})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleDeleteHostFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.cfg.deleteHostFolder(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: "delete host folder: " + id})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleSetHostFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireHostAccess(w, r, id) {
		return
	}
	var req struct {
		FolderID string `json:"folder_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.assignHostFolder(id, req.FolderID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	label := s.hostLabelForID(id)
	folderName := "未分组"
	if req.FolderID != "" {
		folders, _ := s.cfg.hostFoldersSnapshot()
		paths := folderPathMap(folders)
		if p := paths[req.FolderID]; p != "" {
			folderName = p
		} else {
			folderName = "未知分组"
		}
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Host: label,
		Message: Tz("log.set_category", label, folderName)})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "host_id": id, "folder_id": req.FolderID})
}
