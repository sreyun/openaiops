/* ========== 仪表盘（自定义 + 导入 Grafana，面板走 VictoriaMetrics） ==========
 * 列表 / 详情渲染 / 面板查询与绘制（时序/数值/仪表/条形/表格/文本/占位）/ 时间范围 /
 * 模板变量 / 尺寸编辑 / Grafana 导入。网格按 24 栏 gridPos 忠实还原，编辑用宽度栏数+高度+排序。
 */
const DASH_COLORS = (typeof DashCharts !== "undefined" && DashCharts.PALETTES && DashCharts.PALETTES.classic)
  ? DashCharts.PALETTES.classic
  : ["#4c8dff", "#22c55e", "#f59e0b", "#ef4d5a", "#a855f7", "#06b6d4", "#eab308", "#ec4899", "#14b8a6", "#f97316"];
let DASH_ECHART_ELS = {}; // panelId → .dash-echart element
let DASH_LIST = [];
let CUR_DASH = null;               // 当前打开的完整仪表盘
let DASH_EDIT = false;             // 编辑模式
let DASH_RANGE = { hours: 1, custom: null };
let DASH_LOAD_SEQ = 0;
let DASH_LOAD = null; // { seq, signal, isCurrent } from beginRangeLoad
let DASH_VARVALS = {};             // 变量名 → 选中值
let DASH_VAR_OPTIONS = {};         // 变量名 → 候选值列表
let DASH_CHART_ARGS = {};          // panelId → createChart 参数（供 resize 重绘）
let PANEL_TARGETS_DRAFT = [];      // 面板编辑中的查询行
let VARS_DRAFT = [];               // 变量编辑中的行
let DASH_DATASOURCES = [];         // 已配置的外部数据源（Prometheus / Loki）
let DASH_UNDO = [];                // 人工编辑撤销栈
let DASH_REDO = [];                // 人工编辑重做栈
let DASH_DIRTY = false;            // 尚未保存的人工修改
let DASH_DRAG_ID = 0;              // legacy drag id (unused by pointer layout)
let DASH_AUTO_FILL = true;         // 松手后自动补位（填空洞 + 碰撞吸附）
try {
  const af = localStorage.getItem("aiops_dash_auto_fill");
  if (af === "0") DASH_AUTO_FILL = false;
  if (af === "1") DASH_AUTO_FILL = true;
} catch (e) {}
let DASH_AI_REVIEW = null;         // AI 优化预览等待中的请求
let DASH_AI_REVIEW_RESOLVE = null;
let DASH_TICKET_DRAFT = null;
let DASH_TREE_SEL = "";            // ""=全部 | custom | grafana | ai
try { DASH_TREE_SEL = localStorage.getItem("aiops_dash_tree_sel") || ""; } catch (e) {}
let DASH_SEARCH = "";
let DASH_TREE_Q = "";
let DASH_TREE_BOUND = false;
// Per-panel forecast / PoP / YoY overlays: { forecast, pop, yoy, horizonSec }
let DASH_PANEL_TREND = {};

function cloneDashboard(d) { return d ? JSON.parse(JSON.stringify(d)) : null; }
function resetDashEditHistory() { DASH_UNDO = []; DASH_REDO = []; DASH_DIRTY = false; }
function rememberDashMutation() {
  if (!CUR_DASH || !DASH_EDIT) return;
  DASH_UNDO.push(cloneDashboard(CUR_DASH));
  if (DASH_UNDO.length > 50) DASH_UNDO.shift();
  DASH_REDO = [];
  DASH_DIRTY = true;
}
function undoDashEdit() {
  if (!DASH_EDIT || !DASH_UNDO.length) return;
  DASH_REDO.push(cloneDashboard(CUR_DASH));
  CUR_DASH = DASH_UNDO.pop();
  DASH_DIRTY = true;
  resolveDashVars().then(renderDashDetail);
}
function redoDashEdit() {
  if (!DASH_EDIT || !DASH_REDO.length) return;
  DASH_UNDO.push(cloneDashboard(CUR_DASH));
  CUR_DASH = DASH_REDO.pop();
  DASH_DIRTY = true;
  resolveDashVars().then(renderDashDetail);
}

/** 模板变量筛选按钮显示名：优先自定义 label，否则常见英文名映射为中文。 */
function dashVarDisplayLabel(v) {
  const custom = (v && v.label || "").trim();
  if (custom) return custom;
  const name = (v && v.name || "").trim();
  switch (name.toLowerCase()) {
    case "instance": return "实例";
    case "device": case "device_name": return "设备";
    case "interface": case "ifname": case "ifdescr": return "接口";
    case "host": case "hostname": case "node": return "主机";
    case "job": return "任务";
    case "pod": return "Pod";
    case "service": return "服务";
    case "container": return "容器";
    case "namespace": return "命名空间";
    case "cluster": return "集群";
    case "region": case "zone": return "区域";
    case "mountpoint": case "mount": return "挂载点";
    case "disk": return "磁盘";
    case "cpu": return "CPU";
    case "gpu": return "GPU";
    case "env": case "environment": return "环境";
    case "system": return "系统";
    case "endpoint": return "接口";
    case "app": case "application": return "应用";
    default: return name || "变量";
  }
}

// 数据源解析：面板级覆盖 > 看板级默认 > 内置 VM（""）
function resolveDS(panel) { return (panel && panel.datasource) || (CUR_DASH && CUR_DASH.datasource) || ""; }
function dsById(id) { return DASH_DATASOURCES.find(d => d.id === id); }
function dsLabel(id) { if (!id || id === "vm") return "内置 VM"; const d = dsById(id); return d ? d.name : id; }
// 生成数据源下拉 options（kinds: 指标面板=["prometheus"]含内置VM；日志面板=["loki"]）
function dsOptions(selected, kinds, withVM) {
  let html = withVM ? `<option value="" ${!selected || selected === "vm" ? "selected" : ""}>内置 VM（VictoriaMetrics）</option>` : "";
  DASH_DATASOURCES.filter(d => kinds.includes(d.type) && d.enabled !== false).forEach(d => {
    html += `<option value="${esc(d.id)}" ${d.id === selected ? "selected" : ""}>${esc(d.name)} · ${d.type}</option>`;
  });
  return html;
}
async function loadDashDatasources() {
  try { const r = await fetch(`${API}/datasources`).then(r => r.json()); DASH_DATASOURCES = Array.isArray(r) ? r : []; }
  catch (e) { DASH_DATASOURCES = []; }
}

/* ---------- 列表 / 来源树 / 搜索 ---------- */
function dashSourceKind(d) {
  const s = (d && d.source) || "";
  if (s.indexOf("grafana:") === 0) return "grafana";
  if (s === "ai" || s.indexOf("ai-analysis") === 0) return "ai";
  return "custom";
}

function dashMatchesSearch(d, q) {
  if (!q) return true;
  const hay = [d.id, d.name, d.description, d.source, ...(d.tags || [])].filter(Boolean).join(" ");
  return matchesSearchTokens(hay, q);
}

function dashFilteredList() {
  const q = normalizeSearchText(DASH_SEARCH);
  const searchActive = !!q;
  return (DASH_LIST || []).filter(d => {
    if (!dashMatchesSearch(d, q)) return false;
    if (!searchActive && DASH_TREE_SEL && dashSourceKind(d) !== DASH_TREE_SEL) return false;
    return true;
  });
}

function dashTreeCaret(id, hasKids, collapsed) {
  if (!hasKids) return `<span class="rtx-caret rtx-caret-gap" aria-hidden="true"></span>`;
  return `<button type="button" class="rtx-caret" data-dashtoggle="${esc(id)}" aria-expanded="${collapsed ? "false" : "true"}">${collapsed ? "▸" : "▾"}</button>`;
}

function renderDashTree() {
  const el = $("dashTree");
  if (!el) return;
  const q = normalizeSearchText(DASH_TREE_Q);
  const searchActive = !!normalizeSearchText(DASH_SEARCH);
  const counts = { all: DASH_LIST.length, custom: 0, grafana: 0, ai: 0 };
  DASH_LIST.forEach(d => { counts[dashSourceKind(d)]++; });
  const nodes = [
    { id: "custom", label: I18N.t("section.dash_custom", "自定义"), n: counts.custom },
    { id: "grafana", label: "Grafana", n: counts.grafana },
    { id: "ai", label: "AI", n: counts.ai },
  ].filter(n => !q || matchesSearchTokens(n.label + " " + n.id, q));
  const rootCollapsed = false;
  const kids = nodes.length
    ? `<div class="rtx-children" role="group">` + nodes.map(n => {
        const sel = !searchActive && DASH_TREE_SEL === n.id;
        return `<div class="rtx-node is-leaf${sel ? " selected" : ""}" data-dashsrc="${esc(n.id)}" role="treeitem" aria-selected="${sel ? "true" : "false"}" tabindex="0">
          ${dashTreeCaret("", false, false)}
          <span class="rtx-ico rtx-ico-leaf" aria-hidden="true"></span>
          <span class="rtx-name">${esc(n.label)}</span>
          <span class="rtx-count">${n.n}</span>
        </div>`;
      }).join("") + `</div>`
    : (q ? `<div class="rtx-empty">${esc(I18N.t("section.folder_empty_hint", "无匹配分组"))}</div>` : "");
  el.innerHTML = `<div class="rtx-tree-search">
      <input type="search" id="dashTreeSearch" class="rtx-tree-q" value="${esc(DASH_TREE_Q || "")}"
        placeholder="${esc(I18N.t("section.dash_tree_search_ph", "搜索来源…"))}" autocomplete="off">
    </div>
    <div class="rtx-scroll">
      <div class="rtx-folder" role="tree">
        <div class="rtx-node rtx-root-node${!DASH_TREE_SEL || searchActive ? " selected" : ""} has-kids" data-dashsrc="" role="treeitem" tabindex="0">
          ${dashTreeCaret("__all__", true, rootCollapsed)}
          <span class="rtx-ico rtx-ico-all" aria-hidden="true"></span>
          <span class="rtx-name">${esc(I18N.t("section.dash_all", "全部仪表盘"))}</span>
          <span class="rtx-count">${counts.all}</span>
        </div>
        ${kids}
      </div>
    </div>`;
}

function bindDashTreeOnce() {
  if (DASH_TREE_BOUND) return;
  DASH_TREE_BOUND = true;
  const home = $("dashHome");
  if (!home) return;
  home.addEventListener("click", e => {
    const src = e.target.closest("[data-dashsrc]");
    if (src && !e.target.closest("[data-dashtoggle]")) {
      DASH_TREE_SEL = src.dataset.dashsrc || "";
      try { localStorage.setItem("aiops_dash_tree_sel", DASH_TREE_SEL); } catch (err) {}
      renderDashHome();
      return;
    }
  });
  home.addEventListener("input", e => {
    if (e.target.id === "dashTreeSearch") {
      DASH_TREE_Q = e.target.value || "";
      const focusPos = e.target.selectionStart;
      renderDashTree();
      const inp = $("dashTreeSearch");
      if (inp) { inp.focus(); try { inp.setSelectionRange(focusPos, focusPos); } catch (err) {} }
      return;
    }
    if (e.target.id === "dashSearch") {
      DASH_SEARCH = e.target.value || "";
      renderDashList(dashFilteredList());
      renderDashTree();
      const c = $("dashCountSpan");
      if (c) c.textContent = `${dashFilteredList().length}/${DASH_LIST.length}`;
    }
  });
  home.addEventListener("search", e => {
    if (e.target.id === "dashSearch" || e.target.id === "dashTreeSearch") {
      e.target.dispatchEvent(new Event("input", { bubbles: true }));
    }
  });
  const layout = $("dashLayout");
  if (layout && window.treeCollapsed && window.treeCollapsed("aiops_dash_tree")) {
    layout.classList.add("tree-collapsed");
    const btn = layout.querySelector("[data-tree-toggle]");
    if (btn) { btn.textContent = "›"; btn.setAttribute("aria-expanded", "false"); }
  }
}

function renderDashHome() {
  bindDashTreeOnce();
  renderDashTree();
  const list = dashFilteredList();
  const c = $("dashCountSpan");
  if (c) c.textContent = `${list.length}/${DASH_LIST.length}`;
  const searchEl = $("dashSearch");
  if (searchEl && searchEl.value !== DASH_SEARCH) searchEl.value = DASH_SEARCH;
  renderDashList(list);
}

async function loadDashboards() {
  showDashHome();
  await loadDashDatasources();
  try {
    const d = await fetch(`${API}/dashboards`).then(r => r.json());
    DASH_LIST = (d && d.dashboards) || [];
    renderDashHome();
  } catch (e) { /* ignore */ }
}
function showDashHome() {
  setDashFullscreen(false);
  const h = $("dashHome"), d = $("dashDetail");
  if (h) h.style.display = "";
  if (d) { d.style.display = "none"; d.innerHTML = ""; }
  CUR_DASH = null; DASH_EDIT = false; DASH_CHART_ARGS = {};
  Object.keys(DASH_ECHART_ELS).forEach(id => { try { const el = DASH_ECHART_ELS[id]; if (el && typeof DashCharts !== "undefined") DashCharts.dispose(el); } catch (e) {} });
  DASH_ECHART_ELS = {};
  resetDashEditHistory();
}

let DASH_FULLSCREEN = false;
function setDashFullscreen(on) {
  DASH_FULLSCREEN = !!on;
  document.body.classList.toggle("dash-fullscreen", DASH_FULLSCREEN);
  const btn = $("dashFullscreenBtn");
  if (btn) {
    btn.textContent = DASH_FULLSCREEN ? "⤓ 退出全屏" : "⛶ 全屏";
    btn.classList.toggle("active", DASH_FULLSCREEN);
    btn.title = DASH_FULLSCREEN ? "退出全屏预览（Esc）" : "全屏预览看板（Esc 退出）";
  }
  if (DASH_FULLSCREEN) {
    // 退出编辑态控件干扰，专注预览
    document.body.classList.add("dash-exporting");
  } else {
    document.body.classList.remove("dash-exporting");
  }
  requestAnimationFrame(() => {
    for (const id in DASH_CHART_ARGS) {
      try { createChart.apply(null, DASH_CHART_ARGS[id]); } catch (e) {}
    }
    if (typeof DashCharts !== "undefined") {
      try { DashCharts.resizeAll(document.getElementById("dashDetail")); } catch (e) {}
    }
  });
}
function toggleDashFullscreen() {
  if (!CUR_DASH) return;
  setDashFullscreen(!DASH_FULLSCREEN);
}
document.addEventListener("keydown", e => {
  if (e.key === "Escape" && DASH_FULLSCREEN) {
    e.preventDefault();
    setDashFullscreen(false);
  }
  if ((e.key === "f" || e.key === "F") && !e.metaKey && !e.ctrlKey && !e.altKey && CUR_DASH) {
    const tag = (e.target && e.target.tagName) || "";
    if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || (e.target && e.target.isContentEditable)) return;
    if ($("view-dashboards") && $("view-dashboards").classList.contains("active")) {
      e.preventDefault();
      toggleDashFullscreen();
    }
  }
});
function renderDashList(list) {
  const wrap = $("dashList");
  if (!wrap) return;
  if (!list.length) {
    const emptyMsg = DASH_LIST.length
      ? I18N.t("empty.no_dash_match", "没有匹配的仪表盘。")
      : "还没有仪表盘。点右上角「✨ AI 生成」用一句话生成，「导入 Grafana」按看板 ID 一键拉取（如 1860 Node Exporter Full），或「新建仪表盘」自定义面板 —— 面板查询直接走所选数据源。";
    wrap.innerHTML = `<div class="empty-box">${esc(emptyMsg)}</div>`;
    return;
  }
  wrap.innerHTML = `<div class="dash-cards">` + list.map(d => {
    const kind = dashSourceKind(d);
    const isG = kind === "grafana", isAI = kind === "ai";
    const logo = d.logo_url || (d.appearance && d.appearance.logo_url) || "";
    const ic = logo
      ? `<img class="dash-card-logo" src="${esc(logo)}" alt="" loading="lazy">`
      : `<span class="dash-card-ic ${isAI ? "ai" : isG ? "gf" : ""}">${isAI ? "✨" : "▦"}</span>`;
    return `<div class="dash-card" data-dash="${esc(d.id)}">
      <div class="dash-card-hd">
        ${ic}
        <div class="dash-card-name" title="${esc(d.name)}">${esc(d.name)}</div>
        <div class="dash-card-ops">
          <button class="mini-btn" data-dact="tpl" data-id="${esc(d.id)}" title="导出 JSON 模板">⇩</button>
          <button class="mini-btn" data-dact="meta" data-id="${esc(d.id)}" title="编辑信息">✎</button>
          <button class="mini-btn del" data-dact="del" data-id="${esc(d.id)}" title="删除">✕</button>
        </div>
      </div>
      <div class="dash-card-desc ${d.description ? "" : "muted"}">${d.description ? esc(d.description) : "暂无描述"}</div>
      <div class="dash-card-ft">
        <span class="dash-card-badge">${d.panels} 面板</span>
        ${isAI ? '<span class="dash-card-badge ai">AI</span>' : isG ? '<span class="dash-card-badge gf">Grafana</span>' : ""}
        ${(d.tags || []).slice(0, 3).map(t => `<span class="dash-card-tag">${esc(t)}</span>`).join("")}
      </div>
    </div>`;
  }).join("") + `</div>`;
}

/* ---------- 打开 / 详情渲染 ---------- */
async function openDashboard(id) {
  try {
    CUR_DASH = await fetch(`${API}/dashboards/${encodeURIComponent(id)}`).then(r => r.json());
  } catch (e) { toast("加载失败：" + e, "err"); return; }
  if (!CUR_DASH || !CUR_DASH.id) { toast("仪表盘不存在", "err"); return; }
  DASH_EDIT = false;
  resetDashEditHistory();
  // 打开看板时默认关闭各面板「预测/环比/同比」，需手动开启
  DASH_PANEL_TREND = {};
  $("dashHome").style.display = "none";
  $("dashDetail").style.display = "";
  // 直接打开（AI 生成 / 消息中心 / 事件跳转）时也要确保数据源已加载，否则「数据源」下拉只有内置 VM，无法选择外部源。
  if (!DASH_DATASOURCES.length) await loadDashDatasources();
  await resolveDashVars();
  renderDashDetail();
}
// 解析模板变量候选值 + 默认选中
async function resolveDashVars() {
  DASH_VAR_OPTIONS = {}; DASH_VARVALS = {};
  for (const v of (CUR_DASH.vars || [])) {
    let opts = [];
    try {
      const r = await fetch(`${API}/dashboards/var-values`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(Object.assign({}, v, { datasource: (CUR_DASH && CUR_DASH.datasource) || "" })) }).then(r => r.json());
      opts = (r && r.values) || [];
    } catch (e) { /* ignore */ }
    if (v.include_all) opts = ["$__all", ...opts];
    DASH_VAR_OPTIONS[v.name] = opts;
    let cur = v.current || (opts.length ? opts[0] : "");
    if (cur === "$__all" || cur === "All") cur = "$__all";
    DASH_VARVALS[v.name] = cur;
  }
}
function dashRange() {
  const dashKey = "dashboard:" + (CUR_DASH && CUR_DASH.id || "x");
  if (DASH_RANGE.custom && DASH_RANGE.custom.from < DASH_RANGE.custom.to) {
    if (typeof clearAnchoredRange === "function") clearAnchoredRange(dashKey);
    return { from: DASH_RANGE.custom.from, to: DASH_RANGE.custom.to };
  }
  if (typeof resolveAnchoredRange === "function") {
    return resolveAnchoredRange(dashKey, DASH_RANGE.hours > 0 ? DASH_RANGE.hours : 1, null);
  }
  const now = Math.floor(Date.now() / 1000);
  const h = DASH_RANGE.hours > 0 ? DASH_RANGE.hours : 1;
  return { from: now - h * 3600, to: now };
}
function dashAbortPanelLoads() {
  const m = window.__dashPanelLoads;
  if (!m) return;
  Object.keys(m).forEach(k => {
    const cur = m[k];
    if (cur && cur.ctrl) {
      try { cur.ctrl.abort(); } catch (_) {}
    }
    m[k] = { seq: (cur && cur.seq ? cur.seq : 0) + 1, ctrl: null };
  });
}
function dashBumpLoad() {
  const key = "dashboard:" + (CUR_DASH && CUR_DASH.id || "x");
  dashAbortPanelLoads();
  if (typeof beginRangeLoad === "function") {
    DASH_LOAD = beginRangeLoad(key);
    DASH_LOAD_SEQ = DASH_LOAD.seq;
  } else {
    DASH_LOAD = null;
    DASH_LOAD_SEQ++;
  }
  return DASH_LOAD_SEQ;
}
function dashBeginPanelLoad(panelKey) {
  if (!window.__dashPanelLoads) window.__dashPanelLoads = {};
  const prev = window.__dashPanelLoads[panelKey];
  if (prev && prev.ctrl) {
    try { prev.ctrl.abort(); } catch (_) {}
  }
  const seq = (prev && prev.seq ? prev.seq : 0) + 1;
  const ctrl = (typeof AbortController !== "undefined") ? new AbortController() : null;
  window.__dashPanelLoads[panelKey] = { seq, ctrl };
  return {
    seq,
    signal: ctrl ? ctrl.signal : undefined,
    isCurrent: () => {
      const cur = window.__dashPanelLoads[panelKey];
      return !!(cur && cur.seq === seq);
    }
  };
}
function dashAppearance(d) {
  const a = (d && d.appearance) || {};
  return {
    logo_url: a.logo_url || "",
    background_url: a.background_url || "",
    background_color: a.background_color || "",
    background_fit: a.background_fit || "cover",
    panel_opacity: (typeof a.panel_opacity === "number" && a.panel_opacity > 0) ? a.panel_opacity : 0
  };
}
function dashHasCustomBg(a) {
  return !!(a && (a.background_url || a.background_color));
}
function applyDashAppearanceStyles(wrap, d) {
  if (!wrap) return;
  const a = dashAppearance(d);
  const custom = dashHasCustomBg(a);
  wrap.classList.toggle("dash-has-appear", custom || !!a.logo_url);
  wrap.classList.toggle("dash-has-custom-bg", custom);
  if (a.background_color) wrap.style.setProperty("--dash-bg-color", a.background_color);
  else wrap.style.removeProperty("--dash-bg-color");
  if (a.background_url) {
    wrap.style.setProperty("--dash-bg-image", `url("${a.background_url.replace(/"/g, "")}")`);
    wrap.style.setProperty("--dash-bg-fit", a.background_fit === "contain" ? "contain" : a.background_fit === "repeat" ? "auto" : "cover");
    wrap.style.setProperty("--dash-bg-repeat", a.background_fit === "repeat" ? "repeat" : "no-repeat");
  } else {
    wrap.style.removeProperty("--dash-bg-image");
    wrap.style.removeProperty("--dash-bg-fit");
    wrap.style.removeProperty("--dash-bg-repeat");
  }
  const alpha = custom ? (a.panel_opacity || 0.92) : 1;
  wrap.style.setProperty("--dash-panel-alpha", String(alpha));
}

