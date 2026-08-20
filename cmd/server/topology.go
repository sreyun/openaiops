package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ============================================================================
// 轻量服务依赖拓扑 + 变更关联 RCA（P3）
//
// 不追求完整 CMDB：运维用「主机 / 分类 / 逻辑服务」三条边描述依赖，
// 事件发生时做一跳～两跳扩散，关联同拓扑上的未决事件与近期硬件变更，
// 供诊断提示词与时间线「拓扑 RCA」使用。
// ============================================================================

// TopologyEdge 一条有向依赖边。节点写法：host:<id> | cat:<分类名> | svc:<服务名>
type TopologyEdge struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // depends_on | runs_on | talks_to
	Note string `json:"note,omitempty"`
}

// TopologyRCA 某主机/事件的轻量根因与影响面摘要。
type TopologyRCA struct {
	HostID        string              `json:"host_id"`
	Hostname      string              `json:"hostname,omitempty"`
	Category      string              `json:"category,omitempty"`
	Upstream      []TopologyNodeHit   `json:"upstream"`       // 可能根因（本机依赖的上游）
	Downstream    []TopologyNodeHit   `json:"downstream"`     // 影响面（依赖本机的下游）
	RelatedHosts  []TopologyHostHit   `json:"related_hosts"`  // 扩散到的具体主机
	OpenIncidents []TopologyIncHit    `json:"open_incidents"` // 关联主机上的未决事件
	RecentChanges []TopologyChangeHit `json:"recent_changes"` // 近期硬件/资产变更
	Summary       string              `json:"summary"`        // 给时间线 / 提示词的短文
	Hints         []string            `json:"hints,omitempty"`
}

type TopologyNodeHit struct {
	Ref  string `json:"ref"`
	Kind string `json:"kind,omitempty"` // 边类型
	Via  string `json:"via,omitempty"`  // 经由哪条边
	Note string `json:"note,omitempty"`
}

type TopologyHostHit struct {
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname"`
	Category string `json:"category,omitempty"`
	Reason   string `json:"reason"` // upstream|downstream|same_category
}

type TopologyIncHit struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	HostID   string `json:"host_id"`
	Hostname string `json:"hostname"`
}

type TopologyChangeHit struct {
	HostID    string `json:"host_id"`
	Hostname  string `json:"hostname,omitempty"`
	Kind      string `json:"kind"`
	Component string `json:"component"`
	Action    string `json:"action"`
	Detail    string `json:"detail,omitempty"`
	At        int64  `json:"at"`
}

func normalizeTopoRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	// 允许简写：裸 host id → host:<id>；含中文/空格的当 svc
	if strings.Contains(ref, ":") {
		parts := strings.SplitN(ref, ":", 2)
		kind := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(parts[1])
		if val == "" {
			return ""
		}
		switch kind {
		case "host", "cat", "svc", "vm", "container", "pod":
			return kind + ":" + val
		default:
			return "svc:" + ref
		}
	}
	return "host:" + ref
}

func topoRefKind(ref string) string {
	if i := strings.IndexByte(ref, ':'); i > 0 {
		return ref[:i]
	}
	return ""
}

func topoRefValue(ref string) string {
	if i := strings.IndexByte(ref, ':'); i >= 0 && i+1 < len(ref) {
		return ref[i+1:]
	}
	return ref
}

func normalizeTopoKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "runs_on", "runson", "runs-on":
		return "runs_on"
	case "talks_to", "talksto", "talks-to", "peers":
		return "talks_to"
	default:
		return "depends_on"
	}
}

