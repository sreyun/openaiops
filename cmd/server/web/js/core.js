/* ============================================================
   AIOps · 前端逻辑
   数据源：/api/v1/{summary,hosts,alerts,events,config}
   3 秒轮询（P1-2: 已改为差异化轮询频率）；事件委托绑定，避免内联 onclick 的转义隐患。

   P2-1 模块拆分说明：
   本文件可按功能域拆分为多个模块（服务端已支持 /js/ 路由）：
   - js/app-core.js    : 全局变量、工具函数、路由、轮询、主题、通知
   - js/app-render.js  : renderCards, renderHosts, renderAlerts, renderLog, renderTop
   - js/app-chart.js   : createChart, drawChart, attachChartEvents（Canvas 图表引擎）
   - js/app-terminal.js: VT100 仿真器、远程终端、会话回放
   - js/app-auth.js    : initAuth, login, MFA, 用户管理
   - js/app-automation.js: 剧本编排、批量执行
   在 index.html 中按依赖顺序加载多个 <script> 标签即可。
   ============================================================ */
"use strict";

/* ===== 树折叠：硬件/虚拟机等「左树 + 右详情」布局，一键收起左树给右侧内容腾空间 =====
   约定：容器加 .tree-wrap，左树加 .tree-pane，中间放一个 [data-tree-toggle="<存储键>"] 把手。
   折叠态记忆到 localStorage，跨视图/刷新保持。样式与点击逻辑集中在此，各视图只需按约定出 DOM。*/
(function(){
  var st = document.createElement("style");
  st.textContent =
    ".tree-wrap{position:relative}" +
    ".tree-toggle-btn{flex:0 0 16px;align-self:stretch;min-height:120px;border:1px solid var(--line);" +
      "background:var(--panel);border-radius:8px;cursor:pointer;color:var(--muted);display:flex;" +
      "align-items:center;justify-content:center;padding:0;font-size:13px;line-height:1;user-select:none;" +
      "transition:background .15s,color .15s}" +
    ".tree-toggle-btn:hover{color:var(--text);background:rgba(127,127,127,.12)}" +
    ".tree-wrap.tree-collapsed .tree-pane{display:none}" +
    // 窄屏单列布局折叠意义不大：隐藏把手并强制展开，避免出现难看的横条。
    "@media(max-width:960px){.tree-toggle-btn{display:none}.tree-wrap.tree-collapsed .tree-pane{display:block}}";
  (document.head || document.documentElement).appendChild(st);

  document.addEventListener("click", function(e){
    var btn = e.target && e.target.closest ? e.target.closest("[data-tree-toggle]") : null;
    if (!btn) return;
    var wrap = btn.closest(".tree-wrap");
    if (!wrap) return;
    var collapsed = wrap.classList.toggle("tree-collapsed");
    btn.textContent = collapsed ? "›" : "‹";
    btn.setAttribute("aria-expanded", collapsed ? "false" : "true");
    try { localStorage.setItem(btn.getAttribute("data-tree-toggle"), collapsed ? "1" : "0"); } catch(err){}
  });

  // 各视图渲染时读初始折叠态（避免首帧闪烁）。
  window.treeCollapsed = function(key){
    try { return localStorage.getItem(key) === "1"; } catch(err){ return false; }
  };
})();

/* ===== UI/UX 审查修复（5.6 弹窗语义角色 / 6.4 全局加载指示） ===== */
(function(){
  /* 6.4 全局请求加载指示：包装原生 fetch，任何请求进行中时显示顶部细进度条 */
  var _origFetch = window.fetch ? window.fetch.bind(window) : null;
  if (_origFetch) {
    var _pending = 0;
    var _bar = document.createElement("div");
    _bar.className = "loadbar";
    _bar.setAttribute("aria-hidden", "true");
    document.addEventListener("DOMContentLoaded", function(){ document.body.appendChild(_bar); });
    window.fetch = function() {
      _pending++; _bar.classList.add("active");
      return _origFetch.apply(window, arguments).finally(function(){
        _pending--; if (_pending <= 0) { _pending = 0; _bar.classList.remove("active"); }
      });
    };
  }
  /* 5.6 为所有弹窗补充语义角色（读屏支持），含动态创建的弹窗 */
  function enhanceModals(){
    document.querySelectorAll(".modal:not([role])").forEach(function(m){
      m.setAttribute("role", "dialog");
      m.setAttribute("aria-modal", "true");
    });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", enhanceModals);
  else enhanceModals();
  var _mo = window.MutationObserver && new MutationObserver(function(muts){
    muts.forEach(function(m){ if (m.addedNodes && m.addedNodes.length) enhanceModals(); });
  });
  if (_mo) _mo.observe(document.documentElement, { childList:true, subtree:true });
})();

// 防御性初始化：若 i18n-dashboard.js 加载失败，注入最小可用 I18N 对象，
// 避免 app.js 中大量顶层 I18N.t() 调用抛出 ReferenceError 导致整个脚本崩溃，
// 进而阻止 initAuth() 执行、登录界面无法显示。
if (typeof window.I18N === "undefined" || typeof window.I18N.t !== "function") {
  console.warn("[AIOps] I18N not loaded, installing fallback translator");
  window.I18N = {
    t: function(key, fallback) { return fallback || key; },
    applyTranslations: function() {},
    setLang: function() {},
    getLang: function() { return "zh-CN"; },
    syncLangButtons: function() {},
    supported: ["zh-CN"],
    init: function() {}
  };
}

const API = "/api/v1";

/**
 * beginRangeLoad — cancel prior in-flight range fetch and bump a generation token.
 * Fixes "click 1h→6h→24h and the chart keeps flipping / empties" races across
 * host detail, dashboard panels, checks, etc.
 * Usage:
 *   const load = beginRangeLoad("host-detail");
 *   const r = await fetch(url, { signal: load.signal });
 *   if (!load.isCurrent()) return;
 */
function beginRangeLoad(key) {
  if (!window.__rangeLoads) window.__rangeLoads = {};
  const prev = window.__rangeLoads[key];
  if (prev && prev.ctrl) {
    try { prev.ctrl.abort(); } catch (_) {}
  }
  const seq = (prev && prev.seq ? prev.seq : 0) + 1;
  const ctrl = (typeof AbortController !== "undefined") ? new AbortController() : null;
  const state = { seq, ctrl };
  window.__rangeLoads[key] = state;
  return {
    seq,
    signal: ctrl ? ctrl.signal : undefined,
    isCurrent: () => {
      const cur = window.__rangeLoads[key];
      return !!(cur && cur.seq === seq);
    }
  };
}

/**
 * Freeze relative [from,to] within a view session so re-clicking "1h" / forecast
 * does not slide the window with wall-clock now (reduces flicker).
 * key: stable id (e.g. "checks:abc"); rangeH: hours; custom: {from,to}|null
 */
function resolveAnchoredRange(key, rangeH, custom) {
  if (!window.__rangeAnchors) window.__rangeAnchors = {};
  if (custom && custom.from < custom.to) {
    delete window.__rangeAnchors[key];
    return { from: custom.from, to: custom.to };
  }
  const spanSec = Math.max(3600, (rangeH > 0 ? rangeH : 1) * 3600);
  let step = Math.floor(spanSec / 480);
  if (step < 5) step = 5;
  if (step > 3600) step = 3600;
  const prev = window.__rangeAnchors[key];
  if (prev && prev.rangeH === rangeH && prev.from < prev.to) {
    return { from: prev.from, to: prev.to };
  }
  const now = Math.floor(Date.now() / 1000);
  const to = Math.floor(now / step) * step;
  const from = to - spanSec;
  window.__rangeAnchors[key] = { rangeH, from, to };
  return { from, to };
}
function clearAnchoredRange(key) {
  if (window.__rangeAnchors) delete window.__rangeAnchors[key];
}

/** Format unix seconds as `<input type="datetime-local">` local value (YYYY-MM-DDTHH:mm). */
function toLocalDatetimeValue(unixSec) {
  const d = new Date((Number(unixSec) || 0) * 1000);
  if (!Number.isFinite(d.getTime())) return "";
  const p = n => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/**
 * Parse a datetime-local (or "YYYY-MM-DD HH:mm") string as **local** time.
 * `new Date("2024-01-15T14:30")` is implementation-defined (UTC vs local) and
 * is why "apply custom range" sometimes silently no-ops or shifts by 8h.
 */
function parseLocalDatetimeValue(s) {
  const str = String(s || "").trim();
  if (!str) return NaN;
  const m = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2}))?/.exec(str);
  if (m) {
    const d = new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +(m[6] || 0));
    const ms = d.getTime();
    return Number.isFinite(ms) ? Math.floor(ms / 1000) : NaN;
  }
  const ms = Date.parse(str);
  return Number.isFinite(ms) ? Math.floor(ms / 1000) : NaN;
}

