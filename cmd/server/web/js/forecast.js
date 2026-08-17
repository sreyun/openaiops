/* ============================================================================
 * Shared multi-series forecast helper for Canvas charts (hosts / AI cost / SNMP / …).
 * Left = realtime history, right = future forecast (dashed). Uses POST /metrics/forecast.
 * Supports AbortSignal + isCurrent() so stale responses never paint a new canvas.
 * ============================================================================ */
window._FC_ON = window._FC_ON || {};
window._FC_MODEL = window._FC_MODEL || {}; // scope -> model id
/** Host detail mounts many charts; keep headroom for CPU/mem/disk/net/conns/IO/… */
const FC_MAX_SERIES = 32;
/** Prefer core gauges when capping shared forecast (load1/5/15 must never be squeezed out). */
const FC_PRIORITY_KEYS = [
  "cpu_percent", "mem_percent", "disk_percent",
  "load1", "load5", "load15",
  "latency_ms", "dns_ms", "tcp_ms", "tls_ms", "ttfb_ms", "online", "loss_pct",
  "net_recv_rate", "net_sent_rate", "net_conns",
  "disk_io_util_percent", "disk_read_rate", "disk_write_rate",
  "disk_read_iops", "disk_write_iops", "proc_count", "swap_percent",
  "in_util", "out_util", "in_bps", "out_bps"
];

function _fcPriorityRank(key) {
  const k = String(key || "");
  const i = FC_PRIORITY_KEYS.indexOf(k);
  if (i >= 0) return i;
  if (k.indexOf("load") === 0) return FC_PRIORITY_KEYS.length;
  if (/^(latency|dns|tcp|tls|ttfb|loss|ok|online)/i.test(k)) return FC_PRIORITY_KEYS.length + 1;
  return FC_PRIORITY_KEYS.length + 50;
}

/**
 * LOCF-align joined multi-series sample rows for keys that are null/undefined
 * (not numeric 0 — that needs server presence maps). Used by SNMP/hardware/host
 * matrix joins so staggered Prom timestamps never open gaps in createChart.
 */
function alignJoinedSeriesSamples(samples, keys) {
  const keyList = keys && keys.length
    ? keys
    : null;
  const last = Object.create(null);
  const out = [];
  for (const sm of samples || []) {
    if (!sm) continue;
    const row = Object.assign({}, sm);
    const useKeys = keyList || Object.keys(row).filter(k => k !== "timestamp" && k !== "ts");
    for (const k of useKeys) {
      const v = row[k];
      if (v == null || (typeof v === "number" && !isFinite(v))) {
        if (last[k] != null) row[k] = last[k];
      } else if (typeof v === "number" || (typeof v === "string" && v !== "" && isFinite(+v))) {
        last[k] = +v;
      }
    }
    out.push(row);
  }
  return out;
}

/** User-facing forecast models — labels emphasize use-case, not algorithm jargon. */
const FC_MODEL_OPTIONS = [
  { id: "auto", label: "智能匹配（推荐）", title: "系统按当前数据形态自动选最合适的预测方式" },
  { id: "damped-holt", label: "平滑波动", title: "适合 CPU、延时、连接数等上下抖动的指标" },
  { id: "drift", label: "一路涨/跌", title: "适合磁盘占用、容量等持续上升或下降的指标" },
  { id: "holt-winters", label: "有规律起伏", title: "适合流量、访问量等带日/周周期的指标" },
  { id: "flat", label: "基本不变", title: "适合可用率等几乎稳定在某一水平的指标" },
];

