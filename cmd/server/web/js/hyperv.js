/* ============================================================================
 * Hyper-V 虚拟机页 — 主从式目录树：
 *   左侧树  物理宿主机 ▸ 其下虚拟机（可折叠、可选中、异常优先、状态圆点）
 *   右侧详情 选中 VM 的完整信息（概览 / CPU / 内存 / 硬盘 / 网络 / 检查点）
 * VM 关联回已纳管主机可一键跳转。样式经 <style> 注入（CSP 允许 style 'unsafe-inline'），
 * 不依赖 style.css；事件全部委托（CSP script-src 'self'，禁内联 onclick）。
 * 对老 Agent 缺失的明细字段优雅降级（显示"需更新 Agent"）。
 * ========================================================================== */

const hvT = (k, fb) => I18N.t(k, fb);

let HV_INVENTORIES = [];               // [{host_id, host_name, guest_count, guests:[...], updated_at}]
let HV_STALE_TOTAL = 0;                // orphan host inventories (Agent reinstall twins)
const HV_FILTER = { q: "", status: "running" }; // 默认「运行中」，与资产管理常用视角一致
let HV_HOST_FILTER = "";               // ""=全部宿主机
let HV_TREE_Q = "";                    // 左树内搜索
const HV_COLLAPSED = new Set();        // host_ids collapsed in the tree (default expanded)
let HV_SELECTED = null;                // { host, vm } currently shown in the detail panel

/* ---------- 小工具 ---------- */

function hvVmKey(g) { return g.id || ("name:" + (g.name || "")); }

function hvFmtGB(gb) {
  gb = +gb || 0;
  if (gb >= 1024) return (gb / 1024).toFixed(1) + " TB";
  if (gb > 0 && gb < 1) return Math.round(gb * 1024) + " MB";
  return gb.toFixed(1) + " GB";
}

function hvMac(m) {
  if (!m) return "—";
  const h = String(m).replace(/[^0-9A-Fa-f]/g, "");
  return h.length === 12 ? h.match(/.{2}/g).join(":").toUpperCase() : String(m);
}

/** Drop GBK→UTF-8 mojibake (U+FFFD diamonds) left by older Agents. */
function hvCleanText(s) {
  if (s == null || s === "") return "";
  const t = String(s).trim();
  if (!t) return "";
  if (/^[\uFFFD\s?◆]+$/.test(t)) return "";
  const bad = (t.match(/\uFFFD/g) || []).length;
  if (bad > 0 && bad * 2 >= [...t].length) return "";
  return t;
}

function hvAbnormal(g) {
  if (g.health === "Critical") return true;
  if (g.state !== "Running") return true;
  if (g.repl_health === "Warning" || g.repl_health === "Critical") return true;
  if ((g.cpu_usage || 0) >= 85) return true;
  const asg = g.mem_assigned_mb || 0, dem = g.mem_demand_mb || 0;
  if (g.dynamic_mem_enabled && asg > 0 && dem > 0 && dem / asg * 100 >= 90) return true;
  return false;
}

function hvDotClass(g) {
  if (g.health === "Critical") return "crit";
  if (g.state !== "Running") return "off";
  if (hvAbnormal(g)) return "warn";
  return "ok";
}

function hvStateText(g) {
  switch (g.state) {
    case "Running": return hvT("hyperv.st_running", "运行中");
    case "Off": return hvT("hyperv.st_off", "已关机");
    case "Paused": return hvT("hyperv.st_paused", "已暂停");
    case "Saved": return hvT("hyperv.st_saved", "已保存");
    default: return g.state || hvT("hyperv.st_unknown", "未知");
  }
}

function hvStateBadge(g) {
  if (g.health === "Critical") return `<span class="badge crit">${esc(hvT("hyperv.st_critical", "严重"))}</span>`;
  switch (g.state) {
    case "Running": return `<span class="badge ok">${esc(hvT("hyperv.st_running", "运行中"))}</span>`;
    case "Off": return `<span class="badge warn">${esc(hvT("hyperv.st_off", "已关机"))}</span>`;
    case "Paused": return `<span class="badge warn">${esc(hvT("hyperv.st_paused", "已暂停"))}</span>`;
    case "Saved": return `<span class="badge warn">${esc(hvT("hyperv.st_saved", "已保存"))}</span>`;
    default: return `<span class="badge">${esc(g.state || hvT("hyperv.st_unknown", "未知"))}</span>`;
  }
}

/* ---------- 过滤 ---------- */

function hvMatches(g, q, inv) {
  if (!q) return true;
  const hay = [
    g.name, g.state, g.id, (g.ip_addresses || []).join(" "),
    g.linked_host_name, g.linked_host_id,
    inv && inv.host_id, inv && inv.host_name,
  ].filter(Boolean).join(" ");
  return matchesSearchTokens(hay, q);
}

function hvGuestVisible(g, inv) {
  if (!hvMatches(g, HV_FILTER.q, inv)) return false;
  // 有关键词时跨状态搜索，避免默认「运行中」筛掉已关机的目标机
  if (normalizeSearchText(HV_FILTER.q)) return true;
  switch (HV_FILTER.status) {
    case "running": return g.state === "Running";
    case "notrunning": return g.state !== "Running";
    case "abnormal": return hvAbnormal(g);
    default: return true;
  }
}

function hvTreeCaret(id, hasKids, collapsed) {
  if (!hasKids) return `<span class="rtx-caret rtx-caret-gap" aria-hidden="true"></span>`;
  return `<button type="button" class="rtx-caret" data-hvhosttoggle="${esc(id)}" aria-expanded="${collapsed ? "false" : "true"}">${collapsed ? "▸" : "▾"}</button>`;
}

/* ---------- 数据加载 ---------- */

async function loadHyperVPanel() {
  const container = $("hypervPanel");
  if (!container) return;
  container.innerHTML = `<div class="empty-line">${esc(hvT("hyperv.loading", "加载中…"))}</div>`;
  try {
    const d = await fetch(`${API}/hyperv/list`).then(r => r.json());
    HV_INVENTORIES = (d && d.inventories) ? d.inventories : [];
    HV_STALE_TOTAL = (d && d.stale_total) ? (+d.stale_total || 0) : 0;
  } catch (e) {
    HV_INVENTORIES = [];
    HV_STALE_TOTAL = 0;
  }
  renderHyperVPanel();
}

function hvDupBannerHTML() {
  if (!HV_STALE_TOTAL) return "";
  return `<div class="hw-dup-bar">
    <span>⚠ ${esc(hvT("hyperv.dup_found", "检测到重复宿主机清单"))}
      ${HV_STALE_TOTAL} ${esc(hvT("hyperv.dup_stale_hint", "条可清理（Agent 重装会换主机 ID，旧 Hyper-V 清单会残留）"))}</span>
    <button class="btn sm danger" data-hvcleanup="1">${esc(hvT("hyperv.dup_clean", "清理"))}</button>
  </div>`;
}

async function hvCleanupDuplicates() {
  const msg = hvT("hyperv.dup_confirm", "将删除已离线的重复宿主机 Hyper-V 清单（保留当前在上报的那条）。该操作不可撤销，确定继续？");
  if (!confirm(msg)) return;
  try {
    const r = await fetch(`${API}/hyperv/cleanup-duplicates`, {
      method: "POST", credentials: "same-origin",
    }).then(r => r.json());
    toast(`${hvT("hyperv.dup_cleaned", "已清理重复宿主机清单")} ${r.count || 0}`, "ok");
    loadHyperVPanel();
  } catch (e) {
    toast(hvT("hyperv.dup_clean_failed", "清理失败") + "：" + (e.message || e), "err");
  }
}