/** @returns {{ok:true,from:number,to:number}|{ok:false,reason:string}} */
function readCustomRangeInputs(fromEl, toEl) {
  if (!fromEl || !toEl || !String(fromEl.value || "").trim() || !String(toEl.value || "").trim()) {
    return { ok: false, reason: "incomplete" };
  }
  const from = parseLocalDatetimeValue(fromEl.value);
  const to = parseLocalDatetimeValue(toEl.value);
  if (!Number.isFinite(from) || !Number.isFinite(to)) return { ok: false, reason: "invalid" };
  if (to <= from) return { ok: false, reason: "order" };
  if (to - from < 60) return { ok: false, reason: "tooshort" };
  return { ok: true, from, to };
}

function toastCustomRangeError(reason) {
  const map = {
    incomplete: ["time.custom_incomplete", "请选择开始和结束时间"],
    invalid: ["time.custom_invalid", "时间格式无效"],
    order: ["time.custom_order", "结束时间必须晚于开始时间"],
    tooshort: ["time.custom_tooshort", "时间范围太短（至少 1 分钟）"]
  };
  const pair = map[reason] || map.invalid;
  const msg = (typeof I18N !== "undefined" && I18N.t) ? (I18N.t(pair[0], pair[1]) || pair[1]) : pair[1];
  if (typeof toast === "function") toast(msg, reason === "invalid" ? "err" : "warn");
}

function applyCustomRangeFromInputs(fromEl, toEl, onOk) {
  const r = readCustomRangeInputs(fromEl, toEl);
  if (!r.ok) { toastCustomRangeError(r.reason); return false; }
  onOk(r.from, r.to);
  return true;
}

/**
 * Native `<input type="datetime-local">` pickers inside `.mask` / `overflow:auto`
 * (host history, zoom, checks, …) swallow clicks on the time spinner on Chromium
 * Windows — the calendar paints, but hour/minute clicks do nothing.
 * Replace the native picker with a body-level popover (date grid + <select> time).
 */
let _dtPop = null;
let _dtPopInput = null;
let _dtPopView = null; // { y, mo } month being shown

function _dtT(key, fallback) {
  return (typeof I18N !== "undefined" && I18N.t) ? (I18N.t(key, fallback) || fallback) : fallback;
}

function closeDtPopover() {
  if (_dtPop && _dtPop.parentNode) _dtPop.parentNode.removeChild(_dtPop);
  _dtPop = null;
  _dtPopInput = null;
  _dtPopView = null;
}

function _dtWeekdays() {
  const lang = (document.documentElement.lang || (typeof I18N !== "undefined" && I18N.getLang && I18N.getLang()) || "").toLowerCase();
  if (lang.indexOf("en") === 0) return ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"];
  if (lang.indexOf("zh-tw") === 0 || lang.indexOf("zh-hant") === 0) return ["日", "一", "二", "三", "四", "五", "六"];
  return ["日", "一", "二", "三", "四", "五", "六"];
}

function _dtPad(n) { return String(n).padStart(2, "0"); }

function _dtReadInput(input) {
  const parsed = parseLocalDatetimeValue(input && input.value);
  if (Number.isFinite(parsed)) return new Date(parsed * 1000);
  return new Date();
}

function _dtCommit(input, d) {
  if (!input || !d || !Number.isFinite(d.getTime())) return;
  input.value = toLocalDatetimeValue(Math.floor(d.getTime() / 1000));
  try {
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  } catch (_) {}
}

function _dtPosition(pop, input) {
  const r = input.getBoundingClientRect();
  const pw = pop.offsetWidth || 292;
  const ph = pop.offsetHeight || 340;
  let left = r.left;
  let top = r.bottom + 6;
  if (left + pw > window.innerWidth - 8) left = Math.max(8, window.innerWidth - pw - 8);
  if (left < 8) left = 8;
  if (top + ph > window.innerHeight - 8 && r.top - ph - 6 > 8) top = r.top - ph - 6;
  pop.style.left = Math.round(left) + "px";
  pop.style.top = Math.round(top) + "px";
}

function _dtRenderCal(pop, selected) {
  const cal = pop.querySelector("[data-dt-cal]");
  if (!cal || !_dtPopView) return;
  const { y, mo } = _dtPopView;
  const first = new Date(y, mo, 1);
  const startPad = first.getDay();
  const daysInMo = new Date(y, mo + 1, 0).getDate();
  const selY = selected.getFullYear(), selM = selected.getMonth(), selD = selected.getDate();
  const today = new Date();
  const wd = _dtWeekdays();
  let html = `<div class="dt-pop-week">${wd.map(w => `<span>${w}</span>`).join("")}</div><div class="dt-pop-grid">`;
  for (let i = 0; i < startPad; i++) html += `<span class="dt-pop-day is-pad"></span>`;
  for (let d = 1; d <= daysInMo; d++) {
    const isSel = selY === y && selM === mo && selD === d;
    const isToday = today.getFullYear() === y && today.getMonth() === mo && today.getDate() === d;
    html += `<button type="button" class="dt-pop-day${isSel ? " is-sel" : ""}${isToday ? " is-today" : ""}" data-dt-day="${d}">${d}</button>`;
  }
  html += "</div>";
  cal.innerHTML = html;
  const title = pop.querySelector("[data-dt-title]");
  if (title) title.textContent = `${y}-${_dtPad(mo + 1)}`;
}

