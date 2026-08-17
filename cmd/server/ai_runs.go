package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AIRun is the durable Wave-2 unit of AI work (Assist / Diagnose / Sreyun).
type AIRun struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"` // assist | diagnose | sreyun | dashboard
	Task         string          `json:"task,omitempty"`
	Actor        string          `json:"actor,omitempty"`
	Model        string          `json:"model,omitempty"`
	Input        string          `json:"input,omitempty"`
	Answer       string          `json:"answer,omitempty"`
	ContentHash  string          `json:"content_hash,omitempty"`
	VerifyJSON   json.RawMessage `json:"verify,omitempty"`
	MetaJSON     json.RawMessage `json:"meta,omitempty"` // tool_turns / fallback_model / citations…
	OK           bool            `json:"ok"`
	LatencyMs    int64           `json:"latency_ms,omitempty"`
	MemHits      int             `json:"memory_hits,omitempty"`
	SkillHits    int             `json:"skill_hits,omitempty"`
	Feedback     string          `json:"feedback,omitempty"` // applied|helpful|unhelpful|""
	IncidentID   int64           `json:"incident_id,omitempty"`
	DataSourceID string          `json:"datasource_id,omitempty"`
	CreatedAt    int64           `json:"created_at"`
	UpdatedAt    int64           `json:"updated_at"`
}

func (s *Server) persistAIRun(run AIRun) {
	if s == nil || strings.TrimSpace(run.ID) == "" {
		return
	}
	now := time.Now().Unix()
	if run.CreatedAt == 0 {
		run.CreatedAt = now
	}
	run.UpdatedAt = now
	if run.ContentHash == "" && (run.Input != "" || run.Answer != "") {
		run.ContentHash = memoryContentHash(run.Input + "\n" + run.Answer)
	}
	// Always keep hot cache for feedback binding.
	//
	// 所有 kind 都进热缓存，不只是 assist：PG 落库是 go 异步的，而反馈按钮和闭环
	// 动作按钮就挂在刚吐完的那条回答下面，用户点得比 upsert 快。只缓存 assist 时，
	// Hermes 会话的反馈与「建工单」会随机撞上「run 无效或已过期」。
	if s.assistStore != nil {
		s.assistStore.putWithID(run.ID, run.Kind, run.Task, run.Input, run.Answer, run.Actor)
	}
	if s.pg == nil {
		return
	}
	go s.pg.upsertAIRun(run)
}

func (s *Server) lookupAIRun(id string) (AIRun, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return AIRun{}, false
	}
	if s.assistStore != nil {
		if rec, ok := s.assistStore.get(id); ok {
			return AIRun{
				ID: rec.ID, Kind: rec.Kind, Task: rec.Task, Actor: rec.Actor,
				Input: rec.Input, Answer: rec.Answer, ContentHash: rec.Hash,
				CreatedAt: rec.CreatedAt, UpdatedAt: rec.CreatedAt,
			}, true
		}
	}
	if s.pg != nil {
		if run, ok := s.pg.getAIRun(id); ok {
			return run, true
		}
	}
	return AIRun{}, false
}

// GET /api/v1/ai/runs?limit=50&kind=
func (s *Server) handleListAIRuns(w http.ResponseWriter, r *http.Request) {
	if s.pg == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	list, err := s.pg.listAIRuns(kind, limit)
	if err != nil || list == nil {
		list = []AIRun{}
	}
	// Trim bulky answer bodies for list view.
	for i := range list {
		list[i].Answer = truncateRun(list[i].Answer, 400)
		list[i].Input = truncateRun(list[i].Input, 200)
	}
	if u, ok := s.currentUser(r); ok && roleRank(u.Role) < roleRank(RoleAdmin) {
		filtered := list[:0]
		for _, run := range list {
			if run.Actor == "" || strings.EqualFold(run.Actor, u.Username) {
				filtered = append(filtered, run)
			}
		}
		list = filtered
	}
	writeJSON(w, http.StatusOK, list)
}

// GET /api/v1/ai/runs/{id}
func (s *Server) handleGetAIRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, ok := s.lookupAIRun(id)
	if !ok {
		writeAPIError(w, r, http.StatusNotFound, "not_found", "run 不存在或已过期")
		return
	}
	if u, ok := s.currentUser(r); ok && roleRank(u.Role) < roleRank(RoleAdmin) {
		if run.Actor != "" && !strings.EqualFold(run.Actor, u.Username) {
			writeAPIError(w, r, http.StatusForbidden, "forbidden", "无权查看该 AI 运行记录")
			return
		}
	}
	writeJSON(w, http.StatusOK, run)
}

