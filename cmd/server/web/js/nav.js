/* ---------- 主循环 ---------- */
let LAST_FAVICON_CRIT = -1;
function updateFavicon(critCount) {
  // 仅在严重告警数变化时重绘图标：避免每次轮询/推送都做 canvas 分配 + toDataURL 编码占用主线程。
  if (critCount === LAST_FAVICON_CRIT) return;
  LAST_FAVICON_CRIT = critCount;
  const canvas = document.createElement("canvas");
  canvas.width = 16; canvas.height = 16;
  const ctx = canvas.getContext("2d");
  ctx.fillStyle = "#0a0d13"; ctx.fillRect(0, 0, 16, 16);
  ctx.fillStyle = "#4c8dff"; ctx.font = "bold 9px sans-serif"; ctx.textAlign = "center"; ctx.textBaseline = "middle";
  ctx.fillText("AI", 8, 8.5);
  if (critCount > 0) {
    ctx.fillStyle = "#ef4d5a"; ctx.beginPath(); ctx.arc(13, 3, 3.5, 0, Math.PI * 2); ctx.fill();
    ctx.fillStyle = "#fff"; ctx.font = "bold 5px sans-serif"; ctx.fillText(critCount > 9 ? "9+" : String(critCount), 13, 3.5);
  }
  let link = document.querySelector("link[rel=icon]");
  if (!link) { link = document.createElement("link"); link.rel = "icon"; document.head.appendChild(link); }
  link.href = canvas.toDataURL();
}
let _refreshPromise = null;
let _lastAlertsActivityAt = 0;
const REFRESH_CORE_VIEWS = new Set(["overview", "hosts", "alerts", "log", "checks", "forward"]);
const ALERTS_ACTIVITY_TTL_MS = 45000;

async function refresh(force) {
  if (_refreshPromise && !force) return _refreshPromise;
  const run = (async () => {
  try {
    const activeView = document.querySelector(".view.active")?.id.replace("view-", "") || "overview";
    const coreView = REFRESH_CORE_VIEWS.has(activeView);
    const needAlertsActivity = force || coreView ||
      (Date.now() - _lastAlertsActivityAt > ALERTS_ACTIVITY_TTL_MS);

    const rs = await fetch(`${API}/summary`);
    if (rs.status === 401) { $("loginView").classList.add("show"); return; }
    const s = await rs.json();

    const fetches = [
      fetch(`${API}/hosts`, { credentials: "same-origin" }).then(r => r.json())
    ];
    if (needAlertsActivity) {
      fetches.push(
        fetch(`${API}/alerts`, { credentials: "same-origin" }).then(r => r.json()),
        fetch(`${API}/activity`, { credentials: "same-origin" }).then(r => r.json())
      );
    }
    const results = await Promise.all(fetches);
    const hostsRaw = results[0];
    let alertsRaw = needAlertsActivity ? results[1] : (typeof LAST_ALERTS !== "undefined" ? LAST_ALERTS : []);
    let activityRaw = needAlertsActivity ? results[2] : (typeof LAST_LOG !== "undefined" ? LAST_LOG : []);
    if (needAlertsActivity) _lastAlertsActivityAt = Date.now();

    await loadHostFolders();
    // P0-#2: Connection state feedback
    if (FIRST_LOAD) { FIRST_LOAD = false; }
    if (CONN_STATE !== "connected") {
      if (CONN_STATE === "disconnected") toast(I18N.t("toast.reconnected"), "ok");
      CONN_STATE = "connected";
    }
    // 性能优化：按当前活动视图只渲染其拥有的 DOM；非核心视图降载 alerts/activity 拉取。
    const onOverview = activeView === "overview";
    const hosts = typeof syncHostCache === "function" ? syncHostCache(hostsRaw) : normalizeHostsPayload(hostsRaw);
    const alerts = Array.isArray(alertsRaw) ? alertsRaw : [];
    const activity = Array.isArray(activityRaw) ? activityRaw : [];
    const setTxt = (id, v) => { const e = $(id); if (e) e.textContent = v; };
    setTxt("navHosts", hosts.length); setTxt("hostsCount", hosts.length);
    setTxt("navAlerts", alerts.length); setTxt("alertsCount", alerts.length); setTxt("ovAlertsCount", alerts.length);
    setTxt("navLog", activity.length);
    if (onOverview) { renderCards(s); renderStatsHealth(s); renderTop(hosts); }
    if (onOverview || activeView === "alerts") renderAlerts(alerts);
    if ((onOverview || activeView === "log") && (needAlertsActivity || activeView === "log")) renderLog(activity);
    if (activeView === "hosts") renderHosts(hosts);
    updateFavicon(s.critical_alerts || 0);
    notifyCriticalAlerts(s.critical_alerts || 0);
    const pulseEl = $("pulse"); if (pulseEl) pulseEl.className = "pulse";
  } catch (e) {
    const pulseEl = $("pulse"); if (pulseEl) pulseEl.className = "pulse off";
    if (CONN_STATE === "connected") {
      CONN_STATE = "disconnected";
      toast(I18N.t("ui.reconnecting"), "err");
    }
  }
  })();
  _refreshPromise = run.finally(() => { if (_refreshPromise === run) _refreshPromise = null; });
  return _refreshPromise;
}

/* ---------- 事件绑定（委托） ---------- */
const groupsEl = $("groups");
// 折叠功能已临时停用：移除 group-head 点击事件委托中的 toggleCatCollapse 逻辑
if (groupsEl) {
  groupsEl.addEventListener("click", e => {
    const host = e.target.closest(".host"); if (!host) return;
    const act = e.target.closest("[data-act]");
    const { id, name, cat } = host.dataset;
    if (act) {
      if (act.dataset.act === "detail") openDetail(id, name);
      else if (act.dataset.act === "cat") editCategory(id, cat);
      else if (act.dataset.act === "del") delHost(id, name);
      else if (act.dataset.act === "term") openTerminal(id, name);
      else if (act.dataset.act === "desktop") openDesktop(id, name);
      else if (act.dataset.act === "agent-sel" || act.dataset.act === "agent-sel-wrap") {
        e.stopPropagation();
        return;
      }
    } else {
      // 点击主机卡片/行内任意非操作按钮区域（进度条、负载、底部等）→ 打开详情
      openDetail(id, name);
    }
  });
}

/* ---------- Multi-select category dropdown ---------- */
function getSelectedCats() {
  try { const s = localStorage.getItem("aiops_cats"); if (s) return JSON.parse(s); } catch (e) {}
  return [];
}
function setSelectedCats(arr) {
  CUR_CATS = arr;
  try { localStorage.setItem("aiops_cats", JSON.stringify(arr)); } catch (e) {}
}
function catCollapsed(cat) {
  try { const s = localStorage.getItem("aiops_collapsed"); if (s) { const arr = JSON.parse(s); return arr.includes(cat); } } catch (e) {}
  return false;
}
function toggleCatCollapse(cat) {
  let arr = [];
  try { const s = localStorage.getItem("aiops_collapsed"); if (s) arr = JSON.parse(s); } catch (e) {}
  const i = arr.indexOf(cat);
  if (i >= 0) arr.splice(i, 1); else arr.push(cat);
  try { localStorage.setItem("aiops_collapsed", JSON.stringify(arr)); } catch (e) {}
}
function renderCatDropdown(cats) {
  const wrap = $("catDropdownWrap") ? $("catDropdownWrap").parentElement : null;
  if (!wrap) return; // 分类筛选已移除
  if (wrap.querySelector(".cat-dropdown")) {
    // Update options in existing dropdown
    updateCatDropdownOptions(cats);
    return;
  }
  // Remove native select
  const oldSel = $("catFilter");
  if (oldSel) oldSel.remove();
  wrap.innerHTML = `<div class="cat-dropdown" id="catDropdownWrap">
    <button class="cat-dd-btn" id="catDropdownBtn"><span id="catDropdownLabel">${I18N.t("ui.all_categories")}</span> <span class="dd-arrow">▾</span></button>
    <div class="cat-dd-menu" id="catDropdownMenu"></div>
  </div>`;
  updateCatDropdownOptions(cats);
  // Toggle menu
  $("catDropdownBtn").addEventListener("click", e => {
    e.stopPropagation();
    $("catDropdownMenu").classList.toggle("show");
  });
  document.addEventListener("click", e => {
    const menu = $("catDropdownMenu");
    if (menu && !e.target.closest(".cat-dropdown")) menu.classList.remove("show");
  });
}
function updateCatDropdownOptions(cats) {
  const menu = $("catDropdownMenu");
  if (!menu) return;
  // Clean stale selections
  CUR_CATS = CUR_CATS.filter(c => cats.includes(c));
  // P0-#1: Only rebuild DOM when category list actually changes
  const newKey = cats.join("\u0001");
  if (newKey === LAST_CATS_KEY) {
    // Same categories: just sync checkbox states without rebuilding DOM
    menu.querySelectorAll("input").forEach(inp => {
      if (inp.value === "") inp.checked = CUR_CATS.length === 0;
      else inp.checked = CUR_CATS.includes(inp.value);
    });
    updateCatDropdownLabel();
    return;
  }
  LAST_CATS_KEY = newKey;
  menu.innerHTML = `<label class="cat-dd-opt"><input type="checkbox" value="" ${CUR_CATS.length === 0 ? "checked" : ""}> 全部分类</label>` +
    cats.map(c => `<label class="cat-dd-opt"><input type="checkbox" value="${esc(c)}" ${CUR_CATS.includes(c) ? "checked" : ""}> ${esc(c)}</label>`).join("");
  menu.querySelectorAll("input").forEach(inp => {
    inp.addEventListener("change", () => {
      if (inp.value === "") {
        if (inp.checked) {
          menu.querySelectorAll("input").forEach(x => { if (x !== inp) x.checked = false; });
          setSelectedCats([]);
        }
      } else {
        menu.querySelector('input[value=""]').checked = false;
        const selected = [...menu.querySelectorAll('input:checked')].map(x => x.value).filter(v => v !== "");
        setSelectedCats(selected);
      }
      HOST_PAGE = 1;
      updateCatDropdownLabel();
      renderHosts(LAST_HOSTS);
    });
  });
  updateCatDropdownLabel();
}
function updateCatDropdownLabel() {
  const label = $("catDropdownLabel");
  if (!label) return;
  const btn = $("catDropdownBtn");
  if (CUR_CATS.length === 0) {
    label.textContent = I18N.t("section.all_categories");
    if (btn) btn.classList.remove("filtered");
  } else {
    if (CUR_CATS.length <= 2) label.textContent = CUR_CATS.join(", ");
    else label.textContent = CUR_CATS.length + " 个分类";
    if (btn) btn.classList.add("filtered");
  }
}