function openDtPopover(input) {
  if (!input || input.disabled || input.readOnly) return;
  if (_dtPopInput === input && _dtPop) { closeDtPopover(); return; }
  closeDtPopover();
  const cur = _dtReadInput(input);
  _dtPopInput = input;
  _dtPopView = { y: cur.getFullYear(), mo: cur.getMonth() };
  const pop = document.createElement("div");
  pop.className = "dt-pop";
  pop.setAttribute("role", "dialog");
  pop.setAttribute("aria-label", _dtT("time.custom_range", "自定义时间范围"));
  const hours = Array.from({ length: 24 }, (_, i) => `<option value="${i}"${i === cur.getHours() ? " selected" : ""}>${_dtPad(i)}</option>`).join("");
  const mins = Array.from({ length: 60 }, (_, i) => `<option value="${i}"${i === cur.getMinutes() ? " selected" : ""}>${_dtPad(i)}</option>`).join("");
  pop.innerHTML = `<div class="dt-pop-head">
      <button type="button" class="dt-pop-nav" data-dt-prev aria-label="prev">‹</button>
      <div class="dt-pop-title" data-dt-title></div>
      <button type="button" class="dt-pop-nav" data-dt-next aria-label="next">›</button>
    </div>
    <div data-dt-cal></div>
    <div class="dt-pop-time">
      <label>${_dtT("time.hour", "小时")}
        <select data-dt-h class="dt-pop-sel">${hours}</select>
      </label>
      <span class="dt-sep">:</span>
      <label>${_dtT("time.min", "分")}
        <select data-dt-mi class="dt-pop-sel">${mins}</select>
      </label>
    </div>
    <div class="dt-pop-act">
      <button type="button" class="chip-btn" data-dt-now>${_dtT("time.now", "此刻")}</button>
      <button type="button" class="chip-btn primary" data-dt-ok>${_dtT("time.custom_apply", "应用")}</button>
    </div>`;
  document.body.appendChild(pop);
  _dtPop = pop;
  _dtRenderCal(pop, cur);
  _dtPosition(pop, input);

  const hourSel = pop.querySelector("[data-dt-h]");
  const minSel = pop.querySelector("[data-dt-mi]");
  const selected = () => {
    const dayBtn = pop.querySelector(".dt-pop-day.is-sel");
    const day = dayBtn ? parseInt(dayBtn.getAttribute("data-dt-day"), 10) : cur.getDate();
    const y = _dtPopView.y, mo = _dtPopView.mo;
    const h = hourSel ? parseInt(hourSel.value, 10) : cur.getHours();
    const mi = minSel ? parseInt(minSel.value, 10) : cur.getMinutes();
    return new Date(y, mo, day, h, mi, 0);
  };

  pop.addEventListener("mousedown", e => { e.stopPropagation(); });
  pop.addEventListener("click", e => {
    e.stopPropagation();
    const t = e.target;
    if (!(t instanceof Element)) return;
    if (t.closest("[data-dt-prev]")) {
      _dtPopView.mo -= 1;
      if (_dtPopView.mo < 0) { _dtPopView.mo = 11; _dtPopView.y -= 1; }
      _dtRenderCal(pop, selected());
      return;
    }
    if (t.closest("[data-dt-next]")) {
      _dtPopView.mo += 1;
      if (_dtPopView.mo > 11) { _dtPopView.mo = 0; _dtPopView.y += 1; }
      _dtRenderCal(pop, selected());
      return;
    }
    const day = t.closest("[data-dt-day]");
    if (day) {
      pop.querySelectorAll(".dt-pop-day.is-sel").forEach(el => el.classList.remove("is-sel"));
      day.classList.add("is-sel");
      return;
    }
    if (t.closest("[data-dt-now]")) {
      const n = new Date();
      _dtPopView = { y: n.getFullYear(), mo: n.getMonth() };
      if (hourSel) hourSel.value = String(n.getHours());
      if (minSel) minSel.value = String(n.getMinutes());
      _dtRenderCal(pop, n);
      _dtCommit(input, n);
      closeDtPopover();
      return;
    }
    if (t.closest("[data-dt-ok]")) {
      _dtCommit(input, selected());
      closeDtPopover();
    }
  });
}

function bindDatetimeLocal(input) {
  if (!input || input._dtBound) return;
  input._dtBound = true;
  input.setAttribute("autocomplete", "off");
  input.addEventListener("mousedown", e => {
    if (e.button !== 0) return;
    e.preventDefault();
    e.stopPropagation();
    try { input.focus(); } catch (_) {}
    openDtPopover(input);
  }, true);
  input.addEventListener("keydown", e => {
    if (e.key === "Escape" && _dtPopInput === input) {
      e.stopPropagation();
      closeDtPopover();
    } else if ((e.key === "Enter" || e.key === " ") && !_dtPop) {
      e.preventDefault();
      openDtPopover(input);
    }
  });
}

function installDatetimeLocalGuard() {
  if (document._dtGuard) return;
  document._dtGuard = true;
  const scan = root => {
    if (!root) return;
    if (root.matches && root.matches("input[type='datetime-local']")) bindDatetimeLocal(root);
    if (root.querySelectorAll) root.querySelectorAll("input[type='datetime-local']").forEach(bindDatetimeLocal);
  };
  scan(document);
  const mo = new MutationObserver(muts => {
    muts.forEach(m => {
      m.addedNodes.forEach(n => {
        if (n.nodeType === 1) scan(n);
      });
      if (_dtPopInput && m.removedNodes) {
        m.removedNodes.forEach(n => {
          if (n === _dtPopInput || (n.contains && n.contains(_dtPopInput))) closeDtPopover();
        });
      }
    });
  });
  mo.observe(document.documentElement, { childList: true, subtree: true });
  document.addEventListener("mousedown", e => {
    if (!_dtPop) return;
    const t = e.target;
    if (t === _dtPop || (_dtPop.contains && _dtPop.contains(t))) return;
    if (t === _dtPopInput) return;
    closeDtPopover();
  }, true);
  document.addEventListener("keydown", e => {
    if (e.key === "Escape" && _dtPop) {
      e.stopPropagation();
      closeDtPopover();
    }
  }, true);
  window.addEventListener("resize", () => { if (_dtPop && _dtPopInput) _dtPosition(_dtPop, _dtPopInput); });
  window.addEventListener("scroll", () => { if (_dtPop && _dtPopInput) _dtPosition(_dtPop, _dtPopInput); }, true);
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", installDatetimeLocalGuard);
} else {
  installDatetimeLocalGuard();
}

// Account password policy (mirrors the server): >=8 chars incl. upper/lower/digit/special.
function pwPolicyOK(pw){
  return typeof pw==="string" && pw.length>=8 && /[A-Z]/.test(pw) && /[a-z]/.test(pw) && /[0-9]/.test(pw) && /[^A-Za-z0-9]/.test(pw);
}

/* 复制到剪贴板（兼容 HTTP 环境） */
function copyToClipboard(text) {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text);
  }
  // Fallback: textarea + execCommand
  return new Promise((resolve, reject) => {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.cssText = "position:fixed;left:-9999px;top:-9999px;opacity:0";
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand("copy") ? resolve() : reject(new Error("execCommand failed"));
    } catch (e) { reject(e); }
    finally { document.body.removeChild(ta); }
  });
}
function copyWithFeedback(btn, text, okMsg) {
  copyToClipboard(text).then(
    () => { const old = btn.textContent; btn.textContent = "✓"; toast(okMsg, "ok"); setTimeout(() => btn.textContent = old, 1200); },
    () => toast(I18N.t("toast.copy_failed"), "err")
  );
}

