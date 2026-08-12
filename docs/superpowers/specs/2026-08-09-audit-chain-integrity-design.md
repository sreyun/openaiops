# Audit Chain Integrity and Dual-Track Persistence Design

**Date:** 2026-08-09

**Status:** Approved for implementation

## Objective

Restore trustworthy audit-chain verification without rewriting historical audit
content or replacing historical hashes. Fix the related migration, dual-write,
partition, key-rotation, API, and frontend defects that can create false alarms,
duplicate data, partial writes, or false healthy results.

## Success Criteria

- Historical audit JSON and historical `content_hash` / `prev_hash` values remain
  unchanged.
- Existing v1 entries that were written correctly verify after PostgreSQL JSONB
  normalization.
- New entries use an explicitly versioned, deterministic payload and record the
  signing key ID.
- Audit writes are serialized across processes and atomically committed to the
  legacy and partitioned tables.
- Exact partition-mirror duplicates are removed without discarding a unique
  audit entry.
- The legacy and partitioned audit tables contain the same unique logical chain
  after reconciliation.
- Audit retention never silently removes the chain root.
- Event and AI-call dual-track writes use one stable identity and cannot commit
  only one side.
- The API distinguishes healthy, degraded, broken, unverifiable, empty, and
  unavailable states.
- The Vue UI presents localized, actionable states and never exposes raw
  backend diagnostics such as `content_hash mismatch at id=1` as user copy.
- Full Go and frontend release checks pass, and the Docker deployment verifies
  the repaired chain against the real database.

## Evidence and Root Causes

The defect was reproduced in the running local deployment at
`/v2/#/security?tab=auditExport`. The API-backed alert displayed
`content_hash mismatch at id=1`.

Runtime metadata showed:

- `audit_log`: 211 chained rows, covering sequence 19 through 229.
- `audit_log_p`: 391 chained rows, covering sequence 1 through 229.
- The partition table contained repeated copies of the same logical hashes.
- PostgreSQL returned reformatted JSONB text, whereas the hash had been created
  from Go's compact struct JSON before insertion.

The implementation has the following related root causes:

1. **Unstable signed bytes.** Append signs compact `json.Marshal(LogEntry)`
   bytes; verification signs PostgreSQL's normalized JSONB representation.
2. **Incorrect fresh-database migration order.** Partition migration runs before
   the legacy bootstrap. On a fresh database, adding chain columns and backfill
   fail before `audit_log` exists; early entries therefore reach only
   `audit_log_p`.
3. **Non-atomic audit writes.** The global in-memory tip advances before either
   table insert. The two inserts are independent, and their errors do not roll
   back the tip or the other insert.
4. **Process-local serialization.** A Go mutex cannot serialize multiple server
   processes sharing PostgreSQL.
5. **Duplicate-prone backfill.** Partition and legacy tables allocate IDs
   independently. Backfill identifies rows by `(id, ts)`, which is not a stable
   logical identity after the ID streams diverge.
6. **Incorrect verification window.** `ORDER BY chain_seq ASC LIMIT n` checks
   the oldest records even though the UI says “latest.” It also skips the first
   predecessor boundary, does not require consecutive positive sequence values,
   ignores scan errors, and reports an empty result as healthy.
7. **Rotation ambiguity.** Historical rows do not record a chain version or key
   ID, while verification uses only the current master key.
8. **Retention contradiction.** Audit partitions can be dropped by the generic
   time-series retention job even though audit history is documented as
   append-only. Verification can then accept a truncated chain.
9. **Duplicate reads.** Recent audit and event loaders read partition rows by
   local partition ID, so mirrored duplicates can reappear in the UI and the
   ordering is not a reliable event order.
10. **The same dual-track pattern affects events and AI calls.** Event writes are
    not transactional. AI-call writes use a transaction but swallow errors,
    allowing partial commits despite the transaction wrapper.
11. **Weak API and UI semantics.** Database failures and integrity failures are
    both returned as HTTP 200 booleans, internal diagnostics become visible UI
    copy, and degraded/empty/unverifiable states are not represented.

