// snmp.js — SNMP 网络设备面板（轮询接口状态/流量 + Trap 事件）
// Loaded as part of the unified app.js bundle. 复用 netflow 的 nf-* 样式与全局 helper。

(function() {
"use strict";

let snCurrentHost = "";
let snHostQuery = "";
let snIfFilter = "up"; // all | up | down — 交换机多数端口闲置，默认只看在线
let snTab = "devices"; // devices | traps
let snSearchT = null;
let snDevices = [];    // 最近一次加载的设备快照
let snTraps = [];      // 最近一次加载的 trap
let snHist = { host: "", device: "", deviceIP: "", ifindex: "", ifname: "", range: 1, custom: null };
let snHistCharts = {};
// 「只列有网络设备的主机」：从 /api/v1/snmp/hosts 拉有 SNMP 设备快照或 trap 的主机，
// 按设备数降序。null=未加载。刷新/进入视图时重新拉。
let snSNMPHosts = null;

function snFmtBps(bps) {
  bps = bps || 0;
  const u = ["bps", "Kbps", "Mbps", "Gbps", "Tbps"];
  let i = 0;
  while (bps >= 1000 && i < u.length - 1) { bps /= 1000; i++; }
  return bps.toFixed(1) + " " + u[i];
}
function snFmtSpeed(bps) {
  if (!bps) return "-";
  const u = ["bps", "Kbps", "Mbps", "Gbps", "Tbps"];
  let i = 0, v = bps;
  while (v >= 1000 && i < u.length - 1) { v /= 1000; i++; }
  return Math.round(v) + " " + u[i];
}
// snUtilCell：利用率迷你进度条 + 百分比（≥80 红 / ≥50 黄 / 其余蓝）。
function snUtilCell(u) {
  const col = u >= 80 ? "var(--crit)" : u >= 50 ? "var(--warn)" : "var(--accent)";
  return `<div class="sn-util"><div class="sn-util-bar"><div class="sn-util-fill" style="width:${Math.min(100, u)}%;background:${col}"></div></div><span class="sn-util-v">${u.toFixed(0)}%</span></div>`;
}

function renderSNMPPanel() {
  const container = $("snmpPanel");
  if (!container) return;

  // 先拉「有网络设备数据的主机」，再渲染；避免把无 SNMP 数据的主机塞进下拉。
  if (snSNMPHosts === null) {
    container.innerHTML = `<div class="loading-dots">${I18N.t("common.loading") || "加载中..."}</div>`;
    fetch(`/api/v1/snmp/hosts`, { credentials: "same-origin" })
      .then(r => r.json())
      .then(d => { snSNMPHosts = d.hosts || []; renderSNMPPanel(); })
      .catch(() => { snSNMPHosts = []; renderSNMPPanel(); });
    return;
  }

  const q = normalizeSearchText(snHostQuery);
  // 优先用 API 返回的 hostname；_cachedHosts 仅作兜底。
  const nameMap = {};
  (window._cachedHosts || []).forEach(h => { nameMap[h.id] = h; });

  let html = `<div class="nf-toolbar">`;
  html += `<input type="search" id="snHostSearch" class="nf-input" value="${esc(snHostQuery)}"
    placeholder="${esc(I18N.t("netflow.search_ph") || "搜索主机")}" autocomplete="off">`;
  html += `<select id="snHostSelect" class="nf-select">`;
  let shown = 0;
  snSNMPHosts.forEach(sh => {
    const h = nameMap[sh.host_id] || {};
    const name = sh.hostname || h.hostname || sh.host_id;
    const ip = sh.ip || h.ip || "";
    const hay = `${name} ${sh.host_id} ${ip}`;
    if (q && !matchesSearchTokens(hay, snHostQuery)) return;
    shown++;
    const sel = sh.host_id === snCurrentHost ? " selected" : "";
    const dev = Number(sh.devices) || 0;
    // 下拉直接标出设备数，一眼看出哪些主机纳管了网络设备
    html += `<option value="${esc(sh.host_id)}"${sel}>${esc(name)}${dev ? " · " + dev + " " + (I18N.t("snmp.dev_unit") || "设备") : ""}</option>`;
  });
  html += `</select>`;
  // Tab: 设备 / Trap
  html += `<span class="sn-tabs">`;
  html += `<button class="nf-btn${snTab === "devices" ? " sn-active" : ""}" data-snact="tab-devices">${esc(I18N.t("snmp.tab_devices") || "设备与接口")}</button>`;
  html += `<button class="nf-btn${snTab === "traps" ? " sn-active" : ""}" data-snact="tab-traps">${esc(I18N.t("snmp.tab_traps") || "Trap 事件")}</button>`;
  html += `</span>`;
  html += `<button class="nf-btn" data-snact="refresh">${I18N.t("common.refresh") || "刷新"}</button>`;
  html += `<button class="nf-btn" data-snact="ai">${esc(I18N.t("snmp.ai_diagnose") || "🤖 AI 诊断")}</button>`;
  html += `</div>`;

  if (snSNMPHosts.length === 0) {
    container.innerHTML = html + `<div class="empty-state">${I18N.t("snmp.no_snmp_hosts") || "暂无纳管网络设备的主机（未配置 SNMP 轮询/Trap，或 Agent 未上报）"}</div>`;
    snBindToolbar();
    return;
  }
  if (shown === 0) {
    container.innerHTML = html + `<div class="empty-state">${I18N.t("empty.no_host_match2") || "没有匹配的主机"}</div>`;
    snBindToolbar();
    return;
  }

  html += `<div id="snContent" class="nf-content"><div id="snBody"></div></div>`;
  container.innerHTML = html;

  const sel = $("snHostSelect");
  if (sel && sel.options.length > 0) {
    if (![...sel.options].some(o => o.value === snCurrentHost)) snCurrentHost = sel.options[0].value;
    sel.value = snCurrentHost;
  }
  snBindToolbar();
  if (snCurrentHost) loadSNMPData();
}

function snBindToolbar() {
  const sel = $("snHostSelect");
  sel && sel.addEventListener("change", function() { snCurrentHost = this.value; loadSNMPData(); });
  const search = $("snHostSearch");
  if (search) {
    const onSnSearch = function() {
      clearTimeout(snSearchT);
      const v = this.value;
      snSearchT = setTimeout(() => {
        snHostQuery = v;
        renderSNMPPanel();
        const s = $("snHostSearch");
        if (s) { s.focus(); s.setSelectionRange(s.value.length, s.value.length); }
      }, 200);
    };
    search.addEventListener("input", onSnSearch);
    search.addEventListener("search", onSnSearch);
  }
}

window.loadSNMPData = function() {
  const host = snCurrentHost || ($("snHostSelect") || {}).value;
  if (!host) return;
  const body = $("snBody");
  if (body) body.innerHTML = `<div class="loading-dots">${I18N.t("common.loading") || "加载中..."}</div>`;

  if (snTab === "traps") {
    fetch(`/api/v1/snmp/traps?host=${encodeURIComponent(host)}&limit=100`, { credentials: "same-origin" })
      .then(r => r.json())
      .then(data => { snTraps = data.traps || []; renderSNTraps(body, snTraps); })
      .catch(() => { if (body) body.innerHTML = `<div class="empty-state">${I18N.t("netflow.load_error") || "加载失败"}</div>`; });
  } else {
    fetch(`/api/v1/snmp/list?host=${encodeURIComponent(host)}`, { credentials: "same-origin" })
      .then(r => r.json())
      .then(data => { snDevices = data.devices || []; renderSNDevices(body, snDevices); })
      .catch(() => { if (body) body.innerHTML = `<div class="empty-state">${I18N.t("netflow.load_error") || "加载失败"}</div>`; });
  }
};

function snIfMatchesFilter(i) {
  if (snIfFilter === "up") return !!i.oper_up;
  if (snIfFilter === "down") return !i.oper_up;
  return true;
}

function snIfFilterChips() {
  const opts = [
    ["all", "snmp.if_all", "全部"],
    ["up", "snmp.if_up", "在线"],
    ["down", "snmp.if_down", "离线"],
  ];
  return `<span class="sn-if-filter" title="${esc(I18N.t("snmp.if_filter", "端口状态"))}">` +
    opts.map(([v, k, fb]) =>
      `<button type="button" class="nf-btn sm${snIfFilter === v ? " sn-active" : ""}" data-snifilter="${v}">${esc(I18N.t(k, fb))}</button>`
    ).join("") + `</span>`;
}

function renderSNDevices(container, devices) {
  if (!container) return;
  if (devices.length === 0) {
    container.innerHTML = `<div class="empty-state">${I18N.t("snmp.no_devices") || "暂无 SNMP 设备数据（未配置轮询目标或 Agent 未上报）"}</div>`;
    return;
  }
  let html = `<div class="sn-toolbar-row">${snIfFilterChips()}</div>`;
  devices.forEach(d => {
    const snap = d.snapshot || {};
    const sys = snap.system || {};
    const ifs = snap.interfaces || [];
    const up = ifs.filter(i => i.oper_up).length, down = ifs.length - up;
    const visible = ifs.filter(snIfMatchesFilter);
    const reachable = d.reachable !== false;
    html += `<div class="sn-dev">`;
    html += `<div class="sn-dev-head">
      <div class="sn-dev-title" title="${esc(d.device_name || "")}">${esc(d.device_name || "设备")}${reachable ? `<span class="sn-pill ok">${I18N.t("snmp.reachable") || "可达"}</span>` : `<span class="sn-pill bad">${I18N.t("snmp.unreachable") || "不可达"}</span>`}</div>
      <div class="sn-dev-sub" title="${esc((d.device_ip || "") + (sys.name ? " · " + sys.name : ""))}">${esc(d.device_ip || "")}${sys.name ? " · " + esc(sys.name) : ""}</div>
      <span style="flex:1"></span>
      <button class="icon-btn danger" data-sndel="${esc(d.device_name || "")}" data-snhost="${esc(snCurrentHost || "")}" title="${esc(I18N.t("snmp.delete") || "删除")}">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M3 6h18M8 6V4a2 2 0 012-2h4a2 2 0 012 2v2m3 0v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6h14z"/></svg>
      </button>
    </div>`;
    if (snap.error) {
      html += `<div class="empty-state">${esc(I18N.t("snmp.poll_error") || "采集失败")}: ${esc(snap.error)}</div></div>`;
      return;
    }
    html += `<div class="sn-stats">
      <span class="sn-stat" data-snifilter="all" style="cursor:pointer" title="${esc(I18N.t("snmp.if_all", "全部"))}"><b>${ifs.length}</b>${I18N.t("snmp.interfaces") || "接口"}</span>
      <span class="sn-stat ok" data-snifilter="up" style="cursor:pointer" title="${esc(I18N.t("snmp.if_up", "在线"))}"><b>${up}</b>UP</span>
      <span class="sn-stat ${down > 0 ? "bad" : ""}" data-snifilter="down" style="cursor:pointer" title="${esc(I18N.t("snmp.if_down", "离线"))}"><b>${down}</b>DOWN</span>
      <span class="sn-stat"><b>${snFmtUptime(sys.uptime_sec)}</b>${I18N.t("snmp.uptime") || "运行"}</span>
    </div>`;
    html += `<div class="nf-table-wrap"><table class="nf-flow-table"><thead><tr>`;
    ["snmp.if_name:接口", "snmp.if_status:状态", "snmp.if_speed:速率", "snmp.if_in:入向", "snmp.if_out:出向", "snmp.if_util:利用率", "snmp.if_err:错误/丢包", "snmp.history:历史趋势"].forEach(kv => {
      const [k, fb] = kv.split(":");
      html += `<th>${esc(I18N.t(k) || fb)}</th>`;
    });
    html += `</tr></thead><tbody>`;
    if (!visible.length) {
      html += `<tr><td colspan="8" class="sn-dim" style="text-align:center;padding:16px">${esc(I18N.t("snmp.if_none", "无匹配接口"))}</td></tr>`;
    } else {
      // 异常接口排前面
      const sorted = visible.slice().sort((a, b) => snIfBad(b) - snIfBad(a));
      sorted.forEach(i => {
        const util = Math.max(i.in_util_percent || 0, i.out_util_percent || 0);
        const err = (i.in_err_pps || 0) + (i.out_err_pps || 0) + (i.in_discard_pps || 0) + (i.out_discard_pps || 0);
        const isDown = i.admin_status === 1 && !i.oper_up;
        const rowCls = isDown ? " sn-row-crit" : (util >= 80 ? " sn-row-warn" : "");
        html += `<tr class="${rowCls.trim()}">`;
        html += `<td>${esc(i.name || ("if" + i.index))}${i.alias ? ` <span class="sn-dim">${esc(i.alias)}</span>` : ""}</td>`;
        html += `<td>${i.oper_up ? `<span class="sn-badge sn-up">UP</span>` : `<span class="sn-badge sn-down">DOWN</span>`}</td>`;
        html += `<td class="nf-mono">${snFmtSpeed(i.speed_bps)}</td>`;
        html += `<td class="nf-num">${i.rate_valid ? snFmtBps(i.in_bps) : "-"}</td>`;
        html += `<td class="nf-num">${i.rate_valid ? snFmtBps(i.out_bps) : "-"}</td>`;
        html += `<td>${i.rate_valid ? snUtilCell(util) : "-"}</td>`;
        html += `<td class="nf-num">${i.rate_valid ? err.toFixed(1) : "-"}</td>`;
        html += `<td><button class="nf-btn sm" data-snhist="1" data-device="${esc(d.device_name || "")}" data-device-ip="${esc(d.device_ip || "")}" data-ifindex="${esc(i.index)}" data-ifname="${esc(i.name || "")}">${esc(I18N.t("snmp.view_history") || "查看曲线")}</button></td>`;
        html += `</tr>`;
      });
    }
    html += `</tbody></table></div></div>`;
  });
  container.innerHTML = html;
}

function snIfBad(i) {
  if (i.admin_status === 1 && !i.oper_up) return 3;
  const util = Math.max(i.in_util_percent || 0, i.out_util_percent || 0);
  if (util >= 80) return 2;
  if (((i.in_err_pps || 0) + (i.out_err_pps || 0)) > 1) return 1;
  return 0;
}

function renderSNTraps(container, traps) {
  if (!container) return;
  if (traps.length === 0) {
    container.innerHTML = `<div class="empty-state">${I18N.t("snmp.no_traps") || "暂无 Trap 事件"}</div>`;
    return;
  }
  let html = `<div class="nf-section"><h3>${I18N.t("snmp.tab_traps") || "Trap 事件"}</h3>`;
  html += `<div class="nf-table-wrap"><table class="nf-flow-table"><thead><tr>`;
  ["snmp.trap_time:时间", "snmp.trap_severity:级别", "snmp.trap_source:来源", "snmp.trap_oid:Trap OID"].forEach(kv => {
    const [k, fb] = kv.split(":");
    html += `<th>${esc(I18N.t(k) || fb)}</th>`;
  });
  html += `</tr></thead><tbody>`;
  traps.forEach(t => {
    const sev = t.severity || "info";
    const sevCls = sev === "critical" ? "sn-down" : (sev === "warning" ? "sn-warn" : "sn-up");
    html += `<tr>`;
    html += `<td>${esc(snFmtTime(t.received_at))}</td>`;
    html += `<td><span class="sn-badge ${sevCls}">${esc(sev)}</span></td>`;
    html += `<td>${esc(t.source_ip || "")}</td>`;
    html += `<td class="sn-oid" title="${esc(t.trap_oid || "")}">${esc(t.trap_oid || "")}</td>`;
    html += `</tr>`;
  });
  html += `</tbody></table></div></div>`;
  container.innerHTML = html;
}

function snFmtUptime(sec) {
  sec = sec || 0;
  if (sec <= 0) return "-";
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}
function snFmtTime(t) {
  if (!t) return "";
  try { return new Date(t).toLocaleString(); } catch (e) { return String(t); }
}

function snHostDisplayName(hostID) {
  const row = (snSNMPHosts || []).find(h => h.host_id === hostID);
  if (row && row.hostname) return row.hostname;
  const cached = (window._cachedHosts || []).find(h => h.id === hostID);
  return (cached && cached.hostname) || hostID || "?";
}

// AI 诊断：把当前设备/Trap 数据整理成文本塞进 openAIAssist 的 context。
function snAIDiagnose() {
  const host = snCurrentHost || ($("snHostSelect") || {}).value;
  if (!host) return;
  const hn = snHostDisplayName(host);
  if (snTab === "traps") {
    if (!snTraps.length) { alert(I18N.t("snmp.no_traps") || "暂无 Trap 事件"); return; }
    let ctx = `主机 ${hn} 最近 SNMP Trap 事件（${snTraps.length} 条）:\n`;
    snTraps.slice(0, 80).forEach((t, i) => {
      ctx += `${i + 1}. [${t.severity}] 来自 ${t.source_ip} trapOID=${t.trap_oid} @ ${snFmtTime(t.received_at)}\n`;
    });
    openAIAssist({ task: "trap_diagnosis", title: I18N.t("snmp.ai_trap_title") || "🤖 AI Trap 诊断", mode: "analyze", context: ctx.slice(0, 14000) });
  } else {
    if (!snDevices.length) { alert(I18N.t("snmp.no_devices") || "暂无设备数据"); return; }
    let ctx = `主机 ${hn}\n` + snDevicesToText(snDevices);
    openAIAssist({ task: "snmp_diagnosis", title: I18N.t("snmp.ai_dev_title") || "🤖 AI 网络设备诊断", mode: "analyze", context: ctx.slice(0, 14000) });
  }
}

function snDevicesToText(devices) {
  let out = `主机 SNMP 网络设备快照（${devices.length} 台）:\n`;
  devices.forEach(d => {
    const snap = d.snapshot || {}, sys = snap.system || {}, ifs = snap.interfaces || [];
    if (snap.error) { out += `- ${d.device_name}（${d.device_ip}）采集失败: ${snap.error}\n`; return; }
    const up = ifs.filter(i => i.oper_up).length;
    out += `- ${d.device_name}（${d.device_ip}）${sys.name || ""} 接口 ${ifs.length}（up ${up}/down ${ifs.length - up}）运行 ${snFmtUptime(sys.uptime_sec)}\n`;
    ifs.forEach(i => {
      const util = Math.max(i.in_util_percent || 0, i.out_util_percent || 0);
      const err = (i.in_err_pps || 0) + (i.out_err_pps || 0) + (i.in_discard_pps || 0) + (i.out_discard_pps || 0);
      const bad = snIfBad(i);
      if (bad > 0) {
        out += `    ${i.name}: ${i.oper_up ? "UP" : "DOWN"}, 利用率 ${util.toFixed(0)}%, in ${snFmtBps(i.in_bps)}, out ${snFmtBps(i.out_bps)}, 错误/丢包 ${err.toFixed(1)}pps\n`;
      }
    });
  });
  return out;
}

function snMatrixSamples(series) {
  const byTs = new Map();
  const keys = Object.keys(series || {});
  keys.forEach(key => {
    (series[key] || []).forEach(result => {
      (result.values || []).forEach(pair => {
        const ts = Number(pair[0]), value = Number(pair[1]);
        if (!Number.isFinite(ts) || !Number.isFinite(value)) return;
        const sample = byTs.get(ts) || { timestamp: ts };
        sample[key] = value;
        byTs.set(ts, sample);
      });
    });
  });
  const rows = [...byTs.values()].sort((a, b) => a.timestamp - b.timestamp);
  return typeof alignJoinedSeriesSamples === "function"
    ? alignJoinedSeriesSamples(rows, keys)
    : rows;
}

function snHistoryControls(from, to) {
  const custom = snHist.custom;
  return `${renderChartControls(custom ? -1 : snHist.range, "snhrange")}
    <button class="chip-btn ${custom ? "active" : ""}" data-snh-custom-toggle>${I18N.t("time.custom") || "自定义"}</button>
    ${typeof forecastChipHTML === "function" ? forecastChipHTML("snmp") : ""}
    <span class="chart-custom-range" id="snhCustomPanel"${custom ? "" : " hidden"}>
      <input type="datetime-local" id="snhCustomFrom" class="dt-input" value="${toLocalDatetimeValue(from)}">
      <span class="dt-sep">→</span>
      <input type="datetime-local" id="snhCustomTo" class="dt-input" value="${toLocalDatetimeValue(to)}">
      <button class="chip-btn primary" data-snh-custom-apply>${I18N.t("time.custom_apply") || "应用"}</button>
    </span>`;
}

function snOpenInterfaceHistory(btn) {
  snHist = {
    host: snCurrentHost, device: btn.dataset.device || "", deviceIP: btn.dataset.deviceIp || "",
    ifindex: btn.dataset.ifindex || "", ifname: btn.dataset.ifname || "", range: 1, custom: null,
  };
  $("networkHistTitle").textContent = `${snHist.device} · ${snHist.ifname} · ${I18N.t("snmp.history") || "接口历史趋势"}`;
  $("networkHistMask").classList.add("show");
  snLoadInterfaceHistory();
}

async function snLoadInterfaceHistory() {
  const body = $("networkHistBody");
  if (!body) return;
  body.innerHTML = `<div class="empty-line">${I18N.t("ui.loading") || "加载中…"}</div>`;
  const now = Math.floor(Date.now() / 1000);
  const anchorKey = "snmp:" + (snHist.host || "") + ":" + (snHist.ifindex || "");
  const win = (typeof resolveAnchoredRange === "function")
    ? resolveAnchoredRange(anchorKey, snHist.range > 0 ? snHist.range : 1, snHist.custom)
    : { from: snHist.custom ? snHist.custom.from : now - snHist.range * 3600, to: snHist.custom ? snHist.custom.to : now };
  const from = win.from, to = win.to;
  const q = new URLSearchParams({
    host: snHist.host, device: snHist.device, device_ip: snHist.deviceIP,
    ifindex: snHist.ifindex, ifname: snHist.ifname, from: String(from), to: String(to),
  });
  const load = (typeof beginRangeLoad === "function")
    ? beginRangeLoad(anchorKey)
    : { signal: undefined, isCurrent: () => true };
  try {
    const opts = { credentials: "same-origin" };
    if (load.signal) opts.signal = load.signal;
    const data = await fetch(`${API}/snmp/interface-history?${q}`, opts).then(r => {
      if (!r.ok) throw new Error(r.statusText);
      return r.json();
    });
    if (!load.isCurrent()) return;
    const samples = snMatrixSamples(data.series || {});
    const controls = snHistoryControls(from, to);
    if (!samples.length) {
      body.innerHTML = `<div class="chart-controls">${controls}</div><div class="empty-line">${I18N.t("empty.no_trend_data") || "该时间范围暂无趋势数据"}</div>`;
      return;
    }
    const wrap = id => `<div class="chart-wrap"><canvas id="${id}" width="1000" height="240"></canvas><button class="chart-enlarge" data-sn-chart="${id}" title="${I18N.t("ui.zoom_preview") || "放大预览"}">⛶</button></div>`;
    body.innerHTML = `<div class="chart-controls">${controls}</div><div class="chart-container">
      ${wrap("snhThroughput")}${wrap("snhUtil")}${wrap("snhErrors")}${wrap("snhState")}${wrap("snhSpeed")}
    </div><div class="hint">${I18N.t("snmp.history_hint") || "包含收发速率、利用率、错误/丢包、链路状态和接口速率；悬停查看数值，拖动框选放大，双击还原。"}</div>`;
    const snOpts = (title, extra) => Object.assign({ title, cssH: 220, legendMode: "dash" }, extra || {});
    const specs = [
      { id: "snhThroughput", samples, series: [
        { key: "in_bps", label: I18N.t("snmp.if_in", "入向"), color: "#2fd07a", fmt: fmtRate },
        { key: "out_bps", label: I18N.t("snmp.if_out", "出向"), color: "#43b6f0", fmt: fmtRate },
      ], yMin: 0, yMax: null, opts: snOpts(I18N.t("snmp.throughput_history", "接口吞吐速率")) },
      { id: "snhUtil", samples, series: [
        { key: "in_util", label: I18N.t("snmp.in_util", "入向利用率"), color: "#8b5cf6", fmt: v => v.toFixed(1) + "%" },
        { key: "out_util", label: I18N.t("snmp.out_util", "出向利用率"), color: "#f7b23b", fmt: v => v.toFixed(1) + "%" },
      ], yMin: 0, yMax: 100, opts: snOpts(I18N.t("snmp.util_history", "接口利用率")) },
      { id: "snhErrors", samples, series: [
        { key: "in_err_pps", label: I18N.t("snmp.in_errors", "入向错误"), color: "#f2545b", fmt: v => v.toFixed(1) + " pps" },
        { key: "out_err_pps", label: I18N.t("snmp.out_errors", "出向错误"), color: "#e06c9a", fmt: v => v.toFixed(1) + " pps" },
        { key: "in_disc_pps", label: I18N.t("snmp.in_discards", "入向丢包"), color: "#f7b23b", fmt: v => v.toFixed(1) + " pps" },
        { key: "out_disc_pps", label: I18N.t("snmp.out_discards", "出向丢包"), color: "#ff8f5a", fmt: v => v.toFixed(1) + " pps" },
      ], yMin: 0, yMax: null, opts: snOpts(I18N.t("snmp.error_history", "接口错误与丢包")) },
      { id: "snhState", samples, series: [
        { key: "oper_up", label: I18N.t("snmp.if_status", "链路状态"), color: "#2fd07a", fmt: v => v >= .5 ? "UP" : "DOWN" },
      ], yMin: 0, yMax: 1, opts: snOpts(I18N.t("snmp.state_history", "链路状态（UP / DOWN）")) },
      { id: "snhSpeed", samples, series: [
        { key: "speed_bps", label: I18N.t("snmp.if_speed", "接口速率"), color: "#4c8dff", fmt: fmtRate },
      ], yMin: 0, yMax: null, opts: snOpts(I18N.t("snmp.speed_history", "接口协商速率")) },
    ];
    if (!load.isCurrent()) return;
    snHistCharts = typeof mountChartsWithForecast === "function"
      ? await mountChartsWithForecast("snmp", specs, load)
      : Object.fromEntries(specs.map(sp => [sp.id, createChart(sp.id, sp.samples, sp.series, sp.yMin, sp.yMax, sp.opts)]));
  } catch (e) {
    if (e && (e.name === "AbortError" || /aborted/i.test(String(e.message || e)))) return;
    if (!load.isCurrent()) return;
    body.innerHTML = `<div class="empty-line">${I18N.t("netflow.load_error") || "加载失败"}: ${esc(e)}</div>`;
  }
}
document.addEventListener("chart-forecast-toggle", (ev) => {
  if (ev.detail && ev.detail.scope === "snmp" && snHist && snHist.host) snLoadInterfaceHistory();
});

function snApplyCustomRange() {
  const f = $("snhCustomFrom"), t = $("snhCustomTo");
  if (!f || !t || !f.value || !t.value) return;
  const from = Math.floor(new Date(f.value).getTime() / 1000), to = Math.floor(new Date(t.value).getTime() / 1000);
  if (!Number.isFinite(from) || !Number.isFinite(to) || to <= from || to - from < 60) {
    toast(I18N.t("time.custom_order") || "请选择有效的时间范围（至少 1 分钟）", "warn");
    return;
  }
  snHist.custom = { from, to };
  snLoadInterfaceHistory();
}

// 事件委托（CSP: script-src 'self'，内联 onclick 会被拦）。
safeAddEventListener("snmpPanel", "click", e => {
  const ifFilter = e.target.closest("[data-snifilter]");
  if (ifFilter) {
    const v = ifFilter.dataset.snifilter;
    if (v && v !== snIfFilter) {
      snIfFilter = v;
      renderSNDevices($("snBody"), snDevices);
    }
    return;
  }
  const histBtn = e.target.closest("[data-snhist]");
  if (histBtn) { snOpenInterfaceHistory(histBtn); return; }
  const delBtn = e.target.closest("[data-sndel]");
  if (delBtn) {
    e.stopPropagation();
    const device = delBtn.dataset.sndel;
    const hostID = delBtn.dataset.snhost || snCurrentHost;
    if (!device || !hostID) return;
    if (!confirm(I18N.t("snmp.confirm_delete") || "确定删除该网络设备记录？删除后不可恢复。")) return;
    fetch(`/api/v1/snmp/${encodeURIComponent(hostID)}?device=${encodeURIComponent(device)}`, {
      method: "DELETE", credentials: "same-origin",
    })
      .then(r => r.ok ? r.json() : Promise.reject(r.statusText))
      .then(() => {
        if (typeof toast === "function") toast(I18N.t("toast.deleted") || "已删除", "ok");
        snSNMPHosts = null;
        renderSNMPPanel();
      })
      .catch(err => {
        if (typeof toast === "function") toast((I18N.t("snmp.delete_failed") || "删除失败") + ": " + err, "err");
        else alert((I18N.t("snmp.delete_failed") || "删除失败") + ": " + err);
      });
    return;
  }
  const b = e.target.closest("[data-snact]");
  if (!b) return;
  const act = b.dataset.snact;
  // 刷新：连「有网络设备的主机」列表一起重拉（否则新纳管的设备/主机不会出现在下拉里）。
  if (act === "refresh") { snSNMPHosts = null; renderSNMPPanel(); }
  else if (act === "ai") snAIDiagnose();
  else if (act === "tab-devices") { snTab = "devices"; renderSNMPPanel(); }
  else if (act === "tab-traps") { snTab = "traps"; renderSNMPPanel(); }
});

safeAddEventListener("networkHistBody", "click", e => {
  const range = e.target.closest("[data-snhrange]");
  if (range) {
    const next = parseInt(range.dataset.snhrange);
    const anchorKey = "snmp:" + (snHist.host || "") + ":" + (snHist.ifindex || "");
    if (snHist.custom || snHist.range !== next) {
      if (typeof clearAnchoredRange === "function") clearAnchoredRange(anchorKey);
    }
    snHist.range = next; snHist.custom = null; snLoadInterfaceHistory(); return;
  }
  if (e.target.closest("[data-snh-custom-toggle]")) {
    const p = $("snhCustomPanel"); if (p) p.hidden = !p.hidden;
    return;
  }
  if (e.target.closest("[data-snh-custom-apply]")) { snApplyCustomRange(); return; }
  const en = e.target.closest("[data-sn-chart]");
  if (en && snHistCharts[en.dataset.snChart]) openChartZoom(snHistCharts[en.dataset.snChart]);
});

if (typeof window._pageRenderers === "undefined") window._pageRenderers = {};
window._pageRenderers.snmp = renderSNMPPanel;

})();
