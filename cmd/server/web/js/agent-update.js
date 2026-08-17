/* ---------- Agent fleet update (batch push from server) ---------- */
let AGENT_TARGET_VERSION = "";
let AGENT_TARGET_COMPARABLE = false;
let AGENT_UPDATE_SELECTED = new Set();
let AGENT_UPDATE_JOB = null;
let AGENT_UPDATE_TIMER = null;
let AGENT_UPDATE_MODE = false;
let AGENT_UPDATE_POLL_LEFT = 0;

function normalizeAgentVerUI(v) {
  return String(v || "").trim().replace(/^v/i, "").toLowerCase();
}

function isComparableAgentVerUI(v) {
  const n = normalizeAgentVerUI(v);
  if (!n || n === "aiops" || n === "dev") return false;
  return /^[0-9]/.test(n);
}

function compareAgentVerUI(a, b) {
  const ap = normalizeAgentVerUI(a).split(".");
  const bp = normalizeAgentVerUI(b).split(".");
  const n = Math.max(ap.length, bp.length);
  for (let i = 0; i < n; i++) {
    const ai = parseInt(String(ap[i] || "0").replace(/\D.*$/, ""), 10) || 0;
    const bi = parseInt(String(bp[i] || "0").replace(/\D.*$/, ""), 10) || 0;
    if (ai < bi) return -1;
    if (ai > bi) return 1;
  }
  return 0;
}

function hostAgentOutdated(h) {
  if (!h || !h.online) return false;
  if (!AGENT_TARGET_COMPARABLE && !isComparableAgentVerUI(AGENT_TARGET_VERSION)) return false;
  const target = normalizeAgentVerUI(AGENT_TARGET_VERSION);
  if (!isComparableAgentVerUI(target)) return false;
  const cur = normalizeAgentVerUI(h.agent_version);
  if (!cur || !isComparableAgentVerUI(cur)) return true;
  return compareAgentVerUI(cur, target) < 0;
}

function agentVersionBadgeHTML(h) {
  const ver = (h && h.agent_version) ? String(h.agent_version) : "";
  if (!ver) {
    return `<span class="agent-ver unknown" title="${esc(I18N.t("agent_update.unknown_ver", "未上报 Agent 版本（旧客户端）"))}">Agent —</span>`;
  }
  const outdated = hostAgentOutdated(h);
  const cls = outdated ? "agent-ver outdated" : "agent-ver ok";
  let tip = I18N.t("agent_update.up_to_date", "已是目标版本");
  if (outdated) {
    tip = I18N.t("agent_update.outdated_tip", "落后目标版本 {0}").replace("{0}", AGENT_TARGET_VERSION || "?");
  } else if (!isComparableAgentVerUI(AGENT_TARGET_VERSION)) {
    tip = I18N.t("agent_update.target_uncomparable", "当前服务端版本不可比较（需正式版本号）");
  }
  return `<span class="${cls}" title="${esc(tip)}">Agent ${esc(ver)}</span>`;
}

// A spacer keeps the list columns lined up while select mode is on: offline
// hosts get no checkbox, and without it every offline row shifts left of the
// header and its neighbours.
function agentSelectSpacerHTML() {
  return AGENT_UPDATE_MODE ? `<span class="agent-sel spacer" aria-hidden="true"></span>` : "";
}

function agentSelectCheckboxHTML(h) {
  if (!AGENT_UPDATE_MODE) return "";
  if (!h || !h.online) return agentSelectSpacerHTML();
  const checked = AGENT_UPDATE_SELECTED.has(h.id) ? "checked" : "";
  const label = I18N.t("agent_update.select", "选择以批量更新");
  return `<label class="agent-sel" title="${esc(label)}" data-act="agent-sel-wrap">
    <input type="checkbox" data-act="agent-sel" data-id="${esc(h.id)}" aria-label="${esc(label)}" ${checked}>
  </label>`;
}

function agentHostCardClass(h) {
  return hostAgentOutdated(h) ? " outdated-agent" : "";
}