/* ---------- Host filters ---------- */
function filterHosts(value) {
  HOST_FILTER = value;
  HOST_PAGE = 1;
  renderHosts(LAST_HOSTS);
}

function sortHosts(value) {
  HOST_SORT = value;
  HOST_PAGE = 1;
  renderHosts(LAST_HOSTS);
}

// 暂停/恢复已移除，系统始终开启自动刷新（PAUSED 恒为 false）

// 一键清理所有离线主机
async function purgeOffline() {
  const off = LAST_HOSTS.filter(h => !h.online);
  if (!off.length) { toast(I18N.t("empty.no_offline_hosts"), "ok"); return; }
  if (!confirm(`确认清理 ${off.length} 台离线主机？\n若其 Agent 仍在运行，约 60 秒后会重新出现。`)) return;
  let ok = 0;
  for (const h of off) {
    try { const r = await fetch(`${API}/hosts/${encodeURIComponent(h.id)}`, { method: "DELETE" }); if (r.ok) ok++; } catch (e) { /* skip */ }
  }
  toast(I18N.t("toast.cleaned") + ok + I18N.t("toast.hosts_cleaned"), "ok");
  refresh(true);
}

// Helper function to safely add event listeners
function safeAddEventListener(id, event, handler) {
  const el = $(id);
  if (el) {
    el.addEventListener(event, handler);
  } else {
    console.warn(`Element with id "${id}" not found`);
  }
}

// 注：settingsBtn / themeToggle / topbarThemeBtn 已移入右上角用户下拉菜单
// 侧栏菜单
safeAddEventListener("saveBtn", "click", saveSettings);
safeAddEventListener("saveThresholdsBtn", "click", () => saveThresholds()); // 告警阈值 Tab 独立保存
["thrPresetConservative", "thrPresetStandard", "thrPresetRelaxed"].forEach(id => {
  safeAddEventListener(id, "click", (e) => {
    const key = e.currentTarget && e.currentTarget.dataset ? e.currentTarget.dataset.preset : "";
    if (key) applyThresholdPreset(key);
  });
});
safeAddEventListener("aiExpandBtn", "click", function () { // AI 面板放大↔还原（铺满右侧交互区 / 默认宽度）
  const m = $("aiChatMask"); if (!m) return; // 几何在遮罩容器上，放大切遮罩宽度
  const on = m.classList.toggle("ai-expanded");
  this.textContent = on ? "⤡" : "⤢";
  this.title = on ? "还原窗口" : "放大窗口";
});
safeAddEventListener("testBtn", "click", testSettings);
safeAddEventListener("installBtn", "click", openInstall);
safeAddEventListener("resetTokenBtn", "click", resetToken);
safeAddEventListener("revokeTokenBtn", "click", revokeInstallToken);
safeAddEventListener("installTokenPolicyBtn", "click", saveInstallTokenPolicy);
safeAddEventListener("agentAutoUpdateSaveBtn", "click", saveAgentAutoUpdatePolicy);
safeAddEventListener("agentAutoUpdate", "change", syncAgentAutoUpdateBadge);
safeAddEventListener("agentAutoUpdateToggle", "click", () => {
  const b = document.getElementById("agentAutoUpdateBody");
  const c = document.getElementById("agentAutoUpdateCaret");
  const h = document.getElementById("agentAutoUpdateToggle");
  if (!b) return;
  const hidden = b.style.display === "none";
  b.style.display = hidden ? "" : "none";
  if (c) c.textContent = hidden ? "▾" : "▸";
  if (h) h.setAttribute("aria-expanded", hidden ? "true" : "false");
});
safeAddEventListener("agentAutoUpdateToggle", "keydown", (e) => {
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    document.getElementById("agentAutoUpdateToggle")?.click();
  }
});
safeAddEventListener("installTokenPolicyToggle", "click", () => {
  const b = document.getElementById("installTokenPolicyBody");
  const c = document.getElementById("installTokenPolicyCaret");
  const h = document.getElementById("installTokenPolicyToggle");
  if (!b) return;
  const hidden = b.style.display === "none";
  b.style.display = hidden ? "" : "none";
  if (c) c.textContent = hidden ? "▾" : "▸";
  if (h) h.setAttribute("aria-expanded", hidden ? "true" : "false");
});
safeAddEventListener("installTokenPolicyToggle", "keydown", (e) => {
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    document.getElementById("installTokenPolicyToggle")?.click();
  }
});
safeAddEventListener("tokenToggleBtn", "click", function() {
  TOKEN_REVEALED = !TOKEN_REVEALED;
  updateTokenDisplay();
  this.title = TOKEN_REVEALED ? I18N.t("ui.hide_token") : I18N.t("ui.show_token");
  if (typeof renderInstallCmd === "function") renderInstallCmd();
});
safeAddEventListener("copyCmdBtn", "click", function() {
  if (MULTI_SERVER_MODE && buildMultiServerTargets().length < 2) {
    toast(I18N.t("install.multi_need_two"), "err");
    return;
  }
  const raw = this.dataset.rawCmd || $("installCmd").textContent;
  copyWithFeedback(this, raw, I18N.t("toast.copy_install"));
});
// 点击命令区域本身也可复制
safeAddEventListener("installCmd", "click", function() {
  const sel = window.getSelection();
  sel.removeAllRanges();
  const range = document.createRange();
  range.selectNodeContents(this);
  sel.addRange(range);
});
safeAddEventListener("installFolderId", "change", function() {
  if (typeof syncInstallNewFolderBtn === "function") syncInstallNewFolderBtn();
  renderInstallCmd();
});
safeAddEventListener("installNewFolderBtn", "click", function() {
  if (typeof createInstallChildFolder === "function") createInstallChildFolder();
});
safeAddEventListener("installLogPaths", "input", renderInstallCmd); // 日志路径变化即时更新安装命令
["installSNIEnabled", "installContentAudit", "installCaptureBackend", "installContentAuditBodyMode"].forEach(id => safeAddEventListener(id, "change", renderInstallCmd));
["installSNIInterface", "installContentAuditPorts", "installContentAuditMaxBody", "installContentAuditMaxEvents", "installContentAuditHosts", "installContentAuditExcludePaths"].forEach(id => safeAddEventListener(id, "input", renderInstallCmd));
safeAddEventListener("logCollectToggle", "click", () => { // 折叠/展开「日志采集」
  const b = document.getElementById("logCollectBody"), c = document.getElementById("lcCaret");
  if (!b) return;
  const hidden = b.style.display === "none";
  b.style.display = hidden ? "" : "none";
  if (c) c.textContent = hidden ? "▾" : "▸";
});
safeAddEventListener("networkAuditToggle", "click", () => {
  const b = document.getElementById("networkAuditBody"), c = document.getElementById("networkAuditCaret");
  if (!b) return;
  const hidden = b.style.display === "none";
  b.style.display = hidden ? "" : "none";
  if (c) c.textContent = hidden ? "▾" : "▸";
});
safeAddEventListener("osTabs", "click", e => {
  const t = e.target.closest(".tab"); if (!t) return;
  CUR_OS = t.dataset.os;
  document.querySelectorAll("#osTabs .tab").forEach(x => x.classList.toggle("active", x === t));
  renderInstallCmd();
});
safeAddEventListener("copyUninstallBtn", "click", function() {
  copyWithFeedback(this, $("uninstallCmd").textContent, I18N.t("toast.copy_uninstall"));
});
// 安装模式切换（radio buttons）
document.querySelectorAll('input[name="installMode"]').forEach(r => {
  r.addEventListener("change", function() {
    RELAY_MODE = (this.value === "relay");
    MULTI_SERVER_MODE = (this.value === "multi");
    renderInstallCmd();
  });
});
// 多服务端推送列表变更
safeAddEventListener("multiServerList", "input", renderInstallCmd);
safeAddEventListener("relayGatewayIP", "input", renderInstallCmd);
safeAddEventListener("relayListenPort", "input", renderInstallCmd);
safeAddEventListener("copyRelayGatewayBtn", "click", function() {
  const raw = this.dataset.rawCmd || $("relayGatewayCmd").textContent;
  copyWithFeedback(this, raw, I18N.t("toast.copy_relay_install"));
});
safeAddEventListener("copyRelayInternalBtn", "click", function() {
  const raw = this.dataset.rawCmd || $("relayInternalCmd").textContent;
  copyWithFeedback(this, raw, I18N.t("toast.copy_intranet_install"));
});

