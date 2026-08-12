package main

import (
	"fmt"
	"strings"
)

// LocateResult is a cross-layer resource localization summary for AI / topology.
type LocateResult struct {
	Ref             string   `json:"ref"`
	Kind            string   `json:"kind"`
	HostID          string   `json:"host_id,omitempty"`
	Hostname        string   `json:"hostname,omitempty"`
	HyperVHostID    string   `json:"hyperv_host_id,omitempty"`
	VMName          string   `json:"vm_name,omitempty"`
	Containers      []string `json:"containers,omitempty"`
	PodsHint        []string `json:"pods_hint,omitempty"`
	HardwareHint    string   `json:"hardware_hint,omitempty"`
	OpenAlerts      int      `json:"open_alerts"`
	TopologySummary string   `json:"topology_summary,omitempty"`
	Summary         string   `json:"summary"`
	Chain           []string `json:"chain,omitempty"`
}

// locateResource resolves host / vm / container / pod / svc refs into a layered chain.
func (s *Server) locateResource(ref string) LocateResult {
	ref = normalizeTopoRef(ref)
	out := LocateResult{Ref: ref, Kind: topoRefKind(ref), Chain: []string{}}
	val := topoRefValue(ref)

	switch out.Kind {
	case "host":
		out.HostID = val
		if h := s.hostByID(val); h != nil {
			out.Hostname = h.Hostname
		}
		out.Chain = append(out.Chain, "host:"+val)
		s.locateFillHostExtras(&out)
	case "vm":
		// vm:<hostID>/<vmID or name>
		parts := strings.SplitN(val, "/", 2)
		if len(parts) == 2 {
			out.HyperVHostID = parts[0]
			out.VMName = parts[1]
			out.Chain = append(out.Chain, "vm:"+val, "host:"+parts[0])
			if s.pg != nil {
				if inv, ok := s.pg.getHyperVInventory(parts[0]); ok {
					if guests, _ := inv["guests"].([]any); guests != nil {
						for _, gi := range guests {
							g, _ := gi.(map[string]any)
							if g == nil {
								continue
							}
							id, _ := g["id"].(string)
							name, _ := g["name"].(string)
							if strings.EqualFold(id, parts[1]) || strings.EqualFold(name, parts[1]) {
								out.VMName = name
								if lid, _ := g["linked_host_id"].(string); lid != "" {
									out.HostID = lid
									out.Hostname, _ = g["linked_host_name"].(string)
									out.Chain = append(out.Chain, "host:"+lid)
								}
							}
						}
					}
				}
			}
			// enrich links on the fly
			if out.HostID == "" && s.pg != nil {
				rows := []map[string]any{}
				if inv, ok := s.pg.getHyperVInventory(parts[0]); ok {
					rows = append(rows, inv)
					s.enrichHyperVLinks(rows)
					if guests, _ := rows[0]["guests"].([]any); guests != nil {
						for _, gi := range guests {
							g, _ := gi.(map[string]any)
							if g == nil {
								continue
							}
							id, _ := g["id"].(string)
							name, _ := g["name"].(string)
							if strings.EqualFold(id, parts[1]) || strings.EqualFold(name, parts[1]) {
								if lid, _ := g["linked_host_id"].(string); lid != "" {
									out.HostID = lid
									out.Hostname, _ = g["linked_host_name"].(string)
									out.Chain = append(out.Chain, "host:"+lid)
								}
							}
						}
					}
				}
			}
		}
		if out.HostID != "" {
			s.locateFillHostExtras(&out)
		} else if out.HyperVHostID != "" {
			tmp := out
			tmp.HostID = out.HyperVHostID
			s.locateFillHostExtras(&tmp)
			out.Containers = tmp.Containers
			out.HardwareHint = tmp.HardwareHint
			out.OpenAlerts = tmp.OpenAlerts
		}
	case "container":
		// container:<hostID>/<cid>
		parts := strings.SplitN(val, "/", 2)
		if len(parts) == 2 {
			out.HostID = parts[0]
			out.Containers = []string{parts[1]}
			out.Chain = append(out.Chain, "container:"+val, "host:"+parts[0])
			s.locateFillHostExtras(&out)
			s.locateFillHyperVParent(&out)
		}
	case "pod":
		// pod:<cluster>/<ns>/<name>
		out.PodsHint = []string{val}
		out.Chain = append(out.Chain, "pod:"+val)
		parts := strings.SplitN(val, "/", 3)
		if len(parts) == 3 && s.cfg != nil {
			if c, ok := s.cfg.GetK8sCluster(parts[0]); ok && c.Enabled {
				if cli, err := newK8sRESTClient(c); err == nil {
					res, _ := cli.ListPods(parts[1], 200, "")
					for _, it := range res.Items {
						_, name := k8sMetaName(it)
						if name != parts[2] {
							continue
						}
						node := ""
						if spec, _ := it["spec"].(map[string]any); spec != nil {
							node, _ = spec["nodeName"].(string)
						}
						if node != "" {
							if hid, hname := s.hostIDForK8sNodeName(node); hid != "" {
								out.HostID = hid
								out.Hostname = hname
								out.Chain = append(out.Chain, "host:"+hid+" (node="+node+")")
								s.locateFillHostExtras(&out)
								s.locateFillHyperVParent(&out)
							} else {
								out.Chain = append(out.Chain, "node:"+node+" (未关联纳管主机)")
							}
						}
						break
					}
				}
			}
		}
	case "svc", "cat":
		rca := s.computeTopologyRCA("", 7)
		_ = rca
		edges := s.cfg.TopologyEdges()
		related := []string{}
		for _, e := range edges {
			if e.From == ref || e.To == ref {
				related = append(related, fmt.Sprintf("%s -[%s]-> %s", e.From, e.Kind, e.To))
				if strings.HasPrefix(e.From, "host:") {
					out.HostID = strings.TrimPrefix(e.From, "host:")
				}
				if strings.HasPrefix(e.To, "host:") && out.HostID == "" {
					out.HostID = strings.TrimPrefix(e.To, "host:")
				}
			}
		}
		if out.HostID != "" {
			out.Chain = append(out.Chain, "host:"+out.HostID)
			s.locateFillHostExtras(&out)
			s.locateFillHyperVParent(&out)
		}
		out.TopologySummary = strings.Join(related, "; ")
	default:
		out.Summary = "无法识别资源引用"
		return out
	}

	if out.HostID != "" && s.incidents != nil {
		tr := s.computeTopologyRCA(out.HostID, 7)
		out.TopologySummary = tr.Summary
	}
	var b strings.Builder
	fmt.Fprintf(&b, "定位 %s", ref)
	if out.Hostname != "" {
		fmt.Fprintf(&b, " → 主机 %s(%s)", out.Hostname, out.HostID)
	} else if out.HostID != "" {
		fmt.Fprintf(&b, " → 主机 %s", out.HostID)
	}
	if out.HyperVHostID != "" {
		fmt.Fprintf(&b, " → Hyper-V 宿主 %s / VM %s", out.HyperVHostID, out.VMName)
	}
	if len(out.Containers) > 0 {
		fmt.Fprintf(&b, " → 容器 %s", strings.Join(out.Containers, ","))
	}
	if out.HardwareHint != "" {
		fmt.Fprintf(&b, " · %s", out.HardwareHint)
	}
	if out.OpenAlerts > 0 {
		fmt.Fprintf(&b, " · 未恢复告警 %d", out.OpenAlerts)
	}
	out.Summary = b.String()
	return out
}