/* ---------- 选中态 ---------- */

function hvFindSelected() {
  if (!HV_SELECTED) return null;
  const inv = HV_INVENTORIES.find(i => i.host_id === HV_SELECTED.host);
  if (!inv) return null;
  const g = (inv.guests || []).find(x => hvVmKey(x) === HV_SELECTED.vm);
  return g ? { inv, g } : null;
}

// 默认选中：当前筛选内的异常 VM → 筛选内第一台 → 任意第一台，让详情不空。
function hvFirstSelectable() {
  let firstAny = null, firstVisible = null, firstAbnormalVisible = null;
  for (const inv of HV_INVENTORIES) {
    if (HV_HOST_FILTER && !normalizeSearchText(HV_FILTER.q) && inv.host_id !== HV_HOST_FILTER) continue;
    for (const g of (inv.guests || [])) {
      const key = { host: inv.host_id, vm: hvVmKey(g) };
      if (!firstAny) firstAny = key;
      if (!hvGuestVisible(g, inv)) continue;
      if (!firstVisible) firstVisible = key;
      if (!firstAbnormalVisible && hvAbnormal(g)) firstAbnormalVisible = key;
    }
  }
  return firstAbnormalVisible || firstVisible || firstAny;
}

function hvSelect(host, vm) {
  HV_SELECTED = { host, vm };
  const tree = $("hvTree");
  if (tree) tree.querySelectorAll("[data-hvvm]").forEach(el =>
    el.classList.toggle("selected", el.dataset.hvhost === host && el.dataset.hvvm === vm));
  renderHyperVDetailOnly();
}

function hvSelectHost(hostId) {
  HV_HOST_FILTER = hostId || "";
  HV_SELECTED = hvFirstSelectable();
  renderHyperVPanel();
}

/* ---------- 左侧树 ---------- */

function hvVmNode(inv, g) {
  const sel = HV_SELECTED && HV_SELECTED.host === inv.host_id && HV_SELECTED.vm === hvVmKey(g);
  const running = g.state === "Running";
  const mini = running
    ? `${Math.round(g.cpu_usage || 0)}% · ${Math.round(g.mem_assigned_mb || 0)}MB`
    : hvStateText(g);
  return `<div class="rtx-node is-leaf${sel ? " selected" : ""}${hvAbnormal(g) ? " bad" : ""}" data-hvhost="${esc(inv.host_id)}" data-hvvm="${esc(hvVmKey(g))}" role="treeitem" aria-selected="${sel ? "true" : "false"}" tabindex="0">
    ${hvTreeCaret("", false, false)}
    <span class="rtx-dot ${hvDotClass(g)}" aria-hidden="true"></span>
    <span class="rtx-name" title="${esc(g.name)}">${esc(g.name)}</span>
    <span class="rtx-sub">${esc(mini)}</span>
  </div>`;
}

function hvHostMatchesTreeQ(inv, q) {
  if (!q) return true;
  return matchesSearchTokens([inv.host_name, inv.host_id].filter(Boolean).join(" "), q);
}

function hvHostNode(inv, filtering) {
  const guests = (inv.guests || []).slice().sort((a, b) => {
    const aa = hvAbnormal(a), ab = hvAbnormal(b);
    if (aa !== ab) return aa ? -1 : 1;
    return String(a.name).localeCompare(String(b.name));
  });
  const searchActive = !!normalizeSearchText(HV_FILTER.q);
  const visible = guests.filter(g => hvGuestVisible(g, inv));
  const bad = guests.filter(hvAbnormal).length;
  const hostName = inv.host_name || inv.host_id;
  const collapsed = HV_COLLAPSED.has(inv.host_id) && !filtering;
  const hostSel = !searchActive && HV_HOST_FILTER === inv.host_id;
  const tMem = +inv.host_total_mem_mb || 0, aMem = +inv.host_avail_mem_mb || 0;
  let memSub = "";
  if (tMem > 0) memSub = `${(aMem / 1024).toFixed(1)}/${(tMem / 1024).toFixed(1)} GB`;
  let body = "";
  if (!collapsed) {
    body = visible.length
      ? `<div class="rtx-children" role="group">${visible.map(g => hvVmNode(inv, g)).join("")}</div>`
      : `<div class="rtx-children"><div class="rtx-empty">${esc(hvT("hyperv.no_match", "无匹配虚拟机"))}</div></div>`;
  }
  return `<div class="rtx-folder">
    <div class="rtx-node${hostSel ? " selected" : ""}${guests.length ? " has-kids" : " is-leaf"}" data-hvhostsel="${esc(inv.host_id)}" role="treeitem" aria-selected="${hostSel ? "true" : "false"}" tabindex="0">
      ${hvTreeCaret(inv.host_id, guests.length > 0, collapsed)}
      <span class="rtx-ico rtx-ico-host" aria-hidden="true"></span>
      <span class="rtx-name" title="${esc(hostName)}">${esc(hostName)}</span>
      ${memSub ? `<span class="rtx-sub" title="${esc(hvT("hyperv.host_mem", "宿主机内存 可用/总"))}">${esc(memSub)}</span>` : ""}
      <span class="rtx-count${bad ? " bad" : ""}">${guests.length}${bad ? `<span class="hc-bad">${bad}</span>` : ""}</span>
    </div>
    ${body}
  </div>`;
}

function hvToolbar(totalHosts, totalVMs, totalBad) {
  return `<div class="rtx-toolbar">
    <input id="hvSearch" class="hw-search" type="search" placeholder="${esc(hvT("hyperv.search", "搜索 VM 名称 / IP / 宿主机…"))}" value="${esc(HV_FILTER.q)}" autocomplete="off" spellcheck="false">
    <select id="hvStatusFilter" class="hw-sel">
      <option value="">${esc(hvT("hyperv.f_all", "全部状态"))}</option>
      <option value="running"${HV_FILTER.status === "running" ? " selected" : ""}>${esc(hvT("hyperv.st_running", "运行中"))}</option>
      <option value="notrunning"${HV_FILTER.status === "notrunning" ? " selected" : ""}>${esc(hvT("hyperv.f_notrunning", "非运行"))}</option>
      <option value="abnormal"${HV_FILTER.status === "abnormal" ? " selected" : ""}>${esc(hvT("hyperv.attention", "需关注"))}</option>
    </select>
    <button data-hvrefresh="1" class="btn sm" title="${esc(hvT("hyperv.refresh", "刷新"))}">↻</button>
    <button type="button" class="btn sm ai-assist-btn" data-hvai-fleet="1" title="${esc(hvT("hyperv.ai_fleet_title", "AI 分析整体 Hyper-V 清单"))}"><span class="ai-assist-btn-ic">🤖</span>${esc(hvT("hyperv.ai_diag", "AI 诊断"))}</button>
    <span class="rtx-count">${hvT("hyperv.summary", "宿主机")} ${totalHosts} · ${hvT("hyperv.vm", "虚拟机")} ${totalVMs}${totalBad ? ` · ${hvT("hyperv.attention", "需关注")} ${totalBad}` : ""}</span>
  </div>
  ${hvDupBannerHTML()}`;
}

