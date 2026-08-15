/* Category/folder badge: CSS ellipsis + full path in title (names are often long). */
function hostCategoryBadgeHTML(h) {
  const raw = String((h && (h.folder_path || h.category)) || "").trim();
  const label = raw ? esc(raw) : I18N.t("section.uncategorized");
  const tip = raw
    ? (raw + " · " + I18N.t("section.click_set_folder"))
    : I18N.t("section.click_set_folder");
  return `<span class="cat-badge" data-act="cat" title="${esc(tip)}">${label}</span>`;
}

/** User-facing host label: hostname (ip). Never show raw host id. */
function hostDisplayTitle(h) {
  if (typeof HostPicker !== "undefined" && HostPicker.hostTitle) return HostPicker.hostTitle(h || {});
  const name = (h && h.hostname) ? String(h.hostname).trim() : "";
  const ip = (h && (h.ip || h.agent_ip || h.primary_ip)) ? String(h.ip || h.agent_ip || h.primary_ip).trim() : "";
  if (name && ip) return `${name} (${ip})`;
  if (name) return name;
  if (ip) return ip;
  return I18N.t("ui.unknown_host", "未知主机");
}

/* ---------- 渲染：主机卡片 ---------- */
function hostCard(h) {
  const m = h.latest || {};
  const swap = (m.swap_total || 0) > 0
    ? bar(I18N.t("section.swap"), m.swap_percent || 0, (m.swap_percent || 0).toFixed(1) + "% · " + fmtGB(m.swap_used || 0) + "/" + fmtGB(m.swap_total || 0) + I18N.t("unit.gb"))
    : "";
  const disks = (Array.isArray(m.disks) ? m.disks : []).filter(d => !isSystemMount(d.path));
  const disksHtml = disks.length
    ? disks.map(d => {
        const shortPath = shortenMountPath(d.path);
        const label = I18N.t("ui.disk_label") + " " + esc(shortPath) + (d.percent >= 90 ? " ⚠" : "");
        return bar(label, d.percent, d.percent.toFixed(1) + "% · " + fmtGB(d.used) + "/" + fmtGB(d.total) + I18N.t("unit.gb"), undefined, d.path);
      }).join("")
    : bar(I18N.t("ui.disk"), m.disk_percent || 0, (m.disk_percent || 0).toFixed(1) + "% · " + fmtGB(m.disk_used || 0) + "/" + fmtGB(m.disk_total || 0) + I18N.t("unit.gb"), "disk");
  const gpus = Array.isArray(m.gpus) ? m.gpus : [];
  const gpusHtml = gpus.map(g => {
    const util = Math.max(0, Math.min(g.util_percent || 0, 100));
    const memTxt = (g.mem_total || 0) > 0 ? " · " + I18N.t("ui.gpu_mem_short") + " " + fmtGB(g.mem_used || 0) + "/" + fmtGB(g.mem_total || 0) + I18N.t("unit.gb") : "";
    const tempTxt = (g.temp || 0) > 0 ? " · " + Math.round(g.temp) + "℃" : "";
    const name = esc((g.name || "GPU").slice(0, 22));
    return `<div class="metric gpu"><div class="row"><span class="label">GPU ${name}</span>
      <span class="val mono">${(g.util_percent || 0).toFixed(0)}%${memTxt}${tempTxt}</span></div>
      <div class="bar"><div class="fill" style="width:${util}%;background:${usageColor(g.util_percent || 0)}"></div></div></div>`;
  }).join("");
  let chips = "";
  if (h.custom && Object.keys(h.custom).length) {
    chips = `<div class="chips">` + Object.entries(h.custom).sort().map(([k, v]) => {
      const isDown = /\.up$/.test(k) && v === 0;
      const num = Number.isInteger(v) ? v : v.toFixed(1);
      return `<span class="chip ${isDown ? "crit" : ""}">${esc(k)} <b>${num}</b></span>`;
    }).join("") + `<span class="chip-label">${I18N.t("section.custom_metrics")}</span></div>`;
  }
  const loadTitle = I18N.t("section.load_avg") + (h.os === "windows" ? I18N.t("misc.windows_approx") : "");
  const lastCell = !h.online
    ? `<span class="g offline-tag" title="${I18N.t("section.last_seen")} ${fmtDateTime(h.last_seen)}">⚠ ${I18N.t("ui.offline_status")} ${ago(h.last_seen)}</span>`
    : h.stale
      ? `<span class="g stale-tag" title="${I18N.t("section.data_stale")}，${I18N.t("section.last_seen")} ${fmtDateTime(h.last_seen)}">⚠ ${I18N.t("ui.data")} ${ago(h.last_seen)}</span>`
      : `<span class="g">${I18N.t("ui.running")} ${fmtUptime(m.uptime || 0)}</span>`;
  const agentVer = (typeof agentVersionBadgeHTML === "function") ? agentVersionBadgeHTML(h) : "";
  const agentSel = (typeof agentSelectCheckboxHTML === "function") ? agentSelectCheckboxHTML(h) : "";
  const outdatedCls = (typeof agentHostCardClass === "function") ? agentHostCardClass(h) : "";
  return `<div class="host ${h.online ? "online" : "offline"}${outdatedCls}" tabindex="0" data-id="${esc(h.id)}" data-name="${esc(hostDisplayTitle(h))}" data-cat="${esc(h.category || "")}" data-folder="${esc(h.folder_id || "")}">
    <div class="host-head">
      <div class="host-name">${agentSel}<span class="dot ${h.online ? "on" : "off"}"></span>
        <div class="hn" data-act="detail" title="${esc(hostDisplayTitle(h))}">${esc(hostDisplayTitle(h))}</div>
      </div>
      <div class="host-tags">
        ${hostCategoryBadgeHTML(h)}
        <span class="os-badge">${esc((h.os || "?").toUpperCase())}</span>
        ${agentVer}
        ${(h.online && TERMINAL_ENABLED) ? `<button class="term-btn" data-act="term" title="${I18N.t('section.terminal_desc')}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/></svg></button>` : ""}
        ${(h.online && DESKTOP_ENABLED) ? `<button class="term-btn desktop-btn" data-act="desktop" title="${I18N.t('desktop.btn_title')}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg></button>` : ""}
        <button class="x-btn" data-act="del" title="${I18N.t("ui.delete")}">✕</button>
      </div>
    </div>
    <div class="host-meta" title="${esc([h.ip, h.platform, h.arch].filter(Boolean).join(" · "))}">
      <span class="hm-ip mono">${h.ip ? esc(h.ip) : "—"}</span>
      <span class="hm-sep">·</span>
      <span class="hm-os">${esc(h.platform || "—")}${h.arch ? " · " + esc(h.arch) : ""}</span>
    </div>
    ${bar("CPU", m.cpu_percent || 0, (m.cpu_percent || 0).toFixed(1) + "% · " + (m.cpu_cores || 0) + I18N.t("ui.cores"), "cpu")}
    ${bar(I18N.t("ui.memory"), m.mem_percent || 0, (m.mem_percent || 0).toFixed(1) + "% · " + fmtGB(m.mem_used || 0) + "/" + fmtGB(m.mem_total || 0) + I18N.t("unit.gb"), "mem")}
    ${swap}
    ${disksHtml}
    ${gpusHtml}
    <div class="loadline" title="${loadTitle}">
      <div class="load-cell"><div class="lv mono">${(m.load1 || 0).toFixed(2)}</div><div class="lk">${I18N.t("section.load_1m")}</div></div>
      <div class="load-cell"><div class="lv mono">${(m.load5 || 0).toFixed(2)}</div><div class="lk">${I18N.t("section.load_5m")}</div></div>
      <div class="load-cell"><div class="lv mono">${(m.load15 || 0).toFixed(2)}</div><div class="lk">${I18N.t("section.load_15m")}</div></div>
    </div>
    ${chips}
    <div class="foot">
      <span class="g">↑<span class="mono">${fmtRate(m.net_sent_rate || 0)}</span> ↓<span class="mono">${fmtRate(m.net_recv_rate || 0)}</span></span>
      <span class="g">💾<span class="mono">${I18N.t("ui.disk_read")} ${fmtIORate(m.disk_read_rate || 0)}</span> <span class="mono">${I18N.t("ui.disk_write")} ${fmtIORate(m.disk_write_rate || 0)}</span></span>
      <span class="g">💿<span class="mono">${fmtIOPS((m.disk_read_iops || 0) + (m.disk_write_iops || 0))} ${I18N.t("unit.iops")}</span></span>
      <span class="g">🔗<span class="mono">${m.net_conns || 0}</span> ${I18N.t("section.connections")}</span>
      <span class="g">📊<span class="mono">${m.proc_count || 0}</span> ${I18N.t("section.processes")}</span>
      ${lastCell}
    </div>
  </div>`;
}

/* ---------- 渲染：主机列表表头（列表视图） ---------- */
// Column labels turn the dense row into a scannable table. The header reuses the
// row's column classes so both react to the same container queries and stay
// aligned when a column drops out on narrow layouts.
function hostListHeader() {
  const sel = (typeof agentSelectSpacerHTML === "function") ? agentSelectSpacerHTML() : "";
  return `<div class="hrow-head" role="row">
    ${sel}<span class="hrow-dot" aria-hidden="true"></span>
    <div class="hrow-id">${esc(I18N.t("ui.col_host", "主机"))}</div>
    <span class="hh-os">${esc(I18N.t("ui.col_os", "系统"))}</span>
    <span class="hh-cat">${esc(I18N.t("ui.col_group", "分组"))}</span>
    <div class="hrow-metrics">${esc(I18N.t("ui.col_usage", "资源使用"))}</div>
    <span class="hrow-net">${esc(I18N.t("ui.col_net", "网络"))}</span>
    <span class="hrow-load">${esc(I18N.t("ui.col_load", "负载"))}</span>
    <span class="hrow-last">${esc(I18N.t("ui.col_status", "状态"))}</span>
    <span class="hrow-actions">${esc(I18N.t("ui.col_actions", "操作"))}</span>
  </div>`;
}

/* ---------- 渲染：主机列表行（列表视图） ---------- */
function hostRow(h) {
  const m = h.latest || {};
  const disks = (Array.isArray(m.disks) ? m.disks : []).filter(d => !isSystemMount(d.path));
  const diskMax = disks.length ? Math.max(...disks.map(d => d.percent)) : (m.disk_percent || 0);
  const gpus = Array.isArray(m.gpus) ? m.gpus : [];
  const gpuMax = gpus.length ? Math.max(...gpus.map(g => g.util_percent || 0)) : null;
  // Mini metric bar: label + progress bar + value
  const miniBar = (label, v) => {
    const pct = Math.max(0, Math.min(v || 0, 100));
    const color = usageColor(v || 0);
    return `<div class="hrow-mbar" title="${label} ${pct.toFixed(1)}%">
      <span class="hm-k">${label}</span>
      <div class="hm-track"><div class="hm-fill" style="width:${pct}%;background:${color}"></div></div>
      <span class="hm-v mono" style="color:${color}">${pct.toFixed(0)}%</span>
    </div>`;
  };
  const isStale = h.online && h.stale;
  const statusCls = !h.online ? "offline" : isStale ? "stale" : "online";
  const last = !h.online
    ? `<span class="hrow-status offline" title="${I18N.t("section.last_seen")} ${fmtDateTime(h.last_seen)}">⚠ ${I18N.t("ui.offline_status")} ${ago(h.last_seen)}</span>`
    : isStale
      ? `<span class="hrow-status stale" title="${I18N.t('section.data_stale')}">⚠ ${ago(h.last_seen)}</span>`
      : `<span class="hrow-status online">${I18N.t("ui.running")} ${fmtUptime(m.uptime || 0)}</span>`;
  const termBtn = (h.online && TERMINAL_ENABLED)
    ? `<button class="term-btn" data-act="term" title="${I18N.t('ui.remote_terminal')}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/></svg></button>`
    : "";
  const deskBtn = (h.online && DESKTOP_ENABLED)
    ? `<button class="term-btn desktop-btn" data-act="desktop" title="${I18N.t('desktop.btn_title')}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg></button>`
    : "";
  // Rendered even when the platform reports no load average: an omitted cell
  // would pull every following column left and break alignment with the header.
  const loadStr = m.load1 !== undefined ? `${I18N.t("ui.load")} ${(m.load1||0).toFixed(2)} / ${(m.load5||0).toFixed(2)}` : "—";
  const ipTitle = h.ip ? esc(h.ip) : "";
  const agentVer = (typeof agentVersionBadgeHTML === "function") ? agentVersionBadgeHTML(h) : "";
  const agentSel = (typeof agentSelectCheckboxHTML === "function") ? agentSelectCheckboxHTML(h) : "";
  const outdatedCls = (typeof agentHostCardClass === "function") ? agentHostCardClass(h) : "";
  return `<div class="host hrow ${statusCls}${outdatedCls}" tabindex="0" data-id="${esc(h.id)}" data-name="${esc(hostDisplayTitle(h))}" data-cat="${esc(h.category || "")}" data-folder="${esc(h.folder_id || "")}">
    ${agentSel}<span class="hrow-dot ${h.online ? "on" : "off"}"></span>
    <div class="hrow-id">
      <div class="hrow-name" data-act="detail" title="${esc(hostDisplayTitle(h))}">${esc(hostDisplayTitle(h))}</div>
      <div class="hrow-sub" title="${ipTitle}">${h.ip ? `<span class="mono">${esc(h.ip)}</span>` : ""}${h.platform ? `<span class="hrow-sep">·</span>${esc(h.platform)}` : ""}${agentVer ? `<span class="hrow-sep">·</span>${agentVer}` : ""}</div>
    </div>
    <span class="os-badge">${esc((h.os || "?").toUpperCase())}</span>
    ${hostCategoryBadgeHTML(h)}
    <div class="hrow-metrics">
      ${miniBar("CPU", m.cpu_percent)}${miniBar(I18N.t("ui.memory"), m.mem_percent)}${miniBar(I18N.t("ui.disk"), diskMax)}${gpuMax !== null ? miniBar("GPU", gpuMax) : ""}
    </div>
    <span class="hrow-net g">↑<span class="mono">${fmtRate(m.net_sent_rate || 0)}</span> ↓<span class="mono">${fmtRate(m.net_recv_rate || 0)}</span></span>
    <span class="hrow-load mono">${loadStr}</span>
    <span class="hrow-last">${last}</span>
    <span class="ch-actions hrow-actions">${termBtn}${deskBtn}<button class="mini-btn del" data-act="del" title="${I18N.t("ui.delete")}">✕</button></span>
  </div>`;
}

function setCurFolder(id) {
  CUR_FOLDER = id || "";
  try { localStorage.setItem("aiops_host_folder", CUR_FOLDER); } catch (e) {}
  HOST_PAGE = 1;
}

function setCurType(key) {
  CUR_TYPE = key || "";
  try { localStorage.setItem("aiops_host_type", CUR_TYPE); } catch (e) {}
  HOST_PAGE = 1;
}

function setHostTreeMode(mode) {
  HOST_TREE_MODE = mode === "type" ? "type" : "folder";
  try { localStorage.setItem("aiops_host_tree_mode", HOST_TREE_MODE); } catch (e) {}
  HOST_PAGE = 1;
}

function persistHostTreeCollapsed() {
  try { localStorage.setItem("aiops_host_tree_collapsed", JSON.stringify([...HOST_TREE_COLLAPSED])); } catch (e) {}
}

function hostFolderMatchSet(folderId) {
  if (!folderId) return null;
  if (folderId === "__ungrouped__") return new Set(["__ungrouped__"]);
  const ids = new Set();
  const walk = (nodes) => {
    for (const n of nodes || []) {
      if (n.id === folderId) {
        const collect = (x) => { ids.add(x.id); (x.children || []).forEach(collect); };
        collect(n);
        return true;
      }
      if (walk(n.children)) return true;
    }
    return false;
  };
  if (!walk(HOST_FOLDERS.folders || []) || ids.size === 0) {
    // Stale localStorage / deleted folder — clear filter instead of emptying the list.
    if (CUR_FOLDER === folderId) setCurFolder("");
    return null;
  }
  return ids;
}

function flattenHostFolders(folders, prefix) {
  const out = [];
  (folders || []).forEach(n => {
    const path = prefix ? prefix + " / " + n.name : n.name;
    out.push({ id: n.id, name: n.name, path });
    out.push(...flattenHostFolders(n.children || [], path));
  });
  return out;
}

function hostTypeKey(h) {
  const p = (h.platform || "").trim();
  if (p) return p;
  const os = (h.os || "").trim().toLowerCase();
  if (os === "windows") return "Windows";
  if (os === "darwin" || os === "macos") return "macOS";
  if (os === "linux") return "Linux";
  return os ? os : I18N.t("section.type_unknown");
}

function hostInFolderFilter(h, matchSet) {
  if (!matchSet) return true;
  const fid = h.folder_id || "__ungrouped__";
  return matchSet.has(fid);
}

function hostInTypeFilter(h) {
  if (!CUR_TYPE) return true;
  return hostTypeKey(h) === CUR_TYPE;
}

function currentHostsCrumb() {
  const q = String(HOST_SEARCH || "").trim();
  if (q) {
    return I18N.t("section.host_search_results", "搜索结果") + " · " + q;
  }
  if (HOST_TREE_MODE === "type") {
    return CUR_TYPE
      ? I18N.t("section.type_tree") + " / " + CUR_TYPE
      : I18N.t("section.all_hosts_tree");
  }
  if (!CUR_FOLDER) return I18N.t("section.all_hosts_tree");
  if (CUR_FOLDER === "__ungrouped__") return I18N.t("section.uncategorized");
  const flat = flattenHostFolders(HOST_FOLDERS.folders || []);
  const cur = flat.find(x => x.id === CUR_FOLDER);
  return cur ? cur.path : CUR_FOLDER;
}

/** Build searchable haystack for a host (id / name / IP / OS / folder…). */
function hostSearchHaystack(h) {
  if (!h) return "";
  const parts = [
    h.id, h.hostname, h.ip, h.os, h.platform, h.arch, h.kernel, h.agent_version,
    h.category, h.folder_path, h.folder_id,
  ];
  return parts.filter(Boolean).join(" ");
}

/**
 * Match host against search query.
 * - Multi-token (space-separated): every token must match.
 * - Scoped to current tree folder/type only when query is empty;
 *   active search always matches across all hosts (see renderHosts).
 */
function hostMatchesSearch(h, query) {
  return matchesSearchTokens(hostSearchHaystack(h), query);
}

function normalizeHostSearchText(s) {
  return normalizeSearchText(s);
}

function invalidateHostRenderCache() {
  LAST_RENDER_KEY = "";
  HOST_DOM_CACHE = {};
}