// 告警操作按钮事件委托（确认 / 静默 / 清除状态）
document.addEventListener("click", async function(e) {
  const btn = e.target.closest(".alert-action");
  if (!btn) return;
  e.preventDefault();
  e.stopPropagation();
  const action = btn.dataset.action;
  const body = JSON.stringify({
    host_id: btn.dataset.host || "",
    type: btn.dataset.type || "",
    scope: btn.dataset.scope || ""
  });
  try {
    const r = await fetch(`${API}/alerts/${action}`, { method: "POST", headers: { "Content-Type": "application/json" }, body });
    if (r.ok) {
      toast(action === "ack" ? I18N.t("toast.alert_ack") : action === "silence" ? I18N.t("toast.alert_silence") : I18N.t("toast.alert_cleared"), "ok");
      await refresh(true);
    } else {
      toast(I18N.t("toast.operation_failed"), "err");
    }
  } catch (e) { toast(I18N.t("toast.network_error"), "err"); }
});

/* ---------- 侧栏导航：视图切换 + 收起 + 移动抽屉 ---------- */
const navItems = document.querySelectorAll(".nav-item");
// 页面头元信息：标题 + 副标题。副标题让顶栏页面头承载“页面语义”，
// 而非机械回显侧栏导航名，从根上消除“两个概览”的重复观感。
// 方案A 分层一致：页头标题=侧栏短名，描述放副标题。合并后的父视图(监控/告警)标题一致，
// 由视图内 Tab 指示子上下文。
const _SUB_MON = "合成监控 · 拨测（网站 / 端口 / 进程）与 API 业务接口性能";
const _SUB_ALT = "当前告警 · 治理规则（静默 / 抑制 / 生效时段 / 通知路由）· 告警阈值";
const PAGE_META = {
  overview: { title: "首页", sub: I18N.t("section.overview_desc") },
  hosts:    { title: I18N.t("nav.hosts"), sub: I18N.t("section.hosts_desc") },
  alerts:   { title: "告警", sub: _SUB_ALT },
  governance: { title: "告警", sub: _SUB_ALT },
  thresholds: { title: "告警", sub: _SUB_ALT },
  checks:   { title: "监控", sub: _SUB_MON },
  apimon:   { title: "监控", sub: _SUB_MON },
  automation: { title: "编排", sub: I18N.t("section.automation_desc") },
  forward:  { title: I18N.t("section.port_forward"), sub: I18N.t("section.forward_desc") },
  sre:      { title: I18N.t("nav.sre") || "SRE 中枢", sub: I18N.t("section.sre_desc") },
  logs:     { title: "日志", sub: I18N.t("section.logs_desc") },
  log:      { title: "审计日志", sub: I18N.t("section.log_desc") },
  datasource: { title: "数据源", sub: I18N.t("section.datasource_desc") },
  "sql-toolkit": { title: I18N.t("nav.sql_toolkit") || "SQL 工具", sub: I18N.t("section.sql_toolkit_desc") || "MySQL 美化 / 审核 / 优化 + EXPLAIN" },
  hardware:  { title: I18N.t("nav.resources") || "资源", sub: I18N.t("section.resources_desc") || "物理硬件 / 虚拟机 / 容器 / Kubernetes" },
  hyperv:    { title: I18N.t("nav.resources") || "资源", sub: I18N.t("section.resources_desc") || "物理硬件 / 虚拟机 / 容器 / Kubernetes" },
  containers:{ title: I18N.t("nav.resources") || "资源", sub: I18N.t("containers.tag") || "主机 Docker / Podman 容器" },
  k8s:       { title: I18N.t("nav.resources") || "资源", sub: I18N.t("k8s.tag") || "Kubernetes 集群资源" },
  netflow:   { title: I18N.t("nav.network") || "网络", sub: I18N.t("section.netflow_desc") || "NetFlow 网络流量分析" },
  snmp:      { title: I18N.t("nav.network") || "网络", sub: I18N.t("section.snmp_desc") || "SNMP 网络设备接口流量与 Trap 事件" },
  "host-security": { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("section.host_security_desc", "主机安全扫描") },
  "security-overview": { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("sec.tab_overview", "总览") },
  "web-security": { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("section.web_security_desc", "Web 漏洞扫描") },
  "content-audit": { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("section.content_audit_desc", "明文 HTTP 内容审计") },
  "ai-tool-audit": { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("sec.tab_ai_tools", "AI 工具审计") },
  "audit-export": { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("sec.tab_export", "审计外发") },
  oidc: { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("sec.tab_oidc", "单点登录") },
  dashboards: { title: I18N.t("nav.dashboards") || "仪表盘", sub: I18N.t("section.dashboards_desc") || "自定义仪表盘 · 一键导入 Grafana 看板 · 面板走 VictoriaMetrics" },
};
// Rebuild the JS-baked page-meta strings in the current language (called on
// i18n:changed so titles/subtitles follow an in-place language switch).
function rebuildPageMeta() {
  PAGE_META.overview   = { title: "首页", sub: I18N.t("section.overview_desc") };
  PAGE_META.hosts      = { title: I18N.t("nav.hosts"), sub: I18N.t("section.hosts_desc") };
  PAGE_META.alerts     = { title: "告警", sub: _SUB_ALT };
  PAGE_META.governance = { title: "告警", sub: _SUB_ALT };
  PAGE_META.thresholds = { title: "告警", sub: _SUB_ALT };
  PAGE_META.checks     = { title: "监控", sub: _SUB_MON };
  PAGE_META.apimon     = { title: "监控", sub: _SUB_MON };
  PAGE_META.scrape     = { title: "监控", sub: _SUB_MON };
  PAGE_META.automation = { title: "编排", sub: I18N.t("section.automation_desc") };
  PAGE_META.forward    = { title: I18N.t("section.port_forward"), sub: I18N.t("section.forward_desc") };
  PAGE_META.sre        = { title: I18N.t("nav.sre") || "SRE 中枢", sub: I18N.t("section.sre_desc") };
  PAGE_META.logs       = { title: "日志", sub: I18N.t("section.logs_desc") };
  PAGE_META.log        = { title: "审计日志", sub: I18N.t("section.log_desc") };
  PAGE_META.datasource = { title: "数据源", sub: I18N.t("section.datasource_desc") };
  PAGE_META["sql-toolkit"] = { title: I18N.t("nav.sql_toolkit") || "SQL 工具", sub: I18N.t("section.sql_toolkit_desc") || "MySQL 美化 / 审核 / 优化 + EXPLAIN" };
  PAGE_META.hardware   = { title: I18N.t("nav.resources") || "资源", sub: I18N.t("section.resources_desc") || "物理硬件 / 虚拟机 / 容器 / Kubernetes" };
  PAGE_META.hyperv     = { title: I18N.t("nav.resources") || "资源", sub: I18N.t("section.resources_desc") || "物理硬件 / 虚拟机 / 容器 / Kubernetes" };
  PAGE_META.containers = { title: I18N.t("nav.resources") || "资源", sub: I18N.t("containers.tag") || "主机 Docker / Podman 容器" };
  PAGE_META.k8s        = { title: I18N.t("nav.resources") || "资源", sub: I18N.t("k8s.tag") || "Kubernetes 集群资源" };
  PAGE_META.netflow    = { title: I18N.t("nav.network") || "网络", sub: I18N.t("section.netflow_desc") || "NetFlow 网络流量分析" };
  PAGE_META.snmp       = { title: I18N.t("nav.network") || "网络", sub: I18N.t("section.snmp_desc") || "SNMP 网络设备接口流量与 Trap 事件" };
  PAGE_META["host-security"] = { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("section.host_security_desc", "主机安全扫描") };
  PAGE_META["security-overview"] = { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("sec.tab_overview", "总览") };
  PAGE_META["web-security"] = { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("section.web_security_desc", "Web 漏洞扫描") };
  PAGE_META["content-audit"] = { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("section.content_audit_desc", "明文 HTTP 内容审计") };
  PAGE_META["ai-tool-audit"] = { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("sec.tab_ai_tools", "AI 工具审计") };
  PAGE_META["audit-export"] = { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("sec.tab_export", "审计外发") };
  PAGE_META.oidc = { title: I18N.t("nav.security", "安全中心"), sub: I18N.t("sec.tab_oidc", "单点登录") };
  PAGE_META.dashboards = { title: I18N.t("nav.dashboards") || "仪表盘", sub: I18N.t("section.dashboards_desc") || "自定义仪表盘 · 一键导入 Grafana 看板 · 面板走 VictoriaMetrics" };
}
// IA 重构（方案B）：把「监控(拨测+性能)」「告警(当前+治理)」合并为父导航 + 视图内 Tab。
// 不搬 DOM、不动各视图内部逻辑——仅减导航项 + 由 switchView 渲染共享 Tab 栏 #viewTabs。
// NET_TABS：网络流量 + 网络设备（内容审计已升维至「安全中心」）。
function NET_TABS() {
  return [
    ["netflow", I18N.t("net.tab_traffic") || "流量"],
    ["snmp", I18N.t("net.tab_devices") || "网络设备"],
  ];
}
function SEC_TAB_GROUPS() {
  return {
    parent: "security-overview",
    sections: [
      { tabs: [["security-overview", I18N.t("sec.tab_overview", "总览")]] },
      {
        label: I18N.t("sec.section_vuln", "漏洞运营"),
        tabs: [
          ["host-security", I18N.t("sec.tab_host", "主机安全")],
          ["web-security", I18N.t("sec.tab_web", "Web 扫描")],
        ],
      },
      {
        label: I18N.t("sec.section_audit", "数据审计"),
        tabs: [["content-audit", I18N.t("sec.tab_content", "内容审计")]],
      },
      {
        label: I18N.t("sec.section_trust", "平台信任"),
        tabs: [
          ["ai-tool-audit", I18N.t("sec.tab_ai_tools", "AI 工具审计")],
          ["audit-export", I18N.t("sec.tab_export", "审计外发")],
          ["oidc", I18N.t("sec.tab_oidc", "单点登录")],
        ],
      },
    ],
  };
}
function flattenViewTabs(g) {
  if (!g) return [];
  if (g.sections) {
    const out = [];
    g.sections.forEach(sec => { (sec.tabs || []).forEach(t => out.push(t)); });
    return out;
  }
  return g.tabs || [];
}
const VIEW_TAB_GROUPS = {
  checks:     { parent: "checks", tabs: [["checks", "拨测监控"], ["apimon", "可靠性保障"], ["scrape", "指标抓取"]] },
  apimon:     { parent: "checks", tabs: [["checks", "拨测监控"], ["apimon", "可靠性保障"], ["scrape", "指标抓取"]] },
  scrape:     { parent: "checks", tabs: [["checks", "拨测监控"], ["apimon", "可靠性保障"], ["scrape", "指标抓取"]] },
  alerts:     { parent: "alerts", tabs: [["alerts", "当前告警"], ["governance", "治理规则"], ["thresholds", "告警阈值"]] },
  governance: { parent: "alerts", tabs: [["alerts", "当前告警"], ["governance", "治理规则"], ["thresholds", "告警阈值"]] },
  thresholds: { parent: "alerts", tabs: [["alerts", "当前告警"], ["governance", "治理规则"], ["thresholds", "告警阈值"]] },
  netflow:         { parent: "netflow", tabs: NET_TABS() },
  snmp:            { parent: "netflow", tabs: NET_TABS() },
  // 「安全中心」：总览 + 漏洞运营 + 数据审计 + 平台信任
  "security-overview": SEC_TAB_GROUPS(),
  "host-security": SEC_TAB_GROUPS(),
  "web-security":  SEC_TAB_GROUPS(),
  "content-audit": SEC_TAB_GROUPS(),
  "ai-tool-audit": SEC_TAB_GROUPS(),
  "audit-export":  SEC_TAB_GROUPS(),
  oidc:            SEC_TAB_GROUPS(),
  // 「资源」父导航：硬件 / 虚拟机 / 容器 / K8s。parent=hardware=第一个子标签。
  hardware:   { parent: "hardware", tabs: [["hardware", I18N.t("nav.hardware") || "硬件"], ["hyperv", I18N.t("nav.hyperv") || "虚拟机"], ["containers", I18N.t("nav.containers") || "容器"], ["k8s", I18N.t("nav.k8s") || "K8s"]] },
  hyperv:     { parent: "hardware", tabs: [["hardware", I18N.t("nav.hardware") || "硬件"], ["hyperv", I18N.t("nav.hyperv") || "虚拟机"], ["containers", I18N.t("nav.containers") || "容器"], ["k8s", I18N.t("nav.k8s") || "K8s"]] },
  containers: { parent: "hardware", tabs: [["hardware", I18N.t("nav.hardware") || "硬件"], ["hyperv", I18N.t("nav.hyperv") || "虚拟机"], ["containers", I18N.t("nav.containers") || "容器"], ["k8s", I18N.t("nav.k8s") || "K8s"]] },
  k8s:        { parent: "hardware", tabs: [["hardware", I18N.t("nav.hardware") || "硬件"], ["hyperv", I18N.t("nav.hyperv") || "虚拟机"], ["containers", I18N.t("nav.containers") || "容器"], ["k8s", I18N.t("nav.k8s") || "K8s"]] },
};
function renderViewTabs(view) {
  const bar = $("viewTabs"); if (!bar) return;
  const g = VIEW_TAB_GROUPS[view];
  const resourceBar = $("resourceSearchBar");
  const resourceView = !!(g && g.parent === "hardware");
  if (resourceBar) {
    resourceBar.style.display = resourceView ? "" : "none";
    if (!resourceView) {
      const results = $("resourceSearchResults");
      if (results) results.classList.remove("show");
    }
  }
  if (!g) { bar.style.display = "none"; bar.innerHTML = ""; return; }
  const secViews = new Set(["security-overview", "host-security", "web-security", "content-audit", "ai-tool-audit", "audit-export", "oidc"]);
  const viewer = (typeof CUR_ROLE !== "undefined" && CUR_ROLE === "viewer");
  const tabVisible = ([v]) => !viewer || !secViews.has(v);
  if (g.sections) {
    const parts = [];
    g.sections.forEach(sec => {
      const visible = (sec.tabs || []).filter(tabVisible);
      if (!visible.length) return;
      if (sec.label) parts.push(`<span class="view-tab-section">${sec.label}</span>`);
      visible.forEach(([v, label]) => {
        parts.push(`<button class="view-tab ${v === view ? "active" : ""}" data-vtab="${v}">${label}</button>`);
      });
    });
    if (!parts.length) { bar.style.display = "none"; bar.innerHTML = ""; return; }
    bar.style.display = "";
    bar.innerHTML = parts.join("");
  } else {
    const tabs = flattenViewTabs(g).filter(tabVisible);
    if (!tabs.length) { bar.style.display = "none"; bar.innerHTML = ""; return; }
    bar.style.display = "";
    bar.innerHTML = tabs.map(([v, label]) => `<button class="view-tab ${v === view ? "active" : ""}" data-vtab="${v}">${label}</button>`).join("");
  }
  bar.querySelectorAll("[data-vtab]").forEach(b => b.addEventListener("click", () => switchView(b.dataset.vtab)));
}

