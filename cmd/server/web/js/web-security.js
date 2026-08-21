// web-security.js — 安全中心 · Web 漏洞扫描（商用交互）
(function () {
"use strict";

let wsTargets = [];
let wsScans = [];
let wsSelected = null;
let wsBusy = false;
let wsPollTimer = null;
let wsCfg = null;
let wsEngine = null;
let wsShowForm = false;
let wsShowCfg = false;
let wsEditTarget = null; // target being edited, or null for create
let wsTagPick = ["misconfig", "exposures", "panel", "cves", "default-logins"];
let wsSevPick = ["critical", "high", "medium", "low", "info"];
let wsAuthType = "none";
let wsProfilePick = "standard"; // quick | standard | deep | custom
let wsShowPacks = false;
let wsPendingFilter = null; // { level } from security overview
let wsPickSelected = new Set(); // toolbar multi-select
let wsPickCollapsed = new Set();
let wsPickQ = "";
let wsPickInited = false;

const wsT = (k, fb) => I18N.t(k, fb);
function wsEsc(s) { return typeof esc === "function" ? esc(String(s ?? "")) : String(s ?? ""); }

function wsTargetTitle(t) {
  const name = (t && (t.name || t.id)) || "";
  const url = (t && t.base_url) || "";
  return url ? `${name} (${url})` : name;
}
function wsCapturePick() {
  const root = $("wsTargetTree");
  if (!root) return;
  root.querySelectorAll(".hs-pick-host:checked").forEach(cb => wsPickSelected.add(cb.value));
  root.querySelectorAll(".hs-pick-host:not(:checked)").forEach(cb => wsPickSelected.delete(cb.value));
}
function wsInitPickDefaults(targets) {
  if (wsPickInited) return;
  wsPickInited = true;
  const enabled = (targets || []).filter(t => t && t.enabled !== false);
  (enabled.length ? enabled : targets || []).forEach(t => { if (t && t.id) wsPickSelected.add(t.id); });
}
function wsFilterTarget(t, q) {
  if (!q) return true;
  const hay = [t.id, t.name, t.base_url, t.auth_type, ...(t.tags || [])]
    .filter(Boolean).join(" ").toLowerCase();
  return hay.includes(q);
}
function wsPickTargetRowHTML(t, selected, depth) {
  const enabled = t.enabled !== false;
  const pad = Math.min(depth, 6) * 14;
  return `<label class="hs-pick-row${enabled ? "" : " off"}${selected.has(t.id) && enabled ? " is-on" : ""}" style="padding-left:${pad + 22}px" title="${wsEsc(wsTargetTitle(t))}">
    <input type="checkbox" class="hs-pick-host" value="${wsEsc(t.id)}" ${enabled ? "" : "disabled"} ${selected.has(t.id) ? "checked" : ""}>
    <span class="hs-pick-name">${wsEsc(t.name || t.id)}</span>
    <span class="hs-pick-ip mono">${wsEsc(t.base_url || "—")}</span>
    <span class="hs-pick-st ${enabled ? "ok" : ""}"><i class="hs-pick-dot" aria-hidden="true"></i>${enabled ? wsEsc(wsT("ws.on", "启用")) : wsEsc(wsT("ws.off", "停用"))}</span>
  </label>`;
}
function wsPickFolderBlockHTML(fid, label, list, selected, collapsed) {
  const isCollapsed = collapsed.has(fid);
  const ids = list.filter(t => t.enabled !== false).map(t => t.id);
  const checkedN = ids.filter(id => selected.has(id)).length;
  const folderState = !ids.length ? "" : (checkedN === ids.length ? "checked" : (checkedN > 0 ? "data-indeterminate=\"1\"" : ""));
  let html = `<div class="hs-pick-folder">
    <button type="button" class="hs-pick-caret" data-ws-fold="${wsEsc(fid)}" aria-expanded="${isCollapsed ? "false" : "true"}">${isCollapsed ? "▸" : "▾"}</button>
    <label class="hs-pick-folder-lab">
      <input type="checkbox" class="hs-pick-folder-cb" data-ws-folder="${wsEsc(fid)}" ${folderState}>
      <span class="hs-pick-folder-name">${wsEsc(label)}</span>
      <span class="hs-pick-count">${list.length}</span>
    </label>
  </div>`;
  if (!isCollapsed) html += list.map(t => wsPickTargetRowHTML(t, selected, 1)).join("");
  return html;
}
function wsPickTreeHTML() {
  const targets = wsTargets || [];
  const selected = wsPickSelected;
  const collapsed = wsPickCollapsed;
  const q = (wsPickQ || "").trim().toLowerCase();
  const filtered = q ? targets.filter(t => wsFilterTarget(t, q)) : targets.slice();
  filtered.sort((a, b) => {
    const an = (a.name || a.id || "").toLowerCase();
    const bn = (b.name || b.id || "").toLowerCase();
    if (an !== bn) return an < bn ? -1 : 1;
    return String(a.base_url || "").localeCompare(String(b.base_url || ""));
  });
  const enabledN = filtered.filter(t => t.enabled !== false).length;
  const selN = [...selected].filter(id => filtered.some(t => t.id === id)).length;
  let body = "";
  if (!targets.length) {
    body = `<div class="hs-pick-empty">${wsEsc(wsT("ws.no_targets", "暂无目标"))}</div>`;
  } else if (!filtered.length) {
    body = `<div class="hs-pick-empty">${wsEsc(wsT("ws.no_target_match", "无匹配目标"))}</div>`;
  } else {
    const on = filtered.filter(t => t.enabled !== false);
    const off = filtered.filter(t => t.enabled === false);
    if (on.length) body += wsPickFolderBlockHTML("grp:enabled", wsT("ws.grp_enabled", "启用"), on, selected, collapsed);
    if (off.length) body += wsPickFolderBlockHTML("grp:disabled", wsT("ws.grp_disabled", "停用"), off, selected, collapsed);
  }
  return `<div class="hs-pick-tree-wrap" data-ws-pick="scan">
    <div class="hs-pick-tools">
      <input type="search" id="wsTargetSearch" class="hs-pick-search" value="${wsEsc(wsPickQ || "")}" placeholder="${wsEsc(wsT("ws.target_search_ph", "搜索名称 / URL…"))}" autocomplete="off">
      <div class="hs-pick-quick">
        <button type="button" class="btn sm ghost" data-ws-pick-act="all-enabled">${wsEsc(wsT("ws.select_all_enabled", "全选启用"))}</button>
        <button type="button" class="btn sm ghost" data-ws-pick-act="clear">${wsEsc(wsT("ws.clear_sel", "清空"))}</button>
        <span class="hs-pick-meta">${selN}/${enabledN || filtered.length}</span>
      </div>
    </div>
    <div class="hs-pick-tree" id="wsTargetTree">${body}</div>
  </div>`;
}
function wsFolderMemberIds(folderId) {
  const q = (wsPickQ || "").trim().toLowerCase();
  const targets = wsTargets || [];
  if (folderId === "grp:enabled") {
    return targets.filter(t => t.enabled !== false && wsFilterTarget(t, q)).map(t => t.id);
  }
  if (folderId === "grp:disabled") {
    return targets.filter(t => t.enabled === false && wsFilterTarget(t, q)).map(t => t.id);
  }
  return [];
}
function wsBindPickTree(root) {
  if (!root) return;
  root.querySelectorAll(".hs-pick-folder-cb[data-indeterminate]").forEach(cb => { cb.indeterminate = true; });
  root.querySelectorAll("[data-ws-fold]").forEach(btn => {
    btn.addEventListener("click", e => {
      e.preventDefault();
      const id = btn.dataset.wsFold;
      if (wsPickCollapsed.has(id)) wsPickCollapsed.delete(id); else wsPickCollapsed.add(id);
      wsCapturePick();
      wsPickQ = ($("wsTargetSearch") && $("wsTargetSearch").value) || wsPickQ;
      paintWebSecurity();
    });
  });
  root.querySelectorAll(".hs-pick-folder-cb").forEach(cb => {
    cb.addEventListener("change", () => {
      const folderId = cb.dataset.wsFolder;
      const ids = wsFolderMemberIds(folderId);
      ids.forEach(id => { if (cb.checked) wsPickSelected.add(id); else wsPickSelected.delete(id); });
      wsPickQ = ($("wsTargetSearch") && $("wsTargetSearch").value) || "";
      paintWebSecurity();
    });
  });
  const search = root.querySelector(".hs-pick-search");
  if (search) {
    search.addEventListener("input", () => {
      wsCapturePick();
      wsPickQ = search.value || "";
      paintWebSecurity();
      const again = $("wsTargetSearch");
      if (again) { again.focus(); const v = again.value; again.setSelectionRange(v.length, v.length); }
    });
  }
  root.querySelectorAll("[data-ws-pick-act]").forEach(btn => {
    btn.addEventListener("click", () => {
      wsCapturePick();
      if (btn.dataset.wsPickAct === "clear") wsPickSelected.clear();
      else (wsTargets || []).filter(t => t.enabled !== false).forEach(t => wsPickSelected.add(t.id));
      paintWebSecurity();
    });
  });
  root.querySelectorAll(".hs-pick-host").forEach(cb => {
    cb.addEventListener("change", () => {
      if (cb.checked) wsPickSelected.add(cb.value); else wsPickSelected.delete(cb.value);
      const meta = root.querySelector(".hs-pick-meta");
      if (meta) {
        const enabledN = (wsTargets || []).filter(t => t.enabled !== false).length;
        meta.textContent = `${wsPickSelected.size}/${enabledN}`;
      }
      const chip = document.querySelector("#webSecurityPanel .sec-sel-chip");
      if (chip) chip.textContent = `${wsSelectedScanIds().length} ${wsT("ws.selected_n", "已选")}`;
      root.querySelectorAll(".hs-pick-row").forEach(row => {
        const input = row.querySelector(".hs-pick-host");
        if (input) row.classList.toggle("is-on", !!input.checked && !input.disabled);
      });
    });
  });
}
function wsSelectedScanIds() {
  wsCapturePick();
  return [...wsPickSelected].filter(id => (wsTargets || []).some(t => t.id === id && t.enabled !== false));
}

const WS_TAG_OPTS = [
  ["misconfig", "ws.tag_misconfig", "错误配置"], ["exposures", "ws.tag_exposures", "信息暴露"],
  ["panel", "ws.tag_panel", "管理面板"], ["cves", "ws.tag_cves", "CVE"],
  ["default-logins", "ws.tag_logins", "默认口令"], ["vulnerabilities", "ws.tag_vuln", "通用漏洞"],
  ["technologies", "ws.tag_tech", "技术指纹"], ["xss", "ws.tag_xss", "XSS"],
  ["sqli", "ws.tag_sqli", "SQLi"], ["rce", "ws.tag_rce", "RCE"],
];
const WS_SEV_OPTS = [
  ["critical", "ws.sev_critical", "危急"], ["high", "ws.sev_high", "高危"],
  ["medium", "ws.sev_medium", "中危"], ["low", "ws.sev_low", "低危"], ["info", "ws.sev_info", "信息"],
];
const WS_PROFILE = {
  quick: ["misconfig", "exposures"],
  standard: ["misconfig", "exposures", "panel", "cves", "default-logins"],
  deep: ["misconfig", "exposures", "panel", "cves", "default-logins", "vulnerabilities", "technologies", "xss", "sqli", "rce"],
};

function wsSameTagSet(a, b) {
  const sa = new Set(a || []), sb = new Set(b || []);
  if (sa.size !== sb.size) return false;
  for (const x of sa) if (!sb.has(x)) return false;
  return true;
}
function wsMatchProfile(tags) {
  if (wsSameTagSet(tags, WS_PROFILE.quick)) return "quick";
  if (wsSameTagSet(tags, WS_PROFILE.standard)) return "standard";
  if (wsSameTagSet(tags, WS_PROFILE.deep)) return "deep";
  return "custom";
}
function wsApplyProfile(profile) {
  const p = WS_PROFILE[profile] ? profile : "standard";
  wsProfilePick = p;
  wsTagPick = WS_PROFILE[p].slice();
  wsSyncProfileUI();
  wsSyncTagChipsUI();
}
function wsSyncProfileUI() {
  const cur = wsProfilePick || wsMatchProfile(wsTagPick);
  document.querySelectorAll("#webSecurityPanel [data-wsprofile]").forEach(btn => {
    const on = btn.dataset.wsprofile === cur;
    btn.classList.toggle("primary", on);
    btn.classList.toggle("on", on);
    btn.setAttribute("aria-pressed", on ? "true" : "false");
  });
  const hint = $("wsProfileHint");
  if (hint) hint.textContent = wsProfileHintText();
}
function wsSyncTagChipsUI() {
  const box = $("wsTagChips");
  if (!box) return;
  box.innerHTML = wsChipGroup(WS_TAG_OPTS, wsTagPick, "wstag");
}

function wsSevLabel(sev) {
  const m = {
    critical: wsT("ws.sev_critical", "危急"), high: wsT("ws.sev_high", "高危"),
    medium: wsT("ws.sev_medium", "中危"), low: wsT("ws.sev_low", "低危"),
    info: wsT("ws.sev_info", "信息"),
  };
  return m[String(sev || "").toLowerCase()] || (sev || "—");
}
async function wsUpdateFindingStatus(finding, status) {
  if (!wsSelected || !finding) return;
  try {
    const r = await fetch(`${API}/security/findings/status`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        scope: "web", target_id: wsSelected.target_id, status,
        finding: {
          template_id: finding.template_id,
          url: finding.url || finding.matched_at,
          matcher_name: finding.matcher_name || "",
        },
      }),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || "更新失败");
    finding.status = status;
    wsPaintDetail(wsSelected);
    toast(wsT("ws.finding_updated", "状态已更新"), "ok");
  } catch (e) { toast(e.message || String(e), "err"); }
}
function wsFindingStatusSelect(f) {
  const st = f.status || "open";
  const opts = ["open", "ack", "false_positive", "resolved"];
  return `<select class="ws-finding-status" data-wstpl="${wsEsc(f.template_id || "")}" data-wsmatcher="${wsEsc(f.matcher_name || "")}" data-wsurl="${wsEsc(f.url || f.matched_at || "")}" style="font-size:11px;height:26px;margin-left:6px">
    ${opts.map(o => `<option value="${o}"${o === st ? " selected" : ""}>${o}</option>`).join("")}
  </select>`;
}
function wsSevBadge(sev) {
  const m = { critical: "crit", high: "high", medium: "warn", low: "info", info: "info" };
  const cls = m[String(sev || "").toLowerCase()] || "info";
  return `<span class="badge ${cls}">${wsEsc(wsSevLabel(sev))}</span>`;
}
function wsStatusLabel(st) {
  const m = {
    running: wsT("ws.status_running", "进行中"),
    completed: wsT("ws.status_completed", "已完成"),
    failed: wsT("ws.status_failed", "失败"),
  };
  return m[st] || st || "—";
}
function wsStatusBadge(st) {
  const m = { running: "info", completed: "ok", failed: "crit" };
  return `<span class="badge ${m[st] || "info"}">${wsEsc(wsStatusLabel(st))}</span>`;
}
function wsTagLabel(tag) {
  const hit = WS_TAG_OPTS.find(x => x[0] === tag);
  if (hit) return wsT(hit[1], hit[2] || hit[0]);
  return tag;
}
function wsSevLabel(sev) {
  const hit = WS_SEV_OPTS.find(x => x[0] === sev);
  if (hit) return wsT(hit[1], hit[2] || hit[0]);
  return sev;
}
function wsFmtTime(ts) {
  if (!ts) return "—";
  try { return new Date(ts * 1000).toLocaleString(); } catch (_) { return "—"; }
}
function wsScanLabel(s) {
  if (!s) return "—";
  if (s.label) return s.label;
  const name = s.target_name || s.base_url || "扫描";
  return `${name} · ${wsFmtTime(s.started_at)}`;
}
function wsScanIdShort(id) {
  const s = String(id || "");
  if (s.length <= 22) return s;
  return s.slice(0, 18) + "…";
}
function wsChipGroup(opts, selected, dataAttr) {
  return opts.map(row => {
    const id = row[0];
    const label = row.length >= 3 ? wsT(row[1], row[2]) : row[1];
    const on = selected.includes(id);
    return `<button type="button" class="ws-chip${on ? " on" : ""}" data-${dataAttr}="${wsEsc(id)}">${wsEsc(label)}</button>`;
  }).join("");
}

