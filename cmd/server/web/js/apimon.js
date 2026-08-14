/* ========== API 性能监控 ==========
 * 按「业务系统」分组批量监控接口：聚合表(最新状态 / 本次·平均·P95 响应时间 /
 * 1h·24h 可用率 / 吞吐) + 历史曲线(复用拨测历史弹窗) + 异常告警。数据来自
 * GET /apimon/systems（实时状态由内存、聚合由 VM 现算），写入 VM 后重启不丢。
 */
let LAST_APIMON = { systems: [] };
let APIMON_PICK_COLLAPSED = new Set();
let APIMON_PICK_Q = "";
let APIMON_PICK_SELECTED = new Set();

async function loadAPIMon() {
  try {
    const data = await fetch(`${API}/apimon/systems`).then(r => r.json());
    LAST_APIMON = data && data.systems ? data : { systems: [] };
    renderAPIMon(LAST_APIMON);
  } catch (e) { /* ignore */ }
}

// 可用率着色：≥99.9 绿、≥99 黄、否则红；<0 表示暂无数据。
function apimonAvailClass(v) {
  if (v < 0) return "";
  if (v >= 99.9) return "ok";
  if (v >= 99) return "warn";
  return "crit";
}
function apiFmtPct(v) { return v < 0 ? "—" : v.toFixed(2) + "%"; }
function apiFmtMs(v) { return (!v || v <= 0) ? "—" : v.toFixed(0) + " ms"; }
function envLabel(env) { return { prod: "生产 prod", staging: "预发 staging", dev: "测试 dev" }[env] || env || "无"; }

if (!window._apimonHostTreesRefreshBound) {
  window._apimonHostTreesRefreshBound = true;
  let _apiTreeRefreshT = null;
  document.addEventListener("aiops:host-trees-refresh", () => {
    if (!$("apiSysHosts") || !$("apimonMask") || !$("apimonMask").classList.contains("show")) return;
    if (_apiTreeRefreshT) clearTimeout(_apiTreeRefreshT);
    _apiTreeRefreshT = setTimeout(() => {
      _apiTreeRefreshT = null;
      if (typeof apiCaptureHostPick === "function") apiCaptureHostPick();
      window._apimonHosts = (typeof LAST_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS)) ? LAST_HOSTS : (window._apimonHosts || []);
      paintApiSysHostPicker(APIMON_PICK_SELECTED);
    }, 250);
  });
}

function paintApiSysHostPicker(selected) {
  const hc = $("apiSysHosts");
  if (!hc) return;
  APIMON_PICK_SELECTED = selected instanceof Set ? selected : new Set(selected || []);
  const hosts = window._apimonHosts || [];
  if (!hosts.length) {
    hc.innerHTML = `<span class="muted">暂无已纳管主机</span>`;
    return;
  }
  if (!window.HostPicker) {
    hc.innerHTML = hosts.map(h => {
      const lab = `${h.hostname || h.id}${h.ip ? " (" + h.ip + ")" : ""}`;
      return `<label class="host-chk"><input type="checkbox" class="ep-host" value="${esc(h.id)}" ${APIMON_PICK_SELECTED.has(h.id) ? "checked" : ""}> ${esc(lab)}</label>`;
    }).join("");
    return;
  }
  hc.innerHTML = HostPicker.renderHTML({
    id: "apiSysHostTree",
    mode: "multi",
    hosts,
    selected: APIMON_PICK_SELECTED,
    collapsed: APIMON_PICK_COLLAPSED,
    q: APIMON_PICK_Q,
    onlineOnly: false,
    compact: true,
  });
  const root = hc.querySelector(".host-picker") || hc;
  root._hpBound = false;
  HostPicker.bind(root, {
    onToggleFold: (id) => {
      apiCaptureHostPick();
      if (APIMON_PICK_COLLAPSED.has(id)) APIMON_PICK_COLLAPSED.delete(id); else APIMON_PICK_COLLAPSED.add(id);
      paintApiSysHostPicker(APIMON_PICK_SELECTED);
    },
    onSearch: (q) => {
      apiCaptureHostPick();
      APIMON_PICK_Q = q || "";
      paintApiSysHostPicker(APIMON_PICK_SELECTED);
    },
    onQuick: (act) => {
      apiCaptureHostPick();
      if (act === "clear") APIMON_PICK_SELECTED.clear();
      else hosts.forEach(h => APIMON_PICK_SELECTED.add(h.id));
      paintApiSysHostPicker(APIMON_PICK_SELECTED);
    },
    onFolderToggle: (fid, checked) => {
      apiCaptureHostPick();
      const q = (APIMON_PICK_Q || "").trim().toLowerCase();
      const byFolder = HostPicker.hostsByFolder(hosts);
      let ids = [];
      if (String(fid).startsWith("cat:")) {
        const cat = String(fid).slice(4);
        ids = hosts.filter(h => {
          const c = (h.category || "").trim() || "未分组";
          return c === cat && HostPicker.filterHost(h, q);
        }).map(h => h.id);
      } else if (fid === "__ungrouped__") {
        ids = (byFolder.get("__ungrouped__") || []).filter(h => HostPicker.filterHost(h, q)).map(h => h.id);
      } else {
        const find = (nodes) => {
          for (const n of nodes || []) {
            if (n.id === fid) return n;
            const c = find(n.children || []);
            if (c) return c;
          }
          return null;
        };
        const node = find(HostPicker.folderTree());
        if (node) ids = HostPicker.collectFolderHostIds(node, byFolder, q, false);
      }
      ids.forEach(id => { if (checked) APIMON_PICK_SELECTED.add(id); else APIMON_PICK_SELECTED.delete(id); });
      paintApiSysHostPicker(APIMON_PICK_SELECTED);
    },
    onHostToggle: (id, checked) => {
      if (checked) APIMON_PICK_SELECTED.add(id); else APIMON_PICK_SELECTED.delete(id);
    },
  });
}

function apiCaptureHostPick() {
  const hc = $("apiSysHosts");
  if (!hc || !window.HostPicker) return;
  const set = HostPicker.readMulti(hc);
  APIMON_PICK_SELECTED.clear();
  set.forEach(id => APIMON_PICK_SELECTED.add(id));
}