function isChartForecastOn(scope) {
  return !!(window._FC_ON && window._FC_ON[scope]);
}
function setChartForecastOn(scope, on) {
  window._FC_ON = window._FC_ON || {};
  window._FC_ON[scope] = !!on;
}
function getChartForecastModel(scope) {
  const m = (window._FC_MODEL && window._FC_MODEL[scope]) || "auto";
  return FC_MODEL_OPTIONS.some(o => o.id === m) ? m : "auto";
}
function setChartForecastModel(scope, model) {
  window._FC_MODEL = window._FC_MODEL || {};
  const id = String(model || "auto");
  window._FC_MODEL[scope] = FC_MODEL_OPTIONS.some(o => o.id === id) ? id : "auto";
}
function forecastChipHTML(scope, on) {
  const active = on != null ? !!on : isChartForecastOn(scope);
  const sc = esc(scope || "default");
  const cur = getChartForecastModel(scope);
  const opts = FC_MODEL_OPTIONS.map(o =>
    `<option value="${esc(o.id)}" ${o.id === cur ? "selected" : ""} title="${esc(o.title)}">${esc(o.label)}</option>`
  ).join("");
  return `<button type="button" class="chip-btn${active ? " active" : ""}" data-chart-forecast="${sc}" title="多序列趋势预测：左侧实时，右侧未来（虚线）">预测</button>` +
    `<select class="chip-select fc-model-sel" data-chart-fc-model="${sc}" title="选择预测方式（默认推荐智能匹配）" ${active ? "" : "disabled"}>${opts}</select>`;
}

function _fcStillCurrent(opts) {
  if (!opts) return true;
  if (typeof opts.isCurrent === "function" && !opts.isCurrent()) return false;
  if (opts.signal && opts.signal.aborted) return false;
  return true;
}

function _fcSampleTs(sm) {
  if (!sm) return 0;
  const ts = sm.timestamp != null ? sm.timestamp : sm.ts;
  return ts ? Math.round(+ts) : 0;
}

/**
 * Build request payload from Canvas samples + series defs (supports transform).
 * Hold-forward each series to the global last timestamp so every forecast shares
 * the same "now" boundary (avoids empty purple zones for sparse TCP/UDP/…).
 * Also LOCF-fills intra-series null gaps so staggered Prom joins never drop a
 * series below the <4-point forecast floor (load1/5/15 flicker).
 */
function buildForecastRequestSeries(samples, seriesDefs) {
  const ranked = (seriesDefs || []).filter(s => !s.kind || s.kind === "history")
    .slice()
    .sort((a, b) => _fcPriorityRank(a.key) - _fcPriorityRank(b.key) || String(a.key).localeCompare(String(b.key)));
  const use = ranked.slice(0, FC_MAX_SERIES);
  let globalLast = 0;
  for (const sm of samples || []) {
    const ts = _fcSampleTs(sm);
    if (ts > globalLast) globalLast = ts;
  }
  const out = [];
  for (const s of use) {
    const pts = [];
    let lastV = null;
    for (const sm of samples || []) {
      const ts = _fcSampleTs(sm);
      if (!ts) continue;
      let v;
      try { v = typeof seriesVal === "function" ? seriesVal(s, sm) : sm[s.key]; } catch (_) { v = null; }
      if (v == null || !isFinite(+v)) {
        if (lastV == null) continue;
        v = lastV; // LOCF across null/missing join gaps
      } else {
        lastV = +v;
        v = lastV;
      }
      pts.push([ts, v]);
    }
    if (pts.length < 4) continue;
    // Dedup timestamps (keep last)
    const dedup = [];
    for (const p of pts) {
      if (dedup.length && dedup[dedup.length - 1][0] === p[0]) dedup[dedup.length - 1] = p;
      else dedup.push(p);
    }
    const last = dedup[dedup.length - 1];
    if (globalLast > last[0] + 1) {
      dedup.push([globalLast, last[1]]);
    }
    out.push({ name: String(s.key || s.label || ("s" + out.length)), points: dedup });
  }
  return out;
}

/**
 * Enrich samples/series with forecast overlays.
 * @returns {{samples, series, nowTs, meta, stale?}}
 */