function hvTreeHTML(hosts) {
  const treeQ = normalizeSearchText(HV_TREE_Q);
  const searchActive = !!normalizeSearchText(HV_FILTER.q);
  // filtering 的唯一作用是「搜索时强制展开，把命中露出来」，所以它只能由**搜索框**决定。
  //
  // 这里原来把状态下拉也算进来了——而 HV_FILTER.status 默认就是 "running"，于是 filtering
  // 开箱即为真，下面两处 `&& !filtering` 让折叠状态**永远**被忽略：箭头点下去，HV_COLLAPSED
  // 确实写进去了，重渲染时又被强制展开。表现就是「Hyper-V 的树收不起来」。
  // 状态下拉是常驻的视图模式（默认只看运行中的机器），不是一次性的查找动作，把它当搜索
  // 处理是这个 bug 的根。主机树一直只认树内搜索（hosts.js: `!q && HOST_TREE_COLLAPSED...`），
  // 这里与它对齐。
  const filtering = searchActive || !!treeQ;
  // 左树始终列出全部宿主机（树搜过滤）；选中态只高亮，不隐藏兄弟节点
  const shown = hosts.filter(inv => hvHostMatchesTreeQ(inv, treeQ));
  const rootId = "__all__";
  const rootCollapsed = !filtering && HV_COLLAPSED.has(rootId);
  const hasKids = shown.length > 0;
  let totalVMs = 0;
  HV_INVENTORIES.forEach(inv => { totalVMs += (inv.guests || []).length; });
  let kids = "";
  if (!rootCollapsed && hasKids) {
    kids = `<div class="rtx-children" role="group">${shown.map(inv => hvHostNode(inv, filtering)).join("")}</div>`;
  } else if (treeQ || searchActive) {
    kids = `<div class="rtx-empty">${esc(hvT("hyperv.no_match", "无匹配虚拟机"))}</div>`;
  }
  return `<div class="rtx-tree-search">
      <input type="search" id="hvTreeSearch" class="rtx-tree-q" value="${esc(HV_TREE_Q || "")}"
        placeholder="${esc(hvT("hyperv.tree_search_ph", "搜索宿主机…"))}" autocomplete="off">
    </div>
    <div class="rtx-scroll">
      <div class="rtx-folder" role="tree">
        <div class="rtx-node rtx-root-node${!HV_HOST_FILTER || searchActive ? " selected" : ""}${hasKids ? " has-kids" : " is-leaf"}" data-hvhostsel="" role="treeitem" tabindex="0">
          ${hvTreeCaret(rootId, hasKids, rootCollapsed)}
          <span class="rtx-ico rtx-ico-all" aria-hidden="true"></span>
          <span class="rtx-name">${esc(hvT("hyperv.all_vms", "全部虚拟机"))}</span>
          <span class="rtx-count">${totalVMs}</span>
        </div>
        ${kids}
      </div>
    </div>`;
}

/* ---------- 右侧详情 ---------- */

function hvKv(k, v) { return `<div class="hv-kv"><span class="k">${k}</span><span class="v">${v}</span></div>`; }

function hvMiniTable(headers, rows) {
  if (!rows.length) return "";
  return `<table class="hv-mini-table"><thead><tr>${headers.map(h => `<th>${h}</th>`).join("")}</tr></thead><tbody>${rows.map(r => `<tr>${r.map(c => `<td>${c}</td>`).join("")}</tr>`).join("")}</tbody></table>`;
}

function hvOverviewCard(inv, g) {
  const kv = [];
  kv.push(hvKv(hvT("hyperv.ov_state", "状态"), hvStateBadge(g)));
  if (g.health && g.health !== "OK") kv.push(hvKv(hvT("hyperv.ov_health", "健康"), esc(g.health)));
  kv.push(hvKv(hvT("hyperv.ov_uptime", "运行时长"), (g.state === "Running" && g.uptime_sec) ? esc(fmtUptime(g.uptime_sec)) : "—"));
  kv.push(hvKv(hvT("hyperv.ov_host", "宿主机"), `<a class="hv-link" data-hvjump="${esc(inv.host_id)}" data-hvname="${esc(inv.host_name || inv.host_id)}">${esc(inv.host_name || inv.host_id)} ↗</a>`));
  if (g.linked_host_id) kv.push(hvKv(hvT("hyperv.ov_linked", "关联纳管主机"), `<a class="hv-link" data-hvjump="${esc(g.linked_host_id)}" data-hvname="${esc(g.linked_host_name || g.name)}">${esc(g.linked_host_name || g.name)} ↗</a>`));
  kv.push(hvKv(hvT("hyperv.ov_gen", "代/版本"), `${g.generation ? ("Gen" + g.generation) : "—"}${g.version ? (" · " + esc(g.version)) : ""}`));
  if (g.integration_state && !/^[\uFFFD\s?]+$/.test(g.integration_state)) {
    kv.push(hvKv(hvT("hyperv.ov_integration", "集成服务"), esc(g.integration_state)));
  }
  if (g.repl_state && g.repl_state !== "Disabled") kv.push(hvKv(hvT("hyperv.ov_repl", "复制"), esc(g.repl_state + " / " + (g.repl_health || ""))));
  return `<div class="hv-card"><h4>${hvT("hyperv.overview", "概览")}</h4>${kv.join("")}</div>`;
}

function hvCpuCard(g) {
  const running = g.state === "Running";
  const cpuHost = running ? (+g.cpu_usage || 0) : 0;              // 占整机 CPU 比例
  const cpuGuest = running ? (+g.cpu_guest_pct || 0) : 0;         // 占该 VM 自身 vCPU 比例(0~100)
  return `<div class="hv-card"><h4>CPU</h4>
    ${bar(hvT("hyperv.cpu_guest", "客户机 CPU 利用率"), Math.round(cpuGuest), running ? cpuGuest.toFixed(1) + "%" : "—")}
    ${hvKv(hvT("hyperv.cpu_host", "占宿主 CPU"), running ? cpuHost.toFixed(1) + "%" : "—")}
    ${hvKv(hvT("hyperv.vcpu", "vCPU 数"), g.processor_count || "—")}
  </div>`;
}

function hvMemCard(g) {
  const running = g.state === "Running";
  const asg = g.mem_assigned_mb || 0, dem = g.mem_demand_mb || 0;
  const pct = asg > 0 ? Math.round(dem / asg * 100) : 0;
  const barHtml = (running && g.dynamic_mem_enabled && asg > 0)
    ? bar(hvT("hyperv.mem_pressure", "内存压力(需求/分配)"), pct, `${Math.round(dem)} / ${Math.round(asg)} MB`)
    : "";
  const rangeKv = g.dynamic_mem_enabled
    ? hvKv(hvT("hyperv.mem_range", "动态范围"), `${Math.round(g.mem_min_mb || 0)} ~ ${Math.round(g.mem_max_mb || 0)} MB`)
    : hvKv(hvT("hyperv.mem_type", "内存类型"), hvT("hyperv.mem_static", "静态"));
  return `<div class="hv-card"><h4>${hvT("hyperv.memory", "内存")}</h4>
    ${barHtml}
    ${hvKv(hvT("hyperv.mem_assigned", "已分配"), running ? Math.round(asg) + " MB" : "—")}
    ${hvKv(hvT("hyperv.mem_demand", "需求"), running ? Math.round(dem) + " MB" : "—")}
    ${g.mem_startup_mb ? hvKv(hvT("hyperv.mem_startup", "启动内存"), Math.round(g.mem_startup_mb) + " MB") : ""}
    ${rangeKv}
  </div>`;
}

