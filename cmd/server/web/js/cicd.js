/* CI/CD 流水线（GitLab / GitHub Actions / Gitee Go）
 * 与 v2 版 CicdView 同源后端接口：查看 / 触发 / 重跑 / 取消 / 失败 AI 诊断。
 * 读操作 viewer 可用；触发/重跑/取消/连接变更走服务端 routeAllowed 管理员门禁。 */

let CICD_CONNS = [];
let CICD_RUNS = [];
let CICD_DETAIL_RUN = null;

const CICD_STATUS_TXT = {
  success: "成功", failed: "失败", running: "运行中", pending: "等待中",
  canceled: "已取消", skipped: "已跳过", unknown: "未知",
};
const CICD_STATUS_CLS = {
  success: "ok", failed: "crit", running: "info", pending: "warn",
  canceled: "", skipped: "", unknown: "",
};
function cicdStatusBadge(s) {
  const cls = CICD_STATUS_CLS[s] || "";
  return `<span class="badge ${cls}">${esc(CICD_STATUS_TXT[s] || s || "-")}</span>`;
}

async function cicdFetch(path, opts) {
  const r = await fetch(`${API}${path}`, Object.assign({ credentials: "same-origin" }, opts || {}));
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error((j && j.error) || ("HTTP " + r.status));
  return j;
}

/* ---------------- 页面入口 ---------------- */
async function loadCICD() {
  try {
    const conns = await cicdFetch("/cicd/connections");
    CICD_CONNS = Array.isArray(conns.connections) ? conns.connections : [];
  } catch (e) {
    CICD_CONNS = [];
    toast("读取 CI/CD 连接失败：" + e.message, "err");
  }
  renderCICDConnFilter();
  renderCICDConnList();
  await Promise.all([loadCICDOverview(), loadCICDRuns()]);
}

async function loadCICDOverview() {
  const box = $("cicdKpis");
  if (!box) return;
  try {
    const ov = await cicdFetch("/cicd/overview");
    const rate = Number(ov.success_rate || 0);
    const avgSec = Math.round(Number(ov.avg_duration_ms || 0) / 1000);
    box.innerHTML = `
      <div class="stat-card"><div class="sv">${ov.enabled || 0}<span class="muted" style="font-size:12px">/${ov.connections || 0}</span></div><div class="sk">启用/总连接</div></div>
      <div class="stat-card"><div class="sv">${ov.total_runs || 0}</div><div class="sk">近期流水线</div></div>
      <div class="stat-card"><div class="sv ${ov.running ? "warn" : ""}">${ov.running || 0}</div><div class="sk">运行中/等待中</div></div>
      <div class="stat-card"><div class="sv ${ov.failed ? "crit" : "ok"}">${ov.failed || 0}</div><div class="sk">失败</div></div>
      <div class="stat-card"><div class="sv ${rate >= 80 ? "ok" : rate >= 50 ? "warn" : "crit"}">${rate.toFixed(0)}%</div><div class="sk">成功率</div></div>
      <div class="stat-card"><div class="sv">${avgSec ? fmtDur(avgSec) : "-"}</div><div class="sk">平均耗时</div></div>`;
  } catch {
    box.innerHTML = "";
  }
}

function renderCICDConnFilter() {
  const sel = $("cicdConnFilter");
  if (!sel) return;
  const cur = sel.value;
  const opts = [`<option value="">全部连接</option>`].concat(
    CICD_CONNS.map(c => `<option value="${esc(c.id)}">${esc(c.name)}（${esc(c.provider)}）</option>`)
  );
  sel.innerHTML = opts.join("");
  sel.value = CICD_CONNS.some(c => c.id === cur) ? cur : "";
  const trig = $("cicdTriggerConn");
  if (trig) {
    trig.innerHTML = CICD_CONNS.filter(c => c.enabled).map(c =>
      `<option value="${esc(c.id)}">${esc(c.name)} · ${esc(c.project)}</option>`).join("");
  }
}

