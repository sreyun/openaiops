# Console Branding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let admins configure product name, tagline, and logo in Vue Settings; apply them on login, header, and browser tab title via a public brand API.

**Architecture:** Persist `BrandConfig` on `ServerConfig`. Expose public `GET /api/v1/brand` (+ logo file GET) for the login page, and admin write/upload/delete under `/api/v1/admin/brand*`. Vue Pinia `brand` store feeds LoginView, AppLayout, router title, and LangSwitch; Settings gets an admin Brand tab.

**Tech Stack:** Go `cmd/server`, Pinia + Vue 3 + Element Plus (`frontend/`), existing `api()` client and dashboard asset MIME helpers.

**Spec:** `docs/superpowers/specs/2026-08-08-console-branding-design.md`

## Global Constraints

- Vue `/v2` only; do not change classic `cmd/server/web` brand chrome in v1.
- Empty `product_name` / `tagline` / `logo_url` → fall back to i18n `app.name` / `layout.productTagline` / letter `A`.
- Logo ≤ 512 KiB; types png/jpeg/webp/svg (reuse dashboard sniffing).
- `GET /api/v1/brand` and `GET /api/v1/brand/logo/{name}` must be public (`isPublicPath`).
- `/api/v1/admin/brand*` already require admin via `routeAllowed` prefix `/api/v1/admin/`.
- Do not commit unless the user explicitly asks (repo user rule overrides plan commit steps — skip Step “Commit” or stage only).
- Do not add agent `Co-authored-by` trailers if committing later.

## File map

| File | Responsibility |
|------|----------------|
| `cmd/server/brand.go` (create) | `BrandConfig`, ConfigStore get/set, HTTP handlers, logo disk IO |
| `cmd/server/brand_test.go` (create) | Public GET, admin round-trip, upload reject, logo serve |
| `cmd/server/config.go` | Add `Brand` field; preserve on general `Set` merge |
| `cmd/server/auth.go` | Public paths for brand GET + logo prefix |
| `cmd/server/handlers.go` | Register routes |
| `frontend/src/api/modules.ts` | `brandApi` |
| `frontend/src/stores/brand.ts` (create) | Fetch + display helpers |
| `frontend/src/main.ts` | Boot-fetch brand |
| `frontend/src/router/index.ts` | Document title uses brand product name |
| `frontend/src/components/LangSwitch.vue` | Same title helper |
| `frontend/src/layouts/AppLayout.vue` | Header brand from store |
| `frontend/src/views/LoginView.vue` | Login brand from store |
| `frontend/src/views/SettingsView.vue` | Brand settings tab |
| `frontend/src/i18n/locales/{zh-CN,en,zh-TW}.ts` | `settings.brand*` keys |

---

### Task 1: Backend BrandConfig + public/admin text API

**Files:**
- Create: `cmd/server/brand.go`
- Create: `cmd/server/brand_test.go`
- Modify: `cmd/server/config.go` (struct + Set merge preserve)
- Modify: `cmd/server/auth.go` (`isPublicPath`)
- Modify: `cmd/server/handlers.go` (routes)

**Interfaces:**
- Produces: `type BrandConfig struct { ProductName, Tagline, LogoURL string }` with JSON `product_name`, `tagline`, `logo_url`
- Produces: `(cs *ConfigStore) Brand() BrandConfig`, `SetBrand(c BrandConfig) error`
- Produces: handlers `handleGetBrand`, `handleGetAdminBrand`, `handleSetAdminBrand`
- Consumes: `cs.save()`, `writeJSON`, existing admin RBAC

- [ ] **Step 1: Write failing tests** in `cmd/server/brand_test.go`

