/* ---------- 渲染：KPI ---------- */
function renderCards(s) {
  const cardsEl = $("cards");
  const values = [
    { goto: "hosts:all", v: s.total_hosts, cls: "info", vcls: "" },
    { goto: "hosts:online", v: s.online_hosts, cls: "ok", vcls: "ok" },
    { goto: "hosts:offline", v: s.offline_hosts, cls: s.offline_hosts > 0 ? "crit" : "", vcls: s.offline_hosts > 0 ? "crit" : "" },
    { goto: "alerts:", v: s.critical_alerts, cls: s.critical_alerts > 0 ? "crit" : "ok", vcls: s.critical_alerts > 0 ? "crit" : "ok" },
    { goto: "alerts:", v: s.warning_alerts, cls: s.warning_alerts > 0 ? "warn" : "ok", vcls: s.warning_alerts > 0 ? "warn" : "ok" },
    { goto: "log:", v: s.plugin_events || 0, cls: "info", vcls: (s.plugin_events || 0) > 0 ? "info" : "" },
  ];
  const existing = cardsEl.querySelectorAll(".card");
  if (existing.length === values.length) {
    let prevCrit = 0;
    existing.forEach((card, i) => {
      const spec = values[i];
      const vEl = card.querySelector(".v");
      const oldVal = vEl ? (parseInt(vEl.dataset.val) || 0) : 0;
      if (i === 3) prevCrit = oldVal;
      card.className = "card " + spec.cls;
      card.setAttribute("data-goto", spec.goto);
      if (vEl) {
        vEl.className = "v mono " + (spec.vcls || "");
        vEl.dataset.val = String(spec.v);
        if (oldVal !== spec.v) animateValue(vEl, oldVal, spec.v, 400);
        else vEl.textContent = String(spec.v);
      }
    });
    if (s.critical_alerts > prevCrit) {
      const critCard = cardsEl.querySelector(".card.crit[data-goto='alerts:']");
      if (critCard) {
        critCard.classList.add("card-pulse");
        setTimeout(() => critCard.classList.remove("card-pulse"), 600);
      }
    }
  } else {
    const prevVals = {};
    cardsEl.querySelectorAll(".v[data-val]").forEach(el => {
      const k = el.closest(".card")?.getAttribute("data-goto");
      if (k) prevVals[k] = parseInt(el.dataset.val) || 0;
    });
    const card = (cls, ic, v, k, vcls, goto) =>
      `<div class="card ${cls}" data-goto="${goto}" title="${I18N.t('section.click_view')}"><div class="ic">${icon(ic)}</div><div class="txt"><div class="v mono ${vcls || ""}" data-val="${v}">${v}</div><div class="k">${k}</div></div></div>`;
    cardsEl.innerHTML =
      card("info", "host", s.total_hosts, I18N.t("ui.total_hosts"), "", "hosts:all") +
      card("ok", "on", s.online_hosts, I18N.t("ui.online"), "ok", "hosts:online") +
      card(s.offline_hosts > 0 ? "crit" : "", "off", s.offline_hosts, I18N.t("ui.offline"), s.offline_hosts > 0 ? "crit" : "", "hosts:offline") +
      card(s.critical_alerts > 0 ? "crit" : "ok", "crit", s.critical_alerts, I18N.t("ui.critical_alerts"), s.critical_alerts > 0 ? "crit" : "ok", "alerts:") +
      card(s.warning_alerts > 0 ? "warn" : "ok", "warn", s.warning_alerts, I18N.t("ui.warning"), s.warning_alerts > 0 ? "warn" : "ok", "alerts:") +
      card("info", "event", s.plugin_events || 0, I18N.t("ui.plugin_events"), s.plugin_events > 0 ? "info" : "", "log:");
    cardsEl.querySelectorAll(".v[data-val]").forEach(el => {
      const goto = el.closest(".card")?.getAttribute("data-goto");
      const newVal = parseInt(el.dataset.val) || 0;
      const oldVal = prevVals[goto] !== undefined ? prevVals[goto] : newVal;
      if (oldVal !== newVal) animateValue(el, oldVal, newVal, 400);
    });
    const prevCrit = prevVals["alerts:"] !== undefined ? prevVals["alerts:"] : 0;
    if (s.critical_alerts > prevCrit) {
      const critCard = cardsEl.querySelector(".card.crit[data-goto='alerts:']");
      if (critCard) {
        critCard.classList.add("card-pulse");
        setTimeout(() => critCard.classList.remove("card-pulse"), 600);
      }
    }
  }

  // 空态引导 & 版本号
  const ob = $("onboarding");
  if (ob) ob.style.display = s.total_hosts === 0 ? "block" : "none";
  // 版本号显示在 brand 副标题中
  const verSpan = document.querySelector(".brand .sub");
  if (verSpan && s.version && s.version !== "AIOps") {
    verSpan.textContent = s.version;
  }
  // 新版控制台入口：只有产物真的打进了这个二进制才显示。给一个点了就 404 的菜单项
  // 比不给还糟——用户会以为是自己环境坏了。
  const v2Entry = $("ddV2Console");
  if (v2Entry) v2Entry.style.display = s.v2_console ? "" : "none";
  renderLicenseBanner(s.license);
  TERMINAL_ENABLED = s.terminal_enabled !== false;
  if (typeof DESKTOP_ENABLED !== "undefined") DESKTOP_ENABLED = s.desktop_enabled !== false;
}

