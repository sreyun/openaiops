package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const auditChainAdvisoryLock int64 = 0x41494f5053415544

func auditChainSecretDegraded() bool {
	_, degraded := auditChainSigningKeys()
	return degraded
}

var auditDegradedOnce sync.Once

func auditChainSecret() []byte {
	keys, degraded := auditChainSigningKeys()
	if degraded {
		auditDegradedOnce.Do(func() {
			slog.Error("audit chain using degraded default secret; set AIOPS_SECRET_KEY for compliance")
		})
	}
	return keys[0].Key
}

type auditChainVerifyResult struct {
	OK             bool    `json:"ok"`
	Status         string  `json:"status"`
	Code           string  `json:"code"`
	Checked        int     `json:"checked"`
	FromSeq        int64   `json:"from_seq"`
	ToSeq          int64   `json:"to_seq"`
	BrokenAt       int64   `json:"broken_at"`
	ChainVersions  []int16 `json:"chain_versions"`
	MirrorParity   bool    `json:"mirror_parity"`
	SecretDegraded bool    `json:"secret_degraded"`
	VerifiedAt     int64   `json:"verified_at"`
}

type auditMirrorState struct {
	LegacyHashes    int64
	PartitionHashes int64
	Drift           bool
	Conflict        bool
}

func parseAuditVerifyLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 200, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 5000 {
		return 0, fmt.Errorf("invalid audit verification limit")
	}
	return limit, nil
}

func (p *pgStore) loadAuditMirrorState(ctx context.Context) (auditMirrorState, error) {
	var state auditMirrorState
	err := p.db.QueryRowContext(ctx, `
WITH legacy AS (
  SELECT content_hash, ts, data, prev_hash, chain_seq, chain_version, chain_key_id
  FROM audit_log WHERE content_hash <> ''
), partitioned AS (
  SELECT content_hash, ts, data, prev_hash, chain_seq, chain_version, chain_key_id
  FROM audit_log_p WHERE content_hash <> ''
), combined AS (
  SELECT * FROM legacy
  UNION ALL
  SELECT * FROM partitioned
)
SELECT
  (SELECT COUNT(DISTINCT content_hash) FROM legacy) AS legacy_hashes,
  (SELECT COUNT(DISTINCT content_hash) FROM partitioned) AS partition_hashes,
  EXISTS(
    (SELECT content_hash FROM legacy EXCEPT SELECT content_hash FROM partitioned)
    UNION ALL
    (SELECT content_hash FROM partitioned EXCEPT SELECT content_hash FROM legacy)
  ) AS drift,
  EXISTS(
    SELECT 1 FROM combined GROUP BY chain_seq HAVING COUNT(DISTINCT content_hash) > 1
    UNION ALL
    SELECT 1 FROM combined GROUP BY content_hash
      HAVING COUNT(DISTINCT ROW(ts, data, prev_hash, chain_seq, chain_version, chain_key_id)) > 1
  ) AS conflict`).Scan(&state.LegacyHashes, &state.PartitionHashes, &state.Drift, &state.Conflict)
	if err != nil {
		return state, err
	}
	return state, nil
}

