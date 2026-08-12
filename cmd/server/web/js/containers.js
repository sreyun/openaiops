/* containers.js — 主机 Docker/Podman 容器（商用资源台账） */
(function () {
"use strict";

let CT_ITEMS = [];
const CT_FILTER = { q: "", host: "", state: "all", compose: "" }; // all | running | stopped | other
const CT_PAGE = { limit: 50, offset: 0, total: 0 };
let CT_PROJECTS = [];
let CT_SELECTED = null; // { host_id, id, name, ... }
let CT_BOUND = false;
let CT_FILTER_TIMER = null;

const ctT = (k, fb) => I18N.t(k, fb);
function ctEsc(s) { return typeof esc === "function" ? esc(String(s ?? "")) : String(s ?? ""); }

function ctNormState(c) {
  const raw = String((c && (c.state || c.status)) || "").toLowerCase();
  if (/^up\b|running|healthy/.test(raw)) return "running";
  if (/exited|stopped|dead|created|removing|not running/.test(raw)) return "stopped";
  if (/paused|restarting|removing/.test(raw)) return "other";
  if (raw.includes("running") || raw.includes("up")) return "running";
  if (!raw) return "other";
  return "other";
}
function ctStateLabel(key, fallback) {
  const m = {
    running: ctT("containers.st_running", "运行中"),
    stopped: ctT("containers.st_stopped", "已停止"),
    other: ctT("containers.st_other", "其他"),
  };
  return m[key] || fallback || key || "—";
}
function ctStateBadge(c) {
  const key = ctNormState(c);
  const cls = { running: "ok", stopped: "warn", other: "info" }[key] || "info";
  const tip = c.state || c.status || key;
  return `<span class="badge ${cls}" title="${ctEsc(tip)}">${ctEsc(ctStateLabel(key, tip))}</span>`;
}

/** Parse docker/podman Ports into compact chips. */
function ctPortChips(ports) {
  const raw = String(ports || "").trim();
  if (!raw) return `<span class="muted">—</span>`;
  const parts = raw.split(/,\s*/).map(s => s.trim()).filter(Boolean);
  const seen = new Set();
  const chips = [];
  parts.forEach(p => {
    // 0.0.0.0:8529->8529/tcp  or  8529/tcp  or  [::]:443->443/tcp
    let label = p;
    const m = p.match(/:(\d+)->(\d+)\/(\w+)/) || p.match(/^(\d+)\/(\w+)$/);
    if (m) {
      const hostPort = m[1];
      const proto = (m[3] || m[2] || "tcp").toLowerCase();
      label = `${hostPort}/${proto}`;
    }
    if (seen.has(label)) return;
    seen.add(label);
    chips.push(label);
  });
  if (!chips.length) return `<span class="mono muted ct-ports-raw" title="${ctEsc(raw)}">${ctEsc(raw.slice(0, 48))}${raw.length > 48 ? "…" : ""}</span>`;
  const show = chips.slice(0, 4);
  let html = `<div class="ct-port-chips" title="${ctEsc(raw)}">`;
  show.forEach(c => { html += `<span class="ct-port-chip">${ctEsc(c)}</span>`; });
  if (chips.length > 4) html += `<span class="ct-port-chip more">+${chips.length - 4}</span>`;
  html += `</div>`;
  return html;
}

function ctFlatRows() {
  return (CT_ITEMS || []).map(c => ({
    host_id: c.host_id || "",
    host_name: c.host_name || c.host_id || "",
    runtime: c.runtime || "",
    id: c.id,
    name: c.name || c.id,
    image: c.image || "",
    state: c.state || "",
    status: c.status || "",
    ports: c.ports || "",
    created: c.created || "",
    compose_project: c.compose_project || "",
    compose_service: c.compose_service || "",
    state_key: ctNormState(c),
    raw: c,
  }));
}

function ctVisible(rows) {
  return rows.slice().sort((a, b) => {
    const rank = k => ({ stopped: 0, other: 1, running: 2 }[k] ?? 3);
    return rank(a.state_key) - rank(b.state_key) || String(a.name).localeCompare(String(b.name));
  });
}

function ctHostOptions() {
  const map = new Map();
  const add = (id, name) => {
    id = String(id || "").trim();
    if (!id || map.has(id)) return;
    map.set(id, String(name || id));
  };
  try {
    const hosts = (typeof LAST_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS) && LAST_HOSTS.length)
      ? LAST_HOSTS
      : (Array.isArray(window._cachedHosts) ? window._cachedHosts : []);
    hosts.forEach(h => add(h && h.id, (h && (h.hostname || h.name)) || ""));
  } catch (_) {}
  (CT_ITEMS || []).forEach(c => {
    add(c.host_id, c.host_name || c.host_id);
  });
  if (CT_FILTER.host) add(CT_FILTER.host, CT_FILTER.host);
  return [...map.entries()];
}

function ctStats(rows) {
  let running = 0, stopped = 0, other = 0;
  rows.forEach(r => {
    if (r.state_key === "running") running++;
    else if (r.state_key === "stopped") stopped++;
    else other++;
  });
  return { total: rows.length, running, stopped, other, hosts: ctHostOptions().length };
}

function ctRefocus(id) {
  if (!id) return;
  const el = $(id);
  if (!el) return;
  try {
    el.focus();
    if (typeof el.setSelectionRange === "function") el.setSelectionRange(el.value.length, el.value.length);
  } catch (_) {}
}

async function loadContainersPanel(opts) {
  opts = opts || {};
  const panel = $("containersPanel");
  if (!panel) return;
  if (!opts.keepPanel) panel.innerHTML = `<div class="ct-shell"><div class="loading-dots">${ctEsc(ctT("ui.loading", "加载中…"))}</div></div>`;
  try {
    const limit = CT_PAGE.limit || 50;
    const offset = CT_PAGE.offset || 0;
    const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
    if (CT_FILTER.host) params.set("host", CT_FILTER.host);
    if (CT_FILTER.state && CT_FILTER.state !== "all") params.set("status", CT_FILTER.state);
    if (CT_FILTER.q) params.set("q", CT_FILTER.q);
    if (CT_FILTER.compose) params.set("compose_project", CT_FILTER.compose);
    const r = await fetch(`${API}/containers/list?${params}`, { credentials: "same-origin" });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
    CT_ITEMS = Array.isArray(j.items) ? j.items : [];
    CT_PROJECTS = Array.isArray(j.compose_projects) ? j.compose_projects.slice() : [];
    if (CT_FILTER.compose && !CT_PROJECTS.includes(CT_FILTER.compose)) CT_PROJECTS.push(CT_FILTER.compose);
    CT_PROJECTS.sort((a, b) => String(a).localeCompare(String(b)));
    CT_PAGE.total = Number.isFinite(Number(j.total)) ? Number(j.total) : CT_ITEMS.length;
    CT_PAGE.limit = Number.isFinite(Number(j.limit)) && Number(j.limit) > 0 ? Number(j.limit) : limit;
    CT_PAGE.offset = Number.isFinite(Number(j.offset)) && Number(j.offset) >= 0 ? Number(j.offset) : offset;
    if (CT_PAGE.total > 0 && CT_PAGE.offset >= CT_PAGE.total) {
      const lim = CT_PAGE.limit || 50;
      CT_PAGE.offset = Math.max(0, Math.floor((CT_PAGE.total - 1) / lim) * lim);
      return loadContainersPanel(opts);
    }
    renderContainersPanel();
    ctRefocus(opts.focusId);
  } catch (e) {
    panel.innerHTML = `<div class="empty-state"><h4>${ctEsc(ctT("containers.load_failed", "加载失败"))}</h4><p>${ctEsc(e.message || e)}</p></div>`;
  }
}

function ctResetAndLoad(opts) {
  opts = opts || {};
  CT_PAGE.offset = 0;
  if (opts.debounce) {
    if (CT_FILTER_TIMER) clearTimeout(CT_FILTER_TIMER);
    CT_FILTER_TIMER = setTimeout(() => {
      CT_FILTER_TIMER = null;
      loadContainersPanel(opts);
    }, opts.debounce);
    return;
  }
  loadContainersPanel(opts);
}

function ctPagerHTML() {
  const total = CT_PAGE.total || 0;
  const limit = CT_PAGE.limit || 50;
  const offset = Math.min(CT_PAGE.offset || 0, total);
  const start = total ? offset + 1 : 0;
  const end = Math.min(offset + limit, total);
  if (!total) return "";
  const prevDisabled = offset <= 0 ? " disabled" : "";
  const nextDisabled = offset + limit >= total ? " disabled" : "";
  const pages = Math.max(1, Math.ceil(total / limit));
  const page = Math.floor(offset / limit) + 1;
  return `<div class="rtx-toolbar ct-pager" style="justify-content:flex-end;margin-top:10px;gap:8px;align-items:center">
    <span class="pinfo mono">${ctEsc(ctT("containers.pager_range", "第 {start}–{end} 条 / 共 {total} 条")
      .replace("{start}", String(start)).replace("{end}", String(end)).replace("{total}", String(total)))}</span>
    <span class="pinfo mono">${ctEsc(ctT("containers.pager_page", "第 {page}/{pages} 页")
      .replace("{page}", String(page)).replace("{pages}", String(pages)))}</span>
    <button type="button" class="btn sm" data-ct-page="prev"${prevDisabled}>${ctEsc(ctT("ui.prev", "上一页"))}</button>
    <button type="button" class="btn sm" data-ct-page="next"${nextDisabled}>${ctEsc(ctT("ui.next", "下一页"))}</button>
  </div>`;
}

function renderContainersPanel() {
  const panel = $("containersPanel");
  if (!panel) return;
  const all = ctFlatRows();
  const visible = ctVisible(all);
  const pageStats = ctStats(visible);
  const allowWrite = typeof canWrite === "function" && canWrite();
  const hostOpts = ctHostOptions();

  let html = `<div class="ct-shell">`;
  html += `<div class="sec-stat-row">
    <div class="sec-stat"><div class="sec-stat-n">${CT_PAGE.total || 0}</div><div class="sec-stat-l">${ctEsc(ctT("containers.stat_total", "容器总数"))}</div></div>
    <div class="sec-stat"><div class="sec-stat-n" style="color:var(--ok)">${pageStats.running}</div><div class="sec-stat-l">${ctEsc(ctT("containers.st_running_page", "本页运行"))}</div></div>
    <div class="sec-stat"><div class="sec-stat-n high">${pageStats.stopped}</div><div class="sec-stat-l">${ctEsc(ctT("containers.st_stopped_page", "本页停止"))}</div></div>
    <div class="sec-stat"><div class="sec-stat-n">${hostOpts.length}</div><div class="sec-stat-l">${ctEsc(ctT("containers.stat_hosts", "主机数"))}</div></div>
  </div>`;

  html += `<div class="rtx-toolbar ct-toolbar">
    <input type="search" id="ctSearch" class="hw-search" placeholder="${ctEsc(ctT("containers.search_ph", "搜索名称 / 镜像 / 主机 / 端口…"))}" value="${ctEsc(CT_FILTER.q)}" autocomplete="off">
    <div class="select-wrap"><select id="ctHostFilter">
      <option value="">${ctEsc(ctT("containers.filter_all_hosts", "全部主机"))}</option>
      ${hostOpts.map(([id, name]) => `<option value="${ctEsc(id)}"${CT_FILTER.host === id ? " selected" : ""}>${ctEsc(name)}</option>`).join("")}
    </select></div>
    <div class="select-wrap"><select id="ctStateFilter">
      <option value="all"${CT_FILTER.state === "all" ? " selected" : ""}>${ctEsc(ctT("containers.filter_all_state", "全部状态"))}</option>
      <option value="running"${CT_FILTER.state === "running" ? " selected" : ""}>${ctEsc(ctT("containers.st_running", "运行中"))}</option>
      <option value="stopped"${CT_FILTER.state === "stopped" ? " selected" : ""}>${ctEsc(ctT("containers.st_stopped", "已停止"))}</option>
      <option value="other"${CT_FILTER.state === "other" ? " selected" : ""}>${ctEsc(ctT("containers.st_other", "其他"))}</option>
    </select></div>
    <div class="select-wrap"><select id="ctComposeFilter">
      <option value="">${ctEsc(ctT("containers.filter_all_compose", "全部 Compose 项目"))}</option>
      ${CT_PROJECTS.map(p => `<option value="${ctEsc(p)}"${CT_FILTER.compose === p ? " selected" : ""}>${ctEsc(p)}</option>`).join("")}
    </select></div>
    <span class="rtx-count tag">${ctEsc(ctT("containers.page_count", "本页"))} ${visible.length} / ${CT_PAGE.total || 0}</span>
    <button type="button" class="btn sm" id="ctRefreshBtn" style="margin-left:auto">${ctEsc(ctT("ui.refresh", "刷新"))}</button>
  </div>`;

  html += `<div class="ct-layout">
    <div class="ct-main cfg-panel ct-card">`;

  if (!all.length && !(CT_FILTER.host || CT_FILTER.q || CT_FILTER.compose || CT_FILTER.state !== "all")) {
    html += `<div class="empty-state"><h4>${ctEsc(ctT("containers.empty_title", "暂无容器数据"))}</h4>
      <p>${ctEsc(ctT("containers.empty", "请确认主机已安装 Docker/Podman，并更新 Agent 后刷新。"))}</p></div>`;
  } else if (!visible.length) {
    html += `<div class="empty-state"><h4>${ctEsc(ctT("containers.no_match", "无匹配结果"))}</h4>
      <p>${ctEsc(ctT("containers.no_match_hint", "试试清空搜索，或切换主机/状态/Compose 筛选。"))}</p></div>`;
  } else {
    html += `<div class="nf-table-wrap ct-table-wrap"><table class="data-table ct-table"><thead><tr>
      <th>${ctEsc(ctT("containers.col_host", "主机"))}</th>
      <th>${ctEsc(ctT("containers.col_name", "名称"))}</th>
      <th>${ctEsc(ctT("containers.col_compose", "Compose"))}</th>
      <th>${ctEsc(ctT("containers.col_image", "镜像"))}</th>
      <th style="width:96px">${ctEsc(ctT("containers.col_state", "状态"))}</th>
      <th>${ctEsc(ctT("containers.col_ports", "端口"))}</th>
      <th style="width:168px">${ctEsc(ctT("containers.col_actions", "操作"))}</th>
    </tr></thead><tbody>`;
    visible.forEach(r => {
      const active = CT_SELECTED && CT_SELECTED.host_id === r.host_id && CT_SELECTED.id === r.id ? " active-row" : "";
      const acts = [];
      const composeCell = r.compose_project
        ? `<div class="mono">${ctEsc(r.compose_project)}</div>${r.compose_service ? `<div class="muted" style="font-size:11px">${ctEsc(r.compose_service)}</div>` : ""}`
        : `<span class="muted">—</span>`;
      if (allowWrite) {
        if (r.state_key !== "running") {
          acts.push(`<button type="button" class="btn sm primary" data-ct-act="start" data-host="${ctEsc(r.host_id)}" data-id="${ctEsc(r.id)}" data-name="${ctEsc(r.name)}">${ctEsc(ctT("containers.act_start", "启动"))}</button>`);
        }
        if (r.state_key === "running") {
          acts.push(`<button type="button" class="btn sm danger" data-ct-act="stop" data-host="${ctEsc(r.host_id)}" data-id="${ctEsc(r.id)}" data-name="${ctEsc(r.name)}">${ctEsc(ctT("containers.act_stop", "停止"))}</button>`);
          acts.push(`<button type="button" class="btn sm" data-ct-act="restart" data-host="${ctEsc(r.host_id)}" data-id="${ctEsc(r.id)}" data-name="${ctEsc(r.name)}">${ctEsc(ctT("containers.act_restart", "重启"))}</button>`);
        }
      }
      acts.push(`<button type="button" class="btn sm" data-ct-log="1" data-host="${ctEsc(r.host_id)}" data-id="${ctEsc(r.id)}" data-name="${ctEsc(r.name)}">${ctEsc(ctT("containers.logs", "日志"))}</button>`);
      html += `<tr class="ct-row${active}" data-ct-row="${ctEsc(r.host_id)}|${ctEsc(r.id)}">
        <td><div class="ct-host">${ctEsc(r.host_name)}</div>${r.runtime ? `<div class="mono muted ct-runtime">${ctEsc(r.runtime)}</div>` : ""}</td>
        <td><div class="ct-name mono" title="${ctEsc(r.id)}">${ctEsc(r.name)}</div></td>
        <td>${composeCell}</td>
        <td><div class="ct-image mono" title="${ctEsc(r.image)}">${ctEsc(r.image || "—")}</div></td>
        <td>${ctStateBadge(r)}</td>
        <td>${ctPortChips(r.ports)}</td>
        <td><div class="ct-actions">${acts.join("")}</div></td>
      </tr>`;
    });
    html += `</tbody></table></div>`;
  }
  html += ctPagerHTML();
  html += `</div>
    <div class="ct-side"><div id="ctDetail" class="cfg-panel ct-card ct-detail"></div></div>
  </div>
  <div class="cfg-panel ct-card" style="margin-top:12px" id="ctComposePanel">
    <div class="cfg-panel-head"><div>
      <div class="cfg-panel-title">${ctEsc(ctT("containers.compose_title", "Compose 编排"))}</div>
      <p class="cfg-panel-desc">${ctEsc(ctT("containers.compose_hint", "选择主机后列出 compose 项目，可 up/down/ps/logs（需主机已安装 docker compose）。"))}</p>
    </div>
      <button type="button" class="btn sm" id="ctComposeRefresh">${ctEsc(ctT("ui.refresh", "刷新"))}</button>
    </div>
    <div class="rtx-toolbar" style="margin-bottom:8px">
      <div class="select-wrap"><select id="ctComposeHost">
        <option value="">${ctEsc(ctT("containers.compose_pick_host", "选择主机…"))}</option>
        ${hostOpts.map(([id, name]) => `<option value="${ctEsc(id)}">${ctEsc(name)}</option>`).join("")}
      </select></div>
      <input type="text" id="ctComposeProject" class="hw-search" placeholder="${ctEsc(ctT("containers.compose_project_ph", "项目名（可选）"))}" style="max-width:180px">
      <input type="text" id="ctComposeFile" class="hw-search" placeholder="${ctEsc(ctT("containers.compose_file_ph", "绝对路径 compose.yml（可选）"))}" style="max-width:260px">
      ${allowWrite ? `<button type="button" class="btn sm primary" data-ct-compose="up">up -d</button>
      <button type="button" class="btn sm danger" data-ct-compose="down">down</button>
      <button type="button" class="btn sm" data-ct-compose="ps">ps</button>
      <button type="button" class="btn sm" data-ct-compose="logs">logs</button>
      <button type="button" class="btn sm" data-ct-compose="pull">pull</button>` : ""}
    </div>
    <pre id="ctComposeOut" class="mono" style="min-height:80px;max-height:280px;overflow:auto;padding:10px;border:1px solid var(--line);border-radius:8px;background:var(--panel2);font-size:12px;white-space:pre-wrap">${ctEsc(ctT("containers.compose_empty", "选择主机并刷新以查看项目列表"))}</pre>
  </div></div>`;

  panel.innerHTML = html;
  ctWire(panel);
  ctPaintDetail(CT_SELECTED && visible.find(r => r.host_id === CT_SELECTED.host_id && r.id === CT_SELECTED.id) || null);
  ctWireCompose();
}

function ctCloseLog() {
  const mask = $("ctLogMask");
  if (mask) mask.classList.remove("show");
  const body = $("ctLogBody");
  if (body) body.textContent = "";
}

async function ctOpenLog(host, id, name) {
  const title = $("ctLogTitle");
  const body = $("ctLogBody");
  const mask = $("ctLogMask");
  if (!mask) return;
  if (title) title.textContent = `${ctT("containers.logs", "日志")} · ${name || id}`;
  if (body) body.textContent = ctT("ui.loading", "加载中…");
  mask.classList.add("show");
  try {
    const r = await fetch(`${API}/containers/${encodeURIComponent(host)}/${encodeURIComponent(id)}/logs?tail=300`, { credentials: "same-origin" });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
    if (body) body.textContent = j.log || "(empty)";
  } catch (e) {
    if (body) body.textContent = String(e.message || e);
  }
}

function ctPaintDetail(row) {
  const box = $("ctDetail");
  if (!box) return;
  if (!row) {
    box.innerHTML = `<div class="ct-detail-empty">
      <h4>${ctEsc(ctT("containers.pick_title", "选择容器查看详情"))}</h4>
      <p>${ctEsc(ctT("containers.pick_hint", "在左侧列表点击一行，查看镜像、端口与运行状态；可用工具栏搜索与筛选。"))}</p>
    </div>`;
    return;
  }
  CT_SELECTED = row;
  const allowWrite = typeof canWrite === "function" && canWrite();
  box.innerHTML = `<div class="cfg-panel-head"><div>
      <div class="cfg-panel-title">${ctEsc(row.name)}</div>
      <p class="cfg-panel-desc mono muted">${ctEsc(row.host_name)} · ${ctEsc(row.id.slice(0, 12))}</p>
    </div>${ctStateBadge(row)}</div>
    <div class="ct-actions" style="margin:8px 0 12px;display:flex;flex-wrap:wrap;gap:6px">
      <button type="button" class="btn sm" data-ct-log="1" data-host="${ctEsc(row.host_id)}" data-id="${ctEsc(row.id)}" data-name="${ctEsc(row.name)}">${ctEsc(ctT("containers.logs", "日志"))}</button>
      ${allowWrite ? `<button type="button" class="btn sm primary" data-ct-tty="1" data-host="${ctEsc(row.host_id)}" data-id="${ctEsc(row.id)}" data-name="${ctEsc(row.name)}" data-hostname="${ctEsc(row.host_name)}">${ctEsc(ctT("containers.tty", "交互终端"))}</button>
      <button type="button" class="btn sm" data-ct-exec="1" data-host="${ctEsc(row.host_id)}" data-id="${ctEsc(row.id)}" data-name="${ctEsc(row.name)}">${ctEsc(ctT("containers.exec", "短命令"))}</button>` : ""}
      <button type="button" class="btn sm" data-ct-hostterm="${ctEsc(row.host_id)}" data-ct-hostname="${ctEsc(row.host_name)}">${ctEsc(ctT("containers.host_term", "宿主机终端"))}</button>
      <button type="button" class="btn sm ai-assist-btn" data-ct-ai="1"><span class="ai-assist-btn-ic">🤖</span>${ctEsc(ctT("ai.analyze", "AI 分析"))}</button>
    </div>
    <div class="ct-kv">
      <div><span>${ctEsc(ctT("containers.col_image", "镜像"))}</span><code class="mono">${ctEsc(row.image || "—")}</code></div>
      <div><span>${ctEsc(ctT("containers.col_host", "主机"))}</span><code class="mono">${ctEsc(row.host_name)}</code></div>
      <div><span>${ctEsc(ctT("containers.col_compose", "Compose"))}</span><code class="mono">${ctEsc(row.compose_project || "—")}${row.compose_service ? " / " + ctEsc(row.compose_service) : ""}</code></div>
      <div><span>${ctEsc(ctT("containers.col_state", "状态"))}</span>${ctStateBadge(row)} <span class="muted mono">${ctEsc(row.status || row.state || "")}</span></div>
      <div><span>${ctEsc(ctT("containers.runtime", "运行时"))}</span><code class="mono">${ctEsc(row.runtime || "—")}</code></div>
      <div><span>${ctEsc(ctT("containers.created", "创建时间"))}</span><code class="mono">${ctEsc(row.created || "—")}</code></div>
      <div><span>${ctEsc(ctT("containers.col_ports", "端口映射"))}</span><div class="ct-ports-full mono">${ctEsc(row.ports || "—")}</div></div>
    </div>
    <pre id="ctExecOut" class="mono" style="display:none;margin-top:10px;max-height:220px;overflow:auto;padding:10px;border:1px solid var(--line);border-radius:8px;background:var(--panel2);font-size:12px"></pre>`;
}

function ctWire(panel) {
  const search = $("ctSearch");
  if (search) {
    search.addEventListener("input", () => {
      CT_FILTER.q = search.value || "";
      ctResetAndLoad({ keepPanel: true, focusId: "ctSearch", debounce: 220 });
    });
  }
  const hostSel = $("ctHostFilter");
  if (hostSel) hostSel.addEventListener("change", () => { CT_FILTER.host = hostSel.value || ""; ctResetAndLoad({ keepPanel: true }); });
  const stateSel = $("ctStateFilter");
  if (stateSel) stateSel.addEventListener("change", () => { CT_FILTER.state = stateSel.value || "all"; ctResetAndLoad({ keepPanel: true }); });
  const composeSel = $("ctComposeFilter");
  if (composeSel) composeSel.addEventListener("change", () => { CT_FILTER.compose = composeSel.value || ""; ctResetAndLoad({ keepPanel: true }); });
  $("ctRefreshBtn")?.addEventListener("click", () => loadContainersPanel());

  // 日志弹层在 panel 外静态挂载：本地再绑一次，避免依赖全局委托时序
  const mask = $("ctLogMask");
  if (mask && !mask._ctCloseBound) {
    mask._ctCloseBound = true;
    mask.addEventListener("click", e => {
      const t = e.target;
      if (!(t instanceof Element)) return;
      if (t === mask || t.closest("[data-close-btn]")) {
        e.preventDefault();
        e.stopPropagation();
        ctCloseLog();
      }
    });
  }

  if (!CT_BOUND) {
    CT_BOUND = true;
    document.addEventListener("click", ctOnClick);
    document.addEventListener("keydown", e => {
      if (e.key !== "Escape") return;
      const m = $("ctLogMask");
      if (m && m.classList.contains("show")) ctCloseLog();
    });
  }
}

async function ctOnClick(ev) {
  const panel = $("containersPanel");
  if (!panel || !document.querySelector("#view-containers.active")) return;
  const t = ev.target;
  if (!(t instanceof Element)) return;

  // 日志遮罩在 panel 外：关闭按钮/遮罩空白由 mask 自己的监听或全局委托处理
  const mask = $("ctLogMask");
  if (mask && mask.contains(t)) {
    if (t === mask || t.closest("[data-close-btn]")) {
      ev.preventDefault();
      ev.stopPropagation();
      ctCloseLog();
    }
    return;
  }
  if (!panel.contains(t)) return;

  const pageBtn = t.closest("[data-ct-page]");
  if (pageBtn) {
    ev.preventDefault();
    ev.stopPropagation();
    const dir = pageBtn.getAttribute("data-ct-page");
    const limit = CT_PAGE.limit || 50;
    if (dir === "prev") CT_PAGE.offset = Math.max(0, (CT_PAGE.offset || 0) - limit);
    if (dir === "next" && (CT_PAGE.offset || 0) + limit < (CT_PAGE.total || 0)) CT_PAGE.offset = (CT_PAGE.offset || 0) + limit;
    loadContainersPanel();
    return;
  }

  const actBtn = t.closest("[data-ct-act]");
  if (actBtn) {
    ev.preventDefault();
    ev.stopPropagation();
    const act = actBtn.getAttribute("data-ct-act");
    const host = actBtn.getAttribute("data-host");
    const id = actBtn.getAttribute("data-id");
    const name = actBtn.getAttribute("data-name") || id;
    const actLab = { start: ctT("containers.act_start", "启动"), stop: ctT("containers.act_stop", "停止"), restart: ctT("containers.act_restart", "重启") }[act] || act;
    if (!confirm(ctT("containers.act_confirm", "确认对容器「{name}」执行 {action}？").replace("{name}", name).replace("{action}", actLab))) return;
    try {
      const r = await fetch(`${API}/containers/${encodeURIComponent(host)}/${encodeURIComponent(id)}/action`, {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: act, name }),
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
      toast(ctT("containers.act_ok", "操作已提交"), "ok");
      setTimeout(() => loadContainersPanel(), 1200);
    } catch (e) { toast(String(e.message || e), "err"); }
    return;
  }

  const logBtn = t.closest("[data-ct-log]");
  if (logBtn) {
    ev.preventDefault();
    ev.stopPropagation();
    await ctOpenLog(logBtn.getAttribute("data-host"), logBtn.getAttribute("data-id"), logBtn.getAttribute("data-name"));
    return;
  }

  const ttyBtn = t.closest("[data-ct-tty]");
  if (ttyBtn) {
    ev.preventDefault();
    ev.stopPropagation();
    const host = ttyBtn.getAttribute("data-host");
    const id = ttyBtn.getAttribute("data-id");
    const name = ttyBtn.getAttribute("data-name") || id;
    const hostname = ttyBtn.getAttribute("data-hostname") || host;
    if (typeof window.openContainerTerminal === "function") {
      window.openContainerTerminal(host, hostname, id, name, "sh");
    } else if (typeof openTerminal === "function") {
      openTerminal(host, hostname, { containerId: id, containerName: name, shell: "sh" });
    } else {
      toast(ctT("containers.tty_unavailable", "终端未就绪"), "err");
    }
    return;
  }

  const execBtn = t.closest("[data-ct-exec]");
  if (execBtn) {
    ev.preventDefault();
    ev.stopPropagation();
    const host = execBtn.getAttribute("data-host");
    const id = execBtn.getAttribute("data-id");
    const name = execBtn.getAttribute("data-name") || id;
    const cmd = prompt(ctT("containers.exec_prompt", "在容器内执行命令（非交互，如：ps aux | head）"), "ps aux | head -n 20");
    if (cmd === null || !String(cmd).trim()) return;
    const outEl = $("ctExecOut");
    if (outEl) { outEl.style.display = "block"; outEl.textContent = ctT("ui.loading", "执行中…"); }
    try {
      const r = await fetch(`${API}/containers/${encodeURIComponent(host)}/${encodeURIComponent(id)}/exec`, {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ command: String(cmd).trim(), name }),
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(j.error || j.output || `HTTP ${r.status}`);
      if (outEl) outEl.textContent = j.output || "(empty)";
      toast(ctT("containers.exec_ok", "Exec 完成"), "ok");
    } catch (e) {
      if (outEl) outEl.textContent = String(e.message || e);
      toast(String(e.message || e), "err");
    }
    return;
  }

  const hostTerm = t.closest("[data-ct-hostterm]");
  if (hostTerm && typeof openTerminal === "function") {
    ev.preventDefault();
    ev.stopPropagation();
    openTerminal(hostTerm.getAttribute("data-ct-hostterm"), hostTerm.getAttribute("data-ct-hostname") || "");
    return;
  }

  const aiBtn = t.closest("[data-ct-ai]");
  if (aiBtn && typeof openAIAssist === "function") {
    ev.preventDefault();
    ev.stopPropagation();
    const row = CT_SELECTED;
    if (!row) return;
    openAIAssist({
      task: "container_ops_plan",
      title: ctT("containers.ai_title", "容器运维建议"),
      mode: "generate",
      context: JSON.stringify({
        host_id: row.host_id, host_name: row.host_name, id: row.id, name: row.name,
        image: row.image, state: row.state, status: row.status, ports: row.ports, runtime: row.runtime,
      }, null, 2),
      placeholder: ctT("containers.ai_ph", "例如：容器重启循环，给出可执行动作 JSON"),
      applyLabel: ctT("ai.apply_actions", "应用建议动作"),
      applyTo: async (text) => {
        if (typeof window.applyOpsActionPlan === "function") {
          return window.applyOpsActionPlan(text, { source: "containers", refresh: () => loadContainersPanel() });
        }
        return false;
      },
    });
    return;
  }

  const row = t.closest("tr[data-ct-row]");
  if (row) {
    const [hostId, cid] = (row.getAttribute("data-ct-row") || "").split("|");
    const hit = ctVisible(ctFlatRows()).find(r => r.host_id === hostId && r.id === cid);
    if (hit) {
      CT_SELECTED = hit;
      panel.querySelectorAll("tr.ct-row").forEach(tr => tr.classList.toggle("active-row", tr === row));
      ctPaintDetail(hit);
    }
  }
}

async function ctComposeList() {
  const host = ($("ctComposeHost") || {}).value;
  const out = $("ctComposeOut");
  if (!host) { if (out) out.textContent = ctT("containers.compose_pick_host", "选择主机…"); return; }
  if (out) out.textContent = ctT("ui.loading", "加载中…");
  try {
    const r = await fetch(`${API}/containers/compose?host=${encodeURIComponent(host)}`, { credentials: "same-origin" });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || j.output || `HTTP ${r.status}`);
    const data = j.data || j;
    if (out) out.textContent = typeof data === "string" ? data : JSON.stringify(data, null, 2);
  } catch (e) {
    if (out) out.textContent = String(e.message || e);
  }
}

