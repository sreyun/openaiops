// netflow.js — 网络流量面板 (Network Traffic Panel)
// Loaded as part of the unified app.js bundle.

(function() {
"use strict";

let nfCurrentHost = "";
let nfCurrentRange = "1h";
let nfHostQuery = "";       // 主机搜索词
let nfDimension = "dst_ip"; // Top-N 聚合维度（后端支持多种，之前前端写死了）
let nfSearchT = null;
let nfFlowPage = 1, nfFlowSize = 20; // Flow 明细分页（客户端）
// 「只列有流量的主机」：从 /api/v1/netflow/hosts 拉在所选时间窗内产生过 flow 的主机，
// 按字节降序（大流量在前）。null=未加载。换时间范围/刷新/进入视图时重新拉。
let nfTrafficHosts = null;
let nfLastSummary = null;  // 上次加载的 Top-N 汇总（{rows,dimension}），供 AI 分析
let nfLastFlows = null;    // 上次加载的 Flow 明细，供 AI 分析
let nfIPHist = { host: "", ip: "", dimension: "dst_ip", label: "", range: 1, custom: null };
let nfIPCharts = {};

function renderNetFlowPanel() {
  const container = $("netflowPanel");
  if (!container) return;

  // 先拉「有流量的主机」列表，再渲染面板。避免把成百上千台无流量主机塞进下拉。
  if (nfTrafficHosts === null) {
    container.innerHTML = `<div class="loading-dots">${I18N.t("common.loading") || "加载中..."}</div>`;
    fetch(`/api/v1/netflow/hosts?range=${encodeURIComponent(nfCurrentRange)}`, { credentials: "same-origin" })
      .then(r => r.json())
      .then(d => { nfTrafficHosts = d.hosts || []; renderNetFlowPanel(); })
      .catch(() => { nfTrafficHosts = []; renderNetFlowPanel(); });
    return;
  }

  const q = normalizeSearchText(nfHostQuery);
  // 优先用 API 返回的 hostname（服务端已 annotate）；_cachedHosts 仅作兜底。
  const nameMap = {};
  (window._cachedHosts || []).forEach(h => { nameMap[h.id] = h; });

  let html = `<div class="nf-toolbar">`;
  html += `<input type="search" id="nfHostSearch" class="nf-input" value="${esc(nfHostQuery)}"
    placeholder="${esc(I18N.t("netflow.search_ph") || "搜索主机")}" autocomplete="off">`;
  html += `<select id="nfHostSelect" class="nf-select">`;
  let shown = 0;
  nfTrafficHosts.forEach(th => {
    const h = nameMap[th.host_id] || {};
    const name = th.hostname || h.hostname || th.host_id;
    const ip = th.ip || h.ip || "";
    const hay = `${name} ${th.host_id} ${ip}`;
    if (q && !matchesSearchTokens(hay, nfHostQuery)) return;
    shown++;
    const sel = th.host_id === nfCurrentHost ? " selected" : "";
    // 下拉直接标出流量量级，一眼看出谁是大流量主机
    html += `<option value="${esc(th.host_id)}"${sel}>${esc(name)} · ${formatBytes(Number(th.bytes) || 0)}</option>`;
  });
  html += `</select>`;
  html += `<select id="nfRangeSelect" class="nf-select">`;
  [["1h", "最近1小时"], ["6h", "最近6小时"], ["24h", "最近24小时"], ["7d", "最近7天"]].forEach(([v, fb]) => {
    html += `<option value="${v}"${v === nfCurrentRange ? " selected" : ""}>${I18N.t("netflow.last_" + v) || fb}</option>`;
  });
  html += `</select>`;
  // 聚合维度：后端本来就支持，之前前端写死了 src_ip，等于把能力藏起来了
  html += `<select id="nfDimSelect" class="nf-select" title="${esc(I18N.t("netflow.dimension") || "聚合维度")}">`;
  [["dst_ip", "netflow.dst_ip", "目的IP"], ["src_ip", "netflow.src_ip", "源IP"],
   ["dst_port", "netflow.dst_port", "目的端口"], ["src_port", "netflow.src_port", "源端口"],
   ["protocol", "netflow.protocol", "协议"]].forEach(([v, k, fb]) => {
    html += `<option value="${v}"${v === nfDimension ? " selected" : ""}>${esc(I18N.t(k) || fb)}</option>`;
  });
  html += `</select>`;
  html += `<button class="nf-btn" data-nfact="refresh">${I18N.t("common.refresh") || "刷新"}</button>`;
  html += `<button class="nf-btn nf-ai-btn" data-nfact="ai" title="${esc(I18N.t("netflow.ai_hint", "AI 分析该主机流量并沉淀记忆"))}">🤖 ${esc(I18N.t("ai.analyze", "AI 分析"))}</button>`;
  html += `</div>`;

  if (nfTrafficHosts.length === 0) {
    container.innerHTML = html + `<div class="empty-state">${I18N.t("netflow.no_traffic_hosts") || "所选时间范围内没有产生流量的主机"}</div>`;
    nfBindToolbar();
    return;
  }
  if (shown === 0) {
    container.innerHTML = html + `<div class="empty-state">${I18N.t("empty.no_host_match2") || "没有匹配的主机"}</div>`;
    nfBindToolbar();
    return;
  }

  html += `<div id="nfContent" class="nf-content">`;
  html += `<div id="nfSummary" class="nf-section"><h3>${I18N.t("netflow.top_talkers") || "流量排行"}</h3><div id="nfSummaryBody"></div></div>`;
  html += `<div id="nfFlows" class="nf-section"><h3>${I18N.t("netflow.flow_detail") || "Flow 明细"}</h3><div id="nfFlowsBody"></div></div>`;
  html += `</div>`;

  container.innerHTML = html;

  // 之前选中的主机若还在筛选结果里就保持不变，否则退回第一个（流量最大的）——
  // 不然每次输入搜索词都会把选中的主机跳走。
  const sel = $("nfHostSelect");
  if (sel && sel.options.length > 0) {
    if (![...sel.options].some(o => o.value === nfCurrentHost)) nfCurrentHost = sel.options[0].value;
    sel.value = nfCurrentHost;
  }
  nfBindToolbar();
  if (nfCurrentHost) loadNetFlowData();
}

// nfBindToolbar 绑定工具栏事件。工具栏每次重渲染都会被替换掉，所以必须重新绑。
function nfBindToolbar() {
  const sel = $("nfHostSelect");
  sel && sel.addEventListener("change", function() { nfCurrentHost = this.value; loadNetFlowData(); });
  const rng = $("nfRangeSelect");
  rng && rng.addEventListener("change", function() {
    nfCurrentRange = this.value;
    nfTrafficHosts = null; // 换时间范围 → 重新拉「有流量的主机」（不同窗口主机集不同）
    renderNetFlowPanel();
  });
  const dim = $("nfDimSelect");
  dim && dim.addEventListener("change", function() { nfDimension = this.value; loadNetFlowData(); });

  const search = $("nfHostSearch");
  if (search) {
    const onNfSearch = function() {
      clearTimeout(nfSearchT);
      const v = this.value;
      nfSearchT = setTimeout(() => {
        nfHostQuery = v;
        renderNetFlowPanel();
        const s = $("nfHostSearch");
        if (s) { s.focus(); s.setSelectionRange(s.value.length, s.value.length); }
      }, 200);
    };
    search.addEventListener("input", onNfSearch);
    search.addEventListener("search", onNfSearch);
  }
}

window.loadNetFlowData = function() {
  const host = nfCurrentHost || ($("nfHostSelect") || {}).value;
  const range = nfCurrentRange || "1h";
  if (!host) return;

  const summaryBody = $("nfSummaryBody");
  const flowsBody = $("nfFlowsBody");
  if (summaryBody) summaryBody.innerHTML = `<div class="loading-dots">${I18N.t("common.loading") || "加载中..."}</div>`;
  if (flowsBody) flowsBody.innerHTML = "";

  // Fetch Top-N summary
  Promise.all([
    fetch(`/api/v1/netflow/summary?host=${encodeURIComponent(host)}&range=${range}&dimension=${encodeURIComponent(nfDimension)}&top=10`, { credentials: "same-origin" }).then(r => r.json()),
    fetch(`/api/v1/netflow/flows?host=${encodeURIComponent(host)}&range=${encodeURIComponent(range)}&limit=500`, { credentials: "same-origin" }).then(r => r.json()),
  ]).then(([sumData, flowData]) => {
    nfFlowPage = 1; // 新数据回到第一页
    nfLastSummary = { rows: sumData.summary || [], dimension: sumData.dimension || nfDimension };
    nfLastFlows = flowData.flows || [];
    renderNfSummary(summaryBody, sumData.summary || [], sumData.dimension || nfDimension);
    renderNfFlows(flowsBody, flowData.flows || []);
  }).catch(() => {
    if (summaryBody) summaryBody.innerHTML = `<div class="empty-state">${I18N.t("netflow.load_error") || "加载失败"}</div>`;
  });
};

function renderNfSummary(container, summary, dimension) {
  if (!container) return;
  if (summary.length === 0) {
    container.innerHTML = `<div class="empty-state">${I18N.t("netflow.no_data") || "暂无流量数据"}</div>`;
    return;
  }

  // 排行榜式：序号 + Key/域名 + 流量 + 底部占比条（比原来的三列扁平表更清晰、更商用）。
  const maxBytes = summary[0].bytes || 1;
  container.innerHTML = `<div class="nf-rank">` + summary.map((item, i) => {
    const pct = Math.max(2, (item.bytes / maxBytes) * 100);
    const dom = item.enrich && nfEnrichText(item.enrich) ? nfEnrichText(item.enrich) : "";
    const clickable = dimension === "src_ip" || dimension === "dst_ip";
    return `<div class="nf-rank-row${clickable ? " nf-rank-clickable" : ""}"${clickable ? ` data-nfip="${esc(item.key)}" data-nfdim="${esc(dimension)}"` : ""}>
      <span class="nf-rank-no">${i + 1}</span>
      <div class="nf-rank-main">
        <div class="nf-rank-head"><span class="nf-rank-key" title="${esc(item.key)}">${esc(item.key)}</span>${dom ? `<span class="nf-rank-dom" title="${esc(dom)}">${esc(dom)}</span>` : ""}<span class="nf-rank-bytes">${formatBytes(item.bytes)}</span></div>
        <div class="nf-rank-bar"><div class="nf-rank-fill" style="width:${pct}%"></div></div>
      </div>
    </div>`;
  }).join("") + `</div>`;
}

function renderNfFlows(container, flows) {
  if (!container) return;
  window._nfFlowsCache = flows; // 存全量（供 CSV 导出 + 分页）
  if (flows.length === 0) {
    container.innerHTML = `<div class="empty-state">${I18N.t("netflow.no_flows") || "暂无 Flow 记录"}</div>`;
    return;
  }
  const total = flows.length;
  nfFlowPage = tblClampPage(nfFlowPage, total, nfFlowSize);
  const pageFlows = flows.slice((nfFlowPage - 1) * nfFlowSize, nfFlowPage * nfFlowSize);

  let html = `<div class="nf-flows-toolbar">`;
  html += `<input id="nfFilterInput" type="text" class="nf-input" placeholder="${I18N.t("netflow.filter_placeholder") || "筛选: src_ip:10.0.0.1 或 dst_port:443"}">`;
  html += `<button class="nf-btn" data-nfact="filter">${I18N.t("netflow.filter") || "筛选"}</button>`;
  html += `<button class="nf-btn" data-nfact="export">${I18N.t("netflow.export_csv") || "导出 CSV"}</button>`;
  html += `</div>`;

  html += `<div class="nf-table-wrap"><table class="nf-flow-table">`;
  html += `<thead><tr>`;
  html += `<th>${I18N.t("netflow.source") || "来源"}</th>`;
  html += `<th>${I18N.t("netflow.src_ip") || "源IP"}</th>`;
  html += `<th>${I18N.t("netflow.src_port") || "源端口"}</th>`;
  html += `<th>${I18N.t("netflow.dst_ip") || "目的IP"}</th>`;
  html += `<th>${I18N.t("netflow.dst_port") || "目的端口"}</th>`;
  html += `<th>${I18N.t("netflow.proto") || "协议"}</th>`;
  html += `<th>${I18N.t("netflow.bytes") || "字节"}</th>`;
  html += `<th>${I18N.t("netflow.packets") || "包"}</th>`;
  html += `<th>${I18N.t("netflow.avg_pkt") || "平均包长"}</th>`;
  html += `<th>${I18N.t("netflow.duration") || "时长"}</th>`;
  html += `<th>${I18N.t("netflow.last_seen") || "最后活跃"}</th>`;
  html += `</tr></thead><tbody>`;

  pageFlows.forEach(f => {
    const protoName = protoNameMap(f.protocol);
    const bytes = Number(f.bytes) || 0, pkts = Number(f.packets) || 0;
    const avgPkt = pkts > 0 ? Math.round(bytes / pkts) : 0; // 平均包长，辅助识别小包攻击/大流传输
    const dur = nfDurationSec(f);
    html += `<tr>`;
    html += `<td><span class="nf-badge nf-badge-${f.source}">${f.source}</span></td>`;
    html += `<td class="nf-ipcell">${nfIPCell(f.src_ip, f.src_enrich, "src_ip")}</td>`;
    html += `<td class="nf-mono">${f.src_port || ""}</td>`;
    html += `<td class="nf-ipcell">${nfIPCell(f.dst_ip, f.dst_enrich, "dst_ip")}</td>`;
    html += `<td class="nf-mono">${f.dst_port || ""}</td>`;
    html += `<td><span class="nf-proto nf-proto-${(protoName || "").toLowerCase()}">${protoName}</span></td>`;
    html += `<td class="nf-num">${formatBytes(bytes)}</td>`;
    html += `<td class="nf-num">${pkts}</td>`;
    html += `<td class="nf-num">${avgPkt ? avgPkt + " B" : "-"}</td>`;
    html += `<td class="nf-num">${dur === "" ? "-" : dur + " s"}</td>`;
    html += `<td class="nf-mono">${esc(nfShortTime(f.last_seen))}</td>`;
    html += `</tr>`;
  });
  html += `</tbody></table></div>`;
  html += tblPager(total, nfFlowPage, nfFlowSize);
  container.innerHTML = html;
}

window.applyNfFilter = function(customFrom, customTo) {
  const filter = ($("nfFilterInput") || {}).value || "";
  if (!filter) { loadNetFlowData(); return; }
  const host = nfCurrentHost || ($("nfHostSelect") || {}).value;
  if (!host) return;

  nfFlowPage = 1; // 筛选后回到第一页
  const timeQuery = Number.isFinite(customFrom) && Number.isFinite(customTo)
    ? `from=${customFrom}&to=${customTo}`
    : `range=${encodeURIComponent(nfCurrentRange || "1h")}`;
  fetch(`/api/v1/netflow/flows?host=${encodeURIComponent(host)}&filter=${encodeURIComponent(filter)}&${timeQuery}&limit=500`, { credentials: "same-origin" })
    .then(r => r.json())
    .then(data => renderNfFlows($("nfFlowsBody"), data.flows || []))
    .catch(() => {});
};

window.exportNfCSV = function() {
  const flows = window._nfFlowsCache || [];
  if (flows.length === 0) return;

  // 富化字段（反查域名 / WHOIS 组织名）是外部可影响的文本：org 里带逗号（"GOOGLE, US"）
  // 只是显示错位，以 = 开头则会在 Excel 里当公式执行。整表统一走 expRowsToCsv：
  // 每一格都加引号（原来只有 5 个富化字段加，first_seen 之类含逗号就会串列），并中和公式。
  const head = ["source", "src_ip", "src_port", "dst_ip", "dst_port", "protocol", "bytes", "packets",
    "first_seen", "last_seen", "dst_host", "dst_org", "dst_country", "src_host", "src_org"];
  const csv = expRowsToCsv(head, flows.map(f => {
    const de = f.dst_enrich || {}, se = f.src_enrich || {};
    return [f.source, f.src_ip, f.src_port, f.dst_ip, f.dst_port, f.protocol, f.bytes, f.packets,
      f.first_seen || "", f.last_seen || "", de.host, de.org, de.country, se.host, se.org];
  }));

  // BOM：中文组织名（"阿里云计算有限公司"）不带 BOM 在 Excel 里就是乱码。
  const blob = new Blob([EXP_CSV_BOM + csv], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `netflow_flows_${new Date().toISOString().slice(0, 10)}.csv`;
  a.click();
  URL.revokeObjectURL(url);
};

function protoNameMap(proto) {
  switch (proto) {
    case 1: return "ICMP";
    case 6: return "TCP";
    case 17: return "UDP";
    default: return proto;
  }
}

// nfDurationSec 计算一条 flow 的持续秒数（last_seen - first_seen），无效则返回 ""。
function nfDurationSec(f) {
  if (!f.first_seen || !f.last_seen) return "";
  const d = (new Date(f.last_seen) - new Date(f.first_seen)) / 1000;
  return (isNaN(d) || d < 0) ? "" : Math.round(d);
}

// nfShortTime 把 ISO 时间串格式化为本地时分秒；无效返回 ""。
function nfShortTime(s) {
  if (!s) return "";
  const d = new Date(s);
  return isNaN(d) ? "" : d.toLocaleTimeString();
}

// nfEnrichText 把富化结果拼成「域名 · 归属组织 · 国家」，回答"这个 IP 属于谁/在访问什么"。
function nfEnrichText(e) {
  if (!e) return "";
  const parts = [];
  if (e.host) parts.push(e.host);
  if (e.org) parts.push(e.org);
  if (e.country) parts.push(e.country);
  return parts.join(" · ");
}
// nfIPCell 渲染一个 IP 单元格：IP 在上，富化的域名/归属在下（小字）。
function nfIPCell(ip, enrich, dimension) {
  const sub = nfEnrichText(enrich);
  return `<button class="nf-ip-link" data-nfip="${esc(ip || "")}" data-nfdim="${esc(dimension || "dst_ip")}" title="${esc(I18N.t("netflow.view_ip_history") || "查看该 IP 历史趋势与下钻分析")}">${esc(ip || "")}</button>${sub ? `<div style="font-size:11px;color:var(--muted);margin-top:2px">${esc(sub)}</div>` : ""}`;
}

function formatBytes(bytes) {
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  return (bytes / Math.pow(1024, i)).toFixed(1) + " " + units[i];
}

// nfDimLabel 把聚合维度 code 映射为可读标签（供 AI 上下文）。
function nfDimLabel(dim) {
  return ({ dst_ip: "目的IP", src_ip: "源IP", dst_port: "目的端口", src_port: "源端口", protocol: "协议" })[dim] || dim;
}

// netflowToText 把当前主机的流量快照（Top-N 汇总 + Top Flow 明细）汇总为纯文本，供 AI 分析。
function netflowToText() {
  const nameMap = {};
  (window._cachedHosts || []).forEach(h => { nameMap[h.id] = h; });
  const hn = (nameMap[nfCurrentHost] || {}).hostname || nfCurrentHost || "?";
  const sum = (nfLastSummary && nfLastSummary.rows) || [];
  const flows = nfLastFlows || [];
  if (!sum.length && !flows.length) return "（当前主机在所选时间范围内暂无流量数据）";
  const lines = [`主机：${hn}（${nfCurrentHost}）　时间范围：${nfCurrentRange}　聚合维度：${nfDimLabel(nfDimension)}`];
  if (sum.length) {
    lines.push(`\n# Top ${sum.length} ${nfDimLabel((nfLastSummary && nfLastSummary.dimension) || nfDimension)}（按流量降序）`);
    sum.forEach((it, i) => {
      const en = nfEnrichText(it.enrich);
      lines.push(`  ${i + 1}. ${it.key}${en ? "（" + en + "）" : ""} = ${formatBytes(Number(it.bytes) || 0)}`);
    });
  }
  if (flows.length) {
    const top = flows.slice().sort((a, b) => (Number(b.bytes) || 0) - (Number(a.bytes) || 0)).slice(0, 40);
    lines.push(`\n# Top ${top.length} Flow（共 ${flows.length} 条，按字节降序）`);
    top.forEach(f => {
      const pkts = Number(f.packets) || 0, bytes = Number(f.bytes) || 0;
      const avg = pkts > 0 ? Math.round(bytes / pkts) : 0;
      const dur = nfDurationSec(f);
      const den = nfEnrichText(f.dst_enrich);
      lines.push(`  - ${f.src_ip || "?"}:${f.src_port || "?"} → ${f.dst_ip || "?"}:${f.dst_port || "?"}${den ? "[" + den + "]" : ""} ${protoNameMap(f.protocol)} ${formatBytes(bytes)} ${pkts}包 均包${avg}B 时长${dur === "" ? "-" : dur + "s"}`);
    });
  }
  return lines.join("\n").slice(0, 12000);
}

// nfOpenAI 打开 AI 面板研判主机流量；仅人工采纳/反馈后的结果进入学习闭环。
function nfOpenAI() {
  if (typeof openAIAssist !== "function") { if (typeof toast === "function") toast(I18N.t("assist.unavailable", "AI 面板未就绪"), "err"); return; }
  if (!nfCurrentHost) { if (typeof toast === "function") toast(I18N.t("netflow.no_data", "暂无流量数据"), "err"); return; }
  openAIAssist({ task: "netflow_diagnosis", mode: "analyze", title: I18N.t("assist.title_netflow", "AI · 流量分析"), context: netflowToText() });
}

function nfHistoryControls(from, to) {
  const custom = nfIPHist.custom;
  return `${renderChartControls(custom ? -1 : nfIPHist.range, "nfhrange")}
    <button class="chip-btn ${custom ? "active" : ""}" data-nfh-custom-toggle>${I18N.t("time.custom") || "自定义"}</button>
    ${typeof forecastChipHTML === "function" ? forecastChipHTML("netflow") : ""}
    <span class="chart-custom-range" id="nfhCustomPanel"${custom ? "" : " hidden"}>
      <input type="datetime-local" id="nfhCustomFrom" class="dt-input" value="${toLocalDatetimeValue(from)}">
      <span class="dt-sep">→</span>
      <input type="datetime-local" id="nfhCustomTo" class="dt-input" value="${toLocalDatetimeValue(to)}">
      <button class="chip-btn primary" data-nfh-custom-apply>${I18N.t("time.custom_apply") || "应用"}</button>
    </span>`;
}

function nfOpenIPHistory(ip, dimension, keepRange) {
  if (!ip) return;
  const prev = nfIPHist;
  nfIPHist = {
    host: nfCurrentHost, ip, dimension: dimension === "src_ip" ? "src_ip" : "dst_ip",
    label: ip, range: keepRange ? prev.range : 1, custom: keepRange ? prev.custom : null,
  };
  $("networkHistTitle").textContent = `${ip} · ${I18N.t("netflow.ip_history") || "IP 流量历史与下钻"}`;
  $("networkHistMask").classList.add("show");
  nfLoadIPHistory();
}

function nfDrillList(title, rows, kind) {
  if (!rows || !rows.length) return `<div class="nf-drill-card"><h4>${esc(title)}</h4><div class="sn-dim">—</div></div>`;
  const max = Number(rows[0].bytes) || 1;
  return `<div class="nf-drill-card"><h4>${esc(title)}</h4>${rows.map(r => {
    const key = kind === "protocol" ? protoNameMap(Number(r.key)) : r.key;
    const en = r.enrich ? nfEnrichText(r.enrich) : "";
    const attrs = kind === "peer" ? ` data-nf-peer="${esc(r.key)}"` : "";
    return `<button class="nf-drill-row"${attrs}><span><b>${esc(key)}</b>${en ? `<small>${esc(en)}</small>` : ""}</span><span>${formatBytes(Number(r.bytes) || 0)} · ${Number(r.flows) || 0} Flow</span><i style="width:${Math.max(2, (Number(r.bytes) || 0) / max * 100)}%"></i></button>`;
  }).join("")}</div>`;
}

async function nfLoadIPHistory() {
  const body = $("networkHistBody");
  if (!body) return;
  body.innerHTML = `<div class="empty-line">${I18N.t("ui.loading") || "加载中…"}</div>`;
  const now = Math.floor(Date.now() / 1000);
  const anchorKey = "netflow:" + (nfIPHist.host || "") + ":" + (nfIPHist.ip || "") + ":" + (nfIPHist.dimension || "");
  const win = (typeof resolveAnchoredRange === "function")
    ? resolveAnchoredRange(anchorKey, nfIPHist.range > 0 ? nfIPHist.range : 1, nfIPHist.custom)
    : { from: nfIPHist.custom ? nfIPHist.custom.from : now - nfIPHist.range * 3600, to: nfIPHist.custom ? nfIPHist.custom.to : now };
  const from = win.from, to = win.to;
  const q = new URLSearchParams({
    host: nfIPHist.host, ip: nfIPHist.ip, dimension: nfIPHist.dimension,
    from: String(from), to: String(to),
  });
  const load = (typeof beginRangeLoad === "function")
    ? beginRangeLoad(anchorKey)
    : { signal: undefined, isCurrent: () => true };
  try {
    const opts = { credentials: "same-origin" };
    if (load.signal) opts.signal = load.signal;
    const data = await fetch(`${API}/netflow/ip-history?${q}`, opts).then(r => {
      if (!r.ok) throw new Error(r.statusText);
      return r.json();
    });
    if (!load.isCurrent()) return;
    const samples = data.points || [];
    const controls = nfHistoryControls(from, to);
    if (!samples.length) {
      body.innerHTML = `<div class="chart-controls">${controls}</div><div class="empty-line">${I18N.t("empty.no_trend_data") || "该时间范围暂无趋势数据"}</div>`;
      return;
    }
    const wrap = id => `<div class="chart-wrap"><canvas id="${id}" width="1000" height="240"></canvas><button class="chart-enlarge" data-nf-chart="${id}" title="${I18N.t("ui.zoom_preview") || "放大预览"}">⛶</button></div>`;
    const totalBytes = samples.reduce((s, p) => s + (Number(p.bytes) || 0), 0);
    const totalPackets = samples.reduce((s, p) => s + (Number(p.packets) || 0), 0);
    const totalFlows = samples.reduce((s, p) => s + (Number(p.flows) || 0), 0);
    body.innerHTML = `<div class="chart-controls">${controls}</div>
      <div class="nf-ip-kpis"><span><b>${formatBytes(totalBytes)}</b>${I18N.t("netflow.bytes") || "流量"}</span><span><b>${totalPackets}</b>${I18N.t("netflow.packets") || "包"}</span><span><b>${totalFlows}</b>Flow</span><button class="nf-btn" data-nf-filter-ip="1">${I18N.t("netflow.related_flows") || "查看相关 Flow"}</button></div>
      <div class="chart-container">${wrap("nfhTraffic")}${wrap("nfhPackets")}${wrap("nfhActivity")}${wrap("nfhPacketSize")}</div>
      <div class="nf-drill-grid">
        ${nfDrillList(I18N.t("netflow.top_peers") || "主要通信对端", data.peers, "peer")}
        ${nfDrillList(I18N.t("netflow.protocol_breakdown") || "协议分布", data.protocols, "protocol")}
        ${nfDrillList(I18N.t("netflow.dst_port_breakdown") || "目的端口", data.dst_ports, "port")}
        ${nfDrillList(I18N.t("netflow.src_port_breakdown") || "源端口", data.src_ports, "port")}
      </div><div class="hint">${I18N.t("netflow.drill_hint") || "点击通信对端可继续下钻；曲线按时间桶聚合，包含流量、包数、Flow 数、对端数和平均包长。"}</div>`;
    const nfOpts = (title) => ({ title, cssH: 220, legendMode: "dash" });
    const specs = [
      { id: "nfhTraffic", samples, series: [
        { key: "bytes", label: I18N.t("netflow.bytes", "字节"), color: "#4c8dff", fmt: formatBytes },
      ], yMin: 0, yMax: null, opts: nfOpts(I18N.t("netflow.traffic_history", "分桶流量")) },
      { id: "nfhPackets", samples, series: [
        { key: "packets", label: I18N.t("netflow.packets", "包"), color: "#2fd07a", fmt: v => v.toFixed(0) },
      ], yMin: 0, yMax: null, opts: nfOpts(I18N.t("netflow.packet_history", "分桶包数")) },
      { id: "nfhActivity", samples, series: [
        { key: "flows", label: "Flow", color: "#8b5cf6", fmt: v => v.toFixed(0) },
        { key: "peers", label: I18N.t("netflow.peers", "通信对端"), color: "#f7b23b", fmt: v => v.toFixed(0) },
      ], yMin: 0, yMax: null, opts: nfOpts(I18N.t("netflow.activity_history", "连接活跃度")) },
      { id: "nfhPacketSize", samples, series: [
        { key: "avg_packet_bytes", label: I18N.t("netflow.avg_pkt", "平均包长"), color: "#e06c9a", fmt: v => v.toFixed(0) + " B" },
      ], yMin: 0, yMax: null, opts: nfOpts(I18N.t("netflow.packet_size_history", "平均包长")) },
    ];
    if (!load.isCurrent()) return;
    nfIPCharts = typeof mountChartsWithForecast === "function"
      ? await mountChartsWithForecast("netflow", specs, load)
      : Object.fromEntries(specs.map(sp => [sp.id, createChart(sp.id, sp.samples, sp.series, sp.yMin, sp.yMax, sp.opts)]));
  } catch (e) {
    if (e && (e.name === "AbortError" || /aborted/i.test(String(e.message || e)))) return;
    if (!load.isCurrent()) return;
    body.innerHTML = `<div class="empty-line">${I18N.t("netflow.load_error") || "加载失败"}: ${esc(e)}</div>`;
  }
}
document.addEventListener("chart-forecast-toggle", (ev) => {
  if (ev.detail && ev.detail.scope === "netflow" && nfIPHist && nfIPHist.ip) nfLoadIPHistory();
});

function nfApplyIPCustomRange() {
  applyCustomRangeFromInputs($("nfhCustomFrom"), $("nfhCustomTo"), (from, to) => {
    nfIPHist.custom = { from, to };
    nfLoadIPHistory();
  });
}

// Register with navigation
// 事件委托：CSP 为 script-src 'self'，内联 onclick 会被浏览器拦截；且这些函数在 IIFE 内、
// 不挂 window，内联写法即便没有 CSP 也会 ReferenceError。刷新/筛选/导出此前因此全是死按钮。
safeAddEventListener("netflowPanel", "click", e => {
  const ip = e.target.closest("[data-nfip]");
  if (ip) { nfOpenIPHistory(ip.dataset.nfip, ip.dataset.nfdim, false); return; }
  const pg = e.target.closest("[data-pg]"); // Flow 明细分页（客户端，用缓存不重查）
  if (pg) {
    if (pg.dataset.pg === "prev") nfFlowPage--; else if (pg.dataset.pg === "next") nfFlowPage++;
    renderNfFlows($("nfFlowsBody"), window._nfFlowsCache || []);
    return;
  }
  const b = e.target.closest("[data-nfact]");
  if (!b) return;
  // 刷新：连「有流量的主机」列表一起重拉（否则新上流量的主机不会出现在下拉里）
  if (b.dataset.nfact === "refresh") { nfTrafficHosts = null; renderNetFlowPanel(); }
  else if (b.dataset.nfact === "filter") applyNfFilter();
  else if (b.dataset.nfact === "export") exportNfCSV();
  else if (b.dataset.nfact === "ai") nfOpenAI();
});
safeAddEventListener("networkHistBody", "click", e => {
  const range = e.target.closest("[data-nfhrange]");
  if (range) {
    const next = parseInt(range.dataset.nfhrange);
    const anchorKey = "netflow:" + (nfIPHist.host || "") + ":" + (nfIPHist.ip || "") + ":" + (nfIPHist.dimension || "");
    if (nfIPHist.custom || nfIPHist.range !== next) {
      if (typeof clearAnchoredRange === "function") clearAnchoredRange(anchorKey);
    }
    nfIPHist.range = next; nfIPHist.custom = null; nfLoadIPHistory(); return;
  }
  if (e.target.closest("[data-nfh-custom-toggle]")) {
    const p = $("nfhCustomPanel"); if (p) p.hidden = !p.hidden;
    return;
  }
  if (e.target.closest("[data-nfh-custom-apply]")) { nfApplyIPCustomRange(); return; }
  const en = e.target.closest("[data-nf-chart]");
  if (en && nfIPCharts[en.dataset.nfChart]) { openChartZoom(nfIPCharts[en.dataset.nfChart]); return; }
  const peer = e.target.closest("[data-nf-peer]");
  if (peer) {
    const opposite = nfIPHist.dimension === "src_ip" ? "dst_ip" : "src_ip";
    nfOpenIPHistory(peer.dataset.nfPeer, opposite, true);
    return;
  }
  if (e.target.closest("[data-nf-filter-ip]")) {
    $("networkHistMask").classList.remove("show");
    const input = $("nfFilterInput");
    if (input) input.value = `${nfIPHist.dimension}:${nfIPHist.ip}`;
    const now = Math.floor(Date.now() / 1000);
    const from = nfIPHist.custom ? nfIPHist.custom.from : now - nfIPHist.range * 3600;
    const to = nfIPHist.custom ? nfIPHist.custom.to : now;
    applyNfFilter(from, to);
  }
});
safeAddEventListener("netflowPanel", "change", e => {
  if (e.target.dataset && e.target.dataset.pg === "size") { nfFlowSize = +e.target.value || 20; nfFlowPage = 1; renderNfFlows($("nfFlowsBody"), window._nfFlowsCache || []); }
});

if (typeof window._pageRenderers === "undefined") window._pageRenderers = {};
// 每次进入「网络」视图都重拉有流量的主机（时间窗内的流量主机集会变化）。
window._pageRenderers.netflow = function() { nfTrafficHosts = null; renderNetFlowPanel(); };

})();