function renderDashDetail() {
  const d = CUR_DASH, wrap = $("dashDetail");
  if (!wrap) return;
  // Bump generation so in-flight panel fetches from a previous range are ignored.
  dashBumpLoad();
  DASH_CHART_ARGS = {};
  Object.keys(DASH_ECHART_ELS).forEach(id => { try { const el = DASH_ECHART_ELS[id]; if (el && typeof DashCharts !== "undefined") DashCharts.dispose(el); } catch (e) {} });
  DASH_ECHART_ELS = {};
  const ranges = [[1, "1h"], [6, "6h"], [24, "24h"], [72, "3d"], [168, "7d"]];
  const rangeChips = ranges.map(([h, l]) => `<button class="chip-btn ${!DASH_RANGE.custom && DASH_RANGE.hours === h ? "active" : ""}" data-drange="${h}">${l}</button>`).join("");
  const rng = dashRange();
  const varSel = (d.vars || []).map(v => {
    const opts = DASH_VAR_OPTIONS[v.name] || [];
    const cur = DASH_VARVALS[v.name];
    const vLabel = dashVarDisplayLabel(v);
    if (v.type === "textbox" || v.type === "constant") {
      return `<span class="dash-var"><label>${esc(vLabel)}</label><input type="text" class="dt-input" data-dvar="${esc(v.name)}" value="${esc(cur || "")}" style="width:120px"></span>`;
    }
    const optsHtml = opts.map(o => `<option value="${esc(o)}" ${o === cur ? "selected" : ""}>${o === "$__all" ? "全部" : esc(o)}</option>`).join("");
    return `<span class="dash-var"><label>${esc(vLabel)}</label><div class="select-wrap sm"><select data-dvar="${esc(v.name)}">${optsHtml || `<option value="">（无候选）</option>`}</select></div></span>`;
  }).join("");
  const srcBadge = (d.source && d.source.indexOf("grafana:") === 0) ? '<span class="dash-badge">Grafana</span>'
    : (d.source === "ai" || (d.source || "").indexOf("ai-analysis") === 0) ? '<span class="dash-badge ai">AI</span>' : "";
  const appear = dashAppearance(d);
  const logoHtml = appear.logo_url
    ? `<img class="dash-brand-logo" src="${esc(appear.logo_url)}" alt="" draggable="false">`
    : "";
  wrap.innerHTML = `
    <div class="dash-bar">
      <div class="dash-bar-main">
        <button class="dash-icon-btn" id="dashBack" title="返回列表">←</button>
        <div class="dash-title-wrap">${logoHtml}<span class="dash-title">${esc(d.name)}</span>${srcBadge}${DASH_EDIT ? `<span class="dash-edit-state ${DASH_DIRTY ? "dirty" : ""}" role="status" aria-live="polite">${DASH_DIRTY ? "有未保存修改" : "编辑中"}</span>` : ""}</div>
        <div class="dash-bar-actions">
          <span class="dash-ai-actions">
            <button class="btn ghost sm" id="dashAnalyzeBtn" title="基于当前时间范围生成可追问、可导出的 AI 诊断报告">✦ AI 诊断</button>
            <button class="btn ghost sm" id="dashOptimizeBtn" title="AI 评审 → 查询干跑 → 差异预览 → 人工确认">✨ AI 优化</button>
            <button class="btn ghost sm" id="dashTicketBtn" title="AI 研判 → 可编辑工单草案 → 人工确认">🎫 AI 建单</button>
          </span>
          <button class="btn ghost sm" id="dashFullscreenBtn" title="全屏预览看板（Esc 退出）">⛶ 全屏</button>
          <button class="btn ghost sm" id="dashExportBtn" title="导出看板视觉 / 数据 / JSON 模板">⇩ 导出</button>
          <span class="dash-sep"></span>
          ${DASH_EDIT
            ? `<button class="btn sm" id="dashUndoBtn" ${DASH_UNDO.length ? "" : "disabled"} title="撤销（⌘/Ctrl+Z）">↶</button><button class="btn sm" id="dashRedoBtn" ${DASH_REDO.length ? "" : "disabled"} title="重做（⌘/Ctrl+Shift+Z）">↷</button><button class="btn sm ${DASH_AUTO_FILL ? "active" : ""}" id="dashAutoFillBtn" title="松手后自动吸附空位并填补空洞（专业 BI 补位）">${DASH_AUTO_FILL ? "◉ 自动补位" : "○ 自动补位"}</button><button class="btn sm" id="dashCompactBtn" title="立即消除空洞，向上向左紧凑">⊞ 紧凑</button><button class="btn sm" id="dashTidyBtn" title="按顺序流式重排 24 栏">▦ 整齐</button><button class="btn sm" id="dashAddPanel">+ 组件</button><button class="btn sm" id="dashEditVars">变量</button><button class="btn sm" id="dashEditMeta">信息</button><button class="btn sm" id="dashCancelEdit">退出</button><button class="btn primary sm" id="dashSaveBtn">${DASH_DIRTY ? "保存修改" : "保存"}</button>`
            : `<button class="btn primary sm" id="dashEditBtn">✎ 编辑</button>`}
        </div>
      </div>
      <div class="dash-bar-sub">
        <div class="dash-range">${rangeChips}<button class="chip-btn ${DASH_RANGE.custom ? "active" : ""}" id="dashCustomToggle">自定义</button><button class="chip-btn dash-refresh" id="dashRefresh" title="刷新">↻</button></div>
        <span class="chart-custom-range" id="dashCustomPanel"${DASH_RANGE.custom ? "" : " hidden"}>
          <input type="datetime-local" id="dashCustomFrom" class="dt-input" value="${toLocalDatetimeValue(rng.from)}">
          <span class="dt-sep">→</span>
          <input type="datetime-local" id="dashCustomTo" class="dt-input" value="${toLocalDatetimeValue(rng.to)}">
          <button class="chip-btn primary" id="dashCustomApply">应用</button>
        </span>
        <span class="dash-spacer"></span>
        <div class="dash-picker"><span class="dash-picker-lbl">数据源</span><div class="select-wrap sm"><select id="dashDSSelect">${dsOptions(d.datasource, ["prometheus", "vm"], true)}</select></div></div>
        <div class="dash-vars">${varSel}</div>
      </div>
    </div>
    <div class="dash-canvas-shell ${DASH_EDIT ? "editing" : ""}">
      <div class="dash-grid ${DASH_EDIT ? "editing" : ""}" id="dashGrid"></div>
      ${DASH_EDIT && dashBreakpoint() === "d" ? `<div class="dash-layout-hud" id="dashLayoutHud" hidden></div>` : ""}
    </div>`;
  applyDashAppearanceStyles(wrap, d);
  renderPanels();
}
function renderPanels() {
  const grid = $("dashGrid");
  if (!grid || !CUR_DASH) return;
  const panels = (CUR_DASH.panels || []).slice().sort((a, b) => (a.grid.y - b.grid.y) || (a.grid.x - b.grid.x));
  if (!panels.length) {
    grid.style.removeProperty("--dash-canvas-rows");
    grid.innerHTML = `<div class="empty-box" style="grid-column:span 24">还没有面板。${DASH_EDIT ? "点上方「+ 面板」添加。" : "点「编辑」进入编辑模式后添加面板。"}</div>`;
    return;
  }
  const canvasRows = dashCanvasRows(panels);
  grid.style.setProperty("--dash-canvas-rows", String(canvasRows));
  const canvasHTML = (DASH_EDIT && dashBreakpoint() === "d")
    ? `<div class="dash-grid-canvas" id="dashGridCanvas" aria-hidden="true" style="grid-column:1/-1;grid-row:1 / span ${canvasRows}">${dashCanvasCellsHTML(canvasRows)}</div>
       <div class="dash-align-guides" id="dashAlignGuides" aria-hidden="true"></div>`
    : "";
  grid.innerHTML = canvasHTML + panels.map(p => {
    const style = dashPanelGridStyle(p);
    const dsTag = p.datasource ? `<span class="dash-panel-ds" title="面板数据源">${esc(dsLabel(p.datasource))}</span>` : "";
    const zoomBtn = (p.type !== "text" && p.type !== "unsupported")
      ? `<button type="button" class="mini-btn" data-pact="zoom" data-id="${p.id}" title="放大查看组件">⛶</button>` : "";
    const aiBtn = (p.type !== "text" && p.type !== "unsupported") ? `<button class="mini-btn" data-pact="ai" data-id="${p.id}" title="AI 解读此面板">🔍</button>` : "";
    // 仅时序类面板提供预测/环比/同比；Top-N 柱图等瞬时面板不展示，避免误开未来轴
    const trendTypes = { timeseries: 1, graph: 1 };
    const tr = DASH_PANEL_TREND[p.id] || {};
    const fcModel = tr.method || "auto";
    const modelOpts = (typeof FC_MODEL_OPTIONS !== "undefined" ? FC_MODEL_OPTIONS : [
      { id: "auto", label: "智能匹配（推荐）" }, { id: "damped-holt", label: "平滑波动" },
      { id: "drift", label: "一路涨/跌" }, { id: "holt-winters", label: "有规律起伏" }, { id: "flat", label: "基本不变" }
    ]).map(o => `<option value="${esc(o.id)}"${o.id === fcModel ? " selected" : ""}>${esc(o.label)}</option>`).join("");
    const trendBtns = trendTypes[p.type] ? `<div class="dash-trend-tools" data-trend-tools="${p.id}">
        <button type="button" class="mini-btn dash-trend-btn${tr.forecast ? " active" : ""}" data-pact="forecast" data-id="${p.id}" title="趋势预测：左=历史 · 右=预测（默认按数据类型智能匹配模型）">预测</button>
        <button type="button" class="mini-btn dash-trend-btn${tr.pop ? " active" : ""}" data-pact="pop" data-id="${p.id}" title="环比：与前一等长时间段对比">环比</button>
        <button type="button" class="mini-btn dash-trend-btn${tr.yoy ? " active" : ""}" data-pact="yoy" data-id="${p.id}" title="同比：与去年同期对比">同比</button>
        ${tr.forecast ? `<select class="dash-horizon-sel chip-select" data-fc-model="${p.id}" title="预测模型">${modelOpts}</select>
        <select class="dash-horizon-sel" data-horizon="${p.id}" title="自定义预测时长">
          <option value="0"${!tr.horizonSec ? " selected" : ""}>默认窗</option>
          <option value="3600"${tr.horizonSec === 3600 ? " selected" : ""}>未来 1h</option>
          <option value="7200"${tr.horizonSec === 7200 ? " selected" : ""}>未来 2h</option>
          <option value="86400"${tr.horizonSec === 86400 ? " selected" : ""}>未来 1天</option>
          <option value="259200"${tr.horizonSec === 259200 ? " selected" : ""}>未来 3天</option>
          <option value="604800"${tr.horizonSec === 604800 ? " selected" : ""}>未来 1周</option>
        </select>` : ""}
      </div>` : "";
    const editBtns = DASH_EDIT ? `<button type="button" class="mini-btn dash-drag-handle" data-drag-handle data-id="${p.id}" title="拖动到任意格点（幕布吸附）" aria-label="拖动面板">⠿</button>
        <button type="button" class="mini-btn" data-pact="up" data-id="${p.id}" title="上移">↑</button>
        <button type="button" class="mini-btn" data-pact="down" data-id="${p.id}" title="下移">↓</button>
        <button type="button" class="mini-btn" data-pact="dup" data-id="${p.id}" title="复制组件">⧉</button>
        <button type="button" class="mini-btn" data-pact="edit" data-id="${p.id}" title="编辑">✎</button>
        <button type="button" class="mini-btn del" data-pact="del" data-id="${p.id}" title="删除">✕</button>` : "";
    const actions = (trendBtns || zoomBtn || aiBtn || editBtns) ? `<div class="panel-edit-actions">${trendBtns}${zoomBtn}${aiBtn}${editBtns}</div>` : "";
    const resizeHandles = DASH_EDIT && dashBreakpoint() === "d"
      ? `<span class="dash-resize dash-resize-e" data-resize="e" data-id="${p.id}" title="调整宽度"></span>
         <span class="dash-resize dash-resize-s" data-resize="s" data-id="${p.id}" title="调整高度"></span>
         <span class="dash-resize dash-resize-se" data-resize="se" data-id="${p.id}" title="调整大小"></span>`
      : "";
    const g = dashGridBox(p);
    return `<div class="dash-panel dp-${esc(p.type)}${DASH_EDIT ? " dash-panel-editable" : ""}" style="${style}" data-panel="${p.id}" role="article" aria-label="${esc(p.title || "未命名面板")}">
      <div class="dash-panel-head"${DASH_EDIT ? ` data-drag-surface data-id="${p.id}"` : ""}><span class="dash-panel-title" title="${esc(p.title || "")}">${esc(p.title || "")}</span>${dsTag}${actions}</div>
      ${DASH_EDIT ? `<span class="dash-panel-coord">${g.x},${g.y} · ${g.w}×${g.h}</span>` : ""}
      <div class="dash-panel-body" id="panelBody_${p.id}"></div>
      ${resizeHandles}
    </div>`;
  }).join("");
  panels.forEach(loadPanel);
}

function dashCanvasRows(panels) {
  let maxY = 16;
  (panels || []).forEach(p => {
    const b = dashGridBox(p);
    maxY = Math.max(maxY, b.y + b.h);
  });
  return Math.min(120, Math.max(24, maxY + 12));
}

function dashCanvasCellsHTML(rows) {
  const cols = 24;
  let html = "";
  for (let y = 0; y < rows; y++) {
    for (let x = 0; x < cols; x++) {
      html += `<i class="dash-cell" data-gx="${x}" data-gy="${y}"></i>`;
    }
  }
  return html;
}

// dashBreakpoint：与 CSS 媒体查询对齐，供 resize 时判断是否需重排面板。
function dashBreakpoint() {
  if (typeof window.matchMedia !== "function") return "d";
  if (window.matchMedia("(max-width: 720px)").matches) return "m";
  if (window.matchMedia("(max-width: 1100px)").matches) return "t";
  return "d";
}

// dashPanelGridStyle：桌面按 x/y/w/h 绝对落位，避免 CSS dense 把矮面板塞进空洞；
// 平板勿再把 x/2，否则非整齐半行布局会叠层；改为仅 span 宽度走自动流式排布。
function dashPanelGridStyle(p) {
  let w = Math.max(1, Math.min(24, (p.grid && p.grid.w) || 12));
  // 桌面必须尊重原始 h：禁止抬到 ≥3，否则 Grafana 常见 h=2/3 的 stat 行会与下一行 y 重叠，挤成细条。
  const hStored = Math.max(1, Math.min(48, (p.grid && p.grid.h) || 8));
  let x = (p.grid && p.grid.x) || 0;
  let y = (p.grid && p.grid.y) || 0;
  const bp = dashBreakpoint();
  if (bp === "m") {
    return `grid-column:1/-1;grid-row:span ${Math.max(3, hStored)}`;
  }
  if (bp === "t") {
    w = Math.max(1, Math.min(12, Math.ceil(w / 2)));
    return `grid-column:span ${w};grid-row:span ${Math.max(2, hStored)}`;
  }
  return `grid-column:${x + 1} / span ${w};grid-row:${y + 1} / span ${hStored}`;
}

// reflowDashLayout：按视觉顺序流式重排 24 栏（「整齐」布局）。
function reflowDashLayout(ordered) {
  let x = 0, y = 0, rowH = 0;
  for (const p of ordered) {
    if (!p.grid) p.grid = {};
    const w = Math.max(1, Math.min(24, p.grid.w || 12));
    const h = Math.max(1, Math.min(48, p.grid.h || 8));
    if (x + w > 24) { x = 0; y += rowH; rowH = 0; }
    p.grid.x = x; p.grid.y = y; p.grid.w = w; p.grid.h = h;
    x += w;
    if (h > rowH) rowH = h;
  }
}

function dashGridBox(p) {
  const g = p.grid || {};
  return {
    id: p.id,
    x: Math.max(0, Math.min(23, g.x || 0)),
    y: Math.max(0, g.y || 0),
    w: Math.max(1, Math.min(24, g.w || 12)),
    h: Math.max(1, Math.min(48, g.h || 8)),
  };
}

function dashBoxesOverlap(a, b) {
  return a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;
}

// compactDashLayout：消除空洞，向上 / 向左紧凑（保留各面板 w/h）。
function compactDashLayout(panels) {
  const ordered = panels.slice().sort((a, b) => (a.grid.y - b.grid.y) || (a.grid.x - b.grid.x));
  const placed = [];
  for (const p of ordered) {
    if (!p.grid) p.grid = {};
    const w = Math.max(1, Math.min(24, p.grid.w || 12));
    const h = Math.max(1, Math.min(48, p.grid.h || 8));
    let best = { x: 0, y: 1e9 };
    const maxY = placed.reduce((m, b) => Math.max(m, b.y + b.h), 0) + 1;
    for (let y = 0; y <= maxY; y++) {
      for (let x = 0; x <= 24 - w; x++) {
        const cand = { x, y, w, h };
        if (placed.some(b => dashBoxesOverlap(cand, b))) continue;
        if (y < best.y || (y === best.y && x < best.x)) best = { x, y };
      }
    }
    p.grid.x = best.x; p.grid.y = best.y; p.grid.w = w; p.grid.h = h;
    placed.push({ x: best.x, y: best.y, w, h });
  }
}

// pushOverlappingPanels：移动/拉伸后把与 moved 重叠的面板下推（Grafana 风格）。
function pushOverlappingPanels(movedId) {
  const panels = CUR_DASH && CUR_DASH.panels;
  if (!panels) return;
  const moved = panels.find(p => p.id === movedId);
  if (!moved) return;
  let guard = 0;
  while (guard++ < 96) {
    const mb = dashGridBox(moved);
    let changed = false;
    const others = panels.filter(p => p.id !== movedId).sort((a, b) => (a.grid.y - b.grid.y) || (a.grid.x - b.grid.x));
    for (const p of others) {
      const ob = dashGridBox(p);
      if (!dashBoxesOverlap(mb, ob)) continue;
      p.grid.y = mb.y + mb.h;
      changed = true;
    }
    // cascade: also resolve overlaps among others
    for (let i = 0; i < others.length; i++) {
      for (let j = i + 1; j < others.length; j++) {
        const a = dashGridBox(others[i]), b = dashGridBox(others[j]);
        if (!dashBoxesOverlap(a, b)) continue;
        others[j].grid.y = a.y + a.h;
        changed = true;
      }
    }
    if (!changed) break;
  }
}

// findNearestFreeSlot：在目标附近寻找可放入 w×h 的空位（自动吸附补位）。
function findNearestFreeSlot(x, y, w, h, excludeId, panelList) {
  const panels = panelList || (CUR_DASH && CUR_DASH.panels) || [];
  const boxes = panels.filter(p => p.id !== excludeId).map(dashGridBox);
  const fits = (cx, cy) => {
    if (cx < 0 || cy < 0 || cx + w > 24) return false;
    const cand = { x: cx, y: cy, w, h };
    return !boxes.some(b => dashBoxesOverlap(cand, b));
  };
  if (fits(x, y)) return { x, y };
  const maxY = boxes.reduce((m, b) => Math.max(m, b.y + b.h), y) + 24;
  let best = null, bestDist = Infinity;
  for (let radius = 1; radius <= Math.max(24, maxY); radius++) {
    for (let dy = -radius; dy <= radius; dy++) {
      for (let dx = -radius; dx <= radius; dx++) {
        if (Math.abs(dx) !== radius && Math.abs(dy) !== radius) continue;
        const cx = x + dx, cy = Math.max(0, y + dy);
        if (!fits(cx, cy)) continue;
        const dist = Math.abs(dx) + Math.abs(dy) * 1.01;
        if (dist < bestDist) {
          bestDist = dist;
          best = { x: cx, y: cy };
        }
      }
    }
    if (best) return best;
  }
  return { x: 0, y: boxes.reduce((m, b) => Math.max(m, b.y + b.h), 0) };
}

// fillHolesKeepAnchor：锚面板位置不动，其余面板尽量上移/左移填补空洞（BI 自动补位）。
function fillHolesKeepAnchor(anchorId) {
  const panels = (CUR_DASH && CUR_DASH.panels) || [];
  let changed = true, guard = 0;
  while (changed && guard++ < 80) {
    changed = false;
    const ordered = panels
      .filter(p => p.id !== anchorId)
      .sort((a, b) => (a.grid.y - b.grid.y) || (a.grid.x - b.grid.x));
    for (const p of ordered) {
      const b = dashGridBox(p);
      let ny = b.y;
      while (ny > 0) {
        const cand = { x: b.x, y: ny - 1, w: b.w, h: b.h };
        if (panels.some(o => o.id !== p.id && dashBoxesOverlap(cand, dashGridBox(o)))) break;
        ny--;
        changed = true;
      }
      let nx = b.x;
      while (nx > 0) {
        const cand = { x: nx - 1, y: ny, w: b.w, h: b.h };
        if (panels.some(o => o.id !== p.id && dashBoxesOverlap(cand, dashGridBox(o)))) break;
        nx--;
        changed = true;
      }
      p.grid.x = nx;
      p.grid.y = ny;
    }
  }
}

// resolveDropLayout：松手后吸附空位 + 碰撞下推 + 可选自动补位填空洞（锚点不漂）。
function resolveDropLayout(id, desired) {
  const p = CUR_DASH.panels.find(x => x.id === id);
  if (!p) return;
  let { x, y, w, h } = desired;
  w = Math.max(1, Math.min(24, w));
  h = Math.max(1, Math.min(48, h));
  x = Math.max(0, Math.min(24 - w, x));
  y = Math.max(0, y);
  if (DASH_AUTO_FILL && layoutTargetConflicts({ x, y, w, h }, id)) {
    const free = findNearestFreeSlot(x, y, w, h, id);
    x = free.x;
    y = free.y;
  }
  p.grid = { ...(p.grid || {}), x, y, w, h };
  pushOverlappingPanels(id);
  if (DASH_AUTO_FILL) fillHolesKeepAnchor(id);
}

function dashGridMetrics(gridEl) {
  const rect = gridEl.getBoundingClientRect();
  const styles = getComputedStyle(gridEl);
  const gap = parseFloat(styles.columnGap || styles.gap) || 8;
  const cols = dashBreakpoint() === "t" ? 12 : 24;
  const colW = (rect.width - gap * (cols - 1)) / cols;
  const rowH = 24; // matches CSS grid-auto-rows
  return { left: rect.left, top: rect.top, gap, colW, rowH, cols, width: rect.width, height: rect.height };
}

function clientToGrid(mx, gridEl) {
  const m = dashGridMetrics(gridEl);
  const stepX = m.colW + m.gap;
  const stepY = m.rowH + m.gap;
  const x = Math.max(0, Math.min(m.cols - 1, Math.floor((mx.clientX - m.left) / stepX)));
  const y = Math.max(0, Math.floor((mx.clientY - m.top) / stepY));
  return { x, y, metrics: m };
}

// DASH_LAYOUT_DRAG：自由拖放 / 拉伸交互状态（仅桌面编辑态）。
let DASH_LAYOUT_DRAG = null;

function applyPanelGridStyle(el, g) {
  if (!el || !g) return;
  el.style.gridColumn = `${(g.x || 0) + 1} / span ${g.w || 12}`;
  el.style.gridRow = `${(g.y || 0) + 1} / span ${g.h || 8}`;
}

function ensureDashGhost(grid) {
  let ghost = grid.querySelector(".dash-layout-ghost");
  if (!ghost) {
    ghost = document.createElement("div");
    ghost.className = "dash-layout-ghost";
    ghost.innerHTML = `<span class="dash-layout-ghost-label"></span>`;
    grid.appendChild(ghost);
  }
  return ghost;
}

function updateDashLayoutHUD(g, conflict) {
  const hud = $("dashLayoutHud");
  if (!hud || !g) return;
  hud.hidden = false;
  hud.textContent = conflict
    ? `格点 ${g.x},${g.y} · ${g.w}×${g.h} → 将自动吸附空位`
    : `格点 ${g.x},${g.y} · ${g.w}×${g.h}`;
  hud.classList.toggle("conflict", !!conflict);
}

function highlightDashCells(g) {
  const canvas = $("dashGridCanvas");
  if (!canvas || !g) return;
  canvas.querySelectorAll(".dash-cell.hot").forEach(el => el.classList.remove("hot"));
  for (let yy = g.y; yy < g.y + g.h; yy++) {
    for (let xx = g.x; xx < g.x + g.w; xx++) {
      const cell = canvas.querySelector(`[data-gx="${xx}"][data-gy="${yy}"]`);
      if (cell) cell.classList.add("hot");
    }
  }
}

function updateDashAlignGuides(g, excludeId) {
  const wrap = $("dashAlignGuides");
  const grid = $("dashGrid");
  if (!wrap || !grid || !g) return;
  const m = dashGridMetrics(grid);
  const lines = [];
  const boxes = ((CUR_DASH && CUR_DASH.panels) || []).filter(p => p.id !== excludeId).map(dashGridBox);
  const xs = new Set(), ys = new Set();
  boxes.forEach(b => {
    xs.add(b.x); xs.add(b.x + b.w);
    ys.add(b.y); ys.add(b.y + b.h);
  });
  const stepX = m.colW + m.gap, stepY = m.rowH + m.gap;
  [g.x, g.x + g.w].forEach(vx => {
    if (!xs.has(vx)) return;
    const left = vx * stepX;
    lines.push(`<i class="dash-guide-v" style="left:${left}px"></i>`);
  });
  [g.y, g.y + g.h].forEach(vy => {
    if (!ys.has(vy)) return;
    const top = vy * stepY;
    lines.push(`<i class="dash-guide-h" style="top:${top}px"></i>`);
  });
  wrap.innerHTML = lines.join("");
}

function clearDashLayoutChrome() {
  const hud = $("dashLayoutHud");
  if (hud) { hud.hidden = true; hud.classList.remove("conflict"); }
  const canvas = $("dashGridCanvas");
  if (canvas) canvas.querySelectorAll(".dash-cell.hot").forEach(el => el.classList.remove("hot"));
  const guides = $("dashAlignGuides");
  if (guides) guides.innerHTML = "";
}

function layoutTargetConflicts(g, excludeId) {
  const boxes = ((CUR_DASH && CUR_DASH.panels) || []).filter(p => p.id !== excludeId).map(dashGridBox);
  return boxes.some(b => dashBoxesOverlap(g, b));
}

function endDashLayoutDrag(commit) {
  if (!DASH_LAYOUT_DRAG) return;
  const { panelEl, ghost, id } = DASH_LAYOUT_DRAG;
  document.body.classList.remove("dash-layout-dragging");
  if (panelEl) panelEl.classList.remove("dragging", "resizing");
  if (ghost) ghost.remove();
  clearDashLayoutChrome();
  if (commit && CUR_DASH && id != null) {
    const cur = DASH_LAYOUT_DRAG.cur;
    const orig = DASH_LAYOUT_DRAG.orig;
    const changed = !orig || cur.x !== orig.x || cur.y !== orig.y || cur.w !== orig.w || cur.h !== orig.h;
    if (changed) {
      rememberDashMutation();
      resolveDropLayout(id, cur);
    }
    renderPanels();
  } else if (panelEl && DASH_LAYOUT_DRAG.orig) {
    applyPanelGridStyle(panelEl, DASH_LAYOUT_DRAG.orig);
  }
  DASH_LAYOUT_DRAG = null;
}

// dashEmptyHint：无数据时给可操作提示；仅当面板表达式仍残留 label="$var"（等值）时才强调 =~。
function dashEmptyHint(extra, panel) {
  const hasAll = Object.values(DASH_VARVALS || {}).some(v => v === "$__all" || v === "All" || v === "" || v === ".*");
  let hint = "";
  const exprs = (panel && Array.isArray(panel.targets) ? panel.targets : [])
    .map(t => String((t && t.expr) || "")).join(" ");
  if (/\bnode_[a-zA-Z0-9_]+/.test(exprs)) {
    hint = `<div class="dash-empty-hint">该面板仍在查询 Grafana node_* 指标；本平台 Agent 写入的是 aiops_*（如 aiops_cpu_percent）。请重新「AI 优化」或手动改查询。</div>`;
  } else if (hasAll) {
    const eqVar = /\b[a-zA-Z_][\w]*\s*=\s*"\$/;
    const stillEq = !!(CUR_DASH && Array.isArray(CUR_DASH.panels) && CUR_DASH.panels.some(p =>
      (p.targets || []).some(t => eqVar.test(String(t.expr || "")))));
    if (stillEq) {
      hint = `<div class="dash-empty-hint">模板变量为「全部」时，查询需用 instance=~"$instance"（勿用 =）</div>`;
    } else {
      hint = `<div class="dash-empty-hint">当前为「全部」；若持续无数据请检查查询是否为 aiops_* 指标（勿用 Grafana 社区的 node_*）</div>`;
    }
  }
  return `<div class="dash-empty">${extra || "该范围无数据"}${hint}</div>`;
}