/** Normalize search text: trim, NFKC, lowercase, collapse whitespace. */
function normalizeSearchText(s) {
  try {
    return String(s || "")
      .normalize("NFKC")
      .replace(/[\u3000\s]+/g, " ")
      .trim()
      .toLowerCase();
  } catch (e) {
    return String(s || "").replace(/\s+/g, " ").trim().toLowerCase();
  }
}

/** Multi-token AND match against a haystack string (already or will be normalized). */
function matchesSearchTokens(haystack, query) {
  const q = normalizeSearchText(query);
  if (!q) return true;
  const hay = normalizeSearchText(haystack);
  return q.split(" ").filter(Boolean).every(t => hay.includes(t));
}
let CUR_CATS = [];    // legacy（树筛选用 CUR_FOLDER）
let CUR_FOLDER = "";  // ""=全部, "__ungrouped__"=未分组, else folder id
try { CUR_FOLDER = localStorage.getItem("aiops_host_folder") || ""; } catch (e) {}
let HOST_FOLDERS = { folders: [], assign: {}, paths: {}, counts: {} };
let HOST_TREE_COLLAPSED = new Set();
try {
  const _htc = localStorage.getItem("aiops_host_tree_collapsed");
  if (_htc) JSON.parse(_htc).forEach(id => HOST_TREE_COLLAPSED.add(id));
} catch (e) {}
let HOST_TREE_MODE = "folder"; // folder=主机树 | type=类型树
try {
  const _htm = localStorage.getItem("aiops_host_tree_mode");
  if (_htm === "folder" || _htm === "type") HOST_TREE_MODE = _htm;
} catch (e) {}
let CUR_TYPE = ""; // 类型树选中：""=全部，否则为 platform/os 归类键
try { CUR_TYPE = localStorage.getItem("aiops_host_type") || ""; } catch (e) {}
let HOST_TREE_Q = ""; // 左侧树内搜索
let LAST_HOSTS = [];  // 最近一次主机数据（供筛选切换时本地重渲染）
let HOST_CACHE_AT = 0; // LAST_HOSTS 最近一次成功同步的时间戳（ms）
let LOG_KIND = "";    // 日志类型筛选（操作/系统/插件）
let LOG_LEVEL = "";   // 日志级别筛选
let LOG_SEARCH = ""; // 审计日志关键字搜索（内容/操作者/主机）
let LOG_TIME_RANGE = "all"; // 日志时间范围
let CHECK_TYPE = "all"; // 监控类型筛选
let HOST_SORT = "ip"; // 主机排序方式（默认按 IP 升序）
let LAST_LOG = [];    // 最近一次日志数据
let HOST_SEARCH = ""; // 主机搜索关键词
let HOST_FILTER = "all"; // 主机状态筛选 all|online|offline
let HOST_PAGE = 1;    // 主机分页当前页
const HOST_PAGE_SIZE = 12;
let LAST_CHECKS = []; // 最近一次自定义监控数据
let CHECK_SEARCH = "";   // 监控（拨测）搜索关键字
let PB_SEARCH = "";      // 编排（剧本）搜索关键字
let FWD_SEARCH = "";     // 转发搜索关键字
let LAST_PLAYBOOKS = []; // 最近一次剧本数据（供搜索就地过滤，避免每次输入都重新拉取）
let HOST_META = [];   // 主机元数据（id + hostname）用于进程监控
let DEFAULT_EMPTY = null;
let APP_STARTED = false;
let LOG_PAGE = 1;     // 日志分页当前页
let LOG_PAGE_SIZE = 50; // 日志每页条数（10/30/50/100）
let CHECK_VIEW = "pill"; // 自定义监控视图：pill(卡片,默认) | list(列表)
let HOST_VIEW = "card";  // 主机视图：card | list
let TERMINAL_ENABLED = true; // 服务端是否开启远程终端
let DESKTOP_ENABLED = true;  // 远程桌面（依赖端口转发）
let TERM_WS = null;   // 当前终端 WebSocket
let CONN_STATE = "connecting"; // connecting | connected | disconnected
let FIRST_LOAD = true;
let LAST_CATS_KEY = ""; // 用于检测分类列表是否变化
let LAST_RENDER_KEY = ""; // P0-3: 用于差量更新检测
let ALERT_TYPE = "";   // 告警类型筛选
let ALERT_SEARCH = ""; // 告警主机搜索

/* ---------- 工具函数 ---------- */
const $ = id => document.getElementById(id);
const esc = s => String(s == null ? "" : s).replace(/[&<>"']/g, c =>
  ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

/** Normalize /hosts API payload (array or {hosts:[]}) into a host array. */
function normalizeHostsPayload(j) {
  if (Array.isArray(j)) return j;
  if (j && Array.isArray(j.hosts)) return j.hosts;
  return [];
}

/**
 * Keep all host caches in sync. Previously LAST_HOSTS / _cachedHosts were only
 * written inside renderHosts() (hosts page), so Automation / Inspect / Security
 * often saw an empty list until the user opened「主机」— or raced a failed fetch.
 */
function syncHostCache(hosts) {
  const list = normalizeHostsPayload(hosts);
  const prevSig = window._hostRosterSig || "";
  const nextSig = list.map(h => h && h.id).filter(Boolean).sort().join(",");
  LAST_HOSTS = list;
  window._cachedHosts = list;
  HOST_META = list.map(h => ({ id: h.id, hostname: h.hostname }));
  HOST_CACHE_AT = Date.now();
  try {
    if (typeof PB_HOSTS !== "undefined") PB_HOSTS = list;
  } catch (_) {}
  try {
    document.dispatchEvent(new CustomEvent("aiops:hosts-updated", { detail: { hosts: list } }));
  } catch (_) {}
  // Host joined/left: keep left trees + open pickers live. Skip nested auto
  // refresh when already inside refreshHostTreesAuto (avoids double fetch).
  if (prevSig && nextSig !== prevSig && !window._hostTreeAutoInside) {
    try {
      const onHosts = !!document.querySelector("#view-hosts.active");
      if (!onHosts && typeof refreshHostTreesAuto === "function") {
        refreshHostTreesAuto({ forceHosts: false });
      } else {
        document.dispatchEvent(new CustomEvent("aiops:host-trees-refresh", { detail: { hosts: list } }));
      }
    } catch (_) {}
  }
  window._hostRosterSig = nextSig;
  return list;
}

/**
 * Fetch hosts with shared cache. opts.force bypasses TTL; opts.maxAgeMs defaults to 20s.
 * Retries once on network/5xx. Always updates LAST_HOSTS on success.
 */
async function fetchHostsList(opts) {
  const o = opts || {};
  const force = !!o.force;
  const maxAge = o.maxAgeMs != null ? o.maxAgeMs : 20000;
  const now = Date.now();
  if (!force && Array.isArray(LAST_HOSTS) && LAST_HOSTS.length && (now - HOST_CACHE_AT) < maxAge) {
    return LAST_HOSTS;
  }
  if (!force && (!LAST_HOSTS || !LAST_HOSTS.length) && Array.isArray(window._cachedHosts) && window._cachedHosts.length) {
    return syncHostCache(window._cachedHosts);
  }
  if (typeof API === "undefined") return Array.isArray(LAST_HOSTS) ? LAST_HOSTS : [];

  let lastErr = null;
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      const r = await fetch(`${API}/hosts`, { credentials: "same-origin" });
      if (r.status === 401) throw new Error("unauthorized");
      if (!r.ok) throw new Error("HTTP " + r.status);
      const j = await r.json();
      return syncHostCache(j);
    } catch (e) {
      lastErr = e;
      if (attempt === 0) await new Promise(res => setTimeout(res, 300));
    }
  }
  if (Array.isArray(LAST_HOSTS) && LAST_HOSTS.length) return LAST_HOSTS;
  throw lastErr || new Error("hosts fetch failed");
}
// withLoading: disable button + show spinner during async operation, prevent duplicate submits
const _loadingBtns = new WeakSet();
function withLoading(btnId, fn) {
  const btn = typeof btnId === "string" ? $(btnId) : btnId;
  if (!btn) return fn();
  if (_loadingBtns.has(btn)) return Promise.resolve(); // already loading, skip
  _loadingBtns.add(btn);
  const orig = btn.innerHTML;
  const origDisabled = btn.disabled;
  btn.disabled = true;
  btn.style.opacity = "0.6";
  btn.style.pointerEvents = "none";
  btn.innerHTML = '<svg class="spin" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="width:14px;height:14px;animation:spin .6s linear infinite"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>';
  return Promise.resolve(fn()).finally(() => {
    btn.innerHTML = orig;
    btn.disabled = origDisabled;
    btn.style.opacity = "";
    btn.style.pointerEvents = "";
    _loadingBtns.delete(btn);
  });
}
const fmtRate = b => b < 1024 ? b.toFixed(0) + " " + I18N.t("unit.bps")
  : b < 1048576 ? (b / 1024).toFixed(1) + " " + I18N.t("unit.kbps")
  : (b / 1048576).toFixed(2) + " " + I18N.t("unit.mbps");
