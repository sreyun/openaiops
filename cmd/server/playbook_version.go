package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type playbookRevision struct {
	PlaybookID string   `json:"playbook_id"`
	Rev        int      `json:"rev"`
	SavedAt    int64    `json:"saved_at"`
	Actor      string   `json:"actor,omitempty"`
	Name       string   `json:"name"`
	Snapshot   Playbook `json:"snapshot"`
}

func (p *pgStore) ensurePlaybookRevisionsTable() {
	if p == nil || p.db == nil {
		return
	}
	_, _ = p.db.Exec(`
CREATE TABLE IF NOT EXISTS playbook_revisions (
  playbook_id TEXT NOT NULL,
  rev INT NOT NULL,
  saved_at BIGINT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  data JSONB NOT NULL,
  PRIMARY KEY (playbook_id, rev)
);
CREATE INDEX IF NOT EXISTS playbook_revisions_id_saved ON playbook_revisions(playbook_id, saved_at DESC);
`)
}

func (p *pgStore) savePlaybookRevision(pb Playbook, actor string) (int, error) {
	if p == nil || p.db == nil || pb.ID == "" {
		return 0, fmt.Errorf("pg unavailable")
	}
	p.ensurePlaybookRevisionsTable()
	var rev int
	_ = p.db.QueryRow(`SELECT COALESCE(MAX(rev),0) FROM playbook_revisions WHERE playbook_id=$1`, pb.ID).Scan(&rev)
	rev++
	raw, err := json.Marshal(pb)
	if err != nil {
		return 0, err
	}
	_, err = p.db.Exec(`INSERT INTO playbook_revisions(playbook_id,rev,saved_at,actor,name,data)
VALUES($1,$2,$3,$4,$5,$6)`, pb.ID, rev, time.Now().Unix(), actor, pb.Name, raw)
	return rev, err
}

func (p *pgStore) listPlaybookRevisions(id string, limit int) []playbookRevision {
	if p == nil || p.db == nil || id == "" {
		return nil
	}
	p.ensurePlaybookRevisionsTable()
	if limit <= 0 {
		limit = 50
	}
	rows, err := p.db.Query(`SELECT playbook_id,rev,saved_at,actor,name,data FROM playbook_revisions
WHERE playbook_id=$1 ORDER BY rev DESC LIMIT $2`, id, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []playbookRevision
	for rows.Next() {
		var r playbookRevision
		var raw []byte
		if rows.Scan(&r.PlaybookID, &r.Rev, &r.SavedAt, &r.Actor, &r.Name, &raw) != nil {
			continue
		}
		_ = json.Unmarshal(raw, &r.Snapshot)
		out = append(out, r)
	}
	noteRowsErr("listPlaybookRevisions", rows)
	return out
}

func (p *pgStore) getPlaybookRevision(id string, rev int) (playbookRevision, bool) {
	var r playbookRevision
	if p == nil || p.db == nil {
		return r, false
	}
	var raw []byte
	err := p.db.QueryRow(`SELECT playbook_id,rev,saved_at,actor,name,data FROM playbook_revisions
WHERE playbook_id=$1 AND rev=$2`, id, rev).Scan(&r.PlaybookID, &r.Rev, &r.SavedAt, &r.Actor, &r.Name, &raw)
	if err != nil || json.Unmarshal(raw, &r.Snapshot) != nil {
		return r, false
	}
	return r, true
}

func (s *Server) handleListPlaybookRevisions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.pg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"revisions": []any{}})
		return
	}
	list := s.pg.listPlaybookRevisions(id, 50)
	// omit heavy snapshot bodies in list
	slim := make([]map[string]any, 0, len(list))
	for _, x := range list {
		slim = append(slim, map[string]any{
			"playbook_id": x.PlaybookID, "rev": x.Rev, "saved_at": x.SavedAt,
			"actor": x.Actor, "name": x.Name, "steps": len(x.Snapshot.Steps),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": slim})
}

func (s *Server) handleDiffPlaybookRevisions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, _ := strconv.Atoi(r.URL.Query().Get("a"))
	b, _ := strconv.Atoi(r.URL.Query().Get("b"))
	if s.pg == nil || a <= 0 || b <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need a=&b= revision numbers"})
		return
	}
	ra, oka := s.pg.getPlaybookRevision(id, a)
	rb, okb := s.pg.getPlaybookRevision(id, b)
	if !oka || !okb {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "revision not found"})
		return
	}
	ja, _ := json.MarshalIndent(ra.Snapshot, "", "  ")
	jb, _ := json.MarshalIndent(rb.Snapshot, "", "  ")
	writeJSON(w, http.StatusOK, map[string]any{
		"playbook_id": id, "a": a, "b": b,
		"a_json": string(ja), "b_json": string(jb),
		"changed": string(ja) != string(jb),
		"summary": playbookDiffSummary(ra.Snapshot, rb.Snapshot),
	})
}

func playbookDiffSummary(a, b Playbook) map[string]any {
	return map[string]any{
		"name_changed": a.Name != b.Name,
		"steps_a":      len(a.Steps),
		"steps_b":      len(b.Steps),
		"desc_changed": strings.TrimSpace(a.Description) != strings.TrimSpace(b.Description),
		"schedule_a":   a.Schedule != nil && a.Schedule.Enabled,
		"schedule_b":   b.Schedule != nil && b.Schedule.Enabled,
	}
}

func (s *Server) handleRestorePlaybookRevision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rev, _ := strconv.Atoi(r.PathValue("rev"))
	if s.pg == nil || rev <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid rev"})
		return
	}
	snap, ok := s.pg.getPlaybookRevision(id, rev)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "revision not found"})
		return
	}
	pb := snap.Snapshot
	pb.ID = id
	saved, err := s.playbooks.Upsert(pb)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_, _ = s.pg.savePlaybookRevision(saved, s.actorName(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": saved.ID, "restored_from": rev})
}