async function refreshAgentTargetVersion() {
  try {
    const m = await fetch(`${API}/agent-dist/manifest`, { credentials: "same-origin" }).then(r => r.json());
    AGENT_TARGET_VERSION = (m && (m.version || (m.artifacts && m.artifacts[0] && m.artifacts[0].version))) || "";
    AGENT_TARGET_COMPARABLE = !!(m && m.comparable) || isComparableAgentVerUI(AGENT_TARGET_VERSION);
  } catch (e) {
    AGENT_TARGET_VERSION = "";
    AGENT_TARGET_COMPARABLE = false;
  }
}

function toggleAgentHostSelected(id, on) {
  if (!id) return;
  if (on) AGENT_UPDATE_SELECTED.add(id);
  else AGENT_UPDATE_SELECTED.delete(id);
  renderAgentUpdateBar();
}

function setAgentUpdateMode(on) {
  AGENT_UPDATE_MODE = !!on;
  if (!AGENT_UPDATE_MODE) {
    AGENT_UPDATE_SELECTED.clear();
  }
  renderAgentUpdateBar();
  if (typeof renderHosts === "function") renderHosts(LAST_HOSTS);
}

function renderAgentUpdateBar() {
  const bar = document.getElementById("agentUpdateBar");
  if (!bar) return;
  const n = AGENT_UPDATE_SELECTED.size;
  if (!AGENT_UPDATE_MODE && !AGENT_UPDATE_JOB) {
    bar.style.display = "none";
    bar.innerHTML = "";
    return;
  }
  bar.style.display = "";
  let jobHtml = "";
  if (AGENT_UPDATE_JOB) {
    const j = AGENT_UPDATE_JOB;
    const hosts = j.hosts || [];
    const ok = hosts.filter(x => x.status === "success").length;
    const fail = hosts.filter(x => x.status === "failed").length;
    const skip = hosts.filter(x => x.status === "skipped").length;
    const run = hosts.filter(x => x.status === "running" || x.status === "pending" || x.status === "pending_verify").length;
    const stKey = "agent_update.status_" + (j.status || "queued");
    const stLabel = I18N.t(stKey, j.status || "queued");
    jobHtml = `<div class="agent-job" aria-live="polite">
      <span><b>${esc(I18N.t("agent_update.job", "更新任务"))}</b> <span class="mono">${esc(j.id)}</span>
      · ${esc(stLabel)}
      · ${esc(I18N.t("agent_update.job_ok", "成功"))}=${ok}
      · ${esc(I18N.t("agent_update.job_fail", "失败"))}=${fail}
      · ${esc(I18N.t("agent_update.job_skip", "跳过"))}=${skip}
      · ${esc(I18N.t("agent_update.job_run", "进行中"))}=${run}</span>
      <span class="agent-update-actions">
        <button type="button" class="btn sm" data-act="agent-job-detail">${esc(I18N.t("agent_update.detail", "详情"))}</button>
        ${fail ? `<button type="button" class="btn sm danger" data-act="agent-update-rollback">${esc(I18N.t("agent_update.rollback", "回滚失败主机.bak"))}</button>` : ""}
        <button type="button" class="btn sm" data-act="agent-job-dismiss">${esc(I18N.t("agent_update.dismiss", "关闭任务"))}</button>
      </span>
    </div>`;
  }
  const targetHint = !AGENT_TARGET_COMPARABLE && AGENT_TARGET_VERSION
    ? ` · <span class="muted">${esc(I18N.t("agent_update.target_uncomparable", "当前服务端版本不可比较（需正式版本号）"))}</span>`
    : (AGENT_TARGET_VERSION ? ` · ${esc(I18N.t("agent_update.target", "目标"))} <b class="mono">${esc(AGENT_TARGET_VERSION)}</b>` : "");
  bar.innerHTML = `
    <div class="agent-update-inner">
      <span>${esc(I18N.t("agent_update.selected", "已选 {0} 台").replace("{0}", String(n)))}${targetHint}</span>
      <span class="agent-update-actions">
        <button type="button" class="btn sm primary" data-act="agent-update-run" ${n ? "" : "disabled"}>${esc(I18N.t("agent_update.run", "开始更新"))}</button>
        <button type="button" class="btn sm" data-act="agent-update-outdated" ${AGENT_TARGET_COMPARABLE ? "" : "disabled"}>${esc(I18N.t("agent_update.select_outdated", "选中落后"))}</button>
        <button type="button" class="btn sm" data-act="agent-update-clear">${esc(I18N.t("agent_update.clear", "清空选择"))}</button>
        <button type="button" class="btn sm" data-act="agent-update-close">${esc(I18N.t("agent_update.close_mode", "退出"))}</button>
      </span>
    </div>
    ${jobHtml}`;
}

