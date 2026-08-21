// host-security.js — 安全中心 · 主机安全
(function () {
"use strict";

let hsSummary = [];
let hsScans = [];
let hsHosts = [];
let hsSelected = null;
let hsBusy = false;
let hsPollTimer = null;
let hsCfg = null;
let hsShowCfg = false;
let hsPendingFilter = null; // { level: open|critical|high } from security overview
let hsPickSelected = new Set(); // toolbar multi-select
let hsPickCollapsed = new Set();
let hsPickQ = "";
let hsPickInited = false;
let hsCfgPickSelected = new Set(); // schedule config multi-select
let hsCfgPickCollapsed = new Set();
let hsCfgPickQ = "";
let hsCfgPickInited = false;

const hsT = (k, fb) => I18N.t(k, fb);
function hsEsc(s) { return typeof esc === "function" ? esc(String(s ?? "")) : String(s ?? ""); }

function hsHostIP(h) {
  if (!h) return "";
  return String(h.ip || h.agent_ip || h.primary_ip || "").trim();
}
function hsHostTitle(h) {
  const name = (h && (h.hostname || h.id)) || "";
  const ip = hsHostIP(h);
  return ip ? `${name} (${ip})` : name;
}
function hsCapturePick(kind) {
  const root = kind === "cfg" ? $("hsCfgHostTree") : $("hsHostTree");
  const set = kind === "cfg" ? hsCfgPickSelected : hsPickSelected;
  if (!root) return;
  root.querySelectorAll(".hs-pick-host:checked").forEach(cb => set.add(cb.value));
  root.querySelectorAll(".hs-pick-host:not(:checked)").forEach(cb => set.delete(cb.value));
}
function hsInitPickDefaults(hosts) {
  if (hsPickInited) return;
  hsPickInited = true;
  const online = (hosts || []).filter(h => h.online !== false);
  (online.length ? online : hosts || []).forEach(h => { if (h && h.id) hsPickSelected.add(h.id); });
}
function hsInitCfgPickDefaults() {
  if (hsCfgPickInited) return;
  hsCfgPickInited = true;
  const ids = (hsCfg && Array.isArray(hsCfg.host_ids)) ? hsCfg.host_ids : [];
  hsCfgPickSelected = new Set(ids.filter(Boolean));
}
function hsHostsByFolder(hosts) {
  const map = new Map();
  (hosts || []).forEach(h => {
    const fid = h.folder_id || "__ungrouped__";
    if (!map.has(fid)) map.set(fid, []);
    map.get(fid).push(h);
  });
  map.forEach(list => list.sort((a, b) => {
    const an = (a.hostname || a.id || "").toLowerCase();
    const bn = (b.hostname || b.id || "").toLowerCase();
    if (an !== bn) return an < bn ? -1 : 1;
    return hsHostIP(a).localeCompare(hsHostIP(b));
  }));
  return map;
}
function hsFilterHost(h, q) {
  if (!q) return true;
  const hay = [h.id, h.hostname, hsHostIP(h), h.os, h.platform, h.category, h.folder_path]
    .filter(Boolean).join(" ").toLowerCase();
  return hay.includes(q);
}
function hsPickFolderNodeHTML(node, byFolder, q, selected, collapsed, depth) {
  const kids = node.children || [];
  const own = (byFolder.get(node.id) || []).filter(h => hsFilterHost(h, q));
  const childHTML = kids.map(c => hsPickFolderNodeHTML(c, byFolder, q, selected, collapsed, depth + 1)).join("");
  const hasHosts = own.length > 0 || childHTML;
  if (q && !hasHosts && !(node.name || "").toLowerCase().includes(q)) return "";
  const isCollapsed = collapsed.has(node.id);
  const hostIds = [];
  const collect = (n) => {
    (byFolder.get(n.id) || []).forEach(h => { if (hsFilterHost(h, q) && h.online !== false) hostIds.push(h.id); });
    (n.children || []).forEach(collect);
  };
  collect(node);
  const checkedN = hostIds.filter(id => selected.has(id)).length;
  const folderState = !hostIds.length ? "" : (checkedN === hostIds.length ? "checked" : (checkedN > 0 ? "data-indeterminate=\"1\"" : ""));
  const pad = Math.min(depth, 6) * 14;
  let html = `<div class="hs-pick-folder" style="padding-left:${pad}px">
    <button type="button" class="hs-pick-caret" data-hs-fold="${hsEsc(node.id)}" aria-expanded="${isCollapsed ? "false" : "true"}">${isCollapsed ? "▸" : "▾"}</button>
    <label class="hs-pick-folder-lab">
      <input type="checkbox" class="hs-pick-folder-cb" data-hs-folder="${hsEsc(node.id)}" ${folderState}>
      <span class="hs-pick-folder-name">${hsEsc(node.name || node.id)}</span>
      <span class="hs-pick-count">${own.length || hostIds.length}</span>
    </label>
  </div>`;
  if (!isCollapsed) {
    html += own.map(h => hsPickHostRowHTML(h, selected, depth + 1)).join("");
    html += childHTML;
  }
  return html;
}
function hsPickHostRowHTML(h, selected, depth) {
  const online = h.online !== false;
  const ip = hsHostIP(h);
  const pad = Math.min(depth, 6) * 14;
  return `<label class="hs-pick-row${online ? "" : " off"}${selected.has(h.id) && online ? " is-on" : ""}" style="padding-left:${pad + 22}px" title="${hsEsc(hsHostTitle(h))}">
    <input type="checkbox" class="hs-pick-host" value="${hsEsc(h.id)}" ${online ? "" : "disabled"} ${selected.has(h.id) ? "checked" : ""}>
    <span class="hs-pick-name">${hsEsc(h.hostname || h.id)}</span>
    <span class="hs-pick-ip mono">${hsEsc(ip || "—")}</span>
    <span class="hs-pick-st ${online ? "ok" : ""}"><i class="hs-pick-dot" aria-hidden="true"></i>${online ? hsEsc(hsT("hs.online", "在线")) : hsEsc(hsT("hs.offline", "离线"))}</span>
  </label>`;
}
function hsPickTreeHTML(opts) {
  const kind = opts.kind || "scan";
  const hosts = opts.hosts || [];
  const selected = opts.selected || new Set();
  const collapsed = opts.collapsed || new Set();
  const q = (opts.q || "").trim().toLowerCase();
  const treeId = kind === "cfg" ? "hsCfgHostTree" : "hsHostTree";
  const searchId = kind === "cfg" ? "hsCfgHostSearch" : "hsHostSearch";
  const folders = (typeof HOST_FOLDERS !== "undefined" && HOST_FOLDERS && HOST_FOLDERS.folders) ? HOST_FOLDERS.folders : [];
  const byFolder = hsHostsByFolder(hosts);
  const filtered = q ? hosts.filter(h => hsFilterHost(h, q)) : hosts;
  const onlineN = filtered.filter(h => h.online !== false).length;
  const selN = [...selected].filter(id => filtered.some(h => h.id === id)).length;
  let body = "";
  if (!hosts.length) {
    body = `<div class="hs-pick-empty">${hsEsc(hsT("hs.no_hosts", "暂无主机"))}</div>`;
  } else if (!filtered.length) {
    body = `<div class="hs-pick-empty">${hsEsc(hsT("hs.no_host_match", "无匹配主机"))}</div>`;
  } else if (folders.length) {
    body = folders.map(n => hsPickFolderNodeHTML(n, byFolder, q, selected, collapsed, 0)).join("");
    const ug = (byFolder.get("__ungrouped__") || []).filter(h => hsFilterHost(h, q));
    if (ug.length) {
      const fake = { id: "__ungrouped__", name: hsT("hs.ungrouped", "未分组"), children: [] };
      body += hsPickFolderNodeHTML(fake, byFolder, q, selected, collapsed, 0);
    }
  } else {
    // Fallback: group by category / flat list
    const byCat = new Map();
    filtered.forEach(h => {
      const c = (h.category || "").trim() || hsT("hs.ungrouped", "未分组");
      if (!byCat.has(c)) byCat.set(c, []);
      byCat.get(c).push(h);
    });
    [...byCat.keys()].sort().forEach(cat => {
      const list = byCat.get(cat);
      const fid = "cat:" + cat;
      const isCollapsed = collapsed.has(fid);
      const ids = list.filter(h => h.online !== false).map(h => h.id);
      const checkedN = ids.filter(id => selected.has(id)).length;
      const folderState = !ids.length ? "" : (checkedN === ids.length ? "checked" : (checkedN > 0 ? "data-indeterminate=\"1\"" : ""));
      body += `<div class="hs-pick-folder">
        <button type="button" class="hs-pick-caret" data-hs-fold="${hsEsc(fid)}" aria-expanded="${isCollapsed ? "false" : "true"}">${isCollapsed ? "▸" : "▾"}</button>
        <label class="hs-pick-folder-lab">
          <input type="checkbox" class="hs-pick-folder-cb" data-hs-folder="${hsEsc(fid)}" ${folderState}>
          <span class="hs-pick-folder-name">${hsEsc(cat)}</span>
          <span class="hs-pick-count">${list.length}</span>
        </label>
      </div>`;
      if (!isCollapsed) body += list.map(h => hsPickHostRowHTML(h, selected, 1)).join("");
    });
  }
  return `<div class="hs-pick-tree-wrap" data-hs-pick="${hsEsc(kind)}">
    <div class="hs-pick-tools">
      <input type="search" id="${searchId}" class="hs-pick-search" value="${hsEsc(opts.q || "")}" placeholder="${hsEsc(hsT("hs.host_search_ph", "搜索主机名 / IP / 分组…"))}" autocomplete="off">
      <div class="hs-pick-quick">
        <button type="button" class="btn sm ghost" data-hs-pick-act="all-online" data-kind="${hsEsc(kind)}">${hsEsc(hsT("hs.select_all_online", "全选在线"))}</button>
        <button type="button" class="btn sm ghost" data-hs-pick-act="clear" data-kind="${hsEsc(kind)}">${hsEsc(hsT("hs.clear_sel", "清空"))}</button>
        <span class="hs-pick-meta">${selN}/${onlineN || filtered.length}</span>
      </div>
    </div>
    <div class="hs-pick-tree" id="${treeId}">${body}</div>
  </div>`;
}
function hsBindPickTree(root) {
  if (!root) return;
  root.querySelectorAll(".hs-pick-folder-cb[data-indeterminate]").forEach(cb => { cb.indeterminate = true; });
  root.querySelectorAll("[data-hs-fold]").forEach(btn => {
    btn.addEventListener("click", e => {
      e.preventDefault();
      const id = btn.dataset.hsFold;
      const kind = root.dataset.hsPick || "scan";
      const collapsed = kind === "cfg" ? hsCfgPickCollapsed : hsPickCollapsed;
      if (collapsed.has(id)) collapsed.delete(id); else collapsed.add(id);
      hsCapturePick(kind);
      if (kind === "cfg") hsCfgPickQ = ($("hsCfgHostSearch") && $("hsCfgHostSearch").value) || hsCfgPickQ;
      else hsPickQ = ($("hsHostSearch") && $("hsHostSearch").value) || hsPickQ;
      paintHostSecurity();
    });
  });
  root.querySelectorAll(".hs-pick-folder-cb").forEach(cb => {
    cb.addEventListener("change", () => {
      const kind = root.dataset.hsPick || "scan";
      const selected = kind === "cfg" ? hsCfgPickSelected : hsPickSelected;
      const folderId = cb.dataset.hsFolder;
      const tree = kind === "cfg" ? $("hsCfgHostTree") : $("hsHostTree");
      if (!tree) return;
      // Toggle visible online hosts under this folder block until next folder at same/lower depth — simpler: all hosts currently listed that belong to folder
      const ids = hsFolderMemberIds(folderId, kind);
      ids.forEach(id => { if (cb.checked) selected.add(id); else selected.delete(id); });
      if (kind === "cfg") hsCfgPickQ = ($("hsCfgHostSearch") && $("hsCfgHostSearch").value) || "";
      else hsPickQ = ($("hsHostSearch") && $("hsHostSearch").value) || "";
      paintHostSecurity();
    });
  });
  const search = root.querySelector(".hs-pick-search");
  if (search) {
    search.addEventListener("input", () => {
      const kind = root.dataset.hsPick || "scan";
      hsCapturePick(kind);
      if (kind === "cfg") hsCfgPickQ = search.value || "";
      else hsPickQ = search.value || "";
      paintHostSecurity();
      const again = $(kind === "cfg" ? "hsCfgHostSearch" : "hsHostSearch");
      if (again) { again.focus(); const v = again.value; again.setSelectionRange(v.length, v.length); }
    });
  }
  root.querySelectorAll("[data-hs-pick-act]").forEach(btn => {
    btn.addEventListener("click", () => {
      const kind = btn.dataset.kind || "scan";
      const selected = kind === "cfg" ? hsCfgPickSelected : hsPickSelected;
      hsCapturePick(kind);
      if (btn.dataset.hsPickAct === "clear") selected.clear();
      else {
        const pool = (hsHosts || []).filter(h => h.online !== false);
        pool.forEach(h => selected.add(h.id));
      }
      paintHostSecurity();
    });
  });
  root.querySelectorAll(".hs-pick-host").forEach(cb => {
    cb.addEventListener("change", () => {
      const kind = root.dataset.hsPick || "scan";
      const selected = kind === "cfg" ? hsCfgPickSelected : hsPickSelected;
      if (cb.checked) selected.add(cb.value); else selected.delete(cb.value);
      const meta = root.querySelector(".hs-pick-meta");
      if (meta) {
        const onlineN = (hsHosts || []).filter(h => h.online !== false).length;
        meta.textContent = `${selected.size}/${onlineN}`;
      }
      if (kind === "scan") {
        const chip = document.querySelector("#hostSecurityPanel .sec-sel-chip");
        if (chip) chip.textContent = `${hsSelectedScanIds().length} ${hsT("hs.selected_n", "已选")}`;
      }
      root.querySelectorAll(".hs-pick-row").forEach(row => {
        const input = row.querySelector(".hs-pick-host");
        if (input) row.classList.toggle("is-on", !!input.checked && !input.disabled);
      });
    });
  });
}
function hsFolderMemberIds(folderId, kind) {
  const q = ((kind === "cfg" ? hsCfgPickQ : hsPickQ) || "").trim().toLowerCase();
  const hosts = hsHosts || [];
  if (folderId === "__ungrouped__") {
    return hosts.filter(h => !h.folder_id && hsFilterHost(h, q) && h.online !== false).map(h => h.id);
  }
  if (String(folderId).startsWith("cat:")) {
    const cat = folderId.slice(4);
    const un = hsT("hs.ungrouped", "未分组");
    return hosts.filter(h => {
      const c = (h.category || "").trim() || un;
      return c === cat && hsFilterHost(h, q) && h.online !== false;
    }).map(h => h.id);
  }
  // folder + descendants
  const folders = (typeof HOST_FOLDERS !== "undefined" && HOST_FOLDERS && HOST_FOLDERS.folders) ? HOST_FOLDERS.folders : [];
  const ids = new Set();
  const walk = (nodes) => {
    for (const n of nodes || []) {
      if (n.id === folderId) {
        const mark = (x) => {
          ids.add(x.id);
          (x.children || []).forEach(mark);
        };
        mark(n);
        return true;
      }
      if (walk(n.children || [])) return true;
    }
    return false;
  };
  walk(folders);
  return hosts.filter(h => ids.has(h.folder_id) && hsFilterHost(h, q) && h.online !== false).map(h => h.id);
}
function hsSelectedScanIds() {
  hsCapturePick("scan");
  return [...hsPickSelected].filter(id => (hsHosts || []).some(h => h.id === id));
}

function hsLevelLabel(level) {
  const m = {
    crit: hsT("hs.level_crit", "危急"), critical: hsT("hs.level_crit", "危急"),
    high: hsT("hs.level_high", "高危"), medium: hsT("hs.level_medium", "中危"),
    low: hsT("hs.level_low", "低危"), info: hsT("hs.level_info", "信息"),
  };
  return m[String(level || "").toLowerCase()] || (level || "—");
}
function hsLevelBadge(level) {
  const m = { crit: "crit", critical: "crit", high: "high", medium: "warn", low: "info", info: "info" };
  const cls = m[String(level || "").toLowerCase()] || "info";
  return `<span class="badge ${cls}">${hsEsc(hsLevelLabel(level))}</span>`;
}
function hsStatusLabel(st) {
  const m = {
    running: hsT("hs.status_running", "进行中"),
    completed: hsT("hs.status_completed", "已完成"),
    failed: hsT("hs.status_failed", "失败"),
  };
  return m[st] || st || "—";
}
function hsStatusBadge(st) {
  const m = { running: "info", completed: "ok", failed: "crit" };
  return `<span class="badge ${m[st] || "info"}">${hsEsc(hsStatusLabel(st))}</span>`;
}
function hsClamLabel(v) {
  const m = {
    available: hsT("hs.clam_available", "已启用"),
    unavailable: hsT("hs.clam_unavailable", "未检测到"),
    disabled: hsT("hs.clam_disabled", "已关闭"),
    error: hsT("hs.clam_error", "异常"),
  };
  return m[v] || v || "—";
}
// A clean ClamAV result means nothing if the signature database is months old,
// so the age travels with the status everywhere it is shown.
function hsClamText(scan) {
  const base = hsClamLabel(scan.clamav);
  const age = Number(scan.clamav_db_age_days || 0);
  if (scan.clamav !== "available" || age <= 0) return base;
  return `${base} · ${hsT("hs.clam_db_age", "病毒库")} ${age}${hsT("hs.clam_db_age_unit", " 天前更新")}`;
}
function hsClamBadgeClass(scan) {
  const age = Number(scan.clamav_db_age_days || 0);
  if (scan.clamav === "error") return "crit";
  if (scan.clamav === "available" && age >= 30) return "crit";
  if (scan.clamav === "available" && age >= 7) return "warn";
  return "";
}
function hsFwLabel(v) {
  const m = {
    on: hsT("hs.fw_on", "已开启"),
    off: hsT("hs.fw_off", "已关闭"),
    partial: hsT("hs.fw_partial", "部分关闭"),
    unknown: hsT("hs.fw_unknown", "未知"),
  };
  return m[String(v || "").toLowerCase()] || (v ? String(v) : hsT("hs.fw_unknown", "未知"));
}
function hsFwBadge(row, opts) {
  const st = String((row && row.firewall) || "").toLowerCase();
  if (!st) return `<span class="muted">${hsEsc(hsT("hs.ports_unknown", "需重新扫描"))}</span>`;
  const cls = { on: "ok", off: "crit", partial: "high", unknown: "info" }[st] || "info";
  const eng = (row && row.firewall_engine) || "";
  const tip = [hsFwLabel(st) + (eng ? ` · ${eng}` : ""), (row && row.firewall_detail) || ""].filter(Boolean).join(" — ");
  const showEng = opts && opts.engine && eng;
  return `<span class="badge ${cls}" title="${hsEsc(tip)}">${hsEsc(hsFwLabel(st))}</span>${showEng ? `<div class="mono muted hs-fw-eng">${hsEsc(eng)}</div>` : ""}`;
}
function hsFmtTime(ts) {
  if (!ts) return "—";
  try { return new Date(ts * 1000).toLocaleString(); } catch (_) { return "—"; }
}
function hsScanLabel(s) {
  if (!s) return "—";
  if (s.label) return s.label;
  const name = s.hostname || s.host_id || "扫描";
  return `${name} · ${hsFmtTime(s.started_at)}`;
}
function hsScanIdShort(id) {
  const s = String(id || "");
  if (s.length <= 22) return s;
  return s.slice(0, 18) + "…";
}
function hsCatLabel(cat) {
  const m = {
    hardening: hsT("hs.cat_hardening", "加固"),
    malware: hsT("hs.cat_malware", "恶意软件"),
    ioc: hsT("hs.cat_ioc", "威胁迹象"),
    cve: "CVE",
    port: hsT("hs.cat_port", "端口"),
    fim: hsT("hs.cat_fim", "文件变更"),
  };
  return m[cat] || cat || "—";
}
// The advanced FIM fields live in a <details> that is absent unless the config
// panel is open, so every reader falls back to the last saved value.
function hsCfgVal(id, fallback) {
  const el = $(id);
  return el && el.value ? el.value : fallback;
}

function hsCfgNum(id, fallback) {
  const el = $(id);
  const n = el ? parseInt(el.value, 10) : NaN;
  return Number.isFinite(n) && n > 0 ? n : fallback;
}

function hsCfgLines(id, fallback) {
  const el = $(id);
  if (!el) return fallback;
  return el.value.split("\n").map(s => s.trim()).filter(Boolean);
}

function hsFimChangeLabel(ch) {
  const m = {
    added: hsT("hs.fim_added", "新增"),
    removed: hsT("hs.fim_removed", "删除"),
    modified: hsT("hs.fim_modified", "修改"),
  };
  return m[ch] || ch || "—";
}
function hsComplianceText(f) {
  return (f.compliance || []).map(c => `${c.framework} ${c.control}`).join("; ");
}

function hsComplianceTags(f) {
  const refs = (f.compliance || []).slice(0, 4);
  if (!refs.length) return "";
  return `<div class="hs-compliance">` + refs.map(c =>
    `<span class="tag hs-comp-tag" title="${hsEsc(c.title || "")}">${hsEsc(c.framework)} ${hsEsc(c.control)}</span>`
  ).join("") + `</div>`;
}

// hsCompliancePanel shows how many distinct controls are failing per framework.
function hsCompliancePanel(scan) {
  const sum = scan.compliance || {};
  const frameworks = Object.keys(sum).sort();
  if (!frameworks.length) return "";
  const chips = frameworks.map(fw =>
    `<span class="hs-comp-chip"><b>${sum[fw]}</b> ${hsEsc(fw)}</span>`
  ).join("");
  return `<div class="hs-compliance-panel">
    <div class="hs-fw-head"><span class="cfg-panel-title">${hsEsc(hsT("hs.compliance", "合规映射"))}</span></div>
    <p class="ws-help">${hsEsc(hsT("hs.compliance_help", "按框架统计当前未处置项命中的不同控制项数量，可直接作为整改依据。"))}</p>
    <div class="hs-comp-chips">${chips}</div></div>`;
}

function hsFimReasonLabel(c) {
  const map = {
    content: hsT("hs.fim_reason_content", "内容"),
    size: hsT("hs.fim_reason_size", "大小"),
    mtime: hsT("hs.fim_reason_mtime", "时间戳"),
    mode: hsT("hs.fim_reason_mode", "权限"),
    added: hsT("hs.fim_added", "新增"),
    removed: hsT("hs.fim_removed", "删除"),
  };
  const base = map[c.reason] || (c.old_sha || c.new_sha ? map.content : "—");
  if (c.old_mode && c.new_mode && c.old_mode !== c.new_mode) {
    return `${base} · ${c.old_mode}→${c.new_mode}`;
  }
  return base;
}

// hsFimScopeLine states what the agent actually walked, so an empty result is
// never mistaken for "the whole disk is clean".
function hsFimScopeLine(scan) {
  const st = scan.fim_stats;
  if (!st) {
    return hsT("hs.fim_scope_legacy", "该 Agent 为旧版：仅监控内置敏感路径白名单。升级 Agent 后可监控所有目录的文件增删改。");
  }
  if (st.mode === "sensitive") {
    return hsT("hs.fim_scope_sensitive", "监控范围：仅敏感路径白名单（可在配置中切换为全盘）。");
  }
  const roots = (st.roots || []).join(" ") || "/";
  let line = hsT("hs.fim_scope_full", "监控范围：{roots}（共 {files} 个文件 / {dirs} 个目录，耗时 {ms} ms，仅记录路径与元数据）")
    .replace("{roots}", roots)
    .replace("{files}", String(st.files || 0))
    .replace("{dirs}", String(st.dirs || 0))
    .replace("{ms}", String(st.duration_ms || 0));
  if (st.limit_hit || st.budget_hit) {
    line += " " + hsT("hs.fim_scope_partial", "本次遍历因文件数/时间上限提前结束，未覆盖全部目录（不会误报删除）。");
  }
  if (st.error) line += " " + st.error;
  return line;
}

function hsFimChangeBadge(ch) {
  const cls = { added: "ok", removed: "crit", modified: "warn" }[ch] || "info";
  return `<span class="badge ${cls}">${hsEsc(hsFimChangeLabel(ch))}</span>`;
}
function hsShortSHA(s) {
  const t = String(s || "");
  return t.length > 12 ? t.slice(0, 12) : (t || "—");
}
function hsFmtUnix(ts) {
  const n = Number(ts) || 0;
  if (!n) return "—";
  try { return new Date(n * 1000).toLocaleString(); } catch (_) { return "—"; }
}
async function hsUpdateFindingStatus(finding, status) {
  if (!hsSelected || !finding) return;
  try {
    const r = await fetch(`${API}/security/findings/status`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        scope: "host", host_id: hsSelected.host_id, status,
        finding: {
          id: finding.id, category: finding.category, cve: finding.cve, title: finding.title,
          detail: finding.detail || "", package: finding.package || "",
        },
      }),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || "更新失败");
    finding.status = status;
    hsPaintDetail(hsSelected);
    toast(hsT("hs.finding_updated", "状态已更新"), "ok");
  } catch (e) { toast(e.message || String(e), "err"); }
}
function hsFindingStatusControls(f) {
  const st = f.status || "open";
  const opts = ["open", "ack", "false_positive", "resolved"];
  return `<select class="hs-finding-status" data-hsfid="${hsEsc(f.id || "")}" data-hscat="${hsEsc(f.category || "")}" data-hsdetail="${hsEsc(f.detail || "")}" data-hspkg="${hsEsc(f.package || "")}" style="font-size:11px;height:26px">
    ${opts.map(o => `<option value="${o}"${o === st ? " selected" : ""}>${o}</option>`).join("")}
  </select>`;
}
function hsPortRiskCls(risk) {
  const m = { crit: "crit", critical: "crit", high: "high", medium: "warn" };
  return m[String(risk || "").toLowerCase()] || "info";
}
/** Deduplicate open_ports by proto+port (legacy scans may still have dual-stack dupes). */
function hsUniquePorts(ports) {
  const map = new Map();
  (ports || []).forEach(p => {
    if (!p || !p.port) return;
    const key = `${(p.proto || "tcp").toLowerCase()}|${p.port}`;
    const prev = map.get(key);
    if (!prev) { map.set(key, Object.assign({}, p)); return; }
    const rank = r => ({ crit: 3, high: 2, medium: 1 }[r] || 0);
    if (rank(p.risk) > rank(prev.risk)) prev.risk = p.risk;
    if (p.public) prev.public = true;
    if (!prev.process && p.process) prev.process = p.process;
    if (!prev.service && p.service) prev.service = p.service;
    if (p.public || (!prev.public && p.addr)) prev.addr = p.addr || prev.addr;
  });
  return [...map.values()].sort((a, b) => {
    const rank = r => ({ crit: 3, high: 2, medium: 1 }[r] || 0);
    return rank(b.risk) - rank(a.risk) || a.port - b.port;
  });
}
/** Compact open-port summary for tables; clickable to open port detail. */
function hsPortsCell(row, compact) {
  const scanId = row.scan_id || row.id || "";
  const ports = hsUniquePorts(row.open_ports || []);
  const sample = row.port_sample || [];
  const risky = ports.filter(p => p.risk === "crit" || p.risk === "high" || p.risk === "medium");
  const riskyN = risky.length || row.risky_port_count || 0;
  const total = ports.length || (row.port_count != null ? row.port_count : sample.length);
  if (!total && !sample.length) {
    return `<span class="muted">${hsEsc(hsT("hs.ports_unknown", "需重新扫描"))}</span>`;
  }
  const tipPorts = (risky.length ? risky : ports).slice(0, 12).map(p =>
    `${p.proto || "tcp"} ${p.addr || "*"}:${p.port}${p.service ? " " + p.service : ""}`
  ).join("\n");
  const tip = tipPorts || String(total);
  const riskBit = riskyN
    ? `<em class="hs-port-risk ${riskyN >= 3 ? "crit" : "high"}">${riskyN}</em>`
    : `<em class="hs-port-risk ok">0</em>`;
  let chips = "";
  const show = (risky.length ? risky : ports).slice(0, compact ? 2 : 3);
  if (show.length) {
    chips = `<span class="hs-port-chips">${show.map(p =>
      `<span class="hs-port-chip ${hsPortRiskCls(p.risk)}">${hsEsc(String(p.port))}</span>`
    ).join("")}${total > show.length ? `<span class="hs-port-chip more">+${total - show.length}</span>` : ""}</span>`;
  } else if (sample.length) {
    const n = compact ? 2 : 3;
    chips = `<span class="hs-port-chips">${sample.slice(0, n).map(p =>
      `<span class="hs-port-chip">${hsEsc(String(p))}</span>`
    ).join("")}${sample.length > n || total > n ? `<span class="hs-port-chip more">+${Math.max(0, total - n)}</span>` : ""}</span>`;
  }
  const inner = `<span class="hs-ports-meta"><strong>${total}</strong>${riskBit}</span>${chips}`;
  if (!scanId) return `<div class="hs-ports-cell" title="${hsEsc(tip)}">${inner}</div>`;
  return `<button type="button" class="hs-ports-cell hs-ports-link" data-hs-ports="${hsEsc(scanId)}" title="${hsEsc(hsT("hs.ports_click", "点击查看开放端口明细") + "\n" + tip)}">${inner}</button>`;
}
function hsNoticeHTML() {
  return `<div class="sec-notice sec-notice-slim">${hsEsc(hsT("hs.notice", "Agent 采集加固 / 端口 / 防火墙 / ClamAV；服务端匹配 OSV CVE。导出含端口与防火墙明细。"))}</div>`;
}
function hsScoreCell(score, risk) {
  const n = score == null ? "—" : String(score);
  const cls = (risk === "crit" || risk === "critical") ? "crit"
    : (risk === "high") ? "high"
    : (risk === "medium") ? "med" : "ok";
  return `<div class="hs-score-cell ${cls}"><strong>${hsEsc(n)}</strong></div>`;
}
function hsExportMenuHTML(disabled) {
  const dis = disabled ? " disabled" : "";
  return `<div class="exp-dd">
    <button class="btn" data-hs="export-toggle" aria-haspopup="true"${dis}>${hsEsc(hsT("hs.export", "导出报告"))}</button>
    <div class="exp-dd-menu" id="hsExportMenu" role="menu">
      <button class="exp-dd-opt" role="menuitem" data-hsexport="pdf"><span>${hsEsc(hsT("hs.export_pdf", "PDF 报告"))}</span><span class="exp-dd-ext">${hsEsc(hsT("hs.export_pdf_tip", "打印"))}</span></button>
      <button class="exp-dd-opt" role="menuitem" data-hsexport="word"><span>${hsEsc(hsT("hs.export_word", "Word 文档"))}</span><span class="exp-dd-ext">.docx</span></button>
      <button class="exp-dd-opt" role="menuitem" data-hsexport="html"><span>${hsEsc(hsT("hs.export_html", "HTML 网页"))}</span><span class="exp-dd-ext">.html</span></button>
      <button class="exp-dd-opt" role="menuitem" data-hsexport="markdown"><span>${hsEsc(hsT("hs.export_md", "Markdown"))}</span><span class="exp-dd-ext">.md</span></button>
      <button class="exp-dd-opt" role="menuitem" data-hsexport="excel"><span>${hsEsc(hsT("hs.export_excel", "Excel 表格"))}</span><span class="exp-dd-ext">.xlsx</span></button>
      <button class="exp-dd-opt" role="menuitem" data-hsexport="json"><span>${hsEsc(hsT("hs.export_json", "JSON 原始数据"))}</span><span class="exp-dd-ext">.json</span></button>
    </div>
  </div>`;
}
function hsWeekdayOpts(selected) {
  const days = [
    [0, hsT("hs.wd_sun", "周日")], [1, hsT("hs.wd_mon", "周一")], [2, hsT("hs.wd_tue", "周二")],
    [3, hsT("hs.wd_wed", "周三")], [4, hsT("hs.wd_thu", "周四")], [5, hsT("hs.wd_fri", "周五")],
    [6, hsT("hs.wd_sat", "周六")],
  ];
  return days.map(([v, lab]) =>
    `<option value="${v}"${Number(selected) === Number(v) ? " selected" : ""}>${hsEsc(lab)}</option>`
  ).join("");
}