async function loadHostFolders() {
  try {
    const r = await fetch(`${API}/host-folders`);
    if (!r.ok) return;
    const data = await r.json();
    HOST_FOLDERS = {
      folders: data.folders || [],
      assign: data.assign || {},
      paths: data.paths || {},
      counts: data.counts || {}
    };
  } catch (e) {}
}

/** Auto-refresh host/type trees (and open pickers) after a host joins/leaves. */
let _hostTreeAutoTimer = null;
let _hostTreeAutoBusy = false;
let _hostTreeAutoQueued = null; // latest opts while busy (coalesce, never drop)
window._hostTreeAutoInside = false;
async function refreshHostTreesAuto(opts) {
  const o = opts || {};
  if (_hostTreeAutoTimer) { clearTimeout(_hostTreeAutoTimer); _hostTreeAutoTimer = null; }
  const delay = o.immediate ? 0 : 180;
  return new Promise(resolve => {
    _hostTreeAutoTimer = setTimeout(async () => {
      _hostTreeAutoTimer = null;
      if (_hostTreeAutoBusy) {
        _hostTreeAutoQueued = o;
        resolve();
        return;
      }
      _hostTreeAutoBusy = true;
      window._hostTreeAutoInside = true;
      try {
        if (o.forceHosts !== false && typeof fetchHostsList === "function") {
          await fetchHostsList({ force: true });
        }
        if (typeof loadHostFolders === "function") await loadHostFolders();
        const activeView = document.querySelector(".view.active")?.id.replace("view-", "") || "";
        if (activeView === "hosts" && typeof renderHosts === "function") {
          renderHosts(LAST_HOSTS || []);
        } else if (typeof renderHostTree === "function" && $("hostTree")) {
          renderHostTree();
        }
        try {
          document.dispatchEvent(new CustomEvent("aiops:host-trees-refresh", {
            detail: { hosts: LAST_HOSTS || [] }
          }));
        } catch (_) {}
      } catch (_) {}
      window._hostTreeAutoInside = false;
      _hostTreeAutoBusy = false;
      const queued = _hostTreeAutoQueued;
      _hostTreeAutoQueued = null;
      resolve();
      if (queued) refreshHostTreesAuto(Object.assign({}, queued, { immediate: false }));
    }, delay);
  });
}

function folderMatchesTreeQ(n, q) {
  if (!q) return true;
  if ((n.name || "").toLowerCase().includes(q)) return true;
  return (n.children || []).some(c => folderMatchesTreeQ(c, q));
}

/** 统一树节点：固定「展开列 + 图标列」，叶子也占位，保证层级与图标对齐 */
function hostTreeCaretHTML(id, hasKids, collapsed) {
  if (!hasKids) return `<span class="htx-caret htx-caret-gap" aria-hidden="true"></span>`;
  return `<button type="button" class="htx-caret" data-folder-toggle="${esc(id)}" title="${I18N.t("section.folder_toggle")}" aria-expanded="${collapsed ? "false" : "true"}">${collapsed ? "▸" : "▾"}</button>`;
}

function hostTreeNodeHTML(n, depth, q) {
  if (q && !folderMatchesTreeQ(n, q)) return "";
  const cnt = (HOST_FOLDERS.counts && HOST_FOLDERS.counts[n.id]) || { total: 0, online: 0 };
  const sel = HOST_TREE_MODE === "folder" && CUR_FOLDER === n.id;
  const hasKids = (n.children || []).length > 0;
  const collapsed = !q && HOST_TREE_COLLAPSED.has(n.id);
  let kids = "";
  if (hasKids && !collapsed) {
    kids = `<div class="htx-children" role="group">${(n.children || []).map(c => hostTreeNodeHTML(c, depth + 1, q)).join("")}</div>`;
  }
  return `<div class="htx-folder" data-depth="${depth}">
    <div class="htx-node${sel ? " selected" : ""}${hasKids ? " has-kids" : " is-leaf"}" data-folder-sel="${esc(n.id)}" data-ctx-folder="${esc(n.id)}" role="treeitem" aria-selected="${sel ? "true" : "false"}" tabindex="0">
      ${hostTreeCaretHTML(n.id, hasKids, collapsed)}
      <span class="htx-ico htx-ico-folder" aria-hidden="true"></span>
      <span class="htx-name" title="${esc(n.name)}">${esc(n.name)}</span>
      <span class="htx-count" title="${cnt.online || 0}/${cnt.total || 0}">${cnt.total || 0}</span>
      <span class="htx-acts">
        <button type="button" class="htx-act htx-add" data-folder-add="${esc(n.id)}" title="${I18N.t("section.folder_add_child")}">+</button>
        <button type="button" class="htx-act" data-folder-ren="${esc(n.id)}" title="${I18N.t("section.folder_rename")}">✎</button>
        <button type="button" class="htx-act danger" data-folder-del="${esc(n.id)}" title="${I18N.t("section.folder_delete")}">✕</button>
      </span>
    </div>
    ${kids}
  </div>`;
}

function hostTypeTreeHTML(q) {
  const hosts = LAST_HOSTS || [];
  const map = {};
  hosts.forEach(h => {
    const k = hostTypeKey(h);
    if (!map[k]) map[k] = { total: 0, online: 0 };
    map[k].total++;
    if (h.online) map[k].online++;
  });
  const keys = Object.keys(map).sort((a, b) => a.localeCompare(b));
  const filtered = q ? keys.filter(k => k.toLowerCase().includes(q)) : keys;
  const allCnt = hosts.length;
  const rootId = "__all__";
  const collapsed = !q && HOST_TREE_COLLAPSED.has(rootId);
  const hasKids = filtered.length > 0;
  const rows = filtered.map(k => {
    const sel = CUR_TYPE === k;
    return `<div class="htx-node is-leaf${sel ? " selected" : ""}" data-type-sel="${esc(k)}" role="treeitem" aria-selected="${sel ? "true" : "false"}" tabindex="0">
      ${hostTreeCaretHTML("", false, false)}
      <span class="htx-ico htx-ico-type" aria-hidden="true"></span>
      <span class="htx-name" title="${esc(k)}">${esc(k)}</span>
      <span class="htx-count">${map[k].total}</span>
      <span class="htx-acts" aria-hidden="true"></span>
    </div>`;
  }).join("");
  const kids = (!collapsed && hasKids)
    ? `<div class="htx-children" role="group">${rows}</div>`
    : "";
  return `<div class="htx-folder htx-root" data-depth="0" role="tree">
    <div class="htx-node htx-root-node${CUR_TYPE === "" ? " selected" : ""}${hasKids ? " has-kids" : " is-leaf"}" data-type-sel="" role="treeitem" aria-selected="${CUR_TYPE === "" ? "true" : "false"}" tabindex="0">
      ${hostTreeCaretHTML(rootId, hasKids, collapsed)}
      <span class="htx-ico htx-ico-all" aria-hidden="true"></span>
      <span class="htx-name">${I18N.t("section.all_hosts_tree")}</span>
      <span class="htx-count">${allCnt}</span>
      <span class="htx-acts" aria-hidden="true"></span>
    </div>
    ${kids || (q ? `<div class="htx-empty">${I18N.t("section.type_empty_hint")}</div>` : "")}
  </div>`;
}

function hostAssetTreeHTML(q) {
  const allCnt = (LAST_HOSTS || []).length;
  const ug = (HOST_FOLDERS.counts && HOST_FOLDERS.counts.__ungrouped__) || { total: 0, online: 0 };
  const folders = HOST_FOLDERS.folders || [];
  const rootId = "__all__";
  const showRoot = !q || I18N.t("section.all_hosts_tree").toLowerCase().includes(q)
    || I18N.t("section.uncategorized").toLowerCase().includes(q)
    || folders.some(n => folderMatchesTreeQ(n, q));
  if (!showRoot) return `<div class="htx-empty">${I18N.t("section.folder_empty_hint")}</div>`;

  const showUngrouped = !q || I18N.t("section.uncategorized").toLowerCase().includes(q);
  const folderHTML = folders.map(n => hostTreeNodeHTML(n, 1, q)).join("");
  const hasKids = showUngrouped || !!folderHTML;
  const collapsed = !q && HOST_TREE_COLLAPSED.has(rootId);
  let kids = "";
  if (!collapsed && hasKids) {
    // 未分类：系统节点，固定 caret 占位与分组图标列对齐，并与自定义分组用分隔线区分
    const ungrouped = showUngrouped
      ? `<div class="htx-node htx-ungrouped is-leaf${CUR_FOLDER === "__ungrouped__" ? " selected" : ""}" data-folder-sel="__ungrouped__" data-ctx-folder="__ungrouped__" role="treeitem" aria-selected="${CUR_FOLDER === "__ungrouped__" ? "true" : "false"}" tabindex="0">
        ${hostTreeCaretHTML("", false, false)}
        <span class="htx-ico htx-ico-ungrouped" aria-hidden="true"></span>
        <span class="htx-name">${I18N.t("section.uncategorized")}</span>
        <span class="htx-count">${ug.total || 0}</span>
        <span class="htx-acts" aria-hidden="true"></span>
      </div>`
      : "";
    const sep = (showUngrouped && folderHTML)
      ? `<div class="htx-branch-sep" role="separator" aria-hidden="true"></div>`
      : "";
    kids = `<div class="htx-children" role="group">
      ${ungrouped}
      ${sep}
      ${folderHTML || (!showUngrouped ? `<div class="htx-empty">${I18N.t("section.folder_empty_hint")}</div>` : "")}
    </div>`;
  }
  return `<div class="htx-folder htx-root" data-depth="0" role="tree">
    <div class="htx-node htx-root-node${CUR_FOLDER === "" ? " selected" : ""}${hasKids ? " has-kids" : " is-leaf"}" data-folder-sel="" data-ctx-folder="" role="treeitem" aria-selected="${CUR_FOLDER === "" ? "true" : "false"}" tabindex="0">
      ${hostTreeCaretHTML(rootId, hasKids, collapsed)}
      <span class="htx-ico htx-ico-all" aria-hidden="true"></span>
      <span class="htx-name">${I18N.t("section.all_hosts_tree")}</span>
      <span class="htx-count">${allCnt}</span>
      <span class="htx-acts">
        <button type="button" class="htx-act htx-add" data-folder-add="" title="${I18N.t("section.folder_add_root")}">+</button>
      </span>
    </div>
    ${kids}
  </div>`;
}

function hostTreeHTML() {
  const mode = HOST_TREE_MODE === "type" ? "type" : "folder";
  const q = (HOST_TREE_Q || "").trim().toLowerCase();
  const body = mode === "type" ? hostTypeTreeHTML(q) : hostAssetTreeHTML(q);
  return `<div class="htx-tabs">
      <button type="button" class="htx-tab${mode === "folder" ? " active" : ""}" data-tree-mode="folder">${I18N.t("section.asset_tree")}</button>
      <button type="button" class="htx-tab${mode === "type" ? " active" : ""}" data-tree-mode="type">${I18N.t("section.type_tree")}</button>
      <span class="htx-tab-tools">
        <button type="button" class="htx-tool-btn" data-folder-refresh title="${I18N.t("section.host_refresh")}">
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>
        </button>
        ${mode === "folder" ? `<button type="button" class="htx-tool-btn htx-add-root" data-folder-add="" title="${I18N.t("section.folder_add_root")}">+</button>` : ""}
      </span>
    </div>
    <div class="htx-tree-search">
      <input type="search" id="hostTreeSearch" class="htx-tree-q" value="${esc(HOST_TREE_Q || "")}"
        placeholder="${esc(mode === "type" ? I18N.t("section.type_search_ph") : I18N.t("section.folder_search_ph"))}" autocomplete="off">
    </div>
    <div class="htx-scroll">${body}</div>`;
}

function renderHostTree() {
  const el = $("hostTree");
  if (!el) return;
  const focusId = document.activeElement && document.activeElement.id === "hostTreeSearch";
  const caret = focusId ? document.activeElement.selectionStart : null;
  el.innerHTML = hostTreeHTML();
  const layout = $("hostsLayout");
  if (layout && window.treeCollapsed) {
    const col = window.treeCollapsed("aiops_host_tree");
    layout.classList.toggle("tree-collapsed", !!col);
    const btn = layout.querySelector("[data-tree-toggle]");
    if (btn) {
      btn.textContent = col ? "›" : "‹";
      btn.setAttribute("aria-expanded", col ? "false" : "true");
    }
  }
  if (focusId) {
    const inp = el.querySelector("#hostTreeSearch");
    if (inp) {
      inp.focus();
      try { inp.setSelectionRange(caret, caret); } catch (e) {}
    }
  }
}

function hideHostTreeCtx() {
  const m = document.getElementById("htxCtxMenu");
  if (m) m.remove();
}

function showHostTreeCtx(x, y, folderId) {
  hideHostTreeCtx();
  if (HOST_TREE_MODE !== "folder") return;
  if (folderId === "__ungrouped__") return;
  const menu = document.createElement("div");
  menu.id = "htxCtxMenu";
  menu.className = "htx-ctx";
  menu.style.left = x + "px";
  menu.style.top = y + "px";
  menu.innerHTML = `
    <button type="button" class="htx-ctx-item" data-ctx="add" data-id="${esc(folderId || "")}">${I18N.t("section.ctx_create_node")}</button>
    ${folderId ? `<button type="button" class="htx-ctx-item" data-ctx="ren" data-id="${esc(folderId)}">${I18N.t("section.ctx_rename_node")}</button>
    <button type="button" class="htx-ctx-item danger" data-ctx="del" data-id="${esc(folderId)}">${I18N.t("section.ctx_delete_node")}</button>` : ""}`;
  document.body.appendChild(menu);
  const rect = menu.getBoundingClientRect();
  if (rect.right > window.innerWidth - 8) menu.style.left = Math.max(8, window.innerWidth - rect.width - 8) + "px";
  if (rect.bottom > window.innerHeight - 8) menu.style.top = Math.max(8, window.innerHeight - rect.height - 8) + "px";
  const close = (e) => {
    if (e && menu.contains(e.target)) return;
    hideHostTreeCtx();
    document.removeEventListener("mousedown", close, true);
  };
  setTimeout(() => document.addEventListener("mousedown", close, true), 0);
  menu.addEventListener("click", async (e) => {
    const item = e.target.closest("[data-ctx]");
    if (!item) return;
    const act = item.getAttribute("data-ctx");
    const id = item.getAttribute("data-id") || "";
    hideHostTreeCtx();
    if (act === "add") await hostFolderAdd(id);
    else if (act === "ren") await hostFolderRename(id);
    else if (act === "del") await hostFolderDelete(id);
  });
}

async function hostFolderAdd(parentId) {
  const flat = flattenHostFolders(HOST_FOLDERS.folders || []);
  const parent = parentId ? flat.find(x => x.id === parentId) : null;
  const name = await promptFolderName({
    title: parentId ? I18N.t("section.folder_add_child") : I18N.t("section.folder_add_root"),
    parentPath: parent ? parent.path : "",
    defaultValue: "",
    placeholder: I18N.t("section.folder_name_ph")
  });
  if (name === null || !String(name).trim()) return;
  try {
    const r = await fetch(`${API}/host-folders`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ parent_id: parentId || "", name: String(name).trim() })
    });
    if (!r.ok) {
      const e = await r.json().catch(() => ({}));
      toast(e.error || I18N.t("toast.update_failed2"), "err");
      return;
    }
    toast(I18N.t("toast.folder_saved"), "ok");
    // 新建节点时展开父级；根节点挂在「全部主机」下
    HOST_TREE_COLLAPSED.delete(parentId || "__all__");
    persistHostTreeCollapsed();
    await loadHostFolders();
    renderHosts(LAST_HOSTS);
  } catch (e) { toast(I18N.t("toast.update_failed") + e, "err"); }
}

async function hostFolderRename(id) {
  const flat = flattenHostFolders(HOST_FOLDERS.folders || []);
  const cur = flat.find(x => x.id === id);
  const parentPath = cur && cur.path.includes(" / ")
    ? cur.path.slice(0, cur.path.lastIndexOf(" / "))
    : "";
  const name = await promptFolderName({
    title: I18N.t("section.folder_rename"),
    parentPath,
    defaultValue: cur ? cur.name : "",
    placeholder: I18N.t("section.folder_name_ph")
  });
  if (name === null || !String(name).trim()) return;
  try {
    const r = await fetch(`${API}/host-folders/${encodeURIComponent(id)}`, {
      method: "PATCH", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: String(name).trim() })
    });
    if (!r.ok) {
      const e = await r.json().catch(() => ({}));
      toast(e.error || I18N.t("toast.update_failed2"), "err");
      return;
    }
    toast(I18N.t("toast.folder_saved"), "ok");
    await loadHostFolders();
    refresh();
  } catch (e) { toast(I18N.t("toast.update_failed") + e, "err"); }
}

/** 主机分组专用轻量弹窗（不用 AI 反馈那套大弹层） */
function promptFolderName(opts) {
  opts = opts || {};
  return new Promise(resolve => {
    const existing = document.getElementById("htxFolderDlgMask");
    if (existing) existing.remove();
    const mask = document.createElement("div");
    mask.id = "htxFolderDlgMask";
    mask.className = "mask htx-dlg-mask show";
    const parentPath = opts.parentPath || "";
    mask.innerHTML = `
      <div class="htx-dlg" role="dialog" aria-modal="true" aria-labelledby="htxDlgTitle">
        <div class="htx-dlg-head">
          <h3 id="htxDlgTitle">${esc(opts.title || I18N.t("section.folder_add_root"))}</h3>
          <button type="button" class="htx-dlg-x" data-htx-dlg="cancel" aria-label="${esc(I18N.t("ui.close","关闭"))}">✕</button>
        </div>
        <div class="htx-dlg-body">
          ${parentPath ? `<div class="htx-dlg-path" title="${esc(parentPath)}"><span class="htx-dlg-path-k">${esc(I18N.t("section.folder_parent"))}</span><span class="htx-dlg-path-v">${esc(parentPath)}</span></div>` : ""}
          <label class="htx-dlg-label" for="htxDlgInput">${esc(I18N.t("section.folder_name"))}</label>
          <input type="text" id="htxDlgInput" class="htx-dlg-input" maxlength="48"
            placeholder="${esc(opts.placeholder || I18N.t("section.folder_name_ph"))}"
            value="${esc(opts.defaultValue || "")}" autocomplete="off" spellcheck="false">
          <div class="htx-dlg-hint">${esc(I18N.t("section.folder_name_hint"))}</div>
          <div class="htx-dlg-err" id="htxDlgErr" hidden></div>
        </div>
        <div class="htx-dlg-foot">
          <button type="button" class="btn" data-htx-dlg="cancel">${esc(I18N.t("ui.cancel","取消"))}</button>
          <button type="button" class="btn primary" data-htx-dlg="ok">${esc(I18N.t("ui.save","保存"))}</button>
        </div>
      </div>`;
    document.body.appendChild(mask);
    const input = mask.querySelector("#htxDlgInput");
    const err = mask.querySelector("#htxDlgErr");
    let done = false;
    const finish = (v) => {
      if (done) return;
      done = true;
      document.removeEventListener("keydown", onKey, true);
      mask.remove();
      resolve(v);
    };
    const submit = () => {
      const v = (input.value || "").trim();
      if (!v) {
        err.hidden = false;
        err.textContent = I18N.t("section.folder_name_required");
        input.focus();
        return;
      }
      if (/[\\/]/.test(v)) {
        err.hidden = false;
        err.textContent = I18N.t("section.folder_name_slash");
        input.focus();
        return;
      }
      finish(v);
    };
    const onKey = (e) => {
      if (e.key === "Escape") { e.preventDefault(); finish(null); }
      else if (e.key === "Enter") { e.preventDefault(); submit(); }
    };
    mask.addEventListener("click", (e) => {
      const act = e.target.closest("[data-htx-dlg]");
      if (!act) {
        if (e.target === mask) finish(null);
        return;
      }
      if (act.getAttribute("data-htx-dlg") === "ok") submit();
      else finish(null);
    });
    document.addEventListener("keydown", onKey, true);
    setTimeout(() => { input.focus(); input.select(); }, 30);
  });
}