async function startAgentFleetUpdate(hostIds, opts) {
  opts = opts || {};
  if (!hostIds || !hostIds.length) {
    toast(I18N.t("agent_update.need_hosts", "请先勾选在线主机"), "err");
    return;
  }
  if (!opts.skipConfirm) {
    const target = AGENT_TARGET_VERSION || "?";
    const msg = opts.rollback
      ? I18N.t("agent_update.rollback_confirm", "将对 {0} 台主机执行 .bak 回滚。是否继续？").replace("{0}", String(hostIds.length))
      : I18N.t(
        "agent_update.confirm",
        "将对 {0} 台在线主机推送 Agent 更新到 {1}（SHA-256 校验，配置与 host_id 保留）。是否继续？"
      ).replace("{0}", String(hostIds.length)).replace("{1}", target);
    if (typeof uiConfirm === "function"
      ? !(await uiConfirm({ title: I18N.t("agent_update.title", "Agent 更新"), message: msg, tone: "warn" }))
      : !confirm(msg)) return;
  }
  try {
    const r = await fetch(`${API}/agents/update`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        host_ids: hostIds,
        force: !!opts.force,
        rollback: !!opts.rollback,
        confirm: true
      })
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) {
      toast((j && j.error) || I18N.t("agent_update.failed", "更新任务创建失败"), "err");
      return;
    }
    AGENT_UPDATE_JOB = j;
    AGENT_UPDATE_MODE = true;
    renderAgentUpdateBar();
    toast(I18N.t("agent_update.started", "更新任务已创建"), "ok");
    pollAgentUpdateJob(j.id);
  } catch (e) {
    toast(I18N.t("agent_update.failed", "更新任务创建失败") + ": " + e, "err");
  }
}

function stopAgentUpdatePoll() {
  if (AGENT_UPDATE_TIMER) {
    clearInterval(AGENT_UPDATE_TIMER);
    AGENT_UPDATE_TIMER = null;
  }
  AGENT_UPDATE_POLL_LEFT = 0;
}