function wsExportMenuHTML(disabled) {
  const dis = disabled ? " disabled" : "";
  return `<div class="exp-dd">
    <button class="btn" data-ws="export-toggle" aria-haspopup="true"${dis}>${wsEsc(wsT("ws.export", "导出报告"))}</button>
    <div class="exp-dd-menu" id="wsExportMenu" role="menu">
      <button class="exp-dd-opt" role="menuitem" data-wsexport="pdf"><span>${wsEsc(wsT("ws.export_pdf", "PDF 报告"))}</span><span class="exp-dd-ext">${wsEsc(wsT("ws.export_pdf_tip", "打印"))}</span></button>
      <button class="exp-dd-opt" role="menuitem" data-wsexport="word"><span>${wsEsc(wsT("ws.export_word", "Word 文档"))}</span><span class="exp-dd-ext">.docx</span></button>
      <button class="exp-dd-opt" role="menuitem" data-wsexport="html"><span>${wsEsc(wsT("ws.export_html", "HTML 网页"))}</span><span class="exp-dd-ext">.html</span></button>
      <button class="exp-dd-opt" role="menuitem" data-wsexport="markdown"><span>${wsEsc(wsT("ws.export_md", "Markdown"))}</span><span class="exp-dd-ext">.md</span></button>
      <button class="exp-dd-opt" role="menuitem" data-wsexport="excel"><span>${wsEsc(wsT("ws.export_excel", "Excel 表格"))}</span><span class="exp-dd-ext">.xlsx</span></button>
      <button class="exp-dd-opt" role="menuitem" data-wsexport="json"><span>${wsEsc(wsT("ws.export_json", "JSON 原始数据"))}</span><span class="exp-dd-ext">.json</span></button>
    </div>
  </div>`;
}

async function wsFetchJSON(url, opts) {
  const r = await fetch(url, Object.assign({ credentials: "same-origin" }, opts || {}));
  let d = null;
  try { d = await r.json(); } catch (_) { d = null; }
  if (!r.ok) throw new Error((d && d.error) || r.statusText || ("HTTP " + r.status));
  return d;
}

function wsEngineBarHTML() {
  const e = wsEngine || {};
  const ready = !!e.ready;
  const packs = e.packs || [];
  const activePacks = packs.filter(p => p.count > 0);
  const packHTML = packs.length
    ? packs.map(p => `<span class="ws-pack${p.count ? "" : " empty"}" title="${wsEsc(p.path || "")}"><b>${wsEsc(p.name)}</b><em>${p.count || 0}</em></span>`).join("")
    : `<span class="muted">${wsEsc(wsT("ws.engine_no_packs", "暂无模板包信息"))}</span>`;
  const ver = e.nuclei_version ? String(e.nuclei_version).replace(/^v?/i, "v") : "";
  return `<div class="ws-engine ${ready ? "ready" : "warn"}">
    <div class="ws-engine-main">
      <div class="ws-engine-status">
        <span class="ws-dot"></span>
        <div>
          <div class="ws-engine-title">${wsEsc(ready ? wsT("ws.engine_ready", "扫描引擎就绪") : wsT("ws.engine_not_ready", "扫描引擎未就绪"))}</div>
          <div class="ws-engine-sub">${ver ? wsEsc(ver) : ""}${e.message ? (ver ? " · " : "") + wsEsc(e.message) : ""}</div>
        </div>
      </div>
      <div class="ws-engine-kpis">
        <div><strong>${e.template_count || 0}</strong><span>${wsEsc(wsT("ws.tpl_total", "模板"))}</span></div>
        <div><strong>${activePacks.length}</strong><span>${wsEsc(wsT("ws.tpl_packs", "模板包"))}</span></div>
        <div><strong>${e.timeout_sec || 900}s</strong><span>${wsEsc(wsT("ws.cfg_timeout_short", "超时"))}</span></div>
        <div><strong>${e.rate_limit || 120}/s</strong><span>${wsEsc(wsT("ws.cfg_rate_short", "速率"))}</span></div>
        <div><strong>${e.concurrency || 25}</strong><span>${wsEsc(wsT("ws.cfg_conc_short", "模板并发"))}</span></div>
        <div><strong>${e.scan_concurrency || 3}</strong><span>${wsEsc(wsT("ws.cfg_scan_conc_short", "任务并发"))}</span></div>
      </div>
      <div class="ws-engine-actions">
        <button class="btn sm ghost" data-ws="toggle-packs">${wsEsc(wsShowPacks ? wsT("ws.hide_packs", "收起模板包") : wsT("ws.show_packs", "模板包"))}</button>
        <button class="btn sm ghost${typeof SF_OPEN !== "undefined" && SF_OPEN ? " on primary" : ""}" data-ws="feeds" aria-pressed="${typeof SF_OPEN !== "undefined" && SF_OPEN ? "true" : "false"}">${wsEsc(wsT("ws.feeds", "情报源"))}</button>
        ${typeof isAdmin === "function" && isAdmin() ? `<button class="btn sm" data-ws="cfg">${wsEsc(wsT("ws.config", "引擎配置"))}</button>
        <button class="btn sm" data-ws="refresh-tpl">${wsEsc(wsT("ws.refresh_tpl", "更新模板"))}</button>` : ""}
      </div>
    </div>
    ${wsShowPacks ? `<div class="ws-packs">${packHTML}</div>` : ""}
  </div>`;
}

function wsCfgPanelHTML() {
  if (!wsShowCfg || !wsCfg) return "";
  const sev = String(wsCfg.severity || "critical,high,medium,low,info").split(",").map(s => s.trim()).filter(Boolean);
  wsSevPick = sev.length ? sev : wsSevPick;
  return `<div class="cfg-panel sec-cfg-panel ws-cfg">
    <div class="cfg-panel-head"><div>
      <div class="cfg-panel-title">${wsEsc(wsT("ws.config_title", "引擎与策略"))}</div>
      <p class="cfg-panel-desc">${wsEsc(wsT("ws.config_desc", "用业务语言配置扫描强度；底层仍由 Nuclei 执行。写入需管理员权限。"))}</p>
    </div></div>
    <div class="ws-cfg-grid">
      <div class="ws-cfg-card">
        <h4>${wsEsc(wsT("ws.cfg_sev_title", "关注的风险等级"))}</h4>
        <p class="ws-help">${wsEsc(wsT("ws.cfg_sev_help", "勾选要纳入报告的严重度。建议保留「信息」，便于发现指纹与弱配置。"))}</p>
        <div class="ws-chips" id="wsSevChips">${wsChipGroup(WS_SEV_OPTS, wsSevPick, "wssev")}</div>
      </div>
      <div class="ws-cfg-card">
        <h4>${wsEsc(wsT("ws.cfg_limit_title", "速度与时限"))}</h4>
        <p class="ws-help">${wsEsc(wsT("ws.cfg_limit_help", "超时：单次扫描最长等待；速率：每秒最大请求数；模板并发：Nuclei 内部并行；任务并发：同时扫描几个目标。标准/深度含 CVE 包时建议超时≥900s。"))}</p>
        <div class="cfg-form-row">
          <div class="field"><label>${wsEsc(wsT("ws.cfg_timeout", "超时（秒）"))}</label>
            <input id="wsCfgTimeout" type="number" min="60" max="7200" value="${wsEsc(wsCfg.timeout_sec || 900)}"></div>
          <div class="field"><label>${wsEsc(wsT("ws.cfg_rate", "速率限制（请求/秒）"))}</label>
            <input id="wsCfgRate" type="number" min="5" max="500" value="${wsEsc(wsCfg.rate_limit || 120)}"></div>
        </div>
        <div class="cfg-form-row">
          <div class="field"><label>${wsEsc(wsT("ws.cfg_concurrency", "模板并发（-c）"))}</label>
            <input id="wsCfgConc" type="number" min="5" max="100" value="${wsEsc(wsCfg.concurrency || 25)}"></div>
          <div class="field"><label>${wsEsc(wsT("ws.cfg_scan_concurrency", "任务并发"))}</label>
            <input id="wsCfgScanConc" type="number" min="1" max="8" value="${wsEsc(wsCfg.scan_concurrency || 3)}"></div>
        </div>
      </div>
      <div class="ws-cfg-card">
        <h4>${wsEsc(wsT("ws.cfg_adv_title", "高级选项"))}</h4>
        <p class="ws-help">${wsEsc(wsT("ws.cfg_path_help", "一般无需修改引擎路径；私网扫描有 SSRF 风险，仅内网资产评估时开启。"))}</p>
        <div class="field"><label>${wsEsc(wsT("ws.cfg_path", "引擎命令"))}</label>
          <input id="wsCfgPath" class="mono" value="${wsEsc(wsCfg.nuclei_path || "nuclei")}"></div>
        <label class="switch cfg-enable"><input type="checkbox" id="wsCfgPrivate"${wsCfg.allow_private ? " checked" : ""}>
          <span>${wsEsc(wsT("ws.allow_private", "允许扫描私网地址（有 SSRF 风险）"))}</span></label>
        <label class="switch cfg-enable"><input type="checkbox" id="wsCfgUpdate"${wsCfg.update_templates ? " checked" : ""}>
          <span>${wsEsc(wsT("ws.update_templates", "启动时增量更新模板"))}</span></label>
        <label class="switch cfg-enable"><input type="checkbox" id="wsCfgBuiltin"${wsCfg.disable_builtin_checks ? "" : " checked"}>
          <span>${wsEsc(wsT("ws.cfg_builtin", "启用内置检测（TLS/证书/安全头/Cookie/CORS/敏感路径）"))}</span></label>
        <p class="ws-help">${wsEsc(wsT("ws.cfg_builtin_help", "内置检测不依赖 Nuclei；即使模板未就绪也能给出传输层与配置层结论。"))}</p>
        <label class="switch cfg-enable"><input type="checkbox" id="wsCfgAISummary"${wsCfg.auto_ai_summary ? " checked" : ""}>
          <span>${wsEsc(wsT("ws.cfg_ai_summary", "扫描完成后自动 AI 摘要"))}</span></label>
      </div>
    </div>
    <div class="cfg-actions">
      <button class="btn primary" data-ws="save-cfg">${wsEsc(wsT("common.save", "保存"))}</button>
      <button class="btn" data-ws="cfg">${wsEsc(wsT("common.cancel", "收起"))}</button>
      <span class="cfg-status" id="wsCfgStatus"></span>
    </div>
  </div>`;
}

