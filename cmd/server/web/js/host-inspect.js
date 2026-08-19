/* ===== 主机深度巡检（编排页 Tab · Web 报告 · 多机汇总 · 导出） ===== */
let INSP_BATCHES = [];
let INSP_ACTIVE_ID = "";
let INSP_POLL = null;
let INSP_VIEW_ITEM = null; // {batchId, hostId} | null = fleet summary
let INSP_VIEW_MODE = "fleet"; // fleet | host
let INSP_HOST_Q = "";
let INSP_FOCUS_SEARCH = false;
let INSP_HOSTS_LOADING = false;
let INSP_HOSTS_ERR = "";
let INSP_LOAD_SEQ = 0;
let INSP_SELECTED = new Set(); // preserve checkbox selection across re-renders
let INSP_PICK_COLLAPSED = new Set();

function inspT(k, fb) { return (window.I18N && I18N.t) ? I18N.t(k, fb) : fb; }

function switchAutoTab(tab) {
  document.querySelectorAll("#autoTabs .chip-btn").forEach(b => b.classList.toggle("active", b.dataset.autotab === tab));
  document.querySelectorAll("#view-automation .sre-panel").forEach(p => p.classList.toggle("active", p.id === "autoPanel-" + tab));
  if (tab === "inspect") loadHostInspect();
  if (tab === "playbooks" && typeof loadPlaybooks === "function") loadPlaybooks();
}

document.querySelectorAll("#autoTabs .chip-btn").forEach(b => {
  b.addEventListener("click", () => switchAutoTab(b.dataset.autotab));
});

function inspCaptureSelection() {
  const root = $("inspHostList");
  if (!root || !window.HostPicker) return;
  const set = HostPicker.readMulti(root);
  INSP_SELECTED.clear();
  set.forEach(id => INSP_SELECTED.add(id));
}

function inspPanelActive() {
  const panel = $("autoPanel-inspect");
  const view = $("view-automation");
  return !!(panel && panel.classList.contains("active") && view && view.classList.contains("active"));
}

async function loadHostInspect(opts) {
  const forceHosts = !opts || opts.forceHosts !== false;
  const seq = ++INSP_LOAD_SEQ;
  const box = $("inspHostList");
  const warm = (typeof LAST_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS) && LAST_HOSTS.length)
    ? LAST_HOSTS
    : (window._cachedHosts && window._cachedHosts.length ? window._cachedHosts : []);

  if (typeof loadHostFolders === "function") {
    try { await loadHostFolders(); } catch (_) {}
  }

  // Paint warm cache immediately so the picker never sits empty waiting on /hosts.
  if (warm.length) {
    if (typeof syncHostCache === "function" && (!LAST_HOSTS || !LAST_HOSTS.length)) syncHostCache(warm);
    renderInspHostPicker();
  } else if (box) {
    INSP_HOSTS_LOADING = true;
    INSP_HOSTS_ERR = "";
    box.innerHTML = `<div class="insp-empty-mini">${esc(inspT("inspect.hosts_loading", "正在加载主机列表…"))}</div>`;
  }

  const hostsP = (typeof fetchHostsList === "function"
    ? fetchHostsList({ force: forceHosts, maxAgeMs: forceHosts ? 0 : 20000 })
    : fetch(`${API}/hosts`, { credentials: "same-origin" }).then(r => r.json()).then(j => {
        const list = Array.isArray(j) ? j : (j && j.hosts) || [];
        if (typeof syncHostCache === "function") return syncHostCache(list);
        LAST_HOSTS = list; return list;
      })
  ).catch(err => {
    INSP_HOSTS_ERR = String(err && err.message ? err.message : err);
    console.warn("inspect hosts:", err);
    return (LAST_HOSTS && LAST_HOSTS.length) ? LAST_HOSTS : [];
  });

  const batchesP = fetch(`${API}/host-inspect`, { credentials: "same-origin" })
    .then(r => r.json())
    .then(j => Array.isArray(j) ? j : [])
    .catch(e => {
      console.warn("load host-inspect:", e);
      return INSP_BATCHES || [];
    });

  // Progressive: render hosts as soon as ready (don't wait for batch history).
  hostsP.then(list => {
    if (seq !== INSP_LOAD_SEQ) return;
    INSP_HOSTS_LOADING = false;
    if (!list.length && INSP_HOSTS_ERR) {
      /* keep error for picker */
    } else {
      INSP_HOSTS_ERR = "";
    }
    renderInspHostPicker();
  });

  const [, batches] = await Promise.all([hostsP, batchesP]);
  if (seq !== INSP_LOAD_SEQ) return;
  INSP_BATCHES = batches;
  renderInspBatches();
  if (INSP_ACTIVE_ID) {
    const b = INSP_BATCHES.find(x => x.id === INSP_ACTIVE_ID);
    if (b && b.status === "running") startInspPoll(INSP_ACTIVE_ID);
  }
}

function inspHostsPool() {
  return (typeof LAST_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS) && LAST_HOSTS.length)
    ? LAST_HOSTS
    : (window._cachedHosts && Array.isArray(window._cachedHosts) ? window._cachedHosts : []);
}