async function enrichSamplesWithForecast(samples, seriesDefs, opts) {
  opts = opts || {};
  const base = { samples: samples || [], series: seriesDefs || [], nowTs: 0, meta: null };
  if (!opts.forecast || !base.samples.length || !base.series.length) return base;
  if (!_fcStillCurrent(opts)) return Object.assign(base, { stale: true });
  const reqSeries = buildForecastRequestSeries(base.samples, base.series);
  if (!reqSeries.length) {
    return Object.assign(base, { meta: { ok: false, message: "采样点不足，暂无法预测" } });
  }
  let globalLast = 0;
  let globalSpan = 0;
  for (const s of reqSeries) {
    const pts = s.points || [];
    if (pts.length < 2) continue;
    const a = +pts[0][0], b = +pts[pts.length - 1][0];
    if (b > globalLast) globalLast = b;
    const span = b - a;
    if (span > globalSpan) globalSpan = span;
  }
  let res;
  try {
    const fetchOpts = {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        series: reqSeries,
        horizon_sec: opts.horizonSec || Math.max(0, Math.round(globalSpan)),
        step: opts.step || 0,
        now_ts: globalLast || 0,
        host_id: opts.hostId || (opts.forecastScope === "host-detail" && typeof DETAIL_HOST_ID !== "undefined" ? DETAIL_HOST_ID : "") || "",
        method: opts.method != null && opts.method !== ""
          ? opts.method
          : (getChartForecastModel(opts.forecastScope || "") || "auto")
      })
    };
    if (opts.signal) fetchOpts.signal = opts.signal;
    const r = await fetch(`${API}/metrics/forecast`, fetchOpts);
    if (!_fcStillCurrent(opts)) return Object.assign(base, { stale: true });
    if (!r.ok) {
      const errTxt = await r.text().catch(() => "");
      return Object.assign(base, { meta: { ok: false, message: "预测接口失败 HTTP " + r.status + (errTxt ? (": " + errTxt.slice(0, 80)) : "") } });
    }
    res = await r.json();
  } catch (e) {
    if (e && (e.name === "AbortError" || (opts.signal && opts.signal.aborted))) {
      return Object.assign(base, { stale: true });
    }
    return Object.assign(base, { meta: { ok: false, message: String(e) } });
  }
  if (!_fcStillCurrent(opts)) return Object.assign(base, { stale: true });
  if (!res || !res.series) {
    return Object.assign(base, { meta: (res && res.meta) || { ok: false, message: "预测失败" } });
  }
  const histDefs = (seriesDefs || []).filter(s => !s.kind || s.kind === "history");
  const tsMap = new Map();
  for (const sm of base.samples) {
    const ts = _fcSampleTs(sm);
    if (!ts) continue;
    tsMap.set(ts, Object.assign({}, sm, { timestamp: ts }));
  }
  const outSeries = histDefs.map(s => Object.assign({}, s, { kind: s.kind || "history" }));
  let nowTs = (res.meta && (res.meta.now_ts || res.meta.NowTS)) || globalLast || 0;
  for (const fs of res.series) {
    if (fs.kind !== "forecast") continue;
    const baseName = String(fs.name || "").replace(/\s*·\s*预测$/, "");
    const hist = histDefs.find(s => String(s.key) === baseName || String(s.label) === baseName)
      || histDefs.find(s => (fs.name || "").indexOf(String(s.label || "")) === 0);
    const color = (hist && hist.color) || "#4c8dff";
    const fmt = hist && hist.fmt;
    const fcKey = "fc_" + (hist && hist.key ? hist.key : baseName || outSeries.length);
    const pts = fs.points || [];
    for (const pt of pts) {
      const ts = Math.round(+pt[0]);
      if (!ts || !isFinite(+pt[1])) continue;
      let row = tsMap.get(ts);
      if (!row) { row = { timestamp: ts }; tsMap.set(ts, row); }
      row[fcKey] = +pt[1];
    }
    if (!nowTs && pts.length) nowTs = Math.round(+pts[0][0]);
    outSeries.push({
      key: fcKey,
      label: (hist && hist.label ? hist.label : baseName) + " · 预测",
      color, fmt, dashed: true, kind: "forecast"
    });
  }
  if (!nowTs && base.samples.length) {
    nowTs = _fcSampleTs(base.samples[base.samples.length - 1]);
  }
  const merged = [...tsMap.values()].sort((a, b) => a.timestamp - b.timestamp);
  return { samples: merged, series: outSeries, nowTs, meta: res.meta || null };
}