function renderAPIMon(data) {
  const wrap = $("apimonSystems");
  if (!wrap) return;
  const allSystems = data.systems || [];
  const envF = window.APIMON_ENV_FILTER || "";
  const systems = envF ? allSystems.filter(s => (s.env || "") === envF) : allSystems;
  if (!systems.length) {
    wrap.innerHTML = allSystems.length
      ? `<div class="empty-box">环境筛选「${esc(envLabel(envF))}」下暂无业务系统。</div>`
      : `<div class="empty-box">还没有可靠性保障监控。点右上角「添加业务系统」把一个系统的多个接口批量纳入监控——支持 HTTP / GraphQL / WebSocket，自动统计可用率、平均 / P95 / P99 响应时间与吞吐，异常时按配置级别自动告警。</div>`;
    return;
  }
  // 顶部汇总条：业务系统 / 接口总数 / 当前异常 / 平均可用率(1h)
  const allEps = systems.flatMap(s => s.endpoints || []);
  const downCount = allEps.filter(e => e.down).length;
  const withAvail = allEps.filter(e => e.avail_1h >= 0);
  const avgAvail = withAvail.length ? withAvail.reduce((s, e) => s + e.avail_1h, 0) / withAvail.length : -1;
  const summary = `<div class="api-summary">
    <div class="card-stat"><div class="n">${systems.length}</div><div class="l">业务系统</div></div>
    <div class="card-stat"><div class="n">${allEps.length}</div><div class="l">监控接口</div></div>
    <div class="card-stat"><div class="n ${downCount ? "crit" : "ok"}">${downCount}</div><div class="l">当前异常</div></div>
    <div class="card-stat"><div class="n ${avgAvail >= 0 ? apimonAvailClass(avgAvail) : ""}">${apiFmtPct(avgAvail)}</div><div class="l">平均可用率 (1h)</div></div>
  </div>`;
  wrap.innerHTML = summary + systems.map(sys => {
    const eps = sys.endpoints || [];
    const downCount = eps.filter(e => e.down).length;
    const rows = eps.length ? eps.map(ep => {
      const dot = !ep.checked_at ? '<span class="sdot idle"></span>' : (ep.ok ? '<span class="sdot ok"></span>' : '<span class="sdot crit"></span>');
      const statusText = !ep.checked_at ? "未探测" : (ep.ok ? "正常" : "异常");
      return `<tr class="${ep.down ? "row-down" : ""}">
        <td><div class="api-ep-name">${esc(ep.name)}${ep.protocol && ep.protocol !== "http" ? ` <span class="tag" style="font-size:10px">${ep.protocol === "graphql" ? "GraphQL" : "WS"}</span>` : ""}</div><div class="api-ep-url">${esc(ep.protocol === "websocket" ? "WS" : (ep.method || "GET"))} ${esc(ep.url)}</div></td>
        <td>${dot}${statusText}${ep.status_code ? ` <span class="muted">${ep.status_code}</span>` : ""}</td>
        <td>${apiFmtMs(ep.latency_ms)}</td>
        <td>${apiFmtMs(ep.avg_ms)}</td>
        <td>${apiFmtMs(ep.p95_ms)}</td>
        <td>${apiFmtMs(ep.p99_ms)}</td>
        <td class="${apimonAvailClass(ep.avail_1h)}">${apiFmtPct(ep.avail_1h)}</td>
        <td class="${apimonAvailClass(ep.avail_24h)}">${apiFmtPct(ep.avail_24h)}</td>
        <td>${ep.samples_1h > 0 ? ep.samples_1h.toFixed(0) : "—"}</td>
        <td><button class="mini-btn" data-aact="hist" data-ep="${esc(ep.id)}" data-name="${esc(ep.name)}" title="接口性能历史"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3v18h18"/><path d="M7 13l3-3 3 2 5-6"/></svg></button></td>
      </tr>`;
    }).join("") : `<tr><td colspan="10" class="muted">该业务系统暂无接口，点「编辑」添加。</td></tr>`;
    return `<div class="api-sys-card" data-sys="${esc(sys.id)}">
      <div class="api-sys-head">
        <div class="api-sys-title">${esc(sys.name)} ${sys.env ? `<span class="tag env-${esc(sys.env)}">${esc(envLabel(sys.env))}</span>` : ""}${sys.maint_until && sys.maint_until * 1000 > Date.now() ? '<span class="tag warn">🔧 维护中</span>' : ""}<span class="tag">${eps.length} 接口 · 每 ${sys.interval_sec}s</span>${downCount ? `<span class="tag crit">${downCount} 异常</span>` : ""}${!sys.enabled ? '<span class="tag">已停用</span>' : ""}</div>
        <div class="api-sys-actions">
          ${(sys.host_ids && sys.host_ids.length) ? `<button class="mini-btn" data-aact="hosts" data-sys="${esc(sys.id)}" data-name="${esc(sys.name)}" title="承载主机"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="7" rx="1"/><rect x="3" y="13" width="18" height="7" rx="1"/><path d="M7 7.5h.01M7 16.5h.01"/></svg></button>` : ""}
          <button class="mini-btn" data-aact="run" data-sys="${esc(sys.id)}" title="立即探测">▶</button>
          <button class="mini-btn" data-aact="maint" data-sys="${esc(sys.id)}" title="维护窗口"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14.7 6.3a4 4 0 0 0-5.4 5.4L3 18v3h3l6.3-6.3a4 4 0 0 0 5.4-5.4l-2.8 2.8-2-2 2.8-2.8z"/></svg></button>
          <button class="mini-btn" data-aact="edit" data-sys="${esc(sys.id)}" title="编辑">✎</button>
          <button class="mini-btn del" data-aact="del" data-sys="${esc(sys.id)}" title="删除">✕</button>
        </div>
      </div>
      <div class="api-table-wrap"><table class="api-table">
        <thead><tr><th>接口</th><th>最新状态</th><th>本次</th><th>平均(1h)</th><th>P95(1h)</th><th>P99(1h)</th><th>可用率(1h)</th><th>可用率(24h)</th><th>吞吐(次/时)</th><th></th></tr></thead>
        <tbody>${rows}</tbody>
      </table></div>
    </div>`;
  }).join("");
}

async function openAPISystemModal(sys) {
  $("apiSysId").value = sys ? sys.id : "";
  $("apiSysName").value = sys ? sys.name : "";
  $("apiSysInterval").value = sys ? sys.interval_sec : 60;
  $("apiSysLevel").value = sys ? sys.level : "critical";
  $("apiSysEnv").value = sys ? (sys.env || "") : "";
  $("apiSysEnabled").checked = sys ? sys.enabled : true;
  $("apiSysModalTitle").textContent = sys ? "编辑业务系统" : "添加业务系统";
  // 回填公共请求头
  const commonHeadersText = (sys && sys.common_headers)
    ? Object.entries(sys.common_headers).map(([k, v]) => `${k}: ${v}`).join("\n")
    : "";
  $("apiSysCommonHeaders").value = commonHeadersText;
  // 回填公共请求体（必须回显，否则编辑保存会把公共体清零）
  $("apiSysCommonBody").value = (sys && sys.common_body) || "";
  // 承载主机：分组树多选（主机名 + IP）
  try {
    if (typeof loadHostFolders === "function") { try { await loadHostFolders(); } catch (_) {} }
    if (!window._apimonHosts || !window._apimonHosts.length) {
      window._apimonHosts = (await fetch(`${API}/hosts`).then(r => r.json())) || [];
    }
  } catch (_) { window._apimonHosts = window._apimonHosts || []; }
  const selHosts = new Set((sys && sys.host_ids) || []);
  paintApiSysHostPicker(selHosts);
  const rows = $("apiEndpointRows");
  rows.innerHTML = "";
  const eps = (sys && sys.endpoints) || [];
  if (eps.length) eps.forEach(ep => addAPIEndpointRow(ep));
  else addAPIEndpointRow(null);
  $("apimonMask").classList.add("show");
}

