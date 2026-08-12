/* k8s.js — 服务端直连 Kubernetes 资源视图（商用台账） */

let K8S_CLUSTERS = [];
let K8S_TAB = "overview";
let K8S_SCALE_CTX = null;
const K8S_FILTER = { q: "", phase: "all" }; // phase: all|running|pending|failed|other
const K8S_PAGE_LIMIT = 50;
let K8S_CACHE = {
  nodes: [],
  pods: [],
  podsContinue: "",
  podsPageContinue: "",
  podsContinueStack: [],
  podsTruncated: false,
  deployments: [],
  deploymentsContinue: "",
  deploymentsPageContinue: "",
  deploymentsContinueStack: [],
  deploymentsTruncated: false,
  events: [],
};
let K8S_RENDER_SEQ = 0;
let K8S_ABORT = null;
let K8S_EDIT_ID = ""; // currently editing cluster id ("" = create)

const k8sT = (k, fb) => I18N.t(k, fb);

function k8sClusterId() {
  return ($("k8sClusterSel")?.value || "").trim();
}

function k8sNamespace() {
  return ($("k8sNsSel")?.value || "").trim();
}

function k8sSetStatus(text, kind) {
  const el = $("k8sStatusChip");
  if (!el) return;
  el.textContent = text || "";
  el.classList.remove("ok", "warn", "crit");
  if (kind) el.classList.add(kind === "err" ? "crit" : kind);
}

function k8sPhaseKey(phase) {
  const p = String(phase || "").toLowerCase();
  if (p === "running" || p === "succeeded") return "running";
  if (p === "pending" || p === "containercreating") return "pending";
  if (p === "failed" || p === "error" || p === "crashloopbackoff" || p === "unknown") return "failed";
  return "other";
}
function k8sPhaseBadge(phase) {
  const key = k8sPhaseKey(phase);
  const cls = { running: "ok", pending: "warn", failed: "crit", other: "info" }[key] || "info";
  const lab = {
    running: k8sT("k8s.phase_running", "Running"),
    pending: k8sT("k8s.phase_pending", "Pending"),
    failed: k8sT("k8s.phase_failed", "Failed"),
    other: phase || "—",
  }[key];
  return `<span class="badge ${cls}" title="${esc(phase || "")}">${esc(key === "other" ? (phase || "—") : lab)}</span>`;
}
function k8sReadyBadge(ready) {
  const ok = String(ready).toLowerCase() === "true" || ready === true || String(ready).toLowerCase() === "ready";
  const text = ok ? k8sT("k8s.ready_yes", "Ready") : k8sT("k8s.ready_no", "NotReady");
  return `<span class="badge ${ok ? "ok" : "crit"}">${esc(text)}</span>`;
}
function k8sEventTypeBadge(type) {
  const t = String(type || "").toLowerCase();
  if (t === "warning") return `<span class="badge warn">${esc(type)}</span>`;
  if (t === "normal") return `<span class="badge ok">${esc(type)}</span>`;
  return `<span class="badge">${esc(type || "—")}</span>`;
}

function k8sMatch(hay, q) {
  if (!q) return true;
  return typeof matchesSearchTokens === "function"
    ? matchesSearchTokens(hay, q)
    : String(hay).toLowerCase().includes(String(q).toLowerCase());
}

function k8sResetPager(kind) {
  K8S_CACHE[`${kind}Continue`] = "";
  K8S_CACHE[`${kind}PageContinue`] = "";
  K8S_CACHE[`${kind}ContinueStack`] = [];
  K8S_CACHE[`${kind}Truncated`] = false;
}

function k8sPagedResourcePath(id, resource, cont) {
  const params = new URLSearchParams({ limit: String(K8S_PAGE_LIMIT) });
  const ns = k8sNamespace();
  if (ns) params.set("namespace", ns);
  if (cont) params.set("continue", cont);
  return `/k8s/clusters/${encodeURIComponent(id)}/${resource}?${params}`;
}

function k8sPagerHTML(kind) {
  const stack = K8S_CACHE[`${kind}ContinueStack`] || [];
  const hasPrev = stack.length > 0;
  const hasNext = !!K8S_CACHE[`${kind}Continue`];
  const truncated = !!K8S_CACHE[`${kind}Truncated`];
  if (!hasPrev && !hasNext && !truncated) return "";
  const hint = hasNext || truncated
    ? k8sT("k8s.page_more_hint", "当前页最多 50 项，还有更多资源可继续翻页；搜索 / Phase 仅过滤当前页。")
    : k8sT("k8s.page_local_hint", "搜索 / Phase 仅过滤当前页。");
  return `<div class="rtx-toolbar k8s-toolbar" style="justify-content:flex-end;margin-top:10px">
    <span class="muted">${esc(hint)}</span>
    <button type="button" class="btn sm" data-k8s-page="${esc(kind)}:prev"${hasPrev ? "" : " disabled"}>${esc(k8sT("common.prev", "上一页"))}</button>
    <button type="button" class="btn sm" data-k8s-page="${esc(kind)}:next"${hasNext ? "" : " disabled"}>${esc(k8sT("common.next", "下一页"))}</button>
  </div>`;
}

function k8sWirePager(kind) {
  const panel = $("k8sPanel");
  if (!panel) return;
  panel.querySelectorAll(`[data-k8s-page^="${kind}:"]`).forEach(b => {
    b.addEventListener("click", () => {
      const dir = (b.getAttribute("data-k8s-page") || "").split(":")[1];
      k8sGoResourcePage(kind, dir);
    });
  });
}

async function k8sGoResourcePage(kind, dir) {
  const stackKey = `${kind}ContinueStack`;
  const contKey = `${kind}Continue`;
  const pageKey = `${kind}PageContinue`;
  if (!Array.isArray(K8S_CACHE[stackKey])) K8S_CACHE[stackKey] = [];
  if (dir === "next") {
    const cont = K8S_CACHE[contKey] || "";
    if (!cont) return;
    K8S_CACHE[stackKey].push(K8S_CACHE[pageKey] || "");
    await renderK8sPanel({ kind, continueToken: cont });
    return;
  }
  if (dir === "prev") {
    if (!K8S_CACHE[stackKey].length) return;
    const cont = K8S_CACHE[stackKey].pop() || "";
    await renderK8sPanel({ kind, continueToken: cont });
  }
}

function k8sToolbarHTML(opts) {
  opts = opts || {};
  const showPhase = !!opts.phase;
  let html = `<div class="rtx-toolbar k8s-toolbar">
    <input type="search" id="k8sSearch" class="hw-search" placeholder="${esc(k8sT("k8s.search_ph", "搜索名称 / 命名空间 / 节点 / IP…"))}" value="${esc(K8S_FILTER.q)}" autocomplete="off">`;
  if (showPhase) {
    html += `<div class="select-wrap"><select id="k8sPhaseFilter">
      <option value="all"${K8S_FILTER.phase === "all" ? " selected" : ""}>${esc(k8sT("k8s.filter_all_phase", "全部 Phase"))}</option>
      <option value="running"${K8S_FILTER.phase === "running" ? " selected" : ""}>${esc(k8sT("k8s.phase_running", "Running"))}</option>
      <option value="pending"${K8S_FILTER.phase === "pending" ? " selected" : ""}>${esc(k8sT("k8s.phase_pending", "Pending"))}</option>
      <option value="failed"${K8S_FILTER.phase === "failed" ? " selected" : ""}>${esc(k8sT("k8s.phase_failed", "Failed"))}</option>
      <option value="other"${K8S_FILTER.phase === "other" ? " selected" : ""}>${esc(k8sT("k8s.phase_other", "其他"))}</option>
    </select></div>`;
  }
  if (opts.count != null) html += `<span class="rtx-count tag">${opts.count}</span>`;
  html += `</div>`;
  return html;
}

