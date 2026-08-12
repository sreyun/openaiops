package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestCanonicalAuditPayloadNormalizesJSONBFormatting(t *testing.T) {
	compact := []byte(`{"timestamp":1786284000,"kind":"operation","level":"info","actor":"admin","message":"saved"}`)
	jsonb := []byte(`{ "kind": "operation", "actor": "admin", "level": "info", "message": "saved", "timestamp": 1786284000 }`)

	gotCompact, err := canonicalAuditPayload(compact)
	if err != nil {
		t.Fatal(err)
	}
	gotJSONB, err := canonicalAuditPayload(jsonb)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCompact) != string(gotJSONB) {
		t.Fatalf("canonical payload differs: %s != %s", gotCompact, gotJSONB)
	}
	if want := string(compact); string(gotCompact) != want {
		t.Fatalf("canonical payload = %s, want %s", gotCompact, want)
	}
}

func TestCanonicalAuditPayloadRejectsMalformedOrIncompleteEntries(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed", raw: `{"timestamp":`},
		{name: "missing timestamp", raw: `{"kind":"operation","level":"info","actor":"admin","message":"saved"}`},
		{name: "missing kind", raw: `{"timestamp":1,"level":"info","actor":"admin","message":"saved"}`},
		{name: "missing message", raw: `{"timestamp":1,"kind":"operation","level":"info","actor":"admin"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := canonicalAuditPayload([]byte(tt.raw)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestAuditChainV1MatchesHistoricalFraming(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	payload := []byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"x"}`)

	got := computeAuditChainHash(key, auditChainVersionV1, "prev", payload, 7)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("prev\x00"))
	mac.Write(payload)
	mac.Write([]byte("\x007"))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
}

func TestAuditChainV2UsesDomainSeparatedFraming(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	payload := []byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"x"}`)

	got := computeAuditChainHash(key, auditChainVersionV2, "prev", payload, 7)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("aiops-audit-chain/v2\x00prev\x00"))
	mac.Write(payload)
	mac.Write([]byte("\x007"))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
	if got == computeAuditChainHash(key, auditChainVersionV1, "prev", payload, 7) {
		t.Fatal("v1 and v2 hashes must differ")
	}
}

func TestVerifyAuditEntryHashHandlesVersionedKeys(t *testing.T) {
	current := secretKeyEntry{ID: "current", Key: []byte("11111111111111111111111111111111")}
	previous := secretKeyEntry{ID: "previous", Key: []byte("22222222222222222222222222222222")}
	payload := []byte(`{ "message": "x", "actor": "admin", "level": "info", "kind": "operation", "timestamp": 1 }`)
	canonical := []byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"x"}`)

	t.Run("v1 tries previous key", func(t *testing.T) {
		entry := auditChainEntry{
			Data: payload, PrevHash: "prev", Seq: 7, ChainVersion: auditChainVersionV1,
			ContentHash: computeAuditChainHash(previous.Key, auditChainVersionV1, "prev", canonical, 7),
		}
		got := verifyAuditEntryHash(entry, []secretKeyEntry{current, previous})
		if !got.Matched || got.Code != "ok" || got.KeyID != "previous" {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("v1 mismatch remains ambiguous", func(t *testing.T) {
		entry := auditChainEntry{Data: payload, ContentHash: "bad", PrevHash: "prev", Seq: 7, ChainVersion: auditChainVersionV1}
		got := verifyAuditEntryHash(entry, []secretKeyEntry{current, previous})
		if got.Matched || got.Code != "legacy_key_or_content_mismatch" {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("v2 selects key id", func(t *testing.T) {
		entry := auditChainEntry{
			Data: payload, PrevHash: "prev", Seq: 7, ChainVersion: auditChainVersionV2, ChainKeyID: "previous",
			ContentHash: computeAuditChainHash(previous.Key, auditChainVersionV2, "prev", canonical, 7),
		}
		got := verifyAuditEntryHash(entry, []secretKeyEntry{current, previous})
		if !got.Matched || got.Code != "ok" || got.KeyID != "previous" {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("v2 missing key is unverifiable", func(t *testing.T) {
		entry := auditChainEntry{Data: payload, ContentHash: "bad", PrevHash: "prev", Seq: 7, ChainVersion: auditChainVersionV2, ChainKeyID: "missing"}
		got := verifyAuditEntryHash(entry, []secretKeyEntry{current, previous})
		if got.Matched || got.Code != "key_unavailable" {
			t.Fatalf("result = %+v", got)
		}
	})

	t.Run("v2 present key mismatch is broken", func(t *testing.T) {
		entry := auditChainEntry{Data: payload, ContentHash: "bad", PrevHash: "prev", Seq: 7, ChainVersion: auditChainVersionV2, ChainKeyID: "current"}
		got := verifyAuditEntryHash(entry, []secretKeyEntry{current, previous})
		if got.Matched || got.Code != "content_hash_mismatch" {
			t.Fatalf("result = %+v", got)
		}
	})
}

func TestAuditChainSigningKeysUsesDeterministicDegradedFallback(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "")
	t.Setenv("AIOPS_SECRET_KEYS_PREV", "previous:old-passphrase")
	t.Setenv("AIOPS_SECRET_KEY_ID", "")

	keys, degraded := auditChainSigningKeys()
	if !degraded {
		t.Fatal("missing master key must be marked degraded")
	}
	if len(keys) != 2 || keys[0].ID != "v1" || keys[1].ID != "previous" {
		t.Fatalf("keys = %+v", keys)
	}
	want := sha256.Sum256([]byte("aiops-audit-chain-default"))
	if !hmac.Equal(keys[0].Key, want[:]) {
		t.Fatal("fallback key differs from the historical deterministic fallback")
	}
}

func TestAuditChainSigningKeysUsesConfiguredKeyRing(t *testing.T) {
	t.Setenv("AIOPS_SECRET_KEY", "current-passphrase")
	t.Setenv("AIOPS_SECRET_KEY_ID", "current")
	t.Setenv("AIOPS_SECRET_KEYS_PREV", "previous:old-passphrase")

	keys, degraded := auditChainSigningKeys()
	if degraded {
		t.Fatal("configured key ring must not be degraded")
	}
	if len(keys) != 2 || keys[0].ID != "current" || keys[1].ID != "previous" {
		t.Fatalf("keys = %+v", keys)
	}
}
