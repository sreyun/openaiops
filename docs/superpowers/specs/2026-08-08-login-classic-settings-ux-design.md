# Login parity + classic entry + settings form UX — Design

**Date:** 2026-08-08  
**Status:** Approved (conversation); awaiting file review  
**Scope:** Vue `/v2` + frontend Nginx (`docker/nginx/nginx-frontend.conf`); settings forms  
**Follow-on (auto after B+A):** C host tree, D Hermes voice, E visual token consolidation

## Problem

1. **Classic UI unreachable from Vue:** `goClassicUi()` sets `location = "/"` while frontend Nginx does `location = / { return 302 /v2/; }`, so “经典版” loops back into the new console (blank / error / wrong app).
2. **Login parity:** Operators expect Vue login flows to match classic (username/phone, password, MFA, SSO, recover, forced password change). Gaps in entry/copy/validation hurt trust.
3. **Settings form overflow (screenshot):** Agent auto-update form uses `label-width="160px"` with long labels such as `落后目标版本时自动推送（默认开启）`, causing clipped/overlapping text (e.g. visible fragment `送 (默认开`).

## Goals (B + A)

### B — Login + classic entry

1. Same-origin classic entry via **`/classic/`** reverse-proxied to `aiops-server` classic dashboard (`GET /`).
2. Proxy classic absolute static assets on the frontend host so HTML under `/classic/` can load `/app.js`, `/style.css`, `/js/`, `/css/`, i18n, etc.
3. Vue menu “经典版” navigates to `/classic/` (shared cookie domain with `/api`).
4. Keep `/` → `/v2/` (new console remains default).
5. Audit Vue `LoginView` vs classic login; close material gaps (methods, MFA, SSO, recover, forced change) without changing auth API semantics.

### A — Settings form UX

1. Fix Agent auto-update (and similar) label overflow: wider labels and/or top labels; no clipped overlap.
2. Sweep Settings tabs for long `el-form-item` labels, spacing, hint hierarchy.
3. Prefer existing type tokens (title / body / muted hint); do not introduce new font families.

## Non-goals (this phase)

- Rewriting classic internal SPA routes or embedding classic inside an iframe.
- Host tree JumpServer-parity (phase **C**).
- Hermes STT/TTS/alert announce productization (phase **D**).
- Global visual redesign beyond Settings form readability (phase **E**).

## Approach B — Nginx + Vue navigation

**Chosen:** same-origin `/classic/` + root asset proxy (not direct `:18529`, not swapping `/` to classic).

### Nginx (`docker/nginx/nginx-frontend.conf`)

| Location | Behavior |
|----------|----------|
| `=/` | unchanged `302 /v2/` |
| `=/classic` | `302 /classic/` |
| `=/classic/` | `proxy_pass` backend `/` (classic `handleDashboard` HTML) |
| Classic assets | Proxy to backend: `/app.js`, `/style.css`, `/theme-init.js`, `/i18n-dashboard.js`, `/i18n-dashboard.en.js`, `/i18n-dashboard.zh-TW.js`, `/manifest.json`, `/icon.svg`, `/sw.js`, `/js/`, `/css/` |

Preserve existing `/api/`, `/ws/`, install/dl proxies. Use `$http_host` like other locations for CSRF/Origin.

Optional: `proxy_redirect` / careful trailing-slash so relative links behave; classic uses root-absolute asset URLs so asset proxy is the critical piece.

### Vue

- `AppLayout.goClassicUi()` → `window.location.href = "/classic/"`.
- Any other “bridge to classic” links that use `/` for UI should use `/classic/` (API bridges unchanged).

### Login parity checklist

Compare classic login UI vs `LoginView.vue`:

| Capability | Expected |
|------------|----------|
| Username + password | Present |
| Phone + password | Present (switch) |
| MFA / TOTP when required | Present |
| SSO providers | Present when configured |
| Forgot username / password | Present (recover flow) |
| Forced password change | Present |
| Copy / field order / hints | Align to classic where divergent |

Close gaps found in audit; no new auth endpoints unless classic already calls them and Vue does not.

## Approach A — Settings forms

1. Agent tab: change form to `label-position="top"` **or** `label-width` ≥ 220–280px (prefer top for long Chinese labels).
2. Shorten i18n label if needed: keep meaning, move “(默认开启)” into hint/switch adjacent text to reduce label length.
3. Grep SettingsView for `label-width="160px"` (and similar) with long labels; apply same pattern.
4. Normalize hint class to existing muted token; avoid inline one-off font stacks.

## Success criteria

- From Vue user menu → 经典版 opens classic dashboard at `http://<frontend-host>/classic/` with styles/scripts loaded (not redirected to `/v2/`).
- Session cookie still authenticates classic API calls on same host.
- Settings → Agent: labels fully readable, no overlap with next field.
- Login: parity checklist items pass manual QA vs classic.

## Rollout

1. Nginx classic locations + rebuild `aiops-frontend`.
2. Vue `goClassicUi` (+ any bridge links).
3. Login gap fixes.
4. Settings Agent form + Settings sweep.
5. Playwright/manual: classic entry + Agent tab screenshot check.

## Follow-on phases (auto after B+A)

| Phase | Focus |
|-------|--------|
| **C** | Host/asset tree: collapse, add, context menu (JumpServer-like) vs classic |
| **D** | Hermes: voice input, TTS speak, alert announce — visible, reliable |
| **E** | Global font/spacing/color token consolidation |

Each phase gets its own short design/plan before implementation.