/* ---------- 渲染：授权状态横幅 ----------
   过期/超限不是"提示"，是平台行为已经变了：新主机注册会被拒、写操作会降级为只读。
   只在需要人动手时出现——自建/开源模式（未开启强制）下没装授权是正常形态，不打扰。 */
function renderLicenseBanner(lic) {
  const el = $("licenseBanner");
  if (!el) return;
  if (!lic || !lic.state || lic.state === "active" || (lic.state === "unlicensed" && !lic.enforced)) {
    el.style.display = "none";
    el.textContent = "";
    return;
  }
  const key = "license.banner_" + lic.state;
  const txt = I18N.t(key);
  const severe = lic.state === "expired" || lic.state === "invalid";
  el.className = "lic-banner" + (severe ? " crit" : "");
  const used = (lic.used_hosts || 0) + "/" + (lic.max_hosts ? lic.max_hosts : "∞");
  el.textContent = (txt === key ? "" : txt) + "（" + I18N.t("license.hosts") + " " + used + "）";
  el.style.display = "";
}

/* ---------- 渲染：统计与健康小结 ---------- */
function renderStatsHealth(s) {
  const grid = $("statsGrid");
  if (!grid) return;
  const total = s.total_hosts || 0;
  const online = s.online_hosts || 0;
  const rate = total > 0 ? Math.round(online / total * 100) : 0;
  const allAlerts = (s.critical_alerts || 0) + (s.warning_alerts || 0);
  const healthy = (s.critical_alerts || 0) === 0;
  const vals = [
    { cls: "", val: String(total), key: I18N.t("ui.total_hosts"), hint: "" },
    { cls: rate >= 80 ? "ok" : rate >= 50 ? "warn" : "crit", val: rate + "%", key: I18N.t("section.online_rate"), hint: online + "/" + total + " " + I18N.t("ui.online") },
    { cls: healthy ? "ok" : "crit", val: healthy ? I18N.t("section.health_ok") : I18N.t("section.health_error"), key: I18N.t("section.health_status"), hint: !healthy ? I18N.t("section.unprocessed_alerts") + ": " + (s.critical_alerts || 0) : "" },
    { cls: allAlerts > 0 ? "warn" : "ok", val: String(allAlerts), key: I18N.t("section.total_alerts"), hint: I18N.t("ui.critical_alerts") + ": " + (s.critical_alerts || 0) + " / " + I18N.t("ui.warning") + ": " + (s.warning_alerts || 0) },
  ];
  const cards = grid.querySelectorAll(".stat-card");
  if (cards.length === vals.length) {
    cards.forEach((card, i) => {
      const spec = vals[i];
      const sv = card.querySelector(".sv");
      const sk = card.querySelector(".sk");
      let sh = card.querySelector(".sh");
      if (sv) { sv.className = "sv " + spec.cls; sv.textContent = spec.val; }
      if (sk) sk.textContent = spec.key;
      if (spec.hint) {
        if (!sh) { sh = document.createElement("div"); sh.className = "sh"; card.appendChild(sh); }
        sh.textContent = spec.hint;
      } else if (sh) {
        sh.remove();
      }
    });
  } else {
    const sc = (cls, val, key, hint) =>
      `<div class="stat-card"><div class="sv ${cls}">${val}</div><div class="sk">${key}</div>${hint ? `<div class="sh">${hint}</div>` : ""}</div>`;
    grid.innerHTML =
      sc(vals[0].cls, vals[0].val, vals[0].key, vals[0].hint) +
      sc(vals[1].cls, vals[1].val, vals[1].key, vals[1].hint) +
      sc(vals[2].cls, vals[2].val, vals[2].key, vals[2].hint) +
      sc(vals[3].cls, vals[3].val, vals[3].key, vals[3].hint);
  }
  // 更新节头徽章（在线率摘要）
  const badge = $("statsHealthBadge");
  if (badge) badge.textContent = I18N.t("section.online_rate") + " " + rate + "%";
}