```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestBrandPublicGetEmpty(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cs, err := NewConfigStore(cfgPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: cs, auth: newAuthManager(cs)}
	mux := http.NewServeMux()
	srv.registerRoutes(mux) // or wire only brand routes if registerRoutes is private — use handlers registration helper used by other tests
	// Prefer pattern from status_page / auth tests: construct Server and call handler directly:
	req := httptest.NewRequest(http.MethodGet, "/api/v1/brand", nil)
	rr := httptest.NewRecorder()
	srv.handleGetBrand(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out BrandConfig
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ProductName != "" || out.Tagline != "" || out.LogoURL != "" {
		t.Fatalf("expected empty brand, got %+v", out)
	}
}

func TestBrandAdminSetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewConfigStore(filepath.Join(dir, "config.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: cs}
	body := []byte(`{"product_name":"AcmeOps","tagline":"运维中台","logo_url":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/brand", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.handleSetAdminBrand(rr, req)
	if rr.Code != 200 {
		t.Fatalf("set status %d %s", rr.Code, rr.Body.String())
	}
	got := cs.Brand()
	if got.ProductName != "AcmeOps" || got.Tagline != "运维中台" {
		t.Fatalf("got %+v", got)
	}
}
```

Adapt to how other server tests construct `Server` / `NewConfigStore` (see `status_page` tests or `auth_test.go`). If `registerRoutes` is not exported, call handlers directly as above.

- [ ] **Step 2: Run tests — expect fail**

```bash
go test ./cmd/server/ -count=1 -run 'Brand' 
```

Expected: FAIL (undefined Brand / handlers).

- [ ] **Step 3: Implement config + handlers**

In `config.go` on `ServerConfig`:

```go
Brand BrandConfig `json:"brand,omitempty"`
```

In the general config merge (where `c.StatusPage = cs.cfg.StatusPage` lives ~1302):

```go
c.Brand = cs.cfg.Brand
```

Create `brand.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type BrandConfig struct {
	ProductName string `json:"product_name"`
	Tagline     string `json:"tagline"`
	LogoURL     string `json:"logo_url"`
}

func (cs *ConfigStore) Brand() BrandConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.cfg.Brand
}

func (cs *ConfigStore) SetBrand(c BrandConfig) error {
	c.ProductName = strings.TrimSpace(c.ProductName)
	c.Tagline = strings.TrimSpace(c.Tagline)
	c.LogoURL = strings.TrimSpace(c.LogoURL)
	cs.mu.Lock()
	cs.cfg.Brand = c
	cs.mu.Unlock()
	return cs.save()
}

func (s *Server) handleGetBrand(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Brand())
}

func (s *Server) handleGetAdminBrand(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Brand())
}

func (s *Server) handleSetAdminBrand(w http.ResponseWriter, r *http.Request) {
	var c BrandConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	// Preserve existing logo when client omits logo_url key by reading current first:
	cur := s.cfg.Brand()
	if strings.TrimSpace(c.LogoURL) == "" && r.URL.Query().Get("clear_logo") != "1" {
		// If JSON explicitly sent "logo_url":"" treat as clear only when clear_logo=1 OR
		// simpler: SetBrand replaces all three fields; Settings always sends current logo_url.
		_ = cur
	}
	if err := s.cfg.SetBrand(c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.Brand())
}
```

**Simplify SetBrand contract:** Settings always POSTs all three fields (`logo_url` current value or `""` to clear). No special query flag.

In `auth.go` `isPublicPath` switch add:

```go
"/api/v1/brand",
```

And after the switch, add:

```go
if strings.HasPrefix(p, "/api/v1/brand/logo/") {
	return true
}
```

In `handlers.go` near status-page routes:

```go
mux.HandleFunc("GET /api/v1/brand", s.handleGetBrand)
mux.HandleFunc("GET /api/v1/admin/brand", s.handleGetAdminBrand)
mux.HandleFunc("POST /api/v1/admin/brand", s.handleSetAdminBrand)
```

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./cmd/server/ -count=1 -run 'Brand'
```

Expected: PASS for Task 1 tests.

- [ ] **Step 5: Commit** — skip unless user asks.

---

### Task 2: Backend logo upload / delete / serve

**Files:**
- Modify: `cmd/server/brand.go`
- Modify: `cmd/server/brand_test.go`
- Modify: `cmd/server/handlers.go`

