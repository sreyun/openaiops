# Container Compose-project filter — Design

Date: 2026-08-08  
Status: approved (conversation)  
Parent: scale performance Phase 1 (resource lists)

## Goal

When hosts run many containers, operators filter/group primarily by **Docker Compose project** (and optionally service), not only host/status.

## Data

Extend `shared.ContainerInfo`:

- `compose_project` — from label `com.docker.compose.project` (also accept `io.podman.compose.project` if present)
- `compose_service` — from `com.docker.compose.service`

Agent `collectContainers` adds these via `docker/podman ps --format` label placeholders (no extra inspect per container).

## API

Paged `GET /api/v1/containers/list`:

- Query `compose_project` (exact match, case-sensitive as Docker writes)
- Query `compose_service` (optional exact match)
- Presence of either counts as “paged mode” (same as limit/status/q)
- Response items include `compose_project` / `compose_service` when set
- Optional `compose_projects: string[]` — distinct projects in the **filtered host inventory before page slice** (for dropdowns). Keep cheap: scan flattened filtered set before offset.

## UI

- Classic + Vue: dropdown “Compose 项目” (All + distinct projects)
- Show project (and service if present) as a table column or secondary line under name
- Volume filter: **out of scope** for this slice

## Non-goals

- Changing Compose orchestration console
- Volume-based primary taxonomy
- Requiring agent upgrade for old inventories (missing fields → treat as empty project “（未标注）” only in UI filter “all”)
