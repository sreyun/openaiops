# Chart / panel value units — design

**Date:** 2026-08-08  
**Status:** Approved; implementation plan ready  
**Scope:** Vue `/v2` console — all numeric chart builders and dashboard value surfaces

## Problem

Host history and other Vue charts show raw axis/tooltip numbers without units (e.g. memory `60` with no `%`, network `1,200,000` instead of `1.2 MB/s`). Classic UI already formats via `fmtUnit` / `fmtRate` / `fmtIORate` / `fmtIOPS`. Vue `buildTimeseries*` and sibling builders largely ignore panel `unit`, so readability and classic parity suffer across timeseries, gauge, stat, bar, pie, etc.

## Goals

1. One shared formatter for all numeric chart/panel displays (axis, tooltip, gauge detail, bar labels, KPI text, table cells).
2. Classic-compatible unit enum (+ host-metric aliases).
3. Call sites pass explicit `unit` (no fragile auto-inference of mixed-series charts).
4. Percent charts default to `0–100` when min/max unset.
5. Forecast dashed series use the same formatter as history.

## Non-goals

- Changing backend sample semantics (values remain raw B/s, percent 0–100, etc.).
- Visual theme / palette redesign.
- Reworking logs / alerts / free-text panels (no continuous numeric axis).

## Approach

**Shared `formatChartValue(value, unit?, decimals?)`** (prefer `frontend/src/shared/chart-units.ts`, re-exported or used from `charts.ts` / `format.ts`).

Align with classic `dashboard.js` `fmtUnit` and host helpers:

| unit | Display rule |
|------|----------------|
| `percent` | `n` + `%` (1 decimal default) |
| `percentunit` | `n * 100` + `%` |
| `bytes` | IEC-ish bytes (`formatBytes`) |
| `binBps` / `Bps` / `bps` | bytes/s → B/s, KB/s, MB/s, GB/s (classic `fmtRate`/`fmtIORate` style) |
| `iops` / `ops` | compact ops (`1.2k`) |
| `ms` / `s` / `seconds` / `duration` | duration-aware |
| `reqps` | short + `/s` |
| `cores` | fixed + ` cores` |
| `short` / empty / unknown | compact short number |
| `load` | 2-decimal load, no `%` |
| `none` | raw / decimals only |

Aliases: dashboard editor today offers `bps` / `ops` — map to `binBps` / `iops` behavior.

## Chart builder integration (`frontend/src/shared/charts.ts`)

Extend `TimeseriesStyle` (and parallel opts where needed) with:

```ts
unit?: string;
decimals?: number; // already present on TimeseriesStyle
```

Every builder applies `formatChartValue`:

| Builder | Where |
|---------|--------|
| `buildTimeseriesOption` / `WithForecast` | `yAxis.axisLabel.formatter`, tooltip `valueFormatter` |
| `buildGaugeOption` | `detail.formatter`, axis tick labels |
| `buildStatOption` | main text (replace naive string concat) |
| `buildBargaugeOption` | label / tooltip |
| `buildPieOption` | tooltip / optional label |
| `buildBarchartOption` | value axis + tooltip |
| `buildHistogramOption` | same |
| `buildHeatmapOption` | visualMap / tooltip |
| `buildCandlestickOption` | tooltip |
| `buildRadarOption` | indicator / tooltip |
| `buildSankeyOption` | edge/node value tooltip |
| `buildStateTimelineRows` | already unit-aware for % tones; format displayed values |

API shape preference: pass `unit` via style/options object rather than adding many positional args. Where builders currently take a bare `unit` string (stat), keep signature but route through `formatChartValue`.

Percent: if `unit` is `percent` | `percentunit` and style min/max unset → `min: 0`, `max: 100` (percentunit axis shows 0–1 or format as % consistently — prefer format as `%` with max 1 on raw axis OR scale display only; **decision:** keep data as stored, format labels as `%` via `percentunit` math; for host metrics use `percent` with data already 0–100).

## Call-site wiring

### Host history (`HostHistoryPanel.vue`)

| Chart | unit |
|-------|------|
| combo / cpu / mem / disk / gpu | `percent` |
| load | `load` |
| net / diskio | `binBps` |
| iops | `iops` |
| proc | `short` |

### AI chat charts (`AiChatChart.vue`)

Infer from series metric keys when possible (`*_percent` → percent, `*_rate` → binBps, `*_iops` → iops); else panel/source unit or `short`.

### Dashboard (`DashboardEditorView.vue` / viewer)

Pass `p.unit` into all chart builders (not only `%` suffix for gauge/stat). Expand unit select options to match classic: `binBps`, `Bps`, `bytes`, `ms`, `s`, `reqps`, `cores`, `iops`, keep aliases `bps`/`ops`. Table numeric cells use `formatChartValue`.

### Other views (same PR or immediate follow-up in same plan)

Checks latency history, Resources hardware history, ApiMon latency, Network SNMP rates — attach correct unit at build call.

## Dashboard editor UX

- Unit dropdown labels: human-readable + id (e.g. `吞吐 (binBps)`), i18n for zh-CN / zh-TW / en.
- Allow-create retained for custom suffixes that fall through to `short` + raw suffix only if we add `suffix` later; **v1:** unknown units → `short` only (documented).

## Testing

- Unit tests for `formatChartValue` (percent, binBps tiers, iops, aliases, NaN).
- Smoke: HostHistoryPanel net chart tooltip/axis not showing raw millions; dashboard percent gauge shows `%`.
- `npm run check` (typecheck, i18n, build).

## Rollout

1. Add `chart-units.ts` + tests.  
2. Wire `charts.ts` builders.  
3. HostHistoryPanel + AiChatChart.  
4. Dashboard editor/viewer + unit dropdown.  
5. Sweep other views.  
6. Rebuild `aiops-frontend` for docker QA.

## Success criteria

- Host history Y-axis / tooltip show `%` or `MB/s` / `IOPS` as appropriate; no bare `1200000` for net.
- Dashboard panels with `unit=percent|binBps|…` format consistently across timeseries, gauge, stat, bargauge, pie, bar.
- Classic and Vue agree on major unit strings for the same raw value (± rounding).