function k8sWireToolbar(refilter) {
  const search = $("k8sSearch");
  if (search) {
    search.addEventListener("input", () => {
      K8S_FILTER.q = search.value || "";
      refilter();
      const el = $("k8sSearch");
      if (el) { el.focus(); try { el.setSelectionRange(el.value.length, el.value.length); } catch (_) {} }
    });
  }
  const phase = $("k8sPhaseFilter");
  if (phase) {
    phase.addEventListener("change", () => {
      K8S_FILTER.phase = phase.value || "all";
      refilter();
    });
  }
}

function k8sBeginFetch() {
  if (K8S_ABORT) {
    try { K8S_ABORT.abort(); } catch (_) {}
  }
  K8S_ABORT = typeof AbortController !== "undefined" ? new AbortController() : null;
  return ++K8S_RENDER_SEQ;
}

function k8sIsStale(seq) {
  return seq !== K8S_RENDER_SEQ;
}

function k8sIsAbort(err) {
  return err && (err.name === "AbortError" || /abort/i.test(String(err.message || err)));
}

async function k8sFetch(path, opts) {
  opts = Object.assign({}, opts || {});
  const noAbort = !!opts.noAbort;
  delete opts.noAbort;
  const init = Object.assign({ credentials: "same-origin" }, opts);
  if (!noAbort && !init.signal && K8S_ABORT) init.signal = K8S_ABORT.signal;
  const r = await fetch(`${API}${path}`, init);
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || (`HTTP ${r.status}`));
  return j;
}

function k8sUnreachableHTML(errMsg) {
  const tips = [
    k8sT("k8s.tip_route", "确认本平台所在主机能访问该 API（同一网段 / VPN / 防火墙放行 6443）"),
    k8sT("k8s.tip_addr", "核对 API Server 地址与端口（例如 https://x.x.x.x:6443）"),
    k8sT("k8s.tip_tls", "TLS 失败时粘贴正确 CA，或仅在可信内网勾选跳过校验"),
    k8sT("k8s.tip_token", "Token / kubeconfig 权限需能访问 /version 与命名空间列表"),
  ];
  return `<div class="empty-state k8s-empty k8s-unreachable">
    <div class="sec-empty-ico" aria-hidden="true"></div>
    <h4>${esc(k8sT("k8s.status_err", "连接失败"))}</h4>
    <p class="k8s-err-msg">${esc(errMsg || k8sT("k8s.unreachable_generic", "无法连接 Kubernetes API"))}</p>
    <ul class="k8s-tip-list">${tips.map(t => `<li>${esc(t)}</li>`).join("")}</ul>
    <div class="k8s-unreachable-actions">
      <button type="button" class="btn sm" data-k8s-goto="config">${esc(k8sT("k8s.tab_config", "集群配置"))}</button>
      <button type="button" class="btn sm primary" data-k8s-goto="retry">${esc(k8sT("common.refresh", "刷新"))}</button>
    </div>
  </div>`;
}

function k8sWireUnreachableActions(panel) {
  if (!panel) return;
  panel.querySelectorAll("[data-k8s-goto]").forEach(b => {
    b.addEventListener("click", () => {
      const act = b.getAttribute("data-k8s-goto");
      if (act === "config") switchK8sTab("config");
      else loadK8sPage();
    });
  });
}

async function loadK8sClusters() {
  const j = await k8sFetch("/k8s/clusters", { noAbort: true });
  K8S_CLUSTERS = j.clusters || [];
  const sel = $("k8sClusterSel");
  if (!sel) return;
  const prev = sel.value;
  if (!K8S_CLUSTERS.length) {
    sel.innerHTML = `<option value="">${esc(k8sT("k8s.no_cluster", "尚未配置集群"))}</option>`;
    return;
  }
  sel.innerHTML = K8S_CLUSTERS.map(c =>
    `<option value="${esc(c.id)}" ${c.enabled ? "" : "disabled"}>${esc(c.name)}${c.enabled ? "" : " (disabled)"}</option>`
  ).join("");
  if (prev && K8S_CLUSTERS.some(c => c.id === prev)) sel.value = prev;
}

