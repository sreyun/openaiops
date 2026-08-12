# Chart Value Units Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every Vue console numeric chart/panel show classic-parity units (axis, tooltip, KPI, table) via one shared `formatChartValue`.

**Architecture:** Add `frontend/src/shared/chart-units.ts` with `formatChartValue` / `normalizeChartUnit`. Thread `unit` through `TimeseriesStyle` and all `build*` helpers in `charts.ts`. Wire HostHistoryPanel, AiChatChart, DashboardEditorView, and other views that already call builders.

**Tech Stack:** Vue 3 + ECharts (`frontend/src/shared/charts.ts`), classic parity from `cmd/server/web/js/dashboard.js` `fmtUnit` / `core.js` `fmtRate`.

**Spec:** `docs/superpowers/specs/2026-08-08-chart-units-design.md`

## Global Constraints

- Vue `/v2` only; do not change classic `cmd/server/web` formatters in this plan.
- Do not change backend sample semantics (raw B/s, percent 0–100, etc.).
- Unknown / empty unit → `short` compact number (v1: no free-text suffix).
- Aliases: `bps` → `binBps`, `ops` → `iops`, `Bps` → `binBps`.
- Logs / alerts / text panels: out of scope.
- Do not commit unless the user explicitly asks (skip Commit steps).
- Do not add agent `Co-authored-by` trailers if committing later.

## File map

| File | Responsibility |
|------|----------------|
| `frontend/src/shared/chart-units.ts` (create) | `normalizeChartUnit`, `formatChartValue`, `formatChartRate`, `inferMetricUnit` |
| `frontend/scripts/check-chart-units.mjs` (create) | Node assert suite for formatter (no vitest in repo) |
| `frontend/package.json` | Optional: wire check into `npm run check` or run standalone in task |
| `frontend/src/shared/charts.ts` | Style.unit + formatters on all builders; percent default 0–100 |
| `frontend/src/components/HostHistoryPanel.vue` | Per-chart `unit` in style |
| `frontend/src/components/AiChatChart.vue` | Infer / pass unit |
| `frontend/src/shared/ai-chat-actions.ts` | Pass unit when building options |
| `frontend/src/views/DashboardEditorView.vue` | Pass `p.unit` into builders; table format; unit dropdown |
| `frontend/src/views/ChecksView.vue` | latency `ms`, loss `percent` |
| `frontend/src/views/ResourcesView.vue` | Infer unit from metric name |
| `frontend/src/i18n/locales/{zh-CN,en,zh-TW}.ts` | Dashboard unit option labels |

---

### Task 1: `formatChartValue` + assert script

**Files:**
- Create: `frontend/src/shared/chart-units.ts`
- Create: `frontend/scripts/check-chart-units.mjs`

**Interfaces:**
- Produces:
  - `normalizeChartUnit(unit?: string): string`
  - `formatChartValue(value: unknown, unit?: string, decimals?: number | null): string`
  - `inferMetricUnit(metricKey: string): string` — `*_percent`→`percent`, `*_iops`→`iops`, `*_rate` / `*_bps`→`binBps`, `latency_ms`/`*_ms`→`ms`, `load*`→`load`, else `short`

- [ ] **Step 1: Implement `chart-units.ts`**