/* ---------- 面板查询与绘制 ---------- */
function panelVars() { return DASH_VARVALS; }
// panelBodyH：面板正文的实际内容高度（扣除内边距），供图表填满面板用。
function panelBodyH(el) {
  if (!el) return 160;
  const cs = getComputedStyle(el);
  const pad = (parseFloat(cs.paddingTop) || 0) + (parseFloat(cs.paddingBottom) || 0);
  return Math.max(0, el.clientHeight - pad);
}
function dashDisposePanelChart(panelId) {
  const el = DASH_ECHART_ELS[panelId];
  if (el && typeof DashCharts !== "undefined") {
    try { DashCharts.dispose(el); } catch (e) {}
  }
  delete DASH_ECHART_ELS[panelId];
  delete DASH_CHART_ARGS[panelId];
}
function dashMountEchart(body, panelId) {
  if (!body) return null;
  const wantId = "dashEchart_" + panelId;
  let el = document.getElementById(wantId);
  // 复用已挂载且仍在当前 body 内的实例 DOM，避免时间范围切换时 dispose+闪白
  if (el && body.contains(el)) {
    DASH_ECHART_ELS[panelId] = el;
    return el;
  }
  const existing = body.querySelector(".dash-echart");
  if (existing && existing.id === wantId) {
    DASH_ECHART_ELS[panelId] = existing;
    return existing;
  }
  dashDisposePanelChart(panelId);
  body.innerHTML = `<div class="dash-echart" id="${wantId}"></div>`;
  el = document.getElementById(wantId);
  if (el) DASH_ECHART_ELS[panelId] = el;
  return el;
}
function dashPanelOpt(p) { return (p && p.options) || {}; }
function dashColorAt(p, i) {
  if (typeof DashCharts !== "undefined") return DashCharts.colorAt(dashPanelOpt(p), i);
  return DASH_COLORS[i % DASH_COLORS.length];
}
function dashDec(p) {
  if (typeof DashCharts !== "undefined") return DashCharts.effectiveDecimals(p);
  const o = dashPanelOpt(p);
  if (o.decimals != null) return +o.decimals;
  return p && p.decimals ? +p.decimals : null;
}
function dashApplyMapping(p, v) {
  const maps = dashPanelOpt(p).mappings || [];
  if (!maps.length) return null;
  const s = v == null || (typeof v === "number" && isNaN(v)) ? null : String(v);
  for (const m of maps) {
    if (!m) continue;
    if (m.type === "value" && s != null && String(m.value) === s) return m;
    if (m.type === "range" && typeof v === "number" && !isNaN(v)) {
      const from = m.from != null ? +m.from : -Infinity;
      const to = m.to != null ? +m.to : Infinity;
      if (v >= from && v <= to) return m;
    }
    if (m.type === "regex" && s != null && m.pattern) {
      try { if (new RegExp(m.pattern).test(s)) return m; } catch (e) {}
    }
    if (m.type === "special") {
      const sp = (m.special || "").toLowerCase();
      if ((sp === "null" || sp === "null+nan") && (v == null || s === "null")) return m;
      if ((sp === "nan" || sp === "null+nan") && typeof v === "number" && isNaN(v)) return m;
      if (sp === "empty" && (s === "" || v == null)) return m;
    }
  }
  return null;
}
function dashFmt(p, v) {
  const mapped = dashApplyMapping(p, v);
  if (mapped && mapped.text) return mapped.text;
  const o = dashPanelOpt(p);
  if ((v == null || v === "" || (typeof v === "number" && isNaN(v))) && o.no_value) return o.no_value;
  return fmtUnit(v, p.unit, dashDec(p));
}
function dashThresholdColor(p, v, min, max) {
  if (typeof DashCharts !== "undefined") {
    const o = dashPanelOpt(p);
    return DashCharts.thresholdColor(v, o.thresholds, p.unit, min != null ? min : p.min, max != null ? max : p.max, dashColorAt(p, 0), o.threshold_mode);
  }
  return statColor(v, p.unit, min != null ? min : p.min, max != null ? max : p.max);
}
function dashSortLimit(items, p, fallback) {
  const o = dashPanelOpt(p);
  if (typeof DashCharts !== "undefined") {
    return DashCharts.applyLimit(DashCharts.sortItems(items, o.sort || "desc"), o.limit, fallback);
  }
  return items.slice().sort((a, b) => b.val - a.val).slice(0, fallback || 16);
}

const DASH_COMING_SOON = {
  nodegraph: { icon: "🕸", title: "网络拓扑", desc: "节点关系 / 依赖拓扑可视化即将支持" },
  geomap: { icon: "🗺", title: "地理热力", desc: "地理分布与区域热力即将支持" },
  flamegraph: { icon: "🔥", title: "火焰图", desc: "CPU / 函数耗时剖析即将支持" },
  news: { icon: "📰", title: "资讯", desc: "RSS / 新闻面板即将支持" }
};

function renderDashComingSoon(body, p) {
  const meta = DASH_COMING_SOON[p.type] || { icon: "⬚", title: p.raw_type || p.type || "未知", desc: "该面板类型即将支持" };
  const typ = p.raw_type || p.type || "";
  body.innerHTML = `<div class="dash-coming-soon">
    <div class="dash-coming-icon">${meta.icon}</div>
    <div class="dash-coming-title">${esc(meta.title)}</div>
    <div class="dash-coming-type">${esc(typ)}</div>
    <div class="dash-coming-desc">${esc(meta.desc)}</div>
    ${(p.targets || []).length ? `<div class="dash-unsupported-q">${(p.targets || []).map(t => esc(t.expr)).join("<br>")}</div>` : ""}
  </div>`;
}

async function loadPanel(p) {
  const body = document.getElementById("panelBody_" + p.id);
  if (!body) return;
  await loadPanelContent(p, body, p.id, DASH_LOAD_SEQ);
}

/** Render panel content into an arbitrary body (inline panel or zoom modal). chartKey isolates echart registry. */
async function loadPanelContent(p, body, chartKey, loadSeq) {
  if (!body || !p) return;
  const seq = loadSeq != null ? loadSeq : DASH_LOAD_SEQ;
  const key = chartKey != null ? chartKey : p.id;
  const pView = key === p.id ? p : Object.assign({}, p, { id: key });
  const echartTypes = {
    timeseries: 1, graph: 1, gauge: 1, piechart: 1, pie: 1, barchart: 1, bar: 1,
    histogram: 1, heatmap: 1, candlestick: 1, radar: 1, sankey: 1, bargauge: 1
  };
  const reuseEl = document.getElementById("dashEchart_" + key);
  const canReuseEchart = !!(echartTypes[p.type] && reuseEl && body.contains(reuseEl));
  if (!canReuseEchart) dashDisposePanelChart(key);
  if (p.type === "text" || p.type === "markdown") {
    body.innerHTML = `<div class="dash-text">${renderAIMarkdown(p.text || "")}</div>`;
    return;
  }
  if (p.type === "clock") { renderDashClock(body, pView); return; }
  if (DASH_COMING_SOON[p.type]) { renderDashComingSoon(body, pView); return; }
  if (p.type === "unsupported") {
    renderDashComingSoon(body, Object.assign({}, pView, { type: "unsupported" }));
    const wrap = body.querySelector(".dash-coming-soon");
    if (wrap) {
      wrap.querySelector(".dash-coming-title").textContent = "暂不支持的面板类型";
      wrap.querySelector(".dash-coming-desc").textContent = "已保留原始类型与查询，导入不丢数据";
      wrap.querySelector(".dash-coming-icon").textContent = "⚠";
    }
    return;
  }
  if (p.type === "alertlist") { await loadAlertListPanel(pView, body); return; }
  if (!(p.targets || []).length) {
    dashDisposePanelChart(key);
    body.innerHTML = `<div class="dash-empty">未配置查询</div>`;
    return;
  }
  // Keep existing chart visible while refetching — avoids flash on range change.
  if (!canReuseEchart) {
    body.innerHTML = `<div class="dash-panel-skeleton" aria-busy="true" aria-label="加载中"></div>`;
  }
  const { from, to } = dashRange();
  const panelLoad = dashBeginPanelLoad(key);
  const stillCurrent = () => (seq === DASH_LOAD_SEQ || key === "zoom") && panelLoad.isCurrent();
  const run = async (fn) => { await fn; if (!stillCurrent()) return; };
  if (p.type === "logs") await run(loadLogsPanel(pView, body, from, to, panelLoad));
  else if (p.type === "timeseries" || p.type === "graph") await run(loadTimeseriesPanel(pView, body, from, to, panelLoad));
  else if (p.type === "stat") await run(loadStatPanel(pView, body, from, to, panelLoad));
  else if (p.type === "gauge") await run(loadGaugePanel(pView, body, from, to));
  else if (p.type === "piechart" || p.type === "pie") await run(loadPiePanel(pView, body, from, to));
  else if (p.type === "barchart" || p.type === "bar") await run(loadBarPanel(pView, body, from, to, panelLoad));
  else if (p.type === "histogram") await run(loadHistogramPanel(pView, body, from, to));
  else if (p.type === "state-timeline" || p.type === "statetimeline") await run(loadStateTimelinePanel(pView, body, from, to));
  else if (p.type === "heatmap") await run(loadHeatmapPanel(pView, body, from, to));
  else if (p.type === "candlestick") await run(loadCandlestickPanel(pView, body, from, to));
  else if (p.type === "radar") await run(loadRadarPanel(pView, body, from, to));
  else if (p.type === "sankey") await run(loadSankeyPanel(pView, body, from, to));
  else await run(loadInstantPanel(pView, body, from, to));
  // Stale range: clear skeleton that a superseded load may have left behind.
  if (!stillCurrent() && body.querySelector(".dash-panel-skeleton")) {
    /* superseded — leave the newer render alone */
  }
}

let DASH_ZOOM_PID = 0;
async function openDashPanelZoom(pid) {
  if (!CUR_DASH) return;
  const p = (CUR_DASH.panels || []).find(x => x.id === pid);
  if (!p) return;
  DASH_ZOOM_PID = pid;
  const mask = $("dashPanelZoomMask");
  const title = $("dashPanelZoomTitle");
  const body = $("dashPanelZoomBody");
  if (!mask || !body) return;
  if (title) title.textContent = (p.title || "组件") + " · 放大预览";
  mask.classList.add("show");
  body.innerHTML = `<div class="dash-panel-skeleton" aria-busy="true" aria-label="加载中"></div>`;
  // 同步预测/环比状态到 zoom key，并独立挂载，避免销毁看板内原图
  if (DASH_PANEL_TREND[pid]) {
    DASH_PANEL_TREND.zoom = Object.assign({}, DASH_PANEL_TREND[pid]);
  } else {
    delete DASH_PANEL_TREND.zoom;
  }
  await loadPanelContent(p, body, "zoom");
  requestAnimationFrame(() => {
    if (typeof DashCharts !== "undefined") {
      try { DashCharts.resizeAll(body); } catch (e) {}
    }
  });
}
function closeDashPanelZoom() {
  const mask = $("dashPanelZoomMask");
  if (mask) mask.classList.remove("show");
  dashDisposePanelChart("zoom");
  delete DASH_PANEL_TREND.zoom;
  DASH_ZOOM_PID = 0;
}
safeAddEventListener("dashPanelZoomMask", "click", e => {
  if (e.target && (e.target.id === "dashPanelZoomMask" || e.target.closest("[data-dash-zoom-close]"))) {
    closeDashPanelZoom();
  }
});
document.addEventListener("keydown", e => {
  if (e.key === "Escape" && $("dashPanelZoomMask") && $("dashPanelZoomMask").classList.contains("show")) {
    closeDashPanelZoom();
  }
});

function renderDashClock(body, p) {
  const id = "dashClock_" + p.id;
  const tick = () => {
    const el = document.getElementById(id);
    if (!el) return;
    const now = new Date();
    const p2 = n => String(n).padStart(2, "0");
    el.querySelector(".dash-clock-time").textContent =
      `${p2(now.getHours())}:${p2(now.getMinutes())}:${p2(now.getSeconds())}`;
    el.querySelector(".dash-clock-date").textContent =
      `${now.getFullYear()}-${p2(now.getMonth() + 1)}-${p2(now.getDate())}`;
  };
  body.innerHTML = `<div class="dash-clock" id="${id}"><div class="dash-clock-time">--:--:--</div><div class="dash-clock-date"></div>${p.title ? "" : ""}</div>`;
  tick();
  if (body._clockTimer) clearInterval(body._clockTimer);
  body._clockTimer = setInterval(tick, 1000);
}

async function loadCandlestickPanel(p, body, from, to) {
  const series = await rangeSeries(p, body, from, to);
  if (!series) return;
  const pts = downsample((series[0] && series[0].points) || [], 80);
  if (pts.length < 2) { body.innerHTML = dashEmptyHint("数据不足以绘制 K 线"); return; }
  // Approximate OHLC from single series buckets.
  const ohlc = [];
  for (let i = 1; i < pts.length; i++) {
    const a = pts[i - 1][1], b = pts[i][1];
    ohlc.push([pts[i][0] * 1000, a, Math.max(a, b), Math.min(a, b), b]);
  }
  if (typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
    const el = dashMountEchart(body, p.id);
    if (el) DashCharts.render(el, { type: "candlestick", panel: p, ohlc, fmtUnit });
    return;
  }
  body.innerHTML = dashEmptyHint("需要 ECharts 以渲染 K 线");
}

async function loadRadarPanel(p, body, from, to) {
  const series = await instantQuery(p, body, from, to);
  if (!series) return;
  let items = series.map(s => ({
    name: legendFor(p.targets[0].legend, seriesLabels(s)),
    val: seriesVal2(s)
  }));
  items = dashSortLimit(items.map(it => ({ lbl: it.name, val: it.val, name: it.name })), p, 8)
    .map(it => ({ name: it.lbl || it.name, val: it.val }));
  if (typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
    const el = dashMountEchart(body, p.id);
    if (el) DashCharts.render(el, { type: "radar", panel: p, items, fmtUnit });
    return;
  }
  body.innerHTML = `<div class="dash-bars-h">` + items.map(it =>
    `<div class="dash-bar-item"><div class="dash-bar-lbl">${esc(it.name)}</div><div class="dash-bar-val">${dashFmt(p, it.val)}</div></div>`
  ).join("") + `</div>`;
}

async function loadSankeyPanel(p, body, from, to) {
  const series = await instantQuery(p, body, from, to);
  if (!series) return;
  const items = dashSortLimit(series.map(s => ({
    lbl: legendFor(p.targets[0].legend, seriesLabels(s)),
    val: Math.max(0, seriesVal2(s)),
    labels: seriesLabels(s)
  })), p, 16);
  // Build a simple source→target flow: "source" hub → each series (or src/dst labels).
  const nodes = [], links = [], nodeIdx = new Map();
  const ensure = (name) => {
    if (!nodeIdx.has(name)) { nodeIdx.set(name, nodes.length); nodes.push({ name }); }
    return nodeIdx.get(name);
  };
  items.forEach(it => {
    const src = it.labels.src || it.labels.source || it.labels.from || "来源";
    const dst = it.labels.dst || it.labels.destination || it.labels.to || it.lbl || "目标";
    ensure(src); ensure(dst);
    links.push({ source: src, target: dst, value: it.val || 0.001 });
  });
  if (typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
    const el = dashMountEchart(body, p.id);
    if (el) DashCharts.render(el, { type: "sankey", panel: p, sankey: { nodes, links }, fmtUnit });
    return;
  }
  body.innerHTML = dashEmptyHint("需要 ECharts 以渲染桑基图");
}
// 即时查询公共入口：返回序列数组，出错/无数据时写占位并返回 null。
// 瞬时面板（仪表/饼图/柱状/直方图/雷达/桑基/stat）同样受看板时间选择器管辖：
// 窗口决定服务端的 $__range / $__interval 展开值与求值时刻。不传窗口时服务端
// 退回 1h/now，与旧行为一致。
async function instantQuery(p, body, from, to) {
  const win = (typeof from === "number" && typeof to === "number" && from < to)
    ? { from, to }
    : (typeof dashRange === "function" ? dashRange() : null);
  let res;
  try { res = await fetch(`${API}/dashboards/query-instant`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expr: p.targets[0].expr, datasource: resolveDS(p), vars: panelVars(), from: win ? win.from : 0, to: win ? win.to : 0 }) }).then(r => r.json()); }
  catch (e) { body.innerHTML = `<div class="dash-empty">查询失败</div>`; return null; }
  if (res && res.available === false) { body.innerHTML = `<div class="dash-empty">数据源不可用（${esc(dsLabel(resolveDS(p)))}）</div>`; return null; }
  const series = (res && res.series) || [];
  if (!series.length) { body.innerHTML = dashEmptyHint("无数据"); return null; }
  return series;
}
function seriesVal2(s) { return +(s.Value !== undefined ? s.Value : s.value); }
function seriesLabels(s) { return s.Labels || s.labels || {}; }
// statColor：按阈值给颜色（percent / percentunit / 有量程的按占比；否则中性主色）。
function statColor(v, unit, min, max) {
  let pct = null;
  if (unit === "percent") pct = v;
  else if (unit === "percentunit") pct = v * 100;
  else if (max != null && min != null && max > min) pct = (v - min) / (max - min) * 100;
  if (pct == null) return "var(--accent)";
  return pct >= 90 ? "var(--crit)" : pct >= 75 ? "var(--warn)" : "var(--ok)";
}
async function loadLogsPanel(p, body, from, to) {
  const lim = dashPanelOpt(p).limit > 0 ? dashPanelOpt(p).limit : 200;
  let res;
  try {
    res = await fetch(`${API}/dashboards/query-logs`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expr: p.targets[0].expr, from, to, limit: lim, datasource: resolveDS(p), vars: panelVars() }) }).then(r => r.json());
  } catch (e) { body.innerHTML = `<div class="dash-empty">日志查询失败</div>`; return; }
  if (res && res.available === false) { body.innerHTML = `<div class="dash-empty">该面板需选择一个 <b>Loki</b> 数据源</div>`; return; }
  const lines = (res && res.lines) || [];
  if (!lines.length) { body.innerHTML = `<div class="dash-empty">该范围无日志</div>`; return; }
  body.innerHTML = `<div class="dash-logs">${lines.map(l => {
    const lv = detectLogLevel(l.line || "");
    return `<div class="dash-log-row ${lv}"><span class="dash-log-lvl" aria-hidden="true"></span><span class="dash-log-ts">${fmtLogTs(l.ts_ms)}</span><span class="dash-log-line">${esc(l.line || "")}</span></div>`;
  }).join("")}</div>`;
}
function detectLogLevel(line) {
  const s = String(line);
  if (/\b(ERROR|FATAL|CRIT(ICAL)?|ERR)\b/i.test(s) || /level[=:]?\s*error/i.test(s)) return "crit";
  if (/\b(WARN(ING)?)\b/i.test(s) || /level[=:]?\s*warn/i.test(s)) return "warn";
  if (/\b(DEBUG|TRACE)\b/i.test(s)) return "debug";
  if (/\b(INFO)\b/i.test(s) || /level[=:]?\s*info/i.test(s)) return "info";
  return "";
}
function fmtLogTs(ms) {
  if (!ms) return "";
  const d = new Date(ms);
  const p2 = n => String(n).padStart(2, "0");
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}
async function loadTimeseriesPanel(p, body, from, to, panelLoad) {
  const collected = []; // { labels, legendFmt, points, name, kind, band }
  let naOff = false;
  let metaMsg = "";
  const st = panelTrendState(p.id);
  const mode = panelTrendMode(st);
  const useForecastAPI = !!mode;
  const alive = () => !panelLoad || panelLoad.isCurrent();
  for (const t of p.targets) {
    if (!alive()) return;
    let res;
    try {
      const payload = { expr: t.expr, from, to, datasource: resolveDS(p), vars: panelVars() };
      if (useForecastAPI) {
        payload.mode = mode;
        if (st.horizonSec > 0) payload.horizon_sec = st.horizonSec;
        if (st.method && st.method !== "auto") payload.method = st.method;
        else payload.method = st.method || "auto";
      }
      const url = useForecastAPI ? `${API}/dashboards/query-forecast` : `${API}/dashboards/query`;
      const fetchOpts = { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) };
      if (panelLoad && panelLoad.signal) fetchOpts.signal = panelLoad.signal;
      res = await fetch(url, fetchOpts).then(r => r.json());
    } catch (e) {
      if (e && (e.name === "AbortError" || /aborted/i.test(String(e.message || e)))) return;
      continue;
    }
    if (!alive()) return;
    if (res && res.available === false) { naOff = true; break; }
    if (res && res.meta && res.meta.message) metaMsg = res.meta.message;
    for (const s of (res && res.series || [])) {
      if (collected.length >= 32) break;
      collected.push({
        labels: s.labels || {}, legendFmt: t.legend, points: s.points || [],
        kind: s.kind || "history", band: s.band || null,
        name: s.name || ""
      });
    }
    // Multi-target panels (CPU/mem/net combo): forecast each expr; cap total series.
    if (useForecastAPI && collected.length >= 32) break;
  }
  if (!alive()) return;
  if (naOff) {
    dashDisposePanelChart(p.id);
    body.innerHTML = `<div class="dash-empty">数据源不可用（${esc(dsLabel(resolveDS(p)))}）—— 请在「数据源」配置或改选面板数据源</div>`;
    return;
  }
  if (!collected.length) {
    dashDisposePanelChart(p.id);
    body.innerHTML = dashEmptyHint(metaMsg || "该范围无数据", p);
    return;
  }
  const histOnly = collected.filter(c => !c.kind || c.kind === "history");
  const labels = dashLegends(histOnly.length ? histOnly : collected);
  let li = 0;
  collected.forEach((c) => {
    if (!c.name) {
      if (!c.kind || c.kind === "history") c.name = labels[li++] || "系列";
      else c.name = c.kind === "forecast" ? "预测" : (c.kind === "compare_yoy" ? "同比" : "环比");
    }
  });
  const hasFC = collected.some(c => c.kind === "forecast");
  const forecastOn = !!(st.forecast && hasFC);
  const metaBadge = (forecastOn && (metaMsg || hasFC))
    ? `<div class="dash-fc-meta">${metaMsg ? esc(metaMsg) : "左=历史 · 中轴=现在 · 右=预测（虚线）"}</div>` : "";
  // 预测关闭时不传 nowTs，避免图表预留未来空白轴
  let nowTs = 0;
  if (forecastOn) {
    nowTs = to;
    collected.forEach(c => {
      if ((!c.kind || c.kind === "history") && (c.points || []).length) {
        const last = +c.points[c.points.length - 1][0];
        if (last > nowTs) nowTs = last;
      }
    });
  }
  if (typeof DashCharts === "undefined" || typeof echarts === "undefined") {
    // Fallback to Canvas if ECharts failed to load
    const defs = [], tsMap = new Map();
    collected.forEach((c, i) => {
      const key = "s" + i;
      const col = c.kind === "forecast" ? dashColorAt(p, 0)
        : (c.kind === "compare_yoy" ? "#a78bfa" : (c.kind === "compare_pop" ? "#94a3b8" : dashColorAt(p, i)));
      defs.push({
        key, label: c.name || labels[i], color: col, fmt: v => dashFmt(p, v),
        dashed: c.kind === "forecast", kind: c.kind || "history"
      });
      for (const pt of c.points) {
        const ts = Math.round(pt[0]);
        let row = tsMap.get(ts); if (!row) { row = { timestamp: ts }; tsMap.set(ts, row); }
        row[key] = pt[1];
      }
    });
    const samples = [...tsMap.values()].sort((a, b) => a.timestamp - b.timestamp);
    const cid = "dashCanvas_" + p.id;
    body.innerHTML = `${metaBadge}<div class="chart-wrap"><canvas id="${cid}"></canvas></div>`;
    const drawTs = () => {
      if (!document.getElementById(cid)) return;
      let chartH = panelBodyH(body);
      if (chartH < 120) chartH = dashRowHeight(p.grid.h || 8);
      const bodyH = Math.max(80, panelBodyH(body) || chartH);
      const args = [cid, samples, defs, p.min != null ? +p.min : null, p.max != null ? +p.max : null, { cssH: bodyH, legendMode: "dash", title: "", nowTs: nowTs || 0 }];
      DASH_CHART_ARGS[p.id] = args;
      createChart.apply(null, args);
    };
    requestAnimationFrame(drawTs);
    return;
  }
  const el = dashMountEchart(body, p.id);
  if (!el) return;
  body.querySelectorAll(":scope > .dash-fc-meta").forEach(n => n.remove());
  if (metaBadge) el.insertAdjacentHTML("beforebegin", metaBadge);
  DashCharts.render(el, { type: "timeseries", panel: p, series: collected, fmtUnit, nowTs: nowTs || 0 });
}
// dashRowHeight：按 gridPos 行数反推面板正文可用高度（网格行高 24 + 行间距 8，扣面板头+内边距 ~52）。
function dashRowHeight(h) { const n = Math.max(3, Math.min(48, h || 8)); return n * 24 + (n - 1) * 8 - 52; }
// loadInstantPanel 处理 bargauge（横向条）与 table。
async function loadSQLTablePanel(p, body) {
  const t = (p.targets && p.targets[0]) || {};
  let res;
  try {
    res = await fetch(`${API}/dashboards/query-sql`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ expr: t.expr, datasource: resolveDS(p), vars: panelVars(), limit: 200 })
    }).then(r => r.json());
  } catch (e) {
    body.innerHTML = `<div class="dash-empty">SQL 查询失败</div>`;
    return;
  }
  if (res && res.available === false) {
    body.innerHTML = `<div class="dash-empty">数据源不可用（${esc(dsLabel(resolveDS(p)))}）</div>`;
    return;
  }
  if (res && res.error) {
    body.innerHTML = `<div class="dash-empty">${esc(res.error)}</div>`;
    return;
  }
  const cols = Array.isArray(res.columns) ? res.columns : [];
  const rows = Array.isArray(res.rows) ? res.rows : [];
  if (!cols.length || !rows.length) {
    body.innerHTML = dashEmptyHint("无数据");
    return;
  }
  const lim = (dashPanelOpt(p).limit > 0 ? dashPanelOpt(p).limit : 200);
  renderDashDataTable(body, cols, rows.slice(0, lim), p);
}
function renderDashDataTable(body, cols, rows, p) {
  const sortState = { col: null, dir: "desc" };
  const paint = () => {
    let data = rows.slice();
    if (sortState.col != null) {
      const c = cols[sortState.col];
      data.sort((a, b) => {
        const av = a[c], bv = b[c];
        const an = parseFloat(av), bn = parseFloat(bv);
        let cmp = 0;
        if (!isNaN(an) && !isNaN(bn)) cmp = an - bn;
        else cmp = String(av == null ? "" : av).localeCompare(String(bv == null ? "" : bv), undefined, { numeric: true });
        return sortState.dir === "asc" ? cmp : -cmp;
      });
    }
    body.innerHTML = `<div class="dash-table-wrap"><table class="dash-table"><thead><tr>` +
      cols.map((c, i) => `<th data-col="${i}" class="dash-th-sort${sortState.col === i ? " active" : ""}">${esc(c)}${sortState.col === i ? (sortState.dir === "asc" ? " ↑" : " ↓") : ""}</th>`).join("") +
      `</tr></thead><tbody>` +
      data.map(r => {
        const cells = cols.map(c => {
          const raw = r[c];
          const n = parseFloat(raw);
          if (raw !== "" && raw != null && !isNaN(n) && String(raw).trim() !== "" && /^-?\d/.test(String(raw).trim())) {
            const col = dashThresholdColor(p, n);
            return `<td class="num" style="color:${col}">${esc(dashFmt(p, n))}</td>`;
          }
          return `<td>${esc(raw == null ? "" : String(raw))}</td>`;
        }).join("");
        return `<tr>${cells}</tr>`;
      }).join("") + `</tbody></table></div>`;
    body.querySelectorAll(".dash-th-sort").forEach(th => {
      th.addEventListener("click", () => {
        const i = +th.dataset.col;
        if (sortState.col === i) sortState.dir = sortState.dir === "asc" ? "desc" : "asc";
        else { sortState.col = i; sortState.dir = "desc"; }
        paint();
      });
    });
  };
  paint();
}

