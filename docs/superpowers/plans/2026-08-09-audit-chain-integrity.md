# Audit Chain Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repair audit-chain verification and the related dual-track persistence defects while preserving every unique historical audit body and historical hash.

**Architecture:** Keep the legacy and partitioned PostgreSQL tables, reconcile them by stable logical identity, then make all future twin writes transactional and identity-stable. Replace the process-global audit tip with a PostgreSQL advisory-lock transaction, verify canonical versioned payloads, and expose a structured six-state API consumed by a localized Vue status card.

**Tech Stack:** Go 1.26, `database/sql`, PostgreSQL 18, `github.com/DATA-DOG/go-sqlmock`, Vue 3.5, TypeScript 5.9, Element Plus, TanStack Vue Query, Node 26, Docker Compose.

## Global Constraints

- Historical audit JSON and historical `content_hash` / `prev_hash` values must remain unchanged.
- Remove only exact redundant mirrors; abort on same-hash/different-content or same-sequence/different-hash conflicts.
- New audit writes must commit to both audit tables or neither table.
- New event and AI-call twin writes must share the legacy row ID and roll back on either failure.
- `audit_log_p` partitions must not be removed by generic retention.
- The verification API must support `healthy`, `degraded`, `broken`, `unverifiable`, `empty`, and `unavailable`.
- Raw backend diagnostics and secrets must not be rendered in the UI or written to logs.
- User-owned install/folder-ID work already present in the worktree must be preserved and excluded from audit-chain commits.
- No new third-party runtime dependency is required.
- The approved design is `docs/superpowers/specs/2026-08-09-audit-chain-integrity-design.md`.

---

## File Structure

- `cmd/server/audit_chain_format.go`: canonical payload, v1/v2 HMAC framing, entry-level verification types.
- `cmd/server/audit_chain_format_test.go`: pure canonicalization, key-selection, and tamper tests.
- `cmd/server/audit_chain.go`: PostgreSQL append, window verification, result model, and HTTP handler.
- `cmd/server/audit_chain_test.go`: sqlmock transaction/window tests and handler status tests.
- `cmd/server/pg_partition.go`: dual-track DDL, conflict-safe reconciliation, sequence alignment, and partition retention helpers.
- `cmd/server/pg_partition_test.go`: migration ordering, conflict, idempotence, and retention tests.
- `cmd/server/pgstore.go`: bootstrap migration split, stable recent reads, and event twin writer.
- `cmd/server/pgstore_tx_test.go`: event/AI/audit transaction rollback and stable-ID tests.
- `cmd/server/ai_usage.go`: AI-call stable-ID transactional write.
- `frontend/src/shared/audit-chain.ts`: typed API result and pure presentation mapping.
- `frontend/scripts/check-audit-chain.mjs`: Node assertions for all six UI states.
- `frontend/src/api/modules.ts`: typed verification endpoint.
- `frontend/src/views/SecurityView.vue`: separate audit-chain status and export-configuration cards.
- `frontend/src/i18n/locales/{zh-CN,zh-TW,en}.ts`: localized status and reason copy.
- `frontend/package.json`: include the audit-chain frontend check in the release gate.

---

### Task 1: Restore the Existing Test Baseline Without Absorbing User-Owned Work

**Files:**
- Modify: `cmd/server/agent_handshake_test.go:172-175`
- Modify: `cmd/server/install_audit_test.go:68,100`
- Modify: `cmd/server/logcrypt_test.go:90`

**Interfaces:**
- Consumes: current user-owned signatures `renderScript(tmpl, server, token, category, folderID, serversJSON, logPaths string)`, `renderScriptWithAudit(..., folderID, serversJSON, logPaths string, audit installAuditOptions)`, and `buildInstallConfigYAML(..., folderID, serversJSON, logPaths string, audit installAuditOptions, windows bool)`.
- Produces: a compiling baseline so audit-chain tests can run; no production behavior change.

- [ ] **Step 1: Re-run the failing compile check**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestAuditChainHMAC|TestWithPgTx' -count=1
```

Expected: build failure listing the seven calls that omit `folderID`.

- [ ] **Step 2: Add only the missing empty `folderID` arguments**

Use these exact call shapes:

```go
shIn := renderScript(installShTemplate, server, token, "prod", "", "", "")
ps1In := renderScript(installPs1Template, server, token, "prod", "", "", "")
shUn := renderScript(uninstallShTemplate, server, token, "prod", "", "", "")
ps1Un := renderScript(uninstallPs1Template, server, token, "prod", "", "", "")

