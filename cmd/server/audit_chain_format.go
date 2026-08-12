package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	auditChainVersionV1 int16 = 1
	auditChainVersionV2 int16 = 2
)

type auditChainEntry struct {
	ID           int64
	TS           int64
	Data         []byte
	ContentHash  string
	PrevHash     string
	Seq          int64
	ChainVersion int16
	ChainKeyID   string
}

type auditHashResult struct {
	Matched bool
	Code    string
	KeyID   string
}

func canonicalAuditPayload(raw []byte) ([]byte, error) {
	var entry LogEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, fmt.Errorf("decode audit payload: %w", err)
	}
	if entry.Timestamp <= 0 || strings.TrimSpace(entry.Kind) == "" || strings.TrimSpace(entry.Message) == "" {
		return nil, fmt.Errorf("invalid audit payload")
	}
	out, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("encode audit payload: %w", err)
	}
	return out, nil
}

func auditChainSigningKeys() ([]secretKeyEntry, bool) {
	keys := loadAllSecretKeys()
	if len(loadSecretKey()) > 0 {
		return keys, false
	}
	sum := sha256.Sum256([]byte("aiops-audit-chain-default"))
	return append([]secretKeyEntry{{ID: currentSecretKeyID(), Key: sum[:]}}, keys...), true
}

func computeAuditChainHash(key []byte, version int16, prevHash string, payload []byte, seq int64) string {
	mac := hmac.New(sha256.New, key)
	if version == auditChainVersionV2 {
		mac.Write([]byte("aiops-audit-chain/v2"))
		mac.Write([]byte{0})
	}
	mac.Write([]byte(prevHash))
	mac.Write([]byte{0})
	mac.Write(payload)
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(seq, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyAuditEntryHash(entry auditChainEntry, keys []secretKeyEntry) auditHashResult {
	payload, err := canonicalAuditPayload(entry.Data)
	if err != nil {
		return auditHashResult{Code: "invalid_payload"}
	}
	switch entry.ChainVersion {
	case auditChainVersionV1:
		for _, key := range keys {
			got := computeAuditChainHash(key.Key, auditChainVersionV1, entry.PrevHash, payload, entry.Seq)
			if hmac.Equal([]byte(got), []byte(entry.ContentHash)) {
				return auditHashResult{Matched: true, Code: "ok", KeyID: key.ID}
			}
		}
		return auditHashResult{Code: "legacy_key_or_content_mismatch"}
	case auditChainVersionV2:
		for _, key := range keys {
			if key.ID != entry.ChainKeyID {
				continue
			}
			got := computeAuditChainHash(key.Key, auditChainVersionV2, entry.PrevHash, payload, entry.Seq)
			if hmac.Equal([]byte(got), []byte(entry.ContentHash)) {
				return auditHashResult{Matched: true, Code: "ok", KeyID: key.ID}
			}
			return auditHashResult{Code: "content_hash_mismatch", KeyID: key.ID}
		}
		return auditHashResult{Code: "key_unavailable", KeyID: entry.ChainKeyID}
	default:
		return auditHashResult{Code: "unsupported_chain_version"}
	}
}