function isSQLDashDS(id) {
  const d = dsById(id);
  return d && (d.type === "postgres" || d.type === "postgresql" || d.type === "mysql");
}

async function loadInstantPanel(p, body, from, to) {
  if (p.type === "table" && isSQLDashDS(resolveDS(p))) {
    await loadSQLTablePanel(p, body);
    return;
  }
  const series = await instantQuery(p, body, from, to);
  if (!series) return;
  if (p.type === "bargauge") {
    const items = dashSortLimit(series.map(s => ({
      lbl: legendFor(p.targets[0].legend, seriesLabels(s)),
      val: seriesVal2(s)
    })), p, 16);
    if (typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
      const el = dashMountEchart(body, p.id);
      if (el) DashCharts.render(el, { type: "bargauge", panel: p, items, fmtUnit });
      return;
    }
    const min = p.min != null ? p.min : 0;
    const max = p.max != null ? p.max : (p.unit === "percent" ? 100 : (p.unit === "percentunit" ? 1 : autoMax(series)));
    body.innerHTML = `<div class="dash-bars-h">` + items.map(it => {
      const pct = max > min ? Math.max(0, Math.min(100, (it.val - min) / (max - min) * 100)) : 0;
      const col = dashThresholdColor(p, it.val, min, max);
      return `<div class="dash-bar-item"><div class="dash-bar-lbl" title="${esc(it.lbl)}">${esc(it.lbl)}</div><div class="dash-bar-track"><div class="dash-bar-fill" style="width:${pct}%; background:${col}"></div></div><div class="dash-bar-val">${dashFmt(p, it.val)}</div></div>`;
    }).join("") + `</div>`;
  } else { // table (PromQL instant)
    const items = dashSortLimit(series.map(s => ({
      lbl: legendFor(p.targets[0].legend, seriesLabels(s)),
      val: seriesVal2(s)
    })), p, 200);
    const cols = ["序列", "值"];
    const rows = items.map(it => ({ "序列": it.lbl, "值": it.val }));
    renderDashDataTable(body, cols, rows, p);
  }
}
// loadStatPanel：大数值（取区间最后一点）+ 阈值配色 + 迷你趋势 sparkline + 说明。
async function loadStatPanel(p, body, from, to, panelLoad) {
  const t = p.targets[0];
  const st = panelTrendState(p.id);
  const mode = panelTrendMode(st);
  const alive = () => !panelLoad || panelLoad.isCurrent();
  let res;
  try {
    const payload = { expr: t.expr, from, to, datasource: resolveDS(p), vars: panelVars() };
    let url = `${API}/dashboards/query`;
    if (mode) {
      url = `${API}/dashboards/query-forecast`;
      payload.mode = mode;
      if (st.horizonSec > 0) payload.horizon_sec = st.horizonSec;
      payload.method = st.method || "auto";
    }
    const fetchOpts = { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) };
    if (panelLoad && panelLoad.signal) fetchOpts.signal = panelLoad.signal;
    res = await fetch(url, fetchOpts).then(r => r.json());
  } catch (e) {
    if (e && (e.name === "AbortError" || /aborted/i.test(String(e.message || e)))) return;
    body.innerHTML = `<div class="dash-empty">查询失败</div>`; return;
  }
  if (!alive()) return;
  if (res && res.available === false) { body.innerHTML = `<div class="dash-empty">数据源不可用（${esc(dsLabel(resolveDS(p)))}）</div>`; return; }
  const series = (res && res.series) || [];
  if (!series.length) { body.innerHTML = dashEmptyHint((res && res.meta && res.meta.message) || "无数据"); return; }
  const s0 = series.find(s => !s.kind || s.kind === "history") || series[0];
  const pts = s0.points || [];
  const val = pts.length ? pts[pts.length - 1][1] : 0;
  const col = dashThresholdColor(p, val);
  let lbl = legendFor(t.legend, s0.labels || {});
  if (!lbl || /^value$/i.test(lbl) || /^aiops_/i.test(lbl)) lbl = "";
  const fc = series.find(s => s.kind === "forecast");
  const cmp = series.find(s => s.kind === "compare_pop" || s.kind === "compare_yoy");
  let deltaHTML = "";
  if (cmp && (cmp.points || []).length && pts.length) {
    const avg = arr => arr.reduce((a, b) => a + b, 0) / arr.length;
    const curAvg = avg(pts.map(x => +x[1]));
    const prevAvg = avg(cmp.points.map(x => +x[1]));
    if (Math.abs(prevAvg) > 1e-12) {
      const pct = (curAvg - prevAvg) / Math.abs(prevAvg) * 100;
      const up = pct >= 0;
      const tag = cmp.kind === "compare_yoy" ? "同比" : "环比";
      deltaHTML = `<div class="dash-stat-delta ${up ? "up" : "down"}">${tag} ${up ? "↑" : "↓"} ${Math.abs(pct).toFixed(1)}%</div>`;
    }
  }
  const metaMsg = res && res.meta && !res.meta.ok && res.meta.message ? `<div class="dash-fc-meta">${esc(res.meta.message)}</div>` : "";
  body.innerHTML = `${metaMsg}<div class="dash-stat2">
      <div class="dash-stat-num" style="color:${col}">${dashFmt(p, +val)}</div>
      ${deltaHTML}
      ${lbl ? `<div class="dash-stat-cap">${esc(lbl)}</div>` : ""}
      <div class="dash-stat-spark" id="dashStatSpark_${p.id}"></div>
    </div>`;
  if (pts.length > 1 && typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
    const sparkEl = document.getElementById("dashStatSpark_" + p.id);
    if (sparkEl) {
      DASH_ECHART_ELS[p.id] = sparkEl;
      sparkEl.classList.add("dash-echart", "dash-echart-spark");
      DashCharts.render(sparkEl, {
        type: "stat", panel: p, points: pts, fmtUnit,
        forecastPoints: fc ? (fc.points || []) : []
      });
    }
  } else if (pts.length > 1) {
    const spark = svgSparkline(pts.map(pt => pt[1]), col);
    const el = document.getElementById("dashStatSpark_" + p.id);
    if (el) el.innerHTML = spark;
  }
}
// loadGaugePanel：ECharts 仪表；多序列时网格分格。
async function loadGaugePanel(p, body, from, to) {
  const series = await instantQuery(p, body, from, to);
  if (!series) return;
  let items = series.map(s => {
    let lbl = legendFor(p.targets[0].legend, seriesLabels(s));
    // 聚合指标无 instance 时常见 "value"，仪表标题区不展示
    if (!lbl || /^value$/i.test(lbl) || /^aiops_/i.test(lbl)) lbl = "";
    return { val: seriesVal2(s), lbl };
  });
  items = dashSortLimit(items, p, 9);
  if (typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
    if (items.length <= 1) {
      const el = dashMountEchart(body, p.id);
      if (el) DashCharts.render(el, { type: "gauge", panel: p, items, fmtUnit });
      return;
    }
    body.innerHTML = `<div class="dash-gauges dash-gauges-echart">` + items.map((it, i) =>
      `<div class="dash-gauge-item"><div class="dash-echart dash-echart-gauge" id="dashGauge_${p.id}_${i}"></div></div>`
    ).join("") + `</div>`;
    items.forEach((it, i) => {
      const el = document.getElementById(`dashGauge_${p.id}_${i}`);
      if (!el) return;
      if (i === 0) DASH_ECHART_ELS[p.id] = el;
      DashCharts.render(el, { type: "gauge", panel: p, items: [it], fmtUnit });
    });
    return;
  }
  const min = p.min != null ? p.min : 0;
  const max = p.max != null ? p.max : (p.unit === "percent" ? 100 : (p.unit === "percentunit" ? 1 : autoMax(series)));
  body.innerHTML = `<div class="dash-gauges">` + items.map(it => {
    const pct = max > min ? (it.val - min) / (max - min) * 100 : 0;
    const col = dashThresholdColor(p, it.val, min, max);
    return `<div class="dash-gauge-item">${svgGauge(pct, dashFmt(p, it.val), col)}<div class="dash-gauge-lbl" title="${esc(it.lbl)}">${esc(it.lbl)}</div></div>`;
  }).join("") + `</div>`;
}
async function loadPiePanel(p, body, from, to) {
  const series = await instantQuery(p, body, from, to);
  if (!series) return;
  let items = series
    .map((s, i) => ({ val: Math.max(0, seriesVal2(s)), lbl: legendFor(p.targets[0].legend, seriesLabels(s)), col: dashColorAt(p, i) }))
    .filter(it => it.val > 0);
  items = dashSortLimit(items, p, 12);
  if (!items.length) { body.innerHTML = dashEmptyHint("无数据"); return; }
  if (typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
    const el = dashMountEchart(body, p.id);
    if (el) DashCharts.render(el, { type: "piechart", panel: p, items, fmtUnit });
    return;
  }
  const total = items.reduce((a, b) => a + b.val, 0) || 1;
  const centerVal = dashFmt(p, total);
  const legend = items.map(it => {
    const pct = (it.val / total * 100);
    const pctStr = pct >= 10 ? pct.toFixed(0) : pct.toFixed(1);
    return `<div class="dash-pie-li"><span class="dash-pie-dot" style="background:${it.col}"></span>` +
      `<span class="dash-pie-name" title="${esc(it.lbl)}">${esc(it.lbl)}</span>` +
      `<span class="dash-pie-val">${dashFmt(p, it.val)}</span>` +
      `<span class="dash-pie-pct">${pctStr}%</span></div>`;
  }).join("");
  body.innerHTML = `<div class="dash-pie"><div class="dash-pie-chart">${svgDonut(items, total, centerVal)}</div>` +
    `<div class="dash-pie-legend">${legend}</div></div>`;
}
async function loadBarPanel(p, body, from, to, panelLoad) {
  // 预测/环比/同比开启时：走区间时序柱状（左历史右预测），与曲线面板同一套 query-forecast
  const st = panelTrendState(p.id);
  if (panelTrendMode(st)) {
    const range = (typeof from === "number" && typeof to === "number")
      ? { from, to }
      : dashRange();
    const p2 = Object.assign({}, p, {
      type: "timeseries",
      options: Object.assign({}, p.options || {}, { chart_style: "bar" }),
    });
    await loadTimeseriesPanel(p2, body, range.from, range.to, panelLoad);
    return;
  }
  const series = await instantQuery(p, body, from, to);
  if (!series) return;
  let items = series.map((s, i) => ({ val: seriesVal2(s), lbl: legendFor(p.targets[0].legend, seriesLabels(s)), col: dashColorAt(p, i) }));
  items = dashSortLimit(items, p, 16);
  if (typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
    const el = dashMountEchart(body, p.id);
    if (el) DashCharts.render(el, { type: "barchart", panel: p, items, fmtUnit });
    return;
  }
  const mx = Math.max(...items.map(it => it.val), 0) || 1;
  body.innerHTML = `<div class="dash-bars">` + items.map(it => {
    const h = Math.max(2, it.val / mx * 100);
    return `<div class="dash-barcol" title="${esc(it.lbl)}：${dashFmt(p, it.val)}">
        <div class="dash-barcol-v">${dashFmt(p, it.val)}</div>
        <div class="dash-barcol-track"><div class="dash-barcol-bar" style="height:${h}%; background:${it.col}"></div></div>
        <div class="dash-barcol-lbl">${esc(it.lbl)}</div></div>`;
  }).join("") + `</div>`;
}
/* ---------- SVG 组件 ---------- */
// svgSparkline：迷你趋势线（填满宽度，用于 stat 背景趋势）。
function svgSparkline(vals, color) {
  const n = vals.length; if (n < 2) return "";
  const mn = Math.min(...vals), mx = Math.max(...vals), rng = (mx - mn) || 1, W = 100, H = 28;
  const pts = vals.map((v, i) => `${(i / (n - 1) * W).toFixed(2)},${(H - (v - mn) / rng * H).toFixed(2)}`).join(" ");
  return `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" class="spark-svg"><polygon points="0,${H} ${pts} ${W},${H}" fill="${color}" opacity="0.12"/><polyline points="${pts}" fill="none" stroke="${color}" stroke-width="1.5" vector-effect="non-scaling-stroke"/></svg>`;
}
// svgGauge：圆环径向进度仪表 + 中心数值。
function svgGauge(pct, valueText, color) {
  const r = 42, C = 2 * Math.PI * r;
  const off = C * (1 - Math.max(0, Math.min(1, pct / 100)));
  return `<svg viewBox="0 0 100 100" class="gauge-svg">
    <circle cx="50" cy="50" r="${r}" fill="none" stroke="var(--line2)" stroke-width="9"/>
    <circle cx="50" cy="50" r="${r}" fill="none" stroke="${color}" stroke-width="9" stroke-linecap="round" stroke-dasharray="${C.toFixed(1)}" stroke-dashoffset="${off.toFixed(1)}" transform="rotate(-90 50 50)"/>
    <text x="50" y="50" text-anchor="middle" dominant-baseline="central" class="gauge-txt" fill="var(--txt)">${esc(valueText)}</text>
  </svg>`;
}
// svgDonut：环形图（各片按占比，rotate -90 从顶部起）。更大半径 + 更粗环 + 环心总计，
// 减少留白；带底色轨道让占比一目了然。centerText 为可选环心文案（如总计值）。
function svgDonut(items, total, centerText) {
  const r = 38, sw = 20, C = 2 * Math.PI * r; // 外径 48、内径 28，居中充满 100×100 视图
  let off = 0;
  const track = `<circle cx="50" cy="50" r="${r}" fill="none" stroke="var(--line2)" stroke-width="${sw}" opacity="0.35"/>`;
  const segs = items.filter(it => it.val > 0).map(it => {
    const frac = it.val / total;
    const seg = `<circle cx="50" cy="50" r="${r}" fill="none" stroke="${it.col}" stroke-width="${sw}" stroke-dasharray="${(frac * C).toFixed(2)} ${(C - frac * C).toFixed(2)}" stroke-dashoffset="${(-off * C).toFixed(2)}" transform="rotate(-90 50 50)"/>`;
    off += frac;
    return seg;
  }).join("");
  const center = centerText ? `<text x="50" y="50" text-anchor="middle" dominant-baseline="central" class="donut-center" fill="var(--txt)">${esc(String(centerText))}</text>` : "";
  return `<svg viewBox="0 0 100 100" class="pie-svg">${track}${segs}${center}</svg>`;
}
// rangeSeries：区间查询公共入口（取第一个 target 的多序列），出错/无数据写占位并返回 null。
async function rangeSeries(p, body, from, to) {
  let res;
  try { res = await fetch(`${API}/dashboards/query`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expr: p.targets[0].expr, from, to, datasource: resolveDS(p), vars: panelVars() }) }).then(r => r.json()); }
  catch (e) { body.innerHTML = `<div class="dash-empty">查询失败</div>`; return null; }
  if (res && res.available === false) { body.innerHTML = `<div class="dash-empty">数据源不可用（${esc(dsLabel(resolveDS(p)))}）</div>`; return null; }
  const series = (res && res.series) || [];
  if (!series.length) { body.innerHTML = dashEmptyHint("该范围无数据"); return null; }
  return series;
}
async function loadHistogramPanel(p, body, from, to) {
  const series = await instantQuery(p, body, from, to);
  if (!series) return;
  const vals = series.map(seriesVal2).filter(v => isFinite(v));
  if (!vals.length) { body.innerHTML = dashEmptyHint("无数据"); return; }
  const mn = Math.min(...vals), mx = Math.max(...vals);
  const binsN = Math.min(16, Math.max(4, Math.round(Math.sqrt(vals.length)) + 2));
  const w = (mx - mn) / binsN || 1;
  const counts = new Array(binsN).fill(0);
  vals.forEach(v => { let i = Math.floor((v - mn) / w); if (i >= binsN) i = binsN - 1; if (i < 0) i = 0; counts[i]++; });
  const bins = counts.map((c, i) => ({ lbl: fmtShort(mn + i * w), count: c }));
  if (typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
    const el = dashMountEchart(body, p.id);
    if (el) DashCharts.render(el, { type: "histogram", panel: p, bins, fmtUnit });
    return;
  }
  const mxc = Math.max(...counts, 1);
  body.innerHTML = `<div class="dash-bars">` + counts.map((c, i) => {
    const lo = mn + i * w;
    return `<div class="dash-barcol" title="${dashFmt(p, lo)} ~ ${dashFmt(p, lo + w)}：${c}">
        <div class="dash-barcol-v">${c}</div>
        <div class="dash-barcol-track"><div class="dash-barcol-bar" style="height:${Math.max(1, c / mxc * 100)}%; background:${dashColorAt(p, i)}"></div></div>
        <div class="dash-barcol-lbl">${fmtShort(lo)}</div></div>`;
  }).join("") + `</div>`;
}
async function loadStateTimelinePanel(p, body, from, to) {
  const series = await rangeSeries(p, body, from, to);
  if (!series) return;
  const labels = dashLegends(series.map(s => ({ labels: s.labels || {}, legendFmt: p.targets[0].legend })));
  const lim = dashPanelOpt(p).limit > 0 ? dashPanelOpt(p).limit : 16;
  body.innerHTML = `<div class="dash-states">` + series.slice(0, lim).map((s, idx) => {
    const pts = s.points || [];
    const segs = pts.map(pt => `<span class="dash-state-seg" style="background:${stateColor(pt[1], p)}" title="${fmtLogTs(pt[0] * 1000)} · ${dashFmt(p, pt[1])}"></span>`).join("");
    return `<div class="dash-state-row"><div class="dash-state-lbl" title="${esc(labels[idx])}">${esc(labels[idx])}</div><div class="dash-state-track">${segs}</div></div>`;
  }).join("") + `</div>`;
}
function stateColor(v, pOrUnit) {
  if (pOrUnit && typeof pOrUnit === "object") return dashThresholdColor(pOrUnit, v);
  const unit = pOrUnit;
  if (unit === "percent" || unit === "percentunit") return statColor(v, unit);
  return v <= 0 ? "var(--crit)" : "var(--ok)";
}
async function loadHeatmapPanel(p, body, from, to) {
  const series = await rangeSeries(p, body, from, to);
  if (!series) return;
  const lim = dashPanelOpt(p).limit > 0 ? dashPanelOpt(p).limit : 24;
  const rows = series.slice(0, lim);
  const labels = dashLegends(rows.map(s => ({ labels: s.labels || {}, legendFmt: p.targets[0].legend })));
  if (typeof DashCharts !== "undefined" && typeof echarts !== "undefined") {
    // Build shared time buckets from first series downsample
    const nBuckets = 40;
    const allTs = new Set();
    rows.forEach(s => downsample(s.points || [], nBuckets).forEach(pt => allTs.add(Math.round(pt[0]))));
    const xTs = [...allTs].sort((a, b) => a - b);
    const xLabels = xTs.map(ts => fmtLogTs(ts * 1000));
    const data = [];
    rows.forEach((s, yi) => {
      const map = new Map((s.points || []).map(pt => [Math.round(pt[0]), pt[1]]));
      xTs.forEach((ts, xi) => {
        // nearest sample
        let best = null, bestD = Infinity;
        for (const [k, v] of map) {
          const d = Math.abs(k - ts);
          if (d < bestD) { bestD = d; best = v; }
        }
        if (best != null) data.push([xi, yi, best]);
      });
    });
    const el = dashMountEchart(body, p.id);
    if (el) DashCharts.render(el, { type: "heatmap", panel: p, matrix: { xLabels, yLabels: labels, data }, fmtUnit });
    return;
  }
  let mn = Infinity, mx = -Infinity;
  rows.forEach(s => (s.points || []).forEach(pt => { if (pt[1] < mn) mn = pt[1]; if (pt[1] > mx) mx = pt[1]; }));
  const rng = (mx - mn) || 1;
  body.innerHTML = `<div class="dash-heatmap">` + rows.map((s, idx) => {
    const pts = downsample(s.points || [], 80);
    const cells = pts.map(pt => `<span class="dash-heat-cell" style="background:${heatColor((pt[1] - mn) / rng)}" title="${fmtLogTs(pt[0] * 1000)} · ${dashFmt(p, pt[1])}"></span>`).join("");
    return `<div class="dash-heat-row"><div class="dash-heat-lbl" title="${esc(labels[idx])}">${esc(labels[idx])}</div><div class="dash-heat-cells">${cells}</div></div>`;
  }).join("") + `</div>`;
}
function heatColor(t) { t = Math.max(0, Math.min(1, t)); const hue = (1 - t) * 220; return `hsl(${hue.toFixed(0)}, 72%, ${(46 + t * 8).toFixed(0)}%)`; }
function downsample(pts, n) { if (pts.length <= n) return pts; const step = pts.length / n, out = []; for (let i = 0; i < n; i++) out.push(pts[Math.floor(i * step)]); return out; }
// loadAlertListPanel：读平台当前告警（与告警/事件闭环打通），按级别上色。可选 label 过滤（面板查询里填关键词）。
async function loadAlertListPanel(p, body) {
  let alerts;
  try { alerts = await fetch(`${API}/alerts`).then(r => r.json()); } catch (e) { body.innerHTML = `<div class="dash-empty">加载失败</div>`; return; }
  alerts = Array.isArray(alerts) ? alerts : [];
  const kw = ((p.targets && p.targets[0] && p.targets[0].expr) || "").trim().toLowerCase();
  if (kw) alerts = alerts.filter(a => JSON.stringify(a).toLowerCase().includes(kw));
  if (!alerts.length) { body.innerHTML = `<div class="dash-alerts-ok">✓ 当前无告警</div>`; return; }
  const rank = { critical: 0, warning: 1, info: 2 };
  const sort = (dashPanelOpt(p).sort || "desc").toLowerCase();
  alerts.sort((a, b) => {
    const cmp = (rank[a.level] ?? 3) - (rank[b.level] ?? 3);
    return sort === "asc" ? -cmp : cmp;
  });
  const lim = dashPanelOpt(p).limit > 0 ? dashPanelOpt(p).limit : 200;
  body.innerHTML = `<div class="dash-alerts">` + alerts.slice(0, lim).map(a => {
    const lv = a.level === "critical" ? "crit" : a.level === "warning" ? "warn" : "info";
    return `<div class="dash-alert-row ${lv}"><span class="dash-alert-dot"></span><span class="dash-alert-msg" title="${esc(a.message || "")}">${esc(a.message || a.type || "告警")}</span>${a.hostname ? `<span class="dash-alert-host">${esc(a.hostname)}</span>` : ""}</div>`;
  }).join("") + `</div>`;
}
function isHostIDLabel(v) {
  return typeof v === "string" && /^[a-f0-9]{16,64}$/i.test(v.trim());
}
// 去掉图例文本里的主机 ID 段，并折叠多余分隔符（AI 常写 {{category}} - {{host}} - {{instance}}）。
function cleanLegendText(s) {
  if (!s) return s;
  let out = String(s).replace(/\b[a-f0-9]{16,64}\b/gi, "");
  out = out.replace(/\s*[-–—|/]+\s*/g, " · ");
  out = out.replace(/(\s*·\s*)+/g, " · ").replace(/^\s*·\s*|\s*·\s*$/g, "").trim();
  return out || String(s).trim();
}
function humanSeriesLabel(labels) {
  labels = labels || {};
  const cat = labels.category || "";
  const inst = labels.instance || labels.hostname || labels.node || labels.ident || "";
  const parts = [];
  if (cat) parts.push(cat);
  if (inst && !isHostIDLabel(inst)) parts.push(inst);
  if (parts.length) return parts.join(" · ");
  for (const k of Object.keys(labels)) {
    if (k === "__name__" || k === "host") continue;
    const v = labels[k];
    if (v && !isHostIDLabel(v)) return v;
  }
  return labels.__name__ || "value";
}
function legendFor(fmt, labels) {
  labels = labels || {};
  if (fmt && fmt.trim()) {
    // 展开前先丢掉会变成主机 ID 的 {{host}}
    let f = fmt;
    if (isHostIDLabel(labels.host || "")) {
      f = f.replace(/\{\{\s*host\s*\}\}/gi, "");
    }
    const raw = f.replace(/\{\{\s*(\w+)\s*\}\}/g, (m, k) => {
      const v = labels[k];
      if (v === undefined || v === null) return "";
      if (k === "host" && isHostIDLabel(v)) return "";
      return v;
    });
    return cleanLegendText(raw) || humanSeriesLabel(labels);
  }
  return humanSeriesLabel(labels);
}
// dashLegends：多序列图例去重可辨。若各序列图例已互不相同则原样用；否则改用「序列之间取值不同的
// 标签」重建（如 state / mountpoint / device），避免像网络连接数那样 8 条都显示同一个 instance。
function dashLegends(collected) {
  const raw = collected.map(c => legendFor(c.legendFmt, c.labels));
  if (new Set(raw).size === raw.length) return raw.map(cleanLegendText); // 已可区分
  const prefer = ["instance", "hostname", "category", "job", "device", "mountpoint", "path", "state", "mode", "name"];
  const keys = new Set();
  collected.forEach(c => Object.keys(c.labels || {}).forEach(k => {
    if (k === "__name__" || k === "host") return; // 永不把主机 ID 拼进图例
    keys.add(k);
  }));
  const varying = prefer.filter(k => keys.has(k) && new Set(collected.map(c => (c.labels || {})[k] || "")).size > 1);
  const extra = [...keys].filter(k => !prefer.includes(k) && new Set(collected.map(c => (c.labels || {})[k] || "")).size > 1);
  const useKeys = varying.length ? varying : extra;
  return collected.map((c, i) => {
    if (useKeys.length) {
      const lbl = useKeys.map(k => (c.labels || {})[k]).filter(v => v && !isHostIDLabel(v)).join(" · ");
      if (lbl) return lbl;
    }
    return humanSeriesLabel(c.labels) || ((c.labels || {}).__name__ || raw[i] || "series") + " #" + (i + 1);
  });
}
function autoMax(series) {
  let m = 0;
  for (const s of series) { const v = +(s.Value !== undefined ? s.Value : s.value); if (v > m) m = v; }
  return m > 0 ? m * 1.1 : 1;
}