/* ---------- 渲染：告警 / 事件 ---------- */
const ALERT_TYPES = [
  {key:"", label:I18N.t("ui.all")}, {key:"cpu", label:"CPU"}, {key:"memory", label:I18N.t("ui.memory")},
  {key:"disk", label:I18N.t("ui.disk")}, {key:"gpu", label:"GPU"}, {key:"load", label:I18N.t("ui.load")},
  {key:"conn", label:I18N.t("section.connections")},
  {key:"hardware", label:I18N.t("nav.hardware") || "硬件"},
  {key:"offline", label:I18N.t("ui.offline_status")}, {key:"check", label:I18N.t("ui.probe")}
];
/*
 * diffUpdateList — 概览列表差量更新引擎
 * 避免每 3 秒轮询全量 innerHTML 重建 DOM 导致的闪烁和布局跳动。
 * 策略：
 *   1. 生成新数据的签名摘要，若与上次完全一致则跳过更新（最常见路径）
 *   2. 签名不同时，按 key 逐行比对：保留匹配行、插入新行、标记多余行为 leaving
 *   3. 空列表 / 首次渲染走 innerHTML 快速路径
 *   4. 延迟移除机制：即将消失的行不立即删除，而是标记 .row-leaving 并设置
 *      5 秒宽限期。若同一 key 在下一次轮询中重新出现，则取消移除并复用节点。
 *      这解决了服务端 Evaluate() 无状态评估导致指标在阈值边界波动时
 *      同一告警时有时无、DOM 节点反复增删的闪烁问题。
 * 注意：匹配行不做 innerHTML 替换——时间相关的动态文本
 * （如“已持续 3 分钟”）由 refreshAlertRowTimes() 单独更新 textContent
 */
const DIFF_GRACE_MS = 5000; // 延迟移除宽限期：5 秒（覆盖 2 个 3 秒轮询周期）
function diffUpdateList(container, items, rowFn, keyFn, emptyHTML) {
  if (!container) return;
  const now = Date.now();
  // 1. 清理已过期的 leaving 行
  container.querySelectorAll(".row-leaving").forEach(el => {
    if (parseInt(el.dataset.leavingAt || "0") <= now) el.remove();
  });
  // 2. 快速路径：空列表
  if (!items.length) {
    // 若仍有 leaving 行在宽限期内，暂不显示“空”消息
    const leavingCount = container.querySelectorAll(".row-leaving").length;
    if (!leavingCount) {
      if (container.dataset.sig !== "empty") {
        container.innerHTML = emptyHTML;
        container.dataset.sig = "empty";
      }
    }
    return;
  }
  // 3. 签名检查：数据未变则完全跳过 DOM 操作
  const sig = items.map(keyFn).join("\n");
  if (container.dataset.sig === sig) return;
  container.dataset.sig = sig;
  // 4. 首次渲染或容器为空（无 data-key 行且无 leaving 行）
  const existing = container.querySelectorAll("[data-key]");
  if (!existing.length) {
    container.innerHTML = items.map(rowFn).join("");
    return;
  }
  // 5. 差量更新：按 key 匹配复用 DOM 节点
  const oldMap = {};
  existing.forEach(el => { oldMap[el.dataset.key] = el; });
  const newKeys = items.map(keyFn);
  const newKeySet = {};
  newKeys.forEach(k => { newKeySet[k] = true; });
  // 5a. 标记不再存在的行为 leaving（不立即删除）
  existing.forEach(el => {
    if (!newKeySet[el.dataset.key] && !el.classList.contains("row-leaving")) {
      el.classList.add("row-leaving");
      el.dataset.leavingAt = String(now + DIFF_GRACE_MS);
    }
  });
  // 5b. 插入/更新新行
  let refNode = null;
  for (let i = items.length - 1; i >= 0; i--) {
    const key = newKeys[i];
    let el = oldMap[key];
    if (el) {
      // 匹配行：取消任何待移除状态，不做 innerHTML 替换
      el.classList.remove("row-leaving", "row-enter");
      delete el.dataset.leavingAt;
    } else {
      // 新行：创建并插入到正确位置
      el = document.createElement("div");
      el.innerHTML = rowFn(items[i]).trim();
      el = el.firstChild;
      el.classList.add("row-enter");
    }
    // 确保 DOM 顺序与数据顺序一致
    if (refNode && el.nextSibling !== refNode) {
      container.insertBefore(el, refNode);
    } else if (!refNode && el !== container.firstChild) {
      container.insertBefore(el, container.firstChild);
    }
    refNode = el;
  }
}
/* refreshAlertRowTimes — 轻量级更新告警行的时间相关文本
 * 仅通过 textContent 更新“已持续 X 分”和绝对时间显示，
 * 不触及 innerHTML，不触发 DOM 重建和重排。 */
