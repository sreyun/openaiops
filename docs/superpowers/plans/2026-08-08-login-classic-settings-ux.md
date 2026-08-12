# Login + Classic Entry + Settings Form UX — Implementation Plan

> **For agentic workers:** Execute task-by-task. Skip commits unless user asks. After B+A verify, continue phases C→D→E per roadmap.

**Goal:** Fix Vue→classic entry via `/classic/` proxy; align login gaps; fix Settings Agent form label overflow.

**Architecture:** Nginx proxies `/classic/` + classic root assets to `aiops-server`; Vue navigates to `/classic/`; Settings forms use top labels / wider label-width.

**Tech Stack:** `docker/nginx/nginx-frontend.conf`, Vue `AppLayout` / `LoginView` / `SettingsView`, i18n.

**Spec:** `docs/superpowers/specs/2026-08-08-login-classic-settings-ux-design.md`

## Global Constraints

- Do not commit unless user asks.
- Keep `/` → `/v2/`.
- Do not change auth API semantics.
- Prefer existing CSS tokens for typography.

---

### Task 1: Nginx `/classic/` + classic assets

**Files:** `docker/nginx/nginx-frontend.conf`

- [ ] Add `/classic` → `/classic/`, `/classic/` → `proxy_pass http://aiops_backend/`
- [ ] Proxy classic assets: `app.js`, `style.css`, `theme-init.js`, `i18n-dashboard*.js`, `manifest.json`, `icon.svg`, `sw.js`, `/js/`, `/css/`
- [ ] Preserve Host/`$http_host` headers like other locations

### Task 2: Vue classic navigation

**Files:** `frontend/src/layouts/AppLayout.vue` (+ grep other `/` classic bridges)

- [ ] `goClassicUi()` → `/classic/`
- [ ] Fix any UI “open classic” links that wrongly use `/`

### Task 3: Login parity audit + gaps

**Files:** `frontend/src/views/LoginView.vue`, i18n if needed

- [ ] Diff vs classic login (username/phone, MFA, SSO, recover, forced pw)
- [ ] Close material UX/copy gaps

### Task 4: Settings Agent form + Settings sweep

**Files:** `frontend/src/views/SettingsView.vue`, i18n `agentAuto*` if shortening labels

- [ ] Agent form: `label-position="top"` or wider label-width; no overlap
- [ ] Sweep other long-label `el-form` in Settings

### Task 5: Verify B+A

- [ ] Rebuild `aiops-frontend`
- [ ] Curl `/classic/` returns HTML; `/app.js` proxied
- [ ] Playwright: login → classic menu → classic UI loads; Settings Agent labels readable
- [ ] Fix until green

### Task 6+: Phases C → D → E (auto)

- C: Host tree classic/JumpServer-like interactions
- D: Hermes voice input/TTS/alert announce
- E: Global token consolidation