async function loadK8sNamespaces() {
  const id = k8sClusterId();
  const sel = $("k8sNsSel");
  if (!sel) return;
  const keep = sel.value;
  sel.innerHTML = `<option value="">${esc(k8sT("k8s.all_ns", "全部命名空间"))}</option>`;
  if (!id) return;
  try {
    const j = await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/namespaces`, { noAbort: true });
    (j.items || []).forEach(ns => {
      const o = document.createElement("option");
      o.value = ns.name || "";
      o.textContent = ns.name || "";
      sel.appendChild(o);
    });
    if (keep) sel.value = keep;
  } catch (_) { /* ignore */ }
}

function switchK8sTab(tab) {
  K8S_TAB = tab || "overview";
  K8S_FILTER.q = "";
  K8S_FILTER.phase = "all";
  document.querySelectorAll("#k8sInnerTabs .tab").forEach(b => b.classList.toggle("active", b.dataset.k8sTab === K8S_TAB));
  renderK8sPanel();
}

function k8sTable(headers, rows, htmlCells) {
  if (!rows.length) {
    return `<div class="empty-state k8s-empty"><h4>${esc(k8sT("k8s.empty", "暂无数据"))}</h4>
      <p>${esc(k8sT("k8s.empty_hint", "尝试切换命名空间，或清空搜索条件。"))}</p></div>`;
  }
  const th = headers.map(h => `<th>${esc(h)}</th>`).join("");
  const tr = rows.map(r => `<tr>${r.map((c, i) => {
    const last = i === r.length - 1 && htmlCells;
    return `<td>${last ? c : esc(String(c ?? ""))}</td>`;
  }).join("")}</tr>`).join("");
  return `<div class="nf-table-wrap k8s-table-wrap"><table class="data-table k8s-table"><thead><tr>${th}</tr></thead><tbody>${tr}</tbody></table></div>`;
}

function paintK8sNodes() {
  const panel = $("k8sPanel");
  if (!panel) return;
  const q = K8S_FILTER.q;
  const items = (K8S_CACHE.nodes || []).filter(n => {
    const hay = [n.name, n.ready, n.internal_ip, n.linked_host_name, n.linked_host_id].join(" ");
    return k8sMatch(hay, q);
  });
  let body;
  if (!items.length) {
    body = `<div class="empty-state k8s-empty"><h4>${esc(k8sT("k8s.empty", "暂无数据"))}</h4><p>${esc(k8sT("k8s.empty_hint", "尝试切换命名空间，或清空搜索条件。"))}</p></div>`;
  } else {
    const th = [k8sT("k8s.col_name", "名称"), k8sT("k8s.col_ready", "状态"), k8sT("k8s.col_ip", "IP"), k8sT("k8s.col_linked_host", "关联主机")]
      .map(h => `<th>${esc(h)}</th>`).join("");
    const tr = items.map(n => `<tr>
      <td class="mono">${esc(n.name)}</td>
      <td>${k8sReadyBadge(n.ready)}</td>
      <td class="mono">${esc(n.internal_ip || "—")}</td>
      <td>${esc(n.linked_host_name || n.linked_host_id || "—")}</td>
    </tr>`).join("");
    body = `<div class="nf-table-wrap k8s-table-wrap"><table class="data-table k8s-table"><thead><tr>${th}</tr></thead><tbody>${tr}</tbody></table></div>`;
  }
  panel.innerHTML = k8sToolbarHTML({ count: `${items.length}/${K8S_CACHE.nodes.length}` }) + body;
  k8sWireToolbar(paintK8sNodes);
}

function paintK8sPods() {
  const panel = $("k8sPanel");
  if (!panel) return;
  const q = K8S_FILTER.q;
  const items = (K8S_CACHE.pods || []).filter(p => {
    if (K8S_FILTER.phase !== "all" && k8sPhaseKey(p.phase) !== K8S_FILTER.phase) return false;
    const hay = [p.namespace, p.name, p.phase, p.node, p.linked_host_name, p.linked_host_id, p.ip].join(" ");
    return k8sMatch(hay, q);
  });
  const th = [k8sT("k8s.col_ns", "命名空间"), k8sT("k8s.col_name", "名称"), k8sT("k8s.col_phase", "Phase"),
    k8sT("k8s.col_node", "节点"), k8sT("k8s.col_linked_host", "关联主机"), k8sT("k8s.col_ip", "IP"), k8sT("k8s.col_actions", "操作")]
    .map(h => `<th>${esc(h)}</th>`).join("");
  let body;
  if (!items.length) {
    body = `<div class="empty-state k8s-empty"><h4>${esc(k8sT("k8s.empty", "暂无数据"))}</h4><p>${esc(k8sT("k8s.empty_hint", "尝试切换命名空间，或清空搜索条件。"))}</p></div>`;
  } else {
    const allowMutate = (typeof canWrite === "function" && canWrite()) || (typeof isAdmin === "function" && isAdmin());
    const tr = items.map(p => {
      const acts = [`<button type="button" class="btn sm" data-k8s-log="${esc(p.namespace)}|${esc(p.name)}">${esc(k8sT("k8s.view_log", "日志"))}</button>`];
      if (allowMutate) {
        acts.push(`<button type="button" class="btn sm" data-k8s-exec="${esc(p.namespace)}|${esc(p.name)}">${esc(k8sT("k8s.act_exec", "Exec"))}</button>`);
        acts.push(`<button type="button" class="btn sm danger" data-k8s-delpod="${esc(p.namespace)}|${esc(p.name)}">${esc(k8sT("k8s.act_del_pod", "删除"))}</button>`);
      }
      return `<tr>
        <td class="mono">${esc(p.namespace)}</td>
        <td class="mono"><strong>${esc(p.name)}</strong></td>
        <td>${k8sPhaseBadge(p.phase)}</td>
        <td class="mono">${esc(p.node || "—")}</td>
        <td>${esc(p.linked_host_name || p.linked_host_id || "—")}</td>
        <td class="mono">${esc(p.ip || "—")}</td>
        <td><div class="k8s-actions">${acts.join("")}</div></td>
      </tr>`;
    }).join("");
    body = `<div class="nf-table-wrap k8s-table-wrap"><table class="data-table k8s-table"><thead><tr>${th}</tr></thead><tbody>${tr}</tbody></table></div>`;
  }
  const id = k8sClusterId();
  panel.innerHTML = k8sToolbarHTML({ phase: true, count: `${items.length}/${K8S_CACHE.pods.length}` }) +
    `<div style="margin:0 0 10px"><button type="button" class="btn sm ai-assist-btn" id="k8sPodsAI"><span class="ai-assist-btn-ic">🤖</span>${esc(k8sT("ai.analyze", "AI 分析"))}</button></div>` +
    body + k8sPagerHTML("pods");
  k8sWireToolbar(paintK8sPods);
  k8sWirePager("pods");
  panel.querySelectorAll("[data-k8s-log]").forEach(b => {
    b.addEventListener("click", () => {
      const [ns, name] = (b.getAttribute("data-k8s-log") || "").split("|");
      openK8sPodLog(ns, name);
    });
  });
  panel.querySelectorAll("[data-k8s-delpod]").forEach(b => {
    b.addEventListener("click", async () => {
      const [ns, name] = (b.getAttribute("data-k8s-delpod") || "").split("|");
      if (!confirm(k8sT("k8s.del_pod_confirm", "确认删除 Pod「{name}」？由控制器管理的 Pod 会被重建。").replace("{name}", name))) return;
      try {
        await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/pods/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`, { method: "DELETE" });
        toast(k8sT("k8s.del_pod_ok", "已删除 Pod"), "ok");
        renderK8sPanel();
      } catch (e) { toast(String(e.message || e), "err"); }
    });
  });
  panel.querySelectorAll("[data-k8s-exec]").forEach(b => {
    b.addEventListener("click", async () => {
      const [ns, name] = (b.getAttribute("data-k8s-exec") || "").split("|");
      const cmd = prompt(k8sT("k8s.exec_prompt", "在 Pod 内执行命令（需服务端 kubectl）"), "ps aux | head -n 20");
      if (cmd === null || !String(cmd).trim()) return;
      try {
        const j = await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/pods/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/exec`, {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ command: String(cmd).trim() }),
        });
        alert(j.output || "(empty)");
      } catch (e) { toast(String(e.message || e), "err"); }
    });
  });
  const ai = $("k8sPodsAI");
  if (ai) ai.addEventListener("click", () => k8sOpenOpsAI("pods"));
}

function paintK8sDeployments() {
  const panel = $("k8sPanel");
  if (!panel) return;
  const id = k8sClusterId();
  const q = K8S_FILTER.q;
  const allowMutate = (typeof canWrite === "function" && canWrite()) || (typeof isAdmin === "function" && isAdmin());
  const items = (K8S_CACHE.deployments || []).filter(d => {
    const hay = [d.namespace, d.name, String(d.replicas), String(d.ready), String(d.available)].join(" ");
    return k8sMatch(hay, q);
  });
  const th = [k8sT("k8s.col_ns", "命名空间"), k8sT("k8s.col_name", "名称"), k8sT("k8s.col_replicas", "Ready/Desired"),
    k8sT("k8s.col_available", "Available"), k8sT("k8s.col_actions", "操作")].map(h => `<th>${esc(h)}</th>`).join("");
  let body;
  if (!items.length) {
    body = `<div class="empty-state k8s-empty"><h4>${esc(k8sT("k8s.empty", "暂无数据"))}</h4><p>${esc(k8sT("k8s.empty_hint", "尝试切换命名空间，或清空搜索条件。"))}</p></div>`;
  } else {
    const tr = items.map(d => {
      const ready = d.ready || 0, desired = d.replicas || 0;
      const ok = desired > 0 && ready >= desired;
      const ratio = `<span class="badge ${ok ? "ok" : (ready === 0 ? "crit" : "warn")}">${esc(ready)}/${esc(desired)}</span>`;
      const acts = allowMutate
        ? `<div class="k8s-actions">
            <button type="button" class="btn sm" data-k8s-scale="${esc(d.namespace)}|${esc(d.name)}|${esc(String(desired))}">${esc(k8sT("k8s.act_scale", "扩缩容"))}</button>
            <button type="button" class="btn sm" data-k8s-restart="${esc(d.namespace)}|${esc(d.name)}">${esc(k8sT("k8s.act_restart", "重启"))}</button>
            <button type="button" class="btn sm" data-k8s-undo="${esc(d.namespace)}|${esc(d.name)}">${esc(k8sT("k8s.act_undo", "回滚"))}</button>
          </div>`
        : "—";
      return `<tr>
        <td class="mono">${esc(d.namespace)}</td>
        <td class="mono"><strong>${esc(d.name)}</strong></td>
        <td>${ratio}</td>
        <td>${esc(d.available || 0)}</td>
        <td>${acts}</td>
      </tr>`;
    }).join("");
    body = `<div class="nf-table-wrap k8s-table-wrap"><table class="data-table k8s-table"><thead><tr>${th}</tr></thead><tbody>${tr}</tbody></table></div>`;
  }
  panel.innerHTML = k8sToolbarHTML({ count: `${items.length}/${K8S_CACHE.deployments.length}` }) +
    body + k8sPagerHTML("deployments");
  k8sWireToolbar(paintK8sDeployments);
  k8sWirePager("deployments");
  panel.querySelectorAll("[data-k8s-scale]").forEach(b => {
    b.addEventListener("click", () => {
      const [ns, name, reps] = (b.getAttribute("data-k8s-scale") || "").split("|");
      openK8sScale(ns, name, parseInt(reps, 10) || 0);
    });
  });
  panel.querySelectorAll("[data-k8s-restart]").forEach(b => {
    b.addEventListener("click", async () => {
      const [ns, name] = (b.getAttribute("data-k8s-restart") || "").split("|");
      if (!confirm(k8sT("k8s.restart_confirm", "确认对 Deployment 执行 rollout restart？").replace("{name}", name))) return;
      try {
        await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/deployments/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/restart`, {
          method: "POST", headers: { "Content-Type": "application/json" }, body: "{}",
        });
        toast(k8sT("k8s.restart_ok", "已触发 Restart"), "ok");
        renderK8sPanel();
      } catch (e) { toast(String(e.message || e), "err"); }
    });
  });
  panel.querySelectorAll("[data-k8s-undo]").forEach(b => {
    b.addEventListener("click", async () => {
      const [ns, name] = (b.getAttribute("data-k8s-undo") || "").split("|");
      if (!confirm(k8sT("k8s.undo_confirm", "确认对 Deployment「{name}」执行 rollout undo？").replace("{name}", name))) return;
      try {
        await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/deployments/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/undo`, {
          method: "POST", headers: { "Content-Type": "application/json" }, body: "{}",
        });
        toast(k8sT("k8s.undo_ok", "已触发回滚"), "ok");
        renderK8sPanel();
      } catch (e) { toast(String(e.message || e), "err"); }
    });
  });
}

function k8sOpenOpsAI(kind) {
  if (typeof openAIAssist !== "function") { toast(k8sT("k8s.ai_unavailable", "AI 面板未就绪"), "err"); return; }
  const ctx = {
    cluster_id: k8sClusterId(),
    tab: kind || K8S_TAB,
    pods: (K8S_CACHE.pods || []).slice(0, 40),
    deployments: (K8S_CACHE.deployments || []).slice(0, 40),
    events: (K8S_CACHE.events || []).slice(0, 30),
  };
  openAIAssist({
    task: "k8s_ops_plan",
    title: k8sT("k8s.ai_title", "K8s 运维建议"),
    mode: "generate",
    context: JSON.stringify(ctx, null, 2),
    placeholder: k8sT("k8s.ai_ph", "例如：某 Deployment 副本不足，给出可执行动作 JSON"),
    applyLabel: k8sT("ai.apply_actions", "应用建议动作"),
    applyTo: async (text) => {
      if (typeof window.applyOpsActionPlan === "function") {
        return window.applyOpsActionPlan(text, { source: "k8s", refresh: () => renderK8sPanel() });
      }
      return false;
    },
  });
}

function paintK8sEvents() {
  const panel = $("k8sPanel");
  if (!panel) return;
  const q = K8S_FILTER.q;
  const items = (K8S_CACHE.events || []).filter(e => {
    const hay = [e.namespace, e.type, e.reason, e.object, e.message].join(" ");
    return k8sMatch(hay, q);
  });
  const th = [k8sT("k8s.col_ns", "命名空间"), k8sT("k8s.col_type", "类型"), k8sT("k8s.col_reason", "原因"),
    k8sT("k8s.col_object", "对象"), k8sT("k8s.col_message", "消息"), k8sT("k8s.col_count", "次数")]
    .map(h => `<th>${esc(h)}</th>`).join("");
  let body;
  if (!items.length) {
    body = `<div class="empty-state k8s-empty"><h4>${esc(k8sT("k8s.empty", "暂无数据"))}</h4><p>${esc(k8sT("k8s.empty_hint", "尝试切换命名空间，或清空搜索条件。"))}</p></div>`;
  } else {
    const tr = items.map(e => `<tr>
      <td class="mono">${esc(e.namespace || "—")}</td>
      <td>${k8sEventTypeBadge(e.type)}</td>
      <td>${esc(e.reason || "—")}</td>
      <td class="mono">${esc(e.object || "—")}</td>
      <td class="k8s-msg">${esc(e.message || "")}</td>
      <td>${esc(e.count || 0)}</td>
    </tr>`).join("");
    body = `<div class="nf-table-wrap k8s-table-wrap"><table class="data-table k8s-table"><thead><tr>${th}</tr></thead><tbody>${tr}</tbody></table></div>`;
  }
  panel.innerHTML = k8sToolbarHTML({ count: `${items.length}/${K8S_CACHE.events.length}` }) + body;
  k8sWireToolbar(paintK8sEvents);
}

async function renderK8sPanel(pageReq) {
  const panel = $("k8sPanel");
  if (!panel) return;
  const seq = k8sBeginFetch();
  const id = k8sClusterId();
  if (!id) {
    panel.innerHTML = `<div class="empty-state k8s-empty"><h4>${esc(k8sT("k8s.no_cluster", "尚未配置集群"))}</h4>
      <p>${esc(k8sT("k8s.hint_add", "请管理员在「集群配置」中添加 Kubernetes 集群（API Server + Token 或 kubeconfig）。"))}</p></div>`;
    k8sSetStatus(k8sT("k8s.status_none", "未选择集群"), "warn");
    if (K8S_TAB === "config" && typeof isAdmin === "function" && isAdmin()) {
      panel.innerHTML = renderK8sConfigForm();
      wireK8sConfigForm();
    }
    return;
  }
  if (K8S_TAB === "config") {
    k8sSetStatus(k8sT("k8s.status_cfg", "配置模式"), "warn");
    panel.innerHTML = renderK8sConfigForm();
    wireK8sConfigForm();
    return;
  }
  if (K8S_TAB === "apply") {
    // Apply is a local form; don't claim "connected" without a probe.
    k8sSetStatus(k8sT("k8s.status_ready", "可操作"), "warn");
    paintK8sApply();
    return;
  }
  panel.innerHTML = `<div class="loading-dots">${esc(k8sT("sec.loading", "加载中…"))}</div>`;
  k8sSetStatus(k8sT("k8s.status_loading", "连接中…"), "warn");
  const nsQ = k8sNamespace() ? `?namespace=${encodeURIComponent(k8sNamespace())}` : "";
  const pager = pageReq && pageReq.kind === K8S_TAB ? pageReq : null;
  try {
    if (K8S_TAB === "overview") {
      const j = await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/overview`);
      if (k8sIsStale(seq)) return;
      if (j.reachable === false) {
        k8sSetStatus(k8sT("k8s.status_err", "连接失败"), "err");
        panel.innerHTML = k8sUnreachableHTML(j.error);
        k8sWireUnreachableActions(panel);
        return;
      }
      const ver = (j.version && (j.version.gitVersion || j.version.major)) || "—";
      k8sSetStatus(k8sT("k8s.status_ok", "已连接") + " · " + ver, "ok");
      const nodes = j.nodes || {}, pods = j.pods || {}, deps = j.deployments || {};
      panel.innerHTML = `<div class="sec-stat-row k8s-kpi-row">
        <div class="sec-stat"><div class="sec-stat-n">${esc(String(nodes.ready || 0))}<span class="k8s-kpi-den">/${esc(String(nodes.total || 0))}</span></div><div class="sec-stat-l">${esc(k8sT("k8s.kpi_nodes", "节点 Ready"))}</div></div>
        <div class="sec-stat"><div class="sec-stat-n">${esc(String(pods.running || 0))}<span class="k8s-kpi-den">/${esc(String(pods.total || 0))}</span></div><div class="sec-stat-l">${esc(k8sT("k8s.kpi_pods", "Pod Running"))}</div></div>
        <div class="sec-stat"><div class="sec-stat-n">${esc(String(deps.total || 0))}</div><div class="sec-stat-l">${esc(k8sT("k8s.kpi_deployments", "Deployments"))}</div></div>
        <div class="sec-stat"><div class="sec-stat-n mono" style="font-size:15px">${esc(String(ver))}</div><div class="sec-stat-l">${esc(k8sT("k8s.kpi_version", "版本"))}</div></div>
      </div>
      <p class="ws-help" style="margin-top:4px">${esc(k8sT("k8s.overview_hint", "使用上方命名空间筛选，并在 Pods / Deployments / Events 标签中搜索与操作。"))}</p>`;
      return;
    }
    if (K8S_TAB === "nodes") {
      const j = await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/nodes`);
      if (k8sIsStale(seq)) return;
      K8S_CACHE.nodes = j.items || [];
      k8sSetStatus(k8sT("k8s.status_ok", "已连接"), "ok");
      paintK8sNodes();
      return;
    }
    if (K8S_TAB === "pods") {
      if (!pager) k8sResetPager("pods");
      const cont = pager ? (pager.continueToken || "") : "";
      const j = await k8sFetch(k8sPagedResourcePath(id, "pods", cont));
      if (k8sIsStale(seq)) return;
      K8S_CACHE.pods = j.items || [];
      K8S_CACHE.podsPageContinue = cont;
      K8S_CACHE.podsContinue = j.continue || "";
      K8S_CACHE.podsTruncated = !!j.truncated;
      k8sSetStatus(k8sT("k8s.status_ok", "已连接"), "ok");
      paintK8sPods();
      return;
    }
    if (K8S_TAB === "deployments") {
      if (!pager) k8sResetPager("deployments");
      const cont = pager ? (pager.continueToken || "") : "";
      const j = await k8sFetch(k8sPagedResourcePath(id, "deployments", cont));
      if (k8sIsStale(seq)) return;
      K8S_CACHE.deployments = j.items || [];
      K8S_CACHE.deploymentsPageContinue = cont;
      K8S_CACHE.deploymentsContinue = j.continue || "";
      K8S_CACHE.deploymentsTruncated = !!j.truncated;
      k8sSetStatus(k8sT("k8s.status_ok", "已连接"), "ok");
      paintK8sDeployments();
      return;
    }
    if (K8S_TAB === "events") {
      const j = await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/events${nsQ}`);
      if (k8sIsStale(seq)) return;
      K8S_CACHE.events = j.items || [];
      k8sSetStatus(k8sT("k8s.status_ok", "已连接"), "ok");
      paintK8sEvents();
      return;
    }
  } catch (e) {
    if (k8sIsAbort(e) || k8sIsStale(seq)) return;
    k8sSetStatus(k8sT("k8s.status_err", "连接失败"), "err");
    panel.innerHTML = k8sUnreachableHTML(String(e.message || e));
    k8sWireUnreachableActions(panel);
  }
}

