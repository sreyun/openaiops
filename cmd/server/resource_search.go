package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// ResourceSearchResult is a compact, navigation-ready resource match.
// Ref reuses locateResource's host/vm/container/pod convention and extends it
// with hardware/cluster identities used by their UI views.
type ResourceSearchResult struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Host     string `json:"host,omitempty"`
	HostID   string `json:"host_id,omitempty"`
	Ref      string `json:"ref"`
	View     string `json:"view"`
	Subtitle string `json:"subtitle,omitempty"`
}

type scoredResourceResult struct {
	ResourceSearchResult
	score int
}

func resourceMatchScore(query string, fields ...string) int {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0
	}
	best := 0
	for _, field := range fields {
		v := strings.ToLower(strings.TrimSpace(field))
		switch {
		case v == q:
			if best < 100 {
				best = 100
			}
		case strings.HasPrefix(v, q):
			if best < 80 {
				best = 80
			}
		case strings.Contains(v, q):
			if best < 60 {
				best = 60
			}
		}
	}
	return best
}

// 全局搜索必须遵守主机级 RBAC。
//
// 这里原来完全没做授权过滤：一个被限定在某个主机组/标签里的账号，在搜索框里敲两个字符，
// 就能拿到**全量**主机名、IP、虚拟机名、容器名，以及硬件的厂商、型号、序列号、资产标签，
// 而且每条还附带 view/ref 可以直接跳过去。平台其它读接口（主机列表、告警、容器、Hyper-V）
// 一律走 filterHostsForUser / requireHostAccess，唯独搜索这条路是敞开的——
// 等于给受限账号留了一个绕过主机授权的资产清单出口。
//
// 与 filterHostsForUser 保持同一套判定：解析不出用户、未设限、管理员，三种情况都不过滤。
func (s *Server) searchResources(r *http.Request, query string, limit int) []ResourceSearchResult {
	query = strings.TrimSpace(query)
	if query == "" {
		return []ResourceSearchResult{}
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	scopedUser, hasUser := s.currentUser(r)
	scoped := hasUser && scopedUser.hostScopeRestricted() && roleRank(scopedUser.Role) < roleRank(RoleAdmin)
	allowHost := func(hostID string) bool {
		if !scoped {
			return true
		}
		return s.userCanAccessHost(scopedUser, hostID)
	}

	matches := make([]scoredResourceResult, 0, limit)
	seen := map[string]bool{}
	add := func(result ResourceSearchResult, fields ...string) {
		if seen[result.Ref] {
			return
		}
		// 主机维度的资源（主机 / 虚拟机 / 容器 / 硬件）都带 HostID，越权的直接不进候选集；
		// 集群、业务服务、数据源是平台级配置，不按主机授权切分。
		if result.HostID != "" && !allowHost(result.HostID) {
			return
		}
		if score := resourceMatchScore(query, fields...); score > 0 {
			seen[result.Ref] = true
			matches = append(matches, scoredResourceResult{ResourceSearchResult: result, score: score})
		}
	}

	hostNames := map[string]string{}
	if s.store != nil {
		for _, host := range s.store.ListHosts() {
			if host == nil {
				continue
			}
			hostNames[host.ID] = host.Hostname
			add(ResourceSearchResult{
				Type: "host", Name: host.Hostname, Host: host.Hostname, HostID: host.ID,
				Ref: "host:" + host.ID, View: "hosts", Subtitle: host.IP,
			}, host.Hostname, host.ID, host.IP, host.OS, host.Platform)
		}
	}

	if s.hv != nil {
		for hostID, inv := range s.hv.snapshot() {
			hostName := inv.hostname
			if hostName == "" {
				hostName = hostNames[hostID]
			}
			for _, vm := range inv.guests {
				name := vm.Name
				if name == "" {
					name = vm.ID
				}
				add(ResourceSearchResult{
					Type: "hyperv_vm", Name: name, Host: hostName, HostID: hostID,
					Ref: "vm:" + hostID + "/" + vm.ID, View: "hyperv", Subtitle: vm.State,
				}, name, vm.ID, hostName, hostID, strings.Join(vm.IPAddresses, " "))
			}
		}
	}

	if s.pg != nil {
		if rows, err := s.pg.getAllHyperVInventories(); err == nil {
			for _, inv := range rows {
				hostID := fmt.Sprint(inv["host_id"])
				hostName := fmt.Sprint(inv["host_name"])
				if hostName == "" || hostName == "<nil>" {
					hostName = hostNames[hostID]
				}
				guests, _ := inv["guests"].([]any)
				for _, raw := range guests {
					vm, _ := raw.(map[string]any)
					if vm == nil {
						continue
					}
					id, name := fmt.Sprint(vm["id"]), fmt.Sprint(vm["name"])
					if name == "" || name == "<nil>" {
						name = id
					}
					ips := fmt.Sprint(vm["ip_addresses"])
					add(ResourceSearchResult{
						Type: "hyperv_vm", Name: name, Host: hostName, HostID: hostID,
						Ref: "vm:" + hostID + "/" + id, View: "hyperv",
						Subtitle: fmt.Sprint(vm["state"]),
					}, name, id, hostName, hostID, ips)
				}
			}
		}
		if rows, err := s.pg.getAllContainerInventories(); err == nil {
			for _, inv := range rows {
				hostID := fmt.Sprint(inv["host_id"])
				hostName := fmt.Sprint(inv["host_name"])
				if hostName == "" || hostName == "<nil>" {
					hostName = hostNames[hostID]
				}
				containers, _ := inv["containers"].([]any)
				for _, raw := range containers {
					c, _ := raw.(map[string]any)
					if c == nil {
						continue
					}
					id, name := fmt.Sprint(c["id"]), fmt.Sprint(c["name"])
					if name == "" || name == "<nil>" {
						name = id
					}
					add(ResourceSearchResult{
						Type: "container", Name: name, Host: hostName, HostID: hostID,
						Ref: "container:" + hostID + "/" + id, View: "containers",
						Subtitle: fmt.Sprint(c["image"]),
					}, name, id, hostName, hostID, fmt.Sprint(c["image"]), fmt.Sprint(c["ports"]))
				}
			}
		}
	}

	if s.hw != nil {
		for hostID, entry := range s.hw.snapshot() {
			hostName := entry.hostname
			if hostName == "" {
				hostName = hostNames[hostID]
			}
			for _, snap := range entry.snaps {
				name := snap.TargetName
				if name == "" {
					name = snap.System.HostName
				}
				add(ResourceSearchResult{
					Type: "hardware", Name: name, Host: hostName, HostID: hostID,
					Ref: "hardware:" + hostID + "/" + name, View: "hardware",
					Subtitle: strings.TrimSpace(snap.System.Manufacturer + " " + snap.System.Model),
				}, name, hostName, hostID, snap.TargetURL, snap.System.HostName,
					snap.System.Manufacturer, snap.System.Model, snap.System.SerialNumber, snap.System.AssetTag)
			}
		}
	}

	if s.cfg != nil {
		for _, cluster := range s.cfg.ListK8sClusters() {
			add(ResourceSearchResult{
				Type: "k8s_cluster", Name: cluster.Name, Ref: "cluster:" + cluster.ID,
				View: "k8s", Subtitle: cluster.APIServer,
			}, cluster.Name, cluster.ID, cluster.APIServer, cluster.DefaultNS)
		}
		for _, svc := range s.cfg.BusinessServices() {
			add(ResourceSearchResult{
				Type: "service", Name: svc.Name, Ref: "service:" + svc.ID,
				View: "sre", Subtitle: firstNonEmptyOrDash(svc.Env, svc.Owner),
			}, svc.Name, svc.ID, svc.Owner, svc.Env, strings.Join(svc.HostIDs, " "))
		}
		for _, ds := range s.cfg.ListDataSources() {
			add(ResourceSearchResult{
				Type: "datasource", Name: ds.Name, Ref: "datasource:" + ds.ID,
				View: "datasources", Subtitle: ds.Type,
			}, ds.Name, ds.ID, ds.Type, ds.URL)
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].Type != matches[j].Type {
			return matches[i].Type < matches[j].Type
		}
		return strings.ToLower(matches[i].Name) < strings.ToLower(matches[j].Name)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]ResourceSearchResult, len(matches))
	for i := range matches {
		out[i] = matches[i].ResourceSearchResult
	}
	return out
}

func (s *Server) handleResourceSearch(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	results := s.searchResources(r, r.URL.Query().Get("q"), limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   strings.TrimSpace(r.URL.Query().Get("q")),
		"count":   len(results),
		"results": results,
	})
}