function wsAuthLabel(t) {
  const m = {
    none: wsT("ws.auth_none", "无鉴权"),
    basic: wsT("ws.auth_basic", "Basic 账号密码"),
    bearer: wsT("ws.auth_bearer", "Bearer Token"),
    cookie: wsT("ws.auth_cookie", "Cookie"),
    header: wsT("ws.auth_header", "自定义请求头"),
    form: wsT("ws.auth_form", "表单登录"),
    header_body: wsT("ws.auth_header_body", "请求头+请求体预认证"),
  };
  return m[t] || t || m.none;
}
function wsAuthFieldsHTML() {
  const t = wsEditTarget || {};
  const at = wsAuthType || t.auth_type || "none";
  const showUser = at === "basic" || at === "form";
  const showPass = at === "basic" || at === "form" || at === "bearer";
  const showHeader = at === "header" || at === "cookie" || at === "form" || at === "header_body";
  const showBody = at === "form" || at === "header_body";
  const showLogin = at === "form" || at === "header_body";
  let html = `<div class="field"><label>${wsEsc(wsT("ws.auth_type", "鉴权方式"))}</label>
    <div class="select-wrap"><select id="wsFormAuth">
      <option value="none"${at === "none" ? " selected" : ""}>${wsEsc(wsT("ws.auth_none", "无鉴权（公开页面）"))}</option>
      <option value="basic"${at === "basic" ? " selected" : ""}>${wsEsc(wsT("ws.auth_basic", "Basic 用户名/密码"))}</option>
      <option value="bearer"${at === "bearer" ? " selected" : ""}>${wsEsc(wsT("ws.auth_bearer", "Bearer Token"))}</option>
      <option value="cookie"${at === "cookie" ? " selected" : ""}>${wsEsc(wsT("ws.auth_cookie", "Cookie 会话"))}</option>
      <option value="header"${at === "header" ? " selected" : ""}>${wsEsc(wsT("ws.auth_header", "自定义请求头"))}</option>
      <option value="form"${at === "form" ? " selected" : ""}>${wsEsc(wsT("ws.auth_form", "表单登录（用户名+密码+请求体）"))}</option>
      <option value="header_body"${at === "header_body" ? " selected" : ""}>${wsEsc(wsT("ws.auth_header_body", "请求头 + 请求体预认证"))}</option>
    </select></div>
    <p class="ws-help">${wsEsc(wsT("ws.auth_help", "登录类鉴权会先向登录 URL 发请求拿 Cookie/会话，再带着凭证做完整性扫描。"))}</p>
  </div>`;
  if (showUser || showPass) {
    html += `<div class="cfg-form-row">`;
    if (showUser) html += `<div class="field"><label>${wsEsc(wsT("ws.auth_user", "用户名"))}</label>
      <input id="wsFormUser" value="${wsEsc(t.auth_user || "")}" autocomplete="off"></div>`;
    if (showPass) html += `<div class="field"><label>${wsEsc(at === "bearer" ? wsT("ws.auth_token", "Token") : wsT("ws.auth_pass", "密码"))}</label>
      <input id="wsFormPass" type="password" placeholder="${wsEsc(t.id ? wsT("ws.secret_keep", "留空则保持不变") : "")}" autocomplete="new-password"></div>`;
    html += `</div>`;
  }
  if (showLogin) {
    html += `<div class="cfg-form-row">
      <div class="field"><label>${wsEsc(wsT("ws.auth_login_url", "登录/预认证 URL"))}</label>
        <input id="wsFormLoginURL" class="mono" placeholder="/api/login 或 https://…" value="${wsEsc(t.auth_login_url || "")}"></div>
      <div class="field"><label>${wsEsc(wsT("ws.auth_method", "请求方法"))}</label>
        <div class="select-wrap"><select id="wsFormAuthMethod">
          ${["POST", "PUT", "PATCH", "GET"].map(m => `<option value="${m}"${(t.auth_method || "POST") === m ? " selected" : ""}>${m}</option>`).join("")}
        </select></div></div>
    </div>`;
  }
  if (showHeader) {
    const hdr = (t.auth_header || "").includes("********") ? "" : (t.auth_header || "");
    html += `<div class="field"><label>${wsEsc(wsT("ws.auth_headers", "请求头（每行 Name: Value）"))}</label>
      <textarea id="wsFormHeaders" rows="3" class="mono" placeholder="Authorization: Bearer xxx&#10;X-Api-Key: …">${wsEsc(hdr)}</textarea>
      <p class="ws-help">${wsEsc(t.id && (t.auth_header || "").includes("********") ? wsT("ws.secret_keep_hdr", "已保存的请求头已隐藏；留空则保持不变，填写则整段替换。") : wsT("ws.auth_headers_help", "Cookie 鉴权可写 Cookie: a=1; b=2"))}</p>
    </div>`;
  }
  if (showBody) {
    const body = (t.auth_body || "").includes("********") || (t.auth_body || "").includes('"_masked"') ? "" : (t.auth_body || "");
    html += `<div class="field"><label>${wsEsc(wsT("ws.auth_body", "请求体"))}</label>
      <textarea id="wsFormBody" rows="4" class="mono" placeholder='{"username":"{{username}}","password":"{{password}}"}'>${wsEsc(body)}</textarea>
      <p class="ws-help">${wsEsc(wsT("ws.auth_body_help", "支持占位符 {{username}} / {{password}}。表单登录留空时默认按 username/password 表单提交。"))}</p>
      <div class="field" style="margin-top:8px"><label>${wsEsc(wsT("ws.auth_ctype", "Content-Type"))}</label>
        <input id="wsFormCType" class="mono" placeholder="application/json 或 application/x-www-form-urlencoded" value="${wsEsc(t.auth_content_type || "")}"></div>
    </div>`;
  }
  return html;
}
function wsProfileHintText() {
  const cur = wsProfilePick || wsMatchProfile(wsTagPick);
  const lab = {
    quick: wsT("ws.profile_quick", "快速体检"),
    standard: wsT("ws.profile_standard", "标准扫描"),
    deep: wsT("ws.profile_deep", "深度扫描"),
    custom: wsT("ws.profile_custom", "自定义"),
  }[cur] || cur;
  return wsT("ws.profile_current", "当前档位：{p}").replace("{p}", lab)
    + " · " + wsT("ws.profile_tags_n", "已选 {n} 个模板包").replace("{n}", String((wsTagPick || []).length));
}
function wsProfileButtonsHTML() {
  const cur = wsProfilePick || wsMatchProfile(wsTagPick);
  const items = [
    ["quick", wsT("ws.profile_quick", "快速体检"), wsT("ws.profile_quick_tip", "基础暴露面，速度快")],
    ["standard", wsT("ws.profile_standard", "标准扫描"), wsT("ws.profile_standard_tip", "含面板/CVE/默认口令")],
    ["deep", wsT("ws.profile_deep", "深度扫描"), wsT("ws.profile_deep_tip", "全模板包，耗时更长")],
  ];
  return `<div class="ws-profile-row" role="group" aria-label="${wsEsc(wsT("ws.profile", "扫描档位"))}">
    ${items.map(([id, lab, tip]) => {
      const on = cur === id;
      return `<button type="button" class="btn sm ws-profile-btn${on ? " primary on" : ""}" data-wsprofile="${id}"
        aria-pressed="${on ? "true" : "false"}" title="${wsEsc(tip)}">${wsEsc(lab)}</button>`;
    }).join("")}
  </div>
  <p class="ws-help" id="wsProfileHint">${wsEsc(wsProfileHintText())}</p>`;
}

function wsFormPanelHTML() {
  if (!wsShowForm) return "";
  const editing = !!(wsEditTarget && wsEditTarget.id);
  const t = wsEditTarget || {};
  return `<div class="ws-form-mask" id="wsFormMask" data-ws="cancel-form-mask">
    <div class="ws-form-modal cfg-panel" role="dialog" aria-modal="true">
      <div class="cfg-panel-head ws-form-head">
        <div>
          <div class="cfg-panel-title">${wsEsc(editing ? wsT("ws.edit_target", "编辑扫描目标") : wsT("ws.add_target", "添加扫描目标"))}</div>
          <p class="cfg-panel-desc">${wsEsc(wsT("ws.form_desc", "配置名称、地址、扫描档位、鉴权与定时策略；保存后立即生效。"))}</p>
        </div>
        <button type="button" class="btn sm" data-ws="cancel-form">${wsEsc(wsT("common.cancel", "关闭"))}</button>
      </div>
      <div class="cfg-form ws-form-body">
        <div class="cfg-form-row">
          <div class="field"><label>${wsEsc(wsT("ws.name", "名称"))}</label><input id="wsFormName" placeholder="生产官网" value="${wsEsc(t.name || "")}"></div>
          <div class="field"><label>${wsEsc(wsT("ws.base_url", "目标地址"))}</label>
            <input id="wsFormURL" class="mono" placeholder="https://example.com" value="${wsEsc(t.base_url || "")}"></div>
        </div>
        <label class="switch cfg-enable"><input type="checkbox" id="wsFormEnabled"${t.enabled !== false ? " checked" : ""}>
          <span>${wsEsc(wsT("ws.enabled", "启用该目标"))}</span></label>
        <div class="field">
          <label>${wsEsc(wsT("ws.profile", "扫描档位"))}</label>
          ${wsProfileButtonsHTML()}
          <p class="ws-help">${wsEsc(wsT("ws.profile_help", "点击档位会立即切换下方模板范围；也可手动勾选标签变为「自定义」。"))}</p>
        </div>
        <div class="field">
          <label>${wsEsc(wsT("ws.tags", "模板范围"))}</label>
          <div class="ws-chips" id="wsTagChips">${wsChipGroup(WS_TAG_OPTS, wsTagPick, "wstag")}</div>
        </div>
        ${wsAuthFieldsHTML()}
        ${wsScheduleFieldsHTML(t)}
        ${editing ? `<div class="field"><label>${wsEsc(wsT("ws.openapi_scope", "OpenAPI 扩大扫描范围"))}</label>
          <textarea id="wsFormOpenAPI" rows="4" class="mono" placeholder='粘贴 OpenAPI 3 / Swagger 2 JSON'></textarea>
          <p class="ws-help">解析 paths 为额外 URL（最多 80），写入本目标 scan_urls，Nuclei 多 -u 扫描。当前已有 ${(t.scan_urls || []).length} 条。</p>
          <button type="button" class="btn sm" data-ws="import-openapi">${wsEsc(wsT("ws.import_openapi", "导入 OpenAPI 范围"))}</button>
        </div>` : ""}
        <div class="cfg-actions ws-form-actions">
          <button type="button" class="btn primary" data-ws="save-target">${wsEsc(editing ? wsT("common.save", "保存修改") : wsT("ws.save_enable", "保存并启用"))}</button>
          <button type="button" class="btn" data-ws="cancel-form">${wsEsc(wsT("common.cancel", "取消"))}</button>
        </div>
      </div>
    </div>
  </div>`;
}