```ts
import { formatBytes } from "@/shared/format";

export function normalizeChartUnit(unit?: string): string {
  const u = String(unit || "").trim();
  if (u === "bps" || u === "Bps") return "binBps";
  if (u === "ops") return "iops";
  if (u === "seconds") return "s";
  return u;
}

function fmtShort(n: number, decimals?: number | null): string {
  if (decimals != null && Number.isFinite(decimals)) return n.toFixed(Math.max(0, Math.min(10, decimals)));
  const a = Math.abs(n);
  if (a >= 1e9) return (n / 1e9).toFixed(2) + "G";
  if (a >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (a >= 1e3) return (n / 1e3).toFixed(1) + "k";
  if (Number.isInteger(n)) return String(n);
  return n.toFixed(2);
}

/** Classic-aligned binBps / disk IO rate (bytes per second). */
export function formatChartRate(bytesPerSec: number): string {
  const b = Number(bytesPerSec);
  if (!Number.isFinite(b) || b < 0) return "-";
  if (b < 1024) return `${b.toFixed(0)} B/s`;
  if (b < 1048576) return `${(b / 1024).toFixed(1)} KB/s`;
  if (b < 1073741824) return `${(b / 1048576).toFixed(2)} MB/s`;
  return `${(b / 1073741824).toFixed(2)} GB/s`;
}

export function formatChartValue(value: unknown, unit?: string, decimals?: number | null): string {
  if (value === undefined || value === null || value === "") return "-";
  const n = Number(value);
  if (!Number.isFinite(n)) return String(value);
  const u = normalizeChartUnit(unit);
  const d = decimals != null && Number.isFinite(decimals) ? Math.max(0, Math.min(10, Math.floor(Number(decimals)))) : null;
  const fixed = (x: number, def: number) => (d != null ? x.toFixed(d) : x.toFixed(def));
  switch (u) {
    case "none":
      return d != null ? n.toFixed(d) : String(n);
    case "percent":
      return `${fixed(n, 1)}%`;
    case "percentunit":
      return `${fixed(n * 100, 1)}%`;
    case "bytes":
      return formatBytes(n);
    case "binBps":
      return formatChartRate(n);
    case "iops": {
      if (n < 1000) return d != null ? n.toFixed(d) : n.toFixed(0);
      if (n < 10000) return `${(n / 1000).toFixed(1)}k`;
      return `${(n / 1000).toFixed(0)}k`;
    }
    case "ms":
      return n >= 1000 ? formatChartValue(n / 1000, "s", d) : `${fixed(n, 0)}ms`;
    case "s":
    case "duration": {
      // compact duration; keep simple for charts
      if (n < 60) return `${fixed(n, n < 10 ? 2 : 0)}s`;
      if (n < 3600) return `${Math.floor(n / 60)}m ${Math.round(n % 60)}s`;
      return `${Math.floor(n / 3600)}h ${Math.round((n % 3600) / 60)}m`;
    }
    case "reqps":
      return `${d != null ? n.toFixed(d) : fmtShort(n)}/s`;
    case "cores":
      return `${fixed(n, 2)} cores`;
    case "load":
      return fixed(n, 2);
    case "short":
    case "":
      return fmtShort(n, d);
    default:
      return fmtShort(n, d);
  }
}

export function inferMetricUnit(metricKey: string): string {
  const k = String(metricKey || "").toLowerCase();
  if (!k) return "short";
  if (k.includes("percent") || k.endsWith("_pct") || k === "loss_pct") return "percent";
  if (k.includes("iops")) return "iops";
  if (k.includes("latency_ms") || k.endsWith("_ms") || k === "latency") return "ms";
  if (k.includes("rate") || k.endsWith("_bps") || k.includes("bytes_per")) return "binBps";
  if (k.startsWith("load") || k === "load1" || k === "load5" || k === "load15") return "load";
  if (k.includes("proc") || k.includes("count") || k.includes("conn")) return "short";
  return "short";
}
```

- [ ] **Step 2: Add `scripts/check-chart-units.mjs`**

Dynamic-import is awkward with `@/` paths — duplicate minimal asserts by spawning `npx tsx` **or** implement the checker as pure JS mirroring the TS (prefer: export logic testable via Vite-node). Simplest repo-fit approach:

```js
// frontend/scripts/check-chart-units.mjs
// Inline mirror of critical cases; also typecheck covers chart-units.ts.
// Prefer importing compiled logic: use node --experimental-strip-types if Node>=22.

import assert from "node:assert/strict";
import { formatChartValue, normalizeChartUnit, inferMetricUnit } from "../src/shared/chart-units.ts";

assert.equal(normalizeChartUnit("bps"), "binBps");
assert.equal(formatChartValue(62.34, "percent"), "62.3%");
assert.equal(formatChartValue(0.623, "percentunit"), "62.3%");
assert.match(formatChartValue(1200000, "binBps"), /MB\/s/);
assert.equal(formatChartValue(1200, "iops"), "1.2k");
assert.equal(inferMetricUnit("cpu_percent"), "percent");
assert.equal(inferMetricUnit("net_recv_rate"), "binBps");
console.log("chart-units check passed");
```

If Node strip-types fails on `formatBytes` import path alias `@/`, change `chart-units.ts` to relative import:

```ts
import { formatBytes } from "./format";
```

- [ ] **Step 3: Run checker**

```bash
cd frontend && node --experimental-strip-types ./scripts/check-chart-units.mjs
```

Expected: `chart-units check passed`

- [ ] **Step 4: Skip commit** (user rule)

---

### Task 2: Wire all builders in `charts.ts`

**Files:**
- Modify: `frontend/src/shared/charts.ts`

**Interfaces:**
- Consumes: `formatChartValue` from `./chart-units`
- Produces: `TimeseriesStyle.unit?: string`; builders honor `style.unit` / explicit unit args

- [ ] **Step 1: Extend style + timeseries**