function paintK8sApply() {
  const panel = $("k8sPanel");
  if (!panel) return;
  const allow = (typeof canWrite === "function" && canWrite()) || (typeof isAdmin === "function" && isAdmin());
  panel.innerHTML = `<div class="cfg-panel">
    <div class="cfg-panel-head"><div>
      <div class="cfg-panel-title">${esc(k8sT("k8s.apply_title", "YAML Apply / 创建命名空间"))}</div>
      <p class="cfg-panel-desc">${esc(k8sT("k8s.apply_hint", "粘贴多文档 YAML，经 kubectl apply 下发（需服务端安装 kubectl）。高危变更请先 Dry-run。集群「创建」指纳管已有 API；可在此创建 Namespace。"))}</p>
    </div></div>
    <div class="rtx-toolbar" style="margin-bottom:8px;gap:8px;flex-wrap:wrap">
      <input type="text" id="k8sApplyNs" class="hw-search" placeholder="${esc(k8sT("k8s.apply_ns_ph", "默认命名空间（可选）"))}" style="max-width:200px" value="${esc(k8sNamespace() || "")}">
      <input type="text" id="k8sNewNs" class="hw-search" placeholder="${esc(k8sT("k8s.new_ns_ph", "新建 Namespace 名"))}" style="max-width:180px">
      ${allow ? `<button type="button" class="btn sm" id="k8sCreateNsBtn">${esc(k8sT("k8s.create_ns", "创建 Namespace"))}</button>` : ""}
    </div>
    <textarea id="k8sApplyYAML" rows="16" class="mono" style="width:100%;padding:10px;border:1px solid var(--line);border-radius:8px;background:var(--panel2);font-size:12px" placeholder="apiVersion: v1&#10;kind: ConfigMap&#10;metadata:&#10;  name: demo"></textarea>
    <div style="margin-top:10px;display:flex;gap:8px;flex-wrap:wrap">
      ${allow ? `<button type="button" class="btn sm" id="k8sDryRunBtn">${esc(k8sT("k8s.dry_run", "Dry-run"))}</button>
      <button type="button" class="btn sm primary" id="k8sApplyBtn">${esc(k8sT("k8s.apply", "Apply"))}</button>` : `<span class="muted">${esc(k8sT("k8s.apply_readonly", "只读用户不可 Apply"))}</span>`}
    </div>
    <pre id="k8sApplyOut" class="mono" style="margin-top:10px;min-height:60px;max-height:260px;overflow:auto;padding:10px;border:1px solid var(--line);border-radius:8px;background:var(--panel2);font-size:12px;white-space:pre-wrap"></pre>
  </div>`;
  const run = async (dry) => {
    const id = k8sClusterId();
    const yaml = ($("k8sApplyYAML") || {}).value || "";
    const ns = (($("k8sApplyNs") || {}).value || "").trim();
    const out = $("k8sApplyOut");
    if (!yaml.trim()) { toast(k8sT("k8s.apply_empty", "请粘贴 YAML"), "err"); return; }
    if (!dry && !confirm(k8sT("k8s.apply_confirm", "确认 Apply 到集群？"))) return;
    if (out) out.textContent = k8sT("sec.loading", "加载中…");
    try {
      const j = await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/apply`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ yaml, namespace: ns, dry_run: !!dry }),
      });
      if (out) out.textContent = j.output || "ok";
      toast(dry ? "Dry-run OK" : k8sT("k8s.apply_ok", "Apply 完成"), "ok");
    } catch (e) {
      if (out) out.textContent = String(e.message || e);
      toast(String(e.message || e), "err");
    }
  };
  const dryBtn = $("k8sDryRunBtn");
  if (dryBtn) dryBtn.onclick = () => run(true);
  const applyBtn = $("k8sApplyBtn");
  if (applyBtn) applyBtn.onclick = () => run(false);
  const nsBtn = $("k8sCreateNsBtn");
  if (nsBtn) nsBtn.onclick = async () => {
    const id = k8sClusterId();
    const name = (($("k8sNewNs") || {}).value || "").trim();
    if (!name) { toast(k8sT("k8s.new_ns_ph", "新建 Namespace 名"), "err"); return; }
    if (!confirm(k8sT("k8s.create_ns_confirm", "确认创建 Namespace？") + " " + name)) return;
    const out = $("k8sApplyOut");
    try {
      const j = await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/namespaces`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name }),
      });
      if (out) out.textContent = j.output || "ok";
      toast(k8sT("k8s.create_ns_ok", "Namespace 已创建"), "ok");
      loadK8sNamespaces();
    } catch (e) {
      if (out) out.textContent = String(e.message || e);
      toast(String(e.message || e), "err");
    }
  };
}