async function hostFolderDelete(id) {
  const flat = flattenHostFolders(HOST_FOLDERS.folders || []);
  const cur = flat.find(x => x.id === id);
  const ok = typeof uiConfirm === "function"
    ? await uiConfirm({
        title: I18N.t("ui.delete", "删除"),
        message: I18N.t("section.folder_delete_confirm") + (cur ? cur.path : id),
        tone: "danger"
      })
    : confirm(I18N.t("section.folder_delete_confirm") + (cur ? cur.path : id));
  if (!ok) return;
  try {
    const r = await fetch(`${API}/host-folders/${encodeURIComponent(id)}`, { method: "DELETE" });
    if (!r.ok) {
      const e = await r.json().catch(() => ({}));
      toast(e.error || I18N.t("toast.delete_failed"), "err");
      return;
    }
    // Clear selection if the current folder is the deleted node or under it.
    const match = hostFolderMatchSet(id);
    if (CUR_FOLDER === id || (match && match.has(CUR_FOLDER))) setCurFolder("");
    toast(I18N.t("toast.folder_deleted"), "ok");
    await loadHostFolders();
    refresh();
  } catch (e) { toast(I18N.t("toast.deleted") + ": " + e, "err"); }
}

function bindHostTreeOnce() {
  const tree = $("hostTree");
  if (!tree || tree.dataset.bound) return;
  tree.dataset.bound = "1";
  let _hostTreeSearchTimer = null;
  tree.addEventListener("input", (e) => {
    if (e.target && e.target.id === "hostTreeSearch") {
      HOST_TREE_Q = e.target.value || "";
      if (_hostTreeSearchTimer) { clearTimeout(_hostTreeSearchTimer); _hostTreeSearchTimer = null; }
      // 清空立即重建；输入防抖 250ms，避免每键全量重建树
      if (!(HOST_TREE_Q || "").trim()) {
        renderHostTree();
        return;
      }
      _hostTreeSearchTimer = setTimeout(() => {
        _hostTreeSearchTimer = null;
        renderHostTree();
      }, 250);
    }
  });
  tree.addEventListener("contextmenu", (e) => {
    const node = e.target.closest("[data-ctx-folder]");
    if (!node || HOST_TREE_MODE !== "folder") return;
    e.preventDefault();
    showHostTreeCtx(e.clientX, e.clientY, node.getAttribute("data-ctx-folder") || "");
  });
  tree.addEventListener("click", async (e) => {
    const modeBtn = e.target.closest("[data-tree-mode]");
    if (modeBtn) {
      setHostTreeMode(modeBtn.getAttribute("data-tree-mode"));
      HOST_TREE_Q = "";
      renderHosts(LAST_HOSTS);
      return;
    }
    if (e.target.closest("[data-folder-refresh]")) {
      e.stopPropagation();
      if (typeof refresh === "function") refresh();
      return;
    }
    const add = e.target.closest("[data-folder-add]");
    if (add) { e.stopPropagation(); await hostFolderAdd(add.getAttribute("data-folder-add")); return; }
    const ren = e.target.closest("[data-folder-ren]");
    if (ren) { e.stopPropagation(); await hostFolderRename(ren.getAttribute("data-folder-ren")); return; }
    const del = e.target.closest("[data-folder-del]");
    if (del) { e.stopPropagation(); await hostFolderDelete(del.getAttribute("data-folder-del")); return; }
    const tog = e.target.closest("[data-folder-toggle]");
    if (tog) {
      e.preventDefault();
      e.stopPropagation();
      const id = tog.getAttribute("data-folder-toggle");
      if (!id) return;
      if (HOST_TREE_COLLAPSED.has(id)) HOST_TREE_COLLAPSED.delete(id);
      else HOST_TREE_COLLAPSED.add(id);
      persistHostTreeCollapsed();
      renderHostTree();
      return;
    }
    const typeSel = e.target.closest("[data-type-sel]");
    if (typeSel) {
      setCurType(typeSel.getAttribute("data-type-sel") || "");
      renderHosts(LAST_HOSTS);
      return;
    }
    const sel = e.target.closest("[data-folder-sel]");
    if (sel) {
      setCurFolder(sel.getAttribute("data-folder-sel") || "");
      renderHosts(LAST_HOSTS);
    }
  });
}

function bindHostsToolbarOnce() {
  if (window._htxToolbarBound) return;
  window._htxToolbarBound = true;
  document.addEventListener("click", (e) => {
    const moreBtn = e.target.closest("#htxMoreBtn");
    const menu = $("htxMoreMenu");
    const wrap = $("htxMoreWrap");
    if (moreBtn && menu) {
      menu.hidden = !menu.hidden;
      return;
    }
    if (menu && wrap && !wrap.contains(e.target)) menu.hidden = true;
  });
}

/** Sort key for host IP: IPv4 numeric → IPv6 → other → empty. */
function hostIPSortKey(ip) {
  const s = String(ip || "").trim();
  if (!s) return { family: 9, key: "" };
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(s);
  if (m) {
    const parts = [+m[1], +m[2], +m[3], +m[4]];
    if (parts.every(n => n >= 0 && n <= 255)) {
      return { family: 4, key: parts.map(n => String(n).padStart(3, "0")).join(".") };
    }
  }
  if (s.includes(":")) return { family: 6, key: s.toLowerCase() };
  return { family: 8, key: s.toLowerCase() };
}

function compareHostsByIP(a, b) {
  const ka = hostIPSortKey(a && a.ip);
  const kb = hostIPSortKey(b && b.ip);
  if (ka.family !== kb.family) return ka.family - kb.family;
  if (ka.key < kb.key) return -1;
  if (ka.key > kb.key) return 1;
  return String((a && (a.hostname || a.id)) || "").localeCompare(String((b && (b.hostname || b.id)) || ""));
}

function renderHosts(hosts) {
  // Keep LAST_HOSTS / _cachedHosts / HOST_META coherent for Automation & other pages.
  if (typeof syncHostCache === "function") syncHostCache(hosts);
  else {
    LAST_HOSTS = hosts;
    HOST_META = hosts.map(h => ({ id: h.id, hostname: h.hostname }));
    window._cachedHosts = hosts;
  }
  if (DEFAULT_EMPTY === null && $("empty")) DEFAULT_EMPTY = $("empty").innerHTML;
  const countEl = $("hostsCount");
  if (countEl) countEl.textContent = hosts.length;
  const navHosts = $("navHosts");
  if (navHosts) navHosts.textContent = hosts.length;

  bindHostTreeOnce();
  bindHostsToolbarOnce();
  renderHostTree();

  if (!LAST_RENDER_KEY) {
    try {
      const s = localStorage.getItem("aiops_collapsed");
      if (s) {
        const arr = JSON.parse(s);
        const cats = [...new Set(hosts.map(h => h.category || I18N.t("section.uncategorized")))];
        if (Array.isArray(arr) && arr.length > 0 && cats.length > 0 && cats.every(c => arr.includes(c))) {
          localStorage.removeItem("aiops_collapsed");
        }
      }
    } catch (e) {}
  }

  const groupsEl = $("groups"), empty = $("empty"), pager = $("pager");
  if (!groupsEl || !empty || !pager) return;
  const dupBar = $("hostDupBar");
  if (dupBar) dupBar.innerHTML = dupBannerHTML();
  const crumb = $("hostsCrumb");
  if (crumb) crumb.textContent = currentHostsCrumb();

  // Active keyword searches all hosts (ignore left-tree folder/type scope).
  // Empty query keeps the current tree selection. Status filter still applies.
  const searchQ = normalizeHostSearchText(HOST_SEARCH);
  const searchActive = !!searchQ;
  const matchSet = (!searchActive && HOST_TREE_MODE === "folder") ? hostFolderMatchSet(CUR_FOLDER) : null;
  let shown = hosts.filter(h => {
    if (!searchActive) {
      if (HOST_TREE_MODE === "type") {
        if (!hostInTypeFilter(h)) return false;
      } else if (!hostInFolderFilter(h, matchSet)) {
        return false;
      }
    }
    if (HOST_FILTER === "online" && !h.online) return false;
    if (HOST_FILTER === "offline" && h.online) return false;
    if (HOST_FILTER === "outdated" && !(typeof hostAgentOutdated === "function" && hostAgentOutdated(h))) return false;
    if (!hostMatchesSearch(h, searchQ)) return false;
    return true;
  });

  if (HOST_SORT === "cpu") {
    shown.sort((a, b) => (b.latest?.cpu_percent || 0) - (a.latest?.cpu_percent || 0));
  } else if (HOST_SORT === "mem") {
    shown.sort((a, b) => (b.latest?.mem_percent || 0) - (a.latest?.mem_percent || 0));
  } else if (HOST_SORT === "recent") {
    shown.sort((a, b) => (b.last_seen || 0) - (a.last_seen || 0));
  } else if (HOST_SORT === "name") {
    shown.sort((a, b) => (a.hostname || a.id).localeCompare(b.hostname || b.id));
  } else {
    // Default: IP ascending (card + list). Numeric IPv4; IPv6 after; empty last.
    shown.sort(compareHostsByIP);
  }

  if (countEl) countEl.textContent = shown.length;

  if (!hosts.length) {
    invalidateHostRenderCache();
    groupsEl.innerHTML = ""; pager.innerHTML = ""; empty.style.display = "block"; empty.innerHTML = DEFAULT_EMPTY;
    return;
  }
  if (!shown.length) {
    // Must invalidate: otherwise clearing search can early-return on a stale
    // LAST_RENDER_KEY while #groups is still empty from this no-match path.
    invalidateHostRenderCache();
    groupsEl.innerHTML = ""; pager.innerHTML = ""; empty.style.display = "block"; empty.textContent = I18N.t("empty.no_host_match");
    return;
  }
  empty.style.display = "none";

  const isList = HOST_VIEW === "list";
  const isMobile = window.innerWidth <= 480;
  const PAGINATION_THRESHOLD = isMobile ? (isList ? 20 : 10) : (isList ? 50 : 30);
  const pageSize = isList ? 50 : HOST_PAGE_SIZE;
  const shouldPaginate = shown.length > PAGINATION_THRESHOLD;
  let pageHosts, pages;
  if (shouldPaginate) {
    pages = Math.ceil(shown.length / pageSize);
    if (HOST_PAGE > pages) HOST_PAGE = pages;
    if (HOST_PAGE < 1) HOST_PAGE = 1;
    pageHosts = shown.slice((HOST_PAGE - 1) * pageSize, HOST_PAGE * pageSize);
  } else {
    HOST_PAGE = 1; pages = 1;
    pageHosts = shown;
  }

  const render = isList ? hostRow : hostCard;
  const wrapCls = isList ? "host-list" : "grid";
  const filterKey = searchActive
    ? ("q:" + searchQ)
    : (HOST_TREE_MODE === "type" ? ("t:" + CUR_TYPE) : ("f:" + CUR_FOLDER));
  // Include filter/sort/search so incremental update never skips a real list change.
  const newKey = pageHosts.map(h => h.id).join(",") + "|" + HOST_VIEW + "|" + HOST_PAGE + "|" + filterKey + "|" + HOST_TREE_MODE + "|" + HOST_FILTER + "|" + HOST_SORT;
  if (LAST_RENDER_KEY === newKey && Object.keys(HOST_DOM_CACHE).length > 0) {
    pageHosts.forEach(h => updateHostCard(h));
    renderPager(pages, shown.length);
    renderHostTree();
    return;
  }
  LAST_RENDER_KEY = newKey;

  // 选中节点下扁平展示（卡片/列表），不再按路径二次分组
  const head = isList ? hostListHeader() : "";
  groupsEl.innerHTML = `<div class="group htx-flat"><div class="${wrapCls}">${head}${pageHosts.map(render).join("")}</div></div>`;
  buildHostCache();
  renderPager(pages, shown.length);
}

function renderPager(pages, total) {
  const pager = $("pager");
  if (!pager) return;
  if (pages <= 1) { pager.innerHTML = `<span class="pinfo">${I18N.t("section.pager_total", "共")} ${total} ${I18N.t("section.pager_hosts", "台")}</span>`; return; }
  let btns = `<button ${HOST_PAGE === 1 ? "disabled" : ""} data-pg="prev">‹</button>`;
  for (let i = 1; i <= pages; i++) {
    if (i === 1 || i === pages || Math.abs(i - HOST_PAGE) <= 1) {
      btns += `<button class="${i === HOST_PAGE ? "active" : ""}" data-pg="${i}">${i}</button>`;
    } else if (Math.abs(i - HOST_PAGE) === 2) {
      btns += `<span class="pinfo">…</span>`;
    }
  }
  btns += `<button ${HOST_PAGE === pages ? "disabled" : ""} data-pg="next">›</button>`;
  btns += `<span class="pinfo">${I18N.t("section.pager_total", "共")} ${total} ${I18N.t("section.pager_hosts", "台")} · ${HOST_PAGE}/${pages}</span>`;
  pager.innerHTML = btns;
}

/* ---------- 主机操作 ---------- */
async function delHost(id, name) {
  const msg = `${I18N.t("valid.confirm_delete_host_prefix")}${I18N.t("ui.delete")}「${name}」？\n若该主机 Agent 仍在运行，约 60 ${I18N.t("time.sec")}后会重新出现。`;
  const ok = typeof uiConfirm === "function"
    ? await uiConfirm({
        title: I18N.t("ui.delete", "删除"),
        message: msg,
        tone: "danger"
      })
    : confirm(msg);
  if (!ok) return;
  try {
    const r = await fetch(`${API}/hosts/${encodeURIComponent(id)}`, { method: "DELETE" });
    if (r.ok) { toast(I18N.t("toast.host_deleted"), "ok"); refresh(); } else { toast(I18N.t("toast.delete_failed"), "err"); }
  } catch (e) { toast(I18N.t("toast.deleted") + ": " + e, "err"); }
}

async function editCategory(id, cur) {
  const flat = flattenHostFolders(HOST_FOLDERS.folders || []);
  const options = [{ id: "__ungrouped__", path: I18N.t("section.uncategorized") }]
    .concat(flat.map(f => ({ id: f.id, path: f.path })));
  const host = (LAST_HOSTS || []).find(h => h.id === id);
  const curFid = (host && host.folder_id) || "__ungrouped__";
  const folderId = await promptMoveFolder({
    hostname: (host && host.hostname) || id,
    options,
    currentId: curFid
  });
  if (folderId === null) return;
  try {
    const r = await fetch(`${API}/hosts/${encodeURIComponent(id)}/folder`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ folder_id: folderId })
    });
    if (r.ok) { toast(I18N.t("toast.category_updated"), "ok"); await loadHostFolders(); refresh(); }
    else toast(I18N.t("toast.update_failed2"), "err");
  } catch (e) { toast(I18N.t("toast.update_failed") + e, "err"); }
}

function promptMoveFolder(opts) {
  opts = opts || {};
  const options = opts.options || [];
  return new Promise(resolve => {
    const existing = document.getElementById("htxFolderDlgMask");
    if (existing) existing.remove();
    const mask = document.createElement("div");
    mask.id = "htxFolderDlgMask";
    mask.className = "mask htx-dlg-mask show";
    const optsHtml = options.map(o =>
      `<option value="${esc(o.id)}"${o.id === opts.currentId ? " selected" : ""}>${esc(o.path)}</option>`
    ).join("");
    mask.innerHTML = `
      <div class="htx-dlg" role="dialog" aria-modal="true" aria-labelledby="htxDlgTitle">
        <div class="htx-dlg-head">
          <h3 id="htxDlgTitle">${esc(I18N.t("section.set_folder"))}</h3>
          <button type="button" class="htx-dlg-x" data-htx-dlg="cancel" aria-label="${esc(I18N.t("ui.close","关闭"))}">✕</button>
        </div>
        <div class="htx-dlg-body">
          <div class="htx-dlg-path"><span class="htx-dlg-path-k">${esc(I18N.t("section.host_short"))}</span><span class="htx-dlg-path-v">${esc(opts.hostname || "")}</span></div>
          <label class="htx-dlg-label" for="htxDlgSelect">${esc(I18N.t("section.folder_name"))}</label>
          <select id="htxDlgSelect" class="htx-dlg-input htx-dlg-select">${optsHtml}</select>
          <div class="htx-dlg-hint">${esc(I18N.t("section.set_folder_hint"))}</div>
        </div>
        <div class="htx-dlg-foot">
          <button type="button" class="btn" data-htx-dlg="cancel">${esc(I18N.t("ui.cancel","取消"))}</button>
          <button type="button" class="btn primary" data-htx-dlg="ok">${esc(I18N.t("ui.save","保存"))}</button>
        </div>
      </div>`;
    document.body.appendChild(mask);
    const sel = mask.querySelector("#htxDlgSelect");
    let done = false;
    const finish = (v) => {
      if (done) return;
      done = true;
      document.removeEventListener("keydown", onKey, true);
      mask.remove();
      resolve(v);
    };
    const onKey = (e) => {
      if (e.key === "Escape") { e.preventDefault(); finish(null); }
      else if (e.key === "Enter") { e.preventDefault(); finish(sel.value); }
    };
    mask.addEventListener("click", (e) => {
      const act = e.target.closest("[data-htx-dlg]");
      if (!act) {
        if (e.target === mask) finish(null);
        return;
      }
      if (act.getAttribute("data-htx-dlg") === "ok") finish(sel.value);
      else finish(null);
    });
    document.addEventListener("keydown", onKey, true);
    setTimeout(() => sel.focus(), 30);
  });
}