async function ctComposeAction(action) {
  const host = ($("ctComposeHost") || {}).value;
  const project = (($("ctComposeProject") || {}).value || "").trim();
  const file = (($("ctComposeFile") || {}).value || "").trim();
  const out = $("ctComposeOut");
  if (!host) { toast(ctT("containers.compose_pick_host", "选择主机…"), "err"); return; }
  if (!project && !file) { toast(ctT("containers.compose_need_target", "请填写项目名或 compose 文件绝对路径"), "err"); return; }
  if (!confirm(ctT("containers.compose_confirm", "确认在主机上执行 compose {action}？").replace("{action}", action))) return;
  if (out) out.textContent = ctT("ui.loading", "执行中…");
  try {
    const r = await fetch(`${API}/containers/compose/${encodeURIComponent(host)}/action`, {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action, project, file }),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || j.output || `HTTP ${r.status}`);
    if (out) out.textContent = j.output || "ok";
    toast(ctT("containers.compose_ok", "Compose 完成"), "ok");
    setTimeout(() => loadContainersPanel(), 1500);
  } catch (e) {
    if (out) out.textContent = String(e.message || e);
    toast(String(e.message || e), "err");
  }
}

function ctWireCompose() {
  const refresh = $("ctComposeRefresh");
  if (refresh) refresh.onclick = () => ctComposeList();
  const host = $("ctComposeHost");
  if (host) host.onchange = () => ctComposeList();
  document.querySelectorAll("[data-ct-compose]").forEach(b => {
    b.onclick = () => ctComposeAction(b.getAttribute("data-ct-compose"));
  });
}

window._pageRenderers = window._pageRenderers || {};
if (!window._ctHostTreesRefreshBound) {
  window._ctHostTreesRefreshBound = true;
  let _ctTreeRefreshT = null;
  document.addEventListener("aiops:host-trees-refresh", () => {
    if (!document.querySelector("#view-containers.active")) return;
    if (_ctTreeRefreshT) clearTimeout(_ctTreeRefreshT);
    _ctTreeRefreshT = setTimeout(() => {
      _ctTreeRefreshT = null;
      if (typeof loadContainersPanel === "function") loadContainersPanel();
    }, 600);
  });
}

window._pageRenderers.containers = loadContainersPanel;
})();