async function openK8sPodLog(ns, name) {
  const id = k8sClusterId();
  if (!id || !ns || !name) return;
  const title = $("k8sLogTitle");
  const body = $("k8sLogBody");
  if (title) title.textContent = `Pod ${ns}/${name}`;
  if (body) body.textContent = k8sT("sec.loading", "加载中…");
  $("k8sLogMask")?.classList.add("show");
  try {
    const j = await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/pods/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/log?tail=300`);
    if (body) body.textContent = j.log || "(empty)";
  } catch (e) {
    if (body) body.textContent = String(e.message || e);
  }
}

function openK8sScale(ns, name, current) {
  K8S_SCALE_CTX = { ns, name };
  const hint = $("k8sScaleHint");
  if (hint) hint.textContent = `${ns}/${name} · ${k8sT("k8s.current_replicas", "当前副本")} ${current}`;
  if ($("k8sScaleReplicas")) $("k8sScaleReplicas").value = String(current);
  $("k8sScaleMask")?.classList.add("show");
}

function k8sClusterEndpointLabel(c) {
  if (!c) return "—";
  const api = (c.api_server || c.endpoint || "").trim();
  if (api) return api;
  if (c.has_kubeconfig || c.kubeconfig_yaml) {
    return k8sT("k8s.endpoint_kubeconfig_only", "kubeconfig（未解析到 API Server）");
  }
  return "—";
}

function k8sSecretBadge(configured, label) {
  if (configured) return `<span class="badge ok">${esc(label)}</span>`;
  return `<span class="badge">${esc(k8sT("k8s.secret_none", "未配置"))}</span>`;
}

function k8sFillConfigForm(c) {
  c = c || {};
  K8S_EDIT_ID = c.id || "";
  const idEl = $("k8sCfgId");
  const nameEl = $("k8sCfgName");
  const enEl = $("k8sCfgEnabled");
  const apiEl = $("k8sCfgAPI");
  const tokEl = $("k8sCfgToken");
  const caEl = $("k8sCfgCA");
  const insecureEl = $("k8sCfgInsecure");
  const kubeEl = $("k8sCfgKube");
  const nsEl = $("k8sCfgNS");
  const titleEl = $("k8sCfgFormTitle");
  const metaEl = $("k8sCfgFormMeta");
  const form = $("k8sCfgForm");
  if (!idEl || !nameEl || !form) return false;

  idEl.value = c.id || "";
  nameEl.value = c.name || "";
  if (enEl) enEl.checked = c.enabled !== false;
  if (apiEl) apiEl.value = c.api_server || "";
  const hasTok = !!(c.has_token || c.token === "****");
  const hasKube = !!(c.has_kubeconfig || c.kubeconfig_yaml === "****");
  const hasCA = !!(c.has_ca || (c.ca_cert && c.ca_cert !== "****"));
  if (tokEl) tokEl.value = hasTok ? "****" : "";
  if (caEl) caEl.value = c.ca_cert && c.ca_cert !== "****" ? c.ca_cert : "";
  if (insecureEl) insecureEl.checked = !!c.insecure_skip_tls;
  if (kubeEl) kubeEl.value = hasKube ? "****" : "";
  if (nsEl) nsEl.value = c.default_namespace || "";

  if (titleEl) {
    titleEl.textContent = c.id
      ? k8sT("k8s.cfg_edit_title", "编辑集群") + " · " + (c.name || c.id)
      : k8sT("k8s.cfg_create_title", "新建集群");
  }
  if (metaEl) {
    metaEl.innerHTML = c.id
      ? `${k8sSecretBadge(hasTok, k8sT("k8s.secret_token", "Token 已配置"))}
         ${k8sSecretBadge(hasKube, k8sT("k8s.secret_kube", "Kubeconfig 已配置"))}
         ${k8sSecretBadge(hasCA, k8sT("k8s.secret_ca", "CA 已配置"))}
         ${c.insecure_skip_tls ? `<span class="badge warn">${esc(k8sT("k8s.insecure_on", "已跳过 TLS"))}</span>` : ""}`
      : `<span class="muted">${esc(k8sT("k8s.cfg_create_hint", "填写 API Server + Token，或粘贴 kubeconfig"))}</span>`;
  }
  form.classList.toggle("k8s-cfg-editing", !!c.id);
  document.querySelectorAll("#k8sPanel tr[data-k8s-row]").forEach(tr => {
    tr.classList.toggle("active-row", !!c.id && tr.getAttribute("data-k8s-row") === c.id);
  });
  try {
    form.scrollIntoView({ behavior: "smooth", block: "start" });
    nameEl.focus({ preventScroll: true });
  } catch (_) {
    try { form.scrollIntoView(); nameEl.focus(); } catch (__) {}
  }
  return true;
}

function k8sWireSecretPlaceholders() {
  ["k8sCfgToken", "k8sCfgKube"].forEach(id => {
    const el = $(id);
    if (!el || el.dataset.k8sSecretWired === "1") return;
    el.dataset.k8sSecretWired = "1";
    el.addEventListener("focus", () => {
      if (el.value === "****") el.value = "";
    });
  });
}

function renderK8sConfigForm() {
  if (typeof isAdmin === "function" && !isAdmin()) {
    return `<div class="empty-state k8s-empty"><h4>${esc(k8sT("toast.admin_only", "仅管理员可操作"))}</h4>
      <p>${esc(k8sT("k8s.cfg_admin_hint", "集群配置仅管理员可查看与修改。"))}</p></div>`;
  }
  const list = K8S_CLUSTERS.map(c => {
    const active = K8S_EDIT_ID && K8S_EDIT_ID === c.id ? " active-row" : "";
    const auth = [];
    if (c.has_token || c.token === "****") auth.push("Token");
    if (c.has_kubeconfig || c.kubeconfig_yaml === "****") auth.push("Kubeconfig");
    if (c.has_ca || c.ca_cert) auth.push("CA");
    if (c.insecure_skip_tls) auth.push("Insecure");
    return `<tr class="sec-row${active}" data-k8s-row="${esc(c.id)}">
      <td><div class="hs-host-name">${esc(c.name || c.id)}</div>
        <div class="muted" style="font-size:11px;margin-top:2px">${esc(auth.join(" · ") || "—")}</div></td>
      <td class="mono">${esc(k8sClusterEndpointLabel(c))}</td>
      <td>${c.enabled !== false ? `<span class="badge ok">${esc(k8sT("k8s.col_enabled", "启用"))}</span>` : `<span class="badge">${esc(k8sT("k8s.off", "停用"))}</span>`}</td>
      <td>
        <div class="k8s-actions">
          <button type="button" class="btn sm primary" data-k8s-edit="${esc(c.id)}">${esc(k8sT("ui.edit", "编辑"))}</button>
          <button type="button" class="btn sm" data-k8s-test="${esc(c.id)}">${esc(k8sT("k8s.test", "连通测试"))}</button>
          <button type="button" class="btn sm danger" data-k8s-del="${esc(c.id)}">${esc(k8sT("ui.delete", "删除"))}</button>
        </div>
      </td>
    </tr>`;
  }).join("");
  const editing = !!(K8S_EDIT_ID && K8S_CLUSTERS.some(c => c.id === K8S_EDIT_ID));
  return `<div class="cfg-panel k8s-cfg">
    <div class="cfg-panel-head" style="align-items:center;gap:10px;flex-wrap:wrap">
      <div>
        <div class="cfg-panel-title">${esc(k8sT("k8s.tab_config", "集群配置"))}</div>
        <p class="cfg-panel-desc">${esc(k8sT("k8s.config_hint", "推荐使用只读 ServiceAccount Token；Scale/Restart 需额外 patch 权限。密钥显示 **** 表示已配置，聚焦后可粘贴新值覆盖。"))}</p>
      </div>
      <button type="button" class="btn sm primary" id="k8sCfgNewBtn">${esc(k8sT("k8s.cfg_new", "新建集群"))}</button>
    </div>
    <div class="nf-table-wrap k8s-table-wrap" style="margin-bottom:14px"><table class="data-table k8s-table">
      <thead><tr>
        <th>${esc(k8sT("k8s.col_name", "名称"))}</th>
        <th>${esc(k8sT("k8s.col_endpoint", "Endpoint"))}</th>
        <th>${esc(k8sT("k8s.col_enabled", "启用"))}</th>
        <th>${esc(k8sT("k8s.col_actions", "操作"))}</th>
      </tr></thead>
      <tbody>${list || `<tr><td colspan="4" class="empty-line">${esc(k8sT("k8s.empty", "暂无数据"))}</td></tr>`}</tbody>
    </table></div>
    <div class="cfg-form ${editing ? "k8s-cfg-editing" : ""}" id="k8sCfgForm">
      <div class="k8s-cfg-form-head">
        <div class="cfg-panel-title" id="k8sCfgFormTitle">${esc(editing ? k8sT("k8s.cfg_edit_title", "编辑集群") : k8sT("k8s.cfg_create_title", "新建集群"))}</div>
        <div class="k8s-cfg-meta" id="k8sCfgFormMeta"></div>
      </div>
      <input type="hidden" id="k8sCfgId" value="">
      <div class="cfg-form-row">
        <div class="field"><label>${esc(k8sT("k8s.col_name", "名称"))}</label><input id="k8sCfgName" type="text" autocomplete="off" placeholder="prod-k8s"></div>
        <div class="field cfg-field-switch"><label class="switch"><input type="checkbox" id="k8sCfgEnabled" checked><span>${esc(k8sT("k8s.col_enabled", "启用"))}</span></label></div>
      </div>
      <div class="field"><label>API Server</label>
        <input id="k8sCfgAPI" class="mono" type="text" inputmode="url" placeholder="https://192.168.x.x:6443" autocomplete="off">
        <p class="field-hint">${esc(k8sT("k8s.cfg_api_hint", "使用 Kubeconfig 时可留空；否则必填，需本平台服务端网络可达。"))}</p>
      </div>
      <div class="field"><label>Token</label>
        <input id="k8sCfgToken" class="mono" type="password" placeholder="**** / ${esc(k8sT("k8s.secret_keep", "留空或 **** 表示保持原值"))}" autocomplete="new-password">
      </div>
      <div class="field"><label>CA Cert (PEM)</label>
        <textarea id="k8sCfgCA" class="mono" rows="4" spellcheck="false" placeholder="-----BEGIN CERTIFICATE-----"></textarea>
      </div>
      <label class="switch mb"><input type="checkbox" id="k8sCfgInsecure"><span>${esc(k8sT("k8s.insecure", "跳过 TLS 校验（仅内网临时）"))}</span></label>
      <div class="field"><label>Kubeconfig YAML</label>
        <textarea id="k8sCfgKube" class="mono" rows="6" spellcheck="false" placeholder="${esc(k8sT("k8s.cfg_kube_ph", "粘贴完整 kubeconfig；已配置时显示 ****，聚焦后可覆盖"))}"></textarea>
        <p class="field-hint">${esc(k8sT("k8s.cfg_kube_hint", "若填写 kubeconfig，将优先于上方 API Server / Token / CA。"))}</p>
      </div>
      <div class="field"><label>${esc(k8sT("k8s.default_ns", "默认命名空间（空=全部）"))}</label>
        <input id="k8sCfgNS" class="mono" type="text" placeholder="default" autocomplete="off">
      </div>
      <div class="cfg-actions">
        <button type="button" class="btn" id="k8sCfgReset">${esc(k8sT("ui.reset", "重置为空"))}</button>
        <button type="button" class="btn primary" id="k8sCfgSave">${esc(k8sT("settings.save", "保存"))}</button>
      </div>
    </div>
  </div>`;
}

async function k8sLoadClusterForEdit(id) {
  id = String(id || "").trim();
  if (!id) {
    k8sFillConfigForm(null);
    return;
  }
  let c = K8S_CLUSTERS.find(x => x.id === id) || { id };
  try {
    const fresh = await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}`, { noAbort: true });
    if (fresh && fresh.id) {
      c = fresh;
      const idx = K8S_CLUSTERS.findIndex(x => x.id === id);
      if (idx >= 0) K8S_CLUSTERS[idx] = fresh;
      else K8S_CLUSTERS.push(fresh);
    }
  } catch (e) {
    toast(k8sT("k8s.cfg_load_fail", "加载集群详情失败") + "：" + (e.message || e), "err");
  }
  if (!k8sFillConfigForm(c)) {
    toast(k8sT("k8s.cfg_form_missing", "编辑表单未就绪，请刷新页面后重试"), "err");
    return;
  }
  toast(k8sT("k8s.cfg_loaded", "已载入集群配置，可修改后保存"), "ok");
}

