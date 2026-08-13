// hardware.js — 硬件健康面板（Redfish/BMC）
// Loaded as part of the unified app.js bundle.
//
// 交互：主从式目录树（宿主机 ▸ 硬件目标，可折叠）→ 点选任意目标，右侧内联
// 详情面板展示全量数据 + 重点突出 + 历史曲线（与虚拟机页同一套交互范式）。
// 注意：CSP 为 script-src 'self'（无 unsafe-inline），**禁止内联 onclick**——一律事件委托。
//
// 覆盖机型：Dell R730/R740/R750/R760 (iDRAC8/9)、华为 RH2288 V3、TaiShan 200 (Model 2280)(iBMC)。
// 各家 Redfish 字段完整度不一，渲染一律"有才画、没有不画空表"。

let HW_RESULTS = [];                                   // [{host, snap}]
let HW_SEL = null;                                     // 树中选中的目标 key（host.id|target_name）
let HW_HOST_FILTER = "";                               // ""=全部宿主机，否则 host.id
let HW_TREE_Q = "";                                    // 左树内搜索（宿主名）
const HW_TREE_COLLAPSED = new Set();                   // 折叠的宿主机 id
let HW_CHARTS = {};                                    // 详情面板内的图表实例
let HW_CUR = null;                                     // 当前选中详情的项
let HW_HIST_RANGE = "6h";
let HW_HIST_SUMMARY = {};                              // 各指标历史概况（min/max/avg/最新），供导出补上"历史"数据
let HW_LOCAL_EVENTS = [];                              // 平台侧记录的状态变化（异步载入，导出时一并带上）
let HW_FILTER = { q: "", status: "all", fresh: "all" }; // 搜索 / 在线状态 / 数据新鲜度

/* ---------- i18n 小工具 ---------- */

const hwT = (k, fb) => I18N.t(k, fb);

// BMC 一律返回英文枚举（OK / Enabled / NotRedundant…）。直接渲染就会出现
// "中文界面里夹着英文" —— 这里统一过一层字典，查不到就原样回显（新固件可能
// 冒出字典里没有的值，回显总比显示 key 好）。
function hwEnum(ns, v) {
  if (v === undefined || v === null || v === "") return "";
  return I18N.t("hw." + ns + "." + v, v);
}

const hwHealthText = h => hwEnum("health", h || "Unknown");

/* ---------- 数据加载 ---------- */

// 自己拉主机列表，不依赖 window._cachedHosts —— 后者由异步 refresh 填充，
// 首屏点进来时常为 undefined 且此后无人重渲染，导致永远停在"暂无主机"。
async function loadHardwarePanel() {
  const container = $("hardwarePanel");
  if (!container) return;
  container.innerHTML = `<div class="empty-line">${esc(hwT("hardware.loading", "加载中…"))}</div>`;
  let hosts = [];
  try {
    hosts = (window._cachedHosts && window._cachedHosts.length) ? window._cachedHosts
          : (await fetch(`${API}/hosts`).then(r => r.json()) || []);
  } catch (e) { hosts = []; }
  if (!hosts.length) {
    container.innerHTML = `<div class="empty-line">${esc(hwT("hardware.no_hosts", "暂无主机"))}</div>`;
    return;
  }
  // 重复主机（Agent 重装会换新 host_id，同一台机器留下多条记录）单独提示，
  // 不静默过滤：删记录是不可逆的，得由用户确认。
  loadDuplicates(() => renderHardwarePanel());

  // 不过滤离线主机：BMC 是带外通道，主机宕机时的硬件数据恰恰最有价值。
  const results = [];
  await Promise.all(hosts.map(h =>
    fetch(`${API}/hardware/health?host=${encodeURIComponent(h.id)}`)
      .then(r => r.json())
      .then(d => { (d.snapshots || []).forEach(s => results.push({ host: h, snap: s, online: !!h.online })); })
      .catch(() => {})
  ));
  HW_RESULTS = results;
  renderHardwarePanel();
}

/* ---------- 健康判定 ---------- */

function hwHealthMeta(health) {
  if (health === "OK") return { cls: "ok", icon: "✓", label: hwHealthText(health) };
  if (health === "Warning") return { cls: "warn", icon: "⚠", label: hwHealthText(health) };
  if (health === "Critical") return { cls: "crit", icon: "✕", label: hwHealthText(health) };
  return { cls: "unknown", icon: "?", label: hwHealthText(health) };
}

const hwIsBad = h => h === "Warning" || h === "Critical";
const hwBadCls = h => h === "Critical" ? "hw-crit-text" : h === "Warning" ? "hw-warn-text" : "";
const hwTempOver = t => (t.upper_critical > 0 && t.reading >= t.upper_critical) ? "Critical"
                      : (t.upper_caution > 0 && t.reading >= t.upper_caution) ? "Warning" : "";

// 收集所有异常部件，并明确"是哪个部件、什么读数、什么状态"。
// 卡片上的异常计数与详情里的"需要关注"表用的是同一份数据，两处不会再对不上。
function hwBadParts(sd) {
  const out = [];
  const push = (kind, name, reading, status) => out.push({ kind, name, reading, status });

  (sd.temps || []).forEach(t => {
    const over = hwTempOver(t), st = over || (hwIsBad(t.status) ? t.status : "");
    if (st) push(hwT("hardware.temperature", "温度传感器"), t.name, `${t.reading}°C`, st);
  });
  (sd.fans || []).forEach(f => {
    const st = hwIsBad(f.health) ? f.health : (hwIsBad(f.status) ? f.status : "");
    if (st) push(hwT("hardware.fans", "风扇"), f.name, `${f.rpm} RPM`, st);
  });
  ((sd.power || {}).psus || []).forEach(p => {
    if (hwIsBad(p.health)) push(hwT("hardware.power_supply", "电源"), p.name, `${p.input_watts}W`, p.health);
  });
  (sd.storage || []).forEach(d => {
    if (hwIsBad(d.health) || d.smart_warn) {
      const nm = d.location ? `${d.name} (${d.location})` : d.name;
      push(hwT("hardware.storage", "存储"), nm,
        d.smart_warn ? hwT("hardware.smart_fail", "⚠ 预测故障") : `${(d.capacity_gb || 0).toFixed(0)}GB`,
        d.smart_warn ? "Critical" : d.health);
    }
  });
  ((sd.memory || {}).dimms || []).forEach(d => {
    if (hwIsBad(d.health)) push(hwT("hardware.memory", "内存"), d.slot || d.name, `${(d.capacity_gb || 0).toFixed(0)}GB`, d.health);
  });
  (sd.cpus || []).forEach(c => { if (hwIsBad(c.health)) push(hwT("hardware.cpu", "CPU"), c.name, c.model || "", c.health); });
  (sd.gpus || []).forEach(g => { if (hwIsBad(g.health)) push(hwT("hardware.gpu", "GPU / 加速卡"), g.name, g.model || "", g.health); });
  (sd.raid || []).forEach(r => { if (hwIsBad(r.health)) push(hwT("hardware.raid", "RAID / 存储控制器"), r.name, r.model || "", r.health); });
  (sd.enclosures || []).forEach(e => {
    if (hwIsBad(e.health)) push(hwT("hardware.enclosure", "磁盘框"), e.location || e.name,
      e.temperature_c ? e.temperature_c.toFixed(0) + "°C" : (e.model || ""), e.health);
  });
  (sd.raid || []).forEach(r => (r.volumes || []).forEach(v => {
    if (hwIsBad(v.health)) push(hwT("hardware.volumes", "逻辑卷"), `${r.name} / ${v.name}`, v.raid_type || "", v.health);
  }));
  return out;
}

// 汇总一台设备的"重点"：最高温、功耗、异常部件
function hwSummary(sd) {
  const temps = sd.temps || [], fans = sd.fans || [], power = sd.power || {};
  const maxTemp = temps.length ? Math.max(...temps.map(t => t.reading || 0)) : 0;
  const bads = hwBadParts(sd);
  // 最高温那颗传感器是否已越限——决定卡片上温度 chip 要不要标色
  let tempLvl = "";
  temps.forEach(t => {
    if ((t.reading || 0) !== maxTemp) return;
    const o = hwTempOver(t) || (hwIsBad(t.status) ? t.status : "");
    if (o === "Critical" || (o && tempLvl !== "Critical")) tempLvl = o;
  });
  return { maxTemp, tempLvl, watts: power.total_watts || 0, bad: bads.length, bads, temps, fans, power };
}

/* ---------- 渲染 ---------- */

// 异常优先排序：Critical > Warning > OK，让最需要关注的排在最前
// （树内宿主分组与组内目标顺序都继承这个排序）。
function hwSortedItems() {
  const order = { Critical: 0, Warning: 1, OK: 2 };
  return HW_RESULTS.slice().sort((a, b) =>
    (order[a.snap.health] ?? 3) - (order[b.snap.health] ?? 3));
}

// 快照更新时间（秒）。BMC 挂了时快照会停更，这个时间比主机在线状态更能反映
// "这份硬件数据还新不新"。
function hwUpdatedAt(it) {
  const t = it.snap.updated_at ? Date.parse(it.snap.updated_at) / 1000
          : ((it.snap.snapshot || {}).timestamp || 0);
  return t || 0;
}

// 搜索匹配范围覆盖运维实际会输入的东西：主机名、BMC 名/地址、厂商型号、序列号、资产标签。
function hwMatchesQuery(it, q) {
  if (!q) return true;
  const sd = it.snap.snapshot || {}, sys = sd.system || {};
  const hay = [
    it.host.hostname, it.host.id, it.host.ip,
    it.snap.target_name, it.snap.target_url,
    sys.manufacturer, sys.model, sys.serial_number, sys.sku, sys.host_name, sys.asset_tag,
  ].filter(Boolean).join(" ");
  return matchesSearchTokens(hay, q);
}