// 追加一个接口编辑行（新增/编辑通用）。
function addAPIEndpointRow(ep) {
  const rows = $("apiEndpointRows");
  const div = document.createElement("div");
  div.className = "api-ep-row";
  const m = (ep && ep.method) || "GET";
  const methods = ["GET", "POST", "PUT", "DELETE", "HEAD", "PATCH"].map(x => `<option ${x === m ? "selected" : ""}>${x}</option>`).join("");
  const proto = (ep && ep.protocol) || "http";
  const protoOpts = [["http", "HTTP"], ["graphql", "GraphQL"], ["websocket", "WebSocket (ws/wss)"]].map(([v, l]) => `<option value="${v}" ${v === proto ? "selected" : ""}>${l}</option>`).join("");
  const headersText = (ep && ep.headers) ? Object.entries(ep.headers).map(([k, v]) => `${k}: ${v}`).join("\n") : "";
  const commonHeaders = ($("apiSysCommonHeaders")?.value || "").trim();
  div.innerHTML = `
    <button class="api-ep-del" data-aact="ep-del" title="移除接口">✕</button>
    <div style="display:flex; gap:8px; align-items:center; margin-bottom:8px; padding-right:26px">
      <select class="ep-protocol sel" style="font-size:12px; max-width:190px">${protoOpts}</select>
      <span class="hint" style="font-size:11px; margin:0">GraphQL：Body 填查询，自动包 {"query":…} 并校验 errors；WebSocket：填 ws/wss，Body 非空则发一帧读一帧（可配关键字断言）</span>
    </div>
    <div class="api-ep-grid">
      <input class="ep-name" placeholder="接口名称，如 登录" value="${esc((ep && ep.name) || "")}">
      <select class="ep-method sel">${methods}</select>
      <input class="ep-url" placeholder="https://api.example.com/v1/login" value="${esc((ep && ep.url) || "")}">
    </div>
    <div class="api-ep-grid2">
      <input class="ep-status" type="number" placeholder="期望状态码(默认<400)" value="${ep && ep.expect_status ? ep.expect_status : ""}">
      <input class="ep-keyword" placeholder="响应含关键字" value="${esc((ep && ep.expect_keyword) || "")}">
      <input class="ep-jsonpath" placeholder="JSON路径 如 code" value="${esc((ep && ep.json_path) || "")}">
      <input class="ep-jsonexpect" placeholder="JSON期望值 如 0" value="${esc((ep && ep.json_expect) || "")}">
    </div>
    <details class="api-ep-advanced">
      <summary>高级选项（请求头 / 请求体 / 超时 / 重试）${commonHeaders ? `<span class="hint">· 已继承系统级公共请求头</span>` : ""}</summary>
      <div class="api-ep-grid3">
        <textarea class="ep-headers" rows="2" placeholder="接口级请求头（覆盖同名的公共头），每行 Key: Value，如 Authorization: Bearer xxx">${esc(headersText)}</textarea>
        <textarea class="ep-body" rows="2" placeholder="请求体(可选，POST/PUT)">${esc((ep && ep.body) || "")}</textarea>
      </div>
      <div style="display:grid; grid-template-columns:1fr 1fr; gap:8px; margin-top:8px">
        <input class="ep-timeout" type="number" min="1" max="60" placeholder="超时(秒，默认10，慢接口调大)" value="${ep && ep.timeout_sec ? ep.timeout_sec : ""}">
        <input class="ep-retries" type="number" min="0" max="3" placeholder="失败重试次数(默认0，抑制瞬时抖动)" value="${ep && ep.retries ? ep.retries : ""}">
      </div>
      <label class="switch" style="margin-top:10px; font-size:12px"><input type="checkbox" class="ep-distributed" ${ep && ep.distributed ? "checked" : ""}> <span>分布式多点探测（由各地 agent 作为探针执行，聚合区分区域性 / 全局故障）</span></label>
    </details>`;
  rows.appendChild(div);
}

async function saveAPISystem() {
  const endpoints = [...document.querySelectorAll("#apiEndpointRows .api-ep-row")].map(row => {
    const g = s => row.querySelector(s);
    const headers = {};
    (g(".ep-headers").value || "").split("\n").forEach(line => { const i = line.indexOf(":"); if (i > 0) { const k = line.slice(0, i).trim(); if (k) headers[k] = line.slice(i + 1).trim(); } });
    return {
      name: g(".ep-name").value.trim(),
      url: g(".ep-url").value.trim(),
      method: g(".ep-method").value,
      protocol: g(".ep-protocol").value,
      expect_status: parseInt(g(".ep-status").value) || 0,
      expect_keyword: g(".ep-keyword").value.trim(),
      json_path: g(".ep-jsonpath").value.trim(),
      json_expect: g(".ep-jsonexpect").value.trim(),
      headers: headers,
      body: g(".ep-body").value,
      timeout_sec: parseInt(g(".ep-timeout").value) || 0,
      retries: parseInt(g(".ep-retries").value) || 0,
      distributed: g(".ep-distributed").checked,
      enabled: true
    };
  }).filter(ep => ep.name && ep.url);
  // 读取业务系统级公共请求头
  const commonHeaders = {};
  ($("apiSysCommonHeaders").value || "").split("\n").forEach(line => {
    const i = line.indexOf(":");
    if (i > 0) { const k = line.slice(0, i).trim(); if (k) commonHeaders[k] = line.slice(i + 1).trim(); }
  });
  const body = {
    id: $("apiSysId").value,
    name: $("apiSysName").value.trim(),
    interval_sec: Math.max(5, parseInt($("apiSysInterval").value) || 60),
    level: $("apiSysLevel").value,
    env: $("apiSysEnv").value,
    enabled: $("apiSysEnabled").checked,
    common_headers: commonHeaders,
    common_body: ($("apiSysCommonBody").value || "").trim(),
    host_ids: (function () {
      apiCaptureHostPick();
      if (APIMON_PICK_SELECTED.size) return [...APIMON_PICK_SELECTED];
      return [...document.querySelectorAll("#apiSysHosts .ep-host:checked")].map(c => c.value);
    })(),
    endpoints: endpoints
  };
  if (!body.name) { toast("请填写业务系统名称", "err"); return; }
  if (!endpoints.length) { toast("请至少添加一个接口（需填接口名称与 URL）", "err"); return; }
  await withLoading("apiSysSaveBtn", async () => {
    try {
      const r = await fetch(`${API}/apimon/systems`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (r.ok) { toast("已保存", "ok"); $("apimonMask").classList.remove("show"); loadAPIMon(); }
      else { const j = await r.json().catch(() => ({})); toast("保存失败：" + (j.error || ""), "err"); }
    } catch (e) { toast("保存失败：" + e, "err"); }
  });
}

async function delAPISystem(id) {
  const sys = (LAST_APIMON.systems || []).find(s => s.id === id);
  if (!confirm(`确认删除业务系统「${sys ? sys.name : id}」及其全部接口监控？历史数据保留在 VM 中。`)) return;
  try { await fetch(`${API}/apimon/systems/${encodeURIComponent(id)}`, { method: "DELETE" }); toast("已删除", "ok"); loadAPIMon(); }
  catch (e) { toast("删除失败：" + e, "err"); }
}

function runAPISystem(id) {
  fetch(`${API}/apimon/systems/${encodeURIComponent(id)}/run`, { method: "POST" })
    .then(() => { toast("已触发探测", "ok"); setTimeout(loadAPIMon, 1800); })
    .catch(e => toast("触发失败：" + e, "err"));
}

// 维护窗口：静音该业务系统告警 N 分钟（0=解除）。窗口内探测继续、仅抑制告警推送。
async function maintAPISystem(id) {
  const sys = (LAST_APIMON.systems || []).find(s => s.id === id);
  const active = sys && sys.maint_until && sys.maint_until * 1000 > Date.now();
  const inp = await requestAITextInput({
    title:"设置维护窗口",
    message:"窗口内探测继续运行，仅抑制告警推送；输入 0 可解除维护。",
    label:"静音分钟数",defaultValue:active?"0":"60",submitLabel:"应用维护窗口",
    inputType:"number",singleLine:true,min:0,max:525600,step:1,maxLength:8,danger:false,
    requiredMessage:"请输入 0 或正整数分钟数"
  });
  if (inp === null) return;
  const minutes = Number(inp);
  if(!Number.isInteger(minutes)||minutes<0||minutes>525600){ toast("维护时长须为 0–525600 的整数","err"); return; }
  try{
    const r=await fetch(`${API}/apimon/systems/${encodeURIComponent(id)}/maintenance`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ minutes }) });
    const j=await r.json().catch(()=>({}));
    if(!r.ok) throw new Error(j.error||`HTTP ${r.status}`);
    toast(minutes > 0 ? `已进入维护 ${minutes} 分钟` : "已解除维护", "ok");
    loadAPIMon();
  }catch(e){ toast("操作失败：" + e, "err"); }
}