func (p *pgStore) upsertAIRun(run AIRun) {
	if p == nil || p.db == nil || run.ID == "" {
		return
	}
	verify := []byte(run.VerifyJSON)
	if len(verify) == 0 {
		verify = []byte("null")
	}
	meta := []byte(run.MetaJSON)
	if len(meta) == 0 {
		meta = []byte("null")
	}
	_, err := p.db.Exec(`
INSERT INTO ai_runs(id, kind, task, actor, model, input, answer, content_hash, verify_json, meta_json, ok,
	latency_ms, memory_hits, skill_hits, feedback, incident_id, datasource_id, created_at, updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb,$11,$12,$13,$14,$15,$16,$17,$18,$19)
ON CONFLICT (id) DO UPDATE SET
	answer=EXCLUDED.answer, verify_json=EXCLUDED.verify_json, meta_json=COALESCE(EXCLUDED.meta_json, ai_runs.meta_json), ok=EXCLUDED.ok,
	latency_ms=EXCLUDED.latency_ms, memory_hits=EXCLUDED.memory_hits, skill_hits=EXCLUDED.skill_hits,
	feedback=COALESCE(NULLIF(EXCLUDED.feedback,''), ai_runs.feedback),
	model=COALESCE(NULLIF(EXCLUDED.model,''), ai_runs.model),
	updated_at=EXCLUDED.updated_at`,
		run.ID, run.Kind, run.Task, run.Actor, run.Model,
		truncateRun(run.Input, 16000), truncateRun(run.Answer, 64000), run.ContentHash,
		string(verify), string(meta), run.OK, run.LatencyMs, run.MemHits, run.SkillHits, run.Feedback,
		run.IncidentID, run.DataSourceID, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		// best-effort (meta_json column may be missing on very old DBs until migrate)
		_, _ = p.db.Exec(`
INSERT INTO ai_runs(id, kind, task, actor, model, input, answer, content_hash, verify_json, ok,
	latency_ms, memory_hits, skill_hits, feedback, incident_id, datasource_id, created_at, updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12,$13,$14,$15,$16,$17,$18)
ON CONFLICT (id) DO UPDATE SET
	answer=EXCLUDED.answer, verify_json=EXCLUDED.verify_json, ok=EXCLUDED.ok,
	latency_ms=EXCLUDED.latency_ms, memory_hits=EXCLUDED.memory_hits, skill_hits=EXCLUDED.skill_hits,
	feedback=COALESCE(NULLIF(EXCLUDED.feedback,''), ai_runs.feedback),
	updated_at=EXCLUDED.updated_at`,
			run.ID, run.Kind, run.Task, run.Actor, run.Model,
			truncateRun(run.Input, 16000), truncateRun(run.Answer, 64000), run.ContentHash,
			string(verify), run.OK, run.LatencyMs, run.MemHits, run.SkillHits, run.Feedback,
			run.IncidentID, run.DataSourceID, run.CreatedAt, run.UpdatedAt)
	}
}

func (p *pgStore) getAIRun(id string) (AIRun, bool) {
	var run AIRun
	var verify, meta []byte
	err := p.db.QueryRow(`
SELECT id, kind, task, actor, model, input, answer, content_hash, verify_json, COALESCE(meta_json,'null'), ok,
       latency_ms, memory_hits, skill_hits, COALESCE(feedback,''), COALESCE(incident_id,0),
       COALESCE(datasource_id,''), created_at, updated_at
FROM ai_runs WHERE id=$1`, id).Scan(
		&run.ID, &run.Kind, &run.Task, &run.Actor, &run.Model, &run.Input, &run.Answer,
		&run.ContentHash, &verify, &meta, &run.OK, &run.LatencyMs, &run.MemHits, &run.SkillHits,
		&run.Feedback, &run.IncidentID, &run.DataSourceID, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		// legacy schema without meta_json
		err = p.db.QueryRow(`
SELECT id, kind, task, actor, model, input, answer, content_hash, verify_json, ok,
       latency_ms, memory_hits, skill_hits, COALESCE(feedback,''), COALESCE(incident_id,0),
       COALESCE(datasource_id,''), created_at, updated_at
FROM ai_runs WHERE id=$1`, id).Scan(
			&run.ID, &run.Kind, &run.Task, &run.Actor, &run.Model, &run.Input, &run.Answer,
			&run.ContentHash, &verify, &run.OK, &run.LatencyMs, &run.MemHits, &run.SkillHits,
			&run.Feedback, &run.IncidentID, &run.DataSourceID, &run.CreatedAt, &run.UpdatedAt)
		if err != nil {
			return AIRun{}, false
		}
		run.VerifyJSON = verify
		return run, true
	}
	run.VerifyJSON = verify
	run.MetaJSON = meta
	return run, true
}