function hwFilteredItems() {
  const now = Date.now() / 1000;
  const freshWindow = { "1h": 3600, "24h": 86400, "7d": 604800 }[HW_FILTER.fresh] || 0;
  const searchActive = !!normalizeSearchText(HW_FILTER.q);
  return hwSortedItems().filter(it => {
    if (!searchActive && HW_HOST_FILTER && it.host.id !== HW_HOST_FILTER) return false;
    if (HW_FILTER.status === "online" && !it.online) return false;
    if (HW_FILTER.status === "offline" && it.online) return false;
    if (freshWindow) {
      const t = hwUpdatedAt(it);
      if (!t || now - t > freshWindow) return false;
    }
    return hwMatchesQuery(it, HW_FILTER.q);
  });
}

function hwToolbarHTML(shown, total) {
  const opt = (v, cur, label) => `<option value="${v}" ${v === cur ? "selected" : ""}>${esc(label)}</option>`;
  let h = `<div class="rtx-toolbar">
    <input type="search" id="hwSearch" class="hw-search" value="${esc(HW_FILTER.q)}"
      placeholder="${esc(hwT("hardware.search_ph", "搜索主机名 / 型号 / 序列号 / BMC 地址"))}" autocomplete="off" spellcheck="false">
    <select id="hwStatusFilter" class="hw-sel">
      ${opt("all", HW_FILTER.status, hwT("hardware.status_all", "全部状态"))}
      ${opt("online", HW_FILTER.status, hwT("hardware.status_online", "仅在线"))}
      ${opt("offline", HW_FILTER.status, hwT("hardware.status_offline", "仅离线"))}
    </select>
    <select id="hwFreshFilter" class="hw-sel">
      ${opt("all", HW_FILTER.fresh, hwT("hardware.fresh_all", "不限更新时间"))}
      ${opt("1h", HW_FILTER.fresh, hwT("hardware.fresh_1h", "1 小时内更新"))}
      ${opt("24h", HW_FILTER.fresh, hwT("hardware.fresh_24h", "24 小时内更新"))}
      ${opt("7d", HW_FILTER.fresh, hwT("hardware.fresh_7d", "7 天内更新"))}
    </select>
    <span class="rtx-count" id="hwCountSpan">${shown}/${total}</span>
  </div>`;
  h += dupBannerHTML();
  return h;
}

function hwTreeCaret(id, hasKids, collapsed) {
  if (!hasKids) return `<span class="rtx-caret rtx-caret-gap" aria-hidden="true"></span>`;
  return `<button type="button" class="rtx-caret" data-hwhtoggle="${esc(id)}" aria-expanded="${collapsed ? "false" : "true"}">${collapsed ? "▸" : "▾"}</button>`;
}

function renderHardwarePanel() {
  const vt = $("hwViewToggle");
  if (vt) vt.style.display = "none";
  const container = $("hardwarePanel");
  if (!container) return;
  if (!HW_RESULTS.length) {
    container.innerHTML = hwToolbarHTML(0, 0) +
      `<div class="empty-line">${esc(hwT("hardware.no_data", "暂无硬件数据（需在 Agent 配置 Redfish 目标）"))}</div>`;
    return;
  }
  const items = hwFilteredItems();
  if (!hwSelItem()) {
    const first = items.find(it => hwIsBad(it.snap.health)) || items[0];
    HW_SEL = first ? hwKeyOf(first) : null;
  }
  const hwCol = window.treeCollapsed && window.treeCollapsed("aiops_hw_tree");
  const focusTree = document.activeElement && document.activeElement.id === "hwTreeSearch";
  const focusQ = focusTree ? document.activeElement.selectionStart : -1;
  container.innerHTML =
    `<div class="rtx-wrap tree-wrap${hwCol ? " tree-collapsed" : ""}">` +
      `<div class="rtx-tree tree-pane" id="hwxTree">${hwTreeHTML()}</div>` +
      `<button class="tree-toggle-btn" data-tree-toggle="aiops_hw_tree" title="收起/展开设备列表，给右侧腾空间" aria-expanded="${hwCol ? "false" : "true"}">${hwCol ? "›" : "‹"}</button>` +
      `<div class="rtx-main">` +
        hwToolbarHTML(items.length, HW_RESULTS.length) +
        `<div class="rtx-main-scroll" id="hwxDetail"></div>` +
      `</div>` +
    `</div>`;
  hwRenderDetail();
  if (focusTree) {
    const inp = $("hwTreeSearch");
    if (inp) { inp.focus(); if (focusQ >= 0) try { inp.setSelectionRange(focusQ, focusQ); } catch (e) {} }
  }
}

// 筛选变化只重建树（详情里挂着 canvas 图表，整面板重绘会把曲线清掉还丢焦点）
function hwRefreshTree() {
  const focusTree = document.activeElement && document.activeElement.id === "hwTreeSearch";
  const focusQ = focusTree ? document.activeElement.selectionStart : -1;
  const t = $("hwxTree");
  if (t) t.innerHTML = hwTreeHTML();
  const c = $("hwCountSpan");
  if (c) c.textContent = `${hwFilteredItems().length}/${HW_RESULTS.length}`;
  if (focusTree) {
    const inp = $("hwTreeSearch");
    if (inp) { inp.focus(); if (focusQ >= 0) try { inp.setSelectionRange(focusQ, focusQ); } catch (e) {} }
  }
}

// 机型一行：厂商 · 型号 · 序列号。Dell 的 Service Tag 落在 SKU，华为落在
// SerialNumber —— 采集端已归一，这里只管展示。
function hwModelLine(sysInfo) {
  const parts = [sysInfo.manufacturer, sysInfo.model].filter(Boolean);
  const sn = sysInfo.serial_number || sysInfo.sku;
  if (sn) parts.push("SN " + sn);
  return parts.join(" · ");
}

/* ---------- 左侧目录树（宿主机 ▸ 硬件目标） ---------- */

// 选中键：宿主 + 目标名。用稳定键而非列表下标——筛选会让下标漂移指错设备。
function hwKeyOf(it) { return it.host.id + "|" + (it.snap.target_name || it.snap.target_url || ""); }

function hwSelItem() {
  if (!HW_SEL) return null;
  return HW_RESULTS.find(it => hwKeyOf(it) === HW_SEL) || null;
}

function hwTreeNode(it) {
  const snap = it.snap, sd = snap.snapshot || {}, m = hwHealthMeta(snap.health), s = hwSummary(sd);
  const key = hwKeyOf(it), sel = HW_SEL === key;
  const model = (sd.system || {}).model || "";
  const dotCls = m.cls === "ok" ? "ok" : m.cls === "warn" ? "warn" : m.cls === "crit" ? "crit" : "na";
  const mini = [
    s.bad ? String(s.bad) : "",
    s.maxTemp ? Math.round(s.maxTemp) + "°C" : "",
  ].filter(Boolean).join(" · ");
  return `<div class="rtx-node is-leaf${sel ? " selected" : ""}${hwIsBad(snap.health) ? " bad" : ""}" data-hwsel="${esc(key)}" role="treeitem" aria-selected="${sel ? "true" : "false"}" tabindex="0">
    ${hwTreeCaret("", false, false)}
    <span class="rtx-dot ${dotCls}" aria-hidden="true"></span>
    <span class="rtx-name" title="${esc(snap.target_name || snap.target_url)}">${esc(snap.target_name || snap.target_url)}</span>
    ${model || mini ? `<span class="rtx-sub" title="${esc([model, mini].filter(Boolean).join(" · "))}">${esc(model || mini)}</span>` : ""}
  </div>`;
}

function hwHostMatchesTreeQ(host, q) {
  if (!q) return true;
  return matchesSearchTokens([host.hostname, host.id, host.ip].filter(Boolean).join(" "), q);
}