async function hsFetchJSON(url, opts) {
  const r = await fetch(url, Object.assign({ credentials: "same-origin" }, opts || {}));
  let d = null;
  try { d = await r.json(); } catch (_) { d = null; }
  if (!r.ok) throw new Error((d && d.error) || r.statusText || ("HTTP " + r.status));
  return d;
}

function hsFindingMatchesFilter(f, level) {
  if (!f) return false;
  const st = String(f.status || "open").toLowerCase();
  if (st === "resolved" || st === "false_positive" || st === "ack" || st === "accepted") return false;
  const lv = String(f.level || "").toLowerCase();
  if (level === "critical") return lv === "critical" || lv === "crit";
  if (level === "high") return lv === "high";
  return lv === "critical" || lv === "crit" || lv === "high"; // open = crit+high pending
}

function hsPendingBannerHTML() {
  if (!hsPendingFilter || !hsPendingFilter.level) return "";
  const label = hsPendingFilter.level === "critical"
    ? hsT("hs.filter_crit", "仅显示开放危急项")
    : hsPendingFilter.level === "high"
      ? hsT("hs.filter_high", "仅显示开放高危项")
      : hsT("hs.filter_open", "仅显示开放危急/高危项");
  return `<div class="sec-notice sec-notice-slim" id="hsPendingBanner">${hsEsc(label)}
    <button type="button" class="btn sm ghost" data-hs="clear-filter">${hsEsc(hsT("hs.clear_filter", "清除筛选"))}</button></div>`;
}