let RESOURCE_SEARCH_RESULTS = [];
let RESOURCE_SEARCH_TIMER = null;
let RESOURCE_SEARCH_SEQ = 0;
const RESOURCE_TYPE_LABELS = {
  host: "主机", hyperv_vm: "VM", container: "容器", hardware: "硬件",
  k8s_cluster: "K8s", pod: "Pod"
};

function renderResourceSearchResults(rows, query) {
  const panel = $("resourceSearchResults");
  if (!panel) return;
  RESOURCE_SEARCH_RESULTS = Array.isArray(rows) ? rows : [];
  if (!query) {
    panel.innerHTML = "";
    panel.classList.remove("show");
    return;
  }
  if (!RESOURCE_SEARCH_RESULTS.length) {
    panel.innerHTML = `<div class="resource-search-empty">未找到匹配资源</div>`;
  } else {
    panel.innerHTML = RESOURCE_SEARCH_RESULTS.map((row, i) => {
      const sub = row.subtitle || row.ref || "";
      return `<button type="button" class="resource-search-result" role="option" data-resource-result="${i}">
        <span class="resource-search-type">${esc(RESOURCE_TYPE_LABELS[row.type] || row.type)}</span>
        <span class="resource-search-main"><span class="resource-search-name">${esc(row.name || row.ref)}</span><span class="resource-search-sub">${esc(sub)}</span></span>
        <span class="resource-search-host">${esc(row.host || "")}</span>
      </button>`;
    }).join("");
  }
  panel.classList.add("show");
}

async function runResourceSearch(query) {
  const bar = $("resourceSearchBar");
  const seq = ++RESOURCE_SEARCH_SEQ;
  if (!query.trim()) {
    renderResourceSearchResults([], "");
    return;
  }
  if (bar) bar.classList.add("loading");
  try {
    const response = await fetch(`${API}/resources/search?q=${encodeURIComponent(query.trim())}&limit=20`);
    const body = await response.json();
    if (seq === RESOURCE_SEARCH_SEQ) renderResourceSearchResults(body.results || [], query);
  } catch (_) {
    if (seq === RESOURCE_SEARCH_SEQ) renderResourceSearchResults([], query);
  } finally {
    if (seq === RESOURCE_SEARCH_SEQ && bar) bar.classList.remove("loading");
  }
}

function focusResourceViewResult(row) {
  if (!row) return;
  if (row.type === "hardware" && typeof HW_FILTER !== "undefined") HW_FILTER.q = row.name || "";
  if (row.type === "hyperv_vm" && typeof HV_FILTER !== "undefined") {
    HV_FILTER.q = row.name || "";
    HV_FILTER.status = "all";
  }
  switchView(row.view || "hardware");
  if (row.type === "host") {
    setTimeout(() => openDetail(row.host_id, row.name), 0);
    return;
  }
  const targetInput = row.type === "container" ? "ctSearch" : row.type === "pod" ? "k8sSearch" : "";
  const applyDynamicSelection = (attempt) => {
    if (row.type === "k8s_cluster") {
      const select = $("k8sClusterSel");
      const id = String(row.ref || "").replace(/^cluster:/, "");
      if (select && [...select.options].some(o => o.value === id)) {
        select.value = id;
        select.dispatchEvent(new Event("change", { bubbles: true }));
        return;
      }
    } else if (targetInput) {
      const input = $(targetInput);
      if (input) {
        input.value = row.name || "";
        input.dispatchEvent(new Event("input", { bubbles: true }));
        return;
      }
    } else {
      return;
    }
    if (attempt < 12) setTimeout(() => applyDynamicSelection(attempt + 1), 100);
  };
  applyDynamicSelection(0);
}