func (s *Server) locateFillHostExtras(out *LocateResult) {
	if out.HostID == "" {
		return
	}
	if h := s.hostByID(out.HostID); h != nil && out.Hostname == "" {
		out.Hostname = h.Hostname
	}
	if s.pg != nil {
		if inv, ok := s.pg.getContainerInventory(out.HostID); ok {
			if list, _ := inv["containers"].([]any); list != nil {
				for _, ci := range list {
					c, _ := ci.(map[string]any)
					if c == nil {
						continue
					}
					st, _ := c["state"].(string)
					name, _ := c["name"].(string)
					if name == "" {
						name, _ = c["id"].(string)
					}
					if strings.EqualFold(st, "running") || strings.Contains(strings.ToLower(fmt.Sprint(c["status"])), "up") {
						out.Containers = append(out.Containers, name)
					}
				}
				if len(out.Containers) > 8 {
					out.Containers = out.Containers[:8]
				}
			}
		}
	}
	if s.notifier != nil {
		for _, a := range s.notifier.ActiveAlerts() {
			if a.HostID == out.HostID {
				out.OpenAlerts++
			}
		}
	}
	out.HardwareHint = "可通过 query_hardware / query_hardware_changes 查看 BMC 与近期硬件变更"
}

func (s *Server) locateFillHyperVParent(out *LocateResult) {
	if out.HostID == "" || s.pg == nil || out.HyperVHostID != "" {
		return
	}
	rows, err := s.pg.getAllHyperVInventories()
	if err != nil {
		return
	}
	s.enrichHyperVLinks(rows)
	for _, inv := range rows {
		hid, _ := inv["host_id"].(string)
		guests, _ := inv["guests"].([]any)
		for _, gi := range guests {
			g, _ := gi.(map[string]any)
			if g == nil {
				continue
			}
			lid, _ := g["linked_host_id"].(string)
			if lid == out.HostID {
				out.HyperVHostID = hid
				out.VMName, _ = g["name"].(string)
				out.Chain = append(out.Chain, "vm:"+hid+"/"+out.VMName)
				return
			}
		}
	}
}