/* ---------- 主机趋势弹窗 ---------- */
let DETAIL_HOST_ID = '';
let DETAIL_HOST_NAME = '';
let DETAIL_TIME_RANGE = 1; // hours: 1/3/6/12/24/72/168/336（默认 1 小时）
let DETAIL_CUSTOM = null;   // {from,to} unix seconds — set when a custom range is active
let DETAIL_SAMPLES = [];
let DETAIL_LOAD_SEQ = 0;
/** Frozen query window for the open detail session (reduces now-drift on re-click). */
let DETAIL_ANCHOR = null; // { hostId, rangeH, from, to } | null
let DETAIL_SHARED_FC = null; // shared enrich result for current load

// 统一的时间跨度控件渲染函数（主机图表和监控图表共用）
// 快捷时间跨度（小时）：1/3/6/12 小时 + 1/3/7/14 天（+ 自定义，由各视图单独渲染）
const CHART_SPANS = [1, 3, 6, 12, 24, 72, 168, 336];
function historySourceHintText(src) {
  switch (String(src || "").toLowerCase()) {
    case "ram": return I18N.t("section.history_ram_only", "仅内存缓存，重启后变短");
    case "ram-fallback":
    case "vm_miss": return I18N.t("section.history_vm_miss", "时序库暂无数据，仅内存缓存");
    case "vm+ram":
    case "vm":
    case "mixed": return I18N.t("section.history_vm", "持久化时序");
    default: return "";
  }
}
function chartSpanLabel(h) {
  return h < 24 ? h + I18N.t("time.hour") : (h / 24) + I18N.t("time.day");
}
function renderChartControls(currentRange, prefix) {
  return CHART_SPANS.map(h =>
    `<button type="button" class="chip-btn ${currentRange === h ? "active" : ""}" data-${prefix}="${h}">${chartSpanLabel(h)}</button>`
  ).join("");
}

function renderDetailToolbar(from, to) {
  const f = (typeof toLocalDatetimeValue === "function") ? toLocalDatetimeValue(from) : "";
  const t = (typeof toLocalDatetimeValue === "function") ? toLocalDatetimeValue(to) : "";
  return `<div class="chart-controls" id="detailChartControls">
        ${renderChartControls(DETAIL_CUSTOM ? -1 : DETAIL_TIME_RANGE, "range")}
        <button type="button" class="chip-btn ${DETAIL_CUSTOM ? "active" : ""}" data-custom-toggle title="${I18N.t("time.custom_range") || "自定义时间范围"}">${I18N.t("time.custom") || "自定义"}</button>
        ${typeof forecastChipHTML === "function" ? forecastChipHTML("host-detail") : ""}
        <button type="button" class="chip-btn ai-assist-btn" id="detailAIBtn" title="${I18N.t("hosts.ai_analyze_title","用 AI 解读该主机近期指标趋势")}"><span class="ai-assist-btn-ic">🤖</span>${I18N.t("hosts.ai_analyze","AI 分析")}</button>
        <span class="chart-custom-range" id="detailCustomPanel"${DETAIL_CUSTOM ? "" : " hidden"}>
          <input type="datetime-local" id="detailCustomFrom" class="dt-input" value="${f}">
          <span class="dt-sep">→</span>
          <input type="datetime-local" id="detailCustomTo" class="dt-input" value="${t}">
          <button type="button" class="chip-btn primary" data-custom-apply>${I18N.t("time.custom_apply") || "应用"}</button>
        </span>
      </div>`;
}

async function openDetail(id, name) {
  DETAIL_HOST_ID = id;
  DETAIL_HOST_NAME = name || id;
  DETAIL_TIME_RANGE = 1;
  DETAIL_CUSTOM = null;
  DETAIL_ANCHOR = null;
  DETAIL_SHARED_FC = null;
  // 每次打开主机趋势默认关闭预测（需手动点「预测」开启）
  if (typeof setChartForecastOn === "function") setChartForecastOn("host-detail", false);
  $("detailTitle").textContent = name + " " + I18N.t("section.recent_trend");
  const body = $("detailBody");
  const win = resolveDetailWindow();
  body.innerHTML = `${renderDetailToolbar(win.from, win.to)}<div class="empty-line">${I18N.t("ui.loading")}</div>`;
  $("detailMask").classList.add("show");
  await loadAndRenderCharts();
}

/** Align unix seconds down to step for stable PromQL buckets. */
function alignUnixFloor(ts, step) {
  step = Math.max(1, step | 0);
  return Math.floor(ts / step) * step;
}

/**
 * Client-side LOCF for core gauges when a sample is missing the field entirely
 * (undefined/null). Does NOT treat numeric 0 as missing — that needs server
 * presence maps (history_align.go). Prevents load1/5/15 from vanishing on
 * partial JSON / older API payloads.
 */
function alignHistoryGaugeSamples(samples) {
  const keys = [
    "cpu_percent", "mem_percent", "disk_percent", "swap_percent",
    "load1", "load5", "load15", "proc_count", "net_conns",
    "net_recv_rate", "net_sent_rate",
    "disk_io_util_percent", "disk_read_rate", "disk_write_rate",
    "disk_read_iops", "disk_write_iops"
  ];
  const last = Object.create(null);
  const out = [];
  for (const sm of samples || []) {
    if (!sm) continue;
    const row = Object.assign({}, sm);
    let ts = +row.timestamp;
    if (!Number.isFinite(ts) || ts <= 0) continue;
    if (ts > 1e12) ts = Math.floor(ts / 1000); // VM ms → unix sec
    row.timestamp = ts;
    for (const k of keys) {
      const v = row[k];
      if (v == null || (typeof v === "number" && !isFinite(v))) {
        if (last[k] != null) row[k] = last[k];
      } else {
        last[k] = +v;
      }
    }
    out.push(row);
  }
  out.sort((a, b) => a.timestamp - b.timestamp);
  return out;
}

/** Keep durable mounts (latest snapshot), not the union of every docker overlay
 *  that existed anywhere in the window — that union is what made 6h+ disk charts
 *  look like a rainbow scribble. */
function stableDiskPaths(samples, cap) {
  cap = cap || 12;
  const list = samples || [];
  if (!list.length) return [];
  const latest = list[list.length - 1];
  const fromLatest = (latest.disks || []).filter(d => d && d.path);
  if (fromLatest.length) {
    if (fromLatest.length <= cap) return fromLatest.map(d => d.path).sort();
    return fromLatest
      .slice()
      .sort((a, b) => (b.total || 0) - (a.total || 0))
      .slice(0, cap)
      .map(d => d.path)
      .sort();
  }
  const counts = Object.create(null);
  list.forEach(s => (s.disks || []).forEach(d => {
    if (d && d.path) counts[d.path] = (counts[d.path] || 0) + 1;
  }));
  const min = Math.max(3, Math.floor(list.length * 0.25));
  return Object.keys(counts)
    .filter(p => counts[p] >= min)
    .sort((a, b) => counts[b] - counts[a])
    .slice(0, cap)
    .sort();
}

function chartStepForSpan(spanSec) {
  let step = Math.floor(Math.max(1, spanSec) / 480);
  if (step < 5) step = 5;
  if (step > 3600) step = 3600;
  return step;
}

function formatChartXLabel(d, spanSec, t0, t1) {
  const pad = n => String(n).padStart(2, "0");
  const hhmm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  if (spanSec > 172800) return `${d.getMonth() + 1}/${d.getDate()}`;
  const crossesDay = Math.floor(t0 / 86400) !== Math.floor(t1 / 86400);
  if (spanSec > 6 * 3600 || crossesDay) {
    return `${d.getMonth() + 1}/${d.getDate()} ${hhmm}`;
  }
  return hhmm;
}

function fmtHistoryCoverage(sec) {
  sec = Math.max(0, Math.round(Number(sec) || 0));
  if (sec < 120) return sec + "秒";
  if (sec < 7200) return Math.round(sec / 60) + "分钟";
  if (sec < 172800) return (sec / 3600).toFixed(1).replace(/\.0$/, "") + "小时";
  return (sec / 86400).toFixed(1).replace(/\.0$/, "") + "天";
}

function downsampleChartSamples(samples, maxPts) {
  const n = (samples || []).length;
  if (!n || n <= maxPts) return samples;
  const out = new Array(maxPts);
  const step = (n - 1) / (maxPts - 1);
  for (let i = 0; i < maxPts; i++) {
    let idx = Math.round(i * step);
    if (idx >= n) idx = n - 1;
    out[i] = samples[idx];
  }
  return out;
}

/** Resolve [from,to] for host detail; freeze within the same host+preset session. */
function resolveDetailWindow() {
  if (DETAIL_CUSTOM) {
    DETAIL_ANCHOR = null;
    return { from: DETAIL_CUSTOM.from, to: DETAIL_CUSTOM.to };
  }
  const rangeH = DETAIL_TIME_RANGE;
  const spanSec = Math.max(3600, rangeH * 3600);
  const step = chartStepForSpan(spanSec);
  if (
    DETAIL_ANCHOR &&
    DETAIL_ANCHOR.hostId === DETAIL_HOST_ID &&
    DETAIL_ANCHOR.rangeH === rangeH &&
    DETAIL_ANCHOR.from < DETAIL_ANCHOR.to
  ) {
    return { from: DETAIL_ANCHOR.from, to: DETAIL_ANCHOR.to };
  }
  const now = Math.floor(Date.now() / 1000);
  const to = alignUnixFloor(now, step);
  const from = to - spanSec;
  DETAIL_ANCHOR = { hostId: DETAIL_HOST_ID, rangeH, from, to };
  return { from, to };
}

async function loadAndRenderCharts() {
  const body = $("detailBody");
  const win = resolveDetailWindow();
  const from = win.from;
  const to = win.to;
  const spanH = Math.max(0, (to - from) / 3600); // effective window in hours
  const load = (typeof beginRangeLoad === "function")
    ? beginRangeLoad("host-detail:" + DETAIL_HOST_ID)
    : { signal: undefined, isCurrent: () => true, seq: 0 };
  DETAIL_LOAD_SEQ = load.seq;
  DETAIL_SHARED_FC = null;

  // 取消上一轮懒加载观察，避免切时间范围后旧回调继续触发。
  if (DETAIL_CHART_IO) { try { DETAIL_CHART_IO.disconnect(); } catch (_) {} DETAIL_CHART_IO = null; }
  DETAIL_CHART_PENDING = {};
  // Drop previous canvas chart state so stale paint cannot reuse registry.
  Object.keys(DETAIL_CHARTS || {}).forEach(k => {
    try {
      const c = DETAIL_CHARTS[k] && DETAIL_CHARTS[k].canvas;
      if (c) c._chart = null;
    } catch (_) {}
  });
  DETAIL_CHARTS = {};

  try {
    const r = await fetch(`${API}/hosts/${encodeURIComponent(DETAIL_HOST_ID)}/history?from=${from}&to=${to}`,
      load.signal ? { signal: load.signal } : undefined);
    if (!load.isCurrent()) return;
    if (!r.ok) throw new Error("HTTP " + r.status);
    const rawSamples = await r.json().catch(() => []);
    if (!load.isCurrent()) return;
    const samples = alignHistoryGaugeSamples(Array.isArray(rawSamples) ? rawSamples : []);
    if (!samples.length) {
      DETAIL_SAMPLES = [];
      const src = (r.headers && r.headers.get) ? (r.headers.get("X-AIOps-History-Source") || "") : "";
      const emptyHint = historySourceHintText(src);
      const emptyExtra = emptyHint ? `<div class="hint">${emptyHint}</div>` : "";
      body.innerHTML = `${renderDetailToolbar(from, to)}<div class="empty-line">${I18N.t("empty.no_history")}</div>${emptyExtra}`;
      return;
    }
    DETAIL_SAMPLES = samples;

    // 组织图表：每个图表包裹在 .chart-wrap 内，右上角提供放大按钮；真正绘制延后到可见时（懒加载）。
    DETAIL_CHARTS = {};
    const gran = spanH <= 2 ? I18N.t("time.raw") : spanH <= 48 ? I18N.t("time.1m_agg") : I18N.t("time.5m_agg");
    const dataSpan = samples.length > 1 ? (samples[samples.length - 1].timestamp - samples[0].timestamp) : 0;
    const reqSpan = Math.max(0, to - from);
    const src = (r.headers && r.headers.get) ? (r.headers.get("X-AIOps-History-Source") || "") : "";
    const srcText = historySourceHintText(src);
    const srcHint = srcText ? ` · ${srcText}` : "";
    const coverHint = (reqSpan > 3600 && dataSpan > 0 && dataSpan < reqSpan * 0.5)
      ? ` · ${I18N.t("section.partial_history", "仅覆盖")} ${fmtHistoryCoverage(dataSpan)} / ${fmtHistoryCoverage(reqSpan)}`
      : "";
    const hasGPU = samples.some(s => Array.isArray(s.gpus) && s.gpus.length);
    const hasConns = samples.some(s => Array.isArray(s.conns) && s.conns.length);
    const pct = v => v.toFixed(1) + '%';
    const wrap = id => `<div class="chart-wrap" data-lazy-chart="${id}"><canvas id="${id}" width="1000" height="240"></canvas>` +
      `<button class="chart-enlarge" data-chart="${id}" title="${I18N.t('ui.zoom_preview')}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg></button></div>`;
    body.innerHTML = `
      ${renderDetailToolbar(from, to)}
      <div class="chart-container">
        ${wrap('chartCombo')}${wrap('chartCPU')}${wrap('chartMem')}${wrap('chartLoad')}${wrap('chartDisk')}${hasGPU ? wrap('chartGPU') + wrap('chartGPUTemp') + wrap('chartGPUMemPct') + wrap('chartGPUMem') : ''}${wrap('chartNet')}${hasConns ? wrap('chartConns') + wrap('chartConnStates') : ''}${wrap('chartDiskIO')}${wrap('chartIOPS')}${wrap('chartProc')}
      </div>
      <div class="hint">${I18N.t("section.sample_points")}: ${samples.length} · ${I18N.t("section.granularity")}: ${gran}${coverHint}${srcHint}</div>
    `;

    // 先只登记「如何画」；进入视口后再 createChart，避免一次同步创建十多张 Canvas 卡顿。
    const lazy = (id, series, yMin, yMax, title) => {
      DETAIL_CHART_PENDING[id] = { samples, series, yMin, yMax, title, axisFrom: from, axisTo: to };
    };
    // 资源组合：磁盘用聚合 disk_percent（与分盘图语义不同，标题注明）
    lazy('chartCombo', [
      { key: 'cpu_percent', label: I18N.t("section.cpu_usage"), color: '#4c8dff', fmt: pct },
      { key: 'mem_percent', label: I18N.t("section.mem_usage"), color: '#8b5cf6', fmt: pct },
      { key: 'disk_percent', label: I18N.t("section.disk_usage") + " · " + I18N.t("section.disk_agg", "聚合"), color: '#f7b23b', fmt: pct },
    ], 0, 100, I18N.t("section.resource_combo", "资源组合 · CPU / 内存 / 磁盘(聚合)"));
    lazy('chartCPU',
      [{ key: 'cpu_percent', label: I18N.t("section.cpu_usage"), color: '#4c8dff', fmt: pct }], 0, 100, I18N.t("section.cpu_usage"));
    lazy('chartMem',
      [{ key: 'mem_percent', label: I18N.t("section.mem_usage"), color: '#8b5cf6', fmt: pct }], 0, 100, I18N.t("section.mem_usage"));
    lazy('chartLoad', [
      { key: 'load1', label: I18N.t("section.load_1m_label"), color: '#4c8dff', fmt: v => v.toFixed(1) },
      { key: 'load5', label: I18N.t("section.load_5m_label"), color: '#f7b23b', fmt: v => v.toFixed(1) },
      { key: 'load15', label: I18N.t("section.load_15m_label"), color: '#f2545b', fmt: v => v.toFixed(1) },
    ], null, null, I18N.t("section.load_avg"));

    const diskKeys = stableDiskPaths(samples, 12);
    const latestDisk = {};
    for (let i = samples.length - 1; i >= 0 && Object.keys(latestDisk).length < diskKeys.length; i--) {
      (samples[i].disks || []).forEach(d => { if (d && d.path && !(d.path in latestDisk)) latestDisk[d.path] = d; });
    }
    const _gb = b => b / 1073741824;
    const diskLabel = (path) => {
      const d = latestDisk[path];
      const shortPath = shortenMountPath(path);
      if (!d || !d.total) return '磁盘 ' + shortPath;
      const used = _gb(d.used), tot = _gb(d.total);
      return `磁盘 ${shortPath} · 已用 ${used.toFixed(0)}/${tot.toFixed(0)}GB · 剩 ${(tot - used).toFixed(0)}GB`;
    };
    const diskSeries = diskKeys.map((path, idx) => ({
      key: `disk_${path}`, label: diskLabel(path),
      color: ['#f7b23b', '#2fd07a', '#f2545b', '#43b6f0', '#8b5cf6', '#e06c9a'][idx % 6], fmt: pct,
      transform: (s) => { const d = (s.disks || []).find(x => x.path === path); return d ? d.percent : null; }
    }));
    lazy('chartDisk',
      diskSeries.length
        ? diskSeries
        : [{ key: 'disk_percent', label: (I18N.t("section.root_partition") || "根分区") + " · " + I18N.t("section.disk_agg", "聚合"), color: '#f7b23b', fmt: pct }],
      0, 100, I18N.t("section.disk_usage"));

    if (hasGPU) {
      const latestGpus = ((samples[samples.length - 1] || {}).gpus) || [];
      const gpuNameSet = new Set();
      latestGpus.forEach(g => {
        const nm = (g && g.name) ? String(g.name) : "";
        if (nm) gpuNameSet.add(nm);
      });
      if (!gpuNameSet.size) {
        samples.forEach(s => (s.gpus || []).forEach(g => {
          const nm = (g && g.name) ? String(g.name) : "";
          if (nm) gpuNameSet.add(nm);
        }));
      }
      const gpuNames = [...gpuNameSet].sort();
      const gpalette = ['#8b5cf6', '#43b6f0', '#2fd07a', '#f7b23b', '#f2545b', '#e06c9a'];
      const gcolor = idx => gpalette[idx % gpalette.length];
      const gpuByName = (nm, field) => (s) => {
        const g = (s.gpus || []).find(x => x && x.name === nm);
        return g ? (g[field] || 0) : null;
      };
      const gbUnit = I18N.t("unit.gb");
      const gpuBytesGB = (nm, field) => (s) => {
        const g = (s.gpus || []).find(x => x && x.name === nm);
        return g ? (g[field] || 0) / 1073741824 : null;
      };
      lazy('chartGPU', gpuNames.map((nm, idx) => ({
        key: `gpu_${nm}`, label: nm, color: gcolor(idx), fmt: v => v.toFixed(0) + '%', transform: gpuByName(nm, 'util_percent')
      })), 0, 100, I18N.t("section.gpu_usage"));
      lazy('chartGPUTemp', gpuNames.map((nm, idx) => ({
        key: `gput_${nm}`, label: nm, color: gcolor(idx), fmt: v => v.toFixed(0) + '℃', transform: gpuByName(nm, 'temp')
      })), null, null, I18N.t("section.gpu_temp"));
      lazy('chartGPUMemPct', gpuNames.map((nm, idx) => ({
        key: `gpump_${nm}`, label: nm, color: gcolor(idx), fmt: v => v.toFixed(0) + '%', transform: gpuByName(nm, 'mem_percent')
      })), 0, 100, I18N.t("section.gpu_mem_pct"));
      const gpuMemSeries = [];
      gpuNames.forEach((nm, idx) => {
        gpuMemSeries.push({ key: `gpumu_${nm}`, label: `${nm} · ${I18N.t("section.gpu_mem_used")}`, color: gcolor(idx * 2), fmt: v => v.toFixed(1) + gbUnit, transform: gpuBytesGB(nm, 'mem_used') });
        gpuMemSeries.push({ key: `gpumf_${nm}`, label: `${nm} · ${I18N.t("section.gpu_mem_free")}`, color: gcolor(idx * 2 + 1), fmt: v => v.toFixed(1) + gbUnit, transform: gpuBytesGB(nm, 'mem_free') });
      });
      lazy('chartGPUMem', gpuMemSeries, null, null, I18N.t("section.gpu_vram"));
    }

    lazy('chartNet', [
      { key: 'net_recv_rate', label: I18N.t("section.net_recv"), color: '#2fd07a', fmt: fmtRate },
      { key: 'net_sent_rate', label: I18N.t("section.net_send"), color: '#43b6f0', fmt: fmtRate },
    ], null, null, I18N.t("section.net_throughput"));

    if (hasConns) {
      const sumProto = (s, proto) => Array.isArray(s.conns) ? s.conns.reduce((a, c) => c.proto === proto ? a + (c.count || 0) : a, 0) : null;
      lazy('chartConns', [
        { key: 'conn_tcp', label: 'TCP', color: '#43b6f0', fmt: v => v.toFixed(0), transform: (s) => sumProto(s, 'tcp') },
        { key: 'conn_udp', label: 'UDP', color: '#2fd07a', fmt: v => v.toFixed(0), transform: (s) => sumProto(s, 'udp') },
      ], null, null, I18N.t("section.conn_count"));
      const KEY_STATES = ['ESTABLISHED', 'TIME_WAIT', 'LISTEN', 'CLOSE_WAIT'];
      const stateSet = KEY_STATES.filter(st => samples.some(s => (s.conns || []).some(c => c.proto === 'tcp' && c.state === st)));
      const stateColors = { ESTABLISHED: '#4c8dff', TIME_WAIT: '#f7b23b', LISTEN: '#2fd07a', CLOSE_WAIT: '#f2545b' };
      const stateSeries = stateSet.map((st, idx) => ({
        key: `cst_${idx}`, label: st, color: stateColors[st] || '#8b5cf6', fmt: v => v.toFixed(0),
        transform: (s) => { if (!Array.isArray(s.conns)) return null; const c = s.conns.find(x => x.proto === 'tcp' && x.state === st); return c ? c.count : 0; }
      }));
      if (stateSeries.length) lazy('chartConnStates', stateSeries, null, null, I18N.t("section.conn_states"));
    }

    lazy('chartDiskIO', [
      { key: 'disk_read_rate', label: I18N.t("ui.disk_read"), color: '#2fd07a', fmt: fmtIORate },
      { key: 'disk_write_rate', label: I18N.t("ui.disk_write"), color: '#f7b23b', fmt: fmtIORate },
    ], null, null, I18N.t("ui.disk_io"));
    lazy('chartIOPS', [
      { key: 'disk_read_iops', label: I18N.t("ui.disk_read_iops"), color: '#2fd07a', fmt: fmtIOPS },
      { key: 'disk_write_iops', label: I18N.t("ui.disk_write_iops"), color: '#f7b23b', fmt: fmtIOPS },
    ], null, null, I18N.t("ui.disk_iops_title"));
    lazy('chartProc', [
      { key: 'proc_count', label: '进程数', color: '#8b5cf6', fmt: v => v.toFixed(0) },
    ], null, null, '进程数趋势');

    if (!load.isCurrent()) return;

    // Shared forecast once for all charts — avoids N racing POSTs painting stale canvases.
    const fcOn = typeof isChartForecastOn === "function" && isChartForecastOn("host-detail");
    if (fcOn && typeof enrichSharedForecast === "function") {
      const allSeries = [];
      Object.keys(DETAIL_CHART_PENDING).forEach(cid => {
        const sp = DETAIL_CHART_PENDING[cid];
        if (sp && sp.series) allSeries.push(...sp.series);
      });
      const fcMethod = typeof getChartForecastModel === "function" ? getChartForecastModel("host-detail") : "auto";
      const en = await enrichSharedForecast(samples, allSeries, {
        forecast: true,
        signal: load.signal,
        isCurrent: () => load.isCurrent(),
        method: fcMethod,
        forecastScope: "host-detail",
        hostId: DETAIL_HOST_ID
      });
      if (!load.isCurrent() || (en && en.stale)) return;
      DETAIL_SHARED_FC = en;
      // Surface which model actually ran (helps verify model switching).
      try {
        const meta = en && en.meta;
        const hint = meta && (meta.message || meta.Message);
        const hintEl = body.querySelector(".hint");
        if (hintEl && hint) {
          hintEl.textContent = `${I18N.t("section.sample_points")}: ${samples.length} · ${I18N.t("section.granularity")}: ${gran} · ${hint}`;
        }
      } catch (_) {}
    }

    DETAIL_CHARTS = {};
    mountDetailLazyCharts(body, load.seq, load);
  } catch (e) {
    if (e && (e.name === "AbortError" || e.message === "The user aborted a request.")) return;
    if (!load.isCurrent()) return;
    body.innerHTML = `${renderDetailToolbar(from, to)}<div class="empty-line">加载失败: ${esc(e)}</div>`;
  }
}

