# Phase 1: Resource List Scale Triage — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make containers and K8s pods/deployments usable at ~400 containers / ~1000 pods by paginating APIs and both UIs (classic + Vue), fixing per-pod host-index rebuild, and lazy-loading K8s in Vue Resources.

**Architecture:** Extend list handlers to return paged `items` + `total` (containers) or `items` + `continue`/`truncated` (K8s). Classic and Vue both request `limit=50` (max 200) and render only the current page. Host↔node linking builds a host index **once per request**.

**Tech Stack:** Go `cmd/server`, classic JS under `cmd/server/web/js/`, Vue 3 + Element Plus + TanStack Query under `frontend/`.

**Spec:** `docs/superpowers/specs/2026-08-08-scale-performance-roadmap-design.md` (Phase 1 only)

## Global Constraints

- Pagination: `limit` default **50**, max **200**; never silently truncate without pager/`continue`/`total`
- Phase acceptance requires **classic AND Vue** both verified
- Backward compatible: callers that omit paging params must not break internal `getAllContainerInventories` users (topology/search call PG directly — OK)
- HTTP list responses: prefer additive fields (`items`, `total`, `limit`, `offset`, `continue`, `truncated`) over removing `inventories`/`items`
- Do not commit unless the user explicitly asks (repo user rule)
- Phases 2–4 (hosts/push, Hyper-V/hardware, security) are **out of scope** for this plan

## File map

| File | Responsibility |
|------|----------------|
| `cmd/server/containers_api.go` | Paged flat container list |
| `cmd/server/containers_page_test.go` (create) | Container paging/filter tests |
| `cmd/server/k8s_client.go` | ListPods/Deployments return `continue` |
| `cmd/server/k8s_api.go` | Clamp limit; pass continue; host index once |
| `cmd/server/k8s_link.go` | Index-aware host match helper |
| `cmd/server/k8s_pods_page_test.go` (create) | Limit clamp + index-once tests |
| `cmd/server/web/js/containers.js` | Classic pager UI |
| `cmd/server/web/js/k8s.js` | Classic pods/deploys paged fetch + pager |
| `frontend/src/api/modules.ts` | `containersApi` / `k8sApi` query params |
| `frontend/src/views/ResourcesView.vue` | Container table pagination; lazy K8s |
| `frontend/src/views/K8sView.vue` | Pods/deploys server page + ns in API |

Reuse existing: `filterContainersPage` / `compactContainerRow` in `cmd/server/sreyun_resource.go`.

---

### Task 1: Shared paging helpers + container list API

**Files:**
- Modify: `cmd/server/containers_api.go`
- Create: `cmd/server/containers_page_test.go`
- Optionally extract tiny helpers into `cmd/server/list_page.go` if duplication appears — only if needed

**Interfaces:**
- Produces HTTP shape when `limit` **or** `offset` **or** `status` **or** `q` is present (UI always sends `limit`):

```json
{
  "items": [ { "host_id", "host_name", "id", "name", "image", "state", ... } ],
  "total": 412,
  "limit": 50,
  "offset": 0,
  "ts": 123
}
```

- When **no** paging/filter query keys: keep legacy `{ "inventories": [...], "ts": ... }` for unexpected old clients
- Query params: `limit`, `offset`, `host`, `status` (`all|running|stopped|other`), `q` (substring on name/image/host)
- Consumes: `getAllContainerInventories` / `getContainerInventory`, `filterInventoryRows`, reuse `filterContainersPage` patterns (flatten across hosts)

- [ ] **Step 1: Write failing tests** in `containers_page_test.go`

```go
func TestContainerListPagedDefaults(t *testing.T) {
	// Seed pg or stub Server with fake inventories totaling >50 containers
	// GET /api/v1/containers/list?limit=50&offset=0
	// Expect: len(items)==50, total==N, limit==50, offset==0
}

func TestContainerListOffsetAndStatus(t *testing.T) {
	// status=running reduces total; offset=50 returns next page
}

func TestContainerListLegacyInventoriesWithoutLimit(t *testing.T) {
	// GET without limit/offset/status/q → response has inventories key
}
```

Adapt to how other `cmd/server` tests mock `pgStore` (temp PG or in-memory fake if used elsewhere). If PG is hard to spin, unit-test a pure `flattenContainerInventories(rows) []map` + page function extracted from the handler.

- [ ] **Step 2: Run — expect fail**

```bash
go test ./cmd/server/ -count=1 -run 'ContainerList'
```

- [ ] **Step 3: Implement**

In `containers_api.go`:

```go
func parseListLimitOffset(r *http.Request) (limit, offset int, paged bool) {
	q := r.URL.Query()
	if q.Get("limit") == "" && q.Get("offset") == "" && q.Get("status") == "" && q.Get("q") == "" {
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
```

Flatten each inventory’s `containers` array into rows with `host_id` / `host_name` / `runtime` (mirror classic `ctFlatRows`). Apply host ACL via existing `filterInventoryRows` before flatten. Apply `status` + `q` filters. Slice `[offset:offset+limit]`. Write JSON with `items`/`total`/`limit`/`offset`.