/* ---------- 单位格式化 ---------- */
function fmtShort(v) {
  const a = Math.abs(v);
  if (a >= 1e12) return (v / 1e12).toFixed(2) + "T";
  if (a >= 1e9) return (v / 1e9).toFixed(2) + "G";
  if (a >= 1e6) return (v / 1e6).toFixed(2) + "M";
  if (a >= 1e3) return (v / 1e3).toFixed(2) + "K";
  return (Number.isInteger(v) ? v : v.toFixed(2)) + "";
}
function fmtBytes(v) {
  const a = Math.abs(v); const u = ["B", "KB", "MB", "GB", "TB", "PB"]; let i = 0; let n = v;
  while (Math.abs(n) >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return n.toFixed(i ? 2 : 0) + u[i];
}
// fmtDuration：秒 → 人类可读时长。>=1天显示 天+小时（运行时间等长时长换算为天），
// 分钟级显示 分+秒，亚秒显示毫秒。
function fmtDuration(v) {
  const neg = v < 0 ? "-" : "";
  const a = Math.abs(v);
  if (a < 1) return neg + (a * 1000).toFixed(0) + "ms";
  if (a < 60) return neg + (a < 10 ? a.toFixed(1) : a.toFixed(0)) + "s";
  if (a < 3600) { const m = Math.floor(a / 60), s = Math.round(a % 60); return neg + m + "m" + (s ? " " + s + "s" : ""); }
  if (a < 86400) { const h = Math.floor(a / 3600), m = Math.round((a % 3600) / 60); return neg + h + "h" + (m ? " " + m + "m" : ""); }
  const d = Math.floor(a / 86400), h = Math.round((a % 86400) / 3600); return neg + d + "天" + (h ? " " + h + "h" : "");
}
function fmtUnit(v, unit, decimals) {
  if (v === undefined || v === null || isNaN(v)) return "-";
  const d = (decimals != null && decimals !== "" && !isNaN(+decimals)) ? Math.max(0, Math.min(10, +decimals)) : null;
  const fixed = (n, def) => (d != null ? (+n).toFixed(d) : (def != null ? (+n).toFixed(def) : fmtShort(n)));
  switch (unit) {
    case "none": return d != null ? (+v).toFixed(d) : String(v);
    case "percent": return fixed(v, 1) + "%";
    case "percentunit": return fixed(v * 100, 1) + "%";
    case "bytes": return fmtBytes(v);
    case "binBps": case "Bps": return fmtBytes(v) + "/s";
    case "s": case "seconds": case "duration": return fmtDuration(v);
    case "ms": return v >= 1000 ? fmtDuration(v / 1000) : fixed(v, 0) + "ms";
    case "reqps": return (d != null ? (+v).toFixed(d) : fmtShort(v)) + "/s";
    case "cores": return fixed(v, 2) + " cores";
    case "short": return d != null ? (+v).toFixed(d) : fmtShort(v);
    default: return d != null ? (+v).toFixed(d) : fmtShort(v);
  }
}

/* ---------- resize 重绘 ---------- */
let DASH_RESIZE_T = null;
let DASH_LAST_BP = typeof dashBreakpoint === "function" ? dashBreakpoint() : "d";
window.addEventListener("resize", () => {
  const v = document.getElementById("view-dashboards");
  if (!v || !v.classList.contains("active") || !CUR_DASH) return;
  clearTimeout(DASH_RESIZE_T);
  DASH_RESIZE_T = setTimeout(() => {
    const bp = dashBreakpoint();
    // 跨断点时内联 grid 样式需整页重排，仅 recreateChart 不够
    if (bp !== DASH_LAST_BP) {
      DASH_LAST_BP = bp;
      renderPanels();
      return;
    }
    for (const id in DASH_CHART_ARGS) { try { createChart.apply(null, DASH_CHART_ARGS[id]); } catch (e) {} }
    if (typeof DashCharts !== "undefined") {
      try { DashCharts.resizeAll(document.getElementById("dashDetail")); } catch (e) {}
    }
  }, 250);
});

/* ---------- 详情事件委托 ---------- */
safeAddEventListener("dashDetail", "click", async e => {
  const t = e.target;
  if (t.closest("#dashBack")) {
    if (DASH_EDIT && DASH_DIRTY && !confirm("仍有未保存修改，确认放弃并返回列表？")) return;
    showDashHome(); loadDashboards(); return;
  }
  if (t.closest("#dashRefresh")) {
    const dashKey = "dashboard:" + (CUR_DASH && CUR_DASH.id || "x");
    if (typeof clearAnchoredRange === "function") clearAnchoredRange(dashKey);
    dashBumpLoad();
    renderPanels();
    return;
  }
  if (t.closest("#dashEditBtn")) { DASH_EDIT = true; resetDashEditHistory(); renderDashDetail(); return; }
  if (t.closest("#dashAnalyzeBtn")) { aiAnalyzeDash(); return; }
  if (t.closest("#dashOptimizeBtn")) { aiOptimizeDash(); return; }
  if (t.closest("#dashTicketBtn")) { aiTicketDash(); return; }
  if (t.closest("#dashFullscreenBtn")) { toggleDashFullscreen(); return; }
  if (t.closest("#dashExportBtn")) { openMask("dashExportMask"); syncDashExportUI(); return; }
  if (t.closest("#dashUndoBtn")) { undoDashEdit(); return; }
  if (t.closest("#dashRedoBtn")) { redoDashEdit(); return; }
  if (t.closest("#dashAutoFillBtn")) {
    DASH_AUTO_FILL = !DASH_AUTO_FILL;
    try { localStorage.setItem("aiops_dash_auto_fill", DASH_AUTO_FILL ? "1" : "0"); } catch (e) {}
    const btn = $("dashAutoFillBtn");
    if (btn) {
      btn.classList.toggle("active", DASH_AUTO_FILL);
      btn.textContent = DASH_AUTO_FILL ? "◉ 自动补位" : "○ 自动补位";
    }
    toast(DASH_AUTO_FILL ? "已开启自动补位：松手后吸附空位并填补空洞" : "已关闭自动补位：仅下推重叠面板", "ok");
    return;
  }
  if (t.closest("#dashCompactBtn")) {
    if (!CUR_DASH || !DASH_EDIT) return;
    rememberDashMutation();
    compactDashLayout(CUR_DASH.panels || []);
    renderDashDetail();
    toast("已紧凑布局", "ok");
    return;
  }
  if (t.closest("#dashTidyBtn")) {
    if (!CUR_DASH || !DASH_EDIT) return;
    rememberDashMutation();
    const ordered = (CUR_DASH.panels || []).slice().sort((a, b) => (a.grid.y - b.grid.y) || (a.grid.x - b.grid.x));
    reflowDashLayout(ordered);
    CUR_DASH.panels = ordered;
    renderDashDetail();
    toast("已整齐重排", "ok");
    return;
  }
  if (t.closest("#dashCancelEdit")) {
    if (DASH_DIRTY && !confirm("确认放弃所有未保存修改？")) return;
    openDashboard(CUR_DASH.id); return;
  }
  if (t.closest("#dashSaveBtn")) { saveCurDash(); return; }
  if (t.closest("#dashAddPanel")) { openPanelEditor(null); return; }
  if (t.closest("#dashEditVars")) { openVarsEditor(); return; }
  if (t.closest("#dashEditMeta")) { openDashMeta(CUR_DASH); return; }
  if (t.closest("#dashCustomToggle")) { const pn = $("dashCustomPanel"); if (pn) pn.hidden = !pn.hidden; return; }
  if (t.closest("#dashCustomApply")) { applyDashCustom(); return; }
  const rc = t.closest("[data-drange]");
  if (rc) {
    const nextH = +rc.dataset.drange;
    if (DASH_RANGE.hours !== nextH || DASH_RANGE.custom) {
      if (typeof clearAnchoredRange === "function") clearAnchoredRange("dashboard:" + (CUR_DASH && CUR_DASH.id || "x"));
    }
    DASH_RANGE = { hours: nextH, custom: null };
    renderDashDetail();
    return;
  }
  const pa = t.closest("[data-pact]");
  if (pa) { handlePanelAction(pa.dataset.pact, +pa.dataset.id); return; }
});
safeAddEventListener("dashDetail", "change", e => {
  const modelSel = e.target.closest("[data-fc-model]");
  if (modelSel) {
    const pid = +modelSel.dataset.fcModel;
    if (!DASH_PANEL_TREND[pid]) DASH_PANEL_TREND[pid] = {};
    DASH_PANEL_TREND[pid].method = modelSel.value || "auto";
    const p = CUR_DASH && CUR_DASH.panels && CUR_DASH.panels.find(x => x.id === pid);
    const body = $("panelBody_" + pid);
    if (p && body) loadPanel(p);
    return;
  }
  const hz = e.target.closest("[data-horizon]");
  if (hz) {
    const pid = +hz.dataset.horizon;
    if (!DASH_PANEL_TREND[pid]) DASH_PANEL_TREND[pid] = {};
    DASH_PANEL_TREND[pid].horizonSec = +hz.value || 0;
    const p = CUR_DASH && CUR_DASH.panels && CUR_DASH.panels.find(x => x.id === pid);
    const body = $("panelBody_" + pid);
    if (p && body) loadPanel(p);
    return;
  }
  if (e.target.id === "dashDSSelect") {
    if (DASH_EDIT) rememberDashMutation();
    CUR_DASH.datasource = e.target.value;
    if (DASH_EDIT) {
      resolveDashVars().then(renderDashDetail);
    } else {
      // 浏览态切换仍持久化，但同步更新 revision，避免下一次人工保存误判为版本冲突。
      fetch(`${API}/dashboards`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(CUR_DASH) })
        .then(async r => {
          const j = await r.json().catch(() => ({}));
          if (!r.ok) throw new Error(j.error || ("HTTP " + r.status));
          if (j.revision) CUR_DASH.revision = j.revision;
          if (j.updated_at) CUR_DASH.updated_at = j.updated_at;
          return resolveDashVars();
        })
        .then(renderDashDetail)
        .catch(err => { toast("数据源切换保存失败：" + err, "err"); openDashboard(CUR_DASH.id); });
    }
    return;
  }
  const sel = e.target.closest("[data-dvar]");
  if (sel) { DASH_VARVALS[sel.dataset.dvar] = sel.value; renderPanels(); }
});
// Pointer-based free drag / resize with BI grid canvas snap + guides.
safeAddEventListener("dashDetail", "pointerdown", e => {
  if (!DASH_EDIT || !CUR_DASH || dashBreakpoint() !== "d") return;
  if (e.button != null && e.button !== 0) return;
  const grid = $("dashGrid");
  if (!grid) return;
  const resize = e.target.closest("[data-resize]");
  const dragStart = e.target.closest("[data-drag-handle], [data-drag-surface]");
  if (!resize && !dragStart) return;
  if (e.target.closest("[data-pact]") && !e.target.closest("[data-drag-handle]")) return;
  const id = +(resize || dragStart).dataset.id;
  const panel = (CUR_DASH.panels || []).find(p => p.id === id);
  const panelEl = grid.querySelector(`[data-panel="${id}"]`);
  if (!panel || !panelEl) return;
  e.preventDefault();
  try { e.target.setPointerCapture && e.target.setPointerCapture(e.pointerId); } catch (_) {}
  const orig = dashGridBox(panel);
  const ghost = ensureDashGhost(grid);
  applyPanelGridStyle(ghost, orig);
  ghost.classList.add("visible");
  const gl = ghost.querySelector(".dash-layout-ghost-label");
  if (gl) gl.textContent = `${orig.w}×${orig.h}`;
  panelEl.classList.add(resize ? "resizing" : "dragging");
  document.body.classList.add("dash-layout-dragging");
  const grab = clientToGrid(e, grid);
  DASH_LAYOUT_DRAG = {
    mode: resize ? ("resize-" + resize.dataset.resize) : "move",
    id, panelEl, ghost, orig,
    cur: { x: orig.x, y: orig.y, w: orig.w, h: orig.h },
    grabOffX: grab.x - orig.x,
    grabOffY: grab.y - orig.y,
    startX: e.clientX, startY: e.clientY,
    pointerId: e.pointerId,
  };
  highlightDashCells(orig);
  updateDashLayoutHUD(orig, false);
  updateDashAlignGuides(orig, id);
});
document.addEventListener("pointermove", e => {
  if (!DASH_LAYOUT_DRAG) return;
  const grid = $("dashGrid");
  if (!grid) return;
  const d = DASH_LAYOUT_DRAG;
  const m = dashGridMetrics(grid);
  let { x, y, w, h } = d.orig;
  if (d.mode === "move") {
    const gpos = clientToGrid(e, grid);
    x = Math.max(0, Math.min(m.cols - w, gpos.x - (d.grabOffX || 0)));
    y = Math.max(0, gpos.y - (d.grabOffY || 0));
  } else {
    const dx = Math.round((e.clientX - d.startX) / (m.colW + m.gap));
    const dy = Math.round((e.clientY - d.startY) / (m.rowH + m.gap));
    if (d.mode === "resize-e" || d.mode === "resize-se") {
      w = Math.max(1, Math.min(m.cols - x, d.orig.w + dx));
    }
    if (d.mode === "resize-s" || d.mode === "resize-se") {
      h = Math.max(1, Math.min(48, d.orig.h + dy));
    }
  }
  d.cur = { x, y, w, h };
  // Preview snapped free slot when auto-fill on and current cell occupied
  let preview = d.cur;
  let conflict = layoutTargetConflicts(d.cur, d.id);
  if (DASH_AUTO_FILL && conflict && d.mode === "move") {
    const free = findNearestFreeSlot(x, y, w, h, d.id);
    preview = { x: free.x, y: free.y, w, h };
  }
  applyPanelGridStyle(d.panelEl, d.cur);
  applyPanelGridStyle(d.ghost, preview);
  const gl = d.ghost && d.ghost.querySelector(".dash-layout-ghost-label");
  if (gl) gl.textContent = conflict && DASH_AUTO_FILL ? `→ ${preview.x},${preview.y}` : `${preview.w}×${preview.h}`;
  d.ghost.classList.toggle("snap-preview", !!(conflict && DASH_AUTO_FILL));
  highlightDashCells(preview);
  updateDashLayoutHUD(preview, conflict && DASH_AUTO_FILL);
  updateDashAlignGuides(preview, d.id);
});
document.addEventListener("pointerup", () => {
  if (!DASH_LAYOUT_DRAG) return;
  // Commit using preview snap position when auto-fill resolved a conflict
  if (DASH_AUTO_FILL && DASH_LAYOUT_DRAG.mode === "move") {
    const cur = DASH_LAYOUT_DRAG.cur;
    if (layoutTargetConflicts(cur, DASH_LAYOUT_DRAG.id)) {
      const free = findNearestFreeSlot(cur.x, cur.y, cur.w, cur.h, DASH_LAYOUT_DRAG.id);
      DASH_LAYOUT_DRAG.cur = { ...cur, x: free.x, y: free.y };
    }
  }
  endDashLayoutDrag(true);
});
document.addEventListener("pointercancel", () => {
  if (!DASH_LAYOUT_DRAG) return;
  endDashLayoutDrag(false);
});
document.addEventListener("keydown", e => {
  if (e.key === "Escape" && DASH_AI_REVIEW_RESOLVE) {
    e.preventDefault();
    finishDashAIReview(false);
    return;
  }
  if (!DASH_EDIT || !CUR_DASH || !(e.ctrlKey || e.metaKey) || e.altKey) return;
  if (e.target && e.target.closest && e.target.closest("input,textarea,select,[contenteditable=true]")) return;
  if (String(e.key).toLowerCase() !== "z") return;
  e.preventDefault();
  if (e.shiftKey) redoDashEdit(); else undoDashEdit();
});
function applyDashCustom() {
  const f = $("dashCustomFrom"), tt = $("dashCustomTo");
  if (!f || !tt || !f.value || !tt.value) { toast("请选择起止时间", "warn"); return; }
  const from = Math.floor(new Date(f.value).getTime() / 1000), to = Math.floor(new Date(tt.value).getTime() / 1000);
  if (!(to > from)) { toast("结束时间必须晚于开始时间", "warn"); return; }
  DASH_RANGE = { hours: 0, custom: { from, to } }; renderDashDetail();
}
function panelTrendState(pid) {
  if (!DASH_PANEL_TREND[pid]) DASH_PANEL_TREND[pid] = { forecast: false, pop: false, yoy: false, horizonSec: 0, method: "auto" };
  return DASH_PANEL_TREND[pid];
}
function panelTrendMode(st) {
  const parts = [];
  if (st.forecast) parts.push("forecast");
  if (st.pop) parts.push("pop");
  if (st.yoy) parts.push("yoy");
  return parts.join("+");
}
function handlePanelAction(act, pid) {
  const panels = CUR_DASH.panels;
  const idx = panels.findIndex(p => p.id === pid);
  if (idx < 0) return;
  if (act === "forecast" || act === "pop" || act === "yoy") {
    const st = panelTrendState(pid);
    st[act] = !st[act];
    if (act === "forecast" && !st.forecast) {
      st.horizonSec = 0; // 关闭预测时清展望窗，避免残留状态
    }
    // 销毁该面板旧图实例，确保关闭预测后不再沿用带未来轴的 option
    try {
      const body = document.querySelector(`.dash-panel[data-id="${pid}"] .dash-panel-body`);
      if (body && typeof DashCharts !== "undefined" && DashCharts.dispose) {
        const el = body.querySelector(".dash-echart");
        if (el) DashCharts.dispose(el);
      }
    } catch (e) { /* ignore */ }
    renderPanels();
    return;
  }
  if (act === "zoom") { openDashPanelZoom(pid); return; }
  if (act === "ai") { aiAnalyzePanel(panels[idx]); return; }
  if (act === "edit") { openPanelEditor(panels[idx]); return; }
  if (act === "dup") {
    rememberDashMutation();
    const copy = cloneDashboard(panels[idx]);
    copy.id = nextPanelId();
    copy.title = (copy.title || "未命名组件") + " · 副本";
    placeNewPanel(copy, panels);
    panels.push(copy);
    renderDashDetail();
    return;
  }
  if (act === "del") {
    if (confirm("删除该组件？")) { rememberDashMutation(); panels.splice(idx, 1); renderDashDetail(); }
    return;
  }
  // 上/下移：交换视觉顺序后流式重排，避免只换 x/y 造成重叠
  const sorted = panels.slice().sort((a, b) => (a.grid.y - b.grid.y) || (a.grid.x - b.grid.x));
  const si = sorted.findIndex(p => p.id === pid);
  const swap = act === "up" ? si - 1 : si + 1;
  if (swap < 0 || swap >= sorted.length) return;
  rememberDashMutation();
  const tmp = sorted[si]; sorted[si] = sorted[swap]; sorted[swap] = tmp;
  reflowDashLayout(sorted);
  renderDashDetail();
}

/* ---------- 面板编辑器 ---------- */
let PANEL_THRESHOLDS_DRAFT = [];

function defaultPanelThresholds(unit) {
  if (unit === "percent" || unit === "percentunit") {
    const scale = unit === "percentunit" ? 0.01 : 1;
    return [
      { value: 0, color: "var(--ok)" },
      { value: 75 * scale, color: "var(--warn)" },
      { value: 90 * scale, color: "var(--crit)" }
    ];
  }
  return [
    { value: 0, color: "var(--ok)" },
    { value: 75, color: "var(--warn)" },
    { value: 90, color: "var(--crit)" }
  ];
}

function switchPanelEditTab(name) {
  const tab = name || "data";
  document.querySelectorAll("#panelEditMask [data-panel-tab]").forEach(btn => {
    btn.classList.toggle("active", btn.getAttribute("data-panel-tab") === tab);
  });
  document.querySelectorAll("#panelEditMask [data-panel-pane]").forEach(pane => {
    pane.classList.toggle("active", pane.getAttribute("data-panel-pane") === tab);
  });
}

function renderPanelThresholdList() {
  const wrap = $("panelThresholdList");
  if (!wrap) return;
  if (!PANEL_THRESHOLDS_DRAFT.length) {
    wrap.innerHTML = `<div class="dash-empty">尚未配置阈值（默认关闭；点「还原默认」可填入 0/75/90）</div>`;
  } else {
    wrap.innerHTML = PANEL_THRESHOLDS_DRAFT.map((t, i) => `
      <div class="panel-threshold-row">
        <input type="color" data-th-color-pick="${i}" value="${thresholdToColorInput(t.color)}" title="颜色">
        <input type="text" class="mono" data-th-color="${i}" value="${esc(t.color || "")}" placeholder="#22c55e 或 var(--ok)" style="flex:1.2">
        <input type="number" class="mono" data-th-value="${i}" value="${t.value != null ? t.value : ""}" step="any" placeholder="值" style="width:110px">
        <button type="button" class="mini-btn del" data-th-del="${i}" title="删除">✕</button>
      </div>`).join("");
  }
  renderPanelThresholdPreview();
}

function thresholdToColorInput(c) {
  const t = themeThresholdHex(c);
  return /^#[0-9a-fA-F]{6}$/.test(t) ? t : "#22c55e";
}
function themeThresholdHex(c) {
  if (!c) return "#22c55e";
  if (c === "var(--ok)") return "#22c55e";
  if (c === "var(--warn)") return "#f59e0b";
  if (c === "var(--crit)") return "#ef4444";
  if (c === "var(--accent)") return "#4c8dff";
  if (/^#[0-9a-fA-F]{3}$/.test(c)) {
    return "#" + c[1] + c[1] + c[2] + c[2] + c[3] + c[3];
  }
  return c;
}
function renderPanelThresholdPreview() {
  const el = $("panelThresholdPreview");
  if (!el) return;
  const sorted = PANEL_THRESHOLDS_DRAFT.slice().sort((a, b) => (+a.value || 0) - (+b.value || 0));
  if (!sorted.length) { el.innerHTML = ""; return; }
  el.innerHTML = sorted.map(t => `<span style="background:${esc(t.color || "var(--accent)")}" title="${esc(String(t.value))}">${esc(String(t.value))}</span>`).join("");
}
function syncPanelThresholds() {
  document.querySelectorAll("[data-th-value]").forEach(el => {
    const i = +el.dataset.thValue;
    if (PANEL_THRESHOLDS_DRAFT[i]) PANEL_THRESHOLDS_DRAFT[i].value = el.value === "" ? 0 : parseFloat(el.value);
  });
  document.querySelectorAll("[data-th-color]").forEach(el => {
    const i = +el.dataset.thColor;
    if (PANEL_THRESHOLDS_DRAFT[i]) PANEL_THRESHOLDS_DRAFT[i].color = el.value.trim();
  });
}
function renderPanelSwatchPreview() {
  const el = $("panelSwatchPreview");
  const colorsEl = $("panelColors");
  if (!el || !colorsEl) return;
  const cols = colorsEl.value.split(",").map(s => s.trim()).filter(Boolean);
  el.innerHTML = cols.map(c => `<span class="panel-swatch" style="background:${esc(c)}" title="${esc(c)}"></span>`).join("");
}
function panelPaletteToggle() {
  const pal = $("panelPalette") ? $("panelPalette").value : "classic";
  const row = $("panelColorsRow");
  if (row) row.style.display = pal === "custom" ? "" : "none";
  if (pal === "custom") renderPanelSwatchPreview();
}