function hwTreeHTML() {
  const treeQ = normalizeSearchText(HW_TREE_Q);
  const searchActive = !!normalizeSearchText(HW_FILTER.q);
  // 左树始终展示全部宿主机（仅树搜过滤）；右栏/设备节点再受工具栏与 HOST_FILTER 约束
  const base = hwSortedItems().filter(it => {
    const now = Date.now() / 1000;
    const freshWindow = { "1h": 3600, "24h": 86400, "7d": 604800 }[HW_FILTER.fresh] || 0;
    if (HW_FILTER.status === "online" && !it.online) return false;
    if (HW_FILTER.status === "offline" && it.online) return false;
    if (freshWindow) {
      const t = hwUpdatedAt(it);
      if (!t || now - t > freshWindow) return false;
    }
    if (!hwMatchesQuery(it, HW_FILTER.q)) return false;
    return true;
  });
  const byHost = new Map();
  base.forEach(it => {
    const k = it.host.id;
    if (!byHost.has(k)) byHost.set(k, { host: it.host, online: it.online, items: [] });
    byHost.get(k).items.push(it);
  });
  // 无匹配设备的宿主机：树搜时仍可按主机名出现（空子树）
  if (treeQ) {
    hwSortedItems().forEach(it => {
      if (byHost.has(it.host.id)) return;
      if (hwHostMatchesTreeQ(it.host, treeQ)) {
        byHost.set(it.host.id, { host: it.host, online: it.online, items: [] });
      }
    });
  }
  const groups = [...byHost.values()].filter(g => hwHostMatchesTreeQ(g.host, treeQ));
  const allCnt = HW_RESULTS.length;
  const filtering = searchActive || HW_FILTER.status !== "all" || HW_FILTER.fresh !== "all" || !!treeQ;
  const rootId = "__all__";
  const rootCollapsed = !filtering && HW_TREE_COLLAPSED.has(rootId);
  const hasKids = groups.length > 0;
  let kids = "";
  if (!rootCollapsed && hasKids) {
    kids = `<div class="rtx-children" role="group">` + groups.map(g => {
      const bad = g.items.reduce((n, it) => n + (hwIsBad(it.snap.health) ? 1 : 0), 0);
      const collapsed = HW_TREE_COLLAPSED.has(g.host.id) && !filtering;
      const hostSel = !searchActive && HW_HOST_FILTER === g.host.id;
      // 选中某宿主机时，树内只展开其设备；未选中宿主仍展示全部匹配设备
      const showItems = searchActive || !HW_HOST_FILTER || HW_HOST_FILTER === g.host.id ? g.items : g.items;
      let body = "";
      if (!collapsed) {
        body = showItems.length
          ? `<div class="rtx-children" role="group">${showItems.map(hwTreeNode).join("")}</div>`
          : `<div class="rtx-children"><div class="rtx-empty">${esc(hwT("hardware.no_match", "没有匹配的设备，试试放宽筛选条件"))}</div></div>`;
      }
      return `<div class="rtx-folder">
        <div class="rtx-node${hostSel ? " selected" : ""}${g.items.length || hostSel ? " has-kids" : " is-leaf"}" data-hwhost="${esc(g.host.id)}" role="treeitem" aria-selected="${hostSel ? "true" : "false"}" tabindex="0" title="${esc(g.online ? hwT("hardware.host_online", "主机在线") : hwT("hardware.host_offline", "主机离线"))}">
          ${hwTreeCaret(g.host.id, true, collapsed)}
          <span class="rtx-ico rtx-ico-host" aria-hidden="true"></span>
          <span class="rtx-name" title="${esc(g.host.hostname || g.host.id)}">${esc(g.host.hostname || g.host.id)}</span>
          <span class="rtx-count${bad ? " bad" : ""}">${g.items.length}${bad ? `<span class="hc-bad">${bad}</span>` : ""}</span>
        </div>
        ${body}
      </div>`;
    }).join("") + `</div>`;
  } else if (treeQ || searchActive) {
    kids = `<div class="rtx-empty">${esc(hwT("hardware.no_match", "没有匹配的设备，试试放宽筛选条件"))}</div>`;
  }
  return `<div class="rtx-tree-search">
      <input type="search" id="hwTreeSearch" class="rtx-tree-q" value="${esc(HW_TREE_Q || "")}"
        placeholder="${esc(hwT("hardware.tree_search_ph", "搜索宿主机…"))}" autocomplete="off">
    </div>
    <div class="rtx-scroll">
      <div class="rtx-folder" role="tree">
        <div class="rtx-node rtx-root-node${!HW_HOST_FILTER || searchActive ? " selected" : ""}${hasKids ? " has-kids" : " is-leaf"}" data-hwhost="" role="treeitem" aria-selected="${!HW_HOST_FILTER || searchActive ? "true" : "false"}" tabindex="0">
          ${hwTreeCaret(rootId, hasKids, rootCollapsed)}
          <span class="rtx-ico rtx-ico-all" aria-hidden="true"></span>
          <span class="rtx-name">${esc(hwT("hardware.all_devices", "全部设备"))}</span>
          <span class="rtx-count">${allCnt}</span>
        </div>
        ${kids}
      </div>
    </div>`;
}

/* ---------- 右侧内联详情（复用原弹窗的 hwDetailHTML 全量渲染） ---------- */

function hwSelect(key) {
  HW_SEL = key;
  const tree = $("hwxTree");
  if (tree) {
    tree.querySelectorAll("[data-hwsel]").forEach(n =>
      n.classList.toggle("selected", n.dataset.hwsel === key));
  }
  hwRenderDetail();
}

function hwSelectHost(hostId) {
  HW_HOST_FILTER = hostId || "";
  const items = hwFilteredItems();
  if (items.length) {
    const first = items.find(it => hwIsBad(it.snap.health)) || items[0];
    HW_SEL = first ? hwKeyOf(first) : null;
  }
  hwRefreshTree();
  hwRenderDetail();
}

// 详情头：标题 + 健康徽章 + 动作（AI 诊断 / 导出 / 删除）。
// 动作用 data 属性走 hardwarePanel 的事件委托，逻辑复用原弹窗那套函数。
function hwDetailHeadHTML(it, m) {
  const snap = it.snap, sys = (snap.snapshot || {}).system || {};
  const badgeCls = m.cls === "ok" ? "ok" : m.cls === "warn" ? "warn" : m.cls === "crit" ? "crit" : "";
  const sub = [it.host.hostname || it.host.id, hwModelLine(sys)].filter(Boolean).join(" · ");
  return `<div class="hwx-dhead">
    <span class="hw-health-dot hw-${m.cls}" aria-hidden="true">${m.icon}</span>
    <div class="hwx-dtitle">
      <div class="t" title="${esc(snap.target_name || snap.target_url || "")}">${esc(snap.target_name || snap.target_url)}</div>
      <div class="s" title="${esc(sub)}">${esc(sub)}</div>
    </div>
    <span class="badge ${badgeCls}">${esc(m.label)}</span>
    <span style="flex:1"></span>
    <button class="btn sm ai-assist-btn" data-hwai="1" title="${esc(hwT("hardware.ai_diag_title", "AI 分析设备运行状态；人工反馈或采纳后再学习"))}"><span class="ai-assist-btn-ic">🤖</span>${esc(hwT("hardware.ai_diag", "AI 诊断"))}</button>
    <div class="exp-dd hwx-dd">
      <button class="btn sm" data-hwexpbtn="1" aria-haspopup="true">${esc(hwT("hardware.export", "导出"))}</button>
      <div class="exp-dd-menu" data-hwexpmenu="1" role="menu">
        <button class="exp-dd-opt" role="menuitem" data-hwexport="excel"><span>${esc(hwT("hardware.export_excel", "Excel 表格"))}</span><span class="exp-dd-ext">.xlsx</span></button>
        <button class="exp-dd-opt" role="menuitem" data-hwexport="word"><span>${esc(hwT("hardware.export_word", "Word 文档"))}</span><span class="exp-dd-ext">.docx</span></button>
        <button class="exp-dd-opt" role="menuitem" data-hwexport="markdown"><span>${esc(hwT("hardware.export_md", "Markdown"))}</span><span class="exp-dd-ext">.md</span></button>
        <button class="exp-dd-opt" role="menuitem" data-hwexport="pdf"><span>${esc(hwT("hardware.export_pdf", "PDF"))}</span><span class="exp-dd-ext">${esc(hwT("hardware.export_pdf_ext", "打印"))}</span></button>
      </div>
    </div>
    <button class="icon-btn danger" data-hwdel="${esc(snap.target_name || "")}" data-hwhost="${esc(it.host.id)}" title="${esc(hwT("hardware.delete", "删除"))}">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M3 6h18M8 6V4a2 2 0 012-2h4a2 2 0 012 2v2m3 0v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6h14z"/></svg>
    </button>
  </div>`;
}

function hwRenderDetail() {
  hwInjectTreeStyles();
  const box = $("hwxDetail");
  if (!box) return;
  const it = hwSelItem();
  if (!it) {
    HW_CUR = null;
    box.innerHTML = `<div class="hwx-detailbox"><div class="hwx-empty">${esc(hwT("hardware.pick", "从左侧选择一台设备，查看部件详情与历史曲线"))}</div></div>`;
    return;
  }
  HW_CUR = it;
  const sd = it.snap.snapshot || {}, m = hwHealthMeta(it.snap.health);
  box.innerHTML = `<div class="hwx-detailbox">${hwDetailHeadHTML(it, m)}${hwDetailHTML(it, sd, m)}</div>`;
  loadHwHistory();     // 异步填充历史曲线
  loadHwLocalEvents(); // 异步填充平台侧记录的状态变化
}

// 区块骨架：没有数据就明说"该机型未上报"，而不是渲染一张空表头。
// 各家 BMC 暴露的部件差异很大（TaiShan 200 无 RAID 卡时就是没有），
// 空表头会让人误以为是采集坏了。
function hwSection(title, count, bodyHTML) {
  return `<div class="hw-sec">
    <div class="hw-sec-head"><h4>${esc(title)}</h4>${count !== null ? `<span class="hw-sec-count">${count}</span>` : ""}</div>
    ${bodyHTML || `<div class="hw-sec-empty">${esc(hwT("hardware.no_parts", "该机型未上报此类部件"))}</div>`}
  </div>`;
}

function hwTable(head, rows) {
  if (!rows.length) return "";
  return `<div class="hw-table-wrap"><table class="hw-table">
    <thead><tr>${head.map(h => `<th>${esc(h)}</th>`).join("")}</tr></thead>
    <tbody>${rows.join("")}</tbody></table></div>`;
}

const hwSevCls = s => s === "Critical" ? "crit" : s === "Warning" ? "warn" : s === "OK" ? "ok" : "unknown";
const hwSevChip = s => `<span class="hw-sev hw-${hwSevCls(s)}">${esc(hwHealthText(s))}</span>`;
const hwDash = v => (v === undefined || v === null || v === "") ? "-" : String(v);

// 容量格式化：≥1024GB 显示 TB（保留 3 位，如 40.035 TB），否则 GB；0/空返回空串（不占格）。
function hwFmtCap(gb) {
  if (!gb || gb <= 0) return "";
  return gb >= 1024 ? (gb / 1024).toFixed(3) + " TB" : gb.toFixed(0) + " GB";
}

