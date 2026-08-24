package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// aiExperiment is a lightweight A/B definition stored in PG.
type aiExperiment struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Task    string `json:"task,omitempty"` // empty = all tasks
	Enabled bool   `json:"enabled"`
	// Variants: name → traffic percent (sum should be 100).
	Variants map[string]int `json:"variants"`
	// VariantModels: variant → model override (optional).
	VariantModels map[string]string `json:"variant_models,omitempty"`
	// VariantPromptSuffix: variant → extra system prompt (optional).
	VariantPromptSuffix map[string]string `json:"variant_prompt_suffix,omitempty"`
	CreatedAt           int64             `json:"created_at"`
}

func (s *Server) applyExperimentVariantOn(cfg AIConfig, expID, variant string) AIConfig {
	if s == nil || s.pg == nil || expID == "" || variant == "" {
		return cfg
	}
	exp, ok := s.pg.getAIExperiment(expID)
	if !ok {
		return cfg
	}
	if m := strings.TrimSpace(exp.VariantModels[variant]); m != "" {
		cfg.Model = m
	}
	return cfg
}

func experimentPromptSuffix(s *Server, expID, variant string) string {
	if s == nil || s.pg == nil || expID == "" || variant == "" {
		return ""
	}
	exp, ok := s.pg.getAIExperiment(expID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(exp.VariantPromptSuffix[variant])
}

func assignExperimentVariant(expID, actor string, variants map[string]int) string {
	if len(variants) == 0 {
		return "control"
	}
	h := sha256.Sum256([]byte(expID + "|" + actor))
	bucket := int(binary.BigEndian.Uint32(h[:4]) % 100)
	// stable order
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range variants {
		list = append(list, kv{k, v})
	}
	// insertion order from map is random — sort by name
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].k < list[i].k {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	acc := 0
	for _, item := range list {
		acc += item.v
		if bucket < acc {
			return item.k
		}
	}
	return list[len(list)-1].k
}

func (p *pgStore) ensureAIExperimentsTable() {
	if p == nil || p.db == nil {
		return
	}
	_, _ = p.db.Exec(`
CREATE TABLE IF NOT EXISTS ai_experiments (
  id TEXT PRIMARY KEY,
  data JSONB NOT NULL,
  created_at BIGINT NOT NULL DEFAULT 0
);
ALTER TABLE ai_feedback_events ADD COLUMN IF NOT EXISTS experiment_id TEXT DEFAULT '';
ALTER TABLE ai_feedback_events ADD COLUMN IF NOT EXISTS variant TEXT DEFAULT '';
`)
}

func (p *pgStore) upsertAIExperiment(exp aiExperiment) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("pg unavailable")
	}
	p.ensureAIExperimentsTable()
	if exp.CreatedAt == 0 {
		exp.CreatedAt = time.Now().Unix()
	}
	raw, _ := json.Marshal(exp)
	_, err := p.db.Exec(`INSERT INTO ai_experiments(id,data,created_at) VALUES($1,$2,$3)
ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data`, exp.ID, raw, exp.CreatedAt)
	return err
}

func (p *pgStore) getAIExperiment(id string) (aiExperiment, bool) {
	var exp aiExperiment
	if p == nil || p.db == nil || id == "" {
		return exp, false
	}
	var raw []byte
	err := p.db.QueryRow(`SELECT data FROM ai_experiments WHERE id=$1`, id).Scan(&raw)
	if err != nil || json.Unmarshal(raw, &exp) != nil {
		return exp, false
	}
	return exp, true
}