/**
 * One shared forecast for many chart series defs (host detail).
 * Dedupes by series key, caps at FC_MAX_SERIES, returns merged samples + full fc series map.
 */
async function enrichSharedForecast(samples, allSeriesDefs, opts) {
  opts = opts || {};
  const seen = new Set();
  const uniq = [];
  for (const s of allSeriesDefs || []) {
    if (!s || (s.kind && s.kind !== "history")) continue;
    const k = String(s.key || s.label || "");
    if (!k || seen.has(k)) continue;
    seen.add(k);
    uniq.push(s);
  }
  // Cap after priority sort so load1/5/15 / core gauges survive dense host-detail pages.
  uniq.sort((a, b) => _fcPriorityRank(a.key) - _fcPriorityRank(b.key) || String(a.key || "").localeCompare(String(b.key || "")));
  const capped = uniq.slice(0, FC_MAX_SERIES);
  const scope = opts.forecastScope || "";
  const method = (opts.method != null && opts.method !== "")
    ? opts.method
    : getChartForecastModel(scope);
  return enrichSamplesWithForecast(samples, capped, Object.assign({}, opts, {
    forecast: true,
    method,
    forecastScope: scope
  }));
}

/**
 * Pick history + matching fc_* series for one chart from a shared enrich result.
 * Critical: do NOT reuse the shared polluted future timeline for charts without
 * their own forecast points — that creates empty purple zones + tooltip "—".
 *
 * @param {object} enriched shared enrich result
 * @param {array} chartSeriesDefs this chart's history series defs
 * @param {array} [originalSamples] this chart's original history samples (preferred)
 */
function sliceForecastForChart(enriched, chartSeriesDefs, originalSamples) {
  if (!enriched || enriched.stale) return null;
  // 阈值线（kind:'threshold'）要一起留下，而且**必须保持它自己的 kind**：改写成 history
  // 会让它变成实线并被当作可预测序列。留下之后它会横跨历史段与预测段——于是"预测曲线
  // 什么时候越过告警线"直接画在了同一张图上，这正是预测这个功能想回答的问题。
  const histDefs = (chartSeriesDefs || []).filter(s => !s.kind || s.kind === "history" || s.kind === "threshold");
  const keys = new Set(histDefs.filter(s => s.kind !== "threshold").map(s => String(s.key)));
  const outSeries = [];
  for (const s of histDefs) {
    outSeries.push(s.kind === "threshold" ? Object.assign({}, s) : Object.assign({}, s, { kind: "history" }));
  }
  const fcSeries = [];
  for (const s of (enriched.series || [])) {
    if (s.kind !== "forecast") continue;
    const baseKey = String(s.key || "").replace(/^fc_/, "");
    if (keys.has(baseKey)) {
      outSeries.push(s);
      fcSeries.push(s);
    }
  }
  const nowTs = enriched.nowTs || 0;
  const fcKeys = fcSeries.map(s => s.key);

  // Base timeline = this chart's own history (not the shared mega-merge).
  const histSrc = (originalSamples && originalSamples.length)
    ? originalSamples
    : (enriched.samples || []);
  const tsMap = new Map();
  for (const sm of histSrc) {
    const ts = _fcSampleTs(sm);
    if (!ts) continue;
    // Drop foreign future rows that may have leaked into originalSamples
    if (nowTs && ts > nowTs + 1 && fcKeys.length) {
      let own = false;
      for (const k of fcKeys) { if (sm[k] != null && isFinite(+sm[k])) { own = true; break; } }
      if (!own) continue;
    }
    tsMap.set(ts, Object.assign({}, sm, { timestamp: ts }));
  }

  let hasFuture = false;
  if (fcKeys.length && nowTs) {
    for (const sm of (enriched.samples || [])) {
      const ts = _fcSampleTs(sm);
      if (!ts) continue;
      let row = null;
      for (const k of fcKeys) {
        if (sm[k] == null || !isFinite(+sm[k])) continue;
        if (!row) {
          row = tsMap.get(ts);
          if (!row) row = { timestamp: ts };
          else row = Object.assign({}, row);
        }
        row[k] = +sm[k];
        if (ts > nowTs + 1) hasFuture = true;
      }
      if (row) tsMap.set(ts, row);
    }
  }

  const samples = [...tsMap.values()].sort((a, b) => a.timestamp - b.timestamp);
  return {
    samples,
    series: outSeries,
    nowTs: (fcSeries.length && hasFuture) ? nowTs : 0,
    meta: enriched.meta
  };
}

