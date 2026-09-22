package main

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
)

var reComposeProject = regexp.MustCompile(`^([a-zA-Z0-9][a-zA-Z0-9_.-]*)_[a-zA-Z0-9][a-zA-Z0-9_.-]*_\d+$`)

// discoverAutoTopologyEdges derives lightweight dependency edges from K8s, Hyper-V
// and container inventories already linked to managed hosts.
func (s *Server) discoverAutoTopologyEdges() []TopologyEdge {
	seen := map[string]bool{}
	var edges []TopologyEdge
	add := func(from, to, kind, note string) {
		from = normalizeTopoRef(from)
		to = normalizeTopoRef(to)
		kind = normalizeTopoKind(kind)
		if from == "" || to == "" || from == to {
			return
		}
		key := from + "|" + to + "|" + kind
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, TopologyEdge{
			ID: "auto-" + genToken()[:8], From: from, To: to, Kind: kind, Note: note,
		})
	}

	for _, cluster := range s.cfg.ListK8sClusters() {
		if !cluster.Enabled {
			continue
		}
		cli, err := newK8sRESTClient(cluster)
		if err != nil {
			continue
		}
		nodes, err := cli.ListNodes()
		if err != nil {
			continue
		}
		for _, it := range nodes {
			_, name := k8sMetaName(it)
			row := map[string]any{"name": name}
			s.enrichK8sNodeRow(it, row)
			hid, _ := row["linked_host_id"].(string)
			if hid == "" {
				continue
			}
			svc := "svc:k8s:" + cluster.ID + ":node:" + name
			add("host:"+hid, svc, "runs_on", "K8s 节点 "+name+" @ "+cluster.Name)
			add(svc, "host:"+hid, "depends_on", "节点承载于纳管主机")
		}
	}

	if s.pg != nil {
		if hvRows, err := s.pg.getAllHyperVInventories(); err == nil {
			s.enrichHyperVLinks(hvRows)
			for _, row := range hvRows {
				hvHost, _ := row["host_id"].(string)
				if hvHost == "" {
					continue
				}
				guests, _ := row["guests"].([]any)
				for _, gi := range guests {
					g, ok := gi.(map[string]any)
					if !ok {
						continue
					}
					gname, _ := g["name"].(string)
					if gname == "" {
						continue
					}
					vmRef := "vm:" + strings.ToLower(strings.TrimSpace(gname))
					add(vmRef, "host:"+hvHost, "runs_on", "Hyper-V 虚拟机 @ "+hvHost)
					if lid, _ := g["linked_host_id"].(string); lid != "" && lid != hvHost {
						add("host:"+lid, vmRef, "depends_on", "纳管主机运行于 Hyper-V")
					}
				}
			}
		}
		if ctRows, err := s.pg.getAllContainerInventories(); err == nil {
			projects := map[string]string{} // project -> host_id
			for _, row := range ctRows {
				hostID, _ := row["host_id"].(string)
				if hostID == "" {
					continue
				}
				raw, _ := row["containers"].([]any)
				for _, ci := range raw {
					c, ok := ci.(map[string]any)
					if !ok {
						continue
					}
					name, _ := c["name"].(string)
					name = strings.TrimSpace(name)
					if name == "" {
						continue
					}
					add("container:"+name, "host:"+hostID, "runs_on", "容器 @ "+hostID)
					if m := reComposeProject.FindStringSubmatch(name); len(m) == 2 {
						projects[m[1]] = hostID
					}
				}
			}
			for proj, hostID := range projects {
				add("svc:compose:"+proj, "host:"+hostID, "runs_on", "Compose 项目 "+proj)
			}
		}
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return edges
}

func (s *Server) handleDiscoverAutoTopology(w http.ResponseWriter, r *http.Request) {
	edges := s.discoverAutoTopologyEdges()
	apply := r.URL.Query().Get("apply") == "1" || r.URL.Query().Get("apply") == "true"
	added := 0
	if apply && r.Method == http.MethodPost {
		u, okUser := s.currentUser(r)
		scoped := okUser && u.hostScopeRestricted() && roleRank(u.Role) < roleRank(RoleAdmin)
		can := func(string) bool { return true }
		if scoped {
			can = s.hostAccessFor(u)
		}
		existing := map[string]bool{}
		for _, e := range s.cfg.TopologyEdges() {
			existing[e.From+"|"+e.To+"|"+e.Kind] = true
		}
		for _, e := range edges {
			key := e.From + "|" + e.To + "|" + e.Kind
			if existing[key] {
				continue // 手工边优先：同 from/to/kind 不覆盖
			}
			if scoped {
				// 自动发现会扫全舰队；受限账号只允许落自己授权范围内的边，
				// 否则 POST ?apply=1 等于给范围外主机批量建 RCA 通道。
				okEdge := true
				for _, hid := range s.topologyRefsHostIDs(e.From, e.To) {
					if !can(hid) {
						okEdge = false
						break
					}
				}
				if !okEdge {
					continue
				}
			}
			e.ID = ""
			if note := strings.TrimSpace(e.Note); note != "" && !strings.HasPrefix(note, "[auto] ") {
				e.Note = "[auto] " + note
			}
			if _, err := s.cfg.UpsertTopologyEdge(e); err == nil {
				existing[key] = true
				added++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"edges": edges, "count": len(edges), "added": added, "applied": apply && r.Method == http.MethodPost,
		"hint": "GET 仅预览；POST ?apply=1 合并写入（已有手工边优先，不覆盖）。",
	})
}