const fmtIORate = b => b < 1024 ? b.toFixed(0) + " " + I18N.t("unit.bps")
  : b < 1048576 ? (b / 1024).toFixed(1) + " " + I18N.t("unit.kbps")
  : b < 1073741824 ? (b / 1048576).toFixed(1) + " " + I18N.t("unit.mbps")
  : (b / 1073741824).toFixed(2) + " " + I18N.t("unit.gbs");
const fmtIOPS = v => v < 1000 ? v.toFixed(0) : v < 10000 ? (v / 1000).toFixed(1) + I18N.t("unit.kilo") : (v / 1000).toFixed(0) + I18N.t("unit.kilo");
const fmtGB = b => (b / 1073741824).toFixed(1);
const fmtUptime = s => {
  const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
  return d > 0 ? `${d}${I18N.t("time.day")}${h}${I18N.t("time.hour")}` : h > 0 ? `${h}${I18N.t("time.hour")}${m}${I18N.t("time.min")}` : `${m}${I18N.t("time.minute")}`;
};
const fmtDateTime = ts => {
  // Product clock: Asia/Shanghai (北京时间), independent of browser TZ.
  const ms = (Number(ts) < 1e12 ? Number(ts) * 1000 : Number(ts));
  if (!Number.isFinite(ms) || ms <= 0) return "-";
  try {
    const dtf = new Intl.DateTimeFormat("en-CA", {
      timeZone: "Asia/Shanghai",
      year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit",
      hour12: false,
    });
    const bag = {};
    for (const p of dtf.formatToParts(new Date(ms))) {
      if (p.type !== "literal") bag[p.type] = p.value;
    }
    let hour = Number(bag.hour);
    if (hour === 24) hour = 0;
    const pad = n => String(n).padStart(2, "0");
    return `${bag.year}-${pad(bag.month)}-${pad(bag.day)} ${pad(hour)}:${pad(bag.minute)}:${pad(bag.second)}`;
  } catch {
    const d = new Date(ms);
    const Y = d.getFullYear();
    const M = String(d.getMonth() + 1).padStart(2, "0");
    const D = String(d.getDate()).padStart(2, "0");
    const h = String(d.getHours()).padStart(2, "0");
    const m = String(d.getMinutes()).padStart(2, "0");
    const s = String(d.getSeconds()).padStart(2, "0");
    return `${Y}-${M}-${D} ${h}:${m}:${s}`;
  }
};
const usageColor = p => p >= 90 ? "var(--crit)" : p >= 80 ? "var(--warn)" : p >= 60 ? "var(--info)" : "var(--ok)";
const ago = ts => {
  const s = Math.max(0, Math.floor(Date.now() / 1000 - ts));
  return s < 60 ? `${s}${I18N.t("time.ago_sec")}` : s < 3600 ? `${Math.floor(s / 60)}${I18N.t("time.ago_min")}` : s < 86400 ? `${Math.floor(s / 3600)}${I18N.t("time.ago_hour")}` : `${Math.floor(s / 86400)}${I18N.t("time.ago_day")}`;
};
const fmtDur = sec => {
  const s = Math.max(0, Math.floor(sec));
  if (s < 60) return `${s}${I18N.t("time.sec")}`;
  if (s < 3600) return `${Math.floor(s / 60)}${I18N.t("time.minute")}`;
  if (s < 86400) return `${Math.floor(s / 3600)}${I18N.t("time.hour")}${Math.floor(s % 3600 / 60)}${I18N.t("time.min")}`;
  return `${Math.floor(s / 86400)}${I18N.t("time.day")}${Math.floor(s % 86400 / 3600)}${I18N.t("time.hour")}`;
};
// Translate log kind from English enum to display text
const translateLogKind = k => {
  if (k === "operation") return I18N.t("ui.operation");
  if (k === "system") return I18N.t("ui.system");
  if (k === "plugin") return I18N.t("section.op_sys_plugin_plugin");
  if (k === "terminal") return "终端";
  return k;
};
// Translate log level from English enum to display text
const translateLogLevel = lvl => {
  if (lvl === "info") return I18N.t("filter.info_level");
  if (lvl === "warning") return I18N.t("ui.warning");
  if (lvl === "critical") return I18N.t("ui.critical");
  return lvl;
};
// Translate execution status from English enum to display text
const translateExecStatus = s => {
  if (s === "running") return I18N.t("exec.status.running");
  if (s === "completed") return I18N.t("exec.status.completed");
  if (s === "failed") return I18N.t("exec.status.failed");
  if (s === "success") return I18N.t("exec.status.success");
  if (s === "timeout") return I18N.t("exec.status.timeout");
  if (s === "pending") return I18N.t("exec.status.pending");
  if (s === "pending_approval") return I18N.t("exec.status.pending_approval", "待审批");
  if (s === "rejected") return I18N.t("exec.status.rejected", "已拒绝");
  if (s === "partial") return I18N.t("exec.status.partial", "部分成功");
  if (s === "skipped") return I18N.t("ui.skipped", "已跳过");
  if (s === "cancelled") return I18N.t("exec.status.cancelled", "已停止");
  return s;
};
// Translate step status from English enum to display text
const translateStepStatus = s => {
  if (s === "running") return I18N.t("exec.step.running");
  if (s === "completed") return I18N.t("exec.step.completed");
  if (s === "failed") return I18N.t("exec.step.failed");
  if (s === "timeout") return I18N.t("exec.step.timeout");
  if (s === "pending") return I18N.t("exec.step.pending");
  if (s === "success") return I18N.t("exec.status.success");
  if (s === "skipped") return I18N.t("ui.skipped", "已跳过");
  if (s === "rollback_success") return I18N.t("exec.step.rollback_success", "回滚成功");
  if (s === "rollback_failed") return I18N.t("exec.step.rollback_failed", "回滚失败");
  if (s === "cancelled") return I18N.t("exec.status.cancelled", "已停止");
  return s;
};
// 与 agent 端一致的系统目录过滤（前端再兜一道，防旧 agent / 持久化历史里残留 /boot、/System 盘）
const isSystemMount = p => {
  p = String(p || "");
  return p === "/boot" || p.startsWith("/boot/") || p === "/System" || p.startsWith("/System/");
};