out := renderScriptWithAudit(tmpl, "https://monitor.example", "tok", "prod", "", "", "[]", opts)
cfg := buildInstallConfigYAML("http://s:8529", "tok", "prod", "", "", "[]", installAuditOptions{}, false)

sh2 := renderScript(installShTemplate, "http://s:8529", "tok", "prod", "", "", "")
```

- [ ] **Step 3: Verify the package compiles and the existing focused tests pass**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestAuditChainHMAC|TestWithPgTx|TestInstallScript' -count=1
```

Expected: PASS. Any new failure must be recorded as baseline evidence before audit-chain production code changes.

- [ ] **Step 4: Preserve the ownership boundary**

Run:

```bash
git diff -- cmd/server/agent_handshake_test.go cmd/server/install_audit_test.go cmd/server/logcrypt_test.go
```

Expected: only missing-argument alignment in addition to the user's existing edits. Do not stage or commit these overlapping files in audit-chain commits.

---

### Task 2: Introduce Deterministic Versioned Audit Payloads

**Files:**
- Create: `cmd/server/audit_chain_format.go`
- Create: `cmd/server/audit_chain_format_test.go`
- Modify: `cmd/server/gap_closure_test.go:81-95`

**Interfaces:**
- Consumes: `LogEntry`, `secretKeyEntry`, `loadAllSecretKeys()`, and `currentSecretKeyID()`.
- Produces: `canonicalAuditPayload(raw []byte) ([]byte, error)`, `auditChainSigningKeys() ([]secretKeyEntry, bool)`, `computeAuditChainHash(key []byte, version int16, prevHash string, payload []byte, seq int64) string`, and `verifyAuditEntryHash(entry auditChainEntry, keys []secretKeyEntry) auditHashResult`.

- [ ] **Step 1: Write failing canonicalization and hash tests**

Create table-driven tests with hand-derived inputs:

```go
func TestCanonicalAuditPayloadNormalizesJSONBFormatting(t *testing.T) {
    compact := []byte(`{"timestamp":1786284000,"kind":"operation","level":"info","actor":"admin","message":"saved"}`)
    jsonb := []byte(`{ "kind": "operation", "actor": "admin", "level": "info", "message": "saved", "timestamp": 1786284000 }`)
    gotCompact, err := canonicalAuditPayload(compact)
    if err != nil { t.Fatal(err) }
    gotJSONB, err := canonicalAuditPayload(jsonb)
    if err != nil { t.Fatal(err) }
    if string(gotCompact) != string(gotJSONB) {
        t.Fatalf("canonical payload differs: %s != %s", gotCompact, gotJSONB)
    }
}

func TestAuditChainV1MatchesHistoricalFraming(t *testing.T) {
    key := []byte("01234567890123456789012345678901")
    payload := []byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"x"}`)
    got := computeAuditChainHash(key, 1, "prev", payload, 7)
    mac := hmac.New(sha256.New, key)
    mac.Write([]byte("prev\x00"))
    mac.Write(payload)
    mac.Write([]byte("\x007"))
    want := hex.EncodeToString(mac.Sum(nil))
    if got != want { t.Fatalf("got %s want %s", got, want) }
}

func TestAuditChainV2UsesDomainSeparation(t *testing.T) {
    key := []byte("01234567890123456789012345678901")
    payload := []byte(`{"timestamp":1,"kind":"operation","level":"info","actor":"admin","message":"x"}`)
    if computeAuditChainHash(key, 1, "", payload, 1) == computeAuditChainHash(key, 2, "", payload, 1) {
        t.Fatal("v1 and v2 hashes must differ")
    }
}
```

Add cases for malformed JSON, timestamp `<= 0`, missing v2 key, v2 content mutation, v1 current key, v1 previous key, and no configured master key. The final case must assert that `auditChainSigningKeys` returns the existing deterministic fallback under `currentSecretKeyID()` together with `degraded=true`, so signing and verification cannot select different keys.

- [ ] **Step 2: Run the new tests and observe RED**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestCanonicalAudit|TestAuditChainV1|TestAuditChainV2' -count=1
```

Expected: FAIL because the new symbols do not exist.

- [ ] **Step 3: Implement the minimal format module**

Use these exact production types and rules:

```go
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
    if err != nil { return nil, fmt.Errorf("encode audit payload: %w", err) }
    return out, nil
}
```

`auditChainSigningKeys` must return `loadAllSecretKeys()` when configured. When it is empty, return one `secretKeyEntry` containing the current key ID and the existing deterministic SHA-256 fallback, plus `degraded=true`. `computeAuditChainHash` must write the v2 prefix `aiops-audit-chain/v2` plus NUL before the existing frame. `verifyAuditEntryHash` must try every available key for v1, select the exact `ChainKeyID` for v2, return `legacy_key_or_content_mismatch` for unmatched v1, `key_unavailable` for missing v2 key, and `content_hash_mismatch` for a present v2 key that does not match.