// 承载主机下钻：展示该业务系统关联主机的实时 CPU/内存/磁盘/网络水位，把接口异常与主机资源关联。
async function openSystemHosts(id, name) {
  $("apiHostsTitle").textContent = (name || "") + " · 承载主机实时指标";
  $("apiHostsMask").classList.add("show");
  const body = $("apiHostsBody");
  body.innerHTML = `<div class="empty-line">加载中…</div>`;
  try {
    const d = await fetch(`${API}/apimon/systems/${encodeURIComponent(id)}/hosts`).then(r => r.json());
    const hosts = (d && d.hosts) || [];
    if (!hosts.length) { body.innerHTML = `<div class="empty-line">该业务系统未关联承载主机，或关联主机已不在纳管列表中（在「编辑」里勾选主机）。</div>`; return; }
    const metric = (label, v, unit, warn, crit) => {
      const cls = v >= crit ? "crit" : (v >= warn ? "warn" : "ok");
      return `<div class="hd-metric"><div class="hd-m-top"><span>${label}</span><b class="${cls}">${(v || 0).toFixed(0)}${unit}</b></div><div class="hd-bar"><div class="hd-bar-fill ${cls}" style="width:${Math.min(100, v || 0)}%"></div></div></div>`;
    };
    body.innerHTML = `<div class="hint" style="margin-bottom:10px">接口异常时对照这些主机的资源水位，快速判断是否为承载主机的 CPU / 内存 / 磁盘 / 网络问题。</div>
      <div class="host-drill">${hosts.map(h => {
      const off = !h.online;
      return `<div class="host-drill-card ${off ? "off" : ""}">
          <div class="hd-head"><span class="sdot ${off ? "crit" : "ok"}"></span><b>${esc(h.hostname || h.id)}</b> <span class="muted">${esc(h.ip || "")}</span>${off ? '<span class="tag crit">离线</span>' : ""}</div>
          ${metric("CPU", h.cpu, "%", 80, 90)}
          ${metric("内存", h.mem, "%", 85, 95)}
          ${metric("磁盘", h.disk, "%", 85, 95)}
          <div class="hd-net"><span>Load ${(h.load1 || 0).toFixed(2)}</span><span>↓ ${apiFmtRate(h.net_recv || 0)}</span><span>↑ ${apiFmtRate(h.net_sent || 0)}</span></div>
        </div>`;
    }).join("")}</div>`;
  } catch (e) { body.innerHTML = `<div class="empty-line">加载失败：${esc(e)}</div>`; }
}
function apiFmtRate(bps) {
  if (bps < 1024) return (bps || 0).toFixed(0) + " B/s";
  if (bps < 1048576) return (bps / 1024).toFixed(1) + " KB/s";
  return (bps / 1048576).toFixed(1) + " MB/s";
}

// 接口性能历史：专用「组合曲线」弹窗——一屏三图：①响应时间分解(总/DNS/TCP/TLS/TTFB 同轴组合)
// ②可用性 ③响应体量。数据从 VM 全量回读（重启不丢），复用交互式图表引擎(悬停/框选放大/双击还原)。
let API_HIST = { id: "", name: "", range: 24 };
let API_HIST_CHARTS = {};

function openAPIHistory(id, name) {
  API_HIST = { id, name, range: 24, custom: null };
  $("apiHistTitle").textContent = name + " · 接口性能历史";
  $("apiHistMask").classList.add("show");
  loadAPIHistory();
}

function apiHistP95(vals) {
  if (!vals.length) return 0;
  const a = vals.slice().sort((x, y) => x - y);
  return a[Math.min(a.length - 1, Math.floor(a.length * 0.95))] || 0;
}

// 延迟分布直方图：把样本的总延时按语义化区间分桶，客户端渲染 CSS 柱状分布——
// 相比 P95/P99 点值，直方图能直接暴露长尾与双峰（点值看不出的形态）。
function apiHistHistogram(pts) {
  const lat = pts.map(p => p.latency_ms || 0).filter(v => v >= 0);
  if (!lat.length) return "";
  const edges = [0, 50, 100, 200, 300, 500, 1000, 2000, Infinity];
  const labels = ["0–50", "50–100", "100–200", "200–300", "300–500", "500ms–1s", "1–2s", "≥2s"];
  const counts = new Array(labels.length).fill(0);
  lat.forEach(v => {
    for (let i = 0; i < edges.length - 1; i++) {
      if (v >= edges[i] && v < edges[i + 1]) { counts[i]++; break; }
    }
  });
  const max = Math.max.apply(null, counts.concat([1]));
  const total = lat.length;
  const rows = counts.map((c, i) => {
    const pct = c / total * 100;
    const w = c / max * 100;
    const cls = i >= 6 ? "crit" : (i >= 5 ? "warn" : "ok"); // ≥500ms 转暖，≥1s 警示
    return `<div class="lat-hist-row"><span class="lat-hist-label">${labels[i]}</span>` +
      `<span class="lat-hist-bar-wrap"><span class="lat-hist-bar ${cls}" style="width:${w.toFixed(1)}%"></span></span>` +
      `<span class="lat-hist-val">${c} · ${pct.toFixed(1)}%</span></div>`;
  }).join("");
  return `<div class="chart-wrap"><div class="chart-sub-title">延迟分布直方图（总延时分桶 · 看长尾 / 双峰）</div><div class="lat-hist">${rows}</div></div>`;
}

async function loadAPIHistory() {
  const { id, name, range, custom } = API_HIST;
  const body = $("apiHistBody");
  body.innerHTML = `<div class="empty-line">${I18N.t("ui.loading", "加载中…")}</div>`;
  const now = Math.floor(Date.now() / 1000);
  const anchorKey = "apimon:" + id;
  const win = (typeof resolveAnchoredRange === "function")
    ? resolveAnchoredRange(anchorKey, range > 0 ? range : 1, custom)
    : { from: custom ? custom.from : (range > 0 ? now - range * 3600 : now - 3600), to: custom ? custom.to : now };
  const from = win.from, to = win.to;
  const ctrl = `${renderChartControls(custom ? -1 : range, "arange")}
    <button class="chip-btn ${custom ? "active" : ""}" data-ahist-custom-toggle title="${I18N.t("time.custom_range", "自定义时间范围")}">${I18N.t("time.custom", "自定义")}</button>
    ${typeof forecastChipHTML === "function" ? forecastChipHTML("apimon") : ""}
    <span class="chart-custom-range" id="ahistCustomPanel"${custom ? "" : " hidden"}>
      <input type="datetime-local" id="ahistCustomFrom" class="dt-input" value="${toLocalDatetimeValue(from > 0 ? from : now - 3600)}">
      <span class="dt-sep">→</span>
      <input type="datetime-local" id="ahistCustomTo" class="dt-input" value="${toLocalDatetimeValue(to)}">
      <button class="chip-btn primary" data-ahist-custom-apply>${I18N.t("time.custom_apply", "应用")}</button>
    </span>`;
  const load = (typeof beginRangeLoad === "function")
    ? beginRangeLoad(anchorKey)
    : { signal: undefined, isCurrent: () => true };
  try {
    const sinceMin = Math.max(1, Math.ceil((to - from) / 60));
    const r = await fetch(`${API}/apimon/endpoints/${encodeURIComponent(id)}/history?since_min=${sinceMin}&from=${from}&to=${to}`,
      load.signal ? { signal: load.signal } : undefined);
    if (!load.isCurrent()) return;
    const all = await r.json().catch(() => []);
    if (!load.isCurrent()) return;
    const pts = (Array.isArray(all) ? all : []).filter(p => p.timestamp >= from && p.timestamp <= to);
    if (!pts.length) {
      body.innerHTML = `<div class="chart-controls">${ctrl}</div><div class="empty-line">该时间范围暂无数据（接口探测运行一段时间后自动积累，数据入 VM）。</div>`;
      return;
    }
    const samples = pts.map(p => ({
      timestamp: p.timestamp,
      latency_ms: p.latency_ms,
      dns_ms: p.dns_ms, tcp_ms: p.tcp_ms, tls_ms: p.tls_ms, ttfb_ms: p.ttfb_ms,
      online: p.ok ? 100 : 0,
      resp_kb: (p.resp_bytes || 0) / 1024,
    }));
    const aligned = typeof alignJoinedSeriesSamples === "function"
      ? alignJoinedSeriesSamples(samples, ["latency_ms", "dns_ms", "tcp_ms", "tls_ms", "ttfb_ms", "online", "resp_kb"])
      : samples;
    const uptime = (pts.filter(p => p.ok).length / pts.length * 100).toFixed(2);
    const avgLat = (pts.reduce((s, p) => s + (p.latency_ms || 0), 0) / pts.length).toFixed(0);
    const p95 = apiHistP95(pts.map(p => p.latency_ms || 0)).toFixed(0);
    const span = pts.length > 1 ? fmtDur(pts[pts.length - 1].timestamp - pts[0].timestamp) : I18N.t("time.just_now", "刚刚");
    const wrap = (cid, title) => `<div class="chart-wrap"><div class="chart-sub-title">${title}</div><canvas id="${cid}" width="1000" height="220"></canvas>` +
      `<button class="chart-enlarge" data-chart="${cid}" title="${I18N.t("ui.zoom_preview", "放大")}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg></button></div>`;
    body.innerHTML = `<div class="chart-controls">${ctrl}</div>
      <div class="api-hist-stat">
        <span class="ahs"><b class="${apimonAvailClass(parseFloat(uptime))}">${uptime}%</b><i>可用率</i></span>
        <span class="ahs"><b>${apiFmtMs(parseFloat(avgLat))}</b><i>平均延时</i></span>
        <span class="ahs"><b>${apiFmtMs(parseFloat(p95))}</b><i>P95 延时</i></span>
        <span class="ahs"><b>${pts.length}</b><i>采样 · 跨度 ${span}</i></span>
      </div>
      <div class="chart-container">
        ${wrap("apiHLat", "响应时间分解 · 总 / DNS / TCP / TLS / TTFB（ms）")}
        ${wrap("apiHAvail", "可用性 · 在线=100 / 离线=0")}
        ${wrap("apiHBytes", "响应体大小（KB）")}
      </div>
      ${apiHistHistogram(pts)}
      <div class="hint">数据从 VictoriaMetrics 回读，重启不丢；曲线可悬停查看数值、拖动框选放大、双击还原。延迟分布直方图按样本总延时分桶，尾部偏暖色，便于识别长尾与双峰。</div>`;
    const specs = [
      { id: "apiHLat", samples: aligned, series: [
        { key: "latency_ms", label: "总延时", color: "#4c8dff", fmt: v => v.toFixed(0) + " ms" },
        { key: "dns_ms", label: "DNS", color: "#22c55e", fmt: v => v.toFixed(1) + " ms" },
        { key: "tcp_ms", label: "TCP", color: "#eab308", fmt: v => v.toFixed(1) + " ms" },
        { key: "tls_ms", label: "TLS", color: "#a855f7", fmt: v => v.toFixed(1) + " ms" },
        { key: "ttfb_ms", label: "TTFB", color: "#f97316", fmt: v => v.toFixed(1) + " ms" },
      ], yMin: 0, yMax: null, opts: { title: "", legendMode: "dash", cssH: 220 } },
      { id: "apiHAvail", samples: aligned, series: [
        { key: "online", label: "在线", color: "#22c55e", fmt: v => (v >= 50 ? "在线" : "离线") },
      ], yMin: 0, yMax: 100, opts: { title: "", legendMode: "dash", cssH: 220 } },
      { id: "apiHBytes", samples: aligned, series: [
        { key: "resp_kb", label: "响应体", color: "#06b6d4", fmt: v => v.toFixed(1) + " KB" },
      ], yMin: 0, yMax: null, opts: { title: "", legendMode: "dash", cssH: 220 } },
    ];
    if (!load.isCurrent()) return;
    API_HIST_CHARTS = typeof mountChartsWithForecast === "function"
      ? await mountChartsWithForecast("apimon", specs, load)
      : Object.fromEntries(specs.map(sp => [sp.id, createChart(sp.id, sp.samples, sp.series, sp.yMin, sp.yMax, sp.opts)]));
  } catch (e) {
    if (e && (e.name === "AbortError" || /aborted/i.test(String(e.message || e)))) return;
    if (!load.isCurrent()) return;
    body.innerHTML = `<div class="empty-line">加载失败: ${esc(e)}</div>`;
  }
}
document.addEventListener("chart-forecast-toggle", (ev) => {
  if (ev.detail && ev.detail.scope === "apimon" && API_HIST && API_HIST.id) loadAPIHistory();
});