function hvDiskCard(g) {
  const disks = g.disks || [];
  let body;
  if (!disks.length) body = `<div class="hv-hint">${esc(hvT("hyperv.no_disk", "无磁盘明细（需更新 Agent 采集）"))}</div>`;
  else body = hvMiniTable(
    [hvT("hyperv.disk_path", "路径"), hvT("hyperv.disk_ctrl", "控制器"), hvT("hyperv.disk_size", "占用")],
    disks.map(d => [
      `<span title="${esc(d.path || "")}">${esc(shortenMountPath(d.path || "—"))}</span>`,
      esc((d.controller_type || "") + (d.controller_number != null ? ` ${d.controller_number}:${d.controller_location || 0}` : "")),
      d.file_size_gb ? esc(hvFmtGB(d.file_size_gb)) : "—",
    ]));
  return `<div class="hv-card hv-full"><h4>${hvT("hyperv.disk", "硬盘")} · ${disks.length || (g.vhd_count || 0)}</h4>${body}</div>`;
}

function hvNetCard(g) {
  const nics = g.nics || [];
  let body;
  if (!nics.length) {
    const ips = g.ip_addresses || [];
    body = ips.length
      ? hvKv("IP", esc(ips.join(", ")))
      : `<div class="hv-hint">${esc(hvT("hyperv.no_nic", "无网卡明细（需更新 Agent 采集）"))}</div>`;
  } else body = hvMiniTable(
    [hvT("hyperv.nic_name", "网卡"), "MAC", hvT("hyperv.nic_switch", "虚拟交换机"), hvT("hyperv.nic_status", "状态"), "IP"],
    nics.map(n => [
      esc(hvCleanText(n.name) || "—"),
      esc(hvMac(n.mac)),
      esc(hvCleanText(n.switch) || "—"),
      esc(hvCleanText(n.status) || (n.connected ? "Connected" : "—")),
      esc((n.ip_addresses || []).join(", ") || "—"),
    ]));
  const count = nics.length || ((g.ip_addresses || []).length ? 1 : 0);
  return `<div class="hv-card hv-full"><h4>${hvT("hyperv.network", "网络")} · ${count}</h4>${body}</div>`;
}

function hvCheckpointCard(g) {
  const cps = g.checkpoints || [];
  if (!cps.length) return "";
  const body = hvMiniTable(
    [hvT("hyperv.cp_name", "检查点"), hvT("hyperv.cp_created", "创建时间")],
    cps.map(c => [esc(c.name || "—"), esc((c.created || "").replace("T", " "))]));
  return `<div class="hv-card hv-full"><h4>${hvT("hyperv.checkpoints", "检查点")} · ${cps.length}</h4>${body}</div>`;
}

// hvQuickStats renders an at-a-glance chip row under the detail title.
function hvQuickStats(g) {
  const running = g.state === "Running";
  const chip = (k, v) => `<span class="hv-chip"><span class="ck">${k}</span><span class="cv">${v}</span></span>`;
  const chips = [
    chip("CPU", running ? Math.round(+g.cpu_guest_pct || +g.cpu_usage || 0) + "%" : "—"),
    chip(hvT("hyperv.memory", "内存"), running && g.mem_assigned_mb ? Math.round(g.mem_assigned_mb) + " MB" : "—"),
    chip("vCPU", g.processor_count || "—"),
    chip(hvT("hyperv.ov_uptime", "运行"), running && g.uptime_sec ? esc(fmtUptime(g.uptime_sec)) : "—"),
  ];
  const dc = (g.disks || []).length || g.vhd_count || 0;
  if (dc) chips.push(chip(hvT("hyperv.disk", "硬盘"), dc));
  const nc = (g.nics || []).length;
  if (nc) chips.push(chip(hvT("hyperv.network", "网卡"), nc));
  return `<div class="hv-chips">${chips.join("")}</div>`;
}

function hvDetailFor(inv, g) {
  const allowWrite = typeof canWrite === "function" && canWrite();
  const ops = allowWrite ? `<div class="hv-ops" style="display:flex;gap:6px;flex-wrap:wrap;margin:8px 0 4px">
    <button type="button" class="btn sm" data-hvpower="start">${esc(hvT("hyperv.power_start", "启动"))}</button>
    <button type="button" class="btn sm" data-hvpower="stop">${esc(hvT("hyperv.power_stop", "关机"))}</button>
    <button type="button" class="btn sm" data-hvpower="restart">${esc(hvT("hyperv.power_restart", "重启"))}</button>
    <button type="button" class="btn sm" data-hvpower="force_stop">${esc(hvT("hyperv.power_force", "强制关机"))}</button>
    <button type="button" class="btn sm primary" data-hvconfig="1">${esc(hvT("hyperv.edit_config", "编辑配置"))}</button>
  </div>` : "";
  let termBtn;
  if (g.linked_host_id) {
    termBtn = `<button type="button" class="btn sm" data-hvterm="${esc(g.linked_host_id)}" data-hvname="${esc(g.linked_host_name || g.name)}">${esc(hvT("hyperv.open_term", "打开终端"))}</button>
      <span class="badge ok" style="margin-left:6px">${esc(hvT("hyperv.linked", "已关联 Agent"))}</span>`;
  } else {
    termBtn = `<button type="button" class="btn sm" data-hvinstall="${esc(g.name || "")}">${esc(hvT("hyperv.copy_install", "复制 Guest 安装命令"))}</button>
      <span class="hint" style="margin-left:8px">${esc(hvT("hyperv.no_linked_term", "未关联纳管主机：请在 Guest 内安装 Agent 后即可打开终端"))}</span>`;
  }
  const head = `<div class="hv-dhead">
    <span class="hv-dot ${hvDotClass(g)}"></span>
    <span class="title">${esc(g.name)}</span>
    ${hvStateBadge(g)}
    <button type="button" class="btn sm ai-assist-btn" data-hvai="1" title="${esc(hvT("hyperv.ai_vm_title", "AI 分析该虚拟机运行状态"))}" style="margin-left:8px"><span class="ai-assist-btn-ic">🤖</span>${esc(hvT("hyperv.ai_diag", "AI 诊断"))}</button>
    ${termBtn}
    ${g.linked_host_id ? `<a class="hv-link" data-hvjump="${esc(g.linked_host_id)}" data-hvname="${esc(g.linked_host_name || g.name)}" style="margin-left:auto">${esc(hvT("hyperv.open_host", "打开纳管主机"))} ↗</a>` : ""}
  </div>${ops}`;
  const cards = [
    hvOverviewCard(inv, g), hvCpuCard(g), hvMemCard(g),
    hvDiskCard(g), hvNetCard(g), hvCheckpointCard(g),
  ].join("");
  return `<div class="hv-detailbox">${head}${hvQuickStats(g)}<div class="hv-cards">${cards}</div></div>`;
}

async function hvDoPower(action) {
  const sel = hvFindSelected();
  if (!sel) return;
  const label = { start: "启动", stop: "关机", restart: "重启", force_stop: "强制关机" }[action] || action;
  if (!confirm(hvT("hyperv.power_confirm", "确认对虚拟机「{name}」执行 {action}？").replace("{name}", sel.g.name || "").replace("{action}", label))) return;
  try {
    const r = await fetch(`${API}/hyperv/${encodeURIComponent(sel.inv.host_id)}/guests/${encodeURIComponent(sel.g.id || sel.g.name)}/power`, {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action, name: sel.g.name || "" }),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
    toast(hvT("hyperv.power_ok", "操作已提交"), "ok");
    setTimeout(() => loadHyperVPanel(), 1500);
  } catch (e) { toast(String(e.message || e), "err"); }
}

function hvCloseConfigModal() {
  const m = document.getElementById("hvConfigMask");
  if (m) m.classList.remove("show");
}