/* ---------- 挂载路径智能缩短（K8s PVC / 长路径截断） ---------- */
const shortenMountPath = (raw, maxLen = 42) => {
  const p = String(raw || "");
  if (!p) return "";
  // K8s pod volume: /var/lib/kubelet/pods/<UUID>/volumes/<plugin>/<pvc-name>
  const kubeMatch = p.match(/\/volumes\/(?:kubernetes\.io~)?([^/]+)\/([^/]+)$/);
  if (kubeMatch) return "k8s:" + kubeMatch[2];
  // Docker overlay: /var/lib/docker/overlay2/<id>/merged
  if (p.startsWith("/var/lib/docker/")) {
    const last = p.split("/").pop();
    return "docker:" + (last || p);
  }
  // Snap: /snap/...
  if (p.startsWith("/snap/")) return p;
  // Generic long path: show first + last segments
  if (p.length > maxLen) {
    const head = p.slice(0, Math.floor(maxLen * 0.55));
    const tail = p.slice(-Math.floor(maxLen * 0.35));
    return head + "…" + tail;
  }
  return p;
};

/* ============================================================
   P1-1: 主题切换（默认浅色；meta theme-color / color-scheme 跟随应用主题）
   ============================================================ */
const THEME_COLOR_LIGHT = "#f5f7fa";
const THEME_COLOR_DARK = "#0a0d13";

function normalizeTheme(t) {
  return t === "dark" ? "dark" : "light";
}
function applyThemeChrome(theme) {
  const t = normalizeTheme(theme);
  document.documentElement.setAttribute("data-theme", t);
  document.documentElement.style.colorScheme = t;
  const meta = document.querySelector('meta[name="theme-color"][data-aiops-theme]');
  if (meta) meta.setAttribute("content", t === "light" ? THEME_COLOR_LIGHT : THEME_COLOR_DARK);
  const status = document.querySelector('meta[name="apple-mobile-web-app-status-bar-style"]');
  if (status) status.setAttribute("content", t === "light" ? "default" : "black-translucent");
}
function initTheme() {
  let saved = "light";
  try { saved = normalizeTheme(localStorage.getItem("aiops_theme") || "light"); } catch (_) {}
  applyThemeChrome(saved);
  syncThemeIcons(saved);
}
function toggleTheme() {
  const cur = normalizeTheme(document.documentElement.getAttribute("data-theme") || "light");
  const next = cur === "dark" ? "light" : "dark";
  try { localStorage.setItem("aiops_theme", next); } catch (_) {}
  applyThemeChrome(next);
  syncThemeIcons(next);
  // 重绘全部 Canvas + ECharts（含 HW/API/SNMP/AI/看板），跟随 CSS 变量
  if (typeof resizeAllCharts === "function") {
    try { resizeAllCharts(); } catch (e) {}
  } else {
    for (const key in DETAIL_CHARTS) { if (DETAIL_CHARTS[key] && key !== "__zoom") drawChart(DETAIL_CHARTS[key]); }
    for (const key in CHK_CHARTS) { if (CHK_CHARTS[key]) drawChart(CHK_CHARTS[key]); }
  }
}
/* 菜单展示「即将切换到」的图标与文案（浅色时显示月亮=切深色，深色时显示太阳=切浅色） */
function syncThemeIcons(theme) {
  const t = normalizeTheme(theme);
  const ddDark = document.querySelector(".user-dropdown .icon-theme-dark");
  const ddLight = document.querySelector(".user-dropdown .icon-theme-light");
  if (ddDark && ddLight) {
    ddDark.style.display = t === "light" ? "" : "none";
    ddLight.style.display = t === "dark" ? "" : "none";
  }
  const label = document.querySelector("#ddThemeToggle .theme-toggle-label");
  if (label) {
    label.textContent = t === "dark"
      ? (typeof I18N !== "undefined" ? I18N.t("ui.theme_to_light", "切换到浅色") : "切换到浅色")
      : (typeof I18N !== "undefined" ? I18N.t("ui.theme_to_dark", "切换到深色") : "切换到深色");
  }
  const btn = $("ddThemeToggle");
  if (btn) {
    btn.setAttribute("aria-label", label ? label.textContent : "theme");
    btn.title = label ? label.textContent : "";
  }
}

/* ============================================================
   P0-4: 桌面通知 + 声音告警
   ============================================================ */
let NOTIF_PERMITTED = false;
let LAST_CRIT_COUNT = 0;
let NOTIF_SOUND_ENABLED = false;
function initNotifications() {
  if (!("Notification" in window)) return;
  NOTIF_SOUND_ENABLED = localStorage.getItem("aiops_sound") === "1";
  if (Notification.permission === "granted") {
    NOTIF_PERMITTED = true;
  }
}
function requestNotificationPermission() {
  if (!("Notification" in window)) { toast(I18N.t("toast.no_notif_support"), "err"); return; }
  Notification.requestPermission().then(p => {
    if (p === "granted") { NOTIF_PERMITTED = true; toast(I18N.t("toast.desktop_notif_on"), "ok"); }
    else { toast(I18N.t("toast.desktop_notif_denied"), "err"); }
  });
}
function notifyCriticalAlerts(critCount) {
  if (!NOTIF_PERMITTED || critCount <= LAST_CRIT_COUNT) { LAST_CRIT_COUNT = critCount; return; }
  const newAlerts = critCount - LAST_CRIT_COUNT;
  LAST_CRIT_COUNT = critCount;
  try {
    new Notification(I18N.t("misc.critical_alert_title"), {
      body: newAlerts + " " + I18N.t("misc.new_alerts_count") + " " + critCount + " " + I18N.t("misc.count_end"),
      icon: "/icon.svg",
      tag: "critical-alerts",
      renotify: true
    });
  } catch(e) {}
  // 可选声音提醒
  if (NOTIF_SOUND_ENABLED) {
    try {
      const ctx = new (window.AudioContext || window.webkitAudioContext)();
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.connect(gain); gain.connect(ctx.destination);
      osc.frequency.value = 880; osc.type = "sine";
      gain.gain.setValueAtTime(0.3, ctx.currentTime);
      gain.gain.exponentialRampToValueAtTime(0.01, ctx.currentTime + 0.5);
      osc.start(); osc.stop(ctx.currentTime + 0.5);
    } catch(e) {}
  }
}