(function initResourceSearch() {
  const input = $("resourceSearchInput");
  const panel = $("resourceSearchResults");
  if (!input || !panel) return;
  input.addEventListener("input", () => {
    clearTimeout(RESOURCE_SEARCH_TIMER);
    const query = input.value || "";
    RESOURCE_SEARCH_TIMER = setTimeout(() => runResourceSearch(query), 180);
  });
  input.addEventListener("keydown", e => {
    if (e.key === "ArrowDown") {
      const first = panel.querySelector(".resource-search-result");
      if (first) { e.preventDefault(); first.focus(); }
    }
  });
  panel.addEventListener("click", e => {
    const item = e.target.closest("[data-resource-result]");
    if (!item) return;
    const row = RESOURCE_SEARCH_RESULTS[Number(item.dataset.resourceResult)];
    panel.classList.remove("show");
    focusResourceViewResult(row);
  });
  document.addEventListener("click", e => {
    if (!e.target.closest("#resourceSearchBar")) panel.classList.remove("show");
  });
})();

function switchView(view) {
  const secViews = new Set(["security-overview", "host-security", "web-security", "content-audit", "ai-tool-audit", "audit-export", "oidc"]);
  if (secViews.has(view) && typeof CUR_ROLE !== "undefined" && CUR_ROLE === "viewer") {
    view = "overview";
  }
  document.querySelectorAll(".view").forEach(v => v.classList.toggle("active", v.id === "view-" + view));
  const g = VIEW_TAB_GROUPS[view];
  const activeNav = g ? g.parent : view; // 子视图(性能/治理)时高亮父导航(监控/告警)
  navItems.forEach(n => n.classList.toggle("active", n.dataset.view === activeNav));
  renderViewTabs(view);
  const meta = PAGE_META[view];
  if (meta) {
    const t = $("pageTitle"), s = $("pageSub");
    if (t) t.textContent = meta.title;
    if (s) s.textContent = meta.sub;
  }
  if (view === "checks") loadChecks();
  if (view === "automation") {
    const active = document.querySelector("#autoTabs .chip-btn.active");
    const tab = active && active.dataset.autotab ? active.dataset.autotab : "playbooks";
    if (tab === "inspect") loadHostInspect(); else loadPlaybooks();
  }
  if (view === "forward") loadForwards();
  if (view === "sre") loadSRE();
  if (view === "logs") loadLogs();
  if (view === "apimon") { loadAPIMon(); loadAPITxns(); loadDist(); }
  if (view === "scrape") { loadScrapes(); loadPromWrite(); loadRules(); }
  if (view === "dashboards") loadDashboards();
  if (view === "governance") loadGovernance();
  if (view === "thresholds") loadThresholds();
  if (view === "datasource") loadDataSources();
  if (view === "sql-toolkit" && window._pageRenderers && window._pageRenderers["sql-toolkit"]) window._pageRenderers["sql-toolkit"]();
  if (view === "hardware" && window._pageRenderers && window._pageRenderers.hardware) window._pageRenderers.hardware();
  if (view === "hyperv" && window._pageRenderers && window._pageRenderers.hyperv) window._pageRenderers.hyperv();
  if (view === "containers" && window._pageRenderers && window._pageRenderers.containers) window._pageRenderers.containers();
  else {
    const ctLog = document.getElementById("ctLogMask");
    if (ctLog && ctLog.classList.contains("show")) {
      ctLog.classList.remove("show");
      const body = document.getElementById("ctLogBody");
      if (body) body.textContent = "";
    }
  }
  if (view === "k8s" && window._pageRenderers && window._pageRenderers.k8s) window._pageRenderers.k8s();
  if (view === "netflow" && window._pageRenderers && window._pageRenderers.netflow) window._pageRenderers.netflow();
  if (view === "snmp" && window._pageRenderers && window._pageRenderers.snmp) window._pageRenderers.snmp();
  if (view === "security-overview" && window._pageRenderers && window._pageRenderers["security-overview"]) window._pageRenderers["security-overview"]();
  if (view === "host-security" && window._pageRenderers && window._pageRenderers["host-security"]) window._pageRenderers["host-security"]();
  if (view === "web-security" && window._pageRenderers && window._pageRenderers["web-security"]) window._pageRenderers["web-security"]();
  if (view === "content-audit" && window._pageRenderers && window._pageRenderers["content-audit"]) window._pageRenderers["content-audit"]();
  if (view === "ai-tool-audit" && window._pageRenderers && window._pageRenderers["ai-tool-audit"]) window._pageRenderers["ai-tool-audit"]();
  if (view === "audit-export" && window._pageRenderers && window._pageRenderers["audit-export"]) window._pageRenderers["audit-export"]();
  if (view === "oidc" && window._pageRenderers && window._pageRenderers.oidc) window._pageRenderers.oidc();
  // 这些视图的 DOM 由全局轮询按活动视图渲染；切换过来时立即刷新一次，避免等到下个轮询周期(最多5s)才更新。
  if (view === "overview" || view === "hosts" || view === "alerts" || view === "log") refresh(true);
  window.scrollTo(0, 0);
}
navItems.forEach(n => n.addEventListener("click", () => {
  switchView(n.dataset.view);
  const appEl = $("app");
  if (appEl) appEl.classList.remove("nav-open");
}));

// Collapsible nav groups; collapsed state persists per group.
(function initNavGroups(){
  let collapsed = {};
  try { collapsed = JSON.parse(localStorage.getItem("aiops_nav_collapsed")||"{}"); } catch(e){}
  document.querySelectorAll(".nav-group").forEach(g => {
    const key = g.dataset.group;
    if (collapsed[key]) g.classList.add("collapsed");
    const label = g.querySelector(".nav-group-label");
    if (label) label.addEventListener("click", () => {
      g.classList.toggle("collapsed");
      collapsed[key] = g.classList.contains("collapsed");
      try { localStorage.setItem("aiops_nav_collapsed", JSON.stringify(collapsed)); } catch(e){}
    });
  });
})();

// 汉堡：桌面收起/展开侧栏；移动端打开/关闭抽屉
safeAddEventListener("menuBtn", "click", () => {
  const appEl = $("app");
  if (!appEl) return;
  if (window.innerWidth <= 900) {
    appEl.classList.toggle("nav-open");
    document.documentElement.style.removeProperty("--sidew"); // 移动端抽屉用默认宽度
  } else {
    const collapsed = appEl.classList.toggle("collapsed");
    // AI 面板等「.app 之外的 body 级固定元素」靠 --sidew 定位；.app.collapsed 里改 --sidew 不会
    // 级联到 .app 之外，故这里在 root 同步实际侧栏宽度（收起=64px，展开=清除回默认），
    // 让放大后的 AI 面板始终与侧栏右缘对齐、收起时不留空隙。
    if (collapsed) document.documentElement.style.setProperty("--sidew", "64px");
    else document.documentElement.style.removeProperty("--sidew");
  }
  // a11y：同步汉堡按钮的展开态（移动端看 nav-open，桌面看是否 collapsed）
  const btn = $("menuBtn");
  if (btn) {
    const expanded = window.innerWidth <= 900 ? appEl.classList.contains("nav-open") : !appEl.classList.contains("collapsed");
    btn.setAttribute("aria-expanded", expanded ? "true" : "false");
  }
});
// a11y：给所有弹窗补 role=dialog + aria-modal（集中一处，免逐个改 HTML；仅可见时对读屏器生效）
document.querySelectorAll(".mask .modal").forEach(m => {
  if (!m.hasAttribute("role")) m.setAttribute("role", "dialog");
  m.setAttribute("aria-modal", "true");
});
safeAddEventListener("backdrop", "click", () => {
  const appEl = $("app");
  if (appEl) appEl.classList.remove("nav-open");
});

// 日志类型筛选
safeAddEventListener("logFilter", "click", e => {
  const b = e.target.closest(".chip-btn"); if (!b) return;
  LOG_KIND = b.dataset.kind;
  LOG_PAGE = 1;
  document.querySelectorAll("#logFilter .chip-btn").forEach(x => x.classList.toggle("active", x === b));
  renderLog(LAST_LOG);
});
// 审计日志搜索框：按内容/操作者/主机关键字过滤
function onAuditSearchInput(e) { LOG_SEARCH = (e && e.target && e.target.value) || ""; LOG_PAGE = 1; renderLog(LAST_LOG); }
safeAddEventListener("auditSearch", "input", onAuditSearchInput);
safeAddEventListener("auditSearch", "search", onAuditSearchInput);
// 监控 / 编排 / 转发 搜索框（复用标准 .search）
function onCheckSearchInput(e) { CHECK_SEARCH = (e && e.target && e.target.value) || ""; renderChecks(LAST_CHECKS); }
safeAddEventListener("checkSearch", "input", onCheckSearchInput);
safeAddEventListener("checkSearch", "search", onCheckSearchInput);
function onPlaybookSearchInput(e) { PB_SEARCH = (e && e.target && e.target.value) || ""; renderPlaybooks(LAST_PLAYBOOKS); }
safeAddEventListener("playbookSearch", "input", onPlaybookSearchInput);
safeAddEventListener("playbookSearch", "search", onPlaybookSearchInput);
function onForwardSearchInput(e) { FWD_SEARCH = (e && e.target && e.target.value) || ""; renderForwards(); }
safeAddEventListener("forwardSearch", "input", onForwardSearchInput);
safeAddEventListener("forwardSearch", "search", onForwardSearchInput);