function refreshAlertRowTimes(container, now) {
  if (!container) return;
  container.querySelectorAll(".alert-dur[data-since]").forEach(el => {
    const since = parseInt(el.dataset.since);
    if (since) el.textContent = I18N.t("section.duration") + " " + fmtDur(now - since);
  });
}
function renderAlerts(alerts) {
  LAST_ALERTS = alerts;
  const n = alerts.length;
  $("alertsCount").textContent = n; $("navAlerts").textContent = n; $("ovAlertsCount").textContent = n;
  const now = Math.floor(Date.now() / 1000);
  // alertKey 使用稳定身份字段：type + scope + hostname + host_id
  // 这些字段在告警的生命周期内不会变化，不受 message 文本变化
  // （如拨测错误详情不同）或 since 重置（告警闪烁后重新计时）影响。
  // 这确保 diffUpdateList 能正确复用 DOM 节点，避免不必要的增删。
  const alertKey = a => {
    const type = a.type || "";
    const scope = a.scope || "";
    const id = a.host_id || "";
    if (type || scope || id) {
      return `${type}|${scope}|${a.hostname}|${id}`;
    }
    // Fallback: 仅当 type/scope/host_id 均缺失时使用 message（不应发生）
    return `${a.hostname}|${a.message}|${a.level}`;
  };
  const row = a => {
    const dur = a.since ? I18N.t("section.duration") + " " + fmtDur(now - a.since) : "";
    const ipStr = a.ip ? `<span class="alert-ip mono">${esc(a.ip)}</span>` : "";
    // 分组路径：同名主机在不同机房是常态，只看主机名判断不了"这是哪一摊的机器"。
    const groupStr = a.group ? `<span class="alert-group" title="${esc(a.group)}">${esc(a.group)}</span>` : "";
    const timeStr = a.timestamp ? `<span class="alert-time mono">${fmtDateTime(a.timestamp)}</span>` : "";
    // dur 包装在 .alert-dur[data-since] 中，供 refreshAlertRowTimes 轻量更新
    const durSpan = a.since
      ? `<span class="src alert-dur" data-since="${a.since}" title="${I18N.t("section.first_fired")} ${fmtDateTime(a.since)}">${dur}</span>`
      : "";
    // 告警状态标签与操作按钮
    let statusBadge = "", actions = "";
    const hid = esc(a.host_id || ""), atyp = esc(a.type || ""), asc = esc(a.scope || "");
    const actAttrs = `data-host="${hid}" data-type="${atyp}" data-scope="${asc}"`;
    if (a.status === "acknowledged") {
      statusBadge = `<span class="badge status-badge status-ack">${I18N.t("alert.acknowledged")}</span>`;
      actions = `<button class="alert-action" data-action="clear" ${actAttrs} title="${I18N.t("alert.clear_status")}">↩</button>`;
    } else if (a.status === "silenced") {
      statusBadge = `<span class="badge status-badge status-silence">${I18N.t("alert.silenced")}</span>`;
      actions = `<button class="alert-action" data-action="clear" ${actAttrs} title="${I18N.t("alert.clear_status")}">↩</button>`;
    } else {
      actions = `<button class="alert-action" data-action="ack" ${actAttrs} title="${I18N.t("alert.acknowledge")}">✔</button>` +
        `<button class="alert-action" data-action="silence" ${actAttrs} title="${I18N.t("alert.silence")}">🔇</button>`;
    }
    const statusClass = a.status ? ` status-${esc(a.status)}` : "";
    return `<div class="row-item ${esc(a.level)}${statusClass}" tabindex="0" data-key="${esc(alertKey(a))}">
    <span class="badge ${esc(a.level)}">${a.level === "critical" ? I18N.t("ui.critical") : a.level === "info" ? I18N.t("toast.recovered") : I18N.t("ui.warning")}</span>
    ${statusBadge}
    <strong>${esc(a.hostname)}</strong>${groupStr}${ipStr}<span class="msg">${esc(a.message)}</span>
    ${durSpan}
    ${timeStr}
    <span class="alert-actions">${actions}</span></div>`;
  };
  // Apply filters
  let filtered = alerts;
  if (ALERT_TYPE) filtered = filtered.filter(a => a.type === ALERT_TYPE);
  if (ALERT_SEARCH) filtered = filtered.filter(a => {
    const hay = [a.hostname, a.ip, a.group, a.message, a.host_id, a.type, a.scope, a.level].filter(Boolean).join(" ");
    return matchesSearchTokens(hay, ALERT_SEARCH);
  });
  const empty = `<div class="empty-line">✅ ${I18N.t("empty.no_alerts")}</div>`;
  const filterEmpty = `<div class="empty-line">${I18N.t("empty.no_alerts_filtered")}</div>`;
  // 告警页与概览迷你列表共用差量更新，避免全量 innerHTML 闪烁。
  // 只画当前页：生产环境的告警面是上万条，全量进 DOM 就是把标签页冻住。
  ALERT_PAGE = clampPageNo(ALERT_PAGE, filtered.length, ALERT_PAGE_SIZE);
  const alertPages = Math.max(1, Math.ceil(filtered.length / ALERT_PAGE_SIZE));
  const pageAlerts = filtered.slice((ALERT_PAGE - 1) * ALERT_PAGE_SIZE, ALERT_PAGE * ALERT_PAGE_SIZE);
  diffUpdateList($("alerts"), pageAlerts, row, alertKey, n ? filterEmpty : empty);
  refreshAlertRowTimes($("alerts"), now);
  const alertsPager = $("alertsPager");
  if (alertsPager) alertsPager.innerHTML = filtered.length ? pagerHTML(ALERT_PAGE, alertPages, filtered.length, I18N.t("section.pager_items", "条")) : "";
  // 概览页告警列表：差量更新，避免全量 innerHTML 重建导致闪烁
  diffUpdateList($("ovAlerts"), alerts.slice(0, 6), row, alertKey, empty);
  // 轻量级更新“已持续”相对时间文本（仅 textContent，不重建 DOM）
  refreshAlertRowTimes($("ovAlerts"), now);
}