**Interfaces:**
- Consumes: `dashAssetExtForMIME`, `sniffDashAssetMIME`, `dashAssetMIMEForExt`, `dashAssetLogoMaxBytes` from `dashboard_assets.go`
- Produces: `handleUploadBrandLogo`, `handleDeleteBrandLogo`, `handleGetBrandLogo`
- Logo URL shape: `/api/v1/brand/logo/<filename>` where filename matches `^logo\.(png|jpg|jpeg|webp|svg)$`

- [ ] **Step 1: Add failing upload/serve tests**

```go
func TestBrandLogoUploadAndPublicGet(t *testing.T) {
	// minimal 1x1 PNG bytes
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
		0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
		0x00, 0x05, 0xfe, 0x02, 0xfe, 0xa7, 0x35, 0x81, 0x84, 0x00, 0x00, 0x00,
		0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	// multipart POST to handleUploadBrandLogo; expect 200 and logo_url set
	// then GET handleGetBrandLogo; expect 200 image/png
}

func TestBrandLogoRejectOversize(t *testing.T) {
	// body larger than dashAssetLogoMaxBytes → 400
}
```

Use real 1×1 PNG fixture if the snippet above fails sniff; copy from any existing dashboard asset test if present.

- [ ] **Step 2: Run — expect fail**

```bash
go test ./cmd/server/ -count=1 -run 'BrandLogo'
```

- [ ] **Step 3: Implement logo handlers in `brand.go`**

```go
func (s *Server) brandAssetsDir() string {
	if s == nil || s.cfg == nil || s.cfg.path == "" {
		return filepath.Join("data", "brand-assets")
	}
	return filepath.Join(filepath.Dir(s.cfg.path), "brand-assets")
}

// handleUploadBrandLogo: ParseMultipartForm(dashAssetLogoMaxBytes+64<<10)
// field name "file"; sniff MIME; write to brandAssetsDir()/logo.<ext>
// replace previous file; SetBrand with updated LogoURL
// handleDeleteBrandLogo: clear LogoURL, remove local file if URL matches /api/v1/brand/logo/
// handleGetBrandLogo: path value {name}; only allow logo.png|logo.jpg|logo.jpeg|logo.webp|logo.svg
```

Register:

```go
mux.HandleFunc("POST /api/v1/admin/brand/logo", s.handleUploadBrandLogo)
mux.HandleFunc("DELETE /api/v1/admin/brand/logo", s.handleDeleteBrandLogo)
mux.HandleFunc("GET /api/v1/brand/logo/{name}", s.handleGetBrandLogo)
```

Mirror security checks from `handleUploadDashboardAsset` / `handleGetDashboardAsset` (path traversal, size, MIME).

- [ ] **Step 4: Run tests — expect pass**

```bash
go test ./cmd/server/ -count=1 -run 'Brand'
```

- [ ] **Step 5: Commit** — skip unless user asks.

---

### Task 3: Frontend API + brand store + boot fetch

**Files:**
- Modify: `frontend/src/api/modules.ts`
- Create: `frontend/src/stores/brand.ts`
- Modify: `frontend/src/main.ts`

**Interfaces:**
- Produces:

```ts
export type BrandConfig = {
  product_name?: string;
  tagline?: string;
  logo_url?: string;
};

export const brandApi = {
  getPublic: () => api<BrandConfig>("/brand", { skipAuthRedirect: true }),
  getAdmin: () => api<BrandConfig>("/admin/brand"),
  save: (body: BrandConfig) =>
    api<BrandConfig>("/admin/brand", { method: "POST", body: JSON.stringify(body) }),
  uploadLogo: (body: FormData) =>
    api<BrandConfig>("/admin/brand/logo", { method: "POST", body: body as BodyInit }),
  clearLogo: () => api<BrandConfig>("/admin/brand/logo", { method: "DELETE" }),
};
```