// 告警类型筛选
safeAddEventListener("alertFilter", "click", e => {
  const b = e.target.closest(".chip-btn"); if (!b) return;
  filterAlertsByType(b.dataset.atype);
});
// 告警搜索
function onAlertSearchInput(e) { ALERT_SEARCH = (e && e.target && e.target.value) || ""; renderAlerts(LAST_ALERTS); }
safeAddEventListener("alertSearch", "input", onAlertSearchInput);
safeAddEventListener("alertSearch", "search", onAlertSearchInput);

// 日志级别和时间范围筛选
function filterLogsByLevel(level) {
  LOG_LEVEL = level;
  LOG_PAGE = 1;
  renderLog(LAST_LOG);
}

function filterLogsByTime(range) {
  LOG_TIME_RANGE = range;
  LOG_PAGE = 1;
  renderLog(LAST_LOG);
}

// 日志分页点击
safeAddEventListener("logPager", "click", e => {
  const b = e.target.closest("button[data-lpg]"); if (!b) return;
  const pg = b.dataset.lpg;
  if (pg === "prev") LOG_PAGE--;
  else if (pg === "next") LOG_PAGE++;
  else LOG_PAGE = parseInt(pg);
  renderLog(LAST_LOG);
});

// 监控类型筛选
function filterChecks(type) {
  CHECK_TYPE = type;
  renderChecks(LAST_CHECKS);
}
// 弹窗关闭：点遮罩空白处 或 右上角 ✕
// 用 document 委托，兼容后续动态插入的 .mask（如页面内二次渲染的弹层）。
document.addEventListener("click", e => {
  const t = e.target;
  if (!(t instanceof Element)) return;
  const mk = t.closest(".mask");
  if (!mk || !mk.classList.contains("show")) return;
  if (t !== mk && !t.closest("[data-close-btn]")) return;
  if (mk.hasAttribute("data-forced")) return; // 强制弹窗（首次安全初始化）：禁止点遮罩/✕ 关闭
  if (mk.id === "uiConfirmMask") {
    if (typeof _finishUiConfirm === "function") _finishUiConfirm(false);
    else mk.classList.remove("show");
    return;
  }
  mk.classList.remove("show"); hideChartTip();
  if (mk.id === "termMask") { closeTerminalWS(); }
  if (mk.id === "desktopMask") { closeDesktopWS(); }
  if (mk.id === "termReplayMask") { closeReplay(); }
  if (mk.id === "termObserveMask") { closeObserveWS(); }
  if (mk.id === "termSessionsMask") { if (TERM_SESSIONS_TIMER) { clearInterval(TERM_SESSIONS_TIMER); TERM_SESSIONS_TIMER = null; } }
  // v5.3.0: 终端认证弹窗关闭时清理状态
  if (mk.id === "termProtocolMask" || mk.id === "termSetPwdMask" || mk.id === "termVerifyMask" || mk.id === "termLockedMask") { cancelTermAuth(); }
  if (mk.id === "ctLogMask") {
    const body = document.getElementById("ctLogBody");
    if (body) body.textContent = "";
  }
});
document.addEventListener("keydown", e => {
  if (e.key === "Escape") {
    // Only close the topmost mask so stacked modals unwind one layer at a time.
    const masks = document.querySelectorAll(".mask.show:not([data-forced])");
    if (!masks.length) return;
    const top = masks[masks.length - 1];
    const hadTerm = top.id === "termMask";
    const hadDesktop = top.id === "desktopMask";
    const hadReplay = top.id === "termReplayMask";
    const hadObserve = top.id === "termObserveMask";
    const hadSessions = top.id === "termSessionsMask";
    if (typeof closeMask === "function") closeMask(top);
    else top.classList.remove("show");
    hideChartTip();
    if (hadTerm) closeTerminalWS();
    if (hadDesktop) closeDesktopWS();
    if (hadReplay) closeReplay();
    if (hadObserve) closeObserveWS();
    if (hadSessions && TERM_SESSIONS_TIMER) { clearInterval(TERM_SESSIONS_TIMER); TERM_SESSIONS_TIMER = null; }
  }
});