async function renderHostSecurity() {
  const el = $("hostSecurityPanel");
  if (!el) return;
  if (typeof window.secConsumePendingFilter === "function") {
    const f = window.secConsumePendingFilter("host-security");
    if (f) hsPendingFilter = f;
  }
  el.innerHTML = `<div class="loading-dots">${hsT("common.loading", "加载中...")}</div>`;
  try {
    let hosts = [];
    try {
      hosts = typeof fetchHostsList === "function"
        ? await fetchHostsList({ maxAgeMs: 20000 })
        : (await hsFetchJSON(`${API}/hosts`) || []);
    } catch (_) {
      hosts = (window._cachedHosts && window._cachedHosts.length) ? window._cachedHosts : [];
    }
    hsHosts = Array.isArray(hosts) ? hosts : [];
    if (typeof loadHostFolders === "function") {
      try { await loadHostFolders(); } catch (_) {}
    }
    const [sum, scans, cfg] = await Promise.all([
      hsFetchJSON(`${API}/security/host/summary`),
      hsFetchJSON(`${API}/security/host/scans?limit=40`),
      hsFetchJSON(`${API}/security/host/config`).catch(() => null),
    ]);
    hsSummary = sum.hosts || [];
    hsScans = scans.scans || [];
    hsCfg = cfg;
    if (hsPendingFilter && hsPendingFilter.level && !hsSelected) {
      const lv = hsPendingFilter.level;
      const hit = (hsScans || []).find(s => {
        if (s.status !== "completed") return false;
        const sum = s.summary || {};
        if (Number(sum[lv] || 0) > 0) return true;
        return (s.findings || []).some(f => hsFindingMatchesFilter(f, lv));
      });
      if (hit) {
        try { hsSelected = await hsFetchJSON(`${API}/security/host/scans/` + encodeURIComponent(hit.id)); }
        catch (_) { hsSelected = hit; }
      }
    }
    paintHostSecurity();
    hsMaybePoll();
  } catch (err) {
    el.innerHTML = `<div class="empty-state"><h4>${hsEsc(hsT("hs.load_failed", "加载失败"))}</h4><p>${hsEsc(err.message || err)}</p></div>`;
  }
}