async function loadCICDRuns() {
  const list = $("cicdRunsList"), empty = $("cicdRunsEmpty"), errBox = $("cicdSyncErrors");
  if (!list) return;
  const connId = ($("cicdConnFilter") || {}).value || "";
  const status = ($("cicdStatusFilter") || {}).value || "";
  const qs = new URLSearchParams();
  qs.set("limit", "50");
  if (connId) qs.set("connection_id", connId);
  if (status) qs.set("status", status);
  list.innerHTML = `<div class="hint" style="padding:12px">加载中…</div>`;
  try {
    const j = await cicdFetch("/cicd/runs?" + qs.toString());
    CICD_RUNS = Array.isArray(j.runs) ? j.runs : [];
    // 同步错误（连接级）提示在工具栏，便于定位坏连接。
    const errs = j.errors || {};
    const errTxt = Object.keys(errs).filter(k => errs[k]).map(k => {
      const c = CICD_CONNS.find(x => x.id === k);
      return `${c ? c.name : k}: ${errs[k]}`;
    }).join("；");
    if (errBox) errBox.textContent = errTxt ? ("⚠ " + errTxt) : "";
    renderCICDRuns();
  } catch (e) {
    list.innerHTML = "";
    if (empty) {
      empty.style.display = "";
      empty.innerHTML = `<span class="ds-empty-icon">⚠️</span>读取流水线失败：${esc(e.message)}`;
    }
  }
}

function renderCICDRuns() {
  const list = $("cicdRunsList"), empty = $("cicdRunsEmpty");
  if (!list) return;
  if (!CICD_RUNS.length) {
    list.innerHTML = "";
    if (empty) {
      empty.style.display = "";
      // 有连接但无记录 vs 未接入连接，提示文案不同。
      empty.innerHTML = CICD_CONNS.length
        ? `<span class="ds-empty-icon">🔍</span>没有符合条件的流水线记录。可调整筛选条件，或点「刷新」重新同步。`
        : `<span class="ds-empty-icon">🔁</span>还没有流水线记录。先在「连接管理」接入 GitLab / GitHub / Gitee 项目（需要可读流水线的访问令牌），即可在这里统一查看、触发与失败诊断。`;
    }
    return;
  }
  if (empty) empty.style.display = "none";
  const admin = isAdmin();
  list.innerHTML = CICD_RUNS.map((r, i) => {
    const num = r.number ? `#${r.number}` : `#${String(r.id).slice(0, 8)}`;
    const running = r.status === "running" || r.status === "pending";
    const acts = [];
    acts.push(`<button class="btn sm" type="button" data-cicd-act="detail" data-i="${i}">详情</button>`);
    if (admin) {
      if (r.status === "failed" || r.status === "canceled") acts.push(`<button class="btn sm" type="button" data-cicd-act="retry" data-i="${i}">重跑</button>`);
      if (running) acts.push(`<button class="btn sm warn" type="button" data-cicd-act="cancel" data-i="${i}">取消</button>`);
      if (r.status === "failed") acts.push(`<button class="btn sm" type="button" data-cicd-act="diagnose" data-i="${i}">🤖 诊断</button>`);
    }
    if (r.web_url) acts.push(`<a class="btn sm" href="${esc(r.web_url)}" target="_blank" rel="noopener">打开 ↗</a>`);
    return `<div class="cicd-run">
      <div class="cicd-run-main">
        <div class="cicd-run-line1">
          ${cicdStatusBadge(r.status)}
          <b class="cicd-run-proj">${esc(r.project)}</b>
          <span class="mono muted">${esc(num)}</span>
          ${r.name ? `<span class="cicd-run-name">${esc(r.name)}</span>` : ""}
        </div>
        <div class="cicd-run-line2 mono">
          <span>${esc(r.ref || "-")}</span>
          ${r.sha ? `<span title="${esc(r.sha)}">${esc(String(r.sha).slice(0, 7))}</span>` : ""}
          ${r.actor ? `<span>👤 ${esc(r.actor)}</span>` : ""}
          ${r.duration_sec ? `<span>⏱ ${esc(fmtDur(r.duration_sec))}</span>` : ""}
          ${r.created_at ? `<span>${esc(fmtDateTime(r.created_at))}</span>` : ""}
        </div>
      </div>
      <div class="cicd-run-acts">${acts.join("")}</div>
    </div>`;
  }).join("");
}