// KPI 卡片点击 → 跳转对应视图（并按需过滤主机）
safeAddEventListener("cards", "click", e => {
  const c = e.target.closest(".card"); if (!c) return;
  const [view, filter] = (c.dataset.goto || "").split(":");
  if (view === "hosts") { HOST_FILTER = filter || "all"; HOST_PAGE = 1; renderHosts(LAST_HOSTS); }
  if (view) switchView(view);
});
// 主机搜索 + 分页（input + search：兼容 type=search 的清除按钮）
function onHostSearchInput(e) {
  HOST_SEARCH = (e && e.target && e.target.value) || "";
  HOST_PAGE = 1;
  renderHosts(LAST_HOSTS);
}
safeAddEventListener("hostSearch", "input", onHostSearchInput);
safeAddEventListener("hostSearch", "search", onHostSearchInput);
safeAddEventListener("pager", "click", e => {
  const b = e.target.closest("button[data-pg]"); if (!b) return;
  const pg = b.dataset.pg;
  if (pg === "prev") HOST_PAGE--;
  else if (pg === "next") HOST_PAGE++;
  else HOST_PAGE = parseInt(pg);
  renderHosts(LAST_HOSTS);
});
// 自定义监控
safeAddEventListener("addCheckBtn", "click", () => openCheckModal(null));
safeAddEventListener("ckType", "change", updateCkTargetLabel);
safeAddEventListener("ckSaveBtn", "click", saveCheck);
safeAddEventListener("ckAdvanced", "change", () => { const b = document.getElementById("ckAdvancedBody"); if (b) b.style.display = document.getElementById("ckAdvanced").checked ? "" : "none"; }); // 高级模式展开/收起
safeAddEventListener("checksGrid", "click", e => {
  const card = e.target.closest(".check-card"); if (!card) return;
  const act = e.target.closest("[data-cact]"); if (!act) return;
  const id = card.dataset.id, check = LAST_CHECKS.find(c => c.id === id);
  const cact = act.dataset.cact;
  if (cact === "hist") { if (check) openCheckHistory(id, check.name, check.type); return; } // 历史对内置检查也开放
  if (card.dataset.builtin) return; // 内置检查仅可查看历史，无编辑/删除
  if (cact === "edit") openCheckModal(check);
  else if (cact === "del") delCheck(id);
  else if (cact === "run") {
    fetch(`${API}/checks/${encodeURIComponent(id)}/run`, { method: "POST" })
      .then(() => { toast(I18N.t("toast.check_triggered"), "ok"); setTimeout(loadChecks, 1500); })
      .catch(e => toast(I18N.t("toast.trigger_failed2") + e, "err"));
  }
});
// 概览 资源 TOP10 点击：主机详情 / 监控历史
safeAddEventListener("topPanels", "click", e => {
  // 行点击 → 主机详情
  const row = e.target.closest(".top-item");
  if (row) { openDetail(row.dataset.id, row.dataset.name); return; }
  // 监控探针点击
  const chk = e.target.closest(".checks-item");
  if (chk) { openCheckHistory(chk.dataset.checkId, chk.dataset.checkName, chk.dataset.checkType); return; }
});
// 日志导出
safeAddEventListener("exportLogBtn", "click", exportLogsCSV);
// AI 诊断审计日志：分析当前筛选出的操作/系统/终端日志中的异常与安全风险
safeAddEventListener("auditAIBtn", "click", () => {
  const rows = (typeof applyLogFilters === "function") ? applyLogFilters(LAST_LOG) : (LAST_LOG || []);
  if (!rows || !rows.length) { toast("当前没有可分析的日志", "err"); return; }
  const sample = rows.slice(0, 200).map(e => {
    const t = (typeof fmtDateTime === "function") ? fmtDateTime(e.timestamp) : (e.timestamp || "");
    return `[${t}] ${e.level || "info"} ${translateLogKind ? translateLogKind(e.kind) : (e.kind || "")} ${e.actor || ""}${e.host ? "@" + e.host : ""}: ${e.message || ""}`;
  });
  openAIAssist({
    task: "audit_diagnosis",
    title: "AI 诊断审计日志",
    mode: "analyze",
    context: `共 ${rows.length} 条日志（分析前 ${Math.min(rows.length, 200)} 条）：\n` + sample.join("\n")
  });
});
// AI 分析监控看板：基于当前主机水位 + 活跃告警，给出整体健康研判与建议（按需，补充定时巡检）
safeAddEventListener("ovAIBtn", "click", () => {
  const hosts = LAST_HOSTS || [];
  const online = hosts.filter(h => h.online).length;
  const offline = hosts.length - online;
  const usage = hosts.filter(h => h.latest && h.online).map(h => ({
    n: h.hostname || h.id,
    cpu: Math.round(h.latest.cpu_percent || 0),
    mem: Math.round(h.latest.mem_percent || 0),
    disk: Math.round(h.latest.disk_percent || 0)
  }));
  const topCpu = usage.slice().sort((a, b) => b.cpu - a.cpu).slice(0, 8).map(u => `${u.n}  CPU ${u.cpu}% 内存 ${u.mem}% 磁盘 ${u.disk}%`);
  const offlineNames = hosts.filter(h => !h.online).slice(0, 20).map(h => h.hostname || h.id);
  const alerts = (typeof LAST_ALERTS !== "undefined" ? LAST_ALERTS : []) || [];
  const alertLines = alerts.slice(0, 25).map(a => `${a.hostname || ""} ${a.type || ""} ${a.level || ""} ${a.message || ""}`.trim());
  const ctx = `【主机】总数 ${hosts.length}，在线 ${online}，离线 ${offline}` +
    (offlineNames.length ? `\n离线主机：${offlineNames.join("、")}` : "") +
    `\n\n【资源水位 TOP】\n${topCpu.join("\n") || "（暂无在线主机指标）"}` +
    `\n\n【活跃告警 ${alertLines.length}】\n${alertLines.join("\n") || "（无）"}`;
  openAIAssist({ task: "chart_analysis", title: "AI 分析监控看板", mode: "analyze", context: ctx });
});
// 批量清理离线
safeAddEventListener("purgeOfflineBtn", "click", purgeOffline);
// ===== 顶栏用户菜单 =====
(function initUserDropdown() {
  const wrap = $("topbarUserWrap");
  const btn = $("profileBtn");
  if (!wrap || !btn) return;
  // 点击头像切换下拉
  btn.addEventListener("click", function(e) {
    e.stopPropagation();
    wrap.classList.toggle("open");
  });
  // 点击外部关闭
  document.addEventListener("click", function(e) {
    if (!wrap.contains(e.target)) wrap.classList.remove("open");
  });
  // ESC 关闭
  document.addEventListener("keydown", function(e) {
    if (e.key === "Escape") wrap.classList.remove("open");
  });
  // 主题切换
  safeAddEventListener("ddThemeToggle", "click", function() { toggleTheme(); wrap.classList.remove("open"); });
  // 语言切换后刷新主题菜单文案（标签不走 data-i18n，随当前主题动态生成）
  document.addEventListener("i18n:changed", function() {
    try {
      const theme = document.documentElement.getAttribute("data-theme") || "light";
      if (typeof syncThemeIcons === "function") syncThemeIcons(theme);
    } catch (_) {}
  });
  // 语言切换（持久化到 cookie，就地重渲染所有文案，不刷新页面）
  var userDropdown = $("userDropdown");
  if (userDropdown) {
    userDropdown.addEventListener("click", function(e) {
      var b = e.target.closest("[data-lang]");
      if (b) I18N.setLang(b.dataset.lang);
    });
  }
  // 顶栏语言切换按钮组（简 / 繁 / EN）
  var tbLang = $("tbLang");
  if (tbLang) {
    tbLang.addEventListener("click", function(e) {
      var b = e.target.closest("[data-lang]");
      if (b) I18N.setLang(b.dataset.lang);
    });
  }
  // 标记当前选中的语言（涵盖顶栏与下拉两处 [data-lang] 控件）
  if (I18N.syncLangButtons) I18N.syncLangButtons();
  // 告警设置
  safeAddEventListener("ddSettings", "click", function() { openSettings(); wrap.classList.remove("open"); });
  safeAddEventListener("ddAiSettings", "click", function() {
    wrap.classList.remove("open");
    if (typeof openAIConfig === "function") openAIConfig();
  });
  // 告警设置弹窗内 Tab 切换
  safeAddEventListener("notifyTabs", "click", function(e) {
    const tab = e.target.closest(".tab");
    if (tab && tab.dataset.tab) switchNotifyTab(tab.dataset.tab);
  });
  safeAddEventListener("smsProvider", "change", function() { if (typeof updateSmsProviderFields === "function") updateSmsProviderFields(); });
  safeAddEventListener("voiceCallProvider", "change", function() { if (typeof updateVoiceProviderFields === "function") updateVoiceProviderFields(); });
  // 个人信息
  safeAddEventListener("ddProfile", "click", function() { openProfile(); wrap.classList.remove("open"); });
  // 退出登录
  safeAddEventListener("ddLogout", "click", function() { logout(); wrap.classList.remove("open"); });
  // 初始化主题标签
})();
// 旧的 profileBtn 直接打开个人信息 — 已被上面的下拉菜单替代
// #usersBtn 已废弃（用户管理并入个人信息四 Tab），仅保留 openUsers() 重定向入口
safeAddEventListener("profileTabs", "click", function (e) {
  const t = e.target.closest(".tab");
  if (t && t.dataset.ptab) switchProfileTab(t.dataset.ptab);
});
safeAddEventListener("userAddBtn", "click", () => openUserEdit(null));
safeAddEventListener("globalMfaChk", "change", async () => {
  const chk = $("globalMfaChk");
  if (!chk) return;
  const required = chk.checked;
  chk.disabled = true;
  try {
    const r = await fetch(`${API}/mfa/global`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ required }) });
    const j = await r.json().catch(() => ({}));
    if (r.ok) toast(required ? I18N.t("toast.global_mfa_on") : I18N.t("toast.global_mfa_off"), "ok");
    else { toast(j.error || I18N.t("toast.operation_failed"), "err"); chk.checked = !required; }
  } catch (e) { toast(I18N.t("toast.network_error"), "err"); chk.checked = !required; }
  chk.disabled = false;
});
safeAddEventListener("usersList", "click", async e => {
  const btn = e.target.closest("[data-act]"); if (!btn) return;
  const row = e.target.closest(".user-row"); if (!row) return;
  const name = row.dataset.name, act = btn.dataset.act;
  if (act === "edit") {
    const users = await fetch(`${API}/users`).then(r => r.json()).catch(() => []);
    const u = Array.isArray(users) && users.find(x => x.username === name);
    if (u) openUserEdit(u);
  } else { usersAction(name, act); }
});
safeAddEventListener("pfSaveBtn", "click", saveProfile);
safeAddEventListener("pfPwdBtn", "click", changePassword);
// 首次登录·安全初始化弹窗：提交按钮 + 确认密码框回车提交
safeAddEventListener("initSubmitBtn", "click", submitInitSetup);
safeAddEventListener("initPass2", "keydown", e => { if (e.key === "Enter") { e.preventDefault(); submitInitSetup(); } });
safeAddEventListener("pfTermPwdBtn", "click", submitTermPwdChange);
safeAddEventListener("mfaToggleChk", "change", () => {
  const chk = $("mfaToggleChk");
  if (chk) chk.checked = MFA_ENABLED; // revert immediately; renderMfaState will update on success
  MFA_ENABLED ? openMfaDisable() : openMfaSetup();
});
safeAddEventListener("logoutBtn", "click", logout);
// 登录页找回入口
safeAddEventListener("forgotPassLink", "click", openRecoverPass);

// 登录：账号框同时支持用户名或已绑定手机号；可选切到短信验证码模式
let LOGIN_TYPE = "username"; // "username" | "sms"
let _loginSmsCooldownTimer = null;

function applyLoginType(type) {
  LOGIN_TYPE = type === "sms" ? "sms" : "username";
  const phoneHint = $("loginPhoneHint");
  const passField = $("loginPassField");
  const smsField = $("loginSmsField");
  const switchSms = $("loginSwitchSms");
  const userEl = $("loginUser");
  const labelEl = $("loginUserLabel");
  if (LOGIN_TYPE === "sms") {
    if (labelEl) labelEl.textContent = I18N.t("profile.phone") || "手机号";
    if (userEl) {
      userEl.placeholder = I18N.t("login.phone_placeholder") || "输入手机号";
      userEl.type = "tel";
      userEl.maxLength = 11;
    }
    if (passField) passField.style.display = "none";
    if (smsField) smsField.style.display = "";
    if (phoneHint) {
      phoneHint.style.display = "";
      phoneHint.setAttribute("data-i18n", "login.sms_hint");
      phoneHint.textContent = I18N.t("login.sms_hint") || "使用已绑定手机号与短信验证码登录";
    }
    if (switchSms) switchSms.textContent = I18N.t("login.switch_account") || "账号密码登录";
  } else {
    if (labelEl) labelEl.textContent = I18N.t("login.account") || "账号";
    if (userEl) {
      userEl.placeholder = I18N.t("login.account_placeholder") || "用户名或手机号";
      userEl.type = "text";
      userEl.maxLength = 524288;
    }
    if (passField) passField.style.display = "";
    if (smsField) smsField.style.display = "none";
    if (phoneHint) phoneHint.style.display = "none";
    if (switchSms) switchSms.textContent = I18N.t("login.sms_login_btn") || "短信验证码登录";
  }
  if (userEl) userEl.value = "";
  const passEl = $("loginPass"); if (passEl) passEl.value = "";
  const smsCodeEl = $("loginSmsCode"); if (smsCodeEl) smsCodeEl.value = "";
  const loginErrEl = $("loginErr"); if (loginErrEl) loginErrEl.textContent = "";
  const codeField = $("loginCodeField"); if (codeField) codeField.style.display = "none";
}