/* ---------- 事件绑定 ---------- */
safeAddEventListener("apimonAddBtn", "click", () => openAPISystemModal(null));
safeAddEventListener("apimonEnvFilter", "change", e => { window.APIMON_ENV_FILTER = e.target.value; renderAPIMon(LAST_APIMON); });
// OpenAPI / Swagger 一键导入（支持 Knife4j doc.html 自动拉取）
let API_IMPORT_GROUPS = [];
safeAddEventListener("apimonImportBtn", "click", () => {
  $("apiImportName").value = "";
  $("apiImportBase").value = "";
  $("apiImportSpec").value = "";
  if ($("apiImportDocURL")) $("apiImportDocURL").value = "";
  if ($("apiImportCommonHeaders")) $("apiImportCommonHeaders").value = "";
  if ($("apiImportCommonBody")) $("apiImportCommonBody").value = "";
  const adv = document.querySelector("#apiImportMask .api-import-advanced");
  if (adv) adv.open = false;
  API_IMPORT_GROUPS = [];
  renderAPIImportGroups([]);
  resetAPIImportMethods(["get"]);
  const notes = $("apiImportFetchNotes");
  if (notes) { notes.style.display = "none"; notes.textContent = ""; }
  $("apiImportMask").classList.add("show");
});
function parseHeaderLines(text) {
  const headers = {};
  String(text || "").split("\n").forEach(line => {
    const i = line.indexOf(":");
    if (i > 0) {
      const k = line.slice(0, i).trim();
      if (k) headers[k] = line.slice(i + 1).trim();
    }
  });
  return headers;
}
function resetAPIImportMethods(selected) {
  const wrap = $("apiImportMethodFilter");
  if (!wrap) return;
  const set = new Set((selected || []).map(s => String(s).toLowerCase()));
  const wantAll = set.has("all") || set.size === 0;
  wrap.querySelectorAll("[data-api-method]").forEach(btn => {
    const m = (btn.dataset.apiMethod || "").toLowerCase();
    btn.classList.toggle("active", wantAll ? m === "all" : set.has(m));
  });
  if (!wantAll && !wrap.querySelector(".chip-btn.active")) {
    const getBtn = wrap.querySelector('[data-api-method="get"]');
    if (getBtn) getBtn.classList.add("active");
  }
}
function selectedAPIImportMethods() {
  const wrap = $("apiImportMethodFilter");
  if (!wrap) return ["get"];
  if (wrap.querySelector('[data-api-method="all"].active')) return ["all"];
  const ms = [...wrap.querySelectorAll("[data-api-method].active")]
    .map(b => (b.dataset.apiMethod || "").toLowerCase())
    .filter(m => m && m !== "all");
  return ms.length ? ms : ["get"];
}
safeAddEventListener("apiImportMethodFilter", "click", e => {
  const btn = e.target.closest("[data-api-method]");
  if (!btn) return;
  const wrap = $("apiImportMethodFilter");
  if (!wrap) return;
  const m = (btn.dataset.apiMethod || "").toLowerCase();
  if (m === "all") {
    wrap.querySelectorAll("[data-api-method]").forEach(b => b.classList.toggle("active", b === btn));
    return;
  }
  const allBtn = wrap.querySelector('[data-api-method="all"]');
  if (allBtn) allBtn.classList.remove("active");
  btn.classList.toggle("active");
  if (!wrap.querySelector('[data-api-method]:not([data-api-method="all"]).active')) {
    const getBtn = wrap.querySelector('[data-api-method="get"]');
    if (getBtn) getBtn.classList.add("active");
  }
});
function renderAPIImportGroups(groups, selected) {
  API_IMPORT_GROUPS = Array.isArray(groups) ? groups : [];
  const wrap = $("apiImportGroupWrap");
  const sel = $("apiImportGroup");
  if (!wrap || !sel) return;
  if (!API_IMPORT_GROUPS.length) {
    wrap.style.display = "none";
    sel.innerHTML = "";
    return;
  }
  wrap.style.display = "";
  const pick = (selected || "").trim();
  sel.innerHTML = API_IMPORT_GROUPS.map((g, i) => {
    const name = g.name || "";
    const on = pick ? name === pick : i === 0;
    return `<option value="${esc(name)}" data-url="${esc(g.url || "")}" ${on ? "selected" : ""}>${esc(name || g.url || ("分组" + (i + 1)))}</option>`;
  }).join("");
}
async function fetchAPIImportSpec() {
  const url = (($("apiImportDocURL") && $("apiImportDocURL").value) || "").trim();
  if (!url) { toast("请填写文档地址（如 http://host:8080/doc.html#/home）", "err"); return; }
  const group = (($("apiImportGroup") && $("apiImportGroup").value) || "").trim();
  await withLoading("apiImportFetchBtn", async () => {
    try {
      // Do NOT send current base_url: it would freeze the previous group's base when switching groups.
      const r = await fetch(`${API}/apimon/fetch-openapi`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ url, group })
      });
      const j = await r.json().catch(() => ({}));
      if (Array.isArray(j.groups) && j.groups.length) {
        renderAPIImportGroups(j.groups, j.selected_group || group);
      }
      if (!r.ok) {
        toast("拉取失败：" + (j.error || r.status), "err");
        return;
      }
      if (j.spec) $("apiImportSpec").value = typeof j.spec === "string" ? j.spec : JSON.stringify(j.spec, null, 2);
      // Always sync name/base with the selected group (user may still edit before import).
      if ($("apiImportName")) {
        $("apiImportName").value = (j.suggested_name || j.selected_group || "").trim();
      }
      if ($("apiImportBase")) {
        $("apiImportBase").value = (j.suggested_base || "").trim();
      }
      if (j.selected_group && $("apiImportGroup")) {
        $("apiImportGroup").value = j.selected_group;
      }
      const notes = $("apiImportFetchNotes");
      if (notes) {
        const parts = [];
        if (j.source_url) parts.push("来源 " + j.source_url);
        if (Array.isArray(j.notes) && j.notes.length) parts.push(j.notes.join(" · "));
        notes.textContent = parts.join(" · ") || "已拉取规范";
        notes.style.display = "";
      }
      toast("已拉取 OpenAPI 规范，确认后点击导入", "ok");
    } catch (e) { toast("拉取失败：" + e, "err"); }
  });
}
safeAddEventListener("apiImportFetchBtn", "click", fetchAPIImportSpec);
safeAddEventListener("apiImportGroup", "change", () => {
  // Re-fetch selected group when user switches.
  if (($("apiImportDocURL") && $("apiImportDocURL").value || "").trim()) fetchAPIImportSpec();
});
safeAddEventListener("apiImportDoBtn", "click", async () => {
  const name = $("apiImportName").value.trim();
  const spec = $("apiImportSpec").value.trim();
  const docURL = (($("apiImportDocURL") && $("apiImportDocURL").value) || "").trim();
  if (!name && !docURL) { toast("请填写业务系统名称，或先拉取文档自动填充", "err"); return; }
  if (!spec && !docURL) { toast("请粘贴 OpenAPI JSON，或填写文档地址并拉取", "err"); return; }
  await withLoading("apiImportDoBtn", async () => {
    try {
      const body = {
        system_name: name || undefined,
        base_url: $("apiImportBase").value.trim(),
        spec: spec || undefined,
        spec_url: !spec && docURL ? docURL : undefined,
        group: (($("apiImportGroup") && $("apiImportGroup").value) || "").trim() || undefined,
        methods: selectedAPIImportMethods(),
        common_headers: parseHeaderLines($("apiImportCommonHeaders") && $("apiImportCommonHeaders").value),
        common_body: (($("apiImportCommonBody") && $("apiImportCommonBody").value) || "").trim() || undefined
      };
      const r = await fetch(`${API}/apimon/import-openapi`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
      });
      const j = await r.json().catch(() => ({}));
      if (r.ok) { toast(`已导入 ${j.count} 个接口`, "ok"); $("apiImportMask").classList.remove("show"); loadAPIMon(); }
      else {
        if (Array.isArray(j.groups) && j.groups.length) renderAPIImportGroups(j.groups);
        toast("导入失败：" + (j.error || ""), "err");
      }
    } catch (e) { toast("导入失败：" + e, "err"); }
  });
});

