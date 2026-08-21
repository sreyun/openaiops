package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"aiops-monitor/shared"
)

func (s *Server) handleAgentContainers(w http.ResponseWriter, r *http.Request) {
	var rep shared.ContainerReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if rep.HostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host_id required"})
		return
	}
	fp := r.Header.Get("X-Agent-Fingerprint")
	if fp == "" {
		fp = r.URL.Query().Get("fp")
	}
	if !s.forwardFingerprintOKByHost(rep.HostID, fp) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "fingerprint mismatch"})
		return
	}
	hostname := rep.HostName
	if h := s.hostByID(rep.HostID); h != nil && h.Hostname != "" {
		hostname = h.Hostname
	}
	if rep.Error != "" {
		slog.Warn("容器采集失败，保留上一份清单", "host_id", rep.HostID, "err", rep.Error)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if s.pg != nil {
		s.pg.upsertContainerInventory(rep.HostID, hostname, rep.Runtime, rep.Containers)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleContainerList(w http.ResponseWriter, r *http.Request) {
	limit, offset, paged := parseListLimitOffset(r)
	if s.pg == nil {
		if paged {
			writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "total": 0, "limit": limit, "offset": normalizeListOffsetForTotal(offset, 0), "compose_projects": []string{}, "ts": time.Now().Unix()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"inventories": []any{}})
		return
	}
	host := r.URL.Query().Get("host")
	status := r.URL.Query().Get("status")
	query := r.URL.Query().Get("q")
	composeProject := r.URL.Query().Get("compose_project")
	composeService := r.URL.Query().Get("compose_service")
	var rows []map[string]any
	var err error
	if host != "" {
		if !s.requireHostAccess(w, r, host) {
			return
		}
		if inv, ok := s.pg.getContainerInventory(host); ok {
			rows = []map[string]any{inv}
		}
	} else {
		rows, err = s.pg.getAllContainerInventories()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
			return
		}
		rows = s.filterInventoryRows(r, rows)
	}
	if paged {
		items, total, projects := flattenContainerInventoriesPage(rows, status, query, composeProject, composeService, limit, offset)
		writeJSON(w, http.StatusOK, map[string]any{
			"items":            items,
			"total":            total,
			"limit":            limit,
			"offset":           normalizeListOffsetForTotal(offset, total),
			"compose_projects": projects,
			"ts":               time.Now().Unix(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"inventories": rows, "ts": time.Now().Unix()})
}

func parseListLimitOffset(r *http.Request) (limit, offset int, paged bool) {
	q := r.URL.Query()
	if q.Get("limit") == "" && q.Get("offset") == "" && q.Get("status") == "" && q.Get("q") == "" &&
		q.Get("compose_project") == "" && q.Get("compose_service") == "" {
		return 0, 0, false
	}
	limit, _ = strconv.Atoi(q.Get("limit"))
	offset, _ = strconv.Atoi(q.Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset, true
}

func normalizeListOffsetForTotal(offset, total int) int {
	if offset < 0 {
		return 0
	}
	if offset > total {
		return total
	}
	return offset
}

func flattenContainerInventoriesPage(rows []map[string]any, status, q, composeProject, composeService string, limit, offset int) (items []map[string]any, total int, projects []string) {
	status = strings.ToLower(strings.TrimSpace(status))
	q = strings.ToLower(strings.TrimSpace(q))
	composeProject = strings.TrimSpace(composeProject)
	composeService = strings.TrimSpace(composeService)
	filtered := make([]map[string]any, 0)
	projectSet := map[string]struct{}{}
	for _, inv := range rows {
		hostID := strings.TrimSpace(fmt.Sprint(inv["host_id"]))
		hostName := strings.TrimSpace(fmt.Sprint(inv["host_name"]))
		if hostName == "" || hostName == "<nil>" {
			hostName = hostID
		}
		runtime := strings.TrimSpace(fmt.Sprint(inv["runtime"]))
		containers, _ := inv["containers"].([]any)
		for _, it := range containers {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			row := compactContainerRow(m)
			normalizeNamePrefixedContainerID(row, m)
			row["host_id"] = hostID
			row["host_name"] = hostName
			if runtime != "" && runtime != "<nil>" {
				row["runtime"] = runtime
			}
			if !containerRowMatchesStatus(row, status) || !containerRowMatchesQuery(row, q) {
				continue
			}
			if proj := strings.TrimSpace(fmt.Sprint(row["compose_project"])); proj != "" && proj != "<nil>" {
				projectSet[proj] = struct{}{}
			}
			if !containerRowMatchesCompose(row, composeProject, composeService) {
				continue
			}
			filtered = append(filtered, row)
		}
	}
	projects = make([]string, 0, len(projectSet))
	for p := range projectSet {
		projects = append(projects, p)
	}
	sort.Strings(projects)
	total = len(filtered)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total, projects
}

func containerRowMatchesCompose(row map[string]any, project, service string) bool {
	if project != "" {
		got := strings.TrimSpace(fmt.Sprint(row["compose_project"]))
		if got != project {
			return false
		}
	}
	if service != "" {
		got := strings.TrimSpace(fmt.Sprint(row["compose_service"]))
		if got != service {
			return false
		}
	}
	return true
}

func normalizeNamePrefixedContainerID(row, raw map[string]any) {
	name := strings.TrimSpace(fmt.Sprint(row["name"]))
	id := strings.TrimSpace(firstNonEmptyOrDash(fmt.Sprint(raw["id"]), fmt.Sprint(raw["Id"]), fmt.Sprint(raw["ID"])))
	if name != "" && name != "<nil>" && strings.HasPrefix(id, name+"-") {
		row["id"] = name
	}
}

func containerRowMatchesStatus(row map[string]any, status string) bool {
	key := containerStateKey(row)
	switch status {
	case "", "all":
		return true
	case "running":
		return key == "running"
	case "stopped", "exited":
		return key == "stopped"
	case "other":
		return key == "other"
	default:
		return true
	}
}

func containerStateKey(row map[string]any) string {
	raw := strings.ToLower(strings.TrimSpace(firstNonEmptyOrDash(fmt.Sprint(row["state"]), fmt.Sprint(row["status"]))))
	if raw == "" || raw == "-" || raw == "<nil>" {
		return "other"
	}
	if strings.Contains(raw, "not running") {
		return "stopped"
	}
	if fields := strings.Fields(raw); len(fields) > 0 && fields[0] == "up" {
		return "running"
	}
	if strings.Contains(raw, "running") || strings.Contains(raw, "healthy") {
		return "running"
	}
	if strings.Contains(raw, "exited") || strings.Contains(raw, "stopped") ||
		strings.Contains(raw, "dead") || strings.Contains(raw, "created") ||
		strings.Contains(raw, "removing") {
		return "stopped"
	}
	if strings.Contains(raw, "paused") || strings.Contains(raw, "restarting") {
		return "other"
	}
	return "other"
}

func containerRowMatchesQuery(row map[string]any, q string) bool {
	if q == "" {
		return true
	}
	hay := strings.ToLower(strings.Join([]string{
		fmt.Sprint(row["host_name"]),
		fmt.Sprint(row["host_id"]),
		fmt.Sprint(row["name"]),
		fmt.Sprint(row["id"]),
		fmt.Sprint(row["image"]),
		fmt.Sprint(row["state"]),
		fmt.Sprint(row["status"]),
		fmt.Sprint(row["ports"]),
		fmt.Sprint(row["runtime"]),
		fmt.Sprint(row["compose_project"]),
		fmt.Sprint(row["compose_service"]),
	}, " "))
	for _, token := range strings.Fields(q) {
		if !strings.Contains(hay, token) {
			return false
		}
	}
	return true
}

func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	hostID := strings.TrimSpace(r.PathValue("hostID"))
	cid := strings.TrimSpace(r.PathValue("id"))
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	var req struct {
		Action string `json:"action"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case "start", "stop", "restart":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be start|stop|restart"})
		return
	}
	args := map[string]string{"action": action, "id": cid, "name": strings.TrimSpace(req.Name)}
	out, err := s.runAgentModule(hostID, "container_action", args, 90)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "output": out})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: fmt.Sprintf("容器 %s：host=%s id=%s", action, hostID, cid)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	hostID := strings.TrimSpace(r.PathValue("hostID"))
	cid := strings.TrimSpace(r.PathValue("id"))
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "200"
	}
	if _, err := strconv.Atoi(tail); err != nil {
		tail = "200"
	}
	out, err := s.runAgentModule(hostID, "container_logs", map[string]string{"id": cid, "tail": tail}, 60)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "log": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": out})
}

func (s *Server) handleContainerExec(w http.ResponseWriter, r *http.Request) {
	hostID := strings.TrimSpace(r.PathValue("hostID"))
	cid := strings.TrimSpace(r.PathValue("id"))
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	var req struct {
		Command    string `json:"command"`
		Name       string `json:"name"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command required"})
		return
	}
	if len(cmd) > 2000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command too long"})
		return
	}
	args := map[string]string{"id": cid, "name": strings.TrimSpace(req.Name), "command": cmd}
	if req.TimeoutSec >= 5 && req.TimeoutSec <= 60 {
		args["timeout_sec"] = strconv.Itoa(req.TimeoutSec)
	}
	out, err := s.runAgentModule(hostID, "container_exec", args, 90)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "output": out})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: fmt.Sprintf("容器 exec：host=%s id=%s cmd=%s", hostID, cid, truncateRunes(cmd, 80))})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