function hvOpenConfigModal() {
  const sel = hvFindSelected();
  if (!sel) return;
  const g = sel.g;
  const running = g.state === "Running" || g.state === "Paused";
  let mask = document.getElementById("hvConfigMask");
  if (!mask) {
    mask = document.createElement("div");
    mask.id = "hvConfigMask";
    mask.className = "mask";
    mask.setAttribute("data-close", "");
    document.body.appendChild(mask);
    mask.addEventListener("click", e => {
      if (e.target === mask || e.target.closest("[data-close-btn]")) hvCloseConfigModal();
    });
  }
  const dyn = !!g.dynamic_mem_enabled;
  mask.innerHTML = `<div class="modal" role="dialog" aria-modal="true">
    <div class="modal-head">
      <h3>${esc(hvT("hyperv.cfg_title", "编辑虚拟机配置"))} · ${esc(g.name || "")}</h3>
      <button type="button" class="btn ghost close" data-close-btn aria-label="关闭">✕</button>
    </div>
    <div class="modal-body">
      <p class="hint">${running
        ? esc(hvT("hyperv.cfg_need_off", "当前虚拟机未关机。修改 vCPU/内存通常需要先关机（Off）或已保存（Saved），否则操作会失败。"))
        : esc(hvT("hyperv.cfg_hint", "可同时调整 vCPU 与内存（含动态内存范围）。留空表示该项不改。"))}</p>
      <div class="cfg-form-row">
        <div class="field"><label>${esc(hvT("hyperv.vcpu", "vCPU 数"))}</label>
          <input id="hvCfgCPU" type="number" min="1" max="256" value="${esc(String(g.processor_count || 1))}"></div>
        <div class="field"><label>${esc(hvT("hyperv.mem_startup", "启动内存 MB"))}</label>
          <input id="hvCfgMem" type="number" min="32" value="${esc(String(Math.round(g.mem_startup_mb || g.mem_assigned_mb || 1024)))}"></div>
      </div>
      <label class="switch cfg-enable"><input type="checkbox" id="hvCfgDyn"${dyn ? " checked" : ""}>
        <span>${esc(hvT("hyperv.mem_dynamic", "启用动态内存"))}</span></label>
      <div class="cfg-form-row" id="hvCfgDynRow" style="${dyn ? "" : "opacity:.55"}">
        <div class="field"><label>${esc(hvT("hyperv.mem_min", "最小内存 MB"))}</label>
          <input id="hvCfgMin" type="number" min="32" value="${esc(String(Math.round(g.mem_min_mb || g.mem_startup_mb || 512)))}"></div>
        <div class="field"><label>${esc(hvT("hyperv.mem_max", "最大内存 MB"))}</label>
          <input id="hvCfgMax" type="number" min="32" value="${esc(String(Math.round(g.mem_max_mb || Math.max(g.mem_startup_mb || 0, 2048))))}"></div>
      </div>
      <p class="hint mono muted" id="hvCfgStatus"></p>
    </div>
    <div class="modal-foot">
      <button type="button" class="btn" data-close-btn>${esc(hvT("ui.cancel", "取消"))}</button>
      <button type="button" class="btn primary" id="hvCfgSave">${esc(hvT("common.save", "保存"))}</button>
    </div>
  </div>`;
  mask.classList.add("show");
  const dynCb = document.getElementById("hvCfgDyn");
  const dynRow = document.getElementById("hvCfgDynRow");
  if (dynCb && dynRow) dynCb.onchange = () => { dynRow.style.opacity = dynCb.checked ? "1" : ".55"; };
  document.getElementById("hvCfgSave").onclick = () => hvSubmitConfig(sel);
}

async function hvSubmitConfig(sel) {
  const cpuEl = document.getElementById("hvCfgCPU");
  const memEl = document.getElementById("hvCfgMem");
  const minEl = document.getElementById("hvCfgMin");
  const maxEl = document.getElementById("hvCfgMax");
  const dynEl = document.getElementById("hvCfgDyn");
  const status = document.getElementById("hvCfgStatus");
  const body = { name: sel.g.name || "" };
  const cpu = parseInt(cpuEl && cpuEl.value, 10);
  const mem = parseInt(memEl && memEl.value, 10);
  const min = parseInt(minEl && minEl.value, 10);
  const max = parseInt(maxEl && maxEl.value, 10);
  if (cpu > 0) body.processor_count = cpu;
  if (mem > 0) body.memory_mb = mem;
  body.dynamic_memory = !!(dynEl && dynEl.checked);
  if (body.dynamic_memory) {
    if (min > 0) body.memory_min_mb = min;
    if (max > 0) body.memory_max_mb = max;
  }
  if (!body.processor_count && !body.memory_mb && body.dynamic_memory === undefined) {
    toast(hvT("hyperv.config_empty", "未填写变更项"), "err");
    return;
  }
  if (!confirm(hvT("hyperv.config_confirm", "确认修改虚拟机「{name}」配置？").replace("{name}", sel.g.name || ""))) return;
  if (status) status.textContent = hvT("ui.loading", "提交中…");
  try {
    const r = await fetch(`${API}/hyperv/${encodeURIComponent(sel.inv.host_id)}/guests/${encodeURIComponent(sel.g.id || sel.g.name)}/config`, {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || j.output || `HTTP ${r.status}`);
    toast(hvT("toast.saved", "已保存"), "ok");
    hvCloseConfigModal();
    setTimeout(() => loadHyperVPanel(), 1500);
  } catch (e) {
    const msg = String(e.message || e);
    if (status) status.textContent = msg;
    toast(msg, "err");
  }
}

async function hvCopyGuestInstall(guestName) {
  try {
    const info = await fetch(`${API}/install/info`, { credentials: "same-origin" }).then(r => r.json());
    const server = info.server_url || location.origin;
    const token = info.token || "";
    const cat = encodeURIComponent(guestName || "hyperv-guest");
    const cmd = `curl -fsSL "${server}/install.sh?token=${encodeURIComponent(token)}&category=${cat}" | sh`;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(cmd);
    } else {
      const ta = document.createElement("textarea");
      ta.value = cmd; document.body.appendChild(ta); ta.select(); document.execCommand("copy"); ta.remove();
    }
    toast(hvT("hyperv.install_copied", "已复制 Guest 安装命令，请在虚拟机内以管理员/root 执行"), "ok");
  } catch (e) {
    toast(String(e.message || e), "err");
  }
}

function hvDoConfig() { hvOpenConfigModal(); }

function hvDetail() {
  const sel = hvFindSelected();
  if (!sel) return `<div class="hv-detailbox"><div class="hv-empty">${esc(hvT("hyperv.pick", "从左侧选择一台虚拟机，查看其 CPU / 内存 / 硬盘 / 网络 详情"))}</div></div>`;
  return hvDetailFor(sel.inv, sel.g);
}

/* ---------- 渲染 ---------- */

function renderHyperVDetailOnly() {
  const d = $("hvDetail");
  if (d) d.innerHTML = hvDetail();
}