/* ---------------- 运行操作：重跑 / 取消 / 详情 / 诊断 ---------------- */
async function cicdRunAction(kind, run) {
  if (!isAdmin()) { toast("仅管理员可操作", "err"); return; }
  const tipTxt = { retry: "重跑", cancel: "取消" };
  if (kind !== "detail" && kind !== "diagnose") {
    if (!confirm(`确认${tipTxt[kind] || kind}流水线 ${run.project} #${run.number || run.id}？`)) return;
  }
  try {
    await cicdFetch(`/cicd/runs/${encodeURIComponent(run.id)}/${kind}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ connection_id: run.connection_id })
    });
    toast(`${tipTxt[kind]}请求已提交`, "ok");
    loadCICDRuns();
    loadCICDOverview();
  } catch (e) {
    toast(`${tipTxt[kind] || kind}失败：` + e.message, "err");
  }
}

async function openCICDDetail(run) {
  CICD_DETAIL_RUN = run;
  $("cicdDetailTitle").textContent = `运行详情 · ${run.project} #${run.number || run.id}`;
  $("cicdDetailMeta").innerHTML =
    `${cicdStatusBadge(run.status)} 分支 <b class="mono">${esc(run.ref || "-")}</b>` +
    (run.sha ? ` · 提交 <span class="mono">${esc(String(run.sha).slice(0, 7))}</span>` : "") +
    (run.actor ? ` · 触发人 ${esc(run.actor)}` : "") +
    (run.created_at ? ` · ${esc(fmtDateTime(run.created_at))}` : "");
  $("cicdDetailLog").style.display = "none";
  $("cicdDetailLog").textContent = "";
  if ($("cicdDetailIncident")) $("cicdDetailIncident").checked = false;
  $("cicdDetailMask").classList.add("show");
  const jobsBox = $("cicdDetailJobs");
  jobsBox.innerHTML = `<div class="hint" style="padding:8px">加载阶段明细…</div>`;
  try {
    const j = await cicdFetch(`/cicd/runs/${encodeURIComponent(run.id)}/jobs?connection_id=${encodeURIComponent(run.connection_id)}`);
    const jobs = Array.isArray(j.jobs) ? j.jobs : [];
    if (!jobs.length) { jobsBox.innerHTML = `<div class="hint" style="padding:8px">该提供方未返回阶段明细</div>`; return; }
    jobsBox.innerHTML = jobs.map((job, ji) => `
      <div class="cicd-job">
        ${cicdStatusBadge(job.status)}
        <b>${esc(job.name)}</b>
        ${job.stage ? `<span class="muted">[${esc(job.stage)}]</span>` : ""}
        ${job.duration_sec ? `<span class="mono muted">⏱ ${esc(fmtDur(job.duration_sec))}</span>` : ""}
        ${job.failure_note ? `<span class="cicd-job-note">${esc(job.failure_note)}</span>` : ""}
        <button class="btn sm" type="button" data-cicd-job="${ji}" style="margin-left:auto">日志</button>
      </div>`).join("");
    jobsBox._jobs = jobs;
  } catch (e) {
    jobsBox.innerHTML = `<div class="hint" style="padding:8px">阶段明细获取失败：${esc(e.message)}</div>`;
  }
}

async function loadCICDJobLog(job) {
  const pre = $("cicdDetailLog");
  if (!pre || !CICD_DETAIL_RUN) return;
  pre.style.display = "";
  pre.textContent = `加载日志中…（${job.name}）`;
  try {
    const j = await cicdFetch(`/cicd/jobs/${encodeURIComponent(job.id)}/log?connection_id=${encodeURIComponent(CICD_DETAIL_RUN.connection_id)}`);
    pre.textContent = j.log || "（空日志）";
    pre.scrollTop = pre.scrollHeight;
  } catch (e) {
    pre.textContent = "日志获取失败：" + e.message;
  }
}

async function diagnoseCICDRun(run, openIncident) {
  if (!isAdmin()) { toast("仅管理员可操作", "err"); return; }
  const pre = $("cicdDetailLog");
  if (pre) { pre.style.display = ""; pre.textContent = "收集失败证据中（阶段明细 + 失败任务日志尾部）…"; }
  try {
    const j = await cicdFetch(`/cicd/runs/${encodeURIComponent(run.id)}/diagnose`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ connection_id: run.connection_id, open_incident: !!openIncident, status: run.status })
    });
    if (pre) { pre.textContent = j.evidence || "（无证据）"; pre.scrollTop = 0; }
    if (openIncident && j.incident_id) toast(`已登记 SRE 事件 #${j.incident_id}`, "ok");
    else toast("失败证据已收集并写入 AI 记忆", "ok");
  } catch (e) {
    if (pre) pre.textContent = "诊断失败：" + e.message;
    toast("诊断失败：" + e.message, "err");
  }
}

/* ---------------- 触发流水线 ---------------- */
function openCICDTrigger() {
  if (!isAdmin()) { toast("仅管理员可操作", "err"); return; }
  if (!CICD_CONNS.some(c => c.enabled)) { toast("请先在「连接管理」添加并启用连接", "err"); return; }
  $("cicdTriggerRef").value = "";
  $("cicdTriggerWorkflow").value = "";
  $("cicdTriggerMask").classList.add("show");
}