```ts
import { formatChartValue, normalizeChartUnit } from "./chart-units";

export type TimeseriesStyle = {
  // ...existing...
  unit?: string;
};

function applyPercentScale(yAxis: Record<string, unknown>, unit?: string, style?: TimeseriesStyle) {
  const u = normalizeChartUnit(unit || style?.unit);
  if (u === "percent" && style?.min == null && style?.max == null) {
    yAxis.min = 0;
    yAxis.max = 100;
  }
  if (u === "percentunit" && style?.min == null && style?.max == null) {
    yAxis.min = 0;
    yAxis.max = 1;
  }
}

// In buildTimeseriesOption:
const unit = style?.unit;
const yAxis: Record<string, unknown> = {
  type: "value",
  splitLine: { lineStyle: { type: "dashed" } },
  axisLabel: {
    formatter: (v: number) => formatChartValue(v, unit, style?.decimals),
  },
};
applyPercentScale(yAxis, unit, style);
// ...
tooltip: {
  trigger: "axis",
  valueFormatter: (v: unknown) => formatChartValue(v, unit, style?.decimals),
},
```

Remove/replace private `formatTooltipValue` usages with `formatChartValue` (keep decimals-only path via unit `short`/`none`).

- [ ] **Step 2: Gauge / stat / bargauge / pie / bar / histogram / heatmap / candlestick / radar / sankey**

Patterns:

```ts
// gauge — add optional unit param at end for back-compat:
export function buildGaugeOption(
  value: number, max = 100, label = "", min = 0, color?: string, unit = "percent",
): ECOption {
  // detail.formatter: (v: number) => formatChartValue(v, unit)
}

// stat — unit arg is chart unit id, not display suffix:
export function buildStatOption(value: number, unit = "", label = "", color?: string, decimals?: number): ECOption {
  const text = formatChartValue(value, unit || "short", decimals);
  // ...
}

// bargauge:
valueFormatter: (v) => formatChartValue(v, unit)
label formatter on series: formatChartValue

// pie/bar/etc: accept optional unit as 3rd arg or opts object:
export function buildPieOption(items, title?, unit = "short")
export function buildBarchartOption(items, title?, unit = "short")
```

Update `seriesToTableRows` to optional unit:

```ts
export function seriesToTableRows(series: QuerySeries[], unit?: string): TableRow[] {
  // value display: typeof number → formatChartValue(n, unit)
}
```

`buildTimeseriesWithForecast` already spreads style into `buildTimeseriesOption` — unit flows automatically.

- [ ] **Step 3: Typecheck**

```bash
cd frontend && npm run typecheck
```

Expected: pass (fix call sites if arity breaks — Task 3–5).

- [ ] **Step 4: Skip commit**

---

### Task 3: HostHistoryPanel + AiChatChart

**Files:**
- Modify: `frontend/src/components/HostHistoryPanel.vue`
- Modify: `frontend/src/components/AiChatChart.vue`
- Modify: `frontend/src/shared/ai-chat-actions.ts` (if it builds options)

**Interfaces:**
- Consumes: `TimeseriesStyle.unit`, `inferMetricUnit`

- [ ] **Step 1: HostHistoryPanel — per chart style**

```ts
const pctStyle = { ...histStyle, unit: "percent" as const };
const loadStyle = { ...histStyle, unit: "load" as const, min: 0 };
const rateStyle = { ...histStyle, unit: "binBps" as const };
const iopsStyle = { ...histStyle, unit: "iops" as const };
const shortStyle = { ...histStyle, unit: "short" as const };

next.combo = buildTimeseriesWithForecast(..., pctStyle);
next.cpu = buildTimeseriesWithForecast(..., pctStyle);
next.mem = buildTimeseriesWithForecast(..., pctStyle);
next.disk = buildTimeseriesWithForecast(..., pctStyle);
next.load = buildTimeseriesWithForecast(..., loadStyle);
next.net = buildTimeseriesWithForecast(..., rateStyle);
next.diskio = buildTimeseriesWithForecast(..., rateStyle);
next.iops = buildTimeseriesWithForecast(..., iopsStyle);
next.proc = buildTimeseriesWithForecast(..., shortStyle);
next.gpu = buildTimeseriesWithForecast(..., pctStyle);
```

Combo mixes percent series only (cpu/mem/disk) — `percent` is correct.

- [ ] **Step 2: AiChatChart**

When building style, set:

```ts
unit: inferMetricUnit(primaryMetricKey) // from series labels.__name__ or source
```

If multiple conflicting metrics, prefer first series key; fallback `short`.

- [ ] **Step 3: Skip commit**

---