/* ---------- 概览：资源 TOP10 排行榜（多面板横向条形图） ---------- */
let _topPanelsSig = "";
function renderTop(hosts) {
  const el = $("topPanels");
  if (!el) return;
  const live = hosts.filter(h => h.latest && h.online);

  if (!live.length) {
    if (_topPanelsSig !== "empty") {
      _topPanelsSig = "empty";
      el.innerHTML = `<div class="top-empty">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><rect x="3" y="3" width="18" height="18" rx="2"/><line x1="3" y1="9" x2="21" y2="9"/><line x1="9" y1="21" x2="9" y2="9"/></svg>
      <span>${I18N.t("empty.no_online_hosts")}</span>
    </div>`;
    }
    return;
  }

  // GPU panel visibility: show if ANY host has GPU data (even if utilization
  // is 0% — idle GPUs should still appear). The old filter required
  // util_percent > 0 which caused the panel to flicker on/off as GPUs idled.
  const hasGPU = live.some(h => { const gs = (h.latest.gpus || []); return gs.length > 0; });
  const diskMax = m => { const d = m.disks || []; return d.length ? Math.max(...d.map(x => x.percent)) : (m.disk_percent || 0); };
  const gpuMax = m => { const g = m.gpus || []; return g.length ? Math.max(...g.map(x => x.util_percent || 0)) : 0; };
  const netTotal = m => (m.net_sent_rate || 0) + (m.net_recv_rate || 0);
  const iopsTotal = m => (m.disk_read_iops || 0) + (m.disk_write_iops || 0);

  // GPU host filter: include any host that has GPU data (gs.length > 0).
  const gpuLive = live.filter(h => {
    const gs = (h.latest.gpus || []);
    return gs.length > 0;
  });

  // 面板定义：[key, title, unit, valueFn, isPct, displayFn]
  const panels = [
    { key: "cpu", title: I18N.t("section.cpu_top10"), unit: "%", fn: m => m.cpu_percent || 0, isPct: true },
    ...(hasGPU ? [{ key: "gpu", title: I18N.t("section.gpu_top10"), unit: "%", fn: gpuMax, isPct: true, dataSource: gpuLive }] : []),
    { key: "mem", title: I18N.t("section.mem_top10"), unit: "%", fn: m => m.mem_percent || 0, isPct: true },
    { key: "disk", title: I18N.t("section.disk_top10"), unit: "%", fn: diskMax, isPct: true },
    { key: "diskio", title: I18N.t("section.diskio_top10"), unit: "%", fn: m => m.disk_io_util_percent || 0, isPct: true },
    { key: "iops", title: I18N.t("section.iops_top10"), unit: I18N.t("unit.iops"), fn: iopsTotal, isPct: false },
    { key: "net", title: I18N.t("section.net_top10"), unit: I18N.t("unit.mbps"), fn: netTotal, isPct: false },
    { key: "load", title: I18N.t("section.load_top10"), unit: "", fn: m => m.load5 || 0, isPct: false },
    { key: "proc", title: I18N.t("section.proc_top10"), unit: "", fn: m => m.proc_count || 0, isPct: false },
  ];

  const topN = (arr, fn, n) => arr.slice().sort((a, b) => fn(b.latest) - fn(a.latest)).slice(0, n);

  // Signature: panel keys + top-N hostId:roundedValue — skip DOM when unchanged.
  const checksSig = (typeof LAST_CHECKS !== "undefined" && Array.isArray(LAST_CHECKS))
    ? LAST_CHECKS.filter(c => !c.builtin).map(c => `${c.id}:${c.last_ok}:${c.last_latency_ms||0}`).join("|")
    : "";
  const dataSig = panels.map(panel => {
    const source = panel.dataSource || live;
    return panel.key + "=" + topN(source, panel.fn, 10).map(h => {
      const v = panel.fn(h.latest);
      const rv = panel.isPct || panel.key === "load" ? (Math.round(v * 10) / 10) : Math.round(v);
      return `${h.id}:${rv}`;
    }).join(",");
  }).join(";") + "#chk:" + checksSig;
  if (dataSig === _topPanelsSig && el.querySelector(".top-panel")) {
    // Patch bar widths / values in place when structure matches.
    panels.forEach(panel => {
      const source = panel.dataSource || live;
      const sorted = topN(source, panel.fn, 10);
      if (!sorted.length) return;
      const maxVal = Math.max(1, ...sorted.map(h => panel.fn(h.latest)));
      const panelEl = el.querySelector(`.top-panel[data-top-key="${panel.key}"]`);
      if (!panelEl) return;
      sorted.forEach((h, idx) => {
        const item = panelEl.querySelector(`.top-item[data-id="${cssEsc(h.id)}"]`);
        if (!item) return;
        const v = panel.fn(h.latest);
        const pct = panel.isPct ? v : Math.min(100, v / maxVal * 100);
        const width = Math.max(3, pct);
        const color = panel.key === "net" ? "var(--info)" : usageColor(panel.isPct ? v : pct);
        let disp;
        if (panel.isPct) disp = v.toFixed(1) + "%";
        else if (panel.key === "net") disp = fmtRate(v);
        else if (panel.key === "iops") disp = fmtIOPS(v) + " " + I18N.t("unit.iops");
        else if (panel.key === "load") disp = v.toFixed(2);
        else if (panel.key === "proc") disp = v.toFixed(0);
        else disp = v.toFixed(1);
        const fill = item.querySelector(".top-bar-fill");
        if (fill) { fill.style.width = width + "%"; fill.style.background = color; }
        const valEl = item.querySelector(".top-val");
        if (valEl) valEl.textContent = disp;
        item.title = `${hostDisplayTitle(h)} · ${disp}`;
      });
    });
    return;
  }
  _topPanelsSig = dataSig;

  function renderPanel(panel) {
    const source = panel.dataSource || live;
    const sorted = topN(source, panel.fn, 10);
    if (!sorted.length) {
      return `<div class="top-panel" data-top-key="${esc(panel.key)}">
        <div class="top-title">${esc(panel.title)}<span class="top-unit">${esc(panel.unit)}</span></div>
        <div class="top-empty">${I18N.t("empty.no_data")}</div>
      </div>`;
    }
    const maxVal = Math.max(1, ...sorted.map(h => panel.fn(h.latest)));
    const items = sorted.map((h, idx) => {
      const v = panel.fn(h.latest);
      const pct = panel.isPct ? v : Math.min(100, v / maxVal * 100);
      const width = Math.max(3, pct);
      const color = panel.key === "net" ? "var(--info)" : usageColor(panel.isPct ? v : pct);
      let disp;
      if (panel.isPct) disp = v.toFixed(1) + "%";
      else if (panel.key === "net") disp = fmtRate(v);
      else if (panel.key === "iops") disp = fmtIOPS(v) + " " + I18N.t("unit.iops");
      else if (panel.key === "load") disp = v.toFixed(2);
      else if (panel.key === "proc") disp = v.toFixed(0);
      else disp = v.toFixed(1);
      return `<div class="top-item" tabindex="0" data-id="${esc(h.id)}" data-name="${esc(hostDisplayTitle(h))}" title="${esc(hostDisplayTitle(h))} · ${esc(disp)}">
        <span class="top-name">${esc(hostDisplayTitle(h))}</span>
        <div class="top-bar"><div class="top-bar-fill" style="width:${width}%;background:${color}"></div></div>
        <span class="top-val mono">${esc(disp)}</span>
      </div>`;
    }).join("");
    return `<div class="top-panel" data-top-key="${esc(panel.key)}">
      <div class="top-title">${esc(panel.title)}<span class="top-unit">${esc(panel.unit)}</span></div>
      ${items}
    </div>`;
  }

  let html = panels.map(renderPanel).join("");

  // 监控探针面板
  const checksHtml = checkTopPanels();
  if (checksHtml) {
    html += `<div class="top-checks-panel">
      <div class="top-checks-title">${I18N.t("section.custom_monitor")}</div>
      ${checksHtml}
    </div>`;
  }

  el.innerHTML = html;
}

