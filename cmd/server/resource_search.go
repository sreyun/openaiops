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

func (s *Server) searchResources(query string, limit int) []ResourceSearchResult {
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

	matches := make([]scoredResourceResult, 0, limit)
	seen := map[string]bool{}
	add := func(result ResourceSearchResult, fields ...string) {
		if seen[result.Ref] {
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
				View: "sre", Subtitle: firstNonEmpty(svc.Env, svc.Owner),
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
	results := s.searchResources(r.URL.Query().Get("q"), limit)
	if u, ok := s.currentUser(r); ok && u.hostScopeRestricted() && roleRank(u.Role) < roleRank(RoleAdmin) {
		filtered := results[:0]
		for _, res := range results {
			if res.HostID == "" || s.userCanAccessHost(u, res.HostID) {
				filtered = append(filtered, res)
			}
		}
		results = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   strings.TrimSpace(r.URL.Query().Get("q")),
		"count":   len(results),
		"results": results,
	})
}