function hwDetailHTML(it, sd, m) {
  const s = hwSummary(sd);
  const sys = sd.system || {};
  let html = "";

  // ── 采集错误置顶：BMC 都连不上时，下面所有"正常"都是上一份缓存，必须先说清楚 ──
  if (sd.error) {
    html += `<div class="hw-error-row">${esc(hwT("hardware.collect_error", "采集错误"))}：${esc(sd.error)}</div>`;
  }

  // ── 整机身份 ──
  const ident = [
    [hwT("hardware.vendor", "厂商"), sys.manufacturer],
    [hwT("hardware.model", "型号"), sys.model],
    [hwT("hardware.serial", "序列号"), sys.serial_number],
    // Dell 的 Service Tag 与序列号常常同值，同值就不重复占一格
    [hwT("hardware.service_tag", "服务标签"), (sys.sku && sys.sku !== sys.serial_number) ? sys.sku : ""],
    [hwT("hardware.asset_tag", "资产编号"), sys.asset_tag],
    // 存储阵列（OceanStor 等）专有字段：软件版本/补丁/总容量/已用/位置（服务器为空不显示）
    [hwT("hardware.sw_version", "软件版本"), sys.software_version],
    [hwT("hardware.patch_version", "补丁版本"), sys.patch_version],
    [hwT("hardware.total_capacity", "总容量"), hwFmtCap(sys.total_capacity_gb)],
    [hwT("hardware.used_capacity", "已用容量"), hwFmtCap(sys.used_capacity_gb)],
    [hwT("hardware.device_location", "设备位置"), sys.location],
    [hwT("hardware.os_hostname", "OS 主机名"), sys.host_name],
    [hwT("hardware.bios", "BIOS 版本"), sys.bios_version],
    [hwT("hardware.bmc", "BMC"), [sys.bmc_model, sys.bmc_firmware].filter(Boolean).join(" ")],
    [hwT("hardware.power_state", "电源状态"), hwEnum("power", sys.power_state)],
    [hwT("hardware.run_state", "运行状态"), hwEnum("state", sd.state)],
    [hwT("hardware.bmc_addr", "BMC 地址"), it.snap.target_url],
  ].filter(([, v]) => v);
  if (ident.length) {
    html += `<div class="hw-ident">` + ident.map(([k, v]) =>
      `<div class="hw-ident-item"><div class="hw-ident-k">${esc(k)}</div><div class="hw-ident-v">${esc(v)}</div></div>`
    ).join("") + `</div>`;
  }

  // ── 重点摘要条 ──
  html += `<div class="hw-kpis">
    <div class="hw-kpi hw-${m.cls}"><div class="hw-kpi-v">${m.icon} ${esc(m.label)}</div><div class="hw-kpi-k">${esc(hwT("hardware.overall_health", "整机健康"))}</div></div>
    <div class="hw-kpi ${s.bad ? "hw-crit" : "hw-ok"}"><div class="hw-kpi-v">${s.bad}</div><div class="hw-kpi-k">${esc(hwT("hardware.bad_parts", "异常部件"))}</div></div>
    <div class="hw-kpi ${s.tempLvl === "Critical" ? "hw-crit" : s.tempLvl === "Warning" ? "hw-warn" : ""}"><div class="hw-kpi-v">${s.maxTemp ? s.maxTemp.toFixed(0) + "°C" : "-"}</div><div class="hw-kpi-k">${esc(hwT("hardware.max_temp", "最高温度"))}</div></div>
    <div class="hw-kpi"><div class="hw-kpi-v">${s.watts ? s.watts.toFixed(0) + "W" : "-"}</div><div class="hw-kpi-k">${esc(hwT("hardware.total_power", "总功耗"))}</div></div>
    <div class="hw-kpi"><div class="hw-kpi-v">${esc(hwEnum("redundancy", (sd.power || {}).redundancy) || "-")}</div><div class="hw-kpi-k">${esc(hwT("hardware.power_redundancy", "电源冗余"))}</div></div>
  </div>`;

  // ── 异常项置顶（重点突出，明确到具体部件）──
  if (s.bads.length) {
    // 计数用半角括号：全角括号在英文界面里是突兀的中文标点
    html += `<div class="hw-bad-box"><h4>⚠ ${esc(hwT("hardware.needs_attention", "需要关注"))} (${s.bads.length})</h4>` +
      hwTable([hwT("hardware.part", "部件"), hwT("hardware.name", "名称"), hwT("hardware.reading", "读数"), hwT("hardware.status", "状态")],
        s.bads.map(b => `<tr class="${hwBadCls(b.status)}"><td>${esc(b.kind)}</td><td>${esc(b.name)}</td><td>${esc(b.reading)}</td><td>${hwSevChip(b.status)}</td></tr>`)) +
      `</div>`;
  } else if (!sd.error) {
    html += `<div class="hw-ok-box">✓ ${esc(hwT("hardware.all_normal", "全部部件正常"))}</div>`;
  }

  // ── BMC 事件日志：唯一能回答"是哪个部件触发的"的数据 ──
  const evs = sd.events || [];
  html += hwSection(hwT("hardware.events_bmc", "BMC 事件日志（SEL）"), evs.length ? evs.length : null,
    evs.length ? `<div class="hw-events-wrap">` + hwTable(
      [hwT("hardware.event_time", "时间"), hwT("hardware.event_severity", "级别"),
       hwT("hardware.event_component", "触发部件"), hwT("hardware.event_message", "事件内容")],
      evs.map(e => `<tr class="${hwBadCls(e.severity)}">
        <td class="mono">${esc(hwFmtTime(e.created))}</td>
        <td>${hwSevChip(e.severity)}</td>
        <td>${e.component ? `<span class="hw-comp">${esc(e.component)}</span>` : "-"}</td>
        <td>${esc(e.message)}${e.resolved ? ` <span class="hw-sev hw-ok">${esc(hwT("hardware.event_resolved", "已处理"))}</span>` : ""}</td>
      </tr>`)) + `</div><div class="hint">${esc(hwT("hardware.events_hint", "来自 BMC 自身的硬件事件记录，可定位到具体触发部件"))}</div>`
    : "");

  // ── 历史曲线 ──
  const wrap = id => `<div class="chart-wrap"><canvas id="${id}" width="1000" height="200"></canvas>
    <button class="chart-enlarge" data-hwchart="${id}" title="${esc(hwT("ui.zoom_preview", "放大预览"))}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg></button></div>`;
  html += `<div class="hw-sec"><div class="hw-sec-head"><h4>${esc(hwT("hardware.history", "历史曲线"))}</h4></div>
    <div class="chart-controls">${["1h", "6h", "24h", "7d"].map(r =>
      `<button class="chip-btn ${HW_HIST_RANGE === r ? "active" : ""}" data-hwrange="${r}">${r}</button>`).join("")}
      ${typeof forecastChipHTML === "function" ? forecastChipHTML("hardware") : ""}</div>
    <div class="chart-container">${wrap("hwChartTemp")}${wrap("hwChartFan")}${wrap("hwChartPower")}${wrap("hwChartHealth")}</div></div>`;

  html += hwDetailPartsHTML(sd);

  // ── 平台侧记录的状态变化（异步填充）──
  html += `<div class="hw-sec"><div class="hw-sec-head"><h4>${esc(hwT("hardware.events_local", "监控记录的状态变化"))}</h4></div>
    <div id="hwLocalEvents"><div class="hw-sec-empty">${esc(hwT("hardware.loading", "加载中…"))}</div></div></div>`;

  // ── 元信息 ──
  const upd = it.snap.updated_at ? new Date(it.snap.updated_at).toLocaleString()
            : (sd.timestamp ? new Date(sd.timestamp * 1000).toLocaleString() : "-");
  html += `<div class="hint" style="margin-top:10px">${esc(hwT("hardware.updated", "更新时间"))} ${esc(upd)}</div>`;
  return html;
}