let DETAIL_CHART_IO = null;
let DETAIL_CHART_PENDING = {};

/** 视口进入时才 createChart；首屏可见的图表立即绘制（无入场动画）。 */
function mountDetailLazyCharts(root, loadSeq, loadHandle) {
  const seq = loadSeq != null ? loadSeq : DETAIL_LOAD_SEQ;
  const isCurrent = () => {
    if (loadHandle && typeof loadHandle.isCurrent === "function") return loadHandle.isCurrent();
    return seq === DETAIL_LOAD_SEQ;
  };
  const mountOne = async (id) => {
    if (!isCurrent()) return;
    const spec = DETAIL_CHART_PENDING[id];
    if (!spec || DETAIL_CHARTS[id]) return;
    delete DETAIL_CHART_PENDING[id];
    const fcOn = typeof isChartForecastOn === "function" && isChartForecastOn("host-detail");
    const legendMode = fcOn ? "wrap" : "dash";
    const chartOpts = { title: spec.title, noEntrance: true, cssH: 220, legendMode, forecastScope: "host-detail",
      axisFrom: spec.axisFrom || 0, axisTo: spec.axisTo || 0,
      _fcBase: { samples: spec.samples, series: spec.series, yMin: spec.yMin, yMax: spec.yMax, title: spec.title,
        axisFrom: spec.axisFrom || 0, axisTo: spec.axisTo || 0,
        reload: { hostId: DETAIL_HOST_ID, mode: "fields", forecastScope: "host-detail" } } };
    if (fcOn && DETAIL_SHARED_FC && typeof sliceForecastForChart === "function") {
      if (!isCurrent()) return;
      const sliced = sliceForecastForChart(DETAIL_SHARED_FC, spec.series, spec.samples);
      if (!isCurrent()) return;
      if (sliced && sliced.samples && sliced.samples.length) {
        DETAIL_CHARTS[id] = createChart(id, sliced.samples, sliced.series, spec.yMin, spec.yMax,
          Object.assign({}, chartOpts, { nowTs: sliced.nowTs || 0 }));
      } else {
        DETAIL_CHARTS[id] = createChart(id, spec.samples, spec.series, spec.yMin, spec.yMax, chartOpts);
      }
    } else if (fcOn && typeof createChartWithForecast === "function") {
      const ch = await createChartWithForecast(id, spec.samples, spec.series, spec.yMin, spec.yMax, Object.assign({}, chartOpts, {
        forecast: true, forecastScope: "host-detail",
        signal: loadHandle && loadHandle.signal,
        isCurrent
      }));
      if (!isCurrent()) return;
      DETAIL_CHARTS[id] = ch;
    } else {
      DETAIL_CHARTS[id] = createChart(id, spec.samples, spec.series, spec.yMin, spec.yMax, chartOpts);
    }
  };
  const wraps = root.querySelectorAll("[data-lazy-chart]");
  if (!("IntersectionObserver" in window)) {
    wraps.forEach(el => mountOne(el.dataset.lazyChart));
    return;
  }
  DETAIL_CHART_IO = new IntersectionObserver((entries) => {
    entries.forEach(en => {
      if (!en.isIntersecting) return;
      const id = en.target.dataset.lazyChart;
      mountOne(id);
      DETAIL_CHART_IO.unobserve(en.target);
    });
  }, { root: null, rootMargin: "120px 0px", threshold: 0.01 });
  wraps.forEach(el => {
    // 首屏已在视口内的直接绘制，其余交给观察器（滚动时再画）。
    const rect = el.getBoundingClientRect();
    if (rect.top < window.innerHeight + 80 && rect.bottom > -40) mountOne(el.dataset.lazyChart);
    else DETAIL_CHART_IO.observe(el);
  });
}

// 详情弹窗事件委托：放大按钮 + 时间范围切换
// 重复主机横幅的按钮（横幅是重渲染出来的，故走事件委托）。
// 清理后强制刷新主机列表：记录已被删掉，页面必须跟着更新。
dupBindPanel("hostDupBar", () => refresh());
// 首屏拉一次重复分组；有则在下一次渲染时显示横幅
loadDuplicates(() => {
  const bar = $("hostDupBar");
  if (bar) bar.innerHTML = dupBannerHTML();
});

document.addEventListener("chart-forecast-toggle", (ev) => {
  if (ev.detail && ev.detail.scope === "host-detail" && DETAIL_HOST_ID) loadAndRenderCharts();
});

safeAddEventListener("detailBody", "click", e => {
  const en = e.target.closest(".chart-enlarge");
  if (en) {
    const id = en.dataset.chart;
    // 懒加载尚未触发时，放大前先强制挂载该图。
    if (!DETAIL_CHARTS[id] && DETAIL_CHART_PENDING[id]) {
      const spec = DETAIL_CHART_PENDING[id];
      delete DETAIL_CHART_PENDING[id];
      const fcOn = typeof isChartForecastOn === "function" && isChartForecastOn("host-detail");
      const finish = (ch) => { if (ch) { DETAIL_CHARTS[id] = ch; openChartZoom(ch); } };
      const chartOpts = { title: spec.title, noEntrance: true, cssH: 220, legendMode: fcOn ? "wrap" : "dash",
        forecastScope: "host-detail",
        _fcBase: { samples: spec.samples, series: spec.series, yMin: spec.yMin, yMax: spec.yMax, title: spec.title,
          reload: { hostId: DETAIL_HOST_ID, mode: "fields", forecastScope: "host-detail" } } };
      if (fcOn && DETAIL_SHARED_FC && typeof sliceForecastForChart === "function") {
        const sliced = sliceForecastForChart(DETAIL_SHARED_FC, spec.series, spec.samples);
        if (sliced && sliced.samples && sliced.samples.length) {
          finish(createChart(id, sliced.samples, sliced.series, spec.yMin, spec.yMax,
            Object.assign({}, chartOpts, { nowTs: sliced.nowTs || 0 })));
        } else {
          finish(createChart(id, spec.samples, spec.series, spec.yMin, spec.yMax, chartOpts));
        }
        return;
      }
      if (fcOn && typeof createChartWithForecast === "function") {
        createChartWithForecast(id, spec.samples, spec.series, spec.yMin, spec.yMax, Object.assign({}, chartOpts, {
          forecast: true, forecastScope: "host-detail",
          isCurrent: () => true
        })).then(finish);
        return;
      }
      finish(createChart(id, spec.samples, spec.series, spec.yMin, spec.yMax, chartOpts));
      return;
    }
    const ch = DETAIL_CHARTS[id];
    if (ch) openChartZoom(ch);
    return;
  }
  // AI 分析主机趋势
  if (e.target.closest("#detailAIBtn")) {
    analyzeHostDetailAI();
    return;
  }
  // 自定义时间范围：展开/收起面板
  const tog = e.target.closest("[data-custom-toggle]");
  if (tog) {
    const panel = $("detailCustomPanel");
    if (panel) { panel.hidden = !panel.hidden; if (!panel.hidden) { const f = $("detailCustomFrom"); if (f) f.focus(); } }
    return;
  }
  // 自定义时间范围：应用
  if (e.target.closest("[data-custom-apply]")) { applyDetailCustomRange(); return; }
  const btn = e.target.closest(".chip-btn[data-range]");
  if (!btn) return;
  const next = parseInt(btn.dataset.range, 10);
  if (!Number.isFinite(next) || next <= 0) return;
  DETAIL_CUSTOM = null; // 切回预设跨度
  if (DETAIL_TIME_RANGE !== next) DETAIL_ANCHOR = null; // 新预设 → 重建冻结窗口
  DETAIL_TIME_RANGE = next;
  loadAndRenderCharts();
});

function analyzeHostDetailAI() {
  if (typeof openAIAssist !== "function") {
    if (typeof toast === "function") toast(I18N.t("assist.unavailable", "AI 面板未就绪"), "err");
    return;
  }
  const samples = DETAIL_SAMPLES || [];
  if (!samples.length) {
    if (typeof toast === "function") toast(I18N.t("empty.no_history", "暂无历史数据"), "err");
    return;
  }
  const first = samples[0], last = samples[samples.length - 1];
  const avg = (key) => {
    let s = 0, n = 0;
    samples.forEach(x => { if (typeof x[key] === "number") { s += x[key]; n++; } });
    return n ? (s / n) : 0;
  };
  const max = (key) => samples.reduce((m, x) => Math.max(m, typeof x[key] === "number" ? x[key] : 0), 0);
  const lines = [
    `主机：${DETAIL_HOST_NAME || DETAIL_HOST_ID}（id=${DETAIL_HOST_ID}）`,
    `样本数：${samples.length}，时间范围：约 ${((last.ts || last.timestamp || 0) - (first.ts || first.timestamp || 0)) / 3600} 小时`,
    `CPU：均值 ${avg("cpu_percent").toFixed(1)}% · 峰值 ${max("cpu_percent").toFixed(1)}% · 当前 ${(last.cpu_percent || 0).toFixed(1)}%`,
    `内存：均值 ${avg("mem_percent").toFixed(1)}% · 峰值 ${max("mem_percent").toFixed(1)}% · 当前 ${(last.mem_percent || 0).toFixed(1)}%`,
    `磁盘：均值 ${avg("disk_percent").toFixed(1)}% · 峰值 ${max("disk_percent").toFixed(1)}% · 当前 ${(last.disk_percent || 0).toFixed(1)}%`,
    `负载：当前 load1=${(last.load1 || 0).toFixed(2)} load5=${(last.load5 || 0).toFixed(2)} load15=${(last.load15 || 0).toFixed(2)}`,
  ];
  if (Array.isArray(last.gpus) && last.gpus.length) {
    lines.push("GPU：" + last.gpus.map(g => `${g.name || "GPU"} util=${(g.util_percent || 0).toFixed(0)}% mem=${(g.mem_percent || 0).toFixed(0)}%`).join("；"));
  }
  openAIAssist({
    task: "chart_analysis",
    title: "🤖 AI 主机分析 · " + (DETAIL_HOST_NAME || DETAIL_HOST_ID),
    mode: "analyze",
    context: lines.join("\n"),
    hint: I18N.t("hosts.ai_analyzing", "AI 正在解读主机近期指标…"),
  });
}

// 读取两个 datetime-local 输入，校验后按自定义绝对时间范围重新拉取并渲染
function applyDetailCustomRange() {
  applyCustomRangeFromInputs($("detailCustomFrom"), $("detailCustomTo"), (from, to) => {
    DETAIL_CUSTOM = { from, to };
    DETAIL_ANCHOR = null;
    loadAndRenderCharts();
  });
}

/* ---------- Canvas 折线图（交互：悬停十字线 + 数值气泡 / 框选放大 / 双击还原 / 点击放大预览） ---------- */
let DETAIL_CHARTS = {};

function chartTipEl() {
  let t = $("chartTip");
  if (!t) { t = document.createElement("div"); t.id = "chartTip"; t.className = "chart-tip"; document.body.appendChild(t); }
  return t;
}
function hideChartTip() { const t = $("chartTip"); if (t) t.style.display = "none"; }

function seriesVal(s, sample) {
  const v = s.transform ? s.transform(sample) : sample[s.key];
  return (v === null || v === undefined || isNaN(v)) ? null : v;
}

// seriesPathCommands — 折线或「穿过采样点」的 Catmull-Rom 曲线（控制点 Y 钳制到段内，避免虚高/虚低尖峰）。
// 描边与填充必须共用同一套指令，否则会出现「点/Tooltip 在峰值、实线却矮一截」的幽灵峰。
function seriesPathCommands(ctx, pts, smooth) {
  if (!pts || pts.length < 2) return;
  ctx.moveTo(pts[0].x, pts[0].y);
  const useSmooth = !!smooth && pts.length > 12;
  if (!useSmooth) {
    for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y);
    return;
  }
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[Math.max(0, i - 1)];
    const p1 = pts[i];
    const p2 = pts[i + 1];
    const p3 = pts[Math.min(pts.length - 1, i + 2)];
    let cp1x = p1.x + (p2.x - p0.x) / 6;
    let cp1y = p1.y + (p2.y - p0.y) / 6;
    let cp2x = p2.x - (p3.x - p1.x) / 6;
    let cp2y = p2.y - (p3.y - p1.y) / 6;
    const yLo = Math.min(p1.y, p2.y), yHi = Math.max(p1.y, p2.y);
    cp1y = Math.max(yLo, Math.min(yHi, cp1y));
    cp2y = Math.max(yLo, Math.min(yHi, cp2y));
    ctx.bezierCurveTo(cp1x, cp1y, cp2x, cp2y, p2.x, p2.y);
  }
}
function smoothPath(ctx, pts) {
  if (!pts || pts.length < 2) return;
  ctx.beginPath();
  seriesPathCommands(ctx, pts, true);
}