function wsScheduleFieldsHTML(t) {
  const sc = (t && t.schedule) || {};
  const enabled = !!sc.enabled;
  const kind = sc.kind || "weekly";
  const at = sc.at || (kind === "interval" ? String(sc.interval_min || 1440) : "03:30");
  const weekday = sc.weekday != null ? sc.weekday : 0;
  const weekdays = [
    [0, wsT("ws.wd_sun", "周日")], [1, wsT("ws.wd_mon", "周一")], [2, wsT("ws.wd_tue", "周二")],
    [3, wsT("ws.wd_wed", "周三")], [4, wsT("ws.wd_thu", "周四")], [5, wsT("ws.wd_fri", "周五")],
    [6, wsT("ws.wd_sat", "周六")],
  ];
  return `<div class="ws-sched-box">
    <label class="switch cfg-enable"><input type="checkbox" id="wsFormSchedEnabled"${enabled ? " checked" : ""}>
      <span>${wsEsc(wsT("ws.sched_enabled", "启用定时扫描"))}</span></label>
    <p class="ws-help">${wsEsc(wsT("ws.sched_help", "按周期自动对该目标发起 Nuclei 扫描；最短间隔 15 分钟。保存后立即生效。"))}</p>
    <div class="cfg-form-row">
      <div class="field"><label>${wsEsc(wsT("ws.sched_kind", "周期"))}</label>
        <div class="select-wrap"><select id="wsFormSchedKind">
          <option value="interval"${kind === "interval" ? " selected" : ""}>${wsEsc(wsT("ws.kind_interval", "间隔"))}</option>
          <option value="daily"${kind === "daily" ? " selected" : ""}>${wsEsc(wsT("ws.kind_daily", "每天"))}</option>
          <option value="weekly"${kind === "weekly" ? " selected" : ""}>${wsEsc(wsT("ws.kind_weekly", "每周"))}</option>
        </select></div></div>
      <div class="field"><label>${wsEsc(wsT("ws.sched_at", "时间 HH:MM / 间隔分钟"))}</label>
        <input id="wsFormSchedAt" value="${wsEsc(String(at))}" placeholder="03:30 或 1440"></div>
      <div class="field"><label>${wsEsc(wsT("ws.sched_weekday", "星期（仅每周）"))}</label>
        <div class="select-wrap"><select id="wsFormSchedWeekday">
          ${weekdays.map(([v, lab]) => `<option value="${v}"${Number(weekday) === Number(v) ? " selected" : ""}>${wsEsc(lab)}</option>`).join("")}
        </select></div></div>
    </div>
  </div>`;
}

function wsSchedBadge(t) {
  const sc = t && t.schedule;
  if (!sc || !sc.enabled) {
    return `<span class="badge">${wsEsc(wsT("ws.sched_off", "未定时"))}</span>`;
  }
  let tip = sc.kind || "";
  if (sc.kind === "interval") tip = `${wsT("ws.kind_interval", "间隔")} ${sc.interval_min || "?"}m`;
  else if (sc.kind === "daily") tip = `${wsT("ws.kind_daily", "每天")} ${sc.at || ""}`;
  else if (sc.kind === "weekly") tip = `${wsT("ws.kind_weekly", "每周")} ${sc.at || ""}`;
  return `<span class="badge ok" title="${wsEsc(tip)}">${wsEsc(wsT("ws.sched_on", "定时"))}</span>`;
}

function wsTargetTagsHTML(tags) {
  const list = Array.isArray(tags) ? tags.filter(Boolean) : [];
  if (!list.length) return `<span class="muted">—</span>`;
  const maxShow = 3;
  const shown = list.slice(0, maxShow);
  const tip = list.map(wsTagLabel).join("、");
  let html = `<div class="ws-tag-chips" title="${wsEsc(tip)}">`;
  shown.forEach(tag => {
    html += `<span class="ws-tag-chip">${wsEsc(wsTagLabel(tag))}</span>`;
  });
  if (list.length > maxShow) {
    html += `<span class="ws-tag-chip more">+${list.length - maxShow}</span>`;
  }
  html += `</div>`;
  return html;
}

function wsTargetsHTML() {
  let html = `<div class="cfg-panel ws-card sec-panel">
    <div class="sec-panel-head">
      <div class="cfg-panel-title">${wsEsc(wsT("ws.targets", "扫描目标"))}</div>
      <span class="sec-panel-meta">${wsTargets.length}</span>
    </div>`;
  if (!wsTargets.length) {
    html += `<div class="sec-empty">
      <div class="sec-empty-ico" aria-hidden="true"></div>
      <h4>${wsEsc(wsT("ws.no_targets_title", "暂无目标"))}</h4>
      <p>${wsEsc(wsT("ws.no_targets", "添加公网 https 站点即可开始；也可用 example.com 验证引擎。"))}</p>
    </div>`;
  } else {
    html += `<div class="ws-target-list">`;
    wsTargets.forEach(t => {
      const last = t.last_scan_at ? wsFmtTime(t.last_scan_at) : "—";
      const profile = wsMatchProfile(t.tags || []);
      const profileLab = {
        quick: wsT("ws.profile_quick", "快速"),
        standard: wsT("ws.profile_standard", "标准"),
        deep: wsT("ws.profile_deep", "深度"),
        custom: wsT("ws.profile_custom", "自定义"),
      }[profile] || profile;
      html += `<div class="ws-target-card">
        <div class="ws-target-main">
          <div class="ws-target-top">
            <div class="ws-target-name" title="${wsEsc(t.name)}">${wsEsc(t.name)}</div>
            <div class="ws-meta-badges">
              ${t.enabled ? `<span class="badge ok">${wsEsc(wsT("ws.on", "启用"))}</span>` : `<span class="badge">${wsEsc(wsT("ws.off", "停用"))}</span>`}
              <span class="badge info">${wsEsc(wsAuthLabel(t.auth_type))}</span>
              ${wsSchedBadge(t)}
              <span class="badge">${wsEsc(profileLab)}</span>
            </div>
          </div>
          <div class="ws-url mono" title="${wsEsc(t.base_url)}">${wsEsc(t.base_url)}</div>
          <div class="ws-target-foot">
            ${wsTargetTagsHTML(t.tags)}
            <span class="mono muted ws-last-scan">${wsEsc(wsT("ws.last_scan", "上次"))} ${wsEsc(last)}</span>
          </div>
        </div>
        <div class="ws-actions">
          <button class="btn sm primary" data-wsscan="${wsEsc(t.id)}" ${wsBusy ? "disabled" : ""}>${wsEsc(wsT("ws.scan_now", "扫描"))}</button>
          <button class="btn sm" data-wsedit="${wsEsc(t.id)}">${wsEsc(wsT("common.edit", "编辑"))}</button>
          <button class="btn sm danger ghost" data-wsdel="${wsEsc(t.id)}">${wsEsc(wsT("common.delete", "删除"))}</button>
        </div>
      </div>`;
    });
    html += `</div>`;
  }
  return html + `</div>`;
}

function wsHistoryHTML() {
  let html = `<div class="cfg-panel ws-card sec-panel">
    <div class="sec-panel-head">
      <div class="cfg-panel-title">${wsEsc(wsT("ws.history", "扫描历史"))}</div>
      <span class="sec-panel-meta">${Math.min(wsScans.length, 30)}${wsScans.length > 30 ? "+" : ""}</span>
    </div>`;
  if (!wsScans.length) {
    html += `<div class="sec-empty slim"><p>${wsEsc(wsT("ws.history_empty", "尚无扫描历史"))}</p></div>`;
  } else {
    html += `<div class="nf-table-wrap"><table class="data-table ws-history-table hs-table-compact"><thead><tr>
      <th>${wsEsc(wsT("ws.batch", "批次"))}</th>
      <th class="col-risk">${wsEsc(wsT("ws.status", "状态"))}</th>
      <th class="col-num">${wsEsc(wsT("ws.findings", "命中"))}</th>
      <th class="col-time">${wsEsc(wsT("ws.time", "时间"))}</th>
      <th></th>
    </tr></thead><tbody id="wsHistoryBody">`;
    html += wsHistoryRowsHTML();
    html += `</tbody></table></div>`;
  }
  return html + `</div>`;
}

function wsHistoryRowsHTML() {
  return wsScans.slice(0, 30).map(s => {
    const n = (s.findings || []).length;
    const active = wsSelected && wsSelected.id === s.id ? " active-row" : "";
    const sum = s.summary || {};
    const risk = (sum.critical || 0) + (sum.high || 0);
    const trig = s.trigger === "schedule"
      ? `<span class="badge ok">${wsEsc(wsT("ws.trigger_sched", "定时"))}</span>`
      : `<span class="badge">${wsEsc(wsT("ws.trigger_manual", "手动"))}</span>`;
    const cancelBtn = s.status === "running"
      ? `<button type="button" class="btn sm danger" data-ws-cancel="${wsEsc(s.id)}">${wsEsc(wsT("ws.cancel_scan", "取消"))}</button>`
      : "";
    return `<tr class="sec-row${active}" data-wsscanid="${wsEsc(s.id)}" title="${wsEsc(s.id)}">
      <td><div class="sec-batch">${wsEsc(wsScanLabel(s))}</div>
        <div class="mono muted sec-batch-id">${wsEsc(wsScanIdShort(s.id))} · ${trig}</div></td>
      <td>${wsStatusBadge(s.status)}</td>
      <td><strong>${n}</strong>${risk ? ` <span class="badge high">${risk} ${wsEsc(wsT("ws.high_plus", "高危+"))}</span>` : ""}</td>
      <td class="mono muted">${wsEsc(wsFmtTime(s.finished_at || s.started_at))}</td>
      <td>${cancelBtn}</td>
    </tr>`;
  }).join("");
}

function wsLatestSummary() {
  // Latest completed scan per target — avoid summing the same target N times.
  const latest = new Map();
  wsScans.filter(s => s.status === "completed").forEach(s => {
    const prev = latest.get(s.target_id);
    if (!prev || (s.finished_at || 0) > (prev.finished_at || 0)) latest.set(s.target_id, s);
  });
  let crit = 0, high = 0, hits = 0;
  latest.forEach(s => {
    const open = (s.findings || []).filter(f => {
      const st = String(f.status || "open").toLowerCase();
      return st !== "resolved" && st !== "false_positive" && st !== "ack";
    });
    const list = open.length ? open : (s.findings || []);
    hits += list.length;
    list.forEach(f => {
      const sev = String(f.severity || "").toLowerCase();
      if (sev === "critical") crit++;
      else if (sev === "high") high++;
    });
    if (!list.length) {
      crit += (s.summary && s.summary.critical) || 0;
      high += (s.summary && s.summary.high) || 0;
    }
  });
  return { crit, high, hits, running: wsScans.filter(s => s.status === "running").length };
}

function wsFindingMatchesFilter(f, level) {
  if (!f) return false;
  const st = String(f.status || "open").toLowerCase();
  if (st === "resolved" || st === "false_positive" || st === "ack" || st === "accepted") return false;
  const sev = String(f.severity || "").toLowerCase();
  if (level === "critical") return sev === "critical" || sev === "crit";
  if (level === "high") return sev === "high";
  return sev === "critical" || sev === "crit" || sev === "high";
}

function wsPendingBannerHTML() {
  if (!wsPendingFilter || !wsPendingFilter.level) return "";
  const label = wsPendingFilter.level === "critical"
    ? wsT("ws.filter_crit", "仅显示开放危急项")
    : wsPendingFilter.level === "high"
      ? wsT("ws.filter_high", "仅显示开放高危项")
      : wsT("ws.filter_open", "仅显示开放危急/高危项");
  return `<div class="sec-notice sec-notice-slim">${wsEsc(label)}
    <button type="button" class="btn sm ghost" data-ws="clear-filter">${wsEsc(wsT("ws.clear_filter", "清除筛选"))}</button></div>`;
}

