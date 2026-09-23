package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

//go:embed playbookpacks/*.json
var playbookPackFS embed.FS

type playbookPackFile struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Version   string     `json:"version"`
	Playbooks []Playbook `json:"playbooks"`
}

type playbookPackInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Count   int    `json:"playbook_count"`
}

func listEmbeddedPlaybookPacks() ([]playbookPackInfo, error) {
	entries, err := playbookPackFS.ReadDir("playbookpacks")
	if err != nil {
		return nil, err
	}
	var out []playbookPackInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := playbookPackFS.ReadFile("playbookpacks/" + e.Name())
		if err != nil {
			continue
		}
		var pack playbookPackFile
		if json.Unmarshal(b, &pack) != nil || pack.ID == "" {
			continue
		}
		out = append(out, playbookPackInfo{ID: pack.ID, Name: pack.Name, Version: pack.Version, Count: len(pack.Playbooks)})
	}
	return out, nil
}

func loadEmbeddedPlaybookPack(id string) (playbookPackFile, error) {
	id = strings.TrimSpace(strings.ToLower(id))
	b, err := playbookPackFS.ReadFile("playbookpacks/" + id + ".json")
	if err != nil {
		return playbookPackFile{}, fmt.Errorf("剧本包不存在: %s", id)
	}
	var pack playbookPackFile
	if err := json.Unmarshal(b, &pack); err != nil {
		return playbookPackFile{}, err
	}
	if pack.ID == "" {
		pack.ID = id
	}
	return pack, nil
}

// importPlaybookPack upserts embedded playbooks into config. IDs are prefixed
// with pack:<id>: so re-import updates the same rows without colliding with
// operator-authored playbooks.
func (s *Server) importPlaybookPack(packID string) (imported, skipped int, err error) {
	if s == nil || s.playbooks == nil {
		return 0, 0, fmt.Errorf("playbook manager unavailable")
	}
	pack, err := loadEmbeddedPlaybookPack(packID)
	if err != nil {
		return 0, 0, err
	}
	now := time.Now().Unix()
	for _, pb := range pack.Playbooks {
		name := strings.TrimSpace(pb.Name)
		if name == "" || len(pb.Steps) == 0 {
			skipped++
			continue
		}
		id := strings.TrimSpace(pb.ID)
		if id == "" {
			id = slugPlaybookID(name)
		}
		if !strings.HasPrefix(id, "pack:") {
			id = "pack:" + pack.ID + ":" + id
		}
		pb.ID = id
		pb.Name = name
		if pb.CreatedAt == 0 {
			pb.CreatedAt = now
		}
		pb.UpdatedAt = now
		if _, err := s.playbooks.Upsert(pb); err != nil {
			skipped++
			continue
		}
		imported++
	}
	return imported, skipped, nil
}

func slugPlaybookID(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r == ' ' || r == '-' || r == '_' {
			return '-'
		}
		return -1
	}, s)
	s = strings.Trim(s, "-")
	if s == "" {
		return fmt.Sprintf("pb-%d", time.Now().UnixNano()%1e9)
	}
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// GET /api/v1/playbooks/packs
func (s *Server) handleListPlaybookPacks(w http.ResponseWriter, r *http.Request) {
	list, err := listEmbeddedPlaybookPacks()
	if err != nil || list == nil {
		list = []playbookPackInfo{}
	}
	writeJSON(w, http.StatusOK, list)
}

// POST /api/v1/playbooks/packs/import  {id:"selfheal"} or {ids:[...]}
func (s *Server) handleImportPlaybookPacks(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	// Embedded packs target "all". Importing them via Upsert would let a
	// host-scoped operator plant fleet-wide playbooks (and later enable a
	// schedule) without ever passing requirePlaybookTargetsAccess.
	if u, ok := s.currentUser(r); ok && u.hostScopeRestricted() && roleRank(u.Role) < roleRank(RoleAdmin) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "主机范围授权账号不能导入全量目标剧本包"})
		return
	}
	var req struct {
		ID  string   `json:"id"`
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	ids := append([]string{}, req.IDs...)
	if strings.TrimSpace(req.ID) != "" {
		ids = append(ids, req.ID)
	}
	if len(ids) == 0 {
		all, _ := listEmbeddedPlaybookPacks()
		for _, p := range all {
			ids = append(ids, p.ID)
		}
	}
	type one struct {
		ID       string `json:"id"`
		Imported int    `json:"imported"`
		Skipped  int    `json:"skipped"`
		Error    string `json:"error,omitempty"`
	}
	var results []one
	totalIn, totalSkip := 0, 0
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		in, sk, err := s.importPlaybookPack(id)
		item := one{ID: id, Imported: in, Skipped: sk}
		if err != nil {
			item.Error = err.Error()
		}
		totalIn += in
		totalSkip += sk
		results = append(results, item)
	}
	s.store.AddLog(LogEntry{
		Kind: KindSystem, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r),
		Message: fmt.Sprintf("imported playbook packs: +%d skipped=%d", totalIn, totalSkip),
	})
	writeJSON(w, http.StatusOK, map[string]any{"imported": totalIn, "skipped": totalSkip, "results": results})
}