func (p *pgStore) insertAIFeedbackEventAB(task, actor, action, sourceHash, expID, variant string) bool {
	if p == nil || p.db == nil {
		return false
	}
	p.ensureAIExperimentsTable()
	_, err := p.db.Exec(`
INSERT INTO ai_feedback_events(ts, task, actor, action, source_hash, experiment_id, variant)
VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		time.Now().Unix(), task, actor, action, sourceHash, expID, variant)
	if err != nil {
		// fallback without new columns
		return p.insertAIFeedbackEvent(task, actor, action, sourceHash)
	}
	return true
}

func (p *pgStore) aiExperimentStats(expID string, sinceTs int64) []map[string]any {
	out := []map[string]any{}
	if p == nil || p.db == nil || expID == "" {
		return out
	}
	rows, err := p.db.Query(`
SELECT COALESCE(NULLIF(variant,''),'(none)'),
       COUNT(*),
       COALESCE(SUM(CASE WHEN action='helpful' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN action='unhelpful' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN action='applied' THEN 1 ELSE 0 END),0)
FROM ai_feedback_events WHERE experiment_id=$1 AND ts>=$2
GROUP BY 1 ORDER BY COUNT(*) DESC`, expID, sinceTs)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var variant string
		var total, helpful, unhelpful, applied int64
		if rows.Scan(&variant, &total, &helpful, &unhelpful, &applied) != nil {
			continue
		}
		rate := 0.0
		if total > 0 {
			rate = float64(helpful) / float64(total)
		}
		out = append(out, map[string]any{
			"variant": variant, "total": total, "helpful": helpful,
			"unhelpful": unhelpful, "applied": applied, "helpful_rate": rate,
		})
	}
	noteRowsErr("aiExperimentStats", rows)
	return out
}

// pickAssistExperiment returns experiment id + variant for an actor/task.
func (s *Server) pickAssistExperiment(cfg AIConfig, task, actor string) (expID, variant string) {
	expID = strings.TrimSpace(cfg.ActiveExperimentID)
	if expID == "" || s.pg == nil {
		return "", ""
	}
	exp, ok := s.pg.getAIExperiment(expID)
	if !ok || !exp.Enabled {
		return "", ""
	}
	if exp.Task != "" && !strings.EqualFold(exp.Task, task) {
		return "", ""
	}
	variant = assignExperimentVariant(exp.ID, actor, exp.Variants)
	return exp.ID, variant
}

func (s *Server) handleAIExperimentStats(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		id = strings.TrimSpace(s.cfg.AIConfig().ActiveExperimentID)
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days).Unix()
	var rows []map[string]any
	if s.pg != nil {
		rows = s.pg.aiExperimentStats(id, since)
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"experiment_id": id, "days": days, "variants": rows})
}

func (s *Server) handleListAIExperiments(w http.ResponseWriter, r *http.Request) {
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"experiments": []any{}})
		return
	}
	list := s.pg.listAIExperiments()
	if list == nil {
		list = []aiExperiment{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"experiments": list})
}

func (s *Server) handleUpsertAIExperiment(w http.ResponseWriter, r *http.Request) {
	if s.pg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pg unavailable"})
		return
	}
	var exp aiExperiment
	if err := json.NewDecoder(r.Body).Decode(&exp); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if id := strings.TrimSpace(r.PathValue("id")); id != "" {
		exp.ID = id
	}
	exp.ID = strings.TrimSpace(exp.ID)
	if exp.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	if exp.Name == "" {
		exp.Name = exp.ID
	}
	if len(exp.Variants) == 0 {
		exp.Variants = map[string]int{"control": 100}
	}
	if err := s.pg.upsertAIExperiment(exp); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "experiment": exp})
}

func (s *Server) handleDeleteAIExperiment(w http.ResponseWriter, r *http.Request) {
	if s.pg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pg unavailable"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	if err := s.pg.deleteAIExperiment(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (p *pgStore) listAIExperiments() []aiExperiment {
	if p == nil || p.db == nil {
		return nil
	}
	p.ensureAIExperimentsTable()
	rows, err := p.db.Query(`SELECT data FROM ai_experiments ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []aiExperiment
	for rows.Next() {
		var raw []byte
		var exp aiExperiment
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &exp) != nil {
			continue
		}
		out = append(out, exp)
	}
	noteRowsErr("listAIExperiments", rows)
	return out
}

func (p *pgStore) deleteAIExperiment(id string) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("pg unavailable")
	}
	_, err := p.db.Exec(`DELETE FROM ai_experiments WHERE id=$1`, id)
	return err
}
