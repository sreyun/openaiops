package main

import (
	"net/http"
	"strconv"
	"time"
)

// GET /api/v1/incidents/{id}/case-export — compact case package (timeline / verify / links).
func (s *Server) handleIncidentCaseExport(w http.ResponseWriter, r *http.Request) {
	id, ok := sreParseID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_id")})
		return
	}
	inc, found := s.incidents.Get(id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": Tr(r, "incident.not_found")})
		return
	}
	if !s.requireIncidentAccess(w, r, inc.HostID) {
		return
	}
	out := map[string]any{
		"incident":    inc,
		"loop":        inc.Loop,
		"exported_at": time.Now().Unix(),
	}
	var runs []AIRun
	if s.pg != nil {
		if list, err := s.pg.listAIRuns("", 100); err == nil {
			for _, run := range list {
				if run.IncidentID == id {
					runs = append(runs, run)
				}
			}
		}
	}
	out["ai_runs"] = runs

	var changes []ChangeRecord
	seenCh := map[int64]bool{}
	if s.changes != nil {
		if inc.Loop != nil && inc.Loop.ChangeID > 0 {
			if c, ok := s.changes.Get(inc.Loop.ChangeID); ok {
				changes = append(changes, c)
				seenCh[c.ID] = true
			}
		}
		for _, c := range s.changes.RelatedToHosts([]string{inc.HostID}, 0) {
			if seenCh[c.ID] {
				continue
			}
			for _, lid := range c.LinkedIncidentIDs {
				if lid == id {
					changes = append(changes, c)
					seenCh[c.ID] = true
					break
				}
			}
		}
	}
	out["changes"] = changes

	var sqlIDs []string
	for _, l := range inc.Links {
		if l.Type == "sql_change" && l.ID != "" {
			sqlIDs = append(sqlIDs, l.ID)
		}
	}
	out["sql_change_ids"] = sqlIDs

	var termSessions []termSessionInfo
	if s.term != nil {
		for _, sess := range s.term.listSessions() {
			if sess.HostID == inc.HostID || sess.IncidentID == id {
				termSessions = append(termSessions, sess)
			}
		}
	}
	out["terminal_sessions"] = termSessions

	var deskIDs []string
	if s.desk != nil {
		for _, d := range s.desk.listSessions() {
			if d.HostID == inc.HostID {
				deskIDs = append(deskIDs, d.ID)
			}
		}
	}
	out["desktop_session_ids"] = deskIDs

	if inc.Loop != nil && inc.Loop.RemediationRunID > 0 {
		if run, ok := s.findRemediationRun(inc.Loop.RemediationRunID); ok {
			out["remediation_run"] = run
		}
	}

	w.Header().Set("Content-Disposition", "attachment; filename=incident-"+strconv.FormatInt(id, 10)+"-case.json")
	writeJSON(w, http.StatusOK, out)
}
