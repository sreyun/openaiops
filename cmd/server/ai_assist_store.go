package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// assistRecord is a server-side copy of one AI Assist completion.
// Feedback must reference AssistID so clients cannot inject arbitrary RAG text.
type assistRecord struct {
	ID        string
	Kind      string
	Task      string
	Input     string
	Answer    string
	Hash      string
	Actor     string
	CreatedAt int64
	ExpiresAt int64
}

type writeApproval struct {
	ID        string
	Tool      string
	ArgsHash  string
	Actor     string
	CreatedAt int64
	ExpiresAt int64
	Used      bool
	UsedAt    int64
}

type assistStore struct {
	mu      sync.Mutex
	records map[string]assistRecord
	cap     int
}

func newAssistStore() *assistStore {
	return &assistStore{records: map[string]assistRecord{}, cap: 2000}
}

func newOpaqueID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b[:])
}

func (st *assistStore) put(task, input, answer, actor string) assistRecord {
	return st.putWithID(newOpaqueID("ast_"), "assist", task, input, answer, actor)
}

func (st *assistStore) putWithID(id, kind, task, input, answer, actor string) assistRecord {
	if st == nil {
		return assistRecord{}
	}
	if strings.TrimSpace(id) == "" {
		id = newOpaqueID("ast_")
	}
	if strings.TrimSpace(kind) == "" {
		kind = "assist"
	}
	now := time.Now().Unix()
	rec := assistRecord{
		ID:        id,
		Kind:      strings.TrimSpace(kind),
		Task:      strings.TrimSpace(task),
		Input:     input,
		Answer:    answer,
		Hash:      memoryContentHash(input + "\n" + answer),
		Actor:     actor,
		CreatedAt: now,
		ExpiresAt: now + 7*24*3600,
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.records[rec.ID] = rec
	if len(st.records) > st.cap {
		var oldest string
		var oldestTs int64 = now + 1
		for rid, r := range st.records {
			if r.CreatedAt < oldestTs {
				oldestTs = r.CreatedAt
				oldest = rid
			}
		}
		if oldest != "" {
			delete(st.records, oldest)
		}
	}
	return rec
}

func (st *assistStore) get(id string) (assistRecord, bool) {
	if st == nil || strings.TrimSpace(id) == "" {
		return assistRecord{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.records[id]
	if !ok {
		return assistRecord{}, false
	}
	if rec.ExpiresAt > 0 && time.Now().Unix() > rec.ExpiresAt {
		delete(st.records, id)
		return assistRecord{}, false
	}
	return rec, true
}

// --- write-tool approval tokens (per-action skeleton) ---

func (h *aiGovHub) issueWriteApproval(actor, tool, argsHash string, ttlSec int) writeApproval {
	if h == nil {
		return writeApproval{}
	}
	if ttlSec <= 0 {
		ttlSec = 600
	}
	now := time.Now().Unix()
	a := writeApproval{
		ID:        newOpaqueID("wap_"),
		Tool:      strings.TrimSpace(tool),
		ArgsHash:  strings.TrimSpace(argsHash),
		Actor:     actor,
		CreatedAt: now,
		ExpiresAt: now + int64(ttlSec),
	}
	h.mu.Lock()
	if h.approvals == nil {
		h.approvals = map[string]writeApproval{}
	}
	h.approvals[a.ID] = a
	// prune expired
	for id, v := range h.approvals {
		if v.Used || (v.ExpiresAt > 0 && now > v.ExpiresAt) {
			delete(h.approvals, id)
		}
	}
	pg := h.pg
	h.mu.Unlock()
	if pg != nil {
		pg.upsertWriteApproval(a)
	}
	return a
}

// consumeWriteApproval validates a one-shot approval for tool(+optional args hash).
func (h *aiGovHub) consumeWriteApproval(id, tool, argsHash string) bool {
	if h == nil || strings.TrimSpace(id) == "" {
		return false
	}
	now := time.Now().Unix()
	h.mu.Lock()
	a, ok := h.approvals[id]
	pg := h.pg
	if ok && !a.Used {
		if a.ExpiresAt > 0 && now > a.ExpiresAt {
			delete(h.approvals, id)
			h.mu.Unlock()
			return false
		}
		if a.Tool != "" && !strings.EqualFold(a.Tool, tool) {
			h.mu.Unlock()
			return false
		}
		// Fail closed: empty ArgsHash must never authorize arbitrary args.
		if strings.TrimSpace(a.ArgsHash) == "" || strings.TrimSpace(argsHash) == "" || a.ArgsHash != argsHash {
			h.mu.Unlock()
			return false
		}
		a.Used = true
		a.UsedAt = now
		delete(h.approvals, id)
		h.mu.Unlock()
		if pg != nil {
			pg.upsertWriteApproval(a)
		}
		return true
	}
	h.mu.Unlock()
	// Memory miss (e.g. after restart): try durable PG token.
	if pg != nil {
		return pg.consumeWriteApprovalPG(context.Background(), id, tool, argsHash)
	}
	return false
}

// checkAndIncrMCPRate limits MCP calls per token fingerprint (per minute).
func (h *aiGovHub) checkAndIncrMCPRate(tokenFP string, limitPerMin int) (ok bool, used, lim int) {
	if h == nil || limitPerMin <= 0 {
		return true, 0, 0
	}
	if tokenFP == "" {
		tokenFP = "mcp"
	}
	minute := time.Now().UTC().Format("2006-01-02T15:04")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mcpQuota == nil {
		h.mcpQuota = map[string]aiQuotaDay{}
	}
	cur := h.mcpQuota[tokenFP]
	if cur.Day != minute {
		cur = aiQuotaDay{Day: minute, Count: 0}
	}
	if cur.Count >= limitPerMin {
		h.mcpQuota[tokenFP] = cur
		return false, cur.Count, limitPerMin
	}
	cur.Count++
	h.mcpQuota[tokenFP] = cur
	return true, cur.Count, limitPerMin
}

func argsHashForApproval(tool string, args map[string]any) string {
	// Stable-ish fingerprint without full JSON marshal dependency on key order:
	// use tool + sorted-ish fmt of common keys.
	parts := []string{tool}
	for _, k := range []string{"action_name", "host_id", "cluster_id", "namespace", "name", "replicas", "args"} {
		if v, ok := args[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return memoryContentHash(strings.Join(parts, "|"))
}