## Considered Approaches

### A. Repair the Existing Dual-Track Architecture — Selected

Keep the existing tables, preserve all unique records, correct the schema order,
make new writes atomic, reconcile mirrors by logical identity, and introduce a
versioned verifier. This has the smallest safe migration surface and retains
compatibility with existing readers and backups.

### B. Create a New Unified Audit Table

Build a third authoritative table and migrate the union of both existing tables.
This produces a cleaner end state but adds a larger cutover, rollback, and data
validation surface than the current defect requires.

### C. Tolerate JSONB in the Verifier Only

Reconstruct v1 payloads and change the frontend alert. This removes the visible
symptom but leaves migration loss, duplicates, partial writes, multi-process
races, retention truncation, and twin-table drift. It is explicitly rejected.

## Architecture

### Schema and Startup Order

Startup will run in this order:

1. Create or update the legacy bootstrap schema.
2. Create partition parents and child partitions.
3. Add audit-chain metadata columns to both audit tables.
4. Reconcile and backfill dual-track tables.
5. Run versioned migrations that depend on both legacy and partitioned tables.
6. Start background ingestion workers.

Both `audit_log` and `audit_log_p` gain:

- `chain_version SMALLINT NOT NULL DEFAULT 1`
- `chain_key_id TEXT NOT NULL DEFAULT ''`

Both audit tables receive indexes for `content_hash` and `chain_seq` lookups.
After exact duplicates are removed, the legacy table receives a partial unique
index on non-empty `content_hash`, and the partitioned table receives a partial
unique index on `(content_hash, ts)` so the partition key is included. The
migration aborts before creating either guard if conflicting copies remain.

### Historical Reconciliation

Before any cleanup, the rollout creates a PostgreSQL backup and records row,
unique-hash, sequence, and conflict counts.

Reconciliation uses `content_hash` as the historical audit logical identity:

1. Detect repeated hashes in each table.
2. Classify a repeated hash as an exact mirror only when `ts`, JSONB content,
   `prev_hash`, `chain_seq`, `chain_version`, and `chain_key_id` agree.
3. Abort on same-hash/different-content or same-sequence/different-hash
   conflicts. These are possible integrity incidents and must not be resolved
   automatically.
4. Remove only exact redundant partition mirrors, keeping one physical copy of
   every logical record.
5. Insert missing unique hashes from the partition table into the legacy table
   and vice versa. Historical IDs may differ when an existing primary key is
   occupied; chain order and logical identity come from `chain_seq` and
   `content_hash`, not the physical ID.
6. Advance table sequences to at least their maximum stored IDs so later default
   inserts cannot reuse an old value.

The migration is transactional where PostgreSQL DDL permits it and idempotent on
every restart. It logs counts, not audit bodies or secrets.

### Versioned Chain Format

All payloads are produced by one canonical function:

1. Decode stored JSONB into `LogEntry` when verifying.
2. Marshal `LogEntry` with Go's deterministic struct field order and `omitempty`
   rules.
3. Reject malformed or semantically invalid audit JSON rather than skipping it.

Version 1 verification reproduces the existing framing:

`HMAC-SHA256(key, prev_hash || NUL || canonical_payload || NUL || decimal_seq)`

It tries the current and configured previous keys because v1 rows do not carry a
key ID. If no available key matches, the result is `unverifiable` with a legacy
key-or-content ambiguity code; it is not labeled as confirmed tampering.

Version 2 adds domain separation and an explicit key ID:

`HMAC-SHA256(key, "aiops-audit-chain/v2" || NUL || prev_hash || NUL || canonical_payload || NUL || decimal_seq)`

New rows store `chain_version=2` and the current `AIOPS_SECRET_KEY_ID`. Version 2
selects the exact matching current or previous key. A missing key yields
`unverifiable`; a present key with a mismatched HMAC yields `broken`.
When no configured master key exists, append and verification both select the
existing deterministic development fallback under the current key ID and mark
the result `degraded`. This keeps degraded development chains verifiable without
mistaking the fallback for a compliance-grade secret.