- [ ] **Step 4: Run — expect pass**

```bash
go test ./cmd/server/ -count=1 -run 'ContainerList|filterContainers'
```

- [ ] **Step 5: Commit** — skip unless user asks.

---

### Task 2: K8s ListPods/Deployments continue + host index once

**Files:**
- Modify: `cmd/server/k8s_client.go`
- Modify: `cmd/server/k8s_link.go`
- Modify: `cmd/server/k8s_api.go`
- Create: `cmd/server/k8s_pods_page_test.go`

**Interfaces:**
- Change list client to:

```go
type k8sListResult struct {
	Items           []map[string]any
	Continue        string
	RemainingApprox int // from remainingItemCount if present; else 0
}

func (c *k8sRESTClient) ListPods(namespace string, limit int, cont string) (k8sListResult, error)
func (c *k8sRESTClient) ListDeployments(namespace string, limit int, cont string) (k8sListResult, error)
```

- Update all call sites (`k8s_api.go`, `sreyun_resource.go`, overview) to pass `cont=""` and use `.Items`
- API response:

```json
{
  "items": [...],
  "limit": 50,
  "continue": "<token or empty>",
  "truncated": true,
  "remaining": 0
}
```

- Default `limit`: **50** (was 500); clamp max **200**
- Query: `namespace`, `limit`, `continue`
- Host linking: build `k8sHostIndex` once from `ListHosts()`, then `matchHostForK8sNodeWithIndex(node, idx)`

- [ ] **Step 1: Failing tests**

```go
func TestK8sPodsLimitClamp(t *testing.T) {
	// limit missing → 50; limit=999 → 200
}

func TestK8sPodsHostIndexOnce(t *testing.T) {
	// With stub ListHosts counting calls: enriching N pods must not call ListHosts N times
	// Prefer testing matchHostForK8sNodeWithIndex uses a prebuilt map
}
```

- [ ] **Step 2: Run — expect fail**

```bash
go test ./cmd/server/ -count=1 -run 'K8sPods'
```

- [ ] **Step 3: Implement client + link + handlers**

In `k8s_client.go` `ListPods`: parse `metadata.continue` and `metadata.remainingItemCount` from list JSON; set query `continue` when `cont != ""`.

In `k8s_link.go`:

```go
type k8sHostIndex struct {
	byName map[string]*Host
	byIP   map[string]*Host
}

func (s *Server) buildK8sHostIndex() k8sHostIndex { /* move loop from matchHostForK8sNode */ }

func (s *Server) matchHostForK8sNodeWithIndex(nodeName string, addrs []string, idx k8sHostIndex) *Host

func (s *Server) matchHostForK8sNode(nodeName string, addrs []string) *Host {
	return s.matchHostForK8sNodeWithIndex(nodeName, addrs, s.buildK8sHostIndex())
}
```

In `handleK8sPods`: `idx := s.buildK8sHostIndex()` once; for each pod use `WithIndex`.

- [ ] **Step 4: Fix compile of all ListPods/ListDeployments callers**

```bash
go test ./cmd/server/ -count=1 -run 'K8sPods|RoutesRegister'
```

- [ ] **Step 5: Commit** — skip unless user asks.

---

### Task 3: Classic UI — containers pager

**Files:**
- Modify: `cmd/server/web/js/containers.js`

**Interfaces:**
- Consumes: `GET /api/v1/containers/list?limit=50&offset=&host=&status=&q=`
- State: `CT_PAGE = { limit: 50, offset: 0, total: 0 }`
- Stats row: prefer `total` from API; if API also returns summary counts later use them — for v1 compute running/stopped only from **current page** OR add lightweight `stats` in API (optional). Prefer API returning `{ stats: { total, running, stopped, hosts } }` computed on full filtered set before slice — add in Task 1 if cheap.

- [ ] **Step 1: Change `loadContainersPanel` to request paging**

```js
const limit = CT_PAGE.limit || 50;
const offset = CT_PAGE.offset || 0;
const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
if (CT_FILTER.host) params.set("host", CT_FILTER.host);
if (CT_FILTER.state && CT_FILTER.state !== "all") params.set("status", CT_FILTER.state);
if (CT_FILTER.q) params.set("q", CT_FILTER.q);
const r = await fetch(`${API}/containers/list?${params}`, { credentials: "same-origin" });
// store j.items, j.total; reset offset to 0 when filters change
```

- [ ] **Step 2: Render only `items`; add pager controls**

Prev/Next (or page numbers) below table; show `offset+1–min(offset+limit,total) / total`. Changing search/host/state resets `offset=0` and reloads.

- [ ] **Step 3: Keep actions/logs working** using row `host_id`/`id` from paged items (same as today).

- [ ] **Step 4: Manual check** — classic containers page with many containers: only ~50 rows in DOM.

- [ ] **Step 5: Commit** — skip unless user asks.

---

### Task 4: Classic UI — K8s pods/deployments pager

**Files:**
- Modify: `cmd/server/web/js/k8s.js`