func (p *pgStore) listAIRuns(kind string, limit int) ([]AIRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `
SELECT id, kind, task, actor, model, input, answer, content_hash, verify_json, COALESCE(meta_json,'null'), ok,
       latency_ms, memory_hits, skill_hits, COALESCE(feedback,''), COALESCE(incident_id,0),
       COALESCE(datasource_id,''), created_at, updated_at
FROM ai_runs`
	args := []any{}
	if kind != "" {
		q += ` WHERE kind=$1`
		args = append(args, kind)
		q += ` ORDER BY created_at DESC LIMIT $2`
		args = append(args, limit)
	} else {
		q += ` ORDER BY created_at DESC LIMIT $1`
		args = append(args, limit)
	}
	rows, err := p.db.Query(q, args...)
	if err != nil {
		return p.listAIRunsLegacy(kind, limit)
	}
	defer rows.Close()
	var out []AIRun
	for rows.Next() {
		var run AIRun
		var verify, meta []byte
		if err := rows.Scan(
			&run.ID, &run.Kind, &run.Task, &run.Actor, &run.Model, &run.Input, &run.Answer,
			&run.ContentHash, &verify, &meta, &run.OK, &run.LatencyMs, &run.MemHits, &run.SkillHits,
			&run.Feedback, &run.IncidentID, &run.DataSourceID, &run.CreatedAt, &run.UpdatedAt); err == nil {
			run.VerifyJSON = verify
			run.MetaJSON = meta
			out = append(out, run)
		}
	}
	return out, rows.Err()
}

func (p *pgStore) listAIRunsLegacy(kind string, limit int) ([]AIRun, error) {
	q := `
SELECT id, kind, task, actor, model, input, answer, content_hash, verify_json, ok,
       latency_ms, memory_hits, skill_hits, COALESCE(feedback,''), COALESCE(incident_id,0),
       COALESCE(datasource_id,''), created_at, updated_at
FROM ai_runs`
	args := []any{}
	if kind != "" {
		q += ` WHERE kind=$1 ORDER BY created_at DESC LIMIT $2`
		args = append(args, kind, limit)
	} else {
		q += ` ORDER BY created_at DESC LIMIT $1`
		args = append(args, limit)
	}
	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AIRun
	for rows.Next() {
		var run AIRun
		var verify []byte
		if err := rows.Scan(
			&run.ID, &run.Kind, &run.Task, &run.Actor, &run.Model, &run.Input, &run.Answer,
			&run.ContentHash, &verify, &run.OK, &run.LatencyMs, &run.MemHits, &run.SkillHits,
			&run.Feedback, &run.IncidentID, &run.DataSourceID, &run.CreatedAt, &run.UpdatedAt); err == nil {
			run.VerifyJSON = verify
			out = append(out, run)
		}
	}
	return out, rows.Err()
}

// listAIRunsSince returns runs created at/after since (unix), newest first.
func (p *pgStore) listAIRunsSince(since int64, limit int) ([]AIRun, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := p.db.Query(`
SELECT id, kind, task, actor, model, input, answer, content_hash, verify_json, COALESCE(meta_json,'null'), ok,
       latency_ms, memory_hits, skill_hits, COALESCE(feedback,''), COALESCE(incident_id,0),
       COALESCE(datasource_id,''), created_at, updated_at
FROM ai_runs WHERE created_at >= $1 ORDER BY created_at DESC LIMIT $2`, since, limit)
	if err != nil {
		list, err2 := p.listAIRuns("", limit)
		if err2 != nil {
			return nil, err
		}
		var out []AIRun
		for _, r := range list {
			if r.CreatedAt >= since {
				out = append(out, r)
			}
		}
		return out, nil
	}
	defer rows.Close()
	var out []AIRun
	for rows.Next() {
		var run AIRun
		var verify, meta []byte
		if err := rows.Scan(
			&run.ID, &run.Kind, &run.Task, &run.Actor, &run.Model, &run.Input, &run.Answer,
			&run.ContentHash, &verify, &meta, &run.OK, &run.LatencyMs, &run.MemHits, &run.SkillHits,
			&run.Feedback, &run.IncidentID, &run.DataSourceID, &run.CreatedAt, &run.UpdatedAt); err == nil {
			run.VerifyJSON = verify
			run.MetaJSON = meta
			out = append(out, run)
		}
	}
	return out, rows.Err()
}

func (p *pgStore) markAIRunFeedback(id, action string) {
	if p == nil || p.db == nil || id == "" {
		return
	}
	_, _ = p.db.Exec(`UPDATE ai_runs SET feedback=$2, updated_at=$3 WHERE id=$1`,
		id, action, time.Now().Unix())
}

func verifyJSONBytes(v *assistVerifyResult) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