- [ ] **Step 4: Replace the old global-state-only test**

Remove `TestAuditChainHMAC` from `gap_closure_test.go`; its assertions are superseded by format tests that do not mutate package-global state.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestCanonicalAudit|TestAuditChainV1|TestAuditChainV2' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit the isolated format layer**

```bash
git add cmd/server/audit_chain_format.go cmd/server/audit_chain_format_test.go cmd/server/gap_closure_test.go
git commit -m "fix(audit): canonicalize versioned chain payloads"
```

---

### Task 3: Correct Migration Order and Reconcile Audit Mirrors Safely

**Files:**
- Modify: `cmd/server/pgstore.go:105-139,217-690`
- Modify: `cmd/server/pg_partition.go:81-188`
- Create: `cmd/server/pg_partition_test.go`

**Interfaces:**
- Consumes: `migrateBootstrap() error`, `migrateDualTrackPartitions() error`, and `runVersionedMigrations() error`.
- Produces: `runMigrationPhases(bootstrap, dualTrack, versioned func() error) error`, `reconcileAuditMirrors(ctx context.Context, tx *sql.Tx) (auditMirrorStats, error)`, and idempotent twin-table backfill.

- [ ] **Step 1: Write failing migration-order and conflict tests**

Test the phase runner without mocking source text:

```go
func TestRunMigrationPhasesOrdersDependencies(t *testing.T) {
    var got []string
    phase := func(name string) func() error {
        return func() error { got = append(got, name); return nil }
    }
    err := runMigrationPhases(phase("bootstrap"), phase("dual"), phase("versioned"))
    if err != nil { t.Fatal(err) }
    want := []string{"bootstrap", "dual", "versioned"}
    if !reflect.DeepEqual(want, got) {
        t.Fatalf("phase order: got %v want %v", got, want)
    }
}
```

Import `reflect`; do not add `go-cmp`. Add sqlmock tests where the conflict probe returns a hash for same-hash/different-content and a sequence for same-sequence/different-hash; both must return a typed `errAuditMirrorConflict` before any DELETE or INSERT expectation.

- [ ] **Step 2: Run migration tests and observe RED**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestRunMigrationPhases|TestReconcileAuditMirrors' -count=1
```

Expected: FAIL because the phase runner and reconciliation function do not exist.

- [ ] **Step 3: Split bootstrap from versioned migrations**

Rename the current `migrate()` body to `migrateBootstrap()` and replace its final `return p.runVersionedMigrations()` with `return nil`. Add:

```go
func runMigrationPhases(bootstrap, dualTrack, versioned func() error) error {
    for _, phase := range []func() error{bootstrap, dualTrack, versioned} {
        if err := phase(); err != nil { return err }
    }
    return nil
}
```

In `openPGStore`, call:

```go
if err := runMigrationPhases(ps.migrateBootstrap, ps.migrateDualTrackPartitions, ps.runVersionedMigrations); err != nil {
    db.Close()
    return nil, err
}
```

This replaces the current partition-before-bootstrap order.

- [ ] **Step 4: Make dual-track DDL fail closed**

Change `migrateDualTrackPartitions` to return `error`. Add `chain_version SMALLINT NOT NULL DEFAULT 1` and `chain_key_id TEXT NOT NULL DEFAULT ''` to both audit tables with `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`. Any required audit DDL, conflict probe, reconciliation, or unique-index failure returns an error and prevents server startup; optional future partition creation may retain debug logging only when it cannot affect current writes.

Create these guards after reconciliation:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS audit_log_content_hash_uq
ON audit_log(content_hash) WHERE content_hash <> '';

CREATE UNIQUE INDEX IF NOT EXISTS audit_log_p_content_hash_ts_uq
ON audit_log_p(content_hash, ts) WHERE content_hash <> '';
```

- [ ] **Step 5: Implement exact-conflict probes and reconciliation**

Use a transaction and union both tables for these probes:

```sql
SELECT content_hash
FROM (
  SELECT content_hash, ts, data, prev_hash, chain_seq, chain_version, chain_key_id FROM audit_log
  UNION ALL
  SELECT content_hash, ts, data, prev_hash, chain_seq, chain_version, chain_key_id FROM audit_log_p
) u
WHERE content_hash <> ''
GROUP BY content_hash
HAVING COUNT(DISTINCT ROW(ts, data, prev_hash, chain_seq, chain_version, chain_key_id)) > 1
LIMIT 1;
```