function renderHyperVPanel() {
  hvInjectStyles();
  const container = $("hypervPanel");
  if (!container) return;
  if (!HV_INVENTORIES.length) {
    container.innerHTML = `<div class="empty-line">${esc(hvT("hyperv.empty", "暂无 Hyper-V 宿主机数据。请确认物理机已安装并更新 Agent（含 Hyper-V 采集），且该主机已启用 Hyper-V 角色。"))}</div>`;
    return;
  }
  if (!hvFindSelected()) HV_SELECTED = hvFirstSelectable();

  let totalVMs = 0, totalBad = 0;
  HV_INVENTORIES.forEach(inv => {
    totalVMs += (inv.guests || []).length;
    totalBad += (inv.guests || []).filter(hvAbnormal).length;
  });
  const hosts = HV_INVENTORIES.slice().sort((a, b) =>
    (b.guests || []).filter(hvAbnormal).length - (a.guests || []).filter(hvAbnormal).length);
  const focusTree = document.activeElement && document.activeElement.id === "hvTreeSearch";
  const focusMain = document.activeElement && document.activeElement.id === "hvSearch";
  const focusId = focusTree ? "hvTreeSearch" : (focusMain ? "hvSearch" : "");
  const focusPos = focusId && document.activeElement ? document.activeElement.selectionStart : -1;
  const hvCol = window.treeCollapsed && window.treeCollapsed("aiops_hv_tree");
  container.innerHTML = `<div class="rtx-wrap tree-wrap${hvCol ? " tree-collapsed" : ""}">` +
      `<div class="rtx-tree tree-pane" id="hvTree">${hvTreeHTML(hosts)}</div>` +
      `<button class="tree-toggle-btn" data-tree-toggle="aiops_hv_tree" title="收起/展开虚拟机列表，给右侧腾空间" aria-expanded="${hvCol ? "false" : "true"}">${hvCol ? "›" : "‹"}</button>` +
      `<div class="rtx-main">` +
        hvToolbar(HV_INVENTORIES.length, totalVMs, totalBad) +
        `<div class="rtx-main-scroll" id="hvDetail">${hvDetail()}</div>` +
      `</div>` +
    `</div>`;
  if (focusId) {
    const el = $(focusId);
    if (el) { el.focus(); if (focusPos >= 0) try { el.setSelectionRange(focusPos, focusPos); } catch (e) {} }
  }
}

/* ---------- AI 诊断 ---------- */

function hvVmToText(inv, g) {
  let s = `虚拟机：${g.name || "?"}\n宿主机：${(inv && (inv.host_name || inv.host_id)) || "?"}\n`;
  s += `状态：${g.state || "?"}  健康：${g.health || "OK"}\n`;
  if (g.uptime_sec) s += `运行时长(秒)：${g.uptime_sec}\n`;
  s += `vCPU：${g.processor_count || "—"}  客户机CPU%：${g.cpu_guest_pct || g.cpu_usage || 0}  占宿主CPU%：${g.cpu_usage || 0}\n`;
  s += `内存分配MB：${g.mem_assigned_mb || 0}  需求MB：${g.mem_demand_mb || 0}  动态内存：${g.dynamic_mem_enabled ? "是" : "否"}\n`;
  if (g.mem_min_mb || g.mem_max_mb) s += `动态范围MB：${g.mem_min_mb || 0} ~ ${g.mem_max_mb || 0}\n`;
  if (g.linked_host_name || g.linked_host_id) s += `关联纳管主机：${g.linked_host_name || g.linked_host_id}\n`;
  if (g.integration_state) s += `集成服务：${g.integration_state}\n`;
  if (g.repl_state) s += `复制：${g.repl_state} / ${g.repl_health || ""}\n`;
  const ips = (g.ip_addresses || []).join(", ");
  if (ips) s += `IP：${ips}\n`;
  (g.disks || []).forEach((d, i) => {
    s += `磁盘${i + 1}：${d.path || d.name || "?"} 大小GB=${d.size_gb || d.vhd_size_gb || "?"} 类型=${d.type || d.vhd_type || "?"}\n`;
  });
  (g.nics || []).forEach((n, i) => {
    s += `网卡${i + 1}：${n.name || "?"} MAC=${n.mac || "?"} 交换机=${n.switch_name || n.switch || "?"}\n`;
  });
  const cps = g.checkpoints || g.snapshots || [];
  if (cps.length) s += `检查点数量：${cps.length}\n`;
  return s;
}

function hvFleetToText() {
  let s = `Hyper-V 清单摘要（宿主机 ${HV_INVENTORIES.length} 台）\n`;
  HV_INVENTORIES.forEach(inv => {
    const guests = inv.guests || [];
    const bad = guests.filter(hvAbnormal);
    s += `\n【宿主机 ${inv.host_name || inv.host_id}】VM ${guests.length}，需关注 ${bad.length}\n`;
    guests.slice(0, 40).forEach(g => {
      const mark = hvAbnormal(g) ? "!" : " ";
      s += ` ${mark} ${g.name || "?"}  ${g.state || "?"}  CPU%=${Math.round(+g.cpu_guest_pct || +g.cpu_usage || 0)}  memMB=${Math.round(g.mem_assigned_mb || 0)}\n`;
    });
    if (guests.length > 40) s += `  …另有 ${guests.length - 40} 台省略\n`;
  });
  return s.slice(0, 14000);
}

function hvOpenSelectedAI() {
  const sel = hvFindSelected();
  if (!sel) { toast(hvT("hyperv.pick", "请先选择一台虚拟机"), "err"); return; }
  if (typeof openAIAssist !== "function") { toast(hvT("hyperv.ai_unavailable", "AI 面板未就绪"), "err"); return; }
  openAIAssist({
    task: "hyperv_ops_plan",
    title: "🤖 AI Hyper-V 运维 · " + (sel.g.name || "VM"),
    mode: "analyze",
    context: hvVmToText(sel.inv, sel.g).slice(0, 14000),
    hint: hvT("hyperv.ai_hint", "正在分析该虚拟机并生成可执行动作…"),
    applyLabel: hvT("ai.apply_actions", "应用建议动作"),
    applyTo: async (text) => {
      if (typeof window.applyOpsActionPlan !== "function") return false;
      return window.applyOpsActionPlan(text, { source: "hyperv", refresh: () => loadHyperVPanel() });
    },
  });
}

function hvOpenFleetAI() {
  if (!HV_INVENTORIES.length) { toast(hvT("hyperv.empty", "暂无 Hyper-V 数据"), "err"); return; }
  if (typeof openAIAssist !== "function") { toast(hvT("hyperv.ai_unavailable", "AI 面板未就绪"), "err"); return; }
  openAIAssist({
    task: "hyperv_ops_plan",
    title: "🤖 AI Hyper-V 清单运维",
    mode: "analyze",
    context: hvFleetToText(),
    hint: hvT("hyperv.ai_fleet_hint", "正在分析整体虚拟化面并生成可执行动作…"),
    applyLabel: hvT("ai.apply_actions", "应用建议动作"),
    applyTo: async (text) => {
      if (typeof window.applyOpsActionPlan !== "function") return false;
      return window.applyOpsActionPlan(text, { source: "hyperv", refresh: () => loadHyperVPanel() });
    },
  });
}

/* ---------- 导出（资产管理） ---------- */

function hvFilterLabel() {
  switch (HV_FILTER.status) {
    case "running": return hvT("hyperv.st_running", "运行中");
    case "notrunning": return hvT("hyperv.f_notrunning", "非运行");
    case "abnormal": return hvT("hyperv.attention", "需关注");
    default: return hvT("hyperv.f_all", "全部状态");
  }
}