function wireK8sConfigForm() {
  const panel = $("k8sPanel");
  if (!panel) return;
  k8sWireSecretPlaceholders();

  const resetBtn = $("k8sCfgReset");
  if (resetBtn) resetBtn.onclick = () => {
    K8S_EDIT_ID = "";
    k8sFillConfigForm(null);
  };
  const newBtn = $("k8sCfgNewBtn");
  if (newBtn) newBtn.onclick = () => {
    K8S_EDIT_ID = "";
    k8sFillConfigForm(null);
    toast(k8sT("k8s.cfg_new_ready", "已切换到新建集群"), "ok");
  };
  const saveBtn = $("k8sCfgSave");
  if (saveBtn) saveBtn.onclick = async () => {
    const tokenVal = ($("k8sCfgToken")?.value || "").trim();
    const kubeVal = ($("k8sCfgKube")?.value || "").trim();
    const body = {
      id: ($("k8sCfgId")?.value || "").trim(),
      name: ($("k8sCfgName")?.value || "").trim(),
      enabled: !!$("k8sCfgEnabled")?.checked,
      api_server: ($("k8sCfgAPI")?.value || "").trim(),
      token: tokenVal,
      ca_cert: ($("k8sCfgCA")?.value || "").trim(),
      insecure_skip_tls: !!$("k8sCfgInsecure")?.checked,
      kubeconfig_yaml: kubeVal,
      default_namespace: ($("k8sCfgNS")?.value || "").trim(),
    };
    if (!body.name) {
      toast(k8sT("k8s.cfg_name_required", "请填写集群名称"), "err");
      $("k8sCfgName")?.focus();
      return;
    }
    const hasNewKube = !!(kubeVal && !kubeVal.includes("****"));
    const hasNewTok = !!(tokenVal && !tokenVal.includes("****"));
    if (!body.id && !hasNewKube && !(body.api_server && hasNewTok)) {
      toast(k8sT("k8s.cfg_auth_required", "请填写 API Server + Token，或粘贴 kubeconfig"), "err");
      return;
    }
    try {
      const path = body.id ? `/k8s/clusters/${encodeURIComponent(body.id)}` : "/k8s/clusters";
      const method = body.id ? "PUT" : "POST";
      const saved = await k8sFetch(path, {
        method, headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body), noAbort: true,
      });
      toast(k8sT("toast.saved", "已保存"), "ok");
      K8S_TAB = "config";
      K8S_EDIT_ID = (saved && saved.id) || body.id || "";
      document.querySelectorAll("#k8sInnerTabs .tab").forEach(b => b.classList.toggle("active", b.dataset.k8sTab === "config"));
      await loadK8sClusters();
      await renderK8sPanel();
      if (K8S_EDIT_ID) {
        const c = K8S_CLUSTERS.find(x => x.id === K8S_EDIT_ID);
        if (c) k8sFillConfigForm(c);
      }
      loadK8sNamespaces().catch(() => {});
    } catch (e) { toast(String(e.message || e), "err"); }
  };

  // Event delegation: survives partial DOM quirks better than per-button onclick alone.
  panel.onclick = async (e) => {
    const t = e.target && e.target.closest ? e.target.closest("[data-k8s-edit],[data-k8s-test],[data-k8s-del]") : null;
    if (!t || !panel.contains(t)) return;
    e.preventDefault();
    e.stopPropagation();
    if (t.hasAttribute("data-k8s-edit")) {
      await k8sLoadClusterForEdit(t.getAttribute("data-k8s-edit"));
      return;
    }
    if (t.hasAttribute("data-k8s-test")) {
      try {
        const j = await k8sFetch(`/k8s/clusters/${encodeURIComponent(t.getAttribute("data-k8s-test"))}/test`, {
          method: "POST", body: "{}", noAbort: true,
        });
        const ver = j.version?.gitVersion || "ok";
        toast(k8sT("k8s.test_ok", "连通成功") + " · " + ver, "ok");
      } catch (err) { toast(String(err.message || err), "err"); }
      return;
    }
    if (t.hasAttribute("data-k8s-del")) {
      if (!confirm(k8sT("k8s.del_confirm", "确定删除该集群配置？"))) return;
      try {
        await k8sFetch(`/k8s/clusters/${encodeURIComponent(t.getAttribute("data-k8s-del"))}`, { method: "DELETE", noAbort: true });
        toast(k8sT("toast.deleted", "已删除"), "ok");
        if (K8S_EDIT_ID === t.getAttribute("data-k8s-del")) K8S_EDIT_ID = "";
        K8S_TAB = "config";
        await loadK8sClusters();
        await renderK8sPanel();
      } catch (err) { toast(String(err.message || err), "err"); }
    }
  };

  // Restore editing selection after re-render.
  if (K8S_EDIT_ID) {
    const c = K8S_CLUSTERS.find(x => x.id === K8S_EDIT_ID);
    if (c) k8sFillConfigForm(c);
    else {
      K8S_EDIT_ID = "";
      k8sFillConfigForm(null);
    }
  } else {
    k8sFillConfigForm(null);
  }
}