```sql
SELECT chain_seq
FROM (
  SELECT chain_seq, content_hash FROM audit_log WHERE content_hash <> ''
  UNION ALL
  SELECT chain_seq, content_hash FROM audit_log_p WHERE content_hash <> ''
) u
GROUP BY chain_seq
HAVING COUNT(DISTINCT content_hash) > 1
LIMIT 1;
```

After both probes return no rows, delete only identical partition mirrors by keeping the smallest `(tableoid, ctid)` per `content_hash`, then backfill missing hashes in both directions with `NOT EXISTS (... content_hash=...)`. Do not copy historical IDs when occupied; insert historical backfills without `id` and retain `chain_seq` as their order.

Advance all twin-table sequences with `setval(pg_get_serial_sequence(...), max(id), true)` after backfill. Return `auditMirrorStats{LegacyRows, PartitionRows, UniqueHashes, RemovedDuplicates, BackfilledLegacy, BackfilledPartition}` and log counts only.

- [ ] **Step 6: Remove the misleading self-REVOKE**

Delete `tryRevokeAuditMutations`. PostgreSQL table owners retain implicit rights, while a non-owner runtime role may be unable to run reconciliation after the revoke. Integrity is enforced by the HMAC chain, transaction rules, restrictive deployment credentials, and conflict detection; do not claim the existing REVOKE makes owner writes impossible.

- [ ] **Step 7: Stop audit partition retention**

Change `cleanupAuditAndEvents` so it calls `cleanupOldTSPartitions("events_p", months)` but never `cleanupOldTSPartitions("audit_log_p", ...)`. Extract `retainedPartitionParents() []string` returning only `[]string{"events_p"}`; test that exact slice with `reflect.DeepEqual`, and make cleanup iterate it. This prevents an audit parent from silently re-entering retention later.

- [ ] **Step 8: Verify migration tests GREEN**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestRunMigrationPhases|TestReconcileAuditMirrors|TestCleanupAudit' -count=1
```

Expected: PASS with all sqlmock expectations met.

- [ ] **Step 9: Commit the migration and reconciliation layer**

```bash
git add cmd/server/pgstore.go cmd/server/pg_partition.go cmd/server/pg_partition_test.go
git commit -m "fix(storage): reconcile dual-track audit history"
```

---

### Task 4: Make Audit Append Atomic and Multi-Process Safe

**Files:**
- Modify: `cmd/server/audit_chain.go:16-78,181-200`
- Modify: `cmd/server/pgstore.go:905-909`
- Modify: `cmd/server/store.go:763-788`
- Create or extend: `cmd/server/audit_chain_test.go`

**Interfaces:**
- Consumes: `computeAuditChainHash`, `canonicalAuditPayload`, `currentSecretKeyID`, `loadSecretKey`, and `withPgTx`.
- Produces: `appendAuditChained(ctx context.Context, entry LogEntry) (seq int64, err error)`; removes `auditChainPrev`, `auditChainSeq`, `nextAuditChain`, and `hydrateAuditChainTip`.

- [ ] **Step 1: Write failing transaction tests**

Use sqlmock to require this order:

```go
mock.ExpectBegin()
mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
mock.ExpectQuery(`SELECT content_hash, chain_seq FROM audit_log_p`).
    WillReturnRows(sqlmock.NewRows([]string{"content_hash", "chain_seq"}).AddRow("prev", int64(9)))
mock.ExpectQuery(`INSERT INTO audit_log.*RETURNING id`).
    WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(42)))
mock.ExpectExec(`INSERT INTO audit_log_p`).
    WithArgs(int64(42), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "prev", int64(10), int16(2), currentSecretKeyID()).
    WillReturnResult(sqlmock.NewResult(0, 1))
mock.ExpectCommit()
```

Add a second test where the partition insert fails and `ExpectRollback()` is required. Add an empty-tip case that accepts `sql.ErrNoRows`, writes sequence 1 with an empty predecessor, and commits.

- [ ] **Step 2: Run append tests and observe RED**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestAppendAuditChained' -count=1
```

Expected: FAIL because append is non-transactional and has the old signature.

- [ ] **Step 3: Implement database-backed append**

Use a fixed signed 64-bit advisory-lock key declared as `const auditChainAdvisoryLock int64 = 0x41494f5053415544`. Inside `withPgTx`:

```go
if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, auditChainAdvisoryLock); err != nil {
    return fmt.Errorf("lock audit chain: %w", err)
}

var prev string
var seq int64
err := tx.QueryRowContext(ctx, `
SELECT content_hash, chain_seq FROM audit_log_p
WHERE content_hash <> '' ORDER BY chain_seq DESC, id DESC LIMIT 1`).Scan(&prev, &seq)
if err != nil && !errors.Is(err, sql.ErrNoRows) { return err }

if entry.Timestamp <= 0 { entry.Timestamp = time.Now().Unix() }
payload, err := json.Marshal(entry)
if err != nil { return err }
payload, err = canonicalAuditPayload(payload)
if err != nil { return err }
nextSeq := seq + 1
keys, _ := auditChainSigningKeys()
keyID := currentSecretKeyID()
hash := computeAuditChainHash(keys[0].Key, auditChainVersionV2, prev, payload, nextSeq)
```

Insert legacy with `RETURNING id`, then insert partition with explicit `(id, ts, data, content_hash, prev_hash, chain_seq, chain_version, chain_key_id)`. Return every error so `withPgTx` rolls back. Do not update in-memory chain state.

- [ ] **Step 4: Propagate and sanitize write failures**

Keep `Store.appendLog` asynchronous to avoid request latency, but call a wrapper that receives the returned sequence and logs only `seq`, operation, degraded-key mode, and error class. Never log `entry.Message`, serialized data, or key material. Remove `hydrateAuditChainTip()` from startup.

- [ ] **Step 5: Verify append tests GREEN and run race-focused tests**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestAppendAuditChained' -count=1
env GOCACHE=/tmp/aiops-go-cache go test -race ./cmd/server -run 'TestAppendAuditChained|TestCanonicalAudit' -count=1
```

Expected: PASS with no race report.

Add `TestAppendAuditChainedConcurrentPostgres`, gated by `AIOPS_TEST_PG_DSN`, which creates a schema-isolated pair of temporary audit tables, runs eight appenders concurrently through the same advisory-lock algorithm, and asserts committed sequences are exactly 1–8 with matching IDs in both tables. Run it against the local PostgreSQL container during Task 8; ordinary unit runs skip it when the DSN is absent.

- [ ] **Step 6: Commit atomic append**

```bash
git add cmd/server/audit_chain.go cmd/server/audit_chain_test.go cmd/server/pgstore.go cmd/server/store.go
git commit -m "fix(audit): append chain links atomically"
```

---

### Task 5: Build the Structured Window Verifier and HTTP Contract

**Files:**
- Modify: `cmd/server/audit_chain.go:80-140`
- Extend: `cmd/server/audit_chain_test.go`

**Interfaces:**
- Consumes: `auditChainEntry`, `canonicalAuditPayload`, `verifyAuditEntryHash`, and reconciled `audit_log_p`.
- Produces: `auditChainVerifyResult`, `verifyAuditChain(ctx context.Context, limit int) (auditChainVerifyResult, error)`, and `parseAuditVerifyLimit(raw string) (int, error)`.

- [ ] **Step 1: Write failing verifier result tests**

Define expected result fields in tests:

```go
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
```

Add independent fixtures for:

- empty rows → `status=empty`, `ok=false`;
- one valid root → `healthy` or `degraded` according to key mode;
- latest 200 plus one boundary → `checked=200` and `from_seq` excludes the boundary;
- non-consecutive sequence → `broken/sequence_gap`;
- wrong predecessor → `broken/prev_hash_mismatch`;
- v2 present-key mismatch → `broken/content_hash_mismatch`;
- v2 missing key → `unverifiable/key_unavailable`;
- v1 no matching available key → `unverifiable/legacy_key_or_content_mismatch`;
- row scan error or `rows.Err()` → returned error, later mapped to `unavailable`.

- [ ] **Step 2: Run verifier tests and observe RED**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestVerifyAuditChain|TestParseAuditVerifyLimit|TestHandleAuditVerifyChain' -count=1
```

Expected: FAIL because the structured verifier does not exist.

- [ ] **Step 3: Implement latest-window selection with a boundary row**

Use this query shape with `limit+1`:

```sql
SELECT id, ts, data, content_hash, prev_hash, chain_seq, chain_version, chain_key_id
FROM (
  SELECT id, ts, data, content_hash, prev_hash, chain_seq, chain_version, chain_key_id
  FROM audit_log_p
  WHERE content_hash <> ''
  ORDER BY chain_seq DESC, id DESC
  LIMIT $1
) latest
ORDER BY chain_seq ASC, id ASC;
```

Verify every returned entry. When `len(entries) == limit+1`, use the first only as the predecessor boundary and report the remaining `limit` entries. When all history fits, require root sequence 1 and empty `prev_hash`.

Collect distinct versions in ascending order. Query mirror unique-hash counts and conflicts separately; parity mismatch without conflict is `degraded/mirror_drift`.

- [ ] **Step 4: Implement limit and HTTP semantics**