function hvExportModel() {
  let totalVMs = 0, totalRunning = 0, totalBad = 0;
  const vmRows = [], diskRows = [], nicRows = [], cpRows = [], hostRows = [];

  HV_INVENTORIES.forEach(inv => {
    const hostName = inv.host_name || inv.host_id || "";
    const guests = inv.guests || [];
    const running = guests.filter(g => g.state === "Running").length;
    const bad = guests.filter(hvAbnormal).length;
    totalVMs += guests.length;
    totalRunning += running;
    totalBad += bad;

    const tMem = +inv.host_total_mem_mb || 0, aMem = +inv.host_avail_mem_mb || 0;
    const hostMem = tMem > 0 ? `${(aMem / 1024).toFixed(1)}/${(tMem / 1024).toFixed(1)}` : "";
    hostRows.push([
      hostName, inv.host_id || "", String(guests.length), String(running), String(bad),
      hostMem, inv.updated_at || "",
    ]);

    guests.forEach(g => {
      const runningG = g.state === "Running";
      const memType = g.dynamic_mem_enabled
        ? hvT("hyperv.mem_dynamic", "动态")
        : hvT("hyperv.mem_static", "静态");
      const memRange = g.dynamic_mem_enabled
        ? `${Math.round(g.mem_min_mb || 0)} ~ ${Math.round(g.mem_max_mb || 0)}`
        : "";
      const disks = g.disks || [];
      const nics = g.nics || [];
      const cps = g.checkpoints || [];
      const ips = (g.ip_addresses || []).join(", ")
        || nics.flatMap(n => n.ip_addresses || []).filter(Boolean).join(", ");

      vmRows.push([
        hostName,
        g.name || "",
        hvStateText(g),
        g.health || "OK",
        (runningG && g.uptime_sec) ? fmtUptime(g.uptime_sec) : "",
        g.processor_count || "",
        runningG ? ((+g.cpu_guest_pct || +g.cpu_usage || 0).toFixed(1)) : "",
        runningG ? ((+g.cpu_usage || 0).toFixed(1)) : "",
        runningG && g.mem_assigned_mb ? Math.round(g.mem_assigned_mb) : "",
        runningG && g.mem_demand_mb != null ? Math.round(g.mem_demand_mb) : "",
        g.mem_startup_mb ? Math.round(g.mem_startup_mb) : "",
        memType,
        memRange,
        g.generation ? ("Gen" + g.generation) : "",
        g.version || "",
        g.integration_state || "",
        (g.repl_state && g.repl_state !== "Disabled")
          ? `${g.repl_state}${g.repl_health ? " / " + g.repl_health : ""}` : "",
        ips,
        g.linked_host_name || g.linked_host_id || "",
        disks.length || g.vhd_count || 0,
        nics.length || ((g.ip_addresses || []).length ? 1 : 0),
        cps.length || g.checkpoint_count || 0,
        g.id || "",
      ]);

      disks.forEach(d => {
        diskRows.push([
          hostName, g.name || "",
          d.path || "",
          ((d.controller_type || "") + (d.controller_number != null ? ` ${d.controller_number}:${d.controller_location || 0}` : "")).trim(),
          d.file_size_gb ? hvFmtGB(d.file_size_gb) : "",
        ]);
      });
      nics.forEach(n => {
        nicRows.push([
          hostName, g.name || "",
          n.name || "",
          hvMac(n.mac),
          n.switch || "",
          n.status || (n.connected ? "Connected" : ""),
          (n.ip_addresses || []).join(", "),
        ]);
      });
      cps.forEach(c => {
        cpRows.push([
          hostName, g.name || "",
          c.name || "",
          (c.created || "").replace("T", " "),
        ]);
      });
    });
  });

  const model = {
    title: hvT("hyperv.export_title", "Hyper-V 虚拟机资产清单"),
    subtitle: `${hvT("hyperv.meta_exported_at", "导出时间")}: ${new Date().toLocaleString()}`,
    meta: [
      [hvT("hyperv.meta_hosts", "宿主机数"), String(HV_INVENTORIES.length)],
      [hvT("hyperv.meta_vms", "虚拟机数"), String(totalVMs)],
      [hvT("hyperv.meta_running", "运行中"), String(totalRunning)],
      [hvT("hyperv.meta_attention", "需关注"), String(totalBad)],
      [hvT("hyperv.meta_filter", "当前筛选"),
        [hvFilterLabel(), HV_FILTER.q ? (`q=${HV_FILTER.q}`) : ""].filter(Boolean).join(" · ")],
    ],
    sections: [],
  };

  const add = (title, columns, rows) => { if (rows.length) model.sections.push({ title, columns, rows }); };

  add(hvT("hyperv.export_sec_hosts", "宿主机汇总"),
    [hvT("hyperv.col_host", "物理机名称"), "Host ID",
     hvT("hyperv.col_host_vm_count", "虚拟机数"), hvT("hyperv.col_host_running", "运行中"),
     hvT("hyperv.attention", "需关注"), hvT("hyperv.col_host_mem", "宿主机可用/总内存(GB)"),
     hvT("hyperv.col_updated", "清单更新时间")],
    hostRows);

  add(hvT("hyperv.export_sec_vms", "虚拟机清单"),
    [hvT("hyperv.col_host", "物理机名称"), hvT("hyperv.col_vm", "虚拟机名称"),
     hvT("hyperv.col_state", "运行状态"), hvT("hyperv.col_health", "健康"),
     hvT("hyperv.col_uptime", "运行时长"), hvT("hyperv.col_vcpu", "vCPU"),
     hvT("hyperv.col_cpu_guest", "客户机CPU%"), hvT("hyperv.col_cpu_host", "占宿主CPU%"),
     hvT("hyperv.col_mem_assigned", "内存已分配(MB)"), hvT("hyperv.col_mem_demand", "内存需求(MB)"),
     hvT("hyperv.col_mem_startup", "启动内存(MB)"), hvT("hyperv.col_mem_type", "内存类型"),
     hvT("hyperv.col_mem_range", "动态范围(MB)"), hvT("hyperv.col_gen", "世代"),
     hvT("hyperv.col_version", "配置版本"), hvT("hyperv.col_integration", "集成服务"),
     hvT("hyperv.col_repl", "复制状态"), hvT("hyperv.col_ips", "IP地址"),
     hvT("hyperv.col_linked", "关联纳管主机"), hvT("hyperv.col_disk_count", "硬盘数"),
     hvT("hyperv.col_nic_count", "网卡数"), hvT("hyperv.col_cp_count", "检查点数"),
     hvT("hyperv.col_vm_id", "虚拟机ID")],
    vmRows);

  add(hvT("hyperv.export_sec_disks", "虚拟硬盘明细"),
    [hvT("hyperv.col_host", "物理机名称"), hvT("hyperv.col_vm", "虚拟机名称"),
     hvT("hyperv.col_disk_path", "磁盘路径"), hvT("hyperv.col_disk_ctrl", "控制器"),
     hvT("hyperv.col_disk_size", "占用空间")],
    diskRows);

  add(hvT("hyperv.export_sec_nics", "网卡明细"),
    [hvT("hyperv.col_host", "物理机名称"), hvT("hyperv.col_vm", "虚拟机名称"),
     hvT("hyperv.col_nic_name", "网卡名称"), hvT("hyperv.col_nic_mac", "MAC"),
     hvT("hyperv.col_nic_switch", "虚拟交换机"), hvT("hyperv.col_nic_status", "网卡状态"),
     hvT("hyperv.col_ips", "IP地址")],
    nicRows);

  add(hvT("hyperv.export_sec_cps", "检查点明细"),
    [hvT("hyperv.col_host", "物理机名称"), hvT("hyperv.col_vm", "虚拟机名称"),
     hvT("hyperv.col_cp_name", "检查点名称"), hvT("hyperv.col_cp_created", "创建时间")],
    cpRows);

  return model;
}