function paintHostSecurity() {
  const el = $("hostSecurityPanel");
  if (!el) return;
  const crit = hsSummary.filter(h => h.risk === "critical" || h.risk === "crit").length;
  const high = hsSummary.filter(h => h.risk === "high").length;
  const running = hsScans.filter(s => s.status === "running").length;

  let html = `<div class="sec-shell hs-shell">`;
  html += hsNoticeHTML();
  html += hsPendingBannerHTML();
  html += `<div class="sec-metrics">
    <div class="sec-metric"><b>${hsSummary.length}</b><span>${hsEsc(hsT("hs.stat_scanned", "已扫描"))}</span></div>
    <div class="sec-metric crit"><b>${crit}</b><span>${hsEsc(hsT("hs.stat_crit", "危急"))}</span></div>
    <div class="sec-metric high"><b>${high}</b><span>${hsEsc(hsT("hs.stat_high", "高危"))}</span></div>
    <div class="sec-metric"><b>${running}</b><span>${hsEsc(hsT("hs.stat_running", "进行中"))}</span></div>
  </div>`;

  hsCapturePick("scan");
  hsInitPickDefaults(hsHosts);
  const hsSelN = hsSelectedScanIds().length;
  html += `<div class="sec-command">
    <div class="sec-command-pick">
      <div class="sec-command-label">
        <span class="sec-command-title">${hsEsc(hsT("hs.host", "主机"))}</span>
        <span class="sec-command-hint">${hsEsc(hsT("hs.host_pick_hint", "树形多选 · 显示主机名与 IP"))}</span>
      </div>
      ${hsPickTreeHTML({ kind: "scan", hosts: hsHosts, selected: hsPickSelected, collapsed: hsPickCollapsed, q: hsPickQ })}
    </div>
    <div class="sec-command-side">
      <div class="sec-command-cta">
        <button class="btn primary sec-scan-btn" data-hs="scan" ${hsBusy ? "disabled" : ""}>${hsEsc(hsT("hs.scan", "扫描选中"))}</button>
        <span class="sec-sel-chip">${hsSelN} ${hsEsc(hsT("hs.selected_n", "已选"))}</span>
      </div>
      <div class="sec-command-tools">
        <button class="btn ghost" data-hs="refresh">${hsEsc(hsT("common.refresh", "刷新"))}</button>
        ${typeof isAdmin === "function" && isAdmin() ? `<button class="btn" data-hs="cfg">${hsEsc(hsT("hs.config", "定时"))}</button>` : ""}
        <div class="act-menu act-menu-ai">
          <button type="button" class="btn sm act-menu-trigger" aria-haspopup="true" aria-expanded="false"><span data-i18n="ui.ai_menu">AI</span><span class="act-menu-caret">▾</span></button>
          <div class="act-menu-panel" hidden role="menu">
            <div class="act-menu-hint">${hsEsc(hsT("hs.ai_menu_hint", "基于当前扫描详情"))}</div>
            <button type="button" role="menuitem" data-hs="ai-diag">${hsEsc(hsT("hs.ai_diag", "AI 研判"))}<span class="act-menu-sub">${hsEsc(hsT("hs.ai_diag_tip", "研判风险、优先级与疑似误报"))}</span></button>
            <button type="button" role="menuitem" data-hs="ai-rem">${hsEsc(hsT("hs.ai_rem", "AI 修复"))}<span class="act-menu-sub">${hsEsc(hsT("hs.ai_rem_tip", "生成可确认执行的修复动作计划"))}</span></button>
          </div>
        </div>
        ${hsExportMenuHTML(false)}
      </div>
    </div>
  </div>`;

  if (hsShowCfg && hsCfg) {
    const sch = hsCfg.schedule || {};
    hsInitCfgPickDefaults();
    html += `<div class="cfg-panel sec-cfg-panel">
      <div class="cfg-panel-head"><div class="cfg-panel-title">${hsEsc(hsT("hs.config_title", "主机扫描调度"))}</div>
        <span class="tag">${hsEsc(hsT("hs.admin_hint", "写入需管理员"))}</span></div>
      <div class="cfg-form">
        <p class="ws-help">${hsEsc(hsT("hs.cfg_edit_help", "可随时修改并再次保存：定时开关、ClamAV、周期与纳入调度的主机列表。"))}</p>
        <label class="switch cfg-enable"><input type="checkbox" id="hsCfgEnabled"${hsCfg.enabled ? " checked" : ""}><span>${hsEsc(hsT("hs.cfg_enabled", "启用定时扫描"))}</span></label>
        <label class="switch cfg-enable"><input type="checkbox" id="hsCfgClam"${hsCfg.enable_clamav !== false ? " checked" : ""}><span>${hsEsc(hsT("hs.cfg_clam", "尝试使用 ClamAV"))}</span></label>
        <label class="switch cfg-enable"><input type="checkbox" id="hsCfgFIM"${hsCfg.fim_enabled !== false ? " checked" : ""}><span>${hsEsc(hsT("hs.cfg_fim", "文件完整性监控 (FIM)"))}</span></label>
        <label class="switch cfg-enable"><input type="checkbox" id="hsCfgFIMDiff"${hsCfg.fim_content_diff !== false ? " checked" : ""}${hsCfg.fim_enabled === false ? " disabled" : ""}><span>${hsEsc(hsT("hs.cfg_fim_diff", "白名单配置内容差异"))}</span></label>
        <details class="hs-fim-cfg"${hsCfg.fim_scope === "sensitive" || (hsCfg.fim_content_paths || []).length || (hsCfg.fim_excludes || []).length ? " open" : ""}>
          <summary>${hsEsc(hsT("hs.cfg_fim_adv", "FIM 监控范围与内容审计白名单"))}</summary>
          <p class="ws-help">${hsEsc(hsT("hs.cfg_fim_adv_help", "全盘模式记录任意目录下文件的新增/修改/删除（仅路径与元数据，不采集文件内容）；只有内容审计白名单内的文件才会计算哈希并生成脱敏差异。"))}</p>
          <div class="cfg-form-row">
            <div class="field"><label>${hsEsc(hsT("hs.cfg_fim_scope", "监控范围"))}</label>
              <div class="select-wrap"><select id="hsCfgFIMScope">
                <option value="full"${hsCfg.fim_scope !== "sensitive" ? " selected" : ""}>${hsEsc(hsT("hs.cfg_fim_scope_full", "全盘（所有目录，元数据级）"))}</option>
                <option value="sensitive"${hsCfg.fim_scope === "sensitive" ? " selected" : ""}>${hsEsc(hsT("hs.cfg_fim_scope_sensitive", "仅敏感路径（兼容旧版）"))}</option>
              </select></div></div>
            <div class="field"><label>${hsEsc(hsT("hs.cfg_fim_max_files", "单次最多遍历文件数"))}</label>
              <input id="hsCfgFIMMaxFiles" type="number" min="1000" max="2000000" value="${hsEsc(String(hsCfg.fim_max_files || 150000))}"></div>
            <div class="field"><label>${hsEsc(hsT("hs.cfg_fim_max_changes", "单次最多上报变更数"))}</label>
              <input id="hsCfgFIMMaxChanges" type="number" min="10" max="5000" value="${hsEsc(String(hsCfg.fim_max_changes || 500))}"></div>
            <div class="field"><label>${hsEsc(hsT("hs.cfg_fim_budget", "遍历时间预算（秒）"))}</label>
              <input id="hsCfgFIMBudget" type="number" min="5" max="900" value="${hsEsc(String(hsCfg.fim_budget_sec || 90))}"></div>
          </div>
          <div class="field"><label>${hsEsc(hsT("hs.cfg_fim_roots", "监控根目录（每行一个，留空=全部本地磁盘）"))}</label>
            <textarea id="hsCfgFIMRoots" rows="2" placeholder="/">${hsEsc((hsCfg.fim_roots || []).join("\n"))}</textarea></div>
          <div class="field"><label>${hsEsc(hsT("hs.cfg_fim_excludes", "排除路径/目录名（每行一个，内置已排除 /proc /sys 缓存等）"))}</label>
            <textarea id="hsCfgFIMExcludes" rows="3" placeholder="/data/backup&#10;node_modules">${hsEsc((hsCfg.fim_excludes || []).join("\n"))}</textarea></div>
          <div class="field"><label>${hsEsc(hsT("hs.cfg_fim_content", "内容审计白名单（每行一个路径或通配，仅这些文件记录内容差异）"))}</label>
            <textarea id="hsCfgFIMContent" rows="3" placeholder="/etc/nginx/*.conf&#10;/opt/app/config.yaml">${hsEsc((hsCfg.fim_content_paths || []).join("\n"))}</textarea>
            <p class="ws-help">${hsEsc(hsT("hs.cfg_fim_content_help", "内置已含 /etc 下常见配置；口令与私钥类文件（shadow、*.pem、id_rsa、.env 等）永不做内容审计。"))}</p></div>
        </details>
        <label class="switch cfg-enable"><input type="checkbox" id="hsCfgAISummary"${hsCfg.auto_ai_summary ? " checked" : ""}><span>${hsEsc(hsT("hs.cfg_ai_summary", "扫描完成后自动 AI 摘要"))}</span></label>
        <div class="cfg-form-row">
          <div class="field"><label>${hsEsc(hsT("hs.cfg_kind", "周期"))}</label>
            <div class="select-wrap"><select id="hsCfgKind">
              <option value="interval"${sch.kind === "interval" ? " selected" : ""}>${hsEsc(hsT("hs.kind_interval", "间隔"))}</option>
              <option value="daily"${sch.kind === "daily" ? " selected" : ""}>${hsEsc(hsT("hs.kind_daily", "每天"))}</option>
              <option value="weekly"${!sch.kind || sch.kind === "weekly" ? " selected" : ""}>${hsEsc(hsT("hs.kind_weekly", "每周"))}</option>
            </select></div></div>
          <div class="field"><label>${hsEsc(hsT("hs.cfg_at", "时间 HH:MM / 间隔分钟"))}</label>
            <input id="hsCfgAt" value="${hsEsc(sch.at || sch.interval_min || "03:30")}"></div>
          <div class="field"><label>${hsEsc(hsT("hs.cfg_weekday", "星期（仅每周）"))}</label>
            <div class="select-wrap"><select id="hsCfgWeekday">${hsWeekdayOpts(sch.weekday != null ? sch.weekday : 0)}</select></div></div>
        </div>
        <div class="field"><label>${hsEsc(hsT("hs.cfg_hosts", "定时扫描主机（可多选，不选表示全部在线主机）"))}</label>
          ${hsPickTreeHTML({ kind: "cfg", hosts: hsHosts, selected: hsCfgPickSelected, collapsed: hsCfgPickCollapsed, q: hsCfgPickQ })}
          <p class="ws-help">${hsEsc(hsT("hs.cfg_hosts_help", "修改后点保存即可生效；与上方「扫描选中主机」的临时选择相互独立。空选 = 全部在线主机。"))}</p>
        </div>
        <div class="cfg-actions"><button class="btn primary" data-hs="save-cfg">${hsEsc(hsT("common.save", "保存修改"))}</button>
          <button class="btn" data-hs="cfg">${hsEsc(hsT("common.cancel", "收起"))}</button>
          <span class="cfg-status" id="hsCfgStatus"></span></div>
      </div>
    </div>`;
  }

  html += `<div class="hs-layout"><div class="hs-col-main">`;
  html += `<div class="cfg-panel hs-panel sec-panel">
    <div class="sec-panel-head">
      <div class="cfg-panel-title">${hsEsc(hsT("hs.summary", "主机风险汇总"))}</div>
      <span class="sec-panel-meta">${hsSummary.length}</span>
    </div>`;
  if (!hsSummary.length) {
    html += `<div class="sec-empty">
      <div class="sec-empty-ico" aria-hidden="true"></div>
      <h4>${hsEsc(hsT("hs.summary_empty_title", "暂无扫描结果"))}</h4>
      <p>${hsEsc(hsT("hs.summary_empty", "选择在线主机后点击「扫描选中」，结果将出现在此。"))}</p>
    </div>`;
  } else {
    html += `<div class="nf-table-wrap hs-table-wrap"><table class="data-table hs-table hs-table-compact"><thead><tr>
      <th>${hsEsc(hsT("hs.host", "主机"))}</th>
      <th class="col-score">${hsEsc(hsT("hs.score", "分"))}</th>
      <th class="col-risk">${hsEsc(hsT("hs.risk", "风险"))}</th>
      <th>${hsEsc(hsT("hs.ports", "端口"))}</th>
      <th>${hsEsc(hsT("hs.firewall", "防火墙"))}</th>
      <th class="col-num">CVE</th>
      <th class="col-time">${hsEsc(hsT("hs.time", "时间"))}</th>
    </tr></thead><tbody>`;
    hsSummary.forEach(h => {
      const active = hsSelected && hsSelected.id === h.scan_id ? " active-row" : "";
      const osLine = [h.distro, h.os].filter(Boolean).join(" · ");
      html += `<tr class="sec-row${active}" data-scan="${hsEsc(h.scan_id)}">
        <td><div class="hs-host-name">${hsEsc(h.hostname || h.host_id)}</div>
          ${osLine ? `<div class="hs-host-sub muted">${hsEsc(osLine)}</div>` : ""}</td>
        <td class="col-score">${hsScoreCell(h.score, h.risk)}</td>
        <td class="col-risk">${hsLevelBadge(h.risk)}</td>
        <td>${hsPortsCell(h, true)}</td>
        <td>${hsFwBadge(h)}</td>
        <td class="col-num mono">${h.cve_count ?? 0}</td>
        <td class="col-time mono muted">${hsEsc(hsFmtTime(h.finished_at))}</td>
      </tr>`;
    });
    html += `</tbody></table></div>`;
  }
  html += `</div>`;

  html += `<div class="cfg-panel hs-panel sec-panel">
    <div class="sec-panel-head">
      <div class="cfg-panel-title">${hsEsc(hsT("hs.history", "扫描历史"))}</div>
      <span class="sec-panel-meta">${Math.min(hsScans.length, 25)}${hsScans.length > 25 ? "+" : ""}</span>
    </div>`;
  if (!hsScans.length) {
    html += `<div class="sec-empty slim"><p>${hsEsc(hsT("hs.history_empty", "尚无历史记录"))}</p></div>`;
  } else {
    html += `<div class="nf-table-wrap hs-table-wrap"><table class="data-table hs-table hs-table-compact"><thead><tr>
      <th>${hsEsc(hsT("hs.batch", "批次"))}</th>
      <th>${hsEsc(hsT("hs.host", "主机"))}</th>
      <th class="col-risk">${hsEsc(hsT("hs.status", "状态"))}</th>
      <th class="col-score">${hsEsc(hsT("hs.score", "分"))}</th>
      <th>${hsEsc(hsT("hs.ports", "端口"))}</th>
      <th class="col-time">${hsEsc(hsT("hs.time", "时间"))}</th>
      <th></th>
    </tr></thead><tbody>`;
    hsScans.slice(0, 25).forEach(s => {
      const active = hsSelected && hsSelected.id === s.id ? " active-row" : "";
      const done = s.status === "completed";
      const cancelBtn = s.status === "running"
        ? `<button type="button" class="btn sm danger" data-hs-cancel="${hsEsc(s.id)}">${hsEsc(hsT("hs.cancel_scan", "取消"))}</button>`
        : "";
      html += `<tr class="sec-row${active}" data-scan="${hsEsc(s.id)}" title="${hsEsc(s.id)}">
        <td><div class="sec-batch">${hsEsc(hsScanLabel(s))}</div>
          <div class="mono muted sec-batch-id">${hsEsc(hsScanIdShort(s.id))}</div></td>
        <td><div class="hs-host-name">${hsEsc(s.hostname || s.host_id)}</div>
          ${done ? hsLevelBadge(s.risk) : ""}</td>
        <td class="col-risk">${hsStatusBadge(s.status)}</td>
        <td class="col-score">${done ? hsScoreCell(s.score, s.risk) : `<span class="muted">—</span>`}</td>
        <td>${done ? hsPortsCell(s, true) : `<span class="muted">—</span>`}</td>
        <td class="col-time mono muted">${hsEsc(hsFmtTime(s.finished_at || s.started_at))}</td>
        <td>${cancelBtn}</td>
      </tr>`;
    });
    html += `</tbody></table></div>`;
  }
  html += `</div></div>
    <div class="hs-col-side"><div id="hsDetail" class="cfg-panel hs-panel hs-detail sec-panel"></div></div>
  </div></div>`;
  el.innerHTML = html;

  el.querySelectorAll("[data-hs]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    hsAction(b.dataset.hs);
  }));
  el.querySelectorAll("[data-hsexport]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    document.querySelectorAll("#hsExportMenu.show").forEach(m => m.classList.remove("show"));
    hsDoExport(b.dataset.hsexport);
  }));
  el.querySelectorAll("tr[data-scan]").forEach(tr => tr.addEventListener("click", () => hsLoadDetail(tr.dataset.scan)));
  el.querySelectorAll("[data-hs-cancel]").forEach(btn => btn.addEventListener("click", async e => {
    e.preventDefault();
    e.stopPropagation();
    try {
      await hsFetchJSON(`${API}/security/host/scans/${encodeURIComponent(btn.dataset.hsCancel)}/cancel`, { method: "POST" });
      toast(hsT("hs.cancel_ok", "已取消扫描"), "ok");
      renderHostSecurity();
    } catch (err) { toast(err.message || String(err), "err"); }
  }));
  el.querySelectorAll("[data-hs-ports]").forEach(btn => btn.addEventListener("click", e => {
    e.preventDefault();
    e.stopPropagation();
    hsLoadDetail(btn.dataset.hsPorts, { focus: "ports" });
  }));
  const fimBox = $("hsCfgFIM");
  const fimDiffBox = $("hsCfgFIMDiff");
  if (fimBox && fimDiffBox) {
    const syncFIMDiff = () => { fimDiffBox.disabled = !fimBox.checked; };
    fimBox.addEventListener("change", syncFIMDiff);
    syncFIMDiff();
  }
  document.querySelectorAll("#hostSecurityPanel .hs-pick-tree-wrap").forEach(hsBindPickTree);
  if (hsSelected) hsPaintDetail(hsSelected);
  else {
    const box = $("hsDetail");
    if (box) {
      box.innerHTML = `<div class="sec-empty hs-detail-empty">
        <div class="sec-empty-ico" aria-hidden="true"></div>
        <h4>${hsEsc(hsT("hs.pick_scan_title", "选择一条扫描记录"))}</h4>
        <p>${hsEsc(hsT("hs.pick_scan_hint", "点击左侧主机或历史批次，查看防火墙、开放端口与风险明细，并导出报告。"))}</p>
      </div>`;
    }
  }
}