async function submitCICDTrigger() {
  const connId = $("cicdTriggerConn").value;
  if (!connId) { toast("请选择连接", "err"); return; }
  try {
    await withLoading("cicdTriggerGoBtn", () => cicdFetch("/cicd/trigger", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        connection_id: connId,
        ref: ($("cicdTriggerRef").value || "").trim(),
        workflow: ($("cicdTriggerWorkflow").value || "").trim(),
      })
    }));
    toast("流水线已触发", "ok");
    $("cicdTriggerMask").classList.remove("show");
    loadCICDRuns();
    loadCICDOverview();
  } catch (e) {
    toast("触发失败：" + e.message, "err");
  }
}

/* ---------------- 连接管理 ---------------- */
function renderCICDConnList() {
  const box = $("cicdConnList");
  if (!box) return;
  if (!CICD_CONNS.length) {
    box.innerHTML = `<div class="hint" style="padding:10px">还没有连接。点「+ 添加连接」接入 GitLab / GitHub / Gitee 项目。</div>`;
    return;
  }
  const admin = isAdmin();
  box.innerHTML = CICD_CONNS.map(c => {
    const sync = c.last_error ? `<span class="badge warn" title="${esc(c.last_error)}">同步异常</span>` : "";
    return `<div class="cicd-conn">
      <div class="cicd-conn-main">
        <div class="cicd-run-line1">
          <span class="badge info">${esc(c.provider)}</span>
          <b>${esc(c.name)}</b>
          <span class="mono muted">${esc(c.project)}</span>
          ${c.base_url ? `<span class="mono muted" title="${esc(c.base_url)}">🏠 自托管</span>` : ""}
          ${c.enabled ? "" : `<span class="badge">已停用</span>`}
          ${sync}
        </div>
        <div class="cicd-run-line2 mono">
          ${c.ref ? `<span>分支 ${esc(c.ref)}</span>` : ""}
          <span>Token ${esc(c.token || "未设置")}</span>
          ${c.watch_failures ? `<span>失败告警 ✓</span>` : ""}
          ${c.auto_incident ? `<span>自动事件 ✓</span>` : ""}
        </div>
      </div>
      ${admin ? `<div class="cicd-run-acts">
        <button class="btn sm" type="button" data-cicd-conn-act="edit" data-id="${esc(c.id)}">编辑</button>
        <button class="btn sm warn" type="button" data-cicd-conn-act="delete" data-id="${esc(c.id)}">删除</button>
      </div>` : ""}
    </div>`;
  }).join("");
}

function collectCICDConn() {
  return {
    id: $("cicdConnId").value || undefined,
    name: ($("cicdConnName").value || "").trim(),
    provider: $("cicdConnProvider").value,
    project: ($("cicdConnProject").value || "").trim(),
    base_url: ($("cicdConnBaseURL").value || "").trim(),
    token: $("cicdConnToken").value || "",
    ref: ($("cicdConnRef").value || "").trim(),
    pipeline_path: ($("cicdConnPipelinePath").value || "").trim(),
    ca_cert: ($("cicdConnCACert").value || "").trim(),
    insecure_skip_tls: $("cicdConnInsecure").checked,
    watch_failures: $("cicdConnWatch").checked,
    auto_incident: $("cicdConnAutoIncident").checked,
    enabled: $("cicdConnEnabled").checked,
  };
}

function openCICDConnModal(conn) {
  if (!isAdmin()) { toast("仅管理员可操作", "err"); return; }
  $("cicdConnModalTitle").textContent = conn ? "编辑 CI/CD 连接" : "添加 CI/CD 连接";
  $("cicdConnId").value = conn ? conn.id : "";
  $("cicdConnName").value = conn ? conn.name : "";
  $("cicdConnProvider").value = conn ? conn.provider : "gitlab";
  $("cicdConnProject").value = conn ? conn.project : "";
  $("cicdConnBaseURL").value = conn ? (conn.base_url || "") : "";
  $("cicdConnToken").value = conn ? (conn.token || "") : "";
  $("cicdConnRef").value = conn ? (conn.ref || "") : "";
  $("cicdConnPipelinePath").value = conn ? (conn.pipeline_path || "") : "";
  $("cicdConnCACert").value = conn ? (conn.ca_cert || "") : "";
  $("cicdConnInsecure").checked = conn ? !!conn.insecure_skip_tls : false;
  $("cicdConnWatch").checked = conn ? !!conn.watch_failures : true;
  $("cicdConnAutoIncident").checked = conn ? !!conn.auto_incident : false;
  $("cicdConnEnabled").checked = conn ? !!conn.enabled : true;
  $("cicdConnTestResult").textContent = "";
  $("cicdConnMask").classList.add("show");
}