safeAddEventListener("loginSwitchSms", "click", (e) => {
  e.preventDefault();
  applyLoginType(LOGIN_TYPE === "sms" ? "username" : "sms");
});

safeAddEventListener("loginSendSmsBtn", "click", async () => {
  const btn = $("loginSendSmsBtn");
  const phone = ($("loginUser") && $("loginUser").value || "").trim();
  const loginErrEl = $("loginErr");
  if (!phone) {
    if (loginErrEl) loginErrEl.textContent = I18N.t("login.phone_required") || "请输入手机号";
    else if (typeof toast === "function") toast(I18N.t("login.phone_required") || "请输入手机号", "err");
    return;
  }
  if (btn && btn.disabled) return;
  try {
    const r = await fetchWithTimeout(`${API}/login/sms-code`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ phone })
    }, 15000);
    const j = await r.json().catch(() => ({}));
    if (!r.ok) {
      const msg = j.error || j.message || I18N.t("login.sms_send_failed") || "短信发送失败";
      if (loginErrEl) loginErrEl.textContent = msg;
      else if (typeof toast === "function") toast(msg, "err");
      return;
    }
    const okMsg = j.message || I18N.t("login.sms_sent") || "验证码已发送，请检查手机短信";
    if (typeof toast === "function") toast(okMsg, "ok");
    else if (loginErrEl) loginErrEl.textContent = okMsg;
    if (btn) {
      let left = 60;
      const label = I18N.t("login.send_sms") || "发送验证码";
      btn.disabled = true;
      btn.textContent = `${left}s`;
      if (_loginSmsCooldownTimer) clearInterval(_loginSmsCooldownTimer);
      _loginSmsCooldownTimer = setInterval(() => {
        left -= 1;
        if (left <= 0) {
          clearInterval(_loginSmsCooldownTimer);
          _loginSmsCooldownTimer = null;
          btn.disabled = false;
          btn.textContent = label;
        } else {
          btn.textContent = `${left}s`;
        }
      }, 1000);
    }
    const smsCodeEl = $("loginSmsCode");
    if (smsCodeEl) setTimeout(() => smsCodeEl.focus(), 30);
  } catch (err) {
    const msg = err.name === "AbortError"
      ? (I18N.t("toast.login_timeout_mfa") || "请求超时")
      : (I18N.t("login.sms_send_failed") || "短信发送失败");
    if (loginErrEl) loginErrEl.textContent = msg;
    else if (typeof toast === "function") toast(msg, "err");
  }
});

safeAddEventListener("loginForm", "submit", async e => {
  e.preventDefault();
  const loginErrEl = $("loginErr");
  if (loginErrEl) loginErrEl.textContent = "";
  const submitBtn = e.target.querySelector('button[type="submit"]');
  await withLoading(submitBtn, async () => {
    let fetched = false; // fetch 是否已成功返回（用于区分"网络失败" vs "登录后处理出错"）
    try {
      const codeEl = $("loginCode");
      // 去掉自动填充可能带入的空格/短横线，只提交 6 位数字
      const totpCode = codeEl ? String(codeEl.value || "").replace(/\D/g, "").slice(0, 6) : "";
      if (codeEl && totpCode) codeEl.value = totpCode;
      const payload = {
        username: $("loginUser").value.trim(),
        login_type: LOGIN_TYPE,
        code: totpCode
      };
      if (LOGIN_TYPE === "sms") {
        const smsEl = $("loginSmsCode");
        payload.sms_code = smsEl ? String(smsEl.value || "").replace(/\D/g, "").slice(0, 8) : "";
        if (!payload.sms_code) {
          if (loginErrEl) loginErrEl.textContent = I18N.t("login.sms_code_required") || "请输入短信验证码";
          return;
        }
      } else {
        payload.password = $("loginPass").value;
      }
      const r = await fetchWithTimeout(`${API}/login`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      }, 15000);
      fetched = true; // 请求已成功返回——之后任何错误都不是"网络连接失败"
      const j = await r.json().catch(() => ({}));
      if (r.ok && j.mfa_required) {
        const f = $("loginCodeField"); if (f) f.style.display = "";
        if (codeEl) { codeEl.value = ""; codeEl.focus(); }
        if (loginErrEl) loginErrEl.textContent = I18N.t("mfa.login_totp");
      }
      else if (r.ok && j.require_mfa_setup) {
        openMfaSetup(true);
      }
      else if (r.ok && j.must_change_password) {
        // v5.4.0: admin password was reset — force password change
        let user;
        try {
          user = await fetchWithTimeout(`${API}/me`, {}, 10000).then(x => x.json());
        } catch (_) {
          user = { username: $("loginUser").value.trim(), display_name: "" };
        }
        setUser(user);
        $("loginView").classList.remove("show");
        startApp();
        // 强制进入「安全初始化」弹窗：需修改用户名 + 密码后方可进入控制台
        setTimeout(() => openInitSetup(), 300);
      }
      else if (r.ok) {
        // Post-login /me fetch: wrap in try/catch so a transient network
        // hiccup doesn't leave the user stuck on the login page after
        // successful authentication.
        let user;
        try {
          user = await fetchWithTimeout(`${API}/me`, {}, 10000).then(x => x.json());
        } catch (_) {
          // Login succeeded but /me failed — proceed anyway, the next poll
          // will populate user info. Better than showing an error after
          // the user already typed their credentials correctly.
          user = { username: $("loginUser").value.trim(), display_name: "" };
        }
        setUser(user);
        const loginViewEl = $("loginView");
        if (loginViewEl) loginViewEl.classList.remove("show");
        startApp();
      }
      else {
        // MFA 失败：清空口令框，避免用户用同一码反复提交触发「已使用」假阴性
        if (j.code === "totp_replay" || j.code === "totp_invalid" || (codeEl && codeEl.value)) {
          if (codeEl) { codeEl.value = ""; setTimeout(() => codeEl.focus(), 30); }
        }
        if (loginErrEl) loginErrEl.textContent = j.error || I18N.t("toast.login_failed");
      }
    } catch (err) {
      if (!fetched) {
        // 网络层失败（fetch 未成功返回）：区分超时 vs 一般网络错误。
        // 超时后口令可能已在服务端消费，清空输入避免立刻用同一码重试。
        const codeEl = $("loginCode");
        if (codeEl && codeEl.value) {
          codeEl.value = "";
          setTimeout(() => codeEl.focus(), 30);
        }
        const msg = err.name === "AbortError"
          ? I18N.t("toast.login_timeout_mfa")
          : I18N.t("toast.login_network_error");
        if (loginErrEl) loginErrEl.textContent = msg;
      } else {
        // 登录本身已成功（会话已建立），只是登录后初始化出错——绝不误报"网络连接失败"，
        // 记录到 console 并照常进入控制台，避免把用户卡在登录页。
        console.error("登录成功但初始化出错：", err);
        const lv = $("loginView"); if (lv) lv.classList.remove("show");
        try { startApp(); } catch (_) {}
      }
    }
  });
});

/* ---------- 布局宽度切换（已移除，由默认值控制） ---------- */

/* ---------- 自定义监控视图切换（列表 / 胶囊） ---------- */
safeAddEventListener("checkViewToggle", "click", e => {
  const b = e.target.closest(".vt-btn"); if (!b) return;
  setCheckView(b.dataset.cview);
});
safeAddEventListener("hostRefreshBtn", "click", () => { if (typeof refresh === "function") refresh(); });
safeAddEventListener("hostViewToggle", "click", e => {
  const b = e.target.closest(".vt-btn"); if (!b) return;
  setHostView(b.dataset.hview);
});

// 读取本地偏好并应用（视图 / 布局宽度）
function initPrefs() {
  try { const cv = localStorage.getItem("aiops_check_view"); if (cv === "pill" || cv === "list") CHECK_VIEW = cv; } catch (e) {}
  try { const hv = localStorage.getItem("aiops_host_view"); if (hv === "list" || hv === "card") HOST_VIEW = hv; } catch (e) {}
  // 默认卡片视图：即使 localStorage 无值也确保 HOST_VIEW 为 "card"
  if (HOST_VIEW !== "list" && HOST_VIEW !== "card") HOST_VIEW = "card";
  document.querySelectorAll("#checkViewToggle .vt-btn").forEach(b => b.classList.toggle("active", b.dataset.cview === CHECK_VIEW));
  document.querySelectorAll("#hostViewToggle .vt-btn").forEach(b => b.classList.toggle("active", b.dataset.hview === HOST_VIEW));
}

initPrefs();
CUR_CATS = getSelectedCats();

/* ---------- P2-#18: Keyboard shortcuts 1-5 switch views ---------- */
document.addEventListener("keydown", e => {
  if (e.target.tagName === "INPUT" || e.target.tagName === "TEXTAREA" || e.target.tagName === "SELECT") return;
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const views = ["overview", "hosts", "checks", "alerts", "automation", "log"];
  const idx = parseInt(e.key) - 1;
  if (idx >= 0 && idx < views.length) {
    e.preventDefault();
    switchView(views[idx]);
  }
});

/* ---------- Alert filter helpers ---------- */
function filterAlertsByType(type) {
  ALERT_TYPE = type;
  document.querySelectorAll("#alertFilter .chip-btn").forEach(b => b.classList.toggle("active", b.dataset.atype === type));
  renderAlerts(LAST_ALERTS);
}
let LAST_ALERTS = [];
