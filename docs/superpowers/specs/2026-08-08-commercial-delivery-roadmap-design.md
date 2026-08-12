# Commercial delivery roadmap — Design

Date: 2026-08-08  
Status: approved (plan: Commercial UX Waves)  
Scope: Classic vs Vue gap inventory + phased delivery for commercial standard

## Delivery waves

| Wave | Focus | Status |
|------|--------|--------|
| 0 | This gap inventory | done (doc) |
| 1 | Global time picker + Hosts history UX | done |
| 2 | Agent install ↔ host folder tree | done |
| 3 | Gateway relay + multi-server push | done |
| 4 | AI / SRE / ops closed-loop parity | done (P0 + P1 quick wins) |

---

## 1. Global UI / time / charts

| Item | Classic | Vue | Gap | Pri | Acceptance |
|------|---------|-----|-----|-----|------------|
| Time range control | `renderChartControls` + `CHART_SPANS` in `hosts.js` / shared across SNMP/ApiMon/SLO | Inline radios/selects per view; no shared component | Inconsistent height/style; presets differ | P0 | Shared `TimeRangePicker` + `timeRange.ts`; Hosts/Resources/Network/ApiMon use it |
| Range anchoring | `resolveDetailWindow` / `resolveAnchoredRange` | Recomputes `now` every refresh | Window drifts; looks like wrong span | P0 | Same host+preset keeps anchored `[from,to]` until preset/host changes |
| Host history toolbar | Unified `chart-controls` chips | Radio + separate refresh + forecast | Visual mismatch (screenshot) | P0 | Single `.hist-toolbar` visual group |
| Multi-chart dataZoom | Canvas charts; no per-chart slider clutter | `charts.ts` always adds slider per chart | Four sliders; sparse data looks “zoomed” | P0 | Drawer charts: inside-only; zoom dialog may keep slider |
| Forecast UX | Shared chip + scope | Switch + model select | Mostly OK; polish with toolbar | P1 | Forecast on/off + model in same toolbar |
| Checks/Dashboard/SRE long presets | Hour chips incl. 72/168/336 | Separate lists | Defer migrate but API must accept custom presets | P2 | `TimeRangePicker` presets prop supports hour lists |

---

## 2. Agent install / host tree / relay / multi-server

| Item | Classic | Vue | Gap | Pri | Acceptance |
|------|---------|-----|-----|-----|------------|
| Install “一级分组” | Free text → agent `category` | Same free text in `InstallAgentDialog` | Does not pick existing folder tree; nested paths impossible | P0 | Folder picker (L1 find-or-create); optional new L1 name |
| Category → folder migration | `ensureHostFoldersMigrated` L1 only | Same API | Ungrouped bounce-back when agent still has category | P0 | Explicit ungrouped sticks; no auto re-file |
| Dual editors on host detail | Folder + category | Folder select + category text | Category creates wrong L1 vs nested leaf | P1 | Category save maps to folder API; invalidate `host-folders` |
| Type tree mode | OS/platform tree | Same | Install category never applies (by design) | — | Copy clarifies asset-tree only |
| Relay install cmd | Classic may omit token/category on gateway line | Vue includes query params | Operator confusion | P1 | Aligned help text + params |
| Multi-server | `servers_json` fan-out | Mode in Install dialog | Per-panel host_id; weak failure UX | P1 | Clear mutual exclusion + status/errors |

---

## 3. AI closed loop

| Item | Classic | Vue | Gap | Pri | Acceptance |
|------|---------|-----|-----|-----|------------|
| Hermes chat + dock | Classic assist | `HermesChat` + dock | Briefing dual-mount consume race (known) | P0 | Only intended consumer `consume()` |
| Host/container AI analyze | Assist buttons | `openAIAnalysis` paths | Verify apply/feedback parity | P1 | Analyze → result → optional apply |
| Dashboard AI generate/optimize | Classic dash AI | `DashboardEditorView` AI | Timeout/provider thinking flags (product rule) | P0 | Generate/optimize succeed on gateway models |
| Metrics forecast | Chart forecast chip | Hosts/Resources forecast | Shared models OK | P1 | Toggle works; empty-data hint |

---

## 4. SRE / ops tools

| Item | Classic | Vue | Gap | Pri | Acceptance |
|------|---------|-----|-----|-----|------------|
| Automation playbooks | Classic automation | `AutomationView` | AI generate/preflight/retro | P1 | Full loop on Vue |
| Terminal + auth | Classic term | Vue terminal + dock | Protocol/password gate | P1 | Auth before connect |
| Remote desktop | Classic desktop.js | `RemoteDesktopView` | Codec negotiation largely done | P2 | H.264 path stable; H.265 optional |
| Checks / ApiMon history | Shared chart controls | Own range UIs | Migrate to TimeRangePicker (Wave 1 partial / later) | P1 | Same presets semantics |
| SRE trends / SLO | `sre.js` controls | `SreView` | Longer range list | P2 | Preset parity |

---

## Non-goals (this roadmap doc)

- Pixel-perfect Element Plus clone of classic CSS
- Volume-based container taxonomy
- Rewriting classic canvas chart engine in Wave 1

## Next implementation

Wave 1 code lands immediately after this doc; Wave 2 follows Hosts time UX.