func (p *pgStore) verifyAuditChain(ctx context.Context, limit int) (auditChainVerifyResult, error) {
	result := auditChainVerifyResult{VerifiedAt: time.Now().Unix()}
	if p == nil || p.db == nil {
		return result, sql.ErrConnDone
	}
	if limit < 1 || limit > 5000 {
		return result, fmt.Errorf("invalid audit verification limit")
	}
	rows, err := p.db.QueryContext(ctx, `
SELECT id, ts, data, content_hash, prev_hash, chain_seq, chain_version, chain_key_id
FROM (
  SELECT id, ts, data, content_hash, prev_hash, chain_seq, chain_version, chain_key_id
  FROM audit_log_p
  WHERE content_hash <> ''
  ORDER BY chain_seq DESC, id DESC
  LIMIT $1
) latest
ORDER BY chain_seq ASC, id ASC`, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	entries := make([]auditChainEntry, 0, limit+1)
	for rows.Next() {
		var entry auditChainEntry
		if err := rows.Scan(
			&entry.ID, &entry.TS, &entry.Data, &entry.ContentHash, &entry.PrevHash,
			&entry.Seq, &entry.ChainVersion, &entry.ChainKeyID,
		); err != nil {
			return result, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}

	mirrors, err := p.loadAuditMirrorState(ctx)
	if err != nil {
		return result, err
	}
	result.MirrorParity = !mirrors.Drift && mirrors.LegacyHashes == mirrors.PartitionHashes
	if mirrors.Conflict {
		result.Status = "broken"
		result.Code = "mirror_conflict"
		return result, nil
	}
	if len(entries) == 0 {
		if mirrors.LegacyHashes == 0 && mirrors.PartitionHashes == 0 {
			result.Status = "empty"
			result.Code = "no_records"
			return result, nil
		}
		result.Status = "unverifiable"
		result.Code = "mirror_drift"
		return result, nil
	}

	keys, secretDegraded := auditChainSigningKeys()
	result.SecretDegraded = secretDegraded
	start := 0
	if len(entries) > limit {
		start = 1
	}
	legacyVersion := false
	for i, entry := range entries {
		if i >= start {
			result.Checked = i - start + 1
			if result.FromSeq == 0 {
				result.FromSeq = entry.Seq
			}
			result.ToSeq = entry.Seq
		}
		if entry.ChainVersion == auditChainVersionV1 {
			legacyVersion = true
		}
		if entry.Seq <= 0 {
			return auditChainFailure(result, "broken", "sequence_invalid", entry.Seq), nil
		}
		if i == 0 {
			if start == 0 && (entry.Seq != 1 || entry.PrevHash != "") {
				return auditChainFailure(result, "broken", "sequence_gap", entry.Seq), nil
			}
		} else {
			prev := entries[i-1]
			if entry.Seq != prev.Seq+1 {
				return auditChainFailure(result, "broken", "sequence_gap", entry.Seq), nil
			}
			if entry.PrevHash != prev.ContentHash {
				return auditChainFailure(result, "broken", "prev_hash_mismatch", entry.Seq), nil
			}
		}
		hashResult := verifyAuditEntryHash(entry, keys)
		if hashResult.Matched {
			continue
		}
		status := "broken"
		if hashResult.Code == "key_unavailable" || hashResult.Code == "legacy_key_or_content_mismatch" {
			status = "unverifiable"
		}
		return auditChainFailure(result, status, hashResult.Code, entry.Seq), nil
	}

	reported := entries[start:]
	result.Checked = len(reported)
	result.FromSeq = reported[0].Seq
	result.ToSeq = reported[len(reported)-1].Seq
	versions := make(map[int16]struct{})
	for _, entry := range reported {
		versions[entry.ChainVersion] = struct{}{}
	}
	for version := range versions {
		result.ChainVersions = append(result.ChainVersions, version)
	}
	slices.Sort(result.ChainVersions)
	result.OK = true
	result.Status = "healthy"
	result.Code = "ok"
	if !result.MirrorParity {
		result.Status = "degraded"
		result.Code = "mirror_drift"
	} else if secretDegraded {
		result.Status = "degraded"
		result.Code = "secret_degraded"
	} else if legacyVersion {
		result.Status = "degraded"
		result.Code = "legacy_chain"
	}
	return result, nil
}

func auditChainFailure(result auditChainVerifyResult, status, code string, brokenAt int64) auditChainVerifyResult {
	result.OK = false
	result.Status = status
	result.Code = code
	result.BrokenAt = brokenAt
	return result
}

func (s *Server) handleAuditVerifyChain(w http.ResponseWriter, r *http.Request) {
	limit, err := parseAuditVerifyLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, auditChainVerifyResult{
			Status: "unavailable", Code: "invalid_limit", VerifiedAt: time.Now().Unix(),
		})
		return
	}
	if s.pg == nil {
		writeJSON(w, http.StatusServiceUnavailable, auditChainVerifyResult{
			Status: "unavailable", Code: "storage_unavailable", VerifiedAt: time.Now().Unix(),
		})
		return
	}
	result, err := s.pg.verifyAuditChain(r.Context(), limit)
	if err != nil {
		slog.Error("audit chain verification unavailable", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, auditChainVerifyResult{
			Status: "unavailable", Code: "storage_unavailable", VerifiedAt: time.Now().Unix(),
		})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSecurityRewrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if !secretEncryptionEnabled() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未配置 AIOPS_SECRET_KEY"})
		return
	}
	ai := s.cfg.AIConfig()
	n := 0
	rew := func(field *string) {
		if field == nil || *field == "" {
			return
		}
		before := *field
		*field = rewrapSecretIfNeeded(*field)
		if *field != before {
			n++
		}
	}
	rew(&ai.APIKey)
	rew(&ai.EmbedAPIKey)
	rew(&ai.RerankAPIKey)
	rew(&ai.MCPToken)
	rew(&ai.WeKnoraAPIKey)
	rew(&ai.SpeechAPIKey)
	if err := s.cfg.SetAIConfig(ai); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("security rewrap completed", "fields", n)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "rewrapped_fields": n, "keys": secretKeyStatus()})
}

func (s *Server) handleSecurityKeyStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, secretKeyStatus())
}

// appendAuditChained writes one hash-chain link to both mirrors atomically.
func (p *pgStore) appendAuditChained(ctx context.Context, entry LogEntry) (int64, error) {
	var nextSeq int64
	err := p.withPgTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, auditChainAdvisoryLock); err != nil {
			return fmt.Errorf("lock audit chain: %w", err)
		}

		var prevHash string
		var tipSeq int64
		err := tx.QueryRowContext(ctx, `
SELECT content_hash, chain_seq FROM audit_log_p
WHERE content_hash <> '' ORDER BY chain_seq DESC, id DESC LIMIT 1`).Scan(&prevHash, &tipSeq)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read audit chain tip: %w", err)
		}

		if entry.Timestamp <= 0 {
			entry.Timestamp = time.Now().Unix()
		}
		payload, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode audit entry: %w", err)
		}
		payload, err = canonicalAuditPayload(payload)
		if err != nil {
			return err
		}

		nextSeq = tipSeq + 1
		keys, _ := auditChainSigningKeys()
		keyID := keys[0].ID
		contentHash := computeAuditChainHash(keys[0].Key, auditChainVersionV2, prevHash, payload, nextSeq)

		var id int64
		if err := tx.QueryRowContext(ctx, `
INSERT INTO audit_log(ts,data,content_hash,prev_hash,chain_seq,chain_version,chain_key_id)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			entry.Timestamp, payload, contentHash, prevHash, nextSeq, auditChainVersionV2, keyID).Scan(&id); err != nil {
			return fmt.Errorf("insert legacy audit mirror: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_log_p(id,ts,data,content_hash,prev_hash,chain_seq,chain_version,chain_key_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			id, entry.Timestamp, payload, contentHash, prevHash, nextSeq, auditChainVersionV2, keyID); err != nil {
			return fmt.Errorf("insert partition audit mirror: %w", err)
		}
		return nil
	})
	return nextSeq, err
}
