package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
)

// HostFolderUngroupedID is the virtual folder for hosts with no assignment.
const HostFolderUngroupedID = "__ungrouped__"

// MaxHostFolderDepth is the maximum nesting level (root = 1). It is a high
// safety cap only (guards against pathological/abusive trees and deep
// recursion) — operators can nest folders as deeply as they need in practice.
const MaxHostFolderDepth = 32

// maxBatchFolderHosts 限制一次批量变更分组的主机数。上限存在的意义不是"多了会慢"，
// 而是一次误操作的爆炸半径：全选几千台点错分组，比逐台点错难收拾得多。
const maxBatchFolderHosts = 500

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

// folderSubtreeContains 报告 id 是否落在 root 子树内（含 root 自身）。
func folderSubtreeContains(root HostFolderNode, id string) bool {
	for _, sid := range folderSubtreeIDs(root) {
		if sid == id {
			return true
		}
	}
	return false
}

// moveFolderNode 把一个分组挂到新的父级下。
//
// 先摘后挂是天然的顺序，但顺序本身带着两个坑，必须在动手之前就挡住：
//   - 挂到自己（或自己的后代）下面：摘下来之后目标父级已经不在树里，addChildFolder 报
//     "parent not found"，而节点已经被摘走——整棵子树连同里面的主机分组就这么没了；
//   - 挂到一个深度快满的父级下：addChildFolder 只检查"父级+1"，不看被挂上去的子树自己
//     还有多高，超限要靠事后 validateFolderTree 兜住。
//
// 所以这里全程在副本上操作，任何一步失败都原样返回原树。
func moveFolderNode(nodes []HostFolderNode, id, newParentID string) ([]HostFolderNode, error) {
	if id == "" || id == HostFolderUngroupedID {
		return nodes, fmt.Errorf("cannot move this folder")
	}
	if newParentID == HostFolderUngroupedID {
		newParentID = "" // 「未分组」不是真分组，等同挂到根
	}
	if newParentID == id {
		return nodes, fmt.Errorf("cannot move a folder into itself")
	}
	node := findFolderNode(nodes, id)
	if node == nil {
		return nodes, fmt.Errorf("folder not found")
	}
	if newParentID != "" && folderSubtreeContains(*node, newParentID) {
		return nodes, fmt.Errorf("cannot move a folder into its own subtree")
	}
	if newParentID != "" && findFolderNode(nodes, newParentID) == nil {
		return nodes, fmt.Errorf("parent folder not found")
	}

	work := cloneFolderTree(nodes)
	work, removed, oldParent, ok := removeFolderNode(work, id)
	if !ok || removed == nil {
		return nodes, fmt.Errorf("folder not found")
	}
	if oldParent == newParentID {
		return nodes, nil // 没挪窝：保持原树，别白写一次配置
	}
	out, err := addChildFolder(work, newParentID, *removed)
	if err != nil {
		return nodes, err
	}
	if err := validateFolderTree(out); err != nil {
		return nodes, err
	}
	return out, nil
}

// cloneFolderTree 深拷贝一棵分组树。移动是"摘 + 挂"两步，中途失败必须能原样退回，
// 而 removeFolderNode 会就地改写切片里的 Children。
func cloneFolderTree(nodes []HostFolderNode) []HostFolderNode {
	if nodes == nil {
		return nil
	}
	out := make([]HostFolderNode, len(nodes))
	for i, n := range nodes {
		out[i] = HostFolderNode{ID: n.ID, Name: n.Name, Children: cloneFolderTree(n.Children)}
	}
	return out
}