function openPanelEditor(p) {
  $("panelId").value = p ? p.id : "";
  $("panelTitle").value = p ? (p.title || "") : "";
  $("panelType").value = p ? p.type : "timeseries";
  $("panelW").value = p ? (p.grid.w || 12) : 12;
  $("panelH").value = p ? (p.grid.h || 8) : 8;
  $("panelUnit").value = p ? (p.unit || "") : "";
  $("panelMin").value = p && p.min != null ? p.min : "";
  $("panelMax").value = p && p.max != null ? p.max : "";
  $("panelText").value = p ? (p.text || "") : "";
  const o = (p && p.options) || {};
  const decEl = $("panelDecimals");
  if (decEl) {
    if (o.decimals != null) decEl.value = o.decimals;
    else if (p && p.decimals) decEl.value = p.decimals;
    else decEl.value = "";
  }
  if ($("panelSort")) $("panelSort").value = o.sort || "desc";
  if ($("panelLimit")) $("panelLimit").value = o.limit > 0 ? o.limit : "";
  if ($("panelPalette")) $("panelPalette").value = o.palette || "classic";
  if ($("panelLegend")) $("panelLegend").value = o.legend || "bottom";
  if ($("panelColors")) $("panelColors").value = (o.colors || []).join(",");
  if ($("panelChartStyle")) $("panelChartStyle").value = o.chart_style || "line";
  if ($("panelStacked")) $("panelStacked").checked = !!o.stacked;
  if ($("panelSmooth")) $("panelSmooth").checked = !!o.smooth;
  if ($("panelShowPoints")) $("panelShowPoints").checked = !!o.show_points;
  PANEL_THRESHOLDS_DRAFT = (o.thresholds && o.thresholds.length)
    ? o.thresholds.map(t => ({ value: t.value, color: t.color || "var(--ok)" }))
    : [];
  PANEL_TARGETS_DRAFT = p && p.targets ? p.targets.map(t => ({ expr: t.expr, legend: t.legend || "" })) : [{ expr: "", legend: "" }];
  renderPanelTargets();
  renderPanelThresholdList();
  fillPanelDS(p ? p.type : "timeseries", p ? (p.datasource || "") : "");
  panelTypeToggle();
  panelPaletteToggle();
  switchPanelEditTab("data");
  $("panelEditTitle").textContent = p ? "编辑面板" : "添加面板";
  const status = $("panelQueryTestResult"); if (status) { status.textContent = ""; status.className = "panel-query-test-result"; }
  openMask("panelEditMask");
}
safeAddEventListener("panelEditMask", "click", e => {
  const tab = e.target.closest("[data-panel-tab]");
  if (tab) { switchPanelEditTab(tab.getAttribute("data-panel-tab")); return; }
  const del = e.target.closest("[data-th-del]");
  if (del) {
    syncPanelThresholds();
    PANEL_THRESHOLDS_DRAFT.splice(+del.dataset.thDel, 1);
    renderPanelThresholdList();
  }
});
safeAddEventListener("panelAddThreshold", "click", () => {
  syncPanelThresholds();
  const last = PANEL_THRESHOLDS_DRAFT[PANEL_THRESHOLDS_DRAFT.length - 1];
  PANEL_THRESHOLDS_DRAFT.push({ value: last ? (+last.value || 0) + 10 : 0, color: "var(--accent)" });
  renderPanelThresholdList();
});
safeAddEventListener("panelResetThresholds", "click", () => {
  PANEL_THRESHOLDS_DRAFT = defaultPanelThresholds($("panelUnit") ? $("panelUnit").value : "");
  renderPanelThresholdList();
});
safeAddEventListener("panelThresholdList", "input", e => {
  const pick = e.target.closest("[data-th-color-pick]");
  if (pick) {
    const i = +pick.dataset.thColorPick;
    if (PANEL_THRESHOLDS_DRAFT[i]) {
      PANEL_THRESHOLDS_DRAFT[i].color = pick.value;
      const txt = document.querySelector(`[data-th-color="${i}"]`);
      if (txt) txt.value = pick.value;
      renderPanelThresholdPreview();
    }
    return;
  }
  syncPanelThresholds();
  renderPanelThresholdPreview();
});
safeAddEventListener("panelPalette", "change", panelPaletteToggle);
safeAddEventListener("panelColors", "input", renderPanelSwatchPreview);
safeAddEventListener("panelUnit", "change", () => {
  // Keep thresholds; user can reset via button for percent defaults.
});
const PANEL_TEMPLATES = {
  trend: { type: "timeseries", title: "趋势", unit: "", w: 12, h: 7 },
  kpi: { type: "stat", title: "关键指标", unit: "short", w: 6, h: 4 },
  gauge: { type: "gauge", title: "利用率", unit: "percent", w: 8, h: 5, min: 0, max: 100 },
  ranking: { type: "bargauge", title: "Top 排行", unit: "short", w: 12, h: 6 },
  table: { type: "table", title: "明细", unit: "", w: 12, h: 7 },
  logs: { type: "logs", title: "实时日志", unit: "", w: 24, h: 8 },
  alerts: { type: "alertlist", title: "当前告警", unit: "", w: 12, h: 7 },
  text: { type: "text", title: "说明", unit: "", w: 24, h: 3 }
};
safeAddEventListener("panelTemplatePalette", "click", e => {
  const btn = e.target.closest("[data-panel-template]");
  if (!btn) return;
  const tpl = PANEL_TEMPLATES[btn.dataset.panelTemplate];
  if (!tpl) return;
  $("panelType").value = tpl.type;
  if (!$("panelTitle").value.trim()) $("panelTitle").value = tpl.title;
  $("panelUnit").value = tpl.unit || "";
  $("panelW").value = tpl.w;
  $("panelH").value = tpl.h;
  $("panelMin").value = tpl.min == null ? "" : tpl.min;
  $("panelMax").value = tpl.max == null ? "" : tpl.max;
  panelTypeToggle();
  document.querySelectorAll("[data-panel-template]").forEach(x => x.classList.toggle("active", x === btn));
});
function fillPanelDS(type, selected) {
  const sel = $("panelDS");
  if (!sel) return;
  if (type === "logs") sel.innerHTML = dsOptions(selected, ["loki"], false) || `<option value="">（请先在「数据源」配置 Loki）</option>`;
  else if (type === "table") {
    // Table panels can use PromQL instant OR SQL datasources.
    const prom = dsOptions(selected, ["prometheus", "vm"], true);
    const sql = dsOptions(selected, ["postgres", "mysql"], false);
    sel.innerHTML = prom + sql;
  } else sel.innerHTML = dsOptions(selected, ["prometheus", "vm"], true);
}
function renderPanelTargets() {
  const wrap = $("panelTargets");
  if (!wrap) return;
  wrap.innerHTML = PANEL_TARGETS_DRAFT.map((t, i) => `
    <div class="panel-target-row">
      <input type="text" class="mono" data-tgt-expr="${i}" placeholder="PromQL，如 rate(node_cpu_seconds_total[$__interval])，不会写就点右侧 ✨" value="${esc(t.expr)}" style="flex:2">
      <input type="text" data-tgt-legend="${i}" placeholder="图例 {{instance}}（可空）" value="${esc(t.legend)}" style="flex:1">
      <button class="mini-btn" data-tgt-ai="${i}" title="AI 生成查询（用大白话描述需求）">✨</button>
      <button class="mini-btn del" data-tgt-del="${i}" title="删除">✕</button>
    </div>`).join("");
}
// fetchMetricNames：从当前数据源取可用指标名（供 AI 生成查询做上下文，只用真实指标不臆造）。
async function fetchMetricNames(dsID) {
  try {
    const r = await fetch(`${API}/dashboards/var-values`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: "__m", type: "query", query: "label_values(__name__)", datasource: dsID }) }).then(r => r.json());
    return (r && r.values) || [];
  } catch (e) { return []; }
}
// aiGenPromQL：AI 辅助写查询——注入数据源真实指标名 + 面板类型，用户用大白话描述需求，AI 产出 PromQL/LogQL，一键填入。
async function aiGenPromQL(i) {
  syncPanelTargets();
  const ty = $("panelType").value;
  const dsID = ty === "logs" ? $("panelDS").value : ($("panelDS").value || (CUR_DASH && CUR_DASH.datasource) || "");
  let ctx;
  let task = "promql";
  let title = "✨ AI 生成 PromQL";
  let placeholder = "用大白话描述你要查什么，如：这台主机CPU使用率 / MySQL每秒查询数 / 磁盘剩余空间百分比 / HTTP 5xx 错误率";
  if (ty === "logs") {
    task = "logql";
    title = "✨ AI 生成 LogQL";
    ctx = "目标：为日志面板生成 LogQL（数据源为 Loki）。请用常见 Loki 标签选择器 + 过滤/解析。";
    placeholder = "如：nginx 最近的 5xx 错误日志";
  } else if (ty === "table" && isSQLDashDS(dsID)) {
    const ds = dsById(dsID);
    const dialect = ds && ds.type === "mysql" ? "MySQL" : "PostgreSQL";
    task = "pgsql";
    title = "✨ AI 生成 " + dialect + " SQL";
    ctx = `目标：为表格面板生成只读 SQL。\n方言：${dialect}\n数据源 id=${dsID}\n数据源类型：${ds ? ds.type : "sql"}\n数据库：${ds && ds.database ? ds.database : "（未指定）"}\n仅 SELECT/WITH，默认 LIMIT。`;
    placeholder = "如：列出当前活跃会话 / 统计各表行数估计 / 查询最近慢语句";
  } else {
    toast("读取可用指标…", "ok");
    const metrics = (await fetchMetricNames(dsID)).slice(0, 200);
    ctx = (metrics.length ? "【可用指标（节选，只能用这些真实指标名，不要臆造）】\n" + metrics.join(", ") : "（未取到指标列表，按常见 Prometheus / node_exporter / 各 exporter 命名生成）")
      + "\n\n目标面板类型：" + ty + "（据此选合适的聚合与时间窗口）。";
  }
  openAIAssist({
    task,
    title,
    mode: "generate",
    context: ctx,
    datasource: dsID || "",
    placeholder,
    applyLabel: "填入此查询",
    applyTo: (code) => {
      const expr = (code || "").trim();
      if (!expr) { toast("未生成有效查询", "err"); return; }
      if (!PANEL_TARGETS_DRAFT[i]) PANEL_TARGETS_DRAFT[i] = { expr: "", legend: "" };
      PANEL_TARGETS_DRAFT[i].expr = expr;
      renderPanelTargets();
      toast("已填入查询，可微调后保存", "ok");
    }
  });
}
function panelTypeToggle() {
  const ty = $("panelType").value;
  const noTargets = ty === "text" || ty === "markdown" || ty === "alertlist" || ty === "clock" || ty === "news";
  const comingSoon = !!(DASH_COMING_SOON && DASH_COMING_SOON[ty]);
  $("panelTextRow").style.display = (ty === "text" || ty === "markdown" || ty === "news") ? "" : "none";
  $("panelTargetsWrap").style.display = (noTargets || comingSoon) ? "none" : "";
  const qsec = $("panelQuerySec"); if (qsec) qsec.style.display = (ty === "text" || ty === "markdown" || ty === "clock") ? "none" : "";
  $("panelUnitRow").style.display = (ty === "text" || ty === "markdown" || ty === "logs" || ty === "alertlist" || ty === "clock" || ty === "news") ? "none" : "";
  const dsRow = $("panelDSRow"); if (dsRow) dsRow.style.display = (noTargets || comingSoon) ? "none" : "";
  const isTs = ty === "timeseries" || ty === "candlestick";
  const isRank = ty === "piechart" || ty === "pie" || ty === "barchart" || ty === "bar" || ty === "bargauge" || ty === "table" || ty === "alertlist" || ty === "histogram" || ty === "radar" || ty === "sankey";
  const showAxis = isTs || ty === "gauge" || ty === "bargauge" || ty === "stat" || ty === "heatmap" || ty === "radar";
  const showLegend = isTs || ty === "piechart" || ty === "pie" || ty === "barchart" || ty === "bar" || ty === "radar" || ty === "sankey";
  const showThresh = ty === "stat" || ty === "gauge" || ty === "bargauge" || ty === "table" || ty === "state-timeline" || ty === "statetimeline" || isTs || ty === "radar";
  const styleRow = $("panelStyleRow"); if (styleRow) styleRow.style.display = ty === "timeseries" ? "" : "none";
  const sortRow = $("panelSortRow"); if (sortRow) sortRow.style.display = isRank || ty === "heatmap" || ty === "state-timeline" || ty === "statetimeline" ? "" : "none";
  const rangeRow = $("panelRangeRow"); if (rangeRow) rangeRow.style.display = showAxis ? "" : "none";
  const legendField = $("panelLegend") ? $("panelLegend").closest(".field") : null;
  if (legendField) legendField.style.display = showLegend ? "" : "none";
  const threshTab = document.querySelector('#panelEditMask [data-panel-tab="threshold"]');
  if (threshTab) threshTab.style.display = showThresh ? "" : "none";
  fillPanelDS(ty, $("panelDS").value);
  const hint = $("panelQueryHint");
  if (hint) {
    if (comingSoon) hint.innerHTML = "该类型即将支持完整渲染；可先保存面板与查询，后续版本自动启用。";
    else if (ty === "logs") hint.innerHTML = "LogQL 查询（请选 Loki 数据源）";
    else if (ty === "alertlist") hint.innerHTML = "告警列表读取平台当前告警，无需查询。";
    else if (ty === "clock") hint.innerHTML = "本地时钟面板，无需查询。";
    else if (ty === "radar") hint.innerHTML = "雷达图：即时查询多序列作为维度（建议 3–8 维）。";
    else if (ty === "sankey") hint.innerHTML = "桑基图：可用 src/dst 标签表示流量路径；否则以「来源→序列名」构图。";
    else if (ty === "candlestick") hint.innerHTML = "K 线：由时序采样近似 OHLC，适合观察波动区间。";
    else if (ty === "table") hint.innerHTML = "表格：可选 PromQL 即时查询，或 PostgreSQL/MySQL 数据源的只读 SQL（SELECT）。";
    else hint.innerHTML = "PromQL，可多条；支持 <code>$变量</code> 与 <code>{{标签}}</code> 图例。";
  }
  panelPaletteToggle();
}
safeAddEventListener("panelType", "change", panelTypeToggle);
safeAddEventListener("panelAddTarget", "click", () => { PANEL_TARGETS_DRAFT.push({ expr: "", legend: "" }); renderPanelTargets(); });
safeAddEventListener("panelTargets", "click", e => {
  const ai = e.target.closest("[data-tgt-ai]");
  if (ai) { aiGenPromQL(+ai.dataset.tgtAi); return; }
  const del = e.target.closest("[data-tgt-del]");
  if (del) { syncPanelTargets(); PANEL_TARGETS_DRAFT.splice(+del.dataset.tgtDel, 1); if (!PANEL_TARGETS_DRAFT.length) PANEL_TARGETS_DRAFT.push({ expr: "", legend: "" }); renderPanelTargets(); }
});
safeAddEventListener("panelTestQuery", "click", async () => {
  syncPanelTargets();
  const ty = $("panelType").value;
  const result = $("panelQueryTestResult");
  const expr = PANEL_TARGETS_DRAFT.find(t => (t.expr || "").trim());
  if (!expr || (ty !== "alertlist" && ty !== "text" && !expr.expr.trim())) {
    if (result) { result.textContent = "请先填写查询"; result.className = "panel-query-test-result err"; }
    return;
  }
  const started = performance.now();
  if (result) { result.textContent = "正在验证…"; result.className = "panel-query-test-result"; }
  try {
    const ds = $("panelDS").value || (CUR_DASH && CUR_DASH.datasource) || "";
    const dsMeta = (DASH_DATASOURCES || []).find(d => d && d.id === ds);
    const dsType = (dsMeta && dsMeta.type) || "";
    let endpoint = "query-instant";
    let payload = { expr: expr.expr.trim(), datasource: ds, vars: panelVars() };
    if (ty === "logs") {
      endpoint = "query-logs";
      const now = Math.floor(Date.now() / 1000);
      payload = Object.assign(payload, { from: now - 900, to: now, limit: 3 });
    } else if (ty === "table" && (dsType === "postgres" || dsType === "postgresql" || dsType === "mysql")) {
      endpoint = "query-sql";
      payload = { expr: expr.expr.trim(), datasource: ds, limit: 20, vars: panelVars() };
    }
    const r = await fetch(`${API}/dashboards/${endpoint}`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload)
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || ("HTTP " + r.status));
    if (j.available === false) throw new Error("所选数据源不可用或类型不匹配");
    let count = 0;
    if (ty === "logs") count = ((j.lines || []).length);
    else if (endpoint === "query-sql") count = ((j.rows || []).length);
    else count = ((j.series || []).length);
    const ms = Math.round(performance.now() - started);
    if (result) {
      result.textContent = count ? `验证通过 · ${count} 组结果 · ${ms}ms` : `表达式有效，但当前无数据 · ${ms}ms`;
      result.className = "panel-query-test-result " + (count ? "ok" : "warn");
    }
  } catch (e) {
    if (result) { result.textContent = "验证失败：" + String(e); result.className = "panel-query-test-result err"; }
  }
});
function syncPanelTargets() {
  document.querySelectorAll("[data-tgt-expr]").forEach(el => { const i = +el.dataset.tgtExpr; if (PANEL_TARGETS_DRAFT[i]) PANEL_TARGETS_DRAFT[i].expr = el.value; });
  document.querySelectorAll("[data-tgt-legend]").forEach(el => { const i = +el.dataset.tgtLegend; if (PANEL_TARGETS_DRAFT[i]) PANEL_TARGETS_DRAFT[i].legend = el.value; });
}
safeAddEventListener("panelSave", "click", () => {
  syncPanelTargets();
  syncPanelThresholds();
  const ty = $("panelType").value;
  const title = $("panelTitle").value.trim();
  const noTargets = ty === "text" || ty === "markdown" || ty === "alertlist" || ty === "clock" || ty === "news" || !!(DASH_COMING_SOON && DASH_COMING_SOON[ty]);
  const targets = PANEL_TARGETS_DRAFT.filter(t => t.expr.trim()).map(t => ({ expr: t.expr.trim(), legend: t.legend.trim() }));
  if (!noTargets && !targets.length) { toast(ty === "logs" ? "请填写 LogQL 查询" : "请至少填写一条 PromQL 查询", "err"); return; }
  if (ty === "logs" && !$("panelDS").value) { toast("日志面板需选择一个 Loki 数据源", "err"); return; }
  const min = $("panelMin").value.trim(), max = $("panelMax").value.trim();
  if (min !== "" && max !== "" && parseFloat(min) >= parseFloat(max)) { toast("量程最小值必须小于最大值", "err"); return; }
  const decRaw = $("panelDecimals") ? $("panelDecimals").value.trim() : "";
  const limitRaw = $("panelLimit") ? $("panelLimit").value.trim() : "";
  const colorsRaw = $("panelColors") ? $("panelColors").value : "";
  const options = {
    sort: ($("panelSort") && $("panelSort").value) || "desc",
    limit: limitRaw !== "" ? Math.max(0, Math.min(200, parseInt(limitRaw, 10) || 0)) : 0,
    palette: ($("panelPalette") && $("panelPalette").value) || "classic",
    legend: ($("panelLegend") && $("panelLegend").value) || "bottom",
    chart_style: ($("panelChartStyle") && $("panelChartStyle").value) || "line",
    stacked: !!( $("panelStacked") && $("panelStacked").checked ),
    smooth: !!( $("panelSmooth") && $("panelSmooth").checked ),
    show_points: !!( $("panelShowPoints") && $("panelShowPoints").checked ),
    colors: colorsRaw.split(",").map(s => s.trim()).filter(Boolean).slice(0, 32),
    thresholds: PANEL_THRESHOLDS_DRAFT
      .filter(t => t && t.color)
      .map(t => ({ value: +t.value || 0, color: String(t.color).trim() }))
      .sort((a, b) => a.value - b.value)
      .slice(0, 16)
  };
  if (decRaw !== "" && !isNaN(+decRaw)) {
    options.decimals = Math.max(0, Math.min(10, parseInt(decRaw, 10)));
  }
  const panel = {
    id: $("panelId").value ? +$("panelId").value : nextPanelId(),
    title, type: ty, datasource: $("panelDS").value,
    grid: { x: 0, y: 9999, w: Math.max(1, Math.min(24, parseInt($("panelW").value) || 12)), h: Math.max(2, parseInt($("panelH").value) || 8) },
    unit: $("panelUnit").value,
    targets, text: $("panelText").value,
    options
  };
  if (options.decimals != null) panel.decimals = options.decimals;
  if (min !== "") panel.min = parseFloat(min);
  if (max !== "") panel.max = parseFloat(max);
  const panels = CUR_DASH.panels;
  rememberDashMutation();
  const existing = panels.findIndex(p => p.id === panel.id);
  if (existing >= 0) {
    panel.grid = panels[existing].grid;
    panel.grid.w = Math.max(1, Math.min(24, parseInt($("panelW").value) || 12));
    panel.grid.h = Math.max(2, parseInt($("panelH").value) || 8);
    panels[existing] = panel;
  } else { placeNewPanel(panel, panels); panels.push(panel); }
  closeMask($("panelEditMask"));
  renderDashDetail();
  // Immediate refresh of the saved panel without waiting for full re-query cycle
  try { loadPanel(panel); } catch (e) {}
});
function nextPanelId() { let m = 0; (CUR_DASH.panels || []).forEach(p => { if (p.id > m) m = p.id; }); return m + 1; }
function placeNewPanel(panel, panels) {
  if (!panel.grid) panel.grid = {};
  const w = Math.max(1, Math.min(24, panel.grid.w || 12));
  const h = Math.max(1, Math.min(48, panel.grid.h || 8));
  const free = findNearestFreeSlot(0, 0, w, h, panel.id, panels);
  panel.grid.x = free.x;
  panel.grid.y = free.y;
  panel.grid.w = w;
  panel.grid.h = h;
}

/* ---------- 变量编辑器 ---------- */
function openVarsEditor() {
  VARS_DRAFT = (CUR_DASH.vars || []).map(v => ({ ...v }));
  renderVarRows();
  openMask("varEditMask");
}
function renderVarRows() {
  const wrap = $("varList");
  if (!wrap) return;
  wrap.innerHTML = VARS_DRAFT.map((v, i) => `
    <div class="var-row">
      <input type="text" data-v-name="${i}" placeholder="变量名（不含$）" value="${esc(v.name || "")}" style="width:110px" title="PromQL 中使用的 $name">
      <input type="text" data-v-label="${i}" placeholder="显示名（可选）" value="${esc(v.label || "")}" style="width:110px" title="筛选按钮上显示的中文名">
      <div class="select-wrap sm"><select data-v-type="${i}">
        <option value="query" ${v.type === "query" ? "selected" : ""}>query</option>
        <option value="custom" ${v.type === "custom" ? "selected" : ""}>custom</option>
        <option value="textbox" ${v.type === "textbox" ? "selected" : ""}>textbox</option>
        <option value="constant" ${v.type === "constant" ? "selected" : ""}>constant</option>
      </select></div>
      <input type="text" class="mono" data-v-query="${i}" placeholder="${v.type === "custom" ? "候选值：a,b,c" : v.type === "query" ? "label_values(node_uname_info, instance)" : "默认值"}" value="${esc(v.type === "custom" ? (v.options || []).join(",") : (v.query || v.current || ""))}" style="flex:1">
      <label class="mini-check" title="多选"><input type="checkbox" data-v-multi="${i}" ${v.multi ? "checked" : ""}>多</label>
      <label class="mini-check" title="含全部"><input type="checkbox" data-v-all="${i}" ${v.include_all ? "checked" : ""}>全</label>
      <button class="mini-btn del" data-v-del="${i}" title="删除">✕</button>
    </div>`).join("") || `<div class="dash-empty">还没有变量</div>`;
}
safeAddEventListener("varAdd", "click", () => { syncVarRows(); VARS_DRAFT.push({ name: "", type: "query", query: "" }); renderVarRows(); });
safeAddEventListener("varList", "click", e => {
  const del = e.target.closest("[data-v-del]");
  if (del) { syncVarRows(); VARS_DRAFT.splice(+del.dataset.vDel, 1); renderVarRows(); }
});
safeAddEventListener("varList", "change", e => { if (e.target.closest("[data-v-type]")) { syncVarRows(); renderVarRows(); } });
function syncVarRows() {
  VARS_DRAFT.forEach((v, i) => {
    const nm = document.querySelector(`[data-v-name="${i}"]`); if (nm) v.name = nm.value.trim();
    const lb = document.querySelector(`[data-v-label="${i}"]`); if (lb) v.label = lb.value.trim();
    const ty = document.querySelector(`[data-v-type="${i}"]`); if (ty) v.type = ty.value;
    const q = document.querySelector(`[data-v-query="${i}"]`);
    if (q) { if (v.type === "custom") { v.options = q.value.split(",").map(s => s.trim()).filter(Boolean); v.query = ""; } else if (v.type === "query") { v.query = q.value.trim(); } else { v.current = q.value.trim(); } }
    const mu = document.querySelector(`[data-v-multi="${i}"]`); if (mu) v.multi = mu.checked;
    const al = document.querySelector(`[data-v-all="${i}"]`); if (al) v.include_all = al.checked;
  });
}
safeAddEventListener("varSave", "click", async () => {
  syncVarRows();
  rememberDashMutation();
  CUR_DASH.vars = VARS_DRAFT.filter(v => v.name);
  closeMask($("varEditMask"));
  await resolveDashVars();
  renderDashDetail();
});

/* ---------- 仪表盘信息（新建 / 编辑元信息 + 外观） ---------- */
let DASH_META_APPEAR = { logo_url: "", background_url: "", background_color: "", background_fit: "cover", panel_opacity: 0 };