// 监控 TOP10，顺序 Ping → TCP → HTTP → 进程；无该类型监控则该面板不显示
function checkTopPanels() {
  const checks = (Array.isArray(LAST_CHECKS) ? LAST_CHECKS : []).filter(c => !c.builtin);
  if (!checks.length) return "";
  const byType = t => checks.filter(c => c.type === t);
  const panels = checkTopPanel(I18N.t("section.ping_top10"), byType("ping"), false)
    + checkTopPanel(I18N.t("section.tcp_top10"), byType("tcp"), false)
    + checkTopPanel(I18N.t("section.http_top10"), byType("http"), false)
    + checkTopPanel(I18N.t("section.process_top10"), byType("process"), true);
  return panels ? `<div class="checks-row">${panels}</div>` : "";
}
function checkTopPanel(title, list, isProc) {
  if (!list.length) return "";
  const sorted = list.slice().sort((a, b) => {
    const ad = (!a.ok && a.checked_at) ? 1 : 0, bd = (!b.ok && b.checked_at) ? 1 : 0;
    if (ad !== bd) return bd - ad;
    return (b.latency_ms || 0) - (a.latency_ms || 0);
  }).slice(0, 10);
  const maxLat = Math.max(1, ...sorted.map(c => c.latency_ms || 0));
  const items = sorted.map(c => {
    const down = !c.ok && c.checked_at, unknown = !c.checked_at;
    let val, color, width;
    if (isProc) {
      val = down ? I18N.t("ui.abnormal") : unknown ? I18N.t("ui.pending") : I18N.t("ui.normal");
      color = down ? "var(--crit)" : unknown ? "var(--muted2)" : "var(--ok)";
      width = unknown ? 0 : 100;
    } else if (down) { val = I18N.t("ui.abnormal"); color = "var(--crit)"; width = 100; }
    else if (unknown) { val = I18N.t("ui.pending"); color = "var(--muted2)"; width = 0; }
    else {
      const lat = Math.round(c.latency_ms || 0);
      val = lat + " " + I18N.t("unit.ms"); color = lat >= 1000 ? "var(--crit)" : lat >= 300 ? "var(--warn)" : "var(--ok)";
      width = Math.min(100, (c.latency_ms || 0) / maxLat * 100);
    }
    return `<div class="checks-item" data-check-id="${esc(c.id)}" data-check-name="${esc(c.name)}" data-check-type="${esc(c.type)}" title="${I18N.t("section.click_history")}">
      <span class="checks-name">${esc(c.name)}</span>
      <div class="checks-bar"><div class="checks-bar-fill" style="width:${width}%;background:${color}"></div></div>
      <span class="checks-val mono" style="color:${color}">${val}</span>
    </div>`;
  }).join("");
  return `<div class="checks-panel"><div class="checks-title">${title}</div>${items}</div>`;
}