// 轮询预算必须覆盖服务端的**整条阶梯**，而不是只覆盖第一段 verify：
//
//   module 助手 verify（5 分钟）→ legacy 救援 exec（最长 600s）→ 救援 verify（5 分钟）
//
// 服务端的 agentUpdateJobFinalizeWindow 就是按这条链路算的，22 分钟。原值 200 次 ×
// 2s ≈ 6.5 分钟，只够走完第一段的一半。
//
// 后果只落在 Windows 上，原因是两边进 pending_verify 的**含义不同**：
//   - Linux：模块返回时二进制**已经换好了**（rename 允许覆盖运行中的 ELF），剩下的
//     只是拉起服务，版本号通常几十秒内就追上，第一段 verify 就收摊；
//   - Windows：运行中的 PE 改不了，模块返回时**一个字节都还没换**，换版要等助手在
//     Agent 被杀之后的独立进程里做完；一旦助手没做成，就要走 legacy 救援——而救援
//     阶梯（5 分钟 verify + 最长 600s exec + 再 5 分钟 verify）按构造只对 Windows 开
//     （rescueWindowsAgentUpdate 对非 Windows 直接返回 false）。
// 也就是说：超出 6.5 分钟的那一段，永远只有 Windows 主机会走到。操作台上看到的
// 「Windows Agent 升不上去」，有很大一部分其实是这条上限提前弹出来的红字。
const AGENT_UPDATE_POLL_TICKS = 720; // 24 min at 2s，覆盖服务端 22 分钟的完整阶梯
function pollAgentUpdateJob(id) {
  stopAgentUpdatePoll();
  AGENT_UPDATE_POLL_LEFT = AGENT_UPDATE_POLL_TICKS;
  AGENT_UPDATE_TIMER = setInterval(async () => {
    AGENT_UPDATE_POLL_LEFT -= 1;
    if (AGENT_UPDATE_POLL_LEFT <= 0) {
      stopAgentUpdatePoll();
      // 中性色，不是 "err"：前端不看了 ≠ 升级失败了，服务端仍在跑校验/救援。
      // 红字会把一次正常的长校验直接读成「Windows 又没升上去」。
      toast(I18N.t("agent_update.poll_timeout",
        "前端已停止轮询；服务端仍在校验该任务，稍后刷新或查看任务详情即可"));
      return;
    }
    try {
      const r = await fetch(`${API}/agents/update/jobs/${encodeURIComponent(id)}`, { credentials: "same-origin" });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) {
        stopAgentUpdatePoll();
        toast((j && j.error) || I18N.t("agent_update.job_lost", "更新任务不存在或已过期"), "err");
        return;
      }
      AGENT_UPDATE_JOB = j;
      renderAgentUpdateBar();
      if (j && j.status === "done") {
        stopAgentUpdatePoll();
        const fail = (j.hosts || []).filter(x => x.status === "failed").length;
        if (fail > 0) {
          toast(I18N.t("agent_update.done_with_fails", "Agent 更新结束：有 {0} 台失败").replace("{0}", String(fail)), "err");
        } else {
          toast(I18N.t("agent_update.done", "Agent 更新任务已结束"), "ok");
        }
        if (typeof fetchHostsList === "function") {
          fetchHostsList({ force: true }).then(list => {
            if (typeof renderHosts === "function") renderHosts(list || LAST_HOSTS);
          }).catch(() => {});
        }
      }
    } catch (e) { /* ignore transient */ }
  }, 2000);
}

async function rollbackFailedAgentHosts() {
  const j = AGENT_UPDATE_JOB;
  if (!j) return;
  const ids = (j.hosts || []).filter(h => h.status === "failed").map(h => h.host_id).filter(Boolean);
  if (!ids.length) {
    toast(I18N.t("agent_update.no_failed", "没有失败主机可回滚"), "err");
    return;
  }
  const msg = I18N.t(
    "agent_update.rollback_confirm",
    "将对 {0} 台失败主机执行 .bak 回滚（需已具备 agent_update 模块）。是否继续？"
  ).replace("{0}", String(ids.length));
  if (typeof uiConfirm === "function"
    ? !(await uiConfirm({ title: I18N.t("agent_update.rollback", "回滚"), message: msg, tone: "danger" }))
    : !confirm(msg)) return;
  await startAgentFleetUpdate(ids, { rollback: true, force: true, skipConfirm: true });
}

function showAgentUpdateJobDetail() {
  const j = AGENT_UPDATE_JOB;
  if (!j) return;
  // 消息**不能**压成一行再截 160 字：Windows 失败信息里真正有用的部分全在结尾——
  // 服务端拼上去的 " | host evidence: …"（助手日志尾巴）和 Agent 捎回来的
  // "--- 上一轮升级助手留下的记录 ---"。截断恰好把唯一能解释原因的那一段丢掉，
  // 留下的是一句谁都看得懂但什么也没说的英文。「Windows 升不上去查不出原因」
  // 有一半是这么来的：证据一路运到了操作台，最后一步被前端切掉了。
  const lines = (j.hosts || []).map(h => {
    const head = `${h.hostname || h.host_id}\t${h.status}\t${h.method || ""}\t${h.from_version || ""}`;
    const msg = String(h.message || "").trim();
    if (!msg) return head;
    return head + "\n    " + msg.replace(/\r/g, "").replace(/\n/g, "\n    ");
  });
  const box = document.getElementById("agentUpdateDetailPre");
  const mask = document.getElementById("agentUpdateDetailMask");
  if (box && mask) {
    box.textContent = `${j.id}\n${j.status}\n\n` + lines.join("\n");
    mask.classList.add("show");
    return;
  }
  alert(`${j.id}\n${j.status}\n\n` + lines.join("\n"));
}