function hsAction(act) {
  if (act === "refresh") return renderHostSecurity();
  if (act === "scan") return hsRunScan();
  if (act === "ai" || act === "ai-diag") return hsAI("diagnosis");
  if (act === "ai-rem") return hsAI("remediation");
  if (act === "clear-filter") { hsPendingFilter = null; return paintHostSecurity(); }
  if (act === "export-toggle") {
    const menu = $("hsExportMenu");
    if (menu) menu.classList.toggle("show");
    return;
  }
  if (act === "cfg") {
    if (hsShowCfg) hsCapturePick("cfg");
    hsShowCfg = !hsShowCfg;
    if (hsShowCfg) { hsCfgPickInited = false; hsInitCfgPickDefaults(); }
    return paintHostSecurity();
  }
  if (act === "save-cfg") return hsSaveCfg();
}

document.addEventListener("click", () => {
  document.querySelectorAll("#hsExportMenu.show, #wsExportMenu.show").forEach(m => m.classList.remove("show"));
});

async function hsSaveCfg() {
  const status = $("hsCfgStatus");
  const kind = ($("hsCfgKind") && $("hsCfgKind").value) || "weekly";
  const atRaw = ($("hsCfgAt") && $("hsCfgAt").value || "").trim();
  const schedule = { enabled: !!($("hsCfgEnabled") && $("hsCfgEnabled").checked), kind };
  if (kind === "interval") schedule.interval_min = Math.max(15, parseInt(atRaw, 10) || 1440);
  else schedule.at = atRaw || "03:30";
  if (kind === "weekly") {
    schedule.weekday = parseInt(($("hsCfgWeekday") && $("hsCfgWeekday").value) || "0", 10) || 0;
  }
  hsCapturePick("cfg");
  const hostIds = [...hsCfgPickSelected].filter(id => (hsHosts || []).some(h => h.id === id));
  const body = {
    enabled: schedule.enabled,
    enable_clamav: !!($("hsCfgClam") && $("hsCfgClam").checked),
    fim_enabled: !!($("hsCfgFIM") && $("hsCfgFIM").checked),
    fim_content_diff: !!($("hsCfgFIMDiff") && $("hsCfgFIMDiff").checked),
    fim_scope: hsCfgVal("hsCfgFIMScope", (hsCfg && hsCfg.fim_scope) || "full"),
    fim_roots: hsCfgLines("hsCfgFIMRoots", (hsCfg && hsCfg.fim_roots) || []),
    fim_excludes: hsCfgLines("hsCfgFIMExcludes", (hsCfg && hsCfg.fim_excludes) || []),
    fim_content_paths: hsCfgLines("hsCfgFIMContent", (hsCfg && hsCfg.fim_content_paths) || []),
    fim_max_files: hsCfgNum("hsCfgFIMMaxFiles", (hsCfg && hsCfg.fim_max_files) || 150000),
    fim_max_changes: hsCfgNum("hsCfgFIMMaxChanges", (hsCfg && hsCfg.fim_max_changes) || 500),
    fim_budget_sec: hsCfgNum("hsCfgFIMBudget", (hsCfg && hsCfg.fim_budget_sec) || 90),
    auto_ai_summary: !!($("hsCfgAISummary") && $("hsCfgAISummary").checked),
    osv_url: (hsCfg && hsCfg.osv_url) || "",
    timeout_sec: (hsCfg && hsCfg.timeout_sec) || 180,
    host_ids: hostIds,
    schedule,
  };
  try {
    hsCfg = await hsFetchJSON(`${API}/security/host/config`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
    });
    if (status) { status.textContent = hsT("common.save", "保存") + hsT("toast.ok_suffix", "成功"); status.className = "cfg-status ok"; }
    if (typeof toast === "function") toast(hsT("toast.saved", "已保存"), "ok");
  } catch (e) {
    if (status) { status.textContent = e.message; status.className = "cfg-status err"; }
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

async function hsRunScan() {
  let ids = hsSelectedScanIds();
  if (!ids.length) {
    ids = (hsHosts || []).filter(h => h.online !== false).map(h => h.id);
    if (!ids.length) {
      if (typeof toast === "function") toast(hsT("hs.pick_host", "请选择主机"), "err");
      return;
    }
    if (typeof toast === "function") toast(hsT("hs.scan_all_online", "未勾选主机，将扫描全部在线主机"), "ok");
  }
  hsBusy = true;
  paintHostSecurity();
  try {
    const d = await hsFetchJSON(`${API}/security/host/scan`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ host_ids: ids }),
    });
    const first = (d.results || []).find(x => x.scan);
    if (first && first.scan) hsSelected = first.scan;
    if (typeof toast === "function") toast(hsT("hs.scan_started", "扫描已启动") + ` · ${ids.length}`, "ok");
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  } finally {
    hsBusy = false;
    await renderHostSecurity();
  }
}

function hsSoftRefresh(opts) {
  const full = !!(opts && opts.full);
  const anyRunning = (hsScans || []).some(s => s.status === "running") ||
    (hsSelected && hsSelected.status === "running");
  if (!full && anyRunning && !hsNeedsFullHistoryPaint()) {
    hsPatchRunningUI();
    return;
  }
  hsCapturePick("scan");
  if (hsShowCfg) hsCapturePick("cfg");
  paintHostSecurity();
  if (hsSelected) hsPaintDetail(hsSelected);
}

function hsCssEsc(id) {
  // 统一走 core.js 的 cssEsc（老内核里没有 CSS.escape，它自带退化实现）
  return cssEsc(String(id));
}

function hsNeedsFullHistoryPaint() {
  const panel = $("hostSecurityPanel");
  if (!panel || !panel.querySelector(".hs-shell")) return true;
  return (hsScans || []).slice(0, 25).some(s => !panel.querySelector(`tr[data-scan="${hsCssEsc(s.id)}"]`));
}