`parseAuditVerifyLimit("")` returns 200. Values below 1, above 5000, or non-numeric return an error. The handler returns:

- HTTP 400 with `code=invalid_limit` for invalid input;
- HTTP 503 with `status=unavailable` and `code=storage_unavailable` for database/query/scan failure;
- HTTP 200 for completed `healthy`, `degraded`, `broken`, `unverifiable`, or `empty` checks.

Do not include raw SQL or `err.Error()` in the JSON response. Log the internal error server-side.

- [ ] **Step 5: Verify verifier and handler GREEN**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestVerifyAuditChain|TestParseAuditVerifyLimit|TestHandleAuditVerifyChain' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit the verifier contract**

```bash
git add cmd/server/audit_chain.go cmd/server/audit_chain_test.go
git commit -m "fix(audit): return structured chain verification"
```

---

### Task 6: Repair Event and AI-Call Twin Writes

**Files:**
- Modify: `cmd/server/pgstore.go:981-1020`
- Modify: `cmd/server/ai_usage.go:14-54`
- Modify: `cmd/server/pg_partition.go:143-174`
- Extend: `cmd/server/pgstore_tx_test.go`

**Interfaces:**
- Consumes: `withPgTx(ctx, fn)`, legacy sequence IDs, and partition tables.
- Produces: `appendEventDual(ctx context.Context, event storedEvent) error` and `appendAICallEventDual(ctx context.Context, stat aiCallStat) error`.

- [ ] **Step 1: Write failing stable-ID and rollback tests**

For each data type, require `BEGIN`, a legacy `INSERT ... RETURNING id`, a partition insert receiving that ID, and `COMMIT`. Add a partition failure case requiring `ROLLBACK`.

Representative event expectation:

```go
mock.ExpectBegin()
mock.ExpectQuery(`INSERT INTO events.*RETURNING id`).
    WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)))
mock.ExpectExec(`INSERT INTO events_p\(id,ts,data\)`).
    WithArgs(int64(17), int64(1786284000), sqlmock.AnyArg()).
    WillReturnResult(sqlmock.NewResult(0, 1))
mock.ExpectCommit()
```

AI-call expectations must include the returned ID as the first partition insert argument and require rollback when that insert fails.

- [ ] **Step 2: Run twin-write tests and observe RED**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestAppendEventDual|TestAppendAICallEventDual' -count=1
```

Expected: FAIL because current event writes are independent and AI errors are swallowed.

- [ ] **Step 3: Implement transactional stable-ID writes**

Both dual functions must:

1. normalize timestamp before marshaling;
2. use `QueryRowContext(... RETURNING id)` for the legacy insert;
3. insert the partition row with the returned ID;
4. return the second insert error unchanged so `withPgTx` rolls back.

Keep the existing public void wrappers as best-effort observability entry points, but have them call the error-returning dual functions and log sanitized failures.

- [ ] **Step 4: Make historical backfill multiplicity-aware**

For `events` and `ai_call_events`, rank identical value tuples with `row_number() OVER (PARTITION BY <all persisted value columns> ORDER BY id) AS copy_no` on each side. Backfill only source ranks absent from the partition ranks. This preserves two genuinely identical events as two rows while preventing restart backfill from creating a third mirror. After backfill, align sequences to maximum IDs.

- [ ] **Step 5: Stabilize recent reads**

Change recent audit and event queries from ID-only order to `ORDER BY ts DESC, id DESC LIMIT $1`, then return the selected rows in `ORDER BY ts ASC, id ASC`. Audit reads select the reconciled partition mirror where each non-empty hash is unique.

- [ ] **Step 6: Verify GREEN**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -run 'TestAppendEventDual|TestAppendAICallEventDual|TestLoadRecent' -count=1
```

Expected: PASS with no unmet sqlmock expectations.

- [ ] **Step 7: Commit dual-track write repairs**

```bash
git add cmd/server/pgstore.go cmd/server/ai_usage.go cmd/server/pg_partition.go cmd/server/pgstore_tx_test.go
git commit -m "fix(storage): make twin writes identity-stable"
```

---

### Task 7: Build the Localized Audit-Chain Status Card

**Files:**
- Create: `frontend/src/shared/audit-chain.ts`
- Create: `frontend/scripts/check-audit-chain.mjs`
- Modify: `frontend/src/api/modules.ts:770-774`
- Modify: `frontend/src/views/SecurityView.vue:149-159,1907-1956`
- Modify: `frontend/src/i18n/locales/zh-CN.ts:1607-1609`
- Modify: `frontend/src/i18n/locales/zh-TW.ts:1586-1588`
- Modify: `frontend/src/i18n/locales/en.ts:1741-1743`
- Modify: `frontend/package.json`