// sortFolderTreeForDisplay 按「人读的顺序」排序：数字按数值比（srv2 在 srv10 前面），
// 其余按名称。只作用于**返回给界面的副本**——存储里保持创建顺序，这样前端整棵 PUT
// 回来时不会因为排序把用户手工摆的顺序洗掉。
func sortFolderTreeForDisplay(nodes []HostFolderNode) []HostFolderNode {
	out := cloneFolderTree(nodes)
	var walk func([]HostFolderNode)
	walk = func(list []HostFolderNode) {
		sort.SliceStable(list, func(i, j int) bool {
			return naturalLess(list[i].Name, list[j].Name)
		})
		for i := range list {
			walk(list[i].Children)
		}
	}
	walk(out)
	return out
}

// naturalLess 是「自然顺序」比较：把名字切成数字段与非数字段，数字段按数值比。
// 纯字典序会把 30-测试 排在 4-测试 前面，运维在几十个分组里找机房就得靠眼睛扫。
func naturalLess(a, b string) bool {
	ar, br := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ar) && j < len(br) {
		if isASCIIDigit(ar[i]) && isASCIIDigit(br[j]) {
			si, sj := i, j
			for i < len(ar) && isASCIIDigit(ar[i]) {
				i++
			}
			for j < len(br) && isASCIIDigit(br[j]) {
				j++
			}
			na := strings.TrimLeft(string(ar[si:i]), "0")
			nb := strings.TrimLeft(string(br[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) < len(nb) // 位数不同，长的更大（已去前导零）
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		ca, cb := unicode.ToLower(ar[i]), unicode.ToLower(br[j])
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	return len(ar)-i < len(br)-j
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

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

// moveHostFolder 改变分组的上级（层级调整）。名字不变，里面的主机跟着走。
func (cs *ConfigStore) moveHostFolder(id, newParentID string) error {
	cs.mu.Lock()
	out, err := moveFolderNode(cs.cfg.HostFolders, id, newParentID)
	if err != nil {
		cs.mu.Unlock()
		return err
	}
	cs.cfg.HostFolders = out
	cs.mu.Unlock()
	return cs.save()
}

// assignHostFoldersBatch 把一批主机一次性挪到同一个分组。
//
// 不是"循环调用 assignHostFolder"：那样每台机器都要落一次盘，几百台就是几百次配置写入，
// 中途失败还会留下一半改了一半没改的状态。这里一次加锁、一次校验、一次保存。
func (cs *ConfigStore) assignHostFoldersBatch(hostIDs []string, folderID string) error {
	if len(hostIDs) == 0 {
		return fmt.Errorf("no hosts given")
	}
	cs.mu.Lock()
	if cs.cfg.HostFolderAssign == nil {
		cs.cfg.HostFolderAssign = map[string]string{}
	}
	if cs.cfg.Categories == nil {
		cs.cfg.Categories = map[string]string{}
	}
	name := ""
	if folderID != "" && folderID != HostFolderUngroupedID {
		n := findFolderNode(cs.cfg.HostFolders, folderID)
		if n == nil {
			cs.mu.Unlock()
			return fmt.Errorf("folder not found")
		}
		name = n.Name
	} else {
		folderID = HostFolderUngroupedID
	}
	for _, hid := range hostIDs {
		if strings.TrimSpace(hid) == "" {
			continue
		}
		cs.cfg.HostFolderAssign[hid] = folderID
		cs.cfg.Categories[hid] = name
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
	hosts := s.store.ListHosts()
	if s.cfg.ensureHostFoldersMigrated(hosts) {
		_ = s.cfg.save()
	}
	folders, assign := s.cfg.hostFoldersSnapshot()
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

	for _, h := range hosts {
		fid := assign[h.ID]
		if fid == "" {
			fid = HostFolderUngroupedID
		}
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
	// 界面上按人读的顺序给：分组是按创建时间堆进去的，找一个机房要从头扫到尾。
	// 只排返回的副本——存储保持原顺序，前端整棵 PUT 回来时不会被这次排序改写。
	writeJSON(w, http.StatusOK, map[string]any{
		"folders": sortFolderTreeForDisplay(folders),
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

// handlePatchHostFolder 改名，并且（可选地）改上级。
//
// 两件事放在同一个请求里，是因为它们在界面上就是同一个动作：用户打开「重命名分组」，
// 既可能是改叫法，也可能是发现这个分组挂错了层级。parent_id 不传 = 不动层级（老客户端
// 的行为一字不变）。
func (s *Server) handlePatchHostFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name     string  `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if strings.TrimSpace(req.Name) != "" {
		if err := s.cfg.renameHostFolder(id, req.Name); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if req.ParentID != nil {
		if err := s.cfg.moveHostFolder(id, sanitizeFolderID(*req.ParentID)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if strings.TrimSpace(req.Name) == "" && req.ParentID == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name or parent_id required"})
		return
	}
	msg := "rename host folder: " + id
	if req.ParentID != nil {
		msg = "update host folder: " + id + " (parent=" + *req.ParentID + ")"
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: msg})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleSetHostFolderBatch 把一批主机一次挪到同一个分组。
//
// 逐台点是十几次弹窗、十几次刷新；这里一次请求、一次落盘、一条审计。权限按单台的同一
// 把尺子逐个校验，只要有一台越权就整批拒绝——批量操作最忌讳"改了一半"。
func (s *Server) handleSetHostFolderBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HostIDs  []string `json:"host_ids"`
		FolderID string   `json:"folder_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	ids := make([]string, 0, len(req.HostIDs))
	seen := map[string]struct{}{}
	for _, id := range req.HostIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_ids required"})
		return
	}
	if len(ids) > maxBatchFolderHosts {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("too many hosts in one batch (max %d)", maxBatchFolderHosts)})
		return
	}
	u, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	for _, id := range ids {
		if !s.userCanAccessHost(u, id) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "无权访问该主机（主机组/标签授权）: " + id})
			return
		}
	}
	if err := s.cfg.assignHostFoldersBatch(ids, sanitizeFolderID(req.FolderID)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	folderName := "未分组"
	if fid := sanitizeFolderID(req.FolderID); fid != "" && fid != HostFolderUngroupedID {
		folders, _ := s.cfg.hostFoldersSnapshot()
		if p := folderPathMap(folders)[fid]; p != "" {
			folderName = p
		} else {
			folderName = "未知分组"
		}
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info",
		Message: fmt.Sprintf("批量变更分组：%d 台主机 → %s", len(ids), folderName)})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(ids), "folder_id": req.FolderID})
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

// hostFolderPathMap 一次快照出「主机 → 完整分组路径」。告警要带分组时用它，避免每条告警
// 各自去锁一次配置。未分组的主机不出现在结果里（调用方按"没有路径"处理即可）。
func (cs *ConfigStore) hostFolderPathMap() map[string]string {
	folders, assign := cs.hostFoldersSnapshot()
	paths := folderPathMap(folders)
	out := make(map[string]string, len(assign))
	for hid, fid := range assign {
		if fid == "" || fid == HostFolderUngroupedID {
			continue
		}
		if p := paths[fid]; p != "" {
			out[hid] = p
		}
	}
	return out
}

// decorateAlertGroups 给一批告警补上完整分组路径。
//
// 为什么是"完整路径"而不是一级分类：告警文案里只写「数据库」时，运维还得回面板去查这是
// 哪个机房的数据库——层级本来就是他们建出来区分同名节点的。已经带了 Group 的告警不覆盖
// （硬件/SNMP 这类非主机告警可能自己填过）。
func (cs *ConfigStore) decorateAlertGroups(alerts []Alert) []Alert {
	if len(alerts) == 0 {
		return alerts
	}
	paths := cs.hostFolderPathMap()
	if len(paths) == 0 {
		return alerts
	}
	for i := range alerts {
		if alerts[i].Group != "" || alerts[i].HostID == "" {
			continue
		}
		if p := paths[alerts[i].HostID]; p != "" {
			alerts[i].Group = p
		}
	}
	return alerts
}