// drawChartEmpty — 在 Canvas 上绘制空状态插画
function drawChartEmpty(ctx, w, h, message) {
  ctx.clearRect(0, 0, w, h);
  const cssVar = name => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const txtColor = cssVar("--muted") || "#8a95a8";
  const lineColor = cssVar("--line2") || "#2c3442";
  const cx = w / 2, cy = h / 2;

  // 淡色折线图标轮廓
  ctx.strokeStyle = lineColor; ctx.lineWidth = 1.2; ctx.setLineDash([3, 4]); ctx.lineCap = "round";
  const iconPts = [{x: cx - 50, y: cy + 10}, {x: cx - 18, y: cy - 14}, {x: cx + 14, y: cy + 6}, {x: cx + 46, y: cy - 20}];
  ctx.beginPath(); ctx.moveTo(iconPts[0].x, iconPts[0].y);
  for (let i = 1; i < iconPts.length; i++) ctx.lineTo(iconPts[i].x, iconPts[i].y);
  ctx.stroke(); ctx.setLineDash([]);

  // 数据点
  iconPts.forEach(p => { ctx.fillStyle = lineColor; ctx.beginPath(); ctx.arc(p.x, p.y, 2.5, 0, Math.PI * 2); ctx.fill(); });

  // 居中提示文字
  ctx.fillStyle = txtColor; ctx.font = "13px -apple-system, 'Segoe UI', 'PingFang SC', sans-serif"; ctx.textAlign = "center";
  ctx.fillText(message, cx, cy + 40);
}

// createChart builds an interactive line chart on a canvas and returns its
// state. The state (samples/series/visible-window) lives on canvas._chart so a
// single set of event listeners always drives the current chart.
// sizeChartCanvas makes a canvas crisp on HiDPI screens: the pixel buffer is
// scaled by devicePixelRatio while all chart code keeps working in CSS pixels.
// cssH fixes the display height so a chart looks right at any column width
// (full-width or the two-up grid). Returns the logical {W,H,dpr} to draw within.
function sizeChartCanvas(canvas, cssH) {
  const dpr = Math.min(window.devicePixelRatio || 1, 2); // cap at 2 to bound memory
  const cssW = Math.round(canvas.getBoundingClientRect().width) || 1000;
  canvas.style.height = cssH + "px";
  canvas.width = Math.max(1, Math.round(cssW * dpr));
  canvas.height = Math.max(1, Math.round(cssH * dpr));
  canvas.getContext("2d").setTransform(dpr, 0, 0, dpr, 0, 0);
  return { W: cssW, H: cssH, dpr };
}

// resizeAllCharts re-fits every live chart to its current column width.
// Walks all canvases with `_chart` so HW/API/SNMP/NetFlow/SLO/AI charts are included
// (not only DETAIL_CHARTS / CHK_CHARTS registries).
function resizeAllCharts() {
  const seen = new Set();
  document.querySelectorAll("canvas").forEach(canvas => {
    const st = canvas._chart;
    if (!st || !canvas.isConnected || seen.has(st)) return;
    seen.add(st);
    // Only grow with container; never shrink to CSS aspect-ratio collapse (~110px).
    const boxH = Math.round(canvas.getBoundingClientRect().height) || 0;
    if (boxH > (st.cssH || 210) && Math.abs(boxH - (st.cssH || 0)) > 8) st.cssH = boxH;
    const d = sizeChartCanvas(canvas, st.cssH || 220);
    st.W = d.W; st.H = d.H; st.dpr = d.dpr;
    drawChart(st);
  });
  if (typeof DashCharts !== "undefined") {
    try { DashCharts.resizeAll(document); } catch (e) {}
  }
}
let _chartResizeTimer = null;
window.addEventListener("resize", () => {
  clearTimeout(_chartResizeTimer);
  _chartResizeTimer = setTimeout(resizeAllCharts, 150);
});

function createChart(canvasId, allSamples, series, yMin = null, yMax = null, opts = {}) {
  const canvas = $(canvasId);
  if (!canvas) return null;
  // Prefer explicit cssH. Bare <canvas width=1000 height=240> + CSS width:100% shrinks
  // by aspect ratio (~110px) — never treat that as intentional panel height.
  const measured = Math.round(canvas.getBoundingClientRect().height) || 0;
  const defaultH = opts.isZoom ? 440 : 220;
  let cssH = opts.cssH > 0 ? opts.cssH : defaultH;
  if (!opts.cssH) {
    if (opts.useMeasuredH && measured > 40) cssH = measured;
    else if (measured >= defaultH) cssH = measured; // already sized by parent/style
  }
  const dim = sizeChartCanvas(canvas, cssH);
  if (!allSamples || !allSamples.length) {
    drawChartEmpty(canvas.getContext("2d"), dim.W, dim.H, I18N.t("empty.no_trend_data") || "暂无趋势数据");
    return null;
  }
  allSamples = downsampleChartSamples(allSamples, 720);
  // Fixed-axis charts with zero finite points → empty state (avoid misleading 50–100% blank axes).
  if (yMin !== null && yMax !== null) {
    let any = false;
    for (const s of (series || [])) {
      for (const sm of allSamples) {
        const v = seriesVal(s, sm);
        if (v !== null) { any = true; break; }
      }
      if (any) break;
    }
    if (!any) {
      drawChartEmpty(canvas.getContext("2d"), dim.W, dim.H, I18N.t("empty.no_trend_data") || "暂无趋势数据");
      return null;
    }
  }
  const nSeries = (series || []).length;
  // Auto compact legend: many series or short canvas → never use full "当前/峰值" rows.
  // wrap = compact labels but up to 2 rows (forecast doubles series count).
  const legendMode = opts.legendMode || ((nSeries >= 4 || cssH < 220) ? "dash" : "full");
  const state = {
    canvas, ctx: canvas.getContext("2d"),
    W: dim.W, H: dim.H, dpr: dim.dpr, cssH,
    all: allSamples, series, yMin, yMax,
    title: opts.title || "", isZoom: !!opts.isZoom,
    legendMode, // full | dash | wrap
    nowTs: opts.nowTs || 0, // realtime|forecast boundary (unix sec)
    axisFrom: +opts.axisFrom || 0,
    axisTo: +opts.axisTo || 0,
    forecastScope: opts.forecastScope || "",
    _fcBase: opts._fcBase || null,
    reload: opts.reload || (opts._fcBase && opts._fcBase.reload) || null,
    i0: 0, i1: allSamples.length - 1,
    hover: -1, drag: false, downX: null, curX: null, moved: false,
    pad: { top: 22, right: 18, bottom: 28, left: 56 },
  };
  canvas._chart = state;

  // 默认直接绘制；详情弹窗等场景传 noEntrance 跳过入场动画，避免一次打开十多张图连环重绘卡顿。
  drawChart(state);
  if (!opts.noEntrance) {
    state._entranceStart = performance.now();
    state._entranceDur = 400;
    requestAnimationFrame(function entranceStep(now) {
      state._entranceP = Math.min(1, (now - state._entranceStart) / state._entranceDur);
      drawChart(state);
      if (state._entranceP < 1) requestAnimationFrame(entranceStep);
    });
  }

  attachChartEvents(canvas);
  return state;
}

function drawChart(state) {
  const { ctx, canvas, series, pad } = state;
  // Draw in CSS pixels; the buffer is dpr-scaled so lines/text are crisp on HiDPI.
  ctx.setTransform(state.dpr || 1, 0, 0, state.dpr || 1, 0, 0);
  const w = state.W || canvas.width, h = state.H || canvas.height;
  const vis = state.all.slice(state.i0, state.i1 + 1);
  const n = vis.length;
  ctx.clearRect(0, 0, w, h);

  const cssVar = name => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const gridColor = cssVar("--line2") || "rgba(43,53,71,.5)";
  const labelColor = cssVar("--muted") || "#8a95a8";
  const txtColor = cssVar("--txt") || "#e8eef6";
  const panelBg = cssVar("--panel") || cssVar("--bg2") || "#111621";
  const legendBg = (() => {
    // Prefer a readable translucent panel color; avoid "#xxx"+"99" when var empty.
    if (panelBg.startsWith("#") && (panelBg.length === 7 || panelBg.length === 4)) {
      return panelBg.length === 7 ? panelBg + "e6" : panelBg;
    }
    return "rgba(17,22,33,.88)";
  })();

  // Y range (fixed when yMin/yMax given, else padded auto-range)
  let dMin = state.yMin !== null ? state.yMin : Infinity;
  let dMax = state.yMax !== null ? state.yMax : -Infinity;
  series.forEach(s => vis.forEach(sm => {
    const v = seriesVal(s, sm);
    if (v !== null) { dMin = Math.min(dMin, v); dMax = Math.max(dMax, v); }
  }));
  if (dMin === Infinity) dMin = 0;
  if (dMax === -Infinity) dMax = state.yMax !== null ? state.yMax : 100;
  // Headroom so peaks sit clearly inside the plot (not glued to the top edge).
  if (state.yMin === null) dMin = Math.max(0, dMin - (dMax - dMin) * 0.04);
  if (state.yMax === null) {
    const span = Math.max(dMax - dMin, Math.abs(dMax) * 0.01, 1);
    dMax = dMax + span * 0.12;
  }
  if (dMax <= dMin) dMax = dMin + 1;
  const yRange = dMax - dMin;

  ctx.font = "10.5px 'SF Mono', 'Cascadia Code', 'JetBrains Mono', Consolas, monospace";
  let maxLabelW = 0;
  for (let i = 0; i <= 4; i++) {
    const val = dMax - (yRange / 4) * i;
    const lab = series[0] && series[0].fmt ? series[0].fmt(val) : val.toFixed(1);
    maxLabelW = Math.max(maxLabelW, ctx.measureText(lab).width);
  }
  const tFirst = vis.length ? +(vis[0].timestamp || 0) : 0;
  const tLast = vis.length ? +(vis[vis.length - 1].timestamp || 0) : 0;
  const timeSpan = Math.max(0, tLast - tFirst);
  pad.right = 14;
  pad.bottom = timeSpan > 6 * 3600 ? 28 : 26;
  pad.left = Math.max(48, Math.ceil(maxLabelW) + 12);

  // —— Layout: title + legend reserved ABOVE the plot (never overlay series) ——
  const dashLegend = state.legendMode === "dash" || state.legendMode === "wrap";
  const maxCompactLines = state.legendMode === "wrap" ? 2 : 1;
  const titleH = state.title ? 16 : 0;
  const legFont = "10.5px -apple-system, 'Segoe UI', 'PingFang SC', sans-serif";
  const truncLeg = (s, maxN) => {
    s = String(s || "");
    if (s.length <= maxN) return s;
    return s.slice(0, Math.max(1, maxN - 1)) + "…";
  };
  const buildLegend = (compact) => {
    const lines = [];
    let line = { items: [], x: 0 };
    const maxW = Math.max(80, w - pad.left - pad.right - 8);
    const maxItems = compact ? Math.min(series.length, state.legendMode === "wrap" ? 16 : 8) : series.length;
    ctx.font = legFont;
    for (let sIdx = 0; sIdx < maxItems; sIdx++) {
      const s = series[sIdx];
      const vals = [];
      vis.forEach(sm => { const v = seriesVal(s, sm); if (v !== null) vals.push(v); });
      const cur = vals.length ? vals[vals.length - 1] : 0;
      const peak = vals.length ? Math.max(...vals) : 0;
      const fmtV = v => s.fmt ? s.fmt(v) : v.toFixed(1);
      let labelText = compact
        ? truncLeg(s.label || ("#" + (sIdx + 1)), 14)
        : `${s.label}  当前 ${fmtV(cur)} · 峰值 ${fmtV(peak)}`;
      const itemW = ctx.measureText(labelText).width + 26;
      if (line.items.length && line.x + itemW > maxW) {
        lines.push(line);
        line = { items: [], x: 0 };
        if (compact && lines.length >= maxCompactLines) break;
      }
      if (compact && lines.length >= maxCompactLines && line.items.length && line.x + itemW > maxW) break;
      line.items.push({ color: s.color, labelText, w: itemW });
      line.x += itemW;
    }
    if (compact && series.length > line.items.length + lines.reduce((a, l) => a + l.items.length, 0)) {
      const shown = lines.reduce((a, l) => a + l.items.length, 0) + line.items.length;
      const more = `+${series.length - shown}`;
      const mw = ctx.measureText(more).width + 20;
      if (line.x + mw <= maxW || !line.items.length) {
        line.items.push({ color: labelColor, labelText: more, w: mw });
        line.x += mw;
      }
    }
    if (line.items.length) lines.push(line);
    return lines;
  };

  let useCompact = dashLegend;
  let legendLines = buildLegend(useCompact);
  const minPlotH = state.isZoom ? 120 : 72;
  let legendBand = legendLines.length ? legendLines.length * (useCompact ? 15 : 17) + 6 : 0;
  // Prefer compact over overlapping; then drop legend entirely rather than paint over series.
  if (!useCompact && titleH + legendBand + pad.bottom + minPlotH > h) {
    useCompact = true;
    legendLines = buildLegend(true);
    legendBand = legendLines.length ? legendLines.length * 15 + 6 : 0;
  }
  while (legendLines.length && titleH + legendBand + pad.bottom + minPlotH > h) {
    if (legendLines.length > 1) {
      legendLines.pop();
    } else {
      legendLines = []; // hard rule: never overlay legend on the plot
    }
    legendBand = legendLines.length ? legendLines.length * (useCompact ? 15 : 17) + 6 : 0;
  }

  pad.top = titleH + legendBand + (state.title || legendLines.length ? 4 : 8);
  // Final safety: keep min plot height without pulling pad.top under the legend band.
  if (h - pad.top - pad.bottom < minPlotH && legendLines.length) {
    legendLines = [];
    legendBand = 0;
    pad.top = titleH + 8;
  }
  if (h - pad.top - pad.bottom < 40) {
    pad.top = Math.max(titleH + 4, 8);
  }

  const cw = Math.max(40, w - pad.left - pad.right);
  const ch = Math.max(32, h - pad.top - pad.bottom);
  state.dataMin = dMin; state.dataMax = dMax; state._cw = cw; state._ch = ch; state._n = n;
  state._plot = { x: pad.left, y: pad.top, w: cw, h: ch };

  // 仅当存在「现在」之后的预测采样点时才居中拆分；否则不预留未来空白
  let axisT0 = n ? vis[0].timestamp : 0, axisT1 = n ? vis[n - 1].timestamp : 1;
  const zoomed = state.i0 > 0 || state.i1 < (state.all.length - 1);
  if (!state.nowTs && !zoomed && state.axisFrom > 0 && state.axisTo > state.axisFrom) {
    axisT0 = state.axisFrom;
    axisT1 = state.axisTo;
  }
  if (state.nowTs && n >= 2) {
    const nowTs = +state.nowTs;
    const t0 = vis[0].timestamp, t1 = vis[n - 1].timestamp;
    if (nowTs >= t0 && t1 > nowTs + 1) {
      const half = Math.max(nowTs - t0, t1 - nowTs, 1);
      axisT0 = nowTs - half;
      axisT1 = nowTs + half;
    } else {
      state.nowTs = 0; // 无未来预测点时清除中轴，避免右侧空白
    }
  }
  state._axisT0 = axisT0; state._axisT1 = axisT1;
  const xAt = i => {
    if (axisT1 > axisT0 && vis[i]) {
      const ts = +(vis[i].timestamp || 0);
      return pad.left + ((ts - axisT0) / (axisT1 - axisT0)) * cw;
    }
    return pad.left + (n <= 1 ? 0 : (i / (n - 1)) * cw);
  };
  const yAt = v => {
    const y = pad.top + ch - ((v - dMin) / yRange) * ch;
    return Math.max(pad.top, Math.min(pad.top + ch, y));
  };

  // Title band
  if (state.title) {
    ctx.textAlign = "left";
    ctx.fillStyle = txtColor;
    ctx.font = "600 11.5px -apple-system, 'Segoe UI', 'PingFang SC', sans-serif";
    ctx.fillText(state.title, pad.left, 12);
  }

  // Legend band (between title and plot)
  if (legendLines.length) {
    const legendY0 = titleH + 2;
    let legendBgW = 0;
    legendLines.forEach(line => { legendBgW = Math.max(legendBgW, line.x); });
    ctx.fillStyle = legendBg;
    const bgH = legendLines.length * (useCompact ? 15 : 17) + 4;
    if (typeof ctx.roundRect === "function") {
      ctx.beginPath();
      ctx.roundRect(pad.left, legendY0 - 1, Math.min(legendBgW + 8, cw), bgH, 5);
      ctx.fill();
    } else {
      ctx.fillRect(pad.left, legendY0 - 1, Math.min(legendBgW + 8, cw), bgH);
    }
    let ly = legendY0 + 2;
    ctx.font = legFont;
    legendLines.forEach(line => {
      let lx = pad.left + 4;
      line.items.forEach(item => {
        ctx.fillStyle = item.color;
        if (typeof ctx.roundRect === "function") {
          ctx.beginPath(); ctx.roundRect(lx, ly, 9, 9, 2); ctx.fill();
        } else {
          ctx.fillRect(lx, ly, 9, 9);
        }
        ctx.fillStyle = txtColor;
        ctx.textAlign = "left";
        ctx.fillText(item.labelText, lx + 13, ly + 8);
        lx += item.w;
      });
      ly += useCompact ? 15 : 17;
    });
  }

  // Grid + Y labels
  ctx.strokeStyle = gridColor; ctx.lineWidth = 0.5; ctx.setLineDash([2, 4]);
  ctx.font = "10.5px 'SF Mono', 'Cascadia Code', 'JetBrains Mono', Consolas, monospace";
  ctx.textAlign = "right";
  for (let i = 0; i <= 4; i++) {
    const y = pad.top + (ch / 4) * i;
    ctx.beginPath(); ctx.moveTo(pad.left, y); ctx.lineTo(pad.left + cw, y); ctx.stroke();
    const val = dMax - (yRange / 4) * i;
    ctx.fillStyle = labelColor;
    const fmt = series[0] && series[0].fmt;
    ctx.fillText(fmt ? fmt(val) : val.toFixed(1), pad.left - 6, y + 3.5);
  }
  ctx.setLineDash([]);

  // X axis labels (edge ticks inset so they are not clipped by overflow:hidden)
  if (n >= 1) {
    const firstTs = axisT0, span = axisT1 - axisT0;
    ctx.fillStyle = labelColor;
    ctx.font = "10.5px 'SF Mono', 'Cascadia Code', 'JetBrains Mono', Consolas, monospace";
    for (let i = 0; i <= 4; i++) {
      const x = pad.left + (cw / 4) * i;
      const d = new Date((firstTs + (span / 4) * i) * 1000);
      const lab = formatChartXLabel(d, span, firstTs, axisT1);
      ctx.textAlign = i === 0 ? "left" : (i === 4 ? "right" : "center");
      ctx.fillText(lab, x, h - 6);
    }
  }

  // Series clipped strictly to the plot rect — peaks cannot paint into legend/title.
  // Multi-series (load1/5/15, DNS/TCP/…): never area-fill + never Catmull-Rom smooth.
  // Dense 1h/3h samples make gauges nearly coincide; fills/smoothing stack into
  // "1～2 条曲线" 闪烁假象。单序列仍保留面积与轻平滑。
  ctx.save();
  ctx.beginPath();
  ctx.rect(pad.left, pad.top, cw, ch);
  ctx.clip();
  const histSeriesN = series.filter(s => !s.dashed && s.kind !== "forecast"
    && s.kind !== "compare_pop" && s.kind !== "compare_yoy" && !s.compare).length;
  const multiHist = histSeriesN > 1;
  const built = series.map((s, sIdx) => {
    const pts = [];
    vis.forEach((sm, i) => {
      const v = seriesVal(s, sm);
      if (v !== null) pts.push({ x: xAt(i), y: yAt(v), val: v });
    });
    return { s, sIdx, pts };
  });
  // Pass 1: area fill only for single history series (forecast/compare never fill).
  if (!multiHist) {
    built.forEach(({ s, pts }) => {
      if (pts.length < 2) return;
      if (s.dashed || s.kind === "forecast" || s.kind === "compare_pop" || s.kind === "compare_yoy" || s.compare) return;
      const wantSmooth = pts.length > 12;
      const grad = ctx.createLinearGradient(0, pad.top, 0, pad.top + ch);
      grad.addColorStop(0, s.color + "35");
      grad.addColorStop(0.4, s.color + "15");
      grad.addColorStop(0.7, s.color + "06");
      grad.addColorStop(1, s.color + "01");
      ctx.fillStyle = grad;
      const baseY = pad.top + ch;
      ctx.beginPath();
      seriesPathCommands(ctx, pts, wantSmooth);
      ctx.lineTo(pts[pts.length - 1].x, baseY);
      ctx.lineTo(pts[0].x, baseY);
      ctx.closePath();
      ctx.fill();
    });
  }
  // Pass 2: strokes on top so every series stays visible when values nearly overlap.
  built.forEach(({ s, sIdx, pts }) => {
    if (pts.length < 2) return;
    ctx.save();
    ctx.strokeStyle = s.color;
    // Stagger widths so nearly-identical gauges (load1≈load5≈load15) remain separable.
    ctx.lineWidth = multiHist ? Math.max(1.4, 2.4 - sIdx * 0.35) : (sIdx === 0 ? 2.2 : 1.8);
    ctx.lineJoin = "round"; ctx.lineCap = "round";
    if (s.dashed || s.kind === "forecast") ctx.setLineDash([6, 4]);
    else if (s.kind === "compare_pop" || s.kind === "compare_yoy" || s.compare) ctx.setLineDash([2, 3]);
    else if (multiHist && sIdx > 0 && sIdx % 2 === 1) ctx.setLineDash([10, 3]); // alternate slight dash
    else ctx.setLineDash([]);
    // Dense short windows: polyline only — smooth pulls coincident gauges into one ribbon.
    const wantSmooth = !multiHist && pts.length > 12 && pts.length <= 240
      && !s.dashed && s.kind !== "forecast"
      && s.kind !== "compare_pop" && s.kind !== "compare_yoy" && !s.compare;
    ctx.beginPath();
    seriesPathCommands(ctx, pts, wantSmooth);
    ctx.stroke();
    ctx.setLineDash([]);
    ctx.restore();
  });

  // Realtime | forecast boundary (left=历史, right=预测)，中轴居中
  if (state.nowTs && n >= 2 && axisT1 > axisT0) {
    const nowTs = +state.nowTs;
    if (nowTs >= axisT0 && nowTs <= axisT1) {
      const nx = pad.left + ((nowTs - axisT0) / (axisT1 - axisT0)) * cw;
      ctx.fillStyle = "rgba(34,197,94,0.04)";
      ctx.fillRect(pad.left, pad.top, Math.max(0, nx - pad.left), ch);
      ctx.fillStyle = "rgba(99,102,241,0.07)";
      ctx.fillRect(nx, pad.top, Math.max(0, pad.left + cw - nx), ch);
      ctx.strokeStyle = "rgba(239,68,68,0.9)";
      ctx.lineWidth = 1.5;
      ctx.setLineDash([]);
      ctx.beginPath(); ctx.moveTo(nx, pad.top); ctx.lineTo(nx, pad.top + ch); ctx.stroke();
      ctx.fillStyle = txtColor;
      ctx.font = "600 10px -apple-system, 'Segoe UI', sans-serif";
      ctx.textAlign = "left";
      ctx.fillText("现在", nx + 4, pad.top + 12);
    }
  }

  // Box-select + crosshair inside clip
  if (state.drag && state.moved && state.downX !== null && state.curX !== null) {
    const x0 = Math.min(state.downX, state.curX), x1 = Math.max(state.downX, state.curX);
    ctx.fillStyle = "rgba(76,141,255,.12)"; ctx.fillRect(x0, pad.top, x1 - x0, ch);
    ctx.strokeStyle = "rgba(76,141,255,.5)"; ctx.lineWidth = 1; ctx.setLineDash([4, 4]);
    ctx.strokeRect(x0, pad.top, x1 - x0, ch); ctx.setLineDash([]);
  }
  if (state.hover >= state.i0 && state.hover <= state.i1 && !state.drag) {
    const li = state.hover - state.i0, x = xAt(li);
    ctx.strokeStyle = "rgba(200,210,230,.22)"; ctx.lineWidth = 0.8;
    ctx.setLineDash([3, 5]);
    ctx.beginPath(); ctx.moveTo(x, pad.top); ctx.lineTo(x, pad.top + ch); ctx.stroke();
    ctx.setLineDash([]);
    series.forEach(s => {
      const v = seriesVal(s, vis[li]); if (v === null) return;
      const py = yAt(v);
      ctx.fillStyle = s.color + "25"; ctx.beginPath(); ctx.arc(x, py, 8, 0, Math.PI * 2); ctx.fill();
      ctx.fillStyle = s.color; ctx.beginPath(); ctx.arc(x, py, 3.5, 0, Math.PI * 2); ctx.fill();
      ctx.strokeStyle = "#fff"; ctx.lineWidth = 1.5;
      ctx.beginPath(); ctx.arc(x, py, 3.5, 0, Math.PI * 2); ctx.stroke();
    });
  }
  ctx.restore();
}