/* ============================================================
   P1-4: 模态弹窗可访问性 — 焦点陷阱
   ============================================================ */
let FOCUS_TRAP = null;
function trapFocus(mask) {
  const focusable = mask.querySelectorAll('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])');
  if (focusable.length === 0) return;
  const first = focusable[0], last = focusable[focusable.length - 1];
  first.focus();
  FOCUS_TRAP = function(e) {
    if (e.key === "Escape") { closeMask(mask); return; }
    if (e.key !== "Tab") return;
    if (e.shiftKey) {
      if (document.activeElement === first) { e.preventDefault(); last.focus(); }
    } else {
      if (document.activeElement === last) { e.preventDefault(); first.focus(); }
    }
  };
  mask.addEventListener("keydown", FOCUS_TRAP);
}
function releaseFocus(mask) {
  if (FOCUS_TRAP) { mask.removeEventListener("keydown", FOCUS_TRAP); FOCUS_TRAP = null; }
}
function closeMask(mask) {
  if (!mask) return;
  const wasConfirm = mask.id === "uiConfirmMask" && mask.classList.contains("show") && !!_uiConfirmResolve;
  mask.classList.remove("show");
  releaseFocus(mask);
  if (wasConfirm) {
    const r = _uiConfirmResolve;
    _uiConfirmResolve = null;
    if (r) r(false);
  }
}
// P1-4: 统一模态弹窗打开函数（带焦点陷阱）
function openMask(mask) {
  if (typeof mask === "string") mask = $(mask);
  if (!mask) return;
  mask.classList.add("show");
  setTimeout(() => trapFocus(mask), 50);
}

// ---- 明细表通用分页（客户端）----
// tblClampPage：把页码夹到 [1, 总页数]。
function tblClampPage(page, total, size) { return Math.min(Math.max(1, page || 1), Math.max(1, Math.ceil((total || 0) / (size || 20)))); }
// tblPager：返回分页控件 HTML；点击「上一页/下一页」和切换「每页条数」由各表用 [data-pg] 事件委托处理。
function tblPager(total, page, size) {
  const pages = Math.max(1, Math.ceil((total || 0) / size));
  page = Math.min(Math.max(1, page), pages);
  const from = total ? (page - 1) * size + 1 : 0, to = Math.min(page * size, total);
  return `<div class="tbl-pager">
    <span class="tbl-pager-info">共 ${total} 条 · ${from}–${to}</span>
    <span class="tbl-pager-spacer"></span>
    <div class="select-wrap sm"><select class="tbl-pager-size" data-pg="size">${[10, 20, 50, 100].map(n => `<option value="${n}"${n === size ? " selected" : ""}>${n} 条/页</option>`).join("")}</select></div>
    <button class="tbl-pager-btn" data-pg="prev"${page <= 1 ? " disabled" : ""}>‹</button>
    <span class="tbl-pager-cur">${page} / ${pages}</span>
    <button class="tbl-pager-btn" data-pg="next"${page >= pages ? " disabled" : ""}>›</button>
  </div>`;
}

/* ============================================================
   P2-4: 骨架屏
   ============================================================ */
function showSkeleton() {
  const cardsEl = $("cards");
  if (cardsEl) {
    cardsEl.innerHTML = Array(6).fill(0).map(() =>
      '<div class="skeleton skeleton-card"><div class="sk-icon skeleton"></div><div class="sk-lines"><div class="sk-line skeleton w60"></div><div class="sk-line skeleton w40"></div></div></div>'
    ).join("");
  }
  const groupsEl = $("groups");
  if (groupsEl) {
    groupsEl.innerHTML = '<div class="skeleton-grid">' + Array(6).fill(0).map(() =>
      '<div class="skeleton skeleton-host"></div>'
    ).join("") + '</div>';
  }
}

/* ============================================================
   P0-3: 渲染性能优化 — 差量更新
   ============================================================ */
let HOST_DOM_CACHE = {}; // hostID -> { element, data }
function updateHostCard(h) {
  const existing = HOST_DOM_CACHE[h.id];
  if (!existing) return false; // 新主机，需全量重建
  const el = existing.element;
  // 更新在线状态 class（卡片 + 状态灯）
  el.classList.toggle("online", !!h.online);
  el.classList.toggle("offline", !h.online);
  const dot = el.querySelector(".dot");
  if (dot) dot.className = "dot " + (h.online ? "on" : "off");
  // 更新指标数值（文本与 hostCard 保持一致，避免差量更新丢失核数/容量等信息）
  const m = h.latest || {};
  const patch = (key, pct, detail) => {
    const vEl = el.querySelector(`[data-metric=${key}]`);
    if (vEl) vEl.textContent = detail;
    const bEl = el.querySelector(`[data-bar=${key}]`);
    if (bEl) { bEl.style.width = Math.max(0, Math.min(pct || 0, 100)) + "%"; bEl.style.background = usageColor(pct); }
  };
  if (m.cpu_percent !== undefined) {
    patch("cpu", m.cpu_percent, (m.cpu_percent || 0).toFixed(1) + "% · " + (m.cpu_cores || 0) + I18N.t("ui.cores"));
  }
  if (m.mem_percent !== undefined) {
    patch("mem", m.mem_percent, (m.mem_percent || 0).toFixed(1) + "% · " + fmtGB(m.mem_used || 0) + "/" + fmtGB(m.mem_total || 0) + I18N.t("unit.gb"));
  }
  if (m.disk_percent !== undefined) {
    patch("disk", m.disk_percent, (m.disk_percent || 0).toFixed(1) + "% · " + fmtGB(m.disk_used || 0) + "/" + fmtGB(m.disk_total || 0) + I18N.t("unit.gb"));
  }
  existing.data = h;
  return true;
}
function buildHostCache() {
  HOST_DOM_CACHE = {};
  document.querySelectorAll(".host").forEach(el => {
    const id = el.dataset.id;
    if (id) HOST_DOM_CACHE[id] = { element: el, data: null };
  });
}

function toast(msg, kind) {
  const t = $("toast");
  t.textContent = msg;
  t.className = "toast show " + (kind || "");
  clearTimeout(t._t);
  t._t = setTimeout(() => (t.className = "toast"), 2800);
}

/* ---------- 应用内确认对话框（替代原生 confirm） ---------- */
let _uiConfirmResolve = null;

function _finishUiConfirm(ok) {
  const mask = $("uiConfirmMask");
  const r = _uiConfirmResolve;
  _uiConfirmResolve = null;
  if (mask && mask.classList.contains("show")) {
    mask.classList.remove("show");
    releaseFocus(mask);
  }
  if (r) r(!!ok);
}

/**
 * @param {object} opts
 * @param {string} [opts.title]
 * @param {string} [opts.message]
 * @param {string} [opts.detail]
 * @param {string} [opts.confirmText]
 * @param {string} [opts.cancelText]
 * @param {"danger"|"warn"|"neutral"} [opts.tone]
 * @returns {Promise<boolean>}
 */