function selectOutdatedOnlineHosts() {
  if (!AGENT_TARGET_COMPARABLE && !isComparableAgentVerUI(AGENT_TARGET_VERSION)) {
    toast(I18N.t("agent_update.target_uncomparable", "当前服务端版本不可比较（需正式版本号）"), "err");
    return;
  }
  AGENT_UPDATE_SELECTED.clear();
  const hosts = Array.isArray(LAST_HOSTS) ? LAST_HOSTS : [];
  hosts.forEach(h => {
    if (h.online && hostAgentOutdated(h)) AGENT_UPDATE_SELECTED.add(h.id);
  });
  renderAgentUpdateBar();
  if (typeof renderHosts === "function") renderHosts(LAST_HOSTS);
  if (!AGENT_UPDATE_SELECTED.size) {
    toast(I18N.t("agent_update.no_outdated", "没有检测到落后的在线主机"), "err");
  }
}

document.addEventListener("click", (e) => {
  const t = e.target;
  if (!t || !t.closest) return;
  if (t.closest("#agentUpdateBtn")) {
    setAgentUpdateMode(true);
    refreshAgentTargetVersion().then(() => {
      renderAgentUpdateBar();
      if (typeof renderHosts === "function") renderHosts(LAST_HOSTS);
    });
    return;
  }
  const el = t.closest("[data-act]");
  if (!el) return;
  const act = el.getAttribute("data-act");
  if (act === "agent-sel" || act === "agent-sel-wrap") {
    e.stopPropagation();
    const input = act === "agent-sel" ? el : el.querySelector("input[data-act='agent-sel']");
    if (input && input.dataset.id) {
      // label click toggles checkbox before this handler in some browsers; read checked after microtask
      setTimeout(() => toggleAgentHostSelected(input.dataset.id, !!input.checked), 0);
    }
    return;
  }
  if (act === "agent-update-run") {
    startAgentFleetUpdate(Array.from(AGENT_UPDATE_SELECTED));
    return;
  }
  if (act === "agent-update-outdated") {
    selectOutdatedOnlineHosts();
    return;
  }
  if (act === "agent-update-clear") {
    AGENT_UPDATE_SELECTED.clear();
    renderAgentUpdateBar();
    if (typeof renderHosts === "function") renderHosts(LAST_HOSTS);
    return;
  }
  if (act === "agent-update-close") {
    stopAgentUpdatePoll();
    AGENT_UPDATE_JOB = null;
    setAgentUpdateMode(false);
    return;
  }
  if (act === "agent-job-detail") {
    showAgentUpdateJobDetail();
    return;
  }
  if (act === "agent-job-dismiss") {
    AGENT_UPDATE_JOB = null;
    renderAgentUpdateBar();
    return;
  }
  if (act === "agent-update-rollback") {
    rollbackFailedAgentHosts();
    return;
  }
  if (act === "agent-update-detail-close") {
    const mask = document.getElementById("agentUpdateDetailMask");
    if (mask) mask.classList.remove("show");
  }
});

document.addEventListener("change", (e) => {
  const t = e.target;
  if (!t || t.getAttribute("data-act") !== "agent-sel") return;
  e.stopPropagation();
  toggleAgentHostSelected(t.getAttribute("data-id"), !!t.checked);
});

// Warm target version when hosts view is used.
refreshAgentTargetVersion().then(() => {
  if (typeof renderHosts === "function" && Array.isArray(LAST_HOSTS) && LAST_HOSTS.length) {
    renderHosts(LAST_HOSTS);
  }
});