async function testCICDConn() {
  const out = $("cicdConnTestResult");
  try {
    const j = await withLoading("cicdConnTestBtn", () => cicdFetch("/cicd/connections/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(collectCICDConn())
    }));
    out.textContent = j.ok ? `✅ ${j.message || "连接成功"}` : `❌ ${j.error || "连接失败"}`;
    out.style.color = j.ok ? "var(--ok-txt)" : "var(--crit-txt)";
  } catch (e) {
    out.textContent = "❌ " + e.message;
    out.style.color = "var(--crit-txt)";
  }
}

async function saveCICDConn() {
  const body = collectCICDConn();
  const isEdit = !!body.id;
  try {
    await withLoading("cicdConnSaveBtn", async () => {
      if (isEdit) {
        await cicdFetch(`/cicd/connections/${encodeURIComponent(body.id)}`, {
          method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body)
        });
      } else {
        await cicdFetch("/cicd/connections", {
          method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body)
        });
      }
    });
    toast(isEdit ? "连接已更新" : "连接已添加", "ok");
    $("cicdConnMask").classList.remove("show");
    loadCICD();
  } catch (e) {
    toast("保存失败：" + e.message, "err");
  }
}

async function deleteCICDConn(id) {
  if (!isAdmin()) { toast("仅管理员可操作", "err"); return; }
  const c = CICD_CONNS.find(x => x.id === id);
  if (!confirm(`确认删除连接「${c ? c.name : id}」？删除后其流水线不再同步。`)) return;
  try {
    await cicdFetch(`/cicd/connections/${encodeURIComponent(id)}`, { method: "DELETE" });
    toast("连接已删除", "ok");
    loadCICD();
  } catch (e) {
    toast("删除失败：" + e.message, "err");
  }
}

/* ---------------- 事件绑定（模块顶层只绑一次，视图渲染走 _pageRenderers） ---------------- */
(function initCICD() {
  if (!document.getElementById("view-cicd")) return;

  const refresh = () => { loadCICDOverview(); loadCICDRuns(); };
  const safe = (id, ev, fn) => { const el = document.getElementById(id); if (el) el.addEventListener(ev, fn); };
  safe("cicdRefreshBtn", "click", refresh);
  safe("cicdConnFilter", "change", loadCICDRuns);
  safe("cicdStatusFilter", "change", loadCICDRuns);
  safe("cicdTriggerBtn", "click", openCICDTrigger);
  safe("cicdTriggerGoBtn", "click", submitCICDTrigger);
  safe("cicdManageConnsBtn", "click", () => {
    const p = $("cicdConnPanel");
    if (!p) return;
    const hidden = p.style.display === "none";
    p.style.display = hidden ? "" : "none";
    if (hidden) renderCICDConnList();
  });
  safe("cicdAddConnBtn", "click", () => openCICDConnModal(null));
  safe("cicdConnTestBtn", "click", testCICDConn);
  safe("cicdConnSaveBtn", "click", saveCICDConn);
  safe("cicdDetailDiagnoseBtn", "click", () => {
    if (CICD_DETAIL_RUN) diagnoseCICDRun(CICD_DETAIL_RUN, !!($("cicdDetailIncident") && $("cicdDetailIncident").checked));
  });

  // 流水线行内操作（事件委托）
  document.getElementById("cicdRunsList").addEventListener("click", e => {
    const btn = e.target.closest("[data-cicd-act]");
    if (!btn) return;
    const run = CICD_RUNS[Number(btn.dataset.i)];
    if (!run) return;
    const act = btn.dataset.cicdAct;
    if (act === "detail") openCICDDetail(run);
    else if (act === "diagnose") diagnoseCICDRun(run, false);
    else cicdRunAction(act, run);
  });

  // 详情弹窗：阶段日志
  document.getElementById("cicdDetailJobs").addEventListener("click", e => {
    const btn = e.target.closest("[data-cicd-job]");
    if (!btn) return;
    const jobsBox = $("cicdDetailJobs");
    const job = jobsBox._jobs && jobsBox._jobs[Number(btn.dataset.cicdJob)];
    if (job) loadCICDJobLog(job);
  });

  // 连接行操作（事件委托）
  document.getElementById("cicdConnList").addEventListener("click", e => {
    const btn = e.target.closest("[data-cicd-conn-act]");
    if (!btn) return;
    const c = CICD_CONNS.find(x => x.id === btn.dataset.id);
    if (!c) return;
    if (btn.dataset.cicdConnAct === "edit") openCICDConnModal(c);
    else deleteCICDConn(c.id);
  });
})();

window._pageRenderers = window._pageRenderers || {};
window._pageRenderers.cicd = loadCICD;