function readDashMetaAppearanceFromForm() {
  const color = ($("dashAppearBgColor") && $("dashAppearBgColor").value || "").trim();
  const fit = ($("dashAppearBgFit") && $("dashAppearBgFit").value) || "cover";
  const opEl = $("dashAppearOpacity");
  const opacity = opEl ? (Number(opEl.value) / 100) : 0.92;
  return {
    logo_url: DASH_META_APPEAR.logo_url || "",
    background_url: DASH_META_APPEAR.background_url || "",
    background_color: color,
    background_fit: fit,
    panel_opacity: dashHasCustomBg({ background_url: DASH_META_APPEAR.background_url, background_color: color }) ? opacity : 0
  };
}
function setDashAppearThumb(el, url, wide) {
  if (!el) return;
  if (url) {
    el.innerHTML = `<img src="${esc(url)}" alt="">`;
    el.classList.add("has-img");
  } else {
    el.innerHTML = `<span class="muted">未设置</span>`;
    el.classList.remove("has-img");
  }
  if (wide) el.classList.add("wide");
}
function refreshDashAppearPreview() {
  const a = readDashMetaAppearanceFromForm();
  const title = ($("dashMetaName") && $("dashMetaName").value.trim()) || "看板预览";
  const prevTitle = $("dashAppearPrevTitle");
  if (prevTitle) prevTitle.textContent = title;
  const logo = $("dashAppearPrevLogo");
  if (logo) {
    if (a.logo_url) { logo.src = a.logo_url; logo.hidden = false; }
    else { logo.removeAttribute("src"); logo.hidden = true; }
  }
  const canvas = $("dashAppearPrevCanvas");
  if (canvas) {
    canvas.style.backgroundColor = a.background_color || "var(--bg2)";
    if (a.background_url) {
      canvas.style.backgroundImage = `url("${a.background_url.replace(/"/g, "")}")`;
      canvas.style.backgroundSize = a.background_fit === "contain" ? "contain" : a.background_fit === "repeat" ? "auto" : "cover";
      canvas.style.backgroundRepeat = a.background_fit === "repeat" ? "repeat" : "no-repeat";
      canvas.style.backgroundPosition = "center";
    } else {
      canvas.style.backgroundImage = "none";
    }
  }
  const opField = $("dashAppearOpacityField");
  if (opField) opField.style.display = dashHasCustomBg(a) ? "" : "none";
  const opVal = $("dashAppearOpacityVal");
  const opEl = $("dashAppearOpacity");
  if (opVal && opEl) opVal.textContent = (Number(opEl.value) / 100).toFixed(2);
  setDashAppearThumb($("dashAppearLogoThumb"), a.logo_url, false);
  setDashAppearThumb($("dashAppearBgThumb"), a.background_url, true);
}
function setDashMetaUploadEnabled(enabled) {
  ["dashAppearLogoPick", "dashAppearBgPick"].forEach(id => {
    const b = $(id); if (b) b.disabled = !enabled;
  });
  const hint = $("dashAppearUploadHint");
  if (hint) hint.style.display = enabled ? "none" : "";
}
async function uploadDashAppearAsset(kind, file) {
  const id = ($("dashMetaId") && $("dashMetaId").value) || "";
  if (!id) { toast("请先保存看板后再上传图片", "err"); return null; }
  if (!file) return null;
  const max = kind === "logo" ? 512 * 1024 : 2 * 1024 * 1024;
  if (file.size > max) { toast(kind === "logo" ? "Logo 不能超过 512KB" : "背景图不能超过 2MB", "err"); return null; }
  const fd = new FormData();
  fd.append("file", file);
  fd.append("kind", kind);
  try {
    const r = await fetch(`${API}/dashboards/${encodeURIComponent(id)}/assets`, { method: "POST", body: fd });
    const j = await r.json().catch(() => ({}));
    if (!r.ok || !j.url) { toast("上传失败：" + ((j && j.error) || r.status), "err"); return null; }
    return j.url;
  } catch (e) {
    toast("上传失败：" + e, "err");
    return null;
  }
}
function bindDashAppearMetaOnce() {
  if (bindDashAppearMetaOnce.bound) return;
  bindDashAppearMetaOnce.bound = true;
  const syncColorPick = () => {
    const t = $("dashAppearBgColor");
    const p = $("dashAppearBgColorPick");
    if (!t || !p) return;
    const v = (t.value || "").trim();
    if (/^#[0-9a-fA-F]{6}$/.test(v)) p.value = v;
    refreshDashAppearPreview();
  };
  safeAddEventListener("dashAppearBgColor", "input", syncColorPick);
  safeAddEventListener("dashAppearBgColorPick", "input", () => {
    const t = $("dashAppearBgColor");
    const p = $("dashAppearBgColorPick");
    if (t && p) t.value = p.value;
    refreshDashAppearPreview();
  });
  safeAddEventListener("dashAppearBgColorClear", "click", () => {
    if ($("dashAppearBgColor")) $("dashAppearBgColor").value = "";
    refreshDashAppearPreview();
  });
  safeAddEventListener("dashAppearBgFit", "change", refreshDashAppearPreview);
  safeAddEventListener("dashAppearOpacity", "input", refreshDashAppearPreview);
  safeAddEventListener("dashMetaName", "input", refreshDashAppearPreview);
  safeAddEventListener("dashAppearLogoPick", "click", () => { const f = $("dashAppearLogoFile"); if (f) f.click(); });
  safeAddEventListener("dashAppearBgPick", "click", () => { const f = $("dashAppearBgFile"); if (f) f.click(); });
  safeAddEventListener("dashAppearLogoFile", "change", async e => {
    const file = e.target.files && e.target.files[0];
    e.target.value = "";
    const url = await uploadDashAppearAsset("logo", file);
    if (url) { DASH_META_APPEAR.logo_url = url; refreshDashAppearPreview(); toast("Logo 已上传", "ok"); }
  });
  safeAddEventListener("dashAppearBgFile", "change", async e => {
    const file = e.target.files && e.target.files[0];
    e.target.value = "";
    const url = await uploadDashAppearAsset("background", file);
    if (url) { DASH_META_APPEAR.background_url = url; refreshDashAppearPreview(); toast("背景图已上传", "ok"); }
  });
  safeAddEventListener("dashAppearLogoClear", "click", () => {
    DASH_META_APPEAR.logo_url = "";
    refreshDashAppearPreview();
  });
  safeAddEventListener("dashAppearBgClear", "click", () => {
    DASH_META_APPEAR.background_url = "";
    refreshDashAppearPreview();
  });
}
function openDashMeta(d) {
  bindDashAppearMetaOnce();
  $("dashMetaId").value = d ? d.id : "";
  $("dashMetaName").value = d ? d.name : "";
  $("dashMetaDesc").value = d ? (d.description || "") : "";
  $("dashMetaTags").value = d && d.tags ? d.tags.join(",") : "";
  $("dashMetaTitle").textContent = d ? "编辑仪表盘信息" : "新建仪表盘";
  const a = dashAppearance(d);
  DASH_META_APPEAR = { ...a };
  if ($("dashAppearBgColor")) $("dashAppearBgColor").value = a.background_color || "";
  if ($("dashAppearBgColorPick")) {
    const hex = /^#[0-9a-fA-F]{6}$/.test(a.background_color || "") ? a.background_color : "#1a1f2e";
    $("dashAppearBgColorPick").value = hex;
  }
  if ($("dashAppearBgFit")) $("dashAppearBgFit").value = a.background_fit || "cover";
  if ($("dashAppearOpacity")) $("dashAppearOpacity").value = String(Math.round((a.panel_opacity || 0.92) * 100));
  setDashMetaUploadEnabled(!!(d && d.id));
  refreshDashAppearPreview();
  openMask("dashMetaMask");
}
safeAddEventListener("dashMetaSave", "click", async () => {
  const id = $("dashMetaId").value;
  const name = $("dashMetaName").value.trim();
  if (!name) { toast("请填写名称", "err"); return; }
  const tags = $("dashMetaTags").value.split(",").map(s => s.trim()).filter(Boolean);
  const desc = $("dashMetaDesc").value.trim();
  const appearance = readDashMetaAppearanceFromForm();
  // 编辑当前打开的仪表盘元信息（在内存里改，随保存落盘）
  if (CUR_DASH && CUR_DASH.id === id && id) {
    if (DASH_EDIT) rememberDashMutation();
    CUR_DASH.name = name; CUR_DASH.description = desc; CUR_DASH.tags = tags;
    CUR_DASH.appearance = appearance;
    closeMask($("dashMetaMask"));
    if (DASH_EDIT) renderDashDetail(); else saveCurDash();
    return;
  }
  // 从列表编辑信息：必须先拉到完整对象（含 panels），失败则中止——绝不能用 panels:[] 覆盖。
  await withLoading("dashMetaSave", async () => {
    let base;
    if (id) {
      try {
        const gr = await fetch(`${API}/dashboards/${encodeURIComponent(id)}`);
        const gj = await gr.json().catch(() => ({}));
        if (!gr.ok || !gj || !gj.id || !Array.isArray(gj.panels)) {
          toast("保存失败：无法加载原仪表盘（已中止，避免清空面板）", "err");
          return;
        }
        base = gj;
      } catch (e) {
        toast("保存失败：加载原仪表盘出错（已中止）—" + e, "err");
        return;
      }
      base.name = name; base.description = desc; base.tags = tags; base.appearance = appearance;
    } else {
      base = { name, description: desc, tags, panels: [], appearance };
    }
    try {
      const r = await fetch(`${API}/dashboards`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(base) });
      const j = await r.json().catch(() => ({}));
      if (r.ok && j && j.ok) {
        closeMask($("dashMetaMask"));
        toast("已保存", "ok");
        if (!id) { openDashboard(j.id).then(() => { DASH_EDIT = true; renderDashDetail(); }); }
        else {
          if (CUR_DASH && CUR_DASH.id === id) {
            CUR_DASH.name = name; CUR_DASH.description = desc; CUR_DASH.tags = tags; CUR_DASH.appearance = appearance;
            if (j.revision) CUR_DASH.revision = j.revision;
            renderDashDetail();
          }
          loadDashboards();
        }
      } else toast("保存失败：" + ((j && j.error) || ("HTTP " + r.status)), "err");
    } catch (e) { toast("保存失败：" + e, "err"); }
  });
});
async function saveCurDash() {
  if (!CUR_DASH) return;
  await withLoading("dashSaveBtn", async () => {
    try {
      const r = await fetch(`${API}/dashboards`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(CUR_DASH) });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        if (j.revision) CUR_DASH.revision = j.revision;
        if (j.updated_at) CUR_DASH.updated_at = j.updated_at;
        toast("已保存", "ok"); DASH_EDIT = false; resetDashEditHistory(); renderDashDetail();
      } else if (r.status === 409) {
        toast("保存冲突：远端版本已更新。当前修改仍保留，请先另存或刷新后合并。", "err");
      } else {
        toast("保存失败：" + (j.error || r.status), "err");
      }
    } catch (e) { toast("保存失败：" + e, "err"); }
  });
}

/* ---------- Grafana 导入 ---------- */
safeAddEventListener("dashImportBtn", "click", () => {
  $("dashImportId").value = ""; $("dashImportJson").value = ""; $("dashImportName").value = "";
  const fn = $("dashImportFileName"); if (fn) fn.textContent = "";
  const fmt = $("dashImportFormat"); if (fmt) fmt.value = "auto";
  const fi = $("dashImportFile"); if (fi) fi.value = "";
  openMask("dashImportMask");
});
safeAddEventListener("dashImportFileBtn", "click", () => { const fi = $("dashImportFile"); if (fi) fi.click(); });
safeAddEventListener("dashImportFile", "change", e => {
  const f = e.target.files && e.target.files[0];
  if (!f) return;
  if (f.size > 8 * 1024 * 1024) { toast("文件过大（上限 8MB）", "err"); return; }
  const reader = new FileReader();
  reader.onload = () => { $("dashImportJson").value = reader.result || ""; const fn = $("dashImportFileName"); if (fn) fn.textContent = "已载入：" + f.name; };
  reader.onerror = () => toast("读取文件失败", "err");
  reader.readAsText(f);
});
safeAddEventListener("dashImportSave", "click", async () => {
  const body = { grafana_id: $("dashImportId").value.trim(), json: $("dashImportJson").value.trim(), name: $("dashImportName").value.trim(), format: ($("dashImportFormat") || {}).value || "auto" };
  if (!body.grafana_id && !body.json) { toast("请填写 grafana.com 看板 ID，或粘贴 / 上传 JSON", "err"); return; }
  await withLoading("dashImportSave", async () => {
    try {
      const r = await fetch(`${API}/dashboards/import-grafana`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      const j = await r.json().catch(() => ({}));
      if (r.ok && j.ok) {
        closeMask($("dashImportMask"));
        const kind = j.format === "nightingale" ? "兼容看板" : (j.format === "aiops" ? "AIOps 模板" : "Grafana");
        toast(`已从 ${kind} 导入「${j.name}」：${j.panels} 面板${j.unsupported ? "（" + j.unsupported + " 个类型不支持，已占位）" : ""}`, "ok");
        openDashboard(j.id);
      } else toast("导入失败：" + (j.error || r.status), "err");
    } catch (e) { toast("导入失败：" + e, "err"); }
  });
});

/* ---------- AI 闭环：生成 / 解读 / 优化 / 建工单 ---------- */
// panelDigest：抓取单个面板当前数据并汇成文本（用当前选中的模板变量值，保证与所见一致）。
async function panelDigest(p) {
  let s = "面板：" + (p.title || "(未命名)") + "　类型：" + (p.type || "") + (p.unit ? "　单位：" + p.unit : "") + "\n";
  if (p.type === "alertlist") {
    try { const al = await fetch(`${API}/alerts`).then(r => r.json()); const arr = Array.isArray(al) ? al : []; s += "当前平台告警 " + arr.length + " 条：\n" + arr.slice(0, 30).map(a => `- [${a.level}] ${a.message || a.type || ""} ${a.hostname || ""}`).join("\n"); }
    catch (e) { s += "（告警读取失败）"; }
    return s;
  }
  if (!(p.targets || []).length) return s + "（无查询）";
  if (p.type === "logs") {
    try {
      const range = dashRange();
      const r = await fetch(`${API}/dashboards/query-logs`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ expr: p.targets[0].expr, from: range.from, to: range.to, limit: 20, datasource: resolveDS(p), vars: panelVars() })
      }).then(r => r.json());
      if (r && r.available === false) return s + "（日志数据源不可用）";
      s += `当前范围命中 ${(r.lines || []).length} 条（为防敏感信息外发，AI 摘要不自动携带日志正文）`;
    } catch (e) { s += "（日志统计读取失败）"; }
    return s;
  }
  const temporal = ["timeseries", "state-timeline", "statetimeline", "heatmap", "histogram", "stat", "graph"].includes(p.type);
  const targets = (p.targets || []).slice(0, 3);
  const thr = (dashPanelOpt(p).thresholds || []).map(x => +x.value).filter(n => !isNaN(n));
  if (thr.length) s += `阈值：${thr.join(" / ")}\n`;
  for (let ti = 0; ti < targets.length; ti++) {
    const target = targets[ti];
    try {
      if (temporal) {
        const range = dashRange();
        const r = await fetch(`${API}/dashboards/query`, {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ expr: target.expr, from: range.from, to: range.to, datasource: resolveDS(p), vars: panelVars() })
        }).then(r => r.json());
        if (r && r.available === false) return s + "（数据源不可用）";
        const vec = (r && r.series) || [];
        if (!vec.length) { s += `- 查询 ${ti + 1}：当前范围无数据\n`; continue; }
        s += vec.slice(0, 12).map(x => {
          const pts = (x.points || []).map(pt => +pt[1]).filter(Number.isFinite);
          const lbl = legendFor(target.legend, x.labels || {}) || "value";
          if (!pts.length) return `- ${lbl}：无有效样本`;
          const first = pts[0], last = pts[pts.length - 1];
          const minV = Math.min(...pts), maxV = Math.max(...pts), avg = pts.reduce((a, b) => a + b, 0) / pts.length;
          const delta = last - first;
          const trend = Math.abs(first) > 1e-9 ? `${delta >= 0 ? "+" : ""}${(delta / Math.abs(first) * 100).toFixed(1)}%` : `${delta >= 0 ? "+" : ""}${fmtUnit(delta, p.unit)}`;
          let base = `- ${lbl}：当前 ${fmtUnit(last, p.unit)}；均值 ${fmtUnit(avg, p.unit)}；范围 ${fmtUnit(minV, p.unit)} ~ ${fmtUnit(maxV, p.unit)}；区间变化 ${trend}；样本 ${pts.length}`;
          if (thr.length) {
            const hit = thr.filter(tv => last >= tv);
            if (hit.length) base += `；已达/超过阈值 ${hit[hit.length - 1]}`;
            else {
              const next = thr.find(tv => tv > last);
              if (next != null) base += `；距最近阈值 ${fmtUnit(next - last, p.unit)}`;
            }
          }
          return base;
        }).join("\n") + "\n";
      } else {
        const r = await fetch(`${API}/dashboards/query-instant`, {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ expr: target.expr, datasource: resolveDS(p), vars: panelVars() })
        }).then(r => r.json());
        if (r && r.available === false) return s + "（数据源不可用）";
        const vec = (r && r.series) || [];
        if (!vec.length) { s += `- 查询 ${ti + 1}：当前无数据\n`; continue; }
        const rows = vec.map(x => ({
          lbl: legendFor(target.legend, seriesLabels(x)) || "value",
          val: seriesVal2(x)
        })).filter(x => Number.isFinite(+x.val));
        rows.sort((a, b) => b.val - a.val);
        if (p.type === "table" || p.type === "bargauge" || p.type === "barchart") {
          s += `Top-N（前 ${Math.min(10, rows.length)}）：\n`;
          s += rows.slice(0, 10).map((x, i) => `- #${i + 1} ${x.lbl}：${fmtUnit(x.val, p.unit)}`).join("\n") + "\n";
          if (rows.length >= 3) {
            const vals = rows.map(x => +x.val);
            const avg = vals.reduce((a, b) => a + b, 0) / vals.length;
            const maxV = vals[0], minV = vals[vals.length - 1];
            s += `分布：最大 ${fmtUnit(maxV, p.unit)}，最小 ${fmtUnit(minV, p.unit)}，均值 ${fmtUnit(avg, p.unit)}；异常偏高行：` +
              rows.filter(x => x.val > avg * 1.5 || (thr.length && x.val >= thr[thr.length - 1])).slice(0, 5).map(x => x.lbl).join("、") + "\n";
          }
        } else {
          s += rows.slice(0, 20).map(x => {
            let line = `- ${x.lbl}：${fmtUnit(x.val, p.unit)}`;
            if ((p.type === "stat" || p.type === "gauge") && thr.length) {
              const breached = thr.filter(tv => x.val >= tv);
              line += breached.length ? `（超过阈值 ${breached[breached.length - 1]}）` : "（未越阈）";
            }
            return line;
          }).join("\n") + "\n";
        }
      }
    } catch (e) { s += `- 查询 ${ti + 1}：读取失败\n`; }
  }
  return s;
}
// aiAnalyzePanel：对单个面板实时数据做 AI 解读（复用 /ai/assist chart_analysis）。
async function aiAnalyzePanel(p) {
  if (!p) return;
  const digest = await panelDigest(p);
  openAIAssist({ task: "chart_analysis", title: "🔍 AI 解读 · " + (p.title || "面板"), mode: "analyze", context: digest, hint: "AI 正在解读该面板数据…" });
}
// buildDashDigestClient：在前端逐面板抓取当前数据汇成看板级摘要——关键：用真实选中的变量值
// （DASH_VARVALS），修复服务端摘要因 d.Vars.Current 为空、$变量被替换成空而查不到数据的问题。
async function buildDashDigestClient() {
  if (!CUR_DASH) return "";
  const panels = (CUR_DASH.panels || []).filter(p => p.type !== "text" && p.type !== "unsupported").slice(0, 40);
  let out = "看板：" + CUR_DASH.name + "\n";
  const sel = (CUR_DASH.vars || []).map(v => v.name + "=" + (DASH_VARVALS[v.name] || "")).filter(x => !x.endsWith("=")).join(", ");
  if (sel) out += "当前变量：" + sel + "\n";
  const parts = await dashMapLimit(panels, 6, p => panelDigest(p).catch(() => ""));
  return out + "\n" + parts.filter(Boolean).join("\n\n");
}
async function dashMapLimit(items, limit, fn) {
  const out = new Array(items.length);
  let cursor = 0;
  const workers = Array.from({ length: Math.max(1, Math.min(limit || 1, items.length || 1)) }, async () => {
    while (true) {
      const i = cursor++;
      if (i >= items.length) return;
      out[i] = await fn(items[i], i);
    }
  });
  await Promise.all(workers);
  return out;
}
// dashStructureClient：看板结构（面板类型/查询/单位），供 AI 优化审阅。
function dashStructureClient() {
  if (!CUR_DASH) return "";
  let s = "看板结构：" + CUR_DASH.name + "\n";
  if ((CUR_DASH.vars || []).length) s += "模板变量：" + CUR_DASH.vars.map(v => v.name + "(" + v.type + ")").join(", ") + "\n";
  (CUR_DASH.panels || []).forEach(p => {
    s += `- [${p.type}] ${p.title || ""}` + (p.unit ? " 单位=" + p.unit : "") + "\n";
    (p.targets || []).forEach(t => { s += "    " + t.expr + "\n"; });
  });
  return s;
}
async function aiAnalyzeDash() {
  if (!CUR_DASH) return;
  toast("读取各面板数据…", "ok");
  const digest = await buildDashDigestClient();
  openAIAssist({ task: "dashboard_analysis", title: "AI 诊断报告 · " + CUR_DASH.name, mode: "analyze", context: digest, hint: "正在结合趋势、当前水位与历史经验生成诊断报告…" });
}
async function aiOptimizeDash() {
  if (!CUR_DASH) return;
  if (DASH_EDIT && DASH_DIRTY) {
    toast("请先保存当前人工修改，再运行 AI 优化，避免覆盖未保存内容。", "warn");
    return;
  }
  toast("读取各面板数据…", "ok");
  const dashId = CUR_DASH.id;
  let metricsHint = "";
  try {
    const metrics = (await fetchMetricNames(CUR_DASH.datasource || "")).slice(0, 200);
    if (metrics.length) metricsHint = "\n\n【平台可用指标（节选，只能用这些真实指标，不要臆造 node_*）】\n" + metrics.join(", ");
  } catch (e) {}
  const ctx = dashStructureClient() + "\n\n【各面板实时数据】\n" + (await buildDashDigestClient()) + metricsHint;
  openAIAssist({
    task: "dashboard_optimize", title: "✨ AI 优化 · " + CUR_DASH.name, mode: "analyze",
    context: ctx, hint: "AI 正在评审看板并给出优化建议…",
    applyLabel: "应用优化到看板",
    applyTo: async (code) => {
      // 用完整回复（而非仅首个代码块）：AI 可能在 json 前先给了 PromQL 代码块，只取首块会拿错内容；
      // 服务端 extractJSONObject 会优先定位 ```json 块，更稳。
      const answer = (typeof _aiAssistState !== "undefined" && _aiAssistState.lastAnswer) || code || "";
      if (!answer.trim()) { toast("请先等 AI 给出优化建议再应用", "err"); return false; }
      if (!/\{[\s\S]*["']panels["']\s*:/i.test(answer) && !/```json/i.test(answer) && !/"dashboard"\s*:\s*\{/.test(answer)) {
        toast("应用失败：回复里没有可识别的看板 JSON（需含 panels）。请点「重新生成」后再试。", "err");
        return false;
      }
      toast("正在校验查询并生成差异预览…", "ok");
      try {
        const r = await fetch(`${API}/dashboards/${encodeURIComponent(dashId)}/ai-apply`, {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ json: answer, preview_only: true })
        });
        const j = await r.json().catch(() => ({}));
        if (!r.ok || !j.ok) {
          toast("预览失败：" + (j.error || ("HTTP " + r.status) || "AI 未给出可应用的看板结构，请重新生成"), "err");
          return false;
        }
        return await reviewAndApplyDashAI({
          dashId, answer, expectedRevision: j.current_revision || 0, preview: j
        });
      } catch (e) { toast("预览失败：" + e, "err"); return false; }
    }
  });
}

function renderDashDiffList(items, emptyText) {
  const arr = Array.isArray(items) ? items : [];
  return arr.length ? `<ul>${arr.map(x => `<li>${esc(x)}</li>`).join("")}</ul>` : `<span class="muted">${esc(emptyText || "无")}</span>`;
}
function reviewAndApplyDashAI(review) {
  DASH_AI_REVIEW = review;
  const d = review.preview.diff || {};
  $("dashAIReviewSummary").innerHTML = `
    <div class="dash-review-kpis">
      <div><b>${d.before == null ? "—" : d.before}</b><span>原组件</span></div>
      <div><b>${d.after == null ? "—" : d.after}</b><span>优化后</span></div>
      <div><b>${(d.changed || []).length}</b><span>调整</span></div>
      <div><b>${(review.preview.dry_run_empty || []).length}</b><span>当前无数据</span></div>
    </div>`;
  $("dashAIReviewAdded").innerHTML = renderDashDiffList(d.added, "无新增组件");
  $("dashAIReviewChanged").innerHTML = renderDashDiffList(d.changed, "无结构调整");
  $("dashAIReviewRemoved").innerHTML = renderDashDiffList(d.removed, "无删除组件");
  const warnings = [...(review.preview.warnings || [])];
  if ((review.preview.dry_run_empty || []).length) warnings.push("无数据组件：" + review.preview.dry_run_empty.join("、"));
  const warn = $("dashAIReviewWarnings");
  warn.innerHTML = warnings.length ? warnings.map(x => `<div>⚠ ${esc(x)}</div>`).join("") : "✓ 查询干跑通过，未发现阻断项";
  warn.className = "dash-review-warnings " + (warnings.length ? "warn" : "ok");
  openMask("dashAIReviewMask");
  return new Promise(resolve => { DASH_AI_REVIEW_RESOLVE = resolve; });
}
function finishDashAIReview(value) {
  closeMask($("dashAIReviewMask"));
  const resolve = DASH_AI_REVIEW_RESOLVE;
  DASH_AI_REVIEW_RESOLVE = null;
  DASH_AI_REVIEW = null;
  if (resolve) resolve(value);
}
safeAddEventListener("dashAIReviewCancel", "click", () => finishDashAIReview(false));
safeAddEventListener("dashAIReviewClose", "click", () => finishDashAIReview(false));
safeAddEventListener("dashAIReviewMask", "click", e => {
  if (e.target && e.target.id === "dashAIReviewMask") finishDashAIReview(false);
});
safeAddEventListener("dashAIReviewApply", "click", async () => {
  const review = DASH_AI_REVIEW;
  if (!review) return;
  await withLoading("dashAIReviewApply", async () => {
    try {
      const r = await fetch(`${API}/dashboards/${encodeURIComponent(review.dashId)}/ai-apply`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ json: review.answer, expected_revision: review.expectedRevision || 0 })
      });
      const j = await r.json().catch(() => ({}));
      if (r.status === 409 || (j && j.error && /已被更新|重新/.test(j.error))) {
        toast("应用失败：" + (j.error || "看板已变更，请关闭后重新预览"), "err");
        finishDashAIReview(false);
        return;
      }
      if (!r.ok || !j.ok) {
        toast("应用失败：" + (j.error || ("HTTP " + r.status)), "err");
        finishDashAIReview(false);
        return;
      }
      const w = (j.warnings && j.warnings.length) ? "（" + j.warnings.slice(0, 2).join("；") + "）" : "";
      toast(`已应用优化：${j.panels} 个组件${w}`, "ok");
      finishDashAIReview(true);
      if (CUR_DASH && CUR_DASH.id === review.dashId) await openDashboard(review.dashId);
    } catch (e) {
      toast("应用失败：" + e, "err");
      finishDashAIReview(false);
    }
  });
});
async function aiTicketDash() {
  if (!CUR_DASH) return;
  await withLoading("dashTicketBtn", async () => {
    const digest = await buildDashDigestClient();
    try {
      const r = await fetch(`${API}/dashboards/${encodeURIComponent(CUR_DASH.id)}/ai-ticket`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ digest }) });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) { toast("工单草案生成失败：" + (j.error || r.status), "err"); return; }
      if (j.ok && j.needed && j.preview && j.draft) {
        DASH_TICKET_DRAFT = { dashId: CUR_DASH.id, digest };
        $("dashTicketTitle").value = j.draft.title || "";
        $("dashTicketPriority").value = j.draft.priority || "p3";
        $("dashTicketSummary").value = j.draft.summary || "";
        openMask("dashTicketPreviewMask");
      }
      else if (j.ok && !j.needed) toast(j.message || "AI 研判当前无明显异常，未建工单", "ok");
      else toast("工单草案生成失败：" + (j.error || ""), "err");
    } catch (e) { toast("建工单失败：" + e, "err"); }
  });
}
safeAddEventListener("dashTicketConfirm", "click", async () => {
  if (!DASH_TICKET_DRAFT) return;
  const title = $("dashTicketTitle").value.trim();
  const priority = $("dashTicketPriority").value;
  const summary = $("dashTicketSummary").value.trim();
  if (!title) { toast("请填写工单标题", "err"); $("dashTicketTitle").focus(); return; }
  await withLoading("dashTicketConfirm", async () => {
    try {
      const r = await fetch(`${API}/dashboards/${encodeURIComponent(DASH_TICKET_DRAFT.dashId)}/ai-ticket`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ confirm: true, title, priority, summary, digest: DASH_TICKET_DRAFT.digest })
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok || !j.ok) { toast("建单失败：" + (j.error || r.status), "err"); return; }
      closeMask($("dashTicketPreviewMask"));
      DASH_TICKET_DRAFT = null;
      toast(`已建工单 #${j.ticket_id}（${j.priority}）：${j.title}`, "ok");
    } catch (e) { toast("建单失败：" + e, "err"); }
  });
});

function dashboardReportModel(liveRows) {
  const d = CUR_DASH;
  const range = dashRange();
  const vars = Object.entries(DASH_VARVALS || {}).map(([k, v]) => `${k}=${v || "（空）"}`).join("；") || "无";
  const panels = (d.panels || []).slice().sort((a, b) => (a.grid.y - b.grid.y) || (a.grid.x - b.grid.x));
  const sections = [{
    title: "组件与查询配置",
    columns: ["组件", "类型", "数据源", "单位", "查询"],
    rows: panels.map(p => [
      p.title || "（未命名）", p.type || "", dsLabel(resolveDS(p)), p.unit || "",
      (p.targets || []).map(t => t.expr).join("\n") || (p.type === "text" ? "文本内容" : "无查询")
    ])
  }];
  if (liveRows && liveRows.length) {
    sections.unshift({
      title: "实时诊断快照", columns: ["组件", "诊断摘要"],
      rows: liveRows.map(x => [x.title, x.digest.replace(/\s*\n\s*/g, " · ").replace(/\s+/g, " ").trim()])
    });
  }
  return {
    title: "可观测性诊断报告 · " + d.name,
    subtitle: "AIOps · " + new Date().toLocaleString(),
    summaryTitle: "报告概览",
    meta: [
      ["看板", d.name], ["数据源", dsLabel(d.datasource || "")],
      ["时间范围", `${new Date(range.from * 1000).toLocaleString()} — ${new Date(range.to * 1000).toLocaleString()}`],
      ["模板变量", vars], ["组件数量", String(panels.length)],
      ["版本", String(d.revision || 0)], ["生成时间", new Date().toLocaleString()]
    ],
    kpis: [["组件", String(panels.length)], ["数据源", dsLabel(d.datasource || "")], ["实时快照", liveRows ? liveRows.length + " 项" : "未包含"]],
    narrativeTitle: "看板说明",
    narrative: d.description || "该报告由当前看板配置与所选时间范围的数据快照生成。可在看板中运行“AI 诊断”获得可追问的根因分析与处置建议。",
    sections,
    orientation: "landscape",
    footer: "数据快照受采集延迟、数据源可用性和当前模板变量影响；AI 诊断结论需结合变更记录、日志与业务影响人工复核。"
  };
}

