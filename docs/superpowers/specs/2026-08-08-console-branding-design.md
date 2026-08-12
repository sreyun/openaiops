# Console branding (logo + product name) — Design

Date: 2026-08-08  
Status: approved (conversation); awaiting file review  
Scope: Vue frontend (`/v2`) + Go server API

## Problem

The new Vue console hardcodes product branding via i18n and markup:

- Product name: `app.name` → `AIOps`
- Tagline: `layout.productTagline` → localized default
- Mark: letter `A` in `AppLayout` / `LoginView`
- Browser title: `` `${page} · AIOps` `` in the router

There is no global admin setting. Per-dashboard `logo_url` and status-page title are unrelated.

Operators need to configure **product name**, **tagline**, and **logo** from **System Settings**, with changes applying to **login**, **header**, and **document title**.

## Goals

1. Admin can set product name, tagline, and logo in Settings.
2. Unauthenticated login page shows configured brand (public read API).
3. Empty fields fall back to existing i18n / letter mark (no blank chrome).
4. Follow existing patterns: status-page admin routes + dashboard asset upload limits.

## Non-goals (v1)

- Classic embedded UI (`cmd/server/web`) brand block
- Favicon / PWA `manifest.json` name
- Auto-sync SMTP `from_name`
- Env-file / deploy-time-only branding overrides
- Per-user or per-tenant branding

## Approach

**Dedicated brand config + public GET** (chosen over folding into `/api/v1/config` or frontend-only storage).

## Data model

Add to `ServerConfig`:

```go
Brand BrandConfig `json:"brand,omitempty"`

type BrandConfig struct {
	ProductName string `json:"product_name"` // empty → i18n app.name
	Tagline     string `json:"tagline"`      // empty → i18n layout.productTagline
	LogoURL     string `json:"logo_url"`     // empty → letter "A" mark
}
```

Persistence: same `ConfigStore` path as other server settings (Postgres-backed config).

Logo files: `data/brand-assets/` (server data dir).  
Limits (match dashboard assets):

- Max size: 512 KiB
- Types: `image/png`, `image/jpeg`, `image/webp`, `image/svg+xml`
- Reuse sniff/validate helpers from `dashboard_assets.go` where practical

## API

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| `GET` | `/api/v1/brand` | Public | Return `{ product_name, tagline, logo_url }` for login + shell |
| `GET` | `/api/v1/admin/brand` | Admin | Same payload (settings form load) |
| `POST` | `/api/v1/admin/brand` | Admin | Save `product_name` + `tagline` (+ optional `logo_url` clear via empty string) |
| `POST` | `/api/v1/admin/brand/logo` | Admin | Multipart upload; set `logo_url` to public asset URL |
| `DELETE` | `/api/v1/admin/brand/logo` | Admin | Clear `logo_url` and delete stored file when local |
| `GET` | `/api/v1/brand/logo/{name}` | Public | Serve uploaded logo bytes |

Notes:

- Register `GET /api/v1/brand` and logo asset GET on `isPublicPath` (and CSRF-exempt as GETs already are).
- Mutating admin routes require session + admin role and existing CSRF Origin checks.
- `logo_url` stored as a server-relative path such as `/api/v1/brand/logo/<filename>` so reverse proxies (e.g. frontend nginx on `:8080`) keep same-origin.

## Frontend

### Brand store

`frontend/src/stores/brand.ts`:

- `fetchBrand()` → `GET /api/v1/brand`
- Computed display helpers: `productName`, `tagline`, `logoUrl` with i18n fallbacks
- Call on app boot and on Login route enter; refresh after settings save

### Surfaces

| Surface | Behavior |
|---------|----------|
| `LoginView.vue` | Logo img or `A` mark; name + tagline from store |
| `AppLayout.vue` | Same for header `global-brand` |
| `router/index.ts` | `` document.title = page === productName ? page : `${page} · ${productName}` `` |
| `LangSwitch.vue` | Use store product name instead of hardcoded `AIOps` if it currently hardcodes |

### Settings UI

New card on `SettingsView.vue` (near status page):

- Inputs: product name, tagline
- Logo: preview, upload, clear (mirror dashboard appearance upload UX)
- Save button → `POST /api/v1/admin/brand` (+ upload endpoint when file chosen)
- i18n keys in `zh-CN` / `en` / `zh-TW` under `settings.brand*`

### API client

Extend `frontend/src/api/modules.ts` with `brandApi` (`getPublic`, `getAdmin`, `save`, `uploadLogo`, `clearLogo`).

## Security / ops

- Only admins may write brand or upload logo.
- Public GET returns only the three display fields (no secrets).
- SVG allowed like dashboards; keep existing content-type sniffing.
- Trust-proxy / CSRF unchanged; admin POSTs must continue to pass Origin checks (already fixed for `:8080` nginx).

## Testing

- Go: public GET returns defaults when empty; admin set/get round-trip; logo upload reject oversize / bad type; non-admin POST → 403.
- Frontend: manual — Settings save → login + header + tab title update without hard refresh (after store refresh).
- Optional: Vue unit/store test if project already has Pinia test patterns; not required if none exist nearby.

## Acceptance criteria

1. Admin opens Settings → Brand, sets name/tagline, uploads logo, saves.
2. Login page (logged out) shows new name, tagline, logo.
3. After login, header shows the same; browser tab is `… · <product name>`.
4. Clearing fields/logo restores i18n defaults and letter mark.
5. Classic UI and favicon remain unchanged in v1.