function hvToggleExportMenu(show) {
  const menu = $("hvExportMenu"), btn = $("hvExportBtn");
  if (!menu || !btn) return;
  const open = show === undefined ? !menu.classList.contains("show") : show;
  menu.classList.toggle("show", open);
  btn.setAttribute("aria-expanded", open ? "true" : "false");
}

async function hvDoExport(fmt) {
  hvToggleExportMenu(false);
  if (!HV_INVENTORIES.length) {
    toast(hvT("hyperv.export_empty", "暂无虚拟机数据可导出"), "err");
    return;
  }
  try {
    const model = hvExportModel();
    if (!model.sections.length) {
      toast(hvT("hyperv.export_empty", "暂无虚拟机数据可导出"), "err");
      return;
    }
    const ok = await exportModel(model, fmt, hvT("hyperv.export_prefix", "HyperV虚拟机资产"));
    if (!ok) {
      toast(hvT("hyperv.export_popup_blocked", "导出失败：请允许本站弹出窗口后重试"), "err");
      return;
    }
    if (fmt !== "pdf") toast(hvT("toast.exported", "已导出"), "ok");
  } catch (e) {
    toast(hvT("hyperv.export_failed", "导出失败") + "：" + (e.message || e), "err");
  }
}

/* ---------- 事件（全部委托） ---------- */

safeAddEventListener("hypervPanel", "click", e => {
  const refresh = e.target.closest("[data-hvrefresh]");
  if (refresh) { loadHyperVPanel(); return; }
  const clean = e.target.closest("[data-hvcleanup]");
  if (clean) { hvCleanupDuplicates(); return; }
  const fleetAI = e.target.closest("[data-hvai-fleet]");
  if (fleetAI) { hvOpenFleetAI(); return; }
  const vmAI = e.target.closest("[data-hvai]");
  if (vmAI) { hvOpenSelectedAI(); return; }
  const power = e.target.closest("[data-hvpower]");
  if (power) { hvDoPower(power.dataset.hvpower); return; }
  const cfgBtn = e.target.closest("[data-hvconfig]");
  if (cfgBtn) { hvDoConfig(); return; }
  const installBtn = e.target.closest("[data-hvinstall]");
  if (installBtn) { hvCopyGuestInstall(installBtn.dataset.hvinstall || ""); return; }
  const term = e.target.closest("[data-hvterm]");
  if (term && typeof openTerminal === "function") {
    openTerminal(term.dataset.hvterm, term.dataset.hvname || term.dataset.hvterm);
    return;
  }
  const jump = e.target.closest("[data-hvjump]");
  if (jump && typeof openDetail === "function") {
    openDetail(jump.dataset.hvjump, jump.dataset.hvname || jump.dataset.hvjump);
    return;
  }
  const htoggle = e.target.closest("[data-hvhosttoggle]");
  if (htoggle) {
    e.preventDefault();
    e.stopPropagation();
    const id = htoggle.dataset.hvhosttoggle;
    if (!id) return;
    if (HV_COLLAPSED.has(id)) HV_COLLAPSED.delete(id); else HV_COLLAPSED.add(id);
    renderHyperVPanel();
    return;
  }
  const hostSel = e.target.closest("[data-hvhostsel]");
  if (hostSel && !e.target.closest("[data-hvvm]")) {
    hvSelectHost(hostSel.dataset.hvhostsel || "");
    return;
  }
  const vm = e.target.closest("[data-hvvm]");
  if (vm) { hvSelect(vm.dataset.hvhost, vm.dataset.hvvm); return; }
});

let HV_SEARCH_T = null;
function onHvSearchInput(e) {
  if (!e.target) return;
  if (e.target.id === "hvTreeSearch") {
    HV_TREE_Q = e.target.value || "";
    renderHyperVPanel();
    return;
  }
  if (e.target.id !== "hvSearch") return;
  clearTimeout(HV_SEARCH_T);
  const v = e.target.value;
  HV_SEARCH_T = setTimeout(() => {
    HV_FILTER.q = v;
    renderHyperVPanel();
  }, 200);
}
safeAddEventListener("hypervPanel", "input", onHvSearchInput);
safeAddEventListener("hypervPanel", "search", onHvSearchInput);
safeAddEventListener("hypervPanel", "change", e => {
  if (e.target.id === "hvStatusFilter") { HV_FILTER.status = e.target.value; renderHyperVPanel(); }
});

safeAddEventListener("hvRefreshBtn2", "click", () => loadHyperVPanel());
safeAddEventListener("hvExportBtn", "click", e => { e.stopPropagation(); hvToggleExportMenu(); });
safeAddEventListener("hvExportMenu", "click", e => {
  const o = e.target.closest("[data-hvexport]");
  if (o) hvDoExport(o.dataset.hvexport);
});
document.addEventListener("click", e => {
  if (!e.target.closest("#hvExportDD")) hvToggleExportMenu(false);
});

/* ---------- 样式（注入一次） ---------- */

function hvInjectStyles() {
  if (document.getElementById("hv-css")) return;
  const s = document.createElement("style");
  s.id = "hv-css";
  s.textContent = `
  .hv-detailbox{min-height:160px}
  .hv-dhead{display:flex;align-items:center;gap:10px;flex-wrap:wrap;padding-bottom:12px;margin-bottom:14px;border-bottom:1px solid var(--line)}
  .hv-dhead .title{font-size:16px;font-weight:600;color:var(--txt)}
  .hv-chips{display:flex;flex-wrap:wrap;gap:8px;margin-bottom:14px}
  .hv-chip{display:inline-flex;align-items:center;gap:6px;background:var(--panel2);border:1px solid var(--line);border-radius:8px;padding:5px 10px;font-size:12px}
  .hv-chip .ck{color:var(--muted)}
  .hv-chip .cv{color:var(--txt);font-weight:600;font-variant-numeric:tabular-nums}
  .hv-cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:12px}
  .hv-card{background:var(--panel2);border:1px solid var(--line);border-radius:8px;padding:12px}
  .hv-card.hv-full{grid-column:1/-1}
  .hv-card h4{margin:0 0 10px;font-size:11px;color:var(--muted);font-weight:600;text-transform:uppercase;letter-spacing:.04em;display:flex;align-items:center;gap:6px}
  .hv-card h4::before{content:"";width:3px;height:11px;border-radius:2px;background:var(--accent);opacity:.7}
  .hv-kv{display:flex;justify-content:space-between;gap:10px;padding:3px 0;font-size:13px;border-bottom:1px dashed transparent}
  .hv-kv .k{color:var(--muted);white-space:nowrap}
  .hv-kv .v{color:var(--txt);text-align:right;font-variant-numeric:tabular-nums;word-break:break-word}
  .hv-mini-table{width:100%;border-collapse:collapse;font-size:12px}
  .hv-mini-table th{text-align:left;color:var(--muted);font-weight:500;padding:4px 10px 5px 0;border-bottom:1px solid var(--line);white-space:nowrap}
  .hv-mini-table td{padding:5px 10px 5px 0;color:var(--txt2);border-bottom:1px solid var(--line);word-break:break-all;vertical-align:top}
  .hv-mini-table tr:last-child td{border-bottom:none}
  .hv-hint,.hv-empty{color:var(--muted);font-size:12px}
  .hv-empty{padding:40px 12px;text-align:center;font-size:13px}
  .hv-link{color:var(--accent);cursor:pointer;text-decoration:none}
  .hv-link:hover{text-decoration:underline}`;
  document.head.appendChild(s);
}

/* 供 nav.js 的 _pageRenderers 调用 */
if (typeof window._pageRenderers === "undefined") window._pageRenderers = {};
window._pageRenderers.hyperv = loadHyperVPanel;