function hsPatchRunningUI() {
  const panel = $("hostSecurityPanel");
  if (!panel) return;
  const crit = hsSummary.filter(h => h.risk === "critical" || h.risk === "crit").length;
  const high = hsSummary.filter(h => h.risk === "high").length;
  const running = (hsScans || []).filter(s => s.status === "running").length;
  const metrics = panel.querySelectorAll(".sec-metrics .sec-metric b");
  if (metrics.length >= 4) {
    metrics[0].textContent = String(hsSummary.length);
    metrics[1].textContent = String(crit);
    metrics[2].textContent = String(high);
    metrics[3].textContent = String(running);
  }
  (hsScans || []).slice(0, 25).forEach(s => {
    const tr = panel.querySelector(`tr[data-scan="${hsCssEsc(s.id)}"]`);
    if (!tr) return;
    const statusTd = tr.querySelector("td.col-risk");
    if (statusTd) statusTd.innerHTML = hsStatusBadge(s.status);
    const actionTd = tr.lastElementChild;
    if (!actionTd) return;
    const hasCancel = !!actionTd.querySelector("[data-hs-cancel]");
    if (s.status === "running" && !hasCancel) {
      actionTd.innerHTML = `<button type="button" class="btn sm danger" data-hs-cancel="${hsEsc(s.id)}">${hsEsc(hsT("hs.cancel_scan", "取消"))}</button>`;
      const btn = actionTd.querySelector("[data-hs-cancel]");
      if (btn) btn.addEventListener("click", async e => {
        e.preventDefault();
        e.stopPropagation();
        try {
          await hsFetchJSON(`${API}/security/host/scans/${encodeURIComponent(btn.dataset.hsCancel)}/cancel`, { method: "POST" });
          toast(hsT("hs.cancel_ok", "已取消扫描"), "ok");
          renderHostSecurity();
        } catch (err) { toast(err.message || String(err), "err"); }
      });
    } else if (s.status !== "running" && hasCancel) {
      actionTd.innerHTML = "";
    }
  });
  if (hsSelected && hsSelected.status === "running") {
    const box = $("hsDetail");
    if (box && !box.querySelector(".hs-progress")) hsPaintDetail(hsSelected);
  }
}

if (!window._hsHostTreesRefreshBound) {
  window._hsHostTreesRefreshBound = true;
  let _hsTreeRefreshT = null;
  document.addEventListener("aiops:host-trees-refresh", () => {
    if (!document.querySelector("#view-host-security.active")) return;
    if (_hsTreeRefreshT) clearTimeout(_hsTreeRefreshT);
    _hsTreeRefreshT = setTimeout(() => {
      _hsTreeRefreshT = null;
      if (typeof LAST_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS) && LAST_HOSTS.length) {
        hsHosts = LAST_HOSTS;
      }
      if (typeof paintHostSecurity === "function") paintHostSecurity();
    }, 400);
  });
}

function hsMaybePoll() {
  if (hsPollTimer) { clearInterval(hsPollTimer); hsPollTimer = null; }
  const running = (hsScans || []).some(s => s.status === "running") || (hsSelected && hsSelected.status === "running");
  if (!running) return;
  let lastSig = "";
  hsPollTimer = setInterval(async () => {
    if (!document.querySelector("#view-host-security.active")) {
      clearInterval(hsPollTimer); hsPollTimer = null; return;
    }
    try {
      const prevStatus = hsSelected && hsSelected.status;
      hsScans = (await hsFetchJSON(`${API}/security/host/scans?limit=40`)).scans || [];
      const sig = (hsScans || []).map(s => `${s.id}:${s.status}:${s.finished_at || 0}`).join("|");
      const changed = sig !== lastSig;
      if (changed) {
        lastSig = sig;
        hsSummary = (await hsFetchJSON(`${API}/security/host/summary`)).hosts || [];
      }
      if (hsSelected && hsSelected.id) {
        const row = (hsScans || []).find(s => s.id === hsSelected.id);
        if (row && row.status === "running") {
          // Keep slim list row while running — avoid pulling findings/FIM every tick.
          hsSelected = Object.assign({}, hsSelected, row);
        } else if (row && prevStatus === "running" && row.status !== "running") {
          hsSelected = await hsFetchJSON(`${API}/security/host/scans/` + encodeURIComponent(hsSelected.id));
        } else if (row && row.status !== "running" && (!(hsSelected.findings || []).length)) {
          hsSelected = await hsFetchJSON(`${API}/security/host/scans/` + encodeURIComponent(hsSelected.id));
        }
      }
      const transitioned = prevStatus === "running" && hsSelected && hsSelected.status !== "running";
      if (transitioned || (changed && hsNeedsFullHistoryPaint())) {
        hsSoftRefresh({ full: true });
      } else if (changed || (hsSelected && hsSelected.status === "running")) {
        hsSoftRefresh();
      }
      if (!(hsScans || []).some(s => s.status === "running")) {
        clearInterval(hsPollTimer); hsPollTimer = null;
      }
    } catch (_) {}
  }, 3000);
}