/** createChart + optional forecast enrichment — never paints if stale. */
async function createChartWithForecast(canvasId, samples, series, yMin, yMax, opts) {
  opts = opts || {};
  const want = !!opts.forecast || isChartForecastOn(opts.forecastScope || "");
  let sm = samples, ser = series, nowTs = opts.nowTs || 0, meta = null;
  if (want && !opts.preEnriched) {
    const en = await enrichSamplesWithForecast(samples, series, {
      forecast: true,
      horizonSec: opts.horizonSec || 0,
      step: opts.step || 0,
      method: opts.method || getChartForecastModel(opts.forecastScope || ""),
      forecastScope: opts.forecastScope || "",
      signal: opts.signal,
      isCurrent: opts.isCurrent
    });
    if (en.stale || !_fcStillCurrent(opts)) return null;
    // Single-chart path: still slice so axis only opens when this chart has future pts.
    const sliced = sliceForecastForChart(en, series, samples);
    if (sliced) {
      sm = sliced.samples; ser = sliced.series; nowTs = sliced.nowTs || 0; meta = sliced.meta;
    } else {
      sm = en.samples; ser = en.series; nowTs = en.nowTs || nowTs; meta = en.meta;
    }
  } else if (opts.preEnriched) {
    const sliced = sliceForecastForChart(opts.preEnriched, series, samples);
    if (sliced) {
      sm = sliced.samples; ser = sliced.series; nowTs = sliced.nowTs || 0; meta = sliced.meta;
    } else {
      sm = opts.preEnriched.samples || samples;
      ser = opts.preEnriched.series || series;
      nowTs = opts.preEnriched.nowTs || nowTs;
      meta = opts.preEnriched.meta;
    }
  }
  if (!_fcStillCurrent(opts)) return null;
  const state = createChart(canvasId, sm, ser, yMin, yMax, Object.assign({}, opts, {
    nowTs,
    forecastScope: opts.forecastScope || "",
    _fcBase: {
      samples: samples, series: series, yMin, yMax, title: opts.title || "",
      reload: (opts._fcBase && opts._fcBase.reload) || opts.reload || null
    }
  }));
  if (state) {
    state._fcMeta = meta;
    if (opts.reload) state.reload = opts.reload;
  }
  return state;
}

/**
 * Mount many Canvas charts that share the same samples timeline.
 * When forecast is on: one shared POST /metrics/forecast, then slice per chart.
 * loadOpts: { signal, isCurrent } from beginRangeLoad — stale responses never paint.
 */