// computeTopologyRCA 基于配置边 + 当前主机分类，计算轻量 RCA。
func (s *Server) computeTopologyRCA(hostID string, lookbackDays int) TopologyRCA {
	out := TopologyRCA{
		HostID:   hostID,
		Upstream: []TopologyNodeHit{}, Downstream: []TopologyNodeHit{},
		RelatedHosts: []TopologyHostHit{}, OpenIncidents: []TopologyIncHit{},
		RecentChanges: []TopologyChangeHit{}, Hints: []string{},
	}
	if hostID == "" {
		out.Summary = "无主机，无法做拓扑扩散。"
		return out
	}
	if lookbackDays <= 0 {
		lookbackDays = 7
	}
	h, ok := s.store.GetHost(hostID)
	cat := s.effectiveCategory(hostID)
	if ok && h != nil {
		out.Hostname = h.Hostname
	}
	out.Category = cat

	seed := map[string]bool{"host:" + hostID: true}
	if cat != "" {
		seed["cat:"+cat] = true
	}

	edges := s.cfg.TopologyEdges()
	upSet := map[string]TopologyNodeHit{}
	downSet := map[string]TopologyNodeHit{}
	relatedSeed := map[string]string{} // hostID -> reason

	for _, e := range edges {
		from, to := normalizeTopoRef(e.From), normalizeTopoRef(e.To)
		if from == "" || to == "" {
			continue
		}
		kind := normalizeTopoKind(e.Kind)
		fromHit, toHit := seed[from], seed[to]
		if !fromHit && !toHit {
			continue
		}
		// from → to 且 kind=depends_on：from 依赖 to → to 是上游根因候选；from 是下游影响面
		switch {
		case fromHit && !toHit:
			upSet[to] = TopologyNodeHit{Ref: to, Kind: kind, Via: from + "→" + to, Note: e.Note}
			for _, hid := range s.expandTopoRefToHosts(to) {
				if hid != hostID {
					relatedSeed[hid] = "upstream"
				}
			}
		case toHit && !fromHit:
			downSet[from] = TopologyNodeHit{Ref: from, Kind: kind, Via: from + "→" + to, Note: e.Note}
			for _, hid := range s.expandTopoRefToHosts(from) {
				if hid != hostID {
					relatedSeed[hid] = "downstream"
				}
			}
		case fromHit && toHit:
			// 两端都是本机 seed（如 host 与自身 cat），忽略
		}
		// talks_to 双向：另一端也算关联
		if kind == "talks_to" {
			other := to
			if toHit {
				other = from
			}
			for _, hid := range s.expandTopoRefToHosts(other) {
				if hid != hostID {
					if _, ok := relatedSeed[hid]; !ok {
						relatedSeed[hid] = "peer"
					}
				}
			}
		}
	}

	// 同分类软关联（无边时也能给一点上下文）
	if cat != "" {
		for _, hh := range s.store.ListHosts() {
			if hh.ID == hostID {
				continue
			}
			if s.effectiveCategory(hh.ID) == cat {
				if _, ok := relatedSeed[hh.ID]; !ok {
					relatedSeed[hh.ID] = "same_category"
				}
			}
		}
	}

	for ref, n := range upSet {
		_ = ref
		out.Upstream = append(out.Upstream, n)
	}
	for ref, n := range downSet {
		_ = ref
		out.Downstream = append(out.Downstream, n)
	}
	sort.Slice(out.Upstream, func(i, j int) bool { return out.Upstream[i].Ref < out.Upstream[j].Ref })
	sort.Slice(out.Downstream, func(i, j int) bool { return out.Downstream[i].Ref < out.Downstream[j].Ref })

	for hid, reason := range relatedSeed {
		hh, ok := s.store.GetHost(hid)
		name, c := hid, s.effectiveCategory(hid)
		if ok && hh != nil {
			name = hh.Hostname
		}
		out.RelatedHosts = append(out.RelatedHosts, TopologyHostHit{
			HostID: hid, Hostname: name, Category: c, Reason: reason,
		})
	}
	sort.Slice(out.RelatedHosts, func(i, j int) bool {
		if out.RelatedHosts[i].Reason != out.RelatedHosts[j].Reason {
			return out.RelatedHosts[i].Reason < out.RelatedHosts[j].Reason
		}
		return out.RelatedHosts[i].Hostname < out.RelatedHosts[j].Hostname
	})
	if len(out.RelatedHosts) > 24 {
		out.RelatedHosts = out.RelatedHosts[:24]
	}

	relatedIDs := map[string]bool{hostID: true}
	for _, rh := range out.RelatedHosts {
		relatedIDs[rh.HostID] = true
	}
	var openIncs []Incident
	if s.incidents != nil {
		openIncs = s.incidents.List()
	}
	for _, inc := range openIncs {
		if inc.Status == "resolved" || inc.HostID == "" || !relatedIDs[inc.HostID] {
			continue
		}
		out.OpenIncidents = append(out.OpenIncidents, TopologyIncHit{
			ID: inc.ID, Title: inc.Title, Severity: inc.Severity,
			HostID: inc.HostID, Hostname: inc.Hostname,
		})
		if len(out.OpenIncidents) >= 12 {
			break
		}
	}

	cutoff := time.Now().Add(-time.Duration(lookbackDays) * 24 * time.Hour).Unix()
	if s.pg != nil {
		for hid := range relatedIDs {
			chs, err := s.pg.getHardwareChanges(hid, "", 20)
			if err != nil {
				continue
			}
			hn := hid
			if hh, ok := s.store.GetHost(hid); ok && hh != nil {
				hn = hh.Hostname
			}
			for _, c := range chs {
				at := int64(0)
				switch v := c["created_at"].(type) {
				case int64:
					at = v
				case float64:
					at = int64(v)
				case time.Time:
					at = v.Unix()
				}
				if at > 0 && at < cutoff {
					continue
				}
				kind, _ := c["kind"].(string)
				comp, _ := c["component"].(string)
				action, _ := c["action"].(string)
				oldV, _ := c["old_value"].(string)
				newV, _ := c["new_value"].(string)
				detail := strings.TrimSpace(oldV + " → " + newV)
				out.RecentChanges = append(out.RecentChanges, TopologyChangeHit{
					HostID: hid, Hostname: hn, Kind: kind, Component: comp,
					Action: action, Detail: trimLine(detail, 120), At: at,
				})
			}
		}
		sort.Slice(out.RecentChanges, func(i, j int) bool { return out.RecentChanges[i].At > out.RecentChanges[j].At })
		if len(out.RecentChanges) > 15 {
			out.RecentChanges = out.RecentChanges[:15]
		}
	}

	// Merge ops change records for related hosts.
	if s.changes != nil {
		hostIDs := []string{hostID}
		for _, h := range out.RelatedHosts {
			hostIDs = append(hostIDs, h.HostID)
		}
		for _, c := range s.changes.RelatedToHosts(hostIDs, cutoff) {
			out.RecentChanges = append(out.RecentChanges, TopologyChangeHit{
				HostID: strings.Join(c.HostIDs, ","), Hostname: "", Kind: "change:" + c.Kind,
				Component: c.Title, Action: c.Status, Detail: trimLine(c.Summary, 120), At: c.StartedAt,
			})
		}
		sort.Slice(out.RecentChanges, func(i, j int) bool { return out.RecentChanges[i].At > out.RecentChanges[j].At })
		if len(out.RecentChanges) > 20 {
			out.RecentChanges = out.RecentChanges[:20]
		}
	}

	out.Summary = formatTopologyRCASummary(out)
	if len(edges) == 0 {
		out.Hints = append(out.Hints, "尚未配置服务依赖边：可在 SRE「依赖拓扑」页添加 host/cat/svc 边，以增强 RCA。")
	}
	if len(out.RecentChanges) > 0 {
		out.Hints = append(out.Hints, "关联主机近期有硬件/固件变更，优先核对变更与故障时间是否吻合。")
	}
	if len(out.OpenIncidents) > 1 {
		out.Hints = append(out.Hints, "拓扑关联主机上存在多起未决事件，可能为同源故障或连锁影响。")
	}
	return out
}