### Task 4: Dashboard editor / viewer

**Files:**
- Modify: `frontend/src/views/DashboardEditorView.vue`
- Modify: `frontend/src/i18n/locales/zh-CN.ts`, `en.ts`, `zh-TW.ts`

**Interfaces:**
- Consumes: `formatChartValue`, builders with unit

- [ ] **Step 1: Pass `p.unit` into every builder**

Where style is built for timeseries:

```ts
const style = { ...existing, unit: p.unit || "short", decimals: p.decimals };
```

Gauge:

```ts
buildGaugeOption(val, gMax, seriesLabel, gMin, tone, p.unit || "percent")
```

Stat:

```ts
buildStatOption(val, p.unit || "short", seriesLabel, tone, p.decimals)
```

Bargauge / pie / bar:

```ts
buildBargaugeOption(items, p.unit || "short", colorFor)
buildPieOption(items, p.title, p.unit || "short")
buildBarchartOption(items, p.title, p.unit || "short")
```

Table rows:

```ts
seriesToTableRows(series, p.unit)
```

- [ ] **Step 2: Expand unit `<el-option>` list** with i18n labels

Keys (example zh-CN):

```ts
unitPercent: "百分比 percent",
unitPercentUnit: "比例 percentunit (0–1)",
unitShort: "短数字 short",
unitBytes: "字节 bytes",
unitBinBps: "吞吐 binBps",
unitBps: "吞吐 bps (别名)",
unitIops: "IOPS",
unitOps: "ops (别名)",
unitMs: "毫秒 ms",
unitS: "秒 s",
unitReqps: "请求/秒 reqps",
unitCores: "核 cores",
unitLoad: "负载 load",
unitNone: "无格式 none",
```

Options: `percent`, `percentunit`, `short`, `bytes`, `binBps`, `bps`, `Bps`, `iops`, `ops`, `ms`, `s`, `reqps`, `cores`, `load`, `none`.

- [ ] **Step 3: `npm run check:i18n` + typecheck**

- [ ] **Step 4: Skip commit**

---

### Task 5: Sweep Checks / Resources (+ related)

**Files:**
- Modify: `frontend/src/views/ChecksView.vue`
- Modify: `frontend/src/views/ResourcesView.vue`
- Grep and fix any remaining `buildTimeseriesOption(` / `buildTimeseriesWithForecast(` without unit if latency/rate charts

- [ ] **Step 1: ChecksView**

Latency chart style: `{ unit: "ms", min: 0, ... }`  
Loss chart: `{ unit: "percent", min: 0, max: 100, ... }`

- [ ] **Step 2: ResourcesView**

```ts
buildTimeseriesOption([history], metric.value, {
  unit: inferMetricUnit(metric.value),
});
```

Same for forecast path.

- [ ] **Step 3: Grep leftover**

```bash
rg "buildTimeseries(Option|WithForecast)\\(" frontend/src -n
```

Ensure each call passes style.unit or inherits safe default inside builder (`short`).

- [ ] **Step 4: Skip commit**

---

### Task 6: Verify

**Files:** none (commands)

- [ ] **Step 1: Unit script + full check**

```bash
cd frontend && node --experimental-strip-types ./scripts/check-chart-units.mjs
cd frontend && npm run check
```

Expected: all pass.

- [ ] **Step 2: Rebuild frontend image (docker QA)**

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build aiops-frontend
```

- [ ] **Step 3: Manual / Playwright smoke**

- Open Overview TOP → host history: mem axis shows `%`; net axis/tooltip shows `KB/s` or `MB/s` not `1200000`.
- Hosts drawer: same.
- Optional: dashboard panel with unit percent shows `%` on gauge/stat.

- [ ] **Step 4: Skip commit**

---

## Spec coverage checklist

| Spec item | Task |
|-----------|------|
| `formatChartValue` + classic enum/aliases | 1 |
| All builders format axis/tooltip/labels | 2 |
| HostHistoryPanel units | 3 |
| AiChatChart infer | 3 |
| Dashboard pass unit + dropdown | 4 |
| Table cells | 4 |
| Checks / Resources sweep | 5 |
| Tests + check + docker | 1, 6 |
| No backend / classic web changes | Global |
| Logs/alerts untouched | Global |

## Placeholder / consistency self-review

- No TBD steps; signatures use `formatChartValue` / `inferMetricUnit` consistently.
- Stat `unit` meaning changes from “suffix string” to “unit id” — Task 4 must stop passing `"%"` literal and pass `p.unit` instead (critical).
- Relative import `./format` in chart-units avoids path-alias issues in Node strip-types.