**Interfaces:**
- Fetch: `/k8s/clusters/{id}/pods?namespace=&limit=50&continue=`
- Cache: `K8S_CACHE.pods` = current page only; `K8S_CACHE.podsContinue`, `K8S_CACHE.podsContinueStack` for Prev
- `paintK8sPods`: paint current page (already filtered client-side for phase/q — prefer moving phase filter server-side later; for v1 keep client filter **on current page only** and document that phase filter is page-local OR refetch without client phase filter). **Decision for v1:** drop client phase filter on full cache; add `phase` query only if cheap — otherwise filter current page and show note. Simplest: keep client filter on current page items only; namespace change refetches.

- [ ] **Step 1: Update pods fetch** (~line 507) to pass `limit=50` and `continue`

```js
const params = new URLSearchParams({ limit: "50" });
if (ns) params.set("namespace", ns);
if (cont) params.set("continue", cont);
const j = await k8sFetch(`/k8s/clusters/${id}/pods?${params}`);
K8S_CACHE.pods = j.items || [];
K8S_CACHE.podsContinue = j.continue || "";
K8S_CACHE.podsTruncated = !!j.truncated;
```

- [ ] **Step 2: Same for deployments fetch**

- [ ] **Step 3: Pager UI** in `paintK8sPods` / deployments paint — Next uses `continue`; Prev pops stack; show truncated hint when more pages exist

- [ ] **Step 4: Manual check** at multi-namespace clusters

- [ ] **Step 5: Commit** — skip unless user asks.

---

### Task 5: Vue API client + Resources containers pagination

**Files:**
- Modify: `frontend/src/api/modules.ts`
- Modify: `frontend/src/views/ResourcesView.vue`

**Interfaces:**

```ts
// containersApi.list
list: (params?: { limit?: number; offset?: number; host?: string; status?: string; q?: string }) =>
  api<{ items?: ...; total?: number; limit?: number; offset?: number; inventories?: ... }>(
    `/containers/list?${qs}`
  )
```

- `ctrQ` uses `limit: 50`, `offset: (page-1)*50`, status/host/q from filters
- Template: `el-pagination` like HostsView (`PAGE_SIZE = 50`)
- Display `total` from API

- [ ] **Step 1: Extend `containersApi.list` with query string**

- [ ] **Step 2: Wire `ResourcesView` container tab** — page state, refetch on filter/page change; stop flattening full inventories when `items` present

- [ ] **Step 3: `npm run check`**

- [ ] **Step 4: Commit** — skip unless user asks.

---

### Task 6: Vue K8sView paging + lazy import

**Files:**
- Modify: `frontend/src/api/modules.ts` (`k8sApi.pods` / `deployments`)
- Modify: `frontend/src/views/K8sView.vue`
- Modify: `frontend/src/views/ResourcesView.vue` (lazy `K8sView`)

**Interfaces:**

```ts
pods: (id: string, opts?: { namespace?: string; limit?: number; continue?: string }) =>
  api<{ items?: Pod[]; continue?: string; truncated?: boolean; limit?: number }>(...)
```

- Pass `nsFilter` into API (not only client filter)
- Page size 50; Next/Prev via continue token (el-pagination may be approximate — use Prev/Next buttons if total unknown)
- `ResourcesView.vue`: replace `import K8sView from ...` with `defineAsyncComponent(() => import("@/views/K8sView.vue"))`

- [ ] **Step 1: Update `k8sApi.pods` / `deployments` signatures**

- [ ] **Step 2: Update `K8sView.vue` queries** — include namespace + limit + continue in queryKey; bind table to page items; pager UI

- [ ] **Step 3: Lazy-load K8s in Resources**

- [ ] **Step 4: `npm run check`**

- [ ] **Step 5: Commit** — skip unless user asks.

---

### Task 7: Phase 1 verification gate

**Files:** none (checklist)

- [ ] **Step 1: Backend**

```bash
go test ./cmd/server/ -count=1 -run 'ContainerList|K8sPods|filterContainers'
```

- [ ] **Step 2: Frontend**

```bash
cd frontend && npm run check
```

- [ ] **Step 3: Manual / docker classic** — containers + k8s pods: DOM row count ≈ page size; pager reaches more data; no silent-only-500 without Next

- [ ] **Step 4: Manual / docker Vue `/v2`** — same for Resources containers + K8s tab; confirm K8s chunk loads async

- [ ] **Step 5: Record results** in `.superpowers/sdd/` or plan checkbox notes for Phase 2 handoff

---

## Spec coverage (Phase 1)

| Spec item | Task |
|-----------|------|
| Paginate containers API + total | 1 |
| Paginate k8s pods/deploys + no silent truncate | 2, 4, 6 |
| Fix O(pods×hosts) host match | 2 |
| Classic containers/pods pager | 3, 4 |
| Vue containers/pods pager + ns in API | 5, 6 |
| Lazy-load K8sView | 6 |
| Both UIs acceptance | 7 |

## Out of scope (later phases)

Hosts slim payload / push 3s alerts, Hyper-V collapse, hardware N+1, security rate limits.