/* ========== SLA 报表（近 N 天可用率，可导出 CSV） ========== */
function fmtDowntime(min) {
  if (min < 1) return "< 1 分钟";
  if (min < 60) return min.toFixed(0) + " 分钟";
  if (min < 1440) return (min / 60).toFixed(1) + " 小时";
  return (min / 1440).toFixed(1) + " 天";
}
async function openSlaReport() {
  $("apiSlaMask").classList.add("show");
  const body = $("apiSlaBody");
  body.innerHTML = `<div class="empty-line">生成中…</div>`;
  try {
    const d = await fetch(`${API}/apimon/sla?days=30`).then(r => r.json());
    const rows = (d && d.rows) || [];
    window._slaRows = rows;
    if (!rows.length) {
      body.innerHTML = `<div class="empty-line">暂无数据（接口探测运行一段时间、数据入 VM 后才有 SLA 报表；需启用 VictoriaMetrics）。</div>`;
      return;
    }
    body.innerHTML = `<div class="hint" style="margin-bottom:10px">近 ${d.days} 天 · 共 ${rows.length} 个接口 · 可用率 = 成功探测 / 总探测；估算停机 = 不可用比例 × 窗口时长</div>
      <div class="api-table-wrap"><table class="api-table">
        <thead><tr><th>业务系统</th><th>接口</th><th>可用率</th><th>估算停机</th><th>P95</th><th>P99</th><th>探测数</th></tr></thead>
        <tbody>${rows.map(r => `<tr>
          <td>${esc(r.system)}</td>
          <td><div class="api-ep-name">${esc(r.endpoint)}</div><div class="api-ep-url">${esc(r.url)}</div></td>
          <td class="${apimonAvailClass(r.availability)}">${(r.availability || 0).toFixed(3)}%</td>
          <td>${fmtDowntime(r.downtime_min || 0)}</td>
          <td>${apiFmtMs(r.p95_ms)}</td>
          <td>${apiFmtMs(r.p99_ms)}</td>
          <td>${(r.samples || 0).toFixed(0)}</td>
        </tr>`).join("")}</tbody>
      </table></div>`;
  } catch (e) { body.innerHTML = `<div class="empty-line">生成失败：${esc(e)}</div>`; }
}
function exportSlaCsv() {
  const rows = window._slaRows || [];
  if (!rows.length) { toast("暂无数据可导出", "err"); return; }
  const head = ["业务系统", "接口", "URL", "可用率(%)", "估算停机(分钟)", "P95(ms)", "P99(ms)", "探测数"];
  const csvCell = x => `"${String(x).replace(/"/g, '""')}"`;
  const lines = [head.map(csvCell).join(",")].concat(rows.map(r => [
    r.system, r.endpoint, r.url, (r.availability || 0).toFixed(3), (r.downtime_min || 0).toFixed(0),
    (r.p95_ms || 0).toFixed(0), (r.p99_ms || 0).toFixed(0), (r.samples || 0).toFixed(0)
  ].map(csvCell).join(",")));
  const bom = String.fromCharCode(0xFEFF); // UTF-8 BOM，保证 Excel 正确识别中文
  const blob = new Blob([bom + lines.join("\r\n")], { type: "text/csv;charset=utf-8" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob); a.download = "sla-report.csv"; a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}
safeAddEventListener("apimonSlaBtn", "click", openSlaReport);
safeAddEventListener("apiSlaExportBtn", "click", exportSlaCsv);
safeAddEventListener("apiSysSaveBtn", "click", saveAPISystem);
safeAddEventListener("apiAddEndpointBtn", "click", () => addAPIEndpointRow(null));
safeAddEventListener("apiEndpointRows", "click", e => {
  const del = e.target.closest('[data-aact="ep-del"]'); if (!del) return;
  const rows = $("apiEndpointRows");
  if (rows.querySelectorAll(".api-ep-row").length <= 1) { toast("至少保留一个接口", "err"); return; }
  del.closest(".api-ep-row").remove();
});
safeAddEventListener("apimonSystems", "click", e => {
  const btn = e.target.closest("[data-aact]"); if (!btn) return;
  const act = btn.dataset.aact;
  if (act === "hist") { openAPIHistory(btn.dataset.ep, btn.dataset.name); return; }
  if (act === "hosts") { openSystemHosts(btn.dataset.sys, btn.dataset.name); return; }
  const id = btn.dataset.sys; if (!id) return;
  if (act === "run") runAPISystem(id);
  else if (act === "maint") maintAPISystem(id);
  else if (act === "edit") { const sys = (LAST_APIMON.systems || []).find(s => s.id === id); openAPISystemModal(sys); }
  else if (act === "del") delAPISystem(id);
});
// 接口历史弹窗：时间范围切换（快捷跨度）+ 图表放大委托
safeAddEventListener("apiHistBody", "click", e => {
  const tog = e.target.closest("[data-ahist-custom-toggle]");
  if (tog) { const p = $("ahistCustomPanel"); if (p) p.hidden = !p.hidden; return; }
  if (e.target.closest("[data-ahist-custom-apply]")) { applyAhistCustomRange(); return; }
  const rb = e.target.closest(".chip-btn[data-arange]");
  if (rb) {
    const next = parseInt(rb.dataset.arange);
    if (API_HIST.custom || API_HIST.range !== next) {
      if (typeof clearAnchoredRange === "function" && API_HIST.id) clearAnchoredRange("apimon:" + API_HIST.id);
    }
    API_HIST.custom = null; API_HIST.range = next; loadAPIHistory(); return;
  }
  const en = e.target.closest(".chart-enlarge"); if (!en) return;
  const ch = API_HIST_CHARTS[en.dataset.chart]; if (ch) openChartZoom(ch);
});
function applyAhistCustomRange() {
  applyCustomRangeFromInputs($("ahistCustomFrom"), $("ahistCustomTo"), (from, to) => {
    API_HIST.custom = { from, to }; loadAPIHistory();
  });
}

// 把当前 API 业务监控快照汇总为纯文本供 AI 分析；仅人工采纳/反馈后的结果进入学习闭环。
function apimonToText() {
  const systems = (LAST_APIMON && LAST_APIMON.systems) || [];
  if (!systems.length) return "（当前没有任何 API 业务监控系统）";
  let totalEps = 0, totalDown = 0;
  const lines = [];
  systems.forEach(sys => {
    const eps = sys.endpoints || [];
    const down = eps.filter(e => e.down).length;
    totalEps += eps.length; totalDown += down;
    lines.push(`# 业务系统：${sys.name}（每 ${sys.interval_sec}s · 接口 ${eps.length} 个 · 异常 ${down} 个 · 级别 ${sys.level || "critical"}${sys.enabled === false ? " · 已停用" : ""}）`);
    eps.forEach(ep => {
      const st = !ep.checked_at ? "未探测" : (ep.ok ? "正常" : "异常");
      lines.push(`  - ${ep.name} [${ep.method || "GET"} ${ep.url}] 状态=${st}${ep.status_code ? " HTTP" + ep.status_code : ""} 本次=${apiFmtMs(ep.latency_ms)} 平均1h=${apiFmtMs(ep.avg_ms)} P95=${apiFmtMs(ep.p95_ms)} 可用率1h=${apiFmtPct(ep.avail_1h)} 24h=${apiFmtPct(ep.avail_24h)} 吞吐1h=${ep.samples_1h > 0 ? ep.samples_1h.toFixed(0) : "—"}`);
    });
  });
  const head = `业务系统 ${systems.length} 个 · 接口共 ${totalEps} 个 · 当前异常 ${totalDown} 个。\n`;
  return (head + lines.join("\n")).slice(0, 12000);
}

// 「🤖 AI 分析」：对当前所有业务系统接口的可用率/时延/异常做整体研判，结果自动进入 RAG 记忆闭环
safeAddEventListener("apimonAIBtn", "click", () => {
  if (typeof openAIAssist !== "function") { if (typeof toast === "function") toast(I18N.t("assist.unavailable", "AI 面板未就绪"), "err"); return; }
  openAIAssist({ task: "apimon_diagnosis", mode: "analyze", title: I18N.t("assist.title_apimon", "AI · 可靠性保障分析"), context: apimonToText() });
});

/* ========== 合成事务监控（多步链式 + 变量提取/传递） ========== */
let LAST_TXNS = { transactions: [] };

async function loadAPITxns() {
  try {
    const d = await fetch(`${API}/apimon/transactions`).then(r => r.json());
    LAST_TXNS = d && d.transactions ? d : { transactions: [] };
    renderAPITxns(LAST_TXNS);
  } catch (e) { /* ignore */ }
}

function renderAPITxns(data) {
  const wrap = $("apimonTransactions");
  if (!wrap) return;
  const txns = data.transactions || [];
  if (!txns.length) {
    wrap.innerHTML = `<div class="empty-box">还没有合成事务。点右上角「添加合成事务」把「登录→下单→查询」这类多步业务流纳入监控——步骤间自动用 {{变量}} 传递提取的 token 等值，任一步失败即按失败步告警。</div>`;
    return;
  }
  wrap.innerHTML = txns.map(t => {
    const steps = t.step_results || [];
    const stat = !t.checked_at ? '<span class="sdot idle"></span>未执行'
      : (t.ok ? '<span class="sdot ok"></span>正常' : '<span class="sdot crit"></span>异常');
    const pills = steps.length
      ? steps.map((s, i) => `<span class="badge ${s.ok ? "ok" : "crit"}" title="${esc(s.msg || "")}">${i + 1}. ${esc(s.name)} ${s.ok ? "✓" : "✗"}${s.latency_ms ? " " + s.latency_ms.toFixed(0) + "ms" : ""}</span>`).join(" ")
      : `<span class="muted">${(t.steps || []).length} 步 · 未执行</span>`;
    return `<div class="api-sys-card" data-txn="${esc(t.id)}">
      <div class="api-sys-head">
        <div class="api-sys-title">${esc(t.name)} <span class="tag">${(t.steps || []).length} 步 · 每 ${t.interval_sec}s</span>${!t.enabled ? '<span class="tag">已停用</span>' : ""} <span style="font-weight:400; color:var(--muted)">${stat}${t.total_ms ? " · 总耗时 " + t.total_ms.toFixed(0) + "ms" : ""}</span></div>
        <div class="api-sys-actions">
          <button class="mini-btn" data-txnact="run" data-txn="${esc(t.id)}" title="立即执行">▶</button>
          <button class="mini-btn" data-txnact="edit" data-txn="${esc(t.id)}" title="编辑">✎</button>
          <button class="mini-btn del" data-txnact="del" data-txn="${esc(t.id)}" title="删除">✕</button>
        </div>
      </div>
      <div style="padding:12px 16px; display:flex; flex-wrap:wrap; gap:6px">${pills}</div>
    </div>`;
  }).join("");
}

// 每行 "Key: Value" 解析为对象
function txnParseKV(text) {
  const o = {};
  (text || "").split("\n").forEach(l => { const i = l.indexOf(":"); if (i > 0) { const k = l.slice(0, i).trim(); if (k) o[k] = l.slice(i + 1).trim(); } });
  return o;
}

function openAPITxnModal(t) {
  $("apiTxnId").value = t ? t.id : "";
  $("apiTxnName").value = t ? t.name : "";
  $("apiTxnInterval").value = t ? t.interval_sec : 60;
  $("apiTxnLevel").value = t ? t.level : "critical";
  $("apiTxnEnabled").checked = t ? t.enabled : true;
  $("apiTxnModalTitle").textContent = t ? "编辑合成事务" : "添加合成事务";
  $("apiTxnVars").value = (t && t.vars) ? Object.entries(t.vars).map(([k, v]) => `${k}: ${v}`).join("\n") : "";
  const rows = $("apiTxnStepRows");
  rows.innerHTML = "";
  const steps = (t && t.steps) || [];
  if (steps.length) steps.forEach(s => addTxnStepRow(s)); else addTxnStepRow(null);
  $("apiTxnMask").classList.add("show");
}

function addTxnStepRow(step) {
  const rows = $("apiTxnStepRows");
  const div = document.createElement("div");
  div.className = "api-ep-row";
  const m = (step && step.method) || "GET";
  const methods = ["GET", "POST", "PUT", "DELETE", "HEAD", "PATCH"].map(x => `<option ${x === m ? "selected" : ""}>${x}</option>`).join("");
  const hdr = (step && step.headers) ? Object.entries(step.headers).map(([k, v]) => `${k}: ${v}`).join("\n") : "";
  const ext = (step && step.extract) ? Object.entries(step.extract).map(([k, v]) => `${k}: ${v}`).join("\n") : "";
  div.innerHTML = `
    <button class="api-ep-del" data-txnact="step-del" title="移除步骤">✕</button>
    <div class="api-ep-grid">
      <input class="st-name" placeholder="步骤名，如 登录" value="${esc((step && step.name) || "")}">
      <select class="st-method sel">${methods}</select>
      <input class="st-url" placeholder="{{base}}/v1/login" value="${esc((step && step.url) || "")}">
    </div>
    <div class="api-ep-grid2">
      <input class="st-status" type="number" placeholder="期望状态码" value="${step && step.expect_status ? step.expect_status : ""}">
      <input class="st-keyword" placeholder="响应含关键字" value="${esc((step && step.expect_keyword) || "")}">
      <input class="st-jsonpath" placeholder="JSON路径 如 code" value="${esc((step && step.json_path) || "")}">
      <input class="st-jsonexpect" placeholder="JSON期望值 如 0" value="${esc((step && step.json_expect) || "")}">
    </div>
    <div class="api-ep-grid3">
      <textarea class="st-headers" rows="2" placeholder="请求头，每行 Key: Value，可用 {{变量}}，如 Authorization: Bearer {{token}}">${esc(hdr)}</textarea>
      <textarea class="st-body" rows="2" placeholder="请求体(可用 {{变量}})">${esc((step && step.body) || "")}</textarea>
    </div>
    <div style="margin-top:8px"><textarea class="st-extract" rows="2" placeholder="提取变量，每行 变量名: JSON路径，如 token: data.token（供后续步骤 {{token}} 引用）" style="width:100%; font-family:monospace; font-size:12px; padding:6px 9px; background:var(--bg3); border:1px solid var(--line2); border-radius:6px; color:var(--txt); box-sizing:border-box; resize:vertical">${esc(ext)}</textarea></div>`;
  rows.appendChild(div);
}

async function saveAPITxn() {
  const steps = [...document.querySelectorAll("#apiTxnStepRows .api-ep-row")].map(row => {
    const g = s => row.querySelector(s);
    return {
      name: g(".st-name").value.trim(), url: g(".st-url").value.trim(), method: g(".st-method").value,
      expect_status: parseInt(g(".st-status").value) || 0, expect_keyword: g(".st-keyword").value.trim(),
      json_path: g(".st-jsonpath").value.trim(), json_expect: g(".st-jsonexpect").value.trim(),
      headers: txnParseKV(g(".st-headers").value), body: g(".st-body").value,
      extract: txnParseKV(g(".st-extract").value)
    };
  }).filter(s => s.name && s.url);
  const body = {
    id: $("apiTxnId").value, name: $("apiTxnName").value.trim(),
    interval_sec: Math.max(5, parseInt($("apiTxnInterval").value) || 60),
    level: $("apiTxnLevel").value, enabled: $("apiTxnEnabled").checked,
    vars: txnParseKV($("apiTxnVars").value), steps: steps
  };
  if (!body.name) { toast("请填写事务名称", "err"); return; }
  if (!steps.length) { toast("请至少添加一个步骤（需填步骤名与 URL）", "err"); return; }
  await withLoading("apiTxnSaveBtn", async () => {
    try {
      const r = await fetch(`${API}/apimon/transactions`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (r.ok) { toast("已保存", "ok"); $("apiTxnMask").classList.remove("show"); loadAPITxns(); }
      else { const j = await r.json().catch(() => ({})); toast("保存失败：" + (j.error || ""), "err"); }
    } catch (e) { toast("保存失败：" + e, "err"); }
  });
}

async function delAPITxn(id) {
  const t = (LAST_TXNS.transactions || []).find(x => x.id === id);
  if (!confirm(`确认删除合成事务「${t ? t.name : id}」？`)) return;
  try { await fetch(`${API}/apimon/transactions/${encodeURIComponent(id)}`, { method: "DELETE" }); toast("已删除", "ok"); loadAPITxns(); }
  catch (e) { toast("删除失败：" + e, "err"); }
}

function runAPITxn(id) {
  fetch(`${API}/apimon/transactions/${encodeURIComponent(id)}/run`, { method: "POST" })
    .then(() => { toast("已触发执行", "ok"); setTimeout(loadAPITxns, 2000); })
    .catch(e => toast("触发失败：" + e, "err"));
}

safeAddEventListener("apiTxnAddBtn", "click", () => openAPITxnModal(null));
safeAddEventListener("apiTxnSaveBtn", "click", saveAPITxn);
safeAddEventListener("apiTxnAddStepBtn", "click", () => addTxnStepRow(null));
safeAddEventListener("apiTxnStepRows", "click", e => {
  const del = e.target.closest('[data-txnact="step-del"]'); if (!del) return;
  const rows = $("apiTxnStepRows");
  if (rows.querySelectorAll(".api-ep-row").length <= 1) { toast("至少保留一个步骤", "err"); return; }
  del.closest(".api-ep-row").remove();
});
safeAddEventListener("apimonTransactions", "click", e => {
  const btn = e.target.closest("[data-txnact]"); if (!btn) return;
  const id = btn.dataset.txn; if (!id) return;
  const act = btn.dataset.txnact;
  if (act === "run") runAPITxn(id);
  else if (act === "edit") openAPITxnModal((LAST_TXNS.transactions || []).find(x => x.id === id));
  else if (act === "del") delAPITxn(id);
});

/* ========== 分布式多点探测 ========== */
async function loadDist() {
  try { const d = await fetch(`${API}/apimon/distributed`).then(r => r.json()); renderDist((d && d.distributed) || []); } catch (e) { /* ignore */ }
}
function distScopeBadge(scope) {
  if (scope === "global") return '<span class="badge crit">全局故障</span>';
  if (scope === "regional") return '<span class="badge warn">区域性故障</span>';
  return '<span class="badge ok">正常</span>';
}
function renderDist(list) {
  const wrap = $("apimonDistributed");
  if (!wrap) return;
  if (!list.length) {
    wrap.innerHTML = `<div class="empty-box">还没有分布式探测。在接口的「高级选项」勾选「分布式多点探测」，即可让各地 agent 作为探针执行——服务端聚合区分区域性 vs 全局故障（复用已有 agent 网络，免额外部署探针；需 agent 更新到支持版本）。</div>`;
    return;
  }
  wrap.innerHTML = list.map(d => {
    const pts = (d.points || []).map(p => `<span class="badge ${p.ok ? "ok" : "crit"}" title="${esc(p.msg || "")}">${esc(p.hostname || p.host_id)} ${p.ok ? "✓" : "✗"}${p.latency_ms ? " " + p.latency_ms.toFixed(0) + "ms" : ""}</span>`).join(" ")
      || '<span class="muted">暂无探测点数据（agent 更新后开始上报）</span>';
    return `<div class="api-sys-card">
      <div class="api-sys-head"><div class="api-sys-title">${esc(d.name)} ${distScopeBadge(d.scope)} <span class="tag">${d.ok_count}/${d.total} 探测点正常</span></div></div>
      <div style="padding:12px 16px; display:flex; flex-wrap:wrap; gap:6px">${pts}</div>
    </div>`;
  }).join("");
}

// 仅在 API 性能监控视图可见时，每 15s 刷新一次聚合表 + 合成事务 + 分布式探测（避免后台空拉）。
function apimonTick() {
  const v = document.getElementById("view-apimon");
  if (v && v.classList.contains("active")) { loadAPIMon(); loadAPITxns(); loadDist(); }
}
setInterval(apimonTick, 15000);