- Produces store `useBrandStore` with `productName`, `tagline`, `logoUrl` computeds (i18n fallback), `fetchBrand()`, `apply(cfg)`.

- [ ] **Step 1: Add `brandApi` next to `statusPageApi` in `modules.ts`**

- [ ] **Step 2: Create `stores/brand.ts`**

```ts
import { defineStore } from "pinia";
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { brandApi, type BrandConfig } from "@/api/modules";

export const useBrandStore = defineStore("brand", () => {
  const raw = ref<BrandConfig>({});
  const loaded = ref(false);

  // Note: useI18n() only works in setup; prefer i18n.global.t like router does:
  // import { i18n } from "@/i18n";
  const productName = computed(() => {
    const v = String(raw.value.product_name || "").trim();
    return v || String((i18n.global as any).t("app.name"));
  });
  const tagline = computed(() => {
    const v = String(raw.value.tagline || "").trim();
    return v || String((i18n.global as any).t("layout.productTagline"));
  });
  const logoUrl = computed(() => String(raw.value.logo_url || "").trim());

  async function fetchBrand() {
    try {
      raw.value = (await brandApi.getPublic()) || {};
    } catch (e) {
      console.warn("[brand] fetch", e);
    } finally {
      loaded.value = true;
    }
  }

  function apply(cfg: BrandConfig) {
    raw.value = cfg || {};
  }

  return { raw, loaded, productName, tagline, logoUrl, fetchBrand, apply };
});
```

- [ ] **Step 3: In `main.ts` after pinia, before mount:**

```ts
import { useBrandStore } from "./stores/brand";
// inside localeReady.finally:
useBrandStore(pinia).fetchBrand();
app.mount("#app");
```

Call `fetchBrand` without awaiting so first paint is not blocked; Login/AppLayout use fallbacks until loaded.

- [ ] **Step 4: Typecheck**

```bash
cd frontend && npm run check
```

Expected: PASS (or only pre-existing errors unrelated to brand).

- [ ] **Step 5: Commit** — skip unless user asks.

---

### Task 4: Wire Login, AppLayout, document title

**Files:**
- Modify: `frontend/src/views/LoginView.vue`
- Modify: `frontend/src/layouts/AppLayout.vue`
- Modify: `frontend/src/router/index.ts`
- Modify: `frontend/src/components/LangSwitch.vue`

**Interfaces:**
- Consumes: `useBrandStore().productName | tagline | logoUrl`

- [ ] **Step 1: Shared title helper** (inline in router + LangSwitch to avoid new file unless preferred):

```ts
function documentTitleForPage(page: string, productName: string): string {
  const brand = productName || "AIOps";
  return page === brand ? page : `${page} · ${brand}`;
}
```

In `router.afterEach`:

```ts
import { useBrandStore } from "@/stores/brand";
// ...
const brand = useBrandStore();
const page = meta.titleKey ? String((i18n.global as any).t(meta.titleKey)) : brand.productName;
document.title = documentTitleForPage(page, brand.productName);
```

Same in `LangSwitch.vue` after locale change.

- [ ] **Step 2: AppLayout brand button**

Replace letter-only mark:

```vue
<span v-if="brand.logoUrl" class="brand-mark brand-mark--img" aria-hidden="true">
  <img :src="brand.logoUrl" alt="" />
</span>
<span v-else class="brand-mark" aria-hidden="true">A</span>
<strong>{{ brand.productName }}</strong>
<span class="brand-product">{{ brand.tagline }}</span>
```

Add CSS so `.brand-mark--img img` is ~22–28px, `object-fit: contain`.

- [ ] **Step 3: LoginView** — same pattern for `.story-brand` / `.mobile-brand` / `.story-mark`.

- [ ] **Step 4: `npm run check`**

- [ ] **Step 5: Commit** — skip unless user asks.

---

### Task 5: Settings Brand tab + i18n

