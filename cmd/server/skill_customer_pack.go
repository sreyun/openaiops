package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CustomerSkillPack is a portable private skill export/import package.
type CustomerSkillPack struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Version    string              `json:"version"`
	ExportedAt int64               `json:"exported_at"`
	Skills     []CustomerSkillItem `json:"skills"`
}

type CustomerSkillItem struct {
	Name       string `json:"name"`
	Trigger    string `json:"trigger"`
	Steps      string `json:"steps"`
	Tags       string `json:"tags,omitempty"`
	Status     string `json:"status,omitempty"`
	Version    int    `json:"version,omitempty"`
	ServiceIDs string `json:"service_ids,omitempty"`
	Categories string `json:"categories,omitempty"`
	Source     string `json:"source,omitempty"`
}

// GET /api/v1/ai/skills/export?status=active|draft|all&service_id=&category=
func (s *Server) handleExportCustomerSkillPack(w http.ResponseWriter, r *http.Request) {
	if s.pg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "需要 PostgreSQL"})
		return
	}
	statusFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if statusFilter == "" {
		statusFilter = "active"
	}
	svcFilter := strings.TrimSpace(r.URL.Query().Get("service_id"))
	catFilter := strings.TrimSpace(r.URL.Query().Get("category"))
	includeArchived := statusFilter == "all"
	list, err := s.pg.listSkillsFiltered(includeArchived)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	pack := CustomerSkillPack{
		ID: "customer-export", Name: "客户私有技能包", Version: "1",
		ExportedAt: time.Now().Unix(),
	}
	for _, sk := range list {
		st := strings.ToLower(strings.TrimSpace(sk.Status))
		if st == "" {
			st = "active"
		}
		if statusFilter != "all" && st != statusFilter {
			continue
		}
		if svcFilter != "" || catFilter != "" {
			global := strings.TrimSpace(sk.ServiceIDs) == "" && strings.TrimSpace(sk.Categories) == ""
			if !global && !skillMatchesScope(sk, svcFilter, catFilter) {
				continue
			}
		}
		pack.Skills = append(pack.Skills, CustomerSkillItem{
			Name: sk.Name, Trigger: sk.Trigger, Steps: sk.Steps, Tags: sk.Tags,
			Status: st, Version: sk.Version, ServiceIDs: sk.ServiceIDs, Categories: sk.Categories,
			Source: sk.Source,
		})
	}
	if pack.Skills == nil {
		pack.Skills = []CustomerSkillItem{}
	}
	w.Header().Set("Content-Disposition", "attachment; filename=customer-skills.json")
	writeJSON(w, http.StatusOK, pack)
}

// POST /api/v1/ai/skills/import  body: CustomerSkillPack or {skills:[...]}
func (s *Server) handleImportCustomerSkillPack(w http.ResponseWriter, r *http.Request) {
	if s.pg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "需要 PostgreSQL"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var pack CustomerSkillPack
	if err := json.NewDecoder(r.Body).Decode(&pack); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if len(pack.Skills) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skills 为空"})
		return
	}
	cfg := s.cfg.AIConfig()
	imported, skipped := 0, 0
	source := "customer:" + strings.TrimSpace(pack.ID)
	if source == "customer:" {
		source = "customer:import"
	}
	for _, sk := range pack.Skills {
		name := strings.TrimSpace(sk.Name)
		trigger := strings.TrimSpace(sk.Trigger)
		steps := strings.TrimSpace(sk.Steps)
		if name == "" || steps == "" {
			skipped++
			continue
		}
		var emb []float64
		if embedReady(cfg) {
			emb = embedText(cfg, name+"\n"+trigger)
		}
		status := strings.ToLower(strings.TrimSpace(sk.Status))
		if status != "draft" && status != "active" && status != "archived" {
			status = "draft" // imported private packs start as draft until reviewed
		}
		src := source
		if strings.TrimSpace(sk.Source) != "" {
			src = sk.Source
		}
		var id int64
		var err error
		if existing, ok := s.pg.findSkillByNameSource(name, src); ok {
			id = existing
			if len(emb) > 0 {
				err = s.pg.updateSkill(id, name, trigger, steps, emb)
			} else {
				err = s.pg.updateSkillText(id, name, trigger, steps)
			}
		} else if len(emb) > 0 {
			id, err = s.pg.insertSkill(name, trigger, steps, sk.Tags, src, emb)
		} else {
			id, err = s.pg.insertSkillNoEmbed(name, trigger, steps, sk.Tags, src)
		}
		if err != nil || id == 0 {
			skipped++
			continue
		}
		_ = s.pg.setSkillStatus(id, status)
		_ = s.pg.setSkillScope(id, sk.ServiceIDs, sk.Categories)
		imported++
	}
	actor, ip := s.actorIP(r)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: actor, IP: ip,
		Message: fmt.Sprintf("导入客户技能包 %s：更新/新增 %d，跳过 %d", pack.ID, imported, skipped)})
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported, "skipped": skipped, "pack_id": pack.ID})
}

// POST /api/v1/ai/skills/{id}/scope  {service_ids, categories}
func (s *Server) handleSetSkillScope(w http.ResponseWriter, r *http.Request) {
	id, ok := sreParseID(r)
	if !ok || s.pg == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	var req struct {
		ServiceIDs string `json:"service_ids"`
		Categories string `json:"categories"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.pg.setSkillScope(id, req.ServiceIDs, req.Categories); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