### Atomic Append

`appendAuditChained` becomes a database-backed operation:

1. Begin a PostgreSQL transaction.
2. Acquire a transaction-scoped advisory lock dedicated to the audit chain.
3. Read the highest committed chain sequence and hash from the reconciled
   partition mirror.
4. Canonicalize the `LogEntry`, compute the next version 2 link, and insert into
   `audit_log ... RETURNING id`.
5. Insert the same data, metadata, and returned ID into `audit_log_p`.
6. Commit only after both writes succeed; otherwise roll back everything.

The global `auditChainPrev` / `auditChainSeq` state and startup hydration are
removed. This prevents insert failure gaps, restart drift, and multi-process
races. Errors are returned to the caller and logged with sequence/operation
metadata but without audit body or key material.

### Verification

The verifier:

- defaults a missing `limit` to 200 and rejects values outside 1–5000;
- reads the latest requested logical entries plus the preceding boundary entry;
- orders the selected window by `chain_seq` for forward verification;
- checks positive and consecutive sequence values;
- checks predecessor linkage, canonical payload validity, chain version, key
  availability, and HMAC;
- treats duplicate or conflicting logical entries as an integrity failure;
- returns `empty` when no chain exists;
- propagates row scan and `rows.Err()` failures as `unavailable`;
- returns `from_seq`, `to_seq`, `checked`, version information, and verification
  time without leaking SQL or secrets.

The legacy and partition mirrors are also compared by unique content hash during
startup and exposed as a non-sensitive parity indicator. A mirror parity failure
is `degraded` when the logical chain is verifiable and `broken` when conflicting
logical records exist.

### Retention and Recent Reads

`cleanupAuditAndEvents` no longer drops `audit_log_p` partitions. Audit history
remains append-only unless a future explicitly designed checkpoint/archive
protocol replaces this rule.

Recent audit and event reads order by event timestamp plus stable identity and
deduplicate logical mirrors. They never rely on independently allocated
partition IDs to infer chronology.

### Event and AI-Call Dual Tracks

Event and AI-call writers use the same stable identity pattern:

1. Insert the legacy row with `RETURNING id` inside a transaction.
2. Insert the partition row with that exact ID and timestamp.
3. Return an error from either insert so the transaction rolls back.

Backfill becomes content-aware for historical rows whose physical IDs already
diverged and stable-ID based for new rows. Exact mirrors are idempotent; genuine
same-value events remain distinct when their authoritative legacy IDs differ.
No historical event or AI-call cleanup occurs unless runtime evidence identifies
an exact duplicate.

## API Contract

`GET /api/v1/audit/verify-chain?limit=200` returns a structured result:

```json
{
  "ok": true,
  "status": "healthy",
  "code": "ok",
  "checked": 200,
  "from_seq": 30,
  "to_seq": 229,
  "broken_at": 0,
  "chain_versions": [1, 2],
  "mirror_parity": true,
  "secret_degraded": false,
  "verified_at": 1786284000
}
```

Statuses are:

- `healthy`: requested window and mirror parity verify.
- `degraded`: chain verifies but uses a legacy/shared-key path or mirror parity
  still needs a safe repair.
- `broken`: confirmed content, predecessor, sequence, version, or mirror
  conflict with the necessary verification key available.
- `unverifiable`: the required historical key is unavailable or v1 cannot
  distinguish missing key from content mismatch.
- `empty`: no chained records exist.
- `unavailable`: PostgreSQL/query/scan failure prevented verification.

Invalid limits return HTTP 400. Database unavailability returns HTTP 503.
Completed integrity checks, including `broken`, return HTTP 200 because the
request itself succeeded. `ok` remains for compatibility and is true only for
`healthy` or `degraded` results.

## Frontend Design

The audit-export tab separates two concerns:

1. **Audit chain status** — a compact status card with state, verified range,
   count, versions, timestamp, and a dedicated retry action.