**Files:**
- Modify: `frontend/src/views/SettingsView.vue`
- Modify: `frontend/src/i18n/locales/zh-CN.ts`
- Modify: `frontend/src/i18n/locales/en.ts`
- Modify: `frontend/src/i18n/locales/zh-TW.ts`

**Interfaces:**
- Consumes: `brandApi.getAdmin|save|uploadLogo|clearLogo`, `useBrandStore().apply|fetchBrand`
- Admin-only tab like status page (`v-if="auth.isAdmin"`)

- [ ] **Step 1: Add i18n keys** under `settings`:

```ts
brand: "品牌",
brandProductName: "产品名称",
brandTagline: "副标题",
brandLogo: "Logo",
brandUploadLogo: "上传 Logo",
brandClearLogo: "清除 Logo",
brandHint: "用于登录页、顶栏与浏览器标签标题。留空则使用默认文案。",
brandLogoTooBig: "Logo 不能超过 512KB",
brandSaved: "品牌设置已保存",
```

Mirror EN / zh-TW.

- [ ] **Step 2: Add tab before status page** in `SettingsView.vue`

```vue
<el-tab-pane v-if="auth.isAdmin" :label="t('settings.brand')" name="brand">
  <el-card shadow="never" class="surface">
    <el-form label-width="160px" style="max-width: 640px">
      <p class="muted">{{ t('settings.brandHint') }}</p>
      <el-form-item :label="t('settings.brandProductName')">
        <el-input v-model="brandForm.product_name" maxlength="64" show-word-limit />
      </el-form-item>
      <el-form-item :label="t('settings.brandTagline')">
        <el-input v-model="brandForm.tagline" maxlength="128" show-word-limit />
      </el-form-item>
      <el-form-item :label="t('settings.brandLogo')">
        <img v-if="brandForm.logo_url" class="brand-thumb" :src="brandForm.logo_url" alt="" />
        <el-button size="small" @click="brandLogoRef?.click()">{{ t('settings.brandUploadLogo') }}</el-button>
        <el-button size="small" :disabled="!brandForm.logo_url" @click="clearBrandLogo">{{ t('settings.brandClearLogo') }}</el-button>
        <input ref="brandLogoRef" type="file" accept="image/png,image/jpeg,image/webp,image/svg+xml" hidden @change="onBrandLogoFile" />
      </el-form-item>
      <el-form-item>
        <el-button type="primary" :loading="brandSaving" @click="saveBrand">{{ t('common.save') }}</el-button>
      </el-form-item>
    </el-form>
  </el-card>
</el-tab-pane>
```

Script: load via `useQuery(['brand-admin'], brandApi.getAdmin)` or `onMounted` when admin; `saveBrand` POSTs text fields then `useBrandStore().apply` / `fetchBrand`; upload calls `brandApi.uploadLogo` then updates form `logo_url`.

- [ ] **Step 3: `npm run check`**

- [ ] **Step 4: Manual verify** (docker or `npm run dev` + server):

1. Admin → Settings → Brand → set name/tagline, upload logo, save  
2. Logout → login page shows brand  
3. Header + `document.title` use product name  
4. Clear logo/name → i18n + letter A return  

- [ ] **Step 5: Commit** — skip unless user asks.

---

## Spec coverage checklist

| Spec item | Task |
|-----------|------|
| BrandConfig on ServerConfig | 1 |
| Public GET /api/v1/brand | 1 |
| Admin GET/POST brand | 1 |
| Logo upload/delete/serve | 2 |
| Preserve brand on general config Set | 1 |
| Pinia store + boot fetch | 3 |
| Login + AppLayout | 4 |
| Document title + LangSwitch | 4 |
| Settings UI + i18n | 5 |
| Classic/favicon/PWA out of scope | — (intentionally omitted) |

## Placeholder / consistency review

- JSON field names fixed: `product_name`, `tagline`, `logo_url`
- Logo public path prefix: `/api/v1/brand/logo/`
- Admin paths: `/api/v1/admin/brand`, `/logo` suffix for upload/delete
- SetBrand always receives full triple from Settings (no ambiguous omit)