// 全量部件明细。每个区块独立成段，缺数据就说明原因，绝不画空表头。
function hwDetailPartsHTML(sd) {
  let html = "";
  const mem = sd.memory || {};
  const psus = (sd.power || {}).psus || [];

  // CPU
  html += hwSection(hwT("hardware.cpu", "CPU"), (sd.cpus || []).length,
    hwTable([hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.cores_threads", "核心/线程"),
             hwT("hardware.max_freq", "最大频率"), hwT("hardware.health", "健康")],
      (sd.cpus || []).map(c => `<tr class="${hwBadCls(c.health)}"><td>${esc(c.name)}</td><td>${esc(hwDash(c.model))}</td>
        <td>${c.cores || "?"}C / ${c.threads || "?"}T</td><td>${c.max_freq_mhz ? c.max_freq_mhz + "MHz" : "-"}</td>
        <td>${hwSevChip(c.health)}</td></tr>`)));

  // GPU / 加速卡（此前采集了但从未渲染）
  html += hwSection(hwT("hardware.gpu", "GPU / 加速卡"), (sd.gpus || []).length || null,
    hwTable([hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.manufacturer", "制造商"),
             hwT("hardware.max_freq", "最大频率"), hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
      (sd.gpus || []).map(g => `<tr class="${hwBadCls(g.health)}"><td>${esc(g.name)}</td><td>${esc(hwDash(g.model))}</td>
        <td>${esc(hwDash(g.manufacturer))}</td><td>${g.max_freq_mhz ? g.max_freq_mhz + "MHz" : "-"}</td>
        <td>${esc(hwEnum("state", g.state) || "-")}</td><td>${hwSevChip(g.health)}</td></tr>`)));

  // 内存
  const memTitle = hwT("hardware.memory", "内存") +
    (mem.total_gb ? ` · ${mem.total_gb.toFixed(0)}GB` : "") +
    (mem.used_gb > 0 ? ` / ${hwT("hardware.used", "已用")} ${mem.used_gb.toFixed(0)}GB` : "");
  html += hwSection(memTitle, (mem.dimms || []).length,
    hwTable([hwT("hardware.slot", "插槽"), hwT("hardware.capacity", "容量"), hwT("hardware.type", "类型"),
             hwT("hardware.speed", "速率"), hwT("hardware.manufacturer", "制造商"), hwT("hardware.part_number", "部件号"),
             hwT("hardware.serial", "序列号"), hwT("hardware.health", "健康")],
      (mem.dimms || []).map(d => `<tr class="${hwBadCls(d.health)}"><td class="mono">${esc(hwDash(d.slot || d.name))}</td>
        <td>${(d.capacity_gb || 0).toFixed(0)}GB</td><td>${esc(hwDash(d.type))}</td>
        <td>${d.speed_mhz ? d.speed_mhz + "MHz" : "-"}</td><td>${esc(hwDash(d.manufacturer))}</td>
        <td class="mono">${esc(hwDash(d.part_number))}</td><td class="mono">${esc(hwDash(d.serial_number))}</td>
        <td>${hwSevChip(d.health)}</td></tr>`)));

  // 存储 / 硬盘
  html += hwSection(hwT("hardware.storage", "存储"), (sd.storage || []).length,
    hwTable([hwT("hardware.location", "槽位"), hwT("hardware.name", "名称"), hwT("hardware.model", "型号"),
             hwT("hardware.type", "类型"), hwT("hardware.capacity", "容量"), hwT("hardware.serial", "序列号"),
             hwT("hardware.disk_fw", "盘固件"), hwT("hardware.life_left", "剩余寿命"),
             hwT("hardware.smart", "SMART"), hwT("hardware.health", "健康")],
      (sd.storage || []).map(d => {
        const media = [d.media_type, d.protocol].filter(Boolean).join(" / ") ||
                      (d.rotation_rpm ? d.rotation_rpm + " RPM" : "");
        // life_left_pct: -1 = BMC 未提供该字段（多数 HDD 与老 iDRAC），不能显示成 0%
        const life = (d.life_left_pct >= 0) ? d.life_left_pct.toFixed(0) + "%" : "-";
        const lifeCls = (d.life_left_pct >= 0 && d.life_left_pct <= 10) ? "hw-crit-text"
                      : (d.life_left_pct >= 0 && d.life_left_pct <= 20) ? "hw-warn-text" : "";
        return `<tr class="${d.smart_warn ? "hw-crit-text" : hwBadCls(d.health)}">
          <td class="mono">${esc(hwDash(d.location))}</td><td>${esc(d.name)}</td><td>${esc(hwDash(d.model))}</td>
          <td>${esc(hwDash(media))}</td><td>${(d.capacity_gb || 0).toFixed(0)}GB</td>
          <td class="mono">${esc(hwDash(d.serial_number))}</td><td class="mono">${esc(hwDash(d.revision))}</td>
          <td class="${lifeCls}">${life}</td>
          <td>${d.smart_warn ? `<span class="hw-sev hw-crit">${esc(hwT("hardware.smart_fail", "⚠ 预测故障"))}</span>`
                             : `<span class="hw-sev hw-ok">${esc(hwT("hardware.smart_ok", "正常"))}</span>`}</td>
          <td>${hwSevChip(d.health)}</td></tr>`;
      })));

  // 磁盘框（OceanStor 等外置存储；服务器 BMC 不上报此项，故无数据时整段不渲染）
  html += hwSection(hwT("hardware.enclosure", "磁盘框"), (sd.enclosures || []).length || null,
    hwTable([hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.location", "槽位"),
             hwT("hardware.serial", "序列号"), hwT("hardware.temperature", "温度传感器"),
             hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
      (sd.enclosures || []).map(e => `<tr class="${hwBadCls(e.health)}"><td>${esc(e.name)}</td><td>${esc(hwDash(e.model))}</td>
        <td class="mono">${esc(hwDash(e.location))}</td>
        <td class="mono">${esc(hwDash(e.serial_number))}</td>
        <td class="mono">${e.temperature_c ? e.temperature_c.toFixed(0) + "°C" : "-"}</td>
        <td>${esc(hwEnum("state", e.state) || "-")}</td><td>${hwSevChip(e.health)}</td></tr>`)));

  // RAID / 存储控制器（此前采集了但从未渲染）
  html += hwSection(hwT("hardware.raid", "RAID / 存储控制器"), (sd.raid || []).length || null,
    hwTable([hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.firmware", "固件版本"),
             hwT("hardware.cache", "缓存"), hwT("hardware.drive_count", "挂载盘数"), hwT("hardware.speed", "速率"),
             hwT("hardware.health", "健康")],
      (sd.raid || []).map(r => `<tr class="${hwBadCls(r.health)}"><td>${esc(r.name)}</td><td>${esc(hwDash(r.model))}</td>
        <td class="mono">${esc(hwDash(r.firmware_version))}</td>
        <td>${r.cache_mb ? r.cache_mb.toFixed(0) + "MB" : "-"}</td><td>${hwDash(r.drive_count)}</td>
        <td>${r.speed_gbps ? r.speed_gbps + "Gbps" : "-"}</td><td>${hwSevChip(r.health)}</td></tr>`)));

  // 逻辑卷（RAID 组）：盘好不代表卷好——降级的 RAID5 里每块盘都可能是 OK
  const vols = (sd.raid || []).flatMap(r => (r.volumes || []).map(v => ({ ctl: r.name, v })));
  if (vols.length) {
    html += hwSection(hwT("hardware.volumes", "逻辑卷"), vols.length,
      hwTable([hwT("hardware.raid", "RAID / 存储控制器"), hwT("hardware.name", "名称"), hwT("hardware.raid_level", "RAID 级别"),
               hwT("hardware.capacity", "容量"), hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
        vols.map(({ ctl, v }) => `<tr class="${hwBadCls(v.health)}"><td>${esc(ctl)}</td><td>${esc(v.name)}</td>
          <td>${esc(hwDash(v.raid_type))}</td><td>${v.capacity_gb ? v.capacity_gb.toFixed(0) + "GB" : "-"}</td>
          <td>${esc(hwEnum("state", v.state) || "-")}</td><td>${hwSevChip(v.health)}</td></tr>`)));
  }

  // 电源
  html += hwSection(hwT("hardware.power_supply", "电源"), psus.length,
    hwTable([hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.input_watts", "输入(W)"),
             hwT("hardware.output_watts", "输出(W)"), hwT("hardware.rated_watts", "额定功率"),
             hwT("hardware.input_voltage", "输入电压"), hwT("hardware.serial", "序列号"),
             hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
      psus.map(p => `<tr class="${hwBadCls(p.health)}"><td>${esc(p.name)}</td><td>${esc(hwDash(p.model))}</td>
        <td>${p.input_watts ? p.input_watts.toFixed(0) + "W" : "-"}</td>
        <td>${p.output_watts ? p.output_watts.toFixed(0) + "W" : "-"}</td>
        <td>${p.capacity_watts ? p.capacity_watts.toFixed(0) + "W" : "-"}</td>
        <td>${p.line_input_voltage ? p.line_input_voltage.toFixed(0) + "V" : "-"}</td>
        <td class="mono">${esc(hwDash(p.serial_number))}</td>
        <td>${esc(hwEnum("state", p.state) || "-")}</td><td>${hwSevChip(p.health)}</td></tr>`)));

  // 风扇
  html += hwSection(hwT("hardware.fans", "风扇"), (sd.fans || []).length,
    hwTable([hwT("hardware.name", "名称"), hwT("hardware.rpm", "转速"), hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
      (sd.fans || []).map(f => `<tr class="${hwBadCls(f.health) || hwBadCls(f.status)}"><td>${esc(f.name)}</td>
        <td class="mono">${f.rpm} RPM</td><td>${esc(hwEnum("state", f.status) || "-")}</td><td>${hwSevChip(f.health)}</td></tr>`)));

  // 温度传感器
  html += hwSection(hwT("hardware.temperature", "温度传感器"), (sd.temps || []).length,
    hwTable([hwT("hardware.sensor", "传感器"), hwT("hardware.reading", "读数"), hwT("hardware.caution_threshold", "告警阈值"),
             hwT("hardware.crit_threshold", "严重阈值"), hwT("hardware.status", "状态")],
      (sd.temps || []).map(t => {
        const over = hwTempOver(t);
        return `<tr class="${hwBadCls(over || t.status)}"><td>${esc(t.name)}</td><td class="mono">${t.reading}°C</td>
          <td class="mono">${t.upper_caution > 0 ? t.upper_caution + "°C" : "-"}</td>
          <td class="mono">${t.upper_critical > 0 ? t.upper_critical + "°C" : "-"}</td>
          <td>${hwSevChip(over || t.status)}</td></tr>`;
      })));

  // 固件
  html += hwSection(hwT("hardware.firmware", "固件版本"), (sd.firmware || []).length || null,
    hwTable([hwT("hardware.name", "名称"), hwT("hardware.version", "版本")],
      (sd.firmware || []).map(f => `<tr><td>${esc(f.name)}</td><td class="mono">${esc(f.version)}</td></tr>`)));

  return html;
}

// BMC 的时间戳格式各家不一（带/不带时区、偶尔是 "-- ::" 占位）。
// 解析不了就原样显示，总好过显示 "Invalid Date"。
function hwFmtTime(v) {
  if (!v) return "-";
  const d = new Date(v);
  return isNaN(d.getTime()) ? String(v) : d.toLocaleString();
}

/* ---------- 平台侧事件 ---------- */

async function loadHwLocalEvents() {
  const box = $("hwLocalEvents");
  HW_LOCAL_EVENTS = [];
  if (!box || !HW_CUR) return;
  try {
    const qs = new URLSearchParams({ host: HW_CUR.host.id, limit: "50" });
    const target = HW_CUR.snap.target_name || "";
    if (target) qs.set("target", target);
    const d = await fetch(`${API}/hardware/events?${qs}`).then(r => r.json());
    const evs = d.events || [];
    HW_LOCAL_EVENTS = evs;
    if (!evs.length) {
      box.innerHTML = `<div class="hw-sec-empty">${esc(hwT("hardware.no_events", "暂无事件记录"))}</div>`;
      return;
    }
    box.innerHTML = `<div class="hw-events-wrap">` + hwTable(
      [hwT("hardware.event_time", "时间"), hwT("hardware.event_severity", "级别"), hwT("hardware.event_message", "事件内容")],
      evs.map(e => {
        // 平台侧 severity 落库时是小写（critical/warning），与 Redfish 的
        // 首字母大写枚举不同，转一下才能命中同一套色板与字典。
        const sev = e.severity ? e.severity.charAt(0).toUpperCase() + e.severity.slice(1) : "";
        return `<tr class="${hwBadCls(sev)}"><td class="mono">${esc(hwFmtTime(e.created_at))}</td>
          <td>${sev ? hwSevChip(sev) : "-"}</td><td>${esc(e.message || e.event_type || "")}</td></tr>`;
      })) + `</div>`;
  } catch (e) {
    box.innerHTML = `<div class="hw-sec-empty">${esc(hwT("hardware.no_events", "暂无事件记录"))}</div>`;
  }
}

/* ---------- 导出（资产管理） ---------- */

// 把当前设备拍平成 export.js 的中性文档模型。刻意与弹窗里展示的内容一致——
// 用户看到什么就导出什么，不搞"界面一套、导出另一套"。
function hwExportModel(it) {
  const snap = it.snap, sd = snap.snapshot || {}, sys = sd.system || {};
  const m = hwHealthMeta(snap.health), s = hwSummary(sd);
  const mem = sd.memory || {}, psus = (sd.power || {}).psus || [];
  const D = hwDash;

  const model = {
    title: [snap.target_name || snap.target_url, it.host.hostname || it.host.id, sys.model].filter(Boolean).join(" · "),
    subtitle: `${hwT("hardware.updated", "更新时间")}: ${hwFmtTime(snap.updated_at) }`,
    meta: [],
    sections: [],
  };

  const meta = [
    [hwT("hardware.vendor", "厂商"), sys.manufacturer],
    [hwT("hardware.model", "型号"), sys.model],
    [hwT("hardware.serial", "序列号"), sys.serial_number],
    [hwT("hardware.service_tag", "服务标签"), (sys.sku && sys.sku !== sys.serial_number) ? sys.sku : ""],
    [hwT("hardware.asset_tag", "资产编号"), sys.asset_tag],
    [hwT("hardware.sw_version", "软件版本"), sys.software_version],
    [hwT("hardware.patch_version", "补丁版本"), sys.patch_version],
    [hwT("hardware.total_capacity", "总容量"), hwFmtCap(sys.total_capacity_gb)],
    [hwT("hardware.used_capacity", "已用容量"), hwFmtCap(sys.used_capacity_gb)],
    [hwT("hardware.device_location", "设备位置"), sys.location],
    [hwT("hardware.os_hostname", "OS 主机名"), sys.host_name],
    [hwT("hardware.bios", "BIOS 版本"), sys.bios_version],
    [hwT("hardware.bmc", "BMC"), [sys.bmc_model, sys.bmc_firmware].filter(Boolean).join(" ")],
    [hwT("hardware.power_state", "电源状态"), hwEnum("power", sys.power_state)],
    [hwT("hardware.run_state", "运行状态"), hwEnum("state", sd.state)],
    [hwT("hardware.bmc_addr", "BMC 地址"), snap.target_url],
    [hwT("hardware.overall_health", "整机健康"), m.label],
    [hwT("hardware.bad_parts", "异常部件"), String(s.bad)],
    [hwT("hardware.max_temp", "最高温度"), s.maxTemp ? s.maxTemp.toFixed(0) + "°C" : ""],
    [hwT("hardware.total_power", "总功耗"), s.watts ? s.watts.toFixed(0) + "W" : ""],
    [hwT("hardware.power_redundancy", "电源冗余"), hwEnum("redundancy", (sd.power || {}).redundancy)],
  ];
  if (sd.error) meta.push([hwT("hardware.collect_error", "采集错误"), sd.error]);
  model.meta = meta.filter(([, v]) => v);

  const add = (title, columns, rows) => { if (rows.length) model.sections.push({ title, columns, rows }); };

  add(hwT("hardware.needs_attention", "需要关注"),
    [hwT("hardware.part", "部件"), hwT("hardware.name", "名称"), hwT("hardware.reading", "读数"), hwT("hardware.status", "状态")],
    s.bads.map(b => [b.kind, b.name, b.reading, hwHealthText(b.status)]));

  add(hwT("hardware.events_bmc", "BMC 事件日志（SEL）"),
    [hwT("hardware.event_time", "时间"), hwT("hardware.event_severity", "级别"),
     hwT("hardware.event_component", "触发部件"), hwT("hardware.event_message", "事件内容")],
    (sd.events || []).map(e => [hwFmtTime(e.created), hwHealthText(e.severity), D(e.component), e.message || ""]));

  // 历史概况：网页有温度/风扇/功耗/健康的曲线图，导出此前完全没有——这里用 最小/最大/均值/最新
  // 概括当前所选时间范围（HW_HIST_RANGE），把"历史"补进导出。
  add(hwT("hardware.history_summary", "历史概况") + `（${HW_HIST_RANGE}）`,
    [hwT("hardware.metric", "指标"), hwT("hardware.min", "最小值"), hwT("hardware.max", "最大值"),
     hwT("hardware.avg", "均值"), hwT("hardware.latest", "最新")],
    Object.values(HW_HIST_SUMMARY).map(h => {
      const f = h.fmt || (v => v.toFixed(0));
      return [h.label, f(h.min), f(h.max), f(h.avg), h.latest != null ? f(h.latest) : "-"];
    }));

  add(hwT("hardware.cpu", "CPU"),
    [hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.cores_threads", "核心/线程"),
     hwT("hardware.max_freq", "最大频率"), hwT("hardware.health", "健康")],
    (sd.cpus || []).map(c => [c.name, D(c.model), `${c.cores || "?"}C / ${c.threads || "?"}T`,
      c.max_freq_mhz ? c.max_freq_mhz + "MHz" : "-", hwHealthText(c.health)]));

  add(hwT("hardware.gpu", "GPU / 加速卡"),
    [hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.manufacturer", "制造商"),
     hwT("hardware.max_freq", "最大频率"), hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
    (sd.gpus || []).map(g => [g.name, D(g.model), D(g.manufacturer),
      g.max_freq_mhz ? g.max_freq_mhz + "MHz" : "-", hwEnum("state", g.state) || "-", hwHealthText(g.health)]));

  add(hwT("hardware.memory", "内存"),
    [hwT("hardware.slot", "插槽"), hwT("hardware.capacity", "容量"), hwT("hardware.type", "类型"),
     hwT("hardware.speed", "速率"), hwT("hardware.manufacturer", "制造商"), hwT("hardware.part_number", "部件号"),
     hwT("hardware.serial", "序列号"), hwT("hardware.health", "健康")],
    (mem.dimms || []).map(d => [D(d.slot || d.name), (d.capacity_gb || 0).toFixed(0) + "GB", D(d.type),
      d.speed_mhz ? d.speed_mhz + "MHz" : "-", D(d.manufacturer), D(d.part_number), D(d.serial_number),
      hwHealthText(d.health)]));

  add(hwT("hardware.storage", "存储"),
    [hwT("hardware.location", "槽位"), hwT("hardware.name", "名称"), hwT("hardware.model", "型号"),
     hwT("hardware.type", "类型"), hwT("hardware.capacity", "容量"), hwT("hardware.serial", "序列号"),
     hwT("hardware.disk_fw", "盘固件"), hwT("hardware.life_left", "剩余寿命"),
     hwT("hardware.smart", "SMART"), hwT("hardware.health", "健康")],
    (sd.storage || []).map(d => [D(d.location), d.name, D(d.model),
      D([d.media_type, d.protocol].filter(Boolean).join(" / ")), (d.capacity_gb || 0).toFixed(0) + "GB",
      D(d.serial_number), D(d.revision), (d.life_left_pct >= 0) ? d.life_left_pct.toFixed(0) + "%" : "-",
      d.smart_warn ? hwT("hardware.smart_fail", "⚠ 预测故障") : hwT("hardware.smart_ok", "正常"),
      hwHealthText(d.health)]));

  add(hwT("hardware.enclosure", "磁盘框"),
    [hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.location", "槽位"),
     hwT("hardware.serial", "序列号"), hwT("hardware.temperature", "温度传感器"),
     hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
    (sd.enclosures || []).map(e => [e.name, D(e.model), D(e.location), D(e.serial_number),
      e.temperature_c ? e.temperature_c.toFixed(0) + "°C" : "-", hwEnum("state", e.state) || "-",
      hwHealthText(e.health)]));

  add(hwT("hardware.raid", "RAID / 存储控制器"),
    [hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.firmware", "固件版本"),
     hwT("hardware.cache", "缓存"), hwT("hardware.drive_count", "挂载盘数"), hwT("hardware.speed", "速率"),
     hwT("hardware.serial", "序列号"), hwT("hardware.health", "健康")],
    (sd.raid || []).map(r => [r.name, D(r.model), D(r.firmware_version),
      r.cache_mb ? r.cache_mb.toFixed(0) + "MB" : "-", D(r.drive_count),
      r.speed_gbps ? r.speed_gbps + "Gbps" : "-", D(r.serial_number), hwHealthText(r.health)]));

  add(hwT("hardware.volumes", "逻辑卷"),
    [hwT("hardware.raid", "RAID / 存储控制器"), hwT("hardware.name", "名称"), hwT("hardware.raid_level", "RAID 级别"),
     hwT("hardware.capacity", "容量"), hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
    (sd.raid || []).flatMap(r => (r.volumes || []).map(v => [r.name, v.name, D(v.raid_type),
      v.capacity_gb ? v.capacity_gb.toFixed(0) + "GB" : "-", hwEnum("state", v.state) || "-", hwHealthText(v.health)])));

  add(hwT("hardware.power_supply", "电源"),
    [hwT("hardware.name", "名称"), hwT("hardware.model", "型号"), hwT("hardware.input_watts", "输入(W)"),
     hwT("hardware.output_watts", "输出(W)"), hwT("hardware.rated_watts", "额定功率"),
     hwT("hardware.input_voltage", "输入电压"), hwT("hardware.serial", "序列号"),
     hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
    psus.map(p => [p.name, D(p.model), p.input_watts ? p.input_watts.toFixed(0) + "W" : "-",
      p.output_watts ? p.output_watts.toFixed(0) + "W" : "-", p.capacity_watts ? p.capacity_watts.toFixed(0) + "W" : "-",
      p.line_input_voltage ? p.line_input_voltage.toFixed(0) + "V" : "-", D(p.serial_number),
      hwEnum("state", p.state) || "-", hwHealthText(p.health)]));

  add(hwT("hardware.fans", "风扇"),
    [hwT("hardware.name", "名称"), hwT("hardware.rpm", "转速"), hwT("hardware.status", "状态"), hwT("hardware.health", "健康")],
    (sd.fans || []).map(f => [f.name, f.rpm + " RPM", hwEnum("state", f.status) || "-", hwHealthText(f.health)]));

  add(hwT("hardware.temperature", "温度传感器"),
    [hwT("hardware.sensor", "传感器"), hwT("hardware.reading", "读数"), hwT("hardware.caution_threshold", "告警阈值"),
     hwT("hardware.crit_threshold", "严重阈值"), hwT("hardware.status", "状态")],
    (sd.temps || []).map(t => [t.name, t.reading + "°C",
      t.upper_caution > 0 ? t.upper_caution + "°C" : "-", t.upper_critical > 0 ? t.upper_critical + "°C" : "-",
      hwHealthText(hwTempOver(t) || t.status)]));

  add(hwT("hardware.firmware", "固件版本"),
    [hwT("hardware.name", "名称"), hwT("hardware.version", "版本")],
    (sd.firmware || []).map(f => [f.name, f.version]));

  add(hwT("hardware.events_local", "监控记录的状态变化"),
    [hwT("hardware.event_time", "时间"), hwT("hardware.event_severity", "级别"), hwT("hardware.event_message", "事件内容")],
    HW_LOCAL_EVENTS.map(e => {
      const sev = e.severity ? e.severity.charAt(0).toUpperCase() + e.severity.slice(1) : "";
      return [hwFmtTime(e.created_at), sev ? hwHealthText(sev) : "-", e.message || e.event_type || ""];
    }));

  return model;
}

// 把导出模型拍平成纯文本，喂给 AI 诊断——与导出/网页同一份数据，保证"看到什么就分析什么"。
function hwModelToText(model) {
  let s = (model.title || "设备") + "\n";
  (model.meta || []).forEach(([k, v]) => { s += `${k}: ${v}\n`; });
  (model.sections || []).forEach(sec => {
    s += `\n【${sec.title}】\n`;
    if (sec.columns && sec.columns.length) s += sec.columns.join(" | ") + "\n";
    (sec.rows || []).forEach(r => { s += r.join(" | ") + "\n"; });
  });
  return s;
}

function hwToggleExportMenu(show) {
  const menu = $("hwExportMenu"), btn = $("hwExportBtn");
  if (!menu || !btn) return;
  const open = show === undefined ? !menu.classList.contains("show") : show;
  menu.classList.toggle("show", open);
  btn.setAttribute("aria-expanded", open ? "true" : "false");
}

async function hwDoExport(fmt) {
  if (!HW_CUR) return;
  hwToggleExportMenu(false);
  try {
    const model = hwExportModel(HW_CUR);
    const sys = (HW_CUR.snap.snapshot || {}).system || {};
    const base = [hwT("hardware.export_prefix", "硬件资产"),
                  HW_CUR.snap.target_name || HW_CUR.host.id, sys.model].filter(Boolean).join("_");
    const ok = await exportModel(model, fmt, base);
    if (!ok) {
      // PDF 走 window.open，被浏览器拦了必须说清楚，否则用户以为按钮坏了
      toast(hwT("hardware.export_popup_blocked", "导出失败：请允许本站弹出窗口后重试"), "err");
      return;
    }
    if (fmt !== "pdf") toast(hwT("toast.exported", "已导出"), "ok");
  } catch (e) {
    toast(hwT("hardware.export_failed", "导出失败") + "：" + e.message, "err");
  }
}

/* ---------- 历史曲线 ---------- */

async function loadHwHistory() {
  if (!HW_CUR) return;
  const hostID = HW_CUR.host.id, target = HW_CUR.snap.target_name || "";
  HW_CHARTS = {};
  HW_HIST_SUMMARY = {};
  const loadKey = "hardware:" + hostID + ":" + (target || "") + ":" + HW_HIST_RANGE;
  const load = (typeof beginRangeLoad === "function")
    ? beginRangeLoad(loadKey)
    : { signal: undefined, isCurrent: () => true };
  const specs = [
    ["hwChartTemp", "temperature", hwT("hardware.temperature", "温度传感器") + " (°C)", v => v.toFixed(0) + "°C"],
    ["hwChartFan", "fan_rpm", hwT("hardware.fans", "风扇") + " (RPM)", v => v.toFixed(0)],
    ["hwChartPower", "power", hwT("hardware.power", "功耗") + " (W)", v => v.toFixed(0) + "W"],
    ["hwChartHealth", "health_score", hwT("hardware.health", "健康") + " (2=OK/1=Warning/0=Critical)", v => v.toFixed(0)],
  ];
  await Promise.all(specs.map(async ([cid, metric, title, fmt]) => {
    try {
      const qs = new URLSearchParams({ host: hostID, metric, range: HW_HIST_RANGE });
      if (target) qs.set("target", target);
      const fetchOpts = load.signal ? { signal: load.signal } : undefined;
      const d = await fetch(`${API}/hardware/history?${qs}`, fetchOpts).then(r => r.json());
      if (!load.isCurrent()) return;
      const series = hwParseSeries(d.points || []).sort((a, b) => String(a.name).localeCompare(String(b.name)));
      // 汇总该指标历史概况（min/max/avg/最新），供导出的「历史概况」段——补上导出此前缺失的历史数据
      const allVals = series.flatMap(s => s.pts.map(p => p[1])).filter(v => !isNaN(v));
      if (allVals.length) {
        const lastVals = series.map(s => (s.pts.length ? s.pts[s.pts.length - 1][1] : null)).filter(v => v != null && !isNaN(v));
        HW_HIST_SUMMARY[metric] = {
          label: title, fmt,
          min: Math.min(...allVals), max: Math.max(...allVals),
          avg: allVals.reduce((a, b) => a + b, 0) / allVals.length,
          latest: lastVals.length ? lastVals.reduce((a, b) => a + b, 0) / lastVals.length : null,
        };
      }
      if (!series.length) {
        if (!load.isCurrent()) return;
        const c = $(cid);
        if (c) drawChartEmpty(c.getContext("2d"), c.getBoundingClientRect().width || 1000, 200,
          hwT("hardware.no_history", "暂无历史数据（需等待采集积累）"));
        return;
      }
      // 把多序列（每个传感器/风扇一条）对齐成 createChart 需要的 samples 结构
      const tsSet = new Set();
      series.forEach(s => s.pts.forEach(p => tsSet.add(p[0])));
      const rawSamples = [...tsSet].sort((a, b) => a - b).map(ts => {
        const row = { timestamp: ts };
        series.forEach((s, i) => { const hit = s.pts.find(p => p[0] === ts); row["v" + i] = hit ? hit[1] : null; });
        return row;
      });
      const keys = series.map((_, i) => "v" + i);
      const samples = typeof alignJoinedSeriesSamples === "function"
        ? alignJoinedSeriesSamples(rawSamples, keys)
        : rawSamples;
      const palette = ["#4c8dff", "#f7b23b", "#2fd07a", "#f2545b", "#8b5cf6", "#43b6f0", "#e06c9a", "#6ac4b8"];
      const defs = series.slice(0, 8).map((s, i) => ({
        key: "v" + i, label: s.name, color: palette[i % palette.length], fmt,
      }));
      const opts = { title, legendMode: defs.length >= 4 ? "dash" : "full", cssH: 210, forecastScope: "hardware",
        signal: load.signal, isCurrent: load.isCurrent };
      if (!load.isCurrent()) return;
      if (typeof createChartWithForecast === "function" && typeof isChartForecastOn === "function" && isChartForecastOn("hardware")) {
        HW_CHARTS[cid] = await createChartWithForecast(cid, samples, defs, null, null, Object.assign({}, opts, { forecast: true }));
      } else {
        HW_CHARTS[cid] = createChart(cid, samples, defs, null, null, opts);
      }
    } catch (e) {
      if (e && (e.name === "AbortError" || /aborted/i.test(String(e.message || e)))) return;
      /* 单图失败不影响其它图 */
    }
  }));
}
document.addEventListener("chart-forecast-toggle", (ev) => {
  if (ev.detail && ev.detail.scope === "hardware" && typeof loadHwHistory === "function") loadHwHistory();
});

// 把 Prometheus data.result 解析成 [{name, pts:[[tsSec, val]]}]
function hwParseSeries(points) {
  const out = [];
  (points || []).forEach(p => {
    if (!p || !p.values) return;
    const lbl = p.metric || {};
    const name = lbl.sensor || lbl.fan_name || lbl.target || "value";
    const pts = p.values.map(v => [Number(v[0]), parseFloat(v[1])]).filter(v => !isNaN(v[1]));
    if (pts.length) out.push({ name, pts });
  });
  return out;
}

/* ---------- 事件（全部委托，符合 CSP script-src 'self'） ---------- */

if (!window._hwHostTreesRefreshBound) {
  window._hwHostTreesRefreshBound = true;
  let _hwTreeRefreshT = null;
  document.addEventListener("aiops:host-trees-refresh", () => {
    if (!document.querySelector("#view-hardware.active")) return;
    if (_hwTreeRefreshT) clearTimeout(_hwTreeRefreshT);
    // Debounce BMC fan-out — join storms must not stampede /hardware/health.
    _hwTreeRefreshT = setTimeout(() => {
      _hwTreeRefreshT = null;
      if (typeof loadHardwarePanel === "function") loadHardwarePanel();
    }, 800);
  });
}

safeAddEventListener("hwRefreshBtn", "click", loadHardwarePanel);
safeAddEventListener("hardwarePanel", "click", e => {
  // 删除（树节点/详情头共用）
  const delBtn = e.target.closest("[data-hwdel]");
  if (delBtn) {
    e.stopPropagation();
    const target = delBtn.dataset.hwdel;
    const hostID = delBtn.dataset.hwhost;
    if (!target || !hostID) return;
    if (!confirm(hwT("hardware.confirm_delete", "确定删除该硬件资产记录？删除后不可恢复。"))) return;
    fetch(`${API}/hardware/${encodeURIComponent(hostID)}?target=${encodeURIComponent(target)}`, { method: "DELETE" })
      .then(r => r.ok ? r.json() : Promise.reject(r.statusText))
      .then(() => {
        toast(hwT("toast.deleted", "已删除"), "ok");
        if (HW_SEL === hostID + "|" + target) HW_SEL = null; // 被删设备正被选中→重选默认
        loadHardwarePanel();
      })
      .catch(err => toast(hwT("hardware.delete_failed", "删除失败") + ": " + err, "err"));
    return;
  }
  // AI 诊断（详情头）
  if (e.target.closest("[data-hwai]")) {
    if (!HW_CUR) return;
    if (typeof openAIAssist !== "function") { toast(hwT("hardware.ai_unavailable", "AI 面板未就绪"), "err"); return; }
    const model = hwExportModel(HW_CUR);
    openAIAssist({
      task: "hardware_diagnosis",
      title: "🤖 AI 硬件诊断 · " + (model.title || "设备"),
      mode: "analyze",
      context: hwModelToText(model).slice(0, 14000),
    });
    return;
  }
  // 导出下拉（详情头）
  const eb = e.target.closest("[data-hwexpbtn]");
  if (eb) {
    e.stopPropagation();
    const menu = eb.parentElement.querySelector("[data-hwexpmenu]");
    if (menu) menu.classList.toggle("show");
    return;
  }
  const eo = e.target.closest("[data-hwexport]");
  if (eo) {
    document.querySelectorAll("[data-hwexpmenu].show").forEach(m => m.classList.remove("show"));
    hwDoExport(eo.dataset.hwexport);
    return;
  }
  // 历史曲线时间范围 + 图表放大（详情已内联进面板）
  const r = e.target.closest("[data-hwrange]");
  if (r) {
    HW_HIST_RANGE = r.dataset.hwrange;
    document.querySelectorAll("#hardwarePanel [data-hwrange]").forEach(b =>
      b.classList.toggle("active", b.dataset.hwrange === HW_HIST_RANGE));
    loadHwHistory();
    return;
  }
  const z = e.target.closest("[data-hwchart]");
  if (z) { const ch = HW_CHARTS[z.dataset.hwchart]; if (ch) openChartZoom(ch); return; }
  // 树：宿主机折叠/展开（caret）
  const tg = e.target.closest("[data-hwhtoggle]");
  if (tg) {
    e.preventDefault();
    e.stopPropagation();
    const id = tg.dataset.hwhtoggle;
    if (!id) return;
    if (HW_TREE_COLLAPSED.has(id)) HW_TREE_COLLAPSED.delete(id); else HW_TREE_COLLAPSED.add(id);
    hwRefreshTree();
    return;
  }
  // 树：选中宿主机 / 全部
  const hostNode = e.target.closest("[data-hwhost]");
  if (hostNode && !e.target.closest("[data-hwsel]")) {
    hwSelectHost(hostNode.dataset.hwhost || "");
    return;
  }
  // 树：选中目标
  const node = e.target.closest("[data-hwsel]");
  if (node) hwSelect(node.dataset.hwsel);
});
safeAddEventListener("hardwarePanel", "keydown", e => {
  if (e.key !== "Enter" && e.key !== " ") return;
  const node = e.target.closest("[data-hwsel]");
  if (node) { e.preventDefault(); hwSelect(node.dataset.hwsel); return; }
  const hostNode = e.target.closest("[data-hwhost]");
  if (hostNode) { e.preventDefault(); hwSelectHost(hostNode.dataset.hwhost || ""); }
});

/* ---------- 筛选 / 搜索 / 重复主机清理（工具栏是重渲染出来的，一律事件委托） ---------- */

let HW_SEARCH_T = null;
function onHwSearchInput(e) {
  if (!e.target) return;
  if (e.target.id === "hwTreeSearch") {
    HW_TREE_Q = e.target.value || "";
    hwRefreshTree();
    return;
  }
  if (e.target.id !== "hwSearch") return;
  clearTimeout(HW_SEARCH_T);
  const v = e.target.value;
  HW_SEARCH_T = setTimeout(() => { HW_FILTER.q = v; hwRefreshTree(); }, 200);
}
safeAddEventListener("hardwarePanel", "input", onHwSearchInput);
safeAddEventListener("hardwarePanel", "search", onHwSearchInput);
safeAddEventListener("hardwarePanel", "change", e => {
  if (e.target.id === "hwStatusFilter") { HW_FILTER.status = e.target.value; hwRefreshTree(); }
  else if (e.target.id === "hwFreshFilter") { HW_FILTER.fresh = e.target.value; hwRefreshTree(); }
});

// 重复主机的提示/查看/清理逻辑在 duplicates.js 里，主机页与硬件页共用一份。
dupBindPanel("hardwarePanel", loadHardwarePanel);

// 导出下拉：按钮开合 + 选项点击 + 点外部/Esc 收起
// AI 诊断：把该设备硬件快照交给 /ai/assist 流式分析；仅人工采纳/反馈后沉淀记忆。
safeAddEventListener("hwAIBtn", "click", () => {
  if (!HW_CUR) return;
  if (typeof openAIAssist !== "function") { toast(hwT("hardware.ai_unavailable", "AI 面板未就绪"), "err"); return; }
  const model = hwExportModel(HW_CUR);
  openAIAssist({
    task: "hardware_diagnosis",
    title: "🤖 AI 硬件诊断 · " + (model.title || "设备"),
    mode: "analyze",
    context: hwModelToText(model).slice(0, 14000)
  });
});
safeAddEventListener("hwExportBtn", "click", e => { e.stopPropagation(); hwToggleExportMenu(); });
safeAddEventListener("hwExportMenu", "click", e => {
  const o = e.target.closest("[data-hwexport]");
  if (o) hwDoExport(o.dataset.hwexport);
});
document.addEventListener("click", e => {
  // 点在下拉自身之内不收起（选项的 click 由上面的委托处理）
  if (!e.target.closest("#hwExportDD")) hwToggleExportMenu(false);
  if (!e.target.closest(".hwx-dd")) {
    document.querySelectorAll("[data-hwexpmenu].show").forEach(m => m.classList.remove("show"));
  }
});
document.addEventListener("keydown", e => {
  if (e.key !== "Escape") return;
  hwToggleExportMenu(false);
  document.querySelectorAll("[data-hwexpmenu].show").forEach(m => m.classList.remove("show"));
});

/* 详情头/空态样式（树布局已迁入 style.css .rtx-*） */
function hwInjectTreeStyles() {
  if (document.getElementById("hwx-css")) return;
  const s = document.createElement("style");
  s.id = "hwx-css";
  s.textContent = `
  .hwx-detailbox{min-height:160px}
  .hwx-dhead{display:flex;align-items:center;gap:10px;flex-wrap:wrap;padding-bottom:12px;margin-bottom:14px;border-bottom:1px solid var(--line)}
  .hwx-dtitle{min-width:0;flex:1;overflow:hidden}
  .hwx-dtitle .t{font-size:16px;font-weight:600;color:var(--txt);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .hwx-dtitle .s{font-size:12px;color:var(--muted);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .hwx-dd{position:relative}
  .hwx-empty{color:var(--muted);padding:48px 12px;text-align:center;font-size:13px}`;
  document.head.appendChild(s);
}

// 供 nav.js 的 _pageRenderers 调用
if (typeof window._pageRenderers === "undefined") window._pageRenderers = {};
window._pageRenderers.hardware = loadHardwarePanel;
