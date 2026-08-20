package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// BusinessService is a light CMDB node: business → hosts / k8s / datasources.
type BusinessService struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Owner         string   `json:"owner,omitempty"`
	Env           string   `json:"env,omitempty"` // prod|staging|dev
	Description   string   `json:"description,omitempty"`
	HostIDs       []string `json:"host_ids,omitempty"`
	K8sRefs       []string `json:"k8s_refs,omitempty"` // cluster/ns/kind/name or free-form
	DataSourceIDs []string `json:"datasource_ids,omitempty"`
	ChildIDs      []string `json:"child_ids,omitempty"`
	UpdatedAt     int64    `json:"updated_at,omitempty"`
}

func (cs *ConfigStore) BusinessServices() []BusinessService {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make([]BusinessService, len(cs.cfg.BusinessServices))
	copy(out, cs.cfg.BusinessServices)
	return out
}

func (cs *ConfigStore) UpsertBusinessService(in BusinessService) (BusinessService, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return BusinessService{}, fmt.Errorf("服务名称不能为空")
	}
	in.UpdatedAt = time.Now().Unix()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if in.ID == "" {
		in.ID = "svc_" + genToken()[:10]
		cs.cfg.BusinessServices = append(cs.cfg.BusinessServices, in)
	} else {
		found := false
		for i := range cs.cfg.BusinessServices {
			if cs.cfg.BusinessServices[i].ID == in.ID {
				cs.cfg.BusinessServices[i] = in
				found = true
				break
			}
		}
		if !found {
			cs.cfg.BusinessServices = append(cs.cfg.BusinessServices, in)
		}
	}
	return in, cs.save()
}

func (cs *ConfigStore) DeleteBusinessService(id string) error {
	cs.mu.Lock()
	kept := cs.cfg.BusinessServices[:0]
	for _, s := range cs.cfg.BusinessServices {
		if s.ID != id {
			kept = append(kept, s)
		}
	}
	cs.cfg.BusinessServices = kept
	cs.mu.Unlock()
	return cs.save()
}

func (cs *ConfigStore) FindBusinessService(id string) (BusinessService, bool) {
	id = strings.TrimSpace(id)
	for _, s := range cs.BusinessServices() {
		if s.ID == id || s.Name == id {
			return s, true
		}
	}
	return BusinessService{}, false
}

type ServiceImpact struct {
	Service       BusinessService  `json:"service"`
	Hosts         []string         `json:"hosts"`
	OpenIncidents []map[string]any `json:"open_incidents"`
	RecentChanges []ChangeRecord   `json:"recent_changes"`
	DataSources   []string         `json:"datasources,omitempty"`
}

func (s *Server) computeServiceImpact(svc BusinessService) ServiceImpact {
	out := ServiceImpact{Service: svc, Hosts: append([]string{}, svc.HostIDs...), DataSources: append([]string{}, svc.DataSourceIDs...)}
	hostSet := map[string]bool{}
	for _, h := range svc.HostIDs {
		hostSet[h] = true
	}
	since := time.Now().Add(-14 * 24 * time.Hour).Unix()
	if s.incidents != nil {
		for _, inc := range s.incidents.List() {
			if inc.Status == "resolved" {
				continue
			}
			if hostSet[inc.HostID] || serviceMentionsIncident(svc, inc) {
				out.OpenIncidents = append(out.OpenIncidents, map[string]any{
					"id": inc.ID, "title": inc.Title, "severity": inc.Severity, "host_id": inc.HostID,
				})
			}
		}
	}
	if s.changes != nil {
		for _, c := range s.changes.List() {
			if c.StartedAt < since {
				continue
			}
			if changeTouchesService(c, svc, hostSet) {
				out.RecentChanges = append(out.RecentChanges, c)
			}
		}
		if len(out.RecentChanges) > 20 {
			out.RecentChanges = out.RecentChanges[:20]
		}
	}
	return out
}

func serviceMentionsIncident(svc BusinessService, inc Incident) bool {
	for _, l := range inc.Links {
		if l.Type == "service" && (l.ID == svc.ID || l.ID == svc.Name) {
			return true
		}
	}
	return false
}

func changeTouchesService(c ChangeRecord, svc BusinessService, hostSet map[string]bool) bool {
	for _, h := range c.HostIDs {
		if hostSet[h] {
			return true
		}
	}
	for _, name := range c.Services {
		if name == svc.Name || name == svc.ID {
			return true
		}
	}
	for _, l := range c.Links {
		if l.Type == "service" && (l.ID == svc.ID || l.ID == svc.Name) {
			return true
		}
		if l.Type == "host" && hostSet[l.ID] {
			return true
		}
	}
	return false
}

func (s *Server) handleListBusinessServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.BusinessServices())
}

func (s *Server) handleUpsertBusinessService(w http.ResponseWriter, r *http.Request) {
	var in BusinessService
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	out, err := s.cfg.UpsertBusinessService(in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	for _, hid := range out.HostIDs {
		hid = strings.TrimSpace(hid)
		if hid == "" {
			continue
		}
		_, _ = s.cfg.UpsertTopologyEdge(TopologyEdge{
			ID:   "auto-svc-" + out.ID + "-" + hid,
			From: "svc:" + out.Name,
			To:   "host:" + hid,
			Kind: "runs_on",
			Note: "business service",
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteBusinessService(w http.ResponseWriter, r *http.Request) {
	_ = s.cfg.DeleteBusinessService(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBusinessServiceImpact(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.cfg.FindBusinessService(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "service not found"})
		return
	}
	writeJSON(w, http.StatusOK, s.computeServiceImpact(svc))
}

func (s *Server) handleChangeImpact(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rec, ok := s.changes.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	hostSet := map[string]bool{}
	for _, h := range rec.HostIDs {
		hostSet[h] = true
	}
	var matched []BusinessService
	for _, svc := range s.cfg.BusinessServices() {
		hs := map[string]bool{}
		for _, h := range svc.HostIDs {
			hs[h] = true
		}
		if changeTouchesService(rec, svc, hs) {
			matched = append(matched, svc)
		}
	}
	var open []map[string]any
	if s.incidents != nil {
		for _, inc := range s.incidents.List() {
			if inc.Status == "resolved" {
				continue
			}
			if hostSet[inc.HostID] {
				open = append(open, map[string]any{"id": inc.ID, "title": inc.Title, "severity": inc.Severity})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"change": rec, "services": matched, "open_incidents": open, "hosts": rec.HostIDs,
	})
}