function uiConfirm(opts) {
  opts = opts || {};
  const mask = $("uiConfirmMask");
  if (!mask) {
    // Fallback if markup missing (tests / partial pages).
    return Promise.resolve(!!window.confirm([opts.title, opts.message, opts.detail].filter(Boolean).join("\n\n")));
  }
  if (_uiConfirmResolve) {
    const prev = _uiConfirmResolve;
    _uiConfirmResolve = null;
    prev(false);
  }
  return new Promise((resolve) => {
    _uiConfirmResolve = resolve;
    const tone = opts.tone === "danger" || opts.tone === "warn" ? opts.tone : "neutral";
    const title = opts.title || (typeof I18N !== "undefined" ? I18N.t("ui.confirm_title", "请确认") : "请确认");
    const msg = opts.message || "";
    const detail = opts.detail || "";
    const okText = opts.confirmText || (typeof I18N !== "undefined" ? I18N.t("ui.confirm_ok", "确定") : "确定");
    const cancelText = opts.cancelText || (typeof I18N !== "undefined" ? I18N.t("ui.confirm_cancel", "取消") : "取消");

    const titleEl = $("uiConfirmTitle");
    const msgEl = $("uiConfirmMessage");
    const detailEl = $("uiConfirmDetail");
    const okBtn = $("uiConfirmOkBtn");
    const cancelBtn = $("uiConfirmCancelBtn");
    const modal = mask.querySelector(".modal");

    if (titleEl) titleEl.textContent = title;
    if (msgEl) {
      msgEl.textContent = msg;
      msgEl.style.display = msg ? "" : "none";
    }
    if (detailEl) {
      detailEl.textContent = detail;
      detailEl.hidden = !detail;
    }
    if (okBtn) {
      okBtn.textContent = okText;
      okBtn.className = "btn " + (tone === "danger" ? "danger" : tone === "warn" ? "warn" : "primary");
    }
    if (cancelBtn) cancelBtn.textContent = cancelText;
    if (modal) {
      modal.classList.remove("ui-confirm-danger", "ui-confirm-warn", "ui-confirm-neutral");
      modal.classList.add("ui-confirm-" + tone);
    }
    mask.classList.add("show");
    trapFocus(mask);
    // Prefer cancel for high-risk; confirm for neutral.
    const focusEl = tone === "neutral" ? okBtn : cancelBtn;
    if (focusEl) setTimeout(() => focusEl.focus(), 0);
  });
}

function initUiConfirm() {
  if (window._uiConfirmReady) return;
  window._uiConfirmReady = true;
  const okBtn = $("uiConfirmOkBtn");
  const cancelBtn = $("uiConfirmCancelBtn");
  const closeBtn = $("uiConfirmCloseBtn");
  if (okBtn) okBtn.addEventListener("click", () => _finishUiConfirm(true));
  if (cancelBtn) cancelBtn.addEventListener("click", () => _finishUiConfirm(false));
  if (closeBtn) closeBtn.addEventListener("click", () => _finishUiConfirm(false));
}

function icon(name) {
  const p = {
    host: '<path d="M4 4h16v10H4z"/><path d="M2 20h20M8 14v6M16 14v6"/>',
    on:   '<circle cx="12" cy="12" r="9"/><path d="M9 12l2 2 4-4"/>',
    off:  '<circle cx="12" cy="12" r="9"/><path d="M8 12h8"/>',
    crit: '<path d="M12 3 2 20h20z"/><path d="M12 9v5M12 17v.4"/>',
    warn: '<circle cx="12" cy="12" r="9"/><path d="M12 8v5M12 16v.4"/>',
    event:'<path d="M4 5h16v14H4z"/><path d="M4 9h16M9 13h6"/>'
  }[name] || "";
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${p}</svg>`;
}

function bar(label, percent, detail, key, labelTitle) {
  const p = Math.max(0, Math.min(percent || 0, 100));
  // key（cpu/mem/disk）用于差量更新时定位数值与进度条，避免每轮询全量重建主机卡片。
  const vAttr = key ? ` data-metric="${key}"` : "";
  const bAttr = key ? ` data-bar="${key}"` : "";
  const tAttr = labelTitle ? ` title="${esc(labelTitle)}"` : "";
  return `<div class="metric"><div class="row"><span class="label"${tAttr}>${label}</span><span class="val mono"${vAttr}>${detail}</span></div>
    <div class="bar"><div class="fill"${bAttr} style="width:${p}%;background:${usageColor(percent)}"></div></div></div>`;
}

/* ---------- 数字滚动动画 ---------- */
function animateValue(el, from, to, duration = 400) {
  if (from === to) return;
  const start = performance.now();
  const step = (now) => {
    const p = Math.min((now - start) / duration, 1);
    const eased = 1 - Math.pow(1 - p, 3); // ease-out cubic
    el.textContent = Math.round(from + (to - from) * eased);
    if (p < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

/* ---------- 工具栏动作菜单（AI / 更多）：点击展开，减少主路径按钮噪音 ---------- */
function closeAllActMenus(except) {
  document.querySelectorAll(".act-menu.open").forEach(m => {
    if (except && m === except) return;
    m.classList.remove("open");
    const btn = m.querySelector(".act-menu-trigger");
    const panel = m.querySelector(".act-menu-panel");
    if (btn) btn.setAttribute("aria-expanded", "false");
    if (panel) panel.hidden = true;
  });
}
function toggleActMenu(wrap, forceOpen) {
  if (!wrap) return;
  const btn = wrap.querySelector(".act-menu-trigger");
  const panel = wrap.querySelector(".act-menu-panel");
  if (!btn || !panel) return;
  const willOpen = forceOpen != null ? !!forceOpen : !wrap.classList.contains("open");
  closeAllActMenus(willOpen ? wrap : null);
  wrap.classList.toggle("open", willOpen);
  btn.setAttribute("aria-expanded", willOpen ? "true" : "false");
  panel.hidden = !willOpen;
}
function initActMenus() {
  if (window._actMenusReady) return;
  window._actMenusReady = true;
  document.addEventListener("click", e => {
    const trigger = e.target && e.target.closest ? e.target.closest(".act-menu-trigger") : null;
    if (trigger) {
      const wrap = trigger.closest(".act-menu");
      if (!wrap) return;
      e.preventDefault();
      e.stopPropagation();
      toggleActMenu(wrap);
      return;
    }
    // 菜单项点击：先交给按钮自身 handler，再收起（不 stopPropagation）
    if (e.target && e.target.closest && e.target.closest(".act-menu-panel")) {
      if (e.target.closest("[role='menuitem'], button")) {
        setTimeout(() => closeAllActMenus(), 0);
      }
      return;
    }
    if (!e.target.closest || !e.target.closest(".act-menu")) closeAllActMenus();
  });
  document.addEventListener("keydown", e => {
    if (e.key === "Escape") closeAllActMenus();
  });
}
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", initActMenus);
  document.addEventListener("DOMContentLoaded", initUiConfirm);
} else {
  initActMenus();
  initUiConfirm();
}