function renderInspHostPicker() {
  const box = $("inspHostList");
  if (!box) return;
  inspCaptureSelection();
  const hosts = inspHostsPool();
  if (INSP_HOSTS_LOADING && !hosts.length) {
    box.innerHTML = `<div class="insp-empty-mini">${esc(inspT("inspect.hosts_loading", "正在加载主机列表…"))}</div>`;
    return;
  }
  if (!hosts.length) {
    const err = INSP_HOSTS_ERR
      ? `<div class="insp-empty-mini crit">${esc(inspT("inspect.hosts_fail", "主机列表加载失败"))}: ${esc(INSP_HOSTS_ERR)}</div>
         <button type="button" class="btn sm" id="inspRetryHostsBtn">${esc(inspT("inspect.hosts_retry", "重试加载"))}</button>`
      : `<div class="insp-empty-mini">${esc(inspT("inspect.no_host_cache", "暂无主机列表，正在尝试加载…"))}</div>
         <button type="button" class="btn sm" id="inspRetryHostsBtn">${esc(inspT("inspect.hosts_retry", "重试加载"))}</button>`;
    box.innerHTML = err;
    const btn = box.querySelector("#inspRetryHostsBtn");
    if (btn) btn.onclick = () => loadHostInspect({ forceHosts: true });
    return;
  }
  if (!window.HostPicker) {
    box.innerHTML = `<div class="insp-empty-mini">${esc(inspT("inspect.picker_missing", "主机选择器未加载"))}</div>`;
    return;
  }
  box.innerHTML = HostPicker.renderHTML({
    id: "inspHostTree",
    mode: "multi",
    hosts,
    selected: INSP_SELECTED,
    collapsed: INSP_PICK_COLLAPSED,
    q: INSP_HOST_Q,
    onlineOnly: true,
  });
  const root = box.querySelector(".host-picker") || box;
  root._hpBound = false;
  HostPicker.bind(root, {
    onToggleFold: (id) => {
      inspCaptureSelection();
      if (INSP_PICK_COLLAPSED.has(id)) INSP_PICK_COLLAPSED.delete(id); else INSP_PICK_COLLAPSED.add(id);
      renderInspHostPicker();
    },
    onSearch: (q) => {
      inspCaptureSelection();
      INSP_HOST_Q = q || "";
      INSP_FOCUS_SEARCH = true;
      renderInspHostPicker();
    },
    onQuick: (act) => {
      inspCaptureSelection();
      if (act === "clear") INSP_SELECTED.clear();
      else hosts.filter(h => h.online).forEach(h => INSP_SELECTED.add(h.id));
      renderInspHostPicker();
    },
    onFolderToggle: (fid, checked) => {
      inspCaptureSelection();
      const q = (INSP_HOST_Q || "").trim().toLowerCase();
      const byFolder = HostPicker.hostsByFolder(hosts);
      let ids = [];
      if (String(fid).startsWith("cat:")) {
        const cat = String(fid).slice(4);
        ids = hosts.filter(h => {
          const c = (h.category || "").trim() || inspT("hs.ungrouped", "未分组");
          return c === cat && HostPicker.filterHost(h, q) && h.online;
        }).map(h => h.id);
      } else if (fid === "__ungrouped__") {
        ids = (byFolder.get("__ungrouped__") || []).filter(h => HostPicker.filterHost(h, q) && h.online).map(h => h.id);
      } else {
        const folders = HostPicker.folderTree();
        const find = (nodes) => {
          for (const n of nodes || []) {
            if (n.id === fid) return n;
            const c = find(n.children || []);
            if (c) return c;
          }
          return null;
        };
        const node = find(folders);
        if (node) ids = HostPicker.collectFolderHostIds(node, byFolder, q, true);
      }
      ids.forEach(id => { if (checked) INSP_SELECTED.add(id); else INSP_SELECTED.delete(id); });
      renderInspHostPicker();
    },
    onHostToggle: (id, checked) => {
      if (checked) INSP_SELECTED.add(id); else INSP_SELECTED.delete(id);
    },
  });
  if (INSP_FOCUS_SEARCH) {
    INSP_FOCUS_SEARCH = false;
    HostPicker.focusSearch(box);
  }
}

// Global refresh / other pages update host cache → keep inspect picker live.
document.addEventListener("aiops:hosts-updated", () => {
  if (!inspPanelActive()) return;
  INSP_HOSTS_LOADING = false;
  INSP_HOSTS_ERR = "";
  renderInspHostPicker();
});
document.addEventListener("aiops:host-trees-refresh", () => {
  if (!inspPanelActive()) return;
  INSP_HOSTS_LOADING = false;
  INSP_HOSTS_ERR = "";
  renderInspHostPicker();
});

safeAddEventListener("inspRefreshBtn", "click", () => loadHostInspect({ forceHosts: true }));