async function hsLoadDetail(id, opts) {
  if (!id) return;
  try {
    hsSelected = await hsFetchJSON(`${API}/security/host/scans/` + encodeURIComponent(id));
    hsPaintDetail(hsSelected, opts);
    document.querySelectorAll("#hostSecurityPanel tr.sec-row").forEach(tr => {
      tr.classList.toggle("active-row", tr.dataset.scan === id);
    });
  } catch (e) {
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

function hsPaintDetail(scan, opts) {
  const box = $("hsDetail");
  if (!box || !scan) return;
  if (scan.status === "running") {
    box.innerHTML = `<div class="cfg-panel-title">${hsEsc(hsScanLabel(scan))}</div>
      <div class="hs-progress"><div class="hs-progress-bar"></div></div>
      <p class="ws-help">${hsEsc(hsT("hs.scanning", "扫描进行中…"))}</p>`;
    return;
  }
  const ports = hsUniquePorts(scan.open_ports || []);
  const riskyN = ports.filter(p => p.risk === "crit" || p.risk === "high" || p.risk === "medium").length;
  const portTotal = ports.length || scan.port_count || 0;
  const osLine = [scan.os, scan.distro].filter(Boolean).join(" / ");
  const sum = scan.summary || {};
  const canExport = scan.status === "completed";
  let html = `<div class="cfg-panel-head hs-detail-head"><div>
      <div class="cfg-panel-title">${hsEsc(hsT("hs.detail", "扫描详情"))} · ${hsEsc(scan.hostname || scan.host_id)}</div>
      <p class="cfg-panel-desc">${hsEsc(hsScanLabel(scan))} · <code class="mono muted">${hsEsc(hsScanIdShort(scan.id))}</code>${osLine ? ` · ${hsEsc(osLine)}` : ""}</p>
    </div>
    <div class="hs-detail-actions">
      ${hsStatusBadge(scan.status)}
      ${canExport ? `<div class="act-menu act-menu-ai">
        <button type="button" class="btn sm act-menu-trigger" aria-haspopup="true" aria-expanded="false">AI<span class="act-menu-caret">▾</span></button>
        <div class="act-menu-panel" hidden role="menu">
          <button type="button" role="menuitem" data-hs="ai-diag">${hsEsc(hsT("hs.ai_diag", "AI 研判"))}</button>
          <button type="button" role="menuitem" data-hs="ai-rem">${hsEsc(hsT("hs.ai_rem", "AI 修复"))}</button>
        </div>
      </div>
      <button class="btn sm primary" data-hs="export-toggle">${hsEsc(hsT("hs.export", "导出报告"))}</button>` : ""}
    </div></div>`;
  if (scan.error) html += `<div class="sec-error-box">${hsEsc(scan.error)}</div>`;
  if (scan.baseline_diff) {
    const d = scan.baseline_diff;
    html += `<div class="hint" style="margin:8px 0">较上次：新增 <b>${d.added || 0}</b> · 消失 <b>${d.removed || 0}</b> · 恶化 <b>${d.worsened || 0}</b> · 缓解 <b>${d.improved || 0}</b></div>`;
  }
  if (scan.ai_summary) {
    html += `<div class="sec-remediation" style="margin:8px 0"><div class="cfg-panel-title">${hsEsc(hsT("hs.ai_summary", "AI 摘要"))}</div>
      <pre class="mono" style="white-space:pre-wrap;margin:0;font-size:12px">${hsEsc(scan.ai_summary)}</pre></div>`;
  }
  const fimKPI = sum.fim || (scan.file_changes || []).length || 0;
  html += `<div class="sec-metrics compact hs-detail-kpis">
    <div class="sec-metric"><b>${scan.score ?? "—"}</b><span>${hsEsc(hsT("hs.score", "安全分"))}</span></div>
    <div class="sec-metric"><b>${portTotal}</b><span>${hsEsc(hsT("hs.ports_open", "开放端口"))}</span></div>
    <div class="sec-metric ${riskyN ? "high" : ""}"><b>${riskyN}</b><span>${hsEsc(hsT("hs.risky_ports", "高危端口"))}</span></div>
    <div class="sec-metric"><b>${scan.cve_count || 0}</b><span>CVE</span></div>
    <div class="sec-metric ${fimKPI ? "high" : ""}"><b>${fimKPI}</b><span>${hsEsc(hsT("hs.cat_fim", "文件变更"))}</span></div>
    <div class="sec-metric crit"><b>${sum.crit || 0}</b><span>${hsEsc(hsT("hs.level_crit", "危急"))}</span></div>
  </div>`;
  html += `<div class="hs-fw-panel">
    <div class="hs-fw-head"><span class="cfg-panel-title">${hsEsc(hsT("hs.firewall", "防火墙"))}</span> ${hsFwBadge(scan, { engine: true })}
      <span class="tag ${hsClamBadgeClass(scan)}" title="${hsEsc(hsT("hs.clam_db_help", "ClamAV 病毒库需每日更新；超过 7 天未更新会降低检出率"))}">${hsEsc(hsClamText(scan))}</span> ${hsLevelBadge(scan.risk)}</div>
    <p class="ws-help">${hsEsc(scan.firewall_detail || hsT("hs.fw_no_detail", "暂无防火墙引擎原始输出；可重新扫描以刷新状态。"))}</p>
  </div>`;
  if (ports.length) {
    html += `<div class="hs-port-panel" id="hsPortPanel"><div class="cfg-panel-title">${hsEsc(hsT("hs.ports_detail", "开放端口明细"))} <span class="tag">${ports.length}</span></div>
      <p class="ws-help">${hsEsc(hsT("hs.ports_help", "对外绑定（0.0.0.0 / ::）的数据库、远程桌面、缓存等高危服务会标红并计入评分。同端口 IPv4/IPv6 已合并展示。"))}</p>
      <div class="nf-table-wrap hs-table-wrap"><table class="data-table hs-table"><thead><tr>
        <th>${hsEsc(hsT("hs.port", "端口"))}</th><th>${hsEsc(hsT("hs.proto", "协议"))}</th>
        <th>${hsEsc(hsT("hs.bind", "绑定"))}</th><th>${hsEsc(hsT("hs.service", "服务"))}</th>
        <th>${hsEsc(hsT("hs.process", "进程"))}</th><th>${hsEsc(hsT("hs.risk", "风险"))}</th>
      </tr></thead><tbody>`;
    ports.slice(0, 200).forEach(p => {
      html += `<tr>
        <td class="mono"><strong>${p.port}</strong></td>
        <td class="mono">${hsEsc(p.proto || "tcp")}</td>
        <td class="mono">${hsEsc(p.addr || "*")}${p.public ? ` <span class="badge warn">${hsEsc(hsT("hs.bind_public", "对外"))}</span>` : ""}</td>
        <td>${hsEsc(p.service || "—")}</td>
        <td class="mono muted">${hsEsc(p.process || "—")}</td>
        <td>${p.risk ? hsLevelBadge(p.risk) : `<span class="badge ok">${hsEsc(hsT("hs.level_info", "信息"))}</span>`}</td>
      </tr>`;
    });
    html += `</tbody></table></div></div>`;
  } else if (opts && opts.focus === "ports") {
    html += `<div class="hs-port-panel" id="hsPortPanel"><div class="empty-line">${hsEsc(hsT("hs.ports_unknown", "需重新扫描后可查看端口明细"))}</div></div>`;
  }
  // File integrity changes (FIM) — panel chrome aligned with firewall / ports.
  const fileChanges = scan.file_changes || [];
  const fimCount = sum.fim || fileChanges.length || 0;
  const fimStats = scan.fim_stats || null;
  const scopeLine = hsFimScopeLine(scan);
  const scopeHTML = scopeLine ? `<p class="ws-help">${hsEsc(scopeLine)}</p>` : "";
  if (scan.fim_baseline_established) {
    html += `<div class="hs-fim-panel" id="hsFimPanel">
      <div class="hs-fw-head"><span class="cfg-panel-title">${hsEsc(hsT("hs.fim_title", "文件变更"))}</span>
        <span class="badge info">${hsEsc(hsT("hs.fim_baseline_tag", "基线"))}</span></div>
      <p class="ws-help">${hsEsc(hsT("hs.fim_baseline", "已建立文件基线，下次扫描起显示变更"))}</p>${scopeHTML}</div>`;
  } else if (fileChanges.length) {
    html += `<div class="hs-fim-panel" id="hsFimPanel">
      <div class="hs-fw-head"><span class="cfg-panel-title">${hsEsc(hsT("hs.fim_title", "文件变更"))}</span>
        <span class="tag">${fileChanges.length}</span>
        ${fimCount ? `<span class="badge warn">${fimCount} ${hsEsc(hsT("hs.cat_fim", "文件变更"))}</span>` : ""}
        ${fimStats && fimStats.truncated ? `<span class="badge">${hsEsc(hsT("hs.fim_truncated", "已截断"))}</span>` : ""}</div>
      <p class="ws-help">${hsEsc(hsT("hs.fim_help", "相对上次基线的增删改；仅内容审计白名单内的文本可展开查看脱敏差异，其余仅记录路径与元数据。"))}</p>
      ${scopeHTML}
      <div class="nf-table-wrap hs-table-wrap"><table class="data-table hs-table"><thead><tr>
        <th>${hsEsc(hsT("hs.fim_path", "路径"))}</th>
        <th>${hsEsc(hsT("hs.fim_change", "变更类型"))}</th>
        <th>${hsEsc(hsT("hs.fim_reason", "依据"))}</th>
        <th>${hsEsc(hsT("hs.fim_mtime", "时间"))}</th>
        <th>${hsEsc(hsT("hs.fim_sha", "SHA 摘要"))}</th>
        <th></th>
      </tr></thead><tbody>`;
    fileChanges.slice(0, 300).forEach((c, i) => {
      const mtStr = hsFmtUnix(c.new_mtime || c.old_mtime || 0);
      const sha = c.change === "removed"
        ? hsShortSHA(c.old_sha)
        : (c.change === "modified" ? `${hsShortSHA(c.old_sha)} → ${hsShortSHA(c.new_sha)}` : hsShortSHA(c.new_sha));
      const hasDiff = c.change === "modified" && !!c.diff;
      html += `<tr class="hs-fim-row">
        <td class="mono">${hsEsc(c.path || "")}</td>
        <td>${hsFimChangeBadge(c.change)}</td>
        <td class="muted">${hsEsc(hsFimReasonLabel(c))}</td>
        <td class="muted">${hsEsc(mtStr)}</td>
        <td class="mono muted">${hsEsc(sha || "—")}</td>
        <td>${hasDiff ? `<button type="button" class="btn sm" data-fim-toggle="${i}">${hsEsc(hsT("hs.fim_show_diff", "差异"))}</button>` : ""}</td>
      </tr>`;
      if (hasDiff) {
        html += `<tr class="hs-fim-diff-row" id="hsFimDiff${i}" hidden><td colspan="6"><pre class="mono hs-fim-diff">${hsEsc(c.diff)}${c.truncated ? "\n…(" + hsEsc(hsT("hs.fim_truncated", "已截断")) + ")" : ""}</pre></td></tr>`;
      }
    });
    html += `</tbody></table></div></div>`;
  } else if ((fimStats || (scan.file_inventory || []).length) && scan.status === "completed") {
    const invN = (scan.file_inventory || []).length;
    html += `<div class="hs-fim-panel" id="hsFimPanel">
      <div class="hs-fw-head"><span class="cfg-panel-title">${hsEsc(hsT("hs.fim_title", "文件变更"))}</span>
        <span class="badge ok">${hsEsc(hsT("hs.fim_none_tag", "无变更"))}</span>
        ${invN ? `<span class="muted" style="font-size:11px">${invN} ${hsEsc(hsT("hs.fim_inv_count", "个受监控文件"))}</span>` : ""}</div>
      <p class="ws-help">${hsEsc(hsT("hs.fim_none", "相对上次基线无文件变更"))}</p>${scopeHTML}</div>`;
  } else if (scan.status === "completed") {
    html += `<div class="hs-fim-panel" id="hsFimPanel">
      <div class="hs-fw-head"><span class="cfg-panel-title">${hsEsc(hsT("hs.fim_title", "文件变更"))}</span>
        <span class="badge">${hsEsc(hsT("hs.fim_empty_tag", "无清单"))}</span></div>
      <p class="ws-help">${hsEsc(hsT("hs.fim_empty_help", "本次未采集到文件基线数据（权限不足、FIM 关闭或 Agent 过旧）。请确认已开启文件完整性监控，并以有权限的账户运行 Agent 后重新扫描。"))}</p></div>`;
  }
  html += hsCompliancePanel(scan);
  if ((scan.remediation || []).length) {
    html += `<div class="sec-remediation"><div class="cfg-panel-title">${hsEsc(hsT("hs.remediation", "修复建议"))}</div><ul>`;
    const seenTips = new Set();
    scan.remediation.forEach(t => {
      const key = String(t || "").trim().toLowerCase();
      if (!key || seenTips.has(key)) return;
      seenTips.add(key);
      html += `<li>${hsEsc(t)}</li>`;
    });
    html += `</ul></div>`;
  }
  const allFindings = scan.findings || [];
  const filterLevel = hsPendingFilter && hsPendingFilter.level;
  const shownIdx = [];
  allFindings.forEach((f, idx) => {
    if (filterLevel && !hsFindingMatchesFilter(f, filterLevel)) return;
    shownIdx.push(idx);
  });
  html += `<div class="cfg-panel-title">${hsEsc(hsT("hs.findings", "风险明细"))} <span class="tag">${filterLevel ? shownIdx.length + "/" + allFindings.length : allFindings.length}</span></div>`;
  html += `<div class="nf-table-wrap hs-table-wrap"><table class="data-table hs-table"><thead><tr>
    <th>${hsEsc(hsT("hs.level", "级别"))}</th><th>${hsEsc(hsT("hs.category", "类别"))}</th>
    <th>${hsEsc(hsT("hs.title", "标题"))}</th><th>CVE</th><th>${hsEsc(hsT("hs.suggest", "建议"))}</th>
    <th>${hsEsc(hsT("hs.finding_status", "状态"))}</th>
    <th>${hsEsc(hsT("hs.finding_ai", "AI"))}</th>
  </tr></thead><tbody>`;
  shownIdx.slice(0, 200).forEach(idx => {
    const f = allFindings[idx];
    html += `<tr>
      <td>${hsLevelBadge(f.level)}</td>
      <td><span class="tag">${hsEsc(hsCatLabel(f.category))}</span></td>
      <td>${hsEsc(f.title)}<div class="field-hint">${hsEsc(f.detail || "")}</div>${hsComplianceTags(f)}</td>
      <td class="mono">${hsEsc(f.cve || f.id || "")}</td>
      <td>${hsEsc(f.suggest || "")}</td>
      <td>${hsFindingStatusControls(f)}</td>
      <td><button type="button" class="btn sm nf-ai-btn" data-hs-finding="${idx}" title="${hsEsc(hsT("hs.ai_finding_tip", "针对本条给出研判与修复建议"))}">${hsEsc(hsT("hs.ai_finding", "建议"))}</button></td>
    </tr>`;
  });
  if (!shownIdx.length) {
    html += `<tr><td colspan="7" class="empty-line">${hsEsc(filterLevel ? hsT("hs.no_filtered", "当前筛选下无待处置项") : hsT("hs.no_findings", "未发现风险项"))}</td></tr>`;
  }
  html += `</tbody></table></div>`;
  box.innerHTML = html;
  box.querySelectorAll(".hs-finding-status").forEach(sel => {
    sel.addEventListener("change", () => {
      const fid = sel.dataset.hsfid || "";
      const cat = sel.dataset.hscat || "";
      const detail = sel.dataset.hsdetail || "";
      const pkg = sel.dataset.hspkg || "";
      const finding = (scan.findings || []).find(x =>
        (x.id || "") === fid &&
        (x.category || "") === cat &&
        (x.detail || "") === detail &&
        (x.package || "") === pkg
      ) || (scan.findings || []).find(x => (x.id || "") === fid && (x.category || "") === cat);
      if (finding) hsUpdateFindingStatus(finding, sel.value);
    });
  });
  box.querySelectorAll("[data-hs-finding]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    const idx = parseInt(b.dataset.hsFinding, 10);
    hsAIFinding(scan, idx);
  }));
  box.querySelectorAll("[data-fim-toggle]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    const idx = b.dataset.fimToggle;
    const row = $("hsFimDiff" + idx);
    if (!row) return;
    const open = row.hasAttribute("hidden");
    if (open) row.removeAttribute("hidden");
    else row.setAttribute("hidden", "");
    b.textContent = open ? hsT("hs.fim_hide_diff", "收起") : hsT("hs.fim_show_diff", "差异");
  }));
  box.querySelectorAll("[data-hs]").forEach(b => b.addEventListener("click", e => {
    e.stopPropagation();
    hsAction(b.dataset.hs);
  }));
  const expBtn = document.querySelector("#hostSecurityPanel [data-hs=\"export-toggle\"]");
  if (expBtn) expBtn.disabled = !canExport;
  if (opts && opts.focus === "ports") {
    const panel = $("hsPortPanel") || box;
    try { panel.scrollIntoView({ behavior: "smooth", block: "start" }); } catch (_) {}
    panel.classList.add("hs-port-panel-focus");
    setTimeout(() => panel.classList.remove("hs-port-panel-focus"), 1600);
  }
}

