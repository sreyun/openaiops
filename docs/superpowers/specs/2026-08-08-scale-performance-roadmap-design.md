# Scale performance roadmap (classic + Vue) — Design

Date: 2026-08-08  
Status: approved (conversation); awaiting file review  
Scope: Classic UI (`cmd/server/web`), Vue `/v2` (`frontend/`), and supporting Go list/push APIs

## Problem

A production classic-UI deployment (~200 hosts, 400+ containers, ~1000 K8s pods, 30+ API-mon endpoints, logs, content audit) feels severely laggy. Reported symptoms include:

- Containers and pods without usable pagination
- General UI stutter
- Hyper-V / VMs cannot collapse cleanly
- Desire to harden concurrency and security while lifting both classic and new frontends

Exploration confirmed the same cliffs on **both** UIs: unbounded (or silently truncated) inventory fetches, full-table DOM paints, rich `/hosts` polling, push-side full alert recompute, hardware per-host N+1 health fan-out, and Hyper-V full-panel `innerHTML` rebuild on expand/collapse.

## Goals

1. Make inventory pages usable at the stated scale (containers, pods, related resource tables).
2. Cut server and browser work from host polling and realtime push.
3. Fix Hyper-V collapse UX and hardware fan-out cliffs.
4. Keep **classic and Vue in parity** per phase — both must pass acceptance before a phase is done.
5. Improve concurrency backpressure and security hygiene for large-list / admin surfaces.

## Non-goals (roadmap v1)

- Rewriting classic UI as a SPA or replacing its architecture wholesale
- Deep VictoriaMetrics / storage engine redesign
- Remote-desktop codec work (orthogonal to inventory lag)
- Full white-label / branding (separate track)

## Approach (chosen)

**A — Per phase: server contract + classic UI + Vue UI together.**  
Production classic benefits immediately; Vue does not re-accumulate the same debt.  
Acceptance mode: **both UIs must pass** phase criteria (not classic-only).

Rejected:

- **B** Classic-only then Vue later → dual-track debt at `/v2` migration
- **C** API-first with UI deferred → weak near-term operator relief

## Scale assumptions

| Resource | Approx count |
|----------|--------------|
| Hosts | ~200 |
| Containers | 400+ |
| K8s pods | ~1000 |
| API-mon endpoints | ~30+ |
| Plus | logs, content audit |

## Shared pagination contract

All new/changed list endpoints in this roadmap should converge on:

| Param / field | Meaning |
|---------------|---------|
| `limit` | Page size; **default 50**, **max 200** |
| `offset` or opaque `cursor` | Prefer `offset` for v1 unless cursor is already present |
| `total` | Total matching rows after filters |
| Filters | Server-side where UI already filters client-side today (status, namespace, host, q) |

Clients must show total + pager (or equivalent virtual window). **Silent truncation without pager is a defect.**

## Phase 0 — Baseline (½–1 day)

- Define acceptance metrics: time-to-interactive for key pages, scroll smoothness, browser main-thread cost, server CPU under N open consoles
- Capture baseline (prod-like or synthetic) for: containers, K8s pods, hosts, Hyper-V, hardware
- Lock pagination defaults above

**Exit:** Written baseline numbers in the implementation plan or a short appendix; team agrees pass/fail thresholds for Phase 1.

## Phase 1 — Resource list triage (highest priority)

### Problems

- Containers: full `GET /containers/list` + full-table DOM (classic `containers.js`, Vue `ResourcesView`)
- K8s pods: classic paints all cached items; server default `limit=500` can silently under-fetch at ~1000 pods; Vue fetches cluster-wide then filters client-side; per-pod host matching can be O(pods × hosts)

### Work

| Layer | Changes |
|-------|---------|
| API | Paginate `containers/list` and `k8s/.../pods` (and deployments); return `total`; server-side namespace/status filters; fix host↔pod matching to build host index **once per request** |
| Classic | Pager (or virtual window) for containers and pods; pass namespace/limit to API |
| Vue | Paginate container table and K8s pods/deploys; pass `nsFilter` into API; lazy-load `K8sView` inside Resources |