async function renderWebSecurity() {
  const el = $("webSecurityPanel");
  if (!el) return;
  if (typeof window.secConsumePendingFilter === "function") {
    const f = window.secConsumePendingFilter("web-security");
    if (f) wsPendingFilter = f;
  }
  el.innerHTML = `<div class="loading-dots">${wsT("common.loading", "加载中...")}</div>`;
  try {
    const [t, s, cfg, eng] = await Promise.all([
      wsFetchJSON(`${API}/security/web/targets`),
      wsFetchJSON(`${API}/security/web/scans?limit=40`),
      wsFetchJSON(`${API}/security/web/config`).catch(() => null),
      wsFetchJSON(`${API}/security/web/engine`).catch(() => null),
    ]);
    wsTargets = t.targets || [];
    wsScans = s.scans || [];
    wsCfg = cfg;
    wsEngine = eng;
    if (wsCfg && wsCfg.severity) {
      wsSevPick = String(wsCfg.severity).split(",").map(x => x.trim()).filter(Boolean);
    }
    if (wsPendingFilter && wsPendingFilter.level && !wsSelected) {
      const hit = (wsScans || []).find(s => s.status === "completed" && (s.findings || []).some(f => wsFindingMatchesFilter(f, wsPendingFilter.level)));
      if (hit) wsSelected = hit;
    }
    paintWebSecurity();
    wsMaybePoll();
  } catch (err) {
    el.innerHTML = `<div class="empty-state"><h4>${wsEsc(wsT("ws.load_failed", "加载失败"))}</h4><p>${wsEsc(err.message || err)}</p></div>`;
  }
}

function paintWebSecurity() {
  const el = $("webSecurityPanel");
  if (!el) return;
  const sum = wsLatestSummary();
  let html = `<div class="ws-shell sec-shell">`;
  html += `<div class="sec-notice sec-notice-slim">${wsEsc(wsT("ws.notice", "Nuclei 仅扫 http(s)；支持定时（≥15 分钟）。默认禁私网；0 命中≠绝对安全。"))}</div>`;
  html += wsPendingBannerHTML();
  html += wsEngineBarHTML();
  html += `<div class="sec-metrics">
    <div class="sec-metric"><b>${wsTargets.length}</b><span>${wsEsc(wsT("ws.stat_targets", "目标"))}</span></div>
    <div class="sec-metric"><b>${wsScans.length}</b><span>${wsEsc(wsT("ws.stat_scans", "历史"))}</span></div>
    <div class="sec-metric crit"><b>${sum.crit}</b><span>${wsEsc(wsT("ws.sev_critical", "危急"))}</span></div>
    <div class="sec-metric high"><b>${sum.high}</b><span>${wsEsc(wsT("ws.sev_high", "高危"))}</span></div>
    <div class="sec-metric"><b>${sum.running}</b><span>${wsEsc(wsT("ws.stat_running", "进行中"))}</span></div>
  </div>`;
  wsCapturePick();
  wsInitPickDefaults(wsTargets);
  const wsSelN = wsSelectedScanIds().length;
  html += `<div class="sec-command">
    <div class="sec-command-pick">
      <div class="sec-command-label">
        <span class="sec-command-title">${wsEsc(wsT("ws.target", "目标"))}</span>
        <span class="sec-command-hint">${wsEsc(wsT("ws.target_pick_hint", "树形多选 · 显示名称与 URL"))}</span>
      </div>
      ${wsPickTreeHTML()}
    </div>
    <div class="sec-command-side">
      <div class="sec-command-cta">
        <button class="btn primary sec-scan-btn" data-ws="scan" ${wsBusy ? "disabled" : ""}>${wsEsc(wsT("ws.scan_selected", "扫描选中"))}</button>
        <span class="sec-sel-chip" title="${wsEsc(wsT("ws.scan_selected", "扫描选中"))}">${wsSelN} ${wsEsc(wsT("ws.selected_n", "已选"))}</span>
      </div>
      <div class="sec-command-tools">
        <button class="btn" data-ws="add">${wsEsc(wsT("ws.add_target", "添加目标"))}</button>
        <button class="btn ghost" data-ws="refresh">${wsEsc(wsT("common.refresh", "刷新"))}</button>
        <div class="act-menu act-menu-ai">
          <button type="button" class="btn sm act-menu-trigger" aria-haspopup="true" aria-expanded="false"><span data-i18n="ui.ai_menu">AI</span><span class="act-menu-caret">▾</span></button>
          <div class="act-menu-panel" hidden role="menu">
            <div class="act-menu-hint">${wsEsc(wsT("ws.ai_menu_hint", "基于当前扫描报告"))}</div>
            <button type="button" role="menuitem" data-ws="ai-diag">${wsEsc(wsT("ws.ai_diag", "AI 研判"))}<span class="act-menu-sub">${wsEsc(wsT("ws.ai_diag_tip", "研判风险、优先级与疑似误报"))}</span></button>
            <button type="button" role="menuitem" data-ws="ai-rem">${wsEsc(wsT("ws.ai_rem", "AI 修复"))}<span class="act-menu-sub">${wsEsc(wsT("ws.ai_rem_tip", "生成可确认执行的修复/复扫计划"))}</span></button>
          </div>
        </div>
        ${wsExportMenuHTML(false)}
      </div>
    </div>
  </div>`;
  html += wsCfgPanelHTML();
  if (typeof sfPanelHTML === "function") html += sfPanelHTML();
  html += `<div class="ws-layout">
    <div class="ws-col-main">${wsTargetsHTML()}${wsHistoryHTML()}</div>
    <div class="ws-col-side"><div id="wsDetail" class="cfg-panel ws-card ws-detail sec-panel"></div></div>
  </div>`;
  html += wsFormPanelHTML();
  html += `</div>`;
  el.innerHTML = html;
  wsBindShell(el);
  document.querySelectorAll("#webSecurityPanel .hs-pick-tree-wrap").forEach(wsBindPickTree);
  if (wsSelected) wsPaintDetail(wsSelected);
  else wsPaintDetailEmpty();
  if (wsShowForm) {
    wsSyncProfileUI();
    const modal = el.querySelector(".ws-form-modal");
    try { if (modal) modal.scrollTop = 0; } catch (_) {}
  }
}

function wsCaptureFormDraft() {
  const schedKind = ($("wsFormSchedKind") && $("wsFormSchedKind").value) || "weekly";
  const schedAt = ($("wsFormSchedAt") && $("wsFormSchedAt").value) || "03:30";
  const schedule = {
    enabled: !!($("wsFormSchedEnabled") && $("wsFormSchedEnabled").checked),
    kind: schedKind,
    weekday: parseInt(($("wsFormSchedWeekday") && $("wsFormSchedWeekday").value) || "0", 10) || 0,
  };
  if (schedKind === "interval") schedule.interval_min = parseInt(schedAt, 10) || 1440;
  else schedule.at = schedAt;
  const draft = {
    id: wsEditTarget && wsEditTarget.id,
    name: ($("wsFormName") && $("wsFormName").value) || "",
    base_url: ($("wsFormURL") && $("wsFormURL").value) || "",
    enabled: !!($("wsFormEnabled") && $("wsFormEnabled").checked),
    auth_type: wsAuthType,
    auth_user: ($("wsFormUser") && $("wsFormUser").value) || (wsEditTarget && wsEditTarget.auth_user) || "",
    auth_login_url: ($("wsFormLoginURL") && $("wsFormLoginURL").value) || (wsEditTarget && wsEditTarget.auth_login_url) || "",
    auth_method: ($("wsFormAuthMethod") && $("wsFormAuthMethod").value) || (wsEditTarget && wsEditTarget.auth_method) || "POST",
    auth_header: ($("wsFormHeaders") && $("wsFormHeaders").value) || "",
    auth_body: ($("wsFormBody") && $("wsFormBody").value) || "",
    auth_content_type: ($("wsFormCType") && $("wsFormCType").value) || "",
    tags: wsTagPick.slice(),
    schedule,
    allow_private: !!(wsEditTarget && wsEditTarget.allow_private),
    templates: (wsEditTarget && wsEditTarget.templates) || [],
    include: (wsEditTarget && wsEditTarget.include) || [],
    exclude: (wsEditTarget && wsEditTarget.exclude) || [],
  };
  if (wsEditTarget) {
    if (!draft.auth_header) draft.auth_header = wsEditTarget.auth_header || "";
    if (!draft.auth_body) draft.auth_body = wsEditTarget.auth_body || "";
  }
  return draft;
}

function wsOnShellClick(ev) {
  const el = $("webSecurityPanel");
  if (!el) return;
  const t = ev.target;
  if (!(t instanceof Element) || !el.contains(t)) return;

  const exportOpt = t.closest("[data-wsexport]");
  if (exportOpt && el.contains(exportOpt)) {
    ev.preventDefault();
    ev.stopPropagation();
    document.querySelectorAll("#wsExportMenu.show").forEach(m => m.classList.remove("show"));
    wsDoExport(exportOpt.dataset.wsexport);
    return;
  }

  const profileBtn = t.closest("[data-wsprofile]");
  if (profileBtn && el.contains(profileBtn)) {
    ev.preventDefault();
    ev.stopPropagation();
    wsApplyProfile(profileBtn.dataset.wsprofile);
    return;
  }

  const tagBtn = t.closest("[data-wstag]");
  if (tagBtn && el.contains(tagBtn)) {
    ev.preventDefault();
    ev.stopPropagation();
    const id = tagBtn.dataset.wstag;
    if (!id) return;
    if (wsTagPick.includes(id)) wsTagPick = wsTagPick.filter(x => x !== id);
    else wsTagPick = wsTagPick.concat(id);
    wsProfilePick = wsMatchProfile(wsTagPick);
    tagBtn.classList.toggle("on");
    wsSyncProfileUI();
    return;
  }

  const sevBtn = t.closest("[data-wssev]");
  if (sevBtn && el.contains(sevBtn)) {
    ev.preventDefault();
    ev.stopPropagation();
    const id = sevBtn.dataset.wssev;
    if (!id) return;
    if (wsSevPick.includes(id)) wsSevPick = wsSevPick.filter(x => x !== id);
    else wsSevPick = wsSevPick.concat(id);
    sevBtn.classList.toggle("on");
    return;
  }

  const scanBtn = t.closest("[data-wsscan]");
  if (scanBtn && el.contains(scanBtn)) {
    ev.preventDefault();
    ev.stopPropagation();
    wsScanNow(scanBtn.dataset.wsscan);
    return;
  }
  const editBtn = t.closest("[data-wsedit]");
  if (editBtn && el.contains(editBtn)) {
    ev.preventDefault();
    ev.stopPropagation();
    wsStartEdit(editBtn.dataset.wsedit);
    return;
  }
  const delBtn = t.closest("[data-wsdel]");
  if (delBtn && el.contains(delBtn)) {
    ev.preventDefault();
    ev.stopPropagation();
    wsDelete(delBtn.dataset.wsdel);
    return;
  }

  const feedBtn = t.closest("[data-sf]");
  if (feedBtn && el.contains(feedBtn)) {
    ev.preventDefault();
    ev.stopPropagation();
    sfAction(feedBtn.dataset.sf, feedBtn);
    return;
  }

  const actEl = t.closest("[data-ws]");
  if (actEl && el.contains(actEl)) {
    const act = actEl.dataset.ws;
    // Backdrop closes only when the mask itself is the click target.
    if (act === "cancel-form-mask" && t !== actEl) return;
    ev.preventDefault();
    ev.stopPropagation();
    wsAction(act);
    return;
  }

  const cancelBtn = t.closest("[data-ws-cancel]");
  if (cancelBtn && el.contains(cancelBtn)) {
    ev.preventDefault();
    ev.stopPropagation();
    wsCancelScan(cancelBtn.dataset.wsCancel);
    return;
  }

  const row = t.closest("tr[data-wsscanid]");
  if (row && el.contains(row)) wsLoadScan(row.dataset.wsscanid);
}