2. **Audit export configuration** — the existing form in its own card, with its
   own loading/error state and refresh/save actions.

The UI maps structured status and error codes to zh-CN, zh-TW, and English copy:

- green for `healthy`;
- yellow for `degraded` or `unverifiable`;
- red only for confirmed `broken`;
- neutral information for `empty`;
- retryable error treatment for `unavailable`.

Raw backend `detail` text is not rendered as the description. The TypeScript API
type enumerates statuses and reason codes. A pure shared status-mapping function
keeps color, title, and description rules testable outside the Vue template.

## Error Handling and Safety

- Reconciliation aborts on ambiguous conflicts and leaves all source rows in
  place.
- Database backups are created before the first data-changing migration.
- Transaction rollback prevents half-written twins.
- Advisory locking prevents concurrent sequence allocation across processes.
- Verification does not skip malformed rows or scan failures.
- Logs contain IDs, counts, versions, and error classes only; no audit JSON,
  credentials, HMAC keys, or supplied login password is logged.
- The supplied local admin credential is used only for browser verification and
  is never written into repository files or test fixtures.

## Test Strategy

### Go Unit Tests

- v1 verification survives JSONB whitespace and key-order normalization.
- v1 tries current and previous keys without rewriting stored hashes.
- v2 records and selects the expected key ID.
- Version 2 payload, predecessor, sequence, and version mutations are confirmed
  as `broken`; an unmatched version 1 payload remains `unverifiable` because its
  historical key ID was never stored.
- Missing v2 key is `unverifiable`, not `broken`.
- Empty rows return `empty`.
- Scan errors and `rows.Err()` return `unavailable`.
- Latest-window selection verifies its predecessor boundary.
- Limits are defaulted, accepted, or rejected at their exact boundaries.

### Transaction and Migration Tests

- Fresh database ordering creates legacy tables before dependent partition
  migration and version migrations.
- Audit twin insert commits only when both inserts succeed.
- Either insert failure rolls back and does not allocate a persistent chain gap.
- Concurrent appenders receive unique consecutive sequences.
- Exact duplicate reconciliation is idempotent.
- Same-hash/different-content and same-sequence/different-hash cases abort.
- Event and AI-call twin writes share IDs and roll back on the second failure.
- Backfill does not duplicate a previously mirrored logical row.
- Audit retention never calls partition cleanup for `audit_log_p`.

### Frontend and Browser Tests

- The pure status mapper covers all six states and representative reason codes.
- Type checking, i18n validation, API contract validation, parity checks, and the
  production build pass.
- The real Docker UI displays localized status text, correct severity, checked
  range, and independent retry/config behavior.
- No console errors occur on the security page.

### Full Verification Commands

- `go test ./cmd/server -count=1`
- `go test ./... -count=1`
- `npm run check` in `frontend/`
- Docker image rebuild and health checks
- Read-only PostgreSQL parity/conflict queries
- Browser verification of `/v2/#/security?tab=auditExport`

The current worktree contains unrelated, unfinished install/folder-ID signature
changes whose test call sites do not yet compile. Implementation may make only
the mechanical argument alignment required to restore the pre-existing test
suite; it must not overwrite or redesign those user-owned changes.

## Rollout and Rollback

1. Capture baseline test output and database metadata.
2. Create a timestamped `pg_dump` backup outside the repository.
3. Deploy schema/order and reconciliation changes to the local Docker stack.
4. Confirm reconciliation counts and zero ambiguous conflicts.
5. Append a harmless audit event and verify both tables share its ID, sequence,
   version, key ID, and hash.
6. Verify the full chain/API and browser status.
7. Run all release checks.

If reconciliation or verification fails, stop the server, restore the backup,
and return to the prior image. Do not repair hashes or delete a conflicting
unique row to force a green status.

## Non-Goals

- Re-hashing or resealing historical audit content.
- Hiding a real integrity conflict behind a warning.
- Redesigning unrelated security-center features.
- Introducing a third authoritative audit table.
- Implementing a new audit archive/checkpoint protocol in this change.