async function loadK8sPage() {
  const panel = $("k8sPanel");
  try {
    k8sSetStatus(k8sT("k8s.status_loading", "连接中…"), "warn");
    if (panel && K8S_TAB !== "config") {
      panel.innerHTML = `<div class="loading-dots">${esc(k8sT("sec.loading", "加载中…"))}</div>`;
    }
    await loadK8sClusters();
    // Config tab is local — paint immediately; namespaces refresh in background.
    if (K8S_TAB === "config") {
      await renderK8sPanel();
      loadK8sNamespaces().catch(() => {});
      return;
    }
    // Don't block overview/resource tabs on namespaces (can hang ~dial timeout).
    const nsPromise = loadK8sNamespaces().catch(() => {});
    await renderK8sPanel();
    await nsPromise;
  } catch (e) {
    if (k8sIsAbort(e)) return;
    k8sSetStatus(k8sT("k8s.status_err", "连接失败"), "err");
    if (panel) {
      panel.innerHTML = k8sUnreachableHTML(String(e.message || e));
      k8sWireUnreachableActions(panel);
    }
  }
}

window._pageRenderers = window._pageRenderers || {};
window._pageRenderers.k8s = loadK8sPage;

document.addEventListener("DOMContentLoaded", () => {
  document.querySelectorAll("#k8sInnerTabs .tab").forEach(b => {
    b.addEventListener("click", () => switchK8sTab(b.dataset.k8sTab));
  });
  $("k8sRefreshBtn")?.addEventListener("click", () => loadK8sPage());
  $("k8sClusterSel")?.addEventListener("change", async () => {
    const panel = $("k8sPanel");
    if (panel && K8S_TAB !== "config" && K8S_TAB !== "apply") {
      panel.innerHTML = `<div class="loading-dots">${esc(k8sT("sec.loading", "加载中…"))}</div>`;
      k8sSetStatus(k8sT("k8s.status_loading", "连接中…"), "warn");
    }
    const nsPromise = loadK8sNamespaces().catch(() => {});
    await renderK8sPanel();
    await nsPromise;
  });
  $("k8sNsSel")?.addEventListener("change", () => renderK8sPanel());
  $("k8sScaleConfirm")?.addEventListener("click", async () => {
    const id = k8sClusterId();
    if (!id || !K8S_SCALE_CTX) return;
    const replicas = parseInt($("k8sScaleReplicas")?.value || "0", 10);
    if (!confirm(k8sT("k8s.scale_confirm_q", "确认将 {name} 调整为 {n} 副本？")
      .replace("{name}", K8S_SCALE_CTX.name).replace("{n}", String(replicas)))) return;
    try {
      await k8sFetch(`/k8s/clusters/${encodeURIComponent(id)}/deployments/${encodeURIComponent(K8S_SCALE_CTX.ns)}/${encodeURIComponent(K8S_SCALE_CTX.name)}/scale`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ replicas }),
      });
      toast(k8sT("k8s.scale_ok", "Scale 已提交"), "ok");
      $("k8sScaleMask")?.classList.remove("show");
      renderK8sPanel();
    } catch (e) { toast(String(e.message || e), "err"); }
  });
});