// applyLogFilters mirrors the log view's filter chain (类型/级别/时间/内部自检)，
// 供渲染与 CSV 导出共用，保证导出内容与所见一致。
function applyLogFilters(items) {
  let filtered = items;
  if (LOG_KIND) filtered = filtered.filter(e => e.kind === LOG_KIND);
  if (LOG_LEVEL && LOG_LEVEL !== "all") filtered = filtered.filter(e => e.level === LOG_LEVEL);
  if (LOG_TIME_RANGE && LOG_TIME_RANGE !== "all") {
    const now = Math.floor(Date.now() / 1000);
    const hours = parseInt(LOG_TIME_RANGE);
    filtered = filtered.filter(e => (now - e.timestamp) <= hours * 3600);
  }
  if (LOG_SEARCH) {
    filtered = filtered.filter(e => matchesSearchTokens(
  [e.message, e.username, e.actor, e.host, e.kind, e.level, e.ip].filter(Boolean).join(" "),
      LOG_SEARCH
    ));
  }
  // Filter out internal alert engine logs (actor="告警引擎" from backend)
  return filtered.filter(e => e.actor !== I18N.t("notify.alert_engine"));
}

function exportLogsCSV() {
  const rows = applyLogFilters(LAST_LOG);
  if (!rows.length) { toast(I18N.t("empty.no_log_export"), "err"); return; }
  // 日志正文来自 Agent / 被监控主机：以 = 开头的一行日志能在打开导出的人机器上执行。
  const head = ["时间", "类型", "级别", "用户名", "操作者", "IP", "主机", "内容"];
  const csv = expRowsToCsv(head, rows.map(e => [
    fmtDateTime(e.timestamp), translateLogKind(e.kind), translateLogLevel(e.level),
    e.username || "", e.actor || "", e.ip || "", formatLogHost(e), redactLogMessage(e.message || "")
  ]));
  const blob = new Blob([EXP_CSV_BOM + csv], { type: "text/csv;charset=utf-8" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = `AIOps-logs-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.csv`;
  a.click();
  URL.revokeObjectURL(a.href);
  toast(`${I18N.t("toast.exported")} ${rows.length} ${I18N.t("time.records")}${I18N.t("ui.log")}`, "ok");
}

function renderLog(items) {
  LAST_LOG = items;
  const n = items.length;
  $("logCount").textContent = n; $("navLog").textContent = n;
  const kcls = k => k === "operation" ? "op" : k === "system" ? "sys" : k === "terminal" ? "term" : "plg";
  const logKey = e => `${e.kind}|${e.message}|${e.level}|${e.timestamp||0}|${e.username||e.actor||""}|${e.host||""}`;
  const row = e => {
    const user = String(e.username || (!looksLikeLogIP(e.actor) ? e.actor : "") || "—");
    // 右侧固定为访问者（操作发起方）IP，主机已在内容列展示
    const visitorIP = String(e.ip || (looksLikeLogIP(e.actor) ? e.actor : "") || "").trim() || "—";
    const msg = redactLogMessage(e.message || "");
    return `<div class="row-item log-row ${esc(e.level)}" data-key="${esc(logKey(e))}">
    <span class="kind ${kcls(e.kind)}">${esc(translateLogKind(e.kind))}</span>
    <span class="msg" title="${esc(msg)}">${esc(msg)}</span>
    <span class="log-user" title="${esc(user)}">${esc(user)}</span>
    <span class="src log-ip mono" title="${esc(I18N.t("ui.visitor_ip", "访问者 IP") + ": " + visitorIP)}">${esc(visitorIP)}</span>
    <span class="log-time mono">${fmtDateTime(e.timestamp)}</span></div>`;
  };
  
  const filtered = applyLogFilters(items);
  const total = filtered.length;
  const pages = Math.max(1, Math.ceil(total / LOG_PAGE_SIZE));
  if (LOG_PAGE > pages) LOG_PAGE = pages;
  if (LOG_PAGE < 1) LOG_PAGE = 1;
  const pageItems = filtered.slice((LOG_PAGE - 1) * LOG_PAGE_SIZE, LOG_PAGE * LOG_PAGE_SIZE);
  $("log").innerHTML = pageItems.length ? pageItems.map(row).join("") : `<div class="empty-line">${I18N.t("empty.no_logs")}</div>`;
  renderLogPager(pages, total);
}

function looksLikeLogIP(s) {
  s = String(s || "").trim();
  if (!s) return false;
  if (/^\d{1,3}(\.\d{1,3}){3}(:\d+)?$/.test(s)) return true;
  return s.includes(":") && !/\s/.test(s);
}

function formatLogHost(e) {
  const raw = String((e && e.host) || "").trim();
  if (!raw) return "";
  // Already hostname (ip)?
  if (/\([^)]+\)/.test(raw) || !/^[0-9a-fA-F]{8,}$/.test(raw)) {
    if (typeof HostPicker !== "undefined" && HostPicker.displayHost && /^[0-9a-fA-F]{8,}$/.test(raw)) {
      return HostPicker.displayHost(raw);
    }
    return raw;
  }
  if (typeof HostPicker !== "undefined" && HostPicker.displayHost) return HostPicker.displayHost(raw);
  return I18N.t("ui.unknown_host", "未知主机");
}

function redactLogMessage(msg) {
  let t = String(msg || "");
  const hosts = (typeof HOSTS !== "undefined" && Array.isArray(HOSTS)) ? HOSTS
    : ((typeof HOST_META !== "undefined" && Array.isArray(HOST_META)) ? HOST_META : []);
  // Longer ids first
  const list = hosts.filter(h => h && h.id).slice().sort((a, b) => String(b.id).length - String(a.id).length);
  list.forEach(h => {
    const lab = (typeof hostDisplayTitle === "function") ? hostDisplayTitle(h)
      : ((typeof HostPicker !== "undefined" && HostPicker.hostTitle) ? HostPicker.hostTitle(h) : (h.hostname || "未知主机"));
    if (!lab) return;
    t = t.split(h.id).join(lab);
    if (h.id.length >= 8) t = t.split(h.id.slice(0, 8)).join(lab);
  });
  t = t.replace(/\bhermes(?:\s+agent)?\b/gi, "智能运维服务");
  t = t.replace(/\bbreak-glass\b/gi, "紧急放行");
  t = t.replace(/\b[0-9a-fA-F]{8,32}\b/g, "未知主机");
  return t;
}

function renderLogPager(pages, total) {
  const pager = $("logPager");
  if (!pager) return;
  if (total === 0) { pager.innerHTML = ""; return; }
  if (pages <= 1) { pager.innerHTML = `<span class="pinfo">${I18N.t("ui.matched")}${total} ${I18N.t("time.records")}</span>`; return; }
  let btns = `<button ${LOG_PAGE === 1 ? "disabled" : ""} data-lpg="prev">‹</button>`;
  for (let i = 1; i <= pages; i++) {
    if (i === 1 || i === pages || Math.abs(i - LOG_PAGE) <= 1) {
      btns += `<button class="${i === LOG_PAGE ? "active" : ""}" data-lpg="${i}">${i}</button>`;
    } else if (Math.abs(i - LOG_PAGE) === 2) {
      btns += `<span class="pinfo">…</span>`;
    }
  }
  btns += `<button ${LOG_PAGE === pages ? "disabled" : ""} data-lpg="next">›</button>`;
  btns += `<span class="pinfo">${I18N.t("ui.matched")}${total} ${I18N.t("time.records")} · ${LOG_PAGE}/${pages}${I18N.t("time.page_suffix")}</span>`;
  pager.innerHTML = btns;
}

// 每页条数切换（10/30/50/100）
function setLogPageSize(v) {
  LOG_PAGE_SIZE = parseInt(v) || 50;
  LOG_PAGE = 1;
  renderLog(LAST_LOG);
}