**Interfaces:**
- Consumes: structured `GET /audit/verify-chain` response.
- Produces: `AuditChainVerifyResponse`, `AuditChainStatus`, and `auditChainPresentation(result, transportError)` returning `{ type, titleKey, descriptionKey, params }`.

- [ ] **Step 1: Write the failing Node presentation check**

Create `check-audit-chain.mjs` with literal expectations:

```js
import assert from "node:assert/strict";
import { auditChainPresentation } from "../src/shared/audit-chain.ts";

assert.deepEqual(
  auditChainPresentation({ status: "healthy", checked: 200, from_seq: 30, to_seq: 229 }),
  { type: "success", titleKey: "security.auditChainHealthy", descriptionKey: "security.auditChainRange", params: { n: 200, from: 30, to: 229 } },
);
assert.equal(auditChainPresentation({ status: "broken", code: "sequence_gap" }).type, "error");
assert.equal(auditChainPresentation({ status: "unverifiable", code: "key_unavailable" }).type, "warning");
assert.equal(auditChainPresentation({ status: "empty" }).type, "info");
assert.equal(auditChainPresentation(undefined, new Error("offline")).titleKey, "security.auditChainUnavailable");
```

Add assertions for `degraded` and API-provided `unavailable` so all six states are covered.

- [ ] **Step 2: Add the script to the release gate and observe RED**

Add:

```json
"check:audit-chain": "node --experimental-strip-types ./scripts/check-audit-chain.mjs"
```

Insert `npm run check:audit-chain` before `check:i18n` in `npm run check`, then run from the repository root:

```bash
npm --prefix frontend run check:audit-chain
```

Expected: FAIL because `src/shared/audit-chain.ts` does not exist.

- [ ] **Step 3: Implement the typed pure mapper**

Define:

```ts
export type AuditChainStatus = "healthy" | "degraded" | "broken" | "unverifiable" | "empty" | "unavailable";

export interface AuditChainVerifyResponse {
  ok?: boolean;
  status: AuditChainStatus;
  code?: string;
  checked?: number;
  from_seq?: number;
  to_seq?: number;
  broken_at?: number;
  chain_versions?: number[];
  mirror_parity?: boolean;
  secret_degraded?: boolean;
  verified_at?: number;
}
```

Map `healthy→success`, `degraded/unverifiable→warning`, `broken→error`, and `empty/unavailable→info/error` respectively. Return translation keys and numeric params only; never return raw `detail` or transport error text.

- [ ] **Step 4: Type the API module**

Import `AuditChainVerifyResponse` and replace the anonymous response type:

```ts
verifyAuditChain: (limit = 200) => api<AuditChainVerifyResponse>(`/audit/verify-chain?limit=${limit}`),
```

- [ ] **Step 5: Separate status and configuration UI state**

In `SecurityView.vue`, compute the presentation from `auditChainQ.data.value` and `auditChainQ.error.value`. Render a first `el-card` for chain state containing localized title, description, checked range, versions, verified time, and its own refresh button. Render the export form in a second `el-card` wrapped only by `auditExportQ` state. Remove the raw expression:

```vue
:description="auditChainQ.data.value.detail || ..."
```

The chain query failure must not hide or disable the export form.

- [ ] **Step 6: Add exact three-locale copy**

Add keys for status titles, range, versions, verification time, and reason codes. Required Chinese concepts are “审计链完整”, “兼容模式校验通过”, “审计链确认损坏”, “历史密钥不可用，无法完成校验”, “暂无审计链记录”, and “审计链校验服务暂不可用”. Provide equivalent zh-TW and English strings and keep placeholder names `{n}`, `{from}`, `{to}`, `{versions}`, and `{time}` identical across locales.

- [ ] **Step 7: Verify frontend GREEN**

Run:

```bash
npm --prefix frontend run check:audit-chain
npm --prefix frontend run typecheck
npm --prefix frontend run check:i18n
npm --prefix frontend run check:api
npm --prefix frontend run build
```

Expected: every command exits 0 and no raw backend `detail` is rendered by the Vue template.

- [ ] **Step 8: Commit the frontend status surface**

```bash
git add frontend/src/shared/audit-chain.ts frontend/scripts/check-audit-chain.mjs frontend/src/api/modules.ts frontend/src/views/SecurityView.vue frontend/src/i18n/locales/zh-CN.ts frontend/src/i18n/locales/zh-TW.ts frontend/src/i18n/locales/en.ts frontend/package.json
git commit -m "fix(frontend): present structured audit chain status"
```

---

### Task 8: Back Up, Migrate, and Verify the Real Docker Deployment