// attachChartEvents wires pointer interaction once per canvas element; handlers
// read the live state from canvas._chart so a persistent canvas (the zoom modal)
// never accumulates duplicate listeners.
function attachChartEvents(canvas) {
  if (canvas._evt) return;
  canvas._evt = true;
  // Map a pointer's clientX into the chart's CSS-pixel coordinate space (state.W),
  // which is what drawChart / pad.left / _cw work in. Using canvas.width (the
  // dpr-scaled backing buffer) here caused the crosshair to be offset by the
  // devicePixelRatio on HiDPI / zoomed displays — hovering snapped to the wrong point.
  const toX = e => {
    const st = canvas._chart;
    const r = canvas.getBoundingClientRect();
    if (!r.width) return 0;
    const W = (st && st.W) || r.width; // CSS-pixel width the chart was drawn with
    return (e.clientX - r.left) * (W / r.width);
  };
  const localIdx = (st, x) => {
    const n = st._n; if (n <= 1) return 0;
    // 预测居中模式下按时间轴命中最近点
    if (st._axisT1 > st._axisT0 && st._cw > 0) {
      const ts = st._axisT0 + ((x - st.pad.left) / st._cw) * (st._axisT1 - st._axisT0);
      const vis = st.all.slice(st.i0, st.i1 + 1);
      let best = 0, bestD = Infinity;
      for (let i = 0; i < vis.length; i++) {
        const d = Math.abs((vis[i].timestamp || 0) - ts);
        if (d < bestD) { bestD = d; best = i; }
      }
      return best;
    }
    return Math.max(0, Math.min(n - 1, Math.round((x - st.pad.left) / st._cw * (n - 1))));
  };
  canvas.addEventListener("mousemove", e => {
    const st = canvas._chart; if (!st) return;
    const x = toX(e);
    if (st.drag) { st.curX = x; if (Math.abs(x - st.downX) > 4) st.moved = true; }
    const li = localIdx(st, x); st.hover = st.i0 + li;
    drawChart(st); showChartTip(st, e, li);
  });
  canvas.addEventListener("mousedown", e => { const st = canvas._chart; if (!st) return; st.drag = true; st.downX = toX(e); st.curX = st.downX; st.moved = false; });
  canvas.addEventListener("mouseup", e => {
    const st = canvas._chart; if (!st) return;
    if (st.drag && st.moved) {
      const a = localIdx(st, st.downX), b = localIdx(st, toX(e));
      const lo = Math.min(a, b), hi = Math.max(a, b);
      if (hi - lo >= 1) { const base = st.i0; st.i1 = base + hi; st.i0 = base + lo; }
    } else if (st.drag && !st.moved && !st.isZoom) { openChartZoom(st); }
    st.drag = false; st.downX = st.curX = null; st.moved = false; drawChart(st);
  });
  canvas.addEventListener("mouseleave", () => { const st = canvas._chart; if (!st) return; st.hover = -1; st.drag = false; st.moved = false; hideChartTip(); drawChart(st); });
  canvas.addEventListener("dblclick", () => { const st = canvas._chart; if (!st) return; st.i0 = 0; st.i1 = st.all.length - 1; st.hover = -1; hideChartTip(); drawChart(st); });
}

function showChartTip(state, e, li) {
  const vis = state.all.slice(state.i0, state.i1 + 1);
  const sm = vis[li]; if (!sm) { hideChartTip(); return; }
  const d = new Date(sm.timestamp * 1000);
  const time = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")} ${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  const nowTs = +state.nowTs || 0;
  const inFuture = !!(nowTs && sm.timestamp > nowTs + 1);
  let rows = "";
  state.series.forEach(s => {
    const v = seriesVal(s, sm);
    const isFc = s.kind === "forecast" || s.dashed || String(s.key || "").indexOf("fc_") === 0;
    // 未来区不展示历史序列的空值；历史区不展示预测空值；避免整列 "—"
    if (v === null) {
      if (inFuture && !isFc) return;
      if (!inFuture && isFc) return;
      return;
    }
    const txt = s.fmt ? s.fmt(v) : v.toFixed(1);
    rows += `<div class="tip-r"><span class="tip-dot" style="background:${s.color}"></span><span>${esc(s.label)}</span><span class="tip-v">${esc(txt)}</span></div>`;
  });
  if (!rows) { hideChartTip(); return; }
  const t = chartTipEl();
  t.innerHTML = `<div class="tip-t">${time}</div>${rows}`;
  t.style.display = "block";
  const tipW = t.offsetWidth || 180, tipH = t.offsetHeight || 80;
  let px = e.clientX + 14, py = e.clientY + 14;
  if (px + tipW > window.innerWidth - 8) px = e.clientX - tipW - 14;
  if (py + tipH > window.innerHeight - 8) py = e.clientY - tipH - 14;
  if (px < 8) px = 8;
  if (py < 8) py = 8;
  t.style.left = px + "px"; t.style.top = py + "px";
}

// openChartZoom opens the enlarge modal, re-rendering the source chart on a
// larger canvas that keeps the source's current visible window and stays fully
// interactive (hover / box-zoom / dbl-click reset).
let ZOOM_CHART_SRC = null;
/** Reload context for zoom time-range switching (host / check / AI chart). */
let ZOOM_CTX = null; // { hostId, checkId, checkType, checkName, apiBase, mode, metrics, series, yMin, yMax, titleBase, forecastScope, rangeH, custom, canReload }

function zoomMetricField(m) {
  switch (String(m || "").toLowerCase()) {
    case "cpu": return "cpu_percent";
    case "memory": case "mem": return "mem_percent";
    case "disk": return "disk_percent";
    case "load": return "load1";
    case "network": case "net": return "net_rx_mbps";
    case "io": return "disk_read_mbps";
    default: return String(m || "");
  }
}

function estimateZoomRangeHours(src) {
  const base = src && (src._fcBase || src._aiBase);
  const samples = (base && base.samples) || (src && src.all) || [];
  if (samples.length >= 2) {
    const a = +(samples[0].timestamp || samples[0].ts || 0);
    const b = +(samples[samples.length - 1].timestamp || samples[samples.length - 1].ts || 0);
    const h = (b - a) / 3600;
    if (h > 0.2) {
      // snap to nearest preset
      let best = CHART_SPANS[0], bestD = Math.abs(h - best);
      for (const s of CHART_SPANS) {
        const d = Math.abs(h - s);
        if (d < bestD) { best = s; bestD = d; }
      }
      return best;
    }
  }
  if (typeof DETAIL_TIME_RANGE === "number" && DETAIL_TIME_RANGE > 0) return DETAIL_TIME_RANGE;
  if (typeof CHK_HIST !== "undefined" && CHK_HIST && typeof CHK_HIST.range === "number" && CHK_HIST.range > 0 && !CHK_HIST.custom) {
    return CHK_HIST.range;
  }
  return 6;
}

function buildZoomCtxFromSrc(src) {
  const base = src && (src._fcBase || src._aiBase) || {};
  const series = (base.series || src.series || []).filter(s => s && s.kind !== "forecast" && !s.dashed);
  const reload = base.reload || src.reload || null;
  const hostId = (reload && reload.hostId) || (src.forecastScope === "host-detail" ? DETAIL_HOST_ID : "") || "";
  const checkId = (reload && (reload.checkId || reload.id)) || "";
  const mode = (reload && reload.mode) || (hostId && series.some(s => /_percent$|^load\d/.test(String(s.key || ""))) ? "fields" : (reload ? reload.mode : ""));
  const canReload = !!(hostId || (reload && reload.hostId) || checkId || (reload && typeof reload.fetch === "function"));
  return {
    hostId: hostId || (reload && reload.hostId) || "",
    checkId: checkId || "",
    checkType: (reload && (reload.checkType || reload.type)) || "",
    checkName: (reload && (reload.checkName || reload.name)) || "",
    apiBase: (reload && reload.base) || "checks",
    mode: mode || (reload && reload.metrics ? "ai-mapped" : (hostId ? "fields" : (checkId ? "check" : ""))),
    metrics: (reload && reload.metrics) || [],
    series,
    yMin: base.yMin != null ? base.yMin : src.yMin,
    yMax: base.yMax != null ? base.yMax : src.yMax,
    titleBase: String((base.title || src.title || "").replace(/\s*[·•]\s*放大预览\s*$/, "")),
    forecastScope: src.forecastScope || (reload && reload.forecastScope) || "",
    rangeH: estimateZoomRangeHours(src),
    custom: (typeof CHK_HIST !== "undefined" && CHK_HIST && CHK_HIST.custom && checkId && CHK_HIST.id === checkId) ? CHK_HIST.custom : null,
    canReload
  };
}

function renderZoomRangeControls() {
  const box = $("chartZoomRanges");
  if (!box) return;
  if (!ZOOM_CTX) { box.innerHTML = ""; return; }
  const rangeH = ZOOM_CTX.custom ? -1 : ZOOM_CTX.rangeH;
  const can = ZOOM_CTX.canReload;
  box.innerHTML = `
    ${can ? renderChartControls(rangeH, "zoom-range") : `<span class="hint">${I18N.t("ui.zoom_range_local", "当前图仅支持框选缩放（无主机历史可重载）")}</span>`}
    ${can ? `<button type="button" class="chip-btn ${ZOOM_CTX.custom ? "active" : ""}" data-zoom-custom-toggle title="${I18N.t("time.custom_range") || "自定义时间范围"}">${I18N.t("time.custom") || "自定义"}</button>` : ""}
  `;
  const panel = $("chartZoomCustomPanel");
  if (panel) {
    panel.hidden = !(ZOOM_CTX.custom || (panel.dataset.forceOpen === "1"));
    if (ZOOM_CTX.custom) {
      const f = $("chartZoomCustomFrom"), t = $("chartZoomCustomTo");
      if (f) f.value = toLocalDatetimeValue(ZOOM_CTX.custom.from);
      if (t) t.value = toLocalDatetimeValue(ZOOM_CTX.custom.to);
    }
  }
}

function mapHostSamplesForZoom(rawSamples, ctx) {
  const samples = Array.isArray(rawSamples) ? rawSamples : [];
  if (!ctx || ctx.mode !== "ai-mapped" || !ctx.metrics || !ctx.metrics.length) {
    return samples;
  }
  const metrics = ctx.metrics;
  return samples.map(sm => {
    const row = { timestamp: sm.timestamp || sm.ts };
    metrics.forEach((m, i) => {
      const field = zoomMetricField(m);
      let v = sm[field];
      if (v == null && m === "load") v = sm.load1;
      if (v == null || !isFinite(+v)) return;
      row["s" + i] = +v;
    });
    return row;
  });
}

async function fetchZoomHostSamples(hostId, from, to) {
  const r = await fetch(`${API}/hosts/${encodeURIComponent(hostId)}/history?from=${from}&to=${to}`, { credentials: "same-origin" });
  if (!r.ok) throw new Error("历史拉取失败 HTTP " + r.status);
  const j = await r.json();
  const raw = Array.isArray(j) ? j : (Array.isArray(j.samples) ? j.samples : []);
  return typeof alignHistoryGaugeSamples === "function" ? alignHistoryGaugeSamples(raw) : raw;
}

async function fetchZoomCheckSamples(checkId, from, to, apiBase) {
  const sinceMin = Math.max(1, Math.ceil((to - from) / 60));
  const qs = new URLSearchParams({ since_min: String(sinceMin), from: String(from), to: String(to) });
  const base = apiBase || "checks";
  const r = await fetch(`${API}/${base}/${encodeURIComponent(checkId)}/history?${qs}`, { credentials: "same-origin" });
  if (!r.ok) throw new Error("拨测历史拉取失败 HTTP " + r.status);
  const all = await r.json().catch(() => []);
  const pts = (Array.isArray(all) ? all : []).filter(p => {
    const ts = +(p.timestamp || p.ts || 0);
    return ts >= from && ts <= to;
  });
  return pts.map(p => ({
    timestamp: p.timestamp || p.ts,
    latency_ms: p.latency_ms,
    loss_pct: (typeof p.loss_pct === "number" ? p.loss_pct : null),
    ok: p.ok
  }));
}