async function mountChartsWithForecast(scope, specs, loadOpts) {
  loadOpts = loadOpts || {};
  const want = isChartForecastOn(scope);
  const isCurrent = typeof loadOpts.isCurrent === "function" ? loadOpts.isCurrent : () => true;
  const signal = loadOpts.signal;
  const out = {};
  const list = (specs || []).filter(sp => sp && sp.id);
  if (!list.length) return out;

  if (!want) {
    for (const sp of list) {
      if (!isCurrent()) return out;
      const reload = (sp.opts && sp.opts.reload) || null;
      out[sp.id] = createChart(sp.id, sp.samples, sp.series, sp.yMin, sp.yMax, Object.assign({}, sp.opts || {}, {
        forecastScope: scope,
        reload,
        _fcBase: {
          samples: sp.samples, series: sp.series, yMin: sp.yMin, yMax: sp.yMax,
          title: (sp.opts && sp.opts.title) || "",
          reload,
          horizonSec: loadOpts.horizonSec || 0
        }
      }));
    }
    return out;
  }

  // Shared enrich using the first chart's samples (all multi-chart views share one timeline).
  const samples = list[0].samples || [];
  const allSeries = [];
  for (const sp of list) {
    if (sp.series) allSeries.push(...sp.series);
  }
  const en = await enrichSharedForecast(samples, allSeries, {
    forecast: true,
    signal,
    isCurrent,
    horizonSec: loadOpts.horizonSec || 0,
    step: loadOpts.step || 0,
    method: getChartForecastModel(scope),
    forecastScope: scope,
    hostId: loadOpts.hostId || ""
  });
  if (!isCurrent() || (en && en.stale)) return out;

  for (const sp of list) {
    if (!isCurrent()) return out;
    const sliced = sliceForecastForChart(en, sp.series, sp.samples);
    const reload = (sp.opts && sp.opts.reload) || null;
    const baseOpts = Object.assign({}, sp.opts || {}, {
      forecastScope: scope,
      reload,
      _fcBase: {
        samples: sp.samples, series: sp.series, yMin: sp.yMin, yMax: sp.yMax,
        title: (sp.opts && sp.opts.title) || "",
        reload,
        horizonSec: loadOpts.horizonSec || 0
      }
    });
    if (sliced) {
      out[sp.id] = createChart(sp.id, sliced.samples, sliced.series, sp.yMin, sp.yMax,
        Object.assign(baseOpts, { nowTs: sliced.nowTs || 0 }));
    } else {
      out[sp.id] = createChart(sp.id, sp.samples, sp.series, sp.yMin, sp.yMax, baseOpts);
    }
  }
  return out;
}

// Global toggle chip handler — views listen for "chart-forecast-toggle".
document.addEventListener("click", (e) => {
  const btn = e.target && e.target.closest && e.target.closest("[data-chart-forecast]");
  if (!btn) return;
  e.preventDefault();
  const scope = btn.getAttribute("data-chart-forecast") || "default";
  const on = !isChartForecastOn(scope);
  setChartForecastOn(scope, on);
  btn.classList.toggle("active", on);
  document.querySelectorAll(`[data-chart-fc-model="${scope}"]`).forEach(sel => { sel.disabled = !on; });
  document.dispatchEvent(new CustomEvent("chart-forecast-toggle", { detail: { scope, on, method: getChartForecastModel(scope) } }));
});
document.addEventListener("change", (e) => {
  const sel = e.target && e.target.closest && e.target.closest("[data-chart-fc-model]");
  if (!sel) return;
  e.stopPropagation();
  const scope = sel.getAttribute("data-chart-fc-model") || "default";
  const prev = getChartForecastModel(scope);
  setChartForecastModel(scope, sel.value);
  const next = getChartForecastModel(scope);
  // Keep sibling selects in sync (detail + zoom).
  document.querySelectorAll(`[data-chart-fc-model="${scope}"]`).forEach(el => {
    if (el !== sel) el.value = next;
  });
  if (isChartForecastOn(scope)) {
    document.dispatchEvent(new CustomEvent("chart-forecast-toggle", {
      detail: { scope, on: true, method: next, modelChanged: next !== prev }
    }));
  }
});