### Acceptance

- At ~1000 pods / ~400 containers: scroll and page changes remain interactive
- No silent data loss: UI exposes `total` and can reach all pages
- Classic **and** Vue both verified

## Phase 2 — Host payload + realtime push

### Problems

- Unbounded rich `GET /hosts` (with `latest` samples) + ~5s REST poll on core views
- `/ws/push` every ~3s re-runs full alert evaluation and broadcasts full alerts per browser
- Vue HostsView already client-paginates (50) but still full-list polls; Overview slows poll when WS connected — Hosts/Resources often do not

### Work

| Layer | Changes |
|-------|---------|
| API | List endpoints prefer `hosts/meta` or field-projected host cards; detail fetches `latest` on demand; optional server-side host paging if needed |
| Push | Prefer alert digest / incremental updates; avoid full recompute fan-out where possible; document/coalesce work across connections |
| Classic + Vue | List views consume slim payloads; when WS connected, stretch or stop full `/hosts` REST poll (Overview pattern) |

### Acceptance

- Measurable drop in bandwidth for host list refresh at ~200 hosts
- Measurable drop in server CPU with multiple consoles open
- Classic **and** Vue both verified

## Phase 3 — Hyper-V collapse + hardware N+1

### Problems

- Hyper-V: expand/collapse rebuilds entire panel `innerHTML` (`hyperv.js`); collapse ignored under search in some paths
- Hardware: `Promise.all(hosts.map → /hardware/health)` ≈ 200 parallel GETs (classic + Vue)

### Work

| Layer | Changes |
|-------|---------|
| Hyper-V | Local DOM/state update for collapse; clear search vs collapse semantics; page or lazy-expand large trees |
| Hardware | Aggregated or batched health API + concurrency cap; table pagination |
| Vue | Same behaviors on Hyper-V / hardware tabs |

### Acceptance

- Collapse preserves scroll position and does not flash the whole panel
- Opening hardware does not open ~200 parallel connections
- Classic **and** Vue both verified

## Phase 4 — Concurrency + security hardening

- Rate limits / timeouts / cancelation for large list and fan-out endpoints
- Backpressure on push when clients are slow
- Content-audit / API-mon: field projection or pagination for large bodies (secondary)
- Admin authz review on new aggregate endpoints
- Ops notes: recommended max concurrent console tabs, poll knobs

### Acceptance

- Load test: no cascade timeouts under agreed concurrency
- Security checklist completed for new endpoints
- Classic **and** Vue both verified where UI is touched

## Key evidence (exploration)

Classic: containers full render (`cmd/server/web/js/containers.js`), k8s full paint + limit 500 (`k8s.js` / `k8s_api.go`), rich `/hosts` + poll (`ui_api.go` / `nav.js`), push 3s full alerts (`push.go`), Hyper-V full rebuild (`hyperv.js`), hardware N+1 (`hardware.js`).

Vue: pods/containers unbounded tables (`K8sView.vue`, `ResourcesView.vue`), hardware N+1 (`ResourcesView.vue` `hwQ`), duplicate host query keys, eager `K8sView` import in Resources; HostsView already paged (50).

## Risks

- Changing list JSON shape may break older scripts/integrations — keep backward-compatible fields when possible; add `total` without removing arrays
- Cursor vs offset: stick to offset in v1 unless an endpoint already uses cursors
- Push redesign must not drop alert freshness SLOs — define acceptable latency in Phase 2 plan

## Success criteria (roadmap)

After Phases 1–3, operators at the stated scale can use containers, pods, hosts, Hyper-V, and hardware without the current class of freezes; Phase 4 locks concurrency/security. Both classic and Vue remain feature-capable for these surfaces.