function resolveZoomWindow() {
  if (!ZOOM_CTX) return null;
  if (ZOOM_CTX.custom) return { from: ZOOM_CTX.custom.from, to: ZOOM_CTX.custom.to };
  const rangeH = ZOOM_CTX.rangeH || 6;
  const spanSec = Math.max(3600, rangeH * 3600);
  const step = chartStepForSpan(spanSec);
  const now = Math.floor(Date.now() / 1000);
  const to = typeof alignUnixFloor === "function" ? alignUnixFloor(now, step) : now;
  return { from: to - spanSec, to };
}

function zoomTitleWithRange(base, from, to) {
  const hours = Math.max(0.1, (to - from) / 3600);
  const label = hours < 24
    ? (`近 ${Math.round(hours * 10) / 10} 小时`)
    : (`近 ${Math.round(hours / 24 * 10) / 10} 天`);
  const clean = String(base || I18N.t("ui.trend", "趋势")).replace(/\s*[（(]近[^）)]*[）)]\s*/g, " ").replace(/\s+/g, " ").trim();
  return `${clean}（${label}） · ${I18N.t("ui.zoom_preview", "放大预览")}`;
}

function syncZoomRangeToCheckHist() {
  if (!ZOOM_CTX || !ZOOM_CTX.checkId || typeof CHK_HIST === "undefined" || !CHK_HIST) return;
  if (CHK_HIST.id !== ZOOM_CTX.checkId) return;
  if (ZOOM_CTX.custom) {
    CHK_HIST.custom = { from: ZOOM_CTX.custom.from, to: ZOOM_CTX.custom.to };
  } else {
    CHK_HIST.custom = null;
    CHK_HIST.range = ZOOM_CTX.rangeH || CHK_HIST.range || 1;
  }
  // 放大预览改时间后，同步底层历史弹窗（若仍打开），避免关闭后看到旧区间
  const mask = typeof $ === "function" ? $("checkHistMask") : null;
  if (mask && mask.classList.contains("show") && typeof loadCheckHistory === "function") {
    clearTimeout(syncZoomRangeToCheckHist._t);
    syncZoomRangeToCheckHist._t = setTimeout(() => {
      try { loadCheckHistory(); } catch (_) {}
    }, 80);
  }
}

async function reloadZoomChartData() {
  if (!ZOOM_CTX || !ZOOM_CTX.canReload) {
    await refreshChartZoomFromSrc();
    return;
  }
  const win = resolveZoomWindow();
  if (!win) return;
  const wrap = $("chartZoomCanvas") && $("chartZoomCanvas").closest(".chart-wrap");
  if (wrap) wrap.classList.add("is-loading");
  try {
    let samples = [];
    let reloadMeta = {
      forecastScope: ZOOM_CTX.forecastScope
    };
    if (ZOOM_CTX.checkId) {
      samples = await fetchZoomCheckSamples(ZOOM_CTX.checkId, win.from, win.to, ZOOM_CTX.apiBase);
      reloadMeta = {
        checkId: ZOOM_CTX.checkId,
        checkType: ZOOM_CTX.checkType,
        checkName: ZOOM_CTX.checkName,
        base: ZOOM_CTX.apiBase || "checks",
        mode: "check",
        forecastScope: ZOOM_CTX.forecastScope || "checks"
      };
      syncZoomRangeToCheckHist();
    } else if (ZOOM_CTX.hostId) {
      const raw = await fetchZoomHostSamples(ZOOM_CTX.hostId, win.from, win.to);
      samples = mapHostSamplesForZoom(raw, ZOOM_CTX);
      reloadMeta = {
        hostId: ZOOM_CTX.hostId,
        mode: ZOOM_CTX.mode,
        metrics: ZOOM_CTX.metrics,
        forecastScope: ZOOM_CTX.forecastScope
      };
    } else {
      await refreshChartZoomFromSrc();
      return;
    }
    if (!samples.length) {
      if (typeof toast === "function") toast(I18N.t("empty.no_history", "暂无历史数据"), "err");
      $("chartZoomTitle").textContent = zoomTitleWithRange(ZOOM_CTX.titleBase || "", win.from, win.to);
      renderZoomRangeControls();
      const z = createChart("chartZoomCanvas", [], [], ZOOM_CTX.yMin, ZOOM_CTX.yMax, {
        title: ZOOM_CTX.titleBase || "", isZoom: true
      });
      DETAIL_CHARTS.__zoom = z;
      return;
    }
    const series = (ZOOM_CTX.series || []).map(s => Object.assign({}, s, { kind: "history", dashed: false }));
    const horizonSec = Math.max(1800, win.to - win.from);
    const titleBase = ZOOM_CTX.titleBase || "";
    const title = zoomTitleWithRange(titleBase, win.from, win.to).replace(/\s*[·•]\s*放大预览\s*$/, "");
    const base = {
      samples, series,
      yMin: ZOOM_CTX.yMin, yMax: ZOOM_CTX.yMax,
      title, horizonSec,
      reload: reloadMeta
    };
    ZOOM_CHART_SRC = {
      all: samples, series, yMin: ZOOM_CTX.yMin, yMax: ZOOM_CTX.yMax,
      title, nowTs: 0, forecastScope: ZOOM_CTX.forecastScope,
      _fcBase: base, _aiBase: base, reload: base.reload
    };
    $("chartZoomTitle").textContent = zoomTitleWithRange(titleBase, win.from, win.to);
    renderZoomRangeControls();
    await refreshChartZoomFromSrc();
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  } finally {
    if (wrap) wrap.classList.remove("is-loading");
  }
}

async function refreshChartZoomFromSrc() {
  const src = ZOOM_CHART_SRC;
  if (!src || !$("chartZoomMask") || !$("chartZoomMask").classList.contains("show")) return;
  const scope = src.forecastScope || (ZOOM_CTX && ZOOM_CTX.forecastScope) || "";
  const fcOn = scope && typeof isChartForecastOn === "function" && isChartForecastOn(scope);
  const base = src._fcBase || src._aiBase || null;
  const samples = (base && base.samples) || src.all;
  let series = (base && base.series) || src.series;
  // 放大预览只用历史序列做 enrich，避免把已有虚线再喂一遍
  series = (series || []).filter(s => s && s.kind !== "forecast" && !s.dashed);
  const yMin = base && base.yMin != null ? base.yMin : src.yMin;
  const yMax = base && base.yMax != null ? base.yMax : src.yMax;
  const title = src.title || "";
  let horizonSec = (base && base.horizonSec) || 0;
  if (!horizonSec && samples && samples.length >= 2) {
    const a = samples[0].timestamp || samples[0].ts || 0;
    const b = samples[samples.length - 1].timestamp || samples[samples.length - 1].ts || 0;
    horizonSec = Math.max(0, Math.round(+b - +a));
  }
  if (fcOn && horizonSec > 0 && horizonSec < 1800) horizonSec = 1800;
  let sm = samples, ser = series, nowTs = 0;
  if (fcOn && typeof enrichSamplesWithForecast === "function" && samples && samples.length >= 4) {
    const en = await enrichSamplesWithForecast(samples, series, {
      forecast: true,
      horizonSec,
      method: typeof getChartForecastModel === "function" ? getChartForecastModel(scope) : "auto",
      forecastScope: scope || ""
    });
    if (en && !en.stale) {
      const sliced = typeof sliceForecastForChart === "function"
        ? sliceForecastForChart(en, series, samples) : en;
      if (sliced && sliced.samples && sliced.samples.length) {
        sm = sliced.samples; ser = sliced.series; nowTs = sliced.nowTs || 0;
      } else if (en.samples) {
        sm = en.samples; ser = en.series; nowTs = en.nowTs || 0;
      }
    }
  } else if (!fcOn) {
    ser = series;
    nowTs = 0;
  }
  const z = createChart("chartZoomCanvas", sm, ser, yMin, yMax, {
    title, isZoom: true, nowTs, forecastScope: scope,
    axisFrom: src.axisFrom || 0, axisTo: src.axisTo || 0,
    _fcBase: base || { samples, series, yMin, yMax, title, horizonSec, reload: src.reload || (base && base.reload) }
  });
  if (z) {
    z.i0 = 0;
    z.i1 = (z.all ? z.all.length : 1) - 1;
    z.reload = src.reload || (base && base.reload) || null;
    drawChart(z);
    DETAIL_CHARTS.__zoom = z;
    ZOOM_CHART_SRC = z;
  }
}
function openChartZoom(src) {
  hideChartTip();
  ZOOM_CHART_SRC = src;
  ZOOM_CTX = buildZoomCtxFromSrc(src);
  $("chartZoomTitle").textContent = (src.title || I18N.t("ui.trend")) + " · " + I18N.t("ui.zoom_preview");
  const tools = $("chartZoomTools");
  if (tools) {
    const scope = (ZOOM_CTX && ZOOM_CTX.forecastScope) || src.forecastScope || "";
    const fc = (scope && typeof forecastChipHTML === "function") ? forecastChipHTML(scope) : "";
    const ai = `<button type="button" class="chip-btn ai-assist-btn" data-zoom-ai title="${I18N.t("hosts.ai_analyze_title", "用 AI 解读当前趋势图")}"><span class="ai-assist-btn-ic">🤖</span>${I18N.t("hosts.ai_analyze", "AI 分析")}</button>`;
    tools.innerHTML = fc + ai;
  }
  renderZoomRangeControls();
  const panel = $("chartZoomCustomPanel");
  if (panel) { panel.hidden = true; panel.dataset.forceOpen = ""; }
  $("chartZoomMask").classList.add("show");
  const scope = (ZOOM_CTX && ZOOM_CTX.forecastScope) || src.forecastScope || "";
  const fcOn = scope && typeof isChartForecastOn === "function" && isChartForecastOn(scope);
  // 可重载时按当前时间窗拉一次，保证与所选范围一致；否则直接画源图
  if (ZOOM_CTX && ZOOM_CTX.canReload) {
    reloadZoomChartData().catch(() => refreshChartZoomFromSrc());
    return;
  }
  if (fcOn) {
    refreshChartZoomFromSrc().catch(() => {
      const z = createChart("chartZoomCanvas", src.all, src.series, src.yMin, src.yMax, {
        title: src.title, isZoom: true, nowTs: src.nowTs || 0,
        forecastScope: scope,
        _fcBase: src._fcBase || src._aiBase || null
      });
      if (z) { z.i0 = 0; z.i1 = (z.all ? z.all.length : 1) - 1; drawChart(z); }
      DETAIL_CHARTS.__zoom = z;
    });
    return;
  }
  const histSeries = (src.series || []).filter(s => s && s.kind !== "forecast" && !s.dashed);
  const base = src._fcBase || src._aiBase;
  const samples = (base && base.samples) || src.all;
  const z = createChart("chartZoomCanvas", samples, histSeries.length ? histSeries : src.series, src.yMin, src.yMax, {
    title: src.title, isZoom: true, nowTs: 0,
    forecastScope: scope,
    axisFrom: src.axisFrom || 0, axisTo: src.axisTo || 0,
    _fcBase: base || null
  });
  if (z) { z.i0 = 0; z.i1 = (z.all ? z.all.length : 1) - 1; drawChart(z); }
  DETAIL_CHARTS.__zoom = z;
}

function analyzeZoomChartAI() {
  if (typeof openAIAssist !== "function") {
    if (typeof toast === "function") toast(I18N.t("assist.unavailable", "AI 面板未就绪"), "err");
    return;
  }
  const src = ZOOM_CHART_SRC;
  if (!src) {
    if (typeof toast === "function") toast(I18N.t("empty.no_history", "暂无历史数据"), "err");
    return;
  }
  const base = src._fcBase || src._aiBase || {};
  const samples = ((base.samples || src.all) || []).filter(Boolean);
  const series = ((ZOOM_CTX && ZOOM_CTX.series) || base.series || src.series || [])
    .filter(s => s && s.kind !== "forecast" && !s.dashed);
  if (!samples.length || !series.length) {
    if (typeof toast === "function") toast(I18N.t("empty.no_history", "暂无历史数据"), "err");
    return;
  }
  const first = samples[0], last = samples[samples.length - 1];
  const t0 = +(first.timestamp || first.ts || 0);
  const t1 = +(last.timestamp || last.ts || 0);
  const title = (ZOOM_CTX && ZOOM_CTX.titleBase) || src.title || I18N.t("ui.trend", "趋势");
  const lines = [
    `图表：${title}`,
    ZOOM_CTX && ZOOM_CTX.checkId ? `拨测：${ZOOM_CTX.checkName || ZOOM_CTX.checkId}（id=${ZOOM_CTX.checkId}，类型=${ZOOM_CTX.checkType || "?"}）` : "",
    ZOOM_CTX && ZOOM_CTX.hostId ? `主机：${(typeof HostPicker !== "undefined" && HostPicker.displayHost) ? HostPicker.displayHost(ZOOM_CTX.hostId) : (ZOOM_CTX.titleBase || "未知主机")}` : "",
    `样本数：${samples.length}，时间跨度：约 ${((t1 - t0) / 3600).toFixed(2)} 小时（${t0 ? new Date(t0 * 1000).toLocaleString() : "?"} → ${t1 ? new Date(t1 * 1000).toLocaleString() : "?"}）`,
  ].filter(Boolean);
  series.forEach(s => {
    const key = s.key;
    let sum = 0, n = 0, peak = -Infinity, cur = null;
    samples.forEach(sm => {
      let v;
      try { v = typeof seriesVal === "function" ? seriesVal(s, sm) : sm[key]; } catch (_) { v = null; }
      if (v == null || !isFinite(+v)) return;
      v = +v;
      sum += v; n++;
      if (v > peak) peak = v;
      cur = v;
    });
    if (!n) {
      lines.push(`${s.label || key}：无有效采样`);
      return;
    }
    const fmt = typeof s.fmt === "function" ? s.fmt : (v => String(Math.round(v * 100) / 100));
    lines.push(`${s.label || key}：均值 ${fmt(sum / n)} · 峰值 ${fmt(peak)} · 当前 ${fmt(cur)}`);
  });
  if (ZOOM_CTX && ZOOM_CTX.checkId) {
    const okN = samples.filter(sm => sm.ok === true).length;
    if (samples.some(sm => typeof sm.ok === "boolean")) {
      lines.push(`可用率：${(okN / samples.length * 100).toFixed(1)}%（成功 ${okN}/${samples.length}）`);
    }
  }
  lines.push("", "请基于以上趋势做根因研判与处置建议；关注尖峰、突降、持续抬升与可用率变化。");
  openAIAssist({
    task: "chart_analysis",
    mode: "analyze",
    title: "AI · 趋势诊断 · " + title,
    context: lines.join("\n").slice(0, 12000),
    hint: "正在解读当前放大预览中的趋势…"
  });
}
document.addEventListener("chart-forecast-toggle", (ev) => {
  if (!ev.detail || !ZOOM_CHART_SRC) return;
  const scope = ZOOM_CHART_SRC.forecastScope || (ZOOM_CTX && ZOOM_CTX.forecastScope) || "";
  if (!scope || ev.detail.scope !== scope) return;
  const mask = $("chartZoomMask");
  if (!mask || !mask.classList.contains("show")) return;
  refreshChartZoomFromSrc().catch(() => {});
});

// Zoom modal: time-range chips + custom range + AI
safeAddEventListener("chartZoomMask", "click", (e) => {
  if (e.target.closest && e.target.closest("[data-zoom-ai]")) {
    e.preventDefault();
    e.stopPropagation();
    analyzeZoomChartAI();
    return;
  }
  if (!ZOOM_CTX) return;
  const rangeBtn = e.target.closest && e.target.closest("[data-zoom-range]");
  if (rangeBtn) {
    e.preventDefault();
    e.stopPropagation();
    const h = parseInt(rangeBtn.getAttribute("data-zoom-range"), 10);
    if (!Number.isFinite(h) || h <= 0) return;
    ZOOM_CTX.custom = null;
    ZOOM_CTX.rangeH = h;
    const panel = $("chartZoomCustomPanel");
    if (panel) { panel.hidden = true; panel.dataset.forceOpen = ""; }
    renderZoomRangeControls();
    reloadZoomChartData();
    return;
  }
  const tog = e.target.closest && e.target.closest("[data-zoom-custom-toggle]");
  if (tog) {
    e.preventDefault();
    e.stopPropagation();
    const panel = $("chartZoomCustomPanel");
    if (!panel) return;
    const open = panel.hidden;
    panel.hidden = !open;
    panel.dataset.forceOpen = open ? "1" : "";
    if (open) {
      const win = resolveZoomWindow() || { from: Math.floor(Date.now() / 1000) - 3600, to: Math.floor(Date.now() / 1000) };
      const f = $("chartZoomCustomFrom"), t = $("chartZoomCustomTo");
      if (f) f.value = toLocalDatetimeValue(win.from);
      if (t) t.value = toLocalDatetimeValue(win.to);
      if (f) f.focus();
    }
    return;
  }
});
safeAddEventListener("chartZoomCustomApply", "click", (e) => {
  e.preventDefault();
  e.stopPropagation();
  if (!ZOOM_CTX) return;
  applyCustomRangeFromInputs($("chartZoomCustomFrom"), $("chartZoomCustomTo"), (from, to) => {
    ZOOM_CTX.custom = { from, to };
    ZOOM_CTX.rangeH = Math.max(1, Math.round((to - from) / 3600));
    renderZoomRangeControls();
    reloadZoomChartData();
  });
});
function sparkBlock(title, series, color) {
  const last = series.length ? series[series.length - 1] : 0;
  return `<div class="field"><label>${title} · 当前 ${(last || 0).toFixed(1)}</label>
    <div class="spark">${sparkline(series, color)}</div></div>`;
}
function sparkline(series, color) {
  const w = 500, h = 46, n = series.length, max = 100;
  if (n < 2) return `<svg class="sparkline" viewBox="0 0 ${w} ${h}"></svg>`;
  const pts = series.map((v, i) => {
    const x = i / (n - 1) * w;
    const y = h - 2 - (Math.max(0, Math.min(v || 0, max)) / max) * (h - 4);
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(" ");
  const gid = "g" + Math.random().toString(36).slice(2, 7);
  return `<svg class="sparkline" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none">
    <defs><linearGradient id="${gid}" x1="0" x2="0" y1="0" y2="1">
      <stop offset="0" stop-color="${color}" stop-opacity=".35"/><stop offset="1" stop-color="${color}" stop-opacity="0"/>
    </linearGradient></defs>
    <polygon points="0,${h} ${pts} ${w},${h}" fill="url(#${gid})"/>
    <polyline points="${pts}" fill="none" stroke="${color}" stroke-width="1.6"/></svg>`;
}