func (s *Server) expandTopoRefToHosts(ref string) []string {
	ref = normalizeTopoRef(ref)
	switch topoRefKind(ref) {
	case "host":
		return []string{topoRefValue(ref)}
	case "cat":
		cat := topoRefValue(ref)
		var ids []string
		for _, h := range s.store.ListHosts() {
			if s.effectiveCategory(h.ID) == cat {
				ids = append(ids, h.ID)
			}
		}
		return ids
	default:
		// svc：找与该服务直接相连的 host 节点
		var ids []string
		seen := map[string]bool{}
		for _, e := range s.cfg.TopologyEdges() {
			f, t := normalizeTopoRef(e.From), normalizeTopoRef(e.To)
			var other string
			if f == ref {
				other = t
			} else if t == ref {
				other = f
			} else {
				continue
			}
			if topoRefKind(other) == "host" {
				hid := topoRefValue(other)
				if !seen[hid] {
					seen[hid] = true
					ids = append(ids, hid)
				}
			} else if topoRefKind(other) == "cat" {
				for _, hid := range s.expandTopoRefToHosts(other) {
					if !seen[hid] {
						seen[hid] = true
						ids = append(ids, hid)
					}
				}
			}
		}
		return ids
	}
}

func formatTopologyRCASummary(r TopologyRCA) string {
	var b strings.Builder
	name := r.Hostname
	if name == "" {
		name = r.HostID
	}
	fmt.Fprintf(&b, "拓扑 RCA · %s", name)
	if r.Category != "" {
		fmt.Fprintf(&b, "（分类 %s）", r.Category)
	}
	b.WriteString("\n")
	if len(r.Upstream) == 0 && len(r.Downstream) == 0 && len(r.RelatedHosts) == 0 {
		b.WriteString("未配置相关依赖边；仅能按同分类做弱关联。\n")
	}
	if len(r.Upstream) > 0 {
		b.WriteString("上游（可能根因）：")
		parts := make([]string, 0, len(r.Upstream))
		for _, u := range r.Upstream {
			parts = append(parts, u.Ref)
		}
		b.WriteString(strings.Join(parts, ", ") + "\n")
	}
	if len(r.Downstream) > 0 {
		b.WriteString("下游（影响面）：")
		parts := make([]string, 0, len(r.Downstream))
		for _, u := range r.Downstream {
			parts = append(parts, u.Ref)
		}
		b.WriteString(strings.Join(parts, ", ") + "\n")
	}
	if n := len(r.RelatedHosts); n > 0 {
		fmt.Fprintf(&b, "关联主机 %d 台", n)
		show := n
		if show > 5 {
			show = 5
		}
		parts := make([]string, 0, show)
		for i := 0; i < show; i++ {
			parts = append(parts, r.RelatedHosts[i].Hostname+"("+r.RelatedHosts[i].Reason+")")
		}
		b.WriteString("：" + strings.Join(parts, ", "))
		if n > show {
			fmt.Fprintf(&b, " …等%d台", n)
		}
		b.WriteString("\n")
	}
	if n := len(r.OpenIncidents); n > 0 {
		fmt.Fprintf(&b, "关联未决事件 %d 起", n)
		show := n
		if show > 3 {
			show = 3
		}
		parts := make([]string, 0, show)
		for i := 0; i < show; i++ {
			parts = append(parts, fmt.Sprintf("#%d %s", r.OpenIncidents[i].ID, trimLine(r.OpenIncidents[i].Title, 40)))
		}
		b.WriteString("：" + strings.Join(parts, "; ") + "\n")
	}
	if n := len(r.RecentChanges); n > 0 {
		fmt.Fprintf(&b, "近%d日资产变更 %d 条", 7, n)
		c0 := r.RecentChanges[0]
		fmt.Fprintf(&b, "（最近：%s %s %s @%s）\n", c0.Action, c0.Kind, c0.Component, c0.Hostname)
	}
	for _, h := range r.Hints {
		b.WriteString("提示：" + h + "\n")
	}
	return strings.TrimSpace(b.String())
}

// appendTopologyRCAToIncident 把拓扑 RCA 写入事件时间线（correlation 旁路，kind=topology_rca）。
func (s *Server) appendTopologyRCAToIncident(inc Incident) {
	if inc.HostID == "" {
		return
	}
	rca := s.computeTopologyRCA(inc.HostID, 7)
	if rca.Summary == "" || (len(rca.Upstream) == 0 && len(rca.Downstream) == 0 &&
		len(rca.RelatedHosts) == 0 && len(rca.RecentChanges) == 0 && len(rca.OpenIncidents) <= 1) {
		// 几乎无信息时仍写一条弱提示（有同分类或变更时 Summary 已含内容）
		if len(rca.Hints) == 0 && len(rca.RelatedHosts) == 0 && len(rca.RecentChanges) == 0 {
			return
		}
	}
	s.incidents.AddEvent(inc.ID, "topology_rca", "system", rca.Summary)
	s.store.MarkDirty()
}
