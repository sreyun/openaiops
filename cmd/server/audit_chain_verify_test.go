package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func testAuditHash(key []byte, version int16, prev string, payload []byte, seq int64) string {
	mac := hmac.New(sha256.New, key)
	if version == 2 {
		mac.Write([]byte("aiops-audit-chain/v2\x00"))
	}
	mac.Write([]byte(prev))
	mac.Write([]byte{0})
	mac.Write(payload)
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(seq, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

func expectAuditVerifyRows(mock sqlmock.Sqlmock, limit int, entries []auditChainEntry, legacyHashes, partitionHashes int64, drift, conflict bool) {
	rows := sqlmock.NewRows([]string{"id", "ts", "data", "content_hash", "prev_hash", "chain_seq", "chain_version", "chain_key_id"})
	for _, entry := range entries {
		rows.AddRow(entry.ID, entry.TS, entry.Data, entry.ContentHash, entry.PrevHash, entry.Seq, entry.ChainVersion, entry.ChainKeyID)
	}
	mock.ExpectQuery(`(?s)FROM \(.*FROM audit_log_p.*ORDER BY chain_seq ASC`).
		WithArgs(limit + 1).
		WillReturnRows(rows)
	mock.ExpectQuery(`(?s)WITH legacy AS.*legacy_hashes`).
		WillReturnRows(sqlmock.NewRows([]string{"legacy_hashes", "partition_hashes", "drift", "conflict"}).
			AddRow(legacyHashes, partitionHashes, drift, conflict))
}

func configuredAuditKey(t *testing.T) []byte {
	t.Helper()
	t.Setenv("AIOPS_SECRET_KEY", "audit-test-passphrase")
	t.Setenv("AIOPS_SECRET_KEY_ID", "current")
	t.Setenv("AIOPS_SECRET_KEYS_PREV", "")
	sum := sha256.Sum256([]byte("audit-test-passphrase"))
	return sum[:]
}

func TestParseAuditVerifyLimit(t *testing.T) {
	tests := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "", want: 200},
		{raw: "1", want: 1},
		{raw: "5000", want: 5000},
		{raw: "0", wantErr: true},
		{raw: "-1", wantErr: true},
		{raw: "5001", wantErr: true},
		{raw: "abc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseAuditVerifyLimit(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("limit = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestVerifyAuditChainReturnsEmptyForNoMirroredHashes(t *testing.T) {
	p, mock := mockPgStore(t)
	expectAuditVerifyRows(mock, 200, nil, 0, 0, false, false)

	got, err := p.verifyAuditChain(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "empty" || got.OK || got.Code != "no_records" || got.Checked != 0 || !got.MirrorParity {
		t.Fatalf("result = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAuditChainChecksLatestWindowWithBoundary(t *testing.T) {
	key := configuredAuditKey(t)
	p, mock := mockPgStore(t)
	payload1 := []byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"one"}`)
	payload2 := []byte(`{"timestamp":2,"kind":"operation","level":"info","actor":"admin","message":"two"}`)
	payload3 := []byte(`{"timestamp":3,"kind":"operation","level":"info","actor":"admin","message":"three"}`)
	hash1 := testAuditHash(key, 2, "", payload1, 1)
	hash2 := testAuditHash(key, 2, hash1, payload2, 2)
	hash3 := testAuditHash(key, 2, hash2, payload3, 3)
	entries := []auditChainEntry{
		{ID: 1, TS: 1, Data: payload1, ContentHash: hash1, PrevHash: "", Seq: 1, ChainVersion: 2, ChainKeyID: "current"},
		{ID: 2, TS: 2, Data: payload2, ContentHash: hash2, PrevHash: hash1, Seq: 2, ChainVersion: 2, ChainKeyID: "current"},
		{ID: 3, TS: 3, Data: payload3, ContentHash: hash3, PrevHash: hash2, Seq: 3, ChainVersion: 2, ChainKeyID: "current"},
	}
	expectAuditVerifyRows(mock, 2, entries, 3, 3, false, false)

	got, err := p.verifyAuditChain(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "healthy" || !got.OK || got.Checked != 2 || got.FromSeq != 2 || got.ToSeq != 3 {
		t.Fatalf("result = %+v", got)
	}
	if len(got.ChainVersions) != 1 || got.ChainVersions[0] != 2 {
		t.Fatalf("versions = %v", got.ChainVersions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAuditChainClassifiesIntegrityOutcomes(t *testing.T) {
	key := configuredAuditKey(t)
	payload1 := []byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"one"}`)
	payload2 := []byte(`{"timestamp":2,"kind":"operation","level":"info","actor":"admin","message":"two"}`)
	hash1 := testAuditHash(key, 2, "", payload1, 1)

	tests := []struct {
		name        string
		entries     []auditChainEntry
		wantStatus  string
		wantCode    string
		wantBroken  int64
		wantChecked int
	}{
		{
			name: "sequence gap",
			entries: []auditChainEntry{
				{ID: 1, TS: 1, Data: payload1, ContentHash: hash1, Seq: 1, ChainVersion: 2, ChainKeyID: "current"},
				{ID: 3, TS: 3, Data: payload2, ContentHash: testAuditHash(key, 2, hash1, payload2, 3), PrevHash: hash1, Seq: 3, ChainVersion: 2, ChainKeyID: "current"},
			},
			wantStatus: "broken", wantCode: "sequence_gap", wantBroken: 3, wantChecked: 2,
		},
		{
			name: "predecessor mismatch",
			entries: []auditChainEntry{
				{ID: 1, TS: 1, Data: payload1, ContentHash: hash1, Seq: 1, ChainVersion: 2, ChainKeyID: "current"},
				{ID: 2, TS: 2, Data: payload2, ContentHash: testAuditHash(key, 2, "wrong", payload2, 2), PrevHash: "wrong", Seq: 2, ChainVersion: 2, ChainKeyID: "current"},
			},
			wantStatus: "broken", wantCode: "prev_hash_mismatch", wantBroken: 2, wantChecked: 2,
		},
		{
			name: "v2 content mismatch",
			entries: []auditChainEntry{
				{ID: 1, TS: 1, Data: payload1, ContentHash: "bad", Seq: 1, ChainVersion: 2, ChainKeyID: "current"},
			},
			wantStatus: "broken", wantCode: "content_hash_mismatch", wantBroken: 1, wantChecked: 1,
		},
		{
			name: "v2 key unavailable",
			entries: []auditChainEntry{
				{ID: 1, TS: 1, Data: payload1, ContentHash: "unknown", Seq: 1, ChainVersion: 2, ChainKeyID: "missing"},
			},
			wantStatus: "unverifiable", wantCode: "key_unavailable", wantBroken: 1, wantChecked: 1,
		},
		{
			name: "v1 mismatch is ambiguous",
			entries: []auditChainEntry{
				{ID: 1, TS: 1, Data: payload1, ContentHash: "bad", Seq: 1, ChainVersion: 1},
			},
			wantStatus: "unverifiable", wantCode: "legacy_key_or_content_mismatch", wantBroken: 1, wantChecked: 1,
		},
		{
			name: "invalid payload",
			entries: []auditChainEntry{
				{ID: 1, TS: 1, Data: []byte(`{"timestamp":1}`), ContentHash: "bad", Seq: 1, ChainVersion: 2, ChainKeyID: "current"},
			},
			wantStatus: "broken", wantCode: "invalid_payload", wantBroken: 1, wantChecked: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, mock := mockPgStore(t)
			n := int64(len(tt.entries))
			expectAuditVerifyRows(mock, 200, tt.entries, n, n, false, false)
			got, err := p.verifyAuditChain(context.Background(), 200)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.wantStatus || got.Code != tt.wantCode || got.BrokenAt != tt.wantBroken || got.Checked != tt.wantChecked || got.OK {
				t.Fatalf("result = %+v", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifyAuditChainMarksValidLegacyChainDegraded(t *testing.T) {
	key := configuredAuditKey(t)
	p, mock := mockPgStore(t)
	canonical := []byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"one"}`)
	jsonb := []byte(`{ "message": "one", "actor": "admin", "level": "info", "kind": "operation", "timestamp": 1 }`)
	entry := auditChainEntry{ID: 1, TS: 1, Data: jsonb, ContentHash: testAuditHash(key, 1, "", canonical, 1), Seq: 1, ChainVersion: 1}
	expectAuditVerifyRows(mock, 200, []auditChainEntry{entry}, 1, 1, false, false)

	got, err := p.verifyAuditChain(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "degraded" || !got.OK || got.Code != "legacy_chain" {
		t.Fatalf("result = %+v", got)
	}
}

func TestVerifyAuditChainPropagatesRowsError(t *testing.T) {
	p, mock := mockPgStore(t)
	rows := sqlmock.NewRows([]string{"id", "ts", "data", "content_hash", "prev_hash", "chain_seq", "chain_version", "chain_key_id"}).
		AddRow(1, 1, []byte(`{}`), "hash", "", 1, 1, "").
		RowError(0, errors.New("scan transport failed"))
	mock.ExpectQuery(`(?s)FROM \(.*FROM audit_log_p`).WithArgs(201).WillReturnRows(rows)

	if _, err := p.verifyAuditChain(context.Background(), 200); err == nil {
		t.Fatal("expected row iteration error")
	}
}

func TestHandleAuditVerifyChainRejectsInvalidLimit(t *testing.T) {
	// 限流闸门是包级共享的（见 audit_chain.go 里 auditChainGate 的说明），
	// 上一个用例缓存下来的成功结果会漏给这里，把断言变成掷骰子——先清干净。
	auditChainGate.reset()

	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/verify-chain?limit=0", nil)
	rec := httptest.NewRecorder()

	s.handleAuditVerifyChain(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var got auditChainVerifyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "invalid_limit" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleAuditVerifyChainSanitizesStorageFailure(t *testing.T) {
	// 限流闸门是包级共享的（见 audit_chain.go 里 auditChainGate 的说明），
	// 上一个用例缓存下来的成功结果会漏给这里，把断言变成掷骰子——先清干净。
	auditChainGate.reset()

	p, mock := mockPgStore(t)
	mock.ExpectQuery(`(?s)FROM \(.*FROM audit_log_p`).WithArgs(201).WillReturnError(errors.New("database exploded with private detail"))
	s := &Server{pg: p}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/verify-chain", nil)
	rec := httptest.NewRecorder()

	s.handleAuditVerifyChain(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "database exploded") {
		t.Fatalf("raw storage error leaked: %s", rec.Body.String())
	}
	var got auditChainVerifyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "unavailable" || got.Code != "storage_unavailable" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleAuditVerifyChainReturnsHTTP200ForBrokenChain(t *testing.T) {
	// 限流闸门是包级共享的（见 audit_chain.go 里 auditChainGate 的说明），
	// 上一个用例缓存下来的成功结果会漏给这里，把断言变成掷骰子——先清干净。
	auditChainGate.reset()

	configuredAuditKey(t)
	p, mock := mockPgStore(t)
	entry := auditChainEntry{
		ID: 1, TS: 1,
		Data:        []byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"one"}`),
		ContentHash: "bad", Seq: 1, ChainVersion: 2, ChainKeyID: "current",
	}
	expectAuditVerifyRows(mock, 200, []auditChainEntry{entry}, 1, 1, false, false)
	s := &Server{pg: p}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/verify-chain", nil)
	rec := httptest.NewRecorder()

	s.handleAuditVerifyChain(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got auditChainVerifyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "broken" || got.Code != "content_hash_mismatch" {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