function dashExportMeta() {
  const d = CUR_DASH;
  const range = dashRange();
  const vars = Object.entries(DASH_VARVALS || {}).map(([k, v]) => `${k}=${v || "（空）"}`).join("；") || "无";
  return [
    ["看板", d.name],
    ["数据源", dsLabel(d.datasource || "")],
    ["时间范围", `${new Date(range.from * 1000).toLocaleString()} — ${new Date(range.to * 1000).toLocaleString()}`],
    ["模板变量", vars],
    ["组件数量", String((d.panels || []).length)],
    ["导出时间", new Date().toLocaleString()]
  ];
}

// 导出前静止图表入场动画，避免截到半截曲线。
async function prepareDashChartsForExport() {
  for (const id in DASH_CHART_ARGS) {
    try {
      const args = DASH_CHART_ARGS[id].slice();
      const opts = Object.assign({}, args[5] || {}, { noEntrance: true });
      args[5] = opts;
      createChart.apply(null, args);
    } catch (e) {}
  }
  await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)));
  await new Promise(r => setTimeout(r, 60));
}

async function captureDashVisualPanels() {
  await prepareDashChartsForExport();
  const panels = (CUR_DASH.panels || []).slice().sort((a, b) => (a.grid.y - b.grid.y) || (a.grid.x - b.grid.x));
  document.body.classList.add("dash-exporting");
  try {
    const out = [];
    for (const p of panels) {
      const g = dashGridBox(p);
      const el = document.querySelector(`[data-panel="${CSS.escape ? CSS.escape(p.id) : p.id}"]`)
        || document.querySelector(`[data-panel="${p.id}"]`);
      let dataUrl = "";
      if (el && typeof expCaptureElement === "function") {
        try { dataUrl = await expCaptureElement(el, 2); } catch (e) { dataUrl = ""; }
      }
      if (!dataUrl) {
        const body = document.getElementById("panelBody_" + p.id);
        const canvas = body && body.querySelector("canvas");
        if (canvas && canvas.width) {
          try { dataUrl = canvas.toDataURL("image/png"); } catch (e) {}
        }
      }
      out.push({
        id: p.id, title: p.title || "（未命名）", type: p.type || "",
        x: g.x, y: g.y, w: g.w, h: g.h,
        dataUrl, empty: dataUrl ? "" : "面板暂无可截取内容"
      });
    }
    return out;
  } finally {
    document.body.classList.remove("dash-exporting");
  }
}

async function buildDashVisualModel() {
  const d = CUR_DASH;
  const grid = $("dashGrid");
  const m = grid ? dashGridMetrics(grid) : { colW: 48, rowH: 28, gap: 6, cols: 24 };
  const visualPanels = await captureDashVisualPanels();
  return {
    kind: "visual",
    title: d.name,
    subtitle: "看板视觉导出 · " + new Date().toLocaleString(),
    summaryTitle: "看板信息",
    meta: dashExportMeta(),
    visualPanels,
    cols: m.cols || 24,
    colW: Math.max(28, Math.round(m.colW || 48)),
    rowH: Math.max(20, Math.round(m.rowH || 28)),
    gap: Math.max(2, Math.round(m.gap || 6)),
    scale: 2,
    orientation: "landscape",
    footer: "本文件为看板视觉成品截图；数值以导出时刻界面渲染为准。如需原始时序/表格数据请改用 Excel 导出。"
  };
}

function dashIsTemporalType(t) {
  return ["timeseries", "state-timeline", "statetimeline", "heatmap", "histogram", "stat"].includes(t);
}

function dashFmtTs(sec) {
  if (sec == null || !Number.isFinite(+sec)) return "";
  const d = new Date((+sec) * (sec > 1e12 ? 1 : 1000));
  if (sec > 1e12) return new Date(+sec).toLocaleString();
  return d.toLocaleString();
}

// 单面板 → Excel 工作表（原始数据，而非诊断摘要）。
async function panelDataSheet(p) {
  if (!p || p.type === "unsupported") return null;
  const title = (p.title || p.type || "面板").slice(0, 28);
  if (p.type === "text") {
    const lines = String(p.text || "").split(/\r?\n/);
    return { title, columns: ["行", "内容"], rows: lines.map((line, i) => [i + 1, line]) };
  }
  if (p.type === "alertlist") {
    try {
      let alerts = await fetch(`${API}/alerts`).then(r => r.json());
      alerts = Array.isArray(alerts) ? alerts : [];
      const kw = ((p.targets && p.targets[0] && p.targets[0].expr) || "").trim().toLowerCase();
      if (kw) alerts = alerts.filter(a => JSON.stringify(a).toLowerCase().includes(kw));
      return {
        title,
        columns: ["级别", "消息", "主机", "类型"],
        rows: alerts.slice(0, 2000).map(a => [a.level || "", a.message || "", a.hostname || "", a.type || ""])
      };
    } catch (e) {
      return { title, columns: ["错误"], rows: [["告警读取失败"]] };
    }
  }
  if (!(p.targets || []).length) {
    return { title, columns: ["提示"], rows: [["未配置查询"]] };
  }
  if (p.type === "logs") {
    try {
      const range = dashRange();
      const r = await fetch(`${API}/dashboards/query-logs`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ expr: p.targets[0].expr, from: range.from, to: range.to, limit: 2000, datasource: resolveDS(p), vars: panelVars() })
      }).then(r => r.json());
      if (r && r.available === false) return { title, columns: ["提示"], rows: [["日志数据源不可用"]] };
      const lines = r.lines || [];
      return {
        title,
        columns: ["时间", "日志"],
        rows: lines.map(l => [fmtLogTs(l.ts_ms), l.line || ""])
      };
    } catch (e) {
      return { title, columns: ["错误"], rows: [["日志查询失败"]] };
    }
  }

  const range = dashRange();
  const temporal = dashIsTemporalType(p.type) || p.type === "timeseries";
  const collected = []; // { legend, points: [[ts,v]] } or instant { legend, value, labels }

  for (let ti = 0; ti < (p.targets || []).length; ti++) {
    const target = p.targets[ti];
    try {
      if (temporal && p.type !== "histogram") {
        const r = await fetch(`${API}/dashboards/query`, {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ expr: target.expr, from: range.from, to: range.to, datasource: resolveDS(p), vars: panelVars() })
        }).then(r => r.json());
        if (r && r.available === false) {
          return { title, columns: ["提示"], rows: [["数据源不可用：" + dsLabel(resolveDS(p))]] };
        }
        for (const s of (r && r.series) || []) {
          if (collected.length >= 48) break;
          const lbl = legendFor(target.legend, s.labels || {}) || ("series_" + (collected.length + 1));
          collected.push({ legend: lbl, points: s.points || [] });
        }
      } else {
        const r = await fetch(`${API}/dashboards/query-instant`, {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ expr: target.expr, datasource: resolveDS(p), vars: panelVars() })
        }).then(r => r.json());
        if (r && r.available === false) {
          return { title, columns: ["提示"], rows: [["数据源不可用：" + dsLabel(resolveDS(p))]] };
        }
        for (const s of (r && r.series) || []) {
          if (collected.length >= 500) break;
          const labels = seriesLabels(s);
          const lbl = legendFor(target.legend, labels) || ("series_" + (collected.length + 1));
          collected.push({ legend: lbl, value: seriesVal2(s), labels });
        }
      }
    } catch (e) { /* skip target */ }
  }

  if (!collected.length) return { title, columns: ["提示"], rows: [["当前范围无数据"]] };

  // 即时值：序列 | 原始值 | 格式化值 | 标签
  if (!collected[0].points) {
    return {
      title,
      columns: ["序列", "原始值", "显示值", "标签"],
      rows: collected.map(c => [
        c.legend,
        Number.isFinite(c.value) ? String(c.value) : "",
        fmtUnit(c.value, p.unit),
        Object.entries(c.labels || {}).map(([k, v]) => k + "=" + v).join(", ")
      ])
    };
  }

  // 时序：宽表 时间 | s1 | s2 ...（点数过多时均匀降采样，保证 Excel 可打开）
  const maxPts = 4000;
  const tsMap = new Map();
  collected.forEach((c, i) => {
    const key = "s" + i;
    for (const pt of c.points) {
      const ts = Math.round(pt[0]);
      let row = tsMap.get(ts);
      if (!row) { row = { ts }; tsMap.set(ts, row); }
      row[key] = pt[1];
    }
  });
  let stamps = [...tsMap.keys()].sort((a, b) => a - b);
  if (stamps.length > maxPts) {
    const step = stamps.length / maxPts;
    const kept = [];
    for (let i = 0; i < maxPts; i++) kept.push(stamps[Math.floor(i * step)]);
    stamps = kept;
  }
  const columns = ["时间", ...collected.map(c => c.legend)];
  const rows = stamps.map(ts => {
    const row = tsMap.get(ts) || { ts };
    return [dashFmtTs(ts), ...collected.map((_, i) => {
      const v = row["s" + i];
      return v == null || !Number.isFinite(+v) ? "" : String(v);
    })];
  });
  return { title, columns, rows };
}

async function buildDashDataModel() {
  const d = CUR_DASH;
  const panels = (d.panels || []).slice().sort((a, b) => (a.grid.y - b.grid.y) || (a.grid.x - b.grid.x)).slice(0, 60);
  const sheets = await dashMapLimit(panels, 4, p => panelDataSheet(p).catch(() => ({
    title: (p.title || "面板").slice(0, 28), columns: ["错误"], rows: [["读取失败"]]
  })));
  const sections = sheets.filter(Boolean);
  // 目录页
  sections.unshift({
    title: "面板目录",
    columns: ["组件", "类型", "数据源", "单位", "查询"],
    rows: panels.map(p => [
      p.title || "（未命名）", p.type || "", dsLabel(resolveDS(p)), p.unit || "",
      (p.targets || []).map(t => t.expr).join(" | ") || ""
    ])
  });
  return {
    title: "看板数据 · " + d.name,
    subtitle: "面板原始数据导出 · " + new Date().toLocaleString(),
    summaryTitle: "导出信息",
    meta: dashExportMeta(),
    sections,
    orientation: "landscape",
    footer: "本文件为查询原始数据；看板视觉布局请使用 PNG / PDF / Word 导出。"
  };
}

function buildDashTemplatePayload(d) {
  if (!d) return null;
  const panels = (d.panels || []).map(p => ({
    id: p.id, title: p.title, type: p.type, unit: p.unit,
    datasource: p.datasource || "", text: p.text || "",
    min: p.min, max: p.max, decimals: p.decimals,
    grid: p.grid || {}, targets: (p.targets || []).map(t => ({ expr: t.expr, legend: t.legend || "", ref_id: t.ref_id || "" })),
    raw_type: p.raw_type || ""
  }));
  const a = dashAppearance(d);
  // 模板只导出配色与透明度；图片 URL 跨环境无效，导入端也会清空。
  const appearance = {};
  if (a.background_color) appearance.background_color = a.background_color;
  if (a.background_fit && a.background_fit !== "cover") appearance.background_fit = a.background_fit;
  if (a.panel_opacity) appearance.panel_opacity = a.panel_opacity;
  return {
    format: "aiops",
    version: 1,
    exported_at: new Date().toISOString(),
    dashboard: {
      name: d.name,
      description: d.description || "",
      tags: d.tags || [],
      datasource: d.datasource || "",
      appearance,
      vars: (d.vars || []).map(v => ({
        name: v.name, label: v.label || "", type: v.type || "query",
        query: v.query || "", options: v.options || [], multi: !!v.multi, include_all: !!v.include_all
      })),
      panels
    }
  };
}
function downloadDashTemplate(d) {
  const payload = buildDashTemplatePayload(d);
  if (!payload) { toast("无可导出的看板", "err"); return; }
  const name = (typeof expSafeName === "function" ? expSafeName(d.name) : String(d.name || "dashboard").replace(/\s+/g, "_")) + "_template";
  const blob = new Blob([JSON.stringify(payload, null, 2)], { type: "application/json;charset=utf-8" });
  if (typeof expDownload === "function") expDownload(blob, name + ".json");
  else {
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a"); a.href = url; a.download = name + ".json";
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  toast("模板已导出", "ok");
}
async function exportDashTemplateById(id) {
  try {
    const d = await fetch(`${API}/dashboards/${encodeURIComponent(id)}`).then(r => r.json());
    if (!d || !d.id) { toast("读取看板失败", "err"); return; }
    downloadDashTemplate(d);
  } catch (e) { toast("导出模板失败：" + e, "err"); }
}

function syncDashExportUI() {
  const fmt = ($("dashExportFormat") && $("dashExportFormat").value) || "png";
  const liveWrap = $("dashExportLiveWrap");
  const hint = $("dashExportHint");
  const run = $("dashExportRun");
  const isReport = fmt === "markdown" || fmt === "pdf-report";
  if (liveWrap) liveWrap.hidden = !isReport;
  if (run) {
    run.textContent = fmt === "excel" ? "导出数据"
      : (fmt === "template" ? "导出模板"
        : (isReport ? "生成报告" : "导出视觉"));
  }
  if (!hint) return;
  const tips = {
    png: "导出整板 PNG：按当前布局拼接各面板截图（图表为最终渲染效果，非数据表）。",
    pdf: "导出视觉 PDF：打开打印预览，页面为看板布局与图表截图；请选择「另存为 PDF」。",
    word: "导出 Word：每页/每段嵌入面板截图，保留看板视觉成品。",
    excel: "导出 Excel：每个面板一个工作表，写入时序点或即时值等原始数据（非截图）。",
    template: "导出 AIOps JSON 模板：可跨环境通过「导入模板」完整回灌（含变量、布局、查询）。",
    markdown: "导出 Markdown 诊断报告：配置说明 + 可选实时诊断摘要（文本，非视觉）。",
    "pdf-report": "导出诊断报告 PDF：封面/配置/快照表格（非看板截图）。需要视觉成品请选「PDF · 看板视觉」。"
  };
  hint.textContent = tips[fmt] || tips.png;
}
safeAddEventListener("dashExportFormat", "change", syncDashExportUI);

safeAddEventListener("dashExportRun", "click", async () => {
  if (!CUR_DASH) return;
  const fmt = $("dashExportFormat").value;
  if (fmt === "template") {
    downloadDashTemplate(CUR_DASH);
    closeMask($("dashExportMask"));
    return;
  }
  const includeLive = $("dashExportLive") && $("dashExportLive").checked;
  const isVisual = fmt === "png" || fmt === "pdf" || fmt === "word";
  const isData = fmt === "excel";
  const isReport = fmt === "markdown" || fmt === "pdf-report";
  // PDF 窗口必须在用户手势内打开，避免异步截图后被拦截。
  const needsPopup = fmt === "pdf" || fmt === "pdf-report";
  const popup = needsPopup ? window.open("", "_blank") : null;
  if (needsPopup && !popup) { toast("浏览器拦截了 PDF 窗口，请允许弹窗后重试", "warn"); return; }
  await withLoading("dashExportRun", async () => {
    try {
      let model;
      let baseName = CUR_DASH.name;
      if (isVisual) {
        toast("正在截取看板视觉…", "ok");
        model = await buildDashVisualModel();
        baseName = "看板_" + CUR_DASH.name;
      } else if (isData) {
        toast("正在拉取面板数据…", "ok");
        model = await buildDashDataModel();
        baseName = "看板数据_" + CUR_DASH.name;
      } else {
        let rows = null;
        if (includeLive) {
          const panels = (CUR_DASH.panels || []).filter(p => p.type !== "text" && p.type !== "unsupported").slice(0, 40);
          const digests = await dashMapLimit(panels, 6, p => panelDigest(p).catch(() => "读取失败"));
          rows = panels.map((p, i) => ({ title: p.title || "（未命名）", digest: digests[i] || "无数据" }));
        }
        model = dashboardReportModel(rows);
        baseName = "诊断报告_" + CUR_DASH.name;
      }
      if (popup) model._printWindow = popup;
      const ok = await exportModel(model, fmt, baseName);
      if (ok === false) throw new Error("导出窗口不可用");
      closeMask($("dashExportMask"));
      toast(isData ? "数据已导出" : (isReport ? "报告已生成" : "视觉导出完成"), "ok");
    } catch (e) {
      if (popup) try { popup.close(); } catch (_) {}
      toast("导出失败：" + e, "err");
    }
  });
});
let DASH_AI_CREATE_SEQ = 0;
let DASH_AI_LAST_JOB_ID = "";
const DASH_AI_PRESETS = {
  host: "构建主机黄金信号看板：顶部展示在线数、CPU、内存、磁盘总体水位；中部展示 CPU/负载、内存/交换、磁盘 IO、网络吞吐趋势；底部展示高水位主机排行、磁盘卷明细和当前告警。支持 instance 下钻。",
  service: "构建服务 SLI/SLO 看板：覆盖流量、错误率、P50/P95/P99 延迟、可用性、饱和度、SLO 燃烧率和当前告警；顶部 KPI 概览，中部趋势，底部服务/实例排行与明细。",
  database: "构建数据库性能看板：覆盖连接、QPS/TPS、慢查询、错误、缓存命中率、锁等待、复制延迟、存储与资源水位；按实例下钻，并突出容量和故障风险。",
  network: "构建网络与流量看板：覆盖吞吐、包速率、连接数、丢包、错误、时延、Top Talkers、异常端口与设备告警；趋势、排行、表格和状态组件混合布局。",
  capacity: "构建容量与成本看板：覆盖 CPU/内存/存储利用率、增长趋势、剩余天数预测、闲置与高水位资源、Top 消耗对象、容量风险和优化机会。"
};
safeAddEventListener("dashAIBtn", "click", () => {
  // 不递增 SEQ：避免仅打开弹窗就取消后台轮询
  $("dashAIPrompt").value = ""; $("dashAIName").value = "";
  const btn = $("dashAICreate");
  const resuming = DASH_AI_LAST_JOB_ID && btn && (btn.textContent === "查询状态" || btn.textContent === "生成中…");
  if (!resuming) {
    $("dashAIProgress").style.display = "none";
    if (btn) { btn.disabled = false; btn.textContent = "生成"; }
  }
  openMask("dashAIMask");
});
safeAddEventListener("dashAIPresets", "click", e => {
  const btn = e.target.closest("[data-dash-ai-preset]");
  if (!btn) return;
  $("dashAIPrompt").value = DASH_AI_PRESETS[btn.dataset.dashAiPreset] || "";
  $("dashAIPrompt").focus();
});
function setDashAIProgress(stage, progress, hint) {
  const box = $("dashAIProgress");
  if (!box) return;
  box.style.display = "";
  $("dashAIProgressStage").textContent = stage || "处理中…";
  $("dashAIProgressPct").textContent = Math.max(0, Math.min(100, progress || 0)) + "%";
  $("dashAIProgressBar").style.width = Math.max(0, Math.min(100, progress || 0)) + "%";
  if (hint) $("dashAIProgressHint").textContent = hint;
}
async function pollDashboardAIJob(jobID, seq) {
  const deadline = Date.now() + 4 * 60 * 1000;
  while (seq === DASH_AI_CREATE_SEQ && Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 1200));
    let job;
    try {
      const r = await fetch(`${API}/dashboards/ai/jobs/${encodeURIComponent(jobID)}`);
      job = await r.json().catch(() => ({}));
      if (r.status === 404 || r.status === 403) {
        $("dashAICreate").disabled = false;
        $("dashAICreate").textContent = "重试";
        DASH_AI_LAST_JOB_ID = "";
        setDashAIProgress(r.status === 403 ? "无权查看任务" : "任务已过期", 100, job.error || "请重新生成。");
        toast((job.error || (r.status === 403 ? "无权查看该任务" : "AI 看板任务不存在或已过期")), "err");
        return;
      }
      if (!r.ok) throw new Error(job.error || ("HTTP " + r.status));
    } catch (e) {
      setDashAIProgress("正在等待生成服务…", 10, "网络暂时不可用，任务仍可能在后台继续。");
      continue;
    }
    setDashAIProgress(job.stage || job.status, job.progress || 0,
      job.status === "running" ? "正在使用真实指标构建组件、PromQL、变量与布局，请勿重复提交。" : "");
    if (job.status === "done") {
      $("dashAICreate").disabled = false;
      $("dashAICreate").textContent = "生成";
      DASH_AI_LAST_JOB_ID = "";
      closeMask($("dashAIMask"));
      let msg = `AI 看板「${job.name}」已生成：${job.panels} 个组件`;
      const warns = Array.isArray(job.warnings) ? job.warnings : [];
      if (warns.length) msg += "（" + warns[0] + (warns.length > 1 ? ` 等 ${warns.length} 条提示` : "") + "）";
      toast(msg, warns.length ? "warn" : "ok");
      if (job.dashboard_id) await openDashboard(job.dashboard_id);
      return;
    }
    if (job.status === "failed") {
      $("dashAICreate").disabled = false;
      $("dashAICreate").textContent = "重试";
      DASH_AI_LAST_JOB_ID = "";
      setDashAIProgress("生成失败", 100, job.error || "请调整需求或检查 AI 配置后重试。");
      toast("AI 看板生成失败：" + (job.error || ""), "err");
      return;
    }
  }
  if (seq === DASH_AI_CREATE_SEQ) {
    $("dashAICreate").disabled = false;
    $("dashAICreate").textContent = "查询状态";
    setDashAIProgress("任务仍在后台运行", 90, "生成耗时超出前端等待窗口，完成后消息中心仍会通知。可点「查询状态」继续轮询。");
  }
}
// 优化提示词：就地调用 AI，完成后直接覆盖描述框，不再弹辅助窗、无需点「应用」。
safeAddEventListener("dashAIOptimizePrompt", "click", async () => {
  const ta = $("dashAIPrompt");
  const cur = (ta && ta.value || "").trim();
  if (!cur) { toast("请先简单描述你想要的看板", "err"); return; }
  await withLoading("dashAIOptimizePrompt", async () => {
    let ctx = "";
    try {
      const metrics = (await fetchMetricNames((CUR_DASH && CUR_DASH.datasource) || "")).slice(0, 200);
      if (metrics.length) ctx = "【平台可用指标（节选，优先围绕这些真实指标组织）】\n" + metrics.join(", ");
    } catch (e) {}
    toast("正在优化提示词…", "ok");
    let answer = "", streamErr = "";
    const controller = (typeof AbortController !== "undefined") ? new AbortController() : null;
    const timeout = setTimeout(() => { try { if (controller) controller.abort(); } catch (e) {} }, 120000);
    try {
      const r = await fetch(`${API}/ai/assist`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        signal: controller ? controller.signal : undefined,
        body: JSON.stringify({ task: "dashboard_prompt_optimize", input: cur, context: ctx })
      });
      if (!r.ok) throw new Error("HTTP " + r.status);
      await readSSEStream(r,
        (_d, full) => { answer = full; },
        (err) => { streamErr = String(err || "流式失败"); },
        (full) => { answer = full || answer; }
      );
    } catch (e) {
      const msg = e && e.name === "AbortError" ? "请求超时，请检查 AI 服务后重试" : String(e);
      toast("优化失败：" + msg, "err");
      return;
    } finally {
      clearTimeout(timeout);
    }
    if (streamErr) { toast("优化失败：" + streamErr, "err"); return; }
    let text = (answer || "").trim();
    const fence = text.match(/^```(?:\w+)?\s*([\s\S]*?)```\s*$/);
    if (fence) text = fence[1].trim();
    if (!text) { toast("未生成有效内容", "err"); return; }
    ta.value = text;
    try { ta.focus(); ta.setSelectionRange(text.length, text.length); } catch (e) {}
    toast("已优化并覆盖描述，可继续微调后生成", "ok");
  });
});
safeAddEventListener("dashAICreate", "click", async () => {
  // 「查询状态」：继续轮询已有任务，不重新提交
  if ($("dashAICreate").textContent === "查询状态" && DASH_AI_LAST_JOB_ID) {
    const seq = ++DASH_AI_CREATE_SEQ;
    $("dashAICreate").disabled = true;
    $("dashAICreate").textContent = "生成中…";
    setDashAIProgress("继续查询生成状态", 50, "正在恢复轮询…");
    pollDashboardAIJob(DASH_AI_LAST_JOB_ID, seq);
    return;
  }
  const prompt = $("dashAIPrompt").value.trim();
  if (!prompt) { toast("请描述你想要的看板", "err"); return; }
  const name = $("dashAIName").value.trim();
  const seq = ++DASH_AI_CREATE_SEQ;
  $("dashAICreate").disabled = true;
  $("dashAICreate").textContent = "生成中…";
  setDashAIProgress("正在提交生成任务", 5, "AI 将基于当前数据源真实指标生成，不会臆造并直接执行运维操作。");
  try {
    const r = await fetch(`${API}/dashboards/ai-create`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ prompt, name }) });
    const j = await r.json().catch(() => ({}));
    if (r.ok && j.ok && j.queued && j.job_id) {
      DASH_AI_LAST_JOB_ID = j.job_id;
      setDashAIProgress("已进入生成队列", 8, "可继续等待实时状态；关闭弹窗后任务仍会在后台完成。");
      pollDashboardAIJob(j.job_id, seq);
    } else {
      $("dashAICreate").disabled = false;
      $("dashAICreate").textContent = "重试";
      toast("提交失败：" + (j.error || r.status), "err");
    }
  } catch (e) {
    $("dashAICreate").disabled = false;
    $("dashAICreate").textContent = "重试";
    toast("提交失败：" + e, "err");
  }
});

/* ---------- 列表事件 ---------- */
safeAddEventListener("dashCreateBtn", "click", () => openDashMeta(null));
safeAddEventListener("dashList", "click", e => {
  const btn = e.target.closest("[data-dact]");
  const card = e.target.closest("[data-dash]");
  if (btn) {
    const id = btn.dataset.id, act = btn.dataset.dact;
    if (act === "open") openDashboard(id);
    else if (act === "meta") { const d = DASH_LIST.find(x => x.id === id); openDashMeta(d); }
    else if (act === "tpl") exportDashTemplateById(id);
    else if (act === "del") delDashboard(id);
    return;
  }
  if (card) openDashboard(card.dataset.dash);
});
async function delDashboard(id) {
  const d = DASH_LIST.find(x => x.id === id);
  if (!confirm(`确认删除仪表盘「${d ? d.name : id}」？`)) return;
  try { await fetch(`${API}/dashboards/${encodeURIComponent(id)}`, { method: "DELETE" }); toast("已删除", "ok"); loadDashboards(); }
  catch (e) { toast("删除失败：" + e, "err"); }
}