func (s *Server) handleContainerComposeList(w http.ResponseWriter, r *http.Request) {
	hostID := strings.TrimSpace(r.URL.Query().Get("host"))
	if hostID == "" {
		hostID = strings.TrimSpace(r.URL.Query().Get("host_id"))
	}
	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host required"})
		return
	}
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	out, err := s.runAgentModule(hostID, "container_compose_ls", map[string]string{}, 60)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "output": out})
		return
	}
	var parsed any
	if json.Unmarshal([]byte(out), &parsed) == nil {
		writeJSON(w, http.StatusOK, map[string]any{"host_id": hostID, "data": parsed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host_id": hostID, "raw": out})
}

func (s *Server) handleContainerComposeAction(w http.ResponseWriter, r *http.Request) {
	hostID := strings.TrimSpace(r.PathValue("hostID"))
	if !s.requireHostAccess(w, r, hostID) {
		return
	}
	var req struct {
		Action     string `json:"action"`
		Project    string `json:"project"`
		File       string `json:"file"`
		Services   string `json:"services"`
		Tail       string `json:"tail"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	args := map[string]string{
		"action":   strings.TrimSpace(req.Action),
		"project":  strings.TrimSpace(req.Project),
		"file":     strings.TrimSpace(req.File),
		"services": strings.TrimSpace(req.Services),
		"tail":     strings.TrimSpace(req.Tail),
	}
	if req.TimeoutSec > 0 {
		args["timeout_sec"] = strconv.Itoa(req.TimeoutSec)
	}
	timeout := 200
	if req.TimeoutSec >= 30 && req.TimeoutSec <= 600 {
		timeout = req.TimeoutSec + 20
	}
	out, err := s.runAgentModule(hostID, "container_compose", args, timeout)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "output": out})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: fmt.Sprintf("compose %s：host=%s project=%s", req.Action, hostID, req.Project)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}