async function wsCancelScan(id) {
  try {
    await wsFetchJSON(`${API}/security/web/scans/${encodeURIComponent(id)}/cancel`, { method: "POST" });
    if (typeof toast === "function") toast(wsT("ws.cancel_ok", "已取消扫描"), "ok");
    await renderWebSecurity();
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

function wsBindShell(el) {
  if (!el._wsClickBound) {
    el._wsClickBound = true;
    el.addEventListener("click", wsOnShellClick);
  }
  if (!el._wsKeyBound) {
    el._wsKeyBound = true;
    document.addEventListener("keydown", ev => {
      if (ev.key === "Escape" && wsShowForm && document.querySelector("#view-web-security.active")) {
        wsAction("cancel-form");
      }
    });
  }
  const authSel = $("wsFormAuth");
  if (authSel) {
    authSel.onchange = () => {
      wsAuthType = authSel.value || "none";
      wsEditTarget = wsCaptureFormDraft();
      paintWebSecurity();
    };
  }
}

function wsPaintDetailEmpty() {
  const box = $("wsDetail");
  if (!box) return;
  box.innerHTML = `<div class="sec-empty ws-detail-empty">
    <div class="sec-empty-ico" aria-hidden="true"></div>
    <h4>${wsEsc(wsT("ws.pick_scan", "选择一条扫描记录"))}</h4>
    <p>${wsEsc(wsT("ws.pick_scan_hint", "在上方勾选目标后「扫描选中」，或在左侧历史中点击批次，即可查看命中并导出报告。"))}</p>
  </div>`;
}

function wsAction(act) {
  if (act === "refresh") return renderWebSecurity();
  if (act === "scan") return wsRunScanSelected();
  if (act === "add") {
    wsEditTarget = null; wsAuthType = "none";
    wsApplyProfile("standard");
    wsShowForm = true; wsShowCfg = false;
    return paintWebSecurity();
  }
  if (act === "cancel-form" || act === "cancel-form-mask") {
    wsShowForm = false; wsEditTarget = null; wsAuthType = "none";
    return paintWebSecurity();
  }
  if (act === "save-target") return wsSaveTarget();
  if (act === "import-openapi") return wsImportOpenAPI();
  if (act === "cfg") {
    wsShowCfg = !wsShowCfg; wsShowForm = false;
    if (typeof SF_OPEN !== "undefined" && wsShowCfg) { SF_OPEN = false; sfStopPoll(); }
    return paintWebSecurity();
  }
  if (act === "feeds") {
    if (typeof sfToggle !== "function") {
      if (typeof toast === "function") toast(wsT("ws.feeds_unavailable", "情报源模块未加载，请刷新页面"), "err");
      return;
    }
    return sfToggle();
  }
  if (act === "toggle-packs") { wsShowPacks = !wsShowPacks; return paintWebSecurity(); }
  if (act === "save-cfg") return wsSaveCfg();
  if (act === "refresh-tpl") return wsRefreshTemplates();
  if (act === "ai" || act === "ai-diag") return wsAI("diagnosis");
  if (act === "ai-rem") return wsAI("remediation");
  if (act === "clear-filter") { wsPendingFilter = null; return paintWebSecurity(); }
  if (act === "export-toggle") {
    const menu = $("wsExportMenu");
    if (menu) menu.classList.toggle("show");
  }
}

function wsStartEdit(id) {
  const t = (wsTargets || []).find(x => x.id === id);
  if (!t) {
    if (typeof toast === "function") toast(wsT("ws.target_missing", "目标不存在或已删除"), "err");
    return;
  }
  wsEditTarget = Object.assign({}, t);
  wsAuthType = t.auth_type || "none";
  wsTagPick = (t.tags && t.tags.length) ? t.tags.slice() : WS_PROFILE.standard.slice();
  wsProfilePick = wsMatchProfile(wsTagPick);
  wsShowForm = true;
  wsShowCfg = false;
  paintWebSecurity();
}

// The refresh endpoint now returns as soon as the background job starts, so open
// the feed panel and let it show live progress instead of leaving the operator
// staring at a toast for ten minutes.
async function wsRefreshTemplates() {
  try {
    await wsFetchJSON(`${API}/security/web/engine/refresh`, { method: "POST" });
    if (typeof toast === "function") toast(wsT("ws.tpl_updating", "模板库更新已在后台启动，进度见「情报源」面板"), "ok");
    if (typeof SF_OPEN !== "undefined") {
      SF_OPEN = true;
      wsShowCfg = false;
      paintWebSecurity();
      if (typeof sfLoad === "function") await sfLoad();
      if (typeof sfStartPoll === "function") sfStartPoll();
      paintWebSecurity();
      try {
        const panel = document.querySelector("#webSecurityPanel .sf-panel");
        if (panel && panel.scrollIntoView) panel.scrollIntoView({ behavior: "smooth", block: "nearest" });
      } catch (_) {}
    } else {
      paintWebSecurity();
    }
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

async function wsSaveTarget() {
  const name = ($("wsFormName") && $("wsFormName").value || "").trim();
  const base = ($("wsFormURL") && $("wsFormURL").value || "").trim();
  const tags = wsTagPick.slice();
  const authType = ($("wsFormAuth") && $("wsFormAuth").value) || "none";
  if (!name || !base) {
    if (typeof toast === "function") toast(wsT("ws.form_required", "请填写名称与地址"), "err");
    return;
  }
  if (!tags.length) {
    if (typeof toast === "function") toast(wsT("ws.tags_required", "请至少选择一个模板范围"), "err");
    return;
  }
  const schedOn = !!($("wsFormSchedEnabled") && $("wsFormSchedEnabled").checked);
  const schedKind = ($("wsFormSchedKind") && $("wsFormSchedKind").value) || "weekly";
  const schedAtRaw = ($("wsFormSchedAt") && $("wsFormSchedAt").value || "").trim();
  const schedule = { enabled: schedOn, kind: schedKind };
  if (schedKind === "interval") {
    schedule.interval_min = Math.max(15, parseInt(schedAtRaw, 10) || 1440);
  } else {
    schedule.at = schedAtRaw || "03:30";
  }
  if (schedKind === "weekly") {
    schedule.weekday = parseInt(($("wsFormSchedWeekday") && $("wsFormSchedWeekday").value) || "0", 10) || 0;
  }
  const body = {
    id: wsEditTarget && wsEditTarget.id || undefined,
    name,
    base_url: base,
    enabled: !!($("wsFormEnabled") && $("wsFormEnabled").checked),
    tags,
    auth_type: authType,
    auth_user: ($("wsFormUser") && $("wsFormUser").value || "").trim(),
    auth_pass: ($("wsFormPass") && $("wsFormPass").value || "").trim(),
    auth_header: ($("wsFormHeaders") && $("wsFormHeaders").value || "").trim(),
    auth_body: ($("wsFormBody") && $("wsFormBody").value || "").trim(),
    auth_login_url: ($("wsFormLoginURL") && $("wsFormLoginURL").value || "").trim(),
    auth_method: ($("wsFormAuthMethod") && $("wsFormAuthMethod").value || "POST").trim(),
    auth_content_type: ($("wsFormCType") && $("wsFormCType").value || "").trim(),
    schedule,
    // Preserve advanced fields that the form does not edit, so save won't wipe them.
    allow_private: !!(wsEditTarget && wsEditTarget.allow_private),
    templates: (wsEditTarget && wsEditTarget.templates) || [],
    include: (wsEditTarget && wsEditTarget.include) || [],
    exclude: (wsEditTarget && wsEditTarget.exclude) || [],
  };
  // Keep previous secrets when editing an existing target and fields left blank / masked.
  if (wsEditTarget && wsEditTarget.id) {
    if (!body.auth_pass) body.auth_pass = "********";
    if (!body.auth_header && (wsEditTarget.auth_header || "").includes("********")) body.auth_header = "********";
    if (!body.auth_body && ((wsEditTarget.auth_body || "").includes("********") || (wsEditTarget.auth_body || "").includes("_masked"))) {
      body.auth_body = "********";
    }
  }
  try {
    const url = wsEditTarget && wsEditTarget.id
      ? `${API}/security/web/targets/` + encodeURIComponent(wsEditTarget.id)
      : `${API}/security/web/targets`;
    const method = wsEditTarget && wsEditTarget.id ? "PUT" : "POST";
    await wsFetchJSON(url, {
      method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
    });
    wsShowForm = false;
    wsEditTarget = null;
    wsAuthType = "none";
    if (typeof toast === "function") toast(wsT("toast.saved", "已保存"), "ok");
    renderWebSecurity();
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

async function wsImportOpenAPI() {
  const id = wsEditTarget && wsEditTarget.id;
  if (!id) {
    if (typeof toast === "function") toast(wsT("ws.openapi_need_save", "请先保存目标后再导入 OpenAPI"), "err");
    return;
  }
  const spec = ($("wsFormOpenAPI") && $("wsFormOpenAPI").value) || "";
  if (!String(spec).trim()) {
    if (typeof toast === "function") toast("请粘贴 OpenAPI JSON", "err");
    return;
  }
  try {
    const j = await wsFetchJSON(`${API}/security/web/targets/import-openapi`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        target_id: id,
        base_url: ($("wsFormURL") && $("wsFormURL").value) || wsEditTarget.base_url || "",
        spec,
        replace: false,
      }),
    });
    if (typeof toast === "function") toast(`已导入 ${j.imported || 0} 条 URL（合计 ${j.url_count || 0}）`, "ok");
    wsEditTarget = j.target || wsEditTarget;
    renderWebSecurity();
    // reopen edit with updated scan_urls
    if (wsEditTarget && wsEditTarget.id) wsStartEdit(wsEditTarget.id);
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

async function wsSaveCfg() {
  const status = $("wsCfgStatus");
  const sev = (wsSevPick.length ? wsSevPick : ["critical", "high", "medium", "low", "info"]).join(",");
  const body = {
    nuclei_path: ($("wsCfgPath") && $("wsCfgPath").value) || "nuclei",
    severity: sev,
    timeout_sec: parseInt(($("wsCfgTimeout") && $("wsCfgTimeout").value) || "900", 10),
    rate_limit: parseInt(($("wsCfgRate") && $("wsCfgRate").value) || "120", 10),
    allow_private: !!($("wsCfgPrivate") && $("wsCfgPrivate").checked),
    update_templates: !!($("wsCfgUpdate") && $("wsCfgUpdate").checked),
    disable_builtin_checks: $("wsCfgBuiltin") ? !$("wsCfgBuiltin").checked : !!(wsCfg && wsCfg.disable_builtin_checks),
    templates_dir: (wsCfg && wsCfg.templates_dir) || "",
    concurrency: parseInt(($("wsCfgConc") && $("wsCfgConc").value) || ((wsCfg && wsCfg.concurrency) || 25), 10),
    scan_concurrency: parseInt(($("wsCfgScanConc") && $("wsCfgScanConc").value) || ((wsCfg && wsCfg.scan_concurrency) || 3), 10),
    auto_ai_summary: !!($("wsCfgAISummary") && $("wsCfgAISummary").checked),
  };
  try {
    wsCfg = await wsFetchJSON(`${API}/security/web/config`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
    });
    wsEngine = await wsFetchJSON(`${API}/security/web/engine?refresh=1`).catch(() => wsEngine);
    if (status) { status.textContent = wsT("toast.saved", "已保存"); status.className = "cfg-status ok"; }
    if (typeof toast === "function") toast(wsT("toast.saved", "已保存"), "ok");
  } catch (e) {
    if (status) { status.textContent = e.message; status.className = "cfg-status err"; }
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

async function wsDelete(id) {
  if (!confirm(wsT("common.confirm_delete", "确认删除？"))) return;
  try {
    await wsFetchJSON(`${API}/security/web/targets/` + encodeURIComponent(id), { method: "DELETE" });
    renderWebSecurity();
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

async function wsScanNow(id) {
  wsBusy = true;
  paintWebSecurity();
  try {
    const d = await wsFetchJSON(`${API}/security/web/targets/` + encodeURIComponent(id) + "/scan", { method: "POST" });
    wsSelected = d.scan || d;
    if (typeof toast === "function") toast(wsT("ws.scan_started", "扫描已启动，右侧查看进度"), "ok");
    wsScans = (await wsFetchJSON(`${API}/security/web/scans?limit=40`)).scans || [];
    paintWebSecurity();
    wsMaybePoll();
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  } finally {
    wsBusy = false;
    paintWebSecurity();
  }
}

async function wsRunScanSelected() {
  let ids = wsSelectedScanIds();
  if (!ids.length) {
    ids = (wsTargets || []).filter(t => t.enabled !== false).map(t => t.id);
    if (!ids.length) {
      if (typeof toast === "function") toast(wsT("ws.pick_target", "请选择目标"), "err");
      return;
    }
    if (typeof toast === "function") toast(wsT("ws.scan_all_enabled", "未勾选目标，将扫描全部启用目标"), "ok");
  }
  wsBusy = true;
  paintWebSecurity();
  let last = null;
  let ok = 0;
  const errors = [];
  try {
    for (const id of ids) {
      try {
        const d = await wsFetchJSON(`${API}/security/web/targets/` + encodeURIComponent(id) + "/scan", { method: "POST" });
        last = d.scan || d;
        ok++;
      } catch (e) {
        errors.push(String(e.message || e));
      }
    }
    if (last) wsSelected = last;
    wsScans = (await wsFetchJSON(`${API}/security/web/scans?limit=40`)).scans || [];
    if (typeof toast === "function") {
      if (ok) toast(wsT("ws.scan_started", "扫描已启动，右侧查看进度") + ` · ${ok}/${ids.length}`, "ok");
      if (errors.length) toast(errors[0], "err");
    }
    wsMaybePoll();
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  } finally {
    wsBusy = false;
    paintWebSecurity();
  }
}

function wsSoftRefresh(opts) {
  const full = !!(opts && opts.full);
  const selectedRunning = wsSelected && wsSelected.status === "running";
  if (!full && selectedRunning && !wsNeedsFullHistoryPaint()) {
    wsPatchRunningUI();
    return;
  }
  const body = $("wsHistoryBody");
  if (body) body.innerHTML = wsHistoryRowsHTML();
  // KPI running count
  const metrics = document.querySelectorAll("#webSecurityPanel .sec-metrics .sec-metric b");
  if (metrics.length >= 4) {
    const sum = wsLatestSummary();
    metrics[3].textContent = String(sum.running);
  }
  if (wsSelected) wsPaintDetail(wsSelected);
}

function wsCssEsc(id) {
  // 统一走 core.js 的 cssEsc（老内核里没有 CSS.escape，它自带退化实现）
  return cssEsc(String(id));
}

function wsNeedsFullHistoryPaint() {
  const body = $("wsHistoryBody");
  if (!body) return true;
  return (wsScans || []).slice(0, 30).some(s => !body.querySelector(`tr[data-wsscanid="${wsCssEsc(s.id)}"]`));
}

function wsPatchRunningUI() {
  const body = $("wsHistoryBody");
  if (body) {
    (wsScans || []).slice(0, 30).forEach(s => {
      const tr = body.querySelector(`tr[data-wsscanid="${wsCssEsc(s.id)}"]`);
      if (!tr) return;
      const statusTd = tr.children[1];
      if (statusTd) statusTd.innerHTML = wsStatusBadge(s.status);
      const actionTd = tr.lastElementChild;
      if (!actionTd) return;
      const hasCancel = !!actionTd.querySelector("[data-ws-cancel]");
      if (s.status === "running" && !hasCancel) {
        actionTd.innerHTML = `<button type="button" class="btn sm danger" data-ws-cancel="${wsEsc(s.id)}">${wsEsc(wsT("ws.cancel_scan", "取消"))}</button>`;
      } else if (s.status !== "running" && hasCancel) {
        actionTd.innerHTML = "";
      }
    });
  }
  const metrics = document.querySelectorAll("#webSecurityPanel .sec-metrics .sec-metric b");
  if (metrics.length >= 4) {
    const sum = wsLatestSummary();
    metrics[3].textContent = String(sum.running);
  }
  if (wsSelected && wsSelected.status === "running") {
    const box = $("wsDetail");
    if (box && !box.querySelector(".ws-progress")) wsPaintDetail(wsSelected);
  }
}

function wsMaybePoll() {
  if (wsPollTimer) { clearInterval(wsPollTimer); wsPollTimer = null; }
  if (!(wsScans || []).some(s => s.status === "running") && !(wsSelected && wsSelected.status === "running")) return;
  let lastSig = "";
  wsPollTimer = setInterval(async () => {
    if (!document.querySelector("#view-web-security.active")) {
      clearInterval(wsPollTimer); wsPollTimer = null; return;
    }
    try {
      const prevStatus = wsSelected && wsSelected.status;
      wsScans = (await wsFetchJSON(`${API}/security/web/scans?limit=40`)).scans || [];
      const sig = (wsScans || []).map(s => `${s.id}:${s.status}:${s.finished_at || 0}`).join("|");
      const changed = sig !== lastSig;
      if (changed) lastSig = sig;
      if (wsSelected && wsSelected.id) {
        const row = (wsScans || []).find(s => s.id === wsSelected.id);
        if (row && row.status === "running") {
          wsSelected = Object.assign({}, wsSelected, row);
        } else if (row && prevStatus === "running" && row.status !== "running") {
          wsSelected = await wsFetchJSON(`${API}/security/web/scans/` + encodeURIComponent(wsSelected.id));
        } else if (row && row.status !== "running" && (!(wsSelected.findings || []).length)) {
          wsSelected = await wsFetchJSON(`${API}/security/web/scans/` + encodeURIComponent(wsSelected.id));
        }
      }
      const transitioned = prevStatus === "running" && wsSelected && wsSelected.status !== "running";
      if (transitioned || (changed && wsNeedsFullHistoryPaint())) {
        wsSoftRefresh({ full: true });
      } else if (changed || (wsSelected && wsSelected.status === "running")) {
        wsSoftRefresh();
      }
      if (!(wsScans || []).some(x => x.status === "running")) {
        clearInterval(wsPollTimer); wsPollTimer = null;
        wsEngine = await wsFetchJSON(`${API}/security/web/engine`).catch(() => wsEngine);
      }
    } catch (_) {}
  }, 3000);
}

async function wsLoadScan(id) {
  try {
    wsSelected = await wsFetchJSON(`${API}/security/web/scans/` + encodeURIComponent(id));
    wsPaintDetail(wsSelected);
    document.querySelectorAll("#webSecurityPanel tr.sec-row").forEach(tr => {
      tr.classList.toggle("active-row", tr.dataset.wsscanid === id);
    });
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

// wsOwaspPanel summarizes findings by OWASP Top 10 and by audit framework, so
// the result reads as a posture report rather than a raw template dump.
function wsOwaspPanel(scan) {
  const owasp = scan.owasp || {};
  const comp = scan.compliance || {};
  const cats = Object.keys(owasp).sort();
  const fws = Object.keys(comp).sort();
  if (!cats.length && !fws.length) return "";
  let html = `<div class="ws-owasp-panel">`;
  if (cats.length) {
    html += `<div class="cfg-panel-title">${wsEsc(wsT("ws.owasp", "OWASP Top 10 分布"))}</div>
      <div class="ws-owasp-chips">` +
      cats.map(c => `<span class="ws-owasp-chip"><b>${owasp[c]}</b> ${wsEsc(c)}</span>`).join("") +
      `</div>`;
  }
  if (fws.length) {
    html += `<div class="cfg-panel-title" style="margin-top:10px">${wsEsc(wsT("ws.compliance", "合规映射"))}</div>
      <div class="ws-owasp-chips">` +
      fws.map(f => `<span class="ws-owasp-chip"><b>${comp[f]}</b> ${wsEsc(f)}</span>`).join("") +
      `</div>`;
  }
  const engines = scan.engines || [];
  if (engines.length) {
    html += `<p class="ws-help" style="margin:8px 0 0">${wsEsc(wsT("ws.engines_used", "本次使用的检测引擎"))}: ${wsEsc(engines.join(" + "))}</p>`;
  }
  return html + `</div>`;
}

function wsFindingTags(f) {
  const parts = [];
  if (f.owasp) parts.push(`<span class="tag">${wsEsc(f.owasp)}</span>`);
  (f.compliance || []).slice(0, 3).forEach(c => {
    if (c.framework === "OWASP") return;
    parts.push(`<span class="tag" title="${wsEsc(c.title || "")}">${wsEsc(c.framework)} ${wsEsc(c.control)}</span>`);
  });
  if (!parts.length) return "";
  return `<div class="ws-finding-tags">${parts.join("")}</div>`;
}

function wsPaintDetail(scan) {
  const box = $("wsDetail");
  if (!box || !scan) return;
  if (scan.status === "running") {
    box.innerHTML = `<div class="cfg-panel-title">${wsEsc(wsScanLabel(scan))}</div>
      <div class="ws-progress"><div class="ws-progress-bar"></div></div>
      <p class="ws-help">${wsEsc(wsT("ws.scanning", "Nuclei 扫描进行中，完成后自动刷新命中结果…"))}</p>
      <p class="mono muted">${wsEsc(scan.base_url || "")}</p>`;
    return;
  }
  const rep = scan.report || {};
  const counts = scan.summary || rep.risk_counts || {};
  const trig = scan.trigger === "schedule"
    ? wsT("ws.trigger_sched", "定时")
    : wsT("ws.trigger_manual", "手动");
  const canExport = scan.status === "completed";
  let html = `<div class="cfg-panel-head ws-detail-head"><div>
      <div class="cfg-panel-title">${wsEsc(rep.title || wsT("ws.report", "扫描报告"))}</div>
      <p class="cfg-panel-desc">${wsEsc(wsScanLabel(scan))} · <code class="mono muted">${wsEsc(wsScanIdShort(scan.id))}</code>
        · <span class="badge">${wsEsc(trig)}</span></p>
      <p class="cfg-panel-desc mono muted">${wsEsc(scan.base_url || "")}</p>
    </div>
    <div class="ws-detail-actions">
      ${wsStatusBadge(scan.status)}
      ${canExport ? `<div class="act-menu act-menu-ai">
        <button type="button" class="btn sm act-menu-trigger" aria-haspopup="true" aria-expanded="false">AI<span class="act-menu-caret">▾</span></button>
        <div class="act-menu-panel" hidden role="menu">
          <button type="button" role="menuitem" data-ws="ai-diag">${wsEsc(wsT("ws.ai_diag", "AI 研判"))}</button>
          <button type="button" role="menuitem" data-ws="ai-rem">${wsEsc(wsT("ws.ai_rem", "AI 修复"))}</button>
        </div>
      </div>
      <button class="btn sm primary" data-ws="export-toggle">${wsEsc(wsT("ws.export", "导出报告"))}</button>` : ""}
    </div></div>`;
  if (scan.error) html += `<div class="sec-error-box">${wsEsc(scan.error)}</div>`;
  if (scan.baseline_diff) {
    const d = scan.baseline_diff;
    html += `<div class="hint" style="margin:8px 0">较上次：新增 <b>${d.added || 0}</b> · 消失 <b>${d.removed || 0}</b> · 恶化 <b>${d.worsened || 0}</b> · 缓解 <b>${d.improved || 0}</b></div>`;
  }
  if (scan.ai_summary) {
    html += `<div class="sec-remediation" style="margin:8px 0"><div class="cfg-panel-title">${wsEsc(wsT("ws.ai_summary", "AI 摘要"))}</div>
      <pre class="mono" style="white-space:pre-wrap;margin:0;font-size:12px">${wsEsc(scan.ai_summary)}</pre></div>`;
  }
  if (scan.engine_note) {
    html += `<div class="hint" style="margin:8px 0">${wsEsc(scan.engine_note)}</div>`;
  }
  if (rep.executive) html += `<p class="ws-exec">${wsEsc(rep.executive)}</p>`;
  html += `<div class="sec-metrics compact">
    <div class="sec-metric crit"><b>${counts.critical || 0}</b><span>${wsEsc(wsT("ws.sev_critical", "危急"))}</span></div>
    <div class="sec-metric high"><b>${counts.high || 0}</b><span>${wsEsc(wsT("ws.sev_high", "高危"))}</span></div>
    <div class="sec-metric"><b>${counts.medium || 0}</b><span>${wsEsc(wsT("ws.sev_medium", "中危"))}</span></div>
    <div class="sec-metric"><b>${counts.low || 0}</b><span>${wsEsc(wsT("ws.sev_low", "低危"))}</span></div>
    <div class="sec-metric"><b>${counts.info || 0}</b><span>${wsEsc(wsT("ws.sev_info", "信息"))}</span></div>
  </div>`;
  html += wsOwaspPanel(scan);
  const findings = scan.findings || [];
  if (!findings.length) {
    html += `<div class="sec-empty slim">
      <h4>${wsEsc(scan.status === "failed" ? wsT("ws.scan_failed_title", "扫描未成功") : wsT("ws.no_findings", "未命中当前模板集"))}</h4>
      <p>${wsEsc(wsT("ws.no_findings_help", "可尝试：扩大模板范围（标准/深度）、勾选「信息」级、确认目标可从服务端访问。0 命中不代表绝对无风险。"))}</p>
    </div>`;
  } else {
    const filterLevel = wsPendingFilter && wsPendingFilter.level;
    const shown = [];
    findings.forEach((f, idx) => {
      if (filterLevel && !wsFindingMatchesFilter(f, filterLevel)) return;
      shown.push({ f, idx });
    });
    if (filterLevel && !shown.length) {
      html += `<div class="ws-empty-findings"><h4>${wsEsc(wsT("ws.no_filtered", "当前筛选下无待处置项"))}</h4></div>`;
    } else {
      html += `<div class="ws-findings">`;
      shown.forEach(({ f, idx }) => {
        html += `<article class="ws-finding">
          <header>${wsSevBadge(f.severity)}<strong>${wsEsc(f.name || f.template_id)}</strong>
            <code class="mono muted">${wsEsc(f.template_id || "")}</code>${wsFindingStatusSelect(f)}
            <button type="button" class="btn sm nf-ai-btn" data-ws-finding="${idx}" title="${wsEsc(wsT("ws.ai_finding_tip", "针对本条给出研判与修复建议"))}">${wsEsc(wsT("ws.ai_finding", "AI 建议"))}</button></header>
          <div class="mono sec-url" title="${wsEsc(f.url || f.matched_at || "")}">${wsEsc(f.url || f.matched_at || "")}</div>
          ${wsFindingTags(f)}
          ${f.description ? `<p>${wsEsc(f.description)}</p>` : ""}
          ${f.remediation ? `<div class="ws-fix"><span>${wsEsc(wsT("ws.remediation", "修复建议"))}</span>${wsEsc(f.remediation)}</div>` : ""}
        </article>`;
      });
      html += `</div>`;
    }
  }
  if ((rep.remediation || []).length) {
    html += `<div class="sec-remediation"><div class="cfg-panel-title">${wsEsc(wsT("ws.remediation", "汇总建议"))}</div><ul>`;
    rep.remediation.forEach(t => { html += `<li>${wsEsc(t)}</li>`; });
    html += `</ul></div>`;
  }
  box.innerHTML = html;
  box.querySelectorAll(".ws-finding-status").forEach(sel => {
    sel.addEventListener("change", () => {
      const tpl = sel.dataset.wstpl || "";
      const matcher = sel.dataset.wsmatcher || "";
      const url = sel.dataset.wsurl || "";
      const finding = (scan.findings || []).find(x =>
        (x.template_id || "") === tpl &&
        (x.matcher_name || "") === matcher &&
        ((x.url || x.matched_at || "") === url)
      ) || (scan.findings || []).find(x => (x.template_id || "") === tpl && (x.matcher_name || "") === matcher);
      if (finding) wsUpdateFindingStatus(finding, sel.value);
    });
  });
  box.querySelectorAll("[data-ws-finding]").forEach(b => b.addEventListener("click", ev => {
    ev.stopPropagation();
    const idx = parseInt(b.dataset.wsFinding, 10);
    wsAIFinding(scan, idx);
  }));
  box.querySelectorAll("[data-ws]").forEach(b => b.addEventListener("click", ev => {
    ev.stopPropagation();
    wsAction(b.dataset.ws);
  }));
  const expBtn = document.querySelector("#webSecurityPanel [data-ws=\"export-toggle\"]");
  if (expBtn) expBtn.disabled = !canExport;
}

function wsBuildReportModel(scan) {
  const rep = scan.report || {};
  const counts = scan.summary || rep.risk_counts || {};
  const trig = scan.trigger === "schedule"
    ? wsT("ws.trigger_sched", "定时")
    : wsT("ws.trigger_manual", "手动");
  const findings = scan.findings || [];
  const narrative = [
    `# ${wsT("ws.report_exec", "执行摘要")}`,
    "",
    rep.executive || wsT("ws.report_exec_fallback", "Web 漏洞扫描已完成。"),
    "",
    `# ${wsT("ws.remediation", "修复建议")}`,
    ...((rep.remediation || []).length
      ? rep.remediation.map(t => `- ${t}`)
      : [`- ${wsT("ws.no_remediation", "暂无额外修复建议；请结合漏洞明细表逐项处置。")}`]),
  ].join("\n");
  const rows = findings.map(f => [
    wsSevLabel(f.severity), f.template_id || "", f.name || "", f.url || f.matched_at || "",
    f.description || "", f.remediation || "", (f.tags || []).join(", "),
  ]);
  const sevRows = [
    [wsT("ws.sev_critical", "危急"), String(counts.critical || 0)],
    [wsT("ws.sev_high", "高危"), String(counts.high || 0)],
    [wsT("ws.sev_medium", "中危"), String(counts.medium || 0)],
    [wsT("ws.sev_low", "低危"), String(counts.low || 0)],
    [wsT("ws.sev_info", "信息"), String(counts.info || 0)],
    [wsT("ws.findings", "命中合计"), String(findings.length)],
  ];
  return {
    title: rep.title || (wsT("ws.report_title", "Web 漏洞扫描报告") + " — " + (scan.target_name || scan.base_url)),
    subtitle: wsT("ws.report_sub", "生成时间") + " " + new Date().toLocaleString() + " · " + wsScanLabel(scan),
    summaryTitle: wsT("ws.report_meta", "报告摘要"),
    narrativeTitle: wsT("ws.report_analysis", "分析结论与建议"),
    meta: [
      [wsT("ws.batch", "扫描批次"), wsScanLabel(scan)],
      [wsT("ws.name", "目标"), scan.target_name || ""],
      [wsT("ws.base_url", "目标地址"), scan.base_url || ""],
      [wsT("ws.trigger", "触发方式"), trig],
      [wsT("ws.status", "状态"), wsStatusLabel(scan.status)],
      [wsT("ws.sev_critical", "危急"), String(counts.critical || 0)],
      [wsT("ws.sev_high", "高危"), String(counts.high || 0)],
      [wsT("ws.sev_medium", "中危"), String(counts.medium || 0)],
      [wsT("ws.sev_low", "低危"), String(counts.low || 0)],
      [wsT("ws.findings", "命中数"), String(findings.length)],
      [wsT("ws.time", "完成时间"), wsFmtTime(scan.finished_at)],
    ],
    kpis: [
      [wsT("ws.sev_critical", "危急"), String(counts.critical || 0)],
      [wsT("ws.sev_high", "高危"), String(counts.high || 0)],
      [wsT("ws.sev_medium", "中危"), String(counts.medium || 0)],
      [wsT("ws.findings", "命中"), String(findings.length)],
    ],
    narrative,
    sections: [
      {
        title: wsT("ws.report_sev_sec", "严重度分布"),
        columns: [wsT("ws.severity", "严重度"), wsT("ws.report_count", "数量")],
        rows: sevRows,
      },
      {
        title: wsT("ws.findings_detail", "漏洞明细"),
        columns: [
          wsT("ws.severity", "严重度"), wsT("ws.template", "模板"), wsT("ws.name", "名称"),
          "URL", wsT("ws.description", "描述"), wsT("ws.remediation", "修复建议"), wsT("ws.tags", "标签"),
        ],
        rows: rows.length ? rows : [[wsT("ws.no_findings", "未命中当前模板集（不代表绝对无风险）"), "", "", "", "", "", ""]],
      },
    ],
    footer: wsT("ws.report_footer", "本报告由 AIOps 安全中心（Nuclei）自动生成，仅供运维与安全处置参考，不替代专业渗透测试。"),
    orientation: "landscape",
    rawJSON: {
      report_type: "web_security",
      generated_at: new Date().toISOString(),
      scan_id: scan.id,
      label: wsScanLabel(scan),
      target_id: scan.target_id,
      target_name: scan.target_name,
      base_url: scan.base_url,
      trigger: scan.trigger || "manual",
      status: scan.status,
      summary: counts,
      executive: rep.executive || "",
      remediation: rep.remediation || [],
      findings,
      started_at: scan.started_at,
      finished_at: scan.finished_at,
    },
  };
}

async function wsEnsureSelectedScan() {
  if (!wsSelected || !wsSelected.id) return null;
  if (wsSelected.status !== "completed") return null;
  try {
    wsSelected = await wsFetchJSON(`${API}/security/web/scans/` + encodeURIComponent(wsSelected.id));
  } catch (_) { /* use cached */ }
  return wsSelected && wsSelected.status === "completed" ? wsSelected : null;
}

async function wsDoExport(fmt) {
  const scan = await wsEnsureSelectedScan();
  if (!scan) {
    if (typeof toast === "function") toast(wsT("ws.pick_scan_done", "请先选择一条已完成的扫描"), "err");
    return;
  }
  try {
    const model = wsBuildReportModel(scan);
    const ok = await exportModel(model, fmt, "Web漏洞扫描报告_" + (scan.target_name || "target"));
    if (ok === false && typeof toast === "function") toast(wsT("ws.export_popup", "浏览器拦截了导出窗口，请允许弹窗后重试"), "err");
    else if (fmt !== "pdf" && typeof toast === "function") toast(wsT("toast.exported", "已导出"), "ok");
  } catch (e) {
    if (typeof toast === "function") toast(wsT("ws.export_fail", "导出失败") + "：" + (e.message || e), "err");
  }
}

function wsAIContext(scan, maxFindings) {
  const model = wsBuildReportModel(scan);
  const targetId = scan.target_id || "";
  return {
    targetId,
    text: (model.narrative + "\n\n" + JSON.stringify({
      target_id: targetId,
      target_name: scan.target_name,
      base_url: scan.base_url,
      meta: model.meta,
      findings: (scan.findings || []).slice(0, maxFindings || 40),
    }, null, 2)).slice(0, 14000),
  };
}

function wsAI(kind) {
  if (!wsSelected || (wsSelected.status !== "completed" && wsSelected.status !== "failed")) {
    if (typeof toast === "function") toast(wsT("ws.pick_scan_done", "请先选择一条已完成的扫描"), "err");
    return;
  }
  const mode = kind === "remediation" ? "remediation" : "diagnosis";
  const { targetId, text } = wsAIContext(wsSelected, 40);
  if (typeof openAIAssist !== "function") return;
  if (mode === "remediation") {
    openAIAssist({
      task: "web_vuln_remediation", mode: "analyze",
      title: wsT("ws.ai_rem_title", "AI · Web 漏洞修复计划"),
      context: text,
      applyLabel: wsT("ai.apply_actions", "应用建议动作"),
      applyTo: async (code) => {
        if (typeof window.applyOpsActionPlan !== "function") return false;
        return window.applyOpsActionPlan(code, {
          source: "web-security",
          targetId,
          refresh: () => renderWebSecurity(),
        });
      },
    });
    return;
  }
  openAIAssist({
    task: "web_vuln_diagnosis", mode: "analyze",
    title: wsT("ws.ai_diag_title", "AI · Web 漏洞研判"),
    context: text,
    hint: wsT("ws.ai_diag_hint", "正在研判站点风险、优先级与疑似误报…"),
  });
}

function wsAIFinding(scan, idx) {
  if (!scan || typeof openAIAssist !== "function") return;
  const findings = scan.findings || [];
  const f = findings[idx];
  if (!f) {
    if (typeof toast === "function") toast(wsT("ws.finding_missing", "未找到该命中项"), "err");
    return;
  }
  const ctx = {
    target_id: scan.target_id,
    target_name: scan.target_name,
    base_url: scan.base_url,
    finding: f,
    peers_same_template: findings.filter(x => x !== f && x.template_id === f.template_id).slice(0, 5).map(x => ({
      severity: x.severity, matcher_name: x.matcher_name, url: x.url || x.matched_at,
    })),
  };
  openAIAssist({
    task: "web_vuln_finding", mode: "analyze",
    title: wsT("ws.ai_finding_title", "AI · 单条漏洞建议") + " · " + (f.name || f.template_id || "").slice(0, 40),
    context: JSON.stringify(ctx, null, 2).slice(0, 12000),
    hint: wsT("ws.ai_finding_hint", "正在分析本条 finding 的真伪、影响与修复步骤…"),
    applyLabel: wsT("ws.ai_apply_status", "按建议更新状态"),
    applyTo: async (text) => {
      const low = String(text || "").toLowerCase();
      let status = "";
      if (/\bfalse[_\s-]?positive\b/.test(low) || low.includes("误报")) status = "false_positive";
      else if (/\bresolved\b/.test(low) || low.includes("已修复") || low.includes("可关闭")) status = "resolved";
      else if (/\back\b/.test(low) || low.includes("已知接受") || low.includes("暂时接受")) status = "ack";
      if (!status) {
        if (typeof toast === "function") toast(wsT("ws.ai_status_unclear", "未从回复中识别到明确状态建议，请手动选择"), "warn");
        return false;
      }
      await wsUpdateFindingStatus(f, status);
      return true;
    },
  });
}

window._pageRenderers = window._pageRenderers || {};
window._pageRenderers["web-security"] = renderWebSecurity;

// Bridge for security-feeds.js (loaded as a sibling module outside this IIFE).
// Without it, 「情报源」clicks throw ReferenceError on paintWebSecurity/wsFetchJSON
// and look like a dead button.
window.__wsFeedsHost = {
  paint() { paintWebSecurity(); },
  fetchJSON: wsFetchJSON,
  setShowCfg(v) { wsShowCfg = !!v; },
  getShowCfg() { return !!wsShowCfg; },
  setEngine(e) { if (e) wsEngine = e; },
  isOpen() { return typeof SF_OPEN !== "undefined" && !!SF_OPEN; },
};
})();