**Files:**
- Modify only if verification exposes a defect: files already listed in Tasks 2–7.
- Do not commit: database dump under `/tmp`.

**Interfaces:**
- Consumes: completed server/frontend implementation and running Compose services.
- Produces: a backed-up, reconciled local database and browser-verified audit status.

- [ ] **Step 1: Run pre-deployment full static verification**

Run:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -count=1
env GOCACHE=/tmp/aiops-go-cache go test ./... -count=1
npm --prefix frontend run check
```

Expected: zero Go test failures and frontend check/build exit 0. If unrelated user-owned changes still block compilation, report the exact files and do not weaken audit tests.

- [ ] **Step 2: Capture pre-migration conflict and count metadata**

Run read-only queries for each twin table, recording legacy rows, partition rows, unique hashes/value ranks, repeated exact mirrors, same-hash conflicts, and same-sequence conflicts. Expected current audit baseline is 211 legacy rows, 391 partition rows, sequences 1–229, and no sequence gaps; counts may increase from new login audit records.

- [ ] **Step 3: Create a timestamped PostgreSQL custom-format backup**

Use an explicit safe path such as `/tmp/aiops-audit-before-v2-20260809.dump`:

```bash
docker compose exec -T postgres pg_dump -U aiops -d aiops -Fc -f /tmp/aiops-audit-before-v2-20260809.dump
docker cp aiops-pg:/tmp/aiops-audit-before-v2-20260809.dump /tmp/aiops-audit-before-v2-20260809.dump
```

Verify the local file exists and is non-empty with `ls -lh`. Do not print or inspect secret values.

- [ ] **Step 4: Rebuild and start only the affected services**

Run:

```bash
docker compose build aiops-server aiops-frontend
docker compose up -d aiops-server aiops-frontend
docker compose ps
```

Expected: server and frontend become healthy. If migration detects an ambiguous conflict, startup must fail closed and the database must remain unchanged.

- [ ] **Step 5: Verify database reconciliation**

Run read-only SQL to assert:

- zero same-hash/different-content conflicts;
- zero same-sequence/different-hash conflicts;
- one physical partition row per unique non-empty audit hash;
- equal unique audit-hash sets in legacy and partition tables;
- consecutive unique sequence values from 1 through the current tip;
- current event and AI-call twin counts/multiplicity remain consistent.

Expected for the original 229-link baseline: partition exact mirrors fall from 391 to 229 and legacy unique hashes rise from 211 to 229, adjusted equally for any links appended during implementation.

- [ ] **Step 6: Append and verify one harmless version 2 link**

Log in once through the local frontend or invoke another read-only UI action that already creates an audit login entry. Query only metadata for the new tip and assert both tables share `id`, `ts`, `content_hash`, `prev_hash`, `chain_seq`, `chain_version=2`, and non-empty `chain_key_id`.

Run the gated concurrency test with a uniquely named temporary schema against the local PostgreSQL service and confirm eight simultaneous appenders produce exactly eight consecutive committed links with twin-table ID parity. The test must drop only that temporary schema and must not read, mutate, or log production audit bodies.

- [ ] **Step 7: Verify the API and UI in the real browser**

Open `/v2/#/security?tab=auditExport` with the supplied local admin account. Confirm:

- no raw `content_hash mismatch at id=1` text;
- localized healthy or explicitly degraded status;
- checked range/count/version/time are visible;
- chain retry and export configuration work independently;
- browser console contains no error or warning caused by the page.

- [ ] **Step 8: Run final release verification after migration**

Run fresh commands:

```bash
env GOCACHE=/tmp/aiops-go-cache go test ./cmd/server -count=1
env GOCACHE=/tmp/aiops-go-cache go test ./... -count=1
npm --prefix frontend run check
docker compose ps
```

Read all output and confirm zero failures before claiming completion.

- [ ] **Step 9: Review repository scope and create the final implementation commit if needed**

Run:

```bash
git status --short
git diff --check
git log -8 --oneline
```

Expected: audit-chain implementation files are committed, user-owned install/folder-ID files remain preserved, no database dump is under the repository, and no unreviewed generated file is staged.

---

## Rollback Procedure

If migration or real verification fails:

1. Stop `aiops-server` to prevent new writes.
2. Preserve failure logs and post-failure count metadata without audit bodies.
3. Restore `/tmp/aiops-audit-before-v2-20260809.dump` into the local `aiops` database using `pg_restore` after explicit destructive-action confirmation.
4. Start the prior server/frontend images.
5. Re-run the pre-migration count and conflict queries.

Never recompute historical hashes, delete an ambiguous unique record, or report a green chain merely to complete rollout.