function hsBuildReportModel(scan) {
  const sum = scan.summary || {};
  const ports = hsUniquePorts(scan.open_ports || []);
  const riskyN = ports.filter(p => p.risk === "crit" || p.risk === "high" || p.risk === "medium").length
    || scan.risky_port_count || 0;
  const portCount = ports.length || scan.port_count || 0;
  const fwText = hsFwLabel(scan.firewall) + (scan.firewall_engine ? ` (${scan.firewall_engine})` : "");
  const osLine = [scan.os, scan.distro].filter(Boolean).join(" / ") || "—";
  const narrative = [
    `# ${hsT("hs.report_exec", "执行摘要")}`,
    "",
    hsT("hs.report_exec_body", "主机「{host}」安全扫描已完成，安全分 {score}，风险等级 {risk}。防火墙：{fw}；ClamAV：{clam}；软件包 {pkgs} 个，匹配 CVE {cves} 条；开放端口 {ports} 个，其中高危 {risky} 个。")
      .replace("{host}", scan.hostname || scan.host_id)
      .replace("{score}", String(scan.score ?? "—"))
      .replace("{risk}", hsLevelLabel(scan.risk))
      .replace("{fw}", fwText)
      .replace("{clam}", hsClamText(scan))
      .replace("{pkgs}", String(scan.pkg_count || 0))
      .replace("{cves}", String(scan.cve_count || 0))
      .replace("{ports}", String(portCount))
      .replace("{risky}", String(riskyN)),
    "",
    `# ${hsT("hs.remediation", "修复建议")}`,
    ...((scan.remediation || []).length ? scan.remediation.map(t => `- ${t}`) : [`- ${hsT("hs.no_remediation", "暂无额外修复建议")}`]),
  ].join("\n");
  const findingRows = (scan.findings || []).map(f => [
    hsLevelLabel(f.level), hsCatLabel(f.category),
    f.title || "", f.cve || f.id || "", f.detail || "", f.suggest || "",
    hsComplianceText(f) || "—",
  ]);
  const fwDetail = String(scan.firewall_detail || "").trim();
  const fwRows = [
    [hsT("hs.firewall", "防火墙状态"), hsFwLabel(scan.firewall)],
    [hsT("hs.fw_engine", "防火墙引擎"), scan.firewall_engine || "—"],
    [hsT("hs.fw_detail", "引擎详情"), fwDetail || hsT("hs.fw_no_detail", "暂无原始输出")],
  ];
  const portRows = ports.map(p => [
    String(p.port),
    p.proto || "tcp",
    p.addr || "*",
    p.public ? hsT("hs.bind_public", "对外") : hsT("hs.bind_local", "本机/内网"),
    p.service || "—",
    p.process || "—",
    p.risk ? hsLevelLabel(p.risk) : hsT("hs.level_info", "信息"),
  ]);
  const fimRows = (scan.file_changes || []).map(c => [
    c.path || "",
    hsFimChangeLabel(c.change),
    hsFimReasonLabel(c),
    hsFmtUnix(c.new_mtime || c.old_mtime || 0),
    c.change === "removed" ? hsShortSHA(c.old_sha)
      : (c.change === "modified" ? `${hsShortSHA(c.old_sha)} → ${hsShortSHA(c.new_sha)}` : hsShortSHA(c.new_sha)),
    c.diff ? (c.truncated ? hsT("hs.fim_truncated", "已截断") : hsT("hs.fim_has_diff", "有差异")) : "—",
  ]);
  let fimNarrative = "";
  if (scan.fim_baseline_established) {
    fimNarrative = hsT("hs.fim_baseline", "已建立文件基线，下次扫描起显示变更");
  } else if (fimRows.length) {
    fimNarrative = hsT("hs.fim_report_summary", "相对上次基线检测到 {n} 处文件变更。").replace("{n}", String(fimRows.length));
  }
  return {
    title: hsT("hs.report_title", "主机安全扫描报告") + " — " + (scan.hostname || scan.host_id),
    subtitle: hsT("hs.report_sub", "生成时间") + " " + new Date().toLocaleString() + " · " + hsScanLabel(scan),
    summaryTitle: hsT("hs.report_meta", "报告摘要"),
    narrativeTitle: hsT("hs.report_analysis", "分析结论与建议"),
    meta: [
      [hsT("hs.batch", "扫描批次"), hsScanLabel(scan)],
      [hsT("hs.host", "主机"), scan.hostname || scan.host_id || ""],
      [hsT("hs.os", "系统"), osLine],
      [hsT("hs.score", "安全分"), String(scan.score ?? "—")],
      [hsT("hs.risk", "风险等级"), hsLevelLabel(scan.risk)],
      [hsT("hs.firewall", "防火墙"), fwText],
      ["ClamAV", hsClamText(scan)],
      ["CVE", String(scan.cve_count || 0)],
      [hsT("hs.pkgs", "软件包"), String(scan.pkg_count || 0)],
      [hsT("hs.ports_open", "开放端口"), String(portCount)],
      [hsT("hs.risky_ports", "高危端口"), String(riskyN)],
      [hsT("hs.cat_fim", "文件变更"), String(sum.fim || fimRows.length || 0)],
      [hsT("hs.status", "状态"), hsStatusLabel(scan.status)],
      [hsT("hs.time", "完成时间"), hsFmtTime(scan.finished_at)],
    ],
    kpis: [
      [hsT("hs.score", "安全分"), String(scan.score ?? "—")],
      [hsT("hs.ports_open", "开放端口"), String(portCount)],
      [hsT("hs.risky_ports", "高危端口"), String(riskyN)],
      [hsT("hs.cat_fim", "文件变更"), String(sum.fim || fimRows.length || 0)],
      [hsT("hs.level_crit", "危急"), String(sum.crit || 0)],
      [hsT("hs.level_high", "高危"), String(sum.high || 0)],
      ["CVE", String(scan.cve_count || 0)],
    ],
    narrative: fimNarrative ? (narrative + "\n\n# " + hsT("hs.fim_title", "文件变更") + "\n\n" + fimNarrative) : narrative,
    sections: [
      {
        title: hsT("hs.report_fw_sec", "防火墙状态"),
        columns: [hsT("hs.report_item", "项"), hsT("hs.report_value", "值")],
        rows: fwRows,
      },
      {
        title: hsT("hs.ports_detail", "开放端口明细"),
        columns: [
          hsT("hs.port", "端口"), hsT("hs.proto", "协议"), hsT("hs.bind", "绑定"),
          hsT("hs.bind_scope", "暴露面"), hsT("hs.service", "服务"),
          hsT("hs.process", "进程"), hsT("hs.risk", "风险"),
        ],
        rows: portRows.length
          ? portRows
          : [[hsT("hs.ports_unknown", "需重新扫描后可查看端口明细"), "", "", "", "", "", ""]],
      },
      {
        title: hsT("hs.fim_title", "文件变更"),
        columns: [
          hsT("hs.fim_path", "路径"), hsT("hs.fim_change", "变更类型"),
          hsT("hs.fim_reason", "依据"),
          hsT("hs.fim_mtime", "时间"), hsT("hs.fim_sha", "SHA 摘要"),
          hsT("hs.fim_diff_col", "内容差异"),
        ],
        rows: fimRows.length
          ? fimRows
          : [[scan.fim_baseline_established
            ? hsT("hs.fim_baseline", "已建立文件基线，下次扫描起显示变更")
            : hsT("hs.fim_none", "相对上次基线无文件变更"), "", "", "", "", ""]],
      },
      {
        title: hsT("hs.findings", "风险明细"),
        columns: [
          hsT("hs.level", "级别"), hsT("hs.category", "类别"), hsT("hs.title", "标题"),
          "CVE", hsT("hs.detail_col", "详情"), hsT("hs.suggest", "建议"),
          hsT("hs.compliance", "合规映射"),
        ],
        rows: findingRows.length ? findingRows : [[hsT("hs.no_findings", "未发现风险项"), "", "", "", "", "", ""]],
      },
    ],
    footer: hsT("hs.report_footer", "本报告由 AIOps 安全中心自动生成，仅供运维处置参考，不替代专业渗透测试。"),
    orientation: "landscape",
    rawJSON: {
      report_type: "host_security",
      generated_at: new Date().toISOString(),
      scan_id: scan.id,
      label: hsScanLabel(scan),
      host_id: scan.host_id,
      hostname: scan.hostname,
      os: scan.os,
      distro: scan.distro,
      score: scan.score,
      risk: scan.risk,
      status: scan.status,
      clamav: scan.clamav,
      firewall: scan.firewall,
      firewall_engine: scan.firewall_engine,
      firewall_detail: scan.firewall_detail,
      cve_count: scan.cve_count,
      pkg_count: scan.pkg_count,
      port_count: portCount,
      risky_port_count: riskyN,
      open_ports: ports,
      file_changes: scan.file_changes || [],
      file_inventory: scan.file_inventory || [],
      fim_baseline_established: !!scan.fim_baseline_established,
      fim_stats: scan.fim_stats || null,
      remediation: scan.remediation || [],
      findings: scan.findings || [],
      summary: sum,
      started_at: scan.started_at,
      finished_at: scan.finished_at,
    },
  };
}

async function hsEnsureSelectedScan() {
  if (!hsSelected || !hsSelected.id) return null;
  if (hsSelected.status !== "completed") return null;
  // Re-fetch full scan so export always includes open_ports / firewall_detail.
  try {
    hsSelected = await hsFetchJSON(`${API}/security/host/scans/` + encodeURIComponent(hsSelected.id));
  } catch (_) { /* use cached */ }
  return hsSelected && hsSelected.status === "completed" ? hsSelected : null;
}

async function hsDoExport(fmt) {
  const scan = await hsEnsureSelectedScan();
  if (!scan) {
    if (typeof toast === "function") toast(hsT("hs.pick_scan", "请先选择一条已完成的扫描结果"), "err");
    return;
  }
  try {
    const model = hsBuildReportModel(scan);
    const ok = await exportModel(model, fmt, "主机安全报告_" + (scan.hostname || scan.host_id));
    if (ok === false && typeof toast === "function") toast(hsT("hs.export_popup", "浏览器拦截了导出窗口，请允许弹窗后重试"), "err");
    else if (fmt !== "pdf" && typeof toast === "function") toast(hsT("toast.exported", "已导出"), "ok");
  } catch (e) {
    if (typeof toast === "function") toast(hsT("hs.export_fail", "导出失败") + "：" + (e.message || e), "err");
  }
}

function hsAIContext(scan, maxFindings) {
  const model = hsBuildReportModel(scan);
  const hostId = scan.host_id || "";
  return {
    hostId,
    text: (model.narrative + "\n\n" + JSON.stringify({
      host_id: hostId,
      hostname: scan.hostname,
      os: scan.os,
      distro: scan.distro,
      score: scan.score,
      risk: scan.risk,
      clamav: scan.clamav,
      firewall: scan.firewall,
      meta: model.meta,
      findings: (scan.findings || []).slice(0, maxFindings || 40),
    }, null, 2)).slice(0, 14000),
  };
}

function hsAI(kind) {
  if (!hsSelected || hsSelected.status === "running") {
    if (typeof toast === "function") toast(hsT("hs.pick_scan", "请先选择一条已完成的扫描结果"), "err");
    return;
  }
  if (hsSelected.status !== "completed" && hsSelected.status !== "failed") {
    if (typeof toast === "function") toast(hsT("hs.pick_scan", "请先选择一条已完成的扫描结果"), "err");
    return;
  }
  const mode = kind === "remediation" ? "remediation" : "diagnosis";
  const { hostId, text } = hsAIContext(hsSelected, 40);
  if (typeof openAIAssist !== "function") return;
  if (mode === "remediation") {
    openAIAssist({
      task: "host_security_remediation", mode: "analyze",
      title: hsT("hs.ai_rem_title", "AI · 主机安全修复计划"),
      context: text,
      applyLabel: hsT("ai.apply_actions", "应用建议动作"),
      applyTo: async (code) => {
        if (typeof window.applyOpsActionPlan !== "function") return false;
        return window.applyOpsActionPlan(code, {
          source: "host-security",
          hostId,
          refresh: () => renderHostSecurity(),
        });
      },
    });
    return;
  }
  openAIAssist({
    task: "host_security_diagnosis", mode: "analyze",
    title: hsT("hs.ai_diag_title", "AI · 主机安全研判"),
    context: text,
    hint: hsT("hs.ai_diag_hint", "正在研判整体风险、优先级与疑似误报…"),
  });
}

function hsAIFinding(scan, idx) {
  if (!scan || typeof openAIAssist !== "function") return;
  const findings = scan.findings || [];
  const f = findings[idx];
  if (!f) {
    if (typeof toast === "function") toast(hsT("hs.finding_missing", "未找到该风险项"), "err");
    return;
  }
  const ctx = {
    host_id: scan.host_id,
    hostname: scan.hostname,
    os: scan.os,
    distro: scan.distro,
    score: scan.score,
    risk: scan.risk,
    finding: f,
    peers_same_category: findings.filter(x => x !== f && x.category === f.category).slice(0, 5).map(x => ({
      level: x.level, id: x.id, title: x.title, cve: x.cve,
    })),
  };
  openAIAssist({
    task: "host_security_finding", mode: "analyze",
    title: hsT("hs.ai_finding_title", "AI · 单条风险建议") + " · " + (f.title || f.id || "").slice(0, 40),
    context: JSON.stringify(ctx, null, 2).slice(0, 12000),
    hint: hsT("hs.ai_finding_hint", "正在分析本条 finding 的真伪、影响与修复步骤…"),
    applyLabel: hsT("hs.ai_apply_status", "按建议更新状态"),
    applyTo: async (text) => {
      const low = String(text || "").toLowerCase();
      let status = "";
      if (/\bfalse[_\s-]?positive\b/.test(low) || low.includes("误报")) status = "false_positive";
      else if (/\bresolved\b/.test(low) || low.includes("已修复") || low.includes("可关闭")) status = "resolved";
      else if (/\back\b/.test(low) || low.includes("已知接受") || low.includes("暂时接受")) status = "ack";
      if (!status) {
        if (typeof toast === "function") toast(hsT("hs.ai_status_unclear", "未从回复中识别到明确状态建议，请手动选择"), "warn");
        return false;
      }
      await hsUpdateFindingStatus(f, status);
      return true;
    },
  });
}

window._pageRenderers = window._pageRenderers || {};
window._pageRenderers["host-security"] = renderHostSecurity;
})();