safeAddEventListener("inspRunBtn", "click", async () => {
  inspCaptureSelection();
  const ids = [...INSP_SELECTED].filter(id => {
    const h = inspHostsPool().find(x => x.id === id);
    return h && h.online;
  });
  if (!ids.length) {
    toast(inspT("inspect.pick_hosts", "请先勾选要巡检的在线主机"), "warn");
    return;
  }
  try {
    const profile = ($("inspProfile") && $("inspProfile").value) || "standard";
    const timeout = profile === "deep" ? 300 : profile === "quick" ? 90 : 180;
    const r = await fetch(`${API}/host-inspect/run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ host_ids: ids, timeout_sec: timeout, profile })
    });
    const data = await r.json();
    if (!r.ok) {
      toast(data.error || inspT("inspect.run_fail", "发起巡检失败"), "err");
      return;
    }
    toast(inspT("inspect.run_ok", "巡检已开始"), "ok");
    INSP_ACTIVE_ID = data.id;
    INSP_VIEW_ITEM = null;
    INSP_VIEW_MODE = "fleet";
    await loadHostInspect();
    startInspPoll(data.id);
  } catch (e) {
    toast(String(e), "err");
  }
});

function startInspPoll(id) {
  stopInspPoll();
  let lastSig = "";
  INSP_POLL = setInterval(async () => {
    try {
      // compact=1：轮询不含完整 report，避免多机每 2s 拉数十 MB。
      const b = await fetch(`${API}/host-inspect/${encodeURIComponent(id)}?compact=1`).then(r => r.json());
      const sig = inspBatchProgressSig(b);
      const idx = INSP_BATCHES.findIndex(x => x.id === id);
      if (idx >= 0) INSP_BATCHES[idx] = inspMergeBatch(INSP_BATCHES[idx], b); else INSP_BATCHES.unshift(b);
      if (sig !== lastSig) {
        lastSig = sig;
        renderInspBatches();
        if (INSP_VIEW_MODE === "fleet" && INSP_ACTIVE_ID === id) {
          showInspFleetSummary(INSP_BATCHES.find(x => x.id === id) || b);
        } else if (INSP_VIEW_ITEM && INSP_VIEW_ITEM.batchId === id) {
          await inspEnsureHostReport(id, INSP_VIEW_ITEM.hostId);
        }
      }
      if (b.status === "done") {
        stopInspPoll();
        // Final pass: refresh fleet aggregation with any newly finished briefs.
        renderInspBatches();
        if (INSP_VIEW_MODE === "fleet" && INSP_ACTIVE_ID === id) {
          showInspFleetSummary(INSP_BATCHES.find(x => x.id === id) || b);
        }
      }
    } catch (e) { /* ignore transient */ }
  }, 2500);
}

function inspBatchProgressSig(b) {
  if (!b) return "";
  const parts = [b.status || "", b.done_count || 0, b.ok_count || 0, b.warn_count || 0, b.crit_count || 0, b.err_count || 0];
  (b.items || []).forEach(it => parts.push(it.host_id, it.status || "", it.warnings || 0, it.critical || 0, (it.findings_brief || []).length));
  return parts.join("|");
}

/** Merge poll snapshot into cached batch without wiping already-fetched full reports. */
function inspMergeBatch(prev, next) {
  if (!prev || !prev.items) return next;
  const reportByHost = {};
  (prev.items || []).forEach(it => {
    if (it && it.report) reportByHost[it.host_id] = it.report;
  });
  const merged = Object.assign({}, next, { items: (next.items || []).map(it => {
    const cp = Object.assign({}, it);
    if (!cp.report && reportByHost[cp.host_id]) {
      cp.report = reportByHost[cp.host_id];
      cp.has_report = true;
    }
    return cp;
  })});
  return merged;
}

async function inspFetchFullBatch(id) {
  const b = await fetch(`${API}/host-inspect/${encodeURIComponent(id)}`).then(r => r.json());
  const idx = INSP_BATCHES.findIndex(x => x.id === id);
  if (idx >= 0) INSP_BATCHES[idx] = b; else INSP_BATCHES.unshift(b);
  return b;
}

async function inspEnsureHostReport(batchId, hostId) {
  const batch = INSP_BATCHES.find(x => x.id === batchId);
  const item = batch && (batch.items || []).find(x => x.host_id === hostId);
  if (!item) return null;
  if (item.report) {
    showInspReport(batch, item);
    return item;
  }
  if (!item.has_report && item.status !== "ok" && item.status !== "warn" && item.status !== "crit") {
    showInspReport(batch, item);
    return item;
  }
  try {
    const b = await fetch(`${API}/host-inspect/${encodeURIComponent(batchId)}?host_id=${encodeURIComponent(hostId)}`).then(r => r.json());
    const idx = INSP_BATCHES.findIndex(x => x.id === batchId);
    if (idx >= 0) INSP_BATCHES[idx] = inspMergeBatch(INSP_BATCHES[idx], b); else INSP_BATCHES.unshift(b);
    const fresh = (INSP_BATCHES.find(x => x.id === batchId) || b);
    const it = (fresh.items || []).find(x => x.host_id === hostId);
    if (it) showInspReport(fresh, it);
    return it;
  } catch (e) {
    showInspReport(batch, item);
    return item;
  }
}

function stopInspPoll() {
  if (INSP_POLL) { clearInterval(INSP_POLL); INSP_POLL = null; }
}

function inspStatusLabel(s) {
  return ({
    pending: inspT("inspect.st_pending", "等待"),
    running: inspT("inspect.st_running", "巡检中"),
    ok: inspT("inspect.st_ok", "正常"),
    warn: inspT("inspect.st_warn", "警告"),
    crit: inspT("inspect.st_crit", "严重"),
    error: inspT("inspect.st_error", "失败"),
    done: inspT("inspect.st_done", "完成")
  })[s] || s;
}

function inspParseReport(item) {
  if (!item || !item.report) return null;
  let rep = item.report;
  if (typeof rep === "string") {
    try { rep = JSON.parse(rep); } catch (e) { return null; }
  }
  return rep;
}

/** Aggregate findings & recommendations across all hosts in a batch. */
function inspAggregateBatch(batch) {
  const findingMap = new Map(); // key -> {level, message, section, hosts:[]}
  const rec = { short: [], mid: [], long: [] };
  const hostScores = [];
  (batch.items || []).forEach(it => {
    const rep = inspParseReport(it);
    const hn = (typeof HostPicker !== "undefined" && HostPicker.hostTitle)
      ? HostPicker.hostTitle(it)
      : (it.hostname || it.ip || "未知主机");
    if (!rep) {
      // Compact poll / list: use briefs + status metrics (no multi‑MB report JSON).
      if (it.status === "pending" || it.status === "running") return;
      if (it.status === "error") {
        const key = "error:" + (it.error || "fail");
        if (!findingMap.has(key)) findingMap.set(key, { level: "crit", message: it.error || "巡检失败", section: "error", hosts: [] });
        findingMap.get(key).hosts.push(hn);
      } else if (it.status) {
        hostScores.push({
          host: hn, host_id: it.host_id, status: it.status,
          warnings: it.warnings || 0, critical: it.critical || 0,
          cpu: it.cpu_pct != null ? it.cpu_pct : null,
          mem: it.mem_pct != null ? it.mem_pct : null,
        });
      }
      (it.findings_brief || []).forEach(f => {
        const msg = String(f.message || "").trim();
        if (!msg) return;
        const level = String(f.level || "warn").toLowerCase();
        const key = level + "|" + msg;
        if (!findingMap.has(key)) findingMap.set(key, { level, message: msg, section: "", hosts: [] });
        const row = findingMap.get(key);
        if (!row.hosts.includes(hn)) row.hosts.push(hn);
      });
      return;
    }
    const res = rep.result || {};
    hostScores.push({
      host: hn,
      host_id: it.host_id,
      status: it.status,
      warnings: res.warnings || it.warnings || 0,
      critical: res.critical || it.critical || 0,
      cpu: (rep.metrics || {}).cpu_usage_pct,
      mem: (rep.metrics || {}).mem_usage_pct,
    });
    (rep.findings || []).forEach(f => {
      const msg = String(f.message || f.title || "").trim();
      if (!msg) return;
      const level = String(f.level || "warn").toLowerCase();
      const key = level + "|" + msg;
      if (!findingMap.has(key)) findingMap.set(key, { level, message: msg, section: f.section || "", hosts: [] });
      const row = findingMap.get(key);
      if (!row.hosts.includes(hn)) row.hosts.push(hn);
    });
    const recommend = (rep.sections || []).find(s => s.id === "recommend");
    (recommend && recommend.items || []).forEach(item => {
      const label = String(item.label || "").toLowerCase();
      const val = String(item.value || "").trim();
      if (!val) return;
      const bucket = /短|short|立即|urgent/.test(label) ? "short"
        : /中|mid|本周|week/.test(label) ? "mid" : "long";
      const line = `【${hn}】${val}`;
      if (!rec[bucket].includes(line)) rec[bucket].push(line);
    });
  });
  const rank = { crit: 0, critical: 0, error: 0, warn: 1, warning: 1, info: 2 };
  const findings = [...findingMap.values()].sort((a, b) =>
    (rank[a.level] ?? 9) - (rank[b.level] ?? 9) || b.hosts.length - a.hosts.length
  );
  // Deduplicate recommendation themes (same text without host prefix)
  const synth = inspSynthesizeAdvice(findings, hostScores);
  return { findings, recommend: rec, hostScores, synth };
}

function inspSynthesizeAdvice(findings, hostScores) {
  const tips = [];
  const critN = findings.filter(f => /crit|error/.test(f.level)).length;
  const warnN = findings.filter(f => /warn/.test(f.level)).length;
  const highCPU = hostScores.filter(h => Number(h.cpu) >= 85).length;
  const highMem = hostScores.filter(h => Number(h.mem) >= 85).length;
  if (critN) tips.push(`共 ${critN} 类严重问题跨主机出现，建议优先处置出现频次最高的项，并核对是否存在共性配置缺陷。`);
  if (warnN) tips.push(`共 ${warnN} 类警告项；可按「影响主机数」排序批量修复（补丁、清理、扩容、加固）。`);
  if (highCPU) tips.push(`${highCPU} 台主机 CPU ≥85%，建议排查热点进程、限流或扩容计算资源。`);
  if (highMem) tips.push(`${highMem} 台主机内存 ≥85%，建议检查泄漏/缓存策略并提高水位告警阈值前先做根因分析。`);
  if (!tips.length) tips.push("本批次整体健康度较好；建议保持标准/深度巡检节奏，并关注证书、磁盘与认证失败趋势。");
  tips.push("统一改进：对齐时区与 NTP；限制高危端口对外暴露；容器/K8s 资源设 requests/limits；关键服务纳入剧本定期复检。");
  return tips;
}

function renderInspBatches() {
  const list = $("inspBatchList");
  const empty = $("inspEmpty");
  const stats = $("inspStats");
  if (!list) return;
  if (!INSP_BATCHES.length) {
    list.innerHTML = "";
    if (empty) empty.style.display = "";
    if (stats) stats.innerHTML = "";
    const view = $("inspReportView");
    if (view) { view.style.display = "none"; view.innerHTML = ""; }
    return;
  }
  if (empty) empty.style.display = "none";

  const latest = INSP_BATCHES.find(b => b.id === INSP_ACTIVE_ID) || INSP_BATCHES[0];
  if (stats && latest) {
    const agg = inspAggregateBatch(latest);
    stats.innerHTML = `
      <div class="insp-stat-card"><b>${latest.host_count || 0}</b><span>${esc(inspT("inspect.stat_hosts", "目标主机"))}</span></div>
      <div class="insp-stat-card ok"><b>${latest.ok_count || 0}</b><span>${esc(inspT("inspect.st_ok", "正常"))}</span></div>
      <div class="insp-stat-card warn"><b>${latest.warn_count || 0}</b><span>${esc(inspT("inspect.st_warn", "警告"))}</span></div>
      <div class="insp-stat-card crit"><b>${latest.crit_count || 0}</b><span>${esc(inspT("inspect.st_crit", "严重"))}</span></div>
      <div class="insp-stat-card err"><b>${latest.err_count || 0}</b><span>${esc(inspT("inspect.st_error", "失败"))}</span></div>
      <div class="insp-stat-card"><b>${agg.findings.length}</b><span>${esc(inspT("inspect.stat_issue_types", "问题类型"))}</span></div>`;
  }

  list.innerHTML = INSP_BATCHES.slice(0, 20).map(b => {
    const when = b.started_at ? new Date(b.started_at * 1000).toLocaleString() : "";
    const fleetActive = INSP_VIEW_MODE === "fleet" && (INSP_ACTIVE_ID === b.id || (!INSP_ACTIVE_ID && INSP_BATCHES[0] && INSP_BATCHES[0].id === b.id));
    const items = (b.items || []).map(it => {
      const active = INSP_VIEW_MODE === "host" && INSP_VIEW_ITEM && INSP_VIEW_ITEM.batchId === b.id && INSP_VIEW_ITEM.hostId === it.host_id ? "active" : "";
      return `<button type="button" class="insp-item ${it.status} ${active}" data-batch="${esc(b.id)}" data-host="${esc(it.host_id)}">
        <span class="insp-item-name">${esc(it.hostname)}</span>
        <span class="insp-badge ${it.status}">${inspStatusLabel(it.status)}</span>
        ${it.critical ? `<span class="insp-badge crit">${it.critical}</span>` : ""}
        ${it.warnings ? `<span class="insp-badge warn">${it.warnings}</span>` : ""}
      </button>`;
    }).join("");
    return `<div class="insp-batch ${fleetActive ? "active-batch" : ""}">
      <div class="insp-batch-head">
        <button type="button" class="insp-fleet-btn ${fleetActive ? "active" : ""}" data-insp-fleet="${esc(b.id)}" title="${esc(inspT("inspect.view_fleet", "查看多机汇总"))}">
          <strong>${esc(inspT("inspect.batch", "批次"))}</strong>
          <span class="insp-badge ${b.status}">${inspStatusLabel(b.status)}</span>
          <span class="hint">${esc(when)} · ${b.done_count || 0}/${b.host_count || 0}${b.source ? " · " + esc(b.source) : ""}${!b.source && b.operator ? " · " + esc(b.operator) : ""}</span>
        </button>
        <div class="insp-batch-actions">
          <button type="button" class="btn sm" data-insp-export-batch="${esc(b.id)}">${esc(inspT("inspect.export_batch", "导出汇总"))}</button>
          <button type="button" class="btn sm ai-assist-btn" data-insp-ai-fleet="${esc(b.id)}">🤖 ${esc(inspT("inspect.ai_fleet", "汇总分析"))}</button>
        </div>
      </div>
      <div class="insp-items">${items}</div>
    </div>`;
  }).join("");

  list.querySelectorAll(".insp-item").forEach(btn => {
    btn.addEventListener("click", () => {
      const bid = btn.dataset.batch, hid = btn.dataset.host;
      const batch = INSP_BATCHES.find(x => x.id === bid);
      const item = batch && (batch.items || []).find(x => x.host_id === hid);
      if (!item) return;
      INSP_ACTIVE_ID = bid;
      INSP_VIEW_ITEM = { batchId: bid, hostId: hid };
      INSP_VIEW_MODE = "host";
      renderInspBatches();
      inspEnsureHostReport(bid, hid);
    });
  });
  list.querySelectorAll("[data-insp-fleet]").forEach(btn => {
    btn.addEventListener("click", () => {
      const bid = btn.getAttribute("data-insp-fleet");
      const batch = INSP_BATCHES.find(x => x.id === bid);
      if (!batch) return;
      INSP_ACTIVE_ID = bid;
      INSP_VIEW_ITEM = null;
      INSP_VIEW_MODE = "fleet";
      renderInspBatches();
      showInspFleetSummary(batch);
    });
  });
  list.querySelectorAll("[data-insp-export-batch]").forEach(btn => {
    btn.addEventListener("click", async e => {
      e.stopPropagation();
      const bid = btn.getAttribute("data-insp-export-batch");
      let batch = INSP_BATCHES.find(x => x.id === bid);
      if (!batch) return;
      try {
        batch = await inspFetchFullBatch(bid);
      } catch (_) {}
      inspExportBatch(batch, "pdf");
    });
  });
  list.querySelectorAll("[data-insp-ai-fleet]").forEach(btn => {
    btn.addEventListener("click", async e => {
      e.stopPropagation();
      const bid = btn.getAttribute("data-insp-ai-fleet");
      let batch = INSP_BATCHES.find(x => x.id === bid);
      if (!batch) return;
      try {
        batch = await inspFetchFullBatch(bid);
      } catch (_) {}
      openInspectFleetAI(batch);
    });
  });

  // Auto-show fleet summary for active/latest batch when nothing selected
  if (INSP_VIEW_MODE === "fleet" && latest) {
    showInspFleetSummary(latest);
  }
}

function showInspFleetSummary(batch) {
  const view = $("inspReportView");
  if (!view || !batch) return;
  const agg = inspAggregateBatch(batch);
  view.style.display = "";
  const findingRows = agg.findings.slice(0, 80).map(f => {
    const hosts = f.hosts.slice(0, 8).map(h => esc(h)).join("、") + (f.hosts.length > 8 ? ` +${f.hosts.length - 8}` : "");
    return `<tr class="${esc(f.level)}">
      <td><span class="insp-badge ${esc(f.level)}">${esc(f.level)}</span></td>
      <td>${esc(f.message)}</td>
      <td class="mono">${f.hosts.length}</td>
      <td class="insp-host-chips">${hosts}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="4" class="hint">${esc(inspT("inspect.no_findings", "无告警项"))}</td></tr>`;

  const hostRows = agg.hostScores.map(h =>
    `<tr class="${esc(h.status)}">
      <td><button type="button" class="linkish" data-jump-host="${esc(h.host_id)}">${esc(h.host)}</button></td>
      <td><span class="insp-badge ${esc(h.status)}">${inspStatusLabel(h.status)}</span></td>
      <td>${h.critical}</td><td>${h.warnings}</td>
      <td>${h.cpu != null ? h.cpu + "%" : "—"}</td>
      <td>${h.mem != null ? h.mem + "%" : "—"}</td>
    </tr>`
  ).join("");

  const recBlock = (title, arr) => arr.length
    ? `<div class="insp-rec-block"><h5>${esc(title)}</h5><ul>${arr.slice(0, 40).map(x => `<li>${esc(x)}</li>`).join("")}</ul></div>`
    : "";

  view.innerHTML = `
    <div class="insp-report-head insp-fleet-head">
      <div>
        <div class="insp-kicker">${esc(inspT("inspect.fleet_kicker", "多机汇总"))}</div>
        <h3>${esc(inspT("inspect.fleet_title", "巡检问题汇总"))}</h3>
        <div class="hint">${esc(batch.id)} · ${batch.done_count || 0}/${batch.host_count || 0} · ${inspStatusLabel(batch.status)}</div>
      </div>
      <div class="insp-report-result">
        <div class="insp-export-wrap">
          <button type="button" class="btn sm" id="inspExportFleetBtn">${esc(inspT("inspect.export", "导出报告"))}</button>
          <select id="inspExportFleetFmt" aria-label="format">
            <option value="pdf" selected>PDF</option>
            <option value="excel">Excel</option>
            <option value="markdown">Markdown</option>
            <option value="word">Word</option>
          </select>
        </div>
        <button type="button" class="btn sm ai-assist-btn" id="inspAIFleetBtn">🤖 ${esc(inspT("inspect.ai_fleet", "汇总分析"))}</button>
        <button type="button" class="btn sm ai-assist-btn" id="inspAIFleetFixBtn">🛠 ${esc(inspT("inspect.ai_fix", "生成修复剧本"))}</button>
      </div>
    </div>
    <div class="insp-advice">
      <h4>${esc(inspT("inspect.advice_title", "优化改进建议"))}</h4>
      <ul>${agg.synth.map(t => `<li>${esc(t)}</li>`).join("")}</ul>
    </div>
    <div class="insp-rec-grid">
      ${recBlock(inspT("inspect.rec_short", "短期（立即）"), agg.recommend.short)}
      ${recBlock(inspT("inspect.rec_mid", "中期"), agg.recommend.mid)}
      ${recBlock(inspT("inspect.rec_long", "长期"), agg.recommend.long)}
    </div>
    <div class="insp-findings insp-fleet-findings">
      <h4>${esc(inspT("inspect.findings_rollup", "跨主机问题归并"))}</h4>
      <div class="nf-table-wrap"><table class="data-table insp-agg-table">
        <thead><tr>
          <th>${esc(inspT("inspect.col_level", "级别"))}</th>
          <th>${esc(inspT("inspect.col_issue", "问题"))}</th>
          <th>${esc(inspT("inspect.col_hosts_n", "主机数"))}</th>
          <th>${esc(inspT("inspect.col_hosts", "涉及主机"))}</th>
        </tr></thead>
        <tbody>${findingRows}</tbody>
      </table></div>
    </div>
    <div class="insp-findings">
      <h4>${esc(inspT("inspect.host_matrix", "主机健康矩阵"))}</h4>
      <div class="nf-table-wrap"><table class="data-table insp-agg-table">
        <thead><tr>
          <th>${esc(inspT("inspect.col_host", "主机"))}</th>
          <th>${esc(inspT("inspect.col_status", "状态"))}</th>
          <th>Crit</th><th>Warn</th><th>CPU</th><th>MEM</th>
        </tr></thead>
        <tbody>${hostRows || `<tr><td colspan="6" class="hint">—</td></tr>`}</tbody>
      </table></div>
    </div>`;

  view.querySelectorAll("[data-jump-host]").forEach(btn => {
    btn.addEventListener("click", () => {
      const hid = btn.getAttribute("data-jump-host");
      const item = (batch.items || []).find(x => x.host_id === hid);
      if (!item) return;
      INSP_VIEW_MODE = "host";
      INSP_VIEW_ITEM = { batchId: batch.id, hostId: hid };
      renderInspBatches();
      inspEnsureHostReport(batch.id, hid);
    });
  });
  const expBtn = view.querySelector("#inspExportFleetBtn");
  if (expBtn) expBtn.onclick = async () => {
    const fmt = ($("inspExportFleetFmt") || {}).value || "pdf";
    let full = batch;
    try { full = await inspFetchFullBatch(batch.id); } catch (_) {}
    inspExportBatch(full, fmt);
  };
  const fleetFixBtn = view.querySelector("#inspAIFleetFixBtn");
  if (fleetFixBtn) fleetFixBtn.onclick = () => openInspectRemediation(inspectFleetContext(batch));
  const aiBtn = view.querySelector("#inspAIFleetBtn");
  if (aiBtn) aiBtn.onclick = async () => {
    let full = batch;
    try { full = await inspFetchFullBatch(batch.id); } catch (_) {}
    openInspectFleetAI(full);
  };
}

function showInspReport(batch, item) {
  const view = $("inspReportView");
  if (!view) return;
  if (!item.report) {
    view.style.display = "";
    view.innerHTML = `<div class="insp-report-head"><h3>${esc(item.hostname)}</h3>
      <span class="insp-badge ${item.status}">${inspStatusLabel(item.status)}</span></div>
      <div class="hint">${esc(item.error || inspT("inspect.waiting_report", "报告生成中…"))}</div>`;
    return;
  }
  const rep = inspParseReport(item);
  if (!rep) {
    view.style.display = "";
    view.innerHTML = `<div class="hint">${inspT("inspect.bad_report", "报告解析失败")}</div>`;
    return;
  }
  view.style.display = "";
  const h = rep.host || {};
  const m = rep.metrics || {};
  const res = rep.result || {};
  const recommend = (rep.sections || []).find(s => s.id === "recommend");
  const otherSecs = (rep.sections || []).filter(s => s.id !== "recommend");
  const findings = (rep.findings || []).map(f =>
    `<li class="insp-finding ${f.level}"><b>${esc(f.level)}</b> ${esc(f.message)}</li>`
  ).join("") || `<li class="hint">${inspT("inspect.no_findings", "无告警项")}</li>`;

  const toc = otherSecs.map(sec =>
    `<a href="#insp-sec-${esc(sec.id)}" class="insp-toc-a ${esc(sec.status || "ok")}">${esc(sec.title)}</a>`
  ).join("");

  const recHTML = recommend ? `<div class="insp-advice insp-host-advice">
    <h4>${esc(recommend.title || inspT("inspect.recommend", "改进建议"))}</h4>
    <ul>${(recommend.items || []).map(it => `<li><strong>${esc(it.label || "")}</strong> ${esc(it.value || "")}</li>`).join("")}</ul>
  </div>` : "";

  view.innerHTML = `
    <div class="insp-report-head">
      <div>
        <button type="button" class="btn sm ghost" id="inspBackFleet">${esc(inspT("inspect.back_fleet", "← 返回汇总"))}</button>
        <h3>${esc(item.hostname || h.hostname || "")}</h3>
        <div class="hint">${esc(h.os || "")} · ${esc(h.os_family || "")} · ${esc(h.ip || item.ip || "")} · ${esc(h.kernel || "")}${h.fqdn ? " · " + esc(h.fqdn) : ""} · v${esc(rep.version || "")}</div>
      </div>
      <div class="insp-report-result">
        <span class="insp-badge ${item.status}">${inspStatusLabel(item.status)}</span>
        <span>${inspT("inspect.warnings", "警告")} ${res.warnings || 0}</span>
        <span>${inspT("inspect.critical", "严重")} ${res.critical || 0}</span>
        <span class="hint">${esc(rep.timestamp || "")}${rep.elapsed_seconds != null ? " · " + Number(rep.elapsed_seconds).toFixed(1) + "s" : ""}</span>
        <div class="insp-export-wrap">
          <button type="button" class="btn sm" id="inspExportHostBtn">${esc(inspT("inspect.export", "导出报告"))}</button>
          <select id="inspExportHostFmt"><option value="excel">Excel</option><option value="markdown">Markdown</option><option value="pdf">PDF</option></select>
        </div>
        <button type="button" class="btn sm ai-assist-btn" id="inspAIAnalyzeBtn">🤖 ${inspT("inspect.ai_analyze", "AI 分析")}</button>
        <button type="button" class="btn sm ai-assist-btn" id="inspAIFixBtn">🛠 ${inspT("inspect.ai_fix", "生成修复剧本")}</button>
      </div>
    </div>
    <div class="insp-metrics">
      <div><b>${m.cpu_usage_pct ?? "—"}%</b><span>CPU</span></div>
      <div><b>${m.mem_usage_pct ?? "—"}%</b><span>MEM</span></div>
      <div><b>${m.swap_usage_pct ?? "—"}%</b><span>SWAP</span></div>
      <div><b>${m.load_1m ?? "—"}</b><span>Load1</span></div>
      <div><b>${m.disk_alert_count ?? 0}</b><span>Disk⚠</span></div>
      <div><b>${m.inode_alert_count ?? 0}</b><span>Inode⚠</span></div>
      <div><b>${m.fd_usage_pct != null ? m.fd_usage_pct + "%" : "—"}</b><span>FD</span></div>
      <div><b>${m.process_count ?? "—"}</b><span>Procs</span></div>
      <div><b>${m.zombie_count ?? 0}</b><span>Zombie</span></div>
      <div><b>${m.tcp_listen ?? "—"}</b><span>Listen</span></div>
      <div><b>${m.oom_count ?? 0}</b><span>OOM</span></div>
      <div><b>${m.container_count ?? 0}</b><span>Ctr</span></div>
      <div><b>${(m.ssl_expired || 0) + (m.ssl_expiring || 0)}</b><span>SSL⚠</span></div>
    </div>
    ${recHTML}
    <div class="insp-toc">${toc}</div>
    <div class="insp-findings"><h4>${inspT("inspect.findings", "发现问题")}</h4><ul>${findings}</ul></div>
    <div class="insp-sections">${otherSecs.map(sec => inspRenderSection(sec)).join("")}</div>
  `;
  const back = view.querySelector("#inspBackFleet");
  if (back) back.onclick = () => {
    INSP_VIEW_MODE = "fleet";
    INSP_VIEW_ITEM = null;
    renderInspBatches();
    showInspFleetSummary(batch);
  };
  const aiBtn = view.querySelector("#inspAIAnalyzeBtn");
  if (aiBtn) aiBtn.onclick = () => openInspectAIAssist(batch, item, rep);
  const fixBtn = view.querySelector("#inspAIFixBtn");
  if (fixBtn) fixBtn.onclick = () => openInspectRemediation(inspectHostContext(batch, item, rep));
  const expBtn = view.querySelector("#inspExportHostBtn");
  if (expBtn) expBtn.onclick = () => {
    const fmt = ($("inspExportHostFmt") || {}).value || "excel";
    inspExportHost(batch, item, fmt);
  };
  view.scrollIntoView({ behavior: "smooth", block: "nearest" });
}

/** Dense / long-key sections that overflow 2-col cards (kernel sysctl, paths, etc.). */
function inspSectionIsWide(sec) {
  const id = String(sec && sec.id || "").toLowerCase();
  const wideIds = new Set([
    "kernel", "process", "processes", "top_process", "listen", "net_listen",
    "services", "service", "packages", "ssl", "certs", "certificate",
    "large_files", "bigfiles", "cron", "journal", "logs", "dmesg", "auth"
  ]);
  if (wideIds.has(id)) return true;
  const items = (sec && sec.items) || [];
  if (items.length >= 8) return true;
  return items.some(it => String(it.label || "").length > 28 || String(it.value || "").length > 48);
}

/** Prefer stacked key/value for sysctl-like keys (a.b.c) to avoid column clip. */
function inspSectionUseKV(sec) {
  const id = String(sec && sec.id || "").toLowerCase();
  if (id === "kernel" || id === "sysctl") return true;
  const items = (sec && sec.items) || [];
  let dotted = 0;
  for (const it of items) {
    if (/\.[a-z0-9_.-]+/i.test(String(it.label || ""))) dotted++;
  }
  return items.length >= 3 && dotted >= Math.ceil(items.length * 0.5);
}

function inspRenderSection(sec) {
  const items = sec.items || [];
  const wide = inspSectionIsWide(sec);
  const useKV = inspSectionUseKV(sec);
  let body;
  if (!items.length) {
    body = sec.summary
      ? `<div class="hint">${esc(sec.summary)}</div>`
      : `<div class="hint">—</div>`;
  } else if (useKV) {
    body = `<div class="insp-kv">${items.map(it => {
      const label = String(it.label || "");
      const value = String(it.value || "");
      return `<div class="insp-kv-row ${esc(it.status || "")}" title="${esc(label + (value ? " = " + value : ""))}">
        <div class="insp-kv-k">${esc(label)}</div>
        <div class="insp-kv-v">${esc(value)}</div>
      </div>`;
    }).join("")}</div>`;
  } else {
    body = `<table class="insp-table"><tbody>${items.map(it => {
      const label = String(it.label || "");
      const value = String(it.value || "");
      return `<tr class="${esc(it.status || "")}" title="${esc(label + (value ? ": " + value : ""))}">
        <td>${esc(label)}</td><td>${esc(value)}</td></tr>`;
    }).join("")}</tbody></table>`;
  }
  return `<div class="insp-sec ${esc(sec.status || "ok")}${wide ? " insp-sec-wide" : ""}" id="insp-sec-${esc(sec.id)}">
    <div class="insp-sec-head"><span class="insp-badge ${esc(sec.status || "ok")}">${esc(sec.status || "ok")}</span>
      <h4>${esc(sec.title)}</h4>
      ${sec.summary && items.length ? `<span class="hint">${esc(sec.summary)}</span>` : ""}
    </div>
    ${body}
  </div>`;
}

function inspBuildHostModel(batch, item) {
  const rep = inspParseReport(item) || {};
  const h = rep.host || {};
  const m = rep.metrics || {};
  const res = rep.result || {};
  const findings = (rep.findings || []).map(f => [f.level || "", f.section || "", f.message || ""]);
  const sections = (rep.sections || []).map(sec => ({
    title: sec.title || sec.id || "section",
    columns: [inspT("inspect.col_item", "项"), inspT("inspect.col_value", "值")],
    rows: (sec.items || []).map(it => [it.label || "", it.value || ""]),
  }));
  const recommend = (rep.sections || []).find(s => s.id === "recommend");
  const narrative = [
    `# ${inspT("inspect.recommend", "改进建议")}`,
    ...((recommend && recommend.items) || []).map(it => `- **${it.label || ""}**：${it.value || ""}`),
  ].join("\n");
  return {
    title: inspT("inspect.report_title", "主机巡检报告") + " — " + (item.hostname || h.hostname || item.host_id),
    subtitle: (batch && batch.id ? batch.id + " · " : "") + new Date().toLocaleString(),
    summaryTitle: inspT("inspect.report_meta", "报告摘要"),
    narrativeTitle: inspT("inspect.recommend", "改进建议"),
    meta: [
      [inspT("inspect.col_host", "主机"), item.hostname || h.hostname || ""],
      ["IP", h.ip || item.ip || ""],
      [inspT("inspect.col_os", "系统"), [h.os, h.os_family, h.kernel].filter(Boolean).join(" · ")],
      [inspT("inspect.col_status", "状态"), item.status || ""],
      [inspT("inspect.warnings", "警告"), String(res.warnings || 0)],
      [inspT("inspect.critical", "严重"), String(res.critical || 0)],
    ],
    kpis: [
      ["CPU", (m.cpu_usage_pct != null ? m.cpu_usage_pct + "%" : "—")],
      ["MEM", (m.mem_usage_pct != null ? m.mem_usage_pct + "%" : "—")],
      ["Load1", String(m.load_1m ?? "—")],
      ["Disk⚠", String(m.disk_alert_count ?? 0)],
    ],
    narrative,
    sections: [
      { title: inspT("inspect.findings", "发现问题"), columns: ["级别", "分区", "问题"], rows: findings },
      ...sections,
    ],
    rawJSON: rep,
  };
}

function inspBuildBatchModel(batch) {
  const agg = inspAggregateBatch(batch);
  return {
    title: inspT("inspect.fleet_report_title", "多机巡检汇总报告"),
    subtitle: (batch.id || "") + " · " + (batch.host_count || 0) + " 台 · " + new Date().toLocaleString(),
    summaryTitle: inspT("inspect.report_meta", "报告摘要"),
    narrativeTitle: inspT("inspect.advice_title", "优化改进建议"),
    meta: [
      [inspT("inspect.batch", "批次"), batch.id || ""],
      [inspT("inspect.stat_hosts", "目标主机"), String(batch.host_count || 0)],
      [inspT("inspect.st_ok", "正常"), String(batch.ok_count || 0)],
      [inspT("inspect.st_warn", "警告"), String(batch.warn_count || 0)],
      [inspT("inspect.st_crit", "严重"), String(batch.crit_count || 0)],
      [inspT("inspect.st_error", "失败"), String(batch.err_count || 0)],
    ],
    narrative: agg.synth.map(t => `- ${t}`).join("\n") + "\n\n" +
      ["### 短期", ...agg.recommend.short.map(x => `- ${x}`),
        "### 中期", ...agg.recommend.mid.map(x => `- ${x}`),
        "### 长期", ...agg.recommend.long.map(x => `- ${x}`)].join("\n"),
    sections: [
      {
        title: inspT("inspect.findings_rollup", "跨主机问题归并"),
        columns: ["级别", "问题", "主机数", "涉及主机"],
        rows: agg.findings.map(f => [f.level, f.message, String(f.hosts.length), f.hosts.join(", ")]),
      },
      {
        title: inspT("inspect.host_matrix", "主机健康矩阵"),
        columns: ["主机", "状态", "Crit", "Warn", "CPU", "MEM"],
        rows: agg.hostScores.map(h => [h.host, h.status, String(h.critical), String(h.warnings),
          h.cpu != null ? h.cpu + "%" : "—", h.mem != null ? h.mem + "%" : "—"]),
      },
    ],
    rawJSON: batch,
  };
}

async function inspExportHost(batch, item, fmt) {
  if (typeof exportModel !== "function") { toast("导出组件未就绪", "err"); return; }
  try {
    await exportModel(inspBuildHostModel(batch, item), fmt || "excel", "主机巡检_" + (item.hostname || item.host_id));
    toast(inspT("toast.exported", "已导出"), "ok");
  } catch (e) { toast(String(e.message || e), "err"); }
}

async function inspExportBatch(batch, fmt) {
  if (typeof exportModel !== "function") { toast("导出组件未就绪", "err"); return; }
  try {
    await exportModel(inspBuildBatchModel(batch), fmt || "pdf", "巡检汇总_" + (batch.id || "batch"));
    toast(inspT("toast.exported", "已导出"), "ok");
  } catch (e) { toast(String(e.message || e), "err"); }
}

/**
 * 「生成修复剧本」——巡检闭环的最后一段。
 *
 * 复用 sre.js 的 openRemediationDraft：草稿回填到剧本编辑器，由人逐条核对后再走审批。
 * 闭环只应该有一个出口，所以这里不自己实现回填。
 */
function openInspectRemediation(ctxText) {
  if (typeof window.openRemediationDraft !== "function") {
    if (typeof toast === "function") toast(inspT("assist.unavailable", "AI 面板未就绪"), "err");
    return;
  }
  window.openRemediationDraft(ctxText, inspT("inspect.ai_fix_title", "生成修复剧本草稿 · 基于巡检结果"));
}

/** 单机体检上下文。诊断与修复共用同一份事实，避免两处各整理一遍导致结论对不上。 */
function inspectHostContext(batch, item, rep) {
  const h = (rep && rep.host) || {};
  const m = (rep && rep.metrics) || {};
  const res = (rep && rep.result) || {};
  const findings = Array.isArray(rep && rep.findings) ? rep.findings.slice() : [];
  const rank = { critical: 0, error: 1, warn: 2, warning: 2, info: 3 };
  findings.sort((a, b) => (rank[String(a.level || "").toLowerCase()] ?? 9) - (rank[String(b.level || "").toLowerCase()] ?? 9));
  const findingLines = findings.slice(0, 24).map(f =>
    `- [${f.level || "?"}] ${f.message || f.title || ""}`
  ).join("\n") || "（无 findings）";
  const hostName = (typeof HostPicker !== "undefined" && HostPicker.hostTitle)
    ? HostPicker.hostTitle({ hostname: item.hostname || h.hostname, ip: h.ip || item.ip || h.agent_ip, id: item.host_id })
    : (item.hostname || h.hostname || h.ip || item.ip || "未知主机");
  let ctx = [
    `主机：${hostName}`,
    `系统：${h.os || ""} ${h.os_family || ""} ${h.kernel || ""}`,
    `IP：${h.ip || item.ip || ""}`,
    `批次：${batch && batch.id ? batch.id : ""} · 状态：${item.status || ""}`,
    `结果：警告 ${res.warnings || 0} · 严重 ${res.critical || 0}`,
    `指标：CPU ${m.cpu_usage_pct ?? "—"}% · MEM ${m.mem_usage_pct ?? "—"}% · Load1 ${m.load_1m ?? "—"} · Disk⚠ ${m.disk_alert_count ?? 0} · OOM ${m.oom_count ?? 0}`,
    "",
    "【发现问题（优先严重项）】",
    findingLines
  ].join("\n");
  if (ctx.length > 12000) ctx = ctx.slice(0, 12000) + "\n…（已截断）";
  return { ctx: ctx, hostName: hostName };
}

function openInspectAIAssist(batch, item, rep) {
  if (typeof openAIAssist !== "function") {
    if (typeof toast === "function") toast(inspT("assist.unavailable", "AI 面板未就绪"), "err");
    return;
  }
  const built = inspectHostContext(batch, item, rep);
  openAIAssist({
    task: "host_inspect_analysis",
    title: inspT("inspect.ai_title", "AI · 主机体检分析") + " · " + built.hostName,
    mode: "analyze",
    context: built.ctx,
    hint: inspT("inspect.ai_hint", "正在结合体检 findings 与指标生成研判…")
  });
}

/** 多机汇总上下文。 */
function inspectFleetContext(batch) {
  const agg = inspAggregateBatch(batch);
  const lines = [
    `批次：${batch.id} · 主机 ${batch.host_count} · 完成 ${batch.done_count}`,
    `状态计数：ok=${batch.ok_count} warn=${batch.warn_count} crit=${batch.crit_count} err=${batch.err_count}`,
    "",
    "【优化建议要点】",
    ...agg.synth.map(t => `- ${t}`),
    "",
    "【跨主机高频问题】",
    ...agg.findings.slice(0, 40).map(f => `- [${f.level}] x${f.hosts.length} ${f.message} :: ${f.hosts.slice(0, 6).join(",")}`),
  ];
  return lines.join("\n").slice(0, 14000);
}

function openInspectFleetAI(batch) {
  if (typeof openAIAssist !== "function") {
    toast(inspT("assist.unavailable", "AI 面板未就绪"), "err");
    return;
  }
  openAIAssist({
    task: "host_inspect_analysis",
    title: inspT("inspect.ai_fleet_title", "AI · 多机巡检汇总"),
    mode: "analyze",
    context: inspectFleetContext(batch),
    hint: inspT("inspect.ai_fleet_hint", "正在汇总多机问题并生成改进建议…")
  });
}
